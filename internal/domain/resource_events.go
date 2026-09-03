package domain

// Timeline events for resource-lock mutations and declared_resources changes
// (aihub#343).
//
// # What was missing
//
// resource_locks rows were created and destroyed with no audit trail at all, so
// no claim a step summary made about locks or declarations could be checked
// afterwards. Two recorded instances, both cited by aihub#343:
//
//  1. A step summary said "declared_resources reduced to CLAUDE.md(read) and the
//     init.go write lock released". A review called that fabricated, citing
//     `resources_version: 0` in the update call; the producer answered that the
//     reviewer had misread a compare-and-set GUARD as an entry COUNT. Neither
//     side could settle it, because nothing recorded either number.
//  2. A note asserted a write lock had been released, on the strength of
//     pf_acquire_locks returning `already_held: []`. A day later a claim for the
//     same path still returned 409 CONFLICT_LOCK_TAKEN from that very attempt.
//     Whether the release never happened, or happened and the row outlived it,
//     was undecidable.
//
// Instance 1 is why wi_resources_updated carries prior_resources_version and
// prior_entry_count as SEPARATE named fields: the confusion that produced it was
// exactly reading one as the other, and a payload that reported only "the
// version" would let the identical argument happen again.
//
// 🔴 And why its path sets are subtracted by releaseUndeclaredFileScopeLocks
// rather than recomputed here — see narrowingDiff. An independent review of the
// first version of this change found the recomputed record naming locks that
// were still held as "removed"; an audit that is checkable and wrong is worse
// than one that is absent, which is the whole premise of this file.
//
// # The authority rule
//
// 🔴 Every SQL statement in package domain that INSERTs into, DELETEs from or
// UPDATEs resource_locks lives in THIS FILE, and is executed only through the
// helpers below. That is not tidiness — it is the property the fix rests on.
//
// UPDATE is in that list deliberately: an `UPDATE resource_locks SET
// owner_attempt_id=…` transfers a lock with no INSERT and no DELETE, and the
// first version of this rule (and of its gate) said only "INSERT or DELETE",
// which would have let exactly that through.
//
// The alternative, instrumenting each call site, is the shape that has already
// failed four times in this repo (aihub#238, #261, #264, #342 each shipped a
// rule at some derivation sites and not others). There are four sites that turn
// a declaration into a lock, seven lock-mutating SQL constants and ELEVEN
// execution sites across six functions; a reviewer cannot see from any one of
// them whether the other ten are instrumented. Concentrating the statements
// makes "is every mutation audited" a question about ONE file, and
// TestLockEvents_NoLockMutatingSQLOutsideThisFile turns it into a test that a
// new call site fails rather than silently escapes.
//
// # Events derive from what CHANGED, never from what was attempted
//
// Every helper below reads its event payload out of the statement's own
// RETURNING clause. A DO NOTHING insert that took nothing emits nothing; a
// DELETE that matched no row emits nothing. This is deliberate and is the
// difference between an audit trail and a log of intentions: pf_acquire_locks
// already had a field built from the re-derived TARGET set rather than from the
// table, and it reported `already_held: []` for a lock the server was still
// enforcing (aihub#345). An event built from the caller's intent would
// reproduce that defect in a place nobody could cross-check.
//
// # A dropped event must never fail the operation
//
// See emitResourceEvents for how that is achieved, why the obvious way does not
// work, and the measurement that settles it.
//
// # 🔴 Events are trustworthy only from the moment this code was deployed
//
// There is no lock-history table, so nothing can be reconstructed: resource_locks
// carries acquired_at for rows that still EXIST and keeps no trace of a row that
// was deleted. Synthesising lock_acquired rows from the surviving rows would
// produce events indistinguishable from real ones while still saying nothing
// about any release — a worse outcome than no events, because the gap would stop
// being visible. So no history is backfilled, and the consequence has to be
// stated wherever a reader might draw a conclusion from an empty stream:
//
//	The ABSENCE of lock_acquired / lock_released / wi_resources_updated before
//	this code was DEPLOYED is NOT evidence that no lock or declaration changed.
//	It is only evidence that nothing was recording them.
//
// ⚠️ Deployed, not committed. aihub rollouts need an explicit human
// instruction and can trail the merge by days, so a hard-coded start date is
// wrong in the one direction that matters: during that gap there are still no
// events, and a reader holding the commit date would read the emptiness as "the
// recorder was running and saw nothing".
//
// That sentence is also on pf_read_events (internal/mcp/tools_events.go) and in
// docs/design/polyforge-v1-design.md §19.0, because the failure mode aihub#343
// exists to prevent is a reader treating an empty result as proof — and a reader
// who never opens this file is exactly the reader at risk.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ─── The public event-type vocabulary ────────────────────────────────────────

