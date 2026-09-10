package mcp_test

// aihub#543 probe wave 2, the git tail — `docs/mcp-cards/pf_diff.md`.
//
// Four sentences, all of them about a hop nothing else in the tree drives for
// this tool:
//
//   - "`workspace_root` is the resolver's **fallback**, not its primary";
//   - a takeover that lost the worktree map makes this tool fail outright
//     unless the caller supplies `workspace_root`;
//   - `vs_base=true` diffs against the base branch and the default against
//     HEAD; and
//   - "**The result is returned as raw text content rather than JSON.** This is the
//     one tool in the set whose successful result is a `TextContent` that is
//     deliberately not a JSON object".
//
// 🔴 WHY NOTHING HELD THEM. internal/coding has unit tests for GitDiff, and
// internal/mcp/state_resolve_wiring_test.go drives pf_diff — but only to prove
// the state file is RESOLVED, with the worktree map present in every fixture.
// The precedence between the map and `workspace_root` is therefore untested in
// both directions, and it is the whole difference between "this tool reads the
// worktree the claim made" and "this tool reads a path it reconstructed from a
// naming convention". The raw-text sentence had nothing at all: every other
// assertion in this package decodes the result as JSON, which is exactly the
// thing this tool's result is not, so a change routing pf_diff through
// jsonResult would have gone unnoticed while silently breaking every caller
// that prints the diff.
//
// No database. git is required and is skipped for explicitly; every remote is a
// local bare repository under the test's own temp dir.
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestDiff|TestCorpus' -count=1 -v

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/mcp"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// diffCardPath is the card carrying the sentences below.
const diffCardPath = "../../docs/mcp-cards/pf_diff.md"

// corpusDirRel is the checked-in aihub#412 record the machine block copies.
const corpusDirRel = "../../docs/audits/aihub-412-corpus-facts/response-keys"

// callDiffText drives the real pf_diff tool and returns the RAW text of its
// result, plus whether the result was an error.
//
// 🔴 A separate driver from callToolBounded on purpose: that one decodes the
// result as JSON and falls back to a `_raw` key, which is precisely the
// distinction this file is about. A diff read through a JSON decoder is a diff
// nobody looked at.
func callDiffText(t *testing.T, f *fakeAihub, args map[string]any) (string, bool) {
	t.Helper()
	server := mcp.New(nil, client.New(f.server.URL, "test-key"))
	cTransport, sTransport := sdkmcp.NewInMemoryTransports()
	serverCtx, cancelServer := context.WithCancel(context.Background())
	t.Cleanup(cancelServer)
	go func() {
		session, err := server.Connect(serverCtx, sTransport)
		if err != nil {
			return
		}
		_ = session.Wait()
	}()

	cl := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "diff-shape-test", Version: "1.0.0"}, nil)
	session, err := cl.Connect(serverCtx, cTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	ctx, cancel := context.WithTimeout(serverCtx, 60*time.Second)
	defer cancel()
	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "pf_diff", Arguments: args})
	if err != nil {
		t.Fatalf("call pf_diff: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("pf_diff returned %d content block(s), want exactly 1 — the card's sentence is "+
			"about what the single successful result IS", len(res.Content))
	}
	text, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("pf_diff returned %T, want TextContent", res.Content[0])
	}
	return text.Text, res.IsError
}

// diffRepo is one worktree with a local bare origin, its base branch pushed and
// origin/HEAD set — which `git diff origin/HEAD...HEAD` needs and a clone of an
// empty bare repository does not have.
type diffRepo struct {
	root   string
	wt     string
	branch string
}

