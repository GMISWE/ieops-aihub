package clampdisclosure

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot walks up from the test's working directory to the module root,
// identified by go.mod. Same shape as internal/citest/rowserr: a marker file
// rather than a fixed number of "..", so moving this package does not silently
// point the gate at a subtree.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find go.mod above the test's working directory; this gate cannot have run")
	return ""
}

// ─── The gate ───────────────────────────────────────────────────────────────

// TestEveryClampDisclosesOrCarriesANamedWaiver is the aihub#532 gate.
//
// It is a MECHANISM gate. It names no parameter and no endpoint: it asks "does a
// clamp exist that neither discloses nor is accounted for", which is a question
// a clamp added next month cannot answer differently. The two clamps that
// prompted it were both found by a person reading the tree, and a person reading
// the tree is not a gate.
func TestEveryClampDisclosesOrCarriesANamedWaiver(t *testing.T) {
	root := repoRoot(t)
	sites, err := ScanDir(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}
	callerParams, err := CallerParamNames(root)
	if err != nil {
		t.Fatalf("reading the caller-parameter vocabulary: %v", err)
	}
	for _, v := range Violations(sites, Waivers(), ScopeNotes(), callerParams) {
		t.Errorf("%s", v)
	}
	for _, p := range LedgerProblems(Waivers(), ScopeNotes()) {
		t.Errorf("ledger: %s", p)
	}
}

// TestScannerStillSeesEveryKnownClampSite is why a clean report above means
// anything.
//
// The gate passes when it finds nothing WRONG, and it also passes when it finds
// nothing AT ALL. Those are opposite facts with the same exit code, and
// narrowing the recogniser produces the second one silently. So the floor is
// asserted separately, along with the three buckets: a classifier that put
// everything in one bucket would satisfy the gate and measure nothing.
//
// The numbers are floors, deliberately under the 18 sites present when
// aihub#532 landed, because clamps are legitimately added and deleted and a gate
// pinned to an exact count is a gate somebody edits to ship anything.
func TestScannerStillSeesEveryKnownClampSite(t *testing.T) {
	const floor = 12

	root := repoRoot(t)
	sites, err := ScanDir(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}
	if len(sites) < floor {
		t.Fatalf("the scanner found only %d clamps in the repo, expected at least %d.\n"+
			"    This gate reports violations, so seeing NOTHING and seeing nothing WRONG have the "+
			"same exit code. A drop this large means the recogniser stopped recognising clamps "+
			"(clampsIn / minMaxClamp), not that the repo stopped bounding values.", len(sites), floor)
	}

	waivers, notes := Waivers(), ScopeNotes()
	var disclosed, waived, scoped int
	for _, s := range sites {
		switch {
		case s.Disclosed:
			disclosed++
		case waiverFor(waivers, s):
			waived++
		case noteFor(notes, s):
			scoped++
		}
	}
	if disclosed == 0 {
		t.Errorf("no clamp in the repo was resolved as DISCLOSED, but three are "+
			"(normalizeRecallTopK, NormalizeListWorkItemsLimit, newReadyQueue). The disclosure "+
			"resolver is not working, which would make every disclosing site look like a "+
			"violation — and a gate that cries wolf gets switched off. Found %d clamps.", len(sites))
	}
	if waived == 0 {
		t.Errorf("no clamp matched a waiver, but disclosureWaivers has %d entries. Either the "+
			"keys have drifted from the sites or the lookup is broken; a waiver that matches "+
			"nothing protects nothing.", len(waivers))
	}
	if scoped == 0 {
		t.Errorf("no clamp matched a scope note, but clampsOutsideTheConvention has %d entries.",
			len(notes))
	}
}

func waiverFor(waivers map[string]Waiver, s Site) bool { _, ok := waivers[s.Key()]; return ok }
func noteFor(notes map[string]ScopeNote, s Site) bool  { _, ok := notes[s.Key()]; return ok }

