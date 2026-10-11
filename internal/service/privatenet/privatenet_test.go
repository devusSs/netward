package privatenet_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devusSs/netward/internal/adapter/gateway"
	"github.com/devusSs/netward/internal/adapter/netif"
	"github.com/devusSs/netward/internal/adapter/ping"
	domain "github.com/devusSs/netward/internal/domain/privatenet"
	"github.com/devusSs/netward/internal/domain/probe"
	"github.com/devusSs/netward/internal/service/privatenet"
)

//nolint:gochecknoglobals // Read-only fixtures.
var (
	startTime   = time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	gatewayIPv4 = netip.MustParseAddr("192.168.178.1")
	gatewayIPv6 = netip.MustParseAddr("fe80::1%en0")
)

// sequence returns its results in order and repeats the last one.
type sequence[T any] struct {
	mu      sync.Mutex
	results []T
}

func newSequence[T any](results ...T) *sequence[T] {
	return &sequence[T]{mu: sync.Mutex{}, results: results}
}

//nolint:ireturn // T is a plain result struct, not an interface.
func (s *sequence[T]) next() T {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := s.results[0]
	if len(s.results) > 1 {
		s.results = s.results[1:]
	}

	return result
}

type addrsResult struct {
	addrs []netif.Addr
	err   error
}

type fakeAddrs struct {
	results *sequence[addrsResult]
}

func (f fakeAddrs) PrivateAddrs() ([]netif.Addr, error) {
	result := f.results.next()

	return result.addrs, result.err
}

type gatewayResult struct {
	addr netip.Addr
	err  error
}

type fakeGateways struct {
	ipv4 *sequence[gatewayResult]
	ipv6 *sequence[gatewayResult]
}

func (f fakeGateways) IPv4() (netip.Addr, error) {
	result := f.ipv4.next()

	return result.addr, result.err
}

func (f fakeGateways) IPv6() (netip.Addr, error) {
	result := f.ipv6.next()

	return result.addr, result.err
}

type pingResult struct {
	stats ping.Stats
	err   error
}

// fakePinger answers per target. Its map is only read, so it is safe for
// concurrent use.
type fakePinger map[netip.Addr]pingResult

func (f fakePinger) Ping(_ context.Context, target netip.Addr) (ping.Stats, error) {
	result, ok := f[target]
	if !ok {
		var noStats ping.Stats

		return noStats, fmt.Errorf("unexpected target %s", target)
	}

	return result.stats, result.err
}

type fakeRepo struct {
	err error

	mu    sync.Mutex
	saved []*domain.Snapshot
}

func (f *fakeRepo) SavePrivateNetSnapshot(_ context.Context, snap *domain.Snapshot) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.err != nil {
		return f.err
	}

	f.saved = append(f.saved, snap)

	return nil
}

// fakeClock advances by a fixed step on every call.
type fakeClock struct {
	mu   sync.Mutex
	next time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.next
	c.next = c.next.Add(10 * time.Millisecond)

	return now
}

// logRecorder collects JSON log records.
type logRecorder struct {
	buf bytes.Buffer
}

func (r *logRecorder) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(&r.buf, &slog.HandlerOptions{
		AddSource:   false,
		Level:       slog.LevelDebug,
		ReplaceAttr: nil,
	}))
}

func (r *logRecorder) records(t *testing.T) []map[string]any {
	t.Helper()

	var records []map[string]any

	for line := range strings.Lines(r.buf.String()) {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}

		records = append(records, record)
	}

	return records
}

// find returns the records with msg whose attributes include all of attrs.
func (r *logRecorder) find(t *testing.T, msg string, attrs map[string]any) []map[string]any {
	t.Helper()

	var found []map[string]any

	for _, record := range r.records(t) {
		if record["msg"] != msg {
			continue
		}

		matches := true

		for key, want := range attrs {
			if fmt.Sprint(record[key]) != fmt.Sprint(want) {
				matches = false
			}
		}

		if matches {
			found = append(found, record)
		}
	}

	return found
}

func netifAddr(iface, prefix string) netif.Addr {
	return netif.Addr{Interface: iface, Prefix: netip.MustParsePrefix(prefix)}
}

