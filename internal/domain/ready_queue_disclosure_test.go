package domain

// aihub#432 / aihub#411 T1-12 — GET /v1/work_items/ready discloses the page size
// it clamped, closing request_adjusted's one self-declared exemption.
//
// What was here before was a comment inside GetReadyQueue admitting that
// `max=5000` and `max=200` return byte-identical responses: the clamp obeyed
// Rule 2's "clamp to the ceiling" half and not its "and say so" half, because
// ReadyQueue had no field to say it in. The comment was honest and it was still
// the defect — a caller cannot act on a limit it is never told about, and the
// two other list endpoints already tell it.
//
// ─── Why these tests do not call GetReadyQueue ──────────────────────────────
//
// It needs a *pgxpool.Pool and CI never provides one: .github/workflows/ci.yml
// deliberately leaves AIHUB_TEST_DB unset, so every AIHUB_TEST_DB-gated test
// SKIPs there. A disclosure gated only by a DB test would be gated by nothing on
// the machine that decides whether a change lands.
//
// So the clamp and its disclosure live in ONE constructor that needs no
// database, the behavioural tests below drive that constructor for real, and the
// source-level test at the bottom closes the gap that split leaves: it asserts
// GetReadyQueue actually builds its response through the constructor, and
// nothing else. That third test is the one that keeps the first two from
// becoming "a helper that is correct and unused" — the exact failure the header
// of ready_queue_items_db_test.go describes for buildReadyQueueItemsQuery.

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestReadyQueueDisclosesTheMaxItAdjusted drives the constructor over the four
// classes queryparam.go's Rule 2 distinguishes.
func TestReadyQueueDisclosesTheMaxItAdjusted(t *testing.T) {
	for _, tc := range []struct {
		name        string
		requested   int
		wantApplied int
		wantEntries []RequestAdjustment
	}{
		{
			// Rule 2 proper: a coherent request the server will not serve in
			// full. Clamped to the CEILING (not back to the default) and
			// reported — this is the case the old comment named.
			name:      "above the ceiling is clamped and disclosed",
			requested: 5000, wantApplied: 200,
			wantEntries: []RequestAdjustment{{Param: "max", Requested: 5000, Applied: 200}},
		},
		{
			name:      "one over the ceiling is still an adjustment",
			requested: 201, wantApplied: 200,
			wantEntries: []RequestAdjustment{{Param: "max", Requested: 201, Applied: 200}},
		},
		{
			// Exactly at the ceiling nothing happened, and reporting it would
			// train the reader to skip the field (request_adjusted.go).
			name: "at the ceiling nothing is disclosed", requested: 200, wantApplied: 200,
		},
		{
			name: "an ordinary page size is untouched", requested: 25, wantApplied: 25,
		},
		{
			// Not malformed and not over a limit: "the caller named no page
			// size", which yields the endpoint default. A negative DID arrive,
			// so the substitution is disclosed.
			name:      "a negative gets the default, and that is disclosed",
			requested: -5, wantApplied: 10,
			wantEntries: []RequestAdjustment{{Param: "max", Requested: -5, Applied: 10}},
		},
		{
			// The zero-value/absent ambiguity appendIntAdjustment refuses to
			// guess at: handleGetReadyQueue forwards queryInt's value without
			// its present flag, so `max=0` and no `max` at all are the same int
			// by the time they arrive. Claiming `requested: 0` would invent a
			// request the caller may never have made.
			name:      "zero is indistinguishable from absent and is not disclosed",
			requested: 0, wantApplied: 10,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rq, applied := newReadyQueue(tc.requested)
			if applied != tc.wantApplied {
				t.Errorf("newReadyQueue(%d) applied max=%d, want %d", tc.requested, applied, tc.wantApplied)
			}
			if len(rq.RequestAdjusted) != len(tc.wantEntries) {
				t.Fatalf("newReadyQueue(%d) disclosed %#v, want %#v",
					tc.requested, rq.RequestAdjusted, tc.wantEntries)
			}
			for i, want := range tc.wantEntries {
				got := rq.RequestAdjusted[i]
				if got.Param != want.Param || got.Requested != want.Requested || got.Applied != want.Applied {
					t.Errorf("entry %d = %#v, want %#v", i, got, want)
				}
			}
			// The bounded value is what the SQL must page with; a constructor
			// that disclosed 200 while handing back 5000 would be worse than
			// the silence it replaces.
			if applied <= 0 || applied > 200 {
				t.Errorf("newReadyQueue(%d) returned an out-of-bounds page size %d", tc.requested, applied)
			}
		})
	}
}