// 🔴 These strings are PUBLIC API. pf_read_events(types=[...]) filters on them
// with a plain `event_type IN (...)`, so they are part of the contract the tool
// advertises, not an internal detail.
//
// # Why three types and not one `resources_changed` with an `op` field
//
// `types` is the ONLY server-side filter on the event stream. Folding acquire,
// release and declaration changes into one type would force every caller that
// wants lock churn to fetch declaration churn as well and re-filter in the
// client — and /v1/events has no page-size ceiling but does have a default limit
// of 50, so the extra rows come straight out of the caller's budget. Separate
// types cost nothing and are the only shape the existing filter can express.
//
// # Why these particular names
//
// lock_acquired and lock_released are not new names. They have been in the
// documented vocabulary since the schema was written — 0006_events_memories.sql
// lists "locks: lock_acquired, lock_released" in the agent_events comment — and
// nothing ever emitted them. Inventing locks_acquired/locks_released alongside a
// documented pair that means the same thing would leave two spellings of one
// concept in a public vocabulary forever.
//
// wi_resources_updated has no such precedent, so it follows the convention of
// the events emitted from the same function: UpdateWorkItem already emits
// wi_goal_updated, wi_content_updated and wi_reclassified for the other three
// fields it can change.
//
// # Backward-compatibility impact on pf_read_events callers
//
// Written out rather than assumed, because a vocabulary change is a
// compatibility event (aihub#259 made the types filter work at all, and it is
// live):
//
//   - A caller passing an explicit `types` list is byte-unaffected. The filter
//     is a whitelist (`event_type IN (...)`), so a type nobody asked for cannot
//     appear in, or remove anything from, their result.
//   - A caller passing NO types sees new rows, ordered by created_at with
//     everything else. Combined with a `limit`, that means fewer of the older
//     event kinds inside one page. This is the one real regression surface, it
//     is shared by every event type ever added, and the fix for a caller that
//     wants the old view is to name the types it wants.
//   - lock_acquired / lock_released previously matched zero rows for everyone,
//     including a caller who had already written them into a `types` list
//     defensively. Such a caller now gets rows. There is none in this repo
//     (nothing outside the schema comment mentioned either string), but the
//     change is real and is why it is listed.
//   - No migration, so NO deploy ordering constraint and no one-way door. The
//     agent_events type allowlist (chk_evt_work_item_id) only restricts rows
//     with a NULL work_item_id, and every event here carries the work item that
//     held the lock. Rolling BACK to the previous binary leaves the new rows
//     readable — they are simply rows of a type that binary never writes.
const (
	// EventLockAcquired records one resource_locks row coming into existence, or
	// changing owner. One event ROW per lock, never one row carrying an array of
	// locks — see lockOpCtx for why, and for what OpID gives back.
	EventLockAcquired = "lock_acquired"
	// EventLockReleased records one resource_locks row ceasing to exist.
	EventLockReleased = "lock_released"
	// EventResourcesUpdated records a change to work_items.declared_resources.
	EventResourcesUpdated = "wi_resources_updated"
)

// ─── Causes ──────────────────────────────────────────────────────────────────

// A lock event's `cause` says which operation moved the lock. It is a payload
// field rather than part of the event type so that "did this key change hands"
// stays answerable with a single type filter, while "why" stays available to
// anyone who reads the payload.
const (
	lockCauseClaim               = "claim"
	lockCauseClaimTakeover       = "claim_takeover"
	lockCauseAttemptTerminal     = "attempt_terminal"
	lockCauseAttemptPaused       = "attempt_paused"
	lockCauseForceTakeover       = "force_takeover"
	lockCauseAcquireLocks        = "acquire_locks"
	lockCauseOrphanReclaim       = "orphan_reclaim"
	lockCauseDeclarationNarrowed = "declaration_narrowed"
	lockCauseOrphanSweep         = "orphan_sweep"
	// lockCauseOwnerReplaced is the release side of an upsert that rewrote an
	// existing row's owner. It has no call site of its own: see lockUpsertSQL.
	lockCauseOwnerReplaced = "owner_replaced"
)

// ─── Actor / operation context ───────────────────────────────────────────────

// lockEventActor is the caller identity stamped on a lock event.
//
// Every field may be empty, and an empty actor is not a defect: the orphan
// sweep in gc.go has no caller at all, and a lock released by
// FnCompleteAttempt is released on behalf of an attempt whose credential —
// not whose user — was verified. An event with no actor still identifies the
// work item and the attempt, which is what "should this lock be held" needs.
type lockEventActor struct {
	UserID   string
	Display  string
	APIKeyID string
}

// lockOpCtx is everything a lock mutation needs in order to be auditable.
//
// OpID groups the events of ONE operation. It exists because granularity was a
// real fork: one event per lock, or one per call carrying an array of locks.
// This code emits one event PER LOCK, because the acceptance criterion is
// per-key ("looking only at pf_read_events, decide whether THIS lock should be
// held"), and with an array the reader has to diff two events to answer it —
// arithmetic on the audit trail, in the one place arithmetic must not be
// needed. OpID gives back the only thing the array form had: the events of one
// claim or one narrowing share an op_id, so they can be regrouped without
// guessing from timestamps.
type lockOpCtx struct {
	Cause string
	Actor lockEventActor
	OpID  string
	// Extra adds cause-specific payload fields (e.g. the takeover reason).
	Extra map[string]any
}

// newLockOp starts one auditable lock operation.
func newLockOp(cause string, actor lockEventActor) lockOpCtx {
	return lockOpCtx{Cause: cause, Actor: actor, OpID: NewID("lop")}
}

