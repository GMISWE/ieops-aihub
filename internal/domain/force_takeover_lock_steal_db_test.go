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

	"github.com/GMISWE/ieops-aihub/internal/auth"
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
	return ftsClaimWithLocks(t, pool, uid, wiID, idem, force, nil)
}

// ftsClaimWithLocks is ftsClaim with an explicit requested_locks slice, needed
// since aihub#416 retired the git_branch derivation: the arm below has to arrange
// a LIVE same-work-item holder of a non-file_scope row, and requested_locks is
// now the only way one is created.
func ftsClaimWithLocks(t *testing.T, pool *pgxpool.Pool, uid, wiID, idem string, force bool, locks []ResourceLockReq) (*ClaimResponse, *AihubError) {
	t.Helper()
	return FnClaimWorkItem(context.Background(), pool, wiID, &ClaimRequest{
		IdempotencyKey: idem,
		RequestedLocks: locks,
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

		// 🔴 The git_branch row is REQUESTED here, not derived (aihub#416): a repo
		// entry no longer maps to one. The repo entry stays in the declaration
		// because this arm is about a lock with a live SAME-work-item holder, and
		// leaving the declaration in place keeps the fixture readable as "a work
		// item that works on repo-a" rather than as a bare key.
		//
		// What the arm measures is untouched by the retirement: pause releases
		// file_scope only, so a non-file_scope row survives on the PAUSED
		// attempt, and the resume must still displace it because the holder
		// belongs to the same work item. That predicate is "foreign", not "live"
		// (aihub#393), and this is the arm that reddens for a fix keyed on
		// liveness alone.
		//
		// The branch name carries the project because a git_branch key is
		// "<repo>/<branch>" and is NOT project-namespaced, unlike file_scope.
		branch := "br-" + proj
		declared, err := json.Marshal([]map[string]any{
			{"type": "repo", "uri": "repo:repo-a", "intent": "write", "task_branch": branch},
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		wi := seedWIWithResources(t, pool, proj, uid, "own paused git_branch lock, resumed", declared)
		branchLock := []ResourceLockReq{{ResourceType: "git_branch", ResourceKey: "repo-a/" + branch}}
		first, aerr := ftsClaimWithLocks(t, pool, uid, wi.ID, "idem-393-paused", false, branchLock)
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
		}, nil, ""); aerr != nil {
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

		second, aerr := ftsClaimWithLocks(t, pool, uid, wi.ID, "idem-393-paused-2", false, branchLock)
		if aerr != nil {
			t.Fatalf("resuming a wi whose OWN paused attempt holds the lock must succeed, got %v", aerr)
		}
		if owner := ftsGitBranchOwner(t, pool, key); owner != second.AttemptID {
			t.Errorf("git_branch:%s owner=%q, want the new attempt %q", key, owner, second.AttemptID)
		}
	})

	// ─── aihub#543 probe wave 1: the ordinary success path ───────────────────
	//
	// The two arms below are also displacements this function must not refuse,
	// which is why they live here rather than in a new top-level function: the
	// aihub#543 §3.3 selection rule says to attach a row-level probe as a SUBTEST
	// of a function already in internal/citest/dbtestcov/gated_tests.txt and
	// already named by a ci.yml step, because a new gated function costs a line in
	// both shared ratchets. `-run '^TestForceTakeoverLockSteal'` selects them.
	//
	// They strengthen this function rather than merely borrow its fixtures. Every
	// arm above measures ONE column after a displacement — the lock row's owner —
	// so a fix that refused nothing it should refuse and also stopped superseding
	// the prior attempt, or stopped writing the timeline event, would be green in
	// all of them. What docs/mcp-cards/pf_force_takeover.md publishes is the whole
	// effect set, and the existing aihub#451 commit-window arm covers two of its
	// five conjuncts on the RACE path only.
	//
	// MUTANTS, all applied to internal/domain/run_attempts.go and run against this
	// tree; the verdict is what happened, not what was expected:
	//
	//	M20 enforcement: supersede the prior attempt as 'paused' instead
	//	                                       RED  success_path, conjunct 1
	//	M21 enforcement: drop "reason" from the force_takeover event payload
	//	                                       RED  success_path, conjunct 3
	//	M22 enforcement: bind the new attempt to a server-generated secret rather
	//	    than the one this request carried  RED  success_path, conjunct 4
	//	M23 enforcement: newEpoch := wi.CurrentAttemptEpoch (no advance)
	//	                                       RED  success_path, conjunct 5 — and
	//	                                            also two arms that predate this
	//	                                            change, since a claim_epoch
	//	                                            collision fails the whole call
	//	M24 enforcement: delete the releaseLocks call, so the prior attempt's rows
	//	    are never released              RED  success_path, conjunct 2 — and this
	//	                                        is the mutant the fixture is shaped
	//	                                        around: with the declaration
	//	                                        UNCHANGED across the takeover the
	//	                                        re-insert upsert rewrites the same
	//	                                        row's owner and this mutant stays
	//	                                        GREEN, which is why the arm rewrites
	//	                                        declared_resources first
	//	M25 enforcement: refuse a self-takeover (isSelf || !isMaintainerOrAdmin)
	//	                                       RED  self_takeover — the first
	//	                                            takeover still succeeds, the
	//	                                            re-run does not
	//	M26 enforcement: drop the wi.Status != "running" guard
	//	                                       RED  self_takeover's refusal half
	//	G3  control:     rename the machine_id fallback string
	//	                                     GREEN  neither arm reads machine_id, so
	//	                                            an unrelated edit to the same
	//	                                            function must not redden them
	//	M27 publication: delete the success-path bullet's citation from the card
	//	                                       RED  K12 — the sentence stops citing
	//	                                            an arm and falls back into the
	//	                                            debt column
	//	M28 publication: delete the self-takeover bullet's citation
	//	                                       RED  K12, same shape

	t.Run("control_the_success_path_writes_every_effect_the_card_publishes", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		other := ftsSecondUser(t, pool, uid)

		// A MIXED declaration: one `path` (derives file_scope), one `repo` and one
		// `service` (derive nothing since aihub#416). The mix is what makes the
		// last assertion a statement about the type set rather than about one row.
		//
		// 🔴 And the declaration is REWRITTEN before the takeover, which is what
		// makes "its resource_locks rows are deleted" observable at all. With the
		// declaration unchanged, the re-insert loop upserts the same key and
		// rewrites owner_attempt_id, so a takeover that never released anything
		// leaves the prior attempt holding nothing either — the delete and the
		// upsert are byte-identical from the outside. Rewriting it first (the
		// production sequence this file's header describes: pf-plan rewrites
		// declared_resources mid-attempt) means the OLD row has no upsert coming
		// for it, and only a real release removes it.
		declaredAtClaim := json.RawMessage(`[
			{"type":"path","uri":"file:internal/domain/superseded.go","repo":"repo-a","intent":"write"},
			{"type":"repo","uri":"repo:repo-a","intent":"write"},
			{"type":"service","uri":"service:aihub","intent":"write"}
		]`)
		declaredAtTakeover := json.RawMessage(`[
			{"type":"path","uri":"file:internal/domain/run_attempts.go","repo":"repo-a","intent":"write"},
			{"type":"repo","uri":"repo:repo-a","intent":"write"},
			{"type":"service","uri":"service:aihub","intent":"write"}
		]`)
		wi := seedWIWithResources(t, pool, proj, uid, "every effect of a successful takeover", declaredAtClaim)
		first, aerr := ftsClaim(t, pool, uid, wi.ID, "idem-543-success", false)
		if aerr != nil {
			t.Fatalf("initial claim: %v", aerr)
		}

		// Fixture floor. Each assertion after the takeover is a comparison against
		// this state, and every one of them is satisfiable by a work item that held
		// nothing and did nothing.
		oldKey := proj + ":repo-a:internal/domain/superseded.go"
		key := proj + ":repo-a:internal/domain/run_attempts.go"
		if owner := ftsLockOwner(t, pool, oldKey); owner != first.AttemptID {
			t.Fatalf("fixture: file_scope:%s is owned by %q, want the first attempt %q", oldKey, owner, first.AttemptID)
		}
		if held := ftsLockKeysOf(t, pool, first.AttemptID); len(held) != 1 {
			t.Fatalf("fixture: the first attempt holds %v, want exactly the one file_scope row — "+
				"\"its resource_locks rows are deleted\" is not observable on an attempt with none", held)
		}
		ftsDeclare(t, pool, wi.ID, declaredAtTakeover)
		priorEpoch := ftsWorkItemEpoch(t, pool, wi.ID)
		if events := ftsForceTakeoverEvents(t, pool, wi.ID); len(events) != 0 {
			t.Fatalf("fixture: %d force_takeover event(s) on the timeline before any takeover", len(events))
		}

		// A DIFFERENT secret from the one the first claim registered, so "carrying
		// THIS caller's secret hash" is a comparison rather than a coincidence.
		const takerSecret = "s3cr3t-543ft0000000000000000000000000000000000000000000000000000"
		const reason = "aihub#543 success-path effect set"
		resp, aerr := FnForceTakeover(ctx, pool, wi.ID, other, "taker", "admin",
			map[string]string{proj: "maintainer"},
			&ForceTakeoverRequest{Reason: reason,
				SessionInfo: SessionInfo{MachineID: "m-543-ft", SessionSecret: takerSecret}})
		if aerr != nil {
			t.Fatalf("a takeover of a work item holding only its OWN locks must succeed, got %v", aerr)
		}

		// 1 — the prior attempt becomes `superseded`.
		if status := ftsAttemptStatus(t, pool, first.AttemptID); status != "superseded" {
			t.Errorf("the prior attempt %s is %q, want \"superseded\". A takeover that leaves it "+
				"running leaves two attempts believing they hold this work item, and the evicted "+
				"one's next credential-checked call is the only place that would surface",
				first.AttemptID, status)
		}

		// 2 — its resource_locks rows are deleted. The old key has no upsert coming
		// for it, so a row that survives survives because nothing released it.
		if held := ftsLockKeysOf(t, pool, first.AttemptID); len(held) != 0 {
			t.Errorf("the prior attempt %s still owns %v after the takeover. A superseded attempt "+
				"holding a row is a lock nobody can release: the orphan sweep is the only thing "+
				"left that reaches it, and until it runs the key blocks every other work item",
				first.AttemptID, held)
		}
		if n := ftsLockRowsFor(t, pool, oldKey); n != 0 {
			t.Errorf("file_scope:%s still exists after a takeover whose declaration no longer "+
				"names it. The re-derivation cannot have created it, so it is the prior attempt's "+
				"row left behind", oldKey)
		}

		// 3 — a `force_takeover` event is written, naming the prior attempt and
		// carrying the caller's reason. The reason is checked because it is the
		// only account the evicted holder ever gets, and the tool requires it.
		events := ftsForceTakeoverEvents(t, pool, wi.ID)
		if len(events) != 1 {
			t.Errorf("%d force_takeover event(s) on the timeline, want exactly 1 — the timeline is "+
				"where an evicted agent finds out what happened to it", len(events))
		} else {
			if got, _ := events[0]["prior_attempt_id"].(string); got != first.AttemptID {
				t.Errorf("the force_takeover event names prior_attempt_id=%q, want %q", got, first.AttemptID)
			}
			if got, _ := events[0]["reason"].(string); got != reason {
				t.Errorf("the force_takeover event records reason=%q, want %q — the card's hop-1 "+
					"table promises the reason is recorded on the timeline", got, reason)
			}
		}

		// 4 — a new RUNNING attempt exists carrying THIS caller's secret hash.
		if status := ftsAttemptStatus(t, pool, resp.NewAttemptID); status != "running" {
			t.Errorf("the new attempt %s is %q, want \"running\"", resp.NewAttemptID, status)
		}
		gotHash := ftsAttemptSecretHash(t, pool, resp.NewAttemptID)
		if want := auth.HashSecret(takerSecret); gotHash != want {
			t.Errorf("the new attempt's session_secret_hash is not the hash of the secret THIS " +
				"caller sent. The taker's own pf_* calls would then all answer \"invalid " +
				"session_secret\", and the recovery the error message names — re-run the same " +
				"call — would be the only way out of a takeover that reported success")
		}
		if gotHash == auth.HashSecret(testSecret) {
			t.Errorf("the new attempt is bound to the PRIOR holder's secret, which is the one " +
				"reading under which the evicted agent still authenticates and the taker does not")
		}

		// 5 — current_attempt_id and the epoch advance.
		if got := currentAttemptID(t, pool, wi.ID); got != resp.NewAttemptID {
			t.Errorf("work_items.current_attempt_id = %q, want the new attempt %q", got, resp.NewAttemptID)
		}
		if got, want := ftsWorkItemEpoch(t, pool, wi.ID), priorEpoch+1; got != want {
			t.Errorf("work_items.current_attempt_epoch = %d, want %d. The epoch is what makes an "+
				"evicted attempt's credential stale; one that does not move leaves the prior "+
				"holder's calls indistinguishable from the taker's", got, want)
		}
		if resp.NewClaimEpoch != priorEpoch+1 {
			t.Errorf("the response reports new_claim_epoch=%d while the row holds %d — the state "+
				"file this machine writes would then carry a credential the server rejects",
				resp.NewClaimEpoch, ftsWorkItemEpoch(t, pool, wi.ID))
		}

		// 6 — the locks this call re-derives are `file_scope` only (aihub#416).
		// The repo and service entries in the declaration above are what make this
		// an assertion: before the retirement they derived git_branch and
		// deploy_env rows here.
		if held := ftsLockKeysOf(t, pool, resp.NewAttemptID); len(held) != 1 ||
			held[0] != "file_scope:"+key {
			t.Errorf("the new attempt holds %v, want exactly [file_scope:%s] — the takeover "+
				"re-derives from the declaration as it stands NOW, so a new attempt holding the "+
				"old key, or nothing at all, is not holding what the work item declares", held, key)
		}
		types := ftsLockTypesOf(t, pool, resp.NewAttemptID)
		if len(types) == 0 {
			t.Fatalf("the new attempt holds no locks at all, so the type assertion below is " +
				"vacuous — the re-derivation did not run")
		}
		for _, typ := range types {
			if typ != "file_scope" {
				t.Errorf("the takeover re-derived a %s lock. aihub#416 retired that derivation: "+
					"the row is released by no pause and reached by no orphan sweep, which is the "+
					"condition the de-locking ruling removed", typ)
			}
		}
	})

	t.Run("control_re_running_the_same_takeover_is_admitted_as_a_self_takeover", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		other := ftsSecondUser(t, pool, uid)

		wi := seedWIWithResources(t, pool, proj, uid, "taken over twice by the same caller",
			declaredWithRepo("repo-a", "go.mod"))
		if _, aerr := ftsClaim(t, pool, uid, wi.ID, "idem-543-self", false); aerr != nil {
			t.Fatalf("initial claim: %v", aerr)
		}

		const takerSecret = "s3cr3t-543self000000000000000000000000000000000000000000000000000"
		takeover := func(reason string) (*ForceTakeoverResponse, *AihubError) {
			return FnForceTakeover(ctx, pool, wi.ID, other, "taker", "admin",
				map[string]string{proj: "maintainer"},
				&ForceTakeoverRequest{Reason: reason,
					SessionInfo: SessionInfo{MachineID: "m-543-self", SessionSecret: takerSecret}})
		}

		firstResp, aerr := takeover("aihub#543 first takeover")
		if aerr != nil {
			t.Fatalf("first takeover: %v", aerr)
		}

		// The exact same call again. This is the recovery the handler's error
		// message names for a failed local state write, so "safe" has to mean
		// admitted, not merely non-destructive: the caller has no session_secret
		// left at that point and this call is the only way to mint another.
		secondResp, aerr := takeover("aihub#543 the same call, re-run")
		if aerr != nil {
			t.Fatalf("re-running the exact same takeover was refused with %v. The handler's "+
				"post-commit state-write error tells the caller to do precisely this, and "+
				"nothing else recovers a takeover whose secret lived only in memory", aerr)
		}
		if secondResp.PriorAttemptID != firstResp.NewAttemptID {
			t.Errorf("the second takeover superseded %q, want the attempt the first one created "+
				"(%q) — it took over from somebody other than itself, so this is not the "+
				"self-takeover the card describes", secondResp.PriorAttemptID, firstResp.NewAttemptID)
		}
		if secondResp.NewClaimEpoch != firstResp.NewClaimEpoch+1 {
			t.Errorf("epoch went %d -> %d across two takeovers; the card's \"it costs one epoch "+
				"bump\" is what tells a caller the retry is cheap rather than free",
				firstResp.NewClaimEpoch, secondResp.NewClaimEpoch)
		}
		if got := currentAttemptID(t, pool, wi.ID); got != secondResp.NewAttemptID {
			t.Errorf("current_attempt_id = %q, want %q", got, secondResp.NewAttemptID)
		}

		// The boundary of the same sentence, and the reason the re-run is safe at
		// all: the requirement is on the work item's STATUS, and the first takeover
		// is what satisfies it. Take the work item out of `running` and the same
		// call is refused — so "requires the work item to be running" is a live
		// guard rather than a description of a state nothing leaves.
		if aerr := FnCompleteAttempt(ctx, pool, wi.ID, &CompleteAttemptRequest{
			AttemptID: secondResp.NewAttemptID, ClaimEpoch: secondResp.NewClaimEpoch,
			SessionSecret: takerSecret, Status: "paused",
		}, nil, ""); aerr != nil {
			t.Fatalf("pause the attempt this arm just created: %v", aerr)
		}
		if status := ftsWorkItemStatus(t, pool, wi.ID); status == "running" {
			t.Fatalf("the work item is still \"running\" after its attempt was paused, so the " +
				"refusal below would be measuring nothing")
		}
		_, aerr = takeover("aihub#543 the same call against a work item that stopped running")
		if aerr == nil {
			t.Fatal("a takeover of a work item that is not running SUCCEEDED. There is no attempt " +
				"to take over from, so the new attempt's parent_attempt_id points at an ended one " +
				"and the work item is dragged back to running by a call that displaced nobody")
		}
		if aerr.Code != ErrBadRequest {
			t.Errorf("the refusal is %v, want %s — a caller told 500 retries, a caller told 400 "+
				"claims instead", aerr, ErrBadRequest)
		}
	})
}

