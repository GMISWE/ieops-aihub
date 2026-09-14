package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/engine"
)

// Cross-implementation contract gate for the step bracket (aihub#657).
//
// WHAT THIS EXISTS TO PROTECT
// ---------------------------
// aihub#640 names three ways this engine runs: A, a future headless orchestrator with no LLM
// turning the crank, and B/C, today's Claude Code / pi session where an LLM reads
// engine.native.md and executes its contents by hand. Until aihub#654 there was exactly one
// implementation of the step bracket and it was PROSE, so "do both paths agree" was not a
// question anyone could ask. aihub#654 added a second — internal/engine — and aihub#657 then
// deleted the prose copy, replacing it with an instruction to run `polyforge engine
// bracket-plan` and make the pf_update_step calls it prints.
//
// That trade is only safe if the documented invocation is RIGHT. A markdown file telling the
// loop to pass `--next-step-attempt-id` is load-bearing in exactly the way pseudocode used to
// be: internal/cli's flag parsing is hand-rolled `strings.HasPrefix(a, "--x=")`, so a flag the
// doc misspells is not rejected — it is SILENTLY IGNORED, and the plan comes back missing a
// field. The failure mode is the aihub#290 one it was meant to remove: the next step never
// starts, no error, no timeline event, just a wi that looks stalled.
//
// WHY THIS SHAPE OF GATE
// A test that hardcoded the argv would pin the test author's idea of the command, not the one
// the markdown ships — and the markdown is what the LLM actually reads. So the flags are
// EXTRACTED from §0h's own fenced block and rendered into argv from there. The Go side computes
// the same sequence by calling engine.PlanStepBracket directly. Both are driven from one
// []BracketInput fixture set, and the assertion is that the two produce an identical
// pf_update_step sequence.
//
// WHAT IS ASSERTED
//  1. §0h documents a bracket-plan invocation that parses, and its flag set is exactly the set
//     this contract knows how to drive — checked in BOTH directions, so neither a new
//     undocumented flag nor a silently deleted one passes.
//  2. For every step-sequence fixture: the sequence a freshly built binary prints, driven by the
//     documented flags, equals the sequence engine.PlanStepBracket returns. A real subprocess,
//     because "the documented command line resolves through cmd/polyforge" is half the claim and
//     an in-process call cannot see it.
//  3. The fixtures reach all four PlanStepBracket branches (failed / last step / fused /
//     degraded two-call). Agreement over one branch is agreement about almost nothing.
//  4. Anti-vacuity: the extractor is run against fixtures reproducing the forms it must see, and
//     two MUTATED flag sets (a dropped boolean, a renamed value flag) must make the comparison
//     go RED. Equality that cannot fail is not evidence.
//
// Deliberately NOT asserted: that an LLM will actually run the command. No static gate can pin
// that; what it can pin is that the command it is told to run produces the right answer.

const (
	// bcBracketDoc carries §0h. The resident fragment points at it rather than restating the
	// flags, which is the whole budget argument for the thinning (see routerBudget's comment).
	bcBracketDoc = "skills/pf-execute/references/engine-native-details.md"

	// bcBracketHeading opens the subsection whose first fenced block is the invocation. Scoped
	// to the heading, not to the file: engine-native-details.md has other fenced blocks and
	// several other `polyforge engine` verbs, and a whole-file scan would be parsing whichever
	// one happened to come first.
	bcBracketHeading = "### `bracket-plan`"

	// bcResidentDoc is the injected fragment. Only one thing is read out of it here: the status
	// its loop opens the FIRST step with, which is the one pf_update_step call in the sequence
	// that PlanStepBracket does not produce.
	bcResidentDoc = "skills/pf-execute/engine.native.md"
)

// bcDocFlag is one flag as §0h writes it. boolean distinguishes `--supports-next-step` (a bare
// switch) from `--step-id=<step_id>` (a value flag); rendering them the same way would either
// emit `--supports-next-step=` (which the hand-rolled parser does not recognise) or drop the
// value flags' values.
type bcDocFlag struct {
	name        string
	placeholder string
	boolean     bool
}

