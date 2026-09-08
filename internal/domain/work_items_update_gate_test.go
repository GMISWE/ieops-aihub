package domain

// Unit tests for the work_items editability matrix (aihub#440 / aihub#411 T2-1).
// Everything here runs unconditionally: updateGate, wiStatusClassOf,
// strictestSuppliedEditTier and buildWorkItemUpdate all take no DB dependency,
// so this is the "actually executes" coverage for the matrix rather than
// something CI skips without AIHUB_TEST_DB.
//
// Modelled on work_items_cancel_gate_test.go deliberately. The matrix's whole
// claim is that update now answers the way cancel does, and two gates asserted
// in two different shapes would make that claim harder to check than to state.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUpdateGate walks every cell of the matrix: 3 tiers × 7 statuses × 4
// actors. The expectations are written per (status, tier) with the actor axis
// folded in only where it matters, which is the point being asserted — the
// actor only ever decides the CONTRACT tier, and only once the state has
// already been accepted.
func TestUpdateGate(t *testing.T) {
	const (
		roleAdmin  = "admin"
		roleMember = "member"

		projMaintainer = "maintainer"
		projWriter     = "writer"
	)

	type actor struct {
		name        string
		isReporter  bool
		callerRole  string
		projectRole string
	}

	actors := []actor{
		{"reporter", true, roleMember, projWriter},
		{"maintainer", false, roleMember, projMaintainer},
		{"admin", false, roleAdmin, ""},
		{"unrelated_writer", false, roleMember, projWriter},
	}

	// wantFor maps actor name -> expected code; an actor missing from the map is
	// expected to be ALLOWED (nil). "*" is a wildcard for every actor and is what
	// a state rejection looks like: no actor can talk its way past it.
	type cell struct {
		status  string
		tier    wiEditTier
		wantFor map[string]ErrCode
	}

	cells := []cell{
		// ── open: queued, paused, blocked ────────────────────────────────────
		// blocked is the widening this change makes: before it, goal and wi_type
		// were refused on a blocked wi. cancelGate already admits blocked for the
		// far more destructive cancel, on the argument that nobody is executing it.
		{"queued", wiTierRecord, nil},
		{"queued", wiTierWorking, nil},
		{"queued", wiTierContract, map[string]ErrCode{"unrelated_writer": ErrForbidden}},
		{"paused", wiTierRecord, nil},
		{"paused", wiTierWorking, nil},
		{"paused", wiTierContract, map[string]ErrCode{"unrelated_writer": ErrForbidden}},
		{"blocked", wiTierRecord, nil},
		{"blocked", wiTierWorking, nil},
		{"blocked", wiTierContract, map[string]ErrCode{"unrelated_writer": ErrForbidden}},

		// ── live: running ────────────────────────────────────────────────────
		// The contract tier is refused for EVERY actor, including the reporter and
		// an admin, and refused as a STATE conflict. Before this change the same
		// refusal came back as 409 GOAL_CHANGE_NOT_ALLOWED for goal and as 403
		// WI_RECLASSIFY_FORBIDDEN for wi_type — a permission status on a state
		// problem, which is what aihub#242 forbids.
		{"running", wiTierRecord, nil},
		{"running", wiTierWorking, nil},
		{"running", wiTierContract, map[string]ErrCode{"*": ErrConflictWIAlreadyClaimed}},

		// ── closed: wrapped, failed, cancelled ───────────────────────────────
		// The record tier stays open here BY DESIGN — see wiEditTierByField for the
		// corpus measurement. The working tier's refusal is the one new rejection
		// this change introduces.
		{"wrapped", wiTierRecord, nil},
		{"wrapped", wiTierWorking, map[string]ErrCode{"*": ErrConflictTerminalState}},
		{"wrapped", wiTierContract, map[string]ErrCode{"*": ErrConflictTerminalState}},
		{"failed", wiTierRecord, nil},
		{"failed", wiTierWorking, map[string]ErrCode{"*": ErrConflictTerminalState}},
		{"failed", wiTierContract, map[string]ErrCode{"*": ErrConflictTerminalState}},
		{"cancelled", wiTierRecord, nil},
		{"cancelled", wiTierWorking, map[string]ErrCode{"*": ErrConflictTerminalState}},
		{"cancelled", wiTierContract, map[string]ErrCode{"*": ErrConflictTerminalState}},
	}

	// Every legal status must appear in the table above for all three tiers, or a
	// cell is untested and this test's name is a lie.
	seen := map[string]map[wiEditTier]bool{}
	for _, c := range cells {
		if seen[c.status] == nil {
			seen[c.status] = map[wiEditTier]bool{}
		}
		seen[c.status][c.tier] = true
	}
	for _, st := range WorkItemStatusValues() {
		for _, tr := range []wiEditTier{wiTierRecord, wiTierWorking, wiTierContract} {
			if !seen[st][tr] {
				t.Errorf("matrix cell (%s, %s) has no case in this table", st, tr)
			}
		}
	}

	for _, c := range cells {
		for _, a := range actors {
			t.Run(c.status+"/"+c.tier.String()+"/"+a.name, func(t *testing.T) {
				got := updateGate(c.status, c.tier, "probe_field", a.isReporter, a.callerRole, a.projectRole)

				wantCode, wantErr := c.wantFor["*"]
				if !wantErr {
					wantCode, wantErr = c.wantFor[a.name]
				}
				if !wantErr {
					if got != nil {
						t.Fatalf("updateGate(%q, %s, %+v) = %s %q; want nil (allowed)",
							c.status, c.tier, a, got.Code, got.Message)
					}
					return
				}
				if got == nil {
					t.Fatalf("updateGate(%q, %s, %+v) = nil; want %s", c.status, c.tier, a, wantCode)
				}
				if got.Code != wantCode {
					t.Fatalf("updateGate(%q, %s, %+v) code = %s; want %s (message: %s)",
						c.status, c.tier, a, got.Code, wantCode, got.Message)
				}
				if !strings.Contains(got.Message, "probe_field") {
					t.Errorf("the refusal must name the field the caller sent; got %q", got.Message)
				}
			})
		}
	}
}

