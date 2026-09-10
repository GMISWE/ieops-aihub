package mcp_test

// aihub#543 probe wave 2, the annotation tail — `docs/mcp-cards/pf_resolve_commit.md`.
//
// Six sentences, and the thing they have in common is that this tool writes to a
// surface no MCP tool can read back: a commit annotation lives in the `/ui`
// artifact viewer, so every claim on this card is about a hop whose effect is
// invisible from the caller's side.
//
//   - what leaves the process: exactly `{reply}` to
//     `POST /v1/memories/<id>/commit/<commit_id>/resolve`, with two of the three
//     parameters as path segments and no attempt credential;
//   - the sibling `.../reply` route this tool does NOT call;
//   - the two published effects — `status=resolved` and a
//     `memory_commit_resolved` event;
//   - the web UI being another writer of the same state;
//   - `reply` being stored rather than merely acknowledged, which is why it is
//     required; and
//   - `memory_commit_resolved` being a free-text event type with no published
//     vocabulary.
//
// 🔴 WHY NOTHING HELD THEM. internal/domain/memory_commit_test.go's
// TestResolveCommitSQLKeys pins the jsonb paths and the `"resolved"` literal —
// the only arm this card had — and it says nothing about the event, about what
// reaches the wire, or about the other writers. The tool's own description
// promises both effects in one parenthesis, and both are string literals in a
// function whose result no test in this package reads.
//
// No database: the wire arms drive the real tool against a fake aihub, and the
// rest are censuses over the tree's own source, read with go/parser.
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestResolveCommit|TestPublishedResolveCommit|TestEveryResolveCommit' -count=1 -v

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
	"testing"
)

const (
	// domainMemoryPath declares resolveCommitSQL and domain.ResolveCommit.
	domainMemoryPath = "../domain/memory.go"
	// routesMemoryPath registers both /v1 commit routes and holds the /v1
	// handler.
	routesMemoryPath = "../server/routes_memory.go"
	// clientPath is the only way an MCP tool reaches the server.
	clientPath = "../../pkg/client/client.go"
)

// serverFilesWithResolveWriters are the non-test files in internal/server that
// may reach domain.ResolveCommit. Named rather than globbed so a fourth entry
// point in a new file shows up as a census failure below rather than as a file
// this list never looked at.
var serverFilesWithResolveWriters = []string{
	routesMemoryPath,
	"../server/routes_artifacts.go",
	"../server/ui_handlers_memory.go",
}

// TestResolveCommitSendsOnlyTheReplyToTheResolveRoute is the hop-2-3 sentence:
// `buildResolveCommitBody` sends exactly `{reply}`, the other two parameters
// are path segments, and no attempt credential is sent.
//
// The ids are deliberately distinguishable, so a handler that swapped the two
// path segments is red rather than merely differently-ordered.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M51 buildResolveCommitBody also sends memory_id           RED  (body arm)
//	M52 the body carries attempt credentials from the state
//	     file, as this tool's siblings once did                RED  (credential arm)
//	M53 ResolveCommit swaps the two path segments             RED  (path arm)
//	M54 ResolveCommit posts to `.../reply`                    RED  (route arm)
//	M70 the body sends a fixed string rather than the
//	     argument                                             RED  (verbatim arm)
//	M55 green control: rename the body-building function      GREEN
func TestResolveCommitSendsOnlyTheReplyToTheResolveRoute(t *testing.T) {
	const (
		memID    = "mem_ANNOTATED0001"
		commitID = "cmt_THEANNOTATION"
		reply    = "rewrote the section the annotation asked about"
	)
	f := newFakeAihub(t)
	out, isErr := callTool(t, f, "pf_resolve_commit", map[string]any{
		"memory_id": memID,
		"commit_id": commitID,
		"reply":     reply,
	})
	if isErr {
		t.Fatalf("pf_resolve_commit failed: %v", out)
	}

	calls := f.recorded()
	if len(calls) != 1 {
		t.Fatalf("pf_resolve_commit made %d request(s) %v, want exactly 1", len(calls), f.paths())
	}
	c := calls[0]
	wantPath := "/v1/memories/" + memID + "/commit/" + commitID + "/resolve"
	if c.Method != "POST" || c.Path != wantPath {
		t.Errorf("pf_resolve_commit sent %s %s, want POST %s. Two of the three parameters are PATH "+
			"SEGMENTS, so a swap or a wrong route addresses a different annotation entirely and the "+
			"server answers about it.", c.Method, c.Path, wantPath)
	}

	if got := len(c.Body); got != 1 {
		t.Errorf("the request body is %v (%d key(s)), want exactly {reply}. The card's sentence is "+
			"that the body is a single key BECAUSE the other two parameters are path segments; an "+
			"extra key is a hop the card does not describe.", c.Body, got)
	}
	if c.Body["reply"] != reply {
		t.Errorf("the request body carries reply=%v, want %q verbatim", c.Body["reply"], reply)
	}
	// No attempt credential — the direction this tool's siblings got wrong
	// (aihub#324 built three of them into a body nothing read).
	for _, k := range []string{"attempt_id", "claim_epoch", "session_secret", "work_item_id"} {
		if v, has := c.Body[k]; has {
			t.Errorf("the request body carries %q=%v. The card says no attempt credentials are sent, "+
				"and a credential nothing checks is worse than none: a reader concludes the path is "+
				"attempt-gated and the code shape agrees with them.", k, v)
		}
	}
}

