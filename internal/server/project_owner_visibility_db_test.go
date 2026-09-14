package server

// aihub#668 — a project's OWNER must be able to see their own project.
//
// 🔴 WHAT THE WORK ITEM ASKED, AND WHAT THE MEASUREMENT ANSWERED. The work item
// was filed as an aihub#665 follow-up: "the new canSeeProject gate 404s a
// non-admin project owner who is absent from projects.members". It asserted two
// things about the surrounding code, and this file exists because one of them is
// false and the other is narrower than it sounds.
//
//   - "checkProjectAccess behaves this way repo-wide, so this is not newly
//     introduced." HALF TRUE, and the half that is false is the load-bearing
//     one. There are TWO functions with that name. domain.checkProjectAccess
//     (internal/domain/projects.go) is the documented 5-level chain and its
//     LEVEL 2 is `p.OwnerUserID == caller.ID → pass, all permissions` — an owner
//     allow branch that has always been there. server.checkProjectAccess
//     (middleware.go) is a different function over UserContext.ProjectRoles, and
//     THAT map is derived from projects.members alone. So the repo does not have
//     one口径; it has two that disagree about exactly one person.
//
//   - "no allow branch covers this person." True of the map-based family, false
//     of the DB-backed one. The owner is covered by domain level 2 and by
//     nothing that reads ProjectRoles.
//
// ⇒ The defect is not in the gate aihub#665 added. It is that the gate reads a
// map which has never carried projects.owner_user_id, and neither has any other
// consumer of that map. predict is simply the first call every agent makes, so
// it is where the omission became visible.
//
// 🔴 THE POPULATION IS ZERO TODAY AND THAT IS NOT A REASON TO SKIP THE FIX. The
// work item said to count the affected users first and downgrade to a doc note
// if the count was zero, on the theory that zero means "unreachable in the
// data". Counted through the admin read path on 2026-09-14 (GET /v1/projects as
// an admin is an unfiltered `SELECT … FROM projects`, and GET /v1/users returned
// 24 rows against a 100-row cap, so both censuses are complete): 11 projects, 24
// users, exactly one admin. Ten projects are owned by that admin; the eleventh
// is owned by a non-admin who IS in its members. Zero affected users.
//
// But zero is a fact about who has called pf_create_project, not about what the
// code permits:
//
//   - POST /v1/projects carries NO admin guard (routes_projects.go registers it
//     on the plain v1 group), so any of the 23 non-admin users can create one.
//   - domain.CreateProject's INSERT names (name, description, visible, repos,
//     scenario, owner_user_id) and NOT members, so the new row takes the column
//     default — `members JSONB NOT NULL DEFAULT '[]'` (migration 0012).
//
// So a non-admin's very next pf_create_project produces a project whose owner is
// absent from its members, and that owner is then refused every /v1 route in
// their own project. Not only predict: handleGetWorkItem calls
// server.checkProjectAccess too. The combination is one unprivileged API call
// away, which is why this lands as a fix rather than a comment.
//
// 🔴 WHY THE DENY ARMS OUTNUMBER THE ALLOW ARM. aihub#665 closed a demonstrated
// cross-project disclosure and spent 20 mutants doing it. The risk direction
// here is the exact reverse of that work item's: a lazy allow branch that lets
// the owner in by letting everybody in would look like a fix and would be a
// regression. So the stranger in this fixture is not a nobody — he OWNS a
// project of his own and is a maintainer in it. An "any owner passes" or "any
// caller with standing somewhere passes" fix reddens the arms below; only
// "the owner of THIS project passes" is green.
//
// DB-gated like the rest of this package. Named by a `-run` regex in ci.yml and
// listed in internal/citest/dbtestcov/gated_tests.txt, or it runs nowhere:
//
//	AIHUB_TEST_DB=postgres://…/aihub_test?sslmode=disable \
//	go test ./internal/server/ -run TestProjectOwnerCanSeeTheirOwnProject -v -count=1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/auth"
	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// ownerVisStack is two projects and two non-admin callers, each of whom owns one
