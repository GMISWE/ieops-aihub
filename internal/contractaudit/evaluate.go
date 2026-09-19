package contractaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Evaluate is pure: boundary is injected; it reads no clock, file, DB, or runtime state.
func Evaluate(in AuditInput, boundary *Authority) (AuditOutput, error) {
	if err := Validate(in); err != nil {
		return AuditOutput{}, err
	}
	inputDigest, err := digest(inputIdentity(in))
	if err != nil {
		return AuditOutput{}, fmt.Errorf("serialize audit input: %w", err)
	}
	out := baseOutput(in, inputDigest, boundary)
	holds, fails := []ReasonCode{}, []ReasonCode{}
	addHold := func(c ReasonCode) { holds = addCode(holds, c) }
	addFail := func(c ReasonCode) { fails = addCode(fails, c) }
	expectedTuples := map[string]bool{}
	for _, op := range in.MutationInventory.Operations {
		if !op.Writable {
			continue
		}
		for _, s := range in.Policy.LegalStatuses {
			for _, a := range in.Policy.ActorClasses {
				for _, t := range in.Policy.MutationTiers {
					expectedTuples[matrixTuple(s, a, t, op.OperationID, op.FieldPath)] = true
				}
			}
		}
	}
	seenTuples := map[string]bool{}
	for _, cell := range in.Policy.Cells {
		seenTuples[matrixTuple(cell.Status, cell.Actor, cell.Tier, cell.OperationID, cell.Target)] = true
	}
	for k := range expectedTuples {
		if !seenTuples[k] {
			out.Coverage.MissingCells++
			addHold(CoverageIncomplete)
		}
	}
	classified := map[string]bool{}
	for _, cell := range in.Policy.Cells {
		classified[cell.OperationID+"|"+cell.Target] = true
	}
	for _, op := range in.MutationInventory.Operations {
		if op.Writable && !classified[op.OperationID+"|"+op.FieldPath] {
			out.UnclassifiedSurface = append(out.UnclassifiedSurface, op.OperationID+":"+op.FieldPath)
		}
	}
	sort.Strings(out.UnclassifiedSurface)
	out.Coverage.InventoryCount = len(in.MutationInventory.Operations)
	out.Coverage.ClassifiedCount = len(classified)
	if len(out.UnclassifiedSurface) > 0 {
		addHold(InventoryIncomplete)
		addHold(UnclassifiedSurface)
	}
	out.MutationAudit.AllWritesInventoryBound = len(out.UnclassifiedSurface) == 0
	out.Evidence = append(out.Evidence, EvidenceItem{Kind: "inventory", Ref: in.MutationInventory.SourceRef, Digest: in.MutationInventory.SourceDigest, ObservedAt: in.Authority.SnapshotToken.ObservedAt, Scope: in.Subject.Scope, Token: in.Authority.SnapshotToken})
	probesByCell := map[string][]Probe{}
	positive, negative := map[string]bool{}, map[string]bool{}
	for _, id := range in.Controls.PositiveControlIDs {
		positive[id] = true
	}
	for _, id := range in.Controls.NegativeControlIDs {
		negative[id] = true
	}
	for _, p := range in.Controls.Probes {
		probesByCell[p.CellID] = append(probesByCell[p.CellID], p)
		out.Evidence = append(out.Evidence, EvidenceItem{Kind: "probe", Ref: p.ExecutionRef, Digest: p.Observed.ResponseDigest, ObservedAt: p.ExecutedAt, Scope: p.EffectReader.Scope, Token: p.EffectReader.SnapshotToken})
		out.Coverage.ProbeCount++
		if p.Observed.Outcome == ObservedSkipped {
			out.Coverage.SkippedProbeCount++
		} else {
			out.Coverage.ExecutedProbeCount++
		}
	}
	for _, cell := range in.Policy.Cells {
		result := CellResult{CellID: cell.CellID, Expected: cell.Expected, Observed: ObservedSkipped, ProbeIDs: append([]string(nil), cell.ProbeIDs...)}
		probes := probesByCell[cell.CellID]
		if len(probes) != len(cell.ProbeIDs) {
			addHold(ProbeNotExecuted)
			result.ReasonCode = ProbeNotExecuted
			out.CellResults = append(out.CellResults, result)
			continue
		}
		allEffect, allNonEffect := true, true
		seenObservation := ObservedOutcome("")
		for _, p := range probes {
			seenObservation = p.Observed.Outcome
			if p.Observed.Outcome == ObservedSkipped {
				// A skipped probe still carries an execution record. Account for every
				// observed effect before holding on the skipped outcome; skipping must
				// never turn a landed mutation into invisible evidence.
				_, forbidden, changed, unaccounted, evidence := assessEffects(p)
				if unaccounted || forbidden || (changed && p.ExpectedEffect.Cardinality == ExactlyZero) {
					addFail(UnexpectedMutation)
					result.ReasonCode = UnexpectedMutation
					out.MutationAudit.ForbiddenEffectsObserved++
					out.MutationAudit.UnexpectedMutations = append(out.MutationAudit.UnexpectedMutations, p.ProbeID)
				}
				if !evidence {
					addHold(NonEffectUnprovable)
				}
				addHold(ProbeSkipped)
				allEffect = false
				allNonEffect = false
				continue
			}
			expectedObserved := expectedOutcome(cell.Expected)
			if p.Observed.Outcome != expectedObserved {
				addFail(PolicyMismatch)
				result.ReasonCode = PolicyMismatch
				out.SemanticConflicts = append(out.SemanticConflicts, SemanticConflict{Kind: "policy_vs_observation", Ref: p.ProbeID, Expected: string(expectedObserved), Actual: string(p.Observed.Outcome)})
			} else if expectedReason := evaluatorExpectedReason(cell); expectedReason != PassComplete && p.Observed.ReasonCode != expectedReason {
				addFail(ErrorKindMismatch)
				result.ReasonCode = ErrorKindMismatch
				out.SemanticConflicts = append(out.SemanticConflicts, SemanticConflict{Kind: "precedence", Ref: p.ProbeID, Expected: string(expectedReason), Actual: string(p.Observed.ReasonCode)})
			}
			intended, forbidden, changed, unaccounted, evidence := assessEffects(p)
			if !evidence {
				addHold(NonEffectUnprovable)
				allNonEffect = false
				if result.ReasonCode == "" {
					result.ReasonCode = NonEffectUnprovable
				}
			}
			if unaccounted {
				addFail(UnexpectedMutation)
				result.ReasonCode = UnexpectedMutation
				out.MutationAudit.ForbiddenEffectsObserved++
				out.MutationAudit.UnexpectedMutations = append(out.MutationAudit.UnexpectedMutations, p.ProbeID)
			}
			if forbidden {
				addFail(ForbiddenMutation)
				result.ReasonCode = ForbiddenMutation
				out.MutationAudit.ForbiddenEffectsObserved++
				out.MutationAudit.UnexpectedMutations = append(out.MutationAudit.UnexpectedMutations, p.ProbeID)
			}
			if cell.Expected == ExpectedAllow {
				if intended && !forbidden {
					out.MutationAudit.IntendedEffectsVerified++
				} else {
					allEffect = false
					addHold(EffectUnprovable)
					if result.ReasonCode == "" {
						result.ReasonCode = EffectUnprovable
					}
				}
			} else if changed {
				addFail(UnexpectedMutation)
				result.ReasonCode = UnexpectedMutation
				out.MutationAudit.ForbiddenEffectsObserved++
				out.MutationAudit.UnexpectedMutations = append(out.MutationAudit.UnexpectedMutations, p.ProbeID)
				allNonEffect = false
			} else if cell.Expected != ExpectedAllow && evidence {
				out.MutationAudit.RefusalNonEffectsVerified++
			}
		}
		result.Observed = seenObservation
		result.EffectVerified = allEffect && cell.Expected == ExpectedAllow
		result.NonEffectVerified = allNonEffect && cell.Expected != ExpectedAllow
		for _, id := range cell.ProbeIDs {
			if positive[id] && !result.EffectVerified {
				addHold(EffectUnprovable)
			}
			if negative[id] && !result.NonEffectVerified {
				addHold(NonEffectUnprovable)
			}
		}
		out.CellResults = append(out.CellResults, result)
	}
	if in.Authority.SnapshotToken.InventoryDigest != in.MutationInventory.SourceDigest {
		addHold(InventoryDigestMismatch)
	}
	applyFreshness(&out, in, boundary, addHold)
	if in.Authority.SnapshotToken.StepsVersion == 0 && in.RequiresStepHistory {
		addHold(EvidenceNotAuthoritative)
	}
	out.StepHistoryAvailable = in.Authority.SnapshotToken.StepsVersion > 0
	setFinal := func() {
		out.Coverage.Complete = len(holds) == 0 && len(fails) == 0 && out.Coverage.MissingCells == 0 && out.Coverage.ExecutedProbeCount == out.Coverage.ProbeCount
		if len(fails) > 0 {
			out.Verdict = VerdictFail
			out.ReasonCodes = copyReasons(fails)
			for _, reason := range holds {
				out.ReasonCodes = addCode(out.ReasonCodes, reason)
			}
		} else if len(holds) > 0 {
			out.Verdict = VerdictHold
			out.ReasonCodes = copyReasons(holds)
		} else {
			out.Verdict = VerdictPass
			out.ReasonCodes = []ReasonCode{}
		}
	}
	setFinal()
	if r := in.Replay.PriorResult; r != nil {
		if r.InputDigest != inputDigest {
			addHold(ReplayInputMismatch)
			out.Replay.Status = ReplayMismatch
		} else {
			out.Replay.Status = ReplayIdentical
		}
	}
	setFinal()
	canonicalDigest, err := digest(outputIdentity(out))
	if err != nil {
		return AuditOutput{}, fmt.Errorf("serialize finalized audit decision: %w", err)
	}
	if r := in.Replay.PriorResult; r != nil && r.InputDigest == inputDigest && r.OutputDigest != canonicalDigest {
		addHold(ReplayOutputMismatch)
		out.Replay.Status = ReplayMismatch
	}
	setFinal()
	out.Replay.OutputDigest, err = digest(outputIdentity(out))
	if err != nil {
		return AuditOutput{}, fmt.Errorf("serialize finalized audit decision: %w", err)
	}
	return copyOutput(out), nil
}
func evaluatorExpectedReason(cell MatrixCell) ReasonCode {
	if cell.PrecedenceWhen != "" {
		return mustPrecedence(cell.PrecedenceWhen).Reason
	}
	return cell.ExpectedReason
}

