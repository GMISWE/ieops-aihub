package mcp_test

// aihub#324 — the dependency endpoints' REAL authorization model, pinned end to
// end: Postgres, the real echo router, the real pkg/client, the real MCP
// handlers.
//
// ─── what this file is for ───────────────────────────────────────────────────
//
// Adding and removing a dependency requires project `writer` and NOTHING ELSE.
// No run-attempt credential is sent, and none is checked — not by
// handleCreateDependency, not by handleDeleteDependency, not by
// domain.CreateDependency. A `writer` may rewire the dependency graph of a work
// item that somebody else is holding a running attempt on, right now, while they
// run.
//
// Until aihub#324 the code claimed otherwise from three directions at once:
// internal/mcp/tools_dependency.go built attempt_id / claim_epoch /
// session_secret into both request bodies, both tools took a REQUIRED
// `work_item_id` "for credential injection", and domain.CreateDependency carried
// the comment "caller must be the current running attempt holder for
// blocked_wi". None of it was connected to anything — pkg/client.RemoveDependency
// even dropped the whole body on the floor. The credentials are gone now; this
// test is what replaces them, because deleting misleading code leaves no trace
// and the next reader deserves a statement of what IS true that they can run.
//
// ─── 🔴 THIS TEST IS EXPECTED TO GO RED IF THE MODEL EVER CHANGES ────────────
//
// It asserts that a privileged-enough-but-uncredentialed caller SUCCEEDS. If
// somebody later adds genuine attempt validation to the dependency endpoints —
// a legitimate thing to want — these assertions will start failing. That is the
// design, not a regression: this suite is the tripwire that says "the
// authorization model you are changing was written down on purpose, in
// internal/server/router.go above handleCreateDependency; go update that note
// and delete this test in the same change."
//
// Do NOT make it pass by handing the test credentials. Doing so would restore
// exactly the state aihub#324 removed: a test that looks like it exercises an
// authorization boundary while proving nothing about who is kept out.
//
// ─── why it needs a real server ──────────────────────────────────────────────
//
// aihub#319's wiring tests served these very tools from a fake that accepted
// anything, which is why they could not see the defect. A fixture more permissive
// than the real server makes the last hop's assertion a fake one, so every
// assertion below travels the whole path a model travels and lands on a real
// Postgres row.
//
//	AIHUB_TEST_DB='postgres://…@127.0.0.1:5433/aihub_test?sslmode=disable' \
//	  go test ./internal/mcp/ -run TestE2EDependency -count=1 -v
//
// The DB must already be migrated; like the other E2E suites here this test
// connects, it does not migrate. Wired into ci.yml as the "aihub#324 dependency
// authorization-model DB tests" step, and listed in
// internal/citest/dbtestcov/gated_tests.txt.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/auth"
	"github.com/GMISWE/ieops-aihub/internal/mcp"
	"github.com/GMISWE/ieops-aihub/internal/server"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

const (
	depProject = "p_dep_authz_e2e"

	// The actor: project writer, NOT a global admin. A global admin would
	// short-circuit checkProjectAccess entirely and make every assertion here
	// vacuous, so the role matters and is asserted in newDepAuthzStack.
	depWriterUID = "u_dep_authz_writer"
	depWriterKey = "pfk_dep_authz_writer_key" //nolint:gosec // fixture credential for a throwaway test DB

	// The attempt holder: a DIFFERENT user who claims the blocked work item, so
	// that a live run attempt exists and belongs to somebody else at the moment
	// the actor rewires its dependencies.
	depHolderUID = "u_dep_authz_holder"
	depHolderKey = "pfk_dep_authz_holder_key" //nolint:gosec // fixture credential for a throwaway test DB

	// The negative control: viewer on the same project. Without a caller the
	// endpoints REFUSE, "the writer succeeded" is consistent with an endpoint
	// that authorizes nobody out, and this whole file would measure nothing.
	depViewerUID = "u_dep_authz_viewer"
	depViewerKey = "pfk_dep_authz_viewer_key" //nolint:gosec // fixture credential for a throwaway test DB
)

