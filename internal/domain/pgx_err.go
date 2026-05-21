package domain

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// pgxErr translates a low-level pgx error into an *AihubError.
//
// It returns:
//   - nil when err is nil
//   - NewErr(ErrNotFound, notFoundMsg) when err == pgx.ErrNoRows
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
	return NewErr(ErrInternalError, fmt.Sprintf("%s: %v", internalMsg, err))
}
