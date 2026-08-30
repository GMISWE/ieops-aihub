package mcp_test

// aihub#290, the transport half: end-to-end MCP tool calls driven against a fake
// aihub, asserting what actually goes over the wire.
//
// The schema tests in tools_step_contract_test.go prove the fused parameters are
// reachable. These prove they do something — and, for the two failure paths,
// that a fused call reports what it already did instead of hiding it. That
// distinction is the whole risk of fusing: three observable calls become one, so
// anything the caller used to learn by watching the intermediate responses has
// to be carried explicitly by the single response that replaces them.
//
// Run: go test ./internal/mcp/ -run 'TestFused|TestBatch' -v   (no database needed)

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/mcp"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// recordedCall is one HTTP request the MCP server made to aihub.
type recordedCall struct {
	Method string
	Path   string
	Body   map[string]any
}

// fakeAihub records every request and answers from a per-path handler table.
// Absent paths return {"ok":true}.
type fakeAihub struct {
	mu       sync.Mutex
	calls    []recordedCall
	handlers map[string]func(body map[string]any) (int, any)
	server   *httptest.Server
}

func newFakeAihub(t *testing.T) *fakeAihub {
	t.Helper()
	f := &fakeAihub{handlers: map[string]func(map[string]any) (int, any){}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)

		f.mu.Lock()
		f.calls = append(f.calls, recordedCall{Method: r.Method, Path: r.URL.Path, Body: body})
		h := f.handlers[r.URL.Path]
		f.mu.Unlock()

		status, payload := http.StatusOK, any(map[string]any{"ok": true})
		if h != nil {
			status, payload = h(body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeAihub) on(path string, h func(body map[string]any) (int, any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[path] = h
}

func (f *fakeAihub) recorded() []recordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// paths returns the ordered request paths, which is how the ordering assertions
// are phrased (the note must precede the terminal call, not merely accompany it).
func (f *fakeAihub) paths() []string {
	var out []string
	for _, c := range f.recorded() {
		out = append(out, c.Path)
	}
	return out
}

// callTool connects a client session to an MCP server backed by the fake aihub
// and invokes one tool, returning its decoded JSON result.
func callTool(t *testing.T, f *fakeAihub, tool string, args map[string]any) (map[string]any, bool) {
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

	cl := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "fusion-test", Version: "1.0.0"}, nil)
	session, err := cl.Connect(ctx, cTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", tool, err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("call %s returned no content", tool)
	}
	text, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("call %s returned %T, want TextContent", tool, res.Content[0])
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text.Text), &decoded); err != nil {
		// Error results are bare strings, not JSON. Hand the text back so the
		// caller can assert on it.
		return map[string]any{"_raw": text.Text}, res.IsError
	}
	return decoded, res.IsError
}

// seedStateFile points the state directory at a temp dir and writes credentials
// for wiID, so tools that inject them from disk can run.
func seedStateFile(t *testing.T, wiID string) {
	t.Helper()
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", t.TempDir())
	if err := config.WriteStateFile(&config.StateFile{
		WIID:          wiID,
		AttemptID:     "ra_test",
		ClaimEpoch:    1,
		SessionSecret: "secret",
		Claimed:       true,
	}); err != nil {
		t.Fatalf("write state file: %v", err)
	}
}

