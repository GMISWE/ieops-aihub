package domain

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Embedding-writer parity gate (aihub#361).
//
// WHAT WENT WRONG
// ---------------
// The rule for "what text do we hand the embedding provider" was written out
// separately in every place that needed it, and the copies disagreed:
//
//	internal/domain/memory.go:975       Embed(ctx, req.Content)      ← no cap
//	cmd/aihub-embed-backfill:103-104    cap 6000 runes (memories)
//	cmd/aihub-embed-backfill:156-157    cap 6000 runes (work items)
//	cmd/aihub-embed-verify:63           cap 6000 runes
//	internal/domain/wi_embedding.go:22  cap 6000 runes (work items)
//
// So a memories row above the cap got a FULL-TEXT vector from the live path and
// a PREFIX vector from any later backfill — with a byte-identical emb_model, so
// nothing in the data distinguishes the two populations — and a row above the
// provider's context window failed to embed on the live path entirely, leaving
// emb_vector NULL and the row permanently invisible to vector recall.
//
// WHY THIS GATE IS STRUCTURAL AND NOT BEHAVIOURAL
// ----------------------------------------------
// A behavioural test can only assert what ONE writer does with a given input.
// Asserting "the live path truncates at 6000" would have gone green while the
// backfill moved to 8000 the next day — the defect is a RELATION between two
// call sites, and the only thing that makes it impossible is that both sites
// obtain their text from the same function. So this file asserts on the shipped
// call graph: every embedding write in the repo passes the provider a value
// that came, unwrapped and unreassigned, out of one of the two sanctioned
// builders.
//
// WHY A REPO-WIDE CENSUS AND NOT FOUR PER-FILE ASSERTIONS
// A check scoped to the four known writers goes green the moment a fifth writer
// is added somewhere else — which is exactly how this defect got in: the verify
// command grew its own third copy of the cap long after the first two existed.
// The census below classifies EVERY `.Embed(` call in non-test code as writer,
// reader, provider or probe, and an unclassified one is a violation. Adding a
// writer therefore forces a decision instead of silently forking the rule.
//
// The builder names below are string LITERALS, not references to the production
// identifiers. A gate that names the thing it checks by importing it agrees
// with the code by construction and can never disagree with it; keyed on
// literals, this file compiles and FAILS on a tree that has not been fixed,
// which is the only way it is evidence of anything.

const (
	memoryEmbedBuilder   = "MemoryEmbedInput"
	workItemEmbedBuilder = "WorkItemEmbedInput"

	// embedBuilderFile is where both builders and the single rune budget must
	// live. Used as a positive anchor: the census can pass by there being no
	// writers left at all, and this stops that from reading as health.
	embedBuilderFile = "internal/domain/embed_input.go"
)

// embedWriteSites maps each file that WRITES an embedding to the builders it is
// allowed to source its text from. A memory writer must not compose work-item
// text and vice versa, so the permission is per builder, not "any builder".
var embedWriteSites = map[string][]string{
	"internal/domain/memory.go":        {memoryEmbedBuilder},
	"internal/domain/wi_embedding.go":  {workItemEmbedBuilder},
	"cmd/aihub-embed-backfill/main.go": {memoryEmbedBuilder, workItemEmbedBuilder},
	// The verify probe is not a writer, but it must re-embed the SAME text a
	// writer would have produced or its cosine verdict is meaningless — that is
	// why it too carried a copy of the cap, and why it belongs in this table
	// rather than on the allowlist below.
	"cmd/aihub-embed-verify/main.go": {memoryEmbedBuilder},
}

// embedReadSites are the `.Embed(` calls that are legitimately NOT subject to
// the writer rule, each with the reason. Being on this list is a claim someone
// made on purpose; an unlisted call site fails the census.
var embedReadSites = map[string]string{
	"internal/domain/memory_vector.go": "recall QUERY side: embeds the caller's search string, not a stored row. " +
		"A query is short by construction and is never persisted, so it shares no vector-parity obligation.",
	"internal/domain/wi_vector.go": "work-item recall QUERY side, same reasoning as memory_vector.go.",
	"internal/embedding/openai.go": "the provider implementation itself — EmbedBatch/Ping calling its own Embed. " +
		"Below the layer where input composition is decided.",
	"internal/embedding/ollama.go":             "provider implementation, same as openai.go.",
	"internal/embedding/budget.go":             "the concurrency-budget decorator delegating to the wrapped provider.",
	"internal/citest/embedprobe/embedprobe.go": "CI probe: embeds fixed literals to check the backend is reachable.",
}

// embedProviderMethods are the embedding.Provider methods that take text and
// return vectors. EmbedBatch is here even though no writer uses it today: a
// census that watched only Embed would go green the day a writer switched to
// the batch form, which is the same "nobody checks that shape" hole aihub#361
// lived in. What a write site may do with EmbedBatch is decided in the analyser.
var embedProviderMethods = map[string]bool{"Embed": true, "EmbedBatch": true}

// minEmbedCallsites is a floor, not a count. A census that visits nothing
// reports no violations perfectly; this is what makes "clean" mean "looked".
// Measured 2026-09-05: the census found 14 provider call sites (13 Embed, 1
// EmbedBatch) across the repo. The floor sits well under that so ordinary
// churn does not trip it, and well over zero so a broken matcher does.
const minEmbedCallsites = 10

// minGoFilesWalked guards the walk the same way. Measured 2026-09-05: 95
// non-test .go files.
const minGoFilesWalked = 60

// ── The analyser ─────────────────────────────────────────────────────────────

// analyseEmbedCallsites reports every reason src's embedding calls are not
// provably sourced from a sanctioned builder, and how many `.Embed(` call sites
// it saw. rel is the repo-relative path, used to look up the file's role.
//
// The invariant it enforces at a writer site: the value handed to the provider
// is the UNMODIFIED return of one bare call to a permitted builder. It is
// satisfied two ways and no others:
//
//	(a) Embed(ctx, Builder(x))                       — the call inline, or
//	(b) v := Builder(x) ... Embed(ctx, v)            — via a local assigned
//	                                                   exactly once in the same
//	                                                   function.
//
// Form (b) exists because the two commands need the composed text for their own
// logging; it requires exactly one assignment so that
// `v := Builder(x); v = raw; Embed(ctx, v)` — which type-checks and reads fine —
// is a violation rather than a pass. A wrapper (`Embed(ctx, wrap(Builder(x)))`)
// fails because the callee name is not a permitted builder, which is the same
// hole that defeated the first cut of the aihub#359 gate.
//
// A parse failure is a violation, never a silent pass.
func analyseEmbedCallsites(rel, src string) (violations []string, embedCalls int) {
	fset := token.NewFileSet()
	// Comments dropped on purpose: the fix leaves comments quoting the removed
	// inline truncation so the next reader knows why it must not come back, and
	// a scan that cannot tell the warning from the offence would force the fix
	// to ship undocumented.
	f, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		return []string{rel + ": does not parse: " + err.Error() +
			" — a file that cannot be parsed cannot be cleared of anything"}, 0
	}

	allowed, isWriter := embedWriteSites[rel]
	_, isReader := embedReadSites[rel]

	type sited struct {
		call   *ast.CallExpr
		method string
	}
	var calls []sited
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !embedProviderMethods[sel.Sel.Name] || len(call.Args) < 2 {
			return true
		}
		calls = append(calls, sited{call, sel.Sel.Name})
		return true
	})
	embedCalls = len(calls)
	if embedCalls == 0 {
		return nil, 0
	}

	if !isWriter && !isReader {
		return []string{rel + ": calls the embedding provider but is neither in embedWriteSites " +
			"nor in embedReadSites. Every embedding call in this repo must be classified: if it " +
			"persists a vector, add it to embedWriteSites with the builder it may use; if it " +
			"embeds a query or is provider/probe code, add it to embedReadSites with the " +
			"reason. Leaving it unclassified is how the write path and the backfill forked " +
			"their truncation rules in the first place (aihub#361)."}, embedCalls
	}
	if isReader {
		return nil, embedCalls
	}

	for _, c := range calls {
		if c.method != "Embed" {
			// EmbedBatch takes a []string, so "the argument is a bare builder
			// call" does not typecheck as a rule. Rather than guess, refuse:
			// a write site that starts batching must teach this gate how to
			// trace each element, and until it does the gate says so out loud
			// instead of waving the call through.
			violations = append(violations, rel+": a write site calls "+c.method+"(...), which "+
				"this gate does not model — it can only trace a single text argument. Keep "+
				"writes on Embed(ctx, Builder(x)), or extend the analyser to walk the slice "+
				"before switching. Silently exempting the batch form would reopen aihub#361 "+
				"through the one call shape nobody checks.")
			continue
		}
		arg := c.call.Args[1]
		switch a := arg.(type) {
		case *ast.CallExpr:
			name := calleeName(a)
			if !slices.Contains(allowed, name) {
				violations = append(violations, rel+": the text handed to the provider is the "+
					"result of "+describeCallee(name)+", which is not one of the builders this "+
					"file may use ("+strings.Join(allowed, ", ")+"). Wrapping the builder is the "+
					"same defect as re-implementing it: the bytes embedded stop being the bytes "+
					"every other writer embeds.")
			}
		case *ast.Ident:
			violations = append(violations, checkLocalSource(rel, f, c.call, a, allowed)...)
		default:
			violations = append(violations, rel+": the text handed to the provider is not a call "+
				"to a builder and not a local variable — it is an expression the analyser cannot "+
				"trace to "+strings.Join(allowed, " or ")+". Before aihub#361 this was literally "+
				"`Embed(ctx, req.Content)`: the raw column value, with none of the truncation the "+
				"backfill applied to the same row.")
		}
	}
	return violations, embedCalls
}

