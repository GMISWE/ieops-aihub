package server

// aihub#379: the per-memory visibility rule has exactly two executable forms —
// memoryVisibleTo (Go, this package) and domain's memoryVisibilityScopeSQL
// (SQL, shared by Recall's text and vector paths). This DB-gated test drives
// both against the same seeded rows and fails if they ever disagree: for every
// (caller, row) pair, presence in Recall's result set, memoryVisibleTo's
// verdict, and handleGetMemory's HTTP answer must be the same boolean.
//
// This is the behavioural half of the anti-drift pair; the structural half is
// TestMemoryVisibilitySQLPredicateHasOneCopy (internal/domain), which fails if
// a SQL reader re-inlines the clause instead of calling the shared builder.
//
// Run against a live DB (gated by AIHUB_TEST_DB), same harness as
// routes_memory_get_test.go.

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
	"github.com/GMISWE/ieops-aihub/internal/domain"
)

func TestMemoryVisibilityParity_SQLAgreesWithGoPredicate(t *testing.T) {
	pool := setupStepTestDB(t)
	ownerUID, project := seedStepTestUserAndProject(t, pool)
	ctx := context.Background()

	// The project name is deterministic per test name, so clear rows left by a
	// prior run — otherwise the recall set accumulates across runs.
	_, err := pool.Exec(ctx, `DELETE FROM memories WHERE project=$1`, project)
	require.NoError(t, err)

	// One row per visibility tier, all authored by ownerUID.
	visibilities := []string{"project", "team", "private", "admin", "public"}
	memIDs := make(map[string]string, len(visibilities))
	for _, vis := range visibilities {
		memIDs[vis] = seedGetTestMemory(t, pool, project, ownerUID, vis, "parity row "+vis)
	}

	callers := []*UserContext{
		{UserID: ownerUID, DisplayName: "author", Role: "writer",
			ProjectRoles: map[string]string{project: "viewer"}},
		{UserID: "u_other_" + testname.Sanitize(t.Name()), DisplayName: "other", Role: "writer",
			ProjectRoles: map[string]string{project: "viewer"}},
		{UserID: "u_admin_" + testname.Sanitize(t.Name()), DisplayName: "admin", Role: "admin"},
	}

	for _, uc := range callers {
		resp, aerr := domain.Recall(ctx, pool, &domain.RecallRequest{
			Project:      project,
			TopK:         200,
			CallerUserID: uc.UserID,
			CallerRole:   uc.Role,
		})
		require.Nil(t, aerr, "Recall failed for caller %s", uc.UserID)
		inRecall := make(map[string]bool, len(resp.Items))
		for _, it := range resp.Items {
			inRecall[it.ID] = true
		}

		for _, vis := range visibilities {
			memID := memIDs[vis]
			row := &domain.Memory{ID: memID, Project: project, Visibility: vis, AuthorUserID: ownerUID}
			want := memoryVisibleTo(uc, row)

			if inRecall[memID] != want {
				t.Errorf("caller %s (role %s), visibility %s: Recall's SQL predicate says %v but memoryVisibleTo says %v — the two copies of the rule have drifted",
					uc.UserID, uc.Role, vis, inRecall[memID], want)
			}

			c, rec := newGetMemoryRequest(t, memID, uc)
			_ = handleGetMemory(pool)(c) // a denial commits its response and returns the gate's error
			gotHTTP := rec.Code == http.StatusOK
			if gotHTTP != want {
				t.Errorf("caller %s (role %s), visibility %s: handleGetMemory answered %d but memoryVisibleTo says visible=%v",
					uc.UserID, uc.Role, vis, rec.Code, want)
			}
			if !want {
				require.Equal(t, http.StatusNotFound, rec.Code,
					"caller %s, visibility %s: an invisible row must 404 (aihub#379), body %s",
					uc.UserID, vis, rec.Body.String())
				require.Contains(t, rec.Body.String(), notVisibleMessage,
					"caller %s, visibility %s: the denial must carry the shared notVisibleMessage",
					uc.UserID, vis)
			}
		}
	}
}
