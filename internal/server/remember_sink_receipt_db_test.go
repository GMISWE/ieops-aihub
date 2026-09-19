package server

// remember_sink_receipt_db_test.go — the real-DB acceptance for the durable
// controller-sink replay receipt (aihub#725 review_fix B2).
//
// The controller sink saves a worker's completed output as a methodology
// artifact and then records the result against the returned id. If the save's
// response is lost, the retry cannot tell a refused save from a committed
// one, and the in-process once-guard fenced only one process. The receipt
// makes the idempotency durable with NO schema change: the save stamps
// attrs.controller_sink_receipt = {invocation_id, step_attempt_id, attempt_id}
// — this test posts that shape directly, which is the same wire the
// controller's SaveArtifact produces (pinned in pkg/client's wire test) — and
// domain.Remember resolves it BEFORE dedup, embedding and INSERT:
//
//	same receipt + same payload  -> the ALREADY-STORED row, is_new=false
//	same receipt + other payload -> 409 CONFLICT_DUPLICATE, nothing stored
//	same receipt, 1.50 vs 1.5    -> a DIFFERENT payload in the literal number
//	                               universe the digest pipeline hashes
//	malformed receipt            -> 400, nothing stored
//	no receipt                   -> the ordinary create path, unchanged
//
// Real router, real binder, real Postgres: only the real jsonb column and the
// real containment probe can answer whether the receipt survives storage and
// is found again — and only jsonb's number round-trip can answer whether the
// stored payload still hashes to what the controller recorded.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	GOWORK=off go test ./internal/server/ -run TestRememberControllerSinkReceipt -count=1 -v

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/auth"
	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
	"github.com/GMISWE/ieops-aihub/internal/domain"
)

