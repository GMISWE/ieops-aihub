# Deployment & operations

Operational runbook for the `aihub` server: database migrations, image build,
deploy, rollback, and configuration. For local development see
[`../README.md`](../README.md).

## Where it runs

The team runs a single shared instance via Docker Compose on the prod host:

| | |
|---|---|
| Host | `10.146.0.16` (`PROD_HOST` in the `Makefile`) |
| Compose dir | `/root/manifests/aihub-v1` (`COMPOSE_DIR`) |
| Image | `us-west1-docker.pkg.dev/devv-404803/public/aihub` (`GCR_IMAGE`) |
| Port | `8080` |

The container image is built and pushed by CI (`.github/workflows/ci.yml`) on
every push to `main` (tags: `latest` + the git SHA) and on `v*` tags
(semver). You normally do **not** build images by hand.

## Database migrations

Migrations are plain SQL under `internal/db/migrations/`, run by
[goose](https://github.com/pressly/goose). They are baked into the image at
`/migrations`, so the container can self-migrate.

**Locally** (needs `DATABASE_URL` and a local `goose`):

```bash
make migrate-up      # goose -dir internal/db/migrations postgres "$DATABASE_URL" up
make migrate-down    # roll back the most recent migration
```

**In the container** - pass `migrate-up` as the entrypoint argument
(`docker-entrypoint.sh` runs `goose ... up` then exits; any other args start
the server):

```bash
docker run --rm -e DATABASE_URL="$DATABASE_URL" \
  us-west1-docker.pkg.dev/devv-404803/public/aihub:latest migrate-up
```

Run migrations **before** rolling out a new server build whenever the release
adds or changes a migration. goose is forward-and-back per migration; there is
no automatic pre-deploy safety gate, so on a schema-changing release take a DB
snapshot first.

## Deploy

The `Makefile` target pulls the latest image and recreates the service on the
prod host over SSH:

```bash
make deploy
# ssh PROD_HOST: docker pull GCR_IMAGE:latest
#                cd COMPOSE_DIR && docker compose up -d --no-deps --force-recreate aihub
```

Requirements:

- SSH access to `PROD_HOST`.
- Artifact Registry auth configured on the host
  (`docker login us-west1-docker.pkg.dev`; key
  `~/.gcp/devv-404803-2ab2fee8bad0.json`, SA `artifact-service@devv-404803`).
- A `docker-compose.yml` defining the `aihub` service at `COMPOSE_DIR`.

Typical release order:

1. Merge to `main`; wait for CI to build and push `:latest` (+ the SHA tag).
2. If the release changes migrations, run `migrate-up` against the prod DB
   (container `migrate-up` arg or `make migrate-up`).
3. `make deploy`.
4. Verify: `curl http://10.146.0.16:8080/v1/health` and
   `curl http://10.146.0.16:8080/v1/version`.

## Rollback

Images are tagged by git SHA, so roll back by deploying a previous tag instead
of `:latest`:

```bash
ssh 10.146.0.16 '
  cd /root/manifests/aihub-v1 &&
  docker pull us-west1-docker.pkg.dev/devv-404803/public/aihub:<previous-sha> &&
  IMAGE_TAG=<previous-sha> docker compose up -d --no-deps --force-recreate aihub
'
```

(Adjust to however the compose file selects the tag.) If the bad release also
ran a migration, roll the schema back with `goose ... down` **before** starting
the older binary - an old server against a newer schema may fail. This is why
schema changes should be backward-compatible within a release where possible.

## Configuration

Server environment variables (full table in [`../README.md`](../README.md)):

| env | required | purpose |
|---|---|---|
| `DATABASE_URL` | yes | Postgres DSN. |
| `PORT` | no | Listen port (default `8080`). |
| `POLYFORGE_UI_COOKIE_SECRET` | recommended | HMAC key for `/ui/*` session cookies; set it so sessions survive restarts. |
| `ADMIN_BOOTSTRAP_KEY` | first boot only | Enables `POST /v1/bootstrap` until the first admin exists. |
| `RENDER_MEMORY_TYPES` | no | Memory types pre-rendered to HTML on save. |

### First admin (bootstrap)

On a fresh database the `users` table is empty. Start the server with
`ADMIN_BOOTSTRAP_KEY` set, then:

```bash
curl -X POST http://10.146.0.16:8080/v1/bootstrap \
  -H "X-Bootstrap-Key: $ADMIN_BOOTSTRAP_KEY"
```

The response contains the first admin's API key - store it. The endpoint is
disabled once any user exists; unset `ADMIN_BOOTSTRAP_KEY` afterwards.

## Health checks

| endpoint | use |
|---|---|
| `GET /v1/health` | server + DB connectivity |
| `GET /v1/version` | running version, git commit, build time |
