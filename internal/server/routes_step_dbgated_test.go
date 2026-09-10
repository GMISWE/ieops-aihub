package server

// aihub#354 — the behavioural half of the GET /v1/work_items/:id/step guards.
//
// routes_step_authority_test.go pins what can be reached without a database:
// the truncation arithmetic and the SQL-to-struct column order. Everything
// below needs a real one, because the defects it closes are all of the same
// shape — the handler answers 200 with a confident-looking body that is not
// what the rows say. None of them is a compile error, a vet warning, a driver
// error, or a lint finding.
//
// Each test here was measured RED against a specific mutant on the tree that
// preceded it, and green on the tree as shipped. The mutant is named in each
// doc comment; "measured green on all five gates" below means build, vet, `go
// test ./...`, `go test ./...` with AIHUB_TEST_DB set, and golangci-lint all
// passed with the mutant applied.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/server/ -run 'TestHandleGetStep_|TestHandleUpdateStep_EscalatedSurvives' -v -count=1
//
// 🔴 The hole this file used to leave uncovered, and where it is covered now.
// handleUpdateStep reads UpdateStepRequest.Escalated only inside
// `case "failed":`, so PATCH {"status":"completed","escalated":true} is neither
// persisted nor rejected — it is silently dropped. routes_step.go argues the
// opposite for next_step, in this same handler: "every combination that cannot
// be honoured is REJECTED rather than ignored ... the argument for never
// accepting a parameter we are not going to act on". Fixing that is a behaviour
// change to a shipped endpoint; it was out of aihub#354's scope, which was gates
// only, and aihub#398's owner decision was to document the drop rather than
// change it — which is what docs/mcp-cards/pf_update_step.md publishes.
// aihub#543 probe wave 1 therefore PINS the documented behaviour, defect and
// all, in a subtest of
// TestHandleUpdateStep_EscalatedSurvivesToTheHistoryRead, the same shape
// TestHandleUpdateStep_DoubleCompleteIsCaughtByWhicheverGuardApplies's variant C
// uses for its own residual: an unasserted comment about a known hole rots
// silently. If you are here to make the combination a 400, that arm goes red and
// its failure message says so — flip it, and correct the card sentence in the
// same change.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// newStepGetRequest builds an authenticated echo.Context for
// GET /v1/work_items/:id/step, mirroring newStepUpdateRequest.
func newStepGetRequest(t *testing.T, wiID string, uc *UserContext) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/work_items/"+wiID+"/step", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(wiID)
	setUser(c, uc)
	return c, rec
}

func stepViewer(uid, project string) *UserContext {
	return &UserContext{UserID: uid, DisplayName: uid, Role: "writer",
		ProjectRoles: map[string]string{project: "viewer"}}
}

func stepWriter(uid, project string) *UserContext {
	return &UserContext{UserID: uid, DisplayName: uid, Role: "writer",
		ProjectRoles: map[string]string{project: "writer"}}
}

