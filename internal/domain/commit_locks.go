package domain

// aihub#366 — the commit-time lock gate.
//
// # The gap this closes
//
// declared_resources is filled in when a problem is REPORTED, so it describes
// where the problem is. Locks have to cover where the FIX lands, and the two are
// almost never the same set. Measured over four consecutive work items on
// 2026-09-05/06: locks held at claim were 2 / 2 / 0 / 0 while the files actually
// changed were 5 / 9 / 20 / 4. One of them (aihub#357) declared an empty array,
// so it ran with zero protection over a file another work item had declared;
// another (aihub#365) found a fourth file only halfway through the edit, because
// inserting a line into a hook silently invalidated a test's mutation literal.
//
// Every one of those blast radii happened to converge, by luck and by executors
// volunteering pf_acquire_locks. This makes it mechanical.
//
// # What it does
//
// At commit time the caller sends the paths the pending commit contains.
// FnReconcileCommitLocks compares them against the file_scope locks THIS ATTEMPT
// ACTUALLY HOLDS — read from resource_locks, not re-derived from
// declared_resources — and:
//
//	difference empty      -> nothing is written; the commit proceeds
//	difference free       -> the locks are taken and the commit proceeds
//	difference held       -> 409, naming every holder; nothing is committed
//
// The owner's decision (2026-09-06), and the two rejected alternatives, because
// the shape of this endpoint is that decision:
//
//   - WARN-ONLY was rejected: a prompt that can be ignored is, measurably,
//     equivalent to not existing. That is why this returns an error the commit
//     path cannot proceed through rather than a field the caller may read.
//   - HARD-BLOCK-WITH-MANUAL-FIX was rejected: it fires after the work is
//     already done, and at that point acquiring the missing lock can fail
//     because somebody else holds it — leaving the author with changes in hand,
//     no lock, and no commit. That is why the auto-acquire comes FIRST and the
//     interruption is reserved for the case a human actually has to resolve.
//
// # 🔴 Why the acquired locks are NOT written into declared_resources
//
// The obvious follow-through — append the discovered paths to
// declared_resources so the record matches reality — is a trap, and it is the
// opposite of harmless. aihub#264 releases the file_scope locks in
// `prior − next` on every declared_resources update, and pf-plan Step 5 rewrites
// declared_resources as a WHOLE-LIST REPLACE of path entries. So a path written
// in here would be released by the next /pf-plan, whereas a lock with no
// declaration behind it is in neither side of that subtraction and survives
// until the attempt ends.
//
// Declaring the paths would therefore make the record prettier and the
// PROTECTION WEAKER. An undeclared lock is a supported population — see
// AcquireLocksResponse.AlreadyHeld, which exists to report exactly these — and
// the audit trail is the lock_acquired event this file emits with
// cause=commit_gate, which says what was taken and why without touching the
// declaration.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReconcileCommitLocksRequest is the body for
// POST /v1/work_items/:id/commit_locks.
type ReconcileCommitLocksRequest struct {
	AttemptID     string `json:"attempt_id"`
	ClaimEpoch    int64  `json:"claim_epoch"`
	SessionSecret string `json:"session_secret"`

	// Repo is the repository the paths are relative to, and it is REQUIRED —
	// the one field here whose absence would be worse than an error.
	//
	// declared_resources paths are repo-relative, so an unqualified file_scope
	// probe carries a `<project>:%:<path>` LIKE arm that matches EVERY repo's
	// copy of the path (fileScopeConflictProbe). Against another work item that
	// arm is conservative: it over-blocks. Against THIS attempt's own locks,
	// which is what coverage is computed from, it inverts — holding
	// `<project>:other-repo:go.mod` would report this repo's go.mod as already
	// covered and let it through unlocked. Over-blocking is noise; that is a
	// silent hole, so the repo is demanded rather than guessed.
	Repo string `json:"repo"`

	// Paths are the repo-relative paths the pending commit WRITES, as produced
	// by coding.GitPendingCommitPaths.
	//
	// ⚠️ "WRITES" IS NARROWER THAN "CHANGES", and on a merge the difference is a
	// gap this gate cannot see. GitPendingCommitPaths intersects the staged set
	// against every parent, so a path resolved with `--ours` — whose index entry
	// EQUALS HEAD — is absent from this slice, and a merge that discards a file
	// another live attempt holds passes the gate unexamined. That is stated here,
	// on the field the gate consumes, because the gate's own refusals are read
	// here and its blind spots are documented one package away.
	//
	// It is ACCEPTED, and re-confirmed as accepted by aihub#679's review rather
	// than rediscovered by it: coding.GitPendingCommitPaths' doc comment already
	// names this residue, names the two cases that make it reachable (file_scope
	// locks are released on PAUSE while the branch survives, and a plain `git
	// commit` never passes through the gate at all), and records why the
	// alternative is worse — "counting every path that differs from ANY parent is
	// the un-narrowed set this function exists to replace, and that set is what
	// refused aihub#654 over three files it never touched". Widening it back is a
	// change with a measured false-positive cost, not a correction.
	//
	// The direction is the reason it can wait: this is the gate seeing LESS than
	// the whole staged set, which is strictly narrower than the behaviour it
	// replaced. A residue, not a regression.
	Paths []string `json:"paths"`

	// Merge reports that the pending commit is a merge commit — a merge is in
	// progress in the caller's worktree (aihub#662).
	//
	// 🔴 IT CHANGES THE REMEDY, NOT THE DECISION. Coverage, acquisition and the
	// 409 are computed identically either way; what it selects is which advice a
	// refusal ships, because the ordinary one — unstage the blocked paths, retry
	// with the rest — SILENTLY DESTROYS DATA on a merge. Measured in a scratch
	// repo: `git restore --staged <a path the other side brought in>` followed
	// by the narrowed retry produced a merge commit with that file absent from
	// the tree; `git show --stat` on the merge listed only the other file, so
	// the deletion is invisible in the merge commit's own diff, while
	// `git diff --name-status HEAD^2 HEAD` reported `D theirs.txt` — which is
	// exactly what lands on the base branch when the PR merges.
	//
	// It defaults to false, and that default is the SAFE direction for an
	// ordinary commit and the unsafe one for a merge. It is accepted because the
	// alternative — deriving merge-ness server-side — is not available: MERGE_HEAD
	// lives in the caller's worktree and the server has never seen it. A client
	// too old to send this field gets today's behaviour exactly.
	Merge bool `json:"merge"`
}

