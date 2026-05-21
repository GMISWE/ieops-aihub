package domain

import (
	"strings"
	"testing"
)

func TestAihubError_Error(t *testing.T) {
	e := &AihubError{Code: ErrBadRequest, Message: "bad input"}
	got := e.Error()
	want := "BAD_REQUEST: bad input"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestNewErr(t *testing.T) {
	e := NewErr(ErrNotFound, "not here")
	if e.Code != ErrNotFound {
		t.Errorf("Code = %q, want NOT_FOUND", e.Code)
	}
	if e.Message != "not here" {
		t.Errorf("Message = %q", e.Message)
	}
	if e.Details != nil {
		t.Errorf("Details = %v, want nil", e.Details)
	}
	if e.HTTPStatus != 404 {
		t.Errorf("HTTPStatus = %d, want 404", e.HTTPStatus)
	}
}

func TestNewErrDetails(t *testing.T) {
	details := map[string]any{"k": "v"}
	e := NewErrDetails(ErrConflictDuplicate, "dup", details)
	if e.Code != ErrConflictDuplicate {
		t.Errorf("Code = %q", e.Code)
	}
	if e.HTTPStatus != 409 {
		t.Errorf("HTTPStatus = %d, want 409", e.HTTPStatus)
	}
	m, ok := e.Details.(map[string]any)
	if !ok || m["k"] != "v" {
		t.Errorf("Details = %v", e.Details)
	}
}

func TestCodeToHTTPStatus(t *testing.T) {
	tests := []struct {
		code ErrCode
		want int
	}{
		// 400
		{ErrBadRequest, 400},
		{ErrGoalMultiline, 400},
		{ErrInvalidPhaseYAML, 400},
		{ErrInvalidStepTransition, 400},
		{ErrProjectAmbiguous, 400},
		{ErrInvalidMemoryType, 400},
		// 401
		{ErrUnauthorized, 401},
		{ErrStaleCredential, 401},
		// 403
		{ErrForbidden, 403},
		{ErrAttemptMismatch, 403},
		{ErrWIReclassifyForbidden, 403},
		// 404
		{ErrNotFound, 404},
		// 405
		{ErrNotImplemented, 405},
		// 409
		{ErrConflictEpochMismatch, 409},
		{ErrConflictStepInProgress, 409},
		{ErrConflictStepAttemptMismatch, 409},
		{ErrConflictCASFailed, 409},
		{ErrConflictWIAlreadyClaimed, 409},
		{ErrConflictHardBlock, 409},
		{ErrConflictDuplicate, 409},
		{ErrConflictCandidates, 409},
		{ErrConflictSimilarMemory, 409},
		{ErrConflictDependencyCycle, 409},
		{ErrConflictLockTaken, 409},
		{ErrConflictDualWIAgent, 409},
		{ErrRequiresHumanSessionMismatch, 409},
		{ErrConflictVersionMismatch, 409},
		{ErrConflictTerminalState, 409},
		{ErrWITypeMismatch, 409},
		{ErrGoalChangeNotAllowed, 409},
		// 412
		{ErrPreconditionFailed, 412},
		// 413
		{ErrPayloadTooLarge, 413},
		// 500 (default + explicit)
		{ErrInternalError, 500},
		// 503
		{ErrServiceUnavailable, 503},
		{ErrAihubUnavailable, 503},
		// Unknown code falls back to 500
		{ErrCode("WHATEVER"), 500},
	}
	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			got := codeToHTTPStatus(tt.code)
			if got != tt.want {
				t.Errorf("codeToHTTPStatus(%q) = %d, want %d", tt.code, got, tt.want)
			}
		})
	}
}

// TestAllErrCodesMapped verifies that every declared error code has an explicit
// HTTP-status mapping (i.e. does not silently fall back to 500). This guards
// against new codes being added without updating the switch in codeToHTTPStatus.
func TestAllErrCodesMapped(t *testing.T) {
	all := []ErrCode{
		ErrBadRequest, ErrGoalMultiline, ErrGoalChangeNotAllowed,
		ErrInvalidPhaseYAML, ErrInvalidStepTransition, ErrProjectAmbiguous,
		ErrWITypeMismatch, ErrInvalidMemoryType,
		ErrUnauthorized, ErrStaleCredential,
		ErrForbidden, ErrAttemptMismatch, ErrWIReclassifyForbidden,
		ErrNotFound,
		ErrNotImplemented,
		ErrConflictEpochMismatch, ErrConflictStepInProgress,
		ErrConflictStepAttemptMismatch, ErrConflictCASFailed,
		ErrConflictWIAlreadyClaimed, ErrConflictHardBlock,
		ErrConflictDuplicate, ErrConflictCandidates,
		ErrConflictSimilarMemory, ErrConflictDependencyCycle,
		ErrConflictLockTaken, ErrConflictDualWIAgent,
		ErrRequiresHumanSessionMismatch, ErrConflictVersionMismatch,
		ErrConflictTerminalState,
		ErrPreconditionFailed,
		ErrPayloadTooLarge,
		ErrServiceUnavailable, ErrAihubUnavailable,
	}
	for _, code := range all {
		if got := codeToHTTPStatus(code); got == 500 && !strings.Contains(string(code), "INTERNAL") {
			t.Errorf("code %q has no explicit HTTP mapping (falls back to 500)", code)
		}
	}
	// ErrInternalError itself must map to 500.
	if codeToHTTPStatus(ErrInternalError) != 500 {
		t.Errorf("INTERNAL_ERROR should map to 500")
	}
}
