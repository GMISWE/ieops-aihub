package domain

// The anti-drift gate for WorkItemStatusValues (aihub#255).
//
// aihub#255 asked for the `?status=` allowlist to come from "the existing
// vocabulary, not a new copy written on the Go side, or the two will drift".
// A Go copy is unavoidable — the whole point is to reject the value BEFORE it
// reaches Postgres, so the check cannot be a query — which leaves exactly one
// honest option: keep the copy and make drift impossible to commit.
//
// So this reads the authority back. The authority is the CHECK constraint in
// internal/db/migrations, and it is read out of the migration SQL rather than
// out of a live database on purpose: a DB-gated test SKIPs when AIHUB_TEST_DB is
// unset while `go test` still prints ok and exits 0, so it would be worth
// nothing on the plain unit-test step — which is the step that runs on every
// push. This one runs there.
//
// 🔴 Every migration is scanned, not just 0002. A later ALTER that drops and
// re-adds the constraint with a different value set is precisely the drift this
// is for, and reading only the file that first defined it would miss it.
//
// Cross-checked once against a live database with all 32 migrations applied
// (2026-09-03): pg_get_constraintdef reported exactly
// ARRAY['queued','running','paused','blocked','wrapped','failed','cancelled'],
// so on that tree the SQL text and the executed schema agree. This test pins the
// text; that check confirmed the text is not lying.

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// statusCheckRE matches a `CHECK (status IN ('a','b',...))` clause in either a
// CREATE TABLE column definition or an ADD CONSTRAINT, and captures the value
// list. The column name may be quoted, and the `= ANY (ARRAY[...])` spelling
// Postgres normalises to is accepted too, as is a `status::text` cast.
var statusCheckRE = regexp.MustCompile(
	`(?is)CHECK\s*\(\s*"?status"?(?:::text)?\s+(?:IN|=\s*ANY)\s*\(?\s*(?:ARRAY\s*\[)?([^)\]]*)`)

// quotedRE pulls the single-quoted literals out of a captured value list.
var quotedRE = regexp.MustCompile(`'([^']*)'`)

// stmtTableRE captures the table a statement targets, from its head.
//
// 🔴 Scoping to the TABLE is not tidiness. `memories` also has a `status`
// column with its own CHECK (active/archived/redacted), and an unscoped
// "find a status CHECK" regex matched THAT one — from 0006, a later file than
// the 0002 that defines the real authority, so the gate confidently reported
// that work-item statuses were [active archived redacted]. Filtering by
// "the statement mentions work_items" would not have saved it either: the
// CREATE TABLE for memories mentions work_items in a foreign key.
//
// IF EXISTS / IF NOT EXISTS / ONLY and a quoted or schema-qualified name are all
// accepted, because each of them silently captured the WRONG WORD (or nothing)
// in an earlier draft — `ALTER TABLE IF EXISTS work_items` captured "if", so the
// statement was skipped as belonging to some other table and the migration
// became invisible.
var stmtTableRE = regexp.MustCompile(
	`(?is)^\s*(?:CREATE\s+TABLE(?:\s+IF\s+NOT\s+EXISTS)?|ALTER\s+TABLE(?:\s+IF\s+EXISTS)?)` +
		`\s+(?:ONLY\s+)?(?:"?public"?\.)?"?([a-z_]+)"?`)

// dropsStatusConstraintRE matches a DROP of the status CHECK, by the default
// constraint name or by any name on a statement that also drops a constraint.
var dropsStatusConstraintRE = regexp.MustCompile(`(?is)DROP\s+CONSTRAINT\s+(?:IF\s+EXISTS\s+)?"?([a-z_0-9]+)"?`)

// touchesStatusConstraintRE is the negative half: a work_items statement that
// touches the constraint at all must be one this test actually parsed, so a
// re-spelled constraint fails loudly instead of going silently unmatched — which
// is how a regex-based gate turns vacuous without anything going red.
var touchesStatusConstraintRE = regexp.MustCompile(`(?is)(work_items_status_check|CHECK\s*\(\s*"?status"?\b)`)

