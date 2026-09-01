package embedding

// aihub#316 unit tests for the per-call embedding budget.
//
// 🔴 The discriminating arm is a provider that HANGS, not one that errors.
// Error-dimension fallbacks already existed and were already green before this
// wi; a test built on an always-fails provider passes with the decorator
// removed and therefore proves nothing. Every hang test below pairs a long
// parent deadline with a short budget and asserts on the PARENT afterwards.

import (
	"context"
	"testing"
	"time"
)

// hangingProvider blocks until its context is done — the shape of a saturated
// embedding backend, which is what produced the 2026-09-01 incident.
type hangingProvider struct{}

func (h *hangingProvider) Embed(ctx context.Context, _ string) ([]float32, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (h *hangingProvider) EmbedBatch(ctx context.Context, _ []string) ([][]float32, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (h *hangingProvider) ModelID() string { return "hanging-model" }
func (h *hangingProvider) Dims() int       { return 7 }
func (h *hangingProvider) Ping(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestWithBudget_NoopProviderIsNeverWrapped guards the trap that makes this
// decorator dangerous: domain.isNoopProvider decides "is embedding enabled"
// with `p.(*embedding.NoopProvider)`, so a wrapped NoopProvider would silently
// switch every disabled deployment onto the vector path. Pointer identity is
// the assertion, because a *different* NoopProvider would satisfy the type
// assertion while still proving nothing about wrapping.
func TestWithBudget_NoopProviderIsNeverWrapped(t *testing.T) {
	noop := &NoopProvider{}
	got := WithBudget(noop, time.Second)
	if got != Provider(noop) {
		t.Fatalf("WithBudget wrapped a *NoopProvider: got %T (%p), want the same pointer %p", got, got, noop)
	}
	if _, bounded := BudgetOf(got); bounded {
		t.Error("BudgetOf reports a budget on a NoopProvider")
	}
}

// TestWithBudget_ZeroBudgetReturnsProviderUnchanged covers the documented
// EMBEDDING_TIMEOUT=0 escape hatch for bulk tooling.
func TestWithBudget_ZeroBudgetReturnsProviderUnchanged(t *testing.T) {
	inner := &hangingProvider{}
	for _, budget := range []time.Duration{0, -time.Second} {
		got := WithBudget(inner, budget)
		if got != Provider(inner) {
			t.Errorf("WithBudget(p, %s) = %T (%p), want the same pointer %p", budget, got, got, inner)
		}
	}
}

// TestWithBudget_HangingProviderLeavesParentBudgetIntact is the point of the
// whole work item: the wrapped call must give up on its own budget while the
// caller's request context is still alive, so the fallback that runs next still
// has a usable deadline to query the database with.
func TestWithBudget_HangingProviderLeavesParentBudgetIntact(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	p := WithBudget(&hangingProvider{}, 100*time.Millisecond)

	start := time.Now()
	_, err := p.Embed(parent, "x")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Embed returned nil error from a hanging provider")
	}
	if elapsed > 2*time.Second {
		t.Errorf("Embed took %s — the budget did not bound the call", elapsed)
	}
	// The assertion this file exists for.
	if parent.Err() != nil {
		t.Errorf("parent context died with the embedding call (%v) — the request budget was consumed, "+
			"so every fallback after this point would fail on a dead ctx", parent.Err())
	}
}

// TestWithBudget_PingHonoursTheCallersDeadlineNotTheBudget
//
// 🔴 Ping must NOT be re-bounded by the wrapper, and this test pins the
// direction. context.WithTimeout always takes the EARLIER deadline, so a
// wrapper that clamps Ping can only ever shrink what its caller asked for —
// silently, and downward.
//
// The caller that matters is cmd/aihub/main.go's boot readiness check: it
// allows 10s because a cold self-hosted backend can spend seconds loading a
// model on its first embed, and its response to a failed ping is to replace the
// provider with a NoopProvider for the entire process lifetime, with no retry.
// Clamped to DefaultBudget's 5s, a backend that needed 6s to warm up would boot
// with embedding permanently off — and /v1/health would then have to be told
// about it separately (domain.NoteEmbeddingUnavailableAtBoot) instead of the
// situation simply not arising. Found in review of aihub#316.
//
// Every Ping caller sets its own deadline: 10s at boot, 2s in the /v1/health
// probe, its own in cmd/aihub-embed-backfill. Embed has no such caller, which
// is why Embed IS bounded.
func TestWithBudget_PingHonoursTheCallersDeadlineNotTheBudget(t *testing.T) {
	// Caller allows 600ms; the wrapper's budget is a much smaller 50ms. If the
	// wrapper re-bounds, the call comes back at ~50ms.
	parent, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := WithBudget(&hangingProvider{}, 50*time.Millisecond).Ping(parent)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Ping returned nil error from a hanging provider")
	}
	// 300ms is a literal midpoint, not a value derived from either duration
	// above: a fixture computed from the budget under test would follow it.
	if elapsed < 300*time.Millisecond {
		t.Errorf("Ping returned after %s — the wrapper clamped the caller's deadline "+
			"down to its own budget, which is what shrank boot readiness from 10s to 5s", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("Ping took %s — it outlived even the caller's own deadline", elapsed)
	}
}

// TestWithBudget_EmbedBatchScalesWithTextCount: a batch of n is n sequential
// Embed calls in both real providers, so one per-call budget would strangle it.
// The hanging provider returns only when the derived ctx expires, so the
// elapsed time reads back the budget the wrapper actually computed.
func TestWithBudget_EmbedBatchScalesWithTextCount(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	p := WithBudget(&hangingProvider{}, 200*time.Millisecond)

	start := time.Now()
	if _, err := p.EmbedBatch(parent, []string{"a", "b", "c"}); err == nil {
		t.Fatal("EmbedBatch returned nil error from a hanging provider")
	}
	elapsed := time.Since(start)

	if elapsed < 500*time.Millisecond {
		t.Errorf("EmbedBatch of 3 gave up after %s — the budget did not scale with the batch", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("EmbedBatch took %s — the scaled budget did not bound the call", elapsed)
	}
	if parent.Err() != nil {
		t.Errorf("parent context died with the batch: %v", parent.Err())
	}
}

// TestWithBudget_EmptyBatchStillGetsABudget: len(texts)==0 would otherwise
// compute a zero-duration timeout, i.e. a context that is dead on arrival.
//
// The parent carries a deadline of its own so that a REGRESSION (an unbounded
// wrapper) fails on the upper bound instead of blocking this test forever.
func TestWithBudget_EmptyBatchStillGetsABudget(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	p := WithBudget(&hangingProvider{}, 150*time.Millisecond)
	start := time.Now()
	if _, err := p.EmbedBatch(parent, nil); err == nil {
		t.Fatal("EmbedBatch returned nil error from a hanging provider")
	}
	elapsed := time.Since(start)
	if elapsed < 100*time.Millisecond {
		t.Errorf("empty batch expired after %s — it was given a zero budget", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("empty batch took %s — the call was not bounded at all", elapsed)
	}
}

// TestWithBudget_DelegatesModelIDAndDims: the wrapper must be transparent for
// the two values that get written into emb_model / emb_dims.
func TestWithBudget_DelegatesModelIDAndDims(t *testing.T) {
	inner := &hangingProvider{}
	p := WithBudget(inner, time.Second)
	if got := p.ModelID(); got != inner.ModelID() {
		t.Errorf("ModelID() = %q, want %q", got, inner.ModelID())
	}
	if got := p.Dims(); got != inner.Dims() {
		t.Errorf("Dims() = %d, want %d", got, inner.Dims())
	}
}

// TestDefaultBudgetValue pins the constant against a literal rather than
// against itself, so a change to the default is a change to this test.
// The number matters: it has to stay far below the 30s request timeout in
// server/middleware.go, since the difference is the time a fallback has left.
func TestDefaultBudgetValue(t *testing.T) {
	if DefaultBudget != 5*time.Second {
		t.Errorf("DefaultBudget = %s, want 5s", DefaultBudget)
	}
}