// checkLocalSource verifies form (b): the ident passed to Embed is assigned
// exactly once, from a bare permitted builder call, in the innermost enclosing
// block that assigns it at all.
//
// "Innermost block that assigns it", not "the enclosing function", is
// load-bearing and was found by running this gate against the fixed tree:
// cmd/aihub-embed-backfill has two independent loops in one main(), each with
// its own `embInput`, so a function-wide count sees two assignments and reports
// a defect that is not there. A gate that cries wolf on correct code gets its
// assertion deleted, not its scope fixed.
//
// The walk outward stops at the first scope that assigns, so a builder call
// hoisted above a loop is still traced; and because the count within a scope
// includes its nested blocks, a conditional re-assignment anywhere under the
// scope that supplies the value still trips the "more than once" branch.
func checkLocalSource(rel string, f *ast.File, call *ast.CallExpr, id *ast.Ident, allowed []string) []string {
	chain := enclosingBlockChain(f, call)
	if len(chain) == 0 {
		return []string{rel + ": the .Embed call is not inside any block, so the analyser cannot " +
			"see where its argument came from and must not report it clean"}
	}

	for _, blk := range chain {
		rhs := assignmentsTo(blk, id.Name)
		if len(rhs) == 0 {
			continue // not assigned at this level — look one scope out
		}
		if len(rhs) > 1 {
			return []string{rel + ": " + id.Name + " is assigned " + strconv.Itoa(len(rhs)) +
				" times in the scope that supplies it, so the analyser cannot tell which value " +
				"reaches the provider. Assign the builder's result once and pass it; do not " +
				"post-process it."}
		}
		src, ok := rhs[0].(*ast.CallExpr)
		if !ok {
			return []string{rel + ": " + id.Name + " is handed to the provider but is assigned " +
				"from something that is not a call. Before aihub#361 the backfill built this " +
				"value with an inline `if len([]rune(x)) > 6000` — a second copy of the rule, " +
				"which is what drifted."}
		}
		if name := calleeName(src); !slices.Contains(allowed, name) {
			return []string{rel + ": " + id.Name + " is assigned from " + describeCallee(name) +
				", not from one of " + strings.Join(allowed, ", ") + ". A wrapper around the " +
				"builder re-opens the divergence while every unit test of the builder stays green."}
		}
		return nil
	}

	return []string{rel + ": " + id.Name + " is handed to the provider but is never assigned in " +
		"any enclosing block — it is a parameter or a package-level value, so its text does not " +
		"demonstrably come from " + strings.Join(allowed, " or ") + "."}
}

