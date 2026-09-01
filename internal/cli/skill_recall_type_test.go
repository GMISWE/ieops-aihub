package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Recurrence gate for the `pf_recall(type=...)` contract (aihub#289).
//
// WHAT WENT WRONG
// ---------------
// pf_recall's `type` filter is a LIST. Templates taught `type="a|b|c"`, and nothing in the
// chain — MCP tool, HTTP handler, or either SQL builder — ever split on `|`. The whole
// string arrived as ONE type name, matched no row on the exact branch and none on the LIKE
// branch, and came back `{"items":null,"total":0}`: an empty set, no error, no warning,
// indistinguishable from "this project holds no such memory".
//
// WHY THIS FILE READS RENDERED OUTPUT, NOT SOURCE
// -----------------------------------------------
// The first version of this gate walked `plugins/polyforge/**/*.md` for `type="...|..."`
// and reported clean — while the single live producer of a piped value went right past it.
// That value lives in hooks/pf-skill-router's TARGETS tuple as a bare string with no
// `type=` to anchor on, in a file with no `.md` extension, and it is substituted into
// `_common/memory.md` through an `@@RECALL_TYPE@@` placeholder that contains no pipe. All
// three of the gate's assumptions were wrong at once, and the composed artifact — the only
// thing a model ever actually sees — was never examined.
//
// So the PRIMARY probe here renders the shipped hook and scans what it emits.
// TestSkillTemplates_NoPipedTypeStrings is the secondary, source-level sweep.
//
// WHAT COUNTS AS A VIOLATION — and why prose enumerations do not
// -------------------------------------------------------------
// The gate flags a pipe only where the value is TRANSMITTED: an argument position whose
// text becomes the request (`type="a|b"`, `type=a|b`, `type=["a|b"]`). It deliberately does
// NOT flag a pipe inside placeholder brackets (`type=<experience.*|fact.*|rule.*>`) or a
// prose enumeration of the vocabulary (memory-conventions.md's
// "experience.approach|code|debug|pitfall", tools_memory.go's
// "(methodology.spec|plan|review|...)").
//
// That distinction is the substance of the argument for rejecting `|` rather than
// implementing it, so it is worth stating precisely: in `<...>` and in prose, the pipe is
// resolved BY THE READER before any call is made, and the character never reaches the
// server. In an argument position it reaches the server verbatim. The same character is
// therefore safe as notation and unusable as an operator — which is exactly why it cannot
// become one.
var (
	// Quoted value: type="a|b"
	pipedTypeQuoted = regexp.MustCompile(`\btype\s*=\s*"[^"]*\|`)
	// Array value with a piped ENTRY: type=["a|b", ...]
	pipedTypeArray = regexp.MustCompile(`\btype\s*=\s*\[[^\]]*\|`)
	// Bare unquoted value: type=a.b|c.d  (not type=<...>, not type=["..."])
	pipedTypeBare = regexp.MustCompile(`\btype\s*=\s*[A-Za-z0-9_.*-]+\|`)
)

func pipedTypeMatch(line string) bool {
	return pipedTypeQuoted.MatchString(line) ||
		pipedTypeArray.MatchString(line) ||
		pipedTypeBare.MatchString(line)
}

// ─── PRIMARY probe: the shipped hook's rendered output ────────────────────────

func pluginRootDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "plugins", "polyforge")
	if _, err := os.Stat(filepath.Join(dir, "hooks", "pf-skill-router")); err != nil {
		// Fatal, never Skip. A gate that goes green because it could not find the thing
		// it checks is the same silent success this work item exists to remove.
		t.Fatalf("polyforge plugin tree not found at %s: %v", dir, err)
	}
	return dir
}

// routedSkills parses the skill names out of pf-skill-router's TARGETS dict, so adding a
// routed skill to the hook automatically widens this gate instead of quietly escaping it.
func routedSkills(t *testing.T, pluginRoot string) []string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(pluginRoot, "hooks", "pf-skill-router"))
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	s := string(src)
	start := strings.Index(s, "TARGETS = {")
	if start < 0 {
		t.Fatal("hooks/pf-skill-router no longer contains a `TARGETS = {` dict — this gate " +
			"enumerates routed skills from it and cannot silently degrade to checking none")
	}
	// Keys are the only top-level `"name": (` entries in the dict.
	keyRe := regexp.MustCompile(`(?m)^\s{4}"([a-z0-9-]+)":\s*\(`)
	var out []string
	for _, m := range keyRe.FindAllStringSubmatch(s[start:], -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatal("parsed zero skills out of TARGETS — the gate would check nothing")
	}
	sort.Strings(out)
	return out
}

