package domain

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// aihub#289 — the `type` filter rule has ONE implementation, and a type that matches
// nothing says so.

// ─── typeFilterClause: pure, and pinned against the copy it did not replace ────

func TestTypeFilterClause(t *testing.T) {
	t.Run("empty filter renders no predicate", func(t *testing.T) {
		clause, args, next := typeFilterClause(nil, 7)
		require.Empty(t, clause, "an empty filter must mean 'no type predicate', not 'matches nothing'")
		require.Nil(t, args)
		require.Equal(t, 7, next, "an empty filter must not consume a placeholder")
	})

	t.Run("exact and wildcard entries, placeholders in order", func(t *testing.T) {
		clause, args, next := typeFilterClause([]string{"rule.work", "experience.*"}, 3)
		require.Equal(t, "(type = $3 OR type LIKE $4)", clause)
		require.Equal(t, []any{"rule.work", "experience.%"}, args)
		require.Equal(t, 5, next)
	})

	t.Run("a piped value is NOT split here", func(t *testing.T) {
		// The split-or-reject decision belongs to the HTTP edge (firstPipedType in
		// internal/server/routes_memory.go), which 400s it. This asserts the builder
		// does not quietly grow a second opinion: if someone later teaches the server
		// to accept `|`, this test is where they must come and say so out loud.
		clause, args, _ := typeFilterClause([]string{"rule.work|fact.test"}, 2)
		require.Equal(t, "(type = $2)", clause)
		require.Equal(t, []any{"rule.work|fact.test"}, args,
			"the whole string is one type name — this is exactly the shape that matched nothing")
	})

	t.Run("a bare * is not a wildcard", func(t *testing.T) {
		// Only the ".*" suffix triggers LIKE. "experience" alone is an exact match, and
		// so is "*" — asserted so the wildcard trigger cannot silently widen.
		clause, args, _ := typeFilterClause([]string{"experience", "*"}, 2)
		require.Equal(t, "(type = $2 OR type = $3)", clause)
		require.Equal(t, []any{"experience", "*"}, args)
	})
}

// TestTypeFilterClause_MatchesVectorPathInlineCopy pins the shared builder against the
// third copy of the same rule, the inline block in memory_vector.go's WHERE assembly.
//
// That file belongs to another work item in flight, so this change deliberately does not
// touch it — but "two copies of one rule, and nothing notices when they diverge" is the
// condition that produced this bug's neighbours (see the reference-time parity test in
// memory_reftime_test.go, written for the same reason). This test makes the remaining
// duplication GATED: if the vector path's copy is edited without the shared builder, or
// vice versa, the build says so.
func TestTypeFilterClause_MatchesVectorPathInlineCopy(t *testing.T) {
	src, err := os.ReadFile("memory_vector.go")
	require.NoError(t, err, "memory_vector.go is committed at a fixed path; absence is a fault, not a skip")

	// The inline copy, reduced to the two decisions that can drift: what triggers the
	// wildcard branch, and what each branch emits.
	for _, want := range []struct{ desc, pat string }{
		{"wildcard trigger is the \".*\" suffix", `strings\.HasSuffix\(t, "\.\*"\)`},
		{"wildcard branch emits LIKE", `type LIKE \$%d`},
		{"wildcard arg is TrimSuffix(t,\"*\") + \"%\"", `strings\.TrimSuffix\(t, "\*"\)`},
		{"exact branch emits equality", `type = \$%d`},
		{"entries are OR'd", `strings\.Join\(typeClauses, " OR "\)`},
	} {
		require.Regexp(t, regexp.MustCompile(want.pat), string(src),
			"memory_vector.go's inline type filter no longer agrees with typeFilterClause on: %s. "+
				"Reconcile them (ideally by making the vector path call the shared builder) — a "+
				"divergence here means the same request filters differently depending on whether "+
				"an embedding provider happens to be configured", want.desc)
	}

	// And the shared builder still produces what that copy describes.
	clause, args, _ := typeFilterClause([]string{"experience.*", "rule.work"}, 2)
	require.Equal(t, "(type LIKE $2 OR type = $3)", clause)
	require.Equal(t, []any{"experience.%", "rule.work"}, args)
}