// ReconcileCommitLocksResponse reports what the gate found and what it took.
type ReconcileCommitLocksResponse struct {
	// Checked is how many distinct paths were examined.
	Checked int `json:"checked"`
	// Covered lists the paths a lock this attempt already held covered. On the
	// common path this is every path and Acquired is empty.
	Covered []string `json:"covered"`
	// Probed counts the paths this call had to take to the lock table because no
	// held lock covered them — the SIZE OF THE WORK the gate did, as opposed to
	// the size of its input.
	//
	// It is reported rather than left implicit because "an already-covered commit
	// costs nothing" is a promise, and a promise nobody can measure is one that
	// degrades silently. A gate that stopped computing coverage and simply
	// re-probed every path on every commit would still write nothing (the probe
	// finds the lock is already ours and no-ops), so lock-row and event counts
	// cannot tell the two apart. This can: Probed is 0 exactly when the change
	// set was already inside the lock set.
	Probed int `json:"probed"`
	// AcquiredPaths lists the paths this call took a lock for — the difference
	// that had gone undeclared. Empty means the call wrote nothing at all.
	AcquiredPaths []string `json:"acquired_paths"`
	// Acquired is the lock rows behind AcquiredPaths.
	Acquired []ResourceLock `json:"acquired"`
}

// commitLockConflict is one path the gate could not take because a different
// live attempt holds it.
type commitLockConflict struct {
	Path         string `json:"path"`
	ResourceKey  string `json:"resource_key"`
	AttemptID    string `json:"attempt_id"`
	ActorDisplay string `json:"actor_display"`
	WorkItemSlug string `json:"work_item_slug"`
}

