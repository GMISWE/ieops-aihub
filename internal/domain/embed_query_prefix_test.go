package domain

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/embedding"
)

// The aihub#669 query-prefix gate.
//
// WHAT THIS FILE PINS
// -------------------
// A recall query is embedded with the model's instruct prefix; a stored row is
// not; and the LEXICAL half of the same request keeps the caller's raw query.
// The owner adopted that combination on 2026-09-14 as aihub#660's third arm,
// and its four properties each get a test here, because each one is a separate
// way to be wrong:
//
//	1. the prefix reaches the vector path         (remove it  -> red)
//	2. it never reaches a lexical/substring path  (leak it    -> red)
//	3. the template is byte-exact                 (edit a byte, newline
//	                                               included   -> red)
//	4. the work-item side shares ONE *string between the vector and the lexical
//	   path, and the vector path must not write through it (mutate in place
//	                                                        -> red)
//
// WHY PROPERTY 2 IS NOT PEDANTRY
// ------------------------------
// The lexical section (aihub#360) ANDs one case-insensitive substring match per
// whitespace token of the query. The prefix splits into 14 whitespace fields;
// several of them ("Instruct:", "passages", "retrieve") occur in no stored row,
// and under an AND one such token is enough, so a prefixed query returns an
// EMPTY section rather than a worse one. (The others - "a", "the", "that" - are
// everywhere and do no harm on their own; claiming all 14 are absent would be
// the kind of round number this file exists to refuse.)
// Measured on production, 44 frozen queries, 2026-09-14 (aihub#660):
// lexical target hits 2 -> 0, tokens_used mean 10.05 -> 15.64, queries pinned
// at the 16-token cap 13/42 -> 30/42. Arm 2 and arm 3 are indistinguishable on
// every recall NUMBER in that run; the lexical section is the whole reason arm
// 3 was the one adopted, so nothing else in the suite would notice it breaking.
//
// WHY THESE TESTS NEED NO DATABASE
// --------------------------------
// Both query-side functions call the embedding provider BEFORE they touch the
// pool, so a provider that records its argument and then fails exercises the
// real production functions with a nil pool. That keeps this file out of
// internal/citest/dbtestcov/gated_tests.txt and out of ci.yml's -run lists,
// where a new AIHUB_TEST_DB-gated name would otherwise have to be registered
// twice by hand.

// errRecordingProviderStop is returned by recordingEmbedProvider so the caller
// unwinds before its first query. It is not a failure under test: every test
// below asserts that it came back, precisely so that "the provider was never
// called at all" cannot pass as success.
var errRecordingProviderStop = errors.New("recording provider: stop before the database")

// recordingEmbedProvider captures the exact text handed to Embed.
//
// This is the instrument for properties 1 and 4: asserting that the SOURCE says
// QueryEmbedInput is a different claim from asserting that the provider
// RECEIVED the prefixed bytes, and aihub#660's own delivery check (a tokens_used
// echo from the live server) was the run-time half for the same reason.
type recordingEmbedProvider struct{ texts []string }

func (r *recordingEmbedProvider) Embed(_ context.Context, text string) ([]float32, error) {
	r.texts = append(r.texts, text)
	return nil, errRecordingProviderStop
}

func (r *recordingEmbedProvider) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	r.texts = append(r.texts, texts...)
	return nil, errRecordingProviderStop
}

func (r *recordingEmbedProvider) ModelID() string            { return "recording-test-model" }
func (r *recordingEmbedProvider) Dims() int                  { return 8 }
func (r *recordingEmbedProvider) Ping(context.Context) error { return nil }

func installRecordingProvider(t *testing.T) *recordingEmbedProvider {
	t.Helper()
	rec := &recordingEmbedProvider{}
	InitEmbeddingProvider(rec)
	t.Cleanup(func() { InitEmbeddingProvider(&embedding.NoopProvider{}) })
	return rec
}

// ── Property 3: the template, byte for byte ──────────────────────────────────

// qwen3QueryPromptVerbatim is retyped from the model's own
// config_sentence_transformers.json (key prompts.query) rather than referenced
// from QueryEmbedPrefix. A gate that names the value by importing it agrees
// with the code by construction and can never disagree with it.
const qwen3QueryPromptVerbatim = "Instruct: Given a web search query, " +
	"retrieve relevant passages that answer the query\nQuery:"

