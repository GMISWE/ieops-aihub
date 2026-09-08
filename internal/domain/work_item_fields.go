package domain

import (
	"fmt"
	"sort"
	"unicode/utf8"
)

// aihub#396 — the vocabularies and limits on a work item's own columns,
// validated in Go so a caller mistake is a 400 naming the field instead of a
// 500 with a SQLSTATE in it.
//
// ─── The policy, which is repo-wide and not local to this file ──────────────
//
// A vocabulary or limit that a DB CHECK enforces is ALSO validated in Go and
// answered with a 400 naming the field. The CHECK is the last line of defence,
// never the caller-facing one. Recorded as the owner's decision on aihub#396,
// of which this file is the first application.
//
// ⚠️ Why "the constraint already catches it" is not good enough. Every non-class
// -40 SQLSTATE goes through dbErrCause -> ErrInternalError (internal/domain/
// pgx_err.go), so a `priority: "P1"` came back as:
//
//	500 INTERNAL_ERROR  failed to insert work_item :: ERROR: new row for
//	relation "work_items" violates check constraint
//	"work_items_priority_check" (SQLSTATE 23514)
//
// Three things are wrong with that answer and only the first is cosmetic:
// a 500 tells the caller to RETRY something that can never succeed; it tells an
// operator the server is broken when the request was; and the legal values are
// nowhere in it, so the caller cannot self-correct. For an LLM caller the third
// is the expensive one — it will try another guess.
//
// ─── One copy of each vocabulary, checked against the migration ─────────────
//
// The sets below are duplicated from the CHECK constraints, which is a drift
// risk, so it is closed by a test rather than by a comment:
// TestWorkItemVocabulariesMatchTheMigrations parses 0002_work_items.sql and
// 0011_wi_content.sql and fails if either copy moves. Do not "simplify" that
// test into an assertion about these literals — parsing the migration is the
// whole point, because the migration is what the database will actually do.

// workItemPriorities mirrors the priority CHECK in
// internal/db/migrations/0002_work_items.sql.
var workItemPriorities = map[string]bool{
	"low":    true,
	"normal": true,
	"high":   true,
	"urgent": true,
}

// workItemSources mirrors the source CHECK in
// internal/db/migrations/0002_work_items.sql.
var workItemSources = map[string]bool{
	"human":        true,
	"auto_execute": true,
	"auto_debug":   true,
	"auto_review":  true,
	"sync_jira":    true,
	"sync_github":  true,
	"admin":        true,
}

// maxWorkItemGoalRunes mirrors the `length(goal) <= 500` half of the goal CHECK
// in internal/db/migrations/0002_work_items.sql.
//
// aihub#434 gave the number a name. CreateWorkItem had checked it correctly and
// in the right unit since long before aihub#396 — the literal 500 was simply
// typed twice, once in the migration and once in the function, with nothing
// holding them together. The other half of the same CHECK, `goal !~ E'[\n\r]'`,
// is mirrored by the strings.ContainsAny(req.Goal, "\n\r") next to it; both
// halves are asserted by db_check_policy_test.go.
//
// RUNES, for the reason spelled out under maxWorkItemContentRunes below.
const maxWorkItemGoalRunes = 500

// maxWorkItemLabels mirrors `cardinality(labels) <= 20` in
// internal/db/migrations/0002_work_items.sql.
const maxWorkItemLabels = 20

// maxWorkItemContentRunes mirrors `length(content) <= 20000` in
// internal/db/migrations/0011_wi_content.sql.
//
// ⚠️ RUNES, not bytes, and that is not a detail: Postgres `length()` on TEXT
// counts CHARACTERS. Validating bytes here would reject a legal 8,000-character
// Chinese body (three bytes each) that the database would have accepted — the
// fail-closed-too-wide direction, where the guard refuses correct input and the
// cheapest fix is to delete the guard. utf8.RuneCountInString is the matching
// unit, and it is the same choice the goal check above it already makes.
const maxWorkItemContentRunes = 20000

// WorkItemPriorityList and WorkItemSourceList expose the vocabularies for the
// MCP layer to publish as JSON-Schema enums, so the values a caller is offered
// and the values the server accepts are one set rather than two that agree
// today. Sorted for a stable rendered schema.
func WorkItemPriorityList() []string { return sortedKeys(workItemPriorities) }

// WorkItemSourceList returns the legal `source` values.
func WorkItemSourceList() []string { return sortedKeys(workItemSources) }

// MaxWorkItemLabels exposes the labels cap for the published description.
func MaxWorkItemLabels() int { return maxWorkItemLabels }

// vocabularyErr builds the 400 for a value outside a closed set.
//
// The legal values go in the message AND in details. In the message because
// that is the only part some clients surface, and in details because an
// automated caller should not have to parse prose to retry correctly — which is
// the entire difference between this and the 500 it replaces.
func vocabularyErr(field, got string, allowed []string) *AihubError {
	sort.Strings(allowed)
	return NewErrDetails(ErrBadRequest,
		fmt.Sprintf("%s %q is not a legal value; allowed: %v", field, got, allowed),
		map[string]any{
			"field":   field,
			"got":     got,
			"allowed": allowed,
		})
}

// validateWorkItemPriority checks a priority value that was supplied.
//
// The empty string is NOT checked here. Both callers default it before this
// runs, and treating "" as illegal would reject an update that does not mention
// priority at all. Each caller decides what "absent" means; this function only
// ever sees a value somebody chose.
func validateWorkItemPriority(priority string) *AihubError {
	if !workItemPriorities[priority] {
		return vocabularyErr("priority", priority, WorkItemPriorityList())
	}
	return nil
}

// validateWorkItemSource checks a source value that was supplied.
func validateWorkItemSource(source string) *AihubError {
	if !workItemSources[source] {
		return vocabularyErr("source", source, WorkItemSourceList())
	}
	return nil
}

// validateWorkItemLabels checks the labels cap.
func validateWorkItemLabels(labels []string) *AihubError {
	if len(labels) > maxWorkItemLabels {
		return NewErrDetails(ErrBadRequest,
			fmt.Sprintf("labels has %d entries, the maximum is %d", len(labels), maxWorkItemLabels),
			map[string]any{
				"field": "labels",
				"got":   len(labels),
				"max":   maxWorkItemLabels,
			})
	}
	return nil
}

// validateWorkItemContent checks the content length in CHARACTERS.
func validateWorkItemContent(content *string) *AihubError {
	if content == nil {
		return nil
	}
	if n := utf8.RuneCountInString(*content); n > maxWorkItemContentRunes {
		return NewErrDetails(ErrBadRequest,
			fmt.Sprintf("content is %d characters, the maximum is %d", n, maxWorkItemContentRunes),
			map[string]any{
				"field": "content",
				"got":   n,
				"max":   maxWorkItemContentRunes,
				"note":  "the limit is CHARACTERS, not bytes — the same unit the database CHECK uses",
			})
	}
	return nil
}