// assignmentsTo returns the right-hand sides of every assignment to name within
// blk, nested blocks included.
func assignmentsTo(blk *ast.BlockStmt, name string) []ast.Expr {
	var rhs []ast.Expr
	ast.Inspect(blk, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, l := range as.Lhs {
			lid, ok := l.(*ast.Ident)
			if !ok || lid.Name != name {
				continue
			}
			if len(as.Rhs) == len(as.Lhs) {
				rhs = append(rhs, as.Rhs[i])
			} else {
				// Multi-value RHS (v, err := f()): not a bare builder call by
				// shape, and reported as such.
				rhs = append(rhs, as.Rhs[0])
			}
		}
		return true
	})
	return rhs
}

// calleeName returns the bare function name of a call: `f(x)` -> "f",
// `pkg.F(x)` -> "F". Anything else (a call through a variable, a func literal)
// returns "", which is never a permitted builder.
func calleeName(c *ast.CallExpr) string {
	switch fn := c.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

func describeCallee(name string) string {
	if name == "" {
		return "a call the analyser cannot name (through a variable, or a func literal)"
	}
	return name + "(...)"
}

// enclosingBlockChain returns every BlockStmt containing target, innermost
// first. ast.Inspect pushes each node on entry and pops on the nil callback, so
// the stack at the moment target is visited is exactly its ancestor chain.
func enclosingBlockChain(f *ast.File, target ast.Node) []*ast.BlockStmt {
	var stack []ast.Node
	var chain []*ast.BlockStmt
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			return true
		}
		if n == target && !found {
			found = true
			for i := len(stack) - 1; i >= 0; i-- {
				if b, ok := stack[i].(*ast.BlockStmt); ok {
					chain = append(chain, b)
				}
			}
		}
		stack = append(stack, n)
		return true
	})
	return chain
}