func domainAddr(iface, prefix string) domain.InterfaceAddr {
	return domain.InterfaceAddr{Interface: iface, Prefix: netip.MustParsePrefix(prefix)}
}

func okStats() ping.Stats {
	return ping.Stats{
		PacketsSent:     4,
		PacketsReceived: 4,
		RoundTripMin:    time.Millisecond,
		RoundTripMax:    3 * time.Millisecond,
		RoundTripAvg:    2 * time.Millisecond,
		Jitter:          time.Millisecond,
	}
}

type fixture struct {
	cfg  privatenet.Config
	repo *fakeRepo
	logs *logRecorder
}

func newFixture() *fixture {
	repo := &fakeRepo{err: nil, mu: sync.Mutex{}, saved: nil}
	logs := &logRecorder{buf: bytes.Buffer{}}
	clock := &fakeClock{mu: sync.Mutex{}, next: startTime}

	return &fixture{
		cfg: privatenet.Config{
			Addrs: fakeAddrs{results: newSequence(addrsResult{
				addrs: []netif.Addr{netifAddr("en0", "fd00::23/64"), netifAddr("en0", "192.168.178.23/24")},
				err:   nil,
			})},
			Gateways: fakeGateways{
				ipv4: newSequence(gatewayResult{addr: gatewayIPv4, err: nil}),
				ipv6: newSequence(gatewayResult{addr: gatewayIPv6, err: nil}),
			},
			Pinger: fakePinger{
				gatewayIPv4: {stats: okStats(), err: nil},
				gatewayIPv6: {stats: okStats(), err: nil},
			},
			Repository: repo,
			Logger:     logs.logger(),
			Now:        clock.Now,
		},
		repo: repo,
		logs: logs,
	}
}

