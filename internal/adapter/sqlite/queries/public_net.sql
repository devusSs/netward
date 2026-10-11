-- name: InsertPublicNetSnapshot :exec
INSERT INTO public_net_snapshots (id, taken_at)
VALUES (?, ?);

-- name: InsertPublicNetIPProbe :exec
INSERT INTO public_net_ip_probes (
    snapshot_id, family, status, message, started_at, duration_ns, source, addr
) VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertPublicNetConnProbe :exec
INSERT INTO public_net_conn_probes (
    snapshot_id, family, status, message, started_at, duration_ns, method, target,
    packets_sent, packets_received, rtt_min_ns, rtt_max_ns, rtt_avg_ns, jitter_ns
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListPublicNetSnapshots :many
SELECT id, taken_at
FROM public_net_snapshots
WHERE taken_at >= sqlc.arg(from_ms) AND taken_at < sqlc.arg(to_ms)
ORDER BY taken_at DESC, id DESC
LIMIT sqlc.arg(max_rows);

-- name: ListPublicNetIPProbes :many
SELECT snapshot_id, family, status, message, started_at, duration_ns, source, addr
FROM public_net_ip_probes
WHERE snapshot_id IN (sqlc.slice(snapshot_ids));

-- name: ListPublicNetConnProbes :many
SELECT snapshot_id, family, status, message, started_at, duration_ns, method, target,
       packets_sent, packets_received, rtt_min_ns, rtt_max_ns, rtt_avg_ns, jitter_ns
FROM public_net_conn_probes
WHERE snapshot_id IN (sqlc.slice(snapshot_ids));

-- name: DeletePublicNetSnapshotsBefore :execrows
DELETE FROM public_net_snapshots
WHERE taken_at < ?;
