package server

// DB-gated acceptance test for aihub#363: pf_recall's `work_item_id` filter
// answered a slug with an empty page.
//
// 🔴 The shape of the defect. handleRecall takes `work_item_id` straight off the
// query string (routes_memory.go) and domain.recallText binds it into
// `AND work_item_id = $N`. That column FK-references work_items(id) —
// memories_work_item_id_fkey, checked against the migrated schema, not read off
// the DDL — so it only ever holds a canonical `wi_...`. A slug therefore matches
// nothing and the endpoint answers 200 with an empty list, which is exactly what
// a work item with no memories also answers. Measured on production before the
// fix, same work item, two spellings:
//
//	pf_recall(project="aihub", work_item_id="aihub#357")   -> {"items":null,"total":0}
//	pf_recall(project="aihub", work_item_id="wi_uqiiQBZ4") -> 3 items
//
// It is the fourth outing of the class aihub#127 opened on the write side,
// aihub#343 carried to the read side for GET /v1/events, and aihub#357 closed on
// the four dependency call sites. It matters here because pf-work's SKILL.md
// asks for `pf_recall(project=…, work_item_id=<wi_id>)` on EVERY claim path to
// pick up the previous agent's handover notes, and `/pf-work aihub#361` hands
// out a slug — so an agent taking over got zero context and no signal that
// anything was missing.
//
// 🔴 The other half of the acceptance, and the reason for six of these nine
// arms: resolving a slug must not become a WIDER answer. aihub#357's review
// caught precisely that on `blocked_by` — accepting slugs turned a dormant
// authorization gap into an enumerable oracle, because `<project>#<seq>` is
// guessable where `wi_...` is not. Here the invariant is stated as: resolution
// is not an authorization decision. It maps one reference onto the canonical id
// the column holds, and the answer stays whatever `project=` + the visibility
// predicate already allowed. So "no such work item" and "a work item you cannot
// see" produce the same page as each other AND the same page a nonexistent
// canonical id produced before this change — asserted byte-for-byte, not merely
// "both empty".
//
// Real router, real auth middleware, real echo binder, real Postgres: the
// parameter starts life in the handler and the FK that makes the empty page
// unavoidable lives in the database. A domain test with a canonical id in hand
// can see neither end.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/server/ -run TestRecallResolvesWorkItemIdOrSlug -v -count=1
//
// One test FUNCTION with subtests, per aihub#334: internal/citest/dbtestcov
// ratchets on the number of DB-gated functions, and the per-arm coverage claim
// lives in the CI step's `--- PASS:` greps.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/auth"
	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// recallSlugStack is two projects and one caller who can read exactly one of
// them. projB's invisibility is the fixture, the same way it is in
// blocked_by_visibility_db_test.go.
type recallSlugStack struct {
	url   string
	pool  *pgxpool.Pool
	projA string
	projB string
	// uid/key belong to the reader: writer on A, NO role on B, not an admin.
	uid string
	key string
	// bUID owns projB and seeds its rows; it never issues a request.
	bUID string
}

func newRecallSlugStack(t *testing.T) *recallSlugStack {
	t.Helper()
	pool := serverTestPool(t)
	ctx := context.Background()

	// Sanitize caps at 37 chars so a 2-char prefix fits projects.name's CHECK;
	// the a/b discriminator goes INTO the sanitized string rather than onto the
	// result, which would overflow by one (same note as newVisStack).
	projA := "p_" + testname.Sanitize(t.Name()+"a")
	projB := "p_" + testname.Sanitize(t.Name()+"b")
	base := testname.Sanitize(t.Name())
	uid, bUID := "u_"+base+"r", "u_"+base+"o"
	key := "pfk_" + uid

	seedUser := func(id, role string, apiKey, keyID string) {
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
	// users.role is CHECKed to (writer|admin). "writer" is the non-admin value,
	// and non-admin is load-bearing: an admin sees every project and the
	// invisibility arms would pass vacuously.
	seedUser(uid, "writer", key, "k_recallslug")
	seedUser(bUID, "writer", "", "")

	membersA, err := json.Marshal([]map[string]any{{"user_id": uid, "role": "writer"}})
	require.NoError(t, err)

	for _, p := range []struct {
		name    string
		owner   string
		members []byte
	}{
		{projA, bUID, membersA},
		// projB has no members entry for uid and is owned by someone else. That
		// absence is the fixture.
		{projB, bUID, []byte(`[]`)},
	} {
		_, err := pool.Exec(ctx, `
			INSERT INTO projects(name,owner_user_id,members) VALUES($1,$2,$3)
			ON CONFLICT (name) DO UPDATE SET owner_user_id=EXCLUDED.owner_user_id, members=EXCLUDED.members`,
			p.name, p.owner, p.members)
		require.NoError(t, err, "seeding project %s", p.name)
	}

	// Project names derive from t.Name(), so they are the same strings on every
	// run against the same database: without this, the previous run's rows
	// decide this run's seq numbers, and therefore its slugs. Memories go first
	// — memories.work_item_id has no ON DELETE CASCADE, so a leftover memory
	// would make the work-item delete fail rather than reset anything.
	for _, p := range []string{projA, projB} {
		_, err := pool.Exec(ctx,
			`UPDATE memories SET latest_id=NULL, supersedes_id=NULL WHERE project=$1`, p)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `DELETE FROM memories WHERE project=$1`, p)
		require.NoError(t, err)
		resetProjectWorkItems(t, pool, p)
	}

	ts := httptest.NewServer(NewRouter(pool, []byte("recall-slug-test-cookie-secret")))
	t.Cleanup(ts.Close)
	return &recallSlugStack{url: ts.URL, pool: pool, projA: projA, projB: projB, uid: uid, key: key, bUID: bUID}
}