// bcFlagBinding maps a documented flag name onto the BracketInput field that feeds it, and says
// whether this input supplies it at all. It is the ONE piece of glue this test owns: everything
// else about the invocation comes from the markdown.
//
// Keyed on the flag NAME rather than on the placeholder, because the name is what internal/cli
// parses; a doc that renamed `<sa_id>` to `<step_attempt>` has changed nothing that runs, while
// a doc that renamed `--step-attempt-id` has broken the call. The placeholder is still checked —
// see TestEngineBracketPlanDocIsDrivable — but as a readability property, not as a binding.
var bcFlagBinding = map[string]func(in engine.BracketInput) (value string, present bool){
	"step-id":              func(in engine.BracketInput) (string, bool) { return in.StepID, in.StepID != "" },
	"status":               func(in engine.BracketInput) (string, bool) { return in.Status, in.Status != "" },
	"step-attempt-id":      func(in engine.BracketInput) (string, bool) { return in.StepAttemptID, in.StepAttemptID != "" },
	"next-step-id":         func(in engine.BracketInput) (string, bool) { return in.NextStepID, in.NextStepID != "" },
	"next-step-attempt-id": func(in engine.BracketInput) (string, bool) { return in.NextStepAttemptID, in.NextStepAttemptID != "" },
	"supports-next-step":   func(in engine.BracketInput) (string, bool) { return "", in.SupportsNextStep },
	"artifact-summary":     func(in engine.BracketInput) (string, bool) { return in.ArtifactSummary, in.ArtifactSummary != "" },
	"error-type":           func(in engine.BracketInput) (string, bool) { return in.ErrorType, in.ErrorType != "" },
}

// bcDocumentedBracketFlags extracts §0h's bracket-plan invocation and returns its flags in
// document order. It fails loudly at every stage rather than returning an empty set: an empty
// set would make the argv it renders trivially agree with nothing, which is the one way this
// whole file could go green while checking nothing.
func bcDocumentedBracketFlags(t *testing.T, doc string) []bcDocFlag {
	t.Helper()

	h := strings.Index(doc, bcBracketHeading)
	if h < 0 {
		t.Fatalf("%s has no %q subsection. §0h's invocation block is what tells a B/C loop which "+
			"flags to pass and it is what this contract drives; without it the documented path "+
			"cannot be executed, so nothing below is being compared against anything.",
			bcBracketDoc, bcBracketHeading)
	}
	section := doc[h:]
	if end := strings.Index(section[len(bcBracketHeading):], "\n## "); end >= 0 {
		section = section[:len(bcBracketHeading)+end]
	}

	block, ok := bcFirstFencedBlock(section)
	if !ok {
		t.Fatalf("%s: the %q subsection carries no fenced command block. The flags have to be in "+
			"a block this gate can read; prose naming them is not enough, because prose is what "+
			"aihub#657 removed.", bcBracketDoc, bcBracketHeading)
	}

	flags, err := bcParseInvocation(block)
	if err != nil {
		t.Fatalf("%s: cannot parse §0h's bracket-plan invocation (%v). Block:\n%s",
			bcBracketDoc, err, block)
	}
	if len(flags) == 0 {
		t.Fatalf("%s: §0h's invocation names no flags at all. Every argv this gate renders would "+
			"then be a bare `polyforge engine bracket-plan`, which errors — and a comparison that "+
			"never runs proves nothing. Block:\n%s", bcBracketDoc, block)
	}
	return flags
}

// bcFirstFencedBlock returns the body of the first ``` fenced block in s, with the fence's
// info string (```bash) dropped.
func bcFirstFencedBlock(s string) (string, bool) {
	open := strings.Index(s, "```")
	if open < 0 {
		return "", false
	}
	rest := s[open+3:]
	nl := strings.IndexByte(rest, '\n')
	if nl < 0 {
		return "", false
	}
	rest = rest[nl+1:]
	closeAt := strings.Index(rest, "```")
	if closeAt < 0 {
		return "", false
	}
	return rest[:closeAt], true
}

