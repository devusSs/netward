package web_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/devusSs/netward/internal/adapter/tlscert"
	"github.com/devusSs/netward/internal/domain/domainip"
	"github.com/devusSs/netward/internal/domain/privatenet"
	"github.com/devusSs/netward/internal/domain/probe"
	"github.com/devusSs/netward/internal/domain/publicnet"
	"github.com/devusSs/netward/internal/service/history"
	"github.com/devusSs/netward/internal/web"
)

func now() time.Time {
	return time.Date(2026, time.October, 11, 12, 30, 0, 0, time.UTC)
}

func taken() time.Time {
	return now().Add(-42 * time.Second)
}

func okResult(d time.Duration) probe.Result {
	return probe.Result{Status: probe.StatusOK, Message: "", StartedAt: taken(), Duration: d}
}

func stats() probe.PingStats {
	return probe.PingStats{
		PacketsSent:     4,
		PacketsReceived: 4,
		RoundTripMin:    15 * time.Millisecond,
		RoundTripMax:    19 * time.Millisecond,
		RoundTripAvg:    16700 * time.Microsecond,
		Jitter:          1600 * time.Microsecond,
	}
}

func publicSnapshot() *publicnet.Snapshot {
	var noStats probe.PingStats

	return &publicnet.Snapshot{
		ID: uuid.MustParse("01a12860-fb79-7c45-9856-081ff6b1ff7e"),
		IPv4: publicnet.IPAddrProbe{
			Result: okResult(166 * time.Millisecond),
			Source: "https://api.ipify.org",
			Addr:   netip.MustParseAddr("203.0.113.7"),
		},
		IPv6: publicnet.IPAddrProbe{
			Status:    probe.StatusUnavailable,
			Message:   "ip family unavailable: ipv6: no route to host",
			StartedAt: taken(),
			Duration:  3 * time.Millisecond,
			Source:    "https://api6.ipify.org",
			Addr:      netip.Addr{},
		},
		ConnIPv4: publicnet.ConnProbe{
			Result: okResult(time.Second),
			Method: publicnet.MethodICMP,
			Target: netip.MustParseAddr("1.1.1.1"),
			Ping:   stats(),
		},
		ConnIPv6: publicnet.ConnProbe{
			Result: probe.Disabled(),
			Method: publicnet.MethodUnknown,
			Target: netip.Addr{},
			Ping:   noStats,
		},
		Timestamp: taken(),
	}
}

func privateSnapshot() *privatenet.Snapshot {
	return &privatenet.Snapshot{
		ID: uuid.MustParse("01a12879-cd04-7f99-8621-658f5ba4501e"),
		Addrs: privatenet.AddrsProbe{
			Result: okResult(time.Millisecond),
			Addrs: []privatenet.InterfaceAddr{
				{Interface: "en0", Prefix: netip.MustParsePrefix("192.168.178.26/24")},
			},
		},
		GatewayIPv4: privatenet.GatewayProbe{
			Result: okResult(500 * time.Millisecond),
			Addr:   netip.MustParseAddr("192.168.178.1"),
			Ping:   stats(),
		},
		GatewayIPv6: privatenet.GatewayProbe{
			Status:    probe.StatusFailed,
			Message:   "no reply from gateway fe80::1%en0 to 4 packets",
			StartedAt: taken(),
			Duration:  5 * time.Second,
			Addr:      netip.MustParseAddr("fe80::1%en0"),
			Ping: probe.PingStats{
				PacketsSent:     4,
				PacketsReceived: 0,
				RoundTripMin:    0,
				RoundTripMax:    0,
				RoundTripAvg:    0,
				Jitter:          0,
			},
		},
		Timestamp: taken(),
	}
}

type fakeSnapshots struct {
	public  []*publicnet.Snapshot
	private []*privatenet.Snapshot
	domains []*domainip.Snapshot
	err     error
}

func (f *fakeSnapshots) PublicNetSnapshots(context.Context, time.Time, time.Time, int) ([]*publicnet.Snapshot, error) {
	return f.public, f.err
}

