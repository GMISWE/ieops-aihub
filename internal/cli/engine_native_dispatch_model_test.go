package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Tag F (aihub#555, succeeding aihub#553's Tag E) — the step's model is HARNESS-READ
// CONFIGURATION in an agent definition file, never a dispatch argument.
//
// WHAT WENT WRONG, TWICE
// ----------------------
// aihub#338 wired the step-kind model tier (default/raised, keyed on the step id) into
// engine.native.md's auto loop as prose. aihub#544 then measured 3/3 post-cutover review
// dispatches carrying model=None: the tier lived in pseudocode, an omitted model silently
// inherits the parent session's model, and a dead selector produces transcripts identical to
// a live one. aihub#553 (1.1.31) made `model` a REQUIRED argument of the §0b template.
// aihub#555 re-measured after that shipped: 1 of 2 engine-shaped review dispatches STILL
// carried model=None — an argument that lives in prose is copied unevenly, however loudly it
// is marked REQUIRED. So aihub#555 moved the model choice into agent definition files
// (plugins/polyforge/agents/step-executor.md, step-reviewer.md) whose `model:` frontmatter
// the harness itself applies — measured on live dispatches: a plugin agent declaring a model
// ran on that model with no model argument passed, read from the dispatch transcript, not
// from config. The same measurements force the second half of the change: an explicit
// per-invocation `model` argument silently OVERRIDES the agent file, so the argument must be
// DELETED in the same change that ships the files — two live channels mean the files are dead
// text on every dispatch that fills the argument.
//
// WHAT IS ASSERTED
//  1. Both agent files exist, carry `name:` and `model:` frontmatter, and their models are
//     distinct and are not review-depth values (the aihub#358 vocabulary).
//  2. engine.native.md declares STEP_AGENT/REVIEW_AGENT as exactly the plugin-namespaced ids
//     of those files ("polyforge:" + the file's `name:`), and its dispatch line selects
//     subagent_type by is_review and passes no model.
//  3. The §0b template carries an explicit `subagent_type:` argument ahead of the prompt,
//     marked REQUIRED, names BOTH namespaced ids bound to their constants, keys the review
//     agent on is_review, does not mention `level`, and contains NO model argument anywhere —
//     re-adding the argument is the one mutation this gate exists to kill.
//  4. §0f's mapping table names both constants with the same namespaced ids. The model names
//     are deliberately NOT asserted (or copied) there: the agent files are the single copy.
//  5. The reviewer is read-only by construction: its `disallowedTools:` frontmatter covers
//     Edit, Write and NotebookEdit.
//  6. Anti-vacuity: every extractor is run against fixtures reproducing the mutants, so "no
//     violation found" cannot mean "nothing was parsed".
//
// Deliberately NOT asserted: any particular model enum (a re-tier edits the agent files and
// this gate follows), and whether a given harness honours the frontmatter — that was
// aihub#555's live probe (measured, Claude Code 2.1.258), a runtime fact no static gate can
// pin.

const (
	dispatchEngineDoc = "skills/pf-execute/engine.native.md"
	dispatchDetailDoc = "skills/pf-execute/references/engine-native-details.md"

	stepAgentFile   = "agents/step-executor.md"
	reviewAgentFile = "agents/step-reviewer.md"

	// The plugin-name prefix the harness prepends to plugin-shipped agents. Measured
	// (aihub#555): subagent_type for a plugin agent is "<plugin.json name>:<agent name>",
	// and the namespaced id keeps resolving to the plugin file even when a same-named
	// user/project agent exists — only the bare name resolves to the shadow.
	agentIdPrefix = "polyforge:"
)

var (
	// The agent-name constants as the resident loop declares them, e.g.
	//   STEP_AGENT, REVIEW_AGENT = "polyforge:step-executor", "polyforge:step-reviewer"
	agentConstantsRe = regexp.MustCompile(`STEP_AGENT, REVIEW_AGENT = "([a-z][a-z0-9:._-]*)", "([a-z][a-z0-9:._-]*)"`)

	// The agent ids as the §0b template states them, each tied to its constant:
	//   "polyforge:step-reviewer" (REVIEW_AGENT) when is_review(step_id)
	reviewIdRe = regexp.MustCompile(`"([a-z][a-z0-9:._-]*)"\s*\(REVIEW_AGENT\)`)
	stepIdRe   = regexp.MustCompile(`"([a-z][a-z0-9:._-]*)"\s*\(STEP_AGENT\)`)

	// The binding between the review agent and the review predicate. Whitespace-tolerant:
	// the template wraps the subagent_type argument across lines.
	reviewKeyedOnReviewRe = regexp.MustCompile(`\(REVIEW_AGENT\)\s+when\s+is_review\(step_id\)`)

	// The resident loop's dispatch line. Asserted verbatim: it is the single line that makes
	// the agent choice a dispatch argument rather than narrative.
	engineDispatchAgentRe = regexp.MustCompile(`dispatch Agent\(subagent_type=REVIEW_AGENT if is_review\(step_id\) else STEP_AGENT`)

	// Any way of writing a model argument: "model:" (the Agent-call keyword) or "model="
	// (the pseudo-code form). Case-insensitive and whitespace-tolerant, because the defect
	// only has to be REINTRODUCED in a slightly different hand to escape a literal match.
	modelArgRe = regexp.MustCompile(`(?i)\bmodel\s*[:=]`)

	// The agent files' frontmatter `model:` line. Named agentModelRe because
	// hooks/pf-skill-router documents that it reads the SAME source this gate pins.
	agentModelRe = regexp.MustCompile(`(?m)^model:[ \t]*([a-z][a-z0-9.-]*)[ \t]*$`)

	// §0f's step-kind -> agent table — the reader-facing copy of the mapping. It names the
	// agent ids only; the models live solely in the agent files.
	tableReviewAgentRe = regexp.MustCompile("`REVIEW_AGENT` = `([a-z][a-z0-9:._-]*)`")
	tableStepAgentRe   = regexp.MustCompile("`STEP_AGENT` = `([a-z][a-z0-9:._-]*)`")
)

// agentFrontmatter parses the YAML-ish frontmatter block of an agent definition file into a
// flat key -> value map. ok is false when the file has no leading --- fence pair. Only
// single-line scalar fields are supported, which is all these two files may use.
func agentFrontmatter(body string) (map[string]string, bool) {
	if !strings.HasPrefix(body, "---\n") {
		return nil, false
	}
	rest := body[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, false
	}
	out := map[string]string{}
	for _, ln := range strings.Split(rest[:end], "\n") {
		i := strings.Index(ln, ":")
		if i <= 0 || strings.HasPrefix(ln, " ") {
			continue
		}
		out[strings.TrimSpace(ln[:i])] = strings.TrimSpace(ln[i+1:])
	}
	return out, true
}

// dispatchAgentRegion extracts the `subagent_type:` argument of the §0b template — the text
// between `subagent_type:` and the `prompt:` argument that follows it. ok is false when the
// template carries no subagent_type argument at all, or carries it after the prompt (where a
// copier who stops at the prompt never reads it).
func dispatchAgentRegion(tmpl string) (string, bool) {
	s := strings.Index(tmpl, "subagent_type:")
	p := strings.Index(tmpl, "prompt:")
	if s < 0 || p < 0 || s > p {
		return "", false
	}
	return tmpl[s:p], true
}

func readAgentDoc(t *testing.T, pluginRoot, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(pluginRoot, rel))
	if err != nil {
		t.Fatalf("%s: cannot read it (%v). The dispatch names this agent; a missing file means "+
			"every step dispatch fails at the harness, or silently falls back on a harness that "+
			"routes unknown types.", rel, err)
	}
	return string(b)
}

