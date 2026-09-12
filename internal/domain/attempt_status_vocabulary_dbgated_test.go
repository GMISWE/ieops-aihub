package domain

// DB-gated invariant tests for aihub#441 — the three attempt-lifecycle
// vocabulary residues aihub#411's T2-3 row filed.
//
// The row's ruling, verbatim: "One status code for 'invalid attempt credential'
// across all tools; retire `lost` or give it a writer; add the cancelled-attempt
// status the cancel path says it needs."
//
//	(a) 'lost' was in the CHECK with ZERO writers anywhere and no GC path that
//	    assigns any run_attempts.status at all. Retired by migration 0035.
//	(b) No status meant "its work item was cancelled", so a cancelled work item
//	    kept an attempt marked 'paused'. 'cancelled' added by the same migration,
//	    written by CancelWorkItem.
//	(c) One invalid session_secret answered 401 UNAUTHORIZED from
//	    verifyAttemptCredential and 403 ATTEMPT_MISMATCH from
//	    verifyAttemptCredentialSimple. Both now answer 403 ATTEMPT_MISMATCH.
//
// # Why each arm has a control, and what each control rules out
//
// Every claim here has a cheap way to be satisfied by a BROKEN server, so each
// arm carries the negative that separates the two:
//
//   - "'lost' is rejected" is satisfied by a CHECK that rejects everything. The
//     control accepts 'superseded' and 'wrapped' in the same transaction.
//   - "cancel writes 'cancelled'" is satisfied by an UPDATE with no predicate,
//     which would also rewrite attempts that already ended. The control is an
//     already-wrapped attempt on the SAME work item that must keep 'wrapped'.
//   - "a cancelled attempt answers ATTEMPT_MISMATCH" is satisfied by deleting
//     the ATTEMPT_PAUSED branch outright. The control is a paused attempt on a
//     live work item, which must still answer ATTEMPT_PAUSED — the aihub#209
//     property this change must not touch.
//   - "both verifiers answer the same code" is satisfied by both being broken.
//     The control runs the same two paths with the CORRECT secret and requires
//     both to succeed.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:15441/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestAttemptStatusVocabulary' -v -count=1

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// checkViolationSQLState is what Postgres reports for a CHECK constraint
// violation. Asserted by CODE rather than by message text, because the message
// names the constraint and the row and would change with either.
const checkViolationSQLState = "23514"

// setAttemptStatus tries the UPDATE and returns the SQLSTATE, or "" on success.
func setAttemptStatus(t *testing.T, pool *pgxpool.Pool, attemptID, status string) string {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`UPDATE run_attempts SET status=$1 WHERE id=$2`, status, attemptID)
	if err == nil {
		return ""
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("UPDATE run_attempts SET status=%q: want a PgError, got %T: %v", status, err, err)
	}
	return pgErr.Code
}

// TestAttemptStatusVocabulary_TheCheckAcceptsCancelledAndRejectsLost is residue
// (a) and half of (b), asserted where the vocabulary actually lives: the CHECK.
//
// This arm is about the SCHEMA, not about any Go function, and that is
// deliberate. 'lost' had zero Go writers before this change too, so a Go-level
// test of the form "nothing writes 'lost'" was already vacuously green on the
// unfixed tree and would stay green if the value were left in the CHECK
// forever. Only the database can say whether the value is still legal.
func TestAttemptStatusVocabulary_TheCheckAcceptsCancelledAndRejectsLost(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)
	wi := seedWI(t, pool, project, u)
	attemptID := seedRunAttempt(t, pool, wi.ID, u, "aihub441-check-0123456789abcdef0123456789abcdef")

	// The retirement.
	if got := setAttemptStatus(t, pool, attemptID, "lost"); got != checkViolationSQLState {
		t.Errorf("UPDATE ... status='lost' returned SQLSTATE %q, want %q. Migration 0035 retired "+
			"'lost' because it had zero writers anywhere and its only planned writer — design v1.24's "+
			"C-R9-5 zombie sweeper — needs a staleness TTL that v1.21's ownership-only model deleted. "+
			"A database that still accepts it has not run 0035, or 0035 was reverted",
			got, checkViolationSQLState)
	}

	// The addition.
	if got := setAttemptStatus(t, pool, attemptID, "cancelled"); got != "" {
		t.Errorf("UPDATE ... status='cancelled' returned SQLSTATE %q, want success. CancelWorkItem "+
			"writes this value, so a CHECK that rejects it makes every cancel of a claimed work item "+
			"fail with a 500 inside the cancel transaction", got)
	}

	// The controls. Without these, a CHECK narrowed to nothing at all would pass
	// the assertion above.
	for _, keep := range []string{"superseded", "wrapped", "failed", "paused", "running"} {
		if got := setAttemptStatus(t, pool, attemptID, keep); got != "" {
			t.Errorf("CONTROL: UPDATE ... status=%q returned SQLSTATE %q, want success. 0035 was "+
				"supposed to swap ONE value, not narrow the vocabulary", keep, got)
		}
	}
}

