package main

// This file is the shell-suite face of the aihub#508 asserted gate (aihub#535).
//
// # The blind spot it closes
//
// asserted.go verifies every `--- PASS:` assertion in ci.yml against the Go
// test tree, because a renamed subtest used to leave ci.yml's want-lists rotten
// until a push went red on the runner (aihub#416). But ci.yml gates a second
// population the same way: the shell suites under plugins/polyforge/tests/.
// Their steps (the aihub#305 launcher gates, the aihub#513 skill-router gate,
// and six more) run `bash <suite>.test.sh | tee <log>` and then assert
// `grep -qF -- "PASS: <check name>" <log>` over a want-list — a DIFFERENT
// marker spelling from `--- PASS:`, so checkAssertedNames cannot see it, and a
// renamed shell check rots its ci.yml assertion exactly the way the aihub#416
// subtest did: green everywhere locally, red only on the runner.
//
// This file collects those assertions and verifies each one against the suite
// file it greps the output of. The suites are shell (some with embedded python
// here-documents), not Go, so there is no AST to enumerate — what CAN be read
// statically is the check-description literals at the emission sites, with
// `$var`-style expansions treated as gaps. That is the same over-approximation
// direction asserted.go takes for `"prefix_"+tc.name` concatenations: a gap can
// only make this gate ACCEPT a name it should have questioned, never invent a
// failure, which is the direction to err in for a gate that fails the build.
// `for var in <literal words>` loops are folded first (mirroring the want-list
// folding in collectPassAssertions), so a want naming a loop arm the list does
// not produce is still caught concretely.
//
// # The floor, and why it is audited here (aihub#320's shape)
//
// Each suite step also keeps a PASS-count floor: `n=$(grep -c '^  PASS: ' …)`
// then `[ "$n" -ge 57 ]`. That integer is the exact shape aihub#320 retired
// from the DB gate (`-min-gated N`): a bare number nothing re-verifies, which
// conflicts between branches, goes stale as main moves, and cannot see a swap.
// aihub#320's resolution was not to delete the number but to make a MEASUREMENT
// disagree loudly the moment the number stops being true. The measurement
// available here is static: this file enumerates the emission sites the count
// pattern could match, with their loop multiplicities, and fails when the floor
// exceeds what the suite can still emit — the state where the step is red on
// the runner while everything is green locally. A floor BELOW the maximum is
// left alone: ci.yml documents its floors as ratchets, not exact counts.
//
// # What it does and does not prove
//
// It proves the asserted names are printable by the suite and the floors are
// satisfiable. It does NOT execute the suites, so a check that exists but
// FAILS, a broken `tee`, or a suite that exits early is still only visible on
// the runner. Named residual holes: an emission produced by something other
// than `echo` or a helper built on it (a `printf`, a `cat` of a fixture) makes
// the floor unverifiable rather than miscounted; python here-documents are
// opaque text, so a check they emit is matched against their source lines
// (wildcarding `%s`/`{x}` holes) and any here-document that mentions the count
// marker makes that floor unverifiable; and a want that matches only through
// an expansion gap is accepted, so a rename is only guaranteed red where the
// name is literal — which is what every want-list in ci.yml asserts on today.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// shellCheckMarker is the prefix the shell suites print for a passing check
// (some indent it; the ci.yml wants never include the indent because they are
// matched with `grep -F` as substrings).
const shellCheckMarker = "PASS: "

// ShellSuiteInvocation is one `bash <suite>.test.sh … | tee <log>` in a step.
type ShellSuiteInvocation struct {
	Step string
	Line int
	// Path is the suite file as written in ci.yml, relative to the repo root.
	Path string
	// Log is the tee target its output lands in, or "" when nothing captures it
	// (such a step asserts nothing, so there is nothing here to verify).
	Log string
}

// ShellWant is one positive `grep -q…` assertion over a shell suite's log,
// expanded from its `for want in …` list.
type ShellWant struct {
	Step string
	Line int
	Text string
	Log  string
	// Want is the pattern with the loop variable substituted. All of ci.yml's
	// shell-suite assertions are `grep -qF` literals; a non-F pattern carrying
	// regexp metacharacters is reported as unreadable rather than guessed at.
	Want  string
	Fixed bool
}

// ShellFloor is one `n=$(grep -c '<pattern>' <log>)` + `[ "$n" -ge N ]` pair.
type ShellFloor struct {
	Step    string
	Line    int
	Text    string
	Log     string
	Pattern string
	Min     int
}

