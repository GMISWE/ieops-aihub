package mcp_test

// DB-FREE invariant tests for aihub#421, flow `state_file_missing` — the
// corpus's single largest error family, and the only one of the four that is
// entirely client-side.
//
// Provenance. docs/audits/aihub-412-corpus-facts/sequence-inventory.md finds 133
// groups (4.83% of 2756) matching "a call fails with STATE_NOT_FOUND /
// NOT_CLAIMED / a missing-state-file client error", hand-validated at 141/141
// precision per that directory's README. Its three printed traces are all from
// transcript d297e63472ef (11, 11 and 6 calls), and every one turns on an
// `emit_event(!CLIENT_UNCLASSIFIED)` — an agent narrating progress on a work item
// it had not claimed. In the first two it sits between work-item updates; in the
// third, between create_work_item and update_work_item.
//
// Two things about this family are NOT what the flow name suggests, and both are
// measured from error-taxonomy.md rather than inferred:
//
//  1. There is no STATE_NOT_FOUND code and no NOT_CLAIMED code. There is no code
//     at all: errResult (internal/mcp/server.go:170) returns a bare string, so
//     the corpus can only file these under `CLIENT_UNCLASSIFIED`. This family is
//     141 calls — the sum of the 19 `read state file` rows in error-taxonomy.md's
//     level 2, and the same 141 the README reports as hand-validated at 141/141
//     precision. It is NOT the 195 / 31.15% of that bucket's level-1 total: the
//     other 54 are unrelated client refusals (`path escapes workspace` 18,
//     `repo is required` 8, `declared_resources is required` 5, git failures and
//     assorted missing-argument errors). Quoting 195 here would inflate the
//     family by 38%. The tests assert on the message text, via result["_raw"],
//     because with no code that is the entire contract.
//
//  2. The wording is not uniform. Three distinct prefixes cover twelve tools:
//
//     read state file for wi <SLUG>: ...            pf_commit 71+1, pf_pr 12, pf_push 2
//     read state file (wi must be claimed first):   pf_emit_event 16 + 16
//     read state file: ...                          pf_complete_attempt, pf_pause_attempt,
//     pf_save_artifact, pf_update_step,
//     pf_update_memory, pf_reinforce_memory,
//     pf_wrap, and the release tools
//
//     (The corpus also records pf_create_dependency in that third row. It is a
//     HISTORICAL member: aihub#324 removed the credential injection, so the tool
//     no longer requires a state file at all. Do not add it to the table below —
//     that row would assert behaviour the tree deliberately dropped.)
//
//     The actionable half — the parenthetical that tells the reader to claim
//     first — is reached by ONE tool out of twelve. The aihub#421 brief assumed
//     it was the family's answer; it is one tool's answer. The table below
//     therefore carries a per-tool expectation and asserts the split as it
//     stands, rather than asserting a uniformity that does not exist or quietly
//     testing only the one tool that would pass.
//
//     ⚠️ Unifying the wording is filed as aihub#428, and that change MUST update
//     the wantPrefix column below. That is not an inconvenience, it is the
//     reason the column exists: a test asserting "some prefix" would have let
//     eleven tools keep the unhelpful wording silently. A separate defect found
//     the same way — pf_update_step deleting the wrong state-file key on a
//     stale credential — is filed as aihub#427; it is not covered here, because
//     it needs a credential that RESOLVES and then fails, which is the opposite
//     precondition to this file's.
//
// Why DB-free. The refusal happens before a single byte leaves the process, so a
// database would add nothing but a skip condition. The load-bearing assertion in
// every arm below is `len(f.recorded()) == 0` — a client-side refusal, not a 401.
// (A state file that EXISTS with a stale secret is the different failure, and it
// is covered against a real server in
// internal/mcp/attempt_terminal_credential_dbgated_test.go.)
//
// ⚠️ Workspace safety. config.StateDir() is
// $POLYFORGE_WORKSPACE_ROOT/.polyforge/state, and POLYFORGE_WORKSPACE_ROOT is
// its ONLY redirect. Unset, config.FindWorkspaceRoot() walks up from the test
// binary's cwd and lands on the LIVE workspace, whose state directory holds
// every claimed work item's session_secret. Every test here goes through
// sandboxedWorkspace, which sets the variable and then verifies StateDir() is
// really inside the temp root before doing anything. Nothing here can reach the
// real workspace or ~/.polyforge.
//
//	go test ./internal/mcp/ -run 'TestStateFileMissing' -v -count=1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// credentialedTool is one tool that injects credentials from the state file, and
// the prefix its own handler puts on the failure. The prefix is per-tool on
// purpose: see note (2) in the file comment.
type credentialedTool struct {
	name       string
	args       map[string]any
	wantPrefix string
}

