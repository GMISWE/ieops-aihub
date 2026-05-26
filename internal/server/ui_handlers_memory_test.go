package server

// Unit tests for the /ui/memories handlers.
//
// Strategy: override the package-level recallMemoriesFn (and loadMemoryFn for
// the detail page) with synthetic fixtures so we never hit the database.
// setUser (defined in router_auth_test.go) injects a fully-formed UserContext.
// userWithProjects / userNoProjects helpers come from ui_handlers_queue_test.go.

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	if loc != "/v1/artifacts/mem_spec_42/html" {
		t.Errorf("Location: got %q, want %q", loc, "/v1/artifacts/mem_spec_42/html")
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

