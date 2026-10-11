// Package privatenet takes snapshots of the host's private network. It reads
// the private interface addresses, finds and pings the default gateways,
// turns the outcomes into a [domain.Snapshot] and saves it to a
// [domain.SnapshotRepository].
package privatenet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/devusSs/netward/internal/adapter/gateway"
	"github.com/devusSs/netward/internal/adapter/netif"
	"github.com/devusSs/netward/internal/adapter/ping"
	domain "github.com/devusSs/netward/internal/domain/privatenet"
	"github.com/devusSs/netward/internal/domain/probe"
	"github.com/devusSs/netward/internal/service/probelog"
)

const (
	probeAddrs       = "addrs"
	probeGatewayIPv4 = "gateway_ipv4"
	probeGatewayIPv6 = "gateway_ipv6"
)

// AddrReader reads the private interface addresses. [netif.Reader]
// implements it.
type AddrReader interface {
	PrivateAddrs() ([]netif.Addr, error)
}

// GatewayFinder finds the default gateways. [gateway.Finder] implements it.
type GatewayFinder interface {
	IPv4() (netip.Addr, error)
	IPv6() (netip.Addr, error)
}

// Pinger checks whether a gateway answers. [ping.Pinger] implements it.
type Pinger interface {
	Ping(ctx context.Context, target netip.Addr) (ping.Stats, error)
}

// Config configures a [Service]. A nil address reader disables the address
// probe, and a nil gateway finder or pinger disables both gateway probes;
// disabled probes are recorded as [probe.StatusDisabled].
type Config struct {
	// Addrs reads the private interface addresses.
	Addrs AddrReader
	// Gateways finds the default gateways.
	Gateways GatewayFinder
	// Pinger pings the gateways.
	Pinger Pinger
	// Repository stores the snapshots. It is required.
	Repository domain.SnapshotRepository
	// Logger receives the service's logs. Nil discards them.
	Logger *slog.Logger
	// Now returns the current time. Nil uses [time.Now].
	Now func() time.Time
}

// Service takes private net snapshots. It is safe for concurrent use.
type Service struct {
	addrs    AddrReader
	gateways GatewayFinder
	pinger   Pinger
	repo     domain.SnapshotRepository
	logger   *slog.Logger
	now      func() time.Time

	// mu guards the state used to log changes between snapshots.
	mu              sync.Mutex
	addrsSeen       bool
	lastAddrs       []domain.InterfaceAddr
	lastGatewayIPv4 netip.Addr
	lastGatewayIPv6 netip.Addr
}

// New returns a service for cfg. It fails when the repository is missing.
func New(cfg Config) (*Service, error) {
	if cfg.Repository == nil {
		return nil, errors.New("repository is required")
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
		addrs:           cfg.Addrs,
		gateways:        cfg.Gateways,
		pinger:          cfg.Pinger,
		repo:            cfg.Repository,
		logger:          logger.With(slog.String("component", "privatenet")),
		now:             now,
		mu:              sync.Mutex{},
		addrsSeen:       false,
		lastAddrs:       nil,
		lastGatewayIPv4: netip.Addr{},
		lastGatewayIPv6: netip.Addr{},
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
		wg          sync.WaitGroup
		addrs       domain.AddrsProbe
		gatewayIPv4 domain.GatewayProbe
		gatewayIPv6 domain.GatewayProbe
	)

	wg.Go(func() { addrs = s.probeAddrs(ctx) })
	wg.Go(func() { gatewayIPv4 = s.probeGateway(ctx, probeGatewayIPv4, GatewayFinder.IPv4) })
	wg.Go(func() { gatewayIPv6 = s.probeGateway(ctx, probeGatewayIPv6, GatewayFinder.IPv6) })
	wg.Wait()

	// Probes cut short by ctx would be recorded as failures, which says more
	// about the shutdown than about the network.
	err := ctx.Err()
	if err != nil {
		return nil, fmt.Errorf("take snapshot: %w", err)
	}

	snap, err := domain.NewSnapshot(start, addrs, gatewayIPv4, gatewayIPv6)
	if err != nil {
		return nil, fmt.Errorf("build snapshot: %w", err)
	}

	err = s.repo.SavePrivateNetSnapshot(ctx, snap)
	if err != nil {
		return nil, fmt.Errorf("save snapshot %s: %w", snap.ID, err)
	}

	s.logChanges(ctx, snap)

	s.logger.InfoContext(ctx, "snapshot taken",
		slog.String("id", snap.ID.String()),
		slog.Duration("duration", s.now().Sub(start)),
		slog.Group(probeAddrs,
			slog.String("status", snap.Addrs.Status.String()),
			slog.Int("count", len(snap.Addrs.Addrs)),
		),
		gatewayAttr(probeGatewayIPv4, snap.GatewayIPv4),
		gatewayAttr(probeGatewayIPv6, snap.GatewayIPv6),
	)

	return snap, nil
}

