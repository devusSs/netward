package domainip_test

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

	"github.com/devusSs/netward/internal/adapter/resolver"
	domain "github.com/devusSs/netward/internal/domain/domainip"
	"github.com/devusSs/netward/internal/domain/probe"
	"github.com/devusSs/netward/internal/service/domainip"
)

func startTime() time.Time {
	return time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
}

func addrs(s ...string) []netip.Addr {
	result := make([]netip.Addr, 0, len(s))
	for _, a := range s {
		result = append(result, netip.MustParseAddr(a))
	}

	return result
}

type lookupResult struct {
	addrs []netip.Addr
	err   error
}

// fakeResolver answers per domain with results in order, repeating the last.
type fakeResolver struct {
	mu      sync.Mutex
	results map[string][]lookupResult
}

func newResolver(results map[string][]lookupResult) *fakeResolver {
	return &fakeResolver{mu: sync.Mutex{}, results: results}
}

func (f *fakeResolver) Lookup(_ context.Context, d string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	results, ok := f.results[d]
	if !ok {
		return nil, fmt.Errorf("unexpected domain %s", d)
	}

	result := results[0]
	if len(results) > 1 {
		f.results[d] = results[1:]
	}

	return slices.Clone(result.addrs), result.err
}

func (f *fakeResolver) Name() string {
	return "192.168.178.2:53"
}

type fakeRepo struct {
	err error

	mu    sync.Mutex
	saved []*domain.Snapshot
}

