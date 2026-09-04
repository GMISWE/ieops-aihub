package domain

// DB-gated integration test for aihub#355: cancelling a work item must release
// every resource lock still held on its behalf.
//
// # What was wrong, exactly
//
// Every OTHER way an attempt stops holding locks moves the ATTEMPT's status,
// and the orphan sweep is written against exactly that: it deletes a lock only
// when the owner attempt is no longer 'running' or 'paused'. CancelWorkItem
// moved the WORK ITEM to 'cancelled' with a single UPDATE and left run_attempts
// untouched.
//
// That is only a leak because two other decisions meet on it, neither of them
// wrong on its own:
//
//   - cancelGate ADMITS status='paused' — deliberately, since aihub#242 made
//     cancel the missing exit for a wi with nowhere else to go.
//   - a pause releases file_scope ONLY and RETAINS git_branch / deploy_env /
//     worktree / tcp_port, so that a resume can go on holding the branch.
//
// So pause-then-cancel left the retained types owned by an attempt still marked
// 'paused': inside the retention predicate, therefore skipped by the sweep,
// forever. And every API path that could have released them refuses a terminal
// work item — claim rejects 'cancelled', force_takeover rejects anything not
// running, complete_attempt needs a live attempt. There was no way back.
//
// # Why the blast radius is bigger than one work item
//
// A `{"type":"repo"}` entry with no task_branch derives the git_branch key
// `<repo>/main`, and the claim conflict probe matches holders whose attempt is
// 'running' or 'paused'. One pause-then-cancel of a wi that declared a repo
// therefore blocks EVERY later claim declaring that repo's default branch, with
// no holder anybody can talk to — the wi is cancelled. deploy_env keys are
// shared by name and leak the same way.
//
// # What the assertions end on
//
// Not the resource_locks table. Following aihub#264's file, each arm ends at
// the hop the blocked party can actually observe — a real FnClaimWorkItem by a
// second work item — or at aihub#343's stricter bar, a verdict computed from
// the event stream ALONE. A test that stopped at the table would prove the row
// went away, not that anybody can tell.
//
// Run:
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestCancelWorkItemReleasesEveryLockItStillHolds' -race -v -count=1

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCancelWorkItemReleasesEveryLockItStillHolds is aihub#355's acceptance
// criterion.
//
// One function with subtests: dbtestcov counts DB-gated FUNCTIONS, and the
// per-arm claims belong in subtest names, which ci.yml asserts on individually.
func TestCancelWorkItemReleasesEveryLockItStillHolds(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	u := testUser(t, pool)
	project := testProject(t, pool, u)

	const holderPath = "internal/domain/cancel355.go"
	const bystanderPath = "internal/domain/bystander355.go"
	const branch = "pf355-cancel"
	const svc = "pf355-cancel-env"

	// The ":aihub:" segment is the repo (aihub#261): the payload below declares
	// {"type":"repo","uri":"repo:aihub"}, so the repo-relative path inherits it.
	holderPathKey := project + ":aihub:" + holderPath
	branchKey := "aihub/" + branch
	envKey := svc
	// The bystander declares no repo entry, so its key stays unqualified.
	bystanderKey := project + ":" + bystanderPath

	declared := `[{"type":"repo","uri":"repo:aihub","intent":"write","task_branch":"` + branch + `"},` +
		`{"type":"service","uri":"service:` + svc + `","intent":"write"},` +
		`{"type":"path","uri":"file:` + holderPath + `","intent":"write"}]`

	holder := seedClaimableWI(t, pool, project, u,
		"release every lock a cancelled work item still holds", declared)
	claim, aerr := claimWI(t, pool, u, holder.ID, "aihub355-cancel-claim")
	require.Nil(t, aerr, "claim failed: %+v", aerr)

	require.ElementsMatch(t, []string{branchKey, envKey, holderPathKey},
		heldLockKeys(t, pool, claim.AttemptID),
		"fixture check: the claim must really have taken all three lock TYPES, or nothing below "+
			"is measuring a release of the two that a pause retains")

	// A second work item holding a lock of its own, claimed while the holder is
	// still live. It is the control for releaseCancelledWILocksSQL's
	// `ra.work_item_id = $1` scoping: the release reaches across claim epochs,
	// so "every lock of this work item" is one edit away from "every lock".
	bystander := seedClaimableWI(t, pool, project, u,
		"a work item whose lock the cancel must not touch",
		`[{"type":"path","uri":"file:`+bystanderPath+`","intent":"write"}]`)
	bystanderClaim, aerr := claimWI(t, pool, u, bystander.ID, "aihub355-bystander-claim")
	require.Nil(t, aerr, "bystander claim failed: %+v", aerr)
	require.Equal(t, []string{bystanderKey}, heldLockKeys(t, pool, bystanderClaim.AttemptID),
		"fixture check: the bystander must really hold its lock before the cancel")

	// The pause is what makes the leak reachable at all.
	require.Nil(t, FnCompleteAttempt(ctx, pool, holder.ID, &CompleteAttemptRequest{
		AttemptID: claim.AttemptID, ClaimEpoch: claim.ClaimEpoch,
		SessionSecret: testSecret, Status: "paused",
	}), "pause failed")

	// 🔴 NEGATIVE CONTROL, and the reason this test can conclude anything from an
	// absence later. "the lock is gone" and "this query cannot see this lock" are
	// the same empty result, and mistaking one for the other is the measurement
	// error aihub#355 was filed about. This line proves heldLockKeys DOES see the
	// git_branch and deploy_env locks while they are held — after a release path
	// (the pause) has already run and taken the file_scope one. So the emptiness
	// asserted after the cancel is evidence about the locks, not about the query.
	require.ElementsMatch(t, []string{branchKey, envKey},
		heldLockKeys(t, pool, claim.AttemptID),
		"negative control: a pause must RETAIN git_branch and deploy_env (and release only "+
			"file_scope). If this fails, either the retention contract changed or heldLockKeys "+
			"cannot see these types — and then the post-cancel emptiness below proves nothing")

	// The party those retained locks were blocking, measured BEFORE the cancel.
	// Without this the unblocking asserted below would be consistent with the
	// lock never having blocked anybody.
	blocker := seedClaimableWI(t, pool, project, u,
		"the claim a cancelled work items retained git_branch lock blocks",
		`[{"type":"repo","uri":"repo:aihub","intent":"write","task_branch":"`+branch+`"}]`)
	blockedBefore, codeBefore := claimBlocks(t, pool, blocker.ID, u, "aihub355-blocked-before")
	require.True(t, blockedBefore,
		"fixture check: a second work item declaring branch %q must be blocked BEFORE the cancel, "+
			"or the unblocking asserted below proves nothing. got code=%q", branch, codeBefore)

	// MUTANT: internal/domain/work_items.go, CancelWorkItem — delete the
	// releaseLocks(releaseCancelledWILocksSQL, ...) call (or revert the function
	// to its single `UPDATE work_items SET status='cancelled'` form). The first three
	// subtests go red; "the cancel does not touch another work item's lock" stays
	// green, which is what separates "the cancelled item's locks are released"
	// from "locks stopped being held at all".
	require.Nil(t, CancelWorkItem(ctx, pool, holder.ID, u, "", map[string]string{}),
		"cancel failed")

	t.Run("a cancelled work item holds no lock of any type", func(t *testing.T) {
		require.Empty(t, heldLockKeys(t, pool, claim.AttemptID),
			"the cancelled work item's attempt still holds locks. A pause retains "+
				"git_branch/deploy_env for a resume that a cancelled work item can never have, "+
				"and the orphan sweep skips them because the attempt is still 'paused'")
	})

	t.Run("the release is decidable from the event stream alone", func(t *testing.T) {
		events := projectEvents(t, pool, project)
		for _, tc := range []struct{ typ, key string }{
			{"git_branch", branchKey},
			{"deploy_env", envKey},
		} {
			v := lockVerdictFromEvents(events, tc.typ, tc.key)
			require.True(t, v.Decidable,
				"the stream says nothing about %s %q, so a reviewer cannot tell a release "+
					"from a leak — which is the whole complaint in aihub#355", tc.typ, tc.key)
			require.False(t, v.Held,
				"the stream still reads %s %q as HELD after the cancel", tc.typ, tc.key)
			require.Equal(t, lockCauseWICancelled, v.Cause,
				"the release of %s %q is recorded under cause %q; a reader chasing "+
					"%q would look for a wrapped/failed attempt and find one still marked paused",
				tc.typ, tc.key, v.Cause, lockCauseAttemptTerminal)
		}
	})

	t.Run("the party those locks were blocking can now claim", func(t *testing.T) {
		blocked, code := claimBlocks(t, pool, blocker.ID, u, "aihub355-blocked-after")
		require.False(t, blocked,
			"still blocked after the holder was cancelled — with the holder terminal there is "+
				"no attempt to take over and no session to ask, so this is permanent. got code=%q", code)
	})

	t.Run("the cancel does not touch another work items lock", func(t *testing.T) {
		require.Equal(t, []string{bystanderKey}, heldLockKeys(t, pool, bystanderClaim.AttemptID),
			"cancelling one work item released a lock belonging to another — the release must be "+
				"scoped by work_item_id")
	})
}
