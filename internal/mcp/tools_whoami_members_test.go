package mcp_test

// aihub#312: pf_whoami reported the wrong role for every project member who was
// neither admin nor project owner. `client.ListProjects` decodes the response
// into map[string]any (pkg/client/client.go), so proj["members"] arrives as
// []any; the type switch that turned it into bytes handled only `string` and
// `[]byte`, so neither case matched, membersBytes stayed empty, and the caller
// fell through to relation="public" / role="viewer".
//
// The direction of the defect is UNDER-reporting privilege — a writer shown as a
// viewer — so it was never an escalation. What it cost was members and agents
// concluding they could not do things they were entitled to do, and
// relation="public" reading as "you are not in this project at all".
//
// 🔴 The caller in the forward test must be a non-admin, non-owner ordinary
// member. pf_whoami short-circuits to relation="owner" for admins and for the
// project owner ABOVE the members parsing, so an admin caller exercises that
// short-circuit and asserts nothing about members[] at all. That is exactly how
// this survived: the reporter was an admin, so the one person with the access to
// notice it was the only one who could not see it.
//
// Every fixture here reaches the handler through a real HTTP round trip against
// the fake aihub, so the dynamic type of proj["members"] is produced by
// encoding/json decoding the wire bytes — not asserted by the fixture. A
// fixture that handed the handler a []byte or a string directly would type the
// value itself and could not reproduce the bug.
//
// Run: GOWORK=off go test ./internal/mcp/ -run TestWhoami -v   (no database needed)

import (
	"context"
	"encoding/json"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/mcp"
	"github.com/GMISWE/ieops-aihub/pkg/client"
)

// whoamiFake answers the two requests pf_whoami makes: GET /v1/users/me for the
// caller's identity, and GET /v1/projects for the one project it must classify.
//
// The /v1/users/me payload is kept to the fields pf_whoami actually reads plus
// the two the real handler always sends, because the byte-identity goldens below
// quote the response verbatim and every extra field would be noise in them.
func whoamiFake(t *testing.T, callerID, callerRole, ownerID string, members []map[string]any) *fakeAihub {
	t.Helper()
	f := newFakeAihub(t)
	f.on("/v1/users/me", func(map[string]any) (int, any) {
		return 200, map[string]any{
			"user_id":       callerID,
			"role":          callerRole,
			"user_type":     "human",
			"project_roles": map[string]any{},
			// pf_whoami overwrites this key with its enriched
			// [{name, relation, role}] list.
			"projects": []string{},
		}
	})
	f.on("/v1/projects", func(map[string]any) (int, any) {
		return 200, map[string]any{"items": []map[string]any{{
			"name":          "aihub",
			"owner_user_id": ownerID,
			"visible":       true,
			"members":       members,
		}}}
	})
	return f
}

// whoamiRaw drives the real registered pf_whoami tool over a real MCP session
// and returns the response text VERBATIM. The undecoded text is what the
// byte-identity assertion needs; decoding it into a map would throw away the
// very thing that assertion is about.
func whoamiRaw(t *testing.T, f *fakeAihub) string {
	t.Helper()
	ctx := context.Background()

	server := mcp.New(nil, client.New(f.server.URL, "test-key"))
	cTransport, sTransport := sdkmcp.NewInMemoryTransports()
	serverCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		session, err := server.Connect(serverCtx, sTransport)
		if err != nil {
			return
		}
		_ = session.Wait()
	}()

	cl := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "whoami-test", Version: "1.0.0"}, nil)
	session, err := cl.Connect(ctx, cTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "pf_whoami"})
	if err != nil {
		t.Fatalf("call pf_whoami: %v", err)
	}
	if res.IsError {
		t.Fatalf("pf_whoami returned an error result: %v", res.Content)
	}
	if len(res.Content) == 0 {
		t.Fatalf("pf_whoami returned no content")
	}
	text, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("pf_whoami returned %T, want TextContent", res.Content[0])
	}
	return text.Text
}

// whoamiProject pulls the single projects[] entry out of a pf_whoami response.
func whoamiProject(t *testing.T, raw string) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("decode pf_whoami response: %v (raw: %s)", err, raw)
	}
	list, ok := decoded["projects"].([]any)
	if !ok {
		t.Fatalf("pf_whoami response has no projects list: %s", raw)
	}
	if len(list) != 1 {
		t.Fatalf("pf_whoami returned %d projects, want 1: %s", len(list), raw)
	}
	entry, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("projects[0] is %T, want object: %s", list[0], raw)
	}
	return entry
}

