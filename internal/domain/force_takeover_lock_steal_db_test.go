package domain

// DB-gated integration tests for aihub#393: a force takeover must not take a
// resource_locks row away from a DIFFERENT work item that is still live.
//
// # The defect
//
// FnClaimWorkItem's cross-work-item conflict probe was guarded by
// `len(req.RequestedLocks) > 0 && !isTakeover`, so every takeover skipped it.
// The probe already excludes the TARGET work item's own attempts
// (`ra.work_item_id != $4`, aihub#207), so `!isTakeover` bought nothing except
// silence about foreign holders: the upsert loop then ran acquireLockUpsert,
// whose ON CONFLICT DO UPDATE rewrote owner_attempt_id and returned success.
// The displaced work item's agent got no error on any call and kept editing
// files it no longer held the lock on.
//
// FnForceTakeover (the pf_force_takeover tool) had no probe at ALL and
// discarded acquireLockUpsert's error, so it displaced foreign holders the same
// way through a second entry point.
//
// # What the fix asserts, and why this file is shaped in arms
//
// The rule is a property of the LOCK ROW, not of an entry point: a row owned by
// a running-or-paused attempt of another work item may not change hands. It is
// enforced at the single statement that can rewrite that row (lockUpsertSQL's
// conditional DO UPDATE, checked again in acquireLockUpsert), so a takeover
// path that does not exist yet inherits it. The two takeover entry points also
// probe first, so the caller gets CONFLICT_LOCK_TAKEN with a conflict_with
// payload rather than a bare refusal.
//
// That predicate has three ways to be wrong, and each control arm below pins
// one of them. Only the first is the reported bug; the other two are the
// directions in which a fix that is too WIDE breaks working behaviour, which on
// a fail-closed judgement surface is the more expensive mistake:
//
//	foreign + live  -> refuse   (the bug; two arms, one per entry point)
//	own wi  + live  -> allow    (resume displaces its own PAUSED attempt's row;
//	                             a predicate keyed on "live" alone reddens
//	                             TestResumeOwnLocks_NoSelfConflict)
//	foreign + dead  -> allow    (an un-swept orphan row is exactly the case
//	                             lockUpsertSQL's comment says the upsert exists
//	                             to overwrite; refusing it would make a takeover
//	                             unable to recover from a crashed holder)
//
// # Constructing "B is running AND declares a path A holds"
//
// It cannot be reached by claiming in either order — whichever work item claims
// second is refused the lock. It is reached the way it happens in production:
// B is claimed, its declared_resources are then rewritten mid-attempt (pf-plan
// rewrites them on every run, and aihub#264 made that release/keep locks), B's
// agent dies, and somebody force-takes it over. So each arm claims B with no
// declarations, lets A take the path, and only then declares the path on B.
//
// Run:
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@127.0.0.1:15493/aihub_test?sslmode=disable \
//	  go test ./internal/domain/ -run TestForceTakeoverLockSteal -count=1 -v

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ftsSecond seeds a SECOND user in the same project, so the explicit
// `force_takeover=true` branch (run_attempts.go, `else if req.ForceOver`) is
// exercised rather than the same-user implicit-takeover branch above it. Both
// branches set isTakeover, and only one of them needs a different caller.
func ftsSecondUser(t *testing.T, pool *pgxpool.Pool, uid string) string {
	t.Helper()
	other := uid + "_2"
	mustExec(t, pool, `INSERT INTO users(id,email,display_name) VALUES('`+other+
		`','`+other+`@test.local','`+other+`') ON CONFLICT (id) DO NOTHING`)
	return other
}

// ftsClaim claims wiID, optionally as a force takeover, as the given user.
func ftsClaim(t *testing.T, pool *pgxpool.Pool, uid, wiID, idem string, force bool) (*ClaimResponse, *AihubError) {
	t.Helper()
	return FnClaimWorkItem(context.Background(), pool, wiID, &ClaimRequest{
		IdempotencyKey: idem,
		SessionInfo:    SessionInfo{MachineID: "m-393", SessionSecret: testSecret},
		ForceOver:      force,
	}, uid, "", "tester")
}

