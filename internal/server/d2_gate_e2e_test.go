package server

import (
	"strconv"
	"strings"
	"testing"
)

// baselineScripts counts the <script> elements the viewer chrome emits for a payload-free body.
func baselineScripts(t *testing.T) int {
	t.Helper()
	defer withVersionChainOverride()()
	mem := publicSharedMem()
	mem.WorkItemID = strptr("aihub#240")
	mem.RenderedHTML = htmlPtr(`<h1>t</h1><h2 id="s">S</h2><p>keep</p>`)
	defer withLoadMemoryOverride(mem, nil)()
	return strings.Count(renderArtifactAt(t, "/ui/artifacts/:id/html").Body.String(), "<script")
}

// TestD2Gate_PayloadsDoNotReachTheAuthedViewer is the end-to-end half of the d2 gate.
//
// The unit tests in internal/render prove the gate refuses these sources. This proves the
// refusal survives the real handler: agent content -> sanitize -> gated compile -> chrome
// injection -> response bytes. The distinction matters because the defect being guarded lived
// precisely in the seam between those steps, not inside any one of them.
//
// Assertions are on LIVE constructs and on a differential <script> count. Both choices are
// forced: a refused fence degrades to its code block, where the payload survives HTML-escaped
// and harmless, and the viewer chrome ships its own <script> elements, so absolute substring
// counts measure the chrome rather than the payload.
func TestD2Gate_PayloadsDoNotReachTheAuthedViewer(t *testing.T) {
	payloads := map[string]string{
		"md script":  "x: |md **hi** <script>alert(1)</script> |",
		"md island":  `x: |md <script id="pf-annot-data" type="application/json">{"mem_id":"mem_attacker"}</script> |`,
		"md overlay": `x: |md <div style="position:fixed;top:0;left:0;width:100vw;height:100vh;z-index:99999">PHISH</div> |`,
		"link js":    "x: hi\nx.link: \"javascript:alert(1)\"",
		"icon ext":   "x: hi\nx.icon: https://evil.example/px.png",
	}
	for name, d2 := range payloads {
		defer withVersionChainOverride()()
		mem := publicSharedMem()
		mem.WorkItemID = strptr("aihub#240")
		esc := strings.NewReplacer("<", "&lt;", ">", "&gt;", `"`, "&#34;", "&", "&amp;").Replace(d2)
		mem.RenderedHTML = htmlPtr(`<h1>t</h1><h2 id="s">S</h2><p>keep</p>` +
			`<pre><code class="language-d2">` + esc + `</code></pre>`)
		restore := withLoadMemoryOverride(mem, nil)
		body := renderArtifactAt(t, "/ui/artifacts/:id/html").Body.String()
		restore()

		// Differential: the page ships its own first-party <script> (theme setter, JSON island),
		// so an absolute count of "<script>" measures the chrome, not the payload.
		live := []string{}
		for _, bad := range []string{"<foreignObject", "<iframe", `href="javascript:`,
			`href="https://evil.example`, "<div xmlns="} {
			if strings.Contains(body, bad) {
				live = append(live, bad)
			}
		}
		if n, base := strings.Count(body, "<script"), baselineScripts(t); n != base {
			live = append(live, "extra <script> elements beyond the chrome's "+strconv.Itoa(base)+": "+strconv.Itoa(n))
		}
		// The real island must still be the only qualified match.
		islands := strings.Count(body, `id="pf-annot-data"`)
		t.Logf("%-11s live-constructs=%v  id=\"pf-annot-data\" count=%d  degraded-to-code=%v",
			name, live, islands, strings.Contains(body, `class="language-d2"`))
		if len(live) > 0 {
			t.Errorf("%s: live constructs reached the authed /ui viewer: %v", name, live)
		}
		if islands != 1 {
			t.Errorf("%s: %d elements with id=pf-annot-data, want 1 (the server's)", name, islands)
		}
	}
}
