package cli

import (
	"bytes"
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
	"syscall"
	"testing"
	"time"

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

// TestBlockingEdges_ReadsOnlyTheEdgesThatActuallyBlock guards ObserveQueue's external-block
// detection in BOTH directions, which is what its predecessor did not.
//
// ─── what this replaced, and why that test was part of the defect ─────────────
// It was TestDependencyIDs_ReadsEveryShapeTheEndpointUses, and it fed the parser one key
// (`blocked_by`) holding three live in-scope ids. That payload cannot distinguish a correct
// parser from the one that shipped: nothing in it had a `kind`, nothing sat under `blocking`, and
// nothing was inaccessible — so the three false positives aihub#678 ③(a) names were all outside
// what the fixture could express, and the gate certified its own blind spot. The fixture below is
// the one the endpoint actually produces (internal/domain/dependencies.go's
// DependenciesResponse), and every entry in it is one of those false positives.
//
// Mutants watched RED, each applied alone:
//  1. re-add "blocking" to the keys read        → wi_downstream appears
//  2. drop the `kind != "blocks"` filter        → wi_related and wi_superseded appear
//  3. drop the accessible/sentinel check        → the hidden edge comes back notifiable
func TestBlockingEdges_ReadsOnlyTheEdgesThatActuallyBlock(t *testing.T) {
	res := map[string]any{
		// The far side of the graph: work items MY work item is holding up. Reading these as
		// blockers made owning a downstream dependent report BLOCKED_EXTERNAL and put a note on
		// the dependent saying the opposite of the truth.
		"blocking": []any{
			map[string]any{"id": "wi_downstream", "slug": "p#50", "kind": "blocks", "accessible": true},
		},
		"blocked_by": []any{
			map[string]any{"id": "wi_a", "slug": "p#1", "kind": "blocks", "accessible": true},
			// Non-blocking edge kinds. The server's own readiness predicate requires
			// kind='blocks'; these two hold nothing up.
			map[string]any{"id": "wi_related", "slug": "p#2", "kind": "related", "accessible": true},
			map[string]any{"id": "wi_superseded", "slug": "p#3", "kind": "supersedes", "accessible": true},
			// Cross-project, invisible to this caller: slug present, id replaced by the sentinel.
			map[string]any{"id": "hidden", "slug": "other#7", "kind": "blocks", "accessible": false},
		},
	}

	got := blockingEdges(res)
	if len(got) != 2 {
		t.Fatalf("blockingEdges = %+v, want exactly 2 (the live `blocks` edge and the hidden one)", got)
	}

	var sawA, sawHidden bool
	for _, ref := range got {
		switch ref.Slug {
		case "p#1":
			sawA = true
			if ref.ID != "wi_a" || !ref.Notifiable() {
				t.Errorf("the accessible blocker came back %+v; layer 2 needs its id to write a note", ref)
			}
		case "other#7":
			sawHidden = true
			if ref.ID != "" || ref.Notifiable() {
				t.Errorf("an inaccessible blocker came back notifiable as %+v. Its id is the "+
					"sentinel \"hidden\", and emitting a note against it 404s on every round", ref)
			}
			if ref.Display() != "other#7" {
				t.Errorf("Display() = %q; the slug is sent precisely so an unopenable blocker can "+
					"still be named", ref.Display())
			}
		default:
			t.Errorf("blockingEdges returned %+v, which is not an edge that blocks anything", ref)
		}
	}
	if !sawA || !sawHidden {
		t.Errorf("blockingEdges = %+v, want the live blocks edge and the hidden one", got)
	}

	// Negative controls: an unrelated payload, and one whose only edges are the wrong direction.
	if n := len(blockingEdges(map[string]any{"unrelated": 1})); n != 0 {
		t.Errorf("blockingEdges on an unrelated payload returned %d refs", n)
	}
	onlyDownstream := map[string]any{"blocking": []any{
		map[string]any{"id": "wi_x", "kind": "blocks", "accessible": true}}}
	if n := len(blockingEdges(onlyDownstream)); n != 0 {
		t.Errorf("a work item that only BLOCKS others reported %d blockers of its own", n)
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
	// 🔴 attempt_id, and this assertion is why the fixture above has always carried it.
	//
	// It is the field aihub#678 ③(b)'s whole decision rests on — "is the holder one of MY OWN
	// concurrent claims, which will release this lock inside the round?" — and for one commit it
	// was tested nowhere, because every ③(b) test lives in internal/drain where the fake hands
	// the runner a Blocker with AttemptID already populated. This file is the only place the wire
	// is crossed, and this test asserted Actor, WorkItem and Resource: every field EXCEPT the one
	// the new mechanism needed. The details struct simply did not declare `attempt_id`, so the
	// value arrived and was dropped, silently — a missing field in an Unmarshal target never
	// fails — and the entire retry path was inert in production while every unit test was green.
	// Found by a clean-context reviewer who added exactly this line.
	if b.AttemptID != "ra_other" {
		t.Errorf("blocker.AttemptID = %q, want ra_other. Without it heldByThisRun can never say "+
			"yes, so a lock held by this run's own concurrent claim is written off for the whole "+
			"run and the fix does nothing at all", b.AttemptID)
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

	// ...and a refusal that named ONLY the attempt must not be discarded as "unnamed". It is the
	// single field the retry decision reads, so a nil here would throw away the whole signal.
	attemptOnly := &client.APIError{
		StatusCode: 409, Code: "CONFLICT_LOCK_TAKEN", Message: "locked",
		Details: json.RawMessage(`{"conflict_with":{"attempt_id":"ra_mine"}}`),
	}
	if got := lockBlockerFrom(attemptOnly); got == nil || got.AttemptID != "ra_mine" {
		t.Errorf("a conflict naming only the holding attempt returned %+v; that field alone is "+
			"enough to decide whether this run will release the lock itself", got)
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

// ─── aihub#673: --detach / --stop / --preset ──────────────────────────────────

// TestParseDrainArgs_NewFlagsParse pins that the three flags the owner specified
// in aihub#640's command_surface are actually read. Before aihub#673 all three
// were zero-hit literals in this package.
func TestParseDrainArgs_NewFlagsParse(t *testing.T) {
	o, err := parseDrainArgs([]string{"--project=aihub", "--detach", "--preset=frugal"})
	if err != nil {
		t.Fatalf("parseDrainArgs: %v", err)
	}
	if !o.Detach {
		t.Error("--detach did not set Detach")
	}
	if o.Preset != "frugal" {
		t.Errorf("--preset=frugal gave Preset=%q, want %q", o.Preset, "frugal")
	}

	s, err := parseDrainArgs([]string{"--stop", "--run=20260914T120000Z-1"})
	if err != nil {
		t.Fatalf("parseDrainArgs(--stop --run): %v", err)
	}
	if !s.Stop {
		t.Error("--stop did not set Stop")
	}
	if s.RunID != "20260914T120000Z-1" {
		t.Errorf("--run gave RunID=%q", s.RunID)
	}
}

// TestParseDrainArgs_StopNeedsNoProject pins that --stop parses without
// --project.
//
// It is the flag you reach for when a scheduler is misbehaving, and requiring
// the project name to stop it would mean the one command that ends a runaway run
// is unavailable to somebody who only knows there IS one. RunDrain enforces the
// same thing by resolving --stop before the --project check.
func TestParseDrainArgs_StopNeedsNoProject(t *testing.T) {
	o, err := parseDrainArgs([]string{"--stop"})
	if err != nil {
		t.Fatalf("parseDrainArgs(--stop) without --project: %v", err)
	}
	if !o.Stop {
		t.Fatal("--stop did not set Stop")
	}
}

// TestParseDrainArgs_RefusesContradictoryModes pins that mode conflicts are
// REFUSED rather than resolved by precedence.
//
// Each pair has two defensible readings — does `--stop --detach` stop in the
// background, or start one and stop another? — and this command claims and
// executes real work items. Picking one silently is how a flag combination
// becomes folklore.
func TestParseDrainArgs_RefusesContradictoryModes(t *testing.T) {
	for _, args := range [][]string{
		{"--project=aihub", "--stop", "--detach"},
		{"--project=aihub", "--stop", "--plan"},
		{"--project=aihub", "--detach", "--plan"},
		{"--project=aihub", "--stop", "--preset=frugal"},
	} {
		if _, err := parseDrainArgs(args); err == nil {
			t.Errorf("parseDrainArgs(%v) was accepted; contradictory modes must be refused", args)
		}
	}
}

// TestParseDrainArgs_RefusesInertRunFlag pins that --run without --stop is
// refused rather than ignored.
//
// This parser already refuses unknown flags on the stated grounds that a
// silently-dropped flag on a scheduler reads as a budget that was honoured when
// it was not. A flag that IS known but inert in the current mode is the same
// defect wearing a better disguise: `polyforge drain --project=x --run=<id>`
// looks like it targeted a run and targets nothing.
func TestParseDrainArgs_RefusesInertRunFlag(t *testing.T) {
	if _, err := parseDrainArgs([]string{"--project=aihub", "--run=20260914T120000Z-1"}); err == nil {
		t.Error("parseDrainArgs accepted --run without --stop; a known-but-inert flag reads " +
			"to the operator exactly like an honoured one")
	}
}

// TestParseDrainArgs_RefusesEmptyPreset pins that `--preset=` is an error.
//
// The value is routinely a shell variable, and an unset one expands to nothing.
// Treating the empty string as "use the configured default" would make a broken
// invocation indistinguishable from a deliberate one.
func TestParseDrainArgs_RefusesEmptyPreset(t *testing.T) {
	if _, err := parseDrainArgs([]string{"--project=aihub", "--preset="}); err == nil {
		t.Error("parseDrainArgs accepted an empty --preset=")
	}
}

// TestDetachChildArgs_DropsOnlyDetach pins the two properties the detached
// child's argument vector needs.
//
// Keeping --detach would make the child detach again, and again: an infinite
// spawn chain, each generation writing a new run directory. Reconstructing the
// flags from drainOptions instead of filtering the raw vector would silently
// drop any flag this function has not been taught about, which is a defect that
// only appears the next time somebody adds one.
func TestDetachChildArgs_DropsOnlyDetach(t *testing.T) {
	got := detachChildArgs([]string{"--project=aihub", "--detach", "--max-parallel=3", "--preset=frugal"})
	want := []string{"drain", "--project=aihub", "--max-parallel=3", "--preset=frugal"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detachChildArgs() = %v, want %v", got, want)
	}
	for _, a := range got {
		if a == "--detach" {
			t.Fatal("detachChildArgs kept --detach; the child would detach again, forever")
		}
	}
}

// TestDetachedRunID_ReadsThenUnsets pins that the handed-down run id is consumed
// exactly once.
//
// The unset is not tidiness. Step agents inherit this process's environment, so
// a `polyforge drain` started from inside a step would adopt its parent's run id
// and the two processes would write the same snapshot.json, each overwriting the
// other's view of a different run.
func TestDetachedRunID_ReadsThenUnsets(t *testing.T) {
	t.Setenv(detachRunIDEnv, "20260914T120000Z-424242")

	if got := detachedRunID(); got != "20260914T120000Z-424242" {
		t.Fatalf("detachedRunID() = %q, want the value from the environment", got)
	}
	if v, ok := os.LookupEnv(detachRunIDEnv); ok {
		t.Errorf("%s is still set to %q after being read; a nested drain would inherit it",
			detachRunIDEnv, v)
	}
	if got := detachedRunID(); got != "" {
		t.Errorf("detachedRunID() = %q on a second call, want \"\"", got)
	}
}

// TestResolvePresetModels_MapsTierToModelPerHarness pins the mapping drain
// dispatches with, and the two exclusions that matter.
func TestResolvePresetModels_MapsTierToModelPerHarness(t *testing.T) {
	mc := &config.MachineConfig{Roles: &config.MachineRoles{
		Presets: map[string]*config.RolesPreset{
			"frugal": {Tiers: map[string][]config.RoleCandidate{
				"default": {{Harness: "pi", Model: "cheap-default"}},
				"raised":  {{Harness: "pi", Model: "dear-raised"}},
			}},
		},
	}}
	channels := []drain.Channel{{Harness: drain.HarnessClaude}, {Harness: drain.HarnessPi}}

	got, label, err := resolvePresetModels(mc, "frugal", channels, presetTestProbes)
	if err != nil {
		t.Fatalf("resolvePresetModels: %v", err)
	}
	if !strings.Contains(label, "frugal") {
		t.Errorf("label = %q, want it to name the preset so the snapshot records provenance", label)
	}
	if got[drain.HarnessPi]["default"] != "cheap-default" {
		t.Errorf("pi/default = %q, want %q", got[drain.HarnessPi]["default"], "cheap-default")
	}
	if got[drain.HarnessPi]["raised"] != "dear-raised" {
		t.Errorf("pi/raised = %q, want %q", got[drain.HarnessPi]["raised"], "dear-raised")
	}
	// Claude Code must never be given a model from this table. aihub#555
	// measured that passing --model to `claude -p` silently OVERRIDES the agent
	// file's own frontmatter, and config.RoleCandidate's contract is that a
	// candidate's harness is pi/codex/opencode -- never cc.
	if _, ok := got[drain.HarnessClaude]; ok {
		t.Errorf("claude was given tier models %+v; passing --model to Claude Code silently "+
			"overrides the agent file frontmatter (aihub#555)", got[drain.HarnessClaude])
	}
}

// TestResolvePresetModels_UnknownPresetIsFatal pins that drain refuses to start
// on a preset it cannot resolve, rather than running the default table.
//
// The direction of the failure is the point. An operator who asked for `frugal`
// and silently got something dearer has no signal anywhere: the run works, the
// snapshot looks normal, and nothing says the request was dropped.
func TestResolvePresetModels_UnknownPresetIsFatal(t *testing.T) {
	mc := &config.MachineConfig{Roles: &config.MachineRoles{
		Tiers: map[string][]config.RoleCandidate{"default": {{Harness: "pi", Model: "bare"}}},
	}}
	if _, _, err := resolvePresetModels(mc, "nope", []drain.Channel{{Harness: drain.HarnessPi}},
		presetTestProbes); err == nil {
		t.Error("resolvePresetModels accepted an unknown preset; it must refuse so the run " +
			"does not proceed on models nobody asked for")
	}
}

// TestResolvePresetModels_NoTableIsNotAnError pins that a machine which
// configured nothing keeps drain's existing behaviour exactly: no models
// resolved, every harness picks its own default, no error.
func TestResolvePresetModels_NoTableIsNotAnError(t *testing.T) {
	got, label, err := resolvePresetModels(&config.MachineConfig{}, "",
		[]drain.Channel{{Harness: drain.HarnessPi}}, presetTestProbes)
	if err != nil {
		t.Fatalf("resolvePresetModels on an unconfigured machine: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("resolvePresetModels = %+v on an unconfigured machine, want empty", got)
	}
	if label == "" {
		t.Error("resolvePresetModels returned an empty label; the snapshot records it as provenance")
	}
}

// TestDispatchWithPresetModel_FillsFromTierButNeverOverrides pins the dispatch
// seam's two rules: a step's model comes from its ROLE's tier, and an explicitly
// requested model is never overwritten.
//
// It asserts on the request the wrapper passes on rather than on a spawned
// process, because the point under test is the mapping, not the exec.
func TestDispatchWithPresetModel_FillsFromTierButNeverOverrides(t *testing.T) {
	tierModels := map[drain.Harness]map[string]string{
		drain.HarnessPi: {"default": "from-default-tier", "raised": "from-raised-tier"},
	}
	tierOf, err := roleTiers()
	if err != nil {
		t.Fatalf("roleTiers: %v", err)
	}

	// Pick two real roles out of the embedded catalog that sit on different
	// tiers, so this test cannot pass by accident on a single-tier catalog.
	var defaultRole, raisedRole string
	for name, tier := range tierOf {
		switch tier {
		case "default":
			if defaultRole == "" {
				defaultRole = name
			}
		case "raised":
			if raisedRole == "" {
				raisedRole = name
			}
		}
	}
	if defaultRole == "" || raisedRole == "" {
		t.Fatalf("the embedded role catalog has no default-tier and raised-tier role to test with "+
			"(tiers: %+v); this test's premise is gone, not merely unmet", tierOf)
	}

	var seen []drain.Channel
	capture := func(_ context.Context, req drain.DispatchRequest) (drain.DispatchResult, error) {
		seen = append(seen, req.Channel)
		return drain.DispatchResult{Output: "ok"}, nil
	}
	wrapped := wrapDispatchWithPresetModel(tierModels, tierOf, capture)

	for _, tc := range []struct {
		name string
		req  drain.DispatchRequest
		want string
		why  string
	}{
		{
			name: "default-tier role gets the default-tier model",
			req:  drain.DispatchRequest{Role: defaultRole, Channel: drain.Channel{Harness: drain.HarnessPi}},
			want: "from-default-tier",
			why:  "the role's tier selects the model",
		},
		{
			name: "raised-tier role gets the raised-tier model",
			req:  drain.DispatchRequest{Role: raisedRole, Channel: drain.Channel{Harness: drain.HarnessPi}},
			want: "from-raised-tier",
			why:  "tiers must differentiate, or the preset is decorative",
		},
		{
			name: "an explicitly requested model is never overwritten",
			req: drain.DispatchRequest{Role: defaultRole,
				Channel: drain.Channel{Harness: drain.HarnessPi, Model: "from---channel"}},
			want: "from---channel",
			why:  "--channel=pi/model is explicit and beats configuration",
		},
		{
			name: "a harness with no tier models is left alone",
			req:  drain.DispatchRequest{Role: defaultRole, Channel: drain.Channel{Harness: drain.HarnessClaude}},
			want: "",
			why:  "Claude Code must keep its agent-file frontmatter (aihub#555)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen = nil
			if _, err := wrapped(context.Background(), tc.req); err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			if len(seen) != 1 {
				t.Fatalf("dispatch called %d times, want 1", len(seen))
			}
			if seen[0].Model != tc.want {
				t.Errorf("dispatched model = %q, want %q (%s)", seen[0].Model, tc.want, tc.why)
			}
		})
	}
}

// TestRoleTiers_IndexesTheEmbeddedCatalog pins that the role->tier index is
// populated from the real catalog. An empty index would make every preset
// silently inert, which is a failure that looks exactly like "no preset
// configured".
func TestRoleTiers_IndexesTheEmbeddedCatalog(t *testing.T) {
	got, err := roleTiers()
	if err != nil {
		t.Fatalf("roleTiers: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("roleTiers() is empty; every preset would be silently inert")
	}
	for role, tier := range got {
		if tier == "" {
			t.Errorf("role %q has an empty tier", role)
		}
	}
}

// TestCmdlineIsDrainRun pins the pid-reuse guard's decision logic.
//
// The second clause is the one worth stating. "Is it polyforge" alone would be
// satisfied by the long-lived `polyforge serve` MCP process every editor session
// starts -- by far the commonest polyforge process on these machines -- so a
// recycled pid would get the team's MCP server SIGTERMed by a command that
// thought it was stopping a scheduler.
func TestCmdlineIsDrainRun(t *testing.T) {
	const self = "/root/.local/bin/polyforge"
	tests := []struct {
		name    string
		cmdline string
		want    bool
	}{
		{"a real detached drain", "/usr/local/bin/polyforge drain --project=aihub", true},
		{"a drain with more flags", "/root/.local/bin/polyforge drain --project=aihub --max-parallel=3", true},
		// The published artefacts are named polyforge-<goos>-<goarch>
		// (publish-bins.yml), not "polyforge", so this must match.
		{"a published binary's name", "/usr/local/bin/polyforge-linux-amd64 drain --project=aihub", true},
		// A locally built or renamed binary carries none of that, and the FIRST
		// live test of --stop refused a real run for exactly this reason. It is
		// matched by being the same file this process is running as.
		{"a renamed binary that is the same file as us", "/root/.local/bin/polyforge drain --all", true},
		{"the MCP server, same binary", "/usr/local/bin/polyforge serve", false},
		{"another polyforge subcommand", "/usr/local/bin/polyforge doctor", false},
		{"an unrelated process that got the pid", "/usr/bin/postgres -D /var/lib/postgresql", false},
		{"an unrelated process that merely says drain", "/usr/bin/tail -f /var/log/drain.log", false},
		// Only argv[0] identifies the program. Searching the whole command line
		// let a log path or a working directory satisfy the binary half.
		{"an unrelated tool whose ARGS mention polyforge", "/usr/bin/tail -f /var/log/polyforge.log drain", false},
		{"a wrapper script that merely runs polyforge", "/bin/sh /opt/wrap/polyforge drain --project=x", false},
		// This case is the reason the "polyforge" clause exists, and it was
		// added after a mutation run: dropping that clause escaped the first
		// version of this table, because no case there combined "not polyforge"
		// with a BARE `drain` argv token. Without it the guard would signal any
		// recycled pid whose command line happens to take a subcommand by that
		// name.
		{"an unrelated tool with a bare drain subcommand", "/usr/bin/somedb drain --force", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cmdlineIsDrainRun(tt.cmdline, self); got != tt.want {
				t.Errorf("cmdlineIsDrainRun(%q) = %v, want %v", tt.cmdline, got, tt.want)
			}
		})
	}
}

// TestPidLooksLikeDrain_CheckedFlag pins the two-value contract of the /proc
// reader: "could not tell" must stay distinguishable from "not a drain", because
// --stop refuses on the second and proceeds-with-disclosure on the first.
func TestPidLooksLikeDrain_CheckedFlag(t *testing.T) {
	// A pid that cannot exist must report that it could not be checked, never a
	// confident "not a drain".
	if _, checked := pidLooksLikeDrain(-1); checked {
		t.Error("pidLooksLikeDrain(-1) reported that it checked a pid that cannot exist")
	}

	ok, checked := pidLooksLikeDrain(os.Getpid())
	if !checked {
		t.Skip("/proc is unavailable here; --stop discloses the skipped guard rather than " +
			"pretending it ran")
	}
	// This test binary is `cli.test`, not a drain, so the guard must say so.
	// That is the assertion: the check discriminates rather than waving things
	// through.
	if ok {
		t.Error("pidLooksLikeDrain(self) accepted the test binary as a drain run; " +
			"the guard does not discriminate and would not prevent a pid-reuse kill")
	}
}

// presetTestProbes stands in for probeForHarness. It resolves every model for
// the harnesses that have a catalog in production and returns nil for claude,
// exactly as probeForHarness does -- a live probe would shell out to `pi` /
// `codex`, which are unauthenticated in CI, so the real one would resolve
// nothing and the assertions would pass vacuously.
func presetTestProbes(harness string) CatalogProbe {
	switch harness {
	case "pi", "codex", "opencode":
		return allModelsProbe{}
	default:
		return nil
	}
}

type allModelsProbe struct{}

func (allModelsProbe) HasModel(string) (bool, error) { return true, nil }

// TestStartDetached_ReturnsAUsablePid is a regression test for a defect that
// every unit test missed and the first live run found immediately.
//
// os.Process.Release sets Pid to -1 on Unix. The first version of --detach
// called Release and then printed cmd.Process.Pid, so it reported
//
//	pid:    -1
//
// which breaks the single output contract aihub#640 `entrypoint_not_a_session`
// fixes for this flag ("spawn 后台进程后立即返回 pid+日志路径") and hands the
// caller a pid that cannot be signalled, watched or even looked up. --stop reads
// its pid from the snapshot rather than from this line, so nothing else in the
// system would have reported the problem.
//
// The assertion is simply "the pid is usable": positive, and not this process.
func TestStartDetached_ReturnsAUsablePid(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devnull.Close() }()

	out, err := os.CreateTemp(t.TempDir(), "detach-*.log")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer func() { _ = out.Close() }()

	// `true` rather than a real drain: what is under test is the pid handoff,
	// and a command that exits immediately keeps the test from depending on a
	// harness, a credential or a server.
	pid, err := startDetached("/usr/bin/env", []string{"true"}, os.Environ(), devnull, out)
	if err != nil {
		t.Fatalf("startDetached: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("startDetached returned pid %d; Release() zeroes cmd.Process.Pid, so the pid must "+
			"be read BEFORE releasing the child. --detach printed -1 for exactly this reason", pid)
	}
	if pid == os.Getpid() {
		t.Errorf("startDetached returned this process's own pid (%d)", pid)
	}
}

// ─── aihub#673 review follow-ups ──────────────────────────────────────────────

// TestRunDrainDetach_RegistersTheRunBeforeReturning is the regression test for
// the BLOCKER a clean-context review found: --detach spawned the child and
// returned without writing either the snapshot or the `latest` pointer.
//
// The child does write both, but only after resolving its user id, reading the
// project's scenario URL and PREFLIGHTING EVERY CHANNEL, which spawns a real
// harness process per candidate. For that whole window — seconds to tens of
// seconds — `drain --stop` with no --run read `latest` and got the PREVIOUS run:
// if that one had finished it printed "already finished. Nothing to stop." and
// exited 0 while the new run went on claiming work items unattended, and if it
// had not, it SIGTERMed the wrong run. The pid-reuse guard cannot catch either,
// because it asks "is this pid a drain", not "is it THIS run".
//
// The assertion is that after runDrainDetach returns, both records name the run
// it just printed.
func TestRunDrainDetach_RegistersTheRunBeforeReturning(t *testing.T) {
	home := t.TempDir()

	// A previous, FINISHED run, so a regression reproduces the exact bad
	// outcome rather than merely "latest is empty": with `latest` still naming
	// this one, --stop would report success and stop nothing.
	prev := "20260101T000000Z-1"
	if err := drain.WriteSnapshot(drain.RunDir(home, prev), &drain.Snapshot{
		RunID: prev, PID: 1, Finished: true, Terminal: drain.TerminalCompleted,
	}); err != nil {
		t.Fatalf("seed previous snapshot: %v", err)
	}
	if err := drain.WriteLatest(home, prev); err != nil {
		t.Fatalf("seed latest: %v", err)
	}

	// `--project` is a real project name only so the spawned child has
	// something to fail on quickly; the child is never waited for and what is
	// under test is what the PARENT recorded before returning.
	opts, err := parseDrainArgs([]string{"--project=aihub", "--detach"})
	if err != nil {
		t.Fatalf("parseDrainArgs: %v", err)
	}
	out := captureStdout(t, func() {
		runDrainDetach(home, []string{"--project=aihub", "--detach"}, opts)
	})

	runID := drain.ReadLatest(home)
	if runID == prev {
		t.Fatalf("after --detach, latest still names the PREVIOUS run %q; `drain --stop` would "+
			"report \"already finished. Nothing to stop.\" and exit 0 while the new run kept "+
			"claiming work items", prev)
	}
	if runID == "" {
		t.Fatal("after --detach, latest names no run at all")
	}
	if !strings.Contains(out, runID) {
		t.Errorf("--detach printed a different run id than it recorded.\nrecorded: %q\nprinted:\n%s",
			runID, out)
	}

	s, err := drain.ReadSnapshot(drain.RunDir(home, runID))
	if err != nil {
		t.Fatalf("--detach did not write a readable snapshot, so `polyforge watch --run=%s` "+
			"(the command it tells you to run) fails: %v", runID, err)
	}
	if s.RunID != runID {
		t.Errorf("snapshot run_id = %q, want %q", s.RunID, runID)
	}
	if s.PID <= 0 {
		t.Errorf("snapshot pid = %d; --stop reads its target pid from here", s.PID)
	}
	if s.Finished {
		t.Error("the initial snapshot says Finished; --stop would refuse to stop a run that " +
			"has only just started")
	}
	if s.Project != "aihub" {
		t.Errorf("snapshot project = %q, want %q", s.Project, "aihub")
	}
	// Kill the child: this test spawns a real process and must not leak it.
	// A leaked drain on this box is not hypothetical — orphaned processes from a
	// subagent once burned 7.5 of 12 cores for eleven days.
	if s.PID > 0 {
		_ = syscall.Kill(s.PID, syscall.SIGKILL)
	}
}

// TestSignalTarget_GroupWhenLeader pins that --stop signals the whole process
// group when it can, which is what reaches the step agents' own children.
//
// A review found that SIGTERM to a single pid is NOT what Ctrl-C does: the tty
// delivers Ctrl-C to the entire foreground group, while runHarness uses
// exec.CommandContext with no Cancel, so cancellation SIGKILLs the harness
// process only and every grandchild is orphaned onto init. On this box that has
// a measured history: orphaned load generators once burned 7.5 of 12 cores for
// eleven days.
func TestSignalTarget_GroupWhenLeader(t *testing.T) {
	// This test process is a group leader under `go test`'s own setup in most
	// environments, but not guaranteed, so the assertion is driven by what the
	// kernel actually reports rather than by an assumption.
	self := os.Getpid()
	pgid, err := syscall.Getpgid(self)
	if err != nil {
		t.Skipf("Getpgid unavailable: %v", err)
	}

	target, whole := signalTarget(self)
	if pgid == self {
		if !whole || target != -self {
			t.Errorf("signalTarget(%d) = (%d, %v) for a process that IS its own group leader; "+
				"want (%d, true) so the signal reaches the step agents' children",
				self, target, whole, -self)
		}
	} else if whole || target != self {
		t.Errorf("signalTarget(%d) = (%d, %v) for a process that is NOT a group leader (pgid %d); "+
			"want (%d, false) -- signalling -pid there would miss or hit an unrelated group",
			self, target, whole, pgid, self)
	}
}

// TestValidRunID pins the shape check on the handed-down run id. The value is
// interpolated straight into a filesystem path, so `../../x` would write outside
// the drain directory.
func TestValidRunID(t *testing.T) {
	// The real generator's output must pass, or the check would reject every
	// detached run -- which is the way this guard could do harm.
	if got := drain.NewRunID(time.Now(), os.Getpid()); !validRunID(got) {
		t.Errorf("validRunID rejected drain.NewRunID's own output %q", got)
	}
	for _, bad := range []string{
		"", "../../etc", "20260914T124435Z-3448278/../..", "not-a-run-id",
		"20260914T124435Z", "20260914T124435Z-", "/abs/path",
	} {
		if validRunID(bad) {
			t.Errorf("validRunID(%q) = true, want false", bad)
		}
	}
}

// TestDetachedRunID_DiscardsMalformed pins that a malformed handed-down id is
// discarded rather than used or fatal: RunDrain then mints its own, which beats
// failing a scheduler over an environment variable.
func TestDetachedRunID_DiscardsMalformed(t *testing.T) {
	t.Setenv(detachRunIDEnv, "../../escape")
	if got := detachedRunID(); got != "" {
		t.Errorf("detachedRunID() = %q for a malformed value, want \"\"", got)
	}
	if _, ok := os.LookupEnv(detachRunIDEnv); ok {
		t.Error("a malformed value was left in the environment for a nested drain to inherit")
	}
}

// captureStdout mirrors captureStderr (roles_generate_test.go) for stdout, which
// is where --detach's run id / pid / log path contract is printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = orig

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	_ = r.Close()
	return buf.String()
}

// ─── aihub#678 ───────────────────────────────────────────────────────────────────────────────

// TestCompleteAttemptFailed_ForcesTerminationOfTheOpenStep is finding ②.
//
// Every failure path in the runner — a dispatch error, a resolve-role error, engine startup, a
// step bracket that could not be filed, a silent refusal, the 2h step timeout — calls failAttempt
// while the step is still `in_progress`, because the thing that failed is what would have closed
// it. FnCompleteAttempt reads exactly that state and answers 409 CONFLICT_STEP_IN_PROGRESS: "a
// step is still in_progress; set force_terminate_step=true or update step first". The body drain
// sent carried {status, note, credentials} and never that flag, so the one call whose job is to
// release the work item was REFUSED on every one of those paths — leaving the work item `running`,
// holding its locks, behind a process that had exited. Recovery meant pf_force_takeover, by hand.
//
// Both arms are here because the flag must be sent on exactly one status. On `wrapped` a step
// still in progress means the loop wrapped a work item whose last step never completed, and the
// 409 is the only thing that would ever say so.
//
// Mutants watched RED: deleting the `body["force_terminate_step"]` line (failed arm); moving it
// out of the `status == "failed"` guard so it rides on every status (wrapped arm).
func TestCompleteAttemptFailed_ForcesTerminationOfTheOpenStep(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = map[string]any{}
		_ = json.Unmarshal(b, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	q := &drainQueries{c: client.New(srv.URL, "k"), project: "p"}

	if err := q.CompleteAttempt(context.Background(), "wi_1", "failed", "the step agent died"); err != nil {
		t.Fatalf("CompleteAttempt(failed): %v", err)
	}
	if got["force_terminate_step"] != true {
		t.Errorf("the failed body carried force_terminate_step=%#v, want true. Without it the "+
			"server answers 409 CONFLICT_STEP_IN_PROGRESS and the work item is left `running`, "+
			"holding its locks, with nobody executing it. body=%v", got["force_terminate_step"], got)
	}

	if err := q.CompleteAttempt(context.Background(), "wi_1", "wrapped", "drained"); err != nil {
		t.Fatalf("CompleteAttempt(wrapped): %v", err)
	}
	if v, present := got["force_terminate_step"]; present && v == true {
		t.Errorf("the WRAPPED body carried force_terminate_step. A step still in_progress at wrap " +
			"time means the loop wrapped a work item whose last step never completed, and the 409 " +
			"is the only thing that would say so; forcing it files a `failed` step row under a " +
			"`wrapped` attempt and calls that success")
	}
}

// TestObserveQueue_ATerminalBlockerBlocksNothing is finding ③(a)'s second false positive, which is
// the one that fires on an ordinary healthy project.
//
// unblockDependentWI requeues a dependent WITHOUT deleting its wi_dependencies row — its own
// source comment says so — so a finished blocker stays on the edge list forever. AllInScope holds
// only NON-terminal work items, so my own wrapped blocker is absent from the in-scope set and was
// classified as somebody else's: a work item waiting on one live blocker OF MINE reported
// BLOCKED_EXTERNAL (exit 11, "a human must act") instead of IDLE, and left a note on a wrapped
// work item on every round.
//
// The predicate this restores is the server's own: `dep.kind = 'blocks' AND blocker.status NOT IN
// ('wrapped','cancelled','failed')` (noLiveBlockerPredicate).
//
// Mutant watched RED: making blockerIsLive return true unconditionally.
func TestObserveQueue_ATerminalBlockerBlocksNothing(t *testing.T) {
	var wiReads []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/dependencies"):
			// Two live-looking edges. wi_done is MINE and already wrapped; wi_live is mine and
			// still running. Neither is in AllInScope's answer below — the first because it is
			// terminal, which is exactly the case that used to be misread.
			_, _ = w.Write([]byte(`{"blocked_by":[
				{"id":"wi_done","slug":"p#1","kind":"blocks","accessible":true},
				{"id":"wi_live","slug":"p#2","kind":"blocks","accessible":true}]}`))
		case strings.Contains(r.URL.Path, "/work_items/wi_done"):
			wiReads = append(wiReads, "wi_done")
			_, _ = w.Write([]byte(`{"id":"wi_done","slug":"p#1","status":"wrapped"}`))
		case strings.Contains(r.URL.Path, "/work_items/wi_live"):
			wiReads = append(wiReads, "wi_live")
			_, _ = w.Write([]byte(`{"id":"wi_live","slug":"p#2","status":"running"}`))
		default:
			switch r.URL.Query().Get("status") {
			case "blocked":
				_, _ = w.Write([]byte(`{"items":[{"id":"wi_blocked","slug":"p#9"}]}`))
			case "queued,running,blocked,paused":
				_, _ = w.Write([]byte(`{"items":[{"id":"wi_blocked","slug":"p#9"}]}`))
			default:
				_, _ = w.Write([]byte(`{"items":[]}`))
			}
		}
	}))
	defer srv.Close()

	q := &drainQueries{c: client.New(srv.URL, "k"), project: "p", scope: drain.Scope{UserID: "u_me"}}
	st, err := q.ObserveQueue(context.Background())
	if err != nil {
		t.Fatalf("ObserveQueue: %v", err)
	}

	if st.BlockedByOthers() != 1 {
		t.Fatalf("BlockedByOthers = %d, want 1 (the live blocker only). state=%+v",
			st.BlockedByOthers(), st)
	}
	names := st.ExternallyBlocked[0].BlockerNames()
	if len(names) != 1 || names[0] != "p#2" {
		t.Errorf("blockers = %v, want just the LIVE one. A wrapped blocker still carries a "+
			"wi_dependencies row, and counting it means reporting BLOCKED_EXTERNAL forever and "+
			"annotating a wrapped work item on every run", names)
	}
	if !containsStr(wiReads, "wi_done") {
		t.Error("the wrapped blocker's status was never read; the dependency endpoint does not " +
			"return it, so nothing else can answer the predicate")
	}
}

