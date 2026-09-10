package cardclaims

import (
	"strings"
	"testing"
)

// testIndex is the arm index the fixtures resolve against. Hand-built rather than
// walked, so a fixture's verdict does not move when an unrelated test is renamed —
// and so the "cites a test that does not exist" case has something it reliably is
// not in.
var testIndex = ArmIndex{
	Built: true,
	Funcs: map[string]bool{
		"TestReadIntentTakesNoWriteLock":                  true,
		"TestClaimRecordsRepoPins":                        true,
		"TestResourceToLock_FileScopeNamespacedByProject": true,
	},
	Files: map[string]bool{
		"internal/domain/delocking_db_test.go": true,
		"delocking_db_test.go":                 true,
		"claim_response_projection_test.go":    true,
	},
}

// 🔴 Why this file is as long as it is.
//
// K12's ledger MATCHES on a healthy tree — that is the arm working — so every
// branch that reports a problem is unreachable from the card set, exactly the way
// openCitationWaivers' four findings are unreachable from a tree with no waivers.
// An arm whose triggers only run on the day somebody files a marker is an arm
// nobody finds out is broken until the day they rely on it. The precedent is
// explicit and it was written after a count that was wrong for its entire life and
// nothing looked at it (aihub#494).
//
// So: every finding is driven here against a fixture, and — the half the
// clampdisclosure/rowserr shape insists on — so is every sentence the recogniser
// deliberately does NOT recognise. A recogniser tested only on what it accepts is
// a recogniser whose population can shrink without a failing test.

// ─────────────────────────────── the recogniser ──────────────────────────────

func TestRecogniserAcceptsTheFormItIsWrittenFor(t *testing.T) {
	// Every line is a real card sentence, or a minimal reduction of one, that
	// asserts something a test could hold.
	cases := []struct {
		name string
		text string
	}{
		{"published token plus a copula",
			"`intent: \"read\"` is honoured on `path`/`document`/`section` only."},
		{"a refusal",
			"The server refuses a `pf_commit` whose changed files a live `file_scope` lock covers."},
		{"a negative about an absent wire key",
			"`task_branches` is NO LONGER SENT by this call."},
		{"present tense carrying a historical clause",
			"Locks derived at claim are `file_scope` only since `aihub#416`."},
		{"a derivation",
			"A `path` entry derives a `file_scope` lock namespaced by project."},
		{"a status-code verb",
			"The server 400s a request carrying no `machine_id`."},
		{"a reader census stated positively",
			"`last_active_age_seconds` reports an age and no code branches on it."},

		// ── the aihub#591 widening: each of these is a real card sentence the ──
		// ── recogniser was MEASURED to pass over before 2026-09-10.          ──
		{"form (b): a behaviour claim whose only backtick is a wi-id — the isolation pair",
			"`aihub#430` measured the same interleaving on the claim path and found it " +
				"comes back as a retryable 409 with the row untouched, because that path " +
				"opens SERIALIZABLE while this one opens READ COMMITTED."},
		{"form (b): a recorded live defect anchored to wi-ids only — the pf_get_step shape",
			"The tool description still carries the same clause, which this card records " +
				"rather than fixes: it is item 17 of `aihub#400` §3.2, and no probe pins it."},
		{"a placement claim on 'sits'",
			"The guard sits above the first query in `internal/domain/memory.go` (`Remember`), " +
				"so it covers `pf_remember`, `pf_save_artifact` and `pf_update_memory` alike."},
		{"a scope claim on 'governs'",
			"The strictest tier a patch touches governs the whole `attrs_patch`, in both " +
				"directions."},
		{"a wire claim on 'come back'",
			"They come back on this response as `repo_pins` and on `pf_get_step`."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, why := IsCandidate(tc.text)
			if !ok {
				t.Fatalf("IsCandidate rejected a sentence that asserts something: %q\n"+
					"reason given: %s\nA sentence the recogniser passes over is a sentence "+
					"nothing counts, so a false negative here is debt that never appears in "+
					"the ledger at all.", tc.text, why)
			}
		})
	}
}

func TestRecogniserRejectsWhatItDeliberatelyDoesNotRecognise(t *testing.T) {
	// 🔴 The half that matters. Each case names the condition it fails, and each
	// condition is one the package documents as deliberate — so if somebody
	// loosens the recogniser to make a card quiet, one of these goes green-to-red.
	cases := []struct {
		name string
		text string
		want string
	}{
		{
			name: "an evaluative predicate with no token — the `judgement` shape",
			text: "The only reliable conflict signal in this system is the return value.",
			want: "names no published token",
		},
		{
			// ⚠️ Until aihub#591 the fixture here was "`aihub#510` is the work item
			// that landed the exclusion." rejected on condition 1. Form (b) now admits
			// a wi-anchored sentence, so the boundary moved to condition 2: an anchor
			// with no effect verb is still narration about a work item.
			name: "a work-item reference with no effect verb stays out",
			text: "`aihub#510` landed the exclusion for those three rules on 2026-09-09.",
			want: "carries no effect-or-refusal verb",
		},
		{
			name: "a section reference with no effect verb stays out too",
			text: "See `§6.1 T1-2` for the two-vocabulary split.",
			want: "carries no effect-or-refusal verb",
		},
		{
			name: "a token with no effect verb",
			text: "See `internal/domain/conflicts.go` for the shared containment fragments.",
			want: "carries no effect-or-refusal verb",
		},
		{
			name: "past tense — the `history` shape",
			text: "Rule 2 used to return a `git_branch` lock, bypassing the mapper.",
			want: "past tense",
		},
		{
			name: "an effect verb that only appears inside a backticked symbol",
			text: "The field `willUnlock` and the helper `reportsTo` appear here.",
			want: "carries no effect-or-refusal verb",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, why := IsCandidate(tc.text)
			if ok {
				t.Fatalf("IsCandidate accepted %q, which this package documents as outside "+
					"the form it recognises. Widening the recogniser is allowed; doing it "+
					"without moving this fixture is how a population grows unnoticed.", tc.text)
			}
			if !strings.Contains(why, tc.want) {
				t.Errorf("rejected for %q, expected a reason mentioning %q — the reason is "+
					"what a failure message shows a reader, so it has to name the condition "+
					"that actually failed", why, tc.want)
			}
		})
	}
}

func citesForTest(s string) bool {
	ok, _ := CitesAnArm("fixture.md", s, testIndex)
	return ok
}

