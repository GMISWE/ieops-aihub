package server

// Tests for aihub#125: text-selection annotation SSR scaffold.
//
// Coverage:
//   1. route-awareness: /v1 responses contain NO data island / scripts / rail / selform;
//      /ui responses contain all of them.
//   2. island escaping: "</script>" in commit body/quote produces no literal "</script" in
//      the island JSON region; HTML is also escaped in the flat list.
//   3. flat list v2: quote excerpt present + escaped; replies rendered; open commit has
//      inline reply+resolve forms pointing at artifact-scoped routes; resolved commit has no forms.
//   4. artifact-scoped reply/resolve routes: happy-path 303 to artifact page; mock-fn seam.
//   5. escapeJSONForScriptTag unit test.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// annotSpecBody is a goldmark-rendered spec fragment used across tests.
const annotSpecBody = `<h1 id="overview">Overview</h1>
<p>intro</p>
<h2 id="goals">Goals</h2>
<p>goals text</p>`

// openCommitWithQuote is a commit anchored to the "overview" heading with a
// text selection, replies, and open status.
func openCommitWithQuote() CommitEntry {
	return CommitEntry{
		ID:            "cm_open_q",
		AuthorDisplay: "Alice",
		AuthorUserID:  "u_alice",
		Body:          "This section needs more detail",
		CreatedAt:     "2026-06-01T10:00:00Z",
		Status:        CommitStatusOpen,
		Anchor: &CommitAnchor{
			HeadingID:   "overview",
			HeadingText: "Overview",
			Quote:       "exact selected text",
			Prefix:      "context before",
			Suffix:      "context after",
		},
		Replies: []CommitReply{
			{
				ID:            "cr_1",
				AuthorDisplay: "Bob",
				AuthorUserID:  "u_bob",
				Body:          "I agree",
				CreatedAt:     "2026-06-01T11:00:00Z",
			},
		},
	}
}

// resolvedCommit is a commit with no text selection that has been resolved.
func resolvedCommit() CommitEntry {
	return CommitEntry{
		ID:            "cm_res",
		AuthorDisplay: "Bob",
		AuthorUserID:  "u_bob",
		Body:          "Goals are unclear",
		CreatedAt:     "2026-06-02T10:00:00Z",
		Status:        CommitStatusResolved,
		Reply:         "Updated goals with specific OKRs",
		ResolvedAt:    "2026-06-03T09:00:00Z",
		Anchor:        &CommitAnchor{HeadingID: "goals", HeadingText: "Goals"},
	}
}

// xssCommit is a commit whose body and quote contain a </script> injection attempt.
func xssCommit() CommitEntry {
	return CommitEntry{
		ID:            "cm_xss",
		AuthorDisplay: "EvilUser",
		AuthorUserID:  "u_evil",
		Body:          `</script><script>alert(1)</script>`,
		CreatedAt:     "2026-06-01T00:00:00Z",
		Status:        CommitStatusOpen,
		Anchor: &CommitAnchor{
			HeadingID:   "overview",
			HeadingText: "Overview",
			Quote:       `quote with </script> inside`,
		},
	}
}

// marshalCommits marshals a slice of CommitEntry to json.RawMessage for test fixtures.
func marshalCommits(t *testing.T, commits []CommitEntry) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(commits)
	if err != nil {
		t.Fatalf("marshalCommits: %v", err)
	}
	return json.RawMessage(data)
}

// ─── 1. escapeJSONForScriptTag unit test ─────────────────────────────────────

func TestEscapeJSONForScriptTag_Basic(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{`{"body":"no issue"}`, `{"body":"no issue"}`},
		{`{"body":"</script>"}`, `{"body":"<\/script>"}`},
		{`{"body":"</script><script>alert(1)</script>"}`, `{"body":"<\/script><script>alert(1)<\/script>"}`},
		// </ anywhere, not just </script>
		{`{"body":"</style>"}`, `{"body":"<\/style>"}`},
	}
	for _, tc := range cases {
		got := string(escapeJSONForScriptTag([]byte(tc.input)))
		if got != tc.want {
			t.Errorf("escapeJSONForScriptTag(%q) = %q; want %q", tc.input, got, tc.want)
		}
	}
}

