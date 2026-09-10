package domain

// aihub#543 probe wave 2 — the four claims of `docs/mcp-cards/pf_recall.md`
// whose subject is SQL this package builds:
//
//	"The visibility predicates that DO exist on both recall paths are
//	 authorization scoping derived from `CallerRole`/`CallerUserID` — `AND
//	 (visibility != 'private' OR author_user_id = $n)` and `AND visibility !=
//	 'admin'`, SQL literals rather than the caller's filter."
//	    -> TestRecallVisibilityScopingIsCallerDerivedOnBothPaths
//	"Recency is the only ranking SIGNAL — `id DESC` is the deterministic
//	 tiebreaker for rows sharing a reference time … not a second signal."
//	"text, `recall_algo=lexical` — `ts_rank` first, then `tanh` of effective
//	 strength, which carries `exp(-days/stability)`."
//	    -> TestRecallOrderingSignalsAreTheOnesTheCardNames
//	"`cursor` works on the TEXT path only. The vector path and the hybrid merge
//	 both set an empty cursor and return a nil `next_cursor` …"
//	    -> TestRecallCursorIsPromisedOnTheTextPathOnly
//	"`work_item_id` takes an id OR a slug …" (the sentence this wave CORRECTED —
//	 see the note below)
//	    -> TestRecallResolvesTheWorkItemReferenceBeforeAnyColumnComparison
//
// ─── 🔴 One of those sentences was measured FALSE ──────────────────────────
//
// Until this wave the card said, in the hop-4 list:
//
//	"**`work_item_id` must be the canonical id**; a slug matches nothing and
//	 answers 200 with an empty list."
//
// That was true when it was written and stopped being true at aihub#363, which
// put resolveRecallWorkItemRef at the single domain entry every RecallRequest
// builder shares. The card kept the pre-fix sentence, so the published advice
// told callers to spend a pf_get_work_item they no longer need — and the same
// stale claim still sits in pf_get_step's card and in pf_get_step's own
// published DESCRIPTION, which are outside this slice and reported rather than
// edited here. The prose is corrected and the corrected claim is probed, in both
// places it can go wrong: the resolution's ORDERING here, and the answer itself
// end-to-end in internal/server/recall_work_item_slug_db_test.go
// (TestRecallResolvesWorkItemIdOrSlug, DB-gated and already registered).
//
// ─── Why these are source-shaped rather than driven ────────────────────────
//
// All four are claims about SQL the caller never sees and about which of two
// paths builds it. recallText, recallWithVector and mergeRecallHalves are
// unexported and two of the three need a live Postgres and an embedding provider
// to run at all, so the DB-gated ranking tests in this package are the ones that
// observe the ORDER, and this file asserts the property that makes the order
// meaningful: which signals the SQL is allowed to name, and where the value
// bound next to a visibility predicate comes from. §3.3's first rule — prefer
// non-DB, and prefer it hard — is what picks the shape; the enforcement halves
// below all fail on a one-line edit to the query text.
//
// Every expectation is read off the CARD, so neither half of the contract can
// move alone: a predicate reworded in the card reddens the arm even with the SQL
// untouched.
//
//	GOWORK=off go test ./internal/domain/ -run 'TestRecall(Visibility|Ordering|Cursor|Resolves)' -count=1 -v

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// recallCardPath is the published side of every claim in this file.
const recallCardPath = "../../docs/mcp-cards/pf_recall.md"

// recallTextPathFile and recallVectorPathFile are "both recall paths" as the
// card means them: the text/tag query and the embedding query. Named as
// constants because three arms below quantify over the pair, and a pair written
// out at each site is a pair that grows a third member in only one of them.
const (
	recallTextPathFile   = "memory.go"
	recallVectorPathFile = "memory_vector.go"
)

