package mcp_test

// aihub#543 probe wave 1, slice C — the `docs/mcp-cards/pf_complete_attempt.md`
// sentence about the fused wrap, which is the caller that actually meets the
// two hop-4 rules above it.
//
//	"The terminal half is why the fused `pf_wrap`, which completes as `wrapped`
//	 and never sets the flag, is refused while a step is still live — and every
//	 retry re-sends its note."
//	    -> TestWrapCompletesAsWrappedWithNoFlagAndEveryRetryResendsItsNote
//
// 🔴 WHY NO EXISTING ARM HOLDS IT. TestSlugAddressedWrapCompletesUnderThe
// CanonicalID drives the same tool and asserts the URL and the credentials —
// the two things aihub#149 was about. It never looks at the body's `status`,
// and an absent key is invisible to it by construction, so "never sets the
// flag" would survive a wrap that set it. The note-duplication half is worse
// off: TestFusedNoteFailureIsReportedNotSwallowed drives a failing NOTE, which
// is the opposite case — there the note is what broke. The case the card
// describes is a note that LANDED and a completion that did not, which is the
// only combination that duplicates anything, and nothing drove it.
//
// The pf_wrap harness (a real git worktree, a fake gh answering with a PR that
// already covers HEAD) is state_resolve_wiring_test.go's, reused rather than
// rebuilt: coding.Wrap then takes its idempotent no-op branch, so neither arm
// below depends on a push succeeding and a retry is a genuine second attempt at
// the same completion.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run TestWrapCompletesAsWrapped -count=1

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
)

// wrapCompletePath is the completion this tool makes on the way out.
const wrapCompletePath = "/v1/work_items/" + resolveCanonical + "/complete"

// noteEventCount counts the `note` events the tool emitted, which is not the
// same as counting requests to /v1/events: pf_wrap emits push and pr_opened
// events down the same route, and a count that included them would move for
// reasons that have nothing to do with the note.
func noteEventCount(t *testing.T, f *fakeAihub) int {
	t.Helper()
	n := 0
	for _, c := range f.recorded() {
		if c.Path == notesPath && c.Body["event_type"] == "note" {
			n++
		}
	}
	return n
}

// newWrapHarness puts a fresh workspace, a real repo and a fake gh in place and
// returns the fake aihub, ready for one or more pf_wrap calls.
func newWrapHarness(t *testing.T) *fakeAihub {
	t.Helper()
	root := newResolveWorkspace(t)
	r := newResolveRepo(t, root)
	fakeGHForResolve(t, fmt.Sprintf(
		`[{"url":"https://example.invalid/pr/3","number":3,"state":"OPEN","baseRefName":"main","commits":[{"oid":%q}]}]`,
		r.head))
	writeResolveCanonical(t, map[string]string{"aihub": r.wt})
	return newFakeAihub(t)
}

