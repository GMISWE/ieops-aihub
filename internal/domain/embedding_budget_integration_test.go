package domain

// aihub#316: the decisive end-to-end gate for the embedding budget.
//
// What made 2026-09-01 an incident was not a missing fallback — Recall,
// ListWorkItems and CreateWorkItem all already degrade gracefully when the
// provider ERRORS, and those paths were green before this branch. What failed
// was that a HUNG provider consumed the whole 30s request budget
// (server/middleware.go) before the fallback ran, so the first pool.Query /
// pool.Begin the fallback reached died on an expired context. One root cause,
// three symptoms.
//
// 🔴 So the only arm with discriminating power is a provider that HANGS,
// combined with a request deadline LARGER than the embedding budget and SMALLER
// than the hang. A provider that returns an error would pass with or without
// the fix and would prove nothing.
//
// Gated on AIHUB_TEST_DB via setupLatestTestDB (memory_latest_test.go), like
// every other integration test in this package, so it never runs in plain
// `go test ./...`:
//
//	AIHUB_TEST_DB=postgres://postgres:testpass@localhost:5432/aihub_test?sslmode=disable \
//	go test ./internal/domain/ -run TestEmbeddingBudget -v -count=1

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GMISWE/ieops-aihub/internal/embedding"
)

// hangingEmbedProvider blocks until its context is done — a saturated backend.
type hangingEmbedProvider struct{}

