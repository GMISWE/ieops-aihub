package domain

// aihub#444, the DB-free half (aihub#411 decision table §6.2 T2-5).
//
// Three sets governed one column and nothing held them together. This file is
// what holds them together now, and it needs no database on purpose: an
// invariant that only a gated CI step can check is an invariant a local
// `go test ./...` will not tell you that you broke.
//
//	go test ./internal/domain/ -run TestEventTypes -v -count=1
//	go test ./internal/domain/ -run TestEventVocabulary -v -count=1

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// nullWorkItemMigration is the migration whose CHECK NullWorkItemEventTypes
// mirrors. Named as a constant so the failure below can print it.
const nullWorkItemMigration = "0036_agent_events_admin_gc_manual.sql"

// TestEventTypes_AdminOnlyIsASubsetOfTheAdminWhitelist is the regression that
// aihub#444 exists for.
//
// The defect it forbids is not "a missing entry" — it is a SHAPE. When a type
// requires admin role but is absent from the whitelist that gates `admin: true`,
// the flag inverts: the same admin, sending the same event, is REFUSED when they
// declare it and ACCEPTED when they say nothing. The only behaviour that teaches
// is to stop declaring, which is the opposite of what the flag is for.
//
// AdminEventWhitelist is derived from AdminOnlyEventTypes so this cannot happen
// by construction. The assertion is kept anyway, because the derivation is one
// edit away from being unrolled back into two hand-written lists — which is
// exactly the state this replaced.
func TestEventTypes_AdminOnlyIsASubsetOfTheAdminWhitelist(t *testing.T) {
	whitelist := setOf(AdminEventWhitelist)

	require.NotEmpty(t, AdminOnlyEventTypes, "an empty admin-only set would satisfy the containment vacuously")

	for _, typ := range AdminOnlyEventTypes {
		require.True(t, whitelist[typ],
			"%q always requires admin role but is not in AdminEventWhitelist, so an admin sending it "+
				"with admin:true is refused 403 while the SAME admin omitting the flag succeeds — the "+
				"inversion aihub#444 removed", typ)
	}

	// Not equality: the four extras are types a NON-admin may also emit, so
	// folding them into AdminOnlyEventTypes would forbid that. Containment one
	// way is the invariant; equality would be a different and wrong rule, and
	// this assertion says so rather than leaving the difference to be guessed.
	require.Greater(t, len(AdminEventWhitelist), len(AdminOnlyEventTypes),
		"the whitelist has stopped being strictly larger than the admin-only set: either the four "+
			"types a non-admin may also emit have been lost, or the two sets have been made equal")
}

// TestEventTypes_NullWorkItemMirrorsTheMigration holds the Go list and the SQL
// CHECK to the same set.
//
// The direction of a disagreement decides which failure you get, and both are
// bad in different ways, so the test demands equality rather than containment:
//
//   - Go WIDER than the CHECK rebuilds the 500 this list was added to remove.
//     The request passes the Go guard, the INSERT hits SQLSTATE 23514, and the
//     caller is handed the driver's constraint text as a server fault.
//   - Go NARROWER than the CHECK is inert today, but it silently withdraws a
//     capability the schema still grants, and nothing else would ever say so.
func TestEventTypes_NullWorkItemMirrorsTheMigration(t *testing.T) {
	path := filepath.Join("..", "db", "migrations", nullWorkItemMigration)
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "cannot read %s — if the migration was renamed, nullWorkItemMigration must "+
		"be updated with it or this mirror silently stops being checked", path)

	sql := string(raw)

	// The Up section only: Down deliberately restores the PREVIOUS 21-name
	// predicate, and parsing the whole file would compare the Go list against a
	// union of both and pass for the wrong reason.
	upStart := strings.Index(sql, "-- +goose Up")
	require.GreaterOrEqual(t, upStart, 0, "no goose Up section in %s", path)
	downStart := strings.Index(sql, "-- +goose Down")
	require.Greater(t, downStart, upStart, "no goose Down section in %s", path)
	up := sql[upStart:downStart]

	checkStart := strings.Index(up, "ADD CONSTRAINT chk_evt_work_item_id")
	require.GreaterOrEqual(t, checkStart, 0, "no chk_evt_work_item_id in the Up section of %s", path)
	inStart := strings.Index(up[checkStart:], "event_type IN (")
	require.GreaterOrEqual(t, inStart, 0, "no event_type IN (...) list in %s", path)
	body := up[checkStart+inStart:]
	closeAt := strings.Index(body, ")")
	require.Greater(t, closeAt, 0, "unterminated event_type IN (...) list in %s", path)

	var fromSQL []string
	for _, m := range regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(body[:closeAt], -1) {
		fromSQL = append(fromSQL, m[1])
	}
	sort.Strings(fromSQL)

	// Anti-vacuity: a parse that silently matched nothing would make every
	// comparison below trivially true against an empty slice.
	require.Greater(t, len(fromSQL), 15,
		"parsed only %d names out of %s — the parse, not the constraint, is what broke", len(fromSQL), path)

	fromGo := append([]string(nil), NullWorkItemEventTypes...)
	sort.Strings(fromGo)

	require.Equal(t, fromSQL, fromGo,
		"NullWorkItemEventTypes and the chk_evt_work_item_id CHECK in %s name different sets. Go wider "+
			"than the CHECK turns a 400 back into a 500 carrying the driver's constraint text; Go "+
			"narrower withdraws a capability the schema still grants.", nullWorkItemMigration)
}

