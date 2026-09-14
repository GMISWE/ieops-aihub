package drain

import (
	"fmt"
	"regexp"
	"strings"
)

// Harness names one CLI that can host a step agent. These are the four aihub#640's
// harness_survey_2026_09_13 covers; copilot was explicitly excluded by the owner that day.
type Harness string

const (
	HarnessClaude   Harness = "claude"
	HarnessCodex    Harness = "codex"
	HarnessOpenCode Harness = "opencode"
	HarnessPi       Harness = "pi"
)

// KnownHarnesses is the dispatch order used when no explicit candidate list is configured. It
// is deliberately ordered rather than a set: it doubles as the default candidate list, and the
// first entry that preflights successfully wins.
var KnownHarnesses = []Harness{HarnessClaude, HarnessCodex, HarnessOpenCode, HarnessPi}

// Channel is one (harness, model) pair — a candidate route to a model vendor. aihub#640's
// design_notes_addendum records why this is a LIST per role rather than a single vendor or a
// single tool: "harness 不只是工具，它是模型厂商的接入通道", and a machine's usable set depends on
// what that machine has credentials for. Model may be empty, which means "let the harness pick",
// and for Claude Code that is the correct default — the model belongs in the agent file's
// frontmatter, and passing one explicitly silently OVERRIDES it (measured, aihub#555).
type Channel struct {
	Harness Harness `json:"harness"`
	Model   string  `json:"model,omitempty"`
}

func (c Channel) String() string {
	if c.Model == "" {
		return string(c.Harness)
	}
	return string(c.Harness) + "/" + c.Model
}

// Invocation is a fully-formed, non-interactive harness command: what to exec, with what
// arguments, and whether stdin must be closed. It is data, not an exec.Cmd, so the argument
// construction below is testable without running anything.
type Invocation struct {
	Path string
	Args []string
	// CloseStdin reports that the child must be given /dev/null rather than an inherited or
	// open stdin. See the pi note on BuildInvocation.
	CloseStdin bool
}

// BuildInvocation returns the non-interactive command for running `prompt` under ch.
//
// # The permission flags, and why these exact ones (measured on this machine, 2026-09-14)
//
// aihub#640 `three_ops_problems` ① states the problem as: "非交互下每家都会在工具调用上等确认 …
// 否则每个 step 都会静默卡死等一个永远不来的人". The measurement found the problem is REAL but the
// stated failure mode is WRONG for Claude Code, in a direction that matters:
//
//   - `claude -p` with default permissions does NOT hang. It refuses the tool call, explains the
//     refusal in prose, and EXITS 0 with empty stderr. Measured: a step told to create a file
//     produced no file, exit status 0, stdout "The write … needs your permission". A scheduler
//     that treats exit 0 as success would have recorded that step as completed. This is why
//     Runner does not trust exit status alone (runner.go).
//   - `--permission-mode bypassPermissions` is NOT USABLE HERE AT ALL: it exits 1 in under a
//     second with "--dangerously-skip-permissions cannot be used with root/sudo privileges for
//     security reasons". Drain's own deployment target is a container running as root, so the
//     mode the design sketch reached for first is the one mode that cannot work.
//   - `--permission-mode dontAsk` is the trap: it exits 0 and silently DENIES Bash. Measured, a
//     step told to run `uname -s > f` produced no file and exit 0. Never use it here.
//   - `--permission-mode acceptEdits` is what works: the file write and the Bash command both
//     went through, exit 0, 6-9s. It is what this function emits.
//   - `--permission-mode auto` also works, but it routes each decision through a REMOTE
//     classifier, which makes every step depend on a network service that has nothing to do
//     with the step. For an unattended scheduler that is a new failure mode for no gain.
//
// codex takes `-s workspace-write`. Separately measured and not in the survey: `codex exec`
// refuses outright ("Not inside a trusted directory and --skip-git-repo-check was not
// specified") unless it runs inside a git repository. Drain always runs a step inside the work
// item's worktree, which is one, so this holds — but it holds by circumstance, so
// --skip-git-repo-check is passed explicitly rather than relied upon.
//
// opencode takes `--auto` ("auto-approve permissions that are not explicitly denied").
//
// pi has no confirmation prompt to suppress; its hazard is stdin. The survey records that
// `pi -p` with an open stdin blocks forever with zero stdout AND zero stderr — no error, no
// output, nothing to time out on except a wall clock. CloseStdin is set for every harness
// anyway: an unattended child has no use for an inherited stdin, and closing it converts any
// harness's "waiting for input" into a fast EOF instead of a hang.
func BuildInvocation(ch Channel, prompt string) (Invocation, error) {
	switch ch.Harness {
	case HarnessClaude:
		args := []string{"-p", "--permission-mode", "acceptEdits"}
		if ch.Model != "" {
			args = append(args, "--model", ch.Model)
		}
		args = append(args, prompt)
		return Invocation{Path: "claude", Args: args, CloseStdin: true}, nil

	case HarnessCodex:
		args := []string{"exec", "-s", "workspace-write", "--skip-git-repo-check"}
		if ch.Model != "" {
			args = append(args, "-m", ch.Model)
		}
		args = append(args, prompt)
		return Invocation{Path: "codex", Args: args, CloseStdin: true}, nil

	case HarnessOpenCode:
		args := []string{"run", "--auto"}
		if ch.Model != "" {
			args = append(args, "--model", ch.Model)
		}
		args = append(args, prompt)
		return Invocation{Path: "opencode", Args: args, CloseStdin: true}, nil

	case HarnessPi:
		args := []string{"-p"}
		if ch.Model != "" {
			args = append(args, "--model", ch.Model)
		}
		args = append(args, prompt)
		return Invocation{Path: "pi", Args: args, CloseStdin: true}, nil

	default:
		return Invocation{}, fmt.Errorf("drain: unknown harness %q (known: %s)",
			ch.Harness, joinHarnesses(KnownHarnesses))
	}
}

