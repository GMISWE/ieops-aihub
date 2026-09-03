package domain

// aihub#333: the set arithmetic behind expected_removals, with no database.
//
// These run on every `go test ./...`. That matters here more than usual: the
// wiring is one `if` and is covered by DB-gated tests in three packages, but the
// arithmetic is where a plausible-looking implementation goes wrong quietly, and
// every DB-gated test in this repo SKIPs on `go test ./...` while still reading
// as coverage.
//
// Two mistakes in particular are what these tables exist to reject, because both
// are what you write if you reach for the obvious comparison:
//
//   - counting or comparing LENGTHS: `[u_a] -> [u_b]` is the same length and
//     takes u_a's access away;
//   - comparing whole member OBJECTS: `[{u_a,viewer}] -> [{u_a,writer}]` differs
//     in every field and removes nobody.

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stored(pairs ...string) []projectMember {
	out := make([]projectMember, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, projectMember{UserID: pairs[i], Role: pairs[i+1]})
	}
	return out
}

func submitted(pairs ...string) []MemberInput {
	out := make([]MemberInput, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, MemberInput{UserID: pairs[i], Role: pairs[i+1]})
	}
	return out
}

func TestUndeclaredRemovals(t *testing.T) {
	cases := []struct {
		name      string
		stored    []projectMember
		submitted []MemberInput
		declared  []string
		want      []string
		why       string
	}{
		{
			name:      "AddOnlyNeedsNoDeclaration",
			stored:    stored("u_a", "viewer"),
			submitted: submitted("u_a", "viewer", "u_b", "writer"),
			want:      nil,
			why: "adding a member removes nobody. This is the case every existing caller is in " +
				"(internal/cli/init.go, and any read-add-write), and it must keep working with no new " +
				"parameter or aihub#333 is a breaking change for everyone rather than for the mistake",
		},
		{
			name:      "EmptyStoredListRemovesNobody",
			stored:    nil,
			submitted: submitted("u_a", "viewer"),
			want:      nil,
			why:       "a project with no members cannot lose one; the first write must not need a declaration",
		},
		{
			name:      "UndeclaredTruncationIsReported",
			stored:    stored("u_a", "viewer", "u_b", "writer", "u_c", "maintainer"),
			submitted: submitted("u_a", "viewer"),
			want:      []string{"u_b", "u_c"},
			why:       "the whole of aihub#333: a list short by two, with nothing said about the two",
		},
		{
			name:      "FullyDeclaredTruncationIsAllowed",
			stored:    stored("u_a", "viewer", "u_b", "writer", "u_c", "maintainer"),
			submitted: submitted("u_a", "viewer"),
			declared:  []string{"u_b", "u_c"},
			want:      nil,
			why: "the other half of the property. A check that refused this too would not be a fix, it " +
				"would be an outage: removing a member is a routine, legitimate operation",
		},
		{
			name:      "PartialDeclarationStillReportsTheRest",
			stored:    stored("u_a", "viewer", "u_b", "writer", "u_c", "maintainer"),
			submitted: submitted("u_a", "viewer"),
			declared:  []string{"u_b"},
			want:      []string{"u_c"},
			why: "declaring SOME of the removals must not authorise the others. This is the realistic " +
				"accident: an operator means to remove one person and their list is short by two",
		},
		{
			name:      "SwapOfEqualSizeIsARemoval",
			stored:    stored("u_a", "viewer"),
			submitted: submitted("u_b", "viewer"),
			want:      []string{"u_a"},
			why: "this is the case aihub#333's suggested `expected_removals: N` count cannot catch. One " +
				"member in, one out: any length or count comparison sees nothing, and u_a is gone",
		},
		{
			name:      "RoleChangeIsNotARemoval",
			stored:    stored("u_a", "viewer", "u_b", "writer"),
			submitted: submitted("u_a", "writer", "u_b", "maintainer"),
			want:      nil,
			why: "identity is the user_id. Re-grading every member changes every field of every entry and " +
				"removes nobody; an implementation comparing member OBJECTS would refuse this",
		},
		{
			name:      "DeclaringSomebodyNotThereIsNotAnError",
			stored:    stored("u_a", "viewer"),
			submitted: submitted("u_a", "viewer"),
			declared:  []string{"u_gone"},
			want:      nil,
			why: "the check is a SUBSET test, not equality. A removal that already landed (or a retry after " +
				"a timeout that actually committed) must be a no-op, not a 412 the caller cannot clear",
		},
		{
			name:      "EmptySubmittedListRemovesEverybody",
			stored:    stored("u_a", "viewer", "u_b", "writer"),
			submitted: submitted(),
			want:      []string{"u_a", "u_b"},
			why: "clearing the list is the maximal case of the same mistake — `\"members\": []` from a " +
				"client that failed to populate it wipes the project's whole access list",
		},
		{
			name:      "DuplicateStoredEntriesAreReportedOnce",
			stored:    stored("u_a", "viewer", "u_a", "writer"),
			submitted: submitted(),
			want:      []string{"u_a"},
			why: "nothing constrains the JSONB array to be a set, and a duplicated user_id must not be " +
				"named twice in the message an operator reads",
		},
		{
			name:      "ResultIsSortedNotJSONBOrder",
			stored:    stored("u_z", "viewer", "u_a", "writer", "u_m", "maintainer"),
			submitted: submitted(),
			want:      []string{"u_a", "u_m", "u_z"},
			why: "the names appear in an error message and in details; leaving them in storage order would " +
				"make the payload depend on how Postgres happened to return the array",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := undeclaredRemovals(tc.stored, tc.submitted, tc.declared)
			assert.Equal(t, tc.want, got, tc.why)
		})
	}
}