// heldFileScopeKeysSQL reads the file_scope lock keys one attempt holds.
//
// 🔴 This is the whole point of the endpoint and the easiest thing to get
// wrong: the gate must compare against the LOCK TABLE, not against
// declared_resources. The two disagree in both directions — an attempt can hold
// locks it never declared (pf_acquire_locks widened aihub#357 from 0 to 22
// mid-attempt; claim-time git_branch locks were never declared either), and it
// can declare paths whose locks a narrowing has since released. A gate reading
// the declaration would compare today's changes against a list that describes
// intent, which is exactly the mismatch this work item exists to remove.
const heldFileScopeKeysSQL = `
	SELECT resource_key FROM resource_locks
	WHERE owner_attempt_id = $1 AND resource_type = 'file_scope'`

// FnReconcileCommitLocks is the commit gate: it reports which of `paths` this
// attempt's file_scope locks already cover, acquires the rest, and refuses the
// whole call if any of the rest belongs to another live attempt.
//
// It is all-or-nothing by construction. Every conflict is collected BEFORE any
// lock is inserted, so a refusal names every path the author has to sort out
// rather than the first one, and the transaction that would have taken the
// others rolls back — a commit is not half-protected.
func FnReconcileCommitLocks(ctx context.Context, pool *pgxpool.Pool, wiID string,
	req *ReconcileCommitLocksRequest) (*ReconcileCommitLocksResponse, *AihubError) {

	if req.Repo == "" {
		return nil, NewErrDetails(ErrBadRequest,
			"repo is required: an unqualified file_scope probe matches every repo's copy of a path, "+
				"so coverage computed without it can report a file as already locked when the lock names a different repo",
			map[string]any{"hint": `send the same repo name pf_commit/pf_ship was called with, e.g. "aihub"`})
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, NewErr(ErrInternalError, "failed to begin transaction")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// No FOR UPDATE, unlike FnAcquireLocks. That endpoint reads and re-derives
	// declared_resources and must not race an update of it; this one never looks
	// at the declaration, so the row lock would buy nothing and would turn the
	// covered-everything path — the common one, run on every commit — into a
	// writer against the work item row.
	var wi WorkItem
	err = tx.QueryRow(ctx, `
		SELECT id, project, status, current_attempt_id, current_attempt_epoch
		FROM work_items WHERE (id = $1 OR slug = $1)`, wiID,
	).Scan(&wi.ID, &wi.Project, &wi.Status, &wi.CurrentAttemptID, &wi.CurrentAttemptEpoch)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, NewErr(ErrNotFound, fmt.Sprintf("work item %q not found", wiID))
		}
		if aerr := retryConflictErr(err, "failed to read work_item"); aerr != nil {
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "failed to read work_item")
	}

	if wi.Status != "running" {
		return nil, NewErr(ErrAttemptMismatch,
			fmt.Sprintf("work item status is %q; only a running work item holds locks to reconcile", wi.Status))
	}
	if aihubErr := verifyAttemptCredential(ctx, tx, wi, req.AttemptID, req.ClaimEpoch, req.SessionSecret); aihubErr != nil {
		return nil, aihubErr
	}

	held, aerr := heldFileScopeKeys(ctx, tx, req.AttemptID)
	if aerr != nil {
		return nil, aerr
	}

	// Split into covered / missing. Deduplicate on the way: two entries naming
	// the same path (a caller that did not dedupe) must not produce two probes
	// and two identical event rows.
	type missingPath struct {
		path  string
		key   string
		probe lockConflictProbe
	}
	covered := make([]string, 0, len(req.Paths))
	missing := make([]missingPath, 0)
	seen := make(map[string]bool, len(req.Paths))
	for _, p := range req.Paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true

		item := DeclaredResourceItem{Type: "path", URI: "file:" + p, Repo: req.Repo, Intent: "write"}
		lockType, lockKey, probe := derivedLockProbe(item, wi.Project)
		if lockType != "file_scope" || lockKey == "" {
			// Unreachable for a well-formed path entry, and skipped rather than
			// asserted: a path this mapper cannot key is a path no lock can
			// protect, and failing the commit over it would be a gate blocking on
			// its own inability to act.
			continue
		}
		if anyMatches(probe, held) {
			covered = append(covered, p)
			continue
		}
		missing = append(missing, missingPath{path: p, key: lockKey, probe: probe})
	}

	resp := &ReconcileCommitLocksResponse{
		Checked:       len(seen),
		Covered:       covered,
		AcquiredPaths: make([]string, 0),
		Acquired:      make([]ResourceLock, 0),
	}

	// 🔴 The zero-overhead path: with nothing missing this call has performed no
	// INSERT, no DELETE, no event and no declared_resources write, and — see
	// Probed — no collision probe either. Commits do not each drag a round of
	// lock-taking behind them; work happens only when the change set genuinely
	// outran the lock set.
	//
	// ⚠️ This early return is an OPTIMISATION, not the guarantee. Both loops
	// below iterate `missing`, so deleting these four lines changes nothing
	// observable, and no test can be written that distinguishes them. The
	// guarantee is that `missing` is computed from coverage; the mutant that
	// breaks it is one that stops computing coverage, which Probed catches.
	if len(missing) == 0 {
		if err := tx.Commit(ctx); err != nil {
			if aerr := retryConflictErr(err, "failed to commit lock reconcile"); aerr != nil {
				return nil, aerr
			}
			return nil, NewErr(ErrInternalError, "failed to commit lock reconcile")
		}
		return resp, nil
	}

	// Phase 1 — probe every missing path before taking anything.
	//
	// Collecting all conflicts first is what makes the refusal usable. Failing at
	// the first one would send the author round the loop once per conflicting
	// file, and each trip costs them a re-run of the commit.
	var conflicts []commitLockConflict
	stillMissing := missing[:0:0]
	for _, m := range missing {
		resp.Probed++
		var ownerAttemptID, ownerActorDisplay, ownerWISlug string
		scanErr := tx.QueryRow(ctx, acquireLocksCollisionSQL, "file_scope", m.probe.Keys, m.probe.LikePattern).
			Scan(&ownerAttemptID, &ownerActorDisplay, &ownerWISlug)
		switch {
		case scanErr == nil && ownerAttemptID == req.AttemptID:
			// Ours after all, under a key form the coverage test did not see.
			// Not reachable today (the Go probe mirrors the SQL one), and treated
			// as covered rather than acquired because inserting a second row for
			// a file we already hold is the "same path, twice, under two names"
			// hazard aihub#261 documents.
			covered = append(covered, m.path)
		case scanErr == nil:
			conflicts = append(conflicts, commitLockConflict{
				Path:         m.path,
				ResourceKey:  m.key,
				AttemptID:    ownerAttemptID,
				ActorDisplay: ownerActorDisplay,
				WorkItemSlug: ownerWISlug,
			})
		case errors.Is(scanErr, pgx.ErrNoRows):
			stillMissing = append(stillMissing, m)
		default:
			if aerr := retryConflictErr(scanErr, "failed to check lock collision"); aerr != nil {
				return nil, aerr
			}
			return nil, NewErr(ErrInternalError,
				fmt.Sprintf("failed to check lock collision for file_scope:%s", m.key))
		}
	}
	if len(conflicts) > 0 {
		return nil, commitLockConflictErr(conflicts, req.Merge)
	}
	resp.Covered = covered

	// Phase 2 — take the free ones.
	op := newLockOp(lockCauseCommitGate, lockEventActor{})
	for _, m := range stillMissing {
		took, execErr := acquireLockIfFree(ctx, tx, "file_scope", m.key,
			req.AttemptID, req.ClaimEpoch, wi.Project, wi.ID, op)
		if execErr != nil {
			return nil, dbErrCause(execErr, fmt.Sprintf("failed to acquire lock file_scope:%s", m.key))
		}
		if !took {
			// DO NOTHING hit a row phase 1 did not see: either a concurrent
			// acquisition, or an orphan whose owning attempt is not live (phase 1
			// filters on running/paused, so an orphan reads as ErrNoRows there).
			var raceOwnerID, raceActor, raceSlug string
			reScanErr := tx.QueryRow(ctx, acquireLocksCollisionSQL, "file_scope", m.probe.Keys, m.probe.LikePattern).
				Scan(&raceOwnerID, &raceActor, &raceSlug)
			switch {
			case reScanErr == nil && raceOwnerID == req.AttemptID:
				resp.Covered = append(resp.Covered, m.path)
				continue
			case reScanErr == nil:
				return nil, commitLockConflictErr([]commitLockConflict{{
					Path: m.path, ResourceKey: m.key, AttemptID: raceOwnerID,
					ActorDisplay: raceActor, WorkItemSlug: raceSlug,
				}}, req.Merge)
			case errors.Is(reScanErr, pgx.ErrNoRows):
				// An orphan from a crashed attempt the gc sweep has not reached.
				// Reclaimed on the same terms as FnAcquireLocks: the release is
				// filed under the DEAD row's own work item, not this one, so the
				// person wondering where their lock went can find it.
				reclaimOp := newLockOp(lockCauseOrphanReclaim, lockEventActor{}).withExtra(map[string]any{
					"reclaimed_by_attempt_id": req.AttemptID,
					"reclaimed_by_cause":      lockCauseCommitGate,
				})
				if _, delErr := releaseLocks(ctx, tx, lockDeleteByKeySQL, reclaimOp,
					"file_scope", m.key); delErr != nil {
					return nil, dbErr(delErr, fmt.Sprintf("failed to reclaim orphan lock file_scope:%s", m.key))
				}
				if _, insErr := acquireLockIfFree(ctx, tx, "file_scope", m.key,
					req.AttemptID, req.ClaimEpoch, wi.Project, wi.ID, reclaimOp); insErr != nil {
					return nil, dbErr(insErr, fmt.Sprintf("failed to acquire reclaimed lock file_scope:%s", m.key))
				}
			default:
				if aerr := retryConflictErr(reScanErr, "failed to re-check lock owner"); aerr != nil {
					return nil, aerr
				}
				return nil, NewErr(ErrInternalError,
					fmt.Sprintf("failed to re-check lock owner for file_scope:%s", m.key))
			}
		}
		resp.AcquiredPaths = append(resp.AcquiredPaths, m.path)
		resp.Acquired = append(resp.Acquired, ResourceLock{
			ResourceType:   "file_scope",
			ResourceKey:    m.key,
			OwnerAttemptID: req.AttemptID,
			ClaimEpoch:     req.ClaimEpoch,
		})
	}

	if err := tx.Commit(ctx); err != nil {
		if aerr := retryConflictErr(err, "failed to commit lock reconcile"); aerr != nil {
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "failed to commit lock reconcile")
	}
	return resp, nil
}

