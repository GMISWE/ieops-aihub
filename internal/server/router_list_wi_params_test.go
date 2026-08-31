package server

// Hop 3 of the pf_list_work_items parameter contract (aihub#280).
//
// The contract has four hops — published MCP schema, MCP→HTTP forwarding,
// query param→ListWorkItemsFilter, filter field→SQL — and **a contract with N
// hops needs N assertions**. See internal/mcp/tools_list_wi_schema_test.go for
// the hop table and where the other three live.
//
// This file owns hop 3 only: does a query param land on the filter that reaches
// the domain layer? It runs against a nil pool via the listWorkItemsFn seam, so
// it executes on CI's plain "Unit tests" step (which deliberately leaves
// AIHUB_TEST_DB unset) rather than SKIPping there.
//
// Why hop 3 needs its own file rather than relying on the end-to-end DB test:
// seven params were published for a long time while the handler parsed none of
// them, and an end-to-end row count cannot distinguish "the handler dropped it"
// from "the SQL ignored it" from "the fixture happens to match". Each hop
// asserted separately says which one broke.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// listWIRequestAs is listWIRequest (router_list_wi_sort_test.go) with an
// explicit caller identity, since the ids=-without-project path branches on the
// caller's role and project set.
func listWIRequestAs(t *testing.T, pool *pgxpool.Pool, rawQuery string, uc *UserContext) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/work_items?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	setUser(c, uc)
	if err := handleListWorkItems(pool)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	return rec
}

// captureListWIFilter runs one GET /v1/work_items and returns the filter and
// project scope handleListWorkItems handed to the domain layer.
func captureListWIFilter(t *testing.T, rawQuery string) (domain.ListWorkItemsFilter, string, *httptest.ResponseRecorder) {
	t.Helper()
	var captured domain.ListWorkItemsFilter
	var capturedProject string
	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, project string, f domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		captured = f
		capturedProject = project
		return &domain.ListWorkItemsResult{Items: []*domain.WorkItem{}}, nil
	})
	rec := listWIRequest(t, nil, rawQuery)
	return captured, capturedProject, rec
}

// TestListWorkItems_EveryFilterParamReachesTheFilter is the hop-3 guard.
//
// Each case sends exactly one param and asserts the corresponding filter field.
// Before aihub#280 the milestone/scenario/since/ready_only/include_step_state
// and kind cases all failed here: handleListWorkItems never read those query
// params, so the field stayed at its zero value and the caller's argument
// vanished with no error.
func TestListWorkItems_EveryFilterParamReachesTheFilter(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		check func(t *testing.T, f domain.ListWorkItemsFilter)
	}{
		{"status single", "status=wrapped", func(t *testing.T, f domain.ListWorkItemsFilter) {
			if len(f.Status) != 1 || f.Status[0] != "wrapped" {
				t.Errorf("filter.Status = %#v, want [wrapped]", f.Status)
			}
		}},
		{"status csv", "status=wrapped,queued", func(t *testing.T, f domain.ListWorkItemsFilter) {
			if len(f.Status) != 2 || f.Status[0] != "wrapped" || f.Status[1] != "queued" {
				t.Errorf("filter.Status = %#v, want [wrapped queued]", f.Status)
			}
		}},
		{"wi_type", "wi_type=fix_bug", func(t *testing.T, f domain.ListWorkItemsFilter) {
			wantStrPtr(t, "filter.WIType", f.WIType, "fix_bug")
		}},
		// kind is the deprecated alias; /ui has always folded it onto wi_type.
		{"kind aliases wi_type", "kind=fix_bug", func(t *testing.T, f domain.ListWorkItemsFilter) {
			wantStrPtr(t, "filter.WIType (via kind)", f.WIType, "fix_bug")
		}},
		{"priority", "priority=urgent", func(t *testing.T, f domain.ListWorkItemsFilter) {
			wantStrPtr(t, "filter.Priority", f.Priority, "urgent")
		}},
		{"milestone", "milestone=v2", func(t *testing.T, f domain.ListWorkItemsFilter) {
			wantStrPtr(t, "filter.Milestone", f.Milestone, "v2")
		}},
		// "writing", not "release": scenario is CHECKed to coding|writing|data,
		// so probing with the value pf-release sends would imply it is legal.
		{"scenario", "scenario=writing", func(t *testing.T, f domain.ListWorkItemsFilter) {
			wantStrPtr(t, "filter.Scenario", f.Scenario, "writing")
		}},
		{"label", "label=mcp", func(t *testing.T, f domain.ListWorkItemsFilter) {
			wantStrPtr(t, "filter.Label", f.Label, "mcp")
		}},
		{"user_id", "user_id=u_abc", func(t *testing.T, f domain.ListWorkItemsFilter) {
			wantStrPtr(t, "filter.UserID", f.UserID, "u_abc")
		}},
		{"source", "source=human", func(t *testing.T, f domain.ListWorkItemsFilter) {
			wantStrPtr(t, "filter.Source", f.Source, "human")
		}},
		{"ids csv", "ids=wi_a,wi_b", func(t *testing.T, f domain.ListWorkItemsFilter) {
			if len(f.IDs) != 2 || f.IDs[0] != "wi_a" || f.IDs[1] != "wi_b" {
				t.Errorf("filter.IDs = %#v, want [wi_a wi_b]", f.IDs)
			}
		}},
		{"since", "since=2026-08-01T00:00:00Z", func(t *testing.T, f domain.ListWorkItemsFilter) {
			if f.Since == nil {
				t.Fatalf("filter.Since is nil — the since param never reached the filter")
			}
			want := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
			if !f.Since.Equal(want) {
				t.Errorf("filter.Since = %v, want %v", *f.Since, want)
			}
		}},
		{"ready_only", "ready_only=true", func(t *testing.T, f domain.ListWorkItemsFilter) {
			if !f.ReadyOnly {
				t.Errorf("filter.ReadyOnly = false — the ready_only param never reached the filter")
			}
		}},
		{"include_step_state", "include_step_state=true", func(t *testing.T, f domain.ListWorkItemsFilter) {
			if !f.IncludeStepState {
				t.Errorf("filter.IncludeStepState = false — the include_step_state param never reached the filter")
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, project, rec := captureListWIFilter(t, "project=testproject&"+tc.query)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
			}
			if project != "testproject" {
				t.Errorf("project scope = %q, want testproject", project)
			}
			tc.check(t, f)
		})
	}
}

