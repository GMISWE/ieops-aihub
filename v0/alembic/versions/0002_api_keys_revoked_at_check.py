"""api_keys.revoked_at presence CHECK constraint.

Hardens the convention that every users.api_keys array element MUST have an
explicit `revoked_at` field (null = active, ISO timestamp = revoked). Without
this constraint the GIN @> predicate in find_user_by_api_key silently
excludes entries that omit the field — a latent way for a future code path
or manual DB edit to render a user undiscoverable.

Pre-migration audit: all current insert paths (tests/v3/fixtures.py +
routes/v3_admin.py admin_add_user / admin_create_key) explicitly set
`revoked_at: None`. No data backfill needed.

The CHECK uses jsonpath `!exists(@.revoked_at)` to find elements LACKING
the field; the constraint asserts no such element exists.

Revision ID: 0002
Revises: 0001
Create Date: 2026-05-16
"""
from alembic import op


# revision identifiers, used by Alembic.
revision = "0002"
down_revision = "0001"
branch_labels = None
depends_on = None


def upgrade():
    op.execute("""
    ALTER TABLE users
    ADD CONSTRAINT users_api_keys_revoked_at_present
    CHECK (NOT jsonb_path_exists(api_keys, '$[*] ? (!exists(@.revoked_at))'))
    """)


def downgrade():
    op.execute("""
    ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_api_keys_revoked_at_present
    """)
