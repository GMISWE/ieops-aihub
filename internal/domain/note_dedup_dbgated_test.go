package domain

// DB-gated tests for aihub#636: an identical re-send of an attempt's latest
// note records ONCE.
//
// Provenance. pf_complete_attempt's fused note (aihub#290) is emitted as its
// own request BEFORE the completion call, because completing deletes the
// credentials EmitEvent checks — so a completion the server then refuses
// leaves the note on the timeline, and retrying the whole call re-sends it.
// Captured live during the 2026-09-13 deploy (aihub#635): a pf_complete_attempt
// was refused by the aihub#350 derived gate (SQLSTATE 42703 mid-deploy, before
// migration 0040 had run) and the refusal's own suffix admitted the hazard —
// "the closing note WAS already recorded; retrying this call will record it a
// second time". The pf_complete_attempt card said the same thing in its hop-4
// prose: "documented rather than solved".
//
// The fix is server-side ON PURPOSE, in domain.EmitEvent rather than in the
// MCP client's ordering, for two measured reasons:
//
//  1. There is no transaction to reorder. The note and the completion are two
//     independent HTTP requests from the client; FnCompleteAttempt never sees
//     the note. Moving the note write "after all validation" (the wi's option
//     one) means adding note to the completion body — an API change that fixes
//     only clients new enough to use it, and the population that exposes this
//     defect is exactly the OLD clients that predate the aihub#350 client-side
//     pre-refusal (a new client refuses a derived-less wrap before emitting
//     the note at all).
//  2. Even a new client re-duplicates on any server-side refusal that fires
//     after the note landed: an unresolvable filed:<ref>, a retryable 40001
//     (whose classified 409 explicitly invites the retry), or the mid-deploy
//     42703 that was captured live. Dedup at the write covers every one of
//     those, whatever the client's vintage.
//
// Scope, pinned by the third test below: the dedup target is the LATEST note
// of the same attempt, compared as jsonb payload + pinned flag. A straight
// retry has nothing between the two sends, so it always matches; a deliberate
// re-emission of an earlier text after other notes still records (A, B, A
// stays three events).
//
// MUTANTS (applied to this tree and run 2026-09-13; the verdict is what
// happened, re-run to re-measure):
//
//	M1 remove the fix: short-circuit the dedup block in EmitEvent
//	   (internal/domain/memory.go, the `req.EventType == "note"` guard before
//	   the INSERT, neutralised with `false &&`)
//	                                 RED  ARetriedRefusedCompletionRecordsThe
//	                                      NoteOnce — count(note)=3 after the two
//	                                      retries and the dedup id assertion
//	                                      fails first, the exact aihub#635
//	                                      double-note; the other two tests stay
//	                                      green, which is correct (they pin
//	                                      behaviour the mutant does not change).
//	M2 over-widen: dedupe against ANY prior note of the attempt (move the
//	   payload/pinned equality into WHERE, drop ORDER BY created_at DESC)
//	                                 RED  ADeliberateReemissionAfterAnotherNote
//	                                      StillRecords — A, B, A lands 2 events
//	                                      instead of 3; the other two green.
//	M3 blunt the equality: make the SELECT's match verdict constant-true while
//	   keeping the latest-note scope RED  ADifferentNoteStillRecords (the
//	                                      second, different note is swallowed)
//	                                      and ADeliberateReemissionAfterAnother
//	                                      NoteStillRecords (B is swallowed
//	                                      behind A); the acceptance test alone
//	                                      stays green, so neither control is
//	                                      redundant with it.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:15636/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run TestNoteDedup -v -count=1

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noteDedupFixture seeds a work item with a RUNNING attempt — the state the
// fused note is emitted from — and returns the ids the emit and the completion
// need. Unlike pausedAttemptFixture it does not pause: the aihub#635 capture
// happened on a live attempt whose completion was refused, so the fixture must
// leave the attempt where a refusal would.
func noteDedupFixture(t *testing.T, pool *pgxpool.Pool) (wiID, attemptID, secret, caller string) {
	t.Helper()
	caller = testUser(t, pool)
	project := testProject(t, pool, caller)
	wi := seedWI(t, pool, project, caller)
	secret = "aihub636-dedup-0123456789abcdef0123456789abcdef0123456789ab"
	attemptID = seedRunAttempt(t, pool, wi.ID, caller, secret)
	return wi.ID, attemptID, secret, caller
}

