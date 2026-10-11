package netif_test

import (
	"errors"
	"net"
	"net/netip"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/devusSs/netward/internal/adapter/netif"
)

const up = net.FlagUp | net.FlagRunning | net.FlagBroadcast | net.FlagMulticast

// ipNet returns the interface address for cidr, keeping the host part as
// systems report it. IPv4 addresses come out in their 16-byte form with a
// 4-byte mask, like on Linux and macOS.
func ipNet(t *testing.T, cidr string) *net.IPNet {
	t.Helper()

	ip, network, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}

	return &net.IPNet{IP: ip, Mask: network.Mask}
}

func fakeInterfaces(t *testing.T) []netif.Interface {
	t.Helper()

	return []netif.Interface{
		netif.NewInterface("lo0", net.FlagUp|net.FlagLoopback, ipNet(t, "127.0.0.1/8"), ipNet(t, "::1/128")),
		netif.NewInterface("en0", up,
			ipNet(t, "192.168.178.23/24"),
			ipNet(t, "fe80::1c2b:3a4d:5e6f:7081/64"),
			ipNet(t, "2001:db8::23/64"),
			ipNet(t, "fd00::23/64"),
			ipNet(t, "203.0.113.7/24"),
		),
		netif.NewInterface("en1", net.FlagBroadcast, ipNet(t, "10.0.0.5/8")),
		netif.NewInterface("docker0", up, ipNet(t, "172.17.0.1/16")),
		netif.NewInterface("utun3", up|net.FlagPointToPoint, ipNet(t, "10.8.0.2/24")),
	}
}

func newReader(t *testing.T, include, exclude []string, ifaces []netif.Interface) *netif.Reader {
	t.Helper()

	reader, err := netif.New(include, exclude)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	reader.SetInterfaces(func() ([]netif.Interface, error) { return ifaces, nil })

	return reader
}

func addr(iface, prefix string) netif.Addr {
	return netif.Addr{Interface: iface, Prefix: netip.MustParsePrefix(prefix)}
}

func TestPrivateAddrs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		include []string
		exclude []string
		want    []netif.Addr
	}{
		{
			name:    "all interfaces",
			include: nil,
			exclude: nil,
			want: []netif.Addr{
				addr("en0", "192.168.178.23/24"),
				addr("en0", "fd00::23/64"),
				addr("docker0", "172.17.0.1/16"),
				addr("utun3", "10.8.0.2/24"),
			},
		},
		{
			name:    "exclude patterns",
			include: nil,
			exclude: []string{"docker*", "utun?"},
			want: []netif.Addr{
				addr("en0", "192.168.178.23/24"),
				addr("en0", "fd00::23/64"),
			},
		},
		{
			name:    "include patterns",
			include: []string{"docker*", "en*"},
			exclude: nil,
			want: []netif.Addr{
				addr("en0", "192.168.178.23/24"),
				addr("en0", "fd00::23/64"),
				addr("docker0", "172.17.0.1/16"),
			},
		},
		{
			name:    "exclude wins over include",
			include: []string{"*"},
			exclude: []string{"en0"},
			want: []netif.Addr{
				addr("docker0", "172.17.0.1/16"),
				addr("utun3", "10.8.0.2/24"),
			},
		},
		{
			name:    "loopback and down interfaces stay excluded",
			include: []string{"lo0", "en1"},
			exclude: nil,
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := newReader(t, tt.include, tt.exclude, fakeInterfaces(t)).PrivateAddrs()
			if err != nil {
				t.Fatalf("PrivateAddrs() error = %v", err)
			}

			if !slices.Equal(got, tt.want) {
				t.Errorf("PrivateAddrs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPrivateAddrsAddressForms(t *testing.T) {
	t.Parallel()

	ifaces := []netif.Interface{
		netif.NewInterface("en0", up,
			// IPv4 with a 4-byte address and mask.
			&net.IPNet{IP: net.IPv4(192, 168, 1, 2).To4(), Mask: net.CIDRMask(24, 32)},
			// IPv4 in 16-byte form with a 16-byte mask.
			&net.IPNet{IP: net.IPv4(192, 168, 2, 2), Mask: net.CIDRMask(96+16, 128)},
			// An address without a mask counts as a single host.
			&net.IPAddr{IP: net.ParseIP("10.1.2.3"), Zone: ""},
			// Values that are not IP addresses are skipped.
			&net.UnixAddr{Name: "/tmp/socket", Net: "unix"},
			&net.IPNet{IP: net.IP{1, 2, 3}, Mask: net.CIDRMask(8, 32)},
		),
	}

	got, err := newReader(t, nil, nil, ifaces).PrivateAddrs()
	if err != nil {
		t.Fatalf("PrivateAddrs() error = %v", err)
	}

	want := []netif.Addr{
		addr("en0", "192.168.1.2/24"),
		addr("en0", "192.168.2.2/16"),
		addr("en0", "10.1.2.3/32"),
	}

	if !slices.Equal(got, want) {
		t.Errorf("PrivateAddrs() = %v, want %v", got, want)
	}
}

func TestPrivateAddrsError(t *testing.T) {
	t.Parallel()

	reader, err := netif.New(nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	errList := errors.New("list failed")
	reader.SetInterfaces(func() ([]netif.Interface, error) { return nil, errList })

	addrs, err := reader.PrivateAddrs()
	if addrs != nil || !errors.Is(err, errList) {
		t.Errorf("PrivateAddrs() = %v, %v, want nil, %v", addrs, err, errList)
	}
}

func TestNewRejectsBadPatterns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		include []string
		exclude []string
	}{
		{"bad include", []string{"en["}, nil},
		{"bad exclude", nil, []string{"docker*", "utun["}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reader, err := netif.New(tt.include, tt.exclude)
			if reader != nil || !errors.Is(err, path.ErrBadPattern) {
				t.Errorf("New() = %v, %v, want nil, %v", reader, err, path.ErrBadPattern)
			}

			if err != nil && !strings.Contains(err.Error(), `interface pattern "`) {
				t.Errorf("New() error = %q, want it to name the pattern", err)
			}
		})
	}
}

func TestNewCopiesPatterns(t *testing.T) {
	t.Parallel()

	exclude := []string{"docker*"}
	reader := newReader(t, nil, exclude, fakeInterfaces(t))
	exclude[0] = "en0"

	got, err := reader.PrivateAddrs()
	if err != nil {
		t.Fatalf("PrivateAddrs() error = %v", err)
	}

	if !slices.Contains(got, addr("en0", "192.168.178.23/24")) {
		t.Errorf("PrivateAddrs() = %v, changing the caller's slice changed the reader", got)
	}
}

func TestPrivateAddrsSystem(t *testing.T) {
	t.Parallel()

	reader, err := netif.New(nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	addrs, err := reader.PrivateAddrs()
	if err != nil {
		t.Fatalf("PrivateAddrs() error = %v", err)
	}

	for _, a := range addrs {
		if a.Interface == "" || !a.Prefix.IsValid() || !a.Prefix.Addr().IsPrivate() {
			t.Errorf("PrivateAddrs() returned %+v, want a named interface and a private prefix", a)
		}
	}
}