func joinHarnesses(hs []Harness) string {
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = string(h)
	}
	return strings.Join(out, ", ")
}

// PreflightPrompt is the prompt used to prove a channel actually works before the run starts.
// It asks for a fixed token so the reply can be checked, and asks for no tool use so the probe
// cannot itself have a side effect on the repository it runs in.
const PreflightPrompt = "Reply with exactly the word POLYFORGE_OK and nothing else. Do not use any tools."

// PreflightToken is what a healthy channel echoes back.
const PreflightToken = "POLYFORGE_OK"

// PreflightVerdict is one channel's preflight outcome.
type PreflightVerdict struct {
	Channel Channel `json:"channel"`
	OK      bool    `json:"ok"`
	Reason  string  `json:"reason,omitempty"`
}

// ClassifyPreflight turns a probe's raw result into a verdict.
//
// # Why the probe is a real invocation and not an auth-check subcommand
//
// aihub#640 `three_ops_problems` ② says to pre-flight "所有会用到的通道" because "无人值守时没人能
// 重新登录", and notes each harness has an auth-check-shaped command. Measured 2026-09-14, that
// shortcut does not work: `pi auth check --provider anthropic --json` reports
// `{"status":"ready","provider":"anthropic","authType":"api_key"}` on this machine, and the very
// next real `pi -p` call fails with HTTP 401 "invalid x-api-key". The check verifies a
// credential is PRESENT, not that it is ACCEPTED, and those come apart exactly when a key has
// been revoked or has expired — i.e. in precisely the situation the preflight exists to catch.
// So the probe sends a real (tiny) prompt and requires a real answer.
//
// This is not hypothetical on this machine: three of the four harnesses are unauthenticated
// right now. codex exits 1 with "401 Unauthorized: Missing bearer or basic authentication",
// opencode with "Unauthorized: Invalid API key: HTTP 401", and pi with a 401 body — and only
// Claude Code answers. That is the credential-fallback path being exercised on day one rather
// than in six months, which is the whole argument for having it.
//
// A non-zero exit is a failure. A zero exit whose output lacks the token is ALSO a failure, and
// that branch is load-bearing rather than defensive: it is the same silent-denial shape measured
// on `claude -p` above, where the process succeeds and the work does not happen.
func ClassifyPreflight(ch Channel, exitErr error, output string) PreflightVerdict {
	if exitErr != nil {
		return PreflightVerdict{Channel: ch, OK: false, Reason: summarizeFailure(exitErr, output)}
	}
	if !strings.Contains(output, PreflightToken) {
		return PreflightVerdict{
			Channel: ch,
			OK:      false,
			Reason: fmt.Sprintf("exited 0 but did not echo %s (silent refusal or empty reply): %s",
				PreflightToken, firstLine(output)),
		}
	}
	return PreflightVerdict{Channel: ch, OK: true}
}

// IsAuthFailure reports whether a channel's output looks like a credential problem rather than a
// task failure. It is what lets a mid-run failure fall to the next candidate instead of being
// recorded as the work item's fault — the run-time half of `three_ops_problems` ②.
//
// It matches on the response, not on an exit code, because all three unauthenticated harnesses
// on this machine exit 1 for both reasons: a 401 and a genuinely failed step are the same exit
// status, and only the text tells them apart.
func IsAuthFailure(output string) bool {
	l := strings.ToLower(output)
	for _, needle := range []string{
		"401",
		"unauthorized",
		"invalid api key",
		"invalid x-api-key",
		"authentication_error",
		"missing bearer",
		"oauth token has expired",
		"please run `claude login`",
	} {
		if strings.Contains(l, needle) {
			return true
		}
	}
	return false
}

