// Package privatenet models the private side of the host's network: the
// private addresses of its interfaces and whether its default gateways are
// reachable.
//
// Private addresses are those reported by [netip.Addr.IsPrivate]: the IPv4
// ranges 10.0.0.0/8, 172.16.0.0/12 and 192.168.0.0/16 and IPv6 unique local
// addresses (fc00::/7). Public IPv6 addresses belong to the publicnet domain.
//
// A [Snapshot] groups the probes of a single monitoring run and is persisted
// through a [SnapshotRepository].
package privatenet

import (
	"cmp"
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

// ErrInvalidSnapshot is returned by [NewSnapshot] when a probe is inconsistent.
// Every validation error wraps it.
var ErrInvalidSnapshot = errors.New("invalid private net snapshot")

// InterfaceAddr is a private address assigned to a network interface.
type InterfaceAddr struct {
	// Interface is the name of the interface, for example "en0" or "eth0".
	Interface string
	// Prefix is the address together with the length of its subnet, for
	// example 192.168.178.23/24.
	Prefix netip.Prefix
}

// String returns the interface name and prefix, for example
// "en0 192.168.178.23/24".
func (a InterfaceAddr) String() string {
	return a.Interface + " " + a.Prefix.String()
}

// CompareInterfaceAddrs orders addresses by interface name, then address,
// then prefix length. It suits [slices.SortFunc].
func CompareInterfaceAddrs(a, b InterfaceAddr) int {
	return cmp.Or(
		strings.Compare(a.Interface, b.Interface),
		a.Prefix.Addr().Compare(b.Prefix.Addr()),
		cmp.Compare(a.Prefix.Bits(), b.Prefix.Bits()),
	)
}

// DiffAddrs returns the addresses in after that are not in before (added) and
// those in before that are not in after (removed), each in the order of its
// input.
func DiffAddrs(before, after []InterfaceAddr) ([]InterfaceAddr, []InterfaceAddr) {
	return missingFrom(before, after), missingFrom(after, before)
}

func missingFrom(set, addrs []InterfaceAddr) []InterfaceAddr {
	var missing []InterfaceAddr

	for _, addr := range addrs {
		if !slices.Contains(set, addr) {
			missing = append(missing, addr)
		}
	}

	return missing
}

// AddrsProbe is the result of reading the private addresses of the host's
// interfaces.
type AddrsProbe struct {
	probe.Result

	// Addrs are the private addresses found. [NewSnapshot] sorts them with
	// [CompareInterfaceAddrs] and removes duplicates. They are set only when
	// the status is [probe.StatusOK].
	Addrs []InterfaceAddr
}

// GatewayProbe is the result of finding the default gateway of one IP family
// and pinging it.
type GatewayProbe struct {
	probe.Result

	// Addr is the gateway's address. IPv6 gateways are usually link-local
	// addresses with a zone, for example fe80::1%en0. It is set whenever a
	// gateway was found, even if pinging it failed.
	Addr netip.Addr
	// Ping holds the echo request counts and round-trip times.
	Ping probe.PingStats
}

// Reachable reports whether the gateway answered.
func (p GatewayProbe) Reachable() bool {
	return p.Status == probe.StatusOK
}

// Snapshot is the state of the host's private network at one point in time.
type Snapshot struct {
	ID          uuid.UUID
	Addrs       AddrsProbe
	GatewayIPv4 GatewayProbe
	GatewayIPv6 GatewayProbe
	Timestamp   time.Time
}

// NewSnapshot validates the probes and returns a snapshot with a new
// time-ordered ID and the timestamp converted to UTC.
//
// The addresses are copied, sorted and deduplicated, and an IPv4-mapped IPv6
// gateway is converted to a plain IPv4 address. All validation errors are
// joined and wrap [ErrInvalidSnapshot].
func NewSnapshot(timestamp time.Time, addrs AddrsProbe, gatewayIPv4, gatewayIPv6 GatewayProbe) (*Snapshot, error) {
	if timestamp.IsZero() {
		return nil, fmt.Errorf("%w: timestamp is zero", ErrInvalidSnapshot)
	}

	addrs, errAddrs := addrs.normalized()
	gatewayIPv4, errIPv4 := gatewayIPv4.normalized("gateway ipv4", netip.Addr.Is4)
	gatewayIPv6, errIPv6 := gatewayIPv6.normalized("gateway ipv6", netip.Addr.Is6)

	err := errors.Join(errAddrs, errIPv4, errIPv6)
	if err != nil {
		return nil, err
	}

	return &Snapshot{
		ID:          uuid.NewV7(),
		Addrs:       addrs,
		GatewayIPv4: gatewayIPv4,
		GatewayIPv6: gatewayIPv6,
		Timestamp:   timestamp.UTC(),
	}, nil
}

func (p AddrsProbe) normalized() (AddrsProbe, error) {
	err := p.Validate()
	if err != nil {
		return AddrsProbe{}, fmt.Errorf("%w: addrs: %w", ErrInvalidSnapshot, err)
	}

	if p.Status != probe.StatusOK && len(p.Addrs) > 0 {
		return AddrsProbe{}, fmt.Errorf(
			"%w: addrs: %d addresses set on probe with status %s",
			ErrInvalidSnapshot, len(p.Addrs), p.Status,
		)
	}

	var errs []error

	for _, addr := range p.Addrs {
		switch {
		case addr.Interface == "":
			errs = append(errs, fmt.Errorf("%w: addrs: address %s has no interface", ErrInvalidSnapshot, addr.Prefix))
		case !addr.Prefix.IsValid():
			errs = append(errs, fmt.Errorf("%w: addrs: %s: invalid prefix", ErrInvalidSnapshot, addr.Interface))
		case !addr.Prefix.Addr().IsPrivate():
			errs = append(errs, fmt.Errorf("%w: addrs: %s is not private", ErrInvalidSnapshot, addr))
		}
	}

	err = errors.Join(errs...)
	if err != nil {
		return AddrsProbe{}, err
	}

	p.Addrs = slices.Clone(p.Addrs)
	slices.SortFunc(p.Addrs, CompareInterfaceAddrs)
	p.Addrs = slices.Compact(p.Addrs)

	return p, nil
}

func (p GatewayProbe) normalized(name string, inFamily func(netip.Addr) bool) (GatewayProbe, error) {
	err := p.Validate()
	if err != nil {
		return GatewayProbe{}, fmt.Errorf("%w: %s: %w", ErrInvalidSnapshot, name, err)
	}

	if p.Status == probe.StatusDisabled {
		return p, nil
	}

	p.Addr = p.Addr.Unmap()

	err = p.Ping.Validate()

	switch {
	case err != nil:
		return GatewayProbe{}, fmt.Errorf("%w: %s: %w", ErrInvalidSnapshot, name, err)
	case p.Status == probe.StatusOK && !p.Addr.IsValid():
		return GatewayProbe{}, fmt.Errorf("%w: %s: missing address", ErrInvalidSnapshot, name)
	case p.Addr.IsValid() && !inFamily(p.Addr):
		return GatewayProbe{}, fmt.Errorf("%w: %s: address %s has wrong family", ErrInvalidSnapshot, name, p.Addr)
	case p.Status == probe.StatusOK && p.Ping.PacketsReceived == 0:
		return GatewayProbe{}, fmt.Errorf("%w: %s: status ok but no packets received", ErrInvalidSnapshot, name)
	}

	return p, nil
}

// SnapshotRepository persists snapshots.
type SnapshotRepository interface {
	SavePrivateNetSnapshot(ctx context.Context, snap *Snapshot) error
}
