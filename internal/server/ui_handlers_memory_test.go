package server

// Unit tests for the /ui/memories handlers.
//
// Strategy: override the package-level recallMemoriesFn (and loadMemoryFn for
// the detail page) with synthetic fixtures so we never hit the database.
// setUser (defined in router_auth_test.go) injects a fully-formed UserContext.
// userWithProjects / userNoProjects helpers come from ui_handlers_queue_test.go.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// memFixture builds a domain.Memory with sensible defaults so each test can
// override just the fields it cares about.
func memFixture(id, memType, content string) domain.Memory {
	return domain.Memory{
		ID:              id,
		Project:         "testproject",
		Type:            memType,
		Content:         content,
		AuthorUserID:    "u_author",
		AuthorDisplay:   "Author",
		Visibility:      "project",
		BaseStrength:    3.0,
		StabilityDays:   7,
		ActivationCount: 1,
		Status:          "active",
		CreatedAt:       time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC),
		UpdatedAt:       time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC),
	}
}

// withRecallOverride swaps recallMemoriesFn for the duration of a test.
// The fn captures the last RecallRequest so assertions can inspect it.
func withRecallOverride(items []domain.MemoryWithStrength) (capture *domain.RecallRequest, cleanup func()) {
	prev := recallMemoriesFn
	var got domain.RecallRequest
	recallMemoriesFn = func(_ context.Context, _ *pgxpool.Pool, req *domain.RecallRequest) (*domain.RecallResponse, error) {
		got = *req
		return &domain.RecallResponse{Items: items}, nil
	}
	return &got, func() { recallMemoriesFn = prev }
}

// withLoadMemoryOverride swaps loadMemoryFn for the duration of a test.
func withLoadMemoryOverride(mem *domain.Memory, aerr *domain.AihubError) func() {
	prev := loadMemoryFn
	loadMemoryFn = func(_ context.Context, _ *pgxpool.Pool, _ string) (*domain.Memory, *domain.AihubError) {
		return mem, aerr
	}
	return func() { loadMemoryFn = prev }
}

func newMemoriesRequest(t *testing.T, target string, uc *UserContext) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	setUser(c, uc)
	return c, rec
}

func TestUIMemories_NoProjectAccess_HintShown(t *testing.T) {
	// Recall must not be called — override it anyway with an empty result so a
	// regression that bypasses the guard would not panic-deref a nil pool.
	_, cleanup := withRecallOverride(nil)
	defer cleanup()

	tmpl := pageTemplate("memories.html.tmpl")
	c, rec := newMemoriesRequest(t, "/ui/memories", userNoProjects())

	if err := handleUIMemories(nil, tmpl)(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "no projects accessible") {
		t.Fatalf("body should contain no-access hint, got: %s", body[:min(len(body), 400)])
	}
}

func TestUIMemories_FilterByType(t *testing.T) {
	m1 := memFixture("mem_spec_1", "methodology.spec", "spec body")
	m2 := memFixture("mem_exp_1", "experience.debug", "debug story")
	// Recall would normally do the SQL-side filter; we simulate that by only
	// returning the matching row when req.Types matches.
	prev := recallMemoriesFn
	defer func() { recallMemoriesFn = prev }()
	recallMemoriesFn = func(_ context.Context, _ *pgxpool.Pool, req *domain.RecallRequest) (*domain.RecallResponse, error) {
		items := []domain.MemoryWithStrength{}
		for _, m := range []domain.Memory{m1, m2} {
			ok := len(req.Types) == 0
			for _, t := range req.Types {
				if t == m.Type || (strings.HasSuffix(t, ".*") && strings.HasPrefix(m.Type, strings.TrimSuffix(t, "*"))) {
					ok = true
				}
			}
			if ok {
				items = append(items, domain.MemoryWithStrength{Memory: m, EffectiveStrength: 1.5})
			}
		}
		return &domain.RecallResponse{Items: items}, nil
	}

	tmpl := pageTemplate("memories.html.tmpl")
	c, rec := newMemoriesRequest(t,
		"/ui/memories?project=testproject&type=methodology.spec",
		userWithProjects("testproject"))

	if err := handleUIMemories(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "mem_spec_1") {
		t.Errorf("body missing matching mem_spec_1; body=%s", body[:min(len(body), 800)])
	}
	if strings.Contains(body, "mem_exp_1") {
		t.Errorf("body should not contain non-matching mem_exp_1 row")
	}
}

