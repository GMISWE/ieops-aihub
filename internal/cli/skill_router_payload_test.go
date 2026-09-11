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
//     short pointer; without it, engine.native.md. The native branch is the binding constraint
//     — before this change it was 23,873 characters against the superpowers branch's 14,482 —
//     so BOTH branches are measured. A gate that only saw the developer's own machine would
//     measure whichever branch that machine happens to select and call the other one covered.
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
	// IMPORTED from aihub#285, which bisected 10,000/10,001 on the SessionStart hook, and
	// CORROBORATED on PreToolUse rather than re-bisected there. What was measured on THIS
	// event: a 14,482-char additionalContext was diverted to a file in a real session and the
	// cut landed where tle() predicts; and across the session transcripts the largest payload
	// delivered inline is 9,116 while the smallest diverted one is 11,571. So the true
	// threshold is known to sit in (9,116, 11,571] and 10,000 is inside that interval — not
	// established to be exactly 10,000 on this event. The gate is conservative either way.
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
// slimmed the native branch from 23,873 to 9,256, and if the gate stayed near 10,000 that
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
// ⚠️ `native` is the binding branch and it is the one that runs out of room first. Two ceilings
// bind before this map does, and neither can be bought off by raising a number here:
//
//   - normLen must stay under ~9,600, or gate+slack+2 pointers crosses 10,000 and the worst-case
//     assertion below fires whatever is written here;
//   - normLen must stay under ~9,460, or TestRoutedSkillHook_SizeGateDiscriminates degrades on
//     its own +400-character probe (the probe renders from a copy under an ~85-char TMPDIR path)
//     and control 5a stops being able to measure the gate at all. aihub#353 hit exactly that:
//     the native branch reached 9,523 and the control went red before this ratchet did.
//
// A third `📄` pointer alone costs a further 125 of the worst case (see routerAssumedRootLen), so
// deferred sections are reached by a section reference (`§0e`) through an EXISTING pointer. The
// next contributor who needs to add a paragraph to a native-branch fragment must RE-TIER
// something out to skills/**/references/, not re-budget.
//
// aihub#338 added the Iron Rules to the header (+735 chars on BOTH branches) and paid for them
// by re-tiering, not by re-budgeting: engine.native.md -121, _common/lifecycle.md -601,
// _common/memory.md -128 (-850 on disk), with the removed prose moved into existing references/
// files or — in memory.md's case — deleted outright, because it asserted that a session-start
// fragment was "already in context", which is false on exactly the subagent path this serves.
// Net on native: 9,433 -> 9,318, i.e. 115 characters BELOW where it started, which is what
// bought back the control-5a margin the paragraph above says is the real ceiling.
//
// aihub#478 added output-format.md to the same header (+766 on BOTH branches) and was paid for
// the same way, from the resident COMMON fragments so that both branches settle rather than one:
// _common/memory.md -342, _common/lifecycle.md -373, _common/storage.md -65 (-780 on disk),
// plus a native-only -77 for a heartbeat line engine.native.md duplicated out of
// _common/lifecycle.md, which is injected on both branches anyway. What MOVED rather than
// vanished went to a section that was already its subject — the lock-conflict prose to
// lifecycle-details §3, the .pf_* mechanics to §5 — and what vanished was either restated
// elsewhere in the same payload or addressed to whoever edits the template rather than to the
// model reading it. Net: native 9,318 -> 9,227,
// superpowers 7,002 -> 6,988. Both BELOW where they started, again.
//
// pf-spec / pf-plan are header-only (routerModeHeaderOnly): no fragments, so their number is the
// header alone and is identical on both branches. That equality is not a coincidence to be
// tidied away — it is the assertion that the engine branch really is not consulted in that mode.
// aihub#553's 1.1.31 batch moved these: aihub#519 corrected output-format.md's Status example
// (the de-locked locks line, the lease-era expires row), -34 chars in the header of every
// routed skill; aihub#553 grew engine.native.md (native branch only) by a net +47 for the
// explicit dispatch-model instruction. Every floor sits on the measured payload per this
// map's own invariant — the native gate (9,274+150) still clears both real ceilings: the
// ~9,460 discriminator bound applies to the PAYLOAD (which is 186 under it), and the
// worst-case check on the gate (9,424 + 2 pointers x 125 = 9,674) stays under the harness
// limit.
var routerBudget = map[string]int{
	"pf-execute/superpowers": 6954 + routerGateSlack,
	"pf-execute/native":      9274 + routerGateSlack,
	"pf-plan/native":         1701 + routerGateSlack,
	"pf-plan/superpowers":    1701 + routerGateSlack,
	"pf-spec/native":         1701 + routerGateSlack,
	"pf-spec/superpowers":    1701 + routerGateSlack,
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

// onDemandFiles are the deferred fragments of the STEP-BODY mode. Each must exist on disk, be
// pointed at from the payload, and NOT have its body inlined into it — that is what "deferred"
// means. A header-only payload defers nothing and is checked the opposite way: it must name no
// plugin-root path at all.
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

// execRouter is the ONE exec core behind every hook invocation in this file: fixture home,
// the payload envelope, the hook run under bash, a scrubbed environment, stderr captured,
// non-zero exit fatal. It does not interpret the emission — the aihub#514 header-only guard
// makes an EMPTY emission a correct outcome for some fixtures, and asserting that requires
// being able to observe it. Callers that need a parsed, guaranteed-non-empty render go
// through renderRouter; callers that must see the raw (possibly empty) output call this
// directly. It used to exist twice (renderRouter and a runRouterRaw copy of its first half),
// which is how the two could drift apart without either going red.
func execRouter(t *testing.T, pluginRoot, skill string, superpowers bool) (string, string) {
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
	return string(stdout), stderr.String()
}

// renderRouter drives the shipped hook exactly as the harness does, with the engine branch
// pinned by fixture settings rather than inherited from whoever is running the test.
func renderRouter(t *testing.T, pluginRoot, skill string, superpowers bool) routerRender {
	t.Helper()
	rawOut, rawErr := execRouter(t, pluginRoot, skill, superpowers)
	stdout := []byte(rawOut)
	// The hook is FAIL-SILENT by design. That is right for production and fatal for a gate:
	// an empty render makes every assertion below vacuously true.
	if len(strings.TrimSpace(rawOut)) == 0 {
		t.Fatalf("hook emitted nothing for %s (superpowers=%v) — it is fail-silent, so this "+
			"gate cannot tell a clean render from no render (stderr: %s)",
			skill, superpowers, rawErr)
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
	r := routerRender{ctx: ctx, stderr: rawErr, assembledLen: charLen(ctx)}
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
	// The header's leading prefix, rendered by the hook in both modes. SINGLE-SOURCED here on
	// purpose: it used to be hard-copied at every site that looked for it, including both
	// banner-order checks, and those guarded themselves with `strings.Index(...) >= 0` — so a
	// reworded sentinel updated in only one copy turned the stale site's lookup into -1 and its
	// self-guard into a silent skip. Green by vacancy, not by order. One const cannot drift
	// against itself, and assertBannerLeads makes a miss a FAILURE rather than a skip.
	routerHeaderMark = "[polyforge router]"
	// The sentence in the router's header that has to survive truncation.
	routerBudgetNotice = "Fragments marked 📄 are NOT injected"
	// Its header-only counterpart. Deliberately a DIFFERENT sentence: that mode defers nothing,
	// so promising a deferred tier there would send the model after files this payload never
	// named. What has to survive truncation instead is the claim that the SKILL.md still rules.
	routerHeaderOnlyNotice = "the SKILL.md is self-sufficient and remains authoritative"

	// The first parts[] fragment, i.e. the boundary between "header" and "droppable". Used to
	// prove the header-resident fragments sit ahead of everything the degrade loop can drop.
	routerFirstPartMark = "# _common/memory.md"
)

// TestRouterPreviewWindowCheckDiscriminates is the control for the ordering assertion above.
// Without it, "the notice is in the window" would pass just as happily on a payload short
// enough that tle() returns everything, or on a broken tle() that never truncates.
func TestRouterPreviewWindowCheckDiscriminates(t *testing.T) {
	head := routerHeaderMark + " " + routerBudgetNotice + " — rest of the header.\n"
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
	modes := routerModes(t, pluginRoot)

	seen := map[string]bool{}
	for _, skill := range skills {
		headerOnly := modes[skill] == routerModeHeaderOnly
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
				if !strings.HasPrefix(r.ctx, routerHeaderMark) {
					t.Errorf("%s: the payload does not start with the router header", key)
				}
				notice := routerBudgetNotice
				if headerOnly {
					notice = routerHeaderOnlyNotice
				}
				if !strings.Contains(tle(r.ctx), notice) {
					t.Errorf("%s: %q is outside the %d-char preview window, so a truncated "+
						"payload would not tell the model what it is holding",
						key, notice, routerPreviewChars)
				}

				// 3. no unsubstituted placeholder, and every on-demand pointer resolves.
				if strings.Contains(r.ctx, "@@") {
					t.Errorf("%s: an @@…@@ placeholder survived substitution", key)
				}
				if headerOnly {
					// The mirror of assertPointersResolve, and a real check rather than a
					// waiver: this mode injects no fragment, so a plugin-root path appearing
					// in it means one leaked in — and the header makes no promise that
					// anything was deferred, so the model would never be told to read it.
					if r.pointers != 0 {
						t.Errorf("%s is header-only, yet its payload names the plugin root %d "+
							"time(s). Either a fragment leaked into this mode or the header "+
							"grew a pointer it does not explain.", key, r.pointers)
					}
				} else {
					assertPointersResolve(t, key, pluginRoot, r.ctx)
				}

				// 4. tiering is real — deferred bodies are not also inlined. Step-body only:
				// there is no deferred tier to be honest about in header-only mode, and the
				// pointer check above is what covers it there.
				if headerOnly {
					return
				}
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

	// The reverse direction: a budget entry with no routed skill x branch behind it. Two very
	// different states produce that observation, and they need OPPOSITE remedies, so the
	// message must not collapse them (aihub#537). The pre-fix message said "delete it" for
	// every orphan — but when the orphan exists because a skill was REMOVED from TARGETS,
	// that instruction completes the de-routing: the budget rows are the ledger witnessing
	// that the skill used to be routed, and for a skill not on routerFloor they are the ONLY
	// witness (the floor deliberately does not track additions, so a post-floor skill that
	// gets de-routed reds nowhere else). The aihub#513 reproduction had to delete exactly
	// those two rows to reach a fully green tree, with this gate's own message telling it to.
	routedSet := make(map[string]bool, len(skills))
	for _, s := range skills {
		routedSet[s] = true
	}
	branchSet := make(map[string]bool, len(routerBranches))
	for _, br := range routerBranches {
		branchSet[br.name] = true
	}
	for key := range routerBudget {
		if seen[key] {
			continue
		}
		if skill, deRouted := orphanBudgetKeyLooksDeRouted(key, routedSet, branchSet); deRouted {
			// A well-formed key over a real branch whose skill is not in TARGETS. The
			// dangerous reading is de-routing, so the message leads with it and does NOT
			// offer "delete this row" as the mechanical fix — deleting the last witness is
			// how a silent de-route becomes green, not how it becomes reviewed.
			t.Errorf("routerBudget[%q] carries a measured budget but %q is not in the hook's "+
				"TARGETS — the router is INERT for it. If a TARGETS entry was removed, this "+
				"row is the record that %q used to be routed: RESTORE the TARGETS entry, do "+
				"not delete the budget rows to get green. If the de-routing is deliberate, "+
				"remove the TARGETS entry, these budget rows and any routerFloor entry in one "+
				"reviewed change that says so. (If instead this row was newly added for a "+
				"skill that was never routed, add the skill to TARGETS or take the row out.)",
				key, skill, skill)
			continue
		}
		// Malformed key or unknown branch: this entry never corresponded to any routed
		// skill x branch, so it is dead weight that makes the map look like it covers more
		// than it does — here, and only here, deletion is the remedy.
		t.Errorf("routerBudget has an entry for %q, which does not name a <skill>/<branch> "+
			"this gate can ever measure (branches: superpowers, native) — fix the key or "+
			"delete the entry rather than leaving the map overstating its coverage", key)
	}
}

// orphanBudgetKeyLooksDeRouted classifies an unseen routerBudget key. A well-formed
// "<skill>/<branch>" over a branch the gate really measures, whose skill is absent from
// TARGETS, reads as de-routing (or an entry added ahead of routing — the message covers
// both); anything else — malformed key, unknown branch — never corresponded to a measurable
// skill x branch at all. The split exists because the two states need opposite remedies:
// the first must be resisted, the second deleted. Extracted so the classification itself
// can be pinned by TestOrphanBudgetKeyClassifierDiscriminates rather than trusted.
func orphanBudgetKeyLooksDeRouted(key string, routedSet, branchSet map[string]bool) (string, bool) {
	skill, branch, ok := strings.Cut(key, "/")
	return skill, ok && branchSet[branch] && !routedSet[skill]
}

// TestOrphanBudgetKeyClassifierDiscriminates is the control for the orphan split above.
// Both branches of the split call t.Errorf, so a wrong classification is invisible in the
// red/green signal — it only swaps the remedy the message prescribes, and prescribing
// deletion to a de-routed skill is exactly the aihub#537 defect. So the classification is
// pinned here, on both sides.
func TestOrphanBudgetKeyClassifierDiscriminates(t *testing.T) {
	routed := map[string]bool{"pf-execute": true}
	branches := map[string]bool{"native": true, "superpowers": true}
	cases := []struct {
		key      string
		deRouted bool
		why      string
	}{
		{"pf-spec/native", true, "valid branch, skill gone from TARGETS — the #513 shape"},
		{"pf-spec/superpowers", true, "same, other branch"},
		{"pf-execute/native", false, "skill still routed — not an orphan the split should resist"},
		{"pf-execute/bogus", false, "unknown branch — a key this gate can never measure"},
		{"pf-spec", false, "no branch separator — malformed"},
		{"", false, "empty key — malformed"},
	}
	for _, tc := range cases {
		if _, got := orphanBudgetKeyLooksDeRouted(tc.key, routed, branches); got != tc.deRouted {
			t.Errorf("classifier(%q) = %v, want %v (%s) — the orphan loop would prescribe "+
				"the wrong remedy for this key", tc.key, got, tc.deRouted, tc.why)
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

// ironRulesFragment / outputFormatFragment are the ONE authoritative copy of each text.
// hooks/pf-session-start reads both for the session payload, and hooks/pf-skill-router reads the
// SAME two files for its header — iron-rules since aihub#338, output-format since aihub#478.
// Nothing here restates either: a second copy is what aihub#294 already cost this repo (IR1's
// worktree path drifted between two hand-maintained copies for three months), so the assertions
// below compare the payload against the file rather than against a literal.
const (
	ironRulesFragment    = "skills/using-polyforge/fragments/iron-rules.md"
	outputFormatFragment = "skills/using-polyforge/fragments/output-format.md"
)

// headerResidentFragment is a fragment the router must copy into its HEADER, verbatim.
//
// markers is the ANTI-VACUITY guard: strings.Contains(payload, "") is true for every payload
// ever produced, so an emptied or gutted fragment would otherwise turn the whole assertion green
// while delivering nothing — the same shape as the NegativeControl in
// engine_native_contract_test.go. They are checked against the FILE first, so "the payload
// contains this file" keeps meaning "the payload contains this rule".
type headerResidentFragment struct {
	rel     string
	markers []string
	lost    string // what a dispatched subagent is left without when this goes missing
}

var (
	ironRulesResident = headerResidentFragment{
		rel:     ironRulesFragment,
		markers: []string{"IR1 —", "IR2 —", "IR3 —"},
		lost:    "no Iron Rules at all (aihub#338 layer 2)",
	}
	outputFormatResident = headerResidentFragment{
		rel: outputFormatFragment,
		markers: []string{
			"MUST follow this format exactly", "## Result", "## Status", "## Next steps",
			"max 5 items", // the rule most easily lost to a "shorter" rewrite of the example
		},
		lost: "the three-segment CONTRACT — the Status field list, the max-5 rule and the MUST " +
			"— leaving only the three headings _common/lifecycle.md names, which is exactly the " +
			"aihub#478 gap",
	}
)

// TestRoutedSkillHook_InjectsIronRules is the aihub#338 layer-2 gate.
// TestRoutedSkillHook_InjectsOutputFormat is the aihub#478 one. Both run assertHeaderResident.
//
// WHY THEY EXIST
// A dispatched subagent does not inherit SessionStart's additionalContext, and every routed skill
// runs in one. Measured 2026-09-04 with a zero-tool-call probe pinned to the subagent's FIRST
// action: CLAUDE.md and MEMORY.md arrived, IR1-IR3 did not. PreToolUse does fire inside a subagent
// (measured the same day — pf-commit-guard blocked a push from one), so this hook is the only
// resident channel that reaches the executor, and both texts ride it.
//
// WHY THESE ASSERTIONS AND NOT "the payload mentions IR1"
//  1. VERBATIM against the file on disk, so a paraphrase or a drifted second copy fails. A marker
//     check would pass on a copy that had drifted, which is the defect being avoided.
//  2. In the HEADER, measured as "ahead of the first parts[] fragment" — NOT as "after the first
//     separator", which the header's own trailing separator would satisfy for free. parts[] is
//     what the degrade loop drops to fit, and a rule that can be dropped to make room is not a
//     rule. In header-only mode there is no parts[] at all, so the same claim is asserted as the
//     ABSENCE of one: nothing in that payload is droppable.
//  3. Every routed skill and both branches. A real user gets exactly one branch and which one is
//     not the runner's choice; the native branch is the one with no budget to spare, so it is the
//     one that would be "fixed" by dropping this.
func TestRoutedSkillHook_InjectsIronRules(t *testing.T) {
	assertHeaderResident(t, ironRulesResident)
}

func TestRoutedSkillHook_InjectsOutputFormat(t *testing.T) {
	assertHeaderResident(t, outputFormatResident)
}

func assertHeaderResident(t *testing.T, frag headerResidentFragment) {
	t.Helper()
	pluginRoot := pluginRootDir(t)
	modes := routerModes(t, pluginRoot)
	body := mustHeaderFragmentBody(t, pluginRoot, frag)

	for _, skill := range routedSkills(t, pluginRoot) {
		for _, br := range routerBranches {
			key := skill + "/" + br.name
			t.Run(key, func(t *testing.T) {
				r := renderRouter(t, pluginRoot, skill, br.superpowers)

				idx := strings.Index(r.ctx, body)
				if idx < 0 {
					t.Fatalf("%s: the payload does not carry %s verbatim. A subagent running "+
						"this skill would have %s — or the header has grown its own paraphrase, "+
						"which is the aihub#294 two-copies defect, not a fix for this one.",
						key, frag.rel, frag.lost)
				}

				first := strings.Index(r.ctx, routerFirstPartMark)
				if modes[skill] == routerModeHeaderOnly {
					if first >= 0 {
						t.Errorf("%s is header-only, yet its payload carries a parts[] fragment "+
							"at offset %d. The mode's whole guarantee is that there is nothing "+
							"the degrade loop could drop this text to make room for.", key, first)
					}
					return
				}
				if first < 0 {
					t.Fatalf("%s: cannot find the first parts[] fragment in the payload, so "+
						"'this text is in the header' has no boundary to be measured against",
						key)
				}
				if idx > first {
					t.Errorf("%s: %s appears at offset %d, after the first parts[] fragment at "+
						"%d — i.e. it is a fragment, not the header. The degrade loop drops "+
						"fragments to fit, so text placed there can be dropped to make room for "+
						"whatever pushed the payload over.", key, frag.rel, idx, first)
				}
			})
		}
	}
}

// mustHeaderFragmentBody reads a header-resident fragment and proves it is usable as evidence
// before anything is compared against it.
func mustHeaderFragmentBody(t *testing.T, pluginRoot string, frag headerResidentFragment) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(pluginRoot, frag.rel))
	if err != nil {
		t.Fatalf("%s: cannot read it (%v). It is the single source for this text in both the "+
			"session payload and the router header; if it moved, both channels lost it and this "+
			"gate must fail rather than skip.", frag.rel, err)
	}
	body := strings.TrimSpace(string(raw))
	if body == "" {
		t.Fatalf("%s is empty. Every containment assertion against it would pass vacuously.",
			frag.rel)
	}
	for _, marker := range frag.markers {
		if !strings.Contains(body, marker) {
			t.Fatalf("%s does not contain %q, so 'the payload contains this file' would no "+
				"longer mean 'the payload contains the rule'", frag.rel, marker)
		}
	}
	return body
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
				// iron-rules.md rather than _common/memory.md (aihub#478). The header is the
				// only text resident in BOTH modes: padding a parts[] fragment adds nothing at
				// all to a header-only payload, so the equality check below would fail for two
				// of the three routed skills — a red that says nothing about the gate, which is
				// the state a control must never be able to reach. The +400 is identical either
				// way; the equality assertion is what keeps it honest.
				padFragment(t, filepath.Join(probeRoot, ironRulesFragment), routerProbeChars)
				probe := renderRouter(t, probeRoot, skill, br.superpowers)

				// Compared on normLen, not raw length: the copy lives at a different absolute
				// path, and the router renders that path into the payload, so raw lengths
				// differ by the path difference alone and the arithmetic below would be
				// measuring the temp directory's name.
				if probe.degraded {
					// Most likely cause is the environment, not the tree: the probe renders
					// from a copy under TMPDIR, whose absolute path is substituted into the
					// payload once per 📄 pointer, so a long root costs
					// pointers x (len(root) - len(routerRootToken)). On the native branch
					// (2 pointers, base 9,227) base+400 crosses the harness limit at a root of
					// about 200 characters and the hook then degrades by design; GitHub runners
					// land ~105. Header-only payloads carry no pointer and are indifferent.
					// Check that before touching routerProbeChars or the budget — this is
					// never a false green, only a confusing red.
					t.Fatalf("%s: the probe build degraded, so its size is capped by "+
						"construction and this control cannot measure the gate. TMPDIR is %q "+
						"(%d chars) — if that is long, shorten it and re-run before concluding "+
						"anything about routerProbeChars (%d) or the budget.",
						key, os.TempDir(), len(os.TempDir()), routerProbeChars)
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
	modes := routerModes(t, pluginRoot)
	// Widens with TARGETS like the other two tests: a skill added to the hook must not get a
	// degrade path that nobody ever drove. The native branch is the one measured because it is
	// the larger of the two and therefore the one that reaches the limit first.
	for _, skill := range routedSkills(t, pluginRoot) {
		t.Run(skill, func(t *testing.T) {
			if modes[skill] == routerModeHeaderOnly {
				// The degrade loop drops parts[], and this mode has none — the fixture below
				// pads a fragment that is not injected, so it could never drive the path. That
				// exemption is ASSERTED, not assumed: if a fragment ever leaks into this mode
				// it acquires a degrade path nothing drives, which is precisely the silent
				// state this suite exists to remove.
				assertNothingDroppable(t, pluginRoot, skill)
				return
			}
			assertDegradesLoudly(t, pluginRoot, skill)
		})
	}
}

// assertNothingDroppable is the header-only counterpart of assertDegradesLoudly: it proves the
// exemption is structural rather than an oversight.
func assertNothingDroppable(t *testing.T, pluginRoot, skill string) {
	t.Helper()
	r := renderRouter(t, pluginRoot, skill, false)
	for _, mark := range []string{
		routerFirstPartMark, "# _common/storage.md", "# _common/lifecycle.md",
		"native engine (Wi Agent main loop)",
	} {
		if strings.Contains(r.ctx, mark) {
			t.Errorf("%s is header-only, but its payload carries %q. It now has droppable "+
				"fragments and no test drives their degrade path — route it as step-body or "+
				"take the fragment back out.", skill, mark)
		}
	}
	if strings.Contains(r.ctx, routerBannerMark) {
		t.Errorf("%s: the over-budget banner appears on a header-only payload of %d chars",
			skill, charLen(r.ctx))
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
	// aihub#514 F4: the banner must LEAD the degraded payload. The hook's final safety cut is
	// ctx[:limit] — a TAIL cut — so a banner joined after the header is the first thing a
	// too-long header pushes out: exactly the sentence announcing the truncation.
	assertBannerLeads(t, over.ctx, over.stderr)
	// aihub#338 / aihub#478: the degrade loop drops FRAGMENTS. Both header-resident texts live
	// in the header precisely so that it cannot drop them, and this is the only place that
	// state can be observed — every other assertion in this file measures a tree that does not
	// degrade. Read from overRoot, the tree that actually degraded, not from pluginRoot.
	for _, frag := range []headerResidentFragment{ironRulesResident, outputFormatResident} {
		body := mustHeaderFragmentBody(t, overRoot, frag)
		if !strings.Contains(over.ctx, body) {
			t.Errorf("the DEGRADED payload (%d chars) lost %s. It lives in the header for "+
				"exactly this reason: a rule that gets dropped to make room is not a rule. "+
				"stderr: %s", charLen(over.ctx), frag.rel, over.stderr)
		}
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

// assertBannerLeads proves the over-budget banner is PRESENT in a degraded payload and sits
// ahead of the header. Both sentinel lookups fail LOUDLY: the two inline copies of this check
// guarded themselves with `bi >= 0 && hi >= 0`, so rewording a sentinel in the hook while
// updating only one test copy left the stale site silently skipping the ordering assertion
// (strings.Index == -1) instead of going red — an order gate green precisely because it could
// no longer see either of the things it orders.
func assertBannerLeads(t *testing.T, ctx, stderr string) {
	t.Helper()
	bi := strings.Index(ctx, routerBannerMark)
	if bi < 0 {
		t.Errorf("no %q banner in the degraded payload — the truncation is silent, the exact "+
			"aihub#514 F4 failure the degrade path exists to remove (stderr: %s)",
			routerBannerMark, stderr)
	}
	hi := strings.Index(ctx, routerHeaderMark)
	if hi < 0 {
		t.Errorf("header mark %q is missing from the degraded payload — either the cut took "+
			"the header itself or the sentinel drifted from the hook. Both are failures; "+
			"neither may downgrade the ordering assertion to a silent skip.", routerHeaderMark)
	}
	if bi < 0 || hi < 0 {
		return
	}
	if bi > hi {
		t.Errorf("the banner sits at offset %d, after the header at offset %d — the hook's "+
			"final safety cut is ctx[:limit], a TAIL cut, so it removes the banner before "+
			"anything else", bi, hi)
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

// TestRoutedSkillHook_HeaderOnlyOverBudgetBannerSurvives drives the header-only over-budget
// path (aihub#514 F4). That mode has no droppable parts[], so the ONLY way the hook can fit
// an oversized header is the ctx[:limit] tail cut — and with the banner joined after the
// header, the tail cut removed exactly the sentence announcing "this payload is incomplete",
// reinstating the silent truncation the degrade block exists to prevent. The shipped header
// is ~1.7k chars so the path is not reachable on this tree; this fixture is the only thing
// that drives it (assertDegradesLoudly deliberately skips header-only skills).
func TestRoutedSkillHook_HeaderOnlyOverBudgetBannerSurvives(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	modes := routerModes(t, pluginRoot)
	drove := 0
	for _, skill := range routedSkills(t, pluginRoot) {
		if modes[skill] != routerModeHeaderOnly {
			continue
		}
		drove++
		t.Run(skill, func(t *testing.T) {
			overRoot := copyPluginTree(t, pluginRoot)
			clean := renderRouter(t, overRoot, skill, false)
			if clean.degraded {
				t.Fatalf("the copied tree already degrades (%d chars) — this fixture could "+
					"not be told apart from a real regression", clean.assembledLen)
			}
			// Pad a HEADER-resident fragment: in this mode everything is header, so the
			// degrade loop has nothing to drop and must fall through to the tail cut.
			pad := routerHarnessHardLimit - clean.assembledLen + 500
			padFragment(t, filepath.Join(overRoot, ironRulesFragment), pad)

			over := renderRouter(t, overRoot, skill, false)
			if !over.degraded {
				t.Fatalf("a %d-char header-only payload did not degrade — the harness would "+
					"replace it with a ~%d-char preview silently, the aihub#285 failure",
					clean.assembledLen+pad, routerPreviewChars)
			}
			if got := charLen(over.ctx); got == 0 || got > routerHarnessHardLimit {
				t.Errorf("delivered payload is %d chars — must be non-empty and within %d",
					got, routerHarnessHardLimit)
			}
			assertBannerLeads(t, over.ctx, over.stderr)
			// The WHOLE banner, not a prefix of it: both of its load-bearing sentences.
			for _, want := range []string{
				"THIS HEADER IS TRUNCATED",
				"Treat any rule below as possibly incomplete",
			} {
				if !strings.Contains(over.ctx, want) {
					t.Errorf("the delivered payload lost the banner sentence %q — a reader is "+
						"no longer told what it is holding", want)
				}
			}
			// And it must tell the truth for this mode: nothing was dropped, so it must not
			// claim fragments were, nor point at a deferred tier this payload never had.
			if strings.Contains(over.ctx, "dropped fragment(s)") {
				t.Errorf("the header-only banner claims fragments were dropped; this mode has none")
			}
			if !strings.Contains(over.stderr, "over the 10000-char harness limit") {
				t.Errorf("nothing usable on stderr for the over-budget header: %q", over.stderr)
			}
		})
	}
	if drove == 0 {
		t.Error("no header-only skill in TARGETS — this test drove nothing; if the mode was " +
			"removed, remove the test with it rather than leaving it green by vacancy")
	}
}

// TestRoutedSkillHook_HeaderOnlyEmptyFragmentGuard is aihub#514 F5. In header-only mode the
// resident rules ARE the payload: a render with iron-rules.md or output-format.md empty still
// announced "the resident context a DISPATCHED subagent does not inherit" while carrying no
// rules at all. The guard makes that render inert instead — the SKILL.md is self-sufficient
// in this mode, so silence leaves it authoritative, while a lying payload claims coverage
// nothing provides. step-body must stay fail-open on the same fixture: its payload is the
// step body, and going inert there would kill the engine to punish a missing rule text.
func TestRoutedSkillHook_HeaderOnlyEmptyFragmentGuard(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	modes := routerModes(t, pluginRoot)
	cases := []struct {
		name   string
		gutted []string
	}{
		{"iron-rules-empty", []string{ironRulesFragment}},
		{"output-format-empty", []string{outputFormatFragment}},
		{"both-empty", []string{ironRulesFragment, outputFormatFragment}},
	}
	droveHeaderOnly, droveStepBody := 0, 0
	for _, skill := range routedSkills(t, pluginRoot) {
		headerOnly := modes[skill] == routerModeHeaderOnly
		for _, tc := range cases {
			t.Run(skill+"/"+tc.name, func(t *testing.T) {
				root := copyPluginTree(t, pluginRoot)
				// Control first: the COPY, before gutting, emits. Without this, the silence
				// asserted below could be the copy failing rather than the guard firing.
				// execRouter, not renderRouter: the guard under test makes an EMPTY emission
				// the correct outcome, and renderRouter's fail-silent trap forbids observing it.
				if out, stderr := execRouter(t, root, skill, false); strings.TrimSpace(out) == "" {
					t.Fatalf("the copied tree emits nothing before gutting (stderr: %s) — "+
						"fixture broken, nothing below means anything", stderr)
				}
				for _, rel := range tc.gutted {
					if err := os.WriteFile(filepath.Join(root, rel), []byte("  \n"), 0o644); err != nil {
						t.Fatalf("gut %s: %v", rel, err)
					}
				}
				out, stderr := execRouter(t, root, skill, false)
				if headerOnly {
					droveHeaderOnly++
					if strings.TrimSpace(out) != "" {
						t.Errorf("header-only %s still emitted %d bytes with %v empty — a "+
							"payload claiming to carry the resident rules while carrying none",
							skill, len(out), tc.gutted)
					}
					if !strings.Contains(stderr, "NOT emitted") {
						t.Errorf("the guard fired silently — stderr must say why, or the "+
							"missing payload is undiagnosable outside the model: %q", stderr)
					}
					return
				}
				// The guard's scope is the mode whose payload is nothing but rules:
				// step-body must stay fail-open, and "fail-open" means a WELL-FORMED
				// payload, not merely bytes. Reviewer-confirmed on PR #452: asserting
				// non-emptiness alone stayed green when the hook printed non-JSON here.
				droveStepBody++
				var emitted struct {
					HookSpecificOutput struct {
						AdditionalContext string `json:"additionalContext"`
					} `json:"hookSpecificOutput"`
				}
				if err := json.Unmarshal([]byte(out), &emitted); err != nil {
					t.Errorf("step-body %s must stay fail-open with a well-formed payload; "+
						"got unparseable output (%v): %.120q", skill, err, out)
				} else if strings.TrimSpace(emitted.HookSpecificOutput.AdditionalContext) == "" {
					t.Errorf("step-body %s emitted JSON carrying no additionalContext — "+
						"fail-open in name only", skill)
				}
			})
		}
	}
	// Anti-vacuity (reviewer-confirmed on PR #452: without these, deleting the header-only
	// TARGETS entries left every guard assertion unexecuted and this test green).
	if droveHeaderOnly == 0 {
		t.Error("no header-only skill was driven — the guard assertions ran zero times; if " +
			"the mode was removed, remove this test with it rather than leaving it green by " +
			"vacancy")
	}
	if droveStepBody == 0 {
		t.Error("no step-body skill was driven — the fail-open negative control ran zero times")
	}
}
