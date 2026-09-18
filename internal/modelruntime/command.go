package modelruntime

import (
	"errors"
	"fmt"
	"strings"

	"github.com/GMISWE/ieops-aihub/internal/workflow"
)

// Command is a fully-formed non-interactive harness invocation. It is DATA,
// not an exec.Cmd, so the construction is testable without running anything.
type Command struct {
	// Path is the binary to exec (resolved by Preflight; "cc" maps to the
	// claude binary).
	Path string
	// Args is the complete argv. Fixed derivation from (candidate, grant,
	// prompt) and nothing else — see BuildCommand.
	Args []string
	// CloseStdin reports the child must read /dev/null rather than inherit
	// an open stdin: `pi -p` with an open stdin blocks forever with zero
	// output (measured, aihub#640 survey), and closing it converts any
	// harness's "waiting for input" into a fast EOF. The runner applies it
	// by leaving Stdin nil (os/exec gives the null device).
	CloseStdin bool
}

// HarnessBinary names the executable behind each harness key. "cc" is the
// machine-config/roles spelling of Claude Code; the BINARY is "claude" (the
// name an operator types after drain's --channel). The mapping is stated
// here once, for the new path only; drain keeps its own.
var HarnessBinary = map[string]string{
	"cc":       "claude",
	"codex":    "codex",
	"opencode": "opencode",
	"pi":       "pi",
}

// ErrNoReadOnlyCarrier is the typed refusal a harness that cannot
// represent a read_only grant answers BuildCommand with. Preflight
// classifies it as RefusalReadOnlyUnsupported: the candidate never started,
// the chain records the refusal visibly, and the bounded fallback machine
// may advance — a refusal is never a silent fallback to a wider dispatch.
var ErrNoReadOnlyCarrier = errors.New("no representable read-only carrier")

// piReadOnlyTools is pi's read-only carrier: an exact-match tool ALLOWLIST,
// carrying the same tokens as internal/roles' piReadOnlyTools (which mirrors
// pf-explore.md's frontmatter) — same set, different list separators (this
// copy emits a CLI --tools value, roles emits agent-frontmatter spacing).
// Read tools plus the four read-only polyforge MCP tools, no shell, no
// write, no edit.
//
// It is a frozen measured value duplicated deliberately rather than imported:
// internal/roles is the LEGACY path's package (the new path must not depend
// on role machinery), and importing it would couple this package to a package
// under another work item's ownership. The two constants must stay equal; a
// test cross-checks them textually to fail loudly on drift.
const piReadOnlyTools = "read,grep,find,ls,polyforge_pf_get_work_item,polyforge_pf_get_step,polyforge_pf_list_work_items,polyforge_pf_recall"

const (
	codexReadOnlySandbox = "read-only"
	codexWriteSandbox    = "workspace-write"
)

