package drain

import (
	"errors"
	"strings"
	"testing"
)

func argsOf(t *testing.T, ch Channel) []string {
	t.Helper()
	inv, err := BuildInvocation(ch, "PROMPT")
	if err != nil {
		t.Fatalf("BuildInvocation(%s): %v", ch, err)
	}
	return inv.Args
}

func hasPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func has(args []string, v string) bool {
	for _, a := range args {
		if a == v {
			return true
		}
	}
	return false
}

// TestBuildInvocation_ClaudeUsesTheOnlyPermissionModeThatWorksAsRoot pins the single most
// consequential measurement behind this package (2026-09-14, this machine, Claude Code 2.1.258,
// clean CLAUDE_CONFIG_DIR so the box's own allowlist could not mask the behaviour):
//
//	default (no flag)  exit 0,  6s, file NOT created, stdout "the write … needs your permission"
//	bypassPermissions  exit 1,  1s, "--dangerously-skip-permissions cannot be used with
//	                                 root/sudo privileges for security reasons"
//	dontAsk            exit 0, 13s, Bash DENIED, file not created
//	acceptEdits        exit 0,  6s, file created; and with a Bash tool call, 9s, command ran
//
// So the mode the design would reach for first (bypassPermissions) is the one mode that cannot
// work here — drain's own deployment target is a container running as root — and the two modes
// that exit 0 while doing nothing are the ones that would make drain wrap work items that did
// nothing at all.
//
// Mutants watched go red: swapping acceptEdits for bypassPermissions, for dontAsk, or dropping
// the flag entirely.
func TestBuildInvocation_ClaudeUsesTheOnlyPermissionModeThatWorksAsRoot(t *testing.T) {
	args := argsOf(t, Channel{Harness: HarnessClaude})

	if !has(args, "-p") {
		t.Errorf("claude args %v lack -p; without it the CLI starts an interactive session "+
			"and an unattended run blocks forever", args)
	}
	if !hasPair(args, "--permission-mode", "acceptEdits") {
		t.Fatalf("claude args %v must carry `--permission-mode acceptEdits`: measured, it is the "+
			"only mode that both runs tools and works under root", args)
	}
	for _, forbidden := range []string{"bypassPermissions", "dontAsk", "plan"} {
		if has(args, forbidden) {
			t.Errorf("claude args %v use permission mode %q: bypassPermissions exits 1 as root, "+
				"dontAsk silently denies Bash with exit 0, plan executes nothing", args, forbidden)
		}
	}
	if has(args, "--dangerously-skip-permissions") {
		t.Error("claude args carry --dangerously-skip-permissions, which is refused under root")
	}
}

// TestBuildInvocation_NoModelUnlessAskedFor protects aihub#555's measured hazard at the A-mode
// boundary: an explicit per-invocation model silently OVERRIDES the agent file's `model:`
// frontmatter, which is where the role's tier lives. A channel with no model must therefore emit
// no model flag at all, rather than a default that would quietly outrank the role catalog.
func TestBuildInvocation_NoModelUnlessAskedFor(t *testing.T) {
	for _, h := range KnownHarnesses {
		args := argsOf(t, Channel{Harness: h})
		for _, flag := range []string{"--model", "-m"} {
			if has(args, flag) {
				t.Errorf("%s emitted %s with no model configured: an explicit model silently "+
					"overrides the agent file's frontmatter (aihub#555)", h, flag)
			}
		}
	}
	// When one IS configured, it must actually reach the command line.
	if !hasPair(argsOf(t, Channel{Harness: HarnessClaude, Model: "opus"}), "--model", "opus") {
		t.Error("a configured claude model did not reach the command line")
	}
	if !hasPair(argsOf(t, Channel{Harness: HarnessCodex, Model: "gpt-5.6"}), "-m", "gpt-5.6") {
		t.Error("a configured codex model did not reach the command line")
	}
}

