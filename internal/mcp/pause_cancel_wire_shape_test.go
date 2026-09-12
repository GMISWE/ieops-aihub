package mcp_test

// aihub#543 probe wave 2, lane L11 — the `docs/mcp-cards/pf_pause_attempt.md`
// and `docs/mcp-cards/pf_cancel_work_item.md` sentences about WHAT IS PUBLISHED,
// WHAT LEAVES THIS PROCESS, and WHAT THE LOCAL STATE FILE DOES.
//
//	"The description carries the lock semantics … pausing **releases**
//	 `file_scope` locks acquired mid-attempt and **retains** every other lock
//	 type for resume."
//	    -> TestPublishedPauseReleasesOneLockTypeFromTheLiveVocabulary
//	"⚠️ Since `aihub#416` that retained set is normally EMPTY … so the
//	 description says so…"
//	    -> TestPublishedPauseReleasesOneLockTypeFromTheLiveVocabulary
//	"The request is addressed by `sf.WIID` — the **resolved canonical** id…"
//	    -> TestPauseAddressesTheCanonicalIDAndSendsAReasonOnlyWhenGiven
//	"`pause_reason` is forwarded only when non-empty…"
//	    -> TestPauseAddressesTheCanonicalIDAndSendsAReasonOnlyWhenGiven
//	"The attempt's status becomes `paused` and the local state file is **kept**…"
//	    -> TestPauseKeepsTheStateFileAndATerminalCompletionDeletesIt
//	"…sends `{reason}` — and only when non-empty — to … `POST
//	 /v1/work_items/<id>/cancel`…"
//	    -> TestCancelSendsOnlyAReasonAndNoAttemptCredential
//
// 🔴 WHERE THE ENFORCEMENT HALF OF THE LOCK SENTENCE LIVES. The released type is
// an unexported SQL constant in internal/domain (acquireLocksReleasePausedSQL),
// so this package cannot read it and that package cannot open a live MCP
// session. The pair is therefore two arms in two packages, and each is useless
// without the other:
//
//	internal/domain  TestAcquireLocksReleasePausedSQL_FileScopeOnly and
//	                 …_NotAllLocks hold the DELETE's predicate — file_scope, and
//	                 not an unfiltered delete.
//	here             the published sentence names that one type and no other
//	                 member of the live lock vocabulary, and says what happens to
//	                 the rest.
//
// A description rewritten to promise the wrong release reddens here; a pause
// widened to delete every lock reddens there. Neither direction is covered by
// the other, which is why the card's sentence cites both.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestPublishedPause|TestPauseAddresses|TestPauseKeeps|TestCancelSends' -count=1

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/domain"
)

const (
	pauseWIID  = "wi_01JPAUSECARD"
	pauseSlug  = "aihub#583"
	cancelWIID = "wi_01JCANCELCARD"
)

const (
	pauseWirePath  = "/v1/work_items/" + pauseWIID + "/pause"
	cancelWirePath = "/v1/work_items/" + cancelWIID + "/cancel"
)

// pausedReleasedLockType is the one lock type the pause path releases. It is
// written down here because the published SENTENCE is the subject of this arm —
// but it is checked against domain.ResourceLockTypeList() below rather than
// trusted, so a rename in the vocabulary reddens instead of quietly leaving a
// description naming a type that no longer exists.
const pausedReleasedLockType = "file_scope"

// seedPauseState writes a claimed state file keyed on the CANONICAL id while
// also registering the slug, which is the shape a slug-addressed call meets.
func seedPauseState(t *testing.T, wiID string) {
	t.Helper()
	t.Setenv("POLYFORGE_WORKSPACE_ROOT", t.TempDir())
	sf := &config.StateFile{
		WIID:          wiID,
		Project:       "aihub",
		Slug:          pauseSlug,
		AttemptID:     "ra_pause",
		ClaimEpoch:    4,
		SessionSecret: "pause-secret",
		Claimed:       true,
	}
	// WriteClaimState is what pf_claim_work_item uses: it writes the canonical
	// file and leaves the slug resolvable, so ResolveStateFile(slug) finds it.
	if err := config.WriteClaimState(pauseSlug, wiID, sf); err != nil {
		t.Fatalf("write claim state: %v", err)
	}
}

