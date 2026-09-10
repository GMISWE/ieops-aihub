package mcp_test

// aihub#543 probe wave 2, band 2 (identity/authz) — the
// `docs/mcp-cards/pf_update_user.md` and `docs/mcp-cards/pf_create_user.md`
// sentences about what the admin user surface PUBLISHES and what it does not
// offer at all.
//
//	pf_update_user.md
//	  "It is now a `propEnum` sourced from `domain.UserGlobalRoleList()`…"
//	      -> TestUpdateUserRoleEnumIsTheDomainVocabulary
//	  "…nothing here refuses an out-of-vocabulary value on the strength of the
//	   enum…"
//	      -> TestUpdateUserEnumDoesNotRefuseInProcess
//	  "A user is created and updated; nothing on this surface deletes the row…"
//	      -> TestNothingOnThisSurfaceDeletesAUser
//	pf_create_user.md
//	  "`email` is the interesting row: it is **not** in the `required` array…"
//	      -> TestCreateUserPublishesEmailAsProseRatherThanRequired
//	  "`author_aliases` is stored on the user row and read nowhere in this repo…"
//	      -> TestAuthorAliasesIsWrittenAndNeverRead
//
// All DB-free. Four read a real session; the last is a census over the tree.
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestUpdateUserRoleEnum|TestUpdateUserEnumDoesNot|TestNothingOnThisSurface|TestCreateUserPublishesEmail|TestAuthorAliasesIsWritten' -count=1

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// TestUpdateUserRoleEnumIsTheDomainVocabulary is the twin of
// TestCreateUserVocabulariesArePublishedAsEnums, and the reason it needs to
// exist is the defect aihub#496 closed: ONE column, two tools, two different
// answers to "what may I send", and only one of them machine-readable.
//
// 🔴 The expected set is read out of domain rather than written here. A literal
// {"admin","writer"} would be a third copy of the vocabulary and would stay
// green in the one case that matters — a value added to userGlobalRoles that the
// published enum does not offer — because the literal and the enum would still
// agree with each other while both disagreed with the validator. domain's own
// side of that chain is TestUsersVocabulariesMatchTheMigrations, which parses
// the CHECK out of the SQL.
//
// MUTANTS:
//
//	M12 publication: replace propEnum(…, domain.UserGlobalRoleList()) on
//	     pf_update_user.role with a literal list of one value
//	                                          RED  the sets differ
//	M13 publication: drop the enum, going back to a prose description
//	                                          RED  enumOfProp fatals with the
//	                                               aihub#396 explanation
//	M14 enforcement: let ValidateUserGlobalRole accept a value outside
//	     userGlobalRoles                      RED  in internal/server,
//	                                               TestUpdateUser_IllegalRoleNamesTheField
//	M15 floor: shrink UserGlobalRoleList to one value
//	                                          RED  the sets differ
//
//	🟡 MEASURED GREEN, and that IS the property: adding "maintainer" to
//	domain.userGlobalRoles without touching the schema. The first version of this
//	list recorded it as an enforcement mutant, which was wrong — the enum is
//	SOURCED from UserGlobalRoleList(), so both sides move together and there is
//	nothing to drift. What catches a drift is M12, and the honest green is the
//	evidence that the sourcing is real rather than a coincidence of today's
//	values.
func TestUpdateUserRoleEnumIsTheDomainVocabulary(t *testing.T) {
	want := append([]string(nil), domain.UserGlobalRoleList()...)
	sort.Strings(want)

	// Anti-vacuity: an empty domain list makes the comparison satisfiable by an
	// empty enum, which is a published vocabulary offering nothing.
	if len(want) == 0 {
		t.Fatal("domain publishes no global roles at all, so the comparison below would be " +
			"satisfied by an enum that offers a caller nothing")
	}

	got := enumOfProp(t, "pf_update_user", "role")
	if len(got) != len(want) {
		t.Fatalf("pf_update_user publishes role enum %v; domain enforces %v. A caller offered a "+
			"value the server refuses and a caller not told about a value it accepts are the "+
			"same defect, and this tool published the column as PROSE until aihub#496 while "+
			"pf_create_user had published it as an enum since aihub#463 — one column, two "+
			"answers.", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("pf_update_user publishes role enum %v; domain enforces %v", got, want)
			break
		}
	}
}

