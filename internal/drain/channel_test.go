package drain

import (
	"errors"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/roles"
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
	// ⚠️ This assertion USED TO READ `-s workspace-write`, unconditionally, for every role. Its
	// stated reason was "without a sandbox policy it asks for approval, and read-only cannot
	// complete a write step" — and BOTH halves of that are still honoured, they are just honoured
	// per role now (see TestBuildStepInvocation_TheRolesCapabilityReachesEveryHarness: a write-capable
	// role still gets workspace-write, for exactly that reason). What the old form additionally
	// asserted, without meaning to, was that a READ-ONLY role must also run write-capable — the
	// defect aihub#678 ① is about, pinned as a requirement by the test suite.
	//
	// What is left here is the probe, which has no role at all. It gets read-only because
	// PreflightPrompt forbids tool use, so a sandbox that enforces that makes "the probe cannot
	// have a side effect on the repository it runs in" (ClassifyPreflight's own promise)
	// structural rather than merely requested.
	if !hasPair(codex, "-s", "read-only") {
		t.Errorf("the codex PROBE's args %v lack `-s read-only`: without a sandbox policy codex "+
			"asks for approval, and the probe is specified to use no tools at all", codex)
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

// stepArgsOf builds a STEP invocation (as opposed to the roleless probe) and fails on error.
func stepArgsOf(t *testing.T, ch Channel, b Binding) []string {
	t.Helper()
	inv, err := BuildStepInvocation(ch, b, "PROMPT")
	if err != nil {
		t.Fatalf("BuildStepInvocation(%s, %+v): %v", ch, b, err)
	}
	return inv.Args
}

// TestBuildStepInvocation_TheRolesCapabilityReachesEveryHarness is aihub#678 ①.
//
// # What was broken
//
// executeWorkItem resolved a role and its read_only capability and put both on DispatchRequest;
// BuildInvocation took neither argument. `git grep ReadOnly` over the non-test sources found three
// hits, all WRITES (the field, its assignment, drainResolveRole's return) and zero reads. So every
// role on all four harnesses got one bare command line, and a `code_review` step — which
// drainResolveRole correctly resolves to the read-only reviewer, its own comment promising "never
// silently to the write-capable executor" — was spawned as
// `claude -p --permission-mode acceptEdits`: write-capable, edits AUTO-APPROVED. The identical
// step under B/C dispatches polyforge:step-reviewer, whose agent file disallows Edit/Write/
// NotebookEdit. Two reviewers found this independently.
//
// # What this pins, per harness, and which half is the safety property
//
// Each row has a capability mechanism that cannot silently fall back, and an agent selector that
// restores the role's prompt and model tier but is best-effort. Only the first is safety.
//
// Mutants watched RED, each applied alone and each `go build`-checked before running:
//
//	M1  drop the `--disallowedTools` append          → claude reviewer arm
//	M2  drop the `--agent` append                    → agent-id arms on three harnesses
//	M3  make CodexSandboxMode default to             → codex reviewer arm
//	    workspace-write for read-only too
//	M4  return the zero Shape for opencode           → opencode reviewer arm (no refusal)
//	    instead of refusing a read-only role
//	M5  emit shape values verbatim (no cliList)      → the no-spaces arms
func TestBuildStepInvocation_TheRolesCapabilityReachesEveryHarness(t *testing.T) {
	const (
		readOnlyRole = "reviewer" // roles/definitions/reviewer.yaml: read_only: true
		writeRole    = "executor" // roles/definitions/executor.yaml: read_only: false
	)
	ro := Binding{Role: readOnlyRole, ReadOnly: true}
	rw := Binding{Role: writeRole, ReadOnly: false}

	t.Run("claude", func(t *testing.T) {
		// Measured 2026-09-14, Claude Code 2.1.258: `--agent polyforge:step-reviewer` resolves and
		// then REFUSES a direct instruction to use Write (no file created, exit 0), while an
		// unknown name exits 1 naming the available agents. `--disallowedTools` is carried
		// alongside because the selector depends on the INSTALLED plugin, which on this machine
		// is two of the five role agents.
		args := stepArgsOf(t, Channel{Harness: HarnessClaude}, ro)
		if !hasPair(args, "--agent", "polyforge:step-reviewer") {
			t.Errorf("claude read-only args %v do not select the reviewer agent. Without it the "+
				"step runs on the default agent at the default model tier — the very widening "+
				"this fix exists to close", args)
		}
		if !hasPair(args, "--disallowedTools", "Edit,Write,NotebookEdit") {
			t.Errorf("claude read-only args %v carry no --disallowedTools. This is the half that "+
				"does not depend on the installed plugin, so it is the half that must never be "+
				"missing", args)
		}

		w := stepArgsOf(t, Channel{Harness: HarnessClaude}, rw)
		if !hasPair(w, "--agent", "polyforge:step-executor") {
			t.Errorf("claude write args %v do not select the executor agent", w)
		}
		if has(w, "--disallowedTools") {
			t.Errorf("claude WRITE args %v deny Edit/Write/NotebookEdit. A write-capable role that "+
				"cannot write completes no step: roles.CompileCapability returns an EMPTY shape "+
				"for it precisely so nothing is emitted", w)
		}
	})

	t.Run("codex", func(t *testing.T) {
		// `-s` is the load-bearing half and `-p` is not: measured, `codex exec -p step-nosuchrole`
		// is SILENTLY ACCEPTED — no error, no warning, the run proceeds — so the profile flag
		// cannot be relied on for anything and the sandbox mode carries the capability alone.
		args := stepArgsOf(t, Channel{Harness: HarnessCodex}, ro)
		if !hasPair(args, "-s", "read-only") {
			t.Errorf("codex read-only args %v run in a WRITE sandbox. `-s` is the only enforcement "+
				"codex has here, because `-p <missing profile>` is silently ignored", args)
		}
		if !hasPair(args, "-p", "step-reviewer") {
			t.Errorf("codex read-only args %v name no profile; the $CODEX_HOME/step-<role>.config.toml "+
				"files aihub#655 generates have no other consumer", args)
		}

		w := stepArgsOf(t, Channel{Harness: HarnessCodex}, rw)
		if !hasPair(w, "-s", "workspace-write") {
			t.Errorf("codex write args %v lack `-s workspace-write`: without a sandbox policy it "+
				"asks for approval, and read-only cannot complete a write step — the original "+
				"reason this assertion was written, now applied per role", w)
		}
		if !hasPair(w, "-p", "step-executor") {
			t.Errorf("codex write args %v name no profile", w)
		}
	})

	t.Run("pi", func(t *testing.T) {
		// pi has no process-level agent selector at all (roles.DispatchFor("pi").Call is the
		// in-session `subagent(agent=…)` tool, and `pi --help` lists none), so its capability
		// flag is the whole of what drain can do — and it maps exactly: `--tools, -t <tools>` is
		// documented as "Comma-separated allowlist of tool names to enable", which is the shape
		// roles' PiTools already holds.
		args := stepArgsOf(t, Channel{Harness: HarnessPi}, ro)
		var tools string
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--tools" {
				tools = args[i+1]
			}
		}
		if tools == "" {
			t.Fatalf("pi read-only args %v carry no --tools allowlist, so the step runs with pi's "+
				"whole tool set including edit and write", args)
		}
		if strings.Contains(tools, " ") {
			t.Errorf("pi --tools value %q contains a space. The flag takes a COMMA-separated list "+
				"and documents no trimming, so \" write\" would match no tool and the allowlist "+
				"would silently admit or exclude the wrong set", tools)
		}
		for _, want := range []string{"read", "grep", "polyforge_pf_get_step"} {
			if !strings.Contains(tools, want) {
				t.Errorf("pi --tools value %q is missing %q; a step agent that cannot read its own "+
					"step cannot run", tools, want)
			}
		}
		if has(stepArgsOf(t, Channel{Harness: HarnessPi}, rw), "--tools") {
			t.Error("pi WRITE args carry a --tools allowlist; an allowlist on a write-capable role " +
				"removes the tools the role exists to use")
		}
		for _, flag := range []string{"--agent", "-a"} {
			if has(stepArgsOf(t, Channel{Harness: HarnessPi}, rw), flag) {
				t.Errorf("pi args carry %s, which pi has no such flag for: its agents are reachable "+
					"only through the in-session subagent tool", flag)
			}
		}
	})

	t.Run("opencode carries read_only in the agent file, and refuses only when it cannot", func(t *testing.T) {
		// ⚠️ THIS SUBTEST ASSERTED THE OPPOSITE in the first draft of this change, and a
		// clean-context reviewer was right to reject it. The premise was "roles.CompileCapability
		// refuses opencode, therefore opencode cannot express read_only". False: it is excluded
		// from SupportedHarnesses because its expression is a different SHAPE that lives in the
		// same package — render_opencode.go's opencodePermissionBlock writes
		// `permission:\n  edit: deny` into the generated agent file, and roles/dispatch.go says
		// so in as many words. Refusing outright would have FAILED every work item with a review
		// step on a correctly configured `--channel=opencode` run.
		oc := stepArgsOf(t, Channel{Harness: HarnessOpenCode}, ro)
		if !hasPair(oc, "--agent", "step-reviewer") {
			t.Fatalf("opencode read-only args %v select no agent. The agent file is the ONLY "+
				"carrier of read_only here, so not selecting it IS the widening", oc)
		}

		// What IS true is that the carrier fails open — measured, `--agent step-nosuchrole`
		// prints `! agent "…" not found. Falling back to default agent` and runs the
		// write-capable default. Two defences, and this is the second: when the selector is
		// SUPPRESSED (the stale-plugin retry path), opencode has nothing left, so the build is
		// refused rather than producing an unrestricted command line that looks restricted.
		// The first defence is after the fact, in dispatchWithFallback.
		suppressed := Binding{Role: readOnlyRole, ReadOnly: true, NoAgentSelector: true}
		if _, err := BuildStepInvocation(Channel{Harness: HarnessOpenCode}, suppressed, "P"); !errors.Is(err, ErrNoReadOnlyCapability) {
			t.Errorf("opencode built a read-only invocation with the agent selector suppressed "+
				"(err=%v). Nothing on that command line carries the capability", err)
		}
		// The same suppression on claude is FINE, because --disallowedTools survives it. That is
		// the whole point of carrying both there, and the negative control for the guard above.
		cc := stepArgsOf(t, Channel{Harness: HarnessClaude},
			Binding{Role: readOnlyRole, ReadOnly: true, NoAgentSelector: true})
		if has(cc, "--agent") {
			t.Errorf("claude args %v still carry --agent under NoAgentSelector", cc)
		}
		if !hasPair(cc, "--disallowedTools", "Edit,Write,NotebookEdit") {
			t.Errorf("claude args %v lost the capability along with the selector; the retry is "+
				"supposed to drop only the selector", cc)
		}

		// A write-capable role is fine there, and still gets the selector.
		w := stepArgsOf(t, Channel{Harness: HarnessOpenCode}, rw)
		if !hasPair(w, "--agent", "step-executor") {
			t.Errorf("opencode write args %v select no agent", w)
		}
		// ...and the PROBE must not be refused: it has no role, runs no tools, and deleting a
		// whole channel over it would be a worse answer than the one being prevented.
		if _, err := BuildInvocation(Channel{Harness: HarnessOpenCode}, "P"); err != nil {
			t.Errorf("the opencode credential probe was refused: %v", err)
		}
	})
}