// depAuthzStack is one wired-up copy of the real thing, with three distinct
// callers pointed at it.
type depAuthzStack struct {
	pool   *pgxpool.Pool
	writer *client.Client // project writer, holds no attempt
	holder *client.Client // owns the running attempt on the blocked wi
	viewer *client.Client // project viewer, the refusal control

	// session is an MCP session speaking for the writer, with an EMPTY workspace
	// root: there is no state file anywhere for it to read credentials out of,
	// so "no attempt credentials were sent" is a property of the fixture rather
	// than a promise.
	session *sdkmcp.ClientSession
}

func newDepAuthzStack(t *testing.T) *depAuthzStack {
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

	// A workspace root with no state directory at all. config.ResolveStateFile
	// would fail outright here, which is the point: if any hop in this chain
	// still tried to inject credentials, these tests would error rather than
	// quietly send empty ones.
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", t.TempDir())

	for _, u := range []struct{ uid, key, keyID string }{
		{depWriterUID, depWriterKey, "k_dep_w"},
		{depHolderUID, depHolderKey, "k_dep_h"},
		{depViewerUID, depViewerKey, "k_dep_v"},
	} {
		keys, merr := json.Marshal([]map[string]any{{"id": u.keyID, "key_hash": auth.HashKey(u.key)}})
		if merr != nil {
			t.Fatalf("marshal api keys: %v", merr)
		}
		// role='writer' is the GLOBAL role and is deliberately not 'admin':
		// checkProjectAccess returns nil immediately for admins, which would make
		// every assertion in this file pass no matter what the endpoints did.
		if _, err := pool.Exec(ctx, `
			INSERT INTO users(id,email,display_name,user_type,role,api_keys)
			VALUES($1,$1||'@test.local',$1,'human','writer',$2)
			ON CONFLICT (id) DO UPDATE SET api_keys=EXCLUDED.api_keys, role='writer'`, u.uid, keys); err != nil {
			t.Fatalf("seed user %s: %v", u.uid, err)
		}
	}

	members, err := json.Marshal([]map[string]any{
		{"user_id": depWriterUID, "role": "writer"},
		{"user_id": depHolderUID, "role": "writer"},
		{"user_id": depViewerUID, "role": "viewer"},
	})
	if err != nil {
		t.Fatalf("marshal members: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects(name,owner_user_id,members) VALUES($1,$2,$3)
		ON CONFLICT (name) DO UPDATE SET members=EXCLUDED.members, owner_user_id=EXCLUDED.owner_user_id`,
		depProject, depWriterUID, members); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	// CreateWorkItem runs goal-similarity dedup against live work items in the
	// project, so a previous run's rows would reject this run's create. Child to
	// parent, the order the other E2E suites here use.
	for _, q := range []string{
		`DELETE FROM wi_dependencies WHERE blocked_wi_id IN (SELECT id FROM work_items WHERE project=$1)
		    OR blocking_wi_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM wi_step_completions WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM agent_events WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM wi_step_state WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`UPDATE work_items SET current_attempt_id=NULL WHERE project=$1`,
		// resource_locks keys on owner_attempt_id, and its FK to run_attempts is
		// ON DELETE RESTRICT, so it has to go before the attempts do.
		`DELETE FROM resource_locks WHERE owner_attempt_id IN (
		    SELECT ra.id FROM run_attempts ra JOIN work_items wi ON wi.id = ra.work_item_id WHERE wi.project=$1)`,
		`DELETE FROM run_attempts WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM memories WHERE work_item_id IN (SELECT id FROM work_items WHERE project=$1)`,
		`DELETE FROM work_items WHERE project=$1`,
	} {
		if _, err := pool.Exec(ctx, q, depProject); err != nil {
			t.Fatalf("clean fixture (%s): %v", q, err)
		}
	}

	ts := httptest.NewServer(server.NewRouter(pool, []byte("dep-authz-e2e-cookie-secret-not-a-real-one")))
	t.Cleanup(ts.Close)

	s := &depAuthzStack{
		pool:   pool,
		writer: client.New(ts.URL, depWriterKey),
		holder: client.New(ts.URL, depHolderKey),
		viewer: client.New(ts.URL, depViewerKey),
	}

	mcpServer := mcp.New(nil, s.writer)
	cTransport, sTransport := sdkmcp.NewInMemoryTransports()
	serverCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		srv, cerr := mcpServer.Connect(serverCtx, sTransport)
		if cerr != nil {
			return
		}
		_ = srv.Wait()
	}()
	cl := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "dep-authz-e2e", Version: "1.0.0"}, nil)
	session, err := cl.Connect(ctx, cTransport, nil)
	if err != nil {
		t.Fatalf("mcp client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	s.session = session

	s.assertRoles(t)
	return s
}

// assertRoles reads the identities back THROUGH the real auth middleware, which
// is the only thing that decides what checkProjectAccess will see.
//
// This is the assertion that stops the rest of the file being vacuous. Every
// test here rests on the actor being a project writer and NOT a global admin —
// checkProjectAccess returns nil immediately for `role == "admin"`, so an actor
// that drifted to admin would sail through endpoints that had stopped
// authorizing anything at all and every case below would still be green. The
// seeding INSERT says 'writer', but an ON CONFLICT against a row some other
// suite created, a changed default, or a members blob the middleware decodes
// differently would all leave that INSERT looking correct and the derived
// identity wrong. So read the derived identity, not the intent.
func (s *depAuthzStack) assertRoles(t *testing.T) {
	t.Helper()
	for _, want := range []struct {
		name        string
		c           *client.Client
		projectRole string
	}{
		{"writer", s.writer, "writer"},
		{"holder", s.holder, "writer"},
		{"viewer", s.viewer, "viewer"},
	} {
		me, err := want.c.WhoAmI(context.Background())
		if err != nil {
			t.Fatalf("WhoAmI for the %s: %v", want.name, err)
		}
		if role, _ := me["role"].(string); role != "writer" {
			t.Fatalf("the %s's GLOBAL role is %q, want \"writer\"; an admin bypasses checkProjectAccess "+
				"entirely and would make every assertion in this file pass regardless of what the "+
				"dependency endpoints do", want.name, role)
		}
		roles, _ := me["project_roles"].(map[string]any)
		if got, _ := roles[depProject].(string); got != want.projectRole {
			t.Fatalf("the %s's role on %s is %q, want %q — the fixture is not the scenario these tests describe",
				want.name, depProject, got, want.projectRole)
		}
	}
}

// call invokes a tool for the writer and reports whether it errored, returning
// the text either way — the refusal message is evidence here, not a crash.
func (s *depAuthzStack) call(t *testing.T, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", tool, err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("call %s returned no content", tool)
	}
	text, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("call %s returned %T, want TextContent", tool, res.Content[0])
	}
	return text.Text, res.IsError
}

