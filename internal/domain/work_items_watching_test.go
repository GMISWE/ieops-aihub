package domain

// Pure-unit probes for the aihub#143 Watching predicate in
// buildListWorkItemsWhere. No database — these assert the SHAPE of the
// statement, which is where the authorization property lives.
//
// The property under test is not "the watcher filter narrows the set" (a DB test
// proves that, in ui_handlers_wi_watching_db_test.go). It is the composition:
// the wi_watches semi-join must be ANDed with the project scope, so that a
// watch row can never widen what a caller may read. That is a fact about the
// generated SQL and is checkable without a server.

import (
	"strings"
	"testing"
)

// TestBuildListWorkItemsWhere_WatcherIsAndedWithProjectScope pins the
// composition. A watch row is not a read grant: whatever the watcher filter
// does, the project clause must still be in the WHERE, joined by AND.
func TestBuildListWorkItemsWhere_WatcherIsAndedWithProjectScope(t *testing.T) {
	t.Run("single project", func(t *testing.T) {
		_, where, args := buildListWorkItemsWhere("proj", ListWorkItemsFilter{
			WatcherUserID: ptrStr("u_alice"),
		})
		if !strings.Contains(where, "wi.project = $1") {
			t.Errorf("the project scope must survive the watcher filter; got WHERE: %s", where)
		}
		if !strings.Contains(where, "EXISTS (SELECT 1 FROM wi_watches w WHERE w.work_item_id = wi.id AND w.user_id = $2)") {
			t.Errorf("watcher filter missing or malformed; got WHERE: %s", where)
		}
		if !strings.Contains(where, " AND ") {
			t.Errorf("the two clauses must be conjoined, not alternatives; got WHERE: %s", where)
		}
		if strings.Contains(where, " OR ") {
			t.Errorf("🔴 an OR here would make a watch row a read grant; got WHERE: %s", where)
		}
		if len(args) != 2 || args[0] != "proj" || args[1] != "u_alice" {
			t.Errorf("args = %#v, want [proj u_alice]", args)
		}
	})

	t.Run("cross-project allow-list", func(t *testing.T) {
		// The view-all shape: no single project, an explicit allow-list. This is
		// the one the /ui Watching scope actually issues.
		_, where, args := buildListWorkItemsWhere("", ListWorkItemsFilter{
			AccessibleProjects: []string{"a", "b"},
			WatcherUserID:      ptrStr("u_alice"),
		})
		if !strings.Contains(where, "wi.project = ANY($1)") {
			t.Fatalf("🔴 the allow-list must bound a watching query; got WHERE: %s", where)
		}
		if !strings.Contains(where, "w.user_id = $2") {
			t.Errorf("watcher must be bound as $2 after the allow-list; got WHERE: %s", where)
		}
		if !argsEqual(args[0], []string{"a", "b"}) || args[1] != "u_alice" {
			t.Errorf("args = %#v, want [[a b] u_alice]", args)
		}
	})
}

// TestBuildListWorkItemsWhere_WatcherAbsentAddsNothing is the negative control
// for the test above. Without it, a builder that emitted the wi_watches EXISTS
// unconditionally would satisfy every positive assertion while silently hiding
// every unwatched work item from every list in the product.
func TestBuildListWorkItemsWhere_WatcherAbsentAddsNothing(t *testing.T) {
	for name, f := range map[string]ListWorkItemsFilter{
		"nil pointer":   {},
		"empty user id": {WatcherUserID: ptrStr("")},
	} {
		t.Run(name, func(t *testing.T) {
			_, where, args := buildListWorkItemsWhere("proj", f)
			if strings.Contains(where, "wi_watches") {
				t.Errorf("no watcher was requested, so no wi_watches clause may appear; got: %s", where)
			}
			if len(args) != 1 {
				t.Errorf("only the project should be bound; got %#v", args)
			}
		})
	}
}

// TestBuildListWorkItemsWhere_WatcherKeepsPlaceholderNumbering guards the
// invariant the whole builder rests on: every clause that binds an arg bumps
// argIdx, so argIdx == len(args)+1 throughout. A missed bump does not error —
// it collides two values onto one placeholder and returns the wrong rows
// (aihub#147), and listWorkItemsByVector places its own placeholders after
// these on the strength of that same invariant.
func TestBuildListWorkItemsWhere_WatcherKeepsPlaceholderNumbering(t *testing.T) {
	_, where, args := buildListWorkItemsWhere("proj", ListWorkItemsFilter{
		WatcherUserID: ptrStr("u_alice"),
		Status:        []string{"queued"},
		Milestone:     ptrStr("v2"),
	})
	// $1=project, $2=watcher, $3=status, $4=milestone — watcher is emitted
	// immediately after the project scope.
	for _, want := range []string{
		"wi.project = $1",
		"w.user_id = $2",
		"wi.status = ANY($3)",
		"wi.milestone = $4",
	} {
		if !strings.Contains(where, want) {
			t.Errorf("missing %q in WHERE: %s", want, where)
		}
	}
	if len(args) != 4 {
		t.Fatalf("expected 4 bound args, got %d: %#v", len(args), args)
	}
	wantArgs := []any{"proj", "u_alice", []string{"queued"}, "v2"}
	for i := range wantArgs {
		if !argsEqual(args[i], wantArgs[i]) {
			t.Errorf("arg $%d = %#v, want %#v", i+1, args[i], wantArgs[i])
		}
	}
}
