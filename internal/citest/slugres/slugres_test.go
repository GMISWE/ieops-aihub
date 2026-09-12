package slugres

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot walks up from the test's working directory to the module root,
// identified by go.mod — a marker file rather than a fixed number of "..", so
// moving this package does not silently point the gate at a subtree (same
// shape as internal/citest/rowserr).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find go.mod above the test's working directory; this gate cannot have run")
	return ""
}

// scanFixtureFiles runs the scanner over synthetic files, against the REAL
// registry tables. Stale-entry checks are deliberately absent here (they
// belong to the whole-repo run, where every entry is expected to be used).
func scanFixtureFiles(t *testing.T, files map[string]string) *Census {
	t.Helper()
	s := &scanner{
		pkgConsts: map[string]map[string]constEntry{},
		consumed:  map[*fileEntry]map[token.Pos]bool{},
		reg:       NewRegistry(),
		census:    &Census{GreenByKind: map[string]int{}},
	}
	funcIndex := map[string][]*declSite{}
	for rel, src := range files {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("fixture %s does not parse: %v — an unparseable fixture proves nothing", rel, err)
		}
		fe := &fileEntry{rel: rel, fset: fset, file: f, src: []byte(src)}
		s.files = append(s.files, fe)
		s.consumed[fe] = map[token.Pos]bool{}
		dir := filepath.ToSlash(filepath.Dir(rel))
		if s.pkgConsts[dir] == nil {
			s.pkgConsts[dir] = map[string]constEntry{}
		}
	}
	for _, fe := range s.files {
		for _, d := range fe.file.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok {
				funcIndex[fn.Name.Name] = append(funcIndex[fn.Name.Name], &declSite{fe: fe, fn: fn})
			}
		}
	}
	for _, fe := range s.files {
		s.scanFileSinks(fe)
	}
	for _, fe := range s.files {
		s.scanDynamicFragments(fe)
	}
	s.scanContractCallers(funcIndex)
	s.scanAliasRefs()
	return s.census
}

// ─── WiPositions: the SQL half of the recogniser ─────────────────────────────

func TestWiPositionsRecognisesTheColumnShapes(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []Position
	}{
		{"FK comparison", `SELECT 1 FROM agent_events WHERE work_item_id = $1`,
			[]Position{{Col: "work_item_id", N: 1}}},
		{"alias-qualified, inequality", `SELECT 1 FROM run_attempts ra WHERE ra.work_item_id != $4`,
			[]Position{{Col: "work_item_id", N: 4}}},
		{"bare id attributed to work_items", `UPDATE work_items SET status='queued' WHERE id=$1 AND status='blocked'`,
			[]Position{{Col: "work_items.id", N: 1}}},
		{"bare id on another table is NOT a position", `DELETE FROM agent_events WHERE id = $1`, nil},
		{"subquery attribution", `INSERT INTO agent_events (id, work_item_id, payload, project)
			VALUES ($1, $2, $3::jsonb, (SELECT project FROM work_items WHERE id=$2))`,
			[]Position{{Col: "work_item_id", N: 2}, {Col: "work_items.id", N: 2}}},
		{"self-resolving is marked", `SELECT id FROM work_items WHERE id = $1 OR slug = $1`,
			[]Position{{Col: "work_items.id", N: 1, SelfResolving: true}}},
		{"ANY over an FK column", `DELETE FROM wi_dependencies WHERE blocked_wi_id = ANY($2)`,
			[]Position{{Col: "blocked_wi_id", N: 2}}},
		{"dependency columns", `SELECT 1 FROM wi_dependencies WHERE blocked_wi_id=$1 AND blocking_wi_id=$2`,
			[]Position{{Col: "blocked_wi_id", N: 1}, {Col: "blocking_wi_id", N: 2}}},
	}
	for _, c := range cases {
		got := WiPositions(c.sql)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: position %d: got %+v, want %+v", c.name, i, got[i], c.want[i])
			}
		}
	}
}

// ─── Calibration: sources whose verdict is known by construction ─────────────
//
// Every arm below is a shape the analyser must separate BEFORE any census
// result is trusted. Both directions on purpose: an analyser that flags
// everything and one that flags nothing each pass half of this set.