// TestUpdateUserEnumDoesNotRefuseInProcess is pf_update_user's copy of the
// measurement pf_create_user's card carries, and like that one it is GREEN on
// both arms by design: it is not evidence for a change, it is what stops the
// published enum from being MISTAKEN for the guard.
//
// polyforge registers through the untyped `(*mcp.Server).AddTool`, whose
// `callTool` hands the request straight to the handler with no schema step
// (aihub#463 measured it on go-sdk v1.6.0). So an out-of-vocabulary role travels
// the whole in-process path and arrives in the PATCH body; the refusal is hop 4,
// in `internal/server/update_user_vocab_test.go`.
//
// 🔴 The value sent is checked against the PUBLISHED enum first. Without that,
// the arm's premise evaporates silently the day `maintainer` is added to the
// vocabulary: "an out-of-vocabulary value reaches the wire" would then be a
// sentence about a value that is in the vocabulary, and it would still pass.
//
// MUTANTS:
//
//	M16 enforcement: add an in-process ValidateUserGlobalRole check to
//	     registerUserTools                    RED  zero calls recorded, with the
//	                                               fatal telling the reader which
//	                                               comments to correct
//	M17 publication: publish pf_update_user.role as prose, with no enum
//	                                          RED  the_value_is_out_of_vocabulary
//	                                               (enumOfProp fatals, since the
//	                                               premise cannot be established)
//	M18 publication: add "maintainer" to the published role enum
//	                                          RED  the_value_is_out_of_vocabulary
func TestUpdateUserEnumDoesNotRefuseInProcess(t *testing.T) {
	// `maintainer` is a legal PROJECT MEMBER role and an illegal global one, so
	// it is the mistake a caller conflating the two vocabularies actually makes —
	// the same value internal/server/update_user_vocab_test.go picks, and the same
	// reason.
	const illegal = "maintainer"

	t.Run("the_value_is_out_of_vocabulary", func(t *testing.T) {
		for _, v := range enumOfProp(t, "pf_update_user", "role") {
			if v == illegal {
				t.Fatalf("%q is now inside pf_update_user's published role enum, so this arm is "+
					"no longer measuring what its name says. Pick a value the vocabulary "+
					"excludes and update internal/server/update_user_vocab_test.go with it.",
					illegal)
			}
		}
	})

	f := newFakeAihub(t)
	callTool(t, f, "pf_update_user", map[string]any{
		"id":   "u_probe580",
		"role": illegal,
	})

	calls := f.recorded()
	if len(calls) != 1 {
		t.Fatalf("expected exactly one HTTP call, got %d (%v) — if this is zero, something in "+
			"this process refused the value and the enum IS a guard. Correct the comments in "+
			"internal/domain/user_fields.go and internal/mcp/tools_users.go, and the paragraph "+
			"in docs/mcp-cards/pf_update_user.md, which all say it is not.",
			len(calls), f.paths())
	}
	if got := calls[0].Path; got != "/v1/admin/users/u_probe580" {
		t.Fatalf("pf_update_user patched %q, want /v1/admin/users/u_probe580", got)
	}
	if got := calls[0].Body["role"]; got != illegal {
		t.Errorf("role reached the PATCH body as %#v, want %q — the published enum neither "+
			"refuses nor rewrites it in this process, which is the whole point of publishing it "+
			"AND validating server-side", got, illegal)
	}
}

// userDeletingName recognises a tool that offers to delete a user.
//
// Broad on the verb and narrow on the object: this has to catch a name nobody
// has thought of yet, and the fixture arm below is what keeps it from being a
// regex that matches nothing.
var userDeletingName = regexp.MustCompile(`(?i)(delete|remove|destroy|purge|drop).*user`)

