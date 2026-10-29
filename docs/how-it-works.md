# How it works

netward runs on one host and checks the network from that host's point of
view. Every check stores a snapshot in SQLite. A failed check is stored too,
with the reason, so outages show up in the history.

## Checks

By default, netward checks every 15 seconds:

- **Public IPv4 and IPv6 addresses**, looked up through
  [ipify.org](https://ipify.org) or endpoints of your choice.
- **Internet connectivity and latency**, by pinging `1.1.1.1` and
  `2606:4700:4700::1111`.
- **Private addresses** of the host's interfaces: the private IPv4 ranges
  (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`) and IPv6 unique local
  addresses (`fc00::/7`).
- **Default gateways** (IPv4 and IPv6), read from the routing table and pinged.

Optionally, every 30 seconds:

- **Domains**, resolved to their IPv4 and IPv6 addresses through the system
  resolver or a DNS server of your choice, for example to check that a dynamic
  DNS name follows your public IP address.

A missing family, such as no IPv6, shows as `unavailable` rather than
`failed`.

## Web interface

- **Overview** shows the latest state of every check on one screen and
  refreshes every 5 seconds. Every address and domain has a copy button.
- **History** lists what changed in the last hour, 24 hours or 7 days: new
  addresses, gateways that stopped answering, domains that point elsewhere.

The interface follows the system's light or dark mode, works without
JavaScript and loads nothing from other hosts. The same data is available as
JSON, see [API](api.md).

## Storage

Changes are not stored separately; netward compares consecutive snapshots when
the history is requested. Snapshots are kept for 30 days by default
(`retention`). Every check is stored, so longer intervals or a shorter
retention keep the database smaller.