var (
	// shellSuiteInvRE recognises the one spelling ci.yml uses to run a shell
	// suite. Anchoring on `bash ` keeps prose mentions of a .test.sh out.
	shellSuiteInvRE = regexp.MustCompile(`(?:^|\s)bash\s+((?:[\w.@-]+/)*[\w.@-]+\.test\.sh)(?:\s|$)`)
	// shellWantChainRE is the accepted spelling of a shell-suite assertion: one
	// or more `grep -q… <pattern> <log>` alternatives `||`-chained into a
	// non-zero exit. Same allowlist reasoning as passChainRE, one difference:
	// the `--` is optional because ci.yml's shell steps write both forms.
	shellWantChainRE = regexp.MustCompile(
		`^((?:grep\s+-q[A-Za-z]*\s+(?:--\s+)?(?:'[^']*'|"[^"]*")\s+\S+\s*\|\|\s*)+)` +
			`(?:exit\s+[1-9][0-9]*|\{\s*(?:[^{}]*;\s*)?exit\s+[1-9][0-9]*\s*;?\s*\})$`)
	shellWantGrepRE = regexp.MustCompile(`grep\s+-q([A-Za-z]*)\s+(?:--\s+)?(?:'([^']*)'|"([^"]*)")\s+(\S+)`)
	// shellCountRE is the count-capture spelling: `n=$(grep -c '<pat>' <log>)`,
	// with or without `--` and a `|| true` tail.
	shellCountRE = regexp.MustCompile(
		`^([A-Za-z_][A-Za-z0-9_]*)=\$\(\s*grep\s+-c([A-Za-z]*)\s+(?:--\s+)?(?:'([^']*)'|"([^"]*)")\s+(\S+)\s*(?:\|\|\s*true\s*)?\)$`)
	// shellFloorRE is the comparison that turns a captured count into a gate.
	shellFloorRE = regexp.MustCompile(
		`^\[\s+"\$\{?([A-Za-z_][A-Za-z0-9_]*)(?::-0)?\}?"\s+-ge\s+([0-9]+)\s+\]\s*\|\|\s*` +
			`(?:exit\s+[1-9][0-9]*|\{\s*(?:[^{}]*;\s*)?exit\s+[1-9][0-9]*\s*;?\s*\})$`)
)

