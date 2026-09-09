package mcp

// aihub#469 — pf_recall's hop-4 gate: a published parameter must be READ by the
// ranking code, not merely bound into the request struct.
//
// ─── What shipped, and why every existing gate was green on it ──────────────
//
// `recency_weight` was published (recallSchema), forwarded (recallNumberParams),
// and bound (handleRecall -> domain.RecallRequest.RecencyWeight) — and read by
// nothing. `grep -rn RecencyWeight internal/domain --include='*.go'` outside the
// struct declaration and one NOTE comment returned zero reads. Passing any value
// returned the same page as passing none, with no error and no warning.
//
// Each gate in this package quantifies over a hop that was intact:
//
//	recall_params_wiring_test.go   hops 1-2  schema subset of forwarded — GREEN, it was forwarded
//	recall_wire_query_test.go      hop 2-3   the value reaches the wire  — GREEN, it did
//	universal_contract_gate_test.go G4       server binds a name no tool can reach
//	                                         — GREEN, the tool DID publish it
//	contract_cards_gate_test.go    K4        the param is named in the card's prose
//	                                         — GREEN, a bare table row satisfies it
//
// The unmeasured hop was the last one: nothing asked whether the field the
// handler fills is ever consumed. That is this file.
//
// ─── Why the quantifier is "published", and what its blind spot is ──────────
//
// This gate quantifies over PUBLISHED parameters, so withdrawing one removes it
// from view — the same blind spot aihub#424 documented for `mode`, which survived
// aihub#394's withdrawal as a bound-but-unpublished field. The complement is
// aihub#419's G4 (server binds a name no tool can reach), which is the quantifier
// that does not shrink on withdrawal. Neither alone is a gate for both
// directions; they are a pair, and this comment is the pointer between them.
//
// ─── Why an AST scan rather than grep ──────────────────────────────────────
//
// A string search over internal/domain cannot tell a read from a mention, and
// `RecencyWeight` appeared in a prose NOTE saying it was unused — so grep would
// have counted the admission of the defect as evidence against it. The scan below
// resolves selector expressions on the actual RecallRequest parameter of each
// function that takes one, so a comment, a rename, or a reformat cannot turn it
// into an assertion about nothing (rule.coding mem_Os6BoOBG).
//
// No database needed:
//
//	go test ./internal/mcp/ -run TestRecallEveryPublishedParamIsReadByTheRankingCode -v

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// domainPkgDir is the package whose source is scanned for reads. handleRecall
// binds into domain.RecallRequest, so domain is where a read has to happen for a
// parameter to affect a result.
const domainPkgDir = "../domain"

// recallRequestTypeNames are the spellings of the bound request type that the
// scan recognises in a parameter declaration.
var recallRequestTypeNames = map[string]bool{"RecallRequest": true}

// floorRecallFieldReads is a liveness floor on the scan, not a measurement of
// anything. A census that resolved nothing would make this whole file vacuous
// while still printing a plausible pass, so the instrument has to prove it can
// see reads at all before its silence about one field means anything.
//
// Measured 2026-09-08 and re-measured 2026-09-09, over the 6 functions in
// internal/domain taking a RecallRequest: the census resolves 12 distinct field
// names on both dates. What changed is underneath that number. On 2026-09-08
// the struct had 12 json-tagged fields, 10 resolving at least one read and TWO
// resolving zero — recency_weight (withdrawn by aihub#469, field deleted by
// aihub#485) and visibility (withdrawn by aihub#484). Both are now gone, so the
// 10 json-tagged fields that remain each resolve at least one read, and the
// census total is unchanged only because the other two entries were never
// json-tagged: CallerUserID and CallerRole are read but tagged `json:"-"`, so
// they are not part of the published contract this gate is about.
//
// The floor is deliberately below 12 rather than pinned to it: this constant
// guards against the scan breaking, and a scan that legitimately sees one fewer
// field after a refactor should not fail as instrument failure when the arms
// below can still speak for themselves.
const floorRecallFieldReads = 8

// recallParamsNotReadByDomain is the reasoned allowlist: a published parameter
// this process consumes itself and deliberately never sends to the server.
//
// An entry here is a claim that gets checked in both directions below — it must
// still be published, and it must still be genuinely unread. That matters because
// the withdrawal this file gates was chosen over the alternative of adding
// `recency_weight` to this map: an exemption says "the caller's argument does
// something, just not in domain", and for recency_weight nothing anywhere did
// anything with it.
var recallParamsNotReadByDomain = map[string]string{
	"fields": "local-only projection, consumed by slimRecallResult (recall_slim.go) " +
		"in this process and deliberately never put on the wire — see the `fields` " +
		"probe in recallWireProbes, which asserts want:\"\"",
}

