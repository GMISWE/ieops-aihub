package mcp_test

// aihub#543 probe wave 1 — three `docs/mcp-cards/pf_commit.md` sentences about
// what pf_commit ANSWERS, as opposed to what its gate decides:
//
//	"A file held by another live attempt REFUSES the whole commit with
//	 CONFLICT_LOCK_TAKEN: nothing is committed, the files stay staged, and the
//	 error names every blocked path plus its holder — actor, work item and
//	 attempt."
//	    -> TestRefusedCommitLeavesTheIndexPopulatedAndNamesEveryHolder
//	"A refusal or a failed check comes back as a plain error string with no
//	 `lock_gate` field at all, so its absence is a signal rather than a default."
//	    -> TestRefusedOrUncheckedCommitCarriesNoLockGateField
//	"`jsonResult` of `{sha, repo, files}` plus the gate's report."
//	    -> TestCommitSuccessAnswersTheCardsThreeKeysPlusTheGateReport
//
// 🔴 WHY THE aihub#366 WIRE ARMS NEXT DOOR DO NOT HOLD THESE.
// commit_gate_wire_test.go is thorough about the GATE — it drives every
// lock_gate value, the fail-closed branch and the executable remedy — but it
// asserts the answer's SHAPE only where the shape happened to be convenient:
//
//   - TestCommitGateWire_ConflictRefusesTheCommit checks the refusal text for the
//     path, the actor and the work item, and stops there. The attempt id is in
//     the fixture and never looked for, so the card's "actor, work item and
//     attempt" rests on two of its three nouns. It also never looks at the index,
//     while "the files stay staged" is the half a reader acts on: it is what makes
//     the refusal's remedy a two-step (`git restore --staged` then a narrowed
//     retry) rather than a plain re-run.
//   - TestCommitGateWire_CommitAdvertisesOnlyReachableLockGateValues does close
//     the "no lock_gate on a failure" half — for ONE failure, the empty index,
//     which is neither of the two the sentence names. A refusal and a check that
//     could not run are the two paths where the gate DID have something to say
//     and the response deliberately does not carry it, and neither was driven.
//   - Nothing anywhere reads the hop-5 key set. K10 is a one-directional ratchet
//     (an undeclared key fails; a declared key nothing emits is invisible), so
//     "jsonResult of {sha, repo, files} plus the gate's report" could lose `files`
//     entirely with every gate in the repo green.
//
// Every expectation below is read out of the published text — the card for the
// key list, the live tool description for the key name and the promised holder
// nouns — so a change to either side of the contract alone reddens these arms.
// The alternative, a literal "lock_gate" in the test, goes green on the one day
// the name moves.
//
// No database:
//
//	GOWORK=off go test ./internal/mcp/ -run 'TestRefusedCommit|TestRefusedOrUnchecked|TestCommitSuccessAnswers' -count=1

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// commitCardPath is the card these three sentences live on.
const commitCardPath = "../../docs/mcp-cards/pf_commit.md"

// readCardText returns one card's whole text, failing rather than skipping: the
// card is the published half of every claim below, so a card this walk cannot
// read is a walk that would otherwise assert only the code half.
func readCardText(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v — the card is the PUBLISHED side of this probe, so an "+
			"unreadable one must fail rather than leave the code half asserting alone", path, err)
	}
	return string(raw)
}

// The blocked path and the holder fields the fake server reports on a conflict.
// Distinct, improbable values, so a substring hit is the value travelling rather
// than a word that happens to occur in the message.
const (
	contestedPath    = "contested/only-here.go"
	contestedAttempt = "ra_holderProbe7"
	contestedActor   = "holder-probe-actor"
	contestedWI      = "aihub#9731"
)

// conflict409 answers the gate with the 409 the server really sends, holder
// fields and all.
func conflict409(map[string]any) (int, any) {
	return http.StatusConflict, map[string]any{
		"code":    "CONFLICT_LOCK_TAKEN",
		"message": "this commit changes 1 file(s) locked by another attempt",
		"details": map[string]any{
			"conflicts": []map[string]any{{
				"path": contestedPath, "resource_key": "aihub:aihub:" + contestedPath,
				"attempt_id": contestedAttempt, "actor_display": contestedActor,
				"work_item_slug": contestedWI,
			}},
			"conflict_with": map[string]any{
				"attempt_id": contestedAttempt, "actor_display": contestedActor,
				"work_item_slug": contestedWI,
			},
			"blocked_paths": []string{contestedPath},
		},
	}
}

