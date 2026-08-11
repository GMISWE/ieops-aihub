package render

import (
	"html"
	"os"
	"strings"
	"sync"
	"testing"
)

// The single most consequential assertion in this package. Granting allow-scripts and
// allow-same-origin together does not weaken isolation — it removes it outright: script
// inside the frame gains same-origin privileges and can simply strip the sandbox
// attribute off its own frame element (01-static-html-render-engine-research.md §2.1).
// There is deliberately no option, argument, or config that can add it.
func TestSafeEmbed_SandboxNeverGrantsSameOrigin(t *testing.T) {
	inputs := []string{
		"<p>hello</p>",
		"<svg viewBox=\"0 0 10 10\"><rect width=\"10\" height=\"10\"/></svg>",
		"",
		"<script>alert(1)</script>",
		"<iframe sandbox=\"allow-scripts allow-same-origin\" src=\"x\"></iframe>",
	}
	for _, in := range inputs {
		out := SafeEmbedDocument(in, EmbedOptions{Title: "t"})
		// Assert the attribute equals allow-scripts exactly, rather than searching the
		// response for the string "allow-same-origin". The substring form is unsound in
		// both directions: it misses `sandbox="allow-scripts allow-forms"` (a widening
		// it does not name) and it false-positives on any document that merely mentions
		// the token — which the annotation bridge's own comments do.
		if got := sandboxAttrOf(t, out); got != "allow-scripts" {
			t.Fatalf("sandbox = %q, want exactly \"allow-scripts\"\ninput: %q", got, in)
		}
	}
}

// The failsafe path is still a sandboxed frame. Degrading to unisolated rendering on an
// internal error would turn a display bug into an XSS.
func TestSafeEmbed_FailsafeKeepsSandbox(t *testing.T) {
	if got := sandboxAttrOf(t, failsafeFrame(EmbedOptions{Title: "t"})); got != "allow-scripts" {
		t.Fatalf("failsafe sandbox = %q, want exactly \"allow-scripts\"", got)
	}
}

// The agent's HTML lands inside a quoted srcdoc attribute. If its quotes are not
// escaped it breaks out of the attribute and back into the parent document, where the
// sandbox does not apply — the sanitizer's output would be re-parsed as parent markup.
func TestSafeEmbed_SrcdocAttributeCannotBeEscaped(t *testing.T) {
	breakout := `<p class="x">a</p>" onload="alert(1)`
	out := SafeEmbedDocument(breakout, EmbedOptions{Title: "t"})

	// Everything after the opening srcdoc=" up to the closing quote is the payload.
	i := strings.Index(out, `srcdoc="`)
	if i < 0 {
		t.Fatalf("no srcdoc attribute in output: %s", out)
	}
	rest := out[i+len(`srcdoc="`):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("srcdoc attribute is unterminated: %s", out)
	}
	tail := rest[j:]
	// After the attribute closes, only the iframe's own remaining markup may appear.
	if strings.Contains(strings.ToLower(tail), "onload") {
		t.Errorf("payload escaped the srcdoc attribute into parent markup: %s", tail)
	}
}

// innerDoc returns the document the browser reconstructs from srcdoc, i.e. the output
// with exactly one level of attribute escaping removed. Asserting here rather than on
// the raw bytes is deliberate: the raw form is double-escaped (once for the inner
// document's own attributes, once for the srcdoc attribute that carries it), so a
// literal-byte assertion tests the encoding rather than the behaviour.
func innerDoc(t *testing.T, out string) string {
	t.Helper()
	i := strings.Index(out, `srcdoc="`)
	if i < 0 {
		t.Fatalf("no srcdoc attribute: %s", out)
	}
	rest := out[i+len(`srcdoc="`):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("unterminated srcdoc: %s", out)
	}
	return html.UnescapeString(rest[:j])
}

// innerDocDecoded removes the second level too, yielding the values the inner parser
// hands to the engine (e.g. the CSP string as the browser applies it).
func innerDocDecoded(t *testing.T, out string) string {
	t.Helper()
	return html.UnescapeString(innerDoc(t, out))
}