// withExtra returns a copy carrying extra payload fields. Copy rather than
// mutate: one operation's context is passed to several helpers and a mutating
// setter would let the last caller's fields leak onto the earlier events.
func (op lockOpCtx) withExtra(kv map[string]any) lockOpCtx {
	merged := make(map[string]any, len(op.Extra)+len(kv))
	for k, v := range op.Extra {
		merged[k] = v
	}
	for k, v := range kv {
		merged[k] = v
	}
	op.Extra = merged
	return op
}

// lockRow is one resource_locks row as the mutating statement reported it,
// resolved to the work item that held it.
//
// WorkItemID is the OWNER's work item, not the caller's, and the difference is
// load-bearing at two sites: FnAcquireLocks reclaims an orphan row that may
// belong to a different work item, and the gc.go sweep is not called on behalf
// of any work item at all. Stamping the caller's work item on those events
// would file the release under a timeline where the lock never existed.
type lockRow struct {
	ResourceType string
	ResourceKey  string
	AttemptID    string
	ClaimEpoch   int64
	WorkItemID   string
	Project      string
}

// ─── SQL: every statement that mutates resource_locks ────────────────────────

// lockRowReturning is the column list every lock-mutating statement returns.
//
// Qualified with the `rl.` alias on purpose: releaseUndeclaredLocksSQL joins
// run_attempts, which ALSO has a claim_epoch column, so an unqualified
// `RETURNING claim_epoch` is ambiguous and Postgres rejects the statement. Every
// DELETE below therefore aliases its target `rl`, so one RETURNING clause fits
// all of them.
const lockRowReturning = `rl.resource_type, rl.resource_key, rl.owner_attempt_id, rl.claim_epoch`

// lockDeleteReporting wraps a DELETE over resource_locks so that the rows it
// ACTUALLY removed come back, each resolved to the work item that held it.
//
// 🔴 The wrapper is a CTE, so the DELETE's own PREDICATE is untouched — the
// only edit the statements needed was an `rl` alias and its column
// qualifications, which cannot change which rows match.
// TestLockEvents_ReportingWrapperDoesNotAlterTheDelete asserts the containment
// is verbatim.
//
// The alternative — rewriting each DELETE as
// `DELETE ... USING run_attempts ra WHERE rl.owner_attempt_id = ra.id AND ...`
// to reach work_item_id — reads the same and deletes LESS: a row whose join
// partner is missing silently stops being deleted. The FK makes that
// unreachable today, which is exactly the kind of premise that stops being true
// later and takes a lock leak with it.
//
// The joins here are LEFT joins for the same reason: a lock that was really
// deleted must appear in the report even if its work item cannot be resolved,
// or the release would go unrecorded precisely when the data is strange.
func lockDeleteReporting(deleteStmt string) string {
	return `WITH del AS (
		` + deleteStmt + `
		RETURNING ` + lockRowReturning + `
	)
	SELECT d.resource_type, d.resource_key, d.owner_attempt_id, d.claim_epoch,
	       COALESCE(ra.work_item_id, ''), COALESCE(wi.project, '')
	FROM del d
	LEFT JOIN run_attempts ra ON ra.id = d.owner_attempt_id
	LEFT JOIN work_items wi ON wi.id = ra.work_item_id`
}

// lockDeleteByAttemptSQL releases every lock an attempt holds. Used by the
// claim-time takeover, by FnCompleteAttempt on a terminal status, and by
// FnForceTakeover.
const lockDeleteByAttemptSQL = `DELETE FROM resource_locks rl WHERE rl.owner_attempt_id = $1`

// lockDeleteByKeySQL releases one lock by key regardless of who holds it. Its
// only caller is FnAcquireLocks reclaiming a row whose owner is not a live
// attempt.
const lockDeleteByKeySQL = `DELETE FROM resource_locks rl WHERE rl.resource_type = $1 AND rl.resource_key = $2`

// lockUpsertSQL takes a lock for an attempt, overwriting any existing row.
//
// The `prior` CTE is not decoration. ON CONFLICT DO UPDATE can rewrite a row's
// owner_attempt_id, and if only the acquisition were recorded, a reader
// following the previous owner would see lock_acquired with no matching
// lock_released and conclude that owner still holds it — breaking the one
// question these events exist to answer. `prior` reads the pre-statement
// snapshot inside the same statement, so the displaced owner is known without a
// second round trip. When it is non-empty and names a different attempt, the
// caller emits a lock_released for that attempt as well (see acquireLockUpsert).
//
// ⚠️ One case `prior` does NOT cover, stated rather than left to be discovered:
// under READ COMMITTED, ON CONFLICT DO UPDATE will overwrite a row committed
// AFTER the statement snapshot, which `prior` cannot see — so that displacement
// would emit no owner_replaced release. Every caller here runs SERIALIZABLE
// (FnClaimWorkItem, FnForceTakeover, FnAcquireLocks all use
// pgx.TxOptions{IsoLevel: pgx.Serializable}), where such a conflict aborts
// instead, so the gap is unreachable today. It becomes reachable the moment one
// of them is moved to a weaker isolation level.
//
// Reachability of the displacement, stated rather than dismissed: at claim the
// conflict check rejects any key held by a running or paused attempt before this
// runs, and FnForceTakeover deletes the prior attempt's rows first, so what is
// overwritten is an un-swept orphan. That is still a real lock row belonging to
// a real work item, and the sweep it is racing may not have run for up to a
// minute.
const lockUpsertSQL = `
	WITH prior AS (
		SELECT owner_attempt_id, claim_epoch
		FROM resource_locks
		WHERE resource_type = $1 AND resource_key = $2
	), ins AS (
		INSERT INTO resource_locks (resource_type, resource_key, owner_attempt_id, claim_epoch)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (resource_type, resource_key) DO UPDATE
		  SET owner_attempt_id = $3, claim_epoch = $4, acquired_at = clock_timestamp()
		RETURNING resource_type, resource_key
	)
	SELECT i.resource_type, i.resource_key,
	       COALESCE(p.owner_attempt_id, ''), COALESCE(p.claim_epoch, 0)
	FROM ins i LEFT JOIN prior p ON TRUE`