// TestResolveCommitCannotReachTheReplySibling is the sentence about the sibling
// route: `.../commit/<commit_id>/reply` is bound by the server and "this tool
// does not call" it, because replying and resolving are different operations
// and only the second is published.
//
// Three parts, because "it does not call it" is only interesting if the route
// EXISTS and if nothing in the client could reach it by accident:
//
//  1. the server really binds both paths (read out of its registration file);
//  2. pkg/client declares no method that issues the `/reply` path at all, so no
//     MCP tool can reach it even by naming the wrong helper; and
//  3. the tool's single observed request is the `/resolve` one.
//
// Mutants (2026-09-10):
//
//	M56 the /reply route registration is deleted             RED  (part 1)
//	M57 pkg/client gains a ReplyCommit issuing that path     RED  (part 2)
//	M58 green control: reword the route comments             GREEN
func TestResolveCommitCannotReachTheReplySibling(t *testing.T) {
	routes, err := os.ReadFile(routesMemoryPath)
	if err != nil {
		t.Fatalf("read %s: %v", routesMemoryPath, err)
	}
	for _, want := range []string{
		`"/memories/:id/commit/:commit_id/resolve"`,
		`"/memories/:id/commit/:commit_id/reply"`,
	} {
		if !strings.Contains(string(routes), want) {
			t.Errorf("%s no longer registers %s. The card records the sibling route as a route this "+
				"tool deliberately does not call; with the route gone the sentence describes nothing.",
				routesMemoryPath, want)
		}
	}

	// pkg/client is the only path from an MCP tool to the server, so a method
	// issuing `/reply` is the only way this tool could ever reach it.
	replyIssuers := clientMethodsIssuingPath(t, "/reply")
	if len(replyIssuers) != 0 {
		t.Errorf("pkg/client declares %v, which issue a `/reply` path. The card's sentence is that "+
			"only resolving is published; a client method for the sibling makes it reachable from a "+
			"tool by naming the wrong helper, which is a mistake nothing else here would catch.",
			replyIssuers)
	}
	// Floor: the walk must find the resolve issuer, or "no reply issuer" is a
	// statement about a scan that matches nothing.
	if got := clientMethodsIssuingPath(t, "/commit/"); len(got) == 0 {
		t.Errorf("the scan of %s found no method issuing any `/commit/` path at all, so the negative "+
			"above is vacuous", clientPath)
	}

	f := newFakeAihub(t)
	out, isErr := callTool(t, f, "pf_resolve_commit", map[string]any{
		"memory_id": "mem_SIBLING0001", "commit_id": "cmt_SIBLING0001", "reply": "resolved",
	})
	if isErr {
		t.Fatalf("pf_resolve_commit failed: %v", out)
	}
	for _, p := range f.paths() {
		if strings.HasSuffix(p, "/reply") {
			t.Errorf("pf_resolve_commit posted to %s — replying and resolving are different "+
				"operations and this tool publishes the second", p)
		}
	}
}

