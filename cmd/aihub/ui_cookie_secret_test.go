package main

// The gate for aihub#344.
//
// What is being pinned is NOT "the server reads POLYFORGE_UI_COOKIE_SECRET".
// A server that reads the variable and then ignores it passes that test, and so
// does the defect this file exists to close: before the fix the variable was
// read, found empty, and quietly replaced with 32 fresh random bytes, which is
// a server that reads it and ignores its absence.
//
// The criterion is restart survival, and it is exercised across two REAL
// processes through the real signing and verifying code:
//
//	process A: resolve the key from the environment, mint a /ui session cookie
//	process B: resolve the key from the same environment, verify A's cookie
//
// B must accept it. A restart is exactly this — same configuration, new
// process — so a configuration under which B rejects A's cookie is a
// configuration in which every deploy signs out every /ui user.
//
// There are exactly two sanctioned ways out, and the test names both:
//
//  1. the process REFUSES TO START (and says which variable is missing), or
//  2. the operator explicitly asked for per-process keys with
//     POLYFORGE_UI_COOKIE_SECRET=ephemeral, in which case B must REJECT the
//     cookie — an opt-in that silently kept working across restarts would not
//     mean what it says.
//
// Anything else — including "start anyway with a random key" (the old
// behaviour) and "start anyway with a key compiled into the binary" (the
// tempting way to make this file green without fixing anything) — fails.
//
// Four tests, because a review of the first draft proved that the round-trip
// alone leaves two live holes. Deleting strings.TrimSpace, and degrading
// main()'s os.Exit(1) to log-and-continue, BOTH kept the first draft green:
//
//	TestUISessionSurvivesProcessRestart          the criterion, per configuration
//	TestUICookieSecretTrimmedValueIsTheSameKey   A and B differ only in whitespace
//	TestMainRefusesToStartBeforeTouchingTheDB    main() really exits, and early
//	TestUICookieSecretGuidanceIsActionable       the remedy is pasteable

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/server"
)

// Environment names used to drive a re-exec of this test binary as a stand-in
// for a server process. All share one prefix so the parent can strip them out
// of the child's environment and build it deterministically.
const (
	helperEnvPrefix = "PF344_"
	helperEnvActive = helperEnvPrefix + "HELPER"
	helperEnvOp     = helperEnvPrefix + "OP"
	helperEnvToken  = helperEnvPrefix + "TOKEN"
)

// The identity carried by the test cookie. Verified on the far side so an
// "accepted" verdict means the payload round-tripped, not merely that some
// HMAC matched.
const (
	helperUserID = "u_aihub344"
	helperKeyID  = "k_aihub344"
)

// Single-line verdicts the child writes to stdout. Prefixes rather than the
// whole of stdout, because the code under test legitimately prints a line of
// its own when a key is configured.
const (
	verdictRefused  = "REFUSED "
	verdictToken    = "TOKEN "
	verdictAccepted = "ACCEPTED "
	verdictRejected = "REJECTED "
)

// childBudget bounds a child process. Every path this file drives either exits
// or fails to start within milliseconds; the budget exists so that a future
// main() which BLOCKS instead of exiting fails the test rather than hanging CI.
const childBudget = 30 * time.Second

// unparseableDSN fails inside pgxpool's DSN parser — no socket, no DNS, no
// wait. It is the marker for "execution reached db.New": the port is out of
// range, so db.New returns `cannot parse ...` immediately and offline. pgxpool
// connects lazily, so an unREACHABLE DSN would NOT do — main() would sail past
// db.New and block in Start(), and the test would time out instead of
// reporting which line was crossed.
const unparseableDSN = "postgres://user:pw@localhost:99999/db"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnvActive) == "1" {
		os.Exit(runSessionHelper())
	}
	os.Exit(m.Run())
}

