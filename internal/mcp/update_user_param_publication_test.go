package mcp_test

// aihub#587 (2026-09-10) — `author_aliases` is WITHDRAWN from pf_create_user and
// pf_update_user, and this file holds the withdrawal.
//
// ─── What this file used to hold, and why it flipped ──────────────────────
//
// It was aihub#426's arm: pf_update_user bound `author_aliases` without
// publishing it (the aihub#419 layer-1 gate's BOUND_FIELD_UNPUBLISHED), and the
// fix then was to PUBLISH the field, with the empty-array-clears spelling
// documented and the forwarding measured at the wire. Publishing a bound field
// was right on what was known then.
//
// What aihub#543 measured later (2026-09-10) is that the column the field fed
// has no reader: no SQL statement anywhere in internal/ or pkg/ selects
// users.author_aliases, and commit records take their author from the
// authenticated caller — the "aliases drive attribution" sentence both cards
// carried was never true of this tree. That put the field in §6.1 T1-9's shape
// (a published field nothing reads: withdraw, fix, or file), and the owner's
// aihub#587 ruling was to WITHDRAW: remove the published parameter, the
// bindings and all three write sites. The COLUMN stays — dropping it is a
// destructive migration and a separate decision — recorded as dormant, dated,
// in docs/mcp-cards/pf_create_user.md and pf_update_user.md.
//
// ─── What is asserted now ──────────────────────────────────────────────────
//
// A caller who has not heard about the withdrawal will keep sending the name,
// and the failure mode aihub#389 measured is exactly this shape: an argument no
// schema publishes crosses every hop with no error anywhere, and a caller
// believes aliases were stored that never were. So the arm here holds the
// DISCLOSURE: a call sending `author_aliases` gets it named in the response's
// `request_adjusted` under `unknown_params`, for both tools, on the same
// mechanism every tool shares ((*Server).addTool → unknown_params.go).
//
// "Nothing lands in the column" is held at the layer where landing happens:
//   - the tree-wide census — zero SQL write sites, zero reads —
//     is TestAuthorAliasesIsNeitherWrittenNorRead (user_admin_surface_test.go);
//   - handleCreateUser's INSERT column list not naming the column is
//     internal/server/user_admin_write_shape_test.go
//     (TestCreateUserResponseIsTheHandlersOwnProjection);
//   - handleUpdateUser treating a body that carries ONLY the withdrawn name as
//     "no fields to update" is the same file's
//     TestUpdateUserWritesTwoFieldsRefusesUserTypeAndDropsTheRest.
//
// ⚠️ The MCP handler still FORWARDS the key (pf_update_user copies its whole
// args map into the PATCH body; pf_create_user passes the map wholesale). That
// is deliberate phase-1 aihub#389 behaviour — report, do not strip — and it is
// why the third bullet above matters: the name arrives at the server and the
// server binds nothing to it, which the wire assertion below pins so the
// disclosure cannot silently become the only true half.
//
// No database needed:
//
//	go test ./internal/mcp/ -run TestAuthorAliasesWithdrawal -v
//
// MUTANTS (aihub#587, 2026-09-10 — each applied, run RED, reverted, with
// `git diff --stat` checked non-empty before each run):
//
//	W7 publication: republish author_aliases on pf_update_user's schema
//	                                          RED  the_echo_names_it (the key is
//	                                               then known, so no disclosure
//	                                               comes back) — and
//	                                               neither_tool_publishes_it in
//	                                               user_admin_surface_test.go
//	W8 enforcement: strip author_aliases from the forwarded body instead of
//	     forwarding it                        RED  the_name_still_reaches_the_wire
//	     (pf_update_user)                          — phase 2 (reject/strip) is a
//	                                               separate, licensed change, not
//	                                               a side effect of this one

import (
	"encoding/json"
	"strings"
	"testing"
)

