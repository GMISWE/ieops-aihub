package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/engine"
	"github.com/GMISWE/ieops-aihub/internal/roles"
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
// (plugins/polyforge/agents/step-*.md) whose `model:` frontmatter the harness itself applies —
// measured on live dispatches: a plugin agent declaring a model ran on that model with no model
// argument passed, read from the dispatch transcript, not from config. The same measurements
// force the second half of the change: an explicit per-invocation `model` argument silently
// OVERRIDES the agent file, so the argument must be DELETED in the same change that ships the
// files — two live channels mean the files are dead text on every dispatch that fills the
// argument.
//
// WHAT WENT WRONG A THIRD TIME (aihub#664)
// -----------------------------------------
// aihub#642 grew the role catalog to five roles (executor, operator, explorer, reviewer,
// designer) but the documented dispatch loop stayed a TWO-way `is_review(step_id)` predicate:
// `dispatch Agent(subagent_type=REVIEW_AGENT if is_review(step_id) else STEP_AGENT)`. That
// predicate has no branch for operator/explorer/designer, so all three fell through to the
// `else` arm — the default, WRITE-CAPABLE executor. For `prepare_context`, whose catalog role
// (`explorer`) is read-only BY CONSTRUCTION (Edit/Write/NotebookEdit disallowed in its agent
// file), that is a silent CAPABILITY WIDENING: `polyforge engine resolve-role` and a session
// following the documented loop disagreed not just on tier, but on whether the dispatched agent
// could write at all. aihub#664 closes it by replacing the two-way predicate with a `ROLE_AGENT`
// dict keyed on the SAME five roles the catalog defines, and by making the loop RESOLVE role via
// `polyforge engine resolve-role` rather than restating any predicate in prose — there is now
// exactly one place, the CLI verb, that decides the role for either dispatch path.
//
// WHAT IS ASSERTED
//  1. Every catalog role's agent file exists, carries `name:` and `model:` frontmatter, and its
//     model is not a review-depth value (the aihub#358 vocabulary).
//  2. engine.native.md declares ROLE_AGENT as a dict covering every catalog role, each entry
//     naming the plugin-namespaced id of that role's own agent file, and its dispatch line
//     resolves `role` from `polyforge engine resolve-role` and selects `subagent_type =
//     ROLE_AGENT[role]` — never a restated is_review predicate.
//  3. The §0b template carries an explicit `subagent_type:` argument ahead of the prompt, marked
//     REQUIRED, selects through `ROLE_AGENT[role]` and names `polyforge engine resolve-role`,
//     does not mention `level`, and contains NO model argument anywhere — re-adding the argument,
//     or re-keying the choice back onto a restated predicate, is what this gate exists to kill.
//  4. §0f's mapping table names every role with the same namespaced id ROLE_AGENT declares. The
//     model names are deliberately NOT asserted (or copied) there: the agent files are the single
//     copy.
//  5. Capability agrees with the catalog for every role, named explicitly for "explorer": a role
//     the catalog marks read_only=true dispatches to an agent file whose `disallowedTools:`
//     frontmatter covers Edit, Write and NotebookEdit. This is the aihub#664 closure itself, not
//     a restatement of role-name equality — a role could resolve to the "right" name and still
//     dispatch to a file with the wrong tool policy.
//  6. Anti-vacuity: every extractor is run against fixtures reproducing the mutants, so "no
//     violation found" cannot mean "nothing was parsed".
//
// Deliberately NOT asserted: any particular model enum (a re-tier edits the agent files and this
// gate follows), and whether a given harness honours the frontmatter — that was aihub#555's live
// probe (measured, Claude Code 2.1.258), a runtime fact no static gate can pin.
//
// aihub#663: THE FILE NAMES ARE NOT HARDCODED HERE
// -------------------------------------------------
// The role -> agent-file naming is DERIVED, not enumerated: the engine document's own dict
// entries are parsed first, resolved against roles.LoadRoles(), and the agent file path is
// computed from the role name by the same rule internal/roles/render_cc.go renders it with. The
// catalog directory is therefore the contract — a renamed role breaks the resolution, and
// TestEngineDispatchIsRootedInTheRoleCatalog below closes the other three directions (a role
// with no agent file, an agent file with no role, and a role the documented dispatch routes
// nowhere).

const (
	dispatchEngineDoc = "skills/pf-execute/engine.native.md"
	dispatchDetailDoc = "skills/pf-execute/references/engine-native-details.md"

	// dispatchAgentDir is where the plugin ships the generated CC agent definitions.
	dispatchAgentDir = "agents"

	// The plugin-name prefix the harness prepends to plugin-shipped agents. Measured
	// (aihub#555): subagent_type for a plugin agent is "<plugin.json name>:<agent name>",
	// and the namespaced id keeps resolving to the plugin file even when a same-named
	// user/project agent exists — only the bare name resolves to the shadow.
	agentIdPrefix = "polyforge:"

	// agentNamePrefix is the "step-" in "step-executor": internal/roles/render_cc.go writes
	// `name: step-<role>` and emits the file as `step-<role>.md`, for EVERY role, and
	// internal/roles' staleness gate byte-diffs the result. So the rule below is a derivation
	// of that renderer, not a second copy of a file list that could go stale against it.
	agentNamePrefix = "step-"
)

// dispatchDeferredRoles names the catalog roles the DOCUMENTED dispatch does not route to, each
// with the reason it does not.
//
// aihub#664 closed the fork this map used to carry: the documented loop dispatched
// `REVIEW_AGENT if is_review(step_id) else STEP_AGENT` — two agents — while `polyforge engine
// resolve-role` resolved a step id against all five catalog roles, so operator/explorer/designer
// steps were routed by NOTHING documented and fell through to the write-capable default
// silently. engine.native.md's ROLE_AGENT dict now covers every catalog role, so
// EveryCatalogRoleIsRoutedOrDeclaredUnrouted below finds nothing left to excuse and this map is
// empty.
//
// It STAYS as a live mechanism rather than being deleted, because the property it protects is
// not "five roles today" but "no role is EVER silently unrouted": a sixth role added to
// internal/roles/definitions/ without a matching ROLE_AGENT entry goes red in that subtest and
// must either be wired into the dict or named here with a reason it stays unrouted. An empty map
// is therefore the PASSING state of the tripwire, not a sign it has nothing left to do.
var dispatchDeferredRoles = map[string]string{}

// dispatchAgentFile is the ONE place this file states the role -> agent-file mapping, and it
// states it as a rule rather than as an enumeration.
func dispatchAgentFile(roleName string) string {
	return dispatchAgentDir + "/" + agentNamePrefix + roleName + ".md"
}

// dispatchRoleOfAgentID turns a documented subagent_type ("polyforge:step-reviewer") into the
// role name it claims to dispatch ("reviewer"). ok is false for an id that is not plugin-
// namespaced or not a step agent at all — both of which are dispatches this gate must reject
// rather than silently read a role name out of.
func dispatchRoleOfAgentID(id string) (string, bool) {
	rest, ok := strings.CutPrefix(id, agentIdPrefix+agentNamePrefix)
	if !ok || rest == "" {
		return "", false
	}
	return rest, true
}

