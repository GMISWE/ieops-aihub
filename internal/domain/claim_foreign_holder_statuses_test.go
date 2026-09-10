package domain

// aihub#543 probe wave 1 — the pf_claim_work_item card's refusal sentence,
// bound to the predicate that enforces it.
//
//	"**`force_takeover` does not steal another work item's lock.** A lock held by
//	 a running or paused attempt of a DIFFERENT work item answers 409
//	 `CONFLICT_LOCK_TAKEN`."
//
// The 409 half is measured against a live database by
// TestForceTakeoverLockSteal_ClaimRefusesAForeignHolder (aihub#393), and its
// foreign holder is `running`. What no arm held is the QUANTIFIER: which attempt
// statuses make a holder live. That is a two-word phrase in the card, a two-value
// IN list in one SQL constant, and a six-value CHECK constraint in the schema —
// three places, and until this file nothing compared them.
//
// 🔴 Why the third source matters. Comparing the card against the SQL alone is
// satisfiable by writing the same wrong pair twice. The migration's CHECK is the
// full vocabulary of run_attempts.status, so the comparison below is quantified
// over EVERY status the column can hold and each one is checked in both
// directions: named in the card ⇔ selected by the predicate. A status added to
// the schema tomorrow arrives in this walk the day it is added, and the two
// unrecorded answers it could have — "the card forgot to mention it" and "the
// predicate forgot to select it" — are exactly the two this reports.
//
// No database: `foreignLockHolderSQL` is a package-level const precisely so a
// test can inspect it without one (the pattern its own comment cites).
//
//	GOWORK=off go test ./internal/domain/ -run TestPublishedForeignHolderStatuses -count=1

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const (
	// claimCardPath is the card whose sentence this arm holds.
	claimCardPath = "../../docs/mcp-cards/pf_claim_work_item.md"
	// attemptStatusMigration declares the vocabulary run_attempts.status may hold.
	attemptStatusMigration = "../db/migrations/0035_run_attempts_status_vocabulary.sql"
	// refusalMarker identifies the card bullet that states the refusal. The error
	// CODE is the anchor rather than any English phrasing, because the code is the
	// thing this repo cannot rename without a compiler noticing.
	refusalMarker = "CONFLICT_LOCK_TAKEN"
)

var statusCheckRe = regexp.MustCompile(`(?s)CHECK\s*\(status IN\s*\((.*?)\)\)`)
var quotedRe = regexp.MustCompile(`'([a-z_]+)'`)

// attemptStatusVocabulary returns every status run_attempts.status may hold,
// read from the UP half of the migration that declares it.
//
// The down half restores the PREVIOUS vocabulary (it still carries `lost` and
// not `cancelled`), so a walk that took the last CHECK in the file would measure
// the schema this repo no longer has.
func attemptStatusVocabulary(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(attemptStatusMigration)
	if err != nil {
		t.Fatalf("read %s: %v — this arm cannot quantify over a vocabulary it could not read, "+
			"and answering green while unable to check is the failure it exists to refuse",
			attemptStatusMigration, err)
	}
	up := string(raw)
	if i := strings.Index(up, "-- +goose Down"); i >= 0 {
		up = up[:i]
	}
	matches := statusCheckRe.FindAllStringSubmatch(up, -1)
	if len(matches) == 0 {
		t.Fatalf("%s declares no CHECK (status IN (…)) above its Down section — the walk is "+
			"broken, not the schema", attemptStatusMigration)
	}
	last := matches[len(matches)-1][1]
	var out []string
	for _, m := range quotedRe.FindAllStringSubmatch(last, -1) {
		out = append(out, m[1])
	}
	if len(out) < 4 {
		t.Fatalf("only %d status(es) parsed out of %s (%v) — a short vocabulary makes every "+
			"comparison below vacuous", len(out), attemptStatusMigration, out)
	}
	return out
}

// claimCardBulletContaining returns the one top-level card bullet carrying the
// marker, failing when there is not exactly one: two would make the assertion
// depend on which was found first, and none means the sentence was reworded out
// from under this arm, which is a fact to report rather than to skip.
func claimCardBulletContaining(t *testing.T, marker string) string {
	t.Helper()
	raw, err := os.ReadFile(claimCardPath)
	if err != nil {
		t.Fatalf("read %s: %v", claimCardPath, err)
	}
	var bullets []string
	var cur strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "- ") {
			bullets = append(bullets, cur.String())
			cur.Reset()
		}
		cur.WriteString(line)
		cur.WriteString(" ")
	}
	bullets = append(bullets, cur.String())

	var found []string
	for _, b := range bullets {
		if strings.Contains(b, marker) {
			found = append(found, b)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s has %d bullet(s) mentioning %s, want exactly 1. The sentence this arm holds "+
			"has moved or been split, and a probe that guesses which half to read is not holding "+
			"anything.", claimCardPath, len(found), marker)
	}
	return found[0]
}

