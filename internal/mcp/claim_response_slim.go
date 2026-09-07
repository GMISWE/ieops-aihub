package mcp

// Claim RESPONSE projection (aihub#388).
//
// This file has no imports: every rule is a map lookup and a delete. That is a
// deliberate ceiling, the same one list_wi_slim.go sets — anything needing a
// parser or a numeric conversion to decide a drop is doing more than restating
// the response.
//
// ─── Why this is a delete-list, and why that is not a stylistic choice ──────
//
// The claim handler used to build its answer as a KEEP-LIST: a fresh map holding
// `attempt_id`, `claim_epoch` and `ok`, then a copy loop over six named keys.
// Every field of domain.ClaimResponse that nobody remembered to name was dropped
// in silence, and four were:
//
//	requires_human_session   *bool,  no omitempty — ALWAYS in the domain response
//	wi_type                  *string, no omitempty — ALWAYS in the domain response
//	id                       omitempty, non-empty on every real claim
//	step_recovery_hint       omitempty, non-empty whenever there is a hint
//
// The first is the one the post-claim routing rule BRANCHES ON: with
// requires_human_session=false the caller dispatches /pf-execute unattended, with
// true it emits three-segment output and waits for a human. So the response
// omitted precisely the field its caller needs in order to decide what to do
// next, and a cold-starting agent had to spend a second round-trip on
// pf_get_work_item or guess. Measured live 2026-09-07: three real claims
// (aihub#380/#382/#383) and the claim of aihub#388 itself all came back without
// any of the three.
//
// The list had rotted in the other direction too: `expires_at` is not a field of
// ClaimResponse at all — v1.21 removed it, and the pf_force_takeover handler
// thirty lines above says "no expires_at; do not surface that field". So the
// keep-list was faithfully copying a key the response can never have while
// dropping four it always can. That is what an unmaintained keep-list looks like,
// and no test could have told the difference, because both halves of the rot are
// invisible from inside the list.
//
// ⇒ It is instances four through seven of ONE pattern. recall_slim.go's keep-list
// swallowed a newly-added field three times — `total` (aihub#249), the truncation
// pair (aihub#269), `unmatched_types` (aihub#289) — which is exactly why
// list_wi_slim.go (aihub#281) was deliberately built the other way round, its
// header saying "so it cannot become the fourth instance". And the claim
// projection lived in the SAME file as a single-field patch for the same failure:
// aihub#238 pinned `unrecognized_resources` into the keep-list with a comment
// explaining that dropping it makes the whole remedy inert. Somebody recognised
// the hazard, patched one field, and left the machine that produces instances
// running. This file switches the machine off instead.
//
// ─── The rule ───────────────────────────────────────────────────────────────
//
// Exposure is the default. A key is removed only if it is named below WITH a
// reason. The consumer is a language model: project an API used by code and the
// compiler names who broke, project an API used by a model and the model reads a
// smaller object, concludes the field is absent, and reports that confidently.
//
// 🔴 The direction of the failure changes with the shape, which is the whole
// argument. Under a keep-list the cheapest outcome of adding a field is that it
// silently disappears. Under a delete-list the cheapest outcome is that it
// arrives — and the cost is that a key the server should NOT hand a model now
// needs a line here. Exactly one such key exists, and it is the reason this
// projection was written in the first place:
//
//	session_secret — a live credential. See the entry below.
//
// A fix that widened the projection and let that leak would be a textbook case of
// the new thing committing the bug it was built to fix, so it is not left to this
// comment: TestClaimResultWithholdsExactlyTheDocumentedKeys plants a secret in
// the server's answer — a shape the real server never produces, and therefore not
// a property this code may rely on — and fails if it appears anywhere in the
// serialised result, under this key or any other.
//
// ─── Pinned by tests, because the argument is what would rot ────────────────
//
// claim_response_projection_test.go drives the REAL registered tool (a
// pure-function test would be the aihub#309 trap: a mutant one layer away left
// four such tests green while the defect stood, and `if false { safeResult =
// slimClaimResult(...) }` at the call site is such a mutant):
//
//	TestClaimResultCarriesEveryClaimResponseField        reflection over the struct,
//	                                                     so a field added tomorrow
//	                                                     is covered that day
//	TestClaimResultWithholdsExactlyTheDocumentedKeys     both directions of the
//	                                                     delete-list + the secret
//	TestClaimResultPassesThroughAFieldTheStructDoesNot…  the SHAPE — the one
//	                                                     assertion no complete
//	                                                     keep-list can pass
//	TestClaimResultKeepsTheStateFileAuthoritativeKeys    ok / attempt_id / claim_epoch

// claimResponseWithheldKeys names the keys the claim projection removes, and why.
//
// Not spelled map[string]struct{}: the reason IS the payload. A delete-list whose
// entries carry no reason degrades into a keep-list written backwards, because
// the next person cannot tell a deliberate exclusion from a leftover.
var claimResponseWithheldKeys = map[string]string{
	// The original purpose of this projection — the handler's comment read
	// "Don't return session_secret to LLM (decision A)".
	//
	// ⚠️ The secret is minted IN THIS PROCESS and persisted to the state file at
	// mode 0600; the server does not echo it, so on today's server this delete is
	// a no-op. It is here precisely because a delete-list forwards by default:
	// "the server does not send it" is a property of the other side of an HTTP
	// boundary that versions independently of this binary (which is why
	// /pf-doctor exists), and a projection may not depend on it. Putting the
	// secret in a tool result would paste a live credential into a transcript.
	"session_secret": "a live credential minted in this process and written to the state file at " +
		"mode 0600; it must never reach the model. The server does not echo it today, so this " +
		"is defence in depth against one that starts to.",

	// domain.ClaimResponse.Goal's own doc comment states this contract: the field
	// exists so the claim can name the task branch
	// polyforge/<project>-<seq>-<kebab goal> without a second round-trip
	// (aihub#322), and it says "Not forwarded to the LLM by the MCP claim tool".
	// It is mutable work-item content that pf_get_work_item serves, so echoing it
	// on every claim would pay for the goal text to tell the caller something it
	// can already read — and it would go stale against an edited goal.
	"goal": "consumed locally to derive the task branch name (aihub#322) and deliberately not " +
		"forwarded, per ClaimResponse.Goal's own doc comment; pf_get_work_item serves the " +
		"authoritative, non-stale goal.",
}

// slimClaimResult projects a claim response IN PLACE and returns the SAME map.
//
// Returning the same map, rather than building a new one, is the aihub#249 lesson
// applied structurally rather than remembered: a rebuilt map drops every key
// nobody thought to copy, which is how `total` vanished from pf_recall and how
// the four fields above vanished from here. Nothing is copied, so nothing can be
// forgotten.
//
// A nil response yields an empty map rather than nil, so the caller can always
// add its own keys to the result.
func slimClaimResult(result map[string]any) map[string]any {
	if result == nil {
		return map[string]any{}
	}
	for k := range claimResponseWithheldKeys {
		delete(result, k)
	}
	return result
}
