package domain

// aihub#463, the pure half: the Go vocabularies for the `users` table must equal
// the CHECK constraints that will actually refuse the value, and every
// CHECK-constrained users column a caller can supply must have a Go validator.
//
// Modelled on work_item_fields_test.go and deliberately a SECOND instance of it
// rather than an extension: aihub#411 §6.1 T1-4 rules that this policy is gated
// per DB CHECK, not per field, and a gate that only ever walked work_items would
// have said nothing about the users table — which is how these two columns
// stayed prose while the work-item ones were fixed.
//
// No database needed:
//
//	go test ./internal/domain/ -run 'Users' -v

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// goValidatedUserColumns names the CHECK-constrained users columns a caller can
// supply and that Go therefore refuses BEFORE the INSERT. The value is where the
// check lives, so "validated" is a claim about a specific function.
var goValidatedUserColumns = map[string]string{
	"user_type": "ValidateUserType (user_fields.go), called by handleCreateUser",
	"role":      "ValidateUserGlobalRole (user_fields.go), called by handleCreateUser",
}

// userColumnsNotCallerSupplied names CHECK-constrained users columns no caller
// can set. Empty today, and kept as a declared map rather than dropped: the
// alternative is that the first such column has nowhere to be recorded and gets
// added to the map above instead, which would state something false.
var userColumnsNotCallerSupplied = map[string]string{}

// usersStatements returns every CREATE/ALTER TABLE statement targeting `users`.
//
// Scoped to statements whose TABLE is users, not to a filename and not to
// "mentions users": many tables carry `REFERENCES users(id)`, and several of
// them have CHECKs of their own that would then be attributed here.
func usersStatements(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("read %s: %v", migrationsDir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, readErr := os.ReadFile(filepath.Join(migrationsDir, e.Name()))
		if readErr != nil {
			t.Fatalf("read %s: %v", e.Name(), readErr)
		}
		// Comments are stripped BEFORE the `;` split, because a `;` inside a
		// comment in a CREATE TABLE body cuts the statement in half and the
		// census silently shrinks (work_item_fields_test.go paid for this once).
		for _, stmt := range strings.Split(stripSQLComments(string(b)), ";") {
			m := stmtTableRE.FindStringSubmatch(stmt)
			if m == nil || strings.ToLower(m[1]) != "users" {
				continue
			}
			out = append(out, stmt)
		}
	}
	if len(out) == 0 {
		t.Fatalf("found no CREATE/ALTER TABLE users statement in %s — the walk is broken, and "+
			"every assertion built on it would be vacuous", migrationsDir)
	}
	return out
}

// usersColumns returns the column names declared for users.
func usersColumns(t *testing.T, stmts []string) map[string]bool {
	t.Helper()
	cols := map[string]bool{}
	colLine := regexp.MustCompile(`(?m)^\s+([a-z_][a-z0-9_]*)\s+[A-Za-z]`)
	addColumn := regexp.MustCompile(`(?is)ADD\s+COLUMN\s+(IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)`)
	for _, stmt := range stmts {
		for _, m := range colLine.FindAllStringSubmatch(stmt, -1) {
			cols[m[1]] = true
		}
		for _, m := range addColumn.FindAllStringSubmatch(stmt, -1) {
			cols[m[2]] = true
		}
	}
	// Sanity: a broken parse makes the census empty and every assertion below
	// vacuous. None of these can disappear without a migration.
	for _, want := range []string{"id", "email", "user_type", "role"} {
		if !cols[want] {
			t.Fatalf("users column census did not find %q among %v — the parse is broken, not "+
				"the schema", want, sortedSetMembers(cols))
		}
	}
	return cols
}

// checkedUserColumns is the census: which users columns are named inside a CHECK.
func checkedUserColumns(t *testing.T) map[string]bool {
	t.Helper()
	stmts := usersStatements(t)
	cols := usersColumns(t, stmts)

	checked := map[string]bool{}
	for _, stmt := range stmts {
		for _, expr := range checkExpressions(stmt) {
			// Strip string literals first: 'machine' would otherwise contribute
			// the identifier `machine`, and a value is not a column.
			bare := quotedRE.ReplaceAllString(expr, "''")
			for _, id := range identRe.FindAllString(strings.ToLower(bare), -1) {
				if cols[id] {
					checked[id] = true
				}
			}
		}
	}
	if len(checked) < 2 {
		t.Fatalf("only %d users column(s) look CHECK-constrained (%v) — the CHECK parse is "+
			"broken; 0001_initial.sql constrains user_type and role",
			len(checked), sortedSetMembers(checked))
	}
	t.Logf("CHECK-constrained users columns: %v", sortedSetMembers(checked))
	return checked
}

