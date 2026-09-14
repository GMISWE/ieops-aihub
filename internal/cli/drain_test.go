package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/drain"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// TestParseChannels_ModelKeepsItsSlashes is the assertion this function exists for.
//
// pi and opencode both identify a model as `<provider>/<model>`, and aihub#640's
// design_notes_addendum measured on this machine what happens when that identifier is not exact:
// the bare id `claude-fable-5` is rejected as "ambiguous across providers", a glob is not
// accepted at all, and a FUZZY match silently routed to amazon-bedrock — a different vendor
// entirely. So mangling a provider-qualified model is not a cosmetic bug; it is how a step
// silently runs on the wrong company's model.
//
// Splitting on every "/" would turn `pi/anthropic/claude-opus-4-5` into harness=pi,
// model=anthropic — a provider name where a model belongs.
//
// Mutant watched: switching IndexByte to LastIndexByte, or to strings.Split(part, "/"), turns
// the three-segment case red.
func TestParseChannels_ModelKeepsItsSlashes(t *testing.T) {
	got, err := parseChannels("claude,codex/gpt-5.6,pi/anthropic/claude-opus-4-5,opencode")
	if err != nil {
		t.Fatalf("parseChannels: %v", err)
	}
	want := []drain.Channel{
		{Harness: drain.HarnessClaude},
		{Harness: drain.HarnessCodex, Model: "gpt-5.6"},
		{Harness: drain.HarnessPi, Model: "anthropic/claude-opus-4-5"},
		{Harness: drain.HarnessOpenCode},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseChannels =\n  %+v\nwant\n  %+v", got, want)
	}
}

// TestParseChannels_RejectsAnUnknownHarness is the negative control. Accepting a typo would
// build an invocation for a harness that does not exist, and the failure would surface only at
// the first dispatch — after work items had been claimed.
func TestParseChannels_RejectsAnUnknownHarness(t *testing.T) {
	for _, bad := range []string{"cursor", "claude,cursor", "", "   ", ","} {
		if _, err := parseChannels(bad); err == nil {
			t.Errorf("parseChannels(%q) succeeded; it should refuse", bad)
		}
	}
	// Whitespace around a real name is fine.
	if _, err := parseChannels(" claude , codex "); err != nil {
		t.Errorf("parseChannels rejected padded but valid input: %v", err)
	}
}

// TestParseDrainArgs_RefusesUnknownFlags is deliberately strict, and the reason is the budgets.
// A scheduler that claims and executes real work items must not treat `--max-workitems=3` (a
// plausible typo for --max-work-items) as "no cap": the flag silently vanishes and the run goes
// unbounded, which is the exact opposite of what the operator asked for.
func TestParseDrainArgs_RefusesUnknownFlags(t *testing.T) {
	for _, bad := range []string{"--max-workitems=3", "--maxparallel=2", "--proj=aihub", "-x"} {
		if _, err := parseDrainArgs([]string{"--project=aihub", bad}); err == nil {
			t.Errorf("parseDrainArgs accepted unknown flag %q; a mistyped budget would silently "+
				"become unlimited", bad)
		}
	}
}

// TestParseDrainArgs_BudgetsMustBePositive covers the other half of the same hazard: a
// non-numeric or non-positive budget must be refused, not coerced. Budget.Parallelism() reads 0
// as "use the default" and RemainingWorkItems reads 0 as "unbounded", so `--max-work-items=0`
// silently accepted would mean "no limit" to a reader who typed it meaning "none".
func TestParseDrainArgs_BudgetsMustBePositive(t *testing.T) {
	for _, bad := range []string{
		"--max-parallel=0", "--max-parallel=-1", "--max-parallel=lots",
		"--max-rounds=0", "--max-work-items=0", "--max-work-items=",
	} {
		if _, err := parseDrainArgs([]string{"--project=aihub", bad}); err == nil {
			t.Errorf("parseDrainArgs accepted %q", bad)
		}
	}
	o, err := parseDrainArgs([]string{"--project=aihub", "--max-parallel=4", "--max-rounds=2", "--max-work-items=9"})
	if err != nil {
		t.Fatalf("parseDrainArgs on valid budgets: %v", err)
	}
	if o.Budget != (drain.Budget{MaxParallel: 4, MaxRounds: 2, MaxWorkItems: 9}) {
		t.Fatalf("budget = %+v", o.Budget)
	}
}

