package mcp_test

// aihub#474 — the goal cap a caller is TOLD about must be the goal cap the
// server enforces, on both tools that write the column.
//
// This is the bridge test aihub#433 established for base_strength, applied to
// the other pair that had drifted. Two things had to be true and neither was:
//
//  1. pf_update_work_item's `goal` description stated the status gate and NOTHING
//     about shape, while pf_create_work_item's stated both constraints. A caller
//     reading the pair — which is the intended use, since the two tools write the
//     same column — reasonably concluded the cap was create-only. It was, and
//     that was the bug, not the documentation.
//  2. The number itself was TYPED into the description. So was the one in the
//     migration, and so was the one in CreateWorkItem, and the whole reason
//     aihub#434 gave the constant a name is that three hand-typed copies of a
//     limit are three chances for the published promise to outlive the check.
//
// So the assertions below are anchored on domain.MaxWorkItemGoalRunes() rather
// than on the literal 500, which is what gives them something to say in both
// directions: move the constant without retyping the descriptions and they fail,
// retype a description back to a number the server no longer enforces and they
// fail too. A test that grepped for "500" would go green on exactly the day the
// bound changed — the failure this file exists to make impossible.
//
// ⚠️ This gate is about the PUBLISHED text. That the descriptions are true of the
// running code is a separate claim, pinned where the enforcement lives
// (internal/domain/work_item_goal_shape_test.go). Both are needed: this one alone
// would pass on a build that published a perfect description and enforced
// nothing, which is the state pf_update_work_item was in in the other direction.

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// TestPublishedGoalCapIsTheEnforcedOne checks every tool that publishes a `goal`
// parameter against the shape domain enforces: the cap
// (domain.MaxWorkItemGoalRunes()), the newline refusal, and non-emptiness.
//
// It quantifies over the live tool list rather than over a hand-written pair of
// names on purpose: pf_batch_create_work_items publishes the same field through
// the shared workItemFieldProps, and a third tool that grows a `goal` is exactly
// the case a two-name list would silently skip.
//
// The non-emptiness arm arrived here by that route. aihub#507 shipped it as a
// test named for pf_update_work_item alone, because at that moment only that
// tool's description carried the word and the loop below would have failed on the
// other two — a red gate for a gap the work item had deliberately not closed. Its
// doc comment said to fold it in here rather than leave a second named test if
// the create side ever stated it too, and aihub#520 stated it. Three descriptions
// asserted by one loop is what stops them drifting apart again; two tests, one
// quantified and one named, is a shape where the named tool is the only one that
// cannot silently lose the word.
func TestPublishedGoalCapIsTheEnforcedOne(t *testing.T) {
	limit := domain.MaxWorkItemGoalRunes()
	want := "≤" + strconv.Itoa(limit) + " chars"

	descriptions := publishedGoalDescriptions(t)
	// aihub#474 measured three: pf_create_work_item, pf_update_work_item and
	// pf_batch_create_work_items (whose `items` entries carry the field). A drop
	// below that means the walk stopped finding them, and every assertion in the
	// loop would be vacuously true.
	if len(descriptions) < 2 {
		t.Fatalf("only %d published `goal` description(s) found (%v); at least "+
			"pf_create_work_item and pf_update_work_item publish one, so this walk is broken "+
			"and the loop below would assert nothing", len(descriptions), toolNames(descriptions))
	}

	for tool, desc := range descriptions {
		if !strings.Contains(desc, want) {
			t.Errorf("%s publishes goal as %q, which never states the enforced cap (%s). "+
				"pf_create_work_item and pf_update_work_item write the SAME column through the "+
				"same CHECK; a caller who reads one description and calls the other is the "+
				"reader aihub#474 was filed by.", tool, desc, want)
		}
		if !strings.Contains(strings.ToLower(desc), "single-line") {
			t.Errorf("%s publishes goal as %q, which states the length but not that a newline "+
				"is refused. Both halves are one CHECK (`length(goal) <= %d AND goal !~ "+
				"E'[\\n\\r]'`) and both are enforced in Go; publishing one of them is how the "+
				"update tool came to document neither.", tool, desc, limit)
		}
		// aihub#507's arm, quantified by aihub#520. `pf_update_work_item {goal: ""}`
		// used to be STORED — 200, and a work item that renders blank everywhere —
		// while pf_create_work_item answered the identical value with 400 "goal is
		// required". domain.validateWorkItemGoalPresent now refuses it on both
		// paths. A newly reachable refusal that no published text mentions is
		// discovered by being hit, which is the §6.1 T1-9 failure mode with the
		// sign flipped: not prose outliving its behaviour, but behaviour arriving
		// without prose. And on the create paths the refusal was never new at all —
		// only unstated, because JSON-Schema `required` means "must be present",
		// not "must be non-empty", so `goal: ""` satisfied the published schema and
		// took the 400 regardless.
		if !strings.Contains(strings.ToLower(desc), "non-empty") {
			t.Errorf("%s publishes goal as %q, which does not say the value must be non-empty. "+
				"domain.validateWorkItemGoalPresent refuses \"\" with 400 \"goal is required\" on "+
				"every path that writes this column. `required` in the published schema means "+
				"the property must be PRESENT, so a caller reading it cannot learn that the "+
				"empty string is refused — it learns by sending one.", tool, desc)
		}
		// The aihub#433 arm: a description that states the cap AND some other
		// number states two caps, and the reader cannot tell which is enforced.
		// Every integer in the text must be the enforced one.
		for _, n := range integersIn(desc) {
			if n != strconv.Itoa(limit) {
				t.Errorf("%s publishes goal as %q, which names the number %s — the enforced cap "+
					"is %d. A published limit disjoint from the enforced one reads as true and "+
					"is not; that is the aihub#433 failure mode, where a description survived "+
					"the bound it described.", tool, desc, n, limit)
			}
		}
	}
}

