package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/mcp"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// Cross-implementation contract gate for the WORK-ITEM lifecycle (aihub#667).
//
// WHAT THIS EXISTS TO PROTECT
// ---------------------------
// aihub#640 names two ways the polyforge workflow runs: B/C, a Claude Code / pi session where a
// model calls `pf_claim_work_item` over MCP stdio, and A, `polyforge drain`, a headless Go process
// with no model in it. Until aihub#667 there was exactly one implementation of the claim and it
// was reachable only from B/C, so A could not claim at all and refused loudly rather than writing
// a second one — because a second one is precisely what aihub#640's workflow_identity_constraint
// forbids, and because the FIRST one needed two separate incident fixes (aihub#328, aihub#257) to
// get worktree adoption right.
//
// aihub#667 moved it to internal/lifecycle and made both paths call it. That trade is only safe if
// the two paths really do produce the same thing, and "the same thing" here is not a return value:
// it is SERVER STATE AND DISK STATE. A claim's whole product is a row on aihub, a credential file
// on this machine, and a set of worktrees on branches with particular upstreams. Two callers can
// agree on every returned struct field and still differ in which branch a worktree is on.
//
// WHY THIS SHAPE OF GATE
// aihub#657 established the shape for the step layer ("same input => same []StepCall") and this is
// its analogue. Both halves are driven for real:
//
//   - THE MCP PATH goes through a real MCP session (NewInMemoryTransports + Server.Connect +
//     session.CallTool), not through a Go call to the handler. That is deliberate: the handler is
//     reached in production only after argument parsing, schema validation and the aihub#586
//     unpublished-argument strip, and a gate that called the closure directly would be comparing
//     a path nobody runs.
//   - THE GO PATH goes through drainClaimer.Claim, the actual seam `polyforge drain` wires into
//     drain.Runner — not through lifecycle.Claim directly. If drain ever grows a step between the
//     two, this is where it shows.
//
// Each path runs in its OWN workspace built from the SAME fixture, so the comparison is between
// two independent executions rather than between one execution and a recording of it.
//
// WHAT IS ASSERTED, per fixture
//  1. The claim request body the server received is identical, modulo the session_secret (which is
//     32 random bytes by construction and must therefore DIFFER — asserted, not waived).
//  2. The repo_pins request body is identical, modulo the same secret and the sha (two independent
//     clones of the same fixture have the same sha, so this one is compared for real).
//  3. The state file is identical, modulo session_secret and claimed_at.
//  4. The worktree layout is identical: which repos got a worktree, the branch each has checked
//     out, its upstream, and the .git/info/exclude content.
//  5. The worktree_problems the caller is shown are identical.
//
// The fixtures reach all three branches of the worktree loop — create, adopt-a-healthy-directory,
// refuse-a-half-built-one — because agreement on one branch is agreement about almost nothing.
//
// Deliberately NOT asserted: that the aihub ROW is byte-identical. This gate drives a fake server;
// what it can pin is the request that reaches it, which is the whole of what the client controls.
//
//	GOWORK=off go test ./internal/cli/ -run TestClaimLifecycle -count=1

// ─── fixture ──────────────────────────────────────────────────────────────────

const (
	contractWIID    = "wi_01JCONTRACT667AAAAAAAAAAA"
	contractSlug    = "aihub#667"
	contractProject = "aihub"
	contractRepo    = "aihub"
	contractGoal    = "extract the work item lifecycle"
	contractWIType  = "feature"
	// contractBranch is what newClaimBranchNames derives from the project, seq and goal above.
	// Spelled out so a change to the derivation shows up here as a named branch rather than as
	// two paths agreeing on a new wrong answer.
	contractBranch = "polyforge/aihub-667-extract-the-work-item-lifecycle"
)

// contractWorkspace is one isolated polyforge workspace: a bare "origin", a .repo clone, and a
// .polyforge.yaml naming one project with one repo.
type contractWorkspace struct {
	root string
	bare string
	src  string
}