func TestCitingAnArmIsNarrowerThanCitingAFile(t *testing.T) {
	// 🔴 The one that retires debt, so it is the one worth pinning hardest: if
	// naming ANY .go file counted, a card could clear its ledger by describing
	// where the implementation lives.
	cited := []string{
		"Held by `internal/domain/delocking_db_test.go`.",
		"Held by `TestReadIntentTakesNoWriteLock`.",
		"Held by `claim_response_projection_test.go` (`TestClaimRecordsRepoPins`).",
	}
	notCited := []string{
		"The rule's SQL lives in `internal/domain/conflicts.go`.",
		"Bound by `internal/server/router.go` (`handlePredictConflicts`).",
		"The registry is `internal/mcp/tools_lifecycle.go`.",
	}
	for _, s := range cited {
		if !citesForTest(s) {
			t.Errorf("CitesAnArm(%q) = false; a sentence naming its arm must retire its own "+
				"debt, or the only way to close the ledger is a marker", s)
		}
	}
	for _, s := range notCited {
		if citesForTest(s) {
			t.Errorf("CitesAnArm(%q) = true; that names the implementation, not a gate over "+
				"it. Counting it as probed would let a card retire debt by saying where the "+
				"code is, which is the one thing a reader of this ledger must not be able to "+
				"do.", s)
		}
	}
}

// ───────────────────────────── marker parse + place ──────────────────────────

func TestMarkerAttachesToTheSentenceItFollows(t *testing.T) {
	prose := "## hop 4\n\n" +
		"- `repo` entries derive no lock at all.\n" +
		"  <!-- probe-waiver: kind=pending-implementation | decided=2026-09-10 |\n" +
		"  citation=aihub#543 | reason=the DB fixture for the derivation table is not\n" +
		"  written yet, and this is the claim it will hold first. -->\n" +
		"  A `service` entry answers `info` and carries `last_active_age_seconds`.\n"

	read := ReadCard("fixture.md", prose)
	sentences := read.Sentences
	if len(read.Orphans) != 0 || len(read.Dropped) != 0 {
		t.Fatalf("orphans = %d, dropped = %d, want 0/0: %+v %+v",
			len(read.Orphans), len(read.Dropped), read.Orphans, read.Dropped)
	}
	if len(sentences) != 2 {
		t.Fatalf("split into %d sentence(s), want 2:\n%+v", len(sentences), sentences)
	}
	if len(sentences[0].Markers) != 1 {
		t.Fatalf("the marker attached to sentence %d, not the one it follows. A marker "+
			"written between two sentences sits at exactly the second one's start offset, "+
			"so reading it as classifying what it PRECEDES puts every trailing marker on "+
			"the wrong claim.\nsentence 0 markers=%d, sentence 1 markers=%d",
			1, len(sentences[0].Markers), len(sentences[1].Markers))
	}
	m := sentences[0].Markers[0]
	if m.Form != MarkerWaiver || m.Kind != KindPendingImplementation ||
		m.Decided != "2026-09-10" || m.Citation != "aihub#543" {
		t.Errorf("multi-line marker parsed wrong: %+v — a reason long enough to wrap is the "+
			"ordinary case, not the exception", m)
	}
	if !strings.Contains(m.Reason, "written yet") {
		t.Errorf("reason lost its tail: %q. `reason` is last and runs to the closing "+
			"comment, so a reason containing the separator has to survive.", m.Reason)
	}
	if len(sentences[1].Markers) != 0 {
		t.Errorf("the following sentence picked up a marker it does not carry")
	}
}

func TestMarkerOnALineTheWalkDoesNotReadIsAnOrphan(t *testing.T) {
	// A marker classifies the SENTENCE it sits on. On a heading or inside a fence
	// it classifies nothing while looking like it does — which is the
	// exemption-that-outlives-its-gap shape, arriving on day one.
	//
	// ⚠️ A TABLE ROW is deliberately no longer in this list: aihub#591 put rows
	// into the walk, so a marker in a cell places onto the row's own unit — see
	// TestTableRowsAreCountableAndWaivable. The orphan population is what is left.
	for _, prose := range []string{
		"## hop 0-1 <!-- prose-only: because=history -->\n\nA `path` entry derives a lock.\n",
		"```go\n<!-- prose-only: because=history -->\n```\n",
	} {
		read := ReadCard("fixture.md", prose)
		if len(read.Orphans) != 1 {
			t.Errorf("orphans = %d, want 1 for:\n%s", len(read.Orphans), prose)
		}
	}
	tally := Tally("fixture.md", "## hop 0-1 <!-- prose-only: because=history -->\n\nx\n", testIndex)
	if !hasFinding(tally.Problems, "K12 MARKER_ORPHAN") {
		t.Errorf("Tally did not report MARKER_ORPHAN: %v", tally.Problems)
	}
}

func TestWalkPopulationOnAMixedSection(t *testing.T) {
	// ⚠️ This test used to require the walk to reproduce the aihub#543 §0.1 sizer —
	// fences, table rows and headings all dropped. aihub#591 deliberately broke
	// with the sizer on TABLE ROWS: 46 candidate-assertable claims were measured
	// living in |-prefixed rows across the 45 cards, never split into units, so
	// they could not be counted or waived. Rows are units now, each hard-bounded
	// at both edges; fences, headings, SEPARATOR rows and sub-floor fragments stay
	// outside.
	prose := "## hop 0-1\n\n" +
		"| param | type |\n|---|---|\n| `dry_run` | boolean |\n\n" +
		"```json\n{\"tool\": \"pf_x\"}\n```\n\n" +
		"A `path` entry derives a `file_scope` lock. `repo` entries derive none.\n" +
		"tiny\n"
	sentences := ReadCard("fixture.md", prose).Sentences
	if len(sentences) != 4 {
		t.Fatalf("split %d sentence(s), want 4 — the header row, the data row and the two "+
			"prose sentences; the fence, the heading, the separator row and the sub-floor "+
			"fragment are all outside the population:\n%+v", len(sentences), sentences)
	}
	if sentences[0].Text != "| param | type |" || sentences[1].Text != "| `dry_run` | boolean |" {
		t.Errorf("the two rows did not come through as their own units: %q / %q",
			sentences[0].Text, sentences[1].Text)
	}
	// 🔴 The row boundary is load-bearing in BOTH directions: a row does not end in
	// sentence punctuation, so without the trailing cut the prose after the table
	// would silently join the last row and every claim in it would ride that row's
	// classification.
	if !strings.HasPrefix(sentences[2].Text, "A `path` entry") {
		t.Errorf("the prose after the table merged into the last row: %q", sentences[2].Text)
	}
}

