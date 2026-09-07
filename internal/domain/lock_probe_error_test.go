package domain

// aihub#410 (residue of aihub#393): probeForeignLockHolders must not answer "no
// conflict" for an error that was never about conflicts.
//
// THE DEFECT. The loop's entire error handling was `if err == nil { conflict }`.
// ErrNoRows is the one error that means "nobody else holds this key"; every
// other one — a serialization failure, an aborted transaction, a dropped
// connection — took the same silent path and the claim walked on.
//
// WHY IT IS A CLASSIFICATION BUG AND NOT A SAFETY BUG, which is worth being
// precise about because the two have different fixes. Safety survives a blind
// probe: lockUpsertSQL's conditional ON CONFLICT DO UPDATE refuses to displace a
// live foreign holder on its own (resource_events.go), and that backstop holds.
// What does not survive is the answer the caller gets. Both call sites run
// inside a transaction and FnClaimWorkItem's is SERIALIZABLE, so 40001 is live
// on this path rather than latent; a 40001 aborts the transaction, every later
// statement in it then fails with 25P02, and 25P02 is not class 40 — so the
// caller is told 500 INTERNAL_ERROR about the second victim while the SQLSTATE
// that meant "retry and this works" was thrown away at the hop that held it.
//
// WHY A STUB pgx.Tx RATHER THAN A DATABASE. `tx` is an interface, so the error
// can be chosen exactly, which is the only way to assert the mapping arm by arm
// — a real 40001 cannot be aimed at one specific statement (see the DB-gated
// companion in lock_probe_error_db_test.go, which pays for the end-to-end hop a
// different way). These arms and that one answer different questions: this file
// asks "does this function classify what it is handed", that one asks "does the
// classification survive the trip out through FnClaimWorkItem to the caller".
// Neither implies the other, and the unit arms alone would be exactly the
// "proves the classifier compiles" trap that serialization_failure_db_test.go
// warns about.
//
// MUTANT (the pre-aihub#410 build): delete the `if !errors.Is(err,
// pgx.ErrNoRows)` branch from probeForeignLockHolders. The 40001, deadlock and
// opaque-error arms go red; the ErrNoRows and found-a-holder arms stay green,
// which is what makes them controls rather than decoration.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// probeStubRow hands back one chosen error from Scan.
type probeStubRow struct{ err error }

func (r probeStubRow) Scan(dest ...any) error { return r.err }

// probeStubTx is a pgx.Tx whose QueryRow fails on demand.
//
// The embedded pgx.Tx is deliberately nil: any method this test does not
// override panics instead of quietly returning a zero value. If the function
// under test grows a second statement, that is a loud failure rather than a
// silently half-exercised arm.
type probeStubTx struct {
	pgx.Tx
	err   error
	calls int
}

func (tx *probeStubTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.calls++
	return probeStubRow{err: tx.err}
}

// oneLockAndProbe is the smallest well-formed (locks, probes) pair. The two
// slices are index-paired by contract, so they are built together here for the
// same reason deriveClaimLocks builds them together.
func oneLockAndProbe() ([]ResourceLockReq, []lockConflictProbe) {
	return []ResourceLockReq{{ResourceType: "file_scope", ResourceKey: "aihub:aihub:internal/domain/run_attempts.go"}},
		[]lockConflictProbe{exactProbe("aihub:aihub:internal/domain/run_attempts.go")}
}