// TestPublishedPauseReleasesOneLockTypeFromTheLiveVocabulary is the card's hop
// 0-1, both paragraphs.
//
// The quantified half is the one that earns its keep: the description must name
// exactly ONE member of the live lock vocabulary as released. A description that
// also named git_branch or deploy_env would be a promise the pause SQL does not
// keep, and the failure mode is the expensive direction — a caller told a lock
// was released holds a work item it believes is unblocked.
//
// MUTANTS (applied to this tree and run; the verdict is what happened):
//
//	M18 publication: delete " — since aihub#416 that set is normally empty,
//	    because file_scope is the only lock the server derives" from the tool
//	    description                             RED  the_description_says_the
//	                                                 _retained_set_is_normally
//	                                                 _empty
//	M19 publication: change "releases file_scope locks" to "releases git_branch
//	    locks"                                  RED  exactly_one_vocabulary_type
//	                                                 _is_named_as_released
//	M20 publication: add "and deploy_env" to the released clause
//	                                            RED  exactly_one_vocabulary_type
//	                                                 _is_named_as_released
//	M21 publication: drop "retained for resume" from the description
//	                                            RED  the_description_states_the
//	                                                 _retention
//	M22 enforcement: rename file_scope to file_lock in domain.resourceLockTypes
//	                                            RED  the_named_type_is_a_live
//	                                                 _lock_type — the arm refuses
//	                                                 a description naming a type
//	                                                 the vocabulary has dropped
//	M23 publication: delete the citation clause from the card's hop 0-1 paragraph
//	                                            RED  K12 DEBT_GROWTH
//
// ⚠️ M18-M21 edit the TOOL DESCRIPTION, so each of them also reddens K3, which
// compares the card's description_sha256 against the live registry. That is a
// different finding from a different arm and is not what these mutants
// establish: K3 fires on any description edit whatever, including one that made
// the sentence MORE accurate, so it cannot tell a lock promise the server keeps
// from one it does not. Each run above was scoped to this arm for that reason.
func TestPublishedPauseReleasesOneLockTypeFromTheLiveVocabulary(t *testing.T) {
	desc := publishedTool(t, "pf_pause_attempt").Description

	// FLOOR: the vocabulary has to have several members, or "exactly one is
	// named" is a statement about a one-element set and holds for free.
	vocab := domain.ResourceLockTypeList()
	if len(vocab) < 3 {
		t.Fatalf("domain.ResourceLockTypeList() = %v; this arm subtracts the released type from "+
			"the vocabulary and asserts the remainder is absent, which says nothing about a set "+
			"this small", vocab)
	}

	t.Run("the_named_type_is_a_live_lock_type", func(t *testing.T) {
		found := false
		for _, lt := range vocab {
			if lt == pausedReleasedLockType {
				found = true
			}
		}
		if !found {
			t.Fatalf("%q is not in domain.ResourceLockTypeList() (%v).\nThe published sentence "+
				"names it as the type a pause releases; a name the lock vocabulary no longer "+
				"carries is a promise about nothing, and this is the direction a rename takes.",
				pausedReleasedLockType, vocab)
		}
	})

	t.Run("exactly_one_vocabulary_type_is_named_as_released", func(t *testing.T) {
		if !strings.Contains(desc, pausedReleasedLockType) {
			t.Errorf("the pf_pause_attempt description never names %q.\nThe card says the "+
				"description carries the lock semantics precisely because they differ from every "+
				"other terminal path — a caller who has to guess which locks survive a pause is "+
				"the reader this sentence exists for. Description:\n%s",
				pausedReleasedLockType, desc)
		}
		for _, lt := range vocab {
			if lt == pausedReleasedLockType {
				continue
			}
			if strings.Contains(desc, lt) {
				t.Errorf("the pf_pause_attempt description names %q as well as %q.\nThe pause "+
					"DELETE is filtered to one resource_type (internal/domain, "+
					"TestAcquireLocksReleasePausedSQL_FileScopeOnly), so any second type named "+
					"here is a release the server does not perform — and a caller told a lock is "+
					"gone stops waiting for it. Description:\n%s", lt, pausedReleasedLockType, desc)
			}
		}
	})

	t.Run("the_description_states_the_retention", func(t *testing.T) {
		if !containsAny(desc, "retained for resume", "is retained", "retained") {
			t.Errorf("the pf_pause_attempt description says what is RELEASED and not what is "+
				"KEPT.\nThe two halves are one contract: a caller that reads only the release "+
				"clause cannot tell a pause from a terminal completion, and the whole point of the "+
				"sentence is that they differ. Description:\n%s", desc)
		}
	})

	t.Run("the_description_says_the_retained_set_is_normally_empty", func(t *testing.T) {
		// The aihub#416 clause. Without it the retention reads as a live
		// mechanism, and a caller plans around holding a git_branch lock across a
		// pause that no longer exists to be held.
		if !containsAny(desc, "normally empty", "usually empty", "is normally empty") {
			t.Errorf("the pf_pause_attempt description promises a retention without saying that "+
				"the retained set is normally EMPTY.\nSince aihub#416 %q is the only lock the "+
				"server derives (internal/domain, TestResourceToLock_RepoAndServiceDeriveNoLock "+
				"with TestResourceToLock_PathStillDerivesFileScope), so the retention describes a "+
				"set a caller will not observe unless it supplied requested_locks itself. "+
				"Description:\n%s", pausedReleasedLockType, desc)
		}
		if !strings.Contains(desc, "aihub#416") {
			t.Errorf("the pf_pause_attempt description states the empty-set fact without naming "+
				"the change it came from. Description:\n%s", desc)
		}
	})
}

