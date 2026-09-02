package mcp_test

// aihub#325 — pf_reinforce_memory's `work_item_id`, end to end through the REAL
// stack: Postgres, the real echo router, the real pkg/client, the real MCP
// handler, and the text a model would actually receive.
//
// ─── Why this has to run against a real server ───────────────────────────────
//
// The defect is not "a key is missing from a map". It is that
// pf_reinforce_memory declares work_item_id REQUIRED in its own InputSchema,
// its handler refuses the call without one, and then builds a request body that
// does not contain it — so the server's non-methodology branch of
// enforceMethodologyAttemptGate (internal/server/routes_memory.go) answers
//
//	400  work_item_id is required when attempt_id/session_secret are provided
//
// to EVERY pf_reinforce_memory call on a non-methodology memory. The handler
// always sends attempt_id and session_secret (they come from the state file,
// unconditionally), so that branch is always taken and the tool was 100% broken
// for the type of memory it exists to strengthen.
//
// aihub#319's wiring tests could not see any of this: they serve the call from a
// fake server that accepts whatever it is handed. A fixture more permissive than
// the real server makes the last hop's assertion a fake one — the contract has
// N hops and needs N assertions, and the hop that REJECTS is the one that was
// never exercised. Hence a real router and a real gate here.
//
// Why nobody noticed: methodology.* memories take the OTHER branch of the same
// gate, which binds to the target memory's own work item and never reads the
// request's work_item_id at all. pf_save_artifact traffic therefore worked
// throughout.
//
// DB-gated in the AIHUB_TEST_DB style of internal/domain's integration tests, so
// a plain `go test ./...` skips it. Reuses newE2EStack from
// wi_echo_e2e_db_test.go.
//
//	AIHUB_TEST_DB='postgres://postgres:…@127.0.0.1:5446/aihub_test?sslmode=disable' \
//	  go test ./internal/mcp/ -run TestE2EReinforce -count=1 -v
//
// 🔴 NOT yet wired into ci.yml's aihub#303 coverage gate (-min-gated): that file
// is owned by another change in flight. See this wi's report for the number.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// callAllowingError invokes a tool and returns its text plus whether the tool
// reported an error, instead of failing the test on one.
//
// e2eStack.call cannot be used for the assertion below: it t.Fatalf's on
// IsError, which would report this wi's defect as "the harness blew up" rather
// than as the 400 it is. The message text is the evidence, so it has to come
// back.
func (s *e2eStack) callAllowingError(t *testing.T, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", tool, err)
	}
	text, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("call %s returned %T, want TextContent", tool, res.Content[0])
	}
	return text.Text, res.IsError
}

// claimedWorkItem creates a work item, claims it through the real server, and
// writes the credential state file the MCP handler will read — i.e. exactly the
// state a session is in when it calls pf_reinforce_memory.
//
// The state file goes into a per-test workspace root so the run cannot read (or
// write) the operator's real ~/…/.polyforge/state.
func claimedWorkItem(t *testing.T, s *e2eStack) string {
	t.Helper()
	ctx := context.Background()
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", t.TempDir())

	created, err := s.client.CreateWorkItem(ctx, map[string]any{
		"project": s.project,
		"goal":    "carry work_item_id through pf_reinforce_memory to the reinforce gate",
		"wi_type": "fix_bug", // the server refuses to claim a work item without one
	})
	if err != nil {
		t.Fatalf("create work item: %v", err)
	}
	wiID, _ := created["id"].(string)
	if wiID == "" {
		t.Fatalf("create work item returned no id: %v", created)
	}

	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		t.Fatalf("generate session secret: %v", err)
	}
	secret := hex.EncodeToString(secretBytes)

	claimed, err := s.client.ClaimWorkItem(ctx, wiID, map[string]any{
		"idempotency_key": fmt.Sprintf("idem-%d", time.Now().UnixNano()),
		"mode":            "fresh",
		"session_info": map[string]any{
			"machine_id":     "m_reinforce_e2e",
			"session_secret": secret,
		},
	})
	if err != nil {
		t.Fatalf("claim work item: %v", err)
	}
	attemptID, _ := claimed["attempt_id"].(string)
	epoch, _ := claimed["claim_epoch"].(float64)
	if attemptID == "" {
		t.Fatalf("claim returned no attempt_id: %v", claimed)
	}

	if err := config.WriteStateFile(&config.StateFile{
		WIID:          wiID,
		Project:       s.project,
		AttemptID:     attemptID,
		ClaimEpoch:    int64(epoch),
		SessionSecret: secret,
		Claimed:       true,
	}); err != nil {
		t.Fatalf("write state file: %v", err)
	}
	return wiID
}

