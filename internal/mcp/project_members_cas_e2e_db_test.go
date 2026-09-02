package mcp_test

// aihub#260, the MCP hop and the whole stack behind it: Postgres, the real echo
// router, the real pkg/client, the real MCP handler, and the tool schema a model
// actually sees.
//
// ─── why the acceptance test lives here and not in internal/domain ──────────
//
// internal/domain's TestUpdateProjectMembersCASConcurrentAddsBothSurvive proves
// the guard works when called directly. It cannot prove that a model can REACH
// the guard: members_version has to appear in the advertised schema, survive the
// MCP handler's body construction, survive pkg/client, survive echo's binder,
// and come back out of a read so the next writer has a token. A parameter that
// exists in the schema and is dropped one hop later is this repo's recurring
// defect, and at the call site it is indistinguishable from a precondition that
// passed.
//
// ─── and why it is written with maps and no domain types ────────────────────
//
// So that it COMPILES against the pre-change tree (359a435) and fails there at
// RUNTIME, by actually losing a member, rather than failing to build. A build
// failure is a weaker pre-change signal: it demonstrates that a symbol is new,
// not that the behaviour was broken.
//
//	AIHUB_TEST_DB='postgres://postgres:testpass@localhost:5440/aihub_test?sslmode=disable' \
//	  go test ./internal/mcp/ -run 'TestProjectMembersCAS' -count=1 -v
//
// The DB must already be migrated (goose -dir internal/db/migrations ... up),
// including 0032_projects_members_version.sql.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/auth"
	"github.com/GMISWE/ieops-aihub/internal/mcp"
	"github.com/GMISWE/ieops-aihub/internal/server"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// ─── hop 1: the advertised schema (no database) ─────────────────────────────
//
// Deliberately NOT DB-gated. What a tool advertises is a property of the
// process, so this runs on every `go test ./...` — the schema is the one hop
// whose regression would otherwise be invisible until somebody looked at a
// model transcript.