// TestPauseAddressesTheCanonicalIDAndSendsAReasonOnlyWhenGiven is the card's two
// hop 2-3 bullets, driven on the wire.
//
// The canonical-id half is asserted with a SLUG, because that is the only input
// under which the claim can be false: a handler that forwarded the caller's
// string would be indistinguishable from this one on a canonical id. The
// consequence is not cosmetic — /v1/work_items/<slug>/pause reaches
// GetWorkItem, which resolves slugs, so a slug-addressed pause "works" while
// silently disagreeing with the id every other call in the attempt used.
//
// MUTANTS:
//
//	M24 enforcement: in the pf_pause_attempt handler, call PauseAttempt(ctx,
//	    wiID, body) instead of PauseAttempt(ctx, sf.WIID, body)
//	                                            RED  a_slug_addressed_pause_uses
//	                                                 _the_canonical_id
//	M25 enforcement: assign body["pause_reason"] unconditionally
//	                                            RED  the_reason_is_omitted_when
//	                                                 _absent
//	M26 enforcement: drop the pause_reason assignment entirely
//	                                            RED  the_reason_is_forwarded_when
//	                                                 _given
//	M27 publication: delete the citation clause from the card's two hop 2-3
//	    bullets                                 RED  K12 DEBT_GROWTH
func TestPauseAddressesTheCanonicalIDAndSendsAReasonOnlyWhenGiven(t *testing.T) {
	drive := func(t *testing.T, addressedAs string, args map[string]any) (*fakeAihub, map[string]any) {
		t.Helper()
		seedPauseState(t, pauseWIID)
		f := newFakeAihub(t)
		full := map[string]any{"work_item_id": addressedAs}
		for k, v := range args {
			full[k] = v
		}
		result, isErr := callTool(t, f, "pf_pause_attempt", full)
		if isErr {
			t.Fatalf("pf_pause_attempt(%v) failed: %v", full, result)
		}
		return f, result
	}

	t.Run("a_slug_addressed_pause_uses_the_canonical_id", func(t *testing.T) {
		f, _ := drive(t, pauseSlug, nil)
		paths := f.paths()
		if len(paths) != 1 {
			t.Fatalf("pf_pause_attempt made requests %v, want exactly one", paths)
		}
		if paths[0] != pauseWirePath {
			t.Errorf("a pause addressed by the slug %q went to %q, want %q.\nThe state file holds "+
				"the resolved canonical id, and the card tells a caller the request is addressed "+
				"by it — an attempt whose pause is filed under a different spelling of the work "+
				"item than its claim was is the aihub#127 defect class.",
				pauseSlug, paths[0], pauseWirePath)
		}
		if strings.Contains(paths[0], "#") {
			t.Errorf("the pause URL still carries the slug's %q: %q", "#", paths[0])
		}
	})

	t.Run("the_reason_is_forwarded_when_given", func(t *testing.T) {
		const reason = "waiting on the owner's ruling"
		f, _ := drive(t, pauseWIID, map[string]any{"pause_reason": reason})
		body := lastBodyFor(t, f, pauseWirePath)
		if body["pause_reason"] != reason {
			t.Errorf("pause_reason = %#v, want %q. The reason is what pf_get_ready_queue surfaces "+
				"for a paused item, so a dropped one leaves the queue showing a pause nobody can "+
				"explain.", body["pause_reason"], reason)
		}
	})

	t.Run("the_reason_is_omitted_when_absent", func(t *testing.T) {
		f, _ := drive(t, pauseWIID, nil)
		body := lastBodyFor(t, f, pauseWirePath)
		if v, present := body["pause_reason"]; present {
			t.Errorf("the pause body carries pause_reason=%#v on a call that supplied none.\nThe "+
				"card says the guard is the same one pf_complete_attempt applies for the same "+
				"reason: FnCompleteAttempt writes what it is given, so an unguarded assignment "+
				"sends \"\" and turns \"paused, reason not given\" into \"paused for no stated "+
				"reason\" on every pause.", v)
		}
		// FLOOR: the credentials must be there, or the absence above is satisfied
		// by an empty body.
		want := []string{"attempt_id", "claim_epoch", "session_secret"}
		for _, k := range want {
			if v, present := body[k]; !present || v == nil || v == "" {
				t.Fatalf("the pause body carries no usable %q (keys=%v) — the walk is broken",
					k, sortedBodyKeys(body))
			}
		}
		got := sortedBodyKeys(body)
		sort.Strings(want)
		if len(got) != len(want) {
			t.Errorf("the pause body carries %v, want exactly %v for a call with no reason",
				got, want)
		}
	})
}

