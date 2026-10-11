package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/devusSs/netward/internal/adapter/sqlite"
	"github.com/devusSs/netward/internal/domain/domainip"
	"github.com/devusSs/netward/internal/domain/probe"
)

func newDomainIPSnapshot(t *testing.T) *domainip.Snapshot {
	t.Helper()

	snap, err := domainip.NewSnapshot(snapshotTime(), "1.1.1.1:53", []domainip.LookupProbe{
		{
			Result: okProbeResult(time.Millisecond),
			Domain: "home.example.com",
			Addrs:  []netip.Addr{netip.MustParseAddr("203.0.113.7"), netip.MustParseAddr("2001:db8::7")},
		},
		{
			Result: probe.NewResult(snapshotTime().Add(2*time.Millisecond), 3*time.Millisecond,
				errors.New("domain not found: gone.example.com"), errors.New("not matched")),
			Domain: "gone.example.com",
			Addrs:  nil,
		},
	})
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}

	return snap
}

func wantDomainIPRowCounts(t *testing.T, store *sqlite.Store, snapshots, lookups, addrs int) {
	t.Helper()

	got := []int{
		countRows(t, store, "domain_ip_snapshots"),
		countRows(t, store, "domain_ip_lookups"),
		countRows(t, store, "domain_ip_addrs"),
	}

	if want := []int{snapshots, lookups, addrs}; !slices.Equal(got, want) {
		t.Errorf("rows (snapshots, lookups, addrs) = %v, want %v", got, want)
	}
}

func TestSaveDomainIPSnapshot(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)
	snap := newDomainIPSnapshot(t)

	if err := store.SaveDomainIPSnapshot(t.Context(), snap); err != nil {
		t.Fatalf("SaveDomainIPSnapshot() error = %v", err)
	}

	wantDomainIPRowCounts(t, store, 1, 2, 2)

	var (
		id       []byte
		takenAt  int64
		resolver string
	)

	err := store.DB().QueryRowContext(t.Context(),
		`SELECT id, taken_at, resolver FROM domain_ip_snapshots`).Scan(&id, &takenAt, &resolver)
	if err != nil {
		t.Fatalf("select snapshot: %v", err)
	}

	if string(id) != string(snap.ID[:]) || takenAt != snapshotTime().UnixMilli() || resolver != "1.1.1.1:53" {
		t.Errorf("snapshot row = %x, %d, %q, want the snapshot's values", id, takenAt, resolver)
	}
}

type lookupRow struct {
	status     string
	message    string
	startedAt  sql.NullInt64
	durationNs int64
}

func TestSaveDomainIPSnapshotLookups(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)
	snap := newDomainIPSnapshot(t)

	if err := store.SaveDomainIPSnapshot(t.Context(), snap); err != nil {
		t.Fatalf("SaveDomainIPSnapshot() error = %v", err)
	}

	want := map[string]lookupRow{
		"home.example.com": {
			status:     "ok",
			message:    "",
			startedAt:  sql.NullInt64{Int64: snapshotTime().UnixMilli() + 1, Valid: true},
			durationNs: (150 * time.Millisecond).Nanoseconds(),
		},
		"gone.example.com": {
			status:     "failed",
			message:    "domain not found: gone.example.com",
			startedAt:  sql.NullInt64{Int64: snapshotTime().UnixMilli() + 2, Valid: true},
			durationNs: (3 * time.Millisecond).Nanoseconds(),
		},
	}

	for domain, wantRow := range want {
		var got lookupRow

		err := store.DB().QueryRowContext(t.Context(), `
			SELECT status, message, started_at, duration_ns
			FROM domain_ip_lookups WHERE snapshot_id = ? AND domain = ?`,
			snap.ID[:], domain,
		).Scan(&got.status, &got.message, &got.startedAt, &got.durationNs)
		if err != nil {
			t.Fatalf("select lookup %s: %v", domain, err)
		}

		if got != wantRow {
			t.Errorf("lookup %s row = %+v, want %+v", domain, got, wantRow)
		}
	}

	rows, err := store.DB().QueryContext(t.Context(),
		`SELECT domain || ' ' || addr FROM domain_ip_addrs WHERE snapshot_id = ? ORDER BY addr`, snap.ID[:])
	if err != nil {
		t.Fatalf("select addrs: %v", err)
	}
	defer rows.Close()

	var addrs []string

	for rows.Next() {
		var addr string
		if err = rows.Scan(&addr); err != nil {
			t.Fatalf("scan addr: %v", err)
		}

		addrs = append(addrs, addr)
	}

	if err = rows.Err(); err != nil {
		t.Fatalf("iterate addrs: %v", err)
	}

	if want := []string{"home.example.com 2001:db8::7", "home.example.com 203.0.113.7"}; !slices.Equal(addrs, want) {
		t.Errorf("addrs = %v, want %v", addrs, want)
	}
}

func TestSaveDomainIPSnapshotIsAtomic(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)

	// Snapshots from NewSnapshot are always valid, so break one by hand so the
	// last lookup fails a CHECK constraint.
	snap := newDomainIPSnapshot(t)
	snap.Lookups[1].Status = probe.StatusUnknown

	if err := store.SaveDomainIPSnapshot(t.Context(), snap); err == nil {
		t.Fatal("SaveDomainIPSnapshot() error = nil, want a constraint error")
	}

	wantDomainIPRowCounts(t, store, 0, 0, 0)
}

func TestSaveDomainIPSnapshotDuplicateID(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)
	snap := newDomainIPSnapshot(t)

	if err := store.SaveDomainIPSnapshot(t.Context(), snap); err != nil {
		t.Fatalf("first SaveDomainIPSnapshot() error = %v", err)
	}

	if err := store.SaveDomainIPSnapshot(t.Context(), snap); err == nil {
		t.Error("second SaveDomainIPSnapshot() error = nil, want a duplicate key error")
	}

	wantDomainIPRowCounts(t, store, 1, 2, 2)
}

func TestSaveDomainIPSnapshotContextCanceled(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := store.SaveDomainIPSnapshot(ctx, newDomainIPSnapshot(t)); !errors.Is(err, context.Canceled) {
		t.Errorf("SaveDomainIPSnapshot() error = %v, want %v", err, context.Canceled)
	}

	wantDomainIPRowCounts(t, store, 0, 0, 0)
}

func TestDeletingDomainIPSnapshotCascades(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)

	if err := store.SaveDomainIPSnapshot(t.Context(), newDomainIPSnapshot(t)); err != nil {
		t.Fatalf("SaveDomainIPSnapshot() error = %v", err)
	}

	if _, err := store.DB().ExecContext(t.Context(), `DELETE FROM domain_ip_snapshots`); err != nil {
		t.Fatalf("delete snapshots: %v", err)
	}

	wantDomainIPRowCounts(t, store, 0, 0, 0)
}
