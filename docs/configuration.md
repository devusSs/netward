# Configuration

netward reads a YAML file, `netward.yaml` in the working directory by default
or the file given with `-config`. Every setting has a default, so you only
need the ones you want to change. Unknown keys are rejected, so typos do not
go unnoticed.

[`netward.example.yaml`](../netward.example.yaml) lists every setting with its
default and a short explanation. Durations use Go syntax, for example `15s`,
`1m` or `1h30m`.

| Section      | What it configures                                                          |
| ------------ | --------------------------------------------------------------------------- |
| `database`   | Path of the SQLite file (`netward.db`).                                     |
| `retention`  | How long snapshots are kept (`720h`, 30 days; `0` keeps them forever).      |
| `log`        | Level (`debug`, `info`, `warn`, `error`) and format (`text`, `json`).       |
| `ping`       | Echo requests per check, their interval and timeout, privileged pings.      |
| `publicnet`  | Interval (`15s`), lookup endpoints and ping targets for the public checks.  |
| `privatenet` | Interval (`15s`), gateway pings and which interfaces are monitored.         |
| `domainip`   | Off by default. Interval (`30s`), DNS server, timeout and the domains.      |
| `http`       | Listen address (`:8443`), HTTPS and the certificate.                        |

A minimal configuration that also monitors a domain:

```yaml
domainip:
  enabled: true
  domains:
    - home.example.com
```

Restart netward after changing the configuration.
