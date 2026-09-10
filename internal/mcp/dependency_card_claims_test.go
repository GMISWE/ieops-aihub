package mcp_test

// aihub#543 probe wave 2, the dependency family — the non-DB half of
// `docs/mcp-cards/pf_list_dependencies.md`, `pf_create_dependency.md` and
// `pf_remove_dependency.md`. The row-level half is beside the existing DB arms
// (internal/domain/dependencies_direction_test.go and
// dependencies_requeue_test.go), attached as subtests of the functions
// internal/citest/dbtestcov/gated_tests.txt and ci.yml already name.
//
// What is held here:
//
//   - how each of the three tools addresses an edge, and that none of them
//     sends an attempt credential — the fields aihub#324 withdrew rather than
//     made real;
//   - `slug` carrying no `omitempty`, so "you may not see this" and "this has
//     no slug" cannot arrive as the same absent field;
//   - the three non-test read sites of `domain.RoleLevel`, which is the census
//     `pf_list_dependencies`'s own prose makes a numeric claim about;
//   - `kind` being published as PROSE rather than an enum, so an
//     out-of-vocabulary value is not refused before the handler runs; and
//   - migration `0013`'s `maintainer` -> `writer` backfill.
//
// 🔴 WHY NOTHING HELD THEM. internal/mcp/dependency_authz_e2e_db_test.go pins
// the authorization model, and internal/domain has direction and requeue arms —
// all three about EFFECTS. Nothing looked at what leaves the process for these
// tools since aihub#324 deleted the credentials, and nothing at all looked at
// the response STRUCT, whose `omitempty` decision is a contract a reader cannot
// see from any response they are likely to receive (a caller who can see every
// end never observes the withheld case).
//
// No database, no git.
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestDependency|TestRoleLevelIsRead|TestMigration0013' -count=1 -v

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

const (
	migration0013Path = "../db/migrations/0013_backfill_projects.sql"
	serverMiddleware  = "../server/middleware.go"
)