// heldFileScopeKeys reads the file_scope lock keys owned by attemptID.
func heldFileScopeKeys(ctx context.Context, tx pgx.Tx, attemptID string) ([]string, *AihubError) {
	rows, err := tx.Query(ctx, heldFileScopeKeysSQL, attemptID)
	if err != nil {
		if aerr := retryConflictErr(err, "failed to list held locks"); aerr != nil {
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "failed to list held locks")
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var k string
		if scanErr := rows.Scan(&k); scanErr != nil {
			if aerr := retryConflictErr(scanErr, "failed to scan held locks"); aerr != nil {
				return nil, aerr
			}
			return nil, NewErr(ErrInternalError, "failed to scan held locks")
		}
		keys = append(keys, k)
	}
	// aihub#334: a streamed result set's error has no other exit, and this
	// transaction is SERIALIZABLE so 40001 is reachable here. Without this the
	// loop merely looks empty — and "this attempt holds no locks" is the one
	// answer that turns the gate into an acquisition of everything.
	if err := rows.Err(); err != nil {
		if aerr := retryConflictErr(err, "failed to list held locks"); aerr != nil {
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "failed to list held locks")
	}
	return keys, nil
}

// anyMatches reports whether any held key satisfies the probe.
func anyMatches(probe lockConflictProbe, held []string) bool {
	for _, k := range held {
		if probe.Matches(k) {
			return true
		}
	}
	return false
}

