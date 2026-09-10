package domain

// aihub#543 probe wave 2, lane L5 — two NEGATIVE claims on
// `docs/mcp-cards/pf_emit_event.md`, each of them the shape §1.4 calls the
// highest-value one in the set, because as prose it is the unfalsifiable form
// K11 bans and as a census it is exactly answerable:
//
//	"`event_type` is still a free string and there is still no CHECK behind it
//	 — `agent_events.event_type` is `TEXT NOT NULL` with no constraint"
//	    -> TestEventTypeCarriesNoVocabularyCheckAtHead
//	"`aihub#446` retired all three …, so `artifact_action` now has no publisher
//	 of its own"
//	    -> TestArtifactActionHasNoPublisherLeft
//
// ─── Why these are not covered by what exists ──────────────────────────────
//
// TestDBCheckRegistry_AccountsForEveryCheck enumerates the CHECK set at HEAD and
// demands a disposition for each, which makes a NEW check on this column loud —
// but a disposition is a row somebody adds, so the registry cannot say that
// event_type is unconstrained, only that whatever constrains it was declared.
// TestEventVocabulary_CoversEveryEmitter walks the emitters in ONE direction on
// purpose (found ⊆ published) and names artifact_action in its own comment as a
// reason the other direction must not be asserted — so it is the arm that
// explicitly declines to hold this.
//
// No database: both read the tree.
//
//	go test ./internal/domain/ -run 'TestEventTypeCarriesNo|TestArtifactAction' -count=1 -v

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// eventTypeInList matches the only shape a CHECK can use to restrict this
// column to a set of names.
var eventTypeInList = regexp.MustCompile(`(?is)\bevent_type\s+IN\s*\(`)

// TestEventTypeCarriesNoVocabularyCheckAtHead is the card's "no CHECK behind
// it", asserted as a property of the schema the migrations arrive at rather than
// as an absence somebody remembered to keep true.
//
// The subtlety, and the reason a naive "no CHECK names event_type" arm would be
// FALSE: chk_evt_work_item_id names the column in an IN-list. It is not a
// vocabulary. Its predicate is `work_item_id IS NOT NULL OR event_type IN (…)`,
// so the list only decides which types may be filed with NO work item — and the
// card says so two bullets further down, "the 22-entry CHECK is still not a
// vocabulary: it says which events may be filed without a work item, not which
// events exist". So what is asserted here is the DISJUNCTION: every restriction
// on this column at HEAD is guarded by the work item being absent, which is what
// makes "every other string is accepted" true for the ordinary call.
//
// 🔴 Derived from effectiveDBChecks, which replays every Up section in order
// including the DROPs, so this is the constraint set the schema CARRIES and not
// the union of everything that ever existed. A union would report 0006's and
// 0009's superseded predicates as live and this arm would fail on history.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M14 enforcement: add `ADD CONSTRAINT chk_evt_type CHECK (event_type IN
//	    ('note'))` to migration 0036's Up section
//	                                            RED  the unguarded-restriction arm
//	                                                 names chk_evt_type
//	M15 enforcement: drop the `work_item_id IS NOT NULL OR` disjunct from
//	    chk_evt_work_item_id's Up predicate     RED  the same arm — the surviving
//	                                                 constraint is then a real
//	                                                 vocabulary
//	M16 publication: delete every citation from the card sentence this arm helps
//	    retire                                  RED  K12 — the sentence lands back
//	                                                 in the debt column
//	M48 enforcement, against a migrated database: install that same
//	    CHECK (event_type IN ('note')) NOT VALID on agent_events and re-run the DB
//	    subtests this sentence also cites       RED  the off-vocabulary-with-a-work
//	                                                 -item subtest, plus three
//	                                                 pre-existing arms — a column
//	                                                 vocabulary breaks all of them
//
//	── recorded GREEN ──
//	M16a publication: delete only THIS arm's citation, leaving the two others the
//	     same sentence names                    GREEN the sentence stays cited; a
//	                                                  claim resting on three arms
//	                                                  cannot lose one visibly
func TestEventTypeCarriesNoVocabularyCheckAtHead(t *testing.T) {
	checks := effectiveDBChecks(t)

	// FLOOR, and the canary is the constraint this arm is ABOUT: a fold that
	// found nothing on agent_events would satisfy "no vocabulary CHECK" by
	// having read no schema at all, which is the same green as a column with no
	// constraint.
	require.Greater(t, len(checks), 20,
		"the fold enumerated only %d CHECK constraint(s) — the PARSE is what broke, and an "+
			"absence assertion over an empty set means nothing", len(checks))
	require.Contains(t, checks, "agent_events.chk_evt_work_item_id",
		"the fold did not find chk_evt_work_item_id, which this schema certainly carries — so "+
			"its verdict about what else constrains event_type is worth nothing. Found: %v",
		dbCheckKeys(checks))

	// Every constraint at HEAD that restricts event_type to a list, and whether
	// that restriction is guarded by the work item being absent.
	var unguarded []string
	restricting := 0
	for _, key := range dbCheckKeys(checks) {
		f := checks[key]
		if !eventTypeInList.MatchString(f.Predicate) {
			continue
		}
		restricting++
		normalised := strings.Join(strings.Fields(strings.ToLower(f.Predicate)), " ")
		if !strings.Contains(normalised, "work_item_id is not null or") {
			unguarded = append(unguarded, key+" ("+f.Migration+"): "+normalised)
		}
	}
	sort.Strings(unguarded)

	require.Empty(t, unguarded,
		"these CHECK constraints restrict agent_events.event_type to a list with NO "+
			"`work_item_id IS NOT NULL OR` guard in front of it, so the column HAS a vocabulary "+
			"and the card's \"no CHECK behind it … every other string is accepted\" is false:\n  %s\n"+
			"Either the sentence needs rewriting or the constraint needs the guard.",
		strings.Join(unguarded, "\n  "))

	// The other direction, so this is not satisfied by a regexp that stopped
	// matching: the ONE guarded restriction is still there. Without it, deleting
	// chk_evt_work_item_id from the schema would leave this arm green and the
	// 22-entry list the card describes would be gone.
	require.Equal(t, 1, restricting,
		"expected exactly one CHECK naming event_type in an IN-list at HEAD "+
			"(chk_evt_work_item_id, the null-work-item list), found %d — a second one is either a "+
			"vocabulary this card denies or a duplicate of the first", restricting)
}