// TestNothingOnThisSurfaceDeletesAUser holds pf_update_user's "a user is created
// and updated" bullet.
//
// Two directions, because the tool registry is not the only surface: a route
// with no tool is still reachable by anything holding an admin key, and the card
// bullet's own next clause is about what the HTTP layer exposes.
//
// ⚠️ The bullet used to read "the only removal available ANYWHERE here is
// pf_update_project's membership write", which is loose: pf_revoke_api_key is a
// removal, and it is on this very surface. What is true is narrower and is what
// this arm asserts — nothing deletes the user ROW, and the only removal of a
// user's project ACCESS is the membership write. The api-key DELETE is the
// positive control below rather than a counterexample, because it removes a
// credential and leaves the membership alone.
//
// MUTANTS:
//
//	M19 enforcement: register a pf_delete_user tool
//	                                          RED  no_tool_deletes_a_user
//	M20 enforcement: add `c.do(ctx, "DELETE", "/v1/admin/users/"+seg(id), …)` to
//	     pkg/client                           RED  no_client_route_deletes_a_user
//	M21 floor: neuter userDeletingName to match nothing
//	                                          RED  the_recogniser_recognises
//	M22 floor: point the client scan at a file that declares no c.do call
//	                                          RED  the api-key DELETE control (or
//	                                               clientDeleteRoutes' parse fatal,
//	                                               if the file does not exist —
//	                                               both name the reason)
func TestNothingOnThisSurfaceDeletesAUser(t *testing.T) {
	// The recogniser's own fixture arm. A census whose recogniser matches nothing
	// reports a clean surface however dirty it is, and that is the shape every
	// floor in this package exists to refuse.
	t.Run("the_recogniser_recognises", func(t *testing.T) {
		for _, name := range []string{"pf_delete_user", "pf_remove_user", "pf_purge_users"} {
			if !userDeletingName.MatchString(name) {
				t.Errorf("userDeletingName does not match %q, so the census below would pass "+
					"with that tool registered", name)
			}
		}
		// And the values it must NOT claim, or the census fails on a clean tree
		// and the cheapest repair is deleting the arm.
		for _, name := range []string{"pf_update_user", "pf_revoke_api_key", "pf_remove_dependency"} {
			if userDeletingName.MatchString(name) {
				t.Errorf("userDeletingName matches %q, which does not delete a user", name)
			}
		}
	})

	t.Run("no_tool_deletes_a_user", func(t *testing.T) {
		tools := publishedToolList(t)
		if len(tools) < 40 {
			t.Fatalf("the live registry published only %d tool(s); this walk is not reading the "+
				"registry it thinks it is and an absence it reported would be an artefact",
				len(tools))
		}
		for _, tool := range tools {
			if userDeletingName.MatchString(tool.Name) {
				t.Errorf("%s is published and its name offers user deletion. pf_update_user.md "+
					"says a user is created and updated and never deleted here, and the card's "+
					"reasoning depends on it: the only removal of project access is "+
					"pf_update_project's membership write, which is guarded by expected_removals. "+
					"A delete path has no such guard.", tool.Name)
			}
		}
	})

	// The HTTP half, read off pkg/client's own request declarations rather than
	// off a list of routes written here: the client is what every MCP tool goes
	// through, so a route it cannot address is not reachable from this surface.
	t.Run("no_client_route_deletes_a_user", func(t *testing.T) {
		deletes := clientDeleteRoutes(t)
		// The positive control. pkg/client really does declare a DELETE under
		// /v1/admin/users — the api-key revocation — so a scan that found none is
		// broken rather than reassuring.
		control := false
		for _, path := range deletes {
			if strings.Contains(path, "/v1/admin/users/") && strings.Contains(path, "/keys/") {
				control = true
			}
		}
		if !control {
			t.Fatalf("the scan found no DELETE under /v1/admin/users/…/keys/, which pkg/client "+
				"declares (RevokeAPIKey). It found: %v. A scan that cannot see the DELETE that "+
				"IS there cannot be trusted about the one that is not.", deletes)
		}
		for _, path := range deletes {
			if !strings.Contains(path, "/v1/admin/users") {
				continue
			}
			if strings.Contains(path, "/keys") {
				continue // a credential, not the row and not a membership
			}
			t.Errorf("pkg/client declares DELETE %s. That reaches the user row itself rather "+
				"than a credential, so pf_update_user.md's bullet is false and the deletion has "+
				"no equivalent of expected_removals standing in front of it.", path)
		}
	})
}

