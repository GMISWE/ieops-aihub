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
//  2. The wording WAS not uniform. aihub#428 unified it; the paragraph below is
//     kept in the past tense because the per-tool table it explains is gone, and
//     a reader who finds only the answer cannot tell whether the split was
//     considered or never noticed. Three distinct prefixes used to cover twelve
//     tools:
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
//     first — was reached by ONE tool out of twelve. The aihub#421 brief assumed
//     it was the family's answer; it was one tool's answer. The table therefore
//     carried a per-tool expectation and asserted the split as it stood, rather
//     than asserting a uniformity that did not exist or quietly testing only the
//     one tool that would pass.
//
//     THAT IS WHAT MADE THE FIX POSSIBLE TO LAND SAFELY, and it is the argument
//     for writing tables this way. Because the column pinned the real split
//     per-tool, aihub#428 could not unify the wording without every one of these
//     five rows going red and being looked at — including pf_emit_event, the one
//     tool that was already right and whose wording still had to change to reach
//     the shared minting point. A test that had asserted "some prefix" would have
//     stayed green through both the defect and the fix, and told nobody anything
//     about either.
//
//     ⚠️ RESOLVED. Unifying the wording was aihub#428, and it did update the
//     column — by replacing it with wantStateFileRefusalPrefix, since the split
//     the column described no longer exists. Every credentialed tool now mints
//     this refusal through config.StateFileMissingErr, which lives in
//     internal/config because that is the only package both internal/mcp and
//     internal/coding can import (internal/mcp imports internal/coding, so the
//     reverse is a cycle) — and the commonest prefix of the three was the one in
//     internal/coding. A separate defect found the same way — pf_update_step
//     deleting the wrong state-file key on a stale credential — was aihub#427,
//     fixed and merged; it is not covered here, because it needs a credential
//     that RESOLVES and then fails, which is the opposite precondition to this
//     file's.
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
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/config"
)

// credentialedTool is one tool that injects credentials from the state file, and
// the prefix its own handler puts on the failure. The prefix is per-tool on
// purpose: see note (2) in the file comment.
type credentialedTool struct {
	name string
	args map[string]any
}

// wantStateFileRefusalPrefix is the ONE prefix every credentialed tool now emits,
// minted by config.StateFileMissingErr.
//
// ⚠️ THIS USED TO BE A PER-TOOL `wantPrefix` FIELD ON THE STRUCT ABOVE, and its
// removal is aihub#428 rather than a weakening of this test. The column existed
// because the wording was genuinely per-tool — three prefixes across twelve tools
// — and the aihub#421 header said in as many words that a unification MUST move
// it: "a test asserting 'some prefix' would have let eleven tools keep the
// unhelpful wording silently."
//
// That is still true, and it is why the assertion did not become "some prefix".
// It became a SHARED one: every tool is checked against this single constant, so
// a tool that re-diverges fails on the tool that diverged rather than on a column
// somebody would have had to remember to update. Keeping a per-tool field holding
// twelve identical values would have implied a per-tool decision that is no longer
// being made — the decision now lives in exactly one function.
const wantStateFileRefusalPrefix = "STATE_FILE_MISSING: "

