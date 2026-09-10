package mcp_test

// aihub#543 probe wave 2 — the `docs/mcp-cards/pf_create_project.md` claims about
// what hop 1 states and what the schema can and cannot express.
//
//	"**§6.1 T1-4** — the ruling is to gate the published schema against DB CHECKs
//	 rather than field-by-field; `name`'s character rule is enforced in the domain
//	 and stated here in prose."
//	"The `repos` description carries an **all-or-nothing** rule: …"
//	"That is a conditional requirement a flat `required` array cannot express, so
//	 it is stated in prose…"
//	"The all-or-nothing `repos` rule is unenforced by the schema and enforced by
//	 the server…"
//	"- No members can be set at creation: the only way to add one is
//	 `pf_update_project`, which replaces the whole list."
//
// 🔴 WHY THE EXISTING GATES SEE NONE OF THIS. K2/K3 pin each tool's schema to a
// hash of itself, so a `name` description that stops naming its bounds and a
// `pattern` that appears out of nowhere are both just a hash moving. The
// universal gate drives every published parameter and asserts it arrives, which
// is presence — it cannot see that the `required` array is DELIBERATELY flat, nor
// that the three statements of the name rule (hop 1's prose, the migration's
// CHECK, this repo's regexp) are meant to be the same rule. A relation between
// three strings is exactly what a per-thing hash cannot hold.
//
// Every expected value is read out of the tree rather than written here: the
// bounds come from the published description, the pattern from the migration, and
// the vocabulary from the card. The base-strength arm's rule — a literal in the
// test goes green on the day the value moves, which is the one day it was needed
// — applies three times over in this file.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestPublishedProjectName|TestPublishedRepoBlock|TestProjectMembers' -count=1

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// projectCardPath and projectDDLPath are the two files this arm reads its
// expected values out of.
var (
	projectCardPath = filepath.Join("..", "..", "docs", "mcp-cards", "pf_create_project.md")
	projectDDLPath  = filepath.Join("..", "db", "migrations", "0012_projects.sql")
	projectsGoPath  = filepath.Join("..", "domain", "projects.go")
)

// publishedPropObject returns one published parameter's whole schema object, not
// just its description.
//
// The absence assertions below are about the KEYS of that object — a `pattern`,
// a `minLength`, a `maxLength` — and publishedParamDescription reads one value
// out of it, so it cannot answer "what else is in here".
func publishedPropObject(t *testing.T, tool, param string) map[string]any {
	t.Helper()
	raw, err := json.Marshal(publishedTool(t, tool).InputSchema)
	if err != nil {
		t.Fatalf("marshal InputSchema for %q: %v", tool, err)
	}
	var schema struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode InputSchema for %q: %v", tool, err)
	}
	p, ok := schema.Properties[param]
	if !ok {
		t.Fatalf("%s publishes no %q parameter (it publishes %d). Every assertion about that "+
			"parameter's schema would otherwise be a statement about an absent object.",
			tool, param, len(schema.Properties))
	}
	return p
}

// publishedParamDescriptions returns one tool's parameter names mapped to their
// published descriptions.
//
// A separate helper from publishedSchemaProps for two reasons this file needs:
// that one maps a name to its TYPE, and it treats an empty property set as a
// FAILURE — correct for a tool known to take parameters, wrong for a census that
// walks the whole registry and legitimately meets pf_list_projects, whose schema
// is the empty object.
func publishedParamDescriptions(t *testing.T, tool string) map[string]string {
	t.Helper()
	raw, err := json.Marshal(publishedTool(t, tool).InputSchema)
	if err != nil {
		t.Fatalf("marshal InputSchema for %q: %v", tool, err)
	}
	var schema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode InputSchema for %q: %v", tool, err)
	}
	out := make(map[string]string, len(schema.Properties))
	for k, v := range schema.Properties {
		out[k] = v.Description
	}
	return out
}

// ddlNameCheckRe reads the projects.name CHECK constraint's regexp literal out of
// the migration.
var ddlNameCheckRe = regexp.MustCompile(`name\s+TEXT PRIMARY KEY CHECK \(name ~ '([^']+)'\)`)

