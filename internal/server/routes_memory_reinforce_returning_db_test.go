package server

// aihub#475, the behavioural half — what the caller is TOLD versus what the
// column HOLDS.
//
// routes_memory_reinforce_honesty_test.go asserts the shape of the code and can
// run anywhere. It cannot run the defect: the bug was a divergence between a
// response body and a stored row, and observing that needs a row. So this file
// reproduces the reported sequence against a real database — reinforce with
// `strength_delta: 0.5` twice — and compares each response against
// memories.base_strength read straight back out.
//
//	AIHUB_TEST_DB='postgres://postgres:…@localhost:5432/aihub_test?sslmode=disable' \
//	  go test ./internal/server/ -run TestReinforceMemory_ -v -count=1
//
// 🔴 The criterion is response == column, NOT response == 3. Writing 3 into the
// assertion would pin today's truncation rule, and aihub#459 may yet decide that
// a fractional delta is refused, rounded, or made storable by widening the
// column. Under all three of those the response must still equal the row; only
// the number changes. The arm that pins the number is the no-op arm below, and
// it says in one place why it is allowed to.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// callReinforce drives the real handler. No attempt credentials: the memory is
// experience.*, so enforceMethodologyAttemptGate's verify-if-supplied branch
// asks for none.
func callReinforce(t *testing.T, pool *pgxpool.Pool, memID, body string, uc *UserContext) *httptest.ResponseRecorder {
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
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	return rec
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

// TestReinforceMemory_ResponseMatchesTheStoredBaseStrength is the reported
// sequence, verbatim: a row at 3, reinforced twice with strength_delta 0.5.
//
// Before the fix, both calls answered 3.5 while the row stayed at 3, so the
// second call — which had read 3 back and added 0.5 again — reported the same
// "progress" as the first. Two calls rather than one is what separates "the
// number is rounded somewhere" from "this is a permanent no-op reporting
// success": one call alone cannot show that the value never moves.
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

	first := reinforceRespBaseStrength(t,
		callReinforce(t, pool, memID, `{"additional_context":"first","strength_delta":0.5}`, writer))
	afterFirst := storedBaseStrength(t, pool, memID)
	require.Equal(t, afterFirst, first,
		"THE criterion: the 200 body said base_strength=%v and the column holds %v. A "+
			"response may not name a value the row does not hold — whatever aihub#459 "+
			"decides may be stored, this equality holds under all three of its options",
		first, afterFirst)

	second := reinforceRespBaseStrength(t,
		callReinforce(t, pool, memID, `{"additional_context":"second","strength_delta":0.5}`, writer))
	afterSecond := storedBaseStrength(t, pool, memID)
	require.Equal(t, afterSecond, second,
		"the second call's body said base_strength=%v and the column holds %v", second, afterSecond)

	// The no-op, named. This arm DOES pin the number, and it is allowed to
	// because it is the defect's mechanism rather than a policy: pgx truncates
	// toward zero client-side (internal/domain,
	// TestBaseStrengthIsTruncatedByThePgxInt2Codec), so 3 + 0.5 twice is 3 twice.
	// If aihub#459 changes what may be stored, THIS is the arm that must be
	// revisited — deliberately, and in that work item.
	require.Equal(t, 3.0, afterSecond,
		"two reinforces of +0.5 moved the stored strength to %v; the mechanism this fix "+
			"reports honestly is that they move it nowhere", afterSecond)
	require.Equal(t, first, second,
		"the caller was handed %v then %v for two identical no-ops; before aihub#475 both "+
			"were 3.5, which is the progress report that never happened", first, second)

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
	got = reinforceRespBaseStrength(t,
		callReinforce(t, pool, memID, `{"additional_context":"way up","strength_delta":99}`, writer))
	require.Equal(t, 5.0, got, "the clamp must still hold the value at MaxBaseStrength")
	require.Equal(t, 5.0, storedBaseStrength(t, pool, memID))
}
