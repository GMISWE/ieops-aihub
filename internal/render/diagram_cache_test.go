package render

import (
	"fmt"
	"strings"
	"testing"
)

func TestRenderDiagramCacheHit(t *testing.T) {
	base := diagramCacheMisses.Load()

	svg1, err1 := RenderDiagram("a -> b")
	if err1 != nil {
		t.Fatalf("first RenderDiagram: %v", err1)
	}
	svg2, err2 := RenderDiagram("a -> b")
	if err2 != nil {
		t.Fatalf("second RenderDiagram: %v", err2)
	}

	if got := diagramCacheMisses.Load() - base; got != 1 {
		t.Fatalf("expected exactly 1 cache miss, got %d", got)
	}
	if svg1 != svg2 {
		t.Fatalf("cached svg must be byte-identical to the first render")
	}
	if !strings.Contains(svg1, "<svg") {
		t.Fatalf("expected an <svg> element, got %d bytes", len(svg1))
	}
}

func TestRenderDiagramCacheFailure(t *testing.T) {
	base := diagramCacheMisses.Load()

	const bad = ">>> not valid d2 <<<"
	_, err1 := RenderDiagram(bad)
	if err1 == nil {
		t.Fatalf("first RenderDiagram(bad): expected error, got nil")
	}
	_, err2 := RenderDiagram(bad)
	if err2 == nil {
		t.Fatalf("second RenderDiagram(bad): expected error, got nil")
	}

	// Failure is cached: only the first call should be a miss.
	if got := diagramCacheMisses.Load() - base; got != 1 {
		t.Fatalf("expected exactly 1 cache miss for a failing src, got %d", got)
	}
}

func TestRenderDiagramCacheBounded(t *testing.T) {
	// Exercise the eviction policy directly via diagramCachePut — no need to run
	// the (expensive) d2 compiler cap+ times just to verify the cache stays bounded.
	for i := 0; i < diagramCacheCap+50; i++ {
		diagramCachePut(fmt.Sprintf("k%d", i), diagramEntry{ok: false})
	}

	diagramCache.mu.RLock()
	n := len(diagramCache.m)
	diagramCache.mu.RUnlock()

	if n > diagramCacheCap {
		t.Fatalf("cache exceeded cap: len=%d cap=%d", n, diagramCacheCap)
	}
	// Confirm a flush actually happened (not that we merely stopped writing at
	// cap): after inserting cap+50 distinct keys the post-flush map holds only
	// the overflow remainder, well under cap.
	if n >= diagramCacheCap {
		t.Fatalf("expected a flush to shrink the cache below cap, got len=%d", n)
	}
}
