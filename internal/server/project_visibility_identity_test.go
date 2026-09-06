package server

// Byte-identity acceptance tests for aihub#377, the half that needs no database.
//
// THE CRITERION, and why it is not "both return 4xx"
// --------------------------------------------------
// An existence oracle needs only ONE distinguishable bit. Two refusals that
// carry the same status and different message bodies are still two answers, and
// a caller sweeping `<project>#<seq>` reads the project's contents off the
// difference without ever seeing a field of it. So every arm below takes the
// response for something that DOES NOT EXIST as its reference, and requires the
// response for something INVISIBLE to be equal to it — same status, same bytes.
//
// aihub#357's executor ran the weaker fix as a mutation and named the failure
// `the same oracle wearing a different hat`. That is what these tests exist to
// keep out.
//
// WHY THESE RUN WITHOUT A DATABASE, AND WHAT THAT BUYS
// ----------------------------------------------------
// Every handler here reaches its store through an injectable seam
// (getWorkItemFn / loadMemoryFn / commitMemoryProjectFn), so both arms can be
// driven in-process against a nil pool. That matters beyond convenience: it puts
// the central acceptance criterion in the `Unit tests` CI step, which runs on
// every push, rather than only in the AIHUB_TEST_DB-gated steps. A criterion
// that only runs where a database is provisioned is a criterion whose failures
// arrive late.
//
// The handlers with NO seam — GET /v1/work_items/:id, the step and event
// endpoints, GET /v1/memories/:id, and the dependency edge — are covered by
// project_visibility_identity_db_test.go. Between them the two files cover the
// six resource kinds the work item names.
//
// 🔴 EVERY PAIR CARRIES A THIRD ARM, and it is not decoration.
// "All refusals are identical" is satisfiable by refusing everything, so a
// two-arm comparison alone cannot tell a fix from a broken build — both arms
// would be equal and both wrong. The third arm is a caller who SHOULD see the
// object, and it asserts the response differs. Without it these tests pass
// against a server that has stopped serving.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// invisibleProject is a project none of the test users hold any role on.
const invisibleProject = "p_invisible_to_everyone_here"

// notFoundFromStore is what a store returns for an id that names nothing. The
// message deliberately differs from notVisibleMessage: these tests must prove
// the HANDLER collapses the two, not that the store happened to agree.
func notFoundFromStore() *domain.AihubError {
	return domain.NewErr(domain.ErrNotFound, `work item "wi_nothing_here" not found`)
}

// identityCase is one handler, driven three ways.
type identityCase struct {
	name string
	// absent drives the handler with a store that reports "no such object".
	absent func(t *testing.T) (int, string)
	// invisible drives it with a store that returns a real object living in a
	// project the caller has no role on.
	invisible func(t *testing.T) (int, string)
	// visible drives it with the same object in a project the caller CAN see.
	// Its response must differ from the other two; see the header note.
	//
	// It may be nil ONLY when noVisibleArm says where the anti-vacuity coverage
	// lives instead. A case with neither fails: an absent third arm is the
	// difference between "these two refusals are identical because the fix works"
	// and "…because the handler refuses everyone", and that is exactly the
	// distinction this whole work item turns on.
	visible      func(t *testing.T) (int, string)
	noVisibleArm string
}

