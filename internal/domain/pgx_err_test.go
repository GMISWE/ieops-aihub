package domain

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPgxErr_Nil(t *testing.T) {
	if got := pgxErr(nil, "nf", "ie"); got != nil {
		t.Errorf("nil err: got %v, want nil", got)
	}
}

func TestPgxErr_NoRows(t *testing.T) {
	got := pgxErr(pgx.ErrNoRows, "thing not found", "should not appear")
	if got == nil {
		t.Fatal("want non-nil")
	}
	if got.Code != ErrNotFound {
		t.Errorf("Code = %q, want NOT_FOUND", got.Code)
	}
	if got.Message != "thing not found" {
		t.Errorf("Message = %q", got.Message)
	}
	if got.HTTPStatus != 404 {
		t.Errorf("HTTPStatus = %d, want 404", got.HTTPStatus)
	}
}

func TestPgxErr_OtherError(t *testing.T) {
	src := errors.New("connection refused")
	got := pgxErr(src, "irrelevant", "load failed")
	if got.Code != ErrInternalError {
		t.Errorf("Code = %q, want INTERNAL_ERROR", got.Code)
	}
	if !strings.Contains(got.Message, "load failed") {
		t.Errorf("Message %q missing internal prefix", got.Message)
	}
	if !strings.Contains(got.Message, "connection refused") {
		t.Errorf("Message %q missing underlying error", got.Message)
	}
	if got.HTTPStatus != 500 {
		t.Errorf("HTTPStatus = %d, want 500", got.HTTPStatus)
	}
}

func TestPgxErr_WrappedNoRows(t *testing.T) {
	// errors.Is should still match a wrapped pgx.ErrNoRows.
	wrapped := fmt.Errorf("scan: %w", pgx.ErrNoRows)
	got := pgxErr(wrapped, "nf", "ie")
	if got == nil || got.Code != ErrNotFound {
		t.Errorf("wrapped no-rows: got %v, want NOT_FOUND", got)
	}
}

// aihub#334. The DB-gated companion in serialization_failure_db_test.go proves
// 40001 really arrives on UpdateProject's FOR UPDATE hop and comes back as a
// 409; these cases cover what a race cannot be made to produce on demand — the
// 40P01 half, and the negative cases that keep the classifier from swallowing
// errors that are NOT retryable.
func TestRetryConflictErr_ClassifiesRollbackSQLSTATEs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sqlstate string
	}{
		{"serialization failure", "40001"},
		{"deadlock detected", "40P01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &pgconn.PgError{Code: tc.sqlstate, Message: "some rollback"}
			got := retryConflictErr(src, "lock project")
			if got == nil {
				t.Fatalf("SQLSTATE %s was not classified as retryable, so it still reaches the caller as a 500", tc.sqlstate)
			}
			if got.Code != ErrConflictSerializationFailure {
				t.Errorf("Code = %q, want CONFLICT_SERIALIZATION_FAILURE", got.Code)
			}
			if got.HTTPStatus != 409 {
				t.Errorf("HTTPStatus = %d, want 409 — a 500 sends the caller to the logs instead of retrying", got.HTTPStatus)
			}
			if !strings.Contains(got.Message, "lock project") {
				t.Errorf("Message %q dropped the hop that failed", got.Message)
			}
			details, ok := got.Details.(map[string]any)
			if !ok {
				t.Fatalf("Details = %#v, want a map carrying retry guidance", got.Details)
			}
			if details["retryable"] != true {
				t.Errorf("details[retryable] = %#v, want true", details["retryable"])
			}
			if details["sqlstate"] != tc.sqlstate {
				t.Errorf("details[sqlstate] = %#v, want %q", details["sqlstate"], tc.sqlstate)
			}
		})
	}
}

// The mutation guard for the test above: a classifier that returned a 409 for
// everything would pass it while breaking every other DB error in the repo.
func TestRetryConflictErr_LeavesEverythingElseAlone(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"not a postgres error", errors.New("connection refused")},
		{"unique violation", &pgconn.PgError{Code: "23505"}},
		{"check constraint violation", &pgconn.PgError{Code: "23514"}},
		{"integrity constraint violation in the same class", &pgconn.PgError{Code: "40002"}},
		{"statement completion unknown in the same class", &pgconn.PgError{Code: "40003"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryConflictErr(tc.err, "hop"); got != nil {
				t.Errorf("got %v, want nil — this error is not safe to blind-retry", got)
			}
		})
	}
}

// pgxErr is the shared translation helper, so the 40001 mapping must be visible
// through it and not only at the call sites patched by hand.
func TestPgxErr_RetryableRollbackBecomes409(t *testing.T) {
	got := pgxErr(fmt.Errorf("query: %w", &pgconn.PgError{Code: "40001"}), "nf", "load failed")
	if got == nil {
		t.Fatal("want non-nil")
	}
	if got.Code != ErrConflictSerializationFailure || got.HTTPStatus != 409 {
		t.Errorf("got %d %s, want 409 CONFLICT_SERIALIZATION_FAILURE", got.HTTPStatus, got.Code)
	}
}