// of them. The asymmetry between them is the whole experiment: they differ in
// nothing except WHICH project they own.
type ownerVisStack struct {
	url  string
	pool *pgxpool.Pool

	// owned is the project under test: owner_user_id = ownerUID, members = [].
	// That is the exact row domain.CreateProject writes for a non-admin caller.
	owned string
	// away is the stranger's own project, and it exists so that the deny arms
	// cannot be explained by "this caller is nobody anywhere". He owns it AND is
	// a maintainer in it.
	away string

	ownerUID    string
	strangerUID string
	holderUID   string

	ownerKey    string // non-admin; OWNS `owned`, absent from its members
	strangerKey string // non-admin; owns and maintains `away`, nothing in `owned`

	// The secret: a running attempt in `owned` holding a file_scope lock.
	holderWIID   string
	holderWISlug string
	holderActor  string
	holderRepo   string
	holderPath   string
}

func newOwnerVisStack(t *testing.T) *ownerVisStack {
	t.Helper()
	pool := serverTestPool(t)
	ctx := context.Background()
	base := testname.Sanitize(t.Name())

	s := &ownerVisStack{
		pool:  pool,
		owned: "p_" + base + "o",
		away:  "p_" + base + "a",
		// Per-test rather than a shared literal: rules 2, 4 and 5 match
		// declared_resources across EVERY project, so a repo name shared with
		// another test's leftover attempt would put a foreign holder in this
		// test's response.
		holderRepo:  "r" + base,
		holderPath:  "internal/secret/owner_plan.go",
		holderActor: "owner-project-holder-display",
	}

	s.ownerUID = "u_" + base + "o"
	s.strangerUID = "u_" + base + "s"
	s.holderUID = "u_" + base + "h"
	s.ownerKey = "pfk_" + s.ownerUID
	s.strangerKey = "pfk_" + s.strangerUID

	seedUser := func(uid, role, key, keyID string) {
		entry := map[string]any{"id": keyID, "key_hash": auth.HashKey(key)}
		keys, err := json.Marshal([]map[string]any{entry})
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `
			INSERT INTO users(id,email,display_name,user_type,role,api_keys)
			VALUES($1,$1||'@test.local',$1,'human',$2,$3)
			ON CONFLICT (id) DO UPDATE SET api_keys=EXCLUDED.api_keys, role=EXCLUDED.role`,
			uid, role, keys)
		require.NoError(t, err)
	}
	// users.role is CHECKed to (writer|admin); "writer" is the non-admin value.
	seedUser(s.ownerUID, "writer", s.ownerKey, "k668own")
	seedUser(s.strangerUID, "writer", s.strangerKey, "k668str")
	// The holder's actor is an admin so that seeding the secret needs no
	// membership of its own — `owned` has an EMPTY members list and that is the
	// property under test, so nothing may be added to it.
	seedUser(s.holderUID, "admin", "pfk_"+s.holderUID, "k668hld")

	// 🔴 members is written as an EMPTY array on purpose, and it is the shape
	// domain.CreateProject produces rather than an invented one: that function's
	// INSERT does not name the column, so the row takes `DEFAULT '[]'`.
	_, err := pool.Exec(ctx,
		`INSERT INTO projects(name,owner_user_id,members) VALUES($1,$2,'[]'::jsonb)
		 ON CONFLICT (name) DO UPDATE SET members=EXCLUDED.members, owner_user_id=EXCLUDED.owner_user_id`,
		s.owned, s.ownerUID)
	require.NoError(t, err)

	awayMembers, err := json.Marshal([]map[string]any{
		{"user_id": s.strangerUID, "role": "maintainer"},
	})
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`INSERT INTO projects(name,owner_user_id,members) VALUES($1,$2,$3)
		 ON CONFLICT (name) DO UPDATE SET members=EXCLUDED.members, owner_user_id=EXCLUDED.owner_user_id`,
		s.away, s.strangerUID, awayMembers)
	require.NoError(t, err)

	resetProjectWorkItems(t, pool, s.owned)
	resetProjectWorkItems(t, pool, s.away)

	declared, err := json.Marshal([]domain.DeclaredResourceItem{
		{Type: "path", URI: "file:" + s.holderPath, Repo: s.holderRepo, Intent: "write"},
	})
	require.NoError(t, err)
	wiType := "fix_bug"
	holder, aerr := domain.CreateWorkItem(ctx, pool, &domain.CreateWorkItemRequest{
		Project: s.owned, Goal: "rotate the staging signing key before the audit",
		Scenario: "coding", WIType: &wiType, DeclaredResources: declared,
		Source: "human", ForceCreate: true, ForceReason: "aihub#668 fixture",
	}, s.holderUID, "owned-project-seeder", nil, "")
	require.Nil(t, aerr, "seed holder: %+v", aerr)
	claim, aerr := domain.FnClaimWorkItem(ctx, pool, holder.ID, &domain.ClaimRequest{
		IdempotencyKey: "idem-668-holder",
		SessionInfo: domain.SessionInfo{
			MachineID:     "m668",
			SessionSecret: "s3cr3t-0123456789abcdef0123456789abcdef0123456789abcdef01234567",
		},
	}, s.holderUID, "", s.holderActor)
	require.Nil(t, aerr, "claim holder: %+v", aerr)
	require.NotEmpty(t, claim.AcquiredLocks, "the fixture needs a real lock to conflict with")
	s.holderWIID, s.holderWISlug = holder.ID, holder.Slug

	ts := httptest.NewServer(NewRouter(pool, []byte("owner-visibility-test-cookie-secret")))
	t.Cleanup(ts.Close)
	s.url = ts.URL
	return s
}

