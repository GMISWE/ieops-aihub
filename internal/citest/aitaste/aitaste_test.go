package aitaste

import (
	"os"
	"path/filepath"
	"testing"
)

// repoRoot walks up from the test's working directory to the module root,
// identified by go.mod. Same shape as internal/citest/rowserr: a marker file
// rather than a fixed number of "..", so moving this package does not silently
// point the gate at a subtree.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find go.mod above the test's working directory; this gate cannot have run")
	return ""
}

// TestUserVisibleCopyBanList is the aihub#378 gate. It scans the product-copy
// surfaces for the markers aihub#373 measured at ~zero in human-written text
// of the same genre, and fails on any occurrence no per-occurrence directive
// covers. See the package comment for the surface list, the two TEMPORARY
// surface exclusions (internal/mcp until aihub#626; plugins/** until its
// release-batched work item is filed and lands), and the exemption grammar.
func TestUserVisibleCopyBanList(t *testing.T) {
	root := repoRoot(t)
	res, err := ScanRepo(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}

	for _, v := range res.Violations {
		t.Errorf("banned marker in user-visible copy at %s\n"+
			"    aihub#373 measured this class at ~zero in human-written text of the same genre (aihub#378).\n"+
			"    Fix: rewrite the sentence the way a person types it (comma, semicolon, colon, parentheses,\n"+
			"    or an ASCII arrow, whichever the sentence needs).\n"+
			"    Only if this exact character is load-bearing (a glyph, not prose), exempt THIS occurrence\n"+
			"    with a comment on its line: aitaste:allow U+%04X: <why this glyph is deliberate>",
			v, v.Rune)
	}

	// A directive that covers nothing is deleted, not left lying around: it is
	// an exemption for text that no longer exists, and the next banned rune to
	// land on that line would inherit it silently.
	for _, d := range res.StaleDirectives {
		t.Errorf("stale exemption at %s\n    it covers no banned rune on its line; delete the directive", d)
	}

	// The exemption count is printed on every run so the number is visible
	// when it only ever goes up (aihub#378: the escape hatch must stay more
	// expensive than compliance).
	t.Logf("exemptions in effect: %d", len(res.Exemptions))
	for _, d := range res.Exemptions {
		t.Logf("    %s", d)
	}
	for _, ex := range TemporarySurfaceExclusions {
		t.Logf("temporary surface exclusion: %s", ex)
	}

	// Encoding-channel control (aihub#373 F3): that measurement once reported
	// a false "clean" because an encoding round-trip destroyed every
	// non-ASCII character before the count. The scanned surfaces are known to
	// carry non-ASCII copy (Chinese UI notices in internal/server, CJK text
	// in the templates), so a scan that saw none of it did not read the
	// files' actual bytes.
	if res.NonASCIIRunes == 0 || res.CJKRunes == 0 {
		t.Errorf("encoding-channel control failed: the scan saw %d non-ASCII runes (%d CJK); "+
			"the surfaces are known to contain CJK copy, so a zero here means the scanner "+
			"is not reading UTF-8 faithfully, not that the copy is clean", res.NonASCIIRunes, res.CJKRunes)
	}
}

// scanFixture writes src as a Go file in a temp dir and scans it.
func scanFixture(t *testing.T, src string) Result {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	fr, err := ScanGoFile(path, "fixture.go")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return Check([]FileResult{fr})
}

// TestInjectedMarkersTurnTheGateRed is the negative control the wi hard-codes
// (aihub#378 constraint 1 and 2): a marker injected into a scanned string
// must be found, in every literal form Go offers. Without this, "zero
// violations" and "the scanner did not run" print the same thing.
func TestInjectedMarkersTurnTheGateRed(t *testing.T) {
	cases := map[string]struct {
		src  string
		want int
	}{
		"plain string": {
			src:  "package f\n\nvar x = \"deadline exhausted — retry\"\n",
			want: 1,
		},
		"sprintf format string": {
			src:  "package f\n\nimport \"fmt\"\n\nvar x = fmt.Sprintf(\"wrote %s — open it manually\", \"f\")\n",
			want: 1,
		},
		"raw string, marker on an interior line": {
			src:  "package f\n\nvar x = `first line\nsecond line — with a marker\nthird line`\n",
			want: 1,
		},
		"concatenated multi-line string": {
			src:  "package f\n\nvar x = \"first half \" +\n\t\"second half — with a marker\"\n",
			want: 1,
		},
		"escape-encoded em dash": {
			src:  "package f\n\nvar x = \"sneaky\\u2014dash\"\n",
			want: 1,
		},
		"unicode arrow": {
			src:  "package f\n\nvar x = \"a → b\"\n",
			want: 1,
		},
		"double arrow": {
			src:  "package f\n\nvar x = \"a ⇒ b\"\n",
			want: 1,
		},
		"checkmark and cross": {
			src:  "package f\n\nvar x = \"✅ done ❌ failed\"\n",
			want: 2,
		},
		"two em dashes on one line are two findings": {
			src:  "package f\n\nvar x = \"a — b — c\"\n",
			want: 2,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res := scanFixture(t, tc.src)
			if len(res.Violations) != tc.want {
				t.Errorf("want %d violations, got %d: %v", tc.want, len(res.Violations), res.Violations)
			}
		})
	}
}

