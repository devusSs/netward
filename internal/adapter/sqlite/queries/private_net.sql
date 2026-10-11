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
