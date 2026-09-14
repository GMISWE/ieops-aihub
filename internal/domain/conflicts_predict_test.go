package domain

// aihub#222 integration test for the predict path: PredictConflicts must scope
// file_scope overlap (Rule 3) by project, resolving the project from either the
// wi (work_item_id) or the explicit req.Project fallback. A same-project overlap
// must be flagged; a same-path-different-project one must NOT. Gated on
// AIHUB_TEST_DB like the other integration tests here.
//
//	AIHUB_TEST_DB=postgres://.../aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run TestPredictConflicts_FileScopeProjectScoped -v -count=1

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/token"
	"strings"
	"testing"
)

func TestPredictConflicts_FileScopeProjectScoped(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()

	uid := testUser(t, pool)
	projHolder := "x222pp_holder"
	projOther := "x222pp_other"
	for _, p := range []string{projHolder, projOther} {
		mustExec(t, pool, `INSERT INTO projects(name,owner_user_id) VALUES('`+p+`','`+uid+`') ON CONFLICT (name) DO NOTHING`)
	}
	mustExec(t, pool, `DELETE FROM resource_locks rl USING run_attempts ra, work_items wi
		WHERE rl.owner_attempt_id = ra.id AND ra.work_item_id = wi.id
		  AND wi.project IN ('`+projHolder+`','`+projOther+`')`)

	sharedPath := "file:pkg/gateway/engine.go"
	declared, err := json.Marshal([]DeclaredResourceItem{{Type: "path", URI: sharedPath, Intent: "write"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// A running holder in projHolder that owns the file_scope lock projHolder:path.
	wiType := "fix_bug"
	holder, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project: projHolder, Goal: "holds the file lock", Scenario: "coding", WIType: &wiType,
		DeclaredResources: declared, Source: "human", ForceCreate: true, ForceReason: "aihub#222 predict regression",
	}, uid, "tester", nil, "")
	if aerr != nil {
		t.Fatalf("CreateWorkItem holder: %v", aerr)
	}
	claim, aerr := FnClaimWorkItem(ctx, pool, holder.ID,
		&ClaimRequest{IdempotencyKey: "idem-holder", SessionInfo: SessionInfo{MachineID: "m1", SessionSecret: "s3cr3t-0123456789abcdef0123456789abcdef0123456789abcdef01234567"}},
		uid, "", "tester")
	if aerr != nil {
		t.Fatalf("claim holder: %v", aerr)
	}
	if len(claim.AcquiredLocks) == 0 {
		t.Fatalf("holder acquired no locks")
	}

	roles := map[string]string{projHolder: "owner", projOther: "owner"}
	hasFileScopePrediction := func(resp *PredictConflictsResponse) bool {
		for _, p := range resp.Predictions {
			if p.ResourceType == "file_scope" {
				return true
			}
		}
		return false
	}

	// Same project (via req.Project fallback — the newly-wired path): the candidate
	// key is projHolder:path, which overlaps the holder's lock -> Rule 3 soft_block.
	sameProj, aerr := PredictConflicts(ctx, pool, &PredictConflictsRequest{
		Project: projHolder, DeclaredResources: declared, DryRun: true,
	}, roles, "", nil)
	if aerr != nil {
		t.Fatalf("predict same-project: %v", aerr)
	}
	if !hasFileScopePrediction(sameProj) {
		t.Errorf("same-project predict should flag a file_scope overlap, got %+v", sameProj.Predictions)
	}

	// Different project, same path: candidate key is projOther:path, which does NOT
	// overlap projHolder:path -> no file_scope prediction (the fix).
	diffProj, aerr := PredictConflicts(ctx, pool, &PredictConflictsRequest{
		Project: projOther, DeclaredResources: declared, DryRun: true,
	}, roles, "", nil)
	if aerr != nil {
		t.Fatalf("predict diff-project: %v", aerr)
	}
	if hasFileScopePrediction(diffProj) {
		t.Errorf("cross-project predict must NOT flag a file_scope overlap, got %+v", diffProj.Predictions)
	}

	// Project resolved from work_item_id (a projOther wi) must behave like projOther:
	// no cross-project overlap even though req.Project is unset.
	otherWI, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
		Project: projOther, Goal: "different project, same path", Scenario: "coding", WIType: &wiType,
		DeclaredResources: declared, Source: "human", ForceCreate: true, ForceReason: "aihub#222 predict regression",
	}, uid, "tester", nil, "")
	if aerr != nil {
		t.Fatalf("CreateWorkItem otherWI: %v", aerr)
	}
	byWI, aerr := PredictConflicts(ctx, pool, &PredictConflictsRequest{
		WorkItemID: &otherWI.ID, DeclaredResources: declared, DryRun: true,
	}, roles, "", nil)
	if aerr != nil {
		t.Fatalf("predict by work_item_id: %v", aerr)
	}
	if hasFileScopePrediction(byWI) {
		t.Errorf("predict by work_item_id (projOther) must NOT flag a cross-project overlap, got %+v", byWI.Predictions)
	}
}