// ── The inline-truncation ban ────────────────────────────────────────────────

// runeTruncationsIn reports the rune-truncation shapes in a parsed file: a
// slice expression over a []rune conversion, whether taken directly
// (`[]rune(s)[:n]`) or through a local (`rr := []rune(s); rr[:n]`).
//
// Keyed on the SHAPE, not on the literal 6000: banning the number would be
// satisfied by spelling it `6*1000`, and the defect was never the number — it
// was that the rule existed twice at all. `len([]rune(s))` is deliberately NOT
// matched: counting runes is not truncating them, and the verify command
// legitimately reports content length that way.
func runeTruncationsIn(f *ast.File) int {
	runeLocals := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, l := range as.Lhs {
			lid, ok := l.(*ast.Ident)
			if !ok || i >= len(as.Rhs) {
				continue
			}
			if isRuneConversion(as.Rhs[i]) {
				runeLocals[lid.Name] = true
			}
		}
		return true
	})

	hits := 0
	ast.Inspect(f, func(n ast.Node) bool {
		sl, ok := n.(*ast.SliceExpr)
		if !ok {
			return true
		}
		switch x := sl.X.(type) {
		case *ast.Ident:
			if runeLocals[x.Name] {
				hits++
			}
		default:
			if isRuneConversion(sl.X) {
				hits++
			}
		}
		return true
	})
	return hits
}

