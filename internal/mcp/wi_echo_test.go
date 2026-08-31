package mcp_test

// aihub#281: the create/update content echo, asserted on the bytes the model
// actually receives.
//
// Every case here drives a real MCP session against a fake aihub (the harness
// in tools_fusion_test.go) rather than calling the projection function
// directly, because the projection is not the thing that can break. What can
// break is a hop: the argument not reaching the handler, the handler's edit not
// reaching the marshaller, `brief` leaking into the request body. Those are only
// visible from the ends.
//
// The suppression's own hop table (aihub#280's rule: a contract with N hops
// needs N assertions):
//
//	hop 1  published InputSchema             wi_echo_schema cases below
//	hop 2  MCP args -> HTTP request body     TestUpdateBriefIsNotForwardedToTheServer
//	hop 3  HTTP response -> client map       the fake serves content; every case
//	                                         that KEEPS it proves it arrived
//	hop 4  suppression decision              the positive/negative pairs below
//	hop 5  decision -> tool output text      assertions on rawText, not on the
//	                                         decoded map
//	1->5   against the REAL server + DB      wi_echo_e2e_db_test.go
//
// Hop 3 deserves a note. Asserting "the tool output has no content" is
// satisfied just as well by an upstream that stopped sending content at all, so
// on its own it would be a green that means nothing. Every negative case here
// serves content from the fake and requires it back OUT, which is what makes the
// positive cases discriminating rather than vacuous.
//
// Run: go test ./internal/mcp/ -run 'TestCreate|TestUpdate|TestBatchCreateSupp|TestContentEcho' -v  (no database needed)

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/GMISWE/ieops-aihub/internal/mcp"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// echoTestContent is a body of a size real callers actually send. The measured
// sample's mean was ~4.7 KB with a maximum of 17,870 characters, so a toy
// three-character string would make the size assertions unfalsifiable.
var echoTestContent = "## Background\n\n" + strings.Repeat(
	"Long-form markdown of the sort a spec or plan artifact carries. ", 64)

// workItemRecordBase builds a response with the same keys the live server
// returns for a work item, MINUS content (captured read-only from
// GET /v1/work_items/:id against production, which serialises the identical
// *domain.WorkItem that POST and PATCH answer with — router.go
// handleCreateWorkItem / handleUpdateWorkItem / handleGetWorkItem all end in
// c.JSON(..., wi)).
//
// Written out rather than trimmed to the fields under test so the byte
// measurements below are of a realistic record, and so a case that asserts
// "everything else survived" has something to survive.
//
// Split from workItemRecord because dropContentEcho branches on a distinction a
// single helper could not express: a content key holding a string, a content key
// holding JSON null, and no content key at all. The old helper skipped the key
// whenever the content was empty, so NO test in this file could construct
// `content: null` — and a mutant that answered a null body by deleting it and
// reporting content_len: 0 survived the entire suite because of it.
func workItemRecordBase(id string) map[string]any {
	rec := map[string]any{
		"id": id, "slug": "aihub#281", "seq": 281, "project": "aihub",
		"goal":                   "pf_create/update_work_item echoes back the content the caller just wrote",
		"wi_type":                "feature",
		"status":                 "running",
		"priority":               "high",
		"scenario":               "coding",
		"source":                 "human",
		"milestone":              nil,
		"labels":                 []any{"aihub", "mcp", "token-cost"},
		"declared_resources":     []any{map[string]any{"type": "path", "uri": "file:internal/mcp/tools_lifecycle.go", "intent": "write"}},
		"resources_version":      2,
		"attrs":                  map[string]any{"review_2026_08_29": "624/624 identical"},
		"reporter_user_id":       "u_5dFjeaMZ",
		"reporter_display":       "xiaokang.w",
		"parent_work_item_id":    nil,
		"requires_human_session": false,
		"current_attempt_id":     "ra_rihwIwG7",
		"current_attempt_epoch":  1,
		"external_share_key":     nil,
		"external_share_type":    nil,
		"created_at":             "2026-08-29T11:55:56.113795Z",
		"updated_at":             "2026-08-31T15:39:45.663074Z",
		"closed_at":              nil,
	}
	return rec
}