// TestParseDrainArgs_DefaultsAreTheSafeOnes pins the two defaults the design argues for: scoped
// to me rather than --all ("`--all` 保留但不默认"), and the full harness candidate list in
// preference order.
func TestParseDrainArgs_DefaultsAreTheSafeOnes(t *testing.T) {
	o, err := parseDrainArgs([]string{"--project=aihub"})
	if err != nil {
		t.Fatalf("parseDrainArgs: %v", err)
	}
	if o.All {
		t.Error("--all defaulted to true; two people draining one project would only trade lock races")
	}
	if o.Plan {
		t.Error("--plan defaulted to true")
	}
	if len(o.Channels) != len(drain.KnownHarnesses) {
		t.Fatalf("default channels = %+v, want all %d known harnesses", o.Channels, len(drain.KnownHarnesses))
	}
	if o.Channels[0].Harness != drain.KnownHarnesses[0] {
		t.Errorf("default channel order does not start with %s", drain.KnownHarnesses[0])
	}
	for _, c := range o.Channels {
		if c.Model != "" {
			t.Errorf("default channel %s carries model %q; the model must come from the agent "+
				"file, never from a default here (aihub#555)", c.Harness, c.Model)
		}
	}
	// --dry-run is accepted as a synonym for --plan.
	if o, err := parseDrainArgs([]string{"--project=aihub", "--dry-run"}); err != nil || !o.Plan {
		t.Errorf("--dry-run did not set Plan (err=%v)", err)
	}
}

// TestClassifyHubError_MapsTheTwoControlFlowCodes pins the translation from aihub's error text
// to the sentinels the runner branches on. Both are ordinary control flow rather than failures,
// so misclassifying either turns a healthy run into a FAILED one — or worse, in the paused case,
// makes the loop keep dispatching into an attempt somebody deliberately stopped.
func TestClassifyHubError_MapsTheTwoControlFlowCodes(t *testing.T) {
	if got := classifyHubError(nil); got != nil {
		t.Fatalf("classifyHubError(nil) = %v", got)
	}
	for _, s := range []string{
		"409 CONFLICT_LOCK_TAKEN: internal/x.go held by ra_other",
		"conflict_lock_taken",
	} {
		if err := classifyHubError(errString(s)); !isSentinel(err, drain.ErrLockTaken) {
			t.Errorf("%q did not map to ErrLockTaken", s)
		}
	}
	for _, s := range []string{
		"409 ATTEMPT_PAUSED",
		"the attempt is paused",
	} {
		if err := classifyHubError(errString(s)); !isSentinel(err, drain.ErrAttemptPaused) {
			t.Errorf("%q did not map to ErrAttemptPaused", s)
		}
	}
	// Negative control: an unrelated failure must stay unclassified, or every server hiccup
	// would be read as a lock race and silently skipped.
	for _, s := range []string{"500 internal server error", "connection refused", "403 FORBIDDEN"} {
		err := classifyHubError(errString(s))
		if isSentinel(err, drain.ErrLockTaken) || isSentinel(err, drain.ErrAttemptPaused) {
			t.Errorf("%q was mapped to a control-flow sentinel", s)
		}
	}
}

// TestUnionCandidates_DedupesTheTwoHalvesOfOwnership pins the scope union: "mine" is
// reporter==me OR current-attempt-claimant==me, and a work item satisfying both must appear once.
// Counting it twice would inflate the proliferation baseline and the queue totals.
func TestUnionCandidates_DedupesTheTwoHalvesOfOwnership(t *testing.T) {
	a := []drain.Candidate{{ID: "1"}, {ID: "2"}}
	b := []drain.Candidate{{ID: "2"}, {ID: "3"}, {ID: ""}}
	got := unionCandidates(a, b)
	if len(got) != 3 {
		t.Fatalf("union = %+v, want 3 distinct work items", got)
	}
	seen := map[string]int{}
	for _, c := range got {
		seen[c.ID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("work item %s appears %d times", id, n)
		}
	}
	if seen[""] != 0 {
		t.Error("an entry with an empty id survived the union")
	}
}

// TestDependencyIDs_ReadsEveryShapeTheEndpointUses guards ObserveQueue's external-block
// detection. If this returns nothing, every blocked work item looks blocked-by-mine, and
// BLOCKED_EXTERNAL — the state that notifies a human — never fires.
func TestDependencyIDs_ReadsEveryShapeTheEndpointUses(t *testing.T) {
	res := map[string]any{
		"blocked_by": []any{
			map[string]any{"blocking_wi_id": "wi_a"},
			map[string]any{"id": "wi_b"},
			"wi_c",
		},
	}
	got := dependencyIDs(res)
	for _, want := range []string{"wi_a", "wi_b", "wi_c"} {
		if !containsStr(got, want) {
			t.Errorf("dependencyIDs = %v, missing %s", got, want)
		}
	}
	if n := len(dependencyIDs(map[string]any{"unrelated": 1})); n != 0 {
		t.Errorf("dependencyIDs on an unrelated payload returned %d ids", n)
	}
}

// TestScopeLabel covers the string watch and --plan print, which is how a reader tells a
// COMPLETED that means "my queue is empty" from one that means "the project is empty".
func TestScopeLabel(t *testing.T) {
	if scopeLabel(true) != "--all" || scopeLabel(false) != "mine" {
		t.Fatalf("scopeLabel: %q / %q", scopeLabel(true), scopeLabel(false))
	}
}

