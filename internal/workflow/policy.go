package workflow

import (
	"errors"
	"fmt"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
)

// Decide applies the same pure policy in both modes. All policy inputs must be
// established by the controller; matching an envelope does not authenticate it.
func Decide(flow *ValidatedFlow, results []StepResult, input PolicyInput) (Decision, error) {
	if flow == nil || len(flow.steps) == 0 {
		return "", errors.New("decision requires a validated flow")
	}
	if !input.RequiresHumanSession {
		for _, step := range flow.steps {
			if step.Contract.Runtime.Interactive {
				return "", fmt.Errorf("interactive-only step %q requires a human session", step.Step.ID)
			}
		}
	}
	if len(input.Expected) != len(results) {
		return "", errors.New("every result requires one expected invocation")
	}
	byStep := make(map[string][]StepResult)
	expectedIDs := make(map[string]bool)
	var wi string
	for i, r := range results {
		e := input.Expected[i]
		if !validIdentifier(e.WorkItemID) || e.FlowVersion != flow.flow.Version || !validIdentifier(e.StepID) || !validIdentifier(e.StepAttemptID) || e.Epoch <= 0 || !validIdentifier(e.ProducerID) {
			return "", fmt.Errorf("expected invocation %d incomplete or wrong workflow", i)
		}
		if wi == "" {
			wi = e.WorkItemID
		} else if wi != e.WorkItemID {
			return "", errors.New("expected invocations belong to different work items")
		}
		if r.WorkItemID != e.WorkItemID || r.FlowVersion != e.FlowVersion || r.StepID != e.StepID || r.StepAttemptID != e.StepAttemptID || r.Epoch != e.Epoch || r.ProducerID != e.ProducerID {
			return "", fmt.Errorf("result %d does not match expected invocation", i)
		}
		if expectedIDs[e.StepAttemptID] {
			return "", errors.New("duplicate expected step attempt")
		}
		expectedIDs[e.StepAttemptID] = true
		var step *ValidatedStep
		for j := range flow.steps {
			if flow.steps[j].Step.ID == e.StepID {
				step = &flow.steps[j]
				break
			}
		}
		if step == nil {
			return "", fmt.Errorf("result %d names step outside current workflow", i)
		}
		if err := validateResult(r, *step); err != nil {
			return "", fmt.Errorf("result %d: %w", i, err)
		}
		byStep[r.StepID] = append(byStep[r.StepID], r)
	}
	if len(input.RepairEpisodes) > 8 {
		return "", errors.New("repair episodes exceed bounded limit")
	}
	if err := validateRepairEpisodes(flow, input.RepairEpisodes, results); err != nil {
		return "", err
	}
	// Ordinary invocations follow definition order. Only controller-authorized
	// repair invocations may reopen an earlier step; otherwise latest-result
	// aggregation could hide gates executed before their producing writes.
	lastOrdinary := -1
	for _, r := range results {
		if episodeContainsResult(input.RepairEpisodes, r) ||
			(input.Repair != nil && input.Repair.Authorized && input.Repair.Retry == expectedFor(r)) {
			continue
		}
		position := stepPosition(flow, r.StepID)
		if position < lastOrdinary {
			return "", errors.New("ordinary result history is out of workflow order")
		}
		lastOrdinary = position
	}
	episodeMode := len(input.RepairEpisodes) > 0
	latest := make(map[string]StepResult)
	var failure *StepResult
	var retry *StepResult
	for _, step := range flow.steps {
		history := byStep[step.Step.ID]
		if len(history) > 1 {
			for _, candidate := range history[1:] {
				if episodeContainsResult(input.RepairEpisodes, candidate) {
					continue
				}
				if episodeMode || failure != nil {
					return "", errors.New("result retry is not bound to an authorized repair episode")
				}
				if history[0].Status == StatusCompleted && history[0].ReviewVerdict != ReviewFail || candidate.Epoch < history[0].Epoch || candidate.StepAttemptID == history[0].StepAttemptID {
					return "", errors.New("invalid repair sequence")
				}
				if failure != nil {
					return "", errors.New("multiple failures require separate authorizations")
				}
				failedCopy := history[0]
				failure = &failedCopy
				retryCopy := candidate
				retry = &retryCopy
			}
		}
		if len(history) != 0 {
			latest[step.Step.ID] = history[len(history)-1]
		}
	}
	if retry != nil && !episodeMode {
		auth := input.Repair
		if auth == nil || !auth.Authorized || strings.TrimSpace(auth.Reason) == "" || auth.FailedStepAttemptID != failure.StepAttemptID || auth.Retry != expectedFor(*retry) {
			return "", errors.New("repair requires controller authorization for exact failed attempt and retry")
		}
		if retry.Status != StatusCompleted || retry.ReviewVerdict == ReviewFail {
			return DecisionPause, nil
		}
		if !hasDistinctFreshGates(flow, latest, retry.Epoch, results, retry.StepAttemptID) {
			return DecisionWait, nil
		}
	} else if input.Repair != nil && !episodeMode {
		return "", errors.New("repair authorization without retry")
	}
	if err := validateProducerIndependence(flow, byStep); err != nil {
		return "", err
	}
	if err := validateShippingEventOrder(flow, results); err != nil {
		return "", err
	}
	for i, step := range flow.steps {
		r, ok := latest[step.Step.ID]
		if !ok {
			for _, later := range flow.steps[i+1:] {
				if _, exists := latest[later.Step.ID]; exists {
					return "", fmt.Errorf("missing step %q before later result", step.Step.ID)
				}
			}
			return DecisionWait, nil
		}
		if r.Status != StatusCompleted || r.ReviewVerdict == ReviewFail {
			return DecisionPause, nil
		}
		if hasCapability(step.Contract, skillregistry.CapReview) && r.ReviewVerdict == "" {
			return "", fmt.Errorf("review %q missing verdict", r.StepID)
		}
		if input.RequiresHumanSession && step.Step.RHS != nil && *step.Step.RHS {
			if !hasApproval(input.Approvals, r) {
				for _, later := range flow.steps[i+1:] {
					if _, exists := latest[later.Step.ID]; exists {
						return "", fmt.Errorf("missing human approval for step %q before later result", r.StepID)
					}
				}
				return DecisionWait, nil
			}
		}
	}
	return DecisionAdvance, nil
}

