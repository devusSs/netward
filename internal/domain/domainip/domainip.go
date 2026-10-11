// Package domainip models the IP addresses that configured domains resolve
// to, for example to check that a dynamic DNS name points at the host's
// current public address.
//
// Each domain is looked up once per run and recorded as a [LookupProbe]. A
// [Snapshot] groups the lookups of a single run together with the resolver
// that answered them and is persisted through a [SnapshotRepository].
package domainip

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/devusSs/netward/internal/domain/probe"
)

const (
	maxDomainLen = 253
	maxLabelLen  = 63
)

// ErrInvalidSnapshot is returned by [NewSnapshot] when a lookup is
// inconsistent. Every validation error wraps it.
var ErrInvalidSnapshot = errors.New("invalid domain ip snapshot")

// NormalizeDomain returns domain in lowercase without a trailing dot, or an
// error if it is not a valid host name. Labels may contain letters, digits,
// hyphens and underscores; internationalized names must be given in their
// ASCII (punycode) form.
func NormalizeDomain(domain string) (string, error) {
	name := strings.TrimSuffix(strings.ToLower(domain), ".")

	if name == "" || len(name) > maxDomainLen {
		return "", fmt.Errorf("domain %q: length must be 1 to %d characters", domain, maxDomainLen)
	}

	for label := range strings.SplitSeq(name, ".") {
		if label == "" || len(label) > maxLabelLen {
			return "", fmt.Errorf("domain %q: labels must be 1 to %d characters", domain, maxLabelLen)
		}

		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", fmt.Errorf("domain %q: labels must not start or end with a hyphen", domain)
		}

		for _, r := range label {
			if !isLabelRune(r) {
				return "", fmt.Errorf("domain %q: invalid character %q", domain, r)
			}
		}
	}

	return name, nil
}

func isLabelRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_'
}

// LookupProbe is the result of resolving one domain.
type LookupProbe struct {
	probe.Result

	// Domain is the domain that was looked up, normalized with
	// [NormalizeDomain].
	Domain string
	// Addrs are the IPv4 and IPv6 addresses the domain resolved to.
	// [NewSnapshot] sorts them and removes duplicates. They are set only when
	// the status is [probe.StatusOK], which requires at least one address.
	Addrs []netip.Addr
}

// Snapshot is the state of the configured domains at one point in time.
type Snapshot struct {
	ID uuid.UUID
	// Resolver names the DNS resolver that answered, for example "system" or
	// "1.1.1.1:53".
	Resolver string
	// Lookups holds one probe per domain, sorted by domain.
	Lookups   []LookupProbe
	Timestamp time.Time
}

// NewSnapshot validates the lookups and returns a snapshot with a new
// time-ordered ID and the timestamp converted to UTC.
//
// Domains are normalized, the lookups are sorted by domain and each lookup's
// addresses are copied, converted from IPv4-mapped IPv6 to IPv4, sorted and
// deduplicated. All validation errors are joined and wrap
// [ErrInvalidSnapshot].
func NewSnapshot(timestamp time.Time, resolver string, lookups []LookupProbe) (*Snapshot, error) {
	if timestamp.IsZero() {
		return nil, fmt.Errorf("%w: timestamp is zero", ErrInvalidSnapshot)
	}

	if resolver == "" {
		return nil, fmt.Errorf("%w: resolver is empty", ErrInvalidSnapshot)
	}

	normalized := make([]LookupProbe, 0, len(lookups))

	var errs []error

	for _, raw := range lookups {
		lookup, err := raw.normalized()
		if err != nil {
			errs = append(errs, err)

			continue
		}

		if slices.ContainsFunc(normalized, func(l LookupProbe) bool { return l.Domain == lookup.Domain }) {
			errs = append(errs, fmt.Errorf("%w: %s: duplicate domain", ErrInvalidSnapshot, lookup.Domain))

			continue
		}

		normalized = append(normalized, lookup)
	}

	err := errors.Join(errs...)
	if err != nil {
		return nil, err
	}

	slices.SortFunc(normalized, func(a, b LookupProbe) int { return strings.Compare(a.Domain, b.Domain) })

	return &Snapshot{
		ID:        uuid.NewV7(),
		Resolver:  resolver,
		Lookups:   normalized,
		Timestamp: timestamp.UTC(),
	}, nil
}

func (p LookupProbe) normalized() (LookupProbe, error) {
	domain, err := NormalizeDomain(p.Domain)
	if err != nil {
		return LookupProbe{}, fmt.Errorf("%w: %w", ErrInvalidSnapshot, err)
	}

	p.Domain = domain

	err = p.Validate()
	if err != nil {
		return LookupProbe{}, fmt.Errorf("%w: %s: %w", ErrInvalidSnapshot, domain, err)
	}

	switch {
	case p.Status == probe.StatusOK && len(p.Addrs) == 0:
		return LookupProbe{}, fmt.Errorf("%w: %s: status ok but no addresses", ErrInvalidSnapshot, domain)
	case p.Status != probe.StatusOK && len(p.Addrs) > 0:
		return LookupProbe{}, fmt.Errorf(
			"%w: %s: %d addresses set on lookup with status %s",
			ErrInvalidSnapshot, domain, len(p.Addrs), p.Status,
		)
	}

	addrs := make([]netip.Addr, 0, len(p.Addrs))

	for _, addr := range p.Addrs {
		if !addr.IsValid() || addr.Zone() != "" {
			return LookupProbe{}, fmt.Errorf("%w: %s: invalid address %s", ErrInvalidSnapshot, domain, addr)
		}

		addrs = append(addrs, addr.Unmap())
	}

	if len(addrs) == 0 {
		addrs = nil
	}

	slices.SortFunc(addrs, netip.Addr.Compare)
	p.Addrs = slices.Compact(addrs)

	return p, nil
}

// SnapshotRepository persists snapshots.
type SnapshotRepository interface {
	SaveDomainIPSnapshot(ctx context.Context, snap *Snapshot) error
}
