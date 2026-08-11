package domain

// Unit tests for buildWorkItemUpdate (aihub#241): UpdateWorkItem's pure
// statement builder. These run unconditionally — no AIHUB_TEST_DB needed —
// because buildWorkItemUpdate takes no DB dependency. This is the "actually
// executes" coverage for the fix, unlike the DB-gated suite in
// work_items_cas_db_test.go which SKIPs without a live Postgres (that one is
// run by its own scoped CI step; see .github/workflows/ci.yml).
//
// The two regressions guarded here:
//
//  1. The counter never advanced. resources_version was written only when the
//     caller supplied one, as `= <caller value> + 1`. The ordinary path — write
//     declared_resources, pass no version, which is what every caller does and
//     what the pf-plan skill explicitly instructs — left the column untouched,
//     so it read 0 forever and CAS could never detect anything. It must now be
//     incremented BY POSTGRES, from the stored value: `resources_version =
//     resources_version + 1`. Deriving the next value from the caller's number
//     is not equivalent, because two concurrent writers holding the same stale
//     read would compute the same next value and neither would notice.
//
//  2. There was no compare-and-set at all. Supplying resources_version changed
//     what got stored but added no WHERE predicate, so a stale writer silently
//     clobbered a fresher one — the exact "silent unprotected overwrite" the
//     report describes. The version must appear as a WHERE precondition, which
//     is what lets UpdateWorkItem turn RowsAffected()==0 into a 409.

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func strptr(s string) *string { return &s }
func intptr(i int) *int       { return &i }

const legalResources = `[{"type":"path","uri":"file:internal/a.go","intent":"write"}]`

// splitSetWhere returns the SET body and the WHERE body of a built query.
// Asserting against the whole string would let a clause in the wrong half
// satisfy the wrong assertion — the increment belongs in SET and the CAS
// predicate belongs in WHERE, and swapping them is a real failure mode
// (an increment in WHERE would silently match nothing).
func splitSetWhere(t *testing.T, query string) (setBody, whereBody string) {
	t.Helper()
	const setMark = " SET "
	i := strings.Index(query, setMark)
	if i < 0 {
		t.Fatalf("query has no SET clause: %s", query)
	}
	j := strings.Index(query, " WHERE ")
	if j < 0 {
		t.Fatalf("query has no WHERE clause: %s", query)
	}
	if j < i {
		t.Fatalf("WHERE precedes SET: %s", query)
	}
	return query[i+len(setMark) : j], query[j+len(" WHERE "):]
}

// The regression that made CAS pointless: writing declared_resources without
// passing a version must STILL advance the counter.
func TestBuildWorkItemUpdate_DeclaredResourcesAlwaysIncrementsVersion(t *testing.T) {
	upd := buildWorkItemUpdate(&UpdateWorkItemRequest{
		DeclaredResources: json.RawMessage(legalResources),
	}, "wi_probe")

	setBody, whereBody := splitSetWhere(t, upd.Query)

	if !strings.Contains(setBody, "resources_version = resources_version + 1") {
		t.Errorf("writing declared_resources does not increment resources_version — the counter stays at 0 forever and no CAS can ever detect a conflict (aihub#241 B2). SET was: %s", setBody)
	}
	// The increment must be computed by Postgres from the stored row, not from a
	// bound parameter. `resources_version = $N` with a Go-side +1 would look like
	// a fix and still let two concurrent stale writers agree on the same value.
	if regexp.MustCompile(`resources_version\s*=\s*\$\d`).MatchString(setBody) {
		t.Errorf("resources_version is SET from a bound parameter instead of `resources_version + 1`; a caller-derived next value does not serialize concurrent writers (aihub#241 B2). SET was: %s", setBody)
	}
	if strings.Contains(whereBody, "resources_version") {
		t.Errorf("no resources_version was supplied, so the WHERE clause must not constrain it — an unconditional update would start failing. WHERE was: %s", whereBody)
	}
	if upd.CAS {
		t.Error("CAS is true although the caller supplied no resources_version; UpdateWorkItem would then read RowsAffected()==0 as a version conflict")
	}
}

