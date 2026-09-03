# Deployment & operations

A self-contained runbook to stand up the `aihub` server from scratch on a single
Linux host, by hand, over SSH and a terminal. No CI, orchestrator, or agent is
required. Every command below was run end-to-end on a clean host before this doc
was written; the gotchas in [Troubleshooting](#troubleshooting) are real failures
hit during that run, not hypotheticals.

For local development (running the binary straight from source) see
[`../README.md`](../README.md). What production runs today, and the procedure for
replacing that container, is at the end under
[Team deployment](#team-deployment-reference). There is no one-command deploy:
`make deploy` prints a pointer to that section and exits 1.

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
| `POLYFORGE_UI_COOKIE_SECRET` | **yes** | 32+ bytes (raw or hex), the HMAC key for `/ui/*` session cookies. **The server refuses to start without it** — see [The `/ui` session key](#the-ui-session-key) below for why, and for the one documented way to decline. |
| `ADMIN_BOOTSTRAP_KEY` | first boot only | Enables `POST /v1/bootstrap` until the first admin exists (see step 6). Unset it afterwards. |
| `RENDER_MEMORY_TYPES` | no | Comma-separated memory types whose markdown is pre-rendered to HTML on save (for the artifact viewer). |
| `EMBEDDING_ENABLED` | no | `true`/`1` turns on optional pgvector semantic recall. When enabled also set `EMBEDDING_PROVIDER` (`openai`/`ollama`), `EMBEDDING_MODEL`, `EMBEDDING_DIMS`, and `EMBEDDING_BASE_URL` / `EMBEDDING_API_KEY` as the provider needs. Default off; recall then uses recency + strength only. **`pgvector` is still required either way** because the schema migration creates the extension. |
| `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` | for the bundled DB | Consumed by the `postgres` service; must line up with `DATABASE_URL`. |

### The `/ui` session key

`POLYFORGE_UI_COOKIE_SECRET` is the HMAC key for `/ui/*` session cookies. The
property that matters is not that it is secret — it is that it is **the same
value in the next process**. A cookie minted by one process is verified by its
successor, so a key that changes on restart signs out every `/ui` user at that
moment, and they have to paste their API key again.

It used to be optional: unset meant "32 random bytes per process, plus a warn
line on stderr". Production ran that way from the day `/ui` shipped, so **every
deploy silently signed everybody out** and the only trace was one line in the
container log — [step 7](#7-verify) checks `/v1/version`, `/v1/health` and the
authed read path, and an ephemeral cookie key leaves all three green. The
variable was already documented here, accurately, and it was still never set;
a signal nobody reads is not a signal (aihub#344).

So there are three states and no fourth:

| value | behaviour |
|---|---|
| 32+ bytes, hex or raw | signs sessions with it; sessions survive restarts. Surrounding whitespace is trimmed, so `…=abc ` and `…=abc` are the same key — gated by `TestUICookieSecretTrimmedValueIsTheSameKey`, which mints a session in one process and verifies it in another started from the untrimmed spelling |
| the literal `ephemeral` | random key per process — the old behaviour, now something you have to ask for. The server warns on every start, and the choice is visible in the env-file |
| unset | **the server refuses to start** and prints the `openssl` line to fix it |

Refusing to start is deliberate, and it is a trade: a fresh install with no
configuration now stops at boot instead of coming up. That failure is found in
seconds by whoever is doing the install and is one line to fix; the one it
replaces was a mass sign-out on every deploy of a running system that nothing
could go red on. Two consequences worth knowing before you deploy:

- **Add the variable to the env-file before rolling out a build that contains
  this change**, or the new container will exit at startup. On production that
  is `/root/aihub.env`; the pre-flight check in the
  [current production procedure](#current-production-cloud-sql--bare-docker-run)
  covers it.
- **Setting it for the first time signs everybody out once**, because the
  sessions currently in browsers were signed with the outgoing process's random
  key. That is the last time it happens.

```bash
# Generate and append. mode 600 already; keep it that way.
printf 'POLYFORGE_UI_COOKIE_SECRET=%s\n' "$(openssl rand -hex 32)" >> /root/aihub.env
```

Treat the value like `DATABASE_URL` from then on: **replacing** it signs
everybody out exactly as losing it does, so it is not a variable to regenerate
casually or to leave out when an env-file is rebuilt on a new host.

## 4. Run database migrations

Migrations are baked into the image; run them with the `migrate-up` argument,
which runs `goose ... up` and exits (any other argument starts the server). Bring
the database up first, then migrate:

```bash
docker compose up -d postgres
docker compose run --rm aihub migrate-up
```

Expected tail:

```
Running database migrations...
2026/... OK   0001_initial.sql
...
2026/... goose: successfully migrated database to version: <N>
```

Signal: `<N>` is the highest migration number present in
`internal/db/migrations/` at the revision you are deploying — read it off that
directory, not off a number written here, which would be stale one migration
later. `no migrations to run` is also a pass: the database is already there.

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
# {"db_ok":true,"embedding_enabled":true,"embedding_ok":true,"status":"ok","version":"..."}
```

`status` is `ok` or `degraded`, and the endpoint answers **200 either way** —
the verdict is in the body, never in the status code. See
[Health & version endpoints](#health--version-endpoints).

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
| DB + server + embedding | `curl -s localhost:8080/v1/health` | `{"db_ok":true,"embedding_ok":true,...,"status":"ok"}` — `status` is `degraded` (still HTTP 200) if a dependency is down; see [Health & version endpoints](#health--version-endpoints) |
| build info | `curl -s localhost:8080/v1/version` | JSON with `version`, `git_commit`, `build_time`, `min_client_version` |
| web console | `curl -s -o /dev/null -w '%{http_code} %{redirect_url}\n' localhost:8080/ui/` | `302 .../ui/wi` |

Open `http://<host>:8080/ui/` in a browser and sign in with the admin API key.

## 8. TLS / reverse proxy (optional)

The server speaks plain HTTP on `:8080`. For anything reachable off the host, put
a TLS-terminating reverse proxy in front. TLS also gets the session cookie its
`Secure` flag: the server sets it from `X-Forwarded-Proto`, so a proxy that does
not forward that header leaves the cookie without it. This section is
best-practice guidance and is not part of the verified minimal path above.
Example with Caddy (automatic HTTPS):

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

- **`/v1/health` returns `200` with `"status":"degraded"`.**
  Not a bug, and not something to escalate to a restart: the code stays 200 on
  purpose (see [Health & version endpoints](#health--version-endpoints)). Read
  `db_ok` and `embedding_ok` to see which dependency it is; `embedding_error_kind`
  (`timeout` / `unreachable`) narrows the embedding case, and the full error —
  including the backend URL, which is why it is not in the body — is on the
  server's stderr (`docker compose logs aihub | grep 'embedding health'`).

- **The embedding backend is clearly dead but `/v1/health` still says `ok`.**
  Wait 15 s and ask again. The embedding probe result is cached with a 15 s TTL,
  so the endpoint reports the old verdict for up to that long after the backend
  dies. Two polls 15 s apart is the shortest reliable check.

- **The container exits immediately; logs end with `fatal: no /ui session
  signing key configured`.**
  `POLYFORGE_UI_COOKIE_SECRET` is missing from the env-file. The log prints the
  `openssl` line to fix it; the long form is
  [The `/ui` session key](#the-ui-session-key). This is deliberate — it used to
  start anyway with a per-process key, which signed out every `/ui` user on
  every deploy and could not be detected from any endpoint (aihub#344). It is
  not a reason to roll back: add the line and start the container again.

- **`/ui` users have to paste their API key again after a deploy.**
  Their cookies were signed with a key the new process does not have. Three
  causes: the variable is absent (only possible on a build older than
  aihub#344 — a newer one would not have started), it is set to `ephemeral`, or
  its **value changed** between the two containers. The startup line settles
  the first two — it is printed in both states, so it is never silent:

  ```bash
  docker logs aihub --since 10m 2>&1 | grep -F '/ui session key'
  ```

  For the third, compare the new container against the rollback anchor by
  **digest**, which answers "same value?" without printing the value:

  ```bash
  for c in aihub aihub-prev-<sha>; do
    v=$(docker inspect "$c" --format '{{range .Config.Env}}{{println .}}{{end}}' \
        | grep '^POLYFORGE_UI_COOKIE_SECRET=')
    printf '%s %s\n' "$c" "$([ -n "$v" ] && printf '%s' "$v" | sha256sum || echo 'NO KEY SET')"
  done
  ```

  Equal digests mean the key was not what changed; look at `ephemeral` instead.
  The `[ -n "$v" ]` guard matters: `sha256sum` of empty input is the constant
  `e3b0c442…`, which looks exactly like a real digest, so without it "this
  container had no key" and "this container had a key" are indistinguishable.
  ⚠️ A digest is only opaque if the value has entropy — the server enforces no
  minimum length, so if someone set a short passphrase its digest is
  dictionary-attackable once it is in a scrollback.

## Team deployment (reference)

Production has moved on from the single-Compose host this section originally
described: it now runs against a **managed Cloud SQL** database with a
co-located embedding service, and starts the server as a **bare `docker run`**
(no Compose file). The current setup and the retired one are both recorded below.

### Current production (Cloud SQL + bare `docker run`)

| | |
|---|---|
| Host | `10.146.0.34` (GPU host; its public IP changes across stop/start — use the internal IP over the jump host) |
| Database | **Cloud SQL** — managed Postgres 18 + pgvector at `10.20.80.3:5432`, `sslmode=require`. This is the "managed Postgres" path from [What you are deploying](#what-you-are-deploying); there is **no `postgres` container** |
| Embedding | a TEI container named `tei` on the same host and Docker network `aihub-net`, published on `:8085`. A deploy never touches it |
| Server | one `docker run` container named `aihub` on `aihub-net`, `-p 8080:8080`, `--env-file /root/aihub.env` (holds `DATABASE_URL`, the `EMBEDDING_*` vars, `PORT`, `POLYFORGE_UI_COOKIE_SECRET`, `ADMIN_BOOTSTRAP_KEY`) — **no Compose file**. That file is the only place a generated secret survives a deploy: the container is replaced wholesale, so neither its writable layer nor the image can hold one |
| Image | `us-west1-docker.pkg.dev/devv-404803/public/aihub`, pulled by **git-SHA tag** (not `:latest`). CI tags with the full 40-char commit SHA |
| Rollback anchor | the container being replaced is **stopped and renamed** to `aihub-prev-<short sha>`, never deleted. Exactly one anchor is kept |

The steps mirror the Compose flow above (back up → pull → `migrate-up` → swap →
verify); only the mechanics differ (bare `docker run`, managed DB). Everything
below runs **on the host**. `make deploy` is not the deploy path — it prints a
pointer to this section and exits 1, because a one-line target cannot carry the
four things this procedure exists for: a database backup (step 1), a recorded
rollback anchor (steps 2 and 6), migrations applied strictly **before** the new
binary starts (step 4 before step 6), and a check afterwards that the **read
path** still answers (step 7).

Two of the four deserve their reasons spelled out, because both are places where
the obvious shortcut is the one that hurts:

**Migrations land strictly before the new binary starts.** A migration that adds
a column the new code reads makes the reverse order fail loudly and immediately:
`0032` added `projects.members_version`, which the new binary selects on *every*
project read, so a binary-first rollout answers `GET /v1/projects` with
`500` and `column "members_version" of relation "projects" does not exist
(SQLSTATE 42703)`. Schema-first is safe in the other direction only while
migrations stay additive — the server selects an explicit column list, never
`SELECT *`, so a column an older binary does not know about is invisible to it.
**Read the release's migrations before deploying.** If one drops or renames
anything, the older binary will not survive the new schema and the container
anchor below is not enough on its own; the step-1 dump is.

**The container being replaced is renamed, not deleted.** A stopped container
keeps its image, env-file contents, network, port bindings and restart policy,
so rolling back is one `docker start`. After a `docker rm -f` all of that is
gone and the only way back is to reconstruct the whole `docker run` line — right
tag, right env-file, right network, right ports — at the moment you least want
to be reconstructing anything. `docker rm -f` is also the step most likely to be
refused: during the 2026-09-02 deploy an automated command-safety policy blocked
`docker rm -f aihub` outright (a judgement about the command itself, not a
missing permission), while `docker stop` + `docker rename` went through
unremarked.

```bash
IMG=us-west1-docker.pkg.dev/devv-404803/public/aihub
SHA=<target git sha on main>   # full 40-char SHA — the tag CI pushes. Wait for
                               # "Build & Push Docker image (main)" to be green.

# Identity of what is running now, captured BEFORE anything changes.
CUR=$(docker inspect -f '{{.Config.Image}}' aihub)   # …/aihub:<outgoing sha>
PREV=${CUR##*:}                                      # outgoing sha
ANCHOR=aihub-prev-$(printf %.7s "$PREV")             # e.g. aihub-prev-359a435

# DATABASE_URL is read out of the RUNNING container so the dump cannot be
# pointed at the wrong database by a typo. It contains the database password:
# assign it, never echo/cat it, never paste it anywhere.
DBURL=$(docker inspect aihub --format '{{range .Config.Env}}{{println .}}{{end}}' | grep ^DATABASE_URL= | cut -d= -f2-)

# An API key with access to at least one project — step 7 needs it. `read -rs`
# keeps it off the screen and out of the shell history.
read -rsp 'aihub API key: ' KEY; echo

# Pre-flight: the env-file must carry a /ui session key, or the new container
# exits at startup. Prints the NAME only, never the value.
#
# [^[:space:]] is load-bearing: the server also refuses an EMPTY or
# whitespace-only value, so a bare `=` anchor would report "present" for a
# configuration that will not start.
grep -Eq '^POLYFORGE_UI_COOKIE_SECRET=[^[:space:]]' /root/aihub.env \
  && echo 'POLYFORGE_UI_COOKIE_SECRET: present' \
  || echo 'POLYFORGE_UI_COOKIE_SECRET: MISSING or empty — stop, see below'
```

**If the pre-flight says MISSING, fix it before step 6, not after.** The server
refuses to start without it (aihub#344), so a swap done first takes `/ui` and
`/v1` down until the file is corrected. Nothing is down yet at this point:

```bash
printf 'POLYFORGE_UI_COOKIE_SECRET=%s\n' "$(openssl rand -hex 32)" >> /root/aihub.env
```

Adding it signs out the `/ui` users who are currently signed in — once — because
their cookies were signed with the outgoing process's random key. Every deploy
after this one leaves them signed in. See
[The `/ui` session key](#the-ui-session-key) for why this is fatal rather than a
warning, and what `=ephemeral` is for.

**1. Back up Cloud SQL.** `pg_dump` runs on `aihub-net` so it can reach the DB.

```bash
mkdir -p /root/backups
DUMP=cloudsql_$(date +%Y%m%d_%H%M%S).dump   # /root/backups on the host == /backups in the container
docker run --rm --network aihub-net -v /root/backups:/backups pgvector/pgvector:pg18 \
  pg_dump -Fc -d "$DBURL" -f "/backups/$DUMP"
ls -lh "/root/backups/$DUMP"
docker run --rm -v /root/backups:/backups pgvector/pgvector:pg18 \
  pg_restore -l "/backups/$DUMP" | tail -3
```

Signal: `pg_restore -l` lists TOC entries (it reads the archive, so it catches a
truncated one that a zero exit status would not), and the file is the size of a
database rather than of an error — **136 MB** on 2026-09-02. A dump of a few KB
means it wrote nothing useful; do not continue on one.

**2. Record the rollback anchor.**

```bash
echo "$CUR"                          # image the current container was created from
curl -s localhost:8080/v1/version    # git_commit must equal $PREV
```

Signal: `git_commit` equals `$PREV`. Note `version` reads `dev` on every
main-branch image — CI passes `GIT_COMMIT` but not `VERSION` — so `git_commit`,
not `version`, is the field that identifies a build.

**3. Pull the target image by SHA** (a SHA tag never lags the way `:latest` can).

```bash
docker pull "$IMG:$SHA"
```

Signal: `Status: Downloaded newer image for …:$SHA` (or `Image is up to date`).
`manifest unknown` means CI has not pushed that SHA yet — wait for it. Do not
fall back to `:latest`.

**4. Apply migrations** — same `migrate-up` mechanism as Compose, via
`docker run`. This runs the *new* image's goose against the live database while
the *old* container keeps serving; nothing is down yet.

```bash
docker run --rm --network aihub-net --env-file /root/aihub.env "$IMG:$SHA" migrate-up
```

Expected tail (2026-09-02, the `0032` release):

```
Running database migrations...
2026/09/02 ... OK   0032_projects_members_version.sql (7.83ms)
2026/09/02 ... goose: successfully migrated database to version: 32
```

Signal: `successfully migrated database to version: N`, where N is the highest
migration number in `internal/db/migrations/` at `$SHA`. `no migrations to run`
is also a pass — it means the release changed no schema. Anything else: stop
here. The old container is still serving and nothing needs undoing.

**5. Start the downtime poll** (optional; this is how the number below was
measured). In a second shell, before step 6:

```bash
while :; do
  printf '%s %s\n' "$(date +%s.%N)" \
    "$(curl -s -o /dev/null -w '%{http_code}' --max-time 1 localhost:8080/v1/health)"
  sleep 0.1
done | tee /root/swap-poll.log
```

Downtime is the gap from the last `200` before the swap to the first `200`
after. The `sleep 0.1` is that number's resolution. Ctrl-C it after step 7.

**6. Swap the container** — stop, rename, run.

```bash
docker stop aihub                     # graceful SIGTERM; releases :8080
docker rename aihub "$ANCHOR"         # the old container survives as the rollback anchor
docker run -d --name aihub --network aihub-net -p 8080:8080 --restart unless-stopped \
  --env-file /root/aihub.env "$IMG:$SHA"
docker ps -a --filter name=aihub --format '{{.Names}}\t{{.Status}}\t{{.Image}}'
```

Signals:

- Exactly two rows: `aihub` `Up …` on `$IMG:$SHA`, and `$ANCHOR` `Exited (…)` on
  the outgoing image.
- `docker inspect -f '{{.State.Status}} {{.HostConfig.RestartPolicy.Name}}' "$ANCHOR"`
  → `exited unless-stopped`. The anchor stays down on its own: `unless-stopped`,
  unlike `always`, does not restart a container that was stopped explicitly, so
  it cannot come back and take `:8080` from the new one.
- `docker rename` failing with a name conflict is not a nuisance — it means a
  previous anchor was never cleaned up (step 9). Resolve that before continuing.

**7. Verify. "The container is up" is not the check.** Three checks, in order;
each fails differently:

| check | command | pass |
|---|---|---|
| the intended build is serving | `curl -s localhost:8080/v1/version` | `git_commit` == `$SHA` (ignore `version` — it is `dev` on main images) |
| the process and its dependencies are alive | `curl -s localhost:8080/v1/health` | `{"db_ok":true,"embedding_ok":true,…,"status":"ok"}` — 200 either way, the verdict is in the body |
| **the read path answers** | `curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $KEY" localhost:8080/v1/projects` | `200` |

The third one is why this section exists, and neither of the first two can stand
in for it. `/v1/health`'s `db_ok` is a connection `Ping`, which succeeds against
*any* schema — a binary reading a column the database does not have still
reports `"status":"ok"` while 500ing on real traffic. And `/v1/projects` sits
behind `BearerAuth`: an **unauthenticated call returns 401 without ever touching
the `projects` table**, so a 401 proves nothing. Use a real key.

A 500 whose body carries `SQLSTATE 42703` is exactly the failure the
migrate-first order prevents — roll back (step 8), then find the missing
migration.

Then the three quieter checks:

```bash
docker logs aihub --since 10m 2>&1 | grep -Ei 'error|42703' | head   # expect no output
docker logs aihub --since 10m 2>&1 | grep -F '/ui session key'       # expect the "from ..." line
docker inspect -f '{{.State.StartedAt}}' tei                         # expect it UNCHANGED
```

**That grep prints exactly one line, and the line is the verdict.** Both startup
states carry the string `/ui session key`, on purpose, so there is no state in
which the check is silent:

| line | meaning |
|---|---|
| `aihub: /ui session key from POLYFORGE_UI_COOKIE_SECRET (32 bytes) — sessions survive restarts` | good. The byte count is the resolved key's length, so a truncated env-file value shows up here (`32` for the prescribed `openssl rand -hex 32`; a raw passphrase reports its own length) |
| `warn: /ui session key is EPHEMERAL (…=ephemeral) — random per process; …` | the env-file says `=ephemeral`. The users signed in right now will be signed out by the next deploy |
| *no output* | neither — so you are looking at the wrong container, a rotated log, or a build older than aihub#344 |

**Assert on the line, not on the absence of a warning.** "No warning" is also
what a rotated log and the wrong container produce, which is why both states
print the same greppable tag rather than only the bad one — a check that goes
quiet in the state it exists to catch is not a check. That is gated by
`TestUISessionSurvivesProcessRestart` in `cmd/aihub/ui_cookie_secret_test.go`,
so the two lines cannot drift apart from this table.

This check exists because the three checks in the table above cannot fail on an
ephemeral session key: `/v1/version`, `/v1/health` and the authed read path are
all green while every `/ui` user is being signed out on each deploy
(aihub#344).

`tei` must not have restarted: the swap replaces one container, and a restarted
embedding backend would mean the blast radius was wider than intended.

**8. Roll back** if any check in step 7 fails.

```bash
docker stop aihub
docker rename aihub aihub-failed-$(printf %.7s "$SHA")   # keep it: its logs are the postmortem
docker rename "$ANCHOR" aihub
docker start aihub
curl -s localhost:8080/v1/version    # git_commit back to $PREV
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $KEY" \
  localhost:8080/v1/projects         # 200
```

Renaming the failed container aside rather than deleting it keeps its logs, and
keeps the invariant that the serving container is the one called `aihub`.

Nothing has to be un-migrated for an additive release: on 2026-09-02 the old
binary (`359a435`) ran against schema 32 and stayed healthy — measured on the
day, not assumed. If the release's migration was **not** additive, roll the
schema back too (the `goose … down` invocation is in
[Upgrades & rollback](#10-upgrades--rollback)) or restore the step-1 dump.

**9. Clean up the previous anchor — at the start of the *next* deploy, not the
end of this one.**

```bash
docker ps -a --filter name=aihub-prev- --format '{{.Names}}\t{{.Status}}'
docker rm aihub-prev-<older sha>
```

Keeping exactly one anchor is what makes the `docker rename` in step 6 fail
loudly when someone forgot.

**What the 2026-09-02 run measured** (`359a435` → `b4ed4f5`, one additive
migration). These are observations from that one run — not targets, not an SLO:

| | measured |
|---|---|
| Cloud SQL dump (step 1) | 136 MB |
| Migration `0032` (step 4) | 7.83 ms, applied **before** the container swap |
| Downtime across the swap (step 6) | **0.596 s**, from the 0.1 s poll loop in step 5 — that interval is the number's resolution, so read it as "about 0.6 s" |
| Old binary on the new schema | healthy — which is why the rollback anchor is known to work rather than assumed to |
| `tei` container | not restarted |
| `error` / `42703` in the new container's log | 0 |
| Anchor left behind | `aihub-prev-359a435` |

**Embedding backfill** — only when a release adds or changes embeddings. Run
`aihub-embed-backfill` *on the host* with `DATABASE_URL` and the `EMBEDDING_*`
vars, but **override `EMBEDDING_BASE_URL=http://localhost:8085`**: the value in
`/root/aihub.env` is the Docker-network name `http://tei:80`, which the host
cannot resolve, so a host-run backfill otherwise silently embeds nothing. It is
idempotent (only rows missing a vector for the current model are touched).

### Legacy single-Compose host (`10.146.0.16`, retired)

The earlier shared instance ran server + Postgres via Compose on `10.146.0.16`,
wrapped by `make deploy`. That host has been retired in favour of the Cloud SQL
setup above, and `aihub#341` removed the target and its variables from the
`Makefile` — none of the following still exists. It is recorded because older
notes and scripts refer to it:

| | |
|---|---|
| Host | `10.146.0.16` (`PROD_HOST`) |
| Compose dir on host | `/root/manifests/aihub-v1` (`COMPOSE_DIR`) |
| Image | `us-west1-docker.pkg.dev/devv-404803/public/aihub` (`GCR_IMAGE`) |

```
make deploy, as it was — it pulled :latest, never ran migrations, and did:
  ssh PROD_HOST: docker pull GCR_IMAGE:latest
                 cd COMPOSE_DIR && docker compose up -d --no-deps --force-recreate aihub
```

The current release order is the numbered procedure in
[Current production](#current-production-cloud-sql--bare-docker-run), which is
the only authoritative copy of it.

## Health & version endpoints

| endpoint | use |
|---|---|
| `GET /v1/health` | server + DB + embedding-backend status (body below) |
| `GET /v1/version` | running version, git commit, build time, min client version |
| `GET /ui/` | web console (redirects to `/ui/wi`) |

`GET /v1/health` needs no authentication and answers:

```json
{
  "status": "ok",
  "version": "1.x.x",
  "db_ok": true,
  "embedding_enabled": true,
  "embedding_ok": true
}
```

When a dependency is down, `status` becomes `degraded` and — for the embedding
case — one extra key appears: `"embedding_error_kind": "timeout"` (or
`"unreachable"`). It is absent whenever there is no error to report.

| field | meaning |
|---|---|
| `status` | `ok`, or `degraded` when `db_ok` is false **or** embedding is enabled and its probe failed |
| `db_ok` | the pool answered a `Ping` **within 2 s**. The handler bounds its own context, so a wedged pool reports `false` rather than making the health check hang |
| `embedding_enabled` | `false` when the configured provider is the no-op one, i.e. embedding was never asked for. A dependency nobody asked for is not a degradation |
| `embedding_ok` | the last probe of the embedding backend succeeded; always `true` when embedding is disabled |
| `embedding_error_kind` | `timeout` or `unreachable` — **omitted entirely when empty**. A closed set on purpose: this endpoint is unauthenticated and the raw error names the embedding backend's base URL, so the detail goes to the server's stderr only |

**The HTTP status code is 200 in every branch, `degraded` included. Do not
"fix" this to 503.** Container liveness probes and `polyforge doctor` read this
endpoint's *reachability* as liveness and never look at the body; a 503 because
an **optional** dependency is down would restart a server that is still serving
every request it can — a degraded service turned into a restart loop. Read
`status` from the body, not the status code.

Two limits to know before you act on a green answer:

- **It can be up to 15 s stale.** The embedding probe result is cached with a
  15 s TTL, because this endpoint is polled by container runtimes and by
  `polyforge doctor`, and an uncached probe would put steady load on the very
  backend the check exists to protect. So for up to 15 s after the embedding
  backend dies, `/v1/health` still says `"status":"ok"`. Poll twice, 15 s apart,
  before concluding it is fine.
- **`embedding_ok:true` means "the backend answers", not "vectors are being
  written".** The probe is `Ping`, which embeds the 4-character string `ping`,
  bounded at 2 s. A backend that is up but slow answers that in milliseconds
  while a real 6000-rune embed times out against the 5 s per-call budget. To
  confirm embeddings are actually being written, create a memory and check that
  its row has a vector — not this field.

`polyforge doctor` reads all of the above: its `config` check reports `warn`,
naming the failing dependency and the `embedding_error_kind`, instead of the
bare `[ok] config: aihub reachable` it printed when it only looked at the
status code.