func (f *fakeSnapshots) PrivateNetSnapshots(
	context.Context, time.Time, time.Time, int,
) ([]*privatenet.Snapshot, error) {
	return f.private, f.err
}

func (f *fakeSnapshots) DomainIPSnapshots(context.Context, time.Time, time.Time, int) ([]*domainip.Snapshot, error) {
	return f.domains, f.err
}

type fakeChanges struct {
	events []history.Event
	err    error
	since  time.Time
	limit  int
}

func (f *fakeChanges) Changes(_ context.Context, since time.Time, limit int) ([]history.Event, error) {
	f.since, f.limit = since, limit

	return f.events, f.err
}

type fixture struct {
	snapshots *fakeSnapshots
	changes   *fakeChanges
	features  web.Features
	logs      *bytes.Buffer
}

func newFixture() *fixture {
	return &fixture{
		snapshots: &fakeSnapshots{
			public:  []*publicnet.Snapshot{publicSnapshot()},
			private: []*privatenet.Snapshot{privateSnapshot()},
			domains: nil,
			err:     nil,
		},
		changes: &fakeChanges{
			events: []history.Event{{
				Time:    now().Add(-10 * time.Minute),
				Source:  history.SourcePublicNet,
				Subject: "ipv4",
				Kind:    history.KindChanged,
				Old:     "198.51.100.9",
				New:     "203.0.113.7",
				Message: "",
			}},
			err:   nil,
			since: time.Time{},
			limit: 0,
		},
		features: web.Features{PublicNet: true, PrivateNet: true, DomainIP: false},
		logs:     &bytes.Buffer{},
	}
}

func (f *fixture) server(t *testing.T) *web.Server {
	t.Helper()

	srv, err := web.New(web.Config{
		Address:   "127.0.0.1:0",
		TLS:       nil,
		Snapshots: f.snapshots,
		Changes:   f.changes,
		Features:  f.features,
		Logger: slog.New(slog.NewTextHandler(f.logs, &slog.HandlerOptions{
			AddSource:   false,
			Level:       slog.LevelDebug,
			ReplaceAttr: nil,
		})),
		Now:      now,
		Location: time.UTC,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return srv
}

func (f *fixture) get(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	f.server(t).Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil))

	return rec
}

func TestNewRequiresDependencies(t *testing.T) {
	t.Parallel()

	f := newFixture()

	for name, cfg := range map[string]web.Config{
		"snapshots": {Address: "", TLS: nil, Snapshots: nil, Changes: f.changes, Features: f.features,
			Logger: nil, Now: nil, Location: nil},
		"changes": {Address: "", TLS: nil, Snapshots: f.snapshots, Changes: nil, Features: f.features,
			Logger: nil, Now: nil, Location: nil},
	} {
		if srv, err := web.New(cfg); srv != nil || err == nil || !strings.Contains(err.Error(), name+" are required") {
			t.Errorf("New() without %s = %v, %v, want an error", name, srv, err)
		}
	}
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}

	return body
}

func TestAPIPublicNetLatest(t *testing.T) {
	t.Parallel()

	rec := newFixture().get(t, "/api/publicnet/latest")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	want := `{"id":"01a12860-fb79-7c45-9856-081ff6b1ff7e","timestamp":"2026-10-11T12:29:18Z",` +
		`"ipv4":{"status":"ok","started_at":"2026-10-11T12:29:18Z","duration_ms":166,` +
		`"source":"https://api.ipify.org","addr":"203.0.113.7"},` +
		`"ipv6":{"status":"unavailable","message":"ip family unavailable: ipv6: no route to host",` +
		`"started_at":"2026-10-11T12:29:18Z","duration_ms":3,"source":"https://api6.ipify.org"},` +
		`"conn_ipv4":{"status":"ok","started_at":"2026-10-11T12:29:18Z","duration_ms":1000,"method":"icmp",` +
		`"target":"1.1.1.1","ping":{"sent":4,"received":4,"rtt_min_ms":15,"rtt_max_ms":19,"rtt_avg_ms":16.7,` +
		`"jitter_ms":1.6}},` +
		`"conn_ipv6":{"status":"disabled","duration_ms":0,"ping":{"sent":0,"received":0,"rtt_min_ms":0,` +
		`"rtt_max_ms":0,"rtt_avg_ms":0,"jitter_ms":0}}}`

	if got := rec.Body.String(); got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}

	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestAPIPrivateNetLatest(t *testing.T) {
	t.Parallel()

	body := decode(t, newFixture().get(t, "/api/privatenet/latest"))

	addrs, _ := body["addrs"].(map[string]any)
	gateway, _ := body["gateway_ipv6"].(map[string]any)

	if got := addrs["addrs"]; !strings.Contains(stringOf(t, got), `"interface":"en0","prefix":"192.168.178.26/24"`) {
		t.Errorf("addrs = %v, want en0 192.168.178.26/24", got)
	}

	if gateway["status"] != "failed" || gateway["addr"] != "fe80::1%en0" {
		t.Errorf("gateway_ipv6 = %v, want failed fe80::1%%en0", gateway)
	}
}

