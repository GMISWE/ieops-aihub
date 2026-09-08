package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// notePayload builds the payload for a `note` event. One definition, because
// pf_emit_event's callers have always written `{text: "..."}` by hand and the
// fused note on the terminal calls (aihub#290) has to land in the same shape or
// the UI's event rendering and every existing timeline diverge for no reason.
func notePayload(text string) map[string]any {
	return map[string]any{"text": text}
}

// emitNote posts a `note` event using an already-resolved state file.
//
// Unlike emitCodingEvent this RETURNS its error rather than swallowing it. The
// note fused onto pf_wrap / pf_complete_attempt (aihub#290) is the wi's closing
// statement, and it is emitted at the one moment it can never be re-sent: the
// terminal call that follows deletes the state file, so a silently-lost note is
// lost permanently. Callers report the failure in the response instead of
// failing the wrap over it — the wrap itself is the more important half.
func (s *Server) emitNote(ctx context.Context, wiID string, sf *config.StateFile, text string) error {
	_, err := s.client.EmitEvent(ctx, map[string]any{
		"work_item_id":   wiID,
		"attempt_id":     sf.AttemptID,
		"claim_epoch":    sf.ClaimEpoch,
		"session_secret": sf.SessionSecret,
		"event_type":     "note",
		"payload":        notePayload(text),
	})
	return err
}

// applyNoteResult records on a tool response whether a fused note reached the
// timeline. Always sets note_emitted when a note was requested: "the field is
// absent" and "the note failed" must not look alike to the caller.
func applyNoteResult(result map[string]any, requested bool, err error) {
	if !requested {
		return
	}
	result["note_emitted"] = err == nil
	if err != nil {
		result["note_error"] = err.Error()
	}
}

// noteOutcomeSuffix renders the fused note's fate as a clause to append to an
// error message.
//
// The success path reports the note through applyNoteResult, but the terminal
// call can fail AFTER the note was already emitted — and that is exactly the
// case where the caller most needs to know, because it is about to retry. A bare
// "complete_attempt: ..." leaves it unable to tell whether retrying will
// duplicate the note or supply one that never landed. Returns "" when no note
// was requested, so ordinary errors are unchanged.
func noteOutcomeSuffix(requested bool, err error) string {
	switch {
	case !requested:
		return ""
	case err != nil:
		return " (the closing note was NOT recorded either: " + err.Error() + ")"
	default:
		return " (the closing note WAS already recorded; retrying this call will record it a second time)"
	}
}

// emitEventPayloadPropDescription is pf_emit_event's `payload` description.
//
// aihub#486, and it is spelled out here instead of taking
// jsonObjectPropNote because of the one clause that string ends with. `payload`
// is the single guarded jsonb object parameter with a REAL size cap — 65,536
// bytes, checked in internal/domain/memory.go (`EmitEvent`) ABOVE the shape
// guard — so "size is never the reason" would be a false statement published to
// every caller of this tool. internal/domain/work_items.go's
// jsonObjectParamSizeNote makes exactly the same split on the error-message
// side, and for the same reason.
//
// The rest matches the shared note deliberately: the mistake and its repair are
// identical, and the repair sentence is the one a caller cannot guess — 18 of
// the 19 stringified values in the measured corpus were hand-escaped JSON that
// came out malformed rather than a client wrapping a good object.
const emitEventPayloadPropDescription = "Event payload (arbitrary JSON object). " +
	"Must be a JSON object: a string (including a JSON-encoded string of the object you meant), " +
	"an array, a number or a boolean is rejected with 400 naming the type received, and no event is recorded. " +
	"Do not hand-write the escaped JSON — send the object and let your client serialise it. " +
	"Unlike the other jsonb object parameters, size CAN be the reason here: payload is capped at 64 KB " +
	"and that cap is checked BEFORE the shape check."

