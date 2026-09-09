package domain

// aihub#492: the retry every DB-gated test in this package owes a class-40
// rollback.
//
// # Why the tests need one at all
//
// FnClaimWorkItem, FnForceTakeover and FnAcquireLocks run at
// pgx.Serializable, and their cross-work-item lock probe (foreignLockHolderSQL)
// reads resource_locks JOIN run_attempts JOIN work_items with NO project
// predicate — it cannot have one, because a lock key is global. On a test
// database those three relations hold a few dozen rows, so Postgres seq-scans
// them and SSI takes a RELATION-level SIReadLock rather than a tuple-level one.
// Every concurrent committed write to any of the three then builds a
// read/write dependency with the claim, and one of the pair is aborted with
// SQLSTATE 40001.
//
// That concurrency is not hypothetical and it is not a bug in the tests that
// collide: `go test ./...` runs one test binary per package in parallel, and
// three packages here are DB-gated (internal/domain, internal/server,
// internal/mcp). Within a package Go runs tests sequentially, so the contending
// transaction is always ANOTHER PACKAGE'S — which is exactly why CI has never
// seen this. CI never runs the full suite with a database; each DB step is
// scoped with its own `-run` regex, so two DB-touching binaries are never live
// at once. The configuration only occurs on a developer's or an agent's
// machine, which is the whole cost of the defect.
//
// # Why retrying is the right answer rather than a mask
//
// 40001 is the one error class that MEANS "run this again". The server already
// says so in its own contract: pgx_err.go maps it to
// ErrConflictSerializationFailure with details {retryable:true}, errors.go
// documents it as "the server is healthy and the request was valid, the
// transaction just lost a race", and the pf_* tool descriptions tell clients
// to retry on it. A test that treats it as fatal is the only consumer in the
// system not following that contract.
//
// Retrying cannot weaken an assertion, either. SSI gives the retry a fresh
// serializable execution, so a claim that should have been refused with
// CONFLICT_LOCK_TAKEN still is, deterministically, and a claim that should
// have succeeded still does. The only outcome this removes is "the database
// asked us to retry and we reported a failure instead".
//
// # What it deliberately does NOT do
//
// It retries on the CODE, not on the message, and only on
// ErrConflictSerializationFailure. In particular it does not retry
// ErrInternalError: a 500 is the shape a poisoned transaction takes when a
// class-40 error is swallowed mid-transaction (see the aihub#492 fix at
// FnClaimWorkItem's wi_step_state upsert), and looping on that would hide the
// very defect this work item found rather than surface it.

import (
	"testing"
	"time"
)

// serializationRetryAttempts bounds the loop. A 40001 clears as soon as the
// contending transaction commits, so a handful of attempts is plenty; the
// bound exists so that a test which injects a PERMANENT 40001 — the aihub#410
// probe-failure injection in lock_probe_error_db_test.go does exactly that —
// terminates and still observes the error it was asserting on.
const serializationRetryAttempts = 8

// retryOnSerializationConflict re-runs call while it loses an SSI race.
//
// what names the operation for the retry log line, so a suite that quietly
// starts retrying every time stays visible in `go test -v` output rather than
// just getting slower.
func retryOnSerializationConflict[T any](t *testing.T, what string, call func() (T, *AihubError)) (T, *AihubError) {
	t.Helper()
	for attempt := 1; ; attempt++ {
		v, aerr := call()
		if aerr == nil || aerr.Code != ErrConflictSerializationFailure || attempt == serializationRetryAttempts {
			return v, aerr
		}
		t.Logf("%s: attempt %d lost an SSI race (%s), retrying: %s", what, attempt, aerr.Code, aerr.Message)
		// Linear back-off. The contender is another test binary's transaction,
		// which is short, so this only has to outlast a commit.
		time.Sleep(time.Duration(attempt) * 2 * time.Millisecond)
	}
}
