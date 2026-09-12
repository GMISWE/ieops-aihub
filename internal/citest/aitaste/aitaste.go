// Package aitaste is the aihub#378 gate over user-visible product copy.
//
// It bans, in the strings a user or an agent actually reads, the few
// typographic markers that the aihub#373 measurement found at ~zero frequency
// in human-written text of the same genre: the em dash (U+2014, one code
// point), unicode arrows, and emoji/status symbols. It deliberately does NOT
// ban semicolons, CJK/Latin spacing, bold ratios, the ASCII hyphen, the en
// dash (U+2013), U+2015, U+FF0D, or ASCII arrows (`->`, `=>`): aihub#373
// measured all of those as present in human writing, and flagging them would
// be a false positive by the gate's own admission standard ("humans almost
// never write this", not "humans write this less").
//
// Scanned surfaces (the aihub#373 operationalization of "user-visible"):
//
//   - String literals in non-test Go files under internal/server,
//     internal/domain, internal/coding and internal/cli (error messages,
//     notifications, CLI output). Comments are not scanned: they are
//     developer-facing, not product copy.
//   - Visible text in internal/server/templates/**/*.tmpl, with template
//     comments, {{...}} actions, HTML comments, script/style blocks and tag
//     markup masked out. HTML entities are decoded first, so writing &mdash;
//     instead of the literal character does not dodge the gate.
//
// TEMPORARY surface exclusions (both must eventually be deleted):
//
//   - internal/mcp: its 135 occurrences (tool and parameter descriptions) are
//     aihub#626. Removing them moves description_sha256 on nearly every
//     contract card, so that sweep carries the card regen and the hot-file
//     protocol with it. When aihub#626 lands, add "internal/mcp" to
//     goSurfaceDirs and delete this bullet.
//   - plugins/**: skill markdown hits on ~100% of files, but any plugins/**
//     change forces a marketplace version bump, i.e. a release, and releases
//     are batched. That sweep needs its own work item (not yet filed). When it
//     lands, add a markdown surface for plugins/** and delete this bullet.
//
// Exemptions are per occurrence, never per file or per directory. The
// directive
//
//	aitaste:allow U+XXXX [xN] [U+YYYY [xM] ...]: reason
//
// sits in a comment on the same line as the occurrence (or, when that line
// cannot carry one, on the line directly above), names the exact code points
// it covers and how many of each (default one), and must give a reason. A
// directive that covers nothing fails the gate: a stale exemption is deleted,
// not left around for the next occurrence to inherit. The gate prints how
// many exemptions are in effect on every run, so the number is visible when
// it only ever goes up.
//
// Known boundaries, inherited from the aihub#373 measurement: copy assembled
// at runtime (fmt.Sprintf("%c", 0x2014), string concatenation of variables)
// and text produced inside masked template actions are not visible to a
// static scan.
package aitaste

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// goSurfaceDirs are the Go packages whose string literals are product copy.
// internal/mcp is DELIBERATELY absent; see the package comment (temporary,
// tracked as aihub#626).
var goSurfaceDirs = []string{
	"internal/server",
	"internal/domain",
	"internal/coding",
	"internal/cli",
}

// templateDir holds the server-rendered HTML templates.
const templateDir = "internal/server/templates"

// TemporarySurfaceExclusions is printed by the gate on every run so the two
// carve-outs stay visible until they are deleted.
var TemporarySurfaceExclusions = []string{
	"internal/mcp: TEMPORARY, until aihub#626 lands (135 occurrences in tool/param descriptions plus the contract-card regen they force)",
	"plugins/**: TEMPORARY, release-gated (any change there forces a marketplace version bump); follow-up work item not yet filed",
}

