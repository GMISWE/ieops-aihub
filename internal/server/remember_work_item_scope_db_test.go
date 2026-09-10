package server

// DB-gated acceptance test for aihub#371: writing a memory validated `project`
// and `work_item_id` independently and never against each other.
//
// 🔴 The shape of the defect. domain.Remember resolved work_item_id (an id or a
// slug) to a canonical work_items.id through a bare GetWorkItem, which looks up
// any work item on the server and compares its project with nothing.
// handleRemember authorizes on req.Project alone. So POST /v1/memories with
// project=A and work_item_id=<a work item in B> was written verbatim as
// (project=A, work_item_id=<B's work item>), and a second such row went into
// agent_events beside it.
//
// What makes that worse than it looks: recall ANDs the work_item_id filter onto
// the project scope, so the row is invisible to the one query that ever asks
// for it — pf_recall(project=wi.project, work_item_id=wi_id), which pf-work
// runs on EVERY claim path to pick up the previous agent's handover notes. The
// memory is not leaked to the wrong project; it is write-only. Two neighbours
// in the same file already had the rule (EmitEvent derives project FROM the
// work item; the aihub#210 supersede check compares projects), which is why
// this reads as an omission rather than a design.
//
// 🔴 The other half of the acceptance. Closing a write hole must not open a
// read one. Before the fix, a caller with no role at all on project B could
// tell B#7 (201, memory created) from B#9999 (404) — a one-bit existence oracle
// over `<project>#<seq>`, which is two guessable tokens. This is the trap
// aihub#357 hit on blocked_by, and the reason the scope is a predicate inside
// the resolving query rather than a comparison on its result: "no such work
// item" and "not in this project" are one zero-row outcome, so no later branch
// can grow a second answer. The arms below assert that equivalence directly.
//
// Real router, real auth middleware, real echo binder, real Postgres: the
// mismatch is only observable across two projects that really exist, and the
// FK plus the recall predicate that make the row unreadable live in the
// database. A domain test on one project can see neither.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/server/ -run TestRememberRejectsCrossProjectWorkItem -v -count=1
//
// One test FUNCTION with subtests, per aihub#334: internal/citest/dbtestcov
// ratchets on the number of DB-gated functions, and the per-arm coverage claim
// lives in the CI step's `--- PASS:` greps.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/auth"
	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// rememberScopeStack is two real projects, a writer who holds a role on only
// one of them, and an admin who holds none and may write to both. Both callers
// are needed: the writer states the enumeration half, the admin states that the
// consistency half is not an authorization rule and therefore has no admin
// exemption.
type rememberScopeStack struct {
	url   string
	pool  *pgxpool.Pool
	projA string
	projB string
	// uid/key: writer on projA, NO role on projB, not an admin.
	uid string
	key string
	// admUID/admKey: a server admin, which checkProjectAccess lets write to
	// every project (middleware.go, `if u.Role == "admin" { return nil }`).
	admUID string
	admKey string
	// bUID owns projB and seeds its rows; it never issues a request.
	bUID string
}

