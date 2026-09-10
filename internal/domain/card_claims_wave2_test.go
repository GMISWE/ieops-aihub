package domain

// aihub#543 probe wave 2 — the domain-side halves of five claims published by
// docs/mcp-cards/pf_whoami.md, pf_list_projects.md and pf_list_users.md.
//
//	pf_whoami   "the `role` this tool reports is drawn from a WIDER vocabulary
//	             than the one pf_update_project's `members` accepts" and
//	            "`role` here can be `owner`, which pf_update_project will refuse"
//	                -> TestOwnerIsNotAMemberRoleUpdateProjectAccepts
//	pf_whoami   "Migration 0013 mapped `maintainer` to `writer` on backfill"
//	                -> TestMigration0013MappedMaintainerToWriterOnBackfill
//	pf_list_projects
//	            "Each item carries `members`, `members_version`,
//	             `owner_user_id`, `repos` and `scenario`"
//	                -> TestListProjectsSelectsAndBindsTheItemFieldsTheCardNames
//	pf_list_projects
//	            "It scopes by the SQL predicate … and `roleLevel` is not on its
//	             path. `members` is reported, not compared."
//	                -> TestListProjectsScopesBySQLAndNeverRanksARole
//	pf_list_users
//	            "The GLOBAL role vocabulary here is `writer | admin`, which is a
//	             THIRD vocabulary distinct from both the member roles and the
//	             project owner column"
//	                -> TestTheThreeRoleVocabulariesAreNotSupersetsOfOneAnother
//	pf_list_users
//	            "the identities they can name are the rows this tool returns"
//	                -> TestEveryUserIdColumnReferencesTheRowsListUsersReturns
//
// All of them are non-DB by construction: three read a declaration, two read a
// migration, and one reads the AST of the function that does the scoping. The
// aihub#543 spec §3.3 rule is to reach for a database only after asking whether
// the claim is really about a row, and none of these is.
//
//	GOWORK=off go test ./internal/domain/ -run 'TestOwnerIsNotAMemberRole|TestMigration0013|TestListProjects(Selects|Scopes)|TestTheThreeRole|TestEveryUserIdColumn' -count=1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestOwnerIsNotAMemberRoleUpdateProjectAccepts holds the sharp end of the
// two-vocabulary split: `owner` is the `projects.owner_user_id` column, and
// pf_update_project's `members` refuses it.
//
// The legal set is read out of UpdateProject's own validation by
// validatedMemberRoles (projects_test.go), not restated here — the aihub#443
// argument for that helper applies unchanged: a hand-copied third list stays
// green in exactly the case that matters.
//
// The pf_whoami side, that this tool really does report `owner`, is driven
// through the registered tool by internal/mcp/whoami_projects_shape_test.go
// (TestWhoamiReportsAnOwnerRoleThatIsNoMemberRole). Together they are the claim;
// either alone is half of it, because a value can be absent from a vocabulary
// without anything ever reporting it.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M23 enforcement: add `&& m.Role != "owner"` — i.e. let UpdateProject accept
//	    owner as a member role                  RED
//	M24 enforcement: add `"owner": 4` to RoleLevel   RED  (the other direction:
//	                                                 the ladder ranking a column
//	                                                 value)
//	P1  publication: make every citation in the pf_whoami card unresolvable
//	                                            RED  K12
func TestOwnerIsNotAMemberRoleUpdateProjectAccepts(t *testing.T) {
	legal := validatedMemberRoles(t)
	if len(legal) == 0 {
		t.Fatal("UpdateProject's member-role validation yielded no legal roles; every assertion " +
			"below would then be about an empty set")
	}
	for _, role := range legal {
		if role == "owner" {
			t.Errorf("pf_update_project accepts %q for a member, so the value pf_whoami reports for "+
				"an admin or the project owner is NOT outside the member vocabulary any more. Two "+
				"card sentences say it is, and the §6.2 T2-17 ruling they carry exists because only "+
				"the value a caller cannot send is worth naming to an LLM.", role)
		}
	}
	// The other direction, so this cannot pass by the validation having moved
	// somewhere this test does not read: the ladder keys and the accepted set
	// are held equal by TestRoleLevel_LadderIsExactlyTheValidatedVocabulary, so
	// a rung for owner would be the same defect arriving from the other side.
	if _, ranked := RoleLevel["owner"]; ranked {
		t.Errorf("RoleLevel has a rung for \"owner\" (%d). It is a column value, not a member role, "+
			"and ranking it is the pre-aihub#443 ladder returning", RoleLevel["owner"])
	}
}