// Supplying the version must add a real precondition, and must NOT suppress the
// increment.
func TestBuildWorkItemUpdate_ResourcesVersionAddsCASPredicate(t *testing.T) {
	upd := buildWorkItemUpdate(&UpdateWorkItemRequest{
		DeclaredResources: json.RawMessage(legalResources),
		ResourcesVersion:  intptr(7),
	}, "wi_probe")

	setBody, whereBody := splitSetWhere(t, upd.Query)

	if !strings.Contains(setBody, "resources_version = resources_version + 1") {
		t.Errorf("the increment disappeared when a version was supplied; 0 -> 1 -> 2 would not hold on the CAS path either. SET was: %s", setBody)
	}
	if !regexp.MustCompile(`resources_version\s*=\s*\$\d`).MatchString(whereBody) {
		t.Errorf("supplying resources_version added no WHERE precondition, so a stale writer still overwrites a fresher one silently (aihub#241 B1/B2). WHERE was: %s", whereBody)
	}
	if !upd.CAS {
		t.Error("CAS is false although a resources_version was supplied; UpdateWorkItem would swallow the conflict and return 200")
	}

	// The bound value must be the version the caller READ, not the incremented
	// one. Binding value+1 would make the predicate match only a row someone
	// else had already advanced — CAS inverted.
	if !containsArg(upd.Args, 7) {
		t.Errorf("args do not carry the caller's version 7 verbatim; args = %#v", upd.Args)
	}
	if containsArg(upd.Args, 8) {
		t.Errorf("args carry 8 — the caller's version was incremented before being used as the precondition, which inverts the comparison; args = %#v", upd.Args)
	}
}

// A version with no declared_resources is a plain precondition: guard, do not
// increment. This pins the two behaviours as orthogonal, so a later
// "simplification" that folds one into the other fails here.
func TestBuildWorkItemUpdate_VersionWithoutResourcesGuardsButDoesNotIncrement(t *testing.T) {
	upd := buildWorkItemUpdate(&UpdateWorkItemRequest{
		Priority:         strptr("high"),
		ResourcesVersion: intptr(3),
	}, "wi_probe")

	setBody, whereBody := splitSetWhere(t, upd.Query)

	if strings.Contains(setBody, "resources_version") {
		t.Errorf("resources_version was written although declared_resources was not touched; the counter must track declared_resources only. SET was: %s", setBody)
	}
	if !regexp.MustCompile(`resources_version\s*=\s*\$\d`).MatchString(whereBody) {
		t.Errorf("the supplied version added no precondition. WHERE was: %s", whereBody)
	}
	if !upd.CAS {
		t.Error("CAS should be true whenever a resources_version is supplied")
	}
}

// The untouched baseline: an ordinary patch must not acquire either behaviour.
func TestBuildWorkItemUpdate_UnrelatedPatchIsUnconditional(t *testing.T) {
	upd := buildWorkItemUpdate(&UpdateWorkItemRequest{
		Priority: strptr("low"),
		Goal:     strptr("do the thing"),
	}, "wi_probe")

	if strings.Contains(upd.Query, "resources_version") {
		t.Errorf("a patch touching neither declared_resources nor resources_version mentions the column: %s", upd.Query)
	}
	if upd.CAS {
		t.Error("CAS is true for a patch that supplied no version")
	}
}

// Placeholder hygiene. The builder hands $N out from a single counter shared by
// the SET and WHERE clauses, and the CAS predicate was appended after the id —
// an off-by-one there binds declared_resources JSON to the version comparison
// and fails at runtime as a 500, in a path no unit test reaches without this.
func TestBuildWorkItemUpdate_PlaceholdersAreDenseAndMatchArgs(t *testing.T) {
	cases := map[string]*UpdateWorkItemRequest{
		"resources only": {
			DeclaredResources: json.RawMessage(legalResources),
		},
		"resources + cas": {
			DeclaredResources: json.RawMessage(legalResources),
			ResourcesVersion:  intptr(2),
		},
		"everything": {
			Priority:             strptr("high"),
			Milestone:            strptr("m1"),
			WIType:               strptr("fix_bug"),
			RequiresHumanSession: boolptr(true),
			Labels:               []string{"a", "b"},
			DeclaredResources:    json.RawMessage(legalResources),
			Attrs:                json.RawMessage(`{"k":"v"}`),
			Goal:                 strptr("g"),
			Content:              strptr("c"),
			ResourcesVersion:     intptr(11),
		},
	}

	placeholder := regexp.MustCompile(`\$(\d+)`)
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			upd := buildWorkItemUpdate(req, "wi_probe")
			seen := map[int]bool{}
			max := 0
			for _, m := range placeholder.FindAllStringSubmatch(upd.Query, -1) {
				n, err := strconv.Atoi(m[1])
				if err != nil {
					t.Fatalf("unparseable placeholder %q", m[0])
				}
				if seen[n] {
					t.Errorf("placeholder $%d appears twice in %s", n, upd.Query)
				}
				seen[n] = true
				if n > max {
					max = n
				}
			}
			if max != len(upd.Args) {
				t.Fatalf("highest placeholder is $%d but %d args were bound — pgx would reject or mis-bind this: %s", max, len(upd.Args), upd.Query)
			}
			for i := 1; i <= max; i++ {
				if !seen[i] {
					t.Errorf("placeholder $%d is missing (gap in the sequence): %s", i, upd.Query)
				}
			}
			// The id is bound immediately before any CAS value, so the last two
			// args on the CAS path are (id, version) in that order.
			if req.ResourcesVersion != nil {
				last := upd.Args[len(upd.Args)-1]
				if last != *req.ResourcesVersion {
					t.Errorf("last bound arg is %#v, want the CAS version %d — the id and the version are transposed", last, *req.ResourcesVersion)
				}
			}
		})
	}
}

