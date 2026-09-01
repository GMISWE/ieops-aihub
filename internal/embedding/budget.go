package embedding

// aihub#316: a per-call ceiling on how much of a request's budget the embedding
// backend may consume.
//
// Why a decorator and not a bigger fallback: the graceful fallbacks already
// exist and are already correct (memory.go's recallText fall-through,
// work_items.go's ILIKE fall-through, wi_embedding.go's NULL-column
// best-effort). On 2026-09-01 all three still returned 500 anyway, because
// server/middleware.go gives the whole request 30s and the OpenAI provider's
// http.Client is also 30s (openai.go) — so a hung backend burned the entire
// budget, and the first pool.Query/pool.Begin the fallback reached failed with
// "context deadline exceeded". One root cause, three symptoms. The fix is to
// leave time on the clock, not to add a fourth fallback.
//
// This works because both real providers build their request with
// http.NewRequestWithContext, so a ctx deadline genuinely aborts the in-flight
// HTTP call rather than merely being observed after it returns.

import (
	"context"
	"time"
)

// DefaultBudget is the per-call ceiling FromEnv applies to a real embedding
// provider. It must stay far below server/middleware.go's 30s request timeout:
// the gap between them is exactly the time a fallback path has to reach the
// database after the embedding call gives up. 5s leaves 25s.
const DefaultBudget = 5 * time.Second

// budgetProvider bounds each call to the wrapped provider.
//
// It derives from the caller's ctx rather than replacing it, so the request
// deadline still applies: context.WithTimeout takes the earlier of the two.
type budgetProvider struct {
	inner  Provider
	budget time.Duration
}

// WithBudget returns p wrapped so that each Embed/Ping call gets at most
// budget, and EmbedBatch gets budget per text.
//
// p is returned unchanged in two cases:
//
//   - budget <= 0 — the documented escape hatch (EMBEDDING_TIMEOUT=0) for bulk
//     tooling such as cmd/aihub-embed-backfill, which is not serving a request
//     and has no 30s ceiling to protect.
//
//   - p is a *NoopProvider — load-bearing, not an optimisation. Every "is
//     embedding enabled" decision in this codebase is the type assertion in
//     domain.isNoopProvider (memory_vector.go), so wrapping a NoopProvider
//     would make that assertion false and silently route every disabled
//     deployment down the vector path.
func WithBudget(p Provider, budget time.Duration) Provider {
	if budget <= 0 {
		return p
	}
	if _, isNoop := p.(*NoopProvider); isNoop {
		return p
	}
	return &budgetProvider{inner: p, budget: budget}
}

// BudgetOf reports the per-call budget p was wrapped with, and whether p is
// bounded at all. An unwrapped provider (including a NoopProvider, which
// WithBudget deliberately never wraps) reports (0, false).
func BudgetOf(p Provider) (time.Duration, bool) {
	if bp, ok := p.(*budgetProvider); ok {
		return bp.budget, true
	}
	return 0, false
}

func (b *budgetProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	ctx, cancel := context.WithTimeout(ctx, b.budget)
	defer cancel()
	return b.inner.Embed(ctx, text)
}

// EmbedBatch scales the budget by the number of texts, because both real
// providers implement it as a sequential loop over Embed — a single per-call
// bound would strangle a batch of n at the cost of one.
//
// This is a defensive choice rather than a measured requirement: EmbedBatch has
// zero callers outside this package today (verified by grep across internal/
// and cmd/ — only the interface, the three implementations, and one test stub).
// It is written to be correct if that changes, not because anything depends on
// it now.
func (b *budgetProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	n := len(texts)
	if n < 1 {
		// An empty batch would otherwise compute a zero budget, i.e. a context
		// that is already expired before the call starts.
		n = 1
	}
	ctx, cancel := context.WithTimeout(ctx, b.budget*time.Duration(n))
	defer cancel()
	return b.inner.EmbedBatch(ctx, texts)
}

func (b *budgetProvider) ModelID() string { return b.inner.ModelID() }

func (b *budgetProvider) Dims() int { return b.inner.Dims() }

// Ping is deliberately NOT re-bounded, and that is a correctness requirement,
// not an omission.
//
// The budget exists for calls made deep inside a request path, where nobody
// between the handler and the provider is in a position to say how long to
// wait. Ping is the opposite: it is only ever called by code that already knows
// its own patience, and each caller means something different by it —
// cmd/aihub/main.go gives boot readiness 10s (a cold self-hosted backend can
// take seconds to load a model on its first embed), domain's /v1/health probe
// gives itself 2s because it must answer fast, cmd/aihub-embed-backfill sets
// its own.
//
// Clamping here would silently override all of them downward, since
// context.WithTimeout always takes the EARLIER deadline. Concretely: it shrank
// boot readiness from 10s to DefaultBudget's 5s, and main.go's response to a
// failed boot ping is to replace the provider with a NoopProvider for the whole
// process lifetime with no retry — so a backend that needed 6s to warm up would
// come up with embedding permanently off. Caught in review of aihub#316.
//
// Embed and EmbedBatch stay bounded: those have no such caller.
func (b *budgetProvider) Ping(ctx context.Context) error {
	return b.inner.Ping(ctx)
}
