package mcp_test

// aihub#543 probe wave 2 — the pf_get_work_item card's request_adjusted census.
//
// The card's Policy section is the only place in the repo that states the SHAPE
// of the `request_adjusted` mechanism: how many writers there are, that three of
// them are clamp disclosures sharing one appender, that one is the
// unknown-argument disclosure on its own site — and therefore that the `param`
// value is the only thing that tells a caller which kind they were handed.
//
// Every part of that had an arm for ONE writer and none for the relation:
//
//	appendIntAdjustment's behaviour        internal/domain/request_adjusted_test.go
//	the unknown-argument disclosure        internal/mcp/unknown_params_test.go
//	the two kinds coexisting on one reply  TestUnknownParamsDisclosureKeepsTheServersOwnAdjustments
//	the POPULATION                         nothing
//
// A further writer — a second unknown-argument site, or a clamp that appends its
// own entry without going through the appender — would leave all three of those
// green while the card's census became wrong and, worse, while the `param` value
// stopped being a reliable discriminator. That is the aihub#532 argument applied
// one convention over: a reach written in prose is invisible to everything
// except a human reading the whole tree.
//
// 🔴 REBUILT BY aihub#543's REVIEW ROUND, and the three defects it fixes are
// worth stating because each one made this arm quieter than it reads:
//
//   - The population was WRONG, not just narrow. The walk covered
//     internal/domain only, so it counted four writers while the tree held five:
//     internal/server/routes_step.go hand-builds its own RequestAdjustment
//     entries for the step_id/status a heartbeat discards (documented on the
//     pf_update_step card), which is neither a clamp nor an unknown argument. The
//     card said "four writers" and this arm agreed with it by not looking.
//   - Half of it was a TEXT SCAN over unknown_params.go, and that file's own doc
//     comment contains `"unknown_params"` as an example payload — so renaming the
//     real constant (mutant M22) left the arm green against the comment, with its
//     t.Logf still printing the value from a Go const nobody had changed. Both
//     halves are read with go/parser now, from code rather than from bytes.
//   - Two assertions could never fire: one asked whether a set of
//     internal/domain filenames contained "unknown_params.go", and one compared
//     two constants declared in this file. Both are gone, and the mutants that
//     were attributed to them are re-attributed to the checks that really caught
//     them.
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
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// clampAppender is the one function the three clamp disclosures share.
const clampAppender = "appendIntAdjustment"

// adjustmentType is the entry type every writer of this field constructs, under
// either spelling: bare inside internal/domain, qualified everywhere else.
const adjustmentType = "RequestAdjustment"

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

// expectedAdjustmentBuilders is every file allowed to construct an adjustment
// entry by hand, with what the card says each one is.
//
// A MAP rather than a count, for the same reason expectedClampParams is a list:
// "three files build one" is satisfied by any three, and the claim is which. A
// file arriving here is a writer no card mentions, and a file LEAVING it is a
// disclosure that stopped being made — the second direction is why this is
// compared both ways below.
var expectedAdjustmentBuilders = map[string]string{
	"internal/domain/request_adjusted.go": "the shared clamp appender itself, which is how the " +
		"three clamp disclosures avoid hand-building anything",
	"internal/mcp/unknown_params.go": "the unknown-argument disclosure, on its own site and " +
		"stamping its own param value",
	"internal/server/routes_step.go": "the heartbeat's dropped step_id/status disclosure — " +
		"neither a clamp nor an unknown argument, carded on pf_update_step, and reachable only " +
		"by a direct HTTP caller",
}

// floorRequestAdjustedWriters is how many clamp-appender call sites the walk
// must find.
//
// A walk that finds none asserts nothing: the checks below would pass over an
// empty population, which is the same green as a repo whose writers are exactly
// the expected ones. Equal to the measured count, and it is a floor rather than
// an equality only in the direction that matters — a FOURTH clamp site is
// reported by name below, and a fourth clamp disclosure is a real event that
// needs a card edit, not a number bump.
const floorRequestAdjustedWriters = 3