func TestSafeEmbed_InnerDocumentCarriesCSP(t *testing.T) {
	decoded := innerDocDecoded(t, SafeEmbedDocument("<p>x</p>", EmbedOptions{Title: "t"}))
	for _, want := range []string{
		"Content-Security-Policy",
		"default-src 'none'",
		"connect-src 'none'",
		"object-src 'none'",
		// 'self' and data: only. 'self' is required because the sanitizer admits
		// root-relative <img src="/..."> (reSafeImageURL), and without it the two controls
		// disagreed about the same input: such an image rendered on the un-sandboxed
		// artifact viewer and silently failed inside a frame.
		"img-src 'self' data:",
		// No bridge script was requested, so nothing may execute.
		"script-src 'none'",
	} {
		if !strings.Contains(decoded, want) {
			t.Errorf("inner CSP missing %q\ndecoded document: %s", want, decoded)
		}
	}

	// What must NOT be reachable: any external origin. 'self' widened img-src by exactly one
	// origin — our own — and this pins that it went no further, since an external image URL in
	// a private document is a read receipt.
	for _, forbidden := range []string{"img-src *", "https:", "http:"} {
		if strings.Contains(decoded, "img-src") && strings.Contains(decoded, forbidden) {
			t.Errorf("inner CSP admits an external image source (%q): %s", forbidden, decoded)
		}
	}
}

// TestInnerBaseCSS_ProseRulesTrackUICSS is drift detection for a duplication the code admits
// to: innerBaseCSS transcribes ui.css's .prose rules because a frame under
// default-src 'none' cannot fetch a stylesheet, so the alternative is an embedded document
// whose typography does not match the page around it.
//
// The duplication is currently correct. What was missing is any way to NOTICE it going wrong:
// someone tuning .prose in ui.css six months from now gets no signal that a second copy
// exists. This compares the declarations that both files claim to share.
//
// It deliberately does not require the two to be identical — innerBaseCSS legitimately omits
// rules that cannot apply inside the frame. It requires that where both define the same
// property for the same selector, the values agree.
func TestInnerBaseCSS_ProseRulesTrackUICSS(t *testing.T) {
	uiCSS := readUICSSFromDisk(t)

	shared := map[string]string{} // "selector|property" -> value, from ui.css
	for _, line := range strings.Split(uiCSS, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, ".prose") {
			continue
		}
		open := strings.Index(line, "{")
		closeAt := strings.LastIndex(line, "}")
		if open < 0 || closeAt < open {
			continue
		}
		sel := normaliseCSS(line[:open])
		for _, decl := range strings.Split(line[open+1:closeAt], ";") {
			i := strings.Index(decl, ":")
			if i < 0 {
				continue
			}
			prop := strings.TrimSpace(decl[:i])
			val := normaliseCSS(decl[i+1:])
			if prop != "" && val != "" {
				shared[sel+"|"+prop] = val
			}
		}
	}
	if len(shared) < 20 {
		t.Fatalf("only %d .prose declarations parsed out of ui.css; the parser or the file "+
			"changed shape and this guard is no longer looking at anything", len(shared))
	}

	inner := normaliseCSS(innerBaseCSS)
	checked, mismatched := 0, 0
	for key, uiVal := range shared {
		parts := strings.SplitN(key, "|", 2)
		sel, prop := parts[0], parts[1]
		// Only consider selector blocks innerBaseCSS actually carries.
		at := strings.Index(inner, sel+"{")
		if at < 0 {
			continue
		}
		block := inner[at+len(sel)+1:]
		if end := strings.Index(block, "}"); end >= 0 {
			block = block[:end]
		}
		if !strings.Contains(block, prop+":") {
			continue
		}
		checked++
		if !strings.Contains(block, prop+":"+uiVal) {
			mismatched++
			t.Errorf("innerBaseCSS drifted from ui.css for %s { %s }: ui.css says %q, "+
				"innerBaseCSS block is %q", sel, prop, uiVal, block)
		}
	}
	if checked < 15 {
		t.Errorf("only %d shared declarations compared; innerBaseCSS no longer transcribes "+
			"enough of .prose for this guard to mean anything (mismatches: %d)", checked, mismatched)
	}
}

// readUICSSFromDisk reads internal/server/static/ui.css.
//
// From disk, not from an embed: ui.css belongs to the server package's embed.FS and this test
// lives in render, which cannot import it (server imports render, so the reverse would be a
// cycle). Tests run with the package directory as the working directory, so the relative path
// is stable. A missing file fails the test rather than skipping it — if the path moves, this
// guard must be repointed, not silently disabled.
func readUICSSFromDisk(t *testing.T) string {
	t.Helper()
	const rel = "../server/static/ui.css"
	b, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v — repoint this drift guard rather than deleting it", rel, err)
	}
	return string(b)
}

