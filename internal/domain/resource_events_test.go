package domain

// Pure-unit gates for aihub#343. No live DB, so these run in plain
// `go test ./...` — which matters, because every behavioural test of the event
// stream is AIHUB_TEST_DB-gated and therefore SKIPs outside its own scoped CI
// step (mem_I98xpPgY). Tag C below is the one gate that a future call site
// cannot walk past on a developer's laptop.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ─── Tag C: the structural invariant ─────────────────────────────────────────

// lockMutatingSQL matches a statement that creates, destroys or REWRITES a
// resource_locks row.
//
// 🔴 The pattern is deliberately about the STATEMENT, not about a call site, a
// function name or a file list. Every previous fix in this area (aihub#238,
// #261, #264, #342) shipped a rule at some of the four lock-derivation sites and
// missed others, and every one of those gates was written as "this site does the
// right thing" — a judgement that a NEW site is not covered by. Asking instead
// "where does lock-mutating SQL exist" is a question a new site cannot answer
// differently, because writing the SQL is the only way to mutate a lock.
//
// 🔴 UPDATE is in the alternation, and it was missing from the first version of
// this gate. `UPDATE resource_locks SET owner_attempt_id=$1 WHERE ...` transfers
// a lock to a different attempt with no audit at all, and it passed every gate
// here — while lockUpsertSQL's own comment argues that an owner rewrite is
// precisely the case that must be recorded, because a reader following the
// previous owner would otherwise see an unmatched lock_acquired and read it as
// "still held". `UPDATE resource_locks rl` is an established idiom in this repo
// (0028_file_scope_project_key.sql), so the hole was reachable by copying a
// migration. The question the rule exists to answer is "is every lock MUTATION
// audited", and a DELETE/INSERT-only pattern answers a narrower one.
//
// `(?i)` because SQL keyword casing is not a contract; `\b` after the table name
// tolerates the `rl` alias the wrapped statements carry.
var lockMutatingSQL = regexp.MustCompile(
	`(?i)(INSERT\s+INTO|DELETE\s+FROM|UPDATE)\s+resource_locks\b`)

// lockEventsAuthorityFile is the ONE file allowed to contain that SQL.
const lockEventsAuthorityFile = "resource_events.go"

// auditedLockStatements is the registry of lock-mutating SQL constants in the
// authority file.
//
// It is a hand-maintained SET, on the same principle as
// internal/citest/dbtestcov/gated_tests.txt: adding a statement means adding its
// name here in the same diff, and that line is the reviewable record that
// somebody decided the new statement is audited. Nothing regenerates it. A count
// alone would let a rename swap an audited statement for an unaudited one
// without moving the number.
var auditedLockStatements = []string{
	"acquireLocksInsertSQL",
	"acquireLocksReleasePausedSQL",
	"lockDeleteByAttemptSQL",
	"lockDeleteByKeySQL",
	"lockUpsertSQL",
	"orphanLockSweepSQL",
	"releaseUndeclaredLocksSQL",
}

// The package's non-test source files come from domainSourceFiles
// (retryable_conflict_guard_test.go), which already fails rather than returning
// an empty slice — a wrong working directory must not pass as "nothing to
// check".

// TestLockEvents_NoLockMutatingSQLOutsideThisFile is Tag C: every statement that
// creates or destroys a resource_locks row lives in resource_events.go, so
// "is every lock mutation audited" is a question about one file.
//
// This is the gate that reaches a site that does not exist yet. The DB tests
// below prove the eleven CURRENT statements emit events; only this one fails
// when a twelfth is added somewhere else — and being added somewhere else is
// precisely how the last four fixes in this area were defeated.
func TestLockEvents_NoLockMutatingSQLOutsideThisFile(t *testing.T) {
	files := domainSourceFiles(t)
	sawAuthority := false
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		hits := lockMutatingSQL.FindAllString(string(body), -1)
		if name == lockEventsAuthorityFile {
			sawAuthority = true
			if len(hits) == 0 {
				t.Fatalf("%s contains no lock-mutating SQL at all — this gate is measuring nothing "+
					"(did the statements move again?)", name)
			}
			continue
		}
		if len(hits) > 0 {
			t.Errorf("%s contains lock-mutating SQL %v.\n"+
				"Every INSERT INTO / DELETE FROM resource_locks must live in %s and run through "+
				"releaseLocks / acquireLockUpsert / acquireLockIfFree, or its effect on the lock set "+
				"leaves no timeline event and becomes unverifiable (aihub#343).",
				name, hits, lockEventsAuthorityFile)
		}
	}
	if !sawAuthority {
		t.Fatalf("%s not found among %v — this gate cannot have run", lockEventsAuthorityFile, files)
	}
}

