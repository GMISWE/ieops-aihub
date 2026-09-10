package domain

// aihub#543 probe wave 2 — the `docs/mcp-cards/pf_create_project.md` claims about
// the repos structured block.
//
//	"The `repos` description carries an **all-or-nothing** rule: if any structured
//	 field (`positioning`, `tech_stack`, `main_modules`, `change_scenarios`,
//	 `generated_at`, `generated_commit`) is set, all four content fields are
//	 required, in English."
//	"The all-or-nothing `repos` rule is unenforced by the schema and enforced by
//	 the server…"
//	"The all-or-nothing rule fires on a NARROWER trigger than the description's
//	 wording…"
//
// 🔴 THE MEASUREMENT THAT PUT THE THIRD SENTENCE ON THE CARD. The published
// `repos` description enumerates SIX fields as the structured block and then says
// "If any structured field is set, all four content fields are required
// (English)". The enforcement reads four: hasDescriptionBlock is
// `Positioning != "" || len(TechStack) > 0 || len(MainModules) > 0 ||
// len(ChangeScenarios) > 0`, and neither generation-metadata field appears in it.
// So a caller sending only `generated_commit` is told by hop 1 that four content
// fields have just become required, and the server accepts the repo with none of
// them. Measured 2026-09-10 by the subtests below, both directions.
//
// The card now states the measured trigger next to the published one rather than
// paraphrasing the published one as if it were the behaviour, and this arm holds
// BOTH: the four that trigger and the two that do not. Writing only the first
// half would have been the more comfortable arm and it is the one that lets the
// gap close silently, because "generated_at alone is accepted" is exactly the
// case an all-or-nothing arm never sends.
//
// ⚠️ WHAT THIS ARM DOES NOT HOLD: "in English". The published text says it, the
// card repeats it, and nothing in this package inspects the language of a string
// — so it is a documented expectation with no enforcement anywhere, and pinning
// it here would need a language detector this repo does not have.
//
// No database — validateRepos is a pure function over a JSON payload:
//
//	GOWORK=off go test ./internal/domain/ -run TestRepoDescriptionBlock -count=1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoCardPath is the card this arm reads its field partition out of.
var repoCardPath = filepath.Join("..", "..", "docs", "mcp-cards", "pf_create_project.md")

// repoBlockTriggerListRe captures the card's enumeration of the structured block.
var repoBlockTriggerListRe = regexp.MustCompile(`if any structured field \(([^)]*)\)`)

// repoBlockNonTriggerRe captures the two fields the card's measured bullet says
// are accepted alone.
var repoBlockNonTriggerRe = regexp.MustCompile("only `([a-z_]+)` or only `([a-z_]+)`")

// repoBlockBackticked pulls the names out of a captured list.
var repoBlockBackticked = regexp.MustCompile("`([a-z_]+)`")