// TestObserveQueue_AQueuedHumanSessionWorkItemIsNotCompleted is finding ③(d) end to end, over the
// real query shapes rather than a constructed QueueState.
//
// Mutant watched RED: dropping the NeedsHumanSession append from ObserveQueue.
func TestObserveQueue_AQueuedHumanSessionWorkItemIsNotCompleted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		qv := r.URL.Query()
		switch {
		case qv.Get("ready_only") == "true":
			// The server's ready predicate requires requires_human_session = false, so neither
			// of the two below comes back here.
			_, _ = w.Write([]byte(`{"items":[]}`))
		case qv.Get("status") == "queued":
			_, _ = w.Write([]byte(`{"items":[
				{"id":"wi_rhs","slug":"p#1","requires_human_session":true},
				{"id":"wi_null","slug":"p#2","requires_human_session":null}]}`))
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

	if len(st.NeedsHumanSession) != 2 {
		t.Fatalf("NeedsHumanSession = %+v, want both work items. The NULL one matters as much as "+
			"the true one: the column has three states and `requires_human_session = false` is "+
			"satisfied by neither, so drain cannot execute either", st.NeedsHumanSession)
	}
	if st.Empty() {
		t.Fatal("the queue reported Empty() with two work items still queued")
	}
	if got := drain.Classify(false, st); got != drain.TerminalBlockedExternal {
		t.Errorf("the run would end %s (exit %d); COMPLETED would exit 0 and tell an operator "+
			"there is nothing left to come back for", got, drain.ExitCode(got))
	}
}

