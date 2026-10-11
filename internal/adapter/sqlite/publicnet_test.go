package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/devusSs/netward/internal/adapter/sqlite"
	"github.com/devusSs/netward/internal/domain/probe"
	"github.com/devusSs/netward/internal/domain/publicnet"
)

func snapshotTime() time.Time {
	return time.Date(2026, time.October, 11, 12, 0, 0, 123_000_000, time.UTC)
}

func okProbeResult(offset time.Duration) probe.Result {
	return probe.Result{
		Status:    probe.StatusOK,
		Message:   "",
		StartedAt: snapshotTime().Add(offset),
		Duration:  150 * time.Millisecond,
	}
}

func disabledProbeResult() probe.Result {
	return probe.Result{
		Status:    probe.StatusDisabled,
		Message:   "",
		StartedAt: time.Time{},
		Duration:  0,
	}
}

func newSnapshot(t *testing.T) *publicnet.Snapshot {
	t.Helper()

	snap, err := publicnet.NewSnapshot(
		snapshotTime(),
		publicnet.IPAddrProbe{
			Result: okProbeResult(time.Millisecond),
			Source: "https://api.ipify.org",
			Addr:   netip.MustParseAddr("203.0.113.7"),
		},
		publicnet.IPAddrProbe{
			Status:    probe.StatusUnavailable,
			Message:   "ip family unavailable: ipv6: no route to host",
			StartedAt: snapshotTime().Add(2 * time.Millisecond),
			Duration:  3 * time.Millisecond,
			Source:    "https://api6.ipify.org",
			Addr:      netip.Addr{},
		},
		publicnet.ConnProbe{
			Result: okProbeResult(3 * time.Millisecond),
			Method: publicnet.MethodICMP,
			Target: netip.MustParseAddr("1.1.1.1"),
			Ping: probe.PingStats{
				PacketsSent:     4,
				PacketsReceived: 3,
				RoundTripMin:    10 * time.Millisecond,
				RoundTripMax:    20 * time.Millisecond,
				RoundTripAvg:    15 * time.Millisecond,
				Jitter:          3 * time.Millisecond,
			},
		},
		publicnet.ConnProbe{
			Result: disabledProbeResult(),
			Method: publicnet.MethodUnknown,
			Target: netip.Addr{},
			Ping: probe.PingStats{
				PacketsSent:     0,
				PacketsReceived: 0,
				RoundTripMin:    0,
				RoundTripMax:    0,
				RoundTripAvg:    0,
				Jitter:          0,
			},
		},
	)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}

	return snap
}

func countRows(t *testing.T, store *sqlite.Store, table string) int {
	t.Helper()

	var n int

	err := store.DB().QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&n)
	if err != nil {
		t.Fatalf("count %s: %v", table, err)
	}

	return n
}

func wantRowCounts(t *testing.T, store *sqlite.Store, snapshots, probes int) {
	t.Helper()

	got := []int{
		countRows(t, store, "public_net_snapshots"),
		countRows(t, store, "public_net_ip_probes"),
		countRows(t, store, "public_net_conn_probes"),
	}

	if got[0] != snapshots || got[1] != probes || got[2] != probes {
		t.Errorf("rows (snapshots, ip probes, conn probes) = %v, want [%d %d %d]", got, snapshots, probes, probes)
	}
}

func TestSavePublicNetSnapshot(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)
	snap := newSnapshot(t)

	if err := store.SavePublicNetSnapshot(t.Context(), snap); err != nil {
		t.Fatalf("SavePublicNetSnapshot() error = %v", err)
	}

	wantRowCounts(t, store, 1, 2)

	var (
		id      []byte
		takenAt int64
	)

	err := store.DB().QueryRowContext(t.Context(), `SELECT id, taken_at FROM public_net_snapshots`).Scan(&id, &takenAt)
	if err != nil {
		t.Fatalf("select snapshot: %v", err)
	}

	if string(id) != string(snap.ID[:]) || takenAt != snapshotTime().UnixMilli() {
		t.Errorf("snapshot row = %x, %d, want %x, %d", id, takenAt, snap.ID[:], snapshotTime().UnixMilli())
	}
}

type ipProbeRow struct {
	status     string
	message    string
	startedAt  sql.NullInt64
	durationNs int64
	source     string
	addr       sql.NullString
}

func TestSavePublicNetSnapshotIPProbes(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)
	snap := newSnapshot(t)

	if err := store.SavePublicNetSnapshot(t.Context(), snap); err != nil {
		t.Fatalf("SavePublicNetSnapshot() error = %v", err)
	}

	want := map[string]ipProbeRow{
		"ipv4": {
			status:     "ok",
			message:    "",
			startedAt:  sql.NullInt64{Int64: snapshotTime().UnixMilli() + 1, Valid: true},
			durationNs: (150 * time.Millisecond).Nanoseconds(),
			source:     "https://api.ipify.org",
			addr:       sql.NullString{String: "203.0.113.7", Valid: true},
		},
		"ipv6": {
			status:     "unavailable",
			message:    "ip family unavailable: ipv6: no route to host",
			startedAt:  sql.NullInt64{Int64: snapshotTime().UnixMilli() + 2, Valid: true},
			durationNs: (3 * time.Millisecond).Nanoseconds(),
			source:     "https://api6.ipify.org",
			addr:       sql.NullString{String: "", Valid: false},
		},
	}

	for family, wantRow := range want {
		var got ipProbeRow

		err := store.DB().QueryRowContext(t.Context(), `
			SELECT status, message, started_at, duration_ns, source, addr
			FROM public_net_ip_probes WHERE snapshot_id = ? AND family = ?`,
			snap.ID[:], family,
		).Scan(&got.status, &got.message, &got.startedAt, &got.durationNs, &got.source, &got.addr)
		if err != nil {
			t.Fatalf("select %s probe: %v", family, err)
		}

		if got != wantRow {
			t.Errorf("%s probe row = %+v, want %+v", family, got, wantRow)
		}
	}
}