// TestEventTypes_EverySetMemberIsPublished ties the three enforced sets to the
// published vocabulary.
//
// A type the server gates on but does not publish is the original defect in
// miniature: `admin_gc_manual` was gated in Go, absent from the CHECK, and named
// nowhere a caller could read. A caller cannot avoid a rule they cannot see.
func TestEventTypes_EverySetMemberIsPublished(t *testing.T) {
	published := setOf(EventVocabulary)

	for name, set := range map[string][]string{
		"AdminOnlyEventTypes":    AdminOnlyEventTypes,
		"AdminEventWhitelist":    AdminEventWhitelist,
		"NullWorkItemEventTypes": NullWorkItemEventTypes,
	} {
		require.NotEmpty(t, set, "%s is empty, so its coverage check is vacuous", name)
		for _, typ := range set {
			require.True(t, published[typ],
				"%s names %q, which EventVocabulary does not publish — the server enforces a rule "+
					"about a type no caller can discover", name, typ)
		}
	}
}

// TestEventVocabulary_IsWellFormed checks the shape the published list claims:
// deduplicated, lowercase snake_case, and sorted within each origin group.
//
// The grouping is not decoration — it is what tells a reader whether a name is
// something they can emit or something only the server writes — so "sorted
// within a group" is the strongest ordering that can be asserted without
// flattening it away, and it is asserted rather than trusted because a name
// dropped into the wrong group reads as a claim about its origin.
func TestEventVocabulary_IsWellFormed(t *testing.T) {
	require.Greater(t, len(EventVocabulary), 30, "the vocabulary has shrunk below anything this tree emits")

	seen := map[string]bool{}
	shape := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	for _, typ := range EventVocabulary {
		require.False(t, seen[typ], "%q appears twice in EventVocabulary", typ)
		seen[typ] = true
		require.Regexp(t, shape, typ, "%q is not lowercase snake_case; every emitter in this tree is", typ)
	}

	// Group boundaries are the blank lines in the source, which this test does
	// not read — so instead of parsing the file, the ordering is checked as
	// "sorted except at a small number of restarts", and the restart count is
	// bounded so the arm cannot be satisfied by a list that is simply shuffled.
	restarts := 0
	for i := 1; i < len(EventVocabulary); i++ {
		if EventVocabulary[i] < EventVocabulary[i-1] {
			restarts++
		}
	}
	require.LessOrEqual(t, restarts, 10,
		"EventVocabulary is ordered neither alphabetically nor by sorted groups (%d descents); a "+
			"reader cannot tell which group a name belongs to, and the group is what says whether a "+
			"caller may emit it", restarts)
}

