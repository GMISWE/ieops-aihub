package domain

import "fmt"

// ErrCode is a machine-readable error code. All values match §17 of the design doc.
type ErrCode string

const (
	// HTTP 400
	ErrBadRequest            ErrCode = "BAD_REQUEST"
	ErrGoalMultiline         ErrCode = "GOAL_MULTILINE"
	ErrGoalChangeNotAllowed  ErrCode = "GOAL_CHANGE_NOT_ALLOWED"
	ErrInvalidPhaseYAML      ErrCode = "INVALID_PHASE_YAML"
	ErrInvalidStepTransition ErrCode = "INVALID_STEP_TRANSITION"
	ErrProjectAmbiguous      ErrCode = "PROJECT_AMBIGUOUS"
	ErrWITypeMismatch        ErrCode = "WI_TYPE_MISMATCH"
	ErrInvalidMemoryType     ErrCode = "INVALID_MEMORY_TYPE"

	// HTTP 401
	ErrUnauthorized    ErrCode = "UNAUTHORIZED"
	ErrStaleCredential ErrCode = "STALE_LOCAL_CREDENTIAL"

	// HTTP 403
	ErrForbidden             ErrCode = "FORBIDDEN"
	ErrAttemptMismatch       ErrCode = "ATTEMPT_MISMATCH"
	ErrWIReclassifyForbidden ErrCode = "WI_RECLASSIFY_FORBIDDEN"

	// HTTP 404
	ErrNotFound ErrCode = "NOT_FOUND"

	// HTTP 405
	ErrNotImplemented ErrCode = "NOT_IMPLEMENTED"

	// HTTP 409
	ErrConflictEpochMismatch       ErrCode = "CONFLICT_EPOCH_MISMATCH"
	ErrAttemptPaused               ErrCode = "ATTEMPT_PAUSED"
	ErrConflictStepInProgress      ErrCode = "CONFLICT_STEP_IN_PROGRESS"
	ErrConflictStepAttemptMismatch ErrCode = "CONFLICT_STEP_ATTEMPT_MISMATCH"
	ErrConflictCASFailed           ErrCode = "CONFLICT_CAS_FAILED"
	ErrConflictWIAlreadyClaimed    ErrCode = "CONFLICT_WI_ALREADY_CLAIMED"
	ErrConflictHardBlock           ErrCode = "CONFLICT_HARD_BLOCK"
	ErrConflictDuplicate           ErrCode = "CONFLICT_DUPLICATE"
	ErrConflictCandidates          ErrCode = "CONFLICT_CANDIDATES"
	ErrConflictSimilarMemory       ErrCode = "CONFLICT_SIMILAR_MEMORY"
	ErrConflictDependencyCycle     ErrCode = "CONFLICT_DEPENDENCY_CYCLE"
	ErrConflictLockTaken           ErrCode = "CONFLICT_LOCK_TAKEN"
	ErrConflictDualWIAgent         ErrCode = "CONFLICT_DUAL_WI_AGENT"
	// ErrRequiresHumanSessionMismatch has HAD NO EMITTER since aihub#359, and never really had
	// one: its only call site was an `else if *wi.RequiresHumanSession != resolvedRHS` in
	// FnClaimWorkItem that compared the work item row against a value just assigned from that
	// same row, so the condition was `x != x` and the 409 was unreachable. The branch is gone;
	// the code is kept only so historical responses and clients that switch on it still decode.
	//
	// The design doc still describes this 409 as a live safeguard — §8.4's claim flow, §17's
	// error table, and §27 KL3, which names it as the mitigation that forces a mis-classified
	// critical_bug to be corrected. That mitigation does not exist and never fired. Do not build
	// on it. Re-emitting this code needs a second, INDEPENDENT source for the expected value;
	// the per-wi_type defaults live in the scenario repo, which only the client can read.
	ErrRequiresHumanSessionMismatch ErrCode = "REQUIRES_HUMAN_SESSION_MISMATCH"
	ErrConflictVersionMismatch      ErrCode = "CONFLICT_VERSION_MISMATCH"
	ErrConflictTerminalState        ErrCode = "CONFLICT_TERMINAL_STATE"
	// ErrConflictSerializationFailure is Postgres class 40 (transaction
	// rollback) surfaced to the caller: the server is healthy and the request
	// was valid, the transaction just lost a race and re-running it will very
	// likely succeed. Details carry {retryable:true, sqlstate:"40001"|"40P01"}
	// so a client can decide without parsing the message. See aihub#334.
	ErrConflictSerializationFailure ErrCode = "CONFLICT_SERIALIZATION_FAILURE"
	// ErrIdempotencyKeyReused is returned when an Idempotency-Key that is still
	// cached is presented with a DIFFERENT request (method, target or body). The
	// key names one operation, so the server can neither replay the first
	// response — that was the aihub#152 defect, a stale body served for an
	// unrelated call — nor execute the second under a key that already stands for
	// something else. Retry with a fresh key.
	ErrIdempotencyKeyReused ErrCode = "IDEMPOTENCY_KEY_REUSED"

	// HTTP 412
	ErrPreconditionFailed ErrCode = "PRECONDITION_FAILED"
	// ErrProjectMembersUndeclaredRemoval is aihub#333: a `members` write would
	// take access away from somebody the request did not name in
	// expected_removals. `members` is a whole-list REPLACE, so a caller who
	// sends a short list removes everyone they left out — and aihub#260's
	// members_version cannot see that, because a truncating caller's version
	// matches perfectly.
	//
	// 412 and not 409, deliberately. A 409 CONFLICT_CAS_FAILED means "somebody
	// wrote before you, reread and retry" and clients loop on it; retrying this
	// one re-sends the same short list forever. The request is well-formed and
	// its acceptability depends on stored state, which is what 412 is for, and
	// details carry retryable:false so a client need not parse the message to
	// know that.
	ErrProjectMembersUndeclaredRemoval ErrCode = "PROJECT_MEMBERS_UNDECLARED_REMOVAL"

	// HTTP 413
	ErrPayloadTooLarge ErrCode = "PAYLOAD_TOO_LARGE"

	// HTTP 500
	ErrInternalError ErrCode = "INTERNAL_ERROR"

	// HTTP 503
	ErrServiceUnavailable ErrCode = "SERVICE_UNAVAILABLE"
	ErrAihubUnavailable   ErrCode = "AIHUB_UNAVAILABLE"

	// Projects
	ErrProjectNotFound           ErrCode = "PROJECT_NOT_FOUND"
	ErrProjectAlreadyExists      ErrCode = "PROJECT_ALREADY_EXISTS"
	ErrProjectNameInvalid        ErrCode = "PROJECT_NAME_INVALID"
	ErrProjectScenarioInvalid    ErrCode = "PROJECT_SCENARIO_INVALID"
	ErrProjectAccessDenied       ErrCode = "PROJECT_ACCESS_DENIED"
	ErrProjectOwnerRequired      ErrCode = "PROJECT_OWNER_REQUIRED"
	ErrProjectHasWorkItems       ErrCode = "PROJECT_HAS_WORK_ITEMS"
	ErrRepoDuplicateName         ErrCode = "REPO_DUPLICATE_NAME"
	ErrRepoDuplicateURL          ErrCode = "REPO_DUPLICATE_URL"
	ErrRepoIncompleteDescription ErrCode = "REPO_INCOMPLETE_DESCRIPTION"
	ErrInvalidProjectIdentifier  ErrCode = "INVALID_PROJECT_IDENTIFIER"
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
	case ErrBadRequest, ErrGoalMultiline,
		ErrInvalidPhaseYAML, ErrInvalidStepTransition, ErrProjectAmbiguous,
		ErrInvalidMemoryType:
		return 400
	case ErrUnauthorized, ErrStaleCredential:
		return 401
	case ErrForbidden, ErrAttemptMismatch, ErrWIReclassifyForbidden:
		return 403
	case ErrNotFound:
		return 404
	case ErrNotImplemented:
		return 405
	case ErrConflictEpochMismatch, ErrAttemptPaused, ErrConflictStepInProgress,
		ErrConflictStepAttemptMismatch, ErrConflictCASFailed,
		ErrConflictWIAlreadyClaimed, ErrConflictHardBlock,
		ErrConflictDuplicate, ErrConflictCandidates,
		ErrConflictSimilarMemory, ErrConflictDependencyCycle,
		ErrConflictLockTaken, ErrConflictDualWIAgent,
		ErrRequiresHumanSessionMismatch, ErrConflictVersionMismatch,
		ErrConflictTerminalState, ErrConflictSerializationFailure,
		ErrIdempotencyKeyReused,
		// G6 / design §17: WI_TYPE_MISMATCH is 409 (conflict between wi_type and config)
		ErrWITypeMismatch,
		// G6 / design §4.3 line 1138: GOAL_CHANGE_NOT_ALLOWED is 409, not 400
		ErrGoalChangeNotAllowed:
		return 409
	case ErrPreconditionFailed, ErrProjectMembersUndeclaredRemoval:
		return 412
	case ErrPayloadTooLarge:
		return 413
	case ErrServiceUnavailable, ErrAihubUnavailable:
		return 503
	case ErrProjectNotFound:
		return 404
	case ErrProjectAlreadyExists:
		return 409
	case ErrProjectNameInvalid, ErrProjectScenarioInvalid, ErrProjectHasWorkItems, ErrRepoDuplicateName, ErrRepoDuplicateURL, ErrRepoIncompleteDescription, ErrInvalidProjectIdentifier:
		return 400
	case ErrProjectAccessDenied, ErrProjectOwnerRequired:
		return 403
	default:
		return 500
	}
}
