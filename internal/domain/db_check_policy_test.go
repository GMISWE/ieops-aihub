package domain

// aihub#434 — the repo-wide gate for aihub#396's policy row, from the aihub#411
// decision table §6.1 row T1-4.
//
// ─── The policy ────────────────────────────────────────────────────────────
//
//	A vocabulary or limit that a DB CHECK enforces is ALSO validated in Go and
//	answered 400 naming the field; the CHECK is the last line of defence, never
//	the caller-facing one.
//
// Recorded verbatim as the owner's decision on aihub#396 and adopted here as a
// property of the repository rather than of one table.
//
// ─── Why this gate enumerates CONSTRAINTS, not columns ─────────────────────
//
// aihub#396 shipped the policy AND a gate for it — but the gate
// (work_item_fields_test.go) censuses the CHECK-constrained columns of ONE
// table. That was the right size for that wi and it is the wrong size for the
// rule: at the time it landed the same policy already had three independent
// hand-written implementations — ValidateRequestedLocks (declared_resources.go),
// ValidateDeclaredResources, and the artifact_summary cap (aihub#390) — and
// nothing named them as instances of one thing. Three implementations of a rule
// nobody wrote down is the signature of a class that stays open; a fourth
// per-field fix produces a fifth.
//
// So the unit here is the CHECK CONSTRAINT, and the domain is the whole schema.
// The set is DERIVED from internal/db/migrations by replaying every Up section
// in order — CREATE TABLE, ADD CONSTRAINT, DROP CONSTRAINT, DROP COLUMN,
// DROP TABLE — so what it enumerates is the constraint set the schema carries at
// HEAD, not the union of every constraint that ever existed. A CHECK added
// tomorrow, on any table, fails this test until somebody says in writing what Go
// does about it.
//
// ─── What "has a corresponding Go validation" is allowed to mean ───────────
//
// Not a tick in a list. Five dispositions, and the first four are MECHANICALLY
// CHECKED against the migration text rather than asserted:
//
//	dispMirroredEnum   the Go set and the CHECK's IN-list must be EQUAL
//	dispMirroredLimit  the Go int and the CHECK's numeric bound must be EQUAL
//	dispMirroredRange  both ends of a BETWEEN must equal the Go bounds
//	dispMirroredRegexp the Go pattern and the CHECK's ~ operand must be EQUAL
//	dispMirroredByTest a NAMED DB-free test already does one of the above; it
//	                   must exist, read the migrations directory, and mention a
//	                   declared probe string
//	dispGuarded        Go refuses a SUPERSET of what the CHECK refuses, so no
//	                   illegal value can reach the column — reason required
//	dispServerWritten  no caller can supply the value — reason required
//
// The last two are the allowlist, and they are two rather than one because
// "nothing can reach it" and "nobody can send it" fail in different ways: the
// first breaks when a guard is relaxed, the second when a request struct gains a
// field. Each carries the sentence a reader needs to check it themselves.
//
// 🔴 WHAT A GREEN RUN HERE DOES NOT MEAN, stated so it is not over-read:
//
//   - It does not mean the validator is CALLED. This file proves the Go copy of
//     a vocabulary equals the SQL copy; it does not drive a request. Deleting
//     the call site in Remember leaves it green. That half is behavioural and
//     lives in db_check_policy_db_test.go and work_item_field_validation_db_test.go.
//   - It does not mean the answer is 400. dispGuarded records the cases where it
//     deliberately is not (scenario answers 405 NOT_IMPLEMENTED), with the
//     status written down.
//   - For dispMirroredByTest it means the named test exists and looks like what
//     it claims, not that the named test is correct. That test's own assertions
//     are its business; this registry's job is that no CHECK is unaccounted for.
//
//	go test ./internal/domain/ -run TestDBCheck -v -count=1

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ─── the effective CHECK set, derived from the migrations ───────────────────

// dbCheckFact is one CHECK the schema carries at HEAD.
type dbCheckFact struct {
	Table     string // the relation it is attached to
	Name      string // constraint name; SYNTHESISED for an anonymous CHECK
	Predicate string // the body between CHECK ( and its matching )
	Migration string // the migration whose Up section last defined it
	Synthetic bool   // true when Name was derived rather than read
}

func (f dbCheckFact) key() string { return f.Table + "." + f.Name }

// dbCheckAddRe finds the ADD clauses of an ALTER TABLE, with their offsets, so
// each one's own CHECK can be attributed to it rather than to the statement.
var dbCheckAddRe = regexp.MustCompile(
	`(?is)\bADD\s+(?:CONSTRAINT\s+"?([a-z_][a-z0-9_]*)"?|COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?"?([a-z_][a-z0-9_]*)"?)`)

// dbCheckDropConstraintRe / dbCheckDropColumnRe are the removal halves. Without
// them this enumeration is a union of history rather than a picture of HEAD:
// projects_scenario_check is dropped by 0019 and would otherwise still be
// demanded here, and scenario_phase_configs is dropped whole by 0017.
var dbCheckDropConstraintRe = regexp.MustCompile(`(?is)\bDROP\s+CONSTRAINT\s+(?:IF\s+EXISTS\s+)?"?([a-z_][a-z0-9_]*)"?`)
var dbCheckDropColumnRe = regexp.MustCompile(`(?is)\bDROP\s+COLUMN\s+(?:IF\s+EXISTS\s+)?"?([a-z_][a-z0-9_]*)"?`)
var dbCheckDropTableRe = regexp.MustCompile(`(?is)^\s*DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?"?([a-z_][a-z0-9_]*)"?`)