// projCASToolSession stands up an MCP server whose HTTP peer is never called,
// which is enough to read the tool list.
func projCASToolSession(t *testing.T) *sdkmcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	mcpServer := mcp.New(nil, client.New("http://127.0.0.1:1", "pfk_unused"))
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
	cl := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "proj-cas-schema", Version: "1.0.0"}, nil)
	session, err := cl.Connect(ctx, cTransport, nil)
	if err != nil {
		t.Fatalf("mcp client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestProjectMembersCASToolSchemaAdvertisesMembersVersion(t *testing.T) {
	session := projCASToolSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var update, list *sdkmcp.Tool
	for _, tool := range tools.Tools {
		switch tool.Name {
		case "pf_update_project":
			update = tool
		case "pf_list_projects":
			list = tool
		}
	}
	if update == nil {
		t.Fatal("pf_update_project is not advertised at all")
	}

	raw, err := json.Marshal(update.InputSchema)
	if err != nil {
		t.Fatalf("marshal input schema: %v", err)
	}
	var schema struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode input schema %s: %v", raw, err)
	}
	mv, present := schema.Properties["members_version"]
	if !present {
		t.Fatalf("pf_update_project does not advertise members_version, so no caller can pass the guard. "+
			"Advertised properties: %v", keysOf(schema.Properties))
	}
	if mv.Type != "integer" {
		t.Errorf("members_version is advertised as %q, want \"integer\" — a string would be coerced or "+
			"rejected two hops away as an opaque 400", mv.Type)
	}
	if !strings.Contains(mv.Description, "CONFLICT_CAS_FAILED") {
		t.Errorf("the members_version description does not name the error a caller has to handle: %q", mv.Description)
	}

	// The `members` description has to say it REPLACES, or the guard reads as
	// protection against a mistake it cannot see (see the truncation note).
	members, present := schema.Properties["members"]
	if !present {
		t.Fatal("pf_update_project does not advertise members")
	}
	if !strings.Contains(members.Description, "REPLACES") {
		t.Errorf("the members description does not warn that it replaces the whole list: %q", members.Description)
	}

	// And the guard's INPUT has to be discoverable: pf_list_projects is where a
	// caller reads members_version.
	if list == nil {
		t.Fatal("pf_list_projects is not advertised at all")
	}
	if !strings.Contains(list.Description, "members_version") {
		t.Errorf("pf_list_projects does not mention members_version, so a caller has no documented way to "+
			"obtain the token pf_update_project asks for: %q", list.Description)
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ─── hops 2-5: the real stack ───────────────────────────────────────────────

type projCASStack struct {
	pool    *pgxpool.Pool
	url     string
	project string
	rawKey  string
}

func newProjCASStack(t *testing.T) *projCASStack {
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
		uid    = "u_proj_cas_e2e"
		rawKey = "pfk_proj_cas_e2e_test_key"
	)
	project := "p_proj_cas_e2e"

	keys, err := json.Marshal([]map[string]any{{"id": "k_proj_cas", "key_hash": auth.HashKey(rawKey)}})
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
	// Reset the member list AND the counter. Every version asserted below is
	// absolute, and this project name is shared by each test in this file and
	// reused on every run, so without resetting the counter the second run — and
	// each test after the first within one run — inherits the previous writes.
	// Measured: this suite went red on exactly that before the reset covered
	// members_version.
	//
	// Raw SQL, not the API under test: a fixture must not be built out of the
	// function it exists to catch a bug in.
	if _, err := pool.Exec(ctx,
		`UPDATE projects SET members='[]'::jsonb, members_version=0 WHERE name=$1`, project); err != nil {
		t.Fatalf("reset members: %v", err)
	}

	ts := httptest.NewServer(server.NewRouter(pool, []byte("proj-cas-e2e-cookie-secret")))
	t.Cleanup(ts.Close)

	return &projCASStack{pool: pool, url: ts.URL, project: project, rawKey: rawKey}
}

// session mints an independent MCP session over its own real client, so two
// concurrent writers are genuinely two clients rather than two goroutines
// sharing one.
func (s *projCASStack) session(t *testing.T, label string) *sdkmcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	mcpServer := mcp.New(nil, client.New(s.url, s.rawKey))
	cTransport, sTransport := sdkmcp.NewInMemoryTransports()
	serverCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		srv, err := mcpServer.Connect(serverCtx, sTransport)
		if err != nil {
			return
		}
		_ = srv.Wait()
	}()
	cl := sdkmcp.NewClient(&sdkmcp.Implementation{Name: label, Version: "1.0.0"}, nil)
	session, err := cl.Connect(ctx, cTransport, nil)
	if err != nil {
		t.Fatalf("mcp client connect (%s): %v", label, err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// projCASCall invokes a tool and returns (text, decoded, isError). Every call
// carries a deadline: a hang in this harness would be read as a broken
// environment rather than as a failure.
func projCASCall(t *testing.T, session *sdkmcp.ClientSession, tool string, args map[string]any) (string, map[string]any, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: tool, Arguments: args})
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
	var decoded map[string]any
	_ = json.Unmarshal([]byte(text.Text), &decoded)
	return text.Text, decoded, res.IsError
}

// projCASReadProject returns this project's entry from pf_list_projects.
func projCASReadProject(t *testing.T, session *sdkmcp.ClientSession, project string) map[string]any {
	t.Helper()
	text, decoded, isErr := projCASCall(t, session, "pf_list_projects", map[string]any{})
	if isErr {
		t.Fatalf("pf_list_projects failed: %s", text)
	}
	items, ok := decoded["items"].([]any)
	if !ok {
		t.Fatalf("pf_list_projects returned no items array: %s", text)
	}
	for _, it := range items {
		p, _ := it.(map[string]any)
		if p != nil && p["name"] == project {
			return p
		}
	}
	t.Fatalf("project %q not in pf_list_projects output: %s", project, text)
	return nil
}

// TestProjectMembersCASVersionIsReadableFromListProjects is the assertion that
// makes the guard usable at all — and the one that fails at RUNTIME on the
// pre-change tree rather than failing to compile.
func TestProjectMembersCASVersionIsReadableFromListProjects(t *testing.T) {
	s := newProjCASStack(t)
	session := s.session(t, "proj-cas-read")

	p := projCASReadProject(t, session, s.project)
	v, present := p["members_version"]
	if !present {
		t.Fatalf("pf_list_projects does not report members_version, so no caller can obtain the "+
			"compare-and-set token pf_update_project asks for. Project record: %v", p)
	}
	if _, ok := v.(float64); !ok {
		t.Errorf("members_version came back as %T (%#v), want a number", v, v)
	}
}

