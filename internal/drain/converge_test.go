package drain

import "testing"

// TestDiverging_FiresOnEqualityAndNotOnTheEmptyRound pins BOTH halves of the divergence rule,
// and it is two assertions rather than one because the two mutants that break it are opposites.
//
// The owner's criterion (aihub#640 `convergence_divergence_detector`) is "本轮新建 wi 数 ≥ 完成 wi
// 数 ⇒ 立刻停下来让人看". Read literally that also fires on the round that created nothing and
// completed nothing — the most common ending a drain run has — which would make the loudest
// signal in the system fire mostly on non-events. The implementation therefore requires
// Created > 0 as well, and the pair of cases below is what stops either half from being dropped:
//
//   - Mutant A, delete `t.Created > 0`: the "empty round" case goes red.
//   - Mutant B, weaken `>=` to `>`: the "one in, one out" case goes red.
func TestDiverging_FiresOnEqualityAndNotOnTheEmptyRound(t *testing.T) {
	cases := []struct {
		name    string
		tally   RoundTally
		want    bool
		because string
	}{
		{
			name:    "one in one out is treading water",
			tally:   RoundTally{Completed: 1, Created: 1},
			want:    true,
			because: "the ruling says >=, not >; a round that files one follow-up per completion never converges",
		},
		{
			name:    "more created than completed",
			tally:   RoundTally{Completed: 2, Created: 5},
			want:    true,
			because: "the queue grew",
		},
		{
			name:    "created nothing, completed nothing",
			tally:   RoundTally{Completed: 0, Created: 0},
			want:    false,
			because: "0 >= 0 is literally true but nothing proliferated; this is the ordinary empty round",
		},
		{
			name:    "created nothing while completing work",
			tally:   RoundTally{Completed: 4, Created: 0},
			want:    false,
			because: "converging normally",
		},
		{
			name:    "created one while completing four",
			tally:   RoundTally{Completed: 4, Created: 1},
			want:    false,
			because: "shrinking; one follow-up out of four completions is not proliferation",
		},
		{
			name:    "created work while completing none",
			tally:   RoundTally{Completed: 0, Created: 1, Failed: 2},
			want:    true,
			because: "net growth with zero completions is the clearest divergence there is",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tally.Diverging(); got != tc.want {
				t.Fatalf("Diverging()=%v, want %v (created=%d completed=%d) — %s",
					got, tc.want, tc.tally.Created, tc.tally.Completed, tc.because)
			}
		})
	}
}

// TestClassify_PrecedenceIsFailedThenExternalThenIdle pins the ORDER, not just the mapping.
// Each case below deliberately satisfies several branches at once, so a reordering of the switch
// in Classify changes an answer rather than merely reordering equivalent checks.
//
// Mutant watched: moving the BlockedByOthers case above the anyFailed case turns
// "failed wins over every kind of leftover" red — which is the one that matters, because a
// failure reported as BLOCKED_EXTERNAL tells the reader to go chase another team.
func TestClassify_PrecedenceIsFailedThenExternalThenIdle(t *testing.T) {
	cases := []struct {
		name      string
		anyFailed bool
		queue     QueueState
		want      Terminal
	}{
		{
			name:      "failed wins over every kind of leftover",
			anyFailed: true,
			queue:     QueueState{Executable: 3, ExternallyBlocked: []BlockedWorkItem{{WorkItemID: "ext0", Blockers: []BlockerRef{{ID: "outsider"}}}, {WorkItemID: "ext1", Blockers: []BlockerRef{{ID: "outsider"}}}}, BlockedByMine: 1, Running: 1, Paused: 1},
			want:      TerminalFailed,
		},
		{
			name:      "failed wins even when the queue is empty",
			anyFailed: true,
			queue:     QueueState{},
			want:      TerminalFailed,
		},
		{
			name:      "external block wins over my own leftovers",
			anyFailed: false,
			queue:     QueueState{ExternallyBlocked: []BlockedWorkItem{{WorkItemID: "ext0", Blockers: []BlockerRef{{ID: "outsider"}}}}, BlockedByMine: 5, Executable: 2},
			want:      TerminalBlockedExternal,
		},
		{
			name:      "only my own leftovers is idle",
			anyFailed: false,
			queue:     QueueState{BlockedByMine: 2},
			want:      TerminalIdle,
		},
		{
			name:      "a paused work item alone keeps it from being completed",
			anyFailed: false,
			queue:     QueueState{Paused: 1},
			want:      TerminalIdle,
		},
		{
			name:      "a running work item alone keeps it from being completed",
			anyFailed: false,
			queue:     QueueState{Running: 1},
			want:      TerminalIdle,
		},
		{
			name:      "nothing left at all is completed",
			anyFailed: false,
			queue:     QueueState{},
			want:      TerminalCompleted,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.anyFailed, tc.queue); got != tc.want {
				t.Fatalf("Classify(anyFailed=%v, %+v)=%s, want %s",
					tc.anyFailed, tc.queue, got, tc.want)
			}
		})
	}
}