// TestCallerParamVocabularyStillNamesTheDisclosedParameters is the liveness
// check on the one derived input the gate has.
//
// CallerParamNames reads internal/server for the literals its query readers are
// called with. If that package moved, or the readers were renamed, the set would
// come back EMPTY — and an empty vocabulary silently disarms the check that stops
// a page size from being declared out of scope. The floor is the three
// parameters the convention actually discloses today.
func TestCallerParamVocabularyStillNamesTheDisclosedParameters(t *testing.T) {
	params, err := CallerParamNames(repoRoot(t))
	if err != nil {
		t.Fatalf("reading the caller-parameter vocabulary: %v", err)
	}
	for _, want := range []string{"limit", "top_k", "max"} {
		if !params[normalizeParam(want)] {
			t.Errorf("the caller-parameter vocabulary does not contain %q, a parameter this "+
				"server clamps and discloses today (%d names found). A vocabulary that lost its "+
				"contents disarms the ScopeNote check without failing anything.", want, len(params))
		}
	}
}

// TestGateArmIsWiredToTheEnumeratorAndTheLedger is the M9 arm.
//
// 🔴 It has to exist, and reading the gate above is not a substitute. Every other
// arm here would still pass if TestEveryClampDisclosesOrCarriesANamedWaiver
// stopped calling Violations: the scanner would still scan, the ledger would
// still validate on fixtures, and the one test that decides whether a change
// lands would be green over an unenumerated tree. Same shape as
// internal/mcp/contract_cards_gate_test.go
// (TestOpenCitationWaiverCheckIsWiredIntoTheArm) — the failure mode of a gate
// built from parts is a part quietly disconnected, not a part that breaks.
func TestGateArmIsWiredToTheEnumeratorAndTheLedger(t *testing.T) {
	const armName = "TestEveryClampDisclosesOrCarriesANamedWaiver"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "clampdisclosure_test.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse clampdisclosure_test.go: %v", err)
	}
	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == armName && fn.Recv == nil {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatalf("%s not found in clampdisclosure_test.go — this test cannot be green by not "+
			"finding its subject", armName)
	}
	called := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok {
				called[id.Name] = true
			}
		}
		return true
	})
	for _, need := range []string{"ScanDir", "CallerParamNames", "Violations", "LedgerProblems"} {
		if !called[need] {
			t.Errorf("%s does not call %s.\n"+
				"    The gate is an enumerator plus a classifier plus a ledger validator, and a "+
				"gate assembled from parts fails by having a part disconnected. Without this call "+
				"the arm is green over a tree nobody enumerated.", armName, need)
		}
	}
}

// ─── The recogniser, on fixtures ────────────────────────────────────────────
//
// Every arm below is a source shape whose verdict is known by construction, so
// the recogniser is measured rather than trusted. Both directions are covered on
// purpose: arms proving it FLAGS a clamp, and arms proving it IGNORES the two
// shapes that share a clamp's operands. A recogniser that reports everything and
// one that reports nothing each pass half of this set.

func scan(t *testing.T, src string) []Site {
	t.Helper()
	sites, err := ScanSource([]byte(src), "fixture.go")
	if err != nil {
		t.Fatalf("scanning fixture: %v", err)
	}
	return sites
}

