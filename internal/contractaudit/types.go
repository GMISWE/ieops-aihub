// Package contractaudit evaluates a closed, fixture-only mutation audit contract.
// It deliberately performs no I/O and is not connected to runtime workflow code.
package contractaudit

import (
	"fmt"
	"time"
)

const (
	ContractVersion = "0.2-gate-semantics-and-mutation-audit"
	CandidateID     = "gate-semantics-and-mutation-audit"
)

type Status string
type ActorClass string
type MutationTier string
type ExpectedOutcome string
type ObservedOutcome string
type Polarity string
type ReaderID string
type SourceKind string
type Verdict string
type ReasonCode string
type PublicationStatus string
type ReplayStatus string
type FreshnessStatus string
type Cardinality string

const (
	StatusQueued                Status            = "queued"
	StatusRunning               Status            = "running"
	StatusPaused                Status            = "paused"
	StatusBlocked               Status            = "blocked"
	StatusWrapped               Status            = "wrapped"
	StatusFailed                Status            = "failed"
	StatusCancelled             Status            = "cancelled"
	ActorReporter               ActorClass        = "reporter"
	ActorMaintainer             ActorClass        = "project_maintainer"
	ActorAdmin                  ActorClass        = "admin"
	ActorOther                  ActorClass        = "other"
	TierContract                MutationTier      = "contract"
	TierWorking                 MutationTier      = "working"
	TierRecord                  MutationTier      = "record"
	TierRider                   MutationTier      = "non_mutating_rider"
	ExpectedAllow               ExpectedOutcome   = "allow"
	ExpectedRefuse              ExpectedOutcome   = "refuse"
	ExpectedHold                ExpectedOutcome   = "hold"
	ObservedAllowed             ObservedOutcome   = "allowed"
	ObservedRefused             ObservedOutcome   = "refused"
	ObservedHeld                ObservedOutcome   = "held"
	ObservedMalformed           ObservedOutcome   = "malformed"
	ObservedSkipped             ObservedOutcome   = "skipped"
	Positive                    Polarity          = "positive"
	Negative                    Polarity          = "negative"
	FixtureReader               ReaderID          = "isolated_fixture_reader"
	DBReader                    ReaderID          = "isolated_db_reader"
	SourceFixture               SourceKind        = "fixture"
	SourceIsolatedDB            SourceKind        = "isolated_db"
	VerdictPass                 Verdict           = "pass"
	VerdictFail                 Verdict           = "fail"
	VerdictHold                 Verdict           = "hold"
	NotPublishable              PublicationStatus = "not_publishable"
	ReplayFirst                 ReplayStatus      = "first_run"
	ReplayIdentical             ReplayStatus      = "identical_replay"
	ReplayMismatch              ReplayStatus      = "replay_mismatch"
	Fresh                       FreshnessStatus   = "fresh"
	Stale                       FreshnessStatus   = "stale"
	Changed                     FreshnessStatus   = "changed_during_check"
	Unprovable                  FreshnessStatus   = "unprovable"
	ExactlyOne                  Cardinality       = "exactly_one"
	ExactlyZero                 Cardinality       = "exactly_zero"
	AtMostOne                   Cardinality       = "at_most_one"
	InvalidInput                ReasonCode        = "INVALID_INPUT"
	UnknownField                ReasonCode        = "UNKNOWN_FIELD"
	InvalidDomainValue          ReasonCode        = "INVALID_DOMAIN_VALUE"
	AuthorityUntrusted          ReasonCode        = "AUTHORITY_UNTRUSTED"
	ScopeMismatch               ReasonCode        = "SCOPE_MISMATCH"
	AuthorityUnavailable        ReasonCode        = "AUTHORITY_UNAVAILABLE"
	AuthorityInvalid            ReasonCode        = "AUTHORITY_INVALID"
	InventoryIncomplete         ReasonCode        = "INVENTORY_INCOMPLETE"
	InventoryDigestMismatch     ReasonCode        = "INVENTORY_DIGEST_MISMATCH"
	DuplicateInventoryEntry     ReasonCode        = "DUPLICATE_INVENTORY_ENTRY"
	CoverageIncomplete          ReasonCode        = "COVERAGE_INCOMPLETE"
	DuplicateMatrixCell         ReasonCode        = "DUPLICATE_MATRIX_CELL"
	UnexpectedMatrixCell        ReasonCode        = "UNEXPECTED_MATRIX_CELL"
	UnclassifiedSurface         ReasonCode        = "UNCLASSIFIED_SURFACE"
	VacuousProbe                ReasonCode        = "VACUOUS_PROBE"
	ProbeNotExecuted            ReasonCode        = "PROBE_NOT_EXECUTED"
	ProbeSkipped                ReasonCode        = "PROBE_SKIPPED"
	EffectUnprovable            ReasonCode        = "EFFECT_UNPROVABLE"
	NonEffectUnprovable         ReasonCode        = "NON_EFFECT_UNPROVABLE"
	FreshnessUnprovable         ReasonCode        = "FRESHNESS_UNPROVABLE"
	StaleSnapshot               ReasonCode        = "STALE_SNAPSHOT"
	AuthorityChangedDuringCheck ReasonCode        = "AUTHORITY_CHANGED_DURING_CHECK"
	ReplayInputMismatch         ReasonCode        = "REPLAY_INPUT_MISMATCH"
	ReplayOutputMismatch        ReasonCode        = "REPLAY_OUTPUT_MISMATCH"
	IsolationRequired           ReasonCode        = "ISOLATION_REQUIRED"
	EvidenceNotClosed           ReasonCode        = "EVIDENCE_NOT_CLOSED"
	EvidenceNotAuthoritative    ReasonCode        = "EVIDENCE_NOT_AUTHORITATIVE"
	ExecutionNotProven          ReasonCode        = "EXECUTION_NOT_PROVEN"
	PolicyMismatch              ReasonCode        = "POLICY_MISMATCH"
	ErrorKindMismatch           ReasonCode        = "ERROR_KIND_MISMATCH"
	StateGateWins               ReasonCode        = "STATE_GATE_WINS"
	TerminalGateWins            ReasonCode        = "TERMINAL_GATE_WINS"
	AuthorityGateWins           ReasonCode        = "AUTHORITY_GATE_WINS"
	UnexpectedMutation          ReasonCode        = "UNEXPECTED_MUTATION"
	ForbiddenMutation           ReasonCode        = "FORBIDDEN_MUTATION"
	PassComplete                ReasonCode        = "PASS_COMPLETE"
)