// Classify names the ban class of r, or returns "" for a rune the gate does
// not care about. The classes are copied from the aihub#373 v3 instrument
// (redo_all.py), where each had a human-written negative control at ~zero:
//
//   - em-dash: exactly U+2014. 0 occurrences in 1,257,497 characters of
//     pre-2021 human commit text. U+2013/U+2015/U+FF0D are NOT banned; humans
//     use them, and widening the class was called out as a false-positive
//     trap.
//   - arrow: the Unicode arrow blocks plus the three dingbat arrows. All
//     three human long-document controls measured 0.000/1k.
//   - emoji-symbol: emoji and status/symbol blocks (checkmarks live here).
//     Human hand-typed chat control: 0.0% incidence. U+FE0F (variation
//     selector) is deliberately not in the class.
func Classify(r rune) string {
	switch {
	case r == 0x2014:
		return "em-dash"
	case r >= 0x2190 && r <= 0x21FF,
		r >= 0x27F5 && r <= 0x27FF,
		r >= 0x2B00 && r <= 0x2B0F,
		r >= 0x2B90 && r <= 0x2B9F,
		r == 0x2794, r == 0x279C, r == 0x27A1:
		return "arrow"
	case r >= 0x1F300 && r <= 0x1FAFF,
		r >= 0x2600 && r <= 0x27BF,
		r >= 0x2B00 && r <= 0x2BFF,
		r == 0x2611:
		return "emoji-symbol"
	}
	return ""
}

// Occurrence is one banned rune found in scanned copy.
type Occurrence struct {
	File    string // repo-relative path
	Line    int
	Rune    rune
	Class   string
	Context string // the source line, trimmed
}

func (o Occurrence) String() string {
	return fmt.Sprintf("%s:%d U+%04X (%s): %s", o.File, o.Line, o.Rune, o.Class, o.Context)
}

// Directive is one parsed aitaste:allow exemption.
type Directive struct {
	File   string
	Line   int
	Allows map[rune]int // code point -> how many occurrences it may cover
	Reason string
}

func (d Directive) String() string {
	var cps []string
	for r, n := range d.Allows {
		if n == 1 {
			cps = append(cps, fmt.Sprintf("U+%04X", r))
		} else {
			cps = append(cps, fmt.Sprintf("U+%04X x%d", r, n))
		}
	}
	sort.Strings(cps)
	return fmt.Sprintf("%s:%d %s: %s", d.File, d.Line, strings.Join(cps, " "), d.Reason)
}

// FileResult is the scan of a single file, before directives are applied.
type FileResult struct {
	File        string
	Occurrences []Occurrence
	Directives  []Directive
	// NonASCIIRunes and CJKRunes count what the scanner actually read in the
	// scanned copy. They are the aihub#373 F3 lesson made permanent: that
	// measurement once reported a false "clean" because an encoding
	// round-trip had silently destroyed every non-ASCII character before the
	// count. A scan of this repo that sees zero non-ASCII text did not scan
	// the repo.
	NonASCIIRunes int
	CJKRunes      int
}

// directiveRE matches the exemption grammar; see the package comment.
var directiveRE = regexp.MustCompile(`aitaste:allow((?:\s+U\+[0-9A-Fa-f]{4,6}(?:\s+x[0-9]+)?)+)\s*:\s*(\S.*)`)

// badDirectiveRE catches an aitaste:allow that does not parse, so a typoed
// exemption fails loudly instead of silently exempting nothing while its
// author believes it works.
var badDirectiveRE = regexp.MustCompile(`aitaste:allow`)

// parseDirectives extracts exemption directives from raw source lines.
// The gate recognizes the directive textually on its line; it lives in a
// comment by convention.
func parseDirectives(file string, lines []string) (ds []Directive, malformed []string) {
	for i, ln := range lines {
		m := directiveRE.FindStringSubmatch(ln)
		if m == nil {
			if badDirectiveRE.MatchString(ln) {
				malformed = append(malformed,
					fmt.Sprintf("%s:%d has an aitaste:allow that does not parse; the form is `aitaste:allow U+XXXX [xN]: reason`", file, i+1))
			}
			continue
		}
		allows := map[rune]int{}
		var last rune
		for _, tok := range strings.Fields(m[1]) {
			if strings.HasPrefix(tok, "U+") || strings.HasPrefix(tok, "u+") {
				v, err := strconv.ParseUint(tok[2:], 16, 32)
				if err != nil {
					continue
				}
				last = rune(v)
				allows[last]++
			} else if strings.HasPrefix(tok, "x") && last != 0 {
				n, err := strconv.Atoi(tok[1:])
				if err == nil && n > 0 {
					allows[last] = n
				}
			}
		}
		ds = append(ds, Directive{File: file, Line: i + 1, Allows: allows, Reason: strings.TrimSpace(m[2])})
	}
	return ds, malformed
}

