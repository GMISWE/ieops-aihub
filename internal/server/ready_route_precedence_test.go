package server

// aihub#543 probe wave 1 — GET /v1/work_items/ready reaches the ready queue and
// not handleGetWorkItem.
//
// Why it had no arm. Every existing test of this endpoint calls
// `handleGetReadyQueue(nil)(c)` directly (queryparam_policy_test.go's
// getReadyQueueRequest is the helper they all use), which is the right shape for
// asserting what the handler does with a parameter and cannot say anything at
// all about which handler a PATH reaches. The route's own comment in router.go
// records the concern — "must come before :id" — and the concern was checked by
// nothing.
//
// 🔴 And the measurement corrects the reason. The card and router.go both
// attribute the correct routing to REGISTRATION ORDER. Measured 2026-09-10 on
// this tree: echo resolves the literal segment ahead of `:id` whichever order
// the two are registered in — the third arm below registers them the wrong way
// round and the literal still wins, because echo's router prefers a static
// segment to a parameter at the same position rather than taking the first
// match. So the ordering is not what makes this safe, and a future reader who
// believes it is would "fix" a shadowing bug by moving a line that has no effect.
// What DOES make it safe is stated separately on the route and is unchanged: no
// work item can be addressed as "ready", because a slug is the generated
// `project || '#' || seq` and every id is `wi_` + 8 base62 chars.
//
// No database and no auth: echo's route resolution happens before any middleware
// runs, so Router().Find answers on a router built over a nil pool.
//
//	GOWORK=off go test ./internal/server/ -run TestTheLiteralReadyPath -count=1 -v

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// readyRoutePath and workItemByIDRoutePath are the two registered patterns this
// arm distinguishes between.
const (
	readyRoutePath        = "/v1/work_items/ready"
	workItemByIDRoutePath = "/v1/work_items/:id"
)

// routeProbeCookieSecret is any 32 bytes; /ui session signing is irrelevant here
// and NewRouter only stores it.
var routeProbeCookieSecret = []byte("readyroute-probe-cookie-secret-32")

// resolveRoute asks the REAL router which registered pattern a request path
// matches, and hands back the pattern plus whatever `:id` was bound to.
//
// Find rather than ServeHTTP, deliberately: ServeHTTP would run BearerAuth over
// a nil pool and answer 401 for both paths, which is the same answer whichever
// handler the router picked — an assertion that cannot distinguish its two cases
// is not an assertion.
func resolveRoute(t *testing.T, e *echo.Echo, path string) (pattern, id string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	c := e.NewContext(req, httptest.NewRecorder())
	e.Router().Find(http.MethodGet, path, c)
	return c.Path(), c.Param("id")
}

// TestTheLiteralReadyPathResolvesToTheQueueRouteWhicheverOrder is the arm.
//
// ─── Recorded mutants ──────────────────────────────────────────────────────
//
// Every one was applied to this tree and the verdict is what RAN.
//
//	── enforcement side (the router, the card untouched) ──
//	M20 delete the v1.GET("/work_items/ready", …) registration
//	                                            RED  the path resolves to :id
//	M21 register it as "/work_items/:ready"      RED  the path resolves to :id
//	M23 delete the v1.GET("/work_items/:id", …) registration
//	                                            RED  the control arm, which is why it
//	                                                 is here: both assertions would
//	                                                 pass on a router that had lost
//	                                                 `:id` altogether
//
//	── publication side (the card, the router untouched) ──
//	M22 delete the sentence from the card        RED  K12 POPULATION_MOVED
//	G5  control: reword it, citation untouched GREEN  K12 owns the publication side
func TestTheLiteralReadyPathResolvesToTheQueueRouteWhicheverOrder(t *testing.T) {
	e := NewRouter(nil, routeProbeCookieSecret)

	// ── the claim: the literal reaches the queue route.
	if pattern, id := resolveRoute(t, e, readyRoutePath); pattern != readyRoutePath {
		t.Errorf("GET %s resolves to the %q route (id=%q), not %q. The ready queue is then "+
			"unreachable over HTTP and every caller of pf_get_ready_queue gets whatever "+
			"handleGetWorkItem says about a work item called \"ready\" — which no work item "+
			"can be, so a 404 about a wi nobody asked for.", readyRoutePath, pattern, id,
			readyRoutePath)
	}

	// ── the control: `:id` still catches everything that is not the literal.
	// Without it, a router that resolved every path to the ready route would
	// satisfy the arm above.
	const anID = "/v1/work_items/wi_abc12345"
	if pattern, id := resolveRoute(t, e, anID); pattern != workItemByIDRoutePath || id != "wi_abc12345" {
		t.Errorf("GET %s resolves to %q with id=%q, want %q with the id bound. The literal "+
			"route above proves nothing if the parameter route no longer matches anything: "+
			"both assertions would pass on a router that had lost `:id` altogether.",
			anID, pattern, id, workItemByIDRoutePath)
	}

	// ── the conclusion the card states: order is not what decides it.
	//
	// A separate echo instance rather than the real router, because the real one
	// can only be registered one way and the claim is about the other way. The
	// two patterns are the real ones.
	reversed := echo.New()
	v1 := reversed.Group("/v1")
	v1.GET("/work_items/:id", func(c echo.Context) error { return nil })
	v1.GET("/work_items/ready", func(c echo.Context) error { return nil })
	if pattern, _ := resolveRoute(t, reversed, readyRoutePath); pattern != readyRoutePath {
		t.Errorf("with `:id` registered FIRST, GET %s resolves to %q — so registration order "+
			"IS what keeps the literal from being swallowed, and this router's ordering is "+
			"load-bearing after all. That is the opposite of what was measured on 2026-09-10 "+
			"and of what the pf_get_ready_queue card now says; if echo's precedence has "+
			"changed, the card and the comment on the route both have to change with it, and "+
			"the ⚠️ note in router.go about a future literal segment becomes a much sharper "+
			"warning.", readyRoutePath, pattern)
	}

	t.Logf("%s resolves to the queue route with `:id` registered either side of it", readyRoutePath)
}
