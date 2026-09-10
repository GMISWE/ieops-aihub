package mcp_test

// aihub#543 phase 1b — the cross-card uniqueness census behind
// `docs/mcp-cards/pf_force_takeover.md`'s hop 0-1 sentence:
//
//	"\"No flag displaces another work item's lock, **except in a narrow
//	 commit-window race**\" lives here and only here"
//	    -> TestTakeoverRaceQualifierIsUniqueAcrossThePublishedSurface
//
// force_takeover_published_word_test.go holds one half of "and only here": the
// qualifier is on this tool's description and absent from the ONE other place it
// ever lived, pf_claim_work_item's `force_takeover` flag (aihub#430 deleted it
// there). That arm compares two NAMED strings, so on the day someone copies the
// clause onto a third description — a new tool's, a parameter's, an enum value —
// it stays green. "Only here" is quantified over every string the toolset
// publishes, and until this census no arm owned that population; the pf_force_takeover
// card carried the gap as its one pending-implementation row, filed by the
// aihub#543 §8 Q5 ruling (2026-09-10) which deferred the census to phase 1b.
// This file is phase 1b landing, and the marker goes in the same change.
//
// 🔴 Census, not a second pair of lookups. The population is derived from the
// TREE — the docs/mcp-cards roster — and checked both ways against the live
// session before anything is asserted about the clause, because a census scoped
// more narrowly than the claim it holds is a census that agrees with anything
// (the request_adjusted arm records the same rule). Per tool it walks the
// description plus every string value in the serialised InputSchema — the same
// two string families liveSchemaProse reads and K3's second hash covers, so
// "published" means here what it means to the rest of this file's gates.
//
// The needle is read OUT OF THE CARD (takeoverCardQuote), not written down here,
// for the reason that file records: a literal in the test goes green on the day
// the published wording moves, which is the one day it was needed.
//
// MUTANTS (run against this tree; the verdict is what happened, not what was
// expected):
//
//	M1 enforcement: re-add ", except in a narrow commit-window race (aihub#410)"
//	   to pf_claim_work_item's `force_takeover` prop — the copy-back
//	   internal/mcp/tools_lifecycle.go's own comment forbids
//	                                              RED  the_clause_lives_on_exactly_one_published_string
//	                                                   (two carriers named, the
//	                                                   planted one at
//	                                                   pf_claim_work_item schema)
//	M2 publication: drop the census citation from the card's sentence, keeping
//	   its `pf_force_takeover` backtick so the sentence stays in the population
//	                                              RED  K12 DEBT_GROWTH on the
//	                                                   pf_force_takeover ledger row
//	                                                   (Cited 18 -> 17,
//	                                                   Unclassified 0 -> 1)
//	M2a publication: drop the citation AND every backtick from the sentence
//	                                              RED  K12 POPULATION_MOVED
//	                                                   (Candidates 21 -> 20 — the
//	                                                   sentence left the population
//	                                                   instead of becoming debt, and
//	                                                   the ledger's equality catches
//	                                                   that direction too)
//	M3 detection:   empty the census needle so every string matches
//	                                              RED  a_synthetic_duplicate_trips_the_census
//	                                                   (4 hits against a fixture
//	                                                   holding 2), and the guard
//	                                                   after it stops the live
//	                                                   subtests from reading noise
//	G1 control:     the clean tree                GREEN
//
// The synthetic-duplicate fixture below is the standing calibration for the
// detection itself — it runs on every tree, where M1 and M3 ran once.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run TestTakeoverRaceQualifierIsUnique -count=1

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// publishedString is one string the live toolset publishes: a tool's
// description, or one string VALUE from its serialised InputSchema —
// per-parameter descriptions, enum values, titles. Property names are excluded
// for liveSchemaProse's reason: K3 already checks every parameter name exactly,
// and a clause cannot be a JSON key.
type publishedString struct {
	tool string
	kind string // "description" | "schema"
	text string
}