// The three historical shapes, plus the fifth instance and the evasions the
// aihub#361 review taught this repo to expect.
var slugresBadFixtures = []struct {
	name string
	rel  string
	src  string
}{
	{
		// aihub#127: the WRITE side. Raw caller parameter into an FK column.
		name: "write side: raw param into an FK insert (aihub#127)",
		rel:  "internal/server/fixture.go",
		src: `package server
func handleEmit(pool P, c C) {
	wiID := c.Param("id")
	pool.Exec(ctx, "INSERT INTO agent_events (id, work_item_id) VALUES ($1, $2)", NewID("evt"), wiID)
}`,
	},
	{
		// aihub#343: the READ side. No constraint trips; 200 + empty list.
		name: "read side: raw param into a WHERE comparison (aihub#343)",
		rel:  "internal/server/fixture.go",
		src: `package server
func handleList(pool P, c C) {
	raw := c.QueryParam("work_item_id")
	rows, _ := pool.Query(ctx, "SELECT id FROM agent_events WHERE work_item_id = $1", raw)
	_ = rows
}`,
	},
	{
		// aihub#357: resolve for the access check, then bind the ORIGINAL.
		name: "dependency shape: resolution result dropped (aihub#357)",
		rel:  "internal/server/fixture.go",
		src: `package server
func handleDeps(pool P, c C) {
	wiID := c.Param("id")
	wi, aerr := GetWorkItem(ctx, pool, wiID)
	_, _ = wi, aerr
	rows, _ := pool.Query(ctx, "SELECT 1 FROM wi_dependencies WHERE blocked_wi_id = $1", wiID)
	_ = rows
}`,
	},
	{
		// The fifth instance, verbatim shape: /unblock before aihub#362.
		name: "the /unblock body as it shipped (aihub#362)",
		rel:  "internal/server/fixture.go",
		src: `package server
func handleUnblock(pool P, c C, u U) {
	wiID := c.Param("id")
	var status string
	pool.QueryRow(ctx, "SELECT status FROM work_items WHERE id=$1", wiID).Scan(&status)
	pool.Exec(ctx, "UPDATE work_items SET status='queued' WHERE id=$1 AND status='blocked'", wiID)
	pool.Exec(ctx, "INSERT INTO agent_events (id, work_item_id, event_type, project) VALUES ($1, $2, 'admin_unblock', (SELECT project FROM work_items WHERE id=$2))", NewID("evt"), wiID)
}`,
	},
	{
		// Wrapping the raw value is not resolving it (the evasion that
		// defeated the first cut of the aihub#359 gate, transposed).
		name: "raw value wrapped in a helper call",
		rel:  "internal/server/fixture.go",
		src: `package server
func handleWrapped(pool P, c C) {
	pool.Exec(ctx, "UPDATE work_items SET status='queued' WHERE id=$1", normalize(c.Param("id")))
}`,
	},
	{
		// The mirror of the sanctioned resolve-overwrite pattern: a resolved
		// value overwritten with the raw one. Last write wins, and the last
		// write is raw.
		name: "resolved local overwritten with the raw parameter",
		rel:  "internal/server/fixture.go",
		src: `package server
func handleReassigned(pool P, c C) {
	wi, aerr := GetWorkItem(ctx, pool, c.Param("id"))
	_ = aerr
	wiID := wi.ID
	wiID = c.Param("id")
	pool.Exec(ctx, "UPDATE work_items SET status='queued' WHERE id=$1", wiID)
}`,
	},
	{
		// A NEW unresolved sink added to a file that carries siteExemptions
		// (internal/domain/conflicts.go exempts p.WIID). The exemption is per
		// call site — same file, same function name, different argument must
		// stay red. This is aihub#361's second review finding, pinned.
		name: "new sink in an exempted file is not covered by the exemption",
		rel:  "internal/domain/conflicts.go",
		src: `package domain
func PredictConflicts(pool P, q Q) {
	pool.QueryRow(ctx, "SELECT project FROM work_items WHERE id=$1", q.Other)
}`,
	},
	{
		// A bare id the attributor cannot place, in a statement that mentions
		// work_items: reported for a human, not guessed at.
		name: "unattributable bare id comparison",
		rel:  "internal/domain/fixture.go",
		src: `package domain
func oddJoin(pool P, x string) {
	pool.Exec(ctx, "WITH w AS (SELECT 1) UPDATE work_items_archive SET note=(SELECT 1 FROM work_items) WHERE TRUE AND id = $1", x)
}`,
	},
	{
		// A hardcoded non-empty literal is a hardcoded reference.
		name: "hardcoded work item reference",
		rel:  "internal/domain/fixture.go",
		src: `package domain
func hardcoded(pool P) {
	pool.Exec(ctx, "UPDATE work_items SET status='queued' WHERE id=$1", "aihub#1")
}`,
	},
	{
		// Dynamic SQL in a file with no dynamicSQLExemptions entry.
		name: "unexempted dynamic fragment",
		rel:  "internal/domain/fixture.go",
		src: `package domain
func dyn(pool P, idx int) {
	where := fmt.Sprintf(" AND work_item_id = $%d", idx)
	_ = where
}`,
	},
	{
		// An unregistered parameter reaching a work-item-id position: the
		// census must force the classification decision.
		name: "parameter with no declared contract",
		rel:  "internal/domain/fixture.go",
		src: `package domain
func helperNobodyClassified(pool P, wiID string) {
	pool.Exec(ctx, "DELETE FROM wi_watches WHERE work_item_id = $1", wiID)
}`,
	},
}