// collectShellSuiteData extracts, from one step's script, the shell-suite
// invocations plus every want assertion and count floor aimed at their logs.
// Called from ParseWorkflow so the scan carries the shell side next to the Go
// side; it is a no-op for steps that run no suite.
func collectShellSuiteData(sc *shellScript, stepName string, scan *WorkflowScan) {
	// Pass 1: the invocations, so the logs are known before the assertions are
	// read (ci.yml writes them in that order, but nothing should depend on it).
	suiteLogs := map[string]bool{}
	for _, cmd := range sc.Commands {
		if cmd.Quote != 0 {
			continue
		}
		m := shellSuiteInvRE.FindStringSubmatch(cmd.Text)
		if m == nil {
			continue
		}
		inv := ShellSuiteInvocation{Step: stepName, Line: cmd.Line, Path: m[1]}
		if t := teeRE.FindStringSubmatch(cmd.Text); t != nil {
			inv.Log = t[1]
		}
		scan.ShellSuites = append(scan.ShellSuites, inv)
		if inv.Log != "" && inv.Log != "/dev/null" {
			suiteLogs[inv.Log] = true
		}
	}
	if len(suiteLogs) == 0 {
		return
	}

	// Pass 2: assertions, counts and floors, with the same loop bookkeeping
	// collectPassAssertions uses (a want-list is a `for want in …` here too).
	countPatterns := map[string]ShellFloor{} // count variable -> pattern+log, floor still unset
	var loops []loopScope
	for i, ln := range sc.Lines {
		text := strings.TrimSpace(ln.Text)
		if text != "" && ln.Quote == 0 {
			switch {
			case shellWantChainRE.MatchString(text) && !exitZeroRE.MatchString(text):
				chain := shellWantChainRE.FindStringSubmatch(text)[1]
				collectShellWants(chain, text, stepName, i, loops, suiteLogs, scan)
			case shellCountRE.MatchString(text):
				m := shellCountRE.FindStringSubmatch(text)
				pat := m[3]
				if pat == "" {
					pat = m[4]
				}
				if suiteLogs[m[5]] {
					countPatterns[m[1]] = ShellFloor{Step: stepName, Line: i, Text: text, Log: m[5], Pattern: pat}
				}
			case shellFloorRE.MatchString(text):
				m := shellFloorRE.FindStringSubmatch(text)
				if f, ok := countPatterns[m[1]]; ok {
					n := 0
					_, _ = fmt.Sscanf(m[2], "%d", &n)
					f.Min = n
					f.Line = i
					f.Text = text
					scan.ShellFloors = append(scan.ShellFloors, f)
				}
			default:
				// A line that greps a suite log in a spelling this file cannot
				// read is an unchecked assertion, and "dbtestcov did not
				// understand it" must not look like "it is fine". Negated
				// guards (`! grep -q '^SKIP:' …`) assert absence — nothing
				// there can rot into a stale name — and the tee line is the
				// invocation itself.
				if strings.Contains(text, "grep") && !strings.HasPrefix(text, "!") &&
					!strings.Contains(text, ".test.sh") && namesALog(text, suiteLogs) {
					scan.ShellProblems = append(scan.ShellProblems, fmt.Sprintf(
						"step %q, line %d greps a shell-suite log in a form dbtestcov cannot read, so whatever it asserts "+
							"is not verified against the suite. Spell it `grep -qF -- \"$want\" <log> || exit 1` (a "+
							"`|| { echo …; exit 1; }` tail is fine) or `n=$(grep -c '<pattern>' <log>)`: %s",
						stepName, i, capLine(text)))
				}
			}
		}
		// Loop bookkeeping, after the line is read (a `for` line does not scope
		// itself) — same shape as collectPassAssertions.
		if ln.Text == "" || ln.Quote != 0 {
			continue
		}
		parts, _ := splitUnquoted(ln.Text, ln.Quote)
		for _, p := range parts {
			if p.quote != 0 {
				continue
			}
			t := strings.TrimSpace(p.text)
			switch {
			case t == "done" && len(loops) > 0:
				loops = loops[:len(loops)-1]
			case strings.HasPrefix(t, "for "):
				m := forInRE.FindStringSubmatch(t)
				if m == nil {
					continue
				}
				words, err := shellWords(m[2])
				if err != nil {
					scan.ShellProblems = append(scan.ShellProblems, fmt.Sprintf(
						"step %q, line %d: the `for %s in …` list cannot be read (%v), so any shell-suite assertion "+
							"inside it is unchecked: %s", stepName, i, m[1], err, capLine(t)))
					continue
				}
				loops = append(loops, loopScope{variable: m[1], values: words})
			}
		}
	}
}

