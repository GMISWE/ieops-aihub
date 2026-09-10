// Package cardclaims is the aihub#543 wave-0 ratchet: it enumerates the prose
// sentences of a contract card, decides by FORM which of them assert something a
// test could hold, and reports every such sentence that is neither cited to an arm
// nor carrying a named, dated classification marker.
//
// ─── What this package is for, and what it deliberately is not ─────────────
//
// The gap aihub#543 was filed on: structural gates cannot check "behaviour =
// description". Two consecutive waves (2026-09-08, 2026-09-09) produced 30+ false
// card statements and one behavioural regression, and the gate count on all of
// them was zero — every one was caught by a human re-reading prose.
//
// The counter-shape already exists in this tree: a handful of one-off probes that
// each encode ONE published claim as an executable assertion
// (TestPublishedGoalCapIsTheEnforcedOne, TestDeLockingPredictReportsAdvisoryEntries,
// the aihub#495 publication gates). Extrapolated over all 45 cards, the repo holds
// on the order of 300 assertable-and-unprobed claims, so a plan that proposes 300
// probes is a plan that does not finish.
//
// 🔴 So the deliverable that scales is THIS — the ratchet and the ledger — not the
// probes. The probes are what a wave budget buys inside a frame this holds. Built
// second, a wave lands ~50 probes and the other ~250 claims stay exactly as
// invisible as they are today. That is why it ships alone, ahead of every probe
// wave (aihub#543 attrs.owner_annotations_2026_09_10, Q7).
//
// ⚠️ It decides nothing about whether a sentence is TRUE. That is the review half
// (polyforge-scenario#20) and it is K11's stated limit too: refusing the form in
// which a false claim is unfalsifiable is strictly weaker, and strictly cheaper,
// than refusing a false claim.
//
// ─── The three states a sentence can be in ─────────────────────────────────
//
//	assertable + probed    the sentence CITES ITS ARM in its own prose, in the
//	                       semantic-anchor form K6 already resolves. No ledger
//	                       row: a ledger holding 40% of 995 sentences as pointers
//	                       is a second copy of the cards, and a second copy's only
//	                       failure mode is disagreeing with the first.
//	assertable + unprobed  an in-card `probe-waiver` marker carrying a Kind, a
//	                       date, a citation and a reason. THIS IS DEBT.
//	not assertable         an in-card `prose-only` marker carrying a `because`
//	                       from a closed six-value vocabulary. Not debt — a
//	                       waiver says "this should be probed and is not", a
//	                       prose-only row says "there is nothing here to probe",
//	                       and filing ~250 of the second in with the first buries
//	                       the live debt under bookkeeping.
//
// Anything else is UNCLASSIFIED, which is the grandfathering escape the ceilings
// in the gate hold at its measured value. Wave 0 classifies exactly one sentence
// and grandfathers the rest; probe waves drive the unclassified count to 0.
//
// ─── Where the ledger lives, and why it is not in Go ───────────────────────
//
// The owner ruled Q4 on 2026-09-10: in-card markers, with only the numeric
// ceilings in Go. A PER-SENTENCE ledger in Go is a partial second copy of the
// cards' prose; an inline marker — the shape K9's `<!-- historical -->` already
// uses — puts the classification in the diff a reviewer reads, and leaves exactly
// one number per card per column for the gate to check.
package cardclaims

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// ─────────────────────────── the marker vocabulary ───────────────────────────

// WaiverKind is what a `probe-waiver` marker claims about the sentence it sits on.
//
// 🔴 Four kinds rather than one label, for the reason aihub#411 T1-12 §6.1 gives
// and internal/citest/clampdisclosure encodes: recording different kinds of
// exemption under one label loses exactly the fact that told them apart. The
// floor was "distinguish prose cannot be asserted from not yet implemented"; the
// second splits three ways, and known-defect in particular cannot be collapsed
// into pending-implementation — a probe pinning today's wrong answer arrives RED
// on the day of the fix, and the cheapest compliant path is then deleting it.
type WaiverKind string

const (
	// KindPendingImplementation is assertable, and nobody has written the probe.
	// This is the debt that must ratchet down. It names the work item carrying it.
	KindPendingImplementation WaiverKind = "pending-implementation"
	// KindKnownDefect describes behaviour the repo has MEASURED and does not want
	// pinned, because pinning it makes the eventual fix red. It names the work item
	// carrying the fix.
	KindKnownDefect WaiverKind = "known-defect"
	// KindStructurallyUnreachable is assertable in principle but not from a test in
	// this repo — it needs a real GitHub, a second machine, a process the harness
	// cannot create. Provisional by nature: it closes when the harness appears, the
	// way the six git tools' K10 gap closed. It names what is missing.
	KindStructurallyUnreachable WaiverKind = "structurally-unreachable"
	// KindAcceptedUnprobed is an owner decision that this claim is not worth a
	// probe. It is not waiting for anything, and it names the ruling.
	KindAcceptedUnprobed WaiverKind = "accepted-unprobed"
)

// 🔴 Four, and there is deliberately no fifth for "found but not yet ruled on".
// clampdisclosure carries a KindPendingAdjudication for exactly that state and
// this package does not, because under the owner's Q2 ruling (2026-09-10, attempt
// full coverage) a found-and-unruled claim already has a home: it is
// `pending-implementation`, assertable and unwritten. The one claim that genuinely
// needed adjudication — the pf_predict_conflicts rule-1 self-report, aihub#543 §8
// Q3 — was ruled the same day, `known-defect` with the fix on aihub#564. Keeping a
// kind that would hold zero rows and no live need is the stale-exemption shape K5
// exists to refuse; adding it back is a signed diff on the day something needs it.

