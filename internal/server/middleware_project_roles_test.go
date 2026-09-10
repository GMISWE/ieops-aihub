package server

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/GMISWE/ieops-aihub/internal/domain"
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
	//
	// 🔴 Expected status changed 403 -> 404 on 2026-09-06 (aihub#377). This is a
	// CONTRACT CHANGE, not a red test being tuned green. The two look identical in
	// a diff, so here is what to check it against — the invariant's first clause,
	// verbatim from the work item:
	//
	//	"在某个 project 里的用户，能看到该 project 的一切（memory、work item、
	//	 artifact、event、step、依赖）；不在的，对该 project 的一切必须拿到与
	//	「不存在」逐字节相同的响应。"
	//
	//	("A user who is in a project can see everything about that project —
	//	 memories, work items, artifacts, events, steps, dependencies. A user who
	//	 is not must get a response byte-identical to the one for something that
	//	 does not exist.")
	//
	// u_stranger is not a member of "aihub", so the response must be the same one
	// a nonexistent project gets. The control keeps all of its discriminating
	// power: were the gate open, u_stranger would be AUTHORIZED and the err == nil
	// check above would fire first.
	rec2 := httptest.NewRecorder()
	c2 := e.NewContext(httptest.NewRequest(http.MethodPost, "/v1/work_items", nil), rec2)
	if err := checkProjectAccess(c2, build("u_stranger"), "aihub", "writer"); err == nil {
		t.Fatalf("non-member was authorized; the positive case above proves nothing")
	}
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("non-member status: got %d, want 404 (aihub#377: a non-member is told "+
			"what someone asking about nothing is told)", rec2.Code)
	}
	if body := rec2.Body.String(); strings.Contains(body, "aihub") {
		t.Errorf("the denial must not name the project; got %s", body)
	}
}

// TestProjectRolesHaveOneDerivation is the wiring hop, and the scope of it is the
// point. aihub#315 exists because the members derivation was COPIED — pf_whoami
// had one, BearerAuth had another, and fixing one left the other broken for 26
// days.
//
// The first version of this gate inspected BearerAuth alone, because BearerAuth
// was the function being fixed. That is the mistake this comment exists to stop
// anyone repeating: a gate scoped to the file you are editing cannot see a defect
// one hop away, and there WAS one. `loadUserByAPIKeyID` in ui_handlers_auth.go
// held a third character-identical copy with both defects intact, serving every
// /ui page load, and it stayed green through the entire BearerAuth fix. It was
// found by grepping for writers of ProjectRoles afterwards.
//
// So the anchor is the authorization map, not a function name: EVERY function in
// this package that writes UserContext.ProjectRoles must obtain the role from
// roleForUserInMembers, and none of them may decode JSON themselves. A fourth
// copy — in a file that does not exist yet — fails this the moment it assigns
// into that map.
//
// It reads the AST rather than the files' text, so reformatting, line wrapping,
// or how the call is spelled cannot defeat it.
func TestProjectRolesHaveOneDerivation(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	fset := token.NewFileSet()
	writers := map[string]string{} // function name -> file
	checked := 0

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if !writesProjectRoles(fd) {
				continue
			}
			writers[fd.Name.Name] = name

			shared, unmarshals := countMembersParsing(fd)
			if shared == 0 {
				t.Errorf("%s (%s) writes ProjectRoles but never calls roleForUserInMembers; that is a second derivation of the caller's role, which is the whole of aihub#315", fd.Name.Name, name)
			}
			if unmarshals != 0 {
				t.Errorf("%s (%s) writes ProjectRoles and decodes JSON inline %d time(s); the members derivation must stay in roleForUserInMembers so the next fix cannot miss a copy", fd.Name.Name, name, unmarshals)
			}
		}
	}

	if checked == 0 {
		t.Fatalf("no non-test .go files were parsed; the walk is broken, not the code")
	}
	// Both known writers must be present. Without this the test passes vacuously
	// the day someone renames the field and the walk silently matches nothing —
	// which is exactly how a gate stops gating without going red.
	for _, want := range []string{"BearerAuth", "loadUserByAPIKeyID"} {
		if _, ok := writers[want]; !ok {
			t.Errorf("%s no longer appears to write ProjectRoles; if that is deliberate remove it from this list, but do not leave the list matching nothing", want)
		}
	}
}

// writesProjectRoles reports whether fd contains an assignment into a
// `.ProjectRoles[...]` map.
func writesProjectRoles(fd *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fd, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			idx, ok := lhs.(*ast.IndexExpr)
			if !ok {
				continue
			}
			if sel, ok := idx.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "ProjectRoles" {
				found = true
			}
		}
		return true
	})
	return found
}

// countMembersParsing returns how many times fd calls roleForUserInMembers and
// how many times it decodes JSON itself.
func countMembersParsing(fd *ast.FuncDecl) (shared, unmarshals int) {
	ast.Inspect(fd, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			if f.Name == "roleForUserInMembers" {
				shared++
			}
		case *ast.SelectorExpr:
			pkg, ok := f.X.(*ast.Ident)
			if ok && pkg.Name == "json" && (f.Sel.Name == "Unmarshal" || f.Sel.Name == "NewDecoder") {
				unmarshals++
			}
		}
		return true
	})
	return shared, unmarshals
}

