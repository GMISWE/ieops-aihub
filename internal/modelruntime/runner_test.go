package modelruntime

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// These tests run REAL subprocesses — but only the fake harness scripts in
// testdata/fakeharness, which touch nothing but a record file. No network,
// no model API, no harness credentials: they prove the runner's parameter
// passing (exact argv reaching the child, /dev/null stdin, streamed output)
// and its BOUNDED cleanup (one group SIGTERM reaching grandchildren, one
// SIGKILL backstop, idempotent stop).

func fakeHarness(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join("testdata", "fakeharness", script)
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func recordPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "record.txt")
}

func readRecord(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	return string(b)
}

// waitForRecord polls until the record file contains want, or fails after a
// bounded deadline. The child may not have created the file yet on the first
// poll, and group members may write their lines a moment after the leader
// exits; the poll keeps the assertion honest without sleeping blindly.
func waitForRecord(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			last = string(b)
			if strings.Contains(last, want) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("record never contained %q:\n%s", want, last)
}

func countLines(t *testing.T, path, prefix string) int {
	t.Helper()
	n := 0
	for _, line := range strings.Split(readRecord(t, path), "\n") {
		if strings.HasPrefix(line, prefix) && !strings.HasPrefix(line, "argv-count") {
			n++
		}
	}
	return n
}

// TestRunnerPassesExactArgv proves through a real exec that the argv built
// for a candidate arrives at the child VERBATIM: same count, same order,
// same elements — including the effort fragment and the prompt — and that
// the child's stdin is at EOF (the /dev/null behaviour that keeps `pi -p`
// from hanging forever on an open stdin).
func TestRunnerPassesExactArgv(t *testing.T) {
	rec := recordPath(t)
	t.Setenv("RECORD", rec)

	args := []string{"exec", "-s", "read-only", "--skip-git-repo-check", "-m", "gpt-6-astra",
		"-c", `model_reasoning_effort="high"`, "--ephemeral", "DO THE WORK"}
	var out bytes.Buffer
	p, err := Start(Command{
		Path:       fakeHarness(t, "echo_args.sh"),
		Args:       args,
		CloseStdin: true,
	}, StartOptions{Stdout: &out})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}

	body := readRecord(t, rec)
	if got := countLines(t, rec, "arg"); got != len(args) {
		t.Errorf("child received %d args, want %d:\n%s", got, len(args), body)
	}
	for i, want := range args {
		if !strings.Contains(body, "arg"+itoa(i)+":"+want+"\n") {
			t.Errorf("arg %d = want %q missing:\n%s", i, want, body)
		}
	}
	if !strings.Contains(body, "stdin:\n") {
		t.Errorf("stdin must be EOF-empty, not an open inherited stdin:\n%s", body)
	}
	if !strings.Contains(out.String(), "fake-stdout-line") {
		t.Errorf("stdout must stream to the writer, got %q", out.String())
	}
}

// TestRunnerStopSignalsTheWholeGroup proves bounded group cleanup: one Stop
// sends exactly one SIGTERM to the GROUP — the leader's recorded TERM count
// is the once-only witness — the whole tree, the grandchild group_tree.sh
// spawns included, is gone within the grace, and repeated Stops send no
// further signals.
//
// The grandchild's own "child-term:" marker is deliberately NOT asserted
// here: group_tree.sh announces "child-started:" BEFORE arming the child's
// TERM trap, so the group TERM can legitimately beat the trap and kill the
// child unmarked — a fixture race, not a production defect
// (stubborn_descendant.sh documents the arm-before-announce ordering a
// reliable marker needs). The grandchild is still covered without its
// marker: Stop returning nil IS the production group-empty verification
// (signal-0 observed ESRCH), and the elapsed bound sits well under the
// grace, so the whole tree died to the one group TERM and the SIGKILL
// backstop never fired.
func TestRunnerStopSignalsTheWholeGroup(t *testing.T) {
	rec := recordPath(t)
	t.Setenv("RECORD", rec)

	p, err := Start(Command{
		Path:       fakeHarness(t, "group_tree.sh"),
		Args:       nil,
		CloseStdin: true,
	}, StartOptions{})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForRecord(t, rec, "child-started:")

	start := time.Now()
	if err := p.Stop(3 * time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("a cooperating group must die well within the grace, took %s", elapsed)
	}

	// Stop's nil return above is the production group-empty contract: the
	// group was observed gone, so every member's final record write — the
	// leader's parent-term line included — has already landed, and no
	// polling is needed before reading it.
	body := readRecord(t, rec)
	if got := countLines(t, rec, "parent-term:"); got != 1 {
		t.Errorf("parent received %d SIGTERMs, want exactly 1 (bounded):\n%s", got, body)
	}
	if !strings.Contains(body, "parent-term:") {
		t.Errorf("group leader must have been signalled:\n%s", body)
	}

	// Repeated Stops are no-ops: no further signals may arrive.
	if err := p.Stop(time.Second); err != nil {
		t.Fatalf("second stop: %v", err)
	}
	if err := p.Stop(time.Second); err != nil {
		t.Fatalf("third stop: %v", err)
	}
	if got := countLines(t, rec, "parent-term:"); got != 1 {
		t.Errorf("idempotent stop must not re-signal, got %d TERMs:\n%s", got, readRecord(t, rec))
	}
	if err := p.Wait(); err == nil {
		t.Log("fake harness exited 0 after handling TERM")
	} else {
		var exitErr *exec.ExitError
		if !asExitError(err, &exitErr) {
			t.Errorf("wait after stop: %v", err)
		}
	}
}

