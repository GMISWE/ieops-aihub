package server

// Integration tests for aihub#249: GET /v1/memories/:id. Run against a live DB
// (gated by AIHUB_TEST_DB), following the seed/echo-context pattern from
// routes_step_test.go and routes_memory_activate_test.go.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/server/ -run 'TestHandleGetMemory' -v -count=1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// newGetMemoryRequest builds an authenticated GET /v1/memories/:id echo.Context.
func newGetMemoryRequest(t *testing.T, memID string, uc *UserContext) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/memories/"+memID, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(memID)
	if uc != nil {
		setUser(c, uc)
	}
	return c, rec
}

// seedGetTestMemory inserts a single memory row directly and returns its id.
func seedGetTestMemory(t *testing.T, pool *pgxpool.Pool, project, authorUserID, visibility, content string) string {
	t.Helper()
	memID := domain.NewID("mem")
	_, err := pool.Exec(context.Background(), `
		INSERT INTO memories (id, project, type, content, author_user_id, author_display,
			visibility, status, tags, attrs, base_strength, stability_days)
		VALUES ($1, $2, 'fact.note', $3, $4, $4, $5, 'active', '{}', '{}', 5, 36500)`,
		memID, project, content, authorUserID, visibility,
	)
	require.NoError(t, err)
	return memID
}

// TestHandleGetMemory_HappyPath verifies a viewer on the memory's project gets
// the full memory object back, including fields the list endpoint's lite scan
// omits (status, latest_id).
func TestHandleGetMemory_HappyPath(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	memID := seedGetTestMemory(t, pool, project, uid, "project", "hello world")

	uc := &UserContext{
		UserID:       uid,
		DisplayName:  uid,
		Role:         "writer",
		ProjectRoles: map[string]string{project: "viewer"},
	}
	c, rec := newGetMemoryRequest(t, memID, uc)
	require.NoError(t, handleGetMemory(pool)(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got domain.Memory
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, memID, got.ID)
	require.Equal(t, "active", got.Status)
	require.Equal(t, "hello world", got.Content)
}

// TestHandleGetMemory_NotFound verifies a nonexistent id returns 404.
func TestHandleGetMemory_NotFound(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)

	uc := &UserContext{
		UserID:       uid,
		DisplayName:  uid,
		Role:         "writer",
		ProjectRoles: map[string]string{project: "viewer"},
	}
	c, rec := newGetMemoryRequest(t, "mem_does_not_exist_aihub249", uc)
	require.NoError(t, handleGetMemory(pool)(c))
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestHandleGetMemory_AuthzDenied_PrivateNotAuthor is the core aihub#249
// correctness guard: a caller with project-viewer access, but who is not the
// author of a `private` memory, must be denied — with 404, not 403, so the
// endpoint doesn't confirm the memory's existence to someone who can't see it.
func TestHandleGetMemory_AuthzDenied_PrivateNotAuthor(t *testing.T) {
	pool := setupStepTestDB(t)
	ownerUID, project := seedStepTestUserAndProject(t, pool)
	memID := seedGetTestMemory(t, pool, project, ownerUID, "private", "shh secret")

	otherUID := "u_other_" + sanitizeStepTestName(t.Name())
	_, err := pool.Exec(context.Background(),
		`INSERT INTO users(id,email,display_name) VALUES($1,$1||'@test.local',$1) ON CONFLICT (id) DO NOTHING`, otherUID)
	require.NoError(t, err)

	uc := &UserContext{
		UserID:       otherUID,
		DisplayName:  otherUID,
		Role:         "writer",
		ProjectRoles: map[string]string{project: "viewer"},
	}
	c, rec := newGetMemoryRequest(t, memID, uc)
	require.NoError(t, handleGetMemory(pool)(c))
	require.Equal(t, http.StatusNotFound, rec.Code,
		"a private memory not authored by the caller must 404, not 403 or 200: %s", rec.Body.String())
}

// TestHandleGetMemory_AuthzDenied_NoProjectAccess verifies a caller with no
// role at all on the memory's project is denied with 404 (not 403), mirroring
// domain.Recall's project-access gate exactly.
func TestHandleGetMemory_AuthzDenied_NoProjectAccess(t *testing.T) {
	pool := setupStepTestDB(t)
	ownerUID, project := seedStepTestUserAndProject(t, pool)
	memID := seedGetTestMemory(t, pool, project, ownerUID, "project", "visible to project members only")

	uc := &UserContext{
		UserID:       "u_outsider_" + sanitizeStepTestName(t.Name()),
		DisplayName:  "outsider",
		Role:         "writer",
		ProjectRoles: map[string]string{}, // no role on `project`
	}
	c, rec := newGetMemoryRequest(t, memID, uc)
	require.NoError(t, handleGetMemory(pool)(c))
	require.Equal(t, http.StatusNotFound, rec.Code,
		"a caller with no access to the memory's project must 404, not 403: %s", rec.Body.String())
}

// TestHandleGetMemory_AdminSeesPrivate verifies the mirrored predicate's admin
// bypass matches Recall's: an admin caller can see a private memory authored
// by someone else.
func TestHandleGetMemory_AdminSeesPrivate(t *testing.T) {
	pool := setupStepTestDB(t)
	ownerUID, project := seedStepTestUserAndProject(t, pool)
	memID := seedGetTestMemory(t, pool, project, ownerUID, "private", "admin-visible secret")

	uc := &UserContext{
		UserID:      "u_admin_" + sanitizeStepTestName(t.Name()),
		DisplayName: "admin",
		Role:        "admin",
	}
	c, rec := newGetMemoryRequest(t, memID, uc)
	require.NoError(t, handleGetMemory(pool)(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}
