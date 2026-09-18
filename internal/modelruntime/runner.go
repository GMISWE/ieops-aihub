package modelruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DefaultWaitDelay bounds how long Wait keeps draining the child's output
// pipes after the child exits — the orphan-holding-the-pipe case. It mirrors
// the reason internal/cli's drain runner sets WaitDelay: an orphaned
// grandchild holding the write end must not block the parent forever.
const DefaultWaitDelay = 10 * time.Second

// killWait bounds the final wait after the SIGKILL backstop. A process that
// survives SIGKILL is in uninterruptible I/O; the caller learns about it
// instead of blocking forever.
const killWait = 5 * time.Second

// groupPollInterval is how often the group-drain wait polls emptiness. One
// cheap kill(pid, 0) probe per tick — fine enough that a cooperating tree
// registers as gone within tens of milliseconds, coarse enough that Stop
// never becomes a busy loop.
const groupPollInterval = 10 * time.Millisecond

// Proc is one started harness process with bounded cleanup semantics.
type Proc struct {
	cmd     *exec.Cmd
	done    chan struct{}
	waitErr error

	mu      sync.Mutex
	stopped bool
	// stopErr records a failed Stop (a member that outlived the SIGKILL
	// backstop, or a signal error other than "already gone") so re-entering
	// callers see the same outcome instead of a silent success downgrade.
	stopErr error
	// stopDone is closed by the Stop call that owns the cleanup, on every
	// return path, exactly when that bounded cleanup is over. Concurrent
	// Stop callers receive on it so they report the OWNER's result instead
	// of noticing stopped=true with no outcome recorded yet and answering
	// nil on their own while the owner is still fighting a stubborn
	// descendant (the aihub#708 B2 bug).
	stopDone chan struct{}
}

// StartOptions parameterize Start.
type StartOptions struct {
	// WorkingDir is the child's cwd (the step's worktree).
	WorkingDir string
	// Stdout and Stderr receive the child's streams AS THEY ARE PRODUCED —
	// streaming is not tied to process exit. Either may be nil (discard).
	Stdout io.Writer
	Stderr io.Writer
	// WaitDelay bounds pipe drain after exit (default DefaultWaitDelay).
	WaitDelay time.Duration
}

// Start launches a Command as an isolated process-group leader with /dev/null
// stdin. The process group is the load-bearing part of bounded cleanup: a
// harness spawns subagents, MCP servers and git, and a signal to the leader
// alone orphans all of them (the measured 7.5-core load-generator incident in
// internal/cli/drain.go's header is the cautionary tale). Stop signals the
// GROUP.
//
// Stdin: os/exec gives a child with nil Stdin the null device, which is
// exactly what CloseStdin asks for — an unattended child's "waiting for
// input" becomes a fast EOF instead of a silent hang (measured: `pi -p` with
// an open stdin blocks forever producing nothing).
//
// No environment is added. Credentials live in the harnesses' own stores;
// this package never exports keys into a child's environment or argv.
func Start(command Command, opts StartOptions) (*Proc, error) {
	if command.Path == "" {
		return nil, errors.New("modelruntime: cannot start a command with no path")
	}
	waitDelay := opts.WaitDelay
	if waitDelay <= 0 {
		waitDelay = DefaultWaitDelay
	}
	cmd := exec.Command(command.Path, command.Args...)
	cmd.Dir = opts.WorkingDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = waitDelay
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("modelruntime: start %s: %w", command.Path, err)
	}
	p := &Proc{cmd: cmd, done: make(chan struct{}), stopDone: make(chan struct{})}
	go func() {
		defer close(p.done)
		p.waitErr = cmd.Wait()
	}()
	return p, nil
}

