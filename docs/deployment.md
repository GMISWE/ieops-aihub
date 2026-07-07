# Deployment & operations

A self-contained runbook to stand up the `aihub` server from scratch on a single
Linux host, by hand, over SSH and a terminal. No CI, orchestrator, or agent is
required. Every command below was run end-to-end on a clean host before this doc
was written; the gotchas in [Troubleshooting](#troubleshooting) are real failures
hit during that run, not hypotheticals.

For local development (running the binary straight from source) see
[`../README.md`](../README.md). The team's existing one-command deploy is kept at
the end under [Team deployment](#team-deployment-reference).

## Contents

1. [What you are deploying](#what-you-are-deploying)
2. [Prerequisites](#prerequisites)
3. [Get the image](#1-get-the-image)
4. [Compose file](#2-compose-file)
5. [Configuration (`.env`)](#3-configuration-env)
6. [Run database migrations](#4-run-database-migrations)
7. [Start the server](#5-start-the-server)
8. [Bootstrap the first admin](#6-bootstrap-the-first-admin)
9. [Verify](#7-verify)
10. [TLS / reverse proxy (optional)](#8-tls--reverse-proxy-optional)
11. [Backups](#9-backups)
12. [Upgrades & rollback](#10-upgrades--rollback)
13. [Troubleshooting](#troubleshooting)
14. [Team deployment (reference)](#team-deployment-reference)

## What you are deploying

`aihub` is a single Go HTTP server (listens on `:8080`) backed by PostgreSQL. It
serves the REST/MCP API and a web console at `/ui`. The server is stateless — all
state lives in Postgres — so a deploy is just "run the container against a
database that has the current schema."

| Component | Detail |
|---|---|
| Server | one container, HTTP on `:8080`; image bakes the `aihub` binary, `goose`, and the SQL migrations at `/migrations` |
| Database | **PostgreSQL 18** with the **`pgvector`** extension (required — a migration runs `CREATE EXTENSION vector`) |
| Migrations | plain SQL under `internal/db/migrations/`, applied by [goose](https://github.com/pressly/goose); the container self-migrates when started with the `migrate-up` argument |
| Web console | `GET /ui/` (redirects to `/ui/wi`) |

This runbook uses Docker Compose to run both the server and its database on one
host. That is the smallest complete setup; a managed Postgres works too (skip the
`postgres` service and point `DATABASE_URL` at it — it must still have `pgvector`).

## Prerequisites

- A Linux host you can SSH into, with **Docker Engine + the Compose plugin**.
  Verify:

  ```bash
  docker version         # Docker Engine 24+
  docker compose version # Compose v2+ (the `docker compose` plugin, not the old `docker-compose`)
  ```

  If Docker is not installed, use the official convenience script (Debian/Ubuntu):

  ```bash
  curl -fsSL https://get.docker.com | sh
  sudo systemctl enable --now docker
  ```

- Outbound network access to pull the container images (or build locally — see
  below).
- A working directory to hold the compose file and `.env` (this runbook uses
  `~/aihub/`).

```bash
mkdir -p ~/aihub && cd ~/aihub
```

## 1. Get the image

The server image is `us-west1-docker.pkg.dev/devv-404803/public/aihub`. Choose one:

**Option A — pull the published image (what the team runs).** CI builds and pushes
`:latest` plus a git-SHA tag on every push to `main`, and a semver tag on `v*`
tags. Pulling needs Artifact Registry auth on the host:

```bash
# One-time: authenticate Docker to the registry.
gcloud auth configure-docker us-west1-docker.pkg.dev       # if you use gcloud, OR
cat key.json | docker login -u _json_key --password-stdin \
  https://us-west1-docker.pkg.dev                          # with a service-account key

docker pull us-west1-docker.pkg.dev/devv-404803/public/aihub:latest
```

**Option B — build from source (no registry access needed).** The repo ships a
multi-stage `Dockerfile`:

```bash
git clone https://github.com/GMISWE/ieops-aihub.git && cd ieops-aihub
docker build -t aihub:local \
  --build-arg VERSION="$(git describe --tags --always)" \
  --build-arg GIT_COMMIT="$(git rev-parse HEAD)" \
  --build-arg BUILD_TIME="$(date -u +%FT%TZ)" .
```

If you build locally, use `aihub:local` wherever the compose file below names the
`us-west1-...` image.

## 2. Compose file

Create `~/aihub/docker-compose.yml`:

```yaml
name: aihub

services:
  postgres:
    image: pgvector/pgvector:pg18        # Postgres 18 WITH pgvector (required)
    env_file: .env
    environment:
      POSTGRES_USER: ${POSTGRES_USER}
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
      POSTGRES_DB: ${POSTGRES_DB}
    volumes:
      # Postgres 18+ images require the mount at /var/lib/postgresql (data lives in a
      # major-version subdirectory). Mounting /var/lib/postgresql/data makes initdb
      # refuse to start and the container crash-loops. See Troubleshooting.
      - pgdata:/var/lib/postgresql
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ${POSTGRES_USER} -d ${POSTGRES_DB}"]
      interval: 5s
      timeout: 3s
      retries: 20
    restart: unless-stopped

  aihub:
    image: us-west1-docker.pkg.dev/devv-404803/public/aihub:latest
    depends_on:
      postgres:
        condition: service_healthy
    env_file: .env
    ports:
      - "8080:8080"                       # host:container — change the host port to taste
    restart: unless-stopped

volumes:
  pgdata:
```

Notes:

- **`pgvector/pgvector:pg18` is not optional.** Migration `0006` runs
  `CREATE EXTENSION IF NOT EXISTS vector`; a plain `postgres:18` image has no
  `vector` extension and the migration fails.
- Both services read `env_file: .env`. Postgres ignores the server-only variables
  and vice-versa; keeping one file is simpler.
- The database is not published to the host (no `ports:` on `postgres`) — the
  server reaches it over the compose network at hostname `postgres`.

## 3. Configuration (`.env`)

Create `~/aihub/.env`. This generates one Postgres password and reuses it in
`DATABASE_URL`, so it is copy-paste correct as-is; do not reuse the example
secrets in a real deployment.

```bash
PGPW=$(openssl rand -hex 16)
cat > .env <<EOF
# --- Postgres ---
POSTGRES_USER=aihub
POSTGRES_PASSWORD=${PGPW}
POSTGRES_DB=aihub

# --- aihub server ---
DATABASE_URL=postgres://aihub:${PGPW}@postgres:5432/aihub?sslmode=disable
PORT=8080
POLYFORGE_UI_COOKIE_SECRET=$(openssl rand -hex 32)
ADMIN_BOOTSTRAP_KEY=$(openssl rand -hex 16)
EOF
```

(`POSTGRES_PASSWORD` and the password inside `DATABASE_URL` must match — the
snippet above keeps them in sync via `$PGPW`.) Full variable reference:

| env | required | purpose |
|---|---|---|
| `DATABASE_URL` | **yes** | Postgres DSN. Inside compose use host `postgres`. Example: `postgres://aihub:<pw>@postgres:5432/aihub?sslmode=disable`. |
| `PORT` | no | Listen port (default `8080`). |
| `POLYFORGE_UI_COOKIE_SECRET` | recommended | 32+ bytes (raw or hex), the HMAC key for `/ui/*` session cookies. If unset, the server generates an ephemeral secret each start and logs a warning — UI logins do not survive a restart. |
| `ADMIN_BOOTSTRAP_KEY` | first boot only | Enables `POST /v1/bootstrap` until the first admin exists (see step 6). Unset it afterwards. |
| `RENDER_MEMORY_TYPES` | no | Comma-separated memory types whose markdown is pre-rendered to HTML on save (for the artifact viewer). |
| `EMBEDDING_ENABLED` | no | `true`/`1` turns on optional pgvector semantic recall. When enabled also set `EMBEDDING_PROVIDER` (`openai`/`ollama`), `EMBEDDING_MODEL`, `EMBEDDING_DIMS`, and `EMBEDDING_BASE_URL` / `EMBEDDING_API_KEY` as the provider needs. Default off; recall then uses recency + strength only. **`pgvector` is still required either way** because the schema migration creates the extension. |
| `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` | for the bundled DB | Consumed by the `postgres` service; must line up with `DATABASE_URL`. |

## 4. Run database migrations

Migrations are baked into the image; run them with the `migrate-up` argument,
which runs `goose ... up` and exits (any other argument starts the server). Bring
the database up first, then migrate:

```bash
docker compose up -d postgres
docker compose run --rm aihub migrate-up
```

Expected tail (the version number grows as migrations are added):

```
Running database migrations...
2026/... OK   0001_initial.sql
...
2026/... OK   0027_run_attempts_pause_reason.sql
2026/... goose: successfully migrated database to version: 27
```

Run migrations **before** starting a new server build whenever the release adds or
changes a migration. goose is forward-and-back per migration and there is no
automatic pre-deploy gate, so on a schema-changing release **take a DB snapshot
first** ([Backups](#9-backups)).

## 5. Start the server

```bash
docker compose up -d aihub
```

Wait for health (the first request may return `000` for a second or two while the
process starts):

```bash
until curl -fs http://localhost:8080/v1/health >/dev/null; do sleep 1; done
curl -s http://localhost:8080/v1/health
# {"db_ok":true,"status":"ok","version":"..."}
```

## 6. Bootstrap the first admin

On a fresh database the `users` table is empty. With `ADMIN_BOOTSTRAP_KEY` set,
mint the first admin. **The request needs a JSON body with `email` and
`display_name`** in addition to the header — a header-only call returns
`400 email and display_name required`:

```bash
curl -s -X POST http://localhost:8080/v1/bootstrap \
  -H "X-Bootstrap-Key: $(grep '^ADMIN_BOOTSTRAP_KEY=' .env | cut -d= -f2)" \
  -H "Content-Type: application/json" \
  -d '{"email":"admin@example.com","display_name":"Admin"}'
```

Response (store `api_key` — it is shown only once):

```json
{
  "api_key": "………",
  "api_key_id": "k_……",
  "user_id": "u_……",
  "email": "admin@example.com",
  "display_name": "Admin",
  "role": "admin",
  "note": "save api_key — it will not be shown again"
}
```

The endpoint disables itself once any user exists (a second call returns
`403 bootstrap already done — users table is non-empty`). Remove
`ADMIN_BOOTSTRAP_KEY` from `.env` and `docker compose up -d aihub` to drop it.

## 7. Verify

| check | command | expected |
|---|---|---|
| DB + server | `curl -s localhost:8080/v1/health` | `{"db_ok":true,"status":"ok",...}` |
| build info | `curl -s localhost:8080/v1/version` | JSON with `version`, `git_commit`, `build_time`, `min_client_version` |
| web console | `curl -s -o /dev/null -w '%{http_code} %{redirect_url}\n' localhost:8080/ui/` | `302 .../ui/wi` |

Open `http://<host>:8080/ui/` in a browser and sign in with the admin API key.

## 8. TLS / reverse proxy (optional)

The server speaks plain HTTP on `:8080`. For anything reachable off the host, put
a TLS-terminating reverse proxy in front and set `POLYFORGE_UI_COOKIE_SECRET` so
`/ui` sessions survive restarts. This section is best-practice guidance and is not
part of the verified minimal path above. Example with Caddy (automatic HTTPS):

```
# /etc/caddy/Caddyfile
aihub.example.com {
    reverse_proxy localhost:8080
}
```

Then bind the compose port to loopback only (`"127.0.0.1:8080:8080"`) so the
server is reachable solely through the proxy.

## 9. Backups

All state is in Postgres — back that up.

```bash
# Dump (verified): writes a plain-SQL dump you can restore with psql.
docker compose exec -T postgres pg_dump -U aihub -d aihub > aihub-$(date +%F).sql

# Restore into a fresh database:
docker compose exec -T postgres psql -U aihub -d aihub < aihub-YYYY-MM-DD.sql
```

Take a dump before any schema-changing upgrade.

## 10. Upgrades & rollback

**Upgrade:**

```bash
docker compose pull aihub            # or rebuild: docker build ...
# If the new release changed migrations, snapshot the DB, then:
docker compose run --rm aihub migrate-up
docker compose up -d aihub
curl -s localhost:8080/v1/health
```

**Rollback.** Images are tagged by git SHA, so roll back by pinning a previous tag
instead of `:latest`. Set the tag in `.env` and reference it in compose
(`image: ...aihub:${AIHUB_TAG:-latest}`), or edit the compose file directly:

```bash
docker pull us-west1-docker.pkg.dev/devv-404803/public/aihub:<previous-sha>
# point the aihub service at <previous-sha>, then:
docker compose up -d --force-recreate aihub
```

If the bad release also ran a migration, roll the schema back **before** starting
the older binary — an old server against a newer schema may fail. The image
entrypoint only special-cases the `migrate-up` argument (everything else starts
the server), so a `down` must override the entrypoint:

```bash
docker compose run --rm --entrypoint sh aihub \
  -c 'goose -dir /migrations postgres "$DATABASE_URL" down'   # rolls back one migration
```

Prefer backward-compatible migrations within a release so a server rollback alone
is safe.

## Troubleshooting

- **Postgres container crash-loops; logs say "in 18+, these Docker images are
  configured to store database data in a … major-version-specific directory" /
  "PostgreSQL data in: /var/lib/postgresql/data (unused mount/volume)".**
  The Postgres 18+ image wants the volume mounted at `/var/lib/postgresql`, not
  `/var/lib/postgresql/data`. Fix the mount (as in the compose above), then
  recreate with a clean volume: `docker compose down -v && docker compose up -d postgres`.

- **Migration fails on `CREATE EXTENSION vector` / `type "vector" does not exist`.**
  The database image lacks `pgvector`. Use `pgvector/pgvector:pg18` (or install the
  extension into your managed Postgres); the plain `postgres` image will not work.

- **`POST /v1/bootstrap` returns `400 email and display_name required`.**
  Send a JSON body (`-d '{"email":...,"display_name":...}'`) and
  `Content-Type: application/json`, not just the `X-Bootstrap-Key` header.

- **`POST /v1/bootstrap` returns `403 bootstrap already done`.**
  An admin already exists. Bootstrap is one-time; create further users through the
  API with the admin key.

- **`/v1/health` returns `000` right after `up`.**
  The server is still starting; retry for a few seconds (the `until` loop in step 5).
  If it never becomes `200`, check `docker compose logs aihub` — most often
  `DATABASE_URL` is wrong or the DB is not reachable/healthy.

## Team deployment (reference)

The team runs a single shared instance and does not repeat the from-scratch steps
above each time — the host, compose file, and registry auth already exist. The
`Makefile` wraps the routine update:

| | |
|---|---|
| Host | `10.146.0.16` (`PROD_HOST`) |
| Compose dir on host | `/root/manifests/aihub-v1` (`COMPOSE_DIR`) |
| Image | `us-west1-docker.pkg.dev/devv-404803/public/aihub` (`GCR_IMAGE`) |
| Port | `8080` |

```bash
make deploy
# ssh PROD_HOST: docker pull GCR_IMAGE:latest
#                cd COMPOSE_DIR && docker compose up -d --no-deps --force-recreate aihub
```

Requirements: SSH access to `PROD_HOST`, registry auth on the host
(`docker login us-west1-docker.pkg.dev`), and a compose file at `COMPOSE_DIR`.
Typical release order: merge to `main` → wait for CI to push `:latest` (+ the SHA
tag) → run `migrate-up` if the release changed migrations → `make deploy` →
verify `/v1/health` and `/v1/version`.

## Health & version endpoints

| endpoint | use |
|---|---|
| `GET /v1/health` | server + DB connectivity (`{"db_ok":true,"status":"ok",...}`) |
| `GET /v1/version` | running version, git commit, build time, min client version |
| `GET /ui/` | web console (redirects to `/ui/wi`) |