// floorAdjustmentFilesWalked bounds the tree-wide walk for hand-built entries.
//
// The clamp half has its own floor over one package; this one covers internal/
// and pkg/, and without it a walk pointed at a directory that does not exist
// reports "no writer outside the expected three" — which is the answer a correct
// tree gives. Measured 2026-09-10: 102 non-test .go files, so the floor is set
// well below that and moves only when the tree is restructured.
const floorAdjustmentFilesWalked = 80

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
//	                                        RED  the_unknown_argument_writer_is_its
//	                                             _own_site — the two kinds stopped
//	                                             being separable by site, which is
//	                                             the half the card's last sentence
//	                                             rests on
//
//	── declaration side (the param names, the call sites untouched) ──
//	M22 rename the unknown-argument param value to "unknown_arguments"
//	                                        RED  the value a caller discriminates
//	                                             on moved, and the response-side
//	                                             arm beside this one filters on
//	                                             the old string
//	                                        ⚠️ GREEN before the aihub#543 rebuild:
//	                                             the check was a text scan and this
//	                                             file's own doc comment carries
//	                                             `"unknown_params"` as an example
//	                                             payload, so the arm matched the
//	                                             COMMENT while the code said
//	                                             something else
//	M23 change the ready-queue clamp's param literal from "max" to "limit"
//	                                        RED  two clamp disclosures now answer
//	                                             to one name, so `param` no
//	                                             longer says which endpoint
//	                                             adjusted the request
//
//	── population side (aihub#543's review round, 2026-09-10) ──
//	M26 add a hand-built domain.RequestAdjustment to a file outside the expected
//	    three (internal/server/routes_memory.go), written in the ELIDED form
//	    `[]domain.RequestAdjustment{{Param: …}}`
//	                                        GREEN before the rebuild — the walk
//	                                              never left internal/domain
//	                                        GREEN on the rebuild's FIRST version
//	                                              too, and this is why the run is
//	                                              recorded rather than the result:
//	                                              an elided element literal carries
//	                                              no type of its own, so matching on
//	                                              the type alone saw nothing. The
//	                                              array-element branch exists
//	                                              because of this mutant.
//	                                        RED   the_hand_built_entries_are_the
//	                                              _three_the_card_names, naming the
//	                                              file and the param
//	M27 delete the heartbeat's two dropped-field entries in
//	    internal/server/routes_step.go
//	                                        GREEN before the rebuild, for the same
//	                                              reason
//	                                        RED   the same subtest, from the other
//	                                              direction: a disclosure the card
//	                                              names stopped being made
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

	// Half two: the fourth writer, on its own site, stamping its own name — read
	// from the AST, so a doc comment carrying the value cannot answer for the code.
	t.Run("the_unknown_argument_writer_is_its_own_site", func(t *testing.T) {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, unknownArgumentWriterFile, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v — the card names this file as the unknown-argument "+
				"disclosure's own site, and an arm cannot pass by not finding its subject",
				unknownArgumentWriterFile, err)
		}

		var literals []string
		var appenderCalls int
		ast.Inspect(file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.BasicLit:
				if v.Kind == token.STRING {
					if s, uerr := strconv.Unquote(v.Value); uerr == nil {
						literals = append(literals, s)
					}
				}
			case *ast.CallExpr:
				if id, ok := v.Fun.(*ast.Ident); ok && id.Name == clampAppender {
					appenderCalls++
				}
			}
			return true
		})

		found := false
		for _, lit := range literals {
			if lit == unknownArgumentParamName {
				found = true
			}
		}
		if !found {
			t.Errorf("%s declares no string literal %q anywhere in its CODE (it holds %d string "+
				"literal(s)). That value is the discriminator: unknown_params_test.go's "+
				"unknownParamsEntry filters responses on it, the card tells callers to read it, "+
				"and moving it breaks both without breaking a build. ⚠️ This is read from the "+
				"parsed file rather than from its bytes on purpose — the file's doc comment "+
				"carries the same string as an example payload, and a text scan was satisfied by "+
				"that comment alone.",
				unknownArgumentWriterFile, unknownArgumentParamName, len(literals))
		}
		if appenderCalls > 0 {
			t.Errorf("%s calls %s %d time(s). The card says this writer is its OWN site and the "+
				"three clamps share the appender; routing it through them makes the two kinds of "+
				"disclosure indistinguishable by anything except the param value.",
				unknownArgumentWriterFile, clampAppender, appenderCalls)
		}
	})

	// Half three: the population itself, across the whole tree rather than one
	// package. This is the half that was missing, and the fifth writer is why.
	t.Run("the_hand_built_entries_are_the_three_the_card_names", func(t *testing.T) {
		builders, walked := adjustmentBuilderFiles(t)
		if walked < floorAdjustmentFilesWalked {
			t.Fatalf("the walk read only %d non-test .go file(s) under internal/ and pkg/, floor "+
				"is %d. A walk that reads almost nothing finds no unexpected writer, which is "+
				"exactly what a correct tree looks like from here.", walked, floorAdjustmentFilesWalked)
		}
		for file, why := range expectedAdjustmentBuilders {
			if _, present := builders[file]; !present {
				t.Errorf("%s no longer constructs a %s. The card names it as %s — a disclosure "+
					"the card promises and the code stopped making is the aihub#314 defect in the "+
					"direction nobody watches. Found: %v",
					file, adjustmentType, why, sortedBuilderFiles(builders))
			}
		}
		for file, params := range builders {
			if _, expected := expectedAdjustmentBuilders[file]; expected {
				continue
			}
			t.Errorf("%s hand-builds a %s naming %v, and no contract card mentions it. Every "+
				"entry in this field is a promise on some tool's response, and a writer that is "+
				"neither the shared clamp appender nor a carded site means a caller reading "+
				"`request_adjusted` is being told something no card explains.",
				file, adjustmentType, params)
		}
		t.Logf("request_adjusted writers: %d clamp site(s) naming %v via %s, plus hand-built "+
			"entries in %v", len(sites), disclosedParamNames(named), clampAppender,
			sortedBuilderFiles(builders))
	})
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

