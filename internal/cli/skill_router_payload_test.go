package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Payload budget gate for hooks/pf-skill-router's injected additionalContext (aihub#304).
//
// WHAT WENT WRONG
// ---------------
// The router is a PreToolUse(Skill) hook. pf-execute's SKILL.md is a stub, so the text this
// hook injects IS the step body. Claude Code replaces any single hook output longer than
// 10,000 CHARACTERS with a `<persisted-output>` wrapper: the full text goes to a file on disk
// and only a ~2,000-character preview reaches the model. The hook still exits 0.
//
// aihub#285 measured that limit on the SessionStart hook. Nothing proved it applied here —
// so aihub#304 measured it, in a real session, against the unmodified shipped hook: invoking
// Skill(polyforge:pf-execute) on a 14,482-character payload produced the persisted-output
// wrapper and delivered exactly 1,976 characters. 1,976 is what the preview rule ported below
// as tle() predicts for that payload, to the character — same code path, same constants.
// 13.6% of the step body reached the model; 86.4% did not.
//
// Two facts that make this gate look different from the SessionStart one:
//
//  1. THE PAYLOAD IS ASSEMBLED PER INVOKED SKILL, not from one fixed manifest. So the gate
//     enumerates routed skills from the hook's own TARGETS dict (routedSkills, shared with
//     skill_recall_type_test.go) and demands a budget entry for each. Adding a skill to
//     TARGETS without adding its entry here fails — it cannot escape the gate by being new.
//
//  2. THE PAYLOAD DEPENDS ON THE ENGINE BRANCH. With superpowers enabled the router injects a
//     short pointer; without it, engine.native.md. The native branch is ~2,400 characters
//     larger and is the binding constraint, so BOTH branches are measured. A gate that only
//     saw the developer's own machine would measure whichever branch that machine happens to
//     select and call the other one covered.
//
// WHAT IS ASSERTED, per (skill, branch)
//   1. Size inside [gate-slack, gate] — BOTH bounds, and gate < the harness limit.
//   2. The runtime degrade path is DORMANT: no banner in the payload, nothing on stderr.
//   3. No @@…@@ placeholder survives, and every on-demand pointer resolves to a real file.
//   4. Tiering is real: the deferred files' distinctive content is NOT also in the payload.
//   5. Controls — (a) +N characters to a resident fragment must trip the size gate, and the N
//      characters must be shown to survive into the measurement; (b) a genuinely over-budget
//      tree must degrade loudly: exit 0, still emit, banner present, stderr names what it
//      dropped, and everything it names is really gone.

const (
	// MEASURED on Claude Code 2.1.246, not inferred. See the header comment: a 14,482-char
	// PreToolUse additionalContext was replaced by a preview in a real session, and the
	// cutoff matched tle() exactly.
	routerHarnessHardLimit = 10000
	routerPreviewChars     = 2000

	// Slack is the working margin the ratchet allows before it demands a re-baseline.
	routerGateSlack = 150

	// Control 5a appends this many characters to a resident fragment. It must exceed
	// routerGateSlack, or "the gate rejected it" would prove nothing about the gate's
	// setting — only that the number was large.
	routerProbeChars = 400

	// The router substitutes @@PLUGIN_ROOT@@ with the ABSOLUTE plugin path, so the raw payload
	// length depends on WHERE the plugin is installed. Budgets are therefore recorded against
	// the path folded back to this token, and the hard-limit check adds the worst case back.
	// Without that split, renaming a directory would re-baseline every number in this file and
	// a deep install path could cross 10,000 with the gate still green.
	routerRootToken = "@@PLUGIN_ROOT@@"

	// The longest plugin root the budgets are guaranteed for. Real installs are ~60 chars
	// (`~/.claude/plugins/cache/<marketplace>/polyforge/<version>`) and a repo checkout inside
	// a polyforge worktree is ~62; 140 is well past both.
	routerAssumedRootLen = 140
)

