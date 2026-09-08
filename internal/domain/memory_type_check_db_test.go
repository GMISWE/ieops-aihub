package domain

// aihub#445, the behavioural half (aihub#411 decision table §6.2 T2-6): what the
// memories_type_check CHECK does to a real Postgres, and how it lines up with
// the Go check that has always guarded the same column.
//
// The pure half is memory_type_check_test.go, which parses the migration and
// proves the two predicates NAME the same set. That is not the same claim as
// the two BEHAVING the same way — starts_with is not strings.HasPrefix by
// definition, it is by Postgres's definition — so these run against a database.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	  go test ./internal/domain/ -run 'TestMemoryTypeCheckDB|TestMigration0034' -v -count=1

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/citest/testname"
)

// typeCheckConstraint reports whether memories_type_check exists, whether it
// has been validated against the rows already in the table, and the census the
// migration wrote into its COMMENT.
func typeCheckConstraint(t *testing.T, pool *pgxpool.Pool) (exists, validated bool, comment string) {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT convalidated, coalesce(obj_description(oid, 'pg_constraint'), '')
		  FROM pg_constraint
		 WHERE conrelid = 'memories'::regclass AND conname = 'memories_type_check'`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		require.NoError(t, rows.Scan(&validated, &comment))
		exists = true
	}
	require.NoError(t, rows.Err())
	return exists, validated, comment
}

// insertRawMemory writes a memory row with the given type through plain SQL,
// bypassing Remember entirely. That bypass is the point: the column is being
// asked what IT refuses, independently of what Go refuses first.
func insertRawMemory(t *testing.T, pool *pgxpool.Pool, id, project, userID, memType string) error {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO memories(id, project, author_user_id, type, content) VALUES ($1,$2,$3,$4,'raw seed')`,
		id, project, userID, memType)
	return err
}