var slugresGoodFixtures = []struct {
	name string
	rel  string
	src  string
}{
	{
		name: "resolve, then use the canonical id (the aihub#357 fix shape)",
		rel:  "internal/server/fixture.go",
		src: `package server
func handleFixed(pool P, c C) {
	wi, aerr := GetWorkItem(ctx, pool, c.Param("id"))
	_ = aerr
	pool.Exec(ctx, "UPDATE work_items SET status='queued' WHERE id=$1 AND status='blocked'", wi.ID)
}`,
	},
	{
		name: "resolve-overwrite: raw local overwritten with the resolved id",
		rel:  "internal/server/fixture.go",
		src: `package server
func handleOverwrite(pool P, c C) {
	wiID := c.Param("id")
	wi, aerr := GetWorkItem(ctx, pool, wiID)
	_ = aerr
	wiID = wi.ID
	rows, _ := pool.Query(ctx, "SELECT 1 FROM wi_step_state WHERE work_item_id = $1", wiID)
	_ = rows
}`,
	},
	{
		name: "self-resolving SQL may take the raw reference",
		rel:  "internal/server/fixture.go",
		src: `package server
func handleSelf(pool P, c C) {
	pool.QueryRow(ctx, "SELECT id FROM work_items WHERE id = $1 OR slug = $1", c.Param("id"))
}`,
	},
	{
		name: "a minted id has never been a slug",
		rel:  "internal/domain/fixture.go",
		src: `package domain
func createIt(pool P) {
	wiID := NewID("wi")
	pool.Exec(ctx, "INSERT INTO work_items (id, project) VALUES ($1, $2)", wiID, "p")
}`,
	},
	{
		name: "row provenance: scanned out of a self-resolving query",
		rel:  "internal/domain/fixture.go",
		src: `package domain
func viaScan(pool P, ref string) {
	var id string
	pool.QueryRow(ctx, "SELECT id FROM work_items WHERE id = $1 OR slug = $1", ref).Scan(&id)
	pool.Exec(ctx, "DELETE FROM wi_watches WHERE work_item_id = $1", id)
}`,
	},
	{
		name: "rows loop + append + range (the requeue sweep shape)",
		rel:  "internal/domain/fixture.go",
		src: `package domain
func sweep(tx T, wi *WorkItem) {
	rows, _ := tx.Query(ctx, "SELECT dep.blocked_wi_id FROM wi_dependencies dep WHERE dep.blocking_wi_id = $1", wi.ID)
	var candidateIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			candidateIDs = append(candidateIDs, id)
		}
	}
	for _, blockedID := range candidateIDs {
		tx.Exec(ctx, "UPDATE work_items SET status='queued' WHERE id=$1", blockedID)
	}
}`,
	},
	{
		name: "the empty-string sentinel is not a reference",
		rel:  "internal/domain/fixture.go",
		src: `package domain
func sentinel(pool P, wi *WorkItem) {
	pool.Exec(ctx, "SELECT 1 FROM wi_dependencies WHERE blocked_wi_id=$1 AND blocking_wi_id != $2", wi.ID, "")
}`,
	},
	{
		name: "a WorkItem-typed parameter's ID is canonical by construction",
		rel:  "internal/domain/fixture.go",
		src: `package domain
func onLoaded(pool P, wi *WorkItem) {
	rows, _ := pool.Query(ctx, "SELECT 1 FROM agent_events WHERE work_item_id = $1", wi.ID)
	_ = rows
}`,
	},
}