// clientMethodsIssuingPath returns the pkg/client methods whose body contains a
// string literal holding the given path fragment.
//
// go/parser over the file rather than grep: the fragment appears in comments
// and in doc examples throughout, and what matters is a STRING LITERAL inside a
// method body — the only shape that puts a request on the wire.
func clientMethodsIssuingPath(t *testing.T, fragment string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, clientPath, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", clientPath, err)
	}
	var out []string
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		hit := false
		ast.Inspect(fd, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if strings.Contains(lit.Value, fragment) {
				hit = true
			}
			return true
		})
		if hit {
			out = append(out, fd.Name.Name)
		}
	}
	return out
}

// publishedEffectsRe reads the two effects out of the live tool description:
// "(marks status=resolved, emits memory_commit_resolved)".
var publishedEffectsRe = regexp.MustCompile(
	`marks\s+status=([a-z_]+),\s*emits\s+([a-z_]+)`)

// TestPublishedResolveCommitEffectsAreTheEnforcedOnes is the hop-4 sentence, and
// its two halves are enforced in two different places: the status in the
// `resolveCommitSQL` constant and the event type in an `INSERT INTO
// agent_events` inside `domain.ResolveCommit`.
//
// Both values are READ OUT OF THE PUBLISHED DESCRIPTION rather than written
// here, so editing the description without the code is red and editing the code
// without the description is red too — the base-strength precedent, which
// exists because an arm hard-coding the value goes green on the day the value
// moves.
//
// `internal/domain/memory_commit_test.go` (`TestResolveCommitSQLKeys`) already
// holds the SQL's jsonb paths and its `"resolved"` literal; what it cannot see
// is the description, or the event at all.
//
// Mutants (2026-09-10):
//
//	M59 the SQL writes "done" instead of "resolved"          RED  (status arm)
//	M60 the event type becomes memory_commit_closed          RED  (event arm)
//	M61 the description promises status=closed               RED  (both arms,
//	                                                         from the other side)
//	M62 the description drops the event promise               RED  (parse arm)
//	M63 green control: reword the prose around the two
//	    published tokens                                      GREEN
func TestPublishedResolveCommitEffectsAreTheEnforcedOnes(t *testing.T) {
	desc := publishedTool(t, "pf_resolve_commit").Description
	m := publishedEffectsRe.FindStringSubmatch(desc)
	if m == nil {
		t.Fatalf("pf_resolve_commit's published description no longer states both effects in the "+
			"form this arm reads:\n  %s\nThe card's hop-4 bullet is that BOTH happen — the row is "+
			"rewritten and the timeline records it — so a description that promises one is a "+
			"different contract and this arm has to follow it.", desc)
	}
	status, eventType := m[1], m[2]
	t.Logf("published: status=%q event=%q", status, eventType)

	src, err := os.ReadFile(domainMemoryPath)
	if err != nil {
		t.Fatalf("read %s: %v", domainMemoryPath, err)
	}
	sql := goConstValue(t, domainMemoryPath, "resolveCommitSQL")
	if !strings.Contains(sql, `"`+status+`"`) {
		t.Errorf("resolveCommitSQL does not write the status %q the description promises:\n%s",
			status, sql)
	}

	body := goFuncBody(t, domainMemoryPath, string(src), "ResolveCommit")
	if !strings.Contains(body, "INSERT INTO agent_events") {
		t.Fatalf("domain.ResolveCommit contains no INSERT INTO agent_events, so the event half of " +
			"the published promise is not written here at all and the check below would read the " +
			"wrong function")
	}
	if !strings.Contains(body, "'"+eventType+"'") {
		t.Errorf("domain.ResolveCommit does not emit the event type %q the description promises. Its "+
			"body:\n%s", eventType, body)
	}
	// The event is written best-effort inside this function (the insert's error
	// is discarded), which is exactly why nothing else in the tree notices when
	// the literal changes — stated here because it is the reason this arm reads
	// the source rather than driving a database.
	if !strings.Contains(body, "//nolint:errcheck") {
		t.Logf("note: domain.ResolveCommit no longer marks the event insert as error-checked-away; " +
			"if the event became load-bearing, a DB arm can now assert it")
	}
}

