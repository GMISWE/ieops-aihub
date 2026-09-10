package mcp_test

// aihub#543 probe wave 2 — the pf_get_work_item card's four-writer census.
//
// The card's Policy section is the only place in the repo that states the SHAPE
// of the `request_adjusted` mechanism: four writers, three of them clamp
// disclosures sharing one appender, one of them the unknown-argument disclosure
// on its own site — and therefore that the `param` value is the only thing that
// tells a caller which kind they were handed.
//
// Every part of that had an arm for ONE writer and none for the relation:
//
//	appendIntAdjustment's behaviour        internal/domain/request_adjusted_test.go
//	the unknown-argument disclosure        internal/mcp/unknown_params_test.go
//	the two kinds coexisting on one reply  TestUnknownParamsDisclosureKeepsTheServersOwnAdjustments
//	the POPULATION                         nothing
//
// A fifth writer — a second unknown-argument site, or a clamp that appends its
// own entry without going through the appender — would leave all three of those
// green while the card's census became wrong and, worse, while the `param` value
// stopped being a reliable discriminator. That is the aihub#532 argument applied
// one convention over: a reach written in prose is invisible to everything
// except a human reading the whole tree.
//
// 🔴 Why a source census and not a call. The relation is between CALL SITES, and
// no response carries the number of them. clampdisclosure/ does the equivalent
// walk for clamps and states the same reason for itself.
//
//	GOWORK=off go test ./internal/mcp/ -run TestRequestAdjusted -count=1 -v

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// clampAppender is the one function the three clamp disclosures share.
const clampAppender = "appendIntAdjustment"

// unknownArgumentWriter is the fourth writer's own site, and the parameter name
// it stamps.
//
// The name is here rather than derived, because the claim is about THIS value: a
// caller reading `request_adjusted` tells an unknown argument from a clamp by
// the `param` field, so the value is the discriminator and not an implementation
// detail. unknown_params_test.go's unknownParamsEntry filters on the same
// string, from the response side.
const (
	unknownArgumentWriterFile = "unknown_params.go"
	unknownArgumentParamName  = "unknown_params"
)

// expectedClampParams are the three parameters the clamp disclosures name.
//
// Written down rather than counted for the reason readyQueueSurfacedParams
// states: a count of three is satisfied by any three, and what the card claims
// is WHICH three — each of them carded on a different tool, so a fourth would be
// a disclosure no card mentions.
var expectedClampParams = []string{"limit", "max", "top_k"}

// floorRequestAdjustedWriters is how many clamp-appender call sites the walk
// must find.
//
// A walk that finds none asserts nothing: the checks below would pass over an
// empty population, which is the same green as a repo whose four writers are
// exactly the four. Equal to the measured count, and it is a floor rather than
// an equality only in the direction that matters — a FIFTH site is reported by
// name below, and a fourth clamp disclosure is a real event that needs a card
// edit, not a number bump.
const floorRequestAdjustedWriters = 3

