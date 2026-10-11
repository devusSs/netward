package history_test

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devusSs/netward/internal/domain/domainip"
	"github.com/devusSs/netward/internal/domain/privatenet"
	"github.com/devusSs/netward/internal/domain/probe"
	"github.com/devusSs/netward/internal/domain/publicnet"
	"github.com/devusSs/netward/internal/service/history"
)

func at(minute int) time.Time {
	return time.Date(2026, time.October, 11, 12, minute, 0, 0, time.UTC)
}

func result(status probe.Status, msg string) probe.Result {
	return probe.Result{Status: status, Message: msg, StartedAt: time.Time{}, Duration: 0}
}

func ok() probe.Result {
	return result(probe.StatusOK, "")
}

func addr(s string) netip.Addr {
	if s == "" {
		return netip.Addr{}
	}

	return netip.MustParseAddr(s)
}

// fakeStore keeps snapshots in memory and filters them like the SQLite store.
type fakeStore struct {
	public  []*publicnet.Snapshot
	private []*privatenet.Snapshot
	domains []*domainip.Snapshot
	err     error
}

func filter[S any](snaps []*S, timestamp func(*S) time.Time, from, to time.Time, limit int) []*S {
	var result []*S

	for _, s := range snaps {
		if !timestamp(s).Before(from) && timestamp(s).Before(to) {
			result = append(result, s)
		}
	}

	slices.SortFunc(result, func(a, b *S) int { return timestamp(b).Compare(timestamp(a)) })

	if len(result) > limit {
		result = result[:limit]
	}

	return result
}

func (f *fakeStore) PublicNetSnapshots(
	_ context.Context, from, to time.Time, limit int,
) ([]*publicnet.Snapshot, error) {
	return filter(f.public, func(s *publicnet.Snapshot) time.Time { return s.Timestamp }, from, to, limit), f.err
}

func (f *fakeStore) PrivateNetSnapshots(
	_ context.Context, from, to time.Time, limit int,
) ([]*privatenet.Snapshot, error) {
	return filter(f.private, func(s *privatenet.Snapshot) time.Time { return s.Timestamp }, from, to, limit), nil
}

func (f *fakeStore) DomainIPSnapshots(
	_ context.Context, from, to time.Time, limit int,
) ([]*domainip.Snapshot, error) {
	return filter(f.domains, func(s *domainip.Snapshot) time.Time { return s.Timestamp }, from, to, limit), nil
}

func public(minute int, ipv4 probe.Result, ipv4Addr string, conn probe.Result) *publicnet.Snapshot {
	snap := new(publicnet.Snapshot)
	snap.Timestamp = at(minute)
	snap.IPv4 = publicnet.IPAddrProbe{Result: ipv4, Source: "", Addr: addr(ipv4Addr)}
	snap.IPv6 = publicnet.IPAddrProbe{Result: probe.Disabled(), Source: "", Addr: netip.Addr{}}
	snap.ConnIPv4.Result = conn
	snap.ConnIPv6.Result = probe.Disabled()

	return snap
}

func private(minute int, addrs []string, gateway probe.Result, gatewayAddr string) *privatenet.Snapshot {
	snap := new(privatenet.Snapshot)
	snap.Timestamp = at(minute)
	snap.Addrs.Result = ok()

	for _, a := range addrs {
		iface, prefix, _ := strings.Cut(a, " ")
		snap.Addrs.Addrs = append(snap.Addrs.Addrs,
			privatenet.InterfaceAddr{Interface: iface, Prefix: netip.MustParsePrefix(prefix)})
	}

	snap.GatewayIPv4.Result = gateway
	snap.GatewayIPv4.Addr = addr(gatewayAddr)
	snap.GatewayIPv6.Result = probe.Disabled()

	return snap
}

func domains(minute int, lookups ...domainip.LookupProbe) *domainip.Snapshot {
	snap := new(domainip.Snapshot)
	snap.Timestamp = at(minute)
	snap.Resolver = "system"
	snap.Lookups = lookups

	return snap
}

func lookup(domain string, res probe.Result, addrs ...string) domainip.LookupProbe {
	l := domainip.LookupProbe{Result: res, Domain: domain, Addrs: nil}
	for _, a := range addrs {
		l.Addrs = append(l.Addrs, addr(a))
	}

	return l
}

func event(
	minute int,
	source history.Source,
	subject string,
	kind history.Kind,
	oldValue, newValue, msg string,
) history.Event {
	return history.Event{
		Time:    at(minute),
		Source:  source,
		Subject: subject,
		Kind:    kind,
		Old:     oldValue,
		New:     newValue,
		Message: msg,
	}
}