// bcParseInvocation turns a shell invocation — line continuations and all — into its flag list.
func bcParseInvocation(block string) ([]bcDocFlag, error) {
	// A trailing backslash continues the line; join before tokenising, or every flag after the
	// first line is invisible and the gate silently drives a one-line subset of the command.
	joined := strings.ReplaceAll(block, "\\\n", " ")

	fields := strings.Fields(joined)
	if len(fields) < 3 || fields[0] != "polyforge" || fields[1] != "engine" || fields[2] != "bracket-plan" {
		return nil, fmt.Errorf("the block does not start with `polyforge engine bracket-plan` (got %q). "+
			"This gate renders argv from it verbatim, so it has to be the real command", strings.Join(fields, " "))
	}

	var out []bcDocFlag
	for _, tok := range fields[3:] {
		if !strings.HasPrefix(tok, "--") {
			return nil, fmt.Errorf("token %q is not a flag. A positional argument here would be passed "+
				"by a reader and ignored by internal/cli's parser, which accepts only --name=value", tok)
		}
		body := strings.TrimPrefix(tok, "--")
		if i := strings.Index(body, "="); i >= 0 {
			out = append(out, bcDocFlag{name: body[:i], placeholder: body[i+1:]})
			continue
		}
		out = append(out, bcDocFlag{name: body, boolean: true})
	}
	return out, nil
}

// bcRenderArgv builds the argv a B/C loop types for one BracketInput, using ONLY the flags §0h
// documents and in the order it documents them. Flags whose input is absent are omitted, which
// is what §0h's three "Omit …" bullets say to do.
func bcRenderArgv(flags []bcDocFlag, in engine.BracketInput) []string {
	argv := []string{"engine", "bracket-plan"}
	for _, f := range flags {
		bind, known := bcFlagBinding[f.name]
		if !known {
			// Reported as a hard failure by TestEngineBracketPlanDocIsDrivable; skipping here
			// keeps the renderer total so the sequence tests still produce a comparable answer.
			continue
		}
		value, present := bind(in)
		if !present {
			continue
		}
		if f.boolean {
			argv = append(argv, "--"+f.name)
			continue
		}
		argv = append(argv, "--"+f.name+"="+value)
	}
	return argv
}

// ── the fixtures ─────────────────────────────────────────────────────────────────────────────

// bcStep is one step of a sequence fixture, as the loop sees it after its sub-agent returns.
type bcStep struct {
	id      string
	status  string // "completed" | "failed"
	summary string
	errType string
}

// bcSequence is a whole execute loop's worth of steps. supportsNextStep is the ONE fact that is
// a property of the connected server rather than of the steps, so it sits here and not on bcStep
// — lifecycle-details.md §1's "look at what pf_update_step publishes" is decided once per run.
type bcSequence struct {
	name             string
	steps            []bcStep
	supportsNextStep bool
}

// bcSequences deliberately spans all four PlanStepBracket branches; TestEngineBCContract asserts
// that coverage rather than trusting this list to keep it.
var bcSequences = []bcSequence{
	{
		name:             "three steps, fused form, runs to the end",
		supportsNextStep: true,
		steps: []bcStep{
			{id: "spec", status: "completed", summary: "spec written"},
			{id: "code_change", status: "completed", summary: "code landed"},
			{id: "code_review", status: "completed", summary: "review PASS"},
		},
	},
	{
		name:             "three steps, degraded two-call form (older binary)",
		supportsNextStep: false,
		steps: []bcStep{
			{id: "spec", status: "completed", summary: "spec written"},
			{id: "code_change", status: "completed", summary: "code landed"},
			{id: "code_review", status: "completed", summary: "review PASS"},
		},
	},
	{
		name:             "review FAIL mid-sequence stops the loop",
		supportsNextStep: true,
		steps: []bcStep{
			{id: "spec", status: "completed", summary: "spec written"},
			{id: "code_review", status: "failed", errType: "review_fail"},
			{id: "never_reached", status: "completed", summary: "must not appear"},
		},
	},
	{
		name:             "single step, first and last at once",
		supportsNextStep: true,
		steps:            []bcStep{{id: "chore", status: "completed", summary: "done"}},
	},
	{
		name:             "summary carrying spaces, an equals sign and a hash",
		supportsNextStep: true,
		steps: []bcStep{
			{id: "commit_and_pr", status: "completed", summary: "pr=GMISWE/aihub#657 base=main"},
			{id: "wrap", status: "completed", summary: "wrapped"},
		},
	},
}

