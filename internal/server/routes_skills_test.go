package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func skillRouteTestServer(user *UserContext) *echo.Echo {
	e := echo.New()
	v1 := e.Group("/v1", func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if user != nil {
				c.Set(string(ctxUser), user)
			}
			return next(c)
		}
	})
	RegisterSkillRoutes(v1, nil)
	return e
}

func skillRouteRequest(t *testing.T, e *echo.Echo, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestSkillRoutesRejectInvalidRequestsBeforeDatabase(t *testing.T) {
	writer := &UserContext{UserID: "u_writer", Role: "writer"}

	t.Run("authentication required", func(t *testing.T) {
		rec := skillRouteRequest(t, skillRouteTestServer(nil), http.MethodGet, "/v1/skills", "")
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	cases := []struct {
		name   string
		method string
		target string
		body   string
	}{
		{"unknown list parameter", http.MethodGet, "/v1/skills?enabled=true", ""},
		{"create null", http.MethodPost, "/v1/skills", `null`},
		{"create array", http.MethodPost, "/v1/skills", `[]`},
		{"create Go alias", http.MethodPost, "/v1/skills", `{"Name":"demo"}`},
		{"create duplicate", http.MethodPost, "/v1/skills", `{"name":"a","name":"b"}`},
		{"create trailing", http.MethodPost, "/v1/skills", `{"name":"a"} {}`},
		{"publish null", http.MethodPost, "/v1/skills/skill_x/versions", `null`},
		{"publish array", http.MethodPost, "/v1/skills/skill_x/versions", `[]`},
		{"publish alias", http.MethodPost, "/v1/skills/skill_x/versions", `{"ExpectedLatest":0,"bundle":{},"contract":{}}`},
		{"publish duplicate nested", http.MethodPost, "/v1/skills/skill_x/versions", `{"expected_latest":0,"bundle":{"files":[],"files":[]},"contract":{}}`},
		{"share null", http.MethodPost, "/v1/skills/skill_x/versions/1/shares", `null`},
		{"share alias", http.MethodPost, "/v1/skills/skill_x/versions/1/shares", `{"Project":"p"}`},
		{"share duplicate", http.MethodPost, "/v1/skills/skill_x/versions/1/shares", `{"project":"p","project":"q"}`},
		{"visibility null", http.MethodPatch, "/v1/skills/skill_x/versions/1/visibility", `null`},
		{"visibility alias", http.MethodPatch, "/v1/skills/skill_x/versions/1/visibility", `{"Visibility":"public"}`},
		{"visibility duplicate", http.MethodPatch, "/v1/skills/skill_x/versions/1/visibility", `{"visibility":"public","visibility":"private"}`},

		{"malformed limit", http.MethodGet, "/v1/skills?limit=twelve", ""},
		{"malformed cursor", http.MethodGet, "/v1/skills?cursor=not-a-page-token", ""},
		{"nonpositive version", http.MethodGet, "/v1/skills/skill_x/versions/0", ""},
		{"create unknown field", http.MethodPost, "/v1/skills", `{"name":"demo","enabled":true}`},
		{"publish path id cannot be supplied in body", http.MethodPost, "/v1/skills/skill_x/versions", `{"skill_id":"skill_y","expected_latest":0,"bundle":{},"contract":{}}`},
		{"publish requires expected latest", http.MethodPost, "/v1/skills/skill_x/versions", `{"bundle":{},"contract":{}}`},
		{"share path fields cannot be supplied in body", http.MethodPost, "/v1/skills/skill_x/versions/1/shares", `{"skill_id":"skill_y","version":2,"project":"p"}`},
		{"visibility is closed", http.MethodPatch, "/v1/skills/skill_x/versions/1/visibility", `{"visibility":"private","enabled":false}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := skillRouteRequest(t, skillRouteTestServer(writer), tc.method, tc.target, tc.body)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

func TestSkillRouteEnvelopeAllowsBundleAboveOneMiB(t *testing.T) {
	e := echo.New()
	bundle := strings.Repeat("x", (1<<20)+100)
	body := `{"expected_latest":0,"bundle":{"arbitrary_property":"` + bundle + `"},"contract":{}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/skills/skill_x/versions", strings.NewReader(body))
	c := e.NewContext(req, httptest.NewRecorder())
	var dst struct {
		ExpectedLatest int             `json:"expected_latest"`
		Bundle         json.RawMessage `json:"bundle"`
		Contract       json.RawMessage `json:"contract"`
	}
	require.Nil(t, skillJSONDecode(c, &dst))
	require.Contains(t, string(dst.Bundle), bundle)
}

func TestSkillRouteBodyLimit(t *testing.T) {
	writer := &UserContext{UserID: "u_writer", Role: "writer"}
	body := `{"name":"` + strings.Repeat("a", skillRequestBodyLimit) + `"}`
	rec := skillRouteRequest(t, skillRouteTestServer(writer), http.MethodPost, "/v1/skills", body)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "exceeds")
}