func newRememberScopeStack(t *testing.T) *rememberScopeStack {
	t.Helper()
	pool := serverTestPool(t)
	ctx := context.Background()

	// Sanitize caps at 37 chars so a 2-char prefix fits projects.name's CHECK;
	// the a/b discriminator goes INTO the sanitized string rather than onto the
	// result, which would overflow by one (same note as newRecallSlugStack).
	projA := "p_" + testname.Sanitize(t.Name()+"a")
	projB := "p_" + testname.Sanitize(t.Name()+"b")
	base := testname.Sanitize(t.Name())
	uid, admUID, bUID := "u_"+base+"r", "u_"+base+"m", "u_"+base+"o"
	key, admKey := "pfk_"+uid, "pfk_"+admUID

	seedUser := func(id, role, apiKey, keyID string) {
		var keys []byte
		if apiKey == "" {
			keys = []byte(`[]`)
		} else {
			var err error
			keys, err = json.Marshal([]map[string]any{{"id": keyID, "key_hash": auth.HashKey(apiKey)}})
			require.NoError(t, err)
		}
		_, err := pool.Exec(ctx, `
			INSERT INTO users(id,email,display_name,user_type,role,api_keys)
			VALUES($1,$1||'@test.local',$1,'human',$2,$3)
			ON CONFLICT (id) DO UPDATE SET api_keys=EXCLUDED.api_keys, role=EXCLUDED.role`,
			id, role, keys)
		require.NoError(t, err)
	}
	// users.role is CHECKed to (writer|admin). Non-admin is load-bearing for
	// the writer: an admin sees every project and the enumeration arms would
	// pass vacuously.
	seedUser(uid, "writer", key, "k_remscope")
	seedUser(admUID, "admin", admKey, "k_remscopeadm")
	seedUser(bUID, "writer", "", "")

	membersA, err := json.Marshal([]map[string]any{{"user_id": uid, "role": "writer"}})
	require.NoError(t, err)

	for _, p := range []struct {
		name    string
		members []byte
	}{
		{projA, membersA},
		// projB has no members entry for uid and is owned by someone else. That
		// absence is the fixture.
		{projB, []byte(`[]`)},
	} {
		_, err := pool.Exec(ctx, `
			INSERT INTO projects(name,owner_user_id,members) VALUES($1,$2,$3)
			ON CONFLICT (name) DO UPDATE SET owner_user_id=EXCLUDED.owner_user_id, members=EXCLUDED.members`,
			p.name, bUID, p.members)
		require.NoError(t, err, "seeding project %s", p.name)
	}

	// Project names derive from t.Name(), so they are the same strings on every
	// run against the same database: without this, the previous run's rows
	// decide this run's seq numbers and therefore its slugs. Memories go first —
	// memories.work_item_id has no ON DELETE CASCADE, so a leftover memory would
	// make the work-item delete fail rather than reset anything.
	for _, p := range []string{projA, projB} {
		_, err := pool.Exec(ctx,
			`UPDATE memories SET latest_id=NULL, supersedes_id=NULL WHERE project=$1`, p)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `DELETE FROM memories WHERE project=$1`, p)
		require.NoError(t, err)
		resetProjectWorkItems(t, pool, p)
	}

	ts := httptest.NewServer(NewRouter(pool, []byte("remember-scope-test-cookie-secret")))
	t.Cleanup(ts.Close)
	return &rememberScopeStack{
		url: ts.URL, pool: pool, projA: projA, projB: projB,
		uid: uid, key: key, admUID: admUID, admKey: admKey, bUID: bUID,
	}
}

// seedWI creates a work item through the real domain path so it gets a real
// slug. Goals stay mutually dissimilar: CreateWorkItem runs goal-similarity
// dedup inside a project and would reject a close match before any handler.
func (s *rememberScopeStack) seedWI(t *testing.T, project, goal string) *domain.WorkItem {
	t.Helper()
	wi, aerr := domain.CreateWorkItem(context.Background(), s.pool, &domain.CreateWorkItemRequest{
		Project: project,
		Goal:    goal,
		Source:  "human",
	}, s.bUID, s.bUID, nil, "")
	require.Nil(t, aerr, "seeding %q failed: %+v", goal, aerr)
	require.NotEmpty(t, wi.Slug, "the fixture needs a slug to exercise slug resolution")
	return wi
}

// remember POSTs one memory as the given caller and returns the raw answer.
func (s *rememberScopeStack) remember(t *testing.T, key string, body map[string]any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	r, err := http.NewRequest(http.MethodPost, s.url+"/v1/memories", bytes.NewReader(raw))
	require.NoError(t, err)
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, out
}

// memoryBody is one remember payload with the knobs these arms turn.
func memoryBody(project, content string, workItemID any) map[string]any {
	b := map[string]any{
		"type":       "experience.pitfall",
		"content":    content,
		"visibility": "project",
		"dedup_mode": "off",
	}
	if project != "" {
		b["project"] = project
	}
	if workItemID != nil {
		b["work_item_id"] = workItemID
	}
	return b
}

