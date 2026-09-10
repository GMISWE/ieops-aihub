package domain

// aihub#543 probe wave 1 — the `docs/mcp-cards/pf_predict_conflicts.md` sentence
// that qualifies read intent by declared type.
//
//	"- **`intent: \"read\"` is honoured on `path`/`document`/`section` only.** On a
//	 repo or service entry `read` is inert for a different reason since
//	 `aihub#416`: those two take no lock under any intent, so there is nothing for
//	 it to suppress."
//
// 🔴 THE WORD THIS ARM IS ABOUT IS "ONLY", AND NOTHING HELD IT.
// TestReadIntentTakesNoWriteLock drives one `path` entry through claim, takeover
// and acquire_locks; TestResourceToLock_RepoAndServiceDeriveNoLock walks repo and
// service across every intent. Between them, `document` and `section` under
// intent=read were asserted nowhere — and those are two of the three types the
// card names. A derivedLock that honoured read for `path` alone keeps both of
// those arms green while making the published sentence false for two thirds of
// the types it names.
//
// The walk quantifies over declaredResourceTypes, the live vocabulary, rather
// than over a written list of six: a SEVENTH declared type would be a type this
// sentence says nothing about, and a hand-written list is how it would arrive
// unnoticed.
//
// The publication half is held next door — TestDeclaredResourcesProp_QualifiesReadIntent
// asserts the schema NAMES these types, and this arm asserts the server treats
// exactly them that way.
//
// No database:
//
//	GOWORK=off go test ./internal/domain/ -run TestReadIntentIsHonoured -count=1

import (
	"sort"
	"testing"
)

// readHonouringTypes is the set the card publishes: the declared types where
// intent=read suppresses the lock that would otherwise be derived.
var readHonouringTypes = map[string]bool{"path": true, "document": true, "section": true}

// probeIntents is every intent value with a reason to be tried. "" and
// "exclusive" are in it because `intent` is deliberately NOT an enum — the schema
// says unknown values are accepted and inert — so "read is the only special
// value" has to be checked against values that are not read rather than assumed.
var probeIntents = []string{"", "read", "write", "refactor", "exclusive", "observe"}

// TestReadIntentIsHonouredOnlyOnTheTypesThatDeriveALock is the "only" half of the
// published qualification.
//
// MUTANTS (run against this tree; the verdict is what happened):
//
//	M8  enforcement: in derivedLock, narrow the read exemption to
//	    `res.Type == "path"`                 RED  document/read and section/read —
//	                                              and ONLY those two, which is the
//	                                              gap this arm was written for
//	M9  enforcement: in derivedLock, drop the `res.Intent == "read"` condition
//	                                         RED  path/read, document/read, section/read
//	M10 enforcement: restore a git_branch derivation for repo in resourceToLock
//	                                         RED  lock_deriving_set (the two sets
//	                                              stop being one set) and every
//	                                              repo/<intent> arm
//	M11 GREEN CONTROL: reword rule 3's Description string
//	                                         GREEN — this arm reads the mapper, and
//	                                              a control that reddened here would
//	                                              mean it was reading prose
//	M26 publication: drop this arm's citation from the card sentence
//	                                         RED  K12 DEBT_GROWTH. ⚠️ This arm reads
//	                                              the mapper, not the card, so the
//	                                              publication side is held by K12's
//	                                              citation binding — and by
//	                                              TestDeclaredResourcesProp_QualifiesReadIntent
//	                                              for the schema sentence
func TestReadIntentIsHonouredOnlyOnTheTypesThatDeriveALock(t *testing.T) {
	const project = "aihub543-read-intent"

	// The floor, first: a mapper that derived nothing for anything would satisfy
	// every "no lock" assertion below, and "read takes no lock" is the assertion
	// this whole arm is about.
	locking := lockDerivingTypes(t, project, "write")
	if len(locking) == 0 {
		t.Fatalf("no declared type derives a lock under intent=write, over the live vocabulary %v. "+
			"Every read assertion below is \"no lock\", which a mapper that derives nothing satisfies "+
			"for free — so this is a hard stop.", sortedKeys(declaredResourceTypes))
	}

	t.Run("lock_deriving_set", func(t *testing.T) {
		want := sortedKeys(readHonouringTypes)
		if got := locking; !equalStrings(got, want) {
			t.Errorf("under intent=write the declared types that derive a lock are %v, and the card "+
				"names %v as the types where read is honoured. The two sets are one set: read is "+
				"honoured exactly where there is a lock for it to suppress, and the card's second "+
				"sentence says repo and service are inert \"for a different reason\" — because they "+
				"derive nothing. If these lists differ, one of the two sentences is false.", got, want)
		}
	})

	for _, typ := range sortedKeys(declaredResourceTypes) {
		for _, intent := range probeIntents {
			name := typ + "/" + intentLabel(intent)
			t.Run(name, func(t *testing.T) {
				lockType, lockKey := derivedLock(withIntent(declaredEntryFor(typ), intent), project)
				gotLock := lockType != "" || lockKey != ""
				wantLock := readHonouringTypes[typ] && intent != "read"

				switch {
				case wantLock && !gotLock:
					t.Errorf("derivedLock(%s, intent=%q) took no lock. This type derives file_scope and "+
						"the intent is not \"read\", so suppressing it here widens the read exemption to a "+
						"value the schema calls inert — and an inert value that silently drops a lock is a "+
						"work item that believes a path is guarded when it is not.", typ, intent)
				case !wantLock && gotLock:
					if readHonouringTypes[typ] {
						t.Errorf("derivedLock(%s, intent=\"read\") = (%q, %q), want no lock. The published "+
							"description says read is honoured on path/document/section, and this is one of "+
							"them — a read declaration that still takes the lock 409s somebody else's claim "+
							"for a file this work item only reads.", typ, lockType, lockKey)
					} else {
						t.Errorf("derivedLock(%s, intent=%q) = (%q, %q), want no lock under EVERY intent. "+
							"aihub#416 retired this derivation, so a lock here is a resource_locks row "+
							"nothing releases — and it puts the type back inside the severity ceiling the "+
							"card publishes, where rule 1 can hard_block on it again.",
							typ, intent, lockType, lockKey)
					}
				}
			})
		}
	}
}

// lockDerivingTypes returns the declared types that derive a lock under one
// intent, over the live vocabulary.
func lockDerivingTypes(t *testing.T, project, intent string) []string {
	t.Helper()
	var out []string
	for _, typ := range sortedKeys(declaredResourceTypes) {
		if lockType, _ := derivedLock(withIntent(declaredEntryFor(typ), intent), project); lockType != "" {
			out = append(out, typ)
		}
	}
	sort.Strings(out)
	return out
}

// declaredEntryFor builds a valid entry for a declared type, taking the uri
// scheme from declaredResourceURISchemes rather than writing it down — the
// schemes are validated per type, and a hand-written prefix is the aihub#395
// second copy this repo already paid for once.
func declaredEntryFor(typ string) DeclaredResourceItem {
	uri := declaredResourceURISchemes[typ] + "aihub543-read-intent"
	if declaredResourceURISchemes[typ] == "" {
		uri = "https://example.test/aihub543-read-intent"
	}
	return DeclaredResourceItem{Type: typ, URI: uri}
}

func withIntent(res DeclaredResourceItem, intent string) DeclaredResourceItem {
	res.Intent = intent
	return res
}

func intentLabel(intent string) string {
	if intent == "" {
		return "unset"
	}
	return intent
}
