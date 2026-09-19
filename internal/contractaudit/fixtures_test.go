package contractaudit

import (
	"fmt"
	"time"
)

func testDigest(ch string) string { return "sha256:" + fmt.Sprintf("%064x", len(ch)+1) }
func completeInput() AuditInput {
	now := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	token := VersionToken{StepsVersion: 1, SchemaDigest: testDigest("schema"), InventoryDigest: testDigest("inventory"), PolicyDigest: testDigest("policy"), ObservedAt: now}
	in := AuditInput{ContractVersion: ContractVersion, CandidateID: CandidateID, Subject: Subject{Kind: "work_item", ID: "wi_test", Scope: "isolated-test"}, Authority: Authority{ReaderID: FixtureReader, SourceKind: SourceFixture, SourceRef: "fixture:test", Scope: "isolated-test", SnapshotToken: token}, Freshness: FreshnessPolicy{CheckedAt: now.Add(time.Second), MaxAgeSeconds: 60, RequireBoundaryRecheck: true, RequiredTokens: []string{"steps_version", "schema_digest", "inventory_digest", "policy_digest", "observed_at"}}, MutationInventory: MutationInventory{SourceRef: "fixture:schema", SourceDigest: token.InventoryDigest, ExplicitRiders: []string{}, Operations: []MutationOperation{{OperationID: "update-goal", OperationClass: "update", FieldPath: "goal", SourceLocator: "fixture:goal", Writable: true, Tier: TierContract, SchemaDigest: token.SchemaDigest}}}, Evidence: EvidencePolicy{RequireClosedRefs: true, RequireExecutionRef: true, RedactValues: true}, Replay: ReplayContext{RequestID: "request-1", ReplayKey: "key-1"}}
	in.Policy.LegalStatuses = []Status{StatusQueued, StatusRunning}
	in.Policy.ActorClasses = []ActorClass{ActorReporter, ActorOther}
	in.Policy.MutationTiers = []MutationTier{TierContract, TierWorking}
	in.Policy.Precedence = []PrecedenceRule{{When: "state_and_permission_conflict", Winner: "state"}}
	for _, s := range in.Policy.LegalStatuses {
		for _, a := range in.Policy.ActorClasses {
			for _, tier := range in.Policy.MutationTiers {
				id := fmt.Sprintf("%s-%s-%s", s, a, tier)
				expected := ExpectedRefuse
				if s == StatusQueued && a == ActorReporter {
					expected = ExpectedAllow
				}
				cell := MatrixCell{CellID: id, Status: s, Actor: a, Tier: tier, OperationClass: "update", OperationID: "update-goal", Target: "goal", Expected: expected, ExpectedReason: PassComplete, ProbeIDs: []string{"probe-" + id}}
				if expected != ExpectedAllow {
					cell.ExpectedReason = AuthorityUnavailable
					cell.PrecedenceWhen = ""
				}
				in.Policy.Cells = append(in.Policy.Cells, cell)
				obs := Observation{Outcome: ObservedRefused, ReasonCode: AuthorityUnavailable, ResponseDigest: testDigest("response" + id)}
				effect := EffectExpectation{Target: "goal", OperationID: "update-goal", Cardinality: ExactlyZero, ForbiddenChanges: []string{"other"}}
				effects := []ObservedEffect{{Target: "goal", OperationID: "update-goal", Change: "no change", PreDigest: testDigest("same" + id), PostDigest: testDigest("same" + id)}}
				polarity := Negative
				if expected == ExpectedAllow {
					obs.Outcome = ObservedAllowed
					obs.ReasonCode = ""
					effect = EffectExpectation{Target: "goal", OperationID: "update-goal", Cardinality: ExactlyOne, IntendedChange: "goal updated", ForbiddenChanges: []string{"other"}}
					effects = []ObservedEffect{{Target: "goal", OperationID: "update-goal", Change: "goal updated", PreDigest: testDigest("before" + id), PostDigest: testDigest("after" + id)}}
					polarity = Positive
				}
				p := Probe{ProbeID: "probe-" + id, CellID: id, Polarity: polarity, Request: ProbeRequest{OperationID: "update-goal", OperationClass: "update", Target: "goal", Tier: tier, SubjectRef: "wi_test", PayloadDigest: testDigest("payload" + id)}, Observed: obs, ExpectedEffect: effect, ObservedEffects: effects, EffectReader: ReaderRef{ReaderID: FixtureReader, SourceKind: SourceFixture, SourceRef: "fixture:test", Selector: ReaderSelector{Target: effect.Target, OperationID: effect.OperationID, Cardinality: effect.Cardinality, Change: effect.IntendedChange}, Scope: "isolated-test", SnapshotToken: token, EvidenceStart: now.Add(-time.Second), EvidenceEnd: now.Add(2 * time.Second)}, ExecutedAt: now, ExecutionRef: "run:" + id}
				in.Controls.Probes = append(in.Controls.Probes, p)
				if polarity == Positive {
					in.Controls.PositiveControlIDs = append(in.Controls.PositiveControlIDs, p.ProbeID)
				} else {
					in.Controls.NegativeControlIDs = append(in.Controls.NegativeControlIDs, p.ProbeID)
				}
			}
		}
	}
	return in
}
func probe(in *AuditInput, id string) *Probe {
	for i := range in.Controls.Probes {
		if in.Controls.Probes[i].ProbeID == id {
			return &in.Controls.Probes[i]
		}
	}
	return nil
}