// WaiverKinds is the closed set, in the order a reader should meet them.
var WaiverKinds = []WaiverKind{
	KindPendingImplementation,
	KindKnownDefect,
	KindStructurallyUnreachable,
	KindAcceptedUnprobed,
}

// Describe is the kind's own account of what it claims, used in failure text so a
// reader sees the claim rather than the label.
func (k WaiverKind) Describe() string {
	switch k {
	case KindPendingImplementation:
		return "assertable and unwritten; the named work item carries the probe"
	case KindKnownDefect:
		return "measured behaviour the repo does not want pinned; the named work item carries the fix"
	case KindStructurallyUnreachable:
		return "not assertable from a test in this repo; the marker names what is missing"
	case KindAcceptedUnprobed:
		return "an owner decided this claim is not worth a probe; the marker names the ruling"
	}
	return ""
}

// namesWorkItem reports whether a kind's citation MUST name an aihub work item.
//
// The three that must are the three that are waiting on somebody: a pending
// implementation, a fix, or a ruling. `structurally-unreachable` names a missing
// harness and `accepted-unprobed` names a decision, neither of which is
// necessarily a work item — clampdisclosure's own citations are a file and a
// commit. Requiring one there would be satisfied by citing an unrelated number.
func (k WaiverKind) namesWorkItem() bool {
	switch k {
	case KindPendingImplementation, KindKnownDefect:
		return true
	}
	return false
}

// ProseOnlyBecause is why a sentence the recogniser flagged is not assertable.
//
// 🔴 The vocabulary is CLOSED, and that is the whole mechanism. A free-text
// reason is satisfied by any sentence, and an escape hatch cheaper than
// compliance becomes the compliant path — the maxHistoricalQuoteRows argument,
// applied to prose. The six values are the six negative signatures the recogniser
// itself is written against, so a `because` is a statement about the SHAPE of the
// sentence, which a reviewer can check by reading it.
type ProseOnlyBecause string

const (
	// BecauseHistory is past tense: "used to", "before aihub#NNN", "it was". No
	// further justification is carried, because `git blame` is the record and
	// "provenance lives in git blame, not in a prose changelog" is already this
	// repo's rule for gated_tests.txt.
	BecauseHistory ProseOnlyBecause = "history"
	// BecauseJudgement is an evaluative predicate with no token — reliable,
	// important, worth recording, deliberate, correct.
	BecauseJudgement ProseOnlyBecause = "judgement"
	// BecauseCounterfactual is a modal `would` / `could have` under a hypothetical.
	BecauseCounterfactual ProseOnlyBecause = "counterfactual"
	// BecauseCrossRepo names plugins/**, the scenario repo, services.yaml, or the
	// polyforge skill flow — a surface this repo's tests do not own.
	BecauseCrossRepo ProseOnlyBecause = "cross-repo"
	// BecauseExternalState is a claim about an aihub#NNN's state, or about GitHub —
	// authority is a database this gate cannot read.
	BecauseExternalState ProseOnlyBecause = "external-state"
	// BecauseMeasurement is a number with a unit plus a corpus or a date.
	BecauseMeasurement ProseOnlyBecause = "measurement"
)

// ProseOnlyVocabulary is the closed set.
var ProseOnlyVocabulary = []ProseOnlyBecause{
	BecauseHistory, BecauseJudgement, BecauseCounterfactual,
	BecauseCrossRepo, BecauseExternalState, BecauseMeasurement,
}

func validBecause(b string) bool {
	for _, v := range ProseOnlyVocabulary {
		if string(v) == b {
			return true
		}
	}
	return false
}

func validKind(k string) bool {
	for _, v := range WaiverKinds {
		if string(v) == k {
			return true
		}
	}
	return false
}

// ─────────────────────────────── marker syntax ───────────────────────────────

// MarkerWaiver and MarkerProseOnly are the two in-card forms. HTML comments for
// the reason K9's cardHistoricalMarker is one: they cannot occur by accident, they
// render as nothing so the prose stays readable, and they are greppable, which is
// what lets every classification in the tree be COUNTED.
//
//	<!-- probe-waiver: kind=pending-implementation | decided=2026-09-10 | citation=aihub#543 | reason=… -->
//	<!-- prose-only: because=history -->
//
// `reason` is last and runs to the closing `-->`, so a reason may contain the
// separator. Every other field is `key=value` between pipes.
const (
	MarkerWaiver    = "probe-waiver"
	MarkerProseOnly = "prose-only"
)

var markerRe = regexp.MustCompile(`(?s)<!--\s*(probe-waiver|prose-only)\s*:\s*(.*?)\s*-->`)

// isoDate matches a real ISO calendar date. Month and day are range-checked so the
// requirement cannot be satisfied by something date-SHAPED — a version, a dotted
// identifier, a hash prefix — which an unchecked \d\d-\d\d would accept. Same
// reasoning, same pattern, as K11's openISODate.
var isoDate = regexp.MustCompile(`^20\d{2}-(?:0[1-9]|1[0-2])-(?:0[1-9]|[12]\d|3[01])$`)

// workItemRef matches a work-item citation. Measured 2026-09-08 by K11: every x#N
// form in the card set is aihub#N, so a wider pattern would buy nothing and could
// match a PR or issue reference, which is a different claim.
var workItemRef = regexp.MustCompile(`\baihub#\d+\b`)

// minReasonLen is how much prose a waiver reason must carry. A reason is the only
// part of a marker a reviewer can disagree with, and "TODO" is not one. It sits
// far below every real reason and far above zero, which is the floor discipline
// the card gate's own constants state for themselves.
const minReasonLen = 40

