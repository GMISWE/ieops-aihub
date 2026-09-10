package domain

// DB-gated integration tests for aihub#564: the two lock-table rules of
// PredictConflicts — 1 (hard_block) and 3 (file_scope overlap) — must leave the
// caller's own attempt out, the way aihub#510 taught the four declaration rules
// (2, 4, 5, 6) to.
//
// Measured on the pre-fix build, this file's own arms (RED evidence in the
// aihub#564 PR, captured 2026-09-10 against a scratch pg18 at migration 0038):
// one claimed work item re-predicting its own `path` declaration got, with
// dry_run=false, `severity: "hard_block"` and a single rule-1 prediction
// "Resource lock is already held by another attempt" whose work_item_slug was
// the caller itself; with dry_run=true rule 1 is skipped and rule 3 answered
// `severity: "soft_block"` / "File path overlaps with another running attempt",
// again naming the caller. Those two severities are the value pf-work's
// pre-claim gate branches on (Mode B sends work_item_id=<slug>, dry_run=true),
// so the before/after gate readings are: soft_block naming the caller before,
// info with no predictions after — and, for a dry_run=false re-predict,
// hard_block naming the caller before, info with no predictions after.
//
// The claim path itself never had this defect: probeForeignLockHolders goes
// through foreignLockHolderSQL, which excludes the caller's own work item
// (aihub#207) precisely so a resume can re-take its own locks. Predict exists to
// answer "what will claim do" — rule 1 without the exclusion disagreed with the
// very check it predicts.
//
// Both directions are pinned, deliberately: the self-held lock disappearing is
// the fix, and ANOTHER attempt's lock still hard-blocking is the positive
// control that separates "excluded the caller" from "stopped looking at the
// lock table".
//
// Run:
//
//	AIHUB_TEST_DB=postgres://postgres:test@127.0.0.1:15641/aihub_test?sslmode=disable \
//	  GOWORK=off go test ./internal/domain/ -run TestPredictLockRulesLeaveTheCallerOut -count=1 -v
//
// MUTANTS (run against this tree; the verdict is what happened):
//
//	M1 enforcement: drop notCallersOwnLockHolderSQL+`$4` from rule 1's query (and
//	   canonicalWIID from its args) — re-include self-held locks
//	                                        RED  rule1_self_held_lock_is_excluded,
//	                                             mixed_payload_reports_only_the_foreign_holder
//	                                             and exclusion_holds_when_identified_by_slug
//	M2 enforcement: drop notCallersOwnLockHolderSQL+`$1` from rule 3's query —
//	   re-include self-held locks on the dry_run path
//	                                        RED  rule3_self_held_lock_is_excluded,
//	                                             rule1_dry_run_gate_reading (the gate
//	                                             reading reddens through rule 3, since
//	                                             dry_run skips rule 1) and
//	                                             exclusion_holds_when_identified_by_slug
//	M3 enforcement: bind req.WorkItemID's raw value instead of canonicalWIID in
//	   rule 1                               RED  exclusion_holds_when_identified_by_slug
//	                                             (a slug matches no work_items.id, so
//	                                             the filter silently does nothing for
//	                                             the spelling pf-work's Mode B sends)
//	                                             AND anonymous_predict_keeps_the_self_report
//	                                             — the raw value is a *string, nil
//	                                             crosses as NULL, `<> NULL` is NULL, and
//	                                             rule 1 goes silent for EVERY caller: the
//	                                             aihub#238 fake all-clear on the hard gate
//	M4 publication: reword the card sentence citing this arm to name a Test
//	   symbol the tree does not declare     RED  K12 ARM_CITATION_UNRESOLVED

import (
	"context"
	"encoding/json"
	"testing"
)