// recallParamsKnownUnreadTrackedByWi is the RATCHET, and it is a different claim
// from the allowlist above: these parameters ARE defective by this gate's own
// standard, are known to be, and are recorded here so the gate can stay green on
// the instances already filed while still failing on a new one.
//
// 🔴 It is not an exemption. Every entry must name an OPEN work item, and the
// checks below verify the claim is still true in both directions — the parameter
// must still be published, and it must still be genuinely unread. An entry whose
// parameter acquired a reader fails as stale, so fixing the defect forces the
// entry out rather than letting it sit forever as a blanket pass. That is the
// difference between a deferral that is gated and a deferral that is a comment.
//
// The map is empty, and that is the ratchet having worked rather than a gap.
//
// Its one entry was `visibility`, which the FIRST run of this gate found without
// the gate having been written for it: published as "Filter by visibility",
// forwarded in recallStringParams, bound in handleRecall, and read by none of
// the six domain functions that take a RecallRequest. It was deliberately not
// folded into aihub#469's withdrawal, because the disposition was not the same
// call — recency_weight duplicated ordering the code already had, whereas the
// recall path genuinely cannot filter by visibility, so implementing it would
// have been a real capability rather than a re-run of a fixed bug. That choice
// belonged to the owner, aihub#484 carried it, and on 2026-09-09 the owner ruled
// withdraw: not because implementing would harm, but because nobody was asking —
// 0 of 835 deduplicated pf_recall calls in the transcript corpus had ever sent
// the argument, which the checked-in aihub#412 corpus audit records independently
// as "published, never observed". The parameter went, so the entry went with it.
//
// Add another only alongside a work item that will do the same.
var recallParamsKnownUnreadTrackedByWi = map[string]string{}

// recallFieldsDeliberatelyUnpublished are RecallRequest fields the server fills
// from a source other than a published MCP argument. Unlike the two maps above
// these are not defects in either direction: something DOES write them.
var recallFieldsDeliberatelyUnpublished = map[string]string{
	"recall_algo": "forwarded but deliberately not published — set from an explicit " +
		"argument or POLYFORGE_RECALL_ALGO, and recorded in the G4 allowlist as " +
		"handleRecall.recall_algo. Nothing in any response advertises it, so no caller " +
		"is shown a value it cannot use (aihub#425)",
}

// recallFieldsKnownDeadTrackedByWi is the ratchet for the OTHER direction: a
// json-tagged RecallRequest field that no published parameter can reach and that
// nothing writes.
//
// 🔴 This exists because withdrawing a parameter removes it from the published
// quantifier's field of view, which is exactly how `mode` survived aihub#394's
// withdrawal and had to be cleaned up separately by aihub#424. Such a field is
// unreachable and inert — zero caller-visible surface — and that is precisely
// the state aihub#424 recorded as invisible to every gate in this package, so it
// is recorded here instead of trusted to memory.
//
// The map is empty, and that is the ratchet having worked rather than a gap. Its
// one entry was `recency_weight`, whose schema, forwarding and bind aihub#469
// withdrew while internal/domain/memory.go was locked by aihub#465; aihub#485
// then deleted the struct field and the stale NOTE in recallText, so the entry
// went with them. Add another only alongside a work item that will do the same.
//
// Entries are checked in both directions below. Deleting the field makes the
// entry stale and this arm fails naming it, which is the intended coupling: the
// cleanup cannot land without also removing its own bookkeeping.
var recallFieldsKnownDeadTrackedByWi = map[string]string{}

// recallParamToField overrides the json-tag mapping where the PUBLISHED parameter
// name and the struct field's json tag differ.
//
// There is exactly one such case and it is load-bearing: pf_recall publishes
// `type` (singular — aihub#289 made that spelling the contract), handleRecall
// reads `c.QueryParam("type")`, and the field it fills is `Types`, tagged
// `types`. Mapping by json tag alone therefore reports `type` as bound to
// nothing, which on the first run of this gate produced a false accusation
// against a parameter that is read 10 times. A gate whose failure set is wider
// than the defect it describes trains its readers to skip it.
var recallParamToField = map[string]string{
	"type": "Types",
}

