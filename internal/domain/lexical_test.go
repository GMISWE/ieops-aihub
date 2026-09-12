package domain

// Unit coverage for the aihub#360 lexical helpers — the pure functions both
// sections share. The retrieval itself (predicate against a live schema, the
// two attach points, the vector-miss/lexical-hit anchor) is DB-gated:
// recall_lexical_db_test.go and wi_lexical_db_test.go.

import (
	"strings"
	"testing"
)

func TestLexicalTokens(t *testing.T) {
	t.Run("dedupes_case_insensitively_and_sorts_longest_first", func(t *testing.T) {
		used, dropped := lexicalTokens("Beta beta alpha-longer x BETA")
		if dropped != 0 {
			t.Fatalf("dropped = %d, want 0", dropped)
		}
		want := []string{"alpha-longer", "Beta", "x"}
		if len(used) != len(want) {
			t.Fatalf("tokens = %v, want %v", used, want)
		}
		for i := range want {
			if used[i] != want[i] {
				t.Fatalf("tokens = %v, want %v (first spelling kept, longest first, stable)", used, want)
			}
		}
	})

	t.Run("caps_at_the_longest_tokens_and_discloses_the_drop", func(t *testing.T) {
		// 20 distinct tokens of strictly decreasing length: aaaaaaaaaaaaaaaaaaaa … a
		fields := make([]string, 0, 20)
		for i := 20; i >= 1; i-- {
			fields = append(fields, strings.Repeat("a", i)+"z") // +z keeps them distinct from prefixes
		}
		used, dropped := lexicalTokens(strings.Join(fields, " "))
		if len(used) != lexicalMaxTokens {
			t.Fatalf("len(used) = %d, want the cap %d", len(used), lexicalMaxTokens)
		}
		if dropped != 20-lexicalMaxTokens {
			t.Fatalf("dropped = %d, want %d", dropped, 20-lexicalMaxTokens)
		}
		// The cap must keep the LONGEST tokens — dropping only ever widens the
		// AND-predicate, which is what makes the cap unable to lose a target.
		if used[0] != fields[0] {
			t.Fatalf("used[0] = %q, want the longest token %q", used[0], fields[0])
		}
		for _, u := range used {
			if len(u) < len(fields[lexicalMaxTokens-1]) {
				t.Fatalf("kept token %q is shorter than the shortest that should survive the cap", u)
			}
		}
	})

	t.Run("whitespace_only_query_yields_no_tokens", func(t *testing.T) {
		used, dropped := lexicalTokens(" \t\n ")
		if len(used) != 0 || dropped != 0 {
			t.Fatalf("got %v/%d, want no tokens and no drop", used, dropped)
		}
	})
}

func TestLexicalPattern(t *testing.T) {
	cases := map[string]string{
		"plain":            "%plain%",
		"50%":              `%50\%%`,
		"already_held:":    `%already\_held:%`,
		`back\slash`:       `%back\\slash%`,
		"中文短语":             "%中文短语%",
		"mix_%_of\\_every": `%mix\_\%\_of\\\_every%`,
	}
	for in, want := range cases {
		if got := lexicalPattern(in); got != want {
			t.Errorf("lexicalPattern(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLexicalSnippet(t *testing.T) {
	t.Run("returns_the_line_holding_the_longest_token", func(t *testing.T) {
		content := "# heading\nalready_held: []\nbody line about locks"
		tokens, _ := lexicalTokens("already_held: []")
		got := lexicalSnippet(content, tokens)
		if got != "already_held: []" {
			t.Fatalf("snippet = %q, want the matched line", got)
		}
	})

	t.Run("keeps_the_match_visible_on_an_overlong_line", func(t *testing.T) {
		line := strings.Repeat("x", 300) + " NEEDLE-360 " + strings.Repeat("y", 50)
		got := lexicalSnippet("first\n"+line+"\ntail", []string{"NEEDLE-360"})
		if !strings.Contains(got, "NEEDLE-360") {
			t.Fatalf("snippet lost the match on a long line: %q", got)
		}
		if n := len([]rune(got)); n > lexicalSnippetMaxRunes {
			t.Fatalf("snippet is %d runes, cap is %d", n, lexicalSnippetMaxRunes)
		}
	})

	t.Run("case_insensitive_match", func(t *testing.T) {
		got := lexicalSnippet("The Gateway Engine restarts", []string{"gateway engine"})
		if !strings.Contains(got, "Gateway Engine") {
			t.Fatalf("snippet = %q, want the original-case line", got)
		}
	})

	t.Run("falls_back_to_the_first_non_blank_line", func(t *testing.T) {
		// Reachable on the wi side: the SQL matches goal OR content per token,
		// so a hit's combined doc may not contain the anchor token at all.
		got := lexicalSnippet("\n\nreal first line\nmore", []string{"absent-token"})
		if got != "real first line" {
			t.Fatalf("fallback snippet = %q", got)
		}
	})
}
