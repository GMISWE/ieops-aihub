package server

// aihub#130, observable 3 (read side): the lazy render must back-stop a NULL
// rendered_html on EVERY route that serves an artifact — including /share.
//
// Why this file exists at all, stated plainly, because the reasoning is the
// whole point of the change:
//
// Moving markdown→HTML onto a background worker makes "rendered_html IS NULL" an
// ordinary state. It is the state of every artifact for a moment after it is
// saved, and the PERMANENT state of any artifact whose deferred render was
// dropped — the queue was full, the render panicked, or the process exited
// before the worker got to it. That is only survivable because the reader
// re-derives the HTML from content.
//
// /ui and /v1 have done that since aihub#81/#146. /share did not: it gated on
// `rendered_html != nil` and answered 404 otherwise, with a TODO(aihub#81)
// admitting it. So on that one route a dropped render would not have been a
// latency optimisation, it would have been an artifact that never comes back.
// Sharing a spec you had just written would also have raced the worker.
//
// These are the only assertions in the three-observable set that are RED on the
// pre-aihub#130 tree for a reason other than the code not compiling: /share
// answered 404 and POST /share answered 412.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// unrenderedSpec is a public methodology.spec whose rendered_html is NULL and
// whose markdown content is intact — i.e. exactly what a save leaves behind
// before (or without) a background render.
func unrenderedSpec() *domain.Memory {
	return &domain.Memory{
		ID:           "mem_share1",
		Project:      "testproj",
		Type:         "methodology.spec",
		Visibility:   "public",
		AuthorUserID: "u_author",
		Content:      "# LAZY-SHARE-MARKER\n\nbody paragraph.\n",
		RenderedHTML: nil, // the background render has not landed (or was dropped)
	}
}

// TestSharedArtifact_LazyRenders_WhenDeferredRenderHasNotLanded is the /share
// half of observable 3. Before aihub#130 this returned 404.
func TestSharedArtifact_LazyRenders_WhenDeferredRenderHasNotLanded(t *testing.T) {
	defer withLoadMemoryOverride(unrenderedSpec(), nil)()

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/share/mem_share1", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("mem_share1")
	// No setUser: /share takes no auth. That is the point of the route.

	if err := handleSharedArtifact(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("/share with a NULL rendered_html: status got %d, want 200 — "+
			"a dropped background render would make this artifact permanently unreachable (body=%s)",
			rec.Code, excerptStr(rec.Body.String()))
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<h1") || !strings.Contains(body, "LAZY-SHARE-MARKER") {
		t.Fatalf("/share body is not a render of this artifact's content; got: %s", excerptStr(body))
	}
}

// TestSharedArtifact_StillNotFoundWithNothingToServe is the negative control for
// the test above: relaxing the gate must not turn /share into a route that
// answers 200 for anything at all. An artifact with neither stored HTML nor
// content has nothing to render and stays a 404 — and so does a non-public one,
// which is the security half of the same gate.
func TestSharedArtifact_StillNotFoundWithNothingToServe(t *testing.T) {
	cases := map[string]func(*domain.Memory){
		"no rendered_html and no content": func(m *domain.Memory) { m.Content = "" },
		"content is whitespace only":      func(m *domain.Memory) { m.Content = "  \n\t " },
		"not public":                      func(m *domain.Memory) { m.Visibility = "project" },
		// 🔴 The one that stops this work item from widening an ANONYMOUS read
		// surface. handleSharedArtifact's only gate is visibility=='public', and
		// 'public' is settable by a project writer straight from POST
		// /v1/memories (RememberRequest.Visibility binds from the body and is
		// checked against nothing but migration 0023's CHECK constraint) — it is
		// not, by itself, a deliberate publication through shareRefusal.
		//
		// A public NON-artifact memory with content and no stored HTML answered
		// 404 before aihub#130 because the column was NULL. A lazy fallback keyed
		// on `content != ""` alone would have started serving it to the internet.
		// hasRenderableBody restricts the lazy half to domain.IsRenderType, so it
		// still 404s.
		"public non-artifact type with content but no stored HTML": func(m *domain.Memory) {
			m.Type = "fact.note"
		},
		"public non-artifact type, agent-authored-looking content": func(m *domain.Memory) {
			m.Type = "experience.pitfall"
			m.Content = "# secret internal note\n\nnot meant for the internet.\n"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			mem := unrenderedSpec()
			mutate(mem)
			defer withLoadMemoryOverride(mem, nil)()

			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/share/mem_share1", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetParamNames("id")
			c.SetParamValues("mem_share1")

			if err := handleSharedArtifact(nil)(c); err != nil {
				e.HTTPErrorHandler(err, c)
			}
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status got %d, want 404 (body=%s)", rec.Code, excerptStr(rec.Body.String()))
			}
		})
	}
}

