package domain

// aihub#543 probe wave 2 — the `docs/mcp-cards/pf_rotate_identifier.md` hop-4
// bullets about who may call this and what stays behind afterwards.
//
//	"- Authorization is owner or admin, checked server-side; a `maintainer` member
//	 cannot call it — worth stating rather than assuming, because `owner` is not a
//	 rung on the member ladder at all: `internal/domain/projects.go`
//	 (`checkProjectAccess`) settles ownership before any member role is ranked."
//	"- `identifier_prefix` is the non-secret half that stays in the project row and
//	 is what `pf_list_projects` shows."
//	"- **§6.2 T2-8** — who may call this is a role question, and the legal member
//	 roles are `viewer | writer | maintainer`; `owner` is a column."
//
// 🔴 WHY ORDER IS THE CLAIM AND NOT AN IMPLEMENTATION DETAIL. "owner is not a
// rung on the member ladder" is checkable by reading RoleLevel, and that alone
// would be a weak arm: a ladder without an "owner" key is also what you get from
// a ladder that ranks owner under a different spelling. What the card actually
// says is a claim about SEQUENCE — the owner is settled and returned before any
// member role is looked up — and that is what makes "a maintainer cannot call
// it" follow rather than be a coincidence. RoleLevel's own comment records the
// version of this repo where the two disagreed (before aihub#443 a second ladder
// scored maintainer 0 and gave rung 3 to "owner"), so this is a shape that has
// been wrong here before.
//
// The identifier bullet is the mirror: the row keeps a prefix and a hash, and the
// serialised project must carry the first and never the second. That is a
// property of a Go struct's tags, checked by marshalling one rather than by
// reading them, because a field added without a `json:"-"` leaks by default.
//
// ⚠️ SCOPE. "checked server-side" is held here only in the sense that the check
// lives in this package, which is the server. The HTTP-level refusal a non-owner
// receives is a different instrument and is not claimed by this file.
//
// No database — every question here is about a declaration:
//
//	GOWORK=off go test ./internal/domain/ -run 'TestProjectOwnership|TestSerialisedProject' -count=1

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// rotateCardPath is the card whose bullets this file holds.
var rotateCardPath = filepath.Join("..", "..", "docs", "mcp-cards", "pf_rotate_identifier.md")

// rotateCardRoleVocabulary reads the member-role vocabulary out of the card's
// Policy bullet, in the `viewer | writer | maintainer` form it is written in.
//
// Read rather than written down for the base-strength reason, and with a second
// edge here: the vocabulary is the thing most likely to grow, and a copy in this
// file would keep asserting the old set while the card advertised the new one.
func rotateCardRoleVocabulary(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(rotateCardPath)
	if err != nil {
		t.Fatalf("read %s: %v — the card is one side of every comparison here", rotateCardPath, err)
	}
	flat := strings.Join(strings.Fields(string(raw)), " ")
	m := regexp.MustCompile("the legal member roles are `([a-z |]+)`").FindStringSubmatch(flat)
	if m == nil {
		t.Fatal("the card no longer states \"the legal member roles are `…`\". That list is the " +
			"population this arm compares RoleLevel against; without it the comparison is " +
			"between the map and nothing.")
	}
	var out []string
	for _, r := range strings.Split(m[1], "|") {
		if v := strings.TrimSpace(r); v != "" {
			out = append(out, v)
		}
	}
	if len(out) < 2 {
		t.Fatalf("the card names %d member role(s) (%v) — too few to be a ladder, so the "+
			"extraction is broken rather than the map", len(out), out)
	}
	return out
}

// projectsAST parses this package's projects.go once for the structural questions
// below. A parse failure is a FAILURE: an arm that cannot read the file cannot
// tell "the ordering is right" from "there was nothing to look at".
func projectsAST(t *testing.T) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, projectsSourcePath, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", projectsSourcePath, err)
	}
	return fset, f
}

// projectFuncDecl finds one top-level function in a parsed file.
func projectFuncDecl(t *testing.T, f *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range f.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if ok && d.Recv == nil && d.Name.Name == name {
			return d
		}
	}
	t.Fatalf("%s declares no top-level func %s", projectsSourcePath, name)
	return nil
}

