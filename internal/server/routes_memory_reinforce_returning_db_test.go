package server

// aihub#475, the behavioural half — what the caller is TOLD versus what the
// column HOLDS.
//
// routes_memory_reinforce_honesty_test.go asserts the shape of the code and can
// run anywhere. It cannot run the defect: the bug was a divergence between a
// response body and a stored row, and observing that needs a row. So this file
// drives the real handler against a real database and compares every published
// base_strength against memories.base_strength read straight back out.
//
//	AIHUB_TEST_DB='postgres://postgres:…@localhost:5432/aihub_test?sslmode=disable' \
//	  go test ./internal/server/ -run TestReinforceMemory_ -v -count=1
//
// 🟢 REVISED BY aihub#459 (2026-09-09), which is the revision the previous
// version of this comment asked for by name. It said: "The criterion is
// response == column, NOT response == 3 … aihub#459 may yet decide that a
// fractional delta is refused, rounded, or made storable by widening the column.
// Under all three of those the response must still equal the row; only the
// number changes." The owner ruled REFUSED, so the sequence this file used to
// reproduce verbatim — a row at 3, reinforced twice with `strength_delta: 0.5`
// — is now a 400 before the first query, and the arm that pinned `3.0` is gone
// with the mechanism it described. The criterion itself is unchanged and still
// asserted, on integral deltas.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

// seedReinforceableMemory inserts one active memory at the given base_strength
// and clears any memory_reinforced events the project carries, so the event
// assertions measure this run only.
func seedReinforceableMemory(t *testing.T, pool *pgxpool.Pool, proj, author, id string, baseStrength int) string {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `DELETE FROM memories WHERE id = $1`, id)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`INSERT INTO memories(id, project, author_user_id, type, content, base_strength)
		 VALUES($1, $2, $3, 'experience.debug', 'reinforce me', $4)`,
		id, proj, author, baseStrength)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`DELETE FROM agent_events WHERE project = $1 AND event_type = 'memory_reinforced'`, proj)
	require.NoError(t, err)
	return id
}

// callReinforceRaw drives the real handler and returns whatever it answered. No
// attempt credentials: the memory is experience.*, so
// enforceMethodologyAttemptGate's verify-if-supplied branch asks for none.
//
// Split from callReinforce by aihub#459: a rejection is now one of the outcomes
// this file has to observe, and a helper that requires 200 cannot see one.
func callReinforceRaw(t *testing.T, pool *pgxpool.Pool, memID, body string, uc *UserContext) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPatch, "/v1/memories/"+memID+"/reinforce", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(memID)
	setUser(c, uc)
	require.NoError(t, handleReinforceMemory(pool)(c))
	return rec
}

// callReinforce is callReinforceRaw for the calls that must succeed.
func callReinforce(t *testing.T, pool *pgxpool.Pool, memID, body string, uc *UserContext) *httptest.ResponseRecorder {
	t.Helper()
	rec := callReinforceRaw(t, pool, memID, body, uc)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	return rec
}

// reinforceRowState reads back the two columns the handler mutates and publishes.
func reinforceRowState(t *testing.T, pool *pgxpool.Pool, id string) (float64, int) {
	t.Helper()
	var bs float64
	var count int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT base_strength, activation_count FROM memories WHERE id = $1`, id).Scan(&bs, &count))
	return bs, count
}

// reinforceEventCount counts the memory_reinforced events the project holds.
// seedReinforceableMemory clears them, so this measures one run.
func reinforceEventCount(t *testing.T, pool *pgxpool.Pool, proj string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_events WHERE project=$1 AND event_type='memory_reinforced'`,
		proj).Scan(&n))
	return n
}

// storedBaseStrength reads the column back. Typed float64 to match what the
// handler publishes, so a mismatch in the assertion is a mismatch in VALUE and
// not one in JSON number formatting.
func storedBaseStrength(t *testing.T, pool *pgxpool.Pool, id string) float64 {
	t.Helper()
	var v float64
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT base_strength FROM memories WHERE id = $1`, id).Scan(&v))
	return v
}

func reinforceRespBaseStrength(t *testing.T, rec *httptest.ResponseRecorder) float64 {
	t.Helper()
	var body struct {
		BaseStrength    float64 `json:"base_strength"`
		ActivationCount int     `json:"activation_count"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "body=%s", rec.Body.String())
	return body.BaseStrength
}

