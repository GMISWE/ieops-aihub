package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubFetcher implements artifactFetcher for tests.
type stubFetcher struct {
	html string
	err  error
}

func (s *stubFetcher) GetArtifactHTML(ctx context.Context, memID string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.html, nil
}

// TestRunArtifactView_EmptyID asserts the empty-ID guard returns an error and
// never calls the fetcher.
func TestRunArtifactView_EmptyID(t *testing.T) {
	called := false
	f := &stubFetcher{html: "x"}
	wrap := stubFetcherWrap{f, &called}
	err := RunArtifactView(context.Background(), wrap, "")
	if err == nil {
		t.Fatal("expected error for empty memory_id")
	}
	if called {
		t.Fatal("fetcher must not be called for empty id")
	}
}

// TestRunArtifactView_NilClient asserts the nil-client guard fires before any
// filesystem mutation.
func TestRunArtifactView_NilClient(t *testing.T) {
	err := RunArtifactView(context.Background(), nil, "mem_x")
	if err == nil {
		t.Fatal("expected error when client is nil")
	}
}

// TestRunArtifactView_ClientError surfaces the underlying client error.
func TestRunArtifactView_ClientError(t *testing.T) {
	f := &stubFetcher{err: errors.New("boom")}
	err := RunArtifactView(context.Background(), f, "mem_x")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected error wrapping 'boom', got %v", err)
	}
}

// TestRunArtifactView_WritesFile confirms the HTML body is written to the
// expected path and that the opener path is suppressed when no opener is
// available (lookPathFn returns an error).
func TestRunArtifactView_WritesFile(t *testing.T) {
	memID := "mem_writetest_42"
	html := "<html><body>hello</body></html>"

	// Force lookPathFn to fail so the test does not actually launch a browser.
	origLook := lookPathFn
	origOpen := openerFn
	defer func() {
		lookPathFn = origLook
		openerFn = origOpen
	}()
	lookPathFn = func(file string) (string, error) {
		return "", errors.New("not installed in test")
	}
	openerCalled := false
	openerFn = func(opener, path string) error {
		openerCalled = true
		return nil
	}

	expected := filepath.Join(os.TempDir(), "polyforge", "artifact-"+memID+".html")
	_ = os.Remove(expected) // ensure clean slate
	t.Cleanup(func() { _ = os.Remove(expected) })

	f := &stubFetcher{html: html}
	if err := RunArtifactView(context.Background(), f, memID); err != nil {
		t.Fatalf("RunArtifactView: %v", err)
	}
	if openerCalled {
		t.Fatalf("opener should not be invoked when lookPath fails")
	}
	got, err := os.ReadFile(expected)
	if err != nil {
		t.Fatalf("output file missing: %v", err)
	}
	if string(got) != html {
		t.Fatalf("written body mismatch: got %q, want %q", string(got), html)
	}
}

// stubFetcherWrap is a tiny shim that records whether GetArtifactHTML was
// invoked — used by the empty-ID guard test.
type stubFetcherWrap struct {
	inner  *stubFetcher
	called *bool
}

func (w stubFetcherWrap) GetArtifactHTML(ctx context.Context, memID string) (string, error) {
	*w.called = true
	return w.inner.GetArtifactHTML(ctx, memID)
}