// TestDependencyToolsAddressEdgesByPathAndSendNoCredential is the hop-2-3 half
// of all three cards at once: one request each, the ids in the path in the
// documented order, and nothing resembling an attempt credential anywhere.
//
// The remove side gets the sharper assertion, because it is the sharper half of
// the aihub#324 defect: every parameter is a path segment and there is no body
// AT ALL, which is why the three credential fields the old code built could be
// constructed and dropped inside the same function without anybody noticing.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M78 pf_list_dependencies drops its empty-value refusal    RED  (a request is
//	                                                          made for an empty id)
//	M79 RemoveDependency sends a body                         RED  (no-body arm)
//	M80 RemoveDependency reorders two path segments           RED  (path arm)
//	M81 pf_create_dependency puts `note` in the body when
//	    empty                                                 RED  (body arm)
//	M82 the create body regains attempt_id/claim_epoch/
//	    session_secret                                        RED  (credential arm)
//	M83 green control: reword the three tool descriptions     GREEN
func TestDependencyToolsAddressEdgesByPathAndSendNoCredential(t *testing.T) {
	const (
		blocked  = "wi_BLOCKEDEND01"
		blocking = "wi_BLOCKINGEND1"
		kind     = "blocks"
	)
	credentialKeys := []string{"attempt_id", "claim_epoch", "session_secret"}

	t.Run("list_addresses_by_path_and_refuses_an_empty_id", func(t *testing.T) {
		f := newFakeAihub(t)
		if out, isErr := callTool(t, f, "pf_list_dependencies",
			map[string]any{"work_item_id": ""}); !isErr {
			t.Errorf("pf_list_dependencies accepted an empty work_item_id and answered %v", out)
		}
		if n := len(f.recorded()); n != 0 {
			t.Errorf("an empty work_item_id still produced %d request(s) %v; the refusal is the "+
				"tool's own, so nothing should leave the process", n, f.paths())
		}

		f2 := newFakeAihub(t)
		f2.on("/v1/work_items/"+blocked+"/dependencies", func(map[string]any) (int, any) {
			return 200, map[string]any{"blocking": []any{}, "blocked_by": []any{}}
		})
		out, isErr := callTool(t, f2, "pf_list_dependencies", map[string]any{"work_item_id": blocked})
		if isErr {
			t.Fatalf("pf_list_dependencies failed: %v", out)
		}
		calls := f2.recorded()
		if len(calls) != 1 {
			t.Fatalf("pf_list_dependencies made %d request(s) %v, want 1", len(calls), f2.paths())
		}
		if calls[0].Method != "GET" ||
			calls[0].Path != "/v1/work_items/"+blocked+"/dependencies" {
			t.Errorf("pf_list_dependencies sent %s %s, want GET /v1/work_items/%s/dependencies",
				calls[0].Method, calls[0].Path, blocked)
		}
		if calls[0].Body != nil {
			t.Errorf("pf_list_dependencies sent a body %v; the work item is a path segment and "+
				"there is nothing else to send", calls[0].Body)
		}
	})

	t.Run("create_sends_the_three_fields_and_no_credential", func(t *testing.T) {
		f := newFakeAihub(t)
		out, isErr := callTool(t, f, "pf_create_dependency", map[string]any{
			"blocked_wi_id": blocked, "blocking_wi_id": blocking, "kind": kind,
		})
		if isErr {
			t.Fatalf("pf_create_dependency failed: %v", out)
		}
		calls := f.recorded()
		if len(calls) != 1 {
			t.Fatalf("pf_create_dependency made %d request(s) %v, want 1", len(calls), f.paths())
		}
		c := calls[0]
		if c.Method != "POST" || c.Path != "/v1/work_items/"+blocked+"/dependencies" {
			t.Errorf("pf_create_dependency sent %s %s, want POST /v1/work_items/%s/dependencies — "+
				"the BLOCKED end is the path segment, which is also the project the writer role is "+
				"checked against", c.Method, c.Path, blocked)
		}
		want := map[string]any{"blocked_wi_id": blocked, "blocking_wi_id": blocking, "kind": kind}
		if !reflect.DeepEqual(c.Body, want) {
			t.Errorf("pf_create_dependency's body is %v, want exactly %v. `note` is omitted when "+
				"empty rather than sent as \"\", and an extra key here is a hop no card describes.",
				c.Body, want)
		}
		for _, k := range credentialKeys {
			if v, has := c.Body[k]; has {
				t.Errorf("the create body carries %q=%v. aihub#324 WITHDREW those fields rather than "+
					"making them real, because a credential nothing checks makes every reader — "+
					"including a reviewer — conclude the path is attempt-gated.", k, v)
			}
		}

		// `note` when non-empty, so "omitted when empty" is a decision between two
		// reachable shapes rather than the only shape.
		f2 := newFakeAihub(t)
		if _, isErr := callTool(t, f2, "pf_create_dependency", map[string]any{
			"blocked_wi_id": blocked, "blocking_wi_id": blocking, "kind": kind,
			"note": "because the upstream item has to land first",
		}); isErr {
			t.Fatalf("pf_create_dependency with a note failed")
		}
		if got := f2.recorded()[0].Body["note"]; got != "because the upstream item has to land first" {
			t.Errorf("a supplied note reached the wire as %v", got)
		}
	})

	t.Run("remove_puts_all_three_in_the_path_and_sends_no_body", func(t *testing.T) {
		f := newFakeAihub(t)
		out, isErr := callTool(t, f, "pf_remove_dependency", map[string]any{
			"blocked_wi_id": blocked, "blocking_wi_id": blocking, "kind": kind,
		})
		if isErr {
			t.Fatalf("pf_remove_dependency failed: %v", out)
		}
		calls := f.recorded()
		if len(calls) != 1 {
			t.Fatalf("pf_remove_dependency made %d request(s) %v, want 1", len(calls), f.paths())
		}
		c := calls[0]
		wantPath := "/v1/work_items/" + blocked + "/dependencies/" + blocking + "/" + kind
		if c.Method != "DELETE" || c.Path != wantPath {
			t.Errorf("pf_remove_dependency sent %s %s, want DELETE %s. All three parameters are path "+
				"segments, so a reordering addresses a different edge and the server answers about "+
				"that one.", c.Method, c.Path, wantPath)
		}
		if c.Body != nil {
			t.Errorf("pf_remove_dependency sent a body %v. There is no body at all on this call — "+
				"which is exactly why the three credential fields the pre-aihub#324 code built here "+
				"were constructed and dropped inside the same function: the client only marshals a "+
				"non-nil body.", c.Body)
		}
	})
}