func TestProbeForeignLockHolders_ClassifiesTheErrorsItUsedToSwallow(t *testing.T) {
	ctx := context.Background()
	locks, probes := oneLockAndProbe()

	t.Run("40001 becomes a retryable 409, not silence", func(t *testing.T) {
		tx := &probeStubTx{err: &pgconn.PgError{Code: "40001", Message: "could not serialize access due to read/write dependencies among transactions"}}

		aerr := probeForeignLockHolders(ctx, tx, "wi_subject", locks, probes)

		require.Equal(t, 1, tx.calls, "the probe statement never ran, so this arm proves nothing about it")
		require.NotNil(t, aerr,
			"a serialization failure was reported BY THE PROBE and answered with nil, i.e. \"no conflict\". "+
				"The transaction is now aborted, so the caller will be told 500 INTERNAL_ERROR about whichever "+
				"later statement fails with 25P02 instead of the retryable 409 this SQLSTATE means")
		assert.Equal(t, ErrConflictSerializationFailure, aerr.Code,
			"got %s: %s", aerr.Code, aerr.Message)
		assert.Equal(t, 409, aerr.HTTPStatus,
			"40001 means \"retry and it will work\"; got %d", aerr.HTTPStatus)
		// The details are the machine-readable half — a client retries on
		// retryable:true, not on the prose.
		details, ok := aerr.Details.(map[string]any)
		require.True(t, ok, "details must carry the retry contract, got %#v", aerr.Details)
		assert.Equal(t, true, details["retryable"])
		assert.Equal(t, "40001", details["sqlstate"])
		// The message has to name the probe, or a 409 here is indistinguishable
		// from the one the neighbouring statements raise.
		assert.Contains(t, aerr.Message, "probe lock holders")
		assert.Contains(t, aerr.Message, "file_scope:aihub:aihub:internal/domain/run_attempts.go",
			"the message does not say WHICH lock's probe failed")
	})

	t.Run("40P01 deadlock rides along", func(t *testing.T) {
		tx := &probeStubTx{err: &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}}

		aerr := probeForeignLockHolders(ctx, tx, "wi_subject", locks, probes)

		require.NotNil(t, aerr, "40P01 is the other class-40 rollback and is reachable at every isolation level")
		assert.Equal(t, ErrConflictSerializationFailure, aerr.Code)
		assert.Equal(t, 409, aerr.HTTPStatus)
	})

	t.Run("an opaque error is an error, not a clean probe", func(t *testing.T) {
		// 42P01 undefined_table stands in for every non-class-40 failure. It must
		// NOT become a 409 — that would tell the caller to retry a request that
		// will fail identically forever — but it must not vanish either.
		tx := &probeStubTx{err: &pgconn.PgError{Code: "42P01", Message: `relation "resource_locks" does not exist`}}

		aerr := probeForeignLockHolders(ctx, tx, "wi_subject", locks, probes)

		require.NotNil(t, aerr, "a failed probe was reported as \"no conflict\"")
		assert.Equal(t, ErrInternalError, aerr.Code,
			"a non-class-40 error must not be dressed up as retryable; got %s", aerr.Code)
		assert.Contains(t, aerr.Message, "probe lock holders")
		// dbErrCause, not dbErr: without the driver's text a 500 here says only
		// that something failed somewhere.
		assert.Contains(t, aerr.Message, "resource_locks",
			"the driver's text was dropped, leaving nothing to diagnose the 500 with")
	})

	t.Run("a non-pg error is still not swallowed", func(t *testing.T) {
		// Not every failure arrives as a *pgconn.PgError — a killed connection or
		// a cancelled context does not. errors.Is(err, pgx.ErrNoRows) is the
		// predicate, so those must propagate too.
		tx := &probeStubTx{err: errors.New("write tcp 10.0.0.1:5432: connection reset by peer")}

		aerr := probeForeignLockHolders(ctx, tx, "wi_subject", locks, probes)

		require.NotNil(t, aerr, "a transport failure was reported as \"no conflict\"")
		assert.Equal(t, ErrInternalError, aerr.Code)
	})

	// ── The two controls. Neither may go red on the mutant, or the arms above
	//    are measuring "probeForeignLockHolders returns something" rather than
	//    "it classifies correctly".

	t.Run("CONTROL: ErrNoRows still means nobody else holds it", func(t *testing.T) {
		tx := &probeStubTx{err: pgx.ErrNoRows}

		aerr := probeForeignLockHolders(ctx, tx, "wi_subject", locks, probes)

		require.Equal(t, 1, tx.calls)
		assert.Nil(t, aerr,
			"ErrNoRows is the probe's success answer. Turning it into an error would 409 every claim "+
				"of an uncontended lock, which is a far worse bug than the one aihub#410 fixed")
	})

	t.Run("CONTROL: a row found is still ErrConflictLockTaken with its payload", func(t *testing.T) {
		tx := &probeStubTx{err: nil}

		aerr := probeForeignLockHolders(ctx, tx, "wi_subject", locks, probes)

		require.NotNil(t, aerr, "a holder was found and no conflict was reported")
		assert.Equal(t, ErrConflictLockTaken, aerr.Code)
		details, ok := aerr.Details.(map[string]any)
		require.True(t, ok, "got %#v", aerr.Details)
		assert.Contains(t, details, "conflict_with",
			"the conflict_with payload is what tells the caller whose lock it is; aihub#410 must not have cost it")
	})

	t.Run("CONTROL: every lock is probed, not just the first", func(t *testing.T) {
		// The loop returns on the first error now, so this guards the other
		// direction: a clean probe must keep going. Three ErrNoRows in, three
		// statements out.
		manyLocks := []ResourceLockReq{
			{ResourceType: "file_scope", ResourceKey: "a"},
			{ResourceType: "file_scope", ResourceKey: "b"},
			{ResourceType: "git_branch", ResourceKey: "c"},
		}
		manyProbes := []lockConflictProbe{exactProbe("a"), exactProbe("b"), exactProbe("c")}
		tx := &probeStubTx{err: pgx.ErrNoRows}

		aerr := probeForeignLockHolders(ctx, tx, "wi_subject", manyLocks, manyProbes)

		assert.Nil(t, aerr)
		assert.Equal(t, 3, tx.calls, "the tail of the lock list stopped being checked")
	})
}
