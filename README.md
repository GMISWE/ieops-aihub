# aihub

**aihub is the polyforge backend** - a PostgreSQL-backed HTTP service and MCP
server that gives AI coding agents a shared, durable work-item lifecycle,
persistent memory, and the coordination primitives a fleet of agents needs to
work the same repositories without colliding.

It ships as two binaries built from one module: `aihub` (the API server) and
`polyforge` (an MCP server plus CLI that an agent talks to from inside its
editor).

## What it is

AI coding agents are stateless and uncoordinated by default. Each session
starts from nothing, two agents happily edit the same file, and yesterday's
reasoning is gone tomorrow. polyforge fixes that by making the backend the
source of truth:

- **One state authority.** Every work item, attempt, event, lock, and memory
  lives in PostgreSQL. Clients keep almost no durable state of their own.
- **Memory-first.** Agents recall prior decisions, pitfalls, and conventions
  before acting, and write new learnings back for the next session.
- **A real lifecycle.** Work moves through spec -> plan -> implement -> review
  -> PR, with each step recorded server-side and visible to the whole team.
- **Coordination, not collisions.** Resource locks and conflict prediction stop
  two agents from claiming the same files or branch.

## Features

- **Work-item lifecycle** - create, claim, and drive tasks through ordered steps
  (spec / plan / implement / review / PR). Ownership is explicit and each step's
  status is reported back to the server.
- **Memory system** - `remember` / `recall` with a forgetting curve: memories
  carry a strength that decays over time and is reinforced on use, so stale
  notes fade while useful ones stay. Memories link to each other and to the work
  item that produced them. Semantic recall over pgvector is available when an
  embedding provider is configured (see `EMBEDDING_*` in Configuration).
- **MCP server** - the `polyforge` binary exposes the `pf_*` tool family over
  stdio MCP, so an agent (for example, in Claude Code) drives the whole
  lifecycle from chat. See [`docs/mcp-tools.md`](docs/mcp-tools.md).
- **Resource locks + conflict prediction** - a task declares the files or branch
  it touches; the server predicts conflicts before work starts and blocks
  double-claims.
- **Dependencies** - link work items (blocks / supersedes / related); blocked
  items stay out of the ready queue until their blockers close.
- **Read-only Web UI** - a browser view of the queue, work items, and memories,
  served from the same origin with no third-party JavaScript.
- **Single-binary distribution** - no language runtime to install on the client;
  an agent pulls one `polyforge` binary.

## Architecture

Two binaries built from one Go module, one database, and a plugin that lets an
AI agent drive it all:

```
  Claude Code  (polyforge plugin)
        |
        |  pf_* tools over stdio MCP
        v
  polyforge    MCP server + CLI, on the agent's machine
        |
        |  HTTP /v1  (bearer API key)
        v
  aihub        HTTP API server (Echo)  -->  PostgreSQL  (source of truth)
        ^
        |  HTTP /ui  (signed-cookie auth)
        |
  Web UI       read-only, in a browser
```

- **`aihub`** - an Echo HTTP server. It owns every side effect and all state.
  `internal/domain/` holds the business logic and invariants (lifecycle, claim /
  attempt / lock semantics, the memory forgetting curve, the dependency graph,
  conflict prediction, GC) with no transport concerns; `internal/server/` is the
  router, middleware, three-tier auth (public / bearer API key / HMAC session
  cookie), and the Web UI.
- **`polyforge`** - runs on the agent's machine as an MCP server and CLI. Claude
  Code reaches it through the polyforge plugin and calls the `pf_*` tools, which
  in turn call `aihub` over HTTP. There is a single path:
  tool or command -> `pkg/client` -> HTTP -> domain.
- **PostgreSQL** - the single source of truth. Work items, attempts, events,
  locks, memories, and dependencies all live here.

## Getting started

There are two sides to running polyforge: standing up the `aihub` server, and
using it from an AI agent. A team typically runs one shared `aihub` server that
everyone's agents point at.

### Run the server

```bash
make build                       # produces bin/aihub and bin/polyforge
export DATABASE_URL=postgres://user:pass@host:5432/aihub
make migrate-up                  # apply the schema (PostgreSQL 18+)
./bin/aihub                      # listens on :8080 (override with PORT)
```

Create the first admin while the `users` table is empty: set
`ADMIN_BOOTSTRAP_KEY`, then `POST /v1/bootstrap` with header
`X-Bootstrap-Key: <key>` to mint the first admin and its API key. The endpoint
is disabled in every other state.

