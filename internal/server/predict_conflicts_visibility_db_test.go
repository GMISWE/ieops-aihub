package server

// aihub#665 — POST /v1/conflicts/predict must not answer about a project the
// caller cannot see, and the answers it DOES give must be redacted by the same
// rule whichever conflict rule produced them.
//
// 🔴 THE DEFECT WAS REACHABLE. The work item was filed on a code SHAPE — an early
// `return` before the H7 visibility fold, plus no authorization on `req.Project`
// anywhere — and said in as many words that nobody had demonstrated an actual
// unauthorized read. This file is that demonstration, taken on 2026-09-14 through
// the real router against a fully migrated database, on the commit the work item
// was filed against (9b4dc40). One authenticated caller holding no role in the
// holder's project, one payload, one boolean apart:
//
//	GET  /v1/work_items/<holder id>     404 {"code":"NOT_FOUND","message":"not found, or you
//	                                        do not have access; …"}
//	POST /v1/conflicts/predict dry_run=true
//	                                    200 {"severity":"soft_block","predictions":[{"rule":3,
//	                                        …,"description":"[conflict in project P, no
//	                                        visibility]"}]}
//	POST /v1/conflicts/predict dry_run=false
//	                                    200 {"severity":"hard_block","predictions":[{"rule":1,
//	                                        …,"attempt_id":"ra_…","actor_display":"victim-agent-
//	                                        display","work_item_id":"wi_…","work_item_slug":
//	                                        "P#1"}]}
//
// Blind to the project by every honest route, and handed the holder's actor,
// work item, slug and attempt id by this one. The two halves of the work item
// are one defect: the path that skipped the redaction was also the path with no
// authorization in front of it.
//
// 🔴 WHY THE ALLOW ARMS OUTNUMBER THE DENY ARMS. This call is pf-work's pre-claim
// gate — every agent in the workspace makes it before claiming anything. A check
// that refused a legitimate member would be a worse outcome than the disclosure
// it replaced, and "every denial is uniform" is trivially satisfiable by denying
// everyone. So the member, admin and in-scope-key arms are not decoration: they
// are the only thing that tells an authorization fix from an outage.
//
// DB-gated like the rest of this package. Named by a `-run` regex in ci.yml and
// listed in internal/citest/dbtestcov/gated_tests.txt, or it runs nowhere:
//
//	AIHUB_TEST_DB=postgres://…/aihub_test?sslmode=disable \
//	go test ./internal/server/ -run TestPredictConflictsVisibility -v -count=1

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

// predictVisStack is two projects and four callers with deliberately different
// sight of the first one.
type predictVisStack struct {
	url  string
	pool *pgxpool.Pool

	// victim holds the running attempt whose locks are the secret.
	victim string
	// home is where the stranger and the scoped key DO have standing, so a
	// refusal cannot be explained by "this caller is a member of nothing".
	home string

	adminKey    string // global admin, member of nothing
	memberKey   string // writer on victim only — the authorized caller
	strangerKey string // writer on home only — no role on victim
	scopedKey   string // writer on BOTH, key confined to home

	holderWIID   string
	holderWISlug string
	holderActor  string
	holderRepo   string
	holderPath   string
	// crossPath is locked by an attempt in `home` under a key namespaced to
	// `victim` — the shape that makes rule 1 report a holder from a project the
	// (authorized) caller cannot see. See the rule-1 fold arm below.
	crossPath string
	// crossActor is that holder's actor_display, and it is a FIELD rather than a
	// literal on purpose: the arm below asserts it is ABSENT from the response,
	// and an absence assertion against a hardcoded string silently passes the day
	// somebody renames the fixture. Every other secret in this file is resolved
	// through s.* for the same reason.
	crossActor string
	// crossWISlug and crossWIID are the same, for the two fields the fold must
	// strip.
	crossWISlug string
	crossWIID   string
	// blockedGoal is the goal of a work item inside `victim` that is BLOCKED by
	// the holder, i.e. what will_unlock would publish.
	blockedGoal string
	blockedWIID string

	// The declaration-family fixture: one running work item in `victim` that
	// DECLARES the repo (intent refactor) and an external_ref, so rules 2, 4 and 5
	// all fire for a payload naming the same two. Those three rules match
	// declarations across every project and never touch `project`, so they are
	// reachable without one — which is what makes them the arm that measures the
	// fold rather than the authorization gate.
	externalRef     string
	declaringActor  string
	declaringWIID   string
	declaringWISlug string
}