// seedStepCompletion writes one wi_step_completions row directly.
//
// Direct SQL rather than a PATCH round-trip on purpose: the column-mapping test
// needs one row with artifact_summary set and error_type NULL and another with
// the reverse, and a NULL on exactly one side is what makes a sibling-reading
// expression observable at all. Driving that through handleUpdateStep would let
// the write path decide which columns are NULL, which is the thing under test.
func seedStepCompletion(t *testing.T, pool *pgxpool.Pool, wiID, stepID, status string,
	summary, errorType *string, escalated bool, agoMinutes int,
) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO wi_step_completions
			(id, work_item_id, step_id, step_attempt_id, status, artifact_summary, error_type, escalated, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now() - make_interval(mins => $9))`,
		domain.NewID("sc"), wiID, stepID, domain.NewID("sa"), status, summary, errorType, escalated, agoMinutes)
	require.NoError(t, err)
}

var safeSQLIdent = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// newTableDeniedPool returns a pool whose every connection runs as a
// purpose-built role that can read everything in the schema EXCEPT one table.
//
// Why this fixture exists at all. Two of handleGetStep's guarantees are about a
// read that FAILS, not a read that returns nothing — "a query failure is not
// reported as idle/empty" and "the 500 does not carry the driver's own text".
// Both are unreachable from a test that only supplies data, and both were
// measured green on all five gates when reverted (aihub#354). Something has to
// make a specific query fail.
//
// Why not the two cheaper tricks:
//
//   - A cancelled request context fails the WRONG query. handleGetStep resolves
//     the work item on the same context first, so a cancelled one answers 404
//     and never reaches either read.
//   - Dropping or renaming the table leaves a broken database behind if the
//     process dies between the rename and the deferred rename-back, and the
//     next run of an unrelated suite pays for it.
//
// Revoking SELECT from a role is idempotent and needs no teardown DURING a run,
// which is what makes it safe to use from a test. Postgres applies table ACLs to
// a superuser session only after SET ROLE, which is what the AfterConnect hook is
// for.
//
// What it DOES leave behind, stated because an earlier version of this comment
// said "nothing has to be undone" and that is not true: the two roles survive in
// the cluster, and every table in schema public gains an explicit ACL entry for
// them. Consequences, none of which bite CI (the service container is thrown
// away) or a sibling suite (a GRANT takes no lock on the tables it names, and the
// role is granted nothing but SELECT):
//
//   - `DROP ROLE aihub_test_denied_*` then fails with "cannot be dropped because
//     some objects depend on it". Cleaning a long-lived local test database needs
//     `DROP OWNED BY <role>; DROP ROLE <role>;`.
//   - `pg_dump` of that database emits GRANT statements naming these roles, and
//     restoring it elsewhere errors on the missing roles.
//
// Two more limits worth knowing. GRANT ... ON ALL TABLES is point-in-time, so a
// table added by a later migration is not covered — harmless today because CI
// migrates before this step and the GRANT re-runs on every fixture construction,
// but it would bite immediately if this setup were ever memoised behind a
// sync.Once. And ALL TABLES covers tables and views, NOT sequences or functions:
// a future read path touching one would fail on the ALLOWED side and be misread
// as the denial under test.
//
// This is the one fixture in the package that needs more than DML rights. A
// non-superuser AIHUB_TEST_DB fails here loudly rather than skipping, on purpose:
// a skip would silently drop the coverage two of these tests exist to provide.
func newTableDeniedPool(t *testing.T, admin *pgxpool.Pool, deniedTable string) *pgxpool.Pool {
	t.Helper()
	require.Regexp(t, safeSQLIdent, deniedTable, "table name is spliced into DDL")
	ctx := context.Background()
	role := "aihub_test_denied_" + deniedTable
	// Postgres truncates identifiers at NAMEDATALEN-1. A truncated role would
	// never match the pg_roles probe below, so CREATE ROLE would be retried
	// forever and fail as a duplicate every time.
	require.LessOrEqualf(t, len(role), 63,
		"role name %q is %d bytes; Postgres would truncate it to 63 and the existence probe "+
			"below would never match", role, len(role))
	roleSQL := pgx.Identifier{role}.Sanitize()

	// CREATE ROLE has no IF NOT EXISTS, and two test binaries can race here.
	var exists bool
	require.NoError(t, admin.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, role).Scan(&exists))
	if !exists {
		if _, err := admin.Exec(ctx, `CREATE ROLE `+roleSQL+` NOLOGIN NOINHERIT`); err != nil {
			require.NoError(t, admin.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, role).Scan(&exists))
			require.Truef(t, exists,
				"could not create role %s: %v. This fixture needs an AIHUB_TEST_DB user with "+
					"superuser or CREATEROLE; every other DB-gated test in this repo needs only DML.",
				role, err)
		}
	}
	for _, stmt := range []string{
		`GRANT USAGE ON SCHEMA public TO ` + roleSQL,
		`GRANT SELECT ON ALL TABLES IN SCHEMA public TO ` + roleSQL,
		`REVOKE ALL ON ` + pgx.Identifier{deniedTable}.Sanitize() + ` FROM ` + roleSQL,
	} {
		// Concurrent GRANTs on the same catalog rows raise 40001-adjacent
		// "tuple concurrently updated" (XX000), which is transient and has no
		// bearing on what is under test. Reproduced with six concurrent sessions,
		// so it is retried rather than reported as a mystery failure.
		var err error
		for attempt := 0; attempt < 5; attempt++ {
			if _, err = admin.Exec(ctx, stmt); err == nil {
				break
			}
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Message != "tuple concurrently updated" {
				break
			}
			time.Sleep(time.Duration(attempt+1) * 50 * time.Millisecond)
		}
		require.NoErrorf(t, err, "privilege setup failed: %s", stmt)
	}

	// Copied from the admin pool rather than re-read from AIHUB_TEST_DB, so the
	// restricted pool cannot end up pointed at a different database — and so
	// that setupStepTestDB stays the single place this package reads that
	// variable, which is what makes internal/citest/dbtestcov able to classify
	// these tests as DB-gated.
	cfg := admin.Config().Copy()
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SET ROLE `+roleSQL)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	// Negative control. The fixture is only evidence if the denial actually took,
	// AND if it took for the reason claimed: this is also the first statement
	// that opens a connection (pgxpool.NewWithConfig above is lazy, so its
	// require.NoError proves nothing), so a SET ROLE that failed in AfterConnect,
	// a mistyped table name (42P01) or an unreachable server would all satisfy a
	// bare "an error happened". Only 42501 insufficient_privilege does.
	var n int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{deniedTable}.Sanitize()).Scan(&n)
	require.Errorf(t, err,
		"the restricted pool can still read %s (count=%d), so this fixture proves nothing", deniedTable, n)
	var pgErr *pgconn.PgError
	require.Truef(t, errors.As(err, &pgErr) && pgErr.Code == "42501",
		"reading %s failed, but not with 42501 insufficient_privilege — got %v. The fixture is "+
			"not demonstrating the denial it claims to.", deniedTable, err)

	return pool
}