// TestEveryResolveCommitEntryPointGoesThroughTheOneDomainFunction is the
// second-writer sentence — and the census corrects it: there are THREE entry
// points into this state, not two.
//
//	handleResolveCommit             /v1  (the route pf_resolve_commit calls)
//	handleUIArtifactResolveCommit   /ui  artifacts viewer
//	handleUIResolveCommit           /ui  memories viewer
//
// All three reach `domain.ResolveCommit`, the last two through the
// `doResolveCommitFn` seam. What the arm holds is that the set is exactly those
// three and that none of them writes the commits column itself: a fourth writer,
// or one that grew its own UPDATE, is the shape that makes "the same vocabulary"
// false without touching either file the card names.
//
// Mutants (2026-09-10):
//
//	M64 handleUIResolveCommit stops calling the seam          RED  (count arm)
//	M65 a fourth handler calls domain.ResolveCommit           RED  (count arm,
//	                                                         other direction)
//	M66 doResolveCommitFn stops wrapping domain.ResolveCommit RED  (seam arm)
//	M67 green control: rename a local variable in one handler  GREEN
func TestEveryResolveCommitEntryPointGoesThroughTheOneDomainFunction(t *testing.T) {
	want := map[string]bool{
		"handleResolveCommit":           false,
		"handleUIArtifactResolveCommit": false,
		"handleUIResolveCommit":         false,
	}
	var extra []string
	seam := false

	for _, path := range serverFilesWithResolveWriters {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				body := goFuncBody(t, path, string(src), d.Name.Name)
				if !callsResolveCommit(body) {
					continue
				}
				if _, known := want[d.Name.Name]; known {
					want[d.Name.Name] = true
					continue
				}
				extra = append(extra, path+":"+d.Name.Name)
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "doResolveCommitFn" {
						continue
					}
					for _, v := range vs.Values {
						if strings.Contains(nodeText(t, path, string(src), v), "domain.ResolveCommit") {
							seam = true
						}
					}
				}
			}
		}
	}

	for name, found := range want {
		if !found {
			t.Errorf("%s no longer reaches domain.ResolveCommit. Every entry point into a commit "+
				"annotation's resolved state has to write it through one function, or the two "+
				"viewers and the tool can disagree about what \"resolved\" means — which is exactly "+
				"the question this card's Open section records as unchecked.", name)
		}
	}
	if len(extra) > 0 {
		t.Errorf("these handlers also reach domain.ResolveCommit and the card names none of them: "+
			"%v. A writer the card does not know about is a writer nobody compared against the "+
			"others.", extra)
	}
	if !seam {
		t.Errorf("doResolveCommitFn no longer wraps domain.ResolveCommit, so the two /ui handlers " +
			"reach something else and \"the same vocabulary\" is no longer a property of the code")
	}
}

// callsResolveCommit reports whether a function body calls the domain function
// or the /ui seam over it.
func callsResolveCommit(body string) bool {
	return strings.Contains(body, "domain.ResolveCommit(") || strings.Contains(body, "doResolveCommitFn(")
}

// TestResolveCommitReplyIsRequiredAtEveryHopAndStored is the sentence "`reply`
// is stored rather than merely acknowledged, which is why it is required".
//
// "Required" is checked at both hops a caller meets: the published schema's
// `required` array, and the tool handler's own refusal — which must refuse
// WITHOUT making a request, because a server that answers 400 is a different
// contract from a tool that never asks.
//
// "Stored" is held by `internal/domain/memory_commit_test.go`
// (`TestResolveCommitSQLKeys`), which requires the `'{reply}'` jsonb path in the
// UPDATE; this arm holds the value reaching the wire verbatim, which is the hop
// between the two.
//
// Mutants (2026-09-10):
//
//	M68 the handler drops its empty-reply refusal             RED  (a request is
//	                                                          made)
//	M69 `reply` leaves the schema's required array            RED
//	M70 the body sends a fixed string rather than the
//	     argument                                             GREEN here, RED in
//	                                                          the wire arm above —
//	                                                          recorded so the two
//	                                                          arms' division of
//	                                                          labour is visible
//	M71 green control: reword the refusal message             GREEN
func TestResolveCommitReplyIsRequiredAtEveryHopAndStored(t *testing.T) {
	tool := publishedTool(t, "pf_resolve_commit")
	schema := schemaJSON(t, tool.Name, tool.InputSchema)
	if !strings.Contains(schema, `"required"`) || !strings.Contains(schema, `"reply"`) {
		t.Fatalf("pf_resolve_commit's published schema names no required array or no reply "+
			"parameter:\n%s", schema)
	}
	req := regexp.MustCompile(`"required"\s*:\s*\[([^\]]*)\]`).FindStringSubmatch(schema)
	if req == nil {
		t.Fatalf("cannot read the required array out of the published schema:\n%s", schema)
	}
	for _, name := range []string{"memory_id", "commit_id", "reply"} {
		if !strings.Contains(req[1], `"`+name+`"`) {
			t.Errorf("the published required array is [%s] and does not contain %q. The card's hop-1 "+
				"table says all three are required, and `reply` is required because it is STORED.",
				req[1], name)
		}
	}

	// The refusal must happen before any request: an empty reply that reached
	// the server would be a 400 from somewhere else, and the card's claim is
	// about this tool.
	f := newFakeAihub(t)
	out, isErr := callTool(t, f, "pf_resolve_commit", map[string]any{
		"memory_id": "mem_EMPTYREPLY01", "commit_id": "cmt_EMPTYREPLY01", "reply": "",
	})
	if !isErr {
		t.Errorf("pf_resolve_commit accepted an empty reply and answered %v", out)
	}
	if len(f.recorded()) != 0 {
		t.Errorf("pf_resolve_commit made %v with an empty reply; the refusal is the tool's own, so "+
			"nothing should have left the process", f.paths())
	}
}

