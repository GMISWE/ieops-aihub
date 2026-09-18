package skillregistry

import (
	"strings"
	"testing"
)

func TestValidateBundlePathAcceptsNormalizedRelativePaths(t *testing.T) {
	for _, p := range []string{
		"SKILL.md",
		"grill-me/SKILL.md",
		"a/b/c/file.md",
		"scripts/run.sh",
		"notes.2026.md",
		"dots.ok.md",
		"sk-中文.md", // non-ASCII is fine: it is the control and separator
		// characters that are dangerous, not the letters.
	} {
		if err := ValidateBundlePath(p); err != nil {
			t.Errorf("ValidateBundlePath(%q) = %v, want nil", p, err)
		}
	}
}

func TestValidateBundlePathRejectsHostileAndMalformedPaths(t *testing.T) {
	for _, p := range []string{
		"",                      // empty
		"/etc/passwd",           // absolute
		"~/.ssh/id_rsa",         // home-relative
		"C:\\windows\\system32", // drive + backslashes
		"C:whatever",            // drive prefix
		"a/../../etc/passwd",    // traversal
		"a/../b",                // traversal, benign-looking
		"../a",                  // leading parent
		"./a",                   // leading dot segment
		"a/./b",                 // interior dot segment
		"a//b",                  // empty segment
		"a/b/",                  // trailing slash (directory-looking)
		"a/..",                  // trailing parent
		"...",                   // only dots (root)
		"a/...",                 // only-dots segment
		".",                     // current dir
		"..",                    // parent dir
		"a\x00b",                // NUL
		"a\nb",                  // newline (control char)
		"a\rb",                  // carriage return
		"a\x7fb",                // DEL
		"back\\slash",           // backslash separator
		"/",                     // root
		"//double",              // leading empty segment
	} {
		if err := ValidateBundlePath(p); err == nil {
			t.Errorf("ValidateBundlePath(%q) = nil, want an error", p)
		}
	}
}

func TestValidateBundlePathRejectsOversizedPaths(t *testing.T) {
	tooLong := strings.Repeat("a", MaxPathBytes+1)
	if err := ValidateBundlePath(tooLong); err == nil {
		t.Errorf("a %d-byte path was accepted; the limit is %d", len(tooLong), MaxPathBytes)
	}
	longSeg := "a/" + strings.Repeat("b", MaxPathComponentBytes+1)
	if err := ValidateBundlePath(longSeg); err == nil {
		t.Errorf("a path with a %d-byte segment was accepted; the limit is %d",
			MaxPathComponentBytes+1, MaxPathComponentBytes)
	}
	// Invalid UTF-8 must be refused: the path is stored and compared as a
	// string, and PostgreSQL byte handling would disagree with Go's.
	if err := ValidateBundlePath("a/\xff\xfe.md"); err == nil {
		t.Error("invalid UTF-8 path was accepted")
	}
}
