package domain

// aihub#416 acceptance criteria that only a database can answer.
//
//	AC-2   a claim declaring only repo + service holds ZERO resource_locks rows
//	AC-3   two work items declaring the SAME repo both claim (no 409)
//	AC-4   two work items declaring the SAME service both claim (no 409)
//	AC-14  predict rule 2 reports soft_block from a DECLARATION join, and its
//	       description no longer says "branch"
//	AC-15  predict rule 6 reports exactly one info per service and does not raise
//	       the top-level severity
//	AC-16  rule 6 still fires under dry_run=true
//	AC-17  last_active_age_seconds tracks run_attempts.last_active_at
//	AC-18  a repo-only or service-only payload never returns hard_block
//	AC-19  migration 0038 left the two retired lock types at zero rows, each with
//	       a lock_released event carrying cause=derivation_retired
//	       + run_attempts.repo_pins round-trips through FnRecordRepoPins
//
// ─── Why these need a database and the unit arms do not ────────────────────
//
// lock_derivation_retired_test.go proves the MAPPER derives nothing and the
// CLAIM'S DERIVATION contributes nothing. Neither can see the row count, and the
// row count is the whole claim of this work item: "a wi declaring a repo
// serialises every other wi on that repo" was a statement about resource_locks,
// and only resource_locks can refute it. AC-3 in particular is the reason this
// work item exists, so it gets a test that fails on the pre-change tree by
// returning 409 CONFLICT_LOCK_TAKEN rather than by an assertion on a mapper.
//
// Predict is here for a sharper reason. Rule 2 used to read the lock table with
// 'git_branch' hardcoded in SQL, which BYPASSED resourceToLock — so the mutant
// that matters ("revert rule 2 to reading resource_locks") is invisible to every
// unit test in this package, and produces ZERO PREDICTIONS, which is
// byte-identical to "no conflict". That is aihub#238's fake all-clear, and only
// a DB test with two really-running work items can tell the two apart.
//
// ─── Mutants, measured ─────────────────────────────────────────────────────
//
//	M1  restore `case "repo": return "git_branch", ...` in resourceToLock
//	    -> AC-2/AC-3 red (two claims, second is 409 CONFLICT_LOCK_TAKEN)
//	M2  restore `case "service": return "deploy_env", svc`
//	    -> AC-2/AC-4 red
//	M3  revert rule 2's query to the resource_locks form
//	    -> AC-14 red with ZERO predictions, which is exactly the failure mode
//	       the assertion is worded to catch
//	M4  delete rule 6
//	    -> AC-15/AC-16/AC-17 red
//	M5  make rule 6 set result.Severity = SeveritySoftBlock
//
// aihub#510 later added the self-exclusion arms at the END of
// TestDeLockingPredictReportsAdvisoryEntries: the same four rules reported the
// CALLER back to itself, because a claimed work item asking about its own
// declarations satisfies both halves of their join. Its mutants (M11-M14) are
// listed beside that subtest rather than here, because the reason each one is
// visible is the two-name fixture it is measured against.
//
// aihub#511 earlier added the containment-operand arms to the same function —
// the four rules that read declarations built the right-hand side of `@>` as
// concatenated JSON text.
// Their mutants, measured 2026-09-09 against a local Postgres 16:
//
//	M6-M9  restore the concatenated literal in rule 2 / 4 / 5 / 6
//	       -> the doublequote and backslash arms go red for exactly that rule
//	          while the plain arm stays green, and the DB-free source guard
//	          (TestPredictContainmentOperandsAreNotConcatenatedJSON) goes red too
//	M10    drop the ::text casts from declaresContainmentSQL
//	       -> the PLAIN arm goes red as well (rules 2, 5 and 6), which is what
//	          makes the "the casts are load-bearing" comment a measurement
//	    -> AC-15 red on the top-level severity. ⚠️ AC-18 stays GREEN under M5,
//	       measured rather than assumed: soft_block is not hard_block, so the
//	       ceiling assertion is satisfied by the very leak AC-15 catches. The two
//	       arms are not redundant — AC-18 bounds the ceiling, AC-15 pins the
//	       exact value, and only the second sees this mutant.
//
// Run:
//
//	AIHUB_TEST_DB=postgres://.../aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run 'TestDeLocking' -v -count=1

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// advisoryOnly is the payload at the centre of this work item: a repo and a
// service, and nothing that locks.
const advisoryOnly = `[{"type":"repo","uri":"repo:aihub","intent":"write"},` +
	`{"type":"service","uri":"service:aihub","intent":"write"}]`

