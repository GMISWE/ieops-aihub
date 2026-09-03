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

import (
	"bytes"
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
// whole of stdout, because resolveUICookieSecret legitimately prints a line of
// its own when a key is configured.
const (
	verdictRefused  = "REFUSED "
	verdictToken    = "TOKEN "
	verdictAccepted = "ACCEPTED "
	verdictRejected = "REJECTED "
)

func TestMain(m *testing.M) {
	if os.Getenv(helperEnvActive) == "1" {
		os.Exit(runSessionHelper())
	}
	os.Exit(m.Run())
}

// runSessionHelper is the child process. It goes through the same startup
// decision main() does — loadUICookieSecret, then server.NewSessionManager —
// and reports one line.
func runSessionHelper() int {
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
}

// runServerProcess re-execs this test binary as a server process.
//
// value == nil means the variable is absent, which is not the same thing as
// present-and-empty and is the state production was actually in.
func runServerProcess(t *testing.T, value *string, op, token string) processResult {
	t.Helper()

	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}

	env := make([]string, 0, len(os.Environ())+4)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, uiCookieSecretEnv+"=") || strings.HasPrefix(kv, helperEnvPrefix) {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, helperEnvActive+"=1", helperEnvOp+"="+op, helperEnvToken+"="+token)
	if value != nil {
		env = append(env, uiCookieSecretEnv+"="+*value)
	}

	cmd := exec.Command(self)
	cmd.Env = env
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	runErr := cmd.Run()

	res := processResult{stdout: out.String(), stderr: errOut.String()}
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &exitErr):
		res.exitCode = exitErr.ExitCode()
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

		// Whitespace is trimmed, so this must resolve to the same key as the
		// "hex 32 bytes" case does — two edits of an env-file differing only in
		// trailing space must not be two different keys.
		{name: "hex with surrounding whitespace", set: true, value: "  " + strings.Repeat("a1b2c3d4", 8) + "\t", want: sessionsSurviveRestart},

		{name: "explicit ephemeral opt-in", set: true, value: uiCookieSecretEphemeral, want: sessionsMayBeEphemeral},

		// Must be read as the opt-in, not as a nine-byte literal key. A
		// case-sensitive match would make this configuration restart-STABLE,
		// so the "survives" clause would pass and nothing would report that
		// the deployment is signing cookies with the word EPHEMERAL.
		{name: "ephemeral opt-in, wrong case", set: true, value: "EPHEMERAL", want: sessionsMayBeEphemeral},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value *string
			cfg := uiCookieSecretEnv + " unset"
			if tc.set {
				v := tc.value
				value = &v
				cfg = fmt.Sprintf("%s=%q", uiCookieSecretEnv, tc.value)
			}

			a := runServerProcess(t, value, "sign", "")

			// Sanctioned way out #1: refuse to start. Allowed for any
			// configuration, but it has to say which variable is missing —
			// a fatal that does not name it moves the cost onto whoever is
			// staring at a container that will not come up.
			if a.refused {
				if !strings.Contains(a.stdout+a.stderr, uiCookieSecretEnv) {
					t.Fatalf("process refused to start on %s without naming %s anywhere in its output:\nstdout=%q\nstderr=%q",
						cfg, uiCookieSecretEnv, a.stdout, a.stderr)
				}
				if tc.want == sessionsMayBeEphemeral {
					t.Fatalf("%s must start — it is the documented way to ask for per-process keys — but the process refused: %s",
						cfg, a.detail)
				}
				return
			}

			if a.token == "" {
				t.Fatalf("process A on %s neither refused to start nor minted a session (exit=%d)\nstdout=%q\nstderr=%q",
					cfg, a.exitCode, a.stdout, a.stderr)
			}

			b := runServerProcess(t, value, "verify", a.token)
			if b.refused {
				t.Fatalf("process B refused to start on %s, the same configuration process A started on: %s", cfg, b.detail)
			}

			switch tc.want {
			case sessionsSurviveRestart:
				if !b.accepted {
					t.Fatalf("RESTART SURVIVAL BROKEN on %s: a /ui session minted by process A was "+
						"REJECTED by process B on the identical configuration (process B said %q). "+
						"Both processes started, so nothing stops a deploy from silently signing out "+
						"every /ui user — that is aihub#344.", cfg, b.detail)
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
						"deployment", cfg, b.detail)
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