// bcInputs turns a sequence fixture into the ordered BracketInputs its loop would build. The
// loop stops after a failed step (§0c: both loops break there), so later steps are not reached —
// modelling that here is what makes "never_reached" a real assertion rather than a comment.
func bcInputs(seq bcSequence) []engine.BracketInput {
	var out []engine.BracketInput
	for i, s := range seq.steps {
		in := engine.BracketInput{
			StepID:           s.id,
			StepAttemptID:    bcAttemptID(i),
			Status:           s.status,
			ArtifactSummary:  s.summary,
			ErrorType:        s.errType,
			SupportsNextStep: seq.supportsNextStep,
		}
		if s.status != "failed" && i+1 < len(seq.steps) {
			in.NextStepID = seq.steps[i+1].id
			in.NextStepAttemptID = bcAttemptID(i + 1)
		}
		out = append(out, in)
		if s.status == "failed" {
			break
		}
	}
	return out
}

// bcAttemptID is a deterministic stand-in for new_ulid(). Both paths are driven from the same
// inputs, so the value only has to be stable and distinguishable.
func bcAttemptID(i int) string {
	return "sa-" + string(rune('A'+i))
}

// ── the two implementations ──────────────────────────────────────────────────────────────────

// bcGoSequence is the Go path: engine.PlanStepBracket called directly, concatenated over the
// sequence. This is what a future headless orchestrator (path A) would execute.
func bcGoSequence(inputs []engine.BracketInput) []engine.StepCall {
	out := []engine.StepCall{}
	for _, in := range inputs {
		out = append(out, engine.PlanStepBracket(in)...)
	}
	return out
}

// bcDocumentedSequence is the B/C path: for each step, render the argv §0h documents, run the
// real binary, and concatenate what it prints. This is what a session following the thinned
// markdown produces.
func bcDocumentedSequence(t *testing.T, bin string, flags []bcDocFlag, inputs []engine.BracketInput) []engine.StepCall {
	t.Helper()
	out := []engine.StepCall{}
	for _, in := range inputs {
		out = append(out, bcRunBracketPlan(t, bin, bcRenderArgv(flags, in))...)
	}
	return out
}

// bcRunBracketPlan runs one documented invocation and decodes its stdout.
func bcRunBracketPlan(t *testing.T, bin string, argv []string) []engine.StepCall {
	t.Helper()
	cmd := exec.Command(bin, argv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("polyforge %s: %v\nstderr: %s\nThe markdown tells a B/C loop to run exactly this; "+
			"if it does not run, the instruction is dead text.",
			strings.Join(argv, " "), err, stderr.String())
	}
	var calls []engine.StepCall
	if err := json.Unmarshal(stdout.Bytes(), &calls); err != nil {
		t.Fatalf("polyforge %s: stdout is not a []StepCall (%v): %s",
			strings.Join(argv, " "), err, stdout.String())
	}
	return calls
}

// bcBuildBinary builds a FRESH polyforge, never one resolved through PATH: on a developer box
// PATH can hold a stale auto-updated copy built from an older commit (mem_11hd6dBQ), and a gate
// that passed against yesterday's binary says nothing about this tree.
func bcBuildBinary(t *testing.T) string {
	t.Helper()
	repoRoot, err := findRepoRoot(t)
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "polyforge")
	build := exec.Command("go", "build", "-o", bin, "./cmd/polyforge")
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "GOWORK=off")
	var buildOut bytes.Buffer
	build.Stdout = &buildOut
	build.Stderr = &buildOut
	if err := build.Run(); err != nil {
		t.Fatalf("go build ./cmd/polyforge: %v\n%s", err, buildOut.String())
	}
	return bin
}

// ── the gates ────────────────────────────────────────────────────────────────────────────────

