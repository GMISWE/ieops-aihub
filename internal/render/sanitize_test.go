package render

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// xssCase mirrors one entry of test/render/fixtures/xss_payloads.json.
type xssCase struct {
	Name      string   `json:"name"`
	Category  string   `json:"category"`
	Input     string   `json:"input"`
	Forbidden []string `json:"forbidden"`
	Required  []string `json:"required"`
}

type xssCorpus struct {
	Cases []xssCase `json:"cases"`
}

// loadCorpus reads the shared payload corpus. It lives under test/render/fixtures/
// rather than beside this file so the same corpus can be reused by the server-side
// integration tests and by the browser checklist in the aihub-test deploy step
// (aihub#240) without duplicating the payloads.
func loadCorpus(t *testing.T) []xssCase {
	t.Helper()
	path := filepath.Join("..", "..", "test", "render", "fixtures", "xss_payloads.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read corpus %s: %v", path, err)
	}
	var c xssCorpus
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("corpus is empty — a passing run against an empty corpus is not coverage")
	}
	return c.Cases
}

// TestSanitizeArtifactHTML_Corpus is the stored-XSS regression gate (aihub#240,
// resolves #144). It asserts on the sanitizer alone: the sandboxed iframe and the
// CSP header are independent layers and are deliberately not relied on here.
func TestSanitizeArtifactHTML_Corpus(t *testing.T) {
	for _, tc := range loadCorpus(t) {
		t.Run(tc.Name, func(t *testing.T) {
			got := SanitizeArtifactHTML(tc.Input)
			lower := strings.ToLower(got)

			for _, bad := range tc.Forbidden {
				if strings.Contains(lower, strings.ToLower(bad)) {
					t.Errorf("[%s] sanitized output still contains %q\ninput:  %s\noutput: %s",
						tc.Category, bad, tc.Input, got)
				}
			}
			// Over-sanitizing is its own defect class: a policy that silently eats
			// legitimate SVG breaks every complex diagram (acceptance criterion 4).
			for _, want := range tc.Required {
				if !strings.Contains(got, want) {
					t.Errorf("[%s] sanitizer dropped legitimate content %q\ninput:  %s\noutput: %s",
						tc.Category, want, tc.Input, got)
				}
			}
		})
	}
}

// TestSanitizeArtifactHTML_CoversAllCategories guards the corpus itself. Without it
// a future edit could delete every xml_dtd case and this suite would still be green —
// the same "a narrower tripwire than the invariant it guards" trap recorded in
// mem_I98xpPgY.
func TestSanitizeArtifactHTML_CoversAllCategories(t *testing.T) {
	want := []string{"script_tag", "event_attr", "javascript_uri", "external_resource", "xml_dtd"}
	seen := map[string]int{}
	for _, tc := range loadCorpus(t) {
		seen[tc.Category]++
	}
	for _, cat := range want {
		if seen[cat] == 0 {
			t.Errorf("corpus has no %q case — required by 01-static-html-render-engine-research 2.6", cat)
		}
	}

	// style_block is not from the research doc's list; it is here because two consecutive
	// review rounds found the sanitizer bypassable in <style> and nowhere else, while this
	// very guard reported full category coverage. A category enumeration only proves what
	// it enumerates, so the class that actually broke is now enumerated too — with a floor,
	// because one token case would satisfy a bare presence check.
	//
	// The floor is deliberately well below the current count: it should fail when the class
	// is gutted, not every time someone reorganises the corpus.
	const minStyleBlock = 10
	if n := seen["style_block"]; n < minStyleBlock {
		t.Errorf("corpus has %d style_block cases, want >= %d — this is the class that "+
			"bypassed the sanitizer twice (foreign-content <style>, and ordinary CSS "+
			"defeating the property allowlist)", n, minStyleBlock)
	}
}

// TestSanitizeArtifactHTML_CorpusIsNotVacuous proves the corpus can actually fail.
//
// Every case here passed on the sanitizer's first run, which is precisely when a
// regression suite deserves distrust (mem_I98xpPgY: "a tripwire scoped narrower than
// the invariant it guards is worse than no tripwire, because it reads as coverage").
// Two independent ways a case can assert nothing:
//
//   - the forbidden substring never appears in the raw input, so no sanitizer of any
//     kind could leave it behind; or
//   - no case in the corpus would fail against an identity function.
//
// Both are checked, so weakening the corpus fails the suite rather than quietly
// shrinking it.
func TestSanitizeArtifactHTML_CorpusIsNotVacuous(t *testing.T) {
	cases := loadCorpus(t)

	for _, tc := range cases {
		for _, bad := range tc.Forbidden {
			if !strings.Contains(strings.ToLower(tc.Input), strings.ToLower(bad)) {
				t.Errorf("case %q forbids %q but the raw input never contains it — "+
					"this assertion would pass against any sanitizer", tc.Name, bad)
			}
		}
	}

	caught := 0
	for _, tc := range cases {
		for _, bad := range tc.Forbidden {
			if strings.Contains(strings.ToLower(tc.Input), strings.ToLower(bad)) {
				caught++
				break
			}
		}
	}
	if caught == 0 {
		t.Fatal("no case would fail against an identity sanitizer — the corpus proves nothing")
	}
	t.Logf("%d/%d cases would fail against an identity sanitizer", caught, len(cases))
}

