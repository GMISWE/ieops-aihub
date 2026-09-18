package workflow

import (
	"strings"
	"testing"
)

const testHash = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func validatedSafeFlow(t *testing.T) *ValidatedFlow {
	t.Helper()
	f, b := safeFlow()
	v, e := Validate(f, b, safeContext(f))
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func resultFor(step string, epoch int, producer string) StepResult {
	r := StepResult{Status: StatusCompleted, WorkItemID: "wi_1", FlowVersion: 5, StepID: step, StepAttemptID: step + "_attempt_" + string(rune('0'+epoch)), Epoch: epoch, ProducerID: producer, Artifact: ArtifactRef{"artifact_" + step, epoch, testHash}}
	if step == "review" {
		r.ReviewVerdict = ReviewPass
	}
	if step == "review" || step == "verify" {
		r.Evidence = []Evidence{{"report", "artifact://" + step, testHash}}
	}
	return r
}
func goodResults() []StepResult {
	return []StepResult{resultFor("write", 1, "writer"), resultFor("review", 1, "reviewer"), resultFor("verify", 1, "verifier"), resultFor("ship", 1, "shipper")}
}
func policy(rs []StepResult) PolicyInput {
	p := PolicyInput{}
	for _, r := range rs {
		p.Expected = append(p.Expected, expectedFor(r))
	}
	return p
}
func TestResultBindingAndReviewVerdict(t *testing.T) {
	flow := validatedSafeFlow(t)
	rs := goodResults()
	p := policy(rs)
	if d, e := Decide(flow, rs, p); e != nil || d != DecisionAdvance {
		t.Fatalf("success %s %v", d, e)
	}
	rs[1].ReviewVerdict = ReviewWarn
	if d, e := Decide(flow, rs, p); e != nil || d != DecisionAdvance {
		t.Fatalf("warn %s %v", d, e)
	}
	rs[1].ReviewVerdict = ""
	if _, e := Decide(flow, rs, p); e == nil {
		t.Fatal("missing review verdict accepted")
	}
	rs = goodResults()
	rs = append(rs[:1], rs[2:]...)
	if _, e := Decide(flow, rs, policy(rs)); e == nil {
		t.Fatal("missing review accepted")
	}
	for _, change := range []struct {
		name string
		edit func(*StepResult)
	}{
		{"epoch", func(r *StepResult) { r.Epoch++ }}, {"step", func(r *StepResult) { r.StepID = "ship" }}, {"workflow", func(r *StepResult) { r.FlowVersion++ }}, {"producer", func(r *StepResult) { r.ProducerID = "intruder" }}, {"attempt", func(r *StepResult) { r.StepAttemptID = "stale" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			rs := goodResults()
			p := policy(rs)
			change.edit(&rs[0])
			if _, e := Decide(flow, rs, p); e == nil || !strings.Contains(e.Error(), "expected invocation") {
				t.Fatalf("accepted stale result: %v", e)
			}
		})
	}
}

// TestReviewProviderErrorVerdictSemantics holds the shape half of the
// provider_error / review-FAIL distinction (mem_FHuxXIXI retry-lineage
// regression): a provider infrastructure error may carry no verdict — the
// invocation died before the review ran — while a well-formed verdict on a
// provider_error result stays shape-legal (retry eligibility and the
// no-reroll rule for FAIL verdicts belong to the repair-authorization gate,
// not this validator). A COMPLETED review must still carry its verdict, and
// a malformed verdict is refused whatever the status.
func TestReviewProviderErrorVerdictSemantics(t *testing.T) {
	flow := validatedSafeFlow(t)
	providerErrorReview := func(verdict ReviewVerdict) []StepResult {
		r := resultFor("review", 1, "reviewer")
		r.Status = StatusProviderError
		r.ReviewVerdict = verdict
		r.Evidence = nil
		return []StepResult{resultFor("write", 1, "writer"), r}
	}
	// No verdict on a provider_error review: valid shape, no error (the
	// decision itself waits — the step has no completed result yet).
	if _, e := Decide(flow, providerErrorReview(""), policy(providerErrorReview(""))); e != nil {
		t.Fatalf("provider_error review without verdict rejected: %v", e)
	}
	// A well-formed verdict on a provider_error result is shape-legal.
	if _, e := Decide(flow, providerErrorReview(ReviewPass), policy(providerErrorReview(ReviewPass))); e != nil {
		t.Fatalf("provider_error review with well-formed verdict rejected: %v", e)
	}
	// A malformed verdict is refused whatever the status.
	bad := providerErrorReview("maybe")
	if _, e := Decide(flow, bad, policy(bad)); e == nil || !strings.Contains(e.Error(), "verdict") {
		t.Fatalf("malformed verdict on provider_error accepted: %v", e)
	}
	// A completed review still refuses to omit its verdict.
	completed := []StepResult{resultFor("write", 1, "writer"), resultFor("review", 1, "reviewer")}
	completed[1].ReviewVerdict = ""
	if _, e := Decide(flow, completed, policy(completed)); e == nil || !strings.Contains(e.Error(), "verdict") {
		t.Fatal("completed review without verdict accepted")
	}
}
func TestApprovalIsControllerInput(t *testing.T) {
	f, b := safeFlow()
	f.Steps[0].RHS = boolp(true)
	flow, e := Validate(f, b, safeContext(f))
	if e != nil {
		t.Fatal(e)
	}
	rs := goodResults()
	p := policy(rs)
	p.RequiresHumanSession = true
	if _, e := Decide(flow, rs, p); e == nil || !strings.Contains(e.Error(), "missing human approval") {
		t.Fatalf("later result bypassed approval: %v", e)
	}
	if d, e := Decide(flow, rs[:1], PolicyInput{RequiresHumanSession: true, Expected: p.Expected[:1]}); e != nil || d != DecisionWait {
		t.Fatalf("gated spec without approval %s %v", d, e)
	}
	p.Approvals = []HumanApproval{{WorkItemID: "wi_1", FlowVersion: 5, StepID: "write", Artifact: rs[0].Artifact, ActorID: "human_1", Decision: ApprovalGranted}}
	if d, e := Decide(flow, rs, p); e != nil || d != DecisionAdvance {
		t.Fatalf("approval %s %v", d, e)
	}
	p.Approvals[0].Artifact.Hash = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if _, e := Decide(flow, rs, p); e == nil {
		t.Fatalf("wrong artifact approval accepted")
	}
	p = policy(rs)
	if d, e := Decide(flow, rs, p); e != nil || d != DecisionAdvance {
		t.Fatalf("WI rhs=false must ignore step true: %s %v", d, e)
	}
}
func repairEpisode(failed, repair, verify, review StepResult) RepairEpisode {
	return RepairEpisode{
		FailedReviewStepAttemptID: failed.StepAttemptID,
		RepairedProducer:          expectedFor(repair),
		Verification:              expectedFor(verify),
		Review:                    expectedFor(review),
		Authorized:                true,
		Reason:                    "human-authorized bounded repair",
	}
}

func TestRepairEpisodeRejectsGateInvocationsInWrongSemanticRoles(t *testing.T) {
	flow := validatedSafeFlow(t)
	write := resultFor("write", 1, "writer")
	failedReview := resultFor("review", 1, "reviewer")
	failedReview.ReviewVerdict = ReviewFail
	verify := resultFor("verify", 1, "verifier")
	ship := resultFor("ship", 1, "shipper")
	passedReview := resultFor("review", 2, "fresh_reviewer")
	rs := []StepResult{write, failedReview, verify, ship, passedReview}
	p := policy(rs)
	p.RepairEpisodes = []RepairEpisode{repairEpisode(failedReview, verify, ship, passedReview)}
	if _, err := Decide(flow, rs, p); err == nil {
		t.Fatal("repair episode accepted verification as producer and shipping as verification")
	}
}

func TestShippingCannotPrecedeFreshRepairGates(t *testing.T) {
	flow := validatedSafeFlow(t)
	write := resultFor("write", 1, "writer")
	failedReview := resultFor("review", 1, "reviewer")
	failedReview.ReviewVerdict = ReviewFail
	repair := resultFor("write", 2, "repairer")
	verify := resultFor("verify", 2, "verifier")
	ship := resultFor("ship", 2, "shipper")
	passedReview := resultFor("review", 2, "fresh_reviewer")
	rs := []StepResult{write, failedReview, repair, verify, ship, passedReview}
	p := policy(rs)
	p.RepairEpisodes = []RepairEpisode{repairEpisode(failedReview, repair, verify, passedReview)}
	if _, err := Decide(flow, rs, p); err == nil {
		t.Fatal("shipping before fresh review was accepted")
	}
}
func TestReviewFailureRepairEpisodeCanRecoverAndShip(t *testing.T) {
	flow := validatedSafeFlow(t)
	write := resultFor("write", 1, "writer")
	failedReview := resultFor("review", 1, "reviewer")
	failedReview.ReviewVerdict = ReviewFail
	repair := resultFor("write", 2, "repairer")
	verify := resultFor("verify", 2, "verifier")
	passedReview := resultFor("review", 2, "fresh_reviewer")
	ship := resultFor("ship", 2, "shipper")
	rs := []StepResult{write, failedReview, repair, verify, passedReview, ship}
	p := policy(rs)
	p.RepairEpisodes = []RepairEpisode{repairEpisode(failedReview, repair, verify, passedReview)}
	if d, err := Decide(flow, rs, p); err != nil || d != DecisionAdvance {
		t.Fatalf("authorized review repair = %s, %v; want advance", d, err)
	}
}

func TestReviewFailureRepairRejectsUnauthorizedAndStaleGates(t *testing.T) {
	flow := validatedSafeFlow(t)
	write := resultFor("write", 1, "writer")
	failedReview := resultFor("review", 1, "reviewer")
	failedReview.ReviewVerdict = ReviewFail
	repair := resultFor("write", 2, "repairer")
	verify := resultFor("verify", 2, "verifier")
	passedReview := resultFor("review", 2, "fresh_reviewer")
	rs := []StepResult{write, failedReview, repair, verify, passedReview}
	if _, err := Decide(flow, rs, policy(rs)); err == nil {
		t.Fatal("unauthorized repair was accepted")
	}
	p := policy(rs)
	staleReview := passedReview
	staleReview.Epoch = 1
	p.RepairEpisodes = []RepairEpisode{repairEpisode(failedReview, repair, verify, staleReview)}
	if _, err := Decide(flow, rs, p); err == nil {
		t.Fatal("episode binding to an old-producer review was accepted")
	}
}

func TestRepeatedReviewFailurePausesAndRequiresNewEpisode(t *testing.T) {
	flow := validatedSafeFlow(t)
	write := resultFor("write", 1, "writer")
	failedReview := resultFor("review", 1, "reviewer")
	failedReview.ReviewVerdict = ReviewFail
	repair := resultFor("write", 2, "repairer")
	verify := resultFor("verify", 2, "verifier")
	failedAgain := resultFor("review", 2, "fresh_reviewer")
	failedAgain.ReviewVerdict = ReviewFail
	rs := []StepResult{write, failedReview, repair, verify, failedAgain}
	p := policy(rs)
	p.RepairEpisodes = []RepairEpisode{repairEpisode(failedReview, repair, verify, failedAgain)}
	if d, err := Decide(flow, rs, p); err != nil || d != DecisionPause {
		t.Fatalf("repeated review failure = %s, %v; want pause", d, err)
	}
	nextRepair := resultFor("write", 3, "second_repairer")
	rs = append(rs, nextRepair)
	p.Expected = policy(rs).Expected
	if _, err := Decide(flow, rs, p); err == nil {
		t.Fatal("second repair without a new authorization episode was accepted")
	}
}

func TestIndependentGateRejectsEveryContributingWriterProducer(t *testing.T) {
	flow := validatedSafeFlow(t)
	failed := resultFor("write", 1, "first_writer")
	failed.Status = StatusBlocked
	retry := resultFor("write", 2, "repairer")
	review := resultFor("review", 2, "first_writer")
	verify := resultFor("verify", 2, "verifier")
	ship := resultFor("ship", 2, "shipper")
	rs := []StepResult{failed, retry, review, verify, ship}
	p := policy(rs)
	p.Repair = &RepairAuthorization{FailedStepAttemptID: failed.StepAttemptID, Retry: expectedFor(retry), Authorized: true, Reason: "repair"}
	if _, err := Decide(flow, rs, p); err == nil || !strings.Contains(err.Error(), "shares producer") {
		t.Fatalf("gate reused failed writer producer: %v", err)
	}
}

func TestRepairRequiresControllerAuthorizationAndFreshGates(t *testing.T) {
	flow := validatedSafeFlow(t)
	failed := resultFor("write", 1, "writer")
	failed.Status = StatusBlocked
	retry := resultFor("write", 1, "repairer")
	retry.StepAttemptID = "retry_attempt"
	rs := []StepResult{failed, retry}
	p := policy(rs)
	if _, e := Decide(flow, rs, p); e == nil {
		t.Fatal("unauthorized retry accepted")
	}
	p.Repair = &RepairAuthorization{FailedStepAttemptID: failed.StepAttemptID, Retry: expectedFor(retry), Authorized: true, Reason: "verified repair"}
	if d, e := Decide(flow, rs, p); e != nil || d != DecisionWait {
		t.Fatalf("missing gates %s %v", d, e)
	}
	review := resultFor("review", 1, "reviewer")
	verify := resultFor("verify", 1, "verifier")
	ship := resultFor("ship", 1, "shipper")
	rs = append(rs, review, verify, ship)
	p.Expected = policy(rs).Expected
	if d, e := Decide(flow, rs, p); e != nil || d != DecisionAdvance {
		t.Fatalf("valid retry %s %v", d, e)
	}
	rs[2].ProducerID = "repairer"
	p.Expected = policy(rs).Expected
	if _, e := Decide(flow, rs, p); e == nil {
		t.Fatal("non-independent reviewer accepted")
	}
}
