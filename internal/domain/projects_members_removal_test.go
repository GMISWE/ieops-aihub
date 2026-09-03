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
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
			why: "adding a member removes nobody. This is the case any read-add-write caller is in, and " +
				"it must keep working with no new parameter or aihub#333 is a breaking change for " +
				"everyone rather than for the mistake. No production caller writes members today " +
				"(internal/cli/init.go patches repos only and reads Members to gate auto-init), so this " +
				"is the assertion standing in for the callers that do not exist yet",
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
			name:      "BlankStoredUserIDIsNotARemovableMember",
			stored:    stored("u_a", "viewer", "", "viewer", "   ", "writer"),
			submitted: submitted("u_a", "viewer"),
			want:      nil,
			why: "an entry that names nobody grants no access, so dropping it takes nothing away. This is " +
				"how a malformed members element arrives — encoding/json leaves the bad one zero-valued " +
				"while decoding its neighbours — and without the skip such a row makes every later " +
				"members write fail with a refusal reading \"did not declare: \", which nobody can act on",
		},
		{
			name:      "BlankStoredUserIDDoesNotMaskARealRemovalBesideIt",
			stored:    stored("u_a", "viewer", "", "viewer", "u_b", "writer"),
			submitted: submitted("u_a", "viewer"),
			want:      []string{"u_b"},
			why: "tolerating the junk entry must not tolerate the truncation next to it — that would turn " +
				"one dirty row into a licence to wipe the project's access list",
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

// The removal check has to dominate EVERY execution of every statement that
// writes the `members` column. Today exactly one such statement exists and the
// check sits immediately above it, inside the same transaction and under the same
// row lock — but nothing enforces that, and a second statement added later would
// be a write path with no declaration check and no failing test. That is the
// shape this repo has shipped four times: a guard pinned on a helper while a
// caller goes round it.
//
// # Why this counts SQL and not a Go identifier
//
// The first version of this test counted occurrences of `buildProjectUpdate(`.
// Review proved it blind to the exact bypass it is named for: a brand-new
// `conn.Exec(ctx, "UPDATE projects SET members=$1, ...")` in this package, with
// no undeclaredRemovals anywhere, left it GREEN — because it is not a caller of
// that helper, it goes ROUND it. It also went RED on prose, when a doc comment
// happened to contain the identifier followed by a paren, and blamed "a second
// caller". A guard whose subject is a Go name cannot see a write path that
// declines to use that name.
//
// So the subject is the SQL. String literals are extracted through go/ast rather
// than by regex over the file: a BasicLit cannot be a comment, which kills the
// prose false-positive structurally instead of by escaping.
//
// It stays deliberately coarse. It does not prove domination by reading control
// flow, only that there is exactly one place where domination must hold and
// exactly one check. If either number moves, somebody has to look.
func TestMembersUpdateHasOneWritePathAndOneRemovalCheck(t *testing.T) {
	// Assigns the members column: `members=$1`, `members = $1`, `members =
	// members || ...`, `members=EXCLUDED.members`. Excludes members_version via
	// the negative lookahead's stand-in — Go's regexp has no lookahead, so the
	// word boundary is spelled out by requiring the next char to be = or space.
	assign := regexp.MustCompile(`\bmembers\s*=`)
	writes, checks := 0, 0
	var writeSites []string

	root := repoRootFromDomainPackage(t)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			// Vendored/third-party trees and the git dir have nothing to say
			// about this invariant.
			if n := d.Name(); n == ".git" || n == "vendor" || n == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		rel, _ := filepath.Rel(root, path)
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.BasicLit:
				if v.Kind != token.STRING {
					return true
				}
				s, uerr := strconv.Unquote(v.Value)
				if uerr != nil {
					return true
				}
				// members_version is a different column and is assigned on the
				// same statement, so strip it before looking for `members =`.
				probe := strings.ReplaceAll(s, "members_version", "mv_column")
				if assign.MatchString(probe) {
					writes++
					writeSites = append(writeSites, fmt.Sprintf("%s:%d %q", rel, fset.Position(v.Pos()).Line, s))
				}
			case *ast.CallExpr:
				if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "undeclaredRemovals" {
					checks++
				}
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)

	assert.Equal(t, 1, writes,
		"found %d SQL string(s) assigning the members column, not 1:\n  %s\nEvery statement that writes "+
			"projects.members needs the aihub#333 removal check above it, in the same transaction and "+
			"under the same row lock. If you added one: add the check there too, then raise both numbers "+
			"here. Do NOT just raise the number", writes, strings.Join(writeSites, "\n  "))
	assert.Equal(t, 1, checks,
		"undeclaredRemovals has %d call site(s) in production code, not 1. Zero means the aihub#333 check "+
			"is disconnected and nothing stands between a short list and a wiped access list; more than "+
			"one means the write paths have multiplied and this test's pairing with the SQL count no "+
			"longer says anything", checks)
}

// repoRootFromDomainPackage walks up from this package's directory to the module
// root, so the scan above covers the whole repo rather than internal/domain. The
// first version scanned only `.` and non-recursively, which would have missed a
// write path added in any other package — and internal/server, internal/mcp and
// internal/cli all touch projects.
func repoRootFromDomainPackage(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	require.NoError(t, err)
	for i := 0; i < 8; i++ {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "walked to the filesystem root without finding go.mod")
		dir = parent
	}
	t.Fatal("go.mod not found within 8 levels above internal/domain; update this helper")
	return ""
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
