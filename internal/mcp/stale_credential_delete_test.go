package mcp_test

// aihub#427 — pf_update_step's stale-credential path must delete the state file
// it actually READ, not the string the caller happened to address the work item
// by.
//
// THE DEFECT. Both stale-credential exits in internal/mcp/tools_step.go — the
// heartbeat branch and the ordinary branch — ran
//
//	outErr, deleteState := classifyStepUpdateErr(err)
//	if deleteState {
//	    _ = config.DeleteStateFile(wiID)   // the RAW caller argument
//	}
//
// while the credential they had just sent came from config.ResolveStateFile(wiID),
// which for a SLUG argument matches through its Slug scan and returns the
// CANONICAL <wi_id>.json. config.DeleteStateFile keys on whatever string it is
// given, so for a slug-addressed call it removed <slug>.json — a file that,
// after any normal claim, does not exist at all, because config.WriteClaimState
// deletes the pre-claim stub the moment the canonical file lands (aihub#141).
//
// The delete therefore removed NOTHING while classifyStepUpdateErr returned the
// literal text "STALE_LOCAL_CREDENTIAL: state file deleted; please re-claim
// this work item". The caller is told the credential is gone; it is still on
// disk; the next slug-addressed call resolves to it again and re-sends the same
// dead credential. Nothing in the loop terminates it — an agent that trusts the
// message and retries gets the identical answer forever.
//
// WHY THIS IS A BUG AND NOT A DESIGN CHOICE. Every other terminal site already
// deletes both keys, explicitly and with a comment saying why:
// pf_complete_attempt (tools_lifecycle.go) and pf_wrap (tools_coding.go) both do
// DeleteStateFile(sf.WIID) and then DeleteStateFile(wiID) when the two differ,
// each citing aihub#141. The author knew the two keys can disagree. These two
// sites were missed.
//
// WHAT WAS ALREADY COVERED, AND WHY IT DID NOT CATCH THIS. classifyStepUpdateErr
// is tested twice as a PURE FUNCTION — TestClassifyStepUpdateErr (tools_step_test.go)
// and the aihub#414 suite (error_code_classification_test.go) — and both are
// still correct. They assert what the classifier RETURNS: the error text and the
// deleteState boolean. Neither can see which key the caller then deletes by,
// because that decision is not in the function under test. The defect lives one
// hop downstream, in the wiring, which is precisely the gap
// state_resolve_wiring_test.go was written to close for the READ side of the
// same resolver. This file closes it for the DELETE side.
//
// Every assertion below therefore travels the whole path the model travels:
// MCP tool call -> handler -> state dir on disk -> HTTP request body -> error
// -> state dir on disk again.
//
// Why DB-free. The delete is a client-side filesystem effect reached from an
// HTTP status the fake aihub supplies directly. A database would add a skip
// condition and nothing else.
//
// ⚠️ Workspace safety. config.StateDir() is
// $POLYFORGE_WORKSPACE_ROOT/.polyforge/state and POLYFORGE_WORKSPACE_ROOT is its
// ONLY redirect; unset, config.FindWorkspaceRoot() walks up from the test
// binary's cwd and lands on the LIVE workspace, whose state directory holds
// every claimed work item's session_secret — and this file's whole subject is
// DELETING files out of that directory. Every arm goes through
// sandboxedWorkspace, which sets the variable and then verifies StateDir() is
// really inside the temp root before anything is written. Nothing here can reach
// the real workspace or ~/.polyforge.
//
//	go test ./internal/mcp/ -run 'TestStaleCredential' -v -count=1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

const (
	// staleSlug is the non-canonical alias the caller addresses the wi by — the
	// form an agent naturally types, and the form /pf-* skills pass through.
	staleSlug = "aihub#427"
	// staleCanonical is the work_items.id the claim echoed back, and therefore
	// the key the real state file is written under.
	staleCanonical = "wi_stale427"

	staleAttempt = "ra_stale427"
	staleSecret  = "s3cr3t-stale427"
	staleEpoch   = 3
)