// runSessionHelper is the child process.
func runSessionHelper() int {
	// "boot" runs the real main(). Every configuration this file boots it with
	// is one main() is expected to reject, so reaching the return below at all
	// means main() declined to exit — which the caller reports as the defect.
	if os.Getenv(helperEnvOp) == "boot" {
		main()
		fmt.Println("BOOT-RETURNED")
		return 0
	}

	secret, err := loadUICookieSecret()
	if err != nil {
		fmt.Printf("%s%v | %s\n", verdictRefused, err, strings.ReplaceAll(uiCookieSecretGuidance, "\n", " "))
		return 1
	}
	sm := server.NewSessionManager(secret)

	switch os.Getenv(helperEnvOp) {
	case "sign":
		fmt.Printf("%s%s\n", verdictToken, sm.Sign(helperUserID, helperKeyID, time.Hour))
	case "verify":
		userID, keyID, verr := sm.Verify(os.Getenv(helperEnvToken))
		if verr != nil {
			fmt.Printf("%s%v\n", verdictRejected, verr)
			return 0
		}
		fmt.Printf("%s%s %s\n", verdictAccepted, userID, keyID)
	default:
		fmt.Fprintf(os.Stderr, "helper: unknown op %q\n", os.Getenv(helperEnvOp))
		return 2
	}
	return 0
}

// childCfg is the environment a child server process is started with.
//
// value == nil means the variable is ABSENT, which is not the same thing as
// present-and-empty and is the state production was actually in.
type childCfg struct {
	value *string
	extra []string
}

func withValue(v string) childCfg { return childCfg{value: &v} }
func withoutValue() childCfg      { return childCfg{} }
func (c childCfg) describe() string {
	if c.value == nil {
		return uiCookieSecretEnv + " unset"
	}
	return fmt.Sprintf("%s=%q", uiCookieSecretEnv, *c.value)
}

// processResult is one child run, parsed.
type processResult struct {
	refused  bool
	token    string
	accepted bool
	userID   string
	keyID    string
	detail   string
	stdout   string
	stderr   string
	exitCode int
	timedOut bool
}

// output is everything the process said, in one string.
func (r processResult) output() string { return r.stdout + r.stderr }

// greppedLine is what the operator's `docker logs … | grep -F <tag>` prints:
// the first line carrying the tag, or "" if the grep finds nothing.
func (r processResult) greppedLine(tag string) string {
	for _, line := range strings.Split(r.output(), "\n") {
		if strings.Contains(line, tag) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// runServerProcess re-execs this test binary as a server process.
func runServerProcess(t *testing.T, cfg childCfg, op, token string) processResult {
	t.Helper()

	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}

	env := make([]string, 0, len(os.Environ())+8)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, uiCookieSecretEnv+"=") || strings.HasPrefix(kv, helperEnvPrefix) {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, helperEnvActive+"=1", helperEnvOp+"="+op, helperEnvToken+"="+token)
	env = append(env, cfg.extra...)
	if cfg.value != nil {
		env = append(env, uiCookieSecretEnv+"="+*cfg.value)
	}

	ctx, cancel := context.WithTimeout(context.Background(), childBudget)
	defer cancel()

	cmd := exec.CommandContext(ctx, self)
	cmd.Env = env
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	runErr := cmd.Run()

	res := processResult{stdout: out.String(), stderr: errOut.String()}
	res.timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &exitErr):
		res.exitCode = exitErr.ExitCode()
	case res.timedOut:
		t.Fatalf("child server process did not exit within %s on %s — it must fail fast, not block\nstdout=%q\nstderr=%q",
			childBudget, cfg.describe(), res.stdout, res.stderr)
	default:
		t.Fatalf("re-exec of %s failed: %v (stderr: %s)", self, runErr, res.stderr)
	}

	for _, line := range strings.Split(res.stdout, "\n") {
		switch {
		case strings.HasPrefix(line, verdictRefused):
			res.refused = true
			res.detail = strings.TrimPrefix(line, verdictRefused)
		case strings.HasPrefix(line, verdictToken):
			res.token = strings.TrimPrefix(line, verdictToken)
		case strings.HasPrefix(line, verdictRejected):
			res.detail = strings.TrimPrefix(line, verdictRejected)
		case strings.HasPrefix(line, verdictAccepted):
			res.accepted = true
			res.detail = line
			if f := strings.Fields(strings.TrimPrefix(line, verdictAccepted)); len(f) == 2 {
				res.userID, res.keyID = f[0], f[1]
			}
		}
	}
	return res
}