// TestWhoamiMembersBranchClassifiesOrdinaryMembers is criterion 1 of aihub#312,
// forward direction. The first two cases FAIL on the pre-change build (both
// report public/viewer).
//
// The last two cases are the pair the forward cases need: without a caller who
// is absent from members[], replacing the whole members branch with a
// hard-coded relation="member" / role="writer" would satisfy the forward cases
// and still be wrong. They pass on the pre-change build too — that is the point,
// they are the control, not the regression.
func TestWhoamiMembersBranchClassifiesOrdinaryMembers(t *testing.T) {
	cases := []struct {
		name         string
		members      []map[string]any
		wantRelation string
		wantRole     string
	}{
		{
			name:         "caller listed as writer",
			members:      []map[string]any{{"user_id": "u_caller", "role": "writer"}},
			wantRelation: "member",
			wantRole:     "writer",
		},
		{
			// A second role, so the assertion is that the caller's OWN role is
			// read out of members[] rather than that some constant is returned.
			name: "caller listed as maintainer among others",
			members: []map[string]any{
				{"user_id": "u_other", "role": "writer"},
				{"user_id": "u_caller", "role": "maintainer"},
			},
			wantRelation: "member",
			wantRole:     "maintainer",
		},
		{
			name:         "caller absent from a non-empty members list",
			members:      []map[string]any{{"user_id": "u_other", "role": "maintainer"}},
			wantRelation: "public",
			wantRole:     "viewer",
		},
		{
			name:         "empty members list",
			members:      []map[string]any{},
			wantRelation: "public",
			wantRole:     "viewer",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// role="user" and owner_user_id="u_someone_else": both
			// short-circuit conditions above the members branch are false, so
			// the members branch is the code actually under test.
			f := whoamiFake(t, "u_caller", "user", "u_someone_else", tc.members)
			got := whoamiProject(t, whoamiRaw(t, f))

			if got["relation"] != tc.wantRelation || got["role"] != tc.wantRole {
				t.Errorf("pf_whoami project entry = %v, want relation=%q role=%q",
					got, tc.wantRelation, tc.wantRole)
			}
		})
	}
}

// TestWhoamiAdminAndOwnerResponsesAreByteIdentical is criterion 2 of aihub#312,
// the reverse direction. Admins and the project owner take the short-circuit
// branch ABOVE the members parsing, so the aihub#312 fix must not reach them.
//
// The goldens are the response text captured verbatim on the PRE-change build
// (main @ ff5a5cd) and pinned as literals rather than recomputed from the
// handler, so a change that does reach this branch cannot carry the expectation
// along with it.
//
// Both fixtures deliberately list the caller in members[] with a role that is
// NOT "owner". That is what makes the assertion discriminating: if the fix ever
// reordered the branches so that members[] were consulted first, these would
// come back as member/writer and member/viewer instead of owner/owner.
func TestWhoamiAdminAndOwnerResponsesAreByteIdentical(t *testing.T) {
	cases := []struct {
		name       string
		callerID   string
		callerRole string
		ownerID    string
		members    []map[string]any
		want       string
	}{
		{
			name:       "admin who is not the owner but is listed as a writer",
			callerID:   "u_caller",
			callerRole: "admin",
			ownerID:    "u_someone_else",
			members:    []map[string]any{{"user_id": "u_caller", "role": "writer"}},
			want:       `{"project_roles":{},"projects":[{"name":"aihub","relation":"owner","role":"owner"}],"role":"admin","user_id":"u_caller","user_type":"human"}`,
		},
		{
			name:       "non-admin project owner who is also listed as a viewer",
			callerID:   "u_caller",
			callerRole: "user",
			ownerID:    "u_caller",
			members:    []map[string]any{{"user_id": "u_caller", "role": "viewer"}},
			want:       `{"project_roles":{},"projects":[{"name":"aihub","relation":"owner","role":"owner"}],"role":"user","user_id":"u_caller","user_type":"human"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := whoamiFake(t, tc.callerID, tc.callerRole, tc.ownerID, tc.members)
			got := whoamiRaw(t, f)
			if got != tc.want {
				t.Errorf("pf_whoami response text changed for a short-circuit caller\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}