// TestParseDrainArgs_MaxDuration is finding ④'s flag half.
//
// Refusing zero matters as much as parsing the value: zero is the field's documented "unbounded",
// so `--max-duration=0` would read as a bound that had been applied. Same reasoning as
// positiveInt, which the numeric budgets already use.
func TestParseDrainArgs_MaxDuration(t *testing.T) {
	o, err := parseDrainArgs([]string{"--project=p", "--max-duration=90m"})
	if err != nil {
		t.Fatalf("parseDrainArgs: %v", err)
	}
	if o.Budget.MaxDuration != 90*time.Minute {
		t.Errorf("MaxDuration = %s, want 90m", o.Budget.MaxDuration)
	}
	// Default is unbounded, which is what every other budget defaults to.
	if d, _ := parseDrainArgs([]string{"--project=p"}); d.Budget.MaxDuration != 0 {
		t.Errorf("default MaxDuration = %s, want 0 (unbounded)", d.Budget.MaxDuration)
	}
	for _, bad := range []string{"--max-duration=0", "--max-duration=-5m", "--max-duration=soon", "--max-duration="} {
		if _, err := parseDrainArgs([]string{"--project=p", bad}); err == nil {
			t.Errorf("%s was accepted; a budget flag that silently means \"unlimited\" is worse "+
				"than one that does not exist", bad)
		}
	}
}