func TestQueryEmbedPrefixIsTheModelTemplateVerbatim(t *testing.T) {
	if QueryEmbedPrefix != qwen3QueryPromptVerbatim {
		t.Fatalf("QueryEmbedPrefix is %q, want %q. This template is not decoration: it moves the "+
			"point every query maps to while every stored vector stays where it is, so a drifted "+
			"byte degrades recall with nothing in the data showing it (aihub#660 measured the "+
			"adopted template end to end).", QueryEmbedPrefix, qwen3QueryPromptVerbatim)
	}
	if got := len(QueryEmbedPrefix); got != 91 {
		t.Errorf("QueryEmbedPrefix is %d bytes, want 91 (aihub#660 attrs.prefix.chars)", got)
	}

	// The newline is the byte most likely to be lost in transit or "cleaned up"
	// by a reader who reads the template as one line of prose. aihub#660
	// verified it survived to the server by predicting tokens_used, which would
	// have come back one low for every query had the separator been dropped.
	if n := strings.Count(QueryEmbedPrefix, "\n"); n != 1 {
		t.Errorf("QueryEmbedPrefix holds %d newlines, want exactly 1", n)
	}
	if i := strings.IndexByte(QueryEmbedPrefix, '\n'); i != 84 {
		t.Errorf("the newline sits at byte %d, want 84 (right after \"...answer the query\")", i)
	}

	// No separator after "Query:": sentence-transformers concatenates prompt and
	// text, so the colon fuses with the query's first character. A trailing
	// space is a different prompt.
	if !strings.HasSuffix(QueryEmbedPrefix, "Query:") {
		t.Errorf("QueryEmbedPrefix ends %q, want it to end with \"Query:\"",
			QueryEmbedPrefix[max(0, len(QueryEmbedPrefix)-10):])
	}
	if got, want := QueryEmbedInput("hello"), qwen3QueryPromptVerbatim+"hello"; got != want {
		t.Errorf("QueryEmbedInput fuses wrongly:\n got %q\nwant %q. It must be plain "+
			"concatenation: any separator, including a single space, is a prompt the model "+
			"was not trained with.", got, want)
	}

	// The document side is NOT prefixed, and that asymmetry is the model's
	// design (prompts.document = ""). It is also what makes this change need no
	// re-embedding, so a "symmetry fix" here would silently invalidate every
	// stored vector.
	if got := MemoryEmbedInput("hello"); strings.Contains(got, "Instruct:") {
		t.Errorf("MemoryEmbedInput has grown a prefix (%q). The document side must stay bare: "+
			"the model ships prompts.document = \"\", and prefixing stored rows would require "+
			"re-embedding the whole corpus.", got)
	}
	if got := WorkItemEmbedInput("goal", "content"); strings.Contains(got, "Instruct:") {
		t.Errorf("WorkItemEmbedInput has grown a prefix (%q); see the note above", got)
	}
}

// ── Property 1: the prefix reaches the vector path ───────────────────────────

func TestMemoryVectorPathEmbedsThePrefixedQuery(t *testing.T) {
	rec := installRecordingProvider(t)

	const rawQuery = "already_held empty"
	req := &RecallRequest{Project: "aihub", Query: rawQuery, TopK: 5}

	_, err := RecallWithVector(context.Background(), nil, req)
	if !errors.Is(err, errRecordingProviderStop) {
		t.Fatalf("RecallWithVector returned %v, want the recording provider's stop error. "+
			"Without it this test would pass by never reaching the embed call at all.", err)
	}
	if len(rec.texts) != 1 {
		t.Fatalf("the provider saw %d texts, want exactly 1", len(rec.texts))
	}
	if got, want := rec.texts[0], QueryEmbedPrefix+rawQuery; got != want {
		t.Errorf("the memory vector path embedded %q, want %q", got, want)
	}

	// The other half of property 2, at run time: recallLexical tokenizes this
	// same field afterwards (memory.go's Recall calls it with the ORIGINAL
	// request), so a vector path that rewrote it would destroy the lexical
	// section of every recall.
	if req.Query != rawQuery {
		t.Errorf("RecallWithVector rewrote req.Query to %q. It must leave the field alone: "+
			"Recall hands that same pointer to recallLexical after this returns.", req.Query)
	}
}

