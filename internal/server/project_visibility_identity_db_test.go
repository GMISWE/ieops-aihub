package server

// DB-gated byte-identity acceptance tests for aihub#377 — the half that cannot
// run in-process.
//
// project_visibility_identity_test.go covers every handler that reaches its
// store through an injectable seam. The endpoints here have none: they call
// domain.GetWorkItem / domain.GetMemoryByID / ListEvents / ListDependencies with
// a live *pgxpool.Pool, so both arms need real rows. They are also the endpoints
// where the leak mattered most, because a work item is addressed by
// `<project>#<seq>` — a two-token namespace counting from 1, which anyone can
// walk.
//
// THE CRITERION is the one aihub#363's recall_work_item_slug_db_test.go
// established and this file reuses: take the response for something that DOES
// NOT EXIST as the reference, and require the response for something INVISIBLE
// to equal it — same status, same body. Not "both are 4xx". A shared status with
// a per-endpoint message is still one distinguishable bit.
//
// 🔴 THE POSITIVE CONTROLS ARE NOT OPTIONAL. Every negative assertion in this
// file is satisfied by a server that refuses everybody, so "all refusals are
// identical" cannot by itself distinguish a fix from an outage. The arms at the
// end assert that a member still reads their own project's work item, step,
// events, memory and dependencies; that an admin is unchanged; and that a member
// with an insufficient ROLE still gets an explanatory 403 rather than the shared
// 404. Delete those and the rest of the file starts proving nothing.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/server/ -run TestProjectVisibilityIdentityOverHTTP -v -count=1

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// get issues a GET as the given key and returns status + raw body.
func (s *visStack) get(t *testing.T, key, path string) (int, string) {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, s.url+path, nil)
	require.NoError(t, err)
	r.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(raw)
}

// postJSON issues a POST with a JSON body as the given key.
func (s *visStack) postJSON(t *testing.T, key, path, body string) (int, string) {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, s.url+path, strings.NewReader(body))
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

// seedMemoryAsAdmin puts a memory in a project over HTTP. Admin bypasses the
// project gate, so this works for projB, which the outsider cannot write.
func (s *visStack) seedMemoryAsAdmin(t *testing.T, project, content string) string {
	t.Helper()
	status, body := s.postJSON(t, s.adminKey, "/v1/memories", fmt.Sprintf(
		`{"project":%q,"type":"fact.note","content":%q,"visibility":"project"}`, project, content))
	require.Equal(t, http.StatusCreated, status, "seeding a memory in %s failed: %s", project, body)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &decoded))
	id, _ := decoded["memory_id"].(string)
	if id == "" {
		id, _ = decoded["id"].(string)
	}
	require.NotEmpty(t, id, "no memory id in %s", body)
	return id
}

