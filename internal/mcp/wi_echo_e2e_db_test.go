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
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/auth"
	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/GMISWE/ieops-aihub/internal/mcp"
	"github.com/GMISWE/ieops-aihub/internal/server"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// e2eContent is a body the size real callers send (the measured sample averaged
// ~4.7 KB, maximum 17,870 characters). A short string would let a size
// regression hide inside the fixed part of the record.
var e2eContent = "## Spec\n\n" + strings.Repeat(
	"A paragraph of the sort a spec or plan artifact carries, long enough to matter. ", 50)

// e2eSerializationRetries bounds the retries below on the typed retryable
// serialization 409 — same value and same reason as internal/domain's
// serializationRetryAttempts (aihub#492): the contender is another test
// binary's short-lived transaction, so a handful of attempts is plenty, and
// the bound keeps a PERMANENT 40001 visible as the refusal it is instead of
// looping on it.
const e2eSerializationRetries = 8

// e2eRetryableSerializationRefusal reports whether one call's result is the
// typed retryable serialization refusal, in EITHER of the two shapes the tools
// answer it with — both measured under cross-binary DB load (aihub#593,
// 2026-09-11):
//
//   - an MCP error result carrying the 409 (most tools; pf_claim_work_item,
//     pf_complete_attempt and pf_wrap were each caught answering this way);
//   - a SUCCESSFUL JSON result from a chain tool that stopped fail-closed with
//     the 409 embedded — pf_ship was caught answering `ok:false, stage:commit,
//     lock_gate:could_not_run` with "failed to list held locks: ... SQLSTATE
//     40001" inside, and its own advice field says "retry pf_ship with the
//     same arguments" because the files are still staged.
//
// Both mean the same thing: the transaction rolled back, nothing was
// committed, and the identical request is safe to resend. The `ok:false`
// requirement on the non-error shape is what keeps a tool that merely ECHOES
// this string in caller-supplied content (a memory body, a goal) from being
// re-driven for no reason.
func e2eRetryableSerializationRefusal(text string, isErr bool) bool {
	if !strings.Contains(text, string(domain.ErrConflictSerializationFailure)) {
		return false
	}
	if isErr {
		return true
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		return false
	}
	okVal, present := decoded["ok"].(bool)
	return present && !okVal
}

// e2eStack is one wired-up copy of the real thing: migrated DB, real router,
// real client, real MCP server.
type e2eStack struct {
	pool    *pgxpool.Pool
	session *sdkmcp.ClientSession
	client  *client.Client
	project string
	// baseURL is the live router's own address, for the one kind of request
	// pkg/client cannot make: an UNAUTHENTICATED one. GET /share/:id is
	// anonymous by design and the aihub#586 arm has to drive it that way —
	// through the client it would carry the admin key and prove nothing about
	// anonymous reachability.
	baseURL string
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
	//
	// resource_locks goes before run_attempts, explicitly, for the reason
	// internal/domain's resetTestProject states: its FK to run_attempts is ON
	// DELETE RESTRICT, so a leftover lock BLOCKS the cleanup of the attempt that
	// owns it. This project is SHARED by every test on this stack, and the
	// live-keys walk really does take file_scope locks in it (pf_commit's lock
	// gate). Measured 2026-09-11 (aihub#593): one walk run that died mid-way
	// left its running attempt holding two locks, and every later run of every
	// test on this stack then failed HERE with SQLSTATE 23001 — 15+ failures per
	// full-suite run, durable until someone deleted the rows by hand.
	for _, q := range []string{
		`DELETE FROM wi_step_completions WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM agent_events WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM wi_step_state WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`UPDATE work_items SET current_attempt_id=NULL WHERE project=$1`,
		`DELETE FROM resource_locks WHERE owner_attempt_id IN (SELECT id FROM run_attempts WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1))`,
		`DELETE FROM run_attempts WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM memories WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM work_items WHERE project=$1`,
	} {
		// Two retryable interference classes from OTHER test binaries sharing
		// this database, both measured during aihub#593 (2026-09-11), each
		// killing every test on this stack for that run:
		//
		//   - class 40 (40001 above READ COMMITTED, 40P01 — a deadlock victim —
		//     at any level): Postgres rolled the statement's transaction back
		//     precisely so it can be re-run. 1 of 20 full-suite-with-database
		//     runs lost the work_items delete to a 40P01.
		//   - 55000 "cannot delete from view resource_locks" / 42P01 "relation
		//     resource_locks does not exist": internal/domain's
		//     TestClaimProbeFailureReachesTheCallerAsARetryable409 swaps that
		//     TABLE for a poison view for the duration of its run (≤0.23s
		//     measured) and restores it; 42P01 is the instant between its
		//     RENAME and its CREATE VIEW. 2 of 10 full-suite runs landed the
		//     resource_locks delete inside that window.
		//
		// The backoff is sized to OUTLAST the poison window, not just a commit:
		// 50ms × attempt over 8 attempts sleeps up to 1.4s against a ≤0.23s
		// window. A permanent error of any of these codes still surfaces
		// through the Fatalf below when the bound runs out.
		var err error
		for attempt := 1; attempt <= e2eSerializationRetries; attempt++ {
			_, err = pool.Exec(ctx, q, project)
			var pgErr *pgconn.PgError
			if err == nil || !errors.As(err, &pgErr) ||
				(pgErr.Code != "40001" && pgErr.Code != "40P01" &&
					pgErr.Code != "55000" && pgErr.Code != "42P01") {
				break
			}
			t.Logf("clean fixture (%s): attempt %d lost a cross-binary race (%s), retrying", q, attempt, pgErr.Code)
			time.Sleep(time.Duration(attempt) * 50 * time.Millisecond)
		}
		if err != nil {
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

	return &e2eStack{pool: pool, session: session, client: aihubClient, project: project, baseURL: ts.URL}
}

// call invokes a tool and returns the response text and its decoded form.
func (s *e2eStack) call(t *testing.T, tool string, args map[string]any) (string, map[string]any) {
	t.Helper()
	text, isErr := s.callAllowingError(t, tool, args)
	// aihub#593 (2026-09-11): the typed retryable serialization 409 is retried
	// before the Fatalf below, for the reason e2eRetryableSerializationRefusal
	// states — the server answers it BECAUSE the identical request is safe to
	// resend, and every production caller is told to. Measured under
	// cross-binary DB load: 1 of 20 full-suite-with-database runs lost
	// pf_claim_work_item in TestE2EClaimWithANewKeyMintsAFreshSecret to it and
	// this function reported the retryable refusal as a fatal harness failure.
	// A PERMANENT refusal still surfaces: the bound runs out and the Fatalf
	// below carries the same message it always did.
	for attempt := 1; e2eRetryableSerializationRefusal(text, isErr) &&
		attempt < e2eSerializationRetries; attempt++ {
		t.Logf("%s: attempt %d lost an SSI race, retrying: %s", tool, attempt, text)
		time.Sleep(time.Duration(attempt) * 2 * time.Millisecond)
		text, isErr = s.callAllowingError(t, tool, args)
	}
	if isErr {
		t.Fatalf("call %s failed: %s", tool, text)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("call %s output is not JSON: %v (%q)", tool, err, text)
	}
	return text, decoded
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
