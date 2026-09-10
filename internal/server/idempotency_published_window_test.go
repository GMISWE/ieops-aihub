package server

// aihub#543 probe wave 1 — the replay window pf_claim_work_item's card
// publishes, bound to the constant that enforces it.
//
//	"`aihub#436` made `pkg/client` (`setStandardHeaders`) mint an `Idempotency-Key`
//	 HTTP header on every POST/PATCH, which `internal/server/idempotency.go`
//	 (`IdempotencyMiddleware`) turns into a 24h replay of the cached HTTP response,
//	 keyed `<api_key_id>:<key>` and refused with 409 `IDEMPOTENCY_KEY_REUSED` if
//	 the same key arrives with a different `method+target+body`."
//
// Four claims in one sentence, and three already have arms: the header on every
// mutating request (pkg/client's TestMutatingRequestsCarryIdempotencyKey and the
// builder census beside it), the per-API-key scoping
// (TestIdempotency_KeysAreScopedPerAPIKey), and the 409 on a changed request
// (TestIdempotency_ReusedKeyDifferentRequestIsRejected). The FOURTH — the
// number — had none: `idempotencyTTL` is unexported, no test compares it to
// anything, and every test that mentions a window derives it FROM the constant,
// so all of them stay green whatever it says.
//
// 🔴 A published duration nothing checks is the shape aihub#511 was filed on.
// The window decides how long a retried call is answered from cache instead of
// re-executed, so shrinking it to an hour makes the card false for the 23 hours
// where the caller now gets a second real claim attempt — and nothing in this
// repo would have said a word.
//
// The number is READ FROM THE CARD rather than written here, which is the rule
// TestPublishedBaseStrengthRangeIsTheEnforcedOne states for itself: an arm that
// hard-codes "24h" goes green on the day the constant moves, which is the one day
// it was needed.
//
//	GOWORK=off go test ./internal/server/ -run TestPublishedReplayWindow -count=1

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// claimCardForWindow is the card that publishes the window. The claim card is
// the one that names it in prose; pf_force_takeover's synthesises its key
// server-side and says nothing about a window.
const claimCardForWindow = "../../docs/mcp-cards/pf_claim_work_item.md"

// publishedWindowRe reads the duration out of the published sentence. It is
// anchored on "replay", the word the sentence uses for what the window governs,
// so a number that happens to appear elsewhere in the card is not mistaken for
// this one.
var publishedWindowRe = regexp.MustCompile(`(\d+)h replay`)

// TestPublishedReplayWindowIsTheEnforcedOne compares the window the card
// publishes against idempotencyTTL.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M28 enforcement: idempotencyTTL = time.Hour
//	                                     RED  "the card publishes a 24h replay
//	                                          window and the middleware enforces 1h"
//	M29 publication: change the card's "24h replay" to "1h replay"
//	                                     RED  the same comparison, from the other
//	                                          side — which is the half a hard-coded
//	                                          expectation would have missed
//	M30 enforcement: idempotencyTTL = 24*time.Hour + time.Minute — a value that
//	    still ROUNDS to 24h                RED  the equality is on the duration, not
//	                                          on its printed hours
//	M31 control: reword an unrelated sentence of the same card
//	                                     GREEN the arm is bound to the published
//	                                           number, not to card churn
func TestPublishedReplayWindowIsTheEnforcedOne(t *testing.T) {
	raw, err := os.ReadFile(claimCardForWindow)
	if err != nil {
		t.Fatalf("read %s: %v — the published number is the subject of this arm, so an "+
			"unreadable card is a failure rather than a skip", claimCardForWindow, err)
	}

	m := publishedWindowRe.FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatalf("%s no longer publishes a \"<N>h replay\" window. Either the sentence was "+
			"reworded — in which case this arm has to follow it, because it is the only thing "+
			"comparing the two — or the claim was deleted, in which case say so in the card's "+
			"ledger row rather than by leaving this arm reading nothing.", claimCardForWindow)
	}

	var hours int
	if _, err := fmt.Sscanf(m[1], "%d", &hours); err != nil {
		t.Fatalf("parse %q out of the published window %q: %v", m[1], m[0], err)
	}
	published := time.Duration(hours) * time.Hour

	if published != idempotencyTTL {
		t.Errorf("%s publishes a %s replay window and IdempotencyMiddleware enforces %s.\n"+
			"The window is what a caller retrying a POST is promised: inside it the cached "+
			"response comes back and the request is NOT re-executed, outside it the call runs "+
			"again for real. A card that overstates it invites a retry that silently makes a "+
			"second claim; one that understates it invites a needless new idempotency_key, "+
			"which costs an epoch bump and a superseded attempt.",
			claimCardForWindow, published, idempotencyTTL)
	}

	// The floor: the sentence this arm reads must be the sentence about THIS
	// mechanism. Without it a card that came to say "24h replay" about something
	// else entirely would keep the comparison green while the middleware's window
	// went unpublished.
	if !strings.Contains(string(raw), "IdempotencyMiddleware") {
		t.Errorf("%s names a replay window but never names IdempotencyMiddleware, so the "+
			"number above may not be about the middleware this arm compares it with",
			claimCardForWindow)
	}
}
