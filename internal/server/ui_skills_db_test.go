package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

// Exercises the actual skill write handlers against an isolated migrated PostgreSQL
// database (AIHUB_TEST_DB), rather than only asserting early validation errors.
func TestUISkillWriteRoutesDB(t *testing.T) {
	pool := setupSkillRouteDB(t)
	ownerID := "u_" + testname.Sanitize(t.Name())
	_, err := pool.Exec(context.Background(), `INSERT INTO users(id,email,display_name) VALUES($1,$1||'@test.local',$1) ON CONFLICT (id) DO NOTHING`, ownerID)
	require.NoError(t, err)
	project := "p_" + testname.Sanitize(t.Name())
	_, err = pool.Exec(context.Background(), `INSERT INTO projects(name,owner_user_id) VALUES($1,$2) ON CONFLICT (name) DO NOTHING`, project, ownerID)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM projects WHERE name=$1`, project) })
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM skills WHERE owner_user_id=$1`, ownerID) })
	owner := &UserContext{UserID: ownerID, DisplayName: ownerID, Role: "writer"}
	created := skillRouteRequest(t, skillRouteDBServer(pool, owner), http.MethodPost, "/v1/skills", `{"name":"ui-route-`+strings.ReplaceAll(testname.Sanitize(t.Name()), "_", "-")+`"}`)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var skill struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &skill))
	sm := NewSessionManager([]byte("ui-route-db-secret"))
	cookie := &http.Cookie{Name: pfSessionCookieName, Value: sm.Sign(ownerID, "key", time.Hour)}
	tokenContext := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/ui/skills", nil), httptest.NewRecorder())
	tokenContext.Request().AddCookie(cookie)
	token := skillCSRFToken(tokenContext, sm)
	invoke := func(path string, handler echo.HandlerFunc, fields url.Values, mode string) *httptest.ResponseRecorder {
		t.Helper()
		if mode != "missing" {
			fields.Set("csrf_token", token)
		}
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(fields.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		if mode == "cross-origin" {
			req.Header.Set("Origin", "http://attacker.example")
		}
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(req, rec)
		c.SetParamNames("id", "version")
		c.SetParamValues(skill.ID, "1")
		setUser(c, owner)
		require.NoError(t, handler(c))
		return rec
	}
	publishPath := "/ui/skills/" + skill.ID + "/versions"
	body := url.Values{"expected_latest": {"0"}, "bundle": {`{"entry":"SKILL.md","files":[{"path":"SKILL.md","content":"ui test"}],"license":{"name":"MIT"}}`}, "contract": {`{"capabilities":["authoring"],"runtime":{"interactive":false}}`}}
	for _, mode := range []string{"missing", "cross-origin"} {
		rec := invoke(publishPath, handleUIPublishSkill(pool, sm), body, mode)
		require.Contains(t, rec.Header().Get("Location"), "error=")
	}
	var count int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM skill_versions WHERE skill_id=$1`, skill.ID).Scan(&count))
	require.Zero(t, count)
	rec := invoke(publishPath, handleUIPublishSkill(pool, sm), body, "valid")
	require.Contains(t, rec.Header().Get("Location"), "Published")
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM skill_versions WHERE skill_id=$1`, skill.ID).Scan(&count))
	require.Equal(t, 1, count)
	for _, tc := range []struct {
		suffix  string
		handler echo.HandlerFunc
		fields  url.Values
	}{
		{"/shares", handleUIShareSkill(pool, sm, false), url.Values{"project": {project}}},
		{"/shares/revoke", handleUIShareSkill(pool, sm, true), url.Values{"project": {project}}},
		{"/visibility", handleUISkillVisibility(pool, sm), url.Values{"visibility": {"public"}}},
	} {
		path := publishPath + "/1" + tc.suffix
		bad := invoke(path, tc.handler, tc.fields, "cross-origin")
		require.Contains(t, bad.Header().Get("Location"), "error=")
		valid := invoke(path, tc.handler, tc.fields, "valid")
		require.NotContains(t, valid.Header().Get("Location"), "error=")
	}

	if os.Getenv("AIHUB_SKILLS_BROWSER_TEST") == "1" {
		t.Run("chromium list detail csrf publish share revoke", func(t *testing.T) {
			runUISkillsBrowser(t, pool, ownerID, project)
		})
	}
}