// readRecallCard returns the card's PROSE with every run of whitespace collapsed
// to one space.
//
// 🔴 Two normalisations, both load-bearing. The fenced machine block goes first,
// because it opens with three backticks and every backtick pairing after it
// would be off by one — the arms below read the card's quoted SQL out of
// backticked spans, so with the fence left in they find zero spans and report
// the card as having stopped publishing claims it still publishes. And the
// whitespace is collapsed because the card WRAPS inside backticks: the
// private-visibility predicate it quotes is split across two source lines, so a
// comparison against the raw bytes is a comparison against the card's line width.
func readRecallCard(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(recallCardPath)
	if err != nil {
		t.Fatalf("read %s: %v — the card is the published side of every claim in this "+
			"file, so failing to read it is a failure and not an empty pass", recallCardPath, err)
	}
	var kept []string
	inFence := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			kept = append(kept, line)
		}
	}
	if inFence {
		t.Fatalf("%s has an unclosed fenced block, so the prose this arm reads ends "+
			"wherever the fence opened", recallCardPath)
	}
	return strings.Join(strings.Fields(strings.Join(kept, "\n")), " ")
}

// cardBackticked returns the whitespace-collapsed contents of every backticked
// span in the card that matches re.
func cardBackticked(t *testing.T, re *regexp.Regexp) []string {
	t.Helper()
	var out []string
	for _, m := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(readRecallCard(t), -1) {
		span := strings.Join(strings.Fields(m[1]), " ")
		if re.MatchString(span) {
			out = append(out, span)
		}
	}
	return out
}

// renderStringExpr renders a Go expression that is entirely string constants
// back into the SQL it produces.
//
// 🔴 A plain string-literal walk is NOT enough here, and the existing
// reference-time guard in this package says why in its own words: it "cannot see
// through a query assembled from several literals — that is a known limit, not a
// claim of completeness". Every ORDER BY this file is about is spliced —
// "ORDER BY " + memRefTimeSQL + " DESC, id DESC" — so a literal walk sees three
// fragments, none of which is the clause the card publishes, and the arm would
// report the ordering as missing while it sat there working.
//
// Only the identifiers this file's claims actually depend on are resolved. An
// unknown identifier makes the render FAIL rather than silently render a hole,
// because a hole is what a comparison then matches nothing against.
func renderStringExpr(expr ast.Expr) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(e.Value)
		if err != nil {
			return "", false
		}
		return v, true
	case *ast.Ident:
		if e.Name == "memRefTimeSQL" {
			return memRefTimeSQL, true
		}
		return "", false
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		left, okL := renderStringExpr(e.X)
		right, okR := renderStringExpr(e.Y)
		if !okL || !okR {
			return "", false
		}
		return left + right, true
	}
	return "", false
}

// domainSQLStrings returns every string-constant expression in one non-test file
// of this package, spliced and whitespace-collapsed.
//
// Comments are deliberately NOT included: memory.go's comments quote several
// ORDER BY clauses, including ones this package is forbidden to use, so a text
// scan would let a comment satisfy an assertion about the query.
func domainSQLStrings(t *testing.T, file string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.BasicLit, *ast.BinaryExpr:
			if s, ok := renderStringExpr(n.(ast.Expr)); ok {
				out = append(out, strings.Join(strings.Fields(s), " "))
			}
		}
		return true
	})
	if len(out) == 0 {
		t.Fatalf("%s yielded no string constants at all — the parse walked nothing and every "+
			"assertion below would be vacuous", file)
	}
	return out
}

