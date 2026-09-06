package domain

// DB-free acceptance for aihub#371: Remember accepted project=A together with
// work_item_id=<a work item in B> and wrote the pair verbatim.
//
// 🔴 Why these arms are DB-free when the sibling suite
// (internal/server/remember_work_item_scope_db_test.go) is not, and why both
// exist. The full-stack suite is the one that proves the endpoint refuses the
// write; it needs a real Postgres because the property is about rows. These
// arms prove something the full-stack suite structurally cannot: WHERE the
// scope lives. A resolver that takes the project as an argument and then
// ignores it in the SQL passes every end-to-end arm on a single-project fixture
// and fails only under a fixture that has two — so the two assertions that
// matter here are that the project reaches the query as a bound parameter AND
// that the query text constrains on it. They are separate arms because they
// fail to two different mutants:
//
//   - drop `AND project = $2` and the parameter with it -> the behavioural arms
//     go red, because the fake then answers unscoped exactly as the
//     pre-aihub#371 GetWorkItem lookup did;
//   - drop only the predicate, leaving the parameter bound and unused -> every
//     behavioural arm stays GREEN (the fake cannot see SQL semantics) and only
//     the query-text arm goes red.
//
// One of those alone would have been a gate with a hole in it.

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeWorkItemRow is the one-column pgx.Row resolveRememberWorkItemRef scans.
type fakeWorkItemRow struct {
	id  string
	err error
}

func (r fakeWorkItemRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 1 {
		return fmt.Errorf("expected the resolver to scan exactly one column, got %d", len(dest))
	}
	p, ok := dest[0].(*string)
	if !ok {
		return fmt.Errorf("expected the resolver to scan into *string, got %T", dest[0])
	}
	*p = r.id
	return nil
}

// fakeWorkItem is one row of the work_items table, narrowed to the three
// columns the resolving query can possibly consult.
type fakeWorkItem struct{ id, slug, project string }

// fakeWorkItems is a Querier standing in for the work_items table.
//
// 🔴 It models the query by what the query BINDS, which is the whole reason it
// can tell the fixed code from the broken code. A lookup that passes only the
// reference cannot be scoping by anything, so this answers it unscoped — which
// is precisely the behaviour of the GetWorkItem call aihub#371 replaced. A
// lookup that also binds a project is answered scoped. Nothing here parses SQL;
// the query TEXT is asserted separately, by the arm that exists for the mutant
// this modelling cannot catch.
type fakeWorkItems struct {
	rows     []fakeWorkItem
	lastSQL  string
	lastArgs []any
	calls    int
}

func (f *fakeWorkItems) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, fmt.Errorf("resolveRememberWorkItemRef must not Exec")
}

func (f *fakeWorkItems) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.calls++
	f.lastSQL = sql
	f.lastArgs = args

	if len(args) == 0 {
		return fakeWorkItemRow{err: fmt.Errorf("the resolving query bound no parameters at all")}
	}
	ref, _ := args[0].(string)
	scoped := len(args) >= 2
	scope := ""
	if scoped {
		scope, _ = args[1].(string)
	}
	for _, r := range f.rows {
		if r.id != ref && r.slug != ref {
			continue
		}
		if scoped && r.project != scope {
			continue
		}
		return fakeWorkItemRow{id: r.id}
	}
	return fakeWorkItemRow{err: pgx.ErrNoRows}
}

// scopeFixture is one work item in the project being written to and one in a
// project the write has nothing to do with. The second is the entire point:
// with only the first, an unscoped resolver is indistinguishable from a scoped
// one.
func scopeFixture() *fakeWorkItems {
	return &fakeWorkItems{rows: []fakeWorkItem{
		{id: "wi_aaaaaaaa", slug: "proj_a#7", project: "proj_a"},
		{id: "wi_bbbbbbbb", slug: "proj_b#7", project: "proj_b"},
	}}
}

