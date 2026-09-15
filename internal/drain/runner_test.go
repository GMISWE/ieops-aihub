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
	// lockedByRunning names another work item: while THAT one is `running`, claiming this one
	// loses a lock race to its attempt. It is how the fake reproduces a lock refusal whose holder
	// is one of THIS RUN's own concurrent claims (aihub#678 ③(b)), which is the case the
	// whole-run skip gets wrong.
	lockedByRunning string
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

	// gate/gateOnce serialise the one genuinely concurrent scenario in this file: a worker whose
	// first step blocks until a SIBLING worker has lost a lock race to its attempt. Without it
	// "the holder is my own live attempt" is a race the test would only sometimes reach.
	//
	// It is closed inside claim() for any id OTHER THAN holdFirstStepOf (i.e. the CONTENDER),
	// registered as a defer before any branch of claim() runs, guarded by gateOnce so only the
	// first qualifying return path fires it. That "any return path" is load-bearing (aihub#687):
	// the close used to live inside the lockedByRunning refusal branch alone, so a contender
	// whose claim() returned via a DIFFERENT branch (a foreign lock holder, "not queued", or even
	// a bare success caused by the ordering bug claimGate now closes) left the holder's dispatch
	// blocked forever — CI run 34918148398.
	gate     chan struct{}
	gateOnce sync.Once
	// holdFirstStepOf names the work item whose first dispatch waits on the gate.
	holdFirstStepOf string

	// claimGate/claimGateOnce/claimGateHolder make claim ORDER deterministic instead of leaving it
	// to the scheduler (aihub#687). executeRound feeds an UNBUFFERED index channel to N workers:
	// receiving index 0 does not order claim(wi1) before claim(wi2) reaching claim() at all, only
	// before the CHANNEL SEND for index 1 — so a contender whose lockedByRunning names the holder
	// can reach its decision while the holder is still "queued", claim successfully instead of
	// being refused, and never close `gate` above.
	//
	// A claim() call for an id whose lockedByRunning equals claimGateHolder waits on claimGate,
	// releasing h.mu first (the holder's own claim needs the same mutex to proceed and close the
	// gate — see armLockRaceGate), until claimGateHolder's own claim() call has returned. That
	// claim() call closes claimGate from a defer, guarded by claimGateOnce, on every one of ITS
	// return paths.
	claimGate       chan struct{}
	claimGateOnce   sync.Once
	claimGateHolder string

	// onClaimLocked, when set, is called inside claim() immediately after h.mu.Lock() and before
	// any decision. It runs UNDER the mutex, which is the whole point: a test can use it to
	// release a parked HOLDER goroutine while the CONTENDER's claim() call still holds h.mu, so
	// the contender's decision provably happens first — the exact bad interleaving claimGate
	// exists to survive, forced instead of hoped for. Nil in every test that does not need it.
	onClaimLocked func(id string)
}

// armLockRaceGate wires the two-gate lock-race scenario this file's concurrent tests share: gate
// holds holder's first dispatch in flight until the contender — the work item in the hub whose
// lockedByRunning names holder — has been decided, and claimGate forces the contender to always
// observe holder as "running" rather than racing it.
//
// The preconditions are ENFORCED here, not assumed, because a silently-violated one turns this
// helper into the thing it exists to prevent. What IS checked, and why each one is load-bearing:
//   - b.MaxParallel >= 2: holder and contender can never be in the same round otherwise.
//   - holder is in the hub AND its status is "queued": claim() only ever runs for a candidate
//     executable() offers, and only a "queued" item is offered. A holder that is missing or not
//     queued is never claimed, so claimGate — closed exclusively from the HOLDER's own claim()
//     call — is never closed either, and every contender parked on it hangs forever.
//   - the number of work items naming holder as lockedByRunning is STRICTLY LESS than
//     b.MaxParallel, not merely >= 1. This is the check a reviewer's probe (aihub#687,
//     mem_442Sz9ym) proved missing: executeRound runs exactly b.Parallelism() workers pulling
//     indices off one unbuffered channel, and a worker that parks on claimGate inside claim()
//     does not return to pull another index. If the contender count equals or exceeds
//     MaxParallel, EVERY worker in the pool can end up parked at once — regardless of where
//     OrderCandidates sorts holder — and the feeder's blocking send for holder's own index then
//     has no free worker to reach, so nobody ever calls claim(holder) to close the gate: a hang,
//     not a race. With contenders < MaxParallel at least one worker is always free to reach
//     holder's index no matter its sort position, which is what makes the check sufficient rather
//     than merely necessary.
//
// What is NOT enforced: anything about the contenders' or holder's relative ORDER. That is
// deliberate — the whole point is that this scenario must hang-free regardless of where
// OrderCandidates places holder, not merely in the arrangement a given test happens to construct.
func (h *fakeHub) armLockRaceGate(t *testing.T, holder string, b Budget) {
	t.Helper()
	if b.MaxParallel < 2 {
		t.Fatalf("armLockRaceGate(%q): Budget.MaxParallel = %d, want >= 2 — the race this arms "+
			"cannot occur unless holder and contender can run in the same round", holder, b.MaxParallel)
	}
	holderWI, ok := h.wis[holder]
	if !ok {
		t.Fatalf("armLockRaceGate: holder %q is not in the hub", holder)
	}
	if holderWI.status != "queued" {
		t.Fatalf("armLockRaceGate(%q): holder status = %q, want \"queued\" — a holder that is not "+
			"claimable is never claimed, so claimGate would never be closed either", holder, holderWI.status)
	}
	contenderCount := 0
	for _, w := range h.wis {
		if w.lockedByRunning == holder {
			contenderCount++
		}
	}
	if contenderCount == 0 {
		t.Fatalf("armLockRaceGate: no work item in the hub has lockedByRunning = %q; nothing would "+
			"ever wait on claimGate, so it would never be closed either", holder)
	}
	if contenderCount >= b.MaxParallel {
		t.Fatalf("armLockRaceGate(%q): %d work items name holder as lockedByRunning, want < "+
			"Budget.MaxParallel (%d) — with that many contenders every worker in the pool can end "+
			"up parked on claimGate at once, whatever position holder sorts into, and the feeder "+
			"never has a free worker left to hand holder's own index to", holder, contenderCount, b.MaxParallel)
	}
	h.gate = make(chan struct{})
	h.holdFirstStepOf = holder
	h.claimGate = make(chan struct{})
	h.claimGateHolder = holder
}