func TestAnalyserIsCalibrated(t *testing.T) {
	for _, c := range slugresBadFixtures {
		census := scanFixtureFiles(t, map[string]string{c.rel: c.src})
		if len(census.Violations) == 0 {
			t.Errorf("analyser reported CLEAN on a fixture that is not: %s\n"+
				"It therefore cannot detect that shape in the shipped tree either, and every clean "+
				"result the census gives is meaningless.", c.name)
		}
	}
	for _, c := range slugresGoodFixtures {
		census := scanFixtureFiles(t, map[string]string{c.rel: c.src})
		if len(census.Violations) != 0 {
			t.Errorf("analyser reported violations on a known-good fixture (%s): %v\n"+
				"An analyser that cannot pass correct code is not a gate, it is noise — and the "+
				"cheapest response to noise is deletion.", c.name, census.Violations)
		}
	}
}

// TestContractLayerChecksCallers calibrates layer 2: a call to a function
// with a canonicalParams contract must prove its argument, and the proof
// travels through the caller's own contract when it has one.
func TestContractLayerChecksCallers(t *testing.T) {
	// The declaring file must carry the real contract anchor path, and the
	// declared function must have the contract parameter in the position the
	// real one has it.
	decl := `package domain
func ListDependencies(ctx C, pool P, wiID string, roles R, role string) {}`

	bad := `package server
func handleRaw(pool P, c C) {
	ListDependencies(ctx, pool, c.Param("id"), nil, "")
}`
	census := scanFixtureFiles(t, map[string]string{
		"internal/domain/dependencies.go": decl,
		"internal/server/fixture.go":      bad,
	})
	found := false
	for _, v := range census.Violations {
		if strings.Contains(v, "handleRaw") && strings.Contains(v, "ListDependencies") {
			found = true
		}
	}
	if !found {
		t.Errorf("layer 2 did not flag a raw caller parameter handed to a contract function; "+
			"violations were: %v", census.Violations)
	}

	good := `package server
func handleResolved(pool P, c C) {
	wi, aerr := GetWorkItem(ctx, pool, c.Param("id"))
	_ = aerr
	ListDependencies(ctx, pool, wi.ID, nil, "")
}`
	census = scanFixtureFiles(t, map[string]string{
		"internal/domain/dependencies.go": decl,
		"internal/server/fixture.go":      good,
	})
	for _, v := range census.Violations {
		if strings.Contains(v, "handleResolved") {
			t.Errorf("layer 2 flagged a caller that passes the resolved wi.ID: %s", v)
		}
	}

	// A value reference to a contract function outside the declared aliases
	// must be reported — calls through an alias are invisible otherwise.
	aliased := `package server
var sneakyFn = ListDependencies
func handleSneaky(pool P, c C) {
	sneakyFn(ctx, pool, c.Param("id"), nil, "")
}`
	census = scanFixtureFiles(t, map[string]string{
		"internal/domain/dependencies.go": decl,
		"internal/server/fixture.go":      aliased,
	})
	found = false
	for _, v := range census.Violations {
		if strings.Contains(v, "referenced as a VALUE") && strings.Contains(v, "sneakyFn") {
			found = true
		}
	}
	if !found {
		t.Errorf("an undeclared alias of a contract function was not reported; violations: %v",
			census.Violations)
	}
}

// ─── The gate ────────────────────────────────────────────────────────────────

