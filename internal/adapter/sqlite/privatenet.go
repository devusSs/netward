package sqlite

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"time"
	"uuid"

	"github.com/devusSs/netward/internal/adapter/sqlite/sqlcgen"
	"github.com/devusSs/netward/internal/domain/privatenet"
)

var _ privatenet.SnapshotRepository = (*Store)(nil)

// SavePrivateNetSnapshot stores snap, its addresses and its gateway probes in
// one transaction.
func (s *Store) SavePrivateNetSnapshot(ctx context.Context, snap *privatenet.Snapshot) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	// Rollback is a no-op once the transaction is committed.
	defer func() { _ = tx.Rollback() }()

	id := snap.ID[:]
	queries := s.queries.WithTx(tx)

	err = queries.InsertPrivateNetSnapshot(ctx, sqlcgen.InsertPrivateNetSnapshotParams{
		ID:              id,
		TakenAt:         snap.Timestamp.UnixMilli(),
		AddrsStatus:     snap.Addrs.Status.String(),
		AddrsMessage:    snap.Addrs.Message,
		AddrsStartedAt:  nullTime(snap.Addrs.StartedAt),
		AddrsDurationNs: snap.Addrs.Duration.Nanoseconds(),
	})
	if err != nil {
		return fmt.Errorf("insert snapshot %s: %w", snap.ID, err)
	}

	for _, addr := range snap.Addrs.Addrs {
		err = queries.InsertPrivateNetAddr(ctx, sqlcgen.InsertPrivateNetAddrParams{
			SnapshotID: id,
			Interface:  addr.Interface,
			Prefix:     addr.Prefix.String(),
		})
		if err != nil {
			return fmt.Errorf("insert address %s of snapshot %s: %w", addr, snap.ID, err)
		}
	}

	gateways := map[string]privatenet.GatewayProbe{familyIPv4: snap.GatewayIPv4, familyIPv6: snap.GatewayIPv6}
	for family, p := range gateways {
		err = queries.InsertPrivateNetGatewayProbe(ctx, gatewayProbeParams(id, family, p))
		if err != nil {
			return fmt.Errorf("insert %s gateway probe of snapshot %s: %w", family, snap.ID, err)
		}
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("commit snapshot %s: %w", snap.ID, err)
	}

	s.logger.DebugContext(ctx, "private net snapshot saved", slog.String("id", snap.ID.String()))

	return nil
}

func gatewayProbeParams(
	id []byte,
	family string,
	p privatenet.GatewayProbe,
) sqlcgen.InsertPrivateNetGatewayProbeParams {
	return sqlcgen.InsertPrivateNetGatewayProbeParams{
		SnapshotID:      id,
		Family:          family,
		Status:          p.Status.String(),
		Message:         p.Message,
		StartedAt:       nullTime(p.StartedAt),
		DurationNs:      p.Duration.Nanoseconds(),
		Addr:            nullAddr(p.Addr),
		PacketsSent:     int64(p.Ping.PacketsSent),
		PacketsReceived: int64(p.Ping.PacketsReceived),
		RttMinNs:        p.Ping.RoundTripMin.Nanoseconds(),
		RttMaxNs:        p.Ping.RoundTripMax.Nanoseconds(),
		RttAvgNs:        p.Ping.RoundTripAvg.Nanoseconds(),
		JitterNs:        p.Ping.Jitter.Nanoseconds(),
	}
}

// PrivateNetSnapshots returns up to limit snapshots taken at or after from and
// before to, newest first. Limit must be between 1 and [MaxSnapshots].
func (s *Store) PrivateNetSnapshots(
	ctx context.Context,
	from, to time.Time,
	limit int,
) ([]*privatenet.Snapshot, error) {
	params, err := newRangeParams(from, to, limit)
	if err != nil {
		return nil, err
	}

	var snaps []*privatenet.Snapshot

	err = s.read(ctx, func(queries *sqlcgen.Queries) error {
		snaps, err = readPrivateNetSnapshots(ctx, queries, params)

		return err
	})
	if err != nil {
		return nil, fmt.Errorf("read private net snapshots: %w", err)
	}

	return snaps, nil
}

