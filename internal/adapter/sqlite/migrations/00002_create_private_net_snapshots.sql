-- Timestamps are Unix milliseconds, durations are nanoseconds.
-- started_at is NULL when a probe did not run. A gateway's addr is NULL when
-- no gateway was found and may carry a zone, for example fe80::1%en0.

-- +goose Up
CREATE TABLE private_net_snapshots (
    id                BLOB    NOT NULL PRIMARY KEY CHECK (length(id) = 16),
    taken_at          INTEGER NOT NULL,
    addrs_status      TEXT    NOT NULL CHECK (addrs_status IN ('ok', 'unavailable', 'failed', 'disabled')),
    addrs_message     TEXT    NOT NULL,
    addrs_started_at  INTEGER,
    addrs_duration_ns INTEGER NOT NULL CHECK (addrs_duration_ns >= 0)
) STRICT;

CREATE INDEX private_net_snapshots_taken_at_idx ON private_net_snapshots (taken_at);

CREATE TABLE private_net_addrs (
    snapshot_id BLOB NOT NULL REFERENCES private_net_snapshots (id) ON DELETE CASCADE,
    interface   TEXT NOT NULL CHECK (interface <> ''),
    prefix      TEXT NOT NULL,
    PRIMARY KEY (snapshot_id, interface, prefix)
) STRICT;

CREATE TABLE private_net_gateway_probes (
    snapshot_id      BLOB    NOT NULL REFERENCES private_net_snapshots (id) ON DELETE CASCADE,
    family           TEXT    NOT NULL CHECK (family IN ('ipv4', 'ipv6')),
    status           TEXT    NOT NULL CHECK (status IN ('ok', 'unavailable', 'failed', 'disabled')),
    message          TEXT    NOT NULL,
    started_at       INTEGER,
    duration_ns      INTEGER NOT NULL CHECK (duration_ns >= 0),
    addr             TEXT,
    packets_sent     INTEGER NOT NULL CHECK (packets_sent >= 0),
    packets_received INTEGER NOT NULL CHECK (packets_received BETWEEN 0 AND packets_sent),
    rtt_min_ns       INTEGER NOT NULL,
    rtt_max_ns       INTEGER NOT NULL,
    rtt_avg_ns       INTEGER NOT NULL,
    jitter_ns        INTEGER NOT NULL,
    PRIMARY KEY (snapshot_id, family)
) STRICT;

-- +goose Down
DROP TABLE private_net_gateway_probes;
DROP TABLE private_net_addrs;
DROP TABLE private_net_snapshots;
