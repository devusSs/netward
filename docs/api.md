# API

The web server also serves JSON under `/api`:

| Endpoint                      | Returns                                              |
| ----------------------------- | ---------------------------------------------------- |
| `GET /api/publicnet/latest`   | The latest public network check.                     |
| `GET /api/privatenet/latest`  | The latest private network check.                    |
| `GET /api/domainip/latest`    | The latest domain check.                             |
| `GET /api/changes`            | Recent changes, newest first.                        |
| `GET /api/health`             | `{"status":"ok"}` while netward runs.                |

`/api/changes` takes `since`, a duration such as `90m`, `24h` or `7d` (default
`24h`, at most `7d`), and `limit` (default 100, at most 1000):

```sh
curl -k "https://localhost:8443/api/changes?since=7d&limit=20"
```

Errors return a JSON object with an `error` field and the matching HTTP
status.