func TestRecognisesTheThreeClampSpellings(t *testing.T) {
	for _, tc := range []struct {
		name      string
		src       string
		wantValue string
		wantBound string
		wantForm  Form
	}{
		{
			name: "if-assign",
			src: `package p
func f(v int) int {
	if v > ceiling {
		v = ceiling
	}
	return v
}`,
			wantValue: "v", wantBound: "ceiling", wantForm: FormIfAssign,
		},
		{
			name: "if-return",
			src: `package p
func f(v int) int {
	if v > ceiling {
		return ceiling
	}
	return v
}`,
			wantValue: "v", wantBound: "ceiling", wantForm: FormIfReturn,
		},
		{
			// The cheapest way around a gate keyed on `if` is to write the same
			// clamp as one call, so the recogniser has to see this spelling or
			// the gate is one refactor from blind.
			name: "min call",
			src: `package p
func f(v int) int {
	v = min(v, ceiling)
	return v
}`,
			wantValue: "v", wantBound: "ceiling", wantForm: FormMinMax,
		},
		{
			name: "max call in a return",
			src: `package p
func f(v int) int {
	return max(v, floorValue)
}`,
			wantValue: "v", wantBound: "floorValue", wantForm: FormMinMax,
		},
		{
			// A clamp with a companion statement is still a clamp. If the
			// recogniser required a lone statement, adding a log line would be
			// a way out of the gate.
			name: "if-assign beside another statement",
			src: `package p
func f(v int) int {
	if v > ceiling {
		note("bounded")
		v = ceiling
	}
	return v
}`,
			wantValue: "v", wantBound: "ceiling", wantForm: FormIfAssign,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sites := scan(t, tc.src)
			if len(sites) != 1 {
				t.Fatalf("got %d sites, want 1: %v", len(sites), sites)
			}
			got := sites[0]
			if got.Value != tc.wantValue || got.Bound != tc.wantBound || got.Form != tc.wantForm {
				t.Errorf("got value=%q bound=%q form=%q, want %q/%q/%q",
					got.Value, got.Bound, got.Form, tc.wantValue, tc.wantBound, tc.wantForm)
			}
		})
	}
}

// TestIgnoresTheShapesThatShareAClampsOperands is the discriminator's real
// content. Both of these are in the tree, and a recogniser that flagged them
// would report eleven false violations — and the cheapest way to silence a gate
// that cries wolf is to delete the gate.
func TestIgnoresTheShapesThatShareAClampsOperands(t *testing.T) {
	for _, tc := range []struct{ name, src, why string }{
		{
			name: "default backfill",
			src: `package p
func f(v int) int {
	if v <= 0 {
		v = 20
	}
	return v
}`,
			why: "the value compared against (0) is not the value assigned (20), so nothing the " +
				"caller sent was changed — there was nothing there. RecallWithVector's topK<=0 is this.",
		},
		{
			name: "max tracking",
			src: `package p
func f(v int) {
	if v > peak {
		peak = v
	}
}`,
			why: "the assignment moves the BOUND, not the value. Same two operands as a clamp, " +
				"roles swapped; internal/server/idempotency.go's PeakBytes is this.",
		},
		{
			name: "bounded read of a different variable",
			src: `package p
func f(v int) int {
	if v > ceiling {
		other = ceiling
	}
	return v
}`,
			why: "assigning something else leaves v untouched, so no caller value was substituted",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if sites := scan(t, tc.src); len(sites) != 0 {
				t.Errorf("reported %v as a clamp; it is not: %s", sites, tc.why)
			}
		})
	}
}

