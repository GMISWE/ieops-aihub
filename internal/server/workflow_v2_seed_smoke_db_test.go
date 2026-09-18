package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/auth"
	"github.com/GMISWE/ieops-aihub/internal/skillregistry"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// TestWorkflowV2SeedSmokeThroughAuthenticatedAPI is the deterministic migration
// smoke fixture: migration 0043 is applied by setupSkillRouteDB, then the same
// authenticated HTTP/client route the CLI uses publishes the canonical private
// seed set. A second pass must be all-present and must create no rows.
func TestWorkflowV2SeedSmokeThroughAuthenticatedAPI(t *testing.T) {
	pool := setupSkillRouteDB(t)
	ctx := context.Background()
	ownerID := "u_" + testnameSafe(t.Name()) + "_seed_owner"
	const apiKey = "authenticated-test-key"
	keys, err := json.Marshal([]map[string]any{{"id": "k_seed_smoke", "key_hash": auth.HashKey(apiKey)}})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO users(id,email,display_name,user_type,role,api_keys)
		VALUES($1,$1||'@test.local',$1,'human','writer',$2)
		ON CONFLICT (id) DO UPDATE SET api_keys=EXCLUDED.api_keys, role=EXCLUDED.role`, ownerID, keys)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM skills WHERE owner_user_id=$1`, ownerID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM skills WHERE owner_user_id=$1`, ownerID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, ownerID)
	})

	e := NewRouter(pool, []byte("workflow-v2-seed-smoke-cookie-secret"))
	ts := httptest.NewServer(e)
	defer ts.Close()
	store := client.New(ts.URL, apiKey)

	first, err := skillregistry.ApplySeed(ctx, store)
	require.NoError(t, err)
	require.Len(t, first, 8)
	for _, result := range first {
		require.Equal(t, skillregistry.SeedOpCreate, result.Op)
		require.Equal(t, 1, result.Version)
		require.NotEmpty(t, result.SkillID)
	}

	var identities, versions, nonPrivate int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM skills WHERE owner_user_id=$1`, ownerID).Scan(&identities))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM skill_versions sv JOIN skills s ON s.id=sv.skill_id WHERE s.owner_user_id=$1`, ownerID).Scan(&versions))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM skill_versions sv JOIN skills s ON s.id=sv.skill_id WHERE s.owner_user_id=$1 AND sv.visibility<>'private'`, ownerID).Scan(&nonPrivate))
	require.Equal(t, 8, identities)
	require.Equal(t, 8, versions)
	require.Zero(t, nonPrivate)

	second, err := skillregistry.ApplySeed(ctx, store)
	require.NoError(t, err)
	require.Len(t, second, 8)
	for i, result := range second {
		require.Equal(t, skillregistry.SeedOpPresent, result.Op)
		require.Equal(t, first[i].SkillID, result.SkillID)
		require.Equal(t, first[i].Version, result.Version)
		require.Equal(t, first[i].Digest, result.Digest)
	}
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM skill_versions sv JOIN skills s ON s.id=sv.skill_id WHERE s.owner_user_id=$1`, ownerID).Scan(&versions))
	require.Equal(t, 8, versions, "an unchanged seed rerun must not publish duplicate versions")
}

func testnameSafe(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}