// TestRecallVisibilityScopingIsCallerDerivedOnBothPaths is the hop-4 sentence
// about what the `visibility` clauses in the recall SQL actually are.
//
// 🔴 Three assertions, and none of them implies the others. That the two
// predicates are PRESENT says nothing about where the value beside them comes
// from; that they are guarded by the caller's ROLE says nothing about whether a
// caller could also supply one; and neither notices a `Visibility` field arriving
// on RecallRequest, which is precisely how aihub#484's withdrawn parameter got
// there in the first place. The card's sentence claims all three — the clauses
// exist, they are derived from CallerRole/CallerUserID, and they are "SQL
// literals rather than the caller's filter" — so an arm holding one of the three
// would be an arm whose title over-claims.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M1  memory_vector.go drops `AND visibility != 'admin'`            RED (presence)
//	M2  the private predicate's guard becomes `req.Query != ""`       RED (caller-derived)
//	M3  RecallRequest grows a bindable `Visibility` field             RED (no filter)
//	M4  the card rewords the predicate to `visibility <> 'private'`   RED (publication)
//	M5  green control: reword the prose AROUND the two quoted
//	    predicates without touching them                              GREEN
func TestRecallVisibilityScopingIsCallerDerivedOnBothPaths(t *testing.T) {
	// The card quotes both predicates in backticks. `$n` is the card's spelling
	// of a bind placeholder, so the comparison stops at the `$`: the index is a
	// property of how many filters preceded it, not of this claim.
	quoted := cardBackticked(t, regexp.MustCompile(`^AND .*visibility`))
	if len(quoted) < 2 {
		t.Fatalf("%s quotes %d visibility predicate(s) in backticks, want the 2 its hop-4 "+
			"sentence names (%v). Without them there is nothing published to compare the SQL "+
			"against, and this arm would assert only that the SQL matches itself.",
			recallCardPath, len(quoted), quoted)
	}
	for _, path := range []string{recallTextPathFile, recallVectorPathFile} {
		lits := strings.ToLower(strings.Join(domainSQLStrings(t, path), "\n"))
		for _, want := range quoted {
			// Cut at the bind placeholder; `$n` in the card is `$%d` in the source.
			needle := strings.ToLower(want)
			if i := strings.Index(needle, "$"); i >= 0 {
				needle = needle[:i]
			}
			if !strings.Contains(lits, needle) {
				t.Errorf("%s: no SQL literal carries the predicate %s publishes as %q. Both "+
					"recall paths are supposed to scope identically, so a predicate present on "+
					"one and missing from the other hands one path's callers rows the other "+
					"path withholds — and the response is byte-identical in shape either way.",
					path, recallCardPath, want)
			}
		}
	}

	// Derived from the CALLER: every visibility predicate sits under a guard that
	// reads CallerRole, and the private half binds CallerUserID.
	guards := 0
	for _, path := range []string{recallTextPathFile, recallVectorPathFile} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ifStmt, ok := n.(*ast.IfStmt)
			if !ok {
				return true
			}
			var body, cond strings.Builder
			ast.Inspect(ifStmt.Body, func(inner ast.Node) bool {
				if lit, isLit := inner.(*ast.BasicLit); isLit && lit.Kind == token.STRING {
					body.WriteString(strings.ToLower(lit.Value))
				}
				return true
			})
			if !strings.Contains(body.String(), "visibility !=") {
				return true
			}
			ast.Inspect(ifStmt.Cond, func(inner ast.Node) bool {
				if id, isID := inner.(*ast.Ident); isID {
					cond.WriteString(id.Name)
					cond.WriteString(" ")
				}
				return true
			})

			guards++
			// Matched case-insensitively on the NAME, not on `req.CallerRole`
			// exactly: loadForwardRelations takes the same value as a plain
			// `callerRole` parameter and its own comment says it mirrors this
			// predicate, so an exact-spelling check would report the one site in
			// the file that copied the rule correctly.
			if !strings.Contains(strings.ToLower(cond.String()), "callerrole") {
				t.Errorf("%s:%d: a visibility predicate is emitted under `if %s`, which does not "+
					"read CallerRole. The card calls these clauses authorization scoping the "+
					"server derives from the caller's own role and id — a predicate switched on "+
					"anything else is a caller-facing filter wearing an authorization clause's "+
					"clothes, and it narrows the page identically, so no response says which it was.",
					path, fset.Position(ifStmt.Pos()).Line, strings.TrimSpace(cond.String()))
			}
			return true
		})
	}
	if guards == 0 {
		t.Fatal("not one visibility predicate was found inside an if-statement in either recall " +
			"path. The walk found nothing, so the caller-derived half of this arm asserted " +
			"nothing — and a query with no visibility scoping at all would pass it.")
	}

	// "rather than the caller's filter": no field a caller could bind.
	rt := reflect.TypeOf(RecallRequest{})
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		tag := field.Tag.Get("json")
		if tag == "-" {
			// CallerUserID/CallerRole are server-set and unbindable by
			// construction, which is the distinction this arm is about.
			continue
		}
		if strings.Contains(strings.ToLower(field.Name), "visibility") ||
			strings.Contains(strings.ToLower(tag), "visibility") {
			t.Errorf("RecallRequest binds %s (json %q), so a caller CAN name a visibility "+
				"filter again. aihub#484 withdrew that parameter after measuring that all four "+
				"hops but the last were intact — a bindable field is how it looked from every "+
				"gate in the tree while nothing read it, and the card now states in two places "+
				"that no caller-facing filter exists.", field.Name, tag)
		}
	}
}