// credentialedTools is a SAMPLE, not the whole set, and the difference matters
// enough to spell out.
//
// Roughly a dozen tools call config.ResolveStateFile directly. The five below
// are the lifecycle ones — the tools an agent calls while working, which is why
// they are the ones the corpus recorded failing. Also in the bare-prefix group
// and NOT covered here: pf_reinforce_memory, pf_update_memory and
// pf_save_artifact, the three config.ResolveStateFile call sites in
// tools_memory.go. They are named by tool rather than by line: this sentence
// used to cite tools_memory.go:147/:176/:231 and every one of those numbers was
// already five lines off the call it pointed at, while the fourth citation
// (:487, for emitArtifactAction) was off by 163. A line number in a comment is
// a claim nothing checks — TestStateRefusalCallSitesAreAccountedFor counts the
// sites per file and is the arm that actually holds.
//
// ⚠️ Five tools this paragraph used to name are gone rather than merely
// uncovered: pf_cut_alpha and pf_promote were unpublished by aihub#448 and
// tools_release.go was deleted, and pf_adopt/close/ignore_artifact were retired
// by aihub#446 along with the emitArtifactAction helper their one shared call
// site lived in. Do not go looking for any of them. They are named here because
// a reader comparing this paragraph against the aihub#412 corpus will find them
// in the corpus — it records calls made while the tools still existed.
//
// The sentence that used to close this paragraph — "they share the message
// verbatim because they share the wording, not because anything enforces it;
// there is no shared helper, no flag and no list" — was true when it was written
// and is now FIXED rather than merely stale: aihub#428 made
// config.StateFileMissingErr the single minting point and added the two gates
// below, so the wording is enforced and the list exists.
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
		}},
		{"pf_complete_attempt", map[string]any{
			"work_item_id": wiID, "status": "wrapped",
		}},
		{"pf_pause_attempt", map[string]any{
			"work_item_id": wiID,
		}},
		{"pf_acquire_locks", map[string]any{
			"work_item_id": wiID,
		}},
		// The one tool whose wording tells the reader what to do about it.
		{"pf_emit_event", map[string]any{
			"work_item_id": wiID, "event_type": "note",
			"payload": map[string]any{"text": "aihub#421 probe"},
		}},
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

			// aihub#428: one prefix, and it carries a CODE. The family used to
			// file under CLIENT_UNCLASSIFIED (31.15% of all recorded failures)
			// precisely because errResult emits a bare string with nothing to
			// classify on; this prefix is what makes the largest error family
			// countable.
			if !strings.HasPrefix(msg, wantStateFileRefusalPrefix) {
				t.Errorf("message = %q\nwant prefix %q — every credentialed tool mints this refusal "+
					"through config.StateFileMissingErr, so a tool with its own wording has gone "+
					"around the single minting point (aihub#428)", msg, wantStateFileRefusalPrefix)
			}
			// The ACTIONABLE half, which is the whole point of the wi: before
			// aihub#428 exactly one tool out of twelve told the reader what to do,
			// and it was not the common one (pf_commit alone is 71 corpus calls
			// and said only "read state file for wi X").
			if !strings.Contains(msg, "/pf-work") {
				t.Errorf("message = %q, want it to name the recovery command. A caller that simply "+
					"has not claimed the work item is told what failed and nothing about what to do "+
					"— which is the state eleven of the twelve tools were in (aihub#428)", msg)
			}
			if !strings.Contains(msg, "no local credential for wi "+wiID) {
				t.Errorf("message = %q, want it to name the work item — the one advantage the "+
					"coding.WorktreePath prefix had, which the unified wording had to keep", msg)
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

// ─── aihub#428: the registry the family never had ───────────────────────────

// TestStateFileRefusalHasExactlyOneMintingPoint is the mechanism, and it exists
// because the aihub#428 brief's sharpest observation was that there wasn't one:
//
//	"There is also no mechanism behind the set: no flag on the tool definition,
//	 no wrapper, no list. The predicate is literally 'the handler body calls
//	 config.ResolveStateFile'."
//
// That is how one message became three. Unifying the wording without adding a
// mechanism would fix the symptom and leave the cause, and the next handler
// added would drift again — silently, because nothing would be watching.
//
// The invariant is deliberately NOT "every ResolveStateFile caller mints this
// refusal". That statement is FALSE, and asserting it would have forced a wrong
// change: see stateRefusalExemptSites below.
//
// ⚠️ aihub#454 REPLACED THE MECHANISM, and the one it replaced is worth naming
// because its failure mode is the very thing this file spends the most words
// warning about. Until this change the gate was a line scan for the literal
// `Errorf("read state file` — the prefix the three OLD voices happened to
// share. So it caught a REVERSION and nothing else. A newly added credentialed
// tool inventing a fourth voice from scratch, say
// `fmt.Errorf("could not load credentials: %w", err)`, tripped neither this gate
// (wrong words) nor the inventory below (a tool that never used the helper
// subtracts nothing from a count of helper calls). The gate's failure message
// said "exactly one minting point"; what it actually measured was "yesterday's
// wording is gone". A gate that only recognises yesterday's wording cannot stop
// tomorrow's drift — this file said so, in the paragraph that used to sit above
// stateRefusalCallSites, and then shipped the gate anyway.
//
// The fix is to assert the PROPERTY rather than the spelling, and the property
// is REACHABILITY. Membership in this family was never about wording: it is "the
// handler body calls config.ResolveStateFile", the aihub#428 brief's own
// predicate. So the gate now parses every production file, finds each
// config.ResolveStateFile call, and classifies what its failure path does with
// the error. There are exactly three legal answers — route it through
// config.StateFileMissingErr, mint nothing caller-facing at all, or be a listed
// exemption. Anything else is a second minting point no matter which words it
// chooses, including words nobody has thought of yet.
func TestStateFileRefusalHasExactlyOneMintingPoint(t *testing.T) {
	sites := scanStateRefusalSites(t)

	byKind := map[stateRefusalKind]int{}
	exemptSeen := map[string]bool{}
	for _, s := range sites {
		byKind[s.Kind]++
		switch s.Kind {
		case refusalViaHelper, refusalSilent:
			// The two legal answers. A silent site is not a member of the
			// family — see stateRefusalExemptSites for why converting one
			// would be a regression rather than a completion.
		case refusalHandRolled:
			ex, exempt := stateRefusalExemptSites[s.Key()]
			if !exempt {
				t.Errorf(`%s:%d builds a state-file refusal by hand instead of calling config.StateFileMissingErr:
    %s
That is how this family came to speak with three different voices across twelve tools, only one of
which told the reader what to do. Route it through the helper, or — if this site is asking a
DIFFERENT question rather than the same one worded differently — add it to stateRefusalExemptSites
with the reason. (aihub#428, gate widened by aihub#454)`, s.File, s.Line, s.Src)
				continue
			}
			exemptSeen[s.Key()] = true
			if ex.Kind != refusalHandRolled {
				t.Errorf("%s:%d is exempt as %q but the detector reads it as %q; the exemption's reason "+
					"describes a site that no longer exists", s.File, s.Line, ex.Kind, s.Kind)
			}
		default:
			t.Errorf(`%s:%d: the detector does not understand this config.ResolveStateFile shape:
    %s
This is a FAILURE, not a pass. A scanner that cannot read a site is silent about it in exactly the
same way it is silent about a clean one, and that indistinguishability is what made the previous
version of this gate useless. Teach findStateRefusalSites the shape (and add a fixture for it) or
rewrite the site into one of the recognised ones.`, s.File, s.Line, s.Src)
		}
	}

	// ── Anti-vacuity. Every assertion above is a negative, so a detector that
	//    parsed nothing, or one whose classifier stopped matching, exits exactly
	//    like a clean tree.
	if len(sites) < 12 {
		t.Fatalf("found only %d config.ResolveStateFile call site(s) in production — the walk or the "+
			"detector is broken, not the tree, and every assertion above was vacuously satisfied", len(sites))
	}
	for kind, want := range map[stateRefusalKind]int{refusalViaHelper: 1, refusalSilent: 1, refusalHandRolled: 1} {
		if byKind[kind] < want {
			t.Errorf("no site classified %q. All three classifications are populated in this tree, so a "+
				"zero means the classifier lost a branch rather than that the tree changed", kind)
		}
	}
	for key, ex := range stateRefusalExemptSites {
		if ex.Kind == refusalHandRolled && !exemptSeen[key] {
			t.Errorf("exemption %q matched no hand-rolled site. Either the detector stopped finding that "+
				"shape — in which case it would also stop finding new violations and this gate is dead — "+
				"or the site is gone and this entry should go with it, which is the reviewable record "+
				"that the exemption is no longer needed", key)
		}
	}
}

// stateRefusalCallSites is the production inventory of the family: which files
// mint this refusal, and how many times each.
//
// ⚠️ THIS IS A DIFFERENT AXIS FROM THE GATE ABOVE, and the division of labour is
// worth naming so neither is mistaken for the other's backup.
//
// The gate asserts that no SECOND minting point exists: every failure path is
// the helper, nothing, or a listed exemption. What it cannot see is a site that
// stops refusing altogether — converting `return errResult(...)` into a bare
// `return` is "silent", which the gate calls legal, and for a best-effort lookup
// it IS legal. Whether a given tool should refuse or should shrug is a decision
// about that tool, not a property of the code, so it has to be written down.
//
// This inventory is where it is written down: it counts CALLS TO THE HELPER, so
// a credentialed tool that quietly stops making one fails here even though the
// gate stays green.
//
// Keeping the numbers current is the cost, and it is deliberate — the same
// ratchet the repo uses elsewhere. Adding a credentialed tool means adding a
// count here, which is one line and a moment's thought about whether the new tool
// really is a member of this family.
//
// ✅ aihub#448 was the first exercise of that, and it went the way this comment
// used to hope: it predicted that unpublishing pf_cut_alpha and pf_promote would
// take out tools_release.go's WHOLE entry and that the change must DELETE the row
// rather than edit it. That is exactly what happened — the file no longer exists,
// so the row is gone rather than lowered to zero. A zero would have been the
// wrong shape: it reads as "this file mints the refusal nowhere", which invites
// re-adding a site, where absence reads as "this file is not part of the family",
// which is the truth.
var stateRefusalCallSites = map[string]int{
	"internal/coding/scenario.go":     1,
	"internal/mcp/tools_coding.go":    2,
	"internal/mcp/tools_events.go":    1,
	"internal/mcp/tools_lifecycle.go": 3,
	"internal/mcp/tools_memory.go":    3,
	"internal/mcp/tools_step.go":      1,
}

// TestStateRefusalCallSitesAreAccountedFor pins the inventory above against the
// tree, in BOTH directions: a file that stops minting the refusal is as much a
// finding as one that starts.
func TestStateRefusalCallSitesAreAccountedFor(t *testing.T) {
	root := moduleRoot(t)
	found := map[string]int{}

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		n := 0
		for _, line := range strings.Split(string(b), "\n") {
			// The declaration is not a call site.
			if strings.Contains(line, "func StateFileMissingErr(") {
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			n += strings.Count(line, "StateFileMissingErr(")
		}
		if n > 0 {
			found[filepath.ToSlash(rel)] = n
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	for file, want := range stateRefusalCallSites {
		if got := found[file]; got != want {
			t.Errorf("%s mints the refusal %d time(s), inventory says %d. If a credentialed tool "+
				"was added or removed, update stateRefusalCallSites in the same change — a site that "+
				"silently stops using config.StateFileMissingErr is how the three prefixes happened "+
				"the first time (aihub#428)", file, got, want)
		}
	}
	for file, got := range found {
		if _, listed := stateRefusalCallSites[file]; !listed {
			t.Errorf("%s mints the refusal %d time(s) but is not in stateRefusalCallSites. Add it, "+
				"so the inventory keeps describing the whole family rather than the part somebody "+
				"remembered", file, got)
		}
	}

	total := 0
	for _, n := range found {
		total += n
	}
	if total == 0 {
		t.Fatal("found no call sites at all — the scan is broken, not the tree; every assertion " +
			"above would be vacuously satisfied")
	}
}

// stateRefusalExemption is one sanctioned answer that is not the helper, with
// the reason it is a different question rather than the same question worded
// differently.
type stateRefusalExemption struct {
	// Kind is the verdict findStateRefusalSites must return for this site.
	// Asserting it turns the reason below from prose into something the tree is
	// checked against: an exemption saying "swallows the error and returns
	// early" is FALSE the moment that site starts minting a message, and the
	// entry then reads as cover for the thing it was never meant to cover.
	Kind   stateRefusalKind
	Reason string
}

// stateRefusalExemptSites are the config.ResolveStateFile call sites that must
// NOT be converted.
//
// There are 15 such call sites in production and 11 are members of this family.
// Writing that down is the point: "unify the wording" is exactly the kind of
// instruction that gets over-applied, and the second entry below is one an
// over-eager unification would visibly damage.
//
// ⚠️ Those two numbers read 18 and 14 until aihub#454 measured them, then 16 and
// 12 until aihub#446 retired pf_adopt/close/ignore_artifact — the three tools
// shared ONE config.ResolveStateFile call (emitArtifactAction), so both numbers
// moved by one, not three. aihub#448 (#374, 2026-09-08) had deleted
// tools_release.go's two calls the same way, and back then the sentence did not
// move at all — the same defect as the gate above, one layer down, surviving
// because nothing was checking the sentence. They are now derived from the
// detector by TestStateRefusalExemptionsStillSayWhatTheyMean rather than
// remembered.
//
// The key is `<file> <enclosing func>(<argument>)`, which is what
// stateRefusalSite.Key reports. It deliberately carries no line number: an
// exemption keyed on a line rots on the next edit made above it, and a rotted
// exemption fails open.
var stateRefusalExemptSites = map[string]stateRefusalExemption{
	"internal/mcp/tools_coding.go (*Server).emitCodingEvent(wiID)": {
		Kind: refusalSilent,
		Reason: "best-effort lookup; swallows the error and returns early rather than refusing, so " +
			"there is no caller-facing message at all.",
	},
	"internal/mcp/tools_coding.go (*commitLockGate).run(g.wiID)": {
		Kind: refusalHandRolled,
		Reason: "discloses a SIDE EFFECT — \"so nothing was committed\" — which the generic refusal " +
			"does not and must not claim. Replacing it would delete the one sentence telling the " +
			"caller the index was left alone.",
	},
	"internal/mcp/tools_lifecycle.go (*Server).registerLifecycleTools(canonicalWIID)": {
		Kind: refusalSilent,
		Reason: "prior-worktree read: best-effort; a miss is normal and the error is deliberately " +
			"ignored in the condition itself.",
	},
	"internal/mcp/tools_lifecycle.go recordedClaimSecret(wiID)": {
		Kind:   refusalSilent,
		Reason: "worktree lookup; returns (\"\", false) — a bool, not a message.",
	},
}

// TestStateRefusalExemptionsStillSayWhatTheyMean is the NEGATIVE control for the
// gate above, and the reason the exemption set is a test rather than a comment.
//
// The gate only rejects hand-rolled refusals, so it is satisfied by converting
// EVERYTHING — including the commit lock gate, whose message exists to say that
// nothing was committed. A caller who is told only "no local credential, claim it
// first" has lost the answer to the question they actually have at that moment,
// which is whether their commit landed. Over-unification is the failure mode this
// wi could plausibly produce, and the gate alone cannot see it.
//
// ⚠️ aihub#454: the entries are now checked against the tree instead of only
// being read, and that found the first thing a checked exemption finds. The key
// `internal/mcp/tools_coding.go worktreePathFor` named a function that has never
// existed in this repo: git shows the string was introduced by aihub#428 (#373)
// in this file and appears nowhere else, so it matched nothing on the day it was
// written. The site it describes is (*Server).emitCodingEvent. An exemption
// pointing at nothing is worse than no exemption — it reads as a considered
// decision about a site nobody can find, which is the shape of a decision that
// was never actually made.
func TestStateRefusalExemptionsStillSayWhatTheyMean(t *testing.T) {
	if len(stateRefusalExemptSites) != 4 {
		t.Fatalf("the exemption set has %d entries, expected 4 — if a call site was added or "+
			"removed, say which and why here rather than letting the count drift",
			len(stateRefusalExemptSites))
	}
	// The reasons are the payload, not decoration: an exemption list whose
	// entries carry none is just a list of things somebody decided not to touch,
	// and the next reader cannot tell a deliberate exemption from an oversight.
	// Asserting them keeps a future edit from emptying one to silence a failure.
	for site, ex := range stateRefusalExemptSites {
		if len(strings.TrimSpace(ex.Reason)) < 40 {
			t.Errorf("exemption %q carries no real reason (%q). Say why this site is a DIFFERENT "+
				"question rather than the same one worded differently", site, ex.Reason)
		}
	}

	// Each exemption must name a site that EXISTS and that the detector reads
	// the way the reason describes. Without this the four entries are four
	// claims about the tree with nothing checking any of them, which is how the
	// worktreePathFor key survived.
	sites := scanStateRefusalSites(t)
	byKey := map[string]stateRefusalSite{}
	for _, s := range sites {
		byKey[s.Key()] = s
	}
	for key, ex := range stateRefusalExemptSites {
		got, ok := byKey[key]
		if !ok {
			t.Errorf("exemption %q names a call site the detector cannot find. Either the site moved "+
				"— in which case fix the key — or it is gone and the entry should go with it", key)
			continue
		}
		if got.Kind != ex.Kind {
			t.Errorf("exemption %q is documented as %q but the tree does %q at %s:%d:\n    %s\nThe reason "+
				"on file describes behaviour this site no longer has", key, ex.Kind, got.Kind, got.File, got.Line, got.Src)
		}
	}

	// The two counts the comment above quotes, derived rather than remembered.
	members := 0
	for _, s := range sites {
		if s.Kind == refusalViaHelper {
			members++
		}
	}
	if len(sites) != 15 || members != 11 {
		t.Errorf("stateRefusalExemptSites' comment says 15 call sites of which 11 are members; the tree "+
			"has %d and %d. Update the sentence in the same change — the pair before this one (16 and "+
			"12) held only until aihub#446 retired the three artifact-action tools, and the pair before "+
			"that (18 and 14) went stale the moment aihub#448 deleted tools_release.go's two sites and "+
			"stayed that way because nothing checked it", len(sites), members)
	}

	b, err := os.ReadFile(filepath.Join(moduleRoot(t), "internal/mcp/tools_coding.go"))
	if err != nil {
		t.Fatalf("read tools_coding.go: %v", err)
	}
	src := string(b)

	// The disclosure that must survive the unification, quoted from the source it
	// protects rather than described.
	const disclosure = "so nothing was committed"
	if !strings.Contains(src, disclosure) {
		t.Errorf("the commit-time lock gate no longer says %q. That message is EXEMPT from "+
			"aihub#428's unification: the generic refusal says a credential is missing, which is "+
			"true, and says nothing about whether the commit landed, which is what the caller needs "+
			"at that moment. Unifying it is a regression, not a completion.", disclosure)
	}
	// And it must still be a refusal about the same underlying failure, so the
	// assertion above cannot be satisfied by an unrelated sentence.
	if !strings.Contains(src, "could not read this work item's attempt") {
		t.Error("the commit-gate disclosure no longer names the credential read it is reporting on")
	}
}

// ─── The detector, extracted (aihub#454) ─────────────────────────────────────

// stateRefusalKind is what one config.ResolveStateFile call's failure path does
// with the error it gets back.
type stateRefusalKind string

const (
	// refusalViaHelper routes it through config.StateFileMissingErr, the single
	// minting point. Wrapping the helper's result still counts — the sentence
	// the caller needs survives a %w.
	refusalViaHelper stateRefusalKind = "helper"

	// refusalSilent mints nothing caller-facing: a bare return, a zero value, a
	// bool. These are NOT members of the family and converting one would be a
	// regression; see stateRefusalExemptSites.
	refusalSilent stateRefusalKind = "silent"

	// refusalHandRolled builds its own refusal — a second minting point, which
	// is the thing the gate exists to stop, whatever words it uses.
	refusalHandRolled stateRefusalKind = "hand-rolled"

	// refusalUnrecognised means the DETECTOR could not read the shape. It fails
	// the gate rather than passing it, because a scanner's silence about code it
	// cannot parse is indistinguishable from its silence about clean code, and
	// treating the two the same is how the literal scan this replaced stayed
	// green through the hole it had.
	refusalUnrecognised stateRefusalKind = "unrecognised"
)

// stateRefusalSite is one config.ResolveStateFile call and the verdict on it.
type stateRefusalSite struct {
	File string
	Line int
	Func string // enclosing function, receiver included
	Arg  string // the argument source, which disambiguates sites sharing a function
	Kind stateRefusalKind
	Src  string // the offending or classifying expression, for the failure message
}

// Key is the identity stateRefusalExemptSites is keyed on: enough to name one
// site among several in the same function, and deliberately free of line
// numbers, which rot on the next edit above them.
func (s stateRefusalSite) Key() string { return s.File + " " + s.Func + "(" + s.Arg + ")" }

// stateRefusalErrorMinters are the calls that manufacture a caller-facing error.
// The list is explicit rather than heuristic because a heuristic that guessed
// would fail open, and this gate's whole subject is a gate that failed open.
var stateRefusalErrorMinters = map[string]bool{
	"fmt.Errorf":        true,
	"fmt.Sprintf":       true,
	"errors.New":        true,
	"errors.Join":       true,
	"domain.NewErr":     true,
	"echo.NewHTTPError": true,
}

// errResult is internal/mcp's universal refusal channel (server.go). A branch
// that hands it anything other than the helper's result is minting a refusal
// even when it writes no words of its own — `errResult(err)` ships the raw
// os.PathError, which is a fourth voice with nobody's name on it.
const stateRefusalChannel = "errResult"

// scanStateRefusalSites runs the detector over every production file in the
// module and returns every site it found, sorted for stable output.
func scanStateRefusalSites(t *testing.T) []stateRefusalSite {
	t.Helper()
	root := moduleRoot(t)
	var all []stateRefusalSite

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if !strings.Contains(string(b), "ResolveStateFile(") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		found, detErr := findStateRefusalSites(b, filepath.ToSlash(rel))
		if detErr != nil {
			return detErr
		}
		all = append(all, found...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].File != all[j].File {
			return all[i].File < all[j].File
		}
		return all[i].Line < all[j].Line
	})
	return all
}

// findStateRefusalSites is THE detector — the single copy, called by the repo
// gate above and by the fixture self-tests below.
//
// 🔴 It is a free function taking source bytes, for the reason
// error_code_classification_test.go states about its own detector: a self-test
// that re-implements the walk measures a COPY, and the copy can keep passing
// while the detector the gate actually runs goes blind. The only way to exercise
// the previous version of this gate was to put a violation into a real
// production file, so its hole could not be covered by a fixture at all.
//
// Recognised shapes, which are all the ones the tree uses:
//
//	sf, err := config.ResolveStateFile(x)   // followed by an `if` on err
//	if err != nil { ... }
//
//	if v, e := config.ResolveStateFile(x); e == nil && ... { ... }  // error handled inline
//
// Anything else is reported as refusalUnrecognised on purpose.
func findStateRefusalSites(src []byte, name string) ([]stateRefusalSite, error) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}

	text := func(n ast.Node) string {
		if n == nil {
			return ""
		}
		lo, hi := fset.Position(n.Pos()).Offset, fset.Position(n.End()).Offset
		if lo < 0 || hi > len(src) || lo > hi {
			return ""
		}
		return strings.Join(strings.Fields(string(src[lo:hi])), " ")
	}

	var out []stateRefusalSite
	seen := map[token.Pos]bool{}
	record := func(call *ast.CallExpr, kind stateRefusalKind, why string) {
		seen[call.Pos()] = true
		arg := ""
		if len(call.Args) > 0 {
			arg = text(call.Args[0])
		}
		out = append(out, stateRefusalSite{
			File: name,
			Line: fset.Position(call.Pos()).Line,
			Func: enclosingFuncName(parsed, call.Pos()),
			Arg:  arg,
			Kind: kind,
			Src:  why,
		})
	}

	ast.Inspect(parsed, func(n ast.Node) bool {
		// Shape 2 — the error is consumed by the `if` condition itself, so the
		// failure path is whatever the else branch does, which is normally
		// nothing at all.
		if ifs, ok := n.(*ast.IfStmt); ok && ifs.Init != nil {
			if call := findResolveStateFileCall(ifs.Init); call != nil {
				kind, why := classifyStateRefusalBranch(ifs.Else, text)
				record(call, kind, why)
			}
		}
		blk, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for i, st := range blk.List {
			assign, ok := st.(*ast.AssignStmt)
			if !ok {
				continue
			}
			call := findResolveStateFileCall(assign)
			if call == nil {
				continue
			}
			// Shape 1 — the error is bound, then guarded by the next statement.
			errName := boundErrName(assign)
			if errName == "" {
				record(call, refusalUnrecognised,
					text(assign)+"  ← the error is discarded at the assignment, so there is no failure path to classify")
				continue
			}
			if i+1 >= len(blk.List) {
				record(call, refusalUnrecognised, text(assign)+"  ← nothing follows the assignment")
				continue
			}
			ifs, ok := blk.List[i+1].(*ast.IfStmt)
			if !ok || !mentionsIdent(ifs.Cond, errName) {
				record(call, refusalUnrecognised,
					text(assign)+"  ← the next statement does not test "+errName)
				continue
			}
			kind, why := classifyStateRefusalBranch(ifs.Body, text)
			record(call, kind, why)
		}
		return true
	})

	// A call the walk above never reached is not a clean call — it is a shape
	// nobody taught the detector, and it has to say so.
	ast.Inspect(parsed, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isResolveStateFileCall(call) || seen[call.Pos()] {
			return true
		}
		record(call, refusalUnrecognised, text(call)+"  ← not in a recognised assign-then-guard shape")
		return true
	})

	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out, nil
}