func stringOf(t *testing.T, v any) string {
	t.Helper()

	data, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func TestAPIDomainIPLatest(t *testing.T) {
	t.Parallel()

	f := newFixture()
	f.features.DomainIP = true
	f.snapshots.domains = []*domainip.Snapshot{{
		ID:       uuid.MustParse("01a12882-3472-74e9-96bc-4355b4c10ff2"),
		Resolver: "192.168.178.1:53",
		Lookups: []domainip.LookupProbe{
			{Result: okResult(time.Millisecond), Domain: "home.example.com",
				Addrs: []netip.Addr{netip.MustParseAddr("203.0.113.7")}},
			{
				Status:    probe.StatusUnavailable,
				Message:   "domain not found: gone.example.com",
				StartedAt: taken(),
				Duration:  time.Millisecond,
				Domain:    "gone.example.com",
				Addrs:     nil,
			},
		},
		Timestamp: taken(),
	}}

	body := decode(t, f.get(t, "/api/domainip/latest"))

	want := `[{"addrs":["203.0.113.7"],"domain":"home.example.com","duration_ms":1,"started_at":"2026-10-11T12:29:18Z",` +
		`"status":"ok"},{"addrs":[],"domain":"gone.example.com","duration_ms":1,` +
		`"message":"domain not found: gone.example.com","started_at":"2026-10-11T12:29:18Z","status":"unavailable"}]`
	if body["resolver"] != "192.168.178.1:53" || stringOf(t, body["lookups"]) != want {
		t.Errorf("body = %v", body)
	}
}

func TestAPILatestWithoutData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path    string
		enabled bool
		want    string
	}{
		{"/api/domainip/latest", false, "domainip is disabled"},
		{"/api/domainip/latest", true, "no domainip snapshot yet"},
	}

	for _, tt := range tests {
		f := newFixture()
		f.features.DomainIP = tt.enabled

		rec := f.get(t, tt.path)
		if body := decode(t, rec); rec.Code != http.StatusNotFound || body["error"] != tt.want {
			t.Errorf("GET %s (enabled %t) = %d %v, want 404 %q", tt.path, tt.enabled, rec.Code, body, tt.want)
		}
	}
}

