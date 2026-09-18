package client

// skill_seed_test.go — the client-side regressions for the aihub#708 final
// Astra blocker 3: SeedSkill's name lookup is OWNER-SCOPED to the
// authenticated caller's own namespace and can never adopt another owner's
// visible same-name skill.
//
// The fake server below models the real registry endpoints the seed drives
// (GET /v1/users/me, GET /v1/skills with the owner/limit/cursor params,
// GET /v1/skills/:id/versions) including the behavior the fix depends on:
// the skills list applies the `owner` filter (for admins too) on top of the
// caller's visibility, and an unfiltered list returns everything the caller
// can see — own skills, other owners' PUBLIC versions, and versions SHARED
// with a project the caller belongs to. That last shape is the vulnerability's
// habitat: two owners may hold the same skill NAME (the registry's identity
// is unique per owner), so a name match over the visible list can pick a
// stranger's skill, and for an unscoped admin even publish into it.
//
// Every test asserts BOTH halves: the outcome (exists / id / versions) and
// the wire (the owner param was sent with the resolved caller id). If the
// client regresses to an unfiltered lookup, the fake answers the way the real
// server would — with the stranger's same-name skill in the list — and the
// outcome assertions fail.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedFakeDigest is a 64-char lowercase-hex suffix, so fake digests carry the
// registry's "sha256:<64 hex>" shape.
const seedFakeDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// seedFakeSkill is one registry skill in the fake: its owner, its visibility
// shape (private by default, optionally public and/or shared with named
// projects) and its single published version's digest.
type seedFakeSkill struct {
	id         string
	name       string
	owner      string
	public     bool
	sharedWith []string
	digest     string
}

// seedFakeServer answers the three endpoints SeedSkill drives, with the real
// server's owner-filter and visibility semantics.
type seedFakeServer struct {
	t *testing.T
	// callerID / callerRole / callerProjects are the authenticated principal's
	// identity: what /v1/users/me answers and whose visibility governs the
	// skills list.
	callerID       string
	callerRole     string
	callerProjects []string
	skills         []seedFakeSkill

	// ignoreOwnerFilter models a server-side regression (a server that loses
	// the owner param), for the client's defense-in-depth arm.
	ignoreOwnerFilter bool

	// observed wire state, asserted by the tests.
	sawWhoami      int
	ownerParams    []string
	versionListFor []string
}

func (f *seedFakeServer) visibleToCaller(s seedFakeSkill) bool {
	if s.owner == f.callerID {
		return true
	}
	if s.public {
		return true
	}
	for _, p := range s.sharedWith {
		for _, mine := range f.callerProjects {
			if p == mine {
				return true
			}
		}
	}
	return false
}

func (f *seedFakeServer) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	require.NoError(f.t, json.NewEncoder(w).Encode(body))
}

func (f *seedFakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/users/me":
		f.sawWhoami++
		f.writeJSON(w, http.StatusOK, map[string]any{
			"user_id":      f.callerID,
			"display_name": "Caller",
			"role":         f.callerRole,
			"projects":     f.callerProjects,
		})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/skills":
		owner := r.URL.Query().Get("owner")
		f.ownerParams = append(f.ownerParams, owner)
		if owner == "" {
			f.t.Errorf("the skills list request carried no owner parameter: %s", r.URL.RawQuery)
		}
		f.writeJSON(w, http.StatusOK, map[string]any{"items": f.listBody(owner)})
	case r.Method == http.MethodGet && r.URL.Path[len(r.URL.Path)-len("/versions"):] == "/versions" && r.URL.Path[:len("/v1/skills/")] == "/v1/skills/":
		id := r.URL.Path[len("/v1/skills/") : len(r.URL.Path)-len("/versions")]
		f.versionListFor = append(f.versionListFor, id)
		for _, s := range f.skills {
			if s.id != id {
				continue
			}
			if !f.visibleToCaller(s) {
				f.writeJSON(w, http.StatusNotFound, map[string]any{"code": "NOT_FOUND", "message": "skill version not found"})
				return
			}
			f.writeJSON(w, http.StatusOK, map[string]any{"items": []map[string]any{{
				"skill_id": s.id, "version": 1, "visibility": "private",
				"digest": s.digest, "author_user_id": s.owner,
			}}})
			return
		}
		f.writeJSON(w, http.StatusNotFound, map[string]any{"code": "NOT_FOUND", "message": "skill version not found"})
	default:
		f.writeJSON(w, http.StatusNotFound, map[string]any{"code": "NOT_FOUND", "message": "no such route"})
	}
}

// listBody renders the visible, owner-filtered skills list — the same
// semantics the real ListSkills applies: visibility first, the owner
// condition beside it, for admins exactly like writers.
func (f *seedFakeServer) listBody(owner string) []map[string]any {
	items := []map[string]any{}
	for _, s := range f.skills {
		if !f.visibleToCaller(s) {
			continue
		}
		if !f.ignoreOwnerFilter && s.owner != owner {
			continue
		}
		items = append(items, map[string]any{
			"id":            s.id,
			"name":          s.name,
			"owner_user_id": s.owner,
			"owner_display": s.owner,
		})
	}
	return items
}