// TestExecutable_AsksTheServerForItsOwnReadyPredicate pins the decision NOT to re-derive
// dependency readiness client-side.
//
// `ready_only=true` is the server's readyOnlyPredicate — one SQL constant that also backs
// pf_get_ready_queue's items[] — so "queued, requires_human_session=false, no unfinished blocking
// dependency" is evaluated in exactly one place. Dropping the parameter does not fail loudly: the
// endpoint happily returns every queued work item, drain claims ones the server considers
// blocked, and the dependency graph the whole feature is built on stops being honoured. Nothing
// else in this package would notice.
//
// Mutant watched: deleting the ready_only line turns this red.
func TestExecutable_AsksTheServerForItsOwnReadyPredicate(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":"wi_1","slug":"p#1","goal":"g","priority":"high","wi_type":"feature","created_at":"2026-01-01T00:00:00Z"}]}`))
	}))
	defer srv.Close()

	q := &drainQueries{c: client.New(srv.URL, "k"), project: "p", scope: drain.Scope{UserID: "u_me"}}
	cands, err := q.Executable(context.Background())
	if err != nil {
		t.Fatalf("Executable: %v", err)
	}

	if got.Get("ready_only") != "true" {
		t.Errorf("ready_only=%q, want true: without it drain claims work items the server "+
			"considers blocked", got.Get("ready_only"))
	}
	if got.Get("project") != "p" {
		t.Errorf("project=%q", got.Get("project"))
	}
	if got.Get("user_id") != "u_me" {
		t.Errorf("user_id=%q, want the reporter-half ownership filter", got.Get("user_id"))
	}
	// claimed_by matches only a CURRENT attempt, and an executable work item is queued and so
	// has none. Sending it here would filter every candidate away and drain would do nothing.
	if got.Get("claimed_by") != "" {
		t.Errorf("claimed_by=%q was sent on the executable query; a queued work item has no "+
			"current attempt, so this matches nothing and the queue would always look empty",
			got.Get("claimed_by"))
	}

	if len(cands) != 1 || cands[0].ID != "wi_1" || cands[0].Priority != "high" {
		t.Fatalf("candidates = %+v", cands)
	}

	// --all must drop the ownership filter entirely, not send it empty.
	q.scope = drain.Scope{All: true, UserID: "u_me"}
	if _, err := q.Executable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, present := got["user_id"]; present {
		t.Errorf("--all still sent user_id=%q", got.Get("user_id"))
	}
}

// TestListByStatus_UnionsReporterAndClaimant pins the other half of the ownership definition.
// aihub#652 shipped `claimed_by` (derived from run_attempts.actor_user_id) instead of the
// assignee column the design sketched, so "mine" is the union of two server-side filters. Asking
// for only one half loses work items: reporter-only misses ones I claimed from someone else's
// queue, claimant-only misses every queued work item I filed.
func TestListByStatus_UnionsReporterAndClaimant(t *testing.T) {
	var queries []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		// Both halves return the same work item, so the union must dedupe it.
		_, _ = w.Write([]byte(`{"items":[{"id":"wi_same","slug":"p#1"}]}`))
	}))
	defer srv.Close()

	q := &drainQueries{c: client.New(srv.URL, "k"), project: "p", scope: drain.Scope{UserID: "u_me"}}
	out, err := q.listByStatus(context.Background(), "queued,blocked")
	if err != nil {
		t.Fatalf("listByStatus: %v", err)
	}

	if len(queries) != 2 {
		t.Fatalf("made %d requests, want 2 (one per ownership half)", len(queries))
	}
	var sawReporter, sawClaimant bool
	for _, qv := range queries {
		if qv.Get("status") != "queued,blocked" {
			t.Errorf("status=%q", qv.Get("status"))
		}
		if qv.Get("user_id") == "u_me" {
			sawReporter = true
		}
		if qv.Get("claimed_by") == "u_me" {
			sawClaimant = true
		}
	}
	if !sawReporter {
		t.Error("no request carried user_id: work items I filed but never claimed would be invisible")
	}
	if !sawClaimant {
		t.Error("no request carried claimed_by: work items I claimed but did not file would be invisible")
	}
	if len(out) != 1 {
		t.Fatalf("union returned %d work items for one work item matched by both halves: %+v", len(out), out)
	}
}

// TestObserveQueue_AnUnanswerableDependencyLookupCountsAsExternal pins the failure branch of the
// IDLE / BLOCKED_EXTERNAL decision, which is the one place in this package where a lookup error
// has to be turned into a verdict rather than propagated.
//
// The asymmetry is the point. Guessing "blocked by mine" on an unanswerable lookup produces IDLE
// — the terminal state that deliberately notifies NOBODY — so a transient dependency-endpoint
// failure would silently convert "a human must act" into "come back later", and the run would
// exit 10 looking healthy. Guessing "external" costs at most one unnecessary notification. When
// one error is silent and the other is noisy, the noisy one is the safe default.
//
// Mutant watched: neutralising the `if derr != nil` branch turns this red.
func TestObserveQueue_AnUnanswerableDependencyLookupCountsAsExternal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/dependencies") {
			// The exact situation this branch exists for: the server cannot tell us who is
			// blocking this work item.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
			return
		}
		switch r.URL.Query().Get("status") {
		case "blocked":
			_, _ = w.Write([]byte(`{"items":[{"id":"wi_blocked","slug":"p#9"}]}`))
		case "queued,running,blocked,paused":
			_, _ = w.Write([]byte(`{"items":[{"id":"wi_blocked","slug":"p#9"}]}`))
		default:
			_, _ = w.Write([]byte(`{"items":[]}`))
		}
	}))
	defer srv.Close()

	q := &drainQueries{c: client.New(srv.URL, "k"), project: "p", scope: drain.Scope{UserID: "u_me"}}
	st, err := q.ObserveQueue(context.Background())
	if err != nil {
		t.Fatalf("ObserveQueue: %v", err)
	}

	if st.BlockedByOthers() != 1 {
		t.Fatalf("BlockedByOthers = %d, want 1: an unanswerable dependency lookup must not be "+
			"read as the reassuring answer (state = %+v)", st.BlockedByOthers(), st)
	}
	if st.BlockedByMine != 0 {
		t.Errorf("BlockedByMine = %d, want 0", st.BlockedByMine)
	}
	if got := drain.Classify(false, st); got != drain.TerminalBlockedExternal {
		t.Errorf("the run would end %s, want BLOCKED_EXTERNAL — IDLE tells nobody", got)
	}
}