// countText scans one line of already-visible text and appends occurrences.
func countText(fr *FileResult, line string, lineNo int, context string) {
	for _, r := range line {
		if r >= 0x80 {
			fr.NonASCIIRunes++
			if r >= 0x4E00 && r <= 0x9FFF {
				fr.CJKRunes++
			}
		}
		if c := Classify(r); c != "" {
			fr.Occurrences = append(fr.Occurrences, Occurrence{
				File: fr.File, Line: lineNo, Rune: r, Class: c,
				Context: truncate(strings.TrimSpace(context)),
			})
		}
	}
}

func truncate(s string) string {
	const max = 160
	if len(s) <= max {
		return s
	}
	// Cut on a rune boundary so the context stays valid UTF-8.
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "..."
}

// ScanGoFile scans the string literals of one Go source file. Escaped forms
// (`—` in an interpreted string) are caught by additionally scanning the
// unquoted value: a banned rune present after unquoting but absent from the
// source text is reported at the literal's first line.
func ScanGoFile(path, rel string) (FileResult, error) {
	fr := FileResult{File: rel}
	src, err := os.ReadFile(path)
	if err != nil {
		return fr, err
	}
	lines := strings.Split(string(src), "\n")
	var malformed []string
	fr.Directives, malformed = parseDirectives(rel, lines)
	if len(malformed) > 0 {
		return fr, fmt.Errorf("%s", strings.Join(malformed, "\n"))
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return fr, err
	}
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		start := fset.Position(lit.Pos()).Line
		lineOff := 0
		rawCount := map[rune]int{}
		for _, r := range lit.Value {
			if r == '\n' {
				lineOff++
				continue
			}
			if r >= 0x80 {
				fr.NonASCIIRunes++
				if r >= 0x4E00 && r <= 0x9FFF {
					fr.CJKRunes++
				}
			}
			if c := Classify(r); c != "" {
				rawCount[r]++
				ln := start + lineOff
				ctx := ""
				if ln-1 < len(lines) {
					ctx = lines[ln-1]
				}
				fr.Occurrences = append(fr.Occurrences, Occurrence{
					File: rel, Line: ln, Rune: r, Class: c,
					Context: truncate(strings.TrimSpace(ctx)),
				})
			}
		}
		// Escape-smuggling check: compare the decoded value against what the
		// source text already reported.
		if decoded, uerr := strconv.Unquote(lit.Value); uerr == nil {
			decCount := map[rune]int{}
			for _, r := range decoded {
				if Classify(r) != "" {
					decCount[r]++
				}
			}
			for r, n := range decCount {
				if extra := n - rawCount[r]; extra > 0 {
					for i := 0; i < extra; i++ {
						fr.Occurrences = append(fr.Occurrences, Occurrence{
							File: rel, Line: start, Rune: r, Class: Classify(r),
							Context: truncate("escape-encoded in string literal: " + strings.TrimSpace(lines[start-1])),
						})
					}
				}
			}
		}
		return true
	})
	return fr, nil
}

// Template masking, in order. Each pattern is blanked with spaces (newlines
// kept) so line numbers survive. Template comments go before generic actions:
// a {{/* ... */}} comment may contain an inner "}}" that would otherwise end
// the match early and leak the comment's tail into visible text (measured on
// wi_detail.html.tmpl).
var (
	tmplCommentRE = regexp.MustCompile(`(?s)\{\{-?\s*/\*.*?\*/\s*-?\}\}`)
	tmplActionRE  = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
	htmlCommentRE = regexp.MustCompile(`(?s)<!--.*?-->`)
	scriptStyleRE = regexp.MustCompile(`(?is)<(script|style)\b.*?</(script|style)>`)
	tagRE         = regexp.MustCompile(`<[^>]+>`)
)

func maskPattern(s string, re *regexp.Regexp) string {
	return re.ReplaceAllStringFunc(s, func(m string) string {
		b := []byte(m)
		for i := range b {
			if b[i] != '\n' {
				b[i] = ' '
			}
		}
		return string(b)
	})
}