// --- aihub#662 -------------------------------------------------------------
//
// The two defects below are in the same function and need no database, so they
// live here beside the aihub#222 integration test rather than in a new file.
// Both are about an answer that is WRONG while looking exactly like a right
// one, which is this function's recurring failure mode.

// reachedTheRules reports whether a payload got PAST the pre-flight checks and
// into the rule loop, by running it against a nil pool and watching for the nil
// dereference the first query makes.
//
// 🔴 THE PANIC IS THE INSTRUMENT, not an accident being tolerated. A test that
// only asserted "this payload is refused" would be satisfied by a check that
// refuses everything, and that check would break every repo-only and
// service-only predict in the workspace while every assertion here stayed
// green. "It reached the first query" is the cheapest observable that tells
// "allowed through" apart from "refused", and it needs no database — the same
// nil-pool trick TestCommitGate_RepoIsRequired and the aihub#238 validation
// test already use in this package, one step further along.
// ⚠️ roles is a PARAMETER since aihub#665, not a hardcoded nil, and that is not
// plumbing. The authorization gate added there sits BEFORE the rule loop, so a
// payload naming a project the caller may not see now stops one step earlier than
// the panic this helper watches for. Passing nil here would make every "reached
// the rules" row below silently measure the new refusal instead of the old
// fallthrough — green for the wrong reason, which is this file's recurring theme.
func reachedTheRules(t *testing.T, req *PredictConflictsRequest,
	roles map[string]string) (reached bool, aerr *AihubError) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			reached = true
		}
	}()
	_, aerr = PredictConflicts(context.Background(), nil, req, roles, "", nil)
	return false, aerr
}