// TestDeLockingClaimTakesNoRepoOrServiceLock is AC-2, AC-3 and AC-4.
//
// One function with subtests: internal/citest/dbtestcov counts DB-gated
// FUNCTIONS, and ci.yml asserts `--- PASS:` per subtest, so the per-criterion
// claim belongs in the subtest name.
func TestDeLockingClaimTakesNoRepoOrServiceLock(t *testing.T) {
	pool := setupLatestTestDB(t)

	u := testUser(t, pool)
	project := testProject(t, pool, u)

	// AC-2. The assertion is on the TABLE, not on ClaimResponse.AcquiredLocks:
	// the response is built from the same slice the derivation filled, so an
	// insert that happened without being reported — or reported without
	// happening — would agree with itself.
	t.Run("a claim declaring only repo and service holds zero lock rows", func(t *testing.T) {
		wi := seedClaimableWI(t, pool, project, u, "declare a repo and a service and lock nothing", advisoryOnly)
		claim := claimFresh(t, pool, wi.ID, u, "aihub416-ac2")

		require.Empty(t, heldLockKeys(t, pool, claim.AttemptID),
			"a wi declaring only a repo and a service holds resource_locks rows. Nothing releases "+
				"them: pause deletes file_scope only and the orphan sweep skips a paused attempt, "+
				"so each one blocks that repo or environment for as long as the attempt is not "+
				"terminal — the reported symptom aihub#416 exists to remove")

		// Anti-vacuity. "zero rows" is also satisfied by a claim that did not
		// happen, by a work item that declared nothing, and by a heldLockKeys
		// that cannot see this attempt — so a file_scope declaration on the SAME
		// work item shape has to produce a row through the same helpers.
		wi2 := seedClaimableWI(t, pool, project, u, "the same shape plus a path, which must still lock",
			`[{"type":"repo","uri":"repo:aihub","intent":"write"},`+
				`{"type":"service","uri":"service:aihub","intent":"write"},`+
				`{"type":"path","uri":"file:internal/domain/ac2.go","intent":"write"}]`)
		claim2 := claimFresh(t, pool, wi2.ID, u, "aihub416-ac2-control")
		require.Equal(t, []string{project + ":aihub:internal/domain/ac2.go"},
			heldLockKeys(t, pool, claim2.AttemptID),
			"control: the path entry must still take exactly one file_scope lock, or the empty result "+
				"above is evidence about the query rather than about the derivation")
	})

	// AC-3 — the reason this work item exists. On the pre-change tree the second
	// claim answers 409 CONFLICT_LOCK_TAKEN on git_branch:aihub/main, which is
	// what serialised parallel batches that shared a repo and no file.
	t.Run("two work items declaring the same repo both claim", func(t *testing.T) {
		const declared = `[{"type":"repo","uri":"repo:shared-repo-416","intent":"write"}]`
		first := seedClaimableWI(t, pool, project, u, "first wi on the shared repo", declared)
		second := seedClaimableWI(t, pool, project, u, "second wi on the same shared repo", declared)

		c1, aerr := claimWI(t, pool, u, first.ID, "aihub416-ac3-a")
		require.Nil(t, aerr, "the first claim failed: %+v", aerr)
		c2, aerr := claimWI(t, pool, u, second.ID, "aihub416-ac3-b")
		require.Nil(t, aerr,
			"a second work item declaring the SAME repo was refused: %+v. That refusal is the "+
				"serialisation aihub#416 removes — two work items on one repo touching no common "+
				"file have no reason to exclude each other, and git itself detects a real "+
				"write-write collision at push time", aerr)

		assert.Empty(t, heldLockKeys(t, pool, c1.AttemptID), "the first claim took a lock for a repo entry")
		assert.Empty(t, heldLockKeys(t, pool, c2.AttemptID), "the second claim took a lock for a repo entry")
	})

	// AC-4, the same shape on the other retired type. It is a separate subtest
	// rather than a loop because the two derivations are separate `case` arms and
	// a mutant restoring one must not be masked by the other still being retired.
	t.Run("two work items declaring the same service both claim", func(t *testing.T) {
		const declared = `[{"type":"service","uri":"service:shared-svc-416","intent":"write"}]`
		first := seedClaimableWI(t, pool, project, u, "first wi observing the shared service", declared)
		second := seedClaimableWI(t, pool, project, u, "second wi observing the same service", declared)

		c1, aerr := claimWI(t, pool, u, first.ID, "aihub416-ac4-a")
		require.Nil(t, aerr, "the first claim failed: %+v", aerr)
		c2, aerr := claimWI(t, pool, u, second.ID, "aihub416-ac4-b")
		require.Nil(t, aerr,
			"a second work item declaring the SAME service was refused: %+v. deploy_env had no "+
				"project segment and was held until the attempt ENDED, and pause does not release "+
				"it — so one paused observer blocked every deploy to that environment with no "+
				"automatic exit", aerr)

		assert.Empty(t, heldLockKeys(t, pool, c1.AttemptID), "the first claim took a lock for a service entry")
		assert.Empty(t, heldLockKeys(t, pool, c2.AttemptID), "the second claim took a lock for a service entry")
	})
}

// predictFor runs PredictConflicts as an owner of `project` over one payload,
// WITHOUT naming a caller — which is what a create-preview does.
func predictFor(t *testing.T, pool *pgxpool.Pool, project, declared string, dryRun bool) *PredictConflictsResponse {
	t.Helper()
	return predictAs(t, pool, project, "", declared, dryRun)
}

// predictAs is predictFor with the caller identifying itself (aihub#510).
//
// wiRef is an id OR a slug, because work_item_id accepts both and pf-work's own
// Mode B spells it as a slug. The empty string means "no work_item_id at all",
// and it is passed as an ABSENT field rather than as a pointer to "": the
// request field is a *string, and the difference between nil and a pointer to
// the empty string is the difference between a filter that does nothing and one
// that could compare against NULL.
func predictAs(t *testing.T, pool *pgxpool.Pool, project, wiRef, declared string, dryRun bool) *PredictConflictsResponse {
	t.Helper()
	req := &PredictConflictsRequest{
		Project:           project,
		DeclaredResources: json.RawMessage(declared),
		DryRun:            dryRun,
	}
	if wiRef != "" {
		req.WorkItemID = &wiRef
	}
	resp, aerr := PredictConflicts(context.Background(), pool, req, map[string]string{project: "owner"})
	require.Nil(t, aerr, "PredictConflicts failed: %+v", aerr)
	return resp
}

// slugsOfRule lists the work items one rule named, so an assertion can say WHO
// was reported rather than only how many were.
func slugsOfRule(resp *PredictConflictsResponse, rule int) []string {
	out := []string{}
	for _, p := range predictionsOfRule(resp, rule) {
		out = append(out, p.WISlug)
	}
	return out
}

// predictionsOfRule filters a response to one rule.
func predictionsOfRule(resp *PredictConflictsResponse, rule int) []ConflictPrediction {
	out := []ConflictPrediction{}
	for _, p := range resp.Predictions {
		if p.Rule == rule {
			out = append(out, p)
		}
	}
	return out
}