// Every build must stay an UPDATE of a single row by id: the CAS predicate is
// an ADDITIONAL constraint, never a replacement for the id lookup. Dropping the
// id would turn a stale-version update into a project-wide overwrite.
func TestBuildWorkItemUpdate_AlwaysConstrainedByID(t *testing.T) {
	for _, req := range []*UpdateWorkItemRequest{
		{DeclaredResources: json.RawMessage(legalResources)},
		{DeclaredResources: json.RawMessage(legalResources), ResourcesVersion: intptr(0)},
		{Priority: strptr("low")},
	} {
		upd := buildWorkItemUpdate(req, "wi_probe")
		_, whereBody := splitSetWhere(t, upd.Query)
		if !regexp.MustCompile(`^id = \$\d`).MatchString(whereBody) {
			t.Errorf("WHERE does not start by constraining id: %s", whereBody)
		}
		if !containsArg(upd.Args, "wi_probe") {
			t.Errorf("the work item id is not bound; args = %#v", upd.Args)
		}
	}
}

// Version 0 is the value every work item starts at, so it must be treated as a
// supplied precondition and not confused with "absent". `*int` makes this
// possible; a plain `int` would not, and this test fails if anyone changes it
// back.
func TestBuildWorkItemUpdate_ZeroVersionIsAPrecondition(t *testing.T) {
	upd := buildWorkItemUpdate(&UpdateWorkItemRequest{
		DeclaredResources: json.RawMessage(legalResources),
		ResourcesVersion:  intptr(0),
	}, "wi_probe")

	if !upd.CAS {
		t.Fatal("resources_version=0 was treated as absent — 0 is the initial value of every work item, so CAS would be unusable exactly on a wi nobody has touched yet (aihub#241)")
	}
	_, whereBody := splitSetWhere(t, upd.Query)
	if !regexp.MustCompile(`resources_version\s*=\s*\$\d`).MatchString(whereBody) {
		t.Errorf("resources_version=0 added no precondition. WHERE was: %s", whereBody)
	}
	if !containsArg(upd.Args, 0) {
		t.Errorf("0 was not bound as the CAS value; args = %#v", upd.Args)
	}
}

func boolptr(b bool) *bool { return &b }

func containsArg(args []any, want any) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// The 409 decision (review finding).
//
// Review of this change found that deleting the whole
// `if isCASConflict(...) { ... return 409 }` branch from UpdateWorkItem left
// `go test ./...` completely green: the only behavioural coverage of it lives
// in work_items_cas_db_test.go, which SKIPs everywhere except its own scoped CI
// step. The branch is the difference between "CAS detects the conflict" and
// "CAS silently returns 200 having changed nothing" — the exact silent
// unprotected overwrite this work item exists to remove — so it gets coverage
// that runs unconditionally: pure unit tests for the decision itself, plus a
// mutation-proven wiring guard that the branch is actually present.