// routerBudget is the ratchet, in CHARACTERS, keyed "<skill>/<branch>".
//
// This is a RATCHET THAT TRACKS THE PAYLOAD, not a fixed ceiling, and BOTH bounds are
// asserted. The lower bound exists because a one-sided gate rots downward in value: aihub#304
// slimmed the native branch from 16,840 to 9,261, and if the gate stayed near 10,000 that
// slimming would simply have donated ~700 unguarded characters to whoever grew the payload
// next. The headroom a slimming buys must not become the cushion for the next silent growth.
//
// If you SHRINK a payload this test goes red and prints the number to write here. The floor
// sits exactly on the last measurement, so even a one-character shrink — a typo fix in a
// fragment — asks for a one-line edit to this map. That is deliberate: it is the price of the
// gate tracking the payload instead of drifting above it, and the failure message carries the
// replacement number, so the edit is mechanical.
// If you GROW one past its gate, do NOT raise the number — move text to the on-demand tier
// (skills/**/references/, reached by a `📄 Read …` pointer in the resident fragment).
//
// ⚠️ `native` is the binding branch and its worst-case headroom is ~500 characters, not the
// ~3,000 the superpowers branch enjoys. Adding a third `📄` pointer costs 125 of that up front
// (see routerAssumedRootLen). If native needs to grow, the growth has to come out of
// engine.native.md or lifecycle.md, not out of this number.
var routerBudget = map[string]int{
	"pf-execute/superpowers": 6803 + routerGateSlack,
	"pf-execute/native":      9256 + routerGateSlack,
}

// routerBranches are the engine branches the router can select. The gate measures every one:
// a real user gets exactly one of them, and which one is not the test runner's choice.
var routerBranches = []struct {
	name        string
	superpowers bool
}{
	{"superpowers", true},
	{"native", false},
}

// onDemandFiles are the deferred fragments. Each must exist on disk, be pointed at from the
// payload, and NOT have its body inlined into the payload — that is what "deferred" means.
// marker is a string distinctive to that file; it anchors the "not inlined" check so the
// check cannot pass merely because the marker was a typo.
var onDemandFiles = []struct {
	rel    string
	marker string
	branch string // "" = both branches
}{
	{
		rel:    "skills/_common/references/lifecycle-details.md",
		marker: "Never pass `next_step` to a tool that does not publish it",
	},
	{
		rel:    "skills/pf-execute/references/engine-native-details.md",
		marker: "Execute (rhs=true, interactive mode) — the loop in full",
		branch: "native",
	},
}

// tle is a faithful port of the harness function that builds the persisted-output preview:
// take the first routerPreviewChars characters, find the last newline in that slice, and cut
// there if its index is past half the window; otherwise use the raw slice.
func tle(s string) string {
	r := []rune(s)
	if len(r) <= routerPreviewChars {
		return s
	}
	window := string(r[:routerPreviewChars])
	i := strings.LastIndex(window, "\n")
	if i > routerPreviewChars/2 {
		return window[:i]
	}
	return window
}

func charLen(s string) int { return len([]rune(s)) }