func validateRepairEpisodes(flow *ValidatedFlow, episodes []RepairEpisode, results []StepResult) error {
	positions := make(map[string]int, len(results))
	values := make(map[string]StepResult, len(results))
	for i, r := range results {
		positions[r.StepAttemptID] = i
		values[r.StepAttemptID] = r
	}
	for _, e := range episodes {
		if !e.Authorized || strings.TrimSpace(e.Reason) == "" {
			return errors.New("repair episode is not authorized")
		}
		failed, ok := values[e.FailedReviewStepAttemptID]
		if !ok || failed.ReviewVerdict != ReviewFail {
			return errors.New("repair episode must bind an exact failed review")
		}
		repair, rok := values[e.RepairedProducer.StepAttemptID]
		verify, vok := values[e.Verification.StepAttemptID]
		review, pok := values[e.Review.StepAttemptID]
		if !rok || !vok || !pok || expectedFor(repair) != e.RepairedProducer || expectedFor(verify) != e.Verification || expectedFor(review) != e.Review {
			return errors.New("repair episode invocation binding does not match results")
		}
		if repair.Status != StatusCompleted || verify.Status != StatusCompleted || review.Status != StatusCompleted {
			return errors.New("repair episode requires completed repair, verification, and review")
		}
		failedStep := findValidatedStep(flow, failed.StepID)
		repairStep := findValidatedStep(flow, repair.StepID)
		verifyStep := findValidatedStep(flow, verify.StepID)
		reviewStep := findValidatedStep(flow, review.StepID)
		if failedStep == nil || repairStep == nil || verifyStep == nil || reviewStep == nil ||
			!hasCapability(failedStep.Contract, skillregistry.CapReview) || flow.grants[failed.StepID].Authority != AuthorityReadOnly || flow.grants[failed.StepID].ProducerIsolation != IsolationRequired ||
			repairStep.Step.ID == "" ||
			repairStep.Step.ID == reviewStep.Step.ID ||
			repairStep.Step.ID == verifyStep.Step.ID ||
			repairStep.Step.ID == failedStep.Step.ID ||
			flow.grants[repair.StepID].Authority != AuthorityWrite || hasCapability(repairStep.Contract, skillregistry.CapShipping) || stepPosition(flow, repair.StepID) >= stepPosition(flow, failed.StepID) ||
			flow.grants[verify.StepID].Authority != AuthorityReadOnly || flow.grants[verify.StepID].ProducerIsolation != IsolationRequired || !hasCapability(verifyStep.Contract, skillregistry.CapVerification) ||
			flow.grants[review.StepID].Authority != AuthorityReadOnly || flow.grants[review.StepID].ProducerIsolation != IsolationRequired || !hasCapability(reviewStep.Contract, skillregistry.CapReview) {
			return errors.New("repair episode invocation has invalid semantic role")
		}
		affected := false
		for _, input := range failedStep.Step.Inputs {
			if input.StepID == repair.StepID {
				affected = true
				break
			}
		}
		if !affected {
			return errors.New("repair episode producer does not affect failed review")
		}
		if positions[e.FailedReviewStepAttemptID] >= positions[e.RepairedProducer.StepAttemptID] || positions[e.RepairedProducer.StepAttemptID] >= positions[e.Verification.StepAttemptID] || positions[e.Verification.StepAttemptID] >= positions[e.Review.StepAttemptID] {
			return errors.New("repair episode invocations must be event ordered")
		}
		if review.ReviewVerdict == ReviewFail {
			continue
		}
	}
	return nil
}