type connProbeRow struct {
	status          string
	message         string
	startedAt       sql.NullInt64
	durationNs      int64
	method          sql.NullString
	target          sql.NullString
	packetsSent     int64
	packetsReceived int64
	rttMinNs        int64
	rttMaxNs        int64
	rttAvgNs        int64
	jitterNs        int64
}

func TestSavePublicNetSnapshotConnProbes(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)
	snap := newSnapshot(t)

	if err := store.SavePublicNetSnapshot(t.Context(), snap); err != nil {
		t.Fatalf("SavePublicNetSnapshot() error = %v", err)
	}

	want := map[string]connProbeRow{
		"ipv4": {
			status:          "ok",
			message:         "",
			startedAt:       sql.NullInt64{Int64: snapshotTime().UnixMilli() + 3, Valid: true},
			durationNs:      (150 * time.Millisecond).Nanoseconds(),
			method:          sql.NullString{String: "icmp", Valid: true},
			target:          sql.NullString{String: "1.1.1.1", Valid: true},
			packetsSent:     4,
			packetsReceived: 3,
			rttMinNs:        (10 * time.Millisecond).Nanoseconds(),
			rttMaxNs:        (20 * time.Millisecond).Nanoseconds(),
			rttAvgNs:        (15 * time.Millisecond).Nanoseconds(),
			jitterNs:        (3 * time.Millisecond).Nanoseconds(),
		},
		"ipv6": {
			status:          "disabled",
			message:         "",
			startedAt:       sql.NullInt64{Int64: 0, Valid: false},
			durationNs:      0,
			method:          sql.NullString{String: "", Valid: false},
			target:          sql.NullString{String: "", Valid: false},
			packetsSent:     0,
			packetsReceived: 0,
			rttMinNs:        0,
			rttMaxNs:        0,
			rttAvgNs:        0,
			jitterNs:        0,
		},
	}

	for family, wantRow := range want {
		var got connProbeRow

		err := store.DB().QueryRowContext(t.Context(), `
			SELECT status, message, started_at, duration_ns, method, target,
			       packets_sent, packets_received, rtt_min_ns, rtt_max_ns, rtt_avg_ns, jitter_ns
			FROM public_net_conn_probes WHERE snapshot_id = ? AND family = ?`,
			snap.ID[:], family,
		).Scan(
			&got.status, &got.message, &got.startedAt, &got.durationNs, &got.method, &got.target,
			&got.packetsSent, &got.packetsReceived, &got.rttMinNs, &got.rttMaxNs, &got.rttAvgNs, &got.jitterNs,
		)
		if err != nil {
			t.Fatalf("select %s probe: %v", family, err)
		}

		if got != wantRow {
			t.Errorf("%s probe row = %+v, want %+v", family, got, wantRow)
		}
	}
}

func TestSavePublicNetSnapshotIsAtomic(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)

	// Snapshots from NewSnapshot are always valid, so build one by hand that
	// passes the first inserts and fails a CHECK constraint on a later one.
	snap := newSnapshot(t)
	snap.ConnIPv6.Status = probe.StatusUnknown

	if err := store.SavePublicNetSnapshot(t.Context(), snap); err == nil {
		t.Fatal("SavePublicNetSnapshot() error = nil, want a constraint error")
	}

	wantRowCounts(t, store, 0, 0)
}

func TestSavePublicNetSnapshotDuplicateID(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)
	snap := newSnapshot(t)

	if err := store.SavePublicNetSnapshot(t.Context(), snap); err != nil {
		t.Fatalf("first SavePublicNetSnapshot() error = %v", err)
	}

	if err := store.SavePublicNetSnapshot(t.Context(), snap); err == nil {
		t.Error("second SavePublicNetSnapshot() error = nil, want a duplicate key error")
	}

	wantRowCounts(t, store, 1, 2)
}

func TestSavePublicNetSnapshotMany(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)

	for range 10 {
		if err := store.SavePublicNetSnapshot(t.Context(), newSnapshot(t)); err != nil {
			t.Fatalf("SavePublicNetSnapshot() error = %v", err)
		}
	}

	wantRowCounts(t, store, 10, 20)
}

func TestSavePublicNetSnapshotContextCanceled(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := store.SavePublicNetSnapshot(ctx, newSnapshot(t)); !errors.Is(err, context.Canceled) {
		t.Errorf("SavePublicNetSnapshot() error = %v, want %v", err, context.Canceled)
	}

	wantRowCounts(t, store, 0, 0)
}

func TestDeletingSnapshotCascades(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)

	if err := store.SavePublicNetSnapshot(t.Context(), newSnapshot(t)); err != nil {
		t.Fatalf("SavePublicNetSnapshot() error = %v", err)
	}

	if _, err := store.DB().ExecContext(t.Context(), `DELETE FROM public_net_snapshots`); err != nil {
		t.Fatalf("delete snapshots: %v", err)
	}

	wantRowCounts(t, store, 0, 0)
}