// sessionOutcome is what THIS TEST requires of a configuration. It is declared
// in the table, never read back out of the code under test — a test that asks
// the implementation what it intends cannot catch the implementation changing
// its mind.
type sessionOutcome int

const (
	// sessionsSurviveRestart: a cookie minted by process A must verify in B.
	sessionsSurviveRestart sessionOutcome = iota
	// sessionsMayBeEphemeral: the operator asked for per-process keys, so B
	// must reject A's cookie.
	sessionsMayBeEphemeral
)

func TestUISessionSurvivesProcessRestart(t *testing.T) {
	for _, tc := range []struct {
		name  string
		set   bool
		value string
		want  sessionOutcome
	}{
		// The defect. Unset is the state /root/aihub.env was in, and the
		// requirement on it is the same as on every other configuration: if
		// the process starts, its sessions must outlive it.
		{name: "unset", set: false, want: sessionsSurviveRestart},

		{name: "hex 32 bytes", set: true, value: strings.Repeat("a1b2c3d4", 8), want: sessionsSurviveRestart},
		{name: "raw passphrase", set: true, value: "not-hex-but-long-enough-passphrase-for-a-test", want: sessionsSurviveRestart},

		{name: "explicit ephemeral opt-in", set: true, value: uiCookieSecretEphemeral, want: sessionsMayBeEphemeral},

		// Must be read as the opt-in, not as a nine-byte literal key. A
		// case-sensitive match would make this configuration restart-STABLE,
		// so the "survives" clause would pass and nothing would report that
		// the deployment is signing cookies with the word EPHEMERAL.
		{name: "ephemeral opt-in, wrong case", set: true, value: "EPHEMERAL", want: sessionsMayBeEphemeral},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := withoutValue()
			if tc.set {
				cfg = withValue(tc.value)
			}

			a := runServerProcess(t, cfg, "sign", "")

			// Sanctioned way out #1: refuse to start. Allowed for any
			// configuration, but it has to say which variable is missing —
			// a fatal that does not name it moves the cost onto whoever is
			// staring at a container that will not come up.
			if a.refused {
				if !strings.Contains(a.output(), uiCookieSecretEnv) {
					t.Fatalf("process refused to start on %s without naming %s anywhere in its output:\nstdout=%q\nstderr=%q",
						cfg.describe(), uiCookieSecretEnv, a.stdout, a.stderr)
				}
				if tc.want == sessionsMayBeEphemeral {
					t.Fatalf("%s must start — it is the documented way to ask for per-process keys — but the process refused: %s",
						cfg.describe(), a.detail)
				}
				return
			}

			if a.token == "" {
				t.Fatalf("process A on %s neither refused to start nor minted a session (exit=%d)\nstdout=%q\nstderr=%q",
					cfg.describe(), a.exitCode, a.stdout, a.stderr)
			}

			// The process started, so an operator has to be able to find out
			// WHICH state it started in. docs/deployment.md step 7 is a single
			// `docker logs … | grep -F '/ui session key'`, and that grep is
			// only a check if the tag is in both lines: present in just the
			// configured one, empty output would mean "ephemeral" AND "wrong
			// container" AND "rotated log" at once — the ambiguity the check
			// was added to remove. Verified here, not asserted in prose.
			line := a.greppedLine(uiCookieSecretLogTag)
			if line == "" {
				t.Errorf("process started on %s but printed no line containing %q, so the step-7 grep "+
					"in docs/deployment.md returns nothing and cannot tell this state apart from a "+
					"rotated log or the wrong container\nstdout=%q\nstderr=%q",
					cfg.describe(), uiCookieSecretLogTag, a.stdout, a.stderr)
			}
			// ...and the one line must SAY which state it is. The doc tells the
			// operator that a `warn:` line means ephemeral; that has to be true
			// in both directions or the instruction misleads.
			if isWarn := strings.HasPrefix(line, "warn:"); isWarn != (tc.want == sessionsMayBeEphemeral) {
				t.Errorf("on %s the %q line is %q; a leading \"warn:\" must mean ephemeral and nothing "+
					"else, because that is how docs/deployment.md tells the operator to read it",
					cfg.describe(), uiCookieSecretLogTag, line)
			}

			b := runServerProcess(t, cfg, "verify", a.token)
			if b.refused {
				t.Fatalf("process B refused to start on %s, the same configuration process A started on: %s",
					cfg.describe(), b.detail)
			}

			switch tc.want {
			case sessionsSurviveRestart:
				if !b.accepted {
					t.Fatalf("RESTART SURVIVAL BROKEN on %s: a /ui session minted by process A was "+
						"REJECTED by process B on the identical configuration (process B said %q). "+
						"Both processes started, so nothing stops a deploy from silently signing out "+
						"every /ui user — that is aihub#344.", cfg.describe(), b.detail)
				}
				if b.userID != helperUserID || b.keyID != helperKeyID {
					t.Errorf("process B accepted the cookie but decoded it as (%q, %q), want (%q, %q) — "+
						"the signature matched while the payload did not round-trip",
						b.userID, b.keyID, helperUserID, helperKeyID)
				}

			case sessionsMayBeEphemeral:
				if b.accepted {
					t.Fatalf("%s is documented as a per-process key, but process B accepted a cookie "+
						"minted by process A (%s) — the opt-in does not mean what it says, so an "+
						"operator reading the env-file would draw the wrong conclusion about this "+
						"deployment", cfg.describe(), b.detail)
				}
			}

			// Sanctioned way out #1 was not taken, so an unconfigured process
			// started. The clause above already required its sessions to
			// survive; this one closes the only remaining way to satisfy that
			// without configuration, which is to compile a key into the binary
			// — a signing key shared by every copy of the image, i.e. forgeable
			// /ui sessions for any user by anyone who can read the binary.
			if !tc.set {
				t.Errorf("with %s unset the process started anyway. It must refuse: a key that is "+
					"stable across processes with no configuration at all can only have been "+
					"compiled into the binary, which makes every /ui session forgeable.", uiCookieSecretEnv)
			}
		})
	}
}

