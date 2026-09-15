package cli

// aihub#679 — a harness model-catalog subprocess must be bounded.
// aihub#683 — and ALL THREE of them must be, not just codex's.
//
// 🔴 THE FAILURE MODE IS NOT AN ERROR, WHICH IS WHY NOTHING CAUGHT IT. Every
// caller treats a probe failure as a warning and carries on, so every error path
// is harmless. A CLI that HANGS returns from no path at all: exec.Command has no
// deadline, so load() blocks forever. Before aihub#683 that only mattered for
// codex, the one probe on the serve path; that change put pi's and opencode's
// there too, and they were unbounded.
//
// The hang arms run a real subprocess that really hangs. A fake returning
// context.DeadlineExceeded would be asserting that a mocked deadline fires; what
// has to be true is that a genuinely unresponsive child process is abandoned,
// which only a genuinely unresponsive child process can show.

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

// fakeHarnessCLIOnPATH writes an executable named `bin` into a fresh directory,
// points PATH at it alone, and returns nothing: the point is the side effect.
//
// PATH is REPLACED rather than prepended so a real codex/pi/opencode installed on
// the machine running the tests cannot answer instead of the fixture — which
// would make a hang arm pass for the wrong reason (a fast real answer is also
// "not a hang") on exactly the developer machines most likely to have one. This
// box has all three installed, so the distinction is not hypothetical.
func fakeHarnessCLIOnPATH(t *testing.T, bin, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fixture is a /bin/sh script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, bin)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatalf("write fake %s: %v", bin, err)
	}
	t.Setenv("PATH", dir)

	// The control for the fixture itself: if `bin` does not resolve to the
	// script, every arm below measures the absence of a binary rather than the
	// behaviour of one.
	resolved, err := exec.LookPath(bin)
	if err != nil || resolved != path {
		t.Fatalf("the fake %s is not what %q resolves to (got %q, %v) — the arms below would be "+
			"testing exec.LookPath failing, not the probe's deadline", bin, bin, resolved, err)
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

// hangingFixture installs a `bin` on PATH that sleeps for 600s, and arranges for
// the orphaned sleep to be killed afterwards.
//
// ⚠️ THE FIXTURE'S SHAPE IS THE TEST. `#!/bin/sh` running an absolute `sleep` is
// a PARENT AND A GRANDCHILD, deliberately: with only context cancellation and no
// cmd.WaitDelay, the shell is killed on time, the sleep survives holding the
// stdout pipe, and Output() never returns. Measured during aihub#679 — the probe
// did not come back at all and the test had to be killed after 2m33s with
// /usr/bin/sleep still running. A fixture that hung in a single process would
// have passed against the half-fix.
func hangingFixture(t *testing.T, bin string) {
	t.Helper()
	// ⚠️ RESOLVED BEFORE PATH IS REPLACED, and the first draft of this test got it
	// wrong in a way worth leaving a note about: fakeHarnessCLIOnPATH sets PATH to
	// the fixture directory ALONE, so a script body of `sleep 600` exits 127
	// instantly — the probe then returned in 1.3ms with "exit status 127" and the
	// arm was measuring a missing binary, not a deadline. The `elapsed <
	// probeTimeout` clause below is what caught it, which is why that clause is
	// not redundant with the upper bound.
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary to build a hanging fixture from: %v", err)
	}
	pidFile := filepath.Join(t.TempDir(), "sleep.pid")
	// Backgrounded-and-waited rather than exec'd: it keeps the parent+grandchild
	// shape the bug needs, and it yields a pid to clean up. WaitDelay unblocks
	// polyforge without reaping the grandchild (documented at
	// catalogProbeWaitDelay), so without the Cleanup every run of this test would
	// leave a 10-minute sleep behind on the CI box.
	fakeHarnessCLIOnPATH(t, bin, sleepBin+" 600 & echo $! > "+pidFile+"; wait")
	t.Cleanup(func() { killRecordedPID(t, pidFile) })
}

const testProbeTimeout = 400 * time.Millisecond