func TestAPIChanges(t *testing.T) {
	t.Parallel()

	f := newFixture()

	rec := f.get(t, "/api/changes?since=7d&limit=5")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	want := `{"since":"2026-10-04T12:30:00Z","events":[{"time":"2026-10-11T12:20:00Z","source":"publicnet",` +
		`"subject":"ipv4","kind":"changed","old":"198.51.100.9","new":"203.0.113.7"}]}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}

	if f.changes.limit != 5 || !f.changes.since.Equal(now().Add(-7*24*time.Hour)) {
		t.Errorf("Changes() called with %v, %d, want 7 days back and limit 5", f.changes.since, f.changes.limit)
	}
}

func TestAPIChangesDefaultsAndEmpty(t *testing.T) {
	t.Parallel()

	f := newFixture()
	f.changes.events = nil

	rec := f.get(t, "/api/changes")

	if rec.Body.String() != `{"since":"2026-10-10T12:30:00Z","events":[]}` || f.changes.limit != 100 {
		t.Errorf("body = %s, limit = %d, want 24 hours, no events and limit 100", rec.Body.String(), f.changes.limit)
	}
}

func TestAPIChangesRejectsBadParameters(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"/api/changes?since=forever",
		"/api/changes?since=-1h",
		"/api/changes?since=8d",
		"/api/changes?since=169h",
		"/api/changes?since=0d",
		"/api/changes?limit=0",
		"/api/changes?limit=1001",
		"/api/changes?limit=ten",
	} {
		rec := newFixture().get(t, target)
		if body := decode(t, rec); rec.Code != http.StatusBadRequest || body["error"] == "" {
			t.Errorf("GET %s = %d %v, want 400 with an error", target, rec.Code, body)
		}
	}
}

func TestAPIInternalErrors(t *testing.T) {
	t.Parallel()

	for _, target := range []string{"/api/publicnet/latest", "/api/privatenet/latest", "/api/changes"} {
		f := newFixture()
		f.snapshots.err = errors.New("database is locked")
		f.changes.err = errors.New("database is locked")

		rec := f.get(t, target)

		if body := decode(t, rec); rec.Code != http.StatusInternalServerError || body["error"] != "internal error" {
			t.Errorf("GET %s = %d %v, want 500 internal error", target, rec.Code, body)
		}

		if !strings.Contains(f.logs.String(), "database is locked") {
			t.Errorf("logs = %s, want the error logged", f.logs.String())
		}
	}
}

func TestAPIHealth(t *testing.T) {
	t.Parallel()

	rec := newFixture().get(t, "/api/health")
	if rec.Code != http.StatusOK || rec.Body.String() != `{"status":"ok"}` {
		t.Errorf("GET /api/health = %d %s", rec.Code, rec.Body.String())
	}
}

func domainSnapshot() *domainip.Snapshot {
	return &domainip.Snapshot{
		ID:       uuid.MustParse("01a12880-0b1c-7d2e-8f30-415263748596"),
		Resolver: "1.1.1.1:53",
		Lookups: []domainip.LookupProbe{{
			Result: okResult(12 * time.Millisecond),
			Domain: "home.example.com",
			Addrs:  []netip.Addr{netip.MustParseAddr("203.0.113.7"), netip.MustParseAddr("2001:db8::7")},
		}},
		Timestamp: taken(),
	}
}

func wantContains(t *testing.T, body string, wants ...string) {
	t.Helper()

	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
}

func TestOverviewPage(t *testing.T) {
	t.Parallel()

	f := newFixture()
	f.snapshots.domains = []*domainip.Snapshot{domainSnapshot()}
	f.features.DomainIP = true

	rec := f.get(t, "/")
	body := rec.Body.String()

	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("GET / = %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}

	wantContains(t, body,
		"<!doctype html>",
		"<title>Overview · netward</title>",
		`<a href="/" aria-current="page">`,
		`<a href="/history">`,
		`<time datetime="2026-10-11T12:30:00Z" data-relative data-updated>at 12:30:00</time>`,
		`hx-get="/fragments/overview" hx-trigger="every 5s"`,
		`data-server-time="2026-10-11T12:30:00Z"`,
		`<h2 id="card-public">Public network</h2>`,
		`<span class="label">Public IPv4</span>`,
		`<span class="status status-ok">ok</span>`,
		`<span class="addr">203.0.113.7</span>`,
		`<button type="button" class="copy" data-copy="203.0.113.7" aria-label="Copy 203.0.113.7" title="Copy" hidden>`,
		"<li>api.ipify.org</li><li>166.0 ms</li>",
		`<span class="status status-unavailable">unavailable</span>`,
		`<p class="message">ip family unavailable: ipv6: no route to host</p>`,
		"<li>4/4 replies</li><li>avg 16.7 ms</li><li>jitter 1.6 ms</li>",
		`<span class="pill status-unavailable">1 unavailable</span>`,
		`<span class="label">en0</span>`,
		`<span class="addr">192.168.178.26<span class="suffix">/24</span></span>`,
		`data-copy="192.168.178.26"`,
		`<span class="addr">fe80::1%en0</span>`,
		"<li>0/4 replies</li>",
		`<span class="pill status-failed">1 failed</span>`,
		`title="2026-10-11 12:29:18">Checked <time datetime="2026-10-11T12:29:18Z" data-relative>42 s ago</time>`,
		`<p class="subtitle">via 1.1.1.1:53</p>`,
		`<span class="label">home.example.com</span><button type="button" class="copy" data-copy="home.example.com"`,
		`data-copy="2001:db8::7"`,
		`<span class="pill status-ok">all ok</span>`,
	)

	if strings.Contains(body, "198.51.100.9") {
		t.Error("the overview shows changes, want them on the history page only")
	}
}

func TestPageLinksVersionedAssets(t *testing.T) {
	t.Parallel()

	f := newFixture()
	body := f.get(t, "/").Body.String()

	for _, tag := range []string{
		`<link rel="icon" href="/static/favicon.svg?v=`,
		`<link rel="stylesheet" href="/static/style.css?v=`,
		`<script src="/static/htmx.min.js?v=`,
		`<script src="/static/app.js?v=`,
	} {
		_, rest, found := strings.Cut(body, tag)
		version, _, _ := strings.Cut(rest, `"`)

		if !found || len(version) != 12 || strings.Trim(version, "0123456789abcdef") != "" {
			t.Errorf("page does not link %s<12 hex digits>, got version %q", tag, version)

			continue
		}

		target := strings.TrimPrefix(strings.TrimPrefix(tag, `<script src="`), `<link rel="stylesheet" href="`)
		target = strings.TrimPrefix(target, `<link rel="icon" href="`)

		if rec := f.get(t, target+version); rec.Code != http.StatusOK {
			t.Errorf("GET %s%s = %d, want 200", target, version, rec.Code)
		}
	}
}