func TestProjectVisibility_InvisibleIsByteIdenticalToAbsent(t *testing.T) {
	for _, tc := range identityCases() {
		t.Run(tc.name, func(t *testing.T) {
			absentStatus, absentBody := tc.absent(t)
			invisibleStatus, invisibleBody := tc.invisible(t)

			if invisibleStatus != absentStatus {
				t.Errorf("status differs: absent=%d invisible=%d — one bit is all an "+
					"enumerator needs", absentStatus, invisibleStatus)
			}
			if invisibleBody != absentBody {
				t.Errorf("body differs, so the refusals are distinguishable:\n"+
					"  absent   : %s\n  invisible: %s\n"+
					"Same status with a different message is still an oracle.",
					absentBody, invisibleBody)
			}
			if strings.Contains(invisibleBody, invisibleProject) {
				t.Errorf("the refusal names the invisible project %q, which is itself the "+
					"disclosure: %s", invisibleProject, invisibleBody)
			}

			// 🔴 The anti-vacuity arm. If the handler refuses everyone, the two
			// comparisons above pass and prove nothing.
			if tc.visible == nil {
				if tc.noVisibleArm == "" {
					t.Fatal("this case has no visible arm and no reason recorded. Without a " +
						"caller who SHOULD see the object, the equality assertions above are " +
						"satisfied by a handler that refuses everybody. Add the arm, or state " +
						"in noVisibleArm which test carries it and why it cannot live here.")
				}
				t.Logf("no in-process visible arm: %s", tc.noVisibleArm)
				return
			}
			visibleStatus, visibleBody := tc.visible(t)
			if visibleStatus == absentStatus && visibleBody == absentBody {
				t.Errorf("a caller who SHOULD see this object got the same response as one "+
					"who should not (status %d, body %s).\n\n"+
					"The equality assertions above are therefore vacuous: they are comparing "+
					"two refusals from a handler that refuses everybody. Either authorization "+
					"is broken, or this fixture's \"visible\" caller is not actually a member.",
					visibleStatus, visibleBody)
			}
		})
	}
}