func (f *fixture) newService(t *testing.T) *privatenet.Service {
	t.Helper()

	svc, err := privatenet.New(f.cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return svc
}

func (f *fixture) takeSnapshot(t *testing.T) *domain.Snapshot {
	t.Helper()

	snap, err := f.newService(t).TakeSnapshot(t.Context())
	if err != nil {
		t.Fatalf("TakeSnapshot() error = %v", err)
	}

	return snap
}

func TestNewRequiresRepository(t *testing.T) {
	t.Parallel()

	cfg := newFixture().cfg
	cfg.Repository = nil

	svc, err := privatenet.New(cfg)
	if svc != nil || err == nil || !strings.Contains(err.Error(), "repository is required") {
		t.Errorf("New() = %v, %v, want nil and a missing repository error", svc, err)
	}
}

func TestNewDefaults(t *testing.T) {
	t.Parallel()

	f := newFixture()
	f.cfg.Logger = nil
	f.cfg.Now = nil

	before := time.Now()
	snap := f.takeSnapshot(t)

	if snap.Timestamp.Before(before) || snap.Timestamp.After(time.Now()) {
		t.Errorf("Timestamp = %v, want the current time", snap.Timestamp)
	}
}

func TestTakeSnapshot(t *testing.T) {
	t.Parallel()

	f := newFixture()
	snap := f.takeSnapshot(t)

	if len(f.repo.saved) != 1 || f.repo.saved[0] != snap {
		t.Fatalf("saved = %v, want exactly the returned snapshot", f.repo.saved)
	}

	if !snap.Timestamp.Equal(startTime) {
		t.Errorf("Timestamp = %v, want %v", snap.Timestamp, startTime)
	}

	wantAddrs := []domain.InterfaceAddr{domainAddr("en0", "192.168.178.23/24"), domainAddr("en0", "fd00::23/64")}
	if snap.Addrs.Status != probe.StatusOK || !slices.Equal(snap.Addrs.Addrs, wantAddrs) {
		t.Errorf("Addrs = %+v, want ok with %v", snap.Addrs, wantAddrs)
	}

	wantPing := probe.PingStats{
		PacketsSent:     4,
		PacketsReceived: 4,
		RoundTripMin:    time.Millisecond,
		RoundTripMax:    3 * time.Millisecond,
		RoundTripAvg:    2 * time.Millisecond,
		Jitter:          time.Millisecond,
	}

	for name, tt := range map[string]struct {
		probe domain.GatewayProbe
		addr  netip.Addr
	}{
		"gateway_ipv4": {snap.GatewayIPv4, gatewayIPv4},
		"gateway_ipv6": {snap.GatewayIPv6, gatewayIPv6},
	} {
		if !tt.probe.Reachable() || tt.probe.Addr != tt.addr || tt.probe.Ping != wantPing || tt.probe.Duration <= 0 {
			t.Errorf("%s = %+v, want reachable %s with %+v", name, tt.probe, tt.addr, wantPing)
		}
	}

	if got := f.logs.find(t, "snapshot taken", map[string]any{"level": "INFO", "id": snap.ID.String()}); len(got) != 1 {
		t.Errorf("want one info log %q, got %v", "snapshot taken", f.logs.records(t))
	}

	if got := f.logs.find(t, "probe succeeded", map[string]any{"level": "DEBUG"}); len(got) != 3 {
		t.Errorf("want three debug logs %q, got %d", "probe succeeded", len(got))
	}
}

func TestTakeSnapshotMapsProbeOutcomes(t *testing.T) {
	t.Parallel()

	var noStats ping.Stats

	lost := okStats()
	lost.PacketsReceived = 0

	tests := []struct {
		name      string
		modify    func(cfg *privatenet.Config)
		check     func(t *testing.T, snap *domain.Snapshot)
		wantLevel string
		wantLog   string
	}{
		{
			name: "reading addresses failed",
			modify: func(cfg *privatenet.Config) {
				cfg.Addrs = fakeAddrs{results: newSequence(addrsResult{addrs: nil, err: errors.New("boom")})}
			},
			check: func(t *testing.T, snap *domain.Snapshot) {
				t.Helper()

				if snap.Addrs.Status != probe.StatusFailed || snap.Addrs.Message != "boom" || snap.Addrs.Addrs != nil {
					t.Errorf("Addrs = %+v, want failed with message boom", snap.Addrs)
				}
			},
			wantLevel: "WARN",
			wantLog:   "probe failed",
		},
		{
			name: "no ipv6 gateway",
			modify: func(cfg *privatenet.Config) {
				cfg.Gateways = fakeGateways{
					ipv4: newSequence(gatewayResult{addr: gatewayIPv4, err: nil}),
					ipv6: newSequence(
						gatewayResult{addr: netip.Addr{}, err: fmt.Errorf("%w: ipv6", gateway.ErrNotFound)},
					),
				}
			},
			check: func(t *testing.T, snap *domain.Snapshot) {
				t.Helper()
				wantGateway(t, snap.GatewayIPv6, probe.StatusUnavailable, "no default gateway: ipv6", netip.Addr{})
			},
			wantLevel: "DEBUG",
			wantLog:   "probe unavailable",
		},
		{
			name: "gateway discovery failed",
			modify: func(cfg *privatenet.Config) {
				cfg.Gateways = fakeGateways{
					ipv4: newSequence(gatewayResult{addr: netip.Addr{}, err: errors.New("cannot parse routes")}),
					ipv6: newSequence(gatewayResult{addr: gatewayIPv6, err: nil}),
				}
			},
			check: func(t *testing.T, snap *domain.Snapshot) {
				t.Helper()
				wantGateway(t, snap.GatewayIPv4, probe.StatusFailed, "cannot parse routes", netip.Addr{})
			},
			wantLevel: "WARN",
			wantLog:   "probe failed",
		},
		{
			name: "gateway unreachable",
			modify: func(cfg *privatenet.Config) {
				cfg.Pinger = fakePinger{
					gatewayIPv4: {stats: okStats(), err: nil},
					gatewayIPv6: {stats: noStats, err: fmt.Errorf("%w: no route", ping.ErrUnavailable)},
				}
			},
			check: func(t *testing.T, snap *domain.Snapshot) {
				t.Helper()
				wantGateway(t, snap.GatewayIPv6, probe.StatusUnavailable, "target unreachable: no route", gatewayIPv6)
			},
			wantLevel: "DEBUG",
			wantLog:   "probe unavailable",
		},
		{
			name: "gateway not answering",
			modify: func(cfg *privatenet.Config) {
				cfg.Pinger = fakePinger{
					gatewayIPv4: {stats: lost, err: nil},
					gatewayIPv6: {stats: okStats(), err: nil},
				}
			},
			check: func(t *testing.T, snap *domain.Snapshot) {
				t.Helper()
				wantGateway(t, snap.GatewayIPv4, probe.StatusFailed,
					"no reply from gateway 192.168.178.1 to 4 packets", gatewayIPv4)
			},
			wantLevel: "WARN",
			wantLog:   "probe failed",
		},
		{
			name: "ping not permitted",
			modify: func(cfg *privatenet.Config) {
				cfg.Pinger = fakePinger{
					gatewayIPv4: {stats: noStats, err: fmt.Errorf("%w: socket", ping.ErrNotPermitted)},
					gatewayIPv6: {stats: okStats(), err: nil},
				}
			},
			check: func(t *testing.T, snap *domain.Snapshot) {
				t.Helper()
				wantGateway(t, snap.GatewayIPv4, probe.StatusFailed, "icmp not permitted: socket", gatewayIPv4)
			},
			wantLevel: "WARN",
			wantLog: "icmp is not permitted for this process; " +
				"allow unprivileged pings via net.ipv4.ping_group_range or run privileged with CAP_NET_RAW",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture()
			tt.modify(&f.cfg)

			tt.check(t, f.takeSnapshot(t))

			if got := f.logs.find(t, tt.wantLog, map[string]any{"level": tt.wantLevel}); len(got) != 1 {
				t.Errorf("want one %s log %q, got %v", tt.wantLevel, tt.wantLog, f.logs.records(t))
			}
		})
	}
}