// TestMigration0013MappedMaintainerToWriterOnBackfill holds the pf_whoami card's
// account of why a live `maintainer` row must have been written after the
// backfill rather than by it.
//
// The migration is scanned as text because that is what the claim is about — a
// statement that ran once, whose only remaining record is the file. Every
// migration is read rather than only 0013, so a later file that re-runs the
// backfill without the mapping is seen.
//
// MUTANTS:
//
//	M25 enforcement: change the CASE to `THEN 'maintainer'`  RED
//	M26 enforcement: delete the CASE, copying the role verbatim (`'role', rec.role`)
//	                                                             RED
//	P1  publication: make every citation in the pf_whoami card unresolvable
//	                                                             RED  K12
func TestMigration0013MappedMaintainerToWriterOnBackfill(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(migrationsDir, "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no migrations under %s — the walk is broken, not the claim", migrationsDir)
	}

	// The mapping as the file spells it, whitespace-insensitive: a CASE over
	// rec.role that answers 'writer' for 'maintainer'.
	mapping := regexp.MustCompile(`(?is)CASE\s+WHEN\s+rec\.role\s*=\s*'maintainer'\s+THEN\s+'writer'`)
	backfills, mapped := 0, 0
	for _, f := range files {
		src, readErr := os.ReadFile(f)
		if readErr != nil {
			t.Fatal(readErr)
		}
		text := string(src)
		// A backfill is any statement that copies users.project_roles into
		// projects.members; the mapping only has to hold for those.
		if !strings.Contains(text, "project_roles") || !strings.Contains(text, "members") {
			continue
		}
		if !strings.Contains(text, "jsonb_build_object") {
			continue
		}
		backfills++
		if mapping.MatchString(text) {
			mapped++
			continue
		}
		t.Errorf("%s copies project_roles into projects.members without mapping maintainer to "+
			"writer. The pf_whoami card concludes from that mapping that a live `maintainer` row "+
			"cannot have arrived from the backfill — with a second, unmapped backfill in the tree "+
			"the conclusion does not follow.", filepath.Base(f))
	}
	if backfills == 0 {
		t.Fatalf("no migration under %s copies project_roles into projects.members, so the loop "+
			"above asserted nothing. Either the backfill was removed — in which case the card's "+
			"reasoning has lost its premise and must be rewritten rather than left standing — or "+
			"this scan no longer recognises it.", migrationsDir)
	}
	t.Logf("%d backfill migration(s), %d carrying the maintainer->writer mapping", backfills, mapped)
}

// TestListProjectsSelectsAndBindsTheItemFieldsTheCardNames holds
// pf_list_projects' hop-4 list of what every item carries.
//
// Both halves, because either alone is satisfiable while the field is missing
// from the response: a column the query does not SELECT arrives as the struct's
// zero value, and a struct field with no json tag never reaches the wire at all.
// handleListProjects marshals []domain.Project straight into `items`, so these
// two declarations are the response shape.
//
// MUTANTS:
//
//	M27 enforcement: drop `scenario` from projectSelectCols   RED
//	M28 enforcement: change Project.MembersVersion's tag to `json:"-"`  RED
//	P2  publication: make every citation in the pf_list_projects card unresolvable
//	                                                                    RED  K12
func TestListProjectsSelectsAndBindsTheItemFieldsTheCardNames(t *testing.T) {
	// The five the card names, in the card's order.
	named := []string{"members", "members_version", "owner_user_id", "repos", "scenario"}

	bound := map[string]bool{}
	typ := reflect.TypeOf(Project{})
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name != "" && name != "-" {
			bound[name] = true
		}
	}
	if len(bound) < len(named) {
		t.Fatalf("domain.Project binds only %d json key(s) %v — fewer than the card names, so the "+
			"loop below would be reporting a broken reflection walk as a broken contract",
			len(bound), bound)
	}

	cols := strings.ToLower(projectSelectCols)
	for _, field := range named {
		if !bound[field] {
			t.Errorf("the pf_list_projects card says every item carries %q, and domain.Project binds "+
				"no such json key. handleListProjects marshals these structs directly, so a caller "+
				"reading the card would branch on a key that never arrives.", field)
		}
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(field) + `\b`).MatchString(cols) {
			t.Errorf("projectSelectCols does not select %q, so the field is marshalled at its zero "+
				"value: `members_version: 0` and `scenario: null` are legitimate values, which is "+
				"why a missing column here is silent rather than empty.", field)
		}
	}
}