func TestWorkItemVectorPathEmbedsThePrefixedQuery(t *testing.T) {
	rec := installRecordingProvider(t)

	rawQuery := "gateway cross-region scan"
	f := ListWorkItemsFilter{Query: &rawQuery, Limit: 10}

	_, _, _, _, aerr := semanticQuerySource(context.Background(), nil, "aihub", f)
	if aerr == nil {
		t.Fatal("semanticQuerySource succeeded with a failing provider, so it never reached " +
			"the embed call and this test would assert nothing")
	}
	if len(rec.texts) != 1 {
		t.Fatalf("the provider saw %d texts, want exactly 1", len(rec.texts))
	}
	if got, want := rec.texts[0], QueryEmbedPrefix+"gateway cross-region scan"; got != want {
		t.Errorf("the work-item vector path embedded %q, want %q", got, want)
	}
}

// ── Property 4: the shared *string on the work-item side ─────────────────────

// TestWorkItemSharedQueryPointerIsNotContaminated reproduces the aliasing the
// production code actually has.
//
// ListWorkItemsFilter.Query is a *string and ListWorkItems passes the filter BY
// VALUE to listWorkItemsByVector, to listWorkItemsLexical and to
// buildListWorkItemsWhere. Copying the struct does NOT copy the string: all
// three readers dereference one pointer. So "prefix the query on the vector
// path" has a spelling that type-checks, reads fine, and silently empties two
// other paths:
//
//	*f.Query = QueryEmbedInput(*f.Query)
//
// This test is what that spelling fails on.
func TestWorkItemSharedQueryPointerIsNotContaminated(t *testing.T) {
	rec := installRecordingProvider(t)

	const rawQuery = "already_held empty"
	shared := rawQuery
	vecFilter := ListWorkItemsFilter{Query: &shared, Limit: 10}
	lexFilter := ListWorkItemsFilter{Query: &shared, Limit: 10}
	if vecFilter.Query != lexFilter.Query {
		t.Fatal("the two filters do not share a pointer, so this test is not reproducing the " +
			"aliasing it exists to test")
	}

	wantTokens, wantDropped := lexicalTokens(rawQuery)
	if len(wantTokens) == 0 {
		t.Fatal("the fixture query tokenizes to nothing; pick one that does not")
	}

	_, _, _, _, aerr := semanticQuerySource(context.Background(), nil, "aihub", vecFilter)
	if aerr == nil {
		t.Fatal("semanticQuerySource succeeded with a failing provider, so the vector path " +
			"never ran and nothing below is evidence")
	}

	// (a) the vector path got the prefixed text
	if len(rec.texts) != 1 || rec.texts[0] != QueryEmbedPrefix+rawQuery {
		t.Errorf("the vector path embedded %v, want exactly [%q]", rec.texts, QueryEmbedPrefix+rawQuery)
	}
	// (b) the pointed-to string is byte-identical
	if shared != rawQuery {
		t.Errorf("the shared string is now %q, want %q. The vector path wrote through the "+
			"pointer.", shared, rawQuery)
	}
	if *lexFilter.Query != rawQuery {
		t.Errorf("the lexical path's view of the query is now %q, want %q", *lexFilter.Query, rawQuery)
	}
	// (c) the lexical predicate built from the SHARED pointer is unchanged
	gotTokens, gotDropped := lexicalTokens(*lexFilter.Query)
	if !slices.Equal(gotTokens, wantTokens) || gotDropped != wantDropped {
		t.Errorf("lexical tokens through the shared pointer are %v (dropped %d), want %v "+
			"(dropped %d). Every token is ANDed as a substring match, so extra tokens do not "+
			"widen the section, they empty it.", gotTokens, gotDropped, wantTokens, wantDropped)
	}
	for _, tok := range gotTokens {
		if strings.HasPrefix(strings.ToLower(tok), "instruct") {
			t.Errorf("the lexical predicate carries the prefix token %q", tok)
		}
	}
}