// workItemRecord is workItemRecordBase plus a content key, set VERBATIM —
// including an empty string, which is a stored body and not the absence of one.
// Tests that need `content: null` or no content key at all build on
// workItemRecordBase directly.
func workItemRecord(id, content string) map[string]any {
	rec := workItemRecordBase(id)
	rec["content"] = content
	return rec
}

// callToolText is callTool's sibling that hands back the response TEXT as well
// as the decoded object. The byte count is the quantity this work item exists
// to move, and it cannot be recovered from the decoded map.
func callToolText(t *testing.T, f *fakeAihub, tool string, args map[string]any) (string, map[string]any) {
	t.Helper()
	ctx := context.Background()

	server := mcp.New(nil, client.New(f.server.URL, "test-key"))
	cTransport, sTransport := sdkmcp.NewInMemoryTransports()
	serverCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		session, err := server.Connect(serverCtx, sTransport)
		if err != nil {
			return
		}
		_ = session.Wait()
	}()

	cl := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "echo-test", Version: "1.0.0"}, nil)
	session, err := cl.Connect(ctx, cTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", tool, err)
	}
	if res.IsError {
		t.Fatalf("call %s returned an error result: %v", tool, res.Content)
	}
	text, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("call %s returned %T, want TextContent", tool, res.Content[0])
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text.Text), &decoded); err != nil {
		t.Fatalf("tool output is not JSON: %v (%q)", err, text.Text)
	}
	return text.Text, decoded
}

// serveCreate makes the fake answer POST /v1/work_items with rec.
func serveCreate(f *fakeAihub, rec map[string]any) {
	f.on("/v1/work_items", func(map[string]any) (int, any) { return http.StatusOK, rec })
}

// servePatch makes the fake answer PATCH /v1/work_items/<id> with rec.
func servePatch(f *fakeAihub, id string, rec map[string]any) {
	f.on("/v1/work_items/"+id, func(map[string]any) (int, any) { return http.StatusOK, rec })
}

// requireIdentityFieldsSurvive is the negative control that stops "delete
// everything" from passing every assertion in this file. id/slug/seq are what
// the whole call chain keys off — pf-work reports the slug, claim takes the id.
func requireIdentityFieldsSurvive(t *testing.T, got map[string]any) {
	t.Helper()
	for k, want := range map[string]any{"id": "wi_echo", "slug": "aihub#281", "seq": float64(281)} {
		if got[k] != want {
			t.Errorf("%s = %#v, want %#v — trimming the response must not touch the fields the call chain keys off", k, got[k], want)
		}
	}
	if got["goal"] == nil || got["attrs"] == nil || got["declared_resources"] == nil {
		t.Errorf("goal/attrs/declared_resources must survive; got %v / %v / %v",
			got["goal"], got["attrs"], got["declared_resources"])
	}
}

// TestCreateSuppressesTheContentItWasJustSent is the positive case on the
// create side. The content must be gone from the TEXT, not merely absent from
// some map: bytes in the transcript are the thing being paid for.
func TestCreateSuppressesTheContentItWasJustSent(t *testing.T) {
	f := newFakeAihub(t)
	serveCreate(f, workItemRecord("wi_echo", echoTestContent))

	text, got := callToolText(t, f, "pf_create_work_item", map[string]any{
		"project": "aihub", "goal": "a new work item", "content": echoTestContent,
	})

	if _, present := got["content"]; present {
		t.Errorf("content is still being echoed back")
	}
	if got["content_len"] != float64(len(echoTestContent)) {
		t.Errorf("content_len = %v, want %d (bytes of the content the caller sent)", got["content_len"], len(echoTestContent))
	}
	if strings.Contains(text, echoTestContent) {
		t.Errorf("the content body is still present in the response text")
	}
	requireIdentityFieldsSurvive(t, got)
}

