package domain

import "sort"

// The agent_events.event_type vocabulary, and the three sets that govern it.
//
// aihub#444, from the aihub#411 decision table §6.2 T2-5. One column carried
// three overlapping-but-different partial whitelists and no vocabulary at all:
//
//	DB    nothing on the value itself. `event_type TEXT NOT NULL`
//	      (0006_events_memories.sql), no CHECK. The one CHECK that LOOKS like a
//	      vocabulary — chk_evt_work_item_id, 21 entries — governs which events
//	      may omit a work item, not which events exist.
//	Go    adminOnlyEventTypes (4, the H6 forgery guard) and adminEventWhitelist
//	      (7, the H10 admin=true gate). Every other string accepted.
//	MCP   three examples in a free-text description.
//
// Membership in one implied nothing about the others, and the gap was not
// theoretical: `admin_gc_manual` was in the FIRST and not the second, which
// INVERTED the flag — an admin sending it with `admin: true` was refused 403
// "not in the admin whitelist" while the SAME admin omitting the flag succeeded.
//
// ─── What this file changes, and what it deliberately does not ──────────────
//
// It does NOT close the vocabulary. §6.2 T2-5 records NO DECISION EVIDENCE
// FOUND that event_type should be open, and none that it should be closed, so a
// CHECK or a Go reject-list over EventVocabulary would be inventing a MUST the
// adjudication says has no source. Two facts make that the right way to leave
// it, rather than merely the cautious way:
//
//   - The aihub#445 disanalogy. There the un-listed value was structurally
//     UNRECALLABLE — a memory whose type carried no legal prefix could never be
//     embedded, filtered or rendered, so the row was write-only data and only
//     the column could make it impossible. An off-vocabulary EVENT is fully
//     readable: it appears unfiltered, and pf_read_events(types=[...]) will
//     return it if you name it. The harm is that you have to know to name it —
//     which is a publication problem, and is what this file fixes.
//   - Closing it is a compatibility event with an unmeasured blast radius.
//     docs/design/polyforge-v1-design.md §19.0.1 already treats ADDING a type as
//     one; removing the ability to add one at all is strictly larger, and
//     §6.4 item 5 records that the live distinct set has never been read.
//
// What it does instead: publishes the vocabulary (EventVocabulary, put on the
// wire by internal/mcp/tools_events.go), and makes the three sets that ARE
// enforced consistent — by construction where possible, by test where not.
//
// ─── The census §6.4 item 5 asks for, as far as it can be taken here ────────
//
// "How many distinct event_type strings are actually in flight" needs a read of
// the production table, which is inside the compose network (docs/deployment.md)
// and was not reachable from where this was written. What WAS reachable is the
// CALLER corpus — the polyforge transcript JSONL this machine holds, the same
// corpus aihub#412 used. Measured 2026-09-08 over 2,244 transcripts:
//
//	pf_emit_event    457 calls, TWO distinct event_type strings:
//	                 note (455) and pr_opened (2). Zero sent admin:true.
//	                 Zero omitted work_item_id.
//	pf_read_events   152 calls, 48 of them passing `types`, THIRTY-FOUR distinct
//	                 values — of which roughly half can never match anything:
//	                 wi_cancelled, attempt_claimed, wi_updated, wi_claimed,
//	                 attempt_lost_lease, wi_wrapped, wrapped, goal_changed,
//	                 wi_created, claimed, wi_note, correction, attempt_paused,
//	                 wi_rhs_changed (plus two deliberate probes).
//
// Read those two numbers together, because they say opposite-looking things and
// the pair is the argument for this file. The WRITE path is not where invented
// names come from — callers send `note`. The READ path is: a caller with no
// published vocabulary guesses, and a guess that matches nothing is returned as
// an empty list, indistinguishable from "it did not happen". So the vocabulary
// is published on the write tool (where the ruling puts it) and the read tool's
// `types` says plainly that it filters rather than validates.
//
// ⚠️ This corpus bounds MCP callers only. It says nothing about direct HTTP
// callers of POST /v1/events, and nothing about what the table already holds.
// EventVocabulary is therefore a claim about THIS TREE plus one retired
// publisher, not a claim that no other string exists in the column.

// ─── The vocabulary ─────────────────────────────────────────────────────────

