package mcp_test

// aihub#543 probe wave 2 — `docs/mcp-cards/pf_whoami.md`'s hop 2-3 and hop 4
// claims about the `projects` array this process builds:
//
//	"The second is best-effort: if it fails, the tool still answers with the
//	 whoami half"                     -> TestWhoamiKeepsTheServersOwnProjectsWhenTheEnrichmentFails
//	"The projects array is computed in this process, not by the server"
//	                                  -> TestWhoamiComputesTheProjectsArrayItself
//	"Admin and owner short-circuit to owner/owner … a WIDER vocabulary than the
//	 one pf_update_project's members accepts"
//	                                  -> TestWhoamiReportsAnOwnerRoleThatIsNoMemberRole
//	"The member scan is a SECOND derivation of a fact the server also derives"
//	                                  -> TestWhoamiDerivesTheMemberRoleWithoutReadingProjectRoles
//	"a member whose role is not a string … is a payload difference, not an
//	 authorization one"               -> TestWhoamiNonStringMemberRoleKeepsTheDefaultRole
//
// 🔴 The first arm corrects a card sentence that was MEASURED FALSE. The card
// said a failed second call makes the tool "simply omit `projects`", and that an
// absent `projects` key therefore means "the second call failed". It cannot:
// `internal/server/router.go` (`handleWhoami`) ALWAYS sends a `projects` key of
// its own — an array of project NAME STRINGS built from `project_roles` — and
// the enrichment in `registerLifecycleTools` only overwrites it when
// `client.ListProjects` returns no error. So the key is never absent; what
// changes is the SHAPE of its elements, from `{name, relation, role}` objects to
// bare strings. That is the discriminator a caller actually has, and it is what
// these arms and the corrected card now state.
//
// The fixtures drive the registered tool through a real MCP session against a
// fake aihub, so the dynamic types the handler switches on are produced by
// decoding wire bytes rather than asserted by the fixture — the reason
// tools_whoami_members_test.go gives for the same choice, and the reason
// aihub#312 was invisible to a fixture that handed the handler a typed value.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run TestWhoami -count=1

