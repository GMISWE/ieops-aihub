package server

// aihub#543 probe wave 2 — the `docs/mcp-cards/pf_get_memory.md` sentence that
// gives this tool its reason to exist:
//
//	"This tool is one half of an escape hatch: `pf_recall` truncates content to
//	 800 runes and flags it with `content_truncated` / `content_full_len`, and
//	 without a by-id read those flags would tell the model its text is incomplete
//	 while giving it no way to complete it."
//	    -> TestPublishedRecallSnippetLimitIsTheEnforcedOne
//
// ─── Why a number in a card needs an arm ───────────────────────────────────
//
// 800 is published on a card and enforced by a constant in this package, and
// nothing joined the two. That is the base-strength shape verbatim
// (TestPublishedBaseStrengthRangeIsTheEnforcedOne): a published range and an
// enforced range with no arm between them drift, and the drift is invisible
// because both halves keep looking internally consistent. Here the drift is
// worse than cosmetic in one direction — a caller told the snippet is 800 runes
// budgets a follow-up read per item over that threshold, and a smaller real
// limit means every item it sizes is short by an amount no field reports.
//
// The flags are asserted alongside the number, and for a sharper reason: the
// truncation is what MAKES them true. A block that shortened the body and set
// neither flag is the aihub#269 defect itself — the model reasoning on a snippet
// while believing it holds the whole memory — and a block that set the flags
// without recording the real length leaves the caller unable to tell whether the
// follow-up read is worth a round trip.
//
// Non-DB by construction: the published number, the enforced constant, and which
// fields the truncation assigns are all properties of this package's source.
// What a real 801-rune row does end to end is the DB-gated recall handler
// family's business; this arm is the one that fails on `go test ./...`.
//
//	GOWORK=off go test ./internal/server/ -run TestPublishedRecallSnippetLimit -count=1 -v

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// getMemoryCardForSnippet is where the 800 is published. The pf_get_memory card
// rather than pf_recall's, because it is pf_get_memory's own justification: the
// flags are useless without a by-id read, so the card that describes the read
// states the limit.
const getMemoryCardForSnippet = "../../docs/mcp-cards/pf_get_memory.md"

// TestPublishedRecallSnippetLimitIsTheEnforcedOne binds the published snippet
// length and the two flag names to what handleRecall really does.
//
// 🔴 The expected value is READ from the card, never written here. §3.4 states
// why with the base-strength precedent: an arm carrying its own copy of the
// number is green on the day the constant moves, which is the one day it was
// needed. Both directions fail — the constant moving without the card, and the
// card moving without the constant.
//
// Mutants, all applied to the tree and run (2026-09-10):
//
//	M1  recallContentMax becomes 400                                  RED (number)
//	M2  the truncation block stops setting ContentTruncated           RED (flags)
//	M3  the truncation block stops recording ContentFullLen           RED (flags)
//	M4  the card publishes "400 runes"                                RED (publication)
//	M5  the card renames content_full_len                             RED (publication)
//	M6  green control: reword the sentence around the same number
//	    and the same two flag names                                   GREEN
func TestPublishedRecallSnippetLimitIsTheEnforcedOne(t *testing.T) {
	raw, err := os.ReadFile(getMemoryCardForSnippet)
	if err != nil {
		t.Fatalf("read %s: %v — the card is the published half of this claim, so an unreadable "+
			"card is a failure and not an empty pass", getMemoryCardForSnippet, err)
	}
	card := strings.Join(strings.Fields(string(raw)), " ")

	// ── the number ───────────────────────────────────────────────────────────
	want := fmt.Sprintf("truncates content to %d runes", recallContentMax)
	if !strings.Contains(card, want) {
		t.Errorf("%s does not publish the snippet limit as %q.\nThe expectation is built from "+
			"recallContentMax, so this fires in both directions: the constant moved and the card "+
			"did not, or the card was reworded and the constant was not. A caller told one number "+
			"and handed another sizes every follow-up read wrongly, and no field in the response "+
			"reports the limit that was applied.", getMemoryCardForSnippet, want)
	}

	// ── the flag names, against the fields that carry them ───────────────────
	memFields := map[string]bool{}
	mt := reflect.TypeOf(domain.Memory{})
	for i := 0; i < mt.NumField(); i++ {
		if tag := strings.Split(mt.Field(i).Tag.Get("json"), ",")[0]; tag != "" && tag != "-" {
			memFields[tag] = true
		}
	}
	flags := []string{"content_truncated", "content_full_len"}
	for _, flag := range flags {
		if !strings.Contains(card, "`"+flag+"`") {
			t.Errorf("%s no longer names `%s`. The two flags together ARE the escape hatch this "+
				"tool completes: one says the body is short, the other says by how much, and a "+
				"card naming only the first describes a signal a caller cannot act on.",
				getMemoryCardForSnippet, flag)
		}
		if !memFields[flag] {
			t.Errorf("the card promises a %q field and domain.Memory publishes no such json name. "+
				"A flag nothing serialises cannot reach the model, so the promise is unkeepable "+
				"whatever the handler does.", flag)
		}
	}

	// ── what the truncation block actually assigns ───────────────────────────
	assigned := recallTruncationAssignments(t)
	if len(assigned) == 0 {
		t.Fatalf("no assignment guarded by recallContentMax was found in this package, so the " +
			"handler either stopped truncating — which makes the card's whole first clause false " +
			"— or the walk broke, and either way the assertions below would report nothing")
	}
	for _, wantField := range []string{"Content", "ContentTruncated", "ContentFullLen"} {
		if !assigned[wantField] {
			t.Errorf("the recall truncation block does not assign %s (it assigns %v).\n"+
				"All three are one act: shortening the body without setting the flags is "+
				"aihub#269 exactly — the model reasons on a prefix believing it is the whole "+
				"memory — and setting the flags without recording the true length leaves a "+
				"caller unable to tell whether the by-id read is worth a round trip.",
				wantField, sortedKeys(assigned))
		}
	}
}

// recallTruncationAssignments returns the Memory field names assigned inside the
// if-block that compares a length against recallContentMax.
//
// Keyed on the CONSTANT rather than on a function or line number: the block has
// moved between handlers before, and rowserr's Loop.Key argument applies — a key
// that moves when anything above it is edited stops matching silently, and a
// silent miss here reads as "the handler no longer truncates".
func recallTruncationAssignments(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ifStmt, ok := n.(*ast.IfStmt)
			if !ok {
				return true
			}
			guardsOnLimit := false
			ast.Inspect(ifStmt.Cond, func(inner ast.Node) bool {
				if id, isID := inner.(*ast.Ident); isID && id.Name == "recallContentMax" {
					guardsOnLimit = true
				}
				return true
			})
			if !guardsOnLimit {
				return true
			}
			ast.Inspect(ifStmt.Body, func(inner ast.Node) bool {
				assign, isAssign := inner.(*ast.AssignStmt)
				if !isAssign {
					return true
				}
				for _, lhs := range assign.Lhs {
					if sel, isSel := lhs.(*ast.SelectorExpr); isSel {
						out[sel.Sel.Name] = true
					}
				}
				return true
			})
			return true
		})
	}
	return out
}
