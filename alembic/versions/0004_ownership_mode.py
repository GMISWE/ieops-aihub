"""ownership mode: lease_until nullable, last_active_at zombie detection

Revision ID: 0004
Revises: 0003
"""
from alembic import op


def upgrade():
    # H3 fix: lock_timeout guards against claim outage on busy tables.
    # ACCESS EXCLUSIVE lock from DROP NOT NULL is held only for metadata update
    # (no table rewrite in PG 11+), but still blocks concurrent claims.
    op.execute("SET lock_timeout = '2s'")
    op.execute("ALTER TABLE run_attempts ALTER COLUMN lease_until DROP NOT NULL")
    op.execute("RESET lock_timeout")

    # R9 (should): zombie detection column
    op.execute("ALTER TABLE run_attempts ADD COLUMN IF NOT EXISTS last_active_at TIMESTAMPTZ")
    # L1 fix: index predicate matches GC query (status='running' only)
    op.execute("""
        CREATE INDEX IF NOT EXISTS idx_ra_zombie
        ON run_attempts (last_active_at)
        WHERE status = 'running' AND last_active_at IS NOT NULL
    """)


def downgrade():
    op.execute("DROP INDEX IF EXISTS idx_ra_zombie")
    op.execute("ALTER TABLE run_attempts DROP COLUMN IF EXISTS last_active_at")
    # M3 fix: backfill NULL rows before re-adding NOT NULL constraint.
    op.execute("""
        UPDATE run_attempts
        SET lease_until = now() + interval '5 minutes'
        WHERE lease_until IS NULL AND status = 'running'
    """)
    op.execute("SET lock_timeout = '2s'")
    op.execute("ALTER TABLE run_attempts ALTER COLUMN lease_until SET NOT NULL")
    op.execute("RESET lock_timeout")