// classifyStateRefusalBranch decides which of the three legal answers a failure
// branch gives. A nil branch (no else) is silent by construction.
//
// The order matters: the helper wins over everything, because a branch that
// wraps the helper's result in fmt.Errorf is still routing through the single
// minting point and the caller still gets the sentence.
func classifyStateRefusalBranch(branch ast.Node, text func(ast.Node) string) (stateRefusalKind, string) {
	if branch == nil {
		return refusalSilent, ""
	}
	var (
		helper  ast.Node
		handmad ast.Node
	)
	ast.Inspect(branch, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			switch fn := v.Fun.(type) {
			case *ast.SelectorExpr:
				pkg, ok := fn.X.(*ast.Ident)
				if !ok {
					return true
				}
				qualified := pkg.Name + "." + fn.Sel.Name
				if qualified == "config.StateFileMissingErr" {
					helper = v
					return true
				}
				if stateRefusalErrorMinters[qualified] && handmad == nil {
					handmad = v
				}
			case *ast.Ident:
				// The package's refusal channel. Its ARGUMENT decides: passing
				// the helper's result is the sanctioned path, passing anything
				// else — including a bare err — is a second voice.
				if fn.Name == stateRefusalChannel && handmad == nil && !containsStateFileHelper(v) {
					handmad = v
				}
			}
		case *ast.BasicLit:
			// You cannot invent a refusal voice without writing a string.
			// `return "", false` is not one: its only literal is empty.
			if v.Kind == token.STRING && handmad == nil {
				if unq, err := strconv.Unquote(v.Value); err == nil && strings.TrimSpace(unq) != "" {
					handmad = v
				}
			}
		}
		return true
	})
	if helper != nil {
		return refusalViaHelper, text(helper)
	}
	if handmad != nil {
		return refusalHandRolled, text(handmad)
	}
	return refusalSilent, ""
}

