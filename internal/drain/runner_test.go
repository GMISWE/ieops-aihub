package drain

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/engine"
)

// ─── an isolated fake project ────────────────────────────────────────────────────────────────
//
// aihub#640's wi.content names the verification approach: "用带依赖链、并行根节点、资源冲突、派生 WI、
// 执行失败和空队列的隔离测试项目，检查实际派发顺序与服务端状态". This is that project. It is a fake rather
// than a real aihub project on purpose and the wi is explicit about why: draining the real
// project would claim and execute live work items.
//
// The fake models the parts of server state the scheduler actually reads: statuses, dependency
// edges, lock holders, and — the piece that makes the proliferation tests possible — work items
// that come into existence as a side effect of another work item's execution.

type fakeWI struct {
	Candidate
	status     string   // queued | running | blocked | paused | wrapped | failed
	blockedBy  []string // ids
	lockHolder string   // non-empty => claiming this returns ErrLockTaken
	// spawns are work items filed while THIS work item's steps run — a derived wi.
	spawns []fakeWI
	// failAtStep makes the named step's agent exit non-zero.
	failAtStep string
	// reviewVerdict, when set, is what a review step's agent prints.
	reviewVerdict string
	// pauseAtStep makes the named step's agent report a pause.
	pauseAtStep string
}

type fakeHub struct {
	mu    sync.Mutex
	wis   map[string]*fakeWI
	order []string // ids in claim order — the dispatch-order assertion

	steps      []string // step ids every work item runs
	stepCalls  []engine.StepCall
	completes  []string // "<id>:<status>"
	notes      []Notification
	dispatched []string // "<id>/<step>"
	cleaned    []string
	// claimAttempts counts EVERY call to claim(), successful or refused. It is what proves
	// "skip, never retry" rather than merely "never succeeded twice".
	claimAttempts map[string]int
}

func newFakeHub(steps []string, wis ...fakeWI) *fakeHub {
	h := &fakeHub{wis: map[string]*fakeWI{}, steps: steps, claimAttempts: map[string]int{}}
	for i := range wis {
		w := wis[i]
		if w.status == "" {
			w.status = "queued"
		}
		h.wis[w.ID] = &w
	}
	return h
}

func fwi(id, priority, created string) fakeWI {
	return fakeWI{Candidate: Candidate{ID: id, Slug: id, Priority: priority, CreatedAt: created, WIType: "feature"}}
}

// executable mirrors the server's ready predicate: queued, and no unfinished blocking dependency.
func (h *fakeHub) executable(context.Context) ([]Candidate, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Candidate
	for _, w := range h.wis {
		if w.status != "queued" || !h.unblockedLocked(w) {
			continue
		}
		out = append(out, w.Candidate)
	}
	return out, nil
}

func (h *fakeHub) unblockedLocked(w *fakeWI) bool {
	for _, b := range w.blockedBy {
		if dep, ok := h.wis[b]; ok && dep.status != "wrapped" && dep.status != "failed" {
			return false
		}
	}
	return true
}

func (h *fakeHub) allInScope(context.Context) ([]Candidate, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Candidate
	for _, w := range h.wis {
		if w.status == "wrapped" || w.status == "failed" {
			continue
		}
		out = append(out, w.Candidate)
	}
	return out, nil
}

func (h *fakeHub) observe(context.Context) (QueueState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var q QueueState
	inScope := map[string]bool{}
	for id, w := range h.wis {
		if w.status != "wrapped" && w.status != "failed" {
			inScope[id] = true
		}
	}
	for _, w := range h.wis {
		switch w.status {
		case "wrapped", "failed":
		case "running":
			q.Running++
		case "paused":
			q.Paused++
		case "queued":
			if h.unblockedLocked(w) {
				q.Executable++
			} else if out := h.outsidersLocked(w, inScope); len(out) > 0 {
				q.ExternallyBlocked = append(q.ExternallyBlocked,
					BlockedWorkItem{WorkItemID: w.ID, Slug: w.Slug, Blockers: out})
			} else {
				q.BlockedByMine++
			}
		case "blocked":
			if out := h.outsidersLocked(w, inScope); len(out) > 0 {
				q.ExternallyBlocked = append(q.ExternallyBlocked,
					BlockedWorkItem{WorkItemID: w.ID, Slug: w.Slug, Blockers: out})
			} else {
				q.BlockedByMine++
			}
		}
	}
	return q, nil
}

// outsidersLocked returns the blockers of w that are outside the scope, which is what the
// production ObserveQueue reports so the notification has somewhere to land.
func (h *fakeHub) outsidersLocked(w *fakeWI, inScope map[string]bool) []string {
	var out []string
	for _, b := range w.blockedBy {
		if !inScope[b] {
			out = append(out, b)
		}
	}
	return out
}

func (h *fakeHub) claim(_ context.Context, id, _ string) (*ClaimInfo, *Blocker, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.claimAttempts[id]++
	w, ok := h.wis[id]
	if !ok {
		return nil, nil, fmt.Errorf("no such work item %s", id)
	}
	if w.lockHolder != "" {
		return nil, &Blocker{WorkItem: w.lockHolder, Actor: "someone.else", Resource: "internal/x.go"},
			fmt.Errorf("%w: held by %s", ErrLockTaken, w.lockHolder)
	}
	if w.status != "queued" {
		return nil, nil, fmt.Errorf("work item %s is %s, not queued", id, w.status)
	}
	w.status = "running"
	h.order = append(h.order, id)
	return &ClaimInfo{WorkItemID: id, AttemptID: "ra_" + id, WIType: w.WIType,
		WorktreeRoot: "/tmp/wt/" + id, Worktrees: map[string]string{"repo": "/tmp/wt/" + id}}, nil, nil
}

func (h *fakeHub) startup(_ context.Context, c ClaimInfo) ([]StepSpec, error) {
	out := make([]StepSpec, 0, len(h.steps))
	for _, s := range h.steps {
		out = append(out, StepSpec{ID: s, Expanded: "do " + s})
	}
	return out, nil
}

func (h *fakeHub) dispatch(_ context.Context, req DispatchRequest) (DispatchResult, error) {
	h.mu.Lock()
	w := h.wis[req.Claim.WorkItemID]
	h.dispatched = append(h.dispatched, req.Claim.WorkItemID+"/"+req.Step.ID)
	// A derived work item is filed the moment the work item's first step runs, which is what
	// makes it "created during this round".
	if req.Index == 1 {
		for i := range w.spawns {
			s := w.spawns[i]
			if s.status == "" {
				s.status = "queued"
			}
			h.wis[s.ID] = &s
		}
		w.spawns = nil
	}
	h.mu.Unlock()

	switch {
	case w.failAtStep == req.Step.ID:
		return DispatchResult{Output: "the build broke\n"}, fmt.Errorf("exit status 1")
	case w.pauseAtStep == req.Step.ID:
		return DispatchResult{Output: "I called pf_pause_attempt because I need a human.\n"}, nil
	case engine.IsReviewStep(req.Step.ID) && w.reviewVerdict != "":
		return DispatchResult{Output: fmt.Sprintf("reviewed it\n<!-- REVIEW_RESULT: %s -->\nreview done\n", w.reviewVerdict)}, nil
	default:
		return DispatchResult{Output: "did the thing\nsummary of " + req.Step.ID + "\n"}, nil
	}
}