func changes(t *testing.T, store *fakeStore, since time.Time, limit int) []history.Event {
	t.Helper()

	svc, err := history.New(store)
	if err != nil {
		t.Fatal(err)
	}

	events, err := svc.Changes(t.Context(), since, limit)
	if err != nil {
		t.Fatalf("Changes() error = %v", err)
	}

	return events
}

func wantEvents(t *testing.T, got, want []history.Event) {
	t.Helper()

	if !reflect.DeepEqual(got, want) {
		t.Errorf("events:\n%s\nwant:\n%s", format(got), format(want))
	}
}

func format(events []history.Event) string {
	lines := make([]string, 0, len(events))
	for _, e := range events {
		lines = append(lines, e.Time.Format("15:04")+" "+string(e.Source)+" "+e.Subject+" "+string(e.Kind)+
			" "+e.Old+" -> "+e.New+" "+e.Message)
	}

	return strings.Join(lines, "\n")
}

func TestPublicNetChanges(t *testing.T) {
	t.Parallel()

	failed := result(probe.StatusFailed, "i/o timeout")
	store := &fakeStore{
		public: []*publicnet.Snapshot{
			public(0, ok(), "203.0.113.7", ok()),
			public(1, ok(), "203.0.113.7", ok()),
			public(2, failed, "", result(probe.StatusFailed, "no reply")),
			public(3, ok(), "203.0.113.7", ok()),
			public(4, ok(), "198.51.100.9", ok()),
		},
		private: nil,
		domains: nil,
		err:     nil,
	}

	src := history.SourcePublicNet

	wantEvents(t, changes(t, store, time.Time{}, 100), []history.Event{
		event(4, src, "ipv4", history.KindChanged, "203.0.113.7", "198.51.100.9", ""),
		event(3, src, "conn_ipv4", history.KindStatus, "failed", "ok", ""),
		event(3, src, "ipv4", history.KindStatus, "failed", "ok", ""),
		event(2, src, "conn_ipv4", history.KindStatus, "ok", "failed", "no reply"),
		event(2, src, "ipv4", history.KindStatus, "ok", "failed", "i/o timeout"),
	})
}

func TestBaselineBeforeSince(t *testing.T) {
	t.Parallel()

	store := &fakeStore{
		public: []*publicnet.Snapshot{
			public(0, ok(), "203.0.113.7", ok()),
			public(5, ok(), "203.0.113.7", ok()),
			public(10, ok(), "198.51.100.9", ok()),
			public(11, ok(), "198.51.100.9", ok()),
		},
		private: nil,
		domains: nil,
		err:     nil,
	}

	want := []history.Event{
		event(10, history.SourcePublicNet, "ipv4", history.KindChanged, "203.0.113.7", "198.51.100.9", ""),
	}

	// The change at minute 10 compares with the snapshot at minute 5, which
	// is before since and only serves as the baseline.
	wantEvents(t, changes(t, store, at(10), 100), want)
	wantEvents(t, changes(t, store, at(11), 100), nil)
}

func TestPrivateNetChanges(t *testing.T) {
	t.Parallel()

	store := &fakeStore{
		public: nil,
		private: []*privatenet.Snapshot{
			private(0, []string{"en0 192.168.178.23/24", "en0 fd00::23/64"}, ok(), "192.168.178.1"),
			private(1, []string{"en0 192.168.178.23/24", "wlan0 10.0.0.5/8"}, ok(), "192.168.178.1"),
			private(2, []string{"en0 192.168.178.23/24", "wlan0 10.0.0.5/8"},
				result(probe.StatusUnavailable, "no default gateway: ipv4"), ""),
			private(3, []string{"en0 192.168.178.23/24", "wlan0 10.0.0.5/8"}, ok(), "192.168.178.1"),
			private(4, []string{"en0 192.168.178.23/24", "wlan0 10.0.0.5/8"},
				result(probe.StatusFailed, "no reply"), "192.168.178.254"),
		},
		domains: nil,
		err:     nil,
	}

	src := history.SourcePrivateNet

	wantEvents(t, changes(t, store, time.Time{}, 100), []history.Event{
		event(4, src, "gateway_ipv4", history.KindChanged, "192.168.178.1", "192.168.178.254", ""),
		event(4, src, "gateway_ipv4", history.KindStatus, "ok", "failed", "no reply"),
		event(3, src, "gateway_ipv4", history.KindStatus, "unavailable", "ok", ""),
		event(2, src, "gateway_ipv4", history.KindStatus, "ok", "unavailable", "no default gateway: ipv4"),
		event(1, src, "en0", history.KindRemoved, "fd00::23/64", "", ""),
		event(1, src, "wlan0", history.KindAdded, "", "10.0.0.5/8", ""),
	})
}

