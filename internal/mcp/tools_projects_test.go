package mcp_test

// aihub#279: pf_list_projects gains an `include_repos` switch.
//
// ─── Why these tests are shaped in pairs ────────────────────────────────────
//
// The projection is one `delete()`. The only way to get it wrong that a naive
// test would miss is to get it wrong in the *direction the test does not look*,
// so every assertion here has a counterpart pointing the other way:
//
//	include_repos=false drops repos   ←→  absent / true still returns them
//	                                      (TestListProjectsDefaultKeepsRepos —
//	                                      without it, a handler that ALWAYS
//	                                      stripped repos would be fully green,
//	                                      and would silently stop `polyforge
//	                                      init` cloning anything)
//	repos goes away                   ←→  repo_count arrives in its place
//	                                      (pf-project/SKILL.md renders a repos
//	                                      count column; dropping the array with
//	                                      nothing to count is a regression, not
//	                                      a saving)
//	repos goes away                   ←→  members / owner_user_id do NOT
//	                                      (pf_whoami derives relation+role from
//	                                      exactly those two fields over the same
//	                                      client.ListProjects; widening this
//	                                      projection would downgrade every
//	                                      member's role with no error anywhere)
//
// These run against the fake aihub from tools_fusion_test.go, i.e. through the
// real registered tool over a real MCP session, so they cover the argument →
// handler → response wiring and not just a helper in isolation. No database.
//
// Run: go test ./internal/mcp/ -run TestListProjects -v   (no database needed)

import (
	"encoding/json"
	"strings"
	"testing"
)

// projectsFixture mirrors the wire shape of GET /v1/projects: `repos` and
// `members` are json.RawMessage columns with no omitempty, so both keys are
// always present and are `null` (not absent) when unset.
func projectsFixture() map[string]any {
	return map[string]any{
		"items": []any{
			map[string]any{
				"name":          "aihub",
				"owner_user_id": "u_owner",
				"visible":       true,
				"wi_seq":        309,
				"members": []any{
					map[string]any{"user_id": "u_member", "role": "writer"},
				},
				"repos": []any{
					map[string]any{
						"name":        "aihub",
						"url":         "git@github.com:GMISWE/ieops-aihub.git",
						"positioning": "Go HTTP server + PostgreSQL backend for polyforge",
						"tech_stack":  []any{"go", "postgres"},
						"main_modules": []any{
							map[string]any{"path": "internal/mcp", "role": "MCP tool surface"},
						},
					},
					map[string]any{
						"name":        "marketplace",
						"url":         "git@github.com:GMISWE/marketplace.git",
						"positioning": "GMI internal Claude Code plugin marketplace",
					},
				},
			},
			map[string]any{
				"name":          "reposless",
				"owner_user_id": "u_other",
				"visible":       false,
				"members":       nil,
				"repos":         nil,
			},
		},
	}
}

// listProjects calls the real tool against a fake aihub serving projectsFixture.
func listProjects(t *testing.T, args map[string]any) (map[string]any, bool) {
	t.Helper()
	f := newFakeAihub(t)
	f.on("/v1/projects", func(map[string]any) (int, any) { return 200, projectsFixture() })
	return callTool(t, f, "pf_list_projects", args)
}

// projectsByName indexes the decoded items[] so assertions read by name.
func projectsByName(t *testing.T, res map[string]any) map[string]map[string]any {
	t.Helper()
	itemsAny, ok := res["items"]
	if !ok {
		t.Fatalf("response has no items[]: %v", res)
	}
	items, ok := itemsAny.([]any)
	if !ok {
		t.Fatalf("items is %T, want []any", itemsAny)
	}
	out := map[string]map[string]any{}
	for _, item := range items {
		proj, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("item is %T, want map[string]any", item)
		}
		name, _ := proj["name"].(string)
		out[name] = proj
	}
	return out
}

// TestListProjectsSchemaPublishesIncludeRepos is hop 1: the parameter has to be
// reachable at all. Before this branch pf_list_projects published
// emptyObjectSchema() — no properties whatsoever — so an argument sent by any
// caller had nothing to bind to.
func TestListProjectsSchemaPublishesIncludeRepos(t *testing.T) {
	schemas := toolInputSchemas(t)
	raw, ok := schemas["pf_list_projects"]
	if !ok {
		t.Fatal("pf_list_projects is not registered")
	}
	var schema struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("pf_list_projects schema is not valid JSON: %v", err)
	}
	p, ok := schema.Properties["include_repos"]
	if !ok {
		t.Fatalf("pf_list_projects must publish include_repos; schema was %s", raw)
	}
	if p.Type != "boolean" {
		t.Errorf("include_repos must be published as a boolean, got %q", p.Type)
	}
	if len(schema.Required) != 0 {
		t.Errorf("include_repos must stay optional, got required=%v", schema.Required)
	}
	// The default is the whole mixed-version-safety argument, so a caller
	// reading the schema has to be told which way it points.
	if !strings.Contains(strings.ToLower(p.Description), "default true") {
		t.Errorf("include_repos description must state that the default is true, got %q", p.Description)
	}
}

