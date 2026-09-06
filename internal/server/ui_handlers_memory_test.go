package server

// Unit tests for the /ui/memories handlers.
//
// Strategy: override the package-level recallMemoriesFn (and loadMemoryFn for
// the detail page) with synthetic fixtures so we never hit the database.
// setUser (defined in router_auth_test.go) injects a fully-formed UserContext.
// userWithProjects / userNoProjects helpers come from ui_handlers_queue_test.go.

import (
	"context"
	"encoding/json"
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
	// aihub#289 added a SECOND database call to handleUIMemories — the unmatched-type
	// diagnostic. This helper's contract is "this handler does not reach the database",
	// so it has to cover both, or every existing caller that passes a nil pool and a
	// type filter starts nil-dereferencing. A test that wants to assert ON the
	// diagnostic layers withUnmatchedOverride over this one (see ui_memory_types_test).
	prevUnmatched := unmatchedTypesFn
	unmatchedTypesFn = func(_ context.Context, _ *pgxpool.Pool, _ *domain.RecallRequest) ([]string, string) {
		return nil, ""
	}
	return &got, func() {
		recallMemoriesFn = prev
		unmatchedTypesFn = prevUnmatched
	}
}

// withUnmatchedStub silences the aihub#289 unmatched-type diagnostic, for tests that
// swap recallMemoriesFn by hand instead of going through withRecallOverride. Returns the
// restore func, so the idiom is `defer withUnmatchedStub()()`.
func withUnmatchedStub() func() {
	prev := unmatchedTypesFn
	unmatchedTypesFn = func(_ context.Context, _ *pgxpool.Pool, _ *domain.RecallRequest) ([]string, string) {
		return nil, ""
	}
	return func() { unmatchedTypesFn = prev }
}

// withLoadMemoryOverride swaps loadMemoryFn for the duration of a test.
func withLoadMemoryOverride(mem *domain.Memory, aerr *domain.AihubError) func() {
	prev := loadMemoryFn
	loadMemoryFn = func(_ context.Context, _ *pgxpool.Pool, _ string) (*domain.Memory, *domain.AihubError) {
		return mem, aerr
	}
	return func() { loadMemoryFn = prev }
}

// withResolveLatestOverride swaps resolveLatestFn (the GetLatestByID seam
// used by the aihub#248 /ui lineage-head redirect) for the duration of a
// test. Same shape as withLoadMemoryOverride, kept separate so a test can
// inject a head that differs from whatever loadMemoryFn returns for the
// originally-requested id.
//
// aihub#248 review (W3): the previous version of this fake discarded the id
// argument entirely (`_ string`), so a handler bug that resolved the WRONG id
// (e.g. a head's own id instead of the originally-requested mem's id) could
// not be caught by this seam — only the eventual Location/body assertions
// happened to recover most of that value. wantID makes the fake itself assert
// on the argument it was actually invoked with, per plan step 4's id-
// dispatching-fake requirement.
func withResolveLatestOverride(t *testing.T, wantID string, mem *domain.Memory, aerr *domain.AihubError) func() {
	t.Helper()
	prev := resolveLatestFn
	resolveLatestFn = func(_ context.Context, _ *pgxpool.Pool, id string) (*domain.Memory, *domain.AihubError) {
		if id != wantID {
			t.Errorf("resolveLatestFn called with id %q, want %q", id, wantID)
		}
		return mem, aerr
	}
	return func() { resolveLatestFn = prev }
}

