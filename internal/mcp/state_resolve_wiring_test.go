package mcp_test

// aihub#319 — the WIRING half of the ReadStateFile -> ResolveStateFile migration
// (aihub#141 / #149).
//
// internal/config/state_test.go already covers ResolveStateFile as a function.
// That is not enough and this repo has been burned by exactly that gap before:
// a pure-function suite stays green while the defect survives, because the
// mutation lives at a different layer than the tests exercise. What has to be
// pinned here is that each credential-injecting TOOL HANDLER goes through the
// resolver — so every assertion below travels the whole path the model travels:
// MCP tool call -> handler -> state dir on disk -> HTTP request body.
//
// The defect, precisely. A work item addressed by SLUG has, at various moments,
// two files in <ws>/.polyforge/state/:
//
//	aihub#319.json   the C6-2 PRE-CLAIM stub: attempt_id "", claimed false
//	wi_res319.json   the real claimed file, whose Slug is "aihub#319"
//
// config.ReadStateFile("aihub#319") does a filename lookup, finds the STUB,
// succeeds, and the tool then sends attempt_id="" to the server, which answers
// 409 CONFLICT_EPOCH_MISMATCH — an error that reads as "someone stole my
// attempt" when the real cause is reading the wrong local file.
//
// Two state-dir shapes are therefore exercised for every site, because they
// discriminate differently and both occur in the wild:
//
//	stubShadowed  stub + canonical. Pre-change: succeeds with EMPTY credentials.
//	              Post-change: the canonical credentials reach the wire.
//	stubCleaned   canonical only — what a normal claim leaves behind, because
//	              WriteClaimState deletes the stub. Pre-change: the tool dies
//	              with "state file not found". Post-change: it works.
//
// stubCleaned is the shape that discriminates even for a tool whose credentials
// never reach the wire at all (pf_remove_dependency — see its row).
//
// ⚠️ The credential sites are only half of it. `git grep -c config.ReadStateFile
// -- internal/mcp` reaching 0 is TRUE AND MEANINGLESS on its own: the same defect
// escaped the package through coding.WorktreePath, which four more tools
// (pf_diff, pf_commit, pf_push, pf_pr) reach with an unresolved work_item_id.
// That is why the acceptance criterion here is
// TestReadStateFileCallSitesAreAccountedFor — a repo-wide, executable inventory —
// and not a grep scoped to the package the fix happened to start in.
//
// Run: GOWORK=off go test ./internal/mcp/ -run 'TestSlugAddressed|TestForceTakeover|TestReadStateFile|TestPreClaimStub' -v
// No database and no environment variables are needed; git and gh are needed
// only by the worktree-tool tests, which skip explicitly without them.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/GMISWE/ieops-aihub/internal/mcp"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

const (
	// resolveSlug is the non-canonical alias the caller addresses the wi by.
	resolveSlug = "aihub#319"
	// resolveCanonical is the work_items.id the claim response echoed back, and
	// therefore the key the real state file is written under.
	resolveCanonical = "wi_res319"
	// The canonical file's credentials. Every forward assertion is that THESE
	// reach the wire.
	resolveAttempt = "ra_resolved"
	resolveSecret  = "s3cr3t-resolved"
	resolveEpoch   = 7
	// resolveStubSecret is the pre-claim stub's session_secret. It exists so a
	// failure message can say which of the two files the handler read, rather
	// than only that the value was wrong.
	resolveStubSecret = "stub-secret-never-claimed"
)

// newResolveWorkspace points the state directory at a fresh temp dir and
// returns its root.
func newResolveWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", root)
	return root
}

// writeResolveStub writes the C6-2 pre-claim state file: keyed by the SLUG,
// carrying a session_secret but NO attempt_id, exactly as pf_claim_work_item
// writes it before the server has answered.
func writeResolveStub(t *testing.T) {
	t.Helper()
	if err := config.WriteStateFile(&config.StateFile{
		WIID:          resolveSlug,
		IdemKey:       "idem_res319",
		SessionSecret: resolveStubSecret,
		Claimed:       false,
	}); err != nil {
		t.Fatalf("write pre-claim stub: %v", err)
	}
}

// writeResolveCanonical writes the post-claim state file: keyed by the CANONICAL
// id, with the slug recorded in Slug — which is the only thing
// ResolveStateFile's scan can match on.
func writeResolveCanonical(t *testing.T, worktrees map[string]string) {
	t.Helper()
	if err := config.WriteStateFile(&config.StateFile{
		WIID:          resolveCanonical,
		Slug:          resolveSlug,
		Project:       "aihub",
		AttemptID:     resolveAttempt,
		ClaimEpoch:    resolveEpoch,
		SessionSecret: resolveSecret,
		Claimed:       true,
		Worktrees:     worktrees,
	}); err != nil {
		t.Fatalf("write canonical state file: %v", err)
	}
}

// callToolBounded drives one registered MCP tool over an in-memory session with
// an explicit deadline, and returns the decoded result.
//
// The deadline is load-bearing rather than decorative. pf_ship and pf_wrap shell
// out to git and gh; if one of those ever blocks, the failure would surface as
// the go test binary's own panic after 10 minutes, which reads as "the harness
// is broken" and gets attributed to the environment. A blown deadline here is a
// clean FAIL that names the tool instead.
func callToolBounded(t *testing.T, f *fakeAihub, tool string, args map[string]any, d time.Duration) (map[string]any, bool) {
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

	cl := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "state-resolve-test", Version: "1.0.0"}, nil)
	session, err := cl.Connect(context.Background(), cTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		if ctx.Err() != nil {
			t.Fatalf("%s did not return within %s — treat this as a FAILURE of the call, "+
				"not of the harness: the handler blocked (state-file resolution or a git/gh child process)", tool, d)
		}
		t.Fatalf("call %s: %v", tool, err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("call %s returned no content", tool)
	}
	text, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("call %s returned %T, want TextContent", tool, res.Content[0])
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text.Text), &decoded); err != nil {
		// errResult carries a bare string, not JSON.
		return map[string]any{"_raw": text.Text}, res.IsError
	}
	return decoded, res.IsError
}