// writeStaleCanonical writes the post-claim state file exactly as a normal claim
// leaves the directory: keyed by the CANONICAL id, carrying the slug in Slug,
// and with NO slug-keyed file beside it — WriteClaimState removed that stub.
// That shape is the whole point: it is what makes DeleteStateFile(<slug>) a
// no-op rather than a redundant second delete.
func writeStaleCanonical(t *testing.T) string {
	t.Helper()
	if err := config.WriteStateFile(&config.StateFile{
		WIID:          staleCanonical,
		Slug:          staleSlug,
		Project:       "aihub",
		AttemptID:     staleAttempt,
		ClaimEpoch:    staleEpoch,
		SessionSecret: staleSecret,
		Claimed:       true,
	}); err != nil {
		t.Fatalf("write canonical state file: %v", err)
	}
	path := filepath.Join(config.StateDir(), staleCanonical+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("precondition: canonical state file not on disk at %q: %v", path, err)
	}
	// The stub must NOT exist. If it did, a delete keyed on the slug would
	// remove something, and every arm below would stop discriminating.
	if _, err := os.Stat(filepath.Join(config.StateDir(), staleSlug+".json")); !os.IsNotExist(err) {
		t.Fatalf("precondition: a slug-keyed state file exists (err=%v) — this fixture models the "+
			"post-claim directory, where WriteClaimState has already removed the stub", err)
	}
	return path
}

// staleCredentialFake returns a fake aihub whose step endpoint answers the given
// aihub error code, and the wire path that endpoint is registered under.
//
// The path is built the way pkg/client builds it (url.PathEscape on the id) and
// then read back the way net/http hands it to a handler (decoded), so the '#' in
// the slug round-trips. Registering the handler under a path the tool never hits
// would make every arm pass through the fake's 200-OK default and assert nothing.
func staleCredentialFake(t *testing.T, wiID, code, message string) *fakeAihub {
	t.Helper()
	f := newFakeAihub(t)
	f.on("/v1/work_items/"+wiID+"/step", func(map[string]any) (int, any) {
		return 409, map[string]any{"code": code, "message": message}
	})
	return f
}

// staleCredentialCall is one of the two stale-credential exits in
// tools_step.go, reached through the real tool.
type staleCredentialCall struct {
	name string
	site string // the source location this row exists to cover
	args map[string]any
}

// staleCredentialCalls are the two exits that carry the bug. They are separate
// code paths with separate delete statements, so fixing one and not the other is
// a real and easy outcome — the wi's brief says so in as many words, and the M4
// mutant (fix the ordinary branch only) confirms this table is what catches it.
//
// The heartbeat row carries step_id and status even though the heartbeat branch
// returns before reading either: that is what a real heartbeat looks like, since
// the engine adds heartbeat=true to an in_progress call rather than sending a
// bare ping.
func staleCredentialCalls(wiID string) []staleCredentialCall {
	return []staleCredentialCall{
		{
			name: "ordinary",
			site: "internal/mcp/tools_step.go, the ordinary UpdateStep branch",
			args: map[string]any{
				"work_item_id": wiID, "step_id": "execute", "status": "in_progress",
			},
		},
		{
			name: "heartbeat",
			site: "internal/mcp/tools_step.go, the heartbeat branch",
			args: map[string]any{
				"work_item_id": wiID, "step_id": "execute", "status": "in_progress",
				"heartbeat": true,
			},
		},
	}
}