// The mirror image of the test above, and the reason the negative control in
// the DB suite exists: a param the handler does not know about must NOT quietly
// become a filter. "Clear everything we don't recognise" would make the
// end-to-end n==0 probe pass for the wrong reason.
func TestListWorkItems_UnknownParamSetsNoFilter(t *testing.T) {
	f, _, rec := captureListWIFilter(t, "project=testproject&zzqqbogusparam=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if f.WIType != nil || f.Priority != nil || f.Milestone != nil || f.Scenario != nil ||
		f.Label != nil || f.Source != nil || f.UserID != nil || f.Since != nil ||
		len(f.Status) != 0 || len(f.IDs) != 0 || f.ReadyOnly || f.IncludeStepState {
		t.Errorf("an unrecognised param must leave every filter field unset; got %+v", f)
	}
}

// An explicit wi_type must beat the deprecated alias, so a caller that sends
// both gets the canonical spelling rather than whichever the code reads last.
func TestListWorkItems_WiTypeWinsOverKind(t *testing.T) {
	f, _, rec := captureListWIFilter(t, "project=testproject&kind=chore&wi_type=fix_bug")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	wantStrPtr(t, "filter.WIType", f.WIType, "fix_bug")
}

// since is the one param the handler has to *parse* rather than pass through,
// so it is the one that can fail by producing a zero time.Time. pf-release
// computes its entire release scope from it, so a garbled value must be
// rejected loudly — the nil pool proves the 400 happens before any query.
func TestListWorkItems_UnparseableSinceRejectedBeforeDB(t *testing.T) {
	rec := listWIRequest(t, nil, "project=testproject&since=last-tuesday")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unparseable since, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "last-tuesday") {
		t.Errorf("the 400 must name the offending value so the caller can fix it; got %s", body)
	}
}

// ─── project becomes optional for an ids= lookup (aihub#280) ────────────────
//
// pf-status, pf-retro and pf-execute all open with
// pf_list_work_items(ids=[<current_wi_id>]) and no project. That returned
// 400 BAD_REQUEST, so those three skills' first call had almost certainly never
// succeeded in production.

func TestListWorkItems_IdsWithoutProjectIsAllowedAndScoped(t *testing.T) {
	f, project, rec := captureListWIFilter(t, "ids=wi_abc")
	if rec.Code != http.StatusOK {
		t.Fatalf("ids= without project must be accepted, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if project != "" {
		t.Errorf("project scope = %q, want \"\" so the query scopes by AccessibleProjects", project)
	}
	if len(f.IDs) != 1 || f.IDs[0] != "wi_abc" {
		t.Errorf("filter.IDs = %#v, want [wi_abc]", f.IDs)
	}
	// The access check moved from "must name a project" to "bounded to the
	// projects you can see". If AccessibleProjects were empty here, a non-admin
	// would get an unscoped query across every project — so this assertion is
	// the authorization guard, not a detail.
	if len(f.AccessibleProjects) != 1 || f.AccessibleProjects[0] != "testproject" {
		t.Errorf("filter.AccessibleProjects = %#v, want [testproject] (viewerUser's only project)",
			f.AccessibleProjects)
	}
}

// Dropping the project requirement must not drop it for everyone: a plain list
// with neither project nor ids is still a 400, and the message must mention the
// new escape hatch or the caller cannot act on it.
func TestListWorkItems_NoProjectAndNoIdsStillRejected(t *testing.T) {
	rec := listWIRequest(t, nil, "status=wrapped")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 with neither project nor ids, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "ids") {
		t.Errorf("the 400 should point at the ids= alternative; got %s", body)
	}
}