// TestBuildStepInvocation_AgentIDsComeFromTheRolesTable is the anti-duplication assertion, and it
// is the reason this fix routes through internal/roles rather than spelling the names locally.
//
// Each harness's agent identity is already decided in that package — render_cc.go writes
// "step-<role>", render_pi.go writes "pf-<role>", and Claude Code needs the plugin-namespaced form
// while the others need the bare one. A table written here would be a SECOND copy free to drift:
// change render_pi.go's prefix and a local copy stays green while every dispatch names an agent
// that does not exist. This compares what the command line carries against what roles.AgentIDFor
// answers, so the two cannot disagree.
//
// Mutant watched: hardcoding "step-%s" for claude turns the cc arm red (it needs "polyforge:").
func TestBuildStepInvocation_AgentIDsComeFromTheRolesTable(t *testing.T) {
	// pi is absent on purpose: it has no process-level agent selector, so there is no command
	// line for its id to appear on. opencode's flag is checked here; its capability is not.
	for _, tc := range []struct {
		harness Harness
		flag    string
		key     string
	}{
		{HarnessClaude, "--agent", "cc"},
		{HarnessCodex, "-p", "codex"},
		{HarnessOpenCode, "--agent", "opencode"},
	} {
		for _, role := range []string{"executor", "operator", "designer"} {
			want, err := roles.AgentIDFor(tc.key, role)
			if err != nil {
				t.Fatalf("roles.AgentIDFor(%q, %q): %v", tc.key, role, err)
			}
			args := stepArgsOf(t, Channel{Harness: tc.harness}, Binding{Role: role})
			if !hasPair(args, tc.flag, want) {
				t.Errorf("%s args %v do not carry %s %s — the id must come from roles.AgentIDFor, "+
					"never from a table written here", tc.harness, args, tc.flag, want)
			}
		}
	}
}