func newDiffRepo(t *testing.T, dirName string) *diffRepo {
	t.Helper()
	requireGitAndSh(t)
	root := t.TempDir()
	bare := filepath.Join(root, "bare")
	wt := filepath.Join(root, dirName)
	runGit(t, "", "init", "--bare", "-q", "-b", "main", bare)
	runGit(t, "", "clone", "-q", bare, wt)
	runGit(t, wt, "config", "user.email", "diff@example.invalid")
	runGit(t, wt, "config", "user.name", "diff")
	runGit(t, wt, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(wt, "seed.txt"), []byte("seed\n"), 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	runGit(t, wt, "add", "-A")
	runGit(t, wt, "commit", "-q", "-m", "seed")
	runGit(t, wt, "push", "-q", "origin", "main")
	runGit(t, wt, "remote", "set-head", "origin", "main")
	runGit(t, wt, "checkout", "-q", "-b", "polyforge/diff-probe")
	return &diffRepo{root: root, wt: wt, branch: "polyforge/diff-probe"}
}

// TestDiffPrefersTheClaimWorktreeMapOverWorkspaceRoot is the precedence
// sentence, and it needs BOTH paths to be live at once: the map names one real
// worktree and `workspace_root` would reconstruct a DIFFERENT real one, so
// whichever the resolver reaches is visible in the diff text itself.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M28 WorktreePath consults workspace_root first           RED  (preference arm)
//	M29 WorktreePath ignores the worktrees map               RED  (same)
//	M30 the fallback drops the pf.<project>-<seq> segment    RED  (control arm:
//	                                                         the fallback stops
//	                                                         resolving at all)
//	M31 green control: reword WorktreePath's comment         GREEN
func TestDiffPrefersTheClaimWorktreeMapOverWorkspaceRoot(t *testing.T) {
	root := newResolveWorkspace(t)

	// The worktree the claim recorded. Its uncommitted change names itself.
	mapped := newDiffRepo(t, "mapped")
	writeFileForDiff(t, mapped.wt, "seed.txt", "from-the-recorded-map\n")

	// The worktree `workspace_root` would reconstruct: pf.<project>-<seq>/<repo>,
	// with resolveSlug = aihub#319 and project aihub.
	reconstructed := filepath.Join(root, "pf.aihub-319", "aihub")
	if err := os.MkdirAll(filepath.Dir(reconstructed), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	fallback := newDiffRepo(t, "fallback")
	if err := os.Rename(fallback.wt, reconstructed); err != nil {
		t.Fatalf("move the fallback worktree into place: %v", err)
	}
	writeFileForDiff(t, reconstructed, "seed.txt", "from-the-reconstructed-path\n")

	f := newFakeAihub(t)

	// Both inputs supplied. The map must win.
	writeResolveCanonical(t, map[string]string{"aihub": mapped.wt})
	out, isErr := callDiffText(t, f, map[string]any{
		"work_item_id":   resolveCanonical,
		"repo":           "aihub",
		"workspace_root": root,
	})
	if isErr {
		t.Fatalf("pf_diff failed with both a worktree map and a workspace_root: %s", out)
	}
	if !strings.Contains(out, "from-the-recorded-map") {
		t.Errorf("pf_diff read the reconstructed path even though the state file names a worktree "+
			"for this repo. `workspace_root` is the FALLBACK: a wi whose worktree is not where the "+
			"naming convention would put it gets somebody else's diff.\n%s", out)
	}
	if strings.Contains(out, "from-the-reconstructed-path") {
		t.Errorf("pf_diff's answer contains the RECONSTRUCTED worktree's change:\n%s", out)
	}

	// Control: with the map empty the fallback is reached, so the preference
	// above is a real decision between two reachable paths rather than the only
	// one that works.
	writeResolveCanonical(t, map[string]string{})
	out2, isErr2 := callDiffText(t, f, map[string]any{
		"work_item_id":   resolveCanonical,
		"repo":           "aihub",
		"workspace_root": root,
	})
	if isErr2 {
		t.Fatalf("pf_diff failed with no map entry and a workspace_root supplied: %s\nThe fallback "+
			"is unreachable, so the preference asserted above compares one live path with a dead "+
			"one and would pass however the resolver were written", out2)
	}
	if !strings.Contains(out2, "from-the-reconstructed-path") {
		t.Errorf("with no map entry pf_diff did not read pf.<project>-<seq>/<repo>:\n%s", out2)
	}
}

// TestDiffWithoutTheClaimMapNeedsWorkspaceRootAndProjectAndSlug is the
// takeover sentence — "a `pf_force_takeover` that lost the map makes this tool
// fail outright **unless** the caller supplies `workspace_root`" — plus the
// condition the sentence leaves out and this arm measured: the fallback also
// needs the state file to still carry `project` and `slug`, so supplying
// `workspace_root` is necessary rather than sufficient.
//
// Mutants (2026-09-10):
//
//	M32 the resolver returns a path instead of an error when
//	    the map is absent and workspace_root is empty        RED
//	M33 the fallback stops requiring project/slug            RED  (third arm)
//	M34 green control: reword the two error strings          GREEN — the arm
//	                                                         asserts the failure
//	                                                         and the repo name,
//	                                                         not the wording
func TestDiffWithoutTheClaimMapNeedsWorkspaceRootAndProjectAndSlug(t *testing.T) {
	root := newResolveWorkspace(t)
	f := newFakeAihub(t)

	// A state file a takeover left without a worktree map, and no workspace_root
	// on the call.
	writeResolveCanonical(t, nil)
	out, isErr := callDiffText(t, f, map[string]any{
		"work_item_id": resolveCanonical,
		"repo":         "aihub",
	})
	if !isErr {
		t.Fatalf("pf_diff answered a NON-error result with no worktree map and no workspace_root: "+
			"%q. Answering anything here means it resolved a path from nowhere.", out)
	}
	if !strings.Contains(out, "aihub") {
		t.Errorf("the failure does not name the repo it could not resolve: %q", out)
	}
	// 🔴 And it must name the INPUT that would fix it. Without this the arm
	// accepts a resolver that built `pf.<project>-<seq>/<repo>` against an empty
	// workspace root and let git fail on the relative path — a failure either
	// way, so "it errored" alone distinguishes nothing, while what the caller
	// needs to know is which argument to supply.
	if !strings.Contains(out, "workspace_root") {
		t.Errorf("the failure does not mention workspace_root: %q. The card's sentence is that "+
			"supplying it is the way out; a caller told only that a path was unusable will not "+
			"discover that from this message.", out)
	}

	// With workspace_root supplied AND project/slug still on the state file, the
	// same call resolves — this is the "unless" half.
	reconstructed := filepath.Join(root, "pf.aihub-319", "aihub")
	if err := os.MkdirAll(filepath.Dir(reconstructed), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	r := newDiffRepo(t, "fallback")
	if err := os.Rename(r.wt, reconstructed); err != nil {
		t.Fatalf("move worktree: %v", err)
	}
	writeFileForDiff(t, reconstructed, "seed.txt", "reconstructed\n")
	out2, isErr2 := callDiffText(t, f, map[string]any{
		"work_item_id":   resolveCanonical,
		"repo":           "aihub",
		"workspace_root": root,
	})
	if isErr2 {
		t.Fatalf("pf_diff failed with workspace_root supplied: %s", out2)
	}
	if !strings.Contains(out2, "reconstructed") {
		t.Errorf("pf_diff resolved something other than pf.aihub-319/aihub:\n%s", out2)
	}

	// The condition the card's "unless" does not state: a state file with no
	// project/slug cannot be reconstructed from either, so `workspace_root` is
	// necessary and not sufficient. Asserted because a reader acting on the
	// sentence alone would supply workspace_root and still be stuck.
	if err := config.WriteStateFile(&config.StateFile{
		WIID:          resolveCanonical,
		AttemptID:     resolveAttempt,
		ClaimEpoch:    resolveEpoch,
		SessionSecret: resolveSecret,
		Claimed:       true,
	}); err != nil {
		t.Fatalf("write a state file with no project/slug: %v", err)
	}
	out3, isErr3 := callDiffText(t, f, map[string]any{
		"work_item_id":   resolveCanonical,
		"repo":           "aihub",
		"workspace_root": root,
	})
	if !isErr3 {
		t.Fatalf("pf_diff resolved a path from a state file carrying neither a worktree map nor "+
			"project/slug, answering %q. There is nothing left to reconstruct pf.<project>-<seq> "+
			"from, so a path here is a guess.", out3)
	}
	if !strings.Contains(out3, "project") && !strings.Contains(out3, "slug") {
		t.Errorf("the failure does not say which fields were missing: %q — a caller told only "+
			"\"not found\" will supply workspace_root again", out3)
	}
}

// TestDiffVsBaseComparesTheBaseBranchAndTheDefaultComparesHead drives both
// values of `vs_base` against one repository holding two distinguishable
// changes: one committed on the task branch, one uncommitted in the worktree.
//
// Both directions, because a single-sided arm passes against a tool that
// ignores the flag: the default must show the uncommitted change and NOT the
// committed one, and `vs_base=true` the other way round.
//
// Mutants (2026-09-10):
//
//	M35 GitDiff ignores vsBase (always `diff HEAD`)          RED
//	M36 GitDiff always diffs origin/HEAD...HEAD              RED
//	M37 the handler stops forwarding vs_base                 RED
//	M38 green control: reword the parameter description       GREEN
func TestDiffVsBaseComparesTheBaseBranchAndTheDefaultComparesHead(t *testing.T) {
	newResolveWorkspace(t)
	r := newDiffRepo(t, "wt")
	writeResolveCanonical(t, map[string]string{"aihub": r.wt})

	// Committed on the task branch: visible against the base branch only.
	writeFileForDiff(t, r.wt, "committed-on-the-branch.txt", "committed\n")
	runGit(t, r.wt, "add", "-A")
	runGit(t, r.wt, "commit", "-q", "-m", "a commit on the task branch")

	// Uncommitted, on a TRACKED file: `git diff HEAD` does not report untracked
	// paths, so an untracked fixture would make the default arm vacuous.
	writeFileForDiff(t, r.wt, "seed.txt", "uncommitted-edit\n")

	f := newFakeAihub(t)
	args := func(vsBase any) map[string]any {
		a := map[string]any{"work_item_id": resolveCanonical, "repo": "aihub"}
		if vsBase != nil {
			a["vs_base"] = vsBase
		}
		return a
	}

	def, isErr := callDiffText(t, f, args(nil))
	if isErr {
		t.Fatalf("pf_diff (default) failed: %s", def)
	}
	if !strings.Contains(def, "uncommitted-edit") {
		t.Errorf("the default diff does not contain the uncommitted change, so it is not comparing "+
			"the working tree against HEAD:\n%s", def)
	}
	if strings.Contains(def, "committed-on-the-branch.txt") {
		t.Errorf("the default diff contains a change that is already committed; \"uncommitted work\" "+
			"is what the card says this value answers:\n%s", def)
	}

	base, isErr := callDiffText(t, f, args(true))
	if isErr {
		t.Fatalf("pf_diff (vs_base=true) failed: %s", base)
	}
	if !strings.Contains(base, "committed-on-the-branch.txt") {
		t.Errorf("vs_base=true does not contain the commit this branch added, so it is not comparing "+
			"against the base branch — which is what a \"what did this wi change\" question means:\n%s", base)
	}
	if strings.Contains(base, "uncommitted-edit") {
		t.Errorf("vs_base=true contains an uncommitted edit. The two values would then answer the "+
			"same question and a reviewer could not tell delivered work from work in progress:\n%s", base)
	}
}

// TestDiffAnswersRawTextAndIsTheOnlyToolThatDoes is the raw-text sentence, in
// both of its halves.
//
// The first half is driven: the successful result is the git diff itself and
// does not parse as a JSON object, so a caller decoding every result as JSON
// gets nothing from it.
//
// The second half — "the ONE tool in the set" — is a census, because a claim
// about the whole tool set cannot be checked by driving one tool. Every
// `&sdkmcp.CallToolResult{` literal in this package's non-test source is
// enumerated and classified by the function it sits in: the three marshalling
// helpers, and handler-level literals. Exactly one handler-level literal may
// exist, and it must be the one whose text is pf_diff's `diff` value.
//
// Mutants (2026-09-10):
//
//	M39 pf_diff returns jsonResult(diff)                     RED  (both halves:
//	                                                         the census finds no
//	                                                         handler literal and
//	                                                         the result parses)
//	M40 a second tool returns a bare TextContent literal     RED  (census arm)
//	M42 green control: move the literal onto one line        GREEN
func TestDiffAnswersRawTextAndIsTheOnlyToolThatDoes(t *testing.T) {
	newResolveWorkspace(t)
	r := newDiffRepo(t, "wt")
	writeResolveCanonical(t, map[string]string{"aihub": r.wt})
	writeFileForDiff(t, r.wt, "seed.txt", "an edit the diff must contain\n")

	f := newFakeAihub(t)
	out, isErr := callDiffText(t, f, map[string]any{
		"work_item_id": resolveCanonical, "repo": "aihub",
	})
	if isErr {
		t.Fatalf("pf_diff failed: %s", out)
	}
	if !strings.Contains(out, "diff --git") || !strings.Contains(out, "an edit the diff must contain") {
		t.Fatalf("pf_diff's result is not the raw diff:\n%s", out)
	}
	var asObject map[string]any
	if err := json.Unmarshal([]byte(out), &asObject); err == nil {
		t.Errorf("pf_diff's successful result parses as a JSON object %v. The card's sentence is that "+
			"it deliberately does not, and every other assertion in this package decodes results as "+
			"JSON — so this is the one shape a change here would silently alter.", asObject)
	}

	handlers, helpers := toolResultLiterals(t)
	const floorResultLiterals = 3
	if helpers < floorResultLiterals {
		t.Errorf("the census found only %d marshalling-helper CallToolResult literal(s), floor is %d "+
			"— a walk that finds too few has stopped seeing this package and the handler count below "+
			"means nothing", helpers, floorResultLiterals)
	}
	if len(handlers) != 1 {
		t.Errorf("the census found %d tool-handler CallToolResult literal(s) %v, want exactly 1. The "+
			"card says pf_diff is the ONE tool whose successful result is a TextContent that is not a "+
			"JSON object; a second one makes that sentence false in another card nobody edited, and "+
			"zero means pf_diff now answers through a marshalling helper.", len(handlers), handlers)
	} else if !strings.Contains(handlers[0], "diff") {
		t.Errorf("the one tool-handler CallToolResult literal is %q, which is not pf_diff's. The "+
			"sentence names this tool, so the census has to land on it.", handlers[0])
	}
}

// toolResultLiterals enumerates every `&sdkmcp.CallToolResult{` composite
// literal in this package's non-test source, returning a description of the
// handler-level ones and a count of the ones inside a marshalling helper.
//
// go/parser rather than grep: a literal inside a fixture string would be
// counted by a scanner, and the classification below is by ENCLOSING FUNCTION,
// which text matching cannot see at all.
func toolResultLiterals(t *testing.T) (handlers []string, helpers int) {
	t.Helper()
	// The functions whose whole job is to build a result; everything else is a
	// handler. Named rather than pattern-matched so a fourth helper is a signed
	// change to this list.
	resultHelpers := map[string]bool{
		"jsonResult": true, "errResult": true, "jsonResultCompact": true,
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	parsed := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		parsed++
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fd, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				sel, ok := lit.Type.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "CallToolResult" {
					return true
				}
				if resultHelpers[fd.Name.Name] {
					helpers++
					return true
				}
				handlers = append(handlers, describeResultLiteral(fset, name, fd.Name.Name, lit))
				return true
			})
		}
	}
	if parsed == 0 {
		t.Fatalf("no non-test .go file was parsed; the census walk is broken, not the code")
	}
	return handlers, helpers
}