func identityCases() []identityCase {
	// A memory that is renderable, so handleArtifactHTML gets past its type and
	// body checks and the only thing deciding the response is authorization.
	memIn := func(project string) *domain.Memory {
		return &domain.Memory{
			ID:           "mem_probe",
			Project:      project,
			Type:         "methodology.spec",
			Status:       "active",
			Visibility:   "project",
			AuthorUserID: "u_someone_else",
			RenderedHTML: htmlPtr("<h1>PROBE-BODY</h1>"),
			Content:      "probe content",
		}
	}
	wiIn := func(project string) *domain.WorkItem {
		return &domain.WorkItem{
			ID: "wi_probe", Slug: project + "#7", Project: project,
			Goal: "a goal the prober must not read", Status: "running",
		}
	}

	// caller: viewer on "seen", nothing anywhere else.
	caller := func() *UserContext { return userWithProjects("seen") }

	artifactHTML := func(mem *domain.Memory, aerr *domain.AihubError) func(*testing.T) (int, string) {
		return func(t *testing.T) (int, string) {
			t.Helper()
			defer withLoadMemoryOverride(mem, aerr)()
			e := echo.New()
			c, rec := newUIContext(e, http.MethodGet, "/v1/artifacts/mem_probe/html", "mem_probe")
			c.SetPath("/v1/artifacts/:id/html")
			setUser(c, caller())
			if err := handleArtifactHTML(nil)(c); err != nil {
				e.HTTPErrorHandler(err, c)
			}
			return rec.Code, rec.Body.String()
		}
	}

	uiCommit := func(project string, loadErr error) func(*testing.T) (int, string) {
		return func(t *testing.T) (int, string) {
			t.Helper()
			defer withCommitMemoryProjectOverride(project, "active", loadErr)()
			defer withDoCommitMemoryOverride(nil)()
			c, rec := newCommitRequest(t, "mem_probe", "annotation", caller())
			if err := handleUICommitMemory(nil)(c); err != nil {
				c.Echo().HTTPErrorHandler(err, c)
			}
			return rec.Code, rec.Body.String()
		}
	}

	wiDetail := func(wi *domain.WorkItem, aerr *domain.AihubError) func(*testing.T) (int, string) {
		return func(t *testing.T) (int, string) {
			t.Helper()
			withFakeGetWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string) (*domain.WorkItem, *domain.AihubError) {
				return wi, aerr
			})
			e := echo.New()
			g := e.Group("/ui", wiInjectUser(caller()))
			registerUIWIHandlers(g, nil, nil)
			req := httptest.NewRequest(http.MethodGet, "/ui/wi/probe", nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			return rec.Code, rec.Body.String()
		}
	}

	watchToggle := func(wi *domain.WorkItem, aerr *domain.AihubError) func(*testing.T) (int, string) {
		return func(t *testing.T) (int, string) {
			t.Helper()
			withFakeGetWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string) (*domain.WorkItem, *domain.AihubError) {
				return wi, aerr
			})
			// The write is stubbed so the SUCCESS path also completes against a nil
			// pool — that is what makes this case's visible arm possible in-process.
			prevWatch := watchWorkItemFn
			watchWorkItemFn = func(_ context.Context, _ *pgxpool.Pool, _, _ string) *domain.AihubError { return nil }
			t.Cleanup(func() { watchWorkItemFn = prevWatch })

			h := handleUIWIWatchToggle(nil, partialTemplate("wi_watch_toggle.html.tmpl"), true)
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/ui/wi/probe/watch", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetParamNames("id")
			c.SetParamValues("probe")
			c.Set("user", caller())
			if err := h(c); err != nil {
				e.HTTPErrorHandler(err, c)
			}
			return rec.Code, rec.Body.String()
		}
	}

	eventsPartial := func(wi *domain.WorkItem, aerr *domain.AihubError) func(*testing.T) (int, string) {
		return func(t *testing.T) (int, string) {
			t.Helper()
			withFakeGetWI(t, func(_ context.Context, _ *pgxpool.Pool, _ string) (*domain.WorkItem, *domain.AihubError) {
				return wi, aerr
			})
			withFakeListEvents(t, func(_ context.Context, _ *pgxpool.Pool, _ *domain.ListEventsFilter) (*domain.ListEventsResponse, error) {
				return &domain.ListEventsResponse{}, nil
			})
			e := echo.New()
			g := e.Group("/ui", wiInjectUser(caller()))
			registerUIWIHandlers(g, nil, nil)
			req := httptest.NewRequest(http.MethodGet, "/ui/wi/probe/events/partial", nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			return rec.Code, rec.Body.String()
		}
	}

	memNotFound := domain.NewErr(domain.ErrNotFound, "memory not found")

	return []identityCase{
		{
			name:      "GET /v1/artifacts/:id/html",
			absent:    artifactHTML(nil, memNotFound),
			invisible: artifactHTML(memIn(invisibleProject), nil),
			visible:   artifactHTML(memIn("seen"), nil),
		},
		{
			name:      "POST /ui/memories/:id/commit",
			absent:    uiCommit("", errNoSuchRow{}),
			invisible: uiCommit(invisibleProject, nil),
			visible:   uiCommit("seen", nil),
		},
		{
			name:      "GET /ui/wi/:id",
			absent:    wiDetail(nil, notFoundFromStore()),
			invisible: wiDetail(wiIn(invisibleProject), nil),
			// Measured, not assumed: with a nil pool the visible arm panics in
			// pgxpool.Pool.Acquire. Past the access check this handler fans out to
			// GetParentRef, ListChildren, ListEvents and fetchArtifactLinks (which
			// reaches domain.Recall), none of which is behind a seam. Stubbing five
			// stores to manufacture an in-process success would be testing the stubs.
			visible: nil,
			noVisibleArm: "success path needs a real pool (5 unseamed stores); the " +
				"anti-vacuity arm for this endpoint is TestUIWIDetail_RendersWorkItem " +
				"plus the member-reads-own-project arms in " +
				"project_visibility_identity_db_test.go",
		},
		{
			name:      "POST /ui/wi/:id/watch",
			absent:    watchToggle(nil, notFoundFromStore()),
			invisible: watchToggle(wiIn(invisibleProject), nil),
			visible:   watchToggle(wiIn("seen"), nil),
		},
		{
			name:      "GET /ui/wi/:id/events",
			absent:    eventsPartial(nil, notFoundFromStore()),
			invisible: eventsPartial(wiIn(invisibleProject), nil),
			visible:   eventsPartial(wiIn("seen"), nil),
		},
	}
}