// TestReinforceMemory_ResponseMatchesTheStoredBaseStrength holds aihub#475's
// criterion — every published base_strength must be the one the column holds —
// and, since aihub#459, records what the endpoint does with the input that used
// to make the two disagree.
//
// Arm 1 is the reported sequence's successor. `strength_delta: 0.5` is now a
// 400, and this asserts it at the level only a database can: the refusal leaves
// the row EXACTLY as it was — same base_strength, same activation_count — and
// writes no memory_reinforced event. routes_memory_reinforce_integral_test.go
// proves with a nil pool that the guard runs before the pool is reached; this
// arm is what "before" means to the data, and it is the one that would catch a
// guard moved below the UPDATE.
//
// 🔴 Arm 2 is WEAKER than it was, and saying so is the point of this paragraph.
// With every accepted delta integral and every stored value SMALLINT, a handler
// that published its own arithmetic and one that published the row can no longer
// DISAGREE, so `response == column` has stopped discriminating between them here
// — it is a consistency check now, not a detector. What still discriminates is
// DB-free and always-on: TestReinforceResponseReportsTheStoredBaseStrength
// parses the shipped handler and requires the published base_strength to be an
// identifier scanned from a statement whose SQL says RETURNING base_strength.
// That gate, not this file, is what a later refactor back to a bare Exec would
// break. Deleting it because "the DB test covers it" would be deleting the only
// arm that does.
func TestReinforceMemory_ResponseMatchesTheStoredBaseStrength(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, proj := seedStepTestUserAndProject(t, pool)
	memID := seedReinforceableMemory(t, pool, proj, uid, "mem_475_fractional", 3)

	writer := &UserContext{
		UserID:       uid,
		Email:        uid + "@test.local",
		DisplayName:  "Reinforcing Writer",
		UserType:     "human",
		Role:         "writer",
		ProjectRoles: map[string]string{proj: "writer"},
		APIKeyID:     "k_475",
	}

	// ── Arm 1: a fractional delta is refused and writes nothing (aihub#459) ──
	rec := callReinforceRaw(t, pool, memID,
		`{"additional_context":"fractional","strength_delta":0.5}`, writer)
	require.Equal(t, http.StatusBadRequest, rec.Code,
		"strength_delta 0.5 must be refused: the column is SMALLINT, so before "+
			"aihub#459 this stored 3 and answered 3.5 — body=%s", rec.Body.String())
	require.Contains(t, rec.Body.String(), "strength_delta",
		"the rejection must name the caller's own argument; body=%s", rec.Body.String())

	bs, count := reinforceRowState(t, pool, memID)
	require.Equal(t, 3.0, bs,
		"the refused call moved base_strength to %v; a 400 must leave the row alone", bs)
	require.Equal(t, 0, count,
		"the refused call bumped activation_count to %d. reinforce mutates more than "+
			"base_strength, so a guard placed after the UPDATE would return the right "+
			"status over the wrong row — which is the failure a status-only assertion "+
			"cannot see", count)
	require.Equal(t, 0, reinforceEventCount(t, pool, proj),
		"the refused call emitted a memory_reinforced event; the durable history would "+
			"then record a reinforcement the caller was told did not happen")

	// ── Arm 2: the criterion, on the deltas that are still accepted ──────────
	first := reinforceRespBaseStrength(t,
		callReinforce(t, pool, memID, `{"additional_context":"first","strength_delta":1}`, writer))
	afterFirst, _ := reinforceRowState(t, pool, memID)
	require.Equal(t, afterFirst, first,
		"THE criterion: the 200 body said base_strength=%v and the column holds %v. A "+
			"response may not name a value the row does not hold",
		first, afterFirst)
	require.Equal(t, 4.0, afterFirst,
		"an integral delta must still take effect — without this the equality above is "+
			"satisfied by a handler that reinforces nothing and reports the unchanged row")

	second := reinforceRespBaseStrength(t,
		callReinforce(t, pool, memID, `{"additional_context":"second","strength_delta":1}`, writer))
	afterSecond, countAfter := reinforceRowState(t, pool, memID)
	require.Equal(t, afterSecond, second,
		"the second call's body said base_strength=%v and the column holds %v", second, afterSecond)
	require.Equal(t, 5.0, afterSecond)
	require.Equal(t, 2, countAfter,
		"two accepted reinforces after one refused one must leave activation_count at 2")

	// The durable record has to agree with the body. An honest 200 over a lying
	// event would leave the false number in the history a later reader
	// reconstructs the memory from — and the event payload was built from the same
	// Go value as the response, so it carried the same lie.
	var payload []byte
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT payload FROM agent_events
		  WHERE project=$1 AND event_type='memory_reinforced'
		  ORDER BY created_at DESC LIMIT 1`, proj).Scan(&payload))
	var evt struct {
		BaseStrength    float64 `json:"base_strength"`
		ActivationCount int     `json:"activation_count"`
	}
	require.NoError(t, json.Unmarshal(payload, &evt))
	require.Equal(t, afterSecond, evt.BaseStrength,
		"the memory_reinforced event says base_strength=%v, the column holds %v",
		evt.BaseStrength, afterSecond)
	require.Equal(t, 2, evt.ActivationCount,
		"activation_count is read back from the same RETURNING and must be the row's")
	require.Equal(t, 2, reinforceEventCount(t, pool, proj),
		"exactly the two accepted calls may have emitted an event")
}

// TestReinforceMemory_IntegralDeltaStillMoves is the positive control for the
// test above, and it is not optional: every assertion up there is satisfied by a
// handler that reinforces NOTHING and reports the unchanged row, which would be
// a worse bug reported honestly. This arm shows the endpoint still does its job,
// and that the clamp still clamps.
func TestReinforceMemory_IntegralDeltaStillMoves(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, proj := seedStepTestUserAndProject(t, pool)
	memID := seedReinforceableMemory(t, pool, proj, uid, "mem_475_integral", 3)

	writer := &UserContext{
		UserID:       uid,
		Email:        uid + "@test.local",
		DisplayName:  "Reinforcing Writer",
		UserType:     "human",
		Role:         "writer",
		ProjectRoles: map[string]string{proj: "writer"},
		APIKeyID:     "k_475i",
	}

	got := reinforceRespBaseStrength(t,
		callReinforce(t, pool, memID, `{"additional_context":"up one","strength_delta":1}`, writer))
	require.Equal(t, 4.0, got, "an integral delta must still take effect")
	require.Equal(t, 4.0, storedBaseStrength(t, pool, memID))

	// Past the top: the clamp is unchanged by aihub#475 and must still land on
	// MaxBaseStrength rather than on the driver's constraint error.
	rec := callReinforce(t, pool, memID, `{"additional_context":"way up","strength_delta":99}`, writer)
	got = reinforceRespBaseStrength(t, rec)
	require.Equal(t, 5.0, got, "the clamp must still hold the value at MaxBaseStrength")
	require.Equal(t, 5.0, storedBaseStrength(t, pool, memID))

	// aihub#543 wave 2, slice L6 — the response SHAPE on the saturating call.
	//
	// The pf_reinforce_memory card records which candidates lost when aihub#506
	// adjudicated the clamp: "a 400 on an overflowing sum and a `clamped: true`
	// field in the response; the second is why the response shape is untouched".
	// An earlier card revision credited that unchanged shape to K10's declared
	// key set; aihub#531 corrected it, and what the card now cites as holding
	// the shape untouched is THIS arm's exact key set. K10 would in fact redden
	// on an arriving undeclared key — its ratchet is "every top-level key a live
	// response actually carries must be declared" — but its walk drives its one
	// reinforce with no strength_delta, so the only reinforce body it ever reads
	// is a non-saturating one. This call, a delta of 99 onto a row at the top,
	// is the one request where the withdrawn field would have had something to
	// say, and it is out of K10's reach.
	//
	// An EXACT key set, not a check that `clamped` is absent: the ruling was
	// that the shape is untouched, and "no key named clamped" is satisfied by a
	// response that grew a differently-named disclosure instead. Reading it off
	// the same recorder the arms above use costs nothing.
	//
	//	M1  add "clamped": true to the reinforce response      RED
	//	M2  rename memory_id to id in the response             RED
	//	M3  drop activation_count from the response            RED
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded), "body=%s", rec.Body.String())
	keys := make([]string, 0, len(decoded))
	for k := range decoded {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	require.Equal(t, []string{"activation_count", "base_strength", "memory_id"}, keys,
		"the saturating call answered with the keys %v. The owner's aihub#506 ruling kept the "+
			"clamp and REJECTED a `clamped: true` field, so this is the request that would "+
			"disclose one if it existed — and since aihub#531 the card cites exactly this "+
			"assertion, not K10, as what holds the response shape untouched.", keys)
}