// TestListProjectsDefaultKeepsRepos is the reverse probe, and the one that
// actually protects production: `polyforge init` and /pf-init both call
// pf_list_projects / GET /v1/projects bare and read repos to clone and to
// detect a stale repo map. A handler that stripped repos unconditionally would
// pass every other test in this file.
func TestListProjectsDefaultKeepsRepos(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"argument absent", nil},
		{"empty arguments", map[string]any{}},
		{"explicit true", map[string]any{"include_repos": true}},
		{"string spelling of true", map[string]any{"include_repos": "true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, isErr := listProjects(t, tc.args)
			if isErr {
				t.Fatalf("pf_list_projects returned an error result: %v", res)
			}
			projects := projectsByName(t, res)

			aihub := projects["aihub"]
			reposAny, present := aihub["repos"]
			if !present {
				t.Fatalf("repos must be present by default, got %v", aihub)
			}
			repos, ok := reposAny.([]any)
			if !ok {
				t.Fatalf("repos is %T, want []any", reposAny)
			}
			if len(repos) != 2 {
				t.Fatalf("want 2 repos, got %d", len(repos))
			}
			// Not just the array: the heavy nested fields are the point.
			first, _ := repos[0].(map[string]any)
			if first["main_modules"] == nil {
				t.Errorf("the default response must carry the full repo metadata, got %v", first)
			}
			// repo_count is the substitute for repos, so shipping both would
			// change the default response shape for every existing caller.
			if _, present := aihub["repo_count"]; present {
				t.Errorf("repo_count must not appear while repos is included, got %v", aihub)
			}
			if projects["reposless"]["repos"] != nil {
				t.Errorf("a null repos must stay null by default, got %v", projects["reposless"]["repos"])
			}
		})
	}
}

// TestListProjectsIncludeReposFalseDropsRepos is the forward probe: it fails on
// the pre-aihub#279 build, where the argument bound to nothing and the full
// repos array came back regardless.
func TestListProjectsIncludeReposFalseDropsRepos(t *testing.T) {
	for _, tc := range []struct {
		name string
		arg  any
	}{
		{"bool false", false},
		{"string spelling", "false"},
		{"numeric spelling", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, isErr := listProjects(t, map[string]any{"include_repos": tc.arg})
			if isErr {
				t.Fatalf("pf_list_projects returned an error result: %v", res)
			}
			projects := projectsByName(t, res)

			for name, proj := range projects {
				if _, present := proj["repos"]; present {
					t.Errorf("%s: repos must be dropped, got %v", name, proj["repos"])
				}
			}
			// pf-project/SKILL.md's repos column must remain computable.
			if got := projects["aihub"]["repo_count"]; got != float64(2) {
				t.Errorf("aihub repo_count = %v (%T), want 2", got, got)
			}
			// A null repos is zero repos, not an unknown number.
			if got := projects["reposless"]["repo_count"]; got != float64(0) {
				t.Errorf("reposless repo_count = %v (%T), want 0", got, got)
			}
		})
	}
}

// TestListProjectsIncludeReposFalseKeepsWhoamiInputs pins the projection's
// blast radius. pf_whoami calls the same client.ListProjects and computes each
// project's relation/role from owner_user_id and members[]; if this projection
// ever widened to either of them, every non-owner member would quietly become
// relation="public" / role="viewer" with no error raised anywhere.
func TestListProjectsIncludeReposFalseKeepsWhoamiInputs(t *testing.T) {
	res, isErr := listProjects(t, map[string]any{"include_repos": false})
	if isErr {
		t.Fatalf("pf_list_projects returned an error result: %v", res)
	}
	aihub := projectsByName(t, res)["aihub"]

	if got, _ := aihub["owner_user_id"].(string); got != "u_owner" {
		t.Errorf("owner_user_id = %q, want u_owner", got)
	}
	members, ok := aihub["members"].([]any)
	if !ok || len(members) != 1 {
		t.Fatalf("members must survive the projection, got %v", aihub["members"])
	}
	member, _ := members[0].(map[string]any)
	if member["user_id"] != "u_member" || member["role"] != "writer" {
		t.Errorf("members[0] = %v, want {user_id: u_member, role: writer}", member)
	}
	// Everything else that is not repos stays too.
	for _, key := range []string{"name", "visible", "wi_seq"} {
		if _, present := aihub[key]; !present {
			t.Errorf("%s must survive the projection, got %v", key, aihub)
		}
	}
}

// TestListProjectsRejectsUnreadableIncludeRepos: an unparseable value is
// rejected, not defaulted. Defaulting is what makes a mis-typed boolean
// indistinguishable from an absent one — the failure parseBoolArg was written
// for, where `ready_only: "true"` silently returned the unfiltered list.
func TestListProjectsRejectsUnreadableIncludeRepos(t *testing.T) {
	for _, arg := range []any{"maybe", 7, []any{false}} {
		res, isErr := listProjects(t, map[string]any{"include_repos": arg})
		if !isErr {
			t.Errorf("include_repos=%#v must be rejected, got a success result: %v", arg, res)
			continue
		}
		if raw, _ := res["_raw"].(string); !strings.Contains(raw, "include_repos") {
			t.Errorf("include_repos=%#v: error must name the parameter, got %q", arg, raw)
		}
	}
}
