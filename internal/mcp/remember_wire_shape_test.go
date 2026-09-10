package mcp_test

// aihub#543 probe wave 2 — what `pf_remember` puts on the wire, and what it
// refuses to put there at all.
//
//	"`validatePfRememberArgs` checks the four required fields and refuses any
//	 `methodology.` prefix, then the handler passes the argument map verbatim to
//	 `Remember` → `POST /v1/memories`"
//	    -> TestRememberRefusesItsOwnContractBeforeAnyRequest
//	"**`methodology.*` is refused client-side**, before the HTTP call"
//	    -> TestRememberRefusesItsOwnContractBeforeAnyRequest
//	the `hasRenderableBody` conjunct in the ⚠️ visibility bullet
//	    -> TestRememberForwardsUnpublishedRenderedHTMLToTheBinder
//
// 🔴 Why a second recorder rather than `newMemoryWireStack`: that stack's `call`
// helper t.Fatals on an error result and on a call that made no request, which
// are the two OUTCOMES this file is about. A refusal-shaped probe cannot borrow a
// harness whose contract is "a request happened".
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestRememberRefusesItsOwnContract|TestRememberForwardsUnpublishedRenderedHTML' -count=1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/GMISWE/ieops-aihub/internal/mcp"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// rememberCounter is a real MCP server and a real pkg/client pointed at an
// httptest recorder that COUNTS requests as well as recording the last one.
//
// The count is the instrument. "Refused client-side" is not a claim about an
// error string — an error string is equally produced by a server that answered
// 400 — it is a claim that the process never spoke to aihub, and only a request
// tally can tell those apart.
type rememberCounter struct {
	session *sdkmcp.ClientSession
	mu      sync.Mutex
	n       int
	method  string
	path    string
	body    map[string]any
}