// seedWI creates a work item through the real domain path so it gets a real
// slug. Goals stay mutually dissimilar: CreateWorkItem runs goal-similarity
// dedup inside a project and would reject a close match before any handler.
func (s *recallSlugStack) seedWI(t *testing.T, project, goal string) *domain.WorkItem {
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

// seedMemory attaches one memory to a work item IN ITS OWN PROJECT, through
// domain.Remember rather than an INSERT, so the row is written by the same code
// that writes production rows — including its own id-or-slug resolution of this
// very column.
func (s *recallSlugStack) seedMemory(t *testing.T, project, wiID, content string) string {
	t.Helper()
	m, _, err := domain.Remember(context.Background(), s.pool, &domain.RememberRequest{
		Project:       project,
		Type:          "experience.pitfall",
		Content:       content,
		Visibility:    "project",
		WorkItemID:    &wiID,
		DedupMode:     "off",
		CallerUserID:  s.bUID,
		CallerDisplay: s.bUID,
	})
	require.NoError(t, err, "seeding memory %q", content)
	return m.ID
}

// seedLegacyCrossProjectMemory writes a memory whose project differs from its
// work item's, by direct INSERT.
//
// 🔴 It has to bypass domain.Remember, and the reason is the point of arms 8/9
// rather than an inconvenience. When this suite was written, Remember validated
// project and work_item_id against each other in neither direction, so
// seedMemory could produce this row and did. aihub#371 closed that: the
// resolving query is now scoped to the request's project, and this row can no
// longer be CREATED through any caller-facing path.
//
// The rows themselves did not go away. aihub#371 deliberately migrated nothing
// — how many exist was never measured, and their disposition is the owner's
// call — and UpdateMemory still re-remembers such a row unchanged, so one can
// still acquire a new id after that gate landed. So the shape stays real, and
// the read side still has to answer for it correctly, which is exactly what
// arms 8/9 assert. Writing it as an INSERT states what it now is: legacy data,
// not something the write path will hand you.
//
// If this ever needs to become reachable through Remember again, that is a
// decision about aihub#371's gate and belongs there — do not "fix" it here by
// widening the fixture's project.
func (s *recallSlugStack) seedLegacyCrossProjectMemory(t *testing.T, project, wiID, content string) string {
	t.Helper()
	id := "mem_" + testname.Sanitize(t.Name()+"legacy")
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO memories (id, project, type, content, visibility, work_item_id,
		                      author_user_id, author_display, latest_id)
		VALUES ($1,$2,'experience.pitfall',$3,'project',$4,$5,$5,$1)`,
		id, project, content, wiID, s.bUID)
	require.NoError(t, err, "seeding legacy cross-project memory %q", content)

	// The fixture is only worth anything if the row really is mismatched;
	// a silently-corrected INSERT would make arms 8/9 vacuous.
	var memProject, wiProject string
	require.NoError(t, s.pool.QueryRow(context.Background(), `
		SELECT m.project, w.project FROM memories m
		  JOIN work_items w ON w.id = m.work_item_id
		 WHERE m.id = $1`, id).Scan(&memProject, &wiProject))
	require.NotEqual(t, wiProject, memProject,
		"the legacy fixture is supposed to be a project mismatch; it is not, so arms 8/9 prove nothing")
	return id
}

// recallPage is the decoded GET /v1/memories body, narrowed to what these arms
// assert on.
type recallPage struct {
	Items []struct {
		ID         string  `json:"id"`
		WorkItemID *string `json:"work_item_id"`
	} `json:"items"`
	Total int `json:"total"`
}

// recall issues one authenticated recall. ref is passed through url.Values so
// the '#' in a slug is percent-encoded rather than starting a fragment.
func (s *recallSlugStack) recall(t *testing.T, project, ref string) (int, []byte) {
	t.Helper()
	q := url.Values{"project": {project}}
	if ref != "" {
		q.Set("work_item_id", ref)
	}
	r, err := http.NewRequest(http.MethodGet, s.url+"/v1/memories?"+q.Encode(), nil)
	require.NoError(t, err)
	r.Header.Set("Authorization", "Bearer "+s.key)
	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, raw
}

// recallIDs is recall plus a 200 assertion and the memory ids, which is what
// every positive arm compares.
func (s *recallSlugStack) recallIDs(t *testing.T, project, ref string) []string {
	t.Helper()
	status, raw := s.recall(t, project, ref)
	require.Equal(t, http.StatusOK, status, "recall project=%s work_item_id=%q: %s", project, ref, raw)
	var page recallPage
	require.NoError(t, json.Unmarshal(raw, &page), "body was %q", raw)
	require.Equal(t, len(page.Items), page.Total,
		"total must count the same filtered set the page returns (body %q)", raw)
	out := make([]string, 0, len(page.Items))
	for _, it := range page.Items {
		out = append(out, it.ID)
	}
	return out
}

func TestRecallResolvesWorkItemIdOrSlug(t *testing.T) {
	s := newRecallSlugStack(t)

	wiA1 := s.seedWI(t, s.projA, "rotate expired TLS certificates on the edge proxies")
	wiA2 := s.seedWI(t, s.projA, "backfill missing avatar thumbnails for legacy users")
	// wiB1 lives in the invisible project and has memories only there. It is the
	// oracle probe.
	wiB1 := s.seedWI(t, s.projB, "archive stale feature flags older than one year")
	// wiB2 also lives in the invisible project, but the memory pointing at it
	// sits in projA. It is how the "resolution is not authorization" arm is
	// stated without inventing an access the canonical id did not already have.
	wiB2 := s.seedWI(t, s.projB, "shard the audit log by tenant before the next migration")

	memA1a := s.seedMemory(t, s.projA, wiA1.ID, "the retry budget is per attempt, not per request")
	memA1b := s.seedMemory(t, s.projA, wiA1.ID, "certificate rotation needs the proxy reloaded, not restarted")
	memA2 := s.seedMemory(t, s.projA, wiA2.ID, "thumbnail backfill must skip rows whose source blob is gone")
	memB1 := s.seedMemory(t, s.projB, wiB1.ID, "flag archival is irreversible once the audit window closes")
	// projA's memory hanging off projB's work item. Since aihub#371 this can no
	// longer be written through domain.Remember, so it is seeded as what it now
	// is — a legacy row — and the helper asserts the mismatch really survived.
	memAonB2 := s.seedLegacyCrossProjectMemory(t, s.projA, wiB2.ID, "the tenant sharding key is chosen in project A's config")

	require.NotEmpty(t, memB1, "fixture: the invisible project needs a memory to probe for")

	// ── 1. The reference side. Green before the fix; it is what arm 2 is
	//        compared against, so a broken fixture shows up here first.
	t.Run("control by canonical id returns the memories of that work item", func(t *testing.T) {
		assert.ElementsMatch(t, []string{memA1a, memA1b}, s.recallIDs(t, s.projA, wiA1.ID))
	})

	// ── 2. The reporter's exact call. RED before the fix: 200 with an empty
	//        list. Comparing the two spellings rather than asserting "two items"
	//        is what makes the failure message say the right thing.
	t.Run("by slug returns the same page as by id", func(t *testing.T) {
		byID := s.recallIDs(t, s.projA, wiA1.ID)
		bySlug := s.recallIDs(t, s.projA, wiA1.Slug)
		assert.ElementsMatch(t, byID, bySlug,
			"work_item_id=%q returned %v but work_item_id=%q returned %v — one spelling of one work item",
			wiA1.ID, byID, wiA1.Slug, bySlug)
	})

	// ── 3. Resolving must not become "no filter at all". A slug names ONE work
	//        item, and the other one's memory must stay out of the page.
	t.Run("a slug still filters to that one work item", func(t *testing.T) {
		got := s.recallIDs(t, s.projA, wiA2.Slug)
		assert.ElementsMatch(t, []string{memA2}, got)
		assert.NotContains(t, got, memA1a, "wiA1's memories must not appear under wiA2's slug")
	})

	// ── 4/5/6. The three ways a reference can name nothing the caller may see.
	//        All three must answer identically — same status, same body — so
	//        that no arrangement of them tells a caller which case they hit.
	//        `absent` is the reference response, and it is the one this endpoint
	//        already gave for an unknown canonical id BEFORE this change, so the
	//        fix adds no new signal of any kind.
	absentStatus, absentBody := s.recall(t, s.projA, "wi_thisidwasnevermintedanywhere")
	t.Run("a nonexistent canonical id is still an empty page", func(t *testing.T) {
		require.Equal(t, http.StatusOK, absentStatus, "body %s", absentBody)
		var page recallPage
		require.NoError(t, json.Unmarshal(absentBody, &page))
		assert.Empty(t, page.Items)
		assert.Equal(t, 0, page.Total)
	})

	t.Run("a nonexistent slug answers exactly what a nonexistent id does", func(t *testing.T) {
		// A slug in a project that does exist, with a seq nothing will ever
		// reach — the "wrong number, right namespace" probe.
		status, body := s.recall(t, s.projA, fmt.Sprintf("%s#999999", s.projA))
		assert.Equal(t, absentStatus, status)
		assert.JSONEq(t, string(absentBody), string(body),
			"a nonexistent slug must be indistinguishable from a nonexistent id")
	})

	t.Run("an invisible slug answers exactly what a nonexistent one does", func(t *testing.T) {
		status, body := s.recall(t, s.projA, wiB1.Slug)
		assert.Equal(t, absentStatus, status,
			"%s exists and holds a memory; answering differently from a miss makes <project>#<seq> enumerable", wiB1.Slug)
		assert.JSONEq(t, string(absentBody), string(body),
			"the invisible work item's memory (%s) or its existence leaked through work_item_id=%s", memB1, wiB1.Slug)
	})

	// ── 7. The fixture control for arm 6. Without it, arm 6 passes just as well
	//        against a caller who can read projB perfectly well and simply has
	//        no memories there.
	t.Run("the caller really cannot read the other project", func(t *testing.T) {
		status, body := s.recall(t, s.projB, "")
		assert.Equal(t, http.StatusForbidden, status,
			"fixture is broken: the reader is supposed to have NO role on %s (body %s)", s.projB, body)
	})

	// ── 8/9. Resolution is not an authorization decision, in both directions.
	//        A slug must reach exactly the rows its canonical id reaches — no
	//        more (arm 6) and no fewer. The narrower "resolve only within
	//        project=" reading would pass every arm above and silently drop this
	//        memory, which is a fresh instance of the same silent-empty class.
	//        Nothing is disclosed: the row is projA's, and this caller can
	//        already read it with no work_item_id filter at all.
	t.Run("a cross-project canonical id still reaches the memory it always did", func(t *testing.T) {
		assert.ElementsMatch(t, []string{memAonB2}, s.recallIDs(t, s.projA, wiB2.ID))
	})

	t.Run("the slug of that work item reaches the same memory as its id", func(t *testing.T) {
		byID := s.recallIDs(t, s.projA, wiB2.ID)
		bySlug := s.recallIDs(t, s.projA, wiB2.Slug)
		assert.ElementsMatch(t, byID, bySlug,
			"work_item_id=%q returned %v but work_item_id=%q returned %v", wiB2.ID, byID, wiB2.Slug, bySlug)
	})

	// ── 10. WHERE the resolution lives, not just that the endpoint answers.
	//        Every arm above goes through handleRecall, so all of them stay
	//        green if the resolution is done in that handler — and the two OTHER
	//        builders of a RecallRequest (the /ui/memories page and the wi
	//        detail page's artifact links) would still be handed a slug and
	//        still answer empty. This arm calls the shared domain entry with no
	//        handler in front of it, which is the only assertion that can tell
	//        those two fixes apart.
	t.Run("the shared domain entry resolves the slug itself", func(t *testing.T) {
		slug := wiA1.Slug
		resp, err := domain.Recall(context.Background(), s.pool, &domain.RecallRequest{
			Project:      s.projA,
			WorkItemID:   &slug,
			CallerUserID: s.uid,
			CallerRole:   "writer",
		})
		require.NoError(t, err)
		got := make([]string, 0, len(resp.Items))
		for _, m := range resp.Items {
			got = append(got, m.ID)
		}
		assert.ElementsMatch(t, []string{memA1a, memA1b}, got,
			"domain.Recall answered %v for slug %q; a fix that lives in handleRecall leaves /ui/memories and the wi detail page broken",
			got, slug)
		assert.Equal(t, len(resp.Items), resp.Total)
	})
}