// credentialedTools is a SAMPLE, not the whole set, and the difference matters
// enough to spell out.
//
// Roughly a dozen tools call config.ResolveStateFile directly. The five below
// are the lifecycle ones — the tools an agent calls while working, which is why
// they are the ones the corpus recorded failing. Also in the bare-prefix group
// and NOT covered here: pf_reinforce_memory (tools_memory.go:147),
// pf_update_memory (:176), pf_save_artifact (:231),
// pf_adopt/close/ignore_artifact (shared emitArtifactAction, :487),
// pf_cut_alpha (tools_release.go:39) and pf_promote (:84). They share the
// message verbatim because they share the wording, not because anything enforces
// it — there is no shared helper, no flag and no list; the predicate is literally
// "the handler body calls ResolveStateFile". That is what aihub#428 is for.
//
// The four worktree tools (pf_diff/pf_commit/pf_push/pf_pr) reach it indirectly
// through coding.WorktreePath and carry the THIRD prefix,
// `read state file for wi <id>: ` (internal/coding/scenario.go:39), which is the
// commonest one in the corpus. ⚠️ That prefix is asserted NOWHERE in the tree,
// including here: the nearest test,
// TestCommitGateWire_DeletingTheStateFileIsNotABypass
// (commit_gate_wire_test.go:412), checks only
// `strings.Contains(raw, "state file")`, which all three prefixes satisfy.
// Covering it needs a real git worktree, so it belongs with the git-dependent
// tests rather than in this DB-free, git-free file — a known gap, recorded
// rather than papered over.
func credentialedTools(wiID string) []credentialedTool {
	return []credentialedTool{
		{"pf_update_step", map[string]any{
			"work_item_id": wiID, "step_id": "execute", "status": "in_progress",
		}, "read state file: "},
		{"pf_complete_attempt", map[string]any{
			"work_item_id": wiID, "status": "wrapped",
		}, "read state file: "},
		{"pf_pause_attempt", map[string]any{
			"work_item_id": wiID,
		}, "read state file: "},
		{"pf_acquire_locks", map[string]any{
			"work_item_id": wiID,
		}, "read state file: "},
		// The one tool whose wording tells the reader what to do about it.
		{"pf_emit_event", map[string]any{
			"work_item_id": wiID, "event_type": "note",
			"payload": map[string]any{"text": "aihub#421 probe"},
		}, "read state file (wi must be claimed first): "},
	}
}