// TestPredictConflicts_UnresolvableProjectIsRefusedNotAnsweredEmpty is
// aihub#238's fake all-clear one field further in.
//
// 🔴 MEASURED ON PRODUCTION, 2026-09-14, not reasoned about. The SAME
// declared_resources — a correct {type:path, uri:file:…, repo:"aihub"} naming a
// file another running attempt held — answered two different things:
//
//	with project     hard_block, rule 1, naming the holder (aihub#663 / ra_ixecaTCw)
//	without project  {"predictions":[],"severity":"info"}
//
// file_scope keys are "<project>:<repo>:<path>" (fileScopeLockKey), so an empty
// project makes every probe key ":<repo>:<path>" and nothing in the table has
// ever had that shape. Zero rows match; zero rows is byte-identical to "nobody
// holds this". It cost a real round trip that day, read as "the locks are
// released" — on the HARD rule, the one whose job is to block.
//
// The refusal is therefore the whole fix: an error cannot be mistaken for an
// all-clear, and an empty prediction list can.
func TestPredictConflicts_UnresolvableProjectIsRefusedNotAnsweredEmpty(t *testing.T) {
	pathPayload := json.RawMessage(
		`[{"type":"path","uri":"file:internal/domain/conflicts.go","repo":"aihub","intent":"write"}]`)

	t.Run("a path payload with no project is refused", func(t *testing.T) {
		resp, aerr := PredictConflicts(context.Background(), nil,
			&PredictConflictsRequest{DeclaredResources: pathPayload}, nil, "", nil)
		if aerr == nil {
			t.Fatalf("PredictConflicts answered %+v with no project. Every file_scope probe key "+
				"would be built as \":<repo>:<path>\", matching no lock, so the answer is "+
				"{\"predictions\":[],\"severity\":\"info\"} — indistinguishable from a genuine "+
				"all-clear, which is what it was read as", resp)
		}
		if aerr.Code != ErrBadRequest {
			t.Errorf("code = %v, want %v", aerr.Code, ErrBadRequest)
		}
		if resp != nil {
			t.Errorf("resp = %+v alongside the error; a caller that ignores the error must not "+
				"find an empty prediction list waiting for it", resp)
		}
		// It happens before any query: the nil pool would have panicked otherwise.
	})

	t.Run("positive control: the same payload WITH a project reaches the rules", func(t *testing.T) {
		reached, aerr := reachedTheRules(t, &PredictConflictsRequest{
			Project: "aihub", DeclaredResources: pathPayload}, map[string]string{"aihub": "writer"})
		if !reached {
			t.Fatalf("a path payload naming a project did not reach the rule loop (aerr=%+v). "+
				"Then the refusal above is not caused by the MISSING project and this pair "+
				"measures nothing", aerr)
		}
	})

	t.Run("positive control: a repo-only payload needs no project", func(t *testing.T) {
		reached, aerr := reachedTheRules(t, &PredictConflictsRequest{
			DeclaredResources: json.RawMessage(`[{"type":"repo","uri":"repo:aihub","intent":"write"}]`)}, nil)
		if !reached {
			t.Fatalf("a repo-only payload with no project was refused (aerr=%+v). Rules 2, 4, 5 "+
				"and 6 match declared_resources containment and never touch the project, so "+
				"they answer exactly as well without one — refusing here would break every "+
				"create-preview predict that names only a repo, which is a worse failure than "+
				"the one being fixed", aerr)
		}
	})

	t.Run("positive control: intent=read still needs it", func(t *testing.T) {
		// derivedLock suppresses the WRITE lock for intent=read, but rule 3 still
		// runs and reports `info` — and that info is project-namespaced too. Using
		// derivedLock here instead of resourceToLock would have left exactly this
		// case answering an empty list.
		resp, aerr := PredictConflicts(context.Background(), nil,
			&PredictConflictsRequest{DeclaredResources: json.RawMessage(
				`[{"type":"path","uri":"file:go.mod","repo":"aihub","intent":"read"}]`)}, nil, "", nil)
		if aerr == nil {
			t.Fatalf("a read-intent path with no project answered %+v. Rule 3 reports overlaps "+
				"for read as `info`, and an info that silently became an empty list is the same "+
				"defect in a quieter register", resp)
		}
		// 🔴 It must be THIS refusal, not merely some refusal. Asserting only
		// `aerr != nil` would be satisfied by any future pre-flight rejection —
		// including one that refuses read-intent paths outright — and the row
		// would go on reading as evidence for a rule it no longer exercises.
		if aerr.Code != ErrBadRequest {
			t.Errorf("code = %v, want %v", aerr.Code, ErrBadRequest)
		}
		details, _ := aerr.Details.(map[string]any)
		named, _ := details["paths_needing_project"].([]string)
		if len(named) != 1 || named[0] != "file:go.mod" {
			t.Errorf("paths_needing_project = %v, want the read-intent path itself. If it is "+
				"absent, the refusal came from somewhere else and this row proves nothing about "+
				"resourceToLock being the predicate", details["paths_needing_project"])
		}
	})
}