// TestPauseKeepsTheStateFileAndATerminalCompletionDeletesIt is the card's first
// hop-4 bullet, and the contrast in it is the assertion.
//
// "The state file is kept" alone is satisfied by a build that never deletes one,
// which is why the terminal completion runs in the same arm as the control. The
// card states the difference as the whole point of the tool — resume needs those
// credentials, and they exist in exactly one place.
//
// MUTANTS:
//
//	M28 enforcement: add config.DeleteStateFile(sf.WIID) after a successful
//	    PauseAttempt                            RED  a_pause_keeps_the_credentials
//	M29 enforcement: narrow the completion's delete guard to "failed" alone
//	                                            RED  CONTROL/a_wrap_deletes_them
//	M30 publication: delete the citation clause from the card's hop 4 bullet
//	                                            RED  K12 DEBT_GROWTH
//
// M31 is deliberately unused. It was drafted as "widen the completion's delete
// guard to include paused" and is NOT probative: pf_pause_attempt has its own
// handler, so that edit cannot reach the arm above. Recorded rather than
// renumbered away, because a mutant that turns out to test nothing is the same
// finding as one that goes green.
func TestPauseKeepsTheStateFileAndATerminalCompletionDeletesIt(t *testing.T) {
	t.Run("a_pause_keeps_the_credentials", func(t *testing.T) {
		seedPauseState(t, pauseWIID)
		f := newFakeAihub(t)
		f.on(pauseWirePath, func(map[string]any) (int, any) {
			return http.StatusOK, map[string]any{"status": "paused"}
		})
		result, isErr := callTool(t, f, "pf_pause_attempt", map[string]any{
			"work_item_id": pauseWIID,
			"pause_reason": "the owner has to rule on this",
		})
		if isErr {
			t.Fatalf("pf_pause_attempt failed: %v", result)
		}
		if result["status"] != "paused" {
			t.Errorf("the result is %v; the server answers {\"status\":\"paused\"} and the card "+
				"says the response is unprojected", result)
		}
		sf, err := config.ResolveStateFile(pauseWIID)
		if err != nil {
			t.Fatalf("the state file is gone after a pause (%v).\nResume re-enters through "+
				"pf_claim_work_item, and the card's own hop 4 says the kept file is the whole "+
				"difference from a terminal completion — with it deleted the attempt is not "+
				"resumable and its retained locks have no holder that can release them "+
				"(aihub#209).", err)
		}
		if sf.SessionSecret == "" || sf.AttemptID == "" {
			t.Errorf("the state file survived the pause but lost its credentials: %+v", sf)
		}
	})

	t.Run("CONTROL/a_wrap_deletes_them", func(t *testing.T) {
		seedPauseState(t, pauseWIID)
		f := newFakeAihub(t)
		result, isErr := callTool(t, f, "pf_complete_attempt", map[string]any{
			"work_item_id": pauseWIID,
			"status":       "wrapped",
			"derived":      []any{},
		})
		if isErr {
			t.Fatalf("pf_complete_attempt(wrapped) failed: %v", result)
		}
		if _, err := config.ResolveStateFile(pauseWIID); err == nil {
			t.Errorf("the state file survived a terminal completion.\nWithout this control the " +
				"arm above says nothing: \"a pause keeps the file\" is trivially true on a build " +
				"that never deletes one, and the card's sentence is about the CONTRAST.")
		}
	})
}

