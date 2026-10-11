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
	"github.com/devusSs/netward/internal/domain/privatenet"
	"github.com/devusSs/netward/internal/domain/probe"
)

func newPrivateSnapshot(t *testing.T) *privatenet.Snapshot {
	t.Helper()

	snap, err := privatenet.NewSnapshot(
		snapshotTime(),
		privatenet.AddrsProbe{
			Result: okProbeResult(time.Millisecond),
			Addrs: []privatenet.InterfaceAddr{
				{Interface: "en0", Prefix: netip.MustParsePrefix("192.168.178.23/24")},
				{Interface: "en0", Prefix: netip.MustParsePrefix("fd00::23/64")},
			},
		},
		privatenet.GatewayProbe{
			Result: okProbeResult(2 * time.Millisecond),
			Addr:   netip.MustParseAddr("192.168.178.1"),
			Ping: probe.PingStats{
				PacketsSent:     4,
				PacketsReceived: 4,
				RoundTripMin:    time.Millisecond,
				RoundTripMax:    3 * time.Millisecond,
				RoundTripAvg:    2 * time.Millisecond,
				Jitter:          time.Millisecond,
			},
		},
		privatenet.GatewayProbe{
			Status:    probe.StatusFailed,
			Message:   "no reply from gateway fe80::1%en0 to 4 packets",
			StartedAt: snapshotTime().Add(3 * time.Millisecond),
			Duration:  5 * time.Second,
			Addr:      netip.MustParseAddr("fe80::1%en0"),
			Ping: probe.PingStats{
				PacketsSent:     4,
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

func wantPrivateRowCounts(t *testing.T, store *sqlite.Store, snapshots, addrs, gateways int) {
	t.Helper()

	got := []int{
		countRows(t, store, "private_net_snapshots"),
		countRows(t, store, "private_net_addrs"),
		countRows(t, store, "private_net_gateway_probes"),
	}

	if want := []int{snapshots, addrs, gateways}; !slices.Equal(got, want) {
		t.Errorf("rows (snapshots, addrs, gateway probes) = %v, want %v", got, want)
	}
}

func TestSavePrivateNetSnapshot(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)
	snap := newPrivateSnapshot(t)

	if err := store.SavePrivateNetSnapshot(t.Context(), snap); err != nil {
		t.Fatalf("SavePrivateNetSnapshot() error = %v", err)
	}

	wantPrivateRowCounts(t, store, 1, 2, 2)

	var (
		id         []byte
		takenAt    int64
		status     string
		message    string
		startedAt  sql.NullInt64
		durationNs int64
	)

	err := store.DB().QueryRowContext(t.Context(), `
		SELECT id, taken_at, addrs_status, addrs_message, addrs_started_at, addrs_duration_ns
		FROM private_net_snapshots`,
	).Scan(&id, &takenAt, &status, &message, &startedAt, &durationNs)
	if err != nil {
		t.Fatalf("select snapshot: %v", err)
	}

	if string(id) != string(snap.ID[:]) || takenAt != snapshotTime().UnixMilli() || status != "ok" || message != "" ||
		startedAt != (sql.NullInt64{Int64: snapshotTime().UnixMilli() + 1, Valid: true}) ||
		durationNs != (150*time.Millisecond).Nanoseconds() {
		t.Errorf("snapshot row = %x, %d, %s, %q, %v, %d, want the snapshot's values",
			id, takenAt, status, message, startedAt, durationNs)
	}
}

func TestSavePrivateNetSnapshotAddrs(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)
	snap := newPrivateSnapshot(t)

	if err := store.SavePrivateNetSnapshot(t.Context(), snap); err != nil {
		t.Fatalf("SavePrivateNetSnapshot() error = %v", err)
	}

	rows, err := store.DB().QueryContext(t.Context(),
		`SELECT interface, prefix FROM private_net_addrs WHERE snapshot_id = ? ORDER BY prefix`, snap.ID[:])
	if err != nil {
		t.Fatalf("select addrs: %v", err)
	}
	defer rows.Close()

	var got []string

	for rows.Next() {
		var iface, prefix string
		if err = rows.Scan(&iface, &prefix); err != nil {
			t.Fatalf("scan addr: %v", err)
		}

		got = append(got, iface+" "+prefix)
	}

	if err = rows.Err(); err != nil {
		t.Fatalf("iterate addrs: %v", err)
	}

	if want := []string{"en0 192.168.178.23/24", "en0 fd00::23/64"}; !slices.Equal(got, want) {
		t.Errorf("addrs = %v, want %v", got, want)
	}
}

type gatewayRow struct {
	status          string
	message         string
	startedAt       sql.NullInt64
	durationNs      int64
	addr            sql.NullString
	packetsSent     int64
	packetsReceived int64
	rttMinNs        int64
	rttMaxNs        int64
	rttAvgNs        int64
	jitterNs        int64
}

func TestSavePrivateNetSnapshotGateways(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)
	snap := newPrivateSnapshot(t)

	if err := store.SavePrivateNetSnapshot(t.Context(), snap); err != nil {
		t.Fatalf("SavePrivateNetSnapshot() error = %v", err)
	}

	want := map[string]gatewayRow{
		"ipv4": {
			status:          "ok",
			message:         "",
			startedAt:       sql.NullInt64{Int64: snapshotTime().UnixMilli() + 2, Valid: true},
			durationNs:      (150 * time.Millisecond).Nanoseconds(),
			addr:            sql.NullString{String: "192.168.178.1", Valid: true},
			packetsSent:     4,
			packetsReceived: 4,
			rttMinNs:        time.Millisecond.Nanoseconds(),
			rttMaxNs:        (3 * time.Millisecond).Nanoseconds(),
			rttAvgNs:        (2 * time.Millisecond).Nanoseconds(),
			jitterNs:        time.Millisecond.Nanoseconds(),
		},
		"ipv6": {
			status:          "failed",
			message:         "no reply from gateway fe80::1%en0 to 4 packets",
			startedAt:       sql.NullInt64{Int64: snapshotTime().UnixMilli() + 3, Valid: true},
			durationNs:      (5 * time.Second).Nanoseconds(),
			addr:            sql.NullString{String: "fe80::1%en0", Valid: true},
			packetsSent:     4,
			packetsReceived: 0,
			rttMinNs:        0,
			rttMaxNs:        0,
			rttAvgNs:        0,
			jitterNs:        0,
		},
	}

	for family, wantRow := range want {
		var got gatewayRow

		err := store.DB().QueryRowContext(t.Context(), `
			SELECT status, message, started_at, duration_ns, addr,
			       packets_sent, packets_received, rtt_min_ns, rtt_max_ns, rtt_avg_ns, jitter_ns
			FROM private_net_gateway_probes WHERE snapshot_id = ? AND family = ?`,
			snap.ID[:], family,
		).Scan(
			&got.status, &got.message, &got.startedAt, &got.durationNs, &got.addr,
			&got.packetsSent, &got.packetsReceived, &got.rttMinNs, &got.rttMaxNs, &got.rttAvgNs, &got.jitterNs,
		)
		if err != nil {
			t.Fatalf("select %s gateway: %v", family, err)
		}

		if got != wantRow {
			t.Errorf("%s gateway row = %+v, want %+v", family, got, wantRow)
		}
	}
}

func TestSavePrivateNetSnapshotDisabledProbes(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)

	var noStats probe.PingStats

	disabled := privatenet.GatewayProbe{Result: probe.Disabled(), Addr: netip.Addr{}, Ping: noStats}

	snap, err := privatenet.NewSnapshot(
		snapshotTime(),
		privatenet.AddrsProbe{Result: probe.Disabled(), Addrs: nil},
		disabled,
		disabled,
	)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}

	if err = store.SavePrivateNetSnapshot(t.Context(), snap); err != nil {
		t.Fatalf("SavePrivateNetSnapshot() error = %v", err)
	}

	wantPrivateRowCounts(t, store, 1, 0, 2)

	var nulls int

	err = store.DB().QueryRowContext(t.Context(), `
		SELECT (SELECT count(*) FROM private_net_snapshots WHERE addrs_started_at IS NULL)
		     + (SELECT count(*) FROM private_net_gateway_probes WHERE started_at IS NULL AND addr IS NULL)`,
	).Scan(&nulls)
	if err != nil || nulls != 3 {
		t.Errorf("rows with NULL start and address = %d (%v), want 3", nulls, err)
	}
}