func (h *hangingEmbedProvider) Embed(ctx context.Context, _ string) ([]float32, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (h *hangingEmbedProvider) EmbedBatch(ctx context.Context, _ []string) ([][]float32, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (h *hangingEmbedProvider) ModelID() string { return "hanging-model" }
func (h *hangingEmbedProvider) Dims() int       { return 4 }
func (h *hangingEmbedProvider) Ping(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// installHangingProvider installs a budget-wrapped hanging provider for the
// duration of the (sub)test and restores the package default afterwards, so it
// cannot poison the rest of the package.
func installHangingProvider(t *testing.T, budget time.Duration) {
	t.Helper()
	InitEmbeddingProvider(embedding.WithBudget(&hangingEmbedProvider{}, budget))
	t.Cleanup(func() {
		InitEmbeddingProvider(&embedding.NoopProvider{})
		ResetEmbeddingHealthCache()
	})
}

// requestCtx is one simulated HTTP request's budget. The 3s is a LITERAL, not
// derived from the embedding budget: deriving it would let a change to the
// budget silently move the goalposts and keep the test green.
func requestCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestEmbeddingBudget_HangingProviderLeavesRoomForFallback(t *testing.T) {
	pool := setupLatestTestDB(t)
	uid := testUser(t, pool)
	proj := testProject(t, pool, uid)

	// Seed with embedding OFF so the fixture itself is fast and unaffected by
	// what the subtests install.
	InitEmbeddingProvider(&embedding.NoopProvider{})
	seedCtx := context.Background()
	if _, _, err := Remember(seedCtx, pool, &RememberRequest{
		Project:       proj,
		Type:          "experience.approach",
		Content:       "aihub#316 budget probe fixture: the text recall path must still return this row",
		Visibility:    "project",
		DedupMode:     "off",
		CallerUserID:  uid,
		CallerDisplay: uid,
	}); err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	wiType := "fix_bug"
	if _, aerr := CreateWorkItem(seedCtx, pool, &CreateWorkItemRequest{
		Project: proj, Goal: "aihub#316 budget probe fixture work item", Scenario: "coding",
		WIType: &wiType, Source: "human",
		ForceCreate: true, ForceReason: "aihub#316 integration fixture",
	}, uid, uid); aerr != nil {
		t.Fatalf("seed work item: %v", aerr)
	}

	query := "budget probe"

	t.Run("budget below the request deadline — all three fall back", func(t *testing.T) {
		installHangingProvider(t, 200*time.Millisecond)

		t.Run("Recall reaches the text path", func(t *testing.T) {
			ctx := requestCtx(t)
			resp, err := Recall(ctx, pool, &RecallRequest{
				Project: proj, Query: query, TopK: 5,
				CallerUserID: uid, CallerRole: "writer",
			})
			if err != nil {
				t.Fatalf("Recall: %v — the fallback ran with no budget left", err)
			}
			if len(resp.Items) == 0 {
				t.Error("Recall returned no items; the text fallback should have found the seeded memory")
			}
		})

		t.Run("ListWorkItems reaches the ILIKE path", func(t *testing.T) {
			ctx := requestCtx(t)
			q := query
			res, aerr := ListWorkItems(ctx, pool, proj, ListWorkItemsFilter{Query: &q, Limit: 10})
			if aerr != nil {
				t.Fatalf("ListWorkItems: %v — the fallback ran with no budget left", aerr)
			}
			if len(res.Items) == 0 {
				t.Error("ListWorkItems returned no items; the ILIKE fallback should have found the seeded work item")
			}
		})

		t.Run("CreateWorkItem commits with a NULL emb_vector", func(t *testing.T) {
			ctx := requestCtx(t)
			wi, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
				Project: proj, Goal: "aihub#316 created while the embedding backend hangs",
				Scenario: "coding", WIType: &wiType, Source: "human",
				ForceCreate: true, ForceReason: "aihub#316 hanging-provider positive arm",
			}, uid, uid)
			if aerr != nil {
				t.Fatalf("CreateWorkItem: %v — pool.Begin ran with no budget left", aerr)
			}
			// Verified on a fresh context: the assertion is about the stored
			// row, not about whatever the request context has left.
			var embNull bool
			if err := pool.QueryRow(context.Background(),
				`SELECT emb_vector IS NULL FROM work_items WHERE id = $1`, wi.ID).Scan(&embNull); err != nil {
				t.Fatalf("verify emb_vector: %v", err)
			}
			if !embNull {
				t.Error("emb_vector is not NULL, but the provider never returned a vector")
			}
		})
	})

	// 🔴 NEGATIVE CONTROL. With the budget LARGER than the request deadline the
	// hang eats the whole request, which is exactly the pre-fix behaviour. All
	// three must FAIL here. If this subtest passes, the positive arm above is
	// asserting nothing — it would be green against an unbounded provider too.
	t.Run("negative control: budget above the request deadline — all three fail", func(t *testing.T) {
		installHangingProvider(t, 10*time.Second)

		t.Run("Recall fails", func(t *testing.T) {
			ctx := requestCtx(t)
			if _, err := Recall(ctx, pool, &RecallRequest{
				Project: proj, Query: query, TopK: 5,
				CallerUserID: uid, CallerRole: "writer",
			}); err == nil {
				t.Error("Recall succeeded with an unbounded hanging provider — the positive arm proves nothing")
			}
		})

		t.Run("ListWorkItems fails", func(t *testing.T) {
			ctx := requestCtx(t)
			q := query
			if _, aerr := ListWorkItems(ctx, pool, proj, ListWorkItemsFilter{Query: &q, Limit: 10}); aerr == nil {
				t.Error("ListWorkItems succeeded with an unbounded hanging provider — the positive arm proves nothing")
			}
		})

		t.Run("CreateWorkItem fails, and blames the right hop", func(t *testing.T) {
			ctx := requestCtx(t)
			_, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
				Project: proj, Goal: "aihub#316 negative control", Scenario: "coding",
				WIType: &wiType, Source: "human",
				ForceCreate: true, ForceReason: "aihub#316 hanging-provider negative control",
			}, uid, uid)
			if aerr == nil {
				t.Fatal("CreateWorkItem succeeded with an unbounded hanging provider — the positive arm proves nothing")
			}
			// Part 4: the ctx-expired message must point upstream, and must not
			// say "transaction" — that word is what sent the original
			// investigation at the database, which was answering in 9ms.
			msg := strings.ToLower(aerr.Message)
			for _, want := range []string{"deadline", "upstream"} {
				if !strings.Contains(msg, want) {
					t.Errorf("error message %q does not contain %q", aerr.Message, want)
				}
			}
			if strings.Contains(msg, "transaction") {
				t.Errorf("error message %q still blames the transaction", aerr.Message)
			}
		})
	})

	// A cancelled request is a DIFFERENT event from an exhausted deadline, and
	// the two must not share a message. "deadline exhausted" printed for a
	// client that simply hung up sends the next reader hunting for a slow
	// dependency that was never slow — the same mis-attribution as the original
	// "failed to begin transaction", one layer along.
	//
	// No hanging provider here: cancellation alone reproduces it, which is the
	// point — this failure has nothing to do with embedding.
	t.Run("a cancelled request is not reported as an exhausted deadline", func(t *testing.T) {
		InitEmbeddingProvider(&embedding.NoopProvider{})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, aerr := CreateWorkItem(ctx, pool, &CreateWorkItemRequest{
			Project: proj, Goal: "aihub#316 cancelled-request attribution", Scenario: "coding",
			WIType: &wiType, Source: "human",
			ForceCreate: true, ForceReason: "aihub#316 cancellation attribution probe",
		}, uid, uid)
		if aerr == nil {
			t.Fatal("CreateWorkItem succeeded on an already-cancelled context")
		}
		msg := strings.ToLower(aerr.Message)
		if !strings.Contains(msg, "cancel") {
			t.Errorf("error message %q does not say the request was cancelled", aerr.Message)
		}
		if strings.Contains(msg, "deadline") {
			t.Errorf("error message %q blames a deadline for what was a client disconnect", aerr.Message)
		}
		if strings.Contains(msg, "transaction") {
			t.Errorf("error message %q still blames the transaction", aerr.Message)
		}
	})

	// 🔴 The attribution must not over-claim. Deciding "who ate the budget" by
	// reading ctx.Err() AFTER pool.Begin returns cannot distinguish a context
	// that was already dead on entry (embedding — the aihub#316 shape) from one
	// that died INSIDE Begin waiting for a free connection (pool exhaustion — a
	// database-side problem). Blaming the embedding provider for the second is
	// the same mis-attribution this wi exists to remove, aimed the other way.
	//
	// Set up so that only the second can be happening: embedding is switched
	// OFF entirely, and the pool has exactly one connection, already held.
	t.Run("pool exhaustion is not blamed on the embedding provider", func(t *testing.T) {
		InitEmbeddingProvider(&embedding.NoopProvider{})

		// Cloned from the pool setupLatestTestDB already built, NOT re-read from
		// os.Getenv("AIHUB_TEST_DB"): reading that variable inside a test
		// function that does not itself t.Skip on it is exactly the pattern
		// internal/citest/dbtestcov audits for, and it fails the build (caught
		// by that gate, not by me). setupLatestTestDB owns the skip.
		cfg := pool.Config().Copy()
		cfg.MaxConns = 1
		tiny, err := pgxpool.NewWithConfig(context.Background(), cfg)
		if err != nil {
			t.Fatalf("open single-connection pool: %v", err)
		}
		defer tiny.Close()

		// Occupy the one connection for longer than the request below will wait.
		hold, err := tiny.Acquire(context.Background())
		if err != nil {
			t.Fatalf("acquire the only connection: %v", err)
		}
		defer hold.Release()

		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		_, aerr := CreateWorkItem(ctx, tiny, &CreateWorkItemRequest{
			Project: proj, Goal: "aihub#316 pool exhaustion attribution", Scenario: "coding",
			WIType: &wiType, Source: "human",
			ForceCreate: true, ForceReason: "aihub#316 pool exhaustion attribution probe",
		}, uid, uid)
		if aerr == nil {
			t.Fatal("CreateWorkItem succeeded against a pool whose only connection is held")
		}
		// Note the shape of these assertions: the correct message ends
		// "...not an upstream dependency", so a bare Contains(msg, "upstream")
		// matches the message's own NEGATION and fails a passing fix. Assert on
		// the wrong message's distinctive phrase instead, plus the right one's.
		msg := strings.ToLower(aerr.Message)
		if strings.Contains(msg, "deadline exhausted before reaching the database") {
			t.Errorf("pool exhaustion reported with the embedding-budget message: %q", aerr.Message)
		}
		if strings.Contains(msg, "embedding provider") {
			t.Errorf("pool exhaustion blamed on an embedding provider that is not even enabled: %q", aerr.Message)
		}
		if !strings.Contains(msg, "connection") {
			t.Errorf("error message %q does not point at the connection pool", aerr.Message)
		}
	})
}