func TestEngineNativeDispatchSelectsAgentNotModel(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	engine := readEngineDoc(t, pluginRoot, dispatchEngineDoc)
	details := readEngineDoc(t, pluginRoot, dispatchDetailDoc)

	// ── The agent files: the mapping's single copy ─────────────────────────────────────────
	stepFM, ok1 := agentFrontmatter(readAgentDoc(t, pluginRoot, stepAgentFile))
	reviewFM, ok2 := agentFrontmatter(readAgentDoc(t, pluginRoot, reviewAgentFile))
	if !ok1 || !ok2 {
		t.Fatalf("agent definition files lack a frontmatter block (step ok=%v, review ok=%v). "+
			"The harness reads name/model from that block; without it nothing below is checked.",
			ok1, ok2)
	}

	t.Run("AgentFilesCarryTheModels", func(t *testing.T) {
		for rel, fm := range map[string]map[string]string{stepAgentFile: stepFM, reviewAgentFile: reviewFM} {
			if fm["name"] == "" {
				t.Errorf("%s frontmatter has no `name:` — the harness derives the dispatchable "+
					"id from it, so the engine's constants would point at nothing", rel)
			}
			if fm["model"] == "" {
				t.Errorf("%s frontmatter has no `model:` — the whole point of the file. Without "+
					"it the dispatch silently inherits the parent session's model, the exact "+
					"aihub#544 state this mechanism replaces.", rel)
			}
			if fm["model"] != "" && scenarioReviewLevels[fm["model"]] {
				t.Errorf("%s declares model %q, a review-depth value (%v) — the mapping has been "+
					"re-keyed onto the vocabulary aihub#358 removed", rel, fm["model"],
					keysOf(scenarioReviewLevels))
			}
		}
		if stepFM["model"] != "" && stepFM["model"] == reviewFM["model"] {
			t.Errorf("both agent files declare model %q — a mapping with one value raises "+
				"nothing", stepFM["model"])
		}
	})

	t.Run("ReviewerIsReadOnlyByConstruction", func(t *testing.T) {
		disallowed := reviewFM["disallowedTools"]
		for _, tool := range []string{"Edit", "Write", "NotebookEdit"} {
			if !regexp.MustCompile(`\b` + tool + `\b`).MatchString(disallowed) {
				t.Errorf("%s disallowedTools (%q) does not cover %s. Read-only BY CONSTRUCTION "+
					"is the reason the review agent exists as a separate definition; without the "+
					"denylist it is read-only by being told, which is the compliance class "+
					"aihub#555 measured failing.", reviewAgentFile, disallowed, tool)
			}
		}
		if stepFM["disallowedTools"] != "" {
			t.Errorf("%s carries disallowedTools %q — the executor must stay write-capable; if "+
				"a denylist is now intended there, move this assertion with the design",
				stepAgentFile, stepFM["disallowedTools"])
		}
	})

	// ── The engine constants bind to the files ─────────────────────────────────────────────
	consts := agentConstantsRe.FindStringSubmatch(engine)
	if consts == nil {
		t.Fatalf("%s no longer declares the agent constants (`STEP_AGENT, REVIEW_AGENT = ...`). "+
			"Every assertion below compares the dispatch template against them, so without the "+
			"declaration nothing here checks anything. If the mapping moved, move this regex "+
			"with it.", dispatchEngineDoc)
	}
	stepAgent, reviewAgent := consts[1], consts[2]

	t.Run("ConstantsAreTheFilesNamespacedIds", func(t *testing.T) {
		if want := agentIdPrefix + stepFM["name"]; stepAgent != want {
			t.Errorf("%s declares STEP_AGENT = %q but %s names itself %q, so the dispatchable id "+
				"is %q — the loop would dispatch an agent that does not exist (or a same-named "+
				"shadow)", dispatchEngineDoc, stepAgent, stepAgentFile, stepFM["name"], want)
		}
		if want := agentIdPrefix + reviewFM["name"]; reviewAgent != want {
			t.Errorf("%s declares REVIEW_AGENT = %q but %s names itself %q, so the dispatchable "+
				"id is %q", dispatchEngineDoc, reviewAgent, reviewAgentFile, reviewFM["name"], want)
		}
		if stepAgent == reviewAgent {
			t.Errorf("STEP_AGENT and REVIEW_AGENT are both %q — a selector with one value raises "+
				"nothing", stepAgent)
		}
		for _, id := range []string{stepAgent, reviewAgent} {
			if !strings.HasPrefix(id, agentIdPrefix) {
				t.Errorf("constant %q is not plugin-namespaced. The bare name resolves to a "+
					"same-named user/project agent when one exists (measured, aihub#555); only "+
					"the %q prefix pins the plugin's own file.", id, agentIdPrefix)
			}
		}
	})

	t.Run("EngineLoopDispatchesByAgentWithNoModel", func(t *testing.T) {
		if !engineDispatchAgentRe.MatchString(engine) {
			t.Errorf("%s: the auto loop's dispatch line no longer passes "+
				"`subagent_type=REVIEW_AGENT if is_review(step_id) else STEP_AGENT` explicitly. "+
				"Without it the agent choice is narrative, which is the aihub#544 state.",
				dispatchEngineDoc)
		}
		if regexp.MustCompile(`dispatch Agent\([^)]*\bmodel\s*=`).MatchString(engine) {
			t.Errorf("%s: the auto loop's dispatch passes a model argument again. An explicit "+
				"per-invocation model silently OVERRIDES the agent file (measured, aihub#555), "+
				"so this single line kills the whole mechanism.", dispatchEngineDoc)
		}
	})

	// ── The §0b template ───────────────────────────────────────────────────────────────────
	tmpl, ok := subAgentPromptTemplate(details)
	if !ok {
		t.Fatalf("%s: could not locate the fenced §0b dispatch template. It is the text "+
			"executors copy verbatim; if it cannot be found, the assertions below are checked "+
			"against nothing.", dispatchDetailDoc)
	}

	t.Run("TemplateSelectsAgentAndCarriesNoModel", func(t *testing.T) {
		region, ok := dispatchAgentRegion(tmpl)
		if !ok {
			t.Fatalf("the §0b template carries no `subagent_type:` argument ahead of `prompt:`. "+
				"The agent id is the only channel that reaches the model files, so a template "+
				"without it dispatches the harness default — model inheritance again. Template:\n%s",
				tmpl)
		}
		if !strings.Contains(region, "REQUIRED") {
			t.Errorf("the template's subagent_type argument is not marked REQUIRED — it reads "+
				"as narrative a dispatcher may skim: %q", region)
		}
		if !strings.Contains(region, "is_review(") {
			t.Errorf("the template's subagent_type argument does not key the choice on "+
				"is_review — the step-kind predicate the owner decided on (aihub#338, "+
				"2026-09-04): %q", region)
		}
		if strings.Contains(strings.ToLower(region), "level") {
			t.Errorf("the template's subagent_type argument mentions `level` — the review-DEPTH "+
				"parameter aihub#358 proved can never select a model: %q", region)
		}
		if m := modelArgRe.FindString(tmpl); m != "" {
			t.Errorf("the §0b template contains a model argument (%q). The template must not "+
				"carry one in ANY position: an explicit model silently overrides the agent "+
				"file (measured, aihub#555), and a copier reproduces the template whole. "+
				"Template:\n%s", m, tmpl)
		}

		review := reviewIdRe.FindStringSubmatch(region)
		step := stepIdRe.FindStringSubmatch(region)
		if review == nil || step == nil {
			t.Fatalf("the subagent_type argument does not state BOTH agents as literal ids "+
				"(`\"<id>\" (REVIEW_AGENT)` / `\"<id>\" (STEP_AGENT)`). A template that names "+
				"only one leaves the other dispatch to the harness default: %q", region)
		}
		if review[1] != reviewAgent {
			t.Errorf("the template reviews with %q but %s declares REVIEW_AGENT = %q — the two "+
				"documents dispatch different agents for review steps", review[1],
				dispatchEngineDoc, reviewAgent)
		}
		if step[1] != stepAgent {
			t.Errorf("the template defaults to %q but %s declares STEP_AGENT = %q — the two "+
				"documents dispatch different agents for non-review steps", step[1],
				dispatchEngineDoc, stepAgent)
		}
		if !reviewKeyedOnReviewRe.MatchString(region) {
			t.Errorf("the REVIEW agent is not bound to the review predicate (`(REVIEW_AGENT) "+
				"when is_review(step_id)`). Whatever else the words say, the binding is what "+
				"stops a swap: %q", region)
		}
	})

	t.Run("SectionZeroFTableAgreesWithConstants", func(t *testing.T) {
		review := tableReviewAgentRe.FindStringSubmatch(details)
		step := tableStepAgentRe.FindStringSubmatch(details)
		if review == nil || step == nil {
			t.Fatalf("%s no longer states the §0f mapping table (`REVIEW_AGENT` = `<id>` / "+
				"`STEP_AGENT` = `<id>`). That table is the copy a reader of the deferred file "+
				"actually consults; if it moved, move this regex with it.", dispatchDetailDoc)
		}
		if review[1] != reviewAgent {
			t.Errorf("§0f's table reviews with %q but %s declares REVIEW_AGENT = %q — the two "+
				"copies contradict each other", review[1], dispatchEngineDoc, reviewAgent)
		}
		if step[1] != stepAgent {
			t.Errorf("§0f's table defaults to %q but %s declares STEP_AGENT = %q — the two "+
				"copies contradict each other", step[1], dispatchEngineDoc, stepAgent)
		}
	})

	t.Run("ExtractorsAreNotBlind", func(t *testing.T) {
		// Every assertion above is "no defect was found", which a parser that finds nothing
		// satisfies for free. Each fixture is one of the mutants this gate exists to reject.
		noAgent := "Agent(\n  prompt: \"\"\"\nbody\n\"\"\"\n)"
		if _, ok := dispatchAgentRegion(noAgent); ok {
			t.Error("dispatchAgentRegion found a subagent_type argument in a template that has none")
		}
		agentAfterPrompt := "Agent(\n  prompt: \"\"\"\nbody\n\"\"\",\n  subagent_type: \"x\"\n)"
		if _, ok := dispatchAgentRegion(agentAfterPrompt); ok {
			t.Error("dispatchAgentRegion accepted a subagent_type placed after the prompt — a " +
				"copier who stops at the prompt never reads it")
		}
		swapped := `subagent_type: <REQUIRED — "` + stepAgent + `" (REVIEW_AGENT) when is_review(step_id), else "` +
			reviewAgent + `" (STEP_AGENT)>, prompt: """`
		region, ok := dispatchAgentRegion(swapped)
		if !ok {
			t.Fatal("dispatchAgentRegion could not parse the swapped-agent fixture")
		}
		if m := reviewIdRe.FindStringSubmatch(region); m == nil || m[1] != stepAgent {
			t.Errorf("reviewIdRe did not extract the swapped review id; the agreement assertion "+
				"could not have caught a swap (got %v)", m)
		}
		for _, mutant := range []string{
			`subagent_type: "polyforge:step-reviewer",
  model: "opus",
  prompt: """`,
			`subagent_type: "polyforge:step-reviewer", model = RAISED_TIER, prompt: """`,
			`subagent_type: "polyforge:step-reviewer", Model： fill in the tier, prompt: """`,
		} {
			// The third fixture is deliberately NOT expected to match: a fullwidth colon is not
			// an Agent-call argument. Only the first two are live reintroductions.
			got := modelArgRe.MatchString(mutant)
			want := !strings.Contains(mutant, "：")
			if got != want {
				t.Errorf("modelArgRe on %q = %v, want %v — the no-model assertion would misjudge "+
					"this reintroduction shape", mutant, got, want)
			}
		}
		levelKeyed := `subagent_type: <REQUIRED — "polyforge:step-reviewer" (REVIEW_AGENT) when the step's level is deep>, prompt: """`
		if region, ok := dispatchAgentRegion(levelKeyed); ok {
			if strings.Contains(region, "is_review(") || !strings.Contains(strings.ToLower(region), "level") {
				t.Error("the level-keyed fixture was not recognisable as level-keyed — the " +
					"re-keying mutant would pass")
			}
		} else {
			t.Error("dispatchAgentRegion could not parse the level-keyed fixture")
		}
		swappedTable := "| review | predicate | `REVIEW_AGENT` = `" + stepAgent + "` |\n" +
			"| everything else | otherwise | `STEP_AGENT` = `" + reviewAgent + "` |\n"
		if m := tableReviewAgentRe.FindStringSubmatch(swappedTable); m == nil || m[1] != stepAgent {
			t.Errorf("tableReviewAgentRe did not extract a swapped §0f table value (got %v) — "+
				"the table-agreement assertion could not have caught a swap", m)
		}
		noDispatchAgent := strings.Replace(engine,
			"dispatch Agent(subagent_type=REVIEW_AGENT if is_review(step_id) else STEP_AGENT",
			"dispatch Agent(", 1)
		if engineDispatchAgentRe.MatchString(noDispatchAgent) {
			t.Error("engineDispatchAgentRe still matches an engine whose dispatch line lost its " +
				"subagent_type argument")
		}
		remodelled := strings.Replace(engine,
			"dispatch Agent(subagent_type=REVIEW_AGENT if is_review(step_id) else STEP_AGENT",
			"dispatch Agent(model=RAISED if is_review(step_id) else DEFAULT", 1)
		if !regexp.MustCompile(`dispatch Agent\([^)]*\bmodel\s*=`).MatchString(remodelled) {
			t.Error("the engine no-model assertion does not recognise a dispatch line that " +
				"regrew a model argument")
		}
		fmMissing, ok := agentFrontmatter("no frontmatter here")
		if ok || fmMissing != nil {
			t.Error("agentFrontmatter parsed a file with no frontmatter block")
		}
		fm, ok := agentFrontmatter("---\nname: x\nmodel: y\ndisallowedTools: Edit, Write\n---\nbody model: z\n")
		if !ok || fm["name"] != "x" || fm["model"] != "y" || fm["disallowedTools"] != "Edit, Write" {
			t.Errorf("agentFrontmatter misparsed the reference fixture: %v", fm)
		}
		if m := agentModelRe.FindStringSubmatch("---\nmodel: tinker\n---\n"); m == nil || m[1] != "tinker" {
			t.Errorf("agentModelRe did not extract a re-tiered model line (got %v) — "+
				"hooks/pf-skill-router documents this regex as the shape it derives tiers with", m)
		}
	})
}