// Marker is one parsed in-card classification.
type Marker struct {
	// Form is MarkerWaiver or MarkerProseOnly.
	Form string
	// Kind is set for MarkerWaiver.
	Kind WaiverKind
	// Because is set for MarkerProseOnly.
	Because ProseOnlyBecause
	// Decided is the date of the classification, YYYY-MM-DD. Waivers only.
	Decided string
	// Citation is where the decision is written down — a work item, a ruling, a
	// merge, or the file that argues it. Waivers only.
	Citation string
	// Reason is why this sentence is not probed. Waivers only.
	Reason string
	// Raw is the marker as it appears in the card, for failure text.
	Raw string
	// Offset is the byte offset in the joined prose at which the marker sat before
	// it was stripped. -1 means it sat on a line the walk does not read.
	Offset int
}

// Problems returns everything wrong with this marker's own fields. It says nothing
// about the sentence the marker is attached to; Classify does that.
func (m Marker) Problems(card string) []string {
	var out []string
	switch m.Form {
	case MarkerProseOnly:
		if !validBecause(string(m.Because)) {
			out = append(out, fmt.Sprintf(
				"K12 PROSE_ONLY_BECAUSE_UNKNOWN: %s carries %s with because=%q, which is not "+
					"one of the closed vocabulary %v. The vocabulary is closed because a "+
					"free-text reason is satisfied by any sentence, and an escape hatch cheaper "+
					"than compliance becomes the compliant path. If none of the six fits, the "+
					"sentence is probably assertable — file a probe-waiver instead.",
				card, m.Raw, m.Because, ProseOnlyVocabulary))
		}
		if m.Kind != "" || m.Decided != "" || m.Citation != "" || m.Reason != "" {
			out = append(out, fmt.Sprintf(
				"K12 PROSE_ONLY_EXTRA_FIELDS: %s carries %s with waiver fields on it. A "+
					"prose-only row has ONE field. Carrying a Kind or a date on it makes it read "+
					"like debt that is being tracked, which is the confusion the two tables are "+
					"separate to prevent.", card, m.Raw))
		}
	case MarkerWaiver:
		if !validKind(string(m.Kind)) {
			out = append(out, fmt.Sprintf(
				"K12 WAIVER_KIND_UNKNOWN: %s carries %s with kind=%q, which is not one of %v. "+
					"Recording different kinds of exemption under one label loses exactly the "+
					"fact that tells them apart — aihub#411 T1-12 §6.1.",
				card, m.Raw, m.Kind, WaiverKinds))
		}
		if !isoDate.MatchString(m.Decided) {
			out = append(out, fmt.Sprintf(
				"K12 WAIVER_NO_DATE: %s carries %s with decided=%q, which is not a real ISO "+
					"calendar date. Undated, a classification is true on the day it is written "+
					"and silently wrong afterwards — the form 5 of 48 cards took when they came "+
					"to assert defects that were already fixed (aihub#476).",
				card, m.Raw, m.Decided))
		}
		if strings.TrimSpace(m.Citation) == "" {
			out = append(out, fmt.Sprintf(
				"K12 WAIVER_NO_CITATION: %s carries %s with no citation. A waiver with nowhere "+
					"to read the decision is a decision nobody made.", card, m.Raw))
		} else if m.Kind.namesWorkItem() && !workItemRef.MatchString(m.Citation) {
			out = append(out, fmt.Sprintf(
				"K12 WAIVER_NO_WORK_ITEM: %s carries %s with kind=%s and citation=%q, which "+
					"names no aihub#NNN. %s — and a kind that is waiting on somebody with nobody "+
					"named is the silence this ledger exists to end.",
				card, m.Raw, m.Kind, m.Citation, m.Kind.Describe()))
		}
		if len(strings.TrimSpace(m.Reason)) < minReasonLen {
			out = append(out, fmt.Sprintf(
				"K12 WAIVER_NO_REASON: %s carries %s with a reason of %d character(s), floor is "+
					"%d. The reason is the only part of a marker a reviewer can disagree with; "+
					"without one the marker records that somebody wanted the gate quiet, not why.",
				card, m.Raw, len(strings.TrimSpace(m.Reason)), minReasonLen))
		}
		if m.Because != "" {
			out = append(out, fmt.Sprintf(
				"K12 WAIVER_EXTRA_FIELDS: %s carries %s with because=%q on it. `because` belongs "+
					"to a prose-only row, which claims there is nothing here to probe; a waiver "+
					"claims the opposite.", card, m.Raw, m.Because))
		}
	}
	return out
}

// ─────────────────────────────── the recogniser ──────────────────────────────

// 🔴 Assertability is a FORM, not a taste call. This is the load-bearing decision
// in the package.
//
// A ratchet needs a population it can enumerate. If "assertable" is a judgement
// each author makes freshly, the gate has nothing to police and the cheapest
// compliant path is to judge every awkward sentence unassertable — the
// escape-hatch failure this repo has already paid for twice (maxHistoricalQuoteRows
// being a signed constant rather than a comment; WAIVER_NO_COUNT being required
// rather than encouraged). So the recogniser is written the way clampdisclosure
// defines a clamp by a code property rather than by the word "clamp", and the way
// K11 refuses a form rather than fact-checking a bullet.
//
// A sentence is CANDIDATE-ASSERTABLE when all three hold:
//
//  1. it names a published token — written in backticks, which is what makes the
//     recogniser cheap;
//  2. its verb is an effect-or-refusal verb;
//  3. it is present tense about the current tree.
//
// ⚠️ And it is tuned to OVER-REPORT, deliberately. A present-tense sentence naming
// a token and an effect verb is a candidate even where a human would immediately
// call it prose-only; the author then files a prose-only marker with a `because`.
// That is the direction clampdisclosure states for itself — "the gate's failure
// mode is noise a human resolves, not silence" — and a recogniser tuned the other
// way would let the population shrink quietly, which is the whole defect.