func (s *Service) probeAddrs(ctx context.Context) domain.AddrsProbe {
	logger := s.logger.With(slog.String("probe", probeAddrs))

	if s.addrs == nil {
		probelog.Log(ctx, logger, probe.Disabled())

		return domain.AddrsProbe{Result: probe.Disabled(), Addrs: nil}
	}

	logger.DebugContext(ctx, "probe started")

	start := s.now()
	found, err := s.addrs.PrivateAddrs()
	result := probe.NewResult(start, s.now().Sub(start), err)

	var addrs []domain.InterfaceAddr

	if err == nil {
		addrs = make([]domain.InterfaceAddr, 0, len(found))

		for _, a := range found {
			addrs = append(addrs, domain.InterfaceAddr{Interface: a.Interface, Prefix: a.Prefix})
		}
	}

	probelog.Log(ctx, logger, result, slog.Int("count", len(addrs)))

	return domain.AddrsProbe{Result: result, Addrs: addrs}
}

func (s *Service) probeGateway(
	ctx context.Context,
	name string,
	find func(GatewayFinder) (netip.Addr, error),
) domain.GatewayProbe {
	logger := s.logger.With(slog.String("probe", name))

	var noStats probe.PingStats

	if s.gateways == nil || s.pinger == nil {
		probelog.Log(ctx, logger, probe.Disabled())

		return domain.GatewayProbe{Result: probe.Disabled(), Addr: netip.Addr{}, Ping: noStats}
	}

	logger.DebugContext(ctx, "probe started")

	start := s.now()

	addr, err := find(s.gateways)
	if err != nil {
		result := probe.NewResult(start, s.now().Sub(start), err, gateway.ErrNotFound)
		probelog.Log(ctx, logger, result)

		return domain.GatewayProbe{Result: result, Addr: netip.Addr{}, Ping: noStats}
	}

	logger = logger.With(slog.String("gateway", addr.String()))

	stats, err := s.pinger.Ping(ctx, addr)
	result := probe.NewResult(start, s.now().Sub(start), err, ping.ErrUnavailable)

	if result.Status == probe.StatusOK && stats.PacketsReceived == 0 {
		result.Status = probe.StatusFailed
		result.Message = fmt.Sprintf("no reply from gateway %s to %d packets", addr, stats.PacketsSent)
	}

	probelog.Log(ctx, logger, result,
		slog.Int("sent", stats.PacketsSent),
		slog.Int("received", stats.PacketsReceived),
		slog.Duration("rtt_avg", stats.RoundTripAvg),
		slog.Duration("jitter", stats.Jitter),
	)
	probelog.PingNotPermitted(ctx, logger, err)

	return domain.GatewayProbe{
		Result: result,
		Addr:   addr,
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

// logChanges logs private addresses and gateways that appeared, disappeared
// or changed since the last snapshot. Probes that did not find anything are
// ignored, so a temporary failure is not reported as a change.
func (s *Service) logChanges(ctx context.Context, snap *domain.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if snap.Addrs.Status == probe.StatusOK {
		s.logAddrChanges(ctx, snap.Addrs.Addrs)
		s.addrsSeen = true
		s.lastAddrs = snap.Addrs.Addrs
	}

	s.lastGatewayIPv4 = s.logGatewayChange(ctx, probeGatewayIPv4, s.lastGatewayIPv4, snap.GatewayIPv4.Addr)
	s.lastGatewayIPv6 = s.logGatewayChange(ctx, probeGatewayIPv6, s.lastGatewayIPv6, snap.GatewayIPv6.Addr)
}

func (s *Service) logAddrChanges(ctx context.Context, addrs []domain.InterfaceAddr) {
	if !s.addrsSeen {
		names := make([]string, 0, len(addrs))
		for _, a := range addrs {
			names = append(names, a.String())
		}

		s.logger.InfoContext(ctx, "private addresses detected", slog.Any("addrs", names))

		return
	}

	added, removed := domain.DiffAddrs(s.lastAddrs, addrs)

	for _, a := range added {
		s.logger.InfoContext(ctx, "private address added", addrAttrs(a)...)
	}

	for _, a := range removed {
		s.logger.InfoContext(ctx, "private address removed", addrAttrs(a)...)
	}
}

func (s *Service) logGatewayChange(ctx context.Context, name string, last, current netip.Addr) netip.Addr {
	switch {
	case !current.IsValid() || current == last:
		return last
	case !last.IsValid():
		s.logger.InfoContext(ctx, "gateway detected",
			slog.String("probe", name),
			slog.String("gateway", current.String()),
		)
	default:
		s.logger.InfoContext(ctx, "gateway changed",
			slog.String("probe", name),
			slog.String("old", last.String()),
			slog.String("new", current.String()),
		)
	}

	return current
}

func addrAttrs(a domain.InterfaceAddr) []any {
	return []any{
		slog.String("interface", a.Interface),
		slog.String("prefix", a.Prefix.String()),
	}
}

func gatewayAttr(name string, p domain.GatewayProbe) slog.Attr {
	addr := ""
	if p.Addr.IsValid() {
		addr = p.Addr.String()
	}

	return slog.Group(name,
		slog.String("status", p.Status.String()),
		slog.String("addr", addr),
		slog.Int("lost", p.Ping.PacketsLost()),
		slog.Duration("rtt_avg", p.Ping.RoundTripAvg),
	)
}