// acquireLocksInsertSQL takes a lock only if the key is free — it never steals.
// A non-zero row count means we took a free key; no row means one already
// exists (held by us, by another live attempt, or an un-GC'd orphan) and the
// caller must re-check the owner to decide no-op / conflict / reclaim.
//
// RETURNING is what makes the event honest: ON CONFLICT DO NOTHING returns no
// row when the key was already taken, so "a row came back" and "a lock was
// acquired" are the same fact, and there is no way to emit an acquisition that
// did not happen. The previous shape of this statement had the caller branch on
// RowsAffected() instead, which is the same information one inference further
// from the database.
const acquireLocksInsertSQL = `
	INSERT INTO resource_locks (resource_type, resource_key, owner_attempt_id, claim_epoch)
	VALUES ($1, $2, $3, $4)
	ON CONFLICT (resource_type, resource_key) DO NOTHING
	RETURNING resource_type, resource_key, owner_attempt_id, claim_epoch`

// acquireLocksReleasePausedSQL releases only file_scope locks when an attempt
// transitions to paused (git_branch / deploy_env locks are kept for resume).
const acquireLocksReleasePausedSQL = `DELETE FROM resource_locks rl WHERE rl.owner_attempt_id=$1 AND rl.resource_type='file_scope'`

// releaseUndeclaredLocksSQL drops the file_scope locks a narrowing orphaned.
//
// Scoped by work_item_id through run_attempts rather than by a single attempt id
// on purpose: the reported instance (ieops#798) was on claim epoch 7, and each
// re-claim mints a new attempt row, so residue from an earlier epoch is owned by
// an attempt that is no longer current. Joining on work_item_id reaches that
// residue while still making it impossible to touch a lock belonging to any
// OTHER work item, whatever its key looks like.
// $2 is the set of exact keys and $3 the set of LIKE patterns that together
// cover EVERY key form a removed path can be held under (aihub#261): the
// unqualified "<project>:<path>" and any repo-qualified "<project>:<repo>:<path>".
//
// 🔴 Matching only the currently-derived key is not enough, and that is measured,
// not theoretical. A lock row is written once and never rewritten, so a work item
// that took "<project>:<repo>:<path>" and later dropped its {"type":"repo"} entry
// derives "<project>:<path>" from then on. Deleting only that misses the row the
// attempt actually holds, and the lock survives a declaration that no longer
// mentions the path at all — silent over-holding, the mirror of the silent
// under-holding releaseUndeclaredFileScopeLocks exists to prevent.
//
// Widening the match cannot reach another work item's lock: ra.work_item_id = $1
// still scopes every row, so the patterns only ever range over key forms of paths
// THIS work item declared and has now dropped.
const releaseUndeclaredLocksSQL = `
	DELETE FROM resource_locks rl
	USING run_attempts ra
	WHERE rl.owner_attempt_id = ra.id
	  AND ra.work_item_id = $1
	  AND rl.resource_type = 'file_scope'
	  AND (rl.resource_key = ANY($2::text[]) OR rl.resource_key LIKE ANY($3::text[]))`

// orphanLockSweepSQL deletes resource_locks whose owner attempt is no longer
// holding them per the lock-retention contract. A lock is retained while its
// owner attempt is 'running' OR 'paused': FnCompleteAttempt keeps the locks on
// paused so resume can reclaim them (N4 / C5-3 design invariant), and the claim
// conflict-check (run_attempts.go) treats the retention set as IN ('running',
// 'paused'). The sweep predicate must match that set, otherwise the GC tick
// deletes a paused attempt's locks within 60s — breaking the resume invariant
// and allowing a concurrent claim to steal the resource (aihub#145).
const orphanLockSweepSQL = `
	DELETE FROM resource_locks rl
	WHERE NOT EXISTS (
		SELECT 1 FROM run_attempts ra
		WHERE ra.id = rl.owner_attempt_id AND ra.status IN ('running', 'paused')
	)`

// ─── Mutating helpers ────────────────────────────────────────────────────────