// ─── UnmatchedTypes: needs a real database ────────────────────────────────────
//
// Why a DB test and not a unit test: the whole value of this field is that its answer
// comes from the same SQL the recall filter runs. A fake would assert the mirror I
// deliberately refused to write. The query is also hand-assembled with N EXISTS
// subqueries sharing $1 and threading their own placeholders — an off-by-one there is a
// runtime bind error no pure-function test can reach.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	  go test ./internal/domain -run TestUnmatchedTypes -v

func seedTypedMemory(t *testing.T, pool *pgxpool.Pool, project, userID, id, memType, visibility string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO memories (id, project, type, content, author_user_id, author_display,
			visibility, status, tags, attrs)
		VALUES ($1, $2, $3, $1, $4, $4, $5, 'active', '{}', '{}')
		ON CONFLICT (id) DO NOTHING`,
		id, project, memType, userID, visibility)
	require.NoError(t, err)
}

func TestUnmatchedTypes(t *testing.T) {
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)
	ctx := context.Background()

	seedTypedMemory(t, pool, proj, uid, "mem_um_rule", "rule.work", "project")
	seedTypedMemory(t, pool, proj, uid, "mem_um_exp", "experience.pitfall", "project")

	req := func(types ...string) *RecallRequest {
		return &RecallRequest{
			Project: proj, Types: types,
			CallerUserID: uid, CallerRole: "member",
		}
	}

	t.Run("no type filter reports nothing", func(t *testing.T) {
		require.Empty(t, UnmatchedTypes(ctx, pool, req()))
	})

	t.Run("all entries match -> nothing to report", func(t *testing.T) {
		require.Empty(t, UnmatchedTypes(ctx, pool, req("rule.work", "experience.*")))
	})

	// The acceptance criterion this field exists for: a type guaranteed not to exist
	// must be visible in the response, not merely absent from the results.
	t.Run("a type that cannot exist is named", func(t *testing.T) {
		got := UnmatchedTypes(ctx, pool, req("zzz.definitely.not.a.type"))
		require.Equal(t, []string{"zzz.definitely.not.a.type"}, got)
	})

	// The insidious case: the recall RETURNS ROWS, so nothing looks wrong, yet one
	// requested type contributed none of them.
	t.Run("partial match still names the entry that matched nothing", func(t *testing.T) {
		got := UnmatchedTypes(ctx, pool, req("rule.work", "fact.nonexistent", "experience.*"))
		require.Equal(t, []string{"fact.nonexistent"}, got)
	})

	// The bug itself, at the layer that can now describe it.
	t.Run("the piped form is named as unmatched", func(t *testing.T) {
		got := UnmatchedTypes(ctx, pool, req("rule.work|experience.pitfall"))
		require.Equal(t, []string{"rule.work|experience.pitfall"}, got,
			"the piped string is one type name and matches nothing; before aihub#289 that "+
				"was an empty result set with no way to tell it from an empty project")
	})

	t.Run("a wildcard matching nothing is named", func(t *testing.T) {
		got := UnmatchedTypes(ctx, pool, req("experience.*", "methodology.*"))
		require.Equal(t, []string{"methodology.*"}, got)
	})

	t.Run("order is preserved and duplicates collapse", func(t *testing.T) {
		got := UnmatchedTypes(ctx, pool, req("b.missing", "rule.work", "a.missing", "b.missing"))
		require.Equal(t, []string{"b.missing", "a.missing"}, got)
	})

	// Scope guard: this field answers "is the type NAME wrong", so it must NOT fire
	// because some other filter emptied the result. min_strength is the cheapest way to
	// empty a recall while leaving the type filter perfectly valid; if this ever starts
	// reporting rule.work, the field has quietly grown a second meaning.
	t.Run("does not fire on filters other than type", func(t *testing.T) {
		r := req("rule.work")
		r.MinStrength = 999
		r.WorkItemID = strPtr("wi_does_not_exist")
		require.Empty(t, UnmatchedTypes(ctx, pool, r))
	})

	// Visibility is mirrored, not bypassed: a private memory belonging to someone else
	// is not evidence for a non-admin caller that their type matched.
	t.Run("mirrors visibility scoping", func(t *testing.T) {
		seedTypedMemory(t, pool, proj, uid, "mem_um_priv", "fact.secret", "private")
		other := &RecallRequest{
			Project: proj, Types: []string{"fact.secret"},
			CallerUserID: "u_someone_else", CallerRole: "member",
		}
		require.Equal(t, []string{"fact.secret"}, UnmatchedTypes(ctx, pool, other),
			"another user's private memory must not count as a match")
		require.Empty(t, UnmatchedTypes(ctx, pool, &RecallRequest{
			Project: proj, Types: []string{"fact.secret"},
			CallerUserID: uid, CallerRole: "member",
		}), "the author sees their own private memory, so the type IS matched for them")
	})
}

// TestUnmatchedTypes_AgreesWithRecall is the differential check: for a filter naming only
// types that do not exist, Recall must come back empty AND UnmatchedTypes must name every
// entry. Asserting both together is what makes the pair meaningful — either one alone can
// be right while the response as a whole still misleads.
func TestUnmatchedTypes_AgreesWithRecall(t *testing.T) {
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)
	ctx := context.Background()

	seedTypedMemory(t, pool, proj, uid, "mem_agree_1", "rule.work", "project")

	// Piped form: empty, and now explained.
	piped := &RecallRequest{
		Project: proj, Types: []string{"rule.work|fact.test"},
		CallerUserID: uid, CallerRole: "member",
	}
	resp, err := Recall(ctx, pool, piped)
	require.NoError(t, err)
	require.Empty(t, resp.Items, "the piped form matches nothing — this is the reproduction")
	require.Equal(t, []string{"rule.work|fact.test"}, UnmatchedTypes(ctx, pool, piped),
		"...and the caller is now told so instead of inferring 'no history'")

	// Array form over the same two names: returns the row, and reports nothing unmatched
	// for the name that exists.
	arrayForm := &RecallRequest{
		Project: proj, Types: []string{"rule.work", "fact.test"},
		CallerUserID: uid, CallerRole: "member",
	}
	resp2, err := Recall(ctx, pool, arrayForm)
	require.NoError(t, err)
	require.Len(t, resp2.Items, 1, "the array form is the working spelling")
	require.Equal(t, "rule.work", resp2.Items[0].Type)
	require.Equal(t, []string{"fact.test"}, UnmatchedTypes(ctx, pool, arrayForm),
		"fact.test is a legitimate name with no rows here — reported, and correctly NOT rule.work")
}

// TestUnmatchedTypes_NeverBreaksRecall: the diagnostic is an add-on. A closed pool stands
// in for any query failure; the contract is that it reports nothing rather than surfacing
// an error on a request whose real answer is already in hand.
func TestUnmatchedTypes_NeverBreaksRecall(t *testing.T) {
	dbURL := os.Getenv("AIHUB_TEST_DB")
	if dbURL == "" {
		t.Skip("set AIHUB_TEST_DB to run this integration test")
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)
	pool.Close()

	got := UnmatchedTypes(context.Background(), pool, &RecallRequest{
		Project: "p_anything", Types: []string{"rule.work"}, CallerRole: "member",
	})
	require.Nil(t, got, "a failed diagnostic must degrade to silence, never to an error")
}

// TestUnmatchedTypes_SQLShape covers the placeholder threading without a database: the
// query is assembled by hand, and $N drifting out of step with the args slice is a bind
// error at runtime rather than a compile error.
func TestUnmatchedTypes_SQLShape(t *testing.T) {
	// Reconstruct what UnmatchedTypes builds for three entries by exercising the shared
	// builder from the same starting index it uses for a non-admin caller ($1 project,
	// $2 author_user_id => entries start at $3).
	idx := 3
	var sels []string
	var args []any
	for i, typ := range []string{"rule.work", "experience.*", "zzz.nope"} {
		clause, cargs, next := typeFilterClause([]string{typ}, idx)
		sels = append(sels, clause+" AS m"+string(rune('0'+i)))
		args = append(args, cargs...)
		idx = next
	}
	require.Equal(t, []string{"(type = $3) AS m0", "(type LIKE $4) AS m1", "(type = $5) AS m2"}, sels)
	require.Equal(t, []any{"rule.work", "experience.%", "zzz.nope"}, args)
	require.Equal(t, 6, idx, "each entry must consume exactly one placeholder")

	// Every placeholder from $3 up to the highest emitted must appear exactly once, and
	// the arg count must line up with the top index.
	joined := strings.Join(sels, ", ")
	for n := 3; n < idx; n++ {
		require.Equal(t, 1, strings.Count(joined, "$"+string(rune('0'+n))),
			"placeholder $%d must appear exactly once", n)
	}
	require.Equal(t, idx-3, len(args))
}
