package privatenet_test

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/devusSs/netward/internal/domain/privatenet"
	"github.com/devusSs/netward/internal/domain/probe"
)

func testTime() time.Time {
	return time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
}

func okResult() probe.Result {
	return probe.NewResult(testTime(), 50*time.Millisecond, nil)
}

func addr(iface, prefix string) privatenet.InterfaceAddr {
	return privatenet.InterfaceAddr{Interface: iface, Prefix: netip.MustParsePrefix(prefix)}
}

func okStats() probe.PingStats {
	return probe.PingStats{
		PacketsSent:     4,
		PacketsReceived: 4,
		RoundTripMin:    time.Millisecond,
		RoundTripMax:    3 * time.Millisecond,
		RoundTripAvg:    2 * time.Millisecond,
		Jitter:          time.Millisecond,
	}
}

func gatewayProbe(gateway string) privatenet.GatewayProbe {
	return privatenet.GatewayProbe{
		Result: okResult(),
		Addr:   netip.MustParseAddr(gateway),
		Ping:   okStats(),
	}
}

type snapshotInput struct {
	timestamp   time.Time
	addrs       privatenet.AddrsProbe
	gatewayIPv4 privatenet.GatewayProbe
	gatewayIPv6 privatenet.GatewayProbe
}

func validInput() snapshotInput {
	return snapshotInput{
		timestamp: testTime(),
		addrs: privatenet.AddrsProbe{
			Result: okResult(),
			Addrs: []privatenet.InterfaceAddr{
				addr("en0", "192.168.178.23/24"),
				addr("en0", "fd00::23/64"),
			},
		},
		gatewayIPv4: gatewayProbe("192.168.178.1"),
		gatewayIPv6: gatewayProbe("fe80::1%en0"),
	}
}

func (in snapshotInput) newSnapshot() (*privatenet.Snapshot, error) {
	return privatenet.NewSnapshot(in.timestamp, in.addrs, in.gatewayIPv4, in.gatewayIPv6)
}

func TestInterfaceAddrString(t *testing.T) {
	t.Parallel()

	if got := addr("en0", "192.168.178.23/24").String(); got != "en0 192.168.178.23/24" {
		t.Errorf("String() = %q, want %q", got, "en0 192.168.178.23/24")
	}
}

func TestCompareInterfaceAddrs(t *testing.T) {
	t.Parallel()

	addrs := []privatenet.InterfaceAddr{
		addr("wlan0", "10.0.0.2/8"),
		addr("en0", "fd00::1/64"),
		addr("en0", "192.168.1.2/24"),
		addr("en0", "192.168.1.2/16"),
		addr("en0", "10.0.0.1/8"),
	}

	slices.SortFunc(addrs, privatenet.CompareInterfaceAddrs)

	want := []privatenet.InterfaceAddr{
		addr("en0", "10.0.0.1/8"),
		addr("en0", "192.168.1.2/16"),
		addr("en0", "192.168.1.2/24"),
		addr("en0", "fd00::1/64"),
		addr("wlan0", "10.0.0.2/8"),
	}

	if !slices.Equal(addrs, want) {
		t.Errorf("sorted = %v, want %v", addrs, want)
	}
}

func TestDiffAddrs(t *testing.T) {
	t.Parallel()

	a := addr("en0", "192.168.1.2/24")
	b := addr("en0", "fd00::2/64")
	c := addr("wlan0", "10.0.0.2/8")

	tests := []struct {
		name        string
		before      []privatenet.InterfaceAddr
		after       []privatenet.InterfaceAddr
		wantAdded   []privatenet.InterfaceAddr
		wantRemoved []privatenet.InterfaceAddr
	}{
		{"both empty", nil, nil, nil, nil},
		{"first addresses", nil, []privatenet.InterfaceAddr{a, b}, []privatenet.InterfaceAddr{a, b}, nil},
		{"all removed", []privatenet.InterfaceAddr{a, b}, nil, nil, []privatenet.InterfaceAddr{a, b}},
		{"unchanged", []privatenet.InterfaceAddr{a, b}, []privatenet.InterfaceAddr{a, b}, nil, nil},
		{
			"one replaced",
			[]privatenet.InterfaceAddr{a, b},
			[]privatenet.InterfaceAddr{a, c},
			[]privatenet.InterfaceAddr{c},
			[]privatenet.InterfaceAddr{b},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			added, removed := privatenet.DiffAddrs(tt.before, tt.after)
			if !slices.Equal(added, tt.wantAdded) || !slices.Equal(removed, tt.wantRemoved) {
				t.Errorf("DiffAddrs() = %v, %v, want %v, %v", added, removed, tt.wantAdded, tt.wantRemoved)
			}
		})
	}
}