// withResolveLatestCounter swaps resolveLatestFn with a fake that returns
// (mem, aerr) and increments *calls on every invocation — used by tests that
// must assert resolveLatestFn is (or is not) called a specific number of
// times (e.g. the no-op LatestID==nil / LatestID==ID cases must not trigger
// an extra lookup). wantID (aihub#248 review W3) is asserted against on any
// call that does happen, same rationale as withResolveLatestOverride.
func withResolveLatestCounter(t *testing.T, wantID string, mem *domain.Memory, aerr *domain.AihubError, calls *int) func() {
	t.Helper()
	prev := resolveLatestFn
	resolveLatestFn = func(_ context.Context, _ *pgxpool.Pool, id string) (*domain.Memory, *domain.AihubError) {
		*calls++
		if id != wantID {
			t.Errorf("resolveLatestFn called with id %q, want %q", id, wantID)
		}
		return mem, aerr
	}
	return func() { resolveLatestFn = prev }
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
	// aihub#289: this test swaps the recall directly rather than via
	// withRecallOverride, so it must stub the second DB seam itself — the handler now
	// also consults the unmatched-type diagnostic whenever a type filter is present.
	defer withUnmatchedStub()()
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

// ─── aihub#248: /ui lineage-head redirect for handleUIMemoryDetail ─────────

// newMemDetailRequest builds an echo context aimed at /ui/memories/:id,
// mirroring TestUIMemoryDetail_SpecRedirects's setup.
func newMemDetailRequest(t *testing.T, target, id string, uc *UserContext) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(id)
	setUser(c, uc)
	return c, rec
}

func TestUIMemoryDetail_LatestIDNil_NoRedirect(t *testing.T) {
	m := memFixture("mem_exp_nil", "experience.debug", "old body")
	cleanup := withLoadMemoryOverride(&m, nil)
	defer cleanup()
	calls := 0
	defer withResolveLatestCounter(t, "mem_exp_nil", nil, nil, &calls)()

	tmpl := pageTemplate("memory_detail.html.tmpl")
	c, rec := newMemDetailRequest(t, "/ui/memories/mem_exp_nil", "mem_exp_nil", userWithProjects("testproject"))

	if err := handleUIMemoryDetail(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("unexpected Location: %q", loc)
	}
	if calls != 0 {
		t.Errorf("resolveLatestFn calls: got %d, want 0 (LatestID nil must skip resolution)", calls)
	}
}