// isRuneConversion reports whether e is a `[]rune(...)` conversion.
func isRuneConversion(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	arr, ok := call.Fun.(*ast.ArrayType)
	if !ok || arr.Len != nil {
		return false
	}
	elt, ok := arr.Elt.(*ast.Ident)
	return ok && elt.Name == "rune"
}

func mustParseEmbedFixture(t *testing.T, name, src string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v — an unparseable fixture proves nothing", name, err)
	}
	return f
}

// ── Fixtures: the shapes this gate exists to reject ──────────────────────────

// embedFixtureBareContent is internal/domain/memory.go as it shipped before
// aihub#361: the raw column value straight to the provider.
const embedFixtureBareContent = `package domain
func Remember(ctx C, req R) {
	if embeddableType(req.Type) {
		if vec, err := embProvider.Embed(ctx, req.Content); err != nil {
			_ = vec
		}
	}
}
`

// embedFixtureInlineTruncation is cmd/aihub-embed-backfill/main.go's memory loop as
// it shipped: its own copy of the budget, inline.
const embedFixtureInlineTruncation = `package main
func main() {
	for _, it := range todo {
		embInput := it.content
		if rr := []rune(embInput); len(rr) > 6000 {
			embInput = string(rr[:6000])
		}
		vec, embErr := prov.Embed(ctx, embInput)
		_, _ = vec, embErr
	}
}
`

// embedFixtureWrapped is the evasion that defeated the first cut of the aihub#359
// gate, transposed: the builder IS called, but its result is post-processed on
// the way to the provider.
const embedFixtureWrapped = `package main
func main() {
	vec, embErr := prov.Embed(ctx, normalise(domain.MemoryEmbedInput(it.content)))
	_, _ = vec, embErr
}
`

// embedFixtureReassigned is the same evasion through a local: assigned from the
// builder, then overwritten with the raw text.
const embedFixtureReassigned = `package main
func main() {
	embInput := domain.MemoryEmbedInput(it.content)
	embInput = it.content
	vec, embErr := prov.Embed(ctx, embInput)
	_, _ = vec, embErr
}
`

// embedFixtureWrongBuilder uses a sanctioned builder that this file may not use —
// composing work-item text for a memories row.
const embedFixtureWrongBuilder = `package domain
func Remember(ctx C, req R) {
	if vec, err := embProvider.Embed(ctx, WorkItemEmbedInput(req.Goal, req.Content)); err != nil {
		_ = vec
	}
}
`

// embedFixtureCleanInline / embedFixtureCleanLocal are the two permitted shapes. They
// exist so a BROKEN analyser — one that reports violations unconditionally — is
// caught too. A gate that can never pass gets deleted as fast as one that can
// never fail.
const embedFixtureCleanInline = `package domain
func Remember(ctx C, req R) {
	if embeddableType(req.Type) {
		if vec, err := embProvider.Embed(ctx, MemoryEmbedInput(req.Content)); err != nil {
			_ = vec
		}
	}
}
`

const embedFixtureCleanLocal = `package main
func main() {
	for _, it := range todo {
		embInput := domain.MemoryEmbedInput(it.content)
		vec, embErr := prov.Embed(ctx, embInput)
		_, _ = vec, embErr
	}
}
`

// embedFixtureTwoLoops is cmd/aihub-embed-backfill's real shape: two
// independent loops in one main(), each declaring its own embInput. A
// function-scoped assignment count calls this a defect; it is not one. This
// fixture is here because the first cut of this gate did exactly that against
// the FIXED tree — the false positive is as fatal to a gate as a false negative,
// because the fix for it is to delete the assertion.
const embedFixtureTwoLoops = `package main
func main() {
	for _, it := range todo {
		embInput := domain.MemoryEmbedInput(it.content)
		vec, embErr := prov.Embed(ctx, embInput)
		_, _ = vec, embErr
	}
	for _, r := range wtodo {
		embInput := domain.WorkItemEmbedInput(r.goal, r.content)
		vec, embErr := prov.Embed(ctx, embInput)
		_, _ = vec, embErr
	}
}
`