// TestUICookieSecretTrimmedValueIsTheSameKey pins the trim across a RESTART,
// which is the only place it matters and the only shape that catches its loss.
//
// The round-trip in the test above cannot: it hands both processes the same
// string, and an untrimmed value is just as stable across two processes as a
// trimmed one. Deleting strings.TrimSpace left that test entirely green while
// docs/deployment.md promised "`…=abc ` and `…=abc` are the same key". So the
// two processes here are given values that differ ONLY in surrounding
// whitespace — an operator re-typing the line into /root/aihub.env, which is
// exactly the silent sign-out this work item exists to end.
func TestUICookieSecretTrimmedValueIsTheSameKey(t *testing.T) {
	const hex32 = "a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4"

	for _, tc := range []struct{ name, valueA, valueB string }{
		{"trailing space added", hex32, hex32 + " "},
		{"leading space removed", " " + hex32, hex32},
		{"tab and newline", "\t" + hex32 + "\n", hex32},
		{"raw passphrase, padded", "  a-raw-passphrase-with-padding  ", "a-raw-passphrase-with-padding"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := runServerProcess(t, withValue(tc.valueA), "sign", "")
			if a.refused || a.token == "" {
				t.Fatalf("process A refused or minted nothing for %q: exit=%d stdout=%q stderr=%q",
					tc.valueA, a.exitCode, a.stdout, a.stderr)
			}

			b := runServerProcess(t, withValue(tc.valueB), "verify", a.token)
			if b.refused {
				t.Fatalf("process B refused to start for %q: %s", tc.valueB, b.detail)
			}
			if !b.accepted {
				t.Fatalf("a session minted with %s=%q was REJECTED by a process started with %q "+
					"(%s). The two differ only in surrounding whitespace, so an operator who "+
					"re-typed the env-file line has just signed out every /ui user — and both "+
					"processes reported a configured, restart-surviving key.",
					uiCookieSecretEnv, tc.valueA, tc.valueB, b.detail)
			}
		})
	}
}

