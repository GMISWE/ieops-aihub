package mcp_test

// aihub#392 — replaying an idempotency_key must leave the client holding a
// credential the server accepts.
//
// The two halves disagreed, each correct in isolation:
//
//	MCP layer      mints a FRESH session_secret on every pf_claim_work_item call
//	               and persists it to the state file (before the request and
//	               again after it).
//	server replay  `SELECT id, claim_epoch FROM run_attempts WHERE
//	               work_item_id=$1 AND idempotency_key=$2` returns the EXISTING
//	               attempt and never touches session_secret_hash. The only
//	               writers of that column are the two INSERTs.
//
// So a retry with the same key ended with the state file holding S2 while the
// server still stored hash(S1), and verifyAttemptCredential then answered
// UNAUTHORIZED "invalid session_secret" for every later call. The trigger is a
// RETRY OF A TIMED-OUT CLAIM — which is the one thing an idempotency key exists
// for. The hazard was already written down, but only inside the state-file-write
// ERROR path, where it is unreachable by anyone whose write succeeded.
//
// ─── Why this test needs a database ────────────────────────────────────────
//
// The defect is a disagreement between the secret this process persists and the
// hash a Postgres row holds, and neither side is observable from the other. A
// fake aihub cannot show it: it has no run_attempts table, so nothing can
// distinguish a replay from a fresh claim and nothing verifies a credential. The
// assertion has to be "an authenticated call SUCCEEDS", and only a real server
// over a real database can answer that.
//
//	AIHUB_TEST_DB='postgres://postgres:…@127.0.0.1:15492/aihub_test?sslmode=disable' \
//	  go test ./internal/mcp/ -run TestE2EClaim -count=1 -v
//
// ─── The three tests are one measurement, not three ────────────────────────
//
//	ReplayKeepsTheSecretTheServerAccepts   the defect. RED on the unfixed tree.
//	WithoutReplayCanAuthenticate           the REFERENCE side. Green on both
//	                                       trees — without it, a red above is
//	                                       equally consistent with "authenticated
//	                                       calls never work in this harness",
//	                                       which is the reference side of a
//	                                       differential measurement lying.
//	WithANewKeyMintsAFreshSecret           the over-reach control. Green on both
//	                                       trees, and it is what fails if the fix
//	                                       reuses a recorded secret for a
//	                                       DIFFERENT key — which would break the
//	                                       ordinary re-claim it must not touch.

import (
	"context"
	"encoding/json"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// tryCall invokes a tool and reports whether it FAILED, instead of fataling on
// failure the way e2eStack.call does. The whole point here is to observe a
// credential refusal (403 ATTEMPT_MISMATCH since aihub#441, 401 UNAUTHORIZED
// before it) rather than to die on it.
func tryCall(t *testing.T, s *e2eStack, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: transport error: %v", tool, err)
	}
	text, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("call %s returned %T, want TextContent", tool, res.Content[0])
	}
	return text.Text, res.IsError
}

// claimStack stands up an isolated workspace (so the claim's state file cannot
// land next to the live one) plus the real DB/router/client/MCP stack, and
// returns a work item ready to claim.
//
// ⚠️ newClaimWorkspace must come FIRST: it sets POLYFORGE_WORKSPACE_ROOT, and
// config.StateDir() — which every pf_* credential lookup goes through — reads
// that variable. Without it the claim below writes into the live workspace's
// state directory, which holds every claimed work item's credentials.
func claimStack(t *testing.T, goal string) (*e2eStack, string) {
	t.Helper()
	newClaimWorkspace(t)
	s := newE2EStack(t)

	_, created := s.call(t, "pf_create_work_item", map[string]any{
		"project":                s.project,
		"goal":                   goal,
		"wi_type":                "fix_bug",
		"requires_human_session": false,
	})
	wiID, _ := created["id"].(string)
	if wiID == "" {
		t.Fatalf("pf_create_work_item returned no id: %v", created)
	}
	return s, wiID
}