// embedFixtureHoisted assigns the builder result OUTSIDE the loop that embeds
// it. The innermost block assigns nothing, so the analyser must keep walking
// outward rather than report "never assigned".
const embedFixtureHoisted = `package main
func main() {
	embInput := domain.MemoryEmbedInput(it.content)
	for range todo {
		vec, embErr := prov.Embed(ctx, embInput)
		_, _ = vec, embErr
	}
}
`

// embedFixtureConditionalOverwrite hides the overwrite in a nested if. The
// scope that supplies the value is the loop body, and the count within it
// includes nested blocks, so this must still be rejected.
const embedFixtureConditionalOverwrite = `package main
func main() {
	for _, it := range todo {
		embInput := domain.MemoryEmbedInput(it.content)
		if len(it.content) < 100 {
			embInput = it.content
		}
		vec, embErr := prov.Embed(ctx, embInput)
		_, _ = vec, embErr
	}
}
`

// TestEmbedWriterAnalyserIsCalibrated runs the analyser over sources whose
// answer is already known, BEFORE any test below trusts it against the real
// repo. This is the whole anti-vacuity story: a clean census is evidence only
// if the analyser can be shown to separate the two classes.
func TestEmbedWriterAnalyserIsCalibrated(t *testing.T) {
	bad := []struct{ name, rel, src string }{
		{"pre-fix live path: raw content, no builder", "internal/domain/memory.go", embedFixtureBareContent},
		{"pre-fix backfill: its own inline truncation", "cmd/aihub-embed-backfill/main.go", embedFixtureInlineTruncation},
		{"builder result wrapped on the way to the provider", "cmd/aihub-embed-backfill/main.go", embedFixtureWrapped},
		{"local assigned from the builder, then overwritten", "cmd/aihub-embed-backfill/main.go", embedFixtureReassigned},
		{"memory row embedded with the work-item composer", "internal/domain/memory.go", embedFixtureWrongBuilder},
		{"an Embed call in a file nobody classified", "internal/domain/somewhere_new.go", embedFixtureCleanInline},
		{"overwrite hidden in a nested if", "cmd/aihub-embed-backfill/main.go", embedFixtureConditionalOverwrite},
	}
	for _, c := range bad {
		got, n := analyseEmbedCallsites(c.rel, c.src)
		if n == 0 {
			t.Errorf("analyser found no .Embed call at all in the fixture %q; it is matching the "+
				"wrong shape and would report every real file clean", c.name)
			continue
		}
		if len(got) == 0 {
			t.Errorf("analyser reported CLEAN on a fixture that is not: %s\n"+
				"It therefore cannot detect that shape in the shipped tree either, and every "+
				"clean result it gives below is meaningless.", c.name)
		}
	}

	good := []struct{ name, rel, src string }{
		{"builder called inline", "internal/domain/memory.go", embedFixtureCleanInline},
		{"builder result passed through one local", "cmd/aihub-embed-backfill/main.go", embedFixtureCleanLocal},
		{"two independent loops, one local name each", "cmd/aihub-embed-backfill/main.go", embedFixtureTwoLoops},
		{"builder call hoisted above the embedding loop", "cmd/aihub-embed-backfill/main.go", embedFixtureHoisted},
	}
	for _, c := range good {
		if got, _ := analyseEmbedCallsites(c.rel, c.src); len(got) != 0 {
			t.Errorf("analyser reported violations on a known-good fixture (%s): %v\n"+
				"An analyser that cannot pass anything is not a gate, it is noise.", c.name, got)
		}
	}

	// The truncation detector, calibrated the same way.
	if n := runeTruncationsIn(mustParseEmbedFixture(t, "bad.go", embedFixtureInlineTruncation)); n == 0 {
		t.Error("runeTruncationsIn does not fire on the pre-aihub#361 backfill loop it exists to " +
			"reject; keyed on the wrong shape, it would clear every file")
	}
	if n := runeTruncationsIn(mustParseEmbedFixture(t, "good.go", embedFixtureCleanLocal)); n != 0 {
		t.Errorf("runeTruncationsIn fired %d time(s) on a file that truncates nothing", n)
	}
	// len([]rune(s)) is a count, not a truncation, and the verify command uses
	// it. If the detector cannot tell them apart the ban below is unusable.
	const counting = "package main\nfunc f() { n := len([]rune(s)); _ = n }\n"
	if n := runeTruncationsIn(mustParseEmbedFixture(t, "count.go", counting)); n != 0 {
		t.Errorf("runeTruncationsIn treats a rune COUNT as a truncation (%d hits); the ban would "+
			"then fail files that are doing nothing wrong", n)
	}
}