// TestDisclosureIsRecognisedInAllThreeSpellings covers the three ways a function
// in this repo says "I told the caller". A resolver that knew only one of them
// would report the sites using the other two as violations.
func TestDisclosureIsRecognisedInAllThreeSpellings(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{
			name: "appendIntAdjustment call",
			src: `package p
func f(v int) int {
	if v > ceiling {
		v = ceiling
	}
	out = appendIntAdjustment(out, "limit", 0, v)
	return v
}`,
		},
		{
			name: "assignment to a RequestAdjusted field",
			src: `package p
func f(v int) int {
	if v > ceiling {
		v = ceiling
	}
	res.RequestAdjusted = entries
	return v
}`,
		},
		{
			name: "RequestAdjusted key in a composite literal",
			src: `package p
func f(v int) *R {
	if v > ceiling {
		v = ceiling
	}
	return &R{RequestAdjusted: entries}
}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sites := scan(t, tc.src)
			if len(sites) != 1 {
				t.Fatalf("got %d sites, want 1", len(sites))
			}
			if !sites[0].Disclosed {
				t.Errorf("the clamp reads as undisclosed, but this function discloses in the "+
					"%s spelling", tc.name)
			}
		})
	}
}

// TestDisclosureResolvesAtHopTwo is the widening aihub#532 needed.
//
// aihub#432 established the shape at hop 1: newReadyQueue clamps and appends in
// one function. The other two disclosing sites are SPLIT — Recall appends around
// normalizeRecallTopK, ListWorkItems around NormalizeListWorkItemsLimit — and a
// hop-1-only resolver would report both as violations while they are the two
// correct examples in the tree.
func TestDisclosureResolvesAtHopTwo(t *testing.T) {
	const src = `package p
func normalize(requested int) int {
	if requested > ceiling {
		return ceiling
	}
	return requested
}
func List(f Filter) *Result {
	requested := f.Limit
	f.Limit = normalize(f.Limit)
	res := run(f)
	res.RequestAdjusted = appendIntAdjustment(res.RequestAdjusted, "limit", requested, f.Limit)
	return res
}`
	sites := scan(t, src)
	if len(sites) != 1 {
		t.Fatalf("got %d sites, want 1: %v", len(sites), sites)
	}
	if !sites[0].Disclosed {
		t.Error("the clamp in normalize reads as undisclosed. Its caller List appends to " +
			"request_adjusted, which is exactly how Recall and ListWorkItems disclose; without " +
			"hop 2 the gate reports the two correct sites in the tree.")
	}
}

// TestHopTwoRequiresTheCallerToActuallyDisclose is the other half. Hop 2 must
// not degrade into "somebody calls this function", which every function in the
// repo satisfies.
func TestHopTwoRequiresTheCallerToActuallyDisclose(t *testing.T) {
	const src = `package p
func normalize(requested int) int {
	if requested > ceiling {
		return ceiling
	}
	return requested
}
func List(f Filter) *Result {
	f.Limit = normalize(f.Limit)
	return run(f)
}`
	sites := scan(t, src)
	if len(sites) != 1 {
		t.Fatalf("got %d sites, want 1", len(sites))
	}
	if sites[0].Disclosed {
		t.Error("the clamp reads as disclosed, but its only caller discloses nothing. Hop 2 has " +
			"collapsed into \"is called by anything\", which every function satisfies.")
	}
}

// ─── The classifier ────────────────────────────────────────────────────────

func fixtureSite(value string, disclosed bool) Site {
	return Site{File: "fixture.go", Line: 7, Func: "f", Value: value, Bound: "ceiling",
		Form: FormIfAssign, Disclosed: disclosed}
}

// TestViolationsReportsAnUnclassifiedClamp is mutation ① of aihub#532: a new
// clamp that neither discloses nor is accounted for must go RED and must NAME
// the site, because a gate that says "something is wrong" costs more to act on
// than it saves.
func TestViolationsReportsAnUnclassifiedClamp(t *testing.T) {
	s := fixtureSite("pageSize", false)
	got := Violations([]Site{s}, map[string]Waiver{}, map[string]ScopeNote{}, map[string]bool{})
	if len(got) != 1 {
		t.Fatalf("got %d violations, want 1: %v", len(got), got)
	}
	for _, want := range []string{"fixture.go", "pageSize", "request_adjusted", s.Key()} {
		if !strings.Contains(got[0].String(), want) {
			t.Errorf("the violation does not mention %q, so a reader cannot act on it:\n%s",
				want, got[0])
		}
	}
}

func TestViolationsAcceptsTheThreeLegalStates(t *testing.T) {
	scoped := fixtureSite("budget", false)
	waived := fixtureSite("newStrength", false)
	sites := []Site{fixtureSite("requested", true), scoped, waived}
	notes := map[string]ScopeNote{scoped.Key(): {Origin: "a per-parse work budget"}}
	waivers := map[string]Waiver{waived.Key(): {
		Kind: KindAcceptedContract, Param: "strength_delta", Reason: "owner kept it",
		Decided: "2026-09-09", Citation: "aihub#506",
	}}
	if got := Violations(sites, waivers, notes, map[string]bool{}); len(got) != 0 {
		t.Errorf("a disclosed clamp, a scope-noted clamp and a waived clamp are all legal, "+
			"but the classifier reported: %v", got)
	}
}

// TestViolationsRefusesAScopeNoteOnAPublishedParameter is the anti-rubber-stamp
// arm.
//
// The cheapest way past this gate is one line in clampsOutsideTheConvention, and
// an escape hatch that costs less than compliance is the one that gets used. So
// the exit is CLOSED for the population that matters: a clamp on something
// spelled like a published request parameter cannot be declared out of scope,
// whatever the note says.
func TestViolationsRefusesAScopeNoteOnAPublishedParameter(t *testing.T) {
	s := fixtureSite("topK", false)
	notes := map[string]ScopeNote{s.Key(): {Origin: "an internal page size"}}
	params := map[string]bool{normalizeParam("top_k"): true}
	got := Violations([]Site{s}, map[string]Waiver{}, notes, params)
	if len(got) != 1 {
		t.Fatalf("a scope note on a clamp of `topK` was accepted while `top_k` is a published "+
			"parameter; got %d violations: %v", len(got), got)
	}
	if !strings.Contains(got[0].String(), "PUBLISHED REQUEST PARAMETER") {
		t.Errorf("the violation does not say why the note was refused:\n%s", got[0])
	}

	// And it must not fire on everything: `budget` is not a parameter anybody
	// sends, and a check that refused every scope note would make the eleven
	// legitimate ones unwritable.
	other := fixtureSite("budget", false)
	notes = map[string]ScopeNote{other.Key(): {Origin: "a per-parse work budget"}}
	if got := Violations([]Site{other}, map[string]Waiver{}, notes, params); len(got) != 0 {
		t.Errorf("a scope note on a clamp of `budget` was refused; the vocabulary check has "+
			"stopped discriminating: %v", got)
	}
}

func TestViolationsReportsAStaleLedgerEntry(t *testing.T) {
	waivers := map[string]Waiver{"gone.go:f:v": {
		Kind: KindStructural, Param: "limit", Reason: "r", Decided: "2026-09-09", Citation: "aihub#1",
	}}
	notes := map[string]ScopeNote{"also-gone.go:g:w": {Origin: "o"}}
	got := Violations(nil, waivers, notes, map[string]bool{})
	if len(got) != 2 {
		t.Fatalf("got %d violations, want 2 (one per stale entry): %v", len(got), got)
	}
	// A stale entry is not cosmetic: it exempts whatever lands in that function
	// next, without anybody deciding to.
	for _, v := range got {
		if !strings.Contains(v.Problem, "no longer exists") {
			t.Errorf("stale-entry violation does not say what is wrong:\n%s", v)
		}
	}
}

func TestViolationsReportsASiteInBothTables(t *testing.T) {
	s := fixtureSite("v", false)
	waivers := map[string]Waiver{s.Key(): {
		Kind: KindStructural, Param: "limit", Reason: "r", Decided: "2026-09-09", Citation: "aihub#1",
	}}
	notes := map[string]ScopeNote{s.Key(): {Origin: "o"}}
	got := Violations([]Site{s}, waivers, notes, map[string]bool{})
	if len(got) != 1 || !strings.Contains(got[0].Problem, "BOTH ledger tables") {
		t.Fatalf("a site claimed to be both a caller value and not one was accepted: %v", got)
	}
}

// TestViolationsReportsALedgerEntryOnADisclosedClamp closes the direction a
// ledger rots in. A site that starts disclosing does not need its waiver, and a
// waiver left behind outlives the reason it was written.
func TestViolationsReportsALedgerEntryOnADisclosedClamp(t *testing.T) {
	s := fixtureSite("v", true)
	waivers := map[string]Waiver{s.Key(): {
		Kind: KindAcceptedContract, Param: "limit", Reason: "r", Decided: "2026-09-09", Citation: "aihub#1",
	}}
	got := Violations([]Site{s}, waivers, map[string]ScopeNote{}, map[string]bool{})
	if len(got) != 1 || !strings.Contains(got[0].Problem, "false") {
		t.Fatalf("a waiver on a clamp that now discloses was accepted: %v", got)
	}
}

// ─── The ledger's own fields ────────────────────────────────────────────────

func TestLedgerProblemsRejectsAnIncompleteWaiver(t *testing.T) {
	full := Waiver{Kind: KindStructural, Param: "limit", Reason: "r", Decided: "2026-09-09",
		Citation: "aihub#340"}
	for _, tc := range []struct {
		name  string
		edit  func(w Waiver) Waiver
		wants string
	}{
		{"no kind", func(w Waiver) Waiver { w.Kind = "invented"; return w }, "not one of"},
		{"no param", func(w Waiver) Waiver { w.Param = " "; return w }, "names no Param"},
		{"no reason", func(w Waiver) Waiver { w.Reason = ""; return w }, "states no Reason"},
		{"no citation", func(w Waiver) Waiver { w.Citation = ""; return w }, "cites nothing"},
		{"no date", func(w Waiver) Waiver { w.Decided = ""; return w }, "YYYY-MM-DD"},
		{"a year is not a date", func(w Waiver) Waiver { w.Decided = "2026"; return w }, "YYYY-MM-DD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems := LedgerProblems(map[string]Waiver{"k": tc.edit(full)}, nil)
			if len(problems) == 0 {
				t.Fatalf("accepted a waiver with %s", tc.name)
			}
			if !strings.Contains(strings.Join(problems, "\n"), tc.wants) {
				t.Errorf("problems do not mention %q: %v", tc.wants, problems)
			}
		})
	}
	if problems := LedgerProblems(map[string]Waiver{"k": full}, nil); len(problems) != 0 {
		t.Errorf("a complete waiver was rejected: %v", problems)
	}
}

func TestLedgerProblemsRejectsAnOriginlessScopeNote(t *testing.T) {
	if problems := LedgerProblems(nil, map[string]ScopeNote{"k": {}}); len(problems) == 0 {
		t.Error("accepted a scope note that says nothing about where the value came from. The " +
			"whole content of a scope note is that statement; without it the entry only records " +
			"that somebody looked.")
	}
}

// TestWaiverKindsCarryDifferentObligations is aihub#411 T1-12's "the two
// exemptions differ in kind" made mechanical.
//
// 🔴 A kind that demanded nothing extra would be a decorative label: any entry
// could be relabelled as any other kind and no test would notice, and the
// distinction that told ReadyQueue's structural-and-closed gap apart from
// reinforce's accepted-and-live divergence would be back in prose only. So the
// obligations are asserted to DIFFER: pending-adjudication must name a work
// item, and the other two must not be forced to.
func TestWaiverKindsCarryDifferentObligations(t *testing.T) {
	base := Waiver{Param: "top_k", Reason: "r", Decided: "2026-09-09",
		Citation: "a design note with no work item in it"}

	pending := base
	pending.Kind = KindPendingAdjudication
	problems := LedgerProblems(map[string]Waiver{"k": pending}, nil)
	if len(problems) == 0 {
		t.Error("a pending-adjudication waiver whose citation names no work item was accepted. " +
			"\"Pending\" with nobody asked is the silence this gate exists to end, wearing the " +
			"gate's own badge.")
	}

	for _, kind := range []WaiverKind{KindAcceptedContract, KindStructural} {
		settled := base
		settled.Kind = kind
		if problems := LedgerProblems(map[string]Waiver{"k": settled}, nil); len(problems) != 0 {
			t.Errorf("kind %s was held to the work-item requirement: %v.\n"+
				"    Then the three kinds impose the same thing and the label carries no "+
				"information — which is the state aihub#411 T1-12 §6.1 was written about.", kind, problems)
		}
	}

	// And the prose has to be distinct, because it is what a reader sees in the
	// failure text instead of the label.
	seen := map[string]WaiverKind{}
	for _, kind := range []WaiverKind{KindAcceptedContract, KindStructural, KindPendingAdjudication} {
		d := kind.Describe()
		if d == "" {
			t.Errorf("kind %s describes itself as nothing", kind)
		}
		if other, dup := seen[d]; dup {
			t.Errorf("kinds %s and %s describe themselves identically", kind, other)
		}
		seen[d] = kind
	}
}

// ─── What the ledger says about the real tree ───────────────────────────────

// TestTheSeedWaiverIsTheReinforceSaturation is mutation ② of aihub#532: delete
// the seed and the clamp is still there, so the gate must go red. That happens
// through the arm at the top of this file; what this arm adds is the two facts
// about the seed that a re-derivation would get wrong.
//
// First, its KIND. aihub#506's ruling declined disclosure — "Option 1: KEEP the
// clamp, write it into the contract … No 400, no response-shape change" — so it
// is accepted-contract and not structural. Recording it as structural would say
// it is waiting for a field that does not exist, which is ReadyQueue's story,
// not this one.
//
// Second, that ONE entry covers BOTH bounds. The saturation is two `if`s, and
// the ruling was about the saturation.
func TestTheSeedWaiverIsTheReinforceSaturation(t *testing.T) {
	root := repoRoot(t)
	sites, err := ScanFile(filepath.Join(root, "internal", "server", "routes_memory.go"),
		"internal/server/routes_memory.go")
	if err != nil {
		t.Fatalf("scanning routes_memory.go: %v", err)
	}
	var saturation []Site
	for _, s := range sites {
		if s.Func == "handleReinforceMemory" {
			saturation = append(saturation, s)
		}
	}
	if len(saturation) != 2 {
		t.Fatalf("found %d clamps in handleReinforceMemory, want 2 (the Max ceiling and the Min "+
			"floor): %v", len(saturation), saturation)
	}
	if saturation[0].Key() != saturation[1].Key() {
		t.Errorf("the two bounds of one saturation have different ledger keys (%q, %q), so the "+
			"aihub#506 ruling would have to be recorded twice — and two copies of one decision "+
			"drift. See Site.Key.", saturation[0].Key(), saturation[1].Key())
	}
	w, ok := Waivers()[saturation[0].Key()]
	if !ok {
		t.Fatalf("the reinforce saturation (%s) carries no waiver. It is the one live accepted "+
			"exemption from aihub#314's convention (aihub#506, 2026-09-09) and the seed this "+
			"ledger was created to hold.", saturation[0].Key())
	}
	if w.Kind != KindAcceptedContract {
		t.Errorf("the reinforce waiver has kind %s, want %s. The owner was asked and declined "+
			"disclosure, so nothing here is waiting to be closed — %s is ReadyQueue's story "+
			"(aihub#432 closed it), not this one.", w.Kind, KindAcceptedContract, KindStructural)
	}
	if w.Param != "strength_delta" {
		t.Errorf("the reinforce waiver names Param=%q; the caller-facing parameter is "+
			"strength_delta", w.Param)
	}
}

// TestThePendingSiteIsRegisteredRatherThanQuietlyWaived pins the census's one
// real finding in the state it is actually in.
//
// RecallWithVector clamps top_k a second time, after normalizeRecallTopK has
// already bounded it and Recall has already disclosed it. Nobody has ruled on
// what should happen to it, and aihub#532 did not rule either — the wi carries
// the question. This arm is what stops the entry from being upgraded to
// "accepted" by an editor who does not know it was never adjudicated: changing
// the kind means changing a test that says why.
func TestThePendingSiteIsRegisteredRatherThanQuietlyWaived(t *testing.T) {
	const key = "internal/domain/memory_vector.go:RecallWithVector:topK"
	w, ok := Waivers()[key]
	if !ok {
		// It may legitimately be gone — that is one of the three answers. But
		// then it must be gone from the tree too, which the arm at the top of
		// this file checks.
		t.Skipf("%s is no longer in the ledger; if the clamp is also gone, the question aihub#532 "+
			"raised has been answered", key)
	}
	if w.Kind != KindPendingAdjudication {
		t.Errorf("the second top_k ceiling is recorded as %s. aihub#532 found it and did NOT "+
			"adjudicate it: the choice between deleting it, disclosing it, and keeping it as "+
			"defence-in-depth belongs to whoever owns the convention. If that call has now been "+
			"made, cite it here — a kind is a claim about who decided.", w.Kind)
	}
	if !mentionsWorkItem(w.Citation) {
		t.Errorf("the pending entry cites %q, which names no work item. A pending question with "+
			"nowhere to look is the silence, not the fix.", w.Citation)
	}
}
