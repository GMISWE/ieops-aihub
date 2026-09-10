package mcp_test

// aihub#543 probe wave 2 — what `pf_remember` puts on the wire, and what it
// refuses to put there at all.
//
//	"`validatePfRememberArgs` checks the four required fields and refuses any
//	 `methodology.` prefix, then the handler passes the argument map, projected
//	 to the published property set, to `Remember` → `POST /v1/memories`"
//	    -> TestRememberRefusesItsOwnContractBeforeAnyRequest
//	"**`methodology.*` is refused client-side**, before the HTTP call"
//	    -> TestRememberRefusesItsOwnContractBeforeAnyRequest
//	the 🟢 CLOSED paragraph in the ⚠️ visibility bullet (aihub#586)
//	    -> TestRememberStripsUnpublishedRenderedHTMLBeforeTheWire
//
// 🔴 Why a second recorder rather than `newMemoryWireStack`: that stack's `call`
// helper t.Fatals on an error result and on a call that made no request, which
// are the two OUTCOMES this file is about. A refusal-shaped probe cannot borrow a
// harness whose contract is "a request happened".
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestRememberRefusesItsOwnContract|TestRememberStripsUnpublishedRenderedHTML' -count=1

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

// TestRememberStripsUnpublishedRenderedHTMLBeforeTheWire is the arm behind the
// card's 🟢 CLOSED paragraph in the ⚠️ visibility bullet (aihub#586, owner
// ruling 2026-09-10).
//
// 🔴 This test's predecessor pinned the OPPOSITE behaviour, and the history
// matters to anyone tempted to "restore" it. Until aihub#586 the handler
// forwarded its whole argument map, so an UNPUBLISHED `rendered_html` argument
// landed in the body verbatim, bound to domain.RememberRequest.RenderedHTML,
// and resolveRenderedHTML stored it verbatim for ANY type — which made
// `visibility: public` plus a name hop 1 never mentions an anonymously
// /share-readable row. The predecessor
// (TestRememberForwardsUnpublishedRenderedHTMLToTheBinder) measured that chain
// after the card had reasoned it impossible; its own mutant table already
// recorded that the projection now in place keeps every landBody probe in
// memory_tools_wire_test.go green ("the projection keeps all 13 published
// names ... measured"), so the fix was landed against a known-green blast
// radius.
//
// What this arm holds, in order:
//
//	strip       the observed body carries NO `rendered_html` key, while the
//	            published siblings sent alongside it arrive byte-identical —
//	            so the strip is a projection, not a rejection and not a
//	            rewrite;
//	binder      the observed bytes decode into domain.RememberRequest with
//	            RenderedHTML nil. The json tag is deliberately NOT removed
//	            server-side (pf_save_artifact's published `html` lands on it),
//	            so absence-in-the-struct must come from absence-on-the-wire;
//	disclosure  the tool result's request_adjusted carries an unknown_params
//	            entry NAMING rendered_html with applied=[] — the aihub#389
//	            echo, whose "we used nothing you sent under these names" claim
//	            this tool used to falsify and now satisfies by construction;
//	control     a call sending only published names still carries them all and
//	            gets NO request_adjusted, so the disclosure above is earned by
//	            the stripped key rather than emitted unconditionally.
//
// ⚠️ What this arm does NOT claim: that no row on a live server can reach the
// /share state through this tool. That needs a database and the real router,
// and is held end to end by remember_strip_e2e_db_test.go
// (TestE2ERememberStripsRenderedHTMLFromTheShareSurface), REST positive
// control included.
//
// MUTANTS:
//
//	M14 enforcement: delete pf_remember from wireStrippedTools
//	                                          RED  the strip arm (rendered_html
//	                                               back on the wire)
//	M15 enforcement: make stripUnpublishedArgs return raw unchanged
//	                                          RED  the strip arm
//	M16 enforcement: strip WITHOUT disclosing (skip discloseUnknownParams for
//	    stripped tools)                       RED  the disclosure arm
//	M17 publication: delete the citation from the card's CLOSED paragraph
//	                                          RED  K12
func TestRememberStripsUnpublishedRenderedHTMLBeforeTheWire(t *testing.T) {
	const custom = "<!doctype html><html><body>unpublished and now stripped</body></html>"

	// Hop 1: the name really is unpublished. Without this the rest is a probe of
	// an ordinary published parameter and says nothing the wire file does not.
	props := rememberPublishedProps(t)
	for _, name := range []string{"html", "rendered_html"} {
		if _, published := props[name]; published {
			t.Fatalf("pf_remember publishes %q. This arm is about a name hop 1 does NOT carry; "+
				"if it now does, aihub#586's option ② has been superseded by option ① and the "+
				"card needs rewriting rather than this arm needing a fix.", name)
		}
	}

	rc := newRememberCounter(t)
	text, isErr := rc.call(t, map[string]any{
		"project": "p_probe", "type": "experience.debug", "content": "a memory body",
		"visibility": "public", "rendered_html": custom,
	})
	if isErr {
		t.Fatalf("the call was refused, so nothing below is a fact about the strip — the ruling "+
			"is strip-and-report, not reject: %s", text)
	}
	rc.mu.Lock()
	body := rc.body
	rc.mu.Unlock()

	// The strip. The exact key, absent.
	if got, present := body["rendered_html"]; present {
		t.Errorf("strip: the request body still carries rendered_html (%#v). This is the "+
			"aihub#586 vulnerability itself: an unpublished name riding the forwarded map into "+
			"a bound field.", got)
	}
	// Published siblings from the same request, byte-identical — the ruling's
	// "published keys unaffected" half, and the guard against a strip that
	// projects to the wrong set.
	for key, want := range map[string]string{
		"content": "a memory body", "visibility": "public", "project": "p_probe",
	} {
		if body[key] != want {
			t.Errorf("strip: published %q arrived as %#v, want %#v — the projection must not "+
				"touch what hop 1 carries", key, body[key], want)
		}
	}

	// The binder, driven with the OBSERVED bytes: nothing arrives, nothing binds.
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("re-marshal the observed body: %v", err)
	}
	var req domain.RememberRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("binder: the observed body does not decode into domain.RememberRequest: %v", err)
	}
	if req.RenderedHTML != nil {
		t.Errorf("binder: RememberRequest.RenderedHTML bound %q from a stripped body — the key "+
			"is reaching the wire under a spelling the strip does not cover", *req.RenderedHTML)
	}

	// The disclosure: request_adjusted names the stripped key, applied is empty.
	var result map[string]any
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("disclosure: the tool result is not a JSON object: %v (%q)", err, text)
	}
	entries, _ := result["request_adjusted"].([]any)
	if len(entries) == 0 {
		t.Fatalf("disclosure: no request_adjusted on the response. The ruling is strip AND "+
			"report; a silent strip is aihub#389's defect resurrected one hop earlier. "+
			"Response: %s", text)
	}
	found := false
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		if entry["param"] != "unknown_params" {
			continue
		}
		requested, _ := entry["requested"].([]any)
		for _, name := range requested {
			if name == "rendered_html" {
				found = true
			}
		}
		if applied, _ := entry["applied"].([]any); len(applied) != 0 {
			t.Errorf("disclosure: unknown_params.applied = %#v, want [] — a non-empty applied "+
				"says some of the named keys took effect, which is what the strip exists to "+
				"make false", applied)
		}
	}
	if !found {
		t.Errorf("disclosure: request_adjusted names no rendered_html under unknown_params: %s", text)
	}

	// The control: only published names — all forwarded, nothing disclosed.
	rc2 := newRememberCounter(t)
	cleanText, isErr := rc2.call(t, map[string]any{
		"project": "p_probe", "type": "experience.debug", "content": "a memory body",
		"visibility": "public",
	})
	if isErr {
		t.Fatalf("the control call was refused: %s", cleanText)
	}
	rc2.mu.Lock()
	plain := rc2.body
	rc2.mu.Unlock()
	for _, key := range []string{"project", "type", "content", "visibility"} {
		if _, present := plain[key]; !present {
			t.Errorf("control: published %q did not reach the wire on a clean call — the strip "+
				"is projecting to the wrong set", key)
		}
	}
	if _, present := plain["rendered_html"]; present {
		t.Errorf("control: a call that sent no rendered_html still put one on the wire (%v) — "+
			"the forwarding is inventing keys", plain["rendered_html"])
	}
	var cleanResult map[string]any
	if err := json.Unmarshal([]byte(cleanText), &cleanResult); err != nil {
		t.Fatalf("control: the clean result is not a JSON object: %v", err)
	}
	if v, present := cleanResult["request_adjusted"]; present {
		t.Errorf("control: request_adjusted = %#v on a call with nothing to disclose — an "+
			"unconditional disclosure discloses nothing", v)
	}
}