// countMemories is how the arms show that a refusal wrote nothing. A 4xx with
// the row already inserted would satisfy a status assertion and none of the
// point.
func (s *rememberScopeStack) countMemories(t *testing.T, project string) int {
	t.Helper()
	var n int
	require.NoError(t, s.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM memories WHERE project=$1`, project).Scan(&n))
	return n
}

// storedWorkItemID reads back what actually landed in the column, which is the
// only thing that distinguishes "accepted" from "accepted and correct".
func (s *rememberScopeStack) storedWorkItemID(t *testing.T, memID string) string {
	t.Helper()
	var got *string
	require.NoError(t, s.pool.QueryRow(context.Background(),
		`SELECT work_item_id FROM memories WHERE id=$1`, memID).Scan(&got))
	require.NotNil(t, got, "memory %s stored a NULL work_item_id", memID)
	return *got
}

// memIDOf pulls the created memory's id out of a 201 body.
func memIDOf(t *testing.T, body []byte) string {
	t.Helper()
	var resp struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &resp), "body was %q", body)
	require.NotEmpty(t, resp.ID)
	return resp.ID
}

func TestRememberRejectsCrossProjectWorkItem(t *testing.T) {
	s := newRememberScopeStack(t)

	wiA := s.seedWI(t, s.projA, "rotate expired TLS certificates on the edge proxies")
	wiB := s.seedWI(t, s.projB, "archive stale feature flags older than one year")

	// ── 0. Fixture controls. Without these the negative arms below pass just as
	//       well against a broken fixture — a work item that is not really in
	//       the other project, or a caller who can read it perfectly well.
	t.Run("the fixture really spans two projects", func(t *testing.T) {
		require.Equal(t, s.projA, wiA.Project)
		require.Equal(t, s.projB, wiB.Project,
			"the cross-project arms are vacuous unless this work item is really in the other project")
	})

	// 🔴 Expected status changed 403 -> 404 on 2026-09-06 (aihub#377). CONTRACT
	// CHANGE, not a red test tuned green. The invariant, verbatim:
	//
	//	在某个 project 里的用户，能看到该 project 的一切（memory、work item、
	//	artifact、event、step、依赖）；不在的，对该 project 的一切必须拿到与
	//	「不存在」逐字节相同的响应。
	//
	//	(A user who is in a project can see everything about it. A user who is
	//	not must get a response byte-identical to the one for something that
	//	does not exist.)
	//
	// 🔴 STILL DISCRIMINATING. If the writer could write projB this arm answers
	// 201 Created, so a fixture that forgot to withhold access still goes red.
	// The change is which refusal counts as correct, not whether a refusal is
	// required.
	t.Run("the caller really cannot read the other project", func(t *testing.T) {
		status, body := s.remember(t, s.key, memoryBody(s.projB, "a memory the writer may not write", nil))
		assert.Equal(t, http.StatusNotFound, status,
			"fixture is broken: got %d, want 404 — the writer is supposed to have NO role "+
				"on %s. A 201 means the fixture granted access; a 403 means the refusal "+
				"still confirms the project exists (body %s)", status, s.projB, body)
	})

	// ── 1/2. The positive controls, both spellings. Green before AND after the
	//        fix, and they are the reason "reject every work_item_id" cannot
	//        pass for a fix — that would satisfy every negative arm below.
	t.Run("a same-project work item still attaches by canonical id", func(t *testing.T) {
		status, body := s.remember(t, s.key, memoryBody(s.projA, "the retry budget is per attempt, not per request", wiA.ID))
		require.Equal(t, http.StatusCreated, status, "body %s", body)
		assert.Equal(t, wiA.ID, s.storedWorkItemID(t, memIDOf(t, body)))
	})

	t.Run("a same-project work item still attaches by slug", func(t *testing.T) {
		status, body := s.remember(t, s.key, memoryBody(s.projA, "certificate rotation reloads the proxy, it does not restart it", wiA.Slug))
		require.Equal(t, http.StatusCreated, status,
			"aihub#127: a slug must still resolve to the canonical id the FK requires (body %s)", body)
		assert.Equal(t, wiA.ID, s.storedWorkItemID(t, memIDOf(t, body)),
			"the slug must be stored as the canonical id, not verbatim")
	})

	// ── 3. The other positive control, and the one the fix could most easily
	//       have broken: handleRemember back-fills project FROM the work item
	//       when project is omitted (routes_memory.go). Callers depend on it,
	//       and after the back-fill the two agree by construction, so the new
	//       scope must be a no-op on this path.
	t.Run("omitting project still back-fills it from the work item", func(t *testing.T) {
		status, body := s.remember(t, s.key, memoryBody("", "the backfill path must keep working", wiA.Slug))
		require.Equal(t, http.StatusCreated, status, "body %s", body)
		var resp struct {
			Project string `json:"project"`
		}
		require.NoError(t, json.Unmarshal(body, &resp))
		assert.Equal(t, s.projA, resp.Project, "project should have been back-filled from the work item")
		assert.Equal(t, wiA.ID, s.storedWorkItemID(t, memIDOf(t, body)))
	})

	// ── 4/5. The defect itself. RED before the fix in both spellings: each
	//        answered 201 and wrote (project=A, work_item_id=<B's work item>).
	//        The canonical-id arm matters on its own — a caller who already
	//        holds the id never needs the slug, so a fix that only scoped slug
	//        lookups would leave the hole open for exactly that caller.
	t.Run("a cross-project work item is refused by slug", func(t *testing.T) {
		before := s.countMemories(t, s.projA)
		status, body := s.remember(t, s.key, memoryBody(s.projA, "mounting project B's work item from project A", wiB.Slug))
		assert.Equal(t, http.StatusNotFound, status,
			"project=%s with work_item_id=%s stores a memory recall can never return (body %s)", s.projA, wiB.Slug, body)
		assert.Equal(t, before, s.countMemories(t, s.projA),
			"the write was refused but the row was created anyway")
	})

	t.Run("a cross-project work item is refused by canonical id", func(t *testing.T) {
		before := s.countMemories(t, s.projA)
		status, body := s.remember(t, s.key, memoryBody(s.projA, "mounting project B's work item by its id", wiB.ID))
		assert.Equal(t, http.StatusNotFound, status, "body %s", body)
		assert.Equal(t, before, s.countMemories(t, s.projA))
	})

	// ── 6. No admin exemption, and this is a deliberate divergence from
	//       resolveBlockedByRef (work_items.go), which DOES widen its scope for
	//       an admin. There the question is "may this caller see it" and an
	//       admin may. Here it is "will this row ever be readable", which does
	//       not depend on who wrote it: the admin's cross-project mount is the
	//       identical unrecallable row. checkProjectAccess returns early for
	//       role=admin, so before the fix this caller could write the mismatch
	//       into any pair of projects on the server.
	t.Run("an admin is refused the same cross-project mount", func(t *testing.T) {
		before := s.countMemories(t, s.projA)
		status, body := s.remember(t, s.admKey, memoryBody(s.projA, "an admin mounting across projects", wiB.Slug))
		assert.Equal(t, http.StatusNotFound, status,
			"an admin creates the same write-only row as anyone else (body %s)", body)
		assert.Equal(t, before, s.countMemories(t, s.projA))
	})

	// ── 7. The admin's own positive control. Without it arm 6 is satisfied by
	//       an admin who cannot write memories at all.
	t.Run("an admin can still attach within one project", func(t *testing.T) {
		status, body := s.remember(t, s.admKey, memoryBody(s.projA, "admins keep writing same-project memories", wiA.ID))
		require.Equal(t, http.StatusCreated, status, "body %s", body)
		assert.Equal(t, wiA.ID, s.storedWorkItemID(t, memIDOf(t, body)))
	})

	// ── 8/9. Containment. The refusal must not become a NEW signal: a work
	//        item in a project the caller cannot see and a work item that does
	//        not exist have to answer identically, or `<project>#<seq>` — two
	//        guessable tokens — enumerates every project on the server for a
	//        caller who is 403'd on every honest read of them. Asserted
	//        byte-for-byte against the nonexistent-reference response, which is
	//        the reference case.
	absentStatus, absentBody := s.remember(t, s.key,
		memoryBody(s.projA, "probing a work item that was never minted", "wi_thisidwasnevermintedanywhere"))
	t.Run("a nonexistent work item is refused too", func(t *testing.T) {
		require.Equal(t, http.StatusNotFound, absentStatus, "body %s", absentBody)
	})

	t.Run("an invisible work item answers exactly what a nonexistent one does", func(t *testing.T) {
		status, body := s.remember(t, s.key,
			memoryBody(s.projA, "probing a work item that was never minted", wiB.Slug))
		assert.Equal(t, absentStatus, status,
			"%s exists; answering differently from a miss makes <project>#<seq> enumerable", wiB.Slug)
		assert.JSONEq(t,
			// Both bodies echo the caller's own reference and nothing else, so
			// normalising that one string is what makes them comparable.
			normalizeRef(t, absentBody, "wi_thisidwasnevermintedanywhere"),
			normalizeRef(t, body, wiB.Slug),
			"the response differs by more than the reference the caller supplied, which is the oracle itself")
	})

	// ── 10. The supersede path, which reaches the same column by another door.
	//        aihub#210's C5 check compares the TARGET memory's project and work
	//        item against the request's, using the RESOLVED work_item_id — so
	//        if the resolution were unscoped, a cross-project work item would
	//        ride in underneath a supersede that C5 itself waves through.
	t.Run("a supersede cannot carry a cross-project work item either", func(t *testing.T) {
		status, body := s.remember(t, s.key, memoryBody(s.projA, "the lineage head this arm supersedes", nil))
		require.Equal(t, http.StatusCreated, status, "seeding the supersede target: %s", body)
		head := memIDOf(t, body)

		before := s.countMemories(t, s.projA)
		sup := memoryBody(s.projA, "a newer body that also re-homes the work item", wiB.Slug)
		sup["supersedes_memory_id"] = head
		status, body = s.remember(t, s.key, sup)
		assert.Equal(t, http.StatusNotFound, status,
			"the supersede path resolved a work item in another project (body %s)", body)
		assert.Equal(t, before, s.countMemories(t, s.projA))
	})

	// ── 11. WHERE the gate lives, not just that the endpoint refuses. Every arm
	//        above goes through handleRemember, so all of them stay green if the
	//        check is done in that handler — and domain.Remember's other callers
	//        would still write the mismatch. This arm calls the shared domain
	//        entry with no handler in front of it.
	//
	//        ⚠️ It is ALSO the only honest way to state the pf_save_artifact
	//        half, and the reason is worth writing down because it contradicts
	//        the obvious guess. The two MCP memory tools do both reach this one
	//        function (internal/mcp/tools_memory.go, s.client.Remember at :32
	//        and :234) — but they cannot both produce the defect.
	//        pf_remember takes `project` from the caller and requires it
	//        (validatePfRememberArgs, :459), so it can disagree with the work
	//        item; that is the reachable path and every HTTP arm above drives
	//        it. pf_save_artifact sends NO project field at all
	//        (buildSaveArtifactBody, :672-694, which builds the whole body and
	//        never sets one), so it always takes handleRemember's back-fill
	//        branch and its project is DERIVED from the work item — the two
	//        agree by construction and no argument the caller can pass will
	//        separate them. So there is no end-to-end pf_save_artifact
	//        mis-mount to assert; what remains assertable is that the gate sits
	//        at the entry both tools share, which is what this arm does, with
	//        methodology.spec — pf_save_artifact's type — as one of its cases.
	//        (Independently, handleRemember demands an attempt credential for
	//        the target work item before Remember is reached on that type, so
	//        the artifact path is additionally gated earlier and for an
	//        unrelated reason.)
	t.Run("the shared domain entry refuses it with no handler in front", func(t *testing.T) {
		for _, memType := range []string{"experience.pitfall", "methodology.spec"} {
			_, _, err := domain.Remember(context.Background(), s.pool, &domain.RememberRequest{
				Project:       s.projA,
				Type:          memType,
				Content:       fmt.Sprintf("a %s written straight at the domain entry", memType),
				Visibility:    "project",
				WorkItemID:    &wiB.ID,
				DedupMode:     "off",
				CallerUserID:  s.uid,
				CallerDisplay: s.uid,
			})
			require.Error(t, err,
				"domain.Remember accepted project=%s with a %s hung off a work item in %s; a fix that lives in "+
					"handleRemember leaves every other caller of this entry writing the mismatch",
				s.projA, memType, s.projB)
			var aerr *domain.AihubError
			require.ErrorAs(t, err, &aerr)
			assert.Equal(t, domain.ErrNotFound, aerr.Code)
		}
	})

	// ── 12/13. aihub#543 probe wave 2. Two claims from
	//          docs/mcp-cards/pf_remember.md that need this endpoint and a real
	//          database, hosted here rather than as new top-level functions
	//          because internal/citest/dbtestcov ratchets on the NUMBER of
	//          DB-gated functions and a new one costs a manifest line and a
	//          ci.yml step. This function already drives POST /v1/memories,
	//          already reads the 201 body, and already has the project and the
	//          key on hand — so what a third function would buy is a name, and
	//          the name is on the subtest.

	// hop 5: "`id` and `memory_id` are both present, which is why callers in
	// this repo use either."
	//
	// 🔴 Nothing held this before, and the reason is worth stating: K10 is a
	// one-directional ratchet — a LIVE key the card does not declare is red —
	// so a key that stops being sent is invisible to it, and K7 compares two
	// checked-in files to each other. The alias is a compatibility promise
	// (memIDOf in this very file reads `id`; internal/mcp's e2e walk reads
	// `memory_id`), and dropping either one breaks callers without failing a
	// single existing arm.
	//
	// MUTANTS:
	//
	//	M35 enforcement: delete "memory_id" from handleRemember's response map
	//	                                      RED  both_id_aliases (missing key)
	//	M36 enforcement: set memory_id to a different value than id
	//	                                      RED  the equality assertion
	//	M37 enforcement: assert a key the reply does not carry
	//	                                      RED  the `content` control
	//	M38 publication: delete the citation from the card sentence
	//	                                      RED  K12
	t.Run("the reply carries both id aliases and they agree", func(t *testing.T) {
		status, body := s.remember(t, s.key,
			memoryBody(s.projA, "the two id aliases the hop-5 section promises", nil))
		require.Equal(t, http.StatusCreated, status, "body %s", body)

		var reply map[string]any
		require.NoError(t, json.Unmarshal(body, &reply), "body was %q", body)

		id, hasID := reply["id"]
		memID, hasMemID := reply["memory_id"]
		require.True(t, hasID, "the 201 carries no `id` (keys: %v)", sortedReplyKeys(reply))
		require.True(t, hasMemID,
			"the 201 carries no `memory_id` (keys: %v). Both are declared in this card's "+
				"machine block and callers in this repo read either — K10 only fails on a live "+
				"key the card does NOT declare, so a key that stops being sent is invisible to "+
				"it", sortedReplyKeys(reply))
		assert.Equal(t, id, memID,
			"`memory_id` is documented as an alias for `id`; two different values make it a "+
				"second identifier and every caller that picked one is now reading a different row")

		// The control. Without it "the key is present" is satisfied by an
		// assertion helper that reports every lookup as a hit.
		_, hasContent := reply["content"]
		assert.False(t, hasContent,
			"the 201 carries `content` too, so this walk is not reading the projection it "+
				"thinks it is and the two presence assertions above prove nothing (keys: %v)",
			sortedReplyKeys(reply))
	})

	// hop 4: "`dedup_mode` and `supersedes_memory_id` change what happens to an
	// existing similar memory; neither is enumerated."
	//
	// 🔴 The three spellings had NO arm anywhere in the tree — not the
	// CONFLICT_SIMILAR_MEMORY code (which no test produced), not the
	// attrs.similar_to annotation, not the skip. `off` is what every other
	// fixture in this file sends precisely so dedup stays out of the way, which
	// is how a parameter with three behaviours ended up driven in one.
	//
	// The similarity is Jaccard over word sets, so identical content scores 1.0
	// and clears memoryDedupHigh; the dissimilar control clears neither
	// threshold. Both are needed: `strict` refusing everything would satisfy the
	// refusal arm on its own.
	//
	// MUTANTS:
	//
	//	M39 enforcement: drop the `dedup_mode != "off"` guard
	//	                                      RED  off_skips_the_check (a 409)
	//	M40 enforcement: make strict the only mode (annotate branch removed)
	//	                                      RED  suggest_annotates
	//	M41 enforcement: make strict answer 200 instead of the 409
	//	                                      RED  strict_refuses
	//	M42 enforcement: compare with `sim > 0` instead of memoryDedupHigh
	//	                                      RED  strict_stores_a_near_miss.
	//	                                           ⚠️ GREEN against the DISSIMILAR
	//	                                           control alone, measured: that
	//	                                           content scores 0.05 and never
	//	                                           reaches the strict branch at all,
	//	                                           because textDedupCheck returns
	//	                                           nothing below memoryDedupLow. The
	//	                                           near-miss arm below exists because
	//	                                           of that measurement.
	//	M43 publication: delete the citation from the card bullet
	//	                                      RED  K12
	t.Run("dedup_mode decides what happens to a similar memory", func(t *testing.T) {
		const twin = "the retry budget is counted per attempt and never per individual request"

		status, body := s.remember(t, s.key, memoryBody(s.projA, twin, nil))
		require.Equal(t, http.StatusCreated, status, "seeding the dedup twin: %s", body)

		t.Run("off stores a second copy and annotates nothing", func(t *testing.T) {
			b := memoryBody(s.projA, twin, nil) // memoryBody sends dedup_mode=off
			status, body := s.remember(t, s.key, b)
			require.Equal(t, http.StatusCreated, status, "body %s", body)
			assert.Empty(t, s.storedSimilarTo(t, memIDOf(t, body)),
				"dedup_mode=off ran the similarity query anyway; the card's `off` spelling is "+
					"the one that has to cost nothing")
		})

		t.Run("suggest stores it and records what it resembles", func(t *testing.T) {
			b := memoryBody(s.projA, twin, nil)
			b["dedup_mode"] = "suggest"
			status, body := s.remember(t, s.key, b)
			require.Equal(t, http.StatusCreated, status, "body %s", body)
			assert.NotEmpty(t, s.storedSimilarTo(t, memIDOf(t, body)),
				"dedup_mode=suggest wrote no attrs.similar_to, so the mode's whole effect — "+
					"telling a reader this row has a near-twin — is gone while the call still "+
					"answers 201")
		})

		t.Run("strict refuses and names the row it collided with", func(t *testing.T) {
			before := s.countMemories(t, s.projA)
			b := memoryBody(s.projA, twin, nil)
			b["dedup_mode"] = "strict"
			status, body := s.remember(t, s.key, b)
			assert.Equal(t, http.StatusConflict, status,
				"dedup_mode=strict stored a near-identical memory (body %s)", body)
			assert.Contains(t, string(body), "CONFLICT_SIMILAR_MEMORY",
				"the refusal does not carry the code, so a caller cannot tell it from any "+
					"other 409; body %s", body)
			assert.Equal(t, before, s.countMemories(t, s.projA),
				"the write was refused and the row was created anyway")

			// WHICH row it collided with, resolved against the database rather
			// than compared to the id this subtest happens to remember. The
			// dedup query takes the highest-similarity candidate, and by now
			// several rows carry this content — so an equality against one of
			// them would be a fact about subtest order. What the card's claim
			// needs is that the id names a row holding the content that
			// collided.
			var refusal struct {
				Details struct {
					Existing struct {
						ID string `json:"id"`
					} `json:"existing"`
				} `json:"details"`
			}
			require.NoError(t, json.Unmarshal(body, &refusal), "body was %q", body)
			require.NotEmpty(t, refusal.Details.Existing.ID,
				"the refusal names no existing row, so a caller is told no and not which "+
					"memory to read instead; body %s", body)
			assert.Equal(t, twin, s.storedContent(t, refusal.Details.Existing.ID),
				"the refusal points at a row whose content is not the one that collided, so "+
					"details.existing is the wrong row rather than a helpful one; body %s", body)
		})

		t.Run("strict still accepts a genuinely different memory", func(t *testing.T) {
			b := memoryBody(s.projA,
				"certificate rotation reloads the edge proxy rather than restarting it", nil)
			b["dedup_mode"] = "strict"
			status, body := s.remember(t, s.key, b)
			require.Equal(t, http.StatusCreated, status,
				"dedup_mode=strict refused a dissimilar memory, so the arm above is satisfied "+
					"by a mode that refuses everything (body %s)", body)
		})

		// 🔴 The control that matters, and the arm above is NOT it. A dissimilar
		// body scores 0.05 and textDedupCheck returns nothing below
		// memoryDedupLow, so the strict comparison is never reached — measured:
		// replacing `sim >= memoryDedupHigh` with `sim > 0` leaves the arm above
		// green. This body scores ~0.83, above the low threshold and below the
		// high one, which is the "strict-below-high" branch the write path's own
		// comment names and the only place the HIGH threshold is observable.
		t.Run("strict stores a near miss and annotates it instead", func(t *testing.T) {
			b := memoryBody(s.projA,
				"a retry budget is counted per attempt and never per individual request", nil)
			b["dedup_mode"] = "strict"
			status, body := s.remember(t, s.key, b)
			require.Equal(t, http.StatusCreated, status,
				"dedup_mode=strict refused a body that is similar without being a near-duplicate "+
					"(body %s). The threshold is what makes `strict` usable at all: a mode that "+
					"409s on everything above the LOW threshold refuses ordinary revisions.", body)
			assert.NotEmpty(t, s.storedSimilarTo(t, memIDOf(t, body)),
				"a strict write below the high threshold took the annotate branch and wrote no "+
					"attrs.similar_to, so the two thresholds have collapsed into one")
		})
	})
}

// storedContent reads a stored row's content, so a refusal that names a memory
// can be checked against the row it names rather than against an id this test
// happens to be holding.
func (s *rememberScopeStack) storedContent(t *testing.T, memID string) string {
	t.Helper()
	var out string
	require.NoError(t, s.pool.QueryRow(context.Background(),
		`SELECT content FROM memories WHERE id=$1`, memID).Scan(&out))
	return out
}

// storedSimilarTo reads attrs.similar_to off a stored row. Empty when the key is
// absent, which is the state dedup_mode=off is supposed to leave behind.
func (s *rememberScopeStack) storedSimilarTo(t *testing.T, memID string) string {
	t.Helper()
	var out *string
	require.NoError(t, s.pool.QueryRow(context.Background(),
		`SELECT attrs->>'similar_to' FROM memories WHERE id=$1`, memID).Scan(&out))
	if out == nil {
		return ""
	}
	return *out
}

// sortedReplyKeys renders a JSON object's key set for a failure message, so a
// missing key is reported beside what WAS there.
func sortedReplyKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// normalizeRef blanks the caller's own reference out of an error body so two
// refusals can be compared for everything else. The reference is the one
// difference the responses are allowed to have: it came from the caller, so
// echoing it back tells them nothing they did not already type.
func normalizeRef(t *testing.T, body []byte, ref string) string {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(body, &m), "body was %q", body)
	if msg, ok := m["message"].(string); ok {
		m["message"] = strings.ReplaceAll(msg, ref, "<ref>")
	}
	out, err := json.Marshal(m)
	require.NoError(t, err)
	return string(out)
}