func newContractWorkspace(t *testing.T) *contractWorkspace {
	t.Helper()
	root := t.TempDir()
	w := &contractWorkspace{
		root: root,
		bare: filepath.Join(root, "origin.git"),
		src:  filepath.Join(root, ".repo", contractRepo),
	}
	contractGit(t, "", "init", "--bare", "-q", "-b", "main", w.bare)
	contractGit(t, "", "clone", "-q", w.bare, w.src)
	contractGit(t, w.src, "config", "user.email", "t@t.test")
	contractGit(t, w.src, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(w.src, "main.txt"), []byte("base"), 0o644); err != nil {
		t.Fatalf("seed the clone: %v", err)
	}
	contractGit(t, w.src, "add", "main.txt")
	// A FIXED author and committer date, so the two workspaces this test builds produce the
	// SAME commit sha. That turns the repo_pins comparison from a formality into a real
	// assertion: with wall-clock dates the two shas differ for a reason that has nothing to do
	// with the code, and the only way to compare the bodies at all would be to normalise away
	// the one value repo pins exist to carry.
	contractCommit(t, w.src, "base")
	contractGit(t, w.src, "push", "-q", "-u", "origin", "main")

	yaml := fmt.Sprintf("version: 1\nprojects:\n  %s:\n    repos:\n      - name: %s\n        url: %s\n",
		contractProject, contractRepo, w.bare)
	if err := os.WriteFile(filepath.Join(root, ".polyforge.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write .polyforge.yaml: %v", err)
	}
	return w
}

// wtDir is <root>/pf.<project>-<seq>, the directory the claim materialises worktrees under.
func (w *contractWorkspace) wtDir() string {
	return filepath.Join(w.root, "pf."+contractProject+"-667")
}

func (w *contractWorkspace) wtPath() string { return filepath.Join(w.wtDir(), contractRepo) }

func contractGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// contractCommit makes a commit whose sha is a function of the tree alone.
func contractCommit(t *testing.T, dir, msg string) {
	t.Helper()
	const fixedDate = "2026-01-01T00:00:00+00:00"
	cmd := exec.Command("git", "-C", dir, "commit", "-q", "-m", msg)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE="+fixedDate,
		"GIT_COMMITTER_DATE="+fixedDate,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.test",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}

func contractGitQuiet(dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", full...).Output()
	return strings.TrimSpace(string(out)), err
}

// ─── fake aihub ───────────────────────────────────────────────────────────────

// contractHub records the bodies of the two requests a claim makes, which is the whole of the
// server-side state this process controls.
type contractHub struct {
	srv       *httptest.Server
	claimBody map[string]any
	pinsBody  map[string]any
}

func newContractHub(t *testing.T) *contractHub {
	t.Helper()
	h := &contractHub{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/work_items/"+contractWIID+"/claim", func(w http.ResponseWriter, r *http.Request) {
		h.claimBody = readJSONBody(t, r)
		writeJSON(w, map[string]any{
			"ok": true, "attempt_id": "ra_CONTRACT667", "claim_epoch": 1,
			"id": contractWIID, "slug": contractSlug, "project": contractProject,
			"goal": contractGoal, "wi_type": contractWIType,
			"requires_human_session": false,
			"acquired_locks":         []any{},
		})
	})
	mux.HandleFunc("/v1/work_items/"+contractWIID+"/repo_pins", func(w http.ResponseWriter, r *http.Request) {
		h.pinsBody = readJSONBody(t, r)
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// A claim that reached an unexpected route is a difference between the two paths that
		// the body comparison would never see, so it fails here rather than 404ing quietly.
		t.Errorf("unexpected request to the fake aihub: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)
	return h
}

func readJSONBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse request body %q: %v", b, err)
	}
	return m
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ─── the observation ──────────────────────────────────────────────────────────

// claimObservation is everything a claim left behind that a caller can be held to.
type claimObservation struct {
	ClaimBody map[string]any
	PinsBody  map[string]any
	State     map[string]any
	Worktrees map[string]worktreeFacts
	Problems  []string
	// Secret is pulled out separately: it must DIFFER between the two runs, so comparing it
	// with everything else would be asserting the opposite of what is true.
	Secret string
}

type worktreeFacts struct {
	Branch   string
	Upstream string
	Excludes string
}

// observe reads the disk half of a finished claim.
func observe(t *testing.T, w *contractWorkspace, h *contractHub, problems []string) claimObservation {
	t.Helper()

	// Every absolute path is rewritten to the token <ws>. The two runs live in different
	// t.TempDir()s by construction, so without this the comparison would report a difference
	// on every line that carries a path and could never see a real one. The token is applied
	// to the OBSERVATION rather than to the assertion, so a path that escaped the workspace
	// would survive the rewrite and show up as the raw string it is.
	obs := claimObservation{
		ClaimBody: normaliseAny(h.claimBody, w.root).(map[string]any),
		Problems:  normaliseStrings(problems, w.root),
	}
	if h.pinsBody != nil {
		obs.PinsBody = normaliseAny(h.pinsBody, w.root).(map[string]any)
	}

	b, err := os.ReadFile(filepath.Join(w.root, ".polyforge", "state", contractWIID+".json"))
	if err != nil {
		t.Fatalf("read the state file this claim was supposed to write: %v", err)
	}
	var state map[string]any
	if err := json.Unmarshal(b, &state); err != nil {
		t.Fatalf("parse the state file: %v", err)
	}
	obs.Secret, _ = state["session_secret"].(string)
	// The two volatile fields. claimed_at is a wall clock, session_secret is 32 random bytes;
	// both are asserted on separately rather than being compared here.
	//
	// ⚠️ PRESENCE IS CHECKED BEFORE THE DELETE, because `delete` on a missing key is a no-op:
	// without this, "both paths wrote claimed_at" and "one path omitted it entirely" compare
	// equal. session_secret has the same hole and it is closed by the non-empty / 64-char /
	// must-differ assertions at the end of the test; claimed_at had no such backstop.
	if _, ok := state["claimed_at"]; !ok {
		t.Errorf("the state file carries no claimed_at. It is deleted below as volatile, so its "+
			"ABSENCE would otherwise be invisible to the comparison: %v", sortedKeys(state))
	}
	delete(state, "session_secret")
	delete(state, "claimed_at")
	obs.State = normaliseAny(state, w.root).(map[string]any)

	obs.Worktrees = map[string]worktreeFacts{}
	entries, err := os.ReadDir(w.wtDir())
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("list %s: %v", w.wtDir(), err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(w.wtDir(), e.Name())
		facts := worktreeFacts{}
		facts.Branch, _ = contractGitQuiet(path, "rev-parse", "--abbrev-ref", "HEAD")
		// An empty upstream is the healthy answer after aihub#257, and `rev-parse @{u}` exits
		// non-zero when there is none — so the error is the DATA here, not a failure.
		facts.Upstream, _ = contractGitQuiet(path, "rev-parse", "--abbrev-ref", "@{u}")
		if gitPath, gerr := contractGitQuiet(path, "rev-parse", "--git-path", "info/exclude"); gerr == nil {
			p := gitPath
			if !filepath.IsAbs(p) {
				p = filepath.Join(path, p)
			}
			if content, rerr := os.ReadFile(p); rerr == nil {
				facts.Excludes = string(content)
			}
		}
		facts.Branch = strings.ReplaceAll(facts.Branch, w.root, "<ws>")
		facts.Upstream = strings.ReplaceAll(facts.Upstream, w.root, "<ws>")
		facts.Excludes = strings.ReplaceAll(facts.Excludes, w.root, "<ws>")
		obs.Worktrees[e.Name()] = facts
	}
	return obs
}

// normalisedPins strips the parts of the repo_pins body that are credentials rather than content.
func normalisedPins(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := map[string]any{}
	for k, v := range in {
		if k == "session_secret" {
			continue
		}
		out[k] = v
	}
	return out
}

func normalisedClaim(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := map[string]any{}
	for k, v := range in {
		if k != "session_info" {
			out[k] = v
			continue
		}
		si, _ := v.(map[string]any)
		stripped := map[string]any{}
		for sk, sv := range si {
			if sk == "session_secret" {
				continue
			}
			stripped[sk] = sv
		}
		out[k] = stripped
	}
	return out
}

// ─── the two paths ────────────────────────────────────────────────────────────

// claimViaMCP drives the real pf_claim_work_item tool over a real MCP session.
func claimViaMCP(t *testing.T, w *contractWorkspace, h *contractHub, idemKey string) claimObservation {
	t.Helper()
	ctx := context.Background()

	// nil config, deliberately: it is the FALLBACK, and passing one would let the MCP path read
	// a project definition the Go path cannot see. Both paths must resolve the workspace from
	// .polyforge.yaml on disk, which is what resolveWorkspaceConfig does first in either case.
	server := mcp.New(nil, client.New(h.srv.URL, "test-key"))
	cTransport, sTransport := sdkmcp.NewInMemoryTransports()
	serverCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		session, err := server.Connect(serverCtx, sTransport)
		if err != nil {
			return
		}
		_ = session.Wait()
	}()

	cl := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "lifecycle-contract", Version: "1.0.0"}, nil)
	session, err := cl.Connect(ctx, cTransport, nil)
	if err != nil {
		t.Fatalf("connect to the MCP server: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name: "pf_claim_work_item",
		Arguments: map[string]any{
			"work_item_id":    contractWIID,
			"idempotency_key": idemKey,
		},
	})
	if err != nil {
		t.Fatalf("call pf_claim_work_item: %v", err)
	}
	if res.IsError {
		t.Fatalf("pf_claim_work_item refused: %s", toolText(res))
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(toolText(res)), &payload); err != nil {
		t.Fatalf("parse the tool result %q: %v", toolText(res), err)
	}
	var problems []string
	if raw, ok := payload["worktree_problems"].([]any); ok {
		for _, p := range raw {
			problems = append(problems, fmt.Sprint(p))
		}
	}
	return observe(t, w, h, problems)
}