// TestCancelSendsOnlyAReasonAndNoAttemptCredential is the cancel card's hop 2-3
// paragraph and the sentence under it.
//
// The absent-credential half is the one with a consequence a caller acts on: the
// card says cancelling works on a work item this machine holds no state file
// for, and the observable form of that is a request body with no attempt
// credential in it. A handler that injected credentials would make the tool fail
// on precisely the work items the sentence promises it can reach.
//
// MUTANTS:
//
//	M32 enforcement: resolve the state file in the pf_cancel_work_item handler
//	    and add the three credentials to the body
//	                                            RED  the_body_is_the_reason_alone
//	M33 enforcement: assign body["reason"] unconditionally
//	                                            RED  an_absent_reason_is_omitted
//	M34 enforcement: drop the reason assignment
//	                                            RED  a_reason_is_forwarded
//	M35 publication: delete the citation clause from the card's hop 2-3 paragraph
//	                                            RED  K12 DEBT_GROWTH
func TestCancelSendsOnlyAReasonAndNoAttemptCredential(t *testing.T) {
	drive := func(t *testing.T, args map[string]any) map[string]any {
		t.Helper()
		// NO state file at all: the card's point is that this call needs none.
		t.Setenv("POLYFORGE_WORKSPACE_ROOT", t.TempDir())
		f := newFakeAihub(t)
		full := map[string]any{"work_item_id": cancelWIID}
		for k, v := range args {
			full[k] = v
		}
		result, isErr := callTool(t, f, "pf_cancel_work_item", full)
		if isErr {
			t.Fatalf("pf_cancel_work_item(%v) failed: %v.\nThe card says no attempt credential is "+
				"involved and the tool works on a work item this machine holds no state file for; "+
				"a failure with no state file present is that sentence being false.", full, result)
		}
		if paths := f.paths(); len(paths) != 1 || paths[0] != cancelWirePath {
			t.Fatalf("pf_cancel_work_item made requests %v, want exactly [%s]", paths, cancelWirePath)
		}
		return lastBodyFor(t, f, cancelWirePath)
	}

	t.Run("a_reason_is_forwarded", func(t *testing.T) {
		const reason = "superseded by aihub#583"
		body := drive(t, map[string]any{"reason": reason})
		if body["reason"] != reason {
			t.Errorf("reason = %#v, want %q", body["reason"], reason)
		}
		if got := sortedBodyKeys(body); len(got) != 1 {
			t.Errorf("the cancel body carries %v, want exactly [reason]", got)
		}
	})

	t.Run("an_absent_reason_is_omitted", func(t *testing.T) {
		body := drive(t, nil)
		if v, present := body["reason"]; present {
			t.Errorf("the cancel body carries reason=%#v on a call that supplied none. Both "+
				"directions are the claim: a handler that always sends the key satisfies \"sends "+
				"{reason}\" and says nothing about \"only when non-empty\".", v)
		}
	})

	t.Run("the_body_is_the_reason_alone", func(t *testing.T) {
		body := drive(t, map[string]any{"reason": "no credential needed"})
		for _, k := range []string{"attempt_id", "claim_epoch", "session_secret"} {
			if v, present := body[k]; present {
				t.Errorf("the cancel body carries %s=%#v.\nCancelling is authorized by project "+
					"ROLE, which is what lets it reach a work item claimed on another machine — "+
					"the case the card names. A credential in this body would either be absent "+
					"(and the call would fail on exactly those work items) or would be somebody "+
					"else's.", k, v)
			}
		}
	})
}
