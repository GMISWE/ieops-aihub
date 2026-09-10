package mcp_test

// aihub#543 wave 2, slice L6 — the hop 0-1 and Policy sentences of the four
// memory-mutation cards, held against the live registry and against the domain
// declarations they describe.
//
// Every arm here is a P-family probe in the sense of the spec's §3.1 table: it
// compares card or schema TEXT with a declaration, and it builds the expected
// value from that declaration rather than typing it out. The base-strength
// precedent is the whole reason — an arm that hard-codes "1-5" goes green on the
// day the constant moves, which is the one day it was needed.
//
// Needs no database.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// ─── "N parameters, M required" ──────────────────────────────────────────────

// paramCountWords is the number vocabulary the cards actually write. "all" and
// "both" are absent on purpose: they are resolved against the parameter count
// rather than mapped to a number, because that is what the words mean.
var paramCountWords = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7,
	"eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13,
	"none": 0,
}

// paramCountSentence matches a card's opening arithmetic about its own schema.
//
// 🔴 Anchored to the start of a LINE with a capitalised number word, which is
// what makes it a claim about the tool rather than a phrase. Measured 2026-09-10
// against all 45 cards: the loose form also matches "That is both numeric rules
// honoured on one parameter" (pf_list_work_items) and "there is one parameter"
// (pf_update_work_item) — mid-sentence references to A parameter, not counts of
// them — and reading either as a census reports a correct card as wrong. Two
// false hits out of 40 is not a rounding error here: this arm's whole value is
// that a failure means the card is wrong.
//
// 🔴 The required clause tolerates markdown emphasis and the word "both", and
// that was MEASURED rather than anticipated. Written without them, the pattern
// matched pf_redact_memory's "Two parameters, **both required**" as a bare
// parameter COUNT and silently checked no required count at all — so dropping
// `reason` from that schema's required list was a live mutant this arm answered
// GREEN. The card sentence is the one this slice classifies, which made the gap
// exactly where it mattered least visibly. Widening it took the cards carrying a
// checked required count from 19 to 22.
var paramCountSentence = regexp.MustCompile(
	`(?m)^(One|Two|Three|Four|Five|Six|Seven|Eight|Nine|Ten|Eleven|Twelve|Thirteen) ` +
		`parameters?\b(?:,\s*\*{0,2}(one|two|three|four|five|six|seven|eight|nine|ten|` +
		`eleven|twelve|thirteen|none|all|both)\*{0,2} required)?`)

// floorParamCountCards bounds how many cards this arm found an arithmetic
// sentence in. Measured at 38 of 45 on 2026-09-10; the seven without one are
// cards that open differently, not cards with no parameters (pf_create_work_item
// has 16). A floor, far below the measurement, so it catches the regex having
// stopped matching rather than reporting every legitimate rewording — the floor
// discipline the card gate's own constants state for themselves.
const floorParamCountCards = 25

// unreadRequiredClause is the anti-blind-spot half, and it is a SHAPE check
// rather than a floor.
//
// 🔴 Measured, and the measurement is why it is not a count. Dropping the
// emphasis tolerance from the pattern above takes the cards with a checked
// required count from 22 to 19 while the 38 matches stay 38 — and a floor tuned
// low enough not to fail a legitimate rewording (memoryToolNames' own comment
// records what a tight floor cost: 10 against a measured 12, failing on the first
// correct removal) cannot see a three-card regression. So instead of counting,
// this asks the question directly: if a card's arithmetic sentence states a
// required count in a form the pattern did not capture, say so. That is
// per-card, survives any future phrasing, and reports noise a human resolves
// rather than silence — the direction clampdisclosure states for itself.
var unreadRequiredClause = regexp.MustCompile(
	`(?i)\*{0,2}\b(one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|` +
		`thirteen|none|all|both)\b\*{0,2}\s+required`)

