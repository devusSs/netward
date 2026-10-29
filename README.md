# netward

netward ("network warden") watches your home network from the device it runs
on, for example a Raspberry Pi. It keeps track of your **public IPv4 and IPv6
addresses**, **internet connectivity**, **private addresses**, **default
gateways** and optionally **domains**, and shows what changed in a small web
interface.

## Quick start

```sh
go build -o netward ./cmd/netward
./netward
```

Then open `https://localhost:8443`. See [Setup](docs/setup.md) for the details.

## Documentation

- [Setup](docs/setup.md): building, configuring and running
- [How it works](docs/how-it-works.md): what is checked, how often, and the
  web interface
- [Configuration](docs/configuration.md): all settings
- [Deployment](docs/deployment.md): running netward with systemd and updating
  it
- [API](docs/api.md): the JSON API
- [Development](docs/development.md): building, testing and the project layout

## Disclaimer

netward is a hobby project, built for personal use and for fun. It is not
intended for production use. Expect bugs and missing features, and use it at
your own risk.

## License

This project is licensed under the [MIT License](LICENSE).