// clientDeleteRoutes returns the path expression of every DELETE request
// pkg/client declares, as source text.
//
// Source text rather than a resolved value on purpose: the paths are built by
// concatenation (`"/v1/admin/users/"+seg(userID)+"/keys/"+seg(keyID)`), so there
// is no constant to read, and the shape of the concatenation is exactly what
// distinguishes a route reaching a sub-resource from one reaching the row.
func clientDeleteRoutes(t *testing.T) []string {
	t.Helper()
	const rel = "../../pkg/client/client.go"

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v — a parse failure reports no DELETE routes, which reads exactly "+
			"like a client that declares none", rel, err)
	}

	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 3 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "do" {
			return true
		}
		method, ok := call.Args[1].(*ast.BasicLit)
		if !ok || method.Kind != token.STRING {
			return true
		}
		verb, uerr := strconv.Unquote(method.Value)
		if uerr != nil || !strings.EqualFold(verb, "DELETE") {
			return true
		}
		out = append(out, exprSourceText(fset, rel, call.Args[2]))
		return true
	})
	return out
}

// exprSourceText renders an expression as the literal strings it concatenates,
// which is all this census needs and is stable under reformatting.
func exprSourceText(fset *token.FileSet, path string, e ast.Expr) string {
	var sb strings.Builder
	ast.Inspect(e, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				sb.WriteString(s)
			}
		}
		return true
	})
	if sb.Len() == 0 {
		// Nothing literal in it at all — record the position so a failure is
		// traceable rather than blank.
		return path + ":" + strconv.Itoa(fset.Position(e.Pos()).Line) + " (no literal segment)"
	}
	return sb.String()
}

// TestCreateUserPublishesEmailAsProseRatherThanRequired holds pf_create_user's
// `email` row, which the card calls the interesting one.
//
// The claim is a CONJUNCTION and both halves have to hold, because either alone
// is the defect: `email` in the `required` array would make every machine-user
// call fail a validating client for a field the server generates, and an `email`
// that is neither required nor described as conditionally required is a field a
// caller has no reason to send until the server refuses the call.
//
// `display_name` is the control. It IS required, so a decode that produced an
// empty `required` list — a schema shape this test no longer parses, a
// registration that failed — fails there first instead of reading as "email is
// correctly absent".
//
// MUTANTS:
//
//	M23 publication: add "email" to pf_create_user's required list
//	                                          RED  email_is_not_in_required
//	M24 publication: drop "human" from the email description
//	                                          RED  email_says_it_is_conditional
//	M25 publication: remove "display_name" from required
//	                                          RED  the control
//	M26 enforcement: stop refusing a human with no email
//	                                          RED  in internal/server,
//	                                               TestCreateUserEmailIsRequiredForHumansAndGeneratedForMachines
func TestCreateUserPublishesEmailAsProseRatherThanRequired(t *testing.T) {
	required, props := publishedRequiredAndProps(t, "pf_create_user")

	t.Run("the_required_list_is_readable", func(t *testing.T) {
		if len(required) == 0 {
			t.Fatalf("pf_create_user publishes an empty `required` list. display_name is "+
				"required, so this is a decode that is not reading the schema — and every "+
				"absence below would be an artefact of that. Properties: %v", keysOf(props))
		}
		found := false
		for _, r := range required {
			if r == "display_name" {
				found = true
			}
		}
		if !found {
			t.Errorf("pf_create_user's required list is %v and does not name display_name, "+
				"which registerUserTools checks locally before the POST", required)
		}
	})

	t.Run("email_is_not_in_required", func(t *testing.T) {
		for _, r := range required {
			if r == "email" {
				t.Errorf("pf_create_user lists `email` as required (%v). It is required for "+
					"HUMAN users and generated for machine users, and a flat required list "+
					"cannot express that — declaring it flatly makes every machine-user call "+
					"fail a validating client for a field the server is about to invent.",
					required)
			}
		}
	})

	t.Run("email_says_it_is_conditional", func(t *testing.T) {
		email, published := props["email"]
		if !published {
			t.Fatalf("pf_create_user does not publish `email` at all; it publishes %v", keysOf(props))
		}
		lower := strings.ToLower(email.Description)
		if !strings.Contains(lower, "human") {
			t.Errorf("the `email` description does not say for whom it is required.\n  got: %s\n"+
				"The conditional requirement is unexpressible in the required array, so prose "+
				"is the only place it can live — and if the prose stops saying it, nothing does.",
				email.Description)
		}
	})
}