// newWI creates a work item in the fixture project as the given caller.
//
// force_create is not laziness: CreateWorkItem runs goal-similarity dedup, and
// the two items each test needs are by nature a matched pair ("the upstream
// one", "the downstream one"), which scored 0.78 and got the second one rejected
// with 409 CONFLICT_CANDIDATES. Wording them further apart would work until
// somebody retuned the scorer; opting out states that this fixture is not about
// dedup at all.
func (s *depAuthzStack) newWI(t *testing.T, c *client.Client, goal string) string {
	t.Helper()
	created, err := c.CreateWorkItem(context.Background(), map[string]any{
		"project":      depProject,
		"goal":         goal,
		"wi_type":      "fix_bug", // the server refuses to claim a work item without one
		"force_create": true,
		"force_reason": "fixture pair for the aihub#324 dependency authorization tests; dedup is not under test here",
	})
	if err != nil {
		t.Fatalf("create work item %q: %v", goal, err)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create work item %q returned no id: %v", goal, created)
	}
	return id
}

// claimAsHolder puts a REAL running attempt on wiID, owned by depHolderUID, and
// returns the attempt id. The actor never learns the session secret, because the
// point is that it does not need one.
func (s *depAuthzStack) claimAsHolder(t *testing.T, wiID string) string {
	t.Helper()
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		t.Fatalf("generate session secret: %v", err)
	}
	claimed, err := s.holder.ClaimWorkItem(context.Background(), wiID, map[string]any{
		"idempotency_key": fmt.Sprintf("idem-dep-authz-%d", time.Now().UnixNano()),
		"mode":            "fresh",
		"session_info": map[string]any{
			"machine_id":     "m_dep_authz_e2e",
			"session_secret": hex.EncodeToString(secretBytes),
		},
	})
	if err != nil {
		t.Fatalf("holder could not claim %s: %v", wiID, err)
	}
	attemptID, _ := claimed["attempt_id"].(string)
	if attemptID == "" {
		t.Fatalf("claim returned no attempt_id: %v", claimed)
	}
	return attemptID
}

