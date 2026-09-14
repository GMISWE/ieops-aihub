package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

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

// TestClaimForDrain_ReportsAStructuralGapNotATransientOne pins the terminal state the unbuilt
// lifecycle seam produces.
//
// A run that could not claim anything used to end IDLE (exit 10), which `internal/drain/drain.go`
// documents as "work remains, waiting on my own work items; come back later" and which notifies
// nobody. Coming back later never helps: the capability does not exist. Wrapping the refusal in
// drain.ErrNotSupported is what makes the run end FAILED (exit 12) instead, i.e. "a human has to
// act", which is true.
//
// Mutant watched: dropping the ErrNotSupported wrap makes the claim classify as ResultClaimFailed
// and the run report IDLE again.
func TestClaimForDrain_ReportsAStructuralGapNotATransientOne(t *testing.T) {
	_, _, err := claimForDrain(context.Background(), "wi_x", "key")
	if err == nil {
		t.Fatal("the unbuilt claim seam returned no error")
	}
	if !errors.Is(err, drain.ErrNotSupported) {
		t.Fatalf("error %v does not wrap drain.ErrNotSupported, so the run would end IDLE "+
			"(exit 10, \"come back later\") for a capability that does not exist", err)
	}
	// The message has to name the work item and the fix; an operator reading it at 3am has
	// nothing else to go on.
	for _, needle := range []string{"wi_x", "--plan", "internal/mcp", "aihub#654"} {
		if !strings.Contains(err.Error(), needle) {
			t.Errorf("the refusal never mentions %q: %v", needle, err)
		}
	}
	// And the terminal state that error produces must be the one that summons a human.
	if got := drain.ExitCode(drain.Classify(true, drain.QueueState{Executable: 1})); got != 12 {
		t.Errorf("a run with a failed work item exits %d, want 12", got)
	}
}
