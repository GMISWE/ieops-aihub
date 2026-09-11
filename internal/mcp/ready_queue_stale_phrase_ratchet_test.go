package mcp_test

// aihub#542 — the whole-repo string ratchet for the ready queue's segment count.
//
// ─── Why a ratchet, when four copies are already gated ───────────────────────
//
// The COUNT has four load-bearing copies (struct / schema / design doc / skill
// doc) and ready_queue_section_count_test.go pins all four to the struct. But
// the stale WORDINGS never lived only in the four copies: aihub#493 chased
// "six-segment" and "LCRS (6-section)" residuals through usage.md's generator
// (internal/cli/init.go), the /ui/queue header, an integration test's opening
// comment and two skill-chain scenario docs; after all that one more survived
// in the pf-status skill doc until an aihub#478 rider retired it, and another
// ("all six LCRS sections", internal/server/ui_handlers_queue_test.go) sat
// unnoticed until this wi found it while building this test. A copy of a number
// can land in ANY file, so only a repo walk refuses the next one everywhere at
// once.
//
// ─── Scope: the attested stale forms, not every number ───────────────────────
//
// staleSegmentWordings matches the four families that actually rotted:
// "six segments"/"6 segments"/"six-segment" (the skill-doc and usage.md form),
// "six LCRS sections/segments" (the ui_handlers_queue_test.go form),
// "6-section"/"six-section" (the pre-aihub#449 schema form and its audit
// translation), and the design doc's Chinese "六段". Deliberate non-goals:
//
//   - bare "six sections" — the card template legitimately HAS six sections
//     (K4's subject, a true count of a different thing), so refusing the words
//     would red the gate on a truth, and the cheap repair for that is deleting
//     the gate (aihub#361, via measured_floor_comment_gate_test.go).
//   - the pf-status "three-segment" RENDER format — a different number that has
//     nothing to do with how many sections the queue carries.
//   - wrong counts other than the six family — the four-copy gate owns general
//     consistency, and scripts/pf_contract_lint.py Rule C (SEGMENT_COUNT_DRIFT,
//     aihub#599) owns context-aware count claims in the plugins corpus.
//
// ─── The allowlist: history is allowed to say "six" ──────────────────────────
//
// The surviving mentions are frozen audit records, the design doc's errata (its
// own text says the v1.20-era「六段」is deliberately kept), quotes of the
// pre-aihub#449/#493 wordings next to their corrections, one design-time
// prototype, and Rule C's positive lint fixtures. Each is pinned per file, per
// normalized phrase, with an EXACT count and the reason. The pin is a ratchet:
// one mention more is a regression (fix the wording, do not raise the pin), one
// mention fewer is a stale pin (lower it in the same change — headroom under a
// pin is where the next regression hides).
//
// No database needed:
//
//	GOWORK=off go test ./internal/mcp/ -run TestReadyQueueStale -count=1 -v

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	// ratchetSelfPath is this file, which the walk skips: the patterns and the
	// allowlist below necessarily spell the wordings they refuse, and a gate
	// that reports its own definition of what it looks for is the failure
	// declaresArmLabel documents (measured_floor_comment_gate_test.go). The
	// cost is one blind file, accepted because a stale claim landing here lands
	// next to the machinery that names it stale.
	ratchetSelfPath = "internal/mcp/ready_queue_stale_phrase_ratchet_test.go"

	// ratchetScannedFloor guards the walk itself: a walk that reads nothing
	// reports every wording as gone, which is the vacuous pass every sibling
	// floor in this package refuses. Well under the tree's file count (the
	// walk's own log line prints the current number), so it fails on a broken
	// walk rather than on ordinary file deletion.
	ratchetScannedFloor = 500
)

// staleSegmentWordings are the stale-form patterns, one per attested family.
// Matches are deduplicated by span, so overlap between patterns cannot double
// count.
var staleSegmentWordings = []*regexp.Regexp{
	// "six segments", "six-segment", "6 segments", "6-segment" — \s includes
	// newlines on purpose: the phrase can wrap in markdown prose.
	regexp.MustCompile(`(?i)\b(?:six|6)[\s-]segments?\b`),
	// "six LCRS sections" / "six LCRS segments" — the interposed-LCRS form.
	regexp.MustCompile(`(?i)\b(?:six|6)[\s-]lcrs[\s-](?:segments?|sections?)\b`),
	// "6-section", "six-section" — hyphenated only. The space form belongs to
	// the card template (see the non-goals above).
	regexp.MustCompile(`(?i)\b(?:six|6)-sections?\b`),
	// The design doc's Chinese form.
	regexp.MustCompile(`六段`),
}

// stalePhrasePin is one allowlisted historical mention: how many times a
// normalized phrase may appear in one file, and why those mentions are
// legitimate.
type stalePhrasePin struct {
	n   int
	why string
}