### Use it from an AI agent

Agents talk to polyforge through the Claude Code plugin, which launches the
`polyforge` MCP server with a key from `~/.polyforge/config.toml`. This repo is
itself the plugin's marketplace (`plugins/polyforge/` + `.claude-plugin/`), so a
workspace can install the plugin straight from here. Once connected, ask the
agent for `pf_whoami` to confirm the link, then start work with the lifecycle
skills (`/pf-work`, `/pf-status`, ...).

The full newcomer walkthrough - API key, config, GitHub access, plugin install,
and a first work item - is in [`docs/onboarding.md`](docs/onboarding.md).

## Configuration

Server-side environment variables:

| env | required | purpose |
|---|---|---|
| `DATABASE_URL` | yes | PostgreSQL DSN (targets PostgreSQL 18+). |
| `PORT` | no | Listen port (default `8080`). |
| `ADMIN_BOOTSTRAP_KEY` | no | Enables `POST /v1/bootstrap` until the first admin exists. |
| `POLYFORGE_UI_COOKIE_SECRET` | recommended | 32+ bytes (raw or hex), the HMAC key for `/ui/*` session cookies. If unset, the server generates an ephemeral secret on each start and logs a warning - UI sessions will not survive a restart. |
| `RENDER_MEMORY_TYPES` | no | Comma-separated memory types whose markdown is pre-rendered to HTML on save (for the artifact viewer). |
| `EMBEDDING_ENABLED` | no | Set to `true`/`1` to turn on optional pgvector semantic recall. When enabled, set `EMBEDDING_PROVIDER` (`openai`/`ollama`), `EMBEDDING_MODEL`, and `EMBEDDING_DIMS` (plus `EMBEDDING_BASE_URL` / `EMBEDDING_API_KEY` as the provider needs). Defaults off; recall then uses recency and strength only. |

## Web UI

A read-only browser UI is served at `/ui/`.

- **Auth.** Paste an API key once at `/ui/login`; the server mints a signed
  (HMAC-SHA256) cookie valid for 7 days. The cookie carries the same authority
  as the key - revoking the key invalidates the session on the next request.
- **Scope.** Read-only: queue overview, work-item list and detail, memory index
  and view, and the artifact / review viewer (markdown, diagrams, annotations).
  All writes still go through the CLI or MCP tools.
- **Refresh.** The work-item detail view polls its activity feed every 5
  seconds, so a running attempt's progress surfaces without a manual reload.
- **No third-party JS.** HTMX and the fonts are vendored under
  `internal/server/static/` and served from the same origin, so the UI works in
  air-gapped deployments with no CDN access.

## Project layout

```
cmd/aihub/         # HTTP API server entrypoint
cmd/polyforge/     # MCP server + CLI entrypoint
internal/
  server/          # Echo router, middleware, auth, /ui templates + static assets
  domain/          # business logic: work items, attempts, locks, events, memory, conflicts, GC
  mcp/             # pf_* MCP tool definitions
  cli/             # polyforge CLI commands (init, doctor, ...)
  auth/            # API-key hashing, attempt-credential verification
  db/              # pgx connection pool + SQL migrations
  embedding/       # pluggable embedding provider interface
  render/          # markdown -> HTML (goldmark + chroma)
pkg/client/        # Go HTTP client used by the CLI and MCP server
plugins/polyforge/ # the Claude Code plugin (skills, hooks, MCP launcher)
.claude-plugin/    # marketplace.json - lists the polyforge plugin
v0/                # archived first-generation Python implementation (read-only)
```

## Development

```bash
make build         # build both binaries to bin/
make test          # go test ./...  (integration tests gated by INTEGRATION=1)
make lint          # golangci-lint
make migrate-up    # apply migrations (needs DATABASE_URL)
```

Migrations, deployment, and rollback are covered in
[`docs/deployment.md`](docs/deployment.md).

## Contributing

polyforge is built with polyforge: work on this repository runs through the same
spec -> plan -> implement -> review -> PR lifecycle it provides, driven from
Claude Code with the polyforge plugin. Start with
[`docs/onboarding.md`](docs/onboarding.md) to set up; the long-form design
contract is in
[`docs/design/polyforge-v1-design.md`](docs/design/polyforge-v1-design.md) (being
updated - parts have drifted from the implementation).
