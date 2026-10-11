package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/netip"
	"time"
	"uuid"

	"github.com/devusSs/netward/internal/adapter/sqlite/sqlcgen"
	"github.com/devusSs/netward/internal/domain/publicnet"
)

const (
	familyIPv4 = "ipv4"
	familyIPv6 = "ipv6"
)

var _ publicnet.SnapshotRepository = (*Store)(nil)

// SavePublicNetSnapshot stores snap and its probes in one transaction.
func (s *Store) SavePublicNetSnapshot(ctx context.Context, snap *publicnet.Snapshot) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	// Rollback is a no-op once the transaction is committed.
	defer func() { _ = tx.Rollback() }()

	id := snap.ID[:]
	queries := s.queries.WithTx(tx)

	err = queries.InsertPublicNetSnapshot(ctx, sqlcgen.InsertPublicNetSnapshotParams{
		ID:      id,
		TakenAt: snap.Timestamp.UnixMilli(),
	})
	if err != nil {
		return fmt.Errorf("insert snapshot %s: %w", snap.ID, err)
	}

	for family, probe := range map[string]publicnet.IPAddrProbe{familyIPv4: snap.IPv4, familyIPv6: snap.IPv6} {
		err = queries.InsertPublicNetIPProbe(ctx, ipProbeParams(id, family, probe))
		if err != nil {
			return fmt.Errorf("insert %s probe of snapshot %s: %w", family, snap.ID, err)
		}
	}

	for family, probe := range map[string]publicnet.ConnProbe{familyIPv4: snap.ConnIPv4, familyIPv6: snap.ConnIPv6} {
		err = queries.InsertPublicNetConnProbe(ctx, connProbeParams(id, family, probe))
		if err != nil {
			return fmt.Errorf("insert %s connectivity probe of snapshot %s: %w", family, snap.ID, err)
		}
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("commit snapshot %s: %w", snap.ID, err)
	}

	s.logger.DebugContext(ctx, "public net snapshot saved", slog.String("id", snap.ID.String()))

	return nil
}

func ipProbeParams(id []byte, family string, p publicnet.IPAddrProbe) sqlcgen.InsertPublicNetIPProbeParams {
	return sqlcgen.InsertPublicNetIPProbeParams{
		SnapshotID: id,
		Family:     family,
		Status:     p.Status.String(),
		Message:    p.Message,
		StartedAt:  nullTime(p.StartedAt),
		DurationNs: p.Duration.Nanoseconds(),
		Source:     p.Source,
		Addr:       nullAddr(p.Addr),
	}
}

func connProbeParams(id []byte, family string, p publicnet.ConnProbe) sqlcgen.InsertPublicNetConnProbeParams {
	method := sql.NullString{String: "", Valid: false}
	if p.Method != publicnet.MethodUnknown {
		method = sql.NullString{String: p.Method.String(), Valid: true}
	}

	return sqlcgen.InsertPublicNetConnProbeParams{
		SnapshotID:      id,
		Family:          family,
		Status:          p.Status.String(),
		Message:         p.Message,
		StartedAt:       nullTime(p.StartedAt),
		DurationNs:      p.Duration.Nanoseconds(),
		Method:          method,
		Target:          nullAddr(p.Target),
		PacketsSent:     int64(p.Ping.PacketsSent),
		PacketsReceived: int64(p.Ping.PacketsReceived),
		RttMinNs:        p.Ping.RoundTripMin.Nanoseconds(),
		RttMaxNs:        p.Ping.RoundTripMax.Nanoseconds(),
		RttAvgNs:        p.Ping.RoundTripAvg.Nanoseconds(),
		JitterNs:        p.Ping.Jitter.Nanoseconds(),
	}
}

func nullTime(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{Int64: 0, Valid: false}
	}

	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

func nullAddr(addr netip.Addr) sql.NullString {
	if !addr.IsValid() {
		return sql.NullString{String: "", Valid: false}
	}

	return sql.NullString{String: addr.String(), Valid: true}
}

// PublicNetSnapshots returns up to limit snapshots taken at or after from and
// before to, newest first. Limit must be between 1 and [MaxSnapshots].
func (s *Store) PublicNetSnapshots(
	ctx context.Context,
	from, to time.Time,
	limit int,
) ([]*publicnet.Snapshot, error) {
	params, err := newRangeParams(from, to, limit)
	if err != nil {
		return nil, err
	}

	var snaps []*publicnet.Snapshot

	err = s.read(ctx, func(queries *sqlcgen.Queries) error {
		snaps, err = readPublicNetSnapshots(ctx, queries, params)

		return err
	})
	if err != nil {
		return nil, fmt.Errorf("read public net snapshots: %w", err)
	}

	return snaps, nil
}