// splitSQLTerms splits an ORDER BY list on the commas that separate TERMS,
// ignoring the ones inside a function call.
//
// 🔴 This is the whole reason the term COUNT is trustworthy. A plain
// strings.Split on ", " reads `GREATEST(last_activated_at, created_at) DESC, id
// DESC` as three terms, so a count of 2 would have been satisfied by an ordering
// with one real term — and the arm below reports "recency is the only signal"
// off that number.
func splitSQLTerms(clause string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range clause {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(clause[start:i]))
				start = i + 1
			}
		}
	}
	if last := strings.TrimSpace(clause[start:]); last != "" {
		out = append(out, last)
	}
	return out
}

// recallOrderByClauses returns the ORDER BY clause of every SQL literal in one
// non-test file that carries one, whitespace-collapsed and cut at LIMIT.
func recallOrderByClauses(t *testing.T, file string) []string {
	t.Helper()
	var out []string
	for _, lit := range domainSQLStrings(t, file) {
		i := strings.Index(strings.ToLower(lit), "order by ")
		if i < 0 {
			continue
		}
		clause := lit[i:]
		if j := strings.Index(strings.ToUpper(clause), " LIMIT "); j > 0 {
			clause = clause[:j]
		}
		out = append(out, strings.Join(strings.Fields(clause), " "))
	}
	return out
}