// predict issues one POST /v1/conflicts/predict as the given key and returns the
// status and the RAW body — raw, because two arms compare bodies byte for byte
// and a decoded map would hide a wording difference.
func (s *ownerVisStack) predict(t *testing.T, key, body string) (int, string) {
	t.Helper()
	return s.do(t, http.MethodPost, "/v1/conflicts/predict", key, body)
}

// get issues one authenticated GET and returns the status and the raw body.
func (s *ownerVisStack) get(t *testing.T, key, path string) (int, string) {
	t.Helper()
	return s.do(t, http.MethodGet, path, key, "")
}

func (s *ownerVisStack) do(t *testing.T, method, path, key, body string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	r, err := http.NewRequest(method, s.url+path, rdr)
	require.NoError(t, err)
	r.Header.Set("Authorization", "Bearer "+key)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(raw)
}

// pathPayload is the request body naming one path in one project.
func (s *ownerVisStack) pathPayload(t *testing.T, project string, dryRun bool) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"project": project,
		"declared_resources": []map[string]any{
			{"type": "path", "uri": "file:" + s.holderPath, "repo": s.holderRepo, "intent": "write"},
		},
		"dry_run": dryRun,
	})
	require.NoError(t, err)
	return string(b)
}

// assertDisclosesNothing is the deny-arm assertion, and it is aihub#665's, kept
// clause for clause: this work item's failure mode is trading that fix for a
// false-rejection fix, so the property it bought is re-measured here rather than
// assumed to still hold.
func (s *ownerVisStack) assertDisclosesNothing(t *testing.T, status int, body string) {
	t.Helper()
	if status != http.StatusNotFound {
		t.Errorf("status %d, want 404. aihub#377: a caller who may not see a project gets what a "+
			"nonexistent one gets — and 403 in particular is wrong, because it confirms the "+
			"project exists. Body: %s", status, body)
	}
	if !strings.Contains(body, notVisibleMessage) {
		t.Errorf("body does not carry notVisibleMessage verbatim, so it is distinguishable from "+
			"the missing-project response: %s", body)
	}
	for _, secret := range []struct{ what, value string }{
		{"the project name", s.owned},
		{"the holder's actor_display", s.holderActor},
		{"the holder's work item id", s.holderWIID},
		{"the holder's work item slug", s.holderWISlug},
		{"the locked path", s.holderPath},
	} {
		if secret.value != "" && strings.Contains(body, secret.value) {
			t.Errorf("the refusal discloses %s (%q): %s", secret.what, secret.value, body)
		}
	}
}

// ownerNoSuchProject is a name nothing seeds. The oracle arm below asserts that
// rather than assuming it.
const ownerNoSuchProject = "p_no_such_project_for_owner_test"

