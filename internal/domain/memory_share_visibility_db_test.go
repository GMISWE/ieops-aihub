package domain

// Integration test for the pre-share visibility bookkeeping SetMemoryVisibility
// performs (aihub#151). Gated on AIHUB_TEST_DB like the rest of the DB suites in
// this package, so plain `go test ./...` skips it:
//
//	AIHUB_TEST_DB=postgres://user:pass@127.0.0.1:5433/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run TestSetMemoryVisibility_PreShareTier -v -count=1
//
// Why this one needs a real database rather than a fake: the whole fix for
// aihub#151 defect 1 IS a SQL expression. The handler-level tests in
// internal/server can only observe which visibility string the handler asks for;
// whether the tier that string is derived from was ever written down, and whether
// it survives a second share, is a property of this UPDATE and of nothing else. A
// mistake here (recording 'public' over the real tier, say) leaves every
// handler test green and makes the fix a no-op.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// seedVisibilityMemory inserts one memory row with the given visibility and attrs
// and returns its id. attrsJSON is spliced as a literal; every caller below passes
// a constant.
func seedVisibilityMemory(t *testing.T, pool *pgxpool.Pool, proj, author, id, visibility, attrsJSON string) string {
	t.Helper()
	mustExec(t, pool, `DELETE FROM memories WHERE id='`+id+`'`)
	mustExec(t, pool, `INSERT INTO memories(id,project,author_user_id,type,content,visibility,attrs)
		VALUES('`+id+`','`+proj+`','`+author+`','methodology.spec','body','`+visibility+`','`+attrsJSON+`'::jsonb)`)
	return id
}

// readVisibility returns the row's visibility column and the recorded pre-share
// tier (empty string when the attrs key is absent).
func readVisibility(t *testing.T, pool *pgxpool.Pool, id string) (visibility, recorded string) {
	t.Helper()
	var rec *string
	err := pool.QueryRow(context.Background(),
		`SELECT visibility, attrs->>'`+PreShareVisibilityKey+`' FROM memories WHERE id=$1`, id).
		Scan(&visibility, &rec)
	require.NoError(t, err)
	if rec != nil {
		recorded = *rec
	}
	return visibility, recorded
}

func TestSetMemoryVisibility_PreShareTier(t *testing.T) {
	pool := setupLatestTestDB(t)
	ctx := context.Background()
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	t.Run("share records the tier being left behind", func(t *testing.T) {
		// 'private' is the tier aihub#151 is about: unshare used to hard-code
		// 'project', so a share round trip published an author-only memory to
		// everyone in the project.
		id := seedVisibilityMemory(t, pool, proj, uid, "mem_pfshare_priv", "private", "{}")
		require.Nil(t, SetMemoryVisibility(ctx, pool, id, "public"))

		vis, rec := readVisibility(t, pool, id)
		require.Equal(t, "public", vis)
		require.Equal(t, "private", rec, "the pre-share tier must be written down at share time; at unshare time the column already reads 'public' and it is unrecoverable")
	})

	t.Run("re-sharing an already public memory does not overwrite the record", func(t *testing.T) {
		// The trap: without the `visibility <> 'public'` arm, a second share
		// records 'public' as the pre-share tier and unshare restores the row to
		// public — a silent no-op revoke, which is worse than the bug being fixed.
		id := seedVisibilityMemory(t, pool, proj, uid, "mem_pfshare_twice", "admin", "{}")
		require.Nil(t, SetMemoryVisibility(ctx, pool, id, "public"))
		require.Nil(t, SetMemoryVisibility(ctx, pool, id, "public"))

		vis, rec := readVisibility(t, pool, id)
		require.Equal(t, "public", vis)
		require.Equal(t, "admin", rec)
	})

	t.Run("unshare clears the record", func(t *testing.T) {
		id := seedVisibilityMemory(t, pool, proj, uid, "mem_pfshare_clear", "team", "{}")
		require.Nil(t, SetMemoryVisibility(ctx, pool, id, "public"))
		_, rec := readVisibility(t, pool, id)
		require.Equal(t, "team", rec)

		require.Nil(t, SetMemoryVisibility(ctx, pool, id, "team"))
		vis, rec := readVisibility(t, pool, id)
		require.Equal(t, "team", vis)
		require.Equal(t, "", rec, "a memory that is no longer public is not in the borrowed-visibility state the key describes")
	})

	t.Run("other attrs keys survive both directions", func(t *testing.T) {
		// jsonb_set and the `-` operator both rewrite the whole document, so this
		// is the assertion that the bookkeeping is not eating unrelated attrs —
		// memories carry related_ids, similar_to and annotation state in here.
		id := seedVisibilityMemory(t, pool, proj, uid, "mem_pfshare_attrs", "project",
			`{"related_ids":["mem_x"],"similar_to":"mem_y"}`)

		require.Nil(t, SetMemoryVisibility(ctx, pool, id, "public"))
		var related, similar string
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT attrs->'related_ids'->>0, attrs->>'similar_to' FROM memories WHERE id=$1`, id).
			Scan(&related, &similar))
		require.Equal(t, "mem_x", related)
		require.Equal(t, "mem_y", similar)

		require.Nil(t, SetMemoryVisibility(ctx, pool, id, "project"))
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT attrs->'related_ids'->>0, attrs->>'similar_to' FROM memories WHERE id=$1`, id).
			Scan(&related, &similar))
		require.Equal(t, "mem_x", related)
		require.Equal(t, "mem_y", similar)
	})

	t.Run("missing row still reports not found", func(t *testing.T) {
		// The rewritten statement is longer than the one it replaced; this pins
		// that its WHERE clause still selects by id and nothing else.
		aerr := SetMemoryVisibility(ctx, pool, "mem_pfshare_absent", "public")
		require.NotNil(t, aerr)
		require.Equal(t, ErrNotFound, aerr.Code)
	})
}