// assertAbandonedAtDeadline is the shared body of the three hang arms.
func assertAbandonedAtDeadline(t *testing.T, display string, probe CatalogProbe) {
	t.Helper()

	start := time.Now()
	ok, probeErr := probe.HasModel("some-model")
	elapsed := time.Since(start)

	// 🔴 THE ASSERTION, and the bound is the REAL one rather than a loose sanity
	// check. The worst case is the deadline plus the WaitDelay grace, so the slack
	// below is for scheduling only; a fix that bounded the deadline but not the
	// pipe would blow straight through it.
	wantBound := testProbeTimeout + catalogProbeWaitDelay + 5*time.Second
	if elapsed > wantBound {
		t.Fatalf("the %s probe took %s (bound %s) against a CLI that sleeps for 600s. Either "+
			"the deadline or the WaitDelay is not in force.", display, elapsed, wantBound)
	}
	if elapsed < testProbeTimeout {
		t.Errorf("the %s probe returned in %s, BEFORE its own %s deadline. Something other than "+
			"the timeout ended it — most likely the fake CLI was not executed at all — so this "+
			"arm is not measuring the deadline.", display, elapsed, testProbeTimeout)
	}
	if probeErr == nil {
		t.Fatalf("the %s probe gave up but reported no error. The caller conservatively treats "+
			"every model as unavailable only when it sees one; a nil error here means generation "+
			"would trust an empty catalog.", display)
	}
	if ok {
		t.Errorf("%s HasModel returned true from a probe that never read a catalog — generation "+
			"must never guess a model ID", display)
	}

	// The message has to say it gave up, and name the command. `Output()` alone
	// reports a killed child as "signal: killed", which reads as "somebody killed
	// the CLI" rather than "polyforge stopped waiting" — and that distinction is
	// the whole value of the log line to whoever is debugging a silent boot.
	msg := probeErr.Error()
	for _, want := range []string{"gave up", display} {
		if !strings.Contains(msg, want) {
			t.Errorf("the timeout error %q does not contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "signal: killed") && !strings.Contains(msg, "deadline") {
		t.Errorf("the timeout is reported as a raw kill (%q) with no mention of the deadline "+
			"that caused it", msg)
	}
}

// TestCatalogProbesAbandonAHangingCLI is the aihub#679 gate, widened by
// aihub#683 to every harness that can reach the serve path.
//
// 🔴 THE pi AND opencode ARMS ARE NOT SYMMETRY FOR ITS OWN SAKE. Both probes ran
// with no deadline at all until this change, which was survivable only while
// nothing but an install script invoked them. SyncRolesOnStartup invokes both on
// every MCP server boot.
func TestCatalogProbesAbandonAHangingCLI(t *testing.T) {
	t.Run("codex", func(t *testing.T) {
		hangingFixture(t, "codex")
		assertAbandonedAtDeadline(t, "codex debug models",
			&codexCatalogProbe{boundedProbe: boundedProbe{timeout: testProbeTimeout}})
	})

	t.Run("pi", func(t *testing.T) {
		hangingFixture(t, "pi")
		assertAbandonedAtDeadline(t, "pi --list-models",
			&piCatalogProbe{boundedProbe: boundedProbe{timeout: testProbeTimeout}})
	})

	t.Run("opencode", func(t *testing.T) {
		hangingFixture(t, "opencode")
		assertAbandonedAtDeadline(t, "opencode models",
			&opencodeCatalogProbe{boundedProbe: boundedProbe{timeout: testProbeTimeout}})
	})

	t.Run("control_a_responsive_codex_is_still_parsed", func(t *testing.T) {
		// 🔴 WITHOUT THIS, "the probe returns quickly with an error" is also what a
		// probe that never runs the CLI at all produces — and that would silently
		// disable aihub#642 AC6's live catalog validation while every arm above
		// stayed green.
		fakeHarnessCLIOnPATH(t, "codex", `printf '{"models":[{"slug":"gpt-5-codex"},{"slug":"o3"}]}'`)

		p := &codexCatalogProbe{boundedProbe: boundedProbe{timeout: 5 * time.Second}}
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

	t.Run("control_a_responsive_opencode_is_still_parsed", func(t *testing.T) {
		// The same control for the harness whose real latency forced the ceiling
		// up: routing it through the shared runner must not have changed its parse.
		fakeHarnessCLIOnPATH(t, "opencode", `printf 'anthropic/claude-sonnet-4-5\nopenai/gpt-5\n'`)

		p := &opencodeCatalogProbe{boundedProbe: boundedProbe{timeout: 5 * time.Second}}
		if ok, err := p.HasModel("anthropic/claude-sonnet-4-5"); err != nil || !ok {
			t.Errorf("HasModel(listed) = %v, %v; want true, nil", ok, err)
		}
		if ok, err := p.HasModel("anthropic/not-listed"); err != nil || ok {
			t.Errorf("HasModel(absent) = %v, %v; want false, nil", ok, err)
		}
	})

	t.Run("control_the_probe_runs_at_most_once", func(t *testing.T) {
		// sync.Once is what keeps the cost to ONE timeout rather than one per role.
		// A script that appends a line per invocation counts them.
		dir := t.TempDir()
		counter := filepath.Join(dir, "calls")
		fakeHarnessCLIOnPATH(t, "codex", `echo x >> `+counter+`; printf '{"models":[{"slug":"m1"}]}'`)

		p := &codexCatalogProbe{boundedProbe: boundedProbe{timeout: 5 * time.Second}}
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
			t.Errorf("codex ran %d times for 3 HasModel calls, want 1. Each run costs up to the "+
				"full deadline, so losing sync.Once multiplies the stall by the number of roles.", n)
		}
	})
}

// TestCatalogProbeTimeoutDefaultIsBounded pins the SHIPPED value, which the arms
// above deliberately override.
//
// 🔴 EVERY ARM ABOVE PASSES WITH catalogProbeTimeout SET TO A WEEK, because they
// all construct their probe with their own. This is the only assertion that sees
// the number production actually runs with.
func TestCatalogProbeTimeoutDefaultIsBounded(t *testing.T) {
	if catalogProbeTimeout <= 0 {
		t.Fatalf("catalogProbeTimeout = %s; a non-positive timeout makes the context expire "+
			"immediately and disables the probe outright rather than bounding it",
			catalogProbeTimeout)
	}

	// 🔴 THE FLOOR IS THE HALF THAT CHANGED, and it is now a MEASUREMENT rather
	// than a guess. `opencode models` took 8.2s cold on the machine this was
	// written on (2026-09-15; ~3.4s warm, three runs). A ceiling below that does
	// not bound a hang — it fires on a healthy harness, and an expired probe is
	// not a slow success: every candidate is reported unavailable, so generation
	// omits the model field and writes files that inherit the caller's model while
	// warning loudly about a machine where nothing is wrong. The previous value,
	// 5s, was below the measured healthy latency of a harness that had not yet
	// been put on this path.
	if catalogProbeTimeout < 15*time.Second {
		t.Errorf("catalogProbeTimeout = %s. `opencode models` was MEASURED at 8.2s cold on a "+
			"healthy machine, so anything near that turns aihub#642 AC6's live catalog check "+
			"off without saying so.", catalogProbeTimeout)
	}

	// The ceiling is no longer about the boot. aihub#683 moved this work off the
	// pre-serve path into a goroutine (cmd/polyforge/main.go), so the cost of a
	// large value is one background goroutine held by a wedged CLI, not "MCP will
	// not connect". It still needs an end: a probe that never gives up leaks a
	// goroutine and a child process per boot, and every session on the machine
	// starts a boot.
	if catalogProbeTimeout > 2*time.Minute {
		t.Errorf("catalogProbeTimeout = %s. Even off the critical path this is held by a "+
			"goroutine and a child process, once per session on the machine.", catalogProbeTimeout)
	}

	// The zero-value path is what production uses: nothing outside tests sets the
	// field, so a probe built with the zero value must get the default.
	if got := (&codexCatalogProbe{}).effectiveTimeout(); got != catalogProbeTimeout {
		t.Errorf("a zero-value probe resolves to %s, not the default %s — production constructs "+
			"it that way and would run unbounded again", got, catalogProbeTimeout)
	}

	// 🔴 AND THAT THE OTHER TWO EMBED IT AT ALL. This is the assertion that would
	// have caught the pre-aihub#683 state, where codex was bounded and the other
	// two were not: a `boundedProbe` field that is never consulted resolves to the
	// same default, so the check is that each probe TYPE carries one.
	if got := (&piCatalogProbe{}).effectiveTimeout(); got != catalogProbeTimeout {
		t.Errorf("pi's zero-value probe resolves to %s, not %s", got, catalogProbeTimeout)
	}
	if got := (&opencodeCatalogProbe{}).effectiveTimeout(); got != catalogProbeTimeout {
		t.Errorf("opencode's zero-value probe resolves to %s, not %s", got, catalogProbeTimeout)
	}
}
