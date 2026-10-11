// Package iplookup looks up the host's public IP address through an HTTP
// endpoint that answers with the caller's address as plain text, such as
// [ipify].
//
// A [Client] is bound to one IP family. It only opens connections over that
// family, so the endpoint sees, and reports, the host's public address of that
// family, even when the endpoint itself supports both.
//
// [ipify]: https://www.ipify.org
package iplookup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

const (
	// DefaultIPv4Endpoint is the ipify endpoint for IPv4 lookups.
	DefaultIPv4Endpoint = "https://api.ipify.org"
	// DefaultIPv6Endpoint is the ipify endpoint for IPv6 lookups.
	DefaultIPv6Endpoint = "https://api6.ipify.org"
)

const (
	userAgent = "netward"
	// maxBodySize is generous for the longest textual IPv6 address (45 bytes)
	// plus surrounding whitespace.
	maxBodySize = 64
)

var (
	// ErrUnavailable is returned by [Client.Lookup] when the host has no
	// connectivity over the client's IP family, for example an IPv6 lookup on
	// a network without IPv6.
	ErrUnavailable = errors.New("ip family unavailable")
	// ErrInvalidResponse is returned by [Client.Lookup] when the endpoint
	// answers with an unexpected status or a body that is not an address of
	// the client's IP family.
	ErrInvalidResponse = errors.New("invalid response")
)

// Family is an IP address family.
type Family uint8

const (
	// FamilyIPv4 selects IPv4.
	FamilyIPv4 Family = iota + 1
	// FamilyIPv6 selects IPv6.
	FamilyIPv6
)

// String returns "ipv4" or "ipv6".
func (f Family) String() string {
	switch f {
	case FamilyIPv4:
		return "ipv4"
	case FamilyIPv6:
		return "ipv6"
	}

	return fmt.Sprintf("Family(%d)", uint8(f))
}

func (f Family) network() (string, bool) {
	switch f {
	case FamilyIPv4:
		return "tcp4", true
	case FamilyIPv6:
		return "tcp6", true
	}

	return "", false
}

func (f Family) contains(addr netip.Addr) bool {
	switch f {
	case FamilyIPv4:
		return addr.Is4()
	case FamilyIPv6:
		return addr.Is6()
	}

	return false
}

// Client looks up the public address of one IP family. It is safe for
// concurrent use.
type Client struct {
	family   Family
	endpoint string
	http     *http.Client
}

// New returns a client that queries endpoint over family. The endpoint must be
// an absolute http or https URL, and timeout limits each lookup, including
// connecting, redirects and reading the response.
//
// The client ignores proxy settings from the environment: a proxy would report
// its own address instead of the host's.
func New(family Family, endpoint string, timeout time.Duration) (*Client, error) {
	network, ok := family.network()
	if !ok {
		return nil, fmt.Errorf("invalid family %s", family)
	}

	if timeout <= 0 {
		return nil, fmt.Errorf("timeout must be positive, got %s", timeout)
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint: %w", err)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("endpoint %q: scheme must be http or https", endpoint)
	}

	if u.Host == "" {
		return nil, fmt.Errorf("endpoint %q: missing host", endpoint)
	}

	var dialer net.Dialer

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
		ForceAttemptHTTP2: true,
	}

	return &Client{
		family:   family,
		endpoint: u.String(),
		http:     &http.Client{Transport: transport, Timeout: timeout},
	}, nil
}

// Family returns the IP family the client looks up.
func (c *Client) Family() Family {
	return c.family
}

// Endpoint returns the URL the client queries.
func (c *Client) Endpoint() string {
	return c.endpoint
}

// Lookup returns the host's public address of the client's family.
// IPv4-mapped IPv6 addresses in the response are converted to plain IPv4.
//
// The error wraps [ErrUnavailable] when the host cannot reach the network over
// this family, and [ErrInvalidResponse] when the endpoint's answer is unusable.
func (c *Client) Lookup(ctx context.Context) (netip.Addr, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "text/plain")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		if isUnavailable(err) {
			return netip.Addr{}, fmt.Errorf("%w: %s: %w", ErrUnavailable, c.family, err)
		}

		return netip.Addr{}, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return netip.Addr{}, fmt.Errorf("%w: unexpected status %s", ErrInvalidResponse, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize+1))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("read response: %w", err)
	}

	if len(body) > maxBodySize {
		return netip.Addr{}, fmt.Errorf("%w: body exceeds %d bytes", ErrInvalidResponse, maxBodySize)
	}

	return c.parse(string(body))
}

func (c *Client) parse(body string) (netip.Addr, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(body))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("%w: %w", ErrInvalidResponse, err)
	}

	addr = addr.Unmap()

	switch {
	case addr.Zone() != "":
		return netip.Addr{}, fmt.Errorf("%w: address %s has a zone", ErrInvalidResponse, addr)
	case !c.family.contains(addr):
		return netip.Addr{}, fmt.Errorf("%w: address %s is not %s", ErrInvalidResponse, addr, c.family)
	}

	return addr, nil
}

// isUnavailable reports whether err means the host has no route over the
// dialed IP family, as opposed to the endpoint failing.
func isUnavailable(err error) bool {
	return errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.EADDRNOTAVAIL)
}