// persistedSecret reads back what the claim actually wrote to the state file —
// the artifact this work item is about.
func persistedSecret(t *testing.T, wiID string) string {
	t.Helper()
	sf, err := config.ResolveStateFile(wiID)
	if err != nil {
		t.Fatalf("no state file for %s after a successful claim: %v", wiID, err)
	}
	if sf.SessionSecret == "" {
		t.Fatalf("the state file for %s carries no session_secret; every later pf_* call "+
			"authenticates with it, so an empty one makes the assertions below vacuous", wiID)
	}
	return sf.SessionSecret
}

// authenticatedCalls are the credential-checked surfaces this asserts against.
// Two, not one, because they verify through different code paths — pf_update_step
// through routes_step's VerifyAttemptCredentialPool and pf_emit_event through the
// unexported variant — and a fix that satisfied only one would be a fix to a
// route rather than to the credential.
func authenticatedCalls(wiID string) []struct {
	tool string
	args map[string]any
} {
	return []struct {
		tool string
		args map[string]any
	}{
		{"pf_update_step", map[string]any{
			"work_item_id": wiID, "step_id": "prepare_context", "status": "in_progress",
		}},
		{"pf_emit_event", map[string]any{
			"work_item_id": wiID, "event_type": "note",
			"payload": map[string]any{"text": "aihub#392 replay credential probe"},
		}},
	}
}

// TestE2EClaimReplayKeepsTheSecretTheServerAccepts is THE gate.
//
// It FAILS on the unfixed tree: the replay overwrites the state file with a
// secret the server has never seen, and both authenticated calls come back
// 403 ATTEMPT_MISMATCH "invalid session_secret" (the code was 401 UNAUTHORIZED
// until aihub#441 unified the invalid-credential class; the refusal is the same
// one either way).
func TestE2EClaimReplayKeepsTheSecretTheServerAccepts(t *testing.T) {
	s, wiID := claimStack(t, "aihub#392 replaying an idempotency key must stay authenticable")

	const key = "idem-aihub-392-replay"
	_, first := s.call(t, "pf_claim_work_item", map[string]any{
		"work_item_id": wiID, "idempotency_key": key,
	})
	firstAttempt, _ := first["attempt_id"].(string)
	if firstAttempt == "" {
		t.Fatalf("the first claim returned no attempt_id: %v", first)
	}
	secretAfterFirst := persistedSecret(t, wiID)

	// The replay: byte-identical arguments, which is what a retried call is.
	_, second := s.call(t, "pf_claim_work_item", map[string]any{
		"work_item_id": wiID, "idempotency_key": key,
	})
	secondAttempt, _ := second["attempt_id"].(string)

	// Anti-vacuity: if the server did NOT take its replay branch, this test is
	// not exercising the defect at all and a green result would mean nothing.
	if secondAttempt != firstAttempt {
		t.Fatalf("the replay returned attempt %q, not the first claim's %q — the server did not "+
			"take its idempotency branch, so this test is not measuring the replay path",
			secondAttempt, firstAttempt)
	}

	secretAfterReplay := persistedSecret(t, wiID)
	if secretAfterReplay != secretAfterFirst {
		t.Errorf("the replay rewrote the state file's session_secret while the server kept the " +
			"attempt (and its session_secret_hash) from the first claim, so the credential on disk " +
			"is one the server has never seen. That is aihub#392: the client must persist the secret " +
			"the server ACCEPTS, and a replay is exactly when an idempotency key is used.")
	}

	// The consequence, which is what a caller actually experiences.
	for _, tc := range authenticatedCalls(wiID) {
		text, isErr := tryCall(t, s, tc.tool, tc.args)
		if isErr {
			t.Errorf("after replaying the idempotency key, %s is rejected: %s\n"+
				"Every credential-checked pf_* call is now unusable for this attempt, which is the "+
				"whole cost of aihub#392 — the retry succeeds and the work item becomes unworkable.",
				tc.tool, text)
		}
	}
}