// releaseLocks runs a DELETE over resource_locks and emits one
// EventLockReleased per row it actually removed.
//
// stmt must be one of this file's lockDelete*SQL / release*SQL constants; args
// are that statement's bind parameters. The returned rows are what the database
// deleted, in the order it reported them.
func releaseLocks(ctx context.Context, tx pgx.Tx, stmt string, op lockOpCtx, args ...any) ([]lockRow, error) {
	rows, err := tx.Query(ctx, lockDeleteReporting(stmt), args...)
	if err != nil {
		return nil, err
	}
	released := make([]lockRow, 0, 4)
	for rows.Next() {
		var r lockRow
		if scanErr := rows.Scan(&r.ResourceType, &r.ResourceKey, &r.AttemptID,
			&r.ClaimEpoch, &r.WorkItemID, &r.Project); scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		released = append(released, r)
	}
	rows.Close()
	// A streamed result set reports a mid-stream failure only here. Without this
	// the loop just looks empty — and "the DELETE removed nothing" is the exact
	// wrong conclusion to draw from a broken connection (aihub#334 fixed the
	// same omission in FnAcquireLocks).
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 🔴 Emitted AFTER rows.Close(), never inside the loop above. A savepoint is
	// a statement, and running one while a lazily-streamed result set is still
	// open on the same connection is not something to find out about in
	// production.
	//
	// Batched into ONE savepoint and one multi-row INSERT, unlike the acquisition
	// side. A per-event savepoint is three round trips (SAVEPOINT / INSERT /
	// RELEASE), which is worth measuring rather than guessing at.
	//
	// Measured on a 20-lock work item against a Postgres on loopback — the
	// cheapest possible link, so a real deployment can only be worse. ⚠️ Compare
	// only WITHIN a row: this host runs several agents concurrently, and the
	// absolute figures moved by 2.5x between the two runs while the deltas held.
	// The instrument is the difference against the same build with emission
	// disabled, in the same run:
	//
	//	                       emission off   emission on   delta / 20 events
	//	per-event savepoints:
	//	  claim (20 acquires)      35.4ms        55.5ms       +20.1ms  (~1.0ms ea)
	//	  wrap  (20 releases)      10.0ms        30.5ms       +20.5ms  (~1.0ms ea)
	//	release batched (this):
	//	  claim (20 acquires)      14.1ms        26.4ms       +12.3ms  (~0.6ms ea)
	//	  wrap  (20 releases)       4.8ms         6.6ms        +1.7ms  (~0.09ms ea)
	//
	// so batching the release side is a 12x reduction on that half, and the
	// acquisition side is unchanged by design.
	//
	// The asymmetry is deliberate rather than half-finished. This function
	// already holds the whole set, so batching costs nothing; the acquisition
	// helpers are called once per lock from a caller's loop, and batching them
	// would mean an event sink threaded through four helpers and six call sites
	// whose flush somebody can forget — a silent-loss mode, in the one component
	// whose job is not losing records. What is left is sub-millisecond per
	// declared path on an operation that runs once per attempt.
	//
	// The sweep in gc.go is the case that made batching non-optional rather than
	// nice: it deletes an unbounded number of rows in one statement, so three
	// round trips per row is a GC tick whose cost grows with the backlog.
	emitLockEvents(ctx, tx, EventLockReleased, released, op)
	return released, nil
}

// acquireLockUpsert takes a lock for an attempt, overwriting any existing row,
// and emits EventLockAcquired — plus EventLockReleased for a displaced owner.
func acquireLockUpsert(ctx context.Context, tx pgx.Tx, lockType, lockKey, attemptID string,
	epoch int64, project, workItemID string, op lockOpCtx) (lockRow, error) {

	var got lockRow
	var priorAttempt string
	var priorEpoch int64
	err := tx.QueryRow(ctx, lockUpsertSQL, lockType, lockKey, attemptID, epoch).
		Scan(&got.ResourceType, &got.ResourceKey, &priorAttempt, &priorEpoch)
	if err != nil {
		return lockRow{}, err
	}
	got.AttemptID = attemptID
	got.ClaimEpoch = epoch
	got.WorkItemID = workItemID
	got.Project = project

	// The displaced owner first, so the timeline reads release-then-acquire for
	// one key rather than two overlapping acquisitions.
	if priorAttempt != "" && priorAttempt != attemptID {
		prior := lockRow{
			ResourceType: lockType, ResourceKey: lockKey,
			AttemptID: priorAttempt, ClaimEpoch: priorEpoch,
		}
		// Resolve the DISPLACED attempt's own work item: an orphan row may belong
		// to another work item entirely, and filing its release under the caller's
		// timeline would hide it from the only reader who cares.
		if lookupErr := tx.QueryRow(ctx, `
			SELECT COALESCE(ra.work_item_id, ''), COALESCE(wi.project, '')
			FROM run_attempts ra LEFT JOIN work_items wi ON wi.id = ra.work_item_id
			WHERE ra.id = $1`, priorAttempt,
		).Scan(&prior.WorkItemID, &prior.Project); lookupErr != nil {
			// Report the release anyway if the owner cannot be resolved — see
			// emitLockEvent for what happens to an event with no work item.
			prior.WorkItemID, prior.Project = "", ""
		}
		// cause=owner_replaced, not the triggering operation's cause: a reader
		// following the DISPLACED attempt needs "somebody took this from me" to be
		// unambiguous, and `replaced_cause` still records which operation did it.
		displaced := op.withExtra(map[string]any{
			"replaced_by_attempt_id": attemptID,
			"replaced_cause":         op.Cause,
		})
		displaced.Cause = lockCauseOwnerReplaced
		emitLockEvent(ctx, tx, EventLockReleased, prior, displaced)
		// The acquisition records that it displaced somebody, so the two halves
		// can be paired without matching on timestamps.
		op = op.withExtra(map[string]any{"replaced_prior_owner": priorAttempt})
	}
	emitLockEvent(ctx, tx, EventLockAcquired, got, op)
	return got, nil
}