// TestHandleGetStep_SummaryAndErrorTypeLandInTheirOwnFields is the behavioural
// twin of TestCompletedStepsQueryMatchesStructOrder.
//
// artifact_summary and error_type are both nullable text, so every way of
// crossing them is silent: the compiler, vet, the driver and any test that
// merely checks "a row came back" all stay quiet. Two mutants were measured on
// the pre-aihub#354 tree:
//
//   - the PLAIN transposition (…, error_type, artifact_summary, …) — already
//     red on the DB-free column-order guard, and red here too;
//   - COALESCE(artifact_summary, error_type) paired with
//     COALESCE(error_type, artifact_summary) — GREEN on all five gates, while
//     the live handler returned
//     "artifact_summary":"SUMMARY_ONLY_VALUE","error_type":"SUMMARY_ONLY_VALUE"
//     for the completed step below and the mirror of that for the failed one.
//
// The NULL on exactly one side of each row is what makes the second mutant
// visible. A fixture where both columns are populated cannot distinguish
// COALESCE(a, b) from a.
func TestHandleGetStep_SummaryAndErrorTypeLandInTheirOwnFields(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)

	seedStepCompletion(t, pool, wi.ID, "spec", "completed", strptr("SUMMARY_ONLY_VALUE"), nil, false, 2)
	seedStepCompletion(t, pool, wi.ID, "plan", "failed", nil, strptr("ERRTYPE_ONLY_VALUE"), true, 1)

	c, rec := newStepGetRequest(t, wi.ID, stepViewer(uid, project))
	require.NoError(t, handleGetStep(pool)(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got StepState
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got), "body=%s", rec.Body.String())
	require.Len(t, got.CompletedSteps, 2, "body=%s", rec.Body.String())

	// Oldest first: the completed step, whose error_type is NULL in the row.
	done := got.CompletedSteps[0]
	require.Equal(t, "spec", done.StepID, "history is not oldest-first")
	require.NotNil(t, done.ArtifactSummary, "the completed step lost its summary; body=%s", rec.Body.String())
	require.Equal(t, "SUMMARY_ONLY_VALUE", *done.ArtifactSummary)
	require.Nilf(t, done.ErrorType,
		"the completed step's error_type is %q, but the row stores NULL there. A SELECT-list "+
			"expression that falls back to a sibling column when its own is NULL files one step's "+
			"summary under both fields — see routes_step_authority_test.go. body=%s",
		derefStr(done.ErrorType), rec.Body.String())

	// The failed step, whose artifact_summary is NULL in the row.
	failed := got.CompletedSteps[1]
	require.Equal(t, "plan", failed.StepID)
	require.NotNil(t, failed.ErrorType, "the failed step lost its error_type; body=%s", rec.Body.String())
	require.Equal(t, "ERRTYPE_ONLY_VALUE", *failed.ErrorType)
	require.Nilf(t, failed.ArtifactSummary,
		"the failed step's artifact_summary is %q, but the row stores NULL there — the error text "+
			"has surfaced under the summary field. body=%s",
		derefStr(failed.ArtifactSummary), rec.Body.String())
	require.True(t, failed.Escalated, "escalated did not survive the read; body=%s", rec.Body.String())
}

