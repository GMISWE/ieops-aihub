package domain

// DB-gated integration tests for aihub#261: a file_scope lock key must identify
// WHICH REPO the declared path is relative to.
//
// The key was "<project>:<repo-relative-path>" (aihub#222 added the project
// segment and stopped there). declared_resources paths are repo-relative, so in
// a multi-repo project the go.mod / go.sum / Makefile / README.md / Dockerfile /
// .github/workflows/*.yml of EVERY repo derive one key. The `ieops` project has
// 15 repos and almost all of them are Go repos with a go.mod.
//
// Measured on the pre-fix build, this file's own tests (see the RED evidence in
// the aihub#261 PR): claiming a work item that declares repo:<A> + file:go.mod,
// then claiming one that declares repo:<B> + file:go.mod, returns
//
//	409 CONFLICT_LOCK_TAKEN: resource file_scope:<project>:go.mod is already locked
//
// even though the two paths are two physically different files in two different
// repositories. That is the FALSE-CONFLICT direction, and it is a hard block on
// the real acquire path, not advisory.
//
// # Which direction the damage runs
//
// Both directions were measured, and only one of them reproduced (see
// TestFileScopeRepoKey_UnqualifiedDeclarationStillConflictsWithQualifiedHolder
// for the negative half). Pre-fix the key is strictly COARSER than the thing it
// names, so it over-blocks and never under-blocks: there is no pre-fix state in
// which two attempts hold what they believe are different files and are in fact
// the same file. The work item's "false protection" hypothesis (direction B)
// does NOT reproduce against the acquire path, because resource_locks has a
// UNIQUE (resource_type, resource_key) constraint — one coarse key still admits
// exactly one holder.
//
// That asymmetry is what constrains the fix. Making keys FINER is the direction
// that can introduce missed conflicts, so every test here that asserts "these
// two no longer collide" is paired with one that asserts "and this pair still
// does".
//
// # The four derivation sites
//
// derivedLock's doc comment names them, and a fix that reaches only some is the
// aihub#342 shape. Two of the four (FnClaimWorkItem, FnForceTakeover) unmarshal
// declared_resources into a LOCAL ANONYMOUS STRUCT with a hardcoded field list,
// so a new field on DeclaredResourceItem does not reach them at all — nothing to
// grep for, and the zero value reads as "no repo declared" and passes. Each site
// therefore gets an end-to-end test through its own entry point rather than a
// unit test of the helper:
//
//	conflicts.go    PredictConflicts rule 1  TestFileScopeRepoKey_PredictRule1NoHardBlockAcrossRepos
//	run_attempts.go FnClaimWorkItem          TestFileScopeRepoKey_ClaimDoesNotCollideAcrossRepos
//	run_attempts.go FnForceTakeover          TestFileScopeRepoKey_ForceTakeoverDerivesRepoQualifiedKey
//	run_attempts.go FnAcquireLocks           TestFileScopeRepoKey_AcquireLocksDoesNotCollideAcrossRepos
//
// and the fifth caller, derivedFileScopeLocks (the aihub#264 narrowing
// release), gets TestFileScopeRepoKey_NarrowingReleasesRepoQualifiedLock.
//
// Run:
//
//	AIHUB_TEST_DB=postgres://postgres@127.0.0.1:5561/aihub_test?sslmode=disable \
//	  go test ./internal/domain/ -run TestFileScopeRepoKey -count=1 -v

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// declaredWithRepo builds a declared_resources payload as RAW JSON rather than
// via DeclaredResourceItem, deliberately: the payload must be constructible
// against the PRE-FIX build so these tests fail on an assertion (the observable
// defect) instead of on a compile error (which proves nothing about behaviour).
func declaredWithRepo(repo, path string) json.RawMessage {
	entry := map[string]any{"type": "path", "uri": "file:" + path, "intent": "write"}
	if repo != "" {
		entry["repo"] = repo
	}
	raw, err := json.Marshal([]map[string]any{entry})
	if err != nil {
		panic(err)
	}
	return raw
}

const testSecret = "s3cr3t-0123456789abcdef0123456789abcdef0123456789abcdef01234567"

func seedWIWithResources(t *testing.T, pool *pgxpool.Pool, proj, uid, goal string, declared json.RawMessage) *WorkItem {
	t.Helper()
	wiType := "fix_bug"
	wi, aerr := CreateWorkItem(context.Background(), pool, &CreateWorkItemRequest{
		Project: proj, Goal: goal, Scenario: "coding", WIType: &wiType,
		DeclaredResources: declared, Source: "human",
		ForceCreate: true, ForceReason: "aihub#261 regression fixture",
	}, uid, "tester")
	if aerr != nil {
		t.Fatalf("CreateWorkItem(%s): %v", goal, aerr)
	}
	return wi
}

func claimWI(t *testing.T, pool *pgxpool.Pool, uid, wiID, idem string) (*ClaimResponse, *AihubError) {
	t.Helper()
	return FnClaimWorkItem(context.Background(), pool, wiID, &ClaimRequest{
		IdempotencyKey: idem,
		SessionInfo:    SessionInfo{MachineID: "m-261", SessionSecret: testSecret},
		Mode:           "fresh",
	}, uid, "", "tester")
}

