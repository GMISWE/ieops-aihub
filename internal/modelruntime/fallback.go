package modelruntime

import (
	"errors"
	"fmt"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/workflow"
)

// AttemptIdentity is the per-attempt identity a fallback chain is bounded to:
// the same tuple workflow.ExpectedInvocation pins (work item, flow version,
// step, step attempt, epoch). Every record the machine keeps carries it, and
// the bound ("once per candidate") is scoped to exactly this identity — a NEW
// step attempt (an authorized repair retry, a new epoch) is a new chain
// opened by the controller, not a reroll of this one.
type AttemptIdentity struct {
	WorkItemID    string
	FlowVersion   int
	StepID        string
	StepAttemptID string
	Epoch         int
}

func (id AttemptIdentity) String() string {
	return fmt.Sprintf("%s/v%d/%s/%s@%d", id.WorkItemID, id.FlowVersion, id.StepID, id.StepAttemptID, id.Epoch)
}

// Valid mirrors the shape rules workflow.Decide enforces on expected
// invocations, so a chain can never be keyed on an identity the result
// validator would later reject.
func (id AttemptIdentity) Valid() error {
	switch {
	case id.WorkItemID == "" || strings.ContainsAny(id.WorkItemID, " \t\r\n"):
		return errors.New("work item id is empty or has whitespace")
	case id.FlowVersion <= 0:
		return errors.New("flow version must be positive")
	case id.StepID == "" || strings.ContainsAny(id.StepID, " \t\r\n"):
		return errors.New("step id is empty or has whitespace")
	case id.StepAttemptID == "" || strings.ContainsAny(id.StepAttemptID, " \t\r\n"):
		return errors.New("step attempt id is empty or has whitespace")
	case id.Epoch <= 0:
		return errors.New("epoch must be positive")
	}
	return nil
}

// CandidateState is what happened to one candidate in this chain.
type CandidateState string

const (
	// StateUnused: never offered.
	StateUnused CandidateState = "unused"
	// StateUnavailable: refused before start (preflight refusal or a
	// pre-start spawn failure). No side effects; the chain advances.
	StateUnavailable CandidateState = "unavailable_before_start"
	// StateStarted: the process started and its outcome is not yet recorded.
	// Side effects are possible from here.
	StateStarted CandidateState = "started"
	// StateReconcileRequired: started, then an infrastructure failure. The
	// chain will not offer the next candidate until Reconciled records the
	// stop and the inspection of the retained side effects.
	StateReconcileRequired CandidateState = "reconcile_required"
	// StateReconciled: the failed candidate's process tree was stopped and
	// its side effects inspected and recorded. The chain may advance.
	StateReconciled CandidateState = "reconciled_after_start_failure"
	// StateNoFallback: a review/test FAIL or a non-infrastructure failure —
	// the chain never advances past it.
	StateNoFallback CandidateState = "no_fallback"
	// StateSucceeded: completed successfully; the chain is finished.
	StateSucceeded CandidateState = "succeeded"
)

// AttemptRecord is the durable story of one candidate in the chain, for the
// controller to persist with the step (Batch 2 owns that wiring).
type AttemptRecord struct {
	Identity  AttemptIdentity
	Index     int
	Candidate workflow.ModelCandidate
	State     CandidateState
	// Reason carries the refusal detail, the failure classification and the
	// reconcile evidence, depending on State.
	Reason string
}

// Selection is the machine's offer of one candidate. Index is the candidate's
// position in the ordered list; the Mark* methods take it back so a record
// can never be attached to the wrong candidate.
type Selection struct {
	Index     int
	Candidate workflow.ModelCandidate
}

// Errors the chain refuses with. Typed values, not prose, because the
// controller branches on them.
var (
	// ErrExhausted: every candidate was used; there is no next one.
	ErrExhausted = errors.New("modelruntime: every candidate has been used; no fallback remains")
	// ErrReconcileRequired: a started candidate failed on infrastructure; the
	// old process tree must be stopped and its side effects inspected and
	// recorded (Reconciled) before any next candidate may start.
	ErrReconcileRequired = errors.New("modelruntime: a started candidate failed; stop the old process tree and reconcile its side effects before the next candidate")
	// ErrNoFallback: a review/test FAIL (or non-infrastructure failure)
	// closed the chain; switching models on a FAIL is rerolling the verdict.
	ErrNoFallback = errors.New("modelruntime: candidate ended in a FAIL that never falls back (review/test FAIL); switching models on it would be a reroll")
	// ErrDone: the step succeeded; the chain is finished.
	ErrDone = errors.New("modelruntime: a candidate already succeeded; the chain is finished")
	// ErrSelectionClosed: the selection was already resolved; each candidate
	// resolves exactly once per chain.
	ErrSelectionClosed = errors.New("modelruntime: selection already resolved; each candidate is used at most once per attempt")
)