// TestFusedNoteReachesTimelineBeforeTerminalCall is the B3 behaviour and its
// ordering constraint in one: the note must be a real event, and it must be sent
// BEFORE the completion — the completion deletes the state file, so a note
// emitted after it has no credentials to authenticate with. Every skill that
// emits a wrap note carries a hand-written warning about this ordering; folding
// the note into the call is what makes the warning unnecessary.
func TestFusedNoteReachesTimelineBeforeTerminalCall(t *testing.T) {
	const wiID = "wi_notefuse"
	seedStateFile(t, wiID)
	f := newFakeAihub(t)

	result, isErr := callTool(t, f, "pf_complete_attempt", map[string]any{
		"work_item_id": wiID,
		"status":       "wrapped",
		"note":         "wrapped: fused the adjacent round-trips",
	})
	if isErr {
		t.Fatalf("pf_complete_attempt failed: %v", result)
	}

	calls := f.recorded()
	if len(calls) != 2 {
		t.Fatalf("expected exactly 2 HTTP calls (note + complete), got %d: %v", len(calls), f.paths())
	}
	if calls[0].Path != "/v1/events" {
		t.Errorf("first call was %s, want /v1/events — the note has to precede the terminal call", calls[0].Path)
	}
	if !strings.HasSuffix(calls[1].Path, "/complete") {
		t.Errorf("second call was %s, want .../complete", calls[1].Path)
	}

	if got := calls[0].Body["event_type"]; got != "note" {
		t.Errorf("event_type = %v, want \"note\"", got)
	}
	payload, ok := calls[0].Body["payload"].(map[string]any)
	if !ok {
		t.Fatalf("note payload is %T, want an object", calls[0].Body["payload"])
	}
	if got := payload["text"]; got != "wrapped: fused the adjacent round-trips" {
		t.Errorf("note payload text = %v; the shape must stay {text: ...} to match every existing note event", got)
	}
	if got := result["note_emitted"]; got != true {
		t.Errorf("note_emitted = %v, want true", got)
	}
}

// TestFusedNoteAbsentMeansNoEvent: not passing a note must cost nothing and must
// not report a note field at all, so "no note requested" stays distinguishable
// from "note requested and failed".
func TestFusedNoteAbsentMeansNoEvent(t *testing.T) {
	const wiID = "wi_nonote"
	seedStateFile(t, wiID)
	f := newFakeAihub(t)

	result, isErr := callTool(t, f, "pf_complete_attempt", map[string]any{
		"work_item_id": wiID, "status": "wrapped",
	})
	if isErr {
		t.Fatalf("pf_complete_attempt failed: %v", result)
	}
	for _, p := range f.paths() {
		if p == "/v1/events" {
			t.Errorf("an event was emitted with no note requested: %v", f.paths())
		}
	}
	if _, present := result["note_emitted"]; present {
		t.Errorf("note_emitted must be absent when no note was requested, got %v", result["note_emitted"])
	}
}

// TestFusedNoteFailureIsReportedNotSwallowed: the note is emitted at the one
// moment it can never be re-sent, so losing it silently is not acceptable — but
// neither is failing the wrap over it. The response has to say so.
func TestFusedNoteFailureIsReportedNotSwallowed(t *testing.T) {
	const wiID = "wi_notefail"
	seedStateFile(t, wiID)
	f := newFakeAihub(t)
	f.on("/v1/events", func(map[string]any) (int, any) {
		return http.StatusInternalServerError, map[string]any{"code": "INTERNAL", "message": "event store down"}
	})

	result, isErr := callTool(t, f, "pf_complete_attempt", map[string]any{
		"work_item_id": wiID, "status": "wrapped", "note": "wrapped: something",
	})
	if isErr {
		t.Fatalf("a failed note must not fail the wrap: %v", result)
	}
	if got := result["note_emitted"]; got != false {
		t.Errorf("note_emitted = %v, want false", got)
	}
	if _, ok := result["note_error"].(string); !ok {
		t.Errorf("note_error must carry the reason, got %v", result["note_error"])
	}
	// The wrap itself still happened — that is the half worth keeping.
	var completed bool
	for _, p := range f.paths() {
		if strings.HasSuffix(p, "/complete") {
			completed = true
		}
	}
	if !completed {
		t.Errorf("the attempt was not completed: %v", f.paths())
	}
}

