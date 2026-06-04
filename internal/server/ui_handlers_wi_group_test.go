package server

// Unit tests for groupListRows — the "status-blocks primary" grouping (Model A,
// aihub#129). Both Mine and All views group work items by STATUS into blocks,
// with two smart sections: "Needs you" (pinned first) and "Unclaimed" (pinned
// last). Every item lands in exactly one section.
//
// These exercise pure logic (no DB, no echo context): a flat []*wiListRow in,
// grouped []wiListGroup out.

import (
	"testing"
	"time"
)

// findGroup returns the group with the given label, or nil.
func findGroup(groups []wiListGroup, label string) *wiListGroup {
	for i := range groups {
		if groups[i].Label == label {
			return &groups[i]
		}
	}
	return nil
}

// findStatusGroup returns the status block with the given raw status, or nil.
func findStatusGroup(groups []wiListGroup, status string) *wiListGroup {
	for i := range groups {
		if groups[i].Kind == "status" && groups[i].Status == status {
			return &groups[i]
		}
	}
	return nil
}

func rowIDs(g *wiListGroup) []string {
	if g == nil {
		return nil
	}
	out := make([]string, 0, len(g.Rows))
	for _, r := range g.Rows {
		out = append(out, r.ID)
	}
	return out
}

// TestGroupListRows_Aihub4_QueuedHumanNoOwner_GoesToUnclaimed is the concrete
// regression: a queued + requires_human_session item with NO current-attempt
// owner must land in "Unclaimed", never "Needs you".
func TestGroupListRows_Aihub4_QueuedHumanNoOwner_GoesToUnclaimed(t *testing.T) {
	rows := []*wiListRow{
		{ID: "wi_4", Slug: "aihub#4", Status: "queued", NeedsHuman: true, OwnerDisplay: ""},
	}

	// Mine view, viewer "U".
	groups := groupListRows(rows, "U", true, nil)

	if g := findGroup(groups, "Needs you"); g != nil && len(g.Rows) > 0 {
		t.Errorf("aihub#4 must NOT appear under Needs you; got rows %v", rowIDs(g))
	}
	unc := findGroup(groups, "Unclaimed")
	if unc == nil {
		t.Fatalf("Unclaimed section missing entirely")
	}
	if got := rowIDs(unc); len(got) != 1 || got[0] != "wi_4" {
		t.Errorf("aihub#4 should be the sole Unclaimed row; got %v", got)
	}
}

// TestGroupListRows_MineView_OwnItemsFeedStatusBlocks: in Mine view the
// viewer's own items feed the status blocks by their OWN status (a running item
// I own -> Running block, a queued item I own -> Queued block), while paused +
// blocked items I own go to "Needs you". Items owned by others are dropped
// except the ownerless Unclaimed pool.
func TestGroupListRows_MineView_OwnItemsFeedStatusBlocks(t *testing.T) {
	rows := []*wiListRow{
		{ID: "mine_run", Status: "running", OwnerDisplay: "U"},
		{ID: "mine_queued", Status: "queued", OwnerDisplay: "U"},
		{ID: "mine_paused", Status: "paused", OwnerDisplay: "U"},
		{ID: "mine_blocked", Status: "blocked", OwnerDisplay: "U"},
		{ID: "other_run", Status: "running", OwnerDisplay: "V"},
		{ID: "other_paused", Status: "paused", OwnerDisplay: "V"},
		{ID: "pool_queued", Status: "queued", OwnerDisplay: ""},
	}

	groups := groupListRows(rows, "U", true, nil)

	// Running block holds only the viewer's running item (Mine is owner-scoped).
	if got := rowIDs(findStatusGroup(groups, "running")); len(got) != 1 || got[0] != "mine_run" {
		t.Errorf("Running block should hold only the viewer's running item; got %v", got)
	}
	// A queued item I own goes to the Queued block (not Unclaimed — it has an owner).
	if got := rowIDs(findStatusGroup(groups, "queued")); len(got) != 1 || got[0] != "mine_queued" {
		t.Errorf("Queued block should hold the viewer's owned queued item; got %v", got)
	}
	// Needs you holds the viewer's paused + blocked.
	needs := findGroup(groups, "Needs you")
	if got := rowIDs(needs); len(got) != 2 || got[0] != "mine_paused" || got[1] != "mine_blocked" {
		t.Errorf("Needs you should hold the viewer's paused+blocked items; got %v", got)
	}
	// Unclaimed holds only the ownerless queued item.
	if got := rowIDs(findGroup(groups, "Unclaimed")); len(got) != 1 || got[0] != "pool_queued" {
		t.Errorf("Unclaimed should hold only the ownerless queued item; got %v", got)
	}
	// Other people's items must not surface anywhere in Mine view.
	for i := range groups {
		for _, id := range rowIDs(&groups[i]) {
			if id == "other_run" || id == "other_paused" {
				t.Errorf("other user's item %q leaked into %q in Mine view", id, groups[i].Label)
			}
		}
	}
}

