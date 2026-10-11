package iplookup_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/devusSs/netward/internal/adapter/iplookup"
)

const testTimeout = 5 * time.Second

// newServer starts a test server listening on loopback over network ("tcp4" or
// "tcp6"). It skips the test if the host has no loopback for that network.
func newServer(t *testing.T, network string, handler http.HandlerFunc) *httptest.Server {
	t.Helper()

	addr := "127.0.0.1:0"
	if network == "tcp6" {
		addr = "[::1]:0"
	}

	var lc net.ListenConfig

	l, err := lc.Listen(t.Context(), network, addr)
	if err != nil {
		t.Skipf("cannot listen on %s: %v", network, err)
	}

	srv := httptest.NewUnstartedServer(handler)
	srv.Listener.Close()
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)

	return srv
}

func respond(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}
}

func newClient(t *testing.T, family iplookup.Family, endpoint string, timeout time.Duration) *iplookup.Client {
	t.Helper()

	client, err := iplookup.New(family, endpoint, timeout)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return client
}

func TestFamilyString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		family iplookup.Family
		want   string
	}{
		{iplookup.FamilyIPv4, "ipv4"},
		{iplookup.FamilyIPv6, "ipv6"},
		{iplookup.Family(0), "Family(0)"},
		{iplookup.Family(42), "Family(42)"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()

			if got := tt.family.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	client := newClient(t, iplookup.FamilyIPv6, iplookup.DefaultIPv6Endpoint, testTimeout)

	if got := client.Family(); got != iplookup.FamilyIPv6 {
		t.Errorf("Family() = %s, want %s", got, iplookup.FamilyIPv6)
	}

	if got := client.Endpoint(); got != iplookup.DefaultIPv6Endpoint {
		t.Errorf("Endpoint() = %q, want %q", got, iplookup.DefaultIPv6Endpoint)
	}
}

func TestNewRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		family   iplookup.Family
		endpoint string
		timeout  time.Duration
		wantMsg  string
	}{
		{"zero family", iplookup.Family(0), iplookup.DefaultIPv4Endpoint, testTimeout, "invalid family"},
		{"unknown family", iplookup.Family(42), iplookup.DefaultIPv4Endpoint, testTimeout, "invalid family"},
		{"zero timeout", iplookup.FamilyIPv4, iplookup.DefaultIPv4Endpoint, 0, "timeout must be positive"},
		{
			"negative timeout",
			iplookup.FamilyIPv4,
			iplookup.DefaultIPv4Endpoint,
			-time.Second,
			"timeout must be positive",
		},
		{"unparsable endpoint", iplookup.FamilyIPv4, "http://[::1", testTimeout, "parse endpoint"},
		{"empty endpoint", iplookup.FamilyIPv4, "", testTimeout, "scheme must be http or https"},
		{"relative endpoint", iplookup.FamilyIPv4, "api.ipify.org", testTimeout, "scheme must be http or https"},
		{"unsupported scheme", iplookup.FamilyIPv4, "ftp://api.ipify.org", testTimeout, "scheme must be http or https"},
		{"missing host", iplookup.FamilyIPv4, "https:///path", testTimeout, "missing host"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, err := iplookup.New(tt.family, tt.endpoint, tt.timeout)
			if client != nil {
				t.Errorf("New() client = %v, want nil", client)
			}

			if err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("New() error = %v, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestLookupIPv4(t *testing.T) {
	t.Parallel()

	srv := newServer(t, "tcp4", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want %s", r.Method, http.MethodGet)
		}

		if got := r.Header.Get("Accept"); got != "text/plain" {
			t.Errorf("Accept = %q, want %q", got, "text/plain")
		}

		if got := r.Header.Get("User-Agent"); got != "netward" {
			t.Errorf("User-Agent = %q, want %q", got, "netward")
		}

		_, _ = io.WriteString(w, "203.0.113.7")
	})

	addr, err := newClient(t, iplookup.FamilyIPv4, srv.URL, testTimeout).Lookup(t.Context())
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}

	if want := netip.MustParseAddr("203.0.113.7"); addr != want {
		t.Errorf("Lookup() = %v, want %v", addr, want)
	}
}

func TestLookupIPv6(t *testing.T) {
	t.Parallel()

	srv := newServer(t, "tcp6", respond("2001:db8::7\n"))

	addr, err := newClient(t, iplookup.FamilyIPv6, srv.URL, testTimeout).Lookup(t.Context())
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}

	if want := netip.MustParseAddr("2001:db8::7"); addr != want {
		t.Errorf("Lookup() = %v, want %v", addr, want)
	}
}

func TestLookupNormalizesResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{"surrounding whitespace", " \t203.0.113.7\r\n"},
		{"ipv4-mapped ipv6", "::ffff:203.0.113.7"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := newServer(t, "tcp4", respond(tt.body))

			addr, err := newClient(t, iplookup.FamilyIPv4, srv.URL, testTimeout).Lookup(t.Context())
			if err != nil {
				t.Fatalf("Lookup() error = %v", err)
			}

			if want := netip.MustParseAddr("203.0.113.7"); addr != want {
				t.Errorf("Lookup() = %v, want %v", addr, want)
			}
		})
	}
}

func TestLookupFollowsRedirects(t *testing.T) {
	t.Parallel()

	target := newServer(t, "tcp4", respond("203.0.113.7"))
	srv := newServer(t, "tcp4", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	})

	addr, err := newClient(t, iplookup.FamilyIPv4, srv.URL, testTimeout).Lookup(t.Context())
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}

	if want := netip.MustParseAddr("203.0.113.7"); addr != want {
		t.Errorf("Lookup() = %v, want %v", addr, want)
	}
}

func TestLookupRejectsInvalidResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantMsg string
	}{
		{
			name: "server error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "boom", http.StatusInternalServerError)
			},
			wantMsg: "unexpected status 500 Internal Server Error",
		},
		{
			name: "no content",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			},
			wantMsg: "unexpected status 204 No Content",
		},
		{
			name:    "empty body",
			handler: respond(""),
			wantMsg: "ParseAddr",
		},
		{
			name:    "not an address",
			handler: respond("<html>hello</html>"),
			wantMsg: "ParseAddr",
		},
		{
			name:    "json body",
			handler: respond(`{"ip":"203.0.113.7"}`),
			wantMsg: "ParseAddr",
		},
		{
			name:    "body too large",
			handler: respond(strings.Repeat(" ", 100) + "203.0.113.7"),
			wantMsg: "body exceeds 64 bytes",
		},
		{
			name:    "wrong family",
			handler: respond("2001:db8::7"),
			wantMsg: "address 2001:db8::7 is not ipv4",
		},
		{
			name:    "zoned address",
			handler: respond("fe80::1%eth0"),
			wantMsg: "address fe80::1%eth0 has a zone",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := newServer(t, "tcp4", tt.handler)

			addr, err := newClient(t, iplookup.FamilyIPv4, srv.URL, testTimeout).Lookup(t.Context())
			if addr.IsValid() {
				t.Errorf("Lookup() = %v, want invalid address", addr)
			}

			if !errors.Is(err, iplookup.ErrInvalidResponse) {
				t.Fatalf("Lookup() error = %v, want %v", err, iplookup.ErrInvalidResponse)
			}

			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("Lookup() error = %q, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestLookupOnlyDialsItsFamily(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		serverNetwork string
		family        iplookup.Family
	}{
		{"ipv6 client to ipv4 server", "tcp4", iplookup.FamilyIPv6},
		{"ipv4 client to ipv6 server", "tcp6", iplookup.FamilyIPv4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := newServer(t, tt.serverNetwork, func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("server was reached over the wrong family")
			})

			_, err := newClient(t, tt.family, srv.URL, testTimeout).Lookup(t.Context())
			if err == nil {
				t.Fatal("Lookup() error = nil, want an error")
			}

			if errors.Is(err, iplookup.ErrInvalidResponse) {
				t.Errorf("Lookup() error = %v, want a connection error", err)
			}
		})
	}
}

func TestLookupTimeout(t *testing.T) {
	t.Parallel()

	srv := newServer(t, "tcp4", func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	_, err := newClient(t, iplookup.FamilyIPv4, srv.URL, 50*time.Millisecond).Lookup(t.Context())

	netErr, ok := errors.AsType[net.Error](err)
	if !ok || !netErr.Timeout() {
		t.Errorf("Lookup() error = %v, want a timeout", err)
	}
}

func TestLookupContextCanceled(t *testing.T) {
	t.Parallel()

	srv := newServer(t, "tcp4", respond("203.0.113.7"))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := newClient(t, iplookup.FamilyIPv4, srv.URL, testTimeout).Lookup(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Lookup() error = %v, want %v", err, context.Canceled)
	}
}

func TestIsUnavailable(t *testing.T) {
	t.Parallel()

	dialErr := func(errno syscall.Errno) error {
		return fmt.Errorf("send request: %w", &net.OpError{
			Op:     "dial",
			Net:    "tcp6",
			Source: nil,
			Addr:   nil,
			Err:    os.NewSyscallError("connect", errno),
		})
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"network unreachable", dialErr(syscall.ENETUNREACH), true},
		{"host unreachable", dialErr(syscall.EHOSTUNREACH), true},
		{"address not available", dialErr(syscall.EADDRNOTAVAIL), true},
		{"connection refused", dialErr(syscall.ECONNREFUSED), false},
		{"timeout", context.DeadlineExceeded, false},
		{"other", errors.New("boom"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := iplookup.IsUnavailable(tt.err); got != tt.want {
				t.Errorf("IsUnavailable(%v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}
