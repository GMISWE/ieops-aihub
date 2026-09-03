package domain

// DB-gated integration test for aihub#329: the SERVER's fill of the
// ClaimResponse fields the claiming client cannot reconstruct.
//
// aihub#322 added `Goal string \`json:"goal,omitempty"\`` to ClaimResponse and
// filled it at BOTH exits of FnClaimWorkItem — the idempotent replay and the
// fresh claim — because the MCP claim handler derives the task branch name from
// it (internal/mcp/tools_lifecycle.go: `wiGoal, _ := result["goal"].(string)`
// then newClaimBranchNames). Nothing asserted that fill. Measured on
// aihub#322's own tree: blanking BOTH exits to `Goal: ""` left
// `GOWORK=off go test -count=1 -race ./...` at exit 0.
//
// It went uncaught because internal/mcp/claim_handler_wiring_test.go's fake
// aihub HARDCODES "goal" into its response map. That test pins three hops — the
// wire key as consumed, the handler's read of it, and newClaimBranchNames — and
// is blind to the fourth: whether this server emits the key at all. This file
// is that fourth hop, and it must not use a fake: a fixture that supplies the
// value is exactly the defect being guarded against.
//
// Why the omission is silent rather than loud, which is why an assertion is
// worth a DB-gated step:
//
//   - the field is `omitempty`, so a server that stops filling it emits NO KEY,
//     not an empty string;
//   - the client reads it with `result["goal"].(string)`, whose comma-ok is
//     discarded, so an absent key yields "" with no error;
//   - newClaimBranchNames("aihub", "322", "", ulid8) then produces
//     `polyforge/aihub-322` — a completely legal, completely unremarkable branch
//     name.
//
// Nothing observable changes. Zero value versus absent field, with a struct tag
// in between.
//
// Both exits are asserted as SEPARATE SUBTESTS on purpose (aihub#329 criterion
// 2): blanking one exit must go red on its own. A single arm covering only the
// fresh path would stay green while every RETRIED claim — the replay branch, the
// one that still has to build a worktree — degraded silently.
//
// Slug/Project/ID are asserted alongside Goal (criterion 5): they are
// `omitempty` too, they are filled at the same two exits, and ResolveStateFile's
// slug scan depends on Slug being non-empty. Do not assume only Goal was
// unguarded.
//
// # The class, not the instance
//
// aihub#329 names Goal. Widening the search to "other omitempty fields on
// ClaimResponse" — the obvious move — stops inside one struct. Anchoring
// instead on what the defect IS, "a field the SERVER fills that the CLIENT
// consumes to name or key something local", walks out of the struct and lands
// on a third exit nobody had looked at: FnForceTakeover. aihub#319 added
// ID/Slug/Project to ForceTakeoverResponse for exactly the reason aihub#322
// added Goal to ClaimResponse — internal/mcp/tools_lifecycle.go reads
// result["id"]/["slug"]/["project"] there too, to key the state file and keep
// ResolveStateFile's slug scan able to find it — and nothing asserted that fill
// either. So this file covers all three exits.
//
// The takeover fields are NOT omitempty (`json:"id"`, not `json:"id,omitempty"`),
// so a blanked one arrives as `"id":""` rather than as an absent key. The
// mechanism differs; the outcome does not, because the client-side read is the
// same discarded comma-ok and "" is what it ends up with either way. Recorded
// because "same struct tag" would be a false reason to trust this arm.
//
// Run:
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestServerFilledResponseFieldsAreEchoed' -race -v -count=1

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServerFilledResponseFieldsAreEchoed pins the server side of the claim and
// takeover contracts: the fields the client consumes but cannot derive on its
// own.
//
// One function with subtests rather than one per exit, because
// internal/citest/dbtestcov counts DB-gated FUNCTIONS and three would move the
// -min-gated ratchet three times for one guard. The per-exit claim lives in the
// subtest names, which ci.yml asserts on individually.
func TestServerFilledResponseFieldsAreEchoed(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	u := testUser(t, pool)
	project := testProject(t, pool, u)
	const goal = "echo the goal back so the claiming client can name its branch"
	wi := seedClaimableWI(t, pool, project, u, goal, "")

	// One idempotency key, used twice. The first call takes the fresh path; the
	// second finds the key already used and takes the replay path. That is
	// exactly the shape of a retried claim, which is the only way the replay
	// exit is ever reached in production.
	const idemKey = "aihub329-echo"
	claim := func() *ClaimResponse {
		t.Helper()
		resp, aerr := FnClaimWorkItem(ctx, pool, wi.ID, &ClaimRequest{
			IdempotencyKey: idemKey,
			SessionInfo: SessionInfo{
				MachineID:     "m_aihub329",
				SessionSecret: "aihub329-secret-0123456789abcdef0123456789abcdef0123456789ab",
			},
			Mode: "fresh",
		}, u, "", "tester")
		require.Nil(t, aerr, "claim failed: %+v", aerr)
		require.NotNil(t, resp)
		return resp
	}

	// assertEchoed is shared so the two exits are held to a byte-identical
	// contract. A replay that returned a DIFFERENT goal would be just as broken
	// as one that returned none, and only comparing both against wi catches it.
	assertEchoed := func(t *testing.T, resp *ClaimResponse, exit string) {
		t.Helper()
		assert.Equal(t, goal, resp.Goal,
			"%s: ClaimResponse.Goal is `omitempty`, so a server that stops filling it emits no key at all; "+
				"the MCP claim handler's `result[\"goal\"].(string)` then yields \"\" without an error and every "+
				"task branch silently degrades to polyforge/<project>-<seq>, which looks entirely normal", exit)
		assert.Equal(t, wi.Slug, resp.Slug,
			"%s: Slug is `omitempty` and ResolveStateFile's slug scan cannot match a state file written without it", exit)
		assert.Equal(t, project, resp.Project,
			"%s: Project is `omitempty` and the claim handler keys the worktree directory (pf.<project>-<seq>) off it", exit)
		assert.Equal(t, wi.ID, resp.ID,
			"%s: ID is `omitempty` and the claim handler falls back to the caller-supplied slug without it, "+
				"writing a slug-keyed state file that later step/event calls send back as a slug", exit)
	}

	// MUTANT: internal/domain/run_attempts.go, the FRESH exit — set `Goal: ""`
	// in the ClaimResponse literal at the end of FnClaimWorkItem. Only this
	// subtest goes red.
	t.Run("fresh claim", func(t *testing.T) {
		assertEchoed(t, claim(), "fresh claim")
	})

	// MUTANT: internal/domain/run_attempts.go, the IDEMPOTENT exit — set
	// `Goal: ""` in the ClaimResponse literal inside the `idemErr == nil`
	// branch. Only this subtest goes red. This is the exit a RETRY sees, and a
	// retry still has to create the worktree, so it is not the lesser half.
	t.Run("idempotent replay", func(t *testing.T) {
		first := claim()
		replay := claim()
		require.Equal(t, first.AttemptID, replay.AttemptID,
			"the second call did not take the idempotency branch, so this subtest is exercising the fresh "+
				"exit twice and asserts nothing about the replay")
		assertEchoed(t, replay, "idempotent replay")
	})

	// MUTANT: internal/domain/run_attempts.go, FnForceTakeover's response
	// literal — set `Slug: ""` (or ID, or Project). Only this subtest goes red.
	//
	// The third exit, reached by anchoring on the defect rather than on the
	// struct aihub#329 named. A takeover that returns no `id` makes the MCP
	// layer fall back to whatever the caller passed — a slug — and write a
	// slug-keyed state file with an empty Slug field, which ResolveStateFile's
	// slug scan can then never match. aihub#319 fixed that; nothing held it
	// fixed.
	t.Run("force takeover", func(t *testing.T) {
		ctx := context.Background()
		// Claim here rather than relying on the subtests above: FnForceTakeover
		// rejects a work item that is not already running, and `go test -run
		// '.../force_takeover'` runs the parent body but skips its siblings.
		// Without this the subtest would 400 when selected alone — a green suite
		// and a red single case, which is the worst way to find out.
		claim()

		resp, aerr := FnForceTakeover(ctx, pool, wi.ID, u, "tester", "admin",
			map[string]string{project: "maintainer"},
			&ForceTakeoverRequest{
				Reason:      "aihub#329: the takeover response keys the state file too",
				SessionInfo: SessionInfo{MachineID: "m_aihub329", SessionSecret: "aihub329-takeover-secret"},
			})
		require.Nil(t, aerr, "force_takeover failed: %+v", aerr)

		assert.Equal(t, wi.ID, resp.ID,
			"force takeover: without `id` the MCP layer keys the state file by whatever the caller passed — "+
				"a slug — and every later step/event/complete call sends that slug back as a work item id")
		assert.Equal(t, wi.Slug, resp.Slug,
			"force takeover: a state file written with an empty Slug is invisible to ResolveStateFile's slug scan")
		assert.Equal(t, project, resp.Project,
			"force takeover: pf_ship / pf_diff / pf_commit resolve worktree paths through the state file's Project")
		// Not NotEmpty/NotZero: a response echoing the PRIOR attempt id, or
		// epoch 1 instead of the incremented one, satisfies both while writing a
		// state file that fails verifyAttemptCredential on the very next call.
		// The independent oracle is the work_items row the takeover just wrote.
		var liveAttemptID string
		var liveEpoch int64
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT current_attempt_id, current_attempt_epoch FROM work_items WHERE id=$1`, wi.ID,
		).Scan(&liveAttemptID, &liveEpoch))
		assert.Equal(t, liveAttemptID, resp.NewAttemptID,
			"force takeover: the echoed attempt id must be the one the server actually made current, or the "+
				"state file authenticates as nothing")
		assert.Equal(t, liveEpoch, resp.NewClaimEpoch,
			"force takeover: the echoed epoch must match the stored one, or every later call 409s on epoch mismatch")
		assert.NotEqual(t, resp.PriorAttemptID, resp.NewAttemptID,
			"force takeover: echoing the superseded attempt would look well-formed and authenticate as a "+
				"dead attempt")
	})
}
