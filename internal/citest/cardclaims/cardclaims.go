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
//	                       row: a ledger holding a pointer for every assertable
//	                       sentence in the set is a second copy of the cards, and a
//	                       second copy's only failure mode is disagreeing with the
//	                       first.
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
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
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
// Fields are pipe-separated `key=value`. `reason` takes every remaining field, so
// a reason may contain the separator — but it is recognised as a FIELD KEY, never
// as a substring, because a citation reading "the reason=… argument" would
// otherwise silently truncate the entry.
const (
	MarkerWaiver    = "probe-waiver"
	MarkerProseOnly = "prose-only"
)

var markerRe = regexp.MustCompile(`(?s)<!--\s*(probe-waiver|prose-only)\s*:\s*(.*?)\s*-->`)

// markerShapedRe matches ANY `<!-- name: … -->` comment, so a marker whose name is
// misspelled or miscased can be reported instead of being inert.
//
// 🔴 An unrecognised marker is the worst failure mode this package has: the author
// believes a sentence is classified, the reviewer reads a classification in the
// diff, and the arm sees an ordinary HTML comment and counts the sentence as
// unclassified — which then only shows up as a ledger number nobody connects to
// the typo. K9's `<!-- historical -->` carries no colon and is unaffected.
var markerShapedRe = regexp.MustCompile(`(?s)<!--\s*([A-Za-z][A-Za-z0-9_-]*)\s*:\s*(.*?)-->`)

// isoDate matches an ISO calendar date and then checks it EXISTS.
//
// The pattern alone accepts 2026-02-31, and a date-shaped string that is not a
// date is exactly what K11's openISODate range-checks its months to refuse. The
// parse is the check; the pattern is only there to reject a partial match.
var isoDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func validDate(s string) bool {
	if !isoDate.MatchString(s) {
		return false
	}
	t, err := time.Parse("2006-01-02", s)
	return err == nil && t.Format("2006-01-02") == s
}

// workItemRef matches a work-item citation. Measured 2026-09-08 by K11: every x#N
// form in the card set is aihub#N, so a wider pattern would buy nothing and could
// match a PR or issue reference, which is a different claim.
var workItemRef = regexp.MustCompile(`\baihub#\d+\b`)

// minReasonLen is how much prose a waiver reason must carry. A reason is the only
// part of a marker a reviewer can disagree with, and "TODO" is not one. It sits
// far below every real reason and far above zero, which is the floor discipline
// the card gate's own constants state for themselves.
const minReasonLen = 40

