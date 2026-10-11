// Package publicnet takes snapshots of the host's public network. It runs the
// public address lookups and connectivity checks, turns their outcomes into a
// [domain.Snapshot] and saves it to a [domain.SnapshotRepository].
package publicnet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/devusSs/netward/internal/adapter/iplookup"
	"github.com/devusSs/netward/internal/adapter/ping"
	"github.com/devusSs/netward/internal/domain/probe"
	domain "github.com/devusSs/netward/internal/domain/publicnet"
	"github.com/devusSs/netward/internal/service/probelog"
)

const (
	probeIPv4     = "ipv4"
	probeIPv6     = "ipv6"
	probeConnIPv4 = "conn_ipv4"
	probeConnIPv6 = "conn_ipv6"
)

// IPLookup looks up the host's public address of one IP family.
// [iplookup.Client] implements it.
type IPLookup interface {
	Lookup(ctx context.Context) (netip.Addr, error)
	Endpoint() string
}

// Pinger checks connectivity to a target. [ping.Pinger] implements it.
type Pinger interface {
	Ping(ctx context.Context, target netip.Addr) (ping.Stats, error)
}

// Config configures a [Service]. A nil lookup, a nil pinger or an invalid
// target disables the corresponding probe; it is then recorded as
// [probe.StatusDisabled].
type Config struct {
	// IPv4Lookup looks up the public IPv4 address.
	IPv4Lookup IPLookup
	// IPv6Lookup looks up the public IPv6 address.
	IPv6Lookup IPLookup
	// Pinger checks connectivity to both targets.
	Pinger Pinger
	// IPv4Target is the server pinged over IPv4.
	IPv4Target netip.Addr
	// IPv6Target is the server pinged over IPv6.
	IPv6Target netip.Addr
	// Repository stores the snapshots. It is required.
	Repository domain.SnapshotRepository
	// Logger receives the service's logs. Nil discards them.
	Logger *slog.Logger
	// Now returns the current time. Nil uses [time.Now].
	Now func() time.Time
}

// Service takes public net snapshots. It is safe for concurrent use.
type Service struct {
	ipv4Lookup IPLookup
	ipv6Lookup IPLookup
	pinger     Pinger
	ipv4Target netip.Addr
	ipv6Target netip.Addr
	repo       domain.SnapshotRepository
	logger     *slog.Logger
	now        func() time.Time

	// mu guards the last addresses seen, used to log address changes.
	mu       sync.Mutex
	lastIPv4 netip.Addr
	lastIPv6 netip.Addr
}

