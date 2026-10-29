# Deployment

## systemd

[`deploy/systemd/netward.service`](../deploy/systemd/netward.service) runs
netward as an unprivileged, sandboxed user. It reads the configuration from
`/etc/netward/netward.yaml` and keeps the database and certificate in
`/var/lib/netward`.

Build netward (see [Setup](setup.md)), then install and start it:

```sh
sudo install -m 0755 netward /usr/local/bin/netward
sudo install -d /etc/netward
sudo install -m 0644 netward.example.yaml /etc/netward/netward.yaml
sudo install -m 0644 deploy/systemd/netward.service /etc/systemd/system/netward.service
sudo systemctl daemon-reload
sudo systemctl enable --now netward
```

Follow the logs with `journalctl -u netward -f`. After changing the
configuration, run `sudo systemctl restart netward`.

To send privileged pings instead, set `ping.privileged: true` and uncomment
the `CAP_NET_RAW` lines in the unit.

## Updating

On a machine that runs netward from a git checkout, run
[`deploy/redeploy.sh`](../deploy/redeploy.sh) as your normal user. It pulls,
builds, installs the binary, restarts the service and prints the log if
netward does not come back up. It needs Go and asks for sudo where needed;
`--no-pull` builds the checkout as it is.

- Database migrations run automatically on start.
- Settings missing from the configuration keep their defaults.
- The script never changes your configuration or the installed unit. It warns
  when the unit in the repository differs, so you can review and copy it.
- If netward refuses to start because of an unknown setting, compare your
  configuration with `netward.example.yaml`.
