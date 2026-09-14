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
// That trade is only safe if the documented invocation is RIGHT, and "right" here has two
// halves that fail in the same silent way:
//
//   - THE FLAG NAMES. internal/cli's parsing is hand-rolled `strings.HasPrefix(a, "--x=")`
//     (engine.go's flagValue), so a flag the doc misspells is not rejected — it is SILENTLY
//     IGNORED and the plan comes back missing a field.
//   - THE QUOTING. The same prefix match means an argument that is not `--name=value` is
//     dropped. A B/C loop types the documented command into a SHELL, so an unquoted
//     `--artifact-summary=pr=x/y#1 base=main` arrives as two words, the summary silently
//     becomes `pr=x/y#1`, and `base=main` is discarded. Exit 0, nothing on stderr.
//
// Both failures are the aihub#290 silent-drop class this engine spent a release removing.
//
// WHY THIS SHAPE OF GATE
// A test that hardcoded the argv would pin the test author's idea of the command, not the one
// the markdown ships — and the markdown is what the LLM actually reads. So the flags are
// EXTRACTED from §0h's own fenced block and rendered into a command line from there, and that
// command line is run THROUGH `sh -c`, because a gate that used exec.Command's argv array would
// deliver every value atomically and could never see the quoting half at all. (It did not, in
// the first version of this file: a fixture summary containing spaces passed while the
// documented command truncated it.) The Go side computes the same sequence by calling
// engine.PlanStepBracket directly. Both are driven from one fixture set, and the assertion is
// that the two produce an identical pf_update_step sequence.
//
// WHAT IS ASSERTED
//  1. §0h documents a bracket-plan invocation that parses; its flag set is exactly the set this
//     contract knows how to drive, checked in BOTH directions; and every value flag is quoted.
//  2. That flag set covers every field of engine.BracketInput, and each binding really reads
//     the field it claims to — so a new field nobody documents cannot leave both paths
//     defaulting it while the comparison stays green.
//  3. For every step-sequence fixture: the sequence a freshly built binary prints, driven
//     through a shell by the documented flags, equals the sequence engine.PlanStepBracket
//     returns.
//  4. §0c's own bracket-plan invocation — the review-FAIL path — is gated the same way. It is a
//     second documented command line with the same failure modes, and leaving it ungated would
//     mean the file's most dangerous branch was the one branch nothing checked.
//  5. The fixtures reach all four PlanStepBracket branches (failed / last step / fused /
//     degraded two-call). Agreement over one branch is agreement about almost nothing.
//  6. Anti-vacuity: the extractor is run against fixtures reproducing the forms it must see,
//     and four MUTATED flag sets — a dropped boolean, a renamed value flag, a dropped value
//     flag, and a value flag left UNQUOTED — must each make the comparison go RED. Equality
//     that cannot fail is not evidence.
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

	// bcFailPathHeading opens §0c, which carries the SECOND documented bracket-plan invocation.
	bcFailPathHeading = "## 0c."

	// bcResidentDoc is the injected fragment. Only one thing is read out of it here: the status
	// its loop opens the FIRST step with, which is the one pf_update_step call in the sequence
	// that PlanStepBracket does not produce.
	bcResidentDoc = "skills/pf-execute/engine.native.md"
)

// bcDocFlag is one flag as the markdown writes it.
//
// boolean distinguishes `--supports-next-step` (a bare switch) from `--step-id='<step_id>'` (a
// value flag); rendering them the same way would either emit `--supports-next-step=` (which the
// bare-token comparison in engine.go's hasFlag does not recognise) or drop the values.
//
// quoted records whether the DOC wraps the value in single quotes. It is not cosmetic and it is
// not the test's own choice: it is rendered faithfully into the command line, so a doc that
// stops quoting a value flag produces a command line that a shell splits — which is exactly
// what a reader copying that doc would get.
type bcDocFlag struct {
	name        string
	placeholder string
	boolean     bool
	quoted      bool
}