// TestCanSeeProject_AdminsAreNotStrangers pins the arm whose absence made
// PredictConflicts redact hardest from the people entitled to the whole answer.
//
// 🔴 AN ADMIN'S ProjectRoles MAP IS EMPTY BY DESIGN (aihub#227). Every other
// visibility gate in this package pairs the map with the global role —
// CreateDependency, GetParentRef, ListChildren, FnForceTakeover all spell it
// `callerRole == "admin" || callerProjectRoles[p] != ""` — and PredictConflicts
// was the one that took only the map. Measured 2026-09-14: pf_whoami answered
// role=admin with projects[].relation=owner for aihub and project_roles={}, and
// a predict over a lock held in aihub came back
// "[conflict in project aihub, no visibility]" to that project's own owner.
//
// The membership rows are the controls, and they are the reason this is a table
// rather than one assertion: a "fix" that returned true unconditionally would
// satisfy the admin row and turn the redaction off for everybody.
func TestCanSeeProject_AdminsAreNotStrangers(t *testing.T) {
	cases := []struct {
		name  string
		roles map[string]string
		role  string
		scope *string
		want  bool
		why   string
	}{
		{
			name: "admin with the empty map every admin has",
			role: "admin", roles: map[string]string{}, want: true,
			why: "aihub#227: an admin carries no per-project rows, so a membership-only test " +
				"reads administrator-of-everything as member-of-nothing",
		},
		{
			name: "admin with a nil map", role: "admin", roles: nil, want: true,
			why: "the map is absent, not empty, on some paths; both mean the same thing",
		},
		{
			name: "member of the project", roles: map[string]string{"aihub": "writer"}, want: true,
			why: "control: the ordinary allow, which must not depend on the admin arm",
		},
		{
			name:  "member of a DIFFERENT project only",
			roles: map[string]string{"tether": "owner"}, want: false,
			why: "control: the redaction has to still fire. A canSeeProject that answered true " +
				"here would satisfy every allow row above while disclosing the holder's actor, " +
				"work item and attempt across a project boundary",
		},
		{
			name: "no role anywhere", roles: nil, want: false,
			why: "control: the anonymous/unprivileged case the fold exists for",
		},
		{
			name: "a non-admin global role does not substitute for membership",
			role: "member", roles: map[string]string{"tether": "owner"}, want: false,
			why: "only \"admin\" is the bypass; reading any non-empty callerRole as one would " +
				"disclose to every authenticated user",
		},

		// ── aihub#665: the api-key project_scope arm ─────────────────────────
		//
		// 🔴 The first of these is the row that makes the arm mean something and
		// the second is the row that stops it meaning too much. A confinement
		// that only ever denied would be indistinguishable from deleting the
		// endpoint for scoped keys; one that only ever allowed would be the
		// clause not being there at all.
		{
			name:  "scoped key, asking about its own project",
			roles: map[string]string{"aihub": "writer"}, scope: strPtr("aihub"), want: true,
			why: "control: the ordinary scoped call. Every polyforge agent that holds a " +
				"project-scoped key predicts inside that project, so a scope arm that denied " +
				"here would trade a disclosure for an outage",
		},
		{
			name:  "scoped key, asking about another project it is a member of",
			roles: map[string]string{"aihub": "writer", "tether": "owner"}, scope: strPtr("tether"),
			want: false,
			why: "the confinement is written on the KEY, so membership held by the USER does " +
				"not lift it — this is the clause hasProjectAccess applies and the one a " +
				"membership-only copy of the rule silently drops",
		},
		{
			name: "scoped ADMIN key, outside its scope",
			role: "admin", roles: map[string]string{}, scope: strPtr("tether"), want: false,
			why: "scope outranks the admin arm, exactly as in hasProjectAccess and " +
				"checkProjectAccess: a scoped admin key that ignored its own confinement " +
				"would make project_scope unissuable to an administrator, which is the one " +
				"case it is most wanted for",
		},
		{
			name:  "unscoped key is the nil pointer, not the empty string",
			roles: map[string]string{"aihub": "writer"}, scope: nil, want: true,
			why: "control: nil means unscoped. Reading it as the empty string would compare " +
				"\"\" != \"aihub\" and deny EVERY unscoped caller — the whole population",
		},
		{
			name:  "an UNRECOGNISED role is still a membership",
			roles: map[string]string{"aihub": "some_legacy_role"}, want: true,
			why: "this pins the looser `!= \"\"` spelling the doc comment argues for, which was " +
				"three paragraphs of prose with no row behind it until now. projects.members is " +
				"JSONB with no CHECK, so an unrecognised role is reachable (aihub#460), and " +
				"switching to roleLevel>=viewer here would newly REFUSE those members — the one " +
				"direction neither aihub#662 nor aihub#665 measured",
		},
		{
			name:  "scope alone does not grant membership",
			roles: nil, scope: strPtr("aihub"), want: false,
			why: "control: the arm may only NARROW. A scope that matched the project must " +
				"still fall through to the membership/admin test, or a key scoped to a " +
				"project its user was never invited to would read it",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canSeeProject("aihub", tc.roles, tc.role, tc.scope); got != tc.want {
				t.Errorf("canSeeProject = %v, want %v — %s", got, tc.want, tc.why)
			}
		})
	}
}

