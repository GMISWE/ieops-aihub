package drain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/roles"
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

// harnessRoleKey maps a drain Harness onto internal/roles' harness key.
//
// The two vocabularies differ in exactly one entry and that is deliberate rather than an
// oversight: drain's name is the BINARY an operator types after `--channel=` ("claude"), while
// internal/roles' key is the harness's identity inside the role catalog ("cc"), which is also
// `polyforge roles generate <harness>`'s positional argument for three of the four. Renaming
// either side would break the spelling one of the two audiences already knows, so the
// translation lives here, once, and every roles lookup in this package goes through it.
var harnessRoleKey = map[Harness]string{
	HarnessClaude:   "cc",
	HarnessCodex:    "codex",
	HarnessOpenCode: "opencode",
	HarnessPi:       "pi",
}

// Binding is the step's resolved role identity — what drain.Runner.ResolveRole returned — carried
// down to the place the command line is built.
//
// 🔴 It exists because it USED NOT TO REACH HERE. `executeWorkItem` resolved a role and its
// read_only capability, put both on DispatchRequest, and `BuildInvocation` took neither: every
// role on all four harnesses got the identical bare command line. So a `code_review` step, which
// `ResolveRole` correctly resolves to the read-only reviewer at the raised tier, was spawned as
// `claude -p --permission-mode acceptEdits` — write-capable with edits AUTO-APPROVED — while the
// same step under the B/C markdown loop dispatches `polyforge:step-reviewer`, whose agent file
// disallows Edit/Write/NotebookEdit. That is the capability-widening class aihub#664 and
// aihub#672 exist to close, on the path aihub#640's design calls "zero nesting" (aihub#678 ①,
// found independently by two reviewers).
type Binding struct {
	// Role is the resolved role name ("reviewer", "executor", ...). Empty ONLY for the
	// credential probe, which is not a step and has no role.
	Role string
	// ReadOnly is the role's capability, straight off roles.Role.Capability.ReadOnly.
	ReadOnly bool
	// NoAgentSelector suppresses the per-harness agent selector while keeping the capability
	// flags. It is the retry shape for a harness that REFUSED the selector, not an option an
	// operator chooses — see ErrNoReadOnlyCapability's sibling note on IsAgentNotFound.
	NoAgentSelector bool
}

// PreflightBinding is the binding for the credential probe.
//
// Role is empty because a probe is not a step: there is no step id, so there is no role to
// resolve and no agent to select. ReadOnly is true because the probe's own prompt forbids tool
// use (PreflightPrompt), and a sandbox that enforces it makes "the probe cannot have a side
// effect on the repository it runs in" structural rather than merely requested.
var PreflightBinding = Binding{ReadOnly: true}

// ErrNoReadOnlyCapability is returned when a harness cannot express a read-only role at all.
//
// Today that is opencode and only opencode, and it is roles.CompileCapability's own position
// rather than a judgement made here: SupportedHarnesses is {"cc","pi","codex"} and the opencode
// case returns an explicit "not implemented in this layer" error (aihub#653 owns opencode's
// permission-rules-table shape). drain must not paper over that, and the measurement says why.
// `opencode run --agent step-nosuchrole` does NOT fail — measured 2026-09-14, opencode 1.18.30,
// it prints
//
//	! agent "step-nosuchrole" not found. Falling back to default agent
//
// and carries on with the DEFAULT agent, which is write-capable. So on opencode the agent file is
// the only carrier of the read-only capability AND the mechanism that selects it fails open. A
// read-only role dispatched there would run write-capable exactly as before this fix, with the
// added insult that the command line would look as though something had been enforced.
//
// The probe is deliberately exempt (see PreflightBinding): refusing there would delete a whole
// channel over an invocation that runs no tools.
var ErrNoReadOnlyCapability = errors.New("drain: harness cannot express a read-only role")