// renderHook runs the real hook exactly as the harness does: payload on stdin,
// CLAUDE_PLUGIN_ROOT pointing at the plugin tree. Returns the injected context.
func renderHook(t *testing.T, pluginRoot, skill string) string {
	t.Helper()
	hook := filepath.Join(pluginRoot, "hooks", "pf-skill-router")
	payload := fmt.Sprintf(`{"tool_name":"Skill","tool_input":{"skill":"polyforge:%s"}}`, skill)

	cmd := exec.Command("bash", hook)
	cmd.Stdin = strings.NewReader(payload)
	cmd.Env = append(os.Environ(), "CLAUDE_PLUGIN_ROOT="+mustAbs(t, pluginRoot))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("hook failed for %s: %v (stderr: %s)", skill, err, stderr.String())
	}

	// The hook is FAIL-SILENT by design: missing python3, or any internal error, means
	// empty output and exit 0. That is right for production and fatal for a gate — an
	// empty render would make every assertion below vacuously true.
	if len(strings.TrimSpace(string(stdout))) == 0 {
		t.Fatalf("hook emitted nothing for %s. It is fail-silent (no python3? missing "+
			"fragment?), so this gate cannot tell a clean render from no render at all "+
			"(stderr: %s)", skill, stderr.String())
	}

	var out struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(stdout, &out); err != nil {
		t.Fatalf("hook output for %s is not the expected JSON: %v\n%s", skill, err, stdout)
	}
	ctx := out.HookSpecificOutput.AdditionalContext
	if ctx == "" {
		t.Fatalf("hook produced no additionalContext for %s", skill)
	}
	return ctx
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("abs(%s): %v", p, err)
	}
	return abs
}

var pfRecallCallRe = regexp.MustCompile(`pf_recall\([^)]*\)`)

// TestRoutedSkillHook_RendersNoPipedType is the gate that the source-only version could
// not be: it asserts on the artifact the model receives.
func TestRoutedSkillHook_RendersNoPipedType(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	skills := routedSkills(t, pluginRoot)
	t.Logf("routed skills parsed from TARGETS: %v", skills)

	for _, skill := range skills {
		t.Run(skill, func(t *testing.T) {
			ctx := renderHook(t, pluginRoot, skill)

			// Coverage guard: if the injected body stops containing a pf_recall call at
			// all, the pipe assertion below becomes vacuous and we must hear about it.
			calls := pfRecallCallRe.FindAllString(ctx, -1)
			if len(calls) == 0 {
				t.Fatalf("no pf_recall call in the rendered body for %s — either the "+
					"Memory-First recall was removed (a separate problem) or this gate "+
					"has stopped covering anything", skill)
			}
			t.Logf("rendered pf_recall calls for %s:", skill)
			for _, c := range calls {
				t.Logf("    %s", c)
			}

			// No unsubstituted placeholder may survive into the model's context.
			if strings.Contains(ctx, "@@") {
				t.Errorf("%s: an @@…@@ placeholder survived substitution into the "+
					"rendered body", skill)
			}

			for _, c := range calls {
				if strings.Contains(c, "|") {
					t.Errorf("%s: the RENDERED pf_recall carries a '|' in its type "+
						"argument, which the server rejects with a 400 (aihub#289) — so "+
						"every step of this skill would hard-fail its Memory-First "+
						"recall:\n    %s", skill, c)
				}
			}
			for _, line := range strings.Split(ctx, "\n") {
				if pipedTypeMatch(line) {
					t.Errorf("%s: rendered body carries a piped type value:\n    %s",
						skill, strings.TrimSpace(line))
				}
			}
		})
	}
}

// TestRoutedSkillHook_RendersAnArrayTypeFilter pins the POSITIVE shape, not just the
// absence of a pipe. Without it, a substitution that emitted an empty string, or dropped
// the type filter entirely, would satisfy the test above.
func TestRoutedSkillHook_RendersAnArrayTypeFilter(t *testing.T) {
	pluginRoot := pluginRootDir(t)
	for _, skill := range routedSkills(t, pluginRoot) {
		t.Run(skill, func(t *testing.T) {
			ctx := renderHook(t, pluginRoot, skill)
			for _, c := range pfRecallCallRe.FindAllString(ctx, -1) {
				if !strings.Contains(c, "type=") {
					continue
				}
				if !regexp.MustCompile(`type=\[\s*"`).MatchString(c) {
					t.Errorf("%s: rendered pf_recall's type is not a JSON array of "+
						"quoted names — the router must substitute a list, and the "+
						"template must not quote the slot:\n    %s", skill, c)
				}
			}
		})
	}
}

// ─── SECONDARY probe: source sweep ────────────────────────────────────────────

// gateWalkRoots are the trees swept for piped type values, relative to internal/cli.
// plugins/ is swept with NO extension filter — the producer this gate originally missed
// was an extensionless shell/python hook.
var gateWalkRoots = []struct {
	path   string
	mdOnly bool
}{
	{filepath.Join("..", "..", "plugins"), false},
	{filepath.Join("..", "..", "docs"), true},
	{filepath.Join("..", "..", "tests", "scenarios"), true},
}