// publishedRequiredAndProps decodes a published InputSchema's `required` list
// alongside its properties. schemaProps gives the second and not the first.
func publishedRequiredAndProps(t *testing.T, tool string) ([]string, map[string]struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}) {
	t.Helper()
	published := publishedTool(t, tool)
	raw, err := json.Marshal(published.InputSchema)
	if err != nil {
		t.Fatalf("marshal %s InputSchema: %v", tool, err)
	}
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode %s InputSchema: %v", tool, err)
	}
	return schema.Required, schemaProps(t, published)
}

// TestAuthorAliasesIsWrittenAndNeverRead is the census behind the correction
// aihub#543 wave 2 made to pf_create_user.md.
//
// 🔴 The card used to say `author_aliases` "set here is how commits attribute to
// this user", and pf_update_user.md said the same in passing. Measured
// 2026-09-10: `users.author_aliases` has WRITERS and no READER. Nothing in
// internal/ or pkg/ selects the column, so no code path in aihub maps a git
// author to a user by it — commit records take their author from the
// authenticated caller instead. That makes the field the §6.1 T1-9 shape the
// pf_update_user card's own Policy section names: a published field nothing
// reads, whose rule is withdraw, fix, or file.
//
// The `display_name` control is the load-bearing half. "No SELECT mentions this
// column" is answered `true` by a scanner that recognises no SELECT at all, and
// that reads as compliance. `display_name` is on the same table and IS selected
// (handleListUsers), so a broken read-detector fails there before this arm can
// report a clean absence.
//
// The published half is asserted too: neither tool's description may claim the
// field is used for attribution. That is the sentence that was false, and if the
// gap ever closes both the description and the cards have to move in the same
// change as the reader.
//
// MUTANTS:
//
//	M27 enforcement: add `author_aliases` to handleListUsers' SELECT
//	                                          RED  no_select_reads_it, naming the
//	                                               file — and the correct repair is
//	                                               to update the cards, not the arm
//	M28 publication: put "used for attribution" back into either published
//	     description                          RED  no_description_claims_a_reader
//	M29 floor: drop `author_aliases` from TWO of its three write sites
//	                                          RED  it_is_written_somewhere. ⚠️
//	                                               Dropping ONE is GREEN — the floor
//	                                               is two of three, which is what a
//	                                               floor is for; the single-site
//	                                               case is caught in internal/server
//	                                               by
//	                                               TestCreateUserResponseIsTheHandlersOwnProjection
//	                                               (the_column_is_written)
//	M30 floor: make the read-detector match nothing
//	                                          RED  the display_name control
func TestAuthorAliasesIsWrittenAndNeverRead(t *testing.T) {
	const column = "author_aliases"
	const readControl = "display_name"

	writes, reads := columnSQLSites(t, column)
	controlWrites, controlReads := columnSQLSites(t, readControl)

	t.Run("the_detector_sees_a_real_read", func(t *testing.T) {
		if len(controlReads) == 0 {
			t.Fatalf("the scan found no SELECT mentioning %q, which handleListUsers declares "+
				"(`SELECT id, email, display_name, user_type, role FROM users`). A read-detector "+
				"that sees no reads answers \"nothing reads it\" about every column, so the "+
				"finding below would be an artefact. Writes seen for the control: %v",
				readControl, controlWrites)
		}
	})

	t.Run("it_is_written_somewhere", func(t *testing.T) {
		if len(writes) < 2 {
			t.Fatalf("the scan found %d SQL write(s) of %q: %v. The column is written by "+
				"handleCreateUser, handleUpdateUser and the bootstrap admin INSERT, so fewer "+
				"than two means this walk is not reading the tree — and a walk that finds no "+
				"writes and no reads is silent, not clean.", len(writes), column, writes)
		}
	})

	t.Run("no_select_reads_it", func(t *testing.T) {
		if len(reads) > 0 {
			t.Errorf("%q is now SELECTed at %v.\nThat closes the gap this arm records, and the "+
				"repair is NOT to relax this arm: docs/mcp-cards/pf_create_user.md and "+
				"docs/mcp-cards/pf_update_user.md both state that the column has no reader, and "+
				"the published descriptions are asserted below not to claim one. Update all "+
				"three in the same change as the reader.", column, reads)
		}
	})

	t.Run("no_description_claims_a_reader", func(t *testing.T) {
		for _, tool := range []string{"pf_create_user", "pf_update_user"} {
			props := schemaProps(t, publishedTool(t, tool))
			p, published := props[column]
			if !published {
				t.Errorf("%s does not publish %q; aihub#426 published it on pf_update_user "+
					"precisely so it would stop being reachable only by guessing", tool, column)
				continue
			}
			lower := strings.ToLower(p.Description)
			for _, claim := range []string{"attribut", "maps to", "mapped to"} {
				if strings.Contains(lower, claim) {
					t.Errorf("%s's %q description contains %q: %q\nNothing in this repo reads "+
						"the column, so a description promising attribution is the exact "+
						"sentence wave 2 removed from the cards. If a reader now exists, say so "+
						"here AND in both cards AND wire the reader — this arm's "+
						"no_select_reads_it subtest is what keeps the three honest.",
						tool, column, claim, p.Description)
				}
			}
		}
	})
}