func wantGateway(t *testing.T, p domain.GatewayProbe, status probe.Status, msg string, addr netip.Addr) {
	t.Helper()

	if p.Status != status || p.Message != msg || p.Addr != addr {
		t.Errorf("gateway probe = %+v, want status %s, message %q and address %v", p, status, msg, addr)
	}
}

func TestTakeSnapshotDisabledProbes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		modify       func(cfg *privatenet.Config)
		wantDisabled []string
	}{
		{
			name:         "no address reader",
			modify:       func(cfg *privatenet.Config) { cfg.Addrs = nil },
			wantDisabled: []string{"addrs"},
		},
		{
			name:         "no gateway finder",
			modify:       func(cfg *privatenet.Config) { cfg.Gateways = nil },
			wantDisabled: []string{"gateway_ipv4", "gateway_ipv6"},
		},
		{
			name:         "no pinger",
			modify:       func(cfg *privatenet.Config) { cfg.Pinger = nil },
			wantDisabled: []string{"gateway_ipv4", "gateway_ipv6"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture()
			tt.modify(&f.cfg)

			snap := f.takeSnapshot(t)
			statuses := map[string]probe.Status{
				"addrs":        snap.Addrs.Status,
				"gateway_ipv4": snap.GatewayIPv4.Status,
				"gateway_ipv6": snap.GatewayIPv6.Status,
			}

			for name, status := range statuses {
				if disabled := slices.Contains(tt.wantDisabled, name); disabled != (status == probe.StatusDisabled) {
					t.Errorf("%s status = %s, want disabled %t", name, status, disabled)
				}
			}

			if got := f.logs.find(t, "probe disabled", nil); len(got) != len(tt.wantDisabled) {
				t.Errorf("want %d %q logs, got %d", len(tt.wantDisabled), "probe disabled", len(got))
			}
		})
	}
}

func TestTakeSnapshotContextCanceled(t *testing.T) {
	t.Parallel()

	f := newFixture()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	snap, err := f.newService(t).TakeSnapshot(ctx)
	if snap != nil || !errors.Is(err, context.Canceled) {
		t.Errorf("TakeSnapshot() = %v, %v, want nil, %v", snap, err, context.Canceled)
	}

	if len(f.repo.saved) != 0 {
		t.Errorf("saved %d snapshots, want none", len(f.repo.saved))
	}
}

