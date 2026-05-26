package server

import (
	"strings"
	"testing"
)

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