func newPredictVisStack(t *testing.T) *predictVisStack {
	t.Helper()
	pool := serverTestPool(t)
	ctx := context.Background()
	base := testname.Sanitize(t.Name())

	s := &predictVisStack{
		pool:        pool,
		victim:      "p_" + base + "v",
		home:        "p_" + base + "h",
		holderActor: "victim-agent-display",
		// 🔴 Per-test, not a shared literal. Rules 2, 4 and 5 match
		// declared_resources containment across EVERY project and every running
		// work item in the database, so a repo or external_ref name shared with
		// another test's leftover attempt would put a foreign holder in this
		// test's response and make the absence assertions pass or fail for
		// reasons that have nothing to do with the fold.
		holderRepo:     "r" + base,
		externalRef:    "https://example.invalid/" + base,
		holderPath:     "internal/secret/plan.go",
		crossPath:      "internal/secret/cross.go",
		crossActor:     "home-agent-display",
		blockedGoal:    "decommission the embargoed reconciliation cron before the audit window",
		declaringActor: "declaring-agent-display",
	}

	adminUID := "u_" + base + "a"
	memberUID := "u_" + base + "m"
	strangerUID := "u_" + base + "s"
	scopedUID := "u_" + base + "c"
	s.adminKey = "pfk_" + adminUID
	s.memberKey = "pfk_" + memberUID
	s.strangerKey = "pfk_" + strangerUID
	s.scopedKey = "pfk_" + scopedUID

	seedUser := func(uid, role, key, keyID string, scope *string) {
		entry := map[string]any{"id": keyID, "key_hash": auth.HashKey(key)}
		if scope != nil {
			entry["project_scope"] = *scope
		}
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
	seedUser(adminUID, "admin", s.adminKey, "k665adm", nil)
	seedUser(memberUID, "writer", s.memberKey, "k665mem", nil)
	seedUser(strangerUID, "writer", s.strangerKey, "k665str", nil)
	seedUser(scopedUID, "writer", s.scopedKey, "k665scp", &s.home)

	membersVictim, err := json.Marshal([]map[string]any{
		{"user_id": memberUID, "role": "maintainer"},
		// The scoped user IS a member here. That is the whole point of the
		// out-of-scope arm: the confinement must hold against a membership the
		// user really has, which is the case a membership-only check waves through.
		{"user_id": scopedUID, "role": "writer"},
	})
	require.NoError(t, err)
	membersHome, err := json.Marshal([]map[string]any{
		{"user_id": strangerUID, "role": "maintainer"},
		{"user_id": scopedUID, "role": "writer"},
	})
	require.NoError(t, err)
	for _, p := range []struct {
		name    string
		members []byte
	}{{s.victim, membersVictim}, {s.home, membersHome}} {
		_, err = pool.Exec(ctx,
			`INSERT INTO projects(name,owner_user_id,members) VALUES($1,$2,$3)
			 ON CONFLICT (name) DO UPDATE SET members=EXCLUDED.members, owner_user_id=EXCLUDED.owner_user_id`,
			p.name, adminUID, p.members)
		require.NoError(t, err)
	}
	resetProjectWorkItems(t, pool, s.victim)
	resetProjectWorkItems(t, pool, s.home)

	// The secret: a running attempt in `victim` holding a file_scope lock.
	declared, err := json.Marshal([]domain.DeclaredResourceItem{
		{Type: "path", URI: "file:" + s.holderPath, Repo: s.holderRepo, Intent: "write"},
	})
	require.NoError(t, err)
	wiType := "fix_bug"
	holder, aerr := domain.CreateWorkItem(ctx, pool, &domain.CreateWorkItemRequest{
		Project: s.victim, Goal: "rotate the production signing key before the audit",
		Scenario: "coding", WIType: &wiType, DeclaredResources: declared,
		Source: "human", ForceCreate: true, ForceReason: "aihub#665 fixture",
	}, adminUID, "victim-owner", nil, "")
	require.Nil(t, aerr, "seed holder: %+v", aerr)
	claim, aerr := domain.FnClaimWorkItem(ctx, pool, holder.ID, &domain.ClaimRequest{
		IdempotencyKey: "idem-665-holder",
		SessionInfo: domain.SessionInfo{
			MachineID:     "m665",
			SessionSecret: "s3cr3t-0123456789abcdef0123456789abcdef0123456789abcdef01234567",
		},
	}, adminUID, "", s.holderActor)
	require.Nil(t, aerr, "claim holder: %+v", aerr)
	require.NotEmpty(t, claim.AcquiredLocks, "the fixture needs a real lock to conflict with")
	s.holderWIID, s.holderWISlug = holder.ID, holder.Slug

	// A second holder, this one in `home`, taking a lock key namespaced to
	// `victim` through an explicit requested_locks. deriveClaimLocks passes
	// client-supplied entries through verbatim (ValidateRequestedLocks checks the
	// type vocabulary and a non-empty key, nothing else), so a lock row in one
	// project's namespace can be owned by an attempt in another. That is what
	// makes rule 1 able to name a cross-project holder to a caller who is
	// properly authorized on the project they asked about — the case the H7 fold
	// exists for, and the case rule 1's early return used to jump over.
	crossWI, aerr := domain.CreateWorkItem(ctx, pool, &domain.CreateWorkItemRequest{
		Project: s.home, Goal: "migrate the shared billing ledger onto the new partition scheme",
		Scenario: "coding", WIType: &wiType,
		Source: "human", ForceCreate: true, ForceReason: "aihub#665 fixture",
	}, adminUID, "home-owner", nil, "")
	require.Nil(t, aerr, "seed cross-namespace holder: %+v", aerr)
	crossClaim, aerr := domain.FnClaimWorkItem(ctx, pool, crossWI.ID, &domain.ClaimRequest{
		IdempotencyKey: "idem-665-cross",
		SessionInfo: domain.SessionInfo{
			MachineID:     "m665x",
			SessionSecret: "s3cr3t-0123456789abcdef0123456789abcdef0123456789abcdef01234568",
		},
		RequestedLocks: []domain.ResourceLockReq{{
			ResourceType: "file_scope",
			ResourceKey:  s.victim + ":" + s.holderRepo + ":" + s.crossPath,
		}},
	}, adminUID, "", s.crossActor)
	require.Nil(t, aerr, "claim cross-namespace holder: %+v", aerr)
	require.NotEmpty(t, crossClaim.AcquiredLocks,
		"the requested_locks entry took no lock, so the rule-1 fold arm below would be "+
			"measuring an absent conflict rather than a redacted one")
	s.crossWIID, s.crossWISlug = crossWI.ID, crossWI.Slug

	// A work item in `victim` BLOCKED by the holder, so the holder has a real
	// will_unlock entry to disclose. Its goal is the payload a stranger must not
	// be able to read by naming the holder's canonical id.
	blocked, aerr := domain.CreateWorkItem(ctx, pool, &domain.CreateWorkItemRequest{
		Project: s.victim, Goal: s.blockedGoal, Scenario: "coding", WIType: &wiType,
		Source: "human", BlockedBy: []string{holder.ID},
		ForceCreate: true, ForceReason: "aihub#665 fixture",
	}, adminUID, "victim-owner", nil, "")
	require.Nil(t, aerr, "seed blocked work item: %+v", aerr)
	s.blockedWIID = blocked.ID

	// The declaration-family holder: rules 2, 4 and 5 join
	// work_items.declared_resources on a RUNNING work item, so it has to be
	// claimed for any of them to fire.
	declared2, err := json.Marshal([]domain.DeclaredResourceItem{
		{Type: "repo", URI: "repo:" + s.holderRepo, Intent: "refactor"},
		{Type: "external_ref", URI: s.externalRef, Intent: "write"},
	})
	require.NoError(t, err)
	declaring, aerr := domain.CreateWorkItem(ctx, pool, &domain.CreateWorkItemRequest{
		Project: s.victim, Goal: "split the vendored protocol package out of the monorepo",
		Scenario: "coding", WIType: &wiType, DeclaredResources: declared2,
		Source: "human", ForceCreate: true, ForceReason: "aihub#665 fixture",
	}, adminUID, "victim-owner", nil, "")
	require.Nil(t, aerr, "seed declaration-family holder: %+v", aerr)
	_, aerr = domain.FnClaimWorkItem(ctx, pool, declaring.ID, &domain.ClaimRequest{
		IdempotencyKey: "idem-665-declaring",
		SessionInfo: domain.SessionInfo{
			MachineID:     "m665d",
			SessionSecret: "s3cr3t-0123456789abcdef0123456789abcdef0123456789abcdef01234569",
		},
	}, adminUID, "", s.declaringActor)
	require.Nil(t, aerr, "claim declaration-family holder: %+v", aerr)
	s.declaringWIID, s.declaringWISlug = declaring.ID, declaring.Slug

	ts := httptest.NewServer(NewRouter(pool, []byte("predict-visibility-test-cookie-secret")))
	t.Cleanup(ts.Close)
	s.url = ts.URL
	return s
}

// predict issues one POST /v1/conflicts/predict as the given key and returns the
// status and the RAW body — raw, because two of the arms below compare bodies
// byte for byte and a decoded map would hide a wording difference.
func (s *predictVisStack) predict(t *testing.T, key, body string) (int, string) {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, s.url+"/v1/conflicts/predict", strings.NewReader(body))
	require.NoError(t, err)
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(raw)
}

