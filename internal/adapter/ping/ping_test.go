package ping_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/devusSs/netward/internal/adapter/ping"
)

func fastConfig() ping.Config {
	return ping.Config{
		Count:      3,
		Interval:   20 * time.Millisecond,
		Timeout:    2 * time.Second,
		Privileged: false,
	}
}

func newPinger(t *testing.T, cfg ping.Config) *ping.Pinger {
	t.Helper()

	pinger, err := ping.New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return pinger
}

// pingOrSkip pings target and skips the test when the environment does not
// allow it, such as a Linux host whose ping_group_range excludes the test
// process or a host without IPv6 loopback.
func pingOrSkip(t *testing.T, pinger *ping.Pinger, target netip.Addr) ping.Stats {
	t.Helper()

	stats, err := pinger.Ping(t.Context(), target)

	switch {
	case errors.Is(err, ping.ErrNotPermitted):
		t.Skipf("unprivileged ping not permitted: %v", err)
	case errors.Is(err, ping.ErrUnavailable):
		t.Skipf("no route to %s: %v", target, err)
	case err != nil:
		t.Fatalf("Ping(%s) error = %v", target, err)
	}

	return stats
}

// zeroStats is what Ping returns alongside an error.
var zeroStats ping.Stats //nolint:gochecknoglobals // Read-only fixture.

func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := ping.DefaultConfig()
	want := ping.Config{
		Count:      ping.DefaultCount,
		Interval:   ping.DefaultInterval,
		Timeout:    ping.DefaultTimeout,
		Privileged: false,
	}

	if cfg != want {
		t.Errorf("DefaultConfig() = %+v, want %+v", cfg, want)
	}

	if _, err := ping.New(cfg); err != nil {
		t.Errorf("New(DefaultConfig()) error = %v", err)
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	cfg := fastConfig()
	cfg.Privileged = true

	if got := newPinger(t, cfg).Config(); got != cfg {
		t.Errorf("Config() = %+v, want %+v", got, cfg)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		modify  func(cfg *ping.Config)
		wantMsg string
	}{
		{"zero count", func(cfg *ping.Config) { cfg.Count = 0 }, "count must be positive"},
		{"negative count", func(cfg *ping.Config) { cfg.Count = -1 }, "count must be positive"},
		{"zero interval", func(cfg *ping.Config) { cfg.Interval = 0 }, "interval must be positive"},
		{"negative interval", func(cfg *ping.Config) { cfg.Interval = -time.Second }, "interval must be positive"},
		{"zero timeout", func(cfg *ping.Config) { cfg.Timeout = 0 }, "is too short"},
		{
			name: "timeout shorter than sending",
			modify: func(cfg *ping.Config) {
				cfg.Count = 4
				cfg.Interval = time.Second
				cfg.Timeout = 3 * time.Second
			},
			wantMsg: "timeout 3s is too short to send 4 packets every 1s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := fastConfig()
			tt.modify(&cfg)

			pinger, err := ping.New(cfg)
			if pinger != nil {
				t.Errorf("New() pinger = %v, want nil", pinger)
			}

			if err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("New() error = %v, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestPingLoopback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target string
	}{
		{"ipv4", "127.0.0.1"},
		{"ipv6", "::1"},
		{"ipv4-mapped ipv6", "::ffff:127.0.0.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := fastConfig()
			stats := pingOrSkip(t, newPinger(t, cfg), netip.MustParseAddr(tt.target))

			if stats.PacketsSent != cfg.Count || stats.PacketsReceived != cfg.Count {
				t.Errorf("sent/received = %d/%d, want %d/%d",
					stats.PacketsSent, stats.PacketsReceived, cfg.Count, cfg.Count)
			}

			if stats.RoundTripMin <= 0 ||
				stats.RoundTripMin > stats.RoundTripAvg ||
				stats.RoundTripAvg > stats.RoundTripMax {
				t.Errorf("round trips min/avg/max = %s/%s/%s, want 0 < min <= avg <= max",
					stats.RoundTripMin, stats.RoundTripAvg, stats.RoundTripMax)
			}

			if stats.Jitter < 0 || stats.Jitter > stats.RoundTripMax-stats.RoundTripMin {
				t.Errorf("Jitter = %s, want within [0, %s]", stats.Jitter, stats.RoundTripMax-stats.RoundTripMin)
			}
		})
	}
}

func TestPingSinglePacketHasNoJitter(t *testing.T) {
	t.Parallel()

	cfg := fastConfig()
	cfg.Count = 1

	stats := pingOrSkip(t, newPinger(t, cfg), netip.MustParseAddr("127.0.0.1"))

	if stats.PacketsReceived != 1 || stats.Jitter != 0 {
		t.Errorf("received = %d, jitter = %s, want 1 and 0", stats.PacketsReceived, stats.Jitter)
	}
}

func TestPingInvalidTarget(t *testing.T) {
	t.Parallel()

	stats, err := newPinger(t, fastConfig()).Ping(t.Context(), netip.Addr{})
	if err == nil || !strings.Contains(err.Error(), "invalid target address") {
		t.Errorf("Ping() error = %v, want invalid target address", err)
	}

	if stats != zeroStats {
		t.Errorf("Ping() stats = %+v, want zero", stats)
	}
}

func TestPingContextCanceled(t *testing.T) {
	t.Parallel()

	cfg := fastConfig()
	cfg.Count = 100
	cfg.Timeout = 10 * time.Second

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	stats, err := newPinger(t, cfg).Ping(ctx, netip.MustParseAddr("127.0.0.1"))
	if errors.Is(err, ping.ErrNotPermitted) {
		t.Skipf("unprivileged ping not permitted: %v", err)
	}

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Ping() error = %v, want %v", err, context.Canceled)
	}

	if stats != zeroStats {
		t.Errorf("Ping() stats = %+v, want zero", stats)
	}
}

