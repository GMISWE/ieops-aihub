package contractaudit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"time"
)

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// DecodeStrict rejects case-folded and logical duplicate members, unknown fields, and trailing values.
func DecodeStrict(data []byte) (AuditInput, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := scanValue(dec, ""); err != nil {
		return AuditInput{}, err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return AuditInput{}, invalid("", "trailing JSON value")
		}
		return AuditInput{}, invalid("", err.Error())
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return AuditInput{}, invalid("", err.Error())
	}
	if err := validateJSONShape(raw, reflect.TypeOf(AuditInput{}), ""); err != nil {
		return AuditInput{}, err
	}
	dec = json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var in AuditInput
	if err := dec.Decode(&in); err != nil {
		return AuditInput{}, invalid("", err.Error())
	}
	if err := Validate(in); err != nil {
		return AuditInput{}, err
	}
	return in, nil
}
func scanValue(dec *json.Decoder, pointer string) error {
	t, err := dec.Token()
	if err != nil {
		return invalid(pointer, err.Error())
	}
	d, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch d {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return invalid(pointer, err.Error())
			}
			name, ok := key.(string)
			if !ok {
				return invalid(pointer, "object key is not a string")
			}
			p := pointer + "/" + escapePointer(name)
			normalized := strings.ToLower(name)
			if seen[normalized] {
				return invalid(p, "duplicate or case-folded object member")
			}
			seen[normalized] = true
			if err := scanValue(dec, p); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	case '[':
		for i := 0; dec.More(); i++ {
			if err := scanValue(dec, fmt.Sprintf("%s/%d", pointer, i)); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	default:
		return invalid(pointer, "unexpected delimiter")
	}
}
func validateJSONShape(value any, typ reflect.Type, pointer string) error {
	if value == nil {
		return invalid(pointer, "null is not permitted")
	}
	if typ == reflect.TypeOf(time.Time{}) {
		if _, ok := value.(string); !ok {
			return invalid(pointer, "timestamp string required")
		}
		return nil
	}
	for typ.Kind() == reflect.Pointer {
		if value == nil {
			return invalid(pointer, "null is not permitted")
		}
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		obj, ok := value.(map[string]any)
		if !ok {
			return invalid(pointer, "object required")
		}
		fields := map[string]reflect.StructField{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				fields[name] = f
			}
		}
		for key, v := range obj {
			f, ok := fields[key]
			if !ok {
				return &ValidationError{Pointer: pointer + "/" + escapePointer(key), Code: UnknownField, Detail: "unknown or non-canonical JSON member"}
			}
			if err := validateJSONShape(v, f.Type, pointer+"/"+escapePointer(key)); err != nil {
				return err
			}
		}
	case reflect.Slice:
		arr, ok := value.([]any)
		if !ok {
			return invalid(pointer, "array required")
		}
		for i, v := range arr {
			if err := validateJSONShape(v, typ.Elem(), fmt.Sprintf("%s/%d", pointer, i)); err != nil {
				return err
			}
		}
	}
	return nil
}
func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}
func invalid(pointer, detail string) error {
	return &ValidationError{Pointer: pointer, Code: InvalidInput, Detail: detail}
}
func domain(pointer, detail string) error {
	return &ValidationError{Pointer: pointer, Code: InvalidDomainValue, Detail: detail}
}
func nonEmpty(s string) bool    { return strings.TrimSpace(s) != "" }
func validDigest(s string) bool { return digestPattern.MatchString(s) }
func unique[T comparable](values []T) bool {
	seen := map[T]bool{}
	for _, v := range values {
		if seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}

func Validate(in AuditInput) error {
	if in.ContractVersion != ContractVersion {
		return domain("/contract_version", "unsupported contract version")
	}
	if in.CandidateID != CandidateID {
		return domain("/candidate_id", "unsupported candidate")
	}
	if !nonEmpty(in.Subject.ID) || !nonEmpty(in.Subject.Scope) || !oneOf(in.Subject.Kind, "work_item", "workflow", "artifact", "mutation_surface") {
		return domain("/subject", "invalid subject")
	}
	if err := validateAuthority("/authority", in.Authority, in.Subject.Scope); err != nil {
		return err
	}
	if in.Freshness.CheckedAt.IsZero() || in.Freshness.MaxAgeSeconds <= 0 || !in.Freshness.RequireBoundaryRecheck || len(in.Freshness.RequiredTokens) != 5 || !unique(in.Freshness.RequiredTokens) {
		return domain("/freshness", "invalid safe closed freshness policy")
	}
	for _, t := range in.Freshness.RequiredTokens {
		if !oneOf(t, "steps_version", "schema_digest", "inventory_digest", "policy_digest", "observed_at") {
			return domain("/freshness/required_tokens", "unknown token")
		}
	}
	p := in.Policy
	if len(p.LegalStatuses) == 0 || len(p.ActorClasses) == 0 || len(p.MutationTiers) == 0 || len(p.Precedence) == 0 || !unique(p.LegalStatuses) || !unique(p.ActorClasses) || !unique(p.MutationTiers) {
		return domain("/policy", "empty or duplicate closed policy dimension")
	}
	for _, v := range p.LegalStatuses {
		if !validStatus(v) {
			return domain("/policy/legal_statuses", "invalid status")
		}
	}
	for _, v := range p.ActorClasses {
		if !validActor(v) {
			return domain("/policy/actor_classes", "invalid actor")
		}
	}
	for _, v := range p.MutationTiers {
		if !validTier(v) {
			return domain("/policy/mutation_tiers", "invalid tier")
		}
	}
	precedence := map[string]PrecedenceRule{}
	for i, r := range p.Precedence {
		decision, ok := derivePrecedence(r.When)
		if !ok || r.Winner != decision.Winner {
			return domain(fmt.Sprintf("/policy/precedence/%d", i), "invalid precedence enum")
		}
		if _, ok := precedence[r.When]; ok {
			return domain(fmt.Sprintf("/policy/precedence/%d", i), "duplicate precedence")
		}
		precedence[r.When] = r
	}
	cellIDs := map[string]bool{}
	tuples := map[string]bool{}
	probeCell := map[string]string{}
	for i, c := range p.Cells {
		ptr := fmt.Sprintf("/policy/cells/%d", i)
		if !nonEmpty(c.CellID) || !nonEmpty(c.OperationClass) || !nonEmpty(c.OperationID) || !nonEmpty(c.Target) || len(c.ProbeIDs) == 0 || cellIDs[c.CellID] || !contains(p.LegalStatuses, c.Status) || !contains(p.ActorClasses, c.Actor) || !contains(p.MutationTiers, c.Tier) || !validExpected(c.Expected) || !unique(c.ProbeIDs) {
			return domain(ptr, "invalid matrix cell")
		}
		if c.ExpectedReason == "" || !validReason(c.ExpectedReason) {
			return domain(ptr, "missing or invalid expected reason")
		}
		if c.PrecedenceWhen != "" {
			declared, ok := precedence[c.PrecedenceWhen]
			derived, derivable := derivePrecedence(c.PrecedenceWhen)
			if !ok || !derivable || declared.Winner != derived.Winner || !precedenceApplies(c, c.PrecedenceWhen) || c.Expected != derived.Outcome || c.ExpectedReason != derived.Reason {
				return domain(ptr, "precedence is not a supported declared applicable cell binding")
			}
		}
		cellIDs[c.CellID] = true
		k := matrixTuple(c.Status, c.Actor, c.Tier, c.OperationID, c.Target)
		if tuples[k] {
			return domain(ptr, "duplicate matrix tuple")
		}
		tuples[k] = true
		for _, id := range c.ProbeIDs {
			if old, ok := probeCell[id]; ok && old != c.CellID {
				return domain(ptr, "probe referenced by multiple cells")
			}
			probeCell[id] = c.CellID
		}
	}
	inv := in.MutationInventory
	if !nonEmpty(inv.SourceRef) || !validDigest(inv.SourceDigest) || len(inv.Operations) == 0 {
		return domain("/mutation_inventory", "invalid inventory")
	}
	ops := map[string]MutationOperation{}
	fields := map[string]bool{}
	for i, o := range inv.Operations {
		ptr := fmt.Sprintf("/mutation_inventory/operations/%d", i)
		if !nonEmpty(o.OperationID) || !nonEmpty(o.OperationClass) || !nonEmpty(o.FieldPath) || !nonEmpty(o.SourceLocator) || !validDigest(o.SchemaDigest) || o.SchemaDigest != in.Authority.SnapshotToken.SchemaDigest || !contains(p.MutationTiers, o.Tier) || (o.Writable && o.Tier == TierRider) || (!o.Writable && o.Tier == TierRider && !nonEmpty(o.RiderReason)) {
			return domain(ptr, "invalid inventory operation")
		}
		if _, ok := ops[o.OperationID]; ok {
			return domain(ptr, "duplicate operation")
		}
		ops[o.OperationID] = o
		if o.Writable {
			if fields[o.FieldPath] {
				return domain(ptr, "duplicate writable field")
			}
			fields[o.FieldPath] = true
		}
	}
	for _, c := range p.Cells {
		o, ok := ops[c.OperationID]
		if !ok || c.OperationClass != o.OperationClass || c.Target != o.FieldPath {
			return domain("/policy/cells", "matrix cell is not bound to an inventory operation surface")
		}
	}
	probes := map[string]Probe{}
	for i, pr := range in.Controls.Probes {
		ptr := fmt.Sprintf("/controls/probes/%d", i)
		if !nonEmpty(pr.ProbeID) || probes[pr.ProbeID].ProbeID != "" || probeCell[pr.ProbeID] != pr.CellID || !validPolarity(pr.Polarity) || !validObserved(pr.Observed.Outcome) || !validReasonOptional(pr.Observed.ReasonCode) || !validDigest(pr.Observed.ResponseDigest) || !validRequest(pr.Request, in.Subject, ops) || !validEffectExpectation(pr.ExpectedEffect, ops) || !nonEmpty(pr.ExecutionRef) || pr.ExecutedAt.IsZero() {
			return domain(ptr, "invalid probe or bidirectional cell reference")
		}
		if err := validateReader(ptr+"/effect_reader", pr.EffectReader, in.Authority, pr.ExpectedEffect); err != nil {
			return err
		}
		if !pr.ExecutedAt.IsZero() && (pr.ExecutedAt.Before(pr.EffectReader.EvidenceStart) || pr.ExecutedAt.After(pr.EffectReader.EvidenceEnd)) {
			return domain(ptr, "probe execution is outside reader evidence interval")
		}
		cell := p.CellByID(pr.CellID)
		if cell.OperationClass != pr.Request.OperationClass || cell.OperationID != pr.Request.OperationID || cell.Target != pr.Request.Target || cell.Tier != pr.Request.Tier || pr.ExpectedEffect.OperationID != pr.Request.OperationID || pr.ExpectedEffect.Target != pr.Request.Target {
			return domain(ptr, "probe request/effect is not bound to matrix cell")
		}
		for j, e := range pr.ObservedEffects {
			if !validEffect(e) {
				return domain(fmt.Sprintf("%s/observed_effects/%d", ptr, j), "invalid typed pre/post effect")
			}
		}
		probes[pr.ProbeID] = pr
	}
	if len(probes) != len(probeCell) {
		return domain("/controls/probes", "not every cell probe is provided")
	}
	if len(in.Controls.PositiveControlIDs) == 0 || len(in.Controls.NegativeControlIDs) == 0 || !unique(in.Controls.PositiveControlIDs) || !unique(in.Controls.NegativeControlIDs) {
		return domain("/controls", "missing or duplicate control")
	}
	designated := map[string]Polarity{}
	for _, id := range in.Controls.PositiveControlIDs {
		p, ok := probes[id]
		if !ok || p.Polarity != Positive {
			return domain("/controls/positive_control_ids", "dangling or wrong-polarity control")
		}
		designated[id] = Positive
	}
	for _, id := range in.Controls.NegativeControlIDs {
		p, ok := probes[id]
		if !ok || p.Polarity != Negative {
			return domain("/controls/negative_control_ids", "dangling or wrong-polarity control")
		}
		if _, ok := designated[id]; ok {
			return domain("/controls", "probe is both controls")
		}
		designated[id] = Negative
	}
	if len(designated) != len(probes) {
		return domain("/controls", "every probe must be a designated control")
	}
	if !in.Evidence.RequireClosedRefs || !in.Evidence.RequireExecutionRef || in.Evidence.AllowCachedAuthority || !in.Evidence.RedactValues || !nonEmpty(in.Replay.RequestID) || !nonEmpty(in.Replay.ReplayKey) {
		return domain("/evidence", "open evidence or invalid replay")
	}
	if r := in.Replay.PriorResult; r != nil {
		if r.RequestID != in.Replay.RequestID || r.ReplayKey != in.Replay.ReplayKey || !validDigest(r.InputDigest) || !validDigest(r.OutputDigest) || r.ResultVersion != ContractVersion || r.RecordedAt.IsZero() {
			return domain("/replay/prior_result", "replay request/key/version binding invalid")
		}
	}
	return nil
}
func validateAuthority(ptr string, a Authority, scope string) error {
	if !validReader(a.ReaderID) || !validSource(a.SourceKind) || !nonEmpty(a.SourceRef) || a.Scope != scope {
		return domain(ptr, "invalid authority")
	}
	return validateToken(ptr+"/snapshot_token", a.SnapshotToken)
}
func validateReader(ptr string, r ReaderRef, a Authority, expected EffectExpectation) error {
	if !validReader(r.ReaderID) || !validSource(r.SourceKind) || r.ReaderID != a.ReaderID || r.SourceKind != a.SourceKind || r.SourceRef != a.SourceRef || r.Scope != a.Scope || r.EvidenceStart.IsZero() || r.EvidenceEnd.IsZero() || r.EvidenceStart.After(r.EvidenceEnd) || a.SnapshotToken.ObservedAt.Before(r.EvidenceStart) || a.SnapshotToken.ObservedAt.After(r.EvidenceEnd) {
		return domain(ptr, "reader is not bound to authority")
	}
	if r.Selector.Target != expected.Target || r.Selector.OperationID != expected.OperationID || r.Selector.Cardinality != expected.Cardinality || r.Selector.Change != expected.IntendedChange {
		return domain(ptr+"/selector", "reader selector is not bound to target, operation, and expected effect")
	}
	if err := validateToken(ptr+"/snapshot_token", r.SnapshotToken); err != nil {
		return err
	}
	if !sameAllTokens(r.SnapshotToken, a.SnapshotToken) {
		return domain(ptr, "reader token is not authority token")
	}
	return nil
}
func validRequest(r ProbeRequest, s Subject, ops map[string]MutationOperation) bool {
	o, ok := ops[r.OperationID]
	return ok && r.OperationClass == o.OperationClass && r.Target == o.FieldPath && r.SubjectRef == s.ID && validDigest(r.PayloadDigest)
}
func validEffectExpectation(e EffectExpectation, ops map[string]MutationOperation) bool {
	o, ok := ops[e.OperationID]
	return ok && e.Target == o.FieldPath && validCardinality(e.Cardinality) && unique(e.ForbiddenChanges) && ((e.Cardinality == ExactlyOne && nonEmpty(e.IntendedChange)) || (e.Cardinality != ExactlyOne && e.IntendedChange == ""))
}
func validEffect(e ObservedEffect) bool {
	return nonEmpty(e.Target) && nonEmpty(e.OperationID) && nonEmpty(e.Change) && validDigest(e.PreDigest) && validDigest(e.PostDigest)
}
func validateToken(ptr string, t VersionToken) error {
	if t.StepsVersion < 0 || !validDigest(t.SchemaDigest) || !validDigest(t.InventoryDigest) || !validDigest(t.PolicyDigest) || t.ObservedAt.IsZero() {
		return domain(ptr, "invalid token")
	}
	return nil
}
func oneOf(s string, vals ...string) bool {
	for _, v := range vals {
		if s == v {
			return true
		}
	}
	return false
}
func contains[T comparable](v []T, x T) bool {
	for _, a := range v {
		if a == x {
			return true
		}
	}
	return false
}
func matrixTuple(s Status, a ActorClass, t MutationTier, operationID, target string) string {
	return string(s) + "|" + string(a) + "|" + string(t) + "|" + operationID + "|" + target
}
func validStatus(v Status) bool {
	return contains([]Status{StatusQueued, StatusRunning, StatusPaused, StatusBlocked, StatusWrapped, StatusFailed, StatusCancelled}, v)
}
func validActor(v ActorClass) bool {
	return contains([]ActorClass{ActorReporter, ActorMaintainer, ActorAdmin, ActorOther}, v)
}
func validTier(v MutationTier) bool {
	return contains([]MutationTier{TierContract, TierWorking, TierRecord, TierRider}, v)
}
func validExpected(v ExpectedOutcome) bool {
	return contains([]ExpectedOutcome{ExpectedAllow, ExpectedRefuse, ExpectedHold}, v)
}
func validObserved(v ObservedOutcome) bool {
	return contains([]ObservedOutcome{ObservedAllowed, ObservedRefused, ObservedHeld, ObservedMalformed, ObservedSkipped}, v)
}
func validPolarity(v Polarity) bool { return v == Positive || v == Negative }
func validCardinality(v Cardinality) bool {
	return v == ExactlyOne || v == ExactlyZero || v == AtMostOne
}
func validReader(v ReaderID) bool           { return v == FixtureReader || v == DBReader }
func validSource(v SourceKind) bool         { return v == SourceFixture || v == SourceIsolatedDB }
func validReasonOptional(v ReasonCode) bool { return v == "" || validReason(v) }
func (p Policy) CellByID(id string) MatrixCell {
	for _, c := range p.Cells {
		if c.CellID == id {
			return c
		}
	}
	return MatrixCell{}
}

type PrecedenceDecision struct {
	Winner  string
	Reason  ReasonCode
	Outcome ExpectedOutcome
}

func derivePrecedence(when string) (PrecedenceDecision, bool) {
	switch when {
	case "state_and_permission_conflict":
		return PrecedenceDecision{Winner: "state", Reason: StateGateWins, Outcome: ExpectedRefuse}, true
	case "terminal_and_working_conflict":
		return PrecedenceDecision{Winner: "terminal", Reason: TerminalGateWins, Outcome: ExpectedRefuse}, true
	default:
		return PrecedenceDecision{}, false
	}
}

func precedenceApplies(c MatrixCell, when string) bool {
	terminal := c.Status == StatusWrapped || c.Status == StatusFailed || c.Status == StatusCancelled
	switch when {
	case "state_and_permission_conflict":
		return terminal && c.Actor != ActorReporter
	case "terminal_and_working_conflict":
		return terminal && c.Tier == TierWorking
	default:
		return false
	}
}

func validReason(v ReasonCode) bool {
	for _, x := range []ReasonCode{AuthorityUntrusted, ScopeMismatch, AuthorityUnavailable, AuthorityInvalid, InventoryIncomplete, InventoryDigestMismatch, DuplicateInventoryEntry, CoverageIncomplete, DuplicateMatrixCell, UnexpectedMatrixCell, UnclassifiedSurface, VacuousProbe, ProbeNotExecuted, ProbeSkipped, EffectUnprovable, NonEffectUnprovable, FreshnessUnprovable, StaleSnapshot, AuthorityChangedDuringCheck, ReplayInputMismatch, ReplayOutputMismatch, IsolationRequired, EvidenceNotClosed, EvidenceNotAuthoritative, ExecutionNotProven, PolicyMismatch, ErrorKindMismatch, StateGateWins, TerminalGateWins, AuthorityGateWins, UnexpectedMutation, ForbiddenMutation, PassComplete} {
		if v == x {
			return true
		}
	}
	return false
}