// holderNounsFromDescription pulls the holder fields pf_commit PROMISES to name
// out of its own description, so the census below is over the published promise
// rather than over a list written into this test.
//
// The clause is "the error names every blocked path plus its holder — actor,
// work item and attempt." Each noun maps to the field whose value must survive
// the trip; a noun with no mapping fails loudly, because a description that
// grew a fourth promise is exactly the case a fixed list skips in silence.
func holderNounsFromDescription(t *testing.T, desc string) map[string]string {
	t.Helper()
	const head = "names every blocked path plus its holder — "
	i := strings.Index(desc, head)
	if i < 0 {
		t.Fatalf("pf_commit's description no longer promises to name the holder after %q, so "+
			"this arm cannot read what it promises. If the promise was withdrawn, the card "+
			"sentence has to go with it.\n%s", head, desc)
	}
	rest := desc[i+len(head):]
	j := strings.Index(rest, ".")
	if j < 0 {
		t.Fatalf("the holder promise has no recognisable end: %s", rest)
	}
	known := map[string]string{
		"actor":     contestedActor,
		"work item": contestedWI,
		"attempt":   contestedAttempt,
		"path":      contestedPath,
	}
	// Split on both separators the clause uses. A comma-only split leaves
	// "work item and attempt" as one unparseable noun, which would have to be
	// either skipped (two of three promises unchecked) or hard-coded.
	out := map[string]string{}
	for _, noun := range strings.Split(strings.ReplaceAll(rest[:j], " and ", ","), ",") {
		noun = strings.TrimSpace(noun)
		if noun == "" {
			continue
		}
		want, ok := known[noun]
		if !ok {
			t.Fatalf("pf_commit's description promises to name the holder's %q and nothing here "+
				"knows which value that is. A promise this arm cannot check is worse than one it "+
				"refuses: add the mapping, or the description names something the refusal does "+
				"not carry.", noun)
		}
		out[noun] = want
	}
	// The path is promised ahead of the dash and belongs to the same census.
	out["blocked path"] = contestedPath
	if len(out) < 3 {
		t.Fatalf("parsed only %d promised holder field(s) out of %q; a one-field census is a "+
			"parser reading the wrong thing, and it would pass on a refusal naming almost nothing",
			len(out), rest[:j])
	}
	return out
}