// domainNameReRe reads the same rule out of this repo's own regexp declaration.
var domainNameReRe = regexp.MustCompile("projectNameRe = regexp.MustCompile\\(`([^`]+)`\\)")

// publishedBoundsRe reads a "1-40 chars" style bound out of a published
// description.
var publishedBoundsRe = regexp.MustCompile(`(\d+)-(\d+) chars`)

// TestPublishedProjectNameRuleIsTheEnforcedOne binds hop 1's prose, the
// migration's CHECK and the domain regexp to each other.
//
// Three sources, because the card's Policy bullet makes a claim about all three:
// the rule is enforced in the DOMAIN, stated in hop 1 as PROSE, and the ruling it
// cites is to gate the published schema against the DB's CHECKs. So the arm
// asserts the DDL and the regexp are the same string, that hop 1's stated bounds
// are that string's real bounds, and that the schema carries no field-by-field
// constraint of its own — which is the "rather than field-by-field" half, and the
// one a reader would otherwise have to take on trust.
//
// MUTANTS (applied to this tree; the verdict is what RAN, not what was expected):
//
//	M1 enforcement: widen the domain regexp to {0,63} and leave the CHECK alone
//	                                             RED  the_check_and_the_regexp_are_one_rule
//	                                                  + the_published_bounds_are_the_real_ones
//	M2 enforcement: widen the migration's CHECK and leave the regexp alone
//	                                             RED  the_check_and_the_regexp_are_one_rule
//	M3 enforcement: allow a leading digit in both (`^[a-z0-9]`)
//	                                             RED  the_published_bounds_are_the_real_ones
//	                                                  /a_leading_digit_is_refused
//	M4 enforcement: add `"pattern"` to the published `name` prop — the field-by-field
//	   gating the cited ruling declines
//	                                             RED  the_schema_states_no_field_level_constraint
//	M5 publication: change the published description's bounds to "1-64 chars"
//	                                             RED  the_published_bounds_are_the_real_ones
//	M6 publication: delete the bounds from the published description entirely
//	                                             RED  the bounds floor in projectNameBounds
//	G1 control:     reword the published description's leading words without
//	   touching the bounds                     GREEN  the arm reads the numbers, not the
//	                                                  sentence
func TestPublishedProjectNameRuleIsTheEnforcedOne(t *testing.T) {
	desc, ok := publishedParamDescription(t, mustMarshalSchema(t, "pf_create_project"), "name")
	if !ok || strings.TrimSpace(desc) == "" {
		t.Fatalf("pf_create_project publishes no `name` description (%q). The card says the "+
			"character rule is STATED here in prose; with no prose there is nothing to compare "+
			"the enforcement against, and this arm would be asserting a rule against itself.", desc)
	}

	lo, hi := projectNameBounds(t, desc)
	ddl := readFileForArm(t, projectDDLPath, "the migration carrying the projects.name CHECK")
	src := readFileForArm(t, projectsGoPath, "the domain file declaring projectNameRe")

	ddlMatch := ddlNameCheckRe.FindStringSubmatch(ddl)
	if ddlMatch == nil {
		t.Fatal("the projects migration declares no `CHECK (name ~ '…')` on the name column. " +
			"The cited ruling is to gate the published schema against the DB's CHECKs; with no " +
			"CHECK there is nothing to gate against, and this arm must say so rather than " +
			"compare the other two and report green.")
	}
	srcMatch := domainNameReRe.FindStringSubmatch(src)
	if srcMatch == nil {
		t.Fatal("internal/domain/projects.go declares no `projectNameRe = regexp.MustCompile(…)`. " +
			"The card says the rule is enforced in the domain; if the declaration has moved, " +
			"this arm is comparing the CHECK against nothing.")
	}

	t.Run("the_check_and_the_regexp_are_one_rule", func(t *testing.T) {
		if ddlMatch[1] != srcMatch[1] {
			t.Errorf("the migration CHECKs %q and the domain enforces %q. Two spellings of one "+
				"rule is the shape where a name the domain accepts is refused by Postgres as an "+
				"opaque 23514, or — worse in this direction — a name the domain refuses would be "+
				"accepted if it ever reached the INSERT another way.", ddlMatch[1], srcMatch[1])
		}
	})

	rule := regexp.MustCompile(ddlMatch[1])

	t.Run("the_published_bounds_are_the_real_ones", func(t *testing.T) {
		t.Run("the_shortest_allowed_name_is_accepted", func(t *testing.T) {
			if name := strings.Repeat("a", lo); !rule.MatchString(name) {
				t.Errorf("the published description says %d-%d chars and the enforced rule %q "+
					"refuses a %d-character name. The lower bound is the one a caller hits by "+
					"accident, on their shortest project.", lo, hi, ddlMatch[1], lo)
			}
		})
		t.Run("the_longest_allowed_name_is_accepted", func(t *testing.T) {
			if name := strings.Repeat("a", hi); !rule.MatchString(name) {
				t.Errorf("the published description says %d-%d chars and the enforced rule %q "+
					"refuses a %d-character name, so hop 1 promises a length the domain will "+
					"reject.", lo, hi, ddlMatch[1], hi)
			}
		})
		t.Run("one_over_the_published_maximum_is_refused", func(t *testing.T) {
			if name := strings.Repeat("a", hi+1); rule.MatchString(name) {
				t.Errorf("the enforced rule %q accepts a %d-character name while the published "+
					"description says %d-%d. A published maximum that is not the real one is the "+
					"aihub#238 shape read the other way round: the contract is narrower than the "+
					"code, so nothing refuses the value and the promise is simply untrue.",
					ddlMatch[1], hi+1, lo, hi)
			}
		})
		t.Run("an_empty_name_is_refused", func(t *testing.T) {
			if rule.MatchString("") {
				t.Errorf("the enforced rule %q accepts the empty name. The tool's own local guard "+
					"refuses it, and a rule that would accept it means the guard is the only "+
					"thing standing between a blank project name and the primary key.", ddlMatch[1])
			}
		})
		t.Run("a_leading_digit_is_refused", func(t *testing.T) {
			// The published prose says "lowercase letters/digits/dash/underscore" and
			// the rule additionally requires the FIRST character to be a letter. That
			// asymmetry is the part of the rule hop 1 does not spell out, so it is
			// asserted here rather than left to a reader to infer from the regexp they
			// cannot see.
			if rule.MatchString("1abc") {
				t.Errorf("the enforced rule %q accepts a name starting with a digit. Every project "+
					"name is also a slug prefix (`project#N`), and the rule's leading-letter "+
					"requirement is what keeps a slug from parsing as a number.", ddlMatch[1])
			}
		})
	})

	t.Run("the_schema_states_no_field_level_constraint", func(t *testing.T) {
		prop := publishedPropObject(t, "pf_create_project", "name")
		for _, banned := range []string{"pattern", "minLength", "maxLength", "format"} {
			if _, present := prop[banned]; present {
				t.Errorf("the published `name` schema carries %q. The cited T1-4 ruling is to gate "+
					"the published schema against the DB's CHECKs RATHER THAN field-by-field, and "+
					"the card tells a reader the character rule is stated in prose for exactly "+
					"that reason. A constraint here is a second copy of the rule that no arm "+
					"compares against the CHECK.", banned)
			}
		}
		if len(prop) < 2 {
			t.Fatalf("the published `name` schema has %d key(s) (%v) — too few to be a property "+
				"object, so the absences above are absences from nothing", len(prop), prop)
		}
	})
}