func TestOverviewFragment(t *testing.T) {
	t.Parallel()

	body := newFixture().get(t, "/fragments/overview").Body.String()

	if strings.Contains(body, "<html") || !strings.HasPrefix(body, `<main id="overview"`) {
		t.Errorf("fragment is not just the overview: %.80s", body)
	}

	wantContains(t, body, `hx-trigger="every 5s"`, `<span class="addr">203.0.113.7</span>`)
}

func TestHistoryPage(t *testing.T) {
	t.Parallel()

	f := newFixture()
	f.changes.events = append(f.changes.events,
		history.Event{
			Time:    now().Add(-20 * time.Minute),
			Source:  history.SourcePrivateNet,
			Subject: "gateway_ipv6",
			Kind:    history.KindStatus,
			Old:     "ok",
			New:     "failed",
			Message: "no reply",
		},
		history.Event{
			Time:    now().Add(-25 * time.Hour),
			Source:  history.SourceDomainIP,
			Subject: "home.example.com",
			Kind:    history.KindAdded,
			Old:     "",
			New:     "2001:db8::7",
			Message: "",
		},
	)

	rec := f.get(t, "/history")
	body := rec.Body.String()

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /history = %d", rec.Code)
	}

	wantContains(t, body,
		"<title>History · netward</title>",
		`<a href="/history" aria-current="page">`,
		`<main id="history" class="history" hx-get="/fragments/history?range=24h" hx-trigger="every 15s"`,
		`<p class="count">3 changes</p>`,
		`hx-push-url="/history?range=24h" aria-current="page">24 hours</a>`,
		`<time datetime="2026-10-11T12:20:00Z">12:20:00</time>`,
		`<span class="source source-public">Public</span>`,
		`<span class="subject">Public IPv4</span>`,
		`<span class="addr">198.51.100.9</span><button type="button" class="copy" data-copy="198.51.100.9"`,
		`data-copy="203.0.113.7"`,
		`<span class="subject">Gateway (IPv6)</span>`,
		`<span class="status status-ok">ok</span>`,
		`<span class="status status-failed">failed</span>`,
		`<p class="message">no reply</p>`,
		`<time datetime="2026-10-10T11:30:00Z">Oct 10 11:30</time>`,
		`<span class="subject">home.example.com<button type="button" class="copy" data-copy="home.example.com"`,
		`<span class="kind">added</span><span class="val"><span class="addr">2001:db8::7</span>`,
	)

	if !f.changes.since.Equal(now().Add(-24*time.Hour)) || f.changes.limit != 100 {
		t.Errorf("Changes() since, limit = %v, %d, want 24 hours back, 100", f.changes.since, f.changes.limit)
	}
}

