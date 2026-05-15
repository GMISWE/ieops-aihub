# ieops-mem

Lightweight self-hosted shared memory service for polyforge-v2.

- FastAPI + SQLite WAL + fastembed (all-MiniLM-L6-v2 via ONNX) + numpy cosine
- Daily encrypted backup to a private GitHub repo via APScheduler + pyrage
- Project-scoped HMAC-SHA256 API key auth, three roles (reader/writer/admin)

Full spec: see `.workspace/memory/local/spec-ieops-mem-v010.md` in the
polyforge workspace that owns this repo.

## Quick start

```bash
docker run -d --name ieops-mem \
  -p 8765:8765 \
  -v $(pwd)/data:/data \
  -e ADMIN_API_KEY=<bootstrap-admin-key> \
  -e HASH_SECRET="$(openssl rand -hex 32)" \
  -e GITHUB_TOKEN=<pat> \
  -e BACKUP_REPO=GMISWE/ieops-mem-backup \
  -e BACKUP_ENCRYPT_KEY=<age-public-key> \
  ghcr.io/gmiswe/ieops-mem:latest
```

`HASH_SECRET` must be ≥ 32 bytes; the service refuses to start otherwise.

## Deploying

For MINOR/MAJOR releases (e.g. v0.3.0), always use:

    make deploy-safe

This snapshots `/opt/ieops-mem/data/ieops-mem.db` to
`/opt/ieops-mem/snapshots/pre-v$(VERSION)-<ts>.db` on the deploy host
before pulling and starting the new image. Rollback is then a 3-line
restore (see "Rollback" below).

For patch releases or rapid iteration:

    SKIP_PREDEPLOY_SNAPSHOT=1 make deploy

## Rollback (v0.3.0 → v0.2.x)

The v0.3.0 startup migration is forward-only — older images cannot
read the new schema. To roll back:

```bash
make redeploy TAG=20260515-f8d012f   # any pre-v0.3.0 image tag
ssh 10.146.0.16 "sudo docker stop ieops-mem \
  && sudo cp /opt/ieops-mem/snapshots/pre-v0.3.0-<ts>.db /opt/ieops-mem/data/ieops-mem.db \
  && sudo rm /opt/ieops-mem/data/ieops-mem.db-wal /opt/ieops-mem/data/ieops-mem.db-shm \
  && sudo docker start ieops-mem"
curl -sS http://10.146.0.16/health   # expect: version < 0.3.0
```

- **RTO ≤ 5 min** (snapshot restore + container restart).
- **RPO** = age of the pre-deploy snapshot (taken immediately before
  the migration; effectively 0 for routine releases).

## Backup restore runbook

Backups land at `backups/ieops-mem-<UTC-timestamp>.db.age` on the
`BACKUP_REPO` `backups` branch (override via `BACKUP_BRANCH`). They are
encrypted with `age` using the X25519 public key in `BACKUP_ENCRYPT_KEY`.

Restoring on a fresh host:

```bash
# 1. Stop the running service (if any) — restore must not race writes
docker stop ieops-mem

# 2. Pull the encrypted snapshot from the backup repo
gh api repos/GMISWE/ieops-mem-backup/contents/backups/ieops-mem-20260515T020000Z.db.age \
   --jq '.content' | base64 -d > snapshot.db.age

# 3. Decrypt with the matching age identity (private key)
age --decrypt -i /path/to/age-identity.key -o snapshot.db snapshot.db.age

# 4. Sanity-check integrity before swapping it into /data
sqlite3 snapshot.db "PRAGMA integrity_check;"     # expect: ok
sqlite3 snapshot.db "SELECT COUNT(*) FROM memories;"
sqlite3 snapshot.db "SELECT COUNT(*) FROM access;"

# 5. Atomic swap (volume-mounted /data on host)
mv ./data/ieops-mem.db ./data/ieops-mem.db.broken
mv snapshot.db ./data/ieops-mem.db
rm -f ./data/ieops-mem.db-wal ./data/ieops-mem.db-shm  # WAL artefacts from old run

# 6. Restart and verify
docker start ieops-mem
curl -sS localhost:8765/health
```

The decrypted snapshot is a regular SQLite file produced via the SQLite
Online Backup API — safe to open with the standard `sqlite3` CLI even if
the source was under heavy WAL activity at backup time.

## API surface

| Endpoint                              | Role   | Notes |
|---------------------------------------|--------|-------|
| `POST   /memories`                    | writer | sync embedding on write |
| `GET    /memories`                    | reader | filters: type, status, max_age_days, external_id, include_deprecated |
| `GET    /memories/:id`                | reader | |
| `PUT    /memories/:id`                | writer | metadata merges; re-embeds on content change |
| `DELETE /memories/:id`                | writer | hard delete |
| `PUT    /memories/:id/deprecate`      | writer | soft delete; sets reason + superseded_by |
| `POST   /memories/search`             | reader | cosine + recency boost |
| `POST   /admin/access`                | admin  | upserts (key_hash, project) |
| `GET    /admin/access?project=`       | admin  | |
| `DELETE /admin/access/:key_id/:project` | admin | |
| `GET    /health`                      | none   | db + model status |

## Development

```bash
pip install ".[test]"
pytest tests/ -v
```

Tests mock the embedder and backup scheduler — fastembed and pyrage do
not need to be exercised locally to run the suite.