// TestQueueStateEmpty_CountsEveryBucket is the negative control for Classify's COMPLETED branch:
// it proves each bucket independently keeps Empty() false, so a future field added to QueueState
// and forgotten in Empty() cannot silently turn "work remains" into COMPLETED — the single most
// reassuring thing drain can say, and therefore the worst one to say by accident.
func TestQueueStateEmpty_CountsEveryBucket(t *testing.T) {
	if !(QueueState{}).Empty() {
		t.Fatal("the zero QueueState must be Empty()")
	}
	for name, q := range map[string]QueueState{
		"executable":        {Executable: 1},
		"blocked by mine":   {BlockedByMine: 1},
		"blocked by others": {ExternallyBlocked: []BlockedWorkItem{{WorkItemID: "ext0", Blockers: []BlockerRef{{ID: "outsider"}}}}},
		"running":           {Running: 1},
		"paused":            {Paused: 1},
	} {
		if q.Empty() {
			t.Errorf("QueueState with %s=1 reported Empty(); COMPLETED would be claimed with work outstanding", name)
		}
	}
}

// TestExitCode_IsInjectiveAndReservesZeroForSuccess pins notification layer ① (aihub#640
// `notification_three_layers`). Two properties, and both are load-bearing:
//
//   - INJECTIVE: the exit code is the only channel a shell script or a cron job has. Two
//     terminal states sharing a code would silently merge, and the pair most likely to be merged
//     by a careless edit is IDLE/BLOCKED_EXTERNAL — exactly the distinction the design says is
//     the whole point of having four states.
//   - Only COMPLETED is 0, and no state collides with the CLI's own 1 (usage) or 2 (internal).
func TestExitCode_IsInjectiveAndReservesZeroForSuccess(t *testing.T) {
	all := []Terminal{TerminalCompleted, TerminalIdle, TerminalBlockedExternal, TerminalFailed}
	seen := map[int]Terminal{}
	for _, tr := range all {
		code := ExitCode(tr)
		if prev, dup := seen[code]; dup {
			t.Fatalf("%s and %s share exit code %d; they would be indistinguishable to any caller",
				prev, tr, code)
		}
		seen[code] = tr

		if tr == TerminalCompleted && code != 0 {
			t.Errorf("COMPLETED must exit 0, got %d", code)
		}
		if tr != TerminalCompleted && code == 0 {
			t.Errorf("%s must not exit 0: it would read as success", tr)
		}
		if tr != TerminalCompleted && (code == 1 || code == 2) {
			t.Errorf("%s exits %d, colliding with the CLI's usage/internal-error codes", tr, code)
		}
		if code >= 126 {
			t.Errorf("%s exits %d, which POSIX shells reserve for exec failures and signals", tr, code)
		}
	}
	if got := ExitCode(Terminal("nonsense")); got == 0 {
		t.Error("an unrecognised Terminal must not exit 0: a state the code does not know is not a success")
	}
}

// TestParallelism_DefaultsBelowTheMeasuredCeiling guards the number against being "corrected"
// upward to the hard quota. aihub#640 `harness_survey_2026_09_13.concurrency_ceiling` measured
// Claude Code's hard limit at 20 concurrent subagents and the team's stable working value at
// about 9; aihub's orchestration memory records 12 losing 4 agents to HTTP 429 and then the
// gateway 503-ing the rest. The default belongs where throughput is, not at the cliff.
func TestParallelism_DefaultsBelowTheMeasuredCeiling(t *testing.T) {
	if got := (Budget{}).Parallelism(); got != DefaultMaxParallel {
		t.Fatalf("zero Budget parallelism = %d, want the default %d", got, DefaultMaxParallel)
	}
	if DefaultMaxParallel >= 20 {
		t.Fatalf("DefaultMaxParallel=%d is at or above the measured hard quota of 20 concurrent "+
			"subagents; the default must sit below the point where dispatch starts failing",
			DefaultMaxParallel)
	}
	if DefaultMaxParallel > 9 {
		t.Errorf("DefaultMaxParallel=%d exceeds the team's measured stable value of ~9", DefaultMaxParallel)
	}
	// Explicit values win, and a nonsensical one falls back rather than deadlocking at zero.
	if got := (Budget{MaxParallel: 3}).Parallelism(); got != 3 {
		t.Errorf("explicit MaxParallel=3 gave %d", got)
	}
	if got := (Budget{MaxParallel: -1}).Parallelism(); got != DefaultMaxParallel {
		t.Errorf("negative MaxParallel gave %d, want the default; 0 or less would stall the round", got)
	}
}