// TestListProjectsScopesBySQLAndNeverRanksARole holds the pf_list_projects
// card's Open section: the scoping is a SQL predicate, `roleLevel` is not on
// this function's path, and `members` is reported rather than compared.
//
// It reads ListProjects out of the AST rather than grepping the file, so another
// function's use of RoleLevel in the same package cannot satisfy or break it.
// The admin branch is asserted too: it runs no predicate at all, and a sentence
// that described only the three-term one would be true of half the callers.
//
// MUTANTS:
//
//	M29 enforcement: rank the caller's role in ListProjects (`_ = RoleLevel[...]`)
//	                                            RED  the_function_ranks_no_role
//	M30 enforcement: drop the `members @>` term from the predicate
//	                                            RED  the_predicate_has_all_three_terms
//	M31 enforcement: give the admin branch a WHERE clause
//	                                            RED  the_admin_branch_has_no_predicate
//	P2  publication: make every citation in the pf_list_projects card unresolvable
//	                                            RED  K12
func TestListProjectsScopesBySQLAndNeverRanksARole(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "projects.go", nil, 0)
	if err != nil {
		t.Fatalf("parse projects.go: %v", err)
	}
	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if ok && fd.Recv == nil && fd.Name.Name == "ListProjects" {
			fn = fd
		}
	}
	if fn == nil {
		t.Fatal("projects.go declares no top-level ListProjects; the card names it as the function " +
			"that does the scoping, so a rename has to be reviewed rather than absorbed")
	}

	// Every SQL literal the function contains, which is where its scoping lives.
	var queries []string
	ast.Inspect(fn, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			queries = append(queries, strings.ToLower(lit.Value))
		}
		return true
	})
	if len(queries) < 2 {
		t.Fatalf("ListProjects contains %d string literal(s); the card describes two queries (one "+
			"per branch) and the assertions below would be about whichever one happened to survive",
			len(queries))
	}
	joined := strings.Join(queries, "\n")

	t.Run("the_predicate_has_all_three_terms", func(t *testing.T) {
		for _, term := range []string{"visible = true", "owner_user_id = $1", "members @>"} {
			if !strings.Contains(joined, term) {
				t.Errorf("no query in ListProjects carries %q. The card names public, owned and a "+
					"members containment test as the three terms of the scope; a missing term is a "+
					"caller who stops seeing projects they are in, with a 200 and a shorter list.",
					term)
			}
		}
	})

	t.Run("the_admin_branch_has_no_predicate", func(t *testing.T) {
		unscoped := false
		for _, q := range queries {
			if strings.Contains(q, "from projects") && !strings.Contains(q, "where") {
				unscoped = true
			}
		}
		if !unscoped {
			t.Error("every query in ListProjects carries a WHERE clause. The admin branch reads the " +
				"whole table, and the card says so — if that stopped being true the card is now " +
				"describing a scope that applies to nobody in particular.")
		}
	})

	t.Run("the_function_ranks_no_role", func(t *testing.T) {
		var ranked []string
		ast.Inspect(fn, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if ok && (id.Name == "RoleLevel" || id.Name == "roleLevel") {
				ranked = append(ranked, id.Name)
			}
			return true
		})
		if len(ranked) > 0 {
			t.Errorf("ListProjects now names %v. The card's Open section concludes that the aihub#443 "+
				"ladder inversion could never bite on this endpoint FOR A SIMPLER REASON THAN THE "+
				"MEASUREMENT — that no role is ranked on this path at all — and that conclusion is "+
				"what a ranking here withdraws. `members` is reported, not compared.", ranked)
		}
	})
}