// stripSQLComments removes `--` line comments and `/* */` block comments,
// tracking single-quoted and dollar-quoted string state so a delimiter inside a
// literal is left alone.
//
// 🔴 Required before splitting on `;`, and found the hard way: 0002's line 15 is
//
//	-- M2: v1 only implements coding scenario; 'writing'/'data' reserved
//
// whose semicolon cut the CREATE TABLE for work_items in half, 30 lines above the
// status CHECK. The gate then reported "no work_items status CHECK found in any
// migration" — which at least failed loudly. Had the split landed the other side
// of the constraint it would have reported a partial vocabulary and passed only
// while the Go list happened to agree.
//
// $$ bodies are skipped whole because seven migrations contain them and an odd
// number of apostrophes inside one would invert the quote state for everything
// after it.
func stripSQLComments(sql string) string {
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(sql); i++ {
		if !inQuote && strings.HasPrefix(sql[i:], "$$") {
			if end := strings.Index(sql[i+2:], "$$"); end >= 0 {
				b.WriteString(" ")
				i += 2 + end + 1
				continue
			}
		}
		if sql[i] == '\'' {
			inQuote = !inQuote
		}
		if !inQuote && strings.HasPrefix(sql[i:], "--") {
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
			continue
		}
		if !inQuote && strings.HasPrefix(sql[i:], "/*") {
			if end := strings.Index(sql[i+2:], "*/"); end >= 0 {
				b.WriteString(" ")
				i += 2 + end + 1
				continue
			}
		}
		b.WriteByte(sql[i])
	}
	return b.String()
}

// statusVocabEvent is what one work_items statement did to the status CHECK.
type statusVocabEvent int

const (
	vocabNoop statusVocabEvent = iota
	vocabDefined
	vocabDropped
	vocabUnparsed
)

// applyStatusVocab folds one migration's statements over the vocabulary in
// force, returning the new value and whether anything was unparseable.
//
// 🔴 Folding, rather than "does this file contain a CHECK", is what makes the
// realistic migration work. The way a value gets added is
//
//	ALTER TABLE work_items DROP CONSTRAINT work_items_status_check;
//	ALTER TABLE work_items ADD  CONSTRAINT work_items_status_check CHECK (status IN (...));
//
// and an earlier draft flagged the DROP as "touched but unparseable" and failed
// the test even when the Go list had been updated correctly. That matters more
// than a missed spelling: the cheapest way to make a wrongly-red gate green is to
// loosen the check that reddened it, which would have deleted the negative half
// altogether. A DROP is now a first-class outcome — it CLEARS the vocabulary, so
// a migration that drops the constraint and does not re-add it correctly leaves
// nothing for the Go list to match.
func applyStatusVocab(inForce []string, sql string) (out []string, unparsed bool) {
	out = inForce
	for _, stmt := range strings.Split(stripSQLComments(sql), ";") {
		m := stmtTableRE.FindStringSubmatch(stmt)
		if m == nil || strings.ToLower(m[1]) != "work_items" {
			continue
		}
		switch ev, vals := classifyStatusStmt(stmt); ev {
		case vocabDefined:
			out = vals
		case vocabDropped:
			out = nil
		case vocabUnparsed:
			unparsed = true
		}
	}
	return out, unparsed
}

// classifyStatusStmt decides what a single work_items statement did.
func classifyStatusStmt(stmt string) (statusVocabEvent, []string) {
	if check := statusCheckRE.FindStringSubmatch(stmt); check != nil {
		var vals []string
		for _, q := range quotedRE.FindAllStringSubmatch(check[1], -1) {
			vals = append(vals, q[1])
		}
		if len(vals) > 0 {
			return vocabDefined, vals
		}
		return vocabUnparsed, nil
	}
	// A DROP counts only when it names the status constraint. A drop of some
	// other constraint on work_items is none of this test's business.
	for _, d := range dropsStatusConstraintRE.FindAllStringSubmatch(stmt, -1) {
		if strings.Contains(strings.ToLower(d[1]), "status") {
			return vocabDropped, nil
		}
	}
	if touchesStatusConstraintRE.MatchString(stmt) {
		return vocabUnparsed, nil
	}
	return vocabNoop, nil
}