// publishedGoalDescriptions returns, per tool, the description of the `goal`
// property as the live SDK session publishes it.
//
// It reads the schema off a real session for the reason liveInputSchemaHashes
// gives: cli.RunDumpMCPSchemas' contract JSON carries no per-property
// descriptions, so it cannot see the very string this test is about.
//
// The walk is recursive because pf_batch_create_work_items nests the field under
// `items`' entry schema rather than at the top level — the shared definition
// aihub#290 introduced so a field could not exist on one create tool and not the
// other. A top-level-only walk would report that tool as having no `goal` and
// pass by saying nothing about it.
func publishedGoalDescriptions(t *testing.T) map[string]string {
	t.Helper()
	_, tools := newContractGate(t)

	out := map[string]string{}
	for _, tool := range tools {
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal %s InputSchema: %v", tool.Name, err)
		}
		var decoded any
		if err := json.Unmarshal(b, &decoded); err != nil {
			t.Fatalf("re-decode %s InputSchema: %v", tool.Name, err)
		}
		if desc, ok := findGoalDescription(decoded); ok {
			out[tool.Name] = desc
		}
	}
	return out
}

// findGoalDescription looks for a `properties.goal.description` anywhere in a
// decoded JSON schema and returns the first one it finds.
func findGoalDescription(node any) (string, bool) {
	obj, ok := node.(map[string]any)
	if !ok {
		if arr, ok := node.([]any); ok {
			for _, el := range arr {
				if d, found := findGoalDescription(el); found {
					return d, true
				}
			}
		}
		return "", false
	}

	if props, ok := obj["properties"].(map[string]any); ok {
		if goal, ok := props["goal"].(map[string]any); ok {
			if desc, ok := goal["description"].(string); ok {
				return desc, true
			}
		}
	}
	for _, v := range obj {
		if d, found := findGoalDescription(v); found {
			return d, true
		}
	}
	return "", false
}

// integersIn returns every maximal run of digits in s.
func integersIn(s string) []string {
	var out []string
	var cur strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) {
			cur.WriteRune(r)
			continue
		}
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// toolNames is for the failure message only. (keysOf is taken in this package.)
func toolNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
