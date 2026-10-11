package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

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

func ipProbeParams(id []byte, family string, probe publicnet.IPAddrProbe) sqlcgen.InsertPublicNetIPProbeParams {
	return sqlcgen.InsertPublicNetIPProbeParams{
		SnapshotID: id,
		Family:     family,
		Status:     probe.Status.String(),
		Message:    probe.Message,
		StartedAt:  nullTime(probe.StartedAt),
		DurationNs: probe.Duration.Nanoseconds(),
		Source:     probe.Source,
		Addr:       nullAddr(probe.Addr),
	}
}

func connProbeParams(id []byte, family string, probe publicnet.ConnProbe) sqlcgen.InsertPublicNetConnProbeParams {
	method := sql.NullString{String: "", Valid: false}
	if probe.Method != publicnet.MethodUnknown {
		method = sql.NullString{String: probe.Method.String(), Valid: true}
	}

	return sqlcgen.InsertPublicNetConnProbeParams{
		SnapshotID:      id,
		Family:          family,
		Status:          probe.Status.String(),
		Message:         probe.Message,
		StartedAt:       nullTime(probe.StartedAt),
		DurationNs:      probe.Duration.Nanoseconds(),
		Method:          method,
		Target:          nullAddr(probe.Target),
		PacketsSent:     int64(probe.Ping.PacketsSent),
		PacketsReceived: int64(probe.Ping.PacketsReceived),
		RttMinNs:        probe.Ping.RoundTripMin.Nanoseconds(),
		RttMaxNs:        probe.Ping.RoundTripMax.Nanoseconds(),
		RttAvgNs:        probe.Ping.RoundTripAvg.Nanoseconds(),
		JitterNs:        probe.Ping.Jitter.Nanoseconds(),
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