// TestUpdateGateStateIsCheckedBeforePermission is the aihub#242 property stated
// as its own assertion rather than left implicit in the table above: an actor
// who fails BOTH halves must be told about the state, because that is the half
// that no change of caller can fix.
func TestUpdateGateStateIsCheckedBeforePermission(t *testing.T) {
	for _, status := range []string{"running", "wrapped", "failed", "cancelled"} {
		got := updateGate(status, wiTierContract, "goal", false, "member", "writer")
		if got == nil {
			t.Fatalf("status %q: contract edit by an unrelated writer was allowed", status)
		}
		if got.Code == ErrForbidden || got.HTTPStatus == 403 {
			t.Errorf("status %q: a state rejection came back as a permission failure (%s %d) — "+
				"this is the exact conflation aihub#242 removed from cancelGate",
				status, got.Code, got.HTTPStatus)
		}
		if got.HTTPStatus != 409 {
			t.Errorf("status %q: state rejection must be a 409, got %d (%s)", status, got.HTTPStatus, got.Code)
		}
	}
}

// TestTerminalWorkItemKeepsTheAttrsWritePath is the load-bearing exemption,
// named so that closing it is loud rather than incidental.
//
// aihub#411's T2-1 note: "the terminal-wi rule that only attrs may be written
// after wrap is load-bearing for existing tooling — whatever matrix ships must
// keep a documented write path for attrs on a terminal wi, or say explicitly
// that it is closing one." It is kept, and the corpus number behind it is in
// wiEditTierByField's comment: 49 of 49 closed-record update calls in the
// 21-day window carried nothing but attrs / attrs_patch / attrs_unset.
func TestTerminalWorkItemKeepsTheAttrsWritePath(t *testing.T) {
	patches := map[string]*UpdateWorkItemRequest{
		"attrs":       {Attrs: json.RawMessage(`{"owner_decision":"x"}`)},
		"attrs_patch": {AttrsPatch: json.RawMessage(`{"merged_2026_09_08":"y"}`)},
		"attrs_unset": {AttrsUnset: []string{"stale_key"}},
	}
	for _, status := range []string{"wrapped", "failed", "cancelled"} {
		for name, req := range patches {
			tier, field, supplied := strictestSuppliedEditTier(req)
			if !supplied {
				t.Fatalf("%s/%s: the patch supplies a tiered field and was read as empty", status, name)
			}
			if tier != wiTierRecord {
				t.Fatalf("%s/%s: %s is tiered %s, not record — the terminal write path is closed", status, name, field, tier)
			}
			// Asserted for the LEAST privileged caller there is: this path must not
			// quietly become maintainer-only either.
			if got := updateGate(status, tier, field, false, "member", "writer"); got != nil {
				t.Fatalf("%s/%s: refused with %s %q — this write path is load-bearing for "+
					"existing tooling (post-wrap decision and merge records). Closing it is a "+
					"decision that needs stating, not a side effect.", status, name, got.Code, got.Message)
			}
		}
	}
}

