package domain

// wi_watches — the user↔work-item "watching" relation (aihub#143).
//
// Watching is set membership: a (user, wi) pair is either in the table or not.
// There is no "unwatched" row and no state to mutate, so the whole API is
// add / remove / test-membership, and both mutations are idempotent — calling
// Watch twice or Unwatch twice is a no-op, not an error. That matters because
// the /ui control is a toggle rendered from a state that may already be stale
// by the time the click arrives (two tabs, a back button); making the second
// call an error would surface a race the user cannot act on.
//
// 🔴 NOTHING IN THIS FILE IS AN AUTHORIZATION BOUNDARY. A wi_watches row says
// only "this user pressed Watch at some point" — it does not, and must not,
// grant read access to the work item. A watch survives the watcher losing
// access to the project (a project membership removal does not walk this
// table), so the row outlives the permission that allowed it to be created.
// Every read path that turns watches into visible work items must therefore
// intersect them with the caller's project scope; see the WatcherUserID
// predicate in buildListWorkItemsWhere, which is ANDed with the
// project/AccessibleProjects predicate rather than replacing it, and
// TestWatchingScope_ProjectAccessStillBounds which pins that it is.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgForeignKeyViolation is Postgres SQLSTATE 23503. Matched on the code rather
// than the message text: the message names the constraint, which a later
// migration may rename, and a renamed constraint silently turning a 404 back
// into a 500 is the kind of regression nothing goes red for.
const pgForeignKeyViolation = "23503"

// WatchWorkItem records that userID watches wiID. Idempotent.
//
// The caller is responsible for having established that userID may see wiID;
// see the header note. Returns ErrNotFound when the work item does not exist
// (the FK rejects it) so the HTTP layer can answer 404 rather than 500.
func WatchWorkItem(ctx context.Context, pool *pgxpool.Pool, userID, wiID string) *AihubError {
	if pool == nil {
		return NewErr(ErrInternalError, "watch work item: no database")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO wi_watches (user_id, work_item_id)
		VALUES ($1, $2)
		ON CONFLICT (user_id, work_item_id) DO NOTHING`, userID, wiID); err != nil {
		// A foreign-key violation here means the wi (or the user) vanished
		// between the handler's lookup and this insert. Reporting it as an
		// internal error would blame the server for a 404.
		if isForeignKeyViolation(err) {
			return NewErr(ErrNotFound, "cannot watch: work item not found")
		}
		return dbErrCause(err, "watch work item")
	}
	return nil
}

// UnwatchWorkItem removes the watch. Idempotent: removing a watch that is not
// there succeeds, because the caller's intent ("I should not be watching this")
// is satisfied either way.
func UnwatchWorkItem(ctx context.Context, pool *pgxpool.Pool, userID, wiID string) *AihubError {
	if pool == nil {
		return NewErr(ErrInternalError, "unwatch work item: no database")
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM wi_watches WHERE user_id = $1 AND work_item_id = $2`, userID, wiID); err != nil {
		return dbErrCause(err, "unwatch work item")
	}
	return nil
}

// IsWatchingWorkItem reports whether userID watches wiID.
//
// It returns a plain (bool, error) rather than *AihubError because its only
// caller renders a toggle: the /ui detail page treats any failure as "not
// watching" so a missing table (a binary-before-migration deploy) degrades the
// control instead of the page. See fetchWatching in ui_handlers_wi.go.
func IsWatchingWorkItem(ctx context.Context, pool *pgxpool.Pool, userID, wiID string) (bool, error) {
	if pool == nil {
		return false, fmt.Errorf("is watching: no database")
	}
	var found bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM wi_watches WHERE user_id = $1 AND work_item_id = $2)`,
		userID, wiID).Scan(&found)
	if err != nil {
		return false, err
	}
	return found, nil
}

// isForeignKeyViolation reports whether err is a Postgres FK violation (23503).
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == pgForeignKeyViolation
}
