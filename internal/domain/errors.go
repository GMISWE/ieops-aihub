package domain

import "fmt"

// ErrCode is a machine-readable error code. All values match §17 of the design doc.
type ErrCode string

const (
	// HTTP 400
	ErrBadRequest              ErrCode = "BAD_REQUEST"
	ErrGoalMultiline           ErrCode = "GOAL_MULTILINE"
	ErrGoalChangeNotAllowed    ErrCode = "GOAL_CHANGE_NOT_ALLOWED"
	ErrInvalidPhaseYAML        ErrCode = "INVALID_PHASE_YAML"
	ErrInvalidStepTransition   ErrCode = "INVALID_STEP_TRANSITION"
	ErrProjectAmbiguous        ErrCode = "PROJECT_AMBIGUOUS"
	ErrWITypeMismatch          ErrCode = "WI_TYPE_MISMATCH"
	ErrInvalidMemoryType       ErrCode = "INVALID_MEMORY_TYPE"

	// HTTP 401
	ErrUnauthorized      ErrCode = "UNAUTHORIZED"
	ErrStaleCredential   ErrCode = "STALE_LOCAL_CREDENTIAL"

	// HTTP 403
	ErrForbidden                  ErrCode = "FORBIDDEN"
	ErrAttemptMismatch            ErrCode = "ATTEMPT_MISMATCH"
	ErrWIReclassifyForbidden      ErrCode = "WI_RECLASSIFY_FORBIDDEN"

	// HTTP 404
	ErrNotFound ErrCode = "NOT_FOUND"

	// HTTP 405
	ErrNotImplemented ErrCode = "NOT_IMPLEMENTED"

	// HTTP 409
	ErrConflictEpochMismatch            ErrCode = "CONFLICT_EPOCH_MISMATCH"
	ErrConflictStepInProgress           ErrCode = "CONFLICT_STEP_IN_PROGRESS"
	ErrConflictStepAttemptMismatch      ErrCode = "CONFLICT_STEP_ATTEMPT_MISMATCH"
	ErrConflictCASFailed                ErrCode = "CONFLICT_CAS_FAILED"
	ErrConflictWIAlreadyClaimed         ErrCode = "CONFLICT_WI_ALREADY_CLAIMED"
	ErrConflictHardBlock                ErrCode = "CONFLICT_HARD_BLOCK"
	ErrConflictDuplicate                ErrCode = "CONFLICT_DUPLICATE"
	ErrConflictCandidates               ErrCode = "CONFLICT_CANDIDATES"
	ErrConflictSimilarMemory            ErrCode = "CONFLICT_SIMILAR_MEMORY"
	ErrConflictDependencyCycle          ErrCode = "CONFLICT_DEPENDENCY_CYCLE"
	ErrConflictLockTaken                ErrCode = "CONFLICT_LOCK_TAKEN"
	ErrConflictDualWIAgent              ErrCode = "CONFLICT_DUAL_WI_AGENT"
	ErrRequiresHumanSessionMismatch     ErrCode = "REQUIRES_HUMAN_SESSION_MISMATCH"
	ErrConflictVersionMismatch          ErrCode = "CONFLICT_VERSION_MISMATCH"
	ErrConflictTerminalState            ErrCode = "CONFLICT_TERMINAL_STATE"

	// HTTP 412
	ErrPreconditionFailed ErrCode = "PRECONDITION_FAILED"

	// HTTP 413
	ErrPayloadTooLarge ErrCode = "PAYLOAD_TOO_LARGE"

	// HTTP 500
	ErrInternalError ErrCode = "INTERNAL_ERROR"

	// HTTP 503
	ErrServiceUnavailable ErrCode = "SERVICE_UNAVAILABLE"
	ErrAihubUnavailable   ErrCode = "AIHUB_UNAVAILABLE"
)

// AihubError is the canonical error type for all API errors.
// JSON encoding matches the envelope: {"code":"...","message":"...","details":{...}}
type AihubError struct {
	Code       ErrCode `json:"code"`
	Message    string  `json:"message"`
	Details    any     `json:"details,omitempty"`
	HTTPStatus int     `json:"-"` // not serialized; used to set HTTP response code
}

func (e *AihubError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// NewErr creates an AihubError with no details.
func NewErr(code ErrCode, msg string) *AihubError {
	return &AihubError{Code: code, Message: msg, HTTPStatus: codeToHTTPStatus(code)}
}

// NewErrDetails creates an AihubError with details.
func NewErrDetails(code ErrCode, msg string, details any) *AihubError {
	return &AihubError{Code: code, Message: msg, Details: details, HTTPStatus: codeToHTTPStatus(code)}
}

// codeToHTTPStatus maps ErrCode to the HTTP status defined in §17.
func codeToHTTPStatus(code ErrCode) int {
	switch code {
	case ErrBadRequest, ErrGoalMultiline, ErrGoalChangeNotAllowed,
		ErrInvalidPhaseYAML, ErrInvalidStepTransition, ErrProjectAmbiguous,
		ErrWITypeMismatch, ErrInvalidMemoryType:
		return 400
	case ErrUnauthorized, ErrStaleCredential:
		return 401
	case ErrForbidden, ErrAttemptMismatch, ErrWIReclassifyForbidden:
		return 403
	case ErrNotFound:
		return 404
	case ErrNotImplemented:
		return 405
	case ErrConflictEpochMismatch, ErrConflictStepInProgress,
		ErrConflictStepAttemptMismatch, ErrConflictCASFailed,
		ErrConflictWIAlreadyClaimed, ErrConflictHardBlock,
		ErrConflictDuplicate, ErrConflictCandidates,
		ErrConflictSimilarMemory, ErrConflictDependencyCycle,
		ErrConflictLockTaken, ErrConflictDualWIAgent,
		ErrRequiresHumanSessionMismatch, ErrConflictVersionMismatch,
		ErrConflictTerminalState:
		return 409
	case ErrPreconditionFailed:
		return 412
	case ErrPayloadTooLarge:
		return 413
	case ErrServiceUnavailable, ErrAihubUnavailable:
		return 503
	default:
		return 500
	}
}