// TestBatchCreateSendsOneCallPerItemAndInheritsProject: the batch tool exists to
// spend ONE MCP round-trip, not one HTTP call — the HTTP calls are cheap and the
// round-trip is what costs a whole request prefix. This pins that split, and the
// project default that makes the items terse enough to be worth batching.
func TestBatchCreateSendsOneCallPerItemAndInheritsProject(t *testing.T) {
	f := newFakeAihub(t)
	seq := 0
	f.on("/v1/work_items", func(body map[string]any) (int, any) {
		seq++
		return http.StatusOK, map[string]any{"id": "wi_00" + string(rune('0'+seq)), "goal": body["goal"]}
	})

	result, isErr := callTool(t, f, "pf_batch_create_work_items", map[string]any{
		"project": "aihub",
		"items": []any{
			map[string]any{"goal": "first follow-up", "wi_type": "fix_bug"},
			map[string]any{"goal": "second follow-up", "project": "marketplace"},
		},
	})
	if isErr {
		t.Fatalf("pf_batch_create_work_items failed: %v", result)
	}

	calls := f.recorded()
	if len(calls) != 2 {
		t.Fatalf("expected 2 create calls, got %d: %v", len(calls), f.paths())
	}
	if got := calls[0].Body["project"]; got != "aihub" {
		t.Errorf("item 0 project = %v, want the inherited \"aihub\"", got)
	}
	if got := calls[1].Body["project"]; got != "marketplace" {
		t.Errorf("item 1 project = %v, want its own \"marketplace\" — a per-item project must win over the default", got)
	}
	if got := calls[0].Body["wi_type"]; got != "fix_bug" {
		t.Errorf("item 0 wi_type = %v, want fix_bug", got)
	}
	if got := result["created_count"]; got != float64(2) {
		t.Errorf("created_count = %v, want 2", got)
	}
	if got := result["ok"]; got != true {
		t.Errorf("ok = %v, want true", got)
	}
}

// TestBatchCreateContinuesPastAFailedItem is the batch equivalent of pf_ship's
// failure contract. Duplicate detection runs per item and a 409 is a routine
// outcome, so aborting the batch would leave the caller knowing only that "it
// failed" — and re-sending the whole batch would then trip dedup on the items
// that DID land. Each failure is reported with its index so a retry can resend
// exactly the ones that did not.
func TestBatchCreateContinuesPastAFailedItem(t *testing.T) {
	f := newFakeAihub(t)
	f.on("/v1/work_items", func(body map[string]any) (int, any) {
		if body["goal"] == "duplicate one" {
			return http.StatusConflict, map[string]any{"code": "DUPLICATE", "message": "a similar wi exists"}
		}
		return http.StatusOK, map[string]any{"id": "wi_ok", "goal": body["goal"]}
	})

	result, isErr := callTool(t, f, "pf_batch_create_work_items", map[string]any{
		"project": "aihub",
		"items": []any{
			map[string]any{"goal": "fine one"},
			map[string]any{"goal": "duplicate one"},
			map[string]any{"goal": "another fine one"},
		},
	})
	if isErr {
		t.Fatalf("a per-item failure must not fail the whole tool call: %v", result)
	}

	if len(f.recorded()) != 3 {
		t.Errorf("expected all 3 items attempted, got %d — the batch stopped early", len(f.recorded()))
	}
	if got := result["created_count"]; got != float64(2) {
		t.Errorf("created_count = %v, want 2", got)
	}
	if got := result["failed_count"]; got != float64(1) {
		t.Errorf("failed_count = %v, want 1", got)
	}
	if got := result["ok"]; got != false {
		t.Errorf("ok = %v, want false when any item failed", got)
	}

	failed, _ := result["failed"].([]any)
	if len(failed) != 1 {
		t.Fatalf("failed = %v, want exactly one entry", result["failed"])
	}
	entry, _ := failed[0].(map[string]any)
	if got := entry["index"]; got != float64(1) {
		t.Errorf("failed index = %v, want 1 — without it a retry cannot tell which item to resend", got)
	}
	if got, _ := entry["error"].(string); !strings.Contains(got, "DUPLICATE") {
		t.Errorf("failed error = %q, want it to carry the server's code", got)
	}
	if got := entry["goal"]; got != "duplicate one" {
		t.Errorf("failed goal = %v, want the item's goal", got)
	}
}