func findValidatedStep(flow *ValidatedFlow, id string) *ValidatedStep {
	for i := range flow.steps {
		if flow.steps[i].Step.ID == id {
			return &flow.steps[i]
		}
	}
	return nil
}

func stepPosition(flow *ValidatedFlow, id string) int {
	for i := range flow.steps {
		if flow.steps[i].Step.ID == id {
			return i
		}
	}
	return len(flow.steps)
}
func validateShippingEventOrder(flow *ValidatedFlow, results []StepResult) error {
	for shipIndex, ship := range results {
		step := findValidatedStep(flow, ship.StepID)
		if step == nil || !hasCapability(step.Contract, skillregistry.CapShipping) {
			continue
		}
		lastWrite := -1
		for i := shipIndex - 1; i >= 0; i-- {
			candidate := findValidatedStep(flow, results[i].StepID)
			if candidate != nil && flow.grants[candidate.Step.ID].Authority == AuthorityWrite && !hasCapability(candidate.Contract, skillregistry.CapShipping) {
				lastWrite = i
				break
			}
		}
		if lastWrite < 0 {
			return fmt.Errorf("shipping result %q has no preceding implementation write", ship.StepID)
		}
		var review, verify string
		for i := lastWrite + 1; i < shipIndex; i++ {
			candidate := findValidatedStep(flow, results[i].StepID)
			if candidate == nil || results[i].Status != StatusCompleted || flow.grants[candidate.Step.ID].Authority != AuthorityReadOnly || flow.grants[candidate.Step.ID].ProducerIsolation != IsolationRequired {
				continue
			}
			if hasCapability(candidate.Contract, skillregistry.CapReview) && results[i].ReviewVerdict != ReviewFail {
				review = results[i].StepAttemptID
			}
			if hasCapability(candidate.Contract, skillregistry.CapVerification) {
				verify = results[i].StepAttemptID
			}
		}
		if review == "" || verify == "" || review == verify {
			return fmt.Errorf("shipping result %q is not preceded by distinct review and verification gates after the latest write", ship.StepID)
		}
	}
	return nil
}
func episodeContainsResult(episodes []RepairEpisode, result StepResult) bool {
	for _, e := range episodes {
		if e.RepairedProducer.StepAttemptID == result.StepAttemptID || e.Verification.StepAttemptID == result.StepAttemptID || e.Review.StepAttemptID == result.StepAttemptID {
			return true
		}
	}
	return false
}
func expectedFor(r StepResult) ExpectedInvocation {
	return ExpectedInvocation{r.WorkItemID, r.FlowVersion, r.StepID, r.StepAttemptID, r.Epoch, r.ProducerID}
}

