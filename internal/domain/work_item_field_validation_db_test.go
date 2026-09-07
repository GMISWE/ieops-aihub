package domain

// aihub#396 — a DB CHECK is not a caller-facing answer.
//
// priority, source, the labels cap, the content length and parent_work_item_id
// were enforced by nothing in Go. The INSERT hit the constraint, and every
// non-class-40 SQLSTATE goes through dbErrCause -> ErrInternalError, so the
// caller got:
//
//	500 INTERNAL_ERROR  failed to insert work_item :: ERROR: new row for
//	relation "work_items" violates check constraint
//	"work_items_priority_check" (SQLSTATE 23514)
//
// and for a parent that does not exist, the FK version of the same thing
// (23503). Three separate costs, only the first cosmetic: a 500 tells the
// caller to RETRY something that can never succeed; it tells an operator the
// server is broken when the request was; and the legal values appear nowhere,
// so the caller cannot self-correct. For an LLM caller the third is the
// expensive one — it guesses again.
//
// ─── Why this test needs a database, when the checks it asserts do not ─────
//
// The Go validation runs before any query, so a nil-pool unit test can prove
// the 400. It CANNOT prove the thing this wi is about: that the answer used to
// be a 500. Without a database, the pre-fix tree panics on the nil pool instead
// of reaching the constraint, so "red before, green after" would be
// unmeasurable — the pre-fix failure would be an artefact of the fixture rather
// than the defect. With a real database the RED arm is the actual 500 the
// production path returned. The pure-unit half lives in work_item_fields_test.go
// and covers the vocabularies against the migrations; this file covers the
// end-to-end status code.
//
// ─── The oracle arm is not decoration ─────────────────────────────────────
//
// parent_work_item_id becomes resolvable by SLUG, which turns the identifier
// into the walkable `<project>#<seq>` namespace. resolveBlockedByRef documents
// at length why that makes any per-entry answer that varies with existence an
// enumeration oracle over every project on the server; aihub#357 shipped
// exactly that bug through this door. So there is a subtest asserting that a
// work item in a project the caller cannot see is INDISTINGUISHABLE from one
// that does not exist — same code, same status, and no project name in the
// message. A fix that resolved the slug without that property would be a
// regression dressed as a feature.
//
// Gated on AIHUB_TEST_DB, like every DB test in this package. CI runs it from a
// dedicated step that applies migrations first and greps for each subtest's
// `--- PASS`, because `go test` exits 0 when everything SKIPs.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run TestWorkItemFieldValidation -v -count=1
//
// ONE test function with subtests, following aihub#334: internal/citest/dbtestcov
// ratchets on the NUMBER of DB-gated functions, and one function per arm would
// move that ratchet a dozen times for one guard. The per-arm coverage claim
// lives in the CI step's `--- PASS:` greps.

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// longRunes returns a string of n multi-byte runes.
//
// Multi-byte deliberately: the DB CHECK is `length(content) <= 20000`, which
// counts CHARACTERS, so a byte-counting guard in Go would reject this string at
// a third of the length the database accepts. That is the fail-closed-too-wide
// direction — the guard refusing legal input — and it is invisible to an
// ASCII-only fixture.
func longRunes(n int) string {
	return strings.Repeat("界", n)
}

// asProblemField pulls the `field` key out of an AihubError's details, so an
// assertion can require the answer to NAME the offending field rather than
// merely be a 400. A 400 that does not say which field is wrong leaves a caller
// with twenty fields no better off than the 500 did.
func asProblemField(t *testing.T, err *AihubError) string {
	t.Helper()
	require.NotNil(t, err)
	details, ok := err.Details.(map[string]any)
	require.True(t, ok, "details must be a map so a caller can read them without parsing prose; got %T", err.Details)
	field, ok := details["field"].(string)
	require.True(t, ok, "details must carry a `field` key naming the offending parameter; got %#v", details)
	return field
}