// TestRoleLevelIsTheDomainLadder is the gate aihub#443 was missing.
//
// This package and internal/domain each used to declare their own role ladder,
// and they disagreed: maintainer scored 3 here and 0 there, and "owner" — which
// is a projects column, never a member role — had a rung there and none here. A
// maintainer therefore passed this package's checks and failed domain's, and
// every comment in router.go, ui_handlers_wi.go and dependencies.go claiming the
// two went "through the same lookup" was wrong while reading as reassurance.
//
// Contents are compared first because that is the property callers depend on,
// and identity second because equal contents are what a re-forked copy looks
// like on the day it is written. Sharing the map value is what keeps the two
// from drifting apart afterwards.
//
// This is a fork detector, not a value oracle: it stays green when both packages
// read the same WRONG ladder, which was measured — a mutation dropping the
// maintainer rung from domain.RoleLevel left this test passing. What the levels
// should be is asserted in internal/domain (TestRoleLevel and
// TestRoleLevel_LadderIsExactlyTheValidatedVocabulary); green here means only
// that there is one ladder, not that it is the right one.
func TestRoleLevelIsTheDomainLadder(t *testing.T) {
	if len(roleLevel) != len(domain.RoleLevel) {
		t.Fatalf("roleLevel has %d entries, domain.RoleLevel has %d: %v vs %v",
			len(roleLevel), len(domain.RoleLevel), roleLevel, domain.RoleLevel)
	}
	for role, level := range domain.RoleLevel {
		if got, ok := roleLevel[role]; !ok || got != level {
			t.Errorf("roleLevel[%q] = %d (present=%t), domain.RoleLevel[%q] = %d",
				role, got, ok, role, level)
		}
	}
	if reflect.ValueOf(roleLevel).Pointer() != reflect.ValueOf(domain.RoleLevel).Pointer() {
		t.Errorf("roleLevel is a separate map that currently happens to agree with " +
			"domain.RoleLevel; it must BE domain.RoleLevel, or the two will drift " +
			"apart again the way they did before aihub#443")
	}
}

// TestRoleForUserInMembers_NonStringRoleIsFoundWithNoRole is the SERVER half of
// the one shape the two derivations of the caller's member role still disagree
// about, published by docs/mcp-cards/pf_whoami.md as "a member whose `role` is
// not a string — and it is a payload difference, not an authorization one".
//
// aihub#543 probe wave 2. The mcp half is
// internal/mcp/whoami_projects_shape_test.go
// (TestWhoamiNonStringMemberRoleKeepsTheDefaultRole): there the membership is
// found and the role stays at the "viewer" default, because the type assert on
// an `any` fails and the default is left standing. Here the value decodes into a
// TYPED struct, so the same entry yields ("", found) and project_roles carries
// {"<project>":""} rather than {} — a different payload for the same row, which
// is the whole of the claim.
//
// The authorization half is asserted too, because "not an authorization one" is
// the load-bearing clause: a reader who took the difference for an access
// difference would treat the two derivations as a live privilege bug. An empty
// role and no membership at all clear exactly the same rungs, which is the
// comparison checkProjectAccess makes.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M9  enforcement: bail out of roleForUserInMembers on a non-nil decodeErr
//	    (the pre-aihub#315 guard)                RED  the membership is lost
//	M10 enforcement: give domain.RoleLevel a rung for "" (`"": 1`)
//	                                            RED  an empty role would then
//	                                                 clear viewer and the
//	                                                 difference WOULD be an
//	                                                 authorization one
//	P1  publication: make every citation in the pf_whoami card unresolvable
//	                                            RED  K12
func TestRoleForUserInMembers_NonStringRoleIsFoundWithNoRole(t *testing.T) {
	const members = `[{"user_id":"u_me","role":5}]`

	role, found, decodeErr := roleForUserInMembers([]byte(members), "u_me")
	if !found {
		t.Fatalf("the membership was lost for %s. The entry names the caller, so it is found "+
			"whatever the role's type — losing it is the pre-aihub#315 wholesale discard, and it "+
			"would make this a role difference rather than the payload difference the card "+
			"publishes", members)
	}
	if role != "" {
		t.Errorf("role = %q, want the empty string: encoding/json leaves a field whose type does not "+
			"match at its zero value, and coercing it into some other role here would invent an "+
			"authorization answer for a row that carries none", role)
	}
	if decodeErr == nil {
		t.Error("decodeErr is nil for a members array with a non-string role — the type error is the " +
			"only signal that this row is dirty, and a silent decode would leave the difference " +
			"between the two derivations unexplainable")
	}

	// The authorization half: an empty role and an absent membership clear the
	// same rungs, so neither shape grants anything the other does not.
	if domain.RoleLevel[""] >= domain.RoleLevel["viewer"] {
		t.Errorf("domain.RoleLevel[\"\"] = %d and RoleLevel[\"viewer\"] = %d, so an empty role now "+
			"clears the viewer rung checkProjectAccess compares against. The pf_whoami card says "+
			"the disagreement between the two derivations is a payload difference and NOT an "+
			"authorization one; with a rung for \"\" that sentence is false and the two derivations "+
			"differ about access.", domain.RoleLevel[""], domain.RoleLevel["viewer"])
	}
	if domain.RoleLevel["viewer"] == 0 {
		t.Fatal("domain.RoleLevel has no viewer rung, so the comparison above is 0 >= 0 and cannot " +
			"fail however the ladder is written — the floor for this arm")
	}
}