func toolText(res *sdkmcp.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	if text, ok := res.Content[0].(*sdkmcp.TextContent); ok {
		return text.Text
	}
	return ""
}

// claimViaGo drives drainClaimer.Claim — the seam RunDrain wires into drain.Runner.
func claimViaGo(t *testing.T, w *contractWorkspace, h *contractHub, idemKey string) claimObservation {
	t.Helper()

	// The Go path reports worktree problems on stderr rather than on a response, because there
	// is no model reading a response in A. Captured here so the two paths' problem lists can be
	// compared at all — if this ever finds nothing while the MCP path reports something, that is
	// the difference this gate exists to catch.
	restore := captureStderrLines(t)
	d := &drainClaimer{c: client.New(h.srv.URL, "test-key"), wsRoot: w.root, scenarioURL: "https://example.test/o/r"}
	info, blocker, err := d.Claim(context.Background(), contractWIID, idemKey)
	lines := restore()
	if err != nil {
		t.Fatalf("drainClaimer.Claim: %v", err)
	}
	if blocker != nil {
		t.Fatalf("a successful claim reported a blocker: %+v", blocker)
	}
	if info.AttemptID != "ra_CONTRACT667" || info.WIType != contractWIType {
		t.Fatalf("ClaimInfo lost server state: %+v", info)
	}
	// WorktreeRoot is asserted here rather than left unobserved: engine.CleanupWorktrees
	// removes this directory tree at wrap, so a wrong value is a delete of the wrong path.
	if want := w.wtDir(); info.WorktreeRoot != want {
		t.Errorf("ClaimInfo.WorktreeRoot = %q, want %q — this is the directory wrap-time cleanup "+
			"removes", info.WorktreeRoot, want)
	}
	if info.ScenarioURL == "" {
		t.Error("ClaimInfo carries no ScenarioURL, so `engine startup` has no step graph to resolve")
	}
	return observe(t, w, h, drainProblemsFrom(lines))
}

// drainProblemsFrom keeps the stderr lines drainClaimer.Claim writes for worktree problems, with
// its own prefix removed so the text can be compared with the MCP response's entries verbatim.
//
// ⚠️ It REJOINS continuation lines. A problem string interpolates git's stderr, which may be
// several lines; a filter that kept only prefixed lines would hand the Go path the first line and
// the MCP path the whole string, and the comparison would go red for a reason that is about this
// helper rather than about the code. Current fixtures produce one line each, so this is prevention
// — but a one-line-only reader is exactly the shape that makes a future multi-line git error look
// like a divergence.
func drainProblemsFrom(lines []string) []string {
	var out []string
	prefix := "drain: " + contractWIID + ": "
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, prefix):
			out = append(out, strings.TrimPrefix(l, prefix))
		case len(out) > 0 && !strings.HasPrefix(l, "drain: ") && !strings.HasPrefix(l, "polyforge: "):
			out[len(out)-1] += "\n" + l
		}
	}
	return out
}