// TestCreateKeepsContentTheCallerDidNotSend: a create that sent no content must
// not have anything deleted. Nothing produces this today — a work item's body
// at creation is whatever the caller supplied — but the guard is shape-driven
// rather than case-driven, and this is what says so.
func TestCreateKeepsContentTheCallerDidNotSend(t *testing.T) {
	f := newFakeAihub(t)
	serveCreate(f, workItemRecord("wi_echo", "content the caller never sent"))

	_, got := callToolText(t, f, "pf_create_work_item", map[string]any{
		"project": "aihub", "goal": "a new work item",
	})

	if got["content"] != "content the caller never sent" {
		t.Errorf("content = %v; a body the caller did not send is new information and must be returned", got["content"])
	}
	if _, present := got["content_len"]; present {
		t.Errorf("content_len must only appear when content was actually removed, got %v", got["content_len"])
	}
}

// TestUpdateSuppressesTheContentItWasJustSent is acceptance criterion 1 of the
// work item: content absent, content_len present and equal to what was sent.
func TestUpdateSuppressesTheContentItWasJustSent(t *testing.T) {
	f := newFakeAihub(t)
	servePatch(f, "wi_echo", workItemRecord("wi_echo", echoTestContent))

	text, got := callToolText(t, f, "pf_update_work_item", map[string]any{
		"work_item_id": "wi_echo", "content": echoTestContent,
	})

	if _, present := got["content"]; present {
		t.Errorf("content is still being echoed back")
	}
	if got["content_len"] != float64(len(echoTestContent)) {
		t.Errorf("content_len = %v, want %d", got["content_len"], len(echoTestContent))
	}
	if strings.Contains(text, echoTestContent) {
		t.Errorf("the content body is still present in the response text")
	}
	requireIdentityFieldsSurvive(t, got)
}

// TestUpdateWithoutContentStillReturnsTheFullBody is acceptance criterion 2 and
// the load-bearing negative control: it is what separates "suppress the echo"
// from "delete content always". An update that only touches priority has not
// seen the body, so the body is new information to it.
func TestUpdateWithoutContentStillReturnsTheFullBody(t *testing.T) {
	f := newFakeAihub(t)
	servePatch(f, "wi_echo", workItemRecord("wi_echo", echoTestContent))

	text, got := callToolText(t, f, "pf_update_work_item", map[string]any{
		"work_item_id": "wi_echo", "priority": "urgent",
	})

	if got["content"] != echoTestContent {
		t.Errorf("content was dropped from an update that never sent one — that is a lossy trim, not echo suppression")
	}
	if _, present := got["content_len"]; present {
		t.Errorf("content_len must not appear when nothing was removed, got %v", got["content_len"])
	}
	if !strings.Contains(text, "## Background") {
		t.Errorf("the body must reach the caller in full")
	}
}

// TestUpdateKeepsContentWhenTheStoredValueDiffers is why the drop is gated on
// equality rather than on "the caller sent something".
//
// The response is not a buffer echoed back: UpdateWorkItem commits, makes a
// synchronous embedding network call, and only then re-reads the row. A
// concurrent writer inside that window makes the stored content differ from
// what was sent — and that is precisely the call whose response the caller
// needs. Here the answer must be the full stored content, not a length.
// The cases are chosen so that each defeats a WEAKER comparison that would
// otherwise pass the whole suite. A single "wholly different" fixture does not:
// the original one here was 35 B against a 4.1 KB payload, so a length-only
// comparison satisfied it, and mutating `stored != sent` to
// `len(stored) != len(sent)` survived every test in this file.
func TestUpdateKeepsContentWhenTheStoredValueDiffers(t *testing.T) {
	const sent = "status: queued\nowner: nobody\n"
	for name, stored := range map[string]string{
		// Same BYTE LENGTH, different bytes. This is the case the test is named
		// for, stated so it can actually be seen: a caller PATCHes the body, a
		// concurrent writer flips queued->paused inside the embedding window, and
		// a length-gated comparison answers content_len: 29 with the body
		// dropped — the caller never learning it was clobbered.
		"same length, different bytes": "status: paused\nowner: nobody\n",
		// Differs ONLY in surrounding whitespace: what a server that started
		// trimming would produce, and what a TrimSpace-based comparison would
		// wave through. Byte lengths differ here, so this case is blind to a
		// length gate and the one above is blind to a trim gate — together they
		// cover both.
		"differs only in trailing whitespace": "status: queued\nowner: nobody",
		"wholly different":                    "what a concurrent writer left behind",
	} {
		t.Run(name, func(t *testing.T) {
			if stored == sent {
				t.Fatalf("fixture does not differ from what is sent; it cannot discriminate")
			}
			f := newFakeAihub(t)
			servePatch(f, "wi_echo", workItemRecord("wi_echo", stored))

			_, got := callToolText(t, f, "pf_update_work_item", map[string]any{
				"work_item_id": "wi_echo", "content": sent,
			})

			if got["content"] != stored {
				t.Errorf("content = %#v, want %#v — when the stored value differs from what was sent, "+
					"suppressing it hides a clobber from the one caller positioned to notice", got["content"], stored)
			}
			if _, present := got["content_len"]; present {
				t.Errorf("content_len must not appear when the content was kept, got %v", got["content_len"])
			}
		})
	}
}

