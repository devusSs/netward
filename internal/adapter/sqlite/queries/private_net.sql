-- name: InsertPrivateNetSnapshot :exec
INSERT INTO private_net_snapshots (
    id, taken_at, addrs_status, addrs_message, addrs_started_at, addrs_duration_ns
) VALUES (?, ?, ?, ?, ?, ?);

-- name: InsertPrivateNetAddr :exec
INSERT INTO private_net_addrs (snapshot_id, interface, prefix)
VALUES (?, ?, ?);

-- name: InsertPrivateNetGatewayProbe :exec
INSERT INTO private_net_gateway_probes (
    snapshot_id, family, status, message, started_at, duration_ns, addr,
    packets_sent, packets_received, rtt_min_ns, rtt_max_ns, rtt_avg_ns, jitter_ns
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListPrivateNetSnapshots :many
SELECT id, taken_at, addrs_status, addrs_message, addrs_started_at, addrs_duration_ns
FROM private_net_snapshots
WHERE taken_at >= sqlc.arg(from_ms) AND taken_at < sqlc.arg(to_ms)
ORDER BY taken_at DESC, id DESC
LIMIT sqlc.arg(max_rows);

-- name: ListPrivateNetAddrs :many
SELECT snapshot_id, interface, prefix
FROM private_net_addrs
WHERE snapshot_id IN (sqlc.slice(snapshot_ids))
ORDER BY snapshot_id, interface, prefix;

-- name: ListPrivateNetGatewayProbes :many
SELECT snapshot_id, family, status, message, started_at, duration_ns, addr,
       packets_sent, packets_received, rtt_min_ns, rtt_max_ns, rtt_avg_ns, jitter_ns
FROM private_net_gateway_probes
WHERE snapshot_id IN (sqlc.slice(snapshot_ids));

-- name: DeletePrivateNetSnapshotsBefore :execrows
DELETE FROM private_net_snapshots
WHERE taken_at < ?;