// --- aihub#665 -------------------------------------------------------------

// TestPredictConflictsRefusesAProjectTheCallerCannotSee is the authorization
// half, stated where it needs no database.
//
// 🔴 THE DEFECT WAS REACHABLE AND IT WAS MEASURED, not inferred from the code
// shape the work item was filed on. Against a migrated database through the real
// router (internal/server/predict_conflicts_visibility_db_test.go holds the
// end-to-end form), one authenticated caller with no role in project P:
//
//	GET /v1/work_items/<a P work item>   404, notVisibleMessage
//	POST /v1/conflicts/predict dry_run=true   "[conflict in project P, no visibility]"
//	POST /v1/conflicts/predict dry_run=false  the holder's actor_display,
//	                                          work_item_id, work_item_slug and
//	                                          attempt_id, verbatim
//
// Blind to the project by every honest route, and handed the holder's identity by
// this one. `project` was never authorized anywhere: not by the route, not by the
// handler, and not here — the roles this function already took were used only to
// fold the ANSWER, and rule 1's early return jumped over that fold.
//
// 🔴 THE ROWS THAT ALLOW ARE NOT DECORATION. This call is pf-work's pre-claim
// gate: every agent in the workspace makes it before claiming. A check that
// refused a legitimate member would be a worse outcome than the disclosure it
// replaced, so the member, admin and in-scope rows below are what tell "authorized"
// apart from "switched off". The panic is the instrument the aihub#662 helper
// above documents: reaching the first query means the payload was let through.
func TestPredictConflictsRefusesAProjectTheCallerCannotSee(t *testing.T) {
	const victim = "victimproject"
	pathPayload := json.RawMessage(
		`[{"type":"path","uri":"file:internal/secret/plan.go","repo":"r","intent":"write"}]`)
	repoOnly := json.RawMessage(`[{"type":"repo","uri":"repo:r","intent":"write"}]`)

	for _, tc := range []struct {
		name  string
		roles map[string]string
		role  string
		scope *string
		req   *PredictConflictsRequest
		allow bool
		why   string
	}{
		{
			name: "non-member naming the project outright", roles: map[string]string{"other": "owner"},
			req: &PredictConflictsRequest{Project: victim, DeclaredResources: pathPayload},
			why: "the measured disclosure: `project` is whatever the caller typed, and rule 1 " +
				"read the lock table in that namespace and named the holder",
		},
		{
			name: "caller with no role anywhere",
			req:  &PredictConflictsRequest{Project: victim, DeclaredResources: pathPayload},
			why:  "an authenticated caller is all it took; no membership was ever required",
		},
		{
			name:  "key scoped to another project, user is a member of both",
			roles: map[string]string{victim: "writer", "other": "owner"}, scope: strPtr("other"),
			req: &PredictConflictsRequest{Project: victim, DeclaredResources: pathPayload},
			why: "project_scope confines the KEY. Dropping this clause is the quiet way to " +
				"ship an authorization check that is weaker than hasProjectAccess while " +
				"looking like it",
		},
		{
			name: "member of the project", roles: map[string]string{victim: "writer"},
			req:   &PredictConflictsRequest{Project: victim, DeclaredResources: pathPayload},
			allow: true,
			why: "🔴 NEGATIVE CONTROL: the ordinary pre-claim call every agent makes. If this " +
				"row ever denies, the fix has traded a disclosure for a refusal in the one " +
				"call the whole workspace runs before claiming",
		},
		{
			name: "admin, whose ProjectRoles map is empty by design", role: "admin",
			roles: map[string]string{},
			req:   &PredictConflictsRequest{Project: victim, DeclaredResources: pathPayload},
			allow: true,
			why: "🔴 NEGATIVE CONTROL, and the aihub#227 trap aihub#662 already fell into on " +
				"the fold: a membership-only gate reads administrator-of-everything as " +
				"member-of-nothing and refuses the whole endpoint to every admin",
		},
		{
			name:  "scoped key asking about its own project",
			roles: map[string]string{victim: "writer"}, scope: strPtr(victim),
			req:   &PredictConflictsRequest{Project: victim, DeclaredResources: pathPayload},
			allow: true,
			why:   "🔴 NEGATIVE CONTROL: the scope arm may narrow, never break the scoped case",
		},
		{
			name:  "repo-only payload with no project at all",
			req:   &PredictConflictsRequest{DeclaredResources: repoOnly},
			allow: true,
			why: "🔴 NEGATIVE CONTROL: rules 2, 4, 5 and 6 match declarations and never touch " +
				"the project, which is why aihub#662's own refusal carved them out. A gate " +
				"that fired on effectiveProject==\"\" would break every anonymous " +
				"create-preview predict in the workspace — and would satisfy every deny row " +
				"above while doing it",
		},
		// ⚠️ NO ROW HERE SETS `Project` AND `WorkItemID` TOGETHER, and that gap is
		// real: a gate spelled
		// `effectiveProject != "" && req.WorkItemID == nil && !canSeeProject(…)`
		// survives this whole table. It cannot be closed here — a non-nil
		// WorkItemID runs the resolution QueryRow BEFORE the gate, so the nil pool
		// panics and the row measures the panic instead of the refusal. The
		// combination is covered against a real database by
		// internal/server/predict_conflicts_visibility_db_test.go
		// (TestPredictConflictsVisibilityAcrossProjects), subtest
		// `the_gate_is_not_suspended_by_naming_a_work_item_id_as_well`, and that
		// mutation is what put it there.
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.allow {
				reached, aerr := reachedTheRules2(t, tc.req, tc.roles, tc.role, tc.scope)
				if !reached {
					t.Fatalf("this payload did not reach the rule loop (aerr=%+v) — %s", aerr, tc.why)
				}
				return
			}
			resp, aerr := PredictConflicts(context.Background(), nil, tc.req,
				tc.roles, tc.role, tc.scope)
			if aerr == nil {
				t.Fatalf("PredictConflicts answered %+v for a project this caller may not see — %s",
					resp, tc.why)
			}
			// 🔴 It must be THIS refusal. `aerr != nil` alone would be satisfied by
			// the aihub#662 400 — which fires for a payload with no project and
			// would make every row here green while the disclosure stayed open.
			if aerr.Code != ErrNotFound {
				t.Errorf("code = %v, want %v. ErrForbidden in particular is wrong: a 403 confirms "+
					"the project exists to someone who may not see it, which is the oracle "+
					"aihub#377 closed repo-wide", aerr.Code, ErrNotFound)
			}
			if resp != nil {
				t.Errorf("resp = %+v alongside the error; a caller that ignores the error must "+
					"not find a prediction list waiting for it", resp)
			}
			// The refusal must disclose nothing itself. handlePredictConflicts
			// replaces this message with notVisibleMessage via hideNotFound, but a
			// message that named the project would leak the moment any other caller
			// forgot that wrapping.
			if strings.Contains(aerr.Message, victim) {
				t.Errorf("the refusal names the project (%q) — naming it IS the disclosure",
					aerr.Message)
			}
			// It happens before any query: the nil pool would have panicked otherwise.
		})
	}
}