// EventVocabulary is every event_type this tree can produce, plus the ones its
// enforcement sets name without producing.
//
// Grouped by origin and sorted WITHIN each group, because where an event comes
// from is the thing a reader needs and a flat alphabetical list hides it. The
// copy that goes on the wire is sorted globally (internal/mcp/tools_events.go).
// event_types_test.go enforces the shape, the deduplication and the per-group
// ordering, and enforces that every member of the three sets below appears here.
//
// 🔴 It is also checked AGAINST THE SOURCE. TestEventVocabulary_CoversEveryEmitter
// scans the non-test tree for the literals this server files events under and
// fails on any it cannot find here. That gate is the point of the file: a
// published list with no drift guard is wrong within weeks, and adding a type is
// already a declared compatibility event (§19.0.1), so the person adding one is
// the right person to publish it.
var EventVocabulary = []string{
	// ── work item lifecycle ──
	"dependency_created",         // internal/domain/dependencies.go
	"wi_classification_missing",  // internal/domain/gc.go (alert, rate-limited)
	"wi_classification_resolved", // internal/domain/run_attempts.go
	"wi_content_updated",         // internal/domain/work_items.go
	"wi_goal_updated",            // internal/domain/work_items.go
	"wi_needs_attention",         // internal/domain/gc.go (alert, rate-limited)
	"wi_reclassified",            // internal/domain/work_items.go
	"wi_stalled",                 // internal/server/routes_step.go
	"wi_unblocked",               // internal/domain/dependencies.go (no actor)
	"work_item_filed",            // internal/domain/work_items.go

	// ── attempt lifecycle ──
	"attempt_completed",  // internal/domain/run_attempts.go (no actor)
	"attempt_started",    // internal/domain/run_attempts.go
	"attempt_superseded", // internal/domain/run_attempts.go
	"force_takeover",     // internal/domain/run_attempts.go

	// ── step timeline ──
	"step_completed", // internal/server/routes_step.go
	"step_failed",    // internal/server/routes_step.go, internal/domain/run_attempts.go
	"step_started",   // internal/server/routes_step.go

	// ── resources and locks (aihub#343; no backfill exists or can exist) ──
	"lock_acquired",        // internal/domain/resource_events.go, one PER LOCK ROW
	"lock_released",        // internal/domain/resource_events.go, one PER LOCK ROW
	"wi_resources_updated", // internal/domain/resource_events.go

	// ── memory ──
	"memory_activated",       // internal/domain/memory.go
	"memory_commit_deleted",  // internal/domain/memory.go
	"memory_commit_edited",   // internal/domain/memory.go
	"memory_commit_replied",  // internal/domain/memory.go
	"memory_commit_resolved", // internal/domain/memory.go
	"memory_committed",       // internal/domain/memory.go
	"memory_created",         // internal/domain/memory.go
	"memory_redacted",        // internal/domain/memory.go
	"memory_reinforced",      // internal/server/routes_memory.go
	"memory_updated",         // internal/server/routes_memory.go

	// ── written by a CALLER through POST /v1/events ──
	// `note` is 455 of the 457 measured pf_emit_event calls. The other three are
	// emitted best-effort by the coding tools (internal/mcp/tools_coding.go,
	// emitCodingEvent), which reach this same endpoint.
	"commit",
	"note",
	"pr_opened",
	"push",

	// ── system / GC ──
	"partition_created", // internal/domain/gc.go
	"system_gc",         // internal/domain/gc.go

	// ── admin ──
	"admin_unblock", // internal/server/router.go — the only admin_* with an emitter

	// ── DECLARED BUT UNEMITTED: named by an enforcement set below or by
	// chk_evt_work_item_id, and produced by nothing in this tree. A caller with
	// the right role is the only way any of these reaches the table, which is
	// exactly why the admin gates over them have to be coherent.
	"admin_force_takeover",
	"admin_gc_manual",
	"admin_redact",
	"memory_archived",
	"memory_gc",
	"phase_config_updated",
	"system_force_takeover",

	// ── RETIRED: aihub#446 removed pf_adopt_artifact / pf_close_artifact /
	// pf_ignore_artifact, which were the only publishers. Rows already in the
	// table keep the type, so a reader filtering history still needs the name.
	"artifact_action",
}

// ─── The three sets that ARE enforced ───────────────────────────────────────

// AdminOnlyEventTypes always require role=admin, whatever the `admin` flag says.
//
// The H6 forgery guard: without it a non-admin could file an admin event simply
// by omitting admin=true, and the audit trail would carry an admin action nobody
// with admin rights performed.
var AdminOnlyEventTypes = []string{
	"admin_force_takeover",
	"admin_gc_manual",
	"admin_redact",
	"admin_unblock",
}

