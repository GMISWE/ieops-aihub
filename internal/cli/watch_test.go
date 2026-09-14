package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/drain"
)

// TestRunStateLabel_TellsADeadRunFromALiveOne is the assertion that earns runStateLabel its
// existence, and the reason it is not a boolean.
//
// An unattended scheduler can be OOM-killed, lose its terminal, or have its container restarted.
// The snapshot it leaves behind is byte-identical to the snapshot of a run that is still working
// — Finished is false in both — so "still going" and "died three hours ago" are the same file.
// The pid is the only thing that separates them, and on a container with no supervisor nothing
// else will ever notice.
//
// Mutant watched: dropping the processAlive check makes the dead case report "running", and
// watch --follow then redraws a frozen frame forever.
func TestRunStateLabel_TellsADeadRunFromALiveOne(t *testing.T) {
	live := &drain.Snapshot{PID: os.Getpid()}
	if got := runStateLabel(live); got != "running" {
		t.Fatalf("a live run reported %q, want running", got)
	}

	// PID 1 exists in every container but is not this run; what matters here is a pid that
	// certainly does NOT exist. Linux pids are bounded, and this one is above every default
	// pid_max, so it cannot be allocated.
	dead := &drain.Snapshot{PID: 0x7FFFFFFE}
	got := runStateLabel(dead)
	if !strings.Contains(got, "DIED") {
		t.Fatalf("a run whose process is gone reported %q; watch would show a frozen frame "+
			"forever and never say the scheduler is not running", got)
	}

	// A finished run must report its terminal state regardless of whether the process is
	// still around — the process exiting is the normal ending, not a death.
	done := &drain.Snapshot{PID: 0x7FFFFFFE, Finished: true, Terminal: drain.TerminalCompleted}
	if got := runStateLabel(done); !strings.Contains(got, string(drain.TerminalCompleted)) {
		t.Fatalf("a finished run reported %q, want its terminal state", got)
	}
	if strings.Contains(runStateLabel(done), "DIED") {
		t.Error("a run that finished normally was reported as DIED")
	}
}

// TestProcessAlive covers the primitive behind it.
func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("this very process was reported dead")
	}
	if processAlive(0x7FFFFFFE) {
		t.Error("an unallocatable pid was reported alive")
	}
}

// TestSnapshotScope_DistinguishesMineFromAll pins the line that tells a reader whether COMPLETED
// meant "my queue is empty" or "the project is empty" — the caveat the design requires watch to
// carry, since a local snapshot only ever describes one process on one machine.
func TestSnapshotScope_DistinguishesMineFromAll(t *testing.T) {
	if got := snapshotScope(&drain.Snapshot{ScopeAll: true}); got != "--all" {
		t.Errorf("snapshotScope(--all) = %q", got)
	}
	got := snapshotScope(&drain.Snapshot{ScopeUserID: "u_me"})
	if !strings.Contains(got, "u_me") {
		t.Errorf("snapshotScope = %q, want it to name the user it was scoped to", got)
	}
}

// TestBlockerLabel_NeverRendersAnEmptyBlocker guards the "who is blocking me" line. That is the
// one piece of server-only knowledge watch shows, resolved by drain when it hit the block; a
// blank rendering would turn the single most useful line into noise.
func TestBlockerLabel_NeverRendersAnEmptyBlocker(t *testing.T) {
	cases := []struct {
		in   drain.Blocker
		want string
	}{
		{drain.Blocker{WorkItem: "aihub#1", Actor: "dahe"}, "aihub#1 (dahe)"},
		{drain.Blocker{WorkItem: "aihub#1"}, "aihub#1"},
		{drain.Blocker{Actor: "dahe"}, "dahe"},
		{drain.Blocker{}, "another attempt"},
	}
	for _, tc := range cases {
		if got := blockerLabel(&tc.in); got != tc.want {
			t.Errorf("blockerLabel(%+v) = %q, want %q", tc.in, got, tc.want)
		}
		if strings.TrimSpace(blockerLabel(&tc.in)) == "" {
			t.Errorf("blockerLabel(%+v) rendered blank", tc.in)
		}
	}
}

// TestWatchUsage_SaysItIsNotTheProjectView pins the scope caveat in the help text itself. watch
// reads one process's local record; a reader who mistakes it for the project's state would read
// "nothing running" as "nothing to do".
func TestWatchUsage_SaysItIsNotTheProjectView(t *testing.T) {
	for _, needle := range []string{"ZERO network", "/pf-status", "this machine"} {
		if !strings.Contains(WatchUsage, needle) {
			t.Errorf("WatchUsage does not mention %q", needle)
		}
	}
}

// TestDrainUsage_DocumentsEveryExitCode pins notification layer 1 in the place an operator
// actually reads it. A scheduler whose exit codes are undocumented has three states nobody can
// act on.
func TestDrainUsage_DocumentsEveryExitCode(t *testing.T) {
	for _, terminal := range []drain.Terminal{
		drain.TerminalCompleted, drain.TerminalIdle,
		drain.TerminalBlockedExternal, drain.TerminalFailed,
	} {
		if !strings.Contains(DrainUsage, string(terminal)) {
			t.Errorf("DrainUsage never mentions the %s terminal state", terminal)
		}
	}
	for _, code := range []string{"10", "11", "12"} {
		if !strings.Contains(DrainUsage, code) {
			t.Errorf("DrainUsage never mentions exit code %s", code)
		}
	}
}

// TestSince_HandlesAClockThatWentBackwards is a small robustness check on the in-flight column:
// a snapshot written by a machine whose clock is slightly ahead must not render a negative age,
// which reads as a bug in watch rather than a clock skew.
func TestSince_HandlesAClockThatWentBackwards(t *testing.T) {
	if got := since("2999-01-01T00:00:00Z"); got != "0s" {
		t.Errorf("since(future) = %q, want 0s", got)
	}
	if got := since("not a timestamp"); got != "" {
		t.Errorf("since(garbage) = %q, want the empty string", got)
	}
}
