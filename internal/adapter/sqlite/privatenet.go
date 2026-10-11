package sqlite

import (
	"context"
	"fmt"
	"log/slog"

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