// dbCheckIdentRe finds candidate identifiers inside a predicate.
var dbCheckIdentRe = regexp.MustCompile(`[a-z_][a-z0-9_]*`)

// dbCheckConstraintNameRe finds `CONSTRAINT <name>` at the head of a table
// constraint inside a CREATE TABLE body.
var dbCheckConstraintNameRe = regexp.MustCompile(`(?is)^\s*CONSTRAINT\s+"?([a-z_][a-z0-9_]*)"?`)

// dbCheckTableConstraintHeadRe recognises the items in a CREATE TABLE body that
// are TABLE constraints rather than column definitions.
var dbCheckTableConstraintHeadRe = regexp.MustCompile(
	`(?is)^\s*(CHECK|PRIMARY\s+KEY|UNIQUE|FOREIGN\s+KEY|EXCLUDE|CONSTRAINT)\b`)

// dbCheckSplitTopLevel splits on sep at paren depth zero, outside single quotes.
// The CREATE TABLE bodies here contain both — `CHECK (priority IN ('low',
// 'normal', ...))` has commas at two nested depths and inside literals — so a
// plain Split would cut a constraint into pieces and attribute each piece to a
// different column.
func dbCheckSplitTopLevel(s string, sep byte) []string {
	var out []string
	depth, inQuote, start := 0, false, 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\'':
			inQuote = !inQuote
		case inQuote:
		case s[i] == '(':
			depth++
		case s[i] == ')':
			depth--
		case s[i] == sep && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// dbCheckParenBody returns the body of the first balanced parenthesis group in s.
func dbCheckParenBody(s string) (string, bool) {
	open := strings.IndexByte(s, '(')
	if open < 0 {
		return "", false
	}
	depth, inQuote := 0, false
	for i := open; i < len(s); i++ {
		switch {
		case s[i] == '\'':
			inQuote = !inQuote
		case inQuote:
		case s[i] == '(':
			depth++
		case s[i] == ')':
			depth--
			if depth == 0 {
				return s[open+1 : i], true
			}
		}
	}
	return "", false
}

// dbCheckNormaliseSpace collapses the runs of whitespace a multi-line predicate
// carries, so a re-indented constraint is the same fact.
func dbCheckNormaliseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// dbCheckReferencedColumns returns the DISTINCT columns of cols named by pred,
// in first-appearance order, with string literals removed first so
// `source IN ('sync_github')` does not report a column called sync_github.
func dbCheckReferencedColumns(pred string, cols []string) []string {
	known := map[string]bool{}
	for _, c := range cols {
		known[c] = true
	}
	bare := quotedRE.ReplaceAllString(pred, "''")
	seen := map[string]bool{}
	var out []string
	for _, id := range dbCheckIdentRe.FindAllString(strings.ToLower(bare), -1) {
		if known[id] && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// dbCheckSynthName reproduces the name Postgres gives an unnamed CHECK.
//
// ChooseConstraintName uses the column when the expression references EXACTLY
// ONE, and the bare table name otherwise — which is why
// `CHECK (length(artifact_summary) <= 4096)`, written as a table constraint on
// its own line, still lands as wi_step_completions_artifact_summary_check while
// `CHECK (blocked_wi_id != blocking_wi_id)` lands as wi_dependencies_check.
// Guessing this wrong would be invisible from the migrations alone, which is
// exactly why TestDBCheckRegistry_MatchesPgConstraint reads pg_constraint and
// compares.
func dbCheckSynthName(table string, refs []string) string {
	if len(refs) == 1 {
		return table + "_" + refs[0] + "_check"
	}
	return table + "_check"
}

// dbCheckFold applies one migration's Up section to the constraint set carried
// so far and returns it. Separated from effectiveDBChecks so
// TestDBCheckParser_HandlesTheShapesTheseMigrationsUse can drive it over inputs
// it controls: the directory walk and the anti-vacuity gates belong to the
// caller, and folding is the part that can silently stop understanding a shape.
func dbCheckFold(t *testing.T, up, file string, checks map[string]dbCheckFact) map[string]dbCheckFact {
	t.Helper()
	record := func(f dbCheckFact) {
		f.Predicate = dbCheckNormaliseSpace(f.Predicate)
		checks[f.key()] = f
	}

	for _, stmt := range dbCheckSplitTopLevel(up, ';') {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if m := dbCheckDropTableRe.FindStringSubmatch(stmt); m != nil {
			gone := strings.ToLower(m[1])
			for k, f := range checks {
				if f.Table == gone {
					delete(checks, k)
				}
			}
			continue
		}
		head := stmtTableRE.FindStringSubmatch(stmt)
		if head == nil {
			continue
		}
		table := strings.ToLower(head[1])
		upper := strings.ToUpper(strings.TrimSpace(stmt))

		switch {
		case strings.HasPrefix(upper, "CREATE TABLE"):
			// A partition inherits its parent's constraints and declares no
			// column list of its own; its first parenthesis is the FOR VALUES
			// range, which would be censused as a column list.
			if strings.Contains(upper, "PARTITION OF") {
				continue
			}
			body, ok := dbCheckParenBody(stmt)
			if !ok {
				continue
			}
			var cols, tableLevel []string
			for _, item := range dbCheckSplitTopLevel(body, ',') {
				if strings.TrimSpace(item) == "" {
					continue
				}
				if dbCheckTableConstraintHeadRe.MatchString(item) {
					if nm := dbCheckConstraintNameRe.FindStringSubmatch(item); nm != nil {
						for _, pred := range checkExpressions(item) {
							record(dbCheckFact{Table: table, Name: strings.ToLower(nm[1]),
								Predicate: pred, Migration: file})
						}
						continue
					}
					tableLevel = append(tableLevel, checkExpressions(item)...)
					continue
				}
				col := dbCheckIdentRe.FindString(strings.ToLower(item))
				if col == "" {
					continue
				}
				cols = append(cols, col)
				for _, pred := range checkExpressions(item) {
					record(dbCheckFact{Table: table, Name: dbCheckSynthName(table, []string{col}),
						Predicate: pred, Migration: file, Synthetic: true})
				}
			}
			for _, pred := range tableLevel {
				refs := dbCheckReferencedColumns(pred, cols)
				record(dbCheckFact{Table: table, Name: dbCheckSynthName(table, refs),
					Predicate: pred, Migration: file, Synthetic: true})
			}

		case strings.HasPrefix(upper, "ALTER TABLE"):
			for _, m := range dbCheckDropConstraintRe.FindAllStringSubmatch(stmt, -1) {
				delete(checks, table+"."+strings.ToLower(m[1]))
			}
			for _, m := range dbCheckDropColumnRe.FindAllStringSubmatch(stmt, -1) {
				delete(checks, table+"."+dbCheckSynthName(table, []string{strings.ToLower(m[1])}))
			}
			adds := dbCheckAddRe.FindAllStringSubmatchIndex(stmt, -1)
			for i, loc := range adds {
				end := len(stmt)
				if i+1 < len(adds) {
					end = adds[i+1][0]
				}
				clause := stmt[loc[0]:end]
				named := ""
				if loc[2] >= 0 {
					named = strings.ToLower(stmt[loc[2]:loc[3]])
				}
				column := ""
				if loc[4] >= 0 {
					column = strings.ToLower(stmt[loc[4]:loc[5]])
				}
				for _, pred := range checkExpressions(clause) {
					if named != "" {
						record(dbCheckFact{Table: table, Name: named, Predicate: pred, Migration: file})
						continue
					}
					if column == "" {
						t.Fatalf("%s: an ADD clause carries a CHECK (%s) but names neither a "+
							"constraint nor a column, so there is nothing to key it on. Synthesising "+
							"a name from the empty string would file it under %q and the DB arm "+
							"would report it as a phantom — which is a true failure with a "+
							"misleading cause, so it is caught here instead.",
							file, pred, table+"__check")
					}
					record(dbCheckFact{Table: table, Name: dbCheckSynthName(table, []string{column}),
						Predicate: pred, Migration: file, Synthetic: true})
				}
			}
		}
	}
	return checks
}

// effectiveDBChecks folds every migration's Up section into the CHECK set the
// schema holds at HEAD.
//
// Up sections ONLY. A Down section restores the PREVIOUS definition of whatever
// it touches — 0036's Down puts back the 21-name chk_evt_work_item_id, 0019's
// Down re-adds projects_scenario_check — so a parse that read the whole file
// would report a union of eras and would pass, or fail, for reasons that have
// nothing to do with the running schema.
func effectiveDBChecks(t *testing.T) map[string]dbCheckFact {
	t.Helper()

	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("read %s: %v", migrationsDir, err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	if len(files) < 30 {
		t.Fatalf("found %d migration(s) in %s — the directory walk is broken, and an empty history "+
			"would make every assertion below vacuous", len(files), migrationsDir)
	}

	checks := map[string]dbCheckFact{}
	for _, file := range files {
		checks = dbCheckFold(t, stripSQLComments(migrationUpSection(t, file)), file, checks)
	}

	// Anti-vacuity. An enumeration that silently found nothing would make the
	// registry test below pass by having nothing to check, and the two canaries
	// are one named CHECK and one SYNTHESISED name, so a regression in either
	// half of the parse is caught rather than only in the easy half.
	if len(checks) < 20 {
		t.Fatalf("enumerated only %d CHECK constraint(s) from %s — the PARSE is what broke, not the "+
			"schema; every assertion built on this would be vacuous. Found: %v",
			len(checks), migrationsDir, dbCheckKeys(checks))
	}
	for _, canary := range []string{
		"agent_events.chk_evt_work_item_id",     // named, installed by ALTER
		"work_items.work_items_priority_check",  // synthesised, column-level in CREATE TABLE
		"wi_dependencies.wi_dependencies_check", // synthesised, table-level, two columns
		"work_items.work_items_content_check",   // synthesised, ADD COLUMN ... CHECK
		"memories.memories_type_check",          // named, installed by a late ALTER
	} {
		if _, ok := checks[canary]; !ok {
			t.Fatalf("the enumeration did not find %s, which this schema certainly has — the parse no "+
				"longer matches how these migrations are written. Found: %v", canary, dbCheckKeys(checks))
		}
	}
	// The removal half has to be exercised too, or a parser that ignored every
	// DROP would look identical to this one on the arms above.
	for _, gone := range []string{
		"projects.projects_scenario_check",                             // dropped by 0019's Up
		"scenario_phase_configs.scenario_phase_configs_scenario_check", // table dropped by 0017
	} {
		if _, ok := checks[gone]; ok {
			t.Fatalf("%s is still in the enumeration, but a migration drops it — DROP CONSTRAINT / "+
				"DROP TABLE is not being applied, so this is a union of every era rather than HEAD", gone)
		}
	}
	return checks
}

func dbCheckKeys(m map[string]dbCheckFact) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ─── the registry ───────────────────────────────────────────────────────────

type dbCheckDisposition int

const (
	dispMirroredEnum dbCheckDisposition = iota
	dispMirroredLimit
	dispMirroredRange
	dispMirroredRegexp
	dispMirroredByTest
	dispGuarded
	dispServerWritten
)

// dbCheckPolicy is one row of the answer to "what does Go do about this CHECK".
type dbCheckPolicy struct {
	// Where the Go side lives, for a reader who wants to go and look. Required
	// for every disposition: an entry nobody can follow up is a tick in a list.
	Where string

	Disposition dbCheckDisposition

	// dispMirroredEnum: the Go set, and the column whose IN-list it mirrors.
	Column  string
	GoVocab []string

	// dispMirroredLimit / dispMirroredRange: the pattern that pulls the bound(s)
	// out of the predicate, and the Go value(s) they must equal.
	BoundExpr string
	GoLimit   int
	GoLow     int
	GoHigh    int

	// dispMirroredRegexp: the pattern the CHECK's ~ operand must equal.
	GoRegexp string

	// dispMirroredByTest: the DB-free test that already mirrors this constraint,
	// and a literal its file must contain.
	MirrorTest  string
	MirrorProbe string

	// Extra predicate fragments the CHECK must still contain. Used where a
	// constraint has a half the structured mirrors above cannot express, so that
	// half cannot quietly disappear.
	MustContain []string

	// dispGuarded / dispServerWritten: why no illegal value can arrive.
	Reason string
}

// dbCheckPolicies is the registry. Every CHECK the schema carries at HEAD must
// have exactly one entry, and every entry must name a CHECK the schema still
// carries; TestDBCheckRegistry_AccountsForEveryCheck asserts both directions.
//
// ⚠️ Adding a row here is the deliberate act. It is meant to cost a sentence of
// thought, because the alternative — a CHECK nobody decided about — is the
// 500 INTERNAL_ERROR this whole file exists to keep out of the API.
var dbCheckPolicies = map[string]dbCheckPolicy{
	"agent_events.chk_evt_work_item_id": {
		Where:       "domain.NullWorkItemEventTypes (event_types.go), refused in EmitEvent with a 400 naming event_type",
		Disposition: dispMirroredByTest,
		MirrorTest:  "TestEventTypes_NullWorkItemMirrorsTheMigration",
		MirrorProbe: "chk_evt_work_item_id",
	},

	"memories.memories_base_strength_check": {
		Where:       "validateBaseStrength (memory.go), via MinBaseStrength/MaxBaseStrength",
		Disposition: dispMirroredRange,
		BoundExpr:   `base_strength\s+BETWEEN\s+(\d+)\s+AND\s+(\d+)`,
		GoLow:       int(MinBaseStrength),
		GoHigh:      int(MaxBaseStrength),
	},
	"memories.memories_status_check": {
		Where:       "the INSERT in Remember hard-codes 'active'; gc.go and the redact/archive paths write the other two",
		Disposition: dispServerWritten,
		Reason: "server-controlled lifecycle. RememberRequest binds no `status` json tag, so no caller " +
			"can name one; every value the column ever holds is a literal in this package.",
	},
	"memories.memories_type_check": {
		Where:       "domain.MemoryTypePrefixes (memory.go), refused in Remember with a 400 naming type",
		Disposition: dispMirroredByTest,
		MirrorTest:  "TestMemoryTypeCheckMatchesTheGoPrefixes",
		MirrorProbe: "memories_type_check",
	},
	"memories.memories_visibility_check": {
		Where:       "validateMemoryVisibility (memory.go), called by Remember",
		Disposition: dispMirroredEnum,
		Column:      "visibility",
		GoVocab:     MemoryVisibilityList(),
	},

	"projects.projects_name_check": {
		Where:       "domain.projectNameRe (projects.go), refused in CreateProject with a 400 quoting the pattern",
		Disposition: dispMirroredRegexp,
		GoRegexp:    projectNameRe.String(),
	},

	"resource_locks.resource_locks_resource_type_check": {
		Where:       "domain.resourceLockTypes (declared_resources.go), via ValidateRequestedLocks",
		Disposition: dispMirroredEnum,
		Column:      "resource_type",
		GoVocab:     ResourceLockTypeList(),
	},

	"run_attempts.run_attempts_status_check": {
		Where:       "FnCompleteAttempt (run_attempts.go), `req.Status != \"wrapped\" && != \"failed\" && != \"paused\"`",
		Disposition: dispGuarded,
		Reason: "the caller-reachable set is a STRICT SUBSET of the CHECK's six: complete_attempt accepts " +
			"only wrapped/failed/paused and answers 400 otherwise, while 'running', 'superseded' and " +
			"'cancelled' are written by literals in this package (claim, takeover, CancelWorkItem). " +
			"Mirroring the full six in Go would WIDEN what a caller may send, which is the opposite of " +
			"what this policy asks for — so the guard is deliberately not the constraint.",
	},

	"users.users_role_check": {
		Where:       "domain.userGlobalRoles (user_fields.go), via ValidateUserGlobalRole",
		Disposition: dispMirroredEnum,
		Column:      "role",
		GoVocab:     UserGlobalRoleList(),
	},
	"users.users_user_type_check": {
		Where:       "domain.userTypes (user_fields.go), via ValidateUserType",
		Disposition: dispMirroredEnum,
		Column:      "user_type",
		GoVocab:     UserTypeList(),
	},

	"wi_dependencies.wi_dependencies_check": {
		Where:       "CreateDependency (dependencies.go): `req.BlockedWIID == req.BlockingWIID` -> 400",
		Disposition: dispGuarded,
		Reason: "this CHECK is a relation between two columns rather than a vocabulary or a limit, so " +
			"there is no set to mirror. Go refuses exactly the same rows and does it first, with a 400 " +
			"naming both fields.",
	},
	"wi_dependencies.wi_dependencies_kind_check": {
		Where:       "domain.dependencyKinds (dependencies.go), via validateDependencyKind",
		Disposition: dispMirroredEnum,
		Column:      "kind",
		GoVocab:     DependencyKindList(),
	},

	"wi_step_completions.wi_step_completions_artifact_summary_check": {
		Where:       "server.maxArtifactSummaryChars (routes_step.go), refused in handleUpdateStep with a 413",
		Disposition: dispMirroredByTest,
		MirrorTest:  "TestArtifactSummaryCapMatchesTheMigration",
		MirrorProbe: "artifact_summary",
	},
	"wi_step_completions.wi_step_completions_status_check": {
		Where:       "handleUpdateStep's `switch req.Status` (routes_step.go); insertStepCompletion is only ever passed a literal",
		Disposition: dispGuarded,
		Reason: "guarded by construction, not by a predicate. The two call sites pass the string " +
			"constants \"completed\" and \"failed\"; req.Status selects the branch and is never itself " +
			"written to the column, so no caller value can reach it. A third branch that forwarded " +
			"req.Status would break this and is what the reader should look for.",
	},

	"wi_step_state.wi_step_state_current_step_status_check": {
		Where:       "startStep and the completed/failed branches of handleUpdateStep write 'in_progress'/'idle' as literals",
		Disposition: dispServerWritten,
		Reason: "server-controlled step machine. No request struct binds this column; it is derived from " +
			"which transition ran.",
	},
	"wi_step_state.wi_step_state_graph_source_check": {
		Where:       "the wi_step_state upsert in run_attempts.go writes 'scenario_config' as a literal",
		Disposition: dispServerWritten,
		Reason: "server-controlled. 'scenario_default' has no writer in this tree at all; the column " +
			"records which source the server used, not anything a caller asked for.",
	},

	"work_items.work_items_content_check": {
		Where:       "validateWorkItemContent (work_item_fields.go), in both CreateWorkItem and UpdateWorkItem",
		Disposition: dispMirroredLimit,
		BoundExpr:   `length\(content\)\s*<=\s*(\d+)`,
		GoLimit:     maxWorkItemContentRunes,
		MustContain: []string{"content IS NULL"},
	},
	"work_items.work_items_external_share_type_check": {
		Where:       "the external-share handlers; neither CreateWorkItemRequest nor UpdateWorkItemRequest binds it",
		Disposition: dispServerWritten,
		Reason: "not caller-supplied: the value says which integration is being attached and is chosen by " +
			"the endpoint, not sent. TestUpdateWorkItemRequestBindsOnlyTheFieldsThisPathValidates fails " +
			"if the update struct ever grows the field.",
	},
	"work_items.work_items_goal_check": {
		Where:       "validateWorkItemGoalShape (work_item_fields.go), in both CreateWorkItem and UpdateWorkItem",
		Disposition: dispMirroredLimit,
		BoundExpr:   `length\(goal\)\s*<=\s*(\d+)`,
		GoLimit:     maxWorkItemGoalRunes,
		// The newline ban is the other half of the same CHECK and has no number
		// to compare, so it is pinned as text: without this, deleting `goal !~
		// E'[\n\r]'` from the migration would leave this entry green while the
		// Go guard silently became the only rule.
		MustContain: []string{"!~"},
	},
	"work_items.work_items_labels_check": {
		Where:       "validateWorkItemLabels (work_item_fields.go), in both CreateWorkItem and UpdateWorkItem",
		Disposition: dispMirroredLimit,
		BoundExpr:   `cardinality\(labels\)\s*<=\s*(\d+)`,
		GoLimit:     maxWorkItemLabels,
	},
	"work_items.work_items_priority_check": {
		Where:       "domain.workItemPriorities (work_item_fields.go), via validateWorkItemPriority",
		Disposition: dispMirroredEnum,
		Column:      "priority",
		GoVocab:     WorkItemPriorityList(),
	},
	"work_items.work_items_scenario_check": {
		Where:       "CreateWorkItem (work_items.go): `req.Scenario != \"coding\"` -> 405 NOT_IMPLEMENTED",
		Disposition: dispGuarded,
		Reason: "STRICTER than the CHECK, so no value can reach the constraint and the 500 is unreachable. " +
			"The answer is NOT_IMPLEMENTED (HTTP 405 — codeToHTTPStatus, errors.go), not 400, and that " +
			"is deliberate for a reserved-but-unbuilt scenario — whether an OUT-of-vocabulary scenario " +
			"deserves a 400 instead is a separate question this entry does not settle (carried over from " +
			"aihub#396's registry, whose entry said \"501\" — a status this code never returned; " +
			"measured and corrected 2026-09-10, aihub#592).",
	},
	"work_items.work_items_source_check": {
		Where:       "domain.workItemSources (work_item_fields.go), via validateWorkItemSource",
		Disposition: dispMirroredEnum,
		Column:      "source",
		GoVocab:     WorkItemSourceList(),
	},
	"work_items.work_items_status_check": {
		Where:       "domain.WorkItemStatusValues (work_items.go); /v1 and /ui reject a ?status= outside it",
		Disposition: dispMirroredEnum,
		Column:      "status",
		GoVocab:     WorkItemStatusValues(),
	},
}

// ─── the gate ───────────────────────────────────────────────────────────────

// TestDBCheckRegistry_AccountsForEveryCheck is the class gate: no CHECK in this
// schema may be undecided, and no decision may outlive the CHECK it describes.
//
// The second half is not symmetry for its own sake. A registry entry for a
// constraint that no longer exists reads to the next person as evidence that the
// policy is being applied, and it is the cheapest possible way for this file to
// become decoration.
func TestDBCheckRegistry_AccountsForEveryCheck(t *testing.T) {
	checks := effectiveDBChecks(t)

	for key, fact := range checks {
		policy, ok := dbCheckPolicies[key]
		if !ok {
			t.Errorf("%s carries a CHECK that nothing in Go is recorded as validating:\n"+
				"    %s\n"+
				"  installed by %s\n\n"+
				"Every non-class-40 SQLSTATE goes through dbErr/dbErrCause (internal/domain/pgx_err.go), "+
				"so if a caller can supply this value it comes back as 500 INTERNAL_ERROR carrying the "+
				"driver's constraint text — a status that tells the caller to retry something that can "+
				"never succeed, and that names none of the legal values to retry with.\n"+
				"Add an entry to dbCheckPolicies: a mirrored Go vocabulary/limit if a caller can supply "+
				"it, or dispGuarded / dispServerWritten WITH the reason. (aihub#434, policy from aihub#396)",
				key, fact.Predicate, fact.Migration)
			continue
		}
		if strings.TrimSpace(policy.Where) == "" {
			t.Errorf("dbCheckPolicies[%q] has no Where — an entry a reader cannot follow up is a tick "+
				"in a list, which is the state this registry replaced", key)
		}
		switch policy.Disposition {
		case dispGuarded, dispServerWritten:
			if strings.TrimSpace(policy.Reason) == "" {
				t.Errorf("dbCheckPolicies[%q] is on the allowlist with no reason. \"not caller-supplied\" "+
					"without one is indistinguishable from \"we forgot\"", key)
			}
		}
	}

	for key, policy := range dbCheckPolicies {
		if _, ok := checks[key]; !ok {
			t.Errorf("dbCheckPolicies claims something about %s (%s), but no CHECK by that name is in "+
				"the schema any more. Either the constraint was renamed — in which case the Go guard is "+
				"now unmirrored and this registry is lying about it — or it was dropped and this row "+
				"should go with it.", key, policy.Where)
		}
	}
}

// TestDBCheckRegistry_MirrorsMatchTheMigration is the half that makes
// "validated in Go" a measurement rather than a claim.
//
// The direction of a divergence decides which failure you get and both are bad,
// which is why every arm demands EQUALITY rather than containment:
//
//   - Go WIDER than the CHECK rebuilds the exact 500 this policy removes. The
//     request passes the Go guard, the write trips SQLSTATE 23514, and the
//     caller is handed the driver's text as a server fault.
//   - Go NARROWER is inert today, but it silently withdraws a capability the
//     schema still grants, and nothing else in the tree would ever say so.
func TestDBCheckRegistry_MirrorsMatchTheMigration(t *testing.T) {
	checks := effectiveDBChecks(t)
	mirrored := 0

	for key, policy := range dbCheckPolicies {
		fact, ok := checks[key]
		if !ok {
			continue // reported by the test above; nothing to compare against here
		}
		t.Run(key, func(t *testing.T) {
			for _, want := range policy.MustContain {
				if !strings.Contains(fact.Predicate, want) {
					t.Errorf("the CHECK no longer contains %q:\n    %s\n"+
						"That fragment is a half of this constraint with no number or set to compare, so "+
						"it is pinned as text; if it was deliberately removed, remove it from MustContain "+
						"in the same change and say why.", want, fact.Predicate)
				}
			}

			switch policy.Disposition {
			case dispMirroredEnum:
				mirrored++
				fromSQL := dbCheckInValues(t, fact.Predicate, policy.Column)
				if len(policy.GoVocab) == 0 {
					t.Fatalf("the Go vocabulary is empty, so equality with the CHECK could be satisfied "+
						"by a constraint that names nothing — %s", policy.Where)
				}
				if got, want := strings.Join(sortedCopy(fromSQL), ","), strings.Join(sortedCopy(policy.GoVocab), ","); got != want {
					t.Errorf("%s and %s name different sets:\n  CHECK: %v\n  Go:    %v\n"+
						"Go wider than the CHECK turns a 400 back into a 500 carrying the driver's text; "+
						"Go narrower withdraws a value the column still accepts.",
						key, policy.Where, sortedCopy(fromSQL), sortedCopy(policy.GoVocab))
				}

			case dispMirroredLimit:
				mirrored++
				got := dbCheckBounds(t, fact.Predicate, policy.BoundExpr, 1)
				if got[0] != policy.GoLimit {
					t.Errorf("%s caps at %d and %s uses %d", key, got[0], policy.Where, policy.GoLimit)
				}

			case dispMirroredRange:
				mirrored++
				got := dbCheckBounds(t, fact.Predicate, policy.BoundExpr, 2)
				if got[0] != policy.GoLow || got[1] != policy.GoHigh {
					t.Errorf("%s allows [%d, %d] and %s uses [%d, %d]",
						key, got[0], got[1], policy.Where, policy.GoLow, policy.GoHigh)
				}

			case dispMirroredRegexp:
				mirrored++
				pats := quotedRE.FindAllStringSubmatch(fact.Predicate, -1)
				if len(pats) != 1 {
					t.Fatalf("expected exactly one quoted pattern in %q, found %d — the predicate has "+
						"been rewritten in a form this comparison cannot read", fact.Predicate, len(pats))
				}
				if pats[0][1] != policy.GoRegexp {
					t.Errorf("%s enforces %q and %s compiles %q", key, pats[0][1], policy.Where, policy.GoRegexp)
				}

			case dispMirroredByTest:
				mirrored++
				dbCheckAssertMirrorTest(t, key, policy)
			}
		})
	}

	// Anti-vacuity for the loop itself: a registry whose entries all drifted to
	// dispGuarded would satisfy the test above and measure nothing here.
	if mirrored < 10 {
		t.Errorf("only %d registry entries are mechanically mirrored against the migrations. The "+
			"allowlist dispositions are prose; if they become the majority this gate has stopped "+
			"measuring anything and is only recording opinions", mirrored)
	}
}

// dbCheckInValues pulls the quoted values of `<column> IN (…)` out of a
// predicate.
func dbCheckInValues(t *testing.T, predicate, column string) []string {
	t.Helper()
	if column == "" {
		t.Fatal("dispMirroredEnum entry has no Column, so there is no IN-list to find")
	}
	re := regexp.MustCompile(`(?is)` + regexp.QuoteMeta(column) + `\s*(?:::text)?\s+IN\s*\(([^)]*)\)`)
	m := re.FindStringSubmatch(predicate)
	if m == nil {
		t.Fatalf("no `%s IN (…)` in %q — either the constraint was re-spelled (as `= ANY (ARRAY[…])`, "+
			"say) or the wrong column is named in the registry; a silently unmatched parse is how a "+
			"gate like this goes vacuous without anything going red", column, predicate)
	}
	var out []string
	for _, q := range quotedRE.FindAllStringSubmatch(m[1], -1) {
		out = append(out, q[1])
	}
	if len(out) == 0 {
		t.Fatalf("found the %s IN clause but no quoted values in it: %q", column, m[1])
	}
	return out
}

// dbCheckBounds pulls n integer capture groups out of a predicate.
func dbCheckBounds(t *testing.T, predicate, pattern string, n int) []int {
	t.Helper()
	if pattern == "" {
		t.Fatal("a mirrored-bound entry has no BoundExpr, so there is nothing to read out of the CHECK")
	}
	m := regexp.MustCompile(`(?is)` + pattern).FindStringSubmatch(predicate)
	if m == nil || len(m) != n+1 {
		t.Fatalf("BoundExpr %q did not match %q — the limit was re-spelled or dropped, and an "+
			"unmatched pattern would otherwise leave this entry checking nothing", pattern, predicate)
	}
	out := make([]int, n)
	for i := 1; i <= n; i++ {
		v := 0
		for _, c := range m[i] {
			v = v*10 + int(c-'0')
		}
		out[i-1] = v
	}
	return out
}

// dbCheckAssertMirrorTest verifies a dispMirroredByTest reference has not
// rotted: the named test must exist somewhere in the tree, its file must read
// the migrations directory, and it must mention the declared probe.
//
// Three conditions rather than one because each removes a different way for the
// reference to be true and useless. "The function exists" survives the test
// being gutted; "the file reads ../db/migrations" is what makes it a mirror
// rather than an assertion about Go literals; the probe is what ties it to THIS
// constraint rather than to some other one in the same file.
func dbCheckAssertMirrorTest(t *testing.T, key string, policy dbCheckPolicy) {
	t.Helper()
	if policy.MirrorTest == "" || policy.MirrorProbe == "" {
		t.Fatalf("dbCheckPolicies[%q] is dispMirroredByTest with no MirrorTest/MirrorProbe", key)
	}
	decl := "func " + policy.MirrorTest + "("
	var found string
	err := filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil || found != "" {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		src := string(raw)
		if !strings.Contains(src, decl) {
			return nil
		}
		found = path
		if !strings.Contains(src, "db/migrations") && !strings.Contains(src, "migrationsDir") {
			t.Errorf("%s names %s as the mirror for %s, but that file never reads the migrations "+
				"directory — it can only be asserting about Go literals, which is exactly the "+
				"drift this registry is supposed to make impossible", path, policy.MirrorTest, key)
		}
		if !strings.Contains(src, policy.MirrorProbe) {
			t.Errorf("%s contains %s but never mentions %q, so it is not visibly about %s — either "+
				"the probe is wrong or the reference points at the wrong test",
				path, policy.MirrorTest, policy.MirrorProbe, key)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree for %s: %v", policy.MirrorTest, err)
	}
	if found == "" {
		t.Errorf("dbCheckPolicies[%q] delegates to %s, and no such test function exists in this tree. "+
			"A delegation to a test that was renamed or deleted leaves this CHECK with no Go mirror at "+
			"all, and nothing else would say so.", key, policy.MirrorTest)
	}
}

// TestDBCheckParser_HandlesTheShapesTheseMigrationsUse is the anti-vacuity half
// of the enumeration, on inputs this file controls.
//
// effectiveDBChecks reads 36 real files; if its parse quietly stopped
// understanding one of the four shapes below, the registry test would report a
// missing constraint (loud) OR a phantom one (also loud) — but only for the
// constraints that happen to exist today. These cases pin the SHAPES, so a
// regression is attributed to the parser rather than to the schema.
func TestDBCheckParser_HandlesTheShapesTheseMigrationsUse(t *testing.T) {
	for _, tc := range []struct {
		name string
		sql  string
		want map[string]string // table.constraint -> normalised predicate
	}{
		{
			name: "column level check is named after its column",
			sql:  "CREATE TABLE t (\n  a TEXT NOT NULL DEFAULT 'x' CHECK (a IN ('x', 'y')),\n  b INT\n);",
			want: map[string]string{"t.t_a_check": "a IN ('x', 'y')"},
		},
		{
			name: "table level check over one column is named after that column",
			sql:  "CREATE TABLE t (\n  a TEXT,\n  b INT,\n  CHECK (length(a) <= 10)\n);",
			want: map[string]string{"t.t_a_check": "length(a) <= 10"},
		},
		{
			name: "table level check over two columns is named after the table",
			sql:  "CREATE TABLE t (\n  a TEXT,\n  b TEXT,\n  CHECK (a != b)\n);",
			want: map[string]string{"t.t_check": "a != b"},
		},
		{
			name: "a named table constraint keeps its name",
			sql:  "CREATE TABLE t (\n  a TEXT,\n  CONSTRAINT chk_a CHECK (a IS NOT NULL)\n);",
			want: map[string]string{"t.chk_a": "a IS NOT NULL"},
		},
		{
			name: "ADD COLUMN carries a column level check",
			sql:  "CREATE TABLE t (a TEXT);\nALTER TABLE t ADD COLUMN b TEXT CHECK (b IS NULL OR length(b) <= 5);",
			want: map[string]string{"t.t_b_check": "b IS NULL OR length(b) <= 5"},
		},
		{
			name: "a later DROP CONSTRAINT removes it",
			sql: "CREATE TABLE t (a TEXT);\nALTER TABLE t ADD CONSTRAINT chk_a CHECK (a <> '');\n" +
				"ALTER TABLE t DROP CONSTRAINT IF EXISTS chk_a;",
			want: map[string]string{},
		},
		{
			name: "a re-ADD after a DROP wins, and only the last definition survives",
			sql: "CREATE TABLE t (a TEXT);\nALTER TABLE t ADD CONSTRAINT chk_a CHECK (a IN ('old'));\n" +
				"ALTER TABLE t DROP CONSTRAINT chk_a;\nALTER TABLE t ADD CONSTRAINT chk_a CHECK (a IN ('new'));",
			want: map[string]string{"t.chk_a": "a IN ('new')"},
		},
		{
			name: "DROP TABLE takes its constraints with it",
			sql:  "CREATE TABLE t (a TEXT CHECK (a <> ''));\nDROP TABLE IF EXISTS t CASCADE;",
			want: map[string]string{},
		},
		{
			name: "a partition declares no columns of its own",
			sql: "CREATE TABLE p (a TEXT CHECK (a <> ''), created_at TIMESTAMPTZ) PARTITION BY RANGE (created_at);\n" +
				"CREATE TABLE p_2026_01 PARTITION OF p FOR VALUES FROM ('2026-01-01') TO ('2026-02-01');",
			want: map[string]string{"p.p_a_check": "a <> ''"},
		},
		{
			name: "a semicolon inside a comment does not cut the statement",
			sql:  "CREATE TABLE t (\n  -- reserved; see the design note\n  a TEXT CHECK (a IN ('x'))\n);",
			want: map[string]string{"t.t_a_check": "a IN ('x')"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := dbCheckParseForTest(t, tc.sql)
			if len(got) != len(tc.want) {
				t.Fatalf("parsed %v, want %v", got, tc.want)
			}
			for k, wantPred := range tc.want {
				gotPred, ok := got[k]
				if !ok {
					t.Fatalf("did not find %s; parsed %v", k, got)
				}
				if gotPred != wantPred {
					t.Errorf("%s predicate = %q, want %q", k, gotPred, wantPred)
				}
			}
		})
	}
}

// dbCheckParseForTest runs the same fold effectiveDBChecks runs, over one
// in-memory Up section.
//
// It is a deliberate second entry point rather than a refactor of
// effectiveDBChecks into a pure function taking a []string: that function would
// still need the directory walk, the ordering and the anti-vacuity gates around
// it, and the shape cases above are about the fold, which is what this shares.
// Keeping the walk out of it is what lets the cases assert an EXACT set.
func dbCheckParseForTest(t *testing.T, sql string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	name := "0001_shapes.sql"
	body := "-- +goose Up\n" + sql + "\n\n-- +goose Down\n-- deliberately not the same as Up: the fold must ignore this\nALTER TABLE t ADD CONSTRAINT chk_down CHECK (a <> 'down');\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	out := map[string]string{}
	up := stripSQLComments(dbCheckUpSection(t, filepath.Join(dir, name)))
	folded := dbCheckFold(t, up, name, map[string]dbCheckFact{})
	for k, f := range folded {
		out[k] = f.Predicate
	}
	return out
}

func dbCheckUpSection(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	up := strings.Index(sql, "-- +goose Up")
	down := strings.Index(sql, "-- +goose Down")
	if up < 0 || down <= up {
		t.Fatalf("fixture %s has no Up/Down markers", path)
	}
	return sql[up+len("-- +goose Up") : down]
}