// errNoSuchRow stands in for pgx.ErrNoRows at the commitMemoryProjectFn seam,
// which returns a plain error rather than an *AihubError.
type errNoSuchRow struct{}

func (errNoSuchRow) Error() string { return "no rows in result set" }

// ─── invariant 2 reaches the rendered page, not just the API ─────────────────
//
// 🔴 SCOPE OF THIS VERIFICATION, stated plainly because the scenario asks for a
// browser check that this environment cannot perform (no display server):
//
//	VERIFIED here — the data reaches the page. The far end's slug and the
//	no-access wording are present in the rendered HTML, and no link to the
//	unopenable work item is emitted. That is a real assertion on real output.
//	NOT VERIFIED — how it looks. Layout, contrast, whether the badge wraps
//	badly next to a long slug. That needs a browser and was not done.
//
// Do not read this test as "the UI was verified". It answers "did the fix reach
// the user's eyes", which is the question that makes a backend-only change a
// no-op, and it does not answer "is it right on screen".
//
// TWO HOPS, TWO ASSERTIONS. The chain is
// ListDependencies (sets Slug + Accessible) -> depEntryFrom -> dep_row template.
// Hop 1 is covered by project_visibility_identity_db_test.go's invariant-2 arm;
// hops 2 and 3 are covered below. A contract with three hops needs an assertion
// per hop — checking only the ends is how a middle one goes quietly missing.
func TestInvariant2_InaccessibleEdgeShowsItsSlugOnThePage(t *testing.T) {
	slug := "p_other#7"
	entry := domain.DependencyListEntry{
		ID:         "hidden",
		Slug:       &slug,
		Project:    "p_other",
		Kind:       "blocks",
		Accessible: false,
	}

	// Hop 2: the view-model projection must carry the slug through.
	got := depEntryFrom(entry)
	if !got.Hidden {
		t.Fatalf("an inaccessible edge must be marked Hidden; got %+v", got)
	}
	if got.Slug != slug {
		t.Fatalf("depEntryFrom dropped the slug: got %q, want %q.\n\n"+
			"Invariant 2 exists because hiding the reference leaves the owner unable to "+
			"read their own record. Dropping it here makes the /v1 half a no-op on the "+
			"surface the invariant is about.", got.Slug, slug)
	}
	if got.ID != "" {
		t.Errorf("the canonical id must stay withheld; got %q", got.ID)
	}

	// Hop 3: the template must actually emit it.
	tmpl := wiDetailTemplate()
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "dep_row", got); err != nil {
		t.Fatalf("render dep_row: %v", err)
	}
	html := buf.String()

	if !strings.Contains(html, slug) {
		t.Errorf("the rendered row does not contain the slug %q — the reference is "+
			"invisible to the owner of this work item.\nrendered: %s", slug, html)
	}
	if !strings.Contains(html, "no access") {
		t.Errorf("the rendered row does not say the caller has no access, so the slug "+
			"appears with no explanation.\nrendered: %s", html)
	}
	// No link: there is nothing here the caller may open, and an href would invite
	// a click that answers 404.
	if strings.Contains(html, "href") {
		t.Errorf("the rendered row links to a work item the caller cannot open.\n"+
			"rendered: %s", html)
	}

	// 🔴 Positive control. Every assertion above is satisfied by a template that
	// renders nothing at all except the word "no access", so prove the accessible
	// case still renders a real, linked row.
	visible := depEntryFrom(domain.DependencyListEntry{
		ID: "wi_real", Slug: &slug, Project: "p_mine", Kind: "blocks", Accessible: true,
	})
	buf.Reset()
	if err := tmpl.ExecuteTemplate(&buf, "dep_row", visible); err != nil {
		t.Fatalf("render dep_row (accessible): %v", err)
	}
	if vis := buf.String(); !strings.Contains(vis, "href") || !strings.Contains(vis, slug) {
		t.Errorf("an ACCESSIBLE edge must still render a linked slug; got %s", vis)
	}
}
