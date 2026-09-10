package mcp_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// aihub#586 end to end: the whole journey the card could never cite in one
// harness — a single pf_remember call with `visibility: public` and an
// UNPUBLISHED `rendered_html`, against Postgres, the real echo router, the real
// pkg/client and the real MCP session, ending at an ANONYMOUS GET /share/:id.
//
// Before the aihub#586 strip this exact call stored the caller's HTML verbatim
// (RememberRequest binds `rendered_html`; resolveRenderedHTML precedence #1
// stores an explicit value for any type) and /share served it with no auth —
// handleSharedArtifact gates on `public` and hasRenderableBody, and a stored
// rendered_html satisfies the second conjunct. The wire-level halves are held
// without a database by remember_wire_shape_test.go and
// wire_strip_family_test.go; what only this file can hold is the two SURFACES:
// the row (rendered_html IS NULL) and the route (the payload is not served).
//
// ─── The REST control is the fix's whole claim, so it is not optional ───────
//
// The ruling scopes the strip to the TOOL boundary: "internal/server-side
// construction of rendered_html untouched". pf_save_artifact's published `html`
// lands on the same struct field through the same endpoint, so a fix that
// stopped the server storing explicit HTML would close aihub#586 by breaking a
// published channel. The control drives the SAME payload through the REST
// client — an authenticated project writer, which is what pkg/client is here —
// and requires it stored AND served, pinning the differential: one payload,
// two doors, and only the unpublished door is closed.
//
// DB-gated in the AIHUB_TEST_DB style of internal/domain's integration tests;
// the DB must already be migrated.
//
//	AIHUB_TEST_DB='postgres://postgres:test@127.0.0.1:5433/aihub_test?sslmode=disable' \
//	  go test ./internal/mcp/ -run TestE2ERememberStripsRenderedHTMLFromTheShareSurface -count=1 -v
//
// MUTANTS:
//
//	M22 enforcement: delete pf_remember from wireStrippedTools
//	                                          RED  the row arm (rendered_html
//	                                               stored) and the /share arm
//	                                               (payload served anonymously)
//	M23 enforcement: make the strip keep rendered_html specifically
//	                                          RED  same two arms
//	M24 harness: break the REST control's payload (send none)
//	                                          RED  the control arm — which is what
//	                                               proves the two positive arms
//	                                               are not green because /share
//	                                               stopped serving anything at all
//	M25 publication: delete the citation from the card's CLOSED paragraph
//	                                          RED  K12
func TestE2ERememberStripsRenderedHTMLFromTheShareSurface(t *testing.T) {
	s := newE2EStack(t)
	ctx := context.Background()

	const (
		attackMarker  = "AIHUB586-ATTACK-MARKER"
		controlMarker = "AIHUB586-CONTROL-MARKER"
	)
	attackHTML := "<!doctype html><html><body>" + attackMarker + "</body></html>"
	controlHTML := "<!doctype html><html><body>" + controlMarker + "</body></html>"

	// A previous run's rows carry no work_item_id, so newE2EStack's per-wi
	// cleanup never removes them; clear them here or the strict-dedup default
	// answers 409 to this run's fixture (the same reason the recall top_k case
	// clears first).
	if _, err := s.pool.Exec(ctx, `DELETE FROM memories WHERE project=$1`, s.project); err != nil {
		t.Fatalf("clean project memories: %v", err)
	}

	anonymousGet := func(memID string) (int, string) {
		t.Helper()
		resp, err := http.Get(s.baseURL + "/share/" + memID)
		if err != nil {
			t.Fatalf("anonymous GET /share/%s: %v", memID, err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read /share/%s body: %v", memID, err)
		}
		return resp.StatusCode, string(b)
	}

	// ── the attack, through the tool ─────────────────────────────────────────
	text, created := s.call(t, "pf_remember", map[string]any{
		"project":       s.project,
		"type":          "experience.debug",
		"content":       "an ordinary memory body used as the aihub#586 e2e fixture",
		"visibility":    "public",
		"tags":          []any{"aihub586-tag"},
		"rendered_html": attackHTML,
	})
	memID, _ := created["id"].(string)
	if memID == "" {
		t.Fatalf("pf_remember returned no id: %s", text)
	}

	// The disclosure names the stripped key on the same response.
	entries, _ := created["request_adjusted"].([]any)
	namedIt := false
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		if entry["param"] != "unknown_params" {
			continue
		}
		requested, _ := entry["requested"].([]any)
		for _, name := range requested {
			if name == "rendered_html" {
				namedIt = true
			}
		}
	}
	if !namedIt {
		t.Errorf("the response's request_adjusted does not name rendered_html — strip AND "+
			"report is the ruling, and this is the report half missing on a live server: %s", text)
	}

	// The row: nothing stored under the column the attack aimed at, while a
	// published sibling from the same call landed byte-identically — so the
	// absence is a fact about the strip, not about a write that failed.
	var stored *string
	var tags []string
	if err := s.pool.QueryRow(ctx,
		`SELECT rendered_html, tags FROM memories WHERE id=$1`, memID).Scan(&stored, &tags); err != nil {
		t.Fatalf("read the row back: %v", err)
	}
	if stored != nil {
		t.Errorf("memories.rendered_html holds %d bytes for the attack row — the unpublished "+
			"key reached storage through the tool", len(*stored))
	}
	if len(tags) != 1 || tags[0] != "aihub586-tag" {
		t.Errorf("published `tags` did not survive the same call (%v) — the strip is taking "+
			"more than the unpublished keys", tags)
	}

	// The route, anonymously. The status is deliberately not pinned — 404 today,
	// but a future lazy-render branch could legitimately serve SOMETHING for this
	// row. What no future may do is serve the attacker's bytes.
	status, page := anonymousGet(memID)
	if strings.Contains(page, attackMarker) {
		t.Errorf("GET /share/%s (status %d, no auth) serves the attack payload — the aihub#586 "+
			"exposure is live end to end", memID, status)
	}

	// ── the control, through REST: same payload, the published door ─────────
	controlResp, err := s.client.Remember(ctx, map[string]any{
		"project":       s.project,
		"type":          "experience.debug",
		"content":       "the aihub#586 REST control row",
		"visibility":    "public",
		"rendered_html": controlHTML,
	})
	if err != nil {
		t.Fatalf("REST control remember: %v", err)
	}
	controlID, _ := controlResp["id"].(string)
	if controlID == "" {
		t.Fatalf("REST control returned no id: %v", controlResp)
	}
	var controlStored *string
	if err := s.pool.QueryRow(ctx,
		`SELECT rendered_html FROM memories WHERE id=$1`, controlID).Scan(&controlStored); err != nil {
		t.Fatalf("read the control row back: %v", err)
	}
	if controlStored == nil || *controlStored != controlHTML {
		t.Fatalf("the REST door no longer stores explicit rendered_html — the fix has moved "+
			"off the tool boundary and broken pf_save_artifact's published `html` channel; "+
			"stored=%v", controlStored != nil)
	}
	controlStatus, controlPage := anonymousGet(controlID)
	if controlStatus != http.StatusOK || !strings.Contains(controlPage, controlMarker) {
		t.Fatalf("GET /share/%s answered %d without the control payload — /share is not "+
			"serving stored HTML at all, so the attack arm's silence above proves nothing",
			controlID, controlStatus)
	}
}