func readPrivateNetSnapshots(
	ctx context.Context,
	queries *sqlcgen.Queries,
	params rangeParams,
) ([]*privatenet.Snapshot, error) {
	rows, err := queries.ListPrivateNetSnapshots(ctx, sqlcgen.ListPrivateNetSnapshotsParams{
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

	snaps := make([]*privatenet.Snapshot, 0, len(rows))
	byID := make(map[uuid.UUID]*privatenet.Snapshot, len(rows))
	ids := make([][]byte, 0, len(rows))

	for _, row := range rows {
		id, idErr := parseID(row.ID)
		if idErr != nil {
			return nil, idErr
		}

		result, resultErr := parseResult(row.AddrsStatus, row.AddrsMessage, row.AddrsStartedAt, row.AddrsDurationNs)
		if resultErr != nil {
			return nil, fmt.Errorf("snapshot %s: address probe: %w", id, resultErr)
		}

		snap := new(privatenet.Snapshot)
		snap.ID = id
		snap.Timestamp = parseTime(row.TakenAt)
		snap.Addrs.Result = result

		snaps = append(snaps, snap)
		byID[id] = snap
		ids = append(ids, row.ID)
	}

	addrRows, err := queries.ListPrivateNetAddrs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list addresses: %w", err)
	}

	for _, row := range addrRows {
		err = assignPrivateAddr(byID, row)
		if err != nil {
			return nil, err
		}
	}

	gatewayRows, err := queries.ListPrivateNetGatewayProbes(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list gateway probes: %w", err)
	}

	for _, row := range gatewayRows {
		err = assignGatewayProbe(byID, row)
		if err != nil {
			return nil, err
		}
	}

	// The database orders prefixes as text; snapshots order them as
	// addresses.
	for _, snap := range snaps {
		slices.SortFunc(snap.Addrs.Addrs, privatenet.CompareInterfaceAddrs)
	}

	return snaps, nil
}

func assignPrivateAddr(byID map[uuid.UUID]*privatenet.Snapshot, row sqlcgen.PrivateNetAddr) error {
	snap, err := snapshotOf(byID, row.SnapshotID)
	if err != nil {
		return err
	}

	prefix, err := netip.ParsePrefix(row.Prefix)
	if err != nil {
		return fmt.Errorf("snapshot %s: address of %s: %w", snap.ID, row.Interface, err)
	}

	snap.Addrs.Addrs = append(snap.Addrs.Addrs, privatenet.InterfaceAddr{Interface: row.Interface, Prefix: prefix})

	return nil
}

func assignGatewayProbe(byID map[uuid.UUID]*privatenet.Snapshot, row sqlcgen.PrivateNetGatewayProbe) error {
	snap, err := snapshotOf(byID, row.SnapshotID)
	if err != nil {
		return err
	}

	result, err := parseResult(row.Status, row.Message, row.StartedAt, row.DurationNs)
	if err != nil {
		return fmt.Errorf("snapshot %s: %s gateway probe: %w", snap.ID, row.Family, err)
	}

	addr, err := parseNullAddr(row.Addr)
	if err != nil {
		return fmt.Errorf("snapshot %s: %s gateway probe: %w", snap.ID, row.Family, err)
	}

	p := privatenet.GatewayProbe{
		Result: result,
		Addr:   addr,
		Ping: pingStats(row.PacketsSent, row.PacketsReceived,
			row.RttMinNs, row.RttMaxNs, row.RttAvgNs, row.JitterNs),
	}

	switch row.Family {
	case familyIPv4:
		snap.GatewayIPv4 = p
	case familyIPv6:
		snap.GatewayIPv6 = p
	default:
		return fmt.Errorf("snapshot %s: unknown family %q", snap.ID, row.Family)
	}

	return nil
}