// effectVerbs is the closed list from the aihub#543 spec §1.2, with the
// inflections the cards actually write. Closed rather than open-ended because a
// list somebody may extend at will is a list that grows to cover whatever the
// author wanted excluded.
var effectVerbs = map[string]bool{
	"is": true, "are": true,
	"return": true, "returns": true,
	"answer": true, "answers": true,
	"refuse": true, "refuses": true,
	"reject": true, "rejects": true,
	"derive": true, "derives": true,
	"create": true, "creates": true,
	"delete": true, "deletes": true,
	"write": true, "writes": true,
	"send": true, "sends": true,
	"forward": true, "forwards": true,
	"carry": true, "carries": true,
	"omit": true, "omits": true,
	"require": true, "requires": true,
	"report": true, "reports": true,
	"clamp": true, "clamps": true,
	"400s": true, "409s": true,
}

// historyPhrases is the ONE automatic disqualifier, and its narrowness is the
// point.
//
// 🔴 Every other negative signature in the spec's six-value vocabulary is left to
// an explicit `prose-only` marker rather than detected. An automatic disqualifier
// is a way for the population to shrink WITHOUT a signed diff, which is precisely
// what the over-report tuning above exists to prevent; the vocabulary's job is to
// bound the author's stated reason, not to be a detector. "used to" is admitted
// because it cannot be a live claim in any tense and gaming it means writing a
// false past-tense sentence — a cost, not a hatch.
//
// ⚠️ Tense alone is NOT the tell, which is why nothing broader is here. "Locks
// derived at claim are `file_scope` only since `aihub#416`" is present tense with a
// historical clause and is correctly candidate-assertable; so is "`task_branches`
// is NO LONGER SENT", which is a claim about an absent wire key that no arm in the
// tree can see.
var historyPhrases = []string{"used to ", "used to,"}

// backtickSpan finds the backticked tokens a card writes its published names in.
var backtickSpan = regexp.MustCompile("`([^`\n]+)`")

// bareWorkItem and bareSectionRef are backticked spans that are NOT published
// tokens: a work item is external state and a spec section reference is a
// pointer, and a sentence whose only backticks are those names nothing this repo
// publishes.
var (
	bareWorkItem   = regexp.MustCompile(`^aihub#\d+$`)
	bareSectionRef = regexp.MustCompile(`^§`)
)

// NamesPublishedToken is condition 1.
func NamesPublishedToken(s string) bool {
	for _, m := range backtickSpan.FindAllStringSubmatch(s, -1) {
		tok := strings.TrimSpace(m[1])
		if tok == "" || bareWorkItem.MatchString(tok) || bareSectionRef.MatchString(tok) {
			continue
		}
		return true
	}
	return false
}

// HasEffectVerb is condition 2. It reads the whole sentence rather than isolating
// the main verb: finding the main verb needs a parser, and the over-report tuning
// says to take the looser test where the two disagree.
func HasEffectVerb(s string) bool {
	for _, w := range words(strings.ToLower(stripBackticked(s))) {
		if effectVerbs[w] {
			return true
		}
	}
	return false
}