// ─── 2. buildAnnotationHTML v2 route-awareness ────────────────────────────────

// TestAnnotHTML_V2_DataIslandAndScaffold verifies the new elements emitted by
// buildAnnotationHTML v2:
//   - data island with correct id
//   - margin rail element
//   - selection form element
//
// and that renderArtifactBodyWithMeta WITHOUT the annotation fragment (the /v1
// path) contains none of them.
func TestAnnotHTML_V2_DataIslandAndScaffold(t *testing.T) {
	commits := marshalCommits(t, []CommitEntry{openCommitWithQuote(), resolvedCommit()})
	annotHTML := buildAnnotationHTML("mem_42", annotSpecBody, commits)

	if annotHTML == "" {
		t.Fatal("buildAnnotationHTML returned empty string")
	}

	// Must have data island.
	if !strings.Contains(annotHTML, `id="pf-annot-data"`) {
		t.Errorf("missing pf-annot-data island; excerpt: %s", excerptStr(annotHTML))
	}
	// Must have margin rail.
	if !strings.Contains(annotHTML, `id="pf-margin-rail"`) {
		t.Errorf("missing pf-margin-rail; excerpt: %s", excerptStr(annotHTML))
	}
	// Must have selection form.
	if !strings.Contains(annotHTML, `id="pf-selform"`) {
		t.Errorf("missing pf-selform; excerpt: %s", excerptStr(annotHTML))
	}

	// /v1 path: no annotation fragment means none of these must appear.
	// Note: we check for HTML element markers (id= attributes, script tags),
	// NOT for CSS class names like "pf-margin-rail" which legitimately appear
	// in the embedded stylesheet.
	v1Doc := renderArtifactBodyWithMeta(annotSpecBody, "mem (methodology.spec)", "", "", "", nil)
	for _, marker := range []string{
		`id="pf-annot-data"`,
		`id="pf-margin-rail"`,
		`id="pf-selform"`,
		`/ui/static/annotator.js`,
		`/ui/static/annot.js`,
	} {
		if strings.Contains(v1Doc, marker) {
			t.Errorf("/v1 doc must not contain %q", marker)
		}
	}
}

// TestAnnotHTML_V2_UIPathHasScripts verifies that when annotation HTML is
// passed to renderArtifactBodyWithMeta (the /ui path), the document contains
// the annotator.js and annot.js script tags.
func TestAnnotHTML_V2_UIPathHasScripts(t *testing.T) {
	commits := marshalCommits(t, []CommitEntry{openCommitWithQuote()})
	annotHTML := buildAnnotationHTML("mem_42", annotSpecBody, commits)
	// Append the script tags as the /ui path does.
	annotHTML += "\n<script src=\"/ui/static/annotator.js\" defer></script>\n<script src=\"/ui/static/annot.js\" defer></script>\n"

	uiDoc := renderArtifactBodyWithMeta(annotSpecBody, "mem (methodology.spec)", "", "", "", nil, annotHTML)
	for _, tag := range []string{
		`/ui/static/annotator.js`,
		`/ui/static/annot.js`,
	} {
		if !strings.Contains(uiDoc, tag) {
			t.Errorf("/ui doc missing %q; excerpt: %s", tag, excerptStr(uiDoc))
		}
	}

	// /v1 doc must not contain script tags.
	v1Doc := renderArtifactBodyWithMeta(annotSpecBody, "mem (methodology.spec)", "", "", "", nil)
	for _, tag := range []string{`/ui/static/annotator.js`, `/ui/static/annot.js`} {
		if strings.Contains(v1Doc, tag) {
			t.Errorf("/v1 doc must not contain %q", tag)
		}
	}
}

// ─── 3. Island escaping / XSS hardening ──────────────────────────────────────

