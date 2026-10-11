package app_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devusSs/netward/internal/adapter/sqlite"
	"github.com/devusSs/netward/internal/app"
	"github.com/devusSs/netward/internal/config"
	"github.com/devusSs/netward/internal/domain/probe"
	"github.com/devusSs/netward/internal/domain/publicnet"
)

// syncBuffer is a [bytes.Buffer] that is safe for concurrent writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func newLogger(w *syncBuffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		AddSource:   false,
		Level:       slog.LevelDebug,
		ReplaceAttr: nil,
	}))
}

// offlineConfig returns a configuration that needs no network: only the
// private addresses are read, and the web interface is off.
func offlineConfig(t *testing.T) config.Config {
	t.Helper()

	cfg := config.Default()
	cfg.Database = filepath.Join(t.TempDir(), "netward.db")
	cfg.PublicNet.Enabled = false
	cfg.PrivateNet.Gateways = false
	cfg.PrivateNet.Interval = time.Hour
	cfg.HTTP.Enabled = false

	return cfg
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunEvery(t *testing.T) {
	t.Parallel()

	var (
		runs atomic.Int32
		logs syncBuffer
	)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})

	go func() {
		defer close(done)

		app.RunEvery(ctx, newLogger(&logs), "test", 10*time.Millisecond, func(context.Context) error {
			if runs.Add(1) == 2 {
				return errors.New("boom")
			}

			return nil
		})
	}()

	eventually(t, "three runs", func() bool { return runs.Load() >= 3 })
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunEvery did not stop after the context ended")
	}

	if !strings.Contains(logs.String(), `level=ERROR msg="job failed" job=test error=boom`) {
		t.Errorf("logs = %s, want the failed run", logs.String())
	}
}

func TestRunEveryIgnoresErrorsAfterCancel(t *testing.T) {
	t.Parallel()

	var logs syncBuffer

	ctx, cancel := context.WithCancel(t.Context())

	app.RunEvery(ctx, newLogger(&logs), "test", time.Hour, func(context.Context) error {
		cancel()

		return context.Canceled
	})

	if strings.Contains(logs.String(), "job failed") {
		t.Errorf("logs = %s, want no error for a run cut short by shutdown", logs.String())
	}
}

func TestRunTakesSnapshotsAndCleansUp(t *testing.T) {
	t.Parallel()

	cfg := offlineConfig(t)
	cfg.Retention = 24 * time.Hour

	// An old public net snapshot that the cleanup must remove.
	seed, err := sqlite.Open(t.Context(), cfg.Database, nil)
	if err != nil {
		t.Fatal(err)
	}

	var noStats probe.PingStats

	old, err := publicnet.NewSnapshot(time.Now().Add(-48*time.Hour),
		publicnet.IPAddrProbe{Result: probe.Disabled(), Source: "", Addr: netip.Addr{}},
		publicnet.IPAddrProbe{Result: probe.Disabled(), Source: "", Addr: netip.Addr{}},
		publicnet.ConnProbe{Result: probe.Disabled(), Method: publicnet.MethodUnknown, Target: netip.Addr{},
			Ping: noStats},
		publicnet.ConnProbe{Result: probe.Disabled(), Method: publicnet.MethodUnknown, Target: netip.Addr{},
			Ping: noStats},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err = seed.SavePublicNetSnapshot(t.Context(), old); err != nil {
		t.Fatal(err)
	}

	var logs syncBuffer

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- app.Run(ctx, cfg, newLogger(&logs)) }()

	eventually(t, "a private net snapshot", func() bool {
		snaps, readErr := seed.PrivateNetSnapshots(t.Context(), time.Time{}, time.Now().Add(time.Hour), 1)

		return readErr == nil && len(snaps) == 1
	})

	eventually(t, "the old snapshot to be deleted", func() bool {
		snaps, readErr := seed.PublicNetSnapshots(t.Context(), time.Time{}, time.Now().Add(time.Hour), 1)

		return readErr == nil && len(snaps) == 0
	})

	cancel()

	if err = <-done; err != nil {
		t.Errorf("Run() error = %v, want nil after shutdown", err)
	}

	if err = seed.Close(); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		`msg="netward started" publicnet=false privatenet=true domainip=false web=false`,
		`msg="old snapshots deleted" count=1`,
		`msg="snapshot taken" component=privatenet`,
		`msg="netward stopped"`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs do not contain %q:\n%s", want, logs.String())
		}
	}
}