// declaredTwoPathsWithRepo builds a two-entry payload the same raw-JSON way
// declaredWithRepo does, for the mixed self+foreign arm.
func declaredTwoPathsWithRepo(repo, pathA, pathB string) json.RawMessage {
	raw, err := json.Marshal([]map[string]any{
		{"type": "path", "uri": "file:" + pathA, "intent": "write", "repo": repo},
		{"type": "path", "uri": "file:" + pathB, "intent": "write", "repo": repo},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func TestPredictLockRulesLeaveTheCallerOut(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)
	roles := map[string]string{proj: "owner"}

	const callerPath = "internal/server/router.go"
	const foreignPath = "pkg/client/client.go"

	// The caller: a claimed work item holding the file_scope lock its own
	// declaration derives.
	caller := seedWIWithResources(t, pool, proj, uid, "caller holds its own path",
		declaredWithRepo("repo-a", callerPath))
	if _, aerr := claimWI(t, pool, uid, caller.ID, "idem-564-caller"); aerr != nil {
		t.Fatalf("claim caller: %v", aerr)
	}
	// The foreign holder: a second running work item holding a different path.
	foreign := seedWIWithResources(t, pool, proj, uid, "foreign attempt holds another path",
		declaredWithRepo("repo-a", foreignPath))
	if _, aerr := claimWI(t, pool, uid, foreign.ID, "idem-564-foreign"); aerr != nil {
		t.Fatalf("claim foreign: %v", aerr)
	}

	predict := func(t *testing.T, wiRef *string, declared json.RawMessage, dryRun bool) *PredictConflictsResponse {
		t.Helper()
		resp, aerr := PredictConflicts(ctx, pool, &PredictConflictsRequest{
			WorkItemID: wiRef, Project: proj, DeclaredResources: declared, DryRun: dryRun,
		}, roles)
		if aerr != nil {
			t.Fatalf("PredictConflicts: %v", aerr)
		}
		return resp
	}
	namedBy := func(resp *PredictConflictsResponse, rule int, slug string) []ConflictPrediction {
		var out []ConflictPrediction
		for _, p := range resp.Predictions {
			if p.Rule == rule && p.WISlug == slug {
				out = append(out, p)
			}
		}
		return out
	}

	t.Run("rule1_self_held_lock_is_excluded", func(t *testing.T) {
		resp := predict(t, &caller.ID, declaredWithRepo("repo-a", callerPath), false)
		if got := namedBy(resp, 1, caller.Slug); len(got) > 0 {
			t.Errorf("rule 1 reported the caller's own lock back to it as %q (severity %s): %+v — "+
				"claim re-takes self-held locks (foreignLockHolderSQL, aihub#207), so this hard_block "+
				"predicts a collision the claim it fronts for would not have", got[0].Description, resp.Severity, got)
		}
		if resp.Severity == SeverityHardBlock {
			t.Errorf("severity is hard_block for a payload whose only matching lock is the caller's own — "+
				"this is the value pf-work's pre-claim gate branches on; predictions=%+v", resp.Predictions)
		}
	})

	t.Run("rule1_foreign_lock_still_hard_blocks", func(t *testing.T) {
		// The positive control: exclusion must not blind the rule to OTHER
		// attempts' locks.
		resp := predict(t, &caller.ID, declaredWithRepo("repo-a", foreignPath), false)
		if got := namedBy(resp, 1, foreign.Slug); len(got) == 0 {
			t.Errorf("rule 1 did not report the foreign holder %s — the exclusion was supposed to remove "+
				"the caller's OWN row, not the question; predictions=%+v", foreign.Slug, resp.Predictions)
		}
		if resp.Severity != SeverityHardBlock {
			t.Errorf("severity=%s for a path another running attempt holds, want hard_block", resp.Severity)
		}
	})

	t.Run("rule3_self_held_lock_is_excluded", func(t *testing.T) {
		// dry_run=true skips rule 1; rule 3 answers the same lock table.
		resp := predict(t, &caller.ID, declaredWithRepo("repo-a", callerPath), true)
		if got := namedBy(resp, 3, caller.Slug); len(got) > 0 {
			t.Errorf("rule 3 (dry_run) reported the caller's own lock back to it as %q (severity %s): %+v",
				got[0].Description, resp.Severity, got)
		}
	})

	t.Run("rule1_dry_run_gate_reading", func(t *testing.T) {
		// pf-work Mode B's exact call shape: work_item_id set, dry_run=true. The
		// gate branches on severity; a re-predicting caller whose only overlap is
		// itself must read all-clear.
		resp := predict(t, &caller.ID, declaredWithRepo("repo-a", callerPath), true)
		if resp.Severity != SeverityInfo {
			t.Errorf("gate reading: severity=%s for pf-work Mode B's own call shape "+
				"(work_item_id=<self>, dry_run=true) with no foreign overlap, want info; predictions=%+v",
				resp.Severity, resp.Predictions)
		}
	})

	t.Run("rule3_foreign_lock_still_soft_blocks", func(t *testing.T) {
		resp := predict(t, &caller.ID, declaredWithRepo("repo-a", foreignPath), true)
		if got := namedBy(resp, 3, foreign.Slug); len(got) == 0 {
			t.Errorf("rule 3 (dry_run) did not report the foreign holder %s; predictions=%+v",
				foreign.Slug, resp.Predictions)
		}
		if resp.Severity != SeveritySoftBlock {
			t.Errorf("severity=%s for a dry_run overlap with another attempt's lock, want soft_block", resp.Severity)
		}
	})

	t.Run("mixed_payload_reports_only_the_foreign_holder", func(t *testing.T) {
		// Self-held AND foreign-held paths in one payload: rule 1 must still
		// hard-block — on the foreign row, never the caller's.
		resp := predict(t, &caller.ID, declaredTwoPathsWithRepo("repo-a", callerPath, foreignPath), false)
		if got := namedBy(resp, 1, caller.Slug); len(got) > 0 {
			t.Errorf("rule 1 named the caller in a payload that also overlaps a foreign holder: %+v", got)
		}
		if got := namedBy(resp, 1, foreign.Slug); len(got) == 0 {
			t.Errorf("rule 1 lost the foreign holder %s in a mixed payload; predictions=%+v",
				foreign.Slug, resp.Predictions)
		}
		if resp.Severity != SeverityHardBlock {
			t.Errorf("severity=%s for a mixed payload, want hard_block from the foreign row", resp.Severity)
		}
	})

	t.Run("anonymous_predict_keeps_the_self_report", func(t *testing.T) {
		// No work_item_id, no "self" to exclude: the create-preview path keeps its
		// pre-aihub#564 answer exactly, same as aihub#510's anonymous arm.
		resp := predict(t, nil, declaredWithRepo("repo-a", callerPath), false)
		if got := namedBy(resp, 1, caller.Slug); len(got) == 0 {
			t.Errorf("anonymous predict no longer reports the held lock at all — the exclusion must be "+
				"opt-in via work_item_id, not a blanket blind spot; predictions=%+v", resp.Predictions)
		}
	})

	t.Run("exclusion_holds_when_identified_by_slug", func(t *testing.T) {
		// pf-work Mode B spells the caller as a slug. A filter bound to the raw
		// parameter matches no work_items.id and silently does nothing (aihub#357
		// / aihub#510's second trap), so this arm drives the resolution end to end.
		resp := predict(t, &caller.Slug, declaredWithRepo("repo-a", callerPath), true)
		if got := namedBy(resp, 3, caller.Slug); len(got) > 0 {
			t.Errorf("rule 3 reported the caller to itself when it identified by SLUG — the exclusion is "+
				"bound to something the aihub#357 resolution never produced: %+v", got)
		}
		respHard := predict(t, &caller.Slug, declaredWithRepo("repo-a", callerPath), false)
		if got := namedBy(respHard, 1, caller.Slug); len(got) > 0 {
			t.Errorf("rule 1 reported the caller to itself when it identified by SLUG: %+v", got)
		}
	})
}