func (p publishedString) describe() string {
	return fmt.Sprintf("%s %s: %q", p.tool, p.kind, truncatePublished(p.text))
}

// truncatePublished keeps a failure line readable; rune-sliced so a multibyte
// character is never cut in half.
func truncatePublished(s string) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) > 140 {
		return string(r[:140]) + " …"
	}
	return string(r)
}

// foldPublished collapses whitespace and case, so a duplicate cannot hide behind
// a line wrap or a recased word. The same collapse takeoverCardQuote applies to
// the card's side of the comparison.
func foldPublished(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// clauseCarriers is the census: every published string carrying the clause,
// after folding. Pure over its inputs so the fixture below can calibrate it on a
// population whose right answer is already known.
func clauseCarriers(clause string, surface []publishedString) []publishedString {
	needle := foldPublished(clause)
	var hits []publishedString
	for _, p := range surface {
		if strings.Contains(foldPublished(p.text), needle) {
			hits = append(hits, p)
		}
	}
	return hits
}

// publishedSurface reads every published string off a live session — the same
// instrument liveInputSchemaHashes and liveSchemaProse use, not a third registry
// reader.
func publishedSurface(t *testing.T) []publishedString {
	t.Helper()
	_, tools := newContractGate(t)
	var out []publishedString
	for _, tool := range tools {
		out = append(out, publishedString{tool: tool.Name, kind: "description", text: tool.Description})
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal %s InputSchema: %v", tool.Name, err)
		}
		var decoded any
		if err := json.Unmarshal(b, &decoded); err != nil {
			t.Fatalf("re-decode %s InputSchema: %v", tool.Name, err)
		}
		for _, s := range jsonStringValues(decoded) {
			out = append(out, publishedString{tool: tool.Name, kind: "schema", text: s})
		}
	}
	return out
}