// TestRecallOrderingSignalsAreTheOnesTheCardNames holds the two ranking
// sentences the card writes as SQL.
//
// 🔴 The discriminating half is the COUNT of ordering terms, not their content.
// "Recency is the only ranking SIGNAL" is false the moment a third term appears,
// and a third term is exactly what the aihub#311 fused score was — it was added,
// it ranked a higher-cosine row second, and every test in this package stayed
// green because each of them asserted the presence of a term rather than the
// absence of the others. An arm that checks `memRefTimeSQL` appears would pass
// against `ORDER BY memRefTimeSQL DESC, tanh(base_strength) DESC, id DESC`,
// which is the claim's negation.
//
// The default clause is built from memRefTimeSQL rather than spelled out, so the
// day that constant moves the card's own quoted SQL is what goes red — never the
// test's literal, which is the base-strength precedent §3.4 states.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M1  the default ORDER BY grows `, base_strength DESC`             RED (term count)
//	M2  the lexical ORDER BY swaps ts_rank and the tanh term          RED (order)
//	M3  the lexical ORDER BY drops the exp() decay factor             RED (decay)
//	M4  memRefTimeSQL becomes COALESCE(last_activated_at, created_at) RED (card mismatch)
//	M5  the card's quoted default ORDER BY says `created_at DESC`     RED (publication)
//	M6  the card's lexical bullet stops naming `tanh`                 RED (publication)
//	M7  green control: reword the bullet's trailing prose             GREEN
func TestRecallOrderingSignalsAreTheOnesTheCardNames(t *testing.T) {
	card := readRecallCard(t)

	// ── the default text path ────────────────────────────────────────────────
	wantDefault := "ORDER BY " + memRefTimeSQL + " DESC, id DESC"
	if !strings.Contains(card, "`"+wantDefault+"`") {
		t.Errorf("%s does not publish the default ordering as %q.\nThe expectation is BUILT from "+
			"memRefTimeSQL, so this fires in both directions: the constant moved and the card "+
			"did not, or the card was reworded and the SQL was not. Hard-coding the clause here "+
			"instead would go green on the one day it was needed.", recallCardPath, wantDefault)
	}
	clauses := recallOrderByClauses(t, recallTextPathFile)
	if len(clauses) < 2 {
		t.Fatalf("%s yielded %d ORDER BY clause(s), want at least the default and the lexical "+
			"one this card describes (%v). A walk that finds one of the two cannot tell which "+
			"it found, and the assertions below would report the other as missing.",
			recallTextPathFile, len(clauses), clauses)
	}
	var gotDefault, gotLexical string
	for _, c := range clauses {
		switch {
		case strings.Contains(c, memRefTimeSQL+" DESC, id DESC"):
			gotDefault = c
		case strings.Contains(strings.ToLower(c), "ts_rank"):
			gotLexical = c
		}
	}
	if gotDefault == "" {
		t.Errorf("no ORDER BY in %s orders by %q — the card publishes that clause verbatim, so "+
			"either the SQL stopped using the shared reference-time expression or it stopped "+
			"carrying the `id DESC` tiebreaker aihub#239 added, and the cursor key is derived "+
			"from that tiebreaker.", recallTextPathFile, memRefTimeSQL+" DESC, id DESC")
	} else if terms := splitSQLTerms(strings.TrimPrefix(gotDefault, "ORDER BY ")); len(terms) != 2 {
		t.Errorf("the default recall ordering has %d term(s) (%v), want exactly 2.\nThe card says "+
			"recency is the ONLY ranking signal and `id DESC` is a deterministic tiebreaker "+
			"rather than a second signal. A third term makes that sentence false while every "+
			"presence check in this package stays green — which is the shape aihub#311's fused "+
			"score had.", len(terms), terms)
	}

	// ── the lexical path ─────────────────────────────────────────────────────
	for _, token := range []string{"ts_rank", "tanh", "exp(-days/stability)"} {
		if !strings.Contains(card, "`"+token+"`") {
			t.Errorf("%s no longer names `%s` in its lexical-ordering bullet. The three tokens "+
				"ARE the published claim about that path; dropping one from the card is how the "+
				"SQL half below stops being checked without any test changing.",
				recallCardPath, token)
		}
	}
	if gotLexical == "" {
		t.Fatalf("no ORDER BY in %s mentions ts_rank, so the lexical path the card describes "+
			"either moved or lost its relevance term, and the assertions below have nothing to "+
			"read.", recallTextPathFile)
	}
	lexTerms := splitSQLTerms(strings.TrimPrefix(gotLexical, "ORDER BY "))
	if len(lexTerms) != 2 {
		t.Fatalf("the lexical ordering has %d term(s) (%v), want the 2 the card names: ts_rank "+
			"and the decayed strength term. A third term is a ranking signal no hop publishes.",
			len(lexTerms), lexTerms)
	}
	if !strings.Contains(strings.ToLower(lexTerms[0]), "ts_rank") {
		t.Errorf("the lexical ordering's FIRST term is %q, which is not the ts_rank relevance "+
			"score. The card says ts_rank first and the strength term second: reversing them "+
			"makes strength the primary signal on a path whose whole purpose is textual "+
			"relevance, and no response field says which order produced a page.", lexTerms[0])
	}
	second := strings.ToLower(lexTerms[1])
	if !strings.Contains(second, "tanh(") {
		t.Errorf("the lexical ordering's SECOND term is %q and is not a tanh of effective "+
			"strength, which is what the card publishes", lexTerms[1])
	}
	if !strings.Contains(second, "exp(") {
		t.Errorf("the lexical ordering's second term is %q and carries no exp() factor, so the "+
			"strength term no longer decays. The card promises `exp(-days/stability)` inside "+
			"it, and without the decay an old, heavily activated memory outranks a fresh one "+
			"forever.", lexTerms[1])
	}
}