// TestHandleGetStep_EmptyHistoryIsAnEmptyArrayNeverNull closes the hole
// TestGetStepCompletedStepsDistinguishesEmptyFromAbsent (internal/mcp) leaves
// open.
//
// That test asserts on hand-built server.StepState values and on the struct
// tag; it never reads a response the handler produced. Measured: making
// handleGetStep assign nil instead of truncateCompletedSteps' result — so the
// wire carries "completed_steps":null — is GREEN on all five gates, that test
// included. `null` is the third spelling of empty on a field whose entire
// contract is that `[]` and absent mean different things, and a client that
// treats it as absent concludes the server predates aihub#265 and starts over
// from step 1.
func TestHandleGetStep_EmptyHistoryIsAnEmptyArrayNeverNull(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)

	c, rec := newStepGetRequest(t, wi.ID, stepViewer(uid, project))
	require.NoError(t, handleGetStep(pool)(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := rec.Body.String()
	require.Contains(t, body, `"completed_steps":[]`,
		"a work item with no completions must serialise an empty ARRAY; body=%s", body)
	require.NotContains(t, body, `"completed_steps":null`,
		"null is the spelling a client reads as \"this server cannot answer\"; body=%s", body)
}

// TestHandleGetStep_MissingStepStateRowStillReturnsTheHistory pins the two
// tables' independence.
//
// wi_step_completions is append-only and survives a wi_step_state reset, so a
// resuming agent's history has to be readable when the current-state row is
// gone — which is exactly the resume path aihub#265 exists for. Keying the
// history read on the state read succeeding would reintroduce a silent empty
// answer there, and no other test in the repo executes that combination.
func TestHandleGetStep_MissingStepStateRowStillReturnsTheHistory(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)
	attemptID := seedStepTestAttempt(t, pool, wi.ID, uid)
	seedStepCompletion(t, pool, wi.ID, "spec", "completed", strptr("DONE_BEFORE_THE_RESET"), nil, false, 3)

	// A wi_step_state row has to EXIST before it can be reset, and
	// domain.CreateWorkItem does not write one — only a step transition does. An
	// earlier version of this test deleted straight after creating the work item,
	// which removed zero rows: it exercised "a row that never existed" while its
	// name and comment claimed "a row that was reset". The end state is the same
	// either way, so nothing failed; the setup was simply not doing what it said.
	c0, rec0 := newStepUpdateRequest(t, wi.ID,
		`{"status":"in_progress","step":"spec","attempt_id":"`+attemptID+
			`","step_attempt_id":"`+domain.NewID("sa")+`"}`, stepWriter(uid, project))
	require.NoError(t, handleUpdateStep(pool)(c0))
	require.Equal(t, http.StatusOK, rec0.Code, rec0.Body.String())

	tag, err := pool.Exec(context.Background(), `DELETE FROM wi_step_state WHERE work_item_id=$1`, wi.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected(),
		"the reset deleted no row, so this test is not exercising a reset at all")

	c, rec := newStepGetRequest(t, wi.ID, stepViewer(uid, project))
	require.NoError(t, handleGetStep(pool)(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got StepState
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "idle", got.CurrentStepStatus, "no wi_step_state row is a real answer, not an error")
	require.EqualValues(t, 0, got.Version)
	require.Len(t, got.CompletedSteps, 1,
		"the step history was dropped because wi_step_state had no row; body=%s", rec.Body.String())
	require.Equal(t, "DONE_BEFORE_THE_RESET", derefStr(got.CompletedSteps[0].ArtifactSummary))
}

// TestHandleGetStep_StepStateReadFailureIsNotReportedAsIdle covers the arm the
// pre-aihub#265 code did not have.
//
// That code answered `idle, version 0` for ANY wi_step_state read error, which
// is byte-for-byte the reply that means "this work item has not started a
// step". A resuming agent cannot tell the two apart, so a transient database
// fault reads as "nothing is done" and it redoes finished work — the same
// false negative aihub#265 removed from the history read.
//
// Measured: reverting the switch to `if scanErr != nil { idle; version = 0 }`
// (the verbatim pre-aihub#265 form, "errors" import and all) is GREEN on all
// five gates. This test is the only thing that separates them, because the
// difference is invisible unless a read actually fails.
func TestHandleGetStep_StepStateReadFailureIsNotReportedAsIdle(t *testing.T) {
	admin := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, admin)
	wi := seedStepTestWI(t, admin, project, uid)
	denied := newTableDeniedPool(t, admin, "wi_step_state")

	c, rec := newStepGetRequest(t, wi.ID, stepViewer(uid, project))
	_ = handleGetStep(denied)(c)

	body := rec.Body.String()
	require.Equalf(t, http.StatusInternalServerError, rec.Code,
		"a failed wi_step_state read was answered %d. If that answer is 200 idle/version 0 it is "+
			"indistinguishable from \"nothing has started\". body=%s", rec.Code, body)
	require.Contains(t, body, "step state read failed", "body=%s", body)
	require.NotContains(t, body, `"current_step_status":"idle"`, "body=%s", body)
}

