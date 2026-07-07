package domain

import "testing"

// TestActivationTargetStatus locks in aihub#214: activation revives archived
// memories to active EXCEPT a superseded methodology.* artifact, which stays
// archived so a read-side recall signal can't resurrect a stale spec/plan head.
func TestActivationTargetStatus(t *testing.T) {
	cases := []struct{ status, typ, want string }{
		{"archived", "methodology.spec", "archived"},   // superseded spec stays archived
		{"archived", "methodology.plan", "archived"},
		{"archived", "methodology.review", "archived"},
		{"archived", "experience.debug", "active"},      // non-methodology still revives
		{"archived", "fact.note", "active"},
		{"archived", "rule.process", "active"},
		{"active", "methodology.spec", "active"},         // already-active untouched
		{"active", "experience.code", "active"},
	}
	for _, c := range cases {
		if got := activationTargetStatus(c.status, c.typ); got != c.want {
			t.Errorf("activationTargetStatus(%q, %q) = %q, want %q", c.status, c.typ, got, c.want)
		}
	}
}