// describeResultLiteral renders one literal as "<file>:<func> text=<expr>", so a
// failure names the site and the value it answers with.
func describeResultLiteral(fset *token.FileSet, file, fn string, lit *ast.CompositeLit) string {
	text := "?"
	ast.Inspect(lit, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Text" {
			return true
		}
		if id, ok := kv.Value.(*ast.Ident); ok {
			text = id.Name
		}
		return true
	})
	_ = fset
	return file + ":" + fn + " text=" + text
}

// TestCorpusRecordsEveryPfDiffResultAsProse is the hop-5 sentence's REASON:
// `response_keys_observed` is an empty list "because every successful result is
// prose".
//
// K7 (TestContractCardsMatchTheCorpusResponseKeys) already holds the card's
// list against this record in both directions, including null-against-a-record
// — so what is left unheld is the causal half, which is a different field of the
// same file. `result_kinds` is what the extractor recorded, and a record with a
// `json_object` count is a record whose tool DID hand callers keys.
//
// ⚠️ "prose" alone is not the discriminator and the control below is why:
// nearly every tool's record carries some prose, because an error result is a
// bare string. pf_diff's record is the one with NO json_object entry at all.
//
// Mutants (2026-09-10):
//
//	M43 pf_diff's record gains a json_object count           RED
//	M44 pf_diff's record drops to calls=0                    RED  (measurable-vs-
//	                                                         nothing-observed)
//	M45 the control tool's record loses its json_object      RED  (control arm)
//	M46 green control: reword the record's `note`            GREEN
func TestCorpusRecordsEveryPfDiffResultAsProse(t *testing.T) {
	diff := corpusRecord(t, "pf_diff")

	if diff.Calls < 1 {
		t.Fatalf("the corpus record for pf_diff reports %d call(s). The card's sentence is that the "+
			"corpus HAS records for this tool and the key union is empty anyway — with no calls, an "+
			"empty union would mean \"never observed\" instead.", diff.Calls)
	}
	if len(diff.Keys) != 0 {
		t.Errorf("pf_diff's record observed top-level keys %v. K7 holds the card's copy against this "+
			"record; this arm is about WHY the list is empty, and a non-empty record makes the "+
			"sentence's reason false rather than its list.", diff.Keys)
	}
	if n, has := diff.ResultKinds["json_object"]; has {
		t.Errorf("pf_diff's record counts %d json_object result(s). The card says every successful "+
			"result is prose, which is what makes an empty key union a statement about the RESULT "+
			"SHAPE rather than about missing data.", n)
	}
	if diff.ResultKinds["prose"] < 1 {
		t.Errorf("pf_diff's record counts no prose results at all: %v. The reason the card gives is "+
			"then unsupported by the record it points at.", diff.ResultKinds)
	}

	// The control. Prose appears in almost every record because an error result
	// is a bare string, so "has prose" would be satisfied by any tool at all;
	// what distinguishes pf_diff is having NO json_object results.
	control := corpusRecord(t, "pf_get_work_item")
	if control.ResultKinds["json_object"] < 1 {
		t.Errorf("the control record (pf_get_work_item) counts no json_object results either: %v. "+
			"Then \"no json_object\" is a property of the extractor rather than of pf_diff, and the "+
			"assertion above distinguishes nothing.", control.ResultKinds)
	}
	if control.ResultKinds["prose"] < 1 {
		t.Errorf("the control record counts no prose results: %v. The ⚠️ above — that prose alone "+
			"cannot be the discriminator — is what this arm exists to keep true.", control.ResultKinds)
	}
}