func TestErrorClassification(t *testing.T) {
	t.Parallel()

	opErr := func(op string, errno syscall.Errno) error {
		return fmt.Errorf("ping 192.0.2.1: %w", &net.OpError{
			Op:     op,
			Net:    "udp4",
			Source: nil,
			Addr:   nil,
			Err:    os.NewSyscallError(op, errno),
		})
	}

	tests := []struct {
		name             string
		err              error
		wantUnavailable  bool
		wantNotPermitted bool
	}{
		{"network unreachable", opErr("sendto", syscall.ENETUNREACH), true, false},
		{"host unreachable", opErr("sendto", syscall.EHOSTUNREACH), true, false},
		{"address not available", opErr("sendto", syscall.EADDRNOTAVAIL), true, false},
		{"operation not permitted", opErr("socket", syscall.EPERM), false, true},
		{"permission denied", opErr("socket", syscall.EACCES), false, true},
		{"protocol not supported", opErr("socket", syscall.EPROTONOSUPPORT), false, true},
		{"connection refused", opErr("sendto", syscall.ECONNREFUSED), false, false},
		{"canceled", context.Canceled, false, false},
		{"other", errors.New("boom"), false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := ping.IsUnavailable(tt.err); got != tt.wantUnavailable {
				t.Errorf("IsUnavailable() = %t, want %t", got, tt.wantUnavailable)
			}

			if got := ping.IsNotPermitted(tt.err); got != tt.wantNotPermitted {
				t.Errorf("IsNotPermitted() = %t, want %t", got, tt.wantNotPermitted)
			}
		})
	}
}

func TestJitter(t *testing.T) {
	t.Parallel()

	ms := time.Millisecond

	tests := []struct {
		name string
		rtts []time.Duration
		want time.Duration
	}{
		{"none", nil, 0},
		{"single", []time.Duration{10 * ms}, 0},
		{"constant", []time.Duration{10 * ms, 10 * ms, 10 * ms}, 0},
		{"rising", []time.Duration{10 * ms, 12 * ms, 16 * ms}, 3 * ms},
		{"alternating", []time.Duration{10 * ms, 20 * ms, 10 * ms, 20 * ms}, 10 * ms},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := ping.Jitter(tt.rtts); got != tt.want {
				t.Errorf("Jitter(%v) = %s, want %s", tt.rtts, got, tt.want)
			}
		})
	}
}
