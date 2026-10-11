-- Timestamps are Unix milliseconds, durations are nanoseconds.
-- resolver is "system" or the DNS server's address, for example 1.1.1.1:53.

-- +goose Up
CREATE TABLE domain_ip_snapshots (
    id       BLOB    NOT NULL PRIMARY KEY CHECK (length(id) = 16),
    taken_at INTEGER NOT NULL,
    resolver TEXT    NOT NULL CHECK (resolver <> '')
) STRICT;

CREATE INDEX domain_ip_snapshots_taken_at_idx ON domain_ip_snapshots (taken_at);

CREATE TABLE domain_ip_lookups (
    snapshot_id BLOB    NOT NULL REFERENCES domain_ip_snapshots (id) ON DELETE CASCADE,
    domain      TEXT    NOT NULL CHECK (domain <> ''),
    status      TEXT    NOT NULL CHECK (status IN ('ok', 'unavailable', 'failed', 'disabled')),
    message     TEXT    NOT NULL,
    started_at  INTEGER,
    duration_ns INTEGER NOT NULL CHECK (duration_ns >= 0),
    PRIMARY KEY (snapshot_id, domain)
) STRICT;

CREATE TABLE domain_ip_addrs (
    snapshot_id BLOB NOT NULL,
    domain      TEXT NOT NULL,
    addr        TEXT NOT NULL,
    PRIMARY KEY (snapshot_id, domain, addr),
    FOREIGN KEY (snapshot_id, domain) REFERENCES domain_ip_lookups (snapshot_id, domain) ON DELETE CASCADE
) STRICT;

-- +goose Down
DROP TABLE domain_ip_addrs;
DROP TABLE domain_ip_lookups;
DROP TABLE domain_ip_snapshots;