// ftsLockOwner reads the owner_attempt_id of one file_scope row by key. The
// assertion the work item names is on this column: "unchanged" is what
// distinguishes a refusal from a takeover that 409'd after already moving the
// row.
func ftsLockOwner(t *testing.T, pool *pgxpool.Pool, key string) string {
	t.Helper()
	var owner string
	if err := pool.QueryRow(context.Background(), `
		SELECT owner_attempt_id FROM resource_locks
		WHERE resource_type='file_scope' AND resource_key=$1`, key).Scan(&owner); err != nil {
		t.Fatalf("read owner of file_scope:%s: %v", key, err)
	}
	return owner
}

// ftsGitBranchOwner is ftsLockOwner for a git_branch row. Separate because a
// git_branch key is "<repo>/<branch>" with no project segment, so the two key
// shapes must not be built by one caller that could pass the wrong one.
func ftsGitBranchOwner(t *testing.T, pool *pgxpool.Pool, key string) string {
	t.Helper()
	var owner string
	if err := pool.QueryRow(context.Background(), `
		SELECT owner_attempt_id FROM resource_locks
		WHERE resource_type='git_branch' AND resource_key=$1`, key).Scan(&owner); err != nil {
		t.Fatalf("read owner of git_branch:%s: %v", key, err)
	}
	return owner
}

// ftsAttemptStatus reads one run_attempt's status, so an arm can tell a refusal
// that rolled the whole transaction back from one that superseded the target's
// attempt on the way out.
func ftsAttemptStatus(t *testing.T, pool *pgxpool.Pool, attemptID string) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(context.Background(),
		`SELECT status FROM run_attempts WHERE id=$1`, attemptID).Scan(&status); err != nil {
		t.Fatalf("read status of %s: %v", attemptID, err)
	}
	return status
}

