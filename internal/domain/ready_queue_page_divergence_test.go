package domain

// aihub#543 probe wave 1 — one predicate, two pages.
//
// The pf_get_ready_queue card and pf_list_work_items' `ready_only` description
// both state the same cross-tool fact: the two surfaces ask the SAME question of
// the database and hand back DIFFERENT pages of the answer, so with more ready
// items than either limit they return different subsets.
//
// Half of that had coverage and half did not. The sharing is asserted
// behaviourally by TestGetReadyQueue_ItemsAgreesWithReadyOnlyFilter, which calls
// both paths against a live database and compares the two sets — on a fixture of
// six work items, i.e. below both page sizes, which is exactly the case where the
// two AGREE. Nothing anywhere held the divergence, and the divergence is the half
// a caller gets wrong: `ready_only`'s description exists because "the natural
// reading is that they agree".
//
// So this arm asserts the divergence at its source — the page, not the predicate
// — and it is pure-unit: both statements are assembled by functions that take no
// pool (aihub#280's buildReadyQueueItemsQuery and buildListWorkItemsQuery, both
// split out for exactly this reason).
//
// 🔴 What it deliberately does NOT do: it does not spell today's numbers out. The
// claim is that the two page sizes DIFFER, not that they are 10 and 50, and an
// arm that pinned the pair would go red on a legitimate change to either while
// staying green on the one change that makes the card false — the two being made
// equal. The order-by halves are compared to each other for the same reason.
//
//	GOWORK=off go test ./internal/domain/ -run TestTheReadyQueueAndReadyOnly -count=1 -v

import (
	"strings"
	"testing"
)

// TestTheReadyQueueAndReadyOnlyShareOnePredicateAndPageDifferently is the arm.
//
// ─── Recorded mutants ──────────────────────────────────────────────────────
//
// Every one was applied to this tree and the verdict is what RAN.
//
//	── enforcement side (the code, the card untouched) ──
//	M9  set readyQueueDefaultMax to ListWorkItemsLimitDefault
//	                                             RED  one page size for both
//	M10 give items[] the list's `ORDER BY wi.created_at DESC`
//	                                             RED  one ordering for both
//	M11 inline readyOnlyPredicate's text into buildReadyQueueItemsQuery as a
//	    second, identical copy                 GREEN  recorded rather than fixed.
//	                                                  This arm claims the two
//	                                                  statements CARRY the same
//	                                                  predicate text, and a verbatim
//	                                                  copy satisfies that. A copy
//	                                                  that DRIFTS is what
//	                                                  TestGetReadyQueue_ItemsAgreesWithReadyOnlyFilter
//	                                                  catches, against a live
//	                                                  database, on the sets the two
//	                                                  paths return.
//
//	── publication side (the card, the code untouched) ──
//	M12 delete the sentence from the card        RED  K12 POPULATION_MOVED
func TestTheReadyQueueAndReadyOnlyShareOnePredicateAndPageDifferently(t *testing.T) {
	queue := buildReadyQueueItemsQuery()
	// Limit is spelled by the caller here rather than defaulted, because
	// buildListWorkItemsQuery documents that it "assumes f.Limit is already
	// clamped" — the default lives on the constant this arm reads separately.
	list, _, _ := buildListWorkItemsQuery("p", ListWorkItemsFilter{
		ReadyOnly: true, Limit: ListWorkItemsLimitDefault,
	})

	// ── the floor, first: is the predicate substantial enough for containment
	// to mean anything? An empty or one-condition constant would be found in
	// both statements and prove nothing.
	for _, condition := range []string{
		"wi.status = 'queued'",
		"wi.requires_human_session = false",
		"NOT EXISTS",
		"dep.kind = 'blocks'",
	} {
		if !strings.Contains(readyOnlyPredicate, condition) {
			t.Fatalf("readyOnlyPredicate no longer contains %q, so the containment checks "+
				"below would pass on a predicate that had stopped meaning \"ready\". It is "+
				"the SQL definition of the word — three conditions, stated on the constant "+
				"itself — and this arm cannot assert two callers share it without knowing "+
				"what it is.", condition)
		}
	}

	// ── one predicate, both callers.
	if !strings.Contains(queue, readyOnlyPredicate) {
		t.Errorf("the ready queue's items[] statement does not embed readyOnlyPredicate:\n%s\n"+
			"aihub#280 made this one constant precisely so `ready_only` could not drift into "+
			"meaning something other than the queue it is named after. A second copy here is "+
			"how it starts.", queue)
	}
	if !strings.Contains(list, readyOnlyPredicate) {
		t.Errorf("ListWorkItems' ready_only statement does not embed readyOnlyPredicate:\n%s\n"+
			"The published `ready_only` description promises the SAME PREDICATE as the ready "+
			"queue's items[], and one shared SQL constant is what makes that true rather than "+
			"a coincidence maintained by hand.", list)
	}

	// ── two pages. The default page size differs …
	if readyQueueDefaultMax == ListWorkItemsLimitDefault {
		t.Errorf("both surfaces now default to a page of %d. The pf_get_ready_queue card and "+
			"`ready_only`'s own description both tell a caller that with more ready items than "+
			"either limit the two return DIFFERENT subsets, and a caller who plans a fan-out "+
			"around that reads two identical pages as a bug in their own code. Make the "+
			"descriptions agree with the code in the same diff.", readyQueueDefaultMax)
	}

	// … and so does the ordering.
	queueOrder, ok := orderByClause(queue)
	if !ok {
		t.Fatalf("no ORDER BY found in the ready queue's items[] statement:\n%s", queue)
	}
	listOrder, ok := orderByClause(list)
	if !ok {
		t.Fatalf("no ORDER BY found in ListWorkItems' statement:\n%s", list)
	}
	if queueOrder == listOrder {
		t.Errorf("both surfaces now order by %q. Same predicate AND same order AND same page "+
			"size would make the two identical, which is not what either description says — "+
			"and the ready queue's priority-first order is the reason it is a dispatch input "+
			"rather than a list.", queueOrder)
	}

	t.Logf("ready queue: page %d ordered by %q; ready_only: page %d ordered by %q",
		readyQueueDefaultMax, queueOrder, ListWorkItemsLimitDefault, listOrder)
}

// orderByClause returns the ORDER BY clause of a statement, whitespace-collapsed
// so two clauses that differ only in indentation compare equal — the comparison
// is about the columns, and a formatting change is not a contract change.
func orderByClause(sql string) (string, bool) {
	_, after, found := strings.Cut(sql, "ORDER BY")
	if !found {
		return "", false
	}
	clause, _, _ := strings.Cut(after, "LIMIT")
	return strings.Join(strings.Fields(clause), " "), true
}