// PID returns the process-group leader's pid (0 before start).
func (p *Proc) PID() int {
	if p == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Done reports whether the process has exited and been reaped.
func (p *Proc) Done() bool {
	if p == nil {
		return true
	}
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// Wait blocks until the process exits and its output pipes are drained
// (bounded by WaitDelay). It returns the process's error: a non-zero exit
// yields an *exec.ExitError. A hung process is unblocked by Stop.
func (p *Proc) Wait() error {
	if p == nil {
		return errors.New("modelruntime: Wait on a nil Proc")
	}
	<-p.done
	return p.waitErr
}

// groupAlive reports whether the process group still has at least one
// member. It is a signal-0 probe: nil means members remain that we can see;
// EPERM means members remain that we may not signal (the group is still not
// gone); only ESRCH means the group is empty — reaped leader included or not.
// This is the identity at the heart of "group cleanup independent of leader
// reaping": the leader being reaped while a descendant lingers leaves the
// group alive, and a group that emptied before its leader was reaped is
// already gone.
func (p *Proc) groupAlive() bool {
	if p.cmd.Process == nil {
		return false
	}
	if err := syscall.Kill(-p.cmd.Process.Pid, 0); err != nil {
		return !errors.Is(err, syscall.ESRCH)
	}
	return true
}

// waitGroupEmpty blocks until every member of the process group is gone or
// the timeout passes, returning whether the group was observed empty. It
// deliberately does NOT wait on p.done: the leader being reaped proves
// nothing about the rest of the group — the exact case where Stop must keep
// going is a leader that exits promptly on SIGTERM while a descendant that
// ignores it stays behind holding the group id.
func (p *Proc) waitGroupEmpty(timeout time.Duration) bool {
	if !p.groupAlive() {
		return true
	}
	deadline := time.Now().Add(timeout)
	for {
		if !p.groupAlive() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(groupPollInterval)
	}
}

// signal sends sig to the whole process group. ESRCH — the group exited and
// was reaped before the signal — is the ordinary race and is translated to
// os.ErrProcessDone, never surfaced as a failure (a cancellation that worked
// must not be classified as an error by callers).
func (p *Proc) signal(sig syscall.Signal) error {
	if p.cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-p.cmd.Process.Pid, sig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

// Stop performs BOUNDED cleanup of the process tree: at most one SIGTERM to
// the group, a grace period, at most one SIGKILL to the group, then a bounded
// final wait. Success means THE GROUP IS EMPTY — not merely that the leader
// exited: a leader that handles SIGTERM and exits while a descendant ignores
// it and stays behind holding the group id is exactly the case Stop exists
// for, so every wait here is on group emptiness (groupAlive), never on the
// leader's reaping (p.done). A nil return therefore guarantees no member of
// the group we started is still running.
//
// Identity safety: the group id is the leader's pid, which can be recycled
// once the whole group has died. Stop therefore NEVER signals a group it has
// just observed empty — each real signal is preceded by a liveness probe, and
// once ESRCH is observed Stop stops signalling entirely and reports success.
// The residual window (probe saw members; the group then died and its id was
// recycled before the kill landed) is the irreducible TOCTOU of POSIX
// process groups; probe-then-signal pairing is what keeps the ordinary paths
// from ever firing at an innocent recycled id.
//
// The signal counts are bounded BY CONSTRUCTION (a stopped flag plus
// group-liveness probes), so a retry loop in a panicking caller cannot
// machine-gun SIGTERM at a group that is already gone — the property the
// fake-subprocess fixtures pin by counting the signals that actually arrive.
// Idempotent: a re-entering Stop signals nothing further and reports the
// group's truth as of THAT call — nil once the group is empty, the recorded
// failure while a member remains (so a caller that retries after an
// uninterruptible member learns when the blockage actually cleared).
//
// Concurrent callers: the first Stop to take the stopped flag owns the one
// bounded cleanup; every other call — racing it or arriving later — awaits
// that cleanup's result instead of answering for itself. A racing caller
// once read stopped=true with no failure recorded yet and returned nil
// while the owner was still waiting out its grace against a stubborn
// descendant (aihub#708 B2), handing a fallback a success that predated the
// group actually being empty; awaiting pins every caller to the SAME
// outcome, and the wait is itself bounded by the cleanup (grace + killWait).
//
// Returns a non-nil error only if a member outlives the SIGKILL backstop
// (uninterruptible I/O) or a signal fails for a reason other than "already
// gone".
func (p *Proc) Stop(grace time.Duration) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		// Another call owns the cleanup or already finished it. Await that
		// call's result: the owner closes stopDone on every return path
		// exactly when its bounded cleanup is over, so this receive can
		// never outlive the cleanup, and only past it does a nil return
		// mean the group is empty. Returning immediately here (aihub#708
		// B2) reported SUCCESS while the owner was still waiting out its
		// grace against a stubborn descendant — a fallback started on that
		// nil would run under a live process tree.
		<-p.stopDone
		p.mu.Lock()
		err := p.stopErr
		p.mu.Unlock()
		if err != nil && p.groupAlive() {
			return err
		}
		return nil
	}
	p.stopped = true
	p.mu.Unlock()
	// This call owns the one bounded cleanup. The defer is the handshake:
	// every racing or later caller above receives on stopDone and so sees
	// the outcome this call produced — never one of their own invention.
	// Defers run last, so the close happens after any recordStopErr write
	// below, giving awaiters a happens-before edge onto the result.
	defer close(p.stopDone)

	// Identity guard: never open with a signal at a group already observed
	// gone (the tree exited on its own and was reaped — pid recycling would
	// aim the TERM at an innocent group).
	if !p.groupAlive() {
		return nil
	}

	// One SIGTERM to the group: catchable, so a harness can reap its own
	// children on the way down.
	if err := p.signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return p.recordStopErr(fmt.Errorf("modelruntime: SIGTERM process group %d: %w", p.PID(), err))
	}
	if p.waitGroupEmpty(grace) {
		return nil
	}

	// Identity guard again: if the last stragglers exited between the failed
	// drain wait and here, the cleanup is DONE — signalling a pgid already
	// observed empty is how a recycled id gets hit.
	if !p.groupAlive() {
		return nil
	}
	// One SIGKILL backstop to the group, then a bounded final wait.
	if err := p.signal(syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return p.recordStopErr(fmt.Errorf("modelruntime: SIGKILL process group %d: %w", p.PID(), err))
	}
	if p.waitGroupEmpty(killWait) {
		return nil
	}
	return p.recordStopErr(fmt.Errorf("modelruntime: process group %d survived SIGKILL within %s; refusing to wait unbounded", p.PID(), killWait))
}

// recordStopErr stores a Stop failure so a concurrent or repeated Stop reports
// the same outcome instead of silently downgrading it to success. Guarded by
// p.mu so the race detector sees one writer per recorded outcome.
func (p *Proc) recordStopErr(err error) error {
	p.mu.Lock()
	p.stopErr = err
	p.mu.Unlock()
	return err
}

// runLocalCatalog runs one harness-local catalog command (e.g. `codex debug
// models`, `pi --list-models`, `opencode models`) and returns its stdout.
// These are local renders of the harness's own bundled catalog — they make no
// model requests. A short timeout keeps a wedged CLI from stalling preflight.
func runLocalCatalog(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = nil
	if err := cmd.Run(); err != nil {
		detail := bytes.TrimSpace(stderr.Bytes())
		if len(detail) > 200 {
			detail = detail[:200]
		}
		return nil, fmt.Errorf("%s %s: %w (stderr: %q)", name, strings.Join(args, " "), err, detail)
	}
	return stdout.Bytes(), nil
}