var (
	// sqlSelect / sqlWrite classify a string literal that mentions a column.
	// Deliberately about the STATEMENT and not about the column's position in it:
	// a literal is a fragment as often as a whole statement, and `author_aliases=$`
	// is the whole of one write site.
	sqlSelect = regexp.MustCompile(`(?is)\bselect\b`)
	sqlWrite  = regexp.MustCompile(`(?is)\binsert\s+into\b|\bupdate\b|\bset\b|=\s*\$\d*`)
)

// columnSQLSites censuses every string literal in non-test Go under internal/
// and pkg/ that mentions the column, split into SQL reads and SQL writes.
//
// String literals rather than a text grep, and go/parser rather than a scanner,
// for the reason BuildArmIndex states: a comment or a fixture can contain the
// word, and counting those would put a mention where a statement has to be.
// Struct tags and schema keys mention the column without being SQL at all, and
// they land in neither list.
func columnSQLSites(t *testing.T, column string) (writes, reads []string) {
	t.Helper()
	for _, root := range []string{"../../internal", "../../pkg"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if d.IsDir() {
				switch d.Name() {
				case ".git", "vendor", "node_modules", "testdata":
					return fs.SkipDir
				}
				return nil
			}
			name := d.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if perr != nil {
				return perr
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, uerr := strconv.Unquote(lit.Value)
				if uerr != nil || !strings.Contains(s, column) {
					return true
				}
				site := filepath.ToSlash(path) + ":" + strconv.Itoa(fset.Position(lit.Pos()).Line)
				switch {
				case sqlSelect.MatchString(s):
					reads = append(reads, site)
				case sqlWrite.MatchString(s):
					writes = append(writes, site)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s for %q: %v — a failed walk reports no sites at all, which is the "+
				"same answer as a column nobody touches", root, column, err)
		}
	}
	sort.Strings(writes)
	sort.Strings(reads)
	return writes, reads
}
