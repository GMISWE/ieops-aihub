package main

// aihub#679 — the one subprocess the MCP server runs before it starts serving
// must be bounded.
//
// 🔴 THE FAILURE MODE IS NOT AN ERROR, WHICH IS WHY NOTHING CAUGHT IT. main()'s
// serve path calls generateCodexProfiles when `codex` is on PATH, its errors are
// warnings, and every error path is therefore harmless. A codex that HANGS
// returns from no path at all: exec.Command has no deadline, so
// codexProfileCatalogProbe.load() blocks, main() never reaches server.Serve, and
// the process never speaks a byte of MCP. Each Claude Code / pi session starts
// its own polyforge MCP server, so a single wedged codex binary presents on this
// machine as "MCP will not connect" in every session at once — with no log line,
// because the block is before the first one.
//
// The arm below runs a real subprocess that really hangs. A fake that returned
// context.DeadlineExceeded would be asserting that a mocked deadline fires;
// what has to be true is that a genuinely unresponsive child process is
// abandoned, which only a genuinely unresponsive child process can show.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeCodexOnPATH writes an executable named `codex` into a fresh directory,
// points PATH at it alone, and returns nothing: the point is the side effect.
//
// PATH is replaced rather than prepended so a real codex installed on the
// machine running the tests cannot answer instead of the fixture — which would
// make the hang arm pass for the wrong reason (a fast real answer is also "not a
// hang") on exactly the developer machines most likely to have one.
func fakeCodexOnPATH(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fixture is a /bin/sh script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", dir)

	// The control for the fixture itself: if `codex` does not resolve to the
	// script, every arm below measures the absence of a binary rather than the
	// behaviour of one.
	resolved, err := exec.LookPath("codex")
	if err != nil || resolved != path {
		t.Fatalf("the fake codex is not what `codex` resolves to (got %q, %v) — the arms "+
			"below would be testing exec.LookPath failing, not the probe's deadline",
			resolved, err)
	}
}

// killRecordedPID kills the process whose pid the fixture wrote, if it is still
// alive. Best-effort by design: the process being gone is the good case, and a
// test must not fail because its cleanup had nothing to do.
func killRecordedPID(t *testing.T, pidFile string) {
	t.Helper()
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return // the fixture never got far enough to fork
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = proc.Kill()
	_, _ = proc.Wait()
}

