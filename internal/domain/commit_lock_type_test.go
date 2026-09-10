package domain

// aihub#543 probe wave 1 — the four `docs/mcp-cards/pf_commit.md` and
// `docs/mcp-cards/pf_ship.md` sentences that all rest on one claim: the commit
// gate operates on `file_scope` and on nothing else, so an advisory `repo` or
// `service` declaration and this gate can never meet.
//
//	pf_commit "§6.2 T2-15 — … the de-locking ruling shrinks the affected row to
//	           `file_scope`, which is exactly the type this gate takes."
//	pf_commit "This gate takes `file_scope` locks for the PATHS a commit
//	           contains; an advisory `repo` or `service` entry derives no lock at
//	           all, so the two operate on disjoint sets."
//	pf_ship   "§6.4 item 6 is CLOSED for this tool as of `aihub#416` … an
//	           advisory `repo`/`service` declaration derives no lock, and this
//	           gate takes `file_scope` locks for the paths a commit contains, so
//	           the two never meet."
//
// ─── Why the existing arms are not enough on their own ─────────────────────
//
// lock_derivation_retired_test.go holds both halves at the MAPPER —
// TestResourceToLock_PathStillDerivesFileScope and
// TestResourceToLock_RepoAndServiceDeriveNoLock — and commit_locks_test.go's
// TestCommitGateCoverage_KeyForms exercises the gate's coverage predicate over
// file_scope keys. What none of them ties down is the SENTENCE'S claim, which is
// about this gate specifically and has two ends the mapper cannot see:
//
//  1. the type name the gate probes with has to be the same one it reads the
//     attempt's held keys back with. Those are two independent literals in
//     commit_locks.go — the `lockType != "file_scope"` guard and
//     heldFileScopeKeysSQL's `resource_type = 'file_scope'` — and if they ever
//     disagreed the gate would acquire locks it could never afterwards see as
//     covering anything, re-probing the same path on every commit. Nothing
//     visible would break: coverage would just quietly always be empty.
//  2. the type name in the CARDS has to be that same literal. The mapper arms
//     hard-code "file_scope" and would stay green through any amount of
//     documentation drift.
//
// So the expected type is read out of the published cards and compared against
// the derivation, the guard's own literal and the held-key query. Never written
// into this test: an arm that hard-codes the value goes green on the one day the
// value moves, which is the day it was needed.
//
// No database — every site below is a pure derivation or a SQL string.
//
//	GOWORK=off go test ./internal/domain/ -run TestCommitGateKeys -count=1 -v

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// commitGateCards are the two cards that publish the gate's lock type. Both,
// because pf_ship's sentence says "same answer as pf_commit" and a card set
// where the two disagreed is exactly what "same answer" stops being true of.
var commitGateCards = []string{
	"../../docs/mcp-cards/pf_commit.md",
	"../../docs/mcp-cards/pf_ship.md",
}

// publishedLockTypeRe matches the cards' own phrasing for the type: a backticked
// snake_case token immediately followed by the word "locks".
// The whitespace class matters: the cards are hard-wrapped, so the token and the
// word "locks" land on different lines about as often as not, and a literal
// space would silently measure half the occurrences.
var publishedLockTypeRe = regexp.MustCompile("`([a-z_]+)`\\s+locks")

// publishedGateLockType returns the single lock type the cards name, failing if
// they name none or more than one.
//
// 🔴 Requiring agreement across every occurrence is the half that earns this
// function. A card that said `file_scope` in one paragraph and something else in
// another would satisfy any "some occurrence matches" check while telling two
// readers two different things, and the disagreement is invisible to a reader who
// only reaches one of the two paragraphs.
func publishedGateLockType(t *testing.T) string {
	t.Helper()
	seen := map[string][]string{}
	for _, path := range commitGateCards {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v — the card is the PUBLISHED side of this claim, so an "+
				"unreadable one must fail rather than leave the derivation asserting alone",
				path, err)
		}
		for _, m := range publishedLockTypeRe.FindAllStringSubmatch(string(raw), -1) {
			seen[m[1]] = append(seen[m[1]], path)
		}
	}
	if len(seen) == 0 {
		t.Fatalf("neither %v states the gate's lock type in the `<type>` locks form, so this arm "+
			"has no published claim to check. If the sentences went, this file goes with them.",
			commitGateCards)
	}
	if len(seen) > 1 {
		t.Fatalf("the cards name %d different gate lock types: %v. One of them is wrong and a "+
			"reader has no way to tell which", len(seen), seen)
	}
	// Exactly one entry by now, so this names the only type the cards state.
	var typ string
	var where []string
	for k, v := range seen {
		typ, where = k, v
	}

	// And EVERY card has to carry it. With only the union checked, one card could
	// drop the sentence entirely and this arm would keep reading the type off the
	// other one — which is precisely the "same answer as pf_commit" claim going
	// unstated while still being asserted.
	for _, card := range commitGateCards {
		found := false
		for _, w := range where {
			found = found || w == card
		}
		if !found {
			t.Errorf("%s no longer states the gate's lock type (%q) in the `<type>` locks form. "+
				"Its sentence about the gate is then unheld by this arm while the other card's "+
				"keeps it looking covered", card, typ)
		}
	}

	t.Logf("cards publish the commit gate's lock type as %q (%d occurrence(s) across %v)",
		typ, len(where), commitGateCards)
	return typ
}

