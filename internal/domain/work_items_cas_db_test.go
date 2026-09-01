package domain

// DB-gated integration test for aihub#241: UpdateWorkItem's declared_resources
// compare-and-set (CAS).
//
// Two independent defects lived in the code this exercises (see
// buildWorkItemUpdate's doc comment in work_items.go):
//
//   - resources_version never advanced on the ordinary path (no version
//     supplied): it was written only as `<caller value> + 1`, so every real
//     caller kept reading 0.
//   - passing resources_version changed what got stored but added no WHERE
//     predicate at all, so a stale writer silently overwrote a fresher one
//     instead of getting a conflict.
//
// buildWorkItemUpdate itself is a pure function with ordinary (non-DB-gated)
// unit test coverage elsewhere. What can only be verified against a real
// Postgres is that UpdateWorkItem actually executes the compiled statement
// and reports RowsAffected()==0 as a 409 CONFLICT_CAS_FAILED — never a 400 —
// while a correct version still advances the counter. That is what this file
// covers.
//
// Follows the AIHUB_TEST_DB gating pattern from memory_latest_test.go /
// dependencies_requeue_test.go: setupLatestTestDB SKIPs unless AIHUB_TEST_DB
// is set. That variable is NOT set locally in this sandbox (no local
// Postgres) and is deliberately NOT set on the main `go test ./...` / "Unit
// tests" step in CI either — turning it on there would also switch on every
// other AIHUB_TEST_DB-gated test in this package (memory ranking,
// cross-project resume, conflict prediction, the aihub#242 dependency-requeue
// tests, ...) against a database whose results this change did not verify.
// Instead, .github/workflows/ci.yml runs a dedicated "aihub#241
// declared_resources CAS DB tests" step that applies migrations and runs only
// `-run 'TestUpdateWorkItemCAS'` with AIHUB_TEST_DB pointed at the job's
// pgvector/pgvector:pg18 service, so this specific regression is exercised in
// CI without widening what else runs there.
//
//	AIHUB_TEST_DB=postgres://postgres:test@localhost:5440/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestUpdateWorkItemCAS' -v -count=1

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// declaredResourcesFixture is a single legal declared_resources entry (the
// shape ValidateDeclaredResources requires): a "path" resource with intent
// "write", pointing at a file: URI.
var declaredResourcesFixture = json.RawMessage(`[{"type":"path","uri":"file:internal/a.go","intent":"write"}]`)

// TestUpdateWorkItemCASVersionAdvancesAcrossWrites is the core aihub#241
// regression: two consecutive UpdateWorkItem calls that each write
// declared_resources and pass no resources_version (the ordinary path — the
// exact scenario that used to leave the counter pinned at 0 forever) must
// advance resources_version 0 -> 1 -> 2.
func TestUpdateWorkItemCASVersionAdvancesAcrossWrites(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	wi := seedWIs(t, pool, project, u, 1)[0]
	require.Equal(t, 0, wi.ResourcesVersion, "precondition: freshly created wi starts at resources_version=0")

	updated1, aerr := UpdateWorkItem(context.Background(), pool, wi.ID, u, "admin", nil, &UpdateWorkItemRequest{
		DeclaredResources: declaredResourcesFixture,
	})
	require.Nil(t, aerr)
	assert.Equal(t, 1, updated1.ResourcesVersion, "first write of declared_resources must advance version 0 -> 1")

	updated2, aerr := UpdateWorkItem(context.Background(), pool, wi.ID, u, "admin", nil, &UpdateWorkItemRequest{
		DeclaredResources: declaredResourcesFixture,
	})
	require.Nil(t, aerr)
	assert.Equal(t, 2, updated2.ResourcesVersion, "second write of declared_resources must advance version 1 -> 2")
}

// TestUpdateWorkItemCASStaleVersionReturns409NotBadRequest is the other half
// of aihub#241: a caller-supplied resources_version that no longer matches
// the row must fail as a 409 CONFLICT_CAS_FAILED conflict, never a 400 (the
// MCP-layer schema-type bug that made ANY resources_version 400 via c.Bind,
// and the missing-WHERE-predicate bug that made a stale write silently
// succeed, are both aihub#241 fixes this guards).
func TestUpdateWorkItemCASStaleVersionReturns409NotBadRequest(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	wi := seedWIs(t, pool, project, u, 1)[0]

	// Advance the real version to 1 so that 0 is now stale.
	_, aerr := UpdateWorkItem(context.Background(), pool, wi.ID, u, "admin", nil, &UpdateWorkItemRequest{
		DeclaredResources: declaredResourcesFixture,
	})
	require.Nil(t, aerr)

	staleVersion := 0
	_, aerr = UpdateWorkItem(context.Background(), pool, wi.ID, u, "admin", nil, &UpdateWorkItemRequest{
		DeclaredResources: declaredResourcesFixture,
		ResourcesVersion:  &staleVersion,
	})
	require.NotNil(t, aerr, "a stale resources_version must be rejected")
	assert.Equal(t, 409, aerr.HTTPStatus, "a stale resources_version must be reported as a conflict, not a 400 bad request")
	assert.Equal(t, ErrConflictCASFailed, aerr.Code)

	// The whole statement is one UPDATE: the aborted write must not have
	// touched declared_resources either.
	fresh, gerr := GetWorkItem(context.Background(), pool, wi.ID)
	require.Nil(t, gerr)
	assert.Equal(t, 1, fresh.ResourcesVersion, "a rejected CAS write must not advance resources_version")
}

// TestUpdateWorkItemCASCorrectVersionSucceedsAndAdvances is the mutation
// guard for the 409 test above: it proves the CAS predicate actually matches
// when given the CURRENT version, rather than the WHERE clause always
// failing to match (which would also make the stale-version test pass, for
// the wrong reason).
func TestUpdateWorkItemCASCorrectVersionSucceedsAndAdvances(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	wi := seedWIs(t, pool, project, u, 1)[0]

	updated1, aerr := UpdateWorkItem(context.Background(), pool, wi.ID, u, "admin", nil, &UpdateWorkItemRequest{
		DeclaredResources: declaredResourcesFixture,
	})
	require.Nil(t, aerr)
	require.Equal(t, 1, updated1.ResourcesVersion)

	currentVersion := 1
	updated2, aerr := UpdateWorkItem(context.Background(), pool, wi.ID, u, "admin", nil, &UpdateWorkItemRequest{
		DeclaredResources: declaredResourcesFixture,
		ResourcesVersion:  &currentVersion,
	})
	require.Nil(t, aerr, "a CAS write carrying the CURRENT resources_version must succeed")
	assert.Equal(t, 2, updated2.ResourcesVersion, "a successful CAS write must still advance the counter")
}