// TestDependencyListEntryDisclosesSlugWithoutOmitempty is
// `pf_list_dependencies`'s hop-5 sentence: "`slug` deliberately carries no
// `omitempty`: with one, \"you may not see this work item\" and \"this work item
// has no slug\" would arrive as the same absent field."
//
// The struct tag AND the marshalled shape, because the tag is the cause and the
// wire is the effect; and `Note` is asserted to KEEP its `omitempty`, so the
// arm is about a decision made per field rather than about a struct nobody
// tagged.
//
// Mutants (2026-09-10):
//
//	M84 Slug gains `omitempty`                                RED  (tag arm and
//	                                                          marshal arm)
//	M85 Slug becomes a plain string                           NOT RUN — it does
//	                                                          not compile: four
//	                                                          sites depend on the
//	                                                          pointer, including a
//	                                                          nil check in
//	                                                          internal/server. The
//	                                                          enforcement mutant
//	                                                          for the absent case
//	                                                          is M107 (the fold
//	                                                          sets Slug=nil),
//	                                                          which the DB arm in
//	                                                          internal/domain
//	                                                          catches.
//	M86 Note loses its `omitempty`                            RED  (control arm)
//	M87 green control: reword the field's doc comment          GREEN
func TestDependencyListEntryDisclosesSlugWithoutOmitempty(t *testing.T) {
	typ := reflect.TypeOf(domain.DependencyListEntry{})

	slug, ok := typ.FieldByName("Slug")
	if !ok {
		t.Fatalf("domain.DependencyListEntry has no Slug field, so the sentence this arm holds is "+
			"about a field that no longer exists: %v", typ)
	}
	if got := slug.Tag.Get("json"); got != "slug" {
		t.Errorf("Slug's json tag is %q, want \"slug\" with no omitempty. With omitempty, a caller "+
			"who may not open the far end and a caller looking at a work item with no slug are "+
			"handed the same absent field, and neither can act on the difference.", got)
	}
	if slug.Type.Kind() != reflect.Pointer {
		t.Errorf("Slug is %s rather than a pointer, so \"this work item has no slug\" has no "+
			"representation distinct from the empty string", slug.Type)
	}

	// The control: omitempty is used deliberately elsewhere in the same struct,
	// so its absence on Slug is a decision rather than a struct nobody tagged.
	note, ok := typ.FieldByName("Note")
	if !ok {
		t.Fatalf("domain.DependencyListEntry has no Note field; the control below has nothing to " +
			"compare against")
	}
	if !strings.Contains(note.Tag.Get("json"), "omitempty") {
		t.Errorf("Note's json tag is %q and carries no omitempty. Then no field in this struct uses "+
			"it, and Slug's lack of it says nothing about anybody's intent.", note.Tag.Get("json"))
	}

	// And the consequence on the wire.
	raw, err := json.Marshal(domain.DependencyListEntry{ID: "hidden", Project: "other", Kind: "blocks"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"slug":null`) {
		t.Errorf("a withheld entry marshals to %s, with no explicit null slug. The absent-versus-null "+
			"distinction only reaches a caller if the key is on the wire.", raw)
	}
	if strings.Contains(string(raw), `"note"`) {
		t.Errorf("an entry with no note marshals to %s and carries the key anyway, so the control "+
			"above is measuring a tag nothing honours", raw)
	}
}

// roleLevelRead is one non-test statement that indexes the role ladder.
type roleLevelRead struct {
	file string
	fn   string
}

// TestRoleLevelIsReadAtExactlyTheSitesThisCardNames is the census
// `pf_list_dependencies`'s Policy section makes a numeric claim about: the
// `Accessible` computation is one of "that map's three non-test call sites".
//
// 🔴 MEASURED 2026-09-10, and the card's number was wrong in two directions at
// once. There are SIX non-test readers, not three, and this computation is TWO
// of them rather than one:
//
//	domain.RoleLevel      internal/domain/dependencies.go  ListDependencies      (2 statements, one per direction)
//	                      internal/domain/projects.go      checkProjectAccess
//	roleLevel (the alias) internal/server/middleware.go    checkProjectAccess
//	                      internal/server/router.go        handleListWorkItems
//	                      internal/server/routes_memory.go hasProjectAccess
//	                      internal/server/ui_handlers_wi.go checkProjectAccessSoft
//
// The four in internal/server read `roleLevel`, which IS `domain.RoleLevel` —
// the card's own previous sentence says so — so counting only the
// domain-spelled reads gives three and describes a third of the blast radius of
// changing the ladder. The card now states the measured set; this arm is what
// keeps it true, in both directions: a new reader is red, and a deleted one is
// red too.
//
// The two properties a census cannot see — that the two packages share ONE map
// value, and that the ladder holds exactly the three legal member roles — are
// held by `internal/server/middleware_project_roles_test.go`
// (`TestRoleLevelIsTheDomainLadder`) and `internal/domain/projects_test.go`
// (`TestRoleLevel_LadderIsExactlyTheValidatedVocabulary`), both read before
// being cited here.
//
// Mutants (2026-09-10):
//
//	M88 a fifth internal/server function reads the alias       RED  (set arm)
//	M89 one of the two Accessible reads is deleted             RED  (per-direction
//	                                                           count)
//	M90 middleware.go copies the map instead of aliasing it    RED  (alias arm)
//	M91 green control: reformat one comparison across lines    GREEN
func TestRoleLevelIsReadAtExactlyTheSitesThisCardNames(t *testing.T) {
	reads := roleLevelReadSites(t)
	if len(reads) == 0 {
		t.Fatalf("the census found no read of the role ladder at all; the walk is broken rather " +
			"than the code, and every count below would agree with a card that claimed anything")
	}

	// The READER SET, keyed on file+function: stable under reformatting, and the
	// unit a reader of the card cares about ("who decides what a role is worth").
	want := map[string]bool{
		"domain/dependencies.go:ListDependencies":         false,
		"domain/projects.go:checkProjectAccess":           false,
		"server/middleware.go:checkProjectAccess":         false,
		"server/router.go:handleListWorkItems":            false,
		"server/routes_memory.go:hasProjectAccess":        false,
		"server/ui_handlers_wi.go:checkProjectAccessSoft": false,
	}
	statements := map[string]int{}
	var extra []string
	for _, r := range reads {
		key := strings.TrimPrefix(filepath.ToSlash(r.file), "../") + ":" + r.fn
		statements[key]++
		if _, known := want[key]; known {
			want[key] = true
			continue
		}
		extra = append(extra, key)
	}
	for key, found := range want {
		if !found {
			t.Errorf("%s no longer reads the role ladder. The card's census names it; a reader that "+
				"went is a place that stopped consulting the one ladder aihub#443 left, and the "+
				"card's number has to move with it.", key)
		}
	}
	if len(extra) > 0 {
		t.Errorf("these non-test functions also read the role ladder and the card's census names "+
			"none of them: %v. Each one is another place that decides what a role is worth.", extra)
	}

	// The two directions of THIS computation, which is the half the card's
	// sentence is actually about: one read per response list, so a fold that
	// stopped happening on one side would be red here as well as in the DB arm.
	if got := statements["domain/dependencies.go:ListDependencies"]; got != 2 {
		t.Errorf("ListDependencies reads the ladder in %d statement(s), want 2 — one per direction "+
			"of the response. With one, the blocking and blocked_by lists no longer decide "+
			"accessibility the same way.", got)
	}

	// The alias, which is why internal/server's four readers are reads of the
	// SAME map rather than of a copy. Identity is asserted by
	// TestRoleLevelIsTheDomainLadder; what this checks is that the declaration is
	// still an alias rather than a literal.
	src, err := os.ReadFile(serverMiddleware)
	if err != nil {
		t.Fatalf("read %s: %v", serverMiddleware, err)
	}
	if !strings.Contains(string(src), "var roleLevel = domain.RoleLevel") {
		t.Errorf("%s no longer declares roleLevel as domain.RoleLevel. A copy that happens to agree "+
			"is what the two packages had before aihub#443, and it would also make the four readers "+
			"below it readers of something else.", serverMiddleware)
	}
}

// roleLevelReadSites walks internal/domain and internal/server for non-test
// statements that index the role ladder, by either spelling.
func roleLevelReadSites(t *testing.T) []roleLevelRead {
	t.Helper()
	var out []roleLevelRead
	for _, dir := range []string{"../domain", "../server"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			fset := token.NewFileSet()
			file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if perr != nil {
				t.Fatalf("parse %s: %v", path, perr)
			}
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				for _, stmt := range flattenStmts(fd.Body) {
					if indexesRoleLevel(stmt) {
						out = append(out, roleLevelRead{file: path, fn: fd.Name.Name})
					}
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].file != out[j].file {
			return out[i].file < out[j].file
		}
		return out[i].fn < out[j].fn
	})
	return out
}

// flattenStmts returns every statement in a body, including nested ones, so a
// read inside a loop or an if is counted once for the statement it sits in.
func flattenStmts(body *ast.BlockStmt) []ast.Stmt {
	var out []ast.Stmt
	ast.Inspect(body, func(n ast.Node) bool {
		if s, ok := n.(ast.Stmt); ok {
			if _, isBlock := s.(*ast.BlockStmt); !isBlock {
				out = append(out, s)
			}
		}
		return true
	})
	// Keep only the outermost statement of each nesting level: an assignment
	// inside an if-statement would otherwise be counted twice, once for the
	// IfStmt and once for itself.
	var kept []ast.Stmt
	for _, s := range out {
		nested := false
		for _, other := range out {
			if other == s {
				continue
			}
			if other.Pos() <= s.Pos() && s.End() <= other.End() && indexesRoleLevel(other) {
				nested = true
				break
			}
		}
		if !nested {
			kept = append(kept, s)
		}
	}
	return kept
}

func indexesRoleLevel(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(node ast.Node) bool {
		idx, ok := node.(*ast.IndexExpr)
		if !ok {
			return true
		}
		switch x := idx.X.(type) {
		case *ast.Ident:
			if x.Name == "RoleLevel" || x.Name == "roleLevel" {
				found = true
			}
		case *ast.SelectorExpr:
			if x.Sel.Name == "RoleLevel" || x.Sel.Name == "roleLevel" {
				found = true
			}
		}
		return true
	})
	return found
}

// TestDependencyKindIsPublishedAsProseAndNotRefusedAtHopOne is
// `pf_create_dependency`'s "`kind` is prose rather than an enum, so an
// out-of-vocabulary value is not refused before the handler runs" — and its
// Open bullet, which said only that and left a reader thinking nothing checks
// the value at all.
//
// Measured 2026-09-10: nothing refuses it BEFORE the handler, and
// `domain.validateDependencyKind` refuses it INSIDE the handler against a
// vocabulary the migration's own CHECK is held equal to
// (`internal/domain/db_check_policy_test.go`,
// `TestDBCheckRegistry_MirrorsMatchTheMigration`, read before citing). The card
// now says both.
//
// Mutants (2026-09-10):
//
//	M92 `kind` becomes a propEnum                              RED  (hop-1 arm —
//	                                                           and then the
//	                                                           sentence is false)
//	M93 the tool refuses an unknown kind locally               RED  (forwarding arm)
//	M94 DependencyKindList gains a fourth value                RED  (vocabulary arm)
//	M95 green control: reword the `kind` property description  GREEN
func TestDependencyKindIsPublishedAsProseAndNotRefusedAtHopOne(t *testing.T) {
	tool := publishedTool(t, "pf_create_dependency")
	schema := schemaJSON(t, tool.Name, tool.InputSchema)
	kindProp := regexpFindProperty(t, schema, "kind")
	if kindProp == "" {
		t.Fatalf("pf_create_dependency publishes no `kind` property:\n%s", schema)
	}
	if strings.Contains(kindProp, `"enum"`) {
		t.Errorf("`kind` now publishes an enum: %s. The card's sentence — and its Open bullet — say "+
			"it is PROSE, which is what makes an out-of-vocabulary value reach the handler; a "+
			"published enum is a different contract and both have to change together.", kindProp)
	}
	// The prose has to name the vocabulary, or "published as prose" means the
	// caller was told nothing at all.
	for _, v := range domain.DependencyKindList() {
		if !strings.Contains(kindProp, v) {
			t.Errorf("`kind`'s published description %s does not name the legal value %q. Prose is "+
				"an acceptable way to publish a vocabulary only while it lists it.", kindProp, v)
		}
	}
	if got := len(domain.DependencyKindList()); got != 3 {
		t.Errorf("domain.DependencyKindList() holds %d value(s) %v; the card's hop-1 table and both "+
			"tool descriptions state three", got, domain.DependencyKindList())
	}

	// And an out-of-vocabulary value really does reach the wire: the refusal is
	// the server's, so the tool must not be the one making it.
	const bogus = "supersedes-ish"
	for _, v := range domain.DependencyKindList() {
		if v == bogus {
			t.Fatalf("the fixture's %q is a legal kind, so the forwarding arm below tests nothing", bogus)
		}
	}
	f := newFakeAihub(t)
	out, isErr := callTool(t, f, "pf_create_dependency", map[string]any{
		"blocked_wi_id": "wi_KINDBLOCKED1", "blocking_wi_id": "wi_KINDBLOCKING", "kind": bogus,
	})
	if isErr {
		t.Fatalf("pf_create_dependency refused %q locally and answered %v. The card's sentence is "+
			"that an out-of-vocabulary value is NOT refused before the handler runs; a local refusal "+
			"makes it false and hides where the real check lives.", bogus, out)
	}
	calls := f.recorded()
	if len(calls) != 1 || calls[0].Body["kind"] != bogus {
		t.Errorf("the unknown kind did not reach the wire verbatim: %v", calls)
	}
}

// regexpFindProperty returns one property object out of a published schema.
func regexpFindProperty(t *testing.T, schema, name string) string {
	t.Helper()
	for _, m := range strings.Split(schema, `"`+name+`":`)[1:] {
		depth := 0
		for i, r := range m {
			switch r {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return m[:i+1]
				}
			}
		}
	}
	return ""
}

// TestMigration0013MappedMaintainerToWriterOnBackfill is
// `pf_list_dependencies`'s Open sentence about where today's `maintainer` rows
// came from: migration `0013` mapped that role to `writer` while backfilling,
// so a live `maintainer` row must have arrived from a later members write.
//
// The migration is in the tree; the live rows are not, so this arm holds the
// half that is checkable and the card carries the other half as a dated
// measurement.
//
// Mutants (2026-09-10):
//
//	M96 the CASE mapping is removed from 0013                  RED
//	M97 the backfill's role filter drops `maintainer`          RED
//	M98 green control: reword a comment in the migration       GREEN
func TestMigration0013MappedMaintainerToWriterOnBackfill(t *testing.T) {
	raw, err := os.ReadFile(migration0013Path)
	if err != nil {
		t.Fatalf("read %s: %v — the card's Open section reasons from this migration, so a missing "+
			"file is a failure rather than a skip", migration0013Path, err)
	}
	sql := string(raw)
	if !strings.Contains(sql, "rec.role='maintainer' THEN 'writer'") {
		t.Errorf("%s no longer maps maintainer to writer on backfill. The card's inference — that a "+
			"live maintainer row came from a members write AFTER the backfill — rests entirely on "+
			"this mapping.", migration0013Path)
	}
	if !strings.Contains(sql, "'viewer','writer','maintainer'") {
		t.Errorf("%s's role filter no longer names maintainer, so the CASE above can never fire and "+
			"the mapping is dead text", migration0013Path)
	}
	// The narrowing was real: `maintainer` is a legal member role today, which is
	// what makes "the backfill wrote writer instead" a fact about the backfill
	// rather than about a role that did not exist.
	if _, ok := domain.RoleLevel["maintainer"]; !ok {
		t.Errorf("domain.RoleLevel no longer ranks maintainer, so the card's whole T2-8 paragraph is "+
			"about a role the vocabulary has dropped: %v", domain.RoleLevel)
	}
}