func (f *fakeRepo) SaveDomainIPSnapshot(_ context.Context, snap *domain.Snapshot) error {
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

// find returns the records with msg whose attributes, formatted with
// [fmt.Sprint], equal those in attrs.
func (r *logRecorder) find(t *testing.T, msg string, attrs map[string]any) []map[string]any {
	t.Helper()

	var found []map[string]any

	for line := range strings.Lines(r.buf.String()) {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}

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

type fixture struct {
	cfg  domainip.Config
	repo *fakeRepo
	logs *logRecorder
}

func newFixture() *fixture {
	repo := &fakeRepo{err: nil, mu: sync.Mutex{}, saved: nil}
	logs := &logRecorder{buf: bytes.Buffer{}}
	clock := &fakeClock{mu: sync.Mutex{}, next: startTime()}

	return &fixture{
		cfg: domainip.Config{
			Resolver: newResolver(map[string][]lookupResult{
				"home.example.com": {{addrs: addrs("2001:db8::7", "203.0.113.7"), err: nil}},
				"nas.example.com":  {{addrs: addrs("203.0.113.7"), err: nil}},
			}),
			Domains:    []string{"nas.example.com", "Home.Example.com."},
			Repository: repo,
			Logger:     logs.logger(),
			Now:        clock.Now,
		},
		repo: repo,
		logs: logs,
	}
}

func (f *fixture) newService(t *testing.T) *domainip.Service {
	t.Helper()

	svc, err := domainip.New(f.cfg)
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

func TestNew(t *testing.T) {
	t.Parallel()

	f := newFixture()
	f.cfg.Domains = []string{"nas.example.com", "Home.Example.com.", "home.example.com"}

	if got, want := f.newService(t).
		Domains(),
		[]string{
			"home.example.com",
			"nas.example.com",
		}; !slices.Equal(
		got,
		want,
	) {
		t.Errorf("Domains() = %v, want %v", got, want)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		modify  func(cfg *domainip.Config)
		wantMsg string
	}{
		{"missing resolver", func(cfg *domainip.Config) { cfg.Resolver = nil }, "resolver is required"},
		{"missing repository", func(cfg *domainip.Config) { cfg.Repository = nil }, "repository is required"},
		{"no domains", func(cfg *domainip.Config) { cfg.Domains = nil }, "at least one domain is required"},
		{
			"invalid domain",
			func(cfg *domainip.Config) { cfg.Domains = []string{"example.com", "https://example.com"} },
			`invalid domain: domain "https://example.com"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := newFixture().cfg
			tt.modify(&cfg)

			svc, err := domainip.New(cfg)
			if svc != nil || err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("New() = %v, %v, want nil and an error containing %q", svc, err, tt.wantMsg)
			}
		})
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

	if !snap.Timestamp.Equal(startTime()) || snap.Resolver != "192.168.178.2:53" || len(snap.Lookups) != 2 {
		t.Fatalf("snapshot = %+v, want two lookups at %v via 192.168.178.2:53", snap, startTime())
	}

	want := map[string][]netip.Addr{
		"home.example.com": addrs("203.0.113.7", "2001:db8::7"),
		"nas.example.com":  addrs("203.0.113.7"),
	}

	for _, l := range snap.Lookups {
		if l.Status != probe.StatusOK || !slices.Equal(l.Addrs, want[l.Domain]) || l.Duration <= 0 {
			t.Errorf("lookup %s = %+v, want ok with %v", l.Domain, l, want[l.Domain])
		}
	}

	summary := f.logs.find(t, "snapshot taken", map[string]any{"level": "INFO", "id": snap.ID.String()})
	if len(summary) != 1 {
		t.Fatalf("want one info log %q, got %d", "snapshot taken", len(summary))
	}

	wantGroup := map[string]any{"status": "ok", "addrs": []any{"203.0.113.7"}}
	if fmt.Sprint(summary[0]["nas.example.com"]) != fmt.Sprint(wantGroup) {
		t.Errorf("summary nas.example.com = %v, want %v", summary[0]["nas.example.com"], wantGroup)
	}

	if got := f.logs.find(t, "probe succeeded", map[string]any{"level": "DEBUG"}); len(got) != 2 {
		t.Errorf("want two debug logs %q, got %d", "probe succeeded", len(got))
	}
}

func TestTakeSnapshotMapsLookupOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		result     lookupResult
		wantStatus probe.Status
		wantMsg    string
		wantLevel  string
		wantLog    string
	}{
		{
			name:       "not found",
			result:     lookupResult{addrs: nil, err: fmt.Errorf("%w: nas.example.com", resolver.ErrNotFound)},
			wantStatus: probe.StatusUnavailable,
			wantMsg:    "domain not found: nas.example.com",
			wantLevel:  "DEBUG",
			wantLog:    "probe unavailable",
		},
		{
			name:       "resolver error",
			result:     lookupResult{addrs: nil, err: errors.New("i/o timeout")},
			wantStatus: probe.StatusFailed,
			wantMsg:    "i/o timeout",
			wantLevel:  "WARN",
			wantLog:    "probe failed",
		},
		{
			name:       "addresses with an error are dropped",
			result:     lookupResult{addrs: addrs("203.0.113.7"), err: errors.New("partial failure")},
			wantStatus: probe.StatusFailed,
			wantMsg:    "partial failure",
			wantLevel:  "WARN",
			wantLog:    "probe failed",
		},
		{
			name:       "no addresses without an error",
			result:     lookupResult{addrs: nil, err: nil},
			wantStatus: probe.StatusFailed,
			wantMsg:    "resolver returned no addresses",
			wantLevel:  "WARN",
			wantLog:    "probe failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture()
			f.cfg.Domains = []string{"nas.example.com"}
			f.cfg.Resolver = newResolver(map[string][]lookupResult{"nas.example.com": {tt.result}})

			snap := f.takeSnapshot(t)
			l := snap.Lookups[0]

			if l.Status != tt.wantStatus || l.Message != tt.wantMsg || l.Addrs != nil {
				t.Errorf("lookup = %+v, want status %s, message %q and no addresses", l, tt.wantStatus, tt.wantMsg)
			}

			if got := f.logs.find(t, tt.wantLog, map[string]any{"level": tt.wantLevel}); len(got) != 1 {
				t.Errorf("want one %s log %q, got %d", tt.wantLevel, tt.wantLog, len(got))
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
	f.cfg.Domains = []string{"nas.example.com"}
	f.cfg.Resolver = newResolver(map[string][]lookupResult{
		"nas.example.com": {{addrs: []netip.Addr{netip.MustParseAddr("fe80::1%en0")}, err: nil}},
	})

	snap, err := f.newService(t).TakeSnapshot(t.Context())
	if snap != nil || !errors.Is(err, domain.ErrInvalidSnapshot) {
		t.Errorf("TakeSnapshot() = %v, %v, want nil, %v", snap, err, domain.ErrInvalidSnapshot)
	}
}

func TestTakeSnapshotLogsChanges(t *testing.T) {
	t.Parallel()

	f := newFixture()
	f.cfg.Domains = []string{"home.example.com"}
	f.cfg.Resolver = newResolver(map[string][]lookupResult{
		"home.example.com": {
			{addrs: addrs("203.0.113.7", "2001:db8::7"), err: nil},
			{addrs: addrs("2001:db8::7", "203.0.113.7"), err: nil},
			{addrs: nil, err: errors.New("i/o timeout")},
			{addrs: addrs("198.51.100.9", "2001:db8::7"), err: nil},
		},
	})

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
		{"domain resolved", map[string]any{"domain": "home.example.com", "addrs": []any{"203.0.113.7", "2001:db8::7"}}},
		{"domain address added", map[string]any{"domain": "home.example.com", "addr": "198.51.100.9"}},
		{"domain address removed", map[string]any{"domain": "home.example.com", "addr": "203.0.113.7"}},
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
