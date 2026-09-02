package domain

// aihub#314, producer half: what appendIntAdjustment records, and what the
// response looks like on the wire when it records nothing.
//
// These are pure and cheap, and they are NOT where this work item is decided —
// aihub#309 measured a mutation one layer away from its defect leaving four pure
// domain tests green while the defect stood. The decisions live in
// internal/mcp/request_adjusted_e2e_db_test.go, against a real server. What is
// pinned here is the one thing a transport test cannot show: the JSON SHAPE of
// the empty case, which is an absence, and an absence is invisible end to end
// unless something states what it should be.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAppendIntAdjustment_RecordsOnlyRealAdjustments(t *testing.T) {
	cases := []struct {
		name               string
		requested, applied int
		want               bool
		why                string
	}{
		{"clamped above the ceiling", 500, 200, true,
			"the caller asked for 500 and got 200; without the entry that is indistinguishable from a 200-row corpus"},
		{"reset from a negative", -3, 20, true,
			"a negative arrived intact, so it is attributable to the caller and its replacement is disclosable"},
		{"honoured verbatim", 30, 30, false,
			"nothing happened; an entry here would train the reader to skip the field"},
		{"absent, i.e. the int zero", 0, 20, false,
			"0 is both 'sent nothing' and 'sent 0' at this layer, so claiming requested:0 would invent a request"},
		{"absent with the list default", 0, 50, false, "same, on the work-items path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := appendIntAdjustment(nil, "top_k", c.requested, c.applied)
			if (len(got) == 1) != c.want {
				t.Fatalf("appendIntAdjustment(nil, top_k, %d, %d) = %#v, want recorded=%v — %s",
					c.requested, c.applied, got, c.want, c.why)
			}
			if !c.want {
				return
			}
			if got[0].Param != "top_k" || got[0].Requested != c.requested || got[0].Applied != c.applied {
				t.Errorf("entry = %#v, want {top_k %d %d}", got[0], c.requested, c.applied)
			}
		})
	}
}

// TestAppendIntAdjustment_Appends pins that the field is a LIST that accumulates,
// which is the property that makes it generic: the second clamp to want
// disclosing must cost nothing but a call, or the incentive this work item exists
// to fix comes straight back.
func TestAppendIntAdjustment_Appends(t *testing.T) {
	got := appendIntAdjustment(appendIntAdjustment(nil, "limit", 500, 50), "top_k", 500, 200)
	if len(got) != 2 || got[0].Param != "limit" || got[1].Param != "top_k" {
		t.Fatalf("appending a second adjustment gave %#v, want both, in order", got)
	}
}

// TestRequestAdjusted_AbsentKeyWhenNothingWasAdjusted is the shape decision from
// request_adjusted.go, asserted on the bytes rather than on the struct.
//
// The rule inherited from aihub#278 is that a key may be omitted only when its
// value is null. An empty adjustment list is that case — it carries nothing the
// absence does not — so `omitempty` is a null-gated drop and is allowed. A
// NON-empty list is a real value and must never be omitted by anything, which is
// the half the rule protects and the half asserted second here.
func TestRequestAdjusted_AbsentKeyWhenNothingWasAdjusted(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    any
	}{
		{"recall", &RecallResponse{Items: []MemoryWithStrength{}, Total: 3}},
		{"list work items", &ListWorkItemsResult{Items: []*WorkItem{}}},
	} {
		b, err := json.Marshal(tc.v)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		if strings.Contains(string(b), "request_adjusted") {
			t.Errorf("%s: an unadjusted response spells the key out: %s\n"+
				"absent and [] say the same thing, and the healthy call must pay zero tokens for it",
				tc.name, b)
		}
	}

	adjusted := &RecallResponse{
		Items:           []MemoryWithStrength{},
		RequestAdjusted: appendIntAdjustment(nil, "top_k", 500, 200),
	}
	b, err := json.Marshal(adjusted)
	if err != nil {
		t.Fatalf("marshal adjusted: %v", err)
	}
	const want = `"request_adjusted":[{"param":"top_k","requested":500,"applied":200}]`
	if !strings.Contains(string(b), want) {
		t.Errorf("adjusted response = %s\nwant it to contain %s", b, want)
	}
}
