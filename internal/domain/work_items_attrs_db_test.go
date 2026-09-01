package domain

// DB-gated behavioural tests for aihub#288: work_items.attrs merge semantics.
//
// # What actually happened
//
// On 2026-08-30, two agents were annotating aihub#284 at the same time. The
// second one sent pf_update_work_item with the two keys it knew about. The
// three keys the first one had written —
// root_cause_topk_clamped_to_10, dahe_report_2026_08_06 and
// related_finding_embedding_truncated_at_6000 — were gone afterwards. No error,
// no conflict, no audit trail: `attrs = $n` is a whole-column assignment, so
// every key absent from the payload is simply dropped.
//
// TestUpdateWorkItemAttrsPatch_DoesNotDestroyOtherKeys replays exactly that.
//
// # Why every request in this file is built from JSON
//
// These tests have to COMPILE against the pre-fix build, otherwise "it fails
// before the fix" degrades into a build error, which proves nothing about
// behaviour and would still be reported by a struct-literal test that named a
// field the old code does not have. Going through json.Unmarshal — the same
// decode echo's c.Bind performs on PATCH /v1/work_items/:id — keeps them
// compilable on both builds, because encoding/json silently drops keys with no
// matching field. That silent drop IS the pre-fix behaviour: on the old build
// attrs_patch vanishes at the decoder and the update becomes a no-op, so the
// assertions below go red with a real, readable diff.
//
// Gating follows the AIHUB_TEST_DB pattern of work_items_cas_db_test.go:
// setupLatestTestDB SKIPs unless AIHUB_TEST_DB is set, and AIHUB_TEST_DB is
// deliberately not set on the main `go test ./...` / "Unit tests" step (turning
// it on there would also switch on every other gated suite in this package
// against a database this change did not verify). .github/workflows/ci.yml runs
// a dedicated "aihub#288 attrs merge DB tests" step instead, which asserts the
// expected `--- PASS` lines are present and that nothing SKIPped — a green step
// full of SKIPs is zero coverage (mem_p3shJXgC).
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5488/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestUpdateWorkItemAttrs' -v -count=1

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// updateReqFromJSON decodes a PATCH body the way echo's c.Bind does. See the
// file comment for why this is JSON and not a struct literal.
func updateReqFromJSON(t *testing.T, body string) *UpdateWorkItemRequest {
	t.Helper()
	var req UpdateWorkItemRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req), "test fixture is not valid JSON")
	return &req
}

// attrKeysOf returns the stored attrs as a plain map so assertions can name
// individual keys instead of comparing raw JSON byte strings (whose key order
// is not stable).
func attrKeysOf(t *testing.T, wi *WorkItem) map[string]any {
	t.Helper()
	out := map[string]any{}
	require.NoError(t, json.Unmarshal(wi.Attrs, &out), "stored attrs is not a JSON object: %s", wi.Attrs)
	return out
}

// seedWIWithAttrs creates one work item and gives it the supplied attrs via the
// pre-existing REPLACE path, so the starting state is built by code this change
// does not touch.
func seedWIWithAttrs(t *testing.T, pool *pgxpool.Pool, project, user, attrs string) *WorkItem {
	t.Helper()
	wi := seedWIs(t, pool, project, user, 1)[0]
	seeded, aerr := UpdateWorkItem(context.Background(), pool, wi.ID, user, "admin", nil,
		updateReqFromJSON(t, `{"attrs":`+attrs+`}`))
	require.Nil(t, aerr)
	return seeded
}