func TestProjectVisibilityIdentityOverHTTP(t *testing.T) {
	s := newVisStack(t)

	// The outsider holds maintainer on projA and NOTHING on projB. Everything
	// below is asked as the outsider unless stated.
	secretID, secretSlug := s.seedWIAsAdmin(t, s.projB, "decommission the legacy billing cron")
	secretMem := s.seedMemoryAsAdmin(t, s.projB, "the projB tenant sharding key")

	ownID, ownSlug := s.seedWIAsAdmin(t, s.projA, "a work item the outsider may read")
	ownMem := s.seedMemoryAsAdmin(t, s.projA, "a memory the outsider may read")

	// Fixture control, first, because every arm below is vacuous without it: the
	// outsider must genuinely be unable to read projB by any honest route. If this
	// is wrong the negative arms pass by accident.
	t.Run("fixture_outsider_has_no_access_to_projB", func(t *testing.T) {
		status, body := s.get(t, s.outsiderKey, "/v1/memories?project="+s.projB)
		require.Equal(t, http.StatusNotFound, status,
			"the outsider is supposed to hold NO role on %s; body %s", s.projB, body)
	})

	// ── The identity arms. reference = a reference that names nothing. ────────
	for _, arm := range []struct {
		name              string
		absent, invisible string
	}{
		{
			name:      "identity_work_item_by_canonical_id",
			absent:    "/v1/work_items/wi_thisidwasnevermintedanywhere",
			invisible: "/v1/work_items/" + secretID,
		},
		{
			// 🔴 The arm that matters most. A canonical id is unguessable; a slug
			// is <project>#<seq> counting from 1, so this is the walkable one.
			name:      "identity_work_item_by_slug",
			absent:    "/v1/work_items/" + s.projB + "%23999999",
			invisible: "/v1/work_items/" + strings.Replace(secretSlug, "#", "%23", 1),
		},
		{
			name:      "identity_step",
			absent:    "/v1/work_items/wi_thisidwasnevermintedanywhere/step",
			invisible: "/v1/work_items/" + secretID + "/step",
		},
		{
			name:      "identity_events",
			absent:    "/v1/events?work_item_id=wi_thisidwasnevermintedanywhere",
			invisible: "/v1/events?work_item_id=" + secretID,
		},
		{
			name:      "identity_dependencies",
			absent:    "/v1/dependencies?work_item_id=wi_thisidwasnevermintedanywhere",
			invisible: "/v1/dependencies?work_item_id=" + secretID,
		},
		{
			name:      "identity_memory",
			absent:    "/v1/memories/mem_thisidwasnevermintedanywhere",
			invisible: "/v1/memories/" + secretMem,
		},
	} {
		t.Run(arm.name, func(t *testing.T) {
			absentStatus, absentBody := s.get(t, s.outsiderKey, arm.absent)
			invisibleStatus, invisibleBody := s.get(t, s.outsiderKey, arm.invisible)

			assert.Equal(t, absentStatus, invisibleStatus,
				"status differs (absent=%d invisible=%d): one bit enumerates %s",
				absentStatus, invisibleStatus, s.projB)
			assert.JSONEq(t, absentBody, invisibleBody,
				"an existing-but-invisible object answered differently from an absent one, "+
					"so the two are distinguishable:\n  absent   : %s\n  invisible: %s",
				absentBody, invisibleBody)
			assert.NotContains(t, invisibleBody, s.projB,
				"the refusal names the project the caller may not see: %s", invisibleBody)
			assert.NotContains(t, invisibleBody, "billing cron",
				"the refusal leaked the work item's goal: %s", invisibleBody)
		})
	}

	// ── Invariant 2: the edge belongs to the side that declared it. ───────────
	t.Run("invariant2_edge_shows_slug_and_no_access", func(t *testing.T) {
		// Created by the admin: the outsider could not name a projB work item even
		// if they wanted to, which is invariant 3 (aihub#357) doing its job.
		status, body := s.createWI(t, s.adminKey, fmt.Sprintf(
			`{"project":%q,"goal":"depends on something in the other project","blocked_by":[%q]}`,
			s.projA, secretID))
		require.Equal(t, http.StatusCreated, status, "seeding the edge failed: %v", body)
		edgeWI, _ := body["id"].(string)
		require.NotEmpty(t, edgeWI)

		depStatus, depBody := s.get(t, s.outsiderKey, "/v1/dependencies?work_item_id="+edgeWI)
		require.Equal(t, http.StatusOK, depStatus, "body %s", depBody)

		var deps struct {
			BlockedBy []struct {
				ID         string  `json:"id"`
				Slug       *string `json:"slug"`
				Accessible bool    `json:"accessible"`
			} `json:"blocked_by"`
		}
		require.NoError(t, json.Unmarshal([]byte(depBody), &deps))
		require.Len(t, deps.BlockedBy, 1, "the edge vanished; invariant 2 says it is A's own data: %s", depBody)

		e := deps.BlockedBy[0]
		assert.False(t, e.Accessible, "the far end is in a project the caller cannot see")
		require.NotNil(t, e.Slug, "invariant 2 requires the slug: the owner of A must be able "+
			"to read their own record. Body: %s", depBody)
		assert.Equal(t, secretSlug, *e.Slug)
		assert.Equal(t, "hidden", e.ID, "the canonical id stays withheld")
		assert.NotContains(t, depBody, "billing cron", "the far end's goal must not travel with the edge")
	})

	// ── 🔴 POSITIVE CONTROLS. Green before AND after; they catch over-blocking. ──
	t.Run("positive_member_reads_own_work_item", func(t *testing.T) {
		status, body := s.get(t, s.outsiderKey, "/v1/work_items/"+ownID)
		require.Equal(t, http.StatusOK, status, "body %s", body)
		assert.Contains(t, body, "a work item the outsider may read")
	})
	t.Run("positive_member_reads_own_work_item_by_slug", func(t *testing.T) {
		status, body := s.get(t, s.outsiderKey,
			"/v1/work_items/"+strings.Replace(ownSlug, "#", "%23", 1))
		require.Equal(t, http.StatusOK, status, "body %s", body)
	})
	t.Run("positive_member_reads_own_step", func(t *testing.T) {
		status, body := s.get(t, s.outsiderKey, "/v1/work_items/"+ownID+"/step")
		require.Equal(t, http.StatusOK, status, "body %s", body)
	})
	t.Run("positive_member_reads_own_events", func(t *testing.T) {
		status, body := s.get(t, s.outsiderKey, "/v1/events?work_item_id="+ownID)
		require.Equal(t, http.StatusOK, status, "body %s", body)
	})
	t.Run("positive_member_reads_own_dependencies", func(t *testing.T) {
		status, body := s.get(t, s.outsiderKey, "/v1/dependencies?work_item_id="+ownID)
		require.Equal(t, http.StatusOK, status, "body %s", body)
	})
	t.Run("positive_member_reads_own_memory", func(t *testing.T) {
		status, body := s.get(t, s.outsiderKey, "/v1/memories/"+ownMem)
		require.Equal(t, http.StatusOK, status, "body %s", body)
		assert.Contains(t, body, "a memory the outsider may read")
	})

	t.Run("positive_admin_unchanged", func(t *testing.T) {
		status, body := s.get(t, s.adminKey, "/v1/work_items/"+secretID)
		require.Equal(t, http.StatusOK, status, "body %s", body)
		assert.Contains(t, body, "billing cron",
			"an admin must still see the content; if this fails the fix over-blocked")
	})

	// 🔴 THE DETECTOR for "did you just switch the feature off". A member whose
	// ROLE is insufficient keeps an explanatory 403 — the invariant's first clause
	// is that a user who IS in a project can see everything about it, so hiding
	// the project from its own members is not a fix.
	//
	// 🔴 CLASSIFICATION, because this is the ONLY StatusForbidden left in any
	// *_db_test.go in this package and the next reader will find it while
	// sweeping for ones aihub#377 missed. IT IS NOT A MISSED ONE. Five stale 403
	// expectations were moved to 404 in that change
	// (blocked_by_visibility / recall_work_item_slug / remember_work_item_scope /
	// routes_memory_redact_idor / ui_handlers_wi_watching); this one is
	// deliberately 403 and must stay 403.
	//
	// The distinction is membership, not endpoint: viewerKey HOLDS viewer on
	// projB. A non-member gets the shared 404; a member short of the needed role
	// gets a 403 that names the role. Turning this into 404 would hide a project
	// from its own members — which the invariant's first clause forbids — and
	// would delete the only control that separates "denials are now uniform"
	// from "authorization is broken and everything is denied".
	t.Run("positive_insufficient_role_still_explains", func(t *testing.T) {
		// viewerKey holds viewer on projB; writing a memory needs writer.
		status, body := s.postJSON(t, s.viewerKey, "/v1/memories", fmt.Sprintf(
			`{"project":%q,"type":"fact.note","content":"x","visibility":"project"}`, s.projB))
		require.Equal(t, http.StatusForbidden, status,
			"a MEMBER short of the required role must still get 403, not the shared 404; body %s", body)
		assert.Contains(t, body, "writer", "the 403 must still say which role is needed: %s", body)
		assert.NotContains(t, body, notVisibleMessage,
			"a member's role shortfall must not be reported with the not-visible wording: %s", body)
	})
}