// gateExclusions are paths the sweep skips, each with the reason. An exclusion without a
// reason is how a gate rots into decoration.
var gateExclusions = map[string]string{
	// A pre-implementation design contract whose own header says parts have drifted and
	// that the CODE is authoritative ("以代码为准"), with an errata table recording each
	// divergence. Its piped examples are historical record, not guidance; the errata
	// table carries a row for this one rather than the body being rewritten.
	filepath.Join("..", "..", "docs", "design", "polyforge-v1-design.md"): "historical design record, superseded by code; drift tracked in its own errata table",
}

func TestSkillTemplates_NoPipedTypeStrings(t *testing.T) {
	pluginRootDir(t) // fail fast if the tree is missing

	var offenders []string
	scanned := 0
	for _, root := range gateWalkRoots {
		if _, err := os.Stat(root.path); err != nil {
			t.Fatalf("gate walk root %s missing: %v — the sweep would silently cover less "+
				"than it claims", root.path, err)
		}
		err := filepath.Walk(root.path, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if info.Name() == ".git" || info.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if _, skip := gateExclusions[path]; skip {
				return nil
			}
			if root.mdOnly && !strings.HasSuffix(path, ".md") {
				return nil
			}
			b, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if bytes.IndexByte(b[:min(len(b), 512)], 0) >= 0 {
				return nil // binary
			}
			scanned++
			for i, line := range strings.Split(string(b), "\n") {
				if pipedTypeMatch(line) {
					offenders = append(offenders,
						fmt.Sprintf("%s:%d: %s", path, i+1, strings.TrimSpace(line)))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root.path, err)
		}
	}
	t.Logf("swept %d text files across %d roots (%d documented exclusions)",
		scanned, len(gateWalkRoots), len(gateExclusions))
	if scanned < 50 {
		t.Errorf("only %d files swept — the walk has collapsed and this gate is no longer "+
			"covering the tree it claims to", scanned)
	}

	if len(offenders) > 0 {
		t.Errorf("a `type=` value contains '|', which the server rejects (aihub#289): '|' "+
			"is not a separator, so the whole string is sent as one type name and matches "+
			"nothing. Pass an array instead — type=[\"a.b\",\"c.*\"]. Offenders:\n%s",
			strings.Join(offenders, "\n"))
	}
}

// TestPipedTypeMatch_Discriminates is the two-way control. Without it the sweep passes
// just as happily on a tree where the patterns never match anything, including if a
// regexp is broken.
func TestPipedTypeMatch_Discriminates(t *testing.T) {
	mustMatch := map[string]string{
		"the pf-recall template form":    `pf_recall(project=<current>, query=<wi.goal>, type="methodology.spec|methodology.plan|fact.*", top_k=8)`,
		"the memory-first fragment form": `pf_recall(project=<current>, query=<user_intent>, type="experience.*|rule.*", top_k=5)`,
		"the pf_remember form":           `pf_remember(type="experience.*|fact.*|rule.*", project=<current>)`,
		"the RENDERED hook output (B1)":  `pf_recall(project=<current>, query=<wi.goal>, type="experience.*|rule.*", top_k=5)`,
		"a piped entry inside an array":  `pf_recall(type=["rule.work|fact.test"])`,
		"an unquoted piped value":        `pf_recall(type=experience.*|rule.*, top_k=5)`,
		"spaces around the equals sign":  `  type = "a|b"`,
	}
	for name, s := range mustMatch {
		if !pipedTypeMatch(s) {
			t.Errorf("detector missed %s, so the sweep proves nothing: %s", name, s)
		}
	}

	mustNotMatch := map[string]string{
		"the fixed array form":            `pf_recall(project=<current>, type=["experience.*","rule.*"], top_k=5)`,
		"the rendered array form":         `pf_recall(project=<current>, query=<wi.goal>, type=["experience.*","rule.*"], top_k=5)`,
		"a concrete-type placeholder":     `pf_remember(type=<ONE concrete type — e.g. experience.pitfall / rule.work>)`,
		"a pick-one placeholder":          `pf_remember(type=<experience.*|fact.*|rule.*>)`,
		"pf-user's choice notation":       `  user_type=<"human"|"machine", default: "human">,`,
		"a markdown table row":            `| type | description |`,
		"a single concrete type":          `type="experience.pitfall"`,
		"prose vocabulary enumeration":    `experience.approach|code|debug|pitfall — pick the one that fits`,
		"a declared_resources JSON entry": `{"type": "path", "uri": "file:internal/x.go"}`,
		"the unsubstituted placeholder":   `pf_recall(project=<current>, query=<wi.goal>, type=@@RECALL_TYPE@@, top_k=5)`,
	}
	for name, s := range mustNotMatch {
		if pipedTypeMatch(s) {
			t.Errorf("detector false-positived on %s, which would make the gate "+
				"unmaintainable: %s", name, s)
		}
	}
}