// TestResolveCommitEventTypeHasNoPublishedVocabulary is the Policy sentence:
// `memory_commit_resolved` is "another free-text event type with no published
// vocabulary".
//
// The census is over every published tool's InputSchema: no `enum` anywhere in
// the set may contain this event type, and `pf_emit_event`'s `event_type`
// parameter — the one place a caller supplies an event type — may declare no
// enum at all.
//
// ⚠️ The control matters more than the assertion here. "No enum" is trivially
// true of a walk that reads no schemas, and it is trivially true of a server
// that publishes no enums at all; so the arm also requires the set to contain a
// REAL enum somewhere, which makes "free-text" a fact about this vocabulary
// rather than about the walk or about the schema style.
//
// Mutants (2026-09-10):
//
//	M72 pf_emit_event's event_type becomes a propEnum
//	    containing memory_commit_resolved                     RED
//	M73 every propEnum in the tool set becomes a plain prop    RED  (control arm)
//	M74 green control: reword event_type's description         GREEN
func TestResolveCommitEventTypeHasNoPublishedVocabulary(t *testing.T) {
	desc := publishedTool(t, "pf_resolve_commit").Description
	m := publishedEffectsRe.FindStringSubmatch(desc)
	if m == nil {
		t.Fatalf("pf_resolve_commit's description no longer names the event type it emits:\n  %s", desc)
	}
	eventType := m[2]

	tools := publishedToolList(t)
	if len(tools) < 40 {
		t.Fatalf("the published tool list holds %d tool(s); this census is over the whole set and a "+
			"short list would make its negative half vacuous", len(tools))
	}
	enumsSeen := 0
	for _, tool := range tools {
		schema := schemaJSON(t, tool.Name, tool.InputSchema)
		for _, enum := range regexp.MustCompile(`"enum"\s*:\s*\[([^\]]*)\]`).FindAllStringSubmatch(schema, -1) {
			enumsSeen++
			if strings.Contains(enum[1], eventType) {
				t.Errorf("%s publishes an enum containing %q: [%s]. The card records this event type "+
					"as free-text with no published vocabulary; a published enum makes that sentence "+
					"false and makes the value part of the wire contract.", tool.Name, eventType, enum[1])
			}
		}
	}
	if enumsSeen == 0 {
		t.Errorf("the census found no published enum in any of the %d tools, so \"no published "+
			"vocabulary\" is a statement about this walk rather than about %q",
			len(tools), eventType)
	}

	emit := publishedTool(t, "pf_emit_event")
	if emit == nil {
		t.Fatalf("pf_emit_event is not published; it is the one tool that takes an event type from a " +
			"caller, so the sentence's \"no published vocabulary\" has nowhere left to be false")
	}
	emitSchema := schemaJSON(t, emit.Name, emit.InputSchema)
	eventTypeProp := regexp.MustCompile(`"event_type"\s*:\s*\{[^}]*\}`).FindString(emitSchema)
	if eventTypeProp == "" {
		t.Fatalf("pf_emit_event publishes no event_type property:\n%s", emitSchema)
	}
	if strings.Contains(eventTypeProp, `"enum"`) {
		t.Errorf("pf_emit_event's event_type now publishes an enum: %s. Either the vocabulary is "+
			"published — and this card's Policy sentence, plus every other card recording a "+
			"free-text event type, has to change — or the enum is wrong.", eventTypeProp)
	}
}