// ──────────────────────────────── the findings ───────────────────────────────

func TestEveryMarkerFieldFindingFires(t *testing.T) {
	const goodReason = "the DB fixture for this derivation is not written yet, and this " +
		"is the claim it will hold first."
	cases := []struct {
		name   string
		marker string
		want   string
	}{
		{
			name: "an unknown kind",
			marker: "<!-- probe-waiver: kind=someday | decided=2026-09-10 | " +
				"citation=aihub#543 | reason=" + goodReason + " -->",
			want: "K12 WAIVER_KIND_UNKNOWN",
		},
		{
			name: "a date-SHAPED string that is not a date",
			marker: "<!-- probe-waiver: kind=pending-implementation | decided=2026-13-40 | " +
				"citation=aihub#543 | reason=" + goodReason + " -->",
			want: "K12 WAIVER_NO_DATE",
		},
		{
			name: "no date at all",
			marker: "<!-- probe-waiver: kind=pending-implementation | " +
				"citation=aihub#543 | reason=" + goodReason + " -->",
			want: "K12 WAIVER_NO_DATE",
		},
		{
			name: "no citation",
			marker: "<!-- probe-waiver: kind=pending-implementation | decided=2026-09-10 | " +
				"reason=" + goodReason + " -->",
			want: "K12 WAIVER_NO_CITATION",
		},
		{
			name: "a kind that waits on somebody, citing nobody",
			marker: "<!-- probe-waiver: kind=known-defect | decided=2026-09-10 | " +
				"citation=the file that argues it | reason=" + goodReason + " -->",
			want: "K12 WAIVER_NO_WORK_ITEM",
		},
		{
			name: "a reason too thin to disagree with",
			marker: "<!-- probe-waiver: kind=pending-implementation | decided=2026-09-10 | " +
				"citation=aihub#543 | reason=TODO -->",
			want: "K12 WAIVER_NO_REASON",
		},
		{
			name: "a waiver wearing a prose-only field",
			marker: "<!-- probe-waiver: kind=pending-implementation | decided=2026-09-10 | " +
				"citation=aihub#543 | because=history | reason=" + goodReason + " -->",
			want: "K12 WAIVER_EXTRA_FIELDS",
		},
		{
			name:   "a because outside the closed vocabulary",
			marker: "<!-- prose-only: because=it-is-fine -->",
			want:   "K12 PROSE_ONLY_BECAUSE_UNKNOWN",
		},
		{
			name:   "a prose-only row wearing waiver fields",
			marker: "<!-- prose-only: because=history | decided=2026-09-10 -->",
			want:   "K12 PROSE_ONLY_EXTRA_FIELDS",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prose := "A `path` entry derives a `file_scope` lock. " + tc.marker + "\n"
			tally := Tally("fixture.md", prose, testIndex)
			if !hasFinding(tally.Problems, tc.want) {
				t.Errorf("no %s. A finding that cannot be triggered from a fixture is a "+
					"finding nobody knows is broken until they rely on it.\ngot: %v",
					tc.want, tally.Problems)
			}
		})
	}
}

func TestKindsThatNameNobodyAreAcceptedWithoutAWorkItem(t *testing.T) {
	// The control for WAIVER_NO_WORK_ITEM above. `structurally-unreachable` names
	// what is MISSING and `accepted-unprobed` names a RULING; clampdisclosure's own
	// citations are a file and a commit. Requiring a work item there would be
	// satisfied by citing an unrelated number, which is worse than not asking.
	for _, kind := range []WaiverKind{KindStructurallyUnreachable, KindAcceptedUnprobed} {
		prose := "A `path` entry derives a `file_scope` lock. " +
			"<!-- probe-waiver: kind=" + string(kind) + " | decided=2026-09-10 | " +
			"citation=internal/server/queryparam.go, the /ui exemption note | " +
			"reason=asserting this needs a second machine, which the harness cannot " +
			"create from a unit test. -->\n"
		tally := Tally("fixture.md", prose, testIndex)
		if hasFinding(tally.Problems, "K12 WAIVER_NO_WORK_ITEM") {
			t.Errorf("kind %s was required to name a work item: %v", kind, tally.Problems)
		}
		if len(tally.Problems) != 0 {
			t.Errorf("kind %s produced findings on a well-formed marker: %v", kind, tally.Problems)
		}
		if tally.Census.Unclassified != 0 {
			t.Errorf("kind %s left the sentence unclassified", kind)
		}
	}
}

func TestClassificationConflictsFire(t *testing.T) {
	const good = " | decided=2026-09-10 | citation=aihub#543 | reason=the DB fixture for " +
		"this derivation is not written yet, and this is the claim it will hold first. -->"
	cases := []struct {
		name  string
		prose string
		want  string
	}{
		{
			name: "a marker on a sentence the recogniser does not flag",
			prose: "Rule 2 used to return a `git_branch` lock, bypassing the mapper. " +
				"<!-- probe-waiver: kind=pending-implementation" + good + "\n",
			want: "K12 STALE_MARKER",
		},
		{
			name: "waived and probed at once",
			prose: "A `path` entry derives a `file_scope` lock, held by " +
				"`TestResourceToLock_FileScopeNamespacedByProject`. " +
				"<!-- probe-waiver: kind=pending-implementation" + good + "\n",
			want: "K12 STALE_WAIVER",
		},
		{
			name: "waived and prose-only at once",
			prose: "A `path` entry derives a `file_scope` lock. " +
				"<!-- prose-only: because=history --> " +
				"<!-- probe-waiver: kind=pending-implementation" + good + "\n",
			want: "K12 MARKER_CONFLICT",
		},
		{
			name: "two markers of the same form on one sentence",
			prose: "A `path` entry derives a `file_scope` lock. " +
				"<!-- prose-only: because=history --> <!-- prose-only: because=judgement -->\n",
			want: "K12 MARKER_DUPLICATE",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tally := Tally("fixture.md", tc.prose, testIndex)
			if !hasFinding(tally.Problems, tc.want) {
				t.Errorf("no %s\ngot: %v", tc.want, tally.Problems)
			}
		})
	}
}

// ─────────────────────────────── the ledger half ─────────────────────────────