// TestContentLenCountsBytesNotRunes. content_len is documented as bytes, to
// match Go's len() and the new_content_length the wi_content_updated event
// already carries for the same string. Every other fixture in this file is
// ASCII, where bytes and runes are the same number and a rune-counting
// content_len is invisible.
func TestContentLenCountsBytesNotRunes(t *testing.T) {
	content := "结论：先量再改 🎯 measure before you change anything"
	if len(content) == utf8.RuneCountInString(content) {
		t.Fatalf("fixture is pure ASCII (%d bytes, %d runes); it cannot tell the two units apart",
			len(content), utf8.RuneCountInString(content))
	}

	f := newFakeAihub(t)
	servePatch(f, "wi_echo", workItemRecord("wi_echo", content))
	_, got := callToolText(t, f, "pf_update_work_item", map[string]any{
		"work_item_id": "wi_echo", "content": content,
	})

	if got["content_len"] != float64(len(content)) {
		t.Errorf("content_len = %v, want %d bytes (rune count would be %d)",
			got["content_len"], len(content), utf8.RuneCountInString(content))
	}
}

// TestUpdateTreatsAJSONNullContentAsUnsent. parseArgs is a plain json.Unmarshal
// into map[string]any, so `content: null` puts the key in the map with a nil
// value — while the server's `Content *string` reads null and absent alike and
// leaves the stored body untouched, so nothing was echoed and nothing may be
// dropped.
//
// This case does NOT depend on the type assertion in contentSentByCaller, and an
// earlier version of this comment wrongly claimed it did. Under a bare presence
// check the caller's content reads as "", the equality gate compares "" against
// a non-empty stored body, and the drop is refused anyway. The assertion earns
// its place on one narrower input — see
// TestUpdateWithNullContentAgainstAnEmptyStoredBody, which is the case that
// actually goes red without it.
func TestUpdateTreatsAJSONNullContentAsUnsent(t *testing.T) {
	f := newFakeAihub(t)
	servePatch(f, "wi_echo", workItemRecord("wi_echo", "the body that was already stored"))

	_, got := callToolText(t, f, "pf_update_work_item", map[string]any{
		"work_item_id": "wi_echo", "content": nil,
	})

	if got["content"] != "the body that was already stored" {
		t.Errorf("content = %v; a null content changes nothing on the server, so nothing was echoed and nothing may be dropped", got["content"])
	}
}

// TestUpdateWithNullContentAgainstAnEmptyStoredBody is the single input on which
// contentSentByCaller's type assertion changes the answer, and therefore the
// only thing standing between that assertion and being deleted as decoration.
//
// `content: null` means "leave the body alone". Under a bare presence check the
// caller's content reads as the zero value "" — and here the STORED body is also
// "", so the equality gate is satisfied, and "leave the body alone" is answered
// with the field deleted and content_len: 0. A caller that sent no content would
// be told one had been suppressed, and could no longer tell an empty stored body
// from a withheld one.
//
// An empty stored body is reachable: content is CHECKed for a maximum length,
// not a minimum, so `content: ""` is a legal write.
func TestUpdateWithNullContentAgainstAnEmptyStoredBody(t *testing.T) {
	f := newFakeAihub(t)
	servePatch(f, "wi_echo", workItemRecord("wi_echo", ""))

	_, got := callToolText(t, f, "pf_update_work_item", map[string]any{
		"work_item_id": "wi_echo", "content": nil,
	})

	if v, present := got["content"]; !present || v != "" {
		t.Errorf("content = %#v (present=%v), want the stored empty string — a null content sends nothing, "+
			"so there is no echo to suppress even when the stored body happens to be empty too", v, present)
	}
	if _, present := got["content_len"]; present {
		t.Errorf("content_len = %v; reporting a length here claims a suppression that never happened",
			got["content_len"])
	}
}