// TestTheThreeRoleVocabulariesAreNotSupersetsOfOneAnother holds the pf_list_users
// card's "a THIRD vocabulary distinct from both the member roles and the project
// owner column".
//
// Every set is read from the code that refuses a value outside it —
// UserGlobalRoleList() for the column, validatedMemberRoles for the member
// roles — and the migrations behind them are held by
// TestUsersVocabulariesMatchTheMigrations and
// TestRoleLevel_LadderIsExactlyTheValidatedVocabulary. What this adds is the
// RELATION between them, which no arm held: the card's claim is not that either
// set has particular members but that neither contains the other, so a reader
// cannot treat one `role` as the other.
//
// MUTANTS:
//
//	M32 enforcement: accept `admin` as a member role, so the member vocabulary
//	    contains the global one                  RED  global_roles_are_not_member
//	                                                  _roles
//	M33 control:     give userGlobalRoles a `viewer` rung — more OVERLAP, still no
//	    containment                            GREEN  recorded rather than hidden:
//	                                                  this arm refuses containment,
//	                                                  not intersection, and
//	                                                  `writer` is already in both
//	P3  publication: make every citation in the pf_list_users card unresolvable
//	                                                  RED  K12
func TestTheThreeRoleVocabulariesAreNotSupersetsOfOneAnother(t *testing.T) {
	global := UserGlobalRoleList()
	member := validatedMemberRoles(t)
	if len(global) == 0 || len(member) == 0 {
		t.Fatalf("one of the vocabularies is empty (global=%v member=%v); a subset test against an "+
			"empty set is vacuously true in one direction and vacuously false in the other",
			global, member)
	}

	in := func(set []string, v string) bool {
		for _, s := range set {
			if s == v {
				return true
			}
		}
		return false
	}

	t.Run("global_roles_are_not_member_roles", func(t *testing.T) {
		outside := 0
		for _, g := range global {
			if !in(member, g) {
				outside++
			}
		}
		if outside == 0 {
			t.Errorf("every global role %v is also a member role %v, so users.role is no longer a "+
				"THIRD vocabulary — it is a subset of the second, and a card that keeps saying "+
				"otherwise invites validating one against the other", global, member)
		}
	})

	t.Run("member_roles_are_not_global_roles", func(t *testing.T) {
		outside := 0
		for _, m := range member {
			if !in(global, m) {
				outside++
			}
		}
		if outside == 0 {
			t.Errorf("every member role %v is also a global role %v; the card's `maintainer is not a "+
				"global one` is then false, and ValidateUserGlobalRole's own test case — which uses "+
				"maintainer as the realistic mistake — stops being one", member, global)
		}
	})

	t.Run("owner_is_in_neither", func(t *testing.T) {
		if in(global, "owner") || in(member, "owner") {
			t.Errorf("\"owner\" appears in a validated role vocabulary (global=%v member=%v). It is "+
				"the projects.owner_user_id column, and the third of the three the card counts is "+
				"the column rather than a set of strings.", global, member)
		}
	})
}

// TestEveryUserIdColumnReferencesTheRowsListUsersReturns holds the second clause
// of pf_list_users' §6.2 T2-18 row: "the identities they can name are the rows
// this tool returns".
//
// The enforcement is a foreign key. Every `*_user_id` column in the schema
// references users(id), which is the column this tool returns as each item's
// `id` (held by internal/server/list_users_response_shape_test.go's
// TestListUsersReturnsTheColumnsItSelectsAndCapsAtOneHundred), so the values a
// `user_id`-shaped parameter can carry are exactly the rows this tool lists.
//
// ⚠️ Its FIRST clause — that such parameters must SAY which identity they filter
// — is a ruling about other tools' published prose, and this arm does not hold
// it. K12 counts one row per sentence, so the citation retires the sentence; the
// limit is recorded here rather than left for a reader to discover, the same way
// aihub#543's wave 1 recorded that no ledger movement isolates one conjunct of a
// multi-claim bullet.
//
// MUTANTS:
//
//	M34 enforcement: drop `REFERENCES users(id)` from wi_watches.user_id  RED
//	M35 enforcement: point it at work_items(id) instead                   RED
//	P3  publication: make every citation in the pf_list_users card unresolvable
//	                                                                      RED  K12
func TestEveryUserIdColumnReferencesTheRowsListUsersReturns(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(migrationsDir, "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no migrations under %s", migrationsDir)
	}

	// A column definition or an ADD COLUMN whose name is user_id or ends in
	// _user_id, with the rest of its line — the FK, when there is one, is on it.
	colRE := regexp.MustCompile(`(?im)^\s*(?:add\s+column\s+)?([a-z][a-z0-9_]*_user_id|user_id)\s+(?:text|varchar)[^,\n]*`)
	fkRE := regexp.MustCompile(`(?i)references\s+users\s*\(\s*id\s*\)`)

	found := 0
	for _, f := range files {
		src, readErr := os.ReadFile(f)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, m := range colRE.FindAllStringSubmatch(stripSQLComments(string(src)), -1) {
			found++
			if !fkRE.MatchString(m[0]) {
				t.Errorf("%s declares %s with no `REFERENCES users(id)`:\n    %s\nThe pf_list_users "+
					"card says the identities a user_id-shaped parameter can name are the rows this "+
					"tool returns. Without the key that is a convention, and a value that names no "+
					"user row is then storable — which is how a filter comes to return nothing for "+
					"an id that looks valid.", filepath.Base(f), m[1], strings.TrimSpace(m[0]))
			}
		}
	}
	// Measured 2026-09-10: six such columns (work_items.reporter_user_id,
	// run_attempts.actor_user_id, agent_events.actor_user_id,
	// memories.author_user_id, projects.owner_user_id, wi_watches.user_id). The
	// floor is what fails when the scan stops recognising a column shape, which
	// is the way this arm would otherwise rot into a live-looking green.
	const floorUserIDColumns = 6
	if found < floorUserIDColumns {
		t.Errorf("the scan found %d user_id-shaped column(s), floor is %d — a walk that finds none "+
			"reports every schema as compliant", found, floorUserIDColumns)
	}
}

