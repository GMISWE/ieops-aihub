package domain

// aihub#434, the DB-gated half. Two things the DB-free gate in
// db_check_policy_test.go structurally cannot do:
//
//  1. Prove the ENUMERATION is faithful. That gate derives the constraint set by
//     parsing 36 migration files and, for an unnamed CHECK, by REPRODUCING the
//     name Postgres would choose. Both are reconstructions. If the parse missed a
//     statement shape, or if dbCheckSynthName guessed wrong, the registry would
//     be complete and correct about a schema that does not exist — and every arm
//     over there would stay green. Only pg_constraint can say otherwise.
//
//  2. Prove the answer is a 400. The other file compares a Go literal against SQL
//     text; it never issues a request. The whole policy is about the STATUS a
//     caller gets, and the failure it exists to prevent (500 INTERNAL_ERROR
//     carrying SQLSTATE 23514) is only observable by actually writing.
//
//	go test ./internal/domain/ -run TestDBCheckRegistry_MatchesPgConstraint -v -count=1
//	go test ./internal/domain/ -run TestMemoryVisibility -v -count=1

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// pgCheckConstraintsSQL lists the CHECK constraints of the live schema.
//
// `NOT c.relispartition` is not a tidy-up. agent_events has seven partitions plus
// a default, and each one carries its own pg_constraint row for the parent's
// chk_evt_work_item_id — eight rows for one constraint. The migrations declare it
// once, on the parent, and Postgres propagates it, so counting the copies would
// make the parse look like it had missed seven constraints.
const pgCheckConstraintsSQL = `
	SELECT c.relname, con.conname
	  FROM pg_constraint con
	  JOIN pg_class c ON c.oid = con.conrelid
	  JOIN pg_namespace n ON n.oid = c.relnamespace
	 WHERE con.contype = 'c'
	   AND n.nspname = 'public'
	   AND NOT c.relispartition
	 ORDER BY 1, 2`

// TestDBCheckRegistry_MatchesPgConstraint holds the migration parse to the
// database it claims to describe.
//
// ─── Why NAMES and not definitions ──────────────────────────────────────────
//
// 🔴 Deliberate, and the reason is a hazard this repo has already been bitten by.
// A CI step that replays an OLD migration against the SHARED database rewinds
// whatever that migration defines: aihub#444 measured chk_evt_work_item_id
// flipping back to its 21-name form because a test replayed 0026, and being
// refused with SQLSTATE 23514 eighty-four seconds later in a job whose migration
// log said "successfully migrated database to version: 36". runMigration's
// superseding-replay repair (memory_latest_test.go) fixes that for tests that go
// through it — but a comparison of constraint TEXT here would still be at the
// mercy of every other step in the job, and would fail for a reason that has
// nothing to do with what it is testing.
//
// A rewind DROPs and re-ADDs the same constraint NAME on the same table, so the
// (table, name) set is invariant under it. That is what this compares, and it is
// exactly the part the DB-free gate cannot self-check: whether the fold found
// every statement, applied every DROP, and named every anonymous CHECK the way
// Postgres does. The PREDICATE half is already covered without a database, against
// the migration text, which is the authority for what the schema is supposed to
// be.
func TestDBCheckRegistry_MatchesPgConstraint(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	rows, err := pool.Query(ctx, pgCheckConstraintsSQL)
	require.NoError(t, err)
	defer rows.Close()

	live := map[string]bool{}
	for rows.Next() {
		var table, name string
		require.NoError(t, rows.Scan(&table, &name))
		live[table+"."+name] = true
	}
	require.NoError(t, rows.Err())

	// Anti-vacuity: an empty read (wrong database, unmigrated database, a query
	// that stopped matching) would make the comparison below pass in one
	// direction and produce a wall of noise in the other. Fail on the read.
	require.Greater(t, len(live), 15,
		"pg_constraint reports only %d CHECK constraint(s) in `public` — this database is not migrated "+
			"to head, so nothing below would mean anything", len(live))

	parsed := effectiveDBChecks(t)

	var missing, phantom []string
	for key := range live {
		if _, ok := parsed[key]; !ok {
			missing = append(missing, key)
		}
	}
	for key := range parsed {
		if !live[key] {
			phantom = append(phantom, key)
		}
	}
	sort.Strings(missing)
	sort.Strings(phantom)

	require.Empty(t, missing,
		"the database carries CHECK constraints the migration parse did not find: %v\n"+
			"Each one is a constraint the DB-free gate cannot demand a Go validator for, because it does "+
			"not know it exists — so the registry can be complete and the policy still unenforced. Fix "+
			"dbCheckFold (a statement shape it does not understand) or dbCheckSynthName (an anonymous "+
			"CHECK whose Postgres-chosen name it guessed wrong).", missing)

	require.Empty(t, phantom,
		"the migration parse reports CHECK constraints the database does not have: %v\n"+
			"Either a DROP is not being applied by the fold — in which case this is a union of eras "+
			"rather than HEAD, and the registry is carrying rows about constraints nobody enforces — or "+
			"a name was synthesised that Postgres spells differently, which means the REAL constraint "+
			"is in `missing` above under its true name and is unaccounted for.", phantom)

	t.Logf("migration parse and pg_constraint agree on %d CHECK constraints", len(live))
}