func TestLedgerIsCheckedInBothDirections(t *testing.T) {
	cards := map[string]bool{"pf_a": true, "pf_b": true}
	measured := map[string]CardTally{
		"pf_a": {Census: Census{Candidates: 5, Unclassified: 4, PendingImplementation: 1}},
	}
	match := Census{Candidates: 5, Unclassified: 4, PendingImplementation: 1}

	cases := []struct {
		name   string
		ledger map[string]Census
		want   []string
		absent []string
	}{
		{
			name:   "a scoped card with no row bounds nothing",
			ledger: map[string]Census{},
			want:   []string{"K12 LEDGER_MISSING", "Unclassified: 4"},
		},
		{
			name:   "a row for a file that is not a card",
			ledger: map[string]Census{"pf_a": match, "pf_gone": {}},
			want:   []string{"K12 LEDGER_ORPHAN"},
		},
		{
			name:   "a row for a card outside the scoped set",
			ledger: map[string]Census{"pf_a": match, "pf_b": {}},
			want:   []string{"K12 LEDGER_UNSCOPED"},
		},
		{
			name:   "debt that grew",
			ledger: map[string]Census{"pf_a": {Candidates: 5, Unclassified: 3, PendingImplementation: 1}},
			want:   []string{"K12 DEBT_GROWTH", "Unclassified: 4"},
			absent: []string{"K12 STALE_DEBT"},
		},
		{
			name:   "a gap that closed",
			ledger: map[string]Census{"pf_a": {Candidates: 5, Unclassified: 9, PendingImplementation: 1}},
			want:   []string{"K12 STALE_DEBT", "Unclassified: 4"},
			absent: []string{"K12 DEBT_GROWTH"},
		},
		{
			// 🔴 The commonest operation in a probe wave, and the first version of
			// classifyDrift accused it of being the worst one: filing a legitimate marker
			// on a grandfathered sentence moves it out of Unclassified into a classified
			// column, and "some column rose" was read as growth. The message then told the
			// author a card sentence asserted something with neither an arm nor a marker,
			// in the same diff where they had just added the marker.
			name:   "filing a marker on a grandfathered sentence is a reclassification",
			ledger: map[string]Census{"pf_a": {Candidates: 5, Unclassified: 5}},
			want:   []string{"K12 RECLASSIFIED"},
			absent: []string{"K12 DEBT_GROWTH", "K12 STALE_DEBT"},
		},
		{
			// The escape the per-kind columns exist to expose. Same finding, because
			// both are "a sentence moved between classes and the row must be re-pinned";
			// the message names both readings so the diff has to say which.
			name:   "a relabel between waiver kinds is a reclassification too",
			ledger: map[string]Census{"pf_a": {Candidates: 5, Unclassified: 4, AcceptedUnprobed: 1}},
			want:   []string{"K12 RECLASSIFIED"},
			absent: []string{"K12 DEBT_GROWTH"},
		},
		{
			// The one thing that must still be growth: a sentence with no arm and no
			// marker appearing where none was.
			name:   "an unheld sentence appearing is growth",
			ledger: map[string]Census{"pf_a": {Candidates: 4, Unclassified: 3, PendingImplementation: 1}},
			want:   []string{"K12 DEBT_GROWTH"},
			absent: []string{"K12 RECLASSIFIED", "K12 STALE_DEBT"},
		},
		{
			// 🔴 The swap: one assertable sentence added, one previously-unclassified
			// sentence cited. Every debt column is unchanged, so without Candidates and
			// Cited on the row this passes green while a new unheld claim lands.
			name:   "a swap that leaves every debt column alone",
			ledger: map[string]Census{"pf_a": {Candidates: 4, Cited: 1, Unclassified: 4, PendingImplementation: 1}},
			want:   []string{"K12 POPULATION_MOVED"},
			absent: []string{"K12 DEBT_GROWTH", "K12 STALE_DEBT"},
		},
		{
			name:   "an exact match is silent",
			ledger: map[string]Census{"pf_a": match},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(LedgerProblems(measured, tc.ledger, cards), "\n")
			if len(tc.want) == 0 && got != "" {
				t.Fatalf("expected silence, got:\n%s", got)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(got, a) {
					t.Errorf("unexpected %q in:\n%s", a, got)
				}
			}
		})
	}
}

func TestAnUnbalancedCensusIsReported(t *testing.T) {
	// The classes are exhaustive by construction, so this can only be reached from a
	// marker naming a kind outside the closed set — but "can only be reached from"
	// is exactly the claim that stops being true after somebody adds a class and
	// forgets a switch arm. The arm costs nothing and the assumption is checked.
	got := strings.Join(LedgerProblems(
		map[string]CardTally{"pf_a": {Census: Census{Candidates: 5, Cited: 1}}},
		map[string]Census{"pf_a": {Candidates: 5, Cited: 1}},
		map[string]bool{"pf_a": true}), "\n")
	if !strings.Contains(got, "K12 CENSUS_UNBALANCED") {
		t.Errorf("a row whose classes do not add up to its population went unreported:\n%s", got)
	}
}

func TestEveryFailureCarriesAPasteableLine(t *testing.T) {
	// 🔴 dbtestcov's rule: a gate that demands a number must print the number it
	// wants. Without this the repair is a hand re-derivation, and a hand-derived
	// count is the thing aihub#494 measured wrong for its entire life.
	measured := Census{Candidates: 6, Unclassified: 4, KnownDefect: 2}
	got := strings.Join(LedgerProblems(
		map[string]CardTally{"pf_a": {Census: measured}},
		map[string]Census{"pf_a": {Candidates: 6, Unclassified: 1, KnownDefect: 2}},
		map[string]bool{"pf_a": true}), "\n")
	want := measured.Line("pf_a")
	if !strings.Contains(got, want) {
		t.Errorf("the failure does not carry the replacement line.\nwant a line containing:\n%s\ngot:\n%s",
			want, got)
	}
}

func TestEveryWaiverKindDescribesItself(t *testing.T) {
	// A label with no account of what it claims is a label a reader guesses at,
	// and the reason the four kinds are not one is that they claim different
	// things. Same arm clampdisclosure runs over its own three.
	for _, k := range WaiverKinds {
		if strings.TrimSpace(k.Describe()) == "" {
			t.Errorf("WaiverKind %q has no Describe(); failure text would show the label "+
				"instead of the claim", k)
		}
	}
	if WaiverKind("invented").Describe() != "" {
		t.Errorf("Describe() answers for a kind outside the closed set")
	}
}