// ftsDeclare rewrites a work item's declared_resources in place. Raw SQL rather
// than UpdateWorkItem on purpose: UpdateWorkItem also RELEASES locks for paths
// it removed (aihub#264), and an arm that needs "B declares p and holds
// nothing" must not have its fixture depend on that behaviour being right.
func ftsDeclare(t *testing.T, pool *pgxpool.Pool, wiID string, declared json.RawMessage) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE work_items SET declared_resources=$1::jsonb WHERE id=$2`, string(declared), wiID); err != nil {
		t.Fatalf("declare on %s: %v", wiID, err)
	}
}

// ftsFixture builds the shared situation: work item B running with no locks,
// work item A running and holding `path` in `repo`. It returns B, A's attempt
// id and the file_scope key they contend for.
func ftsFixture(t *testing.T, pool *pgxpool.Pool, proj, uid, repo, path string) (wiB *WorkItem, aAttempt, key string) {
	t.Helper()
	declared := declaredWithRepo(repo, path)

	// B first, declaring NOTHING, so its claim takes no file_scope lock.
	wiB = seedWIWithResources(t, pool, proj, uid, "B: taken over later", json.RawMessage(`[]`))
	claimB, aerr := ftsClaim(t, pool, uid, wiB.ID, "idem-393-b", false)
	if aerr != nil {
		t.Fatalf("claim B (declares nothing, must succeed): %v", aerr)
	}
	if len(fileScopeKeys(claimB.AcquiredLocks)) != 0 {
		t.Fatalf("claim B took file_scope locks %v; the fixture needs it holding none",
			fileScopeKeys(claimB.AcquiredLocks))
	}

	// A now takes the contended path.
	wiA := seedWIWithResources(t, pool, proj, uid, "A: holds the contended path", declared)
	claimA, aerr := ftsClaim(t, pool, uid, wiA.ID, "idem-393-a", false)
	if aerr != nil {
		t.Fatalf("claim A (path is free, must succeed): %v", aerr)
	}
	got := fileScopeKeys(claimA.AcquiredLocks)
	key = proj + ":" + repo + ":" + path
	if len(got) != 1 || got[0] != key {
		t.Fatalf("A's file_scope keys = %v, want [%q]", got, key)
	}

	// Only now does B declare the same path: it declares it and holds nothing.
	ftsDeclare(t, pool, wiB.ID, declared)
	return wiB, claimA.AttemptID, key
}

// TestForceTakeoverLockSteal_ClaimRefusesAForeignHolder is the work item's named
// gate, on the entry point it names: pf_claim_work_item with
// force_takeover=true. Both arms cover a branch that sets isTakeover — the
// explicit request and the same-user re-claim — because the defect was in the
// shared `!isTakeover` guard and fixing only one branch would leave the other.
func TestForceTakeoverLockSteal_ClaimRefusesAForeignHolder(t *testing.T) {
	pool := setupLatestTestDB(t)

	t.Run("explicit_force_takeover_by_another_user", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		other := ftsSecondUser(t, pool, uid)

		wiB, aAttempt, key := ftsFixture(t, pool, proj, uid, "repo-a", "run_attempts.go")

		_, aerr := ftsClaim(t, pool, other, wiB.ID, "idem-393-steal", true)
		if aerr == nil {
			t.Fatalf("force_takeover of B succeeded; it displaced work item A's live lock on %s", key)
		}
		if aerr.Code != ErrConflictLockTaken {
			t.Fatalf("force_takeover of B: got %v, want ErrConflictLockTaken", aerr)
		}
		if owner := ftsLockOwner(t, pool, key); owner != aAttempt {
			t.Errorf("owner_attempt_id of file_scope:%s = %q, want %q (A's attempt) — "+
				"the refusal must leave the row alone, not move it and then report a conflict", key, owner, aAttempt)
		}
	})

	t.Run("same_user_reclaim_of_a_running_wi", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)

		wiB, aAttempt, key := ftsFixture(t, pool, proj, uid, "repo-a", "run_attempts.go")

		// Same user, force_takeover NOT set: FnClaimWorkItem still sets
		// isTakeover because the running attempt is this caller's own.
		_, aerr := ftsClaim(t, pool, uid, wiB.ID, "idem-393-self", false)
		if aerr == nil {
			t.Fatalf("same-user re-claim of B succeeded; it displaced work item A's live lock on %s", key)
		}
		if aerr.Code != ErrConflictLockTaken {
			t.Fatalf("same-user re-claim of B: got %v, want ErrConflictLockTaken", aerr)
		}
		if owner := ftsLockOwner(t, pool, key); owner != aAttempt {
			t.Errorf("owner_attempt_id of file_scope:%s = %q, want %q (A's attempt)", key, owner, aAttempt)
		}
	})
}

// TestForceTakeoverLockSteal_ForceTakeoverToolRefusesAForeignHolder covers the
// SECOND entry point, pf_force_takeover / FnForceTakeover. It is a separate
// function rather than a third arm above because it reaches the lock through
// its own derivation loop and used to discard the error the upsert returned —
// a fix applied only to the claim path leaves this one displacing rows.
func TestForceTakeoverLockSteal_ForceTakeoverToolRefusesAForeignHolder(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)
	other := ftsSecondUser(t, pool, uid)

	wiB, aAttempt, key := ftsFixture(t, pool, proj, uid, "repo-a", "resource_events.go")
	bAttemptBefore := currentAttemptID(t, pool, wiB.ID)

	_, aerr := FnForceTakeover(ctx, pool, wiB.ID, other, "taker", "admin",
		map[string]string{proj: "maintainer"},
		&ForceTakeoverRequest{Reason: "aihub#393 cross-wi steal",
			SessionInfo: SessionInfo{MachineID: "m-393-ft", SessionSecret: testSecret}})
	if aerr == nil {
		t.Fatalf("pf_force_takeover of B succeeded; it displaced work item A's live lock on %s", key)
	}
	if aerr.Code != ErrConflictLockTaken {
		t.Fatalf("pf_force_takeover of B: got %v, want ErrConflictLockTaken", aerr)
	}
	if owner := ftsLockOwner(t, pool, key); owner != aAttempt {
		t.Errorf("owner_attempt_id of file_scope:%s = %q, want %q (A's attempt)", key, owner, aAttempt)
	}
	// FnForceTakeover supersedes the prior attempt and deletes its locks BEFORE
	// it re-derives them, so a refusal that is not atomic leaves B with no
	// running attempt and no locks — worse than the steal it replaced.
	if status := ftsAttemptStatus(t, pool, bAttemptBefore); status != "running" {
		t.Errorf("B's attempt %s is %q after the refused takeover, want \"running\" — "+
			"the refusal must roll the whole takeover back", bAttemptBefore, status)
	}
}

// TestForceTakeoverLockSteal_ControlsStillAllowTheLegitimateDisplacements is
// the half that keeps the arms above from being satisfied by "never displace
// anything". Each arm is a displacement the fix must NOT refuse; a predicate
// that reads "any live holder" or "any other attempt" reddens one of them.
func TestForceTakeoverLockSteal_ControlsStillAllowTheLegitimateDisplacements(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	t.Run("control_takeover_still_takes_the_targets_own_lock", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		other := ftsSecondUser(t, pool, uid)

		wi := seedWIWithResources(t, pool, proj, uid, "own lock, taken over",
			declaredWithRepo("repo-a", "go.mod"))
		first, aerr := ftsClaim(t, pool, uid, wi.ID, "idem-393-own", false)
		if aerr != nil {
			t.Fatalf("first claim: %v", aerr)
		}
		key := proj + ":repo-a:go.mod"
		if owner := ftsLockOwner(t, pool, key); owner != first.AttemptID {
			t.Fatalf("fixture: owner of %s = %q, want the first attempt %q", key, owner, first.AttemptID)
		}

		second, aerr := ftsClaim(t, pool, other, wi.ID, "idem-393-own-2", true)
		if aerr != nil {
			t.Fatalf("force_takeover of a wi holding its OWN lock must succeed, got %v — "+
				"the refusal is scoped to locks held by a DIFFERENT work item", aerr)
		}
		if owner := ftsLockOwner(t, pool, key); owner != second.AttemptID {
			t.Errorf("owner of %s = %q, want the new attempt %q — the takeover did not "+
				"take over the lock it is entitled to", key, owner, second.AttemptID)
		}
	})

	t.Run("control_force_takeover_tool_still_takes_the_targets_own_lock", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		other := ftsSecondUser(t, pool, uid)

		wi := seedWIWithResources(t, pool, proj, uid, "own lock, taken over by tool",
			declaredWithRepo("repo-a", "go.sum"))
		if _, aerr := ftsClaim(t, pool, uid, wi.ID, "idem-393-own-ft", false); aerr != nil {
			t.Fatalf("first claim: %v", aerr)
		}
		resp, aerr := FnForceTakeover(ctx, pool, wi.ID, other, "taker", "admin",
			map[string]string{proj: "maintainer"},
			&ForceTakeoverRequest{Reason: "aihub#393 own-lock control",
				SessionInfo: SessionInfo{MachineID: "m-393-ft2", SessionSecret: testSecret}})
		if aerr != nil {
			t.Fatalf("pf_force_takeover of a wi holding its OWN lock must succeed, got %v", aerr)
		}
		key := proj + ":repo-a:go.sum"
		if owner := ftsLockOwner(t, pool, key); owner != resp.NewAttemptID {
			t.Errorf("owner of %s = %q, want the new attempt %q", key, owner, resp.NewAttemptID)
		}
	})

	t.Run("control_orphan_row_of_a_dead_foreign_attempt_is_reclaimed", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		other := ftsSecondUser(t, pool, uid)

		wiB, aAttempt, key := ftsFixture(t, pool, proj, uid, "repo-a", "Makefile")
		// A ends. Its lock row survives until the orphan sweep runs, which
		// lockUpsertSQL's comment names as the reachable displacement and the
		// gc tick may be up to a minute away. Refusing THIS would make a
		// takeover unable to recover from a crashed holder.
		mustExec(t, pool, `UPDATE run_attempts SET status='wrapped', ended_at=clock_timestamp() WHERE id='`+aAttempt+`'`)

		claim, aerr := ftsClaim(t, pool, other, wiB.ID, "idem-393-orphan", true)
		if aerr != nil {
			t.Fatalf("force_takeover over an ORPHAN row must succeed, got %v — the refusal "+
				"is scoped to running/paused holders, not to every other attempt", aerr)
		}
		if owner := ftsLockOwner(t, pool, key); owner != claim.AttemptID {
			t.Errorf("owner of %s = %q, want the new attempt %q — the orphan row was not reclaimed",
				key, owner, claim.AttemptID)
		}
	})

	t.Run("control_resume_still_displaces_its_own_paused_attempts_git_branch_lock", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)

		// A repo entry, so the claim derives a git_branch lock. The branch name
		// carries the project because a git_branch key is "<repo>/<branch>" and
		// is NOT project-namespaced, unlike file_scope.
		branch := "br-" + proj
		declared, err := json.Marshal([]map[string]any{
			{"type": "repo", "uri": "repo:repo-a", "intent": "write", "task_branch": branch},
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		wi := seedWIWithResources(t, pool, proj, uid, "own paused git_branch lock, resumed", declared)
		first, aerr := ftsClaim(t, pool, uid, wi.ID, "idem-393-paused", false)
		if aerr != nil {
			t.Fatalf("first claim: %v", aerr)
		}
		// Measured 2026-09-07 on this build: pausing releases only file_scope
		// (acquireLocksReleasePausedSQL names that type), so git_branch and
		// deploy_env stay on the PAUSED attempt — a running-or-paused, i.e.
		// LIVE, holder. Resuming then starts from status=paused, which sets no
		// isTakeover and releases nothing, so the upsert really does displace a
		// live owner. It is legitimate only because that owner belongs to the
		// SAME work item, which is why the predicate has to be "foreign", not
		// "live". This is the arm a fix keyed on liveness alone reddens.
		if aerr := FnCompleteAttempt(ctx, pool, wi.ID, &CompleteAttemptRequest{
			AttemptID: first.AttemptID, ClaimEpoch: first.ClaimEpoch,
			SessionSecret: testSecret, Status: "paused",
		}); aerr != nil {
			t.Fatalf("pause: %v", aerr)
		}
		key := "repo-a/" + branch
		if owner := ftsGitBranchOwner(t, pool, key); owner != first.AttemptID {
			t.Fatalf("fixture: git_branch:%s owner=%q, want the paused attempt %q; this arm "+
				"needs a LIVE same-wi holder to displace", key, owner, first.AttemptID)
		}
		if status := ftsAttemptStatus(t, pool, first.AttemptID); status != "paused" {
			t.Fatalf("fixture: first attempt is %q, want \"paused\"", status)
		}

		second, aerr := ftsClaim(t, pool, uid, wi.ID, "idem-393-paused-2", false)
		if aerr != nil {
			t.Fatalf("resuming a wi whose OWN paused attempt holds the lock must succeed, got %v", aerr)
		}
		if owner := ftsGitBranchOwner(t, pool, key); owner != second.AttemptID {
			t.Errorf("git_branch:%s owner=%q, want the new attempt %q", key, owner, second.AttemptID)
		}
	})
}

// currentAttemptID reads work_items.current_attempt_id.
func currentAttemptID(t *testing.T, pool *pgxpool.Pool, wiID string) string {
	t.Helper()
	var id *string
	if err := pool.QueryRow(context.Background(),
		`SELECT current_attempt_id FROM work_items WHERE id=$1`, wiID).Scan(&id); err != nil {
		t.Fatalf("read current_attempt_id of %s: %v", wiID, err)
	}
	if id == nil {
		t.Fatalf("work item %s has no current_attempt_id", wiID)
	}
	return *id
}