// markerFields is the closed set of keys a marker may carry. Closed so a typo is
// reported rather than dropped: an ignored `kinds=` key surfaces later as an empty
// Kind, which reads to the author as the arm being broken rather than as their own
// spelling.
var markerFields = map[string]bool{
	"kind": true, "because": true, "decided": true, "citation": true, "reason": true,
}

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
	// Duplicated names every key the marker states more than once.
	//
	// 🔴 Reported rather than resolved last-wins. A second `kind=` appended to a
	// long wrapped marker is invisible in review and silently downgrades the entry —
	// measured: appending `| kind=accepted-unprobed` to a known-defect row both
	// changed its column and skipped the work-item requirement, because the kind the
	// checks ran against was the last one parsed.
	Duplicated []string
	// Unknown names every key outside markerFields.
	Unknown []string
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

	if len(m.Duplicated) > 0 {
		out = append(out, fmt.Sprintf(
			"K12 DUPLICATE_FIELD: %s carries %s stating %v more than once. Resolving that "+
				"last-wins would let a second `kind=` appended to a wrapped marker silently "+
				"downgrade the entry AND skip the checks the first kind would have failed, "+
				"with nothing in the rendered card to show for it. State each field once.",
			card, m.Raw, m.Duplicated))
	}
	if len(m.Unknown) > 0 {
		out = append(out, fmt.Sprintf(
			"K12 UNKNOWN_FIELD: %s carries %s with key(s) %v, which this marker syntax does "+
				"not define. An ignored key surfaces later as an empty field, which reads to "+
				"the author as the arm being broken rather than as their own spelling. Known "+
				"keys: kind, because, decided, citation, reason.", card, m.Raw, m.Unknown))
	}

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
		if !validDate(m.Decided) {
			out = append(out, fmt.Sprintf(
				"K12 WAIVER_NO_DATE: %s carries %s with decided=%q, which is not a calendar "+
					"date that exists. Undated, a classification is true on the day it is "+
					"written and silently wrong afterwards — the form 5 of 48 cards took when "+
					"they came to assert defects that were already fixed (aihub#476).",
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

// unrecognisedMarkerProblem renders the finding for a marker-shaped comment whose
// name is not one this package knows.
func unrecognisedMarkerProblem(card, raw, name string) string {
	return fmt.Sprintf(
		"K12 MARKER_NAME_UNRECOGNISED: %s carries %s, whose name %q is neither %q nor %q. "+
			"That comment classifies nothing and renders as nothing, so the author sees a "+
			"classification in the diff, the reviewer sees one too, and this arm sees an "+
			"ordinary HTML comment — the sentence stays unclassified and the only trace is a "+
			"ledger number nobody connects to a typo. Fix the name or delete the comment.",
		card, raw, name, MarkerWaiver, MarkerProseOnly)
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
//  1. it is ANCHORED — one of three forms, in strictly weakening order:
//     (a) it names a published token, written in backticks, which is what makes
//     the recogniser cheap;
//     (b) it carries backticked spans and every one is a wi-id or a section
//     reference — a sentence pinned to a measurement or a ruling, the form the
//     SERIALIZABLE isolation pair and the pf_get_step known-defect record take.
//     Added by aihub#591 after those were measured invisible: a card can record
//     a live defect in a sentence whose only backticks are `aihub#NNN`, and a
//     population that cannot see it reads KnownDefect:0 on a card carrying one;
//     (c) it has NO backticks at all, the sentence BEFORE it names a published
//     token, and its verb is from the refusal-or-response subset — "The server
//     400s without it." one bullet under `session_info.machine_id`, the
//     token-in-previous-sentence attribution the wave-1 checkpoint measured.
//     The subset is deliberately narrower than effectVerbs: with no token and
//     no anchor, a bare copula would sweep every piece of narration that
//     happens to follow a token into the population, which is noise even a
//     gate tuned to over-report cannot spend;
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
//
// 🔴 Widened by aihub#591 (2026-09-10) from the spec's original list, each
// addition MEASURED against a live card sentence the old list passed over:
// govern/touch ("the strictest tier a patch touches governs the whole patch",
// pf_update_work_item — held by a named arm and invisible to this walk), sit
// ("the guard sits above the first query", pf_remember — a placement claim an
// AST arm can hold), open ("this path opens SERIALIZABLE", the isolation-level
// pair on pf_claim_work_item/pf_force_takeover), count ("a same-size swap
// counts as a removal", pf_update_project), come/comes ("they come back on this
// response as `repo_pins`", pf_claim_work_item), tell/tells ("still tells a
// caller to pass the canonical id", pf_recall).
//
// ⚠️ Two of them deliberately carry only the s-inflection: "opens" and "counts".
// Their base forms are the everyday adjective/noun of these very cards — the
// `## Open` section, "left open", "the count", "that count" — and admitting the
// base form would flip narration about the ledger itself into candidates. The
// s-form is a verb in every card occurrence measured.
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
	"govern": true, "governs": true,
	"touch": true, "touches": true,
	"sit": true, "sits": true,
	"come": true, "comes": true,
	"tell": true, "tells": true,
	"opens": true, "counts": true,
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

// NamesPublishedToken is condition 1's form (a).
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

// NamesOnlyExcludedRefs is condition 1's form (b): the sentence carries at least
// one backticked span and every span is a wi-id or a section reference. That is
// an anchor — the sentence is pinned to a measurement or a ruling — without a
// published token, which is exactly the shape the two measured misses take: the
// isolation-level sentence on pf_force_takeover (`aihub#430` … opens SERIALIZABLE
// while this one opens READ COMMITTED) and pf_get_step's record of the live
// tools_step.go falsehood, whose only backticks are `aihub#400` and `aihub#450`.
//
// ⚠️ A sentence with NO backticks at all is deliberately not this form. The
// anchor is what separates a recorded claim from narration; dropping it admits
// every plain-prose sentence with a copula, and the population stops meaning
// anything a probe wave could drain.
func NamesOnlyExcludedRefs(s string) bool {
	ms := backtickSpan.FindAllStringSubmatch(s, -1)
	if len(ms) == 0 {
		return false
	}
	for _, m := range ms {
		tok := strings.TrimSpace(m[1])
		if tok == "" || bareWorkItem.MatchString(tok) || bareSectionRef.MatchString(tok) {
			continue
		}
		return false
	}
	return true
}

// attributionVerbs is condition 1's form (c) verb subset: refusal-or-response
// verbs only. A subset of effectVerbs, and checked as ONE set deliberately — a
// member added here without being an effect verb would recognise a sentence
// IsCandidate's own condition 2 then rejects, and the two reasons would
// contradict each other in the failure text.
//
// Why not the whole effect list: with no backtick anywhere in the sentence, the
// only remaining signal is the verb, and "is"/"are"/"carries" appear in plain
// narration constantly. Measured 2026-09-10 on this card set: attribution with
// the full effect list admits 199 sentences, most of them commentary; with this
// subset it admits the response-behaviour claims ("The server 400s without
// it.", "Mints a key, stores its hash, and returns the plaintext once.") and
// little else.
var attributionVerbs = map[string]bool{
	"return": true, "returns": true,
	"answer": true, "answers": true,
	"refuse": true, "refuses": true,
	"reject": true, "rejects": true,
	"400s": true, "409s": true,
	// "counts" is the one member that is not a wire verb: "a same-size swap counts
	// as a removal" (pf_update_project) is a rule OUTCOME — how the server
	// classifies an input — measured in the same blind spot as the 400s sentence.
	// s-inflection only, for the reason effectVerbs states: "the count" is this
	// card set's everyday noun.
	"counts": true,
}

// AttributedToPrevious is condition 1's form (c): no backticks in this sentence,
// a published token named in the one before it, and a refusal-or-response verb
// here. One hop only, and the hop is not transitive — the previous sentence must
// name the token in ITS OWN text, not inherit it from a sentence before that, or
// a chain of pronouns would carry an attribution across a whole section.
func AttributedToPrevious(s, prev string) bool {
	if prev == "" || backtickSpan.MatchString(s) || !NamesPublishedToken(prev) {
		return false
	}
	for _, w := range words(strings.ToLower(s)) {
		if attributionVerbs[w] {
			return true
		}
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

// IsCandidateInContext is the recogniser. The second return value names the
// condition that failed, so a failure message can say WHY a sentence was passed
// over rather than leaving a reader to re-derive it. prev is the unit the walk
// read immediately before this one — "" at the start of a card — and is consulted
// only by condition 1's form (c).
func IsCandidateInContext(s, prev string) (bool, string) {
	if !NamesPublishedToken(s) && !NamesOnlyExcludedRefs(s) && !AttributedToPrevious(s, prev) {
		return false, "names no published token in backticks, no wi/section anchor, " +
			"and no refusal-or-response verb attributed from the sentence before it"
	}
	if !HasEffectVerb(s) {
		return false, "carries no effect-or-refusal verb"
	}
	if IsHistorical(s) {
		return false, "is written in the past tense (\"used to\")"
	}
	return true, ""
}

// IsCandidate is IsCandidateInContext with no preceding sentence — the form the
// fixtures and one-off measurements call. Everything the walk classifies goes
// through the context form, so a sentence recognised only by attribution is
// invisible here and countable there; that asymmetry is form (c)'s definition,
// not a disagreement between the two functions.
func IsCandidate(s string) (bool, string) {
	return IsCandidateInContext(s, "")
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
// repo-relative .go path, but an arm is a TEST, and `internal/domain/conflicts.go`
// in a sentence names the implementation the claim is about rather than a gate
// over it. Counting that as "probed" would retire debt by describing where the
// code lives, which is the one thing a reader of this ledger must not be able to
// do.
//
// 🔴 And the citation is RESOLVED against the tree here, not delegated. An earlier
// version of this comment claimed "both forms are already K6-resolved"; that is
// FALSE for the symbol form, because both of K6's anchor patterns require a .go
// suffix and a bare `TestSomething` in prose matches neither. Measured: citing
// `TestNoSuchProbeEverExisted` retired a claim with every arm green. Resolution is
// therefore this package's job, against an index of the test functions and test
// files the tree actually declares.
var (
	armPath   = regexp.MustCompile("`([^`\n]*_test\\.go)`")
	armSymbol = regexp.MustCompile("`(Test[A-Z][A-Za-z0-9_]*)`")
)

// ArmIndex is what the tree really declares: every top-level `func Test…` in a
// `*_test.go` file, and every `*_test.go` path, by repo-relative path and by base
// name.
type ArmIndex struct {
	Funcs map[string]bool
	Files map[string]bool
	// Built is false for the zero value, so a caller that forgot to build one gets
	// a failure rather than a silent "nothing resolves".
	Built bool
}

// BuildArmIndex walks root for test files and parses each one. go/parser rather
// than a regex over the bytes: a fixture string in a test file can contain a line
// beginning `func TestFoo(`, and a scanner that counted it would let a citation
// resolve against a name that exists only inside a string literal — which is the
// same class of hole this function exists to close.
func BuildArmIndex(root string) (ArmIndex, error) {
	idx := ArmIndex{Funcs: map[string]bool{}, Files: map[string]bool{}, Built: true}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		idx.Files[filepath.ToSlash(rel)] = true
		idx.Files[d.Name()] = true

		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", rel, perr)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			idx.Funcs[fn.Name.Name] = true
		}
		return nil
	})
	return idx, err
}

// citationNegation matches the phrases that turn a citation into a statement about
// what an arm does NOT hold.
//
// The live case is in the spec's own sample: "`TestReadIntentTakesNoWriteLock`
// holds the lock derivation, not the prediction's answer." That sentence names an
// arm and is precisely a claim that the arm does not cover the sentence's subject.
//
// 🔴 Tight, and measured tight rather than assumed. A first version banned any
// "rather than", "nothing", "deliberately", "is not" and reddened BOTH of the two
// genuinely-cited sentences in the scoped set: pf_get_ready_queue's "a checked
// property RATHER THAN a fact of the current implementation" (a contrast, not a
// negation) and a sentence whose only "nothing" was inside the cited test's own
// NAME, TestReadyQueueOmitsTheDisclosureKeyWhenNothingWasAdjusted. Precision
// matters in both directions here: a false positive puts a genuinely-probed claim
// back into debt, so this is not a place where over-reporting is free.
//
// Backticked spans are stripped before the scan for the second of those reasons —
// a test name is not prose about the test.
var citationNegation = regexp.MustCompile(
	`(?i),\s+not\s+` +
		`|\bdoes\s+not\s+(?:hold|cover|check|pin|assert|reach)\b` +
		`|\bnot\s+(?:held|covered|pinned|checked|asserted)\s+by\b` +
		`|\bno\s+arm\b` +
		`|\bnothing\s+(?:holds|pins|checks|covers|asserts)\b`)

// CitesAnArm is the "assertable + probed" test. It returns whether the sentence
// retires its own debt, plus any problem with the citation itself.
func CitesAnArm(card, s string, idx ArmIndex) (bool, []string) {
	if !idx.Built {
		return false, []string{
			"K12 ARM_INDEX_MISSING: CitesAnArm was called with no arm index, so no citation " +
				"can resolve and every cited sentence would count as debt. Build one with " +
				"BuildArmIndex; a zero index is a caller mistake, not an empty tree."}
	}

	var problems []string
	found := false
	for _, m := range armPath.FindAllStringSubmatch(s, -1) {
		name := strings.TrimPrefix(strings.TrimSpace(m[1]), "./")
		if idx.Files[name] || idx.Files[filepath.Base(name)] {
			found = true
			continue
		}
		problems = append(problems, unresolvedCitation(card, name, s, "test file"))
	}
	for _, m := range armSymbol.FindAllStringSubmatch(s, -1) {
		if idx.Funcs[m[1]] {
			found = true
			continue
		}
		problems = append(problems, unresolvedCitation(card, m[1], s, "test function"))
	}
	if !found {
		return false, problems
	}

	if hit := citationNegation.FindString(stripBackticked(s)); hit != "" {
		problems = append(problems, fmt.Sprintf(
			"K12 CITATION_IN_NEGATIVE: %s names an arm inside a sentence carrying %q:\n"+
				"    %s\nA sentence saying what an arm does NOT hold is not a sentence the arm "+
				"holds, so this does not retire the claim and it is counted as unclassified. "+
				"If the arm does hold it, say so without the negation; if it does not, the "+
				"sentence needs its own probe or a marker.",
			card, strings.TrimSpace(hit), truncate(s)))
		return false, problems
	}
	return true, problems
}

func unresolvedCitation(card, name, sentence, what string) string {
	return fmt.Sprintf(
		"K12 ARM_CITATION_UNRESOLVED: %s cites %s `%s`, which this tree does not declare:\n"+
			"    %s\nA citation is the only way a sentence retires its own debt without a "+
			"marker, so an unresolvable one clears the ledger while nothing holds the claim. "+
			"K6 does NOT cover this — both of its anchor patterns require a .go suffix, so a "+
			"bare Test symbol in prose resolves nowhere before this arm. Name a test that "+
			"exists, or file a marker.", card, what, name, truncate(sentence))
}

// ────────────────────────────── reading a card ───────────────────────────────

// Sentence is one unit of card prose, with whatever markers were attached to it.
type Sentence struct {
	Card    string
	Text    string
	Start   int
	Markers []Marker
	// Prev is the text of the unit the walk read immediately before this one —
	// INCLUDING units below the fragment floor, because attribution is about what
	// a reader just read, and the floor is about what is worth counting. "" for
	// the first unit of a card. Condition 1's form (c) is its only consumer.
	Prev string
}

// minSentenceLen is the fragment floor. Anything shorter is something the splitter
// produced rather than a claim somebody wrote.
const minSentenceLen = 15

// sentenceStarts are the runes, besides an uppercase letter or a digit, that may
// begin a sentence.
//
// 🔴 Measured against the card set rather than copied from the spec's §0.1 sizer,
// which this walk used to reproduce exactly. That sizer's start class is
// [A-Z🔴⚠`*_], and §1.3 says in as many words what that costs: "The sentence
// splitter merges adjacent bullets … It is a population sizer, not the unit
// definition; the unit is the sentence-or-bullet a human reads. Any arm built on
// it must therefore be a floor, never an equality."
//
// It is not a floor here, it is the population, and the merging was measured to be
// load-bearing: in pf_claim_work_item alone, 9 of 36 units spanned several bullets,
// so ONE citation retired several claims at once — and a "used to" anywhere in a
// merged run ejected every live claim merged with it. So bullets are hard
// boundaries (see bulletPrefix) and the start class is widened.
const sentenceStarts = "🔴🟡🟢🟠🔵⚠✅❌`*_-+([\"'"

// bulletPrefix reports whether a line opens a new list item, which this walk
// treats as a hard sentence boundary regardless of what preceded it.
func bulletPrefix(line string) bool {
	if len(line) > 2 && (line[0] == '-' || line[0] == '*' || line[0] == '+') &&
		(line[1] == ' ' || line[1] == '\t') {
		return true
	}
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(line) && (line[i] == '.' || line[i] == ')') && line[i+1] == ' '
}

// CardRead is one card's prose, split and with every marker placed.
type CardRead struct {
	// Sentences are the units above the fragment floor.
	Sentences []Sentence
	// Orphans sat on a line the walk does not read — a heading, a table
	// separator row, a fenced block — or before any sentence at all.
	Orphans []Marker
	// Dropped resolved to a unit BELOW the fragment floor.
	//
	// 🔴 Separate from Orphans because the failure is different and used to be
	// silent: the marker had a host, the host was filtered out for being short, and
	// the marker then attached to whichever sentence happened to precede it. That is
	// a classification landing on a claim nobody wrote it for.
	Dropped []Marker
	// Unrecognised are marker-shaped comments whose name is not one of the two.
	Unrecognised []string
}

// ReadCard splits a card's prose into sentences and attaches every marker to the
// sentence it sits on.
//
// The walk drops fenced blocks, table separator rows and headings, joins what is
// left, and cuts it at sentence-final punctuation, at every list-item boundary,
// and at both edges of every table row.
//
// 🔴 A marker classifies the sentence it FOLLOWS, so the attachment is the last
// unit that STARTS strictly before it. Strictly, because a marker written on its
// own line between two sentences sits at exactly the second one's start offset,
// and reading it as classifying what it precedes puts every trailing marker on the
// wrong claim.
func ReadCard(card, prose string) CardRead {
	joined, markers, out := joinProse(prose)
	spans := splitSentences(joined, out.cuts)

	// Placement runs over EVERY unit, including the sub-floor fragments, so a
	// marker's host is the unit it really follows rather than the nearest survivor.
	type unit struct {
		text string
		keep bool
		idx  int
	}
	units := make([]unit, 0, len(spans))
	read := CardRead{Orphans: out.orphans, Unrecognised: out.unrecognised}
	prev := ""
	for _, sp := range spans {
		text := strings.TrimSpace(joined[sp.start:sp.end])
		keep := len([]rune(text)) > minSentenceLen
		u := unit{text: text, keep: keep, idx: -1}
		if keep {
			u.idx = len(read.Sentences)
			read.Sentences = append(read.Sentences,
				Sentence{Card: card, Text: text, Start: sp.start, Prev: prev})
		}
		units = append(units, u)
		if text != "" {
			prev = text
		}
	}

	for _, m := range markers {
		host := -1
		for i, sp := range spans {
			if sp.start < m.Offset {
				host = i
				continue
			}
			break
		}
		switch {
		case host < 0:
			read.Orphans = append(read.Orphans, m)
		case !units[host].keep:
			read.Dropped = append(read.Dropped, m)
		default:
			s := &read.Sentences[units[host].idx]
			s.Markers = append(s.Markers, m)
		}
	}
	return read
}

type span struct{ start, end int }

// walkOut carries what joinProse found besides the text itself.
type walkOut struct {
	orphans      []Marker
	unrecognised []string
	cuts         []int
}

// joinProse drops what the walk does not read and extracts every marker as it
// goes, recording each marker's offset in the joined text.
//
// 🔴 The line is cleaned and TRIMMED before an offset is taken from it. Doing it
// the other way — the shape this had first — makes every offset on a line with a
// leading marker too large by the width of the whitespace the trim removes, so the
// second marker on such a line lands past the start of the next sentence and the
// strict-< placement puts it on the wrong claim. Measured at 82 against 81 and 36
// against 34 before the order was fixed.
//
// 🔴 TABLE ROWS ARE READ since aihub#591. This walk used to skip every |-prefixed
// line, and the skip was measured to hide 46 candidate-assertable claims across
// the 45 cards — the hop 0-1 rows where each parameter's published meaning is
// written, i.e. the sentences a caller reads FIRST. They were not merely
// unclassified: never split into units, they could not be counted OR waived, and
// a marker on one was reported as MARKER_ORPHAN. A row now enters the walk as a
// hard-bounded unit — a cut at its start and a cut after its end — because a row
// is a cell list, not a clause of whatever prose surrounds it. Only the
// |---|---| SEPARATOR rows stay outside: they are table syntax, not prose, the
// way a fence line is.
func joinProse(prose string) (string, []Marker, walkOut) {
	var sb strings.Builder
	var markers []Marker
	var out walkOut
	inFence, cutNext := false, false

	note := func(text string) {
		out.orphans = append(out.orphans, parseMarkers(text, -1)...)
		out.unrecognised = append(out.unrecognised, unrecognisedIn(text)...)
	}

	for _, line := range logicalLines(prose) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			note(trimmed)
			continue
		}
		isRow := strings.HasPrefix(trimmed, "|")
		if inFence || trimmed == "" || strings.HasPrefix(trimmed, "#") ||
			(isRow && tableSeparatorRow(trimmed)) {
			note(trimmed)
			continue
		}

		out.unrecognised = append(out.unrecognised, unrecognisedIn(trimmed)...)
		clean, found := cleanLine(trimmed)
		if clean == "" {
			// The line held nothing but markers. They belong to whatever the joined
			// text already ends with, which is where the reader met them.
			for i := range found {
				found[i].Offset = sb.Len()
			}
			markers = append(markers, found...)
			continue
		}
		base := sb.Len()
		if base > 0 {
			sb.WriteByte(' ')
			base++
		}
		sb.WriteString(clean)
		if cutNext || isRow || bulletPrefix(clean) {
			out.cuts = append(out.cuts, base)
		}
		cutNext = isRow
		for i := range found {
			found[i].Offset += base
		}
		markers = append(markers, found...)
	}
	return sb.String(), markers, out
}

// tableSeparatorRow reports whether a |-prefixed line is a markdown alignment
// separator — cells holding only dashes and colons — which is table SYNTAX and
// carries no prose. Everything else that starts with | is a row of cells and is
// read.
func tableSeparatorRow(line string) bool {
	for _, r := range line {
		switch r {
		case '|', '-', ':', ' ', '\t':
		default:
			return false
		}
	}
	return true
}

// cleanLine strips every marker from one line and returns the trimmed remainder
// plus each marker's offset WITHIN that remainder.
func cleanLine(line string) (string, []Marker) {
	locs := markerRe.FindAllStringSubmatchIndex(line, -1)
	if len(locs) == 0 {
		return strings.TrimSpace(line), nil
	}
	var sb strings.Builder
	var out []Marker
	prev := 0
	for _, l := range locs {
		sb.WriteString(line[prev:l[0]])
		m := parseMarker(line[l[0]:l[1]], line[l[2]:l[3]], line[l[4]:l[5]])
		m.Offset = sb.Len()
		out = append(out, m)
		prev = l[1]
	}
	sb.WriteString(line[prev:])

	raw := sb.String()
	trimmed := strings.TrimSpace(raw)
	lead := len(raw) - len(strings.TrimLeft(raw, " \t"))
	for i := range out {
		o := out[i].Offset - lead
		if o < 0 {
			o = 0
		}
		if o > len(trimmed) {
			o = len(trimmed)
		}
		out[i].Offset = o
	}
	return trimmed, out
}

// logicalLines coalesces a marker comment that spans several physical lines into
// one, so a reason long enough to need wrapping is still one marker.
//
// ⚠️ Markdown line breaks are a rendering artifact — the same argument K11's
// cardOpenBullets makes for joining a bullet's lines before scanning it.
//
// 🔴 Openness is decided by scanning the WHOLE line, not by its first `<!--`.
// Deciding on the first one means a line already carrying a closed comment — K9's
// `<!-- historical -->` is one, and lives in these very cards — never coalesces a
// second, wrapped marker that starts after it: the marker then silently fails to
// parse and the sentence reads as unclassified with no finding anywhere.
//
// 🔴 And `<!--` inside an inline code span does not open anything. A card
// documenting this syntax writes the token in backticks, and treating that as a
// comment swallows every fence and table after it to the end of the file.
func logicalLines(prose string) []string {
	var out []string
	var pending []string
	inFence, open := false, false
	for _, line := range strings.Split(prose, "\n") {
		trimmed := strings.TrimSpace(line)
		if open {
			pending = append(pending, trimmed)
			if open = scanComment(trimmed, true); !open {
				out = append(out, strings.Join(pending, " "))
				pending = nil
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			out = append(out, line)
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}
		if scanComment(line, false) {
			open = true
			pending = []string{strings.TrimRight(line, " \t")}
			continue
		}
		out = append(out, line)
	}
	if open {
		// Unterminated. Returned as-is: markerRe then fails to match, the sentence
		// reads as unclassified and the ledger row stops matching, which is red.
		out = append(out, strings.Join(pending, " "))
	}
	return out
}

// scanComment reports whether the line LEAVES an HTML comment open, given whether
// one was open when it started. Backtick code spans are skipped while outside a
// comment; inside one, a backtick is ordinary text.
func scanComment(line string, open bool) bool {
	inCode := false
	for i := 0; i < len(line); {
		if open {
			if strings.HasPrefix(line[i:], "-->") {
				open = false
				i += 3
				continue
			}
			i++
			continue
		}
		if line[i] == '`' {
			inCode = !inCode
			i++
			continue
		}
		if !inCode && strings.HasPrefix(line[i:], "<!--") {
			open = true
			i += 4
			continue
		}
		i++
	}
	return open
}

// unrecognisedIn returns the raw text of every marker-shaped comment whose name is
// not one this package knows.
func unrecognisedIn(text string) []string {
	var out []string
	for _, l := range markerShapedRe.FindAllStringSubmatchIndex(text, -1) {
		name := text[l[2]:l[3]]
		if name == MarkerWaiver || name == MarkerProseOnly {
			continue
		}
		out = append(out, text[l[0]:l[1]])
	}
	return out
}

// parseMarkers is the no-offset path, for text the walk does not place.
func parseMarkers(s string, offset int) []Marker {
	var out []Marker
	for _, l := range markerRe.FindAllStringSubmatchIndex(s, -1) {
		m := parseMarker(s[l[0]:l[1]], s[l[2]:l[3]], s[l[4]:l[5]])
		m.Offset = offset
		out = append(out, m)
	}
	return out
}

// parseMarker reads the pipe-separated key=value body.
//
// 🔴 `reason` is recognised as a FIELD KEY and then takes every remaining field,
// never as a substring of the body. Cutting on the first "reason=" anywhere means a
// citation reading `the reason=… argument` truncates the entry at that point and
// files whatever followed as the reason, silently.
func parseMarker(raw, form, body string) Marker {
	m := Marker{Form: form, Raw: raw}
	parts := strings.Split(body, "|")
	seen := map[string]bool{}

	set := func(k, v string) {
		if seen[k] {
			m.Duplicated = append(m.Duplicated, k)
			return
		}
		seen[k] = true
		switch k {
		case "kind":
			m.Kind = WaiverKind(v)
		case "because":
			m.Because = ProseOnlyBecause(v)
		case "decided":
			m.Decided = v
		case "citation":
			m.Citation = v
		case "reason":
			m.Reason = v
		}
	}

	for i := 0; i < len(parts); i++ {
		part := strings.TrimSpace(parts[i])
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if !markerFields[k] {
			m.Unknown = append(m.Unknown, k)
			continue
		}
		if k == "reason" {
			if i+1 < len(parts) {
				v = strings.TrimSpace(v + "|" + strings.Join(parts[i+1:], "|"))
			}
			set(k, v)
			break
		}
		set(k, v)
	}
	return m
}

// splitSentences cuts on sentence-final punctuation followed by whitespace and a
// sentence-start rune, and at every offset in forced (the list-item boundaries).
// Hand-rolled because Go's regexp has no lookaround.
//
// 🔴 Closing formatting runes may sit between the punctuation and the whitespace
// — `.**`, `.)`, `."` — and the cut happens anyway (aihub#591). Without this, a
// bold-terminated sentence merges with everything after it, and the merge is not
// cosmetic: a "used to" in the bold half ejected every LIVE claim merged behind
// it. Measured on pf_list_dependencies: "**`Accessible` is a role comparison,
// and it used to be the wrong one.** It is now computed with … (`RoleLevel`)"
// was ONE unit, so the live RoleLevel claim was outside the population entirely
// — the merged-bullet ejection, the same defect class the forced list boundaries
// fixed for bullets.
var sentenceClosers = "*_\"')]`"

func splitSentences(s string, forced []int) []span {
	rs := []rune(s)
	offs := make([]int, len(rs)+1)
	b := 0
	for i, r := range rs {
		offs[i] = b
		b += len(string(r))
	}
	offs[len(rs)] = b

	starts := map[int]bool{0: true}
	for _, f := range forced {
		if f > 0 && f < len(s) {
			starts[f] = true
		}
	}
	for i := 0; i < len(rs); i++ {
		if rs[i] != '.' && rs[i] != '!' && rs[i] != '?' {
			continue
		}
		j := i + 1
		for j < len(rs) && strings.ContainsRune(sentenceClosers, rs[j]) {
			j++
		}
		ws := j
		for j < len(rs) && unicode.IsSpace(rs[j]) {
			j++
		}
		if j == ws || j >= len(rs) {
			continue
		}
		n := rs[j]
		if !unicode.IsUpper(n) && !unicode.IsDigit(n) && !strings.ContainsRune(sentenceStarts, n) {
			continue
		}
		starts[offs[j]] = true
	}

	keys := make([]int, 0, len(starts))
	for k := range starts {
		keys = append(keys, k)
	}
	sort.Ints(keys)

	out := make([]span, 0, len(keys))
	for i, k := range keys {
		end := len(s)
		if i+1 < len(keys) {
			end = keys[i+1]
		}
		out = append(out, span{k, end})
	}
	return out
}

// ScanAll finds every marker anywhere in a text, with no placement — the arm for
// asking "does this file carry a classification at all".
//
// skipFences is the difference between the two questions it answers. A SCOPED card
// is walked, so a marker inside a fence there is an orphan and is reported by the
// walk; an UNSCOPED card may legitimately quote the syntax in a code sample, and
// flagging that would make documenting the marker impossible.
func ScanAll(text string, skipFences bool) ([]Marker, []string) {
	var keep []string
	inFence := false
	for _, line := range logicalLines(text) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence && skipFences {
			continue
		}
		keep = append(keep, trimmed)
	}
	joined := strings.Join(keep, "\n")
	return parseMarkers(joined, -1), unrecognisedIn(joined)
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
func Classify(s Sentence, idx ArmIndex) (Class, []string) {
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

	candidate, why := IsCandidateInContext(s.Text, s.Prev)
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

	cited, citeProblems := CitesAnArm(s.Card, s.Text, idx)
	problems = append(problems, citeProblems...)
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
func Tally(card, prose string, idx ArmIndex) CardTally {
	read := ReadCard(card, prose)
	t := CardTally{Sentences: len(read.Sentences)}

	for _, m := range read.Orphans {
		t.Problems = append(t.Problems, fmt.Sprintf(
			"K12 MARKER_ORPHAN: %s carries %s on a line this walk does not read — a heading, a "+
				"table separator row or a fenced block — or ahead of every sentence in the card. "+
				"A marker classifies the SENTENCE it sits on, so one that sits on no sentence "+
				"exempts nothing while looking like it does. Move it onto the prose it is about. "+
				"(Table ROWS are read since aihub#591, so a marker in a cell is placed, not "+
				"orphaned.)",
			card, m.Raw))
		t.Problems = append(t.Problems, m.Problems(card)...)
	}
	for _, m := range read.Dropped {
		t.Problems = append(t.Problems, fmt.Sprintf(
			"K12 MARKER_TARGET_DROPPED: %s carries %s immediately after a fragment shorter than "+
				"the %d-character floor, so the unit it classifies is not in the population. "+
				"This used to be silent — the marker attached to whatever longer sentence "+
				"happened to precede it, which is a classification landing on a claim nobody "+
				"wrote it for. Put the marker after the sentence it is about.",
			card, m.Raw, minSentenceLen))
		t.Problems = append(t.Problems, m.Problems(card)...)
	}
	for _, raw := range read.Unrecognised {
		name := "?"
		if l := markerShapedRe.FindStringSubmatch(raw); len(l) > 1 {
			name = l[1]
		}
		t.Problems = append(t.Problems, unrecognisedMarkerProblem(card, raw, name))
	}

	for _, s := range read.Sentences {
		class, problems := Classify(s, idx)
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

// classifyDrift names the difference by what MOVED, because the cases are
// different edits and the message has to name the right one.
//
// 🔴 The RECLASSIFIED case exists because the first version of this function
// accused the most ordinary operation in the whole workflow of being the worst
// one. Putting a legitimate marker on a grandfathered sentence moves it out of
// Unclassified and into a classified column — and the old vector read "some column
// rose" as growth, so the commonest action in a probe wave was reported as "a card
// sentence now asserts something with neither a cited arm nor a named marker",
// which is false in both halves. Only a rise in UNCLASSIFIED is new unheld debt.
func classifyDrift(measured, recorded Census) (finding, explain string) {
	classified := func(c Census) []int {
		return []int{c.PendingImplementation, c.KnownDefect, c.StructurallyUnreachable,
			c.AcceptedUnprobed, c.ProseOnly}
	}
	m, r := classified(measured), classified(recorded)
	rose, fell := false, false
	for i := range m {
		if m[i] > r[i] {
			rose = true
		}
		if m[i] < r[i] {
			fell = true
		}
	}
	unheld := measured.Unclassified - recorded.Unclassified

	switch {
	case unheld > 0:
		return "K12 DEBT_GROWTH", "UNHELD debt grew: a card sentence now asserts something " +
			"with neither a cited arm nor a named marker, which is exactly the event 30+ " +
			"false statements walked through unseen across the 2026-09-08 and 2026-09-09 " +
			"waves. Write the probe, or file the marker and re-pin this row in a diff " +
			"somebody signs."
	case rose:
		return "K12 RECLASSIFIED", "No unheld debt was added — a sentence moved between " +
			"classes. That is the ordinary result of filing a marker on a grandfathered " +
			"sentence, and it is also what a relabel between waiver kinds looks like, which " +
			"is the escape the per-kind columns exist to expose. Re-pin the row, and say in " +
			"the diff which of the two it was."
	case unheld < 0 || fell:
		return "K12 STALE_DEBT", "A gap has CLOSED and nothing took its place. Lower the row " +
			"in the same change, or the vacated slot lets the next unclassified sentence in " +
			"silently."
	default:
		return "K12 POPULATION_MOVED", "No class count moved — the assertable population or " +
			"the cited count did. That is the swap this row exists to expose: a change adding " +
			"one assertable sentence while citing one previously-unclassified sentence nets " +
			"to zero across the class columns and would otherwise pass green, which is a new " +
			"unheld claim arriving under cover of somebody else's probe. Read both halves " +
			"before pasting."
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
	return slices.Sorted(maps.Keys(m))
}

func sortedCensusKeys(m map[string]Census) []string {
	return slices.Sorted(maps.Keys(m))
}