// TestShareArtifact_PublishesArtifactWhoseRenderHasNotLanded covers the WRITE
// half of the same gate. handleShareArtifact answered 412 on a NULL
// rendered_html; under aihub#130 that is the state of every artifact for a
// moment after it is saved, so "write a spec, share it" would have raced the
// background worker and lost. Before aihub#130 this returned 412.
func TestShareArtifact_PublishesArtifactWhoseRenderHasNotLanded(t *testing.T) {
	mem := unrenderedSpec()
	mem.Visibility = "project" // not yet shared
	defer withLoadMemoryOverride(mem, nil)()
	gotID, gotVis, cleanup := withSetVisibilityOverride(nil)
	defer cleanup()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodPost, "/v1/artifacts/mem_share1/share", "mem_share1")
	setUser(c, authorUser()) // writer on testproj

	if err := handleShareArtifact(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("sharing an artifact whose deferred render has not landed: status got %d, want 200 "+
			"(body=%s)", rec.Code, excerptStr(rec.Body.String()))
	}
	if *gotID != "mem_share1" || *gotVis != "public" {
		t.Fatalf("share mutation: got (%q,%q), want (mem_share1,public)", *gotID, *gotVis)
	}
}

// TestShareArtifact_412StillReachableWithNothingToRender is the negative control
// for the write half: the precondition failure must survive for the case it was
// actually written for. Without this, "relax the 412" and "delete the 412" are
// indistinguishable.
func TestShareArtifact_412StillReachableWithNothingToRender(t *testing.T) {
	mem := unrenderedSpec()
	mem.Visibility = "project"
	mem.Content = "" // nothing stored, nothing to render
	defer withLoadMemoryOverride(mem, nil)()
	gotID, _, cleanup := withSetVisibilityOverride(nil)
	defer cleanup()

	e := echo.New()
	c, rec := newUIContext(e, http.MethodPost, "/v1/artifacts/mem_share1/share", "mem_share1")
	setUser(c, authorUser())

	if err := handleShareArtifact(nil)(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("status got %d, want 412 (body=%s)", rec.Code, excerptStr(rec.Body.String()))
	}
	if *gotID != "" {
		t.Fatal("the visibility setter ran despite the 412 — the row was published anyway")
	}
}

// TestArtifactBody_UIv1ShareAgreeOnTheLazyRender pins the aihub#138 byte-parity
// claim to the branch this work item just made reachable. /v1 and /share are
// contractually byte-identical, and they now share resolveArtifactBody — but a
// shared helper is a structural argument, not evidence, so this compares the two
// handlers' actual output for the NULL-rendered_html case they previously could
// not both reach (only one of them served it at all).
func TestArtifactBody_UIv1ShareAgreeOnTheLazyRender(t *testing.T) {
	defer withVersionChainOverride()()
	defer withLoadMemoryOverride(unrenderedSpec(), nil)()

	e := echo.New()

	v1c, v1rec := newUIContext(e, http.MethodGet, "/v1/artifacts/mem_share1/html", "mem_share1")
	setUser(v1c, adminUser())
	if err := handleArtifactHTML(nil)(v1c); err != nil {
		e.HTTPErrorHandler(err, v1c)
	}
	if v1rec.Code != http.StatusOK {
		t.Fatalf("/v1: status got %d, want 200", v1rec.Code)
	}

	sc, srec := newUIContext(e, http.MethodGet, "/share/mem_share1", "mem_share1")
	if err := handleSharedArtifact(nil)(sc); err != nil {
		e.HTTPErrorHandler(err, sc)
	}
	if srec.Code != http.StatusOK {
		t.Fatalf("/share: status got %d, want 200", srec.Code)
	}

	if v1rec.Body.String() != srec.Body.String() {
		t.Fatalf("/v1 and /share disagree on the lazy-rendered body (aihub#138 parity)\n/v1:   %s\n/share: %s",
			excerptStr(v1rec.Body.String()), excerptStr(srec.Body.String()))
	}
}
