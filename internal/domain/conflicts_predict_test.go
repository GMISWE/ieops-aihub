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
	}, roles, "")
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
	}, roles, "")
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
	}, roles, "")
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
func reachedTheRules(t *testing.T, req *PredictConflictsRequest) (reached bool, aerr *AihubError) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			reached = true
		}
	}()
	_, aerr = PredictConflicts(context.Background(), nil, req, nil, "")
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
			&PredictConflictsRequest{DeclaredResources: pathPayload}, nil, "")
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
			Project: "aihub", DeclaredResources: pathPayload})
		if !reached {
			t.Fatalf("a path payload naming a project did not reach the rule loop (aerr=%+v). "+
				"Then the refusal above is not caused by the MISSING project and this pair "+
				"measures nothing", aerr)
		}
	})

	t.Run("positive control: a repo-only payload needs no project", func(t *testing.T) {
		reached, aerr := reachedTheRules(t, &PredictConflictsRequest{
			DeclaredResources: json.RawMessage(`[{"type":"repo","uri":"repo:aihub","intent":"write"}]`)})
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
				`[{"type":"path","uri":"file:go.mod","repo":"aihub","intent":"read"}]`)}, nil, "")
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canSeeProject("aihub", tc.roles, tc.role); got != tc.want {
				t.Errorf("canSeeProject = %v, want %v — %s", got, tc.want, tc.why)
			}
		})
	}
}
