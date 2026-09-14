package domain

// The embedding PIPELINE identity stamp (aihub#661).
//
// # WHAT WENT WRONG
//
// memories.emb_model and work_items.emb_model record WHICH MODEL produced a
// vector. They do not record which PIPELINE did, and on 2026-09-13 that gap
// cost two days:
//
//	text-embeddings-inference 1.7.2 ran Qwen/Qwen3-Embedding-0.6B with
//	BIDIRECTIONAL attention; 1.9.3 runs the same checkpoint causally, which is
//	the only way its last-token vector is meaningful. Measured cosine between
//	the two spaces on identical text: 0.139-0.348 (aihub#648, 17/17 documents).
//	Two mutually unusable vector populations — and emb_model was byte-identical
//	across the swap, as was emb_dims.
//
// The direct, paid consequence: cmd/aihub-embed-backfill selects rows whose
// provenance differs from the current one, and NONE of its clauses could see
// the difference. Converging the corpus needed a human to run
//
//	UPDATE memories   SET embedded_len = NULL;   -- 1671 rows
//	UPDATE work_items SET embedded_len = NULL;   -- 2525 rows
//
// by hand first (aihub#650, 2026-09-14), forging a "no provenance" state to
// trick a predicate into selecting rows it had no other way to name.
//
// # WHAT THE STAMP IS
//
// One TEXT column, emb_pipeline, on memories and work_items (migration 0041),
// written by every vector writer at the moment it writes emb_vector — the same
// rule embedded_len follows, for the same reason: a provenance field that can
// be written without a vector eventually claims provenance for a vector that
// does not exist.
//
// Its shape, verbatim:
//
//	doc:m=<model>;d=<dims>;in=<input budget runes>;s=<serving id>|qry:p=<prefix fingerprint>
//
// e.g.
//
//	doc:m=Qwen/Qwen3-Embedding-0.6B;d=1024;in=16000;s=undeclared|qry:p=1f3a9c02bb41
//
// # WHY TWO SEGMENTS, AND WHY ONLY THE FIRST DRIVES RE-EMBEDDING
//
// The doc segment describes how THIS ROW's vector was produced. A change to any
// of its fields means the stored vector is in a different space than a vector
// written now, so the row must be re-embedded — and
// cmd/aihub-embed-backfill's predicate compares exactly this segment, which is
// why it is first and why the separator is fixed.
//
// The qry segment describes how a SEARCH QUERY was composed at the time the row
// was written. It deliberately does NOT drive re-embedding, and that is a
// measured call, not a shortcut: Qwen3-Embedding ships prompts.document = ""
// (empty), so the instruct prefix aihub#669 added is applied to queries and to
// nothing else. Changing it moves the point every QUESTION maps to and leaves
// every stored point exactly where it is — embed_input.go states this and
// aihub#660 measured it. Re-embedding the corpus over a query-side change would
// be work the evidence says is unnecessary; recording it is not, because
// embed_input.go also warns that a single edited byte of QueryEmbedPrefix
// "changes the point every query maps to ... and nothing in the data would show
// it". The fingerprint below is what shows it.
//
// # 🔴 WHAT THE STAMP DOES NOT COVER
//
// Four of its five fields are derived from the running configuration and cannot
// be forgotten: model, dims, input budget, query-prefix fingerprint. The fifth,
// s=, is the operator's declaration of the serving side, and it is the ONLY
// place pooling, attention direction, backend and backend version can appear,
// because aihub cannot observe any of them (see embedding.ServingID).
//
// So: when s=undeclared, THE STAMP DOES NOT COVER POOLING OR ATTENTION
// DIRECTION, and a swap like 2026-09-13's is still invisible. That state is
// written into the data rather than hidden, precisely so it can be selected for
// and fixed. Anyone quoting this stamp as proof that two vectors share a
// pipeline must check the s= field first.
//
// # WHAT OLD ROWS SAY
//
// emb_pipeline IS NULL means "written before this column existed, identity
// unknown" — and it is deliberately NOT backfilled with a guessed value by
// migration 0041, for the reason 0039 gives for embedded_len: the pipeline that
// produced a historical row cannot be reconstructed from the row. Every such
// row is selected by the backfill on its next run and converges to a recorded
// identity; the clause then never matches it again.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/GMISWE/ieops-aihub/internal/embedding"
)

// EmbedPipelineDoc returns the convergence-bearing segment of the stamp: the
// identity of the pipeline that produces STORED-ROW vectors right now.
//
// cmd/aihub-embed-backfill compares this value against the stored stamp's first
// segment, so it is the single definition of "same pipeline" for re-embedding
// purposes. model and dims are passed in rather than read from the provider so
// the value cannot disagree with the emb_model/emb_dims written by the same
// statement.
func EmbedPipelineDoc(model string, dims int) string {
	return fmt.Sprintf("doc:m=%s%sd=%d%sin=%d%ss=%s",
		embedding.SanitizeStampField(model), embedding.StampFieldSep,
		dims, embedding.StampFieldSep,
		embedInputMaxRunes, embedding.StampFieldSep,
		embedding.ServingID())
}

// EmbedPipelineQuery returns the provenance-only segment: a fingerprint of the
// query composition that was live when the row was written.
//
// A fingerprint rather than a hand-maintained version label, because a label is
// a thing a caller can forget to bump and QueryEmbedPrefix is a const somebody
// will one day edit. Hashing the literal bytes cannot be forgotten.
// It reads the prefix through QueryEmbedPrefixID rather than naming the const:
// embed_query_prefix_test.go's census caps the non-test files that may name
// QueryEmbedPrefix at three, and that cap protects a real property (the
// prefixed TEXT must never reach a lexical path). See QueryEmbedPrefixID.
func EmbedPipelineQuery() string {
	return QueryEmbedPrefixID()
}

// queryPrefixFingerprint takes the prefix as an argument rather than reading the
// const, so a test can prove that two different prefixes produce two different
// fingerprints. Reading the const directly would leave the only assertion
// available "this equals the hash of this", which proves the hash is a hash and
// nothing about the property that matters.
//
// 12 hex characters = 48 bits. This identifies a version, it does not resist an
// adversary: the inputs are a handful of hand-written prompt strings over the
// lifetime of the index, not attacker-chosen.
func queryPrefixFingerprint(prefix string) string {
	if prefix == "" {
		// "no prefix" is a reachable state, not an error — embed_input.go notes
		// that reverting aihub#669 needs no migration — and it gets a word
		// rather than sha256("")[:12], which would put the magic constant
		// e3b0c44298fc on rows whose actual property is "there was no prefix".
		return "qry:p=none"
	}
	sum := sha256.Sum256([]byte(prefix))
	return "qry:p=" + hex.EncodeToString(sum[:])[:12]
}

// EmbedPipelineID is the full stamp written to emb_pipeline.
//
// Returned by value; callers store a *string alongside the vector so the column
// stays NULL when no vector was produced.
func EmbedPipelineID(model string, dims int) string {
	return EmbedPipelineDoc(model, dims) + embedding.StampSegmentSep + EmbedPipelineQuery()
}