// TestForceTerminateStepFilesAFailedRowUnderForceTerminate holds the pf_get_step
// card's account of the one history entry a resuming agent cannot learn about
// any other way: the row fnForceTerminateStep writes when an attempt is paused
// over an `in_progress` step.
//
// WHEN it runs is held by complete_attempt_step_gate_test.go
// (TestTheStepInProgressRefusalIsGatedOnPausedOrTheFlagAlone, which reads the
// H-R9-11 gate out of FnCompleteAttempt). WHAT it writes had no arm at all, and
// the three values in the card's sentence are all in one SQL literal — status,
// error_type, and a step_id taken from `current_step` rather than from any
// request, which is the "naming a step nobody completed" half.
//
// ⚠️ This reads the statement, not a row. It cannot catch a database that
// refuses the INSERT, and it deliberately does not add a DB-gated function for
// that: no registered function drives this path today and a new one costs a
// gated_tests.txt line plus a ci.yml step (aihub#543 spec §3.3 rule 3).
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M36 enforcement: change the status literal to 'completed'   RED
//	M37 enforcement: change error_type to 'paused'              RED
//	M38 enforcement: pass a literal step name instead of *currentStep
//	                                                            RED
//	P4  publication: make every citation in the pf_get_step card unresolvable
//	                                                            RED  K12
func TestForceTerminateStepFilesAFailedRowUnderForceTerminate(t *testing.T) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, "run_attempts.go", nil, 0)
	if err != nil {
		t.Fatalf("parse run_attempts.go: %v", err)
	}
	var fn *ast.FuncDecl
	for _, decl := range parsed.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if ok && fd.Recv == nil && fd.Name.Name == "fnForceTerminateStep" {
			fn = fd
		}
	}
	if fn == nil {
		t.Fatal("run_attempts.go declares no fnForceTerminateStep. The pf_get_step card names it as " +
			"the writer of the entry a resuming agent cannot otherwise know about, so a rename is " +
			"a documentation decision rather than something this scan should absorb")
	}

	// The INSERT into the history table, with its VALUES row.
	var insert string
	var stepArg string
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		for i, arg := range call.Args {
			lit, ok := arg.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			v, uerr := strconv.Unquote(lit.Value)
			if uerr != nil || !strings.Contains(v, "INSERT INTO wi_step_completions") {
				continue
			}
			insert = v
			// The step_id positional argument, as the call really passes it.
			// $3 is step_id, and the query is arg i, so $3 is arg i+3.
			if len(call.Args) > i+3 {
				// exprText is embed_writer_parity_test.go's printer helper: an
				// ast.Inspect reassembly would emit a BinaryExpr's operator ahead
				// of its left operand and stop matching silently.
				stepArg = exprText(call.Args[i+3])
			}
		}
		return true
	})
	if insert == "" {
		t.Fatal("fnForceTerminateStep contains no INSERT INTO wi_step_completions; the row the card " +
			"describes is the whole subject of the sentence, and a function that no longer writes " +
			"one leaves the card promising an entry nothing files")
	}

	for _, want := range []string{"'failed'", "'force_terminate'"} {
		if !strings.Contains(insert, want) {
			t.Errorf("the force-terminate INSERT does not write %s:\n%s\nThe card publishes both "+
				"values, and pf_get_step's own description tells a resuming agent to redo any step "+
				"whose entry is not `completed`. A row written as completed would tell it the "+
				"opposite about a step nobody finished.", want, insert)
		}
	}
	if !strings.Contains(stepArg, "currentStep") {
		t.Errorf("the force-terminate INSERT files its step_id from %q rather than from the step "+
			"state's current_step. \"naming a step nobody completed\" is the point of the sentence: "+
			"the name has to be the step that was live, not a constant and not a request value — "+
			"there is no request here at all.", stepArg)
	}
}