// TestBuildInvocation_CodexAndOpenCodeCarryTheirMeasuredFlags pins the other two harnesses.
//
// The --skip-git-repo-check half is measured and is NOT in aihub#640's survey: `codex exec`
// refuses outright with "Not inside a trusted directory and --skip-git-repo-check was not
// specified" — exit 1 in under a second, before any model call. Drain always runs a step inside
// the work item's worktree, which IS a git repo, so this holds by circumstance; the flag is
// passed so it holds by construction instead.
func TestBuildInvocation_CodexAndOpenCodeCarryTheirMeasuredFlags(t *testing.T) {
	codex := argsOf(t, Channel{Harness: HarnessCodex})
	if len(codex) == 0 || codex[0] != "exec" {
		t.Fatalf("codex args %v must start with the non-interactive `exec` subcommand", codex)
	}
	if !hasPair(codex, "-s", "workspace-write") {
		t.Errorf("codex args %v lack `-s workspace-write`: without a sandbox policy it asks for "+
			"approval, and read-only cannot complete a write step", codex)
	}
	if !has(codex, "--skip-git-repo-check") {
		t.Errorf("codex args %v lack --skip-git-repo-check: measured, `codex exec` exits 1 "+
			"outside a git repo before doing anything", codex)
	}

	oc := argsOf(t, Channel{Harness: HarnessOpenCode})
	if len(oc) == 0 || oc[0] != "run" {
		t.Fatalf("opencode args %v must start with `run`", oc)
	}
	if !has(oc, "--auto") {
		t.Errorf("opencode args %v lack --auto; every permission prompt would wait for a human "+
			"who is not there", oc)
	}

	pi := argsOf(t, Channel{Harness: HarnessPi})
	if !has(pi, "-p") {
		t.Errorf("pi args %v lack -p (non-interactive print mode)", pi)
	}
}

// TestBuildInvocation_ClosesStdinForEveryHarness pins the survey's pi finding and generalises it.
// `pi -p` with an open stdin blocks forever producing ZERO stdout and ZERO stderr — there is no
// error to match on and nothing to time out except a wall clock. Closing stdin turns any
// harness's "waiting for input" into an immediate EOF, so it is set unconditionally.
func TestBuildInvocation_ClosesStdinForEveryHarness(t *testing.T) {
	for _, h := range KnownHarnesses {
		inv, err := BuildInvocation(Channel{Harness: h}, "PROMPT")
		if err != nil {
			t.Fatalf("BuildInvocation(%s): %v", h, err)
		}
		if !inv.CloseStdin {
			t.Errorf("%s does not close stdin: a harness that waits on it hangs with no output "+
				"at all, which is the one failure mode nothing else in this package can detect", h)
		}
		if inv.Path == "" {
			t.Errorf("%s produced an empty executable path", h)
		}
		if len(inv.Args) == 0 || inv.Args[len(inv.Args)-1] != "PROMPT" {
			t.Errorf("%s did not pass the prompt as the final argument: args=%v", h, inv.Args)
		}
	}
}

// TestBuildInvocation_RejectsAnUnknownHarness is the negative control: an unknown harness must
// error rather than fall through to a default. Defaulting would send a step to the wrong vendor
// on a typo, and the step would look like it ran.
func TestBuildInvocation_RejectsAnUnknownHarness(t *testing.T) {
	if _, err := BuildInvocation(Channel{Harness: "cursor"}, "x"); err == nil {
		t.Fatal("an unknown harness built an invocation instead of erroring")
	}
}

// TestClassifyPreflight_ExitZeroWithoutTheTokenIsAFailure is the branch that makes preflight
// worth having. The measured `claude -p` default-permission shape is exit 0 with a prose refusal
// and empty stderr, so "the process succeeded" and "the work happened" are genuinely different
// questions, and only the second one matters.
//
// Mutant watched: deleting the token check makes the middle two cases pass preflight, after
// which drain would claim work items and hand every step to a channel that does nothing.
func TestClassifyPreflight_ExitZeroWithoutTheTokenIsAFailure(t *testing.T) {
	ch := Channel{Harness: HarnessClaude}

	if v := ClassifyPreflight(ch, nil, "POLYFORGE_OK\n"); !v.OK {
		t.Fatalf("a healthy probe was rejected: %s", v.Reason)
	}
	if v := ClassifyPreflight(ch, nil, "Sure! POLYFORGE_OK is the word.\n"); !v.OK {
		t.Fatalf("a token embedded in a sentence was rejected: %s", v.Reason)
	}

	for _, tc := range []struct{ name, out string }{
		{"silent refusal", "The write needs your permission — it was denied/not granted.\n"},
		{"empty reply", ""},
		{"wrong answer", "I'm not sure what you mean.\n"},
	} {
		if v := ClassifyPreflight(ch, nil, tc.out); v.OK {
			t.Errorf("%s passed preflight on exit 0 alone; the channel would be used for real work", tc.name)
		}
	}

	v := ClassifyPreflight(ch, errors.New("exit status 1"), "boom: something broke\n")
	if v.OK {
		t.Error("a non-zero exit passed preflight")
	}
	if !strings.Contains(v.Reason, "boom") {
		t.Errorf("the reason %q dropped the harness's own words; `exit status 1` is not actionable", v.Reason)
	}
}