// routerFixtureHome builds a hermetic HOME + workspace whose settings force the superpowers
// condition on or off, and returns (home, cwd). Both layers are written because the hook
// consults HOME and the workspace, and a test that set only one would be measuring whichever
// the precedence rules happened to pick.
func routerFixtureHome(t *testing.T, superpowers bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	ws := filepath.Join(dir, "ws")
	for _, d := range []string{filepath.Join(home, ".claude"), filepath.Join(ws, ".claude")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(ws, ".polyforge.yaml"), []byte("version: 1\n"), 0o644); err != nil {
		t.Fatalf("write .polyforge.yaml: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"enabledPlugins": map[string]any{"superpowers@fixture": superpowers},
	})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	for _, p := range []string{
		filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(ws, ".claude", "settings.json"),
	} {
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	return home, ws
}

type routerRender struct {
	ctx    string
	stderr string
	// assembledLen is the size of the FULL assembly in real characters, at the plugin root it
	// was rendered from. It equals charLen(ctx) normally, but when the hook degrades an
	// over-budget payload the delivered text is under the limit BY CONSTRUCTION — measuring
	// that would make the size gate a tautology, the exact defect this suite exists to
	// prevent. So in that case it comes off the hook's own stderr.
	assembledLen int
	degraded     bool

	// normLen is assembledLen with the absolute plugin root folded back to routerRootToken:
	// the path-independent number the budgets are recorded against.
	normLen int
	// worstLen is normLen with every pointer's root expanded to routerAssumedRootLen — what a
	// user gets at the deepest install path the budget covers. This, not normLen, is what has
	// to clear the harness limit.
	worstLen int
	// pointers is how many times the absolute plugin root appears in the payload.
	pointers int
}

var routerAssembledRe = regexp.MustCompile(`payload is (\d+) chars`)

// renderRouter drives the shipped hook exactly as the harness does, with the engine branch
// pinned by fixture settings rather than inherited from whoever is running the test.
func renderRouter(t *testing.T, pluginRoot, skill string, superpowers bool) routerRender {
	t.Helper()
	home, ws := routerFixtureHome(t, superpowers)
	payload := fmt.Sprintf(
		`{"tool_name":"Skill","tool_input":{"skill":"polyforge:%s"},"cwd":%q}`, skill, ws)

	cmd := exec.Command("bash", filepath.Join(pluginRoot, "hooks", "pf-skill-router"))
	cmd.Stdin = strings.NewReader(payload)
	// A minimal, scrubbed environment: inheriting os.Environ() would let the developer's own
	// CLAUDE_PLUGIN_ROOT or HOME decide what is measured.
	cmd.Env = []string{
		"HOME=" + home,
		"PATH=" + os.Getenv("PATH"),
		"CLAUDE_PLUGIN_ROOT=" + mustAbs(t, pluginRoot),
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("hook failed for %s (superpowers=%v): %v (stderr: %s)",
			skill, superpowers, err, stderr.String())
	}
	// The hook is FAIL-SILENT by design. That is right for production and fatal for a gate:
	// an empty render makes every assertion below vacuously true.
	if len(strings.TrimSpace(string(stdout))) == 0 {
		t.Fatalf("hook emitted nothing for %s (superpowers=%v) — it is fail-silent, so this "+
			"gate cannot tell a clean render from no render (stderr: %s)",
			skill, superpowers, stderr.String())
	}

	var out struct {
		AdditionalContext  string `json:"additionalContext"`
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(stdout, &out); err != nil {
		t.Fatalf("hook output for %s is not the expected JSON: %v", skill, err)
	}
	ctx := out.HookSpecificOutput.AdditionalContext
	if ctx == "" {
		t.Fatalf("no additionalContext for %s (superpowers=%v)", skill, superpowers)
	}
	// The two copies serve two harnesses (Claude Code reads the nested one, Copilot CLI the
	// top-level one). They must stay byte-identical: aihub#304 records that summing them
	// yields a phantom 2x measurement, and a drift between them would ship two different
	// step bodies to two runtimes.
	if out.AdditionalContext != ctx {
		t.Errorf("%s/%v: top-level and hookSpecificOutput additionalContext differ (%d vs %d "+
			"chars) — the two harnesses would receive different step bodies",
			skill, superpowers, charLen(out.AdditionalContext), charLen(ctx))
	}

	absRoot := mustAbs(t, pluginRoot)
	r := routerRender{ctx: ctx, stderr: stderr.String(), assembledLen: charLen(ctx)}
	r.pointers = strings.Count(ctx, absRoot)
	r.normLen = charLen(strings.ReplaceAll(ctx, absRoot, routerRootToken))
	r.worstLen = r.normLen + r.pointers*(routerAssumedRootLen-charLen(routerRootToken))
	if m := routerAssembledRe.FindStringSubmatch(r.stderr); m != nil {
		n, _ := strconv.Atoi(m[1])
		r.assembledLen, r.degraded = n, true
	}
	return r
}

const (
	routerBannerMark = "POLYFORGE SKILL-ROUTER PAYLOAD OVER BUDGET"
	// The sentence in the router's header that has to survive truncation.
	routerBudgetNotice = "Fragments marked 📄 are NOT injected"
)

// TestRouterPreviewWindowCheckDiscriminates is the control for the ordering assertion above.
// Without it, "the notice is in the window" would pass just as happily on a payload short
// enough that tle() returns everything, or on a broken tle() that never truncates.
func TestRouterPreviewWindowCheckDiscriminates(t *testing.T) {
	head := "[polyforge router] " + routerBudgetNotice + " — rest of the header.\n"
	filler := strings.Repeat("padding line to push past the preview window\n", 200)

	if !strings.Contains(tle(head+filler), routerBudgetNotice) {
		t.Error("notice absent from the window when it leads the payload — the check would " +
			"false-negative on a correct build")
	}
	if strings.Contains(tle(filler+head), routerBudgetNotice) {
		t.Errorf("notice still inside the window with %d chars of filler ahead of it — the "+
			"window check has no discriminating power and the assertion above proves nothing",
			charLen(filler))
	}
	// ...and tle must actually be truncating, not returning its input.
	if got := charLen(tle(filler)); got > routerPreviewChars {
		t.Errorf("tle returned %d chars for a %d-char input; it is not applying the %d-char "+
			"window at all", got, charLen(filler), routerPreviewChars)
	}
}

func TestRoutedSkillHook_PayloadFitsHarnessLimit(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	skills := routedSkills(t, pluginRoot)

	seen := map[string]bool{}
	for _, skill := range skills {
		for _, br := range routerBranches {
			key := skill + "/" + br.name
			seen[key] = true
			t.Run(key, func(t *testing.T) {
				r := renderRouter(t, pluginRoot, skill, br.superpowers)
				gate, ok := routerBudget[key]
				if !ok {
					t.Fatalf("no budget entry for %q. A skill was added to the hook's TARGETS "+
						"without a size budget, so it would ship ungated. Measured %d "+
						"normalised chars — add `%q: %d + routerGateSlack` to routerBudget.",
						key, r.normLen, key, r.normLen)
				}
				t.Logf("%s: %d normalised chars (gate %d) / %d at this root / %d worst case "+
					"(hard limit %d, worst-case margin %d, %d pointer(s))",
					key, r.normLen, gate, r.assembledLen, r.worstLen,
					routerHarnessHardLimit, routerHarnessHardLimit-r.worstLen, r.pointers)

				// 1. size, two-sided, on the path-independent number.
				//
				// Skipped when the hook degraded: normLen is then computed from the DELIVERED
				// payload, which is under the limit by construction, so the "you are below the
				// floor, re-baseline to N" branch would print the truncated size and talk a
				// maintainer into baking a nonsense number into routerBudget. The degrade
				// assertion below is the correct failure for that state.
				floor := gate - routerGateSlack
				switch {
				case r.degraded:
					// handled by the degrade assertion below
				case r.normLen > gate:
					t.Errorf("%s: payload %d exceeds gate %d. Move text to the on-demand tier "+
						"(skills/**/references/, pointed at by a `📄 Read …` line); do NOT raise "+
						"the gate. Above %d chars the harness delivers only a ~%d-char preview.",
						key, r.normLen, gate, routerHarnessHardLimit, routerPreviewChars)
				case r.normLen < floor:
					t.Errorf("%s: payload %d is %d chars below the gate's %d-char working "+
						"margin. Whatever that slimming freed is now an unguarded cushion for "+
						"the next silent growth. Set routerBudget[%q] = %d + routerGateSlack.",
						key, r.normLen, floor-r.normLen, routerGateSlack, key, r.normLen)
				}

				// ...and the worst case a real install can produce must clear the hard limit.
				// The gate above is path-independent on purpose; this is the check that keeps
				// that abstraction honest against the number the harness actually applies.
				if r.worstLen > routerHarnessHardLimit {
					t.Errorf("%s: at a %d-char plugin root the payload is %d chars, over the "+
						"harness limit %d. The gate passed only because it measures the path "+
						"folded away — the budget itself is too large.",
						key, routerAssumedRootLen, r.worstLen, routerHarnessHardLimit)
				}
				if gate+r.pointers*(routerAssumedRootLen-charLen(routerRootToken)) >= routerHarnessHardLimit {
					t.Errorf("%s: gate %d leaves no worst-case headroom under the harness limit "+
						"%d — it can no longer catch growth before the harness does",
						key, gate, routerHarnessHardLimit)
				}

				// 2. degrade path dormant on the shipped tree.
				if r.degraded {
					t.Errorf("%s: the hook DEGRADED this payload (%d assembled -> %d delivered). "+
						"The degrade path is a safety net for sessions running an older plugin "+
						"copy, not an acceptable steady state.", key, r.assembledLen, charLen(r.ctx))
				}
				if strings.Contains(r.ctx, routerBannerMark) {
					t.Errorf("%s: the over-budget banner appears on the shipped tree", key)
				}
				if strings.TrimSpace(r.stderr) != "" {
					t.Errorf("%s: hook wrote to stderr on the shipped tree: %s", key, r.stderr)
				}

				// 2b. Ordering: if a future payload ever busts the budget while running a
				// plugin copy too old to have the degrade path, the harness delivers only
				// tle(ctx). What has to survive that is the header — it is the single line
				// that tells the model fragments were deferred and that `Read` is how to get
				// them. Assert it lands in the window, which is a claim about ORDER.
				if !strings.HasPrefix(r.ctx, "[polyforge router]") {
					t.Errorf("%s: the payload does not start with the router header", key)
				}
				if !strings.Contains(tle(r.ctx), routerBudgetNotice) {
					t.Errorf("%s: the budget notice is outside the %d-char preview window, so a "+
						"truncated payload would not tell the model anything is missing",
						key, routerPreviewChars)
				}

				// 3. no unsubstituted placeholder, and every on-demand pointer resolves.
				if strings.Contains(r.ctx, "@@") {
					t.Errorf("%s: an @@…@@ placeholder survived substitution", key)
				}
				assertPointersResolve(t, key, pluginRoot, r.ctx)

				// 4. tiering is real — deferred bodies are not also inlined.
				for _, od := range onDemandFiles {
					if od.branch != "" && od.branch != br.name {
						continue
					}
					abs := filepath.Join(pluginRoot, od.rel)
					body, err := os.ReadFile(abs)
					if err != nil {
						t.Errorf("%s: on-demand file %s is missing (%v) — a resident fragment "+
							"points at it, so the pointer is dangling", key, od.rel, err)
						continue
					}
					// The marker must really be in the file, or "absent from the payload"
					// passes for free.
					if !strings.Contains(string(body), od.marker) {
						t.Errorf("%s: marker %q not found in %s — this check would pass "+
							"vacuously", key, od.marker, od.rel)
						continue
					}
					if strings.Contains(r.ctx, od.marker) {
						t.Errorf("%s: %s's body is inlined into the payload as well as deferred "+
							"— it costs the budget twice over", key, od.rel)
					}
					if !strings.Contains(r.ctx, filepath.Base(od.rel)) {
						t.Errorf("%s: %s is neither injected nor named by the payload — it is "+
							"orphaned, and nothing will ever tell a model to read it",
							key, od.rel)
					}
				}
			})
		}
	}

	// A budget entry for a skill that is no longer routed is dead weight that makes the map
	// look like it covers more than it does.
	for key := range routerBudget {
		if !seen[key] {
			t.Errorf("routerBudget has an entry for %q, which no longer corresponds to a "+
				"routed skill x branch — delete it rather than leaving the map overstating "+
				"its coverage", key)
		}
	}
}

var pointerRe = regexp.MustCompile(`(/[^\s` + "`" + `]+/skills/[^\s` + "`" + `]+\.md)`)

// assertPointersResolve checks that the absolute paths the router rendered into the payload
// actually exist. @@PLUGIN_ROOT@@ substitution is what makes a `📄 Read …` pointer usable at
// all: the model's cwd is the wi worktree, not the plugin cache, so a relative path would
// send it hunting. A pointer that does not resolve is the on-demand tier failing silently.
func assertPointersResolve(t *testing.T, key, pluginRoot, ctx string) {
	t.Helper()
	matches := pointerRe.FindAllString(ctx, -1)
	if len(matches) == 0 {
		t.Errorf("%s: the payload names no on-demand file by absolute path. Either the "+
			"@@PLUGIN_ROOT@@ substitution broke or the tiering was removed; both mean the "+
			"deferred fragments are unreachable.", key)
		return
	}
	absRoot := mustAbs(t, pluginRoot)
	for _, m := range matches {
		if !strings.HasPrefix(m, absRoot) {
			t.Errorf("%s: pointer %s does not sit under the plugin root %s", key, m, absRoot)
			continue
		}
		if _, err := os.Stat(m); err != nil {
			t.Errorf("%s: pointer %s does not resolve to a file: %v", key, m, err)
		}
	}
}

// TestRoutedSkillHook_SizeGateDiscriminates is control 5a. Without it, the size assertions
// above pass just as happily on a gate that has drifted far above the payload.
func TestRoutedSkillHook_SizeGateDiscriminates(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	for _, skill := range routedSkills(t, pluginRoot) {
		for _, br := range routerBranches {
			key := skill + "/" + br.name
			t.Run(key, func(t *testing.T) {
				base := renderRouter(t, pluginRoot, skill, br.superpowers)
				probeRoot := copyPluginTree(t, pluginRoot)
				padFragment(t, filepath.Join(probeRoot, "skills", "_common", "memory.md"), routerProbeChars)
				probe := renderRouter(t, probeRoot, skill, br.superpowers)

				// Compared on normLen, not raw length: the copy lives at a different absolute
				// path, and the router renders that path into the payload, so raw lengths
				// differ by the path difference alone and the arithmetic below would be
				// measuring the temp directory's name.
				if probe.degraded {
					t.Fatalf("%s: the probe build went over the harness limit and DEGRADED, so "+
						"its size is capped by construction and this control cannot measure the "+
						"gate. Lower routerProbeChars (currently %d) or restore the budget.",
						key, routerProbeChars)
				}
				// The equality is not tidiness: a probe is only evidence if the characters it
				// adds actually reach the measurement. Anything that normalises, trims or
				// degrades the payload in between would silently absorb them, leaving a green
				// control that proves only that the absorbing step works.
				if probe.normLen != base.normLen+routerProbeChars {
					t.Fatalf("%s: probe measured %d, expected %d (%d + %d). The probe "+
						"characters did not survive into the measurement, so this control "+
						"proves nothing about the gate.",
						key, probe.normLen, base.normLen+routerProbeChars,
						base.normLen, routerProbeChars)
				}
				if probe.normLen <= routerBudget[key] {
					t.Errorf("%s: +%d chars -> %d, still under the gate %d. The gate has "+
						"drifted above the payload and no longer catches growth of this size.",
						key, routerProbeChars, probe.normLen, routerBudget[key])
				}
			})
		}
	}
}

// TestRoutedSkillHook_DegradesLoudly is control 5b. Assertions elsewhere all measure the
// shipped tree and cannot see what the hook does when a payload is over budget anyway — an
// older plugin copy, a hand-edited fragment, an unmerged branch. That case IS the aihub#285
// failure mode, so drive it explicitly.
func TestRoutedSkillHook_DegradesLoudly(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	// Widens with TARGETS like the other two tests: a skill added to the hook must not get a
	// degrade path that nobody ever drove. The native branch is the one measured because it is
	// the larger of the two and therefore the one that reaches the limit first.
	for _, skill := range routedSkills(t, pluginRoot) {
		t.Run(skill, func(t *testing.T) { assertDegradesLoudly(t, pluginRoot, skill) })
	}
}

func assertDegradesLoudly(t *testing.T, pluginRoot, skill string) {
	t.Helper()
	overRoot := copyPluginTree(t, pluginRoot)
	// Size the pad from the measured payload so this stays a real violation as the fragments
	// change, rather than a constant that quietly stops being one. Measure the baseline from
	// overRoot, NOT from pluginRoot: the router renders the absolute plugin path into the
	// payload, so a baseline taken at a different path would be off by the path difference and
	// the prediction below would disagree with the hook for a reason that is not a defect.
	clean := renderRouter(t, overRoot, skill, false)
	if clean.degraded {
		t.Fatalf("the shipped tree already degrades (%d chars) — fix the budget first; this "+
			"control cannot distinguish its own fixture from a real regression", clean.assembledLen)
	}
	padded := filepath.Join(overRoot, "skills", "_common", "lifecycle.md")
	pad := routerHarnessHardLimit - clean.assembledLen + 500
	padFragment(t, padded, pad)

	over := renderRouter(t, overRoot, skill, false)
	wantAssembled := clean.assembledLen + pad
	if !over.degraded {
		t.Fatalf("an over-budget tree (%d chars) did not degrade — the hook would let the "+
			"harness truncate it silently, exactly the aihub#285 failure", wantAssembled)
	}
	// Two numbers derived from different sides: a wrong one shows up as disagreement rather
	// than as a green test.
	if over.assembledLen != wantAssembled {
		t.Errorf("hook reports %d chars assembled, this test predicted %d",
			over.assembledLen, wantAssembled)
	}
	if got := charLen(over.ctx); got > routerHarnessHardLimit || got == 0 {
		t.Errorf("degraded payload is %d chars — it must be non-empty and within %d, or it "+
			"would be replaced by a preview exactly as before", got, routerHarnessHardLimit)
	}
	if !strings.Contains(over.ctx, routerBannerMark) {
		t.Errorf("degraded payload carries no banner — the omission would be silent, which is " +
			"the failure mode this exists to remove")
	}
	if !strings.Contains(over.stderr, "over the 10000-char harness limit") {
		t.Errorf("nothing usable on stderr for an over-budget payload: %q", over.stderr)
	}
	// The banner must not lie: whatever stderr names as dropped has to be genuinely gone.
	m := regexp.MustCompile(`dropped (.+?)\. Run`).FindStringSubmatch(over.stderr)
	if m == nil {
		t.Fatalf("stderr names no dropped fragment, so 'dropped X' cannot be checked: %q", over.stderr)
	}
	dropped := parseDroppedNames(m[1])
	if len(dropped) == 0 {
		t.Fatalf("could not parse any fragment name out of %q", m[1])
	}
	t.Logf("over-budget tree dropped, in order: %v", dropped)

	// PRIORITY, not just membership. The hook drops by an explicit drop_rank because emission
	// order is a reading order: a plain suffix drop would throw away lifecycle.md (correct wi
	// state) while keeping storage.md (a four-line note). Nothing else in this file can see
	// that difference — the fixture pads lifecycle.md, so a suffix drop removes the padding on
	// its first iteration and every other assertion here still passes. Without the two checks
	// below, drop_rank and the paragraph defending it are decorative.
	if got := dropped[0]; got != "_common/storage.md" {
		t.Errorf("first fragment dropped was %q, expected _common/storage.md. The hook is "+
			"dropping by position rather than by drop_rank, so the cheapest fragment is being "+
			"kept and a load-bearing one discarded.", got)
	}
	for i := 1; i < len(dropped); i++ {
		prev, cur := routerDropRank(t, dropped[i-1]), routerDropRank(t, dropped[i])
		if cur < prev {
			t.Errorf("fragments were dropped out of rank order: %q (rank %d) before %q (rank "+
				"%d). Lower rank must go first.", dropped[i-1], prev, dropped[i], cur)
		}
	}

	// ...and every fragment the banner names as dropped must really be absent. The counter is
	// the point: the previous version of this loop `continue`d past both of its entries when
	// the mutant dropped lifecycle.md instead, and so asserted nothing at all while passing.
	checked := 0
	for _, frag := range []struct{ name, marker string }{
		{"_common/storage.md", "Artifact type for this step"},
		{"_common/memory.md", "Memory-First recall"},
		// NOT "Bracket every step": engine.native.md cross-references that heading, so the
		// marker would still be in the payload after lifecycle.md was correctly dropped and
		// this check would fire on a working hook. A marker has to be unique to its fragment.
		{"_common/lifecycle.md", "the bracket needs no version number"},
	} {
		// The marker must live in that fragment and NOWHERE else among the injected ones, or
		// "absent from the payload" is either free (marker nonexistent) or impossible
		// (marker duplicated elsewhere).
		assertMarkerUnique(t, pluginRoot, frag.name, frag.marker)
		if !containsName(dropped, frag.name) {
			continue
		}
		checked++
		if strings.Contains(over.ctx, frag.marker) {
			t.Errorf("stderr claims %s was dropped, but its content is still in the payload",
				frag.name)
		}
	}
	if checked == 0 {
		t.Errorf("the banner named %v, none of which this check knows how to verify — it "+
			"asserted nothing. Add a marker for the fragments actually being dropped.", dropped)
	}
}

// assertMarkerUnique proves a fragment marker is usable as evidence: present in the fragment
// it names, and absent from every other injected fragment. Both halves matter — a marker that
// exists nowhere makes an "absent" check pass for free, and one that also lives in a sibling
// makes it fail on a correct hook.
func assertMarkerUnique(t *testing.T, pluginRoot, frag, marker string) {
	t.Helper()
	owners := 0
	for _, rel := range []string{
		"skills/_common/memory.md",
		"skills/_common/storage.md",
		"skills/_common/lifecycle.md",
		"skills/pf-execute/engine.native.md",
	} {
		b, err := os.ReadFile(filepath.Join(pluginRoot, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if !strings.Contains(string(b), marker) {
			continue
		}
		owners++
		if !strings.HasSuffix(rel, frag) {
			t.Errorf("marker %q for %s also appears in %s — it cannot witness that %s was "+
				"dropped, because the sibling keeps it in the payload", marker, frag, rel, frag)
		}
	}
	if owners == 0 {
		t.Errorf("marker %q is in no injected fragment, so the 'dropped %s is really absent' "+
			"check would pass vacuously", marker, frag)
	}
}

// routerDropRank mirrors the hook's drop_rank. It is duplicated here on purpose: this is the
// POLICY under test, so changing it must require changing both sides deliberately rather than
// letting the test re-derive whatever the hook happens to do.
func routerDropRank(t *testing.T, name string) int {
	t.Helper()
	switch name {
	case "_common/storage.md":
		return 0
	case "_common/memory.md":
		return 1
	case "_common/lifecycle.md":
		return 2
	case "engine":
		return 3
	}
	t.Errorf("no drop_rank known for fragment %q — the hook's parts list gained an entry this "+
		"test does not know about, so the ordering check silently stops covering it", name)
	return -1
}

// parseDroppedNames pulls the ordered fragment names out of the hook's "dropped a (p), b (q)"
// stderr list.
func parseDroppedNames(list string) []string {
	var out []string
	for _, part := range strings.Split(list, ", ") {
		if i := strings.Index(part, " ("); i >= 0 {
			part = part[:i]
		}
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func containsName(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func copyPluginTree(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "plugin")
	if out, err := exec.Command("cp", "-r", src, dst).CombinedOutput(); err != nil {
		t.Fatalf("copy plugin tree: %v (%s)", err, out)
	}
	return dst
}

// padFragment appends n characters to a fragment. It rstrips first: the assembler strips each
// fragment's trailing newlines, so appending after them would land n+1 characters in the
// payload and break the exact-equality check that keeps control 5a honest.
func padFragment(t *testing.T, path string, n int) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	body := strings.TrimRight(string(b), "\n") + strings.Repeat("x", n) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