// recallRequestFields maps each json-tagged domain.RecallRequest field to its Go
// field name. Fields tagged `-` are server-side only (CallerUserID / CallerRole)
// and cannot be named by a caller, so they are not part of the published
// contract this gate is about.
func recallRequestFields(t *testing.T) map[string]string {
	t.Helper()
	typ := reflect.TypeOf(domain.RecallRequest{})
	out := map[string]string{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		out[name] = f.Name
	}
	if len(out) == 0 {
		t.Fatal("domain.RecallRequest has no json-tagged fields — the reflection is broken, " +
			"not the struct, and every assertion below would be vacuous")
	}
	return out
}

// recallFieldReads scans package domain's non-test sources and counts, per Go
// field name, the selector expressions taken on a RecallRequest-typed parameter.
//
// Scoping to that parameter is what makes a zero meaningful: `Project`, `Query`
// and `Cursor` are field names other structs in the package also use, so an
// unscoped search for `.Project` would report a read that belongs to a different
// type — a false GREEN, which is the direction a gate must not fail in.
func recallFieldReads(t *testing.T) map[string]int {
	t.Helper()

	entries, err := os.ReadDir(domainPkgDir)
	if err != nil {
		t.Fatalf("read %s: %v — the package under scan is the subject of this gate, so a "+
			"missing directory is a failure, not an empty pass", domainPkgDir, err)
	}

	reads := map[string]int{}
	scanned, funcsWithReq := 0, 0
	fset := token.NewFileSet()

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(domainPkgDir, name), nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		scanned++

		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			for _, pname := range recallRequestParamNames(fn) {
				funcsWithReq++
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == pname {
						reads[sel.Sel.Name]++
					}
					return true
				})
			}
		}
	}

	if scanned == 0 {
		t.Fatalf("scanned 0 non-test files in %s — instrument failure", domainPkgDir)
	}
	if funcsWithReq == 0 {
		t.Fatalf("found no function in %s taking a RecallRequest parameter, over %d file(s) — "+
			"the type may have been renamed; update recallRequestTypeNames. Until then this "+
			"gate reports every parameter as unread", domainPkgDir, scanned)
	}
	if len(reads) < floorRecallFieldReads {
		t.Fatalf("the census resolved reads for only %d field name(s) across %d function(s) in "+
			"%d file(s) — that is the scan being broken, not the ranking code, and a gate that "+
			"cannot see reads would report every parameter as unread: %v",
			len(reads), funcsWithReq, scanned, reads)
	}
	t.Logf("census: %d file(s), %d function(s) taking a RecallRequest, reads=%v",
		scanned, funcsWithReq, reads)
	return reads
}

// recallRequestParamNames returns the names of fn's parameters typed
// RecallRequest or *RecallRequest. A function may take more than one.
func recallRequestParamNames(fn *ast.FuncDecl) []string {
	if fn.Type.Params == nil {
		return nil
	}
	var out []string
	for _, field := range fn.Type.Params.List {
		ident, ok := typeIdent(field.Type)
		if !ok || !recallRequestTypeNames[ident] {
			continue
		}
		for _, n := range field.Names {
			if n.Name != "_" {
				out = append(out, n.Name)
			}
		}
	}
	return out
}

// typeIdent unwraps a pointer type down to its bare identifier name.
func typeIdent(expr ast.Expr) (string, bool) {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return typeIdent(e.X)
	case *ast.Ident:
		return e.Name, true
	}
	return "", false
}