func TestRememberWorkItemRefIsScopedToTheRequestProject(t *testing.T) {
	ctx := context.Background()

	// ── 1/2. The positive control, in both spellings. Green before and after
	//        the fix, and here so that "nothing resolves any more" cannot pass
	//        for a fix — refusing every work_item_id would satisfy every
	//        negative arm below on its own.
	t.Run("a work item in the request project resolves by slug", func(t *testing.T) {
		q := scopeFixture()
		got, aerr := resolveRememberWorkItemRef(ctx, q, "proj_a#7", "proj_a")
		require.Nil(t, aerr, "%+v", aerr)
		assert.Equal(t, "wi_aaaaaaaa", got, "aihub#127: a slug must still resolve to the canonical id the FK requires")
	})

	t.Run("and by canonical id", func(t *testing.T) {
		q := scopeFixture()
		got, aerr := resolveRememberWorkItemRef(ctx, q, "wi_aaaaaaaa", "proj_a")
		require.Nil(t, aerr, "%+v", aerr)
		assert.Equal(t, "wi_aaaaaaaa", got)
	})

	// ── 3/4. The defect. RED before the fix in both spellings — the canonical
	//        id arm matters on its own because a caller who already knows the id
	//        never needs the slug, so a fix that only scoped slug lookups would
	//        leave the hole open for exactly the caller most likely to hit it.
	t.Run("a work item in another project does not resolve by slug", func(t *testing.T) {
		q := scopeFixture()
		got, aerr := resolveRememberWorkItemRef(ctx, q, "proj_b#7", "proj_a")
		require.NotNil(t, aerr,
			"resolved %q: project=proj_a with a work item in proj_b writes a memory that recall can never return", got)
		assert.Equal(t, ErrNotFound, aerr.Code)
	})

	t.Run("a work item in another project does not resolve by canonical id", func(t *testing.T) {
		q := scopeFixture()
		got, aerr := resolveRememberWorkItemRef(ctx, q, "wi_bbbbbbbb", "proj_a")
		require.NotNil(t, aerr, "resolved %q", got)
		assert.Equal(t, ErrNotFound, aerr.Code)
	})

	// ── 5. Containment: closing the write hole must not open a read one. A
	//       work item that exists in another project and one that exists
	//       nowhere have to answer the same way, or `<project>#<seq>` — two
	//       guessable tokens — becomes an existence oracle for a caller who is
	//       403'd on every honest read of that project. Compared with each
	//       reference blanked, because the reference is the caller's own string
	//       and echoing it back discloses nothing.
	t.Run("an out-of-project reference answers exactly what a nonexistent one does", func(t *testing.T) {
		q := scopeFixture()
		_, invisible := resolveRememberWorkItemRef(ctx, q, "proj_b#7", "proj_a")
		_, absent := resolveRememberWorkItemRef(ctx, q, "proj_a#999999", "proj_a")
		require.NotNil(t, invisible)
		require.NotNil(t, absent)

		assert.Equal(t, absent.Code, invisible.Code,
			"a work item in another project must not be distinguishable from one that does not exist")
		assert.Equal(t, absent.HTTPStatus, invisible.HTTPStatus)
		// Blank each reference before comparing: the reference is the caller's
		// own string and echoing it back discloses nothing, so it is the ONLY
		// licensed difference. Everything else must match.
		blankedInvisible := strings.ReplaceAll(invisible.Message, "proj_b#7", "<ref>")
		assert.Equal(t,
			strings.ReplaceAll(absent.Message, "proj_a#999999", "<ref>"),
			blankedInvisible,
			"the two messages differ by more than the caller's own reference, which is the oracle itself")
		// Stated a second way, directly. Note this has to run on the BLANKED
		// message: the reference proj_b#7 contains "proj_b" as a substring, so
		// asserting on the raw message reports the caller's own input as a leak.
		// That is not pedantry — it fired on the first run of this test.
		assert.NotContains(t, blankedInvisible, "proj_b",
			"naming the project the work item is really in is the leak stated the long way round")
	})

	// ── 6. WHERE the scope lives, half one: the project must reach the
	//       database as a bound parameter. Without this a resolver that takes
	//       `project` and never passes it on satisfies nothing but its
	//       signature.
	t.Run("the request project is bound into the resolving query", func(t *testing.T) {
		q := scopeFixture()
		_, _ = resolveRememberWorkItemRef(ctx, q, "proj_a#7", "proj_a")
		require.Equal(t, 1, q.calls, "the resolution must be one query, not a resolve-then-check pair")
		require.Len(t, q.lastArgs, 2,
			"the query bound %d parameter(s); with the project missing the lookup cannot be scoped at all: %v",
			len(q.lastArgs), q.lastArgs)
		assert.Equal(t, "proj_a#7", q.lastArgs[0])
		assert.Equal(t, "proj_a", q.lastArgs[1],
			"the second parameter must be the project the memory is being written to")
	})

	// ── 7. WHERE the scope lives, half two: the query has to USE that
	//       parameter. Arm 6 cannot see this — the fake scopes on the argument
	//       list, so a SQL string that binds $2 and ignores it passes arm 6 and
	//       every behavioural arm above while the defect is fully intact.
	t.Run("the resolving query constrains on that parameter", func(t *testing.T) {
		q := scopeFixture()
		_, _ = resolveRememberWorkItemRef(ctx, q, "proj_a#7", "proj_a")
		normalized := strings.Join(strings.Fields(q.lastSQL), " ")
		assert.Contains(t, normalized, "project = $2",
			"the project scope must be a predicate in the SQL, not a comparison on its result "+
				"(aihub#357: resolve-then-403 is the same oracle wearing a different hat). Query was: %s", normalized)
		assert.Contains(t, normalized, "id = $1 OR slug = $1",
			"aihub#127: the lookup must still accept either spelling of the reference")
	})
}