// TestSanitizeArtifactHTML_Idempotent — sanitizing twice must equal sanitizing once.
// A non-idempotent sanitizer is a smell: it means output is not a fixed point of the
// policy, so a second pass (retry, re-render, cache refill) could yield different bytes.
func TestSanitizeArtifactHTML_Idempotent(t *testing.T) {
	for _, tc := range loadCorpus(t) {
		t.Run(tc.Name, func(t *testing.T) {
			once := SanitizeArtifactHTML(tc.Input)
			twice := SanitizeArtifactHTML(once)
			if once != twice {
				t.Errorf("not idempotent\nonce:  %s\ntwice: %s", once, twice)
			}
		})
	}
}

func TestSanitizeArtifactHTML_Empty(t *testing.T) {
	if got := SanitizeArtifactHTML(""); got != "" {
		t.Errorf("empty input should yield empty output, got %q", got)
	}
}

// TestSanitizeArtifactHTML_Concurrent runs under -race. The policy is built once at
// package init and shared; bluemonday policies are documented as safe for concurrent
// use, and this pins that assumption for us (cf. the goja/textmeasure concurrency
// fragility that made the old D2 path flaky — aihub-d2-rendering-research R3).
func TestSanitizeArtifactHTML_Concurrent(t *testing.T) {
	cases := loadCorpus(t)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, tc := range cases {
				_ = SanitizeArtifactHTML(tc.Input)
			}
		}()
	}
	wg.Wait()
}

// TestSanitizeArtifactHTML_PreservesInlinedAssets pins the fix for a defect that every
// coarse assertion missed.
//
// d2 embeds its webfont into the generated <svg> as @font-face{src:url(data:font/...)}.
// The first cut of sanitizeCSS neutralised every url() that was not a "#" fragment,
// which discarded ~95% of that stylesheet. Nothing failed: the <svg>, <style>, markers
// and fill attributes were all still present, so tag- and attribute-level checks stayed
// green while the diagram lost its typography. Byte volume is asserted here because the
// damage was quantitative, not structural.
func TestSanitizeArtifactHTML_PreservesInlinedAssets(t *testing.T) {
	// A data: image is the inlined-asset form that still passes through this policy.
	// The <style>-embedded webfont this test used to assert on no longer does — see
	// TestSanitizeArtifactHTML_StyleElementIsDroppedWhole for why that is deliberate.
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAAAAAA6fptVAAAACklEQVR4nGMAAgAABQAB"
	in := `<p>fig</p><img src="data:image/png;base64,` + png + `" alt="a"/>`

	got := SanitizeArtifactHTML(in)

	if !strings.Contains(got, "data:image/png;base64,"+png) {
		t.Errorf("inlined data: image was stripped — documents lose their figures\ngot: %s", got)
	}
	// The external case must still be neutralised.
	ext := SanitizeArtifactHTML(`<p>x</p><img src="http://evil.example/x.png"/>`)
	if strings.Contains(ext, "evil.example") {
		t.Errorf("external image URL survived: %s", ext)
	}
}

// Inline style="" carries presentation that dense agent-authored SVG relies on, but only
// from a property whitelist — nothing that can reposition content over the parent UI.
func TestSanitizeArtifactHTML_InlineStyleIsWhitelisted(t *testing.T) {
	got := SanitizeArtifactHTML(
		`<svg viewBox="0 0 10 10"><rect style="fill:#0f0;stroke-width:2;position:absolute;z-index:99" width="10" height="10"/></svg>`)

	for _, want := range []string{"fill", "stroke-width"} {
		if !strings.Contains(got, want) {
			t.Errorf("whitelisted style property %q was dropped: %s", want, got)
		}
	}
	for _, bad := range []string{"position", "z-index"} {
		if strings.Contains(got, bad) {
			t.Errorf("non-whitelisted style property %q survived: %s", bad, got)
		}
	}
}