// staleSegmentAllowlist pins every mention the tree is allowed to keep,
// file → normalized phrase → pin. Normalization is lowercase with whitespace
// collapsed, so "Six\nSegments" and "six segments" are one entry.
var staleSegmentAllowlist = map[string]map[string]stalePhrasePin{
	"internal/mcp/ready_queue_section_count_test.go": {
		"six segments": {n: 2, why: "the gate over the four copies quotes the drift it was " +
			"built on: the skill doc said \"six segments\" from aihub#449 until 1.1.31 (aihub#560)"},
	},
	"scripts/pf_contract_lint.py": {
		"six segments": {n: 2, why: "Rule C's positive fixture (SEGMENT_COUNT_DRIFT, aihub#599): " +
			"the stale wording is the test input that proves the lint fires on it, once in the " +
			"fixture text and once in the expected-violation tuple"},
	},
	"internal/server/ui_handlers_queue.go": {
		"six-segment": {n: 1, why: "the /ui/queue header quotes its own pre-aihub#493 wording " +
			"(\"six-segment LCRS ready-queue view\") to record why the sentence had to go"},
	},
	"internal/domain/work_items.go": {
		"6-section": {n: 1, why: "ReadyQueue's comment quotes the pre-aihub#449 schema wording " +
			"(\"LCRS (6-section)\") next to the correction it explains"},
	},
	"internal/mcp/tools_lifecycle.go": {
		"6-section": {n: 1, why: "the pf_get_ready_queue schema comment quotes the wording this " +
			"string carried until aihub#449 (aihub#411 T2-20)"},
	},
	"docs/audits/aihub-385-mcp-contract-audit-batch1.md": {
		"6-section":   {n: 1, why: "frozen audit record: R1 quotes the then-live description"},
		"six-section": {n: 1, why: "frozen audit record: R4 translates the design doc's 六段视图"},
		"六段":          {n: 1, why: "frozen audit record: R4 quotes the design doc's v1.20 comment"},
	},
	"docs/audits/aihub-411-design-decision-table.md": {
		"6-section": {n: 1, why: "frozen audit record: T2-20 quotes the then-live \"LCRS " +
			"(6-section)\" description it measured"},
	},
	"docs/design/polyforge-v1-design.md": {
		"六段": {n: 5, why: "the errata table (#11) and the v1.20-era lines it corrects; the doc " +
			"itself marks the old wording 刻意保留 (deliberately kept) as the record of what " +
			"v1.20 said"},
	},
	"docs/design/aihub-129/prototype.html": {
		"six-segment": {n: 1, why: "aihub#129's design-time prototype, a frozen artifact of what " +
			"was proposed; what shipped is the four-count strip (internal/server/ui_handlers_queue.go)"},
	},
}