func TestRememberControllerSinkReceipt(t *testing.T) {
	pool := serverTestPool(t)
	ctx := context.Background()

	base := testname.Sanitize(t.Name())
	project := "p_" + base
	owner := "u_" + base
	key := "pfk_" + owner

	keys, err := json.Marshal([]map[string]any{{"id": "k_" + base, "key_hash": auth.HashKey(key)}})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO users(id,email,display_name,user_type,role,api_keys)
		VALUES($1,$1||'@test.local',$1,'human','writer',$2)
		ON CONFLICT (id) DO UPDATE SET api_keys=EXCLUDED.api_keys`,
		owner, keys)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO projects(name,owner_user_id,members) VALUES($1,$2,$3)
		ON CONFLICT (name) DO UPDATE SET owner_user_id=EXCLUDED.owner_user_id, members=EXCLUDED.members`,
		project, owner, []byte(fmt.Sprintf(`[{"user_id":%q,"role":"writer"}]`, owner)))
	require.NoError(t, err)

	// Same derivation as every other DB test here: names come from t.Name(),
	// so previous runs' rows must go before the seq numbers matter.
	_, err = pool.Exec(ctx, `UPDATE memories SET latest_id=NULL, supersedes_id=NULL WHERE project=$1`, project)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM memories WHERE project=$1`, project)
	require.NoError(t, err)
	resetProjectWorkItems(t, pool, project)

	wi, aerr := domain.CreateWorkItem(ctx, pool, &domain.CreateWorkItemRequest{
		Project: project,
		Goal:    "durable controller-sink receipt replay coverage " + base,
		Source:  "human",
	}, owner, owner, nil, "")
	require.Nil(t, aerr, "seeding work item failed: %+v", aerr)

	ts := httptest.NewServer(NewRouter(pool, []byte("sink-receipt-test-cookie-secret")))
	t.Cleanup(ts.Close)

	post := func(body map[string]any) (int, map[string]any) {
		raw, mErr := json.Marshal(body)
		require.NoError(t, mErr)
		r, rErr := http.NewRequest(http.MethodPost, ts.URL+"/v1/memories", strings.NewReader(string(raw)))
		require.NoError(t, rErr)
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("Content-Type", "application/json")
		resp, doErr := http.DefaultClient.Do(r)
		require.NoError(t, doErr)
		defer resp.Body.Close() //nolint:errcheck
		out, readErr := io.ReadAll(resp.Body)
		require.NoError(t, readErr)
		var parsed map[string]any
		require.NoError(t, json.Unmarshal(out, &parsed), "body: %s", out)
		return resp.StatusCode, parsed
	}

	receipt := map[string]any{
		"invocation_id":   "inv_" + base,
		"step_attempt_id": "sa_" + base,
		"attempt_id":      "ra_" + base,
	}
	sinkBody := func(receiptOverride any, payload json.RawMessage) map[string]any {
		b := map[string]any{
			"type":               "experience.note",
			"content":            "controller-sink replay fixture " + base,
			"visibility":         "project",
			"dedup_mode":         "off",
			"work_item_id":       wi.ID,
			"structured_payload": payload,
		}
		if receiptOverride != nil {
			b["attrs"] = map[string]any{"controller_sink_receipt": receiptOverride}
		}
		return b
	}

	// 1. First landing: an ordinary create, receipt stored. The payload keeps
	// a literal (1.50) whose byte form only a UseNumber pipeline preserves.
	code, first := post(sinkBody(receipt, json.RawMessage(`{"ratio":1.50,"summary":"did work"}`)))
	require.Equal(t, http.StatusCreated, code, "body: %+v", first)
	firstID, _ := first["id"].(string)
	require.NotEmpty(t, firstID)
	require.Equal(t, true, first["is_new"])

	// The stored copy keeps the literal: jsonb round-trips decimal scale, and
	// the G35 merge is UseNumber, so what came in is what is stored.
	var storedAttrs []byte
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT attrs FROM memories WHERE id=$1`, firstID).Scan(&storedAttrs))
	// Postgres jsonb::text renders `": "` (space after the colon), so the
	// literal check targets the JSON VALUE spelling — 1.50, not 1.5 — via a
	// jsonb-path extraction rather than a byte Contains on the whole attrs.
	var ratio string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT (attrs->'structured_payload'->>'ratio')::text FROM memories WHERE id=$1`,
		firstID).Scan(&ratio))
	require.Equal(t, "1.50", ratio,
		"the stored structured_payload lost the 1.50 literal (got %q); attrs: %s", ratio, storedAttrs)

	// 2. Same receipt, same payload in a DIFFERENT byte spelling (key order
	// swapped): returns the ALREADY-STORED row, is_new=false — no duplicate.
	code, replay := post(sinkBody(receipt, json.RawMessage(`{"summary":"did work","ratio":1.50}`)))
	require.Equal(t, http.StatusCreated, code, "body: %+v", replay)
	require.Equal(t, firstID, replay["id"], "the replay must return the stored row, not a new one")
	require.Equal(t, false, replay["is_new"])

	// 3. Same receipt, DIFFERENT payload: a collision — one invocation cannot
	// store two different artifacts. 409, and nothing stored.
	code, collision := post(sinkBody(receipt, json.RawMessage(`{"summary":"tampered"}`)))
	require.Equal(t, http.StatusConflict, code, "body: %+v", collision)
	require.Equal(t, string(domain.ErrConflictDuplicate), collision["code"])

	// 4. Same receipt, the FLOAT spelling of the same number (1.5): a
	// different payload in the literal universe the digest pipeline hashes —
	// the receipt is a replay guard, not a value-normalizing one.
	code, floatSpelling := post(sinkBody(receipt, json.RawMessage(`{"ratio":1.5,"summary":"did work"}`)))
	require.Equal(t, http.StatusConflict, code, "body: %+v", floatSpelling)

	// 5. Malformed receipt: refused loudly, nothing stored.
	code, malformed := post(sinkBody(map[string]any{"invocation_id": "x"}, json.RawMessage(`{"summary":"x"}`)))
	require.Equal(t, http.StatusBadRequest, code, "body: %+v", malformed)

	// 6. No receipt: the ordinary create path is unchanged (a second row).
	code, plain := post(sinkBody(nil, json.RawMessage(`{"summary":"plain"}`)))
	require.Equal(t, http.StatusCreated, code, "body: %+v", plain)
	require.Equal(t, true, plain["is_new"])
	require.NotEqual(t, firstID, plain["id"])

	// Count: exactly two rows — the receipt pair stored one, the plain save
	// one. The replay and every refusal stored nothing.
	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM memories WHERE project=$1`, project).Scan(&n))
	require.Equal(t, 2, n, "the receipt pair must have stored exactly one row, plus one plain save")
}