// seedFakeHarness wires one fake server to a Client.
func seedFakeHarness(t *testing.T, fake *seedFakeServer) *Client {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return New(srv.URL, "test-key")
}

// ─── Two owners, public and shared same-name skills ──────────────────────────

// TestSeedSkillNeverAdoptsAnotherOwnersVisibleSkill is the blocker's named
// scenario: owner B holds a skill named "spec" whose latest version is PUBLIC
// and a skill named "plan" whose latest version is SHARED with a project the
// caller belongs to — both fully visible to caller A in an unfiltered list —
// while A owns a same-namespace "review" skill. The owner-scoped lookup must
// answer "spec" and "plan" as FREE names (never B's) and resolve "review" to
// A's own identity and versions.
func TestSeedSkillNeverAdoptsAnotherOwnersVisibleSkill(t *testing.T) {
	fake := &seedFakeServer{
		t:              t,
		callerID:       "u_caller_a",
		callerRole:     "writer",
		callerProjects: []string{"alpha"},
		skills: []seedFakeSkill{
			{id: "skill_b_public", name: "spec", owner: "u_owner_b", public: true, digest: seedFakeDigest},
			{id: "skill_b_shared", name: "plan", owner: "u_owner_b", sharedWith: []string{"alpha"}, digest: seedFakeDigest},
			{id: "skill_a_review", name: "review", owner: "u_caller_a", digest: seedFakeDigest},
		},
	}
	c := seedFakeHarness(t, fake)
	ctx := context.Background()

	// B's PUBLIC same-name skill is invisible to the owner-scoped lookup: the
	// name is free, and no version listing ever touched B's skill.
	id, versions, exists, err := c.SeedSkill(ctx, "spec")
	require.NoError(t, err)
	require.False(t, exists, "a public same-name skill of another owner must never be adopted")
	require.Empty(t, id)
	require.Nil(t, versions)

	// B's SHARED same-name skill either: a grant to the caller's project makes
	// it visible, not ownable.
	id, versions, exists, err = c.SeedSkill(ctx, "plan")
	require.NoError(t, err)
	require.False(t, exists, "a shared same-name skill of another owner must never be adopted")
	require.Empty(t, id)
	require.Nil(t, versions)

	// A's own skill resolves with its exact identity and versions.
	id, versions, exists, err = c.SeedSkill(ctx, "review")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "skill_a_review", id)
	require.Len(t, versions, 1)
	require.Equal(t, 1, versions[0].Version)
	require.Equal(t, seedFakeDigest, versions[0].Digest)

	// The wire: every lookup identified the caller first and carried the
	// caller's user id as the owner filter — three name lookups, three owner
	// params, all A's.
	require.Equal(t, 3, fake.sawWhoami)
	require.Len(t, fake.ownerParams, 3)
	for _, owner := range fake.ownerParams {
		require.Equal(t, "u_caller_a", owner)
	}
	// Only A's own skill ever had its versions listed: B's skills were never
	// reached, let alone matched.
	require.Equal(t, []string{"skill_a_review"}, fake.versionListFor)
}

// TestSeedSkillPrefersOwnSameNameSkillOverVisibleForeign holds the sharper
// collision: BOTH owners hold a skill named "spec" — A privately, B publicly.
// The owner-scoped lookup resolves A's, never B's.
func TestSeedSkillPrefersOwnSameNameSkillOverVisibleForeign(t *testing.T) {
	fake := &seedFakeServer{
		t:              t,
		callerID:       "u_caller_a",
		callerRole:     "writer",
		callerProjects: []string{"alpha"},
		skills: []seedFakeSkill{
			{id: "skill_a_spec", name: "spec", owner: "u_caller_a", digest: seedFakeDigest},
			{id: "skill_b_spec", name: "spec", owner: "u_owner_b", public: true, digest: seedFakeDigest},
		},
	}
	c := seedFakeHarness(t, fake)

	id, versions, exists, err := c.SeedSkill(context.Background(), "spec")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "skill_a_spec", id, "the caller's own same-name skill is the only legal match")
	require.Len(t, versions, 1)
	require.Equal(t, []string{"skill_a_spec"}, fake.versionListFor)
	for _, owner := range fake.ownerParams {
		require.Equal(t, "u_caller_a", owner)
	}
}

// ─── Admins need an explicit target ──────────────────────────────────────────