func newRememberCounter(t *testing.T) *rememberCounter {
	t.Helper()
	rc := &rememberCounter{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var decoded map[string]any
		_ = json.NewDecoder(r.Body).Decode(&decoded)
		rc.mu.Lock()
		rc.n++
		rc.method, rc.path, rc.body = r.Method, r.URL.Path, decoded
		rc.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"mem_reply","memory_id":"mem_reply","is_new":true}`))
	}))
	t.Cleanup(ts.Close)

	srv := mcp.New(nil, client.New(ts.URL, "pfk_probe"))
	cTransport, sTransport := sdkmcp.NewInMemoryTransports()
	ctx := t.Context()
	go func() {
		s, err := srv.Connect(ctx, sTransport)
		if err != nil {
			return
		}
		_ = s.Wait()
	}()
	cl := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "remember-wire", Version: "1.0.0"}, nil)
	session, err := cl.Connect(ctx, cTransport, nil)
	if err != nil {
		t.Fatalf("mcp client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	rc.session = session
	return rc
}

// call invokes pf_remember and returns the tool's own text plus whether the
// result was an error. It does NOT fail on either outcome.
func (rc *rememberCounter) call(t *testing.T, args map[string]any) (text string, isErr bool) {
	t.Helper()
	res, err := rc.session.CallTool(t.Context(), &sdkmcp.CallToolParams{
		Name: "pf_remember", Arguments: args,
	})
	if err != nil {
		t.Fatalf("transport error calling pf_remember(%v): %v", args, err)
	}
	for _, c := range res.Content {
		if tc, ok := c.(*sdkmcp.TextContent); ok {
			text += tc.Text
		}
	}
	return text, res.IsError
}

func (rc *rememberCounter) requests() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.n
}

// TestRememberRefusesItsOwnContractBeforeAnyRequest holds the hop 2-3 sentence
// and the first hop-4 bullet together, because they are one mechanism seen from
// two sides: `validatePfRememberArgs` runs BEFORE `s.client.Remember`, so both a
// missing required field and a `methodology.` type cost no round trip.
//
// 🔴 The positive arm is not decoration and runs FIRST. Every refusal below is
// "the request count did not go up", and a harness whose transport was broken
// answers that perfectly for every case at once. The legal call also pins the
// method and path the card names, which nothing else in this package does for
// this tool — memory_tools_wire_test.go's probes are all `landBody` and never
// look at the verb or the route.
//
// MUTANTS:
//
//	M9  enforcement: delete the methodology.* branch from validatePfRememberArgs
//	                                          RED  methodology.spec (1 request, no error)
//	M10 enforcement: delete the required-field loop
//	                                          RED  missing content (1 request, no error)
//	M11 enforcement: move validatePfRememberArgs BELOW s.client.Remember
//	                                          RED  both refusal arms (the refusal
//	                                               still happens, the request also does)
//	M12 enforcement: point client.Remember at a different route
//	                                          RED  the legal call's path assertion
//	M13 publication: delete the citations from the two card sentences
//	                                          RED  K12
func TestRememberRefusesItsOwnContractBeforeAnyRequest(t *testing.T) {
	legal := func() map[string]any {
		return map[string]any{
			"project": "p_probe", "type": "experience.debug",
			"content": "a memory body", "visibility": "project",
		}
	}

	t.Run("a legal call reaches POST /v1/memories", func(t *testing.T) {
		rc := newRememberCounter(t)
		text, isErr := rc.call(t, legal())
		if isErr {
			t.Fatalf("a legal pf_remember was refused, so every count below would be a "+
				"fact about this harness rather than about the guard: %s", text)
		}
		if got := rc.requests(); got != 1 {
			t.Fatalf("a legal pf_remember made %d request(s), want exactly 1", got)
		}
		rc.mu.Lock()
		method, path := rc.method, rc.path
		rc.mu.Unlock()
		if method != http.MethodPost || path != "/v1/memories" {
			t.Errorf("pf_remember reached %s %s, want POST /v1/memories — the card names the "+
				"route because it is what binds handleRemember, and a tool that arrived "+
				"somewhere else would satisfy every body assertion in this package",
				method, path)
		}
	})

	// The four required names, one at a time. Quantified over the schema's own
	// `required` list would be better still, but the guard reads a hand-written
	// list of four in tools_memory.go and this arm exists to hold THAT list — so
	// it names them, and the legal arm above is what stops the list from being
	// satisfied by a tool that refuses everything.
	for _, missing := range []string{"project", "type", "content", "visibility"} {
		t.Run("missing "+missing+" costs no request", func(t *testing.T) {
			rc := newRememberCounter(t)
			args := legal()
			delete(args, missing)
			text, isErr := rc.call(t, args)
			if !isErr {
				t.Errorf("pf_remember without %q was not refused: %s", missing, text)
			}
			if !strings.Contains(text, missing) {
				t.Errorf("the refusal does not name the missing field %q, so a caller is told "+
					"a call failed and not which argument to add; got %q", missing, text)
			}
			if got := rc.requests(); got != 0 {
				t.Errorf("pf_remember without %q made %d request(s). The card says this hop "+
					"refuses before the HTTP call; a refusal that still spends a round trip is "+
					"the server's 400 wearing this hop's clothes.", missing, got)
			}
		})
	}

	for _, mt := range []string{"methodology.spec", "methodology.plan", "methodology.whatever"} {
		t.Run(mt+" costs no request", func(t *testing.T) {
			rc := newRememberCounter(t)
			args := legal()
			args["type"] = mt
			text, isErr := rc.call(t, args)
			if !isErr {
				t.Errorf("pf_remember accepted %q: %s", mt, text)
			}
			if !strings.Contains(text, "pf_save_artifact") {
				t.Errorf("the refusal of %q does not name pf_save_artifact, so a caller holding "+
					"a spec is told no and not where it goes; got %q", mt, text)
			}
			if got := rc.requests(); got != 0 {
				t.Errorf("%q made %d request(s). These are wi-bound credentialed artifacts and "+
					"the whole reason the refusal is client-side is that this tool injects no "+
					"credentials — reaching the endpoint at all means the two tools' partition "+
					"of memories.type is being decided somewhere else.", mt, got)
			}
		})
	}

	// The prefix, not the six names. `methodology.whatever` above already covers
	// it; this control is the other direction — a type that merely CONTAINS the
	// word must still be accepted, or the guard is a substring match.
	t.Run("a legal type containing the word methodology is accepted", func(t *testing.T) {
		rc := newRememberCounter(t)
		args := legal()
		args["type"] = "experience.methodology_notes"
		if text, isErr := rc.call(t, args); isErr {
			t.Errorf("pf_remember refused %q. The rule is a PREFIX (domain.MethodologyTypePrefix); "+
				"a substring match would refuse legal types nothing in the tree forbids: %s",
				args["type"], text)
		}
		if got := rc.requests(); got != 1 {
			t.Errorf("a legal experience.* type made %d request(s), want 1", got)
		}
	})
}

// TestRememberForwardsUnpublishedRenderedHTMLToTheBinder is the arm behind the
// card's CORRECTED ⚠️ visibility bullet.
//
// 🔴 The card used to say the opposite. It reasoned that `hasRenderableBody`
// needs either a stored `rendered_html` — "this tool publishes no `html`
// parameter, so it never writes one" — or a type in the render set, and
// concluded that on a default deployment the two `/share/:id` conditions
// "cannot both hold for a `pf_remember` row". Measured here, the inference is
// unsound at its first step: this handler forwards its whole argument map, so an
// UNPUBLISHED `rendered_html` argument lands in the body verbatim and binds to
// `domain.RememberRequest.RenderedHTML`. That is the same unpublished-but-
// reachable path `tags` took until aihub#425, and it is written into this very
// card two paragraphs above.
//
// The chain's third link is held next door: resolveRenderedHTML's precedence #1
// stores an explicit non-empty value verbatim FOR ANY TYPE
// (internal/domain/memory_render_test.go, TestResolveRenderedHTML_ExplicitOverrides),
// and the INSERT writes what it returns. So the conjunct IS reachable, and what
// the card can honestly say is that the caller has to know a name hop 1 does not
// publish.
//
// ⚠️ What this arm does NOT claim: that the row is then served. That needs a
// database and a route, and the /share half is held by
// internal/server/routes_artifacts_test.go's public/non-public pair.
//
// MUTANTS:
//
//	M14 enforcement: project the args map in the pf_remember handler so only
//	    published names are forwarded         RED  forwards/rendered_html
//	                                               (and GREEN next door: the
//	                                               projection keeps all 13 published
//	                                               names, so memory_tools_wire's
//	                                               landBody probes still pass —
//	                                               measured, because the first draft
//	                                               of this note guessed the opposite)
//	M15 enforcement: drop the `rendered_html` json tag from RememberRequest
//	                                          RED  binds
//	M16 enforcement: assert the absent case wrongly (send nothing, expect a value)
//	                                          RED  the negative control
//	M17 publication: delete the citation from the corrected card bullet
//	                                          RED  K12
func TestRememberForwardsUnpublishedRenderedHTMLToTheBinder(t *testing.T) {
	const custom = "<!doctype html><html><body>unpublished but stored</body></html>"

	// Hop 1: the name really is unpublished. Without this the rest is a probe of
	// an ordinary published parameter and says nothing the wire file does not.
	props := rememberPublishedProps(t)
	for _, name := range []string{"html", "rendered_html"} {
		if _, published := props[name]; published {
			t.Fatalf("pf_remember publishes %q. The card's bullet is about a name hop 1 does "+
				"NOT carry; if it now does, the bullet needs rewriting rather than this arm "+
				"needing a fix.", name)
		}
	}

	rc := newRememberCounter(t)
	if text, isErr := rc.call(t, map[string]any{
		"project": "p_probe", "type": "experience.debug", "content": "a memory body",
		"visibility": "public", "rendered_html": custom,
	}); isErr {
		t.Fatalf("the call was refused, so nothing below is a fact about forwarding: %s", text)
	}
	rc.mu.Lock()
	body := rc.body
	rc.mu.Unlock()

	// Hop 2: it is on the wire, byte-identical.
	got, present := body["rendered_html"]
	if !present {
		t.Fatalf("forwards/rendered_html: the request body carries nothing under that key: %v\n"+
			"The card's hop 2-3 section says this handler forwards its argument map VERBATIM; "+
			"if that stopped being true it is a contract change, not a fix to this arm.", body)
	}
	if got != custom {
		t.Errorf("forwards/rendered_html: arrived as %#v, want the caller's own bytes %#v", got, custom)
	}
	// A published sibling in the same request, so a body that decoded into
	// something unrecognisable cannot satisfy the assertion above by accident.
	if body["content"] != "a memory body" {
		t.Fatalf("the published `content` did not arrive either (%v), so this recorder is not "+
			"reading the request it thinks it is", body["content"])
	}

	// Hop 3: the server's request struct binds it. Decoded from the OBSERVED
	// bytes rather than from a literal, so a rename on either side is red.
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("re-marshal the observed body: %v", err)
	}
	var req domain.RememberRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("binds: the observed body does not decode into domain.RememberRequest: %v", err)
	}
	if req.RenderedHTML == nil || *req.RenderedHTML != custom {
		t.Errorf("binds: RememberRequest.RenderedHTML is %v after binding the observed body. "+
			"handleRemember binds this struct straight from the body, so a nil here would mean "+
			"the value stops at the server's door — which is what the card used to assume.",
			req.RenderedHTML)
	}

	// The negative control. Without it, a binder that filled RenderedHTML from
	// anything at all would pass.
	rc2 := newRememberCounter(t)
	if text, isErr := rc2.call(t, map[string]any{
		"project": "p_probe", "type": "experience.debug", "content": "a memory body",
		"visibility": "public",
	}); isErr {
		t.Fatalf("the control call was refused: %s", text)
	}
	rc2.mu.Lock()
	plain := rc2.body
	rc2.mu.Unlock()
	if _, present := plain["rendered_html"]; present {
		t.Errorf("a call that sent no rendered_html still put one on the wire (%v) — the "+
			"forwarding is inventing keys, and the positive arm above proves nothing",
			plain["rendered_html"])
	}
	rawPlain, err := json.Marshal(plain)
	if err != nil {
		t.Fatalf("re-marshal the control body: %v", err)
	}
	var plainReq domain.RememberRequest
	if err := json.Unmarshal(rawPlain, &plainReq); err != nil {
		t.Fatalf("the control body does not decode into domain.RememberRequest: %v", err)
	}
	if plainReq.RenderedHTML != nil {
		t.Errorf("binding a body with no rendered_html produced %q", *plainReq.RenderedHTML)
	}
}
