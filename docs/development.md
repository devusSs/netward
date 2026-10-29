# Development

```sh
make                 # sqlc generate, golangci-lint and cross-compile into bin/
make lint            # sqlc generate and golangci-lint
make install-tools   # install or update sqlc, goose and golangci-lint
go test -race ./...
```

`make` installs sqlc and golangci-lint with `go install` if they are missing.
Running netward from the repository creates `netward.db*` and the certificate
files in the working directory; they are gitignored.

## Layout

```
cmd/netward/        entry point
internal/app/       wires the features together and schedules the checks
internal/config/    YAML configuration
internal/domain/    snapshot types and validation, no I/O
internal/adapter/   network probes, SQLite storage, certificates
internal/service/   one service per check, plus the change history
internal/web/       web interface and JSON API
deploy/             systemd unit and redeploy script
```

[AGENTS.md](../AGENTS.md) describes the architecture and the code standards in
detail.
