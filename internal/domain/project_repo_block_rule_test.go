package domain

// aihub#543 probe wave 2, retriggered by aihub#588 — the
// `docs/mcp-cards/pf_create_project.md` claims about the repos structured block.
//
//	"if any of the four content fields (`positioning`, `tech_stack`,
//	 `main_modules`, `change_scenarios`) is set, all four content fields are
//	 required, in English"
//	"a repo entry carrying only `generated_at` or only `generated_commit` is
//	 accepted with no content field at all"
//	"The all-or-nothing `repos` rule is unenforced by the schema and enforced by
//	 the server…"
//
// 🔴 THE MEASUREMENT, in two acts. Act one (aihub#582, 2026-09-10): the published
// `repos` description enumerated SIX fields as the structured block and then said
// "If any structured field is set, all four content fields are required
// (English)" — while the enforcement reads four: hasDescriptionBlock is
// `Positioning != "" || len(TechStack) > 0 || len(MainModules) > 0 ||
// len(ChangeScenarios) > 0`, and neither generation-metadata field appears in it.
// A caller sending only `generated_commit` was told by hop 1 that four content
// fields had just become required, and the server accepted the repo with none of
// them. Act two (aihub#588, same day, owner ruling ①): re-measured on the current
// tree by these same subtests — the trigger is unchanged — and the DESCRIPTION
// moved to the measured rule while the enforcement deliberately stayed put
// (today's enforcement is LOOSER than the old wording, so no caller was hurt;
// widening it would newly refuse metadata-only entries existing callers may
// send). The published half of that ruling is held next door by
// TestPublishedRepoBlockRuleLivesInProseAndNotInTheSchema
// (internal/mcp/project_publication_contract_test.go), which also refuses the
// old sentence outright.
//
// This arm holds the ENFORCEMENT against the card's partition, in BOTH
// directions: the four that trigger and the two that do not. Writing only the
// first half would have been the more comfortable arm and it is the one that
// lets the partition drift silently, because "generated_at alone is accepted"
// is exactly the case an all-or-nothing arm never sends.
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

// repoBlockTriggerListRe captures the card's enumeration of the four content
// fields inside the corrected rule clause. Anchored on the aihub#588 wording; the
// pre-588 clause ("if any structured field (…)") no longer exists in the card and
// its return here would mean the false sentence is back.
var repoBlockTriggerListRe = regexp.MustCompile(`if any of the four content fields \(([^)]*)\)`)

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
		t.Fatal("the card no longer states \"if any of the four content fields (…)\" — the " +
			"aihub#588 rule clause. That clause is the trigger population this arm walks; " +
			"without it the loops below run zero times and report green about a rule they " +
			"never sent a payload for. If the clause was reworded, move this regex with it; " +
			"if it reverted to the pre-588 \"any structured field\" form, that sentence was " +
			"measured false and the publication arm next door refuses it too.")
	}
	for _, f := range repoBlockBackticked.FindAllStringSubmatch(m[1], -1) {
		triggers = append(triggers, f[1])
	}

	n := repoBlockNonTriggerRe.FindStringSubmatch(flat)
	if n == nil {
		t.Fatal("the card no longer names the two fields accepted alone, in the " +
			"form \"only `x` or only `y`\". That pair is the measured half of this arm — the " +
			"half a comfortable version would omit — so an extraction that cannot find it must " +
			"fail rather than fall back to checking only the easy direction.")
	}
	nonTriggers = []string{n[1], n[2]}

	seen := map[string]bool{}
	for _, f := range triggers {
		if seen[f] {
			t.Fatalf("the card's trigger clause names %q twice (%v)", f, triggers)
		}
		seen[f] = true
	}
	for _, f := range nonTriggers {
		if seen[f] {
			t.Fatalf("the card puts %q on BOTH sides of the partition (triggers=%v, "+
				"accepted-alone=%v) — the two clauses are describing different rules", f,
				triggers, nonTriggers)
		}
		seen[f] = true
	}
	all = append(append(all, triggers...), nonTriggers...)
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
// MUTANTS (re-run for aihub#588, 2026-09-10 — each applied to this tree, shown
// RED, and reverted; `git diff --stat` checked non-empty before each run):
//
//	M1 enforcement: delete the `len(r.ChangeScenarios) > 0` disjunct from
//	   hasDescriptionBlock                       RED  a_content_field_alone_is_refused/change_scenarios
//	M2 enforcement: add `r.GeneratedCommit != ""` to hasDescriptionBlock — the
//	   change that would have made the PRE-588 published wording true, and the
//	   option the aihub#588 ruling rejected
//	                                             RED  generation_metadata_alone_is_accepted/generated_commit
//	                                                  🔴 which is the point of that
//	                                                  subtest: the description now
//	                                                  PROMISES the acceptance, so
//	                                                  widening the trigger breaks a
//	                                                  stated contract and must move
//	                                                  the descriptions, the card and
//	                                                  this arm in one signed diff
//	M3 enforcement: make validateDescriptionBlock return nil unconditionally
//	                                             RED  all four cases of
//	                                                  a_content_field_alone_is_refused
//	M4 enforcement: require a fifth field in validateDescriptionBlock
//	                                             RED  the_complete_block_is_accepted
//	M5 publication: drop `tech_stack` from the card's trigger clause
//	                                             RED  the partition floor in
//	                                                  repoBlockFieldPartition (3+2 ≠ 6)
//	M6 publication: reword the card's accepted-alone pair to name `positioning`
//	                                             RED  the both-sides fatal in
//	                                                  repoBlockFieldPartition —
//	                                                  `positioning` cannot be both a
//	                                                  trigger and accepted alone
//	M7 publication: restore the pre-588 clause ("if any structured field (…)")
//	   in place of the corrected one             RED  the trigger-clause fatal in
//	                                                  repoBlockFieldPartition, and
//	                                                  the publication arm next door
//	                                                  refuses the same sentence on
//	                                                  the live schema side
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
					t.Errorf("a repo carrying only %s was refused with %s (%q). Since aihub#588 "+
						"the published description PROMISES this acceptance outright — \"the two "+
						"generation-metadata fields do not trigger that rule\" — so a refusal "+
						"here is no longer just a wording gap, it breaks the stated contract. "+
						"Widening the trigger was the option the owner ruled OUT (it newly "+
						"refuses metadata-only entries existing callers may send); if it is "+
						"being taken up anyway, the descriptions on both tools, the card and "+
						"this arm all have to move in the same change.",
						field, aerr.Code, aerr.Message)
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