// ansiRe matches the terminal escape sequences harness CLIs emit even when their output is a
// pipe rather than a tty. It covers CSI (colour, cursor) and the OSC form used for hyperlinks
// and window titles.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// StripANSI removes terminal escape sequences from harness output.
//
// This is not cosmetic, and it was found by running the real thing rather than by reading the
// code. Measured 2026-09-14, the preflight rejection reasons stored in the snapshot came out as:
//
//	codex:    "\x1b[1m\x1b[31mERROR:\x1b[0m\x1b[0m unexpected status 401 Unauthorized: …"
//	opencode: "\x1b[0m"
//
// The first is merely ugly. The SECOND is the bug: opencode's last non-blank line is a lone
// colour reset, so "why is this channel unavailable?" was answered with an invisible control
// sequence — a reason that is non-empty (and so passes a naive "did we give a reason" check)
// while telling an operator nothing at all. Stripping first makes such a line blank, so
// lastMeaningfulLine keeps looking and finds the actual "Unauthorized: Invalid API key: HTTP
// 401" behind it.
func StripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func firstLine(s string) string {
	s = strings.TrimSpace(StripANSI(s))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	const max = 200
	r := []rune(s)
	if len(r) > max {
		// Cut on a rune boundary: harness output is routinely non-ASCII (this project's own
		// work-item goals are Chinese), and a byte slice would store invalid UTF-8 in the
		// snapshot, which then fails to marshal cleanly for `watch --json`.
		return string(r[:max]) + "…"
	}
	if s == "" {
		return "(no output)"
	}
	return s
}

// summarizeFailure turns a failed harness run into one line a person can act on.
//
// Three cases, and the third exists because a real run produced it. Measured 2026-09-14,
// opencode's preflight exited non-zero having printed only a deprecation notice and a model
// banner: no error text anywhere in its combined output. Reporting its last line alone gives
// "> orchestrator · openai/gpt-5.5", which reads like a status update rather than the reason a
// channel was rejected. So when the best line available does NOT look like an error, the exit
// status is kept alongside it, and the line is labelled as what it is.
func summarizeFailure(err error, output string) string {
	line := firstLine(explanatoryLine(output))
	switch {
	case line == "(no output)":
		return err.Error()
	case errorish.MatchString(StripANSI(line)):
		// The harness explained itself. Its own words beat "exit status 1" every time.
		return line
	default:
		return fmt.Sprintf("%v (no error text; last output: %s)", err, line)
	}
}

// errorish matches the words a harness uses when it is reporting why it failed.
var errorish = regexp.MustCompile(`(?i)\b(error|401|403|unauthorized|forbidden|invalid|failed|failure|denied|not found|expired)\b`)

// explanatoryLine picks the line of a failed harness run most likely to say WHY.
//
// It prefers the last error-shaped line and falls back to the last line with any text. The
// fallback alone is not enough, and that is measured rather than assumed: `lastMeaningfulLine`
// was the whole implementation until a real `polyforge drain` run stored opencode's rejection
// reason as "> orchestrator · openai/gpt-5.5" — a status banner. opencode writes its banner to
// stdout and its error to stderr, and once CombinedOutput interleaves the two streams the banner
// can land after the error, so "last" and "the cause" are simply different lines. codex and pi
// happen to put the error last; relying on that is relying on three CLIs agreeing about
// something none of them documents.
func explanatoryLine(output string) string {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		clean := strings.TrimSpace(StripANSI(lines[i]))
		if clean != "" && errorish.MatchString(clean) {
			return lines[i]
		}
	}
	return lastMeaningfulLine(output)
}

// lastMeaningfulLine returns the final line of s that carries actual text. Harness CLIs print
// progress first and the actual error last, so the tail is where the cause is; the head is a
// banner.
//
// "Meaningful" is judged AFTER stripping escape sequences (StripANSI), because a line that is
// nothing but a colour reset is blank to a reader and must not stop the search. Measured on
// opencode's real 401 output, it did: the last non-blank line was "\x1b[0m", and that became the
// stored reason while the actual "Unauthorized: Invalid API key: HTTP 401" sat one line above it.
func lastMeaningfulLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(StripANSI(lines[i])) != "" {
			return lines[i]
		}
	}
	return ""
}
