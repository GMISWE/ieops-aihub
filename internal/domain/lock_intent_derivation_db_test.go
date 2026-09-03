package domain

// DB-gated integration tests for aihub#342: every place that DERIVES a lock
// from a declared resource must apply the same intent rule.
//
// The contract, carried by one string — declaredResourcesProp in
// internal/mcp/tools_lifecycle.go — and advertised on pf_predict_conflicts,
// pf_update_work_item, pf_create_work_item and pf_batch_create_work_items
// (pf_claim_work_item takes no declared_resources argument, so it does NOT
// carry it):
//
//	only two values carry behaviour: "read" (takes no write lock, and path
//	overlaps report as info instead of soft_block)
//
// Measured 2026-09-02 on a work item whose SOLE declared resource was
// {"type":"path","uri":"file:.gitignore","intent":"read"}:
//
//	pf_predict_conflicts(... intent=read)  -> severity: info      (rule 3)
//	pf_claim_work_item(mode=fresh)         -> acquired_locks: [{"file_scope",
//	                                          "aihub:.gitignore", ...}]
//
// so pf_predict_conflicts stopped being a pre-claim check: its `info` and the
// claim's 409 CONFLICT_LOCK_TAKEN had no relationship left.
//
// # The class, not the instance
//
// Searching for the reported symptom (claim) finds one site. Anchoring on what
// the defect IS — "who turns a declared_resources entry into a resource_locks
// key" — finds four, and those four are the whole population:
//
//	conflicts.go    PredictConflicts rule 1  ignored intent (hard_block on a read path)
//	run_attempts.go FnClaimWorkItem          ignored intent (the reported bug)
//	run_attempts.go FnForceTakeover          ignored intent, and worse: its local
//	                                         struct has no Intent FIELD, so the
//	                                         value never even reaches the mapper
//	run_attempts.go FnAcquireLocks           honoured it (`if d.Intent == "read" { continue }`)
//
// Three of four were wrong and one implementation was already right, which is
// why the fix gives the rule ONE home (derivedLock in conflicts.go) rather than
// repeating `if intent == "read"` a third time.
//
// PredictConflicts rule 3 is a fifth site that reads a lock KEY but derives no
// lock, and it is untouched. It does not go through resourceToLock at all — it
// builds its key straight from fileScopeLockKey — and its read -> info mapping
// IS the contract. The last subtest below asserts rule 3 still emits that
// prediction, because "read now takes no lock" and "read overlaps report as
// info" are two separate halves of one sentence and a fix that quietly deleted
// the second half would look identical from rule 1's side.
//
// # What the acceptance criterion is
//
// aihub#342 is explicit that "claim succeeded" is NOT the criterion — a claim
// that unconditionally waved everything through would satisfy it. The criterion
// is that the two tools give the SAME answer for the SAME input, which is why
// the last subtest asserts predict and claim together and why every read arm
// has a write control beside it.
//
// Run:
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestReadIntentTakesNoWriteLock' -race -v -count=1

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedClaimableWI creates a work item that FnClaimWorkItem will accept: wi_type
// and requires_human_session are both set, which seedWI (step_pause_stall_test.go)
// deliberately leaves unset because its callers stop at the FOR UPDATE.
//
// Shared by this branch's three DB test files (aihub#342 here, aihub#345 in
// acquire_locks_reporting_db_test.go, aihub#329 in
// claim_response_echo_db_test.go). Unlike seedWI it does NOT wipe the project —
// these tests seed several work items into one project on purpose, to make two
// attempts contend over one lock key. Call testProject first; it resets.
func seedClaimableWI(t *testing.T, pool *pgxpool.Pool, project, userID, goal, declaredResources string) *WorkItem {
	t.Helper()
	wiType := "fix_bug"
	rhs := false
	req := &CreateWorkItemRequest{
		Project:              project,
		Goal:                 goal,
		Source:               "human",
		WIType:               &wiType,
		RequiresHumanSession: &rhs,
	}
	if declaredResources != "" {
		req.DeclaredResources = json.RawMessage(declaredResources)
	}
	// ForceCreate: these tests seed several work items into one project and the
	// goal-similarity dedup in CreateWorkItem would reject the later ones.
	req.ForceCreate = true
	req.ForceReason = "DB test fixture for " + t.Name()
	wi, aerr := CreateWorkItem(context.Background(), pool, req, userID, userID)
	require.Nil(t, aerr, "seed create failed: %+v", aerr)
	return wi
}