func fileScopeKeys(locks []ResourceLock) []string {
	out := []string{}
	for _, l := range locks {
		if l.ResourceType == "file_scope" {
			out = append(out, l.ResourceKey)
		}
	}
	return out
}

// TestFileScopeRepoKey_ClaimDoesNotCollideAcrossRepos is the reported instance,
// on the path that reports it: FnClaimWorkItem. Two work items in ONE project
// each declare `go.mod` in a DIFFERENT repo. They are two files. Claiming both
// must succeed.
func TestFileScopeRepoKey_ClaimDoesNotCollideAcrossRepos(t *testing.T) {
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	wiA := seedWIWithResources(t, pool, proj, uid, "edit repo-a go.mod", declaredWithRepo("repo-a", "go.mod"))
	wiB := seedWIWithResources(t, pool, proj, uid, "edit repo-b go.mod", declaredWithRepo("repo-b", "go.mod"))

	claimA, aerr := claimWI(t, pool, uid, wiA.ID, "idem-261-a")
	if aerr != nil {
		t.Fatalf("claim A (must succeed, nothing else holds anything): %v", aerr)
	}
	gotA := fileScopeKeys(claimA.AcquiredLocks)
	wantA := proj + ":repo-a:go.mod"
	if len(gotA) != 1 || gotA[0] != wantA {
		t.Errorf("A's file_scope keys = %v, want [%q] — the key must name the repo the path is relative to", gotA, wantA)
	}

	claimB, aerr := claimWI(t, pool, uid, wiB.ID, "idem-261-b")
	if aerr != nil {
		t.Fatalf("claim B was BLOCKED by A even though they are different repos: %v\n"+
			"A holds %v; B wanted %s:repo-b:go.mod", aerr, gotA, proj)
	}
	gotB := fileScopeKeys(claimB.AcquiredLocks)
	wantB := proj + ":repo-b:go.mod"
	if len(gotB) != 1 || gotB[0] != wantB {
		t.Errorf("B's file_scope keys = %v, want [%q]", gotB, wantB)
	}
}

// TestFileScopeRepoKey_ClaimStillCollidesWithinOneRepo is the other half, and it
// is the one that must never regress: making the key finer must not stop two
// work items that really do target the SAME file in the SAME repo from
// conflicting.
func TestFileScopeRepoKey_ClaimStillCollidesWithinOneRepo(t *testing.T) {
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	wiA := seedWIWithResources(t, pool, proj, uid, "edit repo-a go.mod first", declaredWithRepo("repo-a", "go.mod"))
	wiB := seedWIWithResources(t, pool, proj, uid, "edit repo-a go.mod second", declaredWithRepo("repo-a", "go.mod"))

	if _, aerr := claimWI(t, pool, uid, wiA.ID, "idem-261-same-a"); aerr != nil {
		t.Fatalf("claim A: %v", aerr)
	}
	_, aerr := claimWI(t, pool, uid, wiB.ID, "idem-261-same-b")
	if aerr == nil {
		t.Fatalf("claim B SUCCEEDED — two attempts now believe they exclusively hold %s:repo-a:go.mod", proj)
	}
	if aerr.Code != ErrConflictLockTaken {
		t.Errorf("claim B error code = %q, want %q", aerr.Code, ErrConflictLockTaken)
	}
}

// TestFileScopeRepoKey_UnqualifiedDeclarationStillConflictsWithQualifiedHolder
// pins the no-missed-conflict guarantee across the adoption boundary, in BOTH
// directions.
//
// This is the test that decides whether the fix is safe to ship at all. A
// declaration that does not name its repo means "some repo in this project, I
// am not saying which" — so it cannot be assumed to be a different file from a
// repo-qualified holder. It must keep conflicting, in both orders, or the fix
// trades a noisy false conflict for a silent missed one.
//
// Both orders are exercised because they take DIFFERENT code paths: a qualified
// candidate probes for the legacy unqualified key, while an unqualified
// candidate must probe for every repo-qualified variant.
func TestFileScopeRepoKey_UnqualifiedDeclarationStillConflictsWithQualifiedHolder(t *testing.T) {
	// setupLatestTestDB is called HERE, not inside the subtests, so this function
	// itself SKIPs without a database. dbtestcov counts top-level functions that
	// skip and names that variable; a function whose skip happens one level down
	// is invisible to it — not listed in gated_tests.txt, not required to be run
	// by any CI step, and therefore silently never executed. That is the
	// "coverage gate passes because the check is absent" shape, and this is the
	// test it would have hidden.
	pool := setupLatestTestDB(t)

	t.Run("qualified_holder_blocks_unqualified_claimer", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)

		holder := seedWIWithResources(t, pool, proj, uid, "qualified holder", declaredWithRepo("repo-a", "Makefile"))
		claimer := seedWIWithResources(t, pool, proj, uid, "unqualified claimer", declaredWithRepo("", "Makefile"))

		if _, aerr := claimWI(t, pool, uid, holder.ID, "idem-261-qh"); aerr != nil {
			t.Fatalf("claim holder: %v", aerr)
		}
		if _, aerr := claimWI(t, pool, uid, claimer.ID, "idem-261-uc"); aerr == nil {
			t.Fatalf("an unqualified declaration of Makefile was allowed alongside a holder of %s:repo-a:Makefile — "+
				"a declaration that does not name its repo may BE repo-a, so this is a missed conflict", proj)
		}
	})

	t.Run("unqualified_holder_blocks_qualified_claimer", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)

		holder := seedWIWithResources(t, pool, proj, uid, "unqualified holder", declaredWithRepo("", "Makefile"))
		claimer := seedWIWithResources(t, pool, proj, uid, "qualified claimer", declaredWithRepo("repo-a", "Makefile"))

		if _, aerr := claimWI(t, pool, uid, holder.ID, "idem-261-uh"); aerr != nil {
			t.Fatalf("claim holder: %v", aerr)
		}
		if _, aerr := claimWI(t, pool, uid, claimer.ID, "idem-261-qc"); aerr == nil {
			t.Fatalf("a repo-qualified declaration was allowed alongside an unqualified holder of %s:Makefile — "+
				"the legacy key may name the same file, so this is a missed conflict", proj)
		}
	})
}