// withdrawnUnknownParams pulls the `requested` list of the unknown_params entry
// out of a decoded tool response, or nil when no such entry exists.
func withdrawnUnknownParams(t *testing.T, decoded map[string]any) []string {
	t.Helper()
	raw, err := json.Marshal(decoded["request_adjusted"])
	if err != nil {
		t.Fatalf("re-marshal request_adjusted: %v", err)
	}
	var entries []struct {
		Param     string   `json:"param"`
		Requested []string `json:"requested"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil
	}
	for _, e := range entries {
		if e.Param == "unknown_params" {
			return e.Requested
		}
	}
	return nil
}

// TestAuthorAliasesWithdrawalIsDisclosed drives both tools with the withdrawn
// name and requires the aihub#389 echo to name it — the difference between a
// withdrawal and a silent drop.
func TestAuthorAliasesWithdrawalIsDisclosed(t *testing.T) {
	const withdrawn = "author_aliases"

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"pf_create_user", map[string]any{
			"display_name": "probe 587",
			"user_type":    "machine",
			withdrawn:      []any{"probe587@example.com"},
		}},
		{"pf_update_user", map[string]any{
			"id":           "u_probe587",
			"display_name": "probe 587, renamed",
			withdrawn:      []any{"probe587@example.com"},
		}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			f := newFakeAihub(t)
			decoded, isErr := callTool(t, f, tc.tool, tc.args)
			if isErr {
				t.Fatalf("%s answered an error for a call whose only defect is one withdrawn "+
					"argument: %v\naihub#389 phase 1 is report-not-reject, and flipping to "+
					"rejection is a separate licensed change.", tc.tool, decoded)
			}

			t.Run("the_echo_names_it", func(t *testing.T) {
				requested := withdrawnUnknownParams(t, decoded)
				if len(requested) == 0 {
					t.Fatalf("%s's response carries no request_adjusted.unknown_params entry "+
						"(response: %v).\nThe parameter was withdrawn by aihub#587, so a call "+
						"sending it must be TOLD so — a caller who set aliases and got a clean "+
						"200 believes a mapping was stored that no column write ever received. "+
						"Either the withdrawal regressed (the schema publishes the name again) "+
						"or the aihub#389 disclosure stopped covering this tool.", tc.tool, decoded)
				}
				found := false
				for _, name := range requested {
					if name == withdrawn {
						found = true
					}
				}
				if !found {
					t.Errorf("%s disclosed unknown params %v, and %q is not among them",
						tc.tool, requested, withdrawn)
				}
			})

			t.Run("the_name_still_reaches_the_wire", func(t *testing.T) {
				calls := f.recorded()
				if len(calls) != 1 {
					t.Fatalf("expected exactly one HTTP call, got %d", len(calls))
				}
				if _, present := calls[0].Body[withdrawn]; !present {
					t.Errorf("%s no longer forwards %q in the body (%v). Phase 1 of aihub#389 "+
						"is report-not-strip: the disclosure names the key while the request "+
						"still crosses the wire unchanged, and the far end binds nothing to it "+
						"(internal/server/user_admin_write_shape_test.go holds that half). "+
						"Stripping is phase 2, a separately licensed change — if it has been "+
						"licensed, update this arm and the cards together.", tc.tool, withdrawn, calls[0].Body)
				}
			})
		})
	}
}

// TestWithdrawnAliasArgAloneIsRefusedNotSilentlyHonoured pins the sharpest edge
// of the withdrawal at this layer: a pf_update_user call whose ONLY payload is
// the withdrawn name. Before aihub#587 that call REPLACED the alias list; now
// the server binds none of it, finds no fields to update, and refuses — so the
// caller gets a 400 AND the disclosure, rather than a success for a write that
// did not happen.
func TestWithdrawnAliasArgAloneIsRefusedNotSilentlyHonoured(t *testing.T) {
	f := newFakeAihub(t)
	// The fake answers what the real handler answers for an empty SET list —
	// measured against handleUpdateUser's `len(sets) == 0` refusal — so this
	// test documents the real end-to-end verdict without a database.
	f.on("/v1/admin/users/u_probe587", func(body map[string]any) (int, any) {
		if _, present := body["display_name"]; present {
			return 200, map[string]any{"ok": true}
		}
		// Top-level {code, message}: the shape errorResponse in
		// internal/server/middleware.go actually writes.
		return 400, map[string]any{"code": "BAD_REQUEST", "message": "no fields to update"}
	})
	decoded, isErr := callTool(t, f, "pf_update_user", map[string]any{
		"id":             "u_probe587",
		"author_aliases": []any{"probe587@example.com"},
	})
	if !isErr {
		t.Fatalf("an update whose only argument is withdrawn answered success: %v\nEvery "+
			"bound field is absent, so the server refuses with \"no fields to update\"; a "+
			"success here would be the exact silent-drop aihub#389 exists to prevent.", decoded)
	}
	raw, _ := decoded["_raw"].(string)
	if !strings.Contains(raw, "no fields") {
		t.Errorf("the error text does not carry the server's refusal: %q", raw)
	}
}
