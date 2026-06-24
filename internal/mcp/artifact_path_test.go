package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveArtifactContent(t *testing.T) {
	ws := t.TempDir()
	// real file inside ws
	docRel := "docs/superpowers/specs/x.md"
	docAbs := filepath.Join(ws, docRel)
	if err := os.MkdirAll(filepath.Dir(docAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(docAbs, []byte("# hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a file OUTSIDE ws, plus a symlink inside ws pointing to it
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	escLink := filepath.Join(ws, "escape.md")
	if err := os.Symlink(outside, escLink); err != nil {
		t.Fatal(err)
	}
	// oversized + non-utf8
	big := filepath.Join(ws, "big.md")
	if err := os.WriteFile(big, make([]byte, maxArtifactFileBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	binf := filepath.Join(ws, "bin.md")
	if err := os.WriteFile(binf, []byte{0xff, 0xfe, 0xfd}, 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		args   map[string]any
		want   string
		errSub string // non-empty => expect error containing this
	}{
		{"content only", map[string]any{"content": "inline"}, "inline", ""},
		{"path relative", map[string]any{"path": docRel}, "# hello\n", ""},
		{"path absolute", map[string]any{"path": docAbs}, "# hello\n", ""},
		{"both", map[string]any{"content": "x", "path": docRel}, "", "not both"},
		{"neither", map[string]any{}, "", "content or path required"},
		{"escape via ..", map[string]any{"path": "../" + filepath.Base(outside)}, "", "escapes workspace"},
		{"escape via symlink", map[string]any{"path": "escape.md"}, "", "escapes workspace"},
		{"too large", map[string]any{"path": "big.md"}, "", "too large"},
		{"not utf8", map[string]any{"path": "bin.md"}, "", "UTF-8"},
		{"missing", map[string]any{"path": "nope.md"}, "", "not found"},
		{"is dir", map[string]any{"path": "docs"}, "", "directory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveArtifactContent(tc.args, ws)
			if tc.errSub != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errSub) {
					t.Fatalf("want err containing %q, got %v", tc.errSub, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