// TestPublishedParamCountSentencesAreTheEnforcedOnes holds every card sentence
// that counts its own parameters against the schema the live registry publishes.
//
// It is named for the CLAIM and not for a tool, per the spec's §3.1 rule and for
// the reason TestPublishedGoalCapIsTheEnforcedOne's own comment records: an arm
// named for one card is the shape where every other card can silently lose the
// property. The two cards this slice owns — pf_redact_memory's "Two parameters,
// both required" and pf_reinforce_memory's "Four parameters, three required" —
// are two rows of 38, and a 46th card arriving is covered the day it is written.
//
// The required half is only asserted when the sentence states it. A card that
// says "Five parameters" and stops has made one claim, and inventing a second
// one to check would fail cards for prose they never wrote.
//
//	M1  pf_redact_memory: "Two parameters" -> "Three parameters"       RED
//	M2  pf_reinforce_memory: "three required" -> "two required"        RED
//	M3  drop `reason` from redactMemorySchema's required list          RED
//	    (the enforcement side: the card is unchanged and now wrong)
//	M4  add a fifth property to reinforceMemorySchema                  RED
//	M5  break the anchor (drop `(?m)^`)                                RED
//	    (two cards then report a mid-sentence phrase as a count)
//	M6  make the regex match nothing                                   RED
//	    (FLOOR_PARAM_COUNT_CARDS — the vacuity arm)
//	M3b drop the emphasis/"both" tolerance from the required clause    RED
//	    (REQUIRED_UNREAD — this was GREEN before that finding existed,
//	    and M3 was green with it: see the pattern's own comment)
//	M3c pf_resolve_commit: "all required" -> "two required"            RED
//	    (another card's row, which is the point of walking all of them)
//	M3d delete the REQUIRED_UNREAD report alone                        green,
//	    expected: the pattern still reads all 22 required counts, so there
//	    is nothing for the report to say
//	M3e drop the tolerance AND delete the report, in one diff           🔴 green
//
// 🔴 M3e is the honest limit of any in-tree recogniser: deleting a recogniser and
// the report of its own blind spot in one diff is invisible to that recogniser.
// What is left is the diff itself and the ledger's Candidates column, which is
// the division of labour the ratchet's own header states — this gate refuses a
// FORM, and whether a signed change is honest is the reviewer's half.
func TestPublishedParamCountSentencesAreTheEnforcedOnes(t *testing.T) {
	cards := readCards(t)
	live := map[string]*sdkmcp.Tool{}
	for _, tool := range publishedToolList(t) {
		live[tool.Name] = tool
	}

	matched, withRequired := 0, 0
	for _, name := range sortedCardNames(cards) {
		c := cards[name]
		m := paramCountSentence.FindStringSubmatch(c.prose)
		if m == nil {
			continue
		}
		tool, published := live[name]
		if !published {
			// K1/K4 own "a card for an unpublished tool"; this arm must not add a
			// second, differently-worded report of the same fact.
			continue
		}
		matched++

		props, required := liveSchemaShape(t, name, tool.InputSchema)
		wantParams := paramCountWords[strings.ToLower(m[1])]
		if wantParams != len(props) {
			t.Errorf("K-L6 PARAM_COUNT: %s opens %q and the live schema publishes %d "+
				"parameter(s) (%v). The sentence is the first thing a reader of the card "+
				"believes about the tool, and the machine block above it is regenerated while "+
				"this line is not — so this is exactly where a schema change leaves a card "+
				"stating the previous shape.", name, m[0], len(props), props)
		}
		if m[2] == "" {
			// Scoped to the rest of that sentence, inside that paragraph: the
			// `| param | type | required |` table header two lines down carries the
			// word too, and reading it as a claim reports 6 correct cards as wrong.
			tail := c.prose[strings.Index(c.prose, m[0])+len(m[0]):]
			if para := strings.SplitN(tail, "\n\n", 2)[0]; true {
				if cut := strings.Index(para, ". "); cut >= 0 {
					para = para[:cut]
				}
				if u := unreadRequiredClause.FindString(para); u != "" {
					t.Errorf("K-L6 REQUIRED_UNREAD: %s states %q and this arm read no required "+
						"count out of it, so the schema's required list is unchecked for this "+
						"card while its parameter count is checked. Teach paramCountSentence the "+
						"phrasing — that is what the emphasised \"**both required**\" form cost "+
						"once already.", name, strings.TrimSpace(m[0]+" "+u))
				}
			}
			continue
		}
		withRequired++
		wantRequired, ok := paramCountWords[strings.ToLower(m[2])]
		if !ok { // "all" / "both"
			wantRequired = len(props)
		}
		if wantRequired != len(required) {
			t.Errorf("K-L6 REQUIRED_COUNT: %s opens %q and the live schema marks %d "+
				"required (%v). Required-ness is what decides whether a caller's omission is "+
				"a 400 or a default, so a card that miscounts it teaches the wrong call.",
				name, m[0], len(required), required)
		}
	}

	if matched < floorParamCountCards {
		t.Errorf("K-L6 FLOOR_PARAM_COUNT_CARDS: this arm found an arithmetic sentence in only "+
			"%d card(s), floor is %d. A pattern that has stopped matching asserts nothing "+
			"about every card it no longer sees, and a walk over zero cards is the same green "+
			"as 45 correct ones. Current value: the K-L6 line below.", matched, floorParamCountCards)
	}
	t.Logf("K-L6: %d of %d cards state their own parameter arithmetic, %d of them a required "+
		"count too", matched, len(cards), withRequired)
}

