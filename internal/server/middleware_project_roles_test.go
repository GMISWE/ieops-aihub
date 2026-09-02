package server

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// aihub#315 — the project_roles derivation in BearerAuth carried the same two
// defects aihub#312 fixed on the pf_whoami side:
//
//	(1) one malformed members element discarded the whole project, and
//	(2) the identity comparison had no non-empty guard.
//
// These tests exercise roleForUserInMembers, which is the single copy of that
// derivation that BearerAuth calls — see TestBearerAuth_UsesSharedMembersParser
// for the assertion that keeps it single. A mutation applied to the fix (restoring
// the bail-out, or dropping the callerUserID guard) has to turn one of these red;
// that is the property aihub#309 showed a test at another layer cannot have.

// TestRoleForUserInMembers_MalformedElementKeepsTheRest is defect (1). Every case
// carries a well-formed element on BOTH sides of the malformed one, because
// encoding/json's partial fill is what makes "keep going" correct and a fix that
// only preserved the prefix would still lose data.
func TestRoleForUserInMembers_MalformedElementKeepsTheRest(t *testing.T) {
	cases := []struct {
		name    string
		members string
		wantErr bool
	}{
		{
			name:    "number element",
			members: `[{"user_id":"u_before","role":"viewer"},5,{"user_id":"u_me","role":"maintainer"}]`,
			wantErr: true,
		},
		{
			name:    "string element",
			members: `[{"user_id":"u_before","role":"viewer"},"oops",{"user_id":"u_me","role":"maintainer"}]`,
			wantErr: true,
		},
		{
			name:    "nested array element",
			members: `[{"user_id":"u_before","role":"viewer"},[1,2],{"user_id":"u_me","role":"maintainer"}]`,
			wantErr: true,
		},
		{
			name:    "element whose user_id has the wrong type",
			members: `[{"user_id":"u_before","role":"viewer"},{"user_id":7,"role":"writer"},{"user_id":"u_me","role":"maintainer"}]`,
			wantErr: true,
		},
		{
			// null decodes cleanly into the zero struct, so this one produces no
			// error at all — included so the suite does not silently assume every
			// junk element is loud.
			name:    "null element",
			members: `[{"user_id":"u_before","role":"viewer"},null,{"user_id":"u_me","role":"maintainer"}]`,
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			role, found, err := roleForUserInMembers([]byte(tc.members), "u_me")
			if !found {
				t.Fatalf("membership lost: the malformed element discarded the whole project (members=%s)", tc.members)
			}
			if role != "maintainer" {
				t.Fatalf("role: got %q, want %q (members=%s)", role, "maintainer", tc.members)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("decodeErr: got %v, wantErr=%v", err, tc.wantErr)
			}

			// The element BEFORE the malformed one must survive too.
			role, found, _ = roleForUserInMembers([]byte(tc.members), "u_before")
			if !found || role != "viewer" {
				t.Fatalf("element before the malformed one lost: found=%v role=%q", found, role)
			}
		})
	}
}

// TestRoleForUserInMembers_EmptyCallerNeverMatches is defect (2), and the
// reverse-direction check aihub#312 asked for: fixing the under-report above is
// exactly what makes this over-report reachable, since a malformed element
// decodes to UserID == "". Both arms matter — the second one shows the empty
// string really is present in the decoded slice, so the guard is doing work
// rather than guarding an empty set.
func TestRoleForUserInMembers_EmptyCallerNeverMatches(t *testing.T) {
	members := []byte(`[{"user_id":"u_a","role":"writer"},{"user_id":7,"role":"maintainer"},{"user_id":"u_b","role":"viewer"}]`)

	role, found, _ := roleForUserInMembers(members, "")
	if found {
		t.Fatalf("empty caller id matched a members element and inherited role %q", role)
	}

	// The bad element really does decode to user_id "" carrying a real role, so
	// without the guard the match above would have granted "maintainer".
	var decoded []projectMember
	if err := json.Unmarshal(members, &decoded); err == nil {
		t.Fatalf("fixture is not malformed any more; it must produce an UnmarshalTypeError")
	}
	sawEmptyWithRole := false
	for _, m := range decoded {
		if m.UserID == "" && m.Role != "" {
			sawEmptyWithRole = true
		}
	}
	if !sawEmptyWithRole {
		t.Fatalf("fixture no longer yields an element with user_id=\"\" and a non-empty role; the guard would be vacuous: %#v", decoded)
	}
}

