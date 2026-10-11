// Package netif reads the private addresses assigned to the host's network
// interfaces.
//
// Only interfaces that are up and not loopback are considered, and only
// addresses reported as private by [netip.Addr.IsPrivate] are returned: IPv4
// in 10.0.0.0/8, 172.16.0.0/12 or 192.168.0.0/16 and IPv6 unique local
// addresses (fc00::/7).
package netif

import (
	"fmt"
	"net"
	"net/netip"
	"path"
	"slices"
)

const ipv4MappedBits = 96

// Addr is a private address of an interface.
type Addr struct {
	// Interface is the name of the interface, for example "en0".
	Interface string
	// Prefix is the address and the length of its subnet.
	Prefix netip.Prefix
}

// iface is the part of an interface that [Reader] needs. It lets tests
// replace the system's interfaces.
type iface struct {
	name  string
	flags net.Flags
	addrs []net.Addr
}

// Reader reads private interface addresses. It is safe for concurrent use.
type Reader struct {
	include    []string
	exclude    []string
	interfaces func() ([]iface, error)
}

// New returns a reader that considers the interfaces whose names match one of
// the include patterns, or all interfaces if include is empty, and skips those
// matching an exclude pattern. Patterns use [path.Match] syntax, for example
// "docker*" or "utun?".
func New(include, exclude []string) (*Reader, error) {
	for _, pattern := range slices.Concat(include, exclude) {
		_, err := path.Match(pattern, "")
		if err != nil {
			return nil, fmt.Errorf("interface pattern %q: %w", pattern, err)
		}
	}

	return &Reader{
		include:    slices.Clone(include),
		exclude:    slices.Clone(exclude),
		interfaces: systemInterfaces,
	}, nil
}

// PrivateAddrs returns the private addresses of the matching interfaces in
// the order the system reports them.
func (r *Reader) PrivateAddrs() ([]Addr, error) {
	ifaces, err := r.interfaces()
	if err != nil {
		return nil, err
	}

	var addrs []Addr

	for _, ifc := range ifaces {
		if ifc.flags&net.FlagUp == 0 || ifc.flags&net.FlagLoopback != 0 || !r.matches(ifc.name) {
			continue
		}

		for _, a := range ifc.addrs {
			prefix, ok := toPrefix(a)
			if ok && prefix.Addr().IsPrivate() {
				addrs = append(addrs, Addr{Interface: ifc.name, Prefix: prefix})
			}
		}
	}

	return addrs, nil
}

func (r *Reader) matches(name string) bool {
	match := func(pattern string) bool {
		ok, _ := path.Match(pattern, name) // Patterns were checked in New.

		return ok
	}

	if len(r.include) > 0 && !slices.ContainsFunc(r.include, match) {
		return false
	}

	return !slices.ContainsFunc(r.exclude, match)
}

// toPrefix converts an interface address to a prefix. IPv4 addresses that the
// system reports in their 16-byte form are converted to plain IPv4.
func toPrefix(a net.Addr) (netip.Prefix, bool) {
	var (
		ip   net.IP
		bits int
	)

	switch a := a.(type) {
	case *net.IPNet:
		ip = a.IP
		bits, _ = a.Mask.Size()
	case *net.IPAddr:
		ip = a.IP
		bits = -1
	default:
		return netip.Prefix{}, false
	}

	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Prefix{}, false
	}

	if addr.Is4In6() {
		addr = addr.Unmap()

		if bits > addr.BitLen() {
			bits -= ipv4MappedBits
		}
	}

	if bits < 0 {
		bits = addr.BitLen()
	}

	prefix := netip.PrefixFrom(addr, bits)

	return prefix, prefix.IsValid()
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

		result = append(result, iface{name: ifc.Name, flags: ifc.Flags, addrs: addrs})
	}

	return result, nil
}