// TestRemainingWorkItems_UnboundedIsNotZero is the negative control for the budget check in
// Run: the loop stops when RemainingWorkItems returns 0, so an unbounded budget returning 0
// instead of -1 would make every run stop before its first round — a total failure that no
// individual unit below would notice.
func TestRemainingWorkItems_UnboundedIsNotZero(t *testing.T) {
	if got := (Budget{}).RemainingWorkItems(0); got != -1 {
		t.Fatalf("unbounded budget returned %d, want -1; 0 means 'stop now'", got)
	}
	if got := (Budget{}).RemainingWorkItems(9999); got != -1 {
		t.Fatalf("unbounded budget after 9999 work items returned %d, want -1", got)
	}
	b := Budget{MaxWorkItems: 5}
	for done, want := range map[int]int{0: 5, 4: 1, 5: 0, 6: 0} {
		if got := b.RemainingWorkItems(done); got != want {
			t.Errorf("MaxWorkItems=5, done=%d: got %d, want %d", done, got, want)
		}
	}
}

// TestAttempted_ExcludesCreated proves the round's "did anything happen" test counts only work
// items drain actually touched. Folding Created in would make a round that created work items but
// executed none look productive, and Run uses Attempted()==0 to break out of an otherwise
// infinite loop.
func TestAttempted_ExcludesCreated(t *testing.T) {
	tally := RoundTally{Completed: 1, Failed: 1, LockBlocked: 1, Paused: 1, Created: 7, Executable: 4}
	if got := tally.Attempted(); got != 4 {
		t.Fatalf("Attempted()=%d, want 4 (created work items are not attempts)", got)
	}
	if got := (RoundTally{Created: 3}).Attempted(); got != 0 {
		t.Fatalf("a round that only created work items reported %d attempts, want 0: "+
			"Run would never break out of the loop", got)
	}
}

// TestNeedsHuman_OnlyAGenuineFailure is the negative control for TerminalFailed's trigger. A lock
// race and a pause are ordinary control flow; counting either as needing a human would make
// FAILED fire on a healthy run where two people happened to drain the same project.
func TestNeedsHuman_OnlyAGenuineFailure(t *testing.T) {
	if !ResultFailed.NeedsHuman() {
		t.Error("ResultFailed must need a human")
	}
	for _, r := range []Result{ResultWrapped, ResultLockBlocked, ResultPaused, ResultClaimFailed} {
		if r.NeedsHuman() {
			t.Errorf("%s must not need a human: it is ordinary control flow", r)
		}
	}
}

// TestQueueState_AQueuedHumanSessionWorkItemIsNotAnEmptyQueue is finding ③(d), at the pure-logic
// layer where the lie was told.
//
// ObserveQueue classified what was left into executable / running / paused / blocked. A queued
// work item with requires_human_session != false is NONE of those: the server's ready predicate
// requires `requires_human_session = false`, so it never appears among the executable, and its
// `queued` status keeps it out of the other three. It fell through every bucket, Empty() reported
// the scope clear, and Classify returned COMPLETED — exit 0, documented as "every in-scope work
// item is terminal" — for a project with work still queued. `--plan` printed the same verdict.
//
// Mutants watched RED, each applied alone and `go build`-checked first:
//   - dropping `len(q.NeedsHumanSession) == 0` from Empty()  → the COMPLETED arm
//   - dropping the NeedsHumanSession clause from Classify()  → the BLOCKED_EXTERNAL arm
func TestQueueState_AQueuedHumanSessionWorkItemIsNotAnEmptyQueue(t *testing.T) {
	q := QueueState{NeedsHumanSession: []Candidate{{ID: "wi_1", Slug: "p#1"}}}

	if q.Empty() {
		t.Fatalf("a queue holding a queued requires_human_session work item reported Empty(). "+
			"That is how COMPLETED — the one terminal state that says there is nothing left to "+
			"come back for — was reported for a non-empty queue. state=%+v", q)
	}
	got := Classify(false, q)
	if got == TerminalCompleted {
		t.Fatalf("terminal = COMPLETED (exit %d) with work still queued", ExitCode(got))
	}
	// BLOCKED_EXTERNAL rather than IDLE, by the owner's own criterion for the split — "等下去有
	// 没有意义". Re-running drain tomorrow does not make a requires_human_session work item
	// executable; only a person does. IDLE's contract is the opposite ("no person needed;
	// re-running later is the fix"), so it would be wrong in the same direction as COMPLETED,
	// just less loudly.
	if got != TerminalBlockedExternal {
		t.Errorf("terminal = %s (exit %d), want BLOCKED_EXTERNAL: waiting accomplishes nothing "+
			"here, and IDLE is the state that deliberately notifies nobody", got, ExitCode(got))
	}
	if ExitCode(got) != 11 {
		t.Errorf("exit code = %d, want 11", ExitCode(got))
	}

	// Negative control: a genuinely empty queue must still be COMPLETED, exit 0, or
	// `polyforge drain && …` never chains and the whole exit-code layer is useless.
	if empty := (QueueState{}); !empty.Empty() || Classify(false, empty) != TerminalCompleted {
		t.Errorf("an empty queue no longer reports COMPLETED: empty=%v terminal=%s",
			empty.Empty(), Classify(false, empty))
	}
	// And a FAILURE still outranks it: the precedence order is what makes the exit code
	// actionable.
	if Classify(true, q) != TerminalFailed {
		t.Error("a failed run was reported as BLOCKED_EXTERNAL; FAILED must win")
	}
}