// TestLockEvents_EveryAuditedStatementIsDeclaredAndRegistered pins the registry
// against the file, in both directions.
//
// Forward: a name in the registry must really be a const in the authority file,
// so the registry cannot pass by listing statements that no longer exist.
// Backward: every lock-mutating const in the authority file must be in the
// registry, so adding one is a diff a reviewer sees rather than a silent
// addition to the set of statements nobody checked.
func TestLockEvents_EveryAuditedStatementIsDeclaredAndRegistered(t *testing.T) {
	body, err := os.ReadFile(lockEventsAuthorityFile)
	if err != nil {
		t.Fatalf("read %s: %v", lockEventsAuthorityFile, err)
	}
	src := string(body)

	registered := map[string]bool{}
	for _, name := range auditedLockStatements {
		registered[name] = true
		if !strings.Contains(src, "const "+name+" = ") {
			t.Errorf("auditedLockStatements lists %q but %s declares no such const — "+
				"the registry is describing a statement that does not exist",
				name, lockEventsAuthorityFile)
		}
	}

	// Backward direction: find every backtick-string DECLARATION whose value
	// mutates resource_locks and require it to be registered.
	//
	// 🔴 The pattern accepts four spellings, not one, and three of them were
	// missing from the first version of this gate: a top-level `const X = `, a
	// `var X = `, and either of those INSIDE a `const ( … )` / `var ( … )` group
	// — a form this very file already uses for its cause constants. A statement
	// declared in a group was not `found`, so `found == len(registry)` still held
	// and it escaped BOTH gates while sitting in the authority file. `^\s*` and
	// the optional `const|var` are what close that.
	constDecl := regexp.MustCompile("(?m)^\\s*(?:const |var )?([A-Za-z_][A-Za-z0-9_]*) = (`(?:[^`]*)`)")
	found := 0
	for _, m := range constDecl.FindAllStringSubmatch(src, -1) {
		name, value := m[1], m[2]
		if !lockMutatingSQL.MatchString(value) {
			continue
		}
		found++
		if !registered[name] {
			t.Errorf("%s declares lock-mutating const %q which is NOT in auditedLockStatements.\n"+
				"Add it there in this diff, and make sure it is executed through releaseLocks / "+
				"acquireLockUpsert / acquireLockIfFree (aihub#343).",
				lockEventsAuthorityFile, name)
		}
	}
	if found != len(auditedLockStatements) {
		t.Errorf("found %d lock-mutating consts in %s but the registry has %d (%v) — "+
			"the two must agree exactly",
			found, lockEventsAuthorityFile, len(auditedLockStatements), auditedLockStatements)
	}
}

// TestLockEvents_ReportingWrapperQualifiesItsReturning is a regression guard on
// a defect that is invisible until one specific statement runs.
//
// releaseUndeclaredLocksSQL joins run_attempts, which also has a claim_epoch
// column. An unqualified `RETURNING claim_epoch` inside the CTE is therefore
// ambiguous and Postgres rejects the whole statement — turning aihub#264's
// narrowing release into a hard 500. The other four DELETEs have no such join,
// so the mistake would pass everything except that one path.
func TestLockEvents_ReportingWrapperQualifiesItsReturning(t *testing.T) {
	got := normSQL(lockDeleteReporting(releaseUndeclaredLocksSQL))
	for _, want := range []string{
		"RETURNING rl.resource_type, rl.resource_key, rl.owner_attempt_id, rl.claim_epoch",
		"LEFT JOIN run_attempts ra ON ra.id = d.owner_attempt_id",
	} {
		if !strings.Contains(got, normSQL(want)) {
			t.Errorf("reporting wrapper missing %q.\n got: %q", want, got)
		}
	}
	if strings.Contains(got, normSQL("RETURNING resource_type")) {
		t.Errorf("reporting wrapper uses an UNQUALIFIED RETURNING; releaseUndeclaredLocksSQL "+
			"joins run_attempts, which also has claim_epoch, so Postgres rejects it.\n got: %q", got)
	}
}

