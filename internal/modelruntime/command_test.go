package modelruntime

import (
	"errors"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/workflow"
)

func supportedEffort(native ...string) EffortSupport {
	return EffortSupport{Native: native, Supported: true, Verified: EffortVerificationUnavailable}
}

var (
	writeGrant    = workflow.StepGrant{Authority: workflow.AuthorityWrite, ProducerIsolation: workflow.IsolationShared}
	readOnlyGrant = workflow.StepGrant{Authority: workflow.AuthorityReadOnly, ProducerIsolation: workflow.IsolationRequired}
)

// TestBuildCommandExactArgv pins the exact per-harness command lines. These
// are the parameter-passing contract: model, effort fragment, capability
// carrier, isolation flag and prompt each have one place, and "--" protects
// the prompt on the two harnesses whose flags are variadic.
func TestBuildCommandExactArgv(t *testing.T) {
	tests := []struct {
		name      string
		candidate workflow.ModelCandidate
		grant     workflow.StepGrant
		effort    EffortSupport
		wantArgs  []string
	}{
		{
			name:      "cc write",
			candidate: workflow.ModelCandidate{Harness: "cc", Model: "sonnet", Effort: "high"},
			grant:     writeGrant,
			effort:    supportedEffort("--effort", "high"),
			wantArgs: []string{"-p", "--permission-mode", "acceptEdits", "--model", "sonnet",
				"--effort", "high", "--", "DO THE WORK"},
		},
		{
			name:      "codex write",
			candidate: workflow.ModelCandidate{Harness: "codex", Model: "gpt-6-astra", Effort: "high"},
			grant:     writeGrant,
			effort:    supportedEffort("-c", `model_reasoning_effort="high"`),
			wantArgs: []string{"exec", "-s", "workspace-write", "--skip-git-repo-check", "-m", "gpt-6-astra",
				"-c", `model_reasoning_effort="high"`, "PROMPT"},
		},
		{
			name:      "codex read-only + isolation",
			candidate: workflow.ModelCandidate{Harness: "codex", Model: "gpt-6-astra", Effort: "high"},
			grant:     readOnlyGrant,
			effort:    supportedEffort("-c", `model_reasoning_effort="high"`),
			wantArgs: []string{"exec", "-s", "read-only", "--skip-git-repo-check", "-m", "gpt-6-astra",
				"-c", `model_reasoning_effort="high"`, "--ephemeral", "PROMPT"},
		},
		{
			name:      "opencode write",
			candidate: workflow.ModelCandidate{Harness: "opencode", Model: "anthropic/claude-opus-4-6", Effort: "medium"},
			grant:     writeGrant,
			effort:    supportedEffort("--variant", "medium"),
			wantArgs: []string{"run", "--auto", "--model", "anthropic/claude-opus-4-6",
				"--variant", "medium", "PROMPT"},
		},
		{
			name:      "pi write",
			candidate: workflow.ModelCandidate{Harness: "pi", Model: "sub2api-glm/glm-5.3", Effort: "high"},
			grant:     writeGrant,
			effort:    supportedEffort("--thinking", "high"),
			wantArgs:  []string{"-p", "--model", "sub2api-glm/glm-5.3", "--thinking", "high", "--", "PROMPT"},
		},
		{
			name:      "pi read-only + isolation carries allowlist and no-session",
			candidate: workflow.ModelCandidate{Harness: "pi", Model: "sub2api-glm/glm-5.3", Effort: "low"},
			grant:     readOnlyGrant,
			effort:    supportedEffort("--thinking", "low"),
			wantArgs: []string{"-p", "--model", "sub2api-glm/glm-5.3", "--thinking", "low",
				"--tools", "read,grep,find,ls,polyforge_pf_get_work_item,polyforge_pf_get_step,polyforge_pf_list_work_items,polyforge_pf_recall",
				"--no-session", "--", "REVIEW"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prompt := tc.wantArgs[len(tc.wantArgs)-1]
			cmd, err := BuildCommand(tc.candidate, tc.grant, tc.effort, prompt)
			if err != nil {
				t.Fatalf("BuildCommand: %v", err)
			}
			if cmd.Path != HarnessBinary[tc.candidate.Harness] {
				t.Errorf("path = %q", cmd.Path)
			}
			if !cmd.CloseStdin {
				t.Error("CloseStdin must always be set: an open stdin hangs pi -p forever")
			}
			if strings.Join(cmd.Args, "\x00") != strings.Join(tc.wantArgs, "\x00") {
				t.Errorf("argv:\n got  %q\n want %q", cmd.Args, tc.wantArgs)
			}
		})
	}
}

func TestBuildCommandRefusals(t *testing.T) {
	c := workflow.ModelCandidate{Harness: "opencode", Model: "anthropic/claude-opus-4-6", Effort: "medium"}
	eff := supportedEffort("--variant", "medium")
	// opencode has NO read-only carrier on the new path: refusing is the
	// visible failure, never a silently widened dispatch.
	_, err := BuildCommand(c, readOnlyGrant, eff, "p")
	if err == nil || !errors.Is(err, ErrNoReadOnlyCarrier) {
		t.Errorf("opencode read_only must refuse with ErrNoReadOnlyCarrier, got: %v", err)
	}
	// An unsupported effort mapping never becomes a command.
	if _, err := BuildCommand(c, writeGrant, EffortSupport{}, "p"); err == nil ||
		!strings.Contains(err.Error(), "unsupported") {
		t.Errorf("unsupported effort must refuse, got: %v", err)
	}
	// An invalid grant is refused, not defaulted.
	for _, g := range []workflow.StepGrant{
		{Authority: "", ProducerIsolation: workflow.IsolationShared},
		{Authority: workflow.AuthorityWrite, ProducerIsolation: ""},
	} {
		if _, err := BuildCommand(c, g, eff, "p"); err == nil {
			t.Errorf("grant %+v must be refused", g)
		}
	}
	// Unknown harness and empty model/prompt refuse.
	if _, err := BuildCommand(workflow.ModelCandidate{Harness: "claude", Model: "x", Effort: "low"}, writeGrant, eff, "p"); err == nil {
		t.Error("harness claude must be refused (cc is the key)")
	}
	if _, err := BuildCommand(workflow.ModelCandidate{Harness: "cc", Model: "", Effort: "low"}, writeGrant, eff, "p"); err == nil {
		t.Error("empty model must refuse")
	}
}

