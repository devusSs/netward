package publicnet_test

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/devusSs/netward/internal/domain/probe"
	"github.com/devusSs/netward/internal/domain/publicnet"
)

var testTime = time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC) //nolint:gochecknoglobals // Read-only fixture.

func okResult() probe.Result {
	return probe.Result{
		Status:    probe.StatusOK,
		Message:   "",
		StartedAt: testTime,
		Duration:  50 * time.Millisecond,
	}
}

func statusResult(status probe.Status, msg string) probe.Result {
	result := okResult()
	result.Status = status
	result.Message = msg

	return result
}

func ipProbe(addr string) publicnet.IPAddrProbe {
	return publicnet.IPAddrProbe{
		Result: okResult(),
		Source: "https://api.ipify.org",
		Addr:   netip.MustParseAddr(addr),
	}
}

func connProbe(target string) publicnet.ConnProbe {
	return publicnet.ConnProbe{
		Result: okResult(),
		Method: publicnet.MethodICMP,
		Target: netip.MustParseAddr(target),
		Ping: probe.PingStats{
			PacketsSent:     4,
			PacketsReceived: 4,
			RoundTripMin:    10 * time.Millisecond,
			RoundTripMax:    20 * time.Millisecond,
			RoundTripAvg:    15 * time.Millisecond,
			Jitter:          2 * time.Millisecond,
		},
	}
}

type snapshotInput struct {
	timestamp time.Time
	ipv4      publicnet.IPAddrProbe
	ipv6      publicnet.IPAddrProbe
	connIPv4  publicnet.ConnProbe
	connIPv6  publicnet.ConnProbe
}

func validInput() snapshotInput {
	return snapshotInput{
		timestamp: testTime,
		ipv4:      ipProbe("203.0.113.7"),
		ipv6:      ipProbe("2001:db8::7"),
		connIPv4:  connProbe("1.1.1.1"),
		connIPv6:  connProbe("2606:4700:4700::1111"),
	}
}

func (in snapshotInput) newSnapshot() (*publicnet.Snapshot, error) {
	return publicnet.NewSnapshot(in.timestamp, in.ipv4, in.ipv6, in.connIPv4, in.connIPv6)
}

func TestConnMethod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method    publicnet.ConnMethod
		wantStr   string
		wantValid bool
	}{
		{publicnet.MethodUnknown, "unknown", false},
		{publicnet.MethodICMP, "icmp", true},
		{publicnet.MethodTCP, "tcp", true},
		{publicnet.MethodHTTP, "http", true},
		{publicnet.ConnMethod(42), "ConnMethod(42)", false},
	}

	for _, tt := range tests {
		t.Run(tt.wantStr, func(t *testing.T) {
			t.Parallel()

			if got := tt.method.String(); got != tt.wantStr {
				t.Errorf("String() = %q, want %q", got, tt.wantStr)
			}

			if got := tt.method.IsValid(); got != tt.wantValid {
				t.Errorf("IsValid() = %t, want %t", got, tt.wantValid)
			}
		})
	}
}