// projectNameBounds reads the two numbers out of a published description, and
// fails rather than defaulting if they are not there.
func projectNameBounds(t *testing.T, desc string) (lo, hi int) {
	t.Helper()
	m := publishedBoundsRe.FindStringSubmatch(desc)
	if m == nil {
		t.Fatalf("the published `name` description %q states no \"N-M chars\" bound. That bound "+
			"is what this arm compares the enforced rule against; deleting it is the cheapest "+
			"way to satisfy a check about it, which is what makes this a failure and not a skip.",
			desc)
	}
	lo, _ = strconv.Atoi(m[1])
	hi, _ = strconv.Atoi(m[2])
	if lo < 1 || hi <= lo {
		t.Fatalf("the published bound reads %d-%d, which is not a range", lo, hi)
	}
	return lo, hi
}

// readFileForArm reads a file this arm compares against, and treats a missing one
// as a failure.
func readFileForArm(t *testing.T, path, what string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s (%s): %v — it is one side of a comparison, and an unreadable side "+
			"makes the comparison vacuous rather than green", path, what, err)
	}
	return string(raw)
}

// repoBlockRuleFieldsRe and repoBlockAcceptedAloneRe read the card's partition of
// the structured block: the four content fields out of the aihub#588 rule clause,
// and the two generation-metadata fields out of the accepted-alone sentence. Their
// union is the six-field block. Anchored on the corrected wording — the pre-588
// clause ("if any structured field (…)") was measured false and its return would
// mean the false sentence is back.
var (
	repoBlockRuleFieldsRe    = regexp.MustCompile(`if any of the four content fields \(([^)]*)\)`)
	repoBlockAcceptedAloneRe = regexp.MustCompile("only `([a-z_]+)` or only `([a-z_]+)`")
)

