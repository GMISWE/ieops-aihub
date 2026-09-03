package domain

// aihub#175 findings 2 and 3, at the layer that actually writes the rows.
//
//	AIHUB_TEST_DB='postgres://postgres:…@localhost:5432/aihub_test?sslmode=disable' \
//	  go test ./internal/domain/ -run 'TestRedact_|TestActivate_' -v -count=1
//
// Why these need a database rather than a fake:
//
//   - finding 2's whole fix IS an UPDATE's column list plus an INSERT. Whether
//     redacted_at/redaction_reason are written, and whether the event survives
//     agent_events' chk_evt_work_item_id whitelist (memory_redacted is on it,
//     but a redaction has no work_item_id, so a wrong event_type would be
//     rejected at runtime, not at compile time), is a property of that SQL and
//     of nothing else.
//   - finding 3's guard is an EXISTS subquery. A unit test over
//     activationTargetStatus (activate_status_test.go) covers the decision, but
//     it cannot show that the boolean fed to it is the right boolean, which is
//     the hop that was missing entirely.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// seedAuditMemory inserts one memory row with the given id and status.
func seedAuditMemory(t *testing.T, pool *pgxpool.Pool, proj, author, id, status, supersedesID string) {
	t.Helper()
	ctx := context.Background()
	mustExec(t, pool, `DELETE FROM memories WHERE id='`+id+`'`)
	var sup any
	if supersedesID != "" {
		sup = supersedesID
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO memories(id, project, author_user_id, type, content, status, supersedes_id)
		 VALUES($1, $2, $3, 'experience.debug', 'body', $4, $5)`,
		id, proj, author, status, sup)
	require.NoError(t, err)
}

// TestRedact_WritesAuditColumnsAndEmitsEvent is the aihub#175 finding-2
// regression gate. Before the fix, redacting a memory left redacted_at NULL,
// redaction_reason NULL, and emitted zero agent_events — "who deleted this,
// when, and why" was unanswerable from the database.
func TestRedact_WritesAuditColumnsAndEmitsEvent(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	seedAuditMemory(t, pool, proj, uid, "mem_audit_redact", "active", "")
	_, err := pool.Exec(ctx,
		`DELETE FROM agent_events WHERE project=$1 AND event_type='memory_redacted'`, proj)
	require.NoError(t, err)

	const reason = "duplicate of the newer note"
	require.NoError(t, Redact(ctx, pool, "mem_audit_redact", uid, "Auditor", "admin", reason))

	var status string
	var redactedAt, storedReason *string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status, redacted_at::text, redaction_reason FROM memories WHERE id='mem_audit_redact'`).
		Scan(&status, &redactedAt, &storedReason))
	require.Equal(t, "redacted", status)
	require.NotNil(t, redactedAt, "redacted_at must be stamped, not left NULL as before aihub#175")
	require.NotNil(t, storedReason, "redaction_reason must be stored")
	require.Equal(t, reason, *storedReason)

	var actorID, actorDisplay, payloadReason, payloadMem string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT actor_user_id, actor_display, payload->>'reason', payload->>'memory_id'
		   FROM agent_events WHERE project=$1 AND event_type='memory_redacted'`, proj).
		Scan(&actorID, &actorDisplay, &payloadReason, &payloadMem))
	require.Equal(t, uid, actorID, "memories has no redacted_by column; the event is the only actor record")
	require.Equal(t, "Auditor", actorDisplay)
	require.Equal(t, reason, payloadReason)
	require.Equal(t, "mem_audit_redact", payloadMem)
}

// TestRedact_OmittedReasonStoresNullNotEmptyString pins the NULLIF: "no reason
// given" and "reason given as the empty string" must stay distinguishable in
// an audit record, otherwise the column cannot be used to find undocumented
// deletions.
func TestRedact_OmittedReasonStoresNullNotEmptyString(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	seedAuditMemory(t, pool, proj, uid, "mem_audit_noreason", "active", "")
	require.NoError(t, Redact(ctx, pool, "mem_audit_noreason", uid, "Auditor", "admin", ""))

	var storedReason, redactedAt *string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT redaction_reason, redacted_at::text FROM memories WHERE id='mem_audit_noreason'`).
		Scan(&storedReason, &redactedAt))
	require.Nil(t, storedReason, "an omitted reason must be NULL, not ''")
	require.NotNil(t, redactedAt, "redacted_at is stamped even when no reason was given")
}