// adminAlsoPermittedWithFlag are types that do NOT require admin role but that
// an admin may legitimately mark admin=true on (design §5.2 H10).
var adminAlsoPermittedWithFlag = []string{
	"attempt_superseded",
	"phase_config_updated",
	"wi_classification_missing",
	"wi_needs_attention",
}

// NullWorkItemEventTypes is the Go mirror of the chk_evt_work_item_id CHECK: the
// event types that may be filed with no work_item_id.
//
// 🔴 It exists so the refusal is a 400 naming the field instead of a 500 carrying
// the driver's constraint text. Until aihub#444 this CHECK was enforced in the
// DATABASE ONLY, so POST /v1/events with no work_item_id and any other type went
// all the way to the INSERT, came back SQLSTATE 23514, and EmitEvent wrapped it
// as ErrInternalError. That is the aihub#433 failure mode — a caller error
// reported as a server fault, which sends the reader to the server logs instead
// of to their own request — and the aihub#411 §6.1 T1-4 policy stated in
// internal/domain/work_item_fields.go says the opposite: a vocabulary a DB CHECK
// enforces is ALSO validated in Go and answered with a 400 naming the field; the
// CHECK is the last line of defence, never the caller-facing one.
//
// Equivalence with the CHECK is a REQUIREMENT, not a coincidence.
// event_types_test.go parses 0036_agent_events_admin_gc_manual.sql and fails if
// this list and that CHECK stop naming the same set. It needs no database, so it
// runs in the default `go test ./...`.
//
// The direction of any future disagreement matters more than the fact of it. Go
// STRICTER than the CHECK is inert (Go refuses first, the row never reaches the
// column). Go WIDER than the CHECK rebuilds the 500 this list was added to
// remove. So if these two ever have to differ, narrow Go — never widen it.
var NullWorkItemEventTypes = []string{
	"admin_force_takeover",
	"admin_gc_manual", // aihub#444, migration 0036 — see adminEventWhitelist
	"admin_redact",
	"admin_unblock",
	"memory_activated",
	"memory_archived",
	"memory_commit_deleted",
	"memory_commit_edited",
	"memory_commit_replied",
	"memory_commit_resolved",
	"memory_committed",
	"memory_created",
	"memory_gc",
	"memory_redacted",
	"memory_reinforced",
	"memory_updated",
	"partition_created",
	"phase_config_updated",
	"system_force_takeover",
	"system_gc",
	"wi_classification_missing",
	"wi_needs_attention",
}

// AdminEventWhitelist is the set of event types an `admin: true` call may carry.
//
// 🔴 It is DERIVED — AdminOnlyEventTypes ∪ adminAlsoPermittedWithFlag — and that
// is the whole repair. Before aihub#444 the two lists were written out
// independently and had drifted apart by one entry, `admin_gc_manual`, which
// made setting an HONEST flag strictly more restrictive than omitting it:
//
//	role   admin flag   event_type        before        after
//	admin  true         admin_gc_manual   403 refused   accepted
//	admin  (omitted)    admin_gc_manual   accepted      accepted
//	other  true         admin_gc_manual   403 refused   403 refused
//	other  (omitted)    admin_gc_manual   403 refused   403 refused
//
// The top-left cell is the defect. `admin: true` is documented as "this is an
// admin event"; a caller who declares what they are doing must never be refused
// where a caller who says nothing succeeds, because the only behaviour that
// teaches is to stop declaring. Deriving the union means no future edit can
// reintroduce it: an entry added to AdminOnlyEventTypes is in the whitelist the
// same instant, and event_types_test.go asserts the containment as well, so the
// invariant survives someone rewriting the derivation.
//
// Note what is NOT claimed: the two sets are not made EQUAL. The four extras are
// types a non-admin may also emit, and folding them into AdminOnlyEventTypes
// would forbid that. Containment in one direction is the invariant; equality
// would be a different, wrong, rule.
var AdminEventWhitelist = buildAdminEventWhitelist()

func buildAdminEventWhitelist() []string {
	out := make([]string, 0, len(AdminOnlyEventTypes)+len(adminAlsoPermittedWithFlag))
	out = append(out, AdminOnlyEventTypes...)
	out = append(out, adminAlsoPermittedWithFlag...)
	sort.Strings(out)
	return out
}

// ─── Lookup sets, built once ────────────────────────────────────────────────

var (
	adminOnlyEventTypes  = setOf(AdminOnlyEventTypes)
	adminEventWhitelist  = setOf(AdminEventWhitelist)
	nullWorkItemEventSet = setOf(NullWorkItemEventTypes)
)

func setOf(vals []string) map[string]bool {
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		m[v] = true
	}
	return m
}
