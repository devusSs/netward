package domainip_test

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/devusSs/netward/internal/domain/domainip"
	"github.com/devusSs/netward/internal/domain/probe"
)

func testTime() time.Time {
	return time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
}

func addrs(s ...string) []netip.Addr {
	result := make([]netip.Addr, 0, len(s))
	for _, a := range s {
		result = append(result, netip.MustParseAddr(a))
	}

	return result
}

func okLookup(domain string, a ...string) domainip.LookupProbe {
	return domainip.LookupProbe{
		Result: probe.NewResult(testTime(), 20*time.Millisecond, nil),
		Domain: domain,
		Addrs:  addrs(a...),
	}
}

func TestNormalizeDomain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		domain  string
		want    string
		wantErr string
	}{
		{"example.com", "example.com", ""},
		{"Home.Example.COM.", "home.example.com", ""},
		{"localhost", "localhost", ""},
		{"_dmarc.example.com", "_dmarc.example.com", ""},
		{"xn--bcher-kva.example", "xn--bcher-kva.example", ""},
		{"a-b.example", "a-b.example", ""},
		{"", "", "length must be 1 to 253 characters"},
		{".", "", "length must be 1 to 253 characters"},
		{strings.Repeat("a.", 127) + "a", "", "length must be 1 to 253 characters"},
		{"example..com", "", "labels must be 1 to 63 characters"},
		{strings.Repeat("a", 64) + ".com", "", "labels must be 1 to 63 characters"},
		{"-example.com", "", "labels must not start or end with a hyphen"},
		{"example-.com", "", "labels must not start or end with a hyphen"},
		{"exa mple.com", "", `invalid character ' '`},
		{"bücher.example", "", `invalid character 'ü'`},
		{"https://example.com", "", `invalid character ':'`},
	}

	for _, tt := range tests {
		t.Run(tt.domain, func(t *testing.T) {
			t.Parallel()

			got, err := domainip.NormalizeDomain(tt.domain)

			switch {
			case tt.wantErr == "" && (err != nil || got != tt.want):
				t.Errorf("NormalizeDomain(%q) = %q, %v, want %q", tt.domain, got, err, tt.want)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr) || got != ""):
				t.Errorf("NormalizeDomain(%q) = %q, %v, want error containing %q", tt.domain, got, err, tt.wantErr)
			}
		})
	}
}

func TestNewSnapshot(t *testing.T) {
	t.Parallel()

	timestamp := time.Date(2026, time.October, 11, 14, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	input := []domainip.LookupProbe{
		okLookup("Home.Example.com.", "2001:db8::7", "203.0.113.7", "::ffff:203.0.113.7", "203.0.113.7"),
		okLookup("dns.example.net", "198.51.100.53"),
		{
			Result: probe.NewResult(testTime(), time.Millisecond, errors.New("no such host"), errors.New("x")),
			Domain: "gone.example.org",
			Addrs:  nil,
		},
	}
	original := slices.Clone(input[0].Addrs)

	snap, err := domainip.NewSnapshot(timestamp, "1.1.1.1:53", input)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}

	if snap.ID == uuid.Nil() || snap.ID[6]>>4 != 7 {
		t.Errorf("ID = %v, want a version 7 UUID", snap.ID)
	}

	if !snap.Timestamp.Equal(testTime()) || snap.Timestamp.Location() != time.UTC || snap.Resolver != "1.1.1.1:53" {
		t.Errorf(
			"Timestamp, Resolver = %v, %q, want %v in UTC and 1.1.1.1:53",
			snap.Timestamp,
			snap.Resolver,
			testTime(),
		)
	}

	domains := make([]string, 0, len(snap.Lookups))
	for _, l := range snap.Lookups {
		domains = append(domains, l.Domain)
	}

	if want := []string{"dns.example.net", "gone.example.org", "home.example.com"}; !slices.Equal(domains, want) {
		t.Errorf("domains = %v, want %v", domains, want)
	}

	if want := addrs("203.0.113.7", "2001:db8::7"); !slices.Equal(snap.Lookups[2].Addrs, want) {
		t.Errorf("home.example.com addrs = %v, want %v", snap.Lookups[2].Addrs, want)
	}

	if snap.Lookups[1].Addrs != nil || snap.Lookups[1].Status != probe.StatusFailed {
		t.Errorf("gone.example.org = %+v, want failed without addresses", snap.Lookups[1])
	}

	if !slices.Equal(input[0].Addrs, original) || input[0].Domain != "Home.Example.com." {
		t.Errorf("input was modified: %+v", input[0])
	}
}

