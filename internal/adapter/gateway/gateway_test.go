package gateway_test

import (
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"

	jackpal "github.com/jackpal/gateway"

	"github.com/devusSs/netward/internal/adapter/gateway"
)

func returns(ip string, err error) func() (net.IP, error) {
	return func() (net.IP, error) { return net.ParseIP(ip), err }
}

func ipNet(t *testing.T, cidr string) *net.IPNet {
	t.Helper()

	ip, network, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}

	return &net.IPNet{IP: ip, Mask: network.Mask}
}

func interfaces(t *testing.T) func() ([]gateway.Interface, error) {
	t.Helper()

	ifaces := []gateway.Interface{
		gateway.NewInterface("lo0", ipNet(t, "127.0.0.1/8"), ipNet(t, "::1/128")),
		gateway.NewInterface("en0",
			&net.IPAddr{IP: net.ParseIP("192.168.178.23"), Zone: ""},
			ipNet(t, "192.168.178.23/24"),
			ipNet(t, "2001:db8::23/64"),
		),
	}

	return func() ([]gateway.Interface, error) { return ifaces, nil }
}

func noCall(t *testing.T) func() (net.IP, error) {
	t.Helper()

	return func() (net.IP, error) {
		t.Error("unexpected discovery call")

		return nil, errors.New("unexpected call")
	}
}

func TestIPv4(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")

	tests := []struct {
		name     string
		discover func() (net.IP, error)
		want     netip.Addr
		wantErr  error
		wantMsg  string
	}{
		{"found", returns("192.168.178.1", nil), netip.MustParseAddr("192.168.178.1"), nil, ""},
		{"no gateway", returns("", &jackpal.ErrNoGateway{}), netip.Addr{}, gateway.ErrNotFound,
			"no default gateway: ipv4"},
		{"discovery error", returns("", errBoom), netip.Addr{}, errBoom, "discover ipv4 gateway: boom"},
		{"invalid address", returns("", nil), netip.Addr{}, nil, "discover ipv4 gateway: invalid address"},
		{"ipv6 address", returns("fe80::1", nil), netip.Addr{}, nil, "ipv4 gateway fe80::1 is not an IPv4 address"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			finder := gateway.NewWith(tt.discover, noCall(t), noCall(t), interfaces(t))

			got, err := finder.IPv4()
			checkResult(t, got, err, tt.want, tt.wantErr, tt.wantMsg)
		})
	}
}

func TestIPv6(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")
	ok := func() (net.IP, error) { return nil, nil }

	tests := []struct {
		name          string
		discover      func() (net.IP, error)
		discoverIface func() (net.IP, error)
		interfaces    func() ([]gateway.Interface, error)
		want          netip.Addr
		wantErr       error
		wantMsg       string
	}{
		{
			name:          "link-local gateway gets zone",
			discover:      returns("fe80::1", nil),
			discoverIface: returns("2001:db8::23", nil),
			interfaces:    nil,
			want:          netip.MustParseAddr("fe80::1%en0"),
			wantErr:       nil,
			wantMsg:       "",
		},
		{
			name:          "global gateway needs no zone",
			discover:      returns("2001:db8::1", nil),
			discoverIface: nil,
			interfaces:    nil,
			want:          netip.MustParseAddr("2001:db8::1"),
			wantErr:       nil,
			wantMsg:       "",
		},
		{
			name:          "no gateway",
			discover:      returns("", &jackpal.ErrNoGateway{}),
			discoverIface: nil,
			interfaces:    nil,
			wantErr:       gateway.ErrNotFound,
			wantMsg:       "no default gateway: ipv6",
			want:          netip.Addr{},
		},
		{
			name:          "discovery error",
			discover:      returns("", errBoom),
			discoverIface: nil,
			interfaces:    nil,
			wantErr:       errBoom,
			wantMsg:       "discover ipv6 gateway: boom",
			want:          netip.Addr{},
		},
		{
			name:          "ipv4 address",
			discover:      returns("192.168.178.1", nil),
			discoverIface: nil,
			interfaces:    nil,
			wantMsg:       "ipv6 gateway 192.168.178.1 is not an IPv6 address",
			want:          netip.Addr{},
			wantErr:       nil,
		},
		{
			name:          "interface address discovery fails",
			discover:      returns("fe80::1", nil),
			discoverIface: returns("", errBoom),
			interfaces:    nil,
			wantErr:       errBoom,
			wantMsg:       "find interface of ipv6 gateway fe80::1: discover local address: boom",
			want:          netip.Addr{},
		},
		{
			name:          "invalid interface address",
			discover:      returns("fe80::1", nil),
			discoverIface: ok,
			interfaces:    nil,
			wantMsg:       "invalid local address",
			want:          netip.Addr{},
			wantErr:       nil,
		},
		{
			name:          "no interface with the local address",
			discover:      returns("fe80::1", nil),
			discoverIface: returns("2001:db8::99", nil),
			interfaces:    nil,
			wantMsg:       "no interface has local address 2001:db8::99",
			want:          netip.Addr{},
			wantErr:       nil,
		},
		{
			name:          "listing interfaces fails",
			discover:      returns("fe80::1", nil),
			discoverIface: returns("2001:db8::23", nil),
			interfaces:    func() ([]gateway.Interface, error) { return nil, errBoom },
			wantErr:       errBoom,
			wantMsg:       "find interface of ipv6 gateway fe80::1: boom",
			want:          netip.Addr{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			discoverIface := tt.discoverIface
			if discoverIface == nil {
				discoverIface = noCall(t)
			}

			ifaces := tt.interfaces
			if ifaces == nil {
				ifaces = interfaces(t)
			}

			finder := gateway.NewWith(noCall(t), tt.discover, discoverIface, ifaces)

			got, err := finder.IPv6()
			checkResult(t, got, err, tt.want, tt.wantErr, tt.wantMsg)
		})
	}
}

func checkResult(t *testing.T, got netip.Addr, err error, want netip.Addr, wantErr error, wantMsg string) {
	t.Helper()

	if got != want {
		t.Errorf("address = %v, want %v", got, want)
	}

	if wantMsg == "" {
		if err != nil {
			t.Errorf("error = %v, want nil", err)
		}

		return
	}

	if err == nil || !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("error = %v, want it to contain %q", err, wantMsg)
	}

	if wantErr != nil && !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want it to wrap %v", err, wantErr)
	}
}

func TestSystem(t *testing.T) {
	t.Parallel()

	finder := gateway.New()

	ipv4, err := finder.IPv4()

	switch {
	case errors.Is(err, gateway.ErrNotFound):
		t.Logf("no ipv4 gateway: %v", err)
	case err != nil:
		t.Errorf("IPv4() error = %v", err)
	case !ipv4.Is4():
		t.Errorf("IPv4() = %v, want an IPv4 address", ipv4)
	}

	ipv6, err := finder.IPv6()

	switch {
	case errors.Is(err, gateway.ErrNotFound):
		t.Logf("no ipv6 gateway: %v", err)
	case err != nil:
		t.Errorf("IPv6() error = %v", err)
	case !ipv6.Is6() || ipv6.IsLinkLocalUnicast() && ipv6.Zone() == "":
		t.Errorf("IPv6() = %v, want an IPv6 address with a zone if link-local", ipv6)
	}
}
