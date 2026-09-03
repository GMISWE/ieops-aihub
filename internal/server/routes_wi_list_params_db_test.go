package server

// End-to-end (hop 1→4) probes for the pf_list_work_items parameter contract,
// aihub#280. DB-gated; see the "aihub#280 list work_items param contract DB
// tests" step in .github/workflows/ci.yml.
//
// ─── Why the probe is shaped this way ───────────────────────────────────────
//
// The first investigation of this bug used `project=aihub&limit=50` and compared
// row counts. That cannot work, and the reason generalises:
//
//  1. n=50 was the *limit*, so "the filter applied and >50 rows still match" and
//     "the parameter was discarded" produce the identical number.
//  2. The obvious repair — raise the limit — reproduces the illusion, because
//     limit>200 is clamped to the 200 ceiling and disclosed in request_adjusted
//     (aihub#267/#314, work_items.go). It used to be reset to 50, silently.
//
// So this suite is built the only way that discriminates:
//
//   - a fixture set well under the limit (listParamsFixtureCount items), so a
//     full result is a small exact number, never a truncated page;
//   - probe values guaranteed to match nothing, so the pass criterion is the
//     absolute `n == 0` rather than a relative `n < baseline`;
//   - positive controls, to prove the query can still discriminate at all — an
//     always-empty result would satisfy every n==0 probe;
//   - a negative control (an unknown param), to prove the fix is "honour these
//     params" and not "narrow the result whenever anything unfamiliar arrives";
//   - and for include_step_state, which widens rows instead of narrowing the
//     set, an assertion on the response *key set*. No row count can detect it.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// listParamsFixtureCount is how many work items seedListParamsFixture creates.
// Deliberately far below the 50-row default limit and the 200-row cap so that
// "everything matched" is a distinguishable exact number.
const listParamsFixtureCount = 6