// TestUpdateBriefDropsContentTheCallerNeverSent is the opt-in half: an update
// that touches only attrs still gets the whole body back (~80% of that
// response), and brief=true is how a caller that does not want it says so.
func TestUpdateBriefDropsContentTheCallerNeverSent(t *testing.T) {
	f := newFakeAihub(t)
	servePatch(f, "wi_echo", workItemRecord("wi_echo", echoTestContent))

	text, got := callToolText(t, f, "pf_update_work_item", map[string]any{
		"work_item_id": "wi_echo", "priority": "urgent", "brief": true,
	})

	if _, present := got["content"]; present {
		t.Errorf("brief=true must drop content even when the caller sent none")
	}
	if got["content_len"] != float64(len(echoTestContent)) {
		t.Errorf("content_len = %v, want %d — brief still has to say how much was left out", got["content_len"], len(echoTestContent))
	}
	if strings.Contains(text, "## Background") {
		t.Errorf("the body is still in the response text")
	}
	// pf-plan/SKILL.md reads resources_version off THIS response to feed the
	// next declared_resources compare-and-set. It is the one field any skill is
	// known to take from an update reply, so brief must not reach it.
	if got["resources_version"] != float64(2) {
		t.Errorf("resources_version = %v, want 2 — pf-plan re-reads it from the update response", got["resources_version"])
	}
	requireIdentityFieldsSurvive(t, got)
}

// TestUpdateBriefIsNotForwardedToTheServer is hop 2. `brief` shapes this
// process's reply and means nothing to aihub; forwarding a field the peer does
// not bind is how aihub#290's expected_version travelled the whole way to be
// discarded in silence.
func TestUpdateBriefIsNotForwardedToTheServer(t *testing.T) {
	f := newFakeAihub(t)
	servePatch(f, "wi_echo", workItemRecord("wi_echo", echoTestContent))

	callToolText(t, f, "pf_update_work_item", map[string]any{
		"work_item_id": "wi_echo", "priority": "urgent", "brief": true,
	})

	calls := f.recorded()
	if len(calls) != 1 {
		t.Fatalf("expected 1 HTTP call, got %d: %v", len(calls), f.paths())
	}
	if _, present := calls[0].Body["brief"]; present {
		t.Errorf("brief was forwarded to the server: %v", calls[0].Body["brief"])
	}
	if got := calls[0].Body["priority"]; got != "urgent" {
		t.Errorf("the rest of the body must be unaffected; priority = %v", got)
	}
}

// TestUpdateSendingAnEmptyContentToABodylessWorkItem pins the ONE input at which
// suppressContentEcho's `!ok` guard and dropContentEcho's type assertion could
// conceivably disagree, and documents why no test can force them to.
//
// Dropping the `!ok` leaves `stored != sent` comparing the zero value "" against
// what the caller sent, so the two spellings diverge only when the response
// content is not a string AND the caller sent exactly "". That is this call. And
// even here they agree: the mutant reaches dropContentEcho, which repeats the
// assertion, refuses, and changes nothing. So removing `!ok` is an EQUIVALENT
// mutation — unkillable by construction, not a hole in the suite. It is kept
// because it stops being equivalent the moment dropContentEcho's guard is
// relaxed, and this test is what pins the behaviour either way.
func TestUpdateSendingAnEmptyContentToABodylessWorkItem(t *testing.T) {
	rec := workItemRecordBase("wi_echo")
	rec["content"] = nil
	f := newFakeAihub(t)
	servePatch(f, "wi_echo", rec)

	_, got := callToolText(t, f, "pf_update_work_item", map[string]any{
		"work_item_id": "wi_echo", "content": "",
	})

	if v, present := got["content"]; !present || v != nil {
		t.Errorf("content = %#v (present=%v), want a surviving null", v, present)
	}
	if _, present := got["content_len"]; present {
		t.Errorf("content_len = %v; the response held no string to suppress", got["content_len"])
	}
}