// corpusFacts is the part of an aihub#412 record these arms read.
type corpusFacts struct {
	Calls       int            `json:"calls"`
	Keys        []string       `json:"observed_top_level_keys"`
	ResultKinds map[string]int `json:"result_kinds"`
}

func corpusRecord(t *testing.T, tool string) corpusFacts {
	t.Helper()
	path := filepath.Join(corpusDirRel, tool+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v — this record is the published side of the hop-5 sentence, so a "+
			"missing file is a failure rather than a skip", path, err)
	}
	var out corpusFacts
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return out
}

// TestNullResponseKeysMeansNoCorpusRecordAtAll is the other side of the same
// distinction, and it is `docs/mcp-cards/pf_resolve_commit.md`'s hop-5 sentence:
// its `response_keys_observed` is `null` because the corpus holds no record for
// that tool.
//
// K7 refuses the two mismatched combinations (a list with no record, null with a
// record). What it cannot say is that the distinction is LIVE — that the card
// set really contains both a null card and an empty-list card, which is what
// makes "an empty list is a different fact from null" a statement about this
// repo rather than about JSON.
//
// Mutants (2026-09-10):
//
//	M47 a corpus record appears for pf_resolve_commit         RED
//	M48 pf_resolve_commit.md carries [] instead of null       RED
//	M49 pf_diff.md carries null instead of []                 RED
//	M50 green control: reword either hop-5 paragraph          GREEN
func TestNullResponseKeysMeansNoCorpusRecordAtAll(t *testing.T) {
	// The null side: no record on disk, and the card says null.
	path := filepath.Join(corpusDirRel, "pf_resolve_commit.json")
	if _, err := os.Stat(path); err == nil {
		t.Errorf("%s now exists, so pf_resolve_commit's card may no longer say null. K7 reports that "+
			"as CORPUS_NULL_MASKS_RECORD; this arm names the sentence that has to change with it.", path)
	}
	if raw := cardMachineBlockRaw(t, "../../docs/mcp-cards/pf_resolve_commit.md"); !strings.Contains(
		raw, `"response_keys_observed": null`) {
		t.Errorf("pf_resolve_commit's machine block no longer carries response_keys_observed=null:\n%s\n"+
			"null is what \"the corpus holds no record for this tool\" is written as, and the card's "+
			"hop-5 paragraph says exactly that.", raw)
	}

	// The empty-list side, so the two facts are both present in the set and the
	// distinction is one a reader can act on.
	if raw := cardMachineBlockRaw(t, diffCardPath); !strings.Contains(
		raw, `"response_keys_observed": []`) {
		t.Errorf("pf_diff's machine block no longer carries response_keys_observed=[]:\n%s\nWith both "+
			"cards carrying the same value the distinction the two hop-5 paragraphs draw stops being "+
			"observable anywhere in the set.", raw)
	}
}

// cardMachineBlockRaw returns a card's fenced machine block verbatim.
func cardMachineBlockRaw(t *testing.T, cardPath string) string {
	t.Helper()
	raw, err := os.ReadFile(cardPath)
	if err != nil {
		t.Fatalf("read %s: %v", cardPath, err)
	}
	start := strings.Index(string(raw), "```json")
	end := strings.Index(string(raw), "```\n\n")
	if start < 0 || end <= start {
		t.Fatalf("%s carries no fenced machine block", cardPath)
	}
	return string(raw)[start : end+3]
}

// writeFileForDiff writes one file into a worktree.
func writeFileForDiff(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