// New returns a service for cfg. It fails when the repository is missing or a
// target does not match its IP family.
func New(cfg Config) (*Service, error) {
	if cfg.Repository == nil {
		return nil, errors.New("repository is required")
	}

	ipv4Target := cfg.IPv4Target.Unmap()
	if ipv4Target.IsValid() && !ipv4Target.Is4() {
		return nil, fmt.Errorf("ipv4 target %s is not an IPv4 address", cfg.IPv4Target)
	}

	ipv6Target := cfg.IPv6Target
	if ipv6Target.IsValid() && (!ipv6Target.Is6() || ipv6Target.Is4In6()) {
		return nil, fmt.Errorf("ipv6 target %s is not an IPv6 address", cfg.IPv6Target)
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	return &Service{
		ipv4Lookup: cfg.IPv4Lookup,
		ipv6Lookup: cfg.IPv6Lookup,
		pinger:     cfg.Pinger,
		ipv4Target: ipv4Target,
		ipv6Target: ipv6Target,
		repo:       cfg.Repository,
		logger:     logger.With(slog.String("component", "publicnet")),
		now:        now,
		mu:         sync.Mutex{},
		lastIPv4:   netip.Addr{},
		lastIPv6:   netip.Addr{},
	}, nil
}

// TakeSnapshot runs all probes concurrently, saves the resulting snapshot and
// returns it.
//
// A failing or unavailable probe does not fail the snapshot; its outcome is
// recorded in the probe. TakeSnapshot fails only when ctx ends before the
// probes finish or when the snapshot cannot be built or saved.
func (s *Service) TakeSnapshot(ctx context.Context) (*domain.Snapshot, error) {
	start := s.now()

	s.logger.DebugContext(ctx, "taking snapshot")

	var (
		wg       sync.WaitGroup
		ipv4     domain.IPAddrProbe
		ipv6     domain.IPAddrProbe
		connIPv4 domain.ConnProbe
		connIPv6 domain.ConnProbe
	)

	wg.Go(func() { ipv4 = s.probeIP(ctx, probeIPv4, s.ipv4Lookup) })
	wg.Go(func() { ipv6 = s.probeIP(ctx, probeIPv6, s.ipv6Lookup) })
	wg.Go(func() { connIPv4 = s.probeConn(ctx, probeConnIPv4, s.ipv4Target) })
	wg.Go(func() { connIPv6 = s.probeConn(ctx, probeConnIPv6, s.ipv6Target) })
	wg.Wait()

	// Probes cut short by ctx would be recorded as failures, which says more
	// about the shutdown than about the network.
	err := ctx.Err()
	if err != nil {
		return nil, fmt.Errorf("take snapshot: %w", err)
	}

	snap, err := domain.NewSnapshot(start, ipv4, ipv6, connIPv4, connIPv6)
	if err != nil {
		return nil, fmt.Errorf("build snapshot: %w", err)
	}

	err = s.repo.SavePublicNetSnapshot(ctx, snap)
	if err != nil {
		return nil, fmt.Errorf("save snapshot %s: %w", snap.ID, err)
	}

	s.logAddressChanges(ctx, snap)

	s.logger.InfoContext(ctx, "snapshot taken",
		slog.String("id", snap.ID.String()),
		slog.Duration("duration", s.now().Sub(start)),
		ipAttr(probeIPv4, snap.IPv4),
		ipAttr(probeIPv6, snap.IPv6),
		connAttr(probeConnIPv4, snap.ConnIPv4),
		connAttr(probeConnIPv6, snap.ConnIPv6),
	)

	return snap, nil
}

func (s *Service) probeIP(ctx context.Context, name string, lookup IPLookup) domain.IPAddrProbe {
	logger := s.logger.With(slog.String("probe", name))

	if lookup == nil {
		probelog.Log(ctx, logger, probe.Disabled())

		return domain.IPAddrProbe{Result: probe.Disabled(), Source: "", Addr: netip.Addr{}}
	}

	endpoint := lookup.Endpoint()
	logger = logger.With(slog.String("endpoint", endpoint))
	logger.DebugContext(ctx, "probe started")

	start := s.now()
	addr, err := lookup.Lookup(ctx)
	result := probe.NewResult(start, s.now().Sub(start), err, iplookup.ErrUnavailable)

	if result.Status != probe.StatusOK {
		addr = netip.Addr{}
	}

	probelog.Log(ctx, logger, result, slog.String("addr", addrString(addr)))

	return domain.IPAddrProbe{
		Result: result,
		Source: endpoint,
		Addr:   addr,
	}
}

func (s *Service) probeConn(ctx context.Context, name string, target netip.Addr) domain.ConnProbe {
	logger := s.logger.With(slog.String("probe", name))

	if s.pinger == nil || !target.IsValid() {
		probelog.Log(ctx, logger, probe.Disabled())

		var noStats probe.PingStats

		return domain.ConnProbe{
			Result: probe.Disabled(),
			Method: domain.MethodUnknown,
			Target: target,
			Ping:   noStats,
		}
	}

	logger = logger.With(slog.String("target", target.String()))
	logger.DebugContext(ctx, "probe started")

	start := s.now()
	stats, err := s.pinger.Ping(ctx, target)
	result := probe.NewResult(start, s.now().Sub(start), err, ping.ErrUnavailable)

	if result.Status == probe.StatusOK && stats.PacketsReceived == 0 {
		result.Status = probe.StatusFailed
		result.Message = fmt.Sprintf("no reply from %s to %d packets", target, stats.PacketsSent)
	}

	probelog.Log(ctx, logger, result,
		slog.Int("sent", stats.PacketsSent),
		slog.Int("received", stats.PacketsReceived),
		slog.Duration("rtt_avg", stats.RoundTripAvg),
		slog.Duration("jitter", stats.Jitter),
	)

	probelog.PingNotPermitted(ctx, logger, err)

	return domain.ConnProbe{
		Result: result,
		Method: domain.MethodICMP,
		Target: target,
		Ping: probe.PingStats{
			PacketsSent:     stats.PacketsSent,
			PacketsReceived: stats.PacketsReceived,
			RoundTripMin:    stats.RoundTripMin,
			RoundTripMax:    stats.RoundTripMax,
			RoundTripAvg:    stats.RoundTripAvg,
			Jitter:          stats.Jitter,
		},
	}
}

// logAddressChanges logs when a public address shows up for the first time or
// differs from the last one seen. Probes that did not succeed are ignored, so
// a temporary outage is not reported as a change.
func (s *Service) logAddressChanges(ctx context.Context, snap *domain.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastIPv4 = s.logAddressChange(ctx, probeIPv4, s.lastIPv4, snap.IPv4)
	s.lastIPv6 = s.logAddressChange(ctx, probeIPv6, s.lastIPv6, snap.IPv6)
}

func (s *Service) logAddressChange(
	ctx context.Context,
	name string,
	last netip.Addr,
	p domain.IPAddrProbe,
) netip.Addr {
	switch {
	case p.Status != probe.StatusOK || p.Addr == last:
		return last
	case !last.IsValid():
		s.logger.InfoContext(ctx, "public address detected",
			slog.String("probe", name),
			slog.String("addr", p.Addr.String()),
		)
	default:
		s.logger.InfoContext(ctx, "public address changed",
			slog.String("probe", name),
			slog.String("old", last.String()),
			slog.String("new", p.Addr.String()),
		)
	}

	return p.Addr
}

func ipAttr(name string, p domain.IPAddrProbe) slog.Attr {
	return slog.Group(name,
		slog.String("status", p.Status.String()),
		slog.String("addr", addrString(p.Addr)),
	)
}

func connAttr(name string, p domain.ConnProbe) slog.Attr {
	return slog.Group(name,
		slog.String("status", p.Status.String()),
		slog.Int("lost", p.Ping.PacketsLost()),
		slog.Duration("rtt_avg", p.Ping.RoundTripAvg),
	)
}

func addrString(addr netip.Addr) string {
	if !addr.IsValid() {
		return ""
	}

	return addr.String()
}