// TestGroupListRows_AllView_AllItemsFeedStatusBlocks: in All view, items owned
// by anyone feed the status blocks; Needs you still requires owner == viewer.
func TestGroupListRows_AllView_AllItemsFeedStatusBlocks(t *testing.T) {
	rows := []*wiListRow{
		{ID: "mine_run", Status: "running", OwnerDisplay: "U"},
		{ID: "other_run", Status: "running", OwnerDisplay: "V"},
		{ID: "other_paused", Status: "paused", OwnerDisplay: "V"},
		{ID: "mine_paused", Status: "paused", OwnerDisplay: "U"},
		{ID: "pool_queued", Status: "queued", OwnerDisplay: ""},
	}

	groups := groupListRows(rows, "U", false, nil)

	// Running block holds BOTH owners' running items in All view.
	run := rowIDs(findStatusGroup(groups, "running"))
	if len(run) != 2 {
		t.Errorf("Running block should hold both running items in All view; got %v", run)
	}
	// Needs you still only holds the viewer's paused item.
	if got := rowIDs(findGroup(groups, "Needs you")); len(got) != 1 || got[0] != "mine_paused" {
		t.Errorf("Needs you should hold only the viewer's paused item; got %v", got)
	}
	// Another user's paused item (not mine) falls into the Paused status block.
	if got := rowIDs(findStatusGroup(groups, "paused")); len(got) != 1 || got[0] != "other_paused" {
		t.Errorf("other user's paused item should fall in the Paused block; got %v", got)
	}
	// Unclaimed holds the ownerless queued item.
	if got := rowIDs(findGroup(groups, "Unclaimed")); len(got) != 1 || got[0] != "pool_queued" {
		t.Errorf("Unclaimed should hold the ownerless queued item; got %v", got)
	}
}

// TestGroupListRows_SmartSectionsAlwaysEmitted: both Mine and All views always
// emit the smart sections "Needs you" (first) and "Unclaimed" (last) even when
// empty, so the template renders the empty-state component.
func TestGroupListRows_SmartSectionsAlwaysEmitted(t *testing.T) {
	for _, mine := range []bool{true, false} {
		groups := groupListRows([]*wiListRow{}, "U", mine, nil)
		if len(groups) == 0 {
			t.Fatalf("mine=%v: expected groups, got none", mine)
		}
		if groups[0].Label != "Needs you" {
			t.Errorf("mine=%v: first group must be Needs you; got %q", mine, groups[0].Label)
		}
		if last := groups[len(groups)-1].Label; last != "Unclaimed" {
			t.Errorf("mine=%v: last group must be Unclaimed; got %q", mine, last)
		}
		// Both are empty here.
		if g := findGroup(groups, "Needs you"); g == nil || len(g.Rows) != 0 {
			t.Errorf("mine=%v: Needs you must be emitted empty; got %v", mine, rowIDs(g))
		}
		if g := findGroup(groups, "Unclaimed"); g == nil || len(g.Rows) != 0 {
			t.Errorf("mine=%v: Unclaimed must be emitted empty; got %v", mine, rowIDs(g))
		}
	}
}