func TestUIMemoryDetail_LatestIDSelf_NoRedirect(t *testing.T) {
	m := memFixture("mem_exp_self", "experience.debug", "old body")
	self := m.ID
	m.LatestID = &self
	cleanup := withLoadMemoryOverride(&m, nil)
	defer cleanup()
	calls := 0
	defer withResolveLatestCounter(t, "mem_exp_self", nil, nil, &calls)()

	tmpl := pageTemplate("memory_detail.html.tmpl")
	c, rec := newMemDetailRequest(t, "/ui/memories/mem_exp_self", "mem_exp_self", userWithProjects("testproject"))

	if err := handleUIMemoryDetail(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if calls != 0 {
		t.Errorf("resolveLatestFn calls: got %d, want 0 (LatestID==ID must skip resolution)", calls)
	}
}

func TestUIMemoryDetail_SupersededVisibleHead_RedirectsToMemories(t *testing.T) {
	old := memFixture("mem_exp_old", "experience.debug", "old body")
	headID := "mem_exp_head"
	old.LatestID = &headID
	cleanup := withLoadMemoryOverride(&old, nil)
	defer cleanup()

	head := memFixture(headID, "experience.debug", "new body")
	self := headID
	head.LatestID = &self
	defer withResolveLatestOverride(t, "mem_exp_old", &head, nil)()

	tmpl := pageTemplate("memory_detail.html.tmpl")
	c, rec := newMemDetailRequest(t, "/ui/memories/mem_exp_old?back=/ui/queue", "mem_exp_old",
		userWithProjects("testproject"))

	if err := handleUIMemoryDetail(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusFound {
		t.Fatalf("status: got %d, want 302", rec.Code)
	}
	want := "/ui/memories/mem_exp_head?back=/ui/queue"
	if loc := rec.Header().Get("Location"); loc != want {
		t.Errorf("Location: got %q, want %q", loc, want)
	}
}

func TestUIMemoryDetail_SupersededHead_VisibilityDenied_Fallback(t *testing.T) {
	old := memFixture("mem_exp_old2", "experience.debug", "old body")
	headID := "mem_exp_head2"
	old.LatestID = &headID
	cleanup := withLoadMemoryOverride(&old, nil)
	defer cleanup()

	head := memFixture(headID, "experience.debug", "new body")
	head.Visibility = "private"
	head.AuthorUserID = "u_someone_else"
	defer withResolveLatestOverride(t, "mem_exp_old2", &head, nil)()

	tmpl := pageTemplate("memory_detail.html.tmpl")
	c, rec := newMemDetailRequest(t, "/ui/memories/mem_exp_old2", "mem_exp_old2", userWithProjects("testproject"))

	if err := handleUIMemoryDetail(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (silent fallback, no 403/404)", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("unexpected Location on fallback: %q", loc)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "old body") {
		t.Errorf("fallback should render the original record; body=%s", body[:min(len(body), 800)])
	}
	if strings.Contains(body, "new body") {
		t.Errorf("fallback must NOT leak the inaccessible head's content")
	}
}

func TestUIMemoryDetail_SupersededHead_ProjectDenied_Fallback(t *testing.T) {
	old := memFixture("mem_exp_old3", "experience.debug", "old body")
	headID := "mem_exp_head3"
	old.LatestID = &headID
	cleanup := withLoadMemoryOverride(&old, nil)
	defer cleanup()

	head := memFixture(headID, "experience.debug", "new body")
	head.Project = "otherproject" // caller has no role here
	defer withResolveLatestOverride(t, "mem_exp_old3", &head, nil)()

	tmpl := pageTemplate("memory_detail.html.tmpl")
	c, rec := newMemDetailRequest(t, "/ui/memories/mem_exp_old3", "mem_exp_old3", userWithProjects("testproject"))

	if err := handleUIMemoryDetail(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (silent fallback)", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("unexpected Location on fallback: %q", loc)
	}
}

func TestUIMemoryDetail_HeadResolutionError_Fallback(t *testing.T) {
	old := memFixture("mem_exp_old4", "experience.debug", "old body")
	headID := "mem_exp_head4"
	old.LatestID = &headID
	cleanup := withLoadMemoryOverride(&old, nil)
	defer cleanup()
	defer withResolveLatestOverride(t, "mem_exp_old4", nil, domain.NewErr(domain.ErrNotFound, "memory not found"))()

	tmpl := pageTemplate("memory_detail.html.tmpl")
	c, rec := newMemDetailRequest(t, "/ui/memories/mem_exp_old4", "mem_exp_old4", userWithProjects("testproject"))

	if err := handleUIMemoryDetail(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — never 404 on an unreachable head", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "old body") {
		t.Errorf("should render the original record on head-resolution error")
	}
}

// TestUIMemoryDetail_SupersededToSpec_OneHop covers AC7: a stale id whose
// resolved head is methodology.spec must redirect straight to
// /ui/artifacts/<head>/html in one hop, not via /ui/memories/<head> first
// (which would itself 302 again — two hops).
func TestUIMemoryDetail_SupersededToSpec_OneHop(t *testing.T) {
	old := memFixture("mem_old_to_spec", "experience.debug", "old body")
	headID := "mem_head_spec"
	old.LatestID = &headID
	cleanup := withLoadMemoryOverride(&old, nil)
	defer cleanup()

	head := memFixture(headID, "methodology.spec", "# new spec")
	self := headID
	head.LatestID = &self
	defer withResolveLatestOverride(t, "mem_old_to_spec", &head, nil)()

	tmpl := pageTemplate("memory_detail.html.tmpl")
	c, rec := newMemDetailRequest(t, "/ui/memories/mem_old_to_spec", "mem_old_to_spec",
		userWithProjects("testproject"))

	if err := handleUIMemoryDetail(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusFound {
		t.Fatalf("status: got %d, want 302", rec.Code)
	}
	want := "/ui/artifacts/mem_head_spec/html"
	if loc := rec.Header().Get("Location"); loc != want {
		t.Errorf("Location: got %q, want %q (one-hop straight to the artifact viewer)", loc, want)
	}
}

// TestUIMemoryDetail_SupersededSpecToNonSpec_NoWrongTypeRedirect covers the
// inverse of AC7: a stale methodology.spec id whose head is now a non-spec
// type must NOT be redirected to the artifact viewer (that would render the
// wrong template for the head's actual type) — it goes to the head's own
// /ui/memories/<head> page instead.
func TestUIMemoryDetail_SupersededSpecToNonSpec_NoWrongTypeRedirect(t *testing.T) {
	old := memFixture("mem_old_spec", "methodology.spec", "# old spec")
	headID := "mem_head_nonspec"
	old.LatestID = &headID
	cleanup := withLoadMemoryOverride(&old, nil)
	defer cleanup()

	head := memFixture(headID, "experience.debug", "converted to a debug note")
	self := headID
	head.LatestID = &self
	defer withResolveLatestOverride(t, "mem_old_spec", &head, nil)()

	tmpl := pageTemplate("memory_detail.html.tmpl")
	c, rec := newMemDetailRequest(t, "/ui/memories/mem_old_spec", "mem_old_spec", userWithProjects("testproject"))

	if err := handleUIMemoryDetail(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusFound {
		t.Fatalf("status: got %d, want 302", rec.Code)
	}
	want := "/ui/memories/mem_head_nonspec"
	if loc := rec.Header().Get("Location"); loc != want {
		t.Errorf("Location: got %q, want %q (must not send a non-spec head to the artifact viewer)", loc, want)
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
	// 🔴 aihub#377: was `if err := h(c); err == nil && rec.Code != http.StatusForbidden`,
	// which could never fire — checkProjectAccess returns non-nil, so the status
	// comparison was dead code and 403/404/200/500 all passed. assertNotVisibleDenial
	// checks the real denial AND proves its own predicate can still reject a wrong one.
	err := handleUICommitMemory(nil)(c)
	assertNotVisibleDenial(t, err, rec, "otherproject")
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
	// 🔴 aihub#377: was `if err := h(c); err == nil && rec.Code != http.StatusForbidden`,
	// which could never fire — checkProjectAccess returns non-nil, so the status
	// comparison was dead code and 403/404/200/500 all passed. assertNotVisibleDenial
	// checks the real denial AND proves its own predicate can still reject a wrong one.
	err := handleUIReplyCommit(nil)(c)
	assertNotVisibleDenial(t, err, rec, "otherproject")
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

// d2FixtureContent is an architecture-doc shaped body: prose, a d2 diagram, and
// a non-d2 code block. The d2 block must come out as inline SVG; the go block
// must be left alone.
const d2FixtureContent = "# Gateway overview\n\n" +
	"Request path:\n\n" +
	"```d2\nclient -> gateway: request\ngateway -> upstream: proxy\n```\n\n" +
	"And some code:\n\n" +
	"```go\nfunc main() {}\n```\n"

// TestUIMemoryDetail_D2CompilesForNonMethodologyType is the end-to-end guard for
// aihub#231: a fact.* memory does NOT redirect to the artifact viewer, so before
// the fix its d2 fence reached the browser as raw <code class="language-d2">
// source. Asserting on the real handler + real template response body (not just
// the md FuncMap in isolation) is what actually proves the user-visible bug is
// gone -- mem_smmQ3OyW: a green unit test on a helper is not evidence that the
// page works.
// TestUIMemoryDetail_TwinPairViewSwitch covers the rule that the agent's HTML is what a
// reader lands on, and that the markdown twin is one click away on the SAME page.
//
// Both halves matter and they fail differently. Defaulting to the markdown (the old
// behaviour) means the finished artifact the agent authored is never what anyone sees
// unless they know to go looking for it. Sending the reader to /ui/artifacts/<id>/html for
// the other half — the previous "Agent HTML →" link — means the comparison costs a
// navigation and drops the page's Comments and Details on the way.
//
// The assertions deliberately reach into the frame's srcdoc: both halves render inside the
// same sandbox, so "which half is on screen" is only answerable from the inner document.
func TestUIMemoryDetail_TwinPairViewSwitch(t *testing.T) {
	const mdHalf = "# markdown twin\n\nthe source half, in *markdown*.\n"
	// No data-* marker on the html half: SanitizeArtifactHTML drops attributes outside its
	// allowlist, so a marker attribute would vanish and the assertions below would compare
	// against bytes the sanitizer already removed. Distinct TEXT survives, and is what tells
	// the two halves apart.
	const htmlHalf = `<h1>html twin</h1><p>the authored half.</p>`

	// fact.architecture is the type the D7 gate treats as agent-authored: it is absent from
	// renderTypes, so a non-NULL rendered_html on it cannot have been auto-filled.
	newFixture := func() domain.Memory {
		mem := memFixture("mem_twin", "fact.architecture", mdHalf)
		h := htmlHalf
		mem.RenderedHTML = &h
		return mem
	}

	get := func(t *testing.T, query string) string {
		t.Helper()
		mem := newFixture()
		cleanup := withLoadMemoryOverride(&mem, nil)
		defer cleanup()

		tmpl := pageTemplate("memory_detail.html.tmpl")
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/ui/memories/"+mem.ID+query, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(mem.ID)
		setUser(c, userWithProjects("testproject"))

		if err := handleUIMemoryDetail(nil, tmpl)(c); err != nil {
			t.Fatalf("handler error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200 — this page must render both halves itself, "+
				"not redirect to the artifact viewer", rec.Code)
		}
		return rec.Body.String()
	}

	t.Run("bare URL shows the agent html", func(t *testing.T) {
		body := get(t, "")
		doc := innerDoc(t, body)
		if !strings.Contains(doc, "the authored half") {
			t.Errorf("default view is not the agent's html half; inner document was:\n%s", doc)
		}
		if strings.Contains(doc, "the source half") {
			t.Error("default view rendered the markdown twin as well as the html half")
		}
		if !strings.Contains(body, `class="pf-seg-item on"`) {
			t.Error("the view switch does not mark a current half")
		}
	})

	t.Run("?view=md shows the markdown twin", func(t *testing.T) {
		doc := innerDoc(t, get(t, "?view=md"))
		if !strings.Contains(doc, "the source half") {
			t.Errorf("?view=md did not render the markdown twin; inner document was:\n%s", doc)
		}
		if strings.Contains(doc, "the authored half") {
			t.Error("?view=md still rendered the agent's html half")
		}
	})

	t.Run("?source=1 shows the markdown unrendered", func(t *testing.T) {
		body := get(t, "?source=1")
		// The raw branch emits no frame at all, so assert on the page body. The markdown
		// must arrive escaped rather than rendered — that is the whole point of the view.
		if !strings.Contains(body, `<pre class="pf-content-raw">`) {
			t.Error("?source=1 did not take the unrendered branch")
		}
		if strings.Contains(body, "the authored half") {
			t.Error("?source=1 rendered the html half; source is the markdown's source, not the page's")
		}
	})

	t.Run("no switch when rendered_html was not agent-authored", func(t *testing.T) {
		mem := memFixture("mem_plain", "experience.pitfall", mdHalf)
		h := "<p>auto-rendered from the markdown</p>"
		mem.RenderedHTML = &h
		cleanup := withLoadMemoryOverride(&mem, nil)
		defer cleanup()

		tmpl := pageTemplate("memory_detail.html.tmpl")
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/ui/memories/"+mem.ID, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(mem.ID)
		setUser(c, userWithProjects("testproject"))
		if err := handleUIMemoryDetail(nil, tmpl)(c); err != nil {
			t.Fatalf("handler error: %v", err)
		}
		body := rec.Body.String()
		if strings.Contains(body, "pf-seg-toggle") {
			t.Error("offered a twin-pair switch for a type whose rendered_html is auto-filled — " +
				"the two halves would be the same content, and the switch a lie")
		}
		if !strings.Contains(innerDoc(t, body), "the source half") {
			t.Error("a non-twin memory should still render its markdown")
		}
	})
}

func TestUIMemoryDetail_D2CompilesForNonMethodologyType(t *testing.T) {
	for _, memType := range []string{"fact.architecture", "experience.pitfall", "rule.process"} {
		t.Run(memType, func(t *testing.T) {
			mem := memFixture("mem_d2_"+memType, memType, d2FixtureContent)
			cleanup := withLoadMemoryOverride(&mem, nil)
			defer cleanup()

			tmpl := pageTemplate("memory_detail.html.tmpl")
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/ui/memories/"+mem.ID, nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetParamNames("id")
			c.SetParamValues(mem.ID)
			setUser(c, userWithProjects("testproject"))

			if err := handleUIMemoryDetail(nil, tmpl)(c); err != nil {
				t.Fatalf("handler error: %v", err)
			}
			// Guard the premise: these types must render in place, not 302 to
			// the artifact viewer. If this ever starts redirecting, the d2
			// assertions below would pass vacuously on an empty body.
			if rec.Code != http.StatusOK {
				t.Fatalf("status: got %d, want 200 (%s must render in place, not redirect)", rec.Code, memType)
			}
			// aihub#240: the rendered content lives in a sandboxed iframe's srcdoc now, so
			// assert against the frame's inner document. Matching the page body directly
			// would compare against attribute-escaped bytes — which is why the chroma check
			// below is the one that noticed: `<pre class="chroma">` becomes
			// `<pre class=&#34;chroma&#34;` in an attribute value.
			doc := innerDoc(t, rec.Body.String())

			// Assert on data-d2-version, NOT on a bare "<svg": the page chrome
			// ships its own inline icon SVGs, so a "<svg" check would pass
			// vacuously even with the fix reverted.
			if n := strings.Count(doc, "data-d2-version"); n != 1 {
				t.Errorf("%s detail page: got %d d2-rendered SVGs, want exactly 1", memType, n)
			}
			if strings.Contains(doc, "language-d2") {
				t.Errorf("%s detail page still contains a raw language-d2 code block", memType)
			}
			// Collateral-damage guard: the non-d2 fence must survive untouched.
			// chroma tokenizes it, so match on the highlighted spans rather
			// than the raw source text.
			if !strings.Contains(doc, `<pre class="chroma">`) || !strings.Contains(doc, `<span class="nf">main</span>`) {
				t.Errorf("%s detail page lost or mangled the non-d2 go code block", memType)
			}
		})
	}
}

// TestUIMemoryDetail_D2CompilesWhenContentStartsWithProse closes the gap the
// aihub#231 code review found: RenderAsMD comes from looksLikeMarkdown, which
// used to test only the FIRST characters of the body. An architecture note that
// opens with a sentence and only then draws a diagram took the raw <pre> branch,
// so md -- and therefore RenderDiagramsForUI -- never ran and the reader still
// saw d2 source. Every other d2 fixture in this file opens with "# ", so none of
// them can see this.
func TestUIMemoryDetail_D2CompilesWhenContentStartsWithProse(t *testing.T) {
	proseFirst := "The ieops gateway routes every request through three stages.\n\n" +
		"```d2\nclient -> gateway: request\n```\n"
	mem := memFixture("mem_d2_prose", "fact.architecture", proseFirst)
	cleanup := withLoadMemoryOverride(&mem, nil)
	defer cleanup()

	tmpl := pageTemplate("memory_detail.html.tmpl")
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/ui/memories/mem_d2_prose", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("mem_d2_prose")
	setUser(c, userWithProjects("testproject"))

	if err := handleUIMemoryDetail(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	if strings.Contains(body, `<pre class="pf-content-raw">`) {
		t.Errorf("prose-first content took the raw <pre> branch; looksLikeMarkdown missed the d2 fence")
	}
	if n := strings.Count(body, "data-d2-version"); n != 1 {
		t.Errorf("got %d d2-rendered SVGs, want exactly 1", n)
	}
	if strings.Contains(body, "language-d2") {
		t.Errorf("still contains a raw language-d2 code block")
	}
}

// TestLooksLikeMarkdown_D2Fence unit-pins the heuristic change, including the
// negative cases that keep raw logs / JSON on the <pre> path.
func TestLooksLikeMarkdown_D2Fence(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"prose then d2 fence", "Overview text.\n\n```d2\na -> b\n```\n", true},
		{"indented d2 fence", "Text\n\n  ```d2\na -> b\n```\n", true},
		{"uppercase info string", "Text\n\n```D2\na -> b\n```\n", true},
		{"leading heading still works", "# Title\nbody", true},
		{"plain prose stays raw", "just a sentence about things", false},
		{"json payload stays raw", "{\"key\": \"value\", \"n\": 1}", false},
		{"non-d2 fence mid-body stays raw", "prose\n\n```go\nfunc main() {}\n```\n", false},
		{"inline mention is not a fence", "we write diagrams as ```d2 blocks inline", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeMarkdown(tc.in); got != tc.want {
				t.Errorf("looksLikeMarkdown(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestUIMemories_ListDoesNotCompileD2 pins the CPU-cost boundary from aihub#231:
// d2 layout runs goja, so it must happen on the detail page only. The list view
// truncates content as plain text and must never emit an SVG.
func TestUIMemories_ListDoesNotCompileD2(t *testing.T) {
	mem := memFixture("mem_d2_list", "fact.architecture", d2FixtureContent)
	_, cleanup := withRecallOverride([]domain.MemoryWithStrength{
		{Memory: mem, EffectiveStrength: 3.0},
	})
	defer cleanup()

	tmpl := pageTemplate("memories.html.tmpl")
	c, rec := newMemoriesRequest(t, "/ui/memories", userWithProjects("testproject"))

	if err := handleUIMemories(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "<svg viewBox") || strings.Contains(body, "data-d2-version") {
		t.Errorf("memory list page compiled a d2 diagram; it must stay plain truncated text")
	}
}

// ─── aihub#248 review_fix: exact-version marker (pf_exact) + W1/W2 ─────────
//
// Spec amendment (mem_Vcc8Jf6M superseded following deep review mem_eCIctvsx):
// non-goal 6 now permits a narrow exact-version marker so the two deliberate
// past-version link sites can still reach a specific superseded revision
// without the /ui redirect bouncing them back to the lineage head.

// TestUIMemoryDetail_ExactVersionMarker_SkipsRedirect covers the blocking
// finding's fix for handleUIMemoryDetail: pf_exact=1 must skip head
// resolution entirely, rendering the exact requested (non-spec/plan) record.
func TestUIMemoryDetail_ExactVersionMarker_SkipsRedirect(t *testing.T) {
	old := memFixture("mem_exp_exact1", "experience.debug", "old body")
	headID := "mem_exp_exact1_head"
	old.LatestID = &headID
	cleanup := withLoadMemoryOverride(&old, nil)
	defer cleanup()
	calls := 0
	defer withResolveLatestCounter(t, "mem_exp_exact1", nil, nil, &calls)()

	tmpl := pageTemplate("memory_detail.html.tmpl")
	c, rec := newMemDetailRequest(t, "/ui/memories/mem_exp_exact1?pf_exact=1", "mem_exp_exact1",
		userWithProjects("testproject"))

	if err := handleUIMemoryDetail(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("pf_exact=1 must skip the redirect entirely; got Location %q", loc)
	}
	if !strings.Contains(rec.Body.String(), "old body") {
		t.Errorf("pf_exact=1 must render the exact requested version")
	}
	if calls != 0 {
		t.Errorf("resolveLatestFn calls: got %d, want 0 (pf_exact=1 must skip head resolution)", calls)
	}
}

// TestUIMemoryDetail_HeadLatestID_PointsElsewhere_NoRedirect covers W2 for
// handleUIMemoryDetail: a head whose own cursor points somewhere else must
// not trigger a redirect — the handler falls back to the original record.
func TestUIMemoryDetail_HeadLatestID_PointsElsewhere_NoRedirect(t *testing.T) {
	old := memFixture("mem_exp_w2", "experience.debug", "old body")
	headID := "mem_exp_w2_head"
	old.LatestID = &headID
	cleanup := withLoadMemoryOverride(&old, nil)
	defer cleanup()

	head := memFixture(headID, "experience.debug", "new body")
	elsewhere := "mem_exp_w2_elsewhere"
	head.LatestID = &elsewhere // head's own cursor points away — must not redirect
	defer withResolveLatestOverride(t, "mem_exp_w2", &head, nil)()

	tmpl := pageTemplate("memory_detail.html.tmpl")
	c, rec := newMemDetailRequest(t, "/ui/memories/mem_exp_w2", "mem_exp_w2", userWithProjects("testproject"))

	if err := handleUIMemoryDetail(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (fallback)", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("a head whose own LatestID points elsewhere must not redirect again; got Location %q", loc)
	}
	if !strings.Contains(rec.Body.String(), "old body") {
		t.Errorf("fallback should render the original record")
	}
}

// TestUIMemoryDetail_SpecRedirect_ForwardsQueryString pins the deliberate
// (not reverted) behavior flagged as a minor review finding: the pre-existing
// spec/plan hand-off redirect forwards the query string even when no head
// was resolved. This is required now, not just inert, because pf_exact must
// survive this hop or an exact-version link that happens to land on
// /ui/memories/:id (e.g. a superseded spec/plan id) would lose its exactness
// the moment it hands off to /ui/artifacts/:id/html.
func TestUIMemoryDetail_SpecRedirect_ForwardsQueryString(t *testing.T) {
	spec := memFixture("mem_spec_qs", "methodology.spec", "# spec")
	rendered := "<h1>spec</h1>"
	spec.RenderedHTML = &rendered
	cleanup := withLoadMemoryOverride(&spec, nil)
	defer cleanup()

	tmpl := pageTemplate("memory_detail.html.tmpl")
	c, rec := newMemDetailRequest(t, "/ui/memories/mem_spec_qs?back=/ui/queue", "mem_spec_qs",
		userWithProjects("testproject"))

	if err := handleUIMemoryDetail(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusFound {
		t.Fatalf("status: got %d, want 302", rec.Code)
	}
	want := "/ui/artifacts/mem_spec_qs/html?back=/ui/queue"
	if loc := rec.Header().Get("Location"); loc != want {
		t.Errorf("Location: got %q, want %q (query string must survive this hop even with no head resolved)", loc, want)
	}
}

// TestUIMemoryDetail_RelatedLink_NoExactMarker locks down that the
// memory-detail page's related-memory link (memory_detail.html.tmpl:118) is
// an ordinary cross-link and must NOT carry the exact-version marker — only
// the two deliberate past-version link sites (side rail, wi-detail "View")
// may emit it.
func TestUIMemoryDetail_RelatedLink_NoExactMarker(t *testing.T) {
	exp := memFixture("mem_exp_related", "experience.debug", "body with a related link")
	exp.Attrs = json.RawMessage(`{"related_ids":["mem_related_target"]}`)
	cleanup := withLoadMemoryOverride(&exp, nil)
	defer cleanup()

	tmpl := pageTemplate("memory_detail.html.tmpl")
	c, rec := newMemDetailRequest(t, "/ui/memories/mem_exp_related", "mem_exp_related", userWithProjects("testproject"))

	if err := handleUIMemoryDetail(nil, tmpl)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/ui/artifacts/mem_related_target/html"`) {
		t.Errorf("expected related-memory link without the marker; body=%s", body[:min(len(body), 1200)])
	}
	if strings.Contains(body, "pf_exact") {
		t.Errorf("related-memory link must NOT carry the exact-version marker; body=%s", body[:min(len(body), 1200)])
	}
}