// updateFieldProbes builds a request that supplies exactly one tiered field.
// It is the bridge between the matrix and buildWorkItemUpdate, and the reason
// "no field silently exempt" can be checked rather than asserted in prose.
var updateFieldProbes = map[string]func(*UpdateWorkItemRequest){
	"goal":                   func(r *UpdateWorkItemRequest) { r.Goal = strPtr("a new goal") },
	"wi_type":                func(r *UpdateWorkItemRequest) { r.WIType = strPtr("chore") },
	"content":                func(r *UpdateWorkItemRequest) { r.Content = strPtr("body") },
	"labels":                 func(r *UpdateWorkItemRequest) { r.Labels = []string{"l1"} },
	"priority":               func(r *UpdateWorkItemRequest) { r.Priority = strPtr("high") },
	"milestone":              func(r *UpdateWorkItemRequest) { r.Milestone = strPtr("m1") },
	"requires_human_session": func(r *UpdateWorkItemRequest) { b := true; r.RequiresHumanSession = &b },
	"declared_resources":     func(r *UpdateWorkItemRequest) { r.DeclaredResources = json.RawMessage(`[]`) },
	"attrs":                  func(r *UpdateWorkItemRequest) { r.Attrs = json.RawMessage(`{"k":1}`) },
	"attrs_patch":            func(r *UpdateWorkItemRequest) { r.AttrsPatch = json.RawMessage(`{"k":1}`) },
	"attrs_unset":            func(r *UpdateWorkItemRequest) { r.AttrsUnset = []string{"k"} },
}

