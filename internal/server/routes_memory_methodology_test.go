package server

// aihub#210: methodology.* memory writes require a wi-bound attempt credential.
// These verify the pre-DB guards in handleRemember fire (400 missing wi / 403
// missing creds) BEFORE any database access — a nil pool is passed, so reaching
// the DB would panic. Mirrors router_auth_test.go's auth-before-write strategy.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// methodologyWriterUser has writer access to "testproject" so checkProjectAccess
// passes and execution reaches the methodology gate.
func methodologyWriterUser() *UserContext {
	return &UserContext{
		UserID:       "u_writer",
		DisplayName:  "Writer User",
		UserType:     "human",
		Role:         "writer",
		ProjectRoles: map[string]string{"testproject": "writer"},
		APIKeyID:     "k_writer",
	}
}

func postRememberNilPool(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/v1/memories", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	setUser(c, methodologyWriterUser())
	if err := handleRemember(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	return rec
}

func TestRemember_MethodologyRequiresWorkItem(t *testing.T) {
	rec := postRememberNilPool(t,
		`{"project":"testproject","type":"methodology.spec","content":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("methodology.* without work_item_id: expected 400, got %d (body: %s)",
			rec.Code, rec.Body.String())
	}
}

func TestRemember_MethodologyRequiresCredentials(t *testing.T) {
	rec := postRememberNilPool(t,
		`{"project":"testproject","type":"methodology.spec","content":"x","work_item_id":"wi_x"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("methodology.* without attempt credentials: expected 403, got %d (body: %s)",
			rec.Code, rec.Body.String())
	}
}

// TestEnforceMethodologyAttemptGate covers the shared mutate-time gate used by
// handleReinforceMemory / handleUpdateMemory (aihub#210 bypass fix). Only the
// reject-before-verify branches are exercised (a nil pool is passed, so the
// VerifyAttemptCredentialPool path — which needs a DB — is intentionally not hit).
func TestEnforceMethodologyAttemptGate(t *testing.T) {
	wi := "wi_target"
	t.Run("methodology target without wi -> 403", func(t *testing.T) {
		e := enforceMethodologyAttemptGate(context.TODO(), nil, "methodology.spec", nil, "ra_x", "sekret", 1, "")
		if e == nil || e.Code != domain.ErrForbidden {
			t.Fatalf("want ErrForbidden, got %v", e)
		}
	})
	t.Run("methodology without credentials -> 403", func(t *testing.T) {
		e := enforceMethodologyAttemptGate(context.TODO(), nil, "methodology.plan", &wi, "", "", 0, "")
		if e == nil || e.Code != domain.ErrForbidden {
			t.Fatalf("want ErrForbidden, got %v", e)
		}
	})
	t.Run("non-methodology without credentials -> allowed", func(t *testing.T) {
		if e := enforceMethodologyAttemptGate(context.TODO(), nil, "experience.debug", nil, "", "", 0, ""); e != nil {
			t.Fatalf("want nil (verify-if-supplied skips), got %v", e)
		}
	})
	t.Run("non-methodology creds without work_item_id -> 400", func(t *testing.T) {
		e := enforceMethodologyAttemptGate(context.TODO(), nil, "fact.note", nil, "ra_x", "sekret", 1, "")
		if e == nil || e.Code != domain.ErrBadRequest {
			t.Fatalf("want ErrBadRequest, got %v", e)
		}
	})
}
