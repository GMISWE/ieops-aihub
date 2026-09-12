package domain

import "fmt"

// memoryVisibilityScopeSQL renders THE caller-scoped row-visibility predicate —
// the single SQL copy of the per-memory visibility rule (aihub#379):
//
//   - visibility='private' → only the author (or a global admin) sees the row
//   - visibility='admin'   → only a global admin sees the row
//   - every other tier     → visible to anyone who passed the project gate
//
// Every SQL reader of memories that scopes rows to a caller MUST build its
// WHERE clause through this function. Today that is Recall's text path
// (memory.go), the vector path (memory_vector.go), the unmatched-types
// diagnostic (memory_unmatched.go) and the relation-graph enrichment
// (loadForwardRelations, memory.go). Before aihub#379 each carried its own
// inline copy; the four happened to render the same predicate (up to a table
// alias), but "happened to" is the defect class that work item closed: a
// fifth copy of the same rule (checkMemoryVisibility, internal/server) had
// already drifted to a different observable answer, and a sixth
// (ui_handlers_wi.go's inline guard) had silently lost its admin-tier arm.
//
// The Go twin of this predicate is memoryVisibleTo
// (internal/server/ui_handlers_memory.go), which every response-writing
// handler consults. TestMemoryVisibilityParity_SQLAgreesWithGoPredicate
// (internal/server, DB-gated) drives both against the same seeded rows and
// fails if either side drifts; TestMemoryVisibilitySQLPredicateHasOneCopy
// (this package) fails if a caller re-inlines the clause instead of calling
// here.
//
// The column references are deliberately UNQUALIFIED, including in
// loadForwardRelations' join: memory_relations carries neither `visibility`
// nor `author_user_id`, so the references resolve to the memories side — and
// if that table ever grew a clashing column, Postgres would refuse the query
// as ambiguous rather than silently widening anything. Keeping one literal
// with no alias interpolation is what lets the pf_recall card publish the
// predicate verbatim (recall_card_claims_test.go holds the SQL to the card's
// backticked spelling).
//
// For an admin caller the predicate is empty: admins see every tier. The
// returned clause starts with " AND " so call sites append it verbatim to a
// WHERE body; args carries the one bind value (callerUserID) and nextIdx is
// the first unused placeholder index.
func memoryVisibilityScopeSQL(callerRole, callerUserID string, idx int) (clause string, args []any, nextIdx int) {
	nextIdx = idx
	if callerRole != "admin" {
		clause = fmt.Sprintf(` AND (visibility != 'private' OR author_user_id = $%d)`, nextIdx)
		args = append(args, callerUserID)
		nextIdx++
		clause += ` AND visibility != 'admin'`
	}
	return clause, args, nextIdx
}