// commitLockConflictErr renders the refusal.
//
// `conflicts` is the full list; `conflict_with` repeats the first entry in the
// shape FnClaimWorkItem and FnAcquireLocks already use, so a caller that keys on
// the established field keeps working rather than silently reading nothing.
//
// The remedy it ships is chosen by CommitLockAdviceFor, because there are two
// and one of them destroys data when applied to the other's case.
func commitLockConflictErr(conflicts []commitLockConflict, merge bool) *AihubError {
	paths := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		paths = append(paths, c.Path)
	}
	first := conflicts[0]
	return NewErrDetails(ErrConflictLockTaken,
		fmt.Sprintf("this commit changes %d file(s) locked by another attempt: %v, "+
			"held by %s on %s (attempt %s)", len(conflicts), paths,
			displayOrUnknown(first.ActorDisplay), slugOrUnknown(first.WorkItemSlug), first.AttemptID),
		map[string]any{
			"conflicts": conflicts,
			"conflict_with": map[string]any{
				"attempt_id":     first.AttemptID,
				"actor_display":  first.ActorDisplay,
				"work_item_slug": first.WorkItemSlug,
			},
			"blocked_paths": paths,
			"advice":        CommitLockAdviceFor(merge),
		})
}

// CommitLockAdviceFor returns the remedy a CONFLICT_LOCK_TAKEN refusal ships,
// which is NOT one string (aihub#662).
//
// 🔴 THE TWO ARE NOT VARIANTS OF ONE SENTENCE, they are remedies for different
// situations, and applying either to the other's case is a defect rather than a
// stylistic mismatch: the ordinary one deletes another work item's files on a
// merge, and the merge one lands no commit at all on an ordinary one. That is
// why this is a function with a decision in it and not a `+ mergeSuffix`.
//
// Exported so the wire tests in internal/mcp run the SHIPPED constants rather
// than copies of them.
func CommitLockAdviceFor(merge bool) string {
	if merge {
		return CommitLockMergeRefusalAdvice
	}
	return CommitLockRefusalAdvice
}

