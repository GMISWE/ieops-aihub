package render

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// These tests cover the Go side of the bridge — that it is embedded, that its
// configuration cannot be used as an injection sink, and that the source retains the
// checks the design depends on.
//
// They deliberately do NOT claim the bridge *behaves* correctly: message handling,
// anchoring across regeneration and highlight rendering are browser behaviour, and the
// only honest verification for those is the aihub-test deploy checklist (aihub#240 T7,
// acceptance criteria 7 and 9). Asserting on JS source text here would read as coverage
// it is not.

func TestAnnotationBridge_IsEmbedded(t *testing.T) {
	if len(annotationBridgeJS) < 500 {
		t.Fatalf("bridge source looks empty or truncated: %d bytes", len(annotationBridgeJS))
	}
	if !strings.Contains(annotationBridgeJS, "pf-annot-bridge") {
		t.Error("bridge does not carry its protocol source tag")
	}
}

// parentOrigin comes from scheme + Request.Host. Host is attacker-influenceable, so if
// it were concatenated into the script body a crafted Host header would inject script
// into our own trusted, nonced, CSP-permitted code — the one place in the frame where
// script is allowed to run.
func TestAnnotationBridgeFor_OriginCannotInjectScript(t *testing.T) {
	hostile := []string{
		`https://ok.example";alert(1);var x="`,
		`https://ok.example</script><script>alert(1)</script>`,
		"https://ok.example\n;alert(1)",
		`https://ok.example'+alert(1)+'`,
		`https://ok.example` + "\u2028" + `alert(1)`,
	}
	for _, h := range hostile {
		out := AnnotationBridgeFor(h)
		prologue := out[:strings.Index(out, "\n")]

		// The only sound check is a structural one: peel off the assignment and require
		// that what remains is JSON which round-trips back to exactly the input. If it
		// parses and matches, the value is inside a string literal and cannot have
		// become code. Substring checks cannot distinguish `\";alert(1)` (escaped, inert)
		// from a real break-out.
		const prefix = "window.__PF_BRIDGE_CONFIG__="
		if !strings.HasPrefix(prologue, prefix) || !strings.HasSuffix(prologue, ";") {
			t.Fatalf("prologue is not a single assignment for %q: %s", h, prologue)
		}
		payload := strings.TrimSuffix(strings.TrimPrefix(prologue, prefix), ";")

		var got map[string]string
		if err := json.Unmarshal([]byte(payload), &got); err != nil {
			t.Errorf("prologue payload is not valid JSON for %q: %v\n%s", h, err, payload)
			continue
		}
		if got["parentOrigin"] != h {
			t.Errorf("origin did not round-trip: got %q want %q", got["parentOrigin"], h)
		}
		// Raw script-closing sequences must never appear literally, or the <script>
		// element carrying the prologue would be terminated early by the HTML parser.
		if strings.Contains(strings.ToLower(payload), "</script") {
			t.Errorf("payload contains a literal </script for %q: %s", h, payload)
		}
	}
}

func TestAnnotationBridgeFor_BindsOrigin(t *testing.T) {
	out := AnnotationBridgeFor("https://aihub.example")
	if !strings.Contains(out, `"parentOrigin":"https://aihub.example"`) {
		t.Errorf("origin not bound into config: %s", out[:120])
	}
	if !strings.Contains(out, annotationBridgeJS) {
		t.Error("bridge body missing after prologue")
	}
}

// An empty origin must produce a bridge that stays silent. The failure mode being
// avoided is a fallback to postMessage(msg, '*'), which would hand selected document
// text to any window that listens.
func TestAnnotationBridgeFor_EmptyOriginStaysSilent(t *testing.T) {
	out := AnnotationBridgeFor("")
	if !strings.Contains(out, `"parentOrigin":""`) {
		t.Error("empty origin not represented in config")
	}
	if strings.Contains(annotationBridgeJS, `postMessage(msg, '*')`) ||
		strings.Contains(annotationBridgeJS, `postMessage(msg,"*")`) {
		t.Error("bridge falls back to a wildcard targetOrigin")
	}
}

