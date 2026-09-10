package mcp_test

// aihub#543 probe wave 1 — all three candidate-assertable sentences of
// `docs/mcp-cards/pf_wrap.md`:
//
//	"It never sets `force_terminate_step`. So wrapping with a step still
//	 `in_progress` always fails at the completion — which is the failure that
//	 actually happens, and it is why the note is recorded twice on that retry."
//	    -> TestWrapSendsNoForceTerminateStep
//	    -> TestWrapRecordsTheNoteAgainWhenTheCompletionFailed
//	"The state file is deleted by the resolved canonical key and best-effort by
//	 the passed key, mirroring `pf_complete_attempt`."
//	    -> TestWrapDeletesBothTheCanonicalAndThePassedStateFileKeys
//	"In this repo's own workflow this tool is not the end-of-loop call. The
//	 in-tree plugin's own lifecycle reference … ends the run at
//	 `pf_complete_attempt(…, status="wrapped")` rather than here …"
//	    -> TestTheInTreeLifecycleReferenceEndsTheLoopAtCompleteAttempt
//
// ─── What held these before, and why it was not enough ─────────────────────
//
// state_resolve_wiring_test.go's TestSlugAddressedWrapCompletesUnderTheCanonicalID
// drives a real slug-addressed pf_wrap and checks the CANONICAL state file is
// gone. It writes the slug-keyed pre-claim stub too — and never looks at it
// again, which is exactly the half the card calls "best-effort by the passed
// key". A stub surviving a wrap is a file holding credentials the server has
// already invalidated, and the next slug-addressed call in that workspace reads
// it and gets a 409 that reads as somebody stealing the attempt.
//
// The other two had nothing at all:
//
//   - `force_terminate_step` is a published parameter of pf_complete_attempt and
//     of no other tool. pf_wrap's completion body is a literal composite four
//     keys wide; adding the flag to it would silently convert every wrap into
//     one that terminates a live step, and no arm anywhere reads that body's key
//     set. The absence is the contract, and an absence is what K10 is
//     structurally unable to see.
//   - the double note is DOCUMENTED behaviour ("documented rather than solved"),
//     which is the most fragile kind: nothing fails when it stops being true, so
//     a later idempotency key would leave the card describing a defect the tool
//     no longer has — and the card's Open section still calling it "stated
//     behaviour, not a settled design" is exactly what a reader would act on.
//
// Every expectation is read out of the published side — the card, the live
// pf_complete_attempt schema, the plugin reference the card names — so neither
// half of the contract can move alone.
//
// No database. git and a stub gh are needed and are skipped for explicitly:
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestWrap|TestTheInTreeLifecycle' -count=1 -v

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// wrapCardPath is the card carrying all three sentences.
const wrapCardPath = "../../docs/mcp-cards/pf_wrap.md"

// wrapFixture stands up the workspace, repo, stub gh and state files a pf_wrap
// call needs, with a PR that already covers HEAD so the push half is the
// idempotent no-op and nothing here depends on a push succeeding.
//
// withStub controls whether the slug-keyed pre-claim file exists, because that
// is the only thing the "best-effort by the passed key" clause is about.
func wrapFixture(t *testing.T, withStub bool) *fakeAihub {
	t.Helper()
	root := newResolveWorkspace(t)
	r := newResolveRepo(t, root)
	fakeGHForResolve(t, fmt.Sprintf(
		`[{"url":"https://example.invalid/pr/3","number":3,"state":"OPEN","baseRefName":"main","commits":[{"oid":%q}]}]`,
		r.head))
	if withStub {
		writeResolveStub(t)
	}
	writeResolveCanonical(t, map[string]string{"aihub": r.wt})
	return newFakeAihub(t)
}

// completeCall returns the single POST to the completion endpoint, failing when
// there is not exactly one: zero means the body below is read off nothing, and
// two would make "the body" ambiguous.
func completeCall(t *testing.T, f *fakeAihub) recordedCall {
	t.Helper()
	var found []recordedCall
	for _, c := range f.recorded() {
		if strings.HasSuffix(c.Path, "/complete") {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the call made %d completion request(s), want exactly 1; paths=%v",
			len(found), f.paths())
	}
	return found[0]
}

