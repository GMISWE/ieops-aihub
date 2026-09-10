package mcp

// aihub#591 — pf_recall's projection-identity sentence, pinned: slimRecallResult
// "mutates the incoming map and returns the same map — nothing is copied at the
// top level or per item, so nothing can be forgotten there."
//
// The property is the card's whole argument for why the keep-list bugs (`total`,
// then the truncation pair, each dropped silently) cannot recur: a delete-list
// over the SAME map cannot forget a key it never knew about. Nothing asserted
// the identity itself — a refactor to `out := maps.Clone(result)` would keep
// every existing projection test green while reopening exactly the forgotten-key
// class the sentence promises is closed.

import (
	"reflect"
	"testing"
)

func TestSlimRecallResultMutatesAndReturnsTheSameMap(t *testing.T) {
	in := map[string]any{
		"items": []any{
			map[string]any{"id": "mem_1", "visibility": "team", "content": "x"},
		},
		"total":            1,
		"unknown_envelope": "must survive by identity, not by being copied",
	}
	itemsBefore := in["items"]

	got := slimRecallResultMode(in, false)

	if reflect.ValueOf(got).Pointer() != reflect.ValueOf(in).Pointer() {
		t.Fatalf("slimRecallResultMode returned a DIFFERENT map. The card's claim — and the "+
			"reason the total/truncation-pair bugs cannot recur — is that the projection "+
			"mutates the caller's own map, so an envelope key it has never heard of cannot "+
			"be forgotten. A copy reopens the keep-list failure class: got %p, in %p",
			got, in)
	}
	if got["unknown_envelope"] != "must survive by identity, not by being copied" {
		t.Errorf("the unknown envelope key did not survive: %v", got["unknown_envelope"])
	}
	if reflect.ValueOf(got["items"]).Pointer() == reflect.ValueOf(itemsBefore).Pointer() {
		// items IS rebuilt (each item is projected); this control documents that the
		// identity claim is about the ENVELOPE map, which is where the historical
		// losses happened — not about the items slice the projection legitimately
		// replaces. If this ever flips, re-read the card sentence before "fixing" it.
		t.Logf("note: items slice was reused; the envelope identity above still holds")
	}
}