// TestAttemptStatusVocabulary_CancelEndsTheLiveAttempt is residue (b) at the
// write site: CancelWorkItem must give the work item's live attempts the
// terminal status the cancel actually produced.
//
// The fixture pauses through FnCompleteAttempt rather than an UPDATE so the
// starting state is one the production path really makes — pause_reason,
// ended_at, the work-item transition and the attempt_completed event all
// written by the real function.
func TestAttemptStatusVocabulary_CancelEndsTheLiveAttempt(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	u := testUser(t, pool)
	project := testProject(t, pool, u)
	wi := seedWI(t, pool, project, u)

	const secret = "aihub441-cancel-0123456789abcdef0123456789abcdef"
	attemptID := seedRunAttempt(t, pool, wi.ID, u, secret)

	// The control attempt: an earlier, already-terminal attempt on the SAME work
	// item. current_attempt_id is deliberately NOT moved to it, so it is exactly
	// the residue a re-claim leaves behind. A cancel must not rewrite its status.
	priorID := NewID("ra")
	mustExec(t, pool, `INSERT INTO run_attempts
		(id, work_item_id, status, claim_epoch, idempotency_key, actor_user_id,
		 actor_display, machine_id, session_secret_hash, ended_at)
		VALUES ('`+priorID+`','`+wi.ID+`','wrapped', 2, 'idem_`+priorID+`','`+u+`','`+u+
		`','m_test','deadbeef', clock_timestamp())`)

	reason := "aihub#441 fixture pause"
	require.Nil(t, FnCompleteAttempt(ctx, pool, wi.ID, &CompleteAttemptRequest{
		AttemptID: attemptID, ClaimEpoch: 1, SessionSecret: secret,
		Status: "paused", PauseReason: &reason,
	}, nil, ""), "precondition: pausing the seeded attempt must succeed")

	var wiStatus, attemptStatus string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT w.status, r.status FROM work_items w JOIN run_attempts r ON r.id=$2 WHERE w.id=$1`,
		wi.ID, attemptID).Scan(&wiStatus, &attemptStatus))
	require.Equal(t, "paused", wiStatus, "precondition: the work item is paused")
	require.Equal(t, "paused", attemptStatus,
		"precondition: the attempt is paused — this is the state that used to survive a cancel")

	require.Nil(t, CancelWorkItem(ctx, pool, wi.ID, u, "member", nil),
		"the reporter may cancel a paused work item (cancelGate)")

	var wiAfter, attemptAfter, priorAfter string
	var endedAtSet bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM work_items WHERE id=$1`, wi.ID).Scan(&wiAfter))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status, ended_at IS NOT NULL FROM run_attempts WHERE id=$1`, attemptID,
	).Scan(&attemptAfter, &endedAtSet))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status FROM run_attempts WHERE id=$1`, priorID).Scan(&priorAfter))

	assert.Equal(t, "cancelled", wiAfter, "precondition for everything below: the cancel landed")
	assert.Equal(t, "cancelled", attemptAfter,
		"the attempt of a cancelled work item must say so. Leaving it 'paused' is what aihub#441 "+
			"removed: 'paused' is the one status whose client contract (aihub#209) is \"keep your "+
			"state file, resume this\", and resume is impossible on a terminal work item")
	assert.True(t, endedAtSet,
		"a terminal status without ended_at is a row that claims to have ended at no particular time; "+
			"every other terminal transition (FnCompleteAttempt, the takeover paths) sets it")

	assert.Equal(t, "wrapped", priorAfter,
		"CONTROL: an attempt that had ALREADY ended keeps the status it earned. The cancel's predicate "+
			"is the retention set IN ('running','paused'), not \"every attempt of this work item\" — "+
			"a predicate-free UPDATE would pass every other assertion in this test while rewriting "+
			"the history of attempts the cancel had nothing to do with")
}