// TestRecallEveryPublishedParamIsReadByTheRankingCode is THE gate.
//
// It FAILS on the pre-fix tree, naming `recency_weight`. That is the point: it is
// the regression test for a contract that shipped a promise no hop kept, not a
// description of code that already worked.
func TestRecallEveryPublishedParamIsReadByTheRankingCode(t *testing.T) {
	published := schemaPropTypes(t, recallSchema())
	if len(published) == 0 {
		t.Fatal("recallSchema() published no properties at all — instrument failure")
	}
	fields := recallRequestFields(t)
	reads := recallFieldReads(t)

	// fieldFor resolves a published parameter name to its RecallRequest field,
	// preferring the explicit override over the json tag.
	fieldFor := func(param string) (string, bool) {
		if f, ok := recallParamToField[param]; ok {
			return f, true
		}
		f, ok := fields[param]
		return f, ok
	}

	checked := 0
	for param := range published {
		if reason, exempt := recallParamsNotReadByDomain[param]; exempt {
			// The exemption has to be TRUE, not merely claimed.
			if field, ok := fieldFor(param); ok && reads[field] > 0 {
				t.Errorf("%q is allowlisted as consumed in-process (%s) but package domain "+
					"DOES read %s.%s %d time(s) — stale exemption, drop the entry",
					param, reason, "RecallRequest", field, reads[field])
			}
			continue
		}

		if wi, tracked := recallParamsKnownUnreadTrackedByWi[param]; tracked {
			// A tracked defect that acquired a reader has been FIXED, and the
			// entry must go — otherwise the ratchet silently becomes a permanent
			// exemption covering whatever that parameter does next.
			if field, ok := fieldFor(param); ok && reads[field] > 0 {
				t.Errorf("%q is recorded as a known-unread defect (%s) but package domain now "+
					"reads %s.%s %d time(s) — the defect is fixed, so delete the entry from "+
					"recallParamsKnownUnreadTrackedByWi and close the work item",
					param, wi, "RecallRequest", field, reads[field])
				continue
			}
			t.Logf("%s: known unread, tracked — %s", param, wi)
			continue
		}

		field, bound := fieldFor(param)
		if !bound {
			t.Errorf("pf_recall publishes %q and domain.RecallRequest has no json-tagged field "+
				"for it. The argument reaches the server and echo's Bind drops it, so the call "+
				"answers 200 having done less than it said. Bind it and read it, withdraw it "+
				"from recallSchema, or record it in recallParamsNotReadByDomain with the "+
				"reason.", param)
			continue
		}

		if reads[field] == 0 {
			t.Errorf("pf_recall publishes %q, handleRecall binds it into "+
				"domain.RecallRequest.%s, and package domain never reads that field.\n"+
				"The schema states a contract no hop keeps: the caller's argument is accepted, "+
				"forwarded, bound, and then ignored — no error, no warning, and a response "+
				"byte-identical to the one they would have got without it. This is aihub#469's "+
				"exact signature (and aihub#387's before it), and the cost was not a missing "+
				"feature but a DESIGN written on top of a knob that did nothing: "+
				"docs/design/polyforge-v1-design.md specified a blend for %s and its changelog "+
				"claimed doc and implementation were aligned.\n"+
				"Either make the ranking honour it, or withdraw it from recallSchema AND from "+
				"the hop-3 bind in handleRecall — withdrawing the schema alone leaves a field "+
				"the server binds that no tool can reach, which is what aihub#424 had to clean "+
				"up after aihub#394. If it is genuinely consumed in this process, add it to "+
				"recallParamsNotReadByDomain with the reason.", param, field, param)
			continue
		}
		checked++
	}

	if checked == 0 {
		t.Fatal("not one published pf_recall parameter resolved to a field package domain " +
			"reads — the lookup is broken, not the ranking code, and this gate would report " +
			"every parameter")
	}

	// A stale entry in either list would silently excuse a real drop, so every
	// entry must name something that is still published.
	for param, reason := range recallParamsNotReadByDomain {
		if _, ok := published[param]; !ok {
			t.Errorf("recallParamsNotReadByDomain exempts %q (%s), which pf_recall no longer "+
				"publishes — stale exemption, drop the entry", param, reason)
		}
	}
	for param, wi := range recallParamsKnownUnreadTrackedByWi {
		if _, ok := published[param]; !ok {
			t.Errorf("recallParamsKnownUnreadTrackedByWi records %q (%s), which pf_recall no "+
				"longer publishes — the parameter was withdrawn, so the entry must go with it",
				param, wi)
		}
		if _, ok := recallParamsNotReadByDomain[param]; ok {
			t.Errorf("%q is in BOTH recallParamsNotReadByDomain and "+
				"recallParamsKnownUnreadTrackedByWi. Those say opposite things — the first "+
				"claims the argument does something here, the second that it does nothing "+
				"anywhere. Pick one", param)
		}
	}

	// Liveness on the ratchet's own discriminating power. Every tracked entry
	// resolving to no field would make its "still unread" check vacuous — it
	// would pass for a parameter that has no field to read in the first place,
	// which is a different defect with a different fix.
	for param, wi := range recallParamsKnownUnreadTrackedByWi {
		if _, ok := fieldFor(param); !ok {
			t.Errorf("recallParamsKnownUnreadTrackedByWi records %q (%s) as bound-but-unread, "+
				"but no RecallRequest field resolves for it — it is UNBOUND, which this gate "+
				"reports separately and which needs a different fix. Correct the entry",
				param, wi)
		}
	}

	t.Logf("%d of %d published pf_recall parameters are read by package domain; "+
		"%d exempt (local-only), %d known-unread and tracked",
		checked, len(published), len(recallParamsNotReadByDomain),
		len(recallParamsKnownUnreadTrackedByWi))
}