// ftsWorkItemEpoch reads work_items.current_attempt_epoch, which is the half of
// the credential that a superseded attempt gets wrong.
func ftsWorkItemEpoch(t *testing.T, pool *pgxpool.Pool, wiID string) int64 {
	t.Helper()
	var epoch int64
	if err := pool.QueryRow(context.Background(),
		`SELECT current_attempt_epoch FROM work_items WHERE id=$1`, wiID).Scan(&epoch); err != nil {
		t.Fatalf("read current_attempt_epoch of %s: %v", wiID, err)
	}
	return epoch
}

// ftsWorkItemStatus reads work_items.status.
func ftsWorkItemStatus(t *testing.T, pool *pgxpool.Pool, wiID string) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(context.Background(),
		`SELECT status FROM work_items WHERE id=$1`, wiID).Scan(&status); err != nil {
		t.Fatalf("read status of %s: %v", wiID, err)
	}
	return status
}

// ftsAttemptSecretHash reads run_attempts.session_secret_hash.
func ftsAttemptSecretHash(t *testing.T, pool *pgxpool.Pool, attemptID string) string {
	t.Helper()
	var hash string
	if err := pool.QueryRow(context.Background(),
		`SELECT session_secret_hash FROM run_attempts WHERE id=$1`, attemptID).Scan(&hash); err != nil {
		t.Fatalf("read session_secret_hash of %s: %v", attemptID, err)
	}
	return hash
}