// TestUpdateBriefLeavesABodylessWorkItemAlone pins what brief does when there is
// no body to replace, which is the half the published description used to get
// wrong.
//
// `content: null` stays, and no content_len is emitted. That is deliberate and
// it is the better of the two options: it leaves a POSITIVE signal. Under
// brief=true the response says either "content_len: N" (there is a body, this
// big) or "content: null" (there is none) — never nothing at all, which would
// make absence carry the meaning and reinstate the aihub#269 ambiguity the
// content_len handle exists to remove.
func TestUpdateBriefLeavesABodylessWorkItemAlone(t *testing.T) {
	rec := workItemRecordBase("wi_echo")
	rec["content"] = nil
	f := newFakeAihub(t)
	servePatch(f, "wi_echo", rec)

	_, got := callToolText(t, f, "pf_update_work_item", map[string]any{
		"work_item_id": "wi_echo", "priority": "urgent", "brief": true,
	})

	v, present := got["content"]
	if !present || v != nil {
		t.Errorf("content = %#v (present=%v), want a surviving null — a wi with no body has nothing to "+
			"replace, and deleting the key would leave absence as the only signal", v, present)
	}
	if _, present := got["content_len"]; present {
		t.Errorf("content_len = %v; a wi with no body has no length to report, and 0 would be "+
			"indistinguishable from a stored empty string", got["content_len"])
	}
}

// TestUpdateBriefAcceptsTheStringSpelling. The MCP SDK's untyped AddTool
// type-checks the schema at registration and then never validates a call, so
// nothing between the wire and the handler enforces the published `boolean` —
// and real callers demonstrably send the quoted form (csvArg and parseBoolArg
// both exist because of it, aihub#280). boolArg's tolerance is what makes
// `brief: "true"` work; without a test the tolerance could be narrowed back and
// brief would silently stop applying, which fails in the safe direction and so
// would never be noticed.
func TestUpdateBriefAcceptsTheStringSpelling(t *testing.T) {
	for _, spelling := range []any{"true", true, float64(1)} {
		f := newFakeAihub(t)
		servePatch(f, "wi_echo", workItemRecord("wi_echo", echoTestContent))

		_, got := callToolText(t, f, "pf_update_work_item", map[string]any{
			"work_item_id": "wi_echo", "priority": "urgent", "brief": spelling,
		})

		if _, present := got["content"]; present {
			t.Errorf("brief=%#v did not apply; the published type is not enforced anywhere on the wire, "+
				"so every spelling boolArg accepts has to keep working", spelling)
		}
		if got["content_len"] != float64(len(echoTestContent)) {
			t.Errorf("brief=%#v: content_len = %v, want %d", spelling, got["content_len"], len(echoTestContent))
		}
	}
}

// TestDomainWorkItemHasNoContentLenField is the other half of the delete-list
// argument, and the half that framing does not cover.
//
// wi_echo_slim.go removes only `content`, so a field added to domain.WorkItem
// reaches the caller untouched — but it also WRITES `content_len` into a map it
// does not own. If domain.WorkItem ever gains a field with that JSON name, the
// write clobbers the server's value in silence, and
// TestUpdateKeepsFieldsThisCodeHasNeverHeardOf cannot see it: that probes a name
// nothing produces, which is the opposite of a collision.
func TestDomainWorkItemHasNoContentLenField(t *testing.T) {
	typ := reflect.TypeOf(domain.WorkItem{})
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name == "content_len" {
			t.Fatalf("domain.WorkItem.%s is serialised as content_len, which wi_echo_slim.go overwrites "+
				"whenever it suppresses a body — rename one of the two", typ.Field(i).Name)
		}
	}
}

