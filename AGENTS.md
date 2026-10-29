# AGENTS.md

Guidance for coding agents working on netward. Read [README.md](./README.md) for what netward does from a user's point of view; this file covers how it is built and how to change it.

## Project

netward ("network warden") is a small home-network monitor written in Go. It runs on one host, for example a Raspberry Pi, and periodically:

- looks up the host's public IPv4 and IPv6 addresses (ipify by default) and pings an internet target over each family (`publicnet`),
- reads the private interface addresses (RFC 1918 and IPv6 ULA) and pings the default gateways (`privatenet`),
- optionally resolves configured domains to their addresses (`domainip`).

Every check produces a snapshot that is stored in SQLite. A web interface (server-rendered HTML with HTMX, served over HTTPS with a self-signed certificate) and a JSON API show the latest snapshots and the changes between consecutive snapshots.

It is a hobby project: keep solutions simple and readable rather than enterprise-grade, but correct, tested and lint-clean.

## Workflow with the maintainer

- **Do not commit or push.** The maintainer commits themselves. When a task is done, end with `git add` groups (files that belong in one commit) and a commit message for each group.
- Commit messages use a conventional prefix and a lowercase, past-tense description, for example `feat: added resolver adapter to resolve domains via the system resolver or a custom DNS server`, `fix: …`, `docs: …`, `refactor: …`, `build: …`, `chore: …`.
- Each group should build, test and lint on its own. Order the groups so that they can be committed one after another.
- A feature is finished only with documentation (Go doc comments, README when user-visible), tests and zero golangci-lint issues.
- Do not leave throwaway programs (for example `cmd/tmp*`) or generated binaries behind.
- When something is a design decision with real trade-offs, explain the options and recommend one instead of silently picking.

## Commands

```sh
make                 # sqlc generate + golangci-lint + cross-compile into bin/ (default target)
make sqlcgen         # regenerate internal/adapter/sqlite/sqlcgen
make lint            # sqlcgen, then golangci-lint run ./...
make install-tools   # install/update sqlc, goose and golangci-lint with go install
make clean           # remove bin/
go test ./...        # run the tests (add -race when touching concurrency)
go vet ./...
go build -o netward ./cmd/netward && ./netward   # run locally, reads ./netward.yaml if present
```

- Go version: see `go.mod` (Go 1.27). Builds use `CGO_ENABLED=0`; never add a dependency that needs cgo.
- sqlc is a system tool, not a `go tool` dependency. Do not add tool directives to `go.mod`.
- goose is only needed to create migration files; netward applies its embedded migrations itself on start.
- Running netward locally creates `netward.db*`, `netward-cert.pem` and `netward-key.pem` in the working directory. They are gitignored, like `netward.yaml` and `bin/`.

## Architecture

```
cmd/netward/            entry point: flags, config loading, logger, signal handling
internal/app/           wires everything from the config and schedules the jobs
internal/config/        YAML configuration: Config, Default, Load, Parse, Validate
internal/domain/        pure types and validation, no I/O
  probe/                shared probe Status, Result and PingStats
  publicnet/ privatenet/ domainip/   snapshots, probes and repository interfaces
internal/adapter/       all I/O with the outside world
  iplookup/ ping/ netif/ gateway/ resolver/   network probes
  sqlite/               storage (goose migrations, sqlc queries, generated sqlcgen/)
  tlscert/              loads or generates the self-signed certificate
internal/service/       orchestration and logging per feature
  publicnet/ privatenet/ domainip/   TakeSnapshot: run probes, build snapshot, save it
  history/              derives change events from consecutive snapshots
  probelog/             shared probe logging helpers
internal/web/           HTTP server, HTML templates, static assets, JSON API
deploy/                 example systemd unit (systemd/) and redeploy.sh to update an installed netward
```

Dependency direction: `cmd` → `app` → `service`, `web`, `adapter`, `config` → `domain`. Domain packages import only the standard library and `domain/probe`. Services define small interfaces for what they need (for example `domainip.Resolver`) and receive implementations through a `Config` struct; they must not construct adapters themselves. Only `internal/app` knows the concrete types.

### Key concepts

