"""Drop memories.embedding — pgvector carve-out until semantic search is implemented.

v3.0 design §15.2 explicitly defers embedding computation and semantic search;
the column is always NULL. Keeping VECTOR(384) requires the pgvector shared
library at query time (`$libdir/vector`), which crashes every memory endpoint
with UndefinedFileError on a stock postgres:15 image that lacks pgvector.

Remove the column now; re-add it (with a proper pgvector image requirement) when
semantic search is actually implemented.

Revision ID: 0003
Revises: 0002
Create Date: 2026-05-19
"""
from alembic import op


revision = "0003"
down_revision = "0002"
branch_labels = None
depends_on = None


def upgrade():
    op.execute("ALTER TABLE memories DROP COLUMN IF EXISTS embedding")


def downgrade():
    # Requires pgvector extension — only restore when the extension is available.
    op.execute("CREATE EXTENSION IF NOT EXISTS vector")
    op.execute("ALTER TABLE memories ADD COLUMN IF NOT EXISTS embedding VECTOR(384)")