// Fallback is the ordered, bounded fallback state machine for one step
// attempt. Construction pins the ordered candidate list and the grant; every
// method is then a pure state transition with the invariants the spec fixed:
//
//   - ORDER: candidates are offered strictly in list order, each at most
//     once — the bound is per attempt identity, and a resolved candidate is
//     never offered again;
//   - CLASSIFICATION: unavailability is decided BEFORE start
//     (StateUnavailable), or, after a start, split into infrastructure
//     failure (StateReconcileRequired → Reconciled → advance) versus FAIL
//     (StateNoFallback: never falls back);
//   - NO REROLL: a review/test FAIL or a successful candidate closes the
//     chain permanently; nothing reopens it.
//
// The machine is deliberately not goroutine-safe: one step attempt has one
// controller, and serializing the caller is cheaper than a lock whose only
// job would be to serialize the caller.
type Fallback struct {
	identity   AttemptIdentity
	candidates []workflow.ModelCandidate
	grant      workflow.StepGrant
	states     []CandidateState
	reasons    []string
}

// NewFallback builds the chain for one attempt identity. The candidate list
// must be non-empty and duplicate-free (workflow.Validate enforces the same
// shape at authoring; this re-checks so the machine cannot be handed a
// doctored list), and the grant must be the controller's explicit one —
// every candidate of the chain dispatches under the same grant, which is how
// fallback can never widen read_only.
func NewFallback(identity AttemptIdentity, candidates []workflow.ModelCandidate, grant workflow.StepGrant) (*Fallback, error) {
	if err := identity.Valid(); err != nil {
		return nil, fmt.Errorf("modelruntime: fallback chain needs a valid attempt identity: %w", err)
	}
	if len(candidates) == 0 {
		return nil, errors.New("modelruntime: fallback chain needs at least one ordered candidate")
	}
	seen := make(map[string]bool, len(candidates))
	for i, c := range candidates {
		if c.Harness == "" || c.Model == "" || c.Effort == "" {
			return nil, fmt.Errorf("modelruntime: candidate %d is incomplete (%+v); a candidate is an exact {harness, model, effort} triple", i, c)
		}
		key := c.Harness + "\x00" + c.Model + "\x00" + c.Effort
		if seen[key] {
			return nil, fmt.Errorf("modelruntime: candidate %d duplicates an earlier entry (%s/%s@%s); order is fallback order, a repeat is an authoring slip",
				i, c.Harness, c.Model, c.Effort)
		}
		seen[key] = true
	}
	switch grant.Authority {
	case workflow.AuthorityReadOnly, workflow.AuthorityWrite:
	default:
		return nil, fmt.Errorf("modelruntime: grant authority %q must be %q or %q", grant.Authority, workflow.AuthorityReadOnly, workflow.AuthorityWrite)
	}
	switch grant.ProducerIsolation {
	case workflow.IsolationShared, workflow.IsolationRequired:
	default:
		return nil, fmt.Errorf("modelruntime: grant isolation %q must be %q or %q", grant.ProducerIsolation, workflow.IsolationShared, workflow.IsolationRequired)
	}
	states := make([]CandidateState, len(candidates))
	for i := range states {
		states[i] = StateUnused
	}
	return &Fallback{
		identity:   identity,
		candidates: append([]workflow.ModelCandidate(nil), candidates...),
		grant:      grant,
		states:     states,
		reasons:    make([]string, len(candidates)),
	}, nil
}

// Identity returns the chain's attempt identity (for record keeping).
func (f *Fallback) Identity() AttemptIdentity { return f.identity }

// Grant returns the controller grant every candidate dispatches under.
func (f *Fallback) Grant() workflow.StepGrant { return f.grant }