// TestIsAuthFailure_MatchesTheThreeMeasuredRefusals pins the run-time credential fallback against
// the exact strings the three unauthenticated harnesses on this machine actually produced on
// 2026-09-14. They are matched on TEXT, not exit code, because all three exit 1 for both a
// credential problem and a genuinely failed step — only the words tell them apart, and
// misreading one as the other would blame the work item for the machine's expired key.
func TestIsAuthFailure_MatchesTheThreeMeasuredRefusals(t *testing.T) {
	measured := map[string]string{
		"codex":    `stream error: unexpected status 401 Unauthorized: Missing bearer or basic authentication in header, url: https://api.openai.com/v1/responses`,
		"opencode": `Error: Unauthorized: Invalid API key: HTTP 401`,
		"pi":       `401 {"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`,
	}
	for harness, out := range measured {
		if !IsAuthFailure(out) {
			t.Errorf("%s's measured 401 output was not recognised as an auth failure; the run "+
				"would blame the work item instead of falling to the next channel:\n  %s", harness, out)
		}
	}

	// Negative control. A step that legitimately failed must NOT be mistaken for a credential
	// problem: doing so would burn a healthy channel candidate on every ordinary test failure
	// and, once the candidates ran out, report FAILED for the wrong reason.
	for _, ordinary := range []string{
		"FAIL github.com/x/y  0.4s\n--- FAIL: TestThing\n",
		"exit status 1: the build failed",
		"go vet: composite literal uses unkeyed fields",
		"REVIEW_RESULT: FAIL",
	} {
		if IsAuthFailure(ordinary) {
			t.Errorf("an ordinary step failure was read as an auth failure: %q", ordinary)
		}
	}
}

