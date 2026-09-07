package server

// aihub#175 finding 1 (IDOR) and the handler half of finding 2 / aihub#349.
//
// PATCH /v1/memories/:id/redact used to call domain.Redact with no project
// check at all. domain.Redact's own rule is "the author, or a global admin" —
// a rule that never expires. So a member who wrote a memory and then LOST
// access to the project could still delete it, which is exactly the population
// a project-access check exists to keep out. Every other memory mutation
// (activate / reinforce / commit / resolve / reply) already gated on
// checkProjectAccess(writer) first.
//
//	AIHUB_TEST_DB='postgres://postgres:…@localhost:5432/aihub_test?sslmode=disable' \
//	  go test ./internal/server/ -run TestRedactMemory -v -count=1
//
// Why a DB test rather than the stub-based shape of routes_memory_activate_test.go:
// the assertion that matters is that a genuinely unauthorised caller does not
// get the memory deleted. A stubbed domain.Redact can only show that the stub
// wasn't called, which is a claim about the test's own wiring — it stays green
// if the gate is present but the row is mutated by some other path, and it
// cannot observe the audit columns at all. Here the caller is a real user with
// no row in the project's member set, the handler runs against a real pool, and
// the criterion is the memory's own status column afterwards.
//
// 🔴 The negative case's criterion is `status == "active"`, not merely
// `code == 403`. A handler that answers 403 after having already redacted the
// row would satisfy the status code and still be the bug.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

// seedRedactableMemory inserts one active memory in proj authored by author and
// returns its id. Also clears any memory_redacted events left in the project by
// a previous run of the same test, so the event assertions below measure this
// run only.
func seedRedactableMemory(t *testing.T, pool *pgxpool.Pool, proj, author, id string) string {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `DELETE FROM memories WHERE id = $1`, id)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`INSERT INTO memories(id, project, author_user_id, type, content)
		 VALUES($1, $2, $3, 'experience.debug', 'redact me')`, id, proj, author)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`DELETE FROM agent_events WHERE project = $1 AND event_type = 'memory_redacted'`, proj)
	require.NoError(t, err)
	return id
}

// callRedact drives the real handler with the given caller and JSON body.
func callRedact(t *testing.T, pool *pgxpool.Pool, memID, body string, uc *UserContext) (*httptest.ResponseRecorder, error) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPatch, "/v1/memories/"+memID+"/redact", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(memID)
	setUser(c, uc)
	return rec, handleRedactMemory(pool)(c)
}

func memStatus(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var s string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT status FROM memories WHERE id = $1`, id).Scan(&s))
	return s
}

// TestRedactMemory_AuthorWithoutProjectAccessIsRefused is the aihub#175
// finding-1 regression gate. The caller IS the author — so domain.Redact's own
// author-or-admin rule says yes — and holds no role whatsoever on the memory's
// project. Before the fix this returned 200 and the row came back 'redacted'.
func TestRedactMemory_AuthorWithoutProjectAccessIsRefused(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, proj := seedStepTestUserAndProject(t, pool)
	memID := seedRedactableMemory(t, pool, proj, uid, "mem_idor_evicted")

	evicted := &UserContext{
		UserID:       uid, // the author
		Email:        uid + "@test.local",
		DisplayName:  "Evicted Author",
		UserType:     "human",
		Role:         "writer", // NOT a global admin
		ProjectRoles: map[string]string{},
		APIKeyID:     "k_evicted",
	}
	require.False(t, hasProjectAccess(evicted, proj, "viewer"),
		"fixture sanity: the caller must genuinely lack project access, else this proves nothing")

	rec, err := callRedact(t, pool, memID, `{"reason":"I still want this gone"}`, evicted)

	require.Error(t, err, "an evicted author must be refused")

	// 🔴 Expected status changed 403 -> 404 on 2026-09-06 (aihub#377). CONTRACT
	// CHANGE, not a red test tuned green. The invariant, verbatim:
	//
	//	在某个 project 里的用户，能看到该 project 的一切（memory、work item、
	//	artifact、event、step、依赖）；不在的，对该 project 的一切必须拿到与
	//	「不存在」逐字节相同的响应。
	//
	//	(A user who is in a project can see everything about it. A user who is
	//	not must get a response byte-identical to the one for something that
	//	does not exist.)
	//
	// The evicted author holds no role on the project any more — the arm above
	// asserts exactly that with hasProjectAccess — so they are a non-member and
	// get what a non-member gets. Being the memory's AUTHOR is not a membership;
	// that is the whole point of this test (aihub#175: the project gate is the
	// one that expires, and the author check is not a substitute for it).
	//
	// 🔴 STILL DISCRIMINATING, and note the assertion below is untouched: THE
	// criterion was never the status code. If the eviction were not enforced the
	// redaction would succeed, memStatus would be "redacted", and that line goes
	// red no matter what this one says.
	require.Equal(t, http.StatusNotFound, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, "active", memStatus(t, pool, memID),
		"THE criterion: the memory must be untouched, not merely refused")

	var nEvents int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_events WHERE project=$1 AND event_type='memory_redacted'`,
		proj).Scan(&nEvents))
	require.Zero(t, nEvents, "a refused redaction must not emit a redaction event")
}

// TestRedactMemory_ProjectWriterIsAuditedWithReason is the positive control for
// the gate above (it must not be passing merely because everything is refused)
// and the end-to-end assertion for aihub#349: the `reason` pf_redact_memory
// declares REQUIRED, and has always put on the wire, must survive all the way
// into the row.
func TestRedactMemory_ProjectWriterIsAuditedWithReason(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, proj := seedStepTestUserAndProject(t, pool)
	memID := seedRedactableMemory(t, pool, proj, uid, "mem_idor_writer")

	author := &UserContext{
		UserID:       uid,
		Email:        uid + "@test.local",
		DisplayName:  "Author With Access",
		UserType:     "human",
		Role:         "writer",
		ProjectRoles: map[string]string{proj: "writer"},
		APIKeyID:     "k_writer",
	}

	const reason = "superseded by a newer note"
	rec, err := callRedact(t, pool, memID, `{"reason":"`+reason+`"}`, author)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.Equal(t, "redacted", memStatus(t, pool, memID))

	var storedReason *string
	var redactedAt *string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT redaction_reason, redacted_at::text FROM memories WHERE id=$1`, memID).
		Scan(&storedReason, &redactedAt))
	require.NotNil(t, storedReason, "aihub#349: the reason reached the wire and must not be dropped at the handler")
	require.Equal(t, reason, *storedReason)
	require.NotNil(t, redactedAt, "redacted_at must be stamped")

	var actor, payloadReason string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT actor_user_id, payload->>'reason' FROM agent_events
		  WHERE project=$1 AND event_type='memory_redacted'`, proj).Scan(&actor, &payloadReason))
	require.Equal(t, uid, actor, "the event is the only place the redacting actor is recorded")
	require.Equal(t, reason, payloadReason)
}