// normaliseCSS strips whitespace so declaration comparison is not defeated by formatting.
func normaliseCSS(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r':
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sandbox and frame-ancestors are not deliverable via <meta> (only via a real HTTP
// header). Emitting them there would read as protection while doing nothing, so the
// meta policy must not claim them.
func TestSafeEmbed_MetaCSPOmitsHeaderOnlyDirectives(t *testing.T) {
	out := SafeEmbedDocument("<p>x</p>", EmbedOptions{Title: "t"})
	meta := innerCSPForTest()
	for _, forbidden := range []string{"sandbox", "frame-ancestors"} {
		if strings.Contains(meta, forbidden) {
			t.Errorf("meta CSP claims header-only directive %q: %s", forbidden, meta)
		}
	}
	if !strings.Contains(out, "srcdoc=") {
		t.Fatal("expected srcdoc embedding")
	}
}

// Agent HTML is sanitized on the way in, so safe embedding does not depend on the
// caller having remembered to sanitize first.
func TestSafeEmbed_SanitizesAgentHTML(t *testing.T) {
	out := SafeEmbedDocument(`<p>ok</p><script>alert(1)</script><img src="x" onerror="alert(2)">`,
		EmbedOptions{Title: "t"})
	for _, bad := range []string{"alert(1)", "alert(2)", "onerror"} {
		if strings.Contains(out, bad) {
			t.Errorf("unsanitized content %q survived: %s", bad, out)
		}
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("legitimate content dropped: %s", out)
	}
}

// The trusted bridge script (aihub#240 T4) is ours, not the agent's, and must land in
// <head> with the nonce the CSP grants — otherwise CSP blocks our own code.
func TestSafeEmbed_BridgeScriptIsNonced(t *testing.T) {
	out := SafeEmbedDocument("<p>x</p>", EmbedOptions{
		Title:        "t",
		BridgeScript: "console.log(1)",
		nonce:        "TESTNONCE",
	})
	if doc := innerDoc(t, out); !strings.Contains(doc, `<script nonce="TESTNONCE">`) {
		t.Errorf("bridge script not nonced\ndocument: %s", doc)
	}
	if decoded := innerDocDecoded(t, out); !strings.Contains(decoded, "script-src 'nonce-TESTNONCE'") {
		t.Errorf("CSP does not grant the bridge nonce\ndecoded: %s", decoded)
	}
}

func TestSafeEmbed_NoBridgeMeansNoScriptSrc(t *testing.T) {
	out := SafeEmbedDocument("<p>x</p>", EmbedOptions{Title: "t", nonce: "N"})
	if strings.Contains(out, "nonce-N") {
		t.Errorf("granted a script nonce with no bridge script to run: %s", out)
	}
}

// Malformed input must degrade, never panic: a panic here is a 500 on a page that is
// merely displaying a document (aihub-d2-rendering-research backlog: "panic -> 500, no
// recover" was a real defect in the path this replaces).
func TestSafeEmbed_MalformedInputDoesNotPanic(t *testing.T) {
	malformed := []string{
		"<svg><g/onload=x//<p>",
		strings.Repeat("<div>", 5000),
		"<<<>>>&&&",
		"\x00\x01\x02",
		strings.Repeat("<svg><path d=\"", 1000),
	}
	for _, in := range malformed {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panicked on %.30q: %v", in, r)
				}
			}()
			_ = SafeEmbedDocument(in, EmbedOptions{Title: "t"})
		}()
	}
}

func TestSafeEmbed_NonceIsUniquePerCall(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		n := newNonce()
		if n == "" {
			t.Fatal("empty nonce")
		}
		if seen[n] {
			t.Fatalf("nonce collision after %d draws: %s", i, n)
		}
		seen[n] = true
	}
}

func TestSafeEmbed_Concurrent(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = SafeEmbedDocument("<svg viewBox=\"0 0 4 4\"><rect width=\"4\" height=\"4\"/></svg>",
					EmbedOptions{Title: "t", BridgeScript: "void 0"})
			}
		}()
	}
	wg.Wait()
}