// emitEventTypePropDescription is pf_emit_event's `event_type` description, and
// the publication of the event vocabulary that aihub#411 §6.2 T2-5 ruled for.
//
// ─── Published, and deliberately NOT as an `enum` ───────────────────────────
//
// The owner ruling reads "publish the event vocabulary as an enum on
// pf_emit_event", and the CAUTION attached to that same ruling is why the `enum`
// KEY is not used: an enum on an MCP schema is ADVISORY. polyforge registers
// through the untyped (*mcp.Server).AddTool method, whose callTool path invokes
// the handler with no schema step (measured on go-sdk v1.6.0; written out in
// internal/domain/user_fields.go), and the server does not reject off-list
// values either. Publishing 45 names under `enum` would state a closed contract
// that neither this process nor the server keeps — which is precisely what
// aihub#445 WITHDREW from pf_remember in commit f128b69, which is this change's
// base — reversing it in the very next change would leave the repo asserting
// both halves of one question.
//
// So the ruling's substance is delivered — the vocabulary is on the wire, where
// a caller and a model both see it — and the one word that would have made it a
// lie is not. The same principle in both directions: the published set and the
// enforced set must be one set (aihub#238, aihub#463). What IS enforced is
// stated first, in the same breath, with the status code each rule answers.
//
// ─── Cost, measured rather than waved at ────────────────────────────────────
//
// Measured, not estimated. This property goes from 53 to 1,735 chars; `types`
// from 226 to 592 and `user_id` from 14 to 316 on the sibling tool, so aihub#444
// spends 2,350 chars — roughly 590 tokens — resident on every tools/list. That is
// five times the +437 chars aihub#343 spent on this same tool pair and justified
// in the same terms, so it needs its own reason rather than that precedent.
//
// The reason is the measurement in internal/domain/event_types.go: across 2,244
// transcripts, callers filtering pf_read_events sent THIRTY-FOUR distinct type
// strings and about half name nothing any code path can emit. Every one of those
// calls returned 200 with an empty list. A vocabulary nobody publishes is
// guessed, and a wrong guess here is not an error — it is a confident, empty,
// wrong answer, which is the one failure mode extra bytes can actually buy off.
//
// What was cut to get there, so the next person does not re-add it: the pointer
// to docs/design/polyforge-v1-design.md section 19.0.1 (adding a type is a
// compatibility event) is a fact for whoever ADDS a type, not for the caller
// reading the schema, and it is stated in event_types.go where that person is.
// One copy of the 45-name list also serves both tools — `types` points here
// rather than repeating it.
//
// Built from domain.EventVocabulary, domain.AdminOnlyEventTypes and
// domain.AdminEventWhitelist so the published text cannot drift from the sets
// the server actually enforces. tools_events_vocab_test.go holds both halves:
// no `enum` key, and a description that names every vocabulary entry.
func emitEventTypePropDescription() string {
	vocab := append([]string(nil), domain.EventVocabulary...)
	sort.Strings(vocab)

	// The whitelist minus the admin-only set: naming the four extras is enough
	// once the admin-only four have just been listed, and repeating all eight
	// would spend wire bytes restating a containment the sentence states.
	adminOnly := make(map[string]bool, len(domain.AdminOnlyEventTypes))
	for _, t := range domain.AdminOnlyEventTypes {
		adminOnly[t] = true
	}
	var extras []string
	for _, t := range domain.AdminEventWhitelist {
		if !adminOnly[t] {
			extras = append(extras, t)
		}
	}

	return "Event type. NOT a closed set: agent_events.event_type is TEXT with no CHECK and this " +
		"endpoint stores whatever it is sent, so an off-list value is accepted and a typo becomes an " +
		"event nobody thinks to look for. The vocabulary below is therefore PUBLISHED, not enforced. " +
		"THREE rules ARE enforced: (1) " + strings.Join(domain.AdminOnlyEventTypes, " / ") +
		" always require role=admin whatever `admin` says — 403; (2) with admin:true the type must be " +
		"one of those four or " + strings.Join(extras, " / ") + " — 403; (3) with no work_item_id the " +
		"type must be one the agent_events.chk_evt_work_item_id CHECK permits — 400, and the message " +
		"lists them. KNOWN TYPES, published so pf_read_events(types=[...]) can name them instead of " +
		"guessing: " + strings.Join(vocab, ", ") + ". Callers of this tool normally send `note`; " +
		"commit / push / pr_opened are emitted here by pf_commit / pf_push / pf_pr; the rest are " +
		"written by the server."
}

// readEventsTypesPropDescription is pf_read_events' `types` description.
//
// aihub#411 §6.2 T2-5 names this string directly: it called itself a "whitelist",
// and it is a FILTER. The two words differ in the only way that matters here — a
// whitelist REJECTS what is not on it, and this rejects nothing. An unrecognised
// value produces `event_type IN ('typo')`, which matches no row, and the call
// answers 200 with an empty list.
//
// 🔴 Why that wording was worth changing rather than merely imprecise: measured
// over 2,244 transcripts, 48 pf_read_events calls passed `types` carrying THIRTY-
// FOUR distinct values, of which roughly half — wi_cancelled, attempt_claimed,
// wi_updated, wi_claimed, attempt_lost_lease, wi_wrapped, wrapped, goal_changed,
// wi_created, claimed, wi_note, correction, attempt_paused, wi_rhs_changed —
// name nothing any code path emits. Every one of those calls came back 200 and
// empty, and "whitelist" is the word that makes an empty result read as an
// answer. This is aihub#259's failure mode surviving its own fix: that work item
// made the parameter reach the server, and a parameter that filters correctly on
// a name that cannot exist is still a false green.
//
// The vocabulary is published on pf_emit_event.event_type rather than repeated
// here, because one copy on the wire is the whole budget these two tools share.
const readEventsTypesPropDescription = "Filter by event type. A FILTER, not a whitelist and not a " +
	"validator: an unrecognised value is NOT rejected, it matches nothing, so a typo, a type that has " +
	"never existed and a real event that did not happen all return the same empty list — measured, " +
	"about half of the distinct values callers have passed here name nothing any code path emits. " +
	"The vocabulary is published on pf_emit_event.event_type. Lock churn is lock_acquired/" +
	"lock_released, declaration changes wi_resources_updated. A claim emits one lock_acquired PER " +
	"declared path, so unfiltered these can fill a page (default limit 50)."