// captureStderrLines redirects os.Stderr for the duration of one call and hands back the
// restore-and-collect function.
//
// The draining goroutine starts before anything can write, so the 64 KiB pipe buffer cannot
// deadlock. ⚠️ The restore is NOT deferred by this helper: if the call under test panics,
// os.Stderr stays swapped for the rest of the binary. That is acceptable here because a panic
// fails the run anyway, and deferring it would collect the lines before the caller can read them.
func captureStderrLines(t *testing.T) func() []string {
	t.Helper()
	r, wr, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = wr
	done := make(chan []string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		var lines []string
		for _, l := range strings.Split(string(b), "\n") {
			if l != "" {
				lines = append(lines, l)
			}
		}
		done <- lines
	}()
	return func() []string {
		os.Stderr = orig
		_ = wr.Close()
		lines := <-done
		_ = r.Close()
		return lines
	}
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ─── the gate ─────────────────────────────────────────────────────────────────

// contractFixture prepares the pre-existing state one scenario starts from.
type contractFixture struct {
	name string
	// prepare runs against a fresh workspace before the claim.
	prepare func(t *testing.T, w *contractWorkspace)
	// check runs against BOTH observations after they have been compared, and is where the
	// per-scenario invariant lives — the comparison alone is satisfied by two paths that are
	// identically wrong.
	check func(t *testing.T, path string, w *contractWorkspace, obs claimObservation)
}

func contractFixtures() []contractFixture {
	return []contractFixture{
		{
			name:    "fresh claim creates the worktree and the branch",
			prepare: func(*testing.T, *contractWorkspace) {},
			check: func(t *testing.T, path string, w *contractWorkspace, obs claimObservation) {
				facts, ok := obs.Worktrees[contractRepo]
				if !ok {
					t.Fatalf("%s: no worktree was created; got %v", path, obs.Worktrees)
				}
				if facts.Branch != contractBranch {
					t.Errorf("%s: worktree is on %q, want %q", path, facts.Branch, contractBranch)
				}
				// aihub#257: the created branch must not be configured to push to main.
				if facts.Upstream != "" {
					t.Errorf("%s: the new task branch tracks %q. With push.default=upstream a bare "+
						"`git push` in this worktree fast-forwards that branch (aihub#257)",
						path, facts.Upstream)
				}
				if !strings.Contains(facts.Excludes, ".pf_meta.json") {
					t.Errorf("%s: .git/info/exclude was not seeded: %q", path, facts.Excludes)
				}
				if len(obs.Problems) != 0 {
					t.Errorf("%s: a healthy claim reported problems: %v", path, obs.Problems)
				}
			},
		},
		{
			name: "an existing worktree tracking main is adopted AND repaired",
			prepare: func(t *testing.T, w *contractWorkspace) {
				// Reproduce the pre-aihub#257 state exactly: `worktree add -b` WITHOUT
				// --no-track, which is what created the 199 hazardous worktrees measured on
				// 2026-09-03. The claim must adopt this directory (it is healthy) and clear
				// the upstream, and that repair is reachable ONLY from the reuse branch —
				// addClaimWorktree is never called when the directory exists.
				contractGit(t, w.src, "worktree", "add", "-q", "-b", contractBranch, w.wtPath(), "origin/main")
				if up := contractGit(t, w.wtPath(), "rev-parse", "--abbrev-ref", "@{u}"); up != "origin/main" {
					t.Fatalf("the fixture did not reproduce the hazard: upstream is %q, want origin/main. "+
						"Without it this scenario tests a repair against nothing to repair", up)
				}
			},
			check: func(t *testing.T, path string, w *contractWorkspace, obs claimObservation) {
				facts, ok := obs.Worktrees[contractRepo]
				if !ok {
					t.Fatalf("%s: the healthy existing worktree was not adopted; got %v", path, obs.Worktrees)
				}
				if facts.Branch != contractBranch {
					t.Errorf("%s: adopted worktree is on %q, want %q", path, facts.Branch, contractBranch)
				}
				if facts.Upstream != "" {
					t.Errorf("%s: the reused worktree still tracks %q — repairReusedWorktreeUpstream "+
						"(aihub#257, landing site iii) did not run on this path", path, facts.Upstream)
				}
				if len(obs.Problems) != 0 {
					t.Errorf("%s: adopting a healthy worktree reported problems: %v", path, obs.Problems)
				}
			},
		},
		{
			name: "a half-built directory is refused, not adopted",
			prepare: func(t *testing.T, w *contractWorkspace) {
				// aihub#328's shape one: `git worktree add` wrote the .git file and then died,
				// so the pointer names an admin directory that was never created.
				if err := os.MkdirAll(w.wtPath(), 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				pointer := "gitdir: " + filepath.Join(w.src, ".git", "worktrees", "never-created") + "\n"
				if err := os.WriteFile(filepath.Join(w.wtPath(), ".git"), []byte(pointer), 0o644); err != nil {
					t.Fatalf("write the dangling .git pointer: %v", err)
				}
				if err := os.WriteFile(filepath.Join(w.wtPath(), "half.txt"), []byte("partial"), 0o644); err != nil {
					t.Fatalf("write the half-checked-out file: %v", err)
				}
			},
			check: func(t *testing.T, path string, w *contractWorkspace, obs claimObservation) {
				// THE ASSERTION IS ON THE RECORDED WORKTREE MAP, not on a return value:
				// adoption's damage is that the bad path is written to the state file and every
				// later claim then takes the same early return (aihub#328).
				wts, _ := obs.State["worktrees"].(map[string]any)
				if _, recorded := wts[contractRepo]; recorded {
					t.Errorf("%s: the state file RECORDS %s => %v for a directory that is not a git "+
						"worktree. That record is what makes the adoption permanent (aihub#328)",
						path, contractRepo, wts[contractRepo])
				}
				if len(obs.Problems) == 0 {
					t.Errorf("%s: the rejected directory was never reported to the caller. A silent "+
						"skip is the same blindness as a silent adoption (aihub#328)", path)
				}
				for _, p := range obs.Problems {
					if !strings.Contains(p, "not a usable git worktree") {
						t.Errorf("%s: problem text does not say what is wrong: %q", path, p)
					}
					// The remediation order is load-bearing and was wrong in the first draft:
					// prune-then-rm is a no-op followed by a broken registration.
					ri, pi := strings.Index(p, "rm -rf"), strings.Index(p, "worktree prune")
					if ri < 0 || pi < 0 || ri > pi {
						t.Errorf("%s: the remediation must be `rm -rf` BEFORE `worktree prune`; "+
							"prune first is a no-op and leaves the next `worktree add` exiting 128. got %q", path, p)
					}
				}
				// The claim itself still succeeded: it is a warning, not an error.
				if ok, _ := obs.State["claimed"].(bool); !ok {
					t.Errorf("%s: a rejected directory must not fail the claim", path)
				}
			},
		},
	}
}

func TestClaimLifecycleIsOneImplementationReachedTwoWays(t *testing.T) {
	fixtures := contractFixtures()
	if len(fixtures) < 3 {
		t.Fatalf("only %d fixtures — the three branches of the worktree loop (create, adopt, "+
			"refuse) must each be driven, or agreement here is agreement about one branch", len(fixtures))
	}

	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			// ONE key for both, because "same input" is the premise of the comparison. Safe
			// across the two runs: recordedClaimSecret reads the state file out of
			// POLYFORGE_WORKSPACE_ROOT, and the two runs have different roots, so neither
			// can see the other's record and take the replay branch.
			const idemKey = "idem-contract-667"
			mcpObs := runOnePath(t, fx, claimViaMCP, idemKey)
			goObs := runOnePath(t, fx, claimViaGo, idemKey)

			// 1-2. The two requests that reach aihub.
			if !reflect.DeepEqual(normalisedClaim(mcpObs.ClaimBody), normalisedClaim(goObs.ClaimBody)) {
				t.Errorf("the two paths sent DIFFERENT claim bodies:\n  mcp: %s\n  go:  %s",
					mustJSON(normalisedClaim(mcpObs.ClaimBody)), mustJSON(normalisedClaim(goObs.ClaimBody)))
			}
			if !reflect.DeepEqual(normalisedPins(mcpObs.PinsBody), normalisedPins(goObs.PinsBody)) {
				t.Errorf("the two paths recorded DIFFERENT repo pins:\n  mcp: %s\n  go:  %s",
					mustJSON(normalisedPins(mcpObs.PinsBody)), mustJSON(normalisedPins(goObs.PinsBody)))
			}
			// 3. The credential file every later pf_* call authenticates with.
			if !reflect.DeepEqual(mcpObs.State, goObs.State) {
				t.Errorf("the two paths wrote DIFFERENT state files:\n  mcp: %s\n  go:  %s",
					mustJSON(mcpObs.State), mustJSON(goObs.State))
			}
			// 4. The disk.
			if !reflect.DeepEqual(mcpObs.Worktrees, goObs.Worktrees) {
				t.Errorf("the two paths left DIFFERENT worktrees:\n  mcp: %+v\n  go:  %+v",
					mcpObs.Worktrees, goObs.Worktrees)
			}
			// 5. What the caller is told.
			if !reflect.DeepEqual(sortedCopy(mcpObs.Problems), sortedCopy(goObs.Problems)) {
				t.Errorf("the two paths reported DIFFERENT worktree problems:\n  mcp: %v\n  go:  %v",
					mcpObs.Problems, goObs.Problems)
			}

			// ── The one thing that must NOT match. Both secrets are 32 random bytes; equal ones
			//    would mean something is deriving a credential rather than minting it, and the
			//    comparison above deletes the field, so nothing else here would notice.
			if mcpObs.Secret == "" || goObs.Secret == "" {
				t.Fatalf("a claim wrote no session_secret (mcp=%q go=%q) — every later "+
					"credential-checked call authenticates with it", mcpObs.Secret, goObs.Secret)
			}
			if len(mcpObs.Secret) != 64 || len(goObs.Secret) != 64 {
				t.Errorf("session_secret is not 64 hex chars: mcp=%d go=%d", len(mcpObs.Secret), len(goObs.Secret))
			}
			if mcpObs.Secret == goObs.Secret {
				t.Errorf("both paths minted the SAME session_secret (%q). Two independent claims "+
					"cannot draw the same 32 random bytes; a shared value means it is derived", mcpObs.Secret)
			}
		})
	}
}