// containsStateFileHelper reports whether the helper is called anywhere inside n.
func containsStateFileHelper(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		call, ok := x.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "config" && sel.Sel.Name == "StateFileMissingErr" {
			found = true
		}
		return true
	})
	return found
}

// isResolveStateFileCall reports whether call is `config.ResolveStateFile(...)`.
func isResolveStateFileCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "ResolveStateFile" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "config"
}

// findResolveStateFileCall returns the config.ResolveStateFile call inside n, if any.
func findResolveStateFileCall(n ast.Node) *ast.CallExpr {
	var out *ast.CallExpr
	ast.Inspect(n, func(x ast.Node) bool {
		if call, ok := x.(*ast.CallExpr); ok && isResolveStateFileCall(call) && out == nil {
			out = call
		}
		return true
	})
	return out
}

// boundErrName returns the identifier the assignment binds the error to, or ""
// when the error is dropped (`_`) or the shape is not an assignment at all.
func boundErrName(assign *ast.AssignStmt) string {
	if len(assign.Lhs) == 0 {
		return ""
	}
	last, ok := assign.Lhs[len(assign.Lhs)-1].(*ast.Ident)
	if !ok || last.Name == "_" {
		return ""
	}
	return last.Name
}

// mentionsIdent reports whether expr reads the identifier called name.
func mentionsIdent(expr ast.Expr, name string) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return true
	})
	return found
}

