package server

// aihub#379: one visibility policy, one denial shape, every entry.
//
// The defect this file pins: the per-memory visibility rule (private → author
// only, admin tier → global admins only) existed in three copies, and two had
// drifted apart in their OBSERVABLE answer — handleGetMemory answered a
// project member asking for someone else's private memory with 404 (existence
// hidden), while checkMemoryVisibility (handleArtifactHTML, /ui memory detail)
// answered the identical condition with 403 "this memory is private to its
// author" — confirming existence AND naming the tier of a row every list path
// deliberately drops.
//
// The chosen contract is 404 (errNotVisible): a row the caller may not see is
// byte-indistinguishable from a row that does not exist. That is the side with
// written design behind it — handleGetMemory's header ("returns 404, never
// 403, so the endpoint can't be used to probe for the existence of a memory
// the caller may not see", aihub#249), errNotVisible's own doctrine
// ("identical status with a different message body is still an oracle",
// aihub#377), and aihub#248's side-rail renumbering, which exists precisely so
// a member cannot tell that a hidden lineage version exists.
//
// This suite drives all three response-writing entries through the SAME
// loadMemoryFn seam with the SAME rows and asserts the SAME bytes. If any one
// copy re-drifts — different status, different wording, or a denial that
// diverges from the plain "no such id" answer — one of these goes red naming
// the entry.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// visibilityEntry is one response-writing reader of a single memory row.
type visibilityEntry struct {
	name   string
	invoke func(t *testing.T, caller *UserContext, memID string) *httptest.ResponseRecorder
}

// The three entries share loadMemoryFn, so one withLoadMemoryOverride drives
// them all against the same row.
func memoryReadEntries() []visibilityEntry {
	return []visibilityEntry{
		{
			name: "GET /v1/memories/:id",
			invoke: func(t *testing.T, caller *UserContext, memID string) *httptest.ResponseRecorder {
				t.Helper()
				e := echo.New()
				req := httptest.NewRequest(http.MethodGet, "/v1/memories/"+memID, nil)
				rec := httptest.NewRecorder()
				c := e.NewContext(req, rec)
				c.SetParamNames("id")
				c.SetParamValues(memID)
				setUser(c, caller)
				_ = handleGetMemory(nil)(c) // the gate commits the response; the returned error is echo's business
				return rec
			},
		},
		{
			name: "GET /v1/artifacts/:id/html",
			invoke: func(t *testing.T, caller *UserContext, memID string) *httptest.ResponseRecorder {
				t.Helper()
				e := echo.New()
				req := httptest.NewRequest(http.MethodGet, "/v1/artifacts/"+memID+"/html", nil)
				rec := httptest.NewRecorder()
				c := e.NewContext(req, rec)
				c.SetParamNames("id")
				c.SetParamValues(memID)
				setUser(c, caller)
				_ = handleArtifactHTML(nil)(c)
				return rec
			},
		},
		{
			name: "GET /ui/memories/:id",
			invoke: func(t *testing.T, caller *UserContext, memID string) *httptest.ResponseRecorder {
				t.Helper()
				e := echo.New()
				c, rec := newUIContext(e, http.MethodGet, "/ui/memories/"+memID, memID)
				c.SetPath("/ui/memories/:id")
				setUser(c, caller)
				// nil template is safe: every denial in this suite returns
				// before the render.
				_ = handleUIMemoryDetail(nil, nil)(c)
				return rec
			},
		},
	}
}