// seedListParamsFixture builds a small, fully-known work item set in a project
// of its own, varying every column the filters under test read.
//
// Inserted with raw SQL rather than domain.CreateWorkItem on purpose:
// CreateWorkItem runs goal-similarity dedup, so six work items in one project
// would have to have mutually dissimilar goals, and it cannot set scenario /
// milestone / source / priority per item anyway.
func seedListParamsFixture(t *testing.T, pool *pgxpool.Pool) (project, uid string, ids []string) {
	t.Helper()
	ctx := context.Background()
	uid, project = seedStepTestUserAndProject(t, pool)

	// Child-to-parent, so a rerun starts from a known-empty project.
	for _, q := range []string{
		`DELETE FROM wi_step_completions WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM agent_events WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM wi_dependencies WHERE blocked_wi_id IN (SELECT id FROM work_items WHERE project=$1)
		    OR blocking_wi_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM wi_step_state WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`UPDATE work_items SET current_attempt_id=NULL WHERE project=$1`,
		`DELETE FROM run_attempts WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM memories WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM work_items WHERE project=$1`,
	} {
		_, err := pool.Exec(ctx, q, project)
		require.NoError(t, err)
	}

	rows := []struct {
		scenario, goal, source, wiType, priority, status, milestone string
		requiresHuman                                               bool
	}{
		{"coding", "wire up the milestone filter", "human", "fix_bug", "urgent", "queued", "v2", false},
		{"coding", "teach the handler about since", "human", "fix_bug", "normal", "queued", "v2", false},
		{"coding", "an item that needs a person", "human", "feature", "high", "queued", "v3", true},
		{"coding", "already finished work", "admin", "chore", "low", "wrapped", "v3", false},
		{"writing", "cut the v1 alpha", "human", "chore", "normal", "wrapped", "", false},
		{"writing", "promote alpha to stable", "admin", "feature", "urgent", "queued", "", false},
	}
	for i, r := range rows {
		id := domain.NewID("wi")
		var milestone any
		if r.milestone != "" {
			milestone = r.milestone
		}
		_, err := pool.Exec(ctx, `
			INSERT INTO work_items (
				id, seq, project, scenario, goal, source, wi_type, priority,
				requires_human_session, milestone, labels, status,
				declared_resources, reporter_user_id, reporter_display,
				parent_work_item_id, attrs, content
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8,
				$9, $10, '{}', $11,
				'[]', $12, $12,
				NULL, '{}', NULL
			)`, id, 8100+i, project, r.scenario, r.goal, r.source, r.wiType, r.priority,
			r.requiresHuman, milestone, r.status, uid)
		require.NoError(t, err)
		ids = append(ids, id)
	}
	require.Len(t, ids, listParamsFixtureCount, "fixture size must match the documented constant")
	return project, uid, ids
}

// listParamsCount issues one authenticated GET and returns the row count.
func listParamsCount(t *testing.T, pool *pgxpool.Pool, rawQuery string, uc *UserContext) int {
	t.Helper()
	items, code := listParamsItems(t, pool, rawQuery, uc)
	require.Equal(t, http.StatusOK, code, "query %q should have succeeded", rawQuery)
	return len(items)
}

// listParamsItems returns the decoded items[] and the HTTP status.
func listParamsItems(t *testing.T, pool *pgxpool.Pool, rawQuery string, uc *UserContext) ([]map[string]any, int) {
	t.Helper()
	c, rec := newListWIRequest(t, rawQuery, uc)
	if err := handleListWorkItems(pool)(c); err != nil {
		t.Fatalf("handler returned an error for %q: %v", rawQuery, err)
	}
	if rec.Code != http.StatusOK {
		return nil, rec.Code
	}
	var decoded struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded), "body: %s", rec.Body.String())
	return decoded.Items, rec.Code
}

// listParamsViewer builds a read-only caller for the given project. The uid is
// passed in rather than derived so it matches the seeded row; handleListWorkItems
// never reads UserID, but a mismatched one invites a future test to rely on it.
func listParamsViewer(uid, project string) *UserContext {
	return &UserContext{
		UserID:       uid,
		DisplayName:  uid,
		Role:         "writer",
		ProjectRoles: map[string]string{project: "viewer"},
	}
}

// TestListWorkItemsParams_EndToEnd is the acceptance probe.
//
// Every "silently dropped" case asserts n == 0 against a value that matches
// nothing in the fixture. Before aihub#280 each of those returned the full
// fixture instead, which is precisely the symptom: no error, no warning, just
// the unfiltered set.
func TestListWorkItemsParams_EndToEnd(t *testing.T) {
	pool := setupStepTestDB(t)
	project, uid, ids := seedListParamsFixture(t, pool)
	uc := listParamsViewer(uid, project)

	base := "project=" + project + "&limit=100"

	// Guard the guard: the whole design rests on the fixture being smaller than
	// the limit, so a full result is an exact number and not a clamped page.
	if n := listParamsCount(t, pool, base, uc); n != listParamsFixtureCount {
		t.Fatalf("baseline returned %d items, want exactly %d — the fixture must be smaller than the limit or every n==0 probe below is uninterpretable",
			n, listParamsFixtureCount)
	}

	t.Run("positive controls discriminate", func(t *testing.T) {
		// If these ever all return the full set or all return 0, the n==0 probes
		// below prove nothing.
		for _, tc := range []struct {
			query string
			want  int
		}{
			{"status=wrapped", 2},
			{"status=wrapped,queued", 6},
			{"wi_type=fix_bug", 2},
			{"priority=urgent", 2},
			{"scenario=writing", 2},
			{"milestone=v2", 2},
			{"source=admin", 2},
			// ready_only = queued + no human session + no live blocker.
			// Fixture: 4 queued, of which 1 requires a human session.
			{"ready_only=true", 3},
			{"ids=" + ids[0], 1},
			{"ids=" + ids[0] + "," + ids[1], 2},
			{"since=2000-01-01T00:00:00Z", 6},
		} {
			if n := listParamsCount(t, pool, base+"&"+tc.query, uc); n != tc.want {
				t.Errorf("%s: got n=%d, want %d — this positive control must discriminate, "+
					"or the n==0 probes below are vacuous", tc.query, n, tc.want)
			}
		}
	})

	t.Run("dropped params now filter: n must be 0", func(t *testing.T) {
		// One case per parameter that a caller could name but not rely on. The
		// value cannot match anything, so 0 is the only correct answer and
		// listParamsFixtureCount is the old, broken one.
		//
		// Two different old failures are covered here, deliberately not
		// distinguished by the assertion because the caller could not
		// distinguish them either:
		//   - since / milestone / kind / source / scenario were dropped by the
		//     HTTP handler or the SQL, so even a direct HTTP caller was ignored;
		//   - wi_type / priority / ids / label already worked over HTTP but were
		//     absent from the MCP schema, so no polyforge skill could reach them.
		// Both presented as "I sent a filter and got everything back".
		for _, tc := range []struct{ name, query string }{
			{"since", "since=2099-01-01T00:00:00Z"},
			{"milestone", "milestone=zzz-no-such"},
			{"kind (alias of wi_type)", "kind=zzz-no-such"},
			{"source", "source=zzz-no-such"},
			{"scenario", "scenario=zzz-nope"},
			{"wi_type", "wi_type=zzz-no-such"},
			{"priority", "priority=zzz-no-such"},
			{"ids", "ids=wi_zzznosuchid"},
			{"label", "label=zzz-nope"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				n := listParamsCount(t, pool, base+"&"+tc.query, uc)
				if n == listParamsFixtureCount {
					t.Errorf("%s returned the whole fixture (n=%d): the parameter is being silently dropped",
						tc.query, n)
				} else if n != 0 {
					t.Errorf("%s returned n=%d, want 0 — the value matches nothing in the fixture", tc.query, n)
				}
			})
		}
	})

	t.Run("ready_only excludes the human-session item", func(t *testing.T) {
		// Sharper than the count: name the specific item ready_only must drop,
		// so a predicate that happens to return 3 rows for the wrong reason
		// still fails.
		items, code := listParamsItems(t, pool, base+"&ready_only=true", uc)
		// Without this the whole subtest is vacuous on an error response: items
		// would be nil and the loop below would never execute.
		if code != http.StatusOK {
			t.Fatalf("ready_only=true returned %d, want 200", code)
		}
		if len(items) == 0 {
			t.Fatalf("ready_only=true returned no items; the loop below would assert nothing")
		}
		for _, it := range items {
			if rh, ok := it["requires_human_session"].(bool); ok && rh {
				t.Errorf("ready_only returned %v, which requires a human session", it["slug"])
			}
			if st, _ := it["status"].(string); st != "queued" {
				t.Errorf("ready_only returned %v with status %q, want queued", it["slug"], st)
			}
		}
	})

	t.Run("negative control: unknown param must not filter", func(t *testing.T) {
		// The failure this catches: "narrow the result whenever an unrecognised
		// param arrives" would make every n==0 probe above pass while the actual
		// parameters still did nothing.
		if n := listParamsCount(t, pool, base+"&zzqqbogusparam=1", uc); n != listParamsFixtureCount {
			t.Errorf("an unknown param changed the result (n=%d, want %d) — the params are not being "+
				"honoured, the query is just being narrowed by anything unfamiliar", n, listParamsFixtureCount)
		}
	})

	t.Run("since is a boundary, not a no-op", func(t *testing.T) {
		// A `since` in the past returning everything and a `since` in the future
		// returning nothing are both consistent with "parsed and applied"; only
		// checking both directions rules out a predicate that ignores its value.
		if n := listParamsCount(t, pool, base+"&since=2000-01-01T00:00:00Z", uc); n != listParamsFixtureCount {
			t.Errorf("since in the past must match everything; got n=%d", n)
		}
		if n := listParamsCount(t, pool, base+"&since=2099-01-01T00:00:00Z", uc); n != 0 {
			t.Errorf("since in the future must match nothing; got n=%d", n)
		}
	})

	t.Run("unparseable since is rejected, not ignored", func(t *testing.T) {
		_, code := listParamsItems(t, pool, base+"&since=last-tuesday", uc)
		if code != http.StatusBadRequest {
			t.Errorf("an unparseable since must 400; got %d", code)
		}
	})
}

// include_step_state widens each row rather than narrowing the set, so its guard
// asserts the response key set. A row count is structurally incapable of
// detecting it — which is why it went unnoticed the longest.
func TestListWorkItemsParams_IncludeStepStateChangesTheKeySet(t *testing.T) {
	pool := setupStepTestDB(t)
	project, uid, ids := seedListParamsFixture(t, pool)
	uc := listParamsViewer(uid, project)

	// step_state comes from wi_step_state, which only exists once a wi has been
	// claimed. Seed one row so "absent" cannot be explained away as "no data".
	_, err := pool.Exec(context.Background(), `
		INSERT INTO wi_step_state (work_item_id, wi_type, graph_source, current_step, current_step_status)
		VALUES ($1, 'fix_bug', 'scenario_config', 'implement', 'in_progress')
		ON CONFLICT (work_item_id) DO UPDATE SET current_step='implement', current_step_status='in_progress'`,
		ids[0])
	require.NoError(t, err)

	query := "project=" + project + "&limit=100&ids=" + ids[0]

	without, _ := listParamsItems(t, pool, query, uc)
	require.Len(t, without, 1)
	if _, present := without[0]["step_state"]; present {
		t.Errorf("step_state must be absent when the caller did not ask for it; keys: %v", keysOf(without[0]))
	}

	with, _ := listParamsItems(t, pool, query+"&include_step_state=true", uc)
	require.Len(t, with, 1)
	raw, present := with[0]["step_state"]
	if !present {
		t.Fatalf("include_step_state=true must add a step_state key; got the same %d keys as without it: %v",
			len(with[0]), keysOf(with[0]))
	}

	// Present is not enough — a null would also be "present". The step data has
	// to be the data.
	state, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("step_state must be an object, got %T (%v)", raw, raw)
	}
	if got := state["current_step"]; got != "implement" {
		t.Errorf("step_state.current_step = %v, want implement", got)
	}
	if got := state["current_step_status"]; got != "in_progress" {
		t.Errorf("step_state.current_step_status = %v, want in_progress", got)
	}

	// The key set must differ by exactly step_state; anything else means the
	// param has side effects on the row shape beyond what it advertises.
	if len(with[0]) != len(without[0])+1 {
		t.Errorf("include_step_state must add exactly one key: %d keys with vs %d without\nwith:    %v\nwithout: %v",
			len(with[0]), len(without[0]), keysOf(with[0]), keysOf(without[0]))
	}

	// A wi that was never claimed has no wi_step_state row, so step_state stays
	// absent even when asked for. Documented behaviour, asserted so it stays a
	// decision rather than becoming a surprise.
	unclaimed, _ := listParamsItems(t, pool, "project="+project+"&limit=100&ids="+ids[1]+"&include_step_state=true", uc)
	require.Len(t, unclaimed, 1)
	if _, present := unclaimed[0]["step_state"]; present {
		t.Errorf("a never-claimed wi must have no step_state; got %v", unclaimed[0]["step_state"])
	}
}

// The three skills' actual opening call: ids= with no project=. This used to be
// a hard 400, so pf-status, pf-retro and pf-execute each began with a failing
// request.
func TestListWorkItemsParams_IdsWithoutProjectEndToEnd(t *testing.T) {
	pool := setupStepTestDB(t)
	project, uid, ids := seedListParamsFixture(t, pool)
	uc := listParamsViewer(uid, project)

	items, code := listParamsItems(t, pool, "ids="+ids[0], uc)
	if code != http.StatusOK {
		t.Fatalf("ids= with no project= must succeed; got %d", code)
	}
	if len(items) != 1 {
		t.Fatalf("expected exactly the requested wi, got %d items", len(items))
	}
	if items[0]["id"] != ids[0] {
		t.Errorf("returned %v, want %v", items[0]["id"], ids[0])
	}

	// The authorization half: relaxing project= must not let a caller read ids
	// outside the projects they can see. A viewer of some *other* project asking
	// for this id must get nothing — not a 200 with the row, and not a 500.
	outsider := &UserContext{
		UserID:       "u_outsider_listparams",
		DisplayName:  "outsider",
		Role:         "writer",
		ProjectRoles: map[string]string{"p_some_other_project": "viewer"},
	}
	items, code = listParamsItems(t, pool, "ids="+ids[0], outsider)
	if code != http.StatusOK {
		t.Fatalf("an outsider's ids= lookup should be an empty 200, got %d", code)
	}
	if len(items) != 0 {
		t.Errorf("an ids= lookup must be bounded to the caller's accessible projects; leaked %d items", len(items))
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
