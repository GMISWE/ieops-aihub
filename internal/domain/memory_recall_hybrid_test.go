package domain

import (
	"fmt"
	"strings"
	"testing"
)

// aihub#270 — the vector recall path's WHERE carries `emb_vector IS NOT NULL`, so it can
// only ever return rows whose type is embeddable. Before the fix, a request whose type
// union mixed embeddable and non-embeddable types short-circuited on the non-empty vector
// half and dropped the non-embeddable types entirely, with no error and no warning.
//
// These tests cover the pure helpers that make the split-and-merge correct. The router
// itself and the SQL it builds are covered against a real pgvector Postgres in
// memory_recall_router_db_test.go (CI-executable, gated on AIHUB_TEST_DB).

// TestPartitionTypesByEmbeddable covers the classification that decides which path owns
// which half of a request.
func TestPartitionTypesByEmbeddable(t *testing.T) {
	tests := []struct {
		name       string
		types      []string
		wantEmb    []string
		wantNonEmb []string
	}{
		{
			name: "no filter — both halves empty, caller means every type",
		},
		{
			// The exact union polyforge 1.1.8's pf-spec/pf-plan Step 1 sends.
			name:       "the pf-spec union that regressed",
			types:      []string{"methodology.spec", "methodology.plan", "fact.*", "rule.*", "experience.*"},
			wantEmb:    []string{"fact.*", "rule.*", "experience.*"},
			wantNonEmb: []string{"methodology.spec", "methodology.plan"},
		},
		{
			name:       "pure methodology — nothing for the vector path to serve",
			types:      []string{"methodology.spec", "methodology.plan"},
			wantNonEmb: []string{"methodology.spec", "methodology.plan"},
		},
		{
			name:    "pure embeddable — no complement needed",
			types:   []string{"experience.*", "rule.*"},
			wantEmb: []string{"experience.*", "rule.*"},
		},
		{
			name:    "wildcards classify by prefix, same as concrete types",
			types:   []string{"experience.*", "experience.pitfall"},
			wantEmb: []string{"experience.*", "experience.pitfall"},
		},
		{
			// A bare "rule" would never have been embedded either (embeddableType wants the
			// dot), so the text side is the correct owner.
			name:       "bare prefix without a dot lands on the text side",
			types:      []string{"rule", "fact"},
			wantNonEmb: []string{"rule", "fact"},
		},
		{
			name:       "unknown types land on the text side",
			types:      []string{"unknown.type", ""},
			wantNonEmb: []string{"unknown.type", ""},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotEmb, gotNonEmb := partitionTypesByEmbeddable(tc.types)
			if !equalStrings(gotEmb, tc.wantEmb) {
				t.Errorf("embeddable = %v, want %v", gotEmb, tc.wantEmb)
			}
			if !equalStrings(gotNonEmb, tc.wantNonEmb) {
				t.Errorf("nonEmbeddable = %v, want %v", gotNonEmb, tc.wantNonEmb)
			}
			// Every input entry must land in exactly one half — a lost entry is a silently
			// dropped filter, which is the bug class this whole change is about.
			if len(gotEmb)+len(gotNonEmb) != len(tc.types) {
				t.Errorf("partition lost entries: %d + %d != %d", len(gotEmb), len(gotNonEmb), len(tc.types))
			}
		})
	}
}

// TestPartitionTypesByEmbeddableAgreesWithEmbeddableType pins the invariant the whole fix
// rests on: an entry is routed to the vector path if and only if rows matching it are
// actually embedded. If EmbeddablePrefixes ever changes, both sides move together.
func TestPartitionTypesByEmbeddableAgreesWithEmbeddableType(t *testing.T) {
	concrete := []string{
		"experience.debug", "experience.pitfall", "fact.architecture", "fact.note",
		"rule.process", "rule.work",
		"methodology.spec", "methodology.plan", "methodology.review",
		"methodology.execute", "methodology.retro", "methodology.wrap_summary",
		"unknown.type", "",
	}
	emb, nonEmb := partitionTypesByEmbeddable(concrete)
	for _, ty := range emb {
		if !embeddableType(ty) {
			t.Errorf("%q routed to the vector path but embeddableType says it is never embedded", ty)
		}
	}
	for _, ty := range nonEmb {
		if embeddableType(ty) {
			t.Errorf("%q routed to the text complement but embeddableType says it IS embedded", ty)
		}
	}
}

// TestNonEmbeddableTypeClause verifies the SQL complement: one bound placeholder per
// embeddable prefix, ANDed negations, and a correctly advanced placeholder index.
func TestNonEmbeddableTypeClause(t *testing.T) {
	const startIdx = 5
	clause, args, nextIdx := nonEmbeddableTypeClause(startIdx)

	if len(args) != len(EmbeddablePrefixes) {
		t.Fatalf("got %d args, want one per embeddable prefix (%d)", len(args), len(EmbeddablePrefixes))
	}
	if nextIdx != startIdx+len(EmbeddablePrefixes) {
		t.Errorf("nextIdx = %d, want %d", nextIdx, startIdx+len(EmbeddablePrefixes))
	}

	// Each prefix must appear as a LIKE pattern, bound — never inlined into the SQL.
	for i, p := range EmbeddablePrefixes {
		want := p + "%"
		if args[i] != any(want) {
			t.Errorf("args[%d] = %v, want %q", i, args[i], want)
		}
		if strings.Contains(clause, p) {
			t.Errorf("clause inlines the literal prefix %q instead of binding it: %s", p, clause)
		}
		placeholder := fmt.Sprintf("$%d", startIdx+i)
		if !strings.Contains(clause, "type NOT LIKE "+placeholder) {
			t.Errorf("clause missing %q: %s", "type NOT LIKE "+placeholder, clause)
		}
	}

	// NOT (a OR b OR c) must be expanded as NOT a AND NOT b AND NOT c — an OR here would
	// match every row instead of excluding the embeddable ones.
	if strings.Contains(clause, " OR ") {
		t.Errorf("clause uses OR between negations, which matches everything: %s", clause)
	}
	if got := strings.Count(clause, " AND "); got != len(EmbeddablePrefixes)-1 {
		t.Errorf("got %d AND separators, want %d: %s", got, len(EmbeddablePrefixes)-1, clause)
	}
}