// TestBuildStepInvocation_ClaudeSeparatesTheVariadicFlagFromThePrompt pins a measured trap that
// would have shipped silently: `--disallowedTools` is declared `<tools...>`, so it swallows every
// following positional argument.
//
// Measured, verbatim: `claude -p --permission-mode acceptEdits --disallowedTools
// Edit,Write,NotebookEdit "Use the Write tool … Then reply DONE."` produced three warnings —
// `Permission deny rule "Then" matches no known tool`, likewise "reply" and "DONE." — and then
// `Error: Input must be provided either through stdin or as a prompt argument when using --print`,
// exit 1. The prompt had become deny rules. With `--` in front of it the same command ran.
//
// `--` also makes a prompt that begins with a dash safe, which nothing else here does.
//
// Mutant watched: dropping the `sep` append turns this red.
func TestBuildStepInvocation_ClaudeSeparatesTheVariadicFlagFromThePrompt(t *testing.T) {
	for _, b := range []Binding{{Role: "reviewer", ReadOnly: true}, {Role: "executor"}, PreflightBinding} {
		for _, h := range []Harness{HarnessClaude, HarnessPi} {
			args := stepArgsOf(t, Channel{Harness: h}, b)
			if len(args) < 2 {
				t.Fatalf("%s args %v are too short to carry a prompt", h, args)
			}
			if args[len(args)-1] != "PROMPT" {
				t.Errorf("%s args %v do not end with the prompt", h, args)
			}
			if args[len(args)-2] != "--" {
				t.Errorf("%s args %v do not put `--` immediately before the prompt. This harness "+
					"has a variadic tool-list flag that eats following positionals, measured to "+
					"turn the prompt into deny rules and exit 1", h, args)
			}
		}
	}
}

