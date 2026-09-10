package mcp

// aihub#543 probe wave 2, lane L5 — the four COUNTS
// `docs/mcp-cards/pf_emit_event.md` states in prose, held against the sets they
// count:
//
//	"Five parameters, and `event_type` is the one this card exists to carry"
//	"names the 45 types this tree can produce"
//	"`AdminOnlyEventTypes` (4) … `AdminEventWhitelist` (8) …
//	 `NullWorkItemEventTypes` (22)"
//	    -> TestPublishedEventTypeCountsAreTheEnforcedOnes
//
// ─── Why a card number needs its own arm ───────────────────────────────────
//
// The existing arms hold the CONTENTS of these sets: event_types_test.go holds
// the three enforced sets to each other and to migration 0036's CHECK, and
// tools_events_vocab_test.go holds every EventVocabulary member to the published
// `event_type` description. None of them can see a number written in the card's
// prose. Add a 46th event type and every one of those stays green while the card
// says 45 — a wrong number in the one document a reader consults to avoid
// reading the code, which is the aihub#543 defect class exactly.
//
// 🔴 The expected substring is BUILT from len(), never written out. That is the
// TestPublishedBaseStrengthRangeIsTheEnforcedOne precedent and its stated
// reason: an arm carrying the literal "45" goes green on the day the set moves,
// which is the one day it was needed. Here it means both directions are red —
// the set changing without the card, and the card changing without the set.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run TestPublishedEventTypeCounts -count=1

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// countWords spells the small integers a card writes as words. Bounded at ten
// because a parameter list longer than that is not something a card opens by
// counting, and an unbounded speller would be a second implementation of
// English rather than a lookup for one sentence.
var countWords = map[int]string{
	1: "One", 2: "Two", 3: "Three", 4: "Four", 5: "Five",
	6: "Six", 7: "Seven", 8: "Eight", 9: "Nine", 10: "Ten",
}

// TestPublishedEventTypeCountsAreTheEnforcedOnes holds the pf_emit_event card's
// four stated counts against the schema and the three domain sets.
//
// Each assertion is a substring the arm ASSEMBLES from a measurement, so the
// failure prints the sentence to write rather than leaving the reader to derive
// it. The parameter count is spelled as a word because that is how the card's
// opening sentence writes it; the other three are numerals, because that is how
// the card writes them, and matching the card's own spelling is what makes these
// arms about the sentence a reader sees rather than about a number that happens
// to appear somewhere in an 8 kB file.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M9   enforcement: append one entry to domain.EventVocabulary
//	                                            RED  the vocabulary-count arm
//	                                                 (and TestEventVocabulary_*
//	                                                  stays GREEN, which is why
//	                                                  this arm exists)
//	M10  enforcement: append one entry to domain.AdminOnlyEventTypes
//	                                            RED  the admin-only and the
//	                                                 whitelist arms, because the
//	                                                 whitelist is DERIVED
//	M11  publication: change the card's "45 types" to "44 types"
//	                                            RED  the vocabulary-count arm
//	M12  publication: change the card's "Five parameters" to "Six parameters"
//	                                            RED  the parameter-count arm
//	M13  publication: delete this arm's citation from all three card sentences
//	                                            RED  K12 — the first of the three
//	                                                 names no other arm, so it
//	                                                 lands back in the debt column
//	                                                 and the pf_emit_event ledger
//	                                                 row stops matching
func TestPublishedEventTypeCountsAreTheEnforcedOnes(t *testing.T) {
	card := readEmitEventCard(t)

	// FLOOR. Every assertion below is a Contains against this text, so a short
	// read — a renamed file answered with an empty string, a truncated fetch —
	// would fail them all for the wrong reason, and a reader's repair would be
	// to delete the arm. Failing here says which of the two it is.
	if len(card) < 2000 {
		t.Fatalf("the pf_emit_event card read back as %d bytes; every assertion below is a "+
			"substring of it, so a short read fails them all and says nothing about the counts",
			len(card))
	}

	// Anti-vacuity: an empty set makes its Contains trivially satisfiable by a
	// card that states a count of zero, and all three are read from one file.
	if len(domain.EventVocabulary) == 0 || len(domain.AdminOnlyEventTypes) == 0 ||
		len(domain.AdminEventWhitelist) == 0 || len(domain.NullWorkItemEventTypes) == 0 {
		t.Fatalf("domain publishes %d event types and sets of %d/%d/%d — one vocabulary is empty, "+
			"and a count arm over an empty set asserts nothing",
			len(domain.EventVocabulary), len(domain.AdminOnlyEventTypes),
			len(domain.AdminEventWhitelist), len(domain.NullWorkItemEventTypes))
	}

	params := emitEventPublishedParamCount(t)
	word, ok := countWords[params]
	if !ok {
		t.Fatalf("pf_emit_event publishes %d parameters, which countWords does not spell — extend "+
			"the table in the same diff that grows the tool, or the count sentence stops being "+
			"checked", params)
	}

	for _, tc := range []struct {
		what string
		want string
		why  string
	}{
		{
			what: "the parameter count",
			want: word + " parameters",
			why: "the card's opening sentence counts the published parameters, and the live " +
				"InputSchema is the only thing that decides that number",
		},
		{
			what: "the vocabulary size",
			want: fmt.Sprintf("%d types", len(domain.EventVocabulary)),
			why: "hop 0-1 says the description names this many types; nothing else in the tree " +
				"compares that number to domain.EventVocabulary, so adding one leaves every " +
				"other arm green",
		},
		{
			what: "the admin-only set size",
			want: fmt.Sprintf("`AdminOnlyEventTypes` (%d)", len(domain.AdminOnlyEventTypes)),
			why:  "hop 4 states the size beside the name, which is what a reader counts against",
		},
		{
			what: "the admin whitelist size",
			want: fmt.Sprintf("`AdminEventWhitelist` (%d)", len(domain.AdminEventWhitelist)),
			why: "the whitelist is DERIVED from the admin-only set plus four, so its number moves " +
				"whenever the other one does — and a card stating both can be half right",
		},
		{
			what: "the null-work-item set size",
			want: fmt.Sprintf("`NullWorkItemEventTypes` (%d)", len(domain.NullWorkItemEventTypes)),
			why: "this one mirrors a CHECK, so the card's number is also a claim about the " +
				"migration TestEventTypes_NullWorkItemMirrorsTheMigration holds it equal to",
		},
	} {
		if !strings.Contains(card, tc.want) {
			t.Errorf("docs/mcp-cards/pf_emit_event.md does not state %s as %q.\n%s\nWrite the "+
				"measured value into the sentence; do not adjust it by arithmetic.",
				tc.what, tc.want, tc.why)
		}
	}
}

// readEmitEventCard returns the pf_emit_event card verbatim.
func readEmitEventCard(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "docs", "mcp-cards", "pf_emit_event.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v — the card is the subject of this arm, so a missing file is a "+
			"failure and not an empty pass", path, err)
	}
	return string(raw)
}

// emitEventPublishedParamCount is how many properties pf_emit_event's live
// InputSchema declares.
func emitEventPublishedParamCount(t *testing.T) int {
	t.Helper()
	var decoded struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(emitEventSchema(), &decoded); err != nil {
		t.Fatalf("pf_emit_event InputSchema is not valid JSON: %v", err)
	}
	if len(decoded.Properties) == 0 {
		t.Fatal("pf_emit_event publishes no properties at all; a count arm over an empty schema " +
			"would agree with any sentence")
	}
	return len(decoded.Properties)
}