// TestObserveQueue_ABlockerInsideTheScopeIsIdleNotExternal is the negative control for the test
// above: when the dependency IS answerable and names one of my own work items, waiting genuinely
// helps and nobody should be bothered.
func TestObserveQueue_ABlockerInsideTheScopeIsIdleNotExternal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/dependencies") {
			_, _ = w.Write([]byte(`{"blocked_by":[{"blocking_wi_id":"wi_mine"}]}`))
			return
		}
		switch r.URL.Query().Get("status") {
		case "blocked":
			_, _ = w.Write([]byte(`{"items":[{"id":"wi_blocked","slug":"p#9"}]}`))
		case "queued,running,blocked,paused":
			// wi_mine is in scope, so the block is mine to clear.
			_, _ = w.Write([]byte(`{"items":[{"id":"wi_blocked","slug":"p#9"},{"id":"wi_mine","slug":"p#8"}]}`))
		default:
			_, _ = w.Write([]byte(`{"items":[]}`))
		}
	}))
	defer srv.Close()

	q := &drainQueries{c: client.New(srv.URL, "k"), project: "p", scope: drain.Scope{UserID: "u_me"}}
	st, err := q.ObserveQueue(context.Background())
	if err != nil {
		t.Fatalf("ObserveQueue: %v", err)
	}
	if st.BlockedByMine != 1 || st.BlockedByOthers() != 0 {
		t.Fatalf("state = %+v, want exactly one blocked-by-mine", st)
	}
	if got := drain.Classify(false, st); got != drain.TerminalIdle {
		t.Errorf("the run would end %s, want IDLE", got)
	}
}