- **Probes and statuses.** Every check result embeds `probe.Result{Status, Message, StartedAt, Duration}`. Statuses are `Unknown`, `OK`, `Unavailable`, `Failed` and `Disabled`. `probe.NewResult` maps errors: an adapter's `ErrUnavailable`/`ErrNotFound` (the thing does not exist, for example no IPv6) becomes `Unavailable`, any other error becomes `Failed` and its message is stored. A check that fails must still produce a snapshot.
- **Snapshots** are created with the domain package's `NewSnapshot`, which validates and normalizes (sorting, deduplication, filtering) and assigns a UUIDv7 ID. Snapshots are immutable records of one check.
- **Repository interfaces** live in the domain packages and use feature-specific method names (`SavePublicNetSnapshot`, `SavePrivateNetSnapshot`, `SaveDomainIPSnapshot`, never a bare `Save`), so that one storage type can implement all of them.
- **History** is computed on read by comparing consecutive snapshots within the requested range; there is no events table. The range is capped at 7 days (`web.maxRange`), retention defaults to 30 days.
- **Scheduling.** `app.Run` runs each enabled feature with `runEvery` (immediately, then on a ticker) in an `errgroup`, plus an hourly retention cleanup and the web server. A failing job is logged, not fatal; a failing web server stops netward. Shutdown cancels the context; partial results are not saved.

### Adding a feature

Follow the existing features as templates (`domainip` is the most recent and smallest):

1. `internal/domain/<feature>`: probe and snapshot types, `NewSnapshot`, `Validate`, `SnapshotRepository` with a distinct method name.
2. `internal/adapter/<name>` for new I/O, with sentinel errors that map to probe statuses.
3. `internal/adapter/sqlite`: a new goose migration, queries in `queries/<feature>.sql`, `make sqlcgen`, then save/read methods on `Store` and retention cleanup in `DeleteSnapshotsBefore`.
4. `internal/service/<feature>`: `New(Config)`, `TakeSnapshot(ctx)`, Info logs for detected or changed values, Debug logs for details.
5. `internal/config`: a section with `Enabled`/`Interval`, defaults in `Default()`, checks in `Validate()`, and the same defaults documented in `netward.example.yaml` (a test asserts the example equals `Default()`).
6. `internal/app`: construct and schedule it; setup errors name the YAML key (`domainip.resolver: …`).
7. `internal/service/history` and `internal/web`: change events, overview card and `/api/<feature>/latest`.
8. README: feature list and any new setup steps.

## Code standards

The linter configuration in `.golangci.yml` is strict and authoritative; do not loosen it. If a rule is genuinely wrong for a line, use a specific `//nolint:<linter> // reason` (an explanation is required). Notable rules and conventions:

- **Modern Go.** Use current idioms: `errors.AsType`, `wg.Go`, `for range n`, `strings.SplitSeq`/`Lines`, `t.Context()`, `slog.DiscardHandler`, `new(value)`, `min`/`max`, `slices`/`maps`, `encoding/json/v2` (`omitzero`, `json:",inline"`; durations need an explicit format), the stdlib `uuid` package (`uuid.NewV7`). Prefer the standard library over new dependencies.
- **Package docs** go at the top of the file named after the package (`domainip.go`), not in `doc.go`. Every exported identifier has a doc comment; comments are full sentences ending in a period; refer to other identifiers with doc links like [time.Now] or [domain.Snapshot] (stdlib links are required by godoclint).
- **Structs are initialized completely** (exhaustruct). For an intentionally zero value write `var x T` instead of `T{}`.
- **Errors** are wrapped with context when they cross a package boundary (`fmt.Errorf("open database: %w", err)`, wrapcheck), compared with `errors.Is`/`errors.AsType`, and exported sentinels are named `ErrX`. No named results, no naked returns.
- **Logging** uses an injected `*slog.Logger` (nil means discard), never the global logger, and the context variants (`InfoContext`) where a context exists. Attributes use `slog.String`/`slog.Any` etc. Info logs are for things a user cares about (addresses detected or changed, snapshot taken); details are Debug.
- **No globals or `init`.** Embedded assets (`//go:embed`) are the exception.
- **No magic numbers** (mnd): name constants. Lines are at most 120 characters (golines); imports are grouped with the module's own packages last (goimports `local-prefixes`).
- **Time is injectable**: services and the web server take a `Now func() time.Time` for tests.
- **Interfaces are small and defined by the consumer**; constructors return concrete types (ireturn).

