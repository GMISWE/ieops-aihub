package domain

import "testing"

// TestActivationTargetStatus locks in the two reasons an activated memory is
// NOT revived to active:
//
//   - aihub#214: an archived methodology.* artifact stays archived, so a
//     read-side recall signal can't resurrect a stale spec/plan head.
//   - aihub#175 finding 3: ANY archived row that already has a successor stays
//     archived, whatever its type. Remember archives the old head on every
//     supersede, so before this guard an experience.*/fact.*/rule.* old version
//     could be flipped back to active alongside the chain head — two active
//     rows in one lineage, which makes orderVersionChain report two IsCurrent
//     entries.
//
// The `superseded=false` rows are what keeps this a guard rather than a blanket
// "archived stays archived": a genuinely decayed memory with no successor must
// still revive.
func TestActivationTargetStatus(t *testing.T) {
	cases := []struct {
		status, typ string
		superseded  bool
		want        string
	}{
		// aihub#214: archived methodology.* stays archived either way.
		{"archived", "methodology.spec", false, "archived"},
		{"archived", "methodology.plan", false, "archived"},
		{"archived", "methodology.review", false, "archived"},
		{"archived", "methodology.spec", true, "archived"},

		// aihub#175: a superseded archived row stays archived for EVERY type.
		{"archived", "experience.debug", true, "archived"},
		{"archived", "fact.note", true, "archived"},
		{"archived", "rule.process", true, "archived"},

		// Not superseded and not methodology.* — decayed, so it still revives.
		{"archived", "experience.debug", false, "active"},
		{"archived", "fact.note", false, "active"},
		{"archived", "rule.process", false, "active"},

		// Already-active rows are untouched by either rule.
		{"active", "methodology.spec", false, "active"},
		{"active", "experience.code", false, "active"},
		{"active", "experience.code", true, "active"},
	}
	for _, c := range cases {
		if got := activationTargetStatus(c.status, c.typ, c.superseded); got != c.want {
			t.Errorf("activationTargetStatus(%q, %q, %v) = %q, want %q",
				c.status, c.typ, c.superseded, got, c.want)
		}
	}
}