// TestRecallRequestBindsNothingUnreachable is the reverse arm, quantified over
// what the request struct BINDS rather than over what the tool publishes.
//
// Both quantifiers are needed and neither implies the other. The gate above asks
// "does every published parameter reach a reader?" — and the act of withdrawing a
// parameter removes it from that question, which is how aihub#394's withdrawal of
// `mode` left a bound, inert, unpublished field that satisfied every gate in this
// package until aihub#424 went looking. This arm asks the question that does not
// shrink: "can every field a caller could name still be named?"
//
// It goes RED on a field that is neither published, nor deliberately server-set,
// nor recorded as a tracked leftover.
func TestRecallRequestBindsNothingUnreachable(t *testing.T) {
	published := schemaPropTypes(t, recallSchema())
	if len(published) == 0 {
		t.Fatal("recallSchema() published no properties at all — instrument failure")
	}
	fields := recallRequestFields(t)

	// Reverse the published-name override so a field can be matched by the
	// parameter that actually reaches it. pf_recall publishes `type`; the field
	// it fills is `Types`, tagged `types` — without this the singular/plural
	// mismatch reads as an unreachable field.
	fieldToParam := map[string]string{}
	for param, field := range recallParamToField {
		fieldToParam[field] = param
	}

	reachable := 0
	for jsonName, goName := range fields {
		if _, ok := published[jsonName]; ok {
			reachable++
			continue
		}
		if param, ok := fieldToParam[goName]; ok {
			if _, isPublished := published[param]; isPublished {
				reachable++
				continue
			}
		}
		if reason, exempt := recallFieldsDeliberatelyUnpublished[jsonName]; exempt {
			t.Logf("%s: bound but deliberately unpublished — %s", jsonName, reason)
			continue
		}
		if wi, tracked := recallFieldsKnownDeadTrackedByWi[jsonName]; tracked {
			t.Logf("%s: known dead field, tracked — %s", jsonName, wi)
			continue
		}
		t.Errorf("domain.RecallRequest binds %q (field %s) and pf_recall publishes no such "+
			"parameter.\nNo MCP caller can set it: the handler reads only the arguments the "+
			"schema declares, so an undeclared one is forwarded to nothing and the field can "+
			"only ever hold its zero value. Note which gate this defeats — "+
			"TestRecallEveryPublishedParamIsReadByTheRankingCode quantifies over PUBLISHED "+
			"parameters, so withdrawing one removes it from that arm's field of view, which "+
			"is exactly how aihub#394's withdrawal of `mode` left a field aihub#424 had to "+
			"clean up months later. Stop binding it, publish it, or record it in "+
			"recallFieldsDeliberatelyUnpublished (something writes it) or "+
			"recallFieldsKnownDeadTrackedByWi (it is dead and a work item says so).",
			jsonName, goName)
	}

	if reachable == 0 {
		t.Fatal("not one RecallRequest field was found published — the schema lookup is " +
			"broken, not the struct, and this arm would report every field")
	}

	// Staleness, both directions, for both maps.
	for jsonName, wi := range recallFieldsKnownDeadTrackedByWi {
		if _, ok := published[jsonName]; ok {
			t.Errorf("recallFieldsKnownDeadTrackedByWi records %q (%s) as dead, but pf_recall "+
				"publishes it again — a caller can reach it now, so the entry is wrong",
				jsonName, wi)
		}
		if _, ok := fields[jsonName]; !ok {
			t.Errorf("recallFieldsKnownDeadTrackedByWi records %q (%s), which domain.RecallRequest "+
				"no longer binds — the field was deleted, so delete this entry with it and "+
				"close the work item", jsonName, wi)
		}
	}
	for jsonName, reason := range recallFieldsDeliberatelyUnpublished {
		if _, ok := fields[jsonName]; !ok {
			t.Errorf("recallFieldsDeliberatelyUnpublished exempts %q (%s), which "+
				"domain.RecallRequest no longer binds — stale exemption", jsonName, reason)
		}
		if _, ok := published[jsonName]; ok {
			t.Errorf("%q is now published; drop it from recallFieldsDeliberatelyUnpublished "+
				"(recorded reason: %s)", jsonName, reason)
		}
	}

	t.Logf("%d of %d RecallRequest fields are reachable from a published parameter; "+
		"%d deliberately server-set, %d dead and tracked",
		reachable, len(fields), len(recallFieldsDeliberatelyUnpublished),
		len(recallFieldsKnownDeadTrackedByWi))
}
