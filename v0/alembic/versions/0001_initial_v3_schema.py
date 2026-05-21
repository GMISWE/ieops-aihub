"""initial v3 schema — 6 张表 + 索引 + 触发器 + 循环 FK.

v3-design.md §5 / §5.1 / §5.2 是权威源。本 migration 把它原样落库。

Revision ID: 0001
Revises:
Create Date: 2026-05-16
"""
from alembic import op
import sqlalchemy as sa


revision = "0001"
down_revision = None
branch_labels = None
depends_on = None


def upgrade():
    # pgvector ext
    op.execute("CREATE EXTENSION IF NOT EXISTS vector")

    # touch_updated_at trigger function (用于 work_items.updated_at)
    op.execute("""
    CREATE OR REPLACE FUNCTION touch_updated_at() RETURNS TRIGGER AS $$
    BEGIN
      NEW.updated_at = now();
      RETURN NEW;
    END;
    $$ LANGUAGE plpgsql
    """)

    # 1. users
    op.execute("""
    CREATE TABLE users (
      id              TEXT PRIMARY KEY,
      email           TEXT UNIQUE NOT NULL,
      display_name    TEXT NOT NULL,
      role            TEXT NOT NULL CHECK (role IN ('reader','writer','admin')),
      api_keys        JSONB NOT NULL DEFAULT '[]',
      author_aliases  JSONB NOT NULL DEFAULT '[]',
      projects        JSONB NOT NULL DEFAULT '[]',
      version         BIGINT NOT NULL DEFAULT 0,
      created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
    )
    """)
    op.execute(
        "CREATE INDEX idx_users_api_keys_gin ON users USING gin (api_keys jsonb_path_ops)"
    )

    # 2. work_items (循环 FK 后面 ALTER 加)
    op.execute("""
    CREATE TABLE work_items (
      id                  TEXT PRIMARY KEY,
      project             TEXT NOT NULL,
      scenario            TEXT NOT NULL,
      goal                TEXT NOT NULL,
      status              TEXT NOT NULL
                            CHECK (status IN ('queued','running','blocked','paused','wrapped','failed')),
      priority            TEXT NOT NULL DEFAULT 'normal'
                            CHECK (priority IN ('low','normal','high','urgent')),
      labels              JSONB NOT NULL DEFAULT '[]',
      reporter_user_id    TEXT NOT NULL REFERENCES users(id),
      current_attempt_id  TEXT,
      declared_resources  JSONB NOT NULL DEFAULT '[]',
      resources_version   BIGINT NOT NULL DEFAULT 0,
      external_share_type TEXT
                            CHECK (external_share_type IS NULL
                                  OR external_share_type IN ('jira','github')),
      external_share_key  TEXT,
      parent_work_item_id TEXT REFERENCES work_items(id),
      metadata            JSONB NOT NULL DEFAULT '{}',
      created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
      updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
      closed_at           TIMESTAMPTZ,
      CHECK (external_share_type IS NULL OR external_share_key IS NOT NULL)
    )
    """)
    op.execute("CREATE INDEX idx_wi_project_status ON work_items (project, status)")
    op.execute("CREATE INDEX idx_wi_reporter ON work_items (reporter_user_id)")
    op.execute(
        "CREATE INDEX idx_wi_share ON work_items (external_share_type, external_share_key)"
        " WHERE external_share_type IS NOT NULL"
    )
    op.execute(
        "CREATE INDEX idx_wi_declared_gin ON work_items USING gin (declared_resources jsonb_path_ops)"
    )
    op.execute(
        "CREATE INDEX idx_wi_parent ON work_items (parent_work_item_id)"
        " WHERE parent_work_item_id IS NOT NULL"
    )
    op.execute("""
    CREATE TRIGGER trg_wi_updated_at BEFORE UPDATE ON work_items
      FOR EACH ROW EXECUTE FUNCTION touch_updated_at()
    """)

    # 3. run_attempts
    op.execute("""
    CREATE TABLE run_attempts (
      id                   TEXT PRIMARY KEY,
      work_item_id         TEXT NOT NULL REFERENCES work_items(id),
      status               TEXT NOT NULL
                            CHECK (status IN ('running','superseded','wrapped','failed','expired')),
      claim_epoch          BIGINT NOT NULL,
      idempotency_key      TEXT NOT NULL,
      lease_until          TIMESTAMPTZ NOT NULL,
      actor_user_id        TEXT NOT NULL REFERENCES users(id),
      api_key_id           TEXT NOT NULL,
      actor_display        TEXT NOT NULL,
      machine_id           TEXT NOT NULL,
      session_secret_hash  TEXT NOT NULL,
      parent_attempt_id    TEXT REFERENCES run_attempts(id),
      prepared_workspace   JSONB,
      started_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
      ended_at             TIMESTAMPTZ,
      UNIQUE (work_item_id, idempotency_key),
      UNIQUE (work_item_id, claim_epoch)
    )
    """)
    op.execute("CREATE INDEX idx_ra_wi_status ON run_attempts (work_item_id, status)")
    op.execute("CREATE INDEX idx_ra_actor ON run_attempts (actor_user_id, status)")
    op.execute(
        "CREATE INDEX idx_ra_lease_expired ON run_attempts (lease_until)"
        " WHERE status = 'running'"
    )
    op.execute(
        "CREATE INDEX idx_ra_api_key ON run_attempts (api_key_id)"
        " WHERE status = 'running'"
    )

    # 4. resource_locks (resource_type CHECK 闭集 — 跟 §5/§12.5 RESOURCE_TYPES 同步)
    op.execute("""
    CREATE TABLE resource_locks (
      resource_type    TEXT NOT NULL
                        CHECK (resource_type IN ('git_branch','worktree','file_scope','tcp_port','deploy_env')),
      resource_key     TEXT NOT NULL,
      owner_attempt_id TEXT NOT NULL REFERENCES run_attempts(id) ON DELETE CASCADE,
      claim_epoch      BIGINT NOT NULL,
      acquired_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
      PRIMARY KEY (resource_type, resource_key)
    )
    """)
    op.execute("CREATE INDEX idx_lock_owner ON resource_locks (owner_attempt_id)")

    # 5. agent_events
    op.execute("""
    CREATE TABLE agent_events (
      id              TEXT PRIMARY KEY,
      work_item_id    TEXT NOT NULL REFERENCES work_items(id),
      run_attempt_id  TEXT REFERENCES run_attempts(id),
      actor_user_id   TEXT NOT NULL REFERENCES users(id),
      api_key_id      TEXT,
      event_type      TEXT NOT NULL,
      payload         JSONB NOT NULL DEFAULT '{}',
      pinned          BOOLEAN NOT NULL DEFAULT false,
      created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
    )
    """)
    op.execute("CREATE INDEX idx_ev_wi_time ON agent_events (work_item_id, created_at)")
    op.execute(
        "CREATE INDEX idx_ev_attempt ON agent_events (run_attempt_id)"
        " WHERE run_attempt_id IS NOT NULL"
    )
    op.execute("CREATE INDEX idx_ev_type_time ON agent_events (event_type, created_at)")

    # 6. memories
    op.execute("""
    CREATE TABLE memories (
      id              TEXT PRIMARY KEY,
      project         TEXT NOT NULL,
      author_user_id  TEXT NOT NULL REFERENCES users(id),
      work_item_id    TEXT REFERENCES work_items(id),
      visibility      TEXT NOT NULL DEFAULT 'project'
                        CHECK (visibility IN ('private','project','team','admin')),
      type            TEXT NOT NULL,
      content         TEXT,
      metadata        JSONB NOT NULL DEFAULT '{}',
      embedding       VECTOR(384),
      redacted_at     TIMESTAMPTZ,
      redaction_reason TEXT,
      expires_at      TIMESTAMPTZ,
      created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
    )
    """)
    op.execute(
        "CREATE INDEX idx_mem_project_type ON memories (project, type)"
        " WHERE redacted_at IS NULL"
    )
    op.execute(
        "CREATE INDEX idx_mem_wi ON memories (work_item_id)"
        " WHERE work_item_id IS NOT NULL"
    )
    op.execute("CREATE INDEX idx_mem_author_vis ON memories (author_user_id, visibility)")
    op.execute(
        "CREATE INDEX idx_mem_expires ON memories (expires_at)"
        " WHERE expires_at IS NOT NULL"
    )

    # 循环 FK
    op.execute("""
    ALTER TABLE work_items ADD CONSTRAINT fk_wi_current_attempt
      FOREIGN KEY (current_attempt_id) REFERENCES run_attempts(id) DEFERRABLE INITIALLY DEFERRED
    """)


def downgrade():
    """不写 downgrade — Phase 0 是 forward-only。"""
    raise RuntimeError("v3 0001 initial migration is forward-only; "
                       "use pg_dump+restore for rollback (design §17.3).")