func TestWorkItemStatusValuesMatchTheSchemaCheck(t *testing.T) {
	dir := filepath.Join("..", "db", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // 0001, 0002, ... — later files win
	if len(names) < 30 {
		// A path mistake would make this test pass over an empty set forever.
		t.Fatalf("found only %d migrations in %s — the walk is broken", len(names), dir)
	}

	// Fold every migration in order: the vocabulary in force after the last one
	// is the authority, exactly as it is for the database itself.
	var fromSchema []string
	var definedBy string
	touchedBy := []string{}
	for _, n := range names {
		body, readErr := os.ReadFile(filepath.Join(dir, n))
		if readErr != nil {
			t.Fatalf("read %s: %v", n, readErr)
		}
		next, unparsed := applyStatusVocab(fromSchema, string(body))
		if unparsed {
			touchedBy = append(touchedBy, n)
		}
		if !slices.Equal(next, fromSchema) {
			definedBy = n
		}
		fromSchema = next
	}

	if len(touchedBy) > 0 {
		t.Errorf("migrations %v mention the work_items status CHECK in a form this test could not "+
			"classify as either a definition or a drop. Update classifyStatusStmt to understand the "+
			"new spelling — an unparsed migration silently makes this gate vacuous.\n"+
			"Do NOT fix this by loosening touchesStatusConstraintRE: that is the cheapest edit that "+
			"turns the light off, and it deletes the only half of this test that notices a spelling "+
			"nobody anticipated.", touchedBy)
	}
	if fromSchema == nil {
		t.Fatal("no work_items status CHECK is in force after the last migration — either none defines " +
			"one (the gate is vacuous) or one dropped it without re-adding it, in which case the " +
			"database no longer constrains status and neither should WorkItemStatusValues")
	}
	t.Logf("authority: %s leaves %d status values in force", definedBy, len(fromSchema))

	got := WorkItemStatusValues()

	// Order first, because ORDER is what the caller sees: it is the order the
	// error message enumerates the legal values in, and it is asserted verbatim
	// by TestPolicyRule1_LegalValuesStillWork.
	if strings.Join(got, ",") != strings.Join(fromSchema, ",") {
		t.Errorf("WorkItemStatusValues() has drifted from %s.\n  Go:     %v\n  schema: %v\n"+
			"The database is the authority. If a status was added or removed in a migration, "+
			"update WorkItemStatusValues to match — /v1 and /ui both reject anything outside it "+
			"(aihub#255), so a missing entry is a filter callers can no longer use and an extra "+
			"one is a request the database will never satisfy.", definedBy, got, fromSchema)
	}
}

// TestStatusVocabParserUnderstandsRealMigrationSpellings is the gate ON the gate.
//
// TestWorkItemStatusValuesMatchTheSchemaCheck is a regex reading SQL, and a regex
// that stops matching does not go red — it goes QUIET, reporting whatever the
// last spelling it understood said, forever. Every case below was found by
// writing the spelling and running the parser, and each one silently produced the
// wrong answer (or none) against an earlier draft:
//
//	drop+add in one file   the shape of every real vocabulary change; the draft
//	                       flagged the DROP as unparseable and failed a correct
//	                       migration, whose cheapest fix would have been to
//	                       delete the check that failed
//	ALTER TABLE IF EXISTS  captured the word "if" as the table name, so the whole
//	                       statement was skipped as belonging to another table
//	"public"."work_items"  did not match at all
//	CHECK ("status" IN …)  did not match at all
//	block comment          a `;` inside /* */ split the statement in half
//
// The last two rows are the negative controls: a drop of some OTHER constraint
// must not clear the vocabulary, and an unrecognised spelling must be REPORTED
// rather than skipped — without them "understands everything" is satisfied by a
// parser that ignores everything.
func TestStatusVocabParserUnderstandsRealMigrationSpellings(t *testing.T) {
	base := []string{"queued", "running"}
	for _, tc := range []struct {
		name         string
		sql          string
		want         []string
		wantUnparsed bool
	}{
		{"drop+add in one file, the realistic migration", `
			ALTER TABLE work_items DROP CONSTRAINT work_items_status_check;
			ALTER TABLE work_items ADD CONSTRAINT work_items_status_check
			  CHECK (status IN ('queued','running','archived'));`,
			[]string{"queued", "running", "archived"}, false},
		{"ALTER TABLE IF EXISTS", `ALTER TABLE IF EXISTS work_items ADD CONSTRAINT c CHECK (status IN ('a','b'));`,
			[]string{"a", "b"}, false},
		{"schema-qualified and quoted table", `ALTER TABLE "public"."work_items" ADD CONSTRAINT c CHECK (status IN ('a'));`,
			[]string{"a"}, false},
		{"quoted column", `ALTER TABLE work_items ADD CONSTRAINT c CHECK ("status" IN ('a','b'));`,
			[]string{"a", "b"}, false},
		{"status::text cast", `ALTER TABLE work_items ADD CONSTRAINT c CHECK (status::text IN ('a'));`,
			[]string{"a"}, false},
		{"= ANY (ARRAY[...]), the form Postgres reports", `ALTER TABLE work_items ADD CONSTRAINT c CHECK (status = ANY (ARRAY['a','b']));`,
			[]string{"a", "b"}, false},
		{"block comment carrying a semicolon", `/* note; with a semicolon */ ALTER TABLE work_items ADD CONSTRAINT c CHECK (status IN ('a'));`,
			[]string{"a"}, false},
		{"drop with a non-default name still clears it", `ALTER TABLE work_items DROP CONSTRAINT wi_status_chk;`,
			nil, false},
		{"drop with the default name clears it", `ALTER TABLE work_items DROP CONSTRAINT work_items_status_check;`,
			nil, false},

		// Negative controls.
		{"dropping an unrelated constraint is a no-op", `ALTER TABLE work_items DROP CONSTRAINT work_items_goal_check;`,
			base, false},
		{"a status CHECK on another table is ignored", `ALTER TABLE memories ADD CONSTRAINT c CHECK (status IN ('active'));`,
			base, false},
		{"an unrecognised spelling is REPORTED, not skipped", `ALTER TABLE work_items ADD CONSTRAINT work_items_status_check CHECK (lower(status) SIMILAR TO 'x');`,
			base, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, unparsed := applyStatusVocab(base, tc.sql)
			if !slices.Equal(got, tc.want) {
				t.Errorf("vocabulary in force = %v, want %v", got, tc.want)
			}
			if unparsed != tc.wantUnparsed {
				t.Errorf("unparsed = %v, want %v", unparsed, tc.wantUnparsed)
			}
		})
	}
}

