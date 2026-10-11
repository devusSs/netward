package probe_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devusSs/netward/internal/domain/probe"
)

func TestStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status    probe.Status
		wantStr   string
		wantValid bool
	}{
		{probe.StatusUnknown, "unknown", false},
		{probe.StatusOK, "ok", true},
		{probe.StatusUnavailable, "unavailable", true},
		{probe.StatusFailed, "failed", true},
		{probe.StatusDisabled, "disabled", true},
		{probe.Status(42), "Status(42)", false},
	}

	for _, tt := range tests {
		t.Run(tt.wantStr, func(t *testing.T) {
			t.Parallel()

			if got := tt.status.String(); got != tt.wantStr {
				t.Errorf("String() = %q, want %q", got, tt.wantStr)
			}

			if got := tt.status.IsValid(); got != tt.wantValid {
				t.Errorf("IsValid() = %t, want %t", got, tt.wantValid)
			}
		})
	}
}

func TestNewResult(t *testing.T) {
	t.Parallel()

	errUnavailable := errors.New("unavailable")
	errOtherUnavailable := errors.New("other unavailable")
	startedAt := time.Date(2026, time.October, 11, 14, 0, 0, 0, time.FixedZone("CEST", 2*60*60))

	tests := []struct {
		name        string
		err         error
		unavailable []error
		wantStatus  probe.Status
		wantMessage string
	}{
		{"success", nil, []error{errUnavailable}, probe.StatusOK, ""},
		{"unavailable", fmt.Errorf("lookup: %w", errUnavailable), []error{errUnavailable}, probe.StatusUnavailable,
			"lookup: unavailable"},
		{"second unavailable error", errOtherUnavailable, []error{errUnavailable, errOtherUnavailable},
			probe.StatusUnavailable, "other unavailable"},
		{"failed", errors.New("boom"), []error{errUnavailable}, probe.StatusFailed, "boom"},
		{"no unavailable errors", errUnavailable, nil, probe.StatusFailed, "unavailable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := probe.NewResult(startedAt, time.Second, tt.err, tt.unavailable...)
			want := probe.Result{
				Status:    tt.wantStatus,
				Message:   tt.wantMessage,
				StartedAt: startedAt.UTC(),
				Duration:  time.Second,
			}

			if got != want || got.StartedAt.Location() != time.UTC {
				t.Errorf("NewResult() = %+v, want %+v in UTC", got, want)
			}
		})
	}
}

func TestDisabled(t *testing.T) {
	t.Parallel()

	got := probe.Disabled()
	if got.Status != probe.StatusDisabled || got.Message != "" || !got.StartedAt.IsZero() || got.Duration != 0 {
		t.Errorf("Disabled() = %+v, want a disabled result without timing", got)
	}

	if err := got.Validate(); err != nil {
		t.Errorf("Disabled().Validate() error = %v", err)
	}
}

func TestResultValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		result  probe.Result
		wantErr string
	}{
		{"ok", probe.NewResult(time.Now(), time.Second, nil), ""},
		{"unknown status", probe.Result{Status: probe.StatusUnknown, Message: "", StartedAt: time.Time{}, Duration: 0},
			"invalid status unknown"},
		{
			"out of range status",
			probe.Result{Status: probe.Status(42), Message: "", StartedAt: time.Time{}, Duration: 0},
			"invalid status Status(42)",
		},
		{"negative duration", probe.NewResult(time.Now(), -time.Second, nil), "negative duration -1s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.result.Validate()

			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("Validate() error = %v, want nil", err)
			case tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr):
				t.Errorf("Validate() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func pingStats(sent, received int) probe.PingStats {
	return probe.PingStats{
		PacketsSent:     sent,
		PacketsReceived: received,
		RoundTripMin:    0,
		RoundTripMax:    0,
		RoundTripAvg:    0,
		Jitter:          0,
	}
}

func TestPingStatsPacketsLost(t *testing.T) {
	t.Parallel()

	if got := pingStats(10, 7).PacketsLost(); got != 3 {
		t.Errorf("PacketsLost() = %d, want 3", got)
	}
}

func TestPingStatsValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		stats    probe.PingStats
		wantFail bool
	}{
		{"none sent", pingStats(0, 0), false},
		{"all received", pingStats(4, 4), false},
		{"some lost", pingStats(4, 1), false},
		{"negative sent", pingStats(-1, 0), true},
		{"negative received", pingStats(4, -1), true},
		{"more received than sent", pingStats(4, 5), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.stats.Validate()
			if (err != nil) != tt.wantFail {
				t.Errorf("Validate() error = %v, want failure %t", err, tt.wantFail)
			}

			if err != nil {
				want := fmt.Sprintf("invalid packet counts (sent %d, received %d)",
					tt.stats.PacketsSent, tt.stats.PacketsReceived)
				if err.Error() != want {
					t.Errorf("Validate() error = %q, want %q", err, want)
				}
			}
		})
	}
}

func TestStatusText(t *testing.T) {
	t.Parallel()

	for status := probe.StatusUnknown; status <= probe.StatusDisabled; status++ {
		text, err := status.MarshalText()
		if err != nil {
			t.Fatalf("MarshalText(%s) error = %v", status, err)
		}

		parsed, err := probe.ParseStatus(string(text))
		if err != nil || parsed != status {
			t.Errorf("ParseStatus(%q) = %s, %v, want %s", text, parsed, err, status)
		}

		var unmarshaled probe.Status
		if err = unmarshaled.UnmarshalText(text); err != nil || unmarshaled != status {
			t.Errorf("UnmarshalText(%q) = %s, %v, want %s", text, unmarshaled, err, status)
		}
	}
}

func TestStatusTextErrors(t *testing.T) {
	t.Parallel()

	if _, err := probe.Status(42).MarshalText(); err == nil {
		t.Error("MarshalText(Status(42)) error = nil, want an error")
	}

	if _, err := probe.ParseStatus("OK"); err == nil || err.Error() != `unknown status "OK"` {
		t.Errorf(`ParseStatus("OK") error = %v, want unknown status`, err)
	}

	status := probe.StatusFailed
	if err := status.UnmarshalText([]byte("broken")); err == nil || status != probe.StatusFailed {
		t.Errorf("UnmarshalText(broken) = %s, %v, want an error and no change", status, err)
	}
}