// TestPublishedRepoBlockRuleLivesInProseAndNotInTheSchema is the three sentences
// about the all-or-nothing rule's PUBLICATION.
//
// The enforcement half is next door in `internal/domain/project_repo_block_rule_test.go`
// (`TestRepoDescriptionBlockTriggersOnTheFourContentFieldsOnly`). Until aihub#588
// the two sides disagreed — hop 1 said "any structured field" while the server
// reads four of the six — and the owner's 2026-09-10 ruling moved the DESCRIPTION
// to the measured rule, leaving enforcement alone. This arm holds the publication
// side of that ruling: the rule is stated in prose on every tool that takes
// `repos`, the stated trigger is the measured one (four content fields, with the
// two generation-metadata fields explicitly non-triggering), the pre-588 sentence
// stays gone, and the schema expresses none of it.
//
// ⚠️ SCOPE. The card's "the same limitation that produced `pf_ship` and
// `pf_batch_create_work_items` as separate tools" is a claim about why two tools
// exist, which is a design history no arm in this repo can check. What is checked
// is the limitation itself: the conditional requirement is absent from the flat
// `required` array and present in prose.
//
// MUTANTS (re-run for aihub#588, 2026-09-10 — each applied to this tree, shown
// RED, and reverted; `git diff --stat` checked non-empty before each run):
//
//	M7 enforcement: add `positioning` to pf_create_project's `required` list
//	                                             RED  the_required_array_names_only_the_name
//	M8 enforcement: give the `repos` prop an `items` sub-schema
//	                                             RED  the_repos_schema_expresses_no_conditional
//	M9 publication: restore the pre-588 sentence ("If any structured field is
//	   set, …") on pf_update_project's `repos` description only
//	                                             RED  every_tool_that_takes_repos_states_the_rule
//	                                                  /pf_update_project, three times —
//	                                                  the corrected trigger clause is
//	                                                  gone, the metadata non-trigger
//	                                                  half is gone, AND the false
//	                                                  sentence is named outright
//	M10 publication: drop the "do not trigger" half from pf_create_project's
//	   `repos` description only                  RED  every_tool_that_takes_repos_states_the_rule
//	                                                  /pf_create_project
//	M11 publication: drop `main_modules` from the card's trigger clause
//	                                             RED  the four-field floor on the
//	                                                  clause extraction — the field
//	                                                  names come from the card, so
//	                                                  the card and the descriptions
//	                                                  are compared
//	G2 control:     reword the card's hop 2-3 paragraph
//	                                           GREEN  the extraction is anchored on the
//	                                                  rule's own clause
func TestPublishedRepoBlockRuleLivesInProseAndNotInTheSchema(t *testing.T) {
	card := strings.Join(strings.Fields(readFileForArm(t, projectCardPath, "the create-project card")), " ")
	m := repoBlockRuleFieldsRe.FindStringSubmatch(card)
	if m == nil {
		t.Fatal("the card no longer states \"if any of the four content fields (…)\" — the " +
			"aihub#588 rule clause. The field names below come from that clause, so without it " +
			"this arm checks the descriptions against an empty list and reports green.")
	}
	var contentFields []string
	for _, f := range regexp.MustCompile("`([a-z_]+)`").FindAllStringSubmatch(m[1], -1) {
		contentFields = append(contentFields, f[1])
	}
	if len(contentFields) != 4 {
		t.Fatalf("the card's rule clause names %d content field(s) (%v); the rule is over four. "+
			"A different count means the card and this arm are describing different rules.",
			len(contentFields), contentFields)
	}
	a := repoBlockAcceptedAloneRe.FindStringSubmatch(card)
	if a == nil {
		t.Fatal("the card no longer names the two generation-metadata fields accepted alone, " +
			"in the form \"only `x` or only `y`\" — the aihub#588 description promises that " +
			"acceptance, so this arm cannot check the promise against a card that stopped " +
			"stating it.")
	}
	metadataFields := []string{a[1], a[2]}
	fields := append(append([]string{}, contentFields...), metadataFields...)
	if len(fields) != 6 {
		t.Fatalf("the card partitions the block into %d field(s) (%v); the block has six.",
			len(fields), fields)
	}

	// Quantified over the live registry rather than over a hand-written pair of
	// names: a third tool that grows a `repos` parameter is exactly the case a
	// two-name list skips, and the rule is the same rule wherever the parameter is.
	var withRepos []string
	for _, tool := range publishedToolList(t) {
		if _, ok := publishedParamDescriptions(t, tool.Name)["repos"]; ok {
			withRepos = append(withRepos, tool.Name)
		}
	}
	if len(withRepos) < 2 {
		t.Fatalf("only %d published tool(s) take a `repos` parameter (%v). The rule is a "+
			"property of the parameter, so a walk that found fewer than the create/update pair "+
			"is a walk that would report green having checked almost nothing.",
			len(withRepos), withRepos)
	}

	t.Run("every_tool_that_takes_repos_states_the_rule", func(t *testing.T) {
		for _, tool := range withRepos {
			t.Run(tool, func(t *testing.T) {
				desc := publishedParamDescriptions(t, tool)["repos"]
				if !strings.Contains(strings.ToLower(desc), "all-or-nothing") {
					t.Errorf("%s's `repos` description does not use the words \"all-or-nothing\". "+
						"That phrase is the card's name for the rule and the only warning a caller "+
						"gets that a partial block is refused.\nPublished: %q", tool, desc)
				}
				if !strings.Contains(desc, "all four content fields are required") {
					t.Errorf("%s's `repos` description does not state that all four content fields "+
						"are required.\nPublished: %q\nThe conditional cannot be expressed in the "+
						"schema, so this sentence is the whole published contract for it.", tool, desc)
				}
				// The aihub#588 pins, one per direction of the ruling. The trigger
				// clause must be the MEASURED one — the four content fields —
				// spelled so a caller can tell which fields arm the rule…
				if !strings.Contains(desc, "if any of the four content fields") {
					t.Errorf("%s's `repos` description no longer states the measured trigger "+
						"(\"if any of the four content fields …\").\nPublished: %q\nThat clause "+
						"is what aihub#588 moved the description TO; losing it re-opens the gap "+
						"where hop 1 and the enforcement describe different rules.", tool, desc)
				}
				if !strings.Contains(desc, "do not trigger") {
					t.Errorf("%s's `repos` description no longer says the generation-metadata "+
						"fields do not trigger the rule.\nPublished: %q\nThat half is the "+
						"acceptance the server has always granted and the pre-aihub#588 wording "+
						"denied; a caller holding back generated_commit because they cannot "+
						"complete the content block is the cost of dropping it.", tool, desc)
				}
				// …and the pre-588 sentence may not come back: "any structured
				// field" claims the six-field trigger that was measured false on
				// 2026-09-10 (hasDescriptionBlock reads the four content fields
				// and nothing else — internal/domain/project_repo_block_rule_test.go
				// holds that, both directions).
				if strings.Contains(strings.ToLower(desc), "any structured field") {
					t.Errorf("%s's `repos` description says \"any structured field\" again.\n"+
						"Published: %q\nThat is the exact sentence aihub#588 removed: the "+
						"enforcement trigger is the four content fields, so this wording tells a "+
						"caller that sending generated_at alone makes four fields required when "+
						"the server accepts it with none. If enforcement was really widened, the "+
						"owner ruling that rejected exactly that has to be revisited first.", tool, desc)
				}
				for _, field := range fields {
					if !strings.Contains(desc, field) {
						t.Errorf("%s's `repos` description does not name the structured field %q, "+
							"which the card lists as part of the block.\nPublished: %q\nA field a "+
							"caller is never told about is a field they cannot send — or, if they "+
							"guess it, one that silently changes what else is required.",
							tool, field, desc)
					}
				}
			})
		}
	})

	t.Run("the_required_array_names_only_the_name", func(t *testing.T) {
		required := publishedRequired(t, "pf_create_project")
		if len(required) != 1 || required[0] != "name" {
			t.Errorf("pf_create_project marks %v required. The card's point is that the "+
				"conditional requirement CANNOT be written here — a flat array says \"always\" "+
				"and the rule is \"only when the block is present\" — so anything beyond `name` "+
				"is either a new unconditional requirement or an attempt to express the "+
				"conditional in a place that cannot hold it.", required)
		}
	})

	t.Run("the_repos_schema_expresses_no_conditional", func(t *testing.T) {
		prop := publishedPropObject(t, "pf_create_project", "repos")
		for _, banned := range []string{"items", "if", "then", "allOf", "anyOf", "oneOf", "required"} {
			if _, present := prop[banned]; present {
				t.Errorf("the published `repos` schema carries %q. The card says the rule is "+
					"UNENFORCED by the schema and enforced by the server; a conditional the schema "+
					"now half-expresses is a second contract, and a caller satisfying it is not "+
					"the same as a caller satisfying validateDescriptionBlock.", banned)
			}
		}
		if got, _ := prop["type"].(string); got != "array" {
			t.Errorf("the published `repos` type is %q, want \"array\" — the floor for the "+
				"absences above, which an empty property object would satisfy trivially", got)
		}
	})
}