// enclosingFuncName renders the function containing pos, receiver included, so
// two sites in different methods of the same file never collide.
func enclosingFuncName(f *ast.File, pos token.Pos) string {
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || pos < fd.Pos() || pos >= fd.End() {
			continue
		}
		if fd.Recv == nil || len(fd.Recv.List) == 0 {
			return fd.Name.Name
		}
		recv := ""
		switch rt := fd.Recv.List[0].Type.(type) {
		case *ast.StarExpr:
			if id, ok := rt.X.(*ast.Ident); ok {
				recv = "(*" + id.Name + ")"
			}
		case *ast.Ident:
			recv = rt.Name
		}
		return recv + "." + fd.Name.Name
	}
	return "<file scope>"
}

// ─── Fixtures: the shapes this gate exists to reject, and the ones it must not ──
//
// Every fixture goes through findStateRefusalSites — the same function the repo
// gate runs — so a classifier that goes blind fails here rather than quietly
// widening what the tree is allowed to do.

// refusalFixtureNovelVoice is the exact hole aihub#454 was filed for: a new
// credentialed tool inventing a fourth refusal from scratch. The old literal
// scan was blind to it because it says nothing about reading a state file.
const refusalFixtureNovelVoice = `package mcp
func registerThing() {
	sf, err := config.ResolveStateFile(wiID)
	if err != nil {
		return errResult(fmt.Errorf("could not load credentials: %w", err))
	}
	_ = sf
}
`

