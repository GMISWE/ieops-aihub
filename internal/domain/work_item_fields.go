package domain

import (
	"fmt"
	"sort"
	"strings"
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

// MaxWorkItemGoalRunes exposes the goal cap for the published descriptions, for
// the same reason MaxWorkItemLabels above exists: a number typed into an MCP
// tool description is a promise, and a promise retyped by hand is one the
// enforcement can walk away from without anybody noticing. aihub#474 found the
// walked-away half on the OTHER side of this pair — the cap enforced on create
// and absent on update — so the accessor lands with the fix rather than after
// it.
func MaxWorkItemGoalRunes() int { return maxWorkItemGoalRunes }

// vocabularyErr builds the 400 for a value outside a closed set.
//
// The legal values go in the message AND in details. In the message because
// that is the only part some clients surface, and in details because an
// automated caller should not have to parse prose to retry correctly — which is
// the entire difference between this and the 500 it replaces.
func vocabularyErr(field, got string, allowed []string) *AihubError {
	// Sort a COPY. Every caller today passes a freshly built slice (sortedKeys),
	// so sorting in place is harmless on this tree — which is precisely why it
	// would stay harmless-looking right up to the first caller that hands over a
	// package-level slice and finds it reordered under it. The mutation is
	// invisible from the call site, the failure would be in someone else's
	// output, and the copy costs one allocation on an error path (aihub#493).
	sorted := make([]string, len(allowed))
	copy(sorted, allowed)
	sort.Strings(sorted)
	return NewErrDetails(ErrBadRequest,
		fmt.Sprintf("%s %q is not a legal value; allowed: %v", field, got, sorted),
		map[string]any{
			"field":   field,
			"got":     got,
			"allowed": sorted,
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

// validateWorkItemGoalShape checks the two SHAPE constraints on a goal that was
// supplied: the rune cap and the newline ban. They are the two halves of
// work_items_goal_check, and this is the function db_check_policy_test.go names
// as the Go mirror of it.
//
// ─── Why this is one function and not two copies (aihub#474) ─────────────────
//
// It used to be two copies, and they disagreed. CreateWorkItem checked both
// halves; UpdateWorkItem checked only the newline half. The asymmetry was
// invisible because nothing in the tree ever said updates were exempt; it was
// not a decision, it was a line nobody wrote. The owner ruled it an oversight on
// 2026-09-09.
//
// ⚠️ What the missing half actually cost, measured rather than assumed.
// aihub#474's own body says `pf_update_work_item` STORED an over-cap goal. It
// did not. work_items_goal_check (migration 0002, unaltered by any later
// migration) caps length(goal) at 500 in the database, so an over-cap update was
// refused by POSTGRES as SQLSTATE 23514 — which is neither 40001 nor 40P01, so
// dbErr fell through to ErrInternalError. The real defect is therefore that the
// two tools answered the SAME illegal string two different ways: a 400 naming
// the field on create, and a 500 with a constraint name in it on update. That is
// exactly the defect class aihub#396 exists to remove, arriving through the one
// door aihub#396 did not close. The integrity of the column was never at risk;
// the caller-facing contract was.
//
// Re-adding the missing line to the update path would have fixed the instance
// and left the SHAPE that produced it: two call sites, two literals, no
// structural reason for them to agree tomorrow. One function is the fix that
// cannot come apart — a third write path gets both halves by calling it, and a
// half deleted here is deleted for every caller at once, which is a mutation a
// test can see.
//
// EMPTINESS is still NOT checked here, and after aihub#507 that is a sharper
// statement than it was: both write paths DO refuse an empty goal now, they just
// do not refuse it from inside this function. The reason is what this function
// is — the Go mirror of work_items_goal_check, named as such by
// db_check_policy_test.go. That CHECK permits the empty string (the column is
// TEXT NOT NULL, and NOT NULL admits it), so a required-ness check folded in here
// would make the mirror enforce something the constraint does not, and the one
// test that compares the two would stop meaning what it says. Required-ness is
// validateWorkItemGoalPresent below, a sibling both paths call.
//
// RUNES, not bytes — see maxWorkItemGoalRunes. A 500-character Chinese goal is
// 1,500 bytes and Postgres accepts it, so a byte-counting guard here would
// refuse input the column takes.
func validateWorkItemGoalShape(goal string) *AihubError {
	if utf8.RuneCountInString(goal) > maxWorkItemGoalRunes {
		return NewErr(ErrBadRequest, fmt.Sprintf("goal exceeds %d characters", maxWorkItemGoalRunes))
	}
	if strings.ContainsAny(goal, "\n\r") {
		return NewErr(ErrGoalMultiline, "goal must not contain newlines")
	}
	return nil
}

// validateWorkItemGoalPresent refuses an empty goal, and it is the ONE copy of
// that refusal — both work-item write paths call it (aihub#507).
//
// ─── What it fixes ───────────────────────────────────────────────────────────
//
// `pf_create_work_item {goal: ""}` was a 400 "goal is required"; the identical
// value through `pf_update_work_item` was STORED. Same column, two doors, two
// answers — the aihub#396 class again, and the third instance after aihub#396
// itself and aihub#474. It differs from #474's instance in the direction of the
// disagreement: there the divergent answer was a 500 from the column CHECK, so
// the data was never at risk. Here the CHECK has nothing to say (TEXT NOT NULL
// admits the empty string), so the update path wrote an empty goal and returned
// 200 — a work
// item that renders blank in every list and in the ready queue, with no error
// anywhere to trace it back to. The owner ruled on 2026-09-09 that update must
// mirror create.
//
// ⚠️ Enabling a refusal on a live write path is a behaviour change, so it was
// measured first rather than argued: production Cloud SQL, 2026-09-09, 2,309
// work items, `length(goal) = 0` → 0 rows, and `length(btrim(goal)) = 0 AND
// length(goal) > 0` (whitespace-only) → 0 rows. No stored record depends on the
// old behaviour and
// no caller is observably using it.
//
// ─── Why a separate function and not a third branch in the shape validator ───
//
// aihub#474's lesson is that one rule written twice is a rule that comes apart,
// and the message here is a literal ("goal is required") that a caller may branch
// on, so it must have one home. But the shape validator is not that home: it is
// the declared Go mirror of work_items_goal_check, and the empty string
// SATISFIES that CHECK.
// Two functions, one call site each per path, keeps both claims true — the mirror
// stays a mirror, and required-ness still cannot drift between the doors.
//
// ─── The boundary that is deliberately NOT crossed ───────────────────────────
//
// "" only. A whitespace-only goal (" ") is ACCEPTED here, because create accepts
// it and the ruling was to mirror create — refusing it on update alone would
// close one asymmetry by opening another, which is the defect class itself. The
// census above found zero rows of either kind, so nothing is riding on the
// choice today; tightening both paths together is a separate ruling.
//
// ⚠️ This function never decides whether a goal was SUPPLIED. On the update path
// `goal: ""` and no `goal` at all are different requests, and that distinction
// lives at the call site's `req.Goal != nil` guard, where the pointer still
// carries it. A guard moved in here would refuse every update that leaves the
// goal alone.
func validateWorkItemGoalPresent(goal string) *AihubError {
	if goal == "" {
		return NewErr(ErrBadRequest, "goal is required")
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
				"note":  "the limit is CHARACTERS, not bytes, the same unit the database CHECK uses",
			})
	}
	return nil
}