// The bridge is the only script permitted to run in the frame, so it must not provide a
// route back to arbitrary evaluation for the untrusted document sharing its window.
func TestAnnotationBridge_NoDynamicEvaluationSinks(t *testing.T) {
	for _, sink := range []string{"eval(", "new Function(", ".innerHTML", "document.write", "setTimeout(\""} {
		if strings.Contains(annotationBridgeJS, sink) {
			t.Errorf("bridge contains dynamic-evaluation sink %q", sink)
		}
	}
}

// Both directions of the protocol are validated in the shipped source. This is a
// structural check that the guards exist at all — whether they hold under a real hostile
// message is T7's browser test, not this one's.
func TestAnnotationBridge_ValidatesInboundMessages(t *testing.T) {
	for _, guard := range []string{
		"ev.origin !== PARENT_ORIGIN",
		"ev.source !== parent",
		"d.source !== 'pf-annot-host'",
		"d.v !== PROTOCOL_VERSION",
	} {
		if !strings.Contains(annotationBridgeJS, guard) {
			t.Errorf("inbound validation guard missing: %s", guard)
		}
	}
}

// The theme message is the one inbound type that WRITES to the document the agent's content
// shares. Its vocabulary must stay closed: data-theme is set from d.mode, so a pass-through
// would let a forged message put arbitrary attribute text on <html>.
//
// Structural, like the guard test above — that the check is present in the shipped source.
// Whether a real cross-frame message lands is browser work, and it is what caught the bug
// this handler fixes: cookie-preset dark passed every server-side check while clicking the
// Dark button left the frame light.
func TestAnnotationBridge_ThemeVocabularyIsClosed(t *testing.T) {
	if !strings.Contains(annotationBridgeJS, "d.type === 'theme'") {
		t.Fatal("bridge does not handle the theme message; a live theme switch cannot reach the frame")
	}
	for _, mode := range []string{"'light'", "'dark'", "'auto'"} {
		if !strings.Contains(annotationBridgeJS, "d.mode !== "+mode) {
			t.Errorf("theme handler does not check for %s — the vocabulary must be enumerated, not validated by shape", mode)
		}
	}
	// The three stylesheet states are exactly what EmbedOptions.themeAttr normalises to. If
	// one grows a fourth value, this pins the two lists together.
	for _, mode := range []string{"light", "dark", "auto"} {
		if got := (EmbedOptions{Theme: mode}).themeAttr(); got != mode {
			t.Errorf("themeAttr(%q) = %q; the bridge accepts a value the embed cannot stamp", mode, got)
		}
	}
}

// End-to-end through the embed: the bridge must arrive inside the frame under the nonce
// the inner CSP grants, otherwise our own script is blocked by our own policy.
func TestSafeEmbed_CarriesConfiguredBridge(t *testing.T) {
	out := SafeEmbedDocument("<p>hello</p>", EmbedOptions{
		Title:        "t",
		BridgeScript: AnnotationBridgeFor("https://aihub.example"),
		nonce:        "NONCE1",
	})

	doc := innerDoc(t, out)
	if !strings.Contains(doc, `<script nonce="NONCE1">`) {
		t.Error("bridge not injected under a nonce")
	}
	if !strings.Contains(doc, "pf-annot-bridge") {
		t.Error("bridge body absent from the frame")
	}
	if !strings.Contains(doc, `"parentOrigin":"https://aihub.example"`) {
		t.Error("bridge config absent from the frame")
	}
	if decoded := innerDocDecoded(t, out); !strings.Contains(decoded, "script-src 'nonce-NONCE1'") {
		t.Error("inner CSP does not admit the bridge nonce")
	}
	// Assert on the attribute, not on the whole response: the bridge's own comments
	// discuss allow-same-origin by name, so a substring search over the output reports a
	// widened sandbox that does not exist.
	if got := sandboxAttrOf(t, out); got != "allow-scripts" {
		t.Fatalf("sandbox = %q, want exactly \"allow-scripts\"", got)
	}
}

// sandboxAttrOf extracts the iframe's sandbox attribute value.
var sandboxAttrRe = regexp.MustCompile(`<iframe[^>]*\ssandbox="([^"]*)"`)

func sandboxAttrOf(t *testing.T, out string) string {
	t.Helper()
	m := sandboxAttrRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no iframe sandbox attribute in output: %.200s", out)
	}
	return m[1]
}