// dispatchCatalogRole resolves a documented agent id against the real catalog. A miss is fatal:
// every assertion downstream compares something against the role this returns, so continuing with
// a zero Role would check a file nobody ships against a capability nobody declared.
func dispatchCatalogRole(t *testing.T, catalog []roles.Role, agentID, constName string) roles.Role {
	t.Helper()
	roleName, ok := dispatchRoleOfAgentID(agentID)
	if !ok {
		t.Fatalf("%s declares %s = %q, which is not a %q-namespaced %q agent id. The bare name "+
			"resolves to a same-named user/project agent when one exists (measured, aihub#555), "+
			"and a non-step id corresponds to no role at all.",
			dispatchEngineDoc, constName, agentID, agentIdPrefix, agentNamePrefix)
	}
	r, ok := roles.RoleByName(catalog, roleName)
	if !ok {
		t.Fatalf("%s declares %s = %q, i.e. the role %q, which internal/roles/definitions/ does "+
			"NOT define (it defines %v). The loop would dispatch an agent generated from no role "+
			"— or, if the file was left behind by a rename, one whose definition no longer exists. "+
			"Rename the entry with the role.",
			dispatchEngineDoc, constName, agentID, roleName, dispatchRoleNames(catalog))
	}
	return r
}

func dispatchRoleNames(catalog []roles.Role) []string {
	out := make([]string, 0, len(catalog))
	for _, r := range catalog {
		out = append(out, r.Name)
	}
	sort.Strings(out)
	return out
}

var (
	// roleAgentDictRe captures the ROLE_AGENT dict literal's body (between the opening `{` and
	// the closing `}`) as the resident loop declares it, e.g.
	//   ROLE_AGENT = {"executor": "polyforge:step-executor", "operator": "polyforge:step-operator",
	//       "explorer": "polyforge:step-explorer", "reviewer": "polyforge:step-reviewer",
	//       "designer": "polyforge:step-designer"}
	roleAgentDictRe = regexp.MustCompile(`(?s)ROLE_AGENT\s*=\s*\{(.*?)\}`)

	// roleAgentEntryRe extracts one "<role>": "<agent-id>" pair out of that body.
	roleAgentEntryRe = regexp.MustCompile(`"([a-z][a-z0-9_]*)"\s*:\s*"([a-z][a-z0-9:._-]*)"`)

	// The resident loop's role-resolution line: role comes from the CLI verb, never a
	// restated predicate.
	engineRoleLineRe = regexp.MustCompile(
		"role\\s*=\\s*`polyforge engine resolve-role --step-id='<step_id>'`\\.role")

	// The resident loop's dispatch line. Asserted verbatim: it is the single line that makes
	// the agent choice a dict lookup keyed on the resolved role, rather than narrative.
	engineDispatchAgentRe = regexp.MustCompile(`dispatch Agent\(subagent_type=ROLE_AGENT\[role\]`)

	// Any way of writing a model argument: "model:" (the Agent-call keyword) or "model="
	// (the pseudo-code form). Case-insensitive and whitespace-tolerant, because the defect
	// only has to be REINTRODUCED in a slightly different hand to escape a literal match.
	modelArgRe = regexp.MustCompile(`(?i)\bmodel\s*[:=]`)

	// The agent files' frontmatter `model:` line. Named agentModelRe because
	// hooks/pf-skill-router documents that it reads the SAME source this gate pins.
	agentModelRe = regexp.MustCompile(`(?m)^model:[ \t]*([a-z][a-z0-9.-]*)[ \t]*$`)

	// §0f's role table — the reader-facing copy of the mapping, one row per role, e.g.
	//   | `explorer` | low, **read-only** | `prepare_context`, `map_consumers`, ... | `polyforge:step-explorer` |
	// Captures the role name and the agent id; the tier/step-id columns are free text.
	tableRoleRowRe = regexp.MustCompile("\\| `([a-z][a-z0-9_]*)` \\| [^|]*\\| [^|]*\\| `([a-z][a-z0-9:._-]*)` \\|")

	// harnessRowRe builds the matcher for ONE harness's row of §0f's harness table (aihub#670),
	// e.g.
	//   | pi | `step-<role>` | `subagent(agent=<id>, task=<§0b>)` |
	// Group 1 is the agent-id cell (backticked, so a row that lost its formatting does not
	// match at all rather than matching loosely), group 2 the dispatch-call cell, which is free
	// text because the "no dispatchable agent" row is prose rather than a call.
	//
	// Built per harness instead of as one generic row regex ON PURPOSE: a generic one would be
	// satisfied by four rows for the SAME harness, which is exactly the copy-paste mistake a
	// fifth harness would arrive as.
	harnessRowRe = func(harness string) *regexp.Regexp {
		return regexp.MustCompile("(?m)^\\|\\s*" + regexp.QuoteMeta(harness) +
			"\\s*\\|\\s*`([^`]*)`\\s*\\|\\s*(.*?)\\s*\\|\\s*$")
	}

	// harnessTableRowRe matches ANY row of that table, capturing the harness cell. It exists for
	// the orphan direction: harnessRowRe can only look for harnesses the Go table already names,
	// so it can never see a row for a harness that was DELETED from the Go table (or never added
	// to it). The harness cell is deliberately un-backticked in the document so this cannot also
	// match the role table above it, whose first cell is `role` in backticks.
	harnessTableRowRe = regexp.MustCompile("(?m)^\\|\\s*([a-z][a-z0-9]*)\\s*\\|\\s*`[^`]*`\\s*\\|\\s*.*?\\s*\\|\\s*$")

	// retiredPredicateTokens is any restatement of the two-way predicate aihub#664 retired.
	// Checked against LIVE pseudocode regions only (the resident loop, the §0b template) —
	// never against prose that explains the retirement, which legitimately names these tokens
	// historically.
	retiredPredicateTokens = []string{"is_review(", "sid.endswith(", "REVIEW_AGENT", "STEP_AGENT"}
)

// parseRoleAgentDict extracts engine.native.md's ROLE_AGENT dict into role -> agent-id pairs. ok
// is false when the dict cannot be found, or is found but yields no entries — either is a parse
// failure the caller must not silently treat as "zero roles declared".
func parseRoleAgentDict(doc string) (map[string]string, bool) {
	body := roleAgentDictRe.FindStringSubmatch(doc)
	if body == nil {
		return nil, false
	}
	entries := roleAgentEntryRe.FindAllStringSubmatch(body[1], -1)
	if len(entries) == 0 {
		return nil, false
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		out[e[1]] = e[2]
	}
	return out, true
}

// agentFrontmatter parses the YAML-ish frontmatter block of an agent definition file into a
// flat key -> value map. ok is false when the file has no leading --- fence pair. Only
// single-line scalar fields are supported, which is all these files may use.
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

// harnessNameRe matches a harness key as a WORD. Not a bare substring: "pi" occurs inside
// "pinned" in engine.native.md's startup paragraph, and a strings.Contains check on a
// two-letter key was measured green against a document that had stopped mentioning pi at all.
func harnessNameRe(harness string) *regexp.Regexp {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(harness) + `\b`)
}

