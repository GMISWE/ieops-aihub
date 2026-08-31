package domain

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ─── the type filter, in ONE place ────────────────────────────────────────────

// typeFilterClause renders a caller's `type` filter as a SQL predicate over
// memories.type, using placeholders starting at $startIdx. It returns the
// predicate (already parenthesised), the args to append, and the next free
// placeholder index.
//
// The rule is unchanged from the inline copy it replaces in recallText:
//
//	"<prefix>.*"   ->  type LIKE '<prefix>.%'   (wildcard / prefix match)
//	anything else  ->  type = '<value>'         (exact match)
//
// Entries are OR'd. An EMPTY filter renders as ("", nil, startIdx), which callers
// must read as "no type predicate at all" — never as "matches nothing".
//
// WHY THIS WAS EXTRACTED (aihub#289)
// ----------------------------------
// UnmatchedTypes below has to answer "would this filter entry have matched
// anything?" with EXACTLY the rule the recall query uses. A hand-written Go mirror
// of that rule would already be wrong in a way that matters: SQL LIKE treats `_` as
// a single-character wildcard, so `methodology.wrap_summary.*` is strictly more
// permissive in SQL than strings.HasPrefix is in Go. A diagnostic that under-reports
// matches would report a type as unmatched that recall does in fact match — a false
// alarm in the very field this work item adds to end a false silence. Sharing the
// builder makes that divergence unrepresentable instead of merely tested for.
//
// NOTE: memory_vector.go carries a third copy of this rule for the vector path. It
// is owned by another work item in flight and is deliberately left alone here;
// TestTypeFilterClause_MatchesVectorPathInlineCopy pins the two together so the
// duplication is gated rather than silent.
func typeFilterClause(types []string, startIdx int) (string, []any, int) {
	if len(types) == 0 {
		return "", nil, startIdx
	}
	idx := startIdx
	clauses := make([]string, 0, len(types))
	args := make([]any, 0, len(types))
	for _, t := range types {
		if strings.HasSuffix(t, ".*") {
			clauses = append(clauses, fmt.Sprintf("type LIKE $%d", idx))
			args = append(args, strings.TrimSuffix(t, "*")+"%")
		} else {
			clauses = append(clauses, fmt.Sprintf("type = $%d", idx))
			args = append(args, t)
		}
		idx++
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args, idx
}

// ─── unmatched-type diagnostic (aihub#289) ────────────────────────────────────

// UnmatchedTypes reports which entries of req.Types no visible, live memory in the
// project matches. It is a DIAGNOSTIC, not a filter: it changes nothing about which
// rows recall returns.
//
// WHAT PROBLEM THIS SOLVES
// ------------------------
// A `type` value that matches no row is indistinguishable, in the response, from a
// project that genuinely holds no such memory. Both are `{"items":null,"total":0}`.
// Every way of getting a type value wrong therefore failed SILENTLY:
//
//   - the pipe form `"a|b|c"` that three SKILL.md templates taught for months (the
//     whole string arrives as one type name; nothing in the chain ever split it)
//   - a typo, or a type that has since been renamed
//   - a caller-invented type that never existed
//
// and the consequence is worse than a thin result set: an agent following the
// Memory-First rule reads "no items" as "no prior experience exists", which is
// exactly the signal that makes it redo work and re-hit pitfalls already recorded.
// One observable field covers that whole class.
//
// SCOPE — READ THIS BEFORE WIDENING IT
// ------------------------------------
// The predicate below is project + status + expiry + visibility, i.e. the filters
// that decide whether a row is VISIBLE AT ALL to this caller. It deliberately does
// NOT include min_strength, work_item_id, similarity_threshold or paging. That
// keeps the field answering exactly one question — "is this type NAME wrong?" — and
// stops it from firing on "your other filters excluded everything", which is a
// different diagnosis with a different fix. A field that means two things means
// neither.
//
// Returns entries in the caller's order, deduplicated. A nil/empty result means
// every entry matched something (or no type filter was supplied).
//
// NEVER fails the recall: this is an add-on observation, so a broken diagnostic
// query is logged to stderr and reported as "nothing to say", not surfaced as an
// error on a request whose primary result is already in hand.
func UnmatchedTypes(ctx context.Context, pool *pgxpool.Pool, req *RecallRequest) []string {
	if req == nil || len(req.Types) == 0 {
		return nil
	}

	statusSet := "'active'"
	if req.IncludeArchived {
		statusSet = "'active','archived'"
	}
	// Mirrors recallText's visibility predicate exactly — a type whose only rows are
	// invisible to this caller genuinely has nothing for them, and saying "unmatched"
	// is the honest answer rather than a leak that such rows exist.
	base := fmt.Sprintf(`project = $1
		AND status IN (%s)
		AND (expires_at IS NULL OR expires_at > clock_timestamp())`, statusSet)
	args := []any{req.Project}
	idx := 2
	if req.CallerRole != "admin" {
		base += fmt.Sprintf(` AND (visibility != 'private' OR author_user_id = $%d)`, idx)
		args = append(args, req.CallerUserID)
		idx++
		base += ` AND visibility != 'admin'`
	}

	// One EXISTS per filter entry, all in a single round trip. EXISTS stops at the
	// first hit, and the entry count is the size of a caller's type list (a handful),
	// so this costs one cheap query on type-filtered recalls and nothing at all on
	// unfiltered ones.
	sels := make([]string, 0, len(req.Types))
	for i, t := range req.Types {
		clause, cargs, next := typeFilterClause([]string{t}, idx)
		sels = append(sels, fmt.Sprintf(
			"EXISTS(SELECT 1 FROM memories WHERE %s AND %s) AS m%d", base, clause, i))
		args = append(args, cargs...)
		idx = next
	}

	found := make([]bool, len(req.Types))
	dests := make([]any, len(found))
	for i := range found {
		dests[i] = &found[i]
	}
	q := "SELECT " + strings.Join(sels, ", ")
	if err := pool.QueryRow(ctx, q, args...).Scan(dests...); err != nil {
		fmt.Fprintf(os.Stderr, "recall: unmatched-type diagnostic failed (recall result unaffected): %v\n", err)
		return nil
	}

	var out []string
	seen := make(map[string]bool, len(req.Types))
	for i, t := range req.Types {
		if found[i] || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}