// TestGroupListRows_NoSelection_AllSixBlocksRendered: when no status is selected
// the full six status blocks render (empty ones included), in canonical order.
func TestGroupListRows_NoSelection_AllSixBlocksRendered(t *testing.T) {
	groups := groupListRows([]*wiListRow{}, "U", false, nil)

	wantOrder := []string{"queued", "running", "paused", "blocked", "wrapped", "cancelled"}
	var gotStatus []string
	for _, g := range groups {
		if g.Kind == "status" {
			gotStatus = append(gotStatus, g.Status)
		}
	}
	if len(gotStatus) != len(wantOrder) {
		t.Fatalf("expected all six status blocks; got %v", gotStatus)
	}
	for i, w := range wantOrder {
		if gotStatus[i] != w {
			t.Errorf("status block order: pos %d got %q, want %q (full %v)", i, gotStatus[i], w, gotStatus)
		}
	}
}

// TestGroupListRows_SelectedStatusesDriveBlocks: only the SELECTED statuses get
// a block; unselected statuses are not rendered, and a selected status with no
// rows is still emitted (empty) so the filter's effect is perceptible.
func TestGroupListRows_SelectedStatusesDriveBlocks(t *testing.T) {
	rows := []*wiListRow{
		{ID: "other_wrapped", Status: "wrapped", OwnerDisplay: "V"},
	}
	sel := map[string]bool{"wrapped": true, "cancelled": true}

	groups := groupListRows(rows, "U", false, sel)

	// Wrapped block holds the one wrapped row.
	if got := rowIDs(findStatusGroup(groups, "wrapped")); len(got) != 1 || got[0] != "other_wrapped" {
		t.Errorf("Wrapped block should hold the wrapped row; got %v", got)
	}
	// Cancelled block is emitted even though it is empty.
	cancelled := findStatusGroup(groups, "cancelled")
	if cancelled == nil {
		t.Fatalf("Cancelled block must be emitted (empty) when explicitly selected")
	}
	if len(cancelled.Rows) != 0 {
		t.Errorf("Cancelled block should be empty; got %v", rowIDs(cancelled))
	}
	// Unselected statuses get NO block.
	for _, s := range []string{"queued", "running", "paused", "blocked"} {
		if g := findStatusGroup(groups, s); g != nil {
			t.Errorf("unselected status %q must not render a block", s)
		}
	}
}

// TestGroupListRows_UnclaimedRenderedLast asserts Unclaimed is the LAST group
// and Needs you the FIRST, with status blocks in between.
func TestGroupListRows_UnclaimedRenderedLast(t *testing.T) {
	rows := []*wiListRow{
		{ID: "pool_queued", Status: "queued", OwnerDisplay: ""},
		{ID: "other_wrapped", Status: "wrapped", OwnerDisplay: "V"},
		{ID: "mine_run", Status: "running", OwnerDisplay: "U"},
		{ID: "mine_paused", Status: "paused", OwnerDisplay: "U"},
	}

	groups := groupListRows(rows, "U", false, nil)
	if len(groups) < 2 {
		t.Fatalf("expected groups, got %d", len(groups))
	}
	if groups[0].Label != "Needs you" {
		t.Errorf("first group must be Needs you; got %q", groups[0].Label)
	}
	if last := groups[len(groups)-1].Label; last != "Unclaimed" {
		labels := make([]string, len(groups))
		for i, g := range groups {
			labels[i] = g.Label
		}
		t.Errorf("Unclaimed must be the last group; order was %v", labels)
	}
}

// TestGroupListRows_NeedsYouFlagSet asserts the NeedsYou flag (which gates the
// .row.hot left bar) is set ONLY on the viewer's paused/blocked rows, never on
// Unclaimed/queued/running rows.
func TestGroupListRows_NeedsYouFlagSet(t *testing.T) {
	paused := &wiListRow{ID: "mine_paused", Status: "paused", OwnerDisplay: "U"}
	pool := &wiListRow{ID: "pool_queued", Status: "queued", OwnerDisplay: ""}
	running := &wiListRow{ID: "mine_run", Status: "running", OwnerDisplay: "U"}

	groupListRows([]*wiListRow{paused, pool, running}, "U", true, nil)

	if !paused.NeedsYou {
		t.Errorf("viewer's paused row should have NeedsYou=true")
	}
	if pool.NeedsYou {
		t.Errorf("Unclaimed/queued row must NOT have NeedsYou=true (aihub#4 left-bar bug)")
	}
	if running.NeedsYou {
		t.Errorf("running row must NOT have NeedsYou=true (only paused/blocked qualify)")
	}
}