// TestNormalizeListWorkItemsLimit is the aihub#267 decision, stated as the
// behaviour rather than as the constants.
//
// 50 and 200 are spelled out rather than read off ListWorkItemsLimitDefault /
// Ceiling: a fixture derived from the constant under test moves with the defect
// instead of catching it, which is the rule memory.go already states for
// recallTopKDefault.
func TestNormalizeListWorkItemsLimit(t *testing.T) {
	for _, tc := range []struct {
		requested, want int
		why             string
	}{
		{0, 50, "no page size named -> the default"},
		{-5, 50, "a negative page size names nothing -> the default, never a smaller page"},
		{1, 1, "the smallest positive request is honoured verbatim"},
		{50, 50, "a request equal to the default is honoured"},
		{199, 199, "just under the ceiling is honoured verbatim"},
		{200, 200, "the ceiling itself is honoured, so the ceiling is reachable and therefore real"},
		// 🔴 THE aihub#267 CASE. This returned 50 before, which is BELOW what the
		// endpoint would have given for 200 — so asking for more got less.
		{201, 200, "one over the ceiling clamps to the ceiling, not back to the default"},
		{500, 200, "the value from the audit that produced a retracted conclusion"},
		{5000, 200, "far over the ceiling still clamps to the ceiling"},
	} {
		if got := NormalizeListWorkItemsLimit(tc.requested); got != tc.want {
			t.Errorf("NormalizeListWorkItemsLimit(%d) = %d, want %d — %s", tc.requested, got, tc.want, tc.why)
		}
	}

	// The invariant, asserted rather than assumed: a ceiling below the default
	// inverts the endpoint. Stated relationally so it survives either constant
	// being retuned.
	if NormalizeListWorkItemsLimit(1<<30) < NormalizeListWorkItemsLimit(0) {
		t.Errorf("an over-ceiling request (%d) returns fewer items than an unspecified one (%d): "+
			"asking for a bigger page gives a smaller one, which is aihub#309 on the work-item side",
			NormalizeListWorkItemsLimit(1<<30), NormalizeListWorkItemsLimit(0))
	}

	// And the symmetry with the recall side that aihub#267 exists to restore.
	// normalizeRecallTopK is unexported and in this package, so this is a direct
	// comparison of the two policies rather than an assertion about one of them.
	if got, want := NormalizeListWorkItemsLimit(300), 200; got != want {
		t.Errorf("work_items limit=300 -> %d", got)
	}
	if got, want := normalizeRecallTopK(300), 200; got != want {
		t.Errorf("recall top_k=300 -> %d", got)
	}
	if (NormalizeListWorkItemsLimit(300) == ListWorkItemsLimitCeiling) !=
		(normalizeRecallTopK(300) == recallTopKCeiling) {
		t.Error("the two page-size normalisers disagree about whether an over-ceiling request " +
			"clamps to the ceiling — that divergence IS aihub#267")
	}
}
