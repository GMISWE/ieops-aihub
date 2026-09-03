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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/auth"
	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
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

	// Identifiers are derived from t.Name(), the same way testUser/testProject
	// in internal/domain and newMembersVersionStack in internal/server do it:
	// fixed names would make two tests in this file — and any two runs sharing
	// one database — write to the same project row, and every version asserted
	// below is absolute.
	//
	// The API key is derived too, not a shared constant. The auth middleware
	// resolves a key by scanning users for a matching key_hash and takes the
	// FIRST row (internal/server/middleware.go), so N users holding one key hash
	// would authenticate as an arbitrary one of them.
	uid := "u_" + testname.Sanitize(t.Name())
	project := "p_" + testname.Sanitize(t.Name())
	rawKey := "pfk_" + testname.Sanitize(t.Name())

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
	// absolute, and the name above is derived from t.Name(), so it is the same
	// string on every run against the same database — the isolation it buys is
	// between tests, never between runs. Without this reset the second run
	// inherits the first run's writes. Measured: this suite went red on exactly
	// that (when the project name was shared by every test here too) before the
	// reset covered members_version.
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

// ─── aihub#333: the truncation half, end to end ─────────────────────────────
//
// This section replaces TestProjectMembersCASDoesNotStopSelfInflictedTruncation-
// EndToEnd, aihub#260's characterization of the gap it deliberately left open (a
// caller who reads all N and sends back N-1 wipes the rest, because their
// version matches perfectly so the compare-and-set passes). That test's own
// failure messages said that going red most likely meant aihub#333 had shipped
// and the test was the stale thing. It has, so the test is gone rather than
// inverted in place: its name asserted the opposite of the behaviour, and the
// manifest line it lost (internal/citest/dbtestcov/gated_tests.txt) is the
// reviewable record that the characterization was given up on purpose.

// projUpdateProps returns pf_update_project's advertised property descriptions.
func projUpdateProps(t *testing.T) map[string]struct {
	Type        string `json:"type"`
	Description string `json:"description"`
} {
	t.Helper()
	session := projCASToolSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != "pf_update_project" {
			continue
		}
		raw, merr := json.Marshal(tool.InputSchema)
		if merr != nil {
			t.Fatalf("marshal input schema: %v", merr)
		}
		var schema struct {
			Properties map[string]struct {
				Type        string `json:"type"`
				Description string `json:"description"`
			} `json:"properties"`
		}
		if uerr := json.Unmarshal(raw, &schema); uerr != nil {
			t.Fatalf("decode input schema %s: %v", raw, uerr)
		}
		return schema.Properties
	}
	t.Fatal("pf_update_project is not advertised at all")
	return nil
}

// Not DB-gated: what a tool advertises is a property of the process, and it is
// the one hop whose regression stays invisible until somebody reads a model
// transcript. This is aihub#333's third acceptance criterion — the description
// no longer needs aihub#260's closing NOTE ("it does NOT protect against sending
// a short list yourself") — expressed as the two things that have to be true for
// that NOTE to be unnecessary rather than merely deleted.
func TestProjectMembersRemovalToolSchemaAdvertisesExpectedRemovals(t *testing.T) {
	props := projUpdateProps(t)

	er, present := props["expected_removals"]
	if !present {
		t.Fatalf("pf_update_project does not advertise expected_removals, so no caller can authorise a "+
			"removal and every legitimate one is a 412 nobody can clear. Advertised: %v", keysOf(props))
	}
	if er.Type != "array" {
		t.Errorf("expected_removals is advertised as %q, want \"array\" — it binds to a []string", er.Type)
	}
	if !strings.Contains(er.Description, "PROJECT_MEMBERS_UNDECLARED_REMOVAL") {
		t.Errorf("the expected_removals description does not name the error a caller has to handle: %q",
			er.Description)
	}

	// The caller who needs this parameter is reading `members`, not looking for
	// it — they do not know they are about to remove anybody. So `members` is
	// where it has to be mentioned.
	members, present := props["members"]
	if !present {
		t.Fatal("pf_update_project does not advertise members")
	}
	if !strings.Contains(members.Description, "expected_removals") {
		t.Errorf("the members description does not point at expected_removals, so the guard is only "+
			"discoverable by somebody who already knows it exists: %q", members.Description)
	}

	// The negative half, as a SHAPE and not as the 100-character sentence
	// aihub#260 ended with: that literal is one spelling of an unbounded class,
	// and a paraphrase would reintroduce it exactly. What must not be there is
	// any claim that the server cannot protect the caller from their own short
	// list — it now can, and a stale warning is worse than none, because a caller
	// who believes it stops looking for the parameter that does protect them.
	stale := regexp.MustCompile(`(?i)\b(does ?n[o']?t|cannot|can ?not|no) (protect|protection)`)
	for name, p := range props {
		if m := stale.FindString(p.Description); m != "" {
			t.Errorf("pf_update_project's %q description still tells the caller the server %q them against "+
				"their own list. aihub#333 closed that gap: an undeclared removal is refused with 412 "+
				"PROJECT_MEMBERS_UNDECLARED_REMOVAL. Description: %q", name, m, p.Description)
		}
	}
}