// CommitLockRefusalAdvice is the remedy a CONFLICT_LOCK_TAKEN refusal ships in
// `details.advice`.
//
// 🔴 THE REMEDY IS TWO STEPS AND BOTH ARE WRITTEN DOWN, because step 1 alone
// does not work and two earlier versions of this string each stopped after one
// of them. `pf_commit(paths=[...])` alone narrows nothing — coding.GitStage only
// ever ADDS, and the refusal has already left the index fully populated. But
// `git restore --staged` alone does not finish the job either: the retry it
// sends you to make re-runs GitStage, which with no `paths` is `git add -A`
// (internal/coding/git_ops.go:39-52) and re-stages the file you just removed.
// GitStagedPaths reads INDEX vs HEAD, so the identical set goes back over the
// wire. Measured through the real pf_commit tool against a fake server that
// refuses iff `paths` contains contested.txt:
//
//	index after the refusal                            [contested.txt mine.txt]
//	index after `git restore --staged contested.txt`   [mine.txt]
//	then a PLAIN retry    paths sent [contested.txt mine.txt]  409 again, HEAD unmoved
//	then a paths=[] retry paths sent [mine.txt]                acquired,  HEAD moved
//
// So the two steps are not alternatives, and neither is a preference: step 2 is
// load-bearing and saying only step 1 costs the author a retry to arrive back
// where they started.
//
// ⚠️ THE NUMBERED "(1) … (2) …" SHAPE IS PART OF THE CONTRACT, not styling.
// TestCommitGateWire_RefusalAdviceIsExecutableAndWorks parses THIS constant into
// its numbered steps, executes each one against a real refused worktree through
// the real pf_commit tool, and requires the commit to land — with the plain
// un-narrowed retry as its red control. That is the only kind of assertion that
// can catch this defect class: the substring assertions that used to guard this
// string stayed green against a rewrite that mentioned `git restore --staged`
// and then recommended the broken remedy anyway, and against one that forbade
// it outright. Prose that merely CONTAINS the right words is not a remedy. A
// rewrite that drops the numbering fails that test rather than silently
// escaping it.
//
// Byte budget — measured, and it is not the one an earlier version enforced.
// pkg/client.formatDetails truncates the compacted `details` at
// client.DetailsRenderLimit with keys sorted alphabetically. `advice` sorts
// FIRST, so the envelope total is not the constraint: with realistic values the
// envelope is 606 bytes at n=1 conflict even with a 237-byte advice, already
// past the cap, and the cut lands inside `conflicts` — the only key carrying
// per-path holders, and one that is therefore ALREADY lost at n=1 no matter how
// short this string is. `blocked_paths` and `conflict_with` are the other two
// keys the cut can reach, and neither carries a value the untruncated Message
// just above does not already state — the paths, and the first holder's actor /
// work item / attempt. So bytes spent here buy back nothing a caller can
// otherwise see, and the 237-byte
// discipline bought nothing while being exactly what kept the remedy
// incomplete. What the budget must protect is THIS string, which sorts first
// and is therefore cut in half rather than pushed off the end; half a remedy
// reads complete, which is worse than a short one.
//
// ⚠️ The limit is computed, not written down here. An earlier version of this
// paragraph asserted "489 bytes" and aihub#662 falsified it: the gate measures
// client.DetailsRenderLimit (2048) minus the 11-byte `{"advice":"` prefix, so
// the budget is 2037, and the merge constant below is already past 489 while
// passing. TestCommitLockConflictErr_AdviceSurvivesTheRenderLimit computes it
// from the constant and logs what it measured; read the log, not a number in
// prose.
const CommitLockRefusalAdvice = "Those files belong to another live attempt. Do NOT force a takeover: " +
	"the holder is editing them. Either wait for it to finish, or take them out of this commit, " +
	"which takes TWO steps: a plain retry re-runs `git add -A` and re-stages exactly what step 1 " +
	"removed, earning the same refusal. " +
	"(1) `git restore --staged <blocked paths>` " +
	"(2) retry with paths=[the files that are left]."

