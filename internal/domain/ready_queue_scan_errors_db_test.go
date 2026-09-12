package domain

// Behavioural arm for aihub#608's GetReadyQueue repair: a row-level Scan
// failure fails the whole call, and the error names the failing segment's
// OWN scan.
//
// Before aihub#608 every segment's drain answered a Scan error with `continue`
// — the shape that spells "drop this row, publish the rest as the complete
// queue", and that did exactly that to aihub#206's stalled[] rows back when no
// rows.Err() arm existed. What it does on today's pgx was MEASURED during
// aihub#608 with this test against the pre-change tree (the cp-backed mutant
// run recorded in that wi): a failed Scan poisons the rows (rows.fatal, pgx
// v5.9.2), so the drain stops and the aihub#382/#386 rows.Err() arm fails the
// call as "failed to read running items rows: …" — the right verdict wearing
// the READ arm's message, delivered only because of an undocumented driver
// side effect. This test pins both properties the fix actually claims: the
// call errors (a regression that removed BOTH arms would answer 200 with the
// row silently gone — the aihub#206 lie), and the error names the Scan site,
// so the contract no longer leans on pgx's poisoning behaviour.
//
// The malformed row is real SQL, not a mock: run_attempts.last_active_at is
// TIMESTAMPTZ, `'infinity'` is a value Postgres happily stores, and pgx v5
// cannot represent it in a plain time.Time, so Scan fails on that row and only
// that row. Nothing on the server writes 'infinity' — clock_timestamp() feeds
// every writer — which is the point: this is column-drift/malformation
// territory.
//
// DB-gated; rides the `-run 'TestGetReadyQueue_'` invocation of the
// "aihub#280 list work_items param contract DB tests" step in ci.yml (prefix
// match), and is listed in internal/citest/dbtestcov/gated_tests.txt.

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
)

// seedRunningWI creates a project with one running work item owned by one
// running attempt, and returns the project and attempt id. Idempotent, same
// cleanup discipline as seedReadyQueueFixture.
func seedRunningWI(t *testing.T, pool *pgxpool.Pool) (project, attemptID string) {
	t.Helper()
	ctx := context.Background()
	project = "p_" + testname.Sanitize(t.Name())
	uid := "u_readyq_test"

	for _, q := range []string{
		`UPDATE work_items SET current_attempt_id=NULL WHERE project=$1`,
		`DELETE FROM run_attempts WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM agent_events WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM work_items WHERE project=$1`,
	} {
		if _, err := pool.Exec(ctx, q, project); err != nil {
			t.Fatalf("cleanup %q: %v", q, err)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO users(id,email,display_name,role) VALUES($1,$1||'@test.local',$1,'writer')
		 ON CONFLICT (id) DO NOTHING`, uid); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO projects(name,owner_user_id) VALUES($1,$2) ON CONFLICT (name) DO NOTHING`,
		project, uid); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	wiID := NewID("wi")
	if _, err := pool.Exec(ctx, `
		INSERT INTO work_items (id, seq, project, scenario, goal, source, wi_type, priority,
			requires_human_session, labels, status, declared_resources,
			reporter_user_id, reporter_display, attrs)
		VALUES ($1,9608,$2,'coding','aihub#608 running fixture','human','chore','normal',
			false,'{}','running','[]',$3,$3,'{}')`,
		wiID, project, uid); err != nil {
		t.Fatalf("seed wi: %v", err)
	}
	attemptID = NewID("ra")
	if _, err := pool.Exec(ctx, `
		INSERT INTO run_attempts (id, work_item_id, status, claim_epoch, idempotency_key,
			actor_user_id, actor_display, machine_id, session_secret_hash)
		VALUES ($1,$2,'running',1,'idem-608','`+uid+`','`+uid+`','m-608','unused')`,
		attemptID, wiID); err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE work_items SET current_attempt_id=$1 WHERE id=$2`, attemptID, wiID); err != nil {
		t.Fatalf("wire current_attempt_id: %v", err)
	}
	return project, attemptID
}

func TestGetReadyQueue_RowScanFailureFailsTheCall(t *testing.T) {
	pool := readyQueueTestPool(t)
	ctx := context.Background()
	project, attemptID := seedRunningWI(t, pool)

	// ── CONTROL: with well-formed data the wi is IN running[], nil error. ────
	// This is what makes the malformed arm meaningful: it proves the fixture
	// row is exactly the row that later fails to scan, not some row the
	// predicate never selected.
	rq, aerr := GetReadyQueue(ctx, pool, project, 20)
	if aerr != nil {
		t.Fatalf("control: GetReadyQueue must succeed on well-formed data, got %s: %s", aerr.Code, aerr.Message)
	}
	if len(rq.Running) != 1 {
		t.Fatalf("control: running[] holds %d items, want the 1 seeded running wi", len(rq.Running))
	}

	// ── THE ARM: one malformed row must fail the call, naming the segment. ──
	if _, err := pool.Exec(ctx,
		`UPDATE run_attempts SET last_active_at='infinity' WHERE id=$1`, attemptID); err != nil {
		t.Fatalf("write malformed last_active_at: %v", err)
	}
	rq, aerr = GetReadyQueue(ctx, pool, project, 20)
	if aerr == nil {
		got := len(rq.Running)
		t.Fatalf("GetReadyQueue answered nil error with running[] holding %d item(s) — the malformed row "+
			"was silently dropped and the partial queue published as the complete one: BOTH the Scan "+
			"guard and the rows.Err() backstop are gone (the aihub#206 lie, resurrected)", got)
	}
	// On the ERROR TEXT, not merely non-nil: an unmigrated fixture also yields
	// "an error", and an arm accepting any error is green for the wrong reason
	// (the aihub#382 test states the same rule for its arms).
	if !strings.Contains(aerr.Message, "failed to scan running item row") {
		t.Errorf("error %q does not name the running-segment scan — the failure was surfaced by something "+
			"else, which means this segment's own Scan guard is gone", aerr.Message)
	}
}