// TestLockEvents_ReportingWrapperDoesNotAlterTheDelete is the other half: the
// wrapper must contain the original statement verbatim.
//
// Wrapping is only safe because it cannot change which rows the DELETE matches.
// A wrapper that rewrote the predicate — e.g. adding `USING run_attempts` to get
// at work_item_id — would read the same and delete less, dropping any row whose
// join partner is missing. Verbatim containment is what rules that out.
//
// ⚠️ Be honest about its strength: lockDeleteReporting embeds deleteStmt by
// string concatenation, so this holds BY CONSTRUCTION today and the assertion is
// near-tautological. It is a change detector for the rewrite above, not evidence
// that the CTE is semantically inert — that claim rests on Postgres' rule that a
// data-modifying WITH executes exactly once regardless of the outer query, and on
// the DB arms that run each wrapped statement for real.
func TestLockEvents_ReportingWrapperDoesNotAlterTheDelete(t *testing.T) {
	for _, stmt := range []struct {
		name string
		sql  string
	}{
		{"lockDeleteByAttemptSQL", lockDeleteByAttemptSQL},
		{"lockDeleteByKeySQL", lockDeleteByKeySQL},
		{"acquireLocksReleasePausedSQL", acquireLocksReleasePausedSQL},
		{"releaseUndeclaredLocksSQL", releaseUndeclaredLocksSQL},
		{"orphanLockSweepSQL", orphanLockSweepSQL},
	} {
		wrapped := normSQL(lockDeleteReporting(stmt.sql))
		if !strings.Contains(wrapped, normSQL(stmt.sql)) {
			t.Errorf("lockDeleteReporting(%s) does not contain the original statement verbatim; "+
				"the wrapper may have changed which rows are deleted.\n original: %q\n wrapped: %q",
				stmt.name, normSQL(stmt.sql), wrapped)
		}
	}
}

// ─── Payload shape ───────────────────────────────────────────────────────────

// TestResourcesUpdatedPayload_SeparatesVersionFromEntryCount is the pure-unit
// half of aihub#343's FIRST recorded instance.
//
// A step summary said declared_resources had been "reduced to CLAUDE.md(read)";
// a review called that fabricated because the update carried
// `resources_version: 0`; the producer answered that 0 was the compare-and-set
// GUARD, not an entry count. Both readings are defensible about one number, so
// the payload must never carry one number that could be either.
func TestResourcesUpdatedPayload_SeparatesVersionFromEntryCount(t *testing.T) {
	prior := json.RawMessage(`[{"type":"path","uri":"file:a.go","intent":"write"},
	                           {"type":"path","uri":"file:b.go","intent":"write"}]`)
	next := json.RawMessage(`[{"type":"path","uri":"file:a.go","intent":"read"}]`)

	if got, want := declaredEntryCount(prior), 2; got != want {
		t.Errorf("declaredEntryCount(prior) = %d, want %d", got, want)
	}
	if got, want := declaredEntryCount(next), 1; got != want {
		t.Errorf("declaredEntryCount(next) = %d, want %d", got, want)
	}

	// An entry count and a lock count are different numbers, and conflating them
	// is the other half of the same argument: the surviving entry is intent=read,
	// so it justifies no lock at all.
	nextLocks, ok := derivedFileScopeLocks(next, "proj")
	if !ok {
		t.Fatalf("derivedFileScopeLocks(next) could not read the payload")
	}
	if len(nextLocks) != 0 {
		t.Errorf("a read-intent declaration justified %v, want no lock — "+
			"an entry count is not a lock count", nextLocks)
	}
	priorLocks, ok := derivedFileScopeLocks(prior, "proj")
	if !ok {
		t.Fatalf("derivedFileScopeLocks(prior) could not read the payload")
	}
	if got, want := uniqueSorted(priorLocks), []string{"a.go", "b.go"}; !equalStrings(got, want) {
		t.Errorf("prior locked paths = %v, want %v", got, want)
	}
}