// TestFileScopeRepoKey_ForceTakeoverDerivesRepoQualifiedKey covers the site that
// cannot be reached by testing derivedLock: FnForceTakeover re-derives locks
// from a LOCAL anonymous struct whose field list is written out by hand. A new
// field that is not added there is silently dropped, which is precisely how the
// same class of bug survived aihub#342 in this exact function.
func TestFileScopeRepoKey_ForceTakeoverDerivesRepoQualifiedKey(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	wi := seedWIWithResources(t, pool, proj, uid, "taken over", declaredWithRepo("repo-a", "go.sum"))
	if _, aerr := claimWI(t, pool, uid, wi.ID, "idem-261-ft"); aerr != nil {
		t.Fatalf("initial claim: %v", aerr)
	}

	if _, aerr := FnForceTakeover(ctx, pool, wi.ID, uid, "taker", "admin",
		map[string]string{proj: "owner"},
		&ForceTakeoverRequest{Reason: "aihub#261 derivation coverage",
			SessionInfo: SessionInfo{MachineID: "m-261-ft", SessionSecret: testSecret}},
	); aerr != nil {
		t.Fatalf("force_takeover: %v", aerr)
	}

	var key string
	if err := pool.QueryRow(ctx, `
		SELECT rl.resource_key FROM resource_locks rl
		JOIN run_attempts ra ON ra.id = rl.owner_attempt_id
		WHERE ra.work_item_id=$1 AND rl.resource_type='file_scope'`, wi.ID).Scan(&key); err != nil {
		t.Fatalf("read lock after takeover: %v", err)
	}
	if want := proj + ":repo-a:go.sum"; key != want {
		t.Errorf("force_takeover re-derived file_scope key %q, want %q — "+
			"this site unmarshals into a local struct, so the repo field must be listed there too", key, want)
	}
}

// TestFileScopeRepoKey_AcquireLocksDoesNotCollideAcrossRepos covers the
// pf_acquire_locks site — the mid-attempt reconcile path, which derives its own
// targets and has its own collision SQL.
func TestFileScopeRepoKey_AcquireLocksDoesNotCollideAcrossRepos(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	// A holds repo-a's Dockerfile.
	wiA := seedWIWithResources(t, pool, proj, uid, "holds repo-a Dockerfile", declaredWithRepo("repo-a", "Dockerfile"))
	if _, aerr := claimWI(t, pool, uid, wiA.ID, "idem-261-al-a"); aerr != nil {
		t.Fatalf("claim A: %v", aerr)
	}

	// B claims with NO declared resources, then declares repo-b's Dockerfile and
	// reconciles via acquire_locks — the sequence pf_acquire_locks exists for.
	wiB := seedWIWithResources(t, pool, proj, uid, "later declares repo-b Dockerfile", json.RawMessage(`[]`))
	claimB, aerr := claimWI(t, pool, uid, wiB.ID, "idem-261-al-b")
	if aerr != nil {
		t.Fatalf("claim B: %v", aerr)
	}
	mustExec(t, pool, `UPDATE work_items SET declared_resources='`+
		string(declaredWithRepo("repo-b", "Dockerfile"))+`'::jsonb WHERE id='`+wiB.ID+`'`)

	resp, aerr := FnAcquireLocks(ctx, pool, wiB.ID, &AcquireLocksRequest{
		AttemptID: claimB.AttemptID, ClaimEpoch: claimB.ClaimEpoch, SessionSecret: testSecret,
	})
	if aerr != nil {
		t.Fatalf("acquire_locks for repo-b's Dockerfile was blocked by repo-a's: %v", aerr)
	}
	got := fileScopeKeys(resp.Acquired)
	if want := proj + ":repo-b:Dockerfile"; len(got) != 1 || got[0] != want {
		t.Errorf("acquire_locks acquired %v, want [%q]", got, want)
	}
}

