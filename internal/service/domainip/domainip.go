// Package domainip takes snapshots of the addresses that configured domains
// resolve to. It looks up every domain, turns the outcomes into a
// [domain.Snapshot] and saves it to a [domain.SnapshotRepository].
package domainip

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/devusSs/netward/internal/adapter/resolver"
	domain "github.com/devusSs/netward/internal/domain/domainip"
	"github.com/devusSs/netward/internal/domain/probe"
	"github.com/devusSs/netward/internal/service/probelog"
)

// Resolver resolves domains to addresses. [resolver.Resolver] implements it.
type Resolver interface {
	Lookup(ctx context.Context, domain string) ([]netip.Addr, error)
	Name() string
}

// Config configures a [Service].
type Config struct {
	// Resolver resolves the domains. It is required.
	Resolver Resolver
	// Domains are the domains to monitor. At least one is required; they are
	// normalized with [domain.NormalizeDomain] and deduplicated.
	Domains []string
	// Repository stores the snapshots. It is required.
	Repository domain.SnapshotRepository
	// Logger receives the service's logs. Nil discards them.
	Logger *slog.Logger
	// Now returns the current time. Nil uses [time.Now].
	Now func() time.Time
}

// Service takes domain ip snapshots. It is safe for concurrent use.
type Service struct {
	resolver Resolver
	domains  []string
	repo     domain.SnapshotRepository
	logger   *slog.Logger
	now      func() time.Time

	// mu guards last, the addresses of each domain's last successful lookup,
	// used to log changes.
	mu   sync.Mutex
	last map[string][]netip.Addr
}

// New returns a service for cfg. It fails when the resolver or repository is
// missing, no domain is configured or a domain is invalid.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Resolver == nil:
		return nil, errors.New("resolver is required")
	case cfg.Repository == nil:
		return nil, errors.New("repository is required")
	case len(cfg.Domains) == 0:
		return nil, errors.New("at least one domain is required")
	}

	domains := make([]string, 0, len(cfg.Domains))

	for _, d := range cfg.Domains {
		name, err := domain.NormalizeDomain(d)
		if err != nil {
			return nil, fmt.Errorf("invalid domain: %w", err)
		}

		domains = append(domains, name)
	}

	slices.Sort(domains)

	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	return &Service{
		resolver: cfg.Resolver,
		domains:  slices.Compact(domains),
		repo:     cfg.Repository,
		logger:   logger.With(slog.String("component", "domainip")),
		now:      now,
		mu:       sync.Mutex{},
		last:     make(map[string][]netip.Addr),
	}, nil
}

// Domains returns the normalized domains the service monitors.
func (s *Service) Domains() []string {
	return slices.Clone(s.domains)
}

// TakeSnapshot looks up all domains concurrently, saves the resulting
// snapshot and returns it.
//
// A failing lookup does not fail the snapshot; its outcome is recorded in the
// lookup. TakeSnapshot fails only when ctx ends before the lookups finish or
// when the snapshot cannot be built or saved.
func (s *Service) TakeSnapshot(ctx context.Context) (*domain.Snapshot, error) {
	start := s.now()

	s.logger.DebugContext(ctx, "taking snapshot",
		slog.String("resolver", s.resolver.Name()),
		slog.Int("domains", len(s.domains)),
	)

	var wg sync.WaitGroup

	lookups := make([]domain.LookupProbe, len(s.domains))

	for i, d := range s.domains {
		wg.Go(func() { lookups[i] = s.lookup(ctx, d) })
	}

	wg.Wait()

	// Lookups cut short by ctx would be recorded as failures, which says more
	// about the shutdown than about the domains.
	err := ctx.Err()
	if err != nil {
		return nil, fmt.Errorf("take snapshot: %w", err)
	}

	snap, err := domain.NewSnapshot(start, s.resolver.Name(), lookups)
	if err != nil {
		return nil, fmt.Errorf("build snapshot: %w", err)
	}

	err = s.repo.SaveDomainIPSnapshot(ctx, snap)
	if err != nil {
		return nil, fmt.Errorf("save snapshot %s: %w", snap.ID, err)
	}

	s.logChanges(ctx, snap)

	attrs := []any{
		slog.String("id", snap.ID.String()),
		slog.Duration("duration", s.now().Sub(start)),
		slog.String("resolver", snap.Resolver),
	}

	for _, l := range snap.Lookups {
		attrs = append(attrs, slog.Group(l.Domain,
			slog.String("status", l.Status.String()),
			slog.Any("addrs", addrStrings(l.Addrs)),
		))
	}

	s.logger.InfoContext(ctx, "snapshot taken", attrs...)

	return snap, nil
}

func (s *Service) lookup(ctx context.Context, name string) domain.LookupProbe {
	logger := s.logger.With(slog.String("domain", name))
	logger.DebugContext(ctx, "lookup started")

	start := s.now()
	addrs, err := s.resolver.Lookup(ctx, name)
	result := probe.NewResult(start, s.now().Sub(start), err, resolver.ErrNotFound)

	switch {
	case err != nil:
		addrs = nil
	case len(addrs) == 0:
		result.Status = probe.StatusFailed
		result.Message = "resolver returned no addresses"
	}

	probelog.Log(ctx, logger, result, slog.Any("addrs", addrStrings(addrs)))

	return domain.LookupProbe{Result: result, Domain: name, Addrs: addrs}
}

// logChanges logs the addresses of a domain the first time it resolves and
// every address added or removed after that. Lookups that did not succeed
// are ignored, so a temporary failure is not reported as a change.
func (s *Service) logChanges(ctx context.Context, snap *domain.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, l := range snap.Lookups {
		if l.Status != probe.StatusOK {
			continue
		}

		last, seen := s.last[l.Domain]
		s.last[l.Domain] = l.Addrs

		if !seen {
			s.logger.InfoContext(ctx, "domain resolved",
				slog.String("domain", l.Domain),
				slog.Any("addrs", addrStrings(l.Addrs)),
			)

			continue
		}

		for _, addr := range missingFrom(last, l.Addrs) {
			s.logger.InfoContext(ctx, "domain address added",
				slog.String("domain", l.Domain),
				slog.String("addr", addr.String()),
			)
		}

		for _, addr := range missingFrom(l.Addrs, last) {
			s.logger.InfoContext(ctx, "domain address removed",
				slog.String("domain", l.Domain),
				slog.String("addr", addr.String()),
			)
		}
	}
}

// missingFrom returns the addresses in addrs that are not in set.
func missingFrom(set, addrs []netip.Addr) []netip.Addr {
	var missing []netip.Addr

	for _, addr := range addrs {
		if !slices.Contains(set, addr) {
			missing = append(missing, addr)
		}
	}

	return missing
}

func addrStrings(addrs []netip.Addr) []string {
	result := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		result = append(result, addr.String())
	}

	return result
}
