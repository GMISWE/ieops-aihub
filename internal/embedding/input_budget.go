package embedding

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// DefaultInputMaxRunes is the rune budget applied to text BEFORE it reaches a
// provider. domain.MemoryEmbedInput / domain.WorkItemEmbedInput are its only
// consumers; see internal/domain/embed_input.go.
//
// DERIVED 2026-09-12 (aihub#504) against the production embedding service, not
// inherited. The predecessor value, 6000, was chosen for cmd/aihub-embed-backfill
// and propagated to the live path by aihub#361 for cross-writer parity; its own
// comment admitted it had never been reconciled against any provider's real
// context length. The reconciliation has now been done, and this is it:
//
//   - The provider deployed AT THE TIME OF THAT DERIVATION was
//     text-embeddings-inference 1.7.2 serving Qwen/Qwen3-Embedding-0.6B. Its
//     /info reported max_input_length=32768 tokens, but the EFFECTIVE per-input
//     ceiling was max_batch_tokens=16384:
//     probed 2026-09-12 with length-increasing requests, an input of 16,382
//     tokens embeds in ~1.5s, 16,492 tokens HANGS FOREVER (never schedulable —
//     a single queue entry larger than max_batch_tokens fits in no batch, so
//     the request sits until the client's EMBEDDING_TIMEOUT), and 32,992
//     tokens is rejected immediately with HTTP 413 "must have less than 32768
//     tokens". The hang zone between the two limits is why the margin below
//     is generous rather than tight.
//   - This budget is in runes, the ceiling is in tokens. Measured on the real
//     corpus (every over-budget memories row, 39 rows, mostly Chinese
//     technical markdown, via the provider's own /tokenize): prefix ratios run
//     0.24-0.62 tokens/rune, worst observed 0.620; a pure common-CJK synthetic
//     measures 0.533. 16000 runes at the worst observed ratio is ~9,900
//     tokens — 39% headroom under the 16,384-token ceiling. Crossing the
//     ceiling would take >= 1.024 tokens/rune, outside every sample measured
//     from this corpus; content that adversarial (a measured rare-CJK
//     synthetic hits 1.833) fails to embed after EMBEDDING_TIMEOUT and stores
//     emb_vector NULL — the pre-existing, logged degraded mode, now visible
//     per row via embedded_len (migration 0039).
//
// 🔴 THAT PREMISE IS STALE, AND THE RE-DERIVE TRIGGER THIS COMMENT NAMES HAS
// ALREADY FIRED (recorded 2026-09-14 by aihub#677; NOT re-derived).
//
// The condition written below used to read "if max_batch_tokens is raised to
// match max_input_length, which removes the hang zone entirely — re-run the
// probe and re-derive". aihub#650 then swapped production to
// text-embeddings-inference 89-1.9.3, whose /info reports max_input_length=32768
// AND max_batch_tokens=32768 (plus --auto-truncate false). Source: the aihub#650
// work-item record, attrs.execution_2026_09_14 step 3 — not a probe run for this
// comment. So the hang zone is gone and 16000 is now MORE conservative than it
// was derived to be, not less: the same worst observed 0.620 tokens/rune puts
// 16000 runes at ~9,900 tokens, which is ~70% headroom under 32,768 rather than
// the 39% it had under 16,384. The direction is safe, which is why this is a
// stale premise and not a defect.
//
// 🔴 BEFORE ACTUALLY RE-DERIVING IT, PRICE THE MOVE. This value is stamped into
// every row's provenance: EmbedPipelineDoc (internal/domain/embed_pipeline.go)
// writes `in=<this value>` into the emb_pipeline document segment, and
// cmd/aihub-embed-backfill's predicate compares exactly that segment. Changing
// the number therefore selects EVERY row in both tables for re-embedding, by
// design — memories and work_items alike, thousands of rows, one provider call
// each. That is the correct behaviour (a different budget is a different
// embedding semantics), but it is a deployment-window decision, not a constant
// edit. EMBEDDING_INPUT_MAX_RUNES has the same effect for the same reason.
//
// And whichever value a re-derivation lands on, RAISING THE BUDGET moves BOTH
// writers at once or neither — that is the aihub#361 property, and it belongs to
// the budget, not to the probe.
const DefaultInputMaxRunes = 16000

// InputMaxRunes reads EMBEDDING_INPUT_MAX_RUNES into the embedding input budget.
//
// Unset or unparseable keeps DefaultInputMaxRunes rather than failing startup,
// matching budgetFromEnv: this is a quality knob, and refusing to boot over a
// typo in it would turn degraded recall into an outage. The typo is logged so
// it is findable.
//
// 🔴 Zero and negative are REJECTED, and this is deliberately NOT symmetric
// with EMBEDDING_TIMEOUT, where "0" legitimately means "no bound". "No cap" is
// the exact state aihub#361 existed to remove: an uncapped live writer stores
// emb_vector NULL for any row the provider rejects on length, and produces
// full-text vectors that a capped backfill later overwrites with prefix
// vectors under an identical emb_model. Accepting 0 here would put that defect
// one environment variable away from returning, with no code change to review.
func InputMaxRunes() int {
	raw := strings.TrimSpace(os.Getenv("EMBEDDING_INPUT_MAX_RUNES"))
	if raw == "" {
		return DefaultInputMaxRunes
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"warn: EMBEDDING_INPUT_MAX_RUNES=%q is not an integer (%v) — using %d\n",
			raw, err, DefaultInputMaxRunes)
		return DefaultInputMaxRunes
	}
	if n <= 0 {
		fmt.Fprintf(os.Stderr,
			"warn: EMBEDDING_INPUT_MAX_RUNES=%d is not positive — using %d. There is no "+
				"\"disable the cap\" setting: an uncapped writer is the aihub#361 defect.\n",
			n, DefaultInputMaxRunes)
		return DefaultInputMaxRunes
	}
	return n
}
