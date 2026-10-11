# netward

_netward = network warden_

## Purpose

netward is a web-based tool that monitors your home network, covering both public and private interfaces, from the device it runs on.

## Features

### Default Configuration

By default, netward monitors:

- **Public IPv4 and IPv6 addresses**, if available. These are resolved from the host device's perspective using [ipify.org](https://ipify.org) by default.
- **Private IPv4 and IPv6 addresses**, if available. These are read directly from the host device's network interfaces and cover the private IPv4 ranges (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`) and IPv6 unique local addresses (`fc00::/7`). Public IPv6 addresses are covered by the public address lookup above.
- **Default gateways (IPv4 and IPv6) and their reachability**, if available. The gateways are detected from the host device's routing table on every check and pinged to measure latency.
- **Internet connectivity and latency**, measured by pinging a server from the host device (`1.1.1.1` by default).

### Optional Configuration

The following features can be enabled optionally:

- **Domain IP monitoring**, which resolves the configured domains to their IPv4 and IPv6 addresses and records when they change, for example to check that a dynamic DNS name points at your current public IP address. It uses the host device's system resolver by default or a DNS server of your choice, such as `1.1.1.1` or your local DNS server.

More features may be added in the future.

You can also configure custom endpoints for:

- Public IPv4 and IPv6 address lookups
- Connectivity and latency checks
- Domain lookups (a DNS server instead of the system resolver)

and limit the monitored network interfaces, for example to exclude Docker or VPN interfaces.

### Web Interface

netward serves a web interface at `https://<host>:8443` by default. It shows the current public, private and domain state and the recent changes, such as a new public IP address, a gateway that stopped answering or a domain that now points elsewhere. It refreshes itself every 30 seconds and loads nothing from other hosts.

On first start netward creates a self-signed certificate for HTTPS, like Pi-hole does, so your browser will warn about it once. You can also point netward at your own certificate.

The same data is available as JSON:

- `GET /api/publicnet/latest`, `/api/privatenet/latest` and `/api/domainip/latest` return the latest check of each feature.
- `GET /api/changes?since=24h&limit=100` returns the recent changes, newest first. `since` accepts durations such as `90m`, `24h` or `7d` (at most `7d`).
- `GET /api/health` reports whether netward is running.

Checks are kept for 30 days by default (`retention` in the configuration).

## Setup

netward needs Go 1.27 or later to build:

```sh
go build -o netward ./cmd/netward
```

Alternatively, `make` generates the database code with sqlc, runs golangci-lint and builds netward for Linux and macOS on amd64 and arm64 and for 32-bit ARM Linux into `bin/`. It installs sqlc and golangci-lint with `go install` if they are missing; `make install-tools` installs or updates sqlc, goose and golangci-lint.

Copy [`netward.example.yaml`](./netward.example.yaml) to `netward.yaml` and change what you need. Every setting has a default, so the file can be short or missing entirely.

Pings are sent without root privileges. On Linux this requires the `net.ipv4.ping_group_range` sysctl to include the group netward runs as, for example:

```sh
sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"
```

Alternatively, set `ping.privileged: true` and give netward the `CAP_NET_RAW` capability or run it as root.

## Usage

```sh
./netward                       # uses ./netward.yaml, or the defaults if it does not exist
./netward -config /etc/netward/netward.yaml
```

netward then checks the configured features right away and on their intervals, stores the results in its SQLite database (`netward.db` by default) and serves the web interface at `https://<host>:8443`. It creates `netward-cert.pem` and `netward-key.pem` for HTTPS on first start. Paths in the configuration are relative to the working directory.

Stop netward with Ctrl+C or `SIGTERM`. It cancels checks that are still running, without saving partial results, and shuts down cleanly.

### Running with systemd

[`deploy/systemd/netward.service`](./deploy/systemd/netward.service) is an example unit for Linux systems with systemd. It runs netward as an unprivileged, sandboxed user, reads the configuration from `/etc/netward/netward.yaml` and keeps the database and certificate in `/var/lib/netward`.

netward needs no cgo, so you can also build it for another machine, for example `GOOS=linux GOARCH=arm64 go build -o netward ./cmd/netward` for a 64-bit Raspberry Pi. Then install and start it:

```sh
sudo install -m 0755 netward /usr/local/bin/netward
sudo install -d /etc/netward
sudo install -m 0644 netward.example.yaml /etc/netward/netward.yaml
sudo install -m 0644 deploy/systemd/netward.service /etc/systemd/system/netward.service
sudo systemctl daemon-reload
sudo systemctl enable --now netward
```

Follow the logs with `journalctl -u netward -f`. After changing the configuration, run `sudo systemctl restart netward`.

## Disclaimer

netward is a hobby project. Its authors maintain it for their own home networks and for fun, and it is not intended for production use.

Expect bugs, missing features and unexpected behavior. Use it at your own risk.

## License

This project is licensed under the [MIT License](./LICENSE).