// TestStateFileMissing_RefusesBeforeAnyUpstreamCall is the refusal arm: the 133
// corpus groups' failure, reproduced for five tools, with the assertion that
// nothing was attempted rather than attempted-and-rejected.
//
// `len(f.recorded()) == 0` is the assertion that matters most and the one
// easiest to leave out. A handler that sent empty credentials upstream and let
// the server answer 401 would produce a nearly identical-looking error string
// while having a completely different failure surface: it would burn a
// round-trip, log an auth failure against the caller's API key on every
// narration attempt, and — for a work item some OTHER session holds — hand the
// server a request it has to reject rather than one it never sees.
func TestStateFileMissing_RefusesBeforeAnyUpstreamCall(t *testing.T) {
	const wiID = "wi_aihub421missing"

	for _, tc := range credentialedTools(wiID) {
		t.Run(tc.name, func(t *testing.T) {
			sandboxedWorkspace(t)
			// Deliberately write nothing at all: the state directory does not
			// even exist yet, which is the state a never-claimed work item is
			// really in.
			if _, err := os.Stat(config.StateDir()); !os.IsNotExist(err) {
				t.Fatalf("precondition: state dir %q already exists (err=%v)", config.StateDir(), err)
			}

			f := newFakeAihub(t)
			result, isErr := callTool(t, f, tc.name, tc.args)

			if !isErr {
				t.Fatalf("%s succeeded with no state file: %v", tc.name, result)
			}
			msg := errorText(t, result)

			if !strings.HasPrefix(msg, tc.wantPrefix) {
				t.Errorf("message = %q\nwant prefix %q\n(the prefix is this handler's own wrapping; "+
					"it is NOT shared across tools — only pf_emit_event says \"wi must be claimed first\")",
					msg, tc.wantPrefix)
			}
			if want := "state file not found for " + wiID; !strings.Contains(msg, want) {
				t.Errorf("message = %q, want it to contain %q — config.ReadStateFile's wording, which "+
					"is what names the work item the caller got wrong", msg, want)
			}
			if !strings.Contains(msg, "no such file or directory") {
				t.Errorf("message = %q, want the underlying fs.PathError to survive: it carries the "+
					"PATH, which is how an operator finds out their workspace root is wrong", msg)
			}

			if n := len(f.recorded()); n != 0 {
				t.Errorf("%s made %d upstream call(s), want 0 — the refusal must be client-side. "+
					"paths=%v. Sending empty credentials and letting the server answer 401 would look "+
					"the same in a transcript and is not the same thing", tc.name, n, f.paths())
			}

			// A refusal must not fabricate the state it could not read.
			if entries, err := os.ReadDir(config.StateDir()); err == nil && len(entries) > 0 {
				var names []string
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Errorf("%s created %v in %q; a tool that cannot find a claim must not invent one — "+
					"a stub with no attempt_id would make ResolveStateFile's own fallback report a "+
					"different, more confusing error on the next call", tc.name, names, config.StateDir())
			}
		})
	}
}

// TestStateFileMissing_LeavesANeighbourStateFileIntact is the isolation arm.
//
// State files are one per work item, keyed by the raw StateFile.WIID with
// ".json" appended and no sanitization (config/state.go:72), and
// ResolveStateFile's fallback SCANS the whole directory looking for a file whose
// Slug matches (state.go:126-132). So a failed lookup for work item B reads
// every neighbouring file, including A's. Reading is fine; the invariant is that
// it stays a read.
//
// This matters because the recovery story for a missing state file is "claim it
// again", and an agent hitting this error 133 times across a corpus is an agent
// that will call these tools repeatedly against ids it does not hold. If any one
// of those attempts truncated or rewrote the file for a work item it happened to
// scan past, the blast radius would be another session's live credential.
func TestStateFileMissing_LeavesANeighbourStateFileIntact(t *testing.T) {
	const (
		neighbourID = "wi_aihub421neighbour"
		absentID    = "wi_aihub421absent"
	)

	for _, tc := range credentialedTools(absentID) {
		t.Run(tc.name, func(t *testing.T) {
			sandboxedWorkspace(t)

			// A healthy, claimed neighbour, with a slug so the resolver's scan
			// really does parse it rather than skipping on a decode error.
			if err := config.WriteStateFile(&config.StateFile{
				WIID:          neighbourID,
				Slug:          "aihub#999",
				Project:       "aihub",
				AttemptID:     "ra_neighbour",
				ClaimEpoch:    7,
				SessionSecret: "aihub421-neighbour-secret-do-not-touch",
				Claimed:       true,
			}); err != nil {
				t.Fatalf("seed neighbour state file: %v", err)
			}
			path := filepath.Join(config.StateDir(), neighbourID+".json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read seeded neighbour: %v", err)
			}

			f := newFakeAihub(t)
			if _, isErr := callTool(t, f, tc.name, tc.args); !isErr {
				t.Fatalf("%s succeeded for an unclaimed work item", tc.name)
			}

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%s left the neighbour's state file unreadable: %v — a failed lookup for one "+
					"work item must not touch another's credentials", tc.name, err)
			}
			if string(before) != string(after) {
				t.Errorf("%s modified %q.\nbefore: %s\nafter:  %s", tc.name, path, before, after)
			}

			// And it must not have resolved ONTO the neighbour: a scan that
			// matched too loosely would send the neighbour's live secret
			// upstream under the absent work item's id.
			for _, c := range f.recorded() {
				t.Errorf("%s reached aihub at %s %s with body %v — the neighbour's credential must "+
					"never be borrowed for a work item that was never claimed", tc.name, c.Method, c.Path, c.Body)
			}
		})
	}
}