// TestEngineBracketPlanDocIsDrivable reconciles §0h's flag set with this contract's binding, in
// both directions. It runs before the sequence comparison because that comparison renders argv
// from this set: a flag the doc dropped would simply not be passed, both paths would then be
// driven from different inputs, and the disagreement would look like an engine bug.
func TestEngineBracketPlanDocIsDrivable(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	flags := bcDocumentedBracketFlags(t, readEngineDoc(t, pluginRoot, bcBracketDoc))

	documented := map[string]bcDocFlag{}
	for _, f := range flags {
		if prev, dup := documented[f.name]; dup {
			t.Errorf("§0h names --%s twice (%q and %q). A reader passes it twice and the "+
				"hand-rolled parser takes the FIRST, so the second is silently inert.",
				f.name, prev.placeholder, f.placeholder)
		}
		documented[f.name] = f
		if _, known := bcFlagBinding[f.name]; !known {
			t.Errorf("§0h tells a B/C loop to pass --%s, which this contract does not know how to "+
				"drive. Either the flag is new and belongs in bcFlagBinding with the BracketInput "+
				"field it feeds, or it does not exist and every reader passing it is passing "+
				"something internal/cli ignores.", f.name)
		}
		if !f.boolean && f.placeholder == "" {
			t.Errorf("§0h writes --%s= with no placeholder, so a reader has nothing to substitute",
				f.name)
		}
	}
	for name := range bcFlagBinding {
		if _, ok := documented[name]; !ok {
			t.Errorf("--%s is no longer documented in §0h, so a B/C loop following the markdown "+
				"never passes it. internal/cli ignores unknown and absent flags alike, so the plan "+
				"comes back missing that field with no error anywhere — the exact silent-drop class "+
				"aihub#290 spent a release removing.", name)
		}
	}

	// The boolean/value split is not cosmetic: rendering `--supports-next-step=true` produces a
	// token the parser's `a == "--supports-next-step"` comparison does not match, so the fused
	// form would silently become the degraded one.
	if f, ok := documented["supports-next-step"]; ok && !f.boolean {
		t.Errorf("§0h documents --supports-next-step as taking a value (%q). internal/cli matches "+
			"it as a bare switch, so a reader following the doc would pass a token it ignores and "+
			"silently get the two-call form.", f.placeholder)
	}
}

// TestEngineBCContract is the contract itself: same step sequences, same pf_update_step calls,
// whichever path produced them.
func TestEngineBCContract(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	flags := bcDocumentedBracketFlags(t, readEngineDoc(t, pluginRoot, bcBracketDoc))
	bin := bcBuildBinary(t)

	// Branch coverage, accumulated across fixtures and asserted at the end. Agreement on one
	// branch is close to free — both paths would agree on an empty plan too.
	seen := map[string]bool{}

	for _, seq := range bcSequences {
		t.Run(seq.name, func(t *testing.T) {
			inputs := bcInputs(seq)

			want := bcGoSequence(inputs)
			got := bcDocumentedSequence(t, bin, flags, inputs)

			if !reflect.DeepEqual(want, got) {
				t.Errorf("the two paths disagree.\nGo  (engine.PlanStepBracket): %s\nB/C (documented "+
					"`polyforge engine bracket-plan`): %s\nThe markdown is what a session executes, "+
					"so a disagreement here means a real loop makes different pf_update_step calls "+
					"than the Go implementation this document claims to be a thin caller of.",
					bcFormat(want), bcFormat(got))
			}

			for _, in := range inputs {
				seen[bcBranchOf(in)] = true
			}
		})
	}

	for _, branch := range []string{"failed", "last-step", "fused", "degraded"} {
		if !seen[branch] {
			t.Errorf("no fixture reached PlanStepBracket's %q branch, so the agreement above says "+
				"nothing about it. The four branches produce different CALL COUNTS (1, 1, 1, 2), "+
				"which is exactly where a mis-documented flag shows up.", branch)
		}
	}
}

// bcBranchOf names which PlanStepBracket branch an input takes. Derived from the input, not read
// off the output, so it is an independent statement about coverage rather than a restatement of
// what the function happened to return.
func bcBranchOf(in engine.BracketInput) string {
	switch {
	case in.Status == "failed":
		return "failed"
	case in.NextStepID == "":
		return "last-step"
	case in.SupportsNextStep:
		return "fused"
	default:
		return "degraded"
	}
}