// reachedTheRules2 is reachedTheRules with the caller's full identity, for the
// aihub#665 rows. Kept separate rather than widening the aihub#662 helper because
// that helper's three existing call sites are about the PAYLOAD, not the caller,
// and giving them four more arguments to ignore would bury what each one varies.
func reachedTheRules2(t *testing.T, req *PredictConflictsRequest, roles map[string]string,
	role string, scope *string) (reached bool, aerr *AihubError) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			reached = true
		}
	}()
	_, aerr = PredictConflicts(context.Background(), nil, req, roles, role, scope)
	return false, aerr
}

// TestPredictConflictsFoldsEveryPredictionItReturns is the structural half of the
// other defect: rule 1's hard_block used to `return result, nil` from inside its
// loop, jumping over the H7 visibility fold that every other rule's predictions
// pass through. One credential, one payload, dry_run deciding whether the holder's
// identity was redacted.
//
// 🔴 IT IS STRUCTURAL BECAUSE THE BEHAVIOURAL FORM CANNOT SEE THE NEXT ONE. A
// database test pins the rule that exists today; the population is "every early
// exit this function grows", and rule 1 was itself added long after the fold. So
// the assertion is on the shipped AST: no `return result` may sit before the
// `fold:` label. Re-adding the early return — the exact mutation — puts one there
// and this arm names its line.
//
// The two guards below are what stop it certifying its own blind spot:
//   - the label must EXIST, or "every return of result is after the label" is
//     vacuously true for a function that has no label and returns early again;
//   - a call to canSeeProject must sit between the label and the final return, or
//     the label could be moved past the redaction and satisfy the first rule while
//     folding nothing.
func TestPredictConflictsFoldsEveryPredictionItReturns(t *testing.T) {
	fset, file := parseDomainSource(t, "conflicts.go")
	fn := funcDeclNamed(t, fset, file, "PredictConflicts")

	var labelPos token.Pos
	var gotos int
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.LabeledStmt:
			if s.Label.Name == "fold" {
				labelPos = s.Pos()
			}
		case *ast.BranchStmt:
			if s.Tok == token.GOTO && s.Label != nil && s.Label.Name == "fold" {
				gotos++
			}
		}
		return true
	})
	if labelPos == token.NoPos {
		t.Fatal("PredictConflicts has no `fold:` label any more. Either the visibility fold was " +
			"restructured — in which case re-point this arm at whatever now guarantees every " +
			"prediction is redacted before it leaves — or rule 1's early return is back and " +
			"this test would otherwise pass by having nothing to compare against.")
	}
	if gotos == 0 {
		t.Error("nothing jumps to `fold:`. The label exists but no early exit uses it, so either " +
			"rule 1 stopped answering early (fine, delete this arm and the label together) or it " +
			"answers early some other way, which is the defect.")
	}

	// 🔴 THE PREDICATE IS "PUBLISHES AN ANSWER", NOT "IS SPELLED `result`", and
	// that distinction was measured rather than reasoned about. The first version
	// of this arm matched a return whose first result was the identifier `result`.
	// A review mutation inserting
	//
	//	if len(result.Predictions) > 0 {
	//	    truncated := result
	//	    return truncated, nil
	//	}
	//
	// before the will_unlock block SURVIVED the whole suite, database arms
	// included, and restored the aihub#665 disclosure verbatim on rule 2 — an
	// unredacted cross-project prediction to a caller with no roles. One rename
	// was the whole bypass.
	//
	// So the test is the same one innermostLoopSuppresses (predict_rule_shape_test.go)
	// already uses: a return publishes an answer unless its first result is the
	// `nil` identifier. `return nil, aerr` is error propagation and carries no
	// predictions; everything else is an answer and must have been folded.
	var early []string
	var late int
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) == 0 {
			return true
		}
		if id, ok := ret.Results[0].(*ast.Ident); ok && id.Name == "nil" {
			return true
		}
		if ret.Pos() < labelPos {
			early = append(early, fset.Position(ret.Pos()).String())
			return true
		}
		late++
		return true
	})
	for _, pos := range early {
		t.Errorf("conflicts.go:%s returns an answer BEFORE the `fold:` label, so those predictions "+
			"reach the caller unredacted.\n"+
			"    This is exactly the aihub#665 defect: rule 1's hard_block skipped the H7 fold "+
			"while rule 3's soft_block went through it, so one credential and one payload "+
			"disclosed the holder's actor_display / work_item_id / work_item_slug / attempt_id "+
			"under dry_run=false and `[conflict in project X, no visibility]` under dry_run=true.\n"+
			"    Use `goto fold` instead: it keeps the suppression property "+
			"TestOnlyTheLockTableRuleHardBlocksAndItStopsTheRulesAfterIt pins while making the "+
			"fold the single exit.", pos)
	}
	if late == 0 {
		t.Error("no answer-publishing return after the label either — this arm is comparing " +
			"nothing. Every path out of this function now propagates an error, which cannot " +
			"be right for a function whose job is to answer.")
	}

	sawFoldPredicate := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || call.Pos() < labelPos {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "canSeeProject" {
			sawFoldPredicate = true
		}
		return true
	})
	if !sawFoldPredicate {
		t.Error("no canSeeProject call sits after the `fold:` label. The label would then be a " +
			"jump to code that redacts nothing, and the first assertion in this test would be " +
			"satisfied by moving the label to the end of the function.")
	}
}