// readEventsUserIDPropDescription is pf_read_events' `user_id` description.
//
// aihub#411 §6.2 T2-18: three distinct identities exist — REPORTER
// (work_items.reporter_user_id), ATTEMPT OWNER (run_attempts + claim_epoch +
// session_secret_hash) and WATCHER (wi_watches, migration 0033) — and no tool
// stated which one its user_id-shaped parameter meant. This one said "Filter by
// user".
//
// It is none of the three. internal/domain/memory.go (ListEvents) compares
// e.actor_user_id, the id stamped on the event by whoever emitted it — a FOURTH
// identity, and the row's answer is that naming one of the three would have been
// wrong. aihub#383 already made pf_list_work_items.user_id honest as REPORTER,
// so the two now say different things because they DO different things.
//
// The NULL clause is not a caveat added for completeness. Several event types
// are inserted with no actor at all — wi_unblocked (dependencies.go),
// attempt_completed and the step_failed written by the attempt timeout path
// (run_attempts.go), and everything the GC sweeps file (gc.go) — and
// `actor_user_id = $n` never matches NULL. So this filter silently drops exactly
// the events nobody performed, which is a reasonable thing for it to do and an
// unreasonable thing for a caller to have to discover.
const readEventsUserIDPropDescription = "Filter by ACTOR: matches agent_events.actor_user_id, i.e. " +
	"who EMITTED the event. NOT the work item's reporter (that is pf_list_work_items.user_id), NOT " +
	"the attempt owner, NOT a watcher. Server-written events carry no actor — GC sweeps, " +
	"wi_unblocked, attempt_completed — so any value of this filter excludes them."

// emitEventSchema is pf_emit_event's published InputSchema.
//
// Extracted from the registration (aihub#444) so a test can decode the SAME
// value the server publishes. Asserting on emitEventTypePropDescription() alone
// would pass while the registration still published the old three-example
// string — the shape of defect aihub#259 is: a thing that is correct in this
// process and never reaches the wire.
func emitEventSchema() json.RawMessage {
	return objectSchema(map[string]any{
		"work_item_id": prop("string", "Work item ID"),
		"event_type":   prop("string", emitEventTypePropDescription()),
		"payload":      prop("object", emitEventPayloadPropDescription),
		"pinned":       prop("boolean", "Pin this event (surfaces first in status/resume)"),
		"admin":        prop("boolean", "Admin event (requires role=admin)"),
	}, []string{"work_item_id", "event_type", "payload"})
}

// readEventsSchema is pf_read_events' published InputSchema. Extracted for the
// same reason as emitEventSchema.
func readEventsSchema() json.RawMessage {
	return objectSchema(map[string]any{
		"work_item_id": prop("string", "Work item ID (or use project)"),
		"project":      prop("string", "Project name (or use work_item_id)"),
		"user_id":      prop("string", readEventsUserIDPropDescription),
		"types":        prop("array", readEventsTypesPropDescription),
		// aihub#425. Unlike pf_recall's cursor, this one was NOT merely
		// unpublished — it was never put on the wire either, so this tool
		// needed both halves. handleListEvents has always bound it and
		// ListEvents has always returned next_cursor, so before this change a
		// caller holding a next_cursor had no way to spend it and the second
		// page of any event stream was unreachable from MCP. That is the
		// aihub#259 shape (a parameter that never leaves this process), and
		// the reason it is worse than a missing feature is the same: the
		// answer looks complete.
		"cursor": prop("string", "Opaque page token — pass a previous response's next_cursor "+
			"to continue after the last event it returned."),
		"since":        prop("string", "Since timestamp (RFC3339)"),
		"limit":        prop("string", "Max events to return"),
		"pinned_first": prop("boolean", "Return pinned events first"),
	}, nil)
}

