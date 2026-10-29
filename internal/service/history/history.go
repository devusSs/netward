// Package history derives what changed on the host's network from the stored
// snapshots: public addresses and gateways that changed, private and domain
// addresses that were added or removed, and probes whose status changed, for
// example from ok to failed and back.
//
// Changes are computed when they are requested by comparing consecutive
// snapshots, so nothing besides the snapshots needs to be stored.
package history

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/devusSs/netward/internal/domain/domainip"
	"github.com/devusSs/netward/internal/domain/privatenet"
	"github.com/devusSs/netward/internal/domain/probe"
	"github.com/devusSs/netward/internal/domain/publicnet"
)

// batchSize is how many snapshots are read from the store at a time. It must
// not exceed what the store returns in one read (sqlite.MaxSnapshots).
const batchSize = 10_000

// Source is the feature an [Event] belongs to.
type Source string

const (
	SourcePublicNet  Source = "publicnet"
	SourcePrivateNet Source = "privatenet"
	SourceDomainIP   Source = "domainip"
)

// Kind is what happened in an [Event].
type Kind string

const (
	// KindChanged means a value was replaced: Old became New.
	KindChanged Kind = "changed"
	// KindAdded means New appeared.
	KindAdded Kind = "added"
	// KindRemoved means Old disappeared.
	KindRemoved Kind = "removed"
	// KindStatus means a probe's status changed from Old to New. Message
	// holds the new status's message, for example why a probe failed.
	KindStatus Kind = "status"
)

// Event is a single change between two consecutive snapshots.
type Event struct {
	// Time is when the snapshot that shows the change was taken.
	Time   time.Time
	Source Source
	// Subject is what changed: a probe such as "ipv4", "conn_ipv6" or
	// "gateway_ipv4", an interface name for private addresses or a domain.
	Subject string
	Kind    Kind
	Old     string
	New     string
	Message string
}

// Store reads snapshots, newest first. The SQLite store implements it.
type Store interface {
	PublicNetSnapshots(ctx context.Context, from, to time.Time, limit int) ([]*publicnet.Snapshot, error)
	PrivateNetSnapshots(ctx context.Context, from, to time.Time, limit int) ([]*privatenet.Snapshot, error)
	DomainIPSnapshots(ctx context.Context, from, to time.Time, limit int) ([]*domainip.Snapshot, error)
}

// Service computes changes. It is safe for concurrent use.
type Service struct {
	store Store
	batch int
}

// New returns a service that reads snapshots from store.
func New(store Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}

	return &Service{store: store, batch: batchSize}, nil
}

// Changes returns up to limit changes that happened at or after since, newest
// first.
func (s *Service) Changes(ctx context.Context, since time.Time, limit int) ([]Event, error) {
	if limit < 1 {
		return nil, fmt.Errorf("limit must be positive, got %d", limit)
	}

	public, err := series(ctx, since, s.batch, s.store.PublicNetSnapshots,
		func(snap *publicnet.Snapshot) time.Time { return snap.Timestamp })
	if err != nil {
		return nil, fmt.Errorf("read public net snapshots: %w", err)
	}

	private, err := series(ctx, since, s.batch, s.store.PrivateNetSnapshots,
		func(snap *privatenet.Snapshot) time.Time { return snap.Timestamp })
	if err != nil {
		return nil, fmt.Errorf("read private net snapshots: %w", err)
	}

	domains, err := series(ctx, since, s.batch, s.store.DomainIPSnapshots,
		func(snap *domainip.Snapshot) time.Time { return snap.Timestamp })
	if err != nil {
		return nil, fmt.Errorf("read domain ip snapshots: %w", err)
	}

	events := slices.Concat(publicNetChanges(public), privateNetChanges(private), domainIPChanges(domains))

	slices.SortStableFunc(events, func(a, b Event) int {
		return cmp.Or(
			b.Time.Compare(a.Time),
			strings.Compare(string(a.Source), string(b.Source)),
			strings.Compare(a.Subject, b.Subject),
			strings.Compare(string(a.Kind), string(b.Kind)),
		)
	})

	if len(events) > limit {
		events = events[:limit]
	}

	return events, nil
}

// series returns the snapshots taken at or after since in chronological
// order, preceded by the last snapshot before since, which serves as the
// baseline for the first change. It reads the snapshots in batches of batch,
// so a range is complete however many snapshots it holds.
func series[S any](
	ctx context.Context,
	since time.Time,
	batch int,
	list func(ctx context.Context, from, to time.Time, limit int) ([]*S, error),
	timestamp func(*S) time.Time,
) ([]*S, error) {
	var snaps []*S

	end := time.Date(9999, time.December, 31, 0, 0, 0, 0, time.UTC)

	for {
		page, err := list(ctx, since, end, batch)
		if err != nil {
			return nil, err
		}

		snaps = append(snaps, page...)

		if len(page) < batch {
			break
		}

		// The batch is full, so older snapshots may follow. The next batch
		// ends before the oldest one; a feature never takes two snapshots at
		// the same time, because its checks run one after another.
		end = timestamp(page[len(page)-1])
	}

	baseline, err := list(ctx, time.Time{}, since, 1)
	if err != nil {
		return nil, err
	}

	snaps = append(snaps, baseline...)
	slices.Reverse(snaps)

	return snaps, nil
}