// projectExprText renders an expression back to source, which is how the conditions
// below are matched without hard-coding an AST shape that a harmless rewrite
// would change.
func projectExprText(fset *token.FileSet, src []byte, n ast.Node) string {
	base := fset.File(n.Pos()).Base()
	return string(src[int(n.Pos())-base : int(n.End())-base])
}

// TestProjectOwnershipIsSettledBeforeAnyMemberRoleIsRanked is the authorization
// half of the card's hop-4 bullet and its Policy bullet, encoded.
//
// MUTANTS (applied to this tree; the verdict is what RAN):
//
//	M1 enforcement: add `"owner": 4` to RoleLevel
//	                                             RED  owner_is_not_a_rung
//	M2 enforcement: delete the level-2 owner return, leaving the comparison in place
//	                                             RED  ownership_is_settled_first
//	M3 enforcement: delete the `if minRole == "owner" { break }` guard inside the
//	   member loop — the edit that lets a maintainer satisfy an owner-level call
//	                                             RED  an_owner_level_call_leaves_the_member_loop
//	M4 enforcement: pass "writer" instead of "owner" from RotateIdentifier
//	                                             RED  rotation_asks_for_owner_level_access
//	M5 enforcement: drop `maintainer` from RoleLevel
//	                                             RED  the_ladder_is_exactly_the_cards_vocabulary
//	M6 publication: reword the card's Policy bullet to list `owner` among the legal
//	   member roles                              RED  the_ladder_is_exactly_the_cards_vocabulary
//	G1 control:     rename an unrelated local inside checkProjectAccess
//	                                           GREEN  the arm matches on the owner
//	                                                  comparison and the RoleLevel
//	                                                  lookup, not on the function's
//	                                                  other text
func TestProjectOwnershipIsSettledBeforeAnyMemberRoleIsRanked(t *testing.T) {
	src, err := os.ReadFile(projectsSourcePath)
	if err != nil {
		t.Fatalf("read %s: %v", projectsSourcePath, err)
	}
	fset, file := projectsAST(t)
	check := projectFuncDecl(t, file, "checkProjectAccess")

	var ownerReturnEnd, ownerBreakEnd, firstRoleLevel token.Pos
	ast.Inspect(check, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.IfStmt:
			cond := projectExprText(fset, src, node.Cond)
			hasReturn := false
			hasBreak := false
			ast.Inspect(node.Body, func(inner ast.Node) bool {
				switch s := inner.(type) {
				case *ast.ReturnStmt:
					hasReturn = true
				case *ast.BranchStmt:
					if s.Tok == token.BREAK {
						hasBreak = true
					}
				}
				return true
			})
			if strings.Contains(cond, "OwnerUserID") && strings.Contains(cond, "caller.ID") &&
				hasReturn && !ownerReturnEnd.IsValid() {
				ownerReturnEnd = node.End()
			}
			if strings.Contains(cond, `minRole == "owner"`) && hasBreak && !ownerBreakEnd.IsValid() {
				ownerBreakEnd = node.End()
			}
		case *ast.IndexExpr:
			if id, ok := node.X.(*ast.Ident); ok && id.Name == "RoleLevel" && !firstRoleLevel.IsValid() {
				firstRoleLevel = node.Pos()
			}
		}
		return true
	})

	if !firstRoleLevel.IsValid() {
		t.Fatal("checkProjectAccess indexes RoleLevel nowhere. The card's claim is that " +
			"ownership is settled BEFORE any member role is ranked; with no ranking in the " +
			"function there is nothing for \"before\" to be before, and every ordering " +
			"assertion below would pass by having no second term.")
	}

	t.Run("ownership_is_settled_first", func(t *testing.T) {
		if !ownerReturnEnd.IsValid() {
			t.Fatal("checkProjectAccess has no `if <owner> == caller.ID { return … }`. That " +
				"branch is level 2 of the chain the function's own doc comment describes, and " +
				"it is what the card means by \"settles ownership\".")
		}
		if ownerReturnEnd >= firstRoleLevel {
			t.Errorf("the owner branch ends at offset %d and the first RoleLevel lookup is at "+
				"%d, so a member role is ranked before ownership is settled. The card tells a "+
				"reader that owner is not on the ladder at all; if the ladder is consulted "+
				"first, an owner who is ALSO a member is answered by their member rung — which "+
				"is the aihub#443 shape, where a maintainer of a public project was answered "+
				"with less than an anonymous caller would have been.",
				int(ownerReturnEnd), int(firstRoleLevel))
		}
	})

	t.Run("an_owner_level_call_leaves_the_member_loop", func(t *testing.T) {
		if !ownerBreakEnd.IsValid() {
			t.Fatal("checkProjectAccess has no `if minRole == \"owner\" { break }` inside the " +
				"member walk. Without it a member row is ranked against a minRole that is not on " +
				"the ladder — RoleLevel[\"owner\"] is the zero value, so EVERY member would " +
				"outrank it and the card's \"a maintainer member cannot call it\" would be false.")
		}
		if ownerBreakEnd >= firstRoleLevel {
			t.Errorf("the owner-level escape ends at offset %d and the RoleLevel comparison is "+
				"at %d. The escape has to come first: past it, an unranked minRole compares as 0.",
				int(ownerBreakEnd), int(firstRoleLevel))
		}
	})

	t.Run("owner_is_not_a_rung", func(t *testing.T) {
		if lvl, present := RoleLevel["owner"]; present {
			t.Errorf("RoleLevel ranks \"owner\" at %d. The card says owner is a COLUMN and not a "+
				"member role; a rung for it means a members entry could carry it, and "+
				"UpdateProject would then be able to grant ownership through the member list.", lvl)
		}
	})

	t.Run("the_ladder_is_exactly_the_cards_vocabulary", func(t *testing.T) {
		want := rotateCardRoleVocabulary(t)
		if len(RoleLevel) != len(want) {
			t.Errorf("RoleLevel holds %d role(s) and the card names %d (%v). Both directions "+
				"matter: an extra rung is a role callers were never told about, and a missing "+
				"one is a role the card promises and the ladder scores 0 — which reads as "+
				"non-membership.", len(RoleLevel), len(want), want)
		}
		for _, role := range want {
			if _, present := RoleLevel[role]; !present {
				t.Errorf("the card names %q as a legal member role and RoleLevel does not rank "+
					"it, so a member carrying it scores 0 and is treated as no member at all", role)
			}
		}
	})

	t.Run("rotation_asks_for_owner_level_access", func(t *testing.T) {
		rotate := projectFuncDecl(t, file, "RotateIdentifier")
		found := false
		ast.Inspect(rotate, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "checkProjectAccess" {
				return true
			}
			for _, arg := range call.Args {
				if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING && lit.Value == `"owner"` {
					found = true
				}
			}
			return true
		})
		if !found {
			t.Error("RotateIdentifier does not call checkProjectAccess with minRole \"owner\". " +
				"The card's first hop-4 bullet is that authorization here is owner-or-admin; a " +
				"lower minRole would let the member ladder answer, and every rung on it is a " +
				"member the card says cannot call this.")
		}
	})
}