func TestConnProbeConnected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status probe.Status
		want   bool
	}{
		{probe.StatusOK, true},
		{probe.StatusUnavailable, false},
		{probe.StatusFailed, false},
		{probe.StatusDisabled, false},
	}

	for _, tt := range tests {
		t.Run(tt.status.String(), func(t *testing.T) {
			t.Parallel()

			probe := connProbe("1.1.1.1")
			probe.Status = tt.status

			if got := probe.Connected(); got != tt.want {
				t.Errorf("Connected() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestNewSnapshot(t *testing.T) {
	t.Parallel()

	in := validInput()
	in.timestamp = time.Date(2026, time.October, 11, 14, 0, 0, 0, time.FixedZone("CEST", 2*60*60))

	snap, err := in.newSnapshot()
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}

	if snap.ID == uuid.Nil() {
		t.Error("ID is nil")
	}

	if version := snap.ID[6] >> 4; version != 7 {
		t.Errorf("ID version = %d, want 7", version)
	}

	if !snap.Timestamp.Equal(testTime) || snap.Timestamp.Location() != time.UTC {
		t.Errorf("Timestamp = %v, want %v", snap.Timestamp, testTime)
	}

	if snap.IPv4 != in.ipv4 || snap.IPv6 != in.ipv6 || snap.ConnIPv4 != in.connIPv4 || snap.ConnIPv6 != in.connIPv6 {
		t.Error("probes were changed")
	}
}

func TestNewSnapshotUniqueIDs(t *testing.T) {
	t.Parallel()

	first, err := validInput().newSnapshot()
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}

	second, err := validInput().newSnapshot()
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}

	if first.ID == second.ID {
		t.Errorf("IDs are equal: %v", first.ID)
	}
}

func TestNewSnapshotUnmapsAddresses(t *testing.T) {
	t.Parallel()

	in := validInput()
	in.ipv4.Addr = netip.MustParseAddr("::ffff:203.0.113.7")
	in.connIPv4.Target = netip.MustParseAddr("::ffff:1.1.1.1")

	snap, err := in.newSnapshot()
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}

	if want := netip.MustParseAddr("203.0.113.7"); snap.IPv4.Addr != want {
		t.Errorf("IPv4.Addr = %v, want %v", snap.IPv4.Addr, want)
	}

	if want := netip.MustParseAddr("1.1.1.1"); snap.ConnIPv4.Target != want {
		t.Errorf("ConnIPv4.Target = %v, want %v", snap.ConnIPv4.Target, want)
	}
}

func TestNewSnapshotAcceptsNonOKProbes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		modify func(in *snapshotInput)
	}{
		{
			name: "ipv6 unavailable",
			modify: func(in *snapshotInput) {
				in.ipv6.Result = statusResult(probe.StatusUnavailable, "no route to host")
				in.ipv6.Addr = netip.Addr{}
			},
		},
		{
			name: "ipv4 failed",
			modify: func(in *snapshotInput) {
				in.ipv4.Result = statusResult(probe.StatusFailed, "unexpected status 503")
				in.ipv4.Addr = netip.Addr{}
			},
		},
		{
			name: "conn failed with full packet loss",
			modify: func(in *snapshotInput) {
				in.connIPv4.Result = statusResult(probe.StatusFailed, "100% packet loss")
				in.connIPv4.Ping.PacketsReceived = 0
			},
		},
		{
			name: "conn disabled skips validation",
			modify: func(in *snapshotInput) {
				in.connIPv6.Result = statusResult(probe.StatusDisabled, "")
				in.connIPv6.Method = publicnet.MethodUnknown
				in.connIPv6.Target = netip.Addr{}
				in.connIPv6.Ping.PacketsSent = -1
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			in := validInput()
			tt.modify(&in)

			if _, err := in.newSnapshot(); err != nil {
				t.Errorf("NewSnapshot() error = %v", err)
			}
		})
	}
}

func TestNewSnapshotRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		modify  func(in *snapshotInput)
		wantMsg string
	}{
		{
			name:    "zero timestamp",
			modify:  func(in *snapshotInput) { in.timestamp = time.Time{} },
			wantMsg: "timestamp is zero",
		},
		{
			name:    "unknown status",
			modify:  func(in *snapshotInput) { in.ipv4.Status = probe.StatusUnknown },
			wantMsg: "ipv4: invalid status unknown",
		},
		{
			name:    "out of range status",
			modify:  func(in *snapshotInput) { in.connIPv6.Status = probe.Status(42) },
			wantMsg: "conn ipv6: invalid status Status(42)",
		},
		{
			name:    "ipv6 address in ipv4 probe",
			modify:  func(in *snapshotInput) { in.ipv4.Addr = netip.MustParseAddr("2001:db8::7") },
			wantMsg: "ipv4: address 2001:db8::7 has wrong family",
		},
		{
			name:    "ipv4-mapped address in ipv6 probe",
			modify:  func(in *snapshotInput) { in.ipv6.Addr = netip.MustParseAddr("::ffff:203.0.113.7") },
			wantMsg: "ipv6: address 203.0.113.7 has wrong family",
		},
		{
			name:    "ok probe without address",
			modify:  func(in *snapshotInput) { in.ipv4.Addr = netip.Addr{} },
			wantMsg: "ipv4: missing address",
		},
		{
			name:    "address on failed probe",
			modify:  func(in *snapshotInput) { in.ipv6.Status = probe.StatusFailed },
			wantMsg: "ipv6: address 2001:db8::7 set on probe with status failed",
		},
		{
			name:    "unknown method",
			modify:  func(in *snapshotInput) { in.connIPv4.Method = publicnet.MethodUnknown },
			wantMsg: "conn ipv4: invalid method unknown",
		},
		{
			name:    "ipv4 target in ipv6 conn probe",
			modify:  func(in *snapshotInput) { in.connIPv6.Target = netip.MustParseAddr("1.1.1.1") },
			wantMsg: "conn ipv6: target 1.1.1.1 has wrong family",
		},
		{
			name: "missing target on failed probe",
			modify: func(in *snapshotInput) {
				in.connIPv4.Status = probe.StatusFailed
				in.connIPv4.Target = netip.Addr{}
			},
			wantMsg: "conn ipv4: missing target",
		},
		{
			name:    "negative packets sent",
			modify:  func(in *snapshotInput) { in.connIPv4.Ping.PacketsSent = -1 },
			wantMsg: "conn ipv4: invalid packet counts (sent -1, received 4)",
		},
		{
			name:    "negative packets received",
			modify:  func(in *snapshotInput) { in.connIPv4.Ping.PacketsReceived = -1 },
			wantMsg: "conn ipv4: invalid packet counts (sent 4, received -1)",
		},
		{
			name:    "more packets received than sent",
			modify:  func(in *snapshotInput) { in.connIPv4.Ping.PacketsReceived = 5 },
			wantMsg: "conn ipv4: invalid packet counts (sent 4, received 5)",
		},
		{
			name:    "ok probe without received packets",
			modify:  func(in *snapshotInput) { in.connIPv4.Ping.PacketsReceived = 0 },
			wantMsg: "conn ipv4: status ok but no packets received",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			in := validInput()
			tt.modify(&in)

			snap, err := in.newSnapshot()
			if snap != nil {
				t.Errorf("NewSnapshot() snapshot = %+v, want nil", snap)
			}

			if !errors.Is(err, publicnet.ErrInvalidSnapshot) {
				t.Fatalf("NewSnapshot() error = %v, want %v", err, publicnet.ErrInvalidSnapshot)
			}

			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("NewSnapshot() error = %q, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestNewSnapshotJoinsErrors(t *testing.T) {
	t.Parallel()

	in := validInput()
	in.ipv4.Status = probe.StatusUnknown
	in.connIPv6.Method = publicnet.MethodUnknown

	_, err := in.newSnapshot()
	if !errors.Is(err, publicnet.ErrInvalidSnapshot) {
		t.Fatalf("NewSnapshot() error = %v, want %v", err, publicnet.ErrInvalidSnapshot)
	}

	for _, want := range []string{"ipv4: invalid status unknown", "conn ipv6: invalid method unknown"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("NewSnapshot() error = %q, want it to contain %q", err, want)
		}
	}
}

func TestConnMethodText(t *testing.T) {
	t.Parallel()

	for method := publicnet.MethodUnknown; method <= publicnet.MethodHTTP; method++ {
		text, err := method.MarshalText()
		if err != nil {
			t.Fatalf("MarshalText(%s) error = %v", method, err)
		}

		var parsed publicnet.ConnMethod
		if err = parsed.UnmarshalText(text); err != nil || parsed != method {
			t.Errorf("UnmarshalText(%q) = %s, %v, want %s", text, parsed, err, method)
		}
	}

	if _, err := publicnet.ConnMethod(42).MarshalText(); err == nil {
		t.Error("MarshalText(ConnMethod(42)) error = nil, want an error")
	}

	method := publicnet.MethodTCP
	if err := method.UnmarshalText([]byte("udp")); err == nil || method != publicnet.MethodTCP {
		t.Errorf("UnmarshalText(udp) = %s, %v, want an error and no change", method, err)
	}

	if _, err := publicnet.ParseConnMethod("ICMP"); err == nil {
		t.Error(`ParseConnMethod("ICMP") error = nil, want an error`)
	}
}
