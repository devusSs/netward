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