// assertCanonicalCredentials is the forward assertion, shared by every site
// whose credentials reach the wire: the request must carry the CANONICAL file's
// attempt_id / claim_epoch / session_secret, not the stub's empty ones.
func assertCanonicalCredentials(t *testing.T, where string, body map[string]any) {
	t.Helper()
	if body == nil {
		t.Fatalf("%s: request had no JSON body at all", where)
	}
	if got := body["attempt_id"]; got != resolveAttempt {
		extra := ""
		if got == "" || got == nil {
			extra = " — this is the empty attempt_id from the pre-claim stub, the exact value the server answers 409 CONFLICT_EPOCH_MISMATCH to"
		}
		t.Errorf("%s: attempt_id = %v, want %q%s", where, got, resolveAttempt, extra)
	}
	if got := body["claim_epoch"]; got != float64(resolveEpoch) {
		t.Errorf("%s: claim_epoch = %v, want %d", where, got, resolveEpoch)
	}
	if got := body["session_secret"]; got != resolveSecret {
		extra := ""
		if got == resolveStubSecret {
			extra = " — that is the STUB's secret, so the handler read " + resolveSlug + ".json instead of resolving to " + resolveCanonical + ".json"
		}
		t.Errorf("%s: session_secret = %v, want the canonical file's%s", where, got, extra)
	}
}

// credSite is one migrated call site, reached through its real tool.
type credSite struct {
	site string // the source location this row exists to cover
	tool string
	args map[string]any
	// wantPath is the upstream request path, asserted so a row cannot pass by
	// exercising some other endpoint.
	wantPath string
	// credsOnWire is false for the one site whose pkg/client method sends NO
	// body, so the injected credentials are unobservable downstream. Such a row
	// is only discriminating in the stubCleaned shape; see the row's comment.
	credsOnWire bool
}

func credSites() []credSite {
	return []credSite{
		{
			site: "internal/mcp/tools_memory.go pf_reinforce_memory",
			tool: "pf_reinforce_memory",
			args: map[string]any{
				"memory_id": "mem_res", "additional_context": "still true", "work_item_id": resolveSlug,
			},
			wantPath: "/v1/memories/mem_res/reinforce", credsOnWire: true,
		},
		{
			site: "internal/mcp/tools_memory.go pf_update_memory (aihub#319, post-dated Anne's PR)",
			tool: "pf_update_memory",
			args: map[string]any{
				"memory_id": "mem_res", "content": "a newer body", "work_item_id": resolveSlug,
			},
			wantPath: "/v1/memories/mem_res/update", credsOnWire: true,
		},
		{
			site: "internal/mcp/tools_memory.go pf_save_artifact",
			tool: "pf_save_artifact",
			args: map[string]any{
				"type": "methodology.spec", "work_item_id": resolveSlug, "content": "# spec",
			},
			wantPath: "/v1/memories", credsOnWire: true,
		},
		{
			site: "internal/mcp/tools_memory.go emitArtifactAction (adopt/close/ignore)",
			tool: "pf_adopt_artifact",
			args: map[string]any{
				"work_item_id": resolveSlug, "memory_id": "mem_res", "artifact_type": "methodology.spec",
			},
			wantPath: "/v1/events", credsOnWire: true,
		},
		{
			site: "internal/mcp/tools_dependency.go pf_create_dependency",
			tool: "pf_create_dependency",
			args: map[string]any{
				"blocked_wi_id": "wi_blocked", "blocking_wi_id": "wi_blocking",
				"kind": "blocks", "work_item_id": resolveSlug,
			},
			wantPath: "/v1/work_items/wi_blocked/dependencies", credsOnWire: true,
		},
		{
			// credsOnWire is false ON PURPOSE, and it is a finding rather than a
			// convenience: pkg/client.RemoveDependency issues a DELETE with a nil
			// body (client.go), so the attempt_id / claim_epoch / session_secret
			// this handler injects are built and then thrown away. Nothing
			// downstream can observe which state file was read, so the
			// stubShadowed shape cannot distinguish the two functions here. The
			// stubCleaned shape can, because there ReadStateFile fails outright.
			site: "internal/mcp/tools_dependency.go pf_remove_dependency",
			tool: "pf_remove_dependency",
			args: map[string]any{
				"blocked_wi_id": "wi_blocked", "blocking_wi_id": "wi_blocking",
				"kind": "blocks", "work_item_id": resolveSlug,
			},
			wantPath: "/v1/work_items/wi_blocked/dependencies/wi_blocking/blocks", credsOnWire: false,
		},
		{
			site: "internal/mcp/tools_release.go pf_cut_alpha",
			tool: "pf_cut_alpha",
			args: map[string]any{
				"project": "aihub", "repos": []any{"aihub"}, "work_item_id": resolveSlug,
			},
			wantPath: "/v1/releases/alpha", credsOnWire: true,
		},
		{
			site: "internal/mcp/tools_release.go pf_promote",
			tool: "pf_promote",
			args: map[string]any{
				"source_alpha_tag": "v1.0.0-alpha.1", "new_stable_tag": "v1.0.0",
				"project": "aihub", "work_item_id": resolveSlug,
			},
			wantPath: "/v1/releases/promote", credsOnWire: true,
		},
	}
}

// TestSlugAddressedToolsSendCanonicalCredentials is the stubShadowed shape: the
// pre-claim stub is present and SHADOWS the canonical file under a filename
// lookup. Pre-change every row here reaches the server with attempt_id="" and
// the stub's secret; post-change every row carries the real credentials.
func TestSlugAddressedToolsSendCanonicalCredentials(t *testing.T) {
	for _, s := range credSites() {
		t.Run(s.tool, func(t *testing.T) {
			newResolveWorkspace(t)
			writeResolveStub(t)
			writeResolveCanonical(t, nil)

			f := newFakeAihub(t)
			result, isErr := callToolBounded(t, f, s.tool, s.args, 20*time.Second)
			if isErr {
				t.Fatalf("%s (%s) failed: %v", s.tool, s.site, result)
			}

			calls := f.recorded()
			if len(calls) != 1 {
				t.Fatalf("%s made %d upstream calls, want exactly 1: %v", s.tool, len(calls), f.paths())
			}
			if calls[0].Path != s.wantPath {
				t.Fatalf("%s hit %s, want %s", s.tool, calls[0].Path, s.wantPath)
			}
			if !s.credsOnWire {
				t.Skipf("%s injects credentials the client never sends (nil DELETE body), so this shape "+
					"cannot distinguish ReadStateFile from ResolveStateFile; "+
					"TestSlugAddressedToolsResolveWhenOnlyTheCanonicalFileExists covers %s", s.tool, s.site)
			}
			assertCanonicalCredentials(t, s.site, calls[0].Body)
		})
	}
}