// TestEventVocabulary_CoversEveryEmitter is the drift guard, and the reason this
// file can claim the vocabulary is a fact about the tree rather than a snapshot.
//
// It scans every non-test .go file under internal/ for the two shapes this
// server files events under — an event_type literal inside an
// `INSERT INTO agent_events` statement, and an `emitCodingEvent(..., "type", ...)`
// call — and fails on any literal EventVocabulary does not carry.
//
// ⚠️ ONE DIRECTION ONLY, deliberately. Found ⊆ published is checked; published ⊆
// found is not, because the vocabulary legitimately carries names with no
// emitter in this tree: the admin types only a caller can send, the retired
// artifact_action whose rows still exist, and the types named by
// chk_evt_work_item_id alone. Asserting the reverse would force those to be
// deleted, and deleting them is how a reader loses the ability to filter history.
//
// The failure this catches is the one that matters: somebody adds an event type
// and does not publish it. docs/design/polyforge-v1-design.md section 19.0.1
// already calls adding a type a compatibility event for
// pf_read_events(types=[...]), so requiring the person who adds one to name it
// here is that rule enforced rather than restated.
func TestEventVocabulary_CoversEveryEmitter(t *testing.T) {
	published := setOf(EventVocabulary)

	insertRe := regexp.MustCompile(`(?s)INSERT INTO agent_events\b.{0,600}`)
	litRe := regexp.MustCompile(`'([a-z][a-z0-9_]*)'`)
	codingRe := regexp.MustCompile(`emitCodingEvent\(\s*[A-Za-z0-9_.]+\s*,\s*[A-Za-z0-9_.]+\s*,\s*"([a-z][a-z0-9_]*)"`)

	// Payload KEYS and payload VALUES live inside the same statements as the
	// event_type literal and are quoted the same way, so the scan cannot tell
	// them apart by shape. They are listed rather than pattern-matched away: an
	// exception that has to be written down is an exception somebody reads.
	notAnEventType := map[string]bool{
		"sweep":                  true, // gc.go, payload key
		"partition_create":       true, // gc.go, payload value for that key
		"default_partition_rows": true, // gc.go, payload key
	}

	found := map[string][]string{}
	root := ".."
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		src := string(raw)
		for _, stmt := range insertRe.FindAllString(src, -1) {
			// Stop at the end of the SQL string literal so the scan cannot walk
			// into ordinary Go code following the query.
			if end := strings.Index(stmt, "`"); end > 0 {
				stmt = stmt[:end]
			}
			for _, m := range litRe.FindAllStringSubmatch(stmt, -1) {
				found[m[1]] = append(found[m[1]], path)
			}
		}
		for _, m := range codingRe.FindAllStringSubmatch(src, -1) {
			found[m[1]] = append(found[m[1]], path)
		}
		return nil
	})
	require.NoError(t, err)

	// Anti-vacuity: a walk that found nothing, or a regexp that stopped matching
	// the way these statements are written, would pass this test in silence.
	require.Greater(t, len(found), 20,
		"the scan found only %d candidate literals across internal/ — the SCAN is what broke, not the "+
			"vocabulary; a passing result from here would mean nothing", len(found))
	for _, canary := range []string{"work_item_filed", "attempt_started", "memory_created"} {
		require.Contains(t, found, canary,
			"the scan did not find %q, which this tree certainly emits — the regexps no longer match "+
				"how these statements are written", canary)
	}

	for typ, files := range found {
		if notAnEventType[typ] {
			continue
		}
		require.True(t, published[typ],
			"%s files events with event_type %q, which EventVocabulary does not publish. Add it to "+
				"internal/domain/event_types.go — adding an event type is a compatibility event for "+
				"pf_read_events(types=[...]) and the caller has no other way to learn the name. If it "+
				"is a payload key rather than an event type, add it to notAnEventType in this test.",
			strings.Join(files, ", "), typ)
	}
}