// assertLiveForeignAttempt reads back, from Postgres, that wiID really is under
// a RUNNING attempt owned by somebody other than the actor at this instant.
//
// Without this the headline test would be measuring the wrong thing: if the
// claim had silently failed, or the attempt had been released, "the writer could
// remove the dependency" would be an unremarkable fact about an idle work item
// rather than the authorization statement it is meant to be.
func (s *depAuthzStack) assertLiveForeignAttempt(t *testing.T, wiID, wantAttempt string) {
	t.Helper()
	var attemptID, status, actor string
	err := s.pool.QueryRow(context.Background(), `
		SELECT ra.id, ra.status, ra.actor_user_id
		FROM work_items wi JOIN run_attempts ra ON ra.id = wi.current_attempt_id
		WHERE wi.id = $1`, wiID).Scan(&attemptID, &status, &actor)
	if err != nil {
		t.Fatalf("work item %s has no current run attempt (%v) — the fixture never established "+
			"the condition this test is about, so its result means nothing", wiID, err)
	}
	if attemptID != wantAttempt || status != "running" || actor != depHolderUID {
		t.Fatalf("blocked wi %s is under attempt %s (status %s, actor %s); "+
			"want the holder's %s, running, actor %s",
			wiID, attemptID, status, actor, wantAttempt, depHolderUID)
	}
	if actor == depWriterUID {
		t.Fatalf("the actor owns the attempt; this test only says something if it does NOT")
	}
}