// A stale members_version must be refused. On the pre-change tree this call
// succeeds — the unknown JSON field is discarded by the binder and the clobber
// is written — which is exactly the "present in the schema, dropped a hop later"
// shape that makes a silent 200 look like a passed guard.
func TestProjectMembersCASStaleVersionIsRefusedEndToEnd(t *testing.T) {
	s := newProjCASStack(t)
	session := s.session(t, "proj-cas-stale")

	text, _, isErr := projCASCall(t, session, "pf_update_project", map[string]any{
		"name":    s.project,
		"members": []map[string]any{{"user_id": "u_keep", "role": "viewer"}},
	})
	if isErr {
		t.Fatalf("seed write failed: %s", text)
	}
	before := projCASReadProject(t, session, s.project)

	text, _, isErr = projCASCall(t, session, "pf_update_project", map[string]any{
		"name":            s.project,
		"members":         []map[string]any{{"user_id": "u_clobber", "role": "writer"}},
		"members_version": -1, // a version no project has ever had
	})
	if !isErr {
		t.Fatalf("a members_version that matches nothing was accepted end to end; the precondition never "+
			"reached the database. Reply: %s", text)
	}
	if !strings.Contains(text, "CONFLICT_CAS_FAILED") {
		t.Errorf("the failure is not reported as a compare-and-set conflict: %s", text)
	}
	// The current version has to come back, or a caller cannot retry without a
	// second read. Assert the CURRENT value specifically: matching only the
	// string "members_version" would be satisfied by the echo of the caller's
	// own expected value.
	want := fmt.Sprintf(`"current_members_version":%v`, before["members_version"])
	if !strings.Contains(strings.ReplaceAll(text, " ", ""), strings.ReplaceAll(want, " ", "")) {
		t.Errorf("the conflict does not report the current version (%s); got: %s", want, text)
	}

	after := projCASReadProject(t, session, s.project)
	if strings.Contains(fmt.Sprint(after["members"]), "u_clobber") {
		t.Error("the refused write still replaced the member list")
	}
	if after["members_version"] != before["members_version"] {
		t.Errorf("a refused write advanced members_version: %v -> %v", before["members_version"], after["members_version"])
	}
}

