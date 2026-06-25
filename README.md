# aihub

The **polyforge** backend and plugin host.

This repository contains three things:

1. **The polyforge backend** (Go) - an HTTP API + read-only Web UI for managing
   work items, run attempts, memories, events, and dependencies, backed by
   PostgreSQL. This is the source of truth for all polyforge state.
2. **The polyforge plugin + marketplace** (`plugins/`, `.claude-plugin/`) - the
   Claude Code plugin (skills, hooks, MCP launcher) that drives the backend.
   This repo also serves as its marketplace, so a workspace can install it
   straight from here.
3. **`v0/`** - the archived first-generation Python (FastAPI) implementation.
   Read-only, kept for reference; not built and not imported by any Go code.

```
cmd/                 # two binaries: aihub (server) + polyforge (CLI + MCP)
internal/            # backend implementation (domain, server, mcp, cli, ...)
pkg/client/          # Go SDK used by the CLI and MCP server
docs/                # this index + design doc + onboarding + references
plugins/polyforge/   # vendored Claude Code plugin (skills, hooks, launcher)
.claude-plugin/      # marketplace.json - lists the polyforge plugin
v0/                  # archived Python implementation (read-only)
```

The team runs a shared backend at `http://10.146.0.16:8080`; most users never
build the server and only install the plugin (see
[`docs/onboarding.md`](docs/onboarding.md)).

## Architecture in one picture

```
three client surfaces                              shared backend
---------------------                              --------------
MCP   (46 pf_* tools)  -+
CLI   (polyforge ...)   +-> pkg/client -> HTTP handlers -> domain -> Postgres
Web UI (/ui/*, read)   -+   (Go SDK)      (echo, internal/  (business  (pgx)
                                           server)           rules)
```

- `internal/domain/` - business logic and invariants (wi lifecycle, claim /
  attempt / lock semantics, the memory forgetting curve, dependency graph,
  conflict prediction, GC). No HTTP or transport concerns.
- `internal/server/` - echo router, middleware, three-tier auth (public /
  Bearer API key / HMAC session cookie), and the read-only Web UI.
- `internal/mcp/` - the 46 `pf_*` MCP tools (see
  [`docs/mcp-tools.md`](docs/mcp-tools.md)).
- `internal/cli/` - the `polyforge` CLI commands (`init`, `doctor`, ...).
- `pkg/client/` - the Go SDK both the MCP server and CLI call the HTTP API
  through. There is one path: tool/command -> client -> HTTP -> domain.

## Quick start (server)

```bash
make build                       # builds bin/aihub and bin/polyforge
DATABASE_URL=postgres://... ./bin/aihub
```

The server listens on `:8080` by default; override with `PORT`. Run database
migrations first - see [`docs/deployment.md`](docs/deployment.md).

### First admin user

When the `users` table is empty, set `ADMIN_BOOTSTRAP_KEY` and
`POST /v1/bootstrap` with header `X-Bootstrap-Key: <key>` to create the first
admin and retrieve its API key. The endpoint is disabled in all other states.

### Configuration

| env | required | purpose |
|---|---|---|
| `DATABASE_URL` | yes | Postgres DSN. |
| `PORT` | no | Listen port (default `8080`). |
| `ADMIN_BOOTSTRAP_KEY` | no | Enables `POST /v1/bootstrap` until the first admin is created. |
| `POLYFORGE_UI_COOKIE_SECRET` | recommended | 32+ bytes (raw or hex). HMAC key for `/ui/*` session cookies. If unset, the server generates an ephemeral random secret on every start and logs a warning - existing UI sessions will not survive restart. |
| `RENDER_MEMORY_TYPES` | no | Comma-separated memory types whose markdown is pre-rendered to HTML on save (artifact viewer). |

## Web UI

A read-only browser UI is served at `http://<aihub-host>/ui/`.

- **Auth.** Paste an existing API key once on `/ui/login`. The server mints a
  signed cookie (HMAC-SHA256) valid for 7 days. The cookie carries the same
  authority as the bearer key - revoking the key invalidates the session on
  the next request.
- **Scope.** Read-only: queue overview, work-item list + detail, memory index
  + view, and the artifact / review viewer (markdown + d2 diagrams +
  annotations). All write operations go through the CLI or MCP.
- **Refresh.** Each page issues a 5-second HTMX poll on its data region so
  long-running attempts and queue churn surface without a manual reload.
- **No third-party JS.** HTMX and the Geist fonts are vendored under
  `internal/server/static/` and served from the same origin; the UI works in
  air-gapped deployments without any CDN access.

## The polyforge plugin

`plugins/polyforge/` is the Claude Code plugin that talks to this backend:
the `/pf-*` lifecycle skills, session hooks, and the MCP launcher
(`bin/polyforge-mcp.sh`, which downloads the matching `polyforge` binary on
first run). `.claude-plugin/marketplace.json` lists it as a Claude Code
marketplace so a workspace can install the plugin straight from this repo.

The full newcomer flow (API key -> install -> first work item), including the
exact `/plugin` commands, is in [`docs/onboarding.md`](docs/onboarding.md).

## Build, test, deploy

```bash
make build                       # bin/aihub + bin/polyforge
make test                        # go test ./...  (integration tests gated by INTEGRATION=1)
make lint                        # golangci-lint
make migrate-up                  # goose up (needs DATABASE_URL)
make deploy                      # pull latest image + restart on PROD_HOST
```

Operational details (migrations, rollback, image build, deploy) are in
[`docs/deployment.md`](docs/deployment.md).

## Documentation

- [`docs/onboarding.md`](docs/onboarding.md) - newcomer guide (install the
  plugin, create your first work item).
- [`docs/mcp-tools.md`](docs/mcp-tools.md) - reference for the 46 `pf_*` MCP
  tools.
- [`docs/deployment.md`](docs/deployment.md) - migrations, deploy, rollback,
  configuration.
- [`docs/design/polyforge-v1-design.md`](docs/design/polyforge-v1-design.md) -
  the long-form architecture / design contract (Chinese). See the errata block
  at the top: parts of it have drifted from the implementation.