// TestActivate_DoesNotReviveSupersededVersion is the aihub#175 finding-3
// regression gate, built from the state the bug needs: a two-version lineage
// whose old version was archived by the supersede. Before the fix, activating
// the old version flipped it back to 'active' beside the head, and
// MemoryVersionChain then reported TWO entries with is_current=true.
//
// experience.debug on purpose: aihub#214's existing guard only covers
// methodology.*, so a methodology fixture would pass on the pre-fix build and
// prove nothing.
func TestActivate_DoesNotReviveSupersededVersion(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	seedAuditMemory(t, pool, proj, uid, "mem_sup_v1", "archived", "")
	seedAuditMemory(t, pool, proj, uid, "mem_sup_v2", "active", "mem_sup_v1")
	mustExec(t, pool, `UPDATE memories SET latest_id='mem_sup_v2' WHERE id IN ('mem_sup_v1','mem_sup_v2')`)

	resp, err := Activate(ctx, pool, "mem_sup_v1", uid, "tester")
	require.NoError(t, err, "activation is still recorded — only the status transition is refused")
	require.Equal(t, 1, resp.ActivationCount, "the recall signal must still count")

	var v1Status, v2Status string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM memories WHERE id='mem_sup_v1'`).Scan(&v1Status))
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM memories WHERE id='mem_sup_v2'`).Scan(&v2Status))
	require.Equal(t, "archived", v1Status, "the superseded version must NOT be revived")
	require.Equal(t, "active", v2Status, "the head is untouched")

	var nActive int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM memories WHERE project=$1 AND id IN ('mem_sup_v1','mem_sup_v2') AND status='active'`,
		proj).Scan(&nActive))
	require.Equal(t, 1, nActive, "a lineage must have exactly one active head")

	chain, cerr := MemoryVersionChain(ctx, pool, "mem_sup_v1")
	require.NoError(t, cerr)
	require.Len(t, chain, 2)
	nCurrent := 0
	for _, e := range chain {
		if e.IsCurrent {
			nCurrent++
		}
	}
	require.Equal(t, 1, nCurrent,
		"orderVersionChain marks EVERY active entry current, so two active heads make is_current wrong")
	require.True(t, chain[1].IsCurrent, "the newest version is the current one")
}

// TestActivate_StillRevivesADecayedVersionWithNoSuccessor is the negative
// control for the guard above: without it, "archived stays archived" would be
// trivially satisfiable by never reviving anything, and the forgetting-curve
// behaviour that activation exists for would be silently dead.
func TestActivate_StillRevivesADecayedVersionWithNoSuccessor(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	seedAuditMemory(t, pool, proj, uid, "mem_decayed", "archived", "")

	_, err := Activate(ctx, pool, "mem_decayed", uid, "tester")
	require.NoError(t, err)

	var status string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM memories WHERE id='mem_decayed'`).Scan(&status))
	require.Equal(t, "active", status, "a decayed memory with no successor must still revive")
}

// TestActivate_RedactedSuccessorDoesNotCountAsSupersession pins the
// `s.status <> 'redacted'` half of the EXISTS: if the only successor was itself
// redacted, the lineage has no live head, so the old version is a genuine
// revival candidate again. Without this clause a redacted successor would
// strand its predecessor archived forever.
func TestActivate_RedactedSuccessorDoesNotCountAsSupersession(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	seedAuditMemory(t, pool, proj, uid, "mem_rsup_v1", "archived", "")
	seedAuditMemory(t, pool, proj, uid, "mem_rsup_v2", "redacted", "mem_rsup_v1")

	_, err := Activate(ctx, pool, "mem_rsup_v1", uid, "tester")
	require.NoError(t, err)

	var status string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM memories WHERE id='mem_rsup_v1'`).Scan(&status))
	require.Equal(t, "active", status, "a redacted successor leaves no live head, so v1 may revive")
}
