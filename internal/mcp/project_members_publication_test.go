package mcp_test

// aihub#543 probe wave 2, band 2 (identity/authz) — the two
// `docs/mcp-cards/pf_update_project.md` sentences about what the members write
// TELLS a caller and what its two guards look like on the wire.
//
//	"The `members` description says outright that anyone missing from the list you
//	 send loses access, so adding one person means reading the current list and
//	 sending it back with the addition."
//	    -> TestPublishedMembersDescriptionTeachesTheReadModifyWrite
//	"`members_version` is an INT column and `*int` on the wire, so a quoted `"3"`
//	 becomes a JSON number here…"
//	    -> TestUpdateProjectCoercesBothMembersGuardsOnTheWire
//
// Both read a REAL session rather than the generated contract JSON, which
// carries no per-property descriptions; and both are DB-free, because a claim
// about published text and a claim about what leaves this process are not claims
// about a row.
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestPublishedMembersDescription|TestUpdateProjectCoerces' -count=1

import (
	"strings"
	"testing"
)

// TestPublishedMembersDescriptionTeachesTheReadModifyWrite binds the card's
// claim about the description to the description, and the description to the
// shape of the request it describes.
//
// 🔴 Both halves, because either alone is a gate that does not close. A text-only
// arm goes green on the day the wire starts sending a patch instead of a list —
// the description would then be a promise about a mechanism that no longer
// exists. A wire-only arm goes green on the day the sentence is deleted, and a
// destructive replace that nobody is warned about is the whole defect aihub#333
// was filed for: the caller does not know they are removing anybody.
//
// What the text has to carry is the CONSEQUENCE and the REPAIR — that the missing
// lose access, and that the way to add one person is to read the list first. A
// description saying only "REPLACES the whole member list" states a mechanism and
// leaves the reader to derive both, which is exactly what the aihub#333 review
// found callers not doing.
//
// MUTANTS:
//
//	M1 publication: drop "loses access" from the members description
//	                                          RED  names_the_consequence
//	M2 publication: drop the "(pf_list_projects)" read pointer
//	                                          RED  names_the_repair
//	M3 publication: drop the expected_removals sentence from members
//	                                          RED  points_at_the_guard
//	M4 enforcement: have the MCP handler send `members_patch` instead of
//	    `members`                             RED  the_whole_list_reaches_the_wire
//	M5 enforcement: have the handler drop `members` from the body entirely
//	                                          RED  the_whole_list_reaches_the_wire
//	M6 floor: point publishedTool at a name that is not registered
//	                                          RED  publishedTool fatals — the
//	                                               "description says nothing" and
//	                                               "tool does not exist" answers
//	                                               are then distinguishable
func TestPublishedMembersDescriptionTeachesTheReadModifyWrite(t *testing.T) {
	props := schemaProps(t, publishedTool(t, "pf_update_project"))
	members, published := props["members"]
	if !published {
		t.Fatalf("pf_update_project does not publish `members` at all; it publishes %v. Every "+
			"check below is about that description, so a missing property makes them vacuous "+
			"rather than false.", keysOf(props))
	}
	desc := members.Description

	t.Run("names_the_consequence", func(t *testing.T) {
		// "loses access" is the phrase the card quotes as said OUTRIGHT. Matched as
		// a phrase rather than as the whole sentence: the wording may be improved,
		// but a description that no longer says somebody loses something has stopped
		// making the promise.
		if !strings.Contains(strings.ToLower(desc), "loses access") {
			t.Errorf("the `members` description does not tell the caller that anyone missing "+
				"from the list loses access.\n  got: %s\nREPLACES is a mechanism; losing access "+
				"is the consequence, and pf_update_project.md says the description states it "+
				"outright. A caller who has to derive it is the caller aihub#333 measured "+
				"truncating their own list.", desc)
		}
	})

	t.Run("names_the_repair", func(t *testing.T) {
		// The repair is read-modify-write, and it is only actionable if the read
		// path is named: `members` is not returned by pf_update_project's own
		// response in a form a caller can resend blind.
		lower := strings.ToLower(desc)
		if !strings.Contains(lower, "read the current list") {
			t.Errorf("the `members` description does not tell the caller to read the current "+
				"list before adding one person.\n  got: %s", desc)
		}
		if !strings.Contains(desc, "pf_list_projects") {
			t.Errorf("the `members` description does not name the tool that returns the current "+
				"list, so the repair it prescribes is one the reader has to go looking for.\n"+
				"  got: %s", desc)
		}
	})

	t.Run("points_at_the_guard", func(t *testing.T) {
		// The caller who needs expected_removals is reading `members` and does not
		// know they are about to remove anybody, so `members` is where the pointer
		// has to be. TestProjectMembersRemovalToolSchemaAdvertisesExpectedRemovals
		// asserts the same pointer from the guard's side; this is the half a reader
		// of THIS description depends on.
		if !strings.Contains(desc, "expected_removals") {
			t.Errorf("the `members` description does not point at expected_removals.\n  got: %s",
				desc)
		}
	})

	// The wire half. A description promising "the whole list replaces the whole
	// list" is only true while the request really carries the whole list.
	t.Run("the_whole_list_reaches_the_wire", func(t *testing.T) {
		f := newFakeAihub(t)
		sent := []map[string]any{
			{"user_id": "u_one", "role": "viewer"},
			{"user_id": "u_two", "role": "maintainer"},
		}
		callTool(t, f, "pf_update_project", map[string]any{
			"name":            "p_probe580",
			"members":         sent,
			"members_version": 4,
		})

		calls := f.recorded()
		if len(calls) != 1 {
			t.Fatalf("expected exactly one HTTP call, got %d (%v)", len(calls), f.paths())
		}
		raw, present := calls[0].Body["members"]
		if !present {
			t.Fatalf("`members` never reached the PATCH body, so the description describes a "+
				"parameter this process drops. The server saw %v", calls[0].Body)
		}
		list, ok := raw.([]any)
		if !ok {
			t.Fatalf("`members` arrived as %T (%#v), not a JSON array — the server binds "+
				"[]MemberInput and would answer an opaque 400", raw, raw)
		}
		if len(list) != len(sent) {
			t.Errorf("`members` reached the wire with %d entr(y|ies), want %d. The description "+
				"promises the list the caller sends IS the new list; a request carrying fewer "+
				"than it was given would replace with something the caller never wrote.",
				len(list), len(sent))
		}
		// There must be no add/remove verb beside it. A patch-shaped key would mean
		// the description's REPLACE semantics were no longer the only semantics,
		// and a caller who read this description would be sending the wrong thing.
		for key := range calls[0].Body {
			if key == "members" {
				continue
			}
			if strings.HasPrefix(key, "members_") && key != "members_version" {
				t.Errorf("the PATCH body carries %q beside `members`. The published description "+
					"says the list REPLACES the whole member list; a second members-shaped key "+
					"means there is another mechanism and the description names only one of "+
					"them. Body: %v", key, calls[0].Body)
			}
		}
	})
}

