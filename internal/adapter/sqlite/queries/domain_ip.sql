-- name: InsertDomainIPSnapshot :exec
INSERT INTO domain_ip_snapshots (id, taken_at, resolver)
VALUES (?, ?, ?);

-- name: InsertDomainIPLookup :exec
INSERT INTO domain_ip_lookups (snapshot_id, domain, status, message, started_at, duration_ns)
VALUES (?, ?, ?, ?, ?, ?);

-- name: InsertDomainIPAddr :exec
INSERT INTO domain_ip_addrs (snapshot_id, domain, addr)
VALUES (?, ?, ?);
