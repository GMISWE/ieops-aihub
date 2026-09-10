package server

// aihub#543 probe wave 2 — the hop-3 half of the
// `docs/mcp-cards/pf_create_work_item.md` sentence that names this binding.
//
//	"… passes the **whole argument map** to `pkg/client/client.go`
//	 (`CreateWorkItem`) -> `POST /v1/work_items`, bound by
//	 `internal/server/router.go` (`handleCreateWorkItem`)."
//	    -> TestPostWorkItemsResolvesToTheCreateHandler
//
// WHY IT HAD NO ARM. Every existing test of this endpoint calls
// `handleCreateWorkItem(nil)(c)` directly — router_auth_test.go's
// TestCreateWorkItem_ViewerGets403BeforeDBWrite is the shape they all use, and it
// is the right shape for asserting what the handler does with a request. It
// cannot say which handler a PATH reaches. So "bound by router.go
// (handleCreateWorkItem)" was a sentence about a registration that nothing read,
// on the one route both create tools and every batch item go through.
//
// The method half is the sharper one. `/v1/work_items` carries a GET as well,
// and echo resolves method and path together: a POST registered against the list
// handler, or a create registered with the wrong verb, produces a 404 or a list
// for every create in the system while both handlers still exist and still pass
// their own direct-call tests.
//
// No database and no auth. Echo's route resolution runs before any middleware,
// so Router().Find answers on a router built over a nil pool — the technique
// ready_route_precedence_test.go established in wave 1, and for the same reason:
// ServeHTTP would run BearerAuth and answer 401 for every path, which is the same
// answer whichever handler the router picked.
//
//	GOWORK=off go test ./internal/server/ -run TestPostWorkItemsResolvesToTheCreateHandler -count=1 -v

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// workItemsCollectionPath is the route every create — single or batched — posts
// to, and the route the list handler answers a GET on.
const workItemsCollectionPath = "/v1/work_items"

// createRouteProbeCookieSecret is any 32 bytes; /ui session signing is
// irrelevant here and NewRouter only stores it.
var createRouteProbeCookieSecret = []byte("createroute-probe-cookie-secret32")

// registeredHandlerName returns the Go function name echo has recorded for one
// method+path in its route table, and "" when the table holds no such route.
//
// 🔴 The NAME, not the pattern, and this is the reason it is read out of
// e.Routes() rather than out of a resolved context. `c.Path()` answers
// "/v1/work_items" for a POST wired to the LIST handler just as readily as for
// one wired to the create handler, so a pattern comparison cannot distinguish
// the two cases this arm exists for — and echo wraps the resolved handler in its
// own `(*Echo).add.func1`, so runtime.FuncForPC on `c.Handler()` names echo
// rather than this package. The route table keeps the original.
func registeredHandlerName(e *echo.Echo, method, path string) string {
	for _, r := range e.Routes() {
		if r.Method == method && r.Path == path {
			return r.Name
		}
	}
	return ""
}

// resolvedPattern asks the REAL router which registered pattern a request path
// matches, so a route that exists in the table but is shadowed by another is not
// mistaken for a route requests reach.
//
// Find rather than ServeHTTP, deliberately: ServeHTTP would run BearerAuth over
// a nil pool and answer 401 for every path, which is the same answer whichever
// route the router picked.
func resolvedPattern(e *echo.Echo, method, path string) string {
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	c := e.NewContext(req, httptest.NewRecorder())
	e.Router().Find(method, path, c)
	return c.Path()
}

// TestPostWorkItemsResolvesToTheCreateHandler is the arm.
//
// MUTANTS. Every one was applied to this tree and the verdict is what RAN.
//
//	── enforcement side (the router, the card untouched) ──
//	M69 register the POST against handleListWorkItems
//	                                          RED  post_reaches_the_create_handler
//	M70 delete the v1.POST("/work_items", …) registration
//	                                          RED  post_reaches_the_create_handler (the
//	                                               route table holds no such route, so the
//	                                               name is empty)
//	M71 register the create as a PUT          RED  post_reaches_the_create_handler
//	M72 register the GET against handleCreateWorkItem
//	                                          RED  get_still_reaches_the_list_handler —
//	                                               the control, which is why it is here:
//	                                               both assertions would pass on a
//	                                               router that sent every verb to the
//	                                               create handler
//	── publication side (the card, the router untouched) ──
//	M73 strip the citing sentence's citation, path and symbol both
//	                                          RED  K12 ledger drift//
//
// ⚠️ The publication-side mutant strips the citation's FILE PATH as well as its
// test symbol, and that is not tidiness. Measured 2026-09-10: de-backticking the
// symbol alone left every one of this wave's seventeen sentences still counted as
// Cited, because cardclaims.CitesAnArm is satisfied by EITHER anchor — so the
// publication side of a citation is only as strong as whichever anchor a later
// edit leaves behind. Reported as an incidental finding of aihub#576.
func TestPostWorkItemsResolvesToTheCreateHandler(t *testing.T) {
	e := NewRouter(nil, createRouteProbeCookieSecret)

	t.Run("post_reaches_the_create_handler", func(t *testing.T) {
		handler := registeredHandlerName(e, http.MethodPost, workItemsCollectionPath)
		pattern := resolvedPattern(e, http.MethodPost, workItemsCollectionPath)
		if pattern != workItemsCollectionPath {
			t.Errorf("POST %s resolves to the %q route, so requests do not reach the registration "+
				"read below however it is wired — a route in the table that nothing resolves to is "+
				"a binding on paper", workItemsCollectionPath, pattern)
		}
		if !strings.Contains(handler, "handleCreateWorkItem") {
			t.Errorf("POST %s resolves to pattern %q handled by %q, and the card names "+
				"handleCreateWorkItem. Every create in the system goes through this route — the "+
				"single tool once, a batch of fifty items fifty times — so a POST wired anywhere "+
				"else breaks all of them while handleCreateWorkItem still exists and still passes "+
				"its own direct-call tests.", workItemsCollectionPath, pattern, handler)
		}
	})

	// ── the control. `/v1/work_items` carries two verbs, and without this every
	// assertion above is satisfied by a router that sends all of them to the
	// create handler.
	t.Run("get_still_reaches_the_list_handler", func(t *testing.T) {
		handler := registeredHandlerName(e, http.MethodGet, workItemsCollectionPath)
		pattern := resolvedPattern(e, http.MethodGet, workItemsCollectionPath)
		if strings.Contains(handler, "handleCreateWorkItem") || handler == "" {
			t.Errorf("GET %s resolves to pattern %q handled by %q. The collection route is shared "+
				"by two verbs; if the create handler answers both, the assertion above is about a "+
				"router that no longer distinguishes them and a list request creates a work item.",
				workItemsCollectionPath, pattern, handler)
		}
	})

	t.Logf("POST and GET %s resolve to distinct handlers", workItemsCollectionPath)
}