// TestGroupListRows_OwnedBlockedGoesToNeedsYou asserts the precedence tiebreak:
// an OWNED blocked item goes to "Needs you" (rule 2), while an OWNERLESS blocked
// item goes to "Unclaimed" (rule 1, which is checked first).
func TestGroupListRows_OwnedBlockedGoesToNeedsYou(t *testing.T) {
	rows := []*wiListRow{
		{ID: "mine_blocked", Status: "blocked", OwnerDisplay: "U"},
		{ID: "pool_blocked", Status: "blocked", OwnerDisplay: ""},
	}
	groups := groupListRows(rows, "U", true, nil)

	if got := rowIDs(findGroup(groups, "Needs you")); len(got) != 1 || got[0] != "mine_blocked" {
		t.Errorf("owned blocked item should be in Needs you; got %v", got)
	}
	if got := rowIDs(findGroup(groups, "Unclaimed")); len(got) != 1 || got[0] != "pool_blocked" {
		t.Errorf("ownerless blocked item should be in Unclaimed; got %v", got)
	}
	// The Blocked status block must NOT contain either (both consumed by smart sections).
	if g := findStatusGroup(groups, "blocked"); g != nil && len(g.Rows) != 0 {
		t.Errorf("Blocked block should be empty; got %v", rowIDs(g))
	}
}

// TestGroupCountsFromGroups asserts the strip counts are read straight off the
// grouped output. Running is the running STATUS block; Needs you / Unclaimed are
// the smart sections.
func TestGroupCountsFromGroups(t *testing.T) {
	rows := []*wiListRow{
		{ID: "r1", Status: "running", OwnerDisplay: "U"},
		{ID: "r2", Status: "running", OwnerDisplay: "U"},
		{ID: "n1", Status: "paused", OwnerDisplay: "U"},
		{ID: "u1", Status: "queued", OwnerDisplay: ""},
		{ID: "u2", Status: "queued", OwnerDisplay: ""},
		{ID: "u3", Status: "blocked", OwnerDisplay: ""},
	}
	got := groupCountsFromGroups(groupListRows(rows, "U", true, nil))
	if got.Running != 2 || got.NeedsYou != 1 || got.Unclaimed != 3 {
		t.Errorf("strip counts mismatch: got %+v, want Running=2 NeedsYou=1 Unclaimed=3", got)
	}
}

// TestSortListRows_GlobalOrder asserts rows sort newest-first by CreatedAt,
// tiebroken by project then seq — one consistent key regardless of which project
// segment they arrived in.
func TestSortListRows_GlobalOrder(t *testing.T) {
	t0 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rows := []*wiListRow{
		{ID: "old_b2", Project: "beta", Seq: 2, CreatedAt: t0},
		{ID: "new", Project: "alpha", Seq: 9, CreatedAt: t0.Add(time.Hour)},
		{ID: "old_a1", Project: "alpha", Seq: 1, CreatedAt: t0},
		{ID: "old_a2", Project: "alpha", Seq: 2, CreatedAt: t0},
	}
	sortListRows(rows)
	want := []string{"new", "old_a1", "old_a2", "old_b2"}
	got := make([]string, len(rows))
	for i, r := range rows {
		got[i] = r.ID
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("sort order: pos %d got %q, want %q (full: %v)", i, got[i], w, got)
		}
	}
}

// TestStatusFilterLabel covers the multi-select button label.
func TestStatusFilterLabel(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, "All status"},
		{[]string{}, "All status"},
		{[]string{"queued"}, "Queued"},
		{[]string{"queued", "running"}, "2 selected"},
		{[]string{"queued", "running", "paused"}, "3 selected"},
	}
	for _, c := range cases {
		if got := statusFilterLabel(c.in); got != c.want {
			t.Errorf("statusFilterLabel(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