// TestNarrowingDiffIsSubtractedOnPathsNotKeys is the pure-unit half of the
// defect an independent review found in the first version of this change.
//
// wi_resources_updated used to recompute its added/removed sets from the derived
// KEYS while releaseUndeclaredFileScopeLocks subtracts on the declared PATHS.
// Those disagree in the flow aihub#261 calls the ordinary polyforge flow: drop a
// {"type":"repo"} entry with the paths untouched, and every key changes while
// every path stays. Measured on the repo's own aihub#261 fixture, the key-based
// record reported the two keys that were STILL HELD as "removed" and two keys
// held by nobody as "added", with zero lock_released events to match.
//
// This asserts the property that makes that impossible: the two derivations of
// one payload differ on keys and agree on paths, so only the path subtraction
// can describe what the release did.
func TestNarrowingDiffIsSubtractedOnPathsNotKeys(t *testing.T) {
	withRepo := json.RawMessage(`[
		{"type":"repo","uri":"repo:repo-a","intent":"write"},
		{"type":"path","uri":"file:go.mod","intent":"write"},
		{"type":"path","uri":"file:go.sum","intent":"write"}]`)
	// The very next /pf-plan rewrites declarations as path entries only.
	pathsOnly := json.RawMessage(`[
		{"type":"path","uri":"file:go.mod","intent":"write"},
		{"type":"path","uri":"file:go.sum","intent":"write"}]`)

	before, ok := derivedFileScopeLocks(withRepo, "proj")
	if !ok {
		t.Fatalf("derivedFileScopeLocks(withRepo) could not read the payload")
	}
	after, ok := derivedFileScopeLocks(pathsOnly, "proj")
	if !ok {
		t.Fatalf("derivedFileScopeLocks(pathsOnly) could not read the payload")
	}

	keysOf := func(m map[string]string) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	// Every KEY changed...
	if equalStrings(keysOf(before), keysOf(after)) {
		t.Fatalf("the fixture does not exercise the defect: keys are identical (%v). "+
			"Dropping the repo entry must change the derived keys, or this test proves nothing",
			keysOf(before))
	}
	// ...while every PATH stayed. A key subtraction therefore reports two
	// removals and two additions here; a path subtraction correctly reports none.
	if got, want := uniqueSorted(before), uniqueSorted(after); !equalStrings(got, want) {
		t.Errorf("paths differ across a repo-entry drop: %v vs %v — the fixture is wrong", got, want)
	}
	if got, want := uniqueSorted(before), []string{"go.mod", "go.sum"}; !equalStrings(got, want) {
		t.Errorf("locked paths = %v, want %v", got, want)
	}
}