// BuildInvocation returns the non-interactive command for the credential PROBE.
//
// It is BuildStepInvocation with PreflightBinding, so the probe and a step cannot drift in the
// base command they share — which is the whole reason this is a two-line wrapper rather than a
// second switch.
func BuildInvocation(ch Channel, prompt string) (Invocation, error) {
	return BuildStepInvocation(ch, PreflightBinding, prompt)
}

// BuildStepInvocation returns the non-interactive command for running one step agent.
//
// # What each harness needs, and which half is load-bearing (all measured 2026-09-14, this box)
//
// Every row below has two parts, and keeping them apart is the point: a CAPABILITY mechanism that
// cannot silently fall back, and an AGENT SELECTOR that restores the role's prompt and model tier
// but is best-effort. Only the first is a safety property.
//
//	claude    capability: --disallowedTools <list>       selector: --agent polyforge:step-<role>
//	codex     capability: -s read-only|workspace-write   selector: -p step-<role>
//	opencode  capability: (none exists — refused)        selector: --agent step-<role>
//	pi        capability: --tools <allowlist>            selector: (none exists)
//
// claude. `--agent` is measured to FAIL CLOSED: an unknown name exits 1 printing
// `--agent 'x' not found. Available agents: …`, and `--agent polyforge:step-reviewer` resolves and
// then REFUSES a direct instruction to use Write. That is the same mechanism B/C gets, which is
// why it is used rather than something drain invents. `--disallowedTools` is passed alongside it
// because the selector depends on the INSTALLED plugin: this machine's plugin cache (1.1.38) ships
// only step-executor.md and step-reviewer.md while the repo has all five roles, so explorer,
// designer and operator steps would hard-fail on a current binary with a stale plugin — see
// IsAgentNotFound for the disclosed degradation that covers it.
//
// ⚠️ Two measured traps in that flag. `--agents <json>` (defining the agent inline) is NOT a
// substitute: its `disallowedTools` must be an ARRAY, and even given one the restriction is not
// enforced — a probe agent declared with `["Edit","Write","NotebookEdit"]` wrote the file anyway,
// exit 0. And `--disallowedTools` is variadic (`<tools...>`), so it EATS following positional
// arguments: `--disallowedTools Edit,Write,NotebookEdit "<prompt>"` consumed the prompt as three
// more deny rules and died with "Input must be provided …". Hence the `--` this emits before the
// prompt, which also makes a prompt that begins with a dash safe.
//
// ⚠️ And what read_only does NOT mean, so nobody reads more into it than is there: Bash stays
// available, and a claude step told to write a file with Write denied did it with
// `printf > file` instead. That is not a hole this function can close — it is exactly what B/C's
// reviewer has (step-reviewer.md keeps Bash on purpose, for builds and tests, and says in prose
// not to modify the tree). Matching B/C is the acceptance standard (aihub#640
// `workflow_identity_constraint`); a hermetic sandbox would be a capability A has and B/C lacks.
//
// codex. `-s` is the load-bearing half and `-p` is not, which is the opposite of what the
// aihub#655 profile work suggests: `codex exec -p step-nosuchrole` is SILENTLY ACCEPTED — no
// error, no warning, the banner reports the sandbox from `-s` and the run proceeds. So the profile
// flag is safe to pass unconditionally (it is the one consumer those generated
// $CODEX_HOME/step-<role>.config.toml files have ever had) and worthless as a guarantee.
// roles.CompileCapability returns an EMPTY sandbox mode for a write-capable role — "omit and
// inherit codex's default" — which drain cannot use: with no `-s` codex asks for approval and an
// unattended step waits for a human who never comes. So the empty shape resolves to
// codexWriteSandbox here, which is the value channel_test.go pinned before this change and for the
// reason it gave ("read-only cannot complete a write step"); that reason is honoured, it is now
// just honoured per role.
//
// pi. There is no process-level agent selector at all: roles.DispatchFor("pi").Call is the
// in-session `subagent(agent=…)` tool, and `pi --help` lists no `--agent`. Its capability flag is
// an exact match for the compiled shape, though — `--tools, -t <tools>` is documented as
// "Comma-separated allowlist of tool names to enable", which is what roles' PiTools already is.
//
// the evidence for the base command every row above starts from.
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
// codex takes a `-s` sandbox mode — which one is now the role's business, see the codex
// paragraph above; this note is about the OTHER codex flag. Separately measured and not in the
// survey: `codex exec` refuses outright ("Not inside a trusted directory and
// --skip-git-repo-check was not specified") unless it runs inside a git repository. Drain always
// runs a step inside the work item's worktree, which is one, so this holds — but it holds by
// circumstance, so --skip-git-repo-check is passed explicitly rather than relied upon.
//
// opencode takes `--auto` ("auto-approve permissions that are not explicitly denied").
//
// pi has no confirmation prompt to suppress; its hazard is stdin. The survey records that
// `pi -p` with an open stdin blocks forever with zero stdout AND zero stderr — no error, no
// output, nothing to time out on except a wall clock. CloseStdin is set for every harness
// anyway: an unattended child has no use for an inherited stdin, and closing it converts any
// harness's "waiting for input" into a fast EOF instead of a hang.
func BuildStepInvocation(ch Channel, b Binding, prompt string) (Invocation, error) {
	key, ok := harnessRoleKey[ch.Harness]
	if !ok {
		return Invocation{}, fmt.Errorf("drain: unknown harness %q (known: %s)",
			ch.Harness, joinHarnesses(KnownHarnesses))
	}

	// The agent selector, from roles.AgentIDFor and nowhere else. A table hand-written here
	// would be a second copy of a decision internal/roles already owns: rename the prefix in
	// render_pi.go and a local copy stays green while every dispatch names an agent that does
	// not exist (roles/dispatch.go's own header states this).
	agentID := ""
	if b.Role != "" && !b.NoAgentSelector {
		id, err := roles.AgentIDFor(key, b.Role)
		if err != nil {
			return Invocation{}, err
		}
		agentID = id
	}

	// The capability, from roles.CompileCapability and nowhere else.
	shape, capErr := roles.CompileCapability(b.ReadOnly, key)
	if capErr != nil {
		if b.Role != "" && b.ReadOnly {
			return Invocation{}, fmt.Errorf("%w: %s (role %q): %v",
				ErrNoReadOnlyCapability, ch.Harness, b.Role, capErr)
		}
		// Write-capable role, or the roleless probe: there is nothing to express, so an
		// uncompilable capability costs nothing. Fall through with the zero shape.
		shape = roles.Shape{}
	}

	var args []string
	var sep bool // emit "--" before the prompt

	switch ch.Harness {
	case HarnessClaude:
		args = []string{"-p", "--permission-mode", "acceptEdits"}
		if ch.Model != "" {
			args = append(args, "--model", ch.Model)
		}
		if agentID != "" {
			args = append(args, "--agent", agentID)
		}
		if shape.CCDisallowedTools != "" {
			args = append(args, "--disallowedTools", cliList(shape.CCDisallowedTools))
		}
		sep = true

	case HarnessCodex:
		mode := shape.CodexSandboxMode
		if mode == "" {
			mode = codexWriteSandbox
		}
		args = []string{"exec", "-s", mode, "--skip-git-repo-check"}
		if ch.Model != "" {
			args = append(args, "-m", ch.Model)
		}
		if agentID != "" {
			args = append(args, "-p", agentID)
		}

	case HarnessOpenCode:
		args = []string{"run", "--auto"}
		if ch.Model != "" {
			args = append(args, "--model", ch.Model)
		}
		if agentID != "" {
			args = append(args, "--agent", agentID)
		}

	case HarnessPi:
		args = []string{"-p"}
		if ch.Model != "" {
			args = append(args, "--model", ch.Model)
		}
		if shape.PiTools != "" {
			args = append(args, "--tools", cliList(shape.PiTools))
		}
		sep = true

	default:
		// Unreachable: harnessRoleKey above covers exactly KnownHarnesses and returned already
		// for anything else. Kept so a fifth harness added to one table and not the other is a
		// refusal rather than a bare command line.
		return Invocation{}, fmt.Errorf("drain: unknown harness %q (known: %s)",
			ch.Harness, joinHarnesses(KnownHarnesses))
	}

	if sep {
		args = append(args, "--")
	}
	// The Harness value IS the binary name for all four (see KnownHarnesses), which is what
	// preflightChannels' exec.LookPath(inv.Path) relies on. Stated rather than left as a
	// coincidence, because a fifth harness whose CLI is not named after it would need a table
	// here and the omission would look like a typo rather than a missing mapping.
	return Invocation{Path: string(ch.Harness), Args: append(args, prompt), CloseStdin: true}, nil
}