// TestRunnerStopBoundedKillBackstop proves the stubborn case: a process
// that ignores SIGTERM is killed by the ONE SIGKILL backstop within a
// bounded time, and never gets to run its own exit path.
func TestRunnerStopBoundedKillBackstop(t *testing.T) {
	rec := recordPath(t)
	t.Setenv("RECORD", rec)

	p, err := Start(Command{
		Path:       fakeHarness(t, "stubborn.sh"),
		Args:       nil,
		CloseStdin: true,
	}, StartOptions{})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForRecord(t, rec, "stubborn-started:")

	start := time.Now()
	if err := p.Stop(300 * time.Millisecond); err != nil {
		t.Fatalf("stop: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Errorf("stubborn process must die bounded (grace + kill wait), took %s", elapsed)
	}
	if !p.Done() {
		t.Error("process must be reaped after the kill backstop")
	}
	// SIGKILL cannot be trapped: the script's own exit line must never run.
	time.Sleep(200 * time.Millisecond)
	if strings.Contains(readRecord(t, rec), "stubborn-exit:") {
		t.Error("stubborn process ran its own exit path; the kill backstop did not bound it")
	}
}

// TestRunnerStopNotSuccessWhileDescendantHoldsGroup proves the aihub#708 B5
// repair: a leader that exits promptly on SIGTERM while its descendant
// IGNORES the signal must not let Stop report success on the leader's
// reaping alone. Success is THE GROUP BEING EMPTY: the grace must elapse
// (the descendant holds the group through it), the bounded SIGKILL
// backstop must clear the descendant, and only then may Stop return nil.
func TestRunnerStopNotSuccessWhileDescendantHoldsGroup(t *testing.T) {
	rec := recordPath(t)
	t.Setenv("RECORD", rec)

	p, err := Start(Command{
		Path:       fakeHarness(t, "stubborn_descendant.sh"),
		Args:       nil,
		CloseStdin: true,
	}, StartOptions{})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForRecord(t, rec, "child-started:")

	start := time.Now()
	if err := p.Stop(300 * time.Millisecond); err != nil {
		t.Fatalf("stop: %v", err)
	}
	elapsed := time.Since(start)

	// The leader dies on the first TERM within milliseconds. A Stop that
	// waits on the leader's reaping returns long before the grace (the old
	// bug: nil while the stubborn descendant still ran); here the grace
	// MUST elapse because the descendant ignores TERM and holds the group.
	if elapsed < 250*time.Millisecond {
		t.Errorf("Stop returned in %s, before the grace elapsed: it short-circuited on the leader's exit while a descendant still held the group", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("cleanup must stay bounded (grace + kill wait), took %s", elapsed)
	}

	// The load-bearing assertion: Stop reporting success means no member of
	// the group remains — group emptiness, not leader reaping.
	if p.groupAlive() {
		t.Error("Stop reported success but the process group still has a member: leader reaping is not group cleanup")
	}
	if !p.Done() {
		t.Error("leader must be reaped after Stop")
	}

	// Repeated Stop signals nothing further and reports the group's current
	// truth — gone, so nil.
	if err := p.Stop(time.Second); err != nil {
		t.Fatalf("second stop: %v", err)
	}
	if p.groupAlive() {
		t.Error("second stop left or resurrected a group member")
	}

	// The descendant SURVIVES the one group TERM it records (that is the
	// stubborn shape) and dies to the SIGKILL backstop; the leader had no
	// handler and never got to write a term line of its own.
	body := readRecord(t, rec)
	if strings.Contains(body, "parent-term:") {
		t.Errorf("leader was expected to die on default TERM without a handler:\n%s", body)
	}
	if n := strings.Count(body, "child-started:"); n != 1 {
		t.Errorf("want exactly one descendant start, got %d:\n%s", n, body)
	}
	if got := countLines(t, rec, "child-term:"); got != 1 {
		t.Errorf("descendant received %d SIGTERMs, want exactly 1 (group signalled once):\n%s", got, body)
	}
}

// stopOutcome pairs one Stop call's result with the moment it returned, so
// the concurrent-caller regression can order two racing Stops against each
// other and against the cleanup they contend for.
type stopOutcome struct {
	err      error
	finished time.Time
}

// waitForLeaderReaped polls until the group leader has exited and been
// reaped (p.done closed), bounding the wait. In the stubborn-descendant
// shape the leader dies ONLY from the group SIGTERM that a running Stop
// sent, so its reaping is positive proof that a Stop's cleanup is in
// flight — the synchronization point the concurrent-caller regression
// releases its second caller at.
func waitForLeaderReaped(t *testing.T, p *Proc) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p.Done() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("leader was never reaped; the owning Stop never signalled the group")
}

// TestRunnerStopConcurrentCallersAwaitSameCleanup proves the aihub#708 B2
// repair: while caller A's bounded cleanup is still running — leader
// already TERM'd and reaped, stubborn descendant ignoring TERM through the
// grace — a concurrent caller B must AWAIT A's result, never observe
// stopped=true with no outcome recorded and answer nil on its own. The old
// bug let B return success a full grace before the SIGKILL backstop
// cleared the descendant, so a fallback keyed on B's nil could start under
// a live process tree. Synchronized, not sleep-raced: B is released
// exactly when the leader's reaping proves A's cleanup is in flight.
func TestRunnerStopConcurrentCallersAwaitSameCleanup(t *testing.T) {
	rec := recordPath(t)
	t.Setenv("RECORD", rec)

	p, err := Start(Command{
		Path:       fakeHarness(t, "stubborn_descendant.sh"),
		Args:       nil,
		CloseStdin: true,
	}, StartOptions{})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForRecord(t, rec, "child-started:")

	// Caller A goes first and becomes the cleanup owner. Its grace is the
	// window during which the descendant holds the group no matter what.
	const grace = 300 * time.Millisecond
	aStarted := time.Now()
	resA := make(chan stopOutcome, 1)
	go func() {
		err := p.Stop(grace)
		resA <- stopOutcome{err: err, finished: time.Now()}
	}()

	// Synchronize: the leader only ever dies from A's group SIGTERM, so its
	// reaping proves the TERM went out and A's cleanup is in flight; the
	// descendant still holding the group proves the grace is still elapsing.
	waitForLeaderReaped(t, p)
	if !p.groupAlive() {
		t.Fatal("synchronization lost: the stubborn descendant must still hold the group while the owning Stop's cleanup runs")
	}

	// Caller B enters MID-cleanup. B's own grace is deliberately different
	// and irrelevant: B must await A's result, not run a cleanup of its own.
	bEntered := time.Now()
	resB := make(chan stopOutcome, 1)
	go func() {
		err := p.Stop(time.Second)
		resB <- stopOutcome{err: err, finished: time.Now()}
	}()

	// B is collected FIRST: with the old bug it returned ~a full grace
	// before A, and the liveness probe below catches its nil live.
	b := <-resB
	if b.err != nil {
		t.Fatalf("concurrent caller B: %v", b.err)
	}
	if p.groupAlive() {
		t.Fatal("concurrent caller B reported success while the process group still had a live member: it answered before the owning cleanup finished (aihub#708 B2)")
	}
	// B entered while the grace was still elapsing, so awaiting the owner's
	// bounded cleanup must have blocked B for most of the remaining grace —
	// the old bug returned B in microseconds.
	if blocked := b.finished.Sub(bEntered); blocked < 100*time.Millisecond {
		t.Errorf("concurrent caller B returned after only %s; it did not await the in-flight cleanup (the descendant held the group for the full %s grace)", blocked, grace)
	}

	a := <-resA
	if a.err != nil {
		t.Fatalf("caller A (cleanup owner): %v", a.err)
	}
	if aDur := a.finished.Sub(aStarted); aDur < 250*time.Millisecond {
		t.Errorf("owning Stop returned in %s, before the grace elapsed: the stubborn descendant must hold the group through it", aDur)
	}
	if pair := time.Since(aStarted); pair > 3*time.Second {
		t.Errorf("two concurrent Stops must stay bounded (grace + kill wait), took %s", pair)
	}
	// Both awaited the SAME result: B wakes when the owner's cleanup ends,
	// so its return may trail A's by scheduling jitter only — never lead it
	// by a meaningful margin (the old bug led by a full grace).
	if b.finished.Before(a.finished.Add(-100 * time.Millisecond)) {
		t.Errorf("concurrent caller B returned %s BEFORE the owning cleanup finished: it observed stopped=true mid-cleanup and answered early", a.finished.Sub(b.finished))
	}

	// One group TERM total despite two callers: the owner's. A B that
	// started a second cleanup would re-signal the group.
	waitForRecord(t, rec, "child-term:")
	if got := countLines(t, rec, "child-term:"); got != 1 {
		t.Errorf("descendant received %d SIGTERMs, want exactly 1 (concurrent Stop must not start a second cleanup):\n%s", got, readRecord(t, rec))
	}

	// Sequential idempotency survives the repair: a later Stop still
	// signals nothing and still reports the group's truth.
	if err := p.Stop(time.Second); err != nil {
		t.Fatalf("sequential stop after the concurrent pair: %v", err)
	}
	if p.groupAlive() {
		t.Error("post-cleanup stop left or resurrected a group member")
	}
	if got := countLines(t, rec, "child-term:"); got != 1 {
		t.Errorf("sequential stop re-signalled the group (%d TERMs, want exactly 1):\n%s", got, readRecord(t, rec))
	}
}

// TestRunnerWaitReturnsExitError pins the honest exit status: a non-zero
// child surfaces as an error the controller can classify.
func TestRunnerWaitReturnsExitError(t *testing.T) {
	p, err := Start(Command{Path: "/bin/sh", Args: []string{"-c", "exit 7"}, CloseStdin: true}, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitErr := p.Wait()
	var exitErr *exec.ExitError
	if !asExitError(waitErr, &exitErr) {
		t.Fatalf("want ExitError, got %v", waitErr)
	}
	if exitErr.ExitCode() != 7 {
		t.Errorf("exit code = %d, want 7", exitErr.ExitCode())
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}

func asExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

// TestRunnerAddsNothingToEnv pins the no-secrets-env half of the package's
// guarantee: Start never touches cmd.Env, so the child inherits the parent's
// environment VERBATIM — no injected credentials, no exported key material,
// no polyforge config smuggled into the child. Credentials live in the
// harnesses' own stores; if a future change starts exporting anything here,
// this fails before a secret reaches a child's environment.
func TestRunnerAddsNothingToEnv(t *testing.T) {
	rec := recordPath(t)
	t.Setenv("RECORD", rec)
	parent := os.Environ()
	sort.Strings(parent)

	p, err := Start(Command{
		Path:       fakeHarness(t, "env_probe.sh"),
		Args:       nil,
		CloseStdin: true,
	}, StartOptions{})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}

	child := strings.Split(strings.TrimSpace(readRecord(t, rec)), "\n")
	sort.Strings(child)
	if len(parent) == 0 || len(child) == 0 {
		t.Fatalf("env probe produced nothing: parent=%d child=%d", len(parent), len(child))
	}
	if !reflect.DeepEqual(parent, child) {
		t.Errorf("child environment differs from the parent's (Start must add NOTHING):\n only-in-child: %v\n only-in-parent: %v",
			setDiff(child, parent), setDiff(parent, child))
	}
}

func setDiff(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, s := range b {
		set[s] = true
	}
	var out []string
	for _, s := range a {
		if !set[s] {
			out = append(out, s)
		}
	}
	return out
}
