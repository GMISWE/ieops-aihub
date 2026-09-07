package domain

// Regression guard for aihub#382: listWorkItemsPage's text path never checked
// rows.Err().
//
// pgx v5's Query returns only the errors met while SENDING a statement; a
// failure the server reports while EXECUTING it — a parameter that does not
// cast, a function that raises, a statement_timeout — is deferred to rows.Err()
// after the rows.Next() loop ends (pgx/v5 conn.go, Query's doc comment). A loop
// that never asks sees "no more rows" and returns an empty page with a nil
// error, which the HTTP layer serialised as 200 {"items":[]}. That is the
// cursor=garbage repro this wi opened with, and it is the dangerous direction:
// an empty page is indistinguishable from "nothing matched".
//
// Two negative arms, on purpose — the wi says fix the CLASS, not the instance:
//
//   - cursor_cast_failure_is_an_error is THE INSTANCE: the `$n::timestamptz`
//     cast of an unparseable cursor, against the real migrated schema. It calls
//     the domain function directly, so a hop-3 400 in router.go cannot satisfy
//     it — which is what keeps the domain-side check load-bearing after the
//     handler learned to reject the value first.
//
//   - forced_execution_error_is_not_an_empty_page is THE CLASS: a schema pinned
//     on every pooled connection shadows `work_items` with a view over a
//     set-returning function that RAISEs. Nothing about the request is wrong;
//     the server fails mid-execution and the caller must hear about it. Should
//     the cursor ever be parsed before it reaches SQL, this arm keeps testing
//     what the first one no longer can.
//
// Both assert on the ERROR TEXT, not merely on non-nil: a fixture pointed at an
// unmigrated database also yields "an error", and an arm that accepted any
// error would be green for the wrong reason. The control arm (a well-formed
// cursor lists normally) catches the same thing from the other side and proves
// the fix did not start rejecting every cursor.
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run '^TestListWorkItemsSurfacesExecuteTimeErrors$' -v -count=1

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestListWorkItemsSurfacesExecuteTimeErrors(t *testing.T) {
	dbURL := os.Getenv("AIHUB_TEST_DB")
	if dbURL == "" {
		t.Skip("set AIHUB_TEST_DB to run this integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// A project no fixture seeds. The arms below are about how the page is
	// READ, not what is in it, and an empty project keeps the control arm's
	// nil error from depending on any seeded row.
	const project = "pf382-no-such-project"

	// ── CONTROL. Green before and after, on purpose. ─────────────────────────
	// The RFC3339Nano shape listWorkItemsNextCursor emits, so this is exactly
	// the value a paginating caller sends back.
	t.Run("control_well_formed_cursor_lists_normally", func(t *testing.T) {
		cursor := "2026-01-02T03:04:05.123456Z"
		res, aerr := ListWorkItems(ctx, pool, project, ListWorkItemsFilter{Cursor: &cursor})
		if aerr != nil {
			t.Fatalf("a well-formed cursor must list normally; got %s: %s", aerr.Code, aerr.Message)
		}
		if res == nil {
			t.Fatal("nil result alongside a nil error")
		}
	})

	// ── THE INSTANCE. ────────────────────────────────────────────────────────
	t.Run("cursor_cast_failure_is_an_error", func(t *testing.T) {
		cursor := "garbage-not-a-timestamp"
		res, aerr := ListWorkItems(ctx, pool, project, ListWorkItemsFilter{Cursor: &cursor})
		if aerr == nil {
			n := -1
			if res != nil {
				n = len(res.Items)
			}
			t.Fatalf("cursor=%q reached `$n::timestamptz`, Postgres rejected it, and ListWorkItems "+
				"returned NO error and a page of %d items: the execute-time failure was swallowed "+
				"because rows.Err() was never checked (aihub#382)", cursor, n)
		}
		// Postgres names the offending value in its message, so a message
		// without it is a DIFFERENT error (an unmigrated database, a dropped
		// column): check the fixture, not the fix.
		if !strings.Contains(aerr.Message, cursor) {
			t.Fatalf("an error came back, but not the cast failure: %s: %s", aerr.Code, aerr.Message)
		}
		if res != nil {
			t.Errorf("an error must not travel with a page; got %d items alongside %q",
				len(res.Items), aerr.Message)
		}
	})

	// ── THE CLASS. ───────────────────────────────────────────────────────────
	t.Run("forced_execution_error_is_not_an_empty_page", func(t *testing.T) {
		const schema = "pf382_forced_execution_error"
		const raised = "pf382 forced execution failure"

		// Bootstrap on the plain pool. The function is a SETOF over the real
		// row type so the view has every column listWorkItemsPage selects and
		// Describe succeeds; only Execute fails, which is the deferred path
		// under test.
		for _, ddl := range []string{
			`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`,
			`CREATE SCHEMA ` + schema,
			`CREATE FUNCTION ` + schema + `.boom() RETURNS SETOF public.work_items LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION '` + raised + `'; END $$`,
			`CREATE VIEW ` + schema + `.work_items AS SELECT * FROM ` + schema + `.boom()`,
		} {
			if _, err := pool.Exec(ctx, ddl); err != nil {
				t.Fatalf("fixture %q: %v", ddl, err)
			}
		}
		t.Cleanup(func() { _, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })

		// Pin search_path on every pooled connection: the unqualified
		// `FROM work_items wi` resolves to the raising view, everything else
		// still finds public.
		cfg, err := pgxpool.ParseConfig(dbURL)
		if err != nil {
			t.Fatalf("parse AIHUB_TEST_DB: %v", err)
		}
		cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			_, err := conn.Exec(ctx, `SET search_path = `+schema+`, public`)
			return err
		}
		shadowed, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatalf("connect with search_path: %v", err)
		}
		defer shadowed.Close()

		// Fixture control: the shadowed table must raise when read directly.
		// Without it, a view that silently returned nothing would make the arm
		// below fail for a reason unrelated to rows.Err().
		var n int
		if err := shadowed.QueryRow(ctx, `SELECT count(*) FROM work_items`).Scan(&n); err == nil || !strings.Contains(err.Error(), raised) {
			t.Fatalf("fixture: reading the shadowed work_items should raise %q; got n=%d err=%v", raised, n, err)
		}

		res, aerr := ListWorkItems(ctx, shadowed, project, ListWorkItemsFilter{})
		if aerr == nil {
			n := -1
			if res != nil {
				n = len(res.Items)
			}
			t.Fatalf("the query raised %q during execution and ListWorkItems returned NO error and a "+
				"page of %d items: an execution failure was reported as an empty page (aihub#382)", raised, n)
		}
		if !strings.Contains(aerr.Message, raised) {
			t.Fatalf("an error came back, but not the execution failure: %s: %s", aerr.Code, aerr.Message)
		}
		if res != nil {
			t.Errorf("an error must not travel with a page; got %d items alongside %q",
				len(res.Items), aerr.Message)
		}
	})
}
