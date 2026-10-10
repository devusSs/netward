# netward

_netward = network warden_

## Purpose

netward is a web-based tool that monitors your home network, covering both public and private interfaces, from the device it runs on.

## Features

### Default Configuration

By default, netward monitors:

- **Public IPv4 and IPv6 addresses**, if available. These are resolved from the host device's perspective using [ipify.org](https://ipify.org) by default.
- **Private IPv4 and IPv6 addresses**, if available. These are read directly from the host device.
- **Internet connectivity and latency**, measured by pinging a server from the host device (`1.1.1.1` by default).

### Optional Configuration

The following features can be enabled optionally:

- **Domain IP monitoring**, which uses the host device's system resolver by default.

More features may be added in the future.

You can also configure custom endpoints for:

- Public IPv4 and IPv6 address lookups
- Connectivity and latency checks

## Setup

_Coming soon._

## Usage

_Coming soon._

## Disclaimer

netward is a hobby project. Its authors maintain it for their own home networks and for fun, and it is not intended for production use.

Expect bugs, missing features and unexpected behavior. Use it at your own risk.

## License

This project is licensed under the [MIT License](./LICENSE).