// TestHandleGetStep_HistoryReadFailureIsAnErrorNotAnEmptyHistory pins both
// halves of what a failed history read must do.
//
// It must not answer 200 with an empty history: that tells a resuming agent
// "nothing is done" when the truth is "I could not find out", which is the
// stale-local-file lie relocated into a confident server response.
//
// And its 500 must not carry the driver's own text. This endpoint is open to
// any project VIEWER, and pgx errors carry relation names, column names and
// SQLSTATEs. Measured: replacing the fixed message with histErr.Error() is
// GREEN on all five gates — nothing in the repo reads this body.
func TestHandleGetStep_HistoryReadFailureIsAnErrorNotAnEmptyHistory(t *testing.T) {
	admin := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, admin)
	wi := seedStepTestWI(t, admin, project, uid)
	seedStepCompletion(t, admin, wi.ID, "spec", "completed", strptr("UNREADABLE"), nil, false, 2)
	denied := newTableDeniedPool(t, admin, "wi_step_completions")

	c, rec := newStepGetRequest(t, wi.ID, stepViewer(uid, project))
	_ = handleGetStep(denied)(c)

	body := rec.Body.String()
	require.Equalf(t, http.StatusInternalServerError, rec.Code,
		"a failed history read was answered %d; 200 with an empty history is the answer a resuming "+
			"agent reads as \"nothing is done\". body=%s", rec.Code, body)
	require.Contains(t, body, "step history read failed", "body=%s", body)
	require.NotContains(t, body, `"completed_steps"`, "body=%s", body)

	// The leak list is the vocabulary a pgx error brings with it, not a
	// transcription of one particular message: the relation name, the SQLSTATE
	// marker, the code, and the server's own wording.
	for _, leak := range []string{"wi_step_completions", "SQLSTATE", "42501", "permission denied"} {
		require.NotContainsf(t, body, leak,
			"the 500 body carries %q, which came from the driver. Any project viewer can read this "+
				"reply; the detail belongs in the server log. body=%s", leak, body)
	}
}

