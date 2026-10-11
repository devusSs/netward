package publicnet_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devusSs/netward/internal/adapter/iplookup"
	"github.com/devusSs/netward/internal/adapter/ping"
	"github.com/devusSs/netward/internal/domain/probe"
	domain "github.com/devusSs/netward/internal/domain/publicnet"
	"github.com/devusSs/netward/internal/service/publicnet"
)

//nolint:gochecknoglobals // Read-only fixtures.
var (
	startTime  = time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	publicIPv4 = netip.MustParseAddr("203.0.113.7")
	publicIPv6 = netip.MustParseAddr("2001:db8::7")
	targetIPv4 = netip.MustParseAddr("1.1.1.1")
	targetIPv6 = netip.MustParseAddr("2606:4700:4700::1111")
)

// fakeLookup returns its results in order and repeats the last one.
type fakeLookup struct {
	endpoint string

	mu      sync.Mutex
	results []lookupResult
}

type lookupResult struct {
	addr netip.Addr
	err  error
}

func newLookup(endpoint string, results ...lookupResult) *fakeLookup {
	return &fakeLookup{endpoint: endpoint, mu: sync.Mutex{}, results: results}
}

func (f *fakeLookup) Lookup(_ context.Context) (netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	result := f.results[0]
	if len(f.results) > 1 {
		f.results = f.results[1:]
	}

	return result.addr, result.err
}

func (f *fakeLookup) Endpoint() string {
	return f.endpoint
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
		return ping.Stats{
			PacketsSent:     0,
			PacketsReceived: 0,
			RoundTripMin:    0,
			RoundTripMax:    0,
			RoundTripAvg:    0,
			Jitter:          0,
		}, fmt.Errorf("unexpected target %s", target)
	}

	return result.stats, result.err
}

type fakeRepo struct {
	err error

	mu    sync.Mutex
	saved []*domain.Snapshot
}

func (f *fakeRepo) SavePublicNetSnapshot(_ context.Context, snap *domain.Snapshot) error {
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
			if record[key] != want {
				matches = false
			}
		}

		if matches {
			found = append(found, record)
		}
	}

	return found
}

func okStats() ping.Stats {
	return ping.Stats{
		PacketsSent:     4,
		PacketsReceived: 4,
		RoundTripMin:    10 * time.Millisecond,
		RoundTripMax:    20 * time.Millisecond,
		RoundTripAvg:    15 * time.Millisecond,
		Jitter:          3 * time.Millisecond,
	}
}

type fixture struct {
	cfg  publicnet.Config
	repo *fakeRepo
	logs *logRecorder
}

func newFixture() *fixture {
	repo := &fakeRepo{err: nil, mu: sync.Mutex{}, saved: nil}
	logs := &logRecorder{buf: bytes.Buffer{}}
	clock := &fakeClock{mu: sync.Mutex{}, next: startTime}

	return &fixture{
		cfg: publicnet.Config{
			IPv4Lookup: newLookup(iplookup.DefaultIPv4Endpoint, lookupResult{addr: publicIPv4, err: nil}),
			IPv6Lookup: newLookup(iplookup.DefaultIPv6Endpoint, lookupResult{addr: publicIPv6, err: nil}),
			Pinger: fakePinger{
				targetIPv4: {stats: okStats(), err: nil},
				targetIPv6: {stats: okStats(), err: nil},
			},
			IPv4Target: targetIPv4,
			IPv6Target: targetIPv6,
			Repository: repo,
			Logger:     logs.logger(),
			Now:        clock.Now,
		},
		repo: repo,
		logs: logs,
	}
}

