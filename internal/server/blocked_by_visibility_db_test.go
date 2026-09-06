package server

// DB-gated acceptance test for aihub#357 H1: `blocked_by` must not turn the
// enumerable `<project>#<seq>` identifier into an existence oracle for projects
// the caller cannot see.
//
// 🔴 Read the distinction before changing anything here, because it decides what
// this test is allowed to assert.
//
// The AUTHORIZATION gap is PRE-EXISTING and is NOT what this test closes. On the
// base commit a caller with no role at all on project B could already name one of
// B's work items by its canonical `wi_...` id in `blocked_by` and get a real edge
// into B. That is untouched here — it needs the same viewer+ policy discussion
// `CreateDependency` already had, and widening this work item to settle it would
// be scope creep.
//
// What aihub#357 ADDED, and what this test closes, is that the identifier became
// GUESSABLE. Once `blocked_by` accepts a slug, `<project>#<seq>` is a two-token
// namespace anyone can walk, and the response told the walker which guesses were
// real. Measured on the branch head 0096962 with a caller holding maintainer on A
// and NO role on B:
//
//	blocked_by:["<B>#2"]           -> 201 Created, and a real edge into B
//	blocked_by:["<B>#9999"]        -> 404 "…which does not exist"
//	GET /v1/work_items/<B's wi id> -> 403
//
// 201-vs-404 is a clean one-bit answer to "does <B>#n exist", from a caller who
// is 403'd on every honest read of B. Sweeping n enumerates B's size.
//
// ⇒ The property under test is INDISTINGUISHABILITY, not refusal. "Invisible"
// and "absent" must produce the same status, the same error code and the same
// message template. A fix that answered 403 for the invisible case would keep
// the oracle intact while looking like a security fix.
//
// ✅ The divergence this paragraph used to describe is GONE (aihub#377). It read:
//
//	⚠️ This deliberately DIVERGES from `CreateDependency`, which answers 403 for
//	the cross-project case and names the project in the message
//	(internal/domain/dependencies.go). That is itself an existence leak, but it is
//	reachable only with a canonical id, which is not guessable, and changing it is
//	outside this work item.
//
// Both endpoints now answer the shared 404. Two notes for whoever reads the old
// text in the history:
//
//   - Its factual claim was WRONG, not just narrow. "Reachable only with a
//     canonical id, which is not guessable" — but handleCreateDependency resolves
//     its ends through domain.GetWorkItem, whose clause is `id = $1 OR slug = $1`.
//     A slug worked there exactly as it works here, so the leak it excused as
//     unenumerable was enumerable by the same <project>#<seq> walk this file
//     exists to close. The deferral was reasonable; the reason given for it was
//     never measured.
//   - The scoping conclusion still stands: this test still asserts
//     INDISTINGUISHABILITY, and a fix that answered 403 for the invisible case
//     would still keep the oracle while looking like a security fix.
//
// The three positive arms are not decoration. A fix that simply refused every
// cross-project blocked_by would satisfy every negative assertion above while
// removing a documented capability, so viewer-on-B, admin, and the ordinary
// same-project case are all asserted to still succeed.
//
// Everything here goes through HTTP, including the fixture seeding, so this file
// compiles unchanged against the pre-fix tree. A test that can only be BUILT
// after the fix cannot be shown red before it; the build failure would be
// indistinguishable from a broken test.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/server/ -run TestBlockedBySlugCannotProbeInvisibleProjects -v -count=1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/auth"
	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
)