// CommitLockMergeRefusalAdvice is what a refusal ships when the pending commit
// is a MERGE (aihub#662). It exists because the string above is DESTRUCTIVE
// here, not merely unhelpful.
//
// 🔴 WHAT FOLLOWING THE ORDINARY REMEDY ON A MERGE ACTUALLY DOES, measured in a
// scratch repo on git 2.43.0 rather than reasoned about. The branch adds
// mine.txt; the other side adds theirs.txt and edits shared.txt; the merge is
// refused over theirs.txt because another attempt holds it:
//
//	git restore --staged theirs.txt   index vs HEAD -> [shared.txt]
//	retry paths=[shared.txt]          merge commit created, 2 parents
//	git show --stat <merge>            shared.txt | 1 +      <- theirs.txt ABSENT
//	git diff --name-status HEAD^2 HEAD A mine.txt / D theirs.txt
//
// `git restore --staged` resets the index entry to HEAD, and on a merge HEAD is
// only the FIRST parent — so for a path the other side introduced there is
// nothing to reset TO, and the entry disappears. The merge then records a
// DELETION of a file the holder had just landed, and the combined diff a
// reviewer looks at does not show it. When the PR merges, that deletion is what
// reaches the base branch. aihub#654 was handed this exact instruction twice
// and declined it; nothing was lost because its executor was careful, which is
// not a property of the gate.
//
// ⚠️ IT DELIBERATELY NUMBERS NO STEPS, and that is a contract statement rather
// than an omission. CommitLockRefusalAdvice's "(1) … (2) …" grammar exists so
// TestCommitGateWire_RefusalAdviceIsExecutableAndWorks can EXECUTE it and
// require the commit to land. There is no such recipe here, because every local
// sequence that lands THIS commit either keeps the contested file (the refusal
// stands) or drops it (the deletion above). The remedy is to stop and get the
// lock released, so a numbered recipe would be a lie in executable form — the
// worst of the three shapes that file's header catalogues. adviceRecipe returns
// nothing for this string, by design, and
// TestCommitGateWire_MergeRefusalIsNotTheDestructiveRemedy is what keeps that
// from being a silent vacuum: it measures the ordinary recipe's destructiveness
// against a real refused merge and requires this string not to be it.
const CommitLockMergeRefusalAdvice = "This is a MERGE commit, and the usual remedy is UNSAFE here: " +
	"`git restore --staged` on a path the other side brought in has nothing in HEAD to reset to, " +
	"so it records the merge as DELETING that file. That deletion does not show up in the merge's " +
	"own diff, and it lands on the base branch when the PR merges. Do NOT do it, and do NOT force " +
	"a takeover. Get the lock released instead: wait for the holder to finish, or ask it to pause, " +
	"then re-run this commit with the merge still in progress. `git merge --abort` is the fallback " +
	"if you cannot wait, but it discards your conflict resolutions along with the merge."

func displayOrUnknown(s string) string {
	if s == "" {
		return "an unnamed actor"
	}
	return s
}

func slugOrUnknown(s string) string {
	if s == "" {
		return "an unnamed work item"
	}
	return s
}