// TestReadyQueueOmitsTheDisclosureKeyWhenNothingWasAdjusted pins the SHAPE, which
// aihub#314 decided and aihub#411 T1-12 re-ratified: the key is ABSENT rather
// than an empty list.
//
// Both spellings say "nothing about your request was changed", so an empty list
// carries no information the absence does not. The cost of the choice is that
// absence also means "this server predates the field", and that is acceptable
// only while an absent request_adjusted asserts NOTHING. This test is where that
// stays true: it asserts the key is missing, not that it is empty, so a future
// change that gave absence a meaning would have to come through here.
func TestReadyQueueOmitsTheDisclosureKeyWhenNothingWasAdjusted(t *testing.T) {
	unadjusted, _ := newReadyQueue(25)
	body, err := json.Marshal(unadjusted)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw, present := decoded["request_adjusted"]; present {
		t.Errorf("an unadjusted ready queue carries request_adjusted=%s; the key must be ABSENT, "+
			"not an empty list — see internal/domain/request_adjusted.go", raw)
	}

	adjusted, _ := newReadyQueue(5000)
	body, err = json.Marshal(adjusted)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	decoded = map[string]json.RawMessage{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	raw, present := decoded["request_adjusted"]
	if !present {
		t.Fatalf("a clamped ready queue omitted request_adjusted entirely: %s", body)
	}
	// The key name is the contract: pf_get_ready_queue hands this body to the
	// model untouched (jsonResult, no projection), and a caller that already
	// reads request_adjusted on /v1/work_items and /v1/memories must not have to
	// learn a second name for the same thing.
	var entries []RequestAdjustment
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("request_adjusted is not the shared [{param,requested,applied}] shape: %s (%v)", raw, err)
	}
	if len(entries) != 1 || entries[0].Param != "max" {
		t.Errorf("request_adjusted = %s, want one entry naming max", raw)
	}
}

// TestGetReadyQueueBuildsItsResponseThroughTheDisclosingConstructor is the
// source-level half, and it exists because the two tests above cannot fail for
// the one reason that matters most: GetReadyQueue not calling the constructor at
// all.
//
// Inspecting a helper proves nothing about the function that is supposed to use
// it — ready_queue_items_db_test.go's header says exactly this about
// buildReadyQueueItemsQuery, which an earlier wi "proved" by inspection while
// nothing forced GetReadyQueue to call it. The difference here is the subject:
// this test reads GetReadyQueue's OWN body and asserts three things about it,
// two of them negative, so re-inlining a clamp is caught rather than tolerated.
func TestGetReadyQueueBuildsItsResponseThroughTheDisclosingConstructor(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "work_items.go", nil, 0)
	if err != nil {
		t.Fatalf("parse work_items.go: %v", err)
	}
	var body *ast.BlockStmt
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Name.Name == "GetReadyQueue" && fn.Recv == nil {
			body = fn.Body
		}
		return body == nil
	})
	if body == nil {
		t.Fatal("GetReadyQueue not found in work_items.go — this test cannot be green by not finding its subject")
	}

	constructorCalls, literals := 0, 0
	var strayMaxAssignments []string
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "newReadyQueue" {
				constructorCalls++
			}
		case *ast.CompositeLit:
			if id, ok := v.Type.(*ast.Ident); ok && id.Name == "ReadyQueue" {
				literals++
			}
		case *ast.AssignStmt:
			// Assigning to `max` is how the clamp used to be written. The one
			// legal form is taking it back from the constructor, which is the
			// only place that also produces the disclosure.
			for _, lhs := range v.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || id.Name != "max" {
					continue
				}
				fromConstructor := false
				if len(v.Rhs) == 1 {
					if call, ok := v.Rhs[0].(*ast.CallExpr); ok {
						if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == "newReadyQueue" {
							fromConstructor = true
						}
					}
				}
				if !fromConstructor {
					strayMaxAssignments = append(strayMaxAssignments, fset.Position(v.Pos()).String())
				}
			}
		}
		return true
	})

	if constructorCalls != 1 {
		t.Errorf("GetReadyQueue calls newReadyQueue %d times, want exactly 1 — the clamp and the "+
			"disclosure it produces are one decision and must have one site", constructorCalls)
	}
	if literals != 0 {
		t.Errorf("GetReadyQueue builds %d ReadyQueue literal(s) of its own; a response built "+
			"outside newReadyQueue carries no request_adjusted and re-opens aihub#411 T1-12", literals)
	}
	for _, pos := range strayMaxAssignments {
		t.Errorf("%s: GetReadyQueue reassigns `max` outside newReadyQueue. A bound applied here is "+
			"a bound nothing discloses — that is the half of Rule 2 aihub#432 closed.", pos)
	}
}