type ValidationError struct {
	Pointer string
	Code    ReasonCode
	Detail  string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s at %s: %s", e.Code, e.Pointer, e.Detail)
}

type VersionToken struct {
	StepsVersion    int       `json:"steps_version"`
	SchemaDigest    string    `json:"schema_digest"`
	InventoryDigest string    `json:"inventory_digest"`
	PolicyDigest    string    `json:"policy_digest"`
	ObservedAt      time.Time `json:"observed_at"`
}
type Subject struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Scope string `json:"scope"`
}
type Authority struct {
	ReaderID      ReaderID     `json:"reader_id"`
	SourceKind    SourceKind   `json:"source_kind"`
	SourceRef     string       `json:"source_ref"`
	Scope         string       `json:"scope"`
	SnapshotToken VersionToken `json:"snapshot_token"`
}
type FreshnessPolicy struct {
	CheckedAt              time.Time `json:"checked_at"`
	MaxAgeSeconds          int       `json:"max_age_seconds"`
	RequireBoundaryRecheck bool      `json:"require_boundary_recheck"`
	RequiredTokens         []string  `json:"required_tokens"`
}
type PrecedenceRule struct {
	When   string `json:"when"`
	Winner string `json:"winner"`
}
type MatrixCell struct {
	CellID         string          `json:"cell_id"`
	Status         Status          `json:"status"`
	Actor          ActorClass      `json:"actor"`
	Tier           MutationTier    `json:"tier"`
	OperationClass string          `json:"operation_class"`
	OperationID    string          `json:"operation_id"`
	Target         string          `json:"target"`
	Expected       ExpectedOutcome `json:"expected"`
	ExpectedReason ReasonCode      `json:"expected_reason,omitempty"`
	PrecedenceWhen string          `json:"precedence_when,omitempty"`
	ProbeIDs       []string        `json:"probe_ids"`
}
type Policy struct {
	LegalStatuses []Status         `json:"legal_statuses"`
	ActorClasses  []ActorClass     `json:"actor_classes"`
	MutationTiers []MutationTier   `json:"mutation_tiers"`
	Precedence    []PrecedenceRule `json:"precedence"`
	Cells         []MatrixCell     `json:"cells"`
}
type MutationOperation struct {
	OperationID    string       `json:"operation_id"`
	OperationClass string       `json:"operation_class"`
	FieldPath      string       `json:"field_path"`
	SourceLocator  string       `json:"source_locator"`
	Writable       bool         `json:"writable"`
	Tier           MutationTier `json:"tier"`
	RiderReason    string       `json:"rider_reason,omitempty"`
	SchemaDigest   string       `json:"schema_digest"`
}
type MutationInventory struct {
	SourceRef      string              `json:"source_ref"`
	SourceDigest   string              `json:"source_digest"`
	Operations     []MutationOperation `json:"operations"`
	ExplicitRiders []string            `json:"explicit_riders"`
}