// TestRunHarness_CancellationKillsTheWholeProcessTree is finding ⑥ C2, and it runs the real thing:
// a child that spawns a grandchild, cancelled, with the grandchild's survival measured.
//
// exec.CommandContext's default cancellation is os.Process.Kill — SIGKILL, to the harness process
// ONLY. A harness spawns subagents, MCP servers and git; SIGKILL gives it no chance to reap them,
// so on the 2h step timeout or any cancellation they are orphaned onto init, still holding the
// worktree open and still carrying a valid session_secret. The scenario that makes this more than
// untidy: a step hangs, drain moves on, wraps the work item and REMOVES ITS WORKTREE, while the
// orphans are still writing into it.
//
// Mutant watched RED: removing the Cancel/SysProcAttr assignment (the grandchild survives).
func TestRunHarness_CancellationKillsTheWholeProcessTree(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "grandchild.alive")

	// The grandchild outlives its parent by design: the parent exec's `sleep`, and the
	// grandchild touches a file every 100ms for 30s. If the group signal does not reach it, the
	// file keeps getting newer after the parent is gone.
	script := fmt.Sprintf(
		`( for i in $(seq 1 300); do touch %q; sleep 0.1; done ) & sleep 300`, marker)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runHarness(ctx, drain.Invocation{Path: "/bin/sh", Args: []string{"-c", script}},
			dir, filepath.Join(dir, "out.log"))
	}()

	// Wait for the grandchild to exist before cancelling, or the test proves nothing.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the grandchild never started; the fixture, not the code, is what this would " +
				"have been testing")
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("runHarness did not return after cancellation")
	}

	// Give anything that survived a clear chance to touch the file again.
	fi, err := os.Stat(marker)
	if err != nil {
		t.Fatalf("stat marker: %v", err)
	}
	before := fi.ModTime()
	time.Sleep(1500 * time.Millisecond)
	fi, err = os.Stat(marker)
	if err != nil {
		t.Fatalf("stat marker: %v", err)
	}
	if !fi.ModTime().Equal(before) {
		t.Errorf("the grandchild is still running %s after the harness was cancelled. It holds the "+
			"work item's worktree open and a valid session_secret, and drain will remove that "+
			"worktree at wrap while it is still writing into it", fi.ModTime().Sub(before))
	}
}