// TestMainRefusesToStartBeforeTouchingTheDB exercises main() itself.
//
// Everything above stops at loadUICookieSecret and at the test helper's own
// decision to return 1. main() was never in the loop, so degrading it from
// os.Exit(1) to "log the error and carry on with a random key" — the whole
// defect, restored one line higher up — kept the suite green. So did moving the
// resolution back below db.New, which the comment at main.go's call site claims
// not to do.
//
// One assertion covers both. Booted with the secret absent and a DSN that
// cannot be parsed, main() must exit non-zero having said the cookie key is
// missing and WITHOUT having reached db.New. `db.New:` in the output is proof
// that execution ran past the check; a zero exit is proof it did not stop.
func TestMainRefusesToStartBeforeTouchingTheDB(t *testing.T) {
	cfg := withoutValue()
	cfg.extra = []string{"DATABASE_URL=" + unparseableDSN}

	res := runServerProcess(t, cfg, "boot", "")

	if res.exitCode == 0 {
		t.Fatalf("main() exited 0 with %s unset — it must refuse to start. A server that only "+
			"LOGS this and carries on is the aihub#344 defect with an extra line of output.\n"+
			"stdout=%q\nstderr=%q", uiCookieSecretEnv, res.stdout, res.stderr)
	}
	if strings.Contains(res.output(), "BOOT-RETURNED") {
		t.Errorf("main() returned instead of exiting; the refusal path must terminate the process")
	}
	if !strings.Contains(res.output(), uiCookieSecretEnv) {
		t.Errorf("main() exited %d without naming %s; the operator is left with a container that "+
			"will not start and no reason\nstdout=%q\nstderr=%q",
			res.exitCode, uiCookieSecretEnv, res.stdout, res.stderr)
	}
	if strings.Contains(res.output(), "db.New:") {
		t.Errorf("main() reached db.New before rejecting the missing %s. The check must come first, "+
			"so a misconfigured deployment fails in milliseconds without opening a connection to "+
			"the production database\nstdout=%q\nstderr=%q",
			uiCookieSecretEnv, res.stdout, res.stderr)
	}
}

// TestUICookieSecretGuidanceIsActionable pins the remedy, not the diagnosis.
//
// This variable was documented in README.md and docs/deployment.md, both of
// which described the consequence correctly, and it still went unset in
// production for months. So the missing piece was never "someone should have
// known" — it was a line to paste at the moment the server tells you. A fatal
// that degrades to "POLYFORGE_UI_COOKIE_SECRET not set" reinstates exactly the
// signal that was already measured to fail.
func TestUICookieSecretGuidanceIsActionable(t *testing.T) {
	if _, err := resolveUICookieSecret(""); !errors.Is(err, errUICookieSecretUnset) {
		t.Fatalf("resolveUICookieSecret(\"\") = %v, want %v", err, errUICookieSecretUnset)
	}

	for _, want := range []string{
		// a command that produces a correct value, not a description of one
		"openssl rand -hex 32",
		// where it has to be written so it survives a container swap; the
		// container's own filesystem does not
		"/root/aihub.env",
		// the way to decline, so declining does not require guessing
		uiCookieSecretEphemeral,
	} {
		if !strings.Contains(uiCookieSecretGuidance, want) {
			t.Errorf("startup guidance does not mention %q; an operator reading it cannot act on it "+
				"without going to find the docs that were already there and already ignored", want)
		}
	}
}
