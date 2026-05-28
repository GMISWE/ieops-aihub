# aihub

polyforge backend service — HTTP API + read-only Web UI for managing work
items, attempts, memories, and dependencies. Backed by PostgreSQL.

> New to polyforge? Start with [`docs/onboarding.md`](docs/onboarding.md) for
> the end-to-end install (API key → config.toml → Claude Code plugin → demo).

## Quick start

```bash
go build -buildvcs=false ./...
DATABASE_URL=postgres://… ./aihub
```

The server listens on `:8080` by default; override with `PORT`.

### First admin user

When the `users` table is empty, set `ADMIN_BOOTSTRAP_KEY` and
`POST /v1/bootstrap` with header `X-Bootstrap-Key: <key>` to create the first
admin and retrieve its API key. The endpoint is disabled in all other states.

## Web UI

A read-only browser UI is served at `http://<aihub-host>/ui/`.

- **Auth.** Paste an existing API key once on `/ui/login`. The server mints a
  signed cookie (HMAC-SHA256) valid for 7 days. The cookie carries the same
  authority as the bearer key — revoking the key invalidates the session on
  the next request.
- **Scope.** MVP is read-only: queue overview, work-item list + detail,
  memory index + view. All write operations still go through the CLI or MCP.
- **Refresh.** Each page issues a 5-second HTMX poll on its data region so
  long-running attempts and queue churn surface without a manual reload.
- **No third-party JS.** HTMX is vendored at
  `internal/server/static/htmx.min.js` and served from the same origin; the UI
  works in air-gapped deployments without any CDN access.

### Configuration

| env | required | purpose |
|---|---|---|
| `DATABASE_URL` | yes | Postgres DSN. |
| `PORT` | no | Listen port (default `8080`). |
| `ADMIN_BOOTSTRAP_KEY` | no | Enables `POST /v1/bootstrap` until the first admin is created. |
| `POLYFORGE_UI_COOKIE_SECRET` | recommended | 32+ bytes (raw or hex). HMAC key for `/ui/*` session cookies. If unset, the server generates an ephemeral random secret on every start and logs a warning — existing UI sessions will not survive restart. |

## Layout

```
cmd/aihub/           # main + bootstrap glue
internal/server/     # echo router, middleware, route handlers
  templates/         # html/template files for /ui (layout + per-page)
  static/            # CSS + vendored htmx.min.js, served from /ui/static/
internal/auth/       # API-key hashing, AttemptCredential verification
internal/domain/     # business logic, separated from HTTP
internal/render/     # markdown → HTML (goldmark + chroma)
internal/db/         # connection pool
pkg/client/          # Go SDK used by the CLI
```

## Testing

```bash
go test -buildvcs=false ./...
```

Unit tests under `internal/server` cover middleware, auth-before-write
invariants, and the UI session/route smoke layer. Integration tests against a
real Postgres are gated by `INTEGRATION=1`.
