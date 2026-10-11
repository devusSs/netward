package sqlite_test

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devusSs/netward/internal/adapter/sqlite"
	"github.com/devusSs/netward/internal/domain/probe"
	"github.com/devusSs/netward/internal/domain/publicnet"
)

func farFuture() time.Time {
	return time.Date(9999, time.January, 1, 0, 0, 0, 0, time.UTC)
}

func TestReadRoundTrip(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)

	public := newSnapshot(t)
	private := newPrivateSnapshot(t)
	domains := newDomainIPSnapshot(t)

	if err := store.SavePublicNetSnapshot(t.Context(), public); err != nil {
		t.Fatal(err)
	}

	if err := store.SavePrivateNetSnapshot(t.Context(), private); err != nil {
		t.Fatal(err)
	}

	if err := store.SaveDomainIPSnapshot(t.Context(), domains); err != nil {
		t.Fatal(err)
	}

	gotPublic, err := store.PublicNetSnapshots(t.Context(), time.Time{}, farFuture(), 1)
	if err != nil || len(gotPublic) != 1 || !reflect.DeepEqual(gotPublic[0], public) {
		t.Errorf("PublicNetSnapshots() = %+v, %v\nwant %+v", gotPublic, err, public)
	}

	gotPrivate, err := store.PrivateNetSnapshots(t.Context(), time.Time{}, farFuture(), 1)
	if err != nil || len(gotPrivate) != 1 || !reflect.DeepEqual(gotPrivate[0], private) {
		t.Errorf("PrivateNetSnapshots() = %+v, %v\nwant %+v", gotPrivate, err, private)
	}

	gotDomains, err := store.DomainIPSnapshots(t.Context(), time.Time{}, farFuture(), 1)
	if err != nil || len(gotDomains) != 1 || !reflect.DeepEqual(gotDomains[0], domains) {
		t.Errorf("DomainIPSnapshots() = %+v, %v\nwant %+v", gotDomains, err, domains)
	}
}

func TestReadDisabledProbesRoundTrip(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)

	var noStats probe.PingStats

	snap, err := publicnet.NewSnapshot(
		snapshotTime(),
		publicnet.IPAddrProbe{Result: probe.Disabled(), Source: "", Addr: netip.Addr{}},
		publicnet.IPAddrProbe{Result: probe.Disabled(), Source: "", Addr: netip.Addr{}},
		publicnet.ConnProbe{
			Result: probe.Disabled(),
			Method: publicnet.MethodUnknown,
			Target: netip.Addr{},
			Ping:   noStats,
		},
		publicnet.ConnProbe{
			Result: probe.Disabled(),
			Method: publicnet.MethodUnknown,
			Target: netip.Addr{},
			Ping:   noStats,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err = store.SavePublicNetSnapshot(t.Context(), snap); err != nil {
		t.Fatal(err)
	}

	got, err := store.PublicNetSnapshots(t.Context(), time.Time{}, farFuture(), 1)
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], snap) {
		t.Errorf("PublicNetSnapshots() = %+v, %v\nwant %+v", got, err, snap)
	}
}

// saveSeries saves a snapshot of every feature at start, start+1m, ...,
// start+(n-1)m.
func saveSeries(t *testing.T, store *sqlite.Store, n int) []time.Time {
	t.Helper()

	times := make([]time.Time, 0, n)

	for i := range n {
		at := snapshotTime().Add(time.Duration(i) * time.Minute)
		times = append(times, at)

		public := newSnapshot(t)
		public.Timestamp = at

		private := newPrivateSnapshot(t)
		private.Timestamp = at

		domains := newDomainIPSnapshot(t)
		domains.Timestamp = at

		if err := store.SavePublicNetSnapshot(t.Context(), public); err != nil {
			t.Fatal(err)
		}

		if err := store.SavePrivateNetSnapshot(t.Context(), private); err != nil {
			t.Fatal(err)
		}

		if err := store.SaveDomainIPSnapshot(t.Context(), domains); err != nil {
			t.Fatal(err)
		}
	}

	return times
}

// readTimestamps reads the snapshots of every feature in the range and
// returns their timestamps by feature.
func readTimestamps(t *testing.T, store *sqlite.Store, from, to time.Time, limit int) map[string][]time.Time {
	t.Helper()

	got := map[string][]time.Time{"public": nil, "private": nil, "domains": nil}

	public, err := store.PublicNetSnapshots(t.Context(), from, to, limit)
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range public {
		got["public"] = append(got["public"], s.Timestamp)
	}

	private, err := store.PrivateNetSnapshots(t.Context(), from, to, limit)
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range private {
		got["private"] = append(got["private"], s.Timestamp)
	}

	domains, err := store.DomainIPSnapshots(t.Context(), from, to, limit)
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range domains {
		got["domains"] = append(got["domains"], s.Timestamp)
	}

	return got
}