// TestMemoryTypeCheckDB_ColumnAndGoAgreeOnEveryType is the equivalence arm, and
// the asymmetry in what it demands is deliberate.
//
// Go accepting a type the COLUMN refuses is the defect this whole change must
// not introduce: the write reaches Postgres, comes back as SQLSTATE 23514, and
// the caller is handed a 500 carrying the driver's constraint text instead of a
// 400 naming the field — aihub#433's failure mode, manufactured fresh. The
// reverse (column wider than Go) is inert, because Go refuses first and the
// value never reaches the column.
//
// It is asserted as equality anyway, in both directions, because the wider case
// is not merely harmless — it is evidence the CHECK stopped being the mirror
// this migration claims it is, and the next person to widen Go would be widening
// it into a column that had quietly drifted.
func TestMemoryTypeCheckDB_ColumnAndGoAgreeOnEveryType(t *testing.T) {
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)
	project := testProject(t, pool, uid)
	ctx := context.Background()

	exists, _, _ := typeCheckConstraint(t, pool)
	require.True(t, exists,
		"memories_type_check is not on this database — apply migration 0034 before running this "+
			"suite; without it every assertion below passes for the wrong reason")

	cases := []struct {
		memType string
		legal   bool
		why     string
	}{
		{"experience.debug", true, "on the curated list"},
		{"fact.note", true, "on the curated list"},
		{"rule.work", true, "on the curated list"},
		{"methodology.spec", true, "legal for the column; refused only by pf_remember, one layer up"},
		{"experience.whatever_a_caller_invents", true, "off every list, legal prefix — the decided leniency"},
		{"experience.", true, "the prefix alone is a prefix of itself, in Go and in starts_with alike"},
		{"nonsense.thing", false, "no legal prefix"},
		{"", false, "no legal prefix"},
		{"Experience.debug", false, "prefix matching is case-sensitive on both sides"},
		{"experienceX.debug", false, "the dot is part of the prefix"},
		{" experience.debug", false, "a leading space is not trimmed by either side"},
		{"experience.pitfall|rule.work", false, "legal prefix, but '|' is banned (aihub#289)"},
		{"experience.*|rule.*", false, "the exact shape aihub#289 was filed for"},
	}

	// Anti-vacuity: a table that is all-legal would be satisfied by a column
	// that refuses nothing, and a table that is all-illegal by one that refuses
	// everything. Both floors have to hold for the agreement to mean anything.
	var legalCount, illegalCount int
	for _, tc := range cases {
		if tc.legal {
			legalCount++
		} else {
			illegalCount++
		}
	}
	require.Greater(t, legalCount, 2, "too few accepted types to detect an over-strict column")
	require.Greater(t, illegalCount, 2, "too few refused types to detect a column that refuses nothing")

	for _, tc := range cases {
		id := "mem_tc_" + testname.Sanitize(t.Name()) + "_" + testname.Sanitize(tc.memType)

		columnErr := insertRawMemory(t, pool, id, project, uid, tc.memType)
		if tc.legal {
			require.NoError(t, columnErr,
				"the column refused %q (%s), which Go accepts — a caller sending it now gets a 500 "+
					"carrying the constraint text instead of storing a memory", tc.memType, tc.why)
		} else {
			require.Error(t, columnErr,
				"the column accepted %q (%s); memories_type_check is not enforcing what the "+
					"migration says it enforces", tc.memType, tc.why)
			require.Contains(t, columnErr.Error(), "memories_type_check",
				"%q was refused by something other than memories_type_check, so this case proves "+
					"nothing about the constraint", tc.memType)
		}

		_, _, goErr := Remember(ctx, pool, &RememberRequest{
			Project:       project,
			Type:          tc.memType,
			Content:       "aihub#445 agreement probe for type " + tc.memType,
			Visibility:    "project",
			DedupMode:     "off",
			CallerUserID:  uid,
			CallerDisplay: uid,
		})
		if tc.legal {
			require.Nil(t, goErr, "Go refused %q (%s), which the column accepts", tc.memType, tc.why)
			continue
		}
		require.NotNil(t, goErr, "Go accepted %q (%s), which the column refuses — the 500 this "+
			"change exists to prevent", tc.memType, tc.why)

		// And it must be refused as a 400 naming the type, not as whatever the
		// driver made of a constraint violation. This is the half that says the
		// CHECK is the last line of defence rather than the caller-facing one
		// (the policy in work_item_fields.go).
		var ae *AihubError
		require.ErrorAs(t, goErr, &ae)
		require.Equal(t, ErrInvalidMemoryType, ae.Code,
			"%q came back as %s, so Postgres — not Go — is answering the caller", tc.memType, ae.Code)
		require.NotContains(t, ae.Message, "memories_type_check",
			"the 400 for %q quotes the constraint name, which means it was built from the driver "+
				"error rather than refused before the query", tc.memType)
	}
}