// bcFlagBinding maps a documented flag name onto the BracketInput field that feeds it, and says
// whether this input supplies it at all. It is the ONE piece of glue this test owns: everything
// else about the invocation comes from the markdown.
//
// Keyed on the flag NAME rather than on the placeholder, because the name is what internal/cli
// parses; a doc that renamed `<sa_id>` to `<step_attempt>` has changed nothing that runs, while
// a doc that renamed `--step-attempt-id` has broken the call. The placeholder is still checked,
// but as a readability property, not as a binding.
//
// TestEngineBracketPlanDrivesEveryBracketInputField proves each entry really reads the field it
// claims to, so a copy-paste that pointed `next-step-id` at StepID would not survive here.
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

// bcBracketInputFields binds every field of engine.BracketInput to the flag that carries it.
// Reconciled against the struct by reflection, so adding a field without adding a flag fails
// here rather than silently leaving both paths on the zero value.
var bcBracketInputFields = map[string]string{
	"StepID":            "step-id",
	"StepAttemptID":     "step-attempt-id",
	"Status":            "status",
	"ArtifactSummary":   "artifact-summary",
	"ErrorType":         "error-type",
	"NextStepID":        "next-step-id",
	"NextStepAttemptID": "next-step-attempt-id",
	"SupportsNextStep":  "supports-next-step",
}

// bcDocumentedFlags extracts the bracket-plan invocation under `heading` and returns its flags
// in document order. It fails loudly at every stage rather than returning an empty or partial
// set: an empty set makes the argv it renders trivially agree with nothing, which is the one way
// this whole file could go green while checking nothing.
//
// The unknown-flag check lives HERE, not only in TestEngineBracketPlanDocIsDrivable, because
// bcRenderArgv has to skip a flag it cannot drive in order to stay total — so a flag documented
// but unbound would otherwise be invisible to anyone running `-run TestEngineBCContract` alone.
func bcDocumentedFlags(t *testing.T, doc, heading string) []bcDocFlag {
	t.Helper()

	h := strings.Index(doc, heading)
	if h < 0 {
		t.Fatalf("%s has no %q section. Its invocation block is what tells a B/C loop which "+
			"flags to pass and it is what this contract drives; without it the documented path "+
			"cannot be executed, so nothing below is being compared against anything.",
			bcBracketDoc, heading)
	}
	section := doc[h:]
	// Terminate on the next heading of the SAME OR SHALLOWER depth. "\n## " does not match
	// "\n### ", so a "## " terminator correctly runs past sub-headings while a "### " section
	// stops at the next "## ".
	if end := strings.Index(section[len(heading):], "\n## "); end >= 0 {
		section = section[:len(heading)+end]
	}

	block, ok := bcFirstFencedBlock(section)
	if !ok {
		t.Fatalf("%s: the %q section carries no fenced command block. The flags have to be in a "+
			"block this gate can read; prose naming them is not enough, because prose is what "+
			"aihub#657 removed.", bcBracketDoc, heading)
	}

	flags, err := bcParseInvocation(block)
	if err != nil {
		t.Fatalf("%s %q: cannot parse the bracket-plan invocation (%v). Block:\n%s",
			bcBracketDoc, heading, err, block)
	}
	if len(flags) == 0 {
		t.Fatalf("%s %q: the invocation names no flags at all. Every command line this gate "+
			"renders would then be a bare `polyforge engine bracket-plan`, which errors — and a "+
			"comparison that never runs proves nothing. Block:\n%s", bcBracketDoc, heading, block)
	}
	for _, f := range flags {
		if _, known := bcFlagBinding[f.name]; !known {
			t.Fatalf("%s %q tells a B/C loop to pass --%s, which this contract does not know how "+
				"to drive. Either the flag is new and belongs in bcFlagBinding with the "+
				"BracketInput field it feeds, or it does not exist and every reader passing it is "+
				"passing something internal/cli ignores. Until then every comparison below is "+
				"driven from a different command than the one documented.",
				bcBracketDoc, heading, f.name)
		}
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
			"This gate renders a command line from it verbatim, so it has to be the real command",
			strings.Join(fields, " "))
	}

	var out []bcDocFlag
	for _, tok := range fields[3:] {
		if !strings.HasPrefix(tok, "--") {
			return nil, fmt.Errorf("token %q is not a flag. A positional argument here would be passed "+
				"by a reader and ignored by internal/cli's parser, which reads only --name=value", tok)
		}
		body := strings.TrimPrefix(tok, "--")
		i := strings.Index(body, "=")
		if i < 0 {
			out = append(out, bcDocFlag{name: body, boolean: true})
			continue
		}
		name, placeholder := body[:i], body[i+1:]
		quoted := strings.HasPrefix(placeholder, "'")
		if quoted {
			if len(placeholder) < 2 || !strings.HasSuffix(placeholder, "'") {
				// Whitespace inside a quoted placeholder would also land here, because Fields
				// splits on it. Either way the doc is not something a reader can copy.
				return nil, fmt.Errorf("--%s opens a quote it does not close (%q). A reader "+
					"copying this gets an unterminated string or a split argument", name, placeholder)
			}
			placeholder = placeholder[1 : len(placeholder)-1]
		}
		out = append(out, bcDocFlag{name: name, placeholder: placeholder, quoted: quoted})
	}
	return out, nil
}