// TestWrapCompletesAsWrappedWithNoFlagAndEveryRetryResendsItsNote pins the two
// halves of the card's sentence about the fused wrap.
//
// The halves are separable and both matter. The body half is why pf_wrap is the
// tool that meets ErrConflictStepInProgress at all: it hard-codes the one
// status where the flag decides something and never sets the flag, so a live
// step is a refusal rather than a force-terminate. The retry half is what that
// refusal costs — the note was already on the timeline before the completion
// was attempted, and the caller's only recovery re-sends it.
//
// MUTANTS (applied to this tree and run; the verdict is what happened):
//
//	M32 enforcement: add `body["force_terminate_step"] = true` to pf_wrap's
//	    completion body in internal/mcp/tools_coding.go
//	                                            RED  wrapped_with_no_flag
//	M33 enforcement: change pf_wrap's `"status": "wrapped"` to "paused"
//	                                            RED  wrapped_with_no_flag, naming
//	                                                 the status found
//	M34 enforcement: move pf_wrap's emitNote block BELOW the CompleteAttempt call
//	                                            RED  every_retry_resends_the_note
//	                                                 (0 notes for 2 refused
//	                                                 completions) — which is also
//	                                                 the shape that loses the note
//	                                                 altogether on a successful
//	                                                 wrap, since by then the
//	                                                 credentials are gone
//	M35 enforcement: drop the noteOutcomeSuffix from pf_wrap's completion error
//	                                            RED  the_refusal_says_the_note
//	                                                 _already_landed
//	M36 publication: delete the citation clause from the card's pf_wrap sentence
//	                                            RED  K12 DEBT_GROWTH; this arm
//	                                                 reads the wire, so K12's
//	                                                 citation binding is its
//	                                                 publication side
func TestWrapCompletesAsWrappedWithNoFlagAndEveryRetryResendsItsNote(t *testing.T) {
	t.Run("wrapped_with_no_flag", func(t *testing.T) {
		f := newWrapHarness(t)

		result, isErr := callToolBounded(t, f, "pf_wrap", map[string]any{
			"work_item_id": resolveCanonical,
			"repo":         "aihub",
			"pr_title":     "probe the wrap body",
			"pr_body":      "body",
			"derived":      []any{},
		}, 60*time.Second)
		if isErr {
			t.Fatalf("pf_wrap failed: %v", result)
		}

		body := lastBodyFor(t, f, wrapCompletePath)
		// FLOOR: the credentials must be on the body, or the absence assertion
		// below is satisfied by a body carrying nothing.
		for _, k := range completeAttemptCredentials {
			if v, present := body[k]; !present || v == nil || v == "" {
				t.Fatalf("pf_wrap's completion body carries no usable %q (keys=%v) — this walk is "+
					"broken", k, sortedBodyKeys(body))
			}
		}
		if body["status"] != "wrapped" {
			t.Errorf("pf_wrap completed with status=%#v, want \"wrapped\". The tool's whole name is "+
				"that status; a wrap that completed as anything else would leave the work item "+
				"somewhere its caller cannot see", body["status"])
		}
		if v, present := body["force_terminate_step"]; present {
			t.Errorf("pf_wrap's completion body carries force_terminate_step=%#v.\nThe card's "+
				"sentence turns on this absence: `wrapped` is one of the two statuses where the "+
				"flag is the only way past a live step, and pf_wrap not setting it is why the "+
				"refusal — and the duplicate note the next arm measures — happen at all. Setting "+
				"it here would silently force-terminate somebody's running step on every wrap.", v)
		}
		want := append([]string{"status", "derived"}, completeAttemptCredentials...)
		sort.Strings(want)
		if got := sortedBodyKeys(body); len(got) != len(want) {
			t.Errorf("pf_wrap's completion body carries %v, want exactly %v", got, want)
		}
	})

	t.Run("every_retry_resends_the_note", func(t *testing.T) {
		f := newWrapHarness(t)
		f.on(wrapCompletePath, func(map[string]any) (int, any) {
			// The refusal the previous arm explains: a step is still in_progress
			// and this call never sets the flag.
			return http.StatusConflict, map[string]any{
				"error": map[string]any{
					"code":    "CONFLICT_STEP_IN_PROGRESS",
					"message": "a step is still in_progress; set force_terminate_step=true or update step first",
				},
			}
		})

		const note = "wrapped: the note that outlives its completion"
		var lastText string
		for attempt := 1; attempt <= 2; attempt++ {
			result, isErr := callToolBounded(t, f, "pf_wrap", map[string]any{
				"work_item_id": resolveCanonical,
				"repo":         "aihub",
				"pr_title":     "probe the wrap retry",
				"pr_body":      "body",
				"note":         note,
				"derived":      []any{},
			}, 60*time.Second)
			if !isErr {
				t.Fatalf("attempt %d succeeded against a server that refused the completion: %v — "+
					"this arm needs the refusal, or the retry it measures is not a retry",
					attempt, result)
			}
			lastText, _ = result["_raw"].(string)
			if notes := noteEventCount(t, f); notes != attempt {
				t.Fatalf("after attempt %d the timeline holds %d note event(s), want %d (paths=%v)",
					attempt, notes, attempt, f.paths())
			}
		}

		if notes := noteEventCount(t, f); notes != 2 {
			t.Errorf("two refused wraps recorded %d note event(s), want 2.\nThe note is emitted "+
				"before the completion because the completion deletes the credentials it needs, so "+
				"a completion that fails leaves the note behind and the caller's only recovery is "+
				"to make the whole call again", notes)
		}

		t.Run("the_refusal_says_the_note_already_landed", func(t *testing.T) {
			if lastText == "" {
				t.Fatalf("the refusal carried no text")
			}
			if !strings.Contains(lastText, "note") {
				t.Errorf("the refusal is %q and says nothing about the note.\nThe caller is about "+
					"to decide whether to retry and retrying re-sends the note, so whether the note "+
					"already landed is exactly the fact the decision needs", lastText)
			}
			if !strings.Contains(lastText, "second time") {
				t.Errorf("the refusal is %q and does not warn that a retry records the note again. "+
					"Without that clause the duplicate this arm just measured arrives unannounced.",
					lastText)
			}
		})
	})
}