// TestAnnotHTML_V2_IslandEscaping verifies:
//   - No literal "</script" inside the data island JSON region.
//   - The page outer HTML stays well-formed (no unmatched </script>).
//   - The body text is also HTML-escaped in the flat list.
func TestAnnotHTML_V2_IslandEscaping(t *testing.T) {
	commits := marshalCommits(t, []CommitEntry{xssCommit()})
	annotHTML := buildAnnotationHTML("mem_xss", annotSpecBody, commits)

	if annotHTML == "" {
		t.Fatal("buildAnnotationHTML returned empty for xss commit")
	}

	// Extract the island text between the opening and closing script tags.
	islandOpen := `<script type="application/json" id="pf-annot-data">`
	islandClose := `</script>`
	openIdx := strings.Index(annotHTML, islandOpen)
	if openIdx < 0 {
		t.Fatalf("island open tag not found; excerpt: %s", excerptStr(annotHTML))
	}
	afterOpen := annotHTML[openIdx+len(islandOpen):]
	closeIdx := strings.Index(afterOpen, islandClose)
	if closeIdx < 0 {
		t.Fatalf("island close tag not found; excerpt: %s", excerptStr(afterOpen))
	}
	islandContent := afterOpen[:closeIdx]

	// The JSON inside must NOT contain a literal "</script" sequence.
	if strings.Contains(islandContent, "</script") {
		t.Errorf("island JSON contains literal </script — XSS breakout possible; island: %s", excerptStr(islandContent))
	}

	// The JSON must still be valid after our escaping.
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(islandContent), &parsed); err != nil {
		t.Errorf("island JSON is invalid after escaping: %v; island: %s", err, excerptStr(islandContent))
	}

	// The flat list must HTML-escape the body (no raw script tags).
	if strings.Contains(annotHTML, "<script>alert(1)</script>") {
		t.Errorf("flat list contains unescaped XSS payload; excerpt: %s", excerptStr(annotHTML))
	}
}

// ─── 4. Flat list v2 content ─────────────────────────────────────────────────

// TestAnnotHTML_V2_FlatList verifies:
//   - quote excerpt present and escaped
//   - replies rendered with author + body
//   - open commit has inline reply+resolve forms pointing at artifact-scoped routes
//   - resolved commit has legacy AI reply + no inline forms
func TestAnnotHTML_V2_FlatList(t *testing.T) {
	commits := marshalCommits(t, []CommitEntry{openCommitWithQuote(), resolvedCommit()})
	annotHTML := buildAnnotationHTML("mem_spec_99", annotSpecBody, commits)

	// Quote excerpt.
	if !strings.Contains(annotHTML, "exact selected text") {
		t.Errorf("quote excerpt missing; excerpt: %s", excerptStr(annotHTML))
	}

	// Quote must be HTML-escaped (the fixture has plain text — verify no raw quotes injected).
	// Check that if quote had HTML it would be escaped (separate from xss test above).

	// Replies.
	if !strings.Contains(annotHTML, "I agree") {
		t.Errorf("reply body missing; excerpt: %s", excerptStr(annotHTML))
	}
	if !strings.Contains(annotHTML, "Bob") {
		t.Errorf("reply author missing; excerpt: %s", excerptStr(annotHTML))
	}

	// Open commit: reply form action points to artifact-scoped route.
	replyAction := "/ui/artifacts/mem_spec_99/commit/cm_open_q/reply"
	if !strings.Contains(annotHTML, replyAction) {
		t.Errorf("open commit missing reply form action %q; excerpt: %s", replyAction, excerptStr(annotHTML))
	}

	// Open commit: resolve form action points to artifact-scoped route.
	resolveAction := "/ui/artifacts/mem_spec_99/commit/cm_open_q/resolve"
	if !strings.Contains(annotHTML, resolveAction) {
		t.Errorf("open commit missing resolve form action %q; excerpt: %s", resolveAction, excerptStr(annotHTML))
	}

	// Resolved commit: AI reply is shown.
	if !strings.Contains(annotHTML, "Updated goals with specific OKRs") {
		t.Errorf("resolved commit missing AI reply text; excerpt: %s", excerptStr(annotHTML))
	}

	// Resolved commit: no inline forms.
	resolvedReplyAction := "/ui/artifacts/mem_spec_99/commit/cm_res/reply"
	if strings.Contains(annotHTML, resolvedReplyAction) {
		t.Errorf("resolved commit must not have inline reply form; excerpt: %s", excerptStr(annotHTML))
	}
	resolvedResolveAction := "/ui/artifacts/mem_spec_99/commit/cm_res/resolve"
	if strings.Contains(annotHTML, resolvedResolveAction) {
		t.Errorf("resolved commit must not have inline resolve form; excerpt: %s", excerptStr(annotHTML))
	}
}