// bcShellQuote renders s as a single POSIX shell word.
func bcShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// bcRenderCommand builds the command line a B/C loop types for one BracketInput, using ONLY the
// flags the markdown documents, in the order it documents them, AND its quoting. Flags whose
// input is absent are omitted, which is what §0h's "Omit …" bullets say to do.
//
// An unquoted value flag is emitted verbatim — NOT re-quoted defensively. That is the point: the
// command line has to be the one a reader would type, so that a doc which stops quoting produces
// the same split argument here that it would produce in a real session.
func bcRenderCommand(bin string, flags []bcDocFlag, in engine.BracketInput) string {
	parts := []string{bcShellQuote(bin), "engine", "bracket-plan"}
	for _, f := range flags {
		bind, known := bcFlagBinding[f.name]
		if !known {
			// bcDocumentedFlags already fataled on this; staying total here keeps the renderer
			// usable from the mutation controls, which construct flag sets directly.
			continue
		}
		value, present := bind(in)
		if !present {
			continue
		}
		switch {
		case f.boolean:
			parts = append(parts, "--"+f.name)
		case f.quoted:
			parts = append(parts, "--"+f.name+"="+bcShellQuote(value))
		default:
			parts = append(parts, "--"+f.name+"="+value)
		}
	}
	return strings.Join(parts, " ")
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

// bcSequences deliberately spans all four PlanStepBracket branches (TestEngineBCContract asserts
// that coverage rather than trusting this list to keep it) and, in the last two fixtures, the
// shell metacharacters a real artifact_summary carries. Those two are only meaningful because
// the command runs through `sh -c`: under exec.Command's argv array they would pass whatever the
// doc said about quoting.
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
		name:             "summary in the structured lead-line form lifecycle.md documents",
		supportsNextStep: true,
		steps: []bcStep{
			{id: "commit_and_pr", status: "completed", summary: "pr=GMISWE/aihub#657 base=main"},
			{id: "wrap", status: "completed", summary: "wrapped"},
		},
	},
	{
		name:             "summary carrying a quote, a dollar and a semicolon",
		supportsNextStep: true,
		steps: []bcStep{
			{id: "spec", status: "completed", summary: `it's $HOME; not ${HOME} & not *`},
			{id: "wrap", status: "failed", errType: "review_fail"},
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

// bcDocumentedSequence is the B/C path: for each step, render the command line §0h documents,
// run it THROUGH A SHELL, and concatenate what it prints.
func bcDocumentedSequence(t *testing.T, bin string, flags []bcDocFlag, inputs []engine.BracketInput) []engine.StepCall {
	t.Helper()
	out := []engine.StepCall{}
	for _, in := range inputs {
		out = append(out, bcRunViaShell(t, bcRenderCommand(bin, flags, in))...)
	}
	return out
}

// bcRunViaShell runs one documented command line under `sh -c` and decodes its stdout. The shell
// is load-bearing, not incidental: it is the boundary at which an unquoted value splits.
func bcRunViaShell(t *testing.T, line string) []engine.StepCall {
	t.Helper()
	cmd := exec.Command("sh", "-c", line)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("sh -c %q: %v\nstderr: %s\nThe markdown tells a B/C loop to run exactly this; "+
			"if it does not run, the instruction is dead text.", line, err, stderr.String())
	}
	var calls []engine.StepCall
	if err := json.Unmarshal(stdout.Bytes(), &calls); err != nil {
		t.Fatalf("sh -c %q: stdout is not a []StepCall (%v): %s", line, err, stdout.String())
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
// both directions, and checks the quoting rule. It runs before the sequence comparison because
// that comparison renders a command line from this set: a flag the doc dropped would simply not
// be passed, both paths would then be driven from different inputs, and the disagreement would
// look like an engine bug.
func TestEngineBracketPlanDocIsDrivable(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	flags := bcDocumentedFlags(t, readEngineDoc(t, pluginRoot, bcBracketDoc), bcBracketHeading)

	documented := map[string]bcDocFlag{}
	for _, f := range flags {
		if prev, dup := documented[f.name]; dup {
			t.Errorf("§0h names --%s twice (%q and %q). A reader passes it twice and the "+
				"hand-rolled parser takes the FIRST, so the second is silently inert.",
				f.name, prev.placeholder, f.placeholder)
		}
		documented[f.name] = f
		if f.boolean {
			continue
		}
		if f.placeholder == "" {
			t.Errorf("§0h writes --%s= with no placeholder, so a reader has nothing to substitute",
				f.name)
		}
		// The quoting rule, asserted for EVERY value flag rather than only for the one whose
		// value is prose today. A per-flag judgement ("ids cannot contain spaces") is a rule
		// somebody has to re-make correctly every time a flag is added, and the cost of the
		// blanket rule is four apostrophes.
		if !f.quoted {
			t.Errorf("§0h documents --%s=%s unquoted. A B/C loop types this into a shell, and "+
				"internal/cli reads only tokens matching `--name=`, so any value containing a "+
				"space arrives split: the flag keeps the first word and every later word is "+
				"DROPPED with exit 0 and nothing on stderr. Write --%s='%s'.",
				f.name, f.placeholder, f.name, f.placeholder)
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
	// token engine.go's `a == "--supports-next-step"` comparison does not match, so the fused
	// form would silently become the degraded one.
	if f, ok := documented["supports-next-step"]; ok && !f.boolean {
		t.Errorf("§0h documents --supports-next-step as taking a value (%q). internal/cli matches "+
			"it as a bare switch, so a reader following the doc would pass a token it ignores and "+
			"silently get the two-call form.", f.placeholder)
	}
}

// TestEngineBracketPlanDrivesEveryBracketInputField closes the direction the markdown cannot
// close on its own. Reconciling the doc against bcFlagBinding proves the two agree; it does not
// prove either of them covers engine.BracketInput. A field added to that struct and wired into
// runEngineBracketPlan but documented nowhere would leave BOTH paths on the zero value — and two
// paths that are wrong in the same way agree perfectly.
func TestEngineBracketPlanDrivesEveryBracketInputField(t *testing.T) {
	typ := reflect.TypeOf(engine.BracketInput{})

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i).Name
		flag, mapped := bcBracketInputFields[field]
		if !mapped {
			t.Errorf("engine.BracketInput has a field %q that this contract does not drive. Add "+
				"it to bcBracketInputFields and to §0h's invocation, or the B/C path never sets "+
				"it and agrees with the Go path only because both leave it zero.", field)
			continue
		}
		if _, bound := bcFlagBinding[flag]; !bound {
			t.Errorf("field %q maps to --%s, which is not in bcFlagBinding", field, flag)
		}
	}
	for field := range bcBracketInputFields {
		if _, ok := typ.FieldByName(field); !ok {
			t.Errorf("bcBracketInputFields names %q, which engine.BracketInput no longer has. A "+
				"map larger than the struct hides a field that IS missing behind a count that "+
				"still looks right.", field)
		}
	}

	// ...and each binding must read the field it claims to. A binding table is exactly the
	// shape that survives a copy-paste error: `next-step-id` returning StepID would keep every
	// other assertion in this file green, because both paths are driven from the same table.
	probe := engine.BracketInput{}
	rv := reflect.ValueOf(&probe).Elem()
	for field, flag := range bcBracketInputFields {
		fv := rv.FieldByName(field)
		if !fv.IsValid() {
			continue // already reported above
		}
		switch fv.Kind() {
		case reflect.String:
			fv.SetString("sentinel-" + field)
		case reflect.Bool:
			fv.SetBool(true)
		default:
			t.Errorf("field %q has kind %s, which this probe cannot set; extend it rather than "+
				"leaving the field unverified", field, fv.Kind())
			continue
		}
		value, present := bcFlagBinding[flag](probe)
		if !present {
			t.Errorf("--%s reports absent for an input whose %q is set", flag, field)
		}
		if fv.Kind() == reflect.String && value != "sentinel-"+field {
			t.Errorf("--%s reads %q from an input whose only set field is %s — the binding is "+
				"wired to the wrong field, and both paths would carry that error identically",
				flag, value, field)
		}
		// Reset so each field is probed in isolation; a binding reading a NEIGHBOUR would
		// otherwise be masked by that neighbour also being set.
		switch fv.Kind() {
		case reflect.String:
			fv.SetString("")
		case reflect.Bool:
			fv.SetBool(false)
		}
	}
}

// TestEngineBCContract is the contract itself: same step sequences, same pf_update_step calls,
// whichever path produced them.
func TestEngineBCContract(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	flags := bcDocumentedFlags(t, readEngineDoc(t, pluginRoot, bcBracketDoc), bcBracketHeading)
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
					"`polyforge engine bracket-plan`, run through a shell): %s\nThe markdown is what "+
					"a session executes, so a disagreement here means a real loop makes different "+
					"pf_update_step calls than the Go implementation this document claims to be a "+
					"thin caller of.", bcFormat(want), bcFormat(got))
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

// TestEngineFailPathInvocationIsGated covers §0c's bracket-plan invocation — the review-FAIL
// path. It is a second documented command line with the same failure modes as §0h's, and the
// consequences of getting it wrong are worse: this is the call that files the failure, and it
// runs at the moment the loop is about to stop, when nobody is watching the next step start.
func TestEngineFailPathInvocationIsGated(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	doc := readEngineDoc(t, pluginRoot, bcBracketDoc)
	flags := bcDocumentedFlags(t, doc, bcFailPathHeading)

	byName := map[string]bcDocFlag{}
	for _, f := range flags {
		byName[f.name] = f
		if !f.boolean && !f.quoted {
			t.Errorf("§0c documents --%s=%s unquoted; see TestEngineBracketPlanDocIsDrivable for "+
				"why an unquoted value is silently truncated at the first space", f.name, f.placeholder)
		}
	}

	// runEngineBracketPlan rejects a call missing any of these (internal/cli/engine.go).
	for _, required := range []string{"step-id", "status", "step-attempt-id"} {
		if _, ok := byName[required]; !ok {
			t.Errorf("§0c's invocation omits --%s, which runEngineBracketPlan requires. The "+
				"command as documented cannot run at all — and it is the failure path, so the "+
				"first time anyone finds out is a review that could not be filed.", required)
		}
	}

	// The two semantic facts about this call, both of them things §0c asserts in prose.
	if f := byName["status"]; f.placeholder != "failed" {
		t.Errorf("§0c's invocation passes --status=%q; the review-FAIL path must pass `failed`, "+
			"or the step it files is not a failure at all", f.placeholder)
	}
	for _, forbidden := range []string{"next-step-id", "next-step-attempt-id", "supports-next-step"} {
		if _, ok := byName[forbidden]; ok {
			t.Errorf("§0c's invocation passes --%s. next_step is REJECTED on a failed status "+
				"(not ignored), so the documented command would error where the prose says it "+
				"prints exactly one call.", forbidden)
		}
	}

	// ...and it must really produce the one call §0c promises.
	bin := bcBuildBinary(t)
	in := engine.BracketInput{
		StepID: "code_review", StepAttemptID: "sa-A", Status: "failed", ErrorType: "review_fail",
	}
	got := bcRunViaShell(t, bcRenderCommand(bin, flags, in))
	want := engine.PlanStepBracket(in)
	if !reflect.DeepEqual(want, got) {
		t.Errorf("§0c's documented command and engine.PlanStepBracket disagree.\nGo:  %s\nB/C: %s",
			bcFormat(want), bcFormat(got))
	}
	if len(got) != 1 {
		t.Errorf("§0c says the command prints exactly ONE call; it printed %d: %s",
			len(got), bcFormat(got))
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

// TestEngineBCContractDiscriminates is the anti-vacuity control: the comparison above must be
// capable of failing. All four mutants are real drift, not invented ones — a boolean flag left
// out of a documented command, a value flag whose name drifted, a value flag dropped, and a
// value flag that lost its quotes are the ways prose about a CLI goes wrong, and internal/cli's
// prefix-matching parser turns every one of them into silence rather than an error.
func TestEngineBCContractDiscriminates(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	flags := bcDocumentedFlags(t, readEngineDoc(t, pluginRoot, bcBracketDoc), bcBracketHeading)
	bin := bcBuildBinary(t)

	// A two-step fused sequence whose summary contains a space: the one shape on which every
	// mutant below changes the answer.
	inputs := bcInputs(bcSequence{
		supportsNextStep: true,
		steps: []bcStep{
			{id: "spec", status: "completed", summary: "spec written by hand"},
			{id: "code_change", status: "completed", summary: "code landed"},
		},
	})
	want := bcGoSequence(inputs)

	// The shipped doc must AGREE — otherwise the mutants below could all "differ" simply
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
		{
			name:    "§0h stops QUOTING --artifact-summary",
			mutate:  func(fs []bcDocFlag) []bcDocFlag { return bcUnquoted(fs, "artifact-summary") },
			because: "the shell splits the value and internal/cli drops every word after the first, so the step is filed with a truncated summary and exit 0",
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

// TestEngineBCInvocationParserIsNotBlind is the other half of the anti-vacuity work: the
// extractor has to SEE what it claims to check. Every fixture reproduces a form that really
// appears in, or could plausibly be written into, the shipped blocks — a flag hidden behind a
// line continuation matters most, because §0h's block has three of them.
func TestEngineBCInvocationParserIsNotBlind(t *testing.T) {
	t.Run("finds flags across line continuations", func(t *testing.T) {
		flags, err := bcParseInvocation("polyforge engine bracket-plan --step-id='<id>' \\\n" +
			"  --status='<completed|failed>' \\\n  --supports-next-step\n")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		got := bcNamesOf(flags)
		want := []string{"status", "step-id", "supports-next-step"}
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("flags = %v, want %v. A parser that stops at the first line sees only the "+
				"flags on it, and the shipped block puts six of its eight flags on later lines.",
				got, want)
		}
		for _, f := range flags {
			if f.name == "supports-next-step" && !f.boolean {
				t.Errorf("--supports-next-step parsed as a value flag")
			}
			if f.name == "step-id" && (f.placeholder != "<id>" || !f.quoted) {
				t.Errorf("--step-id parsed as placeholder=%q quoted=%v, want <id>/true",
					f.placeholder, f.quoted)
			}
		}
	})

	t.Run("tells a quoted value from an unquoted one", func(t *testing.T) {
		flags, err := bcParseInvocation("polyforge engine bracket-plan --a='<x>' --b=<y>\n")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(flags) != 2 || !flags[0].quoted || flags[1].quoted {
			t.Errorf("quoted flags = %+v, want --a quoted and --b not. Without this distinction "+
				"the unquoted-value mutant is unrepresentable and the quoting rule is unasserted.",
				flags)
		}
		if flags[0].placeholder != "<x>" {
			t.Errorf("quotes leaked into the placeholder: %q", flags[0].placeholder)
		}
	})

	t.Run("rejects an unterminated quote", func(t *testing.T) {
		if _, err := bcParseInvocation("polyforge engine bracket-plan --a='<x>\n"); err == nil {
			t.Error("accepted a flag whose quote is never closed; a reader copying it gets an " +
				"unterminated string, and this gate would render a command that cannot run")
		}
	})

	t.Run("rejects a block that is not the command", func(t *testing.T) {
		for _, bad := range []string{
			"polyforge engine startup --workspace-root='<ws>'\n",
			"pf_update_step(step_id=<id>, status=\"completed\")\n",
			"bracket-plan --step-id='<id>'\n",
		} {
			if _, err := bcParseInvocation(bad); err == nil {
				t.Errorf("parsed %q as a bracket-plan invocation. Accepting the wrong block means "+
					"driving the contract from flags that belong to another verb.", bad)
			}
		}
	})

	t.Run("rejects a positional argument", func(t *testing.T) {
		if _, err := bcParseInvocation("polyforge engine bracket-plan spec --status='completed'\n"); err == nil {
			t.Error("accepted a positional argument. internal/cli reads only --name=value, so a " +
				"documented positional is an instruction to type something that is ignored.")
		}
	})

	t.Run("finds the fenced block under a heading", func(t *testing.T) {
		section := bcBracketHeading + " - x\n\ntext\n\n```bash\npolyforge engine bracket-plan --step-id='<id>'\n```\n"
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

	t.Run("shell quoting survives a round trip", func(t *testing.T) {
		// bcShellQuote is what makes the quoted path faithful; if it were wrong, the quoted
		// fixtures would fail for a reason that has nothing to do with the markdown.
		for _, s := range []string{`plain`, `two words`, `it's`, `$HOME`, `a;b`, `a'b'c`, `*`} {
			out, err := exec.Command("sh", "-c", "printf %s "+bcShellQuote(s)).Output()
			if err != nil {
				t.Fatalf("sh printf %q: %v", s, err)
			}
			if string(out) != s {
				t.Errorf("shell round trip of %q produced %q", s, string(out))
			}
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

	// The other half of what bracket-plan does not do: mint ids. Nothing in internal/engine or
	// internal/cli generates a ulid, so a loop that is not told to mint one reuses the current
	// step_attempt_id for the next step. Both loops must say so.
	for _, rel := range []string{bcResidentDoc, bcBracketDoc} {
		body := readEngineDoc(t, pluginRoot, rel)
		if !strings.Contains(body, "new_ulid()") {
			t.Errorf("%s never tells the loop to mint a new ulid. `bracket-plan` only threads the "+
				"attempt id it is given (internal/cli/engine.go's runEngineBracketPlan is pure), "+
				"so a loop following this document would pass the CURRENT step's attempt id as "+
				"the next step's — silently, since nothing validates it.", rel)
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

func bcUnquoted(flags []bcDocFlag, name string) []bcDocFlag {
	out := make([]bcDocFlag, 0, len(flags))
	for _, f := range flags {
		if f.name == name {
			f.quoted = false
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