// repoBlockFieldPartition reads the card and returns (all six block fields, the
// two that do not trigger the rule, the four that do).
//
// 🔴 Read from the card, never written down here. Both halves matter: a literal
// list in this file would go green on the day the card starts naming a different
// set, and — the sharper direction — the PARTITION is the finding. If somebody
// edits the card to move `generated_at` into the triggering half without
// changing hasDescriptionBlock, the subtests below flip and this arm reds.
func repoBlockFieldPartition(t *testing.T) (all, nonTriggers, triggers []string) {
	t.Helper()
	raw, err := os.ReadFile(repoCardPath)
	if err != nil {
		t.Fatalf("read %s: %v — the card is the publication side of this arm, so a missing "+
			"one is a failure and not an empty pass", repoCardPath, err)
	}
	flat := strings.Join(strings.Fields(string(raw)), " ")

	m := repoBlockTriggerListRe.FindStringSubmatch(flat)
	if m == nil {
		t.Fatal("the card no longer states \"if any structured field (…)\". That clause is the " +
			"population this arm walks; without it the loops below run zero times and report " +
			"green about a rule they never sent a payload for.")
	}
	for _, f := range repoBlockBackticked.FindAllStringSubmatch(m[1], -1) {
		all = append(all, f[1])
	}

	n := repoBlockNonTriggerRe.FindStringSubmatch(flat)
	if n == nil {
		t.Fatal("the card's hop-4 bullet no longer names the two fields accepted alone, in the " +
			"form \"only `x` or only `y`\". That pair is the measured half of this arm — the " +
			"half a comfortable version would omit — so an extraction that cannot find it must " +
			"fail rather than fall back to checking only the easy direction.")
	}
	nonTriggers = []string{n[1], n[2]}

	skip := map[string]bool{n[1]: true, n[2]: true}
	for _, f := range all {
		if !skip[f] {
			triggers = append(triggers, f)
		}
	}
	if len(all) != 6 || len(nonTriggers) != 2 || len(triggers) != 4 {
		t.Fatalf("the card partitions the block into %d field(s) = %d trigger(s) + %d "+
			"non-trigger(s) (%v / %v). The rule is named \"all-or-nothing\" over FOUR content "+
			"fields inside a SIX-field block; any other split means the card and this arm are "+
			"describing different rules.", len(all), len(triggers), len(nonTriggers), triggers, nonTriggers)
	}
	return all, nonTriggers, triggers
}

// repoBlockPayload builds a one-repo `repos` array with exactly one structured
// field set, which is the shape every subtest below sends.
func repoBlockPayload(t *testing.T, field string) json.RawMessage {
	t.Helper()
	entry := map[string]any{"name": "probe", "url": "git@github.com:GMISWE/probe.git"}
	switch field {
	case "positioning":
		entry["positioning"] = "one line about the repo"
	case "tech_stack":
		entry["tech_stack"] = []string{"Go"}
	case "main_modules":
		entry["main_modules"] = []map[string]string{{"path": "internal/domain", "role": "rules"}}
	case "change_scenarios":
		entry["change_scenarios"] = []string{"add MCP tool"}
	case "generated_at":
		entry["generated_at"] = "2026-09-10T00:00:00Z"
	case "generated_commit":
		entry["generated_commit"] = "0123456789abcdef"
	default:
		t.Fatalf("the card names a structured field %q this arm cannot build a payload for. A "+
			"field added to the block and not to this switch would be walked with an EMPTY "+
			"entry, which every branch of the rule accepts — a silent green for the one field "+
			"nobody had checked.", field)
	}
	b, err := json.Marshal([]any{entry})
	if err != nil {
		t.Fatalf("marshal the %s payload: %v", field, err)
	}
	return b
}

// repoBlockCompletePayload sets all four content fields at once.
func repoBlockCompletePayload(t *testing.T, triggers []string) json.RawMessage {
	t.Helper()
	entry := map[string]any{"name": "probe", "url": "git@github.com:GMISWE/probe.git"}
	for _, f := range triggers {
		var one []any
		if err := json.Unmarshal(repoBlockPayload(t, f), &one); err != nil {
			t.Fatalf("decode the %s payload: %v", f, err)
		}
		for k, v := range one[0].(map[string]any) {
			if k != "name" && k != "url" {
				entry[k] = v
			}
		}
	}
	b, err := json.Marshal([]any{entry})
	if err != nil {
		t.Fatalf("marshal the complete payload: %v", err)
	}
	return b
}