// TestHandleUpdateStep_EscalatedSurvivesToTheHistoryRead walks escalated all
// the way from the request body to the response of the OTHER endpoint.
//
// The column was added to the failed-path INSERT by aihub#265. Measured:
// reverting that INSERT to its previous column list — so escalated falls back
// to the schema default — is GREEN on all five gates. TestHandleUpdateStep_
// EscalatedStall covers what escalation does to the WORK ITEM (status blocked,
// wi_stalled event); nothing asserted that the flag is still there when the
// history is read back, which is where a resuming agent looks to find out that
// a human was asked to triage this step.
func TestHandleUpdateStep_EscalatedSurvivesToTheHistoryRead(t *testing.T) {
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	wi := seedStepTestWI(t, pool, project, uid)
	attemptID := seedStepTestAttempt(t, pool, wi.ID, uid)
	uc := stepWriter(uid, project)

	startSA := domain.NewID("sa")
	c1, rec1 := newStepUpdateRequest(t, wi.ID,
		`{"status":"in_progress","step":"code","attempt_id":"`+attemptID+
			`","step_attempt_id":"`+startSA+`"}`, uc)
	require.NoError(t, handleUpdateStep(pool)(c1))
	require.Equal(t, http.StatusOK, rec1.Code, rec1.Body.String())

	// `"step":"code"` names the step that is actually open, and it is REQUIRED
	// since aihub#398: a terminal transition whose step_id does not equal the
	// stored current_step is refused 409. This fixture used to omit it, which
	// worked only because the history row took its step_id from the STORED value
	// — the very coupling aihub#398 removed. No real client can omit it either
	// (pf_update_step publishes step_id as required and the MCP layer checks it),
	// so naming it here makes the fixture match the contract rather than exploit
	// a gap in it.
	failSA := domain.NewID("sa")
	c2, rec2 := newStepUpdateRequest(t, wi.ID,
		`{"status":"failed","step":"code","attempt_id":"`+attemptID+`","step_attempt_id":"`+failSA+
			`","error_type":"gate_failed","artifact_summary":"needs a human","escalated":true}`, uc)
	require.NoError(t, handleUpdateStep(pool)(c2))
	require.Equal(t, http.StatusOK, rec2.Code, rec2.Body.String())

	// The row itself, so a read-path bug cannot be mistaken for a write-path one.
	var stored *bool
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT escalated FROM wi_step_completions WHERE step_attempt_id=$1`, failSA).Scan(&stored),
		"no wi_step_completions row was written for step_attempt_id=%s", failSA)
	require.NotNil(t, stored, "escalated is NULL in the row")
	require.True(t, *stored, "escalated=true was dropped on the way to wi_step_completions")

	// And what a resuming agent actually reads.
	c3, rec3 := newStepGetRequest(t, wi.ID, uc)
	require.NoError(t, handleGetStep(pool)(c3))
	require.Equal(t, http.StatusOK, rec3.Code, rec3.Body.String())
	var got StepState
	require.NoError(t, json.Unmarshal(rec3.Body.Bytes(), &got))
	require.Len(t, got.CompletedSteps, 1, "body=%s", rec3.Body.String())
	require.True(t, got.CompletedSteps[0].Escalated,
		"the history read does not report the escalation, so a resuming agent cannot tell that this "+
			"step was handed to a human; body=%s", rec3.Body.String())
	require.Equal(t, "gate_failed", derefStr(got.CompletedSteps[0].ErrorType))
	require.Equal(t, "needs a human", derefStr(got.CompletedSteps[0].ArtifactSummary))
	// Added with the step_id above (aihub#398): the escalated row has to be
	// findable by the step it belongs to. Before that change this value came
	// from wi_step_state rather than from the request, so a fixture that named
	// no step still got "code" here and the two sources were indistinguishable.
	require.Equal(t, "code", got.CompletedSteps[0].StepID,
		"the escalated step must be recorded under the step the caller named")

	// aihub#543 probe wave 1. docs/mcp-cards/pf_update_step.md publishes the
	// other half of these two fields — "`error_type` / `escalated` on a
	// `completed` call are silently dropped, which is documented rather than
	// changed because rejecting them is a behaviour change" — and the tool's own
	// InputSchema says "ignored (not refused) on completed". Everything above is
	// the FAILED path. Nothing drove the completed one, so both halves of
	// "ignored" were unheld: a handler that started refusing the combination, or
	// one that started storing it, would have been the same green.
	//
	// A subtest of this function rather than a new gated one, for the reason
	// aihub#543 spec §3.3 rule 2 gives: this function is already in
	// internal/citest/dbtestcov/gated_tests.txt and already named by a ci.yml
	// step, so the claim costs no manifest line.
	//
	// MUTANTS (applied to this tree; the verdict is what ran):
	//
	//	M3 enforcement: pass req.ErrorType/req.Escalated to
	//	   insertStepCompletion on the COMPLETED branch instead of nil/false
	//	                                          RED  both stored-value assertions
	//	M4 enforcement: refuse a completed call carrying either field (400)
	//	                                          RED  the 200 assertion
	//	M5 publication: remove EVERY citation from the card sentence carrying this
	//	   claim                                  RED  K12
	//	M5b publication: remove only THIS arm's citation, leaving the two schema
	//	   arms cited in the same sentence       GREEN measured, and recorded rather
	//	                                              than hidden: K12 binds the
	//	                                              SENTENCE, and that sentence is
	//	                                              three claims in one bullet, so
	//	                                              no ledger movement isolates
	//	                                              this conjunct (aihub#543 spec
	//	                                              §1.3, "one row, several arms")
	t.Run("error_type and escalated on a completed call are neither stored nor refused", func(t *testing.T) {
		// Read before the call, not compared against a literal: the parent body
		// above already escalated a FAILED step, which blocks the work item by
		// design (spec A-1). The claim here is that the completed call moves this
		// value not at all, so the fixture's own state is the baseline.
		wiStatusBefore := readWorkItemStatus(t, pool, wi.ID)

		startSA2, doneSA := domain.NewID("sa"), domain.NewID("sa")
		c4, rec4 := newStepUpdateRequest(t, wi.ID,
			`{"status":"in_progress","step":"ship","attempt_id":"`+attemptID+
				`","step_attempt_id":"`+startSA2+`"}`, uc)
		require.NoError(t, handleUpdateStep(pool)(c4))
		require.Equal(t, http.StatusOK, rec4.Code, rec4.Body.String())

		c5, rec5 := newStepUpdateRequest(t, wi.ID,
			`{"status":"completed","step":"ship","attempt_id":"`+attemptID+`","step_attempt_id":"`+doneSA+
				`","error_type":"gate_failed","artifact_summary":"shipped","escalated":true}`, uc)
		require.NoError(t, handleUpdateStep(pool)(c5))
		require.Equal(t, http.StatusOK, rec5.Code,
			"the two fields are DOCUMENTED as ignored on a completed call (aihub#398's owner decision, "+
				"published on the card), so refusing them is a behaviour change and not a fix that can "+
				"land unannounced. If you came here to make this a 400: that is fine, this arm is the "+
				"MEASURED RESIDUAL and not a wish — flip it and correct the card sentence in the same "+
				"change; body: %s", rec5.Body.String())

		var storedErrorType *string
		var storedEscalated *bool
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT error_type, escalated FROM wi_step_completions WHERE step_attempt_id=$1`, doneSA).
			Scan(&storedErrorType, &storedEscalated),
			"no wi_step_completions row was written for the completion")
		assert.Nil(t, storedErrorType,
			"error_type reached the completed row. The card calls it dropped and pf_get_step's "+
				"completed_steps carries error_type per entry, so a stored value makes a successful step "+
				"read back as one that failed for the reason the caller happened to pass")
		if storedEscalated != nil {
			assert.False(t, *storedEscalated,
				"escalated reached the completed row, so a finished step reads back as one handed to a "+
					"human — and on the failed path that same flag blocks the work item")
		}

		// The work item is untouched too: the escalated-stall block that answers
		// this flag lives in the failed branch alone, and it is what makes the
		// difference between "ignored" and "ignored except for one side effect".
		assert.Equal(t, wiStatusBefore, readWorkItemStatus(t, pool, wi.ID),
			"a COMPLETED call carrying escalated=true must leave the work item's status exactly as it "+
				"found it; stalling for triage is the failed branch's spec A-1 behaviour and this is the "+
				"branch the card calls silent")

		// And the read path agrees, which is where a caller would actually see it.
		c6, rec6 := newStepGetRequest(t, wi.ID, uc)
		require.NoError(t, handleGetStep(pool)(c6))
		require.Equal(t, http.StatusOK, rec6.Code, rec6.Body.String())
		var after StepState
		require.NoError(t, json.Unmarshal(rec6.Body.Bytes(), &after))
		var shipped *CompletedStep
		for i := range after.CompletedSteps {
			if after.CompletedSteps[i].StepID == "ship" {
				shipped = &after.CompletedSteps[i]
			}
		}
		require.NotNil(t, shipped, "the completion is missing from the history; body=%s", rec6.Body.String())
		assert.Equal(t, "completed", shipped.Status)
		assert.Nil(t, shipped.ErrorType, "the history read must not report an error_type for a completed step")
		assert.False(t, shipped.Escalated, "the history read must not report the step as escalated")
		// FLOOR: the failed row from the parent body is still there carrying both
		// fields. Without it every assertion above is satisfied by a database
		// that stores neither field for anybody, which would be the aihub#265
		// regression rather than the documented drop.
		var failedRows int
		require.NoError(t, pool.QueryRow(context.Background(),
			`SELECT count(*) FROM wi_step_completions
			  WHERE work_item_id=$1 AND status='failed' AND error_type='gate_failed' AND escalated`,
			wi.ID).Scan(&failedRows))
		require.Equal(t, 1, failedRows,
			"FLOOR: the failed path must still be storing both fields, or 'dropped on completed' is "+
				"indistinguishable from 'dropped everywhere'")
	})
}

// readWorkItemStatus is the work item's own status column, read directly.
//
// A helper rather than an inline query because the assertion it feeds is a
// BEFORE/AFTER comparison: the two reads have to be the same read, or a
// difference in how they are written is indistinguishable from a difference in
// what the handler did.
func readWorkItemStatus(t *testing.T, pool *pgxpool.Pool, wiID string) string {
	t.Helper()
	var status string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT status FROM work_items WHERE id=$1`, wiID).Scan(&status))
	return status
}