// refusalFixtureLegacyLiteral is the reversion the old scan DID catch. It must
// keep failing, or widening the gate would have traded one hole for another.
const refusalFixtureLegacyLiteral = `package mcp
func registerThing() {
	sf, err := config.ResolveStateFile(wiID)
	if err != nil {
		return errResult(fmt.Errorf("read state file for wi %s: %w", wiID, err))
	}
	_ = sf
}
`

// refusalFixtureBareErrResult is the voice with no words in it: the raw
// os.PathError goes straight to the caller. A vocabulary rule cannot see this
// one at all, which is why the channel's ARGUMENT is what gets classified.
const refusalFixtureBareErrResult = `package mcp
func registerThing() {
	sf, err := config.ResolveStateFile(wiID)
	if err != nil {
		return errResult(err)
	}
	_ = sf
}
`

// refusalFixtureBareMinter is aihub#454's motivating scenario in its purest
// form, and it is the shape a fixture is needed for rather than assumed: a new
// credentialed helper that is not an MCP tool, so it never touches errResult and
// mints its refusal directly. Only stateRefusalErrorMinters catches this one.
const refusalFixtureBareMinter = `package mcp
func loadCreds(wiID string) error {
	sf, err := config.ResolveStateFile(wiID)
	if err != nil {
		return fmt.Errorf("could not load credentials for %s: %w", wiID, err)
	}
	_ = sf
	return nil
}
`