func readPublicNetSnapshots(
	ctx context.Context,
	queries *sqlcgen.Queries,
	params rangeParams,
) ([]*publicnet.Snapshot, error) {
	rows, err := queries.ListPublicNetSnapshots(ctx, sqlcgen.ListPublicNetSnapshotsParams{
		FromMs:  params.fromMs,
		ToMs:    params.toMs,
		MaxRows: params.maxRows,
	})
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}

	if len(rows) == 0 {
		return nil, nil
	}

	snaps := make([]*publicnet.Snapshot, 0, len(rows))
	byID := make(map[uuid.UUID]*publicnet.Snapshot, len(rows))
	ids := make([][]byte, 0, len(rows))

	for _, row := range rows {
		id, idErr := parseID(row.ID)
		if idErr != nil {
			return nil, idErr
		}

		snap := new(publicnet.Snapshot)
		snap.ID = id
		snap.Timestamp = parseTime(row.TakenAt)

		snaps = append(snaps, snap)
		byID[id] = snap
		ids = append(ids, row.ID)
	}

	ipRows, err := queries.ListPublicNetIPProbes(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list address probes: %w", err)
	}

	for _, row := range ipRows {
		err = assignIPProbe(byID, row)
		if err != nil {
			return nil, err
		}
	}

	connRows, err := queries.ListPublicNetConnProbes(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list connectivity probes: %w", err)
	}

	for _, row := range connRows {
		err = assignConnProbe(byID, row)
		if err != nil {
			return nil, err
		}
	}

	return snaps, nil
}

func assignIPProbe(byID map[uuid.UUID]*publicnet.Snapshot, row sqlcgen.PublicNetIpProbe) error {
	snap, err := snapshotOf(byID, row.SnapshotID)
	if err != nil {
		return err
	}

	result, err := parseResult(row.Status, row.Message, row.StartedAt, row.DurationNs)
	if err != nil {
		return fmt.Errorf("snapshot %s: %s probe: %w", snap.ID, row.Family, err)
	}

	addr, err := parseNullAddr(row.Addr)
	if err != nil {
		return fmt.Errorf("snapshot %s: %s probe: %w", snap.ID, row.Family, err)
	}

	p := publicnet.IPAddrProbe{Result: result, Source: row.Source, Addr: addr}

	switch row.Family {
	case familyIPv4:
		snap.IPv4 = p
	case familyIPv6:
		snap.IPv6 = p
	default:
		return fmt.Errorf("snapshot %s: unknown family %q", snap.ID, row.Family)
	}

	return nil
}

func assignConnProbe(byID map[uuid.UUID]*publicnet.Snapshot, row sqlcgen.PublicNetConnProbe) error {
	snap, err := snapshotOf(byID, row.SnapshotID)
	if err != nil {
		return err
	}

	result, err := parseResult(row.Status, row.Message, row.StartedAt, row.DurationNs)
	if err != nil {
		return fmt.Errorf("snapshot %s: %s connectivity probe: %w", snap.ID, row.Family, err)
	}

	target, err := parseNullAddr(row.Target)
	if err != nil {
		return fmt.Errorf("snapshot %s: %s connectivity probe: %w", snap.ID, row.Family, err)
	}

	method := publicnet.MethodUnknown
	if row.Method.Valid {
		method, err = publicnet.ParseConnMethod(row.Method.String)
		if err != nil {
			return fmt.Errorf("snapshot %s: %s connectivity probe: %w", snap.ID, row.Family, err)
		}
	}

	p := publicnet.ConnProbe{
		Result: result,
		Method: method,
		Target: target,
		Ping: pingStats(row.PacketsSent, row.PacketsReceived,
			row.RttMinNs, row.RttMaxNs, row.RttAvgNs, row.JitterNs),
	}

	switch row.Family {
	case familyIPv4:
		snap.ConnIPv4 = p
	case familyIPv6:
		snap.ConnIPv6 = p
	default:
		return fmt.Errorf("snapshot %s: unknown family %q", snap.ID, row.Family)
	}

	return nil
}

// snapshotOf returns the snapshot a probe row belongs to.
func snapshotOf[S any](byID map[uuid.UUID]*S, raw []byte) (*S, error) {
	id, err := parseID(raw)
	if err != nil {
		return nil, err
	}

	snap, ok := byID[id]
	if !ok {
		return nil, fmt.Errorf("probe of unknown snapshot %s", id)
	}

	return snap, nil
}