func TestHistoryFragment(t *testing.T) {
	t.Parallel()

	f := newFixture()

	body := f.get(t, "/fragments/history?range=7d").Body.String()

	if strings.Contains(body, "<html") || !strings.HasPrefix(body, `<main id="history"`) {
		t.Errorf("fragment is not just the history: %.80s", body)
	}

	wantContains(t, body, `hx-get="/fragments/history?range=7d"`, `aria-current="page">7 days</a>`,
		`<p class="count">1 change</p>`)

	if !f.changes.since.Equal(now().Add(-7 * 24 * time.Hour)) {
		t.Errorf("Changes() since = %v, want 7 days back", f.changes.since)
	}
}

func TestHistoryFallsBackToDefaultRange(t *testing.T) {
	t.Parallel()

	f := newFixture()

	body := f.get(t, "/history?range=nonsense").Body.String()

	if !strings.Contains(body, `aria-current="page">24 hours</a>`) || !f.changes.since.Equal(now().Add(-24*time.Hour)) {
		t.Error("an invalid range did not fall back to 24 hours")
	}
}

func TestHistoryTruncated(t *testing.T) {
	t.Parallel()

	f := newFixture()
	f.changes.events = slices.Repeat(f.changes.events, 100)

	wantContains(t, f.get(t, "/history").Body.String(), `<p class="count">100+ changes</p>`,
		"Showing the newest 100 changes.")
}

func TestPagesWithoutData(t *testing.T) {
	t.Parallel()

	f := newFixture()
	f.snapshots.public = nil
	f.snapshots.private = nil
	f.changes.events = nil
	f.features.PublicNet = false

	wantContains(t, f.get(t, "/").Body.String(),
		"Disabled in the configuration.",
		"No data yet. The first check runs shortly after netward starts.",
	)
	wantContains(t, f.get(t, "/history").Body.String(), "No changes in this period.", `<p class="count">0 changes</p>`)
}

func TestDisabledFeatureWithData(t *testing.T) {
	t.Parallel()

	f := newFixture()
	f.features.PublicNet = false

	wantContains(t, f.get(t, "/").Body.String(), "Disabled in the configuration. Showing the last check.")
}

func TestPageErrors(t *testing.T) {
	t.Parallel()

	for _, target := range []string{"/", "/fragments/overview", "/history", "/fragments/history"} {
		f := newFixture()
		f.snapshots.err = errors.New("boom")
		f.changes.err = errors.New("boom")

		rec := f.get(t, target)
		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "boom") {
			t.Errorf("GET %s = %d %q, want 500 without details", target, rec.Code, rec.Body.String())
		}

		if !strings.Contains(f.logs.String(), "boom") {
			t.Errorf("GET %s: logs = %s, want the error logged", target, f.logs.String())
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()

	for _, target := range []string{"/", "/api/health", "/static/style.css"} {
		rec := newFixture().get(t, target)

		for header, want := range map[string]string{
			"Content-Security-Policy": "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' " +
				"data:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'",
			"X-Content-Type-Options": "nosniff",
			"Referrer-Policy":        "no-referrer",
			"X-Frame-Options":        "DENY",
		} {
			if got := rec.Header().Get(header); got != want {
				t.Errorf("GET %s: %s = %q, want %q", target, header, got, want)
			}
		}
	}
}

func TestStaticFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path        string
		contentType string
		contains    string
	}{
		{"/static/htmx.min.js", "text/javascript; charset=utf-8", `version:"2.0.4"`},
		{"/static/style.css", "text/css; charset=utf-8", "--accent"},
		{"/static/app.js", "text/javascript; charset=utf-8", "time[data-relative]"},
		{"/static/favicon.svg", "image/svg+xml", "<svg"},
		{"/static/fonts/ibm-plex-sans.woff2", "font/woff2", "wOF2"},
		{"/static/fonts/jetbrains-mono.woff2", "font/woff2", "wOF2"},
	}

	for _, tt := range tests {
		rec := newFixture().get(t, tt.path)

		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != tt.contentType ||
			rec.Header().
				Get("Cache-Control") !=
				"public, max-age=3600" || !strings.Contains(rec.Body.String(), tt.contains) {
			t.Errorf("GET %s = %d %q %q", tt.path, rec.Code, rec.Header().Get("Content-Type"),
				rec.Header().Get("Cache-Control"))
		}
	}
}

