package domain

// DB-gated end-to-end arm for aihub#585 (owner ruling ②, 2026-09-10): a PAUSED
// attempt retains the right to write timeline events.
//
// Provenance. aihub#583 measured the asymmetry — pf_emit_event's verifier,
// verifyAttemptCredentialSimple, reads the current attempt id, the claim epoch
// and the secret hash and never the attempt's status, so a pause revokes nothing
// it checks — and corrected the pf_pause_attempt card, which had claimed a pause
// hard-rejects "every credential-checked pf_* call". Whether the BEHAVIOUR should
// change (should a paused attempt be refused, aligning the two verifiers?) was
// filed as aihub#585 and ruled by the owner on 2026-09-10: option ② — KEEP it,
// and write it into the contract. The grant is load-bearing in practice: pausing
// hands a wi to a human, and the note that says why often lands after the pause —
// the 2026-09-10 close-out paused aihub#543 and then wrote its checkpoint note
// through exactly this path.
//
// Division of labour with the aihub#583 census
// (paused_refusal_scope_test.go, TestOnlyOneCredentialVerifierRefusesAPausedAttempt):
// the census is a SOURCE walk — it holds that exactly one credential verifier
// answers ErrAttemptPaused and that it is not the Simple one — so it pins the
// population but never executes a call. This arm is the other half: it drives a
// real pause through the production FnCompleteAttempt path and then a real
// EmitEvent against a real database, so "retains the right" is observed as a
// row on the timeline rather than inferred from the absence of a branch. The
// wrong-secret control keeps the observation honest — without it, the positive
// half is equally consistent with "EmitEvent stopped checking credentials".
//
// Both card bullets that state the grant — pf_pause_attempt.md hop 4 and
// pf_emit_event.md hop 4 — cite this test and the census as their pins.
//
// MUTANTS (applied to this tree and run 2026-09-10; the verdict is what happened):
//
//	M1 enforcement: add a status read + ErrAttemptPaused return to
//	   verifyAttemptCredentialSimple (unify the verifiers)
//	                                      RED  the_paused_attempts_own_credentials
//	                                           _still_emit — and the aihub#583
//	                                           census goes red in the same run
//	                                           (exactly_one_verifier…), which is
//	                                           the designed pairing: unifying now
//	                                           contradicts an owner ruling, and
//	                                           both the behavioural and the
//	                                           structural pin say so at once.
//	M2 enforcement: neutralise the secret-hash comparison in
//	   verifyAttemptCredentialSimple       RED  a_wrong_secret_on_the_same_paused
//	                                           _attempt_is_refused — the control
//	                                           that separates "paused attempts
//	                                           keep their rights" from "EmitEvent
//	                                           stopped authenticating".
//	M3 publication: delete the grant bullet from docs/mcp-cards/pf_emit_event.md
//	                                      RED  K12 (contract_cards_gate_test.go)
//	                                           — the ledger row pins the card's
//	                                           candidate/cited counts.
//	M4 publication: delete this test's citation from the pf_pause_attempt.md
//	   grant bullet                        RED  K12 — the sentence loses its arm
//	                                           and lands in UNCLASSIFIED, whose
//	                                           ceiling is 0.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:15585/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run TestPausedAttemptStillWritesTimelineEvents -v -count=1

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPausedAttemptStillWritesTimelineEvents(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	// The fixture pauses through the production path (FnCompleteAttempt with
	// status="paused"), so ended_at, the work-item transition and the
	// attempt_completed event are all real — see its own doc comment.
	wiID, attemptID, secret := pausedAttemptFixture(t, pool)

	// caller is resolved OUTSIDE the subtests: testUser derives the id from
	// t.Name(), and the fixture already seeded this exact user at the parent
	// name, so calling it here is an idempotent lookup. Inside a subtest it
	// would mint a different user.
	caller := testUser(t, pool)

	// Precondition, asserted rather than assumed: the attempt this test emits
	// from IS paused, on both records a reader would check. Without this, a
	// fixture drift that left the attempt running would turn the positive half
	// into a test of the ordinary running-attempt path.
	var attemptStatus, wiStatus string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT ra.status, wi.status FROM run_attempts ra
		 JOIN work_items wi ON wi.id = ra.work_item_id WHERE ra.id = $1`,
		attemptID).Scan(&attemptStatus, &wiStatus))
	require.Equal(t, "paused", attemptStatus,
		"precondition: the fixture must leave run_attempts.status='paused'")
	require.Equal(t, "paused", wiStatus,
		"precondition: the fixture must leave work_items.status='paused'")

	emit := func(sessionSecret, text string) (string, error) {
		return EmitEvent(ctx, pool, &EmitEventRequest{
			WorkItemID:    wiID,
			AttemptID:     attemptID,
			ClaimEpoch:    1,
			SessionSecret: sessionSecret,
			EventType:     "note",
			Payload:       json.RawMessage(`{"text":"` + text + `"}`),
		}, caller, caller, "member")
	}

	t.Run("the_paused_attempts_own_credentials_still_emit", func(t *testing.T) {
		evtID, err := emit(secret, "aihub585 checkpoint note recorded after the pause")
		require.NoError(t, err,
			"a paused attempt's own credential triple must still emit events — owner ruling ② "+
				"(2026-09-10, aihub#585) keeps this grant BY DESIGN. If this now answers "+
				"ATTEMPT_PAUSED, the verifiers were unified against that ruling; see the "+
				"aihub#583 census (TestOnlyOneCredentialVerifierRefusesAPausedAttempt), which "+
				"is red in the same run and says what a compliant diff must carry.")
		require.NotEmpty(t, evtID)

		// Durability, which is the point of the grant: the note is ON the
		// timeline, attributed to the paused attempt, not merely a 200.
		var evtType string
		var evtAttempt *string
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT event_type, run_attempt_id FROM agent_events
			 WHERE id = $1 AND work_item_id = $2`, evtID, wiID).Scan(&evtType, &evtAttempt))
		assert.Equal(t, "note", evtType)
		require.NotNil(t, evtAttempt,
			"the event row must carry the attempt that wrote it — a NULL here would make the "+
				"timeline unable to say the note came from the paused attempt at all")
		assert.Equal(t, attemptID, *evtAttempt,
			"the timeline row must be attributed to the PAUSED attempt whose credentials wrote it")
	})

	t.Run("a_wrong_secret_on_the_same_paused_attempt_is_refused", func(t *testing.T) {
		// The control that gives the positive half its meaning: the grant is for
		// the paused attempt's OWN credentials, not for anyone naming its id.
		const wrong = "aihub585-WRONG-cafebabecafebabecafebabecafebabecafebabecafe"
		_, err := emit(wrong, "must never land")
		require.Error(t, err,
			"a wrong secret must still be refused on a paused attempt — without this, the "+
				"positive half above is equally consistent with EmitEvent having stopped "+
				"checking credentials altogether")
		var ae *AihubError
		require.True(t, errors.As(err, &ae), "EmitEvent must return an *AihubError, got %T: %v", err, err)
		assert.Equal(t, ErrAttemptMismatch, ae.Code,
			"the refusal must be the aihub#441 credential-class code, ATTEMPT_MISMATCH — a "+
				"paused-shaped code here would tell the caller to keep and resume a credential "+
				"that can never work")
		assert.Equal(t, 403, ae.HTTPStatus)

		var landed int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM agent_events
			 WHERE work_item_id = $1 AND payload->>'text' = 'must never land'`, wiID).Scan(&landed))
		assert.Equal(t, 0, landed, "the refused emit must write nothing to the timeline")
	})
}