// TestMemoryVisibilityAnswersFourHundredNamingTheField is the behavioural half
// of the one gap aihub#434's enumeration turned up: memories.visibility was the
// last CHECK-constrained, caller-supplied column in the schema with no Go guard
// in front of it.
//
// The four arms are the four claims the policy makes, and the last one is the
// one that makes the others mean something.
func TestMemoryVisibilityAnswersFourHundredNamingTheField(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	remember := func(visibility string) (*Memory, error) {
		m, _, err := Remember(ctx, pool, &RememberRequest{
			Project:       proj,
			Type:          "fact.note",
			Content:       "aihub#434 visibility arm: " + visibility,
			Visibility:    visibility,
			DedupMode:     "off",
			CallerUserID:  uid,
			CallerDisplay: uid,
		})
		return m, err
	}

	t.Run("an off-vocabulary visibility is a 400 naming the field", func(t *testing.T) {
		_, err := remember("everyone")
		require.Error(t, err)

		var aerr *AihubError
		require.ErrorAs(t, err, &aerr)
		require.Equal(t, ErrBadRequest, aerr.Code,
			"an illegal visibility came back as %s. Before aihub#434 this was ErrInternalError carrying "+
				"the driver's `violates check constraint \"memories_visibility_check\" (SQLSTATE 23514)` "+
				"— a 500 that tells the caller to retry something that can never succeed and names none "+
				"of the legal values", aerr.Code)
		require.Contains(t, aerr.Message, "visibility",
			"the rejection does not name the field, so a caller cannot tell WHICH argument was wrong")
		details, ok := aerr.Details.(map[string]any)
		require.True(t, ok, "Details is %T, so the machine-readable half of the rejection is gone", aerr.Details)
		require.Equal(t, "visibility", details["field"],
			"the field is missing from details; an automated caller should not have to parse prose to "+
				"retry correctly, which is the whole difference between this and the 500 it replaces")
		require.Contains(t, aerr.Message, "private",
			"the legal values are not in the message, so the caller's only recovery is another guess")
	})

	t.Run("every legal visibility is accepted", func(t *testing.T) {
		// The control. A guard that refused everything would satisfy the arm
		// above, and "public" specifically is here because narrowing the Go set
		// below the CHECK is the tempting wrong fix — the artifact-share path
		// writes it through this same function.
		for _, v := range MemoryVisibilityList() {
			m, err := remember(v)
			require.NoError(t, err, "visibility %q is in the CHECK and must not be refused by Go", v)
			require.Equal(t, v, m.Visibility)
		}
	})

	t.Run("an unset visibility still defaults to project", func(t *testing.T) {
		// The empty string is not a value the caller chose, and treating it as
		// illegal would reject every caller that simply does not mention
		// visibility — a far bigger regression than the one being fixed.
		m, err := remember("")
		require.NoError(t, err)
		require.Equal(t, "project", m.Visibility)
	})

	t.Run("the column would still have refused it", func(t *testing.T) {
		// 🔴 The arm that stops the first one being vacuous. If the CHECK were
		// dropped tomorrow, "everyone" would become a legal value of the column
		// and the Go guard would silently be REFUSING INPUT THE DATABASE ACCEPTS
		// — the fail-closed-too-wide direction, where the cheapest fix is to
		// delete the guard. Writing round Go, straight at the column, is the only
		// way to observe that the constraint is still the backstop this policy
		// says it is.
		_, err := pool.Exec(ctx, `
			INSERT INTO memories (id, project, type, content, author_user_id, author_display, visibility)
			VALUES ($1, $2, 'fact.note', 'aihub#434 direct write', $3, $3, 'everyone')`,
			NewID("mem"), proj, uid)
		require.Error(t, err, "the INSERT succeeded, so memories_visibility_check is no longer enforcing "+
			"anything and validateMemoryVisibility has become a rule Go invented rather than a mirror")
		require.Contains(t, strings.ToLower(err.Error()), "memories_visibility_check",
			"the write failed for some reason other than the constraint under test: %v", err)
	})
}
