// Package gateway finds the host's default gateways using
// [github.com/jackpal/gateway].
//
// On Linux it reads the routing tables in /proc/net. On macOS and the BSDs it
// reads the routing table directly and runs "netstat -rn" to find the
// interface of an IPv6 gateway. Windows uses "route print".
//
// IPv6 gateways are usually link-local addresses, which can only be reached
// through a specific interface. [Finder.IPv6] adds that interface as the
// address's zone, for example fe80::1%en0, so the address can be pinged.
package gateway

import (
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/jackpal/gateway"
)

// ErrNotFound is returned when the host has no default gateway of the
// requested IP family, for example no IPv6 gateway on an IPv4-only network.
var ErrNotFound = errors.New("no default gateway")

// iface is the part of an interface that [Finder] needs.
type iface struct {
	name  string
	addrs []net.Addr
}

// Finder finds default gateways. It is safe for concurrent use.
type Finder struct {
	discoverIPv4      func() (net.IP, error)
	discoverIPv6      func() (net.IP, error)
	discoverIfaceIPv6 func() (net.IP, error)
	interfaces        func() ([]iface, error)
}

// New returns a finder that reads the system's routing tables.
func New() *Finder {
	return &Finder{
		discoverIPv4:      gateway.DiscoverGateway,
		discoverIPv6:      gateway.DiscoverGatewayIPv6,
		discoverIfaceIPv6: gateway.DiscoverInterfaceIPv6,
		interfaces:        systemInterfaces,
	}
}

// IPv4 returns the default IPv4 gateway. The error wraps [ErrNotFound] when
// there is none.
func (f *Finder) IPv4() (netip.Addr, error) {
	addr, err := discover("ipv4", f.discoverIPv4)
	if err != nil {
		return netip.Addr{}, err
	}

	addr = addr.Unmap()
	if !addr.Is4() {
		return netip.Addr{}, fmt.Errorf("ipv4 gateway %s is not an IPv4 address", addr)
	}

	return addr, nil
}

// IPv6 returns the default IPv6 gateway. A link-local gateway gets the name of
// the interface its route uses as zone. The error wraps [ErrNotFound] when
// there is no gateway.
func (f *Finder) IPv6() (netip.Addr, error) {
	addr, err := discover("ipv6", f.discoverIPv6)
	if err != nil {
		return netip.Addr{}, err
	}

	if !addr.Is6() || addr.Is4In6() {
		return netip.Addr{}, fmt.Errorf("ipv6 gateway %s is not an IPv6 address", addr.Unmap())
	}

	if !addr.IsLinkLocalUnicast() {
		return addr, nil
	}

	zone, err := f.routeInterfaceIPv6()
	if err != nil {
		return netip.Addr{}, fmt.Errorf("find interface of ipv6 gateway %s: %w", addr, err)
	}

	return addr.WithZone(zone), nil
}

// routeInterfaceIPv6 returns the name of the interface that holds the local
// address of the default IPv6 route.
func (f *Finder) routeInterfaceIPv6() (string, error) {
	ip, err := f.discoverIfaceIPv6()
	if err != nil {
		return "", fmt.Errorf("discover local address: %w", err)
	}

	local, ok := netip.AddrFromSlice(ip)
	if !ok {
		return "", fmt.Errorf("invalid local address %v", ip)
	}

	ifaces, err := f.interfaces()
	if err != nil {
		return "", err
	}

	for _, ifc := range ifaces {
		for _, a := range ifc.addrs {
			ipNet, isIPNet := a.(*net.IPNet)
			if !isIPNet {
				continue
			}

			addr, valid := netip.AddrFromSlice(ipNet.IP)
			if valid && addr == local {
				return ifc.name, nil
			}
		}
	}

	return "", fmt.Errorf("no interface has local address %s", local)
}

func discover(family string, fn func() (net.IP, error)) (netip.Addr, error) {
	ip, err := fn()
	if _, noGateway := errors.AsType[*gateway.ErrNoGateway](err); noGateway {
		return netip.Addr{}, fmt.Errorf("%w: %s", ErrNotFound, family)
	}

	if err != nil {
		return netip.Addr{}, fmt.Errorf("discover %s gateway: %w", family, err)
	}

	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Addr{}, fmt.Errorf("discover %s gateway: invalid address %v", family, ip)
	}

	return addr, nil
}

func systemInterfaces() ([]iface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}

	result := make([]iface, 0, len(ifaces))

	for _, ifc := range ifaces {
		addrs, addrsErr := ifc.Addrs()
		if addrsErr != nil {
			return nil, fmt.Errorf("list addresses of %s: %w", ifc.Name, addrsErr)
		}

		result = append(result, iface{name: ifc.Name, addrs: addrs})
	}

	return result, nil
}