// TestRememberRoutesWorkItemIdThroughTheScopedResolver is the wiring arm.
//
// 🔴 Every arm above calls resolveRememberWorkItemRef directly, so all of them
// stay green if Remember never calls it — which is the single most likely way
// for this fix to be undone, because reinstating the old `GetWorkItem` lookup
// compiles, passes the whole DB-free suite, and looks like a cleanup. Reading
// the AST rather than matching strings so that a rename or a reformat cannot
// quietly turn this into an assertion about nothing.
func TestRememberRoutesWorkItemIdThroughTheScopedResolver(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "memory.go", nil, 0)
	require.NoError(t, err, "parsing memory.go")

	var body *ast.BlockStmt
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == "Remember" {
			body = fn.Body
			return false
		}
		return true
	})
	require.NotNil(t, body, "Remember not found in memory.go — this test is asserting about nothing")

	calls := map[string]int{}
	var scopedResolverArgs []ast.Expr
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok {
			calls[ident.Name]++
			if ident.Name == "resolveRememberWorkItemRef" {
				scopedResolverArgs = call.Args
			}
		}
		return true
	})

	require.Equal(t, 1, calls["resolveRememberWorkItemRef"],
		"Remember must resolve work_item_id through the scoped resolver exactly once; it called it %d times",
		calls["resolveRememberWorkItemRef"])
	assert.Zero(t, calls["GetWorkItem"],
		"Remember calls GetWorkItem again: that lookup resolves any work item on the server and compares "+
			"its project against nothing, which is the whole of aihub#371")

	// The resolver's own signature cannot enforce which project it is handed —
	// `req.Project` and, say, `wi.Project` are both plain strings. Pin it here.
	require.Len(t, scopedResolverArgs, 4, "unexpected resolver arity")
	sel, ok := scopedResolverArgs[3].(*ast.SelectorExpr)
	require.True(t, ok, "the project argument must be req.Project, got %T", scopedResolverArgs[3])
	recv, ok := sel.X.(*ast.Ident)
	require.True(t, ok)
	assert.Equal(t, "req", recv.Name)
	assert.Equal(t, "Project", sel.Sel.Name,
		"the scope must be the project the memory is being written to; any other source re-opens the mismatch")
}
