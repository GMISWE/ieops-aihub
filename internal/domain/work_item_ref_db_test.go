package domain

// DB-gated behavioural gate for aihub#402: GetWorkItem must resolve a work item
// by id OR by slug, with no prefix dispatch between them.
//
// # The defect
//
// GetWorkItem chose ONE column by testing a `wi_` prefix: id if present, slug
// otherwise. Project names are validated by `^[a-z][a-z0-9_-]{0,39}$`, which
// does not reserve `wi_`, and slug is `project || '#' || seq` (migration 0002).
// So a project named `wi_lab` produces slugs `wi_lab#1` that took the ID branch
// and were looked up in the id column, where they never appear — a 404 for a
// work item that exists, from every caller of GetWorkItem, while
// resolveBlockedByRef, ResolveVisibleWorkItemRef and buildListWorkItemsWhere all
// resolved it correctly because they take the union.
//
// Latent when it was reported: pf_list_projects showed 10 visible projects and
// none was `wi_`-prefixed. Nothing reserves the prefix, so it stays reachable by
// creating one project.
//
// # Why the arms are shaped this way
//
// The reported bug is one arm. The other three exist because a fix in this
// direction is a WIDENING — the query goes from one column to two — and widening
// has its own failure mode: matching things it should not. Three controls pin
// that. The last one is the important one: without "an absent ref still 404s", a
// mutant returning the table's first row would pass every other arm.
//
//	Run:
//	  AIHUB_TEST_DB=postgres://postgres:testpass@127.0.0.1:15493/aihub_test?sslmode=disable \
//	    go test ./internal/domain/ -run TestGetWorkItemResolvesIDOrSlug -count=1 -v

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
)

// wiPrefixedTestProject seeds a project whose name STARTS WITH `wi_`, which is
// the whole fixture: its work items' generated slugs then begin with the prefix
// the old code read as "this is a canonical id".
//
// Not testProject, which hardcodes a `p_` prefix. The name still has to satisfy
// projectNameRe — `wi_` is 3 characters and testname.Sanitize returns at most
// 37, so the result is at most the 40 the regex allows.
func wiPrefixedTestProject(t *testing.T, pool *pgxpool.Pool, ownerUserID string) string {
	t.Helper()
	proj := "wi_" + testname.Sanitize(t.Name())
	if !projectNameRe.MatchString(proj) {
		t.Fatalf("fixture project name %q does not satisfy projectNameRe; the arm cannot run", proj)
	}
	mustExec(t, pool, `INSERT INTO projects(name,owner_user_id) VALUES('`+proj+`','`+ownerUserID+
		`') ON CONFLICT (name) DO NOTHING`)
	resetTestProject(t, pool, proj)
	return proj
}

func seedRefWI(t *testing.T, pool *pgxpool.Pool, proj, uid, goal string) *WorkItem {
	t.Helper()
	wiType := "chore"
	wi, aerr := CreateWorkItem(context.Background(), pool, &CreateWorkItemRequest{
		Project: proj, Goal: goal, Scenario: "coding", WIType: &wiType,
		Source: "human", ForceCreate: true, ForceReason: "aihub#402 ref-resolution fixture",
	}, uid, "tester", nil, "")
	if aerr != nil {
		t.Fatalf("CreateWorkItem in %s: %v", proj, aerr)
	}
	return wi
}

// TestGetWorkItemResolvesIDOrSlug is the gate named in aihub#402's owner
// decision: "DB test with a project whose name starts with wi_ and a work item
// whose slug resolves through GetWorkItem — red on the unfixed tree."
func TestGetWorkItemResolvesIDOrSlug(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	t.Run("slug_of_a_wi_prefixed_project_resolves", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := wiPrefixedTestProject(t, pool, uid)
		wi := seedRefWI(t, pool, proj, uid, "reachable only through the union")

		// The fixture's whole point, asserted rather than assumed: this slug
		// really does start with the prefix the old code dispatched on.
		if len(wi.Slug) < 3 || wi.Slug[:3] != "wi_" {
			t.Fatalf("fixture slug %q does not start with \"wi_\"; this arm would pass "+
				"on the unfixed tree and prove nothing", wi.Slug)
		}

		got, aerr := GetWorkItem(ctx, pool, wi.Slug)
		if aerr != nil {
			t.Fatalf("GetWorkItem(%q) = %v; a slug that begins with \"wi_\" was looked up in the "+
				"id column and never found, so an existing work item 404s", wi.Slug, aerr)
		}
		if got.ID != wi.ID {
			t.Errorf("GetWorkItem(%q) returned id %q, want %q", wi.Slug, got.ID, wi.ID)
		}
	})

	t.Run("control_canonical_id_still_resolves", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := wiPrefixedTestProject(t, pool, uid)
		wi := seedRefWI(t, pool, proj, uid, "resolved by canonical id")

		got, aerr := GetWorkItem(ctx, pool, wi.ID)
		if aerr != nil {
			t.Fatalf("GetWorkItem(%q) by canonical id = %v; the union must not cost the id path", wi.ID, aerr)
		}
		if got.Slug != wi.Slug {
			t.Errorf("GetWorkItem(%q) returned slug %q, want %q", wi.ID, got.Slug, wi.Slug)
		}
	})

	t.Run("control_slug_of_an_ordinary_project_still_resolves", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid) // `p_`-prefixed, the ordinary case
		wi := seedRefWI(t, pool, proj, uid, "ordinary slug")

		got, aerr := GetWorkItem(ctx, pool, wi.Slug)
		if aerr != nil {
			t.Fatalf("GetWorkItem(%q) = %v; the ordinary slug path regressed", wi.Slug, aerr)
		}
		if got.ID != wi.ID {
			t.Errorf("GetWorkItem(%q) returned id %q, want %q", wi.Slug, got.ID, wi.ID)
		}
	})

	// 🔴 The arm that stops the fix from being satisfied by resolving anything at
	// all. The change widens the WHERE clause from one column to two, and the
	// failure mode of a widening is matching what it should not: a mutant that
	// dropped the predicate entirely, or returned the first row of the table,
	// passes all three arms above and only this one catches it.
	t.Run("control_an_absent_ref_is_still_not_found", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := wiPrefixedTestProject(t, pool, uid)
		seedRefWI(t, pool, proj, uid, "exists, so the table is not empty")

		for _, ref := range []string{
			proj + "#999999", // right project, no such seq
			"wi_nosuchid00",  // id-shaped, absent
			"no_such_proj#1", // slug-shaped, absent project
			"",               // empty
		} {
			if _, aerr := GetWorkItem(ctx, pool, ref); aerr == nil {
				t.Errorf("GetWorkItem(%q) succeeded; the union resolved a reference that does "+
					"not exist, so a hit no longer means the row was found by id or by slug", ref)
			} else if aerr.Code != ErrNotFound {
				t.Errorf("GetWorkItem(%q) = %v, want ErrNotFound", ref, aerr)
			}
		}
	})
}