// Also NOT DB-gated, and deliberately so. The two end-to-end cases below cover
// this hop as well, but they SKIP without AIHUB_TEST_DB — which means on the
// `go test ./...` that CI's "Unit tests" step runs, nothing at all would assert
// that expected_removals survives the MCP handler's body construction and
// pkg/client. An httptest peer needs no database, so this one runs everywhere.
//
// It is the SUCCESS half that can see this hop: a request whose declaration was
// dropped en route is refused, and a refusal is what the undeclared-removal test
// expects anyway. So a dropped parameter is invisible to every "it was refused"
// assertion in this change, which is exactly why the bytes get read here.
func TestProjectMembersRemovalToolForwardsExpectedRemovalsOnTheWire(t *testing.T) {
	// Both argument shapes a caller can plausibly send. The scalar case is the
	// natural mistake when removing exactly one person, and it is also what a
	// client that coerces to the declared type can produce. Before aihub#333
	// added normalizeStringSliceArg it reached echo's c.Bind as a bare string and
	// died there as 400 BAD_REQUEST "invalid request body" — a message that names
	// nothing and is indistinguishable from the server not knowing the parameter,
	// which is exactly the aihub#241 B1 defect one type over. This test's own
	// comment used to describe that hazard while asserting only the happy path.
	cases := []struct {
		name string
		arg  any
		want []any
	}{
		{name: "Array", arg: []string{"u_two", "u_three"}, want: []any{"u_two", "u_three"}},
		{name: "BareStringIsCoercedNotRejected", arg: "u_two", want: []any{"u_two"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody []byte
			var gotMethod, gotPath string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"name":"p","members":[],"members_version":7}`))
			}))
			defer ts.Close()

			ctx := context.Background()
			mcpServer := mcp.New(nil, client.New(ts.URL, "pfk_wire_probe"))
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
			cl := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "proj-removal-wire", Version: "1.0.0"}, nil)
			session, err := cl.Connect(ctx, cTransport, nil)
			if err != nil {
				t.Fatalf("mcp client connect: %v", err)
			}
			t.Cleanup(func() { _ = session.Close() })

			text, _, isErr := projCASCall(t, session, "pf_update_project", map[string]any{
				"name":              "p",
				"members":           []map[string]any{{"user_id": "u_one", "role": "viewer"}},
				"members_version":   3,
				"expected_removals": tc.arg,
			})
			if isErr {
				t.Fatalf("pf_update_project failed against the capture peer: %s", text)
			}
			if gotMethod != http.MethodPatch || gotPath != "/v1/projects/p" {
				t.Errorf("request was %s %s, want PATCH /v1/projects/p", gotMethod, gotPath)
			}

			// Decode rather than substring-match: `"expected_removals":["u_two"…]`
			// as text would also be satisfied by the value arriving as a single
			// joined string, which the server's []string rejects two hops away.
			var sent map[string]any
			if uerr := json.Unmarshal(gotBody, &sent); uerr != nil {
				t.Fatalf("the server saw a non-JSON body %q: %v", gotBody, uerr)
			}
			raw, present := sent["expected_removals"]
			if !present {
				t.Fatalf("expected_removals never reached the wire, so every declared removal would be "+
					"refused as undeclared. The server saw %s", gotBody)
			}
			list, ok := raw.([]any)
			if !ok {
				t.Fatalf("expected_removals arrived as %T (%#v), not a JSON array — the server binds it "+
					"into a []string and would answer an opaque 400", raw, raw)
			}
			if !reflect.DeepEqual(list, tc.want) {
				t.Errorf("expected_removals = %#v on the wire, want %#v", list, tc.want)
			}
			// The other two members fields must still be there: a handler that
			// started filtering its arguments would drop all three together.
			for _, k := range []string{"members", "members_version"} {
				if _, ok := sent[k]; !ok {
					t.Errorf("%s did not reach the wire alongside expected_removals; the server saw %s",
						k, gotBody)
				}
			}
		})
	}
}

// Half one over the whole stack: the truncating call the old characterization
// asserted would SUCCEED must now be refused, with the names it refused to
// remove, and nothing may be written.
func TestProjectMembersRemovalUndeclaredTruncationIsRefusedEndToEnd(t *testing.T) {
	s := newProjCASStack(t)
	session := s.session(t, "proj-removal-undeclared")

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

	// The caller holds the CURRENT version, so aihub#260's guard passes. That is
	// the point: this refusal cannot come from the compare-and-set.
	args := map[string]any{
		"name":    s.project,
		"members": []map[string]any{{"user_id": "u_one", "role": "viewer"}},
	}
	if v, ok := p["members_version"]; ok && v != nil {
		args["members_version"] = v
	}
	text, _, isErr = projCASCall(t, session, "pf_update_project", args)
	if !isErr {
		t.Fatalf("a list short by two was accepted end to end with a matching members_version; two people "+
			"lost access and the caller was told nothing. Reply: %s", text)
	}
	if !strings.Contains(text, "PROJECT_MEMBERS_UNDECLARED_REMOVAL") {
		t.Errorf("the refusal is not reported as an undeclared removal: %s", text)
	}
	for _, who := range []string{"u_two", "u_three"} {
		if !strings.Contains(text, who) {
			t.Errorf("the refusal does not name %s, so the caller cannot see who it was about to remove: %s",
				who, text)
		}
	}

	final := projCASReadProject(t, session, s.project)
	for _, who := range []string{"u_one", "u_two", "u_three"} {
		if !strings.Contains(fmt.Sprint(final["members"]), who) {
			t.Errorf("%s is gone although the write was refused: %v", who, final["members"])
		}
	}
	if final["members_version"] != p["members_version"] {
		t.Errorf("members_version moved from %v to %v on a refused write, so the refusal happened after the "+
			"UPDATE", p["members_version"], final["members_version"])
	}
}

// Half two over the whole stack, and the assertion that expected_removals is not
// dropped between the MCP handler's body construction, pkg/client and echo's
// binder. Without it, a server that refused every members write would satisfy
// the test above — which would replace silent data loss with an outage.
func TestProjectMembersRemovalDeclaredRemovalSucceedsEndToEnd(t *testing.T) {
	s := newProjCASStack(t)
	session := s.session(t, "proj-removal-declared")

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
		"name":              s.project,
		"members":           []map[string]any{{"user_id": "u_one", "role": "viewer"}},
		"expected_removals": []string{"u_two", "u_three"},
	}
	if v, ok := p["members_version"]; ok && v != nil {
		args["members_version"] = v
	}
	text, _, isErr = projCASCall(t, session, "pf_update_project", args)
	if isErr {
		t.Fatalf("a removal that declared exactly the two user_ids it removes was refused end to end. "+
			"Either expected_removals is dropped on one of the hops between the tool and the domain, or "+
			"removing a member is now impossible. Reply: %s", text)
	}

	final := projCASReadProject(t, session, s.project)
	got := fmt.Sprint(final["members"])
	if !strings.Contains(got, "u_one") {
		t.Errorf("u_one was not kept: %v", final["members"])
	}
	for _, who := range []string{"u_two", "u_three"} {
		if strings.Contains(got, who) {
			t.Errorf("%s survived a DECLARED removal, so the write did not take effect: %v", who, final["members"])
		}
	}
}