// TestPrefixWouldDestroyTheLexicalSection is the mechanism behind property 2,
// asserted rather than described: it shows what the tests above are protecting.
//
// It is also this repo's offline copy of aihub#660's ARRIVAL check. That run
// could not read the server's memory, so it proved the prefix reached the
// service byte for byte by PREDICTING the tokens_used the server would echo:
//
//	predicted = 13 + 1 + (query fields - 1), capped at lexicalMaxTokens
//
// 13 whitespace fields of the prefix stand alone, the 14th ("Query:") fuses
// with the query's first field, and the rest of the query follows. Four
// predictions, four exact matches; a newline lost anywhere in transport would
// have made every one of them one token low. The same arithmetic is checked
// here against the same tokenizer the server ran, including the per-query
// readings aihub#660 published: B5-S 2 tokens -> 15, B4-S 4 -> 16 with one
// dropped.
func TestPrefixWouldDestroyTheLexicalSection(t *testing.T) {
	// The COUNTS are aihub#660's, verbatim from
	// lexical_channel_360.per_query. The query TEXT is only recorded for B5-S
	// ("already_held empty", named in that wi's thin_margin_caveat); B4-S's
	// four-token query is not written down anywhere, so the second row uses a
	// stand-in of the same token count and is labelled as one. Pretending to
	// quote a string nobody recorded is the failure mode this whole file is
	// about.
	cases := []struct {
		name                      string
		query                     string
		rawTokens                 int
		wantPrefixed, wantDropped int
	}{
		{"B5-S_recorded_query", "already_held empty", 2, 15, 0},
		{"B4-S_shape_stand_in_query", "polyforge lock conflict CONFLICT_LOCK_TAKEN", 4, 16, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, rawDropped := lexicalTokens(tc.query)
			if len(raw) != tc.rawTokens || rawDropped != 0 {
				t.Fatalf("the raw query tokenizes to %v (dropped %d), want %d tokens and no "+
					"drop; the comparison below is only meaningful against that baseline",
					raw, rawDropped, tc.rawTokens)
			}

			prefixed, prefixedDropped := lexicalTokens(QueryEmbedInput(tc.query))

			// The prediction aihub#660 checked against the live server.
			predicted := min(13+1+(tc.rawTokens-1), lexicalMaxTokens)
			if predicted != tc.wantPrefixed {
				t.Fatalf("the recorded reading (%d) and the formula (%d) disagree; one of them "+
					"is being retyped wrongly", tc.wantPrefixed, predicted)
			}
			if len(prefixed) != tc.wantPrefixed || prefixedDropped != tc.wantDropped {
				t.Errorf("the prefixed query tokenizes to %d tokens (dropped %d), want %d "+
					"(dropped %d). A mismatch here means the template no longer splits into the "+
					"fields aihub#660 measured, which is exactly what a lost newline looks like.",
					len(prefixed), prefixedDropped, tc.wantPrefixed, tc.wantDropped)
			}
			if len(prefixed) <= len(raw) {
				t.Errorf("the prefix added no tokens (%d -> %d); the whole hazard is that it "+
					"adds them", len(raw), len(prefixed))
			}

			// Each of these is ANDed as a substring match against a row's
			// content, and none of them occurs in the corpus. One is enough to
			// empty the section.
			for _, artifact := range []string{"Instruct:", "passages", "retrieve"} {
				if !slices.Contains(prefixed, artifact) {
					t.Errorf("the prefixed token set %v does not contain %q, so this test is no "+
						"longer demonstrating the failure it describes", prefixed, artifact)
				}
			}
		})
	}
}

// ── Property 1 and 2, structurally: who may know about the prefix ────────────

// prefixAwareFiles are the ONLY non-test files allowed to name QueryEmbedInput
// or QueryEmbedPrefix: the definition, and the two query-side embed call sites.
//
// Anything else naming them is a leak by construction, because there is exactly
// one thing the prefix can be used for and two places that may do it. This is
// the structural half of property 2, and it covers the reader the wi's own
// landing-point note does not list: buildListWorkItemsWhere's ILIKE fallback in
// work_items.go, a THIRD consumer of the same *string.
var prefixAwareFiles = []string{
	"internal/domain/embed_input.go",
	"internal/domain/memory_vector.go",
	"internal/domain/wi_vector.go",
}

// rawQueryConsumers are the call sites that must keep receiving the caller's
// query verbatim, with the exact source text of the argument they take it from.
// An exact match, not a substring check: wrapping the expression is the defect.
var rawQueryConsumers = map[string]string{
	"internal/domain/memory_lexical.go": "req.Query",
	"internal/domain/wi_lexical.go":     "*f.Query",
}

