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

// DomainIPSnapshots returns up to limit snapshots taken at or after from and
// before to, newest first. Limit must be between 1 and [MaxSnapshots].
func (s *Store) DomainIPSnapshots(
	ctx context.Context,
	from, to time.Time,
	limit int,
) ([]*domainip.Snapshot, error) {
	params, err := newRangeParams(from, to, limit)
	if err != nil {
		return nil, err
	}

	var snaps []*domainip.Snapshot

	err = s.read(ctx, func(queries *sqlcgen.Queries) error {
		snaps, err = readDomainIPSnapshots(ctx, queries, params)

		return err
	})
	if err != nil {
		return nil, fmt.Errorf("read domain ip snapshots: %w", err)
	}

	return snaps, nil
}

func readDomainIPSnapshots(
	ctx context.Context,
	queries *sqlcgen.Queries,
	params rangeParams,
) ([]*domainip.Snapshot, error) {
	rows, err := queries.ListDomainIPSnapshots(ctx, sqlcgen.ListDomainIPSnapshotsParams{
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

	snaps := make([]*domainip.Snapshot, 0, len(rows))
	byID := make(map[uuid.UUID]*domainip.Snapshot, len(rows))
	ids := make([][]byte, 0, len(rows))

	for _, row := range rows {
		id, idErr := parseID(row.ID)
		if idErr != nil {
			return nil, idErr
		}

		snap := new(domainip.Snapshot)
		snap.ID = id
		snap.Resolver = row.Resolver
		snap.Timestamp = parseTime(row.TakenAt)

		snaps = append(snaps, snap)
		byID[id] = snap
		ids = append(ids, row.ID)
	}

	// Lookups come sorted by domain, which is the order of a snapshot's
	// lookups.
	lookupRows, err := queries.ListDomainIPLookups(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list lookups: %w", err)
	}

	for _, row := range lookupRows {
		err = assignLookup(byID, row)
		if err != nil {
			return nil, err
		}
	}

	addrRows, err := queries.ListDomainIPAddrs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list addresses: %w", err)
	}

	for _, row := range addrRows {
		err = assignLookupAddr(byID, row)
		if err != nil {
			return nil, err
		}
	}

	for _, snap := range snaps {
		for i := range snap.Lookups {
			slices.SortFunc(snap.Lookups[i].Addrs, netip.Addr.Compare)
		}
	}

	return snaps, nil
}

func assignLookup(byID map[uuid.UUID]*domainip.Snapshot, row sqlcgen.DomainIpLookup) error {
	snap, err := snapshotOf(byID, row.SnapshotID)
	if err != nil {
		return err
	}

	result, err := parseResult(row.Status, row.Message, row.StartedAt, row.DurationNs)
	if err != nil {
		return fmt.Errorf("snapshot %s: lookup of %s: %w", snap.ID, row.Domain, err)
	}

	snap.Lookups = append(snap.Lookups, domainip.LookupProbe{Result: result, Domain: row.Domain, Addrs: nil})

	return nil
}

func assignLookupAddr(byID map[uuid.UUID]*domainip.Snapshot, row sqlcgen.DomainIpAddr) error {
	snap, err := snapshotOf(byID, row.SnapshotID)
	if err != nil {
		return err
	}

	addr, err := netip.ParseAddr(row.Addr)
	if err != nil {
		return fmt.Errorf("snapshot %s: address of %s: %w", snap.ID, row.Domain, err)
	}

	i := slices.IndexFunc(snap.Lookups, func(l domainip.LookupProbe) bool { return l.Domain == row.Domain })
	if i < 0 {
		return fmt.Errorf("snapshot %s: address of unknown lookup %s", snap.ID, row.Domain)
	}

	snap.Lookups[i].Addrs = append(snap.Lookups[i].Addrs, addr)

	return nil
}
