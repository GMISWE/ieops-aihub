package mcp_test

// aihub#426 — pf_update_user binds author_aliases and did not publish it, found
// by the aihub#419 layer-1 gate as BOUND_FIELD_UNPUBLISHED.
//
// ─── What was actually broken ─────────────────────────────────────────────
//
// Not the plumbing. handleUpdateUser has always bound the field (PATCH
// /v1/admin/users/:id appends `author_aliases=$n` whenever it is present) and
// the MCP handler has always forwarded it, because it copies its whole args map
// into the request body. Measured below rather than assumed: the value reaches
// the PATCH body even while unpublished, exactly as aihub#425 measured for
// pf_recall's cursor and pf_remember's tags.
//
// What was broken is that the capability was reachable only by a caller who
// guessed a name no schema mentions — and the consequence was narrow and total.
// pf_create_user DOES publish author_aliases, so aliases could be set once at
// creation and never changed from MCP again — and the case that could not be
// fixed was the one that matters: an alias entered wrongly, or an author who
// acquired a new email.
//
// ⚠️ This comment used to say "aliases are how a git commit author maps to a
// user". Measured 2026-09-10 by aihub#543: users.author_aliases has writers and
// NO reader anywhere in internal/ or pkg/, so nothing in aihub attributes a
// commit by it — commit records take their author from the authenticated caller.
// TestAuthorAliasesIsWrittenAndNeverRead (user_admin_surface_test.go) is that
// census, and docs/mcp-cards/pf_create_user.md and pf_update_user.md carry the
// corrected sentence. The gap is real and is recorded rather than fixed; what
// this file asserts is unaffected, since publishing a bound field is worth doing
// whether or not a reader exists yet.
//
// No database needed:
//
//	go test ./internal/mcp/ -run TestUpdateUserAuthorAliases -v

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestUpdateUserAuthorAliasesIsPublished is the arm that is RED before this
// change. It reads the schema the server actually publishes over tools/list.
func TestUpdateUserAuthorAliasesIsPublished(t *testing.T) {
	props := schemaProps(t, publishedTool(t, "pf_update_user"))

	prop, ok := props["author_aliases"]
	if !ok {
		t.Fatalf("pf_update_user does not publish author_aliases; it publishes %v.\n"+
			"    The server binds it and this handler forwards it, so the capability exists "+
			"and is reachable only by guessing.", keysOf(props))
	}
	if prop.Type != "array" {
		t.Errorf("author_aliases is published as %q, want \"array\" — pf_create_user publishes "+
			"it as an array and the server binds []string; a scalar here would teach callers "+
			"a shape the far end cannot decode", prop.Type)
	}

	// The empty-array spelling must be documented, because the server
	// distinguishes it and nothing else does. `req.AuthorAliases != nil` means an
	// omitted field leaves the column alone while [] decodes to a non-nil empty
	// slice and CLEARS every alias. A caller told only "updated git author
	// aliases" would reasonably send [] meaning "no change" and silently wipe
	// the mapping — the destructive direction, which is why this is asserted
	// rather than left to the description's author.
	for _, want := range []string{"[]", "clear"} {
		if !strings.Contains(prop.Description, want) {
			t.Errorf("author_aliases' description must tell callers what the empty array does "+
				"(absent = keep, [] = clear); it is missing %q.\n  got: %s", want, prop.Description)
		}
	}
}

// TestUpdateUserAuthorAliasesReachesTheWire is the other end, and it is GREEN on
// both arms by design.
//
// It is a regression pin, NOT evidence for this change: the value already
// reached the PATCH body before the schema line existed, and this test is the
// measurement that establishes that, restated so it cannot silently stop being
// true. Publishing a name the process then drops is the aihub#148/#259 defect
// wearing the opposite sign, and nothing else in the tree would catch it for
// this tool — pf_update_user has no wire-probe registry of its own.
func TestUpdateUserAuthorAliasesReachesTheWire(t *testing.T) {
	f := newFakeAihub(t)
	callTool(t, f, "pf_update_user", map[string]any{
		"id":             "u_probe426",
		"author_aliases": []any{"alice@example.com", "Alice A"},
	})

	calls := f.recorded()
	if len(calls) != 1 {
		t.Fatalf("expected exactly one HTTP call, got %d", len(calls))
	}
	raw, _ := json.Marshal(calls[0].Body["author_aliases"])
	if string(raw) != `["alice@example.com","Alice A"]` {
		t.Errorf("author_aliases in the PATCH body = %s, want [\"alice@example.com\",\"Alice A\"]", raw)
	}
	// `id` addresses the user in the URL and must not also appear in the body,
	// where the server does not bind it. Without this the test would pass for a
	// handler that forwarded its arguments indiscriminately.
	if _, leaked := calls[0].Body["id"]; leaked {
		t.Errorf("id was forwarded into the request body as well as the URL: %v", calls[0].Body)
	}
}

// TestUpdateUserClearingAliasesIsDistinctFromOmitting pins the distinction the
// description now publishes, at the wire.
//
// The two spellings must arrive differently, or the description is a promise
// about behaviour the client does not implement: [] has to reach the body as an
// empty array (the server's non-nil test then clears the column), and omitting
// the field has to leave it out entirely rather than sending null.
func TestUpdateUserClearingAliasesIsDistinctFromOmitting(t *testing.T) {
	t.Run("empty array is sent as an empty array", func(t *testing.T) {
		f := newFakeAihub(t)
		callTool(t, f, "pf_update_user", map[string]any{
			"id": "u_probe426", "author_aliases": []any{},
		})
		body := f.recorded()[0].Body
		v, present := body["author_aliases"]
		if !present {
			t.Fatalf("author_aliases was dropped when empty; the server can only clear the "+
				"column if the key arrives: %v", body)
		}
		raw, _ := json.Marshal(v)
		if string(raw) != "[]" {
			t.Errorf("author_aliases = %s, want []", raw)
		}
	})

	t.Run("omitted stays absent", func(t *testing.T) {
		f := newFakeAihub(t)
		callTool(t, f, "pf_update_user", map[string]any{
			"id": "u_probe426", "display_name": "Alice",
		})
		body := f.recorded()[0].Body
		if _, present := body["author_aliases"]; present {
			t.Errorf("author_aliases appeared in the body for a call that did not mention it "+
				"(%v). The server tests req.AuthorAliases != nil, so a key sent as null or [] "+
				"here would clear a user's aliases on a display-name change.", body)
		}
	})
}