// TestCallerControlledWorkItemRefsAreResolved is the aihub#362 gate: every
// value bound to a work-item-id position in the repo's SQL proves it is
// canonical, or somebody wrote down why it need not.
func TestCallerControlledWorkItemRefsAreResolved(t *testing.T) {
	c, err := ScanRepo(repoRoot(t))
	if err != nil {
		t.Fatalf("scanning repo: %v", err)
	}
	t.Logf("census: %d files, %d mapped positions, %d dynamic fragments, %d contract calls, green=%v",
		c.FilesWalked, c.Positions, c.DynamicFragments, c.ContractCalls, c.GreenByKind)
	for _, v := range c.Violations {
		t.Error(v)
	}
}

// TestCensusStillSeesThePopulation is why a clean gate above means anything.
//
// The gate passes on zero violations — and it also passes when the recogniser
// finds no positions at all. Those are opposite facts with the same exit code.
// The floors sit well under the measured values (109 mapped positions, 111
// files, 42 contract calls when the gate landed) so ordinary churn does not
// trip them, and well over zero so a broken matcher does.
func TestCensusStillSeesThePopulation(t *testing.T) {
	const (
		minPositions     = 70
		minFiles         = 70
		minContractCalls = 25
	)
	c, err := ScanRepo(repoRoot(t))
	if err != nil {
		t.Fatalf("scanning repo: %v", err)
	}
	if c.FilesWalked < minFiles {
		t.Errorf("census visited only %d non-test .go files (floor %d); a walk that stops early "+
			"reports no violations perfectly", c.FilesWalked, minFiles)
	}
	if c.Positions < minPositions {
		t.Errorf("census mapped only %d work-item-id positions (floor %d). Either the SQL layer was "+
			"reworked — update the floor with the new measurement — or the recogniser stopped "+
			"recognising, and every green result above is 'not looked' wearing 'nothing wrong'.",
			c.Positions, minPositions)
	}
	if c.ContractCalls < minContractCalls {
		t.Errorf("the contract layer checked only %d call sites (floor %d); canonicalParams may "+
			"have rotted or the call matcher may be broken", c.ContractCalls, minContractCalls)
	}
	// Both halves of the classifier must be live: green kinds that vanish
	// entirely mean the trace stopped working (everything would be red — loud)
	// or the sink matcher stopped matching (silent, caught above); the
	// self-resolving and contract kinds specifically anchor the two mechanisms
	// this gate exists to enforce.
	for _, kind := range []string{"self-resolving", "resolver", "contract", "row-sourced"} {
		if c.GreenByKind[kind] == 0 {
			t.Errorf("no position classified %q anywhere in the repo; that mechanism of proof has "+
				"silently stopped being exercised", kind)
		}
	}
}

// TestExemptionsAreEarned keeps every escape hatch more expensive than
// compliance: each entry names a real file and carries a reason a reviewer can
// disagree with. (Stale entries — ones matching nothing — are rejected by the
// census itself.)
func TestExemptionsAreEarned(t *testing.T) {
	const minReasonLen = 40
	root := repoRoot(t)

	checkFile := func(kind, rel string) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s entry names a file that does not exist: %s (%v). Delete or move the entry — "+
				"leaving it means the census silently forgives whatever takes that path next.", kind, rel, err)
		}
	}
	for _, e := range canonicalParams {
		if len(e.Reason) < minReasonLen {
			t.Errorf("canonicalParams %s has a %d-character reason (minimum %d): a contract is a "+
				"claim about every caller; state it in a sentence.", contractKey(e.Func, e.Param), len(e.Reason), minReasonLen)
		}
		checkFile("canonicalParams", e.File)
	}
	for _, e := range siteExemptions {
		if len(e.Reason) < minReasonLen {
			t.Errorf("siteExemptions %s has a %d-character reason (minimum %d)", e.key(), len(e.Reason), minReasonLen)
		}
		checkFile("siteExemptions", e.File)
	}
	for _, e := range dynamicSQLExemptions {
		if len(e.Reason) < minReasonLen {
			t.Errorf("dynamicSQLExemptions %s has a %d-character reason (minimum %d)", e.key(), len(e.Reason), minReasonLen)
		}
		checkFile("dynamicSQLExemptions", e.File)
	}
	for _, e := range aliasContracts {
		if len(e.Reason) < minReasonLen {
			t.Errorf("aliasContracts %s has a %d-character reason (minimum %d)", e.key(), len(e.Reason), minReasonLen)
		}
		checkFile("aliasContracts", e.File)
	}
}