// refusalFixtureCustomErrorType is why the classifier also looks at string
// literals and not only at a list of constructors. Nothing here is a listed
// minter and nothing goes through the refusal channel, yet the caller still gets
// a sentence somebody wrote — which is the definition of a second voice. A list
// of constructor names can always be stepped around; writing a refusal without
// writing a string cannot.
const refusalFixtureCustomErrorType = `package mcp
func loadCreds(wiID string) error {
	sf, err := config.ResolveStateFile(wiID)
	if err != nil {
		return &credentialError{msg: "no local credential for this work item"}
	}
	_ = sf
	return nil
}
`

// refusalFixtureHelper is the sanctioned shape.
const refusalFixtureHelper = `package mcp
func registerThing() {
	sf, err := config.ResolveStateFile(wiID)
	if err != nil {
		return errResult(config.StateFileMissingErr(wiID, err))
	}
	_ = sf
}
`

// refusalFixtureWrappedHelper wraps the helper's result. The caller still gets
// the sentence, so this is compliant — asserting otherwise would forbid adding
// context, which is not what the invariant says.
const refusalFixtureWrappedHelper = `package mcp
func registerThing() {
	sf, err := config.ResolveStateFile(wiID)
	if err != nil {
		return errResult(fmt.Errorf("ship: %w", config.StateFileMissingErr(wiID, err)))
	}
	_ = sf
}
`