// TestFileScopeRepoKey_PredictRule1NoHardBlockAcrossRepos covers PredictConflicts
// rule 1, the pre-claim gate. It must agree with what claim actually does, or
// the gate has no predictive value (aihub#342). dry_run is false here on purpose:
// rule 1 is skipped when dry_run is true.
func TestFileScopeRepoKey_PredictRule1NoHardBlockAcrossRepos(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	wiA := seedWIWithResources(t, pool, proj, uid, "holds repo-a README", declaredWithRepo("repo-a", "README.md"))
	if _, aerr := claimWI(t, pool, uid, wiA.ID, "idem-261-pr"); aerr != nil {
		t.Fatalf("claim A: %v", aerr)
	}
	roles := map[string]string{proj: "owner"}

	// Different repo, same path: no hard block.
	resp, aerr := PredictConflicts(ctx, pool, &PredictConflictsRequest{
		Project: proj, DeclaredResources: declaredWithRepo("repo-b", "README.md"), DryRun: false,
	}, roles)
	if aerr != nil {
		t.Fatalf("predict repo-b: %v", aerr)
	}
	for _, p := range resp.Predictions {
		if p.Rule == 1 {
			t.Errorf("rule 1 hard-blocked repo-b's README.md on repo-a's lock (%s); "+
				"claim allows this pair, so the pre-claim gate now disagrees with claim", p.ResourceKey)
		}
	}

	// Same repo, same path: rule 1 must still fire.
	resp, aerr = PredictConflicts(ctx, pool, &PredictConflictsRequest{
		Project: proj, DeclaredResources: declaredWithRepo("repo-a", "README.md"), DryRun: false,
	}, roles)
	if aerr != nil {
		t.Fatalf("predict repo-a: %v", aerr)
	}
	sawRule1 := false
	for _, p := range resp.Predictions {
		if p.Rule == 1 {
			sawRule1 = true
		}
	}
	if !sawRule1 {
		t.Errorf("rule 1 did NOT fire for the same repo + same path — predictions=%+v", resp.Predictions)
	}
}