// TestMergeRecallHalves covers the budget split that keeps the text half from being
// starved by the vector half.
func TestMergeRecallHalves(t *testing.T) {
	tests := []struct {
		name     string
		vec      []string
		txt      []string
		topK     int
		wantIDs  []string
		vecTotal int
		txtTotal int
	}{
		{
			// The regression, as a unit test: 8 vector hits and 8 methodology hits with a
			// budget of 8. Concatenating would return zero methodology rows.
			name:    "text half is never starved by a full vector half",
			vec:     []string{"v1", "v2", "v3", "v4", "v5", "v6", "v7", "v8"},
			txt:     []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8"},
			topK:    8,
			wantIDs: []string{"v1", "t1", "v2", "t2", "v3", "t3", "v4", "t4"},
		},
		{
			name:    "short text half — vector takes the remaining budget",
			vec:     []string{"v1", "v2", "v3", "v4", "v5"},
			txt:     []string{"t1"},
			topK:    5,
			wantIDs: []string{"v1", "t1", "v2", "v3", "v4"},
		},
		{
			name:    "empty vector half returns the text half in order",
			txt:     []string{"t1", "t2", "t3"},
			topK:    5,
			wantIDs: []string{"t1", "t2", "t3"},
		},
		{
			name:    "empty text half returns the vector half in order",
			vec:     []string{"v1", "v2"},
			topK:    5,
			wantIDs: []string{"v1", "v2"},
		},
		{
			name:    "both halves empty",
			topK:    5,
			wantIDs: nil,
		},
		{
			// Disjoint by construction today, but the merge must not emit duplicates if a
			// future partition change lets a row appear on both sides.
			name:    "overlapping ids are deduped",
			vec:     []string{"a", "b"},
			txt:     []string{"a", "c"},
			topK:    4,
			wantIDs: []string{"a", "b", "c"},
		},
		{
			name:     "totals sum across the disjoint halves",
			vec:      []string{"v1"},
			txt:      []string{"t1"},
			topK:     8,
			wantIDs:  []string{"v1", "t1"},
			vecTotal: 40,
			txtTotal: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vec := &RecallResponse{Items: itemsWithIDs(tc.vec), Total: tc.vecTotal}
			txt := &RecallResponse{Items: itemsWithIDs(tc.txt), Total: tc.txtTotal}

			got := mergeRecallHalves(vec, txt, tc.topK)

			gotIDs := make([]string, len(got.Items))
			for i := range got.Items {
				gotIDs[i] = got.Items[i].ID
			}
			if !equalStrings(gotIDs, tc.wantIDs) {
				t.Errorf("ids = %v, want %v", gotIDs, tc.wantIDs)
			}
			if len(got.Items) > tc.topK {
				t.Errorf("returned %d items, exceeds topK %d", len(got.Items), tc.topK)
			}
			if want := tc.vecTotal + tc.txtTotal; got.Total != want {
				t.Errorf("Total = %d, want %d", got.Total, want)
			}
			// A merged page has no coherent cursor — the halves are ordered by
			// incomparable keys.
			if got.NextCursor != nil {
				t.Errorf("NextCursor = %q, want nil on a merged page", *got.NextCursor)
			}
		})
	}
}

// TestMergeRecallHalvesPreservesHalfOrdering checks that interleaving never reorders a
// half against itself — each path's own ranking must survive the merge.
func TestMergeRecallHalvesPreservesHalfOrdering(t *testing.T) {
	vec := &RecallResponse{Items: itemsWithIDs([]string{"v1", "v2", "v3"})}
	txt := &RecallResponse{Items: itemsWithIDs([]string{"t1", "t2", "t3"})}

	got := mergeRecallHalves(vec, txt, 6)

	var gotVec, gotTxt []string
	for _, it := range got.Items {
		if strings.HasPrefix(it.ID, "v") {
			gotVec = append(gotVec, it.ID)
		} else {
			gotTxt = append(gotTxt, it.ID)
		}
	}
	if !equalStrings(gotVec, []string{"v1", "v2", "v3"}) {
		t.Errorf("vector half reordered: %v", gotVec)
	}
	if !equalStrings(gotTxt, []string{"t1", "t2", "t3"}) {
		t.Errorf("text half reordered: %v", gotTxt)
	}
}

func itemsWithIDs(ids []string) []MemoryWithStrength {
	if len(ids) == 0 {
		return nil
	}
	items := make([]MemoryWithStrength, 0, len(ids))
	for _, id := range ids {
		items = append(items, MemoryWithStrength{Memory: Memory{ID: id}})
	}
	return items
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