// TestSlugAddressedToolsResolveWhenOnlyTheCanonicalFileExists is the stubCleaned
// shape — the state dir a normal claim leaves behind, since WriteClaimState
// removes the stub. A filename lookup on the slug finds nothing, so pre-change
// every row dies with "state file not found" and sends no request at all.
//
// This is the shape that covers pf_remove_dependency, whose credentials are
// unobservable downstream.
func TestSlugAddressedToolsResolveWhenOnlyTheCanonicalFileExists(t *testing.T) {
	for _, s := range credSites() {
		t.Run(s.tool, func(t *testing.T) {
			newResolveWorkspace(t)
			writeResolveCanonical(t, nil)

			f := newFakeAihub(t)
			result, isErr := callToolBounded(t, f, s.tool, s.args, 20*time.Second)
			if isErr {
				t.Fatalf("%s (%s) refused a slug it can resolve: %v\n"+
					"A filename lookup on %q misses; the canonical file %q records Slug=%q and must be found by the scan.",
					s.tool, s.site, result, resolveSlug, resolveCanonical, resolveSlug)
			}
			calls := f.recorded()
			if len(calls) != 1 {
				t.Fatalf("%s made %d upstream calls, want exactly 1: %v", s.tool, len(calls), f.paths())
			}
			if calls[0].Path != s.wantPath {
				t.Fatalf("%s hit %s, want %s", s.tool, calls[0].Path, s.wantPath)
			}
			if s.credsOnWire {
				assertCanonicalCredentials(t, s.site, calls[0].Body)
			}
		})
	}
}

// ─── the two coding tools, which resolve a worktree before they call aihub ────

// resolveRepo is a real git repo whose origin is a local bare repo, so a push is
// a real push with no network involved.
type resolveRepo struct {
	wt     string
	bare   string
	branch string
	head   string
}

func requireGitAndSh(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"git", "sh"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not on PATH; only the two coding-tool rows need it — every other "+
				"aihub#319 assertion still ran", bin)
		}
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(full, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// newResolveRepo builds <root>/wt with <root>/bare as its origin, one commit,
// and a non-protected task branch checked out.
func newResolveRepo(t *testing.T, root string) *resolveRepo {
	t.Helper()
	requireGitAndSh(t)
	r := &resolveRepo{
		wt:     filepath.Join(root, "wt"),
		bare:   filepath.Join(root, "bare"),
		branch: "polyforge/res319",
	}
	runGit(t, "", "init", "--bare", "-q", r.bare)
	runGit(t, "", "clone", "-q", r.bare, r.wt)
	runGit(t, r.wt, "config", "user.email", "res319@example.invalid")
	runGit(t, r.wt, "config", "user.name", "res319")
	runGit(t, r.wt, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(r.wt, "seed.txt"), []byte("seed\n"), 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	runGit(t, r.wt, "add", "-A")
	runGit(t, r.wt, "commit", "-q", "-m", "seed")
	runGit(t, r.wt, "checkout", "-q", "-b", r.branch)
	r.head = runGit(t, r.wt, "rev-parse", "HEAD")
	return r
}

// fakeGHForResolve puts a `gh` on PATH that answers `pr list` with listJSON and
// `pr create` with a fixed PR. Nothing here touches the network, so neither
// coding test can hang on one.
func fakeGHForResolve(t *testing.T, listJSON string) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
case "$1 $2" in
  "pr list")
    cat <<'PRLIST'
%s
PRLIST
    ;;
  "pr create")
    echo '{"url":"https://example.invalid/pr/9","number":9}'
    ;;
  *)
    echo "fake gh: unhandled args: $@" >&2
    exit 1
    ;;