// TestFileScopeRepoKey_NarrowingReleasesRepoQualifiedLock covers the fifth
// caller, derivedFileScopeLocks (aihub#264). It compares derived keys against
// the rows actually held, so if it derived an unqualified key while claim
// inserted a qualified one, a narrowing would release NOTHING and the lock would
// be held until the attempt ended — silently.
func TestFileScopeRepoKey_NarrowingReleasesRepoQualifiedLock(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	both, err := json.Marshal([]map[string]any{
		{"type": "path", "uri": "file:go.mod", "intent": "write", "repo": "repo-a"},
		{"type": "path", "uri": "file:go.sum", "intent": "write", "repo": "repo-a"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wi := seedWIWithResources(t, pool, proj, uid, "narrows later", both)
	if _, aerr := claimWI(t, pool, uid, wi.ID, "idem-261-narrow"); aerr != nil {
		t.Fatalf("claim: %v", aerr)
	}

	narrowed := declaredWithRepo("repo-a", "go.mod")
	if _, aerr := UpdateWorkItem(ctx, pool, wi.ID, uid, "admin", map[string]string{proj: "owner"},
		&UpdateWorkItemRequest{DeclaredResources: narrowed}); aerr != nil {
		t.Fatalf("narrowing update: %v", aerr)
	}

	rows, err := pool.Query(ctx, `
		SELECT rl.resource_key FROM resource_locks rl
		JOIN run_attempts ra ON ra.id = rl.owner_attempt_id
		WHERE ra.work_item_id=$1 AND rl.resource_type='file_scope' ORDER BY 1`, wi.ID)
	if err != nil {
		t.Fatalf("query locks: %v", err)
	}
	defer rows.Close()
	var held []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scan: %v", err)
		}
		held = append(held, k)
	}
	want := proj + ":repo-a:go.mod"
	if len(held) != 1 || held[0] != want {
		t.Errorf("after narrowing, held file_scope keys = %v, want [%q] — "+
			"a derivation mismatch here orphans the dropped lock instead of releasing it", held, want)
	}
	if strings.Join(held, ",") == proj+":go.mod" {
		t.Errorf("keys are unqualified: the release path derived a different format from the acquire path")
	}
}

// TestFileScopeRepoKey_ProbeSQLAndGoAgree pins the one contract that has two
// implementations: lockConflictProbe is applied as SQL by the three conflict
// sites (lockConflictWhereClause) and as Go by PredictConflicts rule 3
// (probe.Matches, via Overlaps). Two implementations of one predicate drift, and
// the drift is invisible — each half looks right on its own, and the pair is
// only wrong for inputs nobody wrote a case for.
//
// The escaping rows are the reason this is a DB test rather than a unit test.
// `_` is a LIKE wildcard and appears in most Go filenames in this repo
// (run_attempts.go, work_items.go), so an unescaped pattern matches OTHER files
// and the Go mirror does not — a divergence no pure-Go test of Matches can see,
// because the bug lives in the string handed to Postgres.
func TestFileScopeRepoKey_ProbeSQLAndGoAgree(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	candidates := []struct{ project, repo, uri string }{
		{"proj", "", "file:go.mod"},
		{"proj", "repo-a", "file:go.mod"},
		{"proj", "repo-b", "file:go.mod"},
		{"proj", "", "file:internal/run_attempts.go"},
		{"proj", "repo-a", "file:internal/run_attempts.go"},
		{"proj", "", "file:100%_done.md"},
		{"proj", "repo-a", "file:100%_done.md"},
		{"pro_j", "", "file:go.mod"},
		{"proj", "", `file:a\\b.go`},
		{"proj", "repo-a", `file:a\\b.go`},
	}
	existing := []string{
		"proj:go.mod",
		"proj:repo-a:go.mod",
		"proj:repo-b:go.mod",
		"proj:repo-a:go.sum",
		"projX:go.mod",
		"proj:internal/run_attempts.go",
		"proj:repo-a:internal/run_attempts.go",
		"proj:repo-a:internal/runXattempts.go", // `_` as a LIKE wildcard would match this
		"proj:100%_done.md",
		"proj:repo-a:100%_done.md",
		"proj:repo-a:100XYdone.md",
		"pro_j:go.mod",
		"proXj:go.mod", // `_` in the PROJECT name, same trap one segment left
		"pro_j:repo-a:go.mod",
		`proj:a\\b.go`,
		`proj:repo-a:a\\b.go`,
		"proj:repo-a:aXb.go", // an unescaped backslash-escape would let this through
		"proj::go.mod",       // the empty repo segment: `%` matches zero characters
	}

	for _, c := range candidates {
		probe := fileScopeConflictProbe(c.project, c.repo, c.uri)
		for _, e := range existing {
			var sqlMatch bool
			// The predicate is spliced verbatim from the production constant, so a
			// change to lockConflictWhereClause is exercised here rather than
			// re-typed. `rl` is aliased because the constant names that table.
			if err := pool.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM (SELECT $4::text AS resource_key, 'file_scope'::text AS resource_type) rl
				 WHERE `+lockConflictWhereClause+`)`,
				"file_scope", probe.Keys, probe.LikePattern, e,
			).Scan(&sqlMatch); err != nil {
				t.Fatalf("probe SQL for candidate %+v vs %q: %v", c, e, err)
			}
			if goMatch := probe.Matches(e); goMatch != sqlMatch {
				t.Errorf("candidate %+v vs existing %q: SQL says %v, Go says %v (keys=%v pattern=%q)",
					c, e, sqlMatch, goMatch, probe.Keys, probe.LikePattern)
			}
		}
	}
}

// TestFileScopeRepoKey_RepoDefaultIsSingleRepoOnly pins the inference rule that
// gives aihub#261 any reach at all today, and — more importantly — pins where it
// STOPS. No polyforge skill emits a `repo` field on a path entry, so without the
// inference the fix would be inert; with it, a work item that declares one repo
// alongside its paths is qualified for free. Two declared repos must NOT guess:
// a wrong repo segment is a missed conflict, while no segment is merely a
// conservative one.
func TestFileScopeRepoKey_RepoDefaultIsSingleRepoOnly(t *testing.T) {
	path := DeclaredResourceItem{Type: "path", URI: "file:go.mod", Intent: "write"}
	repoA := DeclaredResourceItem{Type: "repo", URI: "repo:repo-a"}
	repoB := DeclaredResourceItem{Type: "repo", URI: "repo:repo-b"}
	explicit := DeclaredResourceItem{Type: "path", URI: "file:go.mod", Intent: "write", Repo: "repo-z"}

	cases := []struct {
		name  string
		items []DeclaredResourceItem
		want  string
	}{
		{"no repo declared", []DeclaredResourceItem{path}, "p:go.mod"},
		{"one repo declared", []DeclaredResourceItem{repoA, path}, "p:repo-a:go.mod"},
		{"two repos declared is ambiguous", []DeclaredResourceItem{repoA, repoB, path}, "p:go.mod"},
		{"same repo twice is not ambiguous", []DeclaredResourceItem{repoA, repoA, path}, "p:repo-a:go.mod"},
		{"explicit repo beats the inferred one", []DeclaredResourceItem{repoA, explicit}, "p:repo-z:go.mod"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolved := resolveDeclaredRepos(tc.items)
			var got string
			for _, r := range resolved {
				if lt, lk := derivedLock(r, "p"); lt == "file_scope" {
					got = lk
				}
			}
			if got != tc.want {
				t.Errorf("derived file_scope key = %q, want %q", got, tc.want)
			}
		})
	}
}

// declaredViaRepoEntry builds a payload whose path entry carries NO repo field,
// relying on the {"type":"repo"} entry for the repo — the INFERENCE half of
// aihub#261, which is the half that works without any client change.
func declaredViaRepoEntry(repo, taskBranch, path string) json.RawMessage {
	raw, err := json.Marshal([]map[string]any{
		{"type": "repo", "uri": "repo:" + repo, "intent": "write", "task_branch": taskBranch},
		{"type": "path", "uri": "file:" + path, "intent": "write"},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

// TestFileScopeRepoKey_InferredRepoReachesEveryDerivationSite closes the hole
// that every other test in this file leaves open.
//
// The rest of the file declares the repo EXPLICITLY, as {"type":"path",...,
// "repo":"repo-a"}. An explicit field survives a plain json.Unmarshal, so those
// tests prove the repo reaches each site — but not that the site ran the
// PRE-PASS that infers a repo from the payload's own {"type":"repo"} entry.
// resolveDeclaredRepos is whole-payload and therefore a separate call at each
// site, exactly the kind of thing one site can be missing.
//
// Measured, and the reason this test exists: deleting the
// `resolveDeclaredRepos` call from FnAcquireLocks left the ENTIRE rest of this
// file green. A unit test of resolveDeclaredRepos would have stayed green too —
// pinning a helper while a call site bypasses it is prevention that cannot reach
// the instances it was filed for.
//
// The inference is also the only half that has any reach today: no polyforge
// skill emits a `repo` field on a path entry, so if the pre-pass silently stops
// running at a site, aihub#261 is a no-op there and nothing else notices.
func TestFileScopeRepoKey_InferredRepoReachesEveryDerivationSite(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	t.Run("claim", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		wi := seedWIWithResources(t, pool, proj, uid, "inferred repo at claim",
			declaredViaRepoEntry("repo-a", "pf261-claim", "go.mod"))
		claim, aerr := claimWI(t, pool, uid, wi.ID, "idem-261-inf-claim")
		if aerr != nil {
			t.Fatalf("claim: %v", aerr)
		}
		got := fileScopeKeys(claim.AcquiredLocks)
		if want := proj + ":repo-a:go.mod"; len(got) != 1 || got[0] != want {
			t.Errorf("claim derived %v, want [%q] — the repo pre-pass did not run at this site", got, want)
		}
	})

	t.Run("force_takeover", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		wi := seedWIWithResources(t, pool, proj, uid, "inferred repo at takeover",
			declaredViaRepoEntry("repo-a", "pf261-ft", "go.sum"))
		if _, aerr := claimWI(t, pool, uid, wi.ID, "idem-261-inf-ft"); aerr != nil {
			t.Fatalf("claim: %v", aerr)
		}
		if _, aerr := FnForceTakeover(ctx, pool, wi.ID, uid, "taker", "admin",
			map[string]string{proj: "owner"},
			&ForceTakeoverRequest{Reason: "aihub#261 inference coverage",
				SessionInfo: SessionInfo{MachineID: "m-261-inf", SessionSecret: testSecret}},
		); aerr != nil {
			t.Fatalf("force_takeover: %v", aerr)
		}
		var key string
		if err := pool.QueryRow(ctx, `
			SELECT rl.resource_key FROM resource_locks rl
			JOIN run_attempts ra ON ra.id = rl.owner_attempt_id
			WHERE ra.work_item_id=$1 AND rl.resource_type='file_scope'`, wi.ID).Scan(&key); err != nil {
			t.Fatalf("read lock: %v", err)
		}
		if want := proj + ":repo-a:go.sum"; key != want {
			t.Errorf("force_takeover derived %q, want %q — the repo pre-pass did not run at this site", key, want)
		}
	})

	t.Run("acquire_locks", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		wi := seedWIWithResources(t, pool, proj, uid, "inferred repo at acquire_locks", json.RawMessage(`[]`))
		claim, aerr := claimWI(t, pool, uid, wi.ID, "idem-261-inf-al")
		if aerr != nil {
			t.Fatalf("claim: %v", aerr)
		}
		mustExec(t, pool, `UPDATE work_items SET declared_resources='`+
			string(declaredViaRepoEntry("repo-a", "pf261-al", "Dockerfile"))+`'::jsonb WHERE id='`+wi.ID+`'`)
		resp, aerr := FnAcquireLocks(ctx, pool, wi.ID, &AcquireLocksRequest{
			AttemptID: claim.AttemptID, ClaimEpoch: claim.ClaimEpoch, SessionSecret: testSecret,
		})
		if aerr != nil {
			t.Fatalf("acquire_locks: %v", aerr)
		}
		got := fileScopeKeys(resp.Acquired)
		if want := proj + ":repo-a:Dockerfile"; len(got) != 1 || got[0] != want {
			t.Errorf("acquire_locks derived %v, want [%q] — the repo pre-pass did not run at this site", got, want)
		}
	})

	t.Run("predict_rule_1", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		wi := seedWIWithResources(t, pool, proj, uid, "inferred repo holder",
			declaredViaRepoEntry("repo-a", "pf261-pr", "README.md"))
		if _, aerr := claimWI(t, pool, uid, wi.ID, "idem-261-inf-pr"); aerr != nil {
			t.Fatalf("claim: %v", aerr)
		}
		roles := map[string]string{proj: "owner"}
		// A different repo, inferred the same way, must not hard-block.
		resp, aerr := PredictConflicts(ctx, pool, &PredictConflictsRequest{
			Project: proj, DeclaredResources: declaredViaRepoEntry("repo-b", "pf261-pr2", "README.md"), DryRun: false,
		}, roles)
		if aerr != nil {
			t.Fatalf("predict repo-b: %v", aerr)
		}
		for _, p := range resp.Predictions {
			if p.Rule == 1 && p.ResourceType == "file_scope" {
				t.Errorf("rule 1 hard-blocked repo-b on repo-a's lock (%s) — the repo pre-pass did not run at this site", p.ResourceKey)
			}
		}
		// The same repo must still hard-block, or the arm above proves nothing.
		resp, aerr = PredictConflicts(ctx, pool, &PredictConflictsRequest{
			Project: proj, DeclaredResources: declaredViaRepoEntry("repo-a", "pf261-pr3", "README.md"), DryRun: false,
		}, roles)
		if aerr != nil {
			t.Fatalf("predict repo-a: %v", aerr)
		}
		sawFileScopeRule1 := false
		for _, p := range resp.Predictions {
			if p.Rule == 1 && p.ResourceType == "file_scope" {
				sawFileScopeRule1 = true
			}
		}
		if !sawFileScopeRule1 {
			t.Errorf("rule 1 did not fire for the SAME inferred repo — predictions=%+v", resp.Predictions)
		}
	})

	t.Run("narrowing_release", func(t *testing.T) {
		uid := testUser(t, pool)
		proj := testProject(t, pool, uid)
		both, err := json.Marshal([]map[string]any{
			{"type": "repo", "uri": "repo:repo-a", "intent": "write", "task_branch": "pf261-nr"},
			{"type": "path", "uri": "file:go.mod", "intent": "write"},
			{"type": "path", "uri": "file:go.sum", "intent": "write"},
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		wi := seedWIWithResources(t, pool, proj, uid, "inferred repo, narrowed later", both)
		if _, aerr := claimWI(t, pool, uid, wi.ID, "idem-261-inf-nr"); aerr != nil {
			t.Fatalf("claim: %v", aerr)
		}
		if _, aerr := UpdateWorkItem(ctx, pool, wi.ID, uid, "admin", map[string]string{proj: "owner"},
			&UpdateWorkItemRequest{DeclaredResources: declaredViaRepoEntry("repo-a", "pf261-nr", "go.mod")}); aerr != nil {
			t.Fatalf("narrowing update: %v", aerr)
		}
		rows, err := pool.Query(ctx, `
			SELECT rl.resource_key FROM resource_locks rl
			JOIN run_attempts ra ON ra.id = rl.owner_attempt_id
			WHERE ra.work_item_id=$1 AND rl.resource_type='file_scope' ORDER BY 1`, wi.ID)
		if err != nil {
			t.Fatalf("query locks: %v", err)
		}
		defer rows.Close()
		var held []string
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				t.Fatalf("scan: %v", err)
			}
			held = append(held, k)
		}
		if want := proj + ":repo-a:go.mod"; len(held) != 1 || held[0] != want {
			t.Errorf("after narrowing, held = %v, want [%q] — the repo pre-pass did not run on the release path", held, want)
		}
	})
}