func (h *fakeHub) updateStep(_ context.Context, _ string, call engine.StepCall) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stepCalls = append(h.stepCalls, call)
	return nil
}

func (h *fakeHub) completeAttempt(_ context.Context, id, status, _ string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.completes = append(h.completes, id+":"+status)
	if w, ok := h.wis[id]; ok {
		w.status = status
	}
	return nil
}

// notify mirrors the PRODUCTION seam, including its early return on an empty WorkItemID.
//
// That guard is the whole reason this comment exists. The fake used to append unconditionally,
// so a notification production would have dropped on the floor was recorded here as delivered,
// and the assertion "a note was recorded" passed while the real run notified nobody. A fake that
// is more permissive than the thing it stands in for does not test that thing.
func (h *fakeHub) notify(_ context.Context, n Notification) error {
	if n.WorkItemID == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notes = append(h.notes, n)
	return nil
}

func (h *fakeHub) cleanup(_ context.Context, c ClaimInfo) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cleaned = append(h.cleaned, c.WorkItemID)
	return nil
}

// runnerFor wires a Runner against the fake. Parallelism defaults to 1 so dispatch order is
// deterministic and can be asserted; tests that care about concurrency set it themselves.
func runnerFor(h *fakeHub, b Budget) *Runner {
	if b.MaxParallel == 0 {
		b.MaxParallel = 1
	}
	// Atomic, not a plain `n++`: Runner calls NewULID from every worker goroutine, so an
	// unsynchronised counter here is a data race in the TEST that `-race` reports against
	// production line numbers. It is also not merely a test artifact — it says the real
	// NewULID must be safe to call concurrently, which is why the production one (newULID in
	// internal/cli) carries an atomic counter too.
	var n atomic.Int64
	return &Runner{
		Project:  "testproj",
		Scope:    Scope{UserID: "u_me"},
		Budget:   b,
		RunDir:   "/tmp/drain-test",
		Channels: []Channel{{Harness: HarnessClaude}},
		Now:      time.Now,
		NewULID:  func() string { return fmt.Sprintf("sa_%d", n.Add(1)) },

		Executable:      h.executable,
		AllInScope:      h.allInScope,
		ObserveQueue:    h.observe,
		Claim:           h.claim,
		UpdateStep:      h.updateStep,
		CompleteAttempt: h.completeAttempt,
		Notify:          h.notify,

		Startup:     h.startup,
		ResolveRole: func(sid string) (string, bool, error) { return "executor", false, nil },
		Dispatch:    h.dispatch,
		Cleanup:     h.cleanup,
	}
}

// ─── dispatch order ───────────────────────────────────────────────────────────────────────────