// TestEveryCheckedUserColumnIsValidatedInGo is the class gate for the users
// table. It FAILS on the pre-fix tree naming user_type and role: both
// CHECK-constrained, both caller-supplied through POST /v1/admin/users, and
// neither validated in Go.
//
// 🔴 What it is NOT sensitive to, so a green run is not over-read: it checks
// that a CHECK-constrained column has a validator RECORDED, not that the
// validator is CALLED. Deleting the two calls in handleCreateUser leaves this
// green — that half is TestCreateUser_IllegalVocabularyRejectedBeforeDB in
// internal/server, which drives the handler itself.
func TestEveryCheckedUserColumnIsValidatedInGo(t *testing.T) {
	checked := checkedUserColumns(t)

	for col := range checked {
		if _, ok := goValidatedUserColumns[col]; ok {
			continue
		}
		if _, ok := userColumnsNotCallerSupplied[col]; ok {
			continue
		}
		t.Errorf("users.%s carries a CHECK constraint and nothing in Go validates it, so an "+
			"illegal value reaches Postgres and comes back as a 500 — and handleCreateUser "+
			"discards the pgx error, so the answer is `500 INTERNAL_ERROR failed to create user`, "+
			"naming neither the field nor the legal values.\n"+
			"Either add a validator and record it in goValidatedUserColumns, or, if no caller can "+
			"supply this column, record it in userColumnsNotCallerSupplied with the reason. "+
			"(aihub#463)", col)
	}

	// Both maps must stay honest, or a stale entry reads as evidence the policy
	// is being applied to a column nothing constrains any more.
	for col, where := range goValidatedUserColumns {
		if !checked[col] {
			t.Errorf("goValidatedUserColumns claims users.%s is validated (%s), but no CHECK "+
				"constrains it any more — stale entry", col, where)
		}
	}
	for col, reason := range userColumnsNotCallerSupplied {
		if !checked[col] {
			t.Errorf("userColumnsNotCallerSupplied exempts users.%s (%s), but no CHECK constrains "+
				"it any more — stale exemption", col, reason)
		}
	}
}

// TestUsersVocabulariesMatchTheMigrations closes the drift between the Go sets
// and the SQL that will actually refuse the value.
//
// Parsing the migration is the point: a test written against the Go literals
// would pass while the database rejected a value the server accepted, which is
// the same 500 by a longer route.
func TestUsersVocabulariesMatchTheMigrations(t *testing.T) {
	stmts := usersStatements(t)

	for _, tc := range []struct {
		column string
		fromGo []string
	}{
		{column: "user_type", fromGo: UserTypeList()},
		{column: "role", fromGo: UserGlobalRoleList()},
	} {
		t.Run(tc.column, func(t *testing.T) {
			fromSQL := inClauseValues(t, stmts, tc.column)
			sort.Strings(fromSQL)
			got := append([]string(nil), tc.fromGo...)
			sort.Strings(got)
			if len(got) == 0 {
				t.Fatalf("the Go vocabulary for users.%s is empty — every comparison would be "+
					"satisfiable by an empty CHECK", tc.column)
			}
			if strings.Join(fromSQL, ",") != strings.Join(got, ",") {
				t.Errorf("users.%s vocabulary drifted:\n  SQL CHECK: %v\n  Go set:    %v",
					tc.column, fromSQL, got)
			}
		})
	}
}

// TestUserVocabularyErrorsCarryTheLegalValues pins what the 400 has to contain,
// which is the whole reason it beats the 500 it replaces: a caller that cannot
// read the legal values out of the answer guesses again.
func TestUserVocabularyErrorsCarryTheLegalValues(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     *AihubError
		field   string
		allowed []string
	}{
		{"user_type", ValidateUserType("bot"), "user_type", UserTypeList()},
		// `maintainer` is the realistic mistake, not a nonsense string: it is a
		// legal PROJECT MEMBER role and an illegal global one, so a caller that
		// conflates the two vocabularies sends exactly this.
		{"role", ValidateUserGlobalRole("maintainer"), "role", UserGlobalRoleList()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil {
				t.Fatalf("an out-of-vocabulary %s was accepted", tc.field)
			}
			if tc.err.HTTPStatus != 400 {
				t.Errorf("HTTPStatus = %d, want 400 — a 500 tells the caller to retry something "+
					"that can never succeed", tc.err.HTTPStatus)
			}
			if !strings.Contains(tc.err.Message, tc.field) {
				t.Errorf("the message does not name the field: %q", tc.err.Message)
			}
			for _, v := range tc.allowed {
				if !strings.Contains(tc.err.Message, v) {
					t.Errorf("the message omits the legal value %q: %q", v, tc.err.Message)
				}
			}
			details, ok := tc.err.Details.(map[string]any)
			if !ok {
				t.Fatalf("Details is %T, want map[string]any — an automated caller should not "+
					"have to parse prose to retry correctly", tc.err.Details)
			}
			if details["field"] != tc.field {
				t.Errorf("details[field] = %v, want %q", details["field"], tc.field)
			}
		})
	}

	// The other direction, or the two validators above would pass by refusing
	// everything — including the defaults handleCreateUser fills in.
	for _, v := range UserTypeList() {
		if err := ValidateUserType(v); err != nil {
			t.Errorf("ValidateUserType(%q) refused a value the CHECK accepts: %v", v, err)
		}
	}
	for _, v := range UserGlobalRoleList() {
		if err := ValidateUserGlobalRole(v); err != nil {
			t.Errorf("ValidateUserGlobalRole(%q) refused a value the CHECK accepts: %v", v, err)
		}
	}
}