// TestEngineBCContractDiscriminates is control 4: the comparison above must be capable of
// failing. Both mutants are real drift, not invented ones — a boolean flag left out of a
// documented command and a value flag whose name drifted are the two ways prose about a CLI
// goes wrong, and internal/cli's hand-rolled parser turns both into silence rather than an
// error.
func TestEngineBCContractDiscriminates(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	flags := bcDocumentedBracketFlags(t, readEngineDoc(t, pluginRoot, bcBracketDoc))
	bin := bcBuildBinary(t)

	// A two-step fused sequence: the one shape where every mutant below changes the answer.
	inputs := bcInputs(bcSequence{
		supportsNextStep: true,
		steps: []bcStep{
			{id: "spec", status: "completed", summary: "spec written"},
			{id: "code_change", status: "completed", summary: "code landed"},
		},
	})
	want := bcGoSequence(inputs)

	// The shipped doc must AGREE — otherwise the two mutants below could both "differ" simply
	// because the baseline was already broken, and this control would certify nothing.
	if got := bcDocumentedSequence(t, bin, flags, inputs); !reflect.DeepEqual(want, got) {
		t.Fatalf("the shipped §0h flags already disagree with the Go path, so the mutants below "+
			"prove nothing.\nGo:  %s\nB/C: %s", bcFormat(want), bcFormat(got))
	}

	for _, m := range []struct {
		name    string
		mutate  func([]bcDocFlag) []bcDocFlag
		because string
	}{
		{
			name:    "§0h stops documenting --supports-next-step",
			mutate:  func(fs []bcDocFlag) []bcDocFlag { return bcWithout(fs, "supports-next-step") },
			because: "the loop would then be told to use the degraded two-call form against a server that publishes next_step",
		},
		{
			name: "§0h misspells --next-step-attempt-id",
			mutate: func(fs []bcDocFlag) []bcDocFlag {
				return bcRenamed(fs, "next-step-attempt-id", "next-step-attempt")
			},
			because: "internal/cli ignores the unknown flag, so the fused call starts the next step with a NULL attempt id",
		},
		{
			name:    "§0h stops documenting --artifact-summary",
			mutate:  func(fs []bcDocFlag) []bcDocFlag { return bcWithout(fs, "artifact-summary") },
			because: "every completed step would report an empty summary and pf_get_step's completed_steps would carry nothing",
		},
	} {
		t.Run(m.name, func(t *testing.T) {
			mutated := m.mutate(flags)
			if reflect.DeepEqual(mutated, flags) {
				t.Fatalf("the mutation changed nothing — it is not exercising the gate. Flags: %v",
					bcNamesOf(flags))
			}
			got := bcDocumentedSequence(t, bin, mutated, inputs)
			if reflect.DeepEqual(want, got) {
				t.Errorf("the mutated flag set still produced the Go path's sequence, so this "+
					"contract cannot see the mutation. %s.\nsequence: %s", m.because, bcFormat(got))
			}
		})
	}
}