// TestPublishedForeignHolderStatusesAreTheEnforcedOnes compares the statuses the
// card names in its refusal sentence against the statuses the claim-time foreign
// holder probe actually selects.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M23 enforcement: drop 'paused' from acquireLocksCollisionSQL's IN list — the
//	    predicate a pause-then-steal walks through
//	                                        RED  "the card says a paused holder
//	                                             answers 409 and the predicate does
//	                                             not select that status"
//	M24 publication: reword the card bullet to "a running attempt of a DIFFERENT
//	    work item"                          RED  paused is selected and no longer
//	                                             named
//	M25 enforcement: add 'wrapped' to the IN list — a status the card does not
//	    name and whose holder is an ENDED attempt
//	                                        RED  wrapped is selected and not named
//	M26 enforcement: point the vocabulary walk at the migration's DOWN half
//	                                        GREEN — and recorded because the
//	                                             prediction was RED. The two halves
//	                                             differ only in `cancelled` vs
//	                                             `lost`, and neither the card nor
//	                                             the predicate mentions either, so
//	                                             the up/down split is not load-
//	                                             bearing TODAY. It is kept because
//	                                             the walk must measure the live
//	                                             vocabulary the day those lists
//	                                             diverge on a status that is
//	                                             mentioned — and because a reader
//	                                             who assumed this mutant was red
//	                                             would over-trust the arm.
//	M27 control: reword an unrelated card sentence (the repo-pin bullet)
//	                                        GREEN bound to the refusal bullet, not
//	                                              to card churn
func TestPublishedForeignHolderStatusesAreTheEnforcedOnes(t *testing.T) {
	vocabulary := attemptStatusVocabulary(t)
	bullet := claimCardBulletContaining(t, refusalMarker)

	// The predicate the claim path and the takeover path both use. Read as text
	// rather than executed, because what is being compared is which statuses it
	// NAMES — the same thing the card's sentence does.
	sql := foreignLockHolderSQL
	if !strings.Contains(sql, "ra.status IN") {
		t.Fatalf("foreignLockHolderSQL no longer filters on ra.status at all:\n%s\nEither the "+
			"liveness rule moved somewhere this arm cannot see, or every attempt is now a live "+
			"holder — and the card's sentence describes neither.", sql)
	}

	selected, named := 0, 0
	for _, status := range vocabulary {
		inSQL := strings.Contains(sql, "'"+status+"'")
		inCard := regexp.MustCompile(`\b` + status + `\b`).MatchString(bullet)
		if inSQL {
			selected++
		}
		if inCard {
			named++
		}
		switch {
		case inSQL && !inCard:
			t.Errorf("the claim refuses a foreign holder whose attempt is %q, and the card's "+
				"refusal sentence does not name that status:\n    %s\nA caller reading the card "+
				"cannot predict the 409 they will get, which is the whole purpose of publishing "+
				"the rule.", status, strings.TrimSpace(bullet))
		case inCard && !inSQL:
			t.Errorf("the card says a %q holder answers 409 %s, and %s does not select that "+
				"status:\n%s\nThe published refusal is wider than the enforced one: a lock held by "+
				"a %s attempt of another work item changes hands, silently, while the card says it "+
				"cannot.", status, refusalMarker, "foreignLockHolderSQL", sql, status)
		}
	}

	// Floors. Two, because the two ways this comparison goes vacuous are
	// different: nothing selected (the predicate stopped filtering) and nothing
	// named (the bullet stopped being the sentence).
	if selected == 0 {
		t.Errorf("foreignLockHolderSQL selects none of the %d statuses in the vocabulary — "+
			"the parse is broken, and a broken parse agrees with any card", len(vocabulary))
	}
	if named == 0 {
		t.Errorf("the card bullet naming %s names no attempt status at all:\n    %s",
			refusalMarker, strings.TrimSpace(bullet))
	}
	t.Logf("attempt-status vocabulary=%v; selected by the predicate=%d, named by the card=%d",
		vocabulary, selected, named)
}