// TestUpdateWorkItemAttrsPatch_DoesNotDestroyOtherKeys is the aihub#288
// reproduction: three keys already on the work item, an update that knows about
// only two, and all five must survive.
//
// Before the fix this fails with the three original keys missing — the exact
// silent loss observed on aihub#284.
func TestUpdateWorkItemAttrsPatch_DoesNotDestroyOtherKeys(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	// Agent A's three keys, named after the ones actually destroyed.
	seeded := seedWIWithAttrs(t, pool, project, u, `{
		"root_cause_topk_clamped_to_10": "topk is clamped to 10 upstream",
		"dahe_report_2026_08_06": "reported to dahe",
		"related_finding_embedding_truncated_at_6000": "embedding input truncated at 6000 chars"
	}`)
	require.Len(t, attrKeysOf(t, seeded), 3, "precondition: three keys are stored before the second writer runs")

	// Agent B knows about two keys and nothing else.
	updated, aerr := UpdateWorkItem(context.Background(), pool, seeded.ID, u, "admin", nil,
		updateReqFromJSON(t, `{"attrs_patch":{
			"second_agent_finding": "attrs is whole-column REPLACE",
			"second_agent_verified_at": "2026-08-30"
		}}`))
	require.Nil(t, aerr)

	got := attrKeysOf(t, updated)
	for _, k := range []string{
		"root_cause_topk_clamped_to_10",
		"dahe_report_2026_08_06",
		"related_finding_embedding_truncated_at_6000",
	} {
		assert.Contains(t, got, k, "a two-key patch destroyed pre-existing key %q — this is the aihub#284 data loss", k)
	}
	assert.Contains(t, got, "second_agent_finding")
	assert.Contains(t, got, "second_agent_verified_at")
	assert.Len(t, got, 5, "expected the 3 pre-existing keys plus the 2 patched keys, got %v", got)

	// The patch must have been applied, not merely tolerated.
	assert.Equal(t, "attrs is whole-column REPLACE", got["second_agent_finding"])
	// ...and it must not have overwritten an untouched key's value.
	assert.Equal(t, "reported to dahe", got["dahe_report_2026_08_06"])
}

// TestUpdateWorkItemAttrs_ReplaceStillDestroysUnsentKeys is the incident itself,
// spelled out: the same three keys, the same two-key update, the same silent
// loss — and it must STILL behave that way after the fix.
//
// That is not a contradiction, and the distinction matters enough to state
// plainly. This test passes on both builds BY DESIGN. `attrs` is a whole-column
// REPLACE and stays one, because it is the only way to delete a key and because
// changing what an existing field means, without the caller changing anything,
// is a worse defect than the one being fixed. What aihub#288 adds is an
// alternative (attrs_patch) that the caller in the incident could have reached
// for; what it does not do is silently reinterpret the call they actually made.
//
// So the pair reads: this test pins the destructive behaviour that is retained,
// and TestUpdateWorkItemAttrsPatch_DoesNotDestroyOtherKeys — which fails on the
// pre-fix build — pins the non-destructive path that did not exist.
func TestUpdateWorkItemAttrs_ReplaceStillDestroysUnsentKeys(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	seeded := seedWIWithAttrs(t, pool, project, u, `{
		"root_cause_topk_clamped_to_10": "topk is clamped to 10 upstream",
		"dahe_report_2026_08_06": "reported to dahe",
		"related_finding_embedding_truncated_at_6000": "embedding input truncated at 6000 chars"
	}`)
	require.Len(t, attrKeysOf(t, seeded), 3, "precondition: three keys before the second writer runs")

	updated, aerr := UpdateWorkItem(context.Background(), pool, seeded.ID, u, "admin", nil,
		updateReqFromJSON(t, `{"attrs":{
			"second_agent_finding": "attrs is whole-column REPLACE",
			"second_agent_verified_at": "2026-08-30"
		}}`))
	require.Nil(t, aerr, "the destructive call succeeds without any error — that is what makes the loss silent")

	got := attrKeysOf(t, updated)
	assert.Equal(t, map[string]any{
		"second_agent_finding":     "attrs is whole-column REPLACE",
		"second_agent_verified_at": "2026-08-30",
	}, got, "attrs must still REPLACE the whole object — adding attrs_patch must not have turned it into a merge")
	for _, k := range []string{
		"root_cause_topk_clamped_to_10",
		"dahe_report_2026_08_06",
		"related_finding_embedding_truncated_at_6000",
	} {
		assert.NotContains(t, got, k, "attrs REPLACE is retained deliberately: %q is expected to be gone here", k)
	}
}

