package mcp

// Force-takeover RESPONSE projection (aihub#422).
//
// This file has no imports: every rule is a map lookup and a delete. That is the
// same deliberate ceiling claim_response_slim.go and list_wi_slim.go set —
// anything needing a parser or a numeric conversion to decide a drop is doing
// more than restating the response.
//
// ─── What was here before ───────────────────────────────────────────────────
//
// The pf_force_takeover handler built its answer as a KEEP-LIST: a fresh map
// holding five hand-named keys.
//
//	prior_attempt_id, prior_actor_display, new_attempt_id, new_claim_epoch, ok
//
// domain.ForceTakeoverResponse has eight JSON fields, so three were dropped in
// silence — and not incidental ones:
//
//	id        the canonical work_items.id
//	slug      the project#seq alias
//	project   the project the wi belongs to
//
// aihub#149 ADDED those three to that struct, with a doc comment saying why: the
// caller may address the wi by slug, and without the canonical id echoed back a
// slug-addressed takeover writes an unmatchable state file. The handler reads all
// three and acts on them — they become the state file's key, Slug and Project —
// and then told the model none of them. So the answer withheld the identity the
// call had just established, and a caller that took over by slug could not even
// name the work_items.id it now owns: the value exists only in a state file the
// model does not read.
//
// ⚠️ An earlier draft of this paragraph justified the fix by saying the caller
// NEEDS the canonical id because "pf_recall and pf_read_events return nothing for
// a slug". That is stale — pf_read_events resolves id-or-slug since aihub#343
// (routes_memory.go's handleListEvents, `f.WorkItemID = &wi.ID`) and pf_recall
// since aihub#363 (resolveRecallWorkItemRef, `WHERE id = $1 OR slug = $1`). The
// sentence survives verbatim in pf_get_step's own tool description, which is
// where it was copied from, and it is wrong there too. The case for forwarding
// does not rest on a downstream consumer at all, which is the point: under a
// delete-list, exposure is the default and nobody has to prove a field is needed.
//
// ─── Why this is a delete-list, and why that is not a stylistic choice ──────
//
// It is the fifth-plus instance of one pattern in this package. recall_slim.go's
// keep-list silently swallowed a newly-added field three times — `total`
// (aihub#249), the truncation pair (aihub#269), `unmatched_types` (aihub#289) —
// which is why list_wi_slim.go (aihub#281) was deliberately built the other way
// round. aihub#388 then found the same shape in the CLAIM handler, registered
// earlier in this same file, and — this is the part worth stopping on — its own
// header QUOTES this handler ("v1.21 ownership-only: no expires_at; do not
// surface that field") while fixing the neighbour, and left this keep-list
// standing. Reading a defect's comment is not the same as auditing the code it
// sits in.
//
// aihub#419's G3 arm found what the reading missed, mechanically: it plants a key
// no struct in this process knows about in the server's answer and asserts it
// reaches the model. Measured on this branch (`go test ./internal/mcp/ -run
// TestContractEveryToolResultPassesThroughAnUnknownServerField -v`): 42 of the 46
// inspected tool results forwarded it before this change and 43 after. The other
// three are pf_commit / pf_push / pf_ship, which compose their own result and are
// listed as such in the gate — so with this change the KEEP_LIST_PROJECTION
// baseline is empty, not merely one line shorter. The wi's own text said "41 of
// 46"; the number was re-measured rather than copied.
//
// ─── The rule ───────────────────────────────────────────────────────────────
//
// Exposure is the default. A key is removed only if it is named below WITH a
// reason. The consumer is a language model: project an API used by code and the
// compiler names who broke; project an API used by a model and the model reads a
// smaller object, concludes the field is absent, and reports that confidently.
//
// 🔴 The direction of the failure changes with the shape, which is the whole
// argument. Under a keep-list the cheapest outcome of the server adding a field
// is that it silently disappears. Under a delete-list the cheapest outcome is
// that it arrives — and the cost is that a key the server should NOT hand a model
// now needs a line here. Exactly one such key exists, and it is the reason a
// projection was written on this route at all: session_secret.
//
// ─── Why this is its own list and not claimResponseWithheldKeys ─────────────
//
// The two routes withhold different things and the difference is not cosmetic.
// The claim list also drops `goal`, because domain.ClaimResponse.Goal exists only
// so the claim can derive the task branch locally and its own doc comment says it
// is not forwarded. ForceTakeoverResponse has no Goal — reusing the claim's list
// here would be a standing licence to drop a field this response does not have
// yet, which is precisely the rot that left `expires_at` in the old claim
// keep-list. A shared list would also make one route's exemption silently govern
// the other's.
//
// ─── Pinned by tests, because the argument is what would rot ────────────────
//
// force_takeover_response_projection_test.go drives the REAL registered tool
// (a pure-function test would be the aihub#309 trap: `if false { safeResult =
// slimForceTakeoverResult(...) }` at the call site is a mutant no such test
// could see):
//
//	TestForceTakeoverResultCarriesEveryResponseField          reflection over the
//	                                                          struct, so a field
//	                                                          added tomorrow is
//	                                                          covered that day
//	TestForceTakeoverResultWithholdsExactlyTheDocumentedKeys  both directions of
//	                                                          the delete-list, plus
//	                                                          the planted and the
//	                                                          live secret
//	TestForceTakeoverResultPassesThroughAFieldTheStruct…      the SHAPE — the one
//	                                                          assertion no complete
//	                                                          keep-list can pass
//	TestForceTakeoverResultReportsTheCredentialsTheState…     the two credential
//	                                                          keys still come from
//	                                                          the state file

// forceTakeoverWithheldKeys names the keys the takeover projection removes, and
// why.
//
// Not spelled map[string]struct{}: the reason IS the payload. A delete-list whose
// entries carry no reason degrades into a keep-list written backwards, because
// the next person cannot tell a deliberate exclusion from a leftover.
var forceTakeoverWithheldKeys = map[string]string{
	// The original purpose of this projection — the handler's comment read
	// "Return result without session_secret."
	//
	// ⚠️ The secret is minted IN THIS PROCESS (generateSessionSecret, a few lines
	// above the call site) and persisted by config.WriteStateFile at mode 0600;
	// domain.ForceTakeoverResponse tags NewSessionSecret `json:"-"` with the note
	// that the client supplied the plaintext, so today's server cannot echo it and
	// this delete is a no-op against it. It is here precisely because a delete-list
	// forwards by default: "the server does not send it" is a property of the other
	// side of an HTTP boundary that versions independently of this binary (which is
	// why /pf-doctor exists), and a projection may not depend on it. Putting the
	// secret in a tool result would paste a live credential into a transcript.
	"session_secret": "a live credential minted in this process and written to the state file at " +
		"mode 0600; it must never reach the model. domain.ForceTakeoverResponse tags " +
		"NewSessionSecret `json:\"-\"`, so the server does not echo it today — this is defence in " +
		"depth against one that starts to.",
}

// slimForceTakeoverResult projects a takeover response IN PLACE and returns the
// SAME map.
//
// Returning the same map, rather than building a new one, is the aihub#249 lesson
// applied structurally rather than remembered: a rebuilt map drops every key
// nobody thought to copy, which is how `total` vanished from pf_recall and how
// `id`/`slug`/`project` vanished from here. Nothing is copied, so nothing can be
// forgotten.
//
// A nil response yields an empty map rather than nil, so the caller can always
// add its own keys to the result.
func slimForceTakeoverResult(result map[string]any) map[string]any {
	if result == nil {
		return map[string]any{}
	}
	for k := range forceTakeoverWithheldKeys {
		delete(result, k)
	}
	return result
}