// adjustmentBuilderFiles censuses every non-test Go file under internal/ and
// pkg/ that constructs an adjustment entry by hand, keyed on the repo-relative
// path, with each site's `Param` value where that value is a literal.
//
// 🔴 The WHOLE tree, and that is the aihub#543 review-round fix rather than
// thoroughness for its own sake: this census used to walk internal/domain only,
// and the writer it therefore could not see — internal/server/routes_step.go —
// is the fifth one, whose existence made the card's own "four writers" false.
// A census scoped more narrowly than the claim it holds is a census that agrees
// with a wrong number by not looking.
//
// A composite literal rather than a call, because that is what a hand-built
// entry IS: `domain.RequestAdjustment{Param: …}` under the qualified spelling
// outside internal/domain and the bare one inside it. Both are matched, since
// which spelling a file uses is a fact about its imports and not about the
// disclosure it makes.
func adjustmentBuilderFiles(t *testing.T) (builders map[string][]string, filesWalked int) {
	t.Helper()
	builders = map[string][]string{}
	for _, root := range []string{filepath.Join("..", "..", "internal"), filepath.Join("..", "..", "pkg")} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if d.IsDir() {
				switch d.Name() {
				case ".git", "vendor", "node_modules", "testdata":
					return fs.SkipDir
				}
				return nil
			}
			name := d.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			filesWalked++
			fset := token.NewFileSet()
			file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if perr != nil {
				return perr
			}
			rel := strings.TrimPrefix(filepath.ToSlash(path), "../../")
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				if isAdjustmentLiteralType(lit.Type) {
					builders[rel] = append(builders[rel], adjustmentLiteralParam(lit))
					return true
				}
				// 🔴 The ELIDED form, and it is here because the first version of this
				// census missed it: inside `[]domain.RequestAdjustment{{Param: …}}` the
				// element literal carries NO type of its own, so matching on the type
				// alone reported nothing for a writer sitting in plain sight. Measured —
				// mutant M26 was written in exactly that shape and stayed green.
				//
				// Elements that are not composite literals (unknown_params.go writes
				// `[]domain.RequestAdjustment{entry}`) are deliberately not counted here:
				// the construction happened elsewhere and is recorded at its own site.
				if at, isArray := lit.Type.(*ast.ArrayType); isArray && isAdjustmentLiteralType(at.Elt) {
					for _, elt := range lit.Elts {
						if el, isLit := elt.(*ast.CompositeLit); isLit {
							builders[rel] = append(builders[rel], adjustmentLiteralParam(el))
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v — a failed walk reports no writer at all, which is the same "+
				"answer as a tree whose writers are exactly the expected ones", root, err)
		}
	}
	return builders, filesWalked
}

// isAdjustmentLiteralType matches both spellings of the entry type and nothing
// else: a slice OF them is not a construction of one (unknown_params.go writes
// `[]domain.RequestAdjustment{entry}` around an entry it built above).
func isAdjustmentLiteralType(expr ast.Expr) bool {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name == adjustmentType
	case *ast.SelectorExpr:
		return v.Sel != nil && v.Sel.Name == adjustmentType
	}
	return false
}

// adjustmentLiteralParam reports the literal `Param:` value of one hand-built
// entry, or a placeholder when the value is computed. The placeholder is
// deliberate rather than a skip: a disclosure whose param is an identifier is
// still a disclosure, and dropping it here would shrink the population.
func adjustmentLiteralParam(lit *ast.CompositeLit) string {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Param" {
			continue
		}
		if v, isLit := kv.Value.(*ast.BasicLit); isLit && v.Kind == token.STRING {
			if s, err := strconv.Unquote(v.Value); err == nil {
				return s
			}
		}
		return "<computed>"
	}
	return "<no Param field>"
}

func disclosedParamNames(named map[string][]string) []string {
	out := make([]string, 0, len(named))
	for k := range named {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedBuilderFiles(builders map[string][]string) []string {
	out := make([]string, 0, len(builders))
	for k, params := range builders {
		out = append(out, k+" ("+strings.Join(params, ", ")+")")
	}
	sort.Strings(out)
	return out
}