// publishedFlagName reads the flag the card says pf_wrap never sets, out of the
// card's own sentence.
func publishedFlagName(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(wrapCardPath)
	if err != nil {
		t.Fatalf("read %s: %v — the card is the published side of this claim", wrapCardPath, err)
	}
	re := regexp.MustCompile("never sets `([a-z_]+)`")
	m := re.FindStringSubmatch(strings.Join(strings.Fields(string(raw)), " "))
	if m == nil {
		t.Fatalf("%s no longer states which flag pf_wrap never sets, so there is no published "+
			"claim here to check. The clause is what the two sentences after it depend on — if "+
			"it went, they go too.", wrapCardPath)
	}
	return m[1]
}

// publishedParamNames returns one registered tool's published input-schema
// property names.
func publishedParamNames(t *testing.T, tool string) map[string]bool {
	t.Helper()
	raw, err := json.Marshal(publishedTool(t, tool).InputSchema)
	if err != nil {
		t.Fatalf("re-marshal %s's input schema: %v", tool, err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("%s's input schema is not valid JSON: %v", tool, err)
	}
	out := map[string]bool{}
	for k := range schema.Properties {
		out[k] = true
	}
	if len(out) == 0 {
		t.Fatalf("%s publishes no parameters at all, so the comparison below is vacuous", tool)
	}
	return out
}

// TestWrapSendsNoForceTerminateStep is the "never sets" clause, with the flag
// name taken from the card and cross-checked against the tool that DOES publish
// it.
//
// 🔴 Both checks are needed and neither is redundant. "pf_wrap's body has no
// `force_terminate_step`" is trivially satisfied by a flag name that exists
// nowhere — which is what a typo in the card, or a rename on the server, looks
// like from inside this test. Requiring the name to be a real published
// parameter of pf_complete_attempt, and absent from pf_wrap's own schema, is
// what makes the absence a decision rather than a spelling.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M1  pf_wrap's completion body sends the flag                       RED
//	M2  the card renames the flag                                      RED (schema arm)
//	M3  pf_complete_attempt stops publishing the flag                  RED (schema arm)
//	M4  pf_wrap's completion body sends a non-wrapped status            RED (status arm)
//	M5  green control: reword the card's clause around the same token   GREEN
func TestWrapSendsNoForceTerminateStep(t *testing.T) {
	flag := publishedFlagName(t)

	// The flag has to be real, and it has to belong to the other tool.
	if !publishedParamNames(t, "pf_complete_attempt")[flag] {
		t.Fatalf("the card says pf_wrap never sets %q, and pf_complete_attempt does not publish "+
			"a parameter by that name either. The clause is then about a flag that does not "+
			"exist, and the two sentences resting on it are unfounded rather than merely "+
			"unprobed", flag)
	}
	if publishedParamNames(t, "pf_wrap")[flag] {
		t.Errorf("pf_wrap now publishes %q as a parameter of its own, so a caller CAN set it and "+
			"\"it never sets\" is no longer the whole story", flag)
	}

	f := wrapFixture(t, false)
	out, isErr := callToolBounded(t, f, "pf_wrap", map[string]any{
		"work_item_id": resolveCanonical, "repo": "aihub",
		"pr_title": "wrapped", "pr_body": "body",
	}, 60*time.Second)
	if isErr {
		t.Fatalf("pf_wrap failed: %v", out)
	}

	body := completeCall(t, f).Body
	if body == nil {
		t.Fatal("the completion request carried no JSON body at all")
	}
	if v, has := body[flag]; has {
		t.Errorf("pf_wrap's completion body carries %q=%v. The server terminates the open step "+
			"when that flag is true, so a wrap sending it would silently close a step still "+
			"reporting in_progress — the thing the card says this tool never does. Body keys: %v",
			flag, v, keysOf(body))
	}
	if got := body["status"]; got != "wrapped" {
		t.Errorf("the completion body sends status=%v, want \"wrapped\" — without that this arm "+
			"is reading a request pf_wrap does not actually make", got)
	}
}

// countWords maps the card's own English count onto a number. Closed, small and
// deliberately not a general number parser: the point is that the card states a
// count in prose and this arm compares its tally against THAT, so a card saying
// "three times" must redden rather than be normalised away.
var countWords = map[string]int{"once": 1, "twice": 2, "three times": 3, "four times": 4}

// publishedNoteRepeatCount reads how many times the card says the note is
// recorded on the retry.
func publishedNoteRepeatCount(t *testing.T) int {
	t.Helper()
	raw, err := os.ReadFile(wrapCardPath)
	if err != nil {
		t.Fatalf("read %s: %v", wrapCardPath, err)
	}
	flat := strings.Join(strings.Fields(string(raw)), " ")
	re := regexp.MustCompile(`the note is recorded ([a-z]+(?: times)?) on that retry`)
	m := re.FindStringSubmatch(flat)
	if m == nil {
		t.Fatalf("%s no longer states how many times the note is recorded on that retry, so "+
			"this arm has no published count to compare its tally against. That count is the "+
			"whole of what the sentence promises a reader, and an arm asserting its own number "+
			"instead would keep passing while the card said something else.", wrapCardPath)
	}
	n, ok := countWords[m[1]]
	if !ok {
		t.Fatalf("%s says the note is recorded %q times and this arm cannot turn that into a "+
			"number. Add it to countWords, or the card is stating a count nothing checks",
			wrapCardPath, m[1])
	}
	return n
}

// TestWrapRecordsTheNoteAgainWhenTheCompletionFailed is the "recorded twice on
// that retry" clause, driven rather than reasoned about, with the count itself
// read out of the card.
//
// The ordering that causes it is deliberate and documented: the note must reach
// the timeline BEFORE the completion, because the completion deletes the
// credentials the note authenticates with. So a completion that fails leaves the
// note already emitted, and the retry emits it again. That is the behaviour the
// card describes as noise rather than damage, and it is only "documented rather
// than solved" for as long as somebody can tell it is still true.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M1  emit the note AFTER the completion instead                RED (order + count)
//	M2  emit the note at most once                                RED (count arm)
//	M3  the card says "three times"                               RED (published count)
//	M4  the card says "once"                                      RED (published count)
//	M5  green control: reword the card's prose around the count    GREEN
//
// ⚠️ M3 went GREEN in the first version of this arm, which drove the `attempts`
// constant above: the loop ran `want` times and then asserted `want` notes, so
// three wraps recorded three notes and the published count could not be wrong.
func TestWrapRecordsTheNoteAgainWhenTheCompletionFailed(t *testing.T) {
	want := publishedNoteRepeatCount(t)

	// 🔴 The number of ATTEMPTS is fixed at two and is NOT taken from the card.
	// Measured 2026-09-10: driving `want` attempts and then asserting `want`
	// notes made the published count unfalsifiable — a card saying "three times"
	// drove three wraps, recorded three notes and passed green. The card's clause
	// is about one call plus "that retry", so two attempts is what the sentence
	// describes and the count is the only free variable left to compare.
	const attempts = 2
	if want < attempts {
		t.Fatalf("the card says the note is recorded %d time(s) across a call and its retry. "+
			"Below %d there is no duplicate to describe, and the Open item calling it \"stated "+
			"behaviour, not a settled design\" is then about something that no longer happens",
			want, attempts)
	}

	f := wrapFixture(t, false)
	f.on("/v1/work_items/"+resolveCanonical+"/complete", func(map[string]any) (int, any) {
		return http.StatusConflict, map[string]any{
			"code":    "CONFLICT_STEP_IN_PROGRESS",
			"message": "a step is still in_progress; set force_terminate_step=true or update step first",
		}
	})

	const note = "wrapped: the note that gets recorded twice"
	args := map[string]any{
		"work_item_id": resolveCanonical, "repo": "aihub",
		"pr_title": "wrapped", "pr_body": "body", "note": note,
	}

	for attempt := 1; attempt <= attempts; attempt++ {
		out, isErr := callToolBounded(t, f, "pf_wrap", args, 60*time.Second)
		if !isErr {
			t.Fatalf("attempt %d: pf_wrap succeeded although the completion answered 409 "+
				"CONFLICT_STEP_IN_PROGRESS: %v. The premise of this arm is the failure that "+
				"actually happens; without it there is no retry to count notes on", attempt, out)
		}
	}

	// The count: one note per attempt, because nothing dedupes them.
	var notePositions, completePositions []int
	for i, c := range f.recorded() {
		switch {
		case c.Path == "/v1/events" && c.Body != nil && c.Body["event_type"] == "note":
			notePositions = append(notePositions, i)
		case strings.HasSuffix(c.Path, "/complete"):
			completePositions = append(completePositions, i)
		}
	}
	notes := notePositions
	if len(notes) != want {
		t.Errorf("%d wraps carrying the same note recorded %d note event(s), and the card says "+
			"%d. The card calls the duplicate noise rather than damage and documents it instead "+
			"of solving it; if the real number has moved in either direction, that sentence and "+
			"the Open item under it are both stale. Requests: %v",
			attempts, len(notes), want, f.paths())
	}
	if len(completePositions) != attempts {
		t.Fatalf("expected %d completion attempts, saw %d: %v",
			attempts, len(completePositions), f.paths())
	}

	// And the ordering that causes it: each note precedes the completion of its
	// own attempt. This is the half that would go red if somebody "fixed" the
	// duplicate by moving the note after the terminal call — which would lose the
	// note entirely on the successful path, since the completion deletes the
	// credentials it needs.
	for i, np := range notePositions {
		if i < len(completePositions) && np > completePositions[i] {
			t.Errorf("attempt %d emitted its note at request %d, AFTER the completion at %d. A "+
				"note emitted after the terminal call has no credentials to authenticate with, "+
				"so that ordering does not fix the duplicate — it drops the note", i+1, np,
				completePositions[i])
		}
	}
}

// TestWrapDeletesBothTheCanonicalAndThePassedStateFileKeys is the state-file
// sentence, both halves.
//
// The "passed key" half only exists when the two keys differ, which is why this
// addresses the call by SLUG and seeds the slug-keyed pre-claim stub the
// resolver is designed to look past. A stub that survives is not cosmetic: it
// holds an attempt_id the server has just retired, and config.ReadStateFile's
// filename lookup finds it before the resolver's scan ever runs.
//
// The "mirroring" half is checked rather than taken on trust, by driving the tool
// the card names through the same fixture and requiring the same two deletions.
// Two independent cleanup blocks in two files claiming to agree, with nothing
// forcing them to, is the shape this repo has been burned by before; and it is
// also what gives this arm a publication side, since a card naming a different
// tool then has to be a card that is wrong about it.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M1  pf_wrap drops the passed-key delete                   RED
//	M2  pf_wrap drops the canonical-key delete                RED
//	M3  the MIRRORED tool drops its passed-key delete         RED (mirror arm)
//	M4  the card says it mirrors `pf_pause_attempt`, which
//	    deletes no state file at all                          RED (mirror arm)
//	M5  green control: reword the sentence around the same
//	    two key names                                         GREEN
func TestWrapDeletesBothTheCanonicalAndThePassedStateFileKeys(t *testing.T) {
	for _, tool := range []string{"pf_wrap", publishedMirroredTool(t)} {
		t.Run(tool, func(t *testing.T) { assertBothStateKeysDeleted(t, tool) })
	}
}

// publishedMirroredTool reads the tool the card says this cleanup mirrors.
func publishedMirroredTool(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(wrapCardPath)
	if err != nil {
		t.Fatalf("read %s: %v", wrapCardPath, err)
	}
	flat := strings.Join(strings.Fields(string(raw)), " ")
	re := regexp.MustCompile("passed key, mirroring `(pf_[a-z_]+)`")
	m := re.FindStringSubmatch(flat)
	if m == nil {
		t.Fatalf("%s no longer says which tool this cleanup mirrors, so the agreement between "+
			"the two cleanup blocks is unstated and unchecked", wrapCardPath)
	}
	return m[1]
}

func assertBothStateKeysDeleted(t *testing.T, tool string) {
	t.Helper()
	f := wrapFixture(t, true)

	// The premise: both files exist before the call, or "gone afterwards" is not
	// evidence of anything.
	for _, key := range []string{resolveCanonical, resolveSlug} {
		if _, err := config.ReadStateFile(key); err != nil {
			t.Fatalf("state file for %q is not there before the wrap (%v), so its absence "+
				"afterwards would prove nothing", key, err)
		}
	}

	args := map[string]any{
		"work_item_id": resolveSlug, "repo": "aihub",
		"pr_title": "wrapped by slug", "pr_body": "body",
	}
	if tool != "pf_wrap" {
		// The mirrored tool is a lifecycle call, not a coding one: it takes the
		// terminal status rather than a PR, and the status is what gates the
		// cleanup at all.
		args = map[string]any{"work_item_id": resolveSlug, "status": "wrapped"}
	}
	out, isErr := callToolBounded(t, f, tool, args, 60*time.Second)
	if isErr {
		t.Fatalf("%s failed: %v", tool, out)
	}
	// The completion has to have reached the server, or the deletions below are
	// not the ones this sentence is about.
	//
	// ⚠️ Only pf_wrap's URL is pinned to the RESOLVED key. Measured 2026-09-10:
	// pf_wrap posts to sf.WIID and the mirrored lifecycle tool posts to the key
	// the caller passed, so a slug-addressed completion goes to
	// /v1/work_items/<slug>/complete. Both work — the server resolves a slug —
	// and neither is this sentence's subject, which is the state-file cleanup. It
	// is noted rather than asserted because writing the difference into an
	// assertion would freeze it as though somebody had decided it.
	got := completeCall(t, f).Path
	if tool == "pf_wrap" && got != "/v1/work_items/"+resolveCanonical+"/complete" {
		t.Fatalf("pf_wrap's completion went to %s, so the call resolved to a different work item "+
			"than this arm is about", got)
	}
	if !strings.Contains(got, resolveCanonical) && !strings.Contains(got, resolveSlug) {
		t.Fatalf("the completion went to %s, which names neither key this arm is about", got)
	}

	if _, err := config.ReadStateFile(resolveCanonical); err == nil {
		t.Errorf("the canonical state file %s.json survived "+tool+". Its credentials are dead "+
			"the moment the attempt completes, so leaving it on disk hands the next call an "+
			"attempt_id the server rejects", resolveCanonical)
	}
	if _, err := config.ReadStateFile(resolveSlug); err == nil {
		t.Errorf("the slug-keyed file %s.json survived a "+tool+" addressed by that very key. "+
			"config.ReadStateFile is a filename lookup, so every later slug-addressed call in "+
			"this workspace finds the stale file BEFORE the resolver's scan runs — and answers "+
			"409 as though somebody had stolen the attempt", resolveSlug)
	}
}

// lifecycleEndClaim is what the card's process-convention sentence promises about
// the in-tree plugin reference.
type lifecycleEndClaim struct {
	// Reference is the repo-relative path of the file the card names.
	Reference string
	// Tool is the call the card says that file ends the run at.
	Tool string
	// Status is the status argument the card shows on it.
	Status string
}

// publishedLifecycleEndClaim reads the three tokens out of the card sentence.
func publishedLifecycleEndClaim(t *testing.T) lifecycleEndClaim {
	t.Helper()
	raw, err := os.ReadFile(wrapCardPath)
	if err != nil {
		t.Fatalf("read %s: %v", wrapCardPath, err)
	}
	flat := strings.Join(strings.Fields(string(raw)), " ")
	re := regexp.MustCompile(
		"`(plugins/[A-Za-z0-9_./-]+\\.md)` — ends the run at `(pf_[a-z_]+)\\([^`]*status=\\\\?\"([a-z]+)\\\\?\"\\)`")
	m := re.FindStringSubmatch(flat)
	if m == nil {
		t.Fatalf("%s no longer names the in-tree lifecycle reference and the call it ends the run "+
			"at, in the form this arm reads. That sentence is the only place the process "+
			"convention is written down for a reader of this card, so a silent reword is what it "+
			"exists to prevent.\nSentence sought: `plugins/….md` — ends the run at "+
			"`pf_xxx(…status=\"yyy\")`", wrapCardPath)
	}
	c := lifecycleEndClaim{Reference: m[1], Tool: m[2], Status: m[3]}
	t.Logf("card claims: %s ends the run at %s(status=%q)", c.Reference, c.Tool, c.Status)
	return c
}

// fencedBlocks returns the fenced code blocks of a markdown file.
//
// Blocks rather than the whole text, and that is the load-bearing choice: this
// file MENTIONS pf_wrap in prose ("if pf_complete_attempt / pf_wrap do not
// publish a note parameter") while never CALLING it, and a whole-text scan
// would read that mention as the reference ending the run here.
func fencedBlocks(t *testing.T, text string) []string {
	t.Helper()
	var out []string
	var cur []string
	open := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if open {
				out = append(out, strings.Join(cur, "\n"))
				cur = nil
			}
			open = !open
			continue
		}
		if open {
			cur = append(cur, line)
		}
	}
	if open {
		t.Fatalf("the markdown has an unclosed fence, so this walk read the tail of the file as "+
			"one block and its prose as code")
	}
	return out
}