// TestProjectMembersCASConcurrentAddsBothSurviveEndToEnd is the work item's
// acceptance criterion, driven the way a real caller drives it: two
// independent MCP clients each read the project, each append one person, each
// write back — overlapping deliberately, so one of them is guaranteed to be
// writing on a version that has already moved.
//
// Compiles against 359a435 (nothing here names a symbol added by this change),
// where members_version is simply absent from the read, the writes go
// unguarded, and one of the two people is silently dropped.
func TestProjectMembersCASConcurrentAddsBothSurviveEndToEnd(t *testing.T) {
	s := newProjCASStack(t)
	setup := s.session(t, "proj-cas-setup")

	text, _, isErr := projCASCall(t, setup, "pf_update_project", map[string]any{
		"name":    s.project,
		"members": []map[string]any{{"user_id": "u_incumbent", "role": "viewer"}},
	})
	if isErr {
		t.Fatalf("seed write failed: %s", text)
	}

	sessions := []*sdkmcp.ClientSession{s.session(t, "admin-a"), s.session(t, "admin-b")}
	people := []string{"u_alice", "u_bob"}

	arrived := make(chan struct{}, len(sessions))
	release := make(chan struct{})
	var (
		mu        sync.Mutex
		conflicts int
		failures  []string
		wg        sync.WaitGroup
	)

	for i := range sessions {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			session, who := sessions[idx], people[idx]
			for attempt := 0; attempt < 6; attempt++ {
				p := projCASReadProject(t, session, s.project)

				var members []any
				if raw, ok := p["members"].([]any); ok {
					members = append(members, raw...)
				}
				members = append(members, map[string]any{"user_id": who, "role": "writer"})

				args := map[string]any{"name": s.project, "members": members}
				// Pass the guard if the server offers one. On a server that does
				// not, this is simply absent and the write is unguarded — which is
				// the pre-change behaviour this test is designed to catch.
				if v, ok := p["members_version"]; ok && v != nil {
					args["members_version"] = v
				}

				if attempt == 0 {
					arrived <- struct{}{}
					select {
					case <-release:
					case <-time.After(30 * time.Second):
					}
				}

				reply, _, isErr := projCASCall(t, session, "pf_update_project", args)
				if !isErr {
					return
				}
				if !strings.Contains(reply, "CONFLICT_CAS_FAILED") {
					mu.Lock()
					failures = append(failures, fmt.Sprintf("%s: %s", who, reply))
					mu.Unlock()
					return
				}
				mu.Lock()
				conflicts++
				mu.Unlock()
			}
			mu.Lock()
			failures = append(failures, who+": gave up after 6 conflicting attempts")
			mu.Unlock()
		}(i)
	}

	for i := 0; i < len(sessions); i++ {
		select {
		case <-arrived:
		case <-time.After(30 * time.Second):
			close(release)
			t.Fatalf("only %d of %d writers reached the barrier within 30s", i, len(sessions))
		}
	}
	close(release)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(90 * time.Second):
		t.Fatal("the concurrent writers did not finish within 90s")
	}

	if len(failures) > 0 {
		t.Fatalf("a writer failed for a reason other than a conflict: %v", failures)
	}

	final := projCASReadProject(t, s.session(t, "proj-cas-verify"), s.project)
	got := map[string]bool{}
	if raw, ok := final["members"].([]any); ok {
		for _, m := range raw {
			if e, ok := m.(map[string]any); ok {
				got[fmt.Sprint(e["user_id"])] = true
			}
		}
	}
	for _, want := range []string{"u_incumbent", "u_alice", "u_bob"} {
		if !got[want] {
			t.Errorf("%s is missing from the final member list %v — a concurrent read-modify-write dropped "+
				"an addition, and afterwards it is indistinguishable from that person never having been added "+
				"(aihub#260)", want, final["members"])
		}
	}
	if conflicts < 1 {
		t.Error("neither writer ever saw CONFLICT_CAS_FAILED, so the two never overlapped and this test " +
			"proved nothing about the guard")
	}
	// One seed write plus exactly two successful adds; a refused attempt must
	// write nothing.
	if final["members_version"] != float64(3) {
		t.Errorf("members_version = %v after 1 seed + 2 successful adds, want 3 — a refused compare-and-set "+
			"still wrote", final["members_version"])
	}
}

// EXPLICITLY OUT OF SCOPE. aihub#260 describes two consequences of `members`
// being a whole-list REPLACE. This change fixes the first (a concurrent
// writer's addition is lost). The second — a caller who reads all N and sends
// back N-1 by mistake — is NOT fixed and cannot be by a compare-and-set: that
// caller's version matches, so the guard passes and the members are gone
// anyway. Pinned here so the work item cannot be read as closed.
func TestProjectMembersCASDoesNotStopSelfInflictedTruncationEndToEnd(t *testing.T) {
	s := newProjCASStack(t)
	session := s.session(t, "proj-cas-truncate")

	text, _, isErr := projCASCall(t, session, "pf_update_project", map[string]any{
		"name": s.project,
		"members": []map[string]any{
			{"user_id": "u_one", "role": "viewer"},
			{"user_id": "u_two", "role": "writer"},
			{"user_id": "u_three", "role": "maintainer"},
		},
	})
	if isErr {
		t.Fatalf("seed write failed: %s", text)
	}
	p := projCASReadProject(t, session, s.project)

	args := map[string]any{
		"name":    s.project,
		"members": []map[string]any{{"user_id": "u_one", "role": "viewer"}},
	}
	if v, ok := p["members_version"]; ok && v != nil {
		args["members_version"] = v
	}
	text, _, isErr = projCASCall(t, session, "pf_update_project", args)
	if isErr {
		t.Fatalf("the guard rejected a write nobody raced: %s", text)
	}

	final := projCASReadProject(t, session, s.project)
	if strings.Contains(fmt.Sprint(final["members"]), "u_two") {
		t.Fatal("u_two survived a truncating write — if that is now prevented, this test is stale and the " +
			"truncation half of aihub#260 has been addressed somewhere; update the work item rather than this test")
	}
	t.Logf("two members were removed with no error and a passing compare-and-set: %v. "+
		"Fixing this needs incremental add_member/remove_member operations or a removal-count "+
		"precondition — a separate API-shape decision, not part of aihub#260.", final["members"])
}