// TestEveryWritableUpdateFieldHasATier is the "no field silently exempt" gate,
// checked in three directions so that neither half can drift alone:
//
//  1. every json field UpdateWorkItemRequest binds is either tiered or a
//     declared rider — a NEW field is a compile-green, test-red change;
//  2. every tiered field is detected by suppliedEditFields, so a tier entry
//     cannot be decorative;
//  3. every tiered field actually writes a column in buildWorkItemUpdate, so
//     the matrix's row set is the set of writable fields and not a wish list.
//
// (1) is the one that matters. The defect T2-1 records is not that some field
// had the wrong guard, it is that five fields had NO guard and nobody noticed —
// which is what happens when the field list lives only in a comment.
func TestEveryWritableUpdateFieldHasATier(t *testing.T) {
	bound := jsonTagsOf(t, UpdateWorkItemRequest{})

	for field := range bound {
		_, tiered := wiEditTierByField[field]
		if !tiered && !wiEditRiderFields[field] {
			t.Errorf("UpdateWorkItemRequest binds %q, which is in neither wiEditTierByField "+
				"nor wiEditRiderFields. It therefore has NO editability guard and is writable "+
				"in every status — the exact defect aihub#411 T2-1 recorded for five fields. "+
				"Give it a tier in the matrix, or declare it a rider if it writes no column.", field)
		}
		if tiered && wiEditRiderFields[field] {
			t.Errorf("%q is both tiered and declared a rider; it can only be one", field)
		}
	}
	for field := range wiEditTierByField {
		if !bound[field] {
			t.Errorf("the matrix tiers %q but UpdateWorkItemRequest no longer binds it — "+
				"the tier is guarding nothing", field)
		}
	}
	for field := range wiEditRiderFields {
		if !bound[field] {
			t.Errorf("wiEditRiderFields names %q but UpdateWorkItemRequest no longer binds it", field)
		}
	}

	// (2) and (3).
	if len(updateFieldProbes) != len(wiEditTierByField) {
		t.Errorf("updateFieldProbes has %d entries against %d tiered fields; every tiered "+
			"field needs a probe or its cell is untested",
			len(updateFieldProbes), len(wiEditTierByField))
	}
	empty := buildWorkItemUpdate(&UpdateWorkItemRequest{}, "wi_probe").Query
	for field, probe := range updateFieldProbes {
		if _, ok := wiEditTierByField[field]; !ok {
			t.Errorf("updateFieldProbes covers %q, which the matrix does not tier", field)
			continue
		}
		req := &UpdateWorkItemRequest{}
		probe(req)

		got := suppliedEditFields(req)
		if len(got) != 1 || got[0] != field {
			t.Errorf("suppliedEditFields for a %s-only patch = %v; want [%s]. A tiered field "+
				"the supplied-ness scan misses is a field with no guard.", field, got, field)
		}
		if q := buildWorkItemUpdate(req, "wi_probe").Query; q == empty {
			t.Errorf("buildWorkItemUpdate writes no column for %q, so the matrix is tiering a "+
				"field this path cannot write; drop the tier or make it a rider", field)
		}
	}

	// The riders' half of (3): a rider must NOT write a column of its own, or it
	// belongs in the matrix.
	riderProbes := map[string]func(*UpdateWorkItemRequest){
		"goal_change_reason": func(r *UpdateWorkItemRequest) { r.GoalChangeReason = strPtr("because of a reason") },
		"reclassify_reason":  func(r *UpdateWorkItemRequest) { r.ReclassifyReason = strPtr("because of a reason") },
		"resources_version":  func(r *UpdateWorkItemRequest) { v := 3; r.ResourcesVersion = &v },
	}
	for field, probe := range riderProbes {
		req := &UpdateWorkItemRequest{}
		probe(req)
		if got := suppliedEditFields(req); len(got) != 0 {
			t.Errorf("rider %q was read as supplying tiered fields %v", field, got)
		}
		u := buildWorkItemUpdate(req, "wi_probe")
		for _, clause := range strings.Split(strings.TrimPrefix(u.Query, "UPDATE work_items SET "), " WHERE ")[:1] {
			if strings.Count(clause, "=") > 1 {
				t.Errorf("rider %q writes a column (%q); it needs a tier, not a rider entry", field, clause)
			}
		}
	}
	if len(riderProbes) != len(wiEditRiderFields) {
		t.Errorf("riderProbes has %d entries against %d declared riders", len(riderProbes), len(wiEditRiderFields))
	}
}

