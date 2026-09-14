package embedding

import (
	"fmt"
	"os"
	"strings"
)

// ServingUndeclared is the literal that stands in for the serving-side identity
// when the operator has not declared one.
//
// It is a VALUE, not a zero: it is written into every row's emb_pipeline stamp
// so "nobody declared what was serving this vector" is a fact you can select
// for —
//
//	SELECT count(*) FROM memories WHERE emb_pipeline LIKE '%;s=undeclared|%';
//
// — instead of a silence that reads exactly like a declared pipeline. That
// distinction is the whole point: on 2026-09-13 the serving side changed
// (text-embeddings-inference 1.7.2 -> 1.9.3, bidirectional -> causal attention
// on the same checkpoint, measured cosine 0.139-0.348 between the two spaces)
// with NO aihub-side config change at all, and nothing on any row recorded it.
const ServingUndeclared = "undeclared"

// ServingID returns the operator's declaration of what is serving the
// embeddings, for the emb_pipeline stamp.
//
// # WHY THIS IS DECLARED AND NOT DERIVED — read before trusting it
//
// aihub cannot observe the two properties that actually broke: the pooling
// strategy and the ATTENTION DIRECTION of the forward pass. Neither is in
// aihub's configuration, neither appears in an OpenAI-compatible embeddings
// response, and the only endpoint that reports either (TEI's /info) is not
// portable to the other backend aihub is expected to run against (vLLM, per
// aihub#651). So the stamp cannot derive them; it can only carry what an
// operator writes down.
//
// 🔴 WHAT THAT MEANS THE STAMP DOES NOT COVER. When this returns
// ServingUndeclared, the emb_pipeline stamp does NOT cover pooling, attention
// direction, backend, backend version, or any serving flag, and a serving swap
// is as invisible to the data as it was on 2026-09-13. Saying otherwise would
// be the next false sentence in a chain this work item exists to break
// (aihub#661). What the stamp covers unconditionally is the aihub-side half —
// model, dimensions, input budget, query composition — which is derived from
// the running configuration and cannot be forgotten.
//
// The declaration is therefore a PROCEDURE, recorded in docs/deployment.md:
// changing the tei/vLLM image tag, the model, --pooling, --auto-truncate,
// --max-batch-tokens or any other serving flag REQUIRES bumping
// EMBEDDING_SERVING_ID in the same change. Doing so makes
// cmd/aihub-embed-backfill select every row written under the previous value
// automatically — which is the one thing that had to be done by hand (a manual
// `UPDATE ... SET embedded_len = NULL` over 4196 rows on 2026-09-14) because
// the stamp could not tell the two populations apart.
//
// A missing declaration is warned about once, at construction, rather than
// refused: an absent provenance label degrades provenance, it does not corrupt
// a vector, and refusing to boot over it would turn a recall-quality problem
// into an outage — the same call InputMaxRunes makes for EMBEDDING_INPUT_MAX_RUNES.
//
// Suggested form (free text, no parsing): "tei-1.9.3-lasttoken-causal" —
// backend, version, pooling, attention direction.
func ServingID() string {
	raw := SanitizeStampField(os.Getenv("EMBEDDING_SERVING_ID"))
	if raw == "" {
		return ServingUndeclared
	}
	return raw
}

// warnIfServingUndeclared prints the one-time construction warning. Called from
// FromEnv only for providers that actually write vectors: a NoopProvider stamps
// nothing, so demanding a serving label from it would be noise.
func warnIfServingUndeclared() {
	if ServingID() != ServingUndeclared {
		return
	}
	fmt.Fprintf(os.Stderr,
		"warn: EMBEDDING_SERVING_ID is unset — every vector written by this process "+
			"will be stamped s=%s, and a change to the embedding backend (image tag, "+
			"--pooling, model) will be invisible to cmd/aihub-embed-backfill exactly as "+
			"it was on 2026-09-13 (aihub#661). Set it to something like "+
			"\"tei-1.9.3-lasttoken-causal\" and bump it whenever the serving side changes.\n",
		ServingUndeclared)
}

// The emb_pipeline stamp's two levels of structure, owned here because
// SanitizeStampField has to know them and because the backfill's SQL predicate
// splits on StampSegmentSep. domain.EmbedPipelineID composes with them; nothing
// else may invent a third separator.
const (
	// StampSegmentSep separates the convergence-bearing document segment from
	// the provenance-only query segment.
	StampSegmentSep = "|"
	// StampFieldSep separates key=value fields inside a segment.
	StampFieldSep = ";"
)

// SanitizeStampField makes a free-text value safe to put inside the
// emb_pipeline stamp. A value carrying a segment or field separator would let
// an operator's string forge a segment boundary and make the backfill
// predicate's split_part compare the wrong thing. '=' is left alone: it only
// ever separates a key from its value, and nothing ever splits on it.
func SanitizeStampField(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, StampSegmentSep, "_")
	s = strings.ReplaceAll(s, StampFieldSep, "_")
	return s
}