// countFileScopeLocks reports how many resource_locks rows exist for a key.
// Reading the TABLE rather than a response field is the point: every tool that
// reports lock state is itself under suspicion in this batch (see aihub#345),
// so the only trustworthy oracle is the row.
func countFileScopeLocks(t *testing.T, pool *pgxpool.Pool, key string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM resource_locks WHERE resource_type='file_scope' AND resource_key=$1`, key).Scan(&n))
	return n
}

// claimFresh runs a normal claim and requires it to succeed.
func claimFresh(t *testing.T, pool *pgxpool.Pool, wiID, userID, idemKey string) *ClaimResponse {
	t.Helper()
	resp, aerr := FnClaimWorkItem(context.Background(), pool, wiID, &ClaimRequest{
		IdempotencyKey: idemKey,
		SessionInfo: SessionInfo{
			MachineID:     "m_locktest",
			SessionSecret: "locktest-secret-0123456789abcdef0123456789abcdef0123456789ab",
		},
		Mode: "fresh",
	}, userID, "", "tester")
	require.Nil(t, aerr, "claim failed: %+v", aerr)
	return resp
}

// TestReadIntentTakesNoWriteLock is aihub#342's acceptance criterion across
// every lock-deriving path, each read arm paired with the write control that
// keeps it honest.
//
// One function with subtests rather than four functions: internal/citest/
// dbtestcov counts DB-gated FUNCTIONS, and four would mean four entries in its
// gated-test manifest for one guard. ci.yml asserts `--- PASS:` per subtest.
func TestReadIntentTakesNoWriteLock(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	u := testUser(t, pool)
	project := testProject(t, pool, u)

	// MUTANT: internal/domain/run_attempts.go, FnClaimWorkItem's derivation
	// block — drop the `derivedLock` call back to `resourceToLock`. This
	// subtest goes red; the write control below stays green, which is what
	// distinguishes "read stopped taking locks" from "claim stopped locking".
	t.Run("claim derives no lock for a read declaration", func(t *testing.T) {
		const path = "internal/cli/init.go"
		key := project + ":" + path
		wi := seedClaimableWI(t, pool, project, u,
			"read a file without taking a write lock on it",
			`[{"type":"path","uri":"file:`+path+`","intent":"read"}]`)

		resp := claimFresh(t, pool, wi.ID, u, "aihub342-read")

		assert.Empty(t, resp.AcquiredLocks,
			"the sole declared resource is intent=read, which the tool schema says takes no write lock; "+
				"claim reported acquiring %+v", resp.AcquiredLocks)
		assert.Equal(t, 0, countFileScopeLocks(t, pool, key),
			"a resource_locks row exists for %q even though nothing declared a write on it — "+
				"read the TABLE, not the response: this is the row that later 409s somebody else's claim", key)
	})

	// The control. A fix that simply stopped deriving locks would satisfy every
	// other arm in this file and destroy the whole mechanism.
	t.Run("write declaration still takes the lock", func(t *testing.T) {
		const path = "internal/domain/write_control.go"
		key := project + ":" + path
		wi := seedClaimableWI(t, pool, project, u,
			"hold a write lock the way declarations are supposed to",
			`[{"type":"path","uri":"file:`+path+`","intent":"write"}]`)

		resp := claimFresh(t, pool, wi.ID, u, "aihub342-write")

		require.Len(t, resp.AcquiredLocks, 1,
			"an intent=write path must still produce exactly one lock; got %+v", resp.AcquiredLocks)
		assert.Equal(t, "file_scope", resp.AcquiredLocks[0].ResourceType)
		assert.Equal(t, key, resp.AcquiredLocks[0].ResourceKey)
		assert.Equal(t, 1, countFileScopeLocks(t, pool, key),
			"the lock row must actually exist, or nothing is being enforced")
	})

	// MUTANT: internal/domain/run_attempts.go, FnForceTakeover's re-INSERT loop
	// — remove `Intent` from the anonymous struct it unmarshals into. That
	// single field is the whole defect on this path: the value never reaches
	// the mapper, so no amount of care inside resourceToLock could have saved
	// it. Only this subtest goes red.
	t.Run("force takeover derives no lock for a read declaration", func(t *testing.T) {
		const readPath = "internal/domain/takeover_read.go"
		const writePath = "internal/domain/takeover_write.go"
		readKey := project + ":" + readPath
		writeKey := project + ":" + writePath
		wi := seedClaimableWI(t, pool, project, u,
			"take over an attempt whose declarations mix read and write",
			`[{"type":"path","uri":"file:`+readPath+`","intent":"read"},`+
				`{"type":"path","uri":"file:`+writePath+`","intent":"write"}]`)

		// Seed the running attempt directly rather than claiming: this arm must
		// isolate FnForceTakeover's OWN derivation. Going through claim would
		// leave the pre-existing lock rows in place and a green result could
		// mean "takeover derived nothing" or "claim derived nothing".
		seedRunAttempt(t, pool, wi.ID, u, "takeover-victim-secret")
		require.Equal(t, 0, countFileScopeLocks(t, pool, readKey),
			"fixture check: seedRunAttempt must not create locks, or this arm measures claim instead")

		resp, aerr := FnForceTakeover(ctx, pool, wi.ID, u, "tester", "admin",
			map[string]string{project: "maintainer"},
			&ForceTakeoverRequest{
				Reason:      "aihub#342: re-derive locks for the new attempt",
				SessionInfo: SessionInfo{MachineID: "m_locktest", SessionSecret: "takeover-new-secret"},
			})
		require.Nil(t, aerr, "force_takeover failed: %+v", aerr)

		assert.Equal(t, 0, countFileScopeLocks(t, pool, readKey),
			"force_takeover re-created a write lock for an intent=read declaration on %q", readKey)
		// Owner, not just count: "the new attempt holds it" is the claim, and a
		// takeover that left the superseded attempt's row in place would satisfy
		// a bare count of 1.
		var writeOwner string
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT owner_attempt_id FROM resource_locks WHERE resource_type='file_scope' AND resource_key=$1`,
			writeKey).Scan(&writeOwner))
		assert.Equal(t, resp.NewAttemptID, writeOwner,
			"force_takeover must hand the NEW attempt the locks the wi really declares; %q is owned by %q "+
				"but the new attempt is %q, so the takeover left the work item unprotected",
			writeKey, writeOwner, resp.NewAttemptID)
	})

	// The scope marker for the decision recorded on derivedLock: the read rule
	// is applied to file_scope ONLY. A `repo` entry keeps its git_branch lock
	// whatever its intent says, because a branch is not a per-file exclusion two
	// readers can share, and because PredictConflicts rule 2 has no intent check
	// either — applying the rule here and not there would rebuild, on `repo`,
	// the very contradiction this file closes on `path`.
	//
	// MUTANT: in derivedLock, drop the `lockType == "file_scope" &&` guard. Only
	// this subtest goes red — which is the point: widening that rule is a real
	// decision about branch safety and must not be reachable by accident.
	t.Run("a read declaration on a repo still takes its branch lock", func(t *testing.T) {
		wi := seedClaimableWI(t, pool, project, u,
			"declare a repo as read and keep the branch to yourself anyway",
			`[{"type":"repo","uri":"repo:aihub","intent":"read","task_branch":"aihub342-scope"}]`)

		resp := claimFresh(t, pool, wi.ID, u, "aihub342-repo-read")

		require.Len(t, resp.AcquiredLocks, 1,
			"a repo declaration must still take its git_branch lock regardless of intent; got %+v", resp.AcquiredLocks)
		assert.Equal(t, "git_branch", resp.AcquiredLocks[0].ResourceType)
		assert.Equal(t, "aihub/aihub342-scope", resp.AcquiredLocks[0].ResourceKey)

		var n int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM resource_locks WHERE resource_type='git_branch' AND resource_key=$1`,
			"aihub/aihub342-scope").Scan(&n))
		assert.Equal(t, 1, n,
			"the branch lock must exist in the table: without it a second attempt can check out the same "+
				"branch, and because both takeover paths DELETE prior locks before re-deriving, an existing "+
				"branch lock would be RELEASED on the next takeover rather than merely not taken")
	})

	// MUTANT: internal/domain/conflicts.go, PredictConflicts rule 1 — drop the
	// `derivedLock` call back to `resourceToLock`. Only this subtest goes red.
	//
	// This is aihub#342's stated criterion: not "claim succeeds", but "both
	// tools answer the same question the same way".
	t.Run("predict and claim agree on a read declaration over a held lock", func(t *testing.T) {
		const path = "internal/domain/contended.go"
		key := project + ":" + path

		// A different work item, running, genuinely holding the write lock.
		holder := seedClaimableWI(t, pool, project, u,
			"hold the contended path so somebody else has something to overlap with",
			`[{"type":"path","uri":"file:`+path+`","intent":"write"}]`)
		claimFresh(t, pool, holder.ID, u, "aihub342-holder")
		require.Equal(t, 1, countFileScopeLocks(t, pool, key),
			"fixture check: the holder must really hold the lock, or nothing is being contended")

		readDecl := json.RawMessage(`[{"type":"path","uri":"file:` + path + `","intent":"read"}]`)

		// dry_run=false so rule 1 actually runs. With dry_run=true it is skipped
		// entirely, which is why the original report — taken with dry_run=true —
		// saw only rule 3's `info` and never the hard_block underneath it.
		pred, aerr := PredictConflicts(ctx, pool, &PredictConflictsRequest{
			Project:           project,
			DeclaredResources: readDecl,
			DryRun:            false,
		}, map[string]string{project: "maintainer"})
		require.Nil(t, aerr, "predict failed: %+v", aerr)
		assert.NotEqual(t, SeverityHardBlock, pred.Severity,
			"rule 1 hard-blocked a path declared intent=read; rule 3 in the same function reports the same "+
				"overlap as info, so the two rules contradict each other on one input. predictions=%+v",
			pred.Predictions)
		for _, p := range pred.Predictions {
			assert.NotEqual(t, 1, p.Rule,
				"rule 1 is the lock-conflict rule; a declaration that takes no lock cannot conflict with one. got %+v", p)
		}

		// 🔴 The positive half, and the one that is easy to leave out: the two
		// assertions above are BOTH satisfied by an empty Predictions slice.
		// "read takes no write lock" and "read overlaps report as info" are two
		// halves of one contract sentence, and only the first has any code of
		// its own now — so a follow-on edit that reasoned "read takes no lock,
		// so rule 3 should skip it too" would delete the second half and be
		// indistinguishable from a correct fix under a test that only asserts
		// what must NOT be there. Assert what must BE there.
		//
		// MUTANT: add `if res.Intent == "read" { continue }` to rule 3's loop in
		// internal/domain/conflicts.go. Only this pair of assertions goes red.
		var rule3 *ConflictPrediction
		for i := range pred.Predictions {
			if pred.Predictions[i].Rule == 3 && pred.Predictions[i].ResourceKey == key {
				rule3 = &pred.Predictions[i]
			}
		}
		require.NotNil(t, rule3,
			"rule 3 must still report the overlap for a read declaration — silently reporting NOTHING is a "+
				"quieter version of the defect this file exists to close, and it passes every 'must not be "+
				"hard_block' assertion. predictions=%+v", pred.Predictions)
		assert.Equal(t, SeverityInfo, rule3.Severity,
			"the contract is `info`, not silence and not soft_block; got %+v", rule3)

		// And the other half of the criterion: the claim must reach the same
		// verdict. Before the fix this 409'd CONFLICT_LOCK_TAKEN while rule 3
		// was reporting `info` for the identical input.
		reader := seedClaimableWI(t, pool, project, u,
			"read the contended path while somebody else writes it",
			string(readDecl))
		resp, claimErr := FnClaimWorkItem(ctx, pool, reader.ID, &ClaimRequest{
			IdempotencyKey: "aihub342-reader",
			SessionInfo: SessionInfo{
				MachineID:     "m_locktest",
				SessionSecret: "reader-secret-0123456789abcdef0123456789abcdef0123456789abcd",
			},
			Mode: "fresh",
		}, u, "", "tester")
		require.Nil(t, claimErr,
			"predict says this is not a lock conflict and claim disagreed: %+v — that gap is the whole "+
				"defect, because predict IS the pre-claim gate", claimErr)
		assert.Empty(t, resp.AcquiredLocks,
			"the reader must not take the contended lock either; got %+v", resp.AcquiredLocks)
		assert.Equal(t, 1, countFileScopeLocks(t, pool, key),
			"the holder's lock must survive the reader's claim — a reader that STEALS the row would satisfy "+
				"every assertion above and be far worse than the bug")
	})
}