func (s *Server) registerEventTools() {
	// pf_emit_event
	s.addTool(&sdkmcp.Tool{
		Name:        "pf_emit_event",
		Description: "Emit an event on a work item. Mutating — credentials injected from state file.",
		InputSchema: emitEventSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		eventType := strArg(args, "event_type")
		if eventType == "" {
			return errResult(fmt.Errorf("event_type is required"))
		}

		// Inject credentials from state file
		sf, err := config.ResolveStateFile(wiID)
		if err != nil {
			return errResult(config.StateFileMissingErr(wiID, err))
		}

		body := map[string]any{
			"work_item_id":   wiID,
			"attempt_id":     sf.AttemptID,
			"claim_epoch":    sf.ClaimEpoch,
			"session_secret": sf.SessionSecret,
			"event_type":     eventType,
			"payload":        args["payload"],
		}
		if boolArg(args, "pinned") {
			body["pinned"] = true
		}
		if boolArg(args, "admin") {
			body["admin"] = true
		}

		result, err := s.client.EmitEvent(ctx, body)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_read_events
	s.addTool(&sdkmcp.Tool{
		Name: "pf_read_events",
		// aihub#343: the cutover caveat is ON THE TOOL, not only in the docs
		// (docs/design/polyforge-v1-design.md §19.0). The failure mode that work
		// item exists to prevent is a reader taking an empty stream as proof that
		// nothing happened — and the reader at risk is the one who never opens
		// the source. Lock and declared_resources events did not exist before the
		// aihub#343 DEPLOY, and nothing could be backfilled because resource_locks
		// keeps no trace of a deleted row.
		//
		// ⚠️ Deploy, not commit, and the wording says so: aihub rollouts need an
		// explicit human instruction and can trail the merge by days. A hard
		// start date would be wrong in the direction that matters — during that
		// gap there are still no events, and a reader holding the commit date
		// would read the emptiness as "the recorder was running and saw nothing".
		//
		// Cost, measured rather than waved at: the two additions here are +437
		// chars of wire text on every tools/list — ~109 tokens, 0.66% of the
		// 66,091 chars of quoted schema text in internal/mcp/tools_*.go. Kept
		// because the empty result this warns about is the exact thing the tool
		// would otherwise be read as proving. (An earlier draft cost +542; the
		// prose was cut, not the two facts.)
		Description: "Read events for a work item or project. work_item_id or project must be provided. " +
			"NOTE: lock_acquired / lock_released / wi_resources_updated exist only from the deploy that " +
			"shipped aihub#343 (2026-09-03 at the earliest; no backfill) — their absence before then is " +
			"not evidence that no lock or declaration changed.",
		InputSchema: readEventsSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		project := strArg(args, "project")
		if wiID == "" && project == "" {
			return errResult(fmt.Errorf("work_item_id or project is required"))
		}
		params := url.Values{}
		setIfNonempty(params, "work_item_id", wiID)
		setIfNonempty(params, "project", project)
		setIfNonempty(params, "user_id", strArg(args, "user_id"))
		// aihub#259: `types` was published in the schema above and never put on
		// the wire, so GET /v1/events was always called unfiltered. Neither end
		// was broken — the handler splits `types` on commas and ListEvents turns
		// it into `event_type IN (...)` — the parameter simply never left this
		// process, which is why a type that cannot exist filtered nothing and
		// returned the full stream.
		//
		// That shape is the reason this is worse than a missing feature. The
		// parameter's main use is checking whether an irreversible operation
		// happened ("was any of these cancelled?"), and a silently unfiltered
		// answer is a false green in BOTH directions: a non-empty result reads as
		// "those events exist" when they are some other type entirely, and not
		// finding a work item in the result reads as "it was never cancelled"
		// when no filtering ever occurred. ieops#680's executor came within one
		// step of publishing a "zero cancels" report over 44 real cancellations.
		//
		// csvArg, not strSliceArg: the parameter arrives from the model as a JSON
		// array and GET /v1/events parses it with strings.Split(...,","), so the
		// array has to be rendered comma-separated. csvArg also accepts a bare
		// string, which is the aihub#280 lesson — a caller sending the scalar form
		// of an array-typed param must not be silently dropped either.
		setIfNonempty(params, "types", csvArg(args, "types"))
		setIfNonempty(params, "cursor", strArg(args, "cursor"))
		setIfNonempty(params, "since", strArg(args, "since"))
		setIfNonempty(params, "limit", strArg(args, "limit"))
		if boolArg(args, "pinned_first") {
			params.Set("pinned_first", "true")
		}
		result, err := s.client.ReadEvents(ctx, params)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})
}