// TestE2EClaimWithoutReplayCanAuthenticate is the REFERENCE side.
//
// Green on both trees, and it must be: without it, the red above is equally
// consistent with "authenticated calls never work in this harness". A
// differential measurement whose reference side is never checked is one
// observation, not two.
func TestE2EClaimWithoutReplayCanAuthenticate(t *testing.T) {
	s, wiID := claimStack(t, "aihub#392 reference side: a single claim authenticates")

	_, claimed := s.call(t, "pf_claim_work_item", map[string]any{
		"work_item_id": wiID, "idempotency_key": "idem-aihub-392-single",
	})
	if claimed["attempt_id"] == nil {
		t.Fatalf("the claim returned no attempt_id: %v", claimed)
	}
	persistedSecret(t, wiID)

	for _, tc := range authenticatedCalls(wiID) {
		text, isErr := tryCall(t, s, tc.tool, tc.args)
		if isErr {
			t.Fatalf("%s is rejected after a SINGLE claim: %s\n"+
				"The harness cannot authenticate at all, so the replay test above proves nothing "+
				"about the replay.", tc.tool, text)
		}
	}
}

// TestE2EClaimWithANewKeyMintsAFreshSecret is the over-reach control.
//
// The fix reuses a recorded secret only when the idempotency_key MATCHES. A
// claim with a NEW key is not a replay: the same-user branch treats it as an
// implicit takeover and issues a fresh attempt bound to the secret THAT call
// generated, so reusing the old secret there would break the ordinary re-claim
// — the mirror-image failure, introduced by the fix rather than by the bug.
//
// Green on both trees; it goes red only if the reuse stops being gated on the
// key. Note what it asserts and what it does not: the secret must CHANGE and the
// authenticated calls must work. It deliberately does not assert a new
// attempt_id as the primary claim, since that is the server's business — the
// anti-vacuity check below states it as the precondition instead.
func TestE2EClaimWithANewKeyMintsAFreshSecret(t *testing.T) {
	s, wiID := claimStack(t, "aihub#392 over-reach control: a new key must mint a new secret")

	_, first := s.call(t, "pf_claim_work_item", map[string]any{
		"work_item_id": wiID, "idempotency_key": "idem-aihub-392-key-a",
	})
	firstAttempt, _ := first["attempt_id"].(string)
	secretAfterFirst := persistedSecret(t, wiID)

	text, isErr := tryCall(t, s, "pf_claim_work_item", map[string]any{
		"work_item_id": wiID, "idempotency_key": "idem-aihub-392-key-b",
	})
	if isErr {
		// Not the point of this test, and not something it should paper over: if
		// a same-user re-claim is refused outright then there is no second
		// attempt to be bound to anything.
		t.Skipf("a second claim under a new key was refused, so there is no fresh attempt to "+
			"check the secret against: %s", text)
	}
	var second map[string]any
	if err := json.Unmarshal([]byte(text), &second); err != nil {
		t.Fatalf("the second claim's output is not JSON: %v (%q)", err, text)
	}
	secondAttempt, _ := second["attempt_id"].(string)
	if secondAttempt == firstAttempt {
		t.Fatalf("a DIFFERENT idempotency key returned the first attempt %q — the server treated "+
			"it as a replay, so this control is not measuring a fresh attempt", firstAttempt)
	}

	if got := persistedSecret(t, wiID); got == secretAfterFirst {
		t.Errorf("a claim under a NEW idempotency key reused the previous secret. The server bound "+
			"the new attempt %q to the secret that call SENT, so the state file now holds a "+
			"credential for the wrong attempt — the mirror image of aihub#392, introduced by "+
			"over-applying its fix. Reuse must be gated on the key matching.", secondAttempt)
	}
	for _, tc := range authenticatedCalls(wiID) {
		text, isErr := tryCall(t, s, tc.tool, tc.args)
		if isErr {
			t.Errorf("after a re-claim under a new key, %s is rejected: %s", tc.tool, text)
		}
	}
}