// TestUpdateProjectCoercesBothMembersGuardsOnTheWire is the card's two coercion
// bullets, read off the bytes a peer really received.
//
// 🔴 Read what the assertion is on: the JSON TYPE, not the value's text.
// `"members_version":"3"` and `"members_version":3` both contain the digit, so a
// substring assertion passes on the uncoerced body — and the uncoerced body is
// precisely the aihub#241 B1 defect the coercion exists to prevent: echo's
// c.Bind refuses a string into a *int as `400 "invalid request body"`, which is
// indistinguishable from the server not knowing the parameter at all.
//
// The two coercions are asserted together because they are the same hazard one
// type over and are wired the same way — and because the SECOND one's failure
// mode is worse than an opaque 400: a dropped `expected_removals` comes back as
// 412 "you did not declare this removal" while the declaration sits in the
// caller's own request.
//
// The published TYPE is checked in the same arm as the wire type it produces.
// Without that, publishing `members_version` as a string would be green here
// (the coercion would still run) while every caller was told to send the shape
// the coercion exists to rescue.
//
// MUTANTS:
//
//	M7  enforcement: delete `normalizeIntArg(args, "members_version")` from
//	     pf_update_project's handler          RED  members_version_is_a_json_number
//	M8  enforcement: move that call BELOW `body := make(map[string]any)`
//	                                          RED  same — the body is built from the
//	                                               uncoerced map
//	M9  enforcement: delete `normalizeStringSliceArg(args, "expected_removals")`
//	                                          RED  expected_removals_is_a_json_array
//	M10 publication: change the published `members_version` type to "string"
//	                                          RED  the_published_types_match_the_wire
//	M11 floor: send neither guard              RED  the fatals below — a body with
//	                                               no guard in it cannot show a
//	                                               coercion either way
func TestUpdateProjectCoercesBothMembersGuardsOnTheWire(t *testing.T) {
	props := schemaProps(t, publishedTool(t, "pf_update_project"))

	f := newFakeAihub(t)
	// Both values in the shape a mixed-version or coercing client actually sends:
	// a quoted integer, and a bare string where an array is declared.
	callTool(t, f, "pf_update_project", map[string]any{
		"name":              "p_probe580",
		"members":           []map[string]any{{"user_id": "u_one", "role": "viewer"}},
		"members_version":   "3",
		"expected_removals": "u_two",
	})

	calls := f.recorded()
	if len(calls) != 1 {
		t.Fatalf("expected exactly one HTTP call, got %d (%v) — a zero here means something in "+
			"this process refused the quoted guard, which is the opaque failure the coercion "+
			"exists to replace", len(calls), f.paths())
	}
	body := calls[0].Body

	t.Run("members_version_is_a_json_number", func(t *testing.T) {
		raw, present := body["members_version"]
		if !present {
			t.Fatalf("members_version never reached the wire, so the compare-and-set guard is "+
				"silently absent and the write overwrites unconditionally. The server saw %v", body)
		}
		// The fake decodes into map[string]any, so a JSON number arrives as
		// float64 and a JSON string as string. That is the whole distinction.
		num, ok := raw.(float64)
		if !ok {
			t.Fatalf("members_version arrived as %T (%#v), not a JSON number. A quoted version "+
				"dies at the server's c.Bind as `400 BAD_REQUEST \"invalid request body\"` — a "+
				"message naming nothing, indistinguishable from the server not knowing the "+
				"parameter (aihub#241 B1). Body: %v", raw, raw, body)
		}
		if num != 3 {
			t.Errorf("members_version = %v on the wire, want 3 — the coercion changed the value "+
				"as well as the type, so the guard would compare against a version nobody read",
				num)
		}
	})

	t.Run("expected_removals_is_a_json_array", func(t *testing.T) {
		raw, present := body["expected_removals"]
		if !present {
			t.Fatalf("expected_removals never reached the wire. Dropped, it comes back as 412 "+
				"PROJECT_MEMBERS_UNDECLARED_REMOVAL while the declaration sits in the caller's "+
				"own request. The server saw %v", body)
		}
		list, ok := raw.([]any)
		if !ok {
			t.Fatalf("expected_removals arrived as %T (%#v), not a JSON array — the server binds "+
				"it into a []string. Body: %v", raw, raw, body)
		}
		if len(list) != 1 || list[0] != "u_two" {
			t.Errorf("expected_removals = %#v on the wire, want [\"u_two\"] — a bare user_id is "+
				"the natural mistake when removing exactly one person, so it is coerced rather "+
				"than dropped", list)
		}
	})

	t.Run("the_published_types_match_the_wire", func(t *testing.T) {
		for param, want := range map[string]string{
			"members_version":   "integer",
			"expected_removals": "array",
		} {
			p, published := props[param]
			if !published {
				t.Errorf("pf_update_project does not publish %q, so no caller can pass the "+
					"guard the coercion above rescues", param)
				continue
			}
			if p.Type != want {
				t.Errorf("%q is published as %q, want %q. The coercion above turns the caller's "+
					"looser value into this shape; publishing the looser shape instead tells "+
					"every caller to send the thing the coercion exists to rescue, and the day "+
					"the coercion is removed there is no record of what the wire wanted.",
					param, p.Type, want)
			}
		}
	})
}