// TestEveryWaiverKindHasItsOwnCensusColumn is kindCell's calibration: since
// aihub#565 the kind vocabulary meets the census columns in that ONE mapping
// (Tally increments through it, Balanced sums through it, classifyDrift compares
// through it), so the two drifts it could carry are checked here directly. A kind
// with NO cell would classify every sentence it waives into nothing; two kinds
// SHARING a cell would silently merge in the ledger the exact distinction the
// per-kind columns exist to expose (a known-defect relabelled accepted-unprobed
// reads as no movement at all).
func TestEveryWaiverKindHasItsOwnCensusColumn(t *testing.T) {
	var d Census
	seen := map[*int]WaiverKind{}
	for _, k := range WaiverKinds {
		cell := d.kindCell(k)
		if cell == nil {
			t.Errorf("WaiverKind %q has no census column; every sentence it waives would be "+
				"counted as a candidate and classified into nothing", k)
			continue
		}
		if prev, dup := seen[cell]; dup {
			t.Errorf("WaiverKinds %q and %q share one census column; a relabel between them "+
				"would move no ledger number, which is the drift the per-kind columns exist "+
				"to expose", prev, k)
		}
		seen[cell] = k
	}
	if d.kindCell("invented") != nil {
		t.Errorf("kindCell answers for a kind outside the closed set; an unrecognised kind " +
			"must classify into nothing so Balanced reports it, not into somebody's column")
	}
}

// TestTallyCountsEachKindIntoItsOwnColumn drives the same mapping end-to-end
// through Tally: one well-formed marker of every kind, each landing in its own
// column and nowhere else. Before aihub#565 no fixture asserted WHICH column a
// waived sentence landed in — TestKindsThatNameNobodyAreAcceptedWithoutAWorkItem
// checks only that the sentence is not left unclassified — so a swap between two
// kind columns was green everywhere except against the live card set.
//
// MUTANTS (each applied on 2026-09-10, tree change proven by sha256
// before/after, restored byte-identical after the run):
//
//	C1 kindCell loses the known-defect case (kind counted into nothing)
//	                            RED  TestEveryWaiverKindHasItsOwnCensusColumn
//	                                 + this arm (census short one column, unbalanced)
//	C2 kindCell miswires known-defect into the structurally-unreachable column
//	                            RED  TestEveryWaiverKindHasItsOwnCensusColumn
//	                                 + this arm (relabel reads as no movement)
//	C3 classifyDrift compares WaiverKinds[1:] — one kind column dropped from the
//	   drift report            RED  TestLedgerIsCheckedInBothDirections (a
//	                                 relabelled row reported STALE_DEBT, not
//	                                 RECLASSIFIED)
func TestTallyCountsEachKindIntoItsOwnColumn(t *testing.T) {
	marker := func(kind, citation string) string {
		return "A `path` entry derives a `file_scope` lock. " +
			"<!-- probe-waiver: kind=" + kind + " | decided=2026-09-10 | " +
			"citation=" + citation + " | reason=fixture exercising the kind-to-column mapping. -->\n"
	}
	prose := marker("pending-implementation", "aihub#543") +
		marker("known-defect", "aihub#564") +
		marker("structurally-unreachable", "a harness this repo cannot create") +
		marker("accepted-unprobed", "the owner ruling of 2026-09-10")

	tally := Tally("fixture.md", prose, testIndex)
	if len(tally.Problems) != 0 {
		t.Fatalf("well-formed markers produced findings, so the census below measures the "+
			"wrong thing: %v", tally.Problems)
	}
	want := Census{Candidates: 4, PendingImplementation: 1, KnownDefect: 1,
		StructurallyUnreachable: 1, AcceptedUnprobed: 1}
	if tally.Census != want {
		t.Errorf("census = %+v, want %+v — one marker of every kind must land in its own "+
			"column and nowhere else, or a ledger row cannot tell a relabel from a fix",
			tally.Census, want)
	}
	if !tally.Census.Balanced() {
		t.Errorf("census is unbalanced on four well-formed waivers: %+v", tally.Census)
	}
}

func hasFinding(problems []string, want string) bool {
	for _, p := range problems {
		if strings.Contains(p, want) {
			return true
		}
	}
	return false
}

// ───────────────────── citation resolution and negation ──────────────────────

func TestACitationThatResolvesToNothingDoesNotRetireDebt(t *testing.T) {
	// 🔴 The sharpest hole this package had. The comment on armSymbol used to claim
	// "both forms are already K6-resolved"; that is false for the symbol form,
	// because both of K6's anchor patterns require a .go suffix and a bare
	// `TestSomething` in prose matches neither. So a card could clear a claim by
	// citing a test nobody ever wrote, with every arm in the repo green.
	const s = "A `path` entry derives a `file_scope` lock, held by `TestNoSuchProbeEverExisted`."
	ok, problems := CitesAnArm("fixture.md", s, testIndex)
	if ok {
		t.Errorf("an unresolvable citation retired the claim — the ledger clears while "+
			"nothing holds it: %q", s)
	}
	if !hasFinding(problems, "K12 ARM_CITATION_UNRESOLVED") {
		t.Errorf("no ARM_CITATION_UNRESOLVED; a citation that resolves nowhere has to be "+
			"reported, not merely uncounted: %v", problems)
	}

	const file = "Held by `internal/domain/no_such_file_test.go`."
	if ok, problems := CitesAnArm("fixture.md", file, testIndex); ok ||
		!hasFinding(problems, "K12 ARM_CITATION_UNRESOLVED") {
		t.Errorf("a path citation to a file that does not exist was accepted: ok=%v %v",
			ok, problems)
	}

	if _, problems := CitesAnArm("fixture.md", "x", ArmIndex{}); !hasFinding(problems, "K12 ARM_INDEX_MISSING") {
		t.Errorf("a zero index answered as though the tree declared no tests: %v", problems)
	}
}

func TestACitationInsideANegationDoesNotRetireDebt(t *testing.T) {
	// The live case is in aihub#543's own §1.4 sample.
	const negated = "It false-negatives on read intent: `TestReadIntentTakesNoWriteLock` " +
		"holds the lock derivation, not the prediction's answer."
	ok, problems := CitesAnArm("fixture.md", negated, testIndex)
	if ok {
		t.Errorf("a sentence saying what an arm does NOT hold retired the claim: %q", negated)
	}
	if !hasFinding(problems, "K12 CITATION_IN_NEGATIVE") {
		t.Errorf("no CITATION_IN_NEGATIVE: %v", problems)
	}
}