// TestFileScopeRepoKey_DroppingTheRepoEntryDoesNotReleaseStillDeclaredLocks is
// the regression that repo-qualified keys introduce into aihub#264's release.
//
// releaseUndeclaredFileScopeLocks releases `derived(prior) − derived(next)`.
// That subtraction assumed a derived key changes only when its PATH is dropped.
// Once the key carries a repo, it also changes when the payload's repo inference
// changes with the paths untouched — and then every still-declared path looks
// "removed", its lock is deleted, and nothing re-acquires it.
//
// This is the ordinary polyforge flow, not a corner case: pf-plan Step 5
// (plugins/polyforge/skills/pf-plan/SKILL.md) derives declared_resources from
// the plan's `Touched files:` lines as PATH ENTRIES ONLY and writes them as a
// whole-list replace. So a work item claimed with a {"type":"repo"} entry holds
// repo-qualified locks, and the very next /pf-plan drops the repo entry — which
// must not drop the locks.
func TestFileScopeRepoKey_DroppingTheRepoEntryDoesNotReleaseStillDeclaredLocks(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	withRepo, err := json.Marshal([]map[string]any{
		{"type": "repo", "uri": "repo:repo-a", "intent": "write", "task_branch": "pf261-drop"},
		{"type": "path", "uri": "file:go.mod", "intent": "write"},
		{"type": "path", "uri": "file:go.sum", "intent": "write"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wi := seedWIWithResources(t, pool, proj, uid, "loses its locks when the repo entry goes away", withRepo)
	claim, aerr := claimWI(t, pool, uid, wi.ID, "idem-261-drop")
	if aerr != nil {
		t.Fatalf("claim: %v", aerr)
	}
	if got := len(fileScopeKeys(claim.AcquiredLocks)); got != 2 {
		t.Fatalf("fixture check: claim took %d file_scope locks, want 2", got)
	}

	// pf-plan's rewrite: same two paths, no repo entry.
	pathsOnly, err := json.Marshal([]map[string]any{
		{"type": "path", "uri": "file:go.mod", "intent": "write"},
		{"type": "path", "uri": "file:go.sum", "intent": "write"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, aerr := UpdateWorkItem(ctx, pool, wi.ID, uid, "admin", map[string]string{proj: "owner"},
		&UpdateWorkItemRequest{DeclaredResources: pathsOnly}); aerr != nil {
		t.Fatalf("update: %v", aerr)
	}

	rows, err := pool.Query(ctx, `
		SELECT rl.resource_key FROM resource_locks rl
		JOIN run_attempts ra ON ra.id = rl.owner_attempt_id
		WHERE ra.work_item_id=$1 AND rl.resource_type='file_scope' ORDER BY 1`, wi.ID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var held []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scan: %v", err)
		}
		held = append(held, k)
	}
	if len(held) != 2 {
		t.Errorf("after dropping the repo entry the attempt holds %v (%d locks), want 2 — "+
			"both paths are STILL declared, so neither lock may be released; "+
			"the work item is now running with no write protection", held, len(held))
	}
}

// TestFileScopeRepoKey_WrongTypedRepoDoesNotZeroOutEveryLock. Adding a
// caller-controllable field to a STRICTLY decoded payload adds a way to make the
// whole payload derive nothing.
//
// ValidateDeclaredResources decodes into []map[string]any and only type-asserts
// `type` and `uri`, so {"repo":123} is stored with a 200. A typed unmarshal then
// fails on that one field with an UnmarshalTypeError and takes the WHOLE array
// down, so the work item claims with NO locks at all and no error anywhere —
// the "fake all-clear" aihub#238 exists to remove. derivedFileScopeLocks
// already documents this exact hazard for `intent`; the shared decoder must not
// reintroduce it for `repo`.
func TestFileScopeRepoKey_WrongTypedRepoDoesNotZeroOutEveryLock(t *testing.T) {
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	bad, err := json.Marshal([]map[string]any{
		{"type": "path", "uri": "file:go.mod", "intent": "write", "repo": 123},
		{"type": "path", "uri": "file:go.sum", "intent": "write", "repo": "repo-a"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wi := seedWIWithResources(t, pool, proj, uid, "wrong-typed repo field", bad)
	claim, aerr := claimWI(t, pool, uid, wi.ID, "idem-261-badrepo")
	if aerr != nil {
		t.Fatalf("claim: %v", aerr)
	}
	got := fileScopeKeys(claim.AcquiredLocks)
	// The bad entry degrades to "no repo declared" (conservative, over-blocking);
	// the good entry keeps its repo. Neither may vanish.
	want := []string{proj + ":go.mod", proj + ":repo-a:go.sum"}
	if len(got) != 2 {
		t.Fatalf("claim acquired %v, want %v — one wrong-typed optional field silently disabled every lock", got, want)
	}
	sort.Strings(got)
	sort.Strings(want)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("claim acquired %v, want %v", got, want)
			break
		}
	}
}

// TestFileScopeRepoKey_RepoNameIsTrimmed. A repo name is spliced into the lock
// key verbatim, so "repo-a " and "repo-a" would key differently and two work
// items editing the same physical file would NOT conflict — a missed conflict
// produced purely by whitespace. A whitespace-only repo must mean "unspecified",
// not a repo literally named " ".
func TestFileScopeRepoKey_RepoNameIsTrimmed(t *testing.T) {
	if got, want := fileScopeLockKey("p", "repo-a", "file:go.mod"), "p:repo-a:go.mod"; got != want {
		t.Errorf("fileScopeLockKey = %q, want %q", got, want)
	}
	items := []DeclaredResourceItem{
		{Type: "repo", URI: "repo: repo-a "},
		{Type: "path", URI: "file:go.mod", Intent: "write"},
	}
	if _, k := derivedLock(resolveDeclaredRepos(items)[1], "p"); k != "p:repo-a:go.mod" {
		t.Errorf("inferred-from-repo-entry key = %q, want %q (repo name must be trimmed)", k, "p:repo-a:go.mod")
	}
	explicit := DeclaredResourceItem{Type: "path", URI: "file:go.mod", Intent: "write", Repo: " repo-a "}
	if _, k := derivedLock(explicit, "p"); k != "p:repo-a:go.mod" {
		t.Errorf("explicit repo key = %q, want %q (repo name must be trimmed)", k, "p:repo-a:go.mod")
	}
	blank := DeclaredResourceItem{Type: "path", URI: "file:go.mod", Intent: "write", Repo: "   "}
	if _, k := derivedLock(blank, "p"); k != "p:go.mod" {
		t.Errorf("whitespace-only repo key = %q, want %q (must mean unspecified)", k, "p:go.mod")
	}
}