// TestDeLockingPredictReportsAdvisoryEntries is AC-14 through AC-18.
func TestDeLockingPredictReportsAdvisoryEntries(t *testing.T) {
	pool := setupLatestTestDB(t)

	u := testUser(t, pool)
	project := testProject(t, pool, u)

	const repoName = "predict-repo-416"
	const svcName = "predict-svc-416"
	const repoDeclared = `[{"type":"repo","uri":"repo:` + repoName + `","intent":"write"}]`
	const svcDeclared = `[{"type":"service","uri":"service:` + svcName + `","intent":"write"}]`

	// One RUNNING work item declaring both, which is what every rule below joins
	// against. It is claimed, so wi.status='running' and wi.current_attempt_id
	// point at a live attempt — both halves of rules 2 and 6's join.
	holder := seedClaimableWI(t, pool, project, u, "the running holder the predictions report",
		`[{"type":"repo","uri":"repo:`+repoName+`","intent":"write"},`+
			`{"type":"service","uri":"service:`+svcName+`","intent":"write"}]`)
	holderClaim := claimFresh(t, pool, holder.ID, u, "aihub416-predict-holder")
	require.Empty(t, heldLockKeys(t, pool, holderClaim.AttemptID),
		"fixture check: the holder must hold NO lock, or a prediction could be coming from the "+
			"lock table and this whole file would be measuring the wrong thing")

	// AC-14. The zero-predictions wording is deliberate: the mutant that reverts
	// this rule to the lock table produces zero rows, and zero rows read exactly
	// like "no conflict" (aihub#238). The message says so, so a future reader of
	// a failure knows which of the two they are looking at.
	t.Run("rule 2 reports soft block for a declared repo without reading the lock table", func(t *testing.T) {
		resp := predictFor(t, pool, project, repoDeclared, false)
		rule2 := predictionsOfRule(resp, 2)
		require.Len(t, rule2, 1,
			"rule 2 returned %d predictions for a repo another RUNNING work item declares. Zero here is "+
				"NOT 'no conflict' — it is what the pre-rewrite rule returns for every input once the "+
				"git_branch derivation is retired, because its SQL hardcoded resource_type='git_branch' "+
				"and bypassed the mapper. predictions=%+v", len(rule2), resp.Predictions)

		p := rule2[0]
		assert.Equal(t, SeveritySoftBlock, p.Severity, "rule 2 must stay soft_block")
		assert.Equal(t, holder.Slug, p.WISlug, "the prediction must name the work item that declares the repo")
		assert.NotContains(t, p.Description, "branch",
			"the description still says 'branch' (%q), and no branch name participates in the judgement "+
				"any more — two attempts on two different branches of one repo match this rule", p.Description)
		assert.Equal(t, "repo", p.ResourceType,
			"resource_type must name what was declared, not the lock type that is no longer derived")
	})

	// AC-15. Two assertions, and the second is the one a mutant would slip past:
	// an `info` that raises the top-level severity is a soft_block under another
	// name, and pf-work's pre-claim gate reads that field, not the array.
	t.Run("rule 6 reports exactly one info per service and does not raise severity", func(t *testing.T) {
		resp := predictFor(t, pool, project, svcDeclared, false)
		rule6 := predictionsOfRule(resp, 6)
		require.Len(t, rule6, 1,
			"rule 6 returned %d predictions for a service another RUNNING work item declares. Before "+
				"aihub#416 added it, a service declaration had NO rule but rule 1 — so retiring the "+
				"deploy_env derivation without this rule leaves service declarations with no signal at "+
				"all. predictions=%+v", len(rule6), resp.Predictions)

		p := rule6[0]
		assert.Equal(t, SeverityInfo, p.Severity, "rule 6 must be info")
		assert.Equal(t, holder.Slug, p.WISlug)
		assert.Equal(t, "service", p.ResourceType)
		assert.Equal(t, SeverityInfo, resp.Severity,
			"the TOP-LEVEL severity was raised to %q by an info prediction. pf-work's pre-claim gate "+
				"branches on this field, so an info that raises it is a soft_block wearing another name",
			resp.Severity)
	})

	// AC-16. Only rule 1 is dry_run-gated, because only rule 1 answers "would an
	// insert collide". A rule that reads declarations has nothing to be advisory
	// about — it already is.
	t.Run("rule 6 still fires under dry run", func(t *testing.T) {
		resp := predictFor(t, pool, project, svcDeclared, true)
		require.Len(t, predictionsOfRule(resp, 6), 1,
			"rule 6 disappeared under dry_run=true. dry_run means 'do not consult the lock table', and "+
				"this rule does not consult it — predictions=%+v", resp.Predictions)
	})

	// AC-17. Asserted as a CHANGE, not as a value: a hardcoded 0, or a field
	// computed from the wrong column, would satisfy "it is present and small".
	t.Run("last active age seconds tracks the attempts last active at", func(t *testing.T) {
		fresh := predictionsOfRule(predictFor(t, pool, project, repoDeclared, false), 2)
		require.Len(t, fresh, 1)
		require.NotNil(t, fresh[0].LastActiveAgeSeconds,
			"no last_active_age_seconds on a repo prediction; deploy preflight is meant to be this "+
				"call, and without the age a human cannot judge wait-versus-takeover")
		assert.LessOrEqual(t, *fresh[0].LastActiveAgeSeconds, int64(120),
			"a just-claimed attempt reported an age of %ds", *fresh[0].LastActiveAgeSeconds)

		// Move the column back an hour and re-read. Nothing else changes, so the
		// only thing that can move the number is the column it is computed from.
		mustExec(t, pool,
			`UPDATE run_attempts SET last_active_at = clock_timestamp() - interval '1 hour' WHERE id = '`+
				holderClaim.AttemptID+`'`)
		aged := predictionsOfRule(predictFor(t, pool, project, repoDeclared, false), 2)
		require.Len(t, aged, 1)
		require.NotNil(t, aged[0].LastActiveAgeSeconds)
		assert.Greater(t, *aged[0].LastActiveAgeSeconds, int64(3000),
			"last_active_at moved back an hour and the reported age is %ds — the field is not computed "+
				"from that column", *aged[0].LastActiveAgeSeconds)

		// ⚠️ NOT a lease. The age is published for a human to read; nothing in
		// this system expires on it. This line is the negative control for that
		// sentence: an hour-old attempt is still reported, still soft_block, and
		// still holds its work item.
		assert.Equal(t, SeveritySoftBlock, aged[0].Severity,
			"an hour-old attempt changed severity — something is treating the age as an expiry, which "+
				"design v1.21 removed from this system outright")
	})

	// AC-18, the §5.3 public claim, checked on the value pf-work branches on.
	t.Run("a repo or service only payload never returns hard block", func(t *testing.T) {
		for name, declared := range map[string]string{
			"repo only":    repoDeclared,
			"service only": svcDeclared,
			"both":         `[{"type":"repo","uri":"repo:` + repoName + `","intent":"write"},{"type":"service","uri":"service:` + svcName + `","intent":"write"}]`,
		} {
			for _, dry := range []bool{false, true} {
				resp := predictFor(t, pool, project, declared, dry)
				assert.NotEqual(t, SeverityHardBlock, resp.Severity,
					"%s (dry_run=%v) returned hard_block. Neither type derives a lock, so rule 1 "+
						"cannot fire for them and there is nothing left that could justify the "+
						"ceiling this value promises", name, dry)
			}
		}
	})

	// ── aihub#511: the containment operand must cross as a PARAMETER ─────────
	//
	// Rules 2, 4, 5 and 6 all ask the same question — "does another RUNNING work
	// item DECLARE this entry" — and all four used to build the right-hand side
	// of `@>` by pasting the declared name into a JSON literal in Go. A name
	// holding a double quote closed that string early, Postgres refused the
	// operand with 22P02, the error went to stderr, and the caller got
	// {"predictions":[],"severity":"info"} — byte-identical to a genuine
	// all-clear, which is the aihub#238 failure mode the rule 2 header above says
	// the rule exists to avoid. Nothing rejected the name on the way in:
	// ValidateDeclaredResources checks the uri SCHEME and says nothing about the
	// characters after it.
	//
	// Every arm is a PAIR, and the plain half is not decoration: "zero
	// predictions for the hostile name" says nothing unless the identical query
	// returns one for a name that differs only in its characters.
	//
	// Two hostile shapes, because they fail differently and only one is audible:
	//
	//	`"`   a syntax error Postgres reports (22P02). Loud in the log, invisible
	//	      in the response.
	//	`\b`  NOT a syntax error: "repo:qc\backslash" is well-formed JSON for
	//	      `repo:qc<backspace>ackslash`, so the operand parses and simply means
	//	      something else. Nothing is logged anywhere, the query returns zero
	//	      rows, and no caller can tell that from "nobody else declared it".
	//
	// Rules 4 and 5 are exercised here rather than beside AC-14/AC-15 because
	// they PRE-DATE aihub#416 and had no arm at all — the concatenation rule 2
	// picked up in its rewrite was copied from them.
	t.Run("declared names that look like json still match every containment rule", func(t *testing.T) {
		for _, arm := range []struct{ label, name string }{
			{"plain", "qc-plain-511"},
			{"doublequote", `qc"quote-511`},
			{"backslash", `qc\backslash-511`},
		} {
			t.Run(arm.label, func(t *testing.T) {
				// One holder per name shape, declaring all four entry types the four
				// containment rules read. intent=refactor on the repo entry is what
				// lets ONE declaration answer rule 2 (any repo) and rule 4 (refactor
				// only) both. json.Marshal, not concatenation — building this fixture
				// the way the bug built its operand would corrupt the fixture in
				// exactly the way that hides the bug.
				declared, err := json.Marshal([]DeclaredResourceItem{
					{Type: "repo", URI: "repo:" + arm.name, Intent: "refactor"},
					{Type: "service", URI: "service:" + arm.name, Intent: "write"},
					{Type: "external_ref", URI: "https://ex.test/" + arm.name},
				})
				require.NoError(t, err)

				h := seedClaimableWI(t, pool, project, u, "aihub#511 holder: "+arm.label, string(declared))
				claimFresh(t, pool, h.ID, u, "aihub511-"+arm.label)

				// The key each rule echoes back for this fixture. Rules 2, 4 and 6
				// report the name with its scheme stripped; rule 5's key IS the
				// external_ref uri, whole.
				wantKey := map[int]string{
					2: arm.name,
					4: arm.name,
					5: "https://ex.test/" + arm.name,
					6: arm.name,
				}

				resp := predictFor(t, pool, project, string(declared), false)
				for _, rule := range []int{2, 4, 5, 6} {
					got := predictionsOfRule(resp, rule)
					// assert-and-continue, not require: a hostile name breaks all four
					// rules at once, and a failure report that names only the first
					// would understate the blast radius.
					if !assert.Len(t, got, 1,
						"rule %d returned %d predictions for the name %q, which another RUNNING work "+
							"item declares VERBATIM. Zero is not 'no conflict' here — the plain arm of "+
							"this same table proves the query matches, so zero means the operand "+
							"Postgres received was not the name that was declared. In the response that "+
							"is indistinguishable from a real all-clear, which is what pf-work's "+
							"pre-claim gate reads. predictions=%+v",
						rule, len(got), arm.name, resp.Predictions) {
						continue
					}
					assert.Equal(t, h.Slug, got[0].WISlug,
						"rule %d named %q, but the work item declaring %q is %q",
						rule, got[0].WISlug, arm.name, h.Slug)
					assert.Equal(t, wantKey[rule], got[0].ResourceKey,
						"rule %d reported resource_key %q for the declared name %q — the key is echoed "+
							"back to the caller, so a mangled one names a resource nobody declared",
						rule, got[0].ResourceKey, arm.name)
				}
			})
		}
	})

	// ── aihub#510: a work item is not its own conflict ───────────────────────
	//
	// All four containment rules join work_items on status='running' AND a
	// declaration overlap, and a CLAIMED work item asking about its own
	// declarations satisfies both halves — so it was reported back to itself as a
	// soft_block (rules 2, 4) or an info (rules 5, 6). aihub#416's rewrite did not
	// introduce this; rule 2 read the caller's own LOCK row before it and joins
	// the caller's own DECLARATION after it, so the defect survived the rewrite
	// unchanged in kind. The contract card recorded it as a known untrustworthy
	// direction rather than fixing it. This is the fix for the declaration half.
	//
	// It is opt-in by construction, not by preference: a predict that names no
	// work item has no self to exclude, and the create-preview path deliberately
	// names none because the work item does not exist yet. So `work_item_id`
	// stops being "optional, for context" and becomes the caller's identity,
	// which is why its schema description says so now.
	//
	// 🔴 THE TWO NAMES ARE THE WHOLE DESIGN OF THIS TABLE. `sharedName` is
	// declared by the caller AND by a second running work item; `soleName` by the
	// caller only. Without the second name, "zero predictions" would be satisfied
	// by a filter that excluded everything, and the fake all-clear this file keeps
	// circling back to would have been reintroduced by the test meant to prevent
	// it. Every arm below therefore checks WHO was reported, not just how many.
	//
	// Mutants, measured 2026-09-09 against a local Postgres 16 (4/4 caught; the
	// aihub#511 source guard stays green under all four, which is correct — it
	// pins how the operand is built, not who is excluded from it):
	//
	//	M11  replace notCallersOwnWISQL in declaresContainmentSQL with a
	//	     tautology that still consumes $1
	//	     -> 4 red: "the caller alone declares it" (rules 2/5/6 report the
	//	        caller, and severity comes back soft_block instead of info),
	//	        "another work item ... still reported" (2 slugs instead of 1),
	//	        "by slug", "under dry run"
	//	M12  the same in declaresIntentContainmentSQL
	//	     -> THE SAME 4 red, via rule 4 alone. The two constants are not
	//	        separately observable by arm, because every arm checks all four
	//	        rules; what separates them is WHICH rule appears in the failure.
	//	M13  bind req.WorkItemID (the *string) instead of canonicalWIID
	//	     -> the anonymous arm red at 0 predictions instead of 2 (nil pointer
	//	        -> NULL -> `wi.id <> NULL` is NULL -> every rule silent), and
	//	        "by slug" red as well. It also reds the five aihub#416/#511 arms
	//	        that expect a prediction at all, which is the blast radius of the
	//	        NULL trap. ⚠️ "the caller alone declares it" and "under dry run"
	//	        stay GREEN under M13 — they assert EMPTINESS, and silence
	//	        satisfies them. The anonymous arm is the only one that sees this.
	//	M14  drop `canonicalWIID = id` from the aihub#357 lookup, so the raw
	//	     work_item_id is bound
	//	     -> "by slug" red ALONE, every other arm green. That single-arm
	//	        result is what makes the aihub#357 dependency measured rather
	//	        than asserted.
	t.Run("a work item that names itself is not its own conflict", func(t *testing.T) {
		const sharedName = "shared-510"
		const soleName = "sole-510"

		// intent=refactor on the repo entries is what lets one declaration answer
		// rule 2 (any repo) and rule 4 (refactor only) at once.
		entriesFor := func(name string) []DeclaredResourceItem {
			return []DeclaredResourceItem{
				{Type: "repo", URI: "repo:" + name, Intent: "refactor"},
				{Type: "service", URI: "service:" + name, Intent: "write"},
				{Type: "external_ref", URI: "https://ex.test/" + name},
			}
		}
		marshal := func(items []DeclaredResourceItem) string {
			b, err := json.Marshal(items)
			require.NoError(t, err)
			return string(b)
		}

		sharedPayload := marshal(entriesFor(sharedName))
		solePayload := marshal(entriesFor(soleName))

		// The caller: RUNNING, declaring both names.
		caller := seedClaimableWI(t, pool, project, u, "aihub#510 the caller asking about its own declarations",
			marshal(append(entriesFor(sharedName), entriesFor(soleName)...)))
		callerClaim := claimFresh(t, pool, caller.ID, u, "aihub510-caller")
		require.Empty(t, heldLockKeys(t, pool, callerClaim.AttemptID),
			"fixture check: the caller must hold NO lock, or these predictions could be coming from "+
				"the lock table (rules 1 and 3), which this work item does NOT change")

		// The other work item: RUNNING, declaring the shared name only. It is the
		// negative control for every arm — a filter written too wide hides it too.
		other := seedClaimableWI(t, pool, project, u, "aihub#510 somebody else declaring the shared name", sharedPayload)
		claimFresh(t, pool, other.ID, u, "aihub510-other")

		containmentRules := []int{2, 4, 5, 6}

		// The payoff arm, and it is an A/B on ONE payload: the only thing that
		// differs between this and the anonymous arm below is work_item_id.
		t.Run("the caller alone declares it, so there is nothing to report", func(t *testing.T) {
			resp := predictAs(t, pool, project, caller.ID, solePayload, false)
			for _, rule := range containmentRules {
				assert.Empty(t, slugsOfRule(resp, rule),
					"rule %d reported %v for a name only the CALLER (%s) declares. A work item is not "+
						"its own conflict, and the anonymous arm of this same table proves the query "+
						"still matches — so this is the caller being handed itself back. predictions=%+v",
					rule, slugsOfRule(resp, rule), caller.Slug, resp.Predictions)
			}
			assert.Equal(t, SeverityInfo, resp.Severity,
				"the top-level severity is %q for a payload nobody but the caller declares. This is the "+
					"field pf-work's pre-claim gate branches on, so a self-report here reads as "+
					"'somebody else is on this' to the one agent that already knows it is not",
				resp.Severity)
		})

		// Same payload, no identity: the pre-aihub#510 answer, unchanged. This arm
		// is also the positive control for the one above — it proves the two
		// declarations really are there to be found.
		t.Run("an anonymous create preview still reports both", func(t *testing.T) {
			resp := predictAs(t, pool, project, "", sharedPayload, false)
			for _, rule := range containmentRules {
				assert.ElementsMatch(t, []string{caller.Slug, other.Slug}, slugsOfRule(resp, rule),
					"rule %d named %v with no work_item_id supplied, want both running declarers. A "+
						"predict that identifies nobody has no self to exclude, and the create-preview "+
						"path names nobody because the work item does not exist yet — so this answer "+
						"must not have moved. Zero here means the filter compared against NULL and "+
						"silenced every rule. predictions=%+v",
					rule, slugsOfRule(resp, rule), resp.Predictions)
			}
			assert.Equal(t, SeveritySoftBlock, resp.Severity,
				"severity = %q; two running work items declare this repo and rule 2 is soft_block", resp.Severity)
		})

		// The negative control as its own arm: excluding yourself must not exclude
		// anybody else.
		t.Run("another work item declaring the same name is still reported", func(t *testing.T) {
			resp := predictAs(t, pool, project, caller.ID, sharedPayload, false)
			for _, rule := range containmentRules {
				assert.Equal(t, []string{other.Slug}, slugsOfRule(resp, rule),
					"rule %d named %v; want exactly [%s] — the caller (%s) excluded and the other "+
						"declarer kept. Both failure directions land here: %s still present is the bug "+
						"unfixed, %s missing is a filter written wide enough to hide real conflicts. "+
						"predictions=%+v",
					rule, slugsOfRule(resp, rule), other.Slug, caller.Slug, caller.Slug, other.Slug,
					resp.Predictions)
			}
			assert.Equal(t, SeveritySoftBlock, resp.Severity,
				"severity = %q; somebody else really does declare this repo", resp.Severity)
		})

		// work_item_id accepts an id OR a slug and pf-work's Mode B sends the
		// slug. A slug matches no work_items.id, so a filter bound to the RAW
		// parameter rather than to the resolved id would do nothing for the caller
		// that spells itself the commonest way.
		t.Run("naming itself by slug works too", func(t *testing.T) {
			resp := predictAs(t, pool, project, caller.Slug, solePayload, false)
			for _, rule := range containmentRules {
				assert.Empty(t, slugsOfRule(resp, rule),
					"rule %d reported %v when the caller named itself by SLUG (%s) rather than by id "+
						"(%s). work_item_id takes both, aihub#357 resolves it to the canonical id "+
						"before the rules run, and the filter has to use that resolution — bound to "+
						"the raw parameter it compares a slug against work_items.id and never matches. "+
						"predictions=%+v",
					rule, slugsOfRule(resp, rule), caller.Slug, caller.ID, resp.Predictions)
			}
		})

		// dry_run changes nothing here: only rule 1 is gated on it, and the four
		// rules under test read declarations rather than the lock table.
		t.Run("the exclusion holds under dry run", func(t *testing.T) {
			resp := predictAs(t, pool, project, caller.ID, solePayload, true)
			for _, rule := range containmentRules {
				assert.Empty(t, slugsOfRule(resp, rule),
					"rule %d reported %v under dry_run=true. dry_run means 'do not consult the lock "+
						"table' and these rules do not consult it, so the caller's identity cannot "+
						"depend on it either. predictions=%+v",
					rule, slugsOfRule(resp, rule), resp.Predictions)
			}
		})
	})
}