import (
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// whoamiIdentity is the nine-field payload internal/server's handleWhoami
// sends, with the two knobs these arms turn: what `project_roles` says (the
// SERVER's derivation) and what `projects` says (the server's own array, which
// the enrichment overwrites on success).
//
// It carries all nine fields rather than the two the handler reads, for the
// reason tools_whoami_members_test.go states: a fixture that sent fewer would
// leave a regression that dropped a pass-through field invisible.
func whoamiIdentity(projectRoles map[string]any, serverProjects []any) map[string]any {
	return map[string]any{
		"user_id":        "u_caller",
		"email":          "caller@example.com",
		"display_name":   "Caller",
		"user_type":      "human",
		"role":           "user",
		"project_roles":  projectRoles,
		"projects":       serverProjects,
		"api_key_id":     "ak_test",
		"server_version": "dev",
	}
}

// whoamiProjectEntries drives pf_whoami and returns the `projects` value with
// the identity half checked first.
//
// The FLOOR is here rather than in each arm: several assertions below are about
// the ELEMENT TYPE of `projects`, and a response that carried no identity at all
// would satisfy an element-type assertion over an empty list. So the whoami half
// is required to have arrived before anything is concluded about the array.
func whoamiProjectEntries(t *testing.T, f *fakeAihub) []any {
	t.Helper()
	got, isErr := callTool(t, f, "pf_whoami", nil)
	if isErr {
		t.Fatalf("pf_whoami returned an error result: %v — the card's claim is that this call still "+
			"answers, so a refusal is not the case under test", got)
	}
	if got["user_id"] != "u_caller" {
		t.Fatalf("pf_whoami answered without the identity half (user_id = %#v, keys %v). Every "+
			"assertion below is about the projects array, and an answer that lost the identity "+
			"would satisfy them by being empty rather than by being right",
			got["user_id"], sortedKeysOfAny(got))
	}
	entries, ok := got["projects"].([]any)
	if !ok {
		t.Fatalf("pf_whoami's projects key is %#v (%T), not an array. handleWhoami always sends one "+
			"and the enrichment replaces it, so neither an absent key nor a non-array is a state "+
			"this tool can produce", got["projects"], got["projects"])
	}
	return entries
}

func sortedKeysOfAny(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestWhoamiKeepsTheServersOwnProjectsWhenTheEnrichmentFails pins the card's
// corrected best-effort sentence.
//
// Both halves are asserted, because the failure the card used to describe —
// an ABSENT key — is not reachable, and asserting only the success path would
// leave the card free to keep describing a state the tool cannot produce.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M1  enforcement: answer a failed list with an empty ENRICHED array
//	    (`result["projects"] = []map[string]any{}`)
//	                                        RED  the_failed_call_leaves_the_servers
//	                                             _own_array
//	M2  enforcement: `delete(result, "projects")` on the listErr branch — i.e.
//	    make this card's OLD sentence true   RED  same subtest
//	P1  publication: make every citation in the card unresolvable
//	                                        RED  K12 (all 11 cited sentences of
//	                                             this card return to unclassified)
func TestWhoamiKeepsTheServersOwnProjectsWhenTheEnrichmentFails(t *testing.T) {
	t.Run("the_failed_call_leaves_the_servers_own_array", func(t *testing.T) {
		f := newFakeAihub(t)
		f.on("/v1/users/me", func(map[string]any) (int, any) {
			return 200, whoamiIdentity(map[string]any{"aihub": "writer"}, []any{"aihub", "tether"})
		})
		f.on("/v1/projects", func(map[string]any) (int, any) {
			return 500, map[string]any{"error": map[string]any{
				"code": "INTERNAL_ERROR", "message": "failed to list projects"}}
		})

		entries := whoamiProjectEntries(t, f)
		if len(entries) != 2 {
			t.Fatalf("projects = %#v, want the two names handleWhoami sent — the enrichment must not "+
				"have run at all", entries)
		}
		for i, e := range entries {
			if _, isObject := e.(map[string]any); isObject {
				t.Errorf("projects[%d] is an enriched object (%#v) although the project list call "+
					"failed; the relation/role in it would be derived from no project data at all", i, e)
			}
			if _, isString := e.(string); !isString {
				t.Errorf("projects[%d] is %#v (%T), want the bare project-name string handleWhoami "+
					"sends. The element TYPE is the only signal a caller has that the second call "+
					"failed — the key itself is never absent, which is what this card used to claim",
					i, e, e)
			}
		}
	})

	t.Run("the_successful_call_replaces_it_with_objects", func(t *testing.T) {
		f := newFakeAihub(t)
		f.on("/v1/users/me", func(map[string]any) (int, any) {
			return 200, whoamiIdentity(map[string]any{"aihub": "writer"}, []any{"aihub", "tether"})
		})
		f.on("/v1/projects", func(map[string]any) (int, any) {
			return 200, map[string]any{"items": []any{map[string]any{
				"name":          "aihub",
				"owner_user_id": "u_someone_else",
				"visible":       true,
				"members":       []any{map[string]any{"user_id": "u_caller", "role": "writer"}},
			}}}
		})

		entries := whoamiProjectEntries(t, f)
		if len(entries) != 1 {
			t.Fatalf("projects = %#v, want the one project the list call returned", entries)
		}
		entry, ok := entries[0].(map[string]any)
		if !ok {
			t.Fatalf("projects[0] is %#v (%T), want an enriched object — this is the control for the "+
				"arm above: without it, a tool that always left the server's strings in place would "+
				"satisfy the failure assertion perfectly", entries[0], entries[0])
		}
		for _, key := range []string{"name", "relation", "role"} {
			if _, present := entry[key]; !present {
				t.Errorf("the enriched entry carries no %q: %#v", key, entry)
			}
		}
	})
}

// TestWhoamiComputesTheProjectsArrayItself pins "The projects array is computed
// in this process, not by the server".
//
// The fixture makes the two answers DISTINGUISHABLE: the server's own `projects`
// carries a name no project row has, so a tool that forwarded the server's value
// and a tool that computed its own cannot both pass. An assertion that only
// checked the entries were objects would stay green on a server that started
// sending objects itself.
//
// MUTANTS:
//
//	M3  enforcement: replace `result["projects"] = projectInfos` with a no-op,
//	    leaving the server's array in place       RED — here, in the successful
//	                                              branch of the arm above, and in
//	                                              six arms that predate this file
//	P5  publication: rename this arm                RED  K12 ARM_CITATION
//	                                                     _UNRESOLVED
func TestWhoamiComputesTheProjectsArrayItself(t *testing.T) {
	f := newFakeAihub(t)
	f.on("/v1/users/me", func(map[string]any) (int, any) {
		// A name that appears in NO project row, so forwarding is visible.
		return 200, whoamiIdentity(map[string]any{}, []any{"only-the-server-says-this"})
	})
	f.on("/v1/projects", func(map[string]any) (int, any) {
		return 200, map[string]any{"items": []any{map[string]any{
			"name":          "aihub",
			"owner_user_id": "u_someone_else",
			"visible":       true,
			"members":       []any{},
		}}}
	})

	entries := whoamiProjectEntries(t, f)
	if len(entries) != 1 {
		t.Fatalf("projects = %#v, want exactly the one row this process classified", entries)
	}
	entry, ok := entries[0].(map[string]any)
	if !ok {
		t.Fatalf("projects[0] is %#v (%T), want the object this process builds", entries[0], entries[0])
	}
	if entry["name"] != "aihub" {
		t.Errorf("projects[0].name = %#v, want \"aihub\" — the value came from the whoami payload's "+
			"own projects key rather than from the project list this process walked", entry["name"])
	}
	if entry["relation"] != "public" || entry["role"] != "viewer" {
		t.Errorf("projects[0] = %#v, want relation=public role=viewer: the caller is neither admin, "+
			"nor the owner, nor in members, and those defaults are set in this process — the server "+
			"sends no relation or role at all", entry)
	}
}

// TestWhoamiReportsAnOwnerRoleThatIsNoMemberRole pins the two-vocabulary split
// the card's hop-4 bullet and its §6.2 T2-17 policy row both name: the `role`
// this tool reports can be `owner`, which is the `projects.owner_user_id`
// column and not a member role, so pf_update_project's `members` refuses it.
//
// The refusal half is asserted against domain.RoleLevel rather than by calling
// UpdateProject, which needs a database: RoleLevel's keys ARE the roles
// UpdateProject accepts for a member, held by
// internal/domain/projects_test.go's
// TestRoleLevel_LadderIsExactlyTheValidatedVocabulary (it reads the validation's
// own literals out of the AST), and by
// TestOwnerIsNotAMemberRoleUpdateProjectAccepts in the same package, which
// names this claim directly.
//
// MUTANTS:
//
//	M4  enforcement: change the short-circuit to memberRole = "maintainer"
//	                                              RED  here and in
//	                                                   TestWhoamiAdminAndOwner
//	                                                   ResponsesAreByteIdentical
//	M24 enforcement: add "owner": 4 to domain.RoleLevel  RED  in this arm's domain
//	                                                   twin (the vocabularies stop
//	                                                   differing, so the card's
//	                                                   WIDER is false)
//	P1  publication: make every citation in the card unresolvable  RED  K12
func TestWhoamiReportsAnOwnerRoleThatIsNoMemberRole(t *testing.T) {
	f := newFakeAihub(t)
	f.on("/v1/users/me", func(map[string]any) (int, any) {
		id := whoamiIdentity(map[string]any{}, []any{})
		// An admin: the first of the two short-circuit conditions.
		id["role"] = "admin"
		return 200, id
	})
	f.on("/v1/projects", func(map[string]any) (int, any) {
		return 200, map[string]any{"items": []any{map[string]any{
			"name":          "aihub",
			"owner_user_id": "u_someone_else",
			"visible":       true,
			// Listed as a writer, deliberately: if the branches were reordered
			// so members were consulted first this would come back member/writer.
			"members": []any{map[string]any{"user_id": "u_caller", "role": "writer"}},
		}}}
	})

	entries := whoamiProjectEntries(t, f)
	if len(entries) != 1 {
		t.Fatalf("projects = %#v, want one entry", entries)
	}
	entry, _ := entries[0].(map[string]any)
	if entry["relation"] != "owner" || entry["role"] != "owner" {
		t.Fatalf("an admin caller got %#v, want relation=owner role=owner", entry)
	}

	reported, _ := entry["role"].(string)
	if _, isMemberRole := domain.RoleLevel[reported]; isMemberRole {
		t.Errorf("pf_whoami reports role=%q and domain.RoleLevel has a rung for it, so it is a role "+
			"pf_update_project's members would ACCEPT. The card's claim is the opposite — that this "+
			"tool draws from a wider vocabulary, and that naming the value a caller cannot send is "+
			"the whole of the T2-17 ruling. Either the ladder grew a rung for a column value, or "+
			"this tool stopped reporting the column.", reported)
	}
	if len(domain.RoleLevel) == 0 {
		t.Fatal("domain.RoleLevel is empty, so the membership test above cannot fail however wrong " +
			"the reported role is — the floor for this arm")
	}
}

// TestWhoamiDerivesTheMemberRoleWithoutReadingProjectRoles pins "The member scan
// is a SECOND derivation of a fact the server also derives".
//
// The fixture makes the two derivations DISAGREE: `project_roles` — which
// internal/server/middleware.go's roleForUserInMembers fills — says maintainer,
// while the members array this process walks says writer. One response, two
// derivations, and the card's point is that nothing makes them agree across the
// mcp/server boundary. A tool that read project_roles instead of walking members
// would report maintainer here.
//
// ⚠️ What this deliberately does NOT do is gate the two into agreement. The card
// states that nothing does, and the whole reason the claim is worth publishing is
// that the pair is held together by prose; an arm that made them agree would
// close a real gap and make that sentence false in the same change.
//
// MUTANTS:
//
//	M5  enforcement: read the role out of result["project_roles"] rather than
//	    walking members                            RED  (reports maintainer)
//	M6  enforcement: drop the `break` so the last matching member wins
//	                                               RED  (the fixture's second
//	                                                    entry for the same caller
//	                                                    is there for this)
//	P1  publication: make every citation in the card unresolvable  RED  K12
func TestWhoamiDerivesTheMemberRoleWithoutReadingProjectRoles(t *testing.T) {
	f := newFakeAihub(t)
	f.on("/v1/users/me", func(map[string]any) (int, any) {
		// The SERVER's derivation of the same fact, deliberately different.
		return 200, whoamiIdentity(map[string]any{"aihub": "maintainer"}, []any{"aihub"})
	})
	f.on("/v1/projects", func(map[string]any) (int, any) {
		return 200, map[string]any{"items": []any{map[string]any{
			"name":          "aihub",
			"owner_user_id": "u_someone_else",
			"visible":       true,
			"members": []any{
				map[string]any{"user_id": "u_caller", "role": "writer"},
				// A second entry for the same caller, so "the first match wins"
				// is asserted rather than assumed.
				map[string]any{"user_id": "u_caller", "role": "viewer"},
			},
		}}}
	})

	got, isErr := callTool(t, f, "pf_whoami", nil)
	if isErr {
		t.Fatalf("pf_whoami returned an error result: %v", got)
	}
	roles, ok := got["project_roles"].(map[string]any)
	if !ok || roles["aihub"] != "maintainer" {
		t.Fatalf("project_roles = %#v, want the server's own value passed through untouched — without "+
			"it there is only one derivation in this response and the claim has nothing to compare",
			got["project_roles"])
	}
	entries, ok := got["projects"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("projects = %#v, want one entry", got["projects"])
	}
	entry, _ := entries[0].(map[string]any)
	if entry["relation"] != "member" || entry["role"] != "writer" {
		t.Errorf("projects[0] = %#v, want relation=member role=writer. The role here is derived in "+
			"THIS process from the members array; reading it out of project_roles would report "+
			"%q, and that the two can differ at all is the duplication this card names.",
			entry, roles["aihub"])
	}
}

// TestWhoamiNonStringMemberRoleKeepsTheDefaultRole pins the mcp half of "One
// shape outside that set still differs — a member whose `role` is not a string".
//
// This process finds the membership (relation=member) and leaves memberRole at
// its "viewer" default, because the type assert on the role fails. The server's
// half of the same comparison — roleForUserInMembers returning ("", found) for
// the same entry, so project_roles carries {"aihub":""} rather than {} — is
// held by internal/server/middleware_project_roles_test.go
// (TestRoleForUserInMembers_NonStringRoleIsFoundWithNoRole), which also holds
// that neither shape clears the viewer rung, i.e. the difference is a payload
// one and not an authorization one.
//
// MUTANTS:
//
//	M7  enforcement: replace the `if r, ok := mem["role"].(string); ok` guard
//	    with fmt.Sprint(mem["role"])               RED  (role becomes "5")
//	M8  enforcement: require a string role for the membership to match at all
//	                                               RED  (relation falls back to
//	                                                    public, which is the
//	                                                    aihub#312 direction)
//	P1  publication: make every citation in the card unresolvable  RED  K12
func TestWhoamiNonStringMemberRoleKeepsTheDefaultRole(t *testing.T) {
	f := newFakeAihub(t)
	f.on("/v1/users/me", func(map[string]any) (int, any) {
		return 200, whoamiIdentity(map[string]any{}, []any{})
	})
	f.on("/v1/projects", func(map[string]any) (int, any) {
		return 200, map[string]any{"items": []any{map[string]any{
			"name":          "aihub",
			"owner_user_id": "u_someone_else",
			"visible":       true,
			// A JSON number where a role string belongs. Not null: a null role
			// decodes to a nil interface and asserts to "" the same way an
			// absent key does, which is a different shape from this one.
			"members": []any{map[string]any{"user_id": "u_caller", "role": 5}},
		}}}
	})

	entries := whoamiProjectEntries(t, f)
	if len(entries) != 1 {
		t.Fatalf("projects = %#v, want one entry", entries)
	}
	entry, _ := entries[0].(map[string]any)
	if entry["relation"] != "member" {
		t.Errorf("projects[0].relation = %#v, want \"member\": the entry names the caller, so the "+
			"membership is found whatever the role's type — reporting public here would be the "+
			"aihub#312 under-reporting direction all over again", entry["relation"])
	}
	if entry["role"] != "viewer" {
		t.Errorf("projects[0].role = %#v, want the \"viewer\" default. A non-string role must not be "+
			"coerced into one: \"5\" is not a member role and publishing it would put a value "+
			"outside every vocabulary in this system into an authorization-shaped field",
			entry["role"])
	}
}