// blockedBy reports whether the wi_dependencies row exists, read straight from
// Postgres rather than from the API that just claimed to have written it.
func (s *depAuthzStack) blockedBy(t *testing.T, blockedID, blockingID string) bool {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM wi_dependencies WHERE blocked_wi_id=$1 AND blocking_wi_id=$2 AND kind='blocks'`,
		blockedID, blockingID).Scan(&n); err != nil {
		t.Fatalf("count dependency rows: %v", err)
	}
	return n > 0
}

// TestE2EDependencyMutationsNeedNoAttemptCredential is aihub#324's acceptance
// criterion, and it asserts a SUCCESS on purpose.
//
// A project writer that holds no run attempt, has no state file on disk and
// sends no credential of any kind adds a dependency to — and then removes one
// from — a work item that somebody ELSE is running right now. Both calls
// succeed. That is today's authorization model, stated as something executable
// instead of something a reader has to reconstruct from three files.
//
// 🔴 If you have just added credential validation and landed here: this failing
// is the correct behaviour of this test. Update the model note above
// handleCreateDependency in internal/server/router.go and retire this file in
// the same change; do not feed the test credentials to make it green.
func TestE2EDependencyMutationsNeedNoAttemptCredential(t *testing.T) {
	s := newDepAuthzStack(t)

	blocking := s.newWI(t, s.writer, "the upstream item that must finish first")
	blocked := s.newWI(t, s.writer, "the downstream item somebody else is running")

	// Somebody else takes the blocked item and is still holding it below.
	attemptID := s.claimAsHolder(t, blocked)
	s.assertLiveForeignAttempt(t, blocked, attemptID)

	// ── add, with no credentials ────────────────────────────────────────────
	text, isErr := s.call(t, "pf_create_dependency", map[string]any{
		"blocked_wi_id":  blocked,
		"blocking_wi_id": blocking,
		"kind":           "blocks",
	})
	if isErr {
		t.Fatalf("pf_create_dependency was REFUSED for a project writer holding no attempt: %s\n\n"+
			"Today's model authorizes this on project role alone. If that is no longer true, this "+
			"test is the tripwire for the change — see the file header.", text)
	}
	if !s.blockedBy(t, blocked, blocking) {
		t.Fatalf("pf_create_dependency reported success but wrote no wi_dependencies row; "+
			"a 2xx that changes nothing would satisfy the assertion above while proving nothing (%s)", text)
	}

	// Still somebody else's live attempt at the moment of the removal — the
	// condition has to hold HERE, not merely at the top of the test.
	s.assertLiveForeignAttempt(t, blocked, attemptID)

	// ── remove, with no credentials ─────────────────────────────────────────
	//
	// This is the half aihub#324 came from: the MCP handler used to build
	// attempt_id / claim_epoch / session_secret for exactly this call and
	// pkg/client.RemoveDependency threw them away with the rest of the body.
	text, isErr = s.call(t, "pf_remove_dependency", map[string]any{
		"blocked_wi_id":  blocked,
		"blocking_wi_id": blocking,
		"kind":           "blocks",
	})
	if isErr {
		t.Fatalf("pf_remove_dependency was REFUSED for a project writer holding no attempt: %s\n\n"+
			"See the file header before changing this expectation.", text)
	}
	if s.blockedBy(t, blocked, blocking) {
		t.Fatalf("pf_remove_dependency reported success but the wi_dependencies row is still there (%s)", text)
	}
}

// TestE2EDependencyMutationsStillRequireProjectWriter is the discriminating
// control for the test above.
//
// "The writer succeeded" is only evidence about the model if somebody FAILS.
// Without this case, an endpoint that had lost its authorization entirely — or a
// fixture whose actor was accidentally a global admin — would look exactly like
// a correctly role-gated one. Same two work items, same request, same real
// router; the only thing that changes is the caller's project role.
//
// It is also the mutation guard for the note above handleCreateDependency: strip
// the checkProjectAccess call from either handler and this goes red, while the
// success test stays green.
func TestE2EDependencyMutationsStillRequireProjectWriter(t *testing.T) {
	s := newDepAuthzStack(t)
	ctx := context.Background()

	blocking := s.newWI(t, s.writer, "an upstream item for the viewer refusal control")
	blocked := s.newWI(t, s.writer, "a downstream item for the viewer refusal control")

	// The viewer is refused the create...
	_, err := s.viewer.CreateDependency(ctx, map[string]any{
		"blocked_wi_id":  blocked,
		"blocking_wi_id": blocking,
		"kind":           "blocks",
	})
	if err == nil {
		t.Fatalf("a project VIEWER created a dependency; project role is not gating these endpoints at all, " +
			"which would also make TestE2EDependencyMutationsNeedNoAttemptCredential vacuous")
	}
	// Match the CODE, not the digits: client.do formats errors as
	// "aihub <status> <CODE>: <message>", so a 404 whose message happened to
	// contain "403" would satisfy a substring check on the number.
	if !strings.Contains(err.Error(), "FORBIDDEN") {
		t.Fatalf("viewer create failed with %v, want a FORBIDDEN — a different failure means this control "+
			"is measuring something other than authorization", err)
	}

	// ...and the writer is not, on the very same edge. Anything else and the
	// refusal above is explained by a broken fixture rather than by the role.
	if _, werr := s.writer.CreateDependency(ctx, map[string]any{
		"blocked_wi_id":  blocked,
		"blocking_wi_id": blocking,
		"kind":           "blocks",
	}); werr != nil {
		t.Fatalf("the writer could not create the edge the viewer was refused: %v", werr)
	}

	// The viewer is refused the delete of that now-existing edge. Note this is a
	// real row: a 403 on a nonexistent dependency would be indistinguishable
	// from a 404 handled early.
	if !s.blockedBy(t, blocked, blocking) {
		t.Fatalf("fixture: the writer's create left no row, so the delete below would prove nothing")
	}
	_, err = s.viewer.RemoveDependency(ctx, blocked, blocking, "blocks")
	if err == nil {
		t.Fatalf("a project VIEWER removed a dependency; handleDeleteDependency's writer check is not in force")
	}
	if !strings.Contains(err.Error(), "FORBIDDEN") {
		t.Fatalf("viewer delete failed with %v, want a FORBIDDEN", err)
	}
	if !s.blockedBy(t, blocked, blocking) {
		t.Fatalf("the refused delete removed the row anyway — the check runs too late to protect anything")
	}
}