// acquireLockIfFree takes a lock only when the key is free. It reports whether
// the lock was taken; taken=false means a row already existed and the caller
// must decide whose it is.
func acquireLockIfFree(ctx context.Context, tx pgx.Tx, lockType, lockKey, attemptID string,
	epoch int64, project, workItemID string, op lockOpCtx) (taken bool, err error) {

	var got lockRow
	err = tx.QueryRow(ctx, acquireLocksInsertSQL, lockType, lockKey, attemptID, epoch).
		Scan(&got.ResourceType, &got.ResourceKey, &got.AttemptID, &got.ClaimEpoch)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// DO NOTHING: the key was already held. Nothing changed, so nothing is
			// emitted — the whole point of building events from RETURNING.
			return false, nil
		}
		return false, err
	}
	got.WorkItemID = workItemID
	got.Project = project
	emitLockEvent(ctx, tx, EventLockAcquired, got, op)
	return true, nil
}

// ─── Emission ────────────────────────────────────────────────────────────────

// lockEventPayload builds the payload for a lock event.
func lockEventPayload(row lockRow, op lockOpCtx) map[string]any {
	p := map[string]any{
		"resource_type": row.ResourceType,
		"resource_key":  row.ResourceKey,
		"attempt_id":    row.AttemptID,
		"claim_epoch":   row.ClaimEpoch,
		"cause":         op.Cause,
		"op_id":         op.OpID,
	}
	if op.Actor.Display != "" {
		p["actor_display"] = op.Actor.Display
	}
	if op.Actor.UserID != "" {
		p["actor_user_id"] = op.Actor.UserID
	}
	for k, v := range op.Extra {
		p[k] = v
	}
	return p
}

// lockEventFor builds the agent_events row for one lock mutation.
func lockEventFor(eventType string, row lockRow, op lockOpCtx) resourceEvent {
	payload, err := json.Marshal(lockEventPayload(row, op))
	if err != nil {
		// Cannot happen for a map of strings/ints, and must not take the caller's
		// transaction down if it somehow does.
		payload = []byte(`{}`)
	}
	return resourceEvent{
		EventType:  eventType,
		WorkItemID: row.WorkItemID,
		AttemptID:  row.AttemptID,
		Project:    row.Project,
		Actor:      op.Actor,
		Payload:    payload,
	}
}

// emitLockEvent writes one lock_acquired / lock_released row.
func emitLockEvent(ctx context.Context, tx pgx.Tx, eventType string, row lockRow, op lockOpCtx) {
	emitResourceEvents(ctx, tx, []resourceEvent{lockEventFor(eventType, row, op)})
}

// emitLockEvents writes one row per lock in a single statement.
func emitLockEvents(ctx context.Context, tx pgx.Tx, eventType string, rows []lockRow, op lockOpCtx) {
	if len(rows) == 0 {
		return
	}
	evs := make([]resourceEvent, 0, len(rows))
	for _, r := range rows {
		evs = append(evs, lockEventFor(eventType, r, op))
	}
	emitResourceEvents(ctx, tx, evs)
}

// resourceEvent is one agent_events row this file is responsible for.
type resourceEvent struct {
	EventType  string
	WorkItemID string
	AttemptID  string
	Project    string
	Actor      lockEventActor
	Payload    []byte
}

// resourceEventInsertPrefix is the INSERT with its VALUES list left open, so
// several events become one statement. The tuples are appended by
// emitResourceEventBatch; every value is still a bind parameter.
const resourceEventInsertPrefix = `
	INSERT INTO agent_events (
		id, work_item_id, run_attempt_id, actor_user_id, actor_display,
		api_key_id, event_type, payload, project
	) VALUES `

// emitResourceEvent writes one event. See emitResourceEvents, which it calls,
// for the failure contract.
func emitResourceEvent(ctx context.Context, tx pgx.Tx, ev resourceEvent) {
	emitResourceEvents(ctx, tx, []resourceEvent{ev})
}