// pathPayload is the request body naming one path in one project.
func (s *predictVisStack) pathPayload(t *testing.T, project, path string, dryRun bool) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"project": project,
		"declared_resources": []map[string]any{
			{"type": "path", "uri": "file:" + path, "repo": s.holderRepo, "intent": "write"},
		},
		"dry_run": dryRun,
	})
	require.NoError(t, err)
	return string(b)
}

// assertDisclosesNothing is the deny-arm assertion, and every clause of it is a
// thing the pre-fix build actually returned.
func (s *predictVisStack) assertDisclosesNothing(t *testing.T, status int, body string) {
	t.Helper()
	if status != http.StatusNotFound {
		t.Errorf("status %d, want 404. aihub#377: a caller who may not see a project gets what a "+
			"nonexistent one gets — and 403 in particular is wrong, because it confirms the "+
			"project exists. Body: %s", status, body)
	}
	if !strings.Contains(body, notVisibleMessage) {
		t.Errorf("body does not carry notVisibleMessage verbatim, so it is distinguishable from "+
			"the missing-project response and still answers \"does this project exist\": %s", body)
	}
	for _, secret := range []struct{ what, value string }{
		{"the project name", s.victim},
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

// noSuchProject is a name nothing seeds. The oracle arm below asserts that
// rather than assuming it.
const noSuchProject = "p_no_such_project_anywhere"

func TestPredictConflictsVisibilityAcrossProjects(t *testing.T) {
	s := newPredictVisStack(t)

	// ── the fixture, checked rather than assumed ─────────────────────────────
	//
	// Without these two arms every deny arm below could pass because the fixture
	// forgot to create the conflict, or because the stranger could see the
	// project all along and the 404 came from somewhere else.
	t.Run("fixture_the_stranger_is_blind_to_the_victim_project_by_an_honest_route", func(t *testing.T) {
		r, err := http.NewRequest(http.MethodGet, s.url+"/v1/work_items/"+s.holderWIID, nil)
		require.NoError(t, err)
		r.Header.Set("Authorization", "Bearer "+s.strangerKey)
		resp, err := http.DefaultClient.Do(r)
		require.NoError(t, err)
		defer resp.Body.Close() //nolint:errcheck
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("the stranger can read the victim project's work item (status %d) — the "+
				"fixture withheld nothing and every deny arm below is vacuous. Body: %s",
				resp.StatusCode, raw)
		}
	})

	t.Run("negative_control_the_member_still_gets_the_whole_answer", func(t *testing.T) {
		// 🔴 THE ANCHOR ARM. It is what the disclosure looked like, and it is what
		// an authorized caller must still receive. If this goes red the fix has
		// traded a leak for a false rejection in the one call every agent in the
		// workspace makes before claiming.
		status, body := s.predict(t, s.memberKey, s.pathPayload(t, s.victim, s.holderPath, false))
		if status != http.StatusOK {
			t.Fatalf("a maintainer of the project was refused its own pre-claim gate: %d %s",
				status, body)
		}
		for _, want := range []string{
			`"severity":"hard_block"`, `"rule":1`, s.holderActor, s.holderWIID, s.holderWISlug,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("the authorized answer lost %q — a redaction that fires for MEMBERS is "+
					"the aihub#662 defect in a new place, not a fix. Body: %s", want, body)
			}
		}
	})

	t.Run("negative_control_the_admin_still_gets_the_whole_answer", func(t *testing.T) {
		// aihub#227: an admin's ProjectRoles map is EMPTY. A membership-only gate
		// reads administrator-of-everything as member-of-nothing and 404s the
		// whole endpoint for every admin — which is exactly how aihub#662's fold
		// got this wrong one layer down.
		status, body := s.predict(t, s.adminKey, s.pathPayload(t, s.victim, s.holderPath, false))
		if status != http.StatusOK || !strings.Contains(body, s.holderActor) {
			t.Fatalf("an admin was refused or redacted: %d %s", status, body)
		}
	})

	t.Run("negative_control_a_scoped_key_inside_its_own_scope_is_not_refused", func(t *testing.T) {
		// The scope arm may NARROW only. An agent holding a project-scoped key
		// predicts inside that project all day.
		status, body := s.predict(t, s.scopedKey, s.pathPayload(t, s.home, s.holderPath, false))
		if status != http.StatusOK {
			t.Fatalf("a key scoped to %s was refused a predict about %s: %d %s",
				s.home, s.home, status, body)
		}
	})

	// ── the disclosure, both halves ─────────────────────────────────────────

	t.Run("non_member_predict_is_the_shared_not_found_hard_path", func(t *testing.T) {
		// dry_run=false reaches rule 1 — the path that returned before the fold.
		status, body := s.predict(t, s.strangerKey, s.pathPayload(t, s.victim, s.holderPath, false))
		s.assertDisclosesNothing(t, status, body)
	})

	t.Run("non_member_predict_is_the_shared_not_found_advisory_path", func(t *testing.T) {
		// dry_run=true skips rule 1 and reaches rule 3, which WAS folded — but
		// the fold still named the project and still published the holder's
		// resource_key, i.e. the victim project's repo and file path.
		status, body := s.predict(t, s.strangerKey, s.pathPayload(t, s.victim, s.holderPath, true))
		s.assertDisclosesNothing(t, status, body)
	})

	t.Run("both_dry_run_values_answer_identically_to_a_non_member", func(t *testing.T) {
		// The pre-fix build's two answers differed in every field that mattered.
		// Any remaining difference is a bit a caller can read.
		_, hard := s.predict(t, s.strangerKey, s.pathPayload(t, s.victim, s.holderPath, false))
		_, soft := s.predict(t, s.strangerKey, s.pathPayload(t, s.victim, s.holderPath, true))
		if hard != soft {
			t.Errorf("dry_run still changes what a non-member is told:\n  false: %s\n  true:  %s",
				hard, soft)
		}
	})

	t.Run("a_project_that_does_not_exist_answers_the_same_as_one_that_does", func(t *testing.T) {
		// 🔴 The other direction of the same oracle, and the reason the gate is
		// keyed on MEMBERSHIP rather than on existence: a caller holds no role in
		// a nonexistent project either, so both take one exit.
		// The imaginary project really is imaginary. Asserted rather than assumed:
		// if some other test ever seeds it, this arm would compare two REAL
		// projects and pass while proving nothing.
		var n int
		require.NoError(t, s.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM projects WHERE name=$1`, noSuchProject).Scan(&n))
		require.Zero(t, n, "%s exists, so this arm is comparing two real projects", noSuchProject)

		_, real := s.predict(t, s.strangerKey, s.pathPayload(t, s.victim, s.holderPath, false))
		_, fake := s.predict(t, s.strangerKey,
			s.pathPayload(t, noSuchProject, s.holderPath, false))
		if real != fake {
			t.Errorf("a real project and an imaginary one answer differently, so the refusal is "+
				"itself an existence oracle:\n  real:      %s\n  imaginary: %s", real, fake)
		}
	})

	t.Run("an_invisible_work_item_id_resolves_like_an_absent_one", func(t *testing.T) {
		// work_item_id accepts a slug (aihub#357) and slugs are `<project>#<seq>`
		// counting from 1, so this parameter is the enumerable one. Authorizing
		// the RESOLVED project would have answered 404 for a real slug and the
		// aihub#662 400 for a made-up one — one bit per guess. The fix declines to
		// ADOPT an invisible resolution instead, which leaves both on the
		// documented create-preview fallback.
		body := func(ref string) string {
			b, err := json.Marshal(map[string]any{
				"work_item_id": ref,
				"declared_resources": []map[string]any{
					{"type": "path", "uri": "file:" + s.holderPath, "repo": s.holderRepo,
						"intent": "write"},
				},
			})
			require.NoError(t, err)
			return string(b)
		}
		realStatus, realBody := s.predict(t, s.strangerKey, body(s.holderWISlug))
		fakeStatus, fakeBody := s.predict(t, s.strangerKey, body(s.victim+"#99999"))
		if realStatus != fakeStatus || realBody != fakeBody {
			t.Errorf("a real work item slug in a project the caller cannot see is "+
				"distinguishable from one that does not exist:\n  %d %s\n  %d %s",
				realStatus, realBody, fakeStatus, fakeBody)
		}
		// 🔴 Positive control: the slug really does resolve, for somebody — and it
		// varies ONE thing, the slug, holding the caller fixed. The first version
		// of this arm compared member+real against STRANGER+fake, which moves the
		// caller and the slug together and is therefore satisfied by a slug that
		// was never valid.
		memberRealStatus, memberRealBody := s.predict(t, s.memberKey, body(s.holderWISlug))
		memberFakeStatus, memberFakeBody := s.predict(t, s.memberKey, body(s.victim+"#99999"))
		if memberRealStatus == memberFakeStatus && memberRealBody == memberFakeBody {
			t.Errorf("for the MEMBER, the real slug and the fake one answer identically too, so "+
				"the pair above proves nothing about visibility — the slug may simply never "+
				"have resolved for anybody:\n  real: %d %s\n  fake: %d %s",
				memberRealStatus, memberRealBody, memberFakeStatus, memberFakeBody)
		}
	})

	t.Run("a_scoped_key_cannot_reach_outside_its_scope_even_where_it_is_a_member", func(t *testing.T) {
		// The user behind this key IS a writer on the victim project. The
		// confinement is on the KEY, and a membership-only authorization check
		// waves this through while looking correct.
		status, body := s.predict(t, s.scopedKey, s.pathPayload(t, s.victim, s.holderPath, false))
		s.assertDisclosesNothing(t, status, body)
	})

	t.Run("the_gate_is_not_suspended_by_naming_a_work_item_id_as_well", func(t *testing.T) {
		// 🔴 THE ARM A REVIEW MUTATION PUT HERE. Every other deny arm sets
		// `project` OR `work_item_id`, never both, and a gate spelled
		// `effectiveProject != "" && req.WorkItemID == nil && !canSeeProject(…)`
		// survived the ENTIRE suite, database arms included. End to end the
		// stranger then got the shared 404 without a work_item_id and
		// `200 hard_block` — resource_key and all — the moment they added a bogus
		// one. A parameter the caller controls must not be able to switch an
		// authorization check off.
		//
		// Both spellings, because both reach a different branch of the resolution:
		// the first resolves to nothing (ErrNoRows, the create-preview fallback),
		// the second resolves to a REAL work item the caller may not see, which
		// the fix declines to adopt. Either way effectiveProject falls back to the
		// project named alongside it, and the gate has to still see it.
		for _, ref := range []struct{ name, value string }{
			{"an id that resolves to nothing", "wi_no_such_work_item"},
			{"a real slug in the project the caller cannot see", s.holderWISlug},
		} {
			t.Run(ref.name, func(t *testing.T) {
				b, err := json.Marshal(map[string]any{
					"project":      s.victim,
					"work_item_id": ref.value,
					"declared_resources": []map[string]any{
						{"type": "path", "uri": "file:" + s.holderPath, "repo": s.holderRepo,
							"intent": "write"},
					},
					"dry_run": false,
				})
				require.NoError(t, err)
				status, body := s.predict(t, s.strangerKey, string(b))
				s.assertDisclosesNothing(t, status, body)
			})
		}
	})

	t.Run("every_rule_is_folded_not_only_the_ones_that_happened_to_set_work_item_id",
		func(t *testing.T) {
			// 🔴 THE FOLD'S ANCHOR IS `p.WIID != ""`, and rules 4 and 5 selected
			// `wi.id`, scanned it, and dropped it — so they were structurally
			// exempt from the redaction. Measured 2026-09-14 during review of this
			// change, on a caller with NO role in the holder's project and a
			// repo-only payload (so no project resolves and the authorization gate
			// cannot fire): one response carried rule 2 as
			// "[conflict in project P, no visibility]" AND rules 4 and 5 with
			// actor_display and work_item_slug in full. Same holder, same
			// response, redacted by one rule and published by two.
			//
			// This arm is deliberately driven with NO project: that is the payload
			// shape the gate above does not cover, so what it measures is the fold
			// alone.
			b, err := json.Marshal(map[string]any{
				"declared_resources": []map[string]any{
					{"type": "repo", "uri": "repo:" + s.holderRepo, "intent": "refactor"},
					{"type": "external_ref", "uri": s.externalRef},
				},
				"dry_run": false,
			})
			require.NoError(t, err)
			status, body := s.predict(t, s.strangerKey, string(b))
			if status != http.StatusOK {
				t.Fatalf("a repo/external_ref payload with no project must still be answered "+
					"(aihub#662 carved exactly this out): %d %s", status, body)
			}
			// The controls first: the rules really did fire, or "no holder was
			// disclosed" is also what an empty response says.
			for _, want := range []string{`"rule":2`, `"rule":4`, `"rule":5`} {
				if !strings.Contains(body, want) {
					t.Fatalf("%s did not fire, so this arm is measuring an empty response "+
						"rather than a folded one. Body: %s", want, body)
				}
			}
			for _, secret := range []struct{ what, value string }{
				{"the holder's actor_display", s.declaringActor},
				{"the holder's work item slug", s.declaringWISlug},
				{"the holder's work item id", s.declaringWIID},
			} {
				if strings.Contains(body, secret.value) {
					t.Errorf("a rule published %s (%q) to a caller with no role in that "+
						"project. The H7 fold keys on work_item_id, so a rule that does not "+
						"set it walks straight through the redaction. Body: %s",
						secret.what, secret.value, body)
				}
			}
			if n := strings.Count(body, "no visibility]"); n < 3 {
				t.Errorf("only %d prediction(s) were folded, want all 3 — a rule that reports "+
					"a holder it will not name is the only acceptable answer here. Body: %s",
					n, body)
			}
		})

	t.Run("will_unlock_does_not_publish_goals_from_an_invisible_project", func(t *testing.T) {
		// 🔴 will_unlock is computed BEFORE the fold label and passes through no
		// visibility filter at all, so the only thing bounding it is which id it
		// is keyed on. It used to be keyed on the caller's raw reference: a
		// stranger naming the canonical `wi_...` id of a work item in a project
		// they cannot see got that work item's blocked dependents back, GOALS
		// INCLUDED, while an id that does not exist got an empty list. Measured
		// 2026-09-14 during review of this change.
		//
		// The payload is repo-only so no project resolves and the authorization
		// gate cannot fire — this measures the will_unlock keying alone.
		body := func(ref string) (int, string) {
			b, err := json.Marshal(map[string]any{
				"work_item_id": ref,
				"declared_resources": []map[string]any{
					{"type": "repo", "uri": "repo:" + s.holderRepo, "intent": "write"},
				},
			})
			require.NoError(t, err)
			return s.predict(t, s.strangerKey, string(b))
		}
		_, invisible := body(s.holderWIID)
		if strings.Contains(invisible, s.blockedGoal) || strings.Contains(invisible, s.blockedWIID) {
			t.Errorf("will_unlock published a work item from a project this caller cannot see: %s",
				invisible)
		}
		_, absent := body("wi_no_such_work_item")
		if invisible != absent {
			t.Errorf("naming a real but invisible work item id is distinguishable from naming "+
				"one that does not exist:\n  invisible: %s\n  absent:    %s", invisible, absent)
		}
		// 🔴 The control: the entry EXISTS and the member gets it. Without this,
		// "the stranger saw no will_unlock" is also true of a fixture that never
		// created a dependency.
		_, seen := s.predict(t, s.memberKey, func() string {
			b, err := json.Marshal(map[string]any{
				"work_item_id": s.holderWIID,
				"declared_resources": []map[string]any{
					{"type": "repo", "uri": "repo:" + s.holderRepo, "intent": "write"},
				},
			})
			require.NoError(t, err)
			return string(b)
		}())
		if !strings.Contains(seen, s.blockedGoal) {
			t.Errorf("the MEMBER does not see the will_unlock entry either, so the arms above "+
				"prove nothing: %s", seen)
		}
	})

	// ── the fold, on the rule that used to skip it ──────────────────────────

	t.Run("rule_1_hard_block_is_redacted_when_the_holder_is_in_another_project", func(t *testing.T) {
		// 🔴 THIS IS THE ARM THE `goto fold` EXISTS FOR, and it is deliberately
		// run as an AUTHORIZED caller: the member may ask about `victim`, so the
		// authorization gate lets the call through and only the fold stands
		// between them and a holder they cannot see. On the pre-fix build rule 1
		// returned before the fold and this answer named the home project's
		// attempt in full.
		status, body := s.predict(t, s.memberKey, s.pathPayload(t, s.victim, s.crossPath, false))
		if status != http.StatusOK {
			t.Fatalf("the authorized caller was refused: %d %s", status, body)
		}
		// Still blocked, and still told so. A "fix" that dropped the prediction
		// would satisfy every redaction clause below while removing the hard gate
		// pf-work branches on.
		for _, want := range []string{
			`"severity":"hard_block"`, `"rule":1`, `"resource_type":"file_scope"`,
			domain.FoldedConflictDescription,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("the folded hard_block lost %q — the caller must still learn it is "+
					"blocked. Body: %s", want, body)
			}
		}
		for _, secret := range []string{s.crossActor, s.crossWIID, s.crossWISlug,
			`"attempt_id"`} {
			if strings.Contains(body, secret) {
				t.Errorf("rule 1's hard_block still publishes %s for a holder in a project the "+
					"caller cannot see — that is the aihub#665 fold bypass. Body: %s",
					secret, body)
			}
		}

		// 🔴 THE aihub#679 ARM. Everything above this line was already here, and
		// the fold was STILL naming the project — because the absence list two
		// clauses up enumerates the secrets aihub#665 remembered (actor, wi id,
		// slug, attempt id) and the project name was not one of them. The `want`
		// list even pinned the leak in place: it required the literal
		// "[conflict in project " + s.home + ", no visibility]" to be PRESENT, so
		// the one arm covering this code path asserted the disclosure rather than
		// refusing it. That is why this is added here and not in a new file: a
		// census that is read as complete has to be corrected where it is read.
		//
		// The caller is s.memberKey, a maintainer of `victim` and NOTHING in
		// `home` — see the fixture's members rows. The holder of s.crossPath is an
		// attempt in `home` holding a key namespaced to `victim`, which is the
		// only shape that makes an AUTHORIZED predict report a holder from an
		// invisible project. So the caller is entitled to the call and not
		// entitled to the tenant.
		//
		// Why a project name is worth an arm of its own. pf_predict_conflicts is
		// the pre-claim gate every agent calls, any key with a role in any one
		// project may call it, and the payload that reaches this branch is just a
		// path. Paths that exist in every repo (README.md, go.mod, Makefile) turn
		// the fold into an oracle that returns one project name per hit, and in
		// this deployment project names are frequently customer names.
		if strings.Contains(body, s.home) {
			t.Errorf("the folded hard_block still names %q — the project the caller has no "+
				"role in. The fold withholds WHO holds the lock and then says WHERE, which "+
				"makes it an enumeration oracle for the project list: this endpoint is "+
				"reachable by every authenticated caller, takes an arbitrary path, and "+
				"project names here are frequently customer names. Body: %s", s.home, body)
		}
		// 🔴 THE CONTROL FOR THE ARM ABOVE, and it is not ceremony: an absence
		// assertion is satisfied by a needle that appears in NO response at all.
		// The discriminating control is in this same body — it carries TWO project
		// names' worth of opportunity and must carry exactly one. `victim` is the
		// caller's own project and reaches the wire in resource_key
		// ("<project>:<repo>:<path>"); `home` is the holder's and must not appear
		// anywhere. One response, one code path, one redaction: that is what tells
		// "the fold withheld it" from "project names never appear in predictions".
		//
		// The degeneracy guard comes first because both halves are fixture-derived
		// ("p_<sanitized name>v" / "…h"): if a rename ever made one a substring of
		// the other, the presence arm and the absence arm would contradict each
		// other and the absence arm would be the one that silently won.
		if len(s.home) < 3 || s.home == s.victim ||
			strings.Contains(s.victim, s.home) || strings.Contains(s.home, s.victim) {
			t.Fatalf("the needles are degenerate: home=%q victim=%q. The absence assertion "+
				"above proves nothing unless home is a distinctive string that neither "+
				"contains nor is contained by the project the caller CAN see.",
				s.home, s.victim)
		}
		if !strings.Contains(body, s.victim) {
			t.Errorf("the caller's OWN project name %q does not appear in this response "+
				"either, so `!Contains(body, %q)` above is satisfied by the fact that no "+
				"project name ever reaches the wire, not by the fold. resource_key is "+
				"\"<project>:<repo>:<path>\" and the caller is entitled to its own half of "+
				"it. Body: %s", s.victim, s.home, body)
		}
	})
}
