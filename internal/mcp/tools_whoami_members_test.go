package mcp_test

// aihub#312: pf_whoami reported the wrong role for every project member who was
// neither admin nor project owner. `client.ListProjects` decodes the response
// into map[string]any (pkg/client/client.go), so proj["members"] arrives as
// []any; the type switch that turned it into members handled only `string` and
// `[]byte`, so neither case matched, no members were ever parsed, and the caller
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
// encoding/json decoding the wire bytes — not asserted by the fixture. A fixture
// that handed the handler a []byte or a string directly would type the value
// itself and could not reproduce the bug.
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

// middlewareProjectRoles mirrors what internal/server/middleware.go:102-134
// puts in /v1/users/me's project_roles for this caller, so a fixture can only
// describe a state the real server can actually produce.
//
// Worth knowing while reading these tests: that middleware already parses the
// same projects.members JSONB and hands pf_whoami the caller's role for free,
// which pf_whoami then re-derives from ListProjects — one response carrying two
// independent derivations of one fact. That is how aihub#312 first showed up in
// the wild (see docs/superpowers/specs/2026-08-06-memory-recall-ranking-design.md
// :137): project_roles said "writer" while projects[] said public/viewer in the
// same payload. Collapsing the two is NOT done here — the middleware skips the
// query entirely for admins, so project_roles is empty for exactly the callers
// the short-circuit branch serves and cannot simply replace the enrichment.
func middlewareProjectRoles(callerID, callerRole string, members []any) map[string]any {
	roles := map[string]any{}
	if callerRole == "admin" {
		return roles
	}
	for _, entry := range members {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if uid, _ := m["user_id"].(string); uid != "" && uid == callerID {
			if r, ok := m["role"].(string); ok {
				roles["aihub"] = r
			}
			break
		}
	}
	return roles
}