// TestE2EReinforceMemoryWorkItemIDReachesTheGate is aihub#325's acceptance
// criterion: the call a caller actually makes SUCCEEDS.
//
// The discriminating observation is the tool's own success/failure, not a field
// in a map: with work_item_id absent from the body the real gate returns 400 and
// the model gets an error string, so "the field exists somewhere" cannot make
// this green.
func TestE2EReinforceMemoryWorkItemIDReachesTheGate(t *testing.T) {
	s := newE2EStack(t)
	ctx := context.Background()
	wiID := claimedWorkItem(t, s)

	// A NON-methodology memory: the branch of enforceMethodologyAttemptGate that
	// reads the request's work_item_id. A methodology.* memory would take the
	// other branch and pass either way, which is exactly why this went unnoticed.
	_, remembered := s.call(t, "pf_remember", map[string]any{
		"project":    s.project,
		"type":       "experience.debug",
		"content":    fmt.Sprintf("aihub#325 reinforce probe %d", time.Now().UnixNano()),
		"visibility": "project",
		"dedup_mode": "off",
	})
	memID, _ := remembered["id"].(string)
	if memID == "" {
		t.Fatalf("pf_remember returned no id: %v", remembered)
	}

	// Baseline arm: without it, a failure below would be consistent with a broken
	// fixture (unclaimed wi, wrong project, missing memory) rather than with the
	// dropped parameter. pf_activate_memory walks the same router, the same auth
	// and the same memory row, and carries no credentials at all.
	if _, activateErr := s.callAllowingError(t, "pf_activate_memory", map[string]any{"memory_id": memID}); activateErr {
		t.Fatalf("the control call pf_activate_memory failed; the fixture is broken, so the assertion below would prove nothing")
	}

	// Read the counter the control just moved, rather than assuming it. A
	// hard-coded expectation here would be measuring the control, not the
	// reinforce.
	var before int
	if err := s.pool.QueryRow(ctx, `SELECT activation_count FROM memories WHERE id=$1`, memID).Scan(&before); err != nil {
		t.Fatalf("read baseline activation_count: %v", err)
	}

	text, isErr := s.callAllowingError(t, "pf_reinforce_memory", map[string]any{
		"memory_id":          memID,
		"additional_context": "reinforced while proving aihub#325",
		"work_item_id":       wiID,
	})
	if isErr {
		t.Fatalf("pf_reinforce_memory failed against the REAL server: %s\n\n"+
			"work_item_id is REQUIRED by this tool's own InputSchema and its handler refuses the "+
			"call without one, yet the request body it builds omits it. The server's "+
			"non-methodology gate then rejects the credentials it was sent. aihub#319's wiring "+
			"tests stay green because their fake server accepts anything.", text)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("pf_reinforce_memory output is not JSON: %v (%q)", err, text)
	}
	if decoded["activation_count"] != float64(before+1) {
		t.Errorf("activation_count = %v, want %d — the reinforce did not take effect",
			decoded["activation_count"], before+1)
	}

	// The value has to have ARRIVED, not merely been accepted: the handler writes
	// it into attrs.reinforcements[].from_wi, which is the provenance of every
	// reinforcement and the only record of which session strengthened a memory.
	// A body that carried work_item_id past the gate but lost the value would
	// still pass the assertion above.
	var fromWI *string
	if err := s.pool.QueryRow(ctx,
		`SELECT attrs->'reinforcements'->0->>'from_wi' FROM memories WHERE id=$1`, memID,
	).Scan(&fromWI); err != nil {
		t.Fatalf("read back reinforcement record: %v", err)
	}
	if fromWI == nil {
		t.Fatalf("attrs.reinforcements[0].from_wi is absent: the reinforcement was recorded with no "+
			"provenance, because the server saw an empty work_item_id (memory %s, wi %s)", memID, wiID)
	}
	if *fromWI != wiID {
		t.Errorf("attrs.reinforcements[0].from_wi = %q, want %q", *fromWI, wiID)
	}
}

// TestE2EReinforceMemoryRejectsAMismatchedWorkItem keeps the fix from being
// satisfied by forwarding a constant, or by the server having stopped verifying.
//
// The gate does not merely require work_item_id to be PRESENT — it verifies the
// attempt credentials against that work item. A second claimed work item's id,
// sent with the first one's credentials, must be refused. If this ever passes,
// the credential check has become decorative and the whole reason the field is
// required has gone.
func TestE2EReinforceMemoryRejectsAMismatchedWorkItem(t *testing.T) {
	s := newE2EStack(t)
	wiID := claimedWorkItem(t, s)

	_, remembered := s.call(t, "pf_remember", map[string]any{
		"project":    s.project,
		"type":       "experience.debug",
		"content":    fmt.Sprintf("aihub#325 mismatch probe %d", time.Now().UnixNano()),
		"visibility": "project",
		"dedup_mode": "off",
	})
	memID, _ := remembered["id"].(string)
	if memID == "" {
		t.Fatalf("pf_remember returned no id: %v", remembered)
	}

	// A second work item whose state file carries the FIRST one's credentials:
	// the shape a copy-pasted or stale session produces.
	other, err := s.client.CreateWorkItem(context.Background(), map[string]any{
		"project": s.project,
		"goal":    "a different work item, never claimed by this session's attempt",
	})
	if err != nil {
		t.Fatalf("create second work item: %v", err)
	}
	otherID, _ := other["id"].(string)
	sf, err := config.ResolveStateFile(wiID)
	if err != nil {
		t.Fatalf("resolve state file: %v", err)
	}
	sf.WIID = otherID
	if err := config.WriteStateFile(sf); err != nil {
		t.Fatalf("write second state file: %v", err)
	}

	text, isErr := s.callAllowingError(t, "pf_reinforce_memory", map[string]any{
		"memory_id":          memID,
		"additional_context": "reinforced with a work_item_id the credentials do not belong to",
		"work_item_id":       otherID,
	})
	if !isErr {
		t.Fatalf("pf_reinforce_memory accepted work_item_id=%s carrying another work item's attempt "+
			"credentials: %s\nThe server must verify the pair, not just see the key present.", otherID, text)
	}
	// 🔴 The rejection must be the CREDENTIAL one. Before this wi's fix the body
	// carried no work_item_id at all, so this case failed with "work_item_id is
	// required" — the right verdict from the wrong cause, and a test that would
	// have stayed green while proving nothing about verification.
	if strings.Contains(text, "work_item_id is required") {
		t.Fatalf("the call was rejected for MISSING work_item_id, not for a mismatched one: %s\n"+
			"This case only discriminates once the field is actually on the wire.", text)
	}
}