// TestSeedSkillAdminScopesToOwnNamespaceToo holds the admin half: an unscoped
// admin's VISIBLE list is every skill in the registry, and an unscoped admin
// is also the one caller the registry would let PUBLISH into a stranger's
// skill — so the implicit name match was not just wrong, it was writable. The
// owner filter applies to admins too (the real server adds the owner
// condition unconditionally), so an admin's seed targets the ADMIN'S OWN
// namespace: B's public "spec" stays a free name, and the admin's own "ci"
// resolves.
func TestSeedSkillAdminScopesToOwnNamespaceToo(t *testing.T) {
	fake := &seedFakeServer{
		t:              t,
		callerID:       "u_admin_a",
		callerRole:     "admin",
		callerProjects: []string{},
		skills: []seedFakeSkill{
			{id: "skill_b_public", name: "spec", owner: "u_owner_b", public: true, digest: seedFakeDigest},
			{id: "skill_a_ci", name: "ci", owner: "u_admin_a", digest: seedFakeDigest},
		},
	}
	c := seedFakeHarness(t, fake)
	ctx := context.Background()

	_, _, exists, err := c.SeedSkill(ctx, "spec")
	require.NoError(t, err)
	require.False(t, exists, "an admin's seed must not implicitly target another owner's namespace")

	id, versions, exists, err := c.SeedSkill(ctx, "ci")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "skill_a_ci", id)
	require.Len(t, versions, 1)
	for _, owner := range fake.ownerParams {
		require.Equal(t, "u_admin_a", owner)
	}
}

// ─── The client's own fences ────────────────────────────────────────────────

// TestSeedSkillRefusesAMatchThatEscapedTheOwnerFilter holds the
// defense-in-depth arm: a server that ignores the owner filter and returns
// the stranger's same-name skill gets a LOUD refusal, not an adoption — the
// failure mode where the seed would publish into another owner's identity
// must be an error, whatever the server did.
func TestSeedSkillRefusesAMatchThatEscapedTheOwnerFilter(t *testing.T) {
	fake := &seedFakeServer{
		t:                 t,
		callerID:          "u_caller_a",
		callerRole:        "writer",
		callerProjects:    []string{"alpha"},
		ignoreOwnerFilter: true,
		skills: []seedFakeSkill{
			{id: "skill_b_public", name: "spec", owner: "u_owner_b", public: true, digest: seedFakeDigest},
		},
	}
	c := seedFakeHarness(t, fake)

	_, _, exists, err := c.SeedSkill(context.Background(), "spec")
	require.Error(t, err, "a match that escaped the owner filter must fail loudly, never seed into it")
	require.False(t, exists)
	require.Contains(t, err.Error(), "refusing to cross owner namespaces")
	require.Contains(t, err.Error(), "u_owner_b")
	require.Empty(t, fake.versionListFor, "the foreign skill's versions must never be listed")
}

// TestSeedSkillFailsClosedWithoutCallerIdentity holds the no-fallback rule:
// when the caller cannot be identified, the lookup FAILS — it never degrades
// to the unfiltered name match, which is the vulnerability itself.
func TestSeedSkillFailsClosedWithoutCallerIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/users/me":
			// An identity response with no user_id.
			_, _ = w.Write([]byte(`{"user_id":"","role":"writer"}`))
		case "/v1/skills":
			t.Errorf("the skills list must never be reached without a resolved caller identity")
			_, _ = w.Write([]byte(`{"items":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "test-key")

	_, _, exists, err := c.SeedSkill(context.Background(), "spec")
	require.Error(t, err)
	require.False(t, exists)
	require.Contains(t, err.Error(), "did not identify the caller")
}

// TestSeedSkillKeepsTheOwnerFilterAcrossCursorPages pins the paging shape:
// the owner parameter rides every page request beside the cursor, so a
// multi-page registry cannot fall back to an unfiltered page mid-walk. The
// first page answers empty with a next_cursor; the match lives on page two.
func TestSeedSkillKeepsTheOwnerFilterAcrossCursorPages(t *testing.T) {
	ownerParams := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/users/me":
			_, _ = w.Write([]byte(`{"user_id":"u_caller_a","role":"writer"}`))
		case r.URL.Path == "/v1/skills" && r.URL.Query().Get("cursor") == "":
			ownerParams = append(ownerParams, r.URL.Query().Get("owner"))
			_, _ = w.Write([]byte(`{"items":[],"next_cursor":"page2"}`))
		case r.URL.Path == "/v1/skills":
			ownerParams = append(ownerParams, r.URL.Query().Get("owner"))
			_, _ = w.Write([]byte(`{"items":[{"id":"skill_a_page","name":"plan","owner_user_id":"u_caller_a"}]}`))
		case r.URL.Path == "/v1/skills/skill_a_page/versions":
			_, _ = w.Write([]byte(fmtVersionsBody()))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "test-key")

	id, versions, exists, err := c.SeedSkill(context.Background(), "plan")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "skill_a_page", id)
	require.Len(t, versions, 1)
	require.Equal(t, seedFakeDigest, versions[0].Digest)
	require.Len(t, ownerParams, 2, "the paged lookup must issue exactly two list requests")
	for _, owner := range ownerParams {
		require.Equal(t, "u_caller_a", owner, "the owner filter must ride every page request")
	}
}

// fmtVersionsBody renders the version-list JSON with the shared fake digest.
func fmtVersionsBody() string {
	return `{"items":[{"skill_id":"skill_a_page","version":1,"visibility":"private","digest":"` +
		seedFakeDigest + `","author_user_id":"u_caller_a"}]}`
}