## Testing

- Tests use external test packages (`package foo_test`, testpackage linter). To test unexported code, expose it in `export_test.go` or write a `*_internal_test.go` file.
- Every test and subtest calls `t.Parallel()` (paralleltest) unless it cannot, and uses `t.Context()` and `t.TempDir()`.
- Tests must not depend on the internet or the local network. Use fakes: `httptest` servers for iplookup, a fake DNS server built with `golang.org/x/net/dns/dnsmessage` for the resolver, fake interfaces/repositories for services, a temporary SQLite file for storage. Tests that need OS permissions (unprivileged ICMP to localhost) skip with a clear reason when the environment does not allow it.
- Table-driven tests are the norm. Cover error and edge cases (missing IPv6, invalid input, status mapping), not only the happy path.
- For storage, round-trip tests (save, read back, compare) are expected for every field.
- For web changes, also check the result visually (for example headless Chrome screenshots against a local run) and at both wide and narrow widths, in light and dark mode.

## Storage

- SQLite through `modernc.org/sqlite` (pure Go). The connection enables WAL, `foreign_keys`, `busy_timeout` and immediate transactions, with a single open connection.
- Tables are `STRICT` with `CHECK` constraints. Timestamps are stored as Unix milliseconds, durations as nanoseconds, IDs as 16-byte BLOBs, statuses and methods as their text form.
- **Never edit an existing migration**; deployed databases have already applied it. Add a new numbered file in `internal/adapter/sqlite/migrations` (`goose -dir internal/adapter/sqlite/migrations create <name> sql`), with `-- +goose Up` and `-- +goose Down` sections.
- Queries live in `internal/adapter/sqlite/queries`; the generated code in `sqlcgen/` must never be edited by hand. Run `make sqlcgen` and commit the regenerated files together with the query or migration change.
- Read methods return snapshots newest first and are bounded by `MaxSnapshots`.

## Web interface

- Server-rendered `html/template` pages; HTMX (vendored in `internal/web/static/htmx.min.js`) refreshes the overview fragment every 30 seconds, and `static/app.js` updates the relative times every second. Every page must also work without JavaScript.
- Nothing is loaded from other hosts: no CDNs, web fonts or analytics. All assets are embedded.
- The Content Security Policy is strict (`script-src 'self'`, no inline scripts or styles, no `eval`). HTMX runs with `allowEval: false` and `allowScriptTags: false`. Do not weaken this; put new behavior in `static/app.js` and new styles in `static/style.css` (with light and dark mode via CSS variables).
- Internal errors return a generic 500 and are logged; never expose error details or paths in responses.
- JSON API changes are user-facing: keep responses backward compatible and document new endpoints in the README.

## Security and privacy

- The private key is written with mode 0600; files are written atomically (temporary file in the same directory, then rename).
- Never commit runtime files (database, certificate, key, `netward.yaml`) or the maintainer's real public addresses, domains or ULA prefix into tests, docs or examples. Use documentation addresses (`192.0.2.0/24`, `2001:db8::/32`) for public ones, common private addresses (`192.168.178.0/24`, `fd00::/64`) where a private one is needed, and `example.com` for domains.
- Validate all configuration in `config.Validate` and all external input (HTTP responses, DNS answers, query parameters) at the boundary.

## Deployment

- `deploy/systemd/netward.service` runs netward with `DynamicUser`, its config in `/etc/netward/netward.yaml` and its state in `/var/lib/netward` (the working directory, so relative paths in the config land there), plus systemd sandboxing. Keep it working when netward starts needing new files, sockets or system calls, and update the README's systemd section with it.
- Pings are unprivileged by default (`net.ipv4.ping_group_range`); `ping.privileged: true` with `CAP_NET_RAW` is the alternative.
- On Linux the gateways are read from `/proc/net/route` and `/proc/net/ipv6_route`; on macOS and the BSDs from the routing table and `netstat -rn`.

## Ideas not yet implemented

Possible future work; discuss with the maintainer before starting any of it:

- CI (GitHub Actions running `make lint`, `go test -race ./...` and the build).
- A Dockerfile (needs ICMP permissions and host networking to be meaningful).
- An events table for change history beyond the 7-day window.
- A dynamic DNS check that compares the domain records with the current public addresses.