// TestBatchCreateRejectsMalformedItemsWithoutSendingThem: a bad entry is
// reported against its index and costs no HTTP call, rather than being skipped
// silently or aborting the good ones around it.
func TestBatchCreateRejectsMalformedItemsWithoutSendingThem(t *testing.T) {
	f := newFakeAihub(t)

	result, isErr := callTool(t, f, "pf_batch_create_work_items", map[string]any{
		"project": "aihub",
		"items": []any{
			map[string]any{"wi_type": "chore"}, // no goal
			"not an object",
			map[string]any{"goal": "the good one"},
		},
	})
	if isErr {
		t.Fatalf("unexpected tool error: %v", result)
	}
	if len(f.recorded()) != 1 {
		t.Errorf("expected only the valid item to be sent, got %d calls", len(f.recorded()))
	}
	if got := result["failed_count"]; got != float64(2) {
		t.Errorf("failed_count = %v, want 2", got)
	}
	if got := result["created_count"]; got != float64(1) {
		t.Errorf("created_count = %v, want 1 — a malformed sibling must not stop a valid item", got)
	}
}

// TestBatchCreateRequiresANonEmptyItemsArray: the empty call is a caller mistake
// worth naming, not a silent no-op that looks like success.
func TestBatchCreateRequiresANonEmptyItemsArray(t *testing.T) {
	f := newFakeAihub(t)
	result, isErr := callTool(t, f, "pf_batch_create_work_items", map[string]any{
		"project": "aihub", "items": []any{},
	})
	if !isErr {
		t.Errorf("an empty items array must be an error, got %v", result)
	}
	if len(f.recorded()) != 0 {
		t.Errorf("nothing should have been sent, got %v", f.paths())
	}
}

// TestBatchCreateRefusesAnOversizedBatchWithoutCreatingAnything: each item is a
// sequential HTTP call inside one tool call, so an unbounded array turns a single
// MCP call into an arbitrarily long silent stall. Refused rather than truncated —
// silently creating a prefix of what was asked for would be worse than creating
// nothing, because the caller cannot see which prefix.
func TestBatchCreateRefusesAnOversizedBatchWithoutCreatingAnything(t *testing.T) {
	f := newFakeAihub(t)
	items := make([]any, 0, 51)
	for i := 0; i < 51; i++ {
		items = append(items, map[string]any{"goal": fmt.Sprintf("item %d", i)})
	}

	result, isErr := callTool(t, f, "pf_batch_create_work_items", map[string]any{
		"project": "aihub", "items": items,
	})
	if !isErr {
		t.Fatalf("an oversized batch must be an error, got %v", result)
	}
	if len(f.recorded()) != 0 {
		t.Errorf("an oversized batch must create nothing at all, got %d calls", len(f.recorded()))
	}

	// The boundary itself must still be accepted, or the cap is off by one.
	f2 := newFakeAihub(t)
	result2, isErr2 := callTool(t, f2, "pf_batch_create_work_items", map[string]any{
		"project": "aihub", "items": items[:50],
	})
	if isErr2 {
		t.Fatalf("a batch exactly at the limit must be accepted: %v", result2)
	}
	if len(f2.recorded()) != 50 {
		t.Errorf("expected 50 create calls at the limit, got %d", len(f2.recorded()))
	}
}