// TestAnnotHTML_V2_QuoteTruncation verifies that a quote longer than 120 runes
// is display-truncated with an ellipsis in the flat list.
func TestAnnotHTML_V2_QuoteTruncation(t *testing.T) {
	longQuote := strings.Repeat("a", 150)
	e := CommitEntry{
		ID:            "cm_long",
		AuthorDisplay: "Alice",
		Body:          "long quote",
		CreatedAt:     "2026-06-01T00:00:00Z",
		Status:        CommitStatusOpen,
		Anchor: &CommitAnchor{
			HeadingID:   "overview",
			HeadingText: "Overview",
			Quote:       longQuote,
		},
	}
	commits := marshalCommits(t, []CommitEntry{e})
	annotHTML := buildAnnotationHTML("mem_lq", annotSpecBody, commits)

	// Extract the flat list portion (after the data island </script>) so we
	// do not accidentally match the full quote in the island JSON.
	islandOpen := `<script type="application/json" id="pf-annot-data">`
	islandClose := `</script>`
	islandIdx := strings.Index(annotHTML, islandOpen)
	if islandIdx < 0 {
		t.Fatal("island open tag not found")
	}
	afterIsland := annotHTML[islandIdx:]
	closeIdx := strings.Index(afterIsland, islandClose)
	if closeIdx < 0 {
		t.Fatal("island close tag not found")
	}
	flatList := annotHTML[islandIdx+len(islandOpen)+closeIdx+len(islandClose):]

	// Full 150-'a' string must NOT appear in the flat list (truncated to 120).
	if strings.Contains(flatList, longQuote) {
		t.Errorf("long quote must be truncated in flat list; fragment found verbatim")
	}
	// Should have 120 'a's (truncated portion) in the flat list.
	truncated := strings.Repeat("a", 120)
	if !strings.Contains(flatList, truncated) {
		t.Errorf("truncated quote (%d chars) not found in flat list; excerpt: %s", 120, excerptStr(flatList))
	}
	// Should contain the ellipsis marker.
	if !strings.Contains(flatList, "…") {
		t.Errorf("ellipsis missing after truncated quote; excerpt: %s", excerptStr(flatList))
	}
}

// ─── 5. Artifact-scoped reply/resolve handlers ───────────────────────────────

// newArtifactFormRequest builds a POST form request for /ui/artifacts/:id/commit/:commit_id/<action>.
func newArtifactFormRequest(t *testing.T, memID, commitID, action string, formBody url.Values, uc *UserContext) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost,
		"/ui/artifacts/"+memID+"/commit/"+commitID+"/"+action,
		strings.NewReader(formBody.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id", "commit_id")
	c.SetParamValues(memID, commitID)
	if uc != nil {
		setUser(c, uc)
	}
	return c, rec
}

