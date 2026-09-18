package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSkillClientWrappers(t *testing.T) {
	type expectedRequest struct {
		method string
		uri    string
		body   string
		json   string
	}
	expected := []expectedRequest{
		{http.MethodPost, "/v1/skills", `{"name":"demo"}`, `{"id":"skill_1"}`},
		{http.MethodGet, "/v1/skills?limit=2&owner=u+one", "", `{"items":[]}`},
		{http.MethodGet, "/v1/skills/skill%2Fone", "", `{"id":"skill/one"}`},
		{http.MethodPost, "/v1/skills/skill%2Fone/versions", `{"expected_latest":0}`, `{"version":1}`},
		{http.MethodGet, "/v1/skills/skill%2Fone/versions", "", `{"items":[]}`},
		{http.MethodGet, "/v1/skills/skill%2Fone/versions/1", "", `{"version":1}`},
		{http.MethodPost, "/v1/skills/skill%2Fone/versions/1/shares", `{"project":"project/a"}`, ""},
		{http.MethodDelete, "/v1/skills/skill%2Fone/versions/1/shares/project%2Fa", "", ""},
		{http.MethodPatch, "/v1/skills/skill%2Fone/versions/1/visibility", `{"visibility":"public"}`, `{"visibility":"public"}`},
	}
	index := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Less(t, index, len(expected))
		want := expected[index]
		index++
		require.Equal(t, want.method, r.Method)
		require.Equal(t, want.uri, r.RequestURI)
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		if r.Method == http.MethodPost || r.Method == http.MethodPatch {
			require.NotEmpty(t, r.Header.Get("Idempotency-Key"))
		}
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		if want.body != "" {
			var gotJSON, wantJSON any
			require.NoError(t, json.Unmarshal(body, &gotJSON))
			require.NoError(t, json.Unmarshal([]byte(want.body), &wantJSON))
			require.Equal(t, wantJSON, gotJSON)
		} else {
			require.Empty(t, body)
		}
		if want.json == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(want.json))
	}))
	defer server.Close()

	ctx := context.Background()
	c := New(server.URL, "test-key")
	_, err := c.CreateSkill(ctx, map[string]any{"name": "demo"})
	require.NoError(t, err)
	_, err = c.ListSkills(ctx, url.Values{"owner": {"u one"}, "limit": {"2"}})
	require.NoError(t, err)
	_, err = c.GetSkill(ctx, "skill/one")
	require.NoError(t, err)
	_, err = c.PublishSkillVersion(ctx, "skill/one", map[string]any{"expected_latest": 0})
	require.NoError(t, err)
	_, err = c.ListSkillVersions(ctx, "skill/one")
	require.NoError(t, err)
	_, err = c.GetSkillVersion(ctx, "skill/one", 1)
	require.NoError(t, err)
	require.NoError(t, c.ShareSkillVersion(ctx, "skill/one", 1, "project/a"))
	require.NoError(t, c.RevokeSkillVersionShare(ctx, "skill/one", 1, "project/a"))
	_, err = c.SetSkillVersionVisibility(ctx, "skill/one", 1, "public")
	require.NoError(t, err)
	require.Equal(t, len(expected), index)
}