// ProbeRequest is deliberately closed: fixture executions cannot carry arbitrary Go values.
type ProbeRequest struct {
	OperationID    string       `json:"operation_id"`
	OperationClass string       `json:"operation_class"`
	Target         string       `json:"target"`
	Tier           MutationTier `json:"tier"`
	SubjectRef     string       `json:"subject_ref"`
	PayloadDigest  string       `json:"payload_digest"`
}
type Observation struct {
	Outcome        ObservedOutcome `json:"outcome"`
	ReasonCode     ReasonCode      `json:"reason_code,omitempty"`
	ErrorClass     string          `json:"error_class,omitempty"`
	ResponseDigest string          `json:"response_digest"`
}
type EffectExpectation struct {
	Target           string      `json:"target"`
	OperationID      string      `json:"operation_id"`
	Cardinality      Cardinality `json:"cardinality"`
	IntendedChange   string      `json:"intended_change,omitempty"`
	ForbiddenChanges []string    `json:"forbidden_changes"`
}

// ObservedEffect binds a typed target/operation/change claim to distinct pre/post snapshots.
type ObservedEffect struct {
	Target      string `json:"target"`
	OperationID string `json:"operation_id"`
	Change      string `json:"change"`
	PreDigest   string `json:"pre_digest"`
	PostDigest  string `json:"post_digest"`
}
type ReaderSelector struct {
	Target      string      `json:"target"`
	OperationID string      `json:"operation_id"`
	Cardinality Cardinality `json:"cardinality"`
	Change      string      `json:"change"`
}
type ReaderRef struct {
	ReaderID      ReaderID       `json:"reader_id"`
	SourceKind    SourceKind     `json:"source_kind"`
	SourceRef     string         `json:"source_ref"`
	Selector      ReaderSelector `json:"selector"`
	Scope         string         `json:"scope"`
	SnapshotToken VersionToken   `json:"snapshot_token"`
	EvidenceStart time.Time      `json:"evidence_start"`
	EvidenceEnd   time.Time      `json:"evidence_end"`
}
type Probe struct {
	ProbeID         string            `json:"probe_id"`
	CellID          string            `json:"cell_id"`
	Polarity        Polarity          `json:"polarity"`
	Request         ProbeRequest      `json:"request"`
	Observed        Observation       `json:"observed"`
	ExpectedEffect  EffectExpectation `json:"expected_effect"`
	ObservedEffects []ObservedEffect  `json:"observed_effects"`
	EffectReader    ReaderRef         `json:"effect_reader"`
	ExecutedAt      time.Time         `json:"executed_at"`
	ExecutionRef    string            `json:"execution_ref"`
}
type Controls struct {
	Probes             []Probe  `json:"probes"`
	PositiveControlIDs []string `json:"positive_control_ids"`
	NegativeControlIDs []string `json:"negative_control_ids"`
}
type EvidencePolicy struct {
	RequireClosedRefs    bool `json:"require_closed_refs"`
	RequireExecutionRef  bool `json:"require_execution_ref"`
	AllowCachedAuthority bool `json:"allow_cached_authority"`
	RedactValues         bool `json:"redact_values"`
}
type ReplayRecord struct {
	RequestID     string    `json:"request_id"`
	ReplayKey     string    `json:"replay_key"`
	InputDigest   string    `json:"input_digest"`
	OutputDigest  string    `json:"output_digest"`
	ResultVersion string    `json:"result_version"`
	RecordedAt    time.Time `json:"recorded_at"`
}
type ReplayContext struct {
	RequestID   string        `json:"request_id"`
	ReplayKey   string        `json:"replay_key"`
	PriorResult *ReplayRecord `json:"prior_result,omitempty"`
}
type AuditInput struct {
	ContractVersion     string            `json:"contract_version"`
	CandidateID         string            `json:"candidate_id"`
	Subject             Subject           `json:"subject"`
	Authority           Authority         `json:"authority"`
	Freshness           FreshnessPolicy   `json:"freshness"`
	Policy              Policy            `json:"policy"`
	MutationInventory   MutationInventory `json:"mutation_inventory"`
	Controls            Controls          `json:"controls"`
	Evidence            EvidencePolicy    `json:"evidence"`
	Replay              ReplayContext     `json:"replay"`
	RequiresStepHistory bool              `json:"requires_step_history"`
}