// TestSanitizeArtifactHTML_PreservesAttributelessContainers pins a defect found only
// when the spike artifact's figure grew dense enough to use a merge filter.
//
// bluemonday drops any element left with no attributes unless it is explicitly allowed
// to have none. <feMerge> and <defs> are pure containers, so they were being removed
// while their children survived — leaving <feMergeNode> dangling outside any merge. The
// filter then silently stops compositing: the figure still renders, just wrong, and
// every count-based assertion over attributed elements (linearGradient, feDropShadow,
// clipPath) stays green throughout.
func TestSanitizeArtifactHTML_PreservesAttributelessContainers(t *testing.T) {
	in := `<svg viewBox="0 0 10 10"><defs><filter id="soft">` +
		`<feGaussianBlur stdDeviation="3" result="b"/>` +
		`<feMerge><feMergeNode in="b"/><feMergeNode in="SourceGraphic"/></feMerge>` +
		`</filter></defs><g><rect width="10" height="10" filter="url(#soft)"/></g></svg>`

	got := SanitizeArtifactHTML(in)

	for _, want := range []string{"<defs>", "<feMerge>", "</feMerge>", "<g>"} {
		if !strings.Contains(got, want) {
			t.Errorf("attributeless container %q was dropped\ngot: %s", want, got)
		}
	}
	// The merge must still wrap its nodes, not merely coexist with them.
	i, j := strings.Index(got, "<feMerge>"), strings.Index(got, "</feMerge>")
	k := strings.Index(got, "<feMergeNode")
	if i < 0 || j < 0 || k < i || k > j {
		t.Errorf("feMergeNode is no longer nested inside feMerge: %s", got)
	}
}

// TestSanitizeArtifactHTML_StyleCloseTagCannotBeObfuscated is the regression gate for a
// working stored-XSS bypass found in clean-context review (aihub#240, mem_Z36hAbl0).
//
// <style> is RAWTEXT: HTML ends it on "</style" followed by whitespace, "/" or ">". The
// regex-based extractor required a literal "</style>", so any of these forms let the
// extractor run on to a later "</style>" and treat everything between — arbitrary markup
// — as a CSS body that skipped bluemonday entirely and was spliced back verbatim.
//
// The extractor is gone (see TestSanitizeArtifactHTML_StyleElementIsDroppedWhole), so this
// no longer guards a boundary we compute — bluemonday's own RAWTEXT handling decides where
// the block ends. It is kept as a tripwire against reintroducing any mechanism that has to
// locate that boundary itself, since two separate attempts got it wrong in two different
// ways. The closer variants are the cheapest evidence that the third one is not being
// written by accident.
func TestSanitizeArtifactHTML_StyleCloseTagCannotBeObfuscated(t *testing.T) {
	variants := []string{
		"</style\n>",
		"</style >",
		"</style/>",
		"</style\t>",
		"</STYLE\n>",
	}
	for _, closer := range variants {
		t.Run(strings.TrimSpace(closer), func(t *testing.T) {
			in := `<p>ok</p><style>a{color:red}` + closer +
				`<script>alert('PWNED')</script><style>b{color:blue}</style>`
			got := SanitizeArtifactHTML(in)

			if strings.Contains(got, "PWNED") || strings.Contains(strings.ToLower(got), "<script") {
				t.Errorf("closer %q let script through:\n%s", closer, got)
			}
			if !strings.Contains(got, "ok") {
				t.Errorf("legitimate content lost for closer %q: %s", closer, got)
			}
		})
	}
}