// IsHistorical is the negation of condition 3, in the narrow form described on
// historyPhrases.
func IsHistorical(s string) bool {
	low := strings.ToLower(s)
	for _, p := range historyPhrases {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}

// IsCandidate is the recogniser. The second return value names the condition that
// failed, so a failure message can say WHY a sentence was passed over rather than
// leaving a reader to re-derive it.
func IsCandidate(s string) (bool, string) {
	if !NamesPublishedToken(s) {
		return false, "names no published token in backticks"
	}
	if !HasEffectVerb(s) {
		return false, "carries no effect-or-refusal verb"
	}
	if IsHistorical(s) {
		return false, "is written in the past tense (\"used to\")"
	}
	return true, ""
}

// stripBackticked removes backticked spans before the verb scan, so a symbol name
// containing an effect verb (`willUnlock`, `reportsTo`) is not read as one. The
// token check above has already run on the same sentence, so nothing is lost.
func stripBackticked(s string) string {
	return backtickSpan.ReplaceAllString(s, " ")
}

func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// ─────────────────────────── citing an arm in prose ──────────────────────────

// armPath and armSymbol are the citation forms that say "an arm holds this".
//
// 🔴 Narrower than K6's anchor form on purpose. K6 resolves any backticked
// repo-relative .go path and any parenthesised symbol, which is the right rule for
// an ANCHOR — but an arm is a TEST, and `internal/domain/conflicts.go` in a
// sentence names the implementation the claim is about, not a gate over it.
// Counting that as "probed" would retire debt by describing where the code lives,
// which is the one thing a reader of this ledger must not be able to do.
//
// So a sentence is cited when it names a `*_test.go` file or a `Test…` symbol, in
// backticks. Both forms are already K6-resolved, so a citation that does not exist
// is red there before it is counted here.
var (
	armPath   = regexp.MustCompile("`[^`\n]*_test\\.go`")
	armSymbol = regexp.MustCompile("`Test[A-Z][A-Za-z0-9_]*`")
)

// CitesAnArm is the "assertable + probed" test.
func CitesAnArm(s string) bool {
	return armPath.MatchString(s) || armSymbol.MatchString(s)
}

// ────────────────────────────── reading a card ───────────────────────────────

// Sentence is one unit of card prose, with whatever markers were attached to it.
type Sentence struct {
	Card    string
	Text    string
	Start   int
	Markers []Marker
}

// minSentenceLen mirrors the population sizer the aihub#543 spec §0.1 states, so
// the numbers this package prints are comparable with the ones that sized the
// work. Anything shorter is a fragment the splitter produced, not a claim.
const minSentenceLen = 15

// sentenceStarts are the runes a new sentence may begin with, matching the sizer.
// A card sentence routinely opens with a bold marker, a backtick or a warning
// emoji rather than a letter.
const sentenceStarts = "🔴⚠`*_"

// ReadCard splits a card's prose into sentences and attaches every marker to the
// sentence it sits on. Markers on a line the walk does not read — a heading, a
// table row, a fenced block — come back with Offset -1 and are orphans.
//
// The walk mirrors the spec's own population sizer: fenced blocks, table rows and
// headings are dropped, the remaining lines are joined, and the join is split on
// sentence-final punctuation followed by whitespace and a sentence-start rune.
// Deliberately the same walk, so a count here and a count from the sizer are about
// the same population.
func ReadCard(card, prose string) ([]Sentence, []Marker) {
	joined, markers, orphans := joinProse(prose)
	spans := splitSentences(joined)

	out := make([]Sentence, 0, len(spans))
	for _, sp := range spans {
		text := strings.TrimSpace(joined[sp.start:sp.end])
		if len([]rune(text)) <= minSentenceLen {
			continue
		}
		out = append(out, Sentence{Card: card, Text: text, Start: sp.start})
	}

	// 🔴 A marker classifies the sentence it FOLLOWS, so the attachment is the last
	// sentence that STARTS strictly before it. Strictly, because a marker written on
	// its own line between two sentences sits at exactly the second one's start
	// offset, and reading it as classifying the sentence it precedes puts every
	// trailing marker on the wrong claim — which this walk reported as a
	// STALE_MARKER the first time one was filed, correctly.
	for _, m := range markers {
		idx := -1
		for i := range out {
			if out[i].Start < m.Offset {
				idx = i
				continue
			}
			break
		}
		if idx < 0 {
			orphans = append(orphans, m)
			continue
		}
		out[idx].Markers = append(out[idx].Markers, m)
	}
	return out, orphans
}

type span struct{ start, end int }

// joinProse drops what the sizer drops and extracts every marker as it goes,
// recording each marker's offset in the joined text so it can be attached to a
// sentence afterwards. A marker on a dropped line is returned as an orphan
// immediately: it classifies nothing, and a classification nothing reads is the
// exemption that outlives its gap.
func joinProse(prose string) (joined string, markers, orphans []Marker) {
	var sb strings.Builder
	inFence := false
	for _, line := range logicalLines(prose) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			orphans = append(orphans, parseMarkers(trimmed, -1)...)
			continue
		}
		dropped := inFence || trimmed == "" ||
			strings.HasPrefix(trimmed, "|") || strings.HasPrefix(trimmed, "#")
		if dropped {
			orphans = append(orphans, parseMarkers(trimmed, -1)...)
			continue
		}
		clean, found := extractMarkers(trimmed, sb.Len())
		markers = append(markers, found...)
		clean = strings.TrimSpace(clean)
		if clean == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(clean)
	}
	return sb.String(), markers, orphans
}

// logicalLines coalesces a marker comment that spans several physical lines into
// one, so a reason long enough to need wrapping is still one marker.
//
// ⚠️ Markdown line breaks are a rendering artifact — the same argument K11's
// cardOpenBullets makes for joining a bullet's lines before scanning it. Without
// this, a wrapped marker would not match and its sentence would fall through as
// unclassified; that failure is red rather than silent (the ledger row stops
// matching), but red for a reason nobody could see from the message.
//
// Fenced blocks are excluded, so a `<!--` inside a code sample cannot swallow the
// rest of the block. An unterminated comment is returned as-is and simply fails to
// match, which is again red rather than silent.
func logicalLines(prose string) []string {
	var out []string
	var pending []string
	inFence, open := false, false
	for _, line := range strings.Split(prose, "\n") {
		trimmed := strings.TrimSpace(line)
		if open {
			pending = append(pending, trimmed)
			if strings.Contains(trimmed, "-->") {
				out = append(out, strings.Join(pending, " "))
				pending, open = nil, false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			out = append(out, line)
			continue
		}
		if i := strings.Index(line, "<!--"); !inFence && i >= 0 && !strings.Contains(line[i:], "-->") {
			open = true
			pending = []string{strings.TrimRight(line, " \t")}
			continue
		}
		out = append(out, line)
	}
	if open {
		out = append(out, strings.Join(pending, " "))
	}
	return out
}

// extractMarkers removes every marker from one line and returns the line without
// them plus the markers, each carrying its offset in the joined text under
// construction. base is the length of the joined buffer BEFORE this line; the
// separating space this line will contribute is accounted for by biasing the
// offset one byte later, which keeps a marker sitting at the very start of a line
// inside that line's first sentence rather than the previous one.
func extractMarkers(line string, base int) (string, []Marker) {
	locs := markerRe.FindAllStringSubmatchIndex(line, -1)
	if len(locs) == 0 {
		return line, nil
	}
	var out []Marker
	var sb strings.Builder
	prev := 0
	removed := 0
	for _, l := range locs {
		sb.WriteString(line[prev:l[0]])
		raw := line[l[0]:l[1]]
		m := parseMarker(raw, line[l[2]:l[3]], line[l[4]:l[5]])
		// +1 for the space this line contributes to the join when base > 0.
		off := base + l[0] - removed
		if base > 0 {
			off++
		}
		m.Offset = off
		out = append(out, m)
		removed += l[1] - l[0]
		prev = l[1]
	}
	sb.WriteString(line[prev:])
	return sb.String(), out
}

// parseMarkers is the orphan path: it needs the markers but not the offsets.
func parseMarkers(s string, offset int) []Marker {
	var out []Marker
	for _, l := range markerRe.FindAllStringSubmatchIndex(s, -1) {
		m := parseMarker(s[l[0]:l[1]], s[l[2]:l[3]], s[l[4]:l[5]])
		m.Offset = offset
		out = append(out, m)
	}
	return out
}

// parseMarker reads the key=value body. `reason` is last and runs to the end, so a
// reason may contain the pipe separator; every other field is a pipe-separated
// key=value.
func parseMarker(raw, form, body string) Marker {
	m := Marker{Form: form, Raw: raw}
	head := body
	if i := strings.Index(body, "reason="); i >= 0 {
		head = body[:i]
		m.Reason = strings.TrimSpace(body[i+len("reason="):])
	}
	for _, part := range strings.Split(head, "|") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "kind":
			m.Kind = WaiverKind(v)
		case "because":
			m.Because = ProseOnlyBecause(v)
		case "decided":
			m.Decided = v
		case "citation":
			m.Citation = v
		}
	}
	return m
}