// codexWriteSandbox is the sandbox a WRITE-capable role gets on codex.
//
// roles.CompileCapability deliberately declines to name one (its comment: "this layer does not
// assert a specific write-mode sandbox value … one fewer place this code has an opinion codex's
// own defaults might already hold correctly"). Unattended dispatch cannot take that offer: with
// no `-s` codex routes tool calls through approval, and there is nobody to approve.
const codexWriteSandbox = "workspace-write"

// cliList turns a roles.Shape tool list into a command-line value.
//
// The shapes are YAML frontmatter scalars — "Edit, Write, NotebookEdit" — and both consuming
// flags document COMMA separation (`claude --disallowedTools`: "Comma or space-separated list";
// `pi --tools`: "Comma-separated allowlist"). Neither documents trimming, and a rule that arrives
// as " Write" matching no tool would be a capability that silently did not apply — the failure
// shape this whole change exists to remove. So the spaces come out here rather than being trusted
// to a parser. Measured: `--disallowedTools Edit,Write,NotebookEdit` does disable Write.
func cliList(shapeValue string) string {
	return strings.ReplaceAll(shapeValue, " ", "")
}

// IsAgentNotFound reports whether a harness refused the AGENT SELECTOR, as opposed to failing the
// step.
//
// This is the disclosed-degradation half of the claude row above, and it exists because of a
// measured, CURRENT skew rather than a hypothetical one: the polyforge plugin installed on this
// machine (cache 1.1.38) ships two of the five role agents, while the repo — and therefore the
// binary's embedded roles catalog — has all five. `--agent polyforge:step-explorer` on such a
// machine exits 1 with
//
//	--agent 'polyforge:step-explorer' not found. Available agents: …
//
// before any model call. Left alone, this change would convert a silent capability widening into a
// hard failure of every explorer, designer and operator step — trading one defect for another.
// Recognising the refusal lets the dispatch retry with the selector suppressed and the CAPABILITY
// FLAGS INTACT, which keeps the safety half while saying out loud that the role's prompt and model
// tier could not be applied. A stale plugin is then a logged degradation, not an outage and not a
// silent widening.
//
// Matched on the harness's own words because that is the only signal: the exit status is 1, which
// is also every ordinary step failure.
func IsAgentNotFound(output string) bool {
	l := strings.ToLower(StripANSI(output))
	return strings.Contains(l, "not found. available agents:") ||
		(strings.Contains(l, "--agent") && strings.Contains(l, "not found"))
}

// AgentFellBackToDefault reports whether a harness SILENTLY ignored the agent selector and ran
// something else — measured on opencode 1.18.30, which prints
//
//	! agent "step-reviewer" not found. Falling back to default agent
//
// and proceeds. For a read-only role this cannot happen: BuildStepInvocation refuses opencode
// outright (ErrNoReadOnlyCapability), because a fall back to the write-capable default IS the
// widening. For a write-capable role the fallback is not a capability change but it does discard
// the role's model tier and prompt, which is worth a line in the log rather than nothing at all —
// a selector that quietly does nothing is how this defect class got here in the first place.
func AgentFellBackToDefault(output string) bool {
	return strings.Contains(strings.ToLower(StripANSI(output)), "falling back to default agent")
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