// prefixedEmbedSites are the two query-side embed calls and the exact argument
// each must hand the provider. embed_writer_parity_test.go asserts the same
// multiset for its own reasons; stated again here so property 1 has a test that
// names it, and so deleting either gate leaves the other standing.
var prefixedEmbedSites = map[string]string{
	"internal/domain/memory_vector.go": "QueryEmbedInput(req.Query)",
	"internal/domain/wi_vector.go":     "QueryEmbedInput(*f.Query)",
}

// minPrefixCensusFiles keeps a walk that visits nothing from reporting a clean
// census. Measured 2026-09-14: 106 non-test .go files under internal/ and cmd/.
const minPrefixCensusFiles = 60

func TestOnlyTheQuerySideKnowsAboutThePrefix(t *testing.T) {
	root := filepath.Join("..", "..")

	var walked int
	mentions := map[string]bool{}
	literals := map[string]bool{}
	embedArgs := map[string][]string{}
	lexicalArgs := map[string][]string{}

	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return rerr
			}
			rel = filepath.ToSlash(rel)
			src, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			// Comments dropped on purpose: memory_vector.go and wi_vector.go
			// both explain in prose why the prefix must not be written through
			// the request field, and those explanations quote the template. A
			// scan that could not tell the warning from the offence would force
			// the fix to ship undocumented.
			f, perr := parser.ParseFile(token.NewFileSet(), rel, src, 0)
			if perr != nil {
				return nil
			}
			walked++

			ast.Inspect(f, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.Ident:
					if node.Name == "QueryEmbedInput" || node.Name == "QueryEmbedPrefix" {
						mentions[rel] = true
					}
				case *ast.BasicLit:
					if node.Kind == token.STRING && strings.Contains(node.Value, "Instruct: Given a web search query") {
						literals[rel] = true
					}
				case *ast.CallExpr:
					switch fn := node.Fun.(type) {
					case *ast.SelectorExpr:
						if fn.Sel.Name == "Embed" && len(node.Args) == 2 {
							embedArgs[rel] = append(embedArgs[rel], exprText(node.Args[1]))
						}
					case *ast.Ident:
						if fn.Name == "lexicalTokens" && len(node.Args) == 1 {
							lexicalArgs[rel] = append(lexicalArgs[rel], exprText(node.Args[0]))
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}

	if walked < minPrefixCensusFiles {
		t.Fatalf("the census walked %d files, under the %d floor. A census that visits nothing "+
			"reports no violations perfectly.", walked, minPrefixCensusFiles)
	}

	// 1. Only the three sanctioned files may name the prefix at all.
	if got := sortedFileSet(mentions); !slices.Equal(got, prefixAwareFiles) {
		t.Errorf("files naming QueryEmbedInput/QueryEmbedPrefix are %v, want exactly %v.\n"+
			"A fourth file is a leak: the prefix has exactly one purpose (the text handed to the "+
			"embedding provider for a SEARCH query) and two call sites that may serve it. If it "+
			"reached a lexical or ILIKE path the query would be matched as a literal substring "+
			"against rows containing \"Instruct: Given a web search query...\", and every one of "+
			"those sections would return zero (aihub#660: lexical target hits 2 -> 0).",
			got, prefixAwareFiles)
	}

	// 2. The template itself is written down once. Catches the leak spelled as
	//    an inline literal rather than as a call to the builder.
	if got := sortedFileSet(literals); !slices.Equal(got, []string{"internal/domain/embed_input.go"}) {
		t.Errorf("the prefix template appears as a string literal in %v, want only in "+
			"internal/domain/embed_input.go. A second copy is a second template: aihub#361's "+
			"whole lesson is that a rule written twice disagrees with itself, and here the "+
			"disagreement would be invisible because both copies still produce a vector.", got)
	}

	// 3. The two query-side embed calls pass the prefixed text.
	for rel, want := range prefixedEmbedSites {
		if got := embedArgs[rel]; !slices.Equal(got, []string{want}) {
			t.Errorf("%s hands the provider %v, want exactly [%q]. Dropping the wrapper is "+
				"dropping the feature: aihub#660 measured recall@1 20 -> 22, recall@5 26 -> 29, "+
				"and real queries scoring below a garbage control 17/36 -> 0/36 on this prefix "+
				"alone.", rel, got, want)
		}
	}

	// 4. The document-side writers must NOT be prefixed. Property 1 phrased as
	//    its own negative control: a change that prefixed everything would pass
	//    check 3 and be a corpus-wide re-embedding obligation.
	for _, rel := range []string{"internal/domain/memory.go", "internal/domain/wi_embedding.go"} {
		for _, arg := range embedArgs[rel] {
			if strings.Contains(arg, "QueryEmbedInput") {
				t.Errorf("%s embeds %q. That is the DOCUMENT side: the model ships "+
					"prompts.document = \"\", and prefixing stored rows makes every existing "+
					"vector stale with a byte-identical emb_model to say so.", rel, arg)
			}
		}
	}

	// 5. The lexical paths still tokenize the caller's raw query.
	for rel, want := range rawQueryConsumers {
		if got := lexicalArgs[rel]; !slices.Equal(got, []string{want}) {
			t.Errorf("%s tokenizes %v, want exactly [%q]. The lexical section answers EXISTENCE "+
				"(aihub#360), and it is the only reason the prefix was adopted in this shape "+
				"rather than wholesale.", rel, got, want)
		}
	}
}

// TestNothingWritesThroughTheRequestQueryField bans the mutation shape, at every
// frame rather than at the one the run-time test above can observe.
//
// `*f.Query = ...` one function up from semanticQuerySource would defeat
// TestWorkItemSharedQueryPointerIsNotContaminated while doing exactly the damage
// it exists to prevent, and `req.Query = QueryEmbedPrefix + req.Query` on a copy
// would defeat it on the memory side. Clearing the field (`tf.Query = nil`) is
// the one assignment that is legitimate and is allowed by name: both existing
// ones exist so buildListWorkItemsWhere skips its ILIKE guard.
func TestNothingWritesThroughTheRequestQueryField(t *testing.T) {
	root := filepath.Join("..", "..")
	entries, err := os.ReadDir(filepath.Join(root, "internal", "domain"))
	if err != nil {
		t.Fatalf("reading internal/domain: %v", err)
	}

	var walked int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, rerr := os.ReadFile(filepath.Join(root, "internal", "domain", name))
		if rerr != nil {
			t.Fatalf("reading %s: %v", name, rerr)
		}
		f, perr := parser.ParseFile(token.NewFileSet(), name, src, 0)
		if perr != nil {
			t.Fatalf("parsing %s: %v", name, perr)
		}
		walked++

		ast.Inspect(f, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, lhs := range as.Lhs {
				var rhs ast.Expr
				if i < len(as.Rhs) {
					rhs = as.Rhs[i]
				}
				switch target := lhs.(type) {
				case *ast.StarExpr:
					sel, ok := target.X.(*ast.SelectorExpr)
					if ok && sel.Sel.Name == "Query" {
						t.Errorf("%s writes through the query pointer: `%s = %s`. "+
							"ListWorkItemsFilter.Query is shared by the vector path, "+
							"listWorkItemsLexical and buildListWorkItemsWhere's ILIKE fallback, "+
							"so this reaches all three and the two substring paths then search "+
							"for the prefix itself.", name, exprText(lhs), exprText(rhs))
					}
				case *ast.SelectorExpr:
					if target.Sel.Name != "Query" {
						continue
					}
					if id, ok := rhs.(*ast.Ident); ok && id.Name == "nil" {
						continue // clearing the field is the legitimate case
					}
					t.Errorf("%s assigns the query field: `%s = %s`. Prefix the ARGUMENT of the "+
						"embed call instead: a value that is never written cannot leak into the "+
						"lexical half of the same request. Only `= nil` is allowed here.",
						name, exprText(lhs), exprText(rhs))
				}
			}
			return true
		})
	}

	if walked < 30 {
		t.Fatalf("walked only %d files in internal/domain; the scan is not seeing the package", walked)
	}
}

// sortedFileSet is declared_resources.go's sortedKeys under a name this test
// file can own. Reusing the production helper would be one import of the thing
// under test too many; duplicating eight lines is cheaper than that coupling.
func sortedFileSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