// splitSentences cuts on sentence-final punctuation followed by whitespace and a
// sentence-start rune. Hand-rolled because Go's regexp has no lookaround, and
// deliberately the same rule as the spec's sizer so the two counts are about the
// same population.
func splitSentences(s string) []span {
	var out []span
	rs := []rune(s)
	// byte offset of each rune index
	offs := make([]int, len(rs)+1)
	b := 0
	for i, r := range rs {
		offs[i] = b
		b += len(string(r))
	}
	offs[len(rs)] = b

	start := 0
	for i := 0; i < len(rs); i++ {
		if rs[i] != '.' && rs[i] != '!' && rs[i] != '?' {
			continue
		}
		j := i + 1
		for j < len(rs) && unicode.IsSpace(rs[j]) {
			j++
		}
		if j == i+1 || j >= len(rs) {
			continue // no whitespace after the punctuation, or end of text
		}
		n := rs[j]
		if !unicode.IsUpper(n) && !strings.ContainsRune(sentenceStarts, n) {
			continue
		}
		out = append(out, span{offs[start], offs[i+1]})
		start = j
	}
	if start < len(rs) {
		out = append(out, span{offs[start], offs[len(rs)]})
	}
	return out
}

// ────────────────────────────── classification ───────────────────────────────

// Class is the state a sentence is in.
type Class int

const (
	// NotCandidate is a sentence the recogniser does not call assertable.
	NotCandidate Class = iota
	// Cited is assertable and probed: it names its arm in its own prose.
	Cited
	// Waived is assertable and unprobed, with a named marker. This is debt.
	Waived
	// ProseOnly is not assertable, with a `because` from the closed vocabulary.
	ProseOnly
	// Unclassified is assertable, unprobed and unmarked — what the ratchet's
	// ceilings hold at their measured value and probe waves drive to zero.
	Unclassified
)

func (c Class) String() string {
	switch c {
	case NotCandidate:
		return "not-candidate"
	case Cited:
		return "cited"
	case Waived:
		return "waived"
	case ProseOnly:
		return "prose-only"
	case Unclassified:
		return "unclassified"
	}
	return "?"
}

// Classify decides one sentence and reports everything wrong with the markers on
// it. The problems are about placement and consistency; Marker.Problems covers the
// marker's own fields, and Classify calls it so one walk reports both.
func Classify(s Sentence) (Class, []string) {
	var problems []string
	var waivers, proseOnly []Marker
	for _, m := range s.Markers {
		problems = append(problems, m.Problems(s.Card)...)
		switch m.Form {
		case MarkerWaiver:
			waivers = append(waivers, m)
		case MarkerProseOnly:
			proseOnly = append(proseOnly, m)
		}
	}

	candidate, why := IsCandidate(s.Text)
	marked := len(waivers) + len(proseOnly)

	// 🔴 The self-emptying half, and it is the property that makes this ledger
	// delete its own entries rather than accumulate them: a marker on a sentence
	// the recogniser no longer flags has outlived its gap, and an exemption that
	// outlives its gap is one nobody removes. Same reasoning as K5's stale
	// exemption and K9's HISTORICAL_STILL_LIVE.
	if !candidate && marked > 0 {
		problems = append(problems, fmt.Sprintf(
			"K12 STALE_MARKER: %s classifies a sentence the recogniser does not call "+
				"candidate-assertable — it %s:\n    %s\nThe marker exempts nothing. Either the "+
				"sentence was rewritten and the marker should go, or the recogniser stopped "+
				"seeing a claim it should see, which is a bug in the recogniser and not "+
				"something to route around.", s.Card, why, truncate(s.Text)))
		return NotCandidate, problems
	}
	if !candidate {
		return NotCandidate, problems
	}

	if len(waivers) > 0 && len(proseOnly) > 0 {
		problems = append(problems, fmt.Sprintf(
			"K12 MARKER_CONFLICT: %s carries both a %s and a %s marker on one sentence:\n"+
				"    %s\nA waiver says \"this should be probed and is not\"; a prose-only row "+
				"says \"there is nothing here to probe\". They cannot both be true of the same "+
				"sentence.", s.Card, MarkerWaiver, MarkerProseOnly, truncate(s.Text)))
	}
	if len(waivers) > 1 || len(proseOnly) > 1 {
		problems = append(problems, fmt.Sprintf(
			"K12 MARKER_DUPLICATE: %s carries %d markers on one sentence:\n    %s\nOne "+
				"sentence, one classification — otherwise which one the tally counts depends on "+
				"marker order, which is not something a reader would predict.",
			s.Card, marked, truncate(s.Text)))
	}

	cited := CitesAnArm(s.Text)
	if cited && len(waivers) > 0 {
		problems = append(problems, fmt.Sprintf(
			"K12 STALE_WAIVER: %s waives a sentence that already cites an arm:\n    %s\nThe "+
				"gap the waiver covers has closed, which is the event that is supposed to delete "+
				"the entry. Drop the marker.", s.Card, truncate(s.Text)))
	}

	switch {
	case len(waivers) > 0:
		return Waived, problems
	case len(proseOnly) > 0:
		return ProseOnly, problems
	case cited:
		return Cited, problems
	default:
		return Unclassified, problems
	}
}