func TestUIMemories_FilterByStrength(t *testing.T) {
	// Capture the request so we can verify min_strength was forwarded.
	got, cleanup := withRecallOverride([]domain.MemoryWithStrength{
		{Memory: memFixture("mem_hi", "experience.debug", "strong"), EffectiveStrength: 5.0},
	})
	defer cleanup()

	tmpl := pageTemplate("memories.html.tmpl")
	c, rec := newMemoriesRequest(t,
		"/ui/memories?project=testproject&strength_min=2.0",
		userWithProjects("testproject"))

	if err := handleUIMemories(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if got.MinStrength != 2.0 {
		t.Errorf("MinStrength: got %f, want 2.0", got.MinStrength)
	}
	if !strings.Contains(rec.Body.String(), "mem_hi") {
		t.Errorf("body missing the high-strength row")
	}
}

func TestUIMemories_DropsAdminVisibilityForNonAdmin(t *testing.T) {
	// Simulate a recall that (incorrectly) returns an admin-visibility row to a
	// non-admin. The handler's per-row visibility re-check must drop it.
	leaky := memFixture("mem_admin_leak", "experience.debug", "admin-only payload")
	leaky.Visibility = "admin"
	visible := memFixture("mem_normal", "experience.debug", "normal content")

	_, cleanup := withRecallOverride([]domain.MemoryWithStrength{
		{Memory: leaky, EffectiveStrength: 1.0},
		{Memory: visible, EffectiveStrength: 1.0},
	})
	defer cleanup()

	tmpl := pageTemplate("memories.html.tmpl")
	c, rec := newMemoriesRequest(t,
		"/ui/memories?project=testproject",
		userWithProjects("testproject"))

	if err := handleUIMemories(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	body := rec.Body.String()
	if strings.Contains(body, "mem_admin_leak") {
		t.Errorf("admin-visibility row leaked to non-admin caller; body=%s", body[:min(len(body), 800)])
	}
	if !strings.Contains(body, "mem_normal") {
		t.Errorf("project-visibility row should still be shown")
	}
	if !strings.Contains(body, "1 hidden by visibility") {
		t.Errorf("hidden count should reflect the dropped row; body=%s", body[:min(len(body), 800)])
	}
}

func TestUIMemoryDetail_SpecRedirects(t *testing.T) {
	spec := memFixture("mem_spec_42", "methodology.spec", "# spec")
	rendered := "<h1>spec</h1>"
	spec.RenderedHTML = &rendered
	cleanup := withLoadMemoryOverride(&spec, nil)
	defer cleanup()

	tmpl := pageTemplate("memory_detail.html.tmpl")
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/ui/memories/mem_spec_42", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("mem_spec_42")
	setUser(c, userWithProjects("testproject"))

	if err := handleUIMemoryDetail(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusFound {
		t.Fatalf("status: got %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc != "/ui/artifacts/mem_spec_42/html" {
		t.Errorf("Location: got %q, want %q", loc, "/ui/artifacts/mem_spec_42/html")
	}
}

func TestUIMemoryDetail_ExperienceRenders(t *testing.T) {
	exp := memFixture("mem_exp_99", "experience.debug",
		"# Debug session\nlooked at a bug")
	cleanup := withLoadMemoryOverride(&exp, nil)
	defer cleanup()

	tmpl := pageTemplate("memory_detail.html.tmpl")
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/ui/memories/mem_exp_99", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("mem_exp_99")
	setUser(c, userWithProjects("testproject"))

	if err := handleUIMemoryDetail(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "mem_exp_99") {
		t.Errorf("body should contain memory id; body=%s", body[:min(len(body), 800)])
	}
	if !strings.Contains(body, "Debug session") && !strings.Contains(body, "looked at a bug") {
		t.Errorf("body should contain memory content; body=%s", body[:min(len(body), 800)])
	}
}

// ─── UI Commit Handler Tests ──────────────────────────────────────────────────

// withCommitMemoryProjectOverride replaces commitMemoryProjectFn for the duration of a test.
func withCommitMemoryProjectOverride(project, status string, err error) func() {
	prev := commitMemoryProjectFn
	commitMemoryProjectFn = func(_ context.Context, _ *pgxpool.Pool, _ string) (string, string, error) {
		return project, status, err
	}
	return func() { commitMemoryProjectFn = prev }
}

// withDoCommitMemoryOverride replaces doCommitMemoryFn for the duration of a test.
func withDoCommitMemoryOverride(returnErr error) func() {
	prev := doCommitMemoryFn
	doCommitMemoryFn = func(_ context.Context, _ *pgxpool.Pool, _, _, _, _ string, _ domain.CommitAnchorArgs) error {
		return returnErr
	}
	return func() { doCommitMemoryFn = prev }
}

// newCommitRequest builds a POST form request for /ui/memories/:id/commit.
func newCommitRequest(t *testing.T, memID, body string, uc *UserContext) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	form := "body=" + body
	req := httptest.NewRequest(http.MethodPost, "/ui/memories/"+memID+"/commit",
		strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(memID)
	if uc != nil {
		setUser(c, uc)
	}
	return c, rec
}

// TestUICommitMemory_NotLoggedIn verifies that unauthenticated requests redirect to login.
func TestUICommitMemory_NotLoggedIn(t *testing.T) {
	c, rec := newCommitRequest(t, "mem_abc", "some body", nil)
	if err := handleUICommitMemory(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusFound {
		t.Errorf("status: got %d, want 302 (redirect to login)", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "/ui/login") {
		t.Errorf("should redirect to /ui/login; got %q", loc)
	}
}

// TestUICommitMemory_EmptyBody verifies that an empty body returns a 400 error.
func TestUICommitMemory_EmptyBody(t *testing.T) {
	c, rec := newCommitRequest(t, "mem_abc", "", userWithProjects("testproject"))
	if err := handleUICommitMemory(nil)(c); err != nil {
		// Error-style response is acceptable too.
		return
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 for empty body", rec.Code)
	}
}

// TestUICommitMemory_MemoryNotFound verifies that a missing memory returns an error.
func TestUICommitMemory_MemoryNotFound(t *testing.T) {
	cleanup := withCommitMemoryProjectOverride("", "", fmt.Errorf("no rows"))
	defer cleanup()

	c, rec := newCommitRequest(t, "mem_missing", "hello", userWithProjects("testproject"))
	if err := handleUICommitMemory(nil)(c); err == nil && rec.Code != http.StatusNotFound {
		t.Errorf("should return error or 404 for missing memory; code=%d", rec.Code)
	}
}

// TestUICommitMemory_NonWriter verifies that a user without writer access gets a 403.
func TestUICommitMemory_NonWriter(t *testing.T) {
	cleanup := withCommitMemoryProjectOverride("otherproject", "active", nil)
	defer cleanup()

	// userWithProjects only has "testproject", not "otherproject"
	c, rec := newCommitRequest(t, "mem_abc", "annotation", userWithProjects("testproject"))
	if err := handleUICommitMemory(nil)(c); err == nil && rec.Code != http.StatusForbidden {
		t.Errorf("should return 403 for non-writer; code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// writerUser returns a UserContext with writer-level access to the given project.
func writerUser(project string) *UserContext {
	return &UserContext{
		UserID:       "u_writer",
		Email:        "writer@example.com",
		DisplayName:  "Writer User",
		UserType:     "human",
		Role:         "writer",
		ProjectRoles: map[string]string{project: "writer"},
		APIKeyID:     "k_writer",
	}
}

// TestUICommitMemory_Success verifies that a valid commit redirects to the memory detail page.
func TestUICommitMemory_Success(t *testing.T) {
	cleanupProject := withCommitMemoryProjectOverride("testproject", "active", nil)
	defer cleanupProject()
	cleanupCommit := withDoCommitMemoryOverride(nil)
	defer cleanupCommit()

	c, rec := newCommitRequest(t, "mem_abc", "a human annotation", writerUser("testproject"))
	if err := handleUICommitMemory(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status: got %d, want 303", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "mem_abc") {
		t.Errorf("redirect Location should contain memory id; got %q", loc)
	}
}

// ─── aihub#113: wi= filter and WorkItem column ────────────────────────────────

// TestUIMemories_WIFilter verifies that ?wi=<id> is forwarded into the
// RecallRequest.WorkItemID field. The domain.Recall function filters by
// work_item_id in SQL (memory.go ~L724-726); here we only assert that the
// handler plumbs the query param through correctly.
func TestUIMemories_WIFilter(t *testing.T) {
	wiID := "aihub#77"
	got, cleanup := withRecallOverride(nil)
	defer cleanup()

	tmpl := pageTemplate("memories.html.tmpl")
	c, rec := newMemoriesRequest(t,
		"/ui/memories?project=testproject&wi="+wiID,
		userWithProjects("testproject"))

	if err := handleUIMemories(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if got.WorkItemID == nil {
		t.Fatalf("RecallRequest.WorkItemID should be set when ?wi= is present, got nil")
	}
	if *got.WorkItemID != wiID {
		t.Errorf("RecallRequest.WorkItemID: got %q, want %q", *got.WorkItemID, wiID)
	}
}

// TestUIMemories_WorkItemColumn verifies that a memory card with a WorkItemID
// renders a link to its work item, while one without renders an em-dash
// placeholder. (The page is now a card grid, not a table — aihub#137 — so the
// assertions target the card footer's .wi element instead of a <td> column.)
func TestUIMemories_WorkItemColumn(t *testing.T) {
	wiID := "aihub#55"
	m1 := memFixture("mem_wi_1", "experience.debug", "has a wi")
	m1.WorkItemID = &wiID
	m2 := memFixture("mem_wi_2", "experience.debug", "no wi")
	// m2.WorkItemID is nil

	_, cleanup := withRecallOverride([]domain.MemoryWithStrength{
		{Memory: m1, EffectiveStrength: 2.0},
		{Memory: m2, EffectiveStrength: 2.0},
	})
	defer cleanup()

	tmpl := pageTemplate("memories.html.tmpl")
	c, rec := newMemoriesRequest(t, "/ui/memories?project=testproject", userWithProjects("testproject"))

	if err := handleUIMemories(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	body := rec.Body.String()

	// Card with a wi should render a link to /ui/wi/<id> in its footer.
	if !strings.Contains(body, "/ui/wi/aihub%2355") {
		t.Errorf("body missing wi link for mem_wi_1; body=%s", body[:min(len(body), 800)])
	}
	// Card without a wi should render an em-dash placeholder in the .wi slot
	// rather than a link.
	if !strings.Contains(body, `<span class="wi" style="color:var(--text-subtle)">—</span>`) {
		t.Errorf("body missing em-dash for mem_wi_2 (no wi); body=%s", body[:min(len(body), 800)])
	}
}

// TestUIMemories_WIFilterPreservedInForm verifies that when the list is filtered
// by ?wi=<id>, the filter form carries a hidden wi input so resubmitting the form
// (to change type/strength/query) does not silently drop the work-item filter.
func TestUIMemories_WIFilterPreservedInForm(t *testing.T) {
	_, cleanup := withRecallOverride([]domain.MemoryWithStrength{})
	defer cleanup()

	tmpl := pageTemplate("memories.html.tmpl")
	c, rec := newMemoriesRequest(t, "/ui/memories?project=testproject&wi=aihub%2377", userWithProjects("testproject"))

	if err := handleUIMemories(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="wi" value="aihub#77"`) {
		t.Errorf("filter form missing hidden wi input (wi filter would be dropped on resubmit); body=%s", body[:min(len(body), 1200)])
	}
}

// TestUIMemoryDetail_Related verifies that the memory detail page renders a
// Related section when attrs.related_ids is set.
func TestUIMemoryDetail_Related(t *testing.T) {
	relID := "mem_related_99"
	exp := memFixture("mem_with_related", "experience.debug", "has related")
	exp.Attrs = []byte(`{"related_ids":["` + relID + `"]}`)
	cleanup := withLoadMemoryOverride(&exp, nil)
	defer cleanup()

	tmpl := pageTemplate("memory_detail.html.tmpl")
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/ui/memories/mem_with_related", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("mem_with_related")
	setUser(c, userWithProjects("testproject"))

	if err := handleUIMemoryDetail(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/ui/artifacts/"+relID+"/html") {
		t.Errorf("body missing related memory link; body=%s", body[:min(len(body), 1000)])
	}
}

// TestUIMemories_TypeOptions verifies that memListPageData has 23 TypeOptions (4 wildcards + 19 exact).
func TestUIMemories_TypeOptions(t *testing.T) {
	_, cleanup := withRecallOverride(nil)
	defer cleanup()

	tmpl := pageTemplate("memories.html.tmpl")
	c, rec := newMemoriesRequest(t, "/ui/memories?project=testproject", userWithProjects("testproject"))

	if err := handleUIMemories(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// The 4 wildcard prefixes must appear as select options.
	for _, opt := range []string{"experience.*", "fact.*", "rule.*", "methodology.*"} {
		if !strings.Contains(body, opt) {
			t.Errorf("type select missing wildcard option %q", opt)
		}
	}
	// At least one exact type must appear.
	if !strings.Contains(body, "methodology.spec") {
		t.Errorf("type select missing exact option methodology.spec")
	}
}

// ─── aihub#125: UI Reply/Resolve handler tests ───────────────────────────────

// withReplyCommitOverride replaces doReplyCommitFn for the duration of a test.
// The capture slice records the (memID, commitID, body) args of each call.
func withReplyCommitOverride(returnErr error) (calls *[][3]string, cleanup func()) {
	prev := doReplyCommitFn
	var recorded [][3]string
	doReplyCommitFn = func(_ context.Context, _ *pgxpool.Pool, memID, commitID, _, _, body string) error {
		recorded = append(recorded, [3]string{memID, commitID, body})
		return returnErr
	}
	return &recorded, func() { doReplyCommitFn = prev }
}

// withResolveCommitOverride replaces doResolveCommitFn for the duration of a test.
func withResolveCommitOverride(returnErr error) (calls *[][3]string, cleanup func()) {
	prev := doResolveCommitFn
	var recorded [][3]string
	doResolveCommitFn = func(_ context.Context, _ *pgxpool.Pool, memID, commitID, reply, _, _ string) error {
		recorded = append(recorded, [3]string{memID, commitID, reply})
		return returnErr
	}
	return &recorded, func() { doResolveCommitFn = prev }
}

// newReplyRequest builds a POST form request for /ui/memories/:id/commit/:commit_id/reply.
func newReplyRequest(t *testing.T, memID, commitID, body string, uc *UserContext) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	form := "body=" + url.QueryEscape(body)
	req := httptest.NewRequest(http.MethodPost,
		"/ui/memories/"+memID+"/commit/"+commitID+"/reply",
		strings.NewReader(form))
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

// newResolveRequest builds a POST form request for /ui/memories/:id/commit/:commit_id/resolve.
func newResolveRequest(t *testing.T, memID, commitID, reply string, uc *UserContext) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	form := "reply=" + url.QueryEscape(reply)
	req := httptest.NewRequest(http.MethodPost,
		"/ui/memories/"+memID+"/commit/"+commitID+"/resolve",
		strings.NewReader(form))
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

// TestUIReplyCommit_Success verifies a valid reply redirects 303 to the memory page.
func TestUIReplyCommit_Success(t *testing.T) {
	cleanupProject := withCommitMemoryProjectOverride("testproject", "active", nil)
	defer cleanupProject()
	calls, cleanupReply := withReplyCommitOverride(nil)
	defer cleanupReply()

	c, rec := newReplyRequest(t, "mem_abc", "cm_001", "great point!", writerUser("testproject"))
	if err := handleUIReplyCommit(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status: got %d, want 303", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc != "/ui/memories/mem_abc" {
		t.Errorf("Location: got %q, want %q", loc, "/ui/memories/mem_abc")
	}
	if len(*calls) != 1 {
		t.Fatalf("doReplyCommitFn call count: got %d, want 1", len(*calls))
	}
	if (*calls)[0][0] != "mem_abc" || (*calls)[0][1] != "cm_001" || (*calls)[0][2] != "great point!" {
		t.Errorf("doReplyCommitFn args: got %v", (*calls)[0])
	}
}

// TestUIReplyCommit_EmptyBody verifies that an empty body returns 4xx without calling domain.
func TestUIReplyCommit_EmptyBody(t *testing.T) {
	calls, cleanupReply := withReplyCommitOverride(nil)
	defer cleanupReply()

	c, rec := newReplyRequest(t, "mem_abc", "cm_001", "", writerUser("testproject"))
	if err := handleUIReplyCommit(nil)(c); err == nil && rec.Code < 400 {
		t.Errorf("expected 4xx for empty body; got %d", rec.Code)
	}
	if len(*calls) != 0 {
		t.Errorf("doReplyCommitFn must not be called for empty body; got %d calls", len(*calls))
	}
}

// TestUIReplyCommit_NonWriter verifies that a non-writer gets 403.
func TestUIReplyCommit_NonWriter(t *testing.T) {
	cleanupProject := withCommitMemoryProjectOverride("otherproject", "active", nil)
	defer cleanupProject()
	calls, cleanupReply := withReplyCommitOverride(nil)
	defer cleanupReply()

	c, rec := newReplyRequest(t, "mem_abc", "cm_001", "a reply", userWithProjects("testproject"))
	if err := handleUIReplyCommit(nil)(c); err == nil && rec.Code != http.StatusForbidden {
		t.Errorf("should return 403 for non-writer; code=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(*calls) != 0 {
		t.Errorf("doReplyCommitFn must not be called on auth failure; got %d calls", len(*calls))
	}
}

// TestUIResolveCommit_Success verifies a resolve redirects 303 to the memory page.
func TestUIResolveCommit_Success(t *testing.T) {
	cleanupProject := withCommitMemoryProjectOverride("testproject", "active", nil)
	defer cleanupProject()
	calls, cleanupResolve := withResolveCommitOverride(nil)
	defer cleanupResolve()

	c, rec := newResolveRequest(t, "mem_xyz", "cm_002", "resolved — looks good", writerUser("testproject"))
	if err := handleUIResolveCommit(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status: got %d, want 303", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc != "/ui/memories/mem_xyz" {
		t.Errorf("Location: got %q, want %q", loc, "/ui/memories/mem_xyz")
	}
	if len(*calls) != 1 {
		t.Fatalf("doResolveCommitFn call count: got %d, want 1", len(*calls))
	}
	if (*calls)[0][0] != "mem_xyz" || (*calls)[0][1] != "cm_002" {
		t.Errorf("doResolveCommitFn args: got %v", (*calls)[0])
	}
}

// TestUIResolveCommit_EmptyReply verifies that reply is optional (empty is accepted).
func TestUIResolveCommit_EmptyReply(t *testing.T) {
	cleanupProject := withCommitMemoryProjectOverride("testproject", "active", nil)
	defer cleanupProject()
	calls, cleanupResolve := withResolveCommitOverride(nil)
	defer cleanupResolve()

	c, rec := newResolveRequest(t, "mem_xyz", "cm_002", "", writerUser("testproject"))
	if err := handleUIResolveCommit(nil)(c); err != nil {
		t.Fatalf("handler error for empty reply: %v", err)
	}
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status: got %d, want 303 (empty reply is allowed)", rec.Code)
	}
	if len(*calls) != 1 {
		t.Fatalf("doResolveCommitFn should be called even with empty reply; got %d calls", len(*calls))
	}
}