// TestFusedUpdateStepForwardsNextStep closes the MCP half of B1: the argument
// has to reach the request body under the key the server binds. The schema test
// proves it is declared; this proves it is sent.
func TestFusedUpdateStepForwardsNextStep(t *testing.T) {
	const wiID = "wi_advance"
	seedStateFile(t, wiID)
	f := newFakeAihub(t)

	result, isErr := callTool(t, f, "pf_update_step", map[string]any{
		"work_item_id":         wiID,
		"step_id":              "code_change",
		"status":               "completed",
		"step_attempt_id":      "sa_done",
		"next_step":            "commit_and_pr",
		"next_step_attempt_id": "sa_next",
	})
	if isErr {
		t.Fatalf("pf_update_step failed: %v", result)
	}

	calls := f.recorded()
	if len(calls) != 1 {
		t.Fatalf("expected 1 HTTP call, got %d: %v", len(calls), f.paths())
	}
	body := calls[0].Body
	for key, want := range map[string]any{
		"step":                 "code_change",
		"status":               "completed",
		"step_attempt_id":      "sa_done",
		"next_step":            "commit_and_pr",
		"next_step_attempt_id": "sa_next",
	} {
		if got := body[key]; got != want {
			t.Errorf("body[%q] = %v, want %v", key, got, want)
		}
	}
	if _, present := body["expected_version"]; present {
		t.Errorf("expected_version is still being sent: %v", body["expected_version"])
	}
}

// TestFusedUpdateStepRejectsNextStepOnNonCompletion checks the MCP layer refuses
// the combination before it costs an HTTP call, and — the part that matters —
// refuses it rather than dropping it.
func TestFusedUpdateStepRejectsNextStepOnNonCompletion(t *testing.T) {
	const wiID = "wi_badadvance"
	seedStateFile(t, wiID)
	f := newFakeAihub(t)

	result, isErr := callTool(t, f, "pf_update_step", map[string]any{
		"work_item_id": wiID, "step_id": "code_change",
		"status": "in_progress", "next_step": "commit_and_pr",
	})
	if !isErr {
		t.Fatalf("next_step with status=in_progress must be an error, got %v", result)
	}
	if raw, _ := result["_raw"].(string); !strings.Contains(raw, "next_step") {
		t.Errorf("the error must name next_step, got %q", raw)
	}
	if len(f.recorded()) != 0 {
		t.Errorf("nothing should have been sent to aihub, got %v", f.paths())
	}
}

// TestOldPluginArgumentsStillWorkAgainstNewBinary is the backward-compatibility
// half of aihub#290, and it is not hypothetical: the polyforge CLI binary and
// the Claude Code plugin update on INDEPENDENT channels (~/.polyforge/config.toml
// has its own [binary] channel), so a machine can be running a NEW binary with an
// OLD plugin whose skill text still passes expected_version.
//
// That combination must keep working. The MCP SDK hands undeclared arguments to
// the handler rather than rejecting the call, so the requirement is that the
// handler IGNORE expected_version — not error on it, and not forward it.
func TestOldPluginArgumentsStillWorkAgainstNewBinary(t *testing.T) {
	const wiID = "wi_oldplugin"
	seedStateFile(t, wiID)
	f := newFakeAihub(t)

	// Exactly what a pre-aihub#290 skill emits: no next_step, and an
	// expected_version fetched from a pf_get_step that the old text still makes.
	result, isErr := callTool(t, f, "pf_update_step", map[string]any{
		"work_item_id":     wiID,
		"step_id":          "code_change",
		"status":           "in_progress",
		"expected_version": "7",
	})
	if isErr {
		t.Fatalf("an old plugin's call must still succeed against a new binary: %v", result)
	}

	calls := f.recorded()
	if len(calls) != 1 {
		t.Fatalf("expected 1 HTTP call, got %d: %v", len(calls), f.paths())
	}
	if _, present := calls[0].Body["expected_version"]; present {
		t.Errorf("expected_version was forwarded to the server: %v", calls[0].Body["expected_version"])
	}
	if got := calls[0].Body["step"]; got != "code_change" {
		t.Errorf("the rest of the call must be unaffected; step = %v", got)
	}
	if got := calls[0].Body["status"]; got != "in_progress" {
		t.Errorf("status = %v, want in_progress", got)
	}
}