// TestResolveCommitAndCommitAreDifferentPublishedTools is the hop-0-1 sentence
// about the name collision: "`pf_commit` is a different tool in a different file
// that does mean git".
//
// Cheap, and worth having anyway: the collision is the reason the card states
// what "commit" means here, so the day the two are merged or renamed that
// paragraph is wrong rather than merely redundant.
//
// Mutants (2026-09-10):
//
//	M75 pf_commit is registered in tools_memory.go            RED  (different-file
//	                                                          arm)
//	M76 pf_commit's description loses the word "commit"       RED  (git-meaning arm)
//	M77 green control: reword pf_resolve_commit's description  GREEN
func TestResolveCommitAndCommitAreDifferentPublishedTools(t *testing.T) {
	for _, name := range []string{"pf_resolve_commit", "pf_commit"} {
		if publishedTool(t, name) == nil {
			t.Fatalf("%s is not published, so the collision this card explains does not exist", name)
		}
	}

	// EVERY file naming each tool, rather than the last one seen: with one
	// filename per tool a second declaration elsewhere is invisible, and "in a
	// different file" is a claim about where the name appears at all.
	where := map[string][]string{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		raw, rerr := os.ReadFile(n)
		if rerr != nil {
			t.Fatalf("read %s: %v", n, rerr)
		}
		for _, tool := range []string{"pf_resolve_commit", "pf_commit"} {
			if strings.Contains(string(raw), `Name:        "`+tool+`"`) ||
				strings.Contains(string(raw), `Name: "`+tool+`"`) {
				where[tool] = append(where[tool], n)
			}
		}
	}
	if len(where["pf_commit"]) == 0 || len(where["pf_resolve_commit"]) == 0 {
		t.Fatalf("the walk found registrations %v; it must find both or the comparison below is "+
			"between one file and nothing", where)
	}
	for _, a := range where["pf_commit"] {
		for _, b := range where["pf_resolve_commit"] {
			if a == b {
				t.Errorf("both tool names are declared in %s. The card says they are different tools "+
					"in different files, which is what makes \"commit\" ambiguous enough to need "+
					"explaining; %v vs %v.", a, where["pf_commit"], where["pf_resolve_commit"])
			}
		}
	}
	if !strings.Contains(strings.ToLower(publishedTool(t, "pf_commit").Description), "commit staged changes") {
		t.Errorf("pf_commit's description no longer describes a git commit: %q. The collision the "+
			"card explains is between a git commit and a review annotation; if the git one stopped "+
			"being one, the paragraph is wrong rather than redundant.",
			publishedTool(t, "pf_commit").Description)
	}
}

// schemaJSON renders a published InputSchema back to JSON text.
//
// The SDK hands it back as a decoded value rather than the bytes, so the
// re-marshal is how the enum census reads what a caller is shown.
func schemaJSON(t *testing.T, tool string, schema any) string {
	t.Helper()
	b, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal %s's InputSchema: %v", tool, err)
	}
	return string(b)
}

// goConstValue returns the string value of a top-level const in a file.
func goConstValue(t *testing.T, path, name string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != name {
				continue
			}
			for _, v := range vs.Values {
				if lit, ok := v.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					return lit.Value
				}
			}
		}
	}
	t.Fatalf("%s declares no string const %s, so the value this arm compares against does not exist",
		path, name)
	return ""
}

// goFuncBody returns one top-level function's source text.
func goFuncBody(t *testing.T, path, src, name string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name.Name != name || fd.Body == nil {
			continue
		}
		return nodeText(t, path, src, fd.Body)
	}
	return ""
}

// nodeText slices a node's source out of src using its own offsets.
//
// ⚠️ Valid only because every parse in this file uses a FRESH FileSet holding
// exactly one file, so a node's Pos is a 1-based offset into that file's source.
// A shared FileSet would make these offsets cumulative across files and this
// would silently slice the wrong text.
func nodeText(t *testing.T, path, src string, n ast.Node) string {
	t.Helper()
	_ = path
	start := int(n.Pos()) - 1
	end := int(n.End()) - 1
	if start < 0 || end > len(src) || start >= end {
		return ""
	}
	return src[start:end]
}