// TestStateFileMissing_ClaimedStateFileMakesTheSameCallWork is the recovery arm,
// and the control that stops all of the above from passing vacuously.
//
// Every assertion in this file is of the form "X did not happen". A handler that
// had been broken into never calling aihub at all — or a fake whose recorder was
// wired up wrong — would satisfy every one of them. This arm writes the state
// file the earlier arms withheld and requires the same tool invocation — same
// table, same arguments, a different work_item_id — to reach the server. It is
// the positive control for those negative assertions, and without it a green run
// here proves nothing.
//
// ⚠️ Its scope is exactly the `len(recorded()) == 0` claim. It does NOT control
// the neighbour byte-comparison in the arm above; nothing here makes that
// comparison capable of failing. The evidence that it can is the mutation probe
// recorded in this work item's report — making ResolveStateFile's scan borrow any
// claimed neighbour reddens those six subtests and nothing else.
func TestStateFileMissing_ClaimedStateFileMakesTheSameCallWork(t *testing.T) {
	const wiID = "wi_aihub421recovered"

	for _, tc := range credentialedTools(wiID) {
		t.Run(tc.name, func(t *testing.T) {
			sandboxedWorkspace(t)
			if err := config.WriteStateFile(&config.StateFile{
				WIID:          wiID,
				Slug:          "aihub#421",
				Project:       "aihub",
				AttemptID:     "ra_recovered",
				ClaimEpoch:    3,
				SessionSecret: "aihub421-recovered-secret",
				Claimed:       true,
			}); err != nil {
				t.Fatalf("write state file: %v", err)
			}

			f := newFakeAihub(t)
			if _, isErr := callTool(t, f, tc.name, tc.args); isErr {
				// Not fatal on its own: the fake answers {"ok":true} for every
				// path, so a tool that needs a specific response shape can
				// still report an error. The recorded call is the assertion.
				t.Logf("%s returned an error against the fake aihub (the fake answers a generic "+
					"{\"ok\":true}); the recorded request below is what this arm is about", tc.name)
			}

			recorded := f.recorded()
			if len(recorded) == 0 {
				t.Fatalf("%s made no upstream call even WITH a state file. Every other assertion in "+
					"this file is a negative, so they would all pass on a handler that never calls "+
					"out — this arm exists to prove they can fail", tc.name)
			}
			got := recorded[0]
			if secret, ok := got.Body["session_secret"].(string); !ok || secret != "aihub421-recovered-secret" {
				t.Errorf("%s sent session_secret=%v, want the value from the state file — the point of "+
					"the recovery path is that the claim's credential reaches the wire",
					tc.name, got.Body["session_secret"])
			}
			if attempt, ok := got.Body["attempt_id"].(string); !ok || attempt != "ra_recovered" {
				t.Errorf("%s sent attempt_id=%v, want ra_recovered", tc.name, got.Body["attempt_id"])
			}
		})
	}
}
