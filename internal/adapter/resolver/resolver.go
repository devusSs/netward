// Package resolver resolves domains to their IPv4 and IPv6 addresses, either
// with the host's system resolver or with a specific DNS server.
package resolver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

const (
	// SystemName is what [Resolver.Name] returns for the system resolver.
	SystemName = "system"

	dnsPort = 53
)

// ErrNotFound is returned by [Resolver.Lookup] when the domain does not exist
// or has no IPv4 or IPv6 addresses.
var ErrNotFound = errors.New("domain not found")

// Resolver resolves domains. It is safe for concurrent use.
type Resolver struct {
	name     string
	resolver *net.Resolver
	timeout  time.Duration
}

// New returns a resolver that queries server, or the system resolver if
// server is empty. Server is an IP address with an optional port, for example
// "1.1.1.1", "192.168.178.2:5353" or "[2606:4700:4700::1111]:53"; the port
// defaults to 53. Timeout limits each lookup.
func New(server string, timeout time.Duration) (*Resolver, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("timeout must be positive, got %s", timeout)
	}

	if server == "" {
		return &Resolver{name: SystemName, resolver: net.DefaultResolver, timeout: timeout}, nil
	}

	addr, err := parseServer(server)
	if err != nil {
		return nil, err
	}

	var dialer net.Dialer

	return &Resolver{
		name: addr.String(),
		resolver: &net.Resolver{
			// Only Go's own resolver honors Dial; the system one would ignore
			// the server.
			PreferGo: true,
			// Return the addresses of one family even if the query for the
			// other one times out, like the system resolver does.
			StrictErrors: false,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr.String())
			},
		},
		timeout: timeout,
	}, nil
}

func parseServer(server string) (netip.AddrPort, error) {
	addrPort, err := netip.ParseAddrPort(server)
	if err == nil {
		return netip.AddrPortFrom(addrPort.Addr().Unmap(), addrPort.Port()), nil
	}

	addr, addrErr := netip.ParseAddr(server)
	if addrErr != nil {
		return netip.AddrPort{}, fmt.Errorf("dns server %q: must be an IP address with an optional port", server)
	}

	return netip.AddrPortFrom(addr.Unmap(), dnsPort), nil
}

// Name returns "system" for the system resolver or the server's address,
// for example "1.1.1.1:53".
func (r *Resolver) Name() string {
	return r.name
}

// Lookup returns the IPv4 and IPv6 addresses of domain in the order the
// resolver returned them. The error wraps [ErrNotFound] when the domain does
// not exist or has no addresses.
func (r *Resolver) Lookup(ctx context.Context, domain string) ([]netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	addrs, err := r.resolver.LookupNetIP(ctx, "ip", domain)
	if err != nil {
		if dnsErr, ok := errors.AsType[*net.DNSError](err); ok && dnsErr.IsNotFound {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, domain)
		}

		return nil, fmt.Errorf("resolve %s via %s: %w", domain, r.name, err)
	}

	return addrs, nil
}