// TestDeLockingMigration0038AndRepoPins is AC-19 plus the repo-pin round trip.
//
// 🔴 AC-19 is asserted as a POST-CONDITION OF THE SCHEMA, not by running the
// migration inside the test. setupLatestTestDB hands back a database already
// migrated to head, so "zero rows of the two retired types" is a statement about
// what migration 0038 left behind — which is the actual acceptance criterion. A
// test that re-ran the DO block would be testing a copy of it.
//
// ⚠️ WHAT THAT CANNOT SEE, said rather than implied: on a database that never
// held a git_branch or deploy_env row, "zero rows" is true for free and the
// event assertion below has nothing to read. That is why the event arm is
// CONDITIONAL and the row-count arm is not — the row count is the criterion, the
// events are the audit trail, and asserting the audit of a deletion that never
// happened would fail on a clean database while proving nothing on a dirty one.
// The real evidence for the event half is the deployment run against production,
// recorded in aihub#416's attrs.
func TestDeLockingMigration0038AndRepoPins(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	t.Run("running the migration deletes both retired types and leaves file scope", func(t *testing.T) {
		u := testUser(t, pool)
		project := testProject(t, pool, u)
		wi := seedClaimableWI(t, pool, project, u, "hold one lock of each retired type", advisoryOnly)

		// Seeded through the CLAIM path with explicit requested_locks, because
		// that is how such a row is created now, and a raw INSERT would not
		// exercise the FK and epoch columns the migration's join reads back.
		branchKey := "repo416/" + project
		envKey := "env416-" + project
		pathKey := project + ":internal/domain/mig38.go"
		claim, aerr := claimFreshWithLocksErr(t, pool, u, wi.ID, "aihub416-mig38", []ResourceLockReq{
			{ResourceType: "git_branch", ResourceKey: branchKey},
			{ResourceType: "deploy_env", ResourceKey: envKey},
			{ResourceType: "file_scope", ResourceKey: pathKey},
		})
		require.Nil(t, aerr, "seeding claim failed: %+v", aerr)
		require.ElementsMatch(t, []string{branchKey, envKey, pathKey}, heldLockKeys(t, pool, claim.AttemptID),
			"fixture check: all three rows must exist BEFORE the migration runs, or a post-run absence "+
				"proves nothing")

		before := lockReleasedDerivationRetiredCount(t, pool)

		// 🔴 THE MIGRATION FILE ITSELF is executed, not a copy of it pasted here.
		// A copy would keep passing after somebody edited the real one, which is
		// the failure mode this whole arm exists to prevent — and the migration's
		// content IS the acceptance criterion for AC-19.
		mustExec(t, pool, goosUpSection(t, "0038_retire_lock_derivation_rows.sql"))

		remaining := heldLockKeys(t, pool, claim.AttemptID)
		assert.Equal(t, []string{pathKey}, remaining,
			"after 0038 the attempt holds %v. Both retired types must be gone and file_scope must "+
				"survive: the migration removes rows that nothing derives, releases or probes any "+
				"more, and touching file_scope would take a lock somebody is relying on", remaining)

		// The audit half. Two events, one per deleted row, both with the new
		// cause — a silent bulk DELETE would recreate at scale exactly the
		// condition aihub#343 exists to remove, where a lock is acquired on a
		// timeline and never released on it.
		got := derivationRetiredReleases(t, pool, claim.AttemptID)
		require.Len(t, got, 2,
			"0038 deleted 2 rows for this attempt and wrote %d lock_released events. Without one per "+
				"row, every future reader of this attempt's timeline sees locks taken and never "+
				"given back", len(got))
		assert.ElementsMatch(t, []string{branchKey, envKey}, []string{got[0].key, got[1].key})
		for _, e := range got {
			assert.Contains(t, []string{"git_branch", "deploy_env"}, e.typ)
			assert.NotEmpty(t, e.opID, "no op_id, so the events of this one operation cannot be regrouped")
			assert.NotEmpty(t, e.attemptID, "no attempt_id, so a reviewer cannot tell WHOSE lock this was")
		}
		assert.Equal(t, got[0].opID, got[1].opID,
			"the two releases carry different op_ids; one migration is one operation")

		assert.Greater(t, lockReleasedDerivationRetiredCount(t, pool), before,
			"the derivation_retired event count did not move — the events above may be left over from "+
				"an earlier run rather than written by this one")

		// Idempotence, because a migration can be re-run against a database that
		// already has it (a rebuilt test DB, a re-applied goose state). The
		// second run must find nothing and write nothing rather than fail.
		countAfterFirst := lockReleasedDerivationRetiredCount(t, pool)
		mustExec(t, pool, goosUpSection(t, "0038_retire_lock_derivation_rows.sql"))
		assert.Equal(t, countAfterFirst, lockReleasedDerivationRetiredCount(t, pool),
			"re-running 0038 wrote more events with nothing left to delete")
	})

	t.Run("the retired types stay in the check vocabulary", func(t *testing.T) {
		// AC-6, and the anti-vacuity control for the arm above: a schema that had
		// DROPPED these types from the constraint would make "no such rows" true
		// for entirely the wrong reason, and would break the requested_locks
		// escape hatch the owner ruling deliberately kept open (Q-3).
		var allowed bool
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT pg_get_constraintdef(oid) LIKE '%git_branch%'
			   AND pg_get_constraintdef(oid) LIKE '%deploy_env%'
			FROM pg_constraint WHERE conname = 'resource_locks_resource_type_check'`).Scan(&allowed))
		assert.True(t, allowed,
			"the resource_locks CHECK no longer admits git_branch/deploy_env. aihub#416 retired the "+
				"DERIVATION, not the vocabulary: requested_locks may still ask for them, and "+
				"worktree/tcp_port are the standing precedent for a type that is legal and underived")
	})

	// The repo-pin round trip: written by FnRecordRepoPins, read back off the
	// column. Asserted through the real function rather than a raw UPDATE, so the
	// credential check and the JSONB encoding are both exercised.
	t.Run("repo pins round trip through the recording call", func(t *testing.T) {
		u := testUser(t, pool)
		project := testProject(t, pool, u)
		wi := seedClaimableWI(t, pool, project, u, "record where this attempt started from", advisoryOnly)
		claim := claimFresh(t, pool, wi.ID, u, "aihub416-pins")

		var before *string
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT repo_pins::text FROM run_attempts WHERE id=$1`, claim.AttemptID).Scan(&before))
		require.Nil(t, before,
			"a fresh claim already carries repo_pins. It must be NULL until the pins are recorded — "+
				"the worktrees do not exist yet at claim time, so any value here is invented")

		pins := map[string]string{
			"aihub":      "0123456789abcdef0123456789abcdef01234567",
			"ieops-core": "89abcdef0123456789abcdef0123456789abcdef",
		}
		require.Nil(t, FnRecordRepoPins(ctx, pool, wi.ID, &RecordRepoPinsRequest{
			AttemptID:     claim.AttemptID,
			ClaimEpoch:    claim.ClaimEpoch,
			SessionSecret: "locktest-secret-0123456789abcdef0123456789abcdef0123456789ab",
			RepoPins:      pins,
		}))

		var stored []byte
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT repo_pins FROM run_attempts WHERE id=$1`, claim.AttemptID).Scan(&stored))
		var got map[string]string
		require.NoError(t, json.Unmarshal(stored, &got))
		assert.Equal(t, pins, got, "the pins did not round-trip through the column")

		// The credential is the gate, not project access. Without this, any
		// writer on the project could assert where somebody else's conclusions
		// came from.
		aerr := FnRecordRepoPins(ctx, pool, wi.ID, &RecordRepoPinsRequest{
			AttemptID:     claim.AttemptID,
			ClaimEpoch:    claim.ClaimEpoch,
			SessionSecret: "not-the-secret-this-attempt-was-bound-to",
			RepoPins:      map[string]string{"aihub": "ffffffffffffffffffffffffffffffffffffffff"},
		})
		require.NotNil(t, aerr, "a wrong session_secret recorded repo pins for somebody else's attempt")

		// And the refusal must have changed nothing.
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT repo_pins FROM run_attempts WHERE id=$1`, claim.AttemptID).Scan(&stored))
		require.NoError(t, json.Unmarshal(stored, &got))
		assert.Equal(t, pins, got, "the refused call still wrote; the transaction is not rolling back whole")

		// An empty map is a no-op, not a wipe: "I built no worktrees" and "forget
		// what I told you" are different statements, and only the first is
		// reachable from the client.
		require.Nil(t, FnRecordRepoPins(ctx, pool, wi.ID, &RecordRepoPinsRequest{
			AttemptID:     claim.AttemptID,
			ClaimEpoch:    claim.ClaimEpoch,
			SessionSecret: "locktest-secret-0123456789abcdef0123456789abcdef0123456789ab",
			RepoPins:      nil,
		}))
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT repo_pins FROM run_attempts WHERE id=$1`, claim.AttemptID).Scan(&stored))
		require.NoError(t, json.Unmarshal(stored, &got))
		assert.Equal(t, pins, got,
			"an empty repo_pins wiped the column. A later failed claim would then erase the provenance "+
				"of work already done under this attempt")
	})

	// The read-back surface a resuming agent uses. It is asserted here rather
	// than in internal/server because what it has to prove is that the value the
	// WRITER stored is the value the READER returns, and both halves are cheapest
	// to drive from one place.
	t.Run("recorded pins are readable from the attempt row a step read joins", func(t *testing.T) {
		u := testUser(t, pool)
		project := testProject(t, pool, u)
		wi := seedClaimableWI(t, pool, project, u, "pins visible through the current attempt", advisoryOnly)
		claim := claimFresh(t, pool, wi.ID, u, "aihub416-pins-read")
		pins := map[string]string{"aihub": "1111111111111111111111111111111111111111"}
		require.Nil(t, FnRecordRepoPins(ctx, pool, wi.ID, &RecordRepoPinsRequest{
			AttemptID:     claim.AttemptID,
			ClaimEpoch:    claim.ClaimEpoch,
			SessionSecret: "locktest-secret-0123456789abcdef0123456789abcdef0123456789ab",
			RepoPins:      pins,
		}))

		// The exact join handleGetStep performs: work_items.current_attempt_id ->
		// run_attempts.repo_pins. Written out rather than calling the handler so
		// this stays a domain test, and kept identical to it on purpose — a
		// divergence here would be a reader that cannot see what the writer wrote.
		var raw []byte
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT ra.repo_pins
			FROM work_items wi
			JOIN run_attempts ra ON ra.id = wi.current_attempt_id
			WHERE wi.id = $1`, wi.ID).Scan(&raw))
		var got map[string]string
		require.NoError(t, json.Unmarshal(raw, &got))
		assert.Equal(t, pins, got)
	})

	// Guard against the pin quietly becoming a clock. There is no expiry
	// anywhere; this asserts the column holds what was put in it however old the
	// attempt looks.
	t.Run("a pin does not expire", func(t *testing.T) {
		u := testUser(t, pool)
		project := testProject(t, pool, u)
		wi := seedClaimableWI(t, pool, project, u, "an old attempt keeps its pin", advisoryOnly)
		claim := claimFresh(t, pool, wi.ID, u, "aihub416-pins-age")
		pins := map[string]string{"aihub": "2222222222222222222222222222222222222222"}
		require.Nil(t, FnRecordRepoPins(ctx, pool, wi.ID, &RecordRepoPinsRequest{
			AttemptID:     claim.AttemptID,
			ClaimEpoch:    claim.ClaimEpoch,
			SessionSecret: "locktest-secret-0123456789abcdef0123456789abcdef0123456789ab",
			RepoPins:      pins,
		}))
		mustExec(t, pool, `UPDATE run_attempts SET started_at = clock_timestamp() - interval '30 days',
			last_active_at = clock_timestamp() - interval '30 days' WHERE id = '`+claim.AttemptID+`'`)

		var raw []byte
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT repo_pins FROM run_attempts WHERE id=$1`, claim.AttemptID).Scan(&raw))
		var got map[string]string
		require.NoError(t, json.Unmarshal(raw, &got))
		assert.Equal(t, pins, got,
			"a 30-day-old attempt lost its pin. There is no such thing as a stale pin, only an old one: "+
				"design v1.21 removed expires_at from this schema and handleRenewLease answers 410 Gone")
	})
}

// goosUpSection returns the executable body of a migration's `-- +goose Up`
// section, read from the real file in internal/db/migrations.
//
// It exists so a migration test runs the ARTIFACT rather than a transcription of
// it. The goose directives are stripped because they are instructions to goose,
// not SQL; everything between them is executed verbatim.
func goosUpSection(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "db", "migrations", name))
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	src := string(body)
	up := strings.Index(src, "-- +goose Up")
	down := strings.Index(src, "-- +goose Down")
	if up < 0 || down < 0 || down < up {
		t.Fatalf("%s does not have an Up section followed by a Down section", name)
	}
	section := src[up+len("-- +goose Up") : down]
	section = strings.ReplaceAll(section, "-- +goose StatementBegin", "")
	section = strings.ReplaceAll(section, "-- +goose StatementEnd", "")
	if !strings.Contains(section, "resource_locks") {
		t.Fatalf("%s's Up section does not mention resource_locks — the extraction is broken, not the migration", name)
	}
	return section
}

// retiredRelease is one lock_released event written by migration 0038.
type retiredRelease struct{ typ, key, attemptID, opID string }

// derivationRetiredReleases reads the migration's audit events for one attempt.
func derivationRetiredReleases(t *testing.T, pool *pgxpool.Pool, attemptID string) []retiredRelease {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT payload->>'resource_type', payload->>'resource_key',
		       payload->>'attempt_id', payload->>'op_id'
		FROM agent_events
		WHERE event_type = 'lock_released'
		  AND payload->>'cause' = $1
		  AND payload->>'attempt_id' = $2`,
		LockCauseDerivationRetired, attemptID)
	if err != nil {
		t.Fatalf("read derivation_retired events: %v", err)
	}
	defer rows.Close()
	out := []retiredRelease{}
	for rows.Next() {
		var e retiredRelease
		if err := rows.Scan(&e.typ, &e.key, &e.attemptID, &e.opID); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// lockReleasedDerivationRetiredCount is the whole-table count, used only to
// prove the arm above wrote something rather than reading an earlier run's rows.
func lockReleasedDerivationRetiredCount(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM agent_events
		WHERE event_type = 'lock_released' AND payload->>'cause' = $1`,
		LockCauseDerivationRetired).Scan(&n); err != nil {
		t.Fatalf("count derivation_retired events: %v", err)
	}
	return n
}