// runOnePath builds a fresh workspace, applies the fixture, runs one claim, and observes.
func runOnePath(t *testing.T, fx contractFixture, run func(*testing.T, *contractWorkspace, *contractHub, string) claimObservation, idemKey string) claimObservation {
	t.Helper()
	w := newContractWorkspace(t)
	// Set AFTER the workspace exists, and asserted, because lifecycle.Claim reads this env var
	// to find the state directory: without the redirection config.StateDir() walks up to the
	// LIVE workspace, whose state directory holds every claimed work item's credentials.
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", w.root)
	if dir := config.StateDir(); !strings.HasPrefix(dir, w.root+string(os.PathSeparator)) {
		t.Fatalf("config.StateDir() = %q, outside this test's temp root %q — refusing to run: "+
			"this test writes state files", dir, w.root)
	}
	h := newContractHub(t)
	fx.prepare(t, w)
	obs := run(t, w, h, idemKey)
	fx.check(t, pathName(run), w, obs)
	return obs
}

func pathName(run func(*testing.T, *contractWorkspace, *contractHub, string) claimObservation) string {
	// Compared by behaviour rather than by name: there are exactly two, and the caller passes
	// them literally.
	if reflect.ValueOf(run).Pointer() == reflect.ValueOf(claimViaMCP).Pointer() {
		return "mcp"
	}
	return "go"
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%+v", v)
	}
	return string(b)
}

// normaliseAny rewrites every absolute path under root to "<ws>", recursively.
func normaliseAny(v any, root string) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = normaliseAny(vv, root)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = normaliseAny(vv, root)
		}
		return out
	case string:
		return strings.ReplaceAll(t, root, "<ws>")
	default:
		return v
	}
}

func normaliseStrings(in []string, root string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, strings.ReplaceAll(s, root, "<ws>"))
	}
	return out
}