// liveSchemaShape decodes a published InputSchema into its property names and
// its required names, both sorted.
func liveSchemaShape(t *testing.T, tool string, raw any) (props, required []string) {
	t.Helper()
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("re-marshal %s's schema: %v", tool, err)
	}
	var decoded struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("%s's published schema is not valid JSON: %v", tool, err)
	}
	for name := range decoded.Properties {
		props = append(props, name)
	}
	required = append(required, decoded.Required...)
	sort.Strings(props)
	sort.Strings(required)
	return props, required
}

// ─── pf_activate_memory publishes no scale of its own ────────────────────────

// TestActivateMemoryPublishesNoStrengthScaleOfItsOwn holds the §6.2 T2-19
// sentence on the pf_activate_memory card: this tool moves a memory's effective
// strength and publishes no scale, so the value it moves is interpretable only
// through the two parameters the ruling is about.
//
// 🔴 The positive control is the whole arm. "This description mentions no
// numbers" is trivially true of an empty string and of a tool that was never
// registered, so the same walk requires pf_recall's `min_strength` and
// pf_remember's `base_strength` to state the range built from the domain
// constants. Without that half, deleting every description in the file would
// make this greener rather than redder — which is the vacuity the spec's §3.4
// floor rule exists for, applied to a negative claim.
//
//	M7   add "(1-5)" to pf_activate_memory's description        RED
//	M8   add a `min_strength`-style numeric param to
//	     activateMemorySchema                                   RED
//	M9   strip the range from pf_remember's base_strength       RED (control)
//	M10  point the control at a tool that does not exist        RED
func TestActivateMemoryPublishesNoStrengthScaleOfItsOwn(t *testing.T) {
	live := map[string]*sdkmcp.Tool{}
	for _, tool := range publishedToolList(t) {
		live[tool.Name] = tool
	}

	activate, ok := live["pf_activate_memory"]
	if !ok {
		t.Fatal("pf_activate_memory is not published at all, so this arm has no subject")
	}

	// The whole published surface: the description plus every parameter
	// description. A check on the tool description alone would miss a scale
	// arriving on a parameter, which is where every other tool's scale lives.
	surface := []string{activate.Description}
	props, _ := liveSchemaShape(t, "pf_activate_memory", activate.InputSchema)
	for _, name := range props {
		desc, found := publishedParamDescription(t, mustSchemaJSON(t, activate.InputSchema), name)
		if !found {
			t.Errorf("pf_activate_memory publishes %q with no description at all", name)
			continue
		}
		surface = append(surface, desc)
	}

	// The bounds this tool must not restate, built from the constants so that a
	// bound which moves cannot leave this arm looking for a stale string.
	bounds := []string{
		trimZero(domain.MinBaseStrength), trimZero(domain.MaxBaseStrength),
		trimZero(domain.DefaultBaseStrength),
	}
	for _, text := range surface {
		for _, n := range integersIn(text) {
			for _, b := range bounds {
				if n == b {
					t.Errorf("pf_activate_memory's published surface carries %q in %q. The card "+
						"says this tool publishes no strength scale of its own — if it starts to, "+
						"there are then two places stating the range and the card's account of why "+
						"the value is only interpretable elsewhere is false.", n, text)
				}
			}
		}
	}

	// The control: the two surfaces that DO publish the scale still do.
	wantRange := trimZero(domain.MinBaseStrength) + "-" + trimZero(domain.MaxBaseStrength)
	for _, tc := range []struct{ tool, param string }{
		{"pf_remember", "base_strength"},
		{"pf_recall", "min_strength"},
	} {
		tool, ok := live[tc.tool]
		if !ok {
			t.Errorf("%s is not published, so the control for this arm asserts nothing — a "+
				"missing control is how a negative claim goes vacuously green", tc.tool)
			continue
		}
		desc, found := publishedParamDescription(t, mustSchemaJSON(t, tool.InputSchema), tc.param)
		if !found {
			t.Errorf("%s no longer publishes %s, so nothing states the scale that "+
				"pf_activate_memory's value is supposed to be interpretable through",
				tc.tool, tc.param)
			continue
		}
		if tc.param == "base_strength" && !strings.Contains(desc, wantRange) {
			t.Errorf("%s publishes %s as %q, which no longer states the %s scale. The "+
				"pf_activate_memory card's claim rests on this being the place a caller learns "+
				"it.", tc.tool, tc.param, desc, wantRange)
		}
		if tc.param == "min_strength" && !strings.Contains(desc, "base_strength") {
			t.Errorf("%s publishes %s as %q, which no longer names the scale it thresholds",
				tc.tool, tc.param, desc)
		}
	}
}

