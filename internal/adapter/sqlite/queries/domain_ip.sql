-- name: InsertDomainIPSnapshot :exec
INSERT INTO domain_ip_snapshots (id, taken_at, resolver)
VALUES (?, ?, ?);

-- name: InsertDomainIPLookup :exec
INSERT INTO domain_ip_lookups (snapshot_id, domain, status, message, started_at, duration_ns)
VALUES (?, ?, ?, ?, ?, ?);

-- name: InsertDomainIPAddr :exec
INSERT INTO domain_ip_addrs (snapshot_id, domain, addr)
VALUES (?, ?, ?);

-- name: ListDomainIPSnapshots :many
SELECT id, taken_at, resolver
FROM domain_ip_snapshots
WHERE taken_at >= sqlc.arg(from_ms) AND taken_at < sqlc.arg(to_ms)
ORDER BY taken_at DESC, id DESC
LIMIT sqlc.arg(max_rows);

-- name: ListDomainIPLookups :many
SELECT snapshot_id, domain, status, message, started_at, duration_ns
FROM domain_ip_lookups
WHERE snapshot_id IN (sqlc.slice(snapshot_ids))
ORDER BY snapshot_id, domain;

-- name: ListDomainIPAddrs :many
SELECT snapshot_id, domain, addr
FROM domain_ip_addrs
WHERE snapshot_id IN (sqlc.slice(snapshot_ids));

-- name: DeleteDomainIPSnapshotsBefore :execrows
DELETE FROM domain_ip_snapshots
WHERE taken_at < ?;