// TestDeclaredEntryCount_UnreadableIsNotZero keeps "the declaration listed
// nothing" distinguishable from "the declaration could not be parsed" in the
// audit record. About 14% of stored declared_resources payloads in this
// deployment are malformed (see ValidateDeclaredResources), so the unreadable
// case is the common one — reporting it as 0 would put a confident, wrong number
// in the one record that exists to settle arguments about numbers.
func TestDeclaredEntryCount_UnreadableIsNotZero(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"absent", "", 0},
		{"empty array", `[]`, 0},
		{"two entries", `[{"type":"path","uri":"file:a"},{"type":"path","uri":"file:b"}]`, 2},
		{"an object, not an array", `{"type":"path"}`, -1},
		{"not JSON at all", `oops`, -1},
	}
	for _, c := range cases {
		if got := declaredEntryCount(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("declaredEntryCount(%s) = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestCappedKeyList_FlagsTruncation: agent_events.payload has a 64KB limit and a
// work item may declare hundreds of paths. A silently shortened list would be
// read as the complete declaration, which is a lie in exactly the record that
// exists to prevent one.
func TestCappedKeyList_FlagsTruncation(t *testing.T) {
	small := []string{"b", "a"}
	got, cut := cappedKeyList(small)
	if cut {
		t.Errorf("cappedKeyList(%v) reported truncation", small)
	}
	if !equalStrings(got, []string{"a", "b"}) {
		t.Errorf("cappedKeyList did not sort: %v", got)
	}

	big := make([]string, resourceKeyListCap+5)
	for i := range big {
		big[i] = string(rune('a'+i%26)) + string(rune('0'+i/26))
	}
	got, cut = cappedKeyList(big)
	if !cut {
		t.Errorf("cappedKeyList(%d keys) did not report truncation", len(big))
	}
	if len(got) != resourceKeyListCap {
		t.Errorf("cappedKeyList returned %d keys, want %d", len(got), resourceKeyListCap)
	}
}

// TestLockEventPayload_CarriesTheKeyAndTheCause: `resource_key` and `cause` are
// what make a per-lock event decidable on its own. Without the key a reader
// cannot tell which lock the event is about; without the cause a reader cannot
// tell a paused attempt's legitimate retention from a leak.
func TestLockEventPayload_CarriesTheKeyAndTheCause(t *testing.T) {
	op := newLockOp(lockCauseAttemptPaused, lockEventActor{UserID: "u_x", Display: "tester"}).
		withExtra(map[string]any{"retained_types": "git_branch"})
	p := lockEventPayload(lockRow{
		ResourceType: "file_scope", ResourceKey: "proj:repo:a.go",
		AttemptID: "ra_1", ClaimEpoch: 3,
	}, op)

	for k, want := range map[string]any{
		"resource_type":  "file_scope",
		"resource_key":   "proj:repo:a.go",
		"attempt_id":     "ra_1",
		"cause":          lockCauseAttemptPaused,
		"actor_display":  "tester",
		"actor_user_id":  "u_x",
		"retained_types": "git_branch",
	} {
		if p[k] != want {
			t.Errorf("payload[%q] = %v, want %v", k, p[k], want)
		}
	}
	if p["op_id"] == "" || p["op_id"] == nil {
		t.Errorf("payload has no op_id; the events of one operation cannot be regrouped")
	}
	if p["claim_epoch"] != int64(3) {
		t.Errorf("payload[claim_epoch] = %v, want 3", p["claim_epoch"])
	}
}

// TestLockOpCtx_WithExtraDoesNotMutateTheSharedOp: one operation's context is
// handed to several helpers in sequence (FnForceTakeover passes ftOp to a
// release and then to every re-acquire). A mutating setter would leak the last
// caller's fields onto events already built from the same value.
func TestLockOpCtx_WithExtraDoesNotMutateTheSharedOp(t *testing.T) {
	base := newLockOp(lockCauseForceTakeover, lockEventActor{})
	a := base.withExtra(map[string]any{"only_on_a": true})
	b := base.withExtra(map[string]any{"only_on_b": true})

	if _, leaked := base.Extra["only_on_a"]; leaked {
		t.Errorf("withExtra mutated the shared op: base carries only_on_a")
	}
	if _, leaked := a.Extra["only_on_b"]; leaked {
		t.Errorf("withExtra leaked b's fields onto a")
	}
	if a.OpID != base.OpID || b.OpID != base.OpID {
		t.Errorf("withExtra changed op_id (%q / %q vs %q) — the events of one operation "+
			"would no longer group", a.OpID, b.OpID, base.OpID)
	}
}

// TestEventVocabulary_UsesTheAlreadyDocumentedLockNames.
//
// 0006_events_memories.sql has listed "locks: lock_acquired, lock_released" in
// the agent_events comment since the schema was written, and nothing ever
// emitted them. Introducing locks_acquired/locks_released alongside a documented
// pair meaning the same thing would leave two spellings of one concept in a
// PUBLIC vocabulary — pf_read_events(types=[...]) filters on these strings — with
// no way to retire either.
func TestEventVocabulary_UsesTheAlreadyDocumentedLockNames(t *testing.T) {
	schema, err := os.ReadFile(filepath.Join("..", "db", "migrations", "0006_events_memories.sql"))
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	for _, name := range []string{EventLockAcquired, EventLockReleased} {
		if !strings.Contains(string(schema), name) {
			t.Errorf("event type %q is not the name the schema already documents; "+
				"a public vocabulary must not carry two spellings of one concept", name)
		}
	}
	// wi_resources_updated has no schema precedent, so it follows the convention
	// of the sibling events UpdateWorkItem already emits for its other fields.
	if !strings.HasPrefix(EventResourcesUpdated, "wi_") {
		t.Errorf("%q does not follow the wi_* convention of wi_goal_updated / "+
			"wi_content_updated / wi_reclassified", EventResourcesUpdated)
	}
}

// TestEventVocabulary_NeedsNoMigration is the deploy-sequencing gate.
//
// agent_events has a type allowlist (chk_evt_work_item_id) but it only
// constrains rows whose work_item_id is NULL, and every event this file writes
// carries the work item that held the lock — emitResourceEvent refuses to insert
// one that cannot. So no migration is required, which means no ordering
// constraint between the migration and the binary, and a rollback leaves the new
// rows readable.
//
// The gate is on the migration DIRECTORY, not on a remembered fact: if somebody
// later adds a migration naming these types, that is a signal the no-NULL
// invariant was broken and this comment is stale.
func TestEventVocabulary_NeedsNoMigration(t *testing.T) {
	dir := filepath.Join("..", "db", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		src := string(body)
		// 0006 lists lock_acquired / lock_released in a schema COMMENT, which is
		// the precedent the vocabulary follows, not an allowlist entry.
		if !strings.Contains(src, "chk_evt_work_item_id") {
			continue
		}
		if strings.Contains(src, "'"+EventLockAcquired+"'") ||
			strings.Contains(src, "'"+EventLockReleased+"'") ||
			strings.Contains(src, "'"+EventResourcesUpdated+"'") {
			t.Errorf("%s adds an aihub#343 event type to the chk_evt_work_item_id allowlist. "+
				"That allowlist only matters for rows with a NULL work_item_id, and these events "+
				"always carry one — if a migration became necessary, the no-NULL invariant broke "+
				"and the deploy is no longer migration-free.", e.Name())
		}
	}
}