// TestProjectMembersAreSettableOnlyThroughTheUpdateTool is the hop-4 members
// bullet: nothing sets members at creation, and the one tool that does replaces
// the list.
//
// 🔴 The wire half is the interesting one. pf_create_project forwards its WHOLE
// argument map, so a `members` key a caller sends does leave this process and
// does reach the server — what drops it is `internal/domain`'s
// CreateProjectRequest having no field to bind it to
// (`internal/domain/project_creation_row_test.go`,
// `TestProjectCreationSetsTheCallerAsOwnerAndLeavesMembersAndTheCounterToTheSchema`).
// A reader of the card would reasonably assume the tool refuses it locally, and
// the difference matters: a locally-refused key is an error the caller sees, and a
// forwarded-and-ignored one is a 201 with members the caller believes they set.
//
// MUTANTS (applied to this tree; the verdict is what RAN):
//
//	M11 enforcement: publish a `members` parameter on pf_create_project
//	                                             RED  exactly_one_tool_publishes_members
//	                                                  + creation_publishes_no_members_parameter
//	M12 enforcement: build the create body from the published names instead of
//	    forwarding the whole map                 RED  an_unpublished_members_key_reaches_the_server
//	M13 enforcement: delete the aihub#389 unknown-parameter echo
//	                                             RED  the_tool_reports_it_as_unpublished
//	M14 publication: reword pf_update_project's `members` description so it no
//	    longer says it REPLACES the list         RED  the_one_tool_says_it_replaces_the_list
//	G3 control:     add a new unrelated parameter to pf_create_project
//	                                           GREEN  the census is about `members`, not
//	                                                  about the parameter count
func TestProjectMembersAreSettableOnlyThroughTheUpdateTool(t *testing.T) {
	var publishers []string
	for _, tool := range publishedToolList(t) {
		if _, ok := publishedParamDescriptions(t, tool.Name)[membersParam]; ok {
			publishers = append(publishers, tool.Name)
		}
	}

	t.Run("exactly_one_tool_publishes_members", func(t *testing.T) {
		if len(publishers) != 1 {
			t.Errorf("%d published tool(s) take a `%s` parameter (%v), and the card says the ONLY "+
				"way to add a member is pf_update_project. A second writer is a second set of "+
				"rules for the same list — and the compare-and-set token that guards it is "+
				"published on one tool only.", len(publishers), membersParam, publishers)
			return
		}
		if publishers[0] != "pf_update_project" {
			t.Errorf("the one tool publishing `%s` is %s, and the card names pf_update_project",
				membersParam, publishers[0])
		}
	})

	t.Run("creation_publishes_no_members_parameter", func(t *testing.T) {
		props := publishedParamDescriptions(t, "pf_create_project")
		if _, ok := props[membersParam]; ok {
			t.Errorf("pf_create_project publishes a `%s` parameter. The card's bullet is that no "+
				"members can be set at creation; publishing the parameter promises the opposite, "+
				"and the server has no field to bind it to.", membersParam)
		}
		if len(props) < 3 {
			t.Fatalf("pf_create_project publishes %d parameter(s) (%v) — too few to be this "+
				"tool, so the absence above is an absence from nothing",
				len(props), sortedPropNames(props))
		}
	})

	t.Run("the_one_tool_says_it_replaces_the_list", func(t *testing.T) {
		desc := publishedParamDescriptions(t, "pf_update_project")[membersParam]
		if !strings.Contains(desc, "REPLACES the whole member list") {
			t.Errorf("pf_update_project's `%s` description does not say it REPLACES the whole "+
				"member list.\nPublished: %q\nThat word is the card's, and it is the difference "+
				"between adding one person and removing everybody else.", membersParam, desc)
		}
	})

	t.Run("an_unpublished_members_key_reaches_the_server", func(t *testing.T) {
		f := newFakeAihub(t)
		f.on(createProjectWirePath, func(map[string]any) (int, any) {
			return http.StatusCreated, map[string]any{"name": "probe-project", "members": []any{}}
		})
		result, isErr := callTool(t, f, "pf_create_project", map[string]any{
			"name":       "probe-project",
			membersParam: []any{map[string]any{"user_id": "u_probe", "role": "writer"}},
		})
		if isErr {
			t.Fatalf("pf_create_project failed: %v — a failed call reaches the server with a "+
				"body nobody should draw conclusions from", result)
		}
		body := lastBodyFor(t, f, createProjectWirePath)
		if _, present := body[membersParam]; !present {
			t.Errorf("the create body carries no %q (it carries %v). The card's bullet reads as "+
				"if the tool refused it; measured, the whole argument map is forwarded and the "+
				"key is dropped by the server's request struct. If that has changed to a local "+
				"refusal, that is a better contract — and the card should say so, because the "+
				"error a caller sees is different.", membersParam, sortedBodyKeys(body))
		}

		t.Run("the_tool_reports_it_as_unpublished", func(t *testing.T) {
			// aihub#389's echo is the only thing that tells the caller their key had no
			// effect. Without it a forwarded-and-ignored `members` is a 201 that looks
			// like success.
			adjusted, ok := result["request_adjusted"]
			if !ok {
				t.Fatalf("the result carries no request_adjusted for a call that sent an "+
					"unpublished %q (result keys: %v). A key with no effect and no report is "+
					"indistinguishable from a key that worked.",
					membersParam, sortedBodyKeys(result))
			}
			if !strings.Contains(mustJSON(t, adjusted), membersParam) {
				t.Errorf("request_adjusted is %v and does not name %q", adjusted, membersParam)
			}
		})
	})
}

// membersParam and createProjectWirePath are the two names this arm quantifies
// over. Asserted rather than assumed: the body reads below take the last request
// to that path, and a tool posting elsewhere would leave them reading nothing.
const (
	membersParam          = "members"
	createProjectWirePath = "/v1/projects"
)