// VisibleTemplateText masks everything in a template that a browser does not
// render as text and returns what is left, newlines preserved.
func VisibleTemplateText(src string) string {
	s := maskPattern(src, tmplCommentRE)
	s = maskPattern(s, tmplActionRE)
	s = maskPattern(s, htmlCommentRE)
	s = maskPattern(s, scriptStyleRE)
	s = maskPattern(s, tagRE)
	return s
}

// ScanTemplateFile scans the visible text of one template. Directives are
// read from the RAW lines (they live in HTML comments, which the visible-text
// pass masks out). Entities are decoded before classification so `&mdash;`
// counts as U+2014.
func ScanTemplateFile(path, rel string) (FileResult, error) {
	fr := FileResult{File: rel}
	src, err := os.ReadFile(path)
	if err != nil {
		return fr, err
	}
	rawLines := strings.Split(string(src), "\n")
	var malformed []string
	fr.Directives, malformed = parseDirectives(rel, rawLines)
	if len(malformed) > 0 {
		return fr, fmt.Errorf("%s", strings.Join(malformed, "\n"))
	}
	visible := strings.Split(VisibleTemplateText(string(src)), "\n")
	for i, ln := range visible {
		ctx := ""
		if i < len(rawLines) {
			ctx = rawLines[i]
		}
		countText(&fr, html.UnescapeString(ln), i+1, ctx)
	}
	return fr, nil
}

// Result is the outcome of a full scan after directives are applied.
type Result struct {
	Violations      []Occurrence
	Exemptions      []Directive // directives that covered at least one occurrence
	StaleDirectives []Directive // directives that covered nothing: delete them
	NonASCIIRunes   int
	CJKRunes        int
}

// Check applies each file's directives to its occurrences. A directive covers
// occurrences on its own line; if its own line has none, it covers the line
// directly below (the form used when a line cannot carry a trailing comment).
// Coverage is bounded per code point by the declared count, so a second
// banned rune landing on an exempted line is a violation, not a free rider.
func Check(files []FileResult) Result {
	var res Result
	for _, fr := range files {
		res.NonASCIIRunes += fr.NonASCIIRunes
		res.CJKRunes += fr.CJKRunes

		budget := make([]map[rune]int, len(fr.Directives))
		used := make([]bool, len(fr.Directives))
		targetLine := make([]int, len(fr.Directives))
		occOnLine := map[int]bool{}
		for _, o := range fr.Occurrences {
			occOnLine[o.Line] = true
		}
		for i, d := range fr.Directives {
			budget[i] = map[rune]int{}
			for r, n := range d.Allows {
				budget[i][r] = n
			}
			targetLine[i] = d.Line
			if !occOnLine[d.Line] {
				targetLine[i] = d.Line + 1
			}
		}
		for _, o := range fr.Occurrences {
			exempted := false
			for i := range fr.Directives {
				if targetLine[i] == o.Line && budget[i][o.Rune] > 0 {
					budget[i][o.Rune]--
					used[i] = true
					exempted = true
					break
				}
			}
			if !exempted {
				res.Violations = append(res.Violations, o)
			}
		}
		for i, d := range fr.Directives {
			if used[i] {
				res.Exemptions = append(res.Exemptions, d)
			} else {
				res.StaleDirectives = append(res.StaleDirectives, d)
			}
		}
	}
	return res
}

// ScanRepo scans every surface under root and applies directives.
func ScanRepo(root string) (Result, error) {
	var files []FileResult
	for _, dir := range goSurfaceDirs {
		err := filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			fr, ferr := ScanGoFile(path, rel)
			if ferr != nil {
				return fmt.Errorf("scanning %s: %w", rel, ferr)
			}
			files = append(files, fr)
			return nil
		})
		if err != nil {
			return Result{}, err
		}
	}
	err := filepath.Walk(filepath.Join(root, templateDir), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".tmpl") {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		fr, ferr := ScanTemplateFile(path, rel)
		if ferr != nil {
			return fmt.Errorf("scanning %s: %w", rel, ferr)
		}
		files = append(files, fr)
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return Check(files), nil
}