// advisoryTypes are the declaration types aihub#416 left advisory: they appear in
// a work item's declared_resources and derive no lock at all. Named here rather
// than derived, with the floor below standing in for the enumeration: this is the
// list the sentence itself names, and a silently shorter one would measure less
// than the sentence claims.
var advisoryTypes = []string{"repo", "service"}

// TestCommitGateKeysFileScopeAndTheAdvisoryTypesDeriveNothing is the disjointness
// claim, from the published type name down to both derivations.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M1  resourceToLock stops matching the "path" type            RED (arm 1)
//	M2  heldFileScopeKeysSQL filters resource_type = 'worktree'   RED (arm 2)
//	M3  pf_commit.md renames the type to `path_scope`             RED (publication)
//	M4  pf_ship.md renames it, so the two cards disagree          RED (per-card floor)
//	M5  restore the retired repo -> git_branch derivation         RED (arm 3)
//	M6  green control: reword the prose around the same token     GREEN
func TestCommitGateKeysFileScopeAndTheAdvisoryTypesDeriveNothing(t *testing.T) {
	want := publishedGateLockType(t)

	// ── Arm 1: what the gate derives for a path it is about to commit ───────
	//
	// The item shape is FnReconcileCommitLocks's own, built the same way from a
	// repo-relative path, so this is the derivation the gate really performs
	// rather than a nearby one.
	const path = "internal/domain/commit_locks.go"
	item := DeclaredResourceItem{Type: "path", URI: "file:" + path, Repo: "aihub", Intent: "write"}
	lockType, lockKey, probe := derivedLockProbe(item, "aihub")
	if lockType != want {
		t.Errorf("the gate derives lock type %q for a path it is committing, and the cards say "+
			"%q. The gate's guard skips any path whose type is not %q, so a mismatch here is a "+
			"gate that protects nothing while reporting every file covered",
			lockType, want, want)
	}
	if lockKey == "" {
		t.Errorf("the gate derived type %q with an empty key for %q. The guard admits a path only "+
			"when both are set, so an empty key silently drops the file out of the change set the "+
			"gate protects", lockType, path)
	}
	if len(probe.Keys) == 0 && probe.LikePattern == "" {
		t.Errorf("the conflict probe for %q is empty in both arms, so anyMatches can never match "+
			"and every path would be reported uncovered on every commit", path)
	}

	// ── Arm 2: the type it reads the attempt's held keys back with ──────────
	//
	// Two literals that must agree, with nothing in the compiler forcing them to.
	if !strings.Contains(heldFileScopeKeysSQL, "resource_type = '"+want+"'") {
		t.Errorf("heldFileScopeKeysSQL does not restrict resource_type to %q:\n%s\nThe gate would "+
			"then compare a commit's %q keys against a different type's rows — coverage comes "+
			"back empty, every path is re-probed, and nothing fails",
			want, heldFileScopeKeysSQL, want)
	}

	// ── Arm 3: the advisory half, which is what makes the sets disjoint ─────
	if len(advisoryTypes) == 0 {
		t.Fatal("advisoryTypes is empty, so the disjointness arm below asserts nothing at all")
	}
	for _, typ := range advisoryTypes {
		lt, lk, p := derivedLockProbe(
			DeclaredResourceItem{Type: typ, URI: typ + ":aihub", Intent: "write"}, "aihub")
		if lt != "" || lk != "" {
			t.Errorf("an advisory %q declaration derives (%q, %q). The cards say it derives no "+
				"lock at all and that the gate's set and the advisory set are therefore disjoint; "+
				"a lock here puts them back in contact and the §6.4 item-6 answer on both cards "+
				"stops being true", typ, lt, lk)
		}
		if len(p.Keys) != 0 || p.LikePattern != "" {
			t.Errorf("an advisory %q declaration produced a non-empty conflict probe %+v; a probe "+
				"with no lock behind it is a blocking test nothing releases", typ, p)
		}
	}
}
