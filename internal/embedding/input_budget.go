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
// 🔴 Where this number came from, stated plainly because the honest answer is
// not flattering: it was chosen for cmd/aihub-embed-backfill, which has to
// survive rows over 300 KB, and aihub#361 then propagated it to the live write
// path so both writers embed the same bytes. Only "there must be SOME cap" was
// forced by the fix; 6000 in particular was INHERITED, not derived. It has
// never been reconciled against any provider's real context length — grep
// internal/embedding for a context-length constant and you will find none,
// because none exists.
//
// The live path therefore gives up headroom it may well have had: before
// aihub#361 it embedded up to whatever the provider actually accepted. That is
// a knowingly conservative trade of recall fidelity for cross-writer parity,
// not a measurement. Re-deriving it needs the configured provider's real
// context length, which is why this is now a knob (EMBEDDING_INPUT_MAX_RUNES)
// instead of a constant: whoever learns that number can raise it without
// touching code, and raising it moves BOTH writers at once or neither.
const DefaultInputMaxRunes = 6000

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
