package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGetWorkItem_PathEscapesSlug guards against the slug-# URL-fragment
// regression: a human slug like "aihub#27" interpolated into the request URL
// with bare string concatenation has its "#27" parsed by net/url as a URL
// fragment and stripped client-side, so the server only ever receives
// "/v1/work_items/aihub" and returns 404. seg()/url.PathEscape must turn the
// '#' into "%23" so the full slug survives the round-trip; id-based callers
// (wi_xxx) must be unaffected. Mirrors internal/server/ui_embed_test.go's
// TestUIFuncMap_Wiref ("fails if a raw '#' leaks").
func TestGetWorkItem_PathEscapesSlug(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		wantRaw string // r.URL.EscapedPath(): what actually went over the wire
		wantDec string // r.URL.Path: server-side decoded path
	}{
		{
			name:    "slug_with_hash",
			id:      "aihub#27",
			wantRaw: "/v1/work_items/aihub%2327",
			wantDec: "/v1/work_items/aihub#27",
		},
		{
			name:    "plain_wi_id_unchanged",
			id:      "wi_abc",
			wantRaw: "/v1/work_items/wi_abc",
			wantDec: "/v1/work_items/wi_abc",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotRaw, gotDec string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotRaw = r.URL.EscapedPath()
				gotDec = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"wi_abc"}`))
			}))
			defer srv.Close()

			c := New(srv.URL, "test-key")
			if _, err := c.GetWorkItem(context.Background(), tc.id); err != nil {
				t.Fatalf("GetWorkItem(%q) returned error: %v", tc.id, err)
			}

			if gotRaw != tc.wantRaw {
				t.Errorf("EscapedPath = %q; want %q", gotRaw, tc.wantRaw)
			}
			if gotDec != tc.wantDec {
				t.Errorf("URL.Path = %q; want %q", gotDec, tc.wantDec)
			}
			// The '#' must have been escaped on the wire: if a raw '#' leaked,
			// it would never reach the server as part of the path at all.
			if tc.id == "aihub#27" && gotRaw == "/v1/work_items/aihub" {
				t.Errorf("server saw truncated path %q — the '#' was treated as a URL fragment and stripped", gotRaw)
			}
		})
	}
}