// TestStrictestSuppliedEditTierGovernsTheWholePatch pins the mixing rule: one
// contract field drags an otherwise-harmless patch into the contract tier, and
// one working field drags a record-only patch out of the terminal exemption.
// Both directions matter — the second is what stops `attrs_patch` being used as
// a carrier to smuggle a `labels` write onto a wrapped work item.
func TestStrictestSuppliedEditTierGovernsTheWholePatch(t *testing.T) {
	cases := []struct {
		name      string
		req       *UpdateWorkItemRequest
		wantTier  wiEditTier
		wantField string
	}{
		{
			name:      "record_only",
			req:       &UpdateWorkItemRequest{AttrsPatch: json.RawMessage(`{"a":1}`)},
			wantTier:  wiTierRecord,
			wantField: "attrs_patch",
		},
		{
			name:      "record_plus_working",
			req:       &UpdateWorkItemRequest{AttrsPatch: json.RawMessage(`{"a":1}`), Labels: []string{"x"}},
			wantTier:  wiTierWorking,
			wantField: "labels",
		},
		{
			name:      "working_plus_contract",
			req:       &UpdateWorkItemRequest{Content: strPtr("body"), WIType: strPtr("chore")},
			wantTier:  wiTierContract,
			wantField: "wi_type",
		},
		{
			name:      "all_three_tiers",
			req:       &UpdateWorkItemRequest{Attrs: json.RawMessage(`{}`), Priority: strPtr("high"), Goal: strPtr("g")},
			wantTier:  wiTierContract,
			wantField: "goal",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tier, field, supplied := strictestSuppliedEditTier(tc.req)
			if !supplied {
				t.Fatalf("supplied=false for %+v", tc.req)
			}
			if tier != tc.wantTier {
				t.Errorf("tier = %s; want %s", tier, tc.wantTier)
			}
			if field != tc.wantField {
				t.Errorf("field = %q; want %q", field, tc.wantField)
			}
		})
	}

	// A patch of riders only, and an empty patch, must report supplied=false —
	// otherwise a CAS-token-only or reason-only request would be gated on a field
	// it never sent.
	for name, req := range map[string]*UpdateWorkItemRequest{
		"empty":       {},
		"riders_only": {GoalChangeReason: strPtr("because of a reason"), ResourcesVersion: func() *int { v := 1; return &v }()},
	} {
		if _, field, supplied := strictestSuppliedEditTier(req); supplied {
			t.Errorf("%s: supplied=true naming %q; a patch that writes no tiered field must not be gated", name, field)
		}
	}
}

// TestEveryWorkItemStatusIsClassified stops the matrix going stale when a status
// is added to the work_items.status CHECK. The status vocabulary itself is
// pinned against the migration by TestWorkItemVocabulariesMatchTheMigrations, so
// this test inherits that authority rather than restating the list.
func TestEveryWorkItemStatusIsClassified(t *testing.T) {
	for _, st := range WorkItemStatusValues() {
		if wiStatusClassOf(st) == wiStatusUnknown {
			t.Errorf("status %q is legal but has no class in wiStatusClassOf, so every field "+
				"tier's behaviour on it is decided by the fail-closed default. Classify it in "+
				"the matrix and add its row to TestUpdateGate.", st)
		}
	}

	// Negative control: without this the test above would pass on a
	// wiStatusClassOf that returned wiStatusOpen for literally anything.
	if wiStatusClassOf("not_a_status") != wiStatusUnknown {
		t.Error("wiStatusClassOf classified a status that does not exist; the fail-closed " +
			"default is not reachable and the test above proves nothing")
	}
	// And the fail-closed default must actually refuse, for both guarded tiers.
	for _, tier := range []wiEditTier{wiTierWorking, wiTierContract} {
		got := updateGate("not_a_status", tier, "goal", true, "admin", "maintainer")
		if got == nil || got.HTTPStatus != 409 {
			t.Errorf("an unclassified status must fail closed with a 409 for the %s tier; got %v", tier, got)
		}
	}
}