// TestPreflightChannels_RefusesWhenNoCandidateIsUsable is ops problem 2's startup gate.
//
// Returning an empty channel list WITHOUT an error is the dangerous shape: drain would go on to
// claim work items and hand every step to a channel that does not exist, producing a run that
// claims real work, fails every step, and reports FAILED for a reason that has nothing to do
// with the work. Stopping before the first claim is the whole point of pre-flighting.
func TestPreflightChannels_RefusesWhenNoCandidateIsUsable(t *testing.T) {
	// An unknown harness cannot even build an invocation, so it is rejected without running
	// anything — no network, no credentials, no subprocess.
	healthy, rejected, err := preflightChannels(context.Background(),
		[]drain.Channel{{Harness: "nope"}, {Harness: "alsonope"}}, t.TempDir())

	if err == nil {
		t.Fatal("preflight accepted a candidate list with nothing usable in it; drain would " +
			"claim work items and then have nowhere to run them")
	}
	if len(healthy) != 0 {
		t.Errorf("healthy = %+v, want none", healthy)
	}
	if len(rejected) != 2 {
		t.Fatalf("rejected %d candidates, want both", len(rejected))
	}
	for _, v := range rejected {
		if v.OK {
			t.Errorf("%s was marked OK", v.Channel)
		}
		if v.Reason == "" {
			t.Errorf("%s was rejected with no reason; an operator cannot act on that", v.Channel)
		}
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

type errString string

func (e errString) Error() string { return string(e) }

func isSentinel(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func containsStr(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

// TestDrainOwnsNoLifecycleLogicOfItsOwn is what replaced aihub#640's refusal test, and it is the
// workflow_identity_constraint expressed as something a build can check.
//
// ─── what it used to be ──────────────────────────────────────────────────────
// Until aihub#667 this file held TestClaimForDrain_ReportsAStructuralGapNotATransientOne, which
// asserted that claimForDrain returned a drain.ErrNotSupported refusal so a run that could not
// claim ended FAILED (exit 12, "a human has to act") rather than IDLE (exit 10, "come back
// later"). That was correct while the capability was missing. The capability now exists, so the
// refusal is gone and asserting it would be asserting a bug.
//
// ─── what is worth asserting instead ─────────────────────────────────────────
// The hazard the refusal was avoiding has not gone away: it is that A grows its own copy of the
// lifecycle. aihub#640's `workflow_identity_constraint` is that A must contain no execution logic
// B/C lacks, and the concrete failure mode is a SECOND implementation of worktree adoption drifting
// from the first — the one that needed aihub#328's and aihub#257's incident fixes to get right.
//
// So: the scheduler may call internal/lifecycle, and may not spell any of its details itself. The
// census is over STRING LITERALS ONLY, via the AST, which matters — the seam's own doc comment
// names "--no-track", ".git/info/exclude" and "session_secret" while explaining why they are not
// here, and a grep would have flagged the explanation as the violation. Every token below is one a
// re-implementation cannot avoid writing down.
func TestDrainOwnsNoLifecycleLogicOfItsOwn(t *testing.T) {
	// Each entry is a literal fragment that only a second implementation of the lifecycle would
	// need. They are argv words and wire keys, not prose: "--no-track" cannot appear in code for
	// any other reason.
	forbidden := map[string]string{
		"--no-track":       "aihub#257's create-path prevention — the branch creation belongs to internal/lifecycle.AddClaimWorktree",
		"--unset-upstream": "aihub#257's repair — coding.GitClearProtectedUpstream is reached through internal/lifecycle, not from here",
		"--show-toplevel":  "aihub#328's adoption check — verifyClaimWorktree is the only place that decides whether a directory is a worktree",
		"info/exclude":     "the per-worktree exclude seeding — writeWorktreeExcludes owns it",
		"polyforge/":       "the task-branch prefix — branch naming is newClaimBranchNames' and nothing else's",
	}

	// ⚠️ "session_secret" WAS on that list and was REMOVED, after this gate caught its own
	// author. The scheduler legitimately spells it: attemptCredentials reads the three
	// credential fields out of the state file the claim wrote and puts them on the wire, which
	// is what every pf_* caller does (internal/mcp/tools_step.go spells it too). READING a
	// credential is not MINTING one, and the forbidden-token list has to name the second.
	//
	// The minting primitive is what this replaces it with: internal/lifecycle/secret.go is the
	// only place allowed to draw 32 random bytes. An import check is the right shape for that —
	// there is no way to mint a secret without one of these, and no other reason for either to
	// appear in a scheduler.
	forbiddenImports := map[string]string{
		"crypto/rand": "minting a session_secret is internal/lifecycle.GenerateSessionSecret's, and only its, job",
	}

	files := []string{"drain.go"}
	entries, err := os.ReadDir(filepath.Join("..", "drain"))
	if err != nil {
		t.Fatalf("list internal/drain: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			files = append(files, filepath.Join("..", "drain", e.Name()))
		}
	}
	if len(files) < 4 {
		t.Fatalf("only %d files to scan (%v) — internal/drain has several production files, so this "+
			"census would pass by having read almost nothing", len(files), files)
	}

	literals := 0
	perFile := map[string]int{}
	for _, path := range files {
		lits, imports, perr := stringLiteralsIn(path)
		if perr != nil {
			t.Fatalf("%v", perr)
		}
		literals += len(lits)
		perFile[path] = len(lits)
		// The BUGGY spellings, not just the fixed ones. A re-implementation that reintroduces
		// aihub#257 writes `worktree add -b` with no --no-track, and one that reintroduces
		// aihub#328 writes `rev-parse --git-dir` — neither contains a forbidden token above,
		// because every token above is part of the CORRECT version. What both must contain is
		// a git command, and a scheduler has no business constructing one: the two places that
		// legitimately shell to git from internal/cli are execGitRunner (engine.go) and the
		// engine seams that call it, none of them in these files.
		for _, line := range gitCommandLinesIn(path) {
			t.Errorf("%s:%d builds a git command directly. Every git invocation on the claim "+
				"path belongs to internal/lifecycle, and every one on the STEP path to "+
				"internal/engine via execGitRunner. A git command spelled here is how "+
				"aihub#257 (`worktree add -b` with no --no-track) and aihub#328 "+
				"(`rev-parse --git-dir`) come back, and neither of those spellings contains a "+
				"forbidden literal — they are the DEFECTIVE forms, not the fixed ones.", path, line)
		}
		for _, imp := range imports {
			if why, bad := forbiddenImports[imp]; bad {
				t.Errorf("%s imports %q. %s\nA scheduler that can mint a credential has become "+
					"the second implementation of the work-item lifecycle (aihub#640's "+
					"workflow_identity_constraint, aihub#667).", path, imp, why)
			}
		}
		for _, lit := range lits {
			for token, why := range forbidden {
				if strings.Contains(lit.text, token) {
					t.Errorf("%s:%d has the string literal %q, which contains %q.\n%s\n"+
						"A scheduler that spells this has become the second implementation of the work-item "+
						"lifecycle, which is what aihub#640's workflow_identity_constraint forbids and what "+
						"aihub#667 existed to stop. Call internal/lifecycle instead.",
						path, lit.line, lit.text, token, why)
				}
			}
		}
	}

	// ── Anti-vacuity, both halves. A walk that collected no literals, or a detector that cannot
	//    fire, passes exactly like a clean tree.
	if literals < 50 {
		t.Fatalf("the walk collected %d string literals across %d files — that is too few for these "+
			"files and means the AST walk, not the code, is what is clean", literals, len(files))
	}
	planted := []string{
		`git worktree add --no-track -b x`,
		`.git/info/exclude`,
		`polyforge/aihub-1-x`,
		`git branch --unset-upstream`,
		`rev-parse --show-toplevel`,
	}
	for _, p := range planted {
		hit := false
		for token := range forbidden {
			if strings.Contains(p, token) {
				hit = true
			}
		}
		if !hit {
			t.Errorf("the detector does not fire on %q, so a re-implementation writing exactly that "+
				"would pass this gate", p)
		}
	}
	// Per-FILE, not just in total: `literals` above is a sum, so internal/drain could
	// contribute zero and drain.go alone would clear the floor. Every file must have been
	// really read.
	for path, n := range perFile {
		if n == 0 {
			t.Errorf("%s yielded no string literals at all — that is the AST walk failing, and "+
				"its silence reads exactly like a clean file", path)
		}
	}

	// The import half needs its own anti-vacuity: an empty import list from every file would
	// satisfy the loop above by having nothing to check.
	if _, imports, err := stringLiteralsIn("drain.go"); err != nil {
		t.Fatalf("%v", err)
	} else if len(imports) < 5 {
		t.Errorf("drain.go reports %d imports (%v) — the import walk is broken, and its silence "+
			"would read as \"this file imports nothing dangerous\"", len(imports), imports)
	}

	// And the git-command half, against a synthetic source rather than by mutating a real file:
	// both call shapes must be found, and a non-git command must NOT be.
	probe := `package p
import ("context"; "os/exec")
func f(ctx context.Context) {
	_ = exec.Command("git", "worktree", "add", "-b", "x", "p", "origin/main")
	_ = exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	_ = exec.Command("claude", "-p")
}`
	if lines := gitCommandLinesInSource(t, probe); len(lines) != 2 {
		t.Errorf("the git-command detector found %d sites in a probe containing exactly two "+
			"(exec.Command and exec.CommandContext) plus one non-git call: %v. A detector that "+
			"cannot fire reports a clean tree", len(lines), lines)
	}
}

type sourceLiteral struct {
	text string
	line int
}

// gitCommandLinesIn reports the lines where path constructs a git command through os/exec.
//
// ⚠️ WHAT THIS GATE STILL CANNOT SEE, stated rather than implied. It is a tripwire on the two
// spellings a re-implementation cannot avoid — a forbidden literal or a git command — in a FIXED
// file set (internal/cli/drain.go plus internal/drain/*.go). A new file elsewhere in internal/cli,
// a branch name built with path.Join, an exclude path built with filepath.Join, or a secret minted
// from math/rand all pass. Widening the file set is not free: internal/cli/engine.go shells to git
// legitimately, so "no git in internal/cli" is false. What actually pins the behaviour is
// TestClaimLifecycleIsOneImplementationReachedTwoWays, which compares OBSERVABLE state from both
// paths and cannot be fooled by where the code lives; this gate exists to make the cheap mistake
// loud, not to be the proof.
func gitCommandLinesIn(path string) []int {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	return gitCommandLines(fset, f)
}

func gitCommandLinesInSource(t *testing.T, src string) []int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "probe.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse the detector probe: %v", err)
	}
	return gitCommandLines(fset, f)
}

func gitCommandLines(fset *token.FileSet, f *ast.File) []int {
	var out []int
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "exec" {
			return true
		}
		// exec.Command(name, ...) and exec.CommandContext(ctx, name, ...) put the program
		// name in different positions; both are checked rather than one assumed.
		idx := -1
		switch sel.Sel.Name {
		case "Command":
			idx = 0
		case "CommandContext":
			idx = 1
		}
		if idx < 0 || len(call.Args) <= idx {
			return true
		}
		lit, ok := call.Args[idx].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if name, err := strconv.Unquote(lit.Value); err == nil && name == "git" {
			out = append(out, fset.Position(call.Pos()).Line)
		}
		return true
	})
	return out
}