func TestUnknownRoutes(t *testing.T) {
	t.Parallel()

	for _, target := range []string{"/nope", "/static/missing.js", "/api/unknown"} {
		if rec := newFixture().get(t, target); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, rec.Code)
		}
	}

	f := newFixture()
	rec := httptest.NewRecorder()
	f.server(t).
		Handler().
		ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/health", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/health = %d, want 405", rec.Code)
	}
}

func TestRequestLogging(t *testing.T) {
	t.Parallel()

	f := newFixture()
	f.get(t, "/api/health")

	if !strings.Contains(
		f.logs.String(),
		`level=DEBUG msg=request component=web method=GET path=/api/health status=200`,
	) {
		t.Errorf("logs = %s, want a request log", f.logs.String())
	}
}

// serve runs srv on a loopback port until the test ends and returns its
// address.
func serve(t *testing.T, srv *web.Server) (string, <-chan error) {
	t.Helper()

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- srv.Serve(ctx, listener) }()

	t.Cleanup(cancel)

	return listener.Addr().String(), done
}

func fetch(t *testing.T, client *http.Client, url string) string {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	return string(body)
}

func TestServeHTTPAndShutdown(t *testing.T) {
	t.Parallel()

	f := newFixture()

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- f.server(t).Serve(ctx, listener) }()

	if body := fetch(
		t,
		http.DefaultClient,
		"http://"+listener.Addr().String()+"/api/health",
	); body != `{"status":"ok"}` {
		t.Errorf("health = %s", body)
	}

	cancel()

	select {
	case err = <-done:
		if err != nil {
			t.Errorf("Serve() error = %v, want nil after shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve() did not return after the context ended")
	}

	if !strings.Contains(f.logs.String(), `msg="web interface listening" component=web url=http://127.0.0.1:`) {
		t.Errorf("logs = %s, want the listening address", f.logs.String())
	}
}

func TestServeHTTPS(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	certs, err := tlscert.New(tlscert.Config{
		CertFile: filepath.Join(dir, "cert.pem"),
		KeyFile:  filepath.Join(dir, "key.pem"),
		Hosts:    nil,
		Logger:   nil,
	})
	if err != nil {
		t.Fatal(err)
	}

	f := newFixture()

	srv, err := web.New(web.Config{
		Address:   "127.0.0.1:0",
		TLS:       &tls.Config{GetCertificate: certs.GetCertificate, MinVersion: tls.VersionTLS12},
		Snapshots: f.snapshots,
		Changes:   f.changes,
		Features:  f.features,
		Logger:    nil,
		Now:       now,
		Location:  time.UTC,
	})
	if err != nil {
		t.Fatal(err)
	}

	addr, _ := serve(t, srv)

	cert, err := certs.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}

	roots := x509.NewCertPool()
	roots.AddCert(cert.Leaf)

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	}}

	if body := fetch(t, client, "https://"+addr+"/api/health"); body != `{"status":"ok"}` {
		t.Errorf("health over https = %s", body)
	}
}

func TestRunReportsListenErrors(t *testing.T) {
	t.Parallel()

	f := newFixture()

	srv, err := web.New(web.Config{
		Address:   "256.0.0.1:0",
		TLS:       nil,
		Snapshots: f.snapshots,
		Changes:   f.changes,
		Features:  f.features,
		Logger:    nil,
		Now:       nil,
		Location:  nil,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err = srv.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "listen on 256.0.0.1:0") {
		t.Errorf("Run() error = %v, want a listen error", err)
	}
}