func TestGatewayProbeReachable(t *testing.T) {
	t.Parallel()

	for _, status := range []probe.Status{probe.StatusOK, probe.StatusUnavailable, probe.StatusFailed} {
		p := gatewayProbe("192.168.178.1")
		p.Status = status

		if got, want := p.Reachable(), status == probe.StatusOK; got != want {
			t.Errorf("Reachable() with status %s = %t, want %t", status, got, want)
		}
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

	if snap.ID == uuid.Nil() || snap.ID[6]>>4 != 7 {
		t.Errorf("ID = %v, want a version 7 UUID", snap.ID)
	}

	if !snap.Timestamp.Equal(testTime()) || snap.Timestamp.Location() != time.UTC {
		t.Errorf("Timestamp = %v, want %v", snap.Timestamp, testTime())
	}

	if !slices.Equal(snap.Addrs.Addrs, in.addrs.Addrs) {
		t.Errorf("Addrs = %v, want %v", snap.Addrs.Addrs, in.addrs.Addrs)
	}

	if snap.GatewayIPv4 != in.gatewayIPv4 || snap.GatewayIPv6 != in.gatewayIPv6 {
		t.Errorf("gateways = %+v, %+v, want them unchanged", snap.GatewayIPv4, snap.GatewayIPv6)
	}
}

func TestNewSnapshotSortsAndDeduplicatesAddrs(t *testing.T) {
	t.Parallel()

	in := validInput()
	in.addrs.Addrs = []privatenet.InterfaceAddr{
		addr("wlan0", "10.0.0.2/8"),
		addr("en0", "192.168.1.2/24"),
		addr("wlan0", "10.0.0.2/8"),
	}
	original := slices.Clone(in.addrs.Addrs)

	snap, err := in.newSnapshot()
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}

	want := []privatenet.InterfaceAddr{addr("en0", "192.168.1.2/24"), addr("wlan0", "10.0.0.2/8")}
	if !slices.Equal(snap.Addrs.Addrs, want) {
		t.Errorf("Addrs = %v, want %v", snap.Addrs.Addrs, want)
	}

	if !slices.Equal(in.addrs.Addrs, original) {
		t.Errorf("input was modified: %v, want %v", in.addrs.Addrs, original)
	}
}

func TestNewSnapshotUnmapsGateway(t *testing.T) {
	t.Parallel()

	in := validInput()
	in.gatewayIPv4.Addr = netip.MustParseAddr("::ffff:192.168.178.1")

	snap, err := in.newSnapshot()
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}

	if want := netip.MustParseAddr("192.168.178.1"); snap.GatewayIPv4.Addr != want {
		t.Errorf("GatewayIPv4.Addr = %v, want %v", snap.GatewayIPv4.Addr, want)
	}
}

