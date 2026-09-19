package contractaudit

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func requireResult(t *testing.T, out AuditOutput, verdict Verdict, reason ReasonCode) {
	t.Helper()
	if out.Verdict != verdict {
		t.Fatalf("verdict=%s reasons=%v", out.Verdict, out.ReasonCodes)
	}
	if verdict == VerdictPass {
		if len(out.ReasonCodes) != 0 {
			t.Fatalf("pass reasons=%v", out.ReasonCodes)
		}
	} else if !contains(out.ReasonCodes, reason) {
		t.Fatalf("reasons=%v, want %s", out.ReasonCodes, reason)
	}
	if out.PublicationStatus != NotPublishable || len(out.SideEffects) != 0 {
		t.Fatalf("prototype escaped boundary: %#v", out)
	}
}
func TestContract_GM_MalformedUnknownFieldRejected(t *testing.T) {
	in := completeInput()
	b, _ := json.Marshal(in)
	b = []byte(strings.TrimSuffix(string(b), "}") + `,"unknown":true}`)
	_, err := DecodeStrict(b)
	v, ok := err.(*ValidationError)
	if !ok || v.Code != UnknownField || v.Pointer != "/unknown" {
		t.Fatalf("%T %#v", err, err)
	}
}
func TestContract_GM_MalformedDuplicateInventoryRejected(t *testing.T) {
	in := completeInput()
	in.MutationInventory.Operations = append(in.MutationInventory.Operations, in.MutationInventory.Operations[0])
	if err := Validate(in); err == nil {
		t.Fatal("accepted duplicate inventory")
	}
}
func TestContract_GM_MalformedInvalidEnumAndPointer(t *testing.T) {
	in := completeInput()
	in.Policy.LegalStatuses[0] = "invalid"
	err := Validate(in)
	v, ok := err.(*ValidationError)
	if !ok || v.Code != InvalidDomainValue || v.Pointer != "/policy/legal_statuses" {
		t.Fatalf("%T %#v", err, err)
	}
}
func TestContract_GM_StrictDuplicateAndTrailingJSONRejected(t *testing.T) {
	_, err := DecodeStrict([]byte(`{"contract_version":"x","contract_version":"y"}`))
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate: %v", err)
	}
	_, err = DecodeStrict([]byte(`{} {}`))
	if err == nil {
		t.Fatal("trailing JSON accepted")
	}
}
func TestContract_GM_P1_ExhaustiveEditabilityMatrixPasses(t *testing.T) {
	in := completeInput()
	out, err := Evaluate(in, &in.Authority)
	if err != nil {
		t.Fatal(err)
	}
	requireResult(t, out, VerdictPass, PassComplete)
	if !out.Coverage.Complete || out.Coverage.MissingCells != 0 {
		t.Fatalf("coverage %#v", out.Coverage)
	}
}
func TestContract_GM_P2_StatePrecedesPermissionAndNoEffect(t *testing.T) {
	in := completeInput()
	out, err := Evaluate(in, &in.Authority)
	if err != nil {
		t.Fatal(err)
	}
	requireResult(t, out, VerdictPass, PassComplete)
	for _, r := range out.CellResults {
		if r.Expected == ExpectedRefuse && !r.NonEffectVerified {
			t.Fatalf("refusal non-effect not verified: %#v", r)
		}
	}
}
func TestContract_GM_P3_PositiveEffectAndForbiddenEffectAbsent(t *testing.T) {
	in := completeInput()
	out, _ := Evaluate(in, &in.Authority)
	requireResult(t, out, VerdictPass, PassComplete)
	if out.MutationAudit.IntendedEffectsVerified == 0 || out.MutationAudit.ForbiddenEffectsObserved != 0 {
		t.Fatalf("audit %#v", out.MutationAudit)
	}
}
func TestContract_GM_N1_UnclassifiedNewFieldHolds(t *testing.T) {
	in := completeInput()
	in.MutationInventory.Operations = append(in.MutationInventory.Operations, MutationOperation{OperationID: "new", OperationClass: "new", FieldPath: "new_field", SourceLocator: "fixture:new", Writable: true, Tier: TierContract, SchemaDigest: in.Authority.SnapshotToken.SchemaDigest})
	out, _ := Evaluate(in, &in.Authority)
	requireResult(t, out, VerdictHold, UnclassifiedSurface)
}
func TestContract_GM_N2_RefusalWithLandedMutationFails(t *testing.T) {
	in := completeInput()
	for _, p := range in.Controls.Probes {
		if p.Observed.Outcome == ObservedRefused {
			q := probe(&in, p.ProbeID)
			q.ObservedEffects = []ObservedEffect{{Target: "goal", OperationID: "update-goal", Change: "unexpected", PreDigest: testDigest("before"), PostDigest: testDigest("landed-change")}}
			break
		}
	}
	out, _ := Evaluate(in, &in.Authority)
	requireResult(t, out, VerdictFail, UnexpectedMutation)
	if len(out.MutationAudit.UnexpectedMutations) == 0 {
		t.Fatal("missing mutation ref")
	}
}
func TestContract_GM_N3_UnboundEffectReaderSelectorIsRejected(t *testing.T) {
	in := completeInput()
	in.Controls.Probes[0].EffectReader.Selector.Target = "other"
	if err := Validate(in); err == nil {
		t.Fatal("unbound reader selector accepted")
	}
}
func TestContract_GM_N4_DBSkipHolds(t *testing.T) {
	in := completeInput()
	in.Controls.Probes[0].Observed.Outcome = ObservedSkipped
	out, _ := Evaluate(in, &in.Authority)
	requireResult(t, out, VerdictHold, ProbeSkipped)
}
func TestContract_GM_N5_BoundaryTokenChangeHolds(t *testing.T) {
	in := completeInput()
	boundary := in.Authority
	boundary.SnapshotToken.PolicyDigest = testDigest("different")
	out, _ := Evaluate(in, &boundary)
	requireResult(t, out, VerdictHold, AuthorityChangedDuringCheck)
}
func TestContract_GM_ReplaySameKeyIsIdentical(t *testing.T) {
	in := completeInput()
	first, _ := Evaluate(in, &in.Authority)
	in.Replay.PriorResult = &ReplayRecord{RequestID: "request-1", ReplayKey: "key-1", InputDigest: first.Replay.InputDigest, OutputDigest: first.Replay.OutputDigest, ResultVersion: ContractVersion, RecordedAt: in.Freshness.CheckedAt}
	out, _ := Evaluate(in, &in.Authority)
	requireResult(t, out, VerdictPass, PassComplete)
	if out.Replay.Status != ReplayIdentical {
		t.Fatalf("replay=%#v", out.Replay)
	}
}
func TestContract_GM_N6_ReplayInputMismatchHolds(t *testing.T) {
	in := completeInput()
	in.Replay.PriorResult = &ReplayRecord{RequestID: "request-1", ReplayKey: "key-1", InputDigest: testDigest("other"), OutputDigest: testDigest("output"), ResultVersion: ContractVersion, RecordedAt: in.Freshness.CheckedAt}
	out, _ := Evaluate(in, &in.Authority)
	requireResult(t, out, VerdictHold, ReplayInputMismatch)
}
func TestContract_GM_LegacyZeroStepsPureMatrixIsExplicit(t *testing.T) {
	in := completeInput()
	in.Authority.SnapshotToken.StepsVersion = 0
	for i := range in.Controls.Probes {
		in.Controls.Probes[i].EffectReader.SnapshotToken.StepsVersion = 0
	}
	out, _ := Evaluate(in, &in.Authority)
	requireResult(t, out, VerdictPass, PassComplete)
	if out.StepsVersion != 0 || out.StepHistoryAvailable {
		t.Fatalf("legacy state not explicit: %#v", out)
	}
}
func TestContract_GM_N7_LegacyZeroStepsHistoryClaimHolds(t *testing.T) {
	in := completeInput()
	in.Authority.SnapshotToken.StepsVersion = 0
	in.RequiresStepHistory = true
	for i := range in.Controls.Probes {
		in.Controls.Probes[i].EffectReader.SnapshotToken.StepsVersion = 0
	}
	out, _ := Evaluate(in, &in.Authority)
	requireResult(t, out, VerdictHold, EvidenceNotAuthoritative)
}
func TestContract_GM_N8_ProductionOrSharedDBIsRefused(t *testing.T) {
	in := completeInput()
	in.Authority.SourceKind = SourceIsolatedDB
	in.Authority.SourceRef = ""
	if err := Validate(in); err == nil {
		t.Fatal("unsafe DB authority accepted")
	}
}
func TestContract_GM_AllCellProbesAndControlsAreEvaluated(t *testing.T) {
	in := completeInput()
	cell := &in.Policy.Cells[0]
	second := in.Controls.Probes[0]
	second.ProbeID = "probe-second"
	second.Observed.Outcome = ObservedSkipped
	cell.ProbeIDs = append(cell.ProbeIDs, second.ProbeID)
	in.Controls.Probes = append(in.Controls.Probes, second)
	in.Controls.PositiveControlIDs = append(in.Controls.PositiveControlIDs, second.ProbeID)
	out, err := Evaluate(in, &in.Authority)
	if err != nil {
		t.Fatal(err)
	}
	requireResult(t, out, VerdictHold, ProbeSkipped)
}
func TestContract_GM_StrictJSONRejectsCaseAliasAndNestedPointerUnknown(t *testing.T) {
	in := completeInput()
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"contract_version"`, `"Contract_Version"`, 1))
	if _, err := DecodeStrict(data); err == nil {
		t.Fatal("case alias accepted")
	}
	data, err = json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"replay_key":"key-1"`, `"prior_result":{"unknown":true},"replay_key":"key-1"`, 1))
	if _, err := DecodeStrict(data); err == nil {
		t.Fatal("nested pointer unknown field accepted")
	}
}
func TestContract_GM_ReplayAndPrecedenceBindingsAreClosed(t *testing.T) {
	in := completeInput()
	in.Policy.Precedence[0].Winner = "authority"
	if err := Validate(in); err == nil {
		t.Fatal("precedence winner mismatch accepted")
	}
	in = completeInput()
	first, err := Evaluate(in, &in.Authority)
	if err != nil {
		t.Fatal(err)
	}
	in.Replay.PriorResult = &ReplayRecord{RequestID: "request-1", ReplayKey: "other", InputDigest: first.Replay.InputDigest, OutputDigest: first.Replay.OutputDigest, ResultVersion: ContractVersion, RecordedAt: in.Freshness.CheckedAt}
	if err := Validate(in); err == nil {
		t.Fatal("replay key mismatch accepted")
	}
}
func TestContract_GM_BoundaryAuthorityAndOutputAreNotAliased(t *testing.T) {
	in := completeInput()
	boundary := in.Authority
	boundary.SourceRef = "fixture:other"
	out, err := Evaluate(in, &boundary)
	if err != nil {
		t.Fatal(err)
	}
	requireResult(t, out, VerdictHold, AuthorityChangedDuringCheck)
	out.Freshness.Boundary.PolicyDigest = testDigest("mutated")
	if boundary.SnapshotToken.PolicyDigest == out.Freshness.Boundary.PolicyDigest {
		t.Fatal("boundary output aliases caller")
	}
}
func TestContract_GM_R1_NonEffectNeedsScopedEvidence(t *testing.T) {
	in := completeInput()
	for i := range in.Controls.Probes {
		if in.Controls.Probes[i].Polarity == Negative {
			in.Controls.Probes[i].ObservedEffects = nil
			break
		}
	}
	out, err := Evaluate(in, &in.Authority)
	if err != nil {
		t.Fatal(err)
	}
	requireResult(t, out, VerdictHold, NonEffectUnprovable)
}