// TestRefusedCommitLeavesTheIndexPopulatedAndNamesEveryHolder is the refusal
// sentence, all four of its clauses.
//
// The index assertion is the one with no equivalent anywhere else, and it is not
// bookkeeping: `git add` only ever adds, so a refusal that left the index
// populated is why the published remedy is `git restore --staged` FOLLOWED BY a
// narrowed retry. An implementation that cleaned up after itself would make the
// first step wrong, and every other arm in the repo would stay green.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M1  the gate stops wrapping the server's 409, so the holder
//	    never reaches the caller                                  RED
//	M2  GitCommitGated runs the gate BEFORE staging, so a
//	    refusal leaves an empty index                             RED
//	M3  the description promises a fourth holder field
//	    ("actor, work item, machine and attempt")                 RED
//	M4  the gate treats CONFLICT_LOCK_TAKEN as success            RED
//	M5  green control: reword the description AFTER the holder
//	    clause                                                    GREEN
//
// ⚠️ The census parser is anchored on the description's literal "names every
// blocked path plus its holder — ", so rewording THAT clause fails the arm
// loudly rather than silently. Measured: replacing "names" with "identifies"
// goes red. That is the direction chosen on purpose — a parser that shrugged
// would leave the promise unchecked — but it means a reword of the clause has
// to update the anchor in the same diff.
func TestRefusedCommitLeavesTheIndexPopulatedAndNamesEveryHolder(t *testing.T) {
	f := newFakeAihub(t)
	wsRoot, wt := gateRepo(t)
	before := gateHEAD(t, wt)
	f.on(commitLocksPath, conflict409)

	if err := os.MkdirAll(filepath.Join(wt, filepath.Dir(contestedPath)), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	gateWrite(t, wt, contestedPath, "somebody else holds the lock on this")

	out, isErr := callTool(t, f, "pf_commit", map[string]any{
		"workspace_root": wsRoot, "work_item_id": gateWIID,
		"repo": "aihub", "message": "feat: must not land",
	})
	if !isErr {
		t.Fatalf("pf_commit succeeded over a file another live attempt holds: %v", out)
	}

	// "nothing is committed"
	if got := gateHEAD(t, wt); got != before {
		t.Errorf("HEAD moved to %s; the refusal did not stop the commit", got)
	}

	// "the files stay staged" — the clause the published remedy is built on.
	staged := gateIndex(t, wt)
	if len(staged) == 0 {
		t.Errorf("the index is empty after a refused commit, but the refusal's own remedy tells "+
			"the caller to run `git restore --staged` first. Either the remedy is now wrong or "+
			"this behaviour changed; index vs HEAD = %v", staged)
	} else if !containsPath(staged, contestedPath) {
		t.Errorf("the index holds %v, which does not include %q — the refused change set is "+
			"what has to still be there for the remedy to have something to unstage",
			staged, contestedPath)
	}

	// "the error names every blocked path plus its holder — actor, work item and
	// attempt", read off the live description rather than listed here.
	text, _ := out["_raw"].(string)
	if text == "" {
		t.Fatalf("the refusal reached the caller as %v, with no plain-string payload to read", out)
	}
	if !strings.Contains(text, "CONFLICT_LOCK_TAKEN") {
		t.Errorf("the refusal never names the code callers branch on: %s", text)
	}
	for noun, want := range holderNounsFromDescription(t, toolDescription(t, "pf_commit")) {
		if !strings.Contains(text, want) {
			t.Errorf("pf_commit promises the refusal names the holder's %s and %q is not in what "+
				"reached the caller. Without it the caller's next move is a pf_read_events "+
				"lookup, which is the round-trip carrying the holder exists to avoid:\n%s",
				noun, want, text)
		}
	}
}

// containsPath reports whether the staged set names a path, allowing for git's
// forward-slash rendering on any platform.
func containsPath(staged []string, want string) bool {
	for _, s := range staged {
		if filepath.ToSlash(s) == want {
			return true
		}
	}
	return false
}

// lockGateKeyFromDescription reads the response key pf_commit says a failure
// does NOT carry, out of the description's own sentence.
func lockGateKeyFromDescription(t *testing.T, desc string) string {
	t.Helper()
	re := regexp.MustCompile("plain error string with no `([A-Za-z_]+)` field at all")
	m := re.FindStringSubmatch(desc)
	if m == nil {
		t.Fatalf("pf_commit's description no longer states which field a failure omits, so "+
			"there is no published claim here to check. The card sentence about it must go at "+
			"the same time.\n%s", desc)
	}
	return m[1]
}

// TestRefusedOrUncheckedCommitCarriesNoLockGateField is the absence sentence,
// driven on both failures it names.
//
// 🔴 The key name is read out of the description AND required on the success
// path, which is what makes this a gate in both directions. Checking only the
// absence would go green the day the field is renamed — an absent
// `lock_gate` is exactly what a response carrying `gate_state` looks like from
// here — and the sentence would then be describing a field that no longer
// exists while every arm stayed quiet.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M1  pf_commit answers a failure with jsonResult + the gate
//	    report instead of errResult                              RED
//	M2  the description renames the field in its failure clause
//	    only                                                     RED
//	M3  report() renames the field it emits, only                RED
//	M4  green control: reword the failure clause around the same
//	    backticked name                                          GREEN
func TestRefusedOrUncheckedCommitCarriesNoLockGateField(t *testing.T) {
	key := lockGateKeyFromDescription(t, toolDescription(t, "pf_commit"))

	// The positive control first: the name has to be the one a SUCCESS really
	// carries, or "absent" below means nothing.
	t.Run("the named field is what a success carries", func(t *testing.T) {
		f := newFakeAihub(t)
		wsRoot, wt := gateRepo(t)
		f.on(commitLocksPath, gateNeverBlocks)
		gateWrite(t, wt, "landed.txt", "x")

		out, isErr := callTool(t, f, "pf_commit", map[string]any{
			"workspace_root": wsRoot, "work_item_id": gateWIID,
			"repo": "aihub", "message": "feat: landed",
		})
		if isErr {
			t.Fatalf("pf_commit failed: %v", out)
		}
		if _, has := out[key]; !has {
			t.Fatalf("the description says a failure omits %q, but a SUCCESS does not carry it "+
				"either: %v. The published name and the emitted name have come apart, and the "+
				"absence sentence is then about a field that does not exist", key, out)
		}
	})

	for _, tc := range []struct {
		name    string
		handler func(map[string]any) (int, any)
		why     string
	}{
		{"a refusal", conflict409, "another live attempt holds one of the files"},
		{"a failed check", func(map[string]any) (int, any) {
			return http.StatusBadGateway, map[string]any{"code": "UPSTREAM", "message": "boom"}
		}, "the check could not reach the server"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAihub(t)
			wsRoot, wt := gateRepo(t)
			f.on(commitLocksPath, tc.handler)
			gateWrite(t, wt, "blocked.txt", "x")

			out, isErr := callTool(t, f, "pf_commit", map[string]any{
				"workspace_root": wsRoot, "work_item_id": gateWIID,
				"repo": "aihub", "message": "feat: refused",
			})
			if !isErr {
				t.Fatalf("pf_commit succeeded although %s: %v", tc.why, out)
			}
			if _, has := out[key]; has {
				t.Errorf("a failed pf_commit carried %q=%v. The card and the description both "+
					"say the field's ABSENCE is the signal, so a caller reading it on a failure "+
					"reads a gate verdict that was never reached", key, out[key])
			}
			raw, plain := out["_raw"].(string)
			if !plain {
				t.Errorf("the failure came back as a JSON object (%v), not the plain error string "+
					"the card describes. pf_ship's structured failure object is the deliberate "+
					"exception; pf_commit answering one would make the two tools' failure "+
					"contracts silently identical", out)
			}
			if strings.Contains(raw, key) {
				t.Errorf("the plain error string mentions %q: %s — the absence is the signal, and "+
					"a failure that talks about the field invites a caller to look for it", key, raw)
			}
		})
	}
}