func TestSavePrivateNetSnapshotIsAtomic(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)

	// Snapshots from NewSnapshot are always valid, so break one by hand so the
	// last insert fails a CHECK constraint.
	snap := newPrivateSnapshot(t)
	snap.GatewayIPv6.Status = probe.StatusUnknown

	if err := store.SavePrivateNetSnapshot(t.Context(), snap); err == nil {
		t.Fatal("SavePrivateNetSnapshot() error = nil, want a constraint error")
	}

	wantPrivateRowCounts(t, store, 0, 0, 0)
}

func TestSavePrivateNetSnapshotDuplicateID(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)
	snap := newPrivateSnapshot(t)

	if err := store.SavePrivateNetSnapshot(t.Context(), snap); err != nil {
		t.Fatalf("first SavePrivateNetSnapshot() error = %v", err)
	}

	if err := store.SavePrivateNetSnapshot(t.Context(), snap); err == nil {
		t.Error("second SavePrivateNetSnapshot() error = nil, want a duplicate key error")
	}

	wantPrivateRowCounts(t, store, 1, 2, 2)
}

func TestSavePrivateNetSnapshotContextCanceled(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := store.SavePrivateNetSnapshot(ctx, newPrivateSnapshot(t)); !errors.Is(err, context.Canceled) {
		t.Errorf("SavePrivateNetSnapshot() error = %v, want %v", err, context.Canceled)
	}

	wantPrivateRowCounts(t, store, 0, 0, 0)
}

func TestDeletingPrivateSnapshotCascades(t *testing.T) {
	t.Parallel()

	store := openStore(t, dbPath(t), nil)

	if err := store.SavePrivateNetSnapshot(t.Context(), newPrivateSnapshot(t)); err != nil {
		t.Fatalf("SavePrivateNetSnapshot() error = %v", err)
	}

	if _, err := store.DB().ExecContext(t.Context(), `DELETE FROM private_net_snapshots`); err != nil {
		t.Fatalf("delete snapshots: %v", err)
	}

	wantPrivateRowCounts(t, store, 0, 0, 0)
}