// TestMemoryTypeCheckDB_OffListTypeStaysStorableEndToEnd states the leniency as
// a property, on its own, because it is the thing most likely to be "fixed"
// by mistake.
//
// The temptation is to point the CHECK at MemoryTypeEnum's 19 names, or at
// PfRememberTypeEnum's 13, and call the vocabulary tidy. aihub#411 §6.2 T2-6
// decided the opposite: three of the four vocabularies were deliberate and only
// the DB was missing, so the constraint enforces the PREFIX and nothing finer.
// This test fails the moment someone narrows it, and the failure names the
// ruling rather than a diff.
func TestMemoryTypeCheckDB_OffListTypeStaysStorableEndToEnd(t *testing.T) {
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)
	project := testProject(t, pool, uid)

	// Without this the whole test is satisfied by a database that has no
	// constraint at all — which is not a hypothetical: adding a narrowed CHECK
	// to a populated table fails its own validation scan and leaves the column
	// bare, so the most likely way to break the leniency is also the way that
	// makes this test green.
	exists, _, _ := typeCheckConstraint(t, pool)
	require.True(t, exists,
		"memories_type_check is not on this database — apply migration 0034 first; without it this "+
			"test proves nothing about what the column permits")

	const offList = "rule.a_name_no_curated_list_contains"

	// The premise, asserted rather than assumed: if this type ever joins a
	// curated list the test below stops testing leniency and starts testing
	// nothing.
	for _, curated := range MemoryTypeEnum {
		require.NotEqual(t, curated, offList, "the specimen is on the UI select list")
	}
	for _, curated := range PfRememberTypeEnum {
		require.NotEqual(t, curated, offList, "the specimen is on the pf_remember suggestion list")
	}

	m, _, aerr := Remember(context.Background(), pool, &RememberRequest{
		Project:       project,
		Type:          offList,
		Content:       "aihub#445: an off-list type carrying a legal prefix is stored, by decision",
		Visibility:    "project",
		DedupMode:     "off",
		CallerUserID:  uid,
		CallerDisplay: uid,
	})
	require.Nil(t, aerr,
		"an off-list type with a legal prefix was refused. If memories_type_check was narrowed to a "+
			"curated list, that reverses aihub#411 §6.2 T2-6, which keeps the leniency deliberately")
	require.Equal(t, offList, m.Type, "the type was stored as something other than what was sent")
}