// harnessNamePairedWithID reports whether doc states the harness name and its agent-id form on
// ONE line. Extracted rather than inlined so the anti-vacuity fixtures can feed it the documents
// that must NOT satisfy it — a fixture that re-implements the predicate proves only that the
// fixture agrees with itself, which is the decoration this file rejects elsewhere.
func harnessNamePairedWithID(doc, harness, wantID string) bool {
	re := harnessNameRe(harness)
	for _, ln := range strings.Split(doc, "\n") {
		if re.MatchString(ln) && strings.Contains(ln, wantID) {
			return true
		}
	}
	return false
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
	engineDoc := readEngineDoc(t, pluginRoot, dispatchEngineDoc)
	details := readEngineDoc(t, pluginRoot, dispatchDetailDoc)

	catalog, err := roles.LoadRoles()
	if err != nil {
		t.Fatalf("roles.LoadRoles: %v — the role catalog is the source of truth for which agent "+
			"files exist; without it this gate has nothing to derive the file names from", err)
	}
	if len(catalog) < 2 {
		t.Fatalf("the role catalog holds %d role(s). Every assertion below iterates it, so a "+
			"catalog this small makes them vacuous rather than satisfied.", len(catalog))
	}

	roleAgent, ok := parseRoleAgentDict(engineDoc)
	if !ok {
		t.Fatalf("%s no longer declares the ROLE_AGENT dict (`ROLE_AGENT = {\"<role>\": "+
			"\"<agent-id>\", ...}`). Every assertion below compares the dispatch template and the "+
			"§0f table against it, so without the declaration nothing here checks anything. If "+
			"the mapping moved, move roleAgentDictRe with it.", dispatchEngineDoc)
	}

	t.Run("RoleAgentDictCoversExactlyTheCatalog", func(t *testing.T) {
		want := map[string]bool{}
		for _, r := range catalog {
			want[r.Name] = true
		}
		for name := range want {
			if _, present := roleAgent[name]; !present {
				t.Errorf("ROLE_AGENT in %s has no entry for catalog role %q — a step `polyforge "+
					"engine resolve-role` resolves to this role has nothing in the dict to "+
					"dispatch to", dispatchEngineDoc, name)
			}
		}
		for name := range roleAgent {
			if !want[name] {
				t.Errorf("ROLE_AGENT in %s names role %q, which internal/roles/definitions/ does "+
					"not define (it defines %v) — a stale or invented key", dispatchEngineDoc,
					name, dispatchRoleNames(catalog))
			}
		}
	})

	// Resolve every dict entry against the catalog once; every subtest below shares this.
	type roleFixture struct {
		role        roles.Role
		agentID     string
		agentFile   string
		frontmatter map[string]string
	}
	fixtures := map[string]roleFixture{}
	for name, agentID := range roleAgent {
		r := dispatchCatalogRole(t, catalog, agentID, fmt.Sprintf("ROLE_AGENT[%q]", name))
		agentFile := dispatchAgentFile(r.Name)
		fm, fmOK := agentFrontmatter(readAgentDoc(t, pluginRoot, agentFile))
		if !fmOK {
			t.Fatalf("%s has no frontmatter block, so the harness reads no name, model or tool "+
				"policy from it, and nothing about ROLE_AGENT[%q] can be checked", agentFile, name)
		}
		fixtures[name] = roleFixture{role: r, agentID: agentID, agentFile: agentFile, frontmatter: fm}
	}

	t.Run("EachDictKeyNamesItsOwnResolvedRole", func(t *testing.T) {
		// dispatchCatalogRole resolves the AGENT ID; this checks the DICT KEY it was filed under
		// agrees with it. A swap ("executor" mapped to step-operator's id and vice versa)
		// resolves fine per-entry but disagrees here.
		for name, fx := range fixtures {
			if fx.role.Name != name {
				t.Errorf("ROLE_AGENT[%q] = %q resolves to role %q — the dict key and the role its "+
					"agent id names disagree, which is exactly a swap between two entries",
					name, fx.agentID, fx.role.Name)
			}
		}
	})

	t.Run("EachEntryIsTheAgentFilesOwnNamespacedId", func(t *testing.T) {
		for name, fx := range fixtures {
			if want := agentIdPrefix + fx.frontmatter["name"]; fx.agentID != want {
				t.Errorf("ROLE_AGENT[%q] = %q but %s names itself %q, so the dispatchable id is "+
					"%q — the loop would dispatch an agent that does not exist (or a same-named "+
					"shadow)", name, fx.agentID, fx.agentFile, fx.frontmatter["name"], want)
			}
			if fx.frontmatter["model"] == "" {
				t.Errorf("%s frontmatter has no `model:` — without it the dispatch silently "+
					"inherits the parent session's model, the aihub#544 state this mechanism "+
					"replaces", fx.agentFile)
			} else if scenarioReviewLevels[fx.frontmatter["model"]] {
				t.Errorf("%s declares model %q, a review-depth value (%v) — the mapping has been "+
					"re-keyed onto the vocabulary aihub#358 removed", fx.agentFile,
					fx.frontmatter["model"], keysOf(scenarioReviewLevels))
			}
		}
	})

	t.Run("CapabilityAgreesWithTheCatalog", func(t *testing.T) {
		// This is the aihub#664 closure itself: a role the catalog marks read_only=true must
		// dispatch to an agent file that CANNOT write, by construction (disallowedTools), not by
		// the loop happening to route it somewhere harmless today.
		for name, fx := range fixtures {
			disallowed := fx.frontmatter["disallowedTools"]
			covers := true
			for _, tool := range []string{"Edit", "Write", "NotebookEdit"} {
				if !regexp.MustCompile(`\b` + tool + `\b`).MatchString(disallowed) {
					covers = false
				}
			}
			switch {
			case fx.role.Capability.ReadOnly && !covers:
				t.Errorf("role %q is read_only=true in the catalog, but ROLE_AGENT dispatches it "+
					"to %s whose disallowedTools (%q) does not cover Edit/Write/NotebookEdit — "+
					"dispatching this role would silently WIDEN it to write-capable, the exact "+
					"aihub#664 defect (the pre-fix predicate had no branch for this role, so it "+
					"fell through to the write-capable default)", name, fx.agentFile, disallowed)
			case !fx.role.Capability.ReadOnly && disallowed != "":
				t.Errorf("role %q is read_only=false in the catalog, but %s carries disallowedTools "+
					"%q — it would be NARROWED below what its own definition grants", name,
					fx.agentFile, disallowed)
			}
		}
		// Named explicitly, not just discovered by the loop above: this is the wi's own
		// canonical example of the defect (prepare_context -> explorer).
		explorerFX, present := fixtures["explorer"]
		if !present {
			t.Fatal("no ROLE_AGENT entry for \"explorer\" — the capability-widening closure this " +
				"gate exists to prove cannot be checked at all")
		}
		if !explorerFX.role.Capability.ReadOnly {
			t.Fatal("catalog role \"explorer\" itself is not read_only=true — fixture data has " +
				"drifted from internal/roles/definitions/explorer.yaml")
		}
		for _, tool := range []string{"Edit", "Write", "NotebookEdit"} {
			if !regexp.MustCompile(`\b` + tool + `\b`).MatchString(explorerFX.frontmatter["disallowedTools"]) {
				t.Errorf("explorer's agent file (%s) disallowedTools %q does not cover %s — a "+
					"prepare_context step would dispatch to an agent that CAN write",
					explorerFX.agentFile, explorerFX.frontmatter["disallowedTools"], tool)
			}
		}
	})

	t.Run("LoopResolvesRoleFromTheCLIVerbAndNeverRestatesThePredicate", func(t *testing.T) {
		if !engineRoleLineRe.MatchString(engineDoc) {
			t.Errorf("%s: the auto loop no longer resolves `role` via `polyforge engine "+
				"resolve-role --step-id=...`.role — without this line the loop has nowhere to "+
				"get `role` from, or a hand-written predicate has crept back in and the two "+
				"dispatch paths (this loop / `polyforge engine resolve-role`) can diverge again",
				dispatchEngineDoc)
		}
		if !engineDispatchAgentRe.MatchString(engineDoc) {
			t.Errorf("%s: the auto loop's dispatch line no longer reads `dispatch "+
				"Agent(subagent_type=ROLE_AGENT[role]`. Without it the agent choice is narrative "+
				"again, which is the aihub#544 state.", dispatchEngineDoc)
		}
		if regexp.MustCompile(`dispatch Agent\([^)]*\bmodel\s*=`).MatchString(engineDoc) {
			t.Errorf("%s: the auto loop's dispatch passes a model argument again. An explicit "+
				"per-invocation model silently OVERRIDES the agent file (measured, aihub#555), "+
				"so this single line kills the whole mechanism.", dispatchEngineDoc)
		}
		for _, tok := range retiredPredicateTokens {
			if strings.Contains(engineDoc, tok) {
				t.Errorf("%s still contains %q — the retired two-way is_review predicate must "+
					"not be restated in the resident loop; a B/C session reading it would fork "+
					"from `polyforge engine resolve-role` again, which is the exact aihub#664 "+
					"defect", dispatchEngineDoc, tok)
			}
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
		region, regionOK := dispatchAgentRegion(tmpl)
		if !regionOK {
			t.Fatalf("the §0b template carries no `subagent_type:` argument ahead of `prompt:`. "+
				"The agent id is the only channel that reaches the model files, so a template "+
				"without it dispatches the harness default — model inheritance again. Template:\n%s",
				tmpl)
		}
		if !strings.Contains(region, "REQUIRED") {
			t.Errorf("the template's subagent_type argument is not marked REQUIRED — it reads "+
				"as narrative a dispatcher may skim: %q", region)
		}
		if !strings.Contains(region, "ROLE_AGENT[role]") {
			t.Errorf("the template's subagent_type argument does not reference ROLE_AGENT[role] "+
				"— it must select the agent through the dict the resident loop declares, not "+
				"restate a predicate: %q", region)
		}
		if !strings.Contains(region, "resolve-role") {
			t.Errorf("the template's subagent_type argument does not mention `polyforge engine "+
				"resolve-role` — role must come from the CLI verb, not from prose: %q", region)
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
		for _, tok := range retiredPredicateTokens {
			if strings.Contains(region, tok) {
				t.Errorf("the template's subagent_type argument still contains %q — the retired "+
					"two-way predicate must not be restated here: %q", tok, region)
			}
		}
	})

	t.Run("SectionZeroFTableAgreesWithTheDict", func(t *testing.T) {
		rows := tableRoleRowRe.FindAllStringSubmatch(details, -1)
		if len(rows) == 0 {
			t.Fatalf("%s no longer states the §0f role table (`| `<role>` | ... | `<agent-id>` "+
				"|`). That table is the reader-facing copy of the mapping; if its shape moved, "+
				"move tableRoleRowRe with it.", dispatchDetailDoc)
		}
		tableAgent := map[string]string{}
		for _, row := range rows {
			tableAgent[row[1]] = row[2]
		}
		for name, agentID := range roleAgent {
			got, present := tableAgent[name]
			if !present {
				t.Errorf("§0f's table has no row for role %q, but %s's ROLE_AGENT declares it as "+
					"%q — the two copies contradict each other", name, dispatchEngineDoc, agentID)
				continue
			}
			if got != agentID {
				t.Errorf("§0f's table names %q for role %q but %s's ROLE_AGENT declares %q — the "+
					"two copies contradict each other", got, name, dispatchEngineDoc, agentID)
			}
		}
	})

	// ── The harness table (aihub#670) ──────────────────────────────────────────────────────
	//
	// WHAT WENT WRONG A FOURTH TIME
	// ------------------------------
	// Everything above pins the dispatch as `Agent(subagent_type=ROLE_AGENT[role])` with ids of
	// the form `polyforge:step-<role>`. All three of those spellings — the tool name, the
	// argument name and the namespace — are Claude Code's alone, and engine.native.md ships
	// BYTE-IDENTICAL to pi, codex and opencode: pi's installer copies skills/ with `cp -r`,
	// .codex-plugin/plugin.json points codex at "./skills/" in place, and opencode's installer
	// never copies skills at all. Three of the four harnesses have no installer stage that could
	// rewrite a line, so a gate that pins ONLY the cc shape is not merely incomplete — it
	// actively certifies a cross-harness contract written in one harness's private API.
	//
	// The measured failure is not symmetric, which is why this is a table and not a footnote.
	// Under pi the tool is `subagent` and BOTH arguments are renamed (`agent`/`task`). Under
	// opencode the argument IS `subagent_type`, so the row looks right and fails anyway:
	// `Agent.get("polyforge:step-executor")` throws, and the REQUIRED `description` is missing.
	// Under codex there is no dispatchable polyforge agent at all.
	//
	// So: internal/roles/dispatch.go is the single source of truth (the renderers in that
	// package take each harness's agent name from it), and the two documents must AGREE with it.
	// A renamed agent file therefore cannot leave the documented dispatch naming something
	// nobody ships — which is the failure a markdown-only table would have permitted silently.
	t.Run("EveryHarnessHasADocumentedDispatchRow", func(t *testing.T) {
		table := roles.Dispatches()
		if len(table) < 2 {
			t.Fatalf("roles.Dispatches() returned %d row(s). This subtest iterates it, so a "+
				"collapsed table would assert nothing while passing.", len(table))
		}
		for _, d := range table {
			m := harnessRowRe(d.Harness).FindAllStringSubmatch(details, -1)
			if len(m) != 1 {
				t.Errorf("%s: §0f's harness table has %d row(s) for harness %q, expected exactly "+
					"1. internal/roles/dispatch.go declares it, the renderers in that package "+
					"generate its agent files, and a model running under it reads THIS file to "+
					"learn how to dispatch them — an absent row leaves it with Claude Code's "+
					"call and no way to know that is wrong.", dispatchDetailDoc, len(m), d.Harness)
				continue
			}
			wantID := fmt.Sprintf(d.AgentIDFormat, "<role>")
			if got := m[0][1]; got != wantID {
				t.Errorf("%s: §0f's %q row names agent id %q, but internal/roles/dispatch.go's "+
					"AgentIDFormat renders %q. The renderers in that package build the actual "+
					"file names from the same row, so the documented dispatch is naming an agent "+
					"nobody generates.", dispatchDetailDoc, d.Harness, got, wantID)
			}
			call := m[0][2]
			if d.Call != "" {
				if !strings.Contains(call, d.Call) {
					t.Errorf("%s: §0f's %q row states the dispatch call as %q, but "+
						"internal/roles/dispatch.go declares %q. These are the literal tokens a "+
						"model copies; a paraphrase is a different call.",
						dispatchDetailDoc, d.Harness, call, d.Call)
				}
				continue
			}
			// An empty Call means "this harness cannot dispatch one of these agents in-session".
			// The doc must SAY so — a blank cell reads as an oversight, and a model that reads an
			// oversight falls back to the nearest call it can see, which is cc's.
			if !strings.Contains(strings.ToLower(call), "none") {
				t.Errorf("%s: §0f's %q row has no dispatch call and does not say so (cell: %q). "+
					"internal/roles/dispatch.go records that this harness has no in-session "+
					"dispatch; the document has to state that outright, or a reader treats the "+
					"gap as an omission and copies another row.",
					dispatchDetailDoc, d.Harness, call)
			}
		}

		// ...and the ORPHAN direction, which the loop above cannot see. It asks "is every Go row
		// documented?"; without this, DELETING a row from internal/roles/dispatch.go leaves that
		// harness's markdown row checked by nothing and free to rot into a lie, while the gate
		// goes green because there is no longer a Go row asking after it. This is the same
		// asymmetry TestEngineDispatchIsRootedInTheRoleCatalog closes for roles (a role with no
		// agent file, an agent file with no role); the harness table needs both halves too.
		//
		// Counted by re-finding every row of the table the loop above matched into, keyed on the
		// first cell, so a row for an unknown harness is caught by the COUNT even though no
		// per-harness matcher would ever look for it.
		documented := harnessTableRowRe.FindAllStringSubmatch(details, -1)
		if len(documented) != len(table) {
			var names []string
			for _, row := range documented {
				names = append(names, row[1])
			}
			sort.Strings(names)
			t.Errorf("%s: §0f's harness table has %d row(s) (%v) but internal/roles/dispatch.go "+
				"declares %d (%v). A row the Go table does not declare is documentation nothing "+
				"checks — and a Go row the document dropped is a harness reading somebody else's "+
				"dispatch.", dispatchDetailDoc, len(documented), names, len(table),
				roles.DispatchHarnesses())
		}
	})

	t.Run("TheResidentLoopSaysWhichHarnessItIsShowing", func(t *testing.T) {
		// engine.native.md is the RESIDENT payload — the only text a step body is guaranteed to
		// carry — so the substitution rule has to survive there, not only in the on-demand
		// reference. It cannot carry the whole table (skill_router_payload_test.go's
		// pf-execute/native floor sits a few hundred characters under the harness limit), so
		// what is asserted here is the minimum a non-cc reader needs: that its harness is NAMED,
		// and that the id spelling it must substitute is stated ON THE SAME LINE as the name.
		//
		// WHY LINE-ASSOCIATED, AND WHY A WORD BOUNDARY (this was measured, not reasoned)
		// ------------------------------------------------------------------------------
		// The first version of this subtest asked `strings.Contains(engineDoc, d.Harness)`. A
		// mutant that deleted "(pi)" from the substitution rule — the exact edit someone shaving
		// the payload budget makes — left it GREEN, because "pi" is a substring of "pinned" in
		// the startup paragraph. A two-letter harness key cannot be checked as a bare substring.
		//
		// The word boundary alone is not enough either: it would be satisfied by the name and
		// the id sitting in unrelated paragraphs, which tells a reader nothing about which id is
		// THEIRS. Requiring both on one line is what makes the text answer the only question
		// being asked: "I am on pi, so which id do I use?"
		//
		// DO NOT DELETE THE NAME CHECK AS REDUNDANT. Measured: for pi, codex and opencode it IS
		// subsumed by the pairing check below (deleting the name also breaks the pair). Its only
		// independent catch is "cc", which the pairing check skips because cc's ids are
		// enumerated in the dict rather than given as a format. Disabling the name check and
		// deleting the words "Off cc" from the document escapes with exit 0 — so this is the one
		// assertion standing between the resident loop and never saying whose dispatch it shows.
		for _, d := range roles.Dispatches() {
			nameRe := regexp.MustCompile(`\b` + regexp.QuoteMeta(d.Harness) + `\b`)
			if !nameRe.MatchString(engineDoc) {
				t.Errorf("%s never names the harness %q as a word. A model running under it reads "+
					"this file, finds only Claude Code's dispatch, and has no signal that the "+
					"line is not addressed to it — which is exactly the aihub#670 defect.",
					dispatchEngineDoc, d.Harness)
				continue
			}
			if d.Harness == "cc" {
				continue // cc's ids are spelled out entry by entry in the ROLE_AGENT dict
			}
			wantID := fmt.Sprintf(d.AgentIDFormat, "<role>")
			if !harnessNamePairedWithID(engineDoc, d.Harness, wantID) {
				t.Errorf("%s never states harness %q and its agent id form %q on the SAME line. "+
					"Naming the harness without pairing it to an id is the half-fix: opencode's "+
					"argument is spelled `subagent_type` exactly as cc's is, so a reader who "+
					"substitutes only the tool name still passes an id that harness has never "+
					"heard of.", dispatchEngineDoc, d.Harness, wantID)
			}
		}
	})

	// NOT ASSERTED, and measured rather than assumed: that agentIdPrefix/agentNamePrefix above
	// still agree with internal/roles/dispatch.go's cc row. A subtest doing exactly that was
	// written, and then deleted after its own mutant escaped nothing: breaking cc's
	// AgentIDFormat in dispatch.go goes RED at EveryHarnessHasADocumentedDispatchRow instead,
	// because §0f's cc row states the old spelling and the two stop matching. Breaking the
	// consts here instead stops dispatchRoleOfAgentID cutting the documented ids, which
	// dispatchCatalogRole already fatals on. There is no mutant the extra check catches alone,
	// so it was decoration, and a decorative assertion is worse than none: it reads as
	// protection nobody has. Recorded here so the gap is not "found" and re-added.
	t.Run("ExtractorsAreNotBlind", func(t *testing.T) {
		// Every assertion above is "no defect was found", which a parser that finds nothing
		// satisfies for free. Each fixture below is one of the mutants this gate exists to reject.

		if _, dictOK := parseRoleAgentDict("no dict here at all"); dictOK {
			t.Error("parseRoleAgentDict found a dict in text that has none")
		}
		if _, dictOK := parseRoleAgentDict("ROLE_AGENT = {}"); dictOK {
			t.Error("parseRoleAgentDict accepted an empty dict body as if it had entries")
		}
		got, dictOK := parseRoleAgentDict(`ROLE_AGENT = {"executor": "polyforge:step-executor", "reviewer": "polyforge:step-reviewer"}`)
		if !dictOK || got["executor"] != "polyforge:step-executor" || got["reviewer"] != "polyforge:step-reviewer" {
			t.Errorf("parseRoleAgentDict misparsed a reference two-entry dict: %v, %v", got, dictOK)
		}

		// aihub#670: the mutant that ESCAPED the first draft of
		// TheResidentLoopSaysWhichHarnessItIsShowing, kept because the reason it escaped is not
		// visible from reading the assertion. These fixtures call harnessNamePairedWithID and
		// harnessNameRe, the SAME functions the live check calls — an earlier version of this
		// block asserted properties of its own string literals instead, which left it passing
		// when the live check was weakened from per-line to whole-document, i.e. it was exactly
		// the decoration this file refuses ten lines further down.
		accidental := "`<workspace_root>/.repo/<owner>__<repo>/`, SHA pinned into `.pf_meta.json`"
		if !strings.Contains(accidental, "pi") {
			t.Error("the accidental-substring fixture no longer contains \"pi\", so it cannot " +
				"demonstrate why a bare-substring name check was unsafe")
		}
		if harnessNameRe("pi").MatchString(accidental) {
			t.Errorf("harnessNameRe(\"pi\") fires on %q. That is the carrier the escaped mutant "+
				"rode: if this ever becomes true the name check is satisfiable by prose that "+
				"says nothing about the pi harness.", accidental)
		}
		// Name and id in the same DOCUMENT but on different lines must not satisfy the pairing.
		// This is the fixture that goes red if the live check is relaxed to whole-document.
		if harnessNamePairedWithID(
			"# pi is one of the four harnesses.\n# unrelated\n# the id is `step-<role>`.",
			"pi", "step-<role>") {
			t.Error("harnessNamePairedWithID accepted a name and an id on DIFFERENT lines — a " +
				"reader of that text still cannot tell which id is theirs, which is the whole " +
				"question the rule exists to answer")
		}
		// ...and the positive control, so "nothing paired" cannot mean "the matcher is dead".
		if !harnessNamePairedWithID("# off cc: id `step-<role>` (pi, codex, opencode).",
			"pi", "step-<role>") {
			t.Error("harnessNamePairedWithID rejected a correctly paired line; every pairing " +
				"assertion above would then pass for the wrong reason")
		}

		noAgent := "Agent(\n  prompt: \"\"\"\nbody\n\"\"\"\n)"
		if _, regionOK := dispatchAgentRegion(noAgent); regionOK {
			t.Error("dispatchAgentRegion found a subagent_type argument in a template that has none")
		}
		agentAfterPrompt := "Agent(\n  prompt: \"\"\"\nbody\n\"\"\",\n  subagent_type: \"x\"\n)"
		if _, regionOK := dispatchAgentRegion(agentAfterPrompt); regionOK {
			t.Error("dispatchAgentRegion accepted a subagent_type placed after the prompt — a " +
				"copier who stops at the prompt never reads it")
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
			mgot := modelArgRe.MatchString(mutant)
			want := !strings.Contains(mutant, "：")
			if mgot != want {
				t.Errorf("modelArgRe on %q = %v, want %v — the no-model assertion would misjudge "+
					"this reintroduction shape", mutant, mgot, want)
			}
		}

		levelKeyed := `subagent_type: <REQUIRED - ROLE_AGENT[role] when the step's level is deep>, prompt: """`
		if region, regionOK := dispatchAgentRegion(levelKeyed); regionOK {
			if !strings.Contains(strings.ToLower(region), "level") {
				t.Error("the level-keyed fixture was not recognisable as level-keyed — the " +
					"re-keying mutant would pass")
			}
		} else {
			t.Error("dispatchAgentRegion could not parse the level-keyed fixture")
		}

		swappedTable := "| `executor` | default, write | `code_change` | `polyforge:step-operator` |\n" +
			"| `operator` | lowest, write | `commit_and_pr` | `polyforge:step-executor` |\n"
		swappedRows := tableRoleRowRe.FindAllStringSubmatch(swappedTable, -1)
		if len(swappedRows) != 2 || swappedRows[0][2] != "polyforge:step-operator" || swappedRows[1][2] != "polyforge:step-executor" {
			t.Errorf("tableRoleRowRe did not extract a swapped §0f table row (got %v) — the "+
				"table-agreement assertion could not have caught a swap", swappedRows)
		}

		noRoleLine := strings.Replace(engineDoc,
			"role = `polyforge engine resolve-role --step-id='<step_id>'`.role",
			"role = ROLE_HEURISTIC(step_id)", 1)
		if engineRoleLineRe.MatchString(noRoleLine) {
			t.Error("engineRoleLineRe still matches an engine doc that stopped delegating to " +
				"`polyforge engine resolve-role`")
		}

		noDispatchAgent := strings.Replace(engineDoc,
			"dispatch Agent(subagent_type=ROLE_AGENT[role]", "dispatch Agent(", 1)
		if engineDispatchAgentRe.MatchString(noDispatchAgent) {
			t.Error("engineDispatchAgentRe still matches an engine whose dispatch line lost its " +
				"ROLE_AGENT[role] lookup")
		}

		remodelled := strings.Replace(engineDoc,
			"dispatch Agent(subagent_type=ROLE_AGENT[role]",
			"dispatch Agent(model=RAISED if role == \"reviewer\" else DEFAULT", 1)
		if !regexp.MustCompile(`dispatch Agent\([^)]*\bmodel\s*=`).MatchString(remodelled) {
			t.Error("the engine no-model assertion does not recognise a dispatch line that " +
				"regrew a model argument")
		}

		forkedBack := strings.Replace(engineDoc,
			"dispatch Agent(subagent_type=ROLE_AGENT[role], prompt=§0b)",
			"dispatch Agent(subagent_type=REVIEW_AGENT if is_review(step_id) else STEP_AGENT, prompt=§0b)", 1)
		if forkedBack == engineDoc {
			t.Fatal("the forked-back fixture's anchor text was not found in engine.native.md — " +
				"the fixture no longer matches the live dispatch line's shape")
		}
		foundRetired := false
		for _, tok := range retiredPredicateTokens {
			if strings.Contains(forkedBack, tok) {
				foundRetired = true
			}
		}
		if !foundRetired {
			t.Error("re-forking the dispatch line back onto the retired is_review predicate was " +
				"not detected by any retiredPredicateTokens entry — the anti-fork check would " +
				"miss exactly the aihub#664 regression")
		}

		fmMissing, fmOK := agentFrontmatter("no frontmatter here")
		if fmOK || fmMissing != nil {
			t.Error("agentFrontmatter parsed a file with no frontmatter block")
		}
		fm, fmOK := agentFrontmatter("---\nname: x\nmodel: y\ndisallowedTools: Edit, Write\n---\nbody model: z\n")
		if !fmOK || fm["name"] != "x" || fm["model"] != "y" || fm["disallowedTools"] != "Edit, Write" {
			t.Errorf("agentFrontmatter misparsed the reference fixture: %v", fm)
		}
		if m := agentModelRe.FindStringSubmatch("---\nmodel: tinker\n---\n"); m == nil || m[1] != "tinker" {
			t.Errorf("agentModelRe did not extract a re-tiered model line (got %v) — "+
				"hooks/pf-skill-router documents this regex as the shape it derives tiers with", m)
		}
	})
}

// TestEngineResolveRoleNeverBottomsOutAtExecutor is aihub#654's CODE-LEVEL companion to
// TestEngineNativeDispatchSelectsAgentNotModel above: that test pins the DOC contract (the
// engine-native prose never wires a model argument, and resolves the agent choice through
// `polyforge engine resolve-role` rather than a restated predicate); this one pins the same
// "never silently default to executor" invariant one layer down, in the actual Go implementation
// (internal/engine.ResolveRole) that a future headless orchestrator calls instead of an LLM
// reading the prose. It is additive - it does not replace or alter any assertion above.
// internal/engine/role_test.go's own TestResolveRole_NeverBottomsOutAtExecutor already runs
// against the same real embedded roles.LoadRoles() catalog (via its loadCatalogOrFail helper),
// not a hand-built fixture, so this test is not adding real-catalog coverage the other one
// lacks; the value here is narrower - this test lives in package cli, beside the doc-contract
// test above, so a reader auditing this file sees both the prose contract and its code-level
// analogue together, without also having to open internal/engine/role_test.go.
func TestEngineResolveRoleNeverBottomsOutAtExecutor(t *testing.T) {
	catalog, err := roles.LoadRoles()
	if err != nil {
		t.Fatalf("roles.LoadRoles: %v", err)
	}

	t.Run("catalog-absent, review-shaped step id resolves to reviewer, never executor", func(t *testing.T) {
		// A step id absent from every role's StepIDs list, but review-shaped by name (the
		// "_review" suffix IsReviewStep recognises) — the exact case ResolveRole's tier-3
		// heuristic exists for.
		const stepID = "aihub654_never_in_any_catalog_yaml_review"
		if _, ok := roles.RoleForStepID(catalog, stepID); ok {
			t.Fatalf("fixture step id %q is unexpectedly present in the real catalog — pick a "+
				"different placeholder, or this subtest exercises tier 2, not the tier-3 "+
				"heuristic it claims to", stepID)
		}
		role, source, _, rerr := engine.ResolveRole(catalog, stepID, "")
		if rerr != nil {
			t.Fatalf("ResolveRole(%q): %v", stepID, rerr)
		}
		if role.Name != "reviewer" {
			t.Errorf("ResolveRole(%q) = role %q, want %q — a review-shaped id must never fall "+
				"through to the write-capable executor role", stepID, role.Name, "reviewer")
		}
		if source != engine.RoleSourceHeuristic {
			t.Errorf("ResolveRole(%q) source = %q, want %q", stepID, source, engine.RoleSourceHeuristic)
		}
		if role.Capability.ReadOnly != true {
			t.Errorf("ResolveRole(%q) resolved to role %q with ReadOnly=%v, want true — the "+
				"reviewer role's whole purpose is to be read-only by construction",
				stepID, role.Name, role.Capability.ReadOnly)
		}
	})

	t.Run("explicit declared role beats the catalog's own step-id mapping", func(t *testing.T) {
		// Pick a step id the real catalog DOES map (to some role X), then declare a DIFFERENT
		// known role name for it. Tier 1 (declared) must win over tier 2 (catalog-by-step-id)
		// even though the catalog itself would resolve this step id to something else.
		var mappedStepID, catalogRole string
		for _, r := range catalog {
			if len(r.StepIDs) > 0 {
				mappedStepID, catalogRole = r.StepIDs[0], r.Name
				break
			}
		}
		if mappedStepID == "" {
			t.Fatal("the real catalog has no role with any StepIDs at all — cannot exercise the " +
				"declared-beats-catalog precedence without one")
		}
		var declaredRole string
		for _, r := range catalog {
			if r.Name != catalogRole {
				declaredRole = r.Name
				break
			}
		}
		if declaredRole == "" {
			t.Fatal("the real catalog has fewer than two distinct role names — cannot exercise " +
				"declared-vs-catalog precedence")
		}

		role, source, unknownDeclared, rerr := engine.ResolveRole(catalog, mappedStepID, declaredRole)
		if rerr != nil {
			t.Fatalf("ResolveRole(%q, declared=%q): %v", mappedStepID, declaredRole, rerr)
		}
		if unknownDeclared != "" {
			t.Errorf("ResolveRole(%q, declared=%q) unknownDeclared = %q, want %q (declaredRole was picked from the real catalog's own role names)",
				mappedStepID, declaredRole, unknownDeclared, "")
		}
		if role.Name != declaredRole {
			t.Errorf("ResolveRole(%q, declared=%q) = role %q, want the DECLARED role %q, not the "+
				"catalog's own step-id mapping (%q)", mappedStepID, declaredRole, role.Name,
				declaredRole, catalogRole)
		}
		if source != engine.RoleSourceDeclared {
			t.Errorf("ResolveRole(%q, declared=%q) source = %q, want %q",
				mappedStepID, declaredRole, source, engine.RoleSourceDeclared)
		}
	})
}

// TestEngineDispatchIsRootedInTheRoleCatalog is aihub#663's half: it makes
// internal/roles/definitions/ the CONTRACT for this dispatch rather than a directory the gate
// above happened not to read.
//
// WHAT WENT WRONG
// ---------------
// aihub#642 grew the role catalog from two roles to five and generated the three new CC agent
// files. Nothing in internal/cli noticed, because the gate above named two files as string
// literals. A role could be added, renamed or deleted and every assertion in this package stayed
// green — the definition of a gate that looks like protection and is not.
//
// WHAT IS ASSERTED, and which direction each closes
//  1. role -> file: every catalog role ships an agent file whose `name:`, `model:` and
//     `disallowedTools:` agree with the role's own definition, capability included. Adding a role
//     without generating its file goes red here.
//  2. file -> role: every agents/step-*.md corresponds to a catalog role. DELETING or RENAMING a
//     role goes red here, where a role->file loop alone would simply stop looking at the orphan.
//  3. dict -> role: the documented ROLE_AGENT entries resolve to catalog roles
//     (dispatchCatalogRole, used by the gate above — a rename is fatal there).
//  4. role -> dispatch: every catalog role is either ROUTED by the documented dispatch or named in
//     dispatchDeferredRoles with a reason. This is the one that fires on a SIXTH role: a new role
//     is routed by nothing and excused by nothing. Since aihub#664 the dict routes all five
//     current roles, so dispatchDeferredRoles is empty; this subtest is what keeps it that way.
//  5. catalog -> predicate: the step ids the catalog binds to the review role are exactly the ids
//     engine.IsReviewStep accepts. A review-shaped step id bound to a write-capable role would
//     otherwise make `polyforge engine resolve-role` and the historical is_review predicate
//     dispatch the same step to different agents.
//
// Deliberately NOT asserted: that a role must always be ROUTED rather than declared unrouted via
// dispatchDeferredRoles — whether a future sixth role gets a ROLE_AGENT entry or a documented
// exemption is a design decision this gate has no standing to make; what it removes is the
// possibility of the question going unasked.
func TestEngineDispatchIsRootedInTheRoleCatalog(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	catalog, err := roles.LoadRoles()
	if err != nil {
		t.Fatalf("roles.LoadRoles: %v", err)
	}
	// Anti-vacuity, before anything else: every loop below is "for each role", and an empty
	// catalog satisfies all of them without examining one file.
	if len(catalog) < 2 {
		t.Fatalf("the role catalog holds %d role(s). Every assertion in this test iterates it, so "+
			"a catalog this small makes them vacuous rather than satisfied.", len(catalog))
	}

	t.Run("EveryCatalogRoleShipsADispatchableAgentFile", func(t *testing.T) {
		for _, r := range catalog {
			rel := dispatchAgentFile(r.Name)
			body, readErr := os.ReadFile(filepath.Join(pluginRoot, rel))
			if readErr != nil {
				t.Errorf("role %q is defined in internal/roles/definitions/ but ships no %s (%v). "+
					"`polyforge engine resolve-role` can resolve a step to this role, and the "+
					"harness would then be asked to dispatch an agent id that resolves to nothing. "+
					"Run `go generate ./internal/roles/...` and commit the result.",
					r.Name, rel, readErr)
				continue
			}
			fm, fmOK := agentFrontmatter(string(body))
			if !fmOK {
				t.Errorf("%s has no frontmatter block, so the harness reads no name, no model and "+
					"no tool policy from it", rel)
				continue
			}
			if want := agentNamePrefix + r.Name; fm["name"] != want {
				t.Errorf("%s declares `name: %s`, but the role it is generated from is %q, so the "+
					"dispatchable id is %q and not %q. A file and a role that disagree about the "+
					"name is a dispatch that silently reaches the wrong agent, or none.",
					rel, fm["name"], r.Name, agentIdPrefix+fm["name"], agentIdPrefix+want)
			}
			if fm["model"] == "" {
				t.Errorf("%s carries no `model:` — the dispatch would inherit the parent session's "+
					"model, the aihub#544 state this whole mechanism replaces", rel)
			}
			// The capability is DERIVED from the role's own read_only flag through the same
			// compiler the generator uses, so flipping read_only in the YAML without regenerating
			// goes red here rather than shipping an agent whose tool policy contradicts its
			// definition.
			shape, cerr := roles.CompileCapability(r.Capability.ReadOnly, "cc")
			if cerr != nil {
				t.Errorf("roles.CompileCapability(%v, \"cc\") for role %q: %v", r.Capability.ReadOnly, r.Name, cerr)
				continue
			}
			if fm["disallowedTools"] != shape.CCDisallowedTools {
				t.Errorf("%s declares disallowedTools %q, but role %q declares read_only=%v, which "+
					"compiles to %q. The YAML is the contract; a file that disagrees with it is "+
					"read-only (or write-capable) by accident.",
					rel, fm["disallowedTools"], r.Name, r.Capability.ReadOnly, shape.CCDisallowedTools)
			}
		}
	})

	t.Run("NoAgentFileOutlivesItsRole", func(t *testing.T) {
		entries, readErr := os.ReadDir(filepath.Join(pluginRoot, dispatchAgentDir))
		if readErr != nil {
			t.Fatalf("read %s: %v", dispatchAgentDir, readErr)
		}
		known := map[string]bool{}
		for _, r := range catalog {
			known[agentNamePrefix+r.Name+".md"] = true
		}
		found := 0
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || !strings.HasPrefix(e.Name(), agentNamePrefix) {
				continue
			}
			found++
			if !known[e.Name()] {
				t.Errorf("%s/%s corresponds to no role in internal/roles/definitions/ (which "+
					"defines %v). Either a role was renamed or deleted and its generated file was "+
					"left behind — a dispatchable agent nothing can resolve a step to — or the "+
					"file is hand-written, which the staleness gate forbids.",
					dispatchAgentDir, e.Name(), dispatchRoleNames(catalog))
			}
		}
		if found == 0 {
			t.Fatalf("no %s* file was found under %s at all, so the orphan check above scanned "+
				"nothing and would pass whatever the catalog said", agentNamePrefix, dispatchAgentDir)
		}
	})

	t.Run("EveryCatalogRoleIsRoutedOrDeclaredUnrouted", func(t *testing.T) {
		engineDoc := readEngineDoc(t, pluginRoot, dispatchEngineDoc)
		roleAgent, dictOK := parseRoleAgentDict(engineDoc)
		if !dictOK {
			t.Fatalf("%s no longer declares the ROLE_AGENT dict, so the routed set is empty and "+
				"this check would report every role as unrouted for the wrong reason",
				dispatchEngineDoc)
		}
		routed := map[string]bool{}
		for name, agentID := range roleAgent {
			if roleName, idOK := dispatchRoleOfAgentID(agentID); idOK && roleName == name {
				routed[name] = true
			}
		}
		if len(routed) == 0 {
			t.Fatal("no ROLE_AGENT entry resolved to a role name, so `routed` is empty and the " +
				"loop below reports every role as unrouted — a red that says nothing")
		}

		for _, r := range catalog {
			_, deferred := dispatchDeferredRoles[r.Name]
			switch {
			case routed[r.Name] && deferred:
				t.Errorf("role %q is BOTH routed by the documented dispatch and listed in "+
					"dispatchDeferredRoles. The list is for roles the dispatch does not reach; a "+
					"routed role left in it makes the list stop meaning anything.", r.Name)
			case !routed[r.Name] && !deferred:
				t.Errorf("role %q is defined in internal/roles/definitions/, `polyforge engine "+
					"resolve-role` can resolve a step to it, and the documented dispatch in %s "+
					"routes to neither it nor anything else on its behalf — it routes only "+
					"%v. A step resolving to %q is therefore dispatched to the wrong agent "+
					"SILENTLY: wrong tier, and possibly wrong tool policy.\n\n"+
					"Fix it one of two ways, and both are decisions, not chores: wire ROLE_AGENT "+
					"to this role's agent, or add %q to dispatchDeferredRoles with the reason it "+
					"stays unrouted.",
					r.Name, dispatchEngineDoc, dispatchSortedKeys(routed), r.Name, r.Name)
			}
		}
		for name := range dispatchDeferredRoles {
			if _, roleOK := roles.RoleByName(catalog, name); !roleOK {
				t.Errorf("dispatchDeferredRoles excuses role %q, which the catalog no longer "+
					"defines (it defines %v). A stale entry silently excuses a role that may be "+
					"re-added later under the same name.", name, dispatchRoleNames(catalog))
			}
		}
	})

	t.Run("TheCatalogAndTheReviewPredicateAgreeOnWhichStepsAreReviews", func(t *testing.T) {
		engineDoc := readEngineDoc(t, pluginRoot, dispatchEngineDoc)
		roleAgent, dictOK := parseRoleAgentDict(engineDoc)
		if !dictOK {
			t.Fatalf("%s no longer declares the ROLE_AGENT dict", dispatchEngineDoc)
		}
		reviewAgentID, present := roleAgent["reviewer"]
		if !present {
			t.Fatalf("ROLE_AGENT in %s has no \"reviewer\" entry", dispatchEngineDoc)
		}
		reviewRoleName, idOK := dispatchRoleOfAgentID(reviewAgentID)
		if !idOK {
			t.Fatalf("ROLE_AGENT[\"reviewer\"] = %q does not name a step agent", reviewAgentID)
		}

		// Both counters must move, or the loop below is agreeing with a constant function.
		reviews, nonReviews := 0, 0
		for _, r := range catalog {
			wantReview := r.Name == reviewRoleName
			for _, stepID := range r.SortedStepIDs() {
				if wantReview {
					reviews++
				} else {
					nonReviews++
				}
				if got := engine.IsReviewStep(stepID); got != wantReview {
					t.Errorf("the catalog binds step id %q to role %q, but engine.IsReviewStep(%q) "+
						"= %v. `polyforge engine resolve-role` answers from the CATALOG (tier 2) "+
						"while a historical B/C session followed the documented is_review "+
						"predicate, so the two paths would dispatch this step to DIFFERENT agents "+
						"for the same wi — the read-only reviewer or a write-capable role.",
						stepID, r.Name, stepID, got)
				}
			}
		}
		if reviews == 0 || nonReviews == 0 {
			t.Errorf("the catalog's step ids are all on one side (review=%d, non-review=%d), so "+
				"agreement with engine.IsReviewStep above is agreement with a constant",
				reviews, nonReviews)
		}
	})

	t.Run("ExtractorsAreNotBlind", func(t *testing.T) {
		// dispatchRoleOfAgentID is what turns a documented entry into a catalog lookup; every
		// assertion above about a rename depends on it REJECTING the ids it should.
		for _, bad := range []string{
			"step-executor",              // not plugin-namespaced: resolves to a user/project shadow
			"polyforge:executor",         // not a step agent
			"polyforge:step-",            // namespaced, prefixed, but names no role
			"other-plugin:step-executor", // a different plugin's agent
			"",
		} {
			if name, ok := dispatchRoleOfAgentID(bad); ok {
				t.Errorf("dispatchRoleOfAgentID(%q) = %q, true — this id would be looked up as a "+
					"role and could resolve, hiding exactly the mis-namespacing aihub#555 measured",
					bad, name)
			}
		}
		if name, ok := dispatchRoleOfAgentID(agentIdPrefix + agentNamePrefix + "reviewer"); !ok || name != "reviewer" {
			t.Errorf("dispatchRoleOfAgentID on the shipped review id = %q, %v; want \"reviewer\", "+
				"true — if it cannot read the real id, every catalog lookup above is checking a "+
				"role name it invented", name, ok)
		}
		// ...and the file rule must produce the paths that actually ship.
		if got := dispatchAgentFile("reviewer"); got != "agents/step-reviewer.md" {
			t.Errorf("dispatchAgentFile(\"reviewer\") = %q, want agents/step-reviewer.md — the "+
				"derivation has drifted from internal/roles/render_cc.go's output naming, so the "+
				"role->file direction would report every role as shipping no file", got)
		}
	})
}

func dispatchSortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