// normalizeStaleMatch lowercases and collapses whitespace so a wrapped or
// oddly-cased mention lands on the same allowlist key.
func normalizeStaleMatch(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// TestReadyQueueStaleSegmentWordingIsGone walks the repository and refuses
// every match of staleSegmentWordings that is not pinned in
// staleSegmentAllowlist, in both directions: a hit without a pin (or over its
// pin) is a regression, and a pin without its hits is stale headroom.
func TestReadyQueueStaleSegmentWordingIsGone(t *testing.T) {
	segments := readyQueueSegments(t)
	if len(segments) == 6 {
		t.Fatalf("STALE_SEGMENT_COUNT PREMISE_INVERTED: domain.ReadyQueue is back to six " +
			"segments, so the wordings this ratchet refuses would now be TRUE. Rework or retire " +
			"this test in the same change that resegmented the queue — a gate that reds on the " +
			"truth gets deleted, which is worse than no gate (aihub#361).")
	}

	counts := map[string]map[string]int{}
	lines := map[string]map[string][]int{}
	scanned := 0

	err := filepath.WalkDir(cardsRepoRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(cardsRepoRoot, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if rel == ratchetSelfPath {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		sniff := raw
		if len(sniff) > 8000 {
			sniff = sniff[:8000]
		}
		if bytes.IndexByte(sniff, 0) >= 0 {
			return nil // binary; the wordings this walk refuses are prose
		}
		scanned++
		text := string(raw)
		seen := map[[2]int]bool{}
		for _, re := range staleSegmentWordings {
			for _, m := range re.FindAllStringIndex(text, -1) {
				span := [2]int{m[0], m[1]}
				if seen[span] {
					continue
				}
				seen[span] = true
				phrase := normalizeStaleMatch(text[m[0]:m[1]])
				if counts[rel] == nil {
					counts[rel] = map[string]int{}
					lines[rel] = map[string][]int{}
				}
				counts[rel][phrase]++
				lines[rel][phrase] = append(lines[rel][phrase],
					1+strings.Count(text[:m[0]], "\n"))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v — an unreadable tree makes every zero below meaningless, so this "+
			"is a failure rather than a smaller census", cardsRepoRoot, err)
	}
	if scanned < ratchetScannedFloor {
		t.Errorf("STALE_SEGMENT_COUNT WALK_BROKEN: only %d file(s) scanned, floor is %d — a "+
			"walk that reads nothing reports every stale wording as gone", scanned,
			ratchetScannedFloor)
	}

	// Direction one: no hit outside (or over) its pin.
	total := 0
	for rel, phrases := range counts {
		for phrase, n := range phrases {
			total += n
			pin, ok := staleSegmentAllowlist[rel][phrase]
			if !ok {
				t.Errorf("STALE_SEGMENT_COUNT NEW_HIT: %s says %q (line %v). domain.ReadyQueue "+
					"has marshalled %d segments since aihub#449, and this wording is one "+
					"aihub#493/#542 already chased out of the tree once. Fix the wording; if "+
					"the mention is a dated historical record (a frozen audit, an errata line, "+
					"a quote of the old text next to its correction), pin it in "+
					"staleSegmentAllowlist with the reason instead.", rel, phrase,
					lines[rel][phrase], len(segments))
				continue
			}
			if n > pin.n {
				t.Errorf("STALE_SEGMENT_COUNT PIN_EXCEEDED: %s says %q %d time(s) (lines %v); "+
					"the pin allows %d (%s). A ratchet only tightens: fix the new mention "+
					"rather than raising the pin — raising it declares a NEW claim historical, "+
					"which no new claim is.", rel, phrase, n, lines[rel][phrase], pin.n, pin.why)
			}
		}
	}

	// Direction two: every pin is exactly spent — a pin nothing matches is
	// headroom for the next regression, and headroom is what a ratchet gives up.
	for rel, phrases := range staleSegmentAllowlist {
		for phrase, pin := range phrases {
			if n := counts[rel][phrase]; n < pin.n {
				t.Errorf("STALE_SEGMENT_COUNT PIN_STALE: staleSegmentAllowlist pins %q ×%d in "+
					"%s (%s) and the tree has %d. Lower or delete the pin in the same change, "+
					"so the ratchet stays tight.", phrase, pin.n, rel, pin.why, n)
			}
		}
	}

	if !t.Failed() {
		t.Logf("stale-wording ratchet: %d file(s) scanned, %d mention(s) all pinned as "+
			"historical, 0 outside the allowlist; the queue marshals %d segments",
			scanned, total, len(segments))
	}
}

// TestReadyQueueStalePatternDiscriminates pins staleSegmentWordings in BOTH
// directions on real text from this repository, the same contract as
// TestMeasuredFloorPatternDiscriminates: the negative half is load-bearing,
// because a pattern that fires on the card template's true "six sections" (or
// on the three-segment render format) reds a correct tree, and the cheap
// repair for that is deleting the gate (aihub#361).
func TestReadyQueueStalePatternDiscriminates(t *testing.T) {
	matches := func(s string) bool {
		for _, re := range staleSegmentWordings {
			if re.MatchString(s) {
				return true
			}
		}
		return false
	}

	mustMatch := []struct{ name, text string }{
		{"the skill-doc form", "Inspect the project-wide ready queue (LCRS six segments)."},
		{"the usage.md form aihub#493 fixed", "/pf-status  # LCRS six-segment ready queue"},
		{"the digit form", "the ready queue returns 6 segments in one call"},
		{"the schema's pre-aihub#449 form", `Description "LCRS (6-section) ready queue"`},
		{"the audit's translation of 六段视图", "its comment says a six-section view"},
		{"the interposed-LCRS form this wi found live", "renders all six LCRS sections"},
		{"the design doc's Chinese form", "-- v1.20：六段视图（加 unclassified[]）"},
		{"a mention wrapped across a line break", "the queue's six\nsegments"},
	}
	for _, c := range mustMatch {
		if !matches(c.text) {
			t.Errorf("staleSegmentWordings MISSED %s: %q. Every one of these is a form that "+
				"was live in this repository once; a pattern that misses one lets it back in.",
				c.name, c.text)
		}
	}

	mustNotMatch := []struct{ name, text string }{
		{"the card template's six sections — K4's subject, a true count of a different thing",
			"The six sections have to be PRESENT, NON-EMPTY and IN ORDER."},
		{"the pf-status render format", "Output three-segment format."},
		{"the current count", "Returns all seven segments in one call."},
		{"a six counting something else entirely", "six bare headings and a hop-4 body"},
		{"the six-plus-one framing beside the segment walk", "the six-plus-one queue sections"},
		{"segments of something that is not the queue", "owner, repo = the last two path segments of the URL"},
		{"the historical comparison wording", "So the schema says 6, the struct has 7"},
	}
	for _, c := range mustNotMatch {
		if matches(c.text) {
			t.Errorf("staleSegmentWordings FALSE POSITIVE on %s: %q. This text is correct as "+
				"written and lives in the files this walk reads, so the ratchet would be red "+
				"on a clean tree — and the cheap repair for that is deleting the ratchet.",
				c.name, c.text)
		}
	}
}