// TestRecallCursorIsPromisedOnTheTextPathOnly holds the hop-4 sentence about
// which paths can page.
//
// 🔴 mergeRecallHalves is driven with a cursor on BOTH halves, which is the
// discriminating input. An arm that merges two cursorless responses and finds no
// cursor asserts nothing at all — the result had nowhere to get one from — and
// that vacuous shape is what "the hybrid merge sets an empty cursor" would
// otherwise be tested by.
//
// The response key is read out of the card rather than written here, so renaming
// it in the card reddens the arm: an absence claim about a key nobody publishes
// is satisfied by every possible response.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M1  mergeRecallHalves carries vec.NextCursor through              RED (merge)
//	M2  recallWithVector's return sets NextCursor                     RED (vector path)
//	M3  the card renames the response key to `cursor_next`            RED (publication)
//	M4  green control: the card rewords the sentence around the
//	    same key name                                                 GREEN
func TestRecallCursorIsPromisedOnTheTextPathOnly(t *testing.T) {
	// The key, off the card. RecallResponse must really publish it, or the
	// absence assertions below are about a name nothing carries.
	key := ""
	for _, span := range cardBackticked(t, regexp.MustCompile(`^next_?cursor$|_cursor$`)) {
		key = span
		break
	}
	if key == "" {
		t.Fatalf("%s names no `*_cursor` response key in backticks, so this arm has no published "+
			"name to check the two paths against", recallCardPath)
	}
	rt := reflect.TypeOf(RecallResponse{})
	found := false
	for i := 0; i < rt.NumField(); i++ {
		if strings.Split(rt.Field(i).Tag.Get("json"), ",")[0] == key {
			found = true
		}
	}
	if !found {
		t.Fatalf("%s promises a %q response key and RecallResponse publishes no field with that "+
			"json name. Either the card renamed it or the field did; an absence check against a "+
			"key no response can carry passes for free.", recallCardPath, key)
	}

	// The merge, driven with a cursor on both halves.
	ts := "2026-09-10T00:00:00.000000000Z|mem_left"
	other := "2026-09-10T00:00:00.000000000Z|mem_right"
	merged := mergeRecallHalves(
		&RecallResponse{Items: []MemoryWithStrength{{Memory: Memory{ID: "v1"}}}, Total: 1, NextCursor: &ts},
		&RecallResponse{Items: []MemoryWithStrength{{Memory: Memory{ID: "t1"}}}, Total: 1, NextCursor: &other},
		5)
	if merged.NextCursor != nil {
		t.Errorf("mergeRecallHalves returned %s=%q. The card promises paging works on the TEXT "+
			"path only, and a hybrid recall that hands back a cursor invites a follow-up that "+
			"returns page one forever — a loop the caller cannot detect, because each page looks "+
			"like a valid answer.", key, *merged.NextCursor)
	}
	if len(merged.Items) != 2 {
		t.Fatalf("the merge dropped items (%d, want 2), so the input above was not the "+
			"two-populated-halves case this arm needs and the nil above proves nothing",
			len(merged.Items))
	}

	// The vector path, which cannot be driven without Postgres and an embedding
	// provider: assert no RecallResponse it builds sets the field at all.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, recallVectorPathFile, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", recallVectorPathFile, err)
	}
	built := 0
	ast.Inspect(f, func(n ast.Node) bool {
		comp, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if id, isID := comp.Type.(*ast.Ident); !isID || id.Name != "RecallResponse" {
			return true
		}
		built++
		for _, elt := range comp.Elts {
			kv, isKV := elt.(*ast.KeyValueExpr)
			if !isKV {
				continue
			}
			if id, isID := kv.Key.(*ast.Ident); isID && strings.Contains(id.Name, "Cursor") {
				t.Errorf("%s:%d builds a RecallResponse setting %s. The vector path has no cursor "+
					"to issue — its ORDER BY is cosine, not the reference time a cursor is a "+
					"position in — so a cursor minted here is one the next request cannot resume "+
					"from and does not fail on either.",
					recallVectorPathFile, fset.Position(kv.Pos()).Line, id.Name)
			}
		}
		return true
	})
	if built == 0 {
		t.Fatalf("no RecallResponse literal was found in %s, so the absence assertion above "+
			"walked nothing — and a vector path that had started minting cursors would pass",
			recallVectorPathFile)
	}
}