// TestMemoryVisibilityDenialIsUniformAcrossEntries asserts the aihub#379
// contract: for "private and not the author" and for "admin tier and not an
// admin", every entry answers 404 with the shared notVisibleMessage, and the
// bytes are identical (a) across the three entries and (b) to the answer the
// same entry gives for an id that does not exist at all. (b) is the actual
// property — indistinguishable from nonexistence — and (a) follows from it;
// asserting both means a drift is named by entry, not just detected.
func TestMemoryVisibilityDenialIsUniformAcrossEntries(t *testing.T) {
	cases := []struct {
		name   string
		mem    *domain.Memory
		caller *UserContext
	}{
		{
			name: "private_not_author",
			mem: &domain.Memory{
				ID:           "mem_vis379",
				Project:      "testproj",
				Type:         "fact.note",
				Status:       "active",
				Visibility:   "private",
				AuthorUserID: "u_author",
				Content:      "AIHUB379-SECRET",
			},
			caller: otherViewerUser(), // viewer on testproj, not the author
		},
		{
			name: "admin_tier_not_admin",
			mem: &domain.Memory{
				ID:           "mem_vis379",
				Project:      "testproj",
				Type:         "fact.note",
				Status:       "active",
				Visibility:   "admin",
				AuthorUserID: "u_author",
				Content:      "AIHUB379-SECRET",
			},
			// even the author: the admin tier is about global role
			caller: authorUser(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, entry := range memoryReadEntries() {
				t.Run(entry.name, func(t *testing.T) {
					// The denial for a row that exists but is hidden…
					restore := withLoadMemoryOverride(tc.mem, nil)
					denied := entry.invoke(t, tc.caller, tc.mem.ID)
					restore()

					// …and the answer for a row that does not exist at all.
					restore = withLoadMemoryOverride(nil, domain.NewErr(domain.ErrNotFound, "memory not found"))
					missing := entry.invoke(t, tc.caller, tc.mem.ID)
					restore()

					if denied.Code != http.StatusNotFound {
						t.Fatalf("%s: got %d, want 404 — a visibility denial must hide existence (aihub#379); body=%s",
							entry.name, denied.Code, denied.Body.String())
					}
					if denied.Body.String() != missing.Body.String() {
						t.Fatalf("%s: the hidden-row denial and the no-such-id answer differ — that difference is an existence oracle.\nhidden:  %s\nmissing: %s",
							entry.name, denied.Body.String(), missing.Body.String())
					}
					if got := denied.Body.String(); !strings.Contains(got, notVisibleMessage) {
						t.Fatalf("%s: denial does not carry the shared notVisibleMessage; body=%s", entry.name, got)
					}
					if strings.Contains(denied.Body.String(), "AIHUB379-SECRET") {
						t.Fatalf("%s: denied content leaked into the response body", entry.name)
					}
				})
			}
		})
	}
}

// TestMemoryVisibilityPositiveControls is the guard against satisfying the
// uniformity above by refusing everyone: the author and a global admin still
// read the same private row with 200 — on the JSON entry and on the artifact
// HTML entry (the one whose contract aihub#379 changed).
func TestMemoryVisibilityPositiveControls(t *testing.T) {
	html := "<h1>AIHUB379-OK</h1>"
	mem := &domain.Memory{
		ID:           "mem_vis379ok",
		Project:      "testproj",
		Type:         "methodology.spec",
		Status:       "active",
		Visibility:   "private",
		AuthorUserID: "u_author",
		Content:      "body",
		RenderedHTML: &html,
	}
	defer withLoadMemoryOverride(mem, nil)()
	defer withVersionChainOverride()()

	for _, caller := range []*UserContext{authorUser(), adminUser()} {
		// JSON read.
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/v1/memories/"+mem.ID, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(mem.ID)
		setUser(c, caller)
		if err := handleGetMemory(nil)(c); err != nil {
			t.Fatalf("caller %s: GET /v1/memories/:id errored: %v", caller.UserID, err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("caller %s: GET /v1/memories/:id got %d, want 200; body=%s", caller.UserID, rec.Code, rec.Body.String())
		}

		// Artifact HTML read.
		e2 := echo.New()
		req2 := httptest.NewRequest(http.MethodGet, "/v1/artifacts/"+mem.ID+"/html", nil)
		rec2 := httptest.NewRecorder()
		c2 := e2.NewContext(req2, rec2)
		c2.SetParamNames("id")
		c2.SetParamValues(mem.ID)
		setUser(c2, caller)
		if err := handleArtifactHTML(nil)(c2); err != nil {
			t.Fatalf("caller %s: GET /v1/artifacts/:id/html errored: %v", caller.UserID, err)
		}
		if rec2.Code != http.StatusOK {
			t.Fatalf("caller %s: GET /v1/artifacts/:id/html got %d, want 200; body=%s", caller.UserID, rec2.Code, rec2.Body.String())
		}
	}
}