// TestIsAgentNotFound_AndTheSilentFallback pins the two measured ways an agent selector can fail,
// which are opposites and must not be confused.
//
// claude FAILS CLOSED: exit 1, naming the agents it does have. That is recoverable — the retry
// drops the selector and keeps the capability flags — but only if it is told apart from an
// ordinary step failure, which has the same exit status.
//
// opencode FAILS OPEN: it warns and runs the default agent. For a read-only role
// BuildStepInvocation refuses opencode outright so this cannot arise; for a write-capable role it
// silently discards the role's model tier and prompt, which is worth a log line.
//
// Mutants watched: deleting either matcher's clause turns its arm red; the negative controls
// catch a matcher widened to any "not found".
func TestIsAgentNotFound_AndTheSilentFallback(t *testing.T) {
	// Verbatim, measured 2026-09-14 on this machine.
	const claudeRefusal = "\x1b[0m\x1b[31m\x1b[31m--agent 'nosuchagent-xyz' not found. " +
		"Available agents: claude, Explore, general-purpose, Plan, polyforge:step-executor, " +
		"polyforge:step-reviewer, statusline-setup\x1b[39m\x1b[0m"
	if !IsAgentNotFound(claudeRefusal) {
		t.Error("claude's measured agent refusal was not recognised. The step would be reported " +
			"as a work-item failure, when the real cause is a stale plugin install")
	}
	const opencodeFallback = "\x1b[93m\x1b[1m! \x1b[0m agent \"step-reviewer\" not found. " +
		"Falling back to default agent"
	if !AgentFellBackToDefault(opencodeFallback) {
		t.Error("opencode's measured silent fallback was not recognised; a selector that quietly " +
			"does nothing is how this defect class arrived")
	}

	// Negative controls. An ordinary step failure must not be read as either: doing so would
	// retry every failed step once with a different command line, and log a fallback that never
	// happened.
	for _, ordinary := range []string{
		"--- FAIL: TestThing\nFAIL\tgithub.com/x/y\t0.4s\n",
		"error: file not found: internal/x.go",
		"REVIEW_RESULT: FAIL",
		"",
		// 🔴 The one that made the matcher line-scoped. drain's first customer is aihub's own
		// work items, and THIS FILE contains the string "--agent": a healthy code_change step
		// that greps or diffs it and separately reports a missing file would, under a
		// whole-output scan, be re-run with its agent selector suppressed — at the wrong model
		// tier, for no reason. The same unanchored-substring shape DetectPause documents as
		// dangerous, caught before it shipped rather than after.
		"reading internal/drain/channel.go: args = append(args, \"--agent\", agentID)\n" +
			"go: internal/nope.go: file not found\n",
		"$ rg -- --agent\ninternal/drain/channel.go:250\n\nerror: config not found\n",
	} {
		if IsAgentNotFound(ordinary) {
			t.Errorf("an ordinary failure was read as an agent refusal: %q", ordinary)
		}
		if AgentFellBackToDefault(ordinary) {
			t.Errorf("an ordinary failure was read as an agent fallback: %q", ordinary)
		}
	}
}
