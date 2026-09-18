package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

func TestUISkillRoutesRequireSessionAndBoundPublishForm(t *testing.T) {
	tmpl := pageTemplate("skills_detail.html.tmpl")

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/ui/skills/skill_x", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/ui/skills/:id")
	c.SetParamNames("id")
	c.SetParamValues("skill_x")
	require.NoError(t, handleUISkillDetail(nil, tmpl, nil)(c))
	require.Equal(t, http.StatusFound, rec.Code)
	require.Contains(t, rec.Header().Get("Location"), "/ui/login")

	form := strings.NewReader("expected_latest=not-an-integer&bundle=%7B%7D&contract=%7B%7D")
	req = httptest.NewRequest(http.MethodPost, "/ui/skills/skill_x/versions", form)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec = httptest.NewRecorder()
	c = e.NewContext(req, rec)
	c.SetPath("/ui/skills/:id/versions")
	c.SetParamNames("id")
	c.SetParamValues("skill_x")
	setUser(c, &UserContext{UserID: "u_owner", Role: "writer"})
	require.NoError(t, handleUIPublishSkill(nil, nil)(c))
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Contains(t, rec.Header().Get("Location"), "CSRF")
}

func TestSkillUIFormsCSRF(t *testing.T) {
	sm := NewSessionManager([]byte("test-only-skill-secret"))
	cookie := &http.Cookie{Name: pfSessionCookieName, Value: sm.Sign("u_owner", "key", time.Hour)}
	cases := []struct {
		path    string
		handler echo.HandlerFunc
	}{
		{"/ui/skills/skill_x/versions", handleUIPublishSkill(nil, sm)},
		{"/ui/skills/skill_x/versions/1/shares", handleUIShareSkill(nil, sm, false)},
		{"/ui/skills/skill_x/versions/1/shares/revoke", handleUIShareSkill(nil, sm, true)},
		{"/ui/skills/skill_x/versions/1/visibility", handleUISkillVisibility(nil, sm)},
	}
	for _, tc := range cases {
		for _, mode := range []string{"missing", "invalid", "cross-origin", "opaque-cross-site", "same-origin", "opaque-same-origin", "valid"} {
			t.Run(tc.path+"/"+mode, func(t *testing.T) {
				e := echo.New()
				values := url.Values{"expected_latest": {"bad"}, "project": {"project_x"}, "visibility": {"public"}}
				if mode != "missing" {
					values.Set("csrf_token", "incorrect")
				}
				if mode == "valid" || mode == "cross-origin" || mode == "opaque-cross-site" || mode == "same-origin" || mode == "opaque-same-origin" {
					probe := e.NewContext(httptest.NewRequest(http.MethodGet, tc.path, nil), httptest.NewRecorder())
					probe.Request().AddCookie(cookie)
					values.Set("csrf_token", skillCSRFToken(probe, sm))
				}
				req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(values.Encode()))
				req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
				req.AddCookie(cookie)
				if mode == "cross-origin" {
					req.Header.Set("Origin", "http://attacker.example")
				}
				if mode == "same-origin" {
					req.Header.Set("Origin", "http://example.com")
				}
				if mode == "opaque-cross-site" || mode == "opaque-same-origin" {
					req.Header.Set("Origin", "null")
					site := "cross-site"
					if mode == "opaque-same-origin" {
						site = "same-origin"
					}
					req.Header.Set("Sec-Fetch-Site", site)
				}
				rec := httptest.NewRecorder()
				c := e.NewContext(req, rec)
				c.SetParamNames("id", "version")
				c.SetParamValues("skill_x", "1")
				setUser(c, &UserContext{UserID: "u_owner", Role: "writer"})
				require.NoError(t, tc.handler(c))
				require.Equal(t, http.StatusSeeOther, rec.Code)
				location := rec.Header().Get("Location")
				switch mode {
				case "missing", "invalid":
					require.Contains(t, location, "CSRF")
				case "cross-origin", "opaque-cross-site":
					require.Contains(t, location, "origin")
				case "valid", "same-origin", "opaque-same-origin":
					if tc.path == cases[0].path {
						require.Contains(t, location, "expected_latest")
					} else {
						require.NotContains(t, location, "CSRF")
						require.NotContains(t, location, "origin")
					}
				}
			})
		}
	}
}

func TestSkillUIFormTokensInAllWriteForms(t *testing.T) {
	data := skillDetailPageData{
		Title: "skill", Theme: "auto", CSRFToken: "sentinel-token", CanManage: true,
		Skill:    &domain.SkillDetail{SkillIdentity: domain.SkillIdentity{ID: "skill_x", Name: "demo"}},
		Versions: []domain.SkillVersionSummary{{SkillID: "skill_x", Version: 1, Visibility: "private"}},
	}
	e := echo.New()
	rec := httptest.NewRecorder()
	require.NoError(t, renderTemplate(e.NewContext(httptest.NewRequest(http.MethodGet, "/ui/skills/skill_x", nil), rec), pageTemplate("skills_detail.html.tmpl"), "layout", data))
	require.Equal(t, 4, strings.Count(rec.Body.String(), `name="csrf_token" value="sentinel-token"`))
}

func TestUISkillVersionTemplateEscapesBundleContent(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	version := &domain.SkillVersion{
		SkillVersionSummary: domain.SkillVersionSummary{
			SkillID: "skill_x", Version: 1, Visibility: "private", Digest: "sha256:test", AuthorUserID: "u_owner", CreatedAt: now,
		},
		Bundle: json.RawMessage(`{"entry":"SKILL.md"}`), Contract: json.RawMessage(`{"runtime":{"interactive":false}}`),
	}
	data := skillDetailPageData{
		Title: "demo", Active: "skills", Theme: "auto", User: &UserContext{UserID: "u_owner", Role: "writer"},
		Skill:    &domain.SkillDetail{SkillIdentity: domain.SkillIdentity{ID: "skill_x", Name: "demo", OwnerUserID: "u_owner", OwnerDisplay: "Owner", LatestVersion: 1}},
		Versions: []domain.SkillVersionSummary{version.SkillVersionSummary}, Version: version,
		Bundle: `<script>alert("private")</script>`, Contract: `{}`, CanManage: true,
	}
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/ui/skills/skill_x/versions/1", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	require.NoError(t, renderTemplate(c, pageTemplate("skills_detail.html.tmpl"), "layout", data))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), `<script>alert`)
	require.Contains(t, rec.Body.String(), `&lt;script&gt;alert`)
	require.Contains(t, rec.Body.String(), `/ui/skills/skill_x/versions/1/shares`)
}
