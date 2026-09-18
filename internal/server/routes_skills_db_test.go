package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
)

func setupSkillRouteDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := setupStepTestDB(t)
	raw, err := os.ReadFile("../db/migrations/0043_skill_registry.sql")
	require.NoError(t, err)
	up, _, ok := strings.Cut(string(raw), "-- +goose Down")
	require.True(t, ok)
	_, err = pool.Exec(context.Background(), up)
	require.NoError(t, err)
	return pool
}

func TestSkillRoutesAuthVersionsAndExpectedLatest(t *testing.T) {
	pool := setupSkillRouteDB(t)
	ctx := context.Background()
	base := testname.Sanitize(t.Name())
	ownerID := "u_" + base + "_owner"
	otherID := "u_" + base + "_other"
	for _, id := range []string{ownerID, otherID} {
		_, err := pool.Exec(ctx, `INSERT INTO users(id,email,display_name) VALUES($1,$1||'@test.local',$1) ON CONFLICT (id) DO NOTHING`, id)
		require.NoError(t, err)
	}
	project := "p_" + base
	members, err := json.Marshal([]map[string]string{{"user_id": otherID, "role": "viewer"}})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO projects(name,owner_user_id,members) VALUES($1,$2,$3) ON CONFLICT (name) DO UPDATE SET owner_user_id=EXCLUDED.owner_user_id,members=EXCLUDED.members`, project, ownerID, members)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM skills WHERE owner_user_id IN ($1,$2)`, ownerID, otherID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM skills WHERE owner_user_id IN ($1,$2)`, ownerID, otherID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM projects WHERE name=$1`, project)
	})

	owner := &UserContext{UserID: ownerID, DisplayName: ownerID, Role: "writer"}
	other := &UserContext{UserID: otherID, DisplayName: otherID, Role: "writer"}
	ownerServer := skillRouteDBServer(pool, owner)
	otherServer := skillRouteDBServer(pool, other)

	created := skillRouteRequest(t, ownerServer, http.MethodPost, "/v1/skills", `{"name":"route-check"}`)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var detail struct {
		ID            string `json:"id"`
		LatestVersion int    `json:"latest_version"`
	}
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &detail))
	require.NotEmpty(t, detail.ID)
	require.Zero(t, detail.LatestVersion)

	publishBody := `{"expected_latest":0,"bundle":{"entry":"SKILL.md","files":[{"path":"SKILL.md","content":"route v1"}],"license":{"name":"MIT"}},"contract":{"capabilities":["authoring"],"runtime":{"interactive":false}}}`
	published := skillRouteRequest(t, ownerServer, http.MethodPost, "/v1/skills/"+detail.ID+"/versions", publishBody)
	require.Equal(t, http.StatusCreated, published.Code, published.Body.String())
	require.Contains(t, published.Body.String(), `"version":1`)
	require.Contains(t, published.Body.String(), `"visibility":"private"`)

	stale := skillRouteRequest(t, ownerServer, http.MethodPost, "/v1/skills/"+detail.ID+"/versions", publishBody)
	require.Equal(t, http.StatusConflict, stale.Code, stale.Body.String())
	require.Contains(t, stale.Body.String(), "CONFLICT_CAS_FAILED")

	privateRead := skillRouteRequest(t, otherServer, http.MethodGet, "/v1/skills/"+detail.ID+"/versions/1", "")
	require.Equal(t, http.StatusNotFound, privateRead.Code, privateRead.Body.String())

	shared := skillRouteRequest(t, ownerServer, http.MethodPost, "/v1/skills/"+detail.ID+"/versions/1/shares", `{"project":"`+project+`"}`)
	require.Equal(t, http.StatusNoContent, shared.Code, shared.Body.String())
	sharedRead := skillRouteRequest(t, otherServer, http.MethodGet, "/v1/skills/"+detail.ID+"/versions/1", "")
	require.Equal(t, http.StatusOK, sharedRead.Code, sharedRead.Body.String())

	revoked := skillRouteRequest(t, ownerServer, http.MethodDelete, "/v1/skills/"+detail.ID+"/versions/1/shares/"+project, "")
	require.Equal(t, http.StatusNoContent, revoked.Code, revoked.Body.String())
	revokedRead := skillRouteRequest(t, otherServer, http.MethodGet, "/v1/skills/"+detail.ID+"/versions/1", "")
	require.Equal(t, http.StatusNotFound, revokedRead.Code, revokedRead.Body.String())

	visible := skillRouteRequest(t, ownerServer, http.MethodPatch, "/v1/skills/"+detail.ID+"/versions/1/visibility", `{"visibility":"public"}`)
	require.Equal(t, http.StatusOK, visible.Code, visible.Body.String())

	publicRead := skillRouteRequest(t, otherServer, http.MethodGet, "/v1/skills/"+detail.ID+"/versions/1", "")
	require.Equal(t, http.StatusOK, publicRead.Code, publicRead.Body.String())
	require.Contains(t, publicRead.Body.String(), "route v1")

	listed := skillRouteRequest(t, otherServer, http.MethodGet, "/v1/skills?limit=1", "")
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	require.Contains(t, listed.Body.String(), detail.ID)
	require.Contains(t, listed.Body.String(), "latest_accessible")
	require.NotContains(t, listed.Body.String(), `"latest_version"`, "non-owner must not receive total version count")
}

func skillRouteDBServer(pool *pgxpool.Pool, user *UserContext) *echo.Echo {
	e := echo.New()
	v1 := e.Group("/v1", func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(string(ctxUser), user)
			return next(c)
		}
	})
	RegisterSkillRoutes(v1, pool)
	return e
}
