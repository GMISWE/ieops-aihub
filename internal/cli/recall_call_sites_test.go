package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Round-count gate for the wi-scoped Memory-First recall (aihub#287).
//
// WHAT THIS MEASURES — and what it deliberately does not
// -----------------------------------------------------
// A recall costs a ROUND, not bytes: one more assistant turn that issues a tool call and
// waits, priced at the whole request prefix (~211k tok measured) rather than the size of
// the response. Trimming what a recall RETURNS therefore buys ~0.3% of one round; removing
// a CALL SITE buys the whole round. So the quantity worth gating is the number of distinct
// pf_recall call sites the model is handed on a given path.
//
// That number is measurable from the shipped artifact: this file RUNS the real
// hooks/pf-session-start and reads the real SKILL.md files, then counts. It is a
// measurement of the instruction stream, not of model behaviour — whether the model issues
// every call it is handed is a compliance question no unit test can answer. The claim being
// gated is the one that IS mechanical: on the resume path the instruction stream now
// carries ONE wi-scoped recall where it used to carry two.
//
// WHAT WAS REMOVED, AND WHAT THE TRADE IS
// ---------------------------------------
// fragments/bootstrap.md's Session Startup Scan used to end with
//
//	Call pf_recall(work_item_id=<wi_id>, project=<wi.project>, top_k=10)
//
// That fragment is RESIDENT: hooks/pf-session-start injects it into every session, and the
// scan runs the line once per active state file, outside both of the scan's status
// branches. pf-work then issues the same call — same project, same work_item_id, same
// top_k=10, no query and no type filter, so the same wi-scoped text path — immediately
// after a successful claim, in ALL FOUR claim modes.
//
// THIS IS A TRADE, NOT A LOSSLESS DELETION. Say what is given up, in the artifact, or the
// next person inherits a claim the code does not support:
//
//   - The two triggers are different events, and the first is NOT contained in the second.
//     Reaching the scan's line = SessionStart AND at least one active state file. Reaching
//     pf-work's = a successful claim. A session that holds a live state file and then
//     answers a question, runs /pf-status, or runs /pf-stop --pause never claims anything.
//   - The uncovered path INCLUDES the scan's own primary case. bootstrap.md surfaces its
//     "⚠️ …resume?" prompt only when the attempt is still `running`, and
//     fragments/nl-routing.md routes "continue / go on / next" on a running flow to
//     PROCEED WITHIN IT — only "resume <slug>" on a PAUSED wi goes to /pf-work --resume.
//     So the flow the scan exists to produce does not reach a claim, by the workspace's
//     own routing rule.
//   - What is given up on that path: the deterministic wi-scoped fetch, and in particular
//     the `methodology.*` wi artifacts (spec/plan bodies). Those carry no emb_vector
//     (internal/domain/memory_vector.go requires `emb_vector IS NOT NULL`), so the
//     project-scoped Memory-First recall cannot reach them unless the type is named.
//     Ordinary wi-linked experience.*/rule.* rows are NOT lost by construction — no recall
//     path excludes wi-linked rows from a project-scoped query (see isWIScoped below).
//
// The trade is deliberate: one MANDATORY round in every session that has a state file,
// against wi-scoped memories on a narrow continuation path where they can still be
// recalled on demand. Rounds are priced at the whole prefix; the recall is not.
//
// Note also that "nothing displayed it" is NOT one of the grounds. The deleted line said
// "to surface wi-linked memories", a display template for recall results is two fragments
// away in memory-first.md, and a recall conditions the model's next action whether or not
// a format is prescribed. Absence of a rendering step is not absence of effect.
//
// The COVERAGE half below is the load-bearing half. A call-site deletion is only safe if
// the paths it claims are still covered really are, so this file asserts the pf-work side
// is intact in all four modes rather than assuming it.
//
// NOTE ON SCOPE — two recalls this gate deliberately does NOT touch.
//
//   - fragments/memory-first.md's project-scoped recall is NOT redundant with pf-spec /
//     pf-plan Step 1, whose type filter is a superset of it. The FILTER is a superset; the
//     PATHS are not. memory-first is resident and fires in every session, while
//     pf-spec/pf-plan Step 1 fire only when those two skills are invoked — a session that
//     answers a question, runs /pf-status, or dispatches /pf-execute reaches neither.
//     Assertion 1 below pins that call site in place.
//   - _common/memory.md:16's recall (router-injected for pf-execute) is not redundant
//     either, and the reason is NOT that skill_recall_type_test.go asserts on it — that
//     assertion is a vacuity guard for aihub#289's piped-type check, not a product rule.
//     The real reason: on the rhs=false path, /pf-execute is dispatched as a SUBAGENT
//     (fragments/post-claim-routing.md), and a subagent receives no SessionStart injection
//     (bootstrap.md says so itself). So for that subagent _common/memory.md is the ONLY
//     project-scoped recall it ever sees — deleting it repeats exactly the mistake the
//     first bullet refuses.

// A call is wi-scoped when it passes work_item_id. That parameter routes the request to
// the deterministic wi-level text path and appends `AND work_item_id = $n` to the WHERE
// clause (internal/domain/memory.go).
//
// It does NOT make the two kinds of recall return disjoint sets, and an earlier version of
// this comment claimed it did. There is no `work_item_id IS NULL` predicate anywhere on
// the recall paths: a project-scoped query can and does return wi-linked rows. The only
// disjointness in the code is between the two halves of ONE hybrid call — the complement
// half is restricted by nonEmbeddableTypeClause to rows the vector half structurally
// cannot return (aihub#270) — which is a different thing entirely.
//
// So the reason the wi-scoped pair is the deduplicable one is not disjointness. It is that
// both members of THAT pair pass identical arguments, and the wi-level text path has no
// semantic query, so what they return is a function of the stored rows rather than of an
// embedding. (Membership can still drift between session start and claim: both paths apply
// the query-time Ebbinghaus decay filter at MinStrength and an `expires_at >
// clock_timestamp()` predicate. Ordering is over stored columns only, so it is stable.)
func isWIScoped(call string) bool { return strings.Contains(call, "work_item_id") }

// recallCalls finds pf_recall call sites as they appear in a template. It reuses
// pfRecallCallRe from skill_recall_type_test.go — same package, same pattern, and two
// copies of a regexp are two things to keep in step. `[^)]*` spans newlines (a negated
// class matches \n in Go), which is required: pf-work writes the call across five lines.
func recallCalls(s string) []string { return pfRecallCallRe.FindAllString(s, -1) }

func wiScopedCalls(s string) []string {
	var out []string
	for _, c := range recallCalls(s) {
		if isWIScoped(c) {
			out = append(out, c)
		}
	}
	return out
}

// renderSessionStart runs hooks/pf-session-start the way the harness does and returns the
// assembled `additionalContext` — the resident payload every session receives.
//
// The hook emits additionalContext TWICE (top level for Copilot CLI, inside
// hookSpecificOutput for Claude Code), byte-identical. Summing the two is a 2x phantom, so
// this asserts they are equal and returns ONE of them.
func renderSessionStart(t *testing.T, pluginRoot string) string {
	t.Helper()

	tmp := t.TempDir()
	ws := filepath.Join(tmp, "ws")
	home := filepath.Join(tmp, "home", ".claude")
	for _, d := range []string{ws, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(ws, ".polyforge.yaml"), []byte("version: 1\n"), 0o644); err != nil {
		t.Fatalf("write .polyforge.yaml: %v", err)
	}
	// Worst case: every condition the manifest can gate on turned ON, matching
	// tests/using-polyforge-payload.test.sh. A bare $HOME would measure a payload no real
	// user receives.
	if err := os.WriteFile(filepath.Join(home, "settings.json"),
		[]byte(`{"enabledPlugins":{"superpowers@fixture":true}}`), 0o644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}

	cmd := exec.Command("bash", filepath.Join(pluginRoot, "hooks", "pf-session-start"))
	cmd.Env = append(os.Environ(),
		"HOME="+filepath.Dir(home),
		"CLAUDE_PROJECT_DIR="+ws,
	)
	// The hook emits a different JSON shape when these are set.
	cmd.Env = filterEnv(cmd.Env, "CURSOR_PLUGIN_ROOT", "PLUGIN_ROOT")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("pf-session-start failed: %v (stderr: %s)", err, stderr.String())
	}
	// The hook is a no-op (exit 0, no output) without python3 or outside a workspace. That
	// is right for a session and fatal for a gate: an empty payload would make every count
	// below zero and every assertion vacuously "improved".
	if len(strings.TrimSpace(string(stdout))) == 0 {
		t.Fatalf("pf-session-start emitted nothing. It is fail-silent (no python3? no "+
			"workspace?), so this gate cannot tell a slimmed payload from no payload at "+
			"all (stderr: %s)", stderr.String())
	}

	var out struct {
		AdditionalContext  string `json:"additionalContext"`
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(stdout, &out); err != nil {
		t.Fatalf("pf-session-start output is not the expected JSON: %v\n%s", err, stdout)
	}
	if out.AdditionalContext != out.HookSpecificOutput.AdditionalContext {
		t.Fatalf("the hook's two additionalContext occurrences differ; one of the two " +
			"harnesses is being served a different payload, and any size or call-site " +
			"number taken from one of them describes only half the users")
	}
	if out.AdditionalContext == "" {
		t.Fatal("pf-session-start produced an empty additionalContext")
	}
	return out.AdditionalContext
}

func filterEnv(env []string, drop ...string) []string {
	out := env[:0:0]
	for _, kv := range env {
		keep := true
		for _, d := range drop {
			if strings.HasPrefix(kv, d+"=") {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, kv)
		}
	}
	return out
}

func readSkill(t *testing.T, pluginRoot, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(pluginRoot, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// pfWorkModes splits pf-work/SKILL.md on its `### Mode X — …` headings so each claim mode
// can be checked independently. Splitting rather than counting the whole file is what makes
// the coverage assertion real: four calls in the file could be four calls in one mode.
func pfWorkModes(t *testing.T, pluginRoot string) map[string]string {
	t.Helper()
	src := readSkill(t, pluginRoot, filepath.Join("skills", "pf-work", "SKILL.md"))
	headRe := regexp.MustCompile(`(?m)^### Mode ([A-D]) `)
	locs := headRe.FindAllStringSubmatchIndex(src, -1)
	if len(locs) != 4 {
		t.Fatalf("expected 4 `### Mode X` sections in pf-work/SKILL.md, found %d — the "+
			"per-mode coverage assertion below cannot be trusted against a file it can no "+
			"longer parse", len(locs))
	}
	out := map[string]string{}
	for i, l := range locs {
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		out[src[l[2]:l[3]]] = src[l[0]:end]
	}
	return out
}

// ─── 1. the resident payload carries exactly ONE recall, and it is the project-scoped one ──

func TestResidentPayload_HasOneProjectScopedRecall(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	ctx := renderSessionStart(t, pluginRoot)

	calls := recallCalls(ctx)
	t.Logf("resident payload: %d chars, %d pf_recall call site(s)", len([]rune(ctx)), len(calls))
	for _, c := range calls {
		t.Logf("    %s", strings.Join(strings.Fields(c), " "))
	}

	// Order matters. The memory-first pin runs FIRST and unconditionally: it is the
	// assertion about what must NOT be deleted, and behind an early t.Fatalf on the count
	// it would go unevaluated in exactly the run that deleted it (deleting memory-first
	// makes the count 0, which trips the count check, which would exit before the pin).
	// Two independent failures should report as two, not shadow one another.
	//
	// memory-first.md is resident and fires in EVERY session; pf-spec/pf-plan Step 1 fire
	// only when those skills are invoked, so they are NOT a superset of it in path terms
	// and cannot absorb it (aihub#287).
	if !strings.Contains(ctx, "Memory-First Principle") {
		t.Error("fragments/memory-first.md is no longer in the resident payload — the " +
			"project-scoped recall it owns reaches no session at all now, and the paths " +
			"that used to have it (a plain question, /pf-status, /pf-work, /pf-execute) " +
			"reach no other one")
	}

	if len(calls) != 1 {
		t.Errorf("the resident SessionStart payload carries %d pf_recall call sites, want 1. "+
			"Every one of them is a round charged at the full request prefix in EVERY "+
			"session, whether or not the session ever uses the result. Calls: %v", len(calls), calls)
		return
	}
	if isWIScoped(calls[0]) {
		t.Errorf("the one resident recall is wi-scoped (%s). The wi-scoped recall belongs "+
			"to pf-work, which issues it after the claim; the resident one must be the "+
			"project-scoped Memory-First recall from fragments/memory-first.md", calls[0])
	}
}

// ─── 2. COVERAGE: the wi-scoped recall still fires on every claim path ────────────────────

func TestPfWork_KeepsWIScopedRecallInEveryClaimMode(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	for mode, body := range pfWorkModes(t, pluginRoot) {
		calls := wiScopedCalls(body)
		if len(calls) == 0 {
			t.Errorf("pf-work Mode %s issues no wi-scoped pf_recall. The Session Startup "+
				"Scan no longer prefetches wi-linked memories (aihub#287), and the claim "+
				"paths are the coverage that deletion was traded against — pf-work "+
				"re-issuing the same call in ALL FOUR claim modes. Remove it from a mode "+
				"and that path loses the memories with nothing to say so", mode)
			continue
		}
		for _, c := range calls {
			if !strings.Contains(c, "top_k=10") {
				t.Errorf("pf-work Mode %s recalls at a different top_k than the scan used "+
					"to (top_k=10), so it is not the same request:\n    %s",
					mode, strings.Join(strings.Fields(c), " "))
			}
		}
		t.Logf("Mode %s: %d wi-scoped recall(s)", mode, len(calls))
	}
}

// ─── 3. the round count on the resume path, MEASURED ─────────────────────────────────────

// wiScopedRoundsOnResumePath counts the wi-scoped recall call sites the model is handed on
// the path "session starts with a state file -> /pf-work <slug> --resume": the resident
// payload plus pf-work's Mode C body. Before aihub#287 this was 2 (bootstrap.md's scan step
// and Mode C's post-claim recall, same arguments in a different order); it is now 1.
//
// This is the COVERED path — the paused wi that the user explicitly resumes. It is not the
// only path that reached the deleted line; see the trade at the top of this file for the
// one that is no longer covered (a live state file whose attempt is still `running`, where
// nl-routing sends "continue" back into the flow rather than through a claim).
func wiScopedRoundsOnResumePath(t *testing.T, pluginRoot string) int {
	t.Helper()
	resident := wiScopedCalls(renderSessionStart(t, pluginRoot))
	modeC := wiScopedCalls(pfWorkModes(t, pluginRoot)["C"])
	t.Logf("resume path: %d resident + %d Mode C = %d wi-scoped recall call site(s)",
		len(resident), len(modeC), len(resident)+len(modeC))
	return len(resident) + len(modeC)
}

func TestResumePath_IssuesOneWIScopedRecall(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	if got := wiScopedRoundsOnResumePath(t, pluginRoot); got != 1 {
		t.Errorf("the resume path hands the model %d wi-scoped pf_recall call sites, want 1. "+
			"Each is a separate round (they sit either side of the claim, so they cannot "+
			"batch into one tool block), and they carry the same project / work_item_id / "+
			"top_k=10 with no query — the same request, issued twice. (Membership can drift "+
			"between the two: both apply the query-time decay filter at MinStrength and an "+
			"expires_at predicate. That is a reason the results may differ slightly, not a "+
			"reason to pay for the round.)", got)
	}
}

// ─── 4. NEGATIVE CONTROL: the measurement must fail on the pre-change build ───────────────

// TestResumePath_MeasurementFailsOnPreChangeBuild reconstructs the tree as it shipped
// BEFORE aihub#287 — the deleted line put back into fragments/bootstrap.md — re-renders the
// real hook against it, and requires the measurement to come back 2.
//
// Without this, every assertion above could be passing because the counter is broken, the
// regexp matches nothing, or the render silently returned less than it should. A probe that
// has never been shown to go red is not evidence.
func TestResumePath_MeasurementFailsOnPreChangeBuild(t *testing.T) {
	pluginRoot := pluginRootDir(t)

	old := t.TempDir()
	copyTree(t, pluginRoot, old)

	frag := filepath.Join(old, "skills", "using-polyforge", "fragments", "bootstrap.md")
	b, err := os.ReadFile(frag)
	if err != nil {
		t.Fatalf("read fixture fragment: %v", err)
	}
	// The exact text aihub#287 deleted, restored in place.
	const anchor = "6. Call `pf_get_ready_queue(project)` to show the current ready queue summary."
	const restored = "6. Call `pf_recall(work_item_id=<wi_id>, project=<wi.project>, top_k=10)` to surface wi-linked memories.\n" +
		"7. Call `pf_get_ready_queue(project)` to show the current ready queue summary."
	if !strings.Contains(string(b), anchor) {
		t.Fatalf("FIXTURE, NOT CODE: fragments/bootstrap.md no longer contains the anchor "+
			"line this control rebuilds the old build from:\n    %s\n"+
			"Update the anchor, or the control silently stops reconstructing anything", anchor)
	}
	if err := os.WriteFile(frag,
		[]byte(strings.Replace(string(b), anchor, restored, 1)), 0o644); err != nil {
		t.Fatalf("write fixture fragment: %v", err)
	}

	got := wiScopedRoundsOnResumePath(t, old)
	if got != 2 {
		t.Fatalf("the pre-change build measures %d wi-scoped recall call sites on the "+
			"resume path, expected 2. The measurement cannot distinguish the old build "+
			"from the new one, so TestResumePath_IssuesOneWIScopedRecall proves nothing "+
			"about what this change removed", got)
	}

	// ...and the same fixture must still satisfy COVERAGE, so the two halves are shown to
	// be independent: the old build was not failing assertion 2, it was failing only the
	// round count. Otherwise "the old build is red" could be true for the wrong reason.
	for mode, body := range pfWorkModes(t, old) {
		if len(wiScopedCalls(body)) == 0 {
			t.Errorf("pre-change fixture: Mode %s has no wi-scoped recall, so the fixture "+
				"is not the pre-change build", mode)
		}
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("copy plugin tree: %v", err)
	}
}