esac
`, listJSON)
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestSlugAddressedShipResolvesTheCanonicalWorktree covers
// internal/mcp/tools_coding.go's pf_ship (aihub#319 — it post-dated Anne's PR).
//
// pf_ship's state file is not only a credential bundle: coding.Ship derives the
// worktree from sf.WIID. Read through the stub, sf.WIID is the SLUG and the stub
// has neither a worktrees map nor project/slug to rebuild one from, so the whole
// chain dies before git is ever invoked and no event is emitted. Resolved, the
// canonical file supplies worktrees["aihub"] and the commit/push/PR chain runs —
// so the assertion is that the wi timeline gets its events, carrying the
// canonical attempt_id.
//
// ⚠️ Measured, not assumed: since coding.WorktreePath itself resolves (aihub#319
// finding 1), this test NO LONGER discriminates the tools_coding.go pf_ship line
// on its own. Reverting only that line leaves this green, because sf is used for
// exactly two things — WorktreePath(sf.WIID), which now compensates, and the
// default PR title at ship.go:112, which pf_ship can never reach (it rejects an
// empty pr_title before calling Ship). pf_ship consumes NO credential from sf,
// so with the worktree hop fixed there is nothing observable left.
//
// The line is kept anyway and held by TestReadStateFileCallSitesAreAccountedFor:
// coding.Ship's contract is that sf.WIID is canonical, and the moment anything
// in that chain reads a credential — which fusing keeps pushing toward — the
// stub would be back. Do not read this test as covering that line.
func TestSlugAddressedShipResolvesTheCanonicalWorktree(t *testing.T) {
	root := newResolveWorkspace(t)
	r := newResolveRepo(t, root)
	fakeGHForResolve(t, `[]`) // no PR on the branch yet -> push + create
	writeResolveStub(t)
	writeResolveCanonical(t, map[string]string{"aihub": r.wt})

	if err := os.WriteFile(filepath.Join(r.wt, "shipped.txt"), []byte("new work\n"), 0644); err != nil {
		t.Fatalf("write worktree file: %v", err)
	}

	f := newFakeAihub(t)
	result, isErr := callToolBounded(t, f, "pf_ship", map[string]any{
		"work_item_id": resolveSlug,
		"repo":         "aihub",
		"message":      "feat: resolved by slug",
		"pr_title":     "resolved by slug",
		"pr_body":      "body",
	}, 60*time.Second)
	if isErr {
		t.Fatalf("pf_ship returned an error result: %v", result)
	}

	// pf_ship reports failure as a JSON payload, not an error result, so `ok`
	// has to be read explicitly or a total failure would look like a pass.
	if result["ok"] != true {
		t.Fatalf("pf_ship ok=%v (stage=%v, error=%v).\n"+
			"Read through the pre-claim stub, sf.WIID is %q and the stub has no worktrees map, "+
			"so coding.Ship cannot find a worktree at all.",
			result["ok"], result["stage"], result["error"], resolveSlug)
	}
	if result["committed"] != true {
		t.Errorf("pf_ship committed=%v, want true", result["committed"])
	}

	var events []recordedCall
	for _, c := range f.recorded() {
		if c.Path == "/v1/events" {
			events = append(events, c)
		}
	}
	if len(events) == 0 {
		t.Fatalf("pf_ship emitted no timeline events; paths=%v", f.paths())
	}
	for _, c := range events {
		assertCanonicalCredentials(t, "tools_coding.go pf_ship event "+fmt.Sprint(c.Body["event_type"]), c.Body)
	}
}

// TestSlugAddressedWrapCompletesUnderTheCanonicalID covers
// internal/mcp/tools_coding.go's pf_wrap (Anne migrated it; this pins the wiring).
//
// The fake gh reports a PR that already covers HEAD, so coding.Wrap takes its
// idempotent no-op branch and the test never depends on a push succeeding. What
// is asserted is the half after it: complete_attempt is addressed to sf.WIID, so
// resolving turns a request against the SLUG into one against the canonical id —
// and the state file is deleted under the canonical key, which was the second
// half of Anne's aihub#149 change.
func TestSlugAddressedWrapCompletesUnderTheCanonicalID(t *testing.T) {
	root := newResolveWorkspace(t)
	r := newResolveRepo(t, root)
	fakeGHForResolve(t, fmt.Sprintf(
		`[{"url":"https://example.invalid/pr/3","number":3,"state":"OPEN","baseRefName":"main","commits":[{"oid":%q}]}]`,
		r.head))
	writeResolveStub(t)
	writeResolveCanonical(t, map[string]string{"aihub": r.wt})

	f := newFakeAihub(t)
	result, isErr := callToolBounded(t, f, "pf_wrap", map[string]any{
		"work_item_id": resolveSlug,
		"repo":         "aihub",
		"pr_title":     "resolved by slug",
		"pr_body":      "body",
	}, 60*time.Second)
	if isErr {
		t.Fatalf("pf_wrap failed: %v\n"+
			"Read through the pre-claim stub, sf.WIID is %q and the stub has no worktrees map, "+
			"so coding.Wrap cannot find a worktree at all.", result, resolveSlug)
	}

	wantPath := "/v1/work_items/" + resolveCanonical + "/complete"
	var complete *recordedCall
	for i, c := range f.recorded() {
		if strings.HasSuffix(c.Path, "/complete") {
			complete = &f.recorded()[i]
		}
	}
	if complete == nil {
		t.Fatalf("pf_wrap never completed the attempt; paths=%v", f.paths())
	}
	if complete.Path != wantPath {
		t.Errorf("complete_attempt went to %s, want %s — the URL is built from sf.WIID, "+
			"so the slug appearing here means the stub was read", complete.Path, wantPath)
	}
	assertCanonicalCredentials(t, "tools_coding.go pf_wrap complete_attempt", complete.Body)

	// aihub#149's second half: the terminal cleanup deletes the RESOLVED key, so
	// a slug-addressed wrap must not orphan the canonical file.
	if _, err := config.ReadStateFile(resolveCanonical); err == nil {
		t.Errorf("the canonical state file %s.json survived a slug-addressed wrap — "+
			"its credentials are now dead but still on disk", resolveCanonical)
	}
}

// ─── force_takeover: the ForceTakeoverResponse half of aihub#149 ──────────────

// TestForceTakeoverBySlugWritesASlugResolvableStateFile drives the real
// pf_force_takeover handler with a SLUG and then asserts the two things that
// make the written file usable afterwards: it is keyed by the canonical id, and
// it records the slug. A file with an empty Slug is invisible to
// ResolveStateFile's scan, so every later slug-addressed call would fail.
//
// The second half of the assertion is deliberately made through a real handler
// call rather than by calling ResolveStateFile: "the file has a Slug" is only
// interesting because a subsequent tool can find it.
func TestForceTakeoverBySlugWritesASlugResolvableStateFile(t *testing.T) {
	newResolveWorkspace(t)
	// The stale stub a prior claim of this same wi left behind. It must not
	// survive the takeover, or a filename lookup keeps finding it.
	writeResolveStub(t)

	f := newFakeAihub(t)
	f.on("/v1/work_items/"+resolveSlug+"/force_takeover", func(map[string]any) (int, any) {
		return 200, map[string]any{
			"id": resolveCanonical, "slug": resolveSlug, "project": "aihub",
			"prior_attempt_id": "ra_prior", "prior_actor_display": "someone else",
			"new_attempt_id": "ra_forced", "new_claim_epoch": 9, "ok": true,
		}
	})

	result, isErr := callToolBounded(t, f, "pf_force_takeover", map[string]any{
		"work_item_id": resolveSlug, "reason": "the holder went stale",
	}, 20*time.Second)
	if isErr {
		t.Fatalf("pf_force_takeover failed: %v", result)
	}
	if got := result["new_attempt_id"]; got != "ra_forced" {
		t.Errorf("new_attempt_id = %v, want ra_forced", got)
	}

	sf, err := config.ReadStateFile(resolveCanonical)
	if err != nil {
		t.Fatalf("no state file under the canonical key %s: %v — the handler keyed it by the "+
			"caller-supplied id instead of the `id` the server echoed", resolveCanonical, err)
	}
	if sf.Slug != resolveSlug {
		t.Errorf("written state file Slug = %q, want %q — an empty Slug is unmatchable by "+
			"ResolveStateFile's scan, so every later slug-addressed call fails", sf.Slug, resolveSlug)
	}
	if sf.Project != "aihub" {
		t.Errorf("written state file Project = %q, want aihub", sf.Project)
	}
	if sf.AttemptID != "ra_forced" || sf.ClaimEpoch != 9 {
		t.Errorf("written credentials = {AttemptID:%q ClaimEpoch:%d}, want {ra_forced 9}", sf.AttemptID, sf.ClaimEpoch)
	}
	if _, err := config.ReadStateFile(resolveSlug); err == nil {
		t.Errorf("the slug-keyed stub survived the takeover; a filename lookup will keep finding " +
			"it and sending its empty attempt_id")
	}

	// The half that matters to a caller: a SUBSEQUENT slug-addressed tool call
	// must pick up the credentials this takeover just wrote.
	f2 := newFakeAihub(t)
	after, isErr2 := callToolBounded(t, f2, "pf_reinforce_memory", map[string]any{
		"memory_id": "mem_after", "additional_context": "post-takeover", "work_item_id": resolveSlug,
	}, 20*time.Second)
	if isErr2 {
		t.Fatalf("a slug-addressed call after the takeover failed: %v", after)
	}
	calls := f2.recorded()
	if len(calls) != 1 {
		t.Fatalf("expected 1 upstream call after the takeover, got %v", f2.paths())
	}
	if got := calls[0].Body["attempt_id"]; got != "ra_forced" {
		t.Errorf("post-takeover attempt_id = %v, want ra_forced", got)
	}
	if got := calls[0].Body["claim_epoch"]; got != float64(9) {
		t.Errorf("post-takeover claim_epoch = %v, want 9", got)
	}
}

// TestForceTakeoverResponseCarriesTheIdentityKeysTheHandlerReads pins the wire
// contract the test above depends on from the PRODUCER side.
//
// The handler reads result["id"], result["slug"] and result["project"] out of
// the decoded force_takeover response. Those keys come from
// domain.ForceTakeoverResponse's json tags, and nothing else forces the two ends
// to agree — a renamed or deleted field there would leave the handler silently
// reading a missing key and writing an empty Slug again.
//
// The struct is inspected by TAG rather than by naming its fields in a literal,
// on purpose: deleting the field then turns this red instead of turning it into
// a compile error somewhere unrelated.
//
// Honest limit: this pins the SHAPE, not the population. Whether
// domain.FnForceTakeover actually fills ID/Slug/Project in can only be asserted
// against a database, and that function has no test of any kind today.
func TestForceTakeoverResponseCarriesTheIdentityKeysTheHandlerReads(t *testing.T) {
	rt := reflect.TypeOf(domain.ForceTakeoverResponse{})
	found := map[string]bool{}
	for i := 0; i < rt.NumField(); i++ {
		tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if tag != "" {
			found[tag] = true
		}
	}
	for _, key := range []string{"id", "slug", "project", "new_attempt_id", "new_claim_epoch"} {
		if !found[key] {
			t.Errorf("domain.ForceTakeoverResponse has no field tagged %q; "+
				"internal/mcp/tools_lifecycle.go reads result[%q] and would silently get nothing",
				key, key)
		}
	}
}

// ─── the four tools that resolve only a WORKTREE, via coding.WorktreePath ─────

// worktreeSite is one MCP tool that hands its raw work_item_id argument to
// coding.WorktreePath and never reads credentials from the state file itself.
//
// These four are why the original acceptance criterion was worthless. They
// contain no `config.ReadStateFile` at all — the filename read was one layer
// down, in internal/coding — so a grep scoped to internal/mcp reported the
// defect fixed while all four were still broken for a slug-addressed caller.
type worktreeSite struct {
	site      string
	tool      string
	extraArgs map[string]any
	// prep dirties the worktree so the tool has something real to do; nil when
	// the fixture repo is already in the right state.
	prep func(t *testing.T, r *resolveRepo)
	// wantEvent is the timeline event the tool emits, "" when it emits none.
	wantEvent string
	// assertResult checks the tool actually did its job, so that "did not error"
	// is not the whole assertion.
	assertResult func(t *testing.T, r *resolveRepo, result map[string]any)
}

// diffMarker is written into the worktree so pf_diff's output can be identified
// as a real diff of the RESOLVED worktree rather than any old text.
const diffMarker = "slug-resolved-worktree-marker"

func worktreeSites() []worktreeSite {
	return []worktreeSite{
		{
			site: "internal/mcp/tools_coding.go pf_diff -> coding.WorktreePath",
			tool: "pf_diff",
			prep: func(t *testing.T, r *resolveRepo) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(r.wt, "seed.txt"), []byte(diffMarker+"\n"), 0644); err != nil {
					t.Fatalf("dirty the worktree: %v", err)
				}
			},
			wantEvent: "", // pf_diff is read-only and emits nothing
			assertResult: func(t *testing.T, _ *resolveRepo, result map[string]any) {
				t.Helper()
				// pf_diff answers with raw diff text, not JSON, so it arrives as _raw.
				raw, _ := result["_raw"].(string)
				if !strings.Contains(raw, diffMarker) {
					t.Errorf("pf_diff output does not contain the worktree's own change %q; got %q",
						diffMarker, raw)
				}
			},
		},
		{
			site:      "internal/mcp/tools_coding.go pf_commit -> coding.WorktreePath",
			tool:      "pf_commit",
			extraArgs: map[string]any{"message": "feat: committed into the resolved worktree"},
			prep: func(t *testing.T, r *resolveRepo) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(r.wt, "committed.txt"), []byte(diffMarker+"\n"), 0644); err != nil {
					t.Fatalf("add a file to commit: %v", err)
				}
			},
			wantEvent: "commit",
			assertResult: func(t *testing.T, r *resolveRepo, result map[string]any) {
				t.Helper()
				sha, _ := result["sha"].(string)
				if sha == "" {
					t.Fatalf("pf_commit returned no sha: %v", result)
				}
				if head := runGit(t, r.wt, "rev-parse", "HEAD"); head != sha {
					t.Errorf("pf_commit reported %s but the resolved worktree's HEAD is %s — "+
						"it committed somewhere else", sha, head)
				}
			},
		},
		{
			site:      "internal/mcp/tools_coding.go pf_push -> coding.WorktreePath",
			tool:      "pf_push",
			wantEvent: "push",
			assertResult: func(t *testing.T, r *resolveRepo, result map[string]any) {
				t.Helper()
				if result["ok"] != true {
					t.Errorf("pf_push ok=%v, want true: %v", result["ok"], result)
				}
				if got := result["branch"]; got != r.branch {
					t.Errorf("pf_push branch = %v, want %q", got, r.branch)
				}
				// The push is real: the local bare origin must now carry the branch.
				if out := runGit(t, r.wt, "ls-remote", "--heads", "origin", "refs/heads/"+r.branch); out == "" {
					t.Errorf("origin has no %s after pf_push reported success", r.branch)
				}
			},
		},
		{
			site:      "internal/mcp/tools_coding.go pf_pr -> coding.WorktreePath",
			tool:      "pf_pr",
			extraArgs: map[string]any{"title": "resolved by slug", "body": "body"},
			wantEvent: "pr_opened",
			assertResult: func(t *testing.T, _ *resolveRepo, result map[string]any) {
				t.Helper()
				if got := result["number"]; got != float64(9) {
					t.Errorf("pf_pr number = %v, want 9 (the fake gh's PR)", got)
				}
			},
		},
	}
}

// runWorktreeSite builds the fixture and drives one worktree tool by SLUG.
func runWorktreeSite(t *testing.T, s worktreeSite, withStub bool) (*resolveRepo, *fakeAihub, map[string]any, bool) {
	t.Helper()
	root := newResolveWorkspace(t)
	r := newResolveRepo(t, root)
	fakeGHForResolve(t, `[]`)
	if withStub {
		writeResolveStub(t)
	}
	writeResolveCanonical(t, map[string]string{"aihub": r.wt})
	if s.prep != nil {
		s.prep(t, r)
	}

	args := map[string]any{"work_item_id": resolveSlug, "repo": "aihub"}
	for k, v := range s.extraArgs {
		args[k] = v
	}
	// workspace_root is deliberately NOT passed. It is WorktreePath's fallback
	// input, and supplying it would let the fallback reconstruct a path from
	// Project+Slug and mask whether the state file was resolved at all.

	f := newFakeAihub(t)
	result, isErr := callToolBounded(t, f, s.tool, args, 60*time.Second)
	return r, f, result, isErr
}

func assertWorktreeSite(t *testing.T, s worktreeSite, r *resolveRepo, f *fakeAihub, result map[string]any, isErr bool) {
	t.Helper()
	if isErr {
		t.Fatalf("%s (%s) refused a slug it can resolve: %v\n"+
			"coding.WorktreePath reads the state file for the id it is handed; a filename lookup on "+
			"%q finds either the pre-claim stub (no worktrees map, no project/slug) or nothing at all.",
			s.tool, s.site, result, resolveSlug)
	}
	s.assertResult(t, r, result)

	var events []recordedCall
	for _, c := range f.recorded() {
		if c.Path == "/v1/events" {
			events = append(events, c)
		}
	}
	if s.wantEvent == "" {
		if len(f.recorded()) != 0 {
			t.Errorf("%s is read-only and must make no upstream call, got %v", s.tool, f.paths())
		}
		return
	}
	if len(events) != 1 {
		t.Fatalf("%s emitted %d timeline events, want 1 (%s); paths=%v",
			s.tool, len(events), s.wantEvent, f.paths())
	}
	if got := events[0].Body["event_type"]; got != s.wantEvent {
		t.Errorf("%s emitted event_type=%v, want %q", s.tool, got, s.wantEvent)
	}
	assertCanonicalCredentials(t, s.site+" event", events[0].Body)
}

// TestSlugAddressedWorktreeToolsResolveTheCanonicalWorktree is the stubShadowed
// shape for the four tools that reach coding.WorktreePath with an unresolved id.
//
// Pre-change these do not merely send bad credentials — they cannot run at all:
// the pre-claim stub has no worktrees map and no project/slug to rebuild one
// from, so every one of them dies with "worktree path for repo ... not found"
// for a work item whose worktree is sitting right there on disk.
func TestSlugAddressedWorktreeToolsResolveTheCanonicalWorktree(t *testing.T) {
	for _, s := range worktreeSites() {
		t.Run(s.tool, func(t *testing.T) {
			r, f, result, isErr := runWorktreeSite(t, s, true)
			assertWorktreeSite(t, s, r, f, result, isErr)
		})
	}
}

// TestSlugAddressedWorktreeToolsResolveWhenOnlyTheCanonicalFileExists is the
// stubCleaned shape for the same four — the state dir a normal claim leaves
// behind. Pre-change the filename lookup misses entirely and the tools die with
// "read state file for wi ...: state file not found".
func TestSlugAddressedWorktreeToolsResolveWhenOnlyTheCanonicalFileExists(t *testing.T) {
	for _, s := range worktreeSites() {
		t.Run(s.tool, func(t *testing.T) {
			r, f, result, isErr := runWorktreeSite(t, s, false)
			assertWorktreeSite(t, s, r, f, result, isErr)
		})
	}
}

// TestSlugAddressedWorktreeToolsStillWorkByCanonicalID is the control. Every
// assertion above would also be satisfied by a WorktreePath that ignored its
// argument entirely and scanned for any claimed state file; this pins that the
// ordinary canonical-id path — the one every existing caller uses, including
// coding.Ship and coding.Wrap via sf.WIID — is untouched.
//
// PASSES ON THE PRE-CHANGE BUILD (359a435), and that is the point: it is the
// control, not the regression. A control that went red pre-change would mean the
// canonical path was broken before, which it was not.
func TestSlugAddressedWorktreeToolsStillWorkByCanonicalID(t *testing.T) {
	s := worktreeSites()[0] // pf_diff: read-only, so the control costs nothing
	root := newResolveWorkspace(t)
	r := newResolveRepo(t, root)
	writeResolveCanonical(t, map[string]string{"aihub": r.wt})
	s.prep(t, r)

	f := newFakeAihub(t)
	result, isErr := callToolBounded(t, f, s.tool, map[string]any{
		"work_item_id": resolveCanonical, "repo": "aihub",
	}, 60*time.Second)
	if isErr {
		t.Fatalf("pf_diff addressed by CANONICAL id failed: %v", result)
	}
	s.assertResult(t, r, result)
}

// ─── Finding 3: force_takeover must not destroy the worktree map ──────────────

// TestForceTakeoverBySlugKeepsTheWorktreeMap covers a regression the aihub#149
// canonicalisation introduced rather than fixed.
//
// Before it, a slug-addressed takeover wrote <slug>.json and left <canonical>.json
// — worktrees map and all — untouched beside it. Now the write is keyed by the
// canonical id, so it REPLACES that file; and the new StateFile is built from
// scratch out of the takeover response, which carries no worktrees. The map is
// therefore destroyed unless it is explicitly carried over.
//
// No git needed: the map is opaque to the handler, so a plain path pins it.
func TestForceTakeoverBySlugKeepsTheWorktreeMap(t *testing.T) {
	root := newResolveWorkspace(t)
	priorWorktree := filepath.Join(root, "pf.aihub-319", "aihub")
	writeResolveStub(t)
	writeResolveCanonical(t, map[string]string{"aihub": priorWorktree})

	f := newFakeAihub(t)
	f.on("/v1/work_items/"+resolveSlug+"/force_takeover", func(map[string]any) (int, any) {
		return 200, map[string]any{
			"id": resolveCanonical, "slug": resolveSlug, "project": "aihub",
			"new_attempt_id": "ra_forced", "new_claim_epoch": 9, "ok": true,
		}
	})
	result, isErr := callToolBounded(t, f, "pf_force_takeover", map[string]any{
		"work_item_id": resolveSlug, "reason": "the holder went stale",
	}, 20*time.Second)
	if isErr {
		t.Fatalf("pf_force_takeover failed: %v", result)
	}

	sf, err := config.ReadStateFile(resolveCanonical)
	if err != nil {
		t.Fatalf("read state file after takeover: %v", err)
	}
	if got := sf.Worktrees["aihub"]; got != priorWorktree {
		t.Errorf("worktrees[\"aihub\"] = %q after a slug-addressed takeover, want %q.\n"+
			"The takeover response carries no worktree map, and this write replaces the "+
			"canonical-keyed file rather than sitting beside it, so the map a prior claim "+
			"recorded is gone unless it is carried over.", got, priorWorktree)
	}
	// The credentials must still be the NEW ones — carrying the map over must not
	// drag the superseded attempt along with it.
	if sf.AttemptID != "ra_forced" {
		t.Errorf("AttemptID = %q after takeover, want ra_forced — the carry-over "+
			"overwrote the new credentials with the prior attempt's", sf.AttemptID)
	}
}

// TestForceTakeoverThenSlugAddressedDiffFindsTheWorktree is the same finding at
// the layer that actually hurts: the reported failure was force_takeover followed
// by a worktree tool with no workspace_root, which then has no path to fall back
// to and fails outright.
func TestForceTakeoverThenSlugAddressedDiffFindsTheWorktree(t *testing.T) {
	root := newResolveWorkspace(t)
	r := newResolveRepo(t, root)
	writeResolveStub(t)
	writeResolveCanonical(t, map[string]string{"aihub": r.wt})
	if err := os.WriteFile(filepath.Join(r.wt, "seed.txt"), []byte(diffMarker+"\n"), 0644); err != nil {
		t.Fatalf("dirty the worktree: %v", err)
	}

	f := newFakeAihub(t)
	f.on("/v1/work_items/"+resolveSlug+"/force_takeover", func(map[string]any) (int, any) {
		return 200, map[string]any{
			"id": resolveCanonical, "slug": resolveSlug, "project": "aihub",
			"new_attempt_id": "ra_forced", "new_claim_epoch": 9, "ok": true,
		}
	})
	if _, isErr := callToolBounded(t, f, "pf_force_takeover", map[string]any{
		"work_item_id": resolveSlug, "reason": "the holder went stale",
	}, 20*time.Second); isErr {
		t.Fatalf("pf_force_takeover failed")
	}

	f2 := newFakeAihub(t)
	result, isErr := callToolBounded(t, f2, "pf_diff", map[string]any{
		"work_item_id": resolveSlug, "repo": "aihub",
	}, 60*time.Second)
	if isErr {
		t.Fatalf("pf_diff after a slug-addressed force_takeover failed: %v\n"+
			"This is the reported scenario: the takeover replaced the canonical state file "+
			"with one that has no worktrees map, and with no workspace_root argument there "+
			"is nothing left to reconstruct a path from.", result)
	}
	if raw, _ := result["_raw"].(string); !strings.Contains(raw, diffMarker) {
		t.Errorf("pf_diff after the takeover did not diff the wi's own worktree; got %q", raw)
	}
}

// ─── the acceptance criterion, as an executable inventory ─────────────────────

// readStateFileAllowlist is every NON-TEST source file permitted to call
// config.ReadStateFile, with the count it may contain and why it is exempt.
//
// This replaces `git grep -c config.ReadStateFile -- internal/mcp`, which
// reported 0 while pf_diff / pf_commit / pf_push / pf_pr were still broken —
// the call had simply moved to internal/coding. A criterion scoped to the
// package a fix started in measures the fix, not the defect.
var readStateFileAllowlist = map[string]struct {
	count  int
	reason string
}{
	// ResolveStateFile's own two direct-lookup calls (the fast path and the
	// error-preserving fallback) plus FindStateFiles' per-filename read — which
	// is reading back a filename it just listed, so there is no alias to
	// resolve. The `func ReadStateFile(` declaration is not counted as a call.
	"internal/config/state.go": {3, "the resolver's own implementation"},

	// worktreePath() in the `polyforge` CLI. `--wi-id` is free text, so a slug
	// CAN arrive here. It is exempt because it is fail-closed rather than
	// wrong: it reads only sf.Worktrees, never a credential, and returns "" on
	// both slug shapes (error -> "", stub -> nil map -> ""). All three callers
	// (RunCommit / RunPush / RunPR) then print "could not determine worktree
	// path" and os.Exit(1). No git command runs against a wrong path and no
	// state is written. The premise that makes that true — that the pre-claim
	// stub carries no worktrees — is asserted by
	// TestPreClaimStubCarriesNoWorktreesOrCredentials below, not merely
	// asserted here in prose.
	//
	// It is still slug-INTOLERANT as a user experience, and that is worth its
	// own work item; this file is not locked for aihub#319 and was deliberately
	// not edited.
	"internal/cli/machine_user.go": {1, "fail-closed: reads only Worktrees, returns \"\", callers exit 1"},
}

// TestReadStateFileCallSitesAreAccountedFor walks the whole module and fails on
// any non-test call to config.ReadStateFile outside the allowlist above.
//
// It lives in internal/mcp because that is where this work item's test files
// belong; what it measures is repo-wide by construction.
func TestReadStateFileCallSitesAreAccountedFor(t *testing.T) {
	root := moduleRoot(t)
	found := map[string]int{}

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		n := 0
		for _, line := range strings.Split(string(b), "\n") {
			// The declaration is not a call site.
			if strings.Contains(line, "func ReadStateFile(") {
				continue
			}
			// Comments mentioning the name are not call sites either; the
			// trailing "(" already excludes most prose, and this excludes the rest.
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			n += strings.Count(line, "ReadStateFile(")
		}
		// Only files that actually call it enter the inventory — a zero entry
		// would report every file in the repo as an unlisted call site.
		if n > 0 {
			found[rel] = n
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	for file, n := range found {
		allowed, ok := readStateFileAllowlist[file]
		if !ok {
			t.Errorf("%s calls config.ReadStateFile %d time(s) and is not on the allowlist.\n"+
				"ReadStateFile is a filename lookup: it cannot resolve a slug, and it succeeds "+
				"on the pre-claim stub instead of failing. Use config.ResolveStateFile, or add "+
				"the file here with the reason a slug can never reach it. (aihub#319)", file, n)
			continue
		}
		if n != allowed.count {
			t.Errorf("%s calls config.ReadStateFile %d time(s), allowlisted for %d (%s). "+
				"A new call site needs its own justification.", file, n, allowed.count, allowed.reason)
		}
	}
	for file, allowed := range readStateFileAllowlist {
		if _, ok := found[file]; !ok {
			t.Errorf("allowlist entry %s (%s) no longer calls ReadStateFile — remove the entry "+
				"so the list keeps meaning something", file, allowed.reason)
		}
	}

	// The two packages the aihub#319 defect actually travelled through must stay
	// at zero, named explicitly so a regression says WHERE rather than only that
	// a count moved.
	for file := range found {
		if strings.HasPrefix(file, "internal/mcp/") || strings.HasPrefix(file, "internal/coding/") {
			t.Errorf("%s calls config.ReadStateFile; every id reaching internal/mcp or "+
				"internal/coding may be a slug", file)
		}
	}
}

// moduleRoot walks up from the test's working directory to the directory holding
// go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// TestPreClaimStubCarriesNoWorktreesOrCredentials is the premise the
// internal/cli/machine_user.go allowlist entry rests on, asserted rather than
// asserted-in-prose.
//
// worktreePath() there reads sf.Worktrees off whatever ReadStateFile hands back.
// Handed a slug in the stubShadowed shape that is the PRE-CLAIM STUB, and the
// only reason it fails closed instead of returning a path is that the stub has
// no worktrees map. If the C6-2 pre-claim write ever gained one, that CLI helper
// would start handing back a path derived from an unclaimed record and this
// assertion is what would notice.
//
// The stub is produced by the real pf_claim_work_item handler, not hand-written:
// the write happens before the server call, and a failed claim deliberately
// leaves it on disk for the retry — so a 500 from the fake is enough to capture
// exactly what that code path writes.
//
// PASSES ON THE PRE-CHANGE BUILD (359a435) — stated so nobody mistakes it for a
// regression test for this diff. aihub#319 changed nothing about the pre-claim
// write. Its job is forward-looking: it turns the allowlist's exemption argument
// into something that can be falsified by a LATER change to that write, instead
// of a sentence in a comment that would go quietly out of date.
func TestPreClaimStubCarriesNoWorktreesOrCredentials(t *testing.T) {
	newResolveWorkspace(t)

	f := newFakeAihub(t)
	f.on("/v1/work_items/"+resolveSlug+"/claim", func(map[string]any) (int, any) {
		return 500, map[string]any{"code": "INTERNAL", "message": "claim exploded"}
	})
	result, isErr := callToolBounded(t, f, "pf_claim_work_item", map[string]any{
		"work_item_id": resolveSlug, "idempotency_key": "idem_res319",
	}, 20*time.Second)
	if !isErr {
		t.Fatalf("expected the claim to fail so the stub is left behind, got %v", result)
	}

	stub, err := config.ReadStateFile(resolveSlug)
	if err != nil {
		t.Fatalf("a failed claim must leave the pre-claim stub on disk for the retry: %v", err)
	}
	if len(stub.Worktrees) != 0 {
		t.Errorf("the pre-claim stub carries a worktrees map (%v). "+
			"internal/cli/machine_user.go's worktreePath() reads that map off a slug-addressed "+
			"ReadStateFile and is only fail-closed because it is empty; with entries in it, that "+
			"helper would hand back a worktree path belonging to an unclaimed record.", stub.Worktrees)
	}
	if stub.AttemptID != "" {
		t.Errorf("the pre-claim stub carries AttemptID %q; it must be empty, or "+
			"ResolveStateFile's slug-scan would accept the stub as a claimed record", stub.AttemptID)
	}
	if stub.Claimed {
		t.Error("the pre-claim stub is marked claimed")
	}
}