// A caller confined to one project (ProjectScope) must stay confined on the
// ids= path too, otherwise the relaxation is a scope escape. The caller here is
// a *member* of the scoped project, which is the legitimate case.
func TestListWorkItems_IdsWithoutProjectRespectsProjectScope(t *testing.T) {
	scope := "onlyproject"
	uc := viewerUser()
	uc.ProjectScope = &scope
	// BearerAuth intersects memberships with the scope, so a real scoped caller
	// who can read the project has exactly this shape.
	uc.ProjectRoles = map[string]string{scope: "viewer"}

	var captured domain.ListWorkItemsFilter
	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string, f domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		captured = f
		return &domain.ListWorkItemsResult{Items: []*domain.WorkItem{}}, nil
	})
	rec := listWIRequestAs(t, nil, "ids=wi_abc", uc)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if len(captured.AccessibleProjects) != 1 || captured.AccessibleProjects[0] != scope {
		t.Errorf("a project-scoped caller must be bounded to %q; got %#v", scope, captured.AccessibleProjects)
	}
}

// The authorization guard on the scoped path, and the sharpest form of it.
//
// ProjectScope is a *confinement* on an API key, never a grant: BearerAuth
// intersects real memberships with it, so a non-admin scoped to a project they
// are not a member of arrives with an EMPTY ProjectRoles. `?project=X` answers
// 403 for that caller. The ids= path must too — otherwise removing someone from
// a project's members would no longer revoke their reads, which is a
// straightforward privilege escalation dressed up as a convenience.
//
// An earlier draft of this change scoped to *u.ProjectScope unconditionally and
// had a test asserting exactly that. The test passed; the behaviour was wrong.
func TestListWorkItems_IdsWithoutProjectDeniedWhenScopedToANonMemberProject(t *testing.T) {
	scope := "notamemberofthis"
	uc := viewerUser()
	uc.ProjectScope = &scope
	// A role somewhere else entirely — the scope must not launder it into access
	// to `scope`.
	uc.ProjectRoles = map[string]string{"someotherproject": "viewer"}

	// A nil pool proves the rejection happens before any query: reaching the DB
	// would panic.
	rec := listWIRequestAs(t, nil, "ids=wi_abc", uc)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a caller scoped to a project they have no role in must get 403, got %d (body: %s)",
			rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, scope) {
		t.Errorf("the 403 should name the project; got %s", body)
	}
}

// A scoped ADMIN is the one legitimate exemption: BearerAuth skips the
// membership query for admins, so their ProjectRoles is always empty and
// requiring a role there would lock them out of their own scoped key.
func TestListWorkItems_IdsWithoutProjectScopedAdminIsAllowed(t *testing.T) {
	scope := "adminscoped"
	uc := adminUser()
	uc.ProjectScope = &scope

	var captured domain.ListWorkItemsFilter
	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string, f domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		captured = f
		return &domain.ListWorkItemsResult{Items: []*domain.WorkItem{}}, nil
	})
	rec := listWIRequestAs(t, nil, "ids=wi_abc", uc)
	if rec.Code != http.StatusOK {
		t.Fatalf("a scoped admin must be allowed, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	// Still confined to the scope — admin does not mean unscoped when a scope is set.
	if len(captured.AccessibleProjects) != 1 || captured.AccessibleProjects[0] != scope {
		t.Errorf("a scoped admin must stay bounded to %q; got %#v", scope, captured.AccessibleProjects)
	}
}

// A caller with no project roles at all has nothing to scope to. Returning an
// unscoped query would expose every project, so this must be a 403.
func TestListWorkItems_IdsWithoutProjectDeniedWhenNoAccessibleProjects(t *testing.T) {
	uc := viewerUser()
	uc.ProjectRoles = map[string]string{}
	rec := listWIRequestAs(t, nil, "ids=wi_abc", uc)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a caller with no accessible projects, got %d (body: %s)",
			rec.Code, rec.Body.String())
	}
}