// whoamiFake answers the two requests pf_whoami makes: GET /v1/users/me for the
// caller's identity, and GET /v1/projects for the one project it must classify.
//
// The /v1/users/me payload carries all nine fields internal/server/router.go
// :119-131 actually sends, not just the two pf_whoami reads. The byte-identity
// goldens below quote the response verbatim, so a fixture that sent fewer fields
// would leave a regression that dropped a pass-through field invisible to them.
//
// Passing members as a nil slice produces `"members": null` on the wire; passing
// an empty non-nil slice produces `[]`. Both are exercised.
func whoamiFake(t *testing.T, callerID, callerRole, ownerID string, members []any) *fakeAihub {
	t.Helper()
	f := newFakeAihub(t)
	f.on("/v1/users/me", func(map[string]any) (int, any) {
		return 200, map[string]any{
			"user_id":       callerID,
			"email":         "caller@example.com",
			"display_name":  "Caller",
			"user_type":     "human",
			"role":          callerRole,
			"project_roles": middlewareProjectRoles(callerID, callerRole, members),
			// pf_whoami overwrites this key with its enriched
			// [{name, relation, role}] list.
			"projects":       []string{},
			"api_key_id":     "ak_test",
			"server_version": "dev",
		}
	})
	f.on("/v1/projects", func(map[string]any) (int, any) {
		return 200, map[string]any{"items": []any{map[string]any{
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
//
// This duplicates callTool's session setup (tools_fusion_test.go) rather than
// refactoring it, deliberately: callTool decodes, several other suites depend on
// it, and this file has no business reshaping a shared harness.
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
// forward direction. The three cases marked FAILS-PRE-CHANGE are red on
// main@ff5a5cd (all three report public/viewer).
//
// The last three cases are the controls the forward cases need: without a caller
// who is absent from members[], replacing the whole members branch with a
// hard-coded relation="member" / role="writer" would satisfy the forward cases
// and still be wrong. They pass on the pre-change build too — that is the point,
// they are the control, not the regression.
func TestWhoamiMembersBranchClassifiesOrdinaryMembers(t *testing.T) {
	cases := []struct {
		name         string
		members      []any
		wantRelation string
		wantRole     string
	}{
		{
			// FAILS-PRE-CHANGE. The wi's reproduction, verbatim.
			name:         "caller listed as writer",
			members:      []any{map[string]any{"user_id": "u_caller", "role": "writer"}},
			wantRelation: "member",
			wantRole:     "writer",
		},
		{
			// FAILS-PRE-CHANGE. A second role and the caller in second
			// position, so the assertion is that the caller's OWN role is read
			// out of members[] — not that a constant is returned, and not that
			// only members[0] is examined.
			name: "caller listed as maintainer among others",
			members: []any{
				map[string]any{"user_id": "u_other", "role": "writer"},
				map[string]any{"user_id": "u_caller", "role": "maintainer"},
			},
			wantRelation: "member",
			wantRole:     "maintainer",
		},
		{
			// FAILS-PRE-CHANGE, and also fails the re-marshalling version of
			// this fix: json.Unmarshal over the whole list errors on the junk
			// entry, the loop is guarded on that error, and a perfectly good
			// membership is discarded. Walking the []any degrades per entry
			// instead of per project.
			name: "caller listed alongside a non-object junk entry",
			members: []any{
				map[string]any{"user_id": "u_caller", "role": "writer"},
				// A JSON number, deliberately NOT a JSON null: `null` decodes
				// into a nil map without error, so it would sail through the
				// whole-list unmarshal and pin nothing.
				5,
			},
			wantRelation: "member",
			wantRole:     "writer",
		},
		{
			name:         "caller absent from a non-empty members list",
			members:      []any{map[string]any{"user_id": "u_other", "role": "maintainer"}},
			wantRelation: "public",
			wantRole:     "viewer",
		},
		{
			name:         "empty members list",
			members:      []any{},
			wantRelation: "public",
			wantRole:     "viewer",
		},
		{
			// A nil slice marshals to `"members": null`, which the handler
			// screens off with `membersRaw != nil` before the switch.
			// (Distinct from a null ELEMENT, which is a valid empty object.)
			name:         "members is JSON null",
			members:      nil,
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

// TestWhoamiUnidentifiedCallerIsNotMatchedByAnEntryWithoutAUserID guards the
// regression the aihub#312 fix would otherwise have INTRODUCED. Both `uid` and
// `callerID` fall back to "" when the field is absent or not a string, so a
// members entry with no user_id matches a caller whose own user_id did not
// decode, and the handler reports them as a member with that entry's role.
//
// Before the fix this was unreachable: the members loop never ran for an HTTP
// caller at all. Making dead code live makes its latent bugs live too. Note the
// direction — aihub#312 under-reported privilege, this would OVER-report it.
func TestWhoamiUnidentifiedCallerIsNotMatchedByAnEntryWithoutAUserID(t *testing.T) {
	f := newFakeAihub(t)
	// No user_id at all, mirroring result["user_id"].(string) failing.
	f.on("/v1/users/me", func(map[string]any) (int, any) {
		return 200, map[string]any{"role": "user", "user_type": "human"}
	})
	f.on("/v1/projects", func(map[string]any) (int, any) {
		return 200, map[string]any{"items": []any{map[string]any{
			"name":          "aihub",
			"owner_user_id": "u_someone_else",
			"visible":       true,
			// An entry carrying a role but no user_id — the privilege that
			// would be handed to an unidentified caller by a bare == match.
			"members": []any{map[string]any{"role": "maintainer"}},
		}}}
	})

	got := whoamiProject(t, whoamiRaw(t, f))
	if got["relation"] != "public" || got["role"] != "viewer" {
		t.Errorf("an unidentified caller was classified as %v; "+
			"want relation=public role=viewer — a members entry with no user_id "+
			"must not match a caller with no user_id", got)
	}
}

// TestWhoamiAdminAndOwnerResponsesAreByteIdentical is criterion 2 of aihub#312,
// the reverse direction. Admins and the project owner take the short-circuit
// branch ABOVE the members parsing, so the aihub#312 fix must not reach them.
//
// The goldens are the response text captured verbatim on the PRE-change build
// (main @ ff5a5cd) and pinned as literals rather than recomputed from the
// handler, so a change that does reach this branch cannot carry the expectation
// along with it. Be clear about what this can and cannot prove: for THIS diff
// the two builds agree by construction, because these callers never execute a
// changed line. The assertion's job is to keep that true of the next diff.
//
// Both fixtures deliberately list the caller in members[] with a role that is
// NOT "owner". That is what makes them discriminating: if the branches were ever
// reordered so members[] were consulted first, these would come back as
// member/writer and member/viewer instead of owner/owner.
//
// ⚠️ The first golden pins an admin as relation="owner" of a project owned by
// u_someone_else. That is pre-existing pf_whoami semantics (tools_lifecycle.go
// :192-194 treats "admin" as owning everything), and it is pinned here because
// criterion 2 requires byte-identity — not because this file endorses it. If it
// is wrong it is a separate question from aihub#312, and changing it would
// deliberately turn this test red.
func TestWhoamiAdminAndOwnerResponsesAreByteIdentical(t *testing.T) {
	cases := []struct {
		name       string
		callerID   string
		callerRole string
		ownerID    string
		members    []any
		want       string
	}{
		{
			name:       "admin who is not the owner but is listed as a writer",
			callerID:   "u_caller",
			callerRole: "admin",
			ownerID:    "u_someone_else",
			members:    []any{map[string]any{"user_id": "u_caller", "role": "writer"}},
			want:       `{"api_key_id":"ak_test","display_name":"Caller","email":"caller@example.com","project_roles":{},"projects":[{"name":"aihub","relation":"owner","role":"owner"}],"role":"admin","server_version":"dev","user_id":"u_caller","user_type":"human"}`,
		},
		{
			name:       "non-admin project owner who is also listed as a viewer",
			callerID:   "u_caller",
			callerRole: "user",
			ownerID:    "u_caller",
			members:    []any{map[string]any{"user_id": "u_caller", "role": "viewer"}},
			want:       `{"api_key_id":"ak_test","display_name":"Caller","email":"caller@example.com","project_roles":{"aihub":"viewer"},"projects":[{"name":"aihub","relation":"owner","role":"owner"}],"role":"user","server_version":"dev","user_id":"u_caller","user_type":"human"}`,
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