// TestRequestAdjustedHasOneClampAppenderAndOneUnknownArgumentWriter is the
// census.
//
// ─── Recorded mutants ──────────────────────────────────────────────────────
//
// Every one was applied to this tree and the verdict is what RAN.
//
//	── enforcement side (the call sites, the param names untouched) ──
//	M19 delete the appendIntAdjustment call in domain/work_items.go's
//	    ListWorkItems (the `limit` disclosure)
//	                                        RED  two sites found, floor is three,
//	                                             and `limit` went missing from
//	                                             the named set
//	M20 add a fourth appendIntAdjustment call naming "since"
//	                                        RED  reported by name as a
//	                                             disclosure no card mentions
//	M21 make internal/mcp/unknown_params.go append its entry through
//	    domain's appender instead of its own site
//	                                        RED  the two kinds stopped being
//	                                             separable by site, which is the
//	                                             half the card's last sentence
//	                                             rests on
//
//	── declaration side (the param names, the call sites untouched) ──
//	M22 rename the unknown-argument param value to "unknown_arguments"
//	                                        RED  the value a caller discriminates
//	                                             on moved, and the response-side
//	                                             arm beside this one filters on
//	                                             the old string
//	M23 change the ready-queue clamp's param literal from "max" to "limit"
//	                                        RED  two clamp disclosures now answer
//	                                             to one name, so `param` no
//	                                             longer says which endpoint
//	                                             adjusted the request
func TestRequestAdjustedHasOneClampAppenderAndOneUnknownArgumentWriter(t *testing.T) {
	// Half one: every clamp-appender call site in the domain, with the
	// parameter each one names.
	sites, filesWalked := clampAppenderSites(t, filepath.Join("..", "domain"))

	if filesWalked < 10 {
		t.Fatalf("the walk read only %d non-test .go file(s) under ../domain — it is looking at "+
			"the wrong directory and every assertion below would be about nothing", filesWalked)
	}
	if len(sites) < floorRequestAdjustedWriters {
		t.Fatalf("found %d %s call site(s), floor is %d.\n"+
			"    A walk that finds nothing classifies nothing: the checks below would pass over "+
			"an empty population, which is the same green as a correct tree. Either the appender "+
			"was renamed (fix clampAppender in the same diff) or a clamp stopped disclosing — "+
			"which is the aihub#314 defect the whole convention exists to prevent.",
			len(sites), clampAppender, floorRequestAdjustedWriters)
	}

	named := map[string][]string{} // param -> the files that disclose it
	for _, s := range sites {
		named[s.param] = append(named[s.param], s.file)
	}
	for _, want := range expectedClampParams {
		if len(named[want]) == 0 {
			t.Errorf("no %s call site names the parameter %q. The pf_get_work_item card says "+
				"the three clamp disclosures are `top_k` (pf_recall), `limit` "+
				"(pf_list_work_items) and `max` (pf_get_ready_queue), each carded on its own "+
				"tool; a missing one is a clamp that changed the caller's value in silence. "+
				"Found: %v", clampAppender, want, disclosedParamNames(named))
		}
		if len(named[want]) > 1 {
			t.Errorf("the parameter %q is disclosed from %d sites (%v). aihub#432's whole "+
				"argument for putting the ready-queue append inside newReadyQueue was that a "+
				"disclosure attached at two exits is two chances to forget one.",
				want, len(named[want]), named[want])
		}
	}
	for param, files := range named {
		known := false
		for _, want := range expectedClampParams {
			if param == want {
				known = true
			}
		}
		if !known {
			t.Errorf("%s discloses a clamp on %q (%v), which no contract card mentions. "+
				"Every disclosed clamp is a promise on some tool's response, so a fourth one "+
				"is a card edit and a ledger row, not a silent addition — the aihub#411 T1-12 "+
				"finding, verbatim.", clampAppender, param, files)
		}
	}
	if strings.Contains(strings.Join(flattenSiteFiles(named), " "), unknownArgumentWriterFile) {
		t.Errorf("the unknown-argument writer appends through %s. The card's last sentence — "+
			"that a caller reading `request_adjusted` cannot infer an unknown argument and the "+
			"`param` value is what tells the two apart — rests on these being separate sites "+
			"with separate names.", clampAppender)
	}

	// Half two: the fourth writer, on its own site, stamping its own name.
	t.Run("the unknown-argument writer is its own site", func(t *testing.T) {
		raw, err := os.ReadFile(unknownArgumentWriterFile)
		if err != nil {
			t.Fatalf("read %s: %v — the card names this file as the unknown-argument "+
				"disclosure's own site, and an arm cannot pass by not finding its subject",
				unknownArgumentWriterFile, err)
		}
		src := string(raw)
		if !strings.Contains(src, strconv.Quote(unknownArgumentParamName)) {
			t.Errorf("%s does not carry the literal %q. That value is the discriminator: "+
				"unknown_params_test.go's unknownParamsEntry filters responses on it, the card "+
				"tells callers to read it, and moving it breaks both without breaking a build.",
				unknownArgumentWriterFile, unknownArgumentParamName)
		}
		if strings.Contains(src, clampAppender) {
			t.Errorf("%s calls %s. The card says this writer is its OWN site and the three "+
				"clamps share the appender; routing it through them makes the two kinds of "+
				"disclosure indistinguishable by anything except the string above.",
				unknownArgumentWriterFile, clampAppender)
		}
		// And the name must not collide with a clamp's, or `param` stops
		// discriminating.
		for _, clamp := range expectedClampParams {
			if unknownArgumentParamName == clamp {
				t.Errorf("the unknown-argument disclosure and a clamp both answer to %q, so a "+
					"caller reading `param` cannot tell an argument the server did not "+
					"recognise from a value it changed.", clamp)
			}
		}
	})

	t.Logf("request_adjusted writers: %d clamp site(s) naming %v, plus %s naming %q",
		len(sites), disclosedParamNames(named), unknownArgumentWriterFile, unknownArgumentParamName)
}

type clampSite struct {
	file  string
	param string
}

// clampAppenderSites finds every call to the clamp appender in a package's
// non-test files and reports the string literal it was called with.
//
// The parameter name is read from the CALL, not from a list: an append whose
// first argument is not a literal is reported rather than skipped, because a
// disclosure whose name is computed is one no reader can predict.
func clampAppenderSites(t *testing.T, dir string) (sites []clampSite, filesWalked int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		filesWalked++
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok || id.Name != clampAppender || len(call.Args) < 2 {
				return true
			}
			lit, ok := call.Args[1].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Errorf("%s: a %s call names its parameter with something other than a string "+
					"literal. A disclosure whose `param` value is computed is one no caller can "+
					"predict and no census can enumerate.", name, clampAppender)
				return true
			}
			param, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				t.Errorf("%s: cannot read the parameter name %s: %v", name, lit.Value, uerr)
				return true
			}
			sites = append(sites, clampSite{file: name, param: param})
			return true
		})
	}
	return sites, filesWalked
}

func disclosedParamNames(named map[string][]string) []string {
	out := make([]string, 0, len(named))
	for k := range named {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func flattenSiteFiles(named map[string][]string) []string {
	var out []string
	for _, files := range named {
		out = append(out, files...)
	}
	sort.Strings(out)
	return out
}