// emitResourceEvents writes one operation's events and CANNOT fail the caller's
// operation.
//
// 🔴 What happens when the insert fails, and why this is the right choice.
//
// The insert runs inside a NESTED transaction, which pgx implements as a
// SAVEPOINT. On failure the savepoint is rolled back, the caller's transaction
// is left usable and its lock mutation still commits, and the failure is
// reported to stderr and nowhere else. Observability that can break a claim is
// worse than no observability: a lost lock_released event costs a reviewer an
// unanswerable question, whereas a failed claim costs an agent its session and
// leaves the lock set in whatever state the rollback chose.
//
// ⚠️ The obvious way to "ignore" the error does NOT have that property, and the
// rest of this package is full of it:
//
//	_, _ = tx.Exec(ctx, `INSERT INTO agent_events ...`)   // NOT safe
//
// A failed statement puts a Postgres transaction into the aborted state. Every
// later statement then fails with 25P02 and Commit returns an error, so
// discarding the Go-level error converts a dropped EVENT into a failed
// OPERATION — the exact outcome the discard looks like it is preventing.
// Measured, both arms, in
// TestLockEventsDB_FailedEventInsertDoesNotAbortTheOperation: the naive form
// really does make Commit fail, and this one really does survive.
//
// This is not a new idea in this codebase, and saying so matters more than
// claiming novelty: emitWIUnblockedEvent (dependencies.go) already documents the
// same reasoning for the same reason (aihub#242), and routes_step.go uses the
// idiom too. The difference here is `tx.Begin()` rather than hand-written
// `SAVEPOINT <name>` / `ROLLBACK TO SAVEPOINT <name>` statements — pgx
// implements the nested transaction as a savepoint with a generated name, so
// nesting cannot collide and no rollback can name the wrong one.
//
// ⚠️ That older comment's named trigger — a missing monthly partition on an
// agent_events table PARTITION BY RANGE(created_at) — has since been mitigated:
// migration 0031 added a DEFAULT partition, so such a row is caught rather than
// rejected. The savepoint is still required, because the trigger was never the
// point: agent_events has FKs to work_items, run_attempts and users, a payload
// size limit, and a CHECK constraint, and any of them rejecting one tuple would
// otherwise take the caller's whole transaction down.
//
// The one thing that is NOT tolerated is a silent drop with no trace: a
// swallowed error that nobody can see would make the event stream's own
// reliability unfalsifiable, which is the class of defect aihub#343 is about.
//
// ONE savepoint and ONE multi-row INSERT for the whole batch, so the cost is per
// operation rather than per event (see releaseLocks for the measurement).
// All-or-nothing within a batch is the intended trade: one operation's events
// are worth less individually than together, because half the releases of a
// takeover is a stream that reads as a PARTIAL release — a wrong answer, where
// having none for that operation is merely a missing one.
func emitResourceEvents(ctx context.Context, tx pgx.Tx, evs []resourceEvent) {
	for len(evs) > 0 {
		n := len(evs)
		if n > resourceEventBatchMax {
			n = resourceEventBatchMax
		}
		emitResourceEventBatch(ctx, tx, evs[:n])
		evs = evs[n:]
	}
}

// resourceEventBatchMax caps one INSERT's row count.
//
// 🔴 Not a tuning knob — a correctness bound. The extended query protocol allows
// at most 65535 bind parameters per statement and pgx enforces it client-side,
// so at 9 parameters per row a single statement dies at 7282 rows. Measured
// against a live Postgres with a probe of the exact statement shape:
//
//	7281 rows (65529 params) -> ok
//	7282 rows (65538 params) -> "extended protocol limited to 65535 parameters"
//
// The savepoint means the operation still commits, so the failure mode is that
// the whole tick's events vanish with one line on stderr — the silent-loss mode
// this file argues against everywhere else, reachable through the one producer
// that is genuinely unbounded: gc.go's orphan sweep deletes every orphaned row
// in one statement with no LIMIT, so a mass attempt failure (or the first tick
// after enabling the sweep against an accumulated backlog) is all it takes.
//
// 2000 rather than 7281: the ceiling is where it breaks, not where it should
// run, and one chunk per 2000 events costs three round trips against the ~2000
// it records. Each chunk gets its own savepoint, so a chunk that fails loses
// only itself.
const resourceEventBatchMax = 2000