// TestEngineBCInvocationParserIsNotBlind is control 4's other half: the extractor has to SEE
// what it claims to check. Every fixture here reproduces a form that really appears in, or could
// plausibly be written into, §0h — a flag hidden behind a line continuation is the one that
// matters most, because the shipped block has three of them.
func TestEngineBCInvocationParserIsNotBlind(t *testing.T) {
	t.Run("finds flags across line continuations", func(t *testing.T) {
		flags, err := bcParseInvocation("polyforge engine bracket-plan --step-id=<id> \\\n" +
			"  --status=<completed|failed> \\\n  --supports-next-step\n")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		got := bcNamesOf(flags)
		want := []string{"status", "step-id", "supports-next-step"}
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("flags = %v, want %v. A parser that stops at the first line sees only the "+
				"flags on it, and the shipped block puts five of its eight flags on later lines.",
				got, want)
		}
		for _, f := range flags {
			if f.name == "supports-next-step" && !f.boolean {
				t.Errorf("--supports-next-step parsed as a value flag")
			}
			if f.name == "step-id" && f.placeholder != "<id>" {
				t.Errorf("--step-id placeholder = %q, want <id>", f.placeholder)
			}
		}
	})

	t.Run("rejects a block that is not the command", func(t *testing.T) {
		for _, bad := range []string{
			"polyforge engine startup --workspace-root=<ws>\n",
			"pf_update_step(step_id=<id>, status=\"completed\")\n",
			"bracket-plan --step-id=<id>\n",
		} {
			if _, err := bcParseInvocation(bad); err == nil {
				t.Errorf("parsed %q as a bracket-plan invocation. Accepting the wrong block means "+
					"driving the contract from flags that belong to another verb.", bad)
			}
		}
	})

	t.Run("rejects a positional argument", func(t *testing.T) {
		if _, err := bcParseInvocation("polyforge engine bracket-plan spec --status=completed\n"); err == nil {
			t.Error("accepted a positional argument. internal/cli reads only --name=value, so a " +
				"documented positional is an instruction to type something that is ignored.")
		}
	})

	t.Run("finds the fenced block under a heading", func(t *testing.T) {
		section := bcBracketHeading + " - x\n\ntext\n\n```bash\npolyforge engine bracket-plan --step-id=<id>\n```\n"
		block, ok := bcFirstFencedBlock(section)
		if !ok {
			t.Fatal("no fenced block found in a section that has one")
		}
		if !strings.Contains(block, "bracket-plan") {
			t.Errorf("block = %q, want the invocation", block)
		}
		if strings.Contains(block, "bash") {
			t.Errorf("block = %q — the fence's info string leaked into the body and would parse "+
				"as a positional argument", block)
		}
	})
}

// TestEngineNativeLoopOpensTheFirstStepAsTheEngineDoes covers the one pf_update_step call in a
// sequence that PlanStepBracket does not produce: the in_progress call that opens the FIRST
// step. The status it must carry is DERIVED from the Go side rather than written here — it is
// the status PlanStepBracket itself emits when it has to start a step (the degraded form's
// second call) — so a re-spelling on either side breaks the pair instead of only the copy
// someone remembered to update.
func TestEngineNativeLoopOpensTheFirstStepAsTheEngineDoes(t *testing.T) {
	plan := engine.PlanStepBracket(engine.BracketInput{
		StepID: "spec", StepAttemptID: "sa-A", Status: "completed",
		NextStepID: "code_change", NextStepAttemptID: "sa-B", SupportsNextStep: false,
	})
	if len(plan) != 2 {
		t.Fatalf("the degraded form no longer emits two calls (%d), so the start-call status "+
			"cannot be derived from it: %s", len(plan), bcFormat(plan))
	}
	startStatus := plan[1].Status
	if startStatus == "" {
		t.Fatal("PlanStepBracket's start call carries no status")
	}

	pluginRoot := pluginRootDir(t)
	for _, rel := range []string{bcResidentDoc, bcBracketDoc} {
		body := readEngineDoc(t, pluginRoot, rel)
		want := `status="` + startStatus + `"`
		if !strings.Contains(body, want) {
			t.Errorf("%s never opens a step with %s. Both loops start the first step themselves "+
				"— it is the one call bracket-plan does not make — so a document that names a "+
				"different status leaves the wi with no step open and every later bracket call "+
				"failing validateStepIdentity.", rel, want)
		}
	}
}

// ── small helpers ────────────────────────────────────────────────────────────────────────────

func bcWithout(flags []bcDocFlag, name string) []bcDocFlag {
	out := make([]bcDocFlag, 0, len(flags))
	for _, f := range flags {
		if f.name != name {
			out = append(out, f)
		}
	}
	return out
}

func bcRenamed(flags []bcDocFlag, from, to string) []bcDocFlag {
	out := make([]bcDocFlag, 0, len(flags))
	for _, f := range flags {
		if f.name == from {
			f.name = to
		}
		out = append(out, f)
	}
	return out
}

func bcNamesOf(flags []bcDocFlag) []string {
	out := make([]string, 0, len(flags))
	for _, f := range flags {
		out = append(out, f.name)
	}
	return out
}

// bcFormat renders a call sequence compactly enough to read in a failure message.
func bcFormat(calls []engine.StepCall) string {
	b, err := json.Marshal(calls)
	if err != nil {
		return "<unmarshalable: " + err.Error() + ">"
	}
	return string(b)
}