// TestRetiredErrCodesAreNotProducedByTheUpdatePath is the gate named in
// errors.go: the two retired codes are kept declared so stale callers and doc
// rows still resolve, and keeping them declared is only safe if nothing can
// quietly start producing them again.
//
// A source scan rather than a behavioural assertion on purpose: the claim is
// "no producer anywhere in this package", and no finite set of gate calls can
// establish that.
func TestRetiredErrCodesAreNotProducedByTheUpdatePath(t *testing.T) {
	retired := []string{"ErrGoalChangeNotAllowed", "ErrWIReclassifyForbidden"}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "errors.go" {
			// errors.go declares them and documents the retirement; that is the one
			// legal mention.
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		scanned++
		for _, code := range retired {
			if strings.Contains(string(src), code) {
				t.Errorf("%s mentions %s, which aihub#440 retired. Wrong-state refusals are "+
					"409 CONFLICT_WI_ALREADY_CLAIMED / CONFLICT_TERMINAL_STATE and wrong-caller "+
					"refusals are 403 FORBIDDEN; see wiEditTierByField. If this code is being "+
					"revived, delete its retirement comment in errors.go in the same change.", f, code)
			}
		}
	}
	if scanned < 10 {
		t.Fatalf("scanned only %d non-test files in this package; the glob is broken and this "+
			"test would pass on an empty set", scanned)
	}
}

// TestOnlyTheAttrsFieldsAreExemptOnATerminalWorkItem and
// TestOnlyGoalAndWITypeCarryAPermissionGate pin the ASSIGNMENTS in
// wiEditTierByField, which the tests above deliberately do not: TestUpdateGate
// asserts what each TIER does, so moving `labels` into the record tier would
// leave every one of its subtests green while silently reopening a terminal work
// item to label writes.
//
// They are written as properties over the whole field set rather than as a
// second copy of the map, so they catch a field moving in either direction and
// a field being added without a decision about it.
func TestOnlyTheAttrsFieldsAreExemptOnATerminalWorkItem(t *testing.T) {
	exempt := map[string]bool{"attrs": true, "attrs_patch": true, "attrs_unset": true}

	for field, tier := range wiEditTierByField {
		got := updateGate("wrapped", tier, field, true, "admin", "maintainer")
		if exempt[field] {
			if got != nil {
				t.Errorf("%q must stay writable on a terminal work item; refused with %s %q",
					field, got.Code, got.Message)
			}
			continue
		}
		if got == nil {
			t.Errorf("%q is writable on a wrapped work item. Only attrs, attrs_patch and "+
				"attrs_unset are exempt (aihub#411 T2-1's load-bearing write path); every other "+
				"field must answer 409 CONFLICT_TERMINAL_STATE. If this field was moved into the "+
				"record tier on purpose, that is a decision about what a closed record means and "+
				"belongs in wiEditTierByField's comment.", field)
		} else if got.Code != ErrConflictTerminalState {
			t.Errorf("%q on a wrapped work item answered %s; want CONFLICT_TERMINAL_STATE", field, got.Code)
		}
	}
}

func TestOnlyGoalAndWITypeCarryAPermissionGate(t *testing.T) {
	gated := map[string]bool{"goal": true, "wi_type": true}

	for field, tier := range wiEditTierByField {
		// An unrelated project writer: passes the route-level "writer" check that
		// every PATCH goes through, and holds none of the three grants.
		got := updateGate("queued", tier, field, false, "member", "writer")
		if gated[field] {
			if got == nil {
				t.Errorf("%q is a contract field and must require reporter/maintainer/admin; "+
					"an unrelated writer was allowed", field)
			} else if got.Code != ErrForbidden {
				t.Errorf("%q refused an unrelated writer with %s; a wrong-caller refusal is "+
					"403 FORBIDDEN", field, got.Code)
			}
			continue
		}
		if got != nil {
			t.Errorf("%q refused an unrelated project writer with %s %q. Only goal and wi_type "+
				"carry a field-level permission gate; everything else is authorized by the "+
				"route-level writer check. Adding a gate here narrows who may update a work "+
				"item and is a decision, not a detail.", field, got.Code, got.Message)
		}
	}
}