func TestNewSnapshotAcceptsNoLookups(t *testing.T) {
	t.Parallel()

	snap, err := domainip.NewSnapshot(testTime(), "system", nil)
	if err != nil || len(snap.Lookups) != 0 {
		t.Errorf("NewSnapshot() = %+v, %v, want an empty snapshot", snap, err)
	}
}

func TestNewSnapshotRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		timestamp time.Time
		resolver  string
		lookups   []domainip.LookupProbe
		wantMsg   string
	}{
		{"zero timestamp", time.Time{}, "system", nil, "timestamp is zero"},
		{"empty resolver", testTime(), "", nil, "resolver is empty"},
		{
			"invalid domain", testTime(), "system",
			[]domainip.LookupProbe{okLookup("exa mple.com", "203.0.113.7")},
			`domain "exa mple.com": invalid character ' '`,
		},
		{
			"unknown status", testTime(), "system",
			[]domainip.LookupProbe{func() domainip.LookupProbe {
				l := okLookup("example.com", "203.0.113.7")
				l.Status = probe.StatusUnknown

				return l
			}()},
			"example.com: invalid status unknown",
		},
		{
			"ok without addresses", testTime(), "system",
			[]domainip.LookupProbe{okLookup("example.com")},
			"example.com: status ok but no addresses",
		},
		{
			"addresses on unavailable lookup", testTime(), "system",
			[]domainip.LookupProbe{func() domainip.LookupProbe {
				l := okLookup("example.com", "203.0.113.7", "2001:db8::7")
				l.Status = probe.StatusUnavailable

				return l
			}()},
			"example.com: 2 addresses set on lookup with status unavailable",
		},
		{
			"invalid address",
			testTime(),
			"system",
			[]domainip.LookupProbe{
				{Result: okLookup("example.com").Result, Domain: "example.com", Addrs: []netip.Addr{{}}},
			},
			"example.com: invalid address invalid IP",
		},
		{
			"zoned address", testTime(), "system",
			[]domainip.LookupProbe{okLookup("example.com", "fe80::1%en0")},
			"example.com: invalid address fe80::1%en0",
		},
		{
			"duplicate domain after normalizing", testTime(), "system",
			[]domainip.LookupProbe{okLookup("example.com", "203.0.113.7"), okLookup("EXAMPLE.com.", "203.0.113.8")},
			"example.com: duplicate domain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			snap, err := domainip.NewSnapshot(tt.timestamp, tt.resolver, tt.lookups)
			if snap != nil {
				t.Errorf("NewSnapshot() snapshot = %+v, want nil", snap)
			}

			if !errors.Is(err, domainip.ErrInvalidSnapshot) {
				t.Fatalf("NewSnapshot() error = %v, want %v", err, domainip.ErrInvalidSnapshot)
			}

			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("NewSnapshot() error = %q, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestNewSnapshotJoinsErrors(t *testing.T) {
	t.Parallel()

	_, err := domainip.NewSnapshot(testTime(), "system", []domainip.LookupProbe{
		okLookup("-bad.example", "203.0.113.7"),
		okLookup("empty.example"),
	})
	if !errors.Is(err, domainip.ErrInvalidSnapshot) {
		t.Fatalf("NewSnapshot() error = %v, want %v", err, domainip.ErrInvalidSnapshot)
	}

	for _, want := range []string{"must not start or end with a hyphen", "empty.example: status ok but no addresses"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("NewSnapshot() error = %q, want it to contain %q", err, want)
		}
	}
}