// TestFusedUpdateStepRejectsNextStepOnHeartbeat is the ordering trap. The
// heartbeat branch returns early with a body of nothing but credentials, so a
// heartbeat carrying next_step is answered "heartbeat_ok" while the successor
// silently never starts — the same silent drop this work item removed,
// reintroduced on the parameter that replaced it.
//
// The status="completed" row is the one that matters and the one a status-only
// guard misses: `heartbeat` is selected by its own flag, not by the status, so
// such a request satisfies `status == "completed"` and walks straight into the
// early return. Verified to reach the handler unrejected before the fix.
func TestFusedUpdateStepRejectsNextStepOnHeartbeat(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"status in_progress": {"status": "in_progress", "heartbeat": true, "next_step": "commit_and_pr"},
		"status completed":   {"status": "completed", "heartbeat": true, "next_step": "commit_and_pr"},
		"no status":          {"heartbeat": true, "next_step": "commit_and_pr"},
	} {
		t.Run(name, func(t *testing.T) {
			wiID := "wi_hbadvance"
			seedStateFile(t, wiID)
			f := newFakeAihub(t)

			args["work_item_id"] = wiID
			args["step_id"] = "code_change"
			result, isErr := callTool(t, f, "pf_update_step", args)
			if !isErr {
				t.Fatalf("a heartbeat carrying next_step must be an error, got %v", result)
			}
			if raw, _ := result["_raw"].(string); !strings.Contains(raw, "heartbeat") {
				t.Errorf("the error should name the heartbeat conflict, got %q", raw)
			}
			if len(f.recorded()) != 0 {
				t.Errorf("nothing should have been sent to aihub, got %v", f.paths())
			}
		})
	}
}

// TestFusedUpdateStepRejectsOrphanNextStepAttemptID: next_step_attempt_id names
// the attempt of the step being STARTED, so without next_step no step is being
// started and nothing would ever read it. Before the fix it was forwarded to the
// server and bound into a field no branch consults — a declared parameter
// discarded in silence, which is the defect this work item is named after.
func TestFusedUpdateStepRejectsOrphanNextStepAttemptID(t *testing.T) {
	const wiID = "wi_orphansa"
	seedStateFile(t, wiID)
	f := newFakeAihub(t)

	result, isErr := callTool(t, f, "pf_update_step", map[string]any{
		"work_item_id": wiID, "step_id": "code_change", "status": "completed",
		"next_step_attempt_id": "sa_orphan",
	})
	if !isErr {
		t.Fatalf("next_step_attempt_id without next_step must be an error, got %v", result)
	}
	if raw, _ := result["_raw"].(string); !strings.Contains(raw, "next_step_attempt_id") {
		t.Errorf("the error must name next_step_attempt_id, got %q", raw)
	}
	if len(f.recorded()) != 0 {
		t.Errorf("nothing should have been sent to aihub, got %v", f.paths())
	}
}

// TestPlainHeartbeatStillWorks is the control for the test above: moving the
// next_step check ahead of the heartbeat branch must not disturb ordinary
// heartbeats, which send no status at all.
func TestPlainHeartbeatStillWorks(t *testing.T) {
	const wiID = "wi_plainhb"
	seedStateFile(t, wiID)
	f := newFakeAihub(t)

	result, isErr := callTool(t, f, "pf_update_step", map[string]any{
		"work_item_id": wiID, "step_id": "code_change",
		"status": "in_progress", "heartbeat": true,
	})
	if isErr {
		t.Fatalf("a plain heartbeat must still succeed: %v", result)
	}
	calls := f.recorded()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(calls), f.paths())
	}
	if got := calls[0].Body["heartbeat"]; got != true {
		t.Errorf("heartbeat flag = %v, want true", got)
	}
}
