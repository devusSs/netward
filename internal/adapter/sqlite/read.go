package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/netip"
	"time"
	"uuid"

	"github.com/devusSs/netward/internal/adapter/sqlite/sqlcgen"
	"github.com/devusSs/netward/internal/domain/probe"
)

// MaxSnapshots is the most snapshots a single read returns.
const MaxSnapshots = 10_000

// rangeParams holds the bounds of a snapshot read in the stored units.
type rangeParams struct {
	fromMs  int64
	toMs    int64
	maxRows int64
}

// newRangeParams checks the arguments of a snapshot read: snapshots taken at
// or after from and before to, at most limit of them.
func newRangeParams(from, to time.Time, limit int) (rangeParams, error) {
	if limit < 1 || limit > MaxSnapshots {
		return rangeParams{}, fmt.Errorf("limit must be between 1 and %d, got %d", MaxSnapshots, limit)
	}

	return rangeParams{fromMs: from.UnixMilli(), toMs: to.UnixMilli(), maxRows: int64(limit)}, nil
}

// read runs fn in a transaction, so a concurrent cleanup cannot remove rows
// between the queries of one read.
func (s *Store) read(ctx context.Context, fn func(queries *sqlcgen.Queries) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	// Rollback ends the read-only transaction; there is nothing to commit.
	defer func() { _ = tx.Rollback() }()

	return fn(s.queries.WithTx(tx))
}

func parseID(raw []byte) (uuid.UUID, error) {
	var id uuid.UUID
	if len(raw) != len(id) {
		return uuid.UUID{}, fmt.Errorf("invalid snapshot id of %d bytes", len(raw))
	}

	copy(id[:], raw)

	return id, nil
}

func parseTime(ms int64) time.Time {
	return time.UnixMilli(ms).UTC()
}

func parseNullTime(ms sql.NullInt64) time.Time {
	if !ms.Valid {
		return time.Time{}
	}

	return parseTime(ms.Int64)
}

func parseResult(status, message string, startedAt sql.NullInt64, durationNs int64) (probe.Result, error) {
	parsed, err := probe.ParseStatus(status)
	if err != nil {
		return probe.Result{}, fmt.Errorf("parse status: %w", err)
	}

	return probe.Result{
		Status:    parsed,
		Message:   message,
		StartedAt: parseNullTime(startedAt),
		Duration:  time.Duration(durationNs),
	}, nil
}

func parseNullAddr(addr sql.NullString) (netip.Addr, error) {
	if !addr.Valid {
		return netip.Addr{}, nil
	}

	parsed, err := netip.ParseAddr(addr.String)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("parse address: %w", err)
	}

	return parsed, nil
}

func pingStats(sent, received, minNs, maxNs, avgNs, jitterNs int64) probe.PingStats {
	return probe.PingStats{
		PacketsSent:     int(sent),
		PacketsReceived: int(received),
		RoundTripMin:    time.Duration(minNs),
		RoundTripMax:    time.Duration(maxNs),
		RoundTripAvg:    time.Duration(avgNs),
		Jitter:          time.Duration(jitterNs),
	}
}

// DeleteSnapshotsBefore removes the snapshots of all features taken before
// before, together with their probes, and returns how many snapshots it
// removed.
func (s *Store) DeleteSnapshotsBefore(ctx context.Context, before time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}

	// Rollback is a no-op once the transaction is committed.
	defer func() { _ = tx.Rollback() }()

	queries := s.queries.WithTx(tx)
	ms := before.UnixMilli()

	var total int64

	for name, deleteBefore := range map[string]func(context.Context, int64) (int64, error){
		"public net":  queries.DeletePublicNetSnapshotsBefore,
		"private net": queries.DeletePrivateNetSnapshotsBefore,
		"domain ip":   queries.DeleteDomainIPSnapshotsBefore,
	} {
		n, deleteErr := deleteBefore(ctx, ms)
		if deleteErr != nil {
			return 0, fmt.Errorf("delete %s snapshots: %w", name, deleteErr)
		}

		total += n
	}

	err = tx.Commit()
	if err != nil {
		return 0, fmt.Errorf("commit deletion: %w", err)
	}

	return total, nil
}