// TestStaleCredentialDeletesTheResolvedStateFile is the primary arm: a
// SLUG-addressed pf_update_step that fails with a stale credential must leave no
// credential on disk, because it told the caller it had deleted one.
//
// Pre-fix this fails on both rows: the tool removes <slug>.json, which does not
// exist, and answers "state file deleted" with wi_stale427.json untouched.
func TestStaleCredentialDeletesTheResolvedStateFile(t *testing.T) {
	for _, tc := range staleCredentialCalls(staleSlug) {
		t.Run(tc.name, func(t *testing.T) {
			sandboxedWorkspace(t)
			canonical := writeStaleCanonical(t)

			f := staleCredentialFake(t, staleSlug, "CONFLICT_EPOCH_MISMATCH", "claim_epoch mismatch")
			result, isErr := callTool(t, f, "pf_update_step", tc.args)

			if !isErr {
				t.Fatalf("%s: pf_update_step succeeded against a 409, want an error: %v", tc.site, result)
			}
			msg := errorText(t, result)

			// Arm selection, asserted rather than assumed. Without this, a
			// classifier change that stopped taking the deleting arm would turn
			// this test into a check that nothing happens — and the file-survives
			// assertion below would then fail for a reason that has nothing to do
			// with aihub#427.
			if !strings.Contains(msg, "STALE_LOCAL_CREDENTIAL") {
				t.Fatalf("%s: took the wrong classifier arm — error was %q, want the stale-credential arm", tc.site, msg)
			}
			// The credential really did reach the server. A refusal that never
			// left the process would exercise none of this.
			if n := len(f.recorded()); n != 1 {
				t.Fatalf("%s: made %d upstream calls, want exactly 1: %v", tc.site, n, f.paths())
			}
			if got := f.recorded()[0].Body["session_secret"]; got != staleSecret {
				t.Fatalf("%s: sent session_secret %v, want the canonical file's %q — "+
					"the fixture is not exercising the resolver", tc.site, got, staleSecret)
			}

			if _, err := os.Stat(canonical); err == nil {
				t.Fatalf("%s: the tool answered %q but %s IS STILL ON DISK.\n"+
					"It deleted by the caller's argument (%q -> %s.json, which does not exist) "+
					"instead of by the resolved state file's own key (%q). The next slug-addressed "+
					"call resolves to this same file and re-sends the same dead credential; the loop "+
					"does not terminate. (aihub#427)",
					tc.site, msg, canonical, staleSlug, staleSlug, staleCanonical)
			} else if !os.IsNotExist(err) {
				t.Fatalf("%s: stat %s: %v", tc.site, canonical, err)
			}
		})
	}
}

// TestStaleCredentialRecoveryTerminatesTheLoop is the recovery arm, and it is
// the assertion the user-visible harm is actually about.
//
// The primary arm above proves a file is gone. This one proves what that buys:
// the SECOND slug-addressed call — the retry an agent makes after reading
// "please re-claim this work item" — must refuse locally instead of resolving to
// the surviving file and shipping the dead credential again. `len(f.recorded())`
// on the second fake is the load-bearing number: pre-fix it is 1 (another 409,
// forever), post-fix it is 0.
//
// The second fake is deliberately configured to answer 200. If the retry reaches
// the server at all, this arm fails on the call count while the tool reports
// success — a shape that would be invisible to any assertion made on the error
// text alone.
func TestStaleCredentialRecoveryTerminatesTheLoop(t *testing.T) {
	for _, tc := range staleCredentialCalls(staleSlug) {
		t.Run(tc.name, func(t *testing.T) {
			sandboxedWorkspace(t)
			writeStaleCanonical(t)

			first := staleCredentialFake(t, staleSlug, "CONFLICT_EPOCH_MISMATCH", "claim_epoch mismatch")
			if _, isErr := callTool(t, first, "pf_update_step", tc.args); !isErr {
				t.Fatalf("%s: first call did not fail", tc.site)
			}

			// A fresh fake with NO error handler: every path answers 200 {"ok":true}.
			second := newFakeAihub(t)
			result, isErr := callTool(t, second, "pf_update_step", tc.args)

			if n := len(second.recorded()); n != 0 {
				t.Fatalf("%s: the retry reached the server %d time(s) (%v) — the credential the caller "+
					"was told had been deleted is still being re-sent. isErr=%v result=%v (aihub#427)",
					tc.site, n, second.paths(), isErr, result)
			}
			if !isErr {
				t.Fatalf("%s: the retry succeeded with no state file on disk: %v", tc.site, result)
			}
			if msg := errorText(t, result); !strings.Contains(msg, "state file") {
				t.Fatalf("%s: retry failed with %q, want a local missing-state-file refusal", tc.site, msg)
			}
		})
	}
}

// TestStaleCredentialCanonicallyAddressedStillDeletes is the control that keeps
// the fix from being read as "delete by sf.WIID INSTEAD OF wiID".
//
// A canonically-addressed call has always worked — wiID and sf.WIID are the same
// string — and it must keep working. Swapping one key for the other would leave
// this green and the primary arm green too, which is why the slug-keyed stub is
// asserted separately below.
func TestStaleCredentialCanonicallyAddressedStillDeletes(t *testing.T) {
	for _, tc := range staleCredentialCalls(staleCanonical) {
		t.Run(tc.name, func(t *testing.T) {
			sandboxedWorkspace(t)
			canonical := writeStaleCanonical(t)

			f := staleCredentialFake(t, staleCanonical, "ATTEMPT_MISMATCH", `attempt status is "superseded"`)
			result, isErr := callTool(t, f, "pf_update_step", tc.args)
			if !isErr {
				t.Fatalf("%s: pf_update_step succeeded against a 409: %v", tc.site, result)
			}
			if _, err := os.Stat(canonical); !os.IsNotExist(err) {
				t.Fatalf("%s: canonically-addressed stale credential left %s on disk (err=%v)",
					tc.site, canonical, err)
			}
		})
	}
}