func TestContract_GM_R2_UnaccountedEffectFails(t *testing.T) {
	in := completeInput()
	p := probe(&in, in.Controls.PositiveControlIDs[0])
	p.ObservedEffects = append(p.ObservedEffects, ObservedEffect{Target: "other", OperationID: "update-goal", Change: "unlisted", PreDigest: testDigest("before-extra"), PostDigest: testDigest("after-extra")})
	out, err := Evaluate(in, &in.Authority)
	if err != nil {
		t.Fatal(err)
	}
	requireResult(t, out, VerdictFail, UnexpectedMutation)
}

func TestContract_GM_R3_CellRequestAndExpectedBinding(t *testing.T) {
	in := completeInput()
	in.Controls.Probes[0].Request.Tier = TierRecord
	if err := Validate(in); err == nil {
		t.Fatal("matrix tier/request mismatch accepted")
	}
	in = completeInput()
	in.Policy.Cells[0].Target = "other"
	if err := Validate(in); err == nil {
		t.Fatal("matrix target/request mismatch accepted")
	}
}

func TestContract_GM_R4_SchemaAndReaderIntervalAreBound(t *testing.T) {
	in := completeInput()
	in.MutationInventory.Operations[0].SchemaDigest = testDigest("other-schema")
	if err := Validate(in); err == nil {
		t.Fatal("operation schema mismatch accepted")
	}
	in = completeInput()
	in.Controls.Probes[0].EffectReader.EvidenceEnd = in.Controls.Probes[0].EffectReader.EvidenceStart
	in.Controls.Probes[0].ExecutedAt = in.Controls.Probes[0].EffectReader.EvidenceEnd.Add(time.Second)
	if err := Validate(in); err == nil {
		t.Fatal("reader interval mismatch accepted")
	}
}