func (f *fixture) takeSnapshot(t *testing.T) *domain.Snapshot {
	t.Helper()

	svc, err := publicnet.New(f.cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	snap, err := svc.TakeSnapshot(t.Context())
	if err != nil {
		t.Fatalf("TakeSnapshot() error = %v", err)
	}

	return snap
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		modify  func(cfg *publicnet.Config)
		wantMsg string
	}{
		{
			name:    "missing repository",
			modify:  func(cfg *publicnet.Config) { cfg.Repository = nil },
			wantMsg: "repository is required",
		},
		{
			name:    "ipv6 address as ipv4 target",
			modify:  func(cfg *publicnet.Config) { cfg.IPv4Target = targetIPv6 },
			wantMsg: "ipv4 target 2606:4700:4700::1111 is not an IPv4 address",
		},
		{
			name:    "ipv4 address as ipv6 target",
			modify:  func(cfg *publicnet.Config) { cfg.IPv6Target = targetIPv4 },
			wantMsg: "ipv6 target 1.1.1.1 is not an IPv6 address",
		},
		{
			name:    "ipv4-mapped address as ipv6 target",
			modify:  func(cfg *publicnet.Config) { cfg.IPv6Target = netip.MustParseAddr("::ffff:1.1.1.1") },
			wantMsg: "ipv6 target ::ffff:1.1.1.1 is not an IPv6 address",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := newFixture().cfg
			tt.modify(&cfg)

			svc, err := publicnet.New(cfg)
			if svc != nil {
				t.Errorf("New() service = %v, want nil", svc)
			}

			if err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("New() error = %v, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestNewAcceptsIPv4MappedIPv4Target(t *testing.T) {
	t.Parallel()

	f := newFixture()
	f.cfg.IPv4Target = netip.MustParseAddr("::ffff:1.1.1.1")

	snap := f.takeSnapshot(t)

	if snap.ConnIPv4.Target != targetIPv4 || snap.ConnIPv4.Status != probe.StatusOK {
		t.Errorf("ConnIPv4 = %+v, want ok probe of %s", snap.ConnIPv4, targetIPv4)
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

	ipTests := []struct {
		name   string
		probe  domain.IPAddrProbe
		addr   netip.Addr
		source string
	}{
		{"ipv4", snap.IPv4, publicIPv4, iplookup.DefaultIPv4Endpoint},
		{"ipv6", snap.IPv6, publicIPv6, iplookup.DefaultIPv6Endpoint},
	}

	for _, tt := range ipTests {
		if tt.probe.Status != probe.StatusOK || tt.probe.Message != "" {
			t.Errorf("%s: status = %s (%q), want ok", tt.name, tt.probe.Status, tt.probe.Message)
		}

		if tt.probe.Addr != tt.addr || tt.probe.Source != tt.source {
			t.Errorf(
				"%s: addr, source = %s, %s, want %s, %s",
				tt.name,
				tt.probe.Addr,
				tt.probe.Source,
				tt.addr,
				tt.source,
			)
		}

		if tt.probe.StartedAt.Before(startTime) || tt.probe.Duration <= 0 {
			t.Errorf("%s: started, duration = %v, %s, want after start and positive",
				tt.name, tt.probe.StartedAt, tt.probe.Duration)
		}
	}

	wantPing := probe.PingStats{
		PacketsSent:     4,
		PacketsReceived: 4,
		RoundTripMin:    10 * time.Millisecond,
		RoundTripMax:    20 * time.Millisecond,
		RoundTripAvg:    15 * time.Millisecond,
		Jitter:          3 * time.Millisecond,
	}

	for name, probe := range map[string]domain.ConnProbe{"conn_ipv4": snap.ConnIPv4, "conn_ipv6": snap.ConnIPv6} {
		if !probe.Connected() || probe.Method != domain.MethodICMP || probe.Ping != wantPing {
			t.Errorf("%s = %+v, want connected over icmp with %+v", name, probe, wantPing)
		}
	}

	if got := f.logs.find(t, "snapshot taken", map[string]any{"level": "INFO", "id": snap.ID.String()}); len(got) != 1 {
		t.Errorf("want one info log %q for the snapshot, got %v", "snapshot taken", f.logs.records(t))
	}

	if got := f.logs.find(t, "probe succeeded", map[string]any{"level": "DEBUG"}); len(got) != 4 {
		t.Errorf("want four debug logs %q, got %d", "probe succeeded", len(got))
	}
}

func TestTakeSnapshotMapsProbeOutcomes(t *testing.T) {
	t.Parallel()

	var noStats ping.Stats

	unavailable := fmt.Errorf("%w: ipv6: no route", iplookup.ErrUnavailable)
	lost := okStats()
	lost.PacketsReceived = 0

	tests := []struct {
		name      string
		modify    func(cfg *publicnet.Config)
		check     func(t *testing.T, snap *domain.Snapshot)
		wantLevel string
		wantLog   string
	}{
		{
			name: "lookup unavailable",
			modify: func(cfg *publicnet.Config) {
				cfg.IPv6Lookup = newLookup("https://v6.example", lookupResult{addr: netip.Addr{}, err: unavailable})
			},
			check: func(t *testing.T, snap *domain.Snapshot) {
				t.Helper()
				wantIPProbe(t, snap.IPv6, probe.StatusUnavailable, unavailable.Error())
			},
			wantLevel: "DEBUG",
			wantLog:   "probe unavailable",
		},
		{
			name: "lookup failed",
			modify: func(cfg *publicnet.Config) {
				cfg.IPv4Lookup = newLookup(
					"https://v4.example",
					lookupResult{addr: netip.Addr{}, err: errors.New("boom")},
				)
			},
			check: func(t *testing.T, snap *domain.Snapshot) {
				t.Helper()
				wantIPProbe(t, snap.IPv4, probe.StatusFailed, "boom")
			},
			wantLevel: "WARN",
			wantLog:   "probe failed",
		},
		{
			name: "lookup error drops returned address",
			modify: func(cfg *publicnet.Config) {
				cfg.IPv4Lookup = newLookup(
					"https://v4.example",
					lookupResult{addr: publicIPv4, err: errors.New("boom")},
				)
			},
			check: func(t *testing.T, snap *domain.Snapshot) {
				t.Helper()
				wantIPProbe(t, snap.IPv4, probe.StatusFailed, "boom")
			},
			wantLevel: "WARN",
			wantLog:   "probe failed",
		},
		{
			name: "ping unavailable",
			modify: func(cfg *publicnet.Config) {
				cfg.Pinger = fakePinger{
					targetIPv4: {stats: okStats(), err: nil},
					targetIPv6: {stats: noStats, err: fmt.Errorf("%w: no route", ping.ErrUnavailable)},
				}
			},
			check: func(t *testing.T, snap *domain.Snapshot) {
				t.Helper()
				wantConnProbe(t, snap.ConnIPv6, probe.StatusUnavailable, "target unreachable: no route")
			},
			wantLevel: "DEBUG",
			wantLog:   "probe unavailable",
		},
		{
			name: "ping without replies",
			modify: func(cfg *publicnet.Config) {
				cfg.Pinger = fakePinger{
					targetIPv4: {stats: lost, err: nil},
					targetIPv6: {stats: okStats(), err: nil},
				}
			},
			check: func(t *testing.T, snap *domain.Snapshot) {
				t.Helper()
				wantConnProbe(t, snap.ConnIPv4, probe.StatusFailed, "no reply from 1.1.1.1 to 4 packets")

				if snap.ConnIPv4.Ping.PacketsLost() != 4 {
					t.Errorf("PacketsLost() = %d, want 4", snap.ConnIPv4.Ping.PacketsLost())
				}
			},
			wantLevel: "WARN",
			wantLog:   "probe failed",
		},
		{
			name: "ping not permitted",
			modify: func(cfg *publicnet.Config) {
				cfg.Pinger = fakePinger{
					targetIPv4: {stats: noStats, err: fmt.Errorf("%w: socket", ping.ErrNotPermitted)},
					targetIPv6: {stats: okStats(), err: nil},
				}
			},
			check: func(t *testing.T, snap *domain.Snapshot) {
				t.Helper()
				wantConnProbe(t, snap.ConnIPv4, probe.StatusFailed, "icmp not permitted: socket")
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

func wantIPProbe(t *testing.T, probe domain.IPAddrProbe, status probe.Status, msg string) {
	t.Helper()

	if probe.Status != status || probe.Message != msg || probe.Addr.IsValid() {
		t.Errorf("probe = %+v, want status %s, message %q and no address", probe, status, msg)
	}
}

func wantConnProbe(t *testing.T, probe domain.ConnProbe, status probe.Status, msg string) {
	t.Helper()

	if probe.Status != status || probe.Message != msg || probe.Connected() {
		t.Errorf("probe = %+v, want status %s and message %q", probe, status, msg)
	}
}

func TestTakeSnapshotDisabledProbes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		modify       func(cfg *publicnet.Config)
		wantDisabled []string
	}{
		{
			name: "nothing configured",
			modify: func(cfg *publicnet.Config) {
				cfg.IPv4Lookup = nil
				cfg.IPv6Lookup = nil
				cfg.Pinger = nil
			},
			wantDisabled: []string{"ipv4", "ipv6", "conn_ipv4", "conn_ipv6"},
		},
		{
			name:         "no pinger",
			modify:       func(cfg *publicnet.Config) { cfg.Pinger = nil },
			wantDisabled: []string{"conn_ipv4", "conn_ipv6"},
		},
		{
			name:         "no ipv6 target",
			modify:       func(cfg *publicnet.Config) { cfg.IPv6Target = netip.Addr{} },
			wantDisabled: []string{"conn_ipv6"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture()
			tt.modify(&f.cfg)

			snap := f.takeSnapshot(t)
			statuses := map[string]probe.Status{
				"ipv4":      snap.IPv4.Status,
				"ipv6":      snap.IPv6.Status,
				"conn_ipv4": snap.ConnIPv4.Status,
				"conn_ipv6": snap.ConnIPv6.Status,
			}

			for name, status := range statuses {
				disabled := false

				for _, want := range tt.wantDisabled {
					disabled = disabled || name == want
				}

				if disabled != (status == probe.StatusDisabled) {
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

	svc, err := publicnet.New(f.cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	snap, err := svc.TakeSnapshot(ctx)
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

	svc, err := publicnet.New(f.cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	snap, err := svc.TakeSnapshot(t.Context())
	if snap != nil || !errors.Is(err, f.repo.err) || !strings.Contains(err.Error(), "save snapshot") {
		t.Errorf("TakeSnapshot() = %v, %v, want nil and a save error", snap, err)
	}

	if got := f.logs.find(t, "snapshot taken", nil); len(got) != 0 {
		t.Errorf("logged %q for an unsaved snapshot", "snapshot taken")
	}
}

func TestTakeSnapshotLogsAddressChanges(t *testing.T) {
	t.Parallel()

	newIPv4 := netip.MustParseAddr("198.51.100.9")

	f := newFixture()
	f.cfg.IPv4Lookup = newLookup(iplookup.DefaultIPv4Endpoint,
		lookupResult{addr: publicIPv4, err: nil},
		lookupResult{addr: publicIPv4, err: nil},
		lookupResult{addr: netip.Addr{}, err: errors.New("timeout")},
		lookupResult{addr: newIPv4, err: nil},
	)

	svc, err := publicnet.New(f.cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	for range 4 {
		if _, err = svc.TakeSnapshot(t.Context()); err != nil {
			t.Fatalf("TakeSnapshot() error = %v", err)
		}
	}

	detected := f.logs.find(t, "public address detected", map[string]any{"level": "INFO"})
	if len(detected) != 2 {
		t.Errorf("want two %q logs (ipv4 and ipv6), got %v", "public address detected", detected)
	}

	changed := f.logs.find(t, "public address changed", map[string]any{
		"level": "INFO",
		"probe": "ipv4",
		"old":   publicIPv4.String(),
		"new":   newIPv4.String(),
	})
	if len(changed) != 1 || len(f.logs.find(t, "public address changed", nil)) != 1 {
		t.Errorf("want exactly one ipv4 %q log, got %v", "public address changed", f.logs.records(t))
	}
}