// TestUpdateWorkItemAttrsUnset_DeletesNamedKeys covers the deletion semantics.
// Without it the merge path would be a capability regression: "resend the whole
// object" is today the only way to remove a key, and a shallow merge cannot
// express removal on its own (null in a patch STORES a null — asserted here).
func TestUpdateWorkItemAttrsUnset_DeletesNamedKeys(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	seeded := seedWIWithAttrs(t, pool, project, u, `{"keep":"1","remove":"2","remove_too":"3"}`)

	updated, aerr := UpdateWorkItem(context.Background(), pool, seeded.ID, u, "admin", nil,
		updateReqFromJSON(t, `{"attrs_unset":["remove","remove_too"]}`))
	require.Nil(t, aerr)
	assert.Equal(t, map[string]any{"keep": "1"}, attrKeysOf(t, updated))

	t.Run("null in a patch stores a null, it does not delete", func(t *testing.T) {
		withNull, aerr := UpdateWorkItem(context.Background(), pool, seeded.ID, u, "admin", nil,
			updateReqFromJSON(t, `{"attrs_patch":{"keep":null}}`))
		require.Nil(t, aerr)
		got := attrKeysOf(t, withNull)
		require.Contains(t, got, "keep", "null must not be treated as a delete — attrs_unset is the delete")
		assert.Nil(t, got["keep"])
	})

	t.Run("patch and unset in one call: unset is applied last", func(t *testing.T) {
		both, aerr := UpdateWorkItem(context.Background(), pool, seeded.ID, u, "admin", nil,
			updateReqFromJSON(t, `{"attrs_patch":{"added":"yes","doomed":"x"},"attrs_unset":["doomed","keep"]}`))
		require.Nil(t, aerr)
		assert.Equal(t, map[string]any{"added": "yes"}, attrKeysOf(t, both),
			"merge runs first and unset second, so a key named in both must end up deleted")
	})
}

// TestUpdateWorkItemAttrsPatch_ConcurrentDisjointKeysAllSurvive is the
// concurrency criterion, and the one that says something a sequential test
// cannot: eight real goroutines each adding a distinct key, all eight keys
// present at the end.
//
// It discriminates. A client-side read-modify-write (get, merge locally, send
// the whole object) passes when run sequentially and loses keys here, because
// every writer's read predates the others' writes. `attrs = attrs || $1::jsonb`
// is a single statement, so Postgres' row lock serialises the writers and each
// one merges into the value the previous one committed.
func TestUpdateWorkItemAttrsPatch_ConcurrentDisjointKeysAllSurvive(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	const writers = 8
	seeded := seedWIWithAttrs(t, pool, project, u, `{"seed":"present"}`)

	// Decoded up front, on the test goroutine: updateReqFromJSON calls
	// require.NoError, and require's t.FailNow is only valid from the goroutine
	// running the test.
	reqs := make([]*UpdateWorkItemRequest, writers)
	for i := range reqs {
		reqs[i] = updateReqFromJSON(t, fmt.Sprintf(`{"attrs_patch":{"writer_%d":%d}}`, i, i))
	}

	var wg sync.WaitGroup
	errs := make([]*AihubError, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, aerr := UpdateWorkItem(context.Background(), pool, seeded.ID, u, "admin", nil, reqs[i])
			errs[i] = aerr
		}(i)
	}
	wg.Wait()
	for i, aerr := range errs {
		require.Nil(t, aerr, "concurrent writer %d failed", i)
	}

	fresh, gerr := GetWorkItem(context.Background(), pool, seeded.ID)
	require.Nil(t, gerr)
	got := attrKeysOf(t, fresh)
	for i := 0; i < writers; i++ {
		assert.Contains(t, got, fmt.Sprintf("writer_%d", i),
			"concurrent patch lost a key — this is the silent lost-update aihub#288 is about")
	}
	assert.Contains(t, got, "seed", "the pre-existing key must survive all of them")
	assert.Len(t, got, writers+1)
}

// TestUpdateWorkItemAttrsPatch_WorksOnTerminalWorkItem guards a constraint that
// is easy to break here by accident: attrs is the ONLY column writable on a
// wrapped/failed work item (content has an explicit terminal-state gate, attrs
// deliberately does not). The merge path must inherit that, not quietly add a
// gate of its own.
func TestUpdateWorkItemAttrsPatch_WorksOnTerminalWorkItem(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	seeded := seedWIWithAttrs(t, pool, project, u, `{"pre_wrap":"kept"}`)
	mustExec(t, pool, `UPDATE work_items SET status='wrapped' WHERE id='`+seeded.ID+`'`)

	updated, aerr := UpdateWorkItem(context.Background(), pool, seeded.ID, u, "admin", nil,
		updateReqFromJSON(t, `{"attrs_patch":{"post_wrap":"added"}}`))
	require.Nil(t, aerr, "attrs must stay writable on a terminal work item")
	assert.Equal(t, "wrapped", updated.Status)
	assert.Equal(t, map[string]any{"pre_wrap": "kept", "post_wrap": "added"}, attrKeysOf(t, updated))
}