// serverTestPool connects to AIHUB_TEST_DB, skipping the test if unset.
// Shared with dependencies_slug_db_test.go.
func serverTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("AIHUB_TEST_DB")
	if dbURL == "" {
		t.Skip("set AIHUB_TEST_DB to run this integration test")
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// resetProjectWorkItems clears a test project's work items. Project names here
// derive from t.Name(), so they are the same string on every run against the
// same database: without this, the previous run's rows decide this run's seq
// numbers and therefore its slugs. The order satisfies the foreign keys that do
// NOT cascade from work_items; wi_dependencies, wi_step_state and wi_watches do
// cascade and are left to the database.
func resetProjectWorkItems(t *testing.T, pool *pgxpool.Pool, project string) {
	t.Helper()
	for _, stmt := range []string{
		`DELETE FROM wi_step_completions WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM agent_events WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`UPDATE work_items SET current_attempt_id=NULL WHERE project=$1`,
		`DELETE FROM run_attempts WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM work_items WHERE project=$1`,
	} {
		_, err := pool.Exec(context.Background(), stmt, project)
		require.NoError(t, err, "reset failed on %q", stmt)
	}
}

// visStack is two projects and three callers with deliberately different sight
// of the second one.
type visStack struct {
	url  string
	pool *pgxpool.Pool
	// projA is where every created work item lands; every caller can write it.
	projA string
	// projB holds the work item being probed for. outsiderKey has NO role here.
	projB string

	outsiderKey string // non-admin; maintainer on A, nothing on B
	viewerKey   string // non-admin; writer on A, viewer on B
	adminKey    string // global admin
}

func newVisStack(t *testing.T) *visStack {
	t.Helper()
	pool := serverTestPool(t)
	ctx := context.Background()

	// Sanitize returns at most 37 characters precisely so a 2-character prefix
	// fits projects.name's CHECK (^[a-z][a-z0-9_-]{0,39}$). So the a/b
	// discriminator goes INTO the name being sanitized, not onto the result —
	// appending it afterwards overflows by one character, and the two names stay
	// distinct because they hash differently.
	projA := "p_" + testname.Sanitize(t.Name()+"a")
	projB := "p_" + testname.Sanitize(t.Name()+"b")
	base := testname.Sanitize(t.Name())
	outsider, viewer, admin := "u_"+base+"o", "u_"+base+"v", "u_"+base+"a"
	// Keys are derived from the USER IDs, not from `base`, so the two can never
	// drift apart. They did during development: a rename left stale users behind
	// holding the same key_hash as the new ones, the middleware resolved the
	// bearer token to the stale user, and every arm 403'd on a project whose
	// members list was correct.
	outsiderKey := "pfk_" + outsider
	viewerKey := "pfk_" + viewer
	adminKey := "pfk_" + admin

	seedUser := func(uid, role, key, keyID string) {
		keys, err := json.Marshal([]map[string]any{{"id": keyID, "key_hash": auth.HashKey(key)}})
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `
			INSERT INTO users(id,email,display_name,user_type,role,api_keys)
			VALUES($1,$1||'@test.local',$1,'human',$2,$3)
			ON CONFLICT (id) DO UPDATE SET api_keys=EXCLUDED.api_keys, role=EXCLUDED.role`,
			uid, role, keys)
		require.NoError(t, err)
	}
	// users.role is CHECKed to (writer|admin); "writer" is the non-admin value.
	seedUser(outsider, "writer", outsiderKey, "k_out")
	seedUser(viewer, "writer", viewerKey, "k_view")
	seedUser(admin, "admin", adminKey, "k_adm")

	// A: outsider is maintainer, viewer is writer. B: viewer is viewer, and the
	// outsider is absent — that absence is the whole fixture.
	membersA, err := json.Marshal([]map[string]any{
		{"user_id": outsider, "role": "maintainer"},
		{"user_id": viewer, "role": "writer"},
	})
	require.NoError(t, err)
	membersB, err := json.Marshal([]map[string]any{
		{"user_id": viewer, "role": "viewer"},
	})
	require.NoError(t, err)

	for _, p := range []struct {
		name    string
		members []byte
	}{{projA, membersA}, {projB, membersB}} {
		_, err = pool.Exec(ctx,
			`INSERT INTO projects(name,owner_user_id,members) VALUES($1,$2,$3)
			 ON CONFLICT (name) DO UPDATE SET members=EXCLUDED.members, owner_user_id=EXCLUDED.owner_user_id`,
			p.name, admin, p.members)
		require.NoError(t, err)
	}
	for _, p := range []string{projA, projB} {
		resetProjectWorkItems(t, pool, p)
	}

	ts := httptest.NewServer(NewRouter(pool, []byte("blocked-by-visibility-test-cookie-secret")))
	t.Cleanup(ts.Close)
	return &visStack{
		url: ts.URL, pool: pool, projA: projA, projB: projB,
		outsiderKey: outsiderKey, viewerKey: viewerKey, adminKey: adminKey,
	}
}

// createWI issues POST /v1/work_items as the given key.
func (s *visStack) createWI(t *testing.T, key, body string) (int, map[string]any) {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, s.url+"/v1/work_items", strings.NewReader(body))
	require.NoError(t, err)
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	var decoded map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&decoded))
	return resp.StatusCode, decoded
}

// seedWIAsAdmin creates a work item over HTTP as the admin and returns its
// canonical id and slug. Over HTTP rather than through domain.CreateWorkItem so
// this file's build does not depend on that function's signature.
func (s *visStack) seedWIAsAdmin(t *testing.T, project, goal string) (id, slug string) {
	t.Helper()
	status, body := s.createWI(t, s.adminKey,
		fmt.Sprintf(`{"project":%q,"goal":%q,"source":"human"}`, project, goal))
	require.Equal(t, http.StatusCreated, status, "seeding %q failed: %v", goal, body)
	id, _ = body["id"].(string)
	slug, _ = body["slug"].(string)
	require.NotEmpty(t, id)
	require.NotEmpty(t, slug, "the fixture needs a slug to exercise slug resolution")
	return id, slug
}

// edgesInto counts wi_dependencies rows pointing at a blocking work item, from
// anywhere. The probe's damage is not only the answer it gets: on the branch
// head it also LEAVES a real edge inside the project it cannot see.
func (s *visStack) edgesInto(t *testing.T, blockingWIID string) int {
	t.Helper()
	var n int
	require.NoError(t, s.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM wi_dependencies WHERE blocking_wi_id=$1`, blockingWIID).Scan(&n))
	return n
}

