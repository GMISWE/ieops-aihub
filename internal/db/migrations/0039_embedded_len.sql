-- +goose Up
-- aihub#504: make embedding-input truncation observable. The input budget
-- (internal/embedding/input_budget.go) truncates what is handed to the
-- embedding provider, and until this column the truncation was recorded
-- nowhere: a row whose content exceeds the budget carried a vector of its
-- prefix with nothing on the row saying so, so the writer of a long memory
-- had no way to learn that most of it is invisible to semantic recall.
--
-- embedded_len is the number of runes (Postgres characters) of the composed
-- embed input that the stored emb_vector actually embeds, written by every
-- vector writer at the moment it writes emb_vector. NULL means "no vector"
-- or "vector written before this column existed" — the latter deliberately
-- stays NULL rather than being backfilled by this migration, because the
-- historical cap cannot be reconstructed per row: before aihub#361 the live
-- write path embedded full text while the backfill embedded a 6000-rune
-- prefix, under a byte-identical emb_model. Claiming a length here would
-- manufacture provenance the data does not carry; cmd/aihub-embed-backfill
-- re-embeds rows whose embedded_len IS NULL, which converges the corpus to
-- recorded provenance instead.
ALTER TABLE memories ADD COLUMN IF NOT EXISTS embedded_len INT;
COMMENT ON COLUMN memories.embedded_len IS
    'runes of content the stored emb_vector embeds (aihub#504); NULL = no vector or unrecorded (pre-0039) provenance';

ALTER TABLE work_items ADD COLUMN IF NOT EXISTS embedded_len INT;
COMMENT ON COLUMN work_items.embedded_len IS
    'runes of the goal+content embed input the stored emb_vector embeds (aihub#504); NULL = no vector or unrecorded (pre-0039) provenance';

-- +goose Down
ALTER TABLE memories DROP COLUMN IF EXISTS embedded_len;
ALTER TABLE work_items DROP COLUMN IF EXISTS embedded_len;
