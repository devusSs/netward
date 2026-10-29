# Setup

## Build

netward needs Go 1.27 or later and no cgo:

```sh
go build -o netward ./cmd/netward
```

To build for another machine, set `GOOS` and `GOARCH`, for example for a
64-bit Raspberry Pi:

```sh
GOOS=linux GOARCH=arm64 go build -o netward ./cmd/netward
```

Use `GOARCH=arm` for a 32-bit Raspberry Pi OS (`uname -m` prints `armv7l`).

## Configure

Copy [`netward.example.yaml`](../netward.example.yaml) to `netward.yaml` and
change what you need. Every setting has a default, so the file can be short or
missing entirely. See [Configuration](configuration.md).

## Allow pings

netward pings without root privileges. On Linux this needs the
`net.ipv4.ping_group_range` sysctl to include the group netward runs as:

```sh
sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"
```

systemd 243 and later already allow this for all groups. Alternatively, set
`ping.privileged: true` and give netward the `CAP_NET_RAW` capability or run it
as root.

## Run

```sh
./netward                                  # uses ./netward.yaml, or the defaults
./netward -config /etc/netward/netward.yaml
```

netward checks right away and then on its intervals, stores the results in
`netward.db` and serves the web interface at `https://<host>:8443`. On first
start it creates a self-signed certificate (`netward-cert.pem` and
`netward-key.pem`), so your browser warns about it once. Paths in the
configuration are relative to the working directory.

Stop netward with Ctrl+C or `SIGTERM`. Checks that are still running are
cancelled without saving partial results.

To run netward permanently, see [Deployment](deployment.md).