// ftsLockKeysOf lists every resource_locks key one attempt owns, of any type.
func ftsLockKeysOf(t *testing.T, pool *pgxpool.Pool, attemptID string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT resource_type || ':' || resource_key FROM resource_locks
		WHERE owner_attempt_id=$1 ORDER BY 1`, attemptID)
	if err != nil {
		t.Fatalf("read locks of %s: %v", attemptID, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scan lock of %s: %v", attemptID, err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate locks of %s: %v", attemptID, err)
	}
	return out
}

// ftsLockRowsFor counts the resource_locks rows carrying one file_scope key,
// whoever owns them. Distinct from ftsLockOwner, which fails when there is no row:
// "the row is gone" is the assertion here, not "somebody else has it".
func ftsLockRowsFor(t *testing.T, pool *pgxpool.Pool, key string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM resource_locks
		WHERE resource_type='file_scope' AND resource_key=$1`, key).Scan(&n); err != nil {
		t.Fatalf("count rows for file_scope:%s: %v", key, err)
	}
	return n
}

// ftsLockTypesOf lists the resource_type of every row one attempt owns.
func ftsLockTypesOf(t *testing.T, pool *pgxpool.Pool, attemptID string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT resource_type FROM resource_locks WHERE owner_attempt_id=$1 ORDER BY 1`, attemptID)
	if err != nil {
		t.Fatalf("read lock types of %s: %v", attemptID, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatalf("scan lock type of %s: %v", attemptID, err)
		}
		out = append(out, typ)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate lock types of %s: %v", attemptID, err)
	}
	return out
}

// ftsForceTakeoverEvents returns the decoded payload of every force_takeover
// event on one work item's timeline, oldest first.
func ftsForceTakeoverEvents(t *testing.T, pool *pgxpool.Pool, wiID string) []map[string]any {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT payload FROM agent_events
		WHERE work_item_id=$1 AND event_type='force_takeover' ORDER BY created_at, id`, wiID)
	if err != nil {
		t.Fatalf("read force_takeover events of %s: %v", wiID, err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scan force_takeover event of %s: %v", wiID, err)
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("decode force_takeover payload of %s: %v", wiID, err)
		}
		out = append(out, payload)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate force_takeover events of %s: %v", wiID, err)
	}
	return out
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