func TestContract_GM_R5_FinalDigestAndCanonicalEmptySlices(t *testing.T) {
	in := completeInput()
	out, err := Evaluate(in, &in.Authority)
	if err != nil {
		t.Fatal(err)
	}
	if out.Replay.OutputDigest == "" {
		t.Fatal("missing finalized output digest")
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"side_effects":null`) || strings.Contains(string(b), `"reason_codes":null`) {
		t.Fatalf("canonical empty slices lost: %s", b)
	}
}

func TestContract_GM_R6_ReasonAndPrecedenceApplicability(t *testing.T) {
	in := completeInput()
	in.Policy.Cells[0].ExpectedReason = ""
	if err := Validate(in); err == nil {
		t.Fatal("allow cell without expected reason accepted")
	}
	in = completeInput()
	in.Policy.Precedence[0].Winner = "authority"
	if err := Validate(in); err == nil {
		t.Fatal("unsupported precedence winner accepted")
	}
	in = completeInput()
	in.Policy.Cells[0].PrecedenceWhen = "state_and_permission_conflict"
	if err := Validate(in); err == nil {
		t.Fatal("inapplicable precedence accepted")
	}
}

func TestContract_GM_FinalAuthorityObservedAtIsExactAndFresh(t *testing.T) {
	in := completeInput()
	boundary := in.Authority
	boundary.SnapshotToken.ObservedAt = boundary.SnapshotToken.ObservedAt.Add(time.Second)
	out, err := Evaluate(in, &boundary)
	if err != nil {
		t.Fatal(err)
	}
	requireResult(t, out, VerdictHold, AuthorityChangedDuringCheck)

	in = completeInput()
	in.Controls.Probes[0].EffectReader.SnapshotToken.ObservedAt = in.Controls.Probes[0].EffectReader.SnapshotToken.ObservedAt.Add(time.Second)
	if err := Validate(in); err == nil {
		t.Fatal("reader observed_at mismatch accepted")
	}
}

func TestContract_GM_FinalCoverageRequiresOperationTargetFieldCellAndProbe(t *testing.T) {
	in := completeInput()
	in.MutationInventory.Operations = append(in.MutationInventory.Operations, MutationOperation{
		OperationID: "update-summary", OperationClass: "update", FieldPath: "summary", SourceLocator: "fixture:summary",
		Writable: true, Tier: TierContract, SchemaDigest: in.Authority.SnapshotToken.SchemaDigest,
	})
	out, err := Evaluate(in, &in.Authority)
	if err != nil {
		t.Fatal(err)
	}
	requireResult(t, out, VerdictHold, CoverageIncomplete)
	if !contains(out.UnclassifiedSurface, "update-summary:summary") {
		t.Fatalf("second same-class operation escaped per-operation coverage: %#v", out.UnclassifiedSurface)
	}
}

func TestContract_GM_FinalStrictJSONRejectsNullPrimitives(t *testing.T) {
	in := completeInput()
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"contract_version", "steps_version", "observed_at", "require_boundary_recheck", "max_age_seconds"} {
		t.Run(field, func(t *testing.T) {
			// Replace the original scalar value, leaving a valid document with an explicit null.
			var document map[string]any
			if err := json.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			setNull(document, field)
			null, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeStrict(null); err == nil {
				t.Fatalf("explicit null for %s accepted", field)
			}
		})
	}
}

func setNull(document map[string]any, field string) {
	if _, ok := document[field]; ok {
		document[field] = nil
		return
	}
	var walk func(any) bool
	walk = func(value any) bool {
		switch value := value.(type) {
		case map[string]any:
			for key, child := range value {
				if key == field {
					value[key] = nil
					return true
				}
				if walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range value {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	if !walk(document) {
		panic("test field missing: " + field)
	}
}

func TestContract_GM_FinalEvaluatorDerivesPrecedenceWinner(t *testing.T) {
	in := completeInput()
	in.Policy.LegalStatuses = append(in.Policy.LegalStatuses, StatusWrapped)
	cell := &in.Policy.Cells[0]
	cell.Status = StatusWrapped
	cell.Actor = ActorOther
	cell.Tier = TierWorking
	cell.Expected = ExpectedRefuse
	cell.ExpectedReason = StateGateWins
	cell.PrecedenceWhen = "state_and_permission_conflict"
	p := probe(&in, cell.ProbeIDs[0])
	p.Request.Tier = TierWorking
	p.Observed = Observation{Outcome: ObservedRefused, ReasonCode: AuthorityUnavailable, ResponseDigest: p.Observed.ResponseDigest}
	p.ExpectedEffect = EffectExpectation{Target: "goal", OperationID: "update-goal", Cardinality: ExactlyZero, ForbiddenChanges: []string{"other"}}
	p.ObservedEffects = []ObservedEffect{{Target: "goal", OperationID: "update-goal", Change: "no change", PreDigest: testDigest("precedence"), PostDigest: testDigest("precedence")}}
	p.EffectReader.Selector = ReaderSelector{Target: "goal", OperationID: "update-goal", Cardinality: ExactlyZero}
	if err := Validate(in); err != nil {
		t.Fatal(err)
	}
	out, err := Evaluate(in, &in.Authority)
	if err != nil {
		t.Fatal(err)
	}
	requireResult(t, out, VerdictFail, ErrorKindMismatch)
}

func TestContract_GM_PrecedenceMustBeDeclaredAndOutcomeBound(t *testing.T) {
	in := completeInput()
	in.Policy.Cells[0].PrecedenceWhen = "terminal_and_working_conflict"
	if err := Validate(in); err == nil {
		t.Fatal("undeclared precedence accepted")
	}
	in = completeInput()
	in.Policy.LegalStatuses = append(in.Policy.LegalStatuses, StatusWrapped)
	cell := &in.Policy.Cells[0]
	cell.Status = StatusWrapped
	cell.Actor = ActorOther
	cell.Tier = TierWorking
	cell.Expected = ExpectedAllow
	cell.ExpectedReason = TerminalGateWins
	cell.PrecedenceWhen = "state_and_permission_conflict"
	if err := Validate(in); err == nil {
		t.Fatal("precedence outcome mismatch accepted")
	}
}

func TestContract_GM_PrecedenceRequiresActualClosedConflict(t *testing.T) {
	in := completeInput()
	in.Policy.LegalStatuses = append(in.Policy.LegalStatuses, StatusWrapped)
	cell := &in.Policy.Cells[0]
	cell.Status = StatusWrapped
	cell.Actor = ActorReporter
	cell.PrecedenceWhen = "state_and_permission_conflict"
	cell.Expected = ExpectedRefuse
	cell.ExpectedReason = StateGateWins
	if err := Validate(in); err == nil {
		t.Fatal("state precedence without permission conflict accepted")
	}
	cell.PrecedenceWhen = "rhs_and_authority_conflict"
	if err := Validate(in); err == nil {
		t.Fatal("unsupported rhs precedence accepted")
	}
}

func TestContract_GM_SkippedEffectsRemainAccounted(t *testing.T) {
	in := completeInput()
	p := probe(&in, in.Controls.PositiveControlIDs[0])
	p.Observed.Outcome = ObservedSkipped
	p.ObservedEffects = []ObservedEffect{{Target: "other", OperationID: "update-goal", Change: "landed", PreDigest: testDigest("before"), PostDigest: testDigest("after")}}
	out, err := Evaluate(in, &in.Authority)
	if err != nil {
		t.Fatal(err)
	}
	requireResult(t, out, VerdictFail, UnexpectedMutation)
	if !contains(out.ReasonCodes, ProbeSkipped) || len(out.MutationAudit.UnexpectedMutations) == 0 {
		t.Fatalf("skipped effect escaped accounting: %#v", out)
	}
}

func TestContract_GM_ReplayMismatchDoesNotDowngradeFailureOrCoverage(t *testing.T) {
	in := completeInput()
	p := probe(&in, in.Controls.PositiveControlIDs[0])
	p.ObservedEffects = append(p.ObservedEffects, ObservedEffect{Target: "other", OperationID: "update-goal", Change: "landed", PreDigest: testDigest("before"), PostDigest: testDigest("after")})
	in.Replay.PriorResult = &ReplayRecord{RequestID: "request-1", ReplayKey: "key-1", InputDigest: testDigest("different-input"), OutputDigest: testDigest("old-output"), ResultVersion: ContractVersion, RecordedAt: in.Freshness.CheckedAt}
	out, err := Evaluate(in, &in.Authority)
	if err != nil {
		t.Fatal(err)
	}
	requireResult(t, out, VerdictFail, UnexpectedMutation)
	if !contains(out.ReasonCodes, ReplayInputMismatch) || out.Coverage.Complete {
		t.Fatalf("replay mismatch did not preserve final failure/coverage: %#v", out)
	}
	if out.Replay.OutputDigest == "" {
		t.Fatal("final canonical digest missing")
	}
}