func TestTakeSnapshotSaveError(t *testing.T) {
	t.Parallel()

	f := newFixture()
	f.repo.err = errors.New("database down")

	snap, err := f.newService(t).TakeSnapshot(t.Context())
	if snap != nil || !errors.Is(err, f.repo.err) || !strings.Contains(err.Error(), "save snapshot") {
		t.Errorf("TakeSnapshot() = %v, %v, want nil and a save error", snap, err)
	}

	if got := f.logs.find(t, "snapshot taken", nil); len(got) != 0 {
		t.Errorf("logged %q for an unsaved snapshot", "snapshot taken")
	}
}

func TestTakeSnapshotBuildError(t *testing.T) {
	t.Parallel()

	f := newFixture()
	f.cfg.Addrs = fakeAddrs{results: newSequence(addrsResult{
		addrs: []netif.Addr{netifAddr("en0", "203.0.113.7/24")},
		err:   nil,
	})}

	snap, err := f.newService(t).TakeSnapshot(t.Context())
	if snap != nil || !errors.Is(err, domain.ErrInvalidSnapshot) {
		t.Errorf("TakeSnapshot() = %v, %v, want nil, %v", snap, err, domain.ErrInvalidSnapshot)
	}
}

func TestTakeSnapshotLogsChanges(t *testing.T) {
	t.Parallel()

	a := netifAddr("en0", "192.168.178.23/24")
	b := netifAddr("en0", "fd00::23/64")
	c := netifAddr("wlan0", "10.0.0.5/8")
	newGateway := netip.MustParseAddr("192.168.178.254")

	f := newFixture()
	f.cfg.Addrs = fakeAddrs{results: newSequence(
		addrsResult{addrs: []netif.Addr{a, b}, err: nil},
		addrsResult{addrs: []netif.Addr{b, a}, err: nil},
		addrsResult{addrs: nil, err: errors.New("boom")},
		addrsResult{addrs: []netif.Addr{a, c}, err: nil},
	)}
	f.cfg.Gateways = fakeGateways{
		ipv4: newSequence(
			gatewayResult{addr: gatewayIPv4, err: nil},
			gatewayResult{addr: gatewayIPv4, err: nil},
			gatewayResult{addr: netip.Addr{}, err: fmt.Errorf("%w: ipv4", gateway.ErrNotFound)},
			gatewayResult{addr: newGateway, err: nil},
		),
		ipv6: newSequence(gatewayResult{addr: netip.Addr{}, err: fmt.Errorf("%w: ipv6", gateway.ErrNotFound)}),
	}
	f.cfg.Pinger = fakePinger{
		gatewayIPv4: {stats: okStats(), err: nil},
		newGateway:  {stats: okStats(), err: nil},
	}

	svc := f.newService(t)

	for range 4 {
		if _, err := svc.TakeSnapshot(t.Context()); err != nil {
			t.Fatalf("TakeSnapshot() error = %v", err)
		}
	}

	tests := []struct {
		msg   string
		attrs map[string]any
	}{
		{"private addresses detected", map[string]any{"addrs": []any{a.Interface + " " + a.Prefix.String(),
			b.Interface + " " + b.Prefix.String()}}},
		{"private address added", map[string]any{"interface": "wlan0", "prefix": "10.0.0.5/8"}},
		{"private address removed", map[string]any{"interface": "en0", "prefix": "fd00::23/64"}},
		{"gateway detected", map[string]any{"probe": "gateway_ipv4", "gateway": gatewayIPv4.String()}},
		{"gateway changed", map[string]any{"probe": "gateway_ipv4", "old": gatewayIPv4.String(),
			"new": newGateway.String()}},
	}

	for _, tt := range tests {
		if got := f.logs.find(t, tt.msg, map[string]any{"level": "INFO"}); len(got) != 1 {
			t.Errorf("want exactly one %q log, got %v", tt.msg, got)
		}

		if got := f.logs.find(t, tt.msg, tt.attrs); len(got) != 1 {
			t.Errorf("want a %q log with %v, got %v", tt.msg, tt.attrs, f.logs.find(t, tt.msg, nil))
		}
	}
}