func TestRunStopsWhenTheWebInterfaceCannotStart(t *testing.T) {
	t.Parallel()

	var lc net.ListenConfig

	taken, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = taken.Close() })

	dir := t.TempDir()
	cfg := offlineConfig(t)
	cfg.HTTP.Enabled = true
	cfg.HTTP.Address = taken.Addr().String()
	cfg.HTTP.TLS.Cert = filepath.Join(dir, "cert.pem")
	cfg.HTTP.TLS.Key = filepath.Join(dir, "key.pem")

	var logs syncBuffer

	done := make(chan error, 1)

	go func() { done <- app.Run(t.Context(), cfg, newLogger(&logs)) }()

	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run() kept running although the web interface could not start")
	}

	if err == nil || !strings.Contains(err.Error(), "listen on "+cfg.HTTP.Address) {
		t.Errorf("Run() error = %v, want a listen error", err)
	}

	if _, statErr := os.Stat(cfg.HTTP.TLS.Cert); statErr != nil {
		t.Errorf("self-signed certificate was not created: %v", statErr)
	}
}

func TestRunSetupErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		modify  func(t *testing.T, cfg *config.Config)
		wantMsg string
	}{
		{
			name: "database directory missing",
			modify: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				cfg.Database = filepath.Join(t.TempDir(), "missing", "netward.db")
			},
			wantMsg: "database: open database",
		},
		{
			name: "bad lookup endpoint",
			modify: func(_ *testing.T, cfg *config.Config) {
				cfg.PublicNet.Enabled = true
				cfg.PublicNet.Lookup.IPv4Endpoint = "ftp://example.com"
			},
			wantMsg: "publicnet.lookup.ipv4_endpoint: endpoint",
		},
		{
			name: "bad ipv6 lookup endpoint",
			modify: func(_ *testing.T, cfg *config.Config) {
				cfg.PublicNet.Enabled = true
				cfg.PublicNet.Lookup.IPv6Endpoint = "example.com"
			},
			wantMsg: "publicnet.lookup.ipv6_endpoint: endpoint",
		},
		{
			name: "ping timeout too short",
			modify: func(_ *testing.T, cfg *config.Config) {
				cfg.PrivateNet.Gateways = true
				cfg.Ping.Timeout = time.Second
			},
			wantMsg: "ping: timeout 1s is too short",
		},
		{
			name: "bad dns server",
			modify: func(_ *testing.T, cfg *config.Config) {
				cfg.DomainIP.Enabled = true
				cfg.DomainIP.Resolver = "dns.google"
				cfg.DomainIP.Domains = []string{"example.com"}
			},
			wantMsg: `domainip.resolver: dns server "dns.google"`,
		},
		{
			name: "only one tls file",
			modify: func(t *testing.T, cfg *config.Config) {
				t.Helper()

				dir := t.TempDir()
				cfg.HTTP.Enabled = true
				cfg.HTTP.TLS.Cert = filepath.Join(dir, "cert.pem")
				cfg.HTTP.TLS.Key = filepath.Join(dir, "key.pem")

				if err := os.WriteFile(cfg.HTTP.TLS.Cert, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantMsg: "http.tls: found only one of",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := offlineConfig(t)
			tt.modify(t, &cfg)

			var logs syncBuffer

			err := app.Run(t.Context(), cfg, newLogger(&logs))
			if err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("Run() error = %v, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}