// TestSanitizeArtifactHTML_StyleElementIsDroppedWhole pins the current contract: a <style>
// element and its entire body are discarded, wherever it appears.
//
// This replaces two tests that asserted the opposite — that a <style> body was filtered
// against a property allowlist, and that its @font-face survived. Both were true of a
// lift-and-filter-and-splice design that was bypassed twice, the second time into a working
// stored XSS on authed /ui:
//
//   - `<svg><style><img src=x onerror=alert(1)></style>` — inside <svg>, `style` is foreign
//     content, so a conformant parser treats its contents as markup. The lifter used a bare
//     tokenizer, which sets RAWTEXT unconditionally, so it carried a live <img> out as a CSS
//     body, past the policy, and spliced it back.
//   - `.x{position:fixed;…;background:red` with no closing brace — the filter dumped any
//     unbalanced tail verbatim, and CSS Syntax closes open blocks at EOF, so browsers applied
//     it. @media/@supports nesting, `{` inside a comment, and `}` inside a string all reached
//     the same verbatim path.
//
// Dropping the element outright makes both classes unreachable rather than filtered, which is
// why this test asserts absence of the *body*, not absence of specific properties: a future
// change that reintroduces any form of body-preservation must fail here.
func TestSanitizeArtifactHTML_StyleElementIsDroppedWhole(t *testing.T) {
	cases := []struct {
		name string
		in   string
		// gone are substrings from the style body that must not survive.
		gone []string
		// kept is content outside the block that must be untouched.
		kept []string
	}{
		{
			name: "foreign content lifts a live img",
			in:   `<svg viewBox="0 0 10 10"><style><img src=x onerror=alert(1)></style><rect width="10" height="10"/></svg>`,
			gone: []string{"onerror", "<img", "alert(1)"},
			kept: []string{"<svg", "<rect"},
		},
		{
			name: "foreign content lifts a script element",
			in:   `<svg><style><script>alert(2)</script></style><rect width="1" height="1"/></svg>`,
			gone: []string{"<script", "alert(2)"},
			kept: []string{"<svg", "<rect"},
		},
		{
			name: "unbalanced block is not dumped verbatim",
			in:   `<p>ok</p><style>.x{position:fixed;top:0;z-index:2147483647;background:red</style>`,
			gone: []string{"position:fixed", "z-index", "2147483647"},
			kept: []string{"ok"},
		},
		{
			name: "at-rule nesting does not reach the verbatim path",
			in:   `<p>ok</p><style>@media all{.x{position:fixed;z-index:99999}}</style>`,
			gone: []string{"position:fixed", "z-index", "@media"},
			kept: []string{"ok"},
		},
		{
			name: "font-face cannot reach the network",
			in:   `<p>ok</p><style>@media all{@font-face{src:url(https://evil.example/f.woff)}}</style>`,
			gone: []string{"evil.example", "@font-face"},
			kept: []string{"ok"},
		},
		{
			name: "benign d2 stylesheet goes too, and says so",
			in:   `<svg viewBox="0 0 100 50"><style>.d2-1 .fill-N1{fill:#1c1c20;}</style><rect class="fill-N1" width="100" height="50"/></svg>`,
			gone: []string{"<style", "{fill:#1c1c20"},
			kept: []string{"<svg", "<rect", `class="fill-N1"`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeArtifactHTML(tc.in)
			if strings.Contains(strings.ToLower(got), "<style") {
				t.Errorf("a <style> element survived: %s", got)
			}
			for _, bad := range tc.gone {
				if strings.Contains(strings.ToLower(got), strings.ToLower(bad)) {
					t.Errorf("style body content %q survived: %s", bad, got)
				}
			}
			for _, good := range tc.kept {
				if !strings.Contains(got, good) {
					t.Errorf("content outside the block was lost — %q missing from: %s", good, got)
				}
			}
		})
	}
}

// TestSanitizeArtifactHTML_TrustedDiagramStylesheetBypassesThisPolicy documents the other
// half of the trade, so that "the sanitizer eats d2's stylesheet" is not read as a bug.
//
// d2's theming (fill/stroke classes) and its embedded webfont live in a <style> inside the
// generated <svg>, and the test above proves this policy deletes exactly that. It is not
// lost, because trusted diagram SVG never passes through here: the ordering is sanitize
// first, compile/insert second, so a d2 fence is still <pre><code class="language-d2"> when
// the policy runs and the SVG appears only afterwards.
//
// This asserts the load-bearing half of that ordering — that a d2 fence survives
// sanitization intact and is therefore still compilable. If the policy ever starts eating
// the fence, diagrams disappear and no test above would notice.
func TestSanitizeArtifactHTML_TrustedDiagramStylesheetBypassesThisPolicy(t *testing.T) {
	in := "<pre><code class=\"language-d2\">a -&gt; b: hello\n</code></pre>"
	got := SanitizeArtifactHTML(in)

	for _, need := range []string{"<pre", "<code", `class="language-d2"`, "a -&gt; b: hello"} {
		if !strings.Contains(got, need) {
			t.Errorf("d2 fence damaged by the policy — %q missing; diagrams would silently "+
				"stop rendering\ngot: %s", need, got)
		}
	}
}

// TestSanitizeArtifactHTML_DoesNotCorruptProse — the DTD cleanup used to run a
// whole-document `\]\s*>` sweep whenever "<!" appeared anywhere, deleting every "]>" in
// the document. That corrupted ordinary prose and stripped the "]]>" CDATA terminator
// out of d2's generated SVG.
func TestSanitizeArtifactHTML_DoesNotCorruptProse(t *testing.T) {
	in := `<!-- a comment --><p>index a[0]> b, and a CDATA end ]]> in prose</p>`
	got := SanitizeArtifactHTML(in)
	for _, want := range []string{"a[0]", "]]&gt;", "in prose"} {
		if !strings.Contains(got, want) {
			t.Errorf("prose corrupted, %q missing: %s", want, got)
		}
	}
	// The doctype itself must still go.
	if d := SanitizeArtifactHTML(`<!DOCTYPE svg [<!ENTITY x SYSTEM "file:///etc/passwd">]><p>y</p>`); strings.Contains(d, "passwd") {
		t.Errorf("doctype/entity survived: %s", d)
	}
}