// TestUpdateKeepsFieldsThisCodeHasNeverHeardOf is the anti-whitelist assertion,
// and the reason wi_echo_slim.go is a delete-list.
//
// recall_slim.go's whitelist has swallowed a newly-added field three times —
// `total` (aihub#249), the truncation pair (aihub#269), `unmatched_types`
// (aihub#289) — because a field added downstream is dropped by default until
// somebody lists it. `a_field_added_after_this_test_was_written` is a field this
// binary cannot know about, and it must come straight through.
func TestUpdateKeepsFieldsThisCodeHasNeverHeardOf(t *testing.T) {
	f := newFakeAihub(t)
	rec := workItemRecord("wi_echo", echoTestContent)
	rec["a_field_added_after_this_test_was_written"] = "must survive"
	servePatch(f, "wi_echo", rec)

	_, got := callToolText(t, f, "pf_update_work_item", map[string]any{
		"work_item_id": "wi_echo", "content": echoTestContent,
	})

	if got["a_field_added_after_this_test_was_written"] != "must survive" {
		t.Errorf("an unknown field was dropped; this projection must name what it REMOVES, never what it keeps")
	}
}

// TestBatchCreateSuppressesEachItemsOwnContent. `created` carries one whole
// record per item, so this is where the echo is worst — and where it can most
// easily be got wrong, by testing the batch's arguments instead of the item's.
// Item 0 sent its content and must lose it; item 1 sent none and must keep what
// the server returned.
func TestBatchCreateSuppressesEachItemsOwnContent(t *testing.T) {
	f := newFakeAihub(t)
	n := 0
	f.on("/v1/work_items", func(body map[string]any) (int, any) {
		n++
		if n == 1 {
			return http.StatusOK, workItemRecord("wi_batch1", echoTestContent)
		}
		return http.StatusOK, workItemRecord("wi_batch2", "a body item 1 never sent")
	})

	text, got := callToolText(t, f, "pf_batch_create_work_items", map[string]any{
		"project": "aihub",
		"items": []any{
			map[string]any{"goal": "the one with a body", "content": echoTestContent},
			map[string]any{"goal": "the one without"},
		},
	})

	created, _ := got["created"].([]any)
	if len(created) != 2 {
		t.Fatalf("created = %v, want 2 items", got["created"])
	}
	first, _ := created[0].(map[string]any)
	if _, present := first["content"]; present {
		t.Errorf("item 0's own content is still echoed back")
	}
	if first["content_len"] != float64(len(echoTestContent)) {
		t.Errorf("item 0 content_len = %v, want %d", first["content_len"], len(echoTestContent))
	}
	// The identity control, per item. Without it a "trim everything" mutant
	// passes this case — the batch is the one response whose per-item records
	// requireIdentityFieldsSurvive above never reaches, and a mutation probe
	// found exactly that hole.
	if first["id"] != "wi_batch1" || first["slug"] == nil || first["goal"] == nil {
		t.Errorf("item 0 lost its identity fields (id=%v slug=%v goal=%v); a batch caller has nothing else to key off",
			first["id"], first["slug"], first["goal"])
	}
	second, _ := created[1].(map[string]any)
	if second["content"] != "a body item 1 never sent" {
		t.Errorf("item 1 content = %v; suppression must be judged per item, against that item's own arguments", second["content"])
	}
	if strings.Contains(text, echoTestContent) {
		t.Errorf("the echoed body is still in the batch response text")
	}
}