// TestRecallResolvesTheWorkItemReferenceBeforeAnyColumnComparison is the
// CORRECTED hop-4 sentence, and the reason the correction needed a probe rather
// than an edit.
//
// The stale sentence was false in the harmless direction — it described a page a
// caller could still get, by resolving the slug themselves — so nothing in the
// tree could go red on it and no reader had to notice. What makes it come back
// is an omission: resolution lives at ONE domain entry precisely because three
// separate builders of a RecallRequest may hold a slug, and a fourth builder
// reintroduces the defect by not resolving. So the ordering is the property:
// resolved BEFORE the router, never at the point of comparison.
//
// The end-to-end answer is held by TestRecallResolvesWorkItemIdOrSlug
// (internal/server/recall_work_item_slug_db_test.go, DB-gated); this arm is the
// non-DB half that fires when the resolution moves or disappears.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M1  Recall stops calling the resolver                             RED (call site)
//	M2  the resolver's SQL drops `OR slug = $1`                       RED (accepts both)
//	M3  the resolution moves below recallRouted                       RED (ordering)
//	M4  the bullet's bold headline reverts to "must be the canonical
//	    id"                                                            RED (publication)
//	M5  green control: reword the corrected sentence's body, keeping
//	    the headline, the claim and the work-item reference             GREEN
//
// ⚠️ M4's first spelling — the stale phrase forbidden ANYWHERE in the card —
// reddened this arm on its own tree, because the correction note quotes the
// sentence it replaced. Nine mutant verdicts were collected against that red
// baseline and are worthless; the scope moved to the bold headline and all five
// were re-run. A mutant run against an already-failing arm measures nothing.
func TestRecallResolvesTheWorkItemReferenceBeforeAnyColumnComparison(t *testing.T) {
	card := readRecallCard(t)
	// The publication half is stated over the bullet's BOLD headline, which is the
	// form this card writes every hop-4 claim in.
	//
	// 🔴 Scoped to the bold span rather than to the card, and the first version of
	// this arm was not — it refused the stale phrase anywhere in the file, and went
	// red on the correction note that QUOTES the stale phrase to record what the
	// bullet used to say. A gate that forbids a card from stating its own history is
	// a gate whose cheapest satisfaction is deleting the record; and a mutant run
	// against an already-red arm proves nothing at all, which is how this was found.
	// Stripping quoted spans instead would have depended on every `"` in an 11 kB
	// card pairing the way one reader expected.
	headline := ""
	for _, m := range regexp.MustCompile(`\*\*([^*]+)\*\*`).FindAllStringSubmatch(card, -1) {
		if strings.Contains(m[1], "`work_item_id`") {
			headline = m[1]
			break
		}
	}
	if headline == "" {
		t.Fatalf("%s carries no bold hop-4 headline naming `work_item_id`, so the claim this arm "+
			"is about is no longer published in the form the card states its behaviour in",
			recallCardPath)
	}
	if strings.Contains(headline, "must be the canonical id") ||
		strings.Contains(headline, "a slug matches nothing") {
		t.Errorf("%s publishes the headline %q again. It was measured false on 2026-09-10: "+
			"aihub#363 put resolveRecallWorkItemRef at the single domain entry every "+
			"RecallRequest builder shares, so a slug resolves and returns the same page the "+
			"canonical id does. The stale advice costs a caller a pf_get_work_item they do not "+
			"need, and it reads as a reason to skip a filter that works.",
			recallCardPath, headline)
	}
	if !strings.Contains(headline, "OR a slug") {
		t.Errorf("%s publishes the headline %q, which no longer says the reference may be a "+
			"slug. That is the whole of the correction: the resolution below exists precisely "+
			"so a caller may send the spelling every human, skill and MCP caller types.",
			recallCardPath, headline)
	}
	if !strings.Contains(card, "aihub#363") {
		t.Errorf("%s no longer names the work item that made the reference resolvable "+
			"(aihub#363) — the citation is what tells the next reader the sentence changed "+
			"because the behaviour did", recallCardPath)
	}

	// The resolver accepts both spellings.
	resolverSQL := ""
	for _, lit := range domainSQLStrings(t, recallTextPathFile) {
		lower := strings.ToLower(lit)
		if strings.Contains(lower, "from work_items where") && strings.Contains(lower, "select id") {
			resolverSQL = lit
		}
	}
	if resolverSQL == "" {
		t.Fatal("no `SELECT id FROM work_items WHERE ...` literal was found in package domain, " +
			"so the reference resolution this card now promises has no query behind it")
	}
	for _, want := range []string{"id = $1", "slug = $1"} {
		if !strings.Contains(resolverSQL, want) {
			t.Errorf("the reference resolver's query is %s and does not compare %s. Both "+
				"spellings have to match the SAME placeholder: matching only the canonical "+
				"column is the pre-aihub#363 behaviour, and it answers 200 with an empty page, "+
				"which is byte-identical to a work item that genuinely holds no memories.",
				resolverSQL, want)
		}
	}

	// The ordering: inside Recall, the resolver call precedes the router call.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, recallTextPathFile, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", recallTextPathFile, err)
	}
	resolveAt, routeAt := -1, -1
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Recall" || fn.Recv != nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, isCall := n.(*ast.CallExpr)
			if !isCall {
				return true
			}
			id, isID := call.Fun.(*ast.Ident)
			if !isID {
				return true
			}
			switch id.Name {
			case "resolveRecallWorkItemRef":
				if resolveAt < 0 {
					resolveAt = fset.Position(call.Pos()).Offset
				}
			case "recallRouted":
				if routeAt < 0 {
					routeAt = fset.Position(call.Pos()).Offset
				}
			}
			return true
		})
	}
	if resolveAt < 0 {
		t.Errorf("domain.Recall does not call resolveRecallWorkItemRef. The card says the " +
			"reference is resolved at this single entry BECAUSE three builders of a " +
			"RecallRequest may each hold a slug; with the call gone every one of them is back " +
			"to the silent empty page, and the only visible difference is the page being empty.")
	}
	if routeAt < 0 {
		t.Fatal("domain.Recall does not call recallRouted, so there is no comparison point to " +
			"order the resolution against and this half asserted nothing")
	}
	if resolveAt > routeAt {
		t.Errorf("domain.Recall resolves the work_item_id reference AFTER routing the request "+
			"(offsets %d vs %d). Everything downstream compares the field against a column that "+
			"holds canonical ids only, so a resolution that happens later happens too late — the "+
			"comparison has already matched nothing.", resolveAt, routeAt)
	}
}