func truncate(s string) string {
	const max = 220
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + " …"
}

// ──────────────────────────────── the ledger ─────────────────────────────────

// Census is one card's recorded classification counts — every class, not only the
// debt ones.
//
// 🔴 Every waiver kind gets its own column, not just the pending one. If only
// `pending-implementation` were counted, the cheapest way to make debt disappear
// would be to relabel a row `accepted-unprobed` — the escape-hatch-cheaper-than-
// compliance failure, one level up. With a column each, a relabel moves a number
// from one column to another in a diff somebody signs, which is visible.
//
// 🔴 Candidates and Cited are here for a sharper reason, found by reading this
// package back before it landed: with only the debt columns recorded, a change
// that ADDS one assertable sentence and CITES one previously-unclassified one nets
// to zero and passes green. That is a new unheld claim arriving under cover of
// somebody else's probe — the exact event the ratchet exists to refuse. With the
// population and the cited count recorded, the two halves of the swap show up
// separately. They also make the row internally checkable: the classes are
// exhaustive, so Candidates must equal Cited plus every column below it.
//
// ⚠️ ProseOnly is counted because it is the cheapest escape of all — declaring a
// claim unassertable — and the count is what lets the review half see it grow. It
// is NOT debt. The gate cannot check whether a `because` is honest;
// polyforge-scenario#20's reviewer owns every prose-only row.
//
// ⚠️ The SENTENCE count is deliberately absent. It moves on any rewording, so
// recording it would redden the gate on cosmetic edits; Candidates moves only when
// the assertable population changes, which is the line worth defending.
type Census struct {
	// Candidates is how many sentences the recogniser called assertable.
	Candidates int
	// Cited is how many of those name their own arm in prose. Not debt.
	Cited int
	// Unclassified is assertable, unprobed and unmarked: the grandfathering
	// escape. It must reach 0 to close a phase.
	Unclassified int
	// PendingImplementation is the debt that must ratchet down.
	PendingImplementation int
	// KnownDefect is measured behaviour deliberately left unpinned.
	KnownDefect int
	// StructurallyUnreachable is provisional by nature and is the only kind that
	// may legitimately be a terminal state.
	StructurallyUnreachable int
	// AcceptedUnprobed is an owner decision, waiting for nothing.
	AcceptedUnprobed int
	// ProseOnly is not debt; it is counted so the cheapest escape is visible.
	ProseOnly int
}

// Line renders a Census as the Go composite literal a reader can paste into the
// ledger, which is what makes a failure repairable without re-deriving anything.
// Same shape dbtestcov uses when it prints the manifest line it wants.
func (d Census) Line(card string) string {
	return fmt.Sprintf(
		"\t%q: {Candidates: %d, Cited: %d, Unclassified: %d, PendingImplementation: %d, "+
			"KnownDefect: %d, StructurallyUnreachable: %d, AcceptedUnprobed: %d, "+
			"ProseOnly: %d},",
		card, d.Candidates, d.Cited, d.Unclassified, d.PendingImplementation, d.KnownDefect,
		d.StructurallyUnreachable, d.AcceptedUnprobed, d.ProseOnly)
}

// Balanced reports whether the classes account for the whole population. They are
// exhaustive by construction, so a false here means a sentence was counted as a
// candidate and then classified into nothing — which today can only happen when a
// waiver names a kind outside the closed set.
func (d Census) Balanced() bool {
	return d.Candidates == d.Cited+d.Unclassified+d.ProseOnly+d.PendingImplementation+
		d.KnownDefect+d.StructurallyUnreachable+d.AcceptedUnprobed
}

// CardTally is what one card measured.
type CardTally struct {
	Sentences int
	Census    Census
	Problems  []string
}

// Tally reads one card and classifies every sentence in it.
func Tally(card, prose string) CardTally {
	sentences, orphans := ReadCard(card, prose)
	t := CardTally{Sentences: len(sentences)}

	for _, m := range orphans {
		t.Problems = append(t.Problems, fmt.Sprintf(
			"K12 MARKER_ORPHAN: %s carries %s on a line this walk does not read — a heading, a "+
				"table row or a fenced block. A marker classifies the SENTENCE it sits on, so "+
				"one that sits on no sentence exempts nothing while looking like it does. Move "+
				"it onto the prose it is about.", card, m.Raw))
		t.Problems = append(t.Problems, m.Problems(card)...)
	}

	for _, s := range sentences {
		class, problems := Classify(s)
		t.Problems = append(t.Problems, problems...)
		if class != NotCandidate {
			t.Census.Candidates++
		}
		switch class {
		case Cited:
			t.Census.Cited++
		case Unclassified:
			t.Census.Unclassified++
		case ProseOnly:
			t.Census.ProseOnly++
		case Waived:
			for _, m := range s.Markers {
				if m.Form != MarkerWaiver {
					continue
				}
				switch m.Kind {
				case KindPendingImplementation:
					t.Census.PendingImplementation++
				case KindKnownDefect:
					t.Census.KnownDefect++
				case KindStructurallyUnreachable:
					t.Census.StructurallyUnreachable++
				case KindAcceptedUnprobed:
					t.Census.AcceptedUnprobed++
				}
			}
		}
	}
	return t
}