// TestMigrationReplay_RestoresTheConstraintToHead pins the repair for the
// defect that reddened aihub#444's first CI run, DB-free.
//
// ─── What happened, since a green local run is what let it through ─────────
//
// `goose up` installed migration 0036's 22-name chk_evt_work_item_id on
// agent_events and on all seven partitions. Eighty-four seconds later, a
// different CI step ran TestBackfillLatestID, which replays migration 0026 to
// exercise its latest_id backfill — and 0026's Up section ends with
// DROP CONSTRAINT / ADD CONSTRAINT / VALIDATE carrying the 21-name list of its
// own era. Every one of those eight rows flipped back, and the next step's
// INSERT was refused with SQLSTATE 23514 inside a job whose migration log reads
// "successfully migrated database to version: 36".
//
// Two diagnoses were offered and BOTH are wrong, which is why they are recorded
// here rather than left to be re-proposed: it is not a partition-local copy the
// parent DROP failed to cascade to (measured — all eight rows carry the new
// list after 0036 and all eight lose it together), and it is not the runtime
// partition template carrying a stale list (createPartitionSQL in gc.go is a
// bare CREATE TABLE ... PARTITION OF, so a new partition inherits whatever the
// parent has at that moment). The rewind is a TEST replaying history against a
// shared database.
//
// ─── What is asserted ──────────────────────────────────────────────────────
//
// That supersedingMigrations finds the repair edge, and that it finds it by
// reading the directory rather than by carrying a list. chk_evt_work_item_id is
// redefined by SEVEN migrations, so a hand-maintained "0026 also needs 0036"
// note would be wrong the next time any of the other six is replayed, and wrong
// again on the day an 0037 touches it.
func TestMigrationReplay_RestoresTheConstraintToHead(t *testing.T) {
	// The exact pair from the CI failure: replaying 0026 must pull 0036 with it.
	after0026 := supersedingMigrations(t, "0026_memories_latest_id.sql")
	require.Contains(t, after0026, nullWorkItemMigration,
		"replaying 0026 no longer re-applies %s, so a test that replays it leaves the whole database "+
			"holding 0026's 21-name chk_evt_work_item_id and every later test in the run sees it",
		nullWorkItemMigration)

	// Every migration that redefines this constraint must reach head, not only
	// the one that happened to break. Anti-vacuity too: if this found one file,
	// the loop below would be proving almost nothing.
	var redefiners []string
	entries, err := os.ReadDir("../db/migrations")
	require.NoError(t, err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		for _, c := range constraintsAddedBy(migrationUpSection(t, e.Name())) {
			if c == "chk_evt_work_item_id" {
				redefiners = append(redefiners, e.Name())
			}
		}
	}
	require.Greater(t, len(redefiners), 4,
		"only %d migration(s) redefine chk_evt_work_item_id — the scan broke, not the history",
		len(redefiners))

	head := redefiners[len(redefiners)-1]
	require.Equal(t, nullWorkItemMigration, head,
		"the LAST migration to redefine chk_evt_work_item_id is %s, but NullWorkItemEventTypes is "+
			"mirrored against %s — the Go mirror is being checked against a superseded file", head,
		nullWorkItemMigration)

	for _, f := range redefiners[:len(redefiners)-1] {
		require.Contains(t, supersedingMigrations(t, f), head,
			"replaying %s would leave chk_evt_work_item_id at its own era's definition instead of "+
				"%s's", f, head)
	}

	// The head itself must supersede nothing, or the mirror above is stale.
	require.NotContains(t, supersedingMigrations(t, head), head)
}

// TestMigrationReplay_ScanIgnoresProse is the anti-vacuity half of the arm
// above: an edge built out of a sentence would make supersedingMigrations
// re-apply unrelated migrations, and the arm above cannot tell a real edge from
// a prose one.
func TestMigrationReplay_ScanIgnoresProse(t *testing.T) {
	got := constraintsAddedBy(`
-- This comment says ALTER TABLE t ADD CONSTRAINT prose_only CHECK (true).
ALTER TABLE t ADD CONSTRAINT real_one CHECK (true);
`)
	require.Equal(t, []string{"real_one"}, got,
		"constraintsAddedBy read a constraint name out of a comment; these migrations document their "+
			"own DDL in prose, so that would wire replay edges between files that share nothing")
}