// Next offers the next unresolved candidate in list order. It refuses with
// ErrExhausted (all resolved, none runnable), ErrReconcileRequired (a started
// candidate failed and has not been reconciled), ErrNoFallback (a FAIL
// closed the chain), ErrDone (a candidate succeeded) or an explicit error
// naming a still-started candidate whose outcome is pending. Idempotent
// between marks: calling Next twice without resolving the offered selection
// returns the SAME selection, so a controller that crashed mid-decision can
// re-derive it.
func (f *Fallback) Next() (Selection, error) {
	for i := 0; i < len(f.candidates); i++ {
		switch f.states[i] {
		case StateUnused:
			return Selection{Index: i, Candidate: f.candidates[i]}, nil
		case StateUnavailable, StateReconciled:
			continue
		case StateStarted:
			// A started candidate with no recorded outcome: the process may
			// still be running. The controller must resolve its outcome
			// before another candidate exists.
			return Selection{}, fmt.Errorf(
				"modelruntime: candidate %d (%s/%s@%s) is still started; resolve its outcome before offering another",
				i, f.candidates[i].Harness, f.candidates[i].Model, f.candidates[i].Effort)
		case StateReconcileRequired:
			return Selection{}, ErrReconcileRequired
		case StateNoFallback:
			return Selection{}, ErrNoFallback
		case StateSucceeded:
			return Selection{}, ErrDone
		default:
			return Selection{}, fmt.Errorf("modelruntime: candidate %d in unknown state %q", i, f.states[i])
		}
	}
	return Selection{}, ErrExhausted
}

// mark applies a transition to one selection after checking it belongs to
// this chain and is in the required source state.
func (f *Fallback) mark(sel Selection, from, to CandidateState, reason string) error {
	if sel.Index < 0 || sel.Index >= len(f.candidates) {
		return fmt.Errorf("modelruntime: selection index %d is outside the candidate list", sel.Index)
	}
	if f.candidates[sel.Index] != sel.Candidate {
		return fmt.Errorf("modelruntime: selection %d does not match its candidate; records cannot be reattached", sel.Index)
	}
	if f.states[sel.Index] != from {
		if f.states[sel.Index] != StateUnused && from != StateUnused {
			return fmt.Errorf("%w: candidate %d is %q, wanted %q", ErrSelectionClosed, sel.Index, f.states[sel.Index], from)
		}
		return fmt.Errorf("modelruntime: candidate %d is %q, wanted %q", sel.Index, f.states[sel.Index], from)
	}
	f.states[sel.Index] = to
	f.reasons[sel.Index] = reason
	return nil
}

// MarkUnavailable records a PRE-START unavailability (a Preflight refusal, or
// a failure to spawn the process). Nothing ran, so the chain may advance
// immediately. The reason is recorded verbatim for the dispatch record.
func (f *Fallback) MarkUnavailable(sel Selection, reason string) error {
	return f.mark(sel, StateUnused, StateUnavailable, reason)
}

// MarkStarted records that the candidate's process started. From here side
// effects are possible, and the chain will not silently advance on failure.
func (f *Fallback) MarkStarted(sel Selection) error {
	return f.mark(sel, StateUnused, StateStarted, "")
}

// MarkInfrastructureFailure records a started candidate failing on
// model/channel/transport infrastructure (provider error, lost transport).
// The chain enters reconcile-required: the controller must stop the old
// process tree, inspect and record the retained side effects, then call
// Reconciled before the next candidate may start (spec D9: "if execution
// started, stop/inspect side effects and record new invocation before
// retry").
func (f *Fallback) MarkInfrastructureFailure(sel Selection, detail string) error {
	return f.mark(sel, StateStarted, StateReconcileRequired, detail)
}

