//go:build integration

// DB-direct regression tests for the aihub#124 annotation feature.
//
// These call the domain layer against a real PostgreSQL database — unlike the
// pure-Go unit tests (orderVersionChain, CommitEntry round-trip), they exercise
// the actual SQL. This is the guard for the recursive-CTE version chain: an
// invalid recursive query (e.g. an illegal LIMIT in the recursive term) compiles
// and passes pure-Go tests but errors only when it hits Postgres. The same
// applies to the in-place jsonb_set resolve UPDATE.
//
// DB selection: TEST_DB_URL env (defaults to the local integration postgres).
// The test discovers an existing user + project so it is portable across any
// migrated+seeded database.
package integration_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func annotationTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DB_URL")
	if dsn == "" {
		dsn = "postgres://postgres:testpass@localhost:5433/aihub_test?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New(%s): %v", dsn, err)
	}
	return pool
}

// existingUserAndProject finds a seeded user id + project name so the tests do
// not depend on specific fixture ids.
func existingUserAndProject(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (userID, project string) {
	t.Helper()
	if err := pool.QueryRow(ctx, `SELECT id FROM users LIMIT 1`).Scan(&userID); err != nil {
		t.Fatalf("lookup user: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT name FROM projects LIMIT 1`).Scan(&project); err != nil {
		t.Fatalf("lookup project: %v", err)
	}
	return userID, project
}

func TestMemoryVersionChainDB(t *testing.T) {
	ctx := context.Background()
	pool := annotationTestPool(t)
	t.Cleanup(pool.Close) // registered first → runs last, after the row-deletion cleanups (t.Cleanup is LIFO)
	userID, project := existingUserAndProject(t, ctx, pool)

	mk := func(content string, supersedes *string) string {
		t.Helper()
		mem, _, err := domain.Remember(ctx, pool, &domain.RememberRequest{
			Project: project, Type: "fact.note", Content: content,
			Visibility: "project", DedupMode: "off",
			CallerUserID: userID, CallerDisplay: "version-chain-test",
			SupersedesMemID: supersedes,
		})
		if err != nil {
			t.Fatalf("Remember: %v", err)
		}
		return mem.ID
	}

	stamp := time.Now().UnixNano()
	v1 := mk(fmt.Sprintf("# chain-test v1 %d\n\n## Section\nbody", stamp), nil)
	v2 := mk(fmt.Sprintf("# chain-test v2 %d\n\n## Section\nbody", stamp), &v1)
	v3 := mk(fmt.Sprintf("# chain-test v3 %d\n\n## Section\nbody", stamp), &v2)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM memories WHERE id = ANY($1)`, []string{v1, v2, v3})
	})

	// Query from the MIDDLE version — must reconstruct the full lineage.
	// Before the fix this errored: "LIMIT in a recursive query is not implemented".
	chain, err := domain.MemoryVersionChain(ctx, pool, v2)
	if err != nil {
		t.Fatalf("MemoryVersionChain: %v", err)
	}
	if len(chain) != 3 {
		t.Fatalf("chain len = %d, want 3: %+v", len(chain), chain)
	}
	for i, want := range []string{v1, v2, v3} {
		if chain[i].ID != want {
			t.Errorf("chain[%d].ID = %s, want %s (oldest->newest)", i, chain[i].ID, want)
		}
	}
	if !chain[2].IsCurrent {
		t.Errorf("head (v3) IsCurrent = false, want true")
	}
	if chain[0].IsCurrent || chain[1].IsCurrent {
		t.Errorf("archived versions marked IsCurrent: %+v", chain)
	}

	// Querying from either end yields the same lineage.
	for _, from := range []string{v1, v3} {
		c, err := domain.MemoryVersionChain(ctx, pool, from)
		if err != nil {
			t.Fatalf("MemoryVersionChain(%s): %v", from, err)
		}
		if len(c) != 3 {
			t.Errorf("MemoryVersionChain(%s) len = %d, want 3", from, len(c))
		}
	}

	// A standalone (un-superseded) artifact → single-element chain, IsCurrent.
	solo := mk(fmt.Sprintf("# chain-test solo %d\n\n## Section\nbody", stamp), nil)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM memories WHERE id=$1`, solo) })
	soloChain, err := domain.MemoryVersionChain(ctx, pool, solo)
	if err != nil {
		t.Fatalf("MemoryVersionChain(solo): %v", err)
	}
	if len(soloChain) != 1 || !soloChain[0].IsCurrent {
		t.Fatalf("solo chain = %+v, want 1 element IsCurrent=true", soloChain)
	}
}

func TestResolveCommitDB(t *testing.T) {
	ctx := context.Background()
	pool := annotationTestPool(t)
	t.Cleanup(pool.Close) // registered first → runs last, after the row-deletion cleanups (t.Cleanup is LIFO)
	userID, project := existingUserAndProject(t, ctx, pool)

	mem, _, err := domain.Remember(ctx, pool, &domain.RememberRequest{
		Project: project, Type: "methodology.spec",
		Content:    fmt.Sprintf("# resolve-test %d\n\n## Overview\nbody", time.Now().UnixNano()),
		Visibility: "project", DedupMode: "off",
		CallerUserID: userID, CallerDisplay: "resolve-test",
	})
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	memID := mem.ID
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM memories WHERE id=$1`, memID) })

	// Add a section-anchored annotation (open).
	if err := domain.CommitMemory(ctx, pool, memID, "this section is too vague", userID, "reviewer", domain.CommitAnchorArgs{HeadingID: "overview", HeadingText: "Overview"}); err != nil {
		t.Fatalf("CommitMemory: %v", err)
	}

	var commitID, status string
	var anchorHeading string
	if err := pool.QueryRow(ctx,
		`SELECT c->>'id', COALESCE(c->>'status',''), COALESCE(c->'anchor'->>'heading_id','')
		   FROM memories m, jsonb_array_elements(m.commits) c WHERE m.id=$1`, memID).
		Scan(&commitID, &status, &anchorHeading); err != nil {
		t.Fatalf("read commit: %v", err)
	}
	if status != "" && status != "open" {
		t.Errorf("new commit status = %q, want open/empty", status)
	}
	if anchorHeading != "overview" {
		t.Errorf("anchor.heading_id = %q, want overview", anchorHeading)
	}

	// Resolve it (exercises the in-place jsonb_set UPDATE against real Postgres).
	if err := domain.ResolveCommit(ctx, pool, memID, commitID, "Tightened the Overview wording.", userID, "agent"); err != nil {
		t.Fatalf("ResolveCommit: %v", err)
	}

	var gotStatus, gotReply, gotResolvedBy, gotResolvedAt string
	if err := pool.QueryRow(ctx,
		`SELECT c->>'status', c->>'reply', c->>'resolved_by', COALESCE(c->>'resolved_at','')
		   FROM memories m, jsonb_array_elements(m.commits) c
		  WHERE m.id=$1 AND c->>'id'=$2`, memID, commitID).
		Scan(&gotStatus, &gotReply, &gotResolvedBy, &gotResolvedAt); err != nil {
		t.Fatalf("read resolved commit: %v", err)
	}
	if gotStatus != "resolved" {
		t.Errorf("status = %q, want resolved", gotStatus)
	}
	if gotReply != "Tightened the Overview wording." {
		t.Errorf("reply = %q", gotReply)
	}
	if gotResolvedBy != "agent" {
		t.Errorf("resolved_by = %q, want agent", gotResolvedBy)
	}
	if gotResolvedAt == "" {
		t.Errorf("resolved_at is empty, want RFC3339 timestamp")
	}
}