func TestBlockedBySlugCannotProbeInvisibleProjects(t *testing.T) {
	s := newVisStack(t)

	// The secret: one work item inside B, whose existence the outsider must not
	// be able to confirm.
	secretID, secretSlug := s.seedWIAsAdmin(t, s.projB,
		"decommission the legacy billing reconciliation cron")

	// Fixture check: the outsider really is blind to B by every honest route.
	// Without this the negative arms could pass because the fixture forgot to
	// withhold access, which would make the whole test vacuous.
	//
	// 🔴 Expected status changed 403 -> 404 on 2026-09-06 (aihub#377), and the
	// subtest was renamed with it: the old name said `_403_` while the assertion
	// said 404, and a test whose name states one contract and whose body checks
	// another is the rot this repo keeps digging out of its own comments.
	//
	// CONTRACT CHANGE, not a red test tuned green. The two are indistinguishable
	// in a diff, so the check is aihub#377's first invariant, verbatim:
	//
	//	在某个 project 里的用户，能看到该 project 的一切（memory、work item、
	//	artifact、event、step、依赖）；不在的，对该 project 的一切必须拿到与
	//	「不存在」逐字节相同的响应。
	//
	//	(A user who is in a project can see everything about it. A user who is
	//	not must get a response byte-identical to the one for something that
	//	does not exist.)
	//
	// The outsider holds no role on projB, so a refusal that says "forbidden"
	// confirms projB exists — the disclosure this whole file exists to close.
	//
	// 🔴 STILL DISCRIMINATING. If the outsider could in fact read projB this arm
	// answers 200 with the entire work item, including the goal, so it goes red
	// exactly as before. The rename and the restatus removed nothing: 404 is now
	// the POSITIVE evidence that projB is hidden, where it used to be 403.
	//
	// ⚠️ The name is also a CI contract: ci.yml greps
	// `TestBlockedBySlugCannotProbeInvisibleProjects/<subtest>` per arm. Renaming
	// here without updating that list makes the step report "did not run", which
	// reads as missing coverage rather than a rename. Both moved together.
	t.Run("fixture_outsider_cannot_read_B_by_any_honest_route", func(t *testing.T) {
		r, err := http.NewRequest(http.MethodGet, s.url+"/v1/work_items/"+secretID, nil)
		require.NoError(t, err)
		r.Header.Set("Authorization", "Bearer "+s.outsiderKey)
		resp, err := http.DefaultClient.Do(r)
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck
		require.Equal(t, http.StatusNotFound, resp.StatusCode,
			"fixture is broken: got %d, want 404. The outsider must be unable to read B "+
				"and must be told only that it is not there. A 200 means the fixture "+
				"granted access and nothing below is about a hidden project; a 403 means "+
				"the refusal still confirms B exists, which is the oracle aihub#377 closed",
			resp.StatusCode)
	})

	// ── THE ORACLE. Red on branch head 0096962. ───────────────────────────────
	var hitStatus, missStatus int
	var hitBody, missBody map[string]any

	t.Run("probing_an_invisible_project_by_slug_is_not_a_create", func(t *testing.T) {
		before := s.edgesInto(t, secretID)

		hitStatus, hitBody = s.createWI(t, s.outsiderKey, fmt.Sprintf(
			`{"project":%q,"goal":"catalogue every orphaned snapshot volume in eu-west","blocked_by":[%q]}`,
			s.projA, secretSlug))

		assert.Equal(t, http.StatusNotFound, hitStatus,
			"naming a work item in a project the caller cannot see must not succeed; got %d %v",
			hitStatus, hitBody)
		assert.Equal(t, before, s.edgesInto(t, secretID),
			"the probe left a real dependency edge inside a project the caller is 403'd on")
	})

	t.Run("an_invisible_hit_is_indistinguishable_from_a_genuine_miss", func(t *testing.T) {
		missRef := s.projB + "#9999" // same namespace, certainly absent
		missStatus, missBody = s.createWI(t, s.outsiderKey, fmt.Sprintf(
			`{"project":%q,"goal":"rewrite the shift handover digest as a daily email","blocked_by":[%q]}`,
			s.projA, missRef))
		require.Equal(t, http.StatusNotFound, missStatus,
			"control: a reference that names nothing must be 404; got %v", missBody)

		assert.Equal(t, missStatus, hitStatus,
			"an existing-but-invisible work item and an absent one answered with different HTTP statuses "+
				"(%d vs %d) — that one bit enumerates the hidden project", hitStatus, missStatus)
		assert.Equal(t, missBody["code"], hitBody["code"],
			"the two cases answered with different error codes (%v vs %v), which is the same oracle "+
				"wearing a different hat", hitBody["code"], missBody["code"])

		// The message may — and should — echo the caller's own input, which is
		// not a leak. What must not differ is the template around it.
		hitMsg := strings.Replace(fmt.Sprint(hitBody["message"]), secretSlug, "<REF>", 1)
		missMsg := strings.Replace(fmt.Sprint(missBody["message"]), missRef, "<REF>", 1)
		assert.Equal(t, missMsg, hitMsg,
			"with the caller's own reference masked out the two messages still differ, so the wording "+
				"itself reports whether the work item exists")
	})

	// ── POSITIVE CONTROLS. Green before AND after; they catch over-blocking. ──

	t.Run("a_viewer_on_the_other_project_may_still_block_on_it", func(t *testing.T) {
		status, body := s.createWI(t, s.viewerKey, fmt.Sprintf(
			`{"project":%q,"goal":"pin the ingest worker image to a digest instead of a tag","blocked_by":[%q]}`,
			s.projA, secretSlug))
		require.Equal(t, http.StatusCreated, status,
			"viewer+ on the blocking project is the policy CreateDependency already applies to a "+
				"cross-project dependency; refusing it here would delete a capability rather than close a leak "+
				"(body: %v)", body)
		assert.Equal(t, "blocked", body["status"])
	})

	t.Run("an_admin_may_still_block_on_any_project", func(t *testing.T) {
		status, body := s.createWI(t, s.adminKey, fmt.Sprintf(
			`{"project":%q,"goal":"migrate the audit trail exporter off the deprecated bucket","blocked_by":[%q]}`,
			s.projA, secretSlug))
		require.Equal(t, http.StatusCreated, status,
			"an admin bypasses project checks everywhere else in this codebase (body: %v)", body)
		assert.Equal(t, "blocked", body["status"])
	})

	t.Run("the_ordinary_same_project_slug_still_resolves", func(t *testing.T) {
		blockerID, blockerSlug := s.seedWIAsAdmin(t, s.projA,
			"retire the nightly full-table vacuum in favour of autovacuum tuning")

		status, body := s.createWI(t, s.outsiderKey, fmt.Sprintf(
			`{"project":%q,"goal":"expose queue depth on the operator dashboard","blocked_by":[%q]}`,
			s.projA, blockerSlug))
		require.Equal(t, http.StatusCreated, status,
			"the common case — a slug naming a wi in the caller's own project — must be untouched (body: %v)", body)
		assert.Equal(t, "blocked", body["status"])
		assert.Equal(t, 1, s.edgesInto(t, blockerID))
	})
}