// Reconciled clears a reconcile requirement with the evidence of what was
// stopped and inspected. Empty evidence is refused: "inspected and recorded"
// is the whole point of the gate, and an empty string records nothing. After
// a reconciliation the chain may offer the next candidate.
func (f *Fallback) Reconciled(sel Selection, evidence string) error {
	if strings.TrimSpace(evidence) == "" {
		return errors.New("modelruntime: reconciling a failed candidate requires the evidence of what was stopped and inspected; an empty record is not a reconciliation")
	}
	if sel.Index < 0 || sel.Index >= len(f.candidates) || f.candidates[sel.Index] != sel.Candidate {
		return errors.New("modelruntime: selection does not match a candidate of this chain")
	}
	if f.states[sel.Index] != StateReconcileRequired {
		return fmt.Errorf("modelruntime: candidate %d is %q, not reconcile_required", sel.Index, f.states[sel.Index])
	}
	f.reasons[sel.Index] = f.reasons[sel.Index] + " | reconciled: " + evidence
	f.states[sel.Index] = StateReconciled
	return nil
}

// MarkNoFallback records an outcome that must NEVER fall back: a review FAIL,
// a test FAIL, or any non-infrastructure failure (incomplete/blocked/invalid
// result). The chain closes permanently — switching models on a FAIL is
// rerolling the verdict until one passes, the review shopping the design
// forbids. Recovery is the controller's authorized repair episode, a NEW
// chain under a NEW step attempt identity.
func (f *Fallback) MarkNoFallback(sel Selection, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("modelruntime: a no-fallback outcome needs its reason recorded")
	}
	return f.mark(sel, StateStarted, StateNoFallback, reason)
}

// MarkSucceeded records the successful completion of the step attempt on
// this candidate. The chain is finished.
func (f *Fallback) MarkSucceeded(sel Selection) error {
	return f.mark(sel, StateStarted, StateSucceeded, "")
}

// Records returns the resolved per-candidate history in candidate order, for
// persisting with the step attempt.
func (f *Fallback) Records() []AttemptRecord {
	out := make([]AttemptRecord, 0, len(f.candidates))
	for i, c := range f.candidates {
		if f.states[i] == StateUnused {
			continue
		}
		out = append(out, AttemptRecord{
			Identity:  f.identity,
			Index:     i,
			Candidate: c,
			State:     f.states[i],
			Reason:    f.reasons[i],
		})
	}
	return out
}

// Outcome classifies a started candidate's StepResult into the transition the
// chain requires. Pure: the mapping is the policy, stated once.
type Outcome int

const (
	// OutcomeSucceeded: completed without a FAIL verdict.
	OutcomeSucceeded Outcome = iota
	// OutcomeInfrastructure: provider/model/channel infrastructure failed
	// (provider_error). Reconcile then advance.
	OutcomeInfrastructure
	// OutcomeNoFallback: review/test FAIL, or a non-infrastructure failure
	// (incomplete, blocked, invalid result). The chain closes.
	OutcomeNoFallback
)

// ClassifyOutcome maps a worker StepResult to the chain transition. The rule
// the spec fixed, stated once:
//
//   - provider_error is the ONLY fallback-eligible failure, and only after
//     stop+reconcile;
//   - a review verdict of fail (the review/test FAIL case) never falls back;
//   - incomplete/blocked/invalid_result are step outcomes, not
//     infrastructure: they close the chain with the reason preserved for the
//     controller's pause path.
func ClassifyOutcome(result workflow.StepResult) Outcome {
	if result.ReviewVerdict == workflow.ReviewFail {
		return OutcomeNoFallback
	}
	switch result.Status {
	case workflow.StatusProviderError:
		return OutcomeInfrastructure
	case workflow.StatusCompleted:
		return OutcomeSucceeded
	default: // incomplete, blocked, invalid_result
		return OutcomeNoFallback
	}
}

// Apply performs the transition ClassifyOutcome selects for a started
// candidate. It is the one-call form of the Mark* sequence, so the
// controller cannot get the order wrong.
func (f *Fallback) Apply(sel Selection, result workflow.StepResult) error {
	switch ClassifyOutcome(result) {
	case OutcomeSucceeded:
		return f.MarkSucceeded(sel)
	case OutcomeInfrastructure:
		return f.MarkInfrastructureFailure(sel, fmt.Sprintf("provider_error (status %q): infrastructure failure after start", result.Status))
	default:
		reason := fmt.Sprintf("status %q", result.Status)
		if result.ReviewVerdict == workflow.ReviewFail {
			reason = fmt.Sprintf("review/test FAIL (verdict %q): switching models on a FAIL is a reroll, never a fallback", result.ReviewVerdict)
		}
		return f.MarkNoFallback(sel, reason)
	}
}