func TestTheNegationScanIsTightEnoughToLeaveRealCitationsAlone(t *testing.T) {
	// 🔴 The control, and it is not decorative: a first version of the negation list
	// banned "rather than", "nothing", "deliberately" and "is not", and reddened
	// BOTH of the only two genuinely-cited sentences in the ten scoped cards. A
	// false positive here puts a probed claim back into debt, so this direction
	// costs as much as the other one.
	cases := []string{
		"That is a checked property rather than a fact of the implementation: " +
			"`TestClaimRecordsRepoPins` drives the registered tool.",
		"`TestResourceToLock_FileScopeNamespacedByProject` pins it, and nothing in the " +
			"name of a test is prose about the test.",
		"`TestClaimRecordsRepoPins` deliberately drives the whole path.",
	}
	for _, s := range cases {
		ok, problems := CitesAnArm("fixture.md", s, testIndex)
		if !ok {
			t.Errorf("a real citation was refused: %q\n%v", s, problems)
		}
	}
}

// ───────────────────────── the walk's own failure modes ──────────────────────

func TestBulletsAreHardSentenceBoundaries(t *testing.T) {
	// 🔴 Measured on pf_claim_work_item before this: 9 of 36 units spanned several
	// bullets, because the sizer's start class has no '-'. Two consequences, both
	// load-bearing — one citation retired every claim merged with it, and a "used
	// to" anywhere in a merged run ejected every live claim in the run.
	prose := "## hop 4\n\n" +
		"- `repo` entries derive no lock.\n" +
		"- Rule 2 used to return a `git_branch` lock.\n" +
		"- A `service` entry answers `info`.\n"
	sentences := ReadCard("fixture.md", prose).Sentences
	if len(sentences) != 3 {
		t.Fatalf("split into %d unit(s), want 3 — adjacent bullets merged:\n%+v",
			len(sentences), sentences)
	}
	live := 0
	for _, s := range sentences {
		if ok, _ := IsCandidate(s.Text); ok {
			live++
		}
	}
	if live != 2 {
		t.Errorf("%d candidate(s), want 2: the past-tense bullet must eject ITSELF and not "+
			"the live claims either side of it", live)
	}
}

func TestAMarkerOnAShortFragmentIsReportedNotReassigned(t *testing.T) {
	// Placement runs over every unit including the sub-floor ones, so a marker whose
	// host is filtered out is reported rather than sliding onto the previous
	// sentence — a classification landing on a claim nobody wrote it for.
	prose := "## hop 4\n\n" +
		"- A `path` entry derives a `file_scope` lock namespaced by project.\n" +
		"- `ok` stays. <!-- prose-only: because=history -->\n"
	read := ReadCard("fixture.md", prose)
	if len(read.Dropped) != 1 {
		t.Fatalf("dropped = %d, want 1 (orphans=%d, sentences=%d)",
			len(read.Dropped), len(read.Orphans), len(read.Sentences))
	}
	for _, s := range read.Sentences {
		if len(s.Markers) != 0 {
			t.Errorf("the marker was reassigned to %q instead of being reported", s.Text)
		}
	}
	if !hasFinding(Tally("fixture.md", prose, testIndex).Problems, "K12 MARKER_TARGET_DROPPED") {
		t.Errorf("Tally did not report MARKER_TARGET_DROPPED")
	}
}

func TestOffsetsSurviveALeadingMarkerOnTheLine(t *testing.T) {
	// 🔴 The line is cleaned and trimmed before an offset is taken. Doing it the
	// other way makes every offset on a line with a LEADING marker too large by the
	// whitespace the trim removes, so a second marker on that line lands past the
	// next sentence's start and the strict-< placement puts it on the wrong claim.
	prose := "## hop 4\n\n" +
		"A `path` entry derives a `file_scope` lock namespaced by project.\n" +
		"<!-- prose-only: because=history --> A `repo` entry derives no lock at all. " +
		"<!-- prose-only: because=judgement -->\n" +
		"A `service` entry answers `info` on a declaration join.\n"
	read := ReadCard("fixture.md", prose)
	if len(read.Sentences) != 3 || len(read.Orphans) != 0 || len(read.Dropped) != 0 {
		t.Fatalf("walk produced %d sentence(s), %d orphan(s), %d dropped",
			len(read.Sentences), len(read.Orphans), len(read.Dropped))
	}
	got := []int{len(read.Sentences[0].Markers), len(read.Sentences[1].Markers),
		len(read.Sentences[2].Markers)}
	if got[0] != 1 || got[1] != 1 || got[2] != 0 {
		t.Errorf("markers landed on %v, want [1 1 0] — the leading marker classifies the "+
			"sentence it follows and the trailing one classifies its own", got)
	}
}

func TestAWrappedMarkerAfterAClosedCommentStillCoalesces(t *testing.T) {
	// 🔴 Deciding openness from the line's FIRST `<!--` means a line already carrying
	// a closed comment — K9's `<!-- historical -->` is one, and it lives in these very
	// cards — never coalesces a second, wrapped marker that starts after it. The
	// marker then fails to parse and the sentence reads as unclassified with no
	// finding anywhere, which is the silent shape this package is against.
	prose := "## hop 4\n\n" +
		"A `path` entry derives a `file_scope` lock. <!-- historical --> <!-- probe-waiver: " +
		"kind=pending-implementation | decided=2026-09-10 |\n" +
		"citation=aihub#543 | reason=the DB fixture for this derivation is not written " +
		"yet, and this is the claim it will hold first. -->\n"
	read := ReadCard("fixture.md", prose)
	if len(read.Sentences) == 0 || len(read.Sentences[0].Markers) != 1 {
		t.Fatalf("the wrapped marker did not coalesce: sentences=%d markers=%v",
			len(read.Sentences), read.Sentences)
	}
	if read.Sentences[0].Markers[0].Kind != KindPendingImplementation {
		t.Errorf("parsed as %+v", read.Sentences[0].Markers[0])
	}
}