// TestSerialisedProjectCarriesTheIdentifierPrefixAndNeverTheHash is the second
// hop-4 bullet: the prefix is the half that stays in the row and is shown.
//
// MUTANTS (applied to this tree; the verdict is what RAN):
//
//	M7 enforcement: add `IdentifierHash *string` with a json tag to the Project
//	   struct                                    RED  the_hash_has_no_serialisable_field
//	   🔴 GREEN on this arm's FIRST version, which only marshalled an instance: a
//	   nil pointer with omitempty emits nothing, so the field could be declared and
//	   the subtest named for refusing it said nothing. The declaration walk was
//	   added because of this run, and the marshal check kept for the untagged-field
//	   case it still covers.
//	M8 enforcement: drop IdentifierPrefix from the Project struct
//	                                             RED  the_prefix_is_serialised
//	M9 enforcement: remove identifier_prefix from projectSelectCols
//	                                             RED  every_read_selects_the_prefix
//	M10 enforcement: add identifier_hash to projectSelectCols
//	                                             RED  every_read_selects_the_prefix
//	M11 publication: reword the card's bullet to name `identifier_hash`
//	                                             RED  the_card_names_the_prefix
//	G2 control:     add an unrelated field to Project
//	                                           GREEN  the arm asserts the prefix is
//	                                                  present and the hash is absent,
//	                                                  not that the struct is frozen
func TestSerialisedProjectCarriesTheIdentifierPrefixAndNeverTheHash(t *testing.T) {
	raw, err := os.ReadFile(rotateCardPath)
	if err != nil {
		t.Fatalf("read %s: %v", rotateCardPath, err)
	}
	flat := strings.Join(strings.Fields(string(raw)), " ")

	t.Run("the_card_names_the_prefix", func(t *testing.T) {
		if !strings.Contains(flat, "`identifier_prefix` is the non-secret half") {
			t.Error("the card no longer says \"`identifier_prefix` is the non-secret half\". " +
				"This arm's subject comes from that bullet; with the bullet gone it is checking " +
				"a claim nobody makes, and it should go with it.")
		}
	})

	prefix := "pi_0123abcd"
	p := Project{
		Name: "probe", Visible: true, IdentifierPrefix: &prefix,
		Repos: json.RawMessage("[]"), Members: json.RawMessage("[]"),
		OwnerUserID: "u_probe",
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal a Project: %v — the serialised shape is the claim", err)
	}
	body := string(encoded)

	t.Run("the_prefix_is_serialised", func(t *testing.T) {
		if !strings.Contains(body, `"identifier_prefix":"`+prefix+`"`) {
			t.Errorf("a serialised Project does not carry identifier_prefix: %s\nThe card says "+
				"this is what pf_list_projects SHOWS, and a caller who cannot see the prefix has "+
				"no way to tell which identifier a project is currently on.", body)
		}
	})

	t.Run("the_hash_has_no_serialisable_field", func(t *testing.T) {
		// 🔴 TWO CHECKS, AND THE SECOND ONE IS THE ONE THAT WORKS. Marshalling an
		// instance was the whole subtest until mutant M7 was run against it: adding
		// `IdentifierHash *string `json:"identifier_hash,omitempty"`` to Project came
		// back GREEN, because a nil pointer with omitempty emits nothing. The field
		// was there, every read that populated it would have serialised the bcrypt
		// hash, and the arm named for refusing exactly that said nothing. So the
		// declaration is walked as well as the output — the marshal check stays,
		// because it is the half that catches an untagged exported field.
		typ := reflect.TypeOf(Project{})
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")[0]
			if strings.Contains(strings.ToLower(f.Name), "hash") ||
				strings.Contains(strings.ToLower(tag), "hash") {
				t.Errorf("Project declares a field %s with json tag %q. The bcrypt hash is the "+
					"secret half and lives on the UNEXPORTED field of projectWithHash precisely so "+
					"that no marshal of a project can reach it. A declared field here serialises "+
					"the hash into every list and get response the moment something populates it "+
					"— and if it is a pointer with omitempty, the day that happens is the first "+
					"day anybody notices.", f.Name, f.Tag.Get("json"))
			}
		}
		if typ.NumField() < 5 {
			t.Fatalf("Project has %d field(s) — too few to be the project row, so the absence "+
				"above is an absence from nothing", typ.NumField())
		}
		if strings.Contains(body, "hash") {
			t.Errorf("a serialised Project mentions a hash: %s\nThe declaration walk above did "+
				"not catch it, so this is a field that leaks without a tag naming it.", body)
		}
	})

	t.Run("every_read_selects_the_prefix", func(t *testing.T) {
		if !strings.Contains(projectSelectCols, "identifier_prefix") {
			t.Error("projectSelectCols does not select identifier_prefix. It is the column list " +
				"every project READ shares, pf_list_projects included, so a prefix missing from " +
				"it is a prefix no caller ever sees whatever the struct says.")
		}
		if strings.Contains(projectSelectCols, "identifier_hash") {
			t.Errorf("projectSelectCols selects identifier_hash: %q. The one read that needs the "+
				"hash names it separately (getProjectByNameWithHash) so that the SHARED list "+
				"cannot carry the secret into a response by accident.", projectSelectCols)
		}
	})
}