type Coverage struct {
	InventoryCount     int  `json:"inventory_count"`
	ClassifiedCount    int  `json:"classified_count"`
	MissingCells       int  `json:"missing_cells"`
	ProbeCount         int  `json:"probe_count"`
	ExecutedProbeCount int  `json:"executed_probe_count"`
	SkippedProbeCount  int  `json:"skipped_probe_count"`
	Complete           bool `json:"complete"`
}
type CellResult struct {
	CellID            string          `json:"cell_id"`
	Expected          ExpectedOutcome `json:"expected"`
	Observed          ObservedOutcome `json:"observed"`
	ReasonCode        ReasonCode      `json:"reason_code,omitempty"`
	EffectVerified    bool            `json:"effect_verified"`
	NonEffectVerified bool            `json:"non_effect_verified"`
	ProbeIDs          []string        `json:"probe_ids"`
}
type SemanticConflict struct {
	Kind     string `json:"kind"`
	Ref      string `json:"ref"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
}
type MutationAudit struct {
	AllWritesInventoryBound   bool     `json:"all_writes_inventory_bound"`
	IntendedEffectsVerified   int      `json:"intended_effects_verified"`
	ForbiddenEffectsObserved  int      `json:"forbidden_effects_observed"`
	RefusalNonEffectsVerified int      `json:"refusal_non_effects_verified"`
	UnexpectedMutations       []string `json:"unexpected_mutations"`
}
type FreshnessResult struct {
	Status        FreshnessStatus `json:"status"`
	Start         VersionToken    `json:"start"`
	Boundary      *VersionToken   `json:"boundary,omitempty"`
	MaxAgeSeconds int             `json:"max_age_seconds"`
}
type ReplayResult struct {
	Status        ReplayStatus `json:"status"`
	RequestID     string       `json:"request_id"`
	ReplayKey     string       `json:"replay_key"`
	ResultVersion string       `json:"result_version"`
	InputDigest   string       `json:"input_digest"`
	OutputDigest  string       `json:"output_digest"`
}
type EvidenceItem struct {
	Kind       string       `json:"kind"`
	Ref        string       `json:"ref"`
	Digest     string       `json:"digest"`
	ObservedAt time.Time    `json:"observed_at"`
	Scope      string       `json:"scope"`
	Token      VersionToken `json:"token"`
}
type AuditOutput struct {
	ContractVersion      string             `json:"contract_version"`
	CandidateID          string             `json:"candidate_id"`
	Verdict              Verdict            `json:"verdict"`
	ReasonCodes          []ReasonCode       `json:"reason_codes"`
	Coverage             Coverage           `json:"coverage"`
	CellResults          []CellResult       `json:"cell_results"`
	UnclassifiedSurface  []string           `json:"unclassified_surface"`
	SemanticConflicts    []SemanticConflict `json:"semantic_conflicts"`
	MutationAudit        MutationAudit      `json:"mutation_audit"`
	Freshness            FreshnessResult    `json:"freshness"`
	Replay               ReplayResult       `json:"replay"`
	Evidence             []EvidenceItem     `json:"evidence"`
	SideEffects          []string           `json:"side_effects"`
	PublicationStatus    PublicationStatus  `json:"publication_status"`
	StepsVersion         int                `json:"steps_version"`
	StepHistoryAvailable bool               `json:"step_history_available"`
}