func TestAnInlineCodeCommentTokenDoesNotOpenAComment(t *testing.T) {
	// 🔴 A card documenting this syntax writes the token in backticks. Treating that
	// as a comment swallows every fence and table after it to the end of the file,
	// silently rewriting the population of the whole card.
	prose := "## hop 4\n\n" +
		"The marker opens with `<!--` and closes with the usual terminator.\n\n" +
		"| param | type |\n|---|---|\n\n" +
		"A `path` entry derives a `file_scope` lock namespaced by project.\n"
	read := ReadCard("fixture.md", prose)
	// 3 units since aihub#591: the header ROW is read now, the separator is not.
	if len(read.Sentences) != 3 {
		t.Fatalf("an inline-code comment token swallowed the rest of the card: %d unit(s)\n%+v",
			len(read.Sentences), read.Sentences)
	}
}

func TestReasonIsAFieldKeyNotASubstring(t *testing.T) {
	// Cutting the body at the first "reason=" anywhere means a citation that merely
	// mentions the word truncates the entry there and files whatever followed as the
	// reason, silently.
	prose := "A `path` entry derives a `file_scope` lock. " +
		"<!-- probe-waiver: kind=pending-implementation | decided=2026-09-10 | " +
		"citation=aihub#543, whose reason=X argument is quoted here | " +
		"reason=the DB fixture for this derivation is not written yet. -->\n"
	read := ReadCard("fixture.md", prose)
	m := read.Sentences[0].Markers[0]
	if !strings.Contains(m.Citation, "whose reason=X argument") {
		t.Errorf("citation was truncated at a substring: %q", m.Citation)
	}
	if !strings.HasPrefix(m.Reason, "the DB fixture") {
		t.Errorf("reason picked up the citation's text: %q", m.Reason)
	}
}

func TestARepeatedFieldIsReportedNotResolvedLastWins(t *testing.T) {
	// 🔴 Appending `| kind=accepted-unprobed` to a long wrapped known-defect marker
	// both moved its column and skipped the work-item requirement, because every
	// check ran against the last kind parsed. Invisible in review.
	prose := "A `path` entry derives a `file_scope` lock. " +
		"<!-- probe-waiver: kind=known-defect | decided=2026-09-10 | citation=aihub#543 | " +
		"kind=accepted-unprobed | reason=measured behaviour the repo does not want " +
		"pinned right now. -->\n"
	problems := Tally("fixture.md", prose, testIndex).Problems
	if !hasFinding(problems, "K12 DUPLICATE_FIELD") {
		t.Errorf("no DUPLICATE_FIELD: %v", problems)
	}
	if got := ReadCard("fixture.md", prose).Sentences[0].Markers[0].Kind; got != KindKnownDefect {
		t.Errorf("the repeated key won: kind=%q — the first value must stand so the checks "+
			"run against what a reader reads first", got)
	}
}

func TestAMisspelledMarkerIsReportedRatherThanInert(t *testing.T) {
	// The worst failure mode available: the author and the reviewer both read a
	// classification in the diff and the arm reads an ordinary HTML comment.
	for _, raw := range []string{
		"<!-- prose_only: because=history -->",
		"<!-- Probe-Waiver: kind=known-defect -->",
		"<!-- probewaiver: kind=known-defect -->",
	} {
		prose := "A `path` entry derives a `file_scope` lock. " + raw + "\n"
		if !hasFinding(Tally("fixture.md", prose, testIndex).Problems, "K12 MARKER_NAME_UNRECOGNISED") {
			t.Errorf("%s went unreported", raw)
		}
	}
	// The control: K9's marker carries no colon and must stay silent, and so must a
	// correctly-spelled one.
	for _, raw := range []string{"<!-- historical -->", "<!-- prose-only: because=history -->"} {
		prose := "Rule 2 used to return a `git_branch` lock. " + raw + "\n"
		if hasFinding(Tally("fixture.md", prose, testIndex).Problems, "K12 MARKER_NAME_UNRECOGNISED") {
			t.Errorf("%s was reported as a misspelling", raw)
		}
	}
}

func TestAnUnknownFieldIsReported(t *testing.T) {
	prose := "A `path` entry derives a `file_scope` lock. " +
		"<!-- probe-waiver: kinds=known-defect | decided=2026-09-10 | citation=aihub#543 | " +
		"reason=measured behaviour the repo does not want pinned right now. -->\n"
	if !hasFinding(Tally("fixture.md", prose, testIndex).Problems, "K12 UNKNOWN_FIELD") {
		t.Errorf("a misspelled key was dropped instead of reported")
	}
}

func TestADateShapedStringThatIsNotADateIsRefused(t *testing.T) {
	for _, d := range []string{"2026-02-31", "2026-13-01", "2026-00-10", "20260910"} {
		prose := "A `path` entry derives a `file_scope` lock. " +
			"<!-- probe-waiver: kind=pending-implementation | decided=" + d + " | " +
			"citation=aihub#543 | reason=the DB fixture for this derivation is not written " +
			"yet, and this is the claim it will hold first. -->\n"
		if !hasFinding(Tally("fixture.md", prose, testIndex).Problems, "K12 WAIVER_NO_DATE") {
			t.Errorf("decided=%s was accepted as a calendar date", d)
		}
	}
	if !validDate("2026-02-28") || !validDate("2024-02-29") {
		t.Errorf("a real date was refused")
	}
}

func TestScanAllSeesMarkersTheWalkNeverPlaces(t *testing.T) {
	// The two questions it answers, and the fence difference between them.
	body := "# card\n\n```json\n{\"tool\": \"x\"}\n<!-- prose-only: because=history -->\n```\n\n" +
		"<!-- probe-waiver: kind=known-defect | decided=2026-09-10 | citation=aihub#564 | " +
		"reason=measured behaviour the repo does not want pinned right now. -->\n"
	all, _ := ScanAll(body, false)
	if len(all) != 2 {
		t.Errorf("scanning everything found %d marker(s), want 2 — the one inside the "+
			"machine block is exactly the blind spot this exists for", len(all))
	}
	outside, _ := ScanAll(body, true)
	if len(outside) != 1 {
		t.Errorf("scanning outside fences found %d marker(s), want 1 — a card documenting "+
			"the syntax in a code sample must not be reddened for quoting it", len(outside))
	}
	if _, unrec := ScanAll("<!-- prose_only: because=history -->", true); len(unrec) != 1 {
		t.Errorf("ScanAll missed a marker-shaped comment with an unrecognised name")
	}
}

// ───────────────────────── the aihub#591 widening ─────────────────────────────