// The removal check has to dominate EVERY execution of the members UPDATE, and
// today it does for a reason that is true by accident: buildProjectUpdate — the
// one statement in this repo that writes the `members` column — has exactly one
// production caller, and the check sits immediately above it in the same
// transaction. Nothing enforces that. A second caller added later would be a
// write path with no declaration check and no failing test, which is the shape
// this repo has shipped four times: a guard pinned on a helper while a caller
// goes round it.
//
// So the count is the assertion. It is deliberately coarse — it does not try to
// prove domination by reading control flow, only that there is exactly one place
// where domination has to hold and exactly one check. If either number moves,
// somebody has to look.
func TestMembersUpdateHasOneWritePathAndOneRemovalCheck(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	writes, checks := 0, 0
	var writeFiles []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, rerr := os.ReadFile(name)
		require.NoError(t, rerr)
		body := string(src)
		// Skip the declarations themselves; only call sites count.
		w := strings.Count(body, "buildProjectUpdate(") - strings.Count(body, "func buildProjectUpdate(")
		c := strings.Count(body, "undeclaredRemovals(") - strings.Count(body, "func undeclaredRemovals(")
		if w > 0 {
			writeFiles = append(writeFiles, name)
		}
		writes += w
		checks += c
	}
	assert.Equal(t, 1, writes,
		"buildProjectUpdate has %d production call site(s) (%v), not 1. It compiles the only UPDATE that "+
			"writes projects.members, so every one of its callers needs the aihub#333 removal check above "+
			"it in the same transaction. If you added a second caller: add the check there too and raise "+
			"both numbers in this test", writes, writeFiles)
	assert.Equal(t, 1, checks,
		"undeclaredRemovals has %d production call site(s), not 1. Zero means the aihub#333 check has been "+
			"disconnected and every DB-gated test for it is now the only thing standing between a short "+
			"list and a wiped access list; more than one means the write paths have multiplied and this "+
			"test's pairing with buildProjectUpdate no longer says anything", checks)
}

// A precondition attached to nothing must be refused rather than answered with a
// 200 the caller reads as "my removal was accepted". Same reasoning aihub#260
// applied to members_version, one field over.
//
// Passes a nil pool on purpose: the check has to sit above the database, so it
// gets a behavioural test that executes on `go test ./...`. If it moved below
// checkProjectAccess this would panic instead of asserting, which is the point —
// the nil pool is what pins the check's POSITION, not just its verdict.
func TestUpdateProjectExpectedRemovalsWithNoMembersIs400(t *testing.T) {
	_, aerr := UpdateProject(t.Context(), nil, "p_probe",
		&UserRecord{ID: "u_probe", Role: "admin"},
		UpdateProjectRequest{ExpectedRemovals: []string{"u_gone"}})
	require.NotNil(t, aerr, "expected_removals with no members list was accepted; there is nothing for it "+
		"to authorise, so a 200 tells the caller a removal succeeded that was never attempted")
	assert.Equal(t, ErrBadRequest, aerr.Code)
	assert.Equal(t, 400, aerr.HTTPStatus)
	assert.Contains(t, aerr.Message, "no members")
}

// And the same request WITH members must get PAST that check — otherwise the
// test above is satisfied by a server that rejects every expected_removals,
// which would make declaring a removal impossible and every legitimate one a
// 412 nobody can clear.
//
// It cannot complete without a database, so what is asserted is that it does not
// stop at the pre-database guard: whatever happens next (a nil-pool panic, or
// some error from the pool) must not be that 400. Written to accept either
// outcome rather than requiring the panic, so it pins the VERDICT and does not
// break if pgx ever starts reporting a nil pool as an error.
func TestUpdateProjectExpectedRemovalsWithMembersPassesThePreDBGuard(t *testing.T) {
	members := []MemberInput{{UserID: "u_a", Role: "viewer"}}
	var aerr *AihubError
	func() {
		defer func() { _ = recover() }()
		_, aerr = UpdateProject(t.Context(), nil, "p_probe",
			&UserRecord{ID: "u_probe", Role: "admin"},
			UpdateProjectRequest{Members: &members, ExpectedRemovals: []string{"u_gone"}})
	}()
	if aerr != nil {
		assert.NotContains(t, aerr.Message, "no members",
			"expected_removals together with members was rejected by the \"nothing to authorise\" guard. "+
				"That guard is keyed on Members being nil and this request carries one, so a caller who "+
				"declares a removal correctly could never perform it")
		assert.NotEqual(t, ErrBadRequest, aerr.Code,
			"a well-formed members+expected_removals request was refused as a bad request before the "+
				"database was reached; the removal set can only be computed against the stored list, so "+
				"this request has to get as far as the transaction")
	}
}