// BuildCommand constructs the non-interactive invocation for one candidate
// under one grant. The command is derived ONLY from:
//
//	candidate triple  (harness, model, effort)
//	grant             (authority, producer isolation) — the trusted controller policy
//	effort fragment   EffortSupport.Native from the adapter (fixed strings)
//	prompt            the step's prompt, placed last
//
// There is deliberately NO field anywhere on this path that lets a config
// table, a database row or a step author inject raw argv: "no arbitrary flags
// passthrough from DB" (spec) is structural, not a runtime check. Capability
// and isolation carriers follow the grant, so every fallback candidate under
// the same grant gets the same enforcement — fallback can narrow what runs
// but never widen read_only.
//
// The per-harness shapes reuse the shapes internal/drain/channel.go measured
// on 2026-09-14 (this box) for the legacy path, extended with the effort
// fragment. They are restated here rather than shared because the two paths
// legitimately differ: the new path has no role agents, so no --agent/-p
// selector appears, and the carriers must stand alone.
//
//	claude    -p --permission-mode acceptEdits --model M [--effort E] -- <prompt>
//	           (write only: a read_only grant REFUSES — ErrNoReadOnlyCarrier)
//	codex     exec -s read-only|workspace-write --skip-git-repo-check -m M
//	           -c model_reasoning_effort="E" [--ephemeral] <prompt>
//	opencode  run --auto --model M --variant E <prompt>          (write only)
//	pi        -p --model M --thinking E [--tools <allowlist>] [--no-session] -- <prompt>
func BuildCommand(candidate workflow.ModelCandidate, grant workflow.StepGrant, effort EffortSupport, prompt string) (Command, error) {
	if !effort.Supported {
		return Command{}, fmt.Errorf(
			"modelruntime: refusing to build a command whose effort mapping is unsupported (%s); "+
				"an unsupported mapping is a preflight failure, never a silent downgrade",
			effort.Refusal)
	}
	binary, ok := HarnessBinary[candidate.Harness]
	if !ok {
		return Command{}, fmt.Errorf(
			"modelruntime: unknown harness %q (known: cc, codex, opencode, pi)", candidate.Harness)
	}
	if candidate.Model == "" || prompt == "" {
		return Command{}, fmt.Errorf("modelruntime: candidate model and prompt are required")
	}
	switch grant.Authority {
	case workflow.AuthorityReadOnly, workflow.AuthorityWrite:
	default:
		return Command{}, fmt.Errorf(
			"modelruntime: grant authority %q is neither %q nor %q; the trusted policy must be explicit",
			grant.Authority, workflow.AuthorityReadOnly, workflow.AuthorityWrite)
	}
	switch grant.ProducerIsolation {
	case workflow.IsolationShared, workflow.IsolationRequired:
	default:
		return Command{}, fmt.Errorf(
			"modelruntime: grant producer isolation %q is neither %q nor %q; the trusted policy must be explicit",
			grant.ProducerIsolation, workflow.IsolationShared, workflow.IsolationRequired)
	}
	readOnly := grant.Authority == workflow.AuthorityReadOnly
	isolated := grant.ProducerIsolation == workflow.IsolationRequired

	var args []string
	switch candidate.Harness {
	case "cc":
		if readOnly {
			// Claude Code has NO representable read-only carrier on the
			// unattended -p path, and the refusal is deliberate. The only
			// command-line capability carrier is a tool DENYLIST
			// (--disallowedTools), and a denylist is not read-only: Bash
			// stays available and Bash is write-capable — a reviewer
			// dispatched "read-only" could still mutate the tree through
			// the shell (Astra review blocker, aihub#708 mem_xFkJ7fpb).
			// There is no harness-native sandbox on this surface (codex's
			// -s read-only has no cc equivalent), and every measured
			// permission mode is unusable as a read-only carrier: default
			// exits 0 on a prose refusal, dontAsk's deny set is
			// undocumented, bypassPermissions is full write and cannot run
			// as root. Refusing visibly — the same treatment opencode gets —
			// is the only honest option until a fixture-verified read-only
			// carrier exists; it is never a silent fallback to a wider
			// dispatch.
			return Command{}, fmt.Errorf(
				"modelruntime: cc has no read-only carrier on the unattended -p path: the --disallowedTools "+
					"denylist leaves Bash, and Bash is write-capable; refusing a read_only dispatch rather "+
					"than widening it: %w", ErrNoReadOnlyCarrier)
		}
		// --permission-mode acceptEdits is the measured unattended mode:
		// default permissions make -p REFUSE the tool call and exit 0 with a
		// prose refusal; bypassPermissions cannot run as root; dontAsk
		// silently denies Bash.
		args = []string{"-p", "--permission-mode", "acceptEdits"}
		args = append(args, "--model", candidate.Model)
		args = append(args, effort.Native...)
		// "--" keeps a prompt that begins with "-" from being parsed as a
		// flag: the prompt rides as exactly one positional argument.
		args = append(args, "--")
	case "codex":
		mode := codexWriteSandbox
		if readOnly {
			mode = codexReadOnlySandbox
		}
		args = []string{"exec", "-s", mode, "--skip-git-repo-check"}
		args = append(args, "-m", candidate.Model)
		args = append(args, effort.Native...)
		if isolated {
			args = append(args, "--ephemeral")
		}
	case "opencode":
		if readOnly {
			// No carrier exists on the command line: opencode's read-only
			// expression lives in a generated agent file's permission block,
			// and the new path selects no agent (--agent fails OPEN on an
			// unknown name, measured opencode 1.18.30 — "Falling back to
			// default agent" — which would silently dispatch a write-capable
			// default under a read_only grant). Refusing visibly is the only
			// honest option; a later batch may add new-path opencode agents.
			return Command{}, fmt.Errorf(
				"modelruntime: opencode has no representable read-only carrier without an agent selector "+
					"(and the selector fails open); refusing a read_only dispatch rather than widening it: %w",
				ErrNoReadOnlyCarrier)
		}
		args = []string{"run", "--auto"}
		args = append(args, "--model", candidate.Model)
		args = append(args, effort.Native...)
	case "pi":
		args = []string{"-p"}
		args = append(args, "--model", candidate.Model)
		args = append(args, effort.Native...)
		if readOnly {
			args = append(args, "--tools", piReadOnlyTools)
		}
		if isolated {
			args = append(args, "--no-session")
		}
		args = append(args, "--")
	default:
		return Command{}, fmt.Errorf(
			"modelruntime: unknown harness %q (known: %s)", candidate.Harness,
			strings.Join([]string{"cc", "codex", "opencode", "pi"}, ", "))
	}
	return Command{
		Path:       binary,
		Args:       append(args, prompt),
		CloseStdin: true,
	}, nil
}
