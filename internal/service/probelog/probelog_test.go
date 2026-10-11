package probelog_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/devusSs/netward/internal/adapter/ping"
	"github.com/devusSs/netward/internal/domain/probe"
	"github.com/devusSs/netward/internal/service/probelog"
)

func TestLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result probe.Result
		want   string
	}{
		{
			name:   "ok",
			result: probe.Result{Status: probe.StatusOK, Message: "", StartedAt: time.Time{}, Duration: time.Second},
			want:   `level=DEBUG msg="probe succeeded" probe=ipv4 status=ok duration=1s`,
		},
		{
			name: "unavailable",
			result: probe.Result{
				Status:    probe.StatusUnavailable,
				Message:   "no route",
				StartedAt: time.Time{},
				Duration:  time.Millisecond,
			},
			want: `level=DEBUG msg="probe unavailable" probe=ipv4 status=unavailable duration=1ms reason="no route"`,
		},
		{
			name: "failed",
			result: probe.Result{
				Status:    probe.StatusFailed,
				Message:   "boom",
				StartedAt: time.Time{},
				Duration:  time.Millisecond,
			},
			want: `level=WARN msg="probe failed" probe=ipv4 status=failed duration=1ms reason=boom`,
		},
		{
			name:   "disabled",
			result: probe.Disabled(),
			want:   `level=DEBUG msg="probe disabled" probe=ipv4 status=disabled duration=0s`,
		},
		{
			name:   "unknown",
			result: probe.Result{Status: probe.StatusUnknown, Message: "", StartedAt: time.Time{}, Duration: 0},
			want:   `level=DEBUG msg="probe finished" probe=ipv4 status=unknown duration=0s`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer

			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
				AddSource: false,
				Level:     slog.LevelDebug,
				ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
					if attr.Key == slog.TimeKey {
						return slog.Attr{}
					}

					return attr
				},
			}))

			probelog.Log(t.Context(), logger, tt.result, slog.String("probe", "ipv4"))

			if got := strings.TrimSpace(buf.String()); got != tt.want {
				t.Errorf("log = %s\nwant  %s", got, tt.want)
			}
		})
	}
}

func TestPingNotPermitted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"not permitted", fmt.Errorf("%w: socket", ping.ErrNotPermitted), true},
		{"other error", errors.New("boom"), false},
		{"no error", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer

			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
				AddSource:   false,
				Level:       slog.LevelDebug,
				ReplaceAttr: nil,
			}))

			probelog.PingNotPermitted(t.Context(), logger, tt.err)

			got := strings.Contains(buf.String(), `level=WARN msg="icmp is not permitted for this process;`)
			if got != tt.want || !tt.want && buf.Len() > 0 {
				t.Errorf("logs = %q, want warning %t", buf.String(), tt.want)
			}
		})
	}
}