// TestAttemptStatusVocabulary_ACancelledAttemptAnswersMismatchNotPaused is what
// residue (b) is FOR. The status write is not bookkeeping; it changes the answer
// a client gets, and this is the arm that reads that answer.
//
// It goes through VerifyAttemptCredentialPool because that is the real path for
// the tools that can reach this state: routes_step.go (pf_update_step) and
// routes_memory.go call it directly, and unlike FnCompleteAttempt they have no
// work-item-terminal precheck that would answer first and hide the credential
// verdict.
func TestAttemptStatusVocabulary_ACancelledAttemptAnswersMismatchNotPaused(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	u := testUser(t, pool)
	project := testProject(t, pool, u)
	wi := seedWI(t, pool, project, u)

	const secret = "aihub441-verdict-0123456789abcdef0123456789abcde"
	attemptID := seedRunAttempt(t, pool, wi.ID, u, secret)

	reason := "aihub#441 verdict fixture"
	require.Nil(t, FnCompleteAttempt(ctx, pool, wi.ID, &CompleteAttemptRequest{
		AttemptID: attemptID, ClaimEpoch: 1, SessionSecret: secret,
		Status: "paused", PauseReason: &reason,
	}, nil, ""))

	// CONTROL FIRST, while the work item is merely paused. This is the aihub#209
	// property the change must leave alone, and running it before the cancel
	// proves the fixture can produce it at all — an arm that only ever observed
	// the post-cancel answer could not tell "the paused branch is intact" from
	// "the paused branch is gone".
	paused := VerifyAttemptCredentialPool(ctx, pool, wi.ID, attemptID, 1, secret)
	require.NotNil(t, paused, "CONTROL: a paused attempt must still be refused")
	assert.Equal(t, ErrAttemptPaused, paused.Code,
		"CONTROL: a paused attempt on a LIVE work item must still answer ATTEMPT_PAUSED. The client "+
			"keys on this code to KEEP the state file and point the user at resume (aihub#209); "+
			"aihub#441 must not have widened the mismatch class over it")
	assert.Equal(t, 409, paused.HTTPStatus, "CONTROL: ATTEMPT_PAUSED is a 409, not a 403")

	// ── aihub#543 wave 2, lane L11 ────────────────────────────────────────────
	//
	// The two subtests below are additions to this arm rather than a new gated
	// function: both need exactly the states this fixture already produces (a
	// paused attempt, then the same work item cancelled), and spec §4.2's DB rule
	// is to attach as a subtest of a function already in gated_tests.txt and
	// already named by a ci.yml step.
	//
	// They hold two docs/mcp-cards sentences the existing assertions do not
	// reach:
	//
	//	pf_pause_attempt.md, Policy — "`ATTEMPT_PAUSED` … is … still reached
	//	  only by a caller whose secret is VALID, so the unification cannot
	//	  shadow it."
	//	pf_cancel_work_item.md, hop 4 — "…resume is impossible because
	//	  `pf_claim_work_item` refuses a terminal work item."
	//
	// The second sentence is the reason residue (b) exists at all, and it was
	// asserted nowhere: the arm below this comment reads the credential verdict
	// on a cancelled work item, and this file's own message says the old verdict
	// pointed at a resume "FnClaimWorkItem refuses as terminal" — stating the
	// property in prose while nothing executed it.
	//
	// MUTANTS (applied to this tree and run against a scratch Postgres; the
	// verdict is what happened):
	//
	//	M41 enforcement: move the `storedStatus == "paused"` branch in
	//	    verifyAttemptCredential ABOVE the ConstantTimeCompare guard
	//	                                     RED  the paused branch needs a valid
	//	                                          secret (got ATTEMPT_PAUSED for a
	//	                                          wrong one)
	//	M42 enforcement: delete `wi.Status == "cancelled"` from FnClaimWorkItem's
	//	    terminal ladder                  RED  a cancelled work item refuses a
	//	                                          re-claim (the claim SUCCEEDED,
	//	                                          which is the loop with no exit
	//	                                          this card describes)
	//	M43 publication: delete the citation clause from the two card sentences
	//	                                     RED  K12 DEBT_GROWTH
	t.Run("the paused branch needs a valid secret", func(t *testing.T) {
		const wrong = "aihub543-verdict-WRONG-cafebabecafebabecafebabeca"
		wrongOnPaused := VerifyAttemptCredentialPool(ctx, pool, wi.ID, attemptID, 1, wrong)
		require.NotNil(t, wrongOnPaused, "a wrong session_secret must be refused whatever the status")
		assert.Equal(t, ErrAttemptMismatch, wrongOnPaused.Code,
			"a WRONG secret on a PAUSED attempt must answer the invalid-credential code, not "+
				"ATTEMPT_PAUSED. The card's Policy bullet rests on this ordering: the paused branch "+
				"sits below the constant-time comparison, so aihub#441 unifying the credential class "+
				"to 403 ATTEMPT_MISMATCH cannot shadow the 409. Reversed, a caller holding a dead "+
				"secret would be told to keep it and resume — the aihub#209 contract pointed at a "+
				"credential that can never work again")
		assert.Equal(t, 403, wrongOnPaused.HTTPStatus)
		assert.NotContains(t, string(wrongOnPaused.Code), "PAUSED",
			"internal/mcp's classifier keys on the code; a paused-shaped one here keeps a dead file")
	})

	require.Nil(t, CancelWorkItem(ctx, pool, wi.ID, u, "member", nil))

	t.Run("a cancelled work item refuses a re-claim", func(t *testing.T) {
		// The other half of the cancel card's credential argument: the answer a
		// cancelled attempt gives is ATTEMPT_MISMATCH ("re-claim") rather than
		// ATTEMPT_PAUSED ("resume") BECAUSE resume is not available — a claim on a
		// terminal work item is refused. Without this, the redirect the card
		// describes could point at a door that opens.
		//
		// wi_type first, and it is a PRECONDITION rather than tidying: C-R9-6
		// refuses an unclassified work item ahead of the terminal ladder, so
		// without it this arm would observe a WI_TYPE_MISMATCH and say nothing
		// about the branch the card's sentence is about. The seed helper sets no
		// wi_type; the aihub#421 fixture next door does the same thing for the
		// same reason.
		mustExec(t, pool, `UPDATE work_items SET wi_type='fix_bug' WHERE id='`+wi.ID+`'`)

		_, claimErr := FnClaimWorkItem(ctx, pool, wi.ID, &ClaimRequest{
			IdempotencyKey: "aihub543-l11-reclaim",
			SessionInfo: SessionInfo{
				MachineID:     "m_aihub543",
				SessionSecret: "aihub543-reclaim-fedcba9876543210fedcba9876543210fedcba",
			},
		}, u, "", "tester")
		require.NotNil(t, claimErr,
			"claiming a cancelled work item must be refused. If it succeeds, the cancel card's "+
				"whole credential argument inverts: ATTEMPT_PAUSED would have been the honest "+
				"answer after all, because resume WOULD be possible")
		assert.Equal(t, ErrConflictTerminalState, claimErr.Code,
			"the refusal is a STATE conflict — aihub#242's rule, 409 for wrong state — and the "+
				"card names it as the reason a cancelled attempt is handed the re-claim code "+
				"instead of the resume code")
		assert.Equal(t, 409, claimErr.HTTPStatus)
		assert.Contains(t, claimErr.Message, "terminal state",
			"the message must say WHY, or an operator reads a 409 on a re-claim as a lock conflict "+
				"and retries it")
	})

	cancelled := VerifyAttemptCredentialPool(ctx, pool, wi.ID, attemptID, 1, secret)
	require.NotNil(t, cancelled, "a cancelled work item's attempt must not be usable")
	assert.Equal(t, ErrAttemptMismatch, cancelled.Code,
		"a cancelled attempt is a DEAD credential, not a paused one. Before aihub#441 this answered "+
			"ATTEMPT_PAUSED — telling the client to keep its state file and resume a work item "+
			"FnClaimWorkItem refuses as terminal, which is a loop with no exit")
	assert.Equal(t, 403, cancelled.HTTPStatus)
	assert.NotContains(t, string(cancelled.Code), "PAUSED",
		"internal/mcp's classifyStepUpdateErr keys on the substring, so any paused-shaped code here "+
			"makes the client KEEP a credential that can never work again")
	assert.Contains(t, cancelled.Message, `attempt status is "cancelled"`,
		"the message must name the status it found — \"only running attempts can be used\" alone does "+
			"not tell an operator whether the work item was cancelled, wrapped or taken over")
}