// TestClassifyPreflight_ReasonSurvivesTheHarnessesOwnColourCodes is a regression test for a
// defect that only appeared when the real binary was run, and that every unit test up to that
// point had passed straight through.
//
// Measured 2026-09-14, `polyforge drain` against the real project stored these preflight
// rejection reasons in its snapshot, and `polyforge watch` printed them:
//
//	codex:    "\x1b[1m\x1b[31mERROR:\x1b[0m\x1b[0m unexpected status 401 Unauthorized: …"
//	opencode: "\x1b[0m"
//
// The opencode one is the real failure. Its last non-blank line was a lone colour reset, so the
// answer to "why can I not use this channel?" was an invisible control sequence. It is non-empty,
// so an assertion that merely required SOME reason was satisfied by it — which is exactly the
// shape of gate this project keeps having to fix: one that certifies its own blind spot.
//
// Mutant watched: removing StripANSI from lastMeaningfulLine's blank test makes the opencode case
// return the bare reset again.
func TestClassifyPreflight_ReasonSurvivesTheHarnessesOwnColourCodes(t *testing.T) {
	// Verbatim shape of what opencode actually produced: a real error line, then a line that
	// holds nothing but a reset.
	opencodeOut := "some plugin banner\n\x1b[0m > orchestrator · openai/gpt-5.5 \x1b[0m\n" +
		"\x1b[91m\x1b[1mError: \x1b[0mUnauthorized: Invalid API key: HTTP 401\n\x1b[0m\n"

	v := ClassifyPreflight(Channel{Harness: HarnessOpenCode}, errors.New("exit status 1"), opencodeOut)
	if v.OK {
		t.Fatal("a 401 passed preflight")
	}
	if strings.Contains(v.Reason, "\x1b") {
		t.Errorf("the reason still carries escape sequences: %q", v.Reason)
	}
	if !strings.Contains(v.Reason, "Unauthorized") {
		t.Fatalf("reason = %q, want the real error; a lone colour reset is non-empty and "+
			"tells an operator nothing", v.Reason)
	}

	codexOut := "\x1b[1m\x1b[31mERROR:\x1b[0m\x1b[0m unexpected status 401 Unauthorized: Missing bearer\n"
	v = ClassifyPreflight(Channel{Harness: HarnessCodex}, errors.New("exit status 1"), codexOut)
	if strings.Contains(v.Reason, "\x1b") {
		t.Errorf("codex reason still carries escape sequences: %q", v.Reason)
	}
	if !strings.Contains(v.Reason, "401") {
		t.Errorf("codex reason = %q, want the 401", v.Reason)
	}

	// The banner-after-error ordering, which is what a real run actually produced. opencode
	// writes its banner to stdout and its error to stderr; once CombinedOutput interleaves the
	// two, the banner can land AFTER the error, and a plain "last line" rule then reports
	// "> orchestrator · openai/gpt-5.5" as the reason the channel is unavailable. That is the
	// exact string a live `polyforge drain` stored on 2026-09-14.
	//
	// Mutant watched: making explanatoryLine return lastMeaningfulLine unconditionally turns
	// this red.
	bannerLast := "\x1b[91m\x1b[1mError: \x1b[0mUnauthorized: Invalid API key: HTTP 401\n" +
		"\x1b[0m > orchestrator · openai/gpt-5.5 \x1b[0m\n"
	v = ClassifyPreflight(Channel{Harness: HarnessOpenCode}, errors.New("exit status 1"), bannerLast)
	if !strings.Contains(v.Reason, "Unauthorized") {
		t.Fatalf("reason = %q, want the error rather than the status banner that followed it", v.Reason)
	}

	// Negative control: stripping must not eat ordinary text, including the non-ASCII that
	// shows up throughout this project.
	if got := StripANSI("plain 文字 text"); got != "plain 文字 text" {
		t.Errorf("StripANSI mangled plain text: %q", got)
	}
	// A harness that fails while explaining NOTHING. Measured: opencode's preflight exited
	// non-zero having printed only a deprecation notice and a model banner. Reporting the
	// banner alone reads like a status update, so the exit status has to stay attached and the
	// line has to be labelled for what it is.
	silent := "[oh-my-opencode-slim] Deprecated tmux config key found and ignored.\n" +
		"\x1b[0m > orchestrator · openai/gpt-5.5 \x1b[0m\n"
	v = ClassifyPreflight(Channel{Harness: HarnessOpenCode}, errors.New("exit status 1"), silent)
	if !strings.Contains(v.Reason, "exit status 1") {
		t.Errorf("a harness that failed without explaining itself lost its exit status: %q", v.Reason)
	}
	if !strings.Contains(v.Reason, "no error text") {
		t.Errorf("reason %q presents a status banner as though it were the cause", v.Reason)
	}

	// Negative control for the error-preferring scan: when NOTHING looks like an error, the
	// last real line must still be carried, not dropped.
	v = ClassifyPreflight(Channel{Harness: HarnessPi}, errors.New("exit status 2"), "step one\nstep two\n")
	if !strings.Contains(v.Reason, "step two") {
		t.Errorf("with no error-shaped line the reason should still carry the last line, got %q", v.Reason)
	}
	// ...and a genuine error line must NOT be padded with the exit status, which would bury it.
	v = ClassifyPreflight(Channel{Harness: HarnessPi}, errors.New("exit status 1"),
		`401 {"type":"error","error":{"message":"invalid x-api-key"}}`)
	if strings.Contains(v.Reason, "no error text") {
		t.Errorf("a real error line was labelled as having none: %q", v.Reason)
	}
	// And output that is ONLY escapes must read as no output, not as a reason.
	v = ClassifyPreflight(Channel{Harness: HarnessOpenCode}, errors.New("exit status 1"), "\x1b[0m\n\x1b[2J\n")
	if v.Reason != "exit status 1" && !strings.Contains(v.Reason, "no output") {
		t.Errorf("all-escape output produced the reason %q; it should fall back to the error "+
			"or say there was no output", v.Reason)
	}
}