// commitResultKeysFromCard reads the response keys the card's hop-5 sentence
// names, out of the sentence.
func commitResultKeysFromCard(t *testing.T, card string) []string {
	t.Helper()
	re := regexp.MustCompile(`jsonResult` + "`" + ` of ` + "`" + `\{([^}]*)\}` + "`")
	m := re.FindStringSubmatch(card)
	if m == nil {
		t.Fatalf("pf_commit's card no longer states its result keys in the `{a, b, c}` form, so "+
			"this arm has no published list to compare against:\n%s", commitCardPath)
	}
	var keys []string
	for _, k := range strings.Split(m[1], ",") {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) < 2 {
		t.Fatalf("parsed %v out of the card's result-key list; a one-key list is a parser "+
			"reading the wrong thing and would pass against almost any response", keys)
	}
	return keys
}

// TestCommitSuccessAnswersTheCardsThreeKeysPlusTheGateReport is the hop-5
// sentence, in both directions.
//
// K10 cannot hold it. K10 refuses an UNDECLARED key and is blind to a declared
// key nothing emits, so `files` could disappear from the response with the card
// unchanged and every arm green. And the reverse — a key the card never
// mentions arriving in the answer — is the drift that put a keep-list in
// pf_force_takeover's projection dropping three identity fields in silence.
//
// The gate's own fields are allowed by PREFIX rather than by name, because they
// are the one part of this response whose composition is another sentence's
// subject (TestCommitGateWire_CommitAdvertisesOnlyReachableLockGateValues owns
// which values are reachable). What must not slip in is a field belonging to
// neither half.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M1  drop "files" from pf_commit's jsonResult          RED
//	M2  drop `files` from the card's key list             RED (the extra-key arm)
//	M3  add an undocumented key to the success response   RED
//	M4  green control: reorder the card's key list        GREEN
func TestCommitSuccessAnswersTheCardsThreeKeysPlusTheGateReport(t *testing.T) {
	want := commitResultKeysFromCard(t, readCardText(t, commitCardPath))

	f := newFakeAihub(t)
	wsRoot, wt := gateRepo(t)
	f.on(commitLocksPath, gateNeverBlocks)
	gateWrite(t, wt, "answered.txt", "x")

	out, isErr := callTool(t, f, "pf_commit", map[string]any{
		"workspace_root": wsRoot, "work_item_id": gateWIID,
		"repo": "aihub", "message": "feat: answered", "paths": []any{"answered.txt"},
	})
	if isErr {
		t.Fatalf("pf_commit failed: %v", out)
	}

	named := map[string]bool{}
	for _, k := range want {
		named[k] = true
		// Presence, not truthiness: `files` echoes the `paths` argument and is
		// null when the caller staged everything, which is still the key the
		// card promises.
		if _, has := out[k]; !has {
			t.Errorf("pf_commit's card names %q among its result keys and the response does not "+
				"carry it: %v", k, out)
		}
	}
	for k := range out {
		if named[k] || strings.HasPrefix(k, "lock_gate") || strings.HasPrefix(k, "locks_") {
			continue
		}
		t.Errorf("pf_commit answered with %q, which is neither one of the card's keys (%v) nor "+
			"part of the gate's report. An undocumented key is a contract a caller can come to "+
			"depend on with nothing published about it", k, want)
	}
}