// TestRoleForUserInMembers_NotAnArray pins the case where keeping the partial
// result could plausibly be worse than bailing out: it is not. A whole-value
// type error leaves the slice nil, so no role is granted either way.
func TestRoleForUserInMembers_NotAnArray(t *testing.T) {
	for _, raw := range []string{`{"user_id":"u_me","role":"maintainer"}`, `"u_me"`, `null`, ``} {
		role, found, _ := roleForUserInMembers([]byte(raw), "u_me")
		if found {
			t.Fatalf("members=%q granted role %q; a non-array members value must grant nothing", raw, role)
		}
	}
}

// TestDirtyMemberStillAuthorizes is aihub#315 acceptance 3: the assertion has to
// land on the authorization consequence, not just on the contents of a map.
// checkProjectAccess is the gate every /v1 handler funnels through, and it is the
// same map (UserContext.ProjectRoles) that domain.ListDependencies,
// ListChildren, GetParentRef, PredictConflicts and FnForceTakeover read — those
// take a *pgxpool.Pool and so cannot be asserted without a database, whereas this
// one reproduces the exact user-visible symptom the wi describes: a 403 on a
// project the user really is a member of.
func TestDirtyMemberStillAuthorizes(t *testing.T) {
	members := []byte(`[{"user_id":"u_other","role":"viewer"},5,{"user_id":"u_me","role":"writer"}]`)

	build := func(callerID string) *UserContext {
		uc := &UserContext{UserID: callerID, Role: "writer", ProjectRoles: map[string]string{}}
		if role, found, _ := roleForUserInMembers(members, callerID); found {
			uc.ProjectRoles["aihub"] = role
		}
		return uc
	}

	// Positive: the member behind the malformed element is still authorized.
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/v1/work_items", nil), rec)
	if err := checkProjectAccess(c, build("u_me"), "aihub", "writer"); err != nil {
		t.Fatalf("writer denied on a project whose members array has one malformed element: %v (body=%s)", err, rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — checkProjectAccess wrote a denial", rec.Code)
	}

	// Negative control: the gate still denies someone who is genuinely not a
	// member, so the assertion above is not passing because the gate is open.
	rec2 := httptest.NewRecorder()
	c2 := e.NewContext(httptest.NewRequest(http.MethodPost, "/v1/work_items", nil), rec2)
	if err := checkProjectAccess(c2, build("u_stranger"), "aihub", "writer"); err == nil {
		t.Fatalf("non-member was authorized; the positive case above proves nothing")
	}
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("non-member status: got %d, want 403", rec2.Code)
	}
}

// TestBearerAuth_UsesSharedMembersParser is the wiring hop. aihub#315 exists
// because the members derivation was copied — pf_whoami had one, BearerAuth had
// another, and fixing one left the other broken for 26 days. The unit tests above
// only cover roleForUserInMembers; this asserts BearerAuth actually routes through
// it and has not grown a second, private derivation beside it.
//
// It reads the AST rather than the file's text so it cannot be defeated by
// reformatting, line wrapping, or how the call is spelled.
func TestBearerAuth_UsesSharedMembersParser(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "middleware.go", nil, 0)
	if err != nil {
		t.Fatalf("parse middleware.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == "BearerAuth" && fd.Recv == nil {
			fn = fd
			break
		}
	}
	if fn == nil {
		t.Fatalf("BearerAuth not found in middleware.go")
	}

	sharedCalls, unmarshalCalls := 0, 0
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			if f.Name == "roleForUserInMembers" {
				sharedCalls++
			}
		case *ast.SelectorExpr:
			pkg, ok := f.X.(*ast.Ident)
			if ok && pkg.Name == "json" && (f.Sel.Name == "Unmarshal" || f.Sel.Name == "NewDecoder") {
				unmarshalCalls++
			}
		}
		return true
	})

	if sharedCalls != 1 {
		t.Fatalf("BearerAuth calls roleForUserInMembers %d times, want exactly 1", sharedCalls)
	}
	if unmarshalCalls != 0 {
		t.Fatalf("BearerAuth decodes JSON inline %d time(s); the members derivation must stay in roleForUserInMembers so the next fix cannot miss a copy", unmarshalCalls)
	}
}