// ── The census ───────────────────────────────────────────────────────────────

type embedCensus struct {
	violations  []string
	callsites   int
	filesWalked int
	perFile     map[string]int
}

func censusEmbedCallsites(t *testing.T) embedCensus {
	t.Helper()
	root := filepath.Join("..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root not found at %s (no go.mod): %v — the census would walk the wrong "+
			"tree and report a clean bill of health for it", root, err)
	}

	c := embedCensus{perFile: map[string]int{}}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			// Nothing else is skipped on purpose: an exclusion list is a place
			// for a fifth writer to hide. These four hold no first-party Go.
			case ".git", "node_modules", "vendor", ".codegraph":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		c.filesWalked++
		v, n := analyseEmbedCallsites(rel, string(b))
		c.violations = append(c.violations, v...)
		c.callsites += n
		if n > 0 {
			c.perFile[rel] = n
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v. The census did not complete, so 'no violations' below would "+
			"mean 'not looked for'.", root, err)
	}
	return c
}

// TestEveryEmbedWriteIsSourcedFromTheSharedBuilder is the gate.
func TestEveryEmbedWriteIsSourcedFromTheSharedBuilder(t *testing.T) {
	c := censusEmbedCallsites(t)

	if c.filesWalked < minGoFilesWalked {
		t.Fatalf("census visited only %d non-test .go files (floor %d). A walk that stops early "+
			"reports no violations perfectly.", c.filesWalked, minGoFilesWalked)
	}
	if c.callsites < minEmbedCallsites {
		t.Fatalf("census found only %d .Embed( call sites (floor %d). Either the provider "+
			"interface was renamed — in which case this gate is matching nothing and must be "+
			"updated — or the walk is not seeing the tree.", c.callsites, minEmbedCallsites)
	}

	// Positive anchor, per writer: each declared write site must still contain
	// an embedding call. Without this the gate passes when a writer moves its
	// Embed elsewhere, which is precisely the fifth-copy scenario.
	for rel := range embedWriteSites {
		if c.perFile[rel] == 0 {
			t.Errorf("%s is declared an embedding write site but the census found no .Embed( "+
				"call in it. Either the write moved — follow it and update embedWriteSites — or "+
				"the file was renamed. A write site the census cannot see is a write site this "+
				"gate does not guard.", rel)
		}
	}

	sort.Strings(c.violations)
	for _, v := range c.violations {
		t.Error(v)
	}
}