// stringLiteralsIn returns every string literal in a Go file, COMMENTS EXCLUDED by construction:
// the parser is run without ParseComments, so a comment naming a forbidden token is invisible here.
// That is the difference between this and a grep, and it is load-bearing — the seam's doc comment
// names several of the tokens while explaining that they live elsewhere.
func stringLiteralsIn(path string) ([]sourceLiteral, []string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var out []sourceLiteral
	var imports []string
	for _, imp := range f.Imports {
		if p, uerr := strconv.Unquote(imp.Path.Value); uerr == nil {
			imports = append(imports, p)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		// An import path is a string literal too, and it is NOT a literal this gate is about:
		// importing internal/lifecycle is the correct behaviour, not a violation.
		if isImportPathLit(f, lit) {
			return true
		}
		unquoted, uerr := strconv.Unquote(lit.Value)
		if uerr != nil {
			unquoted = lit.Value
		}
		out = append(out, sourceLiteral{text: unquoted, line: fset.Position(lit.Pos()).Line})
		return true
	})
	return out, imports, nil
}

func isImportPathLit(f *ast.File, lit *ast.BasicLit) bool {
	for _, imp := range f.Imports {
		if imp.Path == lit {
			return true
		}
	}
	return false
}

// TestDrainWrapSendsDerived is the gate on the last call a successful drain makes, and it exists
// because that call USED TO FAIL EVERY TIME.
//
// aihub#350 (migration 0040) made `derived` required on a wrap: the server's own message is
// "derived is required to wrap … an omitted list is refused because …". drainQueries.CompleteAttempt
// sent only {status, note}, so a run could claim a work item, execute every step, and then be
// refused on the wrap — turning a perfect round into a FAILED one on the final request. Found by
// running a real round against an isolated project for aihub#667, not by reading.
//
// The assertion is on the KEY BEING PRESENT, not on its contents: an empty list is a legitimate
// disposition ("nothing carried forward") and the whole defect was absence.
//
// Mutant watched: delete the `body["derived"]` line and the wrapped arm goes red; the failed arm,
// which must NOT send it, stays green either way — which is why both arms are here.
func TestDrainWrapSendsDerived(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = map[string]any{}
		_ = json.Unmarshal(b, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	// 🔴 SANDBOX FIRST. CompleteAttempt's terminal branch calls config.DeleteStateFile, and
	// config.StateDir() walks up for .polyforge.yaml when POLYFORGE_WORKSPACE_ROOT is unset —
	// from this worktree that lands on the LIVE workspace, whose state directory holds every
	// claimed work item's session_secret. It was safe only because the literal "wi_x" happens
	// not to resolve to one of them, which is luck rather than isolation. The sibling contract
	// test in this package makes the same assertion for the same reason.
	root := t.TempDir()
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", root)
	if dir := config.StateDir(); !strings.HasPrefix(dir, root+string(os.PathSeparator)) {
		t.Fatalf("config.StateDir() = %q, outside this test's temp root %q — refusing to run: "+
			"this test reaches a state-file DELETE", dir, root)
	}

	q := &drainQueries{c: client.New(srv.URL, "k"), project: "p"}

	if err := q.CompleteAttempt(context.Background(), "wi_x", "wrapped", "done"); err != nil {
		t.Fatalf("CompleteAttempt(wrapped): %v", err)
	}
	if _, ok := got["derived"]; !ok {
		t.Errorf("a wrap was sent WITHOUT `derived` (body: %v). The server refuses that since "+
			"aihub#350, so every successful drain round would fail on its last call", got)
	}
	if s, _ := got["status"].(string); s != "wrapped" {
		t.Errorf("status = %q, want wrapped", s)
	}

	// Negative control: only a wrap records a disposition list. Sending it on a failure would be
	// claiming something the tool's own description says only a wrap records.
	got = nil
	if err := q.CompleteAttempt(context.Background(), "wi_x", "failed", "nope"); err != nil {
		t.Fatalf("CompleteAttempt(failed): %v", err)
	}
	if _, ok := got["derived"]; ok {
		t.Errorf("a FAILED completion carried `derived` (body: %v); only a wrap records it", got)
	}
}

// ─── the drain-side lifecycle seam's own helpers ──────────────────────────────
//
// These four were added by aihub#667 and a clean-context review found every one of them
// unreferenced by any test. Three of them are read by something destructive or by the only
// channel that reaches a human in an unattended run, so "it looked right" is not enough.

// TestClaimWorktreeRoot_IsDerivedFromWhatTheClaimActuallyCreated pins the value
// engine.CleanupWorktrees deletes a directory tree from at wrap.
//
// The derivation is deliberately from the RECORDED worktrees rather than recomputed from the
// slug: a repo that was skipped (a directory rejected by verifyClaimWorktree, aihub#328) must not
// make this name a guess, and the parent of any recorded worktree is the same directory for all
// of them.
func TestClaimWorktreeRoot_IsDerivedFromWhatTheClaimActuallyCreated(t *testing.T) {
	const ws = "/tmp/ws"

	t.Run("from the recorded worktrees", func(t *testing.T) {
		sf := &config.StateFile{
			Project: "aihub", Slug: "aihub#667",
			Worktrees: map[string]string{"aihub": "/elsewhere/pf.aihub-667/aihub"},
		}
		// /elsewhere, not /tmp/ws: the recorded path wins over the slug derivation, which is
		// the whole point — the claim may have put them somewhere this process did not compute.
		if got := claimWorktreeRoot(ws, sf); got != "/elsewhere/pf.aihub-667" {
			t.Errorf("claimWorktreeRoot = %q, want the PARENT of the recorded worktree", got)
		}
	})

	t.Run("falls back to the slug derivation when nothing was created", func(t *testing.T) {
		sf := &config.StateFile{Project: "aihub", Slug: "aihub#667"}
		if got := claimWorktreeRoot(ws, sf); got != filepath.Join(ws, "pf.aihub-667") {
			t.Errorf("claimWorktreeRoot = %q, want the slug-derived name", got)
		}
	})

	// 🔴 The empty answer matters more than the derived one. This value is handed to a cleanup
	// that removes a directory tree, and "" joined with anything is a relative path — returning
	// a guess here is how a wrap deletes something it never created.
	for name, sf := range map[string]*config.StateFile{
		"no project":       {Slug: "aihub#667"},
		"no slug":          {Project: "aihub"},
		"slug with no seq": {Project: "aihub", Slug: "aihub"},
		"nothing at all":   {},
	} {
		if got := claimWorktreeRoot(ws, sf); got != "" {
			t.Errorf("%s: claimWorktreeRoot = %q, want \"\" — a directory tree is removed from "+
				"this path at wrap, so a derived-from-nothing value is worse than none", name, got)
		}
	}
}

// TestLockBlockerFrom_ReadsTheHolderTheServerNamed pins layer ②'s input.
//
// aihub#640's `notification_three_layers` calls the note on the BLOCKER's work item the only
// channel that reaches another human in an unattended run, and internal/drain/runner.go only
// sends it when Blocker.WorkItem is non-empty — so a parse that quietly returns nil silences the
// feature without failing anything.
func TestLockBlockerFrom_ReadsTheHolderTheServerNamed(t *testing.T) {
	// The exact shape internal/domain/run_attempts.go builds for CONFLICT_LOCK_TAKEN.
	apiErr := &client.APIError{
		StatusCode: 409,
		Code:       "CONFLICT_LOCK_TAKEN",
		Message:    "resource file_scope:aihub:aihub:internal/cli/drain.go is already locked",
		Details:    json.RawMessage(`{"conflict_with":{"attempt_id":"ra_other","actor_display":"someone","work_item_slug":"aihub#665"}}`),
	}
	b := lockBlockerFrom(fmt.Errorf("wrapped: %w", apiErr))
	if b == nil {
		t.Fatal("no blocker parsed from a well-formed CONFLICT_LOCK_TAKEN, so the note that tells " +
			"the holder somebody is waiting is never sent")
	}
	if b.Actor != "someone" || b.WorkItem != "aihub#665" {
		t.Errorf("blocker = %+v, want actor=someone work_item=aihub#665", b)
	}
	if b.Resource != "file_scope:aihub:aihub:internal/cli/drain.go" {
		t.Errorf("blocker.Resource = %q — the key is the half an operator can act on", b.Resource)
	}

	// Negative controls. Each must return nil rather than an empty-but-non-nil Blocker, which
	// the runner would treat as "a holder was named".
	for name, err := range map[string]error{
		"not an API error":     errString("connection refused"),
		"a different 409":      &client.APIError{StatusCode: 409, Code: "CONFLICT_EPOCH_MISMATCH", Message: "nope"},
		"no details, no match": &client.APIError{StatusCode: 409, Code: "CONFLICT_LOCK_TAKEN", Message: "something else entirely"},
	} {
		if got := lockBlockerFrom(err); got != nil {
			t.Errorf("%s: lockBlockerFrom = %+v, want nil", name, got)
		}
	}

	// Best-effort is legitimate: the server's holder lookup may come back empty, and a refusal
	// reported without a name is still a refusal. The resource alone is enough to report.
	partial := &client.APIError{
		StatusCode: 409, Code: "CONFLICT_LOCK_TAKEN",
		Message: "resource file_scope:aihub:x.go is already locked",
	}
	if got := lockBlockerFrom(partial); got == nil || got.Resource != "file_scope:aihub:x.go" {
		t.Errorf("a conflict with no holder details lost its resource key: %+v", got)
	}
}

// TestProjectScenarioURL_RefusesRatherThanReturningEmpty covers the value every work item in a
// run needs. Returning "" would make engine.ResolveScenarioPath fail once per work item with a
// message about a URL rather than about the project.
func TestProjectScenarioURL_RefusesRatherThanReturningEmpty(t *testing.T) {
	serve := func(body string, status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
	}

	ok := serve(`{"name":"p","scenario":"git@github.com:GMISWE/polyforge-coding.git"}`, 200)
	defer ok.Close()
	got, err := projectScenarioURL(context.Background(), client.New(ok.URL, "k"), "p")
	if err != nil || got != "git@github.com:GMISWE/polyforge-coding.git" {
		t.Fatalf("projectScenarioURL = (%q, %v)", got, err)
	}

	// A project with scenario NULL decodes to a missing key, not to an error — which is
	// precisely the case that must not pass silently.
	none := serve(`{"name":"p"}`, 200)
	defer none.Close()
	if _, err := projectScenarioURL(context.Background(), client.New(none.URL, "k"), "p"); err == nil {
		t.Error("a project with no scenario returned no error, so every work item in the run " +
			"would fail separately on a message about a URL instead of once about the project")
	} else if !strings.Contains(err.Error(), "no scenario repo") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}

	bad := serve(`{"error":{"code":"PROJECT_NOT_FOUND"}}`, 404)
	defer bad.Close()
	if _, err := projectScenarioURL(context.Background(), client.New(bad.URL, "k"), "p"); err == nil {
		t.Error("a 404 on the project lookup was swallowed")
	}
}