func TestPrivateNetIgnoresFailedAddressReads(t *testing.T) {
	t.Parallel()

	failedRead := private(1, nil, ok(), "192.168.178.1")
	failedRead.Addrs.Result = result(probe.StatusFailed, "boom")

	store := &fakeStore{
		public: nil,
		private: []*privatenet.Snapshot{
			private(0, []string{"en0 192.168.178.23/24"}, ok(), "192.168.178.1"),
			failedRead,
			private(2, []string{"en0 192.168.178.23/24"}, ok(), "192.168.178.1"),
		},
		domains: nil,
		err:     nil,
	}

	src := history.SourcePrivateNet

	wantEvents(t, changes(t, store, time.Time{}, 100), []history.Event{
		event(2, src, "addrs", history.KindStatus, "failed", "ok", ""),
		event(1, src, "addrs", history.KindStatus, "ok", "failed", "boom"),
	})
}

func TestDomainIPChanges(t *testing.T) {
	t.Parallel()

	notFound := result(probe.StatusUnavailable, "domain not found: gone.example.com")
	store := &fakeStore{
		public:  nil,
		private: nil,
		domains: []*domainip.Snapshot{
			domains(0, lookup("home.example.com", ok(), "203.0.113.7", "2001:db8::7")),
			domains(1,
				lookup("gone.example.com", ok(), "198.51.100.1"),
				lookup("home.example.com", ok(), "198.51.100.9", "2001:db8::7")),
			domains(2,
				lookup("gone.example.com", notFound),
				lookup("home.example.com", ok(), "198.51.100.9", "2001:db8::7")),
		},
		err: nil,
	}

	src := history.SourceDomainIP

	wantEvents(t, changes(t, store, time.Time{}, 100), []history.Event{
		event(2, src, "gone.example.com", history.KindStatus, "ok", "unavailable",
			"domain not found: gone.example.com"),
		event(1, src, "home.example.com", history.KindAdded, "", "198.51.100.9", ""),
		event(1, src, "home.example.com", history.KindRemoved, "203.0.113.7", "", ""),
	})
}

func TestChangesMergesSourcesAndLimits(t *testing.T) {
	t.Parallel()

	store := &fakeStore{
		public: []*publicnet.Snapshot{
			public(0, ok(), "203.0.113.7", ok()),
			public(3, ok(), "198.51.100.9", ok()),
		},
		private: []*privatenet.Snapshot{
			private(0, nil, ok(), "192.168.178.1"),
			private(2, nil, ok(), "192.168.178.254"),
			private(3, nil, ok(), "192.168.178.1"),
		},
		domains: nil,
		err:     nil,
	}

	all := []history.Event{
		event(3, history.SourcePrivateNet, "gateway_ipv4", history.KindChanged, "192.168.178.254", "192.168.178.1", ""),
		event(3, history.SourcePublicNet, "ipv4", history.KindChanged, "203.0.113.7", "198.51.100.9", ""),
		event(2, history.SourcePrivateNet, "gateway_ipv4", history.KindChanged, "192.168.178.1", "192.168.178.254", ""),
	}

	wantEvents(t, changes(t, store, time.Time{}, 100), all)
	wantEvents(t, changes(t, store, time.Time{}, 2), all[:2])
}

func TestChangesWithoutSnapshots(t *testing.T) {
	t.Parallel()

	wantEvents(t, changes(t, &fakeStore{public: nil, private: nil, domains: nil, err: nil}, time.Time{}, 10), nil)
}

func TestChangesErrors(t *testing.T) {
	t.Parallel()

	if svc, err := history.New(nil); svc != nil || err == nil {
		t.Errorf("New(nil) = %v, %v, want an error", svc, err)
	}

	svc, err := history.New(&fakeStore{public: nil, private: nil, domains: nil, err: errors.New("database down")})
	if err != nil {
		t.Fatal(err)
	}

	if _, err = svc.Changes(t.Context(), time.Time{}, 0); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("Changes(limit 0) error = %v, want a limit error", err)
	}

	if _, err = svc.Changes(t.Context(), time.Time{}, 10); err == nil ||
		!strings.Contains(err.Error(), "read public net snapshots: database down") {
		t.Errorf("Changes() error = %v, want the store error", err)
	}
}