// TestCCReadOnlyRefused is the Astra-review repair (aihub#708): Claude
// Code's only command-line carrier is a tool DENYLIST, and a denylist is not
// read-only — Bash stays available and Bash is write-capable, so a reviewer
// dispatched "read-only" could still mutate the tree through the shell. The
// read_only grant must REFUSE the cc path with the typed carrier error
// naming that exact reason; the write grant on the same candidate must keep
// working, and the refusal must never degrade into a command that carries
// the denylist as if it were a sandbox.
func TestCCReadOnlyRefused(t *testing.T) {
	c := workflow.ModelCandidate{Harness: "cc", Model: "sonnet", Effort: "medium"}
	eff := supportedEffort("--effort", "medium")

	cmd, err := BuildCommand(c, readOnlyGrant, eff, "REVIEW")
	if err == nil {
		t.Fatalf("cc under a read_only grant must refuse; got argv %q", cmd.Args)
	}
	if !errors.Is(err, ErrNoReadOnlyCarrier) {
		t.Errorf("refusal must be typed ErrNoReadOnlyCarrier (preflight classifies on it), got: %v", err)
	}
	for _, want := range []string{"Bash", "denylist", "write-capable"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must name %q (the denylist-is-not-read-only reason), got: %v", want, err)
		}
	}
	if len(cmd.Args) != 0 || cmd.Path != "" {
		t.Errorf("a refusal must not carry a half-built command: %+v", cmd)
	}

	// The write grant on the same candidate still builds, and carries no
	// denylist pretending to be one.
	wcmd, err := BuildCommand(c, workflow.StepGrant{Authority: workflow.AuthorityWrite, ProducerIsolation: workflow.IsolationShared}, eff, "WORK")
	if err != nil {
		t.Fatalf("cc under a write grant must still build: %v", err)
	}
	if strings.Contains(strings.Join(wcmd.Args, " "), "disallowedTools") {
		t.Errorf("write dispatch carries a read-only carrier: %q", wcmd.Args)
	}
}

// TestNoArbitraryFlagPassthrough pins the structural property: the argv is a
// pure function of (candidate, grant, effort fragment, prompt). There is no
// field on any input that could carry extra flags, and the harness/model
// strings appear exactly once each — a config or DB value can never smuggle
// an extra flag onto the line.
func TestNoArbitraryFlagPassthrough(t *testing.T) {
	c := workflow.ModelCandidate{Harness: "codex", Model: `gpt-6-astra --dangerously-bypass-approvals-and-sandbox`, Effort: "high"}
	cmd, err := BuildCommand(c, writeGrant, supportedEffort("-c", `model_reasoning_effort="high"`), "P")
	if err != nil {
		t.Fatal(err)
	}
	for i, a := range cmd.Args {
		if a == "-m" && i+1 < len(cmd.Args) {
			if cmd.Args[i+1] != c.Model {
				t.Fatalf("model not passed verbatim")
			}
		}
	}
	// The injected model string rides as ONE argv element after -m; it can
	// never be split into extra flags by this builder.
	count := 0
	for _, a := range cmd.Args {
		if strings.Contains(a, "dangerously") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("model string must appear as exactly one argv element, found %d", count)
	}
}

// TestPiReadOnlyToolsMatchesRoles is the cross-check command.go's comment
// promises: the new path's pi read-only allowlist is a frozen copy of
// internal/roles' measured value (which mirrors pf-explore.md's frontmatter),
// duplicated deliberately to keep the new path disjoint from the legacy role
// machinery. This test fails loudly when either side drifts. The two
// constants differ in list separators (roles emits agent-frontmatter spacing,
// this package emits a CLI --tools value), so the comparison is on the
// normalized token set, not raw bytes.
func TestPiReadOnlyToolsMatchesRoles(t *testing.T) {
	src, err := os.ReadFile("../roles/compile.go")
	if err != nil {
		t.Fatalf("reading internal/roles source for the drift check: %v", err)
	}
	re := regexp.MustCompile(`(?m)^\s*const piReadOnlyTools = "([^"]+)"`)
	m := re.FindSubmatch(src)
	if m == nil {
		t.Fatal("internal/roles no longer declares a piReadOnlyTools constant; the drift check must be updated in lockstep")
	}
	normalize := func(s string) string {
		fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
		out := make([]string, 0, len(fields))
		for _, f := range fields {
			if f != "" {
				out = append(out, f)
			}
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	if got, want := normalize(piReadOnlyTools), normalize(string(m[1])); got != want {
		t.Errorf("pi read-only allowlist drifted from internal/roles' measured value:\n modelruntime: %s\n roles:       %s\n pick one and update BOTH (or re-verify against pf-explore.md)",
			piReadOnlyTools, m[1])
	}
}