// TestAttemptStatusVocabulary_OneCodeForOneInvalidSecret is residue (c): the two
// credential verifiers must answer the SAME thing for the same wrong secret.
//
// The two paths are not a refactor away from each other and are not merged here.
// verifyAttemptCredential runs inside a caller's transaction, enriches an epoch
// mismatch with superseded_by, checks the attempt's status and bumps
// last_active_at; verifyAttemptCredentialSimple is a pool-level check with none
// of that. What T2-3 ruled on is narrower than the functions: for ONE root cause
// — a session_secret that does not match the stored hash — a caller classifying
// by code must not see two problems.
func TestAttemptStatusVocabulary_OneCodeForOneInvalidSecret(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	u := testUser(t, pool)
	project := testProject(t, pool, u)
	wi := seedWI(t, pool, project, u)

	const secret = "aihub441-onecode-0123456789abcdef0123456789abcd"
	const wrong = "aihub441-onecode-WRONG-cafebabecafebabecafebabeca"
	attemptID := seedRunAttempt(t, pool, wi.ID, u, secret)

	emit := func(sessionSecret string) *AihubError {
		_, err := EmitEvent(ctx, pool, &EmitEventRequest{
			WorkItemID: wi.ID, AttemptID: attemptID, ClaimEpoch: 1,
			SessionSecret: sessionSecret, EventType: "note",
			Payload: json.RawMessage(`{"aihub441":"credential uniformity probe"}`),
		}, u, u, "member")
		if err == nil {
			return nil
		}
		var ae *AihubError
		require.True(t, errors.As(err, &ae), "EmitEvent must return an *AihubError, got %T: %v", err, err)
		return ae
	}

	// The two refusals.
	full := VerifyAttemptCredentialPool(ctx, pool, wi.ID, attemptID, 1, wrong)
	simple := emit(wrong)
	require.NotNil(t, full, "verifyAttemptCredential must refuse a wrong session_secret")
	require.NotNil(t, simple, "verifyAttemptCredentialSimple must refuse a wrong session_secret")

	assert.Equal(t, full.Code, simple.Code,
		"ONE root cause, ONE code. verifyAttemptCredential (pf_update_step, pf_complete_attempt, "+
			"pf_wrap, pf_commit, pf_acquire_locks, pf_save_artifact) answered %q and "+
			"verifyAttemptCredentialSimple (pf_emit_event) answered %q for the same wrong secret; "+
			"aihub#411 T2-3 filed exactly that split", full.Code, simple.Code)
	assert.Equal(t, full.HTTPStatus, simple.HTTPStatus,
		"the HTTP status is half the classification — pkg/client renders \"aihub <status> <CODE>\" "+
			"and callers switch on both")
	assert.Equal(t, full.Message, simple.Message,
		"same cause, same sentence: an operator comparing two transcripts must not have to decide "+
			"whether two different wordings mean two different failures")

	// WHICH code, not merely the same one. Equality alone is satisfied by both
	// paths answering 500.
	assert.Equal(t, ErrAttemptMismatch, full.Code,
		"403 ATTEMPT_MISMATCH is the chosen code: aihub#242's rule (409 = wrong state, 403 = wrong "+
			"caller) makes a wrong secret a wrong CALLER, and UNAUTHORIZED is reserved for the "+
			"API-key authentication layer, whose recovery is re-authenticate rather than re-claim")
	assert.Equal(t, 403, full.HTTPStatus)
	for _, ae := range []*AihubError{full, simple} {
		assert.NotContains(t, string(ae.Code), "UNAUTHORIZED",
			"the retired half of the split must not come back on either path")
	}
	assert.True(t, strings.Contains(full.Message, "session_secret"),
		"the message must name which credential failed: the operator's next action differs entirely "+
			"between a bad secret and a superseded attempt")

	// CONTROL: both paths accept the CORRECT secret. Without this the two
	// assertions above are satisfied by a server that refuses everything
	// identically, which is uniform and useless.
	assert.Nil(t, VerifyAttemptCredentialPool(ctx, pool, wi.ID, attemptID, 1, secret),
		"CONTROL: verifyAttemptCredential must accept the real secret")
	assert.Nil(t, emit(secret),
		"CONTROL: verifyAttemptCredentialSimple must accept the real secret")
}