func TestNewSnapshotAcceptsNonOKProbes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		modify func(in *snapshotInput)
	}{
		{
			name: "no private addresses",
			modify: func(in *snapshotInput) {
				in.addrs.Addrs = nil
			},
		},
		{
			name: "reading addresses failed",
			modify: func(in *snapshotInput) {
				in.addrs.Result = probe.NewResult(testTime(), time.Millisecond, errors.New("boom"))
				in.addrs.Addrs = nil
			},
		},
		{
			name: "no ipv6 gateway",
			modify: func(in *snapshotInput) {
				in.gatewayIPv6.Status = probe.StatusUnavailable
				in.gatewayIPv6.Addr = netip.Addr{}
				in.gatewayIPv6.Ping.PacketsSent = 0
				in.gatewayIPv6.Ping.PacketsReceived = 0
			},
		},
		{
			name: "gateway found but not answering",
			modify: func(in *snapshotInput) {
				in.gatewayIPv4.Status = probe.StatusFailed
				in.gatewayIPv4.Ping.PacketsReceived = 0
			},
		},
		{
			name: "disabled probes skip validation",
			modify: func(in *snapshotInput) {
				in.addrs.Result = probe.Disabled()
				in.addrs.Addrs = nil
				in.gatewayIPv4.Result = probe.Disabled()
				in.gatewayIPv4.Addr = netip.MustParseAddr("fe80::1")
				in.gatewayIPv4.Ping.PacketsSent = -1
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
			name:    "unknown addrs status",
			modify:  func(in *snapshotInput) { in.addrs.Status = probe.StatusUnknown },
			wantMsg: "addrs: invalid status unknown",
		},
		{
			name:    "addresses on failed probe",
			modify:  func(in *snapshotInput) { in.addrs.Status = probe.StatusFailed },
			wantMsg: "addrs: 2 addresses set on probe with status failed",
		},
		{
			name:    "address without interface",
			modify:  func(in *snapshotInput) { in.addrs.Addrs[0].Interface = "" },
			wantMsg: "addrs: address 192.168.178.23/24 has no interface",
		},
		{
			name:    "invalid prefix",
			modify:  func(in *snapshotInput) { in.addrs.Addrs[0].Prefix = netip.Prefix{} },
			wantMsg: "addrs: en0: invalid prefix",
		},
		{
			name:    "public ipv4 address",
			modify:  func(in *snapshotInput) { in.addrs.Addrs[0] = addr("en0", "203.0.113.7/24") },
			wantMsg: "addrs: en0 203.0.113.7/24 is not private",
		},
		{
			name:    "global ipv6 address",
			modify:  func(in *snapshotInput) { in.addrs.Addrs[1] = addr("en0", "2001:db8::7/64") },
			wantMsg: "addrs: en0 2001:db8::7/64 is not private",
		},
		{
			name:    "link-local address",
			modify:  func(in *snapshotInput) { in.addrs.Addrs[1] = addr("en0", "fe80::7/64") },
			wantMsg: "addrs: en0 fe80::7/64 is not private",
		},
		{
			name:    "unknown gateway status",
			modify:  func(in *snapshotInput) { in.gatewayIPv4.Status = probe.StatusUnknown },
			wantMsg: "gateway ipv4: invalid status unknown",
		},
		{
			name:    "negative gateway duration",
			modify:  func(in *snapshotInput) { in.gatewayIPv6.Duration = -time.Second },
			wantMsg: "gateway ipv6: negative duration -1s",
		},
		{
			name:    "ok gateway without address",
			modify:  func(in *snapshotInput) { in.gatewayIPv4.Addr = netip.Addr{} },
			wantMsg: "gateway ipv4: missing address",
		},
		{
			name:    "ipv6 address as ipv4 gateway",
			modify:  func(in *snapshotInput) { in.gatewayIPv4.Addr = netip.MustParseAddr("fe80::1") },
			wantMsg: "gateway ipv4: address fe80::1 has wrong family",
		},
		{
			name: "ipv4 address as ipv6 gateway on failed probe",
			modify: func(in *snapshotInput) {
				in.gatewayIPv6.Status = probe.StatusFailed
				in.gatewayIPv6.Addr = netip.MustParseAddr("10.0.0.1")
			},
			wantMsg: "gateway ipv6: address 10.0.0.1 has wrong family",
		},
		{
			name:    "invalid packet counts",
			modify:  func(in *snapshotInput) { in.gatewayIPv4.Ping.PacketsReceived = 5 },
			wantMsg: "gateway ipv4: invalid packet counts (sent 4, received 5)",
		},
		{
			name:    "ok gateway without replies",
			modify:  func(in *snapshotInput) { in.gatewayIPv6.Ping.PacketsReceived = 0 },
			wantMsg: "gateway ipv6: status ok but no packets received",
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

			if !errors.Is(err, privatenet.ErrInvalidSnapshot) {
				t.Fatalf("NewSnapshot() error = %v, want %v", err, privatenet.ErrInvalidSnapshot)
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
	in.addrs.Addrs[0].Interface = ""
	in.addrs.Addrs[1] = addr("en0", "2001:db8::7/64")
	in.gatewayIPv4.Status = probe.StatusUnknown

	_, err := in.newSnapshot()
	if !errors.Is(err, privatenet.ErrInvalidSnapshot) {
		t.Fatalf("NewSnapshot() error = %v, want %v", err, privatenet.ErrInvalidSnapshot)
	}

	for _, want := range []string{"has no interface", "is not private", "gateway ipv4: invalid status unknown"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("NewSnapshot() error = %q, want it to contain %q", err, want)
		}
	}
}
