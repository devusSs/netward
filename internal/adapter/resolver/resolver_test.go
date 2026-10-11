package resolver_test

import (
	"errors"
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/devusSs/netward/internal/adapter/resolver"
)

const testTimeout = 5 * time.Second

// records are the answers of the fake DNS server. Names it does not know get
// NXDOMAIN.
var records = map[string][]netip.Addr{ //nolint:gochecknoglobals // Read-only fixture.
	"home.example.com.":   {netip.MustParseAddr("203.0.113.7"), netip.MustParseAddr("2001:db8::7")},
	"v4only.example.com.": {netip.MustParseAddr("198.51.100.1")},
	"empty.example.com.":  {},
}

// startServer runs a fake DNS server on a UDP loopback port and returns its
// address. "broken." names get SERVFAIL and "slow." names get no answer.
func startServer(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig

	conn, err := lc.ListenPacket(t.Context(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	go serve(conn)

	return conn.LocalAddr().String()
}

func serve(conn net.PacketConn) {
	buf := make([]byte, 1500)

	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}

		resp, ok := answer(buf[:n])
		if ok {
			_, _ = conn.WriteTo(resp, from)
		}
	}
}

func answer(query []byte) ([]byte, bool) {
	var parser dnsmessage.Parser

	header, err := parser.Start(query)
	if err != nil {
		return nil, false
	}

	question, err := parser.Question()
	if err != nil {
		return nil, false
	}

	// DNS names are case-insensitive.
	name := strings.ToLower(question.Name.String())
	if strings.HasPrefix(name, "slow.") {
		return nil, false
	}

	addrs, known := records[name]

	rcode := dnsmessage.RCodeSuccess

	switch {
	case strings.HasPrefix(name, "broken."):
		rcode = dnsmessage.RCodeServerFailure
	case !known:
		rcode = dnsmessage.RCodeNameError
	}

	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 header.ID,
		Response:           true,
		OpCode:             0,
		Authoritative:      true,
		Truncated:          false,
		RecursionDesired:   header.RecursionDesired,
		RecursionAvailable: true,
		AuthenticData:      false,
		CheckingDisabled:   false,
		RCode:              rcode,
	})

	_ = builder.StartQuestions()
	_ = builder.Question(question)
	_ = builder.StartAnswers()

	resource := dnsmessage.ResourceHeader{
		Name:   question.Name,
		Type:   question.Type,
		Class:  dnsmessage.ClassINET,
		TTL:    60,
		Length: 0,
	}

	for _, addr := range addrs {
		switch {
		case question.Type == dnsmessage.TypeA && addr.Is4():
			_ = builder.AResource(resource, dnsmessage.AResource{A: addr.As4()})
		case question.Type == dnsmessage.TypeAAAA && addr.Is6():
			_ = builder.AAAAResource(resource, dnsmessage.AAAAResource{AAAA: addr.As16()})
		}
	}

	resp, err := builder.Finish()

	return resp, err == nil
}

func newResolver(t *testing.T, server string, timeout time.Duration) *resolver.Resolver {
	t.Helper()

	r, err := resolver.New(server, timeout)
	if err != nil {
		t.Fatalf("New(%q) error = %v", server, err)
	}

	return r
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		server   string
		wantName string
	}{
		{"", resolver.SystemName},
		{"1.1.1.1", "1.1.1.1:53"},
		{"192.168.178.2:5353", "192.168.178.2:5353"},
		{"2606:4700:4700::1111", "[2606:4700:4700::1111]:53"},
		{"[2606:4700:4700::1111]:853", "[2606:4700:4700::1111]:853"},
		{"::ffff:9.9.9.9", "9.9.9.9:53"},
	}

	for _, tt := range tests {
		t.Run(tt.server, func(t *testing.T) {
			t.Parallel()

			if got := newResolver(t, tt.server, testTimeout).Name(); got != tt.wantName {
				t.Errorf("Name() = %q, want %q", got, tt.wantName)
			}
		})
	}
}

func TestNewRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		server  string
		timeout time.Duration
		wantMsg string
	}{
		{"zero timeout", "", 0, "timeout must be positive"},
		{"negative timeout", "1.1.1.1", -time.Second, "timeout must be positive"},
		{"host name", "dns.google", testTimeout, `dns server "dns.google": must be an IP address`},
		{"url", "https://1.1.1.1/dns-query", testTimeout, "must be an IP address"},
		{"bad port", "1.1.1.1:99999", testTimeout, "must be an IP address"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, err := resolver.New(tt.server, tt.timeout)
			if r != nil || err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("New() = %v, %v, want nil and an error containing %q", r, err, tt.wantMsg)
			}
		})
	}
}

func TestLookup(t *testing.T) {
	t.Parallel()

	r := newResolver(t, startServer(t), testTimeout)

	tests := []struct {
		domain string
		want   []netip.Addr
	}{
		{"home.example.com", records["home.example.com."]},
		{"HOME.example.com.", records["home.example.com."]},
		{"v4only.example.com", records["v4only.example.com."]},
	}

	for _, tt := range tests {
		t.Run(tt.domain, func(t *testing.T) {
			t.Parallel()

			got, err := r.Lookup(t.Context(), tt.domain)
			if err != nil {
				t.Fatalf("Lookup() error = %v", err)
			}

			slices.SortFunc(got, netip.Addr.Compare)

			if !slices.Equal(got, tt.want) {
				t.Errorf("Lookup() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLookupErrors(t *testing.T) {
	t.Parallel()

	r := newResolver(t, startServer(t), 300*time.Millisecond)

	tests := []struct {
		domain       string
		wantNotFound bool
		wantMsg      string
	}{
		{"missing.example.com", true, "domain not found: missing.example.com"},
		{"empty.example.com", true, "domain not found: empty.example.com"},
		{"broken.example.com", false, "resolve broken.example.com via 127.0.0.1:"},
		{"slow.example.com", false, "resolve slow.example.com via 127.0.0.1:"},
	}

	for _, tt := range tests {
		t.Run(tt.domain, func(t *testing.T) {
			t.Parallel()

			addrs, err := r.Lookup(t.Context(), tt.domain)
			if addrs != nil {
				t.Errorf("Lookup() = %v, want nil", addrs)
			}

			if got := errors.Is(err, resolver.ErrNotFound); got != tt.wantNotFound {
				t.Errorf("Lookup() error = %v, want not found %t", err, tt.wantNotFound)
			}

			if err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("Lookup() error = %v, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestLookupSystem(t *testing.T) {
	t.Parallel()

	addrs, err := newResolver(t, "", testTimeout).Lookup(t.Context(), "localhost")
	if err != nil {
		t.Fatalf("Lookup(localhost) error = %v", err)
	}

	if !slices.ContainsFunc(addrs, netip.Addr.IsLoopback) {
		t.Errorf("Lookup(localhost) = %v, want a loopback address", addrs)
	}
}
