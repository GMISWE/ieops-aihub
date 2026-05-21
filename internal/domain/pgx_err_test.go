package domain

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
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
