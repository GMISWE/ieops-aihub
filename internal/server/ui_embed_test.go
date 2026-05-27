package server

import (
	"strings"
	"testing"
)

// TestUIFuncMap_Truncate guards against the mid-rune byte-cut regression:
// the old s[:n] + "..." implementation would slice CJK strings in the
// middle of a multi-byte UTF-8 sequence, producing replacement-char
// garbage in wi list / detail / memories / event timeline / queue
// section views. truncate must count runes (user-visible chars), not
// bytes, so n is a display budget.
func TestUIFuncMap_Truncate(t *testing.T) {
	funcs := uiFuncMap()
	truncate, ok := funcs["truncate"].(func(int, string) string)
	if !ok {
		t.Fatalf("truncate not registered in uiFuncMap or wrong signature")
	}

	cases := []struct {
		name string
		n    int
		in   string
		want string
	}{
		{"empty", 10, "", ""},
		{"ascii_short", 10, "hello", "hello"},
		{"ascii_at_budget", 5, "hello", "hello"},
		{"ascii_over", 5, "hello world", "hello..."},
		{"cjk_short", 10, "你好", "你好"},
		{"cjk_at_budget", 2, "你好", "你好"},
		{"cjk_over", 2, "你好世界", "你好..."},
		// Byte 4 of "AB你CD" lands on the 2nd byte of 你 (3-byte rune),
		// so the old s[:4] would emit a replacement char. Rune-based
		// truncation must cleanly yield the first 4 runes + ellipsis.
		{"cjk_boundary_byte", 4, "AB你CD", "AB你C..."},
		{"mixed", 4, "ab你好cd", "ab你好..."},
		{"n_zero", 0, "anything", "anything"},
		{"n_negative", -1, "anything", "anything"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.n, tc.in)
			if got != tc.want {
				t.Errorf("truncate(%d, %q) = %q; want %q", tc.n, tc.in, got, tc.want)
			}
			if strings.ContainsRune(got, '�') {
				t.Errorf("truncate(%d, %q) = %q contains U+FFFD replacement char — mid-rune cut", tc.n, tc.in, got)
			}
		})
	}
}

// TestUIFuncMap_Wiref guards against the slug-# fragment regression: a wi
// slug like "aihub#1" embedded as raw text in an href would let the browser
// treat "#1" as a URL fragment and strip it before the request, so the
// handler would only ever see "aihub" and return 404. wiref must PathEscape
// the # to %23 so the full slug survives the round-trip.
func TestUIFuncMap_Wiref(t *testing.T) {
	funcs := uiFuncMap()
	wiref, ok := funcs["wiref"].(func(string) string)
	if !ok {
		t.Fatalf("wiref not registered in uiFuncMap or wrong signature")
	}

	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"aihub#1", "/ui/wi/aihub%231"},
		{"aihub#59", "/ui/wi/aihub%2359"},
		{"wi_u3WPMDeB", "/ui/wi/wi_u3WPMDeB"},
		{"proj-with-dash#12", "/ui/wi/proj-with-dash%2312"},
	}
	for _, tc := range cases {
		got := wiref(tc.in)
		if got != tc.want {
			t.Errorf("wiref(%q) = %q; want %q", tc.in, got, tc.want)
		}
		if strings.Contains(tc.in, "#") && strings.Contains(got, "#") {
			t.Errorf("wiref(%q) returned %q which still contains a raw '#' — browser will strip it as URL fragment", tc.in, got)
		}
	}
}
