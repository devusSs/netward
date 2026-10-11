package sqlite

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/devusSs/netward/internal/adapter/sqlite/sqlcgen"
	"github.com/devusSs/netward/internal/domain/domainip"
)

var _ domainip.SnapshotRepository = (*Store)(nil)

// SaveDomainIPSnapshot stores snap, its lookups and their addresses in one
// transaction.
func (s *Store) SaveDomainIPSnapshot(ctx context.Context, snap *domainip.Snapshot) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	// Rollback is a no-op once the transaction is committed.
	defer func() { _ = tx.Rollback() }()

	id := snap.ID[:]
	queries := s.queries.WithTx(tx)

	err = queries.InsertDomainIPSnapshot(ctx, sqlcgen.InsertDomainIPSnapshotParams{
		ID:       id,
		TakenAt:  snap.Timestamp.UnixMilli(),
		Resolver: snap.Resolver,
	})
	if err != nil {
		return fmt.Errorf("insert snapshot %s: %w", snap.ID, err)
	}

	for _, lookup := range snap.Lookups {
		err = saveLookup(ctx, queries, id, lookup)
		if err != nil {
			return fmt.Errorf("insert lookup of %s for snapshot %s: %w", lookup.Domain, snap.ID, err)
		}
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("commit snapshot %s: %w", snap.ID, err)
	}

	s.logger.DebugContext(ctx, "domain ip snapshot saved", slog.String("id", snap.ID.String()))

	return nil
}

func saveLookup(ctx context.Context, queries *sqlcgen.Queries, id []byte, lookup domainip.LookupProbe) error {
	err := queries.InsertDomainIPLookup(ctx, sqlcgen.InsertDomainIPLookupParams{
		SnapshotID: id,
		Domain:     lookup.Domain,
		Status:     lookup.Status.String(),
		Message:    lookup.Message,
		StartedAt:  nullTime(lookup.StartedAt),
		DurationNs: lookup.Duration.Nanoseconds(),
	})
	if err != nil {
		return fmt.Errorf("insert lookup: %w", err)
	}

	for _, addr := range lookup.Addrs {
		err = queries.InsertDomainIPAddr(ctx, sqlcgen.InsertDomainIPAddrParams{
			SnapshotID: id,
			Domain:     lookup.Domain,
			Addr:       addr.String(),
		})
		if err != nil {
			return fmt.Errorf("insert address %s: %w", addr, err)
		}
	}

	return nil
}