// emitResourceEventBatch writes at most resourceEventBatchMax events in one
// savepoint and one statement.
func emitResourceEventBatch(ctx context.Context, tx pgx.Tx, evs []resourceEvent) {
	if len(evs) == 0 {
		return
	}
	var stmt strings.Builder
	stmt.WriteString(resourceEventInsertPrefix)
	args := make([]any, 0, len(evs)*9)
	n := 0
	for _, ev := range evs {
		if ev.WorkItemID == "" {
			// agent_events.chk_evt_work_item_id rejects a NULL work_item_id for any
			// event type outside a fixed allowlist, and these types are deliberately
			// not on it (adding them would have required a migration). An
			// unresolvable owner is therefore unrecordable; say so rather than issue
			// a statement that is certain to fail — and certain to take its whole
			// batch with it.
			fmt.Fprintf(os.Stderr,
				"resource event %s dropped: no work_item could be resolved for attempt %q (%s)\n",
				ev.EventType, ev.AttemptID, ev.Payload)
			continue
		}
		if n > 0 {
			stmt.WriteString(", ")
		}
		base := n * 9
		fmt.Fprintf(&stmt, "($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9)
		args = append(args,
			NewID("evt"), ev.WorkItemID, nilIfEmpty(ev.AttemptID),
			nilIfEmpty(ev.Actor.UserID), ev.Actor.Display, ev.Actor.APIKeyID,
			ev.EventType, ev.Payload, nilIfEmpty(ev.Project))
		n++
	}
	if n == 0 {
		return
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%d resource event(s) dropped (savepoint): %v\n", n, err)
		return
	}
	if _, execErr := sp.Exec(ctx, stmt.String(), args...); execErr != nil {
		fmt.Fprintf(os.Stderr, "%d resource event(s) dropped: %v\n", n, execErr)
		_ = sp.Rollback(ctx)
		return
	}
	if commitErr := sp.Commit(ctx); commitErr != nil {
		fmt.Fprintf(os.Stderr, "%d resource event(s) dropped (release savepoint): %v\n",
			n, commitErr)
	}
}

// ─── declared_resources changes ──────────────────────────────────────────────

// resourceKeyListCap bounds the key arrays in a wi_resources_updated payload.
//
// agent_events.payload has a 64KB limit and a work item may declare hundreds of
// paths. Truncating with an explicit flag is the only option that cannot lie: an
// untruncated-looking short list would be read as the complete declaration.
const resourceKeyListCap = 60

// cappedKeyList returns a sorted, capped copy of keys and whether it was cut.
func cappedKeyList(keys []string) (out []string, truncated bool) {
	out = make([]string, 0, len(keys))
	out = append(out, keys...)
	sort.Strings(out)
	if len(out) > resourceKeyListCap {
		return out[:resourceKeyListCap], true
	}
	return out, false
}

// emitResourcesUpdated records a change to work_items.declared_resources.
//
// 🔴 priorVersion / newVersion and priorEntries / newEntries are four separate
// named fields, and that IS the fix for aihub#343's first recorded instance. The
// argument there was one side reading `resources_version: 0` as "the
// declaration now has zero entries" and the other as "the compare-and-set guard
// expected version 0". Both readings are reasonable about a single number, so
// the payload never carries a single number.
//
// The locked-path sets are recorded on both sides as well, because "which locks
// did this declaration justify" is the question a lock_released event has to be
// checked against, and it is not readable off the entry counts: an intent=read
// entry counts but justifies no lock.
//
// 🔴 PATHS, not derived keys, and the diff comes from `rel` — the subtraction
// releaseUndeclaredFileScopeLocks actually performed — rather than being
// recomputed here. The first version of this function recomputed it from the
// derived KEYS, which is the very computation aihub#261 removed from the release
// path: drop a {"type":"repo"} entry with the paths untouched and every key
// changes while every path stays. Measured on the repo's own aihub#261 fixture,
// the key-based record named the two keys that were STILL HELD as removed and
// two keys held by NOBODY as added, with zero lock_released events to match —
// a reader running this work item's own decision procedure would have deleted
// live locks on the strength of it. Recomputing an audit is how it comes to
// disagree with what it audits.
//
// released_file_scope_keys is the one key-shaped field, and it is not a
// subtraction at all: it is what the DELETE's RETURNING clause reported, so it
// names the rows that really went away.
func emitResourcesUpdated(ctx context.Context, tx pgx.Tx, wiID, project string,
	priorVersion, newVersion, priorEntries, newEntries int,
	rel narrowingDiff, actor lockEventActor, opID string) {

	body := map[string]any{
		"prior_resources_version": priorVersion,
		"new_resources_version":   newVersion,
		"prior_entry_count":       priorEntries,
		"new_entry_count":         newEntries,
		"op_id":                   opID,
	}
	if !rel.Readable {
		// A stored declaration could not be decoded, so no path set means
		// anything. Say that, rather than emitting empty lists a reader would
		// take for "this declaration justified no locks".
		body["locked_paths_unreadable"] = true
	} else {
		priorList, priorCut := cappedKeyList(rel.PriorPaths)
		newList, newCut := cappedKeyList(rel.NextPaths)
		addedList, addedCut := cappedKeyList(rel.AddedPaths)
		removedList, removedCut := cappedKeyList(rel.RemovedPaths)
		releasedList, releasedCut := cappedKeyList(rel.ReleasedKeys)
		body["prior_locked_paths"] = priorList
		body["new_locked_paths"] = newList
		body["added_locked_paths"] = addedList
		body["removed_locked_paths"] = removedList
		body["released_file_scope_keys"] = releasedList
		if priorCut || newCut || addedCut || removedCut || releasedCut {
			body["keys_truncated"] = true
			body["keys_truncated_at"] = resourceKeyListCap
		}
	}
	if actor.UserID != "" {
		body["changed_by"] = actor.UserID
	}
	payload, err := json.Marshal(body)
	if err != nil {
		payload = []byte(`{}`)
	}
	emitResourceEvent(ctx, tx, resourceEvent{
		EventType:  EventResourcesUpdated,
		WorkItemID: wiID,
		Project:    project,
		Actor:      actor,
		Payload:    payload,
	})
}

// declaredEntryCount counts the entries in a stored declared_resources payload,
// or -1 when it cannot be read as an array of objects.
//
// -1 rather than 0, because "the declaration listed nothing" and "the
// declaration could not be parsed" must not look alike in an audit record —
// roughly 14% of stored payloads in this deployment are malformed
// (ValidateDeclaredResources), so the unreadable case is the common one, not the
// theoretical one.
func declaredEntryCount(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return -1
	}
	return len(items)
}
