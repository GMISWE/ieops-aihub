package mcp_test

// aihub#543 probe wave 2 — the two zero-parameter list tools, whose whole risk
// is on the response side.
//
//	pf_list_projects
//	  "the description … names `members_version` as \"the compare-and-set token
//	   to pass back to `pf_update_project` when changing members\""
//	      -> TestPublishedMembersVersionTokenClaimNamesTheToolThatConsumesIt
//	  "`jsonResult`, no projection: the single top-level key is `items`"
//	      -> TestListProjectsPassesTheServerAnswerThroughUnprojected
//	pf_list_users
//	  "`jsonResult`, no projection; the single top-level key is `items`"
//	      -> TestListUsersPassesTheServerAnswerThroughUnprojected
//
// 🔴 "No projection" is a claim about a key that is NOT there, and an absence
// cannot be told apart from an empty response — so each pass-through arm sends a
// key the tool has never heard of and requires it to arrive. A test that only
// checked `items` came back would stay green against a slim function that
// dropped everything else, which is the aihub#419 G3 shape and the defect
// aihub#422 found in the takeover handler's keep-list.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestPublishedMembersVersion|TestList(Projects|Users)PassesThe' -count=1

import (
	"sort"
	"strings"
	"testing"
)

// TestPublishedMembersVersionTokenClaimNamesTheToolThatConsumesIt pins the
// pf_list_projects card's hop 0-1 sentence to the schema the session really
// publishes, and to the parameter on the other tool that makes the advice
// actionable.
//
// internal/mcp/project_members_cas_e2e_db_test.go
// (TestProjectMembersCASToolSchemaAdvertisesMembersVersion) already requires
// this description to contain the token's NAME. What it does not require, and
// what the card's sentence claims, is that the description says what the token
// IS and which tool takes it — "a guard nobody can find the input for is a guard
// nobody passes" is about the pointer, not the word.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M44 enforcement: cut the description back to "Each project includes
//	    members_version.", keeping the word     RED  here; the existing CAS arm
//	                                                 stays green, which is why
//	                                                 this one exists
//	M45 enforcement: rename pf_update_project's members_version parameter
//	                                            RED  the_consumer_binds_it
//	P2  publication: make every citation in the card unresolvable  RED  K12
func TestPublishedMembersVersionTokenClaimNamesTheToolThatConsumesIt(t *testing.T) {
	desc := publishedTool(t, "pf_list_projects").Description
	if len(desc) < 40 {
		t.Fatalf("pf_list_projects' published description is %q — too short to carry the claim, so "+
			"the substring assertions below would be reporting an empty schema rather than a "+
			"missing sentence", desc)
	}

	for _, want := range []string{"members_version", "compare-and-set", "pf_update_project"} {
		if !strings.Contains(desc, want) {
			t.Errorf("pf_list_projects' description does not name %q.\n  got: %s\nThe card says this "+
				"description does one job beyond listing: it tells a caller what members_version is "+
				"and which tool to pass it back to. A name with no pointer leaves the CAS guard "+
				"discoverable only by reading another tool's schema.", want, desc)
		}
	}

	t.Run("the_consumer_binds_it", func(t *testing.T) {
		props := schemaProps(t, publishedTool(t, "pf_update_project"))
		if _, ok := props["members_version"]; !ok {
			t.Errorf("pf_list_projects tells callers to pass members_version back to "+
				"pf_update_project, which publishes %v. Advice that names a parameter the other "+
				"tool does not accept is worse than none: the value is sent, dropped by the "+
				"binder, and the caller reads the 200 as a passed guard.", keysOf(props))
		}
	})
}

// listToolTopLevelKeys drives one zero-parameter list tool against a fake aihub
// that answers with `items` plus one key nothing in this process knows about,
// and returns the top-level keys the model was handed.
//
// The unknown key is the instrument. `items` arriving proves nothing about
// projection — a keep-list carries it by definition — so the arm's subject is
// the key that has no reason to survive.
func listToolTopLevelKeys(t *testing.T, tool, path string, item map[string]any) []string {
	t.Helper()
	f := newFakeAihub(t)
	f.on(path, func(map[string]any) (int, any) {
		return 200, map[string]any{
			"items": []any{item},
			// A key no version of this server has ever sent and no slim function
			// lists. Under a keep-list it disappears; under a pass-through it
			// arrives.
			"a_key_no_slim_function_lists": "survives-only-without-a-projection",
		}
	})

	got, isErr := callTool(t, f, tool, nil)
	if isErr {
		t.Fatalf("%s returned an error result: %v", tool, got)
	}
	items, ok := got["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("%s did not return the one item the fake sent (items = %#v). Every assertion below "+
			"is about the top-level key set, and an answer that lost the payload would make the "+
			"set smaller for the wrong reason", tool, got["items"])
	}
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestListProjectsPassesTheServerAnswerThroughUnprojected pins "jsonResult, no
// projection: the single top-level key is items".
//
// MUTANTS:
//
//	M46 enforcement: return jsonResult(map[string]any{"items": result["items"]})
//	    — a two-line keep-list                  RED  (the unknown key vanishes)
//	M47 enforcement: rename the forwarded key to `projects`
//	                                            RED  (the floor in the helper)
//	P6  publication: rename this arm            RED  K12 ARM_CITATION_UNRESOLVED
func TestListProjectsPassesTheServerAnswerThroughUnprojected(t *testing.T) {
	keys := listToolTopLevelKeys(t, "pf_list_projects", "/v1/projects", map[string]any{
		"name": "aihub", "members_version": 3, "owner_user_id": "u_owner",
	})
	assertUnprojected(t, "pf_list_projects", keys)
}

// TestListUsersPassesTheServerAnswerThroughUnprojected is the same claim on the
// other list tool, and it is a separate arm rather than a table row because the
// two tools have separate handlers: pf_list_users' is in tools_users.go and a
// projection added to one says nothing about the other.
//
// MUTANTS:
//
//	M48 enforcement: wrap the pf_list_users result in a keep-list  RED
//	P3  publication: make every citation in the pf_list_users card unresolvable
//	                                                               RED  K12
func TestListUsersPassesTheServerAnswerThroughUnprojected(t *testing.T) {
	keys := listToolTopLevelKeys(t, "pf_list_users", "/v1/admin/users", map[string]any{
		"id": "u_1", "email": "a@b.c", "display_name": "A", "user_type": "human", "role": "writer",
	})
	assertUnprojected(t, "pf_list_users", keys)
}

// assertUnprojected holds both halves of the claim: `items` is the only key the
// SERVER's own answer contributes, and a key the server adds later is forwarded
// rather than dropped.
func assertUnprojected(t *testing.T, tool string, keys []string) {
	t.Helper()
	want := []string{"a_key_no_slim_function_lists", "items"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Errorf("%s handed the model top-level keys %v, want %v.\nThe card says this tool has no "+
			"slim function, so exactly what the server sent arrives: `items` because that is the "+
			"one key the endpoint builds, and the unknown key because nothing here filters. A "+
			"missing unknown key is a projection the card does not publish; an extra key is this "+
			"process inventing one.", tool, keys, want)
	}
}