func TestProjectOwnerCanSeeTheirOwnProject(t *testing.T) {
	s := newOwnerVisStack(t)
	ctx := context.Background()

	// ── the fixture, checked rather than assumed ─────────────────────────────
	//
	// Every arm below is about ONE person: a non-admin who owns a project and is
	// absent from its members. If the fixture quietly stops producing that
	// person, the allow arm passes for the wrong reason and the deny arms become
	// vacuous. These three arms are what stop that.

	t.Run("fixture_the_owner_owns_the_project_and_is_absent_from_its_members", func(t *testing.T) {
		var ownerUserID string
		var members []byte
		require.NoError(t, s.pool.QueryRow(ctx,
			`SELECT owner_user_id, members FROM projects WHERE name=$1`, s.owned,
		).Scan(&ownerUserID, &members))
		if ownerUserID != s.ownerUID {
			t.Fatalf("projects.owner_user_id is %q, want %q — the caller under test is not the "+
				"owner, so the allow arm would be measuring something else", ownerUserID, s.ownerUID)
		}
		var parsed []projectMember
		require.NoError(t, json.Unmarshal(members, &parsed))
		for _, m := range parsed {
			if m.UserID == s.ownerUID {
				t.Fatalf("the owner IS in projects.members with role %q. This test exists for the "+
					"person who is NOT, and a membership would let every arm below pass through "+
					"the ordinary member path without the owner arm existing at all", m.Role)
			}
		}
		if len(parsed) != 0 {
			t.Fatalf("projects.members is %s, and the row under test is the one "+
				"domain.CreateProject writes — an EMPTY list, because its INSERT does not name "+
				"the column", members)
		}
	})

	t.Run("fixture_the_owner_is_not_an_admin", func(t *testing.T) {
		var role string
		require.NoError(t, s.pool.QueryRow(ctx,
			`SELECT role FROM users WHERE id=$1`, s.ownerUID).Scan(&role))
		if role == "admin" {
			t.Fatal("the owner's GLOBAL role is admin, and every gate in this package short-" +
				"circuits on that before it ever reads ProjectRoles. The allow arm would then " +
				"pass on a build with no owner arm in it — which is precisely the way this " +
				"suite could go green while the defect is untouched")
		}
	})

	t.Run("fixture_the_stranger_owns_and_maintains_a_project_of_his_own", func(t *testing.T) {
		var ownerUserID string
		var members []byte
		require.NoError(t, s.pool.QueryRow(ctx,
			`SELECT owner_user_id, members FROM projects WHERE name=$1`, s.away,
		).Scan(&ownerUserID, &members))
		if ownerUserID != s.strangerUID {
			t.Fatalf("the stranger does not own %s (owner is %q). The deny arms are supposed to "+
				"refuse a caller who is a legitimate OWNER somewhere else; without that, they "+
				"are also satisfied by a fix that lets in anybody who owns anything",
				s.away, ownerUserID)
		}
		if !strings.Contains(string(members), s.strangerUID) {
			t.Fatalf("the stranger is not a member of his own project either (%s) — the deny "+
				"arms would then be refusing a caller with no standing anywhere, which is a "+
				"weaker statement than the one they are written to make", members)
		}
	})

	// ── the defect ──────────────────────────────────────────────────────────

	t.Run("the_owner_is_not_refused_their_own_projects_pre_claim_gate", func(t *testing.T) {
		// 🔴 THE BUG ARM. pf_predict_conflicts is what pf-work calls before
		// claiming anything, so this 404 stops its owner at the first step of
		// every session in a project they own.
		status, body := s.predict(t, s.ownerKey, s.pathPayload(t, s.owned, false))
		if status != http.StatusOK {
			t.Fatalf("the OWNER of %s was refused its pre-claim gate: %d %s\n\n"+
				"UserContext.ProjectRoles is derived from projects.members alone, so an owner "+
				"who is absent from that list reaches canSeeProject as a stranger. "+
				"domain.checkProjectAccess's level 2 has admitted exactly this person since it "+
				"was written; the map-based family never learned about the column.",
				s.owned, status, body)
		}
		for _, want := range []string{
			`"severity":"hard_block"`, `"rule":1`, s.holderActor, s.holderWIID, s.holderWISlug,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("the owner's answer lost %q — being admitted and then redacted is the "+
					"aihub#662 defect in a new place, not a fix. Body: %s", want, body)
			}
		}
	})

	t.Run("the_owner_reaches_their_own_projects_work_items_by_the_honest_route_too", func(t *testing.T) {
		// The same omission, one route over: handleGetWorkItem calls
		// server.checkProjectAccess, which reads the same map. This arm is what
		// makes the claim "the defect is the derivation, not the predict gate"
		// falsifiable — a fix applied inside canSeeProject alone leaves it red.
		status, body := s.get(t, s.ownerKey, "/v1/work_items/"+s.holderWIID)
		if status != http.StatusOK {
			t.Fatalf("the owner of %s cannot read a work item in it: %d %s — so this was never "+
				"a pf_predict_conflicts defect. Every /v1 route that calls "+
				"server.checkProjectAccess refuses the owner of a members-empty project.",
				s.owned, status, body)
		}
	})

	t.Run("the_ui_session_derivation_agrees_with_the_api_one", func(t *testing.T) {
		// 🔴 THE TWIN, MEASURED RATHER THAN ASSUMED. aihub#315 exists because this
		// derivation was a second copy of BearerAuth's and the two disagreed about
		// the same user's roles for a few commits, serving every /ui page load.
		// loadUserByAPIKeyID takes a pool and a key id and nothing else, so the
		// map it builds can be read directly — no session cookie needed — and that
		// is what makes this arm behavioural instead of structural.
		//
		// It is also the arm that catches the mutant the AST gate could not: a
		// query that keeps the words `owner_user_id` as an alias over an empty
		// literal while stopping selecting the column. The identifier survives
		// that; the map does not.
		uc, err := loadUserByAPIKeyID(ctx, s.pool, "k668own")
		require.NoError(t, err, "the owner's /ui session failed to load at all")
		if got := uc.ProjectRoles[s.owned]; got != projectOwnerMapRole {
			t.Errorf("the /ui derivation gives the owner %q on his own project, want %q — "+
				"/v1 and /ui would then disagree about the same user, which is the aihub#315 "+
				"shape one relation column further in. ProjectRoles=%v",
				got, projectOwnerMapRole, uc.ProjectRoles)
		}

		// The negative control on the same call: the stranger's /ui session must
		// not acquire a role here either. Without it, "the owner is in the map"
		// is also satisfied by a derivation that puts EVERY project in EVERY map.
		other, err := loadUserByAPIKeyID(ctx, s.pool, "k668str")
		require.NoError(t, err)
		if got, present := other.ProjectRoles[s.owned]; present {
			t.Errorf("the stranger's /ui session carries role %q on a project he neither owns "+
				"nor is a member of; ProjectRoles=%v", got, other.ProjectRoles)
		}
		if got := other.ProjectRoles[s.away]; got != "maintainer" {
			t.Errorf("the stranger lost his own project (%s role %q, want \"maintainer\") — the "+
				"arm above would then be passing because this derivation returns nothing at "+
				"all. ProjectRoles=%v", s.away, got, other.ProjectRoles)
		}
	})

	// ── the negative controls: aihub#665 must not be undone ──────────────────

	t.Run("negative_control_owning_another_project_grants_no_sight_of_this_one", func(t *testing.T) {
		// 🔴 THE ARM THAT BOUNDS THE FIX. The stranger is an owner — of `away`.
		// Any fix that admits "an owner" rather than "the owner of THIS project"
		// reddens here, and so does one that admits any caller who holds a role
		// somewhere.
		status, body := s.predict(t, s.strangerKey, s.pathPayload(t, s.owned, false))
		s.assertDisclosesNothing(t, status, body)
	})

	t.Run("negative_control_the_stranger_is_refused_under_dry_run_too", func(t *testing.T) {
		// aihub#665's disclosure was one boolean apart from its refusal: dry_run
		// picked which rule answered, and only one of the two folded. Both values
		// are measured for the same reason here.
		status, body := s.predict(t, s.strangerKey, s.pathPayload(t, s.owned, true))
		s.assertDisclosesNothing(t, status, body)
	})

	t.Run("negative_control_the_strangers_refusal_is_byte_identical_to_a_missing_project", func(t *testing.T) {
		var exists bool
		require.NoError(t, s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM projects WHERE name=$1)`, ownerNoSuchProject).Scan(&exists))
		if exists {
			t.Fatalf("%s exists, so it is not the missing-project oracle this arm needs",
				ownerNoSuchProject)
		}
		_, real := s.predict(t, s.strangerKey, s.pathPayload(t, s.owned, false))
		_, absent := s.predict(t, s.strangerKey, s.pathPayload(t, ownerNoSuchProject, false))
		if real != absent {
			t.Errorf("a project that exists and one that does not answer differently, so the "+
				"refusal still reports existence:\n  real:   %s\n  absent: %s", real, absent)
		}
	})

	t.Run("negative_control_the_stranger_is_still_blind_by_the_honest_route", func(t *testing.T) {
		// The pair to the owner's honest-route arm. A derivation fix that handed
		// out roles too generously would show up here first, because this route
		// asks for viewer on the project rather than mere visibility.
		status, body := s.get(t, s.strangerKey, "/v1/work_items/"+s.holderWIID)
		if status != http.StatusNotFound {
			t.Fatalf("the stranger can read a work item in a project he neither owns nor is a "+
				"member of: %d %s", status, body)
		}
		if strings.Contains(body, s.holderActor) || strings.Contains(body, s.holderWISlug) {
			t.Errorf("the refusal discloses the work item it refused: %s", body)
		}
	})
}
