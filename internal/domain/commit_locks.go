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

	// Paths are the repo-relative paths the pending commit contains, as
	// produced by coding.GitStagedPaths.
	Paths []string `json:"paths"`
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
		return nil, commitLockConflictErr(conflicts)
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
				}})
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
func commitLockConflictErr(conflicts []commitLockConflict) *AihubError {
	paths := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		paths = append(paths, c.Path)
	}
	first := conflicts[0]
	return NewErrDetails(ErrConflictLockTaken,
		fmt.Sprintf("this commit changes %d file(s) locked by another attempt: %v — "+
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
			"advice": "Those files belong to another live attempt. Wait for it to finish, " +
				"or drop them from this commit with pf_commit(paths=[...]). Do NOT force a takeover " +
				"to get past this: the other attempt is holding the lock because it is editing the file.",
		})
}

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
