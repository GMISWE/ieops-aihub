package domain

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Postgres class 40 — "transaction rollback". These are the two SQLSTATEs whose
// documented remedy is simply to run the transaction again; neither says
// anything is wrong with the server or with the request.
const (
	pgSerializationFailure = "40001" // could not serialize access ...
	pgDeadlockDetected     = "40P01" // deadlock detected
)

// retryConflictErr classifies a class-40 transaction-rollback error and returns
// the 409 that tells the caller to retry. It returns nil for every other error
// (including a nil one), so it composes as a pre-filter in front of whatever
// mapping a call site already had:
//
//	if aerr := retryConflictErr(err, "lock project"); aerr != nil {
//		return nil, aerr
//	}
//	return nil, NewErr(ErrInternalError, fmt.Sprintf("lock project: %v", err))
//
// Why 409 and not 500 (aihub#334): 40001 arriving as INTERNAL_ERROR tells the
// caller the server is broken, so they go and read logs — the one response that
// cannot fix it. The transaction lost a race; re-running it very likely wins.
//
// Why not 503 either: the service is up and accepting work. It is this one
// transaction that must be repeated, not the request stream that must back off.
//
// Why 40P01 rides along with 40001: they differ only in which concurrency
// mechanism gave up. Postgres breaks a lock cycle by killing one side at ANY
// isolation level, so the deadlock half is reachable on today's default READ
// COMMITTED paths, while 40001 needs REPEATABLE READ or SERIALIZABLE and is for
// now a mine rather than a fire — it arms itself the moment anyone raises the
// isolation level of a pool or a path, and that person will not think to audit
// every write site's error mapping. Nothing else in class 40 belongs here:
// 40002 (integrity constraint violation) and 40003 (statement completion
// unknown) are not safe to blind-retry.
//
// what should be lowercase per Go convention and name the hop that failed.
func retryConflictErr(err error, what string) *AihubError {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil
	}
	switch pgErr.Code {
	case pgSerializationFailure, pgDeadlockDetected:
	default:
		return nil
	}
	return NewErrDetails(ErrConflictSerializationFailure,
		fmt.Sprintf("%s: %v; the transaction was rolled back after losing a concurrency race — retry the request", what, err),
		map[string]any{"retryable": true, "sqlstate": pgErr.Code})
}

// dbErr is the drop-in replacement for a bare NewErr(ErrInternalError, msg) on
// a DB error path: a retryable 409 when Postgres reported a class 40 rollback,
// and byte-identical to NewErr(ErrInternalError, msg) otherwise.
//
// Which of the two forms to use (aihub#334):
//
//   - dbErr / dbErrCause where the non-conflict outcome is exactly a
//     NewErr(ErrInternalError, …). That is the common case, and it is one line,
//     so getting it right is no more typing than getting it wrong.
//   - retryConflictErr, the primitive underneath both, where the non-conflict
//     outcome is something ELSE — a `break`, a `continue`, a best-effort
//     `return nil`. Those sites cannot be a single call, because classifying
//     the error and swallowing it are different answers to different errors.
//
// Both are recognised by the guard in retryable_conflict_guard_test.go, so this
// is a readability rule and not a correctness one.
//
// msg is the message shown to the caller when this is NOT a retryable conflict;
// it does not carry the driver's text, matching the call sites that deliberately
// keep Postgres internals out of API responses. Use dbErrCause where the site
// already appended the error.
func dbErr(err error, msg string) *AihubError {
	if aerr := retryConflictErr(err, msg); aerr != nil {
		return aerr
	}
	return NewErr(ErrInternalError, msg)
}

// dbErrCause is dbErr for the sites whose INTERNAL_ERROR message appends the
// driver's text: the message becomes "what: err", exactly as
// fmt.Sprintf("%s: %v", what, err) produced before.
func dbErrCause(err error, what string) *AihubError {
	if aerr := retryConflictErr(err, what); aerr != nil {
		return aerr
	}
	return NewErr(ErrInternalError, fmt.Sprintf("%s: %v", what, err))
}

// pgxErr translates a low-level pgx error into an *AihubError.
//
// It returns:
//   - nil when err is nil
//   - NewErr(ErrNotFound, notFoundMsg) when err == pgx.ErrNoRows
//   - the 409 from retryConflictErr when err is SQLSTATE 40001 / 40P01
//   - NewErr(ErrInternalError, internalMsg + ": " + err) otherwise
//
// Both messages should be lowercase per Go convention. Callers that need
// to attach Details should construct *AihubError directly.
func pgxErr(err error, notFoundMsg, internalMsg string) *AihubError {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return NewErr(ErrNotFound, notFoundMsg)
	}
	if aerr := retryConflictErr(err, internalMsg); aerr != nil {
		return aerr
	}
	return NewErr(ErrInternalError, fmt.Sprintf("%s: %v", internalMsg, err))
}