// LedgerProblems compares the measured census against the recorded ledger, in
// BOTH directions, and checks the scoped card set both ways.
//
// 🔴 Exact equality, not a one-sided ceiling, and that is what makes this a
// ratchet rather than a budget. A debt column ABOVE the record is new debt
// arriving unsigned — a card sentence that asserts something with no probe and no
// marker, which is the exact event two waves of false statements walked through
// unseen. A column BELOW the record is a gap that has closed, and leaving the
// number high would let the next sentence take the vacated slot silently. Both are
// reported, separately, because the edit that fixes them differs — and the failure
// prints the replacement line, so neither costs a re-derivation.
//
// ⚠️ maxPendingCards in the card gate is a ONE-sided ceiling and this deliberately
// is not. The difference is what the number counts: a pending CARD is a file
// somebody has not written yet and its count falls as a side effect of unrelated
// work, while these counts move only when somebody edits a card sentence or lands
// a probe. A number that moves only on purpose can be required to be exact.
func LedgerProblems(tallies map[string]CardTally, ledger map[string]Census, cards map[string]bool) []string {
	var out []string

	for _, card := range sortedTallyKeys(tallies) {
		if _, ok := ledger[card]; !ok {
			out = append(out, fmt.Sprintf(
				"K12 LEDGER_MISSING: %s is in the scoped set and has no ledger row, so nothing "+
					"bounds its debt. Paste:\n%s", card, tallies[card].Census.Line(card)))
		}
		if !tallies[card].Census.Balanced() {
			out = append(out, fmt.Sprintf(
				"K12 CENSUS_UNBALANCED: %s measured %s, and the classes do not add up to the "+
					"candidate count. They are exhaustive by construction, so a sentence was "+
					"counted into the population and then classified into nothing — today that "+
					"means a marker naming a kind outside the closed set, which is reported "+
					"separately. A row that does not balance bounds less than it appears to.",
				card, describeCensus(tallies[card].Census)))
		}
	}

	for _, card := range sortedCensusKeys(ledger) {
		if !cards[card] {
			out = append(out, fmt.Sprintf(
				"K12 LEDGER_ORPHAN: the ledger holds a row for %q, which is not a card. A row "+
					"for a file that does not exist bounds nothing and hides that it was never "+
					"removed.", card))
			continue
		}
		t, ok := tallies[card]
		if !ok {
			out = append(out, fmt.Sprintf(
				"K12 LEDGER_UNSCOPED: the ledger holds a row for %s, which is not in the scoped "+
					"card set. A bound on a card nothing walks is a bound that reports green "+
					"forever.", card))
			continue
		}
		if t.Census == ledger[card] {
			continue
		}

		finding, explain := classifyDrift(t.Census, ledger[card])
		out = append(out, fmt.Sprintf(
			"%s: %s measured %s but the ledger records %s. %s Paste:\n%s",
			finding, card, describeCensus(t.Census), describeCensus(ledger[card]), explain,
			t.Census.Line(card)))
	}
	return out
}

// classifyDrift names the difference by what MOVED, because the three cases are
// three different edits.
//
// A change that both rises and falls in the debt columns — a relabel plus a new
// claim — is reported as growth: the rise is the half that needs signing, and a
// message that led with the fall would tell a reader to lower a number that has to
// go up.
func classifyDrift(measured, recorded Census) (finding, explain string) {
	debt := func(c Census) []int {
		return []int{c.Unclassified, c.PendingImplementation, c.KnownDefect,
			c.StructurallyUnreachable, c.AcceptedUnprobed, c.ProseOnly}
	}
	m, r := debt(measured), debt(recorded)
	grew, shrank := false, false
	for i := range m {
		if m[i] > r[i] {
			grew = true
		}
		if m[i] < r[i] {
			shrank = true
		}
	}
	switch {
	case grew:
		return "K12 DEBT_GROWTH", "Debt GREW. A card sentence now asserts something " +
			"with neither a cited arm nor a named marker, which is exactly the event 30+ " +
			"false statements walked through unseen across the 2026-09-08 and 2026-09-09 " +
			"waves. Write the probe, or file the marker and raise this row in a diff " +
			"somebody signs."
	case shrank:
		return "K12 STALE_DEBT", "A gap has CLOSED. Lower the row in the same change, or " +
			"the vacated slot lets the next unclassified sentence in silently."
	default:
		return "K12 POPULATION_MOVED", "No debt column moved — the assertable population " +
			"or the cited count did. That is the swap this row exists to expose: a change " +
			"adding one assertable sentence while citing one previously-unclassified " +
			"sentence nets to zero across the debt columns and would otherwise pass green, " +
			"which is a new unheld claim arriving under cover of somebody else's probe. " +
			"Read both halves before pasting."
	}
}

func describeCensus(d Census) string {
	return fmt.Sprintf(
		"candidates=%d cited=%d unclassified=%d pending-implementation=%d known-defect=%d "+
			"structurally-unreachable=%d accepted-unprobed=%d prose-only=%d",
		d.Candidates, d.Cited, d.Unclassified, d.PendingImplementation, d.KnownDefect,
		d.StructurallyUnreachable, d.AcceptedUnprobed, d.ProseOnly)
}

func sortedTallyKeys(m map[string]CardTally) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedCensusKeys(m map[string]Census) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