// TestTheInTreeLifecycleReferenceEndsTheLoopAtCompleteAttempt is the process
// convention, checked against the file the card names.
//
// The card says this is "a process convention, not a property of the tool", and
// records it because a card that only described the tool would leave a reader
// thinking otherwise. That makes it a claim about a file in THIS repo, which is
// checkable — and the cheapest thing to do with it would have been to call it
// prose-only for naming `plugins/**`. It is not: the file is right here, and the
// convention drifting out of the reference while the card still describes it is
// the ordinary way this sentence goes wrong.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M1  the reference's end-of-loop block calls pf_wrap instead   RED
//	M2  every documented sequence drops status="wrapped"          RED
//	M3  the card names pf_pause_attempt                           RED
//	M4  the card points at a plugins/ file that is not there      RED
//	M5  green control: reword the reference's surrounding prose,
//	    including its pf_wrap mention                             GREEN
//
// ⚠️ M2 stayed GREEN when it touched only the wrap-and-cleanup block, because
// the reference carries a second sequence (the older-binary fallback) ending at
// the same call. That is the arm behaving as written: the claim is over the
// reference's documented sequences as a SET — one of them makes the call, none
// of them calls this tool — so the mutant has to remove it from all of them.
func TestTheInTreeLifecycleReferenceEndsTheLoopAtCompleteAttempt(t *testing.T) {
	claim := publishedLifecycleEndClaim(t)

	raw, err := os.ReadFile("../../" + claim.Reference)
	if err != nil {
		t.Fatalf("read %s: %v — the card names this file as the place the convention is written "+
			"down, so a card pointing at a file that is not there is the rot this arm is for",
			claim.Reference, err)
	}
	blocks := fencedBlocks(t, string(raw))
	if len(blocks) == 0 {
		t.Fatalf("%s has no fenced code block, so there is no call sequence in it to read and "+
			"every assertion below would be vacuous", claim.Reference)
	}

	// The card's own tool, derived from the card rather than written here, so
	// this arm follows the card if it is ever renamed.
	self := strings.TrimSuffix(strings.TrimPrefix(wrapCardPath, "../../docs/mcp-cards/"), ".md")

	endsAtClaimed, callsSelf := false, false
	for _, b := range blocks {
		if strings.Contains(b, claim.Tool+"(") && strings.Contains(b, `status="`+claim.Status+`"`) {
			endsAtClaimed = true
		}
		if strings.Contains(b, self+"(") {
			callsSelf = true
		}
	}

	if !endsAtClaimed {
		t.Errorf("no code block in %s calls %s with status=%q. The card tells a reader that is "+
			"where the run ends; a reader following the reference instead finds something else, "+
			"and the card is the only one of the two that says which is intended",
			claim.Reference, claim.Tool, claim.Status)
	}
	if callsSelf {
		t.Errorf("a code block in %s calls %s, so it is now part of the documented end-of-loop "+
			"sequence and the card's \"this tool is not the end-of-loop call\" is false. The "+
			"convention changed or the card did; they cannot both stand", claim.Reference, self)
	}
}