// refusalFixtureSilentBool is recordedClaimSecret's shape: an empty string
// literal and a bool, no message. It must NOT be read as a hand-rolled refusal —
// this is the false positive that would force a wrong change.
const refusalFixtureSilentBool = `package mcp
func lookup() (string, bool) {
	sf, err := config.ResolveStateFile(wiID)
	if err != nil || sf == nil {
		return "", false
	}
	return sf.SessionSecret, true
}
`

// refusalFixtureSilentReturn is emitCodingEvent's shape.
const refusalFixtureSilentReturn = `package mcp
func emit() {
	sf, err := config.ResolveStateFile(wiID)
	if err != nil {
		return
	}
	_ = sf
}
`

// refusalFixtureInlineCondition is the prior-worktree read: the error is
// consumed by the condition and the failure path is the absent else.
const refusalFixtureInlineCondition = `package mcp
func register() {
	if prior, priorErr := config.ResolveStateFile(canonicalWIID); priorErr == nil && len(prior.Worktrees) > 0 {
		sf.Worktrees = prior.Worktrees
	}
}
`

// refusalFixtureDroppedError is the shape that must NOT be mistaken for clean:
// the error is thrown away at the assignment, so there is no failure path and
// the detector has nothing to classify. Reporting "no findings" here is how a
// scanner goes blind without anyone noticing.
const refusalFixtureDroppedError = `package mcp
func register() {
	sf, _ := config.ResolveStateFile(wiID)
	_ = sf
}
`

// refusalFixtureUnguarded binds the error and then never tests it.
const refusalFixtureUnguarded = `package mcp
func register() {
	sf, err := config.ResolveStateFile(wiID)
	_ = sf
	_ = err
}
`

// TestStateRefusalDetectorFiresOnFixtures is the discriminating-power proof: the
// gate above is a pile of negatives, and a negative that can never go positive
// asserts nothing. Each fixture pins one verdict the detector must reach, and
// between them they cover both directions — a novel voice must fire, and the
// four sanctioned shapes must not.
func TestStateRefusalDetectorFiresOnFixtures(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want stateRefusalKind
	}{
		{"novel fourth voice", refusalFixtureNovelVoice, refusalHandRolled},
		{"reverted legacy literal", refusalFixtureLegacyLiteral, refusalHandRolled},
		{"bare err to the refusal channel", refusalFixtureBareErrResult, refusalHandRolled},
		{"minted directly, no refusal channel", refusalFixtureBareMinter, refusalHandRolled},
		{"custom error type, no listed constructor", refusalFixtureCustomErrorType, refusalHandRolled},
		{"the helper", refusalFixtureHelper, refusalViaHelper},
		{"the helper, wrapped", refusalFixtureWrappedHelper, refusalViaHelper},
		{"silent bool", refusalFixtureSilentBool, refusalSilent},
		{"silent return", refusalFixtureSilentReturn, refusalSilent},
		{"error consumed by the condition", refusalFixtureInlineCondition, refusalSilent},
		{"error dropped at the assignment", refusalFixtureDroppedError, refusalUnrecognised},
		{"error bound but never tested", refusalFixtureUnguarded, refusalUnrecognised},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sites, err := findStateRefusalSites([]byte(tc.src), "fixture.go")
			if err != nil {
				t.Fatalf("an unparseable fixture proves nothing: %v", err)
			}
			if len(sites) != 1 {
				t.Fatalf("expected exactly 1 site, got %d: %+v — a fixture the detector cannot even "+
					"locate says nothing about how it classifies", len(sites), sites)
			}
			if sites[0].Kind != tc.want {
				t.Errorf("classified %q, want %q (offending expression: %q)", sites[0].Kind, tc.want, sites[0].Src)
			}
		})
	}
}

// TestStateRefusalDetectorReadsTheRealTreeTheWayTheExemptionsClaim is the other
// half of the discriminating-power proof, and the half a fixture cannot give.
//
// Fixtures show the classifier can reach every verdict on source somebody wrote
// to be classified. This shows it reaches the DOCUMENTED verdict on the source
// that actually ships — including the one hand-rolled site, which is the only
// place the gate's exemption path is exercised at all. Without it, a classifier
// that called every real site "silent" would pass the whole file.
func TestStateRefusalDetectorReadsTheRealTreeTheWayTheExemptionsClaim(t *testing.T) {
	sites := scanStateRefusalSites(t)

	handRolled := 0
	for _, s := range sites {
		if s.Kind == refusalHandRolled {
			handRolled++
		}
		if s.Kind == refusalUnrecognised {
			t.Errorf("%s:%d is unrecognised — see TestStateFileRefusalHasExactlyOneMintingPoint for "+
				"why that is a failure rather than a pass:\n    %s", s.File, s.Line, s.Src)
		}
	}
	if handRolled != 1 {
		t.Errorf("the tree has %d hand-rolled site(s), expected exactly 1 (the commit lock gate). More "+
			"means a second voice appeared; fewer means the exemption path in the gate above is never "+
			"taken and is therefore untested", handRolled)
	}
}