// TestAttributionNeedsAllThreeLegs is form (c)'s calibration set: the known
// positives that MUST be candidates and the known negatives that MUST NOT be.
//
// 🔴 The negatives are the load-bearing half. A gate that over-fires is repaired
// by the cheapest compliant edit available, and for a prose gate that edit is
// deleting the gate — so every widening here ships with the sentences it must
// keep refusing, or the next author widens it the rest of the way.
func TestAttributionNeedsAllThreeLegs(t *testing.T) {
	tokenPrev := "- **`session_info.machine_id`**, from `POLYFORGE_MACHINE_ID` or the hostname."
	plainPrev := "Three things are added at this hop that no parameter names:"

	positives := []struct{ name, text, prev string }{
		{"the measured miss on the claim card",
			"The server 400s without it, naming the missing field.", tokenPrev},
		{"a refusal continued from the token sentence",
			"The server rejects a second spelling of the same flag.", tokenPrev},
		{"a rule-outcome verb continued from the token sentence",
			"A same-size swap counts as a removal; changing only a role does not.",
			"A write that would drop somebody not named in `expected_removals` is refused."},
	}
	for _, tc := range positives {
		if ok, why := IsCandidateInContext(tc.text, tc.prev); !ok {
			t.Errorf("%s: IsCandidateInContext rejected %q (prev %q): %s — this is the "+
				"token-in-previous-sentence class the wave-1 checkpoint measured invisible, "+
				"and a recogniser that cannot see it reads a card carrying it as clean",
				tc.name, tc.text, tc.prev, why)
		}
	}

	negatives := []struct{ name, text, prev string }{
		{"no previous sentence at all",
			"The server 400s without it, naming the missing field.", ""},
		{"the previous sentence names no token",
			"The server 400s without it, naming the missing field.", plainPrev},
		{"narration on a bare copula does not ride the token sentence",
			"So the count is honest, the row is identifiable, and the caller is told " +
				"which case it is looking at.", tokenPrev},
		{"attribution does not chain through a second tokenless sentence",
			"The server rejects the other spelling too.",
			"The server 400s without it, naming the missing field."},
		{"an effect verb outside the refusal-or-response subset is not enough",
			"It carries the resolved id back to the caller on every path.", tokenPrev},
	}
	for _, tc := range negatives {
		if ok, _ := IsCandidateInContext(tc.text, tc.prev); ok {
			t.Errorf("%s: IsCandidateInContext accepted %q (prev %q). Attribution takes a "+
				"token in the sentence BEFORE, no backticks here, and a refusal-or-response "+
				"verb — drop any leg and plain narration floods the population, which is the "+
				"overreach the wi that added this class names as the failure to avoid",
				tc.name, tc.text, tc.prev)
		}
	}
}

// TestAttributionVerbsAreASubsetOfEffectVerbs pins the containment the two lists'
// comments claim. A member added to the subset without being an effect verb would
// make form (c) recognise a sentence condition 2 then rejects, and the failure
// text would name two contradicting reasons for one sentence.
func TestAttributionVerbsAreASubsetOfEffectVerbs(t *testing.T) {
	for v := range attributionVerbs {
		if !effectVerbs[v] {
			t.Errorf("attributionVerbs holds %q, which effectVerbs does not — form (c) "+
				"would accept a sentence that fails condition 2", v)
		}
	}
}

// TestTableRowsAreCountableAndWaivable is class (a) of the aihub#591 widening:
// 46 candidate-assertable claims were measured living in |-prefixed rows, where
// the old walk could neither count nor waive them and a marker was MARKER_ORPHAN.
func TestTableRowsAreCountableAndWaivable(t *testing.T) {
	prose := "## hop 0-1\n\n" +
		"| param | type | required | meaning |\n" +
		"|---|---|---|---|\n" +
		"| `attrs` | object | no | a non-object — including a JSON-encoded string of " +
		"one — is a 400 | <!-- prose-only: because=judgement -->\n"

	read := ReadCard("fixture.md", prose)
	if len(read.Orphans) != 0 {
		t.Fatalf("orphans = %d, want 0 — a marker in a table cell must place, not orphan: %+v",
			len(read.Orphans), read.Orphans)
	}
	if len(read.Sentences) != 2 {
		t.Fatalf("split %d unit(s), want 2 (header row, data row):\n%+v",
			len(read.Sentences), read.Sentences)
	}

	header, row := read.Sentences[0], read.Sentences[1]
	if ok, _ := IsCandidate(header.Text); ok {
		t.Errorf("the plain-word header row was called a candidate: %q — headers name no "+
			"published token, and counting them would pad the population with table syntax",
			header.Text)
	}
	if ok, why := IsCandidate(stripMarkerForTest(row.Text)); !ok {
		t.Errorf("the data row was not a candidate (%s): %q — this is the hop 0-1 shape the "+
			"46 measured claims take, and the whole point of reading rows", why, row.Text)
	}
	if len(row.Markers) != 1 || row.Markers[0].Because != BecauseJudgement {
		t.Fatalf("the in-cell marker did not attach to the row: %+v", row.Markers)
	}
	class, _ := Classify(row, testIndex)
	if class != ProseOnly {
		t.Errorf("the marked row classified as %s, want prose-only — waivable means the "+
			"classification machinery works on a row exactly as on a sentence", class)
	}
}

func stripMarkerForTest(s string) string {
	clean, _ := cleanLine(s)
	return clean
}

// TestClosersEndSentencesAndScopeTheHistoryEjection is class (d)'s second half:
// the merged-bullet "used to" ejection. Measured on pf_list_dependencies before
// aihub#591: a bold-terminated historical sentence and the live claim after it
// were ONE unit, so "used to" ejected the live claim from the population with it.
func TestClosersEndSentencesAndScopeTheHistoryEjection(t *testing.T) {
	prose := "## hop 4\n\n" +
		"- **`Accessible` is a role comparison, and it used to be the wrong one.** " +
		"It is now computed with `internal/domain/projects.go` (`RoleLevel`).\n"
	sentences := ReadCard("fixture.md", prose).Sentences
	if len(sentences) != 2 {
		t.Fatalf("split %d unit(s), want 2 — `.**` must end the sentence the way `. ` does, "+
			"or the history clause and the live claim share one classification:\n%+v",
			len(sentences), sentences)
	}
	if ok, _ := IsCandidate(sentences[0].Text); ok {
		t.Errorf("the past-tense half was called a candidate: %q", sentences[0].Text)
	}
	if ok, why := IsCandidate(sentences[1].Text); !ok {
		t.Errorf("the live half was ejected with the history (%s): %q — that is exactly the "+
			"merged-bullet ejection this splitter change exists to end", why, sentences[1].Text)
	}
}