// TestRecallResolvesTheWorkItemFilterBeforeComparingIt is the pf_recall half of
// the pf_get_step card's corrected slug sentence.
//
// The behavioural arm is internal/server/recall_work_item_slug_db_test.go
// (TestRecallResolvesWorkItemIdOrSlug), which drives the real router against a
// real Postgres for the reason its header gives: the FK that makes the empty
// page unavoidable lives in the database. This one holds the WIRING, which is
// the half aihub#363's own comment says is load-bearing — three call sites build
// a RecallRequest and the resolution sits at the single domain entry all three
// share, so an assertion about handleRecall alone would miss the /ui builders.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M39 enforcement: change Recall's guard to `if false`, leaving the resolver
//	    call in dead code                       RED (GREEN against the first
//	                                            version of this arm, which
//	                                            looked for the call alone — the
//	                                            reason it now reads the guard)
//	M40 enforcement: resolve and discard (`_ = resolved`)   RED
//	P4  publication: make every citation in the pf_get_step card unresolvable
//	                                                        RED  K12
func TestRecallResolvesTheWorkItemFilterBeforeComparingIt(t *testing.T) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, "memory.go", nil, 0)
	if err != nil {
		t.Fatalf("parse memory.go: %v", err)
	}
	var fn *ast.FuncDecl
	for _, decl := range parsed.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if ok && fd.Recv == nil && fd.Name.Name == "Recall" {
			fn = fd
		}
	}
	if fn == nil {
		t.Fatal("memory.go declares no top-level Recall")
	}

	// 🔴 The resolution has to be found as a REACHABLE guarded block, not as a call
	// anywhere in the function. Measured: a mutant that changed the guard to
	// `if false` left the resolver call sitting in dead code and an earlier
	// version of this arm — which looked for the call alone — stayed GREEN. So
	// the shape asserted is the one aihub#363 actually installed: an `if` whose
	// condition reads the request's own WorkItemID, whose body calls the resolver
	// AND assigns the result back onto the request.
	resolverRE := regexp.MustCompile(`(?i)resolve.*workitem`)
	resolvedAt, routedAt := token.NoPos, token.NoPos
	assignsFilter := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "recallRouted" && !routedAt.IsValid() {
				routedAt = call.Pos()
			}
			return true
		}
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		guarded := false
		ast.Inspect(ifStmt.Cond, func(c ast.Node) bool {
			if sel, ok := c.(*ast.SelectorExpr); ok && sel.Sel.Name == "WorkItemID" {
				guarded = true
			}
			return true
		})
		if !guarded {
			return true
		}
		calls, assigns := false, false
		ast.Inspect(ifStmt.Body, func(b ast.Node) bool {
			switch node := b.(type) {
			case *ast.CallExpr:
				if id, ok := node.Fun.(*ast.Ident); ok && resolverRE.MatchString(id.Name) {
					calls = true
				}
			case *ast.AssignStmt:
				for _, lhs := range node.Lhs {
					if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "WorkItemID" {
						assigns = true
					}
				}
			}
			return true
		})
		if calls && !resolvedAt.IsValid() {
			resolvedAt = ifStmt.Pos()
			assignsFilter = assigns
		}
		return true
	})

	if !resolvedAt.IsValid() {
		t.Fatal("Recall has no work-item resolution guarded on the request's own WorkItemID. " +
			"pf_get_step's card now tells callers that pf_recall takes either spelling; without a " +
			"reachable resolution a slug reaches `AND work_item_id = $N` against a column that " +
			"FK-references work_items(id), matches nothing, and answers 200 with an empty page — " +
			"which is what a work item with no memories answers too (aihub#363)")
	}
	if !assignsFilter {
		t.Error("Recall resolves the work-item reference and never assigns it back onto the " +
			"request, so the resolved value is discarded and every comparison below it still runs " +
			"against the caller's raw parameter")
	}
	if routedAt.IsValid() && resolvedAt > routedAt {
		t.Error("Recall resolves the work-item filter AFTER handing the request to recallRouted. " +
			"Order is the claim: aihub#127 -> aihub#343 -> aihub#357 -> aihub#363 is one class of " +
			"defect fixed by resolving before comparing, not by special-casing the comparison")
	}
	if !routedAt.IsValid() {
		t.Error("Recall no longer calls recallRouted, so the ordering assertion above cannot fail " +
			"however late the resolution happens — the floor for this arm")
	}
}

