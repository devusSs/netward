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

## Setup

_Coming soon._

## Usage

_Coming soon._

## Disclaimer

netward is a hobby project. Its authors maintain it for their own home networks and for fun, and it is not intended for production use.

Expect bugs, missing features and unexpected behavior. Use it at your own risk.

## License

This project is licensed under the [MIT License](./LICENSE).