// An admin's ids= lookup is deliberately unscoped (empty AccessibleProjects +
// empty project = no project clause), matching the documented admin view-all
// contract on domain.ListWorkItems.
func TestListWorkItems_IdsWithoutProjectUnscopedForAdmin(t *testing.T) {
	var captured domain.ListWorkItemsFilter
	withFakeListWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string, f domain.ListWorkItemsFilter) (*domain.ListWorkItemsResult, *domain.AihubError) {
		captured = f
		return &domain.ListWorkItemsResult{Items: []*domain.WorkItem{}}, nil
	})
	rec := listWIRequestAs(t, nil, "ids=wi_abc", adminUser())
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if len(captured.AccessibleProjects) != 0 {
		t.Errorf("an admin ids= lookup must be unscoped; got AccessibleProjects=%#v", captured.AccessibleProjects)
	}
}

// Booleans must be parsed, not pattern-matched against "true". `ready_only=True`
// reading as false would be indistinguishable from not sending the param — the
// same silent drop this wi is about, one type over (aihub#280).
func TestListWorkItems_BooleanSpellingsAreParsedOrRejected(t *testing.T) {
	for _, tc := range []struct {
		raw      string
		want     bool
		wantCode int
	}{
		{"true", true, http.StatusOK},
		{"True", true, http.StatusOK},
		{"TRUE", true, http.StatusOK},
		{"1", true, http.StatusOK},
		{"t", true, http.StatusOK},
		{"false", false, http.StatusOK},
		{"0", false, http.StatusOK},
		{"F", false, http.StatusOK},
		// Not a boolean in any spelling: reject rather than read as false.
		{"yes", false, http.StatusBadRequest},
		{"maybe", false, http.StatusBadRequest},
		{"2", false, http.StatusBadRequest},
	} {
		t.Run("ready_only="+tc.raw, func(t *testing.T) {
			f, _, rec := captureListWIFilter(t, "project=testproject&ready_only="+tc.raw)
			if rec.Code != tc.wantCode {
				t.Fatalf("ready_only=%q: got %d, want %d (body: %s)", tc.raw, rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode == http.StatusOK && f.ReadyOnly != tc.want {
				t.Errorf("ready_only=%q produced filter.ReadyOnly=%v, want %v", tc.raw, f.ReadyOnly, tc.want)
			}
		})
	}
	// include_step_state goes through the same parser, so one case is enough to
	// prove it is not the hand-rolled comparison.
	if _, _, rec := captureListWIFilter(t, "project=testproject&include_step_state=yes"); rec.Code != http.StatusBadRequest {
		t.Errorf("include_step_state=yes must be rejected, got %d", rec.Code)
	}
	if f, _, _ := captureListWIFilter(t, "project=testproject&include_step_state=1"); !f.IncludeStepState {
		t.Errorf("include_step_state=1 must be honoured")
	}
}

// CSV params must be trimmed. `ids=wi_a, wi_b` is what a human or an agent
// writes, and an untrimmed " wi_b" matches no row while looking like a working
// filter — the silent miss this wi exists to close.
func TestListWorkItems_CSVParamsAreTrimmed(t *testing.T) {
	f, _, rec := captureListWIFilter(t, "project=testproject&ids=wi_a,%20wi_b%20&status=wrapped,%20queued")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if len(f.IDs) != 2 || f.IDs[0] != "wi_a" || f.IDs[1] != "wi_b" {
		t.Errorf("filter.IDs = %#v, want [wi_a wi_b] — untrimmed ids match nothing", f.IDs)
	}
	if len(f.Status) != 2 || f.Status[0] != "wrapped" || f.Status[1] != "queued" {
		t.Errorf("filter.Status = %#v, want [wrapped queued]", f.Status)
	}
	// wi_type/kind are single values, trimmed the same way.
	if f2, _, _ := captureListWIFilter(t, "project=testproject&wi_type=%20fix_bug%20"); f2.WIType == nil || *f2.WIType != "fix_bug" {
		t.Errorf("wi_type must be trimmed; got %v", f2.WIType)
	}
}

// `ids=,` is a non-empty param carrying no id. Treating it as "an ids lookup"
// would drop the project requirement and then list every accessible project in
// full — the opposite of what the caller asked for.
func TestListWorkItems_IdsWithNoUsableValuesStillRequiresProject(t *testing.T) {
	for _, raw := range []string{"ids=,", "ids=%20", "ids=,,,"} {
		t.Run(raw, func(t *testing.T) {
			rec := listWIRequest(t, nil, raw)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s carries no id, so project= is still required; got %d (body: %s)",
					raw, rec.Code, rec.Body.String())
			}
		})
	}
	// With a project it is simply an absent filter, not an empty selection.
	f, _, rec := captureListWIFilter(t, "project=testproject&ids=,")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if len(f.IDs) != 0 {
		t.Errorf("ids=, must leave filter.IDs unset, not bind an empty selection; got %#v", f.IDs)
	}
}

func wantStrPtr(t *testing.T, label string, got *string, want string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is nil — the query param never reached the filter", label)
	}
	if *got != want {
		t.Errorf("%s = %q, want %q", label, *got, want)
	}
}