func hasApproval(approvals []HumanApproval, r StepResult) bool {
	for _, a := range approvals {
		if a.WorkItemID == r.WorkItemID && a.FlowVersion == r.FlowVersion && a.StepID == r.StepID && a.Artifact == r.Artifact && validIdentifier(a.ActorID) && a.Decision == ApprovalGranted {
			return true
		}
	}
	return false
}

func validateResult(r StepResult, step ValidatedStep) error {
	switch r.Status {
	case StatusCompleted, StatusIncomplete, StatusBlocked, StatusProviderError, StatusInvalidResult:
	default:
		return fmt.Errorf("unknown status %q", r.Status)
	}
	if !validIdentifier(r.Artifact.ID) || r.Artifact.Version <= 0 || !sha256RE.MatchString(r.Artifact.Hash) {
		return errors.New("artifact must carry id, positive version and sha256 hash")
	}
	for i, e := range r.Evidence {
		if !validIdentifier(e.Kind) || strings.TrimSpace(e.Ref) == "" || !sha256RE.MatchString(e.Hash) {
			return fmt.Errorf("invalid evidence %d", i)
		}
	}
	review := hasCapability(step.Contract, skillregistry.CapReview)
	if review {
		switch r.ReviewVerdict {
		case ReviewPass, ReviewWarn, ReviewFail:
		case "":
			// A provider infrastructure error is distinct from a review FAIL:
			// the invocation died before the review ran, so it may carry no
			// verdict. A COMPLETED review must always carry one; a malformed
			// verdict is refused whatever the status. Whether a provider_error
			// may be retried — and that a FAIL verdict never rerolls through
			// the retry lineage — is the repair-authorization gate's call, not
			// this shape check's.
			if r.Status != StatusProviderError {
				return errors.New("review missing valid verdict")
			}
		default:
			return errors.New("review missing valid verdict")
		}
	} else if r.ReviewVerdict != "" {
		return errors.New("non-review result carries review verdict")
	}
	if (review || hasCapability(step.Contract, skillregistry.CapVerification)) && r.Status == StatusCompleted && len(r.Evidence) == 0 {
		return errors.New("completed gate requires evidence")
	}
	return nil
}

func hasDistinctFreshGates(flow *ValidatedFlow, latest map[string]StepResult, epoch int, results []StepResult, retryID string) bool {
	retryIndex := -1
	for i, result := range results {
		if result.StepAttemptID == retryID {
			retryIndex = i
			break
		}
	}

	review, verify := false, false
	for _, s := range flow.steps {
		r, ok := latest[s.Step.ID]
		if !ok || r.Epoch < epoch || r.Status != StatusCompleted || flow.grants[s.Step.ID].ProducerIsolation != IsolationRequired {
			continue
		}
		fresh := false
		for i := retryIndex + 1; i < len(results); i++ {
			if results[i].StepAttemptID == r.StepAttemptID {
				fresh = true
				break
			}
		}
		if !fresh {
			continue
		}
		if hasCapability(s.Contract, skillregistry.CapReview) && r.ReviewVerdict != ReviewFail {
			review = true
		}
		if hasCapability(s.Contract, skillregistry.CapVerification) {
			verify = true
		}
	}
	return review && verify
}

func validateProducerIndependence(flow *ValidatedFlow, history map[string][]StepResult) error {
	for i, gate := range flow.steps {
		if flow.grants[gate.Step.ID].ProducerIsolation != IsolationRequired || (!hasCapability(gate.Contract, skillregistry.CapReview) && !hasCapability(gate.Contract, skillregistry.CapVerification)) {
			continue
		}
		for _, r := range history[gate.Step.ID] {
			for _, producer := range flow.steps[:i] {
				if flow.grants[producer.Step.ID].Authority != AuthorityWrite || hasCapability(producer.Contract, skillregistry.CapShipping) {
					continue
				}
				for _, p := range history[producer.Step.ID] {
					if p.ProducerID == r.ProducerID {
						return fmt.Errorf("independent gate %q shares producer with %q", gate.Step.ID, producer.Step.ID)
					}
				}
			}
		}
	}
	return nil
}