// TestCodexProbeAbandonsAHangingCodex is the aihub#679 gate.
func TestCodexProbeAbandonsAHangingCodex(t *testing.T) {
	t.Run("a_hanging_codex_is_abandoned_at_the_deadline", func(t *testing.T) {
		// ⚠️ RESOLVED BEFORE PATH IS REPLACED, and the first draft of this test got
		// it wrong in a way worth leaving a note about: fakeCodexOnPATH sets PATH
		// to the fixture directory ALONE, so a script body of `sleep 600` exits
		// 127 instantly — the probe then returned in 1.3ms with "exit status 127"
		// and the arm was measuring a missing binary, not a deadline. The
		// `elapsed < probeTimeout` clause below is what caught it, which is why
		// that clause is not redundant with the upper bound.
		sleepBin, err := exec.LookPath("sleep")
		if err != nil {
			t.Skipf("no sleep binary to build a hanging fixture from: %v", err)
		}

		// Sleeps far longer than the test could tolerate, and longer than the
		// production timeout too, so "it finished on its own" is not an available
		// explanation for the probe returning.
		//
		// Backgrounded-and-waited rather than exec'd, for two reasons that pull the
		// same way. It keeps the shape the bug needs — sh forks, so a grandchild
		// outlives the killed shell holding the stdout pipe, which `exec sleep`
		// (one process, dies with the kill) would NOT reproduce. And it yields a
		// pid to clean up: WaitDelay unblocks polyforge without reaping the
		// grandchild (documented at codexProbeWaitDelay), so without the Cleanup
		// below every run of this test would leave a 10-minute sleep behind on the
		// CI box.
		pidFile := filepath.Join(t.TempDir(), "sleep.pid")
		fakeCodexOnPATH(t, sleepBin+" 600 & echo $! > "+pidFile+"; wait")
		t.Cleanup(func() { killRecordedPID(t, pidFile) })

		const probeTimeout = 400 * time.Millisecond
		p := &codexProfileCatalogProbe{timeout: probeTimeout}

		start := time.Now()
		ok, probeErr := p.HasModel("gpt-5-codex")
		elapsed := time.Since(start)

		// 🔴 THE ASSERTION, and the bound is the REAL one rather than a loose
		// sanity check. The worst case is the deadline plus the WaitDelay grace,
		// so the slack below is for scheduling only; a fix that bounded the
		// deadline but not the pipe would blow straight through it.
		//
		// ⚠️ THE FIXTURE'S SHAPE IS THE TEST. `#!/bin/sh` running an absolute
		// `sleep` is a PARENT AND A GRANDCHILD, and that is deliberate: with only
		// context cancellation and no cmd.WaitDelay, the shell is killed on time,
		// the sleep survives holding the stdout pipe, and Output() never returns.
		// Measured during aihub#679 — the probe did not come back at all and the
		// test had to be killed after 2m33s with /usr/bin/sleep still running. A
		// fixture that hung in a single process would have passed against the
		// half-fix.
		wantBound := probeTimeout + codexProbeWaitDelay + 5*time.Second
		if elapsed > wantBound {
			t.Fatalf("the probe took %s (bound %s) against a codex that sleeps for 600s. "+
				"Either the deadline or the WaitDelay is not in force, and on the boot path "+
				"that is every MCP session on the machine failing to connect.",
				elapsed, wantBound)
		}
		if elapsed < probeTimeout {
			t.Errorf("the probe returned in %s, BEFORE its own %s deadline. Something other "+
				"than the timeout ended it — most likely the fake codex was not executed at "+
				"all — so this arm is not measuring the deadline.", elapsed, probeTimeout)
		}
		if probeErr == nil {
			t.Fatal("a probe that gave up reported no error. The caller conservatively " +
				"treats every model as unavailable only when it sees one; a nil error here " +
				"means generation would trust an empty catalog.")
		}
		if ok {
			t.Errorf("HasModel returned true from a probe that never read a catalog — " +
				"generation must never guess a model ID")
		}

		// The message has to say it gave up. `Output()` alone reports a killed
		// child as "signal: killed", which reads as "somebody killed codex"
		// rather than "polyforge stopped waiting" — and that distinction is the
		// whole value of the log line to whoever is debugging a slow boot.
		msg := probeErr.Error()
		for _, want := range []string{"gave up", "codex debug models"} {
			if !strings.Contains(msg, want) {
				t.Errorf("the timeout error %q does not contain %q", msg, want)
			}
		}
		if strings.Contains(msg, "signal: killed") && !strings.Contains(msg, "deadline") {
			t.Errorf("the timeout is reported as a raw kill (%q) with no mention of the "+
				"deadline that caused it", msg)
		}
	})

	t.Run("control_a_responsive_codex_is_still_parsed", func(t *testing.T) {
		// 🔴 WITHOUT THIS, "the probe returns quickly with an error" is also what
		// a probe that never runs codex at all produces — and that would silently
		// disable aihub#642 AC6's live catalog validation while every arm above
		// stayed green.
		fakeCodexOnPATH(t, `printf '{"models":[{"slug":"gpt-5-codex"},{"slug":"o3"}]}'`)

		p := &codexProfileCatalogProbe{timeout: 5 * time.Second}
		ok, err := p.HasModel("gpt-5-codex")
		if err != nil {
			t.Fatalf("a codex that answers immediately was treated as a failure: %v", err)
		}
		if !ok {
			t.Error("HasModel(gpt-5-codex) = false against a catalog that lists it — the " +
				"timeout change broke the parse it was supposed to leave alone")
		}
		// The other half of the catalog contract: a model NOT listed is absent.
		// Without it, "returns true" is satisfied by a probe that says yes to
		// everything.
		if absent, err := p.HasModel("a-model-that-is-not-in-the-catalog"); err != nil || absent {
			t.Errorf("HasModel(absent) = %v, %v; want false, nil", absent, err)
		}
	})

	t.Run("control_the_probe_runs_at_most_once", func(t *testing.T) {
		// sync.Once is what keeps the boot cost to ONE timeout rather than one per
		// role. A script that appends a line per invocation counts them.
		dir := t.TempDir()
		counter := filepath.Join(dir, "calls")
		fakeCodexOnPATH(t, `echo x >> `+counter+`; printf '{"models":[{"slug":"m1"}]}'`)

		p := &codexProfileCatalogProbe{timeout: 5 * time.Second}
		for range 3 {
			if _, err := p.HasModel("m1"); err != nil {
				t.Fatalf("HasModel: %v", err)
			}
		}
		b, err := os.ReadFile(counter)
		if err != nil {
			t.Fatalf("the fake codex never ran at all: %v", err)
		}
		if n := strings.Count(string(b), "x"); n != 1 {
			t.Errorf("codex ran %d times for 3 HasModel calls, want 1. Each run costs up to "+
				"the full deadline on the boot path, so losing sync.Once multiplies the "+
				"stall by the number of roles.", n)
		}
	})
}

// TestCodexProbeTimeoutDefaultIsBounded pins the SHIPPED value, which the arms
// above deliberately override.
//
// 🔴 EVERY ARM ABOVE PASSES WITH codexProbeTimeout SET TO A WEEK, because they
// all construct the probe with their own. This is the only assertion that sees
// the number production actually boots with.
func TestCodexProbeTimeoutDefaultIsBounded(t *testing.T) {
	if codexProbeTimeout <= 0 {
		t.Fatalf("codexProbeTimeout = %s; a non-positive timeout makes the context expire "+
			"immediately and disables the probe outright rather than bounding it",
			codexProbeTimeout)
	}
	// The ceiling is about the BOOT, not about codex. This runs before
	// server.Serve on every MCP session start, so the value is the worst-case
	// delay before an agent can talk to polyforge at all.
	if codexProbeTimeout > 15*time.Second {
		t.Errorf("codexProbeTimeout = %s. This is paid before the MCP server begins "+
			"serving, on every session on the machine; anything on this scale is the "+
			"outage aihub#679 closed, just with an eventual end.", codexProbeTimeout)
	}
	// And a floor: `codex debug models` is a local process, but a cold start on a
	// loaded machine is not instant, and a timeout short enough to fire on a
	// healthy codex would silently disable live catalog validation.
	if codexProbeTimeout < 1*time.Second {
		t.Errorf("codexProbeTimeout = %s is short enough to expire on a healthy but cold "+
			"codex, which turns aihub#642 AC6's live catalog check off without saying so.",
			codexProbeTimeout)
	}
	// The zero-value path is what production uses: nothing in main() sets the
	// field, so a probe built with the zero value must get the default.
	if got := (&codexProfileCatalogProbe{}).effectiveTimeout(); got != codexProbeTimeout {
		t.Errorf("a zero-value probe resolves to %s, not the default %s — production "+
			"constructs it that way and would run unbounded again", got, codexProbeTimeout)
	}
}