// TestHumanPunctuationStaysGreen is the positive control (aihub#378
// constraint 3): the characters humans actually type must not be flagged.
// The ASCII hyphen and the en dash U+2013 are named by the wi; U+2015 and
// U+FF0D were measured at ~zero in ALL corpora including the agent side, so
// banning them would widen the class past its evidence; ASCII arrows and
// semicolons are human-written per the aihub#373 controls.
func TestHumanPunctuationStaysGreen(t *testing.T) {
	src := "package f\n\nvar x = []string{\n" +
		"\t\"an ASCII hyphen - stays\",\n" +
		"\t\"a range 2019–2021 with an en dash stays\",\n" +
		"\t\"a horizontal bar ― stays (U+2015)\",\n" +
		"\t\"a fullwidth minus - stays (U+FF0D): -\",\n" +
		"\t\"an ascii arrow a -> b stays; so does a => b\",\n" +
		"\t\"semicolons stay; humans use them, just less\",\n" +
		"\t\"一句普通的中文,带全角标点:(括号)、顿号,不该被误伤。\",\n" +
		"\t\"plain English with (parentheses), colons: and commas, stays\",\n" +
		"}\n"
	res := scanFixture(t, src)
	if len(res.Violations) != 0 {
		t.Errorf("positive control went red; these are human-written characters: %v", res.Violations)
	}
	// The same fixture doubles as the UTF-8 channel control (constraint 4):
	// the scanner must have SEEN the non-ASCII characters it declined to
	// flag. A zero here means they were destroyed before classification,
	// which is exactly the aihub#373 F3 bug shape.
	if res.CJKRunes == 0 {
		t.Errorf("scanner saw no CJK runes in a fixture that contains them; the read path is mangling UTF-8")
	}
}

// TestFullwidthMinusLiteral pins the U+FF0D non-ban with the real code point
// (the fixture above spells it inline where an editor might normalize it).
func TestFullwidthMinusLiteral(t *testing.T) {
	res := scanFixture(t, "package f\n\nvar x = \"a － b\"\n")
	if len(res.Violations) != 0 {
		t.Errorf("U+FF0D is not in the ban list (measured ~zero on the agent side too): %v", res.Violations)
	}
}