// TestStaleCredentialAlsoRemovesTheSlugKeyedStub covers the second half of the
// two-key pattern the honest neighbours use, and it is the arm that stops the
// fix from being a one-key swap.
//
// The stub CAN be present: config.WriteClaimState only removes it when the claim
// itself resolved the alias, and aihub#141 exists because a leftover stub is read
// in preference to the canonical file by a filename lookup. Leaving it behind
// after announcing "state file deleted" reintroduces exactly that: the slug-keyed
// stub survives, a later ReadStateFile(slug) finds it, and its empty attempt_id
// draws a 409 CONFLICT_EPOCH_MISMATCH whose real cause is a stale local file.
func TestStaleCredentialAlsoRemovesTheSlugKeyedStub(t *testing.T) {
	for _, tc := range staleCredentialCalls(staleSlug) {
		t.Run(tc.name, func(t *testing.T) {
			sandboxedWorkspace(t)

			// The pre-claim stub: keyed by the SLUG, no attempt_id, not claimed.
			if err := config.WriteStateFile(&config.StateFile{
				WIID:          staleSlug,
				IdemKey:       "idem_stale427",
				SessionSecret: "stub-secret-never-claimed",
				Claimed:       false,
			}); err != nil {
				t.Fatalf("write pre-claim stub: %v", err)
			}
			if err := config.WriteStateFile(&config.StateFile{
				WIID:          staleCanonical,
				Slug:          staleSlug,
				Project:       "aihub",
				AttemptID:     staleAttempt,
				ClaimEpoch:    staleEpoch,
				SessionSecret: staleSecret,
				Claimed:       true,
			}); err != nil {
				t.Fatalf("write canonical state file: %v", err)
			}

			f := staleCredentialFake(t, staleSlug, "CONFLICT_EPOCH_MISMATCH", "claim_epoch mismatch")
			if _, isErr := callTool(t, f, "pf_update_step", tc.args); !isErr {
				t.Fatalf("%s: pf_update_step succeeded against a 409", tc.site)
			}

			for _, key := range []string{staleCanonical, staleSlug} {
				p := filepath.Join(config.StateDir(), key+".json")
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Errorf("%s: %s survived the stale-credential delete (err=%v) — "+
						"the fix must remove BOTH keys, as pf_complete_attempt and pf_wrap do (aihub#141)",
						tc.site, p, err)
				}
			}
		})
	}
}

// TestStaleCredentialKeepsFileOnNonStaleError is the negative control for the
// whole file: the delete must stay bound to the stale-credential arm.
//
// Widening it — deleting on any 409, or on any error — would make every arm above
// pass. aihub#209 chose ATTEMPT_PAUSED as the case that must NOT delete, because
// the user is expected to resume with that credential; aihub#414 then made the
// classification exact so an observed VALUE could not reach the destructive arm.
// A key-selection fix has no business moving either line, and this arm is what
// says so.
func TestStaleCredentialKeepsFileOnNonStaleError(t *testing.T) {
	for _, code := range []string{"ATTEMPT_PAUSED", "CONFLICT_STEP_ATTEMPT_MISMATCH", "INTERNAL_ERROR"} {
		for _, tc := range staleCredentialCalls(staleSlug) {
			t.Run(code+"/"+tc.name, func(t *testing.T) {
				sandboxedWorkspace(t)
				canonical := writeStaleCanonical(t)

				f := staleCredentialFake(t, staleSlug, code, "not a stale credential")
				if _, isErr := callTool(t, f, "pf_update_step", tc.args); !isErr {
					t.Fatalf("%s (%s): pf_update_step succeeded against a 409", code, tc.site)
				}
				if _, err := os.Stat(canonical); err != nil {
					t.Fatalf("%s (%s) deleted the state file: stat %s: %v — only the stale-credential arm may delete",
						code, tc.site, canonical, err)
				}
			})
		}
	}
}
