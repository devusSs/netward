-- Timestamps are Unix milliseconds, durations are nanoseconds.
-- started_at is NULL and target/addr/method are NULL when a probe did not run.

-- +goose Up
CREATE TABLE public_net_snapshots (
    id       BLOB    NOT NULL PRIMARY KEY CHECK (length(id) = 16),
    taken_at INTEGER NOT NULL
) STRICT;

CREATE INDEX public_net_snapshots_taken_at_idx ON public_net_snapshots (taken_at);

CREATE TABLE public_net_ip_probes (
    snapshot_id BLOB    NOT NULL REFERENCES public_net_snapshots (id) ON DELETE CASCADE,
    family      TEXT    NOT NULL CHECK (family IN ('ipv4', 'ipv6')),
    status      TEXT    NOT NULL CHECK (status IN ('ok', 'unavailable', 'failed', 'disabled')),
    message     TEXT    NOT NULL,
    started_at  INTEGER,
    duration_ns INTEGER NOT NULL CHECK (duration_ns >= 0),
    source      TEXT    NOT NULL,
    addr        TEXT,
    PRIMARY KEY (snapshot_id, family)
) STRICT;

CREATE TABLE public_net_conn_probes (
    snapshot_id      BLOB    NOT NULL REFERENCES public_net_snapshots (id) ON DELETE CASCADE,
    family           TEXT    NOT NULL CHECK (family IN ('ipv4', 'ipv6')),
    status           TEXT    NOT NULL CHECK (status IN ('ok', 'unavailable', 'failed', 'disabled')),
    message          TEXT    NOT NULL,
    started_at       INTEGER,
    duration_ns      INTEGER NOT NULL CHECK (duration_ns >= 0),
    method           TEXT    CHECK (method IN ('icmp', 'tcp', 'http')),
    target           TEXT,
    packets_sent     INTEGER NOT NULL CHECK (packets_sent >= 0),
    packets_received INTEGER NOT NULL CHECK (packets_received BETWEEN 0 AND packets_sent),
    rtt_min_ns       INTEGER NOT NULL,
    rtt_max_ns       INTEGER NOT NULL,
    rtt_avg_ns       INTEGER NOT NULL,
    jitter_ns        INTEGER NOT NULL,
    PRIMARY KEY (snapshot_id, family)
) STRICT;

-- +goose Down
DROP TABLE public_net_conn_probes;
DROP TABLE public_net_ip_probes;
DROP TABLE public_net_snapshots;