// emitNoteAs sends one note event through the production EmitEvent path and
// returns the full verdict: the event id, whether the server deduplicated, and
// the error.
func emitNoteAs(t *testing.T, pool *pgxpool.Pool, wiID, attemptID, secret, caller, text string) (string, bool, error) {
	t.Helper()
	return EmitEvent(context.Background(), pool, &EmitEventRequest{
		WorkItemID:    wiID,
		AttemptID:     attemptID,
		ClaimEpoch:    1,
		SessionSecret: secret,
		EventType:     "note",
		Payload:       json.RawMessage(`{"text":` + string(mustJSONString(t, text)) + `}`),
	}, caller, caller, "member")
}

func mustJSONString(t *testing.T, s string) []byte {
	t.Helper()
	b, err := json.Marshal(s)
	require.NoError(t, err)
	return b
}

func countNotes(t *testing.T, pool *pgxpool.Pool, wiID string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_events WHERE work_item_id=$1 AND event_type='note'`, wiID).Scan(&n))
	return n
}

// TestNoteDedup_ARetriedRefusedCompletionRecordsTheNoteOnce drives the exact
// aihub#635 sequence an old client produces against the server: emit the note,
// have the completion refused by the aihub#350 derived gate, and retry the
// whole call twice. The wi's acceptance criterion is this test's final
// assertion verbatim: after two retries of the same refusal, the timeline
// holds ONE note. Removing the dedup (mutant M1 in the header) reproduces the
// double note this wi was filed on.
func TestNoteDedup_ARetriedRefusedCompletionRecordsTheNoteOnce(t *testing.T) {
	pool := setupLatestTestDB(t)
	wiID, attemptID, secret, caller := noteDedupFixture(t, pool)

	const note = "wrapped: the aihub#636 closing note a refused completion strands"

	// First attempt at the fused call: the note lands, then the completion is
	// refused. Derived is nil — the pre-aihub#350 wire shape, which only an old
	// client can still send (a new client refuses it before emitting the note).
	firstID, deduplicated, err := emitNoteAs(t, pool, wiID, attemptID, secret, caller, note)
	require.NoError(t, err, "the first emit is an ordinary note and must land")
	require.False(t, deduplicated, "the first emit has nothing to deduplicate against")
	require.Equal(t, 1, countNotes(t, pool, wiID), "precondition: the first note is on the timeline")

	refusal := FnCompleteAttempt(context.Background(), pool, wiID, &CompleteAttemptRequest{
		AttemptID:     attemptID,
		ClaimEpoch:    1,
		SessionSecret: secret,
		Status:        "wrapped",
		Derived:       nil,
	}, nil, "")
	require.NotNil(t, refusal, "precondition: a derived-less wrap must be refused (aihub#350)")
	require.Contains(t, refusal.Message, "derived is required",
		"the refusal this test retries must be the derived gate, not some fixture accident")

	// Two straight retries of the whole fused call, which is the caller's only
	// recovery: each re-sends the identical note before the completion is
	// refused again.
	for retry := 1; retry <= 2; retry++ {
		retryID, deduplicated, err := emitNoteAs(t, pool, wiID, attemptID, secret, caller, note)
		require.NoError(t, err, "retry %d: an identical re-send must succeed, not error", retry)
		assert.True(t, deduplicated, "retry %d: the server must report the re-send as deduplicated", retry)
		assert.Equal(t, firstID, retryID,
			"retry %d: the id returned for a deduplicated re-send must name the event that already carries the note", retry)

		refusal := FnCompleteAttempt(context.Background(), pool, wiID, &CompleteAttemptRequest{
			AttemptID:     attemptID,
			ClaimEpoch:    1,
			SessionSecret: secret,
			Status:        "wrapped",
			Derived:       nil,
		}, nil, "")
		require.NotNil(t, refusal, "retry %d: the completion is refused again", retry)
	}

	assert.Equal(t, 1, countNotes(t, pool, wiID),
		"the aihub#636 acceptance criterion: the same refusal retried twice leaves ONE note on the "+
			"timeline. Two or more is the aihub#635 double note back again - check the dedup block in "+
			"EmitEvent (internal/domain/memory.go)")
}

// TestNoteDedup_ADifferentNoteStillRecords is the over-suppression control:
// dedup must fire on an identical re-send and on nothing else. Without this,
// the test above is equally consistent with "the server stopped recording
// notes after the first".
func TestNoteDedup_ADifferentNoteStillRecords(t *testing.T) {
	pool := setupLatestTestDB(t)
	wiID, attemptID, secret, caller := noteDedupFixture(t, pool)

	firstID, deduplicated, err := emitNoteAs(t, pool, wiID, attemptID, secret, caller, "first statement")
	require.NoError(t, err)
	require.False(t, deduplicated)

	secondID, deduplicated, err := emitNoteAs(t, pool, wiID, attemptID, secret, caller, "second, different statement")
	require.NoError(t, err, "a note with different text is a new statement and must record")
	assert.False(t, deduplicated, "different text must not be reported as a duplicate")
	assert.NotEqual(t, firstID, secondID, "a recorded second note must be its own event")

	assert.Equal(t, 2, countNotes(t, pool, wiID),
		"two DIFFERENT notes are two timeline events - dedup is for identical re-sends only")
}

// TestNoteDedup_ADeliberateReemissionAfterAnotherNoteStillRecords pins the
// latest-only scoping: the dedup target is the attempt's most recent note, so
// A, B, A is three events. The straight retry the fix exists for has nothing
// between its two sends and always matches; re-stating an earlier text after
// other notes is a new statement in a new position on the timeline, and
// swallowing it would trade the aihub#635 defect for a quieter one.
func TestNoteDedup_ADeliberateReemissionAfterAnotherNoteStillRecords(t *testing.T) {
	pool := setupLatestTestDB(t)
	wiID, attemptID, secret, caller := noteDedupFixture(t, pool)

	const textA = "checkpoint: build green"
	idA1, dedupA1, err := emitNoteAs(t, pool, wiID, attemptID, secret, caller, textA)
	require.NoError(t, err)
	require.False(t, dedupA1)

	_, dedupB, err := emitNoteAs(t, pool, wiID, attemptID, secret, caller, "interlude: switched branches")
	require.NoError(t, err)
	require.False(t, dedupB)

	idA2, dedupA2, err := emitNoteAs(t, pool, wiID, attemptID, secret, caller, textA)
	require.NoError(t, err,
		"re-stating an earlier text after another note must record - only the LATEST note is a dedup target")
	assert.False(t, dedupA2, "A, B, A: the second A is not a re-send of the latest note")
	assert.NotEqual(t, idA1, idA2, "the re-stated note must be its own event, not the old id replayed")

	assert.Equal(t, 3, countNotes(t, pool, wiID),
		"A, B, A is three timeline events - the dedup window is exactly the attempt's latest note")

	// The chronology a reader sees must be A, B, A - the point of recording the
	// re-statement at all.
	rows, err := pool.Query(context.Background(),
		`SELECT payload->>'text' FROM agent_events
		 WHERE work_item_id=$1 AND event_type='note' ORDER BY created_at`, wiID)
	require.NoError(t, err)
	defer rows.Close()
	var texts []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		texts = append(texts, s)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{textA, "interlude: switched branches", textA}, texts,
		"the timeline must read in emission order, with the re-statement last: "+strings.Join(texts, " | "))
}