// TestMigration0034_SelfValidatesOnACleanTable is the arm that says the NOT
// VALID is a floor and not a ceiling: on a database with nothing to object to,
// the migration ends with the constraint VALIDATED, covering rows that already
// existed as well as every future write. Without this, "added NOT VALID" would
// be indistinguishable from "never validated anywhere".
func TestMigration0034_SelfValidatesOnACleanTable(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	var outliers int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM memories
		 WHERE NOT (starts_with(type,'experience.') OR starts_with(type,'fact.')
		         OR starts_with(type,'rule.')       OR starts_with(type,'methodology.'))
		    OR strpos(type,'|') > 0`).Scan(&outliers))
	require.Zero(t, outliers,
		"this database already holds %d off-vocabulary memories, so the clean-table branch cannot be "+
			"exercised here. That is a fact about the fixture, not about the migration — find them with "+
			"the census SELECT quoted in 0034_memories_type_check.sql", outliers)

	// Down, then Up: the same round trip TestMigration0027_UpDown makes, and the
	// reason the Up section opens with DROP CONSTRAINT IF EXISTS.
	mustExec(t, pool, `ALTER TABLE memories DROP CONSTRAINT IF EXISTS memories_type_check`)
	exists, _, _ := typeCheckConstraint(t, pool)
	require.False(t, exists, "Down: the constraint must be gone")

	runMigration(t, pool, "0034_memories_type_check.sql")

	exists, validated, comment := typeCheckConstraint(t, pool)
	require.True(t, exists, "Up: the constraint must be restored")
	require.True(t, validated,
		"the constraint came back NOT VALID on a table with no outliers, so the DO block's census "+
			"disagrees with the constraint it guards and existing rows are covered by nothing")
	require.Contains(t, comment, "VALIDATED at migration time",
		"the constraint carries no census comment; on a real deploy that comment is the ONLY surviving "+
			"record of what the migration measured, because goose discards RAISE output")

	// Re-running Up must be a no-op rather than an error: the Up section is
	// applied by hand often enough (and by this suite twice) that a
	// non-idempotent one would be a trap.
	runMigration(t, pool, "0034_memories_type_check.sql")
	_, validated, _ = typeCheckConstraint(t, pool)
	require.True(t, validated, "a second Up left the constraint unvalidated")
}

// TestMigration0034_StillAppliesWhenTheTableAlreadyHoldsAnOutlier is the
// MIGRATION CAUTION arm, and it is the one that justifies the whole NOT VALID
// design.
//
// A plain validated CHECK scans the table and aborts the migration on the first
// off-prefix row. The live distinct type set on production has never been read
// (aihub#411 §6.4 item 4), so writing one would have been a bet that the data is
// clean — and losing it means the deploy fails and the column stays unguarded
// while somebody decides what an unmappable historical type should become. This
// test constructs exactly the losing case and requires the migration to survive
// it, guard every future write anyway, and leave the evidence behind.
func TestMigration0034_StillAppliesWhenTheTableAlreadyHoldsAnOutlier(t *testing.T) {
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)
	project := testProject(t, pool, uid)

	// Whatever happens below, the shared database must be handed back with the
	// constraint validated and no outlier left in it — the next test asserts a
	// clean table, and a leaked row would fail it somewhere else entirely.
	t.Cleanup(func() {
		mustExec(t, pool, `ALTER TABLE memories DROP CONSTRAINT IF EXISTS memories_type_check`)
		mustExec(t, pool, `DELETE FROM memories WHERE id IN ('mem_445_outlier', 'mem_445_piped')`)
		runMigration(t, pool, "0034_memories_type_check.sql")
	})

	mustExec(t, pool, `ALTER TABLE memories DROP CONSTRAINT IF EXISTS memories_type_check`)
	require.NoError(t, insertRawMemory(t, pool, "mem_445_outlier", project, uid, "legacy_freeform"))
	require.NoError(t, insertRawMemory(t, pool, "mem_445_piped", project, uid, "experience.a|rule.b"))

	// The migration must not fail. runMigration require.NoErrors the Exec, so a
	// validated CHECK here would fail the test rather than skip past it.
	runMigration(t, pool, "0034_memories_type_check.sql")

	exists, validated, comment := typeCheckConstraint(t, pool)
	require.True(t, exists, "the constraint was not added to a table holding outliers")
	require.False(t, validated,
		"the constraint reports itself validated over a table that holds two rows violating it — "+
			"which Postgres would not allow, so the census must be looking at a different predicate")

	// The census is the measurement §6.4 item 4 asked for. It has to name every
	// distinct offending type, including the piped one: that row passes the
	// prefix half and is caught only by the '|' clause, so a census missing it
	// would report a table clean that the constraint refuses to validate.
	require.Contains(t, comment, "legacy_freeform", "the census does not name the off-prefix type")
	require.Contains(t, comment, "experience.a|rule.b", "the census does not name the piped type")
	require.Contains(t, comment, "NOT VALID at migration time",
		"the comment does not record that existing rows are uncovered")

	// NOT VALID withholds the scan of old rows and nothing else: every new write
	// is refused from this moment.
	err := insertRawMemory(t, pool, "mem_445_new_bad", project, uid, "still_nonsense")
	require.Error(t, err, "a NOT VALID constraint must still refuse new rows")
	require.Contains(t, err.Error(), "memories_type_check")

	// And the pre-existing rows are left exactly where they were. The migration
	// records them; it does not decide for anyone what they should become.
	var survivors int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM memories WHERE id IN ('mem_445_outlier','mem_445_piped')`).Scan(&survivors))
	require.Equal(t, 2, survivors, "the migration deleted or rewrote rows it was only asked to report")

	// Finally, the documented repair. Settle the outliers, run the one line the
	// migration's comment gives you, and the constraint becomes fully validated
	// with no second migration.
	mustExec(t, pool, `DELETE FROM memories WHERE id IN ('mem_445_outlier','mem_445_piped')`)
	mustExec(t, pool, `ALTER TABLE memories VALIDATE CONSTRAINT memories_type_check`)
	_, validated, _ = typeCheckConstraint(t, pool)
	require.True(t, validated, "the documented VALIDATE did not validate the constraint")

	if !strings.Contains(comment, "VALIDATE CONSTRAINT memories_type_check") {
		t.Errorf("the census comment does not quote the repair statement, so an operator reading it "+
			"is told what is wrong and not what to do; got %q", comment)
	}
}