// TestNoSelectInThisRepoReadsAuthorAliases holds the pf_list_users card's
// account of `author_aliases`: the column is written and never read.
//
// It came out of correcting a sentence that called it "the field that matters
// most in this response" — it is not in that response at all
// (internal/server/list_users_response_shape_test.go), and the wider measurement
// is that no query anywhere in this repo selects it. What
// docs/design/polyforge-v1-design.md reserves it for — matching a git commit
// author to a user id — is therefore still a reservation, and a card that says
// the mapping happens is describing an intention.
//
// 🔴 The census is over SQL LITERALS rather than over the word: the column is
// mentioned in an MCP schema, in two request structs and in two write
// statements, and a check keyed on the identifier would report all five as
// readers. A SELECT is the shape a read takes here.
//
// Two floors, because either failure produces the same green: the walk has to
// find a real population of queries, and it has to find the column at least once
// — a scan that reaches no file and a scan that reaches every file except the
// ones touching this column are otherwise indistinguishable.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M41 enforcement: add author_aliases to handleListUsers' SELECT   RED
//	M42 control:     split the column name across two literals
//	    (`"… role, author_" + "aliases"`)                          GREEN — a
//	                 recorded LIMIT: a census over literals cannot see a name
//	                 assembled at run time, and saying so is cheaper than a
//	                 pattern that pretends otherwise
//	P8  publication: rename this arm                                 RED  K12
//	                                                                 ARM_CITATION
//	                                                                 _UNRESOLVED
func TestNoSelectInThisRepoReadsAuthorAliases(t *testing.T) {
	const column = "author_aliases"
	const repoRoot = "../.."

	queries, mentions := 0, 0
	var readers []string
	err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor", ".polyforge":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		parsed, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			// A file this walk cannot parse is a hole in the census, not a pass.
			t.Errorf("parse %s: %v", path, perr)
			return nil
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				// Raw string literals arrive already unquoted-ish; fall back to
				// the source text rather than skipping the biggest queries.
				v = lit.Value
			}
			upper := strings.ToUpper(v)
			hasSelect := strings.Contains(upper, "SELECT ")
			if hasSelect {
				queries++
			}
			if !strings.Contains(v, column) {
				return true
			}
			mentions++
			if hasSelect {
				readers = append(readers, filepath.ToSlash(path))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", repoRoot, err)
	}

	const floorQueries = 50
	if queries < floorQueries {
		t.Errorf("the census found %d SQL literal(s), floor is %d — a walk that reaches no queries "+
			"reports every column as unread", queries, floorQueries)
	}
	if mentions == 0 {
		t.Errorf("the census never saw %q in any SQL literal, so it cannot tell \"nothing reads it\" "+
			"from \"nothing mentions it\". The column is written by two statements in "+
			"internal/server/router.go; a scan that misses those is broken rather than reassuring",
			column)
	}
	if len(readers) > 0 {
		t.Errorf("%q is now SELECTed in %v. The pf_list_users card says no query in this repo reads "+
			"it, and draws the conclusion that the git-author mapping the design reserves it for is "+
			"still a reservation. If that has changed, the card's sentence is the thing to fix — and "+
			"if the new reader is this tool's own handler, the response shape moved too.",
			column, readers)
	}
}
