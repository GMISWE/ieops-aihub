package server

// Unit tests for POST /v1/memories/:id/activate.
//
// Strategy: mirror handleV1ReplyCommit / handleResolveCommit tests — override
// commitMemoryProjectFn (to avoid DB hits when resolving the memory's project)
// and doActivateFn (the swappable wrapper around domain.Activate) to verify the
// handler enforces the project-writer gate BEFORE reinforcing the memory.
//
// Regression guard for aihub#146: handleActivateMemory previously called
// domain.Activate with no project-access check, letting any authed user
// strengthen/revive any memory (IDOR).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// newV1ActivateRequest builds a Bearer-authed POST for
// POST /v1/memories/:id/activate.
func newV1ActivateRequest(t *testing.T, memID string, uc *UserContext) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/memories/"+memID+"/activate", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(memID)
	if uc != nil {
		setUser(c, uc)
	}
	return c, rec
}

// withDoActivateOverride replaces doActivateFn for the duration of a test.
func withDoActivateOverride(resp *domain.ActivateResponse, err error) (func(), *bool) {
	called := false
	prev := doActivateFn
	doActivateFn = func(_ context.Context, _ *pgxpool.Pool, _, _, _ string) (*domain.ActivateResponse, error) {
		called = true
		return resp, err
	}
	return func() { doActivateFn = prev }, &called
}

// TestV1Activate_Success verifies an authorized writer reaches domain.Activate
// and gets a 200 with the activation response.
func TestV1Activate_Success(t *testing.T) {
	cleanupProject := withCommitMemoryProjectOverride("testproject", "active", nil)
	defer cleanupProject()

	want := &domain.ActivateResponse{ActivationCount: 3, NewStabilityDays: 12.5, EffectiveStrength: 2.1}
	cleanupActivate, called := withDoActivateOverride(want, nil)
	defer cleanupActivate()

	c, rec := newV1ActivateRequest(t, "mem_v1", writerUser("testproject"))
	if err := handleActivateMemory(nil)(c); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !*called {
		t.Errorf("domain.Activate must be called for an authorized writer")
	}
	var got domain.ActivateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response not JSON: %v; body=%s", err, rec.Body.String())
	}
	if got.ActivationCount != want.ActivationCount {
		t.Errorf("activation_count: got %d, want %d", got.ActivationCount, want.ActivationCount)
	}
}

// TestV1Activate_NonMember verifies a caller without any role on the memory's
// project gets 403 and never reaches domain.Activate (the IDOR regression guard).
func TestV1Activate_NonMember(t *testing.T) {
	cleanupProject := withCommitMemoryProjectOverride("otherproject", "active", nil)
	defer cleanupProject()

	cleanupActivate, called := withDoActivateOverride(&domain.ActivateResponse{}, nil)
	defer cleanupActivate()

	// userWithProjects("testproject") has no role on "otherproject".
	c, rec := newV1ActivateRequest(t, "mem_v1", userWithProjects("testproject"))
	// 🔴 aihub#377: was `if err := h(c); err == nil && rec.Code != http.StatusForbidden`,
	// which could never fire — checkProjectAccess returns non-nil, so the status
	// comparison was dead code and 403/404/200/500 all passed. assertNotVisibleDenial
	// checks the real denial AND proves its own predicate can still reject a wrong one.
	err := handleActivateMemory(nil)(c)
	assertNotVisibleDenial(t, err, rec, "otherproject")
	if *called {
		t.Errorf("domain.Activate must NOT be called on auth failure (IDOR guard)")
	}
}

// TestV1Activate_MemoryNotFound verifies that a missing memory returns 404
// before any access check or reinforcement.
func TestV1Activate_MemoryNotFound(t *testing.T) {
	cleanupProject := withCommitMemoryProjectOverride("", "", fmt.Errorf("no rows"))
	defer cleanupProject()

	cleanupActivate, called := withDoActivateOverride(&domain.ActivateResponse{}, nil)
	defer cleanupActivate()

	c, rec := newV1ActivateRequest(t, "mem_missing", writerUser("testproject"))
	if err := handleActivateMemory(nil)(c); err == nil && rec.Code != http.StatusNotFound {
		t.Errorf("should return 404 for missing memory; code=%d body=%s", rec.Code, rec.Body.String())
	}
	if *called {
		t.Errorf("domain.Activate must NOT be called when memory is not found")
	}
}