// TestDirectiveMechanics covers the exemption machinery: per-occurrence
// coverage, the count bound, the reason requirement, and stale detection.
func TestDirectiveMechanics(t *testing.T) {
	t.Run("same-line directive exempts exactly the named rune", func(t *testing.T) {
		res := scanFixture(t, "package f\n\nvar x = \"—\" // aitaste:allow U+2014: placeholder glyph\n")
		if len(res.Violations) != 0 || len(res.Exemptions) != 1 || len(res.StaleDirectives) != 0 {
			t.Errorf("want clean pass with 1 exemption, got violations=%v exemptions=%v stale=%v",
				res.Violations, res.Exemptions, res.StaleDirectives)
		}
	})
	t.Run("preceding-line directive covers the next line", func(t *testing.T) {
		res := scanFixture(t, "package f\n\n// aitaste:allow U+2014: placeholder glyph\nvar x = \"—\"\n")
		if len(res.Violations) != 0 || len(res.Exemptions) != 1 {
			t.Errorf("want clean pass with 1 exemption, got violations=%v exemptions=%v",
				res.Violations, res.Exemptions)
		}
	})
	t.Run("a second occurrence past the declared count stays red", func(t *testing.T) {
		res := scanFixture(t, "package f\n\nvar x = \"— and —\" // aitaste:allow U+2014: only one is deliberate\n")
		if len(res.Violations) != 1 {
			t.Errorf("the directive covers one occurrence; the second must stay red, got %v", res.Violations)
		}
	})
	t.Run("an explicit xN count covers N and no more", func(t *testing.T) {
		res := scanFixture(t, "package f\n\nvar x = \"— and —\" // aitaste:allow U+2014 x2: both are deliberate\n")
		if len(res.Violations) != 0 || len(res.Exemptions) != 1 {
			t.Errorf("x2 covers both, got violations=%v", res.Violations)
		}
		res = scanFixture(t, "package f\n\nvar x = \"— and — and —\" // aitaste:allow U+2014 x2: two only\n")
		if len(res.Violations) != 1 {
			t.Errorf("x2 covers two of three, got %v", res.Violations)
		}
	})
	t.Run("a directive for one rune does not cover another", func(t *testing.T) {
		res := scanFixture(t, "package f\n\nvar x = \"→\" // aitaste:allow U+2014: wrong code point\n")
		if len(res.Violations) != 1 || len(res.StaleDirectives) != 1 {
			t.Errorf("want 1 violation and 1 stale directive, got violations=%v stale=%v",
				res.Violations, res.StaleDirectives)
		}
	})
	t.Run("a stale directive is itself a failure", func(t *testing.T) {
		res := scanFixture(t, "package f\n\n// aitaste:allow U+2014: text was fixed but the directive stayed\nvar x = \"clean\"\n")
		if len(res.StaleDirectives) != 1 {
			t.Errorf("want the leftover directive reported stale, got %v", res.StaleDirectives)
		}
	})
	t.Run("a directive without a reason does not parse and fails the scan", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "fixture.go")
		src := "package f\n\nvar x = \"—\" // aitaste:allow U+2014:\n"
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ScanGoFile(path, "fixture.go"); err == nil {
			t.Errorf("a reasonless directive must fail loudly, not silently exempt nothing")
		}
	})
	// There is no file- or directory-level directive: the grammar has no
	// syntax for one, so the wrong shape is unwritable rather than reviewable.
}

// scanTemplateFixture writes src as a .tmpl file and scans it.
func scanTemplateFixture(t *testing.T, src string) Result {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.html.tmpl")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	fr, err := ScanTemplateFile(path, "fixture.html.tmpl")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return Check([]FileResult{fr})
}

// TestTemplateVisibleText covers the template surface: what a browser renders
// as text is scanned; markup, comments, actions and script/style are not.
func TestTemplateVisibleText(t *testing.T) {
	t.Run("visible marker is found", func(t *testing.T) {
		res := scanTemplateFixture(t, "<p>team knowledge — everything</p>\n")
		if len(res.Violations) != 1 {
			t.Errorf("want 1 violation, got %v", res.Violations)
		}
	})
	t.Run("entity-encoded marker is found", func(t *testing.T) {
		res := scanTemplateFixture(t, "<p>team knowledge &mdash; everything &#8212; twice</p>\n")
		if len(res.Violations) != 2 {
			t.Errorf("&mdash; and &#8212; both decode to U+2014, got %v", res.Violations)
		}
	})
	t.Run("template comment spanning lines with an inner action is masked", func(t *testing.T) {
		// The inner {{if .X}} used to make a non-greedy {{.*?}} stop early
		// and leak the comment tail into visible text.
		res := scanTemplateFixture(t, "{{/* a comment with {{if .X}} inside\nand a marker — on the next line */}}\n<p>clean</p>\n")
		if len(res.Violations) != 0 {
			t.Errorf("template comments are not user-visible, got %v", res.Violations)
		}
	})
	t.Run("html comment, action, attribute and script text are not visible", func(t *testing.T) {
		res := scanTemplateFixture(t,
			"<!-- a comment — with a marker -->\n"+
				"{{printf \"—\"}}\n"+
				"<a title=\"tooltip — attribute text is outside the aihub#373 surface\">x</a>\n"+
				"<script>var s = \"—\";</script>\n")
		if len(res.Violations) != 0 {
			t.Errorf("masked regions are not scanned, got %v", res.Violations)
		}
	})
	t.Run("same-line html-comment directive exempts a glyph", func(t *testing.T) {
		res := scanTemplateFixture(t, "<span>—</span><!-- aitaste:allow U+2014: placeholder glyph -->\n")
		if len(res.Violations) != 0 || len(res.Exemptions) != 1 {
			t.Errorf("want clean pass with 1 exemption, got violations=%v exemptions=%v",
				res.Violations, res.Exemptions)
		}
	})
	t.Run("star and arrow glyphs red without a directive", func(t *testing.T) {
		res := scanTemplateFixture(t, "<button>★ Watching</button>\n<a>更早 →</a>\n")
		if len(res.Violations) != 2 {
			t.Errorf("want 2 violations, got %v", res.Violations)
		}
	})
}