// TestRun_DispatchOrderRespectsPriorityAndDependencyChains is the wi's headline verification:
// "检查实际派发顺序与服务端状态", over a project with parallel roots AND a dependency chain.
//
// Shape:
//
//	root-urgent (urgent)        parallel root
//	root-normal (normal)        parallel root, blocks chain-mid
//	  chain-mid  (high)  blocked_by root-normal
//	    chain-end (urgent) blocked_by chain-mid
//
// The expected order is the assertion. chain-mid is `high` and chain-end is `urgent` — both
// outrank root-normal — so any implementation that ordered by priority WITHOUT honouring
// dependency readiness would claim them first. They are not claimable until their blocker
// wraps, so they can only appear in later rounds, and `urgent` chain-end must still come last.
func TestRun_DispatchOrderRespectsPriorityAndDependencyChains(t *testing.T) {
	h := newFakeHub([]string{"spec", "code_change"},
		fwi("root-urgent", "urgent", "2026-01-02T00:00:00Z"),
		fwi("root-normal", "normal", "2026-01-01T00:00:00Z"),
		func() fakeWI {
			w := fwi("chain-mid", "high", "2026-01-01T00:00:00Z")
			w.blockedBy = []string{"root-normal"}
			return w
		}(),
		func() fakeWI {
			w := fwi("chain-end", "urgent", "2026-01-01T00:00:00Z")
			w.blockedBy = []string{"chain-mid"}
			return w
		}(),
	)

	report, err := runnerFor(h, Budget{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{"root-urgent", "root-normal", "chain-mid", "chain-end"}
	if !reflect.DeepEqual(h.order, want) {
		t.Fatalf("claim order = %v\nwant             %v", h.order, want)
	}
	if report.Terminal != TerminalCompleted {
		t.Errorf("terminal = %s, want COMPLETED (every work item wrapped)", report.Terminal)
	}
	if report.Totals.Wrapped != 4 {
		t.Errorf("wrapped = %d, want 4", report.Totals.Wrapped)
	}
	// Dependency readiness is the server's predicate; a blocked work item must never even be
	// attempted, not merely fail after being claimed.
	for _, w := range h.wis {
		if w.status != "wrapped" {
			t.Errorf("%s ended %s, want wrapped", w.ID, w.status)
		}
	}
}

// TestRun_EmptyQueueIsCompletedWithoutClaimingAnything covers the "空队列" case from the wi's
// verification list, and the negative control that goes with it: COMPLETED must be reachable
// only when nothing at all was left.
func TestRun_EmptyQueueIsCompletedWithoutClaimingAnything(t *testing.T) {
	h := newFakeHub([]string{"code_change"})
	report, err := runnerFor(h, Budget{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Terminal != TerminalCompleted {
		t.Fatalf("terminal = %s, want COMPLETED", report.Terminal)
	}
	if report.StopReason != StopQueueDrained {
		t.Errorf("stop reason = %s, want queue_drained", report.StopReason)
	}
	if len(h.order) != 0 {
		t.Errorf("claimed %v on an empty queue", h.order)
	}
	if ExitCode(report.Terminal) != 0 {
		t.Errorf("an empty, complete project exited %d, want 0", ExitCode(report.Terminal))
	}
}

// ─── the two kinds of blocking ────────────────────────────────────────────────────────────────

// TestRun_LockConflictIsSkippedAndNotRetried pins aihub#640 `two_kinds_of_blocking`. A lock
// conflict is ordinary control flow, the policy is SKIP rather than retry ("等待是死锁的温床"),
// and the notification design's "更漂亮的一手" says to leave a note on the BLOCKER's work item so
// the holder learns somebody is waiting.
//
// Mutants watched: turning the ErrLockTaken branch into a retry loop makes this hang;
// reclassifying it as ResultFailed turns the terminal assertion red (FAILED instead of IDLE).
func TestRun_LockConflictIsSkippedAndNotRetried(t *testing.T) {
	locked := fwi("locked", "urgent", "2026-01-01T00:00:00Z")
	locked.lockHolder = "someone-elses-wi"
	h := newFakeHub([]string{"code_change"}, locked, fwi("free", "normal", "2026-01-02T00:00:00Z"))

	report, err := runnerFor(h, Budget{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := h.claimAttempts["locked"]; got != 1 {
		t.Fatalf("the locked work item was attempted %d times, want exactly 1 — a scheduler that "+
			"retries a lock it cannot get is building a deadlock", got)
	}
	if report.Totals.LockBlocked != 1 {
		t.Errorf("lock_blocked = %d, want 1", report.Totals.LockBlocked)
	}
	if report.Totals.Wrapped != 1 {
		t.Errorf("wrapped = %d, want 1 (the unlocked work item must still run)", report.Totals.Wrapped)
	}
	if report.Terminal == TerminalFailed {
		t.Error("a lock race produced FAILED; losing a race is not a defect and must not " +
			"summon a human")
	}

	var told bool
	for _, n := range h.notes {
		if n.WorkItemID == "someone-elses-wi" && strings.Contains(n.Note, "locked") {
			told = true
		}
	}
	if !told {
		t.Errorf("no note was left on the blocker's work item; notes=%+v.\n"+
			"In an unattended run that note is the only channel that reaches another human", h.notes)
	}
}

// TestRun_BlockedExternalIsDistinguishedFromIdle is the four-terminal-state distinction the
// design calls the whole point: both mean "work is left", and they differ only in whether waiting
// accomplishes anything.
func TestRun_BlockedExternalIsDistinguishedFromIdle(t *testing.T) {
	t.Run("blocked by a work item outside the scope", func(t *testing.T) {
		w := fwi("mine", "normal", "2026-01-01T00:00:00Z")
		w.status = "blocked"
		w.blockedBy = []string{"not-in-my-scope"} // no such work item in the fake => outside
		h := newFakeHub([]string{"code_change"}, w)

		report, err := runnerFor(h, Budget{}).Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if report.Terminal != TerminalBlockedExternal {
			t.Fatalf("terminal = %s, want BLOCKED_EXTERNAL: waiting cannot help", report.Terminal)
		}
		if len(h.notes) == 0 {
			t.Error("BLOCKED_EXTERNAL recorded no note; it is the state that is supposed to notify")
		}
	})

	t.Run("blocked by one of my own work items", func(t *testing.T) {
		blocker := fwi("mine-blocker", "normal", "2026-01-01T00:00:00Z")
		blocker.status = "paused" // in scope, not terminal, and drain never resumes it
		blocked := fwi("mine-blocked", "normal", "2026-01-02T00:00:00Z")
		blocked.status = "blocked"
		blocked.blockedBy = []string{"mine-blocker"}
		h := newFakeHub([]string{"code_change"}, blocker, blocked)

		report, err := runnerFor(h, Budget{}).Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if report.Terminal != TerminalIdle {
			t.Fatalf("terminal = %s, want IDLE: the blocker is mine, so coming back later helps "+
				"and nobody needs to be bothered", report.Terminal)
		}
	})
}

// ─── proliferation and convergence ────────────────────────────────────────────────────────────

// TestRun_DerivedWorkItemsWaitForTheNextRound pins the proliferation budget: "本轮新建的 wi 不进
// 本轮队列". The round's candidate set is frozen before anything executes, so a follow-up filed by
// round 1's own execution is first considered in round 2 — where the divergence detector has
// already had its look.
//
// Mutant watched: re-listing candidates mid-round instead of using the frozen set makes `derived`
// run in round 1, which this asserts against directly.
func TestRun_DerivedWorkItemsWaitForTheNextRound(t *testing.T) {
	parent := fwi("parent", "normal", "2026-01-01T00:00:00Z")
	parent.spawns = []fakeWI{fwi("derived", "urgent", "2026-01-01T00:00:00Z")}
	// A second root so round 1 has two work items and the derived one could, if the freeze
	// leaked, be picked up as the round's third — and being `urgent` it would jump to the
	// front of any re-sorted set.
	h := newFakeHub([]string{"code_change"}, parent, fwi("sibling", "normal", "2026-01-02T00:00:00Z"))

	report, err := runnerFor(h, Budget{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{"parent", "sibling", "derived"}
	if !reflect.DeepEqual(h.order, want) {
		t.Fatalf("claim order = %v\nwant             %v\n"+
			"`derived` is urgent; appearing before `sibling` would mean the round's frozen set leaked",
			h.order, want)
	}
	if report.Totals.Created < 1 {
		t.Errorf("created = %d, want at least 1: the derived work item was never counted, so the "+
			"divergence detector is blind to proliferation", report.Totals.Created)
	}
}

// TestRun_DivergenceStopsTheLoop is the runaway-queue guard end to end. Every work item files two
// follow-ups, each of which files two more: without the detector this project never drains.
//
// Mutant watched: deleting the Diverging() check makes this test run until the round budget, and
// with no budget it does not terminate at all.
func TestRun_DivergenceStopsTheLoop(t *testing.T) {
	h := newFakeHub([]string{"code_change"})
	// A generation counter keeps ids unique so the fake can keep spawning forever.
	// RECURSIVE, so every generation proliferates. The first version of this fixture filled
	// .spawns with plain fwi() values, whose own .spawns were empty, so exactly one generation
	// multiplied and the project drained on its own in two rounds. The test still went red when
	// the detector was deleted, so it was not vacuous — but its comment, its MaxRounds backstop
	// and its timeout were all defending against a runaway the fixture could not produce.
	gen := 0
	var spawn func(id string, depth int) fakeWI
	spawn = func(id string, depth int) fakeWI {
		w := fwi(id, "normal", "2026-01-01T00:00:00Z")
		if depth == 0 {
			return w
		}
		gen++
		w.spawns = []fakeWI{
			spawn(fmt.Sprintf("%s-a%d", id, gen), depth-1),
			spawn(fmt.Sprintf("%s-b%d", id, gen), depth-1),
		}
		return w
	}
	root := spawn("root", 8) // 2^8 descendants if nothing stops it
	h.wis["root"] = &root
	h.wis["root"].status = "queued"

	// The result is carried out of the goroutine rather than asserted inside it: on the timeout
	// branch below the parent t.Fatal's while this goroutine is still live, and a t.Errorf after
	// that panics with "Log in goroutine after test has completed".
	type res struct {
		rep RunReport
		err error
	}
	done := make(chan res, 1)
	go func() {
		// A round budget is a backstop only: if divergence detection works, it stops first.
		rep, err := runnerFor(h, Budget{MaxRounds: 25}).Run(context.Background())
		done <- res{rep, err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Run: %v", got.err)
		}
		report := got.rep
		if report.StopReason != StopDivergence {
			t.Fatalf("stop reason = %s, want divergence. A project where every work item files "+
				"two follow-ups must be caught by the detector, not by the round budget", report.StopReason)
		}
		if report.Totals.Rounds > 3 {
			t.Errorf("took %d rounds to notice divergence; the detector runs every round",
				report.Totals.Rounds)
		}
		// Self-check on the fixture, so its depth is load-bearing rather than decorative.
		// The first version filled .spawns with childless values, so exactly one generation
		// multiplied and this project would have drained on its own — the comment above, the
		// MaxRounds backstop and the timeout were all guarding a runaway it could not produce.
		h.mu.Lock()
		var grandparents int
		for id, w := range h.wis {
			if id != "root" && len(w.spawns) > 0 {
				grandparents++
			}
		}
		h.mu.Unlock()
		if grandparents == 0 {
			t.Error("the fixture cannot proliferate past one generation, so it does not " +
				"contain the runaway this test claims to stop")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not terminate on a diverging project")
	}
}

// TestRun_BudgetsStopTheRun covers the two remaining stop conditions ("跑满 N 个 / T 时间" — the
// count half; the time half is the caller's context, exercised by the cancellation test).
func TestRun_BudgetsStopTheRun(t *testing.T) {
	mk := func() *fakeHub {
		return newFakeHub([]string{"code_change"},
			fwi("a", "normal", "2026-01-01T00:00:00Z"),
			fwi("b", "normal", "2026-01-02T00:00:00Z"),
			fwi("c", "normal", "2026-01-03T00:00:00Z"))
	}

	t.Run("max-work-items", func(t *testing.T) {
		h := mk()
		report, err := runnerFor(h, Budget{MaxWorkItems: 2}).Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(h.order) != 2 {
			t.Fatalf("claimed %v, want exactly 2 under --max-work-items=2", h.order)
		}
		if report.StopReason != StopMaxWorkItems {
			t.Errorf("stop reason = %s, want max_work_items", report.StopReason)
		}
		if report.Terminal != TerminalIdle {
			t.Errorf("terminal = %s, want IDLE: one work item is still executable", report.Terminal)
		}
	})

	t.Run("max-rounds", func(t *testing.T) {
		h := mk()
		report, err := runnerFor(h, Budget{MaxRounds: 1, MaxParallel: 1}).Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if report.Totals.Rounds != 1 {
			t.Fatalf("ran %d rounds under --max-rounds=1", report.Totals.Rounds)
		}
		if report.StopReason != StopMaxRounds {
			t.Errorf("stop reason = %s, want max_rounds", report.StopReason)
		}
	})
}

// TestRun_CancellationMidStepLeavesTheAttemptForResumption pins what a Ctrl-C must and must not
// do, and it cancels MID-STEP rather than before the run.
//
// The previous version cancelled the context before calling Run, so Run broke at the
// top-of-loop ctx.Err() guard and nothing was ever claimed. Its assertion then looped over an
// EMPTY h.completes: the property it named ("a cancelled run must not complete the attempt as
// failed") could not fail, and the two code paths that actually implement it had zero coverage.
//
// Cancelling from inside Dispatch exercises the real path, and the assertions are the three
// things that go wrong when it is not handled: the run must still produce a terminal state
// (rather than an error, which the CLI reports as exit 2 "internal error" for an ordinary
// interrupt), the attempt must not be completed, and the outcome must be reported as CANCELLED
// rather than as a pause nobody performed.
func TestRun_CancellationMidStepLeavesTheAttemptForResumption(t *testing.T) {
	h := newFakeHub([]string{"code_change", "commit_and_pr"},
		fwi("a", "normal", "2026-01-01T00:00:00Z"))
	ctx, cancel := context.WithCancel(context.Background())

	r := runnerFor(h, Budget{})
	inner := h.dispatch
	r.Dispatch = func(dctx context.Context, req DispatchRequest) (DispatchResult, error) {
		out, err := inner(dctx, req)
		cancel() // the interrupt lands while this work item is between steps
		return out, err
	}
	// Every server call after the cancellation fails the way a real client would.
	wrapCtx := func(fn func(context.Context) ([]Candidate, error)) func(context.Context) ([]Candidate, error) {
		return func(c context.Context) ([]Candidate, error) {
			if c.Err() != nil {
				return nil, c.Err()
			}
			return fn(c)
		}
	}
	r.AllInScope = wrapCtx(h.allInScope)
	r.Executable = wrapCtx(h.executable)

	report, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("Run returned an error for an ordinary cancellation: %v.\n"+
			"The CLI reports that as exit 2 (internal error), and finish() never runs, so the "+
			"snapshot is left Finished=false and watch renders it as DIED.", err)
	}
	if report.StopReason != StopCancelled {
		t.Errorf("stop reason = %s, want cancelled", report.StopReason)
	}
	if report.Terminal == "" {
		t.Fatal("a cancelled run produced no terminal state, so there is no exit code to report")
	}

	for _, c := range h.completes {
		if strings.HasPrefix(c, "a:") {
			t.Errorf("cancellation completed the attempt (%s); nothing failed, and the work "+
				"item should be resumable", c)
		}
	}
	if report.Totals.Paused != 0 {
		t.Errorf("paused = %d: an abandoned attempt was reported as a pause, which says a "+
			"person decided something when nobody did", report.Totals.Paused)
	}
	if report.Totals.Cancelled != 1 {
		t.Errorf("cancelled = %d, want 1; outcomes=%+v", report.Totals.Cancelled, report.Outcomes)
	}
}

// TestRun_CancellationBeforeTheFirstRoundStillProducesATerminalState covers the other entry
// point: the interrupt that lands before anything is claimed.
func TestRun_CancellationBeforeTheFirstRoundStillProducesATerminalState(t *testing.T) {
	h := newFakeHub([]string{"code_change"}, fwi("a", "normal", "2026-01-01T00:00:00Z"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report, err := runnerFor(h, Budget{}).Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.StopReason != StopCancelled {
		t.Fatalf("stop reason = %s, want cancelled", report.StopReason)
	}
	if len(h.order) != 0 {
		t.Errorf("claimed %v after the run was already cancelled", h.order)
	}
}

// ─── per-work-item execution ──────────────────────────────────────────────────────────────────

// TestRun_ReviewFailMakesBothCallsAndStops pins engine-native-details.md §0c verbatim: on a
// review FAIL the loop makes the pf_update_step(failed) call AND pf_complete_attempt(failed), in
// that order, then stops. The document is explicit that either one alone is a broken state —
// "pf_update_step(failed) alone leaves the attempt running; pf_complete_attempt(failed) alone
// leaves the step showing in_progress forever" — so both halves are asserted.
func TestRun_ReviewFailMakesBothCallsAndStops(t *testing.T) {
	w := fwi("reviewed", "normal", "2026-01-01T00:00:00Z")
	w.reviewVerdict = "FAIL"
	h := newFakeHub([]string{"code_change", "code_review", "commit_and_pr"}, w)

	report, err := runnerFor(h, Budget{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if report.Terminal != TerminalFailed {
		t.Fatalf("terminal = %s, want FAILED", report.Terminal)
	}
	var failedStep *engine.StepCall
	for i, c := range h.stepCalls {
		if c.Status == "failed" {
			failedStep = &h.stepCalls[i]
		}
	}
	if failedStep == nil {
		t.Fatal("no pf_update_step(status=failed) call was made; the step would show in_progress forever")
	}
	if failedStep.StepID != "code_review" {
		t.Errorf("failed step = %s, want code_review", failedStep.StepID)
	}
	if failedStep.ErrorType != "review_fail" {
		t.Errorf("error_type = %q, want review_fail", failedStep.ErrorType)
	}
	if failedStep.NextStep != "" {
		t.Errorf("the failing call carried next_step=%q; next_step is not valid on a failure",
			failedStep.NextStep)
	}
	if want := "reviewed:failed"; !contains(h.completes, want) {
		t.Errorf("completes = %v, want %s; without it the attempt stays running", h.completes, want)
	}
	// The loop stops at the review: the step AFTER it must never be dispatched.
	if contains(h.dispatched, "reviewed/commit_and_pr") {
		t.Error("the step after a failing review was dispatched; the loop must stop at the FAIL")
	}
}

// TestRun_ReviewWarnContinues is the negative control for the FAIL path. engine.native.md says a
// WARN prints and continues; treating it as a failure would stop every run over an advisory.
func TestRun_ReviewWarnContinues(t *testing.T) {
	w := fwi("warned", "normal", "2026-01-01T00:00:00Z")
	w.reviewVerdict = "WARN"
	h := newFakeHub([]string{"code_change", "code_review", "commit_and_pr"}, w)

	r := runnerFor(h, Budget{})
	var logged []string
	r.Logf = func(format string, a ...any) { logged = append(logged, fmt.Sprintf(format, a...)) }

	report, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Terminal != TerminalCompleted {
		t.Fatalf("terminal = %s, want COMPLETED: a WARN continues", report.Terminal)
	}
	if !contains(h.dispatched, "warned/commit_and_pr") {
		t.Errorf("the step after a WARN was never dispatched: %v", h.dispatched)
	}

	// "print the warning and continue" (engine.native.md) is TWO obligations, and only the
	// second one is visible in the terminal state. A WARN that continues silently is a review
	// finding nobody will ever see — the run exits 0, the work item wraps, and the reviewer's
	// only output went to a log file in a directory no human was told to open.
	var warned bool
	for _, l := range logged {
		if strings.Contains(l, "WARN") && strings.Contains(l, "code_review") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("a review WARN was never reported; logged=%v", logged)
	}
}

// TestRun_PauseStopsWithoutCompletingTheAttempt pins engine-native-details.md §0e. A paused
// attempt must NOT be completed: the pause was a deliberate hand-off to a human, and completing
// it would overwrite exactly the state its author asked for.
//
// Mutant watched: calling CompleteAttempt on the pause path turns this red.
func TestRun_PauseStopsWithoutCompletingTheAttempt(t *testing.T) {
	w := fwi("paused-one", "normal", "2026-01-01T00:00:00Z")
	w.pauseAtStep = "code_change"
	h := newFakeHub([]string{"code_change", "commit_and_pr"}, w,
		fwi("other", "normal", "2026-01-02T00:00:00Z"))

	report, err := runnerFor(h, Budget{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, c := range h.completes {
		if strings.HasPrefix(c, "paused-one:") {
			t.Errorf("the paused work item's attempt was completed (%s); §0e forbids it", c)
		}
	}
	if contains(h.dispatched, "paused-one/commit_and_pr") {
		t.Error("the loop continued past a pause")
	}
	if report.Totals.Paused != 1 {
		t.Errorf("paused = %d, want 1", report.Totals.Paused)
	}
	// The rest of the round must still run: one work item pausing is not a reason to stop.
	if !contains(h.order, "other") {
		t.Error("a pause stopped the whole round; only that work item should stop")
	}
	if report.Terminal == TerminalFailed {
		t.Error("a pause produced FAILED; a pause is a hand-off, not a defect")
	}
}

// TestRun_StepFailureFailsTheAttemptAndYieldsFAILED covers the "执行失败" case and proves the
// failure survives to the exit code even though later work items succeed.
func TestRun_StepFailureFailsTheAttemptAndYieldsFAILED(t *testing.T) {
	bad := fwi("bad", "urgent", "2026-01-01T00:00:00Z")
	bad.failAtStep = "code_change"
	h := newFakeHub([]string{"code_change"}, bad, fwi("good", "normal", "2026-01-02T00:00:00Z"))

	report, err := runnerFor(h, Budget{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Terminal != TerminalFailed {
		t.Fatalf("terminal = %s, want FAILED", report.Terminal)
	}
	if ExitCode(report.Terminal) != 12 {
		t.Errorf("exit code = %d, want 12", ExitCode(report.Terminal))
	}
	if !contains(h.completes, "bad:failed") {
		t.Errorf("completes = %v, want bad:failed", h.completes)
	}
	if report.Totals.Wrapped != 1 {
		t.Errorf("wrapped = %d, want 1: one work item failing must not stop the others", report.Totals.Wrapped)
	}
	// Cumulative, not per-round: `good` wrapping after `bad` failed must not erase the failure.
	if report.Totals.Failed != 1 {
		t.Errorf("failed = %d, want 1", report.Totals.Failed)
	}
}

// TestRun_StepBracketThreadsAttemptIDsAndWrapsCleanly pins the happy path's call sequence against
// engine.PlanStepBracket's contract, including the trap lifecycle-details.md §1 names: the
// next step's attempt id must be threaded, or current_step_attempt is left NULL.
func TestRun_StepBracketThreadsAttemptIDsAndWrapsCleanly(t *testing.T) {
	h := newFakeHub([]string{"spec", "code_change", "commit_and_pr"},
		fwi("solo", "normal", "2026-01-01T00:00:00Z"))

	if _, err := runnerFor(h, Budget{}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(h.stepCalls) < 4 {
		t.Fatalf("only %d pf_update_step calls for a 3-step work item: %+v", len(h.stepCalls), h.stepCalls)
	}
	first := h.stepCalls[0]
	if first.StepID != "spec" || first.Status != "in_progress" {
		t.Errorf("first call = %+v, want spec/in_progress", first)
	}
	// Every completing call that names a next step must also carry that step's attempt id.
	for _, c := range h.stepCalls {
		if c.NextStep != "" && c.NextStepAttemptID == "" {
			t.Errorf("call %+v names next_step but no next_step_attempt_id; "+
				"lifecycle-details.md §1: current_step_attempt would be left NULL", c)
		}
		if c.Status == "completed" && c.ArtifactSummary == "" {
			t.Errorf("completed call %+v carries no artifact_summary", c)
		}
	}
	last := h.stepCalls[len(h.stepCalls)-1]
	if last.StepID != "commit_and_pr" || last.NextStep != "" {
		t.Errorf("last call = %+v, want commit_and_pr with no next_step", last)
	}
	if !contains(h.completes, "solo:wrapped") {
		t.Errorf("completes = %v, want solo:wrapped", h.completes)
	}
	if !contains(h.cleaned, "solo") {
		t.Errorf("worktrees were not cleaned for a wrapped work item: %v", h.cleaned)
	}
}

// TestRun_ConcurrencyIsBounded proves --max-parallel actually bounds anything. Without the
// semaphore this reports the full candidate count, and the measured concurrency ceiling stops
// being enforceable at all.
func TestRun_ConcurrencyIsBounded(t *testing.T) {
	var wis []fakeWI
	for i := 0; i < 12; i++ {
		wis = append(wis, fwi(fmt.Sprintf("w%02d", i), "normal", "2026-01-01T00:00:00Z"))
	}
	h := newFakeHub([]string{"code_change"}, wis...)

	var mu sync.Mutex
	cur, peak := 0, 0
	inner := h.dispatch
	r := runnerFor(h, Budget{MaxParallel: 3})
	r.Dispatch = func(ctx context.Context, req DispatchRequest) (DispatchResult, error) {
		mu.Lock()
		cur++
		if cur > peak {
			peak = cur
		}
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		out, err := inner(ctx, req)
		mu.Lock()
		cur--
		mu.Unlock()
		return out, err
	}

	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if peak > 3 {
		t.Fatalf("peak concurrency %d exceeded --max-parallel=3", peak)
	}
	if peak < 2 {
		t.Errorf("peak concurrency %d: the round ran serially, so this test proves nothing "+
			"about the bound", peak)
	}
	if len(h.order) != 12 {
		t.Errorf("claimed %d work items, want 12", len(h.order))
	}
}

// ─── unit-level branches ──────────────────────────────────────────────────────────────────────

// TestClassifyStepDispatch_ExitZeroWithNoOutputIsNotSuccess is the measured Claude Code hazard in
// unit form, and it is the single most important branch in this file.
//
// Measured 2026-09-14: `claude -p` with default permissions declines the tool call, prints a
// prose explanation, and EXITS 0 with empty stderr; `--permission-mode dontAsk` denies Bash the
// same way. A scheduler reading exit status alone marks those steps completed and wraps a work
// item that did nothing — indistinguishable from success in every record the run leaves behind.
//
// Mutant watched: deleting the empty-output branch makes the "exit 0, said nothing" case return
// StepOK, and the runner then files a completed step for work that never happened.
func TestClassifyStepDispatch_ExitZeroWithNoOutputIsNotSuccess(t *testing.T) {
	cases := []struct {
		name string
		out  DispatchResult
		err  error
		want StepVerdict
	}{
		{"ordinary success", DispatchResult{Output: "did it\nsummary\n"}, nil, StepOK},
		{"exit 0 but said nothing", DispatchResult{Output: ""}, nil, StepSilentRefusal},
		{"exit 0 but only whitespace", DispatchResult{Output: "  \n\t\n"}, nil, StepSilentRefusal},
		{"non-zero exit, ordinary failure", DispatchResult{Output: "--- FAIL: TestX\n"}, fmt.Errorf("exit status 1"), StepFailed},
		{"non-zero exit, 401", DispatchResult{Output: "Error: Unauthorized: Invalid API key: HTTP 401"}, fmt.Errorf("exit status 1"), StepAuthFailure},
		{"explicit auth sentinel", DispatchResult{Output: ""}, fmt.Errorf("%w: nope", ErrAuth), StepAuthFailure},
		// ExitErr carries the sentinel while err is nil. The outer branch used to consult
		// ExitErr and the inner one did not, so this classified as StepFailed — the opposite
		// of what the outer test had just detected — and the run blamed the work item for an
		// expired credential instead of falling to the next channel.
		{"auth sentinel in ExitErr only", DispatchResult{Output: "something unhelpful", ExitErr: fmt.Errorf("%w: nope", ErrAuth)}, nil, StepAuthFailure},
		// Negative control: a non-auth ExitErr with a nil err is an ordinary failure, not an
		// auth failure, and must not burn a healthy channel candidate.
		{"ordinary failure in ExitErr only", DispatchResult{Output: "--- FAIL: TestX", ExitErr: fmt.Errorf("exit status 1")}, nil, StepFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyStepDispatch(tc.out, tc.err); got != tc.want {
				t.Fatalf("ClassifyStepDispatch = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRun_AuthFailureFallsToTheNextChannel pins the run-time half of ops problem 2. The first
// channel 401s; the run must switch rather than blame the work item, and the work item must
// still wrap.
func TestRun_AuthFailureFallsToTheNextChannel(t *testing.T) {
	h := newFakeHub([]string{"code_change"}, fwi("a", "normal", "2026-01-01T00:00:00Z"))
	r := runnerFor(h, Budget{})
	r.Channels = []Channel{{Harness: HarnessCodex}, {Harness: HarnessClaude}}

	var used []Harness
	inner := h.dispatch
	r.Dispatch = func(ctx context.Context, req DispatchRequest) (DispatchResult, error) {
		used = append(used, req.Channel.Harness)
		if req.Channel.Harness == HarnessCodex {
			return DispatchResult{Output: "stream error: unexpected status 401 Unauthorized"},
				fmt.Errorf("exit status 1")
		}
		return inner(ctx, req)
	}

	report, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(used) < 2 || used[0] != HarnessCodex || used[1] != HarnessClaude {
		t.Fatalf("channels used = %v, want codex then a fallback to claude", used)
	}
	if report.Totals.Wrapped != 1 {
		t.Errorf("wrapped = %d, want 1: a credential problem on one channel is not the work "+
			"item's fault", report.Totals.Wrapped)
	}
	if report.Terminal != TerminalCompleted {
		t.Errorf("terminal = %s, want COMPLETED", report.Terminal)
	}
}

// TestRun_OrdinaryStepFailureDoesNotBurnAChannel is the negative control for the fallback: only a
// credential problem may demote a channel. Demoting on every failed test run would exhaust the
// candidate list and then report the wrong reason.
func TestRun_OrdinaryStepFailureDoesNotBurnAChannel(t *testing.T) {
	bad := fwi("bad", "normal", "2026-01-01T00:00:00Z")
	bad.failAtStep = "code_change"
	h := newFakeHub([]string{"code_change"}, bad)
	r := runnerFor(h, Budget{})
	r.Channels = []Channel{{Harness: HarnessCodex}, {Harness: HarnessClaude}}

	var used []Harness
	inner := h.dispatch
	r.Dispatch = func(ctx context.Context, req DispatchRequest) (DispatchResult, error) {
		used = append(used, req.Channel.Harness)
		return inner(ctx, req)
	}
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, h := range used {
		if h != HarnessCodex {
			t.Fatalf("channels used = %v: an ordinary step failure demoted the channel", used)
		}
	}
}

// TestDetectPause_MatchesBothRoutes pins §0e's two discovery paths: the agent saying it called
// pf_pause_attempt, and a pf_* call being refused with "attempt is paused".
func TestDetectPause_MatchesBothRoutes(t *testing.T) {
	for _, out := range []string{
		"I called pf_pause_attempt to hand this to a human.",
		"the server said: 409 ATTEMPT_PAUSED",
		"pf_update_step failed: attempt is paused",
	} {
		if !DetectPause(out) {
			t.Errorf("pause not detected in %q; the loop would keep dispatching into a paused attempt", out)
		}
	}
	// Negative control: ordinary output must not be read as a pause, which would silently
	// abandon a healthy work item mid-run.
	for _, out := range []string{
		"ran the tests, all green",
		"the attempt is progressing nicely",
		"I paused to think about the design",
	} {
		if DetectPause(out) {
			t.Errorf("ordinary output read as a pause: %q", out)
		}
	}
}

// TestSummaryLine_TakesTheLastLine pins the summary extraction. Agents narrate first and conclude
// last, so the head is the opening of the reasoning and the tail is the answer.
func TestSummaryLine_TakesTheLastLine(t *testing.T) {
	got := SummaryLine("Let me start by reading the file.\nI made the change.\nAdded the drain scheduler.\n")
	if got != "Added the drain scheduler." {
		t.Fatalf("SummaryLine = %q, want the last line", got)
	}
	if got := SummaryLine("   \n\n"); got == "" {
		t.Error("an empty output produced an empty summary; pf_update_step would file nothing")
	}
	// A long multi-byte summary must be cut on a rune boundary, not a byte one: artifact
	// summaries in this project are routinely Chinese, and a byte slice would file invalid UTF-8.
	long := strings.Repeat("测", 900)
	if s := SummaryLine(long); !utf8Valid(s) {
		t.Error("SummaryLine truncated a multi-byte summary mid-rune, producing invalid UTF-8")
	}
}

// TestStepAgentPrompt_CarriesTheCompletedStepsRule pins the workflow_identity_constraint at the
// one place A could most easily drift from B/C: the instructions handed to the step agent.
//
// The completed_steps paragraph in particular is load-bearing behaviour, not prose — it is what
// stops a resumed work item from redoing finished steps — so an A-mode paraphrase that dropped
// it would be a silent behavioural fork of exactly the kind the constraint forbids.
func TestStepAgentPrompt_CarriesTheCompletedStepsRule(t *testing.T) {
	raw := StepAgentPrompt("wi_abc", "code_change", "EXPANDED-STEP-BODY")
	// Compare with runs of whitespace collapsed. §0b is a wrapped markdown block, so several
	// of its load-bearing sentences straddle a newline ("Never take step\nprogress from a file
	// …"); asserting on the raw text would pin the WRAPPING as well as the words, and a
	// re-wrap that changed nothing would go red while a reworded sentence that changed the
	// instruction would not.
	p := strings.Join(strings.Fields(raw), " ")

	for _, needle := range []string{
		"You are executing step code_change of wi wi_abc.",
		"Call pf_get_step(work_item_id=wi_abc) FIRST",
		`count only entries whose status is "completed" as done`,
		"Never take step progress from a file in the worktree",
		"--- step instructions ---",
		"EXPANDED-STEP-BODY",
		"--- END ---",
		"RETURN your one-line summary",
		"call pf_remember",
	} {
		if !strings.Contains(p, needle) {
			t.Errorf("the step agent prompt dropped §0b's %q.\nA and B/C would then give their "+
				"step agents different instructions, which is a behavioural fork.", needle)
		}
	}

	// Negative control: the prompt must NOT carry a model instruction. aihub#555 measured an
	// explicit model silently overriding the agent file's frontmatter, which is where the
	// role's tier lives.
	for _, forbidden := range []string{"model:", "subagent_type", "opus", "sonnet"} {
		if strings.Contains(p, forbidden) {
			t.Errorf("the prompt carries %q; the model must come from the role/agent file, "+
				"never from the dispatch", forbidden)
		}
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────────────────────

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

// ─── regressions from the clean-context review ────────────────────────────────────────────────

// TestRun_BlockedExternalNotifiesBothHalves is the regression test for a notification that the
// suite used to certify as working while production dropped it on the floor.
//
// notifyExternalBlocks built its Notification with NO WorkItemID, and the production Notify seam
// returns early when the target is empty (a note needs a timeline to land on), so nothing was
// ever written. The old fake appended unconditionally, so "a note was recorded" passed. The fake
// now mirrors the production guard, and this asserts BOTH halves the ruling asks for: a note on
// my blocked work item, and one on the blocker so its holder learns somebody is waiting.
func TestRun_BlockedExternalNotifiesBothHalves(t *testing.T) {
	w := fwi("mine", "normal", "2026-01-01T00:00:00Z")
	w.status = "blocked"
	w.blockedBy = []string{"someone-elses-wi"}
	h := newFakeHub([]string{"code_change"}, w)

	report, err := runnerFor(h, Budget{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Terminal != TerminalBlockedExternal {
		t.Fatalf("terminal = %s, want BLOCKED_EXTERNAL", report.Terminal)
	}

	var toldMine, toldBlocker bool
	for _, n := range h.notes {
		switch n.WorkItemID {
		case "mine":
			toldMine = true
		case "someone-elses-wi":
			toldBlocker = true
		}
	}
	if !toldMine {
		t.Errorf("no note on my own blocked work item; notes=%+v", h.notes)
	}
	if !toldBlocker {
		t.Errorf("no note on the blocker; in an unattended run that is the only channel that "+
			"reaches another person. notes=%+v", h.notes)
	}
	if len(h.notes) == 0 {
		t.Error("BLOCKED_EXTERNAL notified nobody, which is the one thing this state is for")
	}
}

// TestRun_FailedWorkItemsDoNotStayInFlight covers the `defer r.clearActive` fix. Seven of the
// eight exits from executeWorkItem used to leave the work item in Snapshot.Active for the rest
// of the run, so `polyforge watch` showed phantom "in flight" rows whose age only grew, on the
// one screen this feature exists to make trustworthy.
func TestRun_FailedWorkItemsDoNotStayInFlight(t *testing.T) {
	bad := fwi("bad", "normal", "2026-01-01T00:00:00Z")
	bad.failAtStep = "code_change"
	paused := fwi("paused", "normal", "2026-01-02T00:00:00Z")
	paused.pauseAtStep = "code_change"
	reviewed := fwi("reviewed", "normal", "2026-01-03T00:00:00Z")
	reviewed.reviewVerdict = "FAIL"
	h := newFakeHub([]string{"code_change", "code_review"}, bad, paused, reviewed)

	r := runnerFor(h, Budget{MaxParallel: 1})

	// The invariant, checked on EVERY published snapshot: once a work item has an Outcome it
	// must never appear as "in flight" again.
	//
	// Asserting on the FINAL snapshot proves nothing, and that is how the first version of
	// this test passed against the bug: finish() sets Active to an empty slice unconditionally,
	// so the last frame is clean whether or not anything was ever cleared. What a reader of
	// `polyforge watch` actually sees is the frames in between.
	var violations []string
	r.Publish = func(s *Snapshot) {
		done := map[string]bool{}
		for _, o := range s.Recent {
			done[o.Candidate.ID] = true
		}
		for _, a := range s.Active {
			if done[a.Candidate.ID] {
				violations = append(violations, fmt.Sprintf(
					"%s is in Recent (%s) and STILL in Active at step %s",
					a.Candidate.Slug, "finished", a.StepID))
			}
		}
	}

	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("work items stayed in flight after finishing:\n  %s\n"+
			"watch would show each one forever, with a step age that only grows.",
			strings.Join(violations, "\n  "))
	}
}

// TestRun_SkippedWorkItemsDoNotSpendTheWorkItemBudget pins the budget's documented meaning:
// MaxWorkItems "caps how many work items are EXECUTED". A lock-blocked work item was never
// executed, and charging it let two contended work items exhaust `--max-work-items=2` having run
// nothing, then report stop_reason=max_work_items as though the cap had done its job.
func TestRun_SkippedWorkItemsDoNotSpendTheWorkItemBudget(t *testing.T) {
	l1 := fwi("locked-1", "urgent", "2026-01-01T00:00:00Z")
	l1.lockHolder = "other-1"
	l2 := fwi("locked-2", "urgent", "2026-01-02T00:00:00Z")
	l2.lockHolder = "other-2"
	h := newFakeHub([]string{"code_change"}, l1, l2,
		fwi("free-1", "normal", "2026-01-03T00:00:00Z"),
		fwi("free-2", "normal", "2026-01-04T00:00:00Z"))

	report, err := runnerFor(h, Budget{MaxWorkItems: 2}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Totals.Wrapped != 2 {
		t.Fatalf("wrapped = %d, want 2: the two lock-blocked work items spent a budget meant "+
			"for executed work (totals=%+v)", report.Totals.Wrapped, report.Totals)
	}
	if !contains(h.order, "free-1") || !contains(h.order, "free-2") {
		t.Errorf("claim order = %v, want both runnable work items", h.order)
	}
}

// TestRun_ARoundThatCouldClaimNothingSaysSo covers the stop reason. "queue_drained" is what a
// genuinely empty queue reports; a round where every candidate was held by another attempt left
// the work exactly where it was, and reporting the routine ending for it hides the one fact an
// operator needs.
func TestRun_ARoundThatCouldClaimNothingSaysSo(t *testing.T) {
	a := fwi("a", "normal", "2026-01-01T00:00:00Z")
	a.lockHolder = "other"
	h := newFakeHub([]string{"code_change"}, a)

	report, err := runnerFor(h, Budget{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.StopReason != StopNothingClaimable {
		t.Fatalf("stop reason = %s, want nothing_claimable: the queue was not drained, it was "+
			"untouchable", report.StopReason)
	}
	if report.Terminal != TerminalIdle {
		t.Errorf("terminal = %s, want IDLE: the work is still there", report.Terminal)
	}
}

// TestRun_APauseIsConfirmedWithTheServer is the regression test for an unanchored substring scan
// that could strand a claimed work item permanently.
//
// DetectPause matches "attempt is paused" / "pf_pause_attempt" / "attempt_paused" ANYWHERE in the
// step agent's combined output, and this repository contains ATTEMPT_PAUSED in fourteen Go files
// — so a healthy `code_change` step that greps or tests over them trips it. On a false positive
// the loop returns without completing the attempt (correct for a REAL pause), leaving it running
// with its locks held by a process that has exited, recoverable only by pf_force_takeover.
func TestRun_APauseIsConfirmedWithTheServer(t *testing.T) {
	t.Run("a false positive does not strand the work item", func(t *testing.T) {
		h := newFakeHub([]string{"code_change", "commit_and_pr"},
			fwi("greps", "normal", "2026-01-01T00:00:00Z"))
		r := runnerFor(h, Budget{})
		inner := h.dispatch
		r.Dispatch = func(ctx context.Context, req DispatchRequest) (DispatchResult, error) {
			out, err := inner(ctx, req)
			if req.Step.ID == "code_change" {
				// Exactly what a step that greps this repo prints.
				out.Output += "internal/domain/errors.go:92: ErrAttemptPaused ErrCode = \"ATTEMPT_PAUSED\"\n" +
					"ran the tests, all green\n"
			}
			return out, err
		}
		// The server is the authority and says this attempt is not paused.
		r.AttemptPaused = func(context.Context, string) (bool, error) { return false, nil }

		report, err := r.Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if report.Totals.Paused != 0 {
			t.Fatalf("a step that merely MENTIONED a pause was treated as one; the work item "+
				"would be left claimed with its locks held (totals=%+v)", report.Totals)
		}
		if !contains(h.dispatched, "greps/commit_and_pr") {
			t.Errorf("the loop stopped on a false-positive pause: dispatched=%v", h.dispatched)
		}
		if report.Totals.Wrapped != 1 {
			t.Errorf("wrapped = %d, want 1", report.Totals.Wrapped)
		}
	})

	t.Run("a real pause is still honoured", func(t *testing.T) {
		w := fwi("really-paused", "normal", "2026-01-01T00:00:00Z")
		w.pauseAtStep = "code_change"
		h := newFakeHub([]string{"code_change", "commit_and_pr"}, w)
		r := runnerFor(h, Budget{})
		r.AttemptPaused = func(context.Context, string) (bool, error) { return true, nil }

		report, err := r.Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if report.Totals.Paused != 1 {
			t.Fatalf("a confirmed pause was not honoured: %+v", report.Totals)
		}
		for _, c := range h.completes {
			if strings.HasPrefix(c, "really-paused:") {
				t.Errorf("a paused attempt was completed (%s); §0e forbids it", c)
			}
		}
	})

	t.Run("an unanswerable check errs towards paused", func(t *testing.T) {
		w := fwi("unsure", "normal", "2026-01-01T00:00:00Z")
		w.pauseAtStep = "code_change"
		h := newFakeHub([]string{"code_change", "commit_and_pr"}, w)
		r := runnerFor(h, Budget{})
		r.AttemptPaused = func(context.Context, string) (bool, error) {
			return false, fmt.Errorf("server unreachable")
		}

		report, err := r.Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		// Not completing a live attempt is recoverable; completing one somebody deliberately
		// paused destroys the hand-off they asked for. When unsure, do the recoverable thing.
		if report.Totals.Paused != 1 {
			t.Fatalf("an unconfirmable pause was not treated as a pause: %+v", report.Totals)
		}
	})
}

// TestRun_PublishNeverSeesAnAliasedSlice is the regression test for a data race the suite could
// not see, because no test set Publish at all.
//
// update() handed Publish a shallow struct copy whose Active/Recent slice HEADERS still pointed
// at the live backing arrays, while setActive writes s.Active[i] in place and clearActive
// compacts through s.Active[:0]. The production Publish marshals its argument to JSON, so the
// result was a torn ActiveWI in snapshot.json, or a fault inside reflect on a half-updated
// string header. Run this with -race.
func TestRun_PublishNeverSeesAnAliasedSlice(t *testing.T) {
	var wis []fakeWI
	for i := 0; i < 10; i++ {
		wis = append(wis, fwi(fmt.Sprintf("w%02d", i), "normal", "2026-01-01T00:00:00Z"))
	}
	h := newFakeHub([]string{"spec", "code_change", "commit_and_pr"}, wis...)

	r := runnerFor(h, Budget{MaxParallel: 8})
	r.Publish = func(s *Snapshot) {
		// Exactly what the production Publish does: walk the whole structure.
		if _, err := json.Marshal(s); err != nil {
			t.Errorf("marshal published snapshot: %v", err)
		}
		for _, a := range s.Active {
			_ = a.Candidate.Slug + a.StepID
		}
	}

	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
}