// runWithHangDetector runs r.Run in a goroutine and FAILS the test if it has not returned within
// timeout, rather than letting a regression hang the whole package for its 10-minute go-test
// timeout the way CI run 34918148398 did (aihub#687). This is a detector, not a fix-by-timeout:
// on a hang the assertion goes red, it does not silently let the run "pass" by never finishing.
func runWithHangDetector(t *testing.T, r *Runner, timeout time.Duration) (RunReport, error) {
	t.Helper()
	type result struct {
		report RunReport
		err    error
	}
	done := make(chan result, 1)
	go func() {
		report, err := r.Run(context.Background())
		done <- result{report, err}
	}()
	select {
	case res := <-done:
		return res.report, res.err
	case <-time.After(timeout):
		t.Fatalf("r.Run did not return within %s: this is the hang the fake's gates exist to make "+
			"impossible by construction, not a slow test", timeout)
		return RunReport{}, nil // unreachable; t.Fatalf stops the goroutine
	}
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
func (h *fakeHub) outsidersLocked(w *fakeWI, inScope map[string]bool) []BlockerRef {
	var out []BlockerRef
	for _, b := range w.blockedBy {
		if !inScope[b] {
			out = append(out, BlockerRef{ID: b, Slug: b})
		}
	}
	return out
}

func (h *fakeHub) claim(_ context.Context, id, _ string) (*ClaimInfo, *Blocker, error) {
	h.mu.Lock()
	h.claimAttempts[id]++
	if h.onClaimLocked != nil {
		h.onClaimLocked(id)
	}

	// claimGate: an id whose lockedByRunning names this hub's designated holder waits HERE,
	// before deciding anything, until the holder's own claim() call has returned — so it always
	// observes the holder as "running" instead of racing it through the scheduler. Release h.mu
	// while parked: the holder's own claim() call below needs the same mutex to proceed and close
	// this gate, so holding it here would deadlock the two goroutines against each other.
	if h.claimGate != nil && id != h.claimGateHolder {
		if w, ok := h.wis[id]; ok && w.lockedByRunning == h.claimGateHolder {
			h.mu.Unlock()
			<-h.claimGate
			h.mu.Lock()
		}
	}

	defer h.mu.Unlock()
	// gate (stepGate): released for the CONTENDER — any id other than holdFirstStepOf — on EVERY
	// return path below, registered as a defer before any branch runs so it is unconditional
	// rather than living inside one branch. A contender that exits via the foreign-lockHolder
	// branch, the "not queued" branch, or even a bare success, must still let the holder's
	// blocked first dispatch proceed; only the lockedByRunning refusal used to do that.
	if h.gate != nil && id != h.holdFirstStepOf {
		defer h.gateOnce.Do(func() { close(h.gate) })
	}
	// claimGate: released for the HOLDER's own claim on every return path, so a contender parked
	// above wakes the instant the holder has been decided — successfully or not.
	if h.claimGate != nil && id == h.claimGateHolder {
		defer h.claimGateOnce.Do(func() { close(h.claimGate) })
	}

	w, ok := h.wis[id]
	if !ok {
		return nil, nil, fmt.Errorf("no such work item %s", id)
	}
	if w.lockHolder != "" {
		return nil, &Blocker{WorkItem: w.lockHolder, Actor: "someone.else", Resource: "internal/x.go",
				AttemptID: "ra_foreign"},
			fmt.Errorf("%w: held by %s", ErrLockTaken, w.lockHolder)
	}
	if other, ok := h.wis[w.lockedByRunning]; ok && other.status == "running" {
		// The attempt id is the SAME one claim() handed out for `other` — which is what makes
		// this refusal distinguishable from a foreign one, and the whole point of the fix.
		return nil, &Blocker{WorkItem: other.Slug, Actor: "me", Resource: "internal/x.go",
				AttemptID: "ra_" + other.ID},
			fmt.Errorf("%w: held by %s", ErrLockTaken, other.ID)
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
	hold := h.gate != nil && h.holdFirstStepOf == req.Claim.WorkItemID && req.Index == 1
	h.mu.Unlock()

	if hold {
		<-h.gate
	}

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

// ─── aihub#678 ───────────────────────────────────────────────────────────────────────────────

// TestRun_TheResolvedRoleReachesTheCommandLine is finding ① at the loop seam.
//
// The loop always resolved the role correctly — drainResolveRole calls engine.ResolveRole against
// the real catalog, and its own comment promises a review-shaped step "never silently to the
// write-capable executor". It then put the answer on DispatchRequest and NOTHING READ IT: three
// grep hits over the non-test sources, all writes. This asserts the two halves that were
// disconnected — the loop hands the role down, and the command line built from it is the
// read-only one — because either half alone can be right while a `code_review` step still runs
// with edits auto-approved.
//
// Mutants watched RED (each `go build`-checked first):
//   - executeWorkItem passing Role:"" / ReadOnly:false into DispatchRequest
//   - DispatchRequest.Binding() dropping ReadOnly
//   - the --disallowedTools append in channel.go (its own arm)
func TestRun_TheResolvedRoleReachesTheCommandLine(t *testing.T) {
	h := newFakeHub([]string{"code_change", "code_review"}, fwi("a", "normal", "2026-01-01T00:00:00Z"))
	r := runnerFor(h, Budget{})
	// The production three-tier fallback, in miniature: review-shaped step ids are the read-only
	// reviewer, everything else the write-capable executor.
	r.ResolveRole = func(sid string) (string, bool, error) {
		if engine.IsReviewStep(sid) {
			return "reviewer", true, nil
		}
		return "executor", false, nil
	}

	seen := map[string]DispatchRequest{}
	var mu sync.Mutex
	inner := h.dispatch
	r.Dispatch = func(ctx context.Context, req DispatchRequest) (DispatchResult, error) {
		mu.Lock()
		seen[req.Step.ID] = req
		mu.Unlock()
		return inner(ctx, req)
	}
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	review, ok := seen["code_review"]
	if !ok {
		t.Fatalf("the review step was never dispatched; seen=%v", seen)
	}
	if review.Role != "reviewer" || !review.ReadOnly {
		t.Fatalf("the review step reached the dispatcher as role=%q read_only=%v — the loop resolved "+
			"it and then lost it", review.Role, review.ReadOnly)
	}
	// The seam the production dispatcher uses. A Binding that dropped either field would build
	// the same write-capable command line the defect produced.
	if b := review.Binding(); b.Role != "reviewer" || !b.ReadOnly {
		t.Fatalf("Binding() = %+v, want the resolved role and its capability", b)
	}

	inv, err := BuildStepInvocation(Channel{Harness: HarnessClaude}, review.Binding(), review.Prompt)
	if err != nil {
		t.Fatalf("BuildStepInvocation for the review step: %v", err)
	}
	if !hasPair(inv.Args, "--disallowedTools", "Edit,Write,NotebookEdit") {
		t.Errorf("the review step's command line is %v. It denies nothing, so the reviewer can — "+
			"and under --permission-mode acceptEdits WILL, without asking — modify the tree it "+
			"is reviewing", inv.Args)
	}
	if !hasPair(inv.Args, "--agent", "polyforge:step-reviewer") {
		t.Errorf("the review step's command line is %v, selecting no reviewer agent", inv.Args)
	}

	// Negative control: the write step must NOT be restricted, or it completes nothing.
	code, ok := seen["code_change"]
	if !ok {
		t.Fatal("the code_change step was never dispatched")
	}
	if code.ReadOnly {
		t.Fatal("code_change reached the dispatcher as read-only")
	}
	codeInv, err := BuildStepInvocation(Channel{Harness: HarnessClaude}, code.Binding(), code.Prompt)
	if err != nil {
		t.Fatalf("BuildStepInvocation for the code step: %v", err)
	}
	if has(codeInv.Args, "--disallowedTools") {
		t.Errorf("the write step's command line %v denies Edit/Write/NotebookEdit", codeInv.Args)
	}
}

// TestRun_AReadOnlyRoleIsNeverSilentlyRunWriteCapable covers the other side of ①: the harness
// whose read-only capability rides ENTIRELY on an agent selector that fails open.
//
// ⚠️ An earlier draft of this test asserted the wrong thing — that opencode is refused outright
// for a read-only role — on the premise that roles.CompileCapability's refusal meant "opencode
// cannot express read_only". A clean-context reviewer rejected that: render_opencode.go writes
// `permission: edit: deny` into the generated agent file, so opencode CAN express it, and
// refusing would have FAILED every work item with a review step on a working
// `--channel=opencode` configuration. The real hazard is narrower and is what is asserted now.
//
// Mutants watched RED, each applied alone:
//   - demoting the `req.ReadOnly` guard on the AgentFellBackToDefault branch to a log line (the
//     pre-review behaviour) → the silent-fallback arm
//   - deleting the StepChannelUnsuitable classification → the suppressed-selector arm
func TestRun_AReadOnlyRoleIsNeverSilentlyRunWriteCapable(t *testing.T) {
	t.Run("a silent fallback to the default agent fails the step", func(t *testing.T) {
		// The measured opencode shape: exit 0, work done, and a warning that the agent carrying
		// the capability was not the one that ran. Exit 0 and non-empty output make this StepOK
		// to every other check in the package — which is exactly why it needs its own.
		h := newFakeHub([]string{"code_review"}, fwi("a", "normal", "2026-01-01T00:00:00Z"))
		r := runnerFor(h, Budget{})
		r.Channels = []Channel{{Harness: HarnessOpenCode}}
		r.ResolveRole = func(string) (string, bool, error) { return "reviewer", true, nil }
		r.Dispatch = func(_ context.Context, _ DispatchRequest) (DispatchResult, error) {
			return DispatchResult{Output: "! agent \"step-reviewer\" not found. Falling back to " +
				"default agent\nreviewed it\n<!-- REVIEW_RESULT: PASS -->\ndone\n"}, nil
		}
		report, err := r.Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if report.Totals.Wrapped != 0 || report.Terminal != TerminalFailed {
			t.Fatalf("the work item was WRAPPED on a read-only step that ran write-capable "+
				"(terminal=%s, outcomes=%+v). The harness said out loud that it ran the default "+
				"agent; on this harness the agent file is the only thing carrying read_only, so "+
				"that sentence means the reviewer could edit the tree it was reviewing",
				report.Terminal, report.Outcomes)
		}

		// Negative control: the SAME fallback on a write-capable role is not a capability change
		// and must not fail anything — it costs the model tier and the prompt, which is a log
		// line, not a failure.
		h2 := newFakeHub([]string{"code_change"}, fwi("b", "normal", "2026-01-01T00:00:00Z"))
		r2 := runnerFor(h2, Budget{})
		r2.Channels = []Channel{{Harness: HarnessOpenCode}}
		r2.ResolveRole = func(string) (string, bool, error) { return "executor", false, nil }
		r2.Dispatch = func(_ context.Context, _ DispatchRequest) (DispatchResult, error) {
			return DispatchResult{Output: "! agent \"step-executor\" not found. Falling back to " +
				"default agent\ndid the thing\nsummary\n"}, nil
		}
		rep2, err := r2.Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if rep2.Totals.Wrapped != 1 {
			t.Errorf("a write-capable role was failed for a fallback that cost it no capability: "+
				"%+v", rep2.Outcomes)
		}
	})

	t.Run("a channel with nothing left to carry it is demoted, not run", func(t *testing.T) {
		// The second defence: the stale-plugin retry suppresses the agent selector, and on this
		// harness that leaves NOTHING expressing read_only. Building the command is refused and
		// the run falls to a channel that can.
		h := newFakeHub([]string{"code_review"}, fwi("a", "normal", "2026-01-01T00:00:00Z"))
		r := runnerFor(h, Budget{})
		r.Channels = []Channel{{Harness: HarnessOpenCode}, {Harness: HarnessClaude}}
		r.ResolveRole = func(string) (string, bool, error) { return "reviewer", true, nil }

		var used []Harness
		var mu sync.Mutex
		r.Dispatch = func(_ context.Context, req DispatchRequest) (DispatchResult, error) {
			// The PRODUCTION dispatcher's first act, reproduced: build the command, and report a
			// refusal to build it rather than running something else.
			b := req.Binding()
			b.NoAgentSelector = true // as the stale-plugin retry would have left it
			if _, err := BuildStepInvocation(req.Channel, b, req.Prompt); err != nil {
				return DispatchResult{ExitErr: err}, err
			}
			mu.Lock()
			used = append(used, req.Channel.Harness)
			mu.Unlock()
			return DispatchResult{Output: "reviewed\n<!-- REVIEW_RESULT: PASS -->\ndone\n"}, nil
		}

		report, err := r.Run(context.Background())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(used) != 1 || used[0] != HarnessClaude {
			t.Fatalf("the read-only step ran on %v, want claude only: with its selector suppressed "+
				"opencode has no way to express the capability, and claude still has "+
				"--disallowedTools", used)
		}
		if report.Terminal != TerminalCompleted {
			t.Errorf("terminal = %s (%+v); falling to a usable channel is ordinary control flow, "+
				"not a failure of the work item", report.Terminal, report.Outcomes)
		}
	})
}

// TestRun_ARefusedAgentSelectorRetriesWithoutIt keeps the fix for ① from becoming an outage.
//
// `claude --agent polyforge:step-explorer` exits 1 on a machine whose installed plugin predates
// the five-role catalog — which is this machine today: the plugin cache ships step-executor.md and
// step-reviewer.md, the repo has all five. Failing the step there would trade a silent capability
// widening for a hard failure of every explorer, designer and operator step. The retry drops the
// selector, KEEPS the capability flags, and says so.
//
// Mutant watched: deleting the StepAgentSelectorRefused branch makes the work item fail.
func TestRun_ARefusedAgentSelectorRetriesWithoutIt(t *testing.T) {
	h := newFakeHub([]string{"prepare_context"}, fwi("a", "normal", "2026-01-01T00:00:00Z"))
	r := runnerFor(h, Budget{})
	r.ResolveRole = func(string) (string, bool, error) { return "explorer", true, nil }

	var attempts []bool // NoAgentSelector, per attempt
	var logs []string
	var mu sync.Mutex
	r.Logf = func(f string, a ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(f, a...))
		mu.Unlock()
	}
	r.Dispatch = func(_ context.Context, req DispatchRequest) (DispatchResult, error) {
		mu.Lock()
		attempts = append(attempts, req.NoAgentSelector)
		mu.Unlock()
		if !req.NoAgentSelector {
			// Verbatim shape of what claude 2.1.258 prints, measured on this machine.
			return DispatchResult{Output: "--agent 'polyforge:step-explorer' not found. " +
					"Available agents: claude, Explore, polyforge:step-executor, polyforge:step-reviewer\n"},
				fmt.Errorf("exit status 1")
		}
		return DispatchResult{Output: "explored\nsummary\n"}, nil
	}

	report, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(attempts) != 2 || attempts[0] || !attempts[1] {
		t.Fatalf("dispatch attempts (NoAgentSelector per attempt) = %v, want [false true]: one "+
			"try with the selector, then exactly one without", attempts)
	}
	if report.Terminal != TerminalCompleted {
		t.Errorf("terminal = %s (%+v): a stale plugin install is not the work item's fault",
			report.Terminal, report.Outcomes)
	}
	// The degradation must be disclosed. A silent retry would hide that the step ran at the
	// wrong model tier with the wrong prompt, which is the half the selector carries.
	var told bool
	for _, l := range logs {
		if strings.Contains(l, "WITHOUT the agent selector") {
			told = true
		}
	}
	if !told {
		t.Errorf("the retry was not reported. logs=%v", logs)
	}

	// Negative control: an ordinary step failure must NOT be retried this way, or every failing
	// step would be run twice on a different command line.
	h2 := newFakeHub([]string{"prepare_context"}, fwi("b", "normal", "2026-01-01T00:00:00Z"))
	r2 := runnerFor(h2, Budget{})
	var n int
	r2.Dispatch = func(_ context.Context, _ DispatchRequest) (DispatchResult, error) {
		n++
		return DispatchResult{Output: "--- FAIL: TestThing\n"}, fmt.Errorf("exit status 1")
	}
	if _, err := r2.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 1 {
		t.Errorf("an ordinary step failure was dispatched %d times, want 1", n)
	}
}

// TestRun_ALockHeldByThisRunsOwnAttemptIsRetriedNextRound is finding ③(b).
//
// "Skip, never retry" is the right policy and its stated reason is "the holder is another live
// attempt, and nothing this run does will end it". At --max-parallel>=2 that reason can be FALSE:
// worker A claims wi1, worker B loses the race on wi2 to A's own attempt, A wraps and releases the
// lock — and wi2 has already been written off for the rest of the run. The next round filters it
// out, finds nothing, and reports StopQueueDrained while the same run's final observation says
// executable=1: an early stop that contradicts its own report.
//
// The discriminator is real rather than invented: every CONFLICT_LOCK_TAKEN aihub raises carries
// `conflict_with.attempt_id`. Nothing waits on a lock here; the work item is simply offered again
// next round, which is when the holder is gone.
//
// Mutants watched RED:
//   - `skipped[o.Candidate.ID] = true` unconditionally on ResultLockBlocked (the pre-fix line)
//   - heldByThisRun returning false always
//
// ⚠️ NOT watched here, and the claim that it was is one a reviewer had to take out: removing
// registerAttempt's deregistration leaves THIS test green. It is caught by
// TestRun_AnOwnAttemptThatNEVEREndsStopsBeingRetryable, which is written for exactly that
// mutant — the defect is covered, the attribution was not. A "mutant watched" line that names
// the wrong test is worse than no line, because the next person reads it as coverage that has
// been checked.
func TestRun_ALockHeldByThisRunsOwnAttemptIsRetriedNextRound(t *testing.T) {
	wi1 := fwi("wi1", "urgent", "2026-01-01T00:00:00Z")
	wi2 := fwi("wi2", "normal", "2026-01-01T00:00:01Z")
	wi2.lockedByRunning = "wi1"
	h := newFakeHub([]string{"code_change"}, wi1, wi2)
	budget := Budget{MaxParallel: 2, MaxRounds: 5}
	h.armLockRaceGate(t, "wi1", budget) // wi1 does not finish until wi2 has lost the race to its
	// attempt, and wi2's claim is forced to wait for wi1's so this is deterministic rather than
	// depending on which worker the scheduler happens to run first (aihub#687).

	r := runnerFor(h, budget)
	report, err := runWithHangDetector(t, r, 10*time.Second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if report.Totals.Wrapped != 2 {
		t.Fatalf("wrapped = %d, want 2. wi2's lock was held by THIS RUN's own attempt, so it was "+
			"free one round later; skipping it for the whole run strands work the run could do. "+
			"outcomes=%+v", report.Totals.Wrapped, report.Outcomes)
	}
	if report.StopReason != StopQueueDrained {
		t.Errorf("stop reason = %s, want queue_drained", report.StopReason)
	}
	if report.Terminal != TerminalCompleted {
		t.Errorf("terminal = %s, want COMPLETED", report.Terminal)
	}
	// Bounded: exactly one refusal and one success, never a busy-wait.
	if n := h.claimAttempts["wi2"]; n != 2 {
		t.Errorf("wi2 was claimed %d times, want exactly 2 (one refusal, one success). More than "+
			"that is the busy-wait 'skip, never retry' exists to prevent", n)
	}

	// Negative control, and the half that must not regress: a FOREIGN holder is still skipped for
	// the whole run. The fake gives that refusal attempt id "ra_foreign", which this run never
	// claimed.
	h2 := newFakeHub([]string{"code_change"}, func() fakeWI {
		w := fwi("x", "normal", "2026-01-01T00:00:00Z")
		w.lockHolder = "somebody-else#4"
		return w
	}())
	rep2, err := runnerFor(h2, Budget{MaxRounds: 5}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := h2.claimAttempts["x"]; n != 1 {
		t.Errorf("a work item held by a FOREIGN attempt was claimed %d times, want 1: waiting on "+
			"a lock inside a scheduler that holds locks is how a deadlock is built", n)
	}
	if rep2.StopReason != StopNothingClaimable {
		t.Errorf("stop reason = %s, want nothing_claimable", rep2.StopReason)
	}
}

// TestRun_AnOwnAttemptThatNEVEREndsStopsBeingRetryable is the bound on the retry above, and it is
// the reason registerAttempt deregisters on EVERY exit rather than only on wrap.
//
// A paused or cancelled attempt is left claimed and still holding its locks — so it looks exactly
// like one of this run's own attempts, while being the one case where nothing this run does will
// ever release it. If such an attempt stayed registered, its lock-blocked victim would be judged
// "retry next round" every round forever: a busy-wait wearing a round loop's clothing, which is
// precisely what aihub#640 `two_kinds_of_blocking` warns about ("等待是死锁的温床"). Deregistering
// on return makes the live set empty at every round boundary, so the only retryable refusal is one
// raised by a SIBLING WORKER IN THE SAME ROUND — and that sibling, having claimed, made progress.
//
// Mutant watched RED: making registerAttempt's returned function a no-op. Without the bound, wi2
// is re-claimed on every one of the five permitted rounds.
func TestRun_AnOwnAttemptThatNEVEREndsStopsBeingRetryable(t *testing.T) {
	wi1 := fwi("wi1", "urgent", "2026-01-01T00:00:00Z")
	// wi1 pauses mid-step: the loop returns WITHOUT calling pf_complete_attempt (§0e), so the
	// attempt stays claimed and its locks stay held, for the rest of the run and beyond it.
	wi1.pauseAtStep = "code_change"
	wi2 := fwi("wi2", "normal", "2026-01-01T00:00:01Z")
	wi2.lockedByRunning = "wi1"

	h := newFakeHub([]string{"code_change"}, wi1, wi2)
	const rounds = 5
	budget := Budget{MaxParallel: 2, MaxRounds: rounds}
	h.armLockRaceGate(t, "wi1", budget)

	r := runnerFor(h, budget)
	report, err := runWithHangDetector(t, r, 10*time.Second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Two claims is the correct count and the assertion is written to say why, because "1" is
	// wrong and "5" is the defect. The first refusal happens while wi1's attempt really is live,
	// so deferring is right; by the second round that attempt has returned (paused), so it is no
	// longer this run's to finish and wi2 is written off permanently. The bound is what matters:
	// it must not scale with the number of rounds.
	if n := h.claimAttempts["wi2"]; n > 2 {
		t.Errorf("wi2 was claimed %d times over %d rounds. Its lock is held by a PAUSED attempt of "+
			"this run — claimed, locks held, and never coming back — so a per-round retry is a "+
			"busy-wait that only stops when the round budget runs out", n, rounds)
	}
	// The stop reason is the sharper half of the same discriminator: the loop must run out of
	// things to try, not out of ROUNDS. max_rounds here means it was still retrying when the
	// budget cut it off — i.e. it would have kept going forever without one.
	if report.StopReason == StopMaxRounds {
		t.Errorf("stop reason = %s: the loop was still retrying wi2 when the round budget ran out. "+
			"Without a round budget this run does not terminate", report.StopReason)
	}
}

// TestFakeClaimGate_ReleasesTheHolderOnEveryContenderBranch is R1 from aihub#687.
//
// gate (the stepGate above) used to be closed from exactly one place: inside claim()'s
// lockedByRunning refusal branch. That is fine as long as the contender's claim() call always
// takes that branch — but nothing forced it to. CI run 34918148398 hung the whole package for its
// 10-minute timeout because executeRound feeds an UNBUFFERED index channel to N workers: receiving
// index 0 orders nothing about when claim(wi1) runs relative to claim(wi2) reaching its own
// decision, so the contender's claim() call can return via a DIFFERENT branch than the refusal —
// and when it does, the OLD code never closed the gate, and the holder's first dispatch (which was
// waiting on it) blocked forever.
//
// This reproduces that without any goroutine race at all, exactly as the design calls for: claim
// the holder directly, give the CONTENDER a foreign lockHolder so its claim() is GUARANTEED to
// return via the foreign-lockHolder branch (never the lockedByRunning refusal), and show the
// holder's dispatch — the only goroutine here — hangs.
//
// Mutant watched RED: moving the gate-close defer back inside the lockedByRunning branch only
// (i.e. reverting to the pre-fix shape).
func TestFakeClaimGate_ReleasesTheHolderOnEveryContenderBranch(t *testing.T) {
	run := func(t *testing.T, contenderForeign bool) error {
		t.Helper()
		holder := fwi("wi1", "urgent", "2026-01-01T00:00:00Z")
		contender := fwi("wi2", "normal", "2026-01-01T00:00:01Z")
		if contenderForeign {
			// The branch the old code never closed the gate from: claim(wi2) returns at the
			// foreign-lockHolder early return, never reaching the lockedByRunning check at all.
			contender.lockHolder = "somebody-else#9"
		} else {
			// The ONE branch the old code already handled — the negative control.
			contender.lockedByRunning = "wi1"
		}
		h := newFakeHub([]string{"code_change"}, holder, contender)
		h.gate = make(chan struct{})
		h.holdFirstStepOf = "wi1"

		if _, _, err := h.claim(context.Background(), "wi1", "k1"); err != nil {
			t.Fatalf("claim(wi1): %v", err)
		}

		dispatchDone := make(chan error, 1)
		go func() {
			_, derr := h.dispatch(context.Background(), DispatchRequest{
				Claim: ClaimInfo{WorkItemID: "wi1"}, Step: StepSpec{ID: "code_change"}, Index: 1,
			})
			dispatchDone <- derr
		}()

		if _, _, err := h.claim(context.Background(), "wi2", "k2"); err == nil {
			t.Fatalf("claim(wi2): want an error, got none")
		}

		select {
		case err := <-dispatchDone:
			return err
		case <-time.After(10 * time.Second):
			return fmt.Errorf("holder's dispatch did not return within 10s: the contender's claim " +
				"exited via a branch that never closed the gate — exactly CI run 34918148398")
		}
	}

	const n = 10

	// Negative control FIRST, deliberately: the SAME two-call, one-dispatch harness, but the
	// contender is refused via lockedByRunning instead of a foreign lock — the ONE branch that
	// closed the gate even before this fix. It must pass regardless of the fix, proving the
	// failure mode below is about WHICH branch closes the gate and not some incidental bug in
	// this harness. It runs first because a t.Fatalf in the positive half would otherwise skip
	// it, which is precisely when its answer is wanted: a reader looking at a red run needs to
	// see that the control was green in the SAME run (measured 2026-09-15 — reverting the fix
	// aborted the test before the control ever executed).
	for i := 0; i < n; i++ {
		if err := run(t, false); err != nil {
			t.Fatalf("run %d/%d (contender lockedByRunning, negative control): %v", i+1, n, err)
		}
	}

	// Positive: 10/10 deterministic hangs on the pre-aihub#687 gate-close placement — measured by
	// literally reverting the fix and re-running (3/3 runs failed on iteration 1/10); on the FIXED
	// code (this file, as committed) it must be 10/10 green.
	for i := 0; i < n; i++ {
		if err := run(t, true); err != nil {
			t.Fatalf("run %d/%d (contender foreign-locked): %v", i+1, n, err)
		}
	}
}

// TestRun_ContenderClaimArrivingFirstMustNotLoseTheRefusal is R2 from aihub#687.
//
// R1 above shows the gate is released unconditionally now. This test targets the OTHER half of
// the fix — claimGate — which makes claim ORDER deterministic instead of racing the scheduler in
// the first place: it forces, via onClaimLocked, the EXACT bad interleaving that made CI run
// 34918148398 hang. The contender's claim() call acquires h.mu and starts deciding BEFORE the
// holder's claim() call has even been attempted, so on a build with claimGate's WAIT removed the
// contender sees the holder still "queued", claims successfully instead of being refused, and the
// gate that is supposed to unblock the holder's dispatch is never closed by anybody (gate is only
// ever closed by "a return path other than the holder's own claim" — see the fix in claim()).
//
// The assertion is written to fail on "it merely terminated": a run can terminate with wi2 having
// simply won the claim outright, which is exactly the defect. The property under test is that the
// contender was REFUSED once (its first claim(), forced first, sees the holder still queued only
// in the sense that it must wait rather than falling through) and then claimed successfully once
// the holder is actually running — never that it merely finished.
//
// Mutant watched RED: removing claimGate's wait (keeping the gate fix from R1). The run still
// terminates — gate is still closed unconditionally — but wi2 is claimed exactly once, never
// refused, which is what the assertion below must catch.
func TestRun_ContenderClaimArrivingFirstMustNotLoseTheRefusal(t *testing.T) {
	wi1 := fwi("wi1", "urgent", "2026-01-01T00:00:00Z")
	wi2 := fwi("wi2", "normal", "2026-01-01T00:00:01Z")
	wi2.lockedByRunning = "wi1"
	h := newFakeHub([]string{"code_change"}, wi1, wi2)
	budget := Budget{MaxParallel: 2, MaxRounds: 5}
	h.armLockRaceGate(t, "wi1", budget)

	// Force the bad interleaving deterministically instead of hoping the scheduler produces it:
	// the holder's claim() call is not even ATTEMPTED until the contender's claim() call is
	// provably holding h.mu — proving the contender's decision happens first, every run.
	contenderLocked := make(chan struct{})
	var once sync.Once
	h.onClaimLocked = func(id string) {
		if id == "wi2" {
			once.Do(func() { close(contenderLocked) })
		}
	}
	r := runnerFor(h, budget)
	innerClaim := r.Claim
	r.Claim = func(ctx context.Context, id, key string) (*ClaimInfo, *Blocker, error) {
		if id == "wi1" {
			select {
			case <-contenderLocked:
			case <-time.After(10 * time.Second):
				t.Error("wi2 never reached claim(); the fixture, not the code under test, is broken")
			}
		}
		return innerClaim(ctx, id, key)
	}

	report, err := runWithHangDetector(t, r, 10*time.Second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Totals.Wrapped != 2 {
		t.Fatalf("wrapped = %d, want 2. outcomes=%+v", report.Totals.Wrapped, report.Outcomes)
	}

	// The property under test: refused exactly once, then claimed and completed — not merely
	// "the run terminated", which a mutant that drops the claimGate wait still does (gate alone
	// still unblocks the holder). See the mutant note in the doc comment above.
	var wi2Results []Result
	for _, o := range report.Outcomes {
		if o.Candidate.ID == "wi2" {
			wi2Results = append(wi2Results, o.Result)
		}
	}
	if len(wi2Results) != 2 || wi2Results[0] != ResultLockBlocked || wi2Results[1] != ResultWrapped {
		t.Fatalf("wi2 outcomes = %v, want [%s %s]: refused exactly once by the forced bad "+
			"interleaving, then claimed and completed once the holder was actually running",
			wi2Results, ResultLockBlocked, ResultWrapped)
	}
	if n := h.claimAttempts["wi2"]; n != 2 {
		t.Errorf("wi2 was claimed %d times, want exactly 2 (one refusal, one success)", n)
	}
}

// TestRun_ACancelledWorkItemNeedsAHumanNotALaterRun is finding ③(c).
//
// A `--stop` or Ctrl-C that lands mid-step used to end the run IDLE, exit 10 — "come back later,
// no human needed". What it actually leaves is an attempt CLAIMED, status `running`, holding its
// locks, and drain never picks a running work item back up: Executable asks the server for
// `ready_only`, which is `queued`. So running it again makes no progress whatsoever, and the
// machine-readable half of the notification design said the opposite of the prose disclosure
// `--stop` prints two screens away.
//
// Mutant watched: reverting NeedsHuman to `r == ResultFailed` turns the exit-code assertion red.
func TestRun_ACancelledWorkItemNeedsAHumanNotALaterRun(t *testing.T) {
	h := newFakeHub([]string{"code_change", "commit_and_pr"},
		fwi("a", "normal", "2026-01-01T00:00:00Z"))
	ctx, cancel := context.WithCancel(context.Background())

	r := runnerFor(h, Budget{})
	inner := h.dispatch
	r.Dispatch = func(dctx context.Context, req DispatchRequest) (DispatchResult, error) {
		out, err := inner(dctx, req)
		cancel()
		return out, err
	}
	report, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Totals.Cancelled != 1 {
		t.Fatalf("cancelled = %d, want 1; outcomes=%+v", report.Totals.Cancelled, report.Outcomes)
	}
	if report.Terminal != TerminalFailed {
		t.Fatalf("terminal = %s, want FAILED. The attempt is left claimed and holding its locks, "+
			"and drain only ever claims `queued` work items, so IDLE — whose whole contract is "+
			"\"come back later and it will work\" — is a promise nothing can keep", report.Terminal)
	}
	if got := ExitCode(report.Terminal); got != 12 {
		t.Errorf("exit code = %d, want 12. For an unattended runner the exit code IS the contract: "+
			"10 tells the caller no human is needed, and a stranded claim needs pf_force_takeover", got)
	}

	// Negative control, and the reason this is keyed on the OUTCOME rather than on StopCancelled:
	// an interrupt that lands with nothing in flight strands nothing, and must still be IDLE.
	h2 := newFakeHub([]string{"code_change"}, fwi("b", "normal", "2026-01-01T00:00:00Z"))
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	rep2, err := runnerFor(h2, Budget{}).Run(ctx2)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep2.Terminal != TerminalIdle {
		t.Errorf("an interrupt before any claim ended %s, want IDLE: nothing was stranded, so "+
			"re-running really is the fix", rep2.Terminal)
	}
}

// TestRun_MaxDurationBoundsTheWallClock is finding ④.
//
// Budget carried MaxRounds/MaxWorkItems/MaxParallel and no time at all, while the design names
// the dimension verbatim ("跑满 N 个 / T 时间"). Both existing caps default to unlimited, so a
// `polyforge drain --detach` had no wall-clock ceiling of any kind — its only bound was
// rounds x work-items x the 2h per-step timeout.
//
// Mutants watched RED:
//   - deleting the context.WithDeadline block (the run never stops)
//   - reporting StopCancelled instead of StopMaxDuration (the reason arm)
func TestRun_MaxDurationBoundsTheWallClock(t *testing.T) {
	h := newFakeHub([]string{"code_change"},
		fwi("a", "normal", "2026-01-01T00:00:00Z"),
		fwi("b", "normal", "2026-01-01T00:00:01Z"))
	r := runnerFor(h, Budget{MaxDuration: 40 * time.Millisecond})
	// A step that outlives the budget. It honours its context, exactly as exec.CommandContext
	// does in production.
	r.Dispatch = func(ctx context.Context, req DispatchRequest) (DispatchResult, error) {
		select {
		case <-ctx.Done():
			return DispatchResult{Output: "killed\n"}, ctx.Err()
		case <-time.After(10 * time.Second):
			return DispatchResult{Output: "did the thing\nsummary\n"}, nil
		}
	}

	done := make(chan RunReport, 1)
	go func() {
		rep, err := r.Run(context.Background())
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		done <- rep
	}()
	select {
	case report := <-done:
		if report.StopReason != StopMaxDuration {
			t.Errorf("stop reason = %s, want max_duration. StopCancelled would tell an operator "+
				"somebody stopped the run when in fact its own budget did", report.StopReason)
		}
		if report.Totals.Wrapped != 0 {
			t.Errorf("wrapped = %d: the budget expired mid-step", report.Totals.Wrapped)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the run did not stop: --max-duration is not bounding anything, which is the " +
			"state this finding is about")
	}
}

// TestRun_LayerTwoNotifiesOnlyWhatItCanAddress covers the two notification changes aihub#678 makes,
// both of which are about a note going to the wrong place or nowhere.
//
//   - A blocker the caller may not open has NO id: aihub sends its slug and substitutes the
//     sentinel "hidden" for the id (invariant 2). The note used to be emitted with
//     work_item_id="hidden", which 404s — every round, silently, on the one channel that reaches
//     another human in an unattended run.
//   - A queued requires_human_session work item is now a reason a run can end BLOCKED_EXTERNAL,
//     and it must be NAMED — but on stderr and in the snapshot, never as a timeline note. See
//     reportHumanSessionNeeded: aihub#636's note de-duplication keys on the emitting attempt, and
//     drain's Notify sends none, so an hourly run would add identical notes forever.
//
// ⚠️ The hidden arm is asserted on the LOG, not only on the absence of a note, and the difference
// is the point. blockingEdges already strips the sentinel to "", and both the production Notify
// seam and this fake return early on an empty target — so deleting the guard here changes no
// note that is sent. What it changes is whether anybody is TOLD: "somebody outside your project
// visibility is holding this" is real information, and silently dropping the note on the floor is
// how the 404-per-round version managed to look like it was working.
//
// Mutants watched RED: deleting the `!blocker.Notifiable()` guard (the hidden arm, via the log);
// deleting the notifyHumanSessionNeeded call (the human-session arm).
func TestRun_LayerTwoNotifiesOnlyWhatItCanAddress(t *testing.T) {
	h := newFakeHub([]string{"code_change"})
	r := runnerFor(h, Budget{})
	var logs []string
	var logMu sync.Mutex
	r.Logf = func(f string, a ...any) {
		logMu.Lock()
		logs = append(logs, fmt.Sprintf(f, a...))
		logMu.Unlock()
	}
	r.ObserveQueue = func(context.Context) (QueueState, error) {
		return QueueState{
			ExternallyBlocked: []BlockedWorkItem{{
				WorkItemID: "wi_blocked", Slug: "p#9",
				Blockers: []BlockerRef{
					{ID: "wi_theirs", Slug: "other#3"}, // addressable
					{Slug: "secret#1"},                 // cross-project: slug only, no id
				},
			}},
			NeedsHumanSession: []Candidate{{ID: "wi_rhs", Slug: "p#4"}},
		}, nil
	}

	report, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Terminal != TerminalBlockedExternal {
		t.Fatalf("terminal = %s, want BLOCKED_EXTERNAL", report.Terminal)
	}

	targets := map[string]int{}
	for _, n := range h.notes {
		targets[n.WorkItemID]++
	}
	for _, want := range []string{"wi_blocked", "wi_theirs"} {
		if targets[want] == 0 {
			t.Errorf("no note reached %s; notes=%+v", want, h.notes)
		}
	}
	for _, never := range []string{"hidden", "secret#1", "", "wi_rhs"} {
		if targets[never] != 0 {
			t.Errorf("a note was addressed to %q, which is not a work item id this caller can "+
				"emit against; it 404s once per run and tells nobody anything", never)
		}
	}
	var disclosed bool
	for _, l := range logs {
		if strings.Contains(l, "secret#1") && strings.Contains(l, "outside your project visibility") {
			disclosed = true
		}
	}
	if !disclosed {
		t.Errorf("the un-addressable blocker was dropped without a word. Both the production "+
			"Notify seam and this fake return early on an empty target, so an unguarded note "+
			"simply vanishes — and \"somebody you cannot see is holding this\" is the one fact "+
			"an operator can act on. logs=%v", logs)
	}
	// The requires_human_session leftovers must be NAMED somewhere, or exit 11 arrives with no
	// explanation at all. Deliberately not a timeline note (see reportHumanSessionNeeded): the
	// slugs go to stderr and into the snapshot, where a stable population belongs.
	var explained bool
	for _, l := range logs {
		if strings.Contains(l, "requires_human_session") && strings.Contains(l, "p#4") {
			explained = true
		}
	}
	if !explained {
		t.Errorf("nothing named the requires_human_session work item that made this run exit 11. "+
			"logs=%v", logs)
	}
}

// TestRun_TheOwnHolderCheckDoesNotRaceTheClaim closes the window a reviewer found in ③(b)'s first
// implementation, and it fails deterministically against that implementation rather than flakily.
//
// The window: `heldByThisRun` keyed only on the holder's ATTEMPT id, which is known only after
// that worker's claim RETURNED. But the server takes worker A's lock and refuses worker B inside
// the same request, so B's 409 — naming A's attempt — can be processed before A's claim has come
// back and registered it. The refusal then reads as foreign, the work item is written off for the
// whole run, and ③(b) silently does nothing. Non-deterministically, which is the worst way for a
// scheduling decision to be wrong.
//
// The fixture makes that ordering certain instead of likely: A's claim blocks until B has already
// been refused. The fix is the second registry — the work item id and slug are known from the
// candidate list, so they are registered BEFORE the claim goes out and the window does not exist.
//
// Mutant watched RED: dropping `liveCandidates` from heldByThisRun (back to the attempt-only
// check) strands wi2 for the run.
func TestRun_TheOwnHolderCheckDoesNotRaceTheClaim(t *testing.T) {
	wi1 := fwi("wi1", "urgent", "2026-01-01T00:00:00Z")
	wi2 := fwi("wi2", "normal", "2026-01-01T00:00:01Z")
	h := newFakeHub([]string{"code_change"}, wi1, wi2)

	r := runnerFor(h, Budget{MaxParallel: 2, MaxRounds: 5})
	refused := make(chan struct{})
	var once sync.Once
	inner := h.claim
	r.Claim = func(ctx context.Context, id, key string) (*ClaimInfo, *Blocker, error) {
		if id == "wi2" {
			// The refusal the server raises the instant it takes wi1's lock — BEFORE wi1's own
			// claim has returned anything to this process. It names wi1's attempt and wi1's
			// slug, which is exactly the payload conflict_with carries.
			//
			// Only while wi1 still holds the lock: once wi1 has wrapped, the lock is free and the
			// ordinary fake answers. A fixture that refused forever would fail the real code and
			// prove nothing — which is how the first draft of this test read.
			h.mu.Lock()
			held := h.wis["wi1"].status != "wrapped"
			if held {
				h.claimAttempts[id]++
			}
			h.mu.Unlock()
			if held {
				once.Do(func() { close(refused) })
				return nil, &Blocker{AttemptID: "ra_wi1", WorkItem: "wi1", Resource: "internal/x.go"},
					fmt.Errorf("%w: held by wi1", ErrLockTaken)
			}
		}
		if id == "wi1" {
			// wi1's claim does not return until wi2 has already been refused: the window under
			// test, held open.
			select {
			case <-refused:
			case <-time.After(10 * time.Second):
				t.Error("wi2 was never refused; the fixture, not the code, is what this tested")
			}
		}
		return inner(ctx, id, key)
	}
	// Second round: the lock is free, so the ordinary fake answers.
	report, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Totals.Wrapped != 2 {
		t.Fatalf("wrapped = %d, want 2. wi2's refusal named wi1 — a work item THIS RUN was "+
			"executing — so the lock was free one round later. Keyed on the attempt id alone, "+
			"that refusal arrives before the attempt is registered and reads as somebody else's. "+
			"outcomes=%+v", report.Totals.Wrapped, report.Outcomes)
	}
}