// mustSchemaJSON re-marshals a published InputSchema so the existing
// publishedParamDescription helper can read it.
func mustSchemaJSON(t *testing.T, raw any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("re-marshal schema: %v", err)
	}
	return b
}

// trimZero renders a bound the way a description writes it: 1, not 1.0.
func trimZero(v float64) string {
	s := strings.TrimRight(strings.TrimRight(formatFloat(v), "0"), ".")
	if s == "" {
		return "0"
	}
	return s
}

func formatFloat(v float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ─── the three event-type sets pf_redact_memory compares ─────────────────────

// eventSetSizeSentence matches "the N-entry <name> set/whitelist" in a card.
//
// 🔴 `\s+` rather than a literal space, and the card prose is whitespace-collapsed
// before it runs. Measured: the first version used literal spaces and missed
// "the 7-entry admin\n  whitelist" entirely, because markdown wraps prose wherever
// the column runs out — so the arm reported the card as stating ONE set size when
// it stated two, and the second number (the wrong one) went unchecked. Same
// lesson TestBaseStrengthBoundsAreTheColumnsOwn states for the DDL it reads:
// reflowing a file must not silently turn a gate off.
var eventSetSizeSentence = regexp.MustCompile(`(\d+)-entry\s+([a-z][a-z -]*?)(?:\s+set|\s+whitelist)`)

// collapseSpace renders card prose as one line, so an assertion about a sentence
// is about the sentence rather than about where the paragraph happened to wrap.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// TestPublishedAdminEventSetSizesAreTheEnforcedOnes holds the pf_redact_memory
// card's arithmetic about the three event-type sets `admin_redact` sits in.
//
// ─── Why this arm exists ─────────────────────────────────────────────────────
//
// The card's numbers came from the aihub#411 audit table, which measured them
// BEFORE aihub#444, and the card kept the pre-fix text. Two of its three claims
// were measurably false on 2026-09-10: the whitelist is 8 entries and not 7,
// because aihub#444 made it DERIVED (AdminOnlyEventTypes ∪
// adminAlsoPermittedWithFlag); and `admin_gc_manual` is in both sets and not
// "the first and not the second", which is the flag inversion that work item
// removed. The card is corrected rather than pinned, and this is the arm that
// stops the same drift happening again — the sizes are read from `len()`, so
// the card cannot state a number the code does not.
//
// It anchors on the card because that is the artefact that rotted. The code side
// already has TestEventTypes_AdminOnlyIsASubsetOfTheAdminWhitelist for the
// containment; nothing held the card's account of it.
//
//	M11  restore "7-entry" in the card                        RED
//	M12  restore "4-entry" as "5-entry"                       RED
//	M13  add a fifth entry to AdminOnlyEventTypes             RED (enforcement
//	                                                          side: the card is
//	                                                          unchanged and now
//	                                                          wrong)
//	M14  drop admin_redact from AdminOnlyEventTypes           RED (membership)
//	M15  make adminAlsoPermittedWithFlag empty                RED (the two sets
//	                                                          then coincide and
//	                                                          the non-nesting
//	                                                          pair is gone)
//	M16  delete the card sentence entirely                    RED
//	                                                          (SET_SIZE_UNSTATED)
func TestPublishedAdminEventSetSizesAreTheEnforcedOnes(t *testing.T) {
	cards := readCards(t)
	c, ok := cards["pf_redact_memory"]
	if !ok {
		t.Fatal("docs/mcp-cards/pf_redact_memory.md is gone; this arm's subject is the card")
	}

	// The sets, by the name the card uses for each.
	sizes := map[string]int{
		"admin-only": len(domain.AdminOnlyEventTypes),
		"admin":      len(domain.AdminEventWhitelist),
		"null-work-item": func() int {
			return len(domain.NullWorkItemEventTypes)
		}(),
	}

	found := map[string]bool{}
	for _, m := range eventSetSizeSentence.FindAllStringSubmatch(collapseSpace(c.prose), -1) {
		label := strings.TrimSpace(m[2])
		want, known := sizes[label]
		if !known {
			t.Errorf("K-L6 SET_SIZE_UNKNOWN: pf_redact_memory states a %q-entry %q set and this "+
				"arm does not know which declaration that names, so the number is unchecked. "+
				"Either the card renamed a set (rename it here too) or it invented one.",
				m[1], label)
			continue
		}
		found[label] = true
		if m[1] != itoa(want) {
			t.Errorf("K-L6 SET_SIZE: pf_redact_memory calls it the %s-entry %s set and the "+
				"declaration holds %d. This number was 7 against a measured 8 until "+
				"aihub#543 wave 2 — it was copied from the aihub#411 audit's PRE-aihub#444 "+
				"measurement and never re-read.", m[1], label, want)
		}
	}
	if len(found) < 2 {
		t.Errorf("K-L6 SET_SIZE_UNSTATED: pf_redact_memory states the size of only %d of the "+
			"sets this arm checks (%v). The card's whole §6.2 T2-5 comparison is about the "+
			"three sets differing, so a card that stopped stating the sizes would leave this "+
			"arm walking nothing.", len(found), found)
	}

	// Membership: admin_redact is in all three, which is what makes it the
	// comparison the card uses.
	for _, tc := range []struct {
		label string
		set   []string
	}{
		{"admin-only", domain.AdminOnlyEventTypes},
		{"admin whitelist", domain.AdminEventWhitelist},
		{"null-work-item", domain.NullWorkItemEventTypes},
	} {
		if !contains(tc.set, "admin_redact") {
			t.Errorf("admin_redact has left the %s set, and the card's comparison rests on it "+
				"being in all three", tc.label)
		}
	}

	// The non-nesting pair, in both directions. The card used to name
	// admin_gc_manual for this and aihub#444 closed it — AdminOnlyEventTypes is
	// now a subset of the whitelist by construction. The live pair is between the
	// whitelist and the null-work-item CHECK mirror, and asserting BOTH
	// directions is what makes "overlap without nesting" a checked claim rather
	// than a restatement of whichever difference happens to exist.
	whitelistOnly, nullOnly := "", ""
	for _, typ := range domain.AdminEventWhitelist {
		if !contains(domain.NullWorkItemEventTypes, typ) {
			whitelistOnly = typ
			break
		}
	}
	for _, typ := range domain.NullWorkItemEventTypes {
		if !contains(domain.AdminEventWhitelist, typ) {
			nullOnly = typ
			break
		}
	}
	if whitelistOnly == "" || nullOnly == "" {
		t.Errorf("the admin whitelist and the null-work-item set have started to nest "+
			"(whitelist-only=%q, null-only=%q). The card says membership in one set implies "+
			"nothing about another, and that sentence needs a live pair to be about — if the "+
			"sets now nest, the card is what has to change, not this arm.",
			whitelistOnly, nullOnly)
	}
}

func contains(set []string, want string) bool {
	for _, v := range set {
		if v == want {
			return true
		}
	}
	return false
}

// ─── the corpus citation in pf_reinforce_memory's hop 5 ──────────────────────

// TestReinforceCardCorpusCitationResolvesInTheAudit holds the one claim in the
// reinforce card that points at a file rather than at code: the aihub#412 error
// taxonomy records this tool's 400, verbatim.
//
// K6 does not cover it. Both of that arm's anchor patterns require a `.go`
// suffix, so a backticked path under docs/ resolves nowhere and a citation that
// rotted would read as prose. The quote is what makes the sentence worth
// checking — "the audit mentions this tool" would stay true of a file that had
// stopped recording the error.
//
//	M17  point the card at docs/audits/gone.md                RED (path)
//	M18  change one word of the quoted error in the audit     RED (quote)
//
// The two are not independent, and that is recorded rather than hidden: a missing
// file also fails the quote check, so M17 would be red with the existence
// assertion deleted. It is kept because the failure TEXT differs — "cited and
// unreadable" sends a reader to the card, "readable and reworded" sends them to
// the audit — and a probe whose message names the wrong file is a probe somebody
// deletes.
func TestReinforceCardCorpusCitationResolvesInTheAudit(t *testing.T) {
	cards := readCards(t)
	c, ok := cards["pf_reinforce_memory"]
	if !ok {
		t.Fatal("docs/mcp-cards/pf_reinforce_memory.md is gone")
	}

	const wantPath = "docs/audits/aihub-412-corpus-facts/error-taxonomy.md"
	if !strings.Contains(c.prose, "`"+wantPath+"`") {
		t.Fatalf("pf_reinforce_memory no longer cites %s. This arm is that citation's only "+
			"resolver — K6's anchor patterns both require a .go suffix — so if the card now "+
			"points somewhere else, point this arm there too rather than leaving the new "+
			"path unchecked.", wantPath)
	}

	audit, err := os.ReadFile(filepath.Join(cardsRepoRoot, wantPath))
	if err != nil {
		t.Fatalf("pf_reinforce_memory cites %s and it cannot be read: %v", wantPath, err)
	}

	// The quote, compared against whitespace-collapsed text on BOTH sides: the
	// card wraps it mid-sentence and the audit may wrap it differently, and an
	// arm that only matches one layout is off whenever either file is reflowed.
	const quote = "work_item_id is required when attempt_id/session_secret are provided"
	prose := collapseSpace(c.prose)
	if !strings.Contains(prose, quote) {
		t.Fatalf("pf_reinforce_memory no longer quotes %q, so there is nothing to hold the "+
			"audit against", quote)
	}
	if !strings.Contains(collapseSpace(string(audit)), quote) {
		t.Errorf("%s does not carry %q. The card presents it as verbatim from that file, "+
			"which is the strongest form the claim has — an audit that has been rewritten "+
			"leaves the card asserting a measurement nobody can find.", wantPath, quote)
	}
	if !strings.Contains(string(audit), "pf_reinforce_memory") {
		t.Errorf("%s no longer names pf_reinforce_memory, so the card's \"against this "+
			"tool\" is unsupported even if the error string is still there", wantPath)
	}
}