func TestWorkItemFieldValidationAnswersFourHundredNamingTheField(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	u := testUser(t, pool)
	project := testProject(t, pool, u)

	// Every goal below is deliberately dissimilar from the others: CreateWorkItem
	// runs goal-similarity dedup against live wis in the same project and would
	// reject a close match with a 409 before reaching the code under test — which
	// would make a subtest pass for the wrong reason.
	badCreates := []struct {
		name      string
		wantField string
		mutate    func(*CreateWorkItemRequest)
	}{
		{
			name:      "priority_outside_the_vocabulary",
			wantField: "priority",
			// "P1" rather than gibberish: it is a real priority scheme from another
			// tracker, so it is what a caller actually sends by mistake.
			mutate: func(r *CreateWorkItemRequest) { r.Priority = "P1" },
		},
		{
			name:      "source_outside_the_vocabulary",
			wantField: "source",
			// "jira" is the trap the schema set: the legal value is `sync_jira`, and
			// the old description ("Source reference") read as free text.
			mutate: func(r *CreateWorkItemRequest) { r.Source = "jira" },
		},
		{
			name:      "labels_over_the_cap",
			wantField: "labels",
			mutate: func(r *CreateWorkItemRequest) {
				labels := make([]string, maxWorkItemLabels+1)
				for i := range labels {
					labels[i] = "l" + string(rune('a'+i%26)) + string(rune('0'+i/26))
				}
				r.Labels = labels
			},
		},
		{
			name:      "content_over_the_character_limit",
			wantField: "content",
			mutate: func(r *CreateWorkItemRequest) {
				body := longRunes(maxWorkItemContentRunes + 1)
				r.Content = &body
			},
		},
	}

	for i, tc := range badCreates {
		t.Run("create_"+tc.name, func(t *testing.T) {
			req := &CreateWorkItemRequest{
				Project: project,
				Goal:    createProbeGoal(i),
				Source:  "human",
			}
			tc.mutate(req)
			wi, err := CreateWorkItem(ctx, pool, req, u, u, nil, "")
			require.NotNil(t, err,
				"CreateWorkItem accepted an illegal %s; wi=%+v", tc.wantField, wi)
			assert.Equal(t, 400, err.HTTPStatus,
				"a value the DB CHECK will refuse must be a 400 at the point of entry, not a 500 "+
					"from the constraint: a 500 tells the caller to retry something that can never "+
					"succeed and carries no legal values to retry WITH. Got %s: %s",
				err.Code, err.Message)
			assert.Equal(t, tc.wantField, asProblemField(t, err))
			assert.Contains(t, err.Message, tc.wantField,
				"the message must name the field too — details are not surfaced by every client")
		})
	}

	// ── The same limits on the UPDATE path ────────────────────────────────────
	//
	// UpdateWorkItemRequest binds priority, labels and content but NOT source or
	// parent_work_item_id, so those two are create-only by construction and are
	// not asserted here. That is a fact about the request struct rather than a
	// gap in coverage; work_item_fields_test.go pins it so it cannot change
	// silently.
	victim, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project: project,
		Goal:    "catalogue the disused rolling-stock inventory for the northern depot",
		Source:  "human",
	}, u, u, nil, "")
	require.Nil(t, aerr, "seeding the update victim must succeed; got %+v", aerr)

	badUpdates := []struct {
		name      string
		wantField string
		req       *UpdateWorkItemRequest
	}{
		{
			name:      "priority_outside_the_vocabulary",
			wantField: "priority",
			req:       &UpdateWorkItemRequest{Priority: strPtr("P1")},
		},
		{
			name:      "labels_over_the_cap",
			wantField: "labels",
			req: func() *UpdateWorkItemRequest {
				labels := make([]string, maxWorkItemLabels+1)
				for i := range labels {
					labels[i] = "u" + string(rune('a'+i%26)) + string(rune('0'+i/26))
				}
				return &UpdateWorkItemRequest{Labels: labels}
			}(),
		},
		{
			name:      "content_over_the_character_limit",
			wantField: "content",
			req:       &UpdateWorkItemRequest{Content: strPtr(longRunes(maxWorkItemContentRunes + 1))},
		},
	}

	for _, tc := range badUpdates {
		t.Run("update_"+tc.name, func(t *testing.T) {
			wi, err := UpdateWorkItem(ctx, pool, victim.ID, u, "", nil, tc.req)
			require.NotNil(t, err,
				"UpdateWorkItem accepted an illegal %s; wi=%+v", tc.wantField, wi)
			assert.Equal(t, 400, err.HTTPStatus,
				"got %s: %s", err.Code, err.Message)
			assert.Equal(t, tc.wantField, asProblemField(t, err))
		})
	}

	// ── A legal value must still be accepted ─────────────────────────────────
	//
	// The CONTROL for every arm above. A validator that refused everything would
	// satisfy all of them, and the cheap fix for a guard that rejects correct
	// input is to delete the guard — so the positive case is asserted explicitly
	// and for every member of both vocabularies, not for one sample of each.
	t.Run("control_every_legal_priority_and_source_is_accepted", func(t *testing.T) {
		for _, p := range WorkItemPriorityList() {
			for _, s := range WorkItemSourceList() {
				err := validateWorkItemPriority(p)
				require.Nil(t, err, "priority %q is in the vocabulary and was rejected: %+v", p, err)
				err = validateWorkItemSource(s)
				require.Nil(t, err, "source %q is in the vocabulary and was rejected: %+v", s, err)
			}
		}
		// And at the boundary of each limit, since an off-by-one is the likeliest
		// way a cap starts refusing legal input.
		require.Nil(t, validateWorkItemLabels(make([]string, maxWorkItemLabels)),
			"exactly %d labels is legal — the DB CHECK is <=, not <", maxWorkItemLabels)
		atLimit := longRunes(maxWorkItemContentRunes)
		require.Nil(t, validateWorkItemContent(&atLimit),
			"exactly %d CHARACTERS is legal; note this string is 3x that in BYTES, which is the "+
				"case a byte-counting guard would wrongly refuse", maxWorkItemContentRunes)
	})

	// ── parent_work_item_id: resolved like blocked_by, and no oracle ─────────

	parent, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project: project,
		Goal:    "retire the legacy pneumatic signalling loop on platform four",
		Source:  "human",
	}, u, u, nil, "")
	require.Nil(t, aerr, "seeding the parent must succeed; got %+v", aerr)
	require.NotEmpty(t, parent.Slug, "the parent needs a slug for the resolution arm to mean anything")

	t.Run("parent_accepts_a_slug", func(t *testing.T) {
		child, err := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
			Project:          project,
			Goal:             "measure ballast settlement under the western approach viaduct",
			Source:           "human",
			ParentWorkItemID: strPtr(parent.Slug),
		}, u, u, nil, "")
		require.Nil(t, err,
			"a slug must resolve, as it does for blocked_by; got %+v", err)
		require.NotNil(t, child.ParentWorkItemID,
			"the parent was dropped rather than resolved")
		assert.Equal(t, parent.ID, *child.ParentWorkItemID,
			"the CANONICAL id must be stored, not the slug — the column is a FK to work_items(id), "+
				"so storing the slug is the 23503 this wi is about")
	})

	// A blank parent means ABSENT, not "". Regression arm for a hole the fix
	// could easily have left: a `!= ""` guard that skips resolution passes the
	// empty string straight into a foreign-key column, which is a 23503 and so
	// the same 500 this wi removes, reachable by sending `""`.
	t.Run("blank_parent_is_treated_as_absent", func(t *testing.T) {
		// Distinct, mutually dissimilar goals: two near-identical strings trip the
		// goal-similarity dedup at 92% and the arm then fails with a 409 that has
		// nothing to do with the parent field.
		for blank, goal := range map[string]string{
			"":    "grind the switch blades on the freight-only chord",
			"   ": "photograph the semaphore finials before the repaint",
		} {
			child, err := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
				Project:          project,
				Goal:             goal,
				Source:           "human",
				ParentWorkItemID: strPtr(blank),
			}, u, u, nil, "")
			require.Nil(t, err,
				"a blank parent_work_item_id (%q) must be read as absent, not passed to the "+
					"foreign key; got %+v", blank, err)
			assert.Nil(t, child.ParentWorkItemID,
				"a blank parent must store NULL, not %q", blank)
		}
	})

	t.Run("parent_that_does_not_exist_is_404", func(t *testing.T) {
		wi, err := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
			Project:          project,
			Goal:             "audit the disused water tower foundations near the yard throat",
			Source:           "human",
			ParentWorkItemID: strPtr(project + "#999999"),
		}, u, u, nil, "")
		require.NotNil(t, err, "an unknown parent was accepted; wi=%+v", wi)
		assert.Equal(t, 404, err.HTTPStatus,
			"an unresolvable reference is NOT_FOUND, not a 500 from the foreign key. Got %s: %s",
			err.Code, err.Message)
		assert.Contains(t, err.Message, "parent_work_item_id",
			"the message must name the field")
	})

	// THE oracle arm. A work item in a project the caller cannot see must be
	// indistinguishable from one that does not exist — otherwise the walkable
	// <project>#<seq> namespace enumerates every project on the server, which is
	// verbatim the leak resolveBlockedByRef exists to close and aihub#357 shipped.
	t.Run("parent_in_an_invisible_project_is_the_same_404", func(t *testing.T) {
		// ⚠️ projects.name is CHECK (name ~ '^[a-z][a-z0-9_-]{0,39}$'), so the
		// second project cannot be testname.Sanitize(t.Name()) with a suffix —
		// this test's name already fills the 40 characters and the seed fails with
		// a 23514 that has nothing to do with what is under test. A short fixed
		// name is enough: nothing else in this package uses it, and the arms below
		// reset it first.
		const otherOwner = "u_aihub396_other_owner"
		const otherProject = "p_aihub396_other"
		mustExec(t, pool, `INSERT INTO users(id,email,display_name) VALUES('`+otherOwner+`','`+otherOwner+`@test.local','`+otherOwner+`') ON CONFLICT (id) DO NOTHING`)
		mustExec(t, pool, `INSERT INTO projects(name,owner_user_id) VALUES('`+otherProject+`','`+otherOwner+`') ON CONFLICT (name) DO NOTHING`)
		resetTestProject(t, pool, otherProject)

		hidden, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
			Project: otherProject,
			Goal:    "recalibrate the hump yard retarder pressure schedule",
			Source:  "human",
		}, otherOwner, otherOwner, nil, "")
		require.Nil(t, aerr, "seeding the hidden parent must succeed; got %+v", aerr)

		// BOTH probes name the same invisible project, differing only in the seq:
		// one work item that exists and one that does not. That is what makes the
		// two answers literally comparable — a probe in a DIFFERENT project would
		// differ in the project name too, and the messages could not be compared
		// without deciding which differences are allowed, which is exactly the
		// judgement a leak hides behind.
		const absentRef = otherProject + "#999999"
		hiddenRef := hidden.Slug
		require.NotEqual(t, absentRef, hiddenRef, "the two probes must name different work items")

		hiddenErr := createWithParent(t, ctx, pool, project, u,
			"survey the abandoned interlocking tower for asbestos", hiddenRef)
		absentErr := createWithParent(t, ctx, pool, project, u,
			"replace the corroded catenary droppers on the loop line", absentRef)

		require.NotNil(t, hiddenErr, "a parent in an invisible project must be refused")
		require.NotNil(t, absentErr, "a parent that does not exist must be refused")
		assert.Equal(t, absentErr.HTTPStatus, hiddenErr.HTTPStatus,
			"a hidden work item answers %d and a nonexistent one answers %d — that one-bit "+
				"difference enumerates every project on the server through the walkable "+
				"<project>#<seq> namespace",
			hiddenErr.HTTPStatus, absentErr.HTTPStatus)
		assert.Equal(t, absentErr.Code, hiddenErr.Code,
			"the error CODE differs between hidden and absent — same oracle, one layer up")

		// The messages must be identical once each caller's OWN reference is
		// substituted out. Echoing back the string the caller sent is not a leak
		// (resolveVisibleRefOnTx says so, and the reference necessarily contains
		// the project name the caller already typed); anything else that differs
		// between these two answers is derived from the row, and that is a leak.
		assert.Equal(t,
			strings.ReplaceAll(absentErr.Message, absentRef, "<ref>"),
			strings.ReplaceAll(hiddenErr.Message, hiddenRef, "<ref>"),
			"the two messages differ by more than the caller's own reference, so the difference "+
				"comes from the row and distinguishes hidden from absent")
		assert.NotContains(t, hiddenErr.Message, hidden.ID,
			"the message leaks the hidden work item's canonical id — that IS derived from the row")
	})

	// ── The 409 duplicate must report the matched item's REAL status ─────────
	//
	// details.existing.status was the literal string "active", which is not a
	// member of the work_items.status CHECK at all — so a caller deciding what to
	// do about the duplicate (claim it? it is already running. requeue it? it is
	// wrapped.) was handed a value that cannot be compared with anything. Folded
	// in from aihub#397.
	t.Run("duplicate_409_reports_the_real_status", func(t *testing.T) {
		const goal = "decommission the redundant tandem compressor on shed road three"
		original, err := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
			Project: project,
			Goal:    goal,
			Source:  "human",
		}, u, u, nil, "")
		require.Nil(t, err, "seeding the duplicate target must succeed; got %+v", err)

		// Move it off the default so "queued" cannot pass by coincidence: the
		// assertion has to distinguish the real status from ANY fixed string, and
		// a fixture left at the default would let `"queued"` hard-coded pass.
		mustExec(t, pool, `UPDATE work_items SET status='paused' WHERE id='`+original.ID+`'`)

		dup, err := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
			Project: project,
			Goal:    goal,
			Source:  "human",
		}, u, u, nil, "")
		require.NotNil(t, err, "an identical goal must be refused as a duplicate; wi=%+v", dup)
		require.Equal(t, ErrConflictDuplicate, err.Code, "got %s: %s", err.Code, err.Message)

		details, ok := err.Details.(map[string]any)
		require.True(t, ok, "details must be a map; got %T", err.Details)
		existing, ok := details["existing"].(map[string]any)
		require.True(t, ok, "details.existing must be a map; got %#v", details)
		assert.Equal(t, "paused", existing["status"],
			"details.existing.status must be the matched work item's REAL status. It was the "+
				"literal \"active\", which is not a member of the work_items.status CHECK "+
				"(queued/running/paused/blocked/wrapped/failed/cancelled), so a caller could not "+
				"compare it with anything (aihub#397)")
		assert.Equal(t, original.ID, existing["id"], "details.existing.id must be the matched item")
	})
}

// createProbeGoal returns a goal unique and dissimilar enough that dedup does
// not fire between arms.
func createProbeGoal(i int) string {
	goals := []string{
		"relocate the permanent way materials compound to siding twelve",
		"replace the timber sleepers on the goods loop headshunt",
		"instrument the culvert under the branch line for scour",
		"rebuild the hydraulic buffer stops at the terminal platform",
	}
	return goals[i%len(goals)]
}

// createWithParent runs a create whose only interesting field is the parent, and
// returns just the error — the two oracle probes differ in nothing else, so
// sharing the call keeps them literally comparable.
func createWithParent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, project, user, goal, parent string) *AihubError {
	t.Helper()
	_, err := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project:          project,
		Goal:             goal,
		Source:           "human",
		ParentWorkItemID: &parent,
	}, user, user, nil, "")
	return err
}