// TestArtifactActionHasNoPublisherLeft is the card's "`artifact_action` now has
// no publisher of its own".
//
// A published vocabulary entry that nothing in the tree emits is not a defect —
// the rows exist and pf_read_events(types=["artifact_action"]) has to keep
// finding them — but it IS a claim, and the day somebody wires a new emitter the
// card's hop 2-3 becomes wrong in the direction that matters: a reader would go
// on believing the only way to file one is by hand.
//
// 🔴 String literals via go/parser, not a grep. tools_memory.go carries
// `event_type="artifact_action"` inside a comment explaining the retirement, so
// a text scan reports the retired tool's own tombstone as its publisher — which
// is a false positive on the one file most likely to be read next.
//
// MUTANTS (applied to this tree; the verdict is what ran):
//
//	M17 enforcement: add `s.emitCodingEvent(ctx, wiID, "artifact_action", nil)`
//	    to internal/mcp/tools_coding.go        RED  names tools_coding.go
//	M18 enforcement: delete "artifact_action" from EventVocabulary
//	                                           RED  the still-published arm; the
//	                                                sentence's subject is a type
//	                                                that IS published and has no
//	                                                emitter, so losing either half
//	                                                makes it a different claim
//	M19 publication: delete this arm's citation from the card sentence, which
//	    names no other                         RED  K12
func TestArtifactActionHasNoPublisherLeft(t *testing.T) {
	const retired = "artifact_action"

	// Half one: it is still PUBLISHED. "No publisher" is only a claim worth
	// making about a name a caller can still discover and still send.
	require.Contains(t, setOf(EventVocabulary), retired,
		"EventVocabulary no longer publishes %q, so the card's sentence — a published type whose "+
			"only source is a caller — has lost its subject", retired)

	lits := goStringLiteralSites(t, "..")

	// FLOOR plus a positive control on the live half of the same bullet: the
	// coding tools "still emit `commit` / `push` / `pr_opened` here". If the walk
	// cannot see those literals it cannot see an artifact_action one either, and
	// its silence would be indistinguishable from the fact under test.
	require.Greater(t, len(lits), 500,
		"the walk collected only %d distinct string literal(s) from internal/ — it is broken, and "+
			"an absence assertion over it would pass in silence", len(lits))
	for _, canary := range []string{"pr_opened", "commit", "push"} {
		require.Contains(t, lits, canary,
			"the walk did not find the literal %q, which internal/mcp/tools_coding.go passes to "+
				"emitCodingEvent — so it would not have found an artifact_action emitter either", canary)
	}

	// Half two: the only file declaring the literal is the vocabulary itself.
	sites := append([]string(nil), lits[retired]...)
	sort.Strings(sites)
	require.Equal(t, []string{filepath.Join("domain", "event_types.go")}, sites,
		"%q is declared as a string literal in %v. The card says this type has no publisher of "+
			"its own and that a caller is now its only source; a literal outside the vocabulary "+
			"list is either a new emitter (update the card) or a name written where a reader will "+
			"take it for one.", retired, sites)
}

// goStringLiteralSites maps every string literal declared in the non-test Go
// files under root to the repo-relative files declaring it.
//
// go/parser rather than a scan of the bytes, for the reason BuildArmIndex gives
// for the same choice: a comment or a fixture reads identically to code, and a
// census that cannot tell them apart is a census whose false positives land on
// exactly the files somebody documented the fact in.
func goStringLiteralSites(t *testing.T, root string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		seen := map[string]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, uerr := strconv.Unquote(lit.Value)
			if uerr != nil || seen[v] {
				return true
			}
			seen[v] = true
			out[v] = append(out[v], rel)
			return true
		})
		return nil
	})
	require.NoError(t, err, "walking %s for Go string literals", root)
	return out
}