// tracker remembers the last status of every subject and the last
// address(es) seen while it was ok.
type tracker struct {
	source   Source
	statuses map[string]probe.Status
	addrs    map[string]string
	sets     map[string][]string
	events   []Event
}

func newTracker(source Source) *tracker {
	return &tracker{
		source:   source,
		statuses: make(map[string]probe.Status),
		addrs:    make(map[string]string),
		sets:     make(map[string][]string),
		events:   nil,
	}
}

func (t *tracker) add(at time.Time, subject string, kind Kind, oldValue, newValue, message string) {
	t.events = append(t.events, Event{
		Time:    at,
		Source:  t.source,
		Subject: subject,
		Kind:    kind,
		Old:     oldValue,
		New:     newValue,
		Message: message,
	})
}

// status records the status of subject and reports a change from the last
// one.
func (t *tracker) status(at time.Time, subject string, result probe.Result) {
	last, seen := t.statuses[subject]
	t.statuses[subject] = result.Status

	if seen && last != result.Status {
		message := ""
		if result.Status != probe.StatusOK {
			message = result.Message
		}

		t.add(at, subject, KindStatus, last.String(), result.Status.String(), message)
	}
}

// addr records a single address of subject and reports when it differs from
// the last one. An invalid address is ignored.
func (t *tracker) addr(at time.Time, subject string, addr netip.Addr) {
	if !addr.IsValid() {
		return
	}

	last, seen := t.addrs[subject]
	t.addrs[subject] = addr.String()

	if seen && last != addr.String() {
		t.add(at, subject, KindChanged, last, addr.String(), "")
	}
}

// set records the addresses of subject and reports those added and removed
// since the last set. The subject of each event is given by label.
func (t *tracker) set(at time.Time, subject string, values []string, label func(value string) (string, string)) {
	last, seen := t.sets[subject]
	t.sets[subject] = values

	if !seen {
		return
	}

	for _, v := range values {
		if !slices.Contains(last, v) {
			eventSubject, shown := label(v)
			t.add(at, eventSubject, KindAdded, "", shown, "")
		}
	}

	for _, v := range last {
		if !slices.Contains(values, v) {
			eventSubject, shown := label(v)
			t.add(at, eventSubject, KindRemoved, shown, "", "")
		}
	}
}

func publicNetChanges(snaps []*publicnet.Snapshot) []Event {
	t := newTracker(SourcePublicNet)

	for _, snap := range snaps {
		at := snap.Timestamp

		for _, p := range []struct {
			subject string
			probe   publicnet.IPAddrProbe
		}{{"ipv4", snap.IPv4}, {"ipv6", snap.IPv6}} {
			t.status(at, p.subject, p.probe.Result)

			if p.probe.Status == probe.StatusOK {
				t.addr(at, p.subject, p.probe.Addr)
			}
		}

		t.status(at, "conn_ipv4", snap.ConnIPv4.Result)
		t.status(at, "conn_ipv6", snap.ConnIPv6.Result)
	}

	return t.events
}

func privateNetChanges(snaps []*privatenet.Snapshot) []Event {
	t := newTracker(SourcePrivateNet)

	for _, snap := range snaps {
		at := snap.Timestamp

		t.status(at, "addrs", snap.Addrs.Result)

		if snap.Addrs.Status == probe.StatusOK {
			values := make([]string, 0, len(snap.Addrs.Addrs))
			for _, a := range snap.Addrs.Addrs {
				values = append(values, a.String())
			}

			// Values are "interface prefix"; events name the interface.
			t.set(at, "addrs", values, func(v string) (string, string) {
				iface, prefix, _ := strings.Cut(v, " ")

				return iface, prefix
			})
		}

		for _, p := range []struct {
			subject string
			probe   privatenet.GatewayProbe
		}{{"gateway_ipv4", snap.GatewayIPv4}, {"gateway_ipv6", snap.GatewayIPv6}} {
			t.status(at, p.subject, p.probe.Result)
			t.addr(at, p.subject, p.probe.Addr)
		}
	}

	return t.events
}

func domainIPChanges(snaps []*domainip.Snapshot) []Event {
	t := newTracker(SourceDomainIP)

	for _, snap := range snaps {
		for _, lookup := range snap.Lookups {
			t.status(snap.Timestamp, lookup.Domain, lookup.Result)

			if lookup.Status != probe.StatusOK {
				continue
			}

			values := make([]string, 0, len(lookup.Addrs))
			for _, a := range lookup.Addrs {
				values = append(values, a.String())
			}

			t.set(snap.Timestamp, lookup.Domain, values, func(v string) (string, string) { return lookup.Domain, v })
		}
	}

	return t.events
}