func TestIsCASConflict(t *testing.T) {
	cases := []struct {
		name string
		cas  bool
		rows int64
		want bool
	}{
		{"cas requested, no row matched", true, 0, true},
		{"cas requested, row updated", true, 1, false},
		// Without a version the WHERE clause is `id = $n` alone, so zero rows
		// means the work item is gone — a 404, never a version conflict. Reading
		// it as a conflict would send the caller into a retry loop that can never
		// succeed.
		{"no cas requested, no row matched", false, 0, false},
		{"no cas requested, row updated", false, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCASConflict(tc.cas, tc.rows); got != tc.want {
				t.Errorf("isCASConflict(%v, %d) = %v, want %v", tc.cas, tc.rows, got, tc.want)
			}
		})
	}
}

// The conflict must be a 409 carrying both versions. Reporting it as a 400 is
// the specific confusion this work item was filed about: the reporter could not
// tell "your payload is malformed" from "someone else got there first", because
// every use of resources_version returned 400 whatever had happened.
func TestCASConflictErr(t *testing.T) {
	err := casConflictErr(3, 5)
	if err == nil {
		t.Fatal("casConflictErr returned nil")
	}
	if err.HTTPStatus != 409 {
		t.Errorf("HTTPStatus = %d, want 409 — a lost race is a conflict, not a bad request", err.HTTPStatus)
	}
	if err.Code != ErrConflictCASFailed {
		t.Errorf("Code = %q, want %q", err.Code, ErrConflictCASFailed)
	}
	// Both numbers must reach the caller, or they cannot compute what to retry
	// with. The client renders Details into the error string, so this is the
	// caller's only channel.
	details, ok := err.Details.(map[string]any)
	if !ok {
		t.Fatalf("Details is %T, want map[string]any", err.Details)
	}
	if details["expected_resources_version"] != 3 {
		t.Errorf("details[expected_resources_version] = %v, want 3", details["expected_resources_version"])
	}
	if details["current_resources_version"] != 5 {
		t.Errorf("details[current_resources_version] = %v, want 5", details["current_resources_version"])
	}
	for _, want := range []string{"5", "3"} {
		if !strings.Contains(err.Message, want) {
			t.Errorf("message %q does not mention %q", err.Message, want)
		}
	}
}

// A failed re-read must still produce the conflict, and must not claim the
// current version is -1.
func TestCASConflictErrWithUnknownCurrentVersion(t *testing.T) {
	err := casConflictErr(3, casVersionUnknown)
	if err.HTTPStatus != 409 {
		t.Errorf("HTTPStatus = %d, want 409 even when the re-read failed", err.HTTPStatus)
	}
	if strings.Contains(err.Message, "-1") {
		t.Errorf("message reports the sentinel as a real version: %q", err.Message)
	}
	if !strings.Contains(err.Message, "unknown") {
		t.Errorf("message does not say the current version is unknown: %q", err.Message)
	}
}

// Wiring guard: the two helpers above are only worth anything if UpdateWorkItem
// actually branches on them. Review found that deleting the branch left the
// whole suite green, so assert the branch exists — in the error-checked form, so
// that neutering it (the false-pass shape this repo has hit before) also fails.
func TestUpdateWorkItemRejectsFailedCAS(t *testing.T) {
	body := bodyOf(t, sourceOf(t, "work_items.go"), "UpdateWorkItem")

	if !strings.Contains(body, "if isCASConflict(upd.CAS, tag.RowsAffected()) {") {
		t.Error("UpdateWorkItem does not branch on isCASConflict — a failed compare-and-set returns 200 having changed nothing, which is the silent unprotected overwrite aihub#241 exists to remove")
	}
	if !strings.Contains(body, "return nil, casConflictErr(*req.ResourcesVersion, current)") {
		t.Error("UpdateWorkItem does not return casConflictErr on a failed CAS (aihub#241)")
	}
	// The UPDATE's row count must be inspected at all. `_, err = tx.Exec` for the
	// main statement would discard it and make the branch above unreachable.
	if !strings.Contains(body, "tag, err := tx.Exec(ctx, upd.Query, upd.Args...)") {
		t.Error("UpdateWorkItem discards the UPDATE's command tag, so RowsAffected() is unavailable and no CAS conflict can ever be detected (aihub#241)")
	}
	// A vanished row must not be reported as a version conflict.
	if !strings.Contains(body, "errors.Is(scanErr, pgx.ErrNoRows)") {
		t.Error("UpdateWorkItem does not distinguish a deleted work item from a version conflict; the caller would retry forever against a row that no longer exists")
	}
}