// TestTakeoverRaceQualifierIsUniqueAcrossThePublishedSurface is the census.
//
// Four assertions in a fixed order: the census is calibrated on a fixture whose
// answer is known BEFORE anything trusts it against the live surface (the
// anti-vacuity order TestWiringPinSkeletonIsCalibrated establishes); the
// population is the whole card roster in both directions; the qualifier has
// exactly one carrier and it is this tool's description; and the census provably
// reads BOTH string families, held by a statement published on two tools by
// design.
func TestTakeoverRaceQualifierIsUniqueAcrossThePublishedSurface(t *testing.T) {
	t.Run("a_synthetic_duplicate_trips_the_census", func(t *testing.T) {
		fixture := []publishedString{
			{tool: "pf_alpha", kind: "description", text: "Says something else entirely."},
			{tool: "pf_alpha", kind: "schema", text: "carries the planted clause here"},
			{tool: "pf_beta", kind: "description",
				text: "Carries   THE PLANTED\nclause here too, recased and rewrapped."},
			{tool: "pf_gamma", kind: "schema", text: "clause-free"},
		}
		hits := clauseCarriers("the planted clause", fixture)
		if len(hits) != 2 {
			t.Fatalf("the census found %d carrier(s) of a clause planted in exactly 2 fixture "+
				"strings — a count that cannot find a duplicate here would answer \"unique\" "+
				"about a live duplicate too, so nothing below may run on it", len(hits))
		}
		if hits[0].tool != "pf_alpha" || hits[0].kind != "schema" ||
			hits[1].tool != "pf_beta" || hits[1].kind != "description" {
			t.Errorf("the census misattributed the planted duplicates: got %s and %s — a hit "+
				"blamed on the wrong string sends the fix to the wrong file",
				hits[0].describe(), hits[1].describe())
		}
		if n := len(clauseCarriers("appears nowhere in the fixture", fixture)); n != 0 {
			t.Errorf("the census found %d carrier(s) of a clause planted in none — a scan that "+
				"hits on absent text would call every clause a duplicate", n)
		}
	})
	if t.Failed() {
		t.Fatal("the census failed its own calibration; the live readings below would be noise")
	}

	whole, clause := takeoverCardQuote(t)
	surface := publishedSurface(t)

	t.Run("the_population_is_the_whole_card_roster", func(t *testing.T) {
		cards := readCards(t)
		if len(cards) != k12ContractCards {
			t.Errorf("docs/mcp-cards holds %d card(s) and k12ContractCards says %d — the roster "+
				"this census derives its population from has drifted, so \"every published "+
				"description\" below would quantify over the wrong set", len(cards), k12ContractCards)
		}
		type families struct{ desc, schema int }
		perTool := make(map[string]families, len(cards))
		for _, p := range surface {
			f := perTool[p.tool]
			switch p.kind {
			case "description":
				f.desc++
			case "schema":
				f.schema++
			}
			perTool[p.tool] = f
		}
		for name := range cards {
			f, live := perTool[name]
			if !live {
				t.Errorf("%s has a card and publishes nothing this census read — a tool outside "+
					"the population is a place the clause could live unseen", name)
				continue
			}
			if f.desc == 0 || f.schema == 0 {
				t.Errorf("%s contributed %d description string(s) and %d schema string(s) — a "+
					"tool missing a whole family is the census scanning less than the tool "+
					"publishes", name, f.desc, f.schema)
			}
		}
		for tool := range perTool {
			if _, carded := cards[tool]; !carded {
				t.Errorf("the session publishes %s, which has no card — K1 owns that failure, "+
					"but this census must still report it because a card-derived population "+
					"would silently skip the tool", tool)
			}
		}
	})

	t.Run("the_clause_lives_on_exactly_one_published_string", func(t *testing.T) {
		hits := clauseCarriers(clause, surface)
		if len(hits) != 1 {
			lines := make([]string, 0, len(hits))
			for _, h := range hits {
				lines = append(lines, "    "+h.describe())
			}
			t.Fatalf("the qualifier %q is carried by %d published string(s), and the card says "+
				"exactly one:\n%s\nZERO means the published wording moved out from under the "+
				"card's quote — force_takeover_published_word_test.go reddens the same day, and "+
				"the two must be fixed together. TWO or more means \"lives here and only here\" "+
				"is false as published: aihub#430 measured why the same sentence is untrue on "+
				"the claim path (SERIALIZABLE there, READ COMMITTED here), so a copy is not a "+
				"clarification, it is a wrong guarantee on whichever tool now carries it. "+
				"Delete the copy rather than re-pinning this census.",
				clause, len(hits), strings.Join(lines, "\n"))
		}
		if hits[0].tool != "pf_force_takeover" || hits[0].kind != "description" {
			t.Errorf("the qualifier's one carrier is %s, and the card says this tool's own "+
				"description. One occurrence somewhere else is the clause having MOVED, not "+
				"having stayed unique.", hits[0].describe())
		}
	})

	t.Run("the_census_reads_descriptions_and_schema_prose", func(t *testing.T) {
		shared := strings.TrimRight(strings.TrimSpace(strings.Replace(whole, clause, "", 1)), " ,")
		if len(shared) < 20 {
			t.Fatalf("removing %q from %q leaves %q — with the two halves indistinguishable "+
				"this control would search for the clause itself and prove nothing", clause,
				whole, shared)
		}
		var takeoverDesc, claimSchema bool
		for _, h := range clauseCarriers(shared, surface) {
			if h.tool == "pf_force_takeover" && h.kind == "description" {
				takeoverDesc = true
			}
			if h.tool == "pf_claim_work_item" && h.kind == "schema" {
				claimSchema = true
			}
		}
		if !takeoverDesc || !claimSchema {
			t.Errorf("the aihub#393 statement %q is published on pf_force_takeover's description "+
				"and on pf_claim_work_item's `force_takeover` flag by design, and the census saw "+
				"it on [description=%v schema=%v]. A family it cannot see is a family the clause "+
				"could hide in, which would make the exactly-one answer above vacuous on that "+
				"side.", shared, takeoverDesc, claimSchema)
		}
	})
}
