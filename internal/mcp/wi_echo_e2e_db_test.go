package mcp_test

// aihub#281, hops 1 through 5 against the REAL stack: Postgres, the real echo
// router, the real pkg/client, the real MCP handler, and the text a model would
// actually receive.
//
// ─── Why this exists when wi_echo_test.go already passes ────────────────────
//
// Those cases serve the work item from a fake, so they prove the SUPPRESSION
// works on whatever they are handed. They cannot prove the two things the
// suppression depends on being true of the real server:
//
//  1. that a create/update response still carries `content` at all. If the HTTP
//     layer ever stopped sending it, every "content is absent from the output"
//     assertion would stay green while measuring nothing — the reference side of
//     a differential test lying, and the failure mode the three recall_slim
//     regressions all shared.
//
//  2. that the stored content comes back BYTE-IDENTICAL to what was sent. The
//     drop is gated on that equality, so a server that started trimming
//     whitespace or normalising line endings would silently stop the saving with
//     nothing anywhere going red. TestRealServerStoresContentByteIdentically is
//     that alarm.
//
// DB-gated in the AIHUB_TEST_DB style of internal/domain's integration tests, so
// a plain `go test ./...` skips it.
//
//	AIHUB_TEST_DB='postgres://postgres:test@127.0.0.1:5433/aihub_test?sslmode=disable' \
//	  go test ./internal/mcp/ -run TestE2E -count=1 -v
//
// The DB must already be migrated (goose -dir internal/db/migrations ... up);
// like setupLatestTestDB this test connects, it does not migrate.
//
// WIRED INTO CI by aihub#303, which held ci.yml while this change was written:
// the "aihub#281 wi-content echo E2E DB tests" step runs both cases with -v
// against the job's Postgres service and asserts a `--- PASS` line per case
// plus no `--- SKIP`, because `go test` prints ok and exits 0 when everything
// skips. aihub#303 did not find this suite by reading the deferral note — its
// new coverage gate computed the difference between the DB-gated tests in the
// repo and the ones any `-run` in ci.yml names, and these two came out as the
// only gap on the merge of the two branches.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/auth"
	"github.com/GMISWE/ieops-aihub/internal/mcp"
	"github.com/GMISWE/ieops-aihub/internal/server"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// e2eContent is a body the size real callers send (the measured sample averaged
// ~4.7 KB, maximum 17,870 characters). A short string would let a size
// regression hide inside the fixed part of the record.
var e2eContent = "## Spec\n\n" + strings.Repeat(
	"A paragraph of the sort a spec or plan artifact carries, long enough to matter. ", 50)

// e2eStack is one wired-up copy of the real thing: migrated DB, real router,
// real client, real MCP server.
type e2eStack struct {
	pool    *pgxpool.Pool
	session *sdkmcp.ClientSession
	client  *client.Client
	project string
}