func namesALog(text string, logs map[string]bool) bool {
	for log := range logs {
		re := regexp.MustCompile(`(^|\s)` + regexp.QuoteMeta(log) + `(\s|$)`)
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// collectShellWants expands one accepted grep chain into ShellWant entries,
// substituting the innermost loop variable the patterns use — the exact
// expansion parsePassLine does for the Go marker.
func collectShellWants(chain, text, stepName string, line int, loops []loopScope, suiteLogs map[string]bool, scan *WorkflowScan) {
	type alt struct {
		pattern, log string
		fixed        bool
	}
	var alts []alt
	onSuite := 0
	for _, m := range shellWantGrepRE.FindAllStringSubmatch(chain, -1) {
		pat := m[2]
		if pat == "" {
			pat = m[3]
		}
		a := alt{pattern: pat, log: m[4], fixed: strings.Contains(m[1], "F")}
		if suiteLogs[a.log] {
			onSuite++
		}
		alts = append(alts, a)
	}
	if onSuite == 0 {
		return // not about a suite log; the Go-marker collector owns the rest
	}
	if onSuite != len(alts) {
		scan.ShellProblems = append(scan.ShellProblems, fmt.Sprintf(
			"step %q, line %d chains a shell-suite log grep together with one on another log; dbtestcov cannot tell "+
				"which alternative is supposed to hold, so neither is checked: %s", stepName, line, capLine(text)))
		return
	}

	variable, values := "", []string{""}
	for _, a := range alts {
		for i := len(loops) - 1; i >= 0; i-- {
			if strings.Contains(a.pattern, "$"+loops[i].variable) ||
				strings.Contains(a.pattern, "${"+loops[i].variable+"}") {
				variable, values = loops[i].variable, loops[i].values
				break
			}
		}
		if variable != "" {
			break
		}
	}
	for _, v := range values {
		for _, a := range alts {
			pat := a.pattern
			if variable != "" {
				pat = strings.ReplaceAll(pat, "${"+variable+"}", v)
				pat = strings.ReplaceAll(pat, "$"+variable, v)
			}
			if shellExpansionRE.MatchString(pat) {
				scan.ShellProblems = append(scan.ShellProblems, fmt.Sprintf(
					"step %q, line %d: the shell-suite assertion pattern %q still holds a shell expansion after "+
						"substitution, so dbtestcov cannot tell what it asserts. Write the name out, or put it in a "+
						"`for … in` list: %s", stepName, line, pat, capLine(text)))
				continue
			}
			scan.ShellWants = append(scan.ShellWants, ShellWant{
				Step: stepName, Line: line, Text: text, Log: a.log, Want: pat, Fixed: a.fixed})
		}
	}
}

// ------------------------------------------------------------ the suite model

// wildcardRE matches the holes in a suite line: shell expansions and the
// python format/f-string holes the here-document blocks use. Everything it
// matches becomes a gap that absorbs anything, which can only widen what a
// want is allowed to match — the safe direction.
var wildcardRE = regexp.MustCompile(
	`\$\{[^}]*\}|\$\([^)]*\)|\$[A-Za-z_][A-Za-z0-9_]*|\$[0-9@*#?!-]|%\([^)]*\)[sdrif]|%[-0-9.]*[sdrifxX]|\{[^{}\s]*\}`)

// suiteLine is one folded line of a suite file with its shell context.
type suiteLine struct {
	// text is the shell-parsed text: trimmed, comments stripped, here-document
	// bodies blanked. raw keeps the original folded line, which is where the
	// python blocks' literals live.
	text, raw string
	heredoc   bool
	// mult is the product of the enclosing foldable `for` lists, or -1 when an
	// enclosing loop's trip count is not statically known (while/until, or a
	// `for` over something that is not a literal word list).
	mult int
	// inFunc names the enclosing shell function definition — including on the
	// definition's own line, so a single-line helper's `echo` is attributed to
	// the helper and not to the top level.
	inFunc string
}

type suiteModel struct {
	path  string
	lines []suiteLine
	// variants are the candidate template lines a want is matched against:
	// every non-comment line, with foldable loop variables substituted (so a
	// want naming an arm the list does not produce cannot hide in a gap).
	variants []string
}

// variantCap bounds the loop-fold cross product per line. Past it the fold is
// abandoned for that line and the variables stay as gaps — wider, never red.
const variantCap = 64

var suiteFuncDefRE = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*\(\)\s*\{`)

func loadSuiteModel(path string) (*suiteModel, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path comes from the CI workflow under a CI-controlled root
	if err != nil {
		return nil, err
	}
	folded := continuation.ReplaceAllString(string(data), " ")
	sc := parseShellScript(folded)
	raw := strings.Split(folded, "\n")

	m := &suiteModel{path: path}
	var loops []loopScope // values==nil marks an unknown trip count (while/until, unfoldable for)
	inFunc := ""
	for i, ln := range sc.Lines {
		rawLine := raw[i]
		sl := suiteLine{text: strings.TrimSpace(ln.Text), raw: rawLine, inFunc: inFunc, mult: 1}
		if sl.text == "" && strings.TrimSpace(rawLine) != "" &&
			!strings.HasPrefix(strings.TrimSpace(rawLine), "#") && ln.Quote == 0 {
			// Blanked but not a comment: a here-document body (python, usually).
			sl.heredoc = true
		}
		for _, l := range loops {
			if l.values == nil {
				sl.mult = -1
				break
			}
			sl.mult *= len(l.values)
		}
		// A definition line belongs to the function it defines, whether the
		// body closes on the same line or opens a region.
		openedHere := false
		if sl.text != "" && ln.Quote == 0 && inFunc == "" {
			if fd := suiteFuncDefRE.FindStringSubmatch(sl.text); fd != nil {
				sl.inFunc = fd[1]
				if !strings.HasSuffix(sl.text, "}") {
					inFunc = fd[1]
					openedHere = true
				}
			}
		}
		m.lines = append(m.lines, sl)

		// Variants for the want matcher.
		switch {
		case sl.heredoc:
			m.variants = append(m.variants, rawLine)
		case sl.text != "":
			m.variants = append(m.variants, foldVariants(sl.text, loops)...)
		}

		// Bookkeeping after the line: loop frames and the definition region's
		// close (the suites close a multi-line definition with a lone `}`).
		if sl.text == "" || ln.Quote != 0 {
			continue
		}
		if inFunc != "" && !openedHere && sl.text == "}" {
			inFunc = ""
		}
		parts, _ := splitUnquoted(ln.Text, ln.Quote)
		for _, p := range parts {
			if p.quote != 0 {
				continue
			}
			t := strings.TrimSpace(p.text)
			w := firstWord(t)
			switch {
			case w == "done": // `done < file` and `done <<< "$x"` still close the loop
				if len(loops) > 0 {
					loops = loops[:len(loops)-1]
				}
			case w == "while" || w == "until":
				loops = append(loops, loopScope{})
			case strings.HasPrefix(t, "for "):
				fm := forInRE.FindStringSubmatch(t)
				if fm == nil {
					loops = append(loops, loopScope{}) // `for ((…))`: trip count unknown
					continue
				}
				words, err := shellWords(fm[2])
				if err != nil {
					// Not an error, unlike ci.yml's want-lists: the values just
					// stay unknown and the variable remains a gap.
					loops = append(loops, loopScope{variable: fm[1]})
					continue
				}
				loops = append(loops, loopScope{variable: fm[1], values: words})
			}
		}
	}
	return m, nil
}

// foldVariants substitutes the enclosing foldable loop variables into a line,
// cross-producing their values. When an enclosing variable folds, only the
// folded variants are kept — keeping the raw line too would let a want naming
// a non-existent loop arm match through the unsubstituted `$var` gap, which is
// the exact laxity the fold exists to remove.
func foldVariants(line string, scopes []loopScope) []string {
	out := []string{line}
	total := 1
	for _, sc := range scopes {
		if sc.values == nil || !mentionsVar(line, sc.variable) {
			continue
		}
		total *= len(sc.values)
		if total > variantCap {
			return []string{line}
		}
		re := regexp.MustCompile(`\$(?:` + regexp.QuoteMeta(sc.variable) + `\b|\{` + regexp.QuoteMeta(sc.variable) + `\})`)
		var next []string
		for _, v := range out {
			for _, val := range sc.values {
				next = append(next, re.ReplaceAllString(v, val))
			}
		}
		out = next
	}
	return out
}

func mentionsVar(line, name string) bool {
	return strings.Contains(line, "$"+name) || strings.Contains(line, "${"+name+"}")
}

// minWantOverlap is the least literal overlap a want may have with a template
// fragment when the rest of the want falls into a gap. One shared character
// would let almost anything match; the check names in ci.yml share dozens of
// literal characters with their emission sites, so four is far below any real
// name and far above coincidence.
const minWantOverlap = 4

// templateMatches reports whether the want could be a substring of some
// instantiation of the template line, with at least minWantOverlap characters
// landing on literal text. A want satisfied ENTIRELY by a gap proves nothing
// and is refused — otherwise one `echo "PASS: $1"` helper would satisfy every
// assertion ever written.
func templateMatches(line, want string) bool {
	frags := wildcardRE.Split(line, -1)
	if len(frags) == 1 {
		return strings.Contains(frags[0], want)
	}
	need := min(minWantOverlap, len(want))
	for i, f := range frags {
		if strings.Contains(f, want) {
			return true
		}
		// The want starts inside this fragment and runs on into the gap after
		// it: a suffix of the fragment is a prefix of the want.
		if i < len(frags)-1 {
			for k := min(len(f), len(want)-1); k >= need; k-- {
				if f[len(f)-k:] == want[:k] {
					return true
				}
			}
		}
		// The want ends inside this fragment, having started in the gap before
		// it: a prefix of the fragment is a suffix of the want.
		if i > 0 {
			for k := min(len(f), len(want)-1); k >= need; k-- {
				if f[:k] == want[len(want)-k:] {
					return true
				}
			}
		}
		// The want contains the whole fragment, entering and leaving through
		// the gaps on both sides.
		if i > 0 && i < len(frags)-1 && len(f) >= minWantOverlap && strings.Contains(want, f) {
			return true
		}
	}
	return false
}

// wantMatchesSuite decides one want against a suite. A want that carries the
// PASS marker is matched by its NAME part: several suites print the marker
// through one helper (`echo "  PASS: $3"`), and matching the full want against
// that definition would accept every name ever asserted; and one suite
// (pi-runtime) composes the marker in shell around python lines that spell it
// `PASS|`, so the marker's spelling is not reliably in the same literal as the
// name.
func wantMatchesSuite(m *suiteModel, want string) bool {
	part := want
	if idx := strings.Index(want, shellCheckMarker); idx >= 0 {
		part = want[idx+len(shellCheckMarker):]
	}
	if strings.TrimSpace(part) == "" {
		part = want // "ALL PASS" and other marker-less wants match literally
	}
	for _, v := range m.variants {
		if templateMatches(v, part) {
			return true
		}
	}
	return false
}

// --------------------------------------------------------------- floor audit

// emitInfo is what one shell function contributes to the counted output per
// call: how many matching lines, or that the number is not statically known.
type emitInfo struct {
	direct    int
	unbounded bool
	// calls maps callee -> summed call-site multiplicity; -1 when any call
	// site's multiplicity is unknown.
	calls map[string]int
}

// echoWordRE finds `echo` in command position loosely: any occurrence not glued
// into a longer word. Occurrences inside string arguments over-extract, which
// can only raise the maximum — the safe direction for a floor check.
var echoWordRE = regexp.MustCompile(`(?:^|[^A-Za-z0-9_./-])echo[ \t]+`)

// maxEmissions returns the largest number of log lines matching the anchored
// `grep -c` pattern that the suite could print, or ok=false (with the reason)
// when the suite is not statically countable for that pattern. Ambiguity about
// one SITE counts the site — a maximum may only ever be too high — but an
// unknown MULTIPLICITY cannot be bounded, and neither can text this file does
// not model (here-documents and non-echo printers that mention the marker).
func (m *suiteModel) maxEmissions(pattern string) (total int, ok bool, reason string) {
	body, exact, readable := countPatternBody(pattern)
	if !readable {
		return 0, false, fmt.Sprintf("the count pattern %q is not an anchored literal", pattern)
	}

	funcs := map[string]*emitInfo{}
	topEmits := 0
	var topCalls []map[string]int // per line: callee -> call count; multiplicity folded in below
	infoFor := func(name string) *emitInfo {
		fi := funcs[name]
		if fi == nil {
			fi = &emitInfo{calls: map[string]int{}}
			funcs[name] = fi
		}
		return fi
	}

	// Pass 1: emission sites, and the marker catch-all.
	for _, ln := range m.lines {
		if ln.heredoc {
			if strings.Contains(ln.raw, body) {
				return 0, false, "a here-document mentions the count marker, and what an embedded interpreter prints is not statically countable"
			}
			continue
		}
		if ln.text == "" {
			continue
		}
		matching, accounted := 0, 0
		for _, lit := range echoLiterals(ln.text) {
			if emissionCouldMatch(wildcardRE.Split(lit, -1), body, exact) {
				matching++
			}
			if strings.Contains(lit, body) {
				accounted++
			}
		}
		if strings.Contains(ln.text, body) && accounted == 0 {
			return 0, false, fmt.Sprintf(
				"a line mentions the count marker outside any `echo` literal, so the suite's output is not statically countable: %s",
				capLine(ln.text))
		}
		if matching == 0 {
			continue
		}
		switch {
		case ln.inFunc != "":
			fi := infoFor(ln.inFunc)
			if ln.mult == -1 {
				fi.unbounded = true
			} else {
				fi.direct += matching * ln.mult
			}
		case ln.mult == -1:
			return 0, false, "an emission site sits inside a loop whose trip count is not statically known"
		default:
			topEmits += matching * ln.mult
		}
	}

	// Pass 2: the call graph. Edges are collected against every DEFINED
	// function (a non-emitting helper may still call an emitting one), over a
	// set fixed up front — infoFor mutates funcs, so ranging funcs here would
	// make the walk depend on map iteration order.
	defined := map[string]bool{}
	for _, ln := range m.lines {
		if ln.inFunc != "" {
			defined[ln.inFunc] = true
		}
	}
	defNames := make([]string, 0, len(defined))
	for name := range defined {
		defNames = append(defNames, name)
	}
	sort.Strings(defNames)
	for _, ln := range m.lines {
		if ln.text == "" || ln.heredoc {
			continue
		}
		for _, name := range defNames {
			if name == ln.inFunc {
				continue // a definition is not a call to itself
			}
			n := countCalls(ln.text, name)
			if n == 0 {
				continue
			}
			if ln.inFunc != "" {
				fi := infoFor(ln.inFunc)
				if ln.mult == -1 {
					fi.calls[name] = -1
				} else if fi.calls[name] != -1 {
					fi.calls[name] += n * ln.mult
				}
			} else {
				entry := map[string]int{name: n * ln.mult}
				if ln.mult == -1 {
					entry[name] = -1
				}
				topCalls = append(topCalls, entry)
			}
		}
	}

	// Resolve each function's per-call emission count, cycles and unknown
	// multiplicities collapsing to "unknown".
	const unknown = -1
	memo := map[string]int{}
	visiting := map[string]bool{}
	var resolve func(name string) int
	resolve = func(name string) int {
		if v, done := memo[name]; done {
			return v
		}
		if visiting[name] {
			return unknown // recursion: no static bound
		}
		fi := funcs[name]
		if fi == nil {
			return 0
		}
		if fi.unbounded {
			memo[name] = unknown
			return unknown
		}
		visiting[name] = true
		defer delete(visiting, name)
		total := fi.direct
		for callee, mult := range fi.calls {
			sub := resolve(callee)
			if sub == 0 {
				continue
			}
			if sub == unknown || mult == -1 {
				memo[name] = unknown
				return unknown
			}
			total += mult * sub
		}
		memo[name] = total
		return total
	}

	total = topEmits
	for _, entry := range topCalls {
		for callee, mult := range entry {
			sub := resolve(callee)
			if sub == 0 {
				continue
			}
			if sub == unknown || mult == -1 {
				return 0, false, fmt.Sprintf("a call to %s sits where its emission count is not statically known", callee)
			}
			total += mult * sub
		}
	}
	return total, true, ""
}

// countPatternBody reads a `grep -c` pattern of the shape the suite steps use:
// `^<literal>` or `^<literal>$`. Anything else — unanchored, or carrying live
// regexp metacharacters — is not modelled.
func countPatternBody(pattern string) (body string, exact, ok bool) {
	if !strings.HasPrefix(pattern, "^") {
		return "", false, false
	}
	body = strings.TrimPrefix(pattern, "^")
	if strings.HasSuffix(body, "$") {
		body, exact = strings.TrimSuffix(body, "$"), true
	}
	if body == "" || strings.ContainsAny(body, `.*[]\^$`) {
		return "", false, false
	}
	return body, exact, true
}

// emissionCouldMatch reports whether the echoed literal could print a line the
// anchored pattern counts. `body startsWith frags[0]` covers the site whose
// short leading literal leaves the answer open (`echo "  $verdict: $n"`) —
// counted, because a maximum may only ever be too high. A literal that STARTS
// with a hole (`echo "$fails CHECK(S) FAILED"`) is not counted: it carries no
// marker evidence of its own, and the code that could put the marker into that
// variable is caught where the marker is literal — an assignment or heredoc
// trips the catch-all in maxEmissions, and data read in a loop trips the
// unknown-trip-count rule.
func emissionCouldMatch(frags []string, body string, exact bool) bool {
	if frags[0] == "" {
		return false
	}
	if exact && len(frags) == 1 {
		return frags[0] == body
	}
	return strings.HasPrefix(frags[0], body) || strings.HasPrefix(body, frags[0])
}

// echoLiterals extracts the text each `echo` on the line prints, holes kept as
// `$…` for wildcardRE. Quoting is honoured; redirections end the argument
// list. Any printer this cannot read stays invisible here and is caught by the
// marker catch-all in maxEmissions instead.
func echoLiterals(text string) []string {
	var out []string
	for _, loc := range echoWordRE.FindAllStringIndex(text, -1) {
		rest := text[loc[1]:]
		for {
			trimmed := strings.TrimLeft(rest, " \t")
			if w := firstWord(trimmed); w == "-n" || w == "-e" || w == "-ne" || w == "-en" {
				rest = strings.TrimPrefix(trimmed, w)
				continue
			}
			rest = trimmed
			break
		}
		var b strings.Builder
		var quote byte
	scan:
		for i := 0; i < len(rest); i++ {
			c := rest[i]
			switch {
			case quote == '\'':
				if c == '\'' {
					quote = 0
				} else {
					b.WriteByte(c)
				}
			case quote == '"':
				if c == '"' {
					quote = 0
				} else if c == '\\' && i+1 < len(rest) {
					i++
					b.WriteByte(rest[i])
				} else {
					b.WriteByte(c)
				}
			case c == '\'' || c == '"':
				quote = c
			case c == ';' || c == '|' || c == '&' || c == '>' || c == '<' || c == '#' || c == ')' || c == '}':
				break scan
			case c == '\\' && i+1 < len(rest):
				i++
				b.WriteByte(rest[i])
			default:
				b.WriteByte(c)
			}
		}
		out = append(out, strings.TrimRight(b.String(), " \t"))
	}
	return out
}

// countCalls counts word-boundary occurrences of a helper name in a line. A
// name inside a longer word does not count; one inside a string argument does,
// which can only over-count — and an over-counted maximum only weakens the
// floor check, never falsifies it.
func countCalls(text, name string) int {
	n := 0
	for i := 0; i+len(name) <= len(text); {
		j := strings.Index(text[i:], name)
		if j < 0 {
			break
		}
		start := i + j
		end := start + len(name)
		leftOK := start == 0 || !isWordByte(text[start-1])
		rightOK := end == len(text) || !isWordByte(text[end])
		if leftOK && rightOK {
			n++
		}
		i = start + 1
	}
	return n
}

func isWordByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// ------------------------------------------------------------------ verdicts

// checkShellSuites verifies the collected shell-suite assertions and floors
// against the suite files themselves. Returns the problems and the progress
// lines for the report.
func checkShellSuites(scan *WorkflowScan, sourceRoot string) (problems, reportLines []string, err error) {
	models := map[string]*suiteModel{}
	byLog := map[string]*suiteModel{}
	pathOf := map[string]string{}
	for _, inv := range scan.ShellSuites {
		if inv.Log == "" || inv.Log == "/dev/null" {
			continue
		}
		m, seen := models[inv.Path]
		if !seen {
			m, err = loadSuiteModel(filepath.Join(sourceRoot, filepath.FromSlash(inv.Path)))
			if err != nil {
				problems = append(problems, fmt.Sprintf(
					"step %q runs %s and asserts on its output, but the suite cannot be read (%v) — the assertions "+
						"then name checks nothing can verify", inv.Step, inv.Path, err))
				m = nil
			}
			models[inv.Path] = m
		}
		if m == nil {
			continue
		}
		byLog[inv.Step+"\x00"+inv.Log] = m
		pathOf[inv.Step+"\x00"+inv.Log] = inv.Path
	}

	asserted := map[string]bool{}
	for _, w := range scan.ShellWants {
		key := w.Step + "\x00" + w.Log
		m := byLog[key]
		if m == nil {
			continue // unreadable suite, already reported
		}
		asserted[key] = true
		if !w.Fixed && strings.ContainsAny(w.Want, `.*[]\^$`) {
			problems = append(problems, fmt.Sprintf(
				"step %q, line %d of its `run:` block asserts the pattern %q on %s without -F; regexp patterns over a "+
					"shell suite's output are not modelled — use `grep -qF` and a literal: %s",
				w.Step, w.Line, w.Want, w.Log, capLine(w.Text)))
			continue
		}
		if !wantMatchesSuite(m, w.Want) {
			problems = append(problems, fmt.Sprintf(
				"step %q, line %d of its `run:` block asserts %q in %s, and no line of %s can print it — the check was "+
					"renamed or deleted while this assertion stayed behind, the shell-suite face of the aihub#416 rot. "+
					"Fix the name here (or restore the check); do NOT delete the assertion to go green",
				w.Step, w.Line, w.Want, w.Log, pathOf[key]))
		}
	}

	// A captured suite log nothing asserts on is the state a stale want-list
	// can be DELETED into — the same escape hatch the Go side closes with
	// scan.Unasserted, so it is closed here too.
	for _, inv := range scan.ShellSuites {
		if inv.Log == "" || inv.Log == "/dev/null" || byLog[inv.Step+"\x00"+inv.Log] == nil {
			continue
		}
		if !asserted[inv.Step+"\x00"+inv.Log] {
			problems = append(problems, fmt.Sprintf(
				"step %q tees %s to %s but asserts none of its %q lines, so nothing records WHICH checks ran; add a "+
					"`for want in …` list of `grep -qF` assertions like the other suite steps",
				inv.Step, inv.Path, inv.Log, strings.TrimSpace(shellCheckMarker)))
		}
	}

	for _, f := range scan.ShellFloors {
		key := f.Step + "\x00" + f.Log
		m := byLog[key]
		if m == nil {
			continue
		}
		maxN, ok, reason := m.maxEmissions(f.Pattern)
		if !ok {
			reportLines = append(reportLines, fmt.Sprintf(
				"dbtestcov: shell-suite floor %d on %q in %s is not statically checkable (%s); it is enforced on the runner only\n",
				f.Min, f.Pattern, pathOf[key], reason))
			continue
		}
		if f.Min > maxN {
			problems = append(problems, fmt.Sprintf(
				"step %q keeps a PASS-count floor of %d on %q, but %s can only print %d such line(s) — the step can no "+
					"longer go green on the runner. If checks were removed deliberately, lower the floor in the same "+
					"change so the diff records the coverage given up; if not, restore the checks",
				f.Step, f.Min, f.Pattern, pathOf[key], maxN))
			continue
		}
		reportLines = append(reportLines, fmt.Sprintf(
			"dbtestcov: shell-suite floor %d on %q ok: %s can print up to %d\n", f.Min, f.Pattern, pathOf[key], maxN))
	}

	sort.Strings(problems)
	return problems, reportLines, nil
}