// TestUpdateWorkItemAttrsPatch_SurvivesANonObjectStoredValue covers the hole
// code review found in the first cut of this change.
//
// The column is `JSONB NOT NULL DEFAULT '{}'` and every reader unmarshals it
// into a map, but nothing enforces that it holds an object: `attrs` REPLACE
// takes any JSON, and JSON null is not SQL NULL so it slips past NOT NULL. The
// first half of this test proves that reachability rather than assuming it.
//
// Merging onto such a row is not a harmless no-op. Without the jsonb_typeof
// guard, `'null'::jsonb || '{"a":1}'::jsonb` is `[null, {"a":1}]` — silently an
// ARRAY, in a column everything downstream decodes as an object — and
// `'null'::jsonb - ARRAY['a']::text[]` raises "cannot delete from scalar", i.e.
// a 500 on a well-formed request.
func TestUpdateWorkItemAttrsPatch_SurvivesANonObjectStoredValue(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	for _, stored := range []string{`null`, `[1,2]`, `"scalar"`} {
		t.Run("stored="+stored, func(t *testing.T) {
			// Reachability, proved not assumed: the REPLACE path stores this today.
			seeded := seedWIWithAttrs(t, pool, project, u, stored)
			var typed any
			require.NoError(t, json.Unmarshal(seeded.Attrs, &typed))
			if _, isObject := typed.(map[string]any); isObject {
				t.Skipf("attrs REPLACE no longer stores %s verbatim — this guard's premise has changed", stored)
			}

			patched, aerr := UpdateWorkItem(context.Background(), pool, seeded.ID, u, "admin", nil,
				updateReqFromJSON(t, `{"attrs_patch":{"a":1}}`))
			require.Nil(t, aerr, "merging onto a non-object stored value must not fail the request")
			assert.Equal(t, map[string]any{"a": float64(1)}, attrKeysOf(t, patched),
				"a non-object stored value must be coerced to an empty object, never merged into an array")

			unset, aerr := UpdateWorkItem(context.Background(), pool, seeded.ID, u, "admin", nil,
				updateReqFromJSON(t, `{"attrs_unset":["a"]}`))
			require.Nil(t, aerr, "deleting from a coerced value must not raise 'cannot delete from scalar'")
			assert.Equal(t, map[string]any{}, attrKeysOf(t, unset))
		})
	}
}

// TestUpdateWorkItemAttrs_NullFilledPatchIsNotAConflict is the end-to-end half of
// normalizeAttrsPatch: a client that null-fills its optional parameters must
// still be able to send a plain attrs update. Before normalisation this came
// back 400 "cannot be combined", naming a field the caller had not really sent.
func TestUpdateWorkItemAttrs_NullFilledPatchIsNotAConflict(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	seeded := seedWIWithAttrs(t, pool, project, u, `{"before":"x"}`)

	updated, aerr := UpdateWorkItem(context.Background(), pool, seeded.ID, u, "admin", nil,
		updateReqFromJSON(t, `{"attrs":{"after":"y"},"attrs_patch":null,"attrs_unset":[]}`))
	require.Nil(t, aerr, "a null-filled attrs_patch must be treated as absent, not as a conflicting instruction")
	assert.Equal(t, map[string]any{"after": "y"}, attrKeysOf(t, updated),
		"and the request must still behave as the plain REPLACE it is")
}

// TestUpdateWorkItemAttrs_RejectsAttrsCombinedWithPatch checks the guard end to
// end rather than only on the pure validator: sending both is a contradiction
// (replace everything vs. keep everything and amend), and resolving it silently
// by clause order is how this bug class starts.
func TestUpdateWorkItemAttrs_RejectsAttrsCombinedWithPatch(t *testing.T) {
	pool := setupLatestTestDB(t)
	u := testUser(t, pool)
	project := testProject(t, pool, u)

	seeded := seedWIWithAttrs(t, pool, project, u, `{"before":"unchanged"}`)

	_, aerr := UpdateWorkItem(context.Background(), pool, seeded.ID, u, "admin", nil,
		updateReqFromJSON(t, `{"attrs":{"x":1},"attrs_patch":{"y":2}}`))
	require.NotNil(t, aerr, "attrs together with attrs_patch must be rejected")
	assert.Equal(t, 400, aerr.HTTPStatus)

	fresh, gerr := GetWorkItem(context.Background(), pool, seeded.ID)
	require.Nil(t, gerr)
	assert.Equal(t, map[string]any{"before": "unchanged"}, attrKeysOf(t, fresh),
		"a rejected request must not have written anything")
}