// TestContentEchoSuppressionShrinksTheResponse is the measurement, taken on the
// same bytes the model would receive.
//
// "before" is not an estimate: the handler used to end in `return
// jsonResult(result)` and jsonResult is marshalJSON, so marshalling the record
// the fake served IS the previous output, evaluated on this exact input. The
// round trip through encoding/json first is what the real response also makes
// (server JSON -> client map -> marshal), so the two numbers are comparable.
func TestContentEchoSuppressionShrinksTheResponse(t *testing.T) {
	rec := workItemRecord("wi_echo", echoTestContent)

	// What the pre-aihub#281 handler emitted for this record.
	viaWire, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	var roundTripped map[string]any
	if err := json.Unmarshal(viaWire, &roundTripped); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	beforeBytes, err := json.Marshal(roundTripped)
	if err != nil {
		t.Fatalf("remarshal fixture: %v", err)
	}
	before := len(beforeBytes)

	f := newFakeAihub(t)
	servePatch(f, "wi_echo", rec)
	text, _ := callToolText(t, f, "pf_update_work_item", map[string]any{
		"work_item_id": "wi_echo", "content": echoTestContent,
	})
	after := len(text)

	t.Logf("pf_update_work_item response: before=%d B  after=%d B  saved=%d B (%.1f%%)",
		before, after, before-after, 100*float64(before-after)/float64(before))

	// The content plus its key and quoting is what leaves; content_len is what
	// arrives. Anything materially off that means the wrong thing was trimmed.
	wantSaved := len(echoTestContent) + len(`"content":"",`) - len(`"content_len":1234`) - 1
	if got := before - after; got < wantSaved-8 || got > wantSaved+8 {
		t.Errorf("saved %d B, expected about %d B (the body, its key and quotes, less content_len)", got, wantSaved)
	}
	if after >= before {
		t.Errorf("response did not shrink: before=%d after=%d", before, after)
	}
}

// TestWorkItemToolSchemasPublishTheResponseShape is hop 1. A response-shape
// change that the published schema keeps quiet about is a contract the tool
// does not keep — aihub#238 and aihub#241 are both that defect. Asserted on
// every tool that carries a `content` parameter, so a fourth one added later
// cannot quietly ship the old wording.
func TestWorkItemToolSchemasPublishTheResponseShape(t *testing.T) {
	schemas := toolInputSchemas(t)

	for _, tool := range []string{"pf_create_work_item", "pf_update_work_item", "pf_batch_create_work_items"} {
		raw, ok := schemas[tool]
		if !ok {
			t.Fatalf("%s is not registered", tool)
		}
		desc := contentPropDescriptionFrom(t, tool, raw)
		if !strings.Contains(desc, "content_len") {
			t.Errorf("%s's content parameter does not mention content_len; the schema still describes the old response: %q", tool, desc)
		}
	}

	// brief is published on update only. Create needs no counterpart: a work
	// item's body at creation is whatever the caller supplied, so an unsent
	// content is an absent one and the equality gate already covers all of it.
	var updateProps struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schemas["pf_update_work_item"], &updateProps); err != nil {
		t.Fatalf("update schema is not valid JSON: %v", err)
	}
	if updateProps.Properties["brief"].Type != "boolean" {
		t.Errorf("pf_update_work_item must publish brief as a boolean, got %q", updateProps.Properties["brief"].Type)
	}
}

// toolInputSchemas lists every registered tool's published InputSchema by
// asking the server the way a client does, so the test reads the same value the
// caller is handed.
func toolInputSchemas(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	ctx := context.Background()
	server := mcp.New(nil, client.New("http://127.0.0.1:1", "test-key"))
	cTransport, sTransport := sdkmcp.NewInMemoryTransports()
	serverCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		session, err := server.Connect(serverCtx, sTransport)
		if err != nil {
			return
		}
		_ = session.Wait()
	}()
	cl := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "schema-test", Version: "1.0.0"}, nil)
	session, err := cl.Connect(ctx, cTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	res, err := session.ListTools(ctx, &sdkmcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	out := map[string]json.RawMessage{}
	for _, tool := range res.Tools {
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal %s schema: %v", tool.Name, err)
		}
		out[tool.Name] = raw
	}
	return out
}

// contentPropDescriptionFrom digs out the `content` parameter's description,
// following the batch tool's nested items.properties when needed.
func contentPropDescriptionFrom(t *testing.T, tool string, raw json.RawMessage) string {
	t.Helper()
	var schema struct {
		Properties map[string]struct {
			Description string `json:"description"`
			Items       struct {
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("%s schema is not valid JSON: %v", tool, err)
	}
	if p, ok := schema.Properties["content"]; ok {
		return p.Description
	}
	if items, ok := schema.Properties["items"]; ok {
		return items.Items.Properties["content"].Description
	}
	t.Fatalf("%s publishes no content parameter, directly or per item", tool)
	return ""
}