// TestRepoDescriptionBlockTriggersOnTheFourContentFieldsOnly drives every field
// the card names, one at a time, and asserts the verdict the card's partition
// predicts.
//
// MUTANTS (applied to this tree; the verdict is what RAN):
//
//	M1 enforcement: delete the `len(r.ChangeScenarios) > 0` disjunct from
//	   hasDescriptionBlock                       RED  a_content_field_alone_is_refused/change_scenarios
//	M2 enforcement: add `r.GeneratedCommit != ""` to hasDescriptionBlock — the
//	   change that would make the PUBLISHED wording true
//	                                             RED  generation_metadata_alone_is_accepted/generated_commit
//	                                                  🔴 which is the point of that
//	                                                  subtest: it pins the measured
//	                                                  gap, so closing the gap is a
//	                                                  change somebody signs on the
//	                                                  card as well as in the code
//	M3 enforcement: make validateDescriptionBlock return nil unconditionally
//	                                             RED  all four cases of
//	                                                  a_content_field_alone_is_refused
//	M4 enforcement: require a fifth field in validateDescriptionBlock
//	                                             RED  the_complete_block_is_accepted
//	M5 publication: drop `generated_at` from the card's six-field list
//	                                             RED  the partition floor in
//	                                                  repoBlockFieldPartition
//	M6 publication: reword the card's measured bullet to name `positioning` as one
//	   of the two accepted alone                 RED  a_content_field_alone_is_refused/positioning
//	                                                  AND generation_metadata_alone_is_accepted
//	G1 control:     reword the card's "Policy" section
//	                                           GREEN  both extractions are anchored on
//	                                                  their own clauses, not on a
//	                                                  position in the file
func TestRepoDescriptionBlockTriggersOnTheFourContentFieldsOnly(t *testing.T) {
	all, nonTriggers, triggers := repoBlockFieldPartition(t)
	t.Logf("card partition: block=%v triggers=%v accepted-alone=%v", all, triggers, nonTriggers)

	t.Run("a_content_field_alone_is_refused", func(t *testing.T) {
		for _, field := range triggers {
			t.Run(field, func(t *testing.T) {
				aerr := validateRepos(repoBlockPayload(t, field))
				if aerr == nil {
					t.Errorf("a repo carrying only %s was accepted. The card calls this rule "+
						"all-or-nothing over the four content fields, and a field that can stand "+
						"alone is a field the rule does not cover — which is how a half-written "+
						"repo description reaches the row and /pf-init reads it as complete.", field)
					return
				}
				if aerr.Code != ErrRepoIncompleteDescription {
					t.Errorf("a repo carrying only %s was refused with %s (%q), want %s. The code "+
						"is what a caller branches on; a generic bad-request says the payload was "+
						"malformed, which is a different repair.",
						field, aerr.Code, aerr.Message, ErrRepoIncompleteDescription)
				}
			})
		}
	})

	t.Run("generation_metadata_alone_is_accepted", func(t *testing.T) {
		for _, field := range nonTriggers {
			t.Run(field, func(t *testing.T) {
				if aerr := validateRepos(repoBlockPayload(t, field)); aerr != nil {
					t.Errorf("a repo carrying only %s was refused with %s (%q). The card records "+
						"this as MEASURED behaviour that the published wording overstates: hop 1 "+
						"says \"any structured field\" and hasDescriptionBlock reads four. If that "+
						"has been fixed, this is the good news — but it is a contract change, and "+
						"the card's bullet and the published description have to move with it "+
						"rather than after it.", field, aerr.Code, aerr.Message)
				}
			})
		}
	})

	t.Run("the_complete_block_is_accepted", func(t *testing.T) {
		if aerr := validateRepos(repoBlockCompletePayload(t, triggers)); aerr != nil {
			t.Errorf("a repo carrying all four content fields was refused with %s (%q). Without "+
				"this case every refusal above is satisfied by a rule that refuses everything, "+
				"which polices nothing and blocks every caller.", aerr.Code, aerr.Message)
		}
	})

	t.Run("an_empty_block_is_accepted", func(t *testing.T) {
		// The "may be absent" half. Without it the all-or-nothing rule could be read
		// as "always required", and every repo entry that predates the block would
		// be unwritable.
		bare, err := json.Marshal([]any{map[string]any{
			"name": "probe", "url": "git@github.com:GMISWE/probe.git"}})
		if err != nil {
			t.Fatalf("marshal the bare payload: %v", err)
		}
		if aerr := validateRepos(bare); aerr != nil {
			t.Errorf("a repo with no structured block at all was refused with %s (%q). "+
				"All-or-nothing includes the nothing.", aerr.Code, aerr.Message)
		}
	})
}