// newE2EStack seeds a user, an API key and a project, then stands up the real
// echo router in-process and points a real MCP session at it through the real
// SDK client.
func newE2EStack(t *testing.T) *e2eStack {
	t.Helper()
	dbURL := os.Getenv("AIHUB_TEST_DB")
	if dbURL == "" {
		t.Skip("set AIHUB_TEST_DB to run this integration test")
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect to AIHUB_TEST_DB: %v", err)
	}
	t.Cleanup(pool.Close)

	const (
		uid    = "u_echo_e2e"
		rawKey = "pfk_echo_e2e_test_key"
	)
	project := "p_echo_e2e"

	// role=admin so project access is not what this test is about.
	keys, err := json.Marshal([]map[string]any{{"id": "k_echo", "key_hash": auth.HashKey(rawKey)}})
	if err != nil {
		t.Fatalf("marshal api keys: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users(id,email,display_name,user_type,role,api_keys)
		VALUES($1,$1||'@test.local',$1,'human','admin',$2)
		ON CONFLICT (id) DO UPDATE SET api_keys=EXCLUDED.api_keys, role='admin'`, uid, keys); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO projects(name,owner_user_id) VALUES($1,$2) ON CONFLICT (name) DO NOTHING`,
		project, uid); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	// CreateWorkItem runs goal-similarity dedup against live work items in the
	// project, so a previous run's rows would reject this run's create. Clear
	// child-to-parent, the order seedStepTestWI uses.
	for _, q := range []string{
		`DELETE FROM wi_step_completions WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM agent_events WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM wi_step_state WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`UPDATE work_items SET current_attempt_id=NULL WHERE project=$1`,
		`DELETE FROM run_attempts WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM memories WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM work_items WHERE project=$1`,
	} {
		if _, err := pool.Exec(ctx, q, project); err != nil {
			t.Fatalf("clean fixture (%s): %v", q, err)
		}
	}

	ts := httptest.NewServer(server.NewRouter(pool, []byte("e2e-test-cookie-secret-not-a-real-one")))
	t.Cleanup(ts.Close)

	aihubClient := client.New(ts.URL, rawKey)
	mcpServer := mcp.New(nil, aihubClient)
	cTransport, sTransport := sdkmcp.NewInMemoryTransports()
	serverCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		s, err := mcpServer.Connect(serverCtx, sTransport)
		if err != nil {
			return
		}
		_ = s.Wait()
	}()
	cl := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "echo-e2e", Version: "1.0.0"}, nil)
	session, err := cl.Connect(ctx, cTransport, nil)
	if err != nil {
		t.Fatalf("mcp client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	return &e2eStack{pool: pool, session: session, client: aihubClient, project: project}
}

// call invokes a tool and returns the response text and its decoded form.
func (s *e2eStack) call(t *testing.T, tool string, args map[string]any) (string, map[string]any) {
	t.Helper()
	res, err := s.session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", tool, err)
	}
	text, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("call %s returned %T, want TextContent", tool, res.Content[0])
	}
	if res.IsError {
		t.Fatalf("call %s failed: %s", tool, text.Text)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text.Text), &decoded); err != nil {
		t.Fatalf("call %s output is not JSON: %v (%q)", tool, err, text.Text)
	}
	return text.Text, decoded
}

// TestE2EWorkItemContentEchoAgainstARealServer walks a real create and a real
// update end to end and reports the byte sizes.
func TestE2EWorkItemContentEchoAgainstARealServer(t *testing.T) {
	s := newE2EStack(t)

	// ── create ──────────────────────────────────────────────────────────────
	createText, created := s.call(t, "pf_create_work_item", map[string]any{
		"project": s.project,
		"goal":    "measure the create and update content echo end to end",
		"content": e2eContent,
	})
	if _, present := created["content"]; present {
		t.Errorf("create still echoes the content back")
	}
	if created["content_len"] != float64(len(e2eContent)) {
		t.Errorf("create content_len = %v, want %d", created["content_len"], len(e2eContent))
	}
	for _, k := range []string{"id", "slug", "seq"} {
		if created[k] == nil {
			t.Errorf("create response lost %s — the call chain keys off it", k)
		}
	}
	wiID, _ := created["id"].(string)
	if wiID == "" {
		t.Fatalf("create returned no id: %s", createText)
	}

	// ── hop 3: the server really does hold, and return, that content ────────
	//
	// Without this the assertions above would be satisfied just as well by a
	// server that had stopped storing content at all.
	fetched, err := s.client.GetWorkItem(context.Background(), wiID)
	if err != nil {
		t.Fatalf("GetWorkItem: %v", err)
	}
	stored, _ := fetched["content"].(string)
	if stored != e2eContent {
		t.Fatalf("the server did not store the content verbatim (%d B stored vs %d B sent); "+
			"the echo suppression is gated on that equality and has just stopped firing",
			len(stored), len(e2eContent))
	}

	// ── update WITHOUT content: the full record, i.e. today's "before" ───────
	beforeText, before := s.call(t, "pf_update_work_item", map[string]any{
		"work_item_id": wiID, "priority": "high",
	})
	if before["content"] != e2eContent {
		t.Errorf("an update that sent no content must still return the body in full")
	}

	// ── update WITH content: the "after" ────────────────────────────────────
	afterText, after := s.call(t, "pf_update_work_item", map[string]any{
		"work_item_id": wiID, "content": e2eContent,
	})
	if _, present := after["content"]; present {
		t.Errorf("update still echoes the content back")
	}
	if after["content_len"] != float64(len(e2eContent)) {
		t.Errorf("update content_len = %v, want %d", after["content_len"], len(e2eContent))
	}
	if strings.Contains(afterText, "A paragraph of the sort") {
		t.Errorf("the body is still in the update response text")
	}

	// ── brief: drops a body the caller did not send ─────────────────────────
	briefText, brief := s.call(t, "pf_update_work_item", map[string]any{
		"work_item_id": wiID, "priority": "normal", "brief": true,
	})
	if _, present := brief["content"]; present {
		t.Errorf("brief=true did not drop the content")
	}
	if brief["content_len"] != float64(len(e2eContent)) {
		t.Errorf("brief content_len = %v, want %d", brief["content_len"], len(e2eContent))
	}
	// pf-plan/SKILL.md re-reads this off the update response to feed the next
	// declared_resources compare-and-set; it is the one field any skill is known
	// to take from an update reply.
	if brief["resources_version"] == nil {
		t.Errorf("resources_version must survive brief — pf-plan reads it back from here")
	}

	// ── brief on a wi that HAS no body ──────────────────────────────────────
	//
	// The published description promises `content: null` and no content_len
	// here, which rests on the real server serialising a NULL content column as
	// a JSON null rather than omitting the key (domain.WorkItem.Content is
	// *string with no omitempty). If that ever changed, brief would answer a
	// bodyless work item with nothing at all about its content and absence would
	// silently become the signal.
	_, bodyless := s.call(t, "pf_create_work_item", map[string]any{
		"project": s.project,
		"goal":    "a work item filed with no body at all, to probe the null branch",
	})
	bodylessID, _ := bodyless["id"].(string)
	if bodylessID == "" {
		t.Fatalf("create returned no id for the bodyless work item")
	}
	_, nullBrief := s.call(t, "pf_update_work_item", map[string]any{
		"work_item_id": bodylessID, "priority": "low", "brief": true,
	})
	if v, present := nullBrief["content"]; !present || v != nil {
		t.Errorf("brief on a bodyless wi: content = %#v (present=%v), want a surviving null — "+
			"the published description promises exactly this", v, present)
	}
	if _, present := nullBrief["content_len"]; present {
		t.Errorf("brief on a bodyless wi reported content_len = %v; there is no body to measure",
			nullBrief["content_len"])
	}

	t.Logf("MEASURED against a real Postgres + router + client + MCP handler (content %d B):", len(e2eContent))
	t.Logf("  pf_create_work_item  after=%d B   (with content it would be ~%d B)", len(createText), len(createText)+len(e2eContent))
	t.Logf("  pf_update_work_item  before=%d B  after=%d B  saved=%d B (%.1f%%)",
		len(beforeText), len(afterText), len(beforeText)-len(afterText),
		100*float64(len(beforeText)-len(afterText))/float64(len(beforeText)))
	t.Logf("  pf_update_work_item  brief=%d B (%.1f%% of before)",
		len(briefText), 100*float64(len(briefText))/float64(len(beforeText)))

	if len(afterText) >= len(beforeText) {
		t.Errorf("the suppressed update response (%d B) is not smaller than the unsuppressed one (%d B)",
			len(afterText), len(beforeText))
	}
}

// TestE2ERealServerStoresContentByteIdentically is the standing alarm on the
// premise the whole design rests on.
//
// The response is not a buffer echoed back — UpdateWorkItem commits, makes a
// synchronous embedding network call, then re-reads the row — so "what you sent
// is what comes back" is a fact about the server, not a guarantee of the
// transport. If it ever stops being true (a TrimSpace, a CRLF normalisation, a
// unicode NFC pass) the equality gate silently stops firing and the saving
// evaporates with every other test still green. This one goes red instead.
//
// Deliberately probes the shapes such a change would touch first: leading and
// trailing whitespace, CRLF, and a trailing newline.
func TestE2ERealServerStoresContentByteIdentically(t *testing.T) {
	s := newE2EStack(t)
	ctx := context.Background()

	probe := "  leading and trailing space  \r\nCRLF line\nplain line\n"
	_, created := s.call(t, "pf_create_work_item", map[string]any{
		"project": s.project,
		"goal":    "probe whether the server rewrites the content it is given",
		"content": probe,
	})
	wiID, _ := created["id"].(string)
	if wiID == "" {
		t.Fatalf("create returned no id")
	}
	if created["content_len"] != float64(len(probe)) {
		t.Errorf("content_len = %v, want %d — the suppression fired on a value it should not have",
			created["content_len"], len(probe))
	}

	fetched, err := s.client.GetWorkItem(ctx, wiID)
	if err != nil {
		t.Fatalf("GetWorkItem: %v", err)
	}
	if stored, _ := fetched["content"].(string); stored != probe {
		t.Errorf("the server rewrote the content it was given:\n  sent   %q\n  stored %q\n"+
			"aihub#281's echo suppression only drops bytes it can prove are identical, so this "+
			"does not corrupt anything — it silently costs the whole saving. Fix the normalisation "+
			"or move the gate.", probe, stored)
	}
}