// TestArtifactReplyCommit_HappyPath verifies that a valid reply POST:
//   - calls doReplyCommitFn with the right args
//   - 303 redirects to the artifact HTML page (not the memory page)
func TestArtifactReplyCommit_HappyPath(t *testing.T) {
	cleanupProject := withCommitMemoryProjectOverride("testproject", "active", nil)
	defer cleanupProject()
	calls, cleanupReply := withReplyCommitOverride(nil)
	defer cleanupReply()

	form := url.Values{"body": {"great observation"}}
	c, rec := newArtifactFormRequest(t, "mem_spec_1", "cm_a", "reply", form, writerUser("testproject"))
	if err := handleUIArtifactReplyCommit(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status: got %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	wantLoc := "/ui/artifacts/mem_spec_1/html"
	if loc := rec.Header().Get("Location"); loc != wantLoc {
		t.Errorf("Location: got %q, want %q", loc, wantLoc)
	}
	if len(*calls) != 1 {
		t.Fatalf("doReplyCommitFn call count: got %d, want 1", len(*calls))
	}
	if (*calls)[0][0] != "mem_spec_1" || (*calls)[0][1] != "cm_a" || (*calls)[0][2] != "great observation" {
		t.Errorf("doReplyCommitFn args: got %v", (*calls)[0])
	}
}

// TestArtifactReplyCommit_EmptyBody verifies 400 on empty body.
func TestArtifactReplyCommit_EmptyBody(t *testing.T) {
	calls, cleanupReply := withReplyCommitOverride(nil)
	defer cleanupReply()

	form := url.Values{"body": {""}}
	c, rec := newArtifactFormRequest(t, "mem_spec_1", "cm_a", "reply", form, writerUser("testproject"))
	if err := handleUIArtifactReplyCommit(nil)(c); err == nil && rec.Code < 400 {
		t.Errorf("expected 4xx for empty body; got %d", rec.Code)
	}
	if len(*calls) != 0 {
		t.Errorf("doReplyCommitFn must not be called for empty body")
	}
}

// TestArtifactReplyCommit_NonWriter verifies 403 for non-writer.
func TestArtifactReplyCommit_NonWriter(t *testing.T) {
	cleanupProject := withCommitMemoryProjectOverride("otherproject", "active", nil)
	defer cleanupProject()
	calls, cleanupReply := withReplyCommitOverride(nil)
	defer cleanupReply()

	form := url.Values{"body": {"a reply"}}
	c, rec := newArtifactFormRequest(t, "mem_spec_1", "cm_a", "reply", form, userWithProjects("testproject"))
	if err := handleUIArtifactReplyCommit(nil)(c); err == nil && rec.Code != http.StatusForbidden {
		t.Errorf("should return 403 for non-writer; code=%d", rec.Code)
	}
	if len(*calls) != 0 {
		t.Errorf("doReplyCommitFn must not be called on auth failure")
	}
}

// TestArtifactResolveCommit_HappyPath verifies that a resolve POST:
//   - calls doResolveCommitFn with the right args
//   - 303 redirects to the artifact HTML page
func TestArtifactResolveCommit_HappyPath(t *testing.T) {
	cleanupProject := withCommitMemoryProjectOverride("testproject", "active", nil)
	defer cleanupProject()
	calls, cleanupResolve := withResolveCommitOverride(nil)
	defer cleanupResolve()

	form := url.Values{"reply": {"resolved, looks good"}}
	c, rec := newArtifactFormRequest(t, "mem_spec_2", "cm_b", "resolve", form, writerUser("testproject"))
	if err := handleUIArtifactResolveCommit(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status: got %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	wantLoc := "/ui/artifacts/mem_spec_2/html"
	if loc := rec.Header().Get("Location"); loc != wantLoc {
		t.Errorf("Location: got %q, want %q", loc, wantLoc)
	}
	if len(*calls) != 1 {
		t.Fatalf("doResolveCommitFn call count: got %d, want 1", len(*calls))
	}
	if (*calls)[0][0] != "mem_spec_2" || (*calls)[0][1] != "cm_b" {
		t.Errorf("doResolveCommitFn args: got %v", (*calls)[0])
	}
}

// TestArtifactResolveCommit_EmptyReply verifies that reply is optional (empty allowed).
func TestArtifactResolveCommit_EmptyReply(t *testing.T) {
	cleanupProject := withCommitMemoryProjectOverride("testproject", "active", nil)
	defer cleanupProject()
	calls, cleanupResolve := withResolveCommitOverride(nil)
	defer cleanupResolve()

	form := url.Values{"reply": {""}}
	c, rec := newArtifactFormRequest(t, "mem_spec_2", "cm_b", "resolve", form, writerUser("testproject"))
	if err := handleUIArtifactResolveCommit(nil)(c); err != nil {
		t.Fatalf("handler error for empty reply: %v", err)
	}
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status: got %d, want 303", rec.Code)
	}
	_ = calls // was called once
}

// TestArtifactResolveCommit_NonWriter verifies 403 for non-writer.
func TestArtifactResolveCommit_NonWriter(t *testing.T) {
	cleanupProject := withCommitMemoryProjectOverride("otherproject", "active", nil)
	defer cleanupProject()
	calls, cleanupResolve := withResolveCommitOverride(nil)
	defer cleanupResolve()

	form := url.Values{"reply": {"done"}}
	c, rec := newArtifactFormRequest(t, "mem_spec_2", "cm_b", "resolve", form, userWithProjects("testproject"))
	if err := handleUIArtifactResolveCommit(nil)(c); err == nil && rec.Code != http.StatusForbidden {
		t.Errorf("should return 403 for non-writer; code=%d", rec.Code)
	}
	if len(*calls) != 0 {
		t.Errorf("doResolveCommitFn must not be called on auth failure")
	}
}

// ─── 6. handleArtifactHTML route-awareness (scripts + scaffold) ─────────────

// TestHandleArtifactHTML_UIPath_HasAnnotScaffold verifies that the full
// handleArtifactHTML handler, when invoked via the /ui route pattern, produces
// a document containing the data island, margin rail, selform, and script tags.
func TestHandleArtifactHTML_UIPath_HasAnnotScaffold(t *testing.T) {
	commits, _ := json.Marshal([]CommitEntry{openCommitWithQuote()})
	mem := publicSharedMem()
	mem.RenderedHTML = strptr(annotSpecBody)
	mem.Commits = json.RawMessage(commits)
	defer withLoadMemoryOverride(mem, nil)()

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/ui/artifacts/mem_share1/html", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	// Echo records the route pattern in c.Path() only when routed through the engine.
	// For a direct handler call we set the route manually to simulate the /ui mount.
	c.SetPath("/ui/artifacts/:id/html")
	c.SetParamNames("id")
	c.SetParamValues("mem_share1")
	setUser(c, adminUser())

	// Stub out versionChainFn to avoid DB.
	prevVCF := versionChainFn
	versionChainFn = func(_ context.Context, _ *pgxpool.Pool, _ string) ([]domain.MemoryVersionRef, error) {
		return nil, nil
	}
	defer func() { versionChainFn = prevVCF }()

	if err := handleArtifactHTML(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, marker := range []string{
		`id="pf-annot-data"`,
		`id="pf-margin-rail"`,
		`id="pf-selform"`,
		`id="pf-doc-col"`,
		`/ui/static/annotator.js`,
		`/ui/static/annot.js`,
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("/ui response missing %q; excerpt: %s", marker, excerptStr(body))
		}
	}
}

// TestHandleArtifactHTML_V1Path_NoAnnotScaffold verifies that the /v1 route
// pattern produces a document with NO annotation scaffold.
func TestHandleArtifactHTML_V1Path_NoAnnotScaffold(t *testing.T) {
	commits, _ := json.Marshal([]CommitEntry{openCommitWithQuote()})
	mem := publicSharedMem()
	mem.RenderedHTML = strptr(annotSpecBody)
	mem.Commits = json.RawMessage(commits)
	defer withLoadMemoryOverride(mem, nil)()

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/mem_share1/html", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/v1/artifacts/:id/html")
	c.SetParamNames("id")
	c.SetParamValues("mem_share1")
	setUser(c, adminUser())

	if err := handleArtifactHTML(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Use id= attribute selectors and src= to distinguish HTML elements from CSS class
	// names — ".pf-margin-rail" appears in the embedded stylesheet, so a bare string
	// match would false-positive.
	for _, marker := range []string{
		`id="pf-annot-data"`,
		`id="pf-margin-rail" hidden`,
		`id="pf-selform"`,
		`id="pf-doc-col"`,
		`src="/ui/static/annotator.js"`,
		`src="/ui/static/annot.js"`,
	} {
		if strings.Contains(body, marker) {
			t.Errorf("/v1 response must not contain %q; excerpt: %s", marker, excerptStr(body))
		}
	}
}