func TestReadRangeAndLimit(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)
	times := saveSeries(t, store, 5)

	tests := []struct {
		name  string
		from  time.Time
		to    time.Time
		limit int
		want  []time.Time
	}{
		{"everything", time.Time{}, farFuture(), 10, []time.Time{times[4], times[3], times[2], times[1], times[0]}},
		{"from is inclusive, to exclusive", times[1], times[4], 10, []time.Time{times[3], times[2], times[1]}},
		{"limit keeps the newest", times[1], times[4], 2, []time.Time{times[3], times[2]}},
		{"latest", time.Time{}, farFuture(), 1, []time.Time{times[4]}},
		{"nothing in range", times[4].Add(time.Millisecond), farFuture(), 10, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := readTimestamps(t, store, tt.from, tt.to, tt.limit)

			for feature, timestamps := range got {
				if !reflect.DeepEqual(timestamps, tt.want) {
					t.Errorf("%s timestamps = %v, want %v", feature, timestamps, tt.want)
				}
			}
		})
	}
}

func TestReadRejectsInvalidLimit(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)

	for _, limit := range []int{0, -1, sqlite.MaxSnapshots + 1} {
		_, err := store.PublicNetSnapshots(t.Context(), time.Time{}, farFuture(), limit)
		if err == nil || !strings.Contains(err.Error(), "limit must be between 1 and 10000") {
			t.Errorf("PublicNetSnapshots(limit %d) error = %v, want a limit error", limit, err)
		}

		_, err = store.PrivateNetSnapshots(t.Context(), time.Time{}, farFuture(), limit)
		if err == nil {
			t.Errorf("PrivateNetSnapshots(limit %d) error = nil, want a limit error", limit)
		}

		_, err = store.DomainIPSnapshots(t.Context(), time.Time{}, farFuture(), limit)
		if err == nil {
			t.Errorf("DomainIPSnapshots(limit %d) error = nil, want a limit error", limit)
		}
	}
}

func TestReadReportsCorruptRows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		corrupt string
		read    func(t *testing.T, store *sqlite.Store) error
		wantMsg string
	}{
		{
			name:    "public address",
			corrupt: `UPDATE public_net_ip_probes SET addr = 'not-an-ip' WHERE family = 'ipv4'`,
			read: func(t *testing.T, store *sqlite.Store) error {
				t.Helper()
				_, err := store.PublicNetSnapshots(t.Context(), time.Time{}, farFuture(), 1)

				return err
			},
			wantMsg: "ipv4 probe: parse address",
		},
		{
			name:    "private prefix",
			corrupt: `UPDATE private_net_addrs SET prefix = 'nonsense' WHERE prefix LIKE '192.%'`,
			read: func(t *testing.T, store *sqlite.Store) error {
				t.Helper()
				_, err := store.PrivateNetSnapshots(t.Context(), time.Time{}, farFuture(), 1)

				return err
			},
			wantMsg: "address of en0",
		},
		{
			name:    "domain address",
			corrupt: `UPDATE domain_ip_addrs SET addr = '999.1.1.1' WHERE addr = '203.0.113.7'`,
			read: func(t *testing.T, store *sqlite.Store) error {
				t.Helper()
				_, err := store.DomainIPSnapshots(t.Context(), time.Time{}, farFuture(), 1)

				return err
			},
			wantMsg: "address of home.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := openStore(t, dbPath(t), nil)
			saveSeries(t, store, 1)

			if _, err := store.DB().ExecContext(t.Context(), tt.corrupt); err != nil {
				t.Fatal(err)
			}

			if err := tt.read(t, store); err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("read error = %v, want it to contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestDeleteSnapshotsBefore(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)
	times := saveSeries(t, store, 5)

	deleted, err := store.DeleteSnapshotsBefore(t.Context(), times[2])
	if err != nil {
		t.Fatalf("DeleteSnapshotsBefore() error = %v", err)
	}

	if deleted != 6 {
		t.Errorf("DeleteSnapshotsBefore() = %d, want 6 (two per feature)", deleted)
	}

	wantRowCounts(t, store, 3, 6)
	wantPrivateRowCounts(t, store, 3, 6, 6)
	wantDomainIPRowCounts(t, store, 3, 6, 6)

	deleted, err = store.DeleteSnapshotsBefore(t.Context(), times[0])
	if err != nil || deleted != 0 {
		t.Errorf("DeleteSnapshotsBefore(nothing older) = %d, %v, want 0", deleted, err)
	}
}

func TestReadAndDeleteAfterClose(t *testing.T) {
	t.Parallel()

	store, err := sqlite.Open(t.Context(), dbPath(t), nil)
	if err != nil {
		t.Fatal(err)
	}

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err = store.PublicNetSnapshots(t.Context(), time.Time{}, farFuture(), 1); err == nil {
		t.Error("PublicNetSnapshots() after Close error = nil")
	}

	if _, err = store.DeleteSnapshotsBefore(t.Context(), farFuture()); err == nil {
		t.Error("DeleteSnapshotsBefore() after Close error = nil")
	}
}