func mustPrecedence(when string) PrecedenceDecision {
	decision, ok := derivePrecedence(when)
	if !ok {
		panic("validated precedence is not derivable")
	}
	return decision
}

func expectedOutcome(e ExpectedOutcome) ObservedOutcome {
	switch e {
	case ExpectedAllow:
		return ObservedAllowed
	case ExpectedRefuse:
		return ObservedRefused
	default:
		return ObservedHeld
	}
}
func assessEffects(p Probe) (intended, forbidden, changed, unaccounted, evidence bool) {
	matches := 0
	for _, e := range p.ObservedEffects {
		evidence = true
		didChange := e.PreDigest != e.PostDigest
		if didChange {
			changed = true
		}
		if e.Target != p.ExpectedEffect.Target || e.OperationID != p.ExpectedEffect.OperationID {
			unaccounted = true
			continue
		}
		if e.Change == p.ExpectedEffect.IntendedChange && didChange {
			matches++
		} else if p.ExpectedEffect.Cardinality == ExactlyOne || didChange {
			unaccounted = true
		}
		for _, f := range p.ExpectedEffect.ForbiddenChanges {
			if e.Change == f && didChange {
				forbidden = true
			}
		}
	}
	switch p.ExpectedEffect.Cardinality {
	case ExactlyOne:
		intended = matches == 1
		if matches > 1 {
			unaccounted = true
		}
	case ExactlyZero:
		intended = matches == 0
	case AtMostOne:
		intended = matches <= 1
		if matches > 1 {
			unaccounted = true
		}
	}
	return
}
func baseOutput(in AuditInput, input string, boundary *Authority) AuditOutput {
	var bt *VersionToken
	if boundary != nil {
		x := boundary.SnapshotToken
		bt = &x
	}
	return AuditOutput{ContractVersion: ContractVersion, CandidateID: CandidateID, Freshness: FreshnessResult{Status: Unprovable, Start: in.Authority.SnapshotToken, Boundary: bt, MaxAgeSeconds: in.Freshness.MaxAgeSeconds}, Replay: ReplayResult{Status: ReplayFirst, RequestID: in.Replay.RequestID, ReplayKey: in.Replay.ReplayKey, ResultVersion: ContractVersion, InputDigest: input}, SideEffects: []string{}, PublicationStatus: NotPublishable, StepsVersion: in.Authority.SnapshotToken.StepsVersion, MutationAudit: MutationAudit{UnexpectedMutations: []string{}}, UnclassifiedSurface: []string{}, SemanticConflicts: []SemanticConflict{}, Evidence: []EvidenceItem{}, CellResults: []CellResult{}}
}
func applyFreshness(out *AuditOutput, in AuditInput, boundary *Authority, add func(ReasonCode)) {
	start := in.Authority.SnapshotToken
	if in.Freshness.CheckedAt.Before(start.ObservedAt) || in.Freshness.CheckedAt.Sub(start.ObservedAt) > time.Duration(in.Freshness.MaxAgeSeconds)*time.Second {
		out.Freshness.Status = Stale
		add(StaleSnapshot)
		return
	}
	for _, p := range in.Controls.Probes {
		if p.ExecutedAt.Before(start.ObservedAt) || p.ExecutedAt.After(in.Freshness.CheckedAt) || in.Freshness.CheckedAt.Sub(p.ExecutedAt) > time.Duration(in.Freshness.MaxAgeSeconds)*time.Second {
			out.Freshness.Status = Stale
			add(StaleSnapshot)
			return
		}
	}
	if boundary == nil {
		out.Freshness.Status = Unprovable
		add(FreshnessUnprovable)
		return
	}
	b := boundary.SnapshotToken
	out.Freshness.Boundary = &b
	if b.ObservedAt.IsZero() || b.ObservedAt.After(in.Freshness.CheckedAt) || in.Freshness.CheckedAt.Sub(b.ObservedAt) > time.Duration(in.Freshness.MaxAgeSeconds)*time.Second {
		out.Freshness.Status = Stale
		add(StaleSnapshot)
		return
	}
	if boundary.ReaderID != in.Authority.ReaderID || boundary.SourceKind != in.Authority.SourceKind || boundary.SourceRef != in.Authority.SourceRef || boundary.Scope != in.Authority.Scope || !sameAllTokens(start, b) {
		out.Freshness.Status = Changed
		add(AuthorityChangedDuringCheck)
		return
	}
	out.Freshness.Status = Fresh
}
func sameAllTokens(a, b VersionToken) bool {
	return a.StepsVersion == b.StepsVersion && a.SchemaDigest == b.SchemaDigest && a.InventoryDigest == b.InventoryDigest && a.PolicyDigest == b.PolicyDigest && a.ObservedAt.Equal(b.ObservedAt)
}
func addCode(codes []ReasonCode, c ReasonCode) []ReasonCode {
	for _, x := range codes {
		if x == c {
			return codes
		}
	}
	return append(codes, c)
}
func digest(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:]), nil
}
func inputIdentity(in AuditInput) AuditInput { in.Replay.PriorResult = nil; return in }
func outputIdentity(out AuditOutput) AuditOutput {
	out.Replay.Status = ReplayFirst
	out.Replay.OutputDigest = ""
	return out
}

func copyStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string{}, in...)
}
func copyReasons(in []ReasonCode) []ReasonCode {
	if in == nil {
		return nil
	}
	return append([]ReasonCode{}, in...)
}
func copyConflicts(in []SemanticConflict) []SemanticConflict {
	if in == nil {
		return nil
	}
	return append([]SemanticConflict{}, in...)
}

// copyOutput makes boundary and all exported slices independent from evaluator-owned storage.
func copyOutput(out AuditOutput) AuditOutput {
	out.ReasonCodes = copyReasons(out.ReasonCodes)
	out.CellResults = append([]CellResult{}, out.CellResults...)
	for i := range out.CellResults {
		out.CellResults[i].ProbeIDs = copyStrings(out.CellResults[i].ProbeIDs)
	}
	out.UnclassifiedSurface = copyStrings(out.UnclassifiedSurface)
	out.SemanticConflicts = copyConflicts(out.SemanticConflicts)
	out.MutationAudit.UnexpectedMutations = copyStrings(out.MutationAudit.UnexpectedMutations)
	out.Evidence = append([]EvidenceItem{}, out.Evidence...)
	out.SideEffects = copyStrings(out.SideEffects)
	if out.Freshness.Boundary != nil {
		x := *out.Freshness.Boundary
		out.Freshness.Boundary = &x
	}
	return out
}