// TestEmbedReadSiteExemptionsAreEarned keeps the escape hatch expensive.
//
// embedReadSites is the only way to make the census ignore an embedding call,
// so it is exactly where a future writer would hide — one line, no argument
// required. Two things make that line cost something. First, the reason must be
// a real sentence, written in this file, next to the ban it suspends. Second,
// the entry must still describe reality: a path that no longer exists, or that
// no longer calls the provider, is an exemption doing nothing except waiting to
// exempt something else if the filename is ever reused.
func TestEmbedReadSiteExemptionsAreEarned(t *testing.T) {
	const minReasonLen = 40

	root := filepath.Join("..", "..")
	for rel, reason := range embedReadSites {
		if len(reason) < minReasonLen {
			t.Errorf("the embedReadSites entry for %s has a %d-character reason (minimum %d). "+
				"Exempting a call from the parity census is a claim that it does not persist a "+
				"vector; state it in a sentence a reviewer can disagree with.", rel, len(reason), minReasonLen)
		}
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Errorf("the embedReadSites entry for %s names a file that cannot be read: %v. "+
				"Delete the stale exemption — leaving it means the census silently forgives "+
				"whatever takes that path next.", rel, err)
			continue
		}
		if _, n := analyseEmbedCallsites("unclassified-probe.go", string(b)); n == 0 {
			t.Errorf("%s is exempted from the parity census but no longer calls the embedding "+
				"provider. An exemption that covers nothing is not harmless: it is a pre-approved "+
				"hole waiting for the next edit to that file.", rel)
		}
	}
}

// TestNoInlineRuneTruncationInEmbedWriters bans the second copy of the budget
// in the files that have historically grown one. The census above already
// requires the builder to supply the text; this catches the halfway state where
// a writer calls the builder AND then trims the result again.
func TestNoInlineRuneTruncationInEmbedWriters(t *testing.T) {
	// Negative control first: the ban must fire on the code it exists to reject.
	if n := runeTruncationsIn(mustParseEmbedFixture(t, "prefix.go", embedFixtureInlineTruncation)); n == 0 {
		t.Fatal("the truncation ban does not fire on the pre-aihub#361 backfill body it exists " +
			"to reject; it would clear every tree")
	}

	root := filepath.Join("..", "..")
	for rel := range embedWriteSites {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Errorf("read %s: %v — this gate cannot pass by not finding its target", rel, err)
			continue
		}
		if n := runeTruncationsIn(mustParseEmbedFixture(t, rel, string(b))); n != 0 {
			t.Errorf("%s truncates runes inline (%d site(s)). The rune budget lives in %s and "+
				"nowhere else: this file had its own copy before aihub#361, and a copy is what "+
				"drifted — the live write path capped nothing while the backfill capped at 6000, "+
				"so the same row got a full-text vector from one writer and a prefix vector from "+
				"the other under an identical emb_model.", rel, n, embedBuilderFile)
		}
	}
}

// TestSharedEmbedBuilderIsDeclaredOnce is the positive anchor for the whole
// file. Every assertion above is satisfiable by deleting the builders and the
// writers with them; this one is not.
func TestSharedEmbedBuilderIsDeclaredOnce(t *testing.T) {
	root := filepath.Join("..", "..")
	b, err := os.ReadFile(filepath.Join(root, embedBuilderFile))
	if err != nil {
		t.Fatalf("read %s: %v. The whole point of aihub#361 is that this file exists and is the "+
			"only place the embedding input rule is written down.", embedBuilderFile, err)
	}
	f := mustParseEmbedFixture(t, embedBuilderFile, string(b))

	declared := map[string]bool{}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil {
			declared[fn.Name.Name] = true
		}
	}
	for _, want := range []string{memoryEmbedBuilder, workItemEmbedBuilder} {
		if !declared[want] {
			t.Errorf("%s does not declare func %s. Both writers are asserted above to source "+
				"their text from it; if it moved, move this anchor with it rather than letting "+
				"the census clear a builder it cannot see.", embedBuilderFile, want)
		}
	}

	// The budget must be applied here, and this is the ONE file where the
	// truncation shape is expected — so the ban above deliberately excludes it,
	// and this assertion is the other half: the shape has to be here, or the
	// cap has silently gone away and every over-long row goes back to failing
	// the provider on length and landing as NULL.
	if n := runeTruncationsIn(f); n == 0 {
		t.Errorf("%s no longer truncates anything. The cap is not cosmetic: without it an "+
			"over-long memory fails the provider with \"input length exceeds the context "+
			"length\", Remember logs a warning, and the row is stored with emb_vector NULL — "+
			"permanently invisible to vector recall (aihub#361).", embedBuilderFile)
	}
}
