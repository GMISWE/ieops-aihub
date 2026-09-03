package mcp

import (
	"context"
	"fmt"
	"net/url"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
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

func (s *Server) registerEventTools() {
	// pf_emit_event
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_emit_event",
		Description: "Emit an event on a work item. Mutating — credentials injected from state file.",
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID"),
			"event_type":   prop("string", "Event type (e.g. note, wi_reclassified, step_started)"),
			"payload":      prop("object", "Event payload (arbitrary JSON object)"),
			"pinned":       prop("boolean", "Pin this event (surfaces first in status/resume)"),
			"admin":        prop("boolean", "Admin event (requires role=admin)"),
		}, []string{"work_item_id", "event_type", "payload"}),
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
			return errResult(fmt.Errorf("read state file (wi must be claimed first): %w", err))
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
	s.mcp.AddTool(&sdkmcp.Tool{
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
		InputSchema: objectSchema(map[string]any{
			"work_item_id": prop("string", "Work item ID (or use project)"),
			"project":      prop("string", "Project name (or use work_item_id)"),
			"user_id":      prop("string", "Filter by user"),
			"types": prop("array", "Filter by event types (whitelist). Lock churn is "+
				"lock_acquired/lock_released, declaration changes wi_resources_updated. A claim emits one "+
				"lock_acquired PER declared path, so unfiltered these can fill a page (default limit 50)."),
			"since":        prop("string", "Since timestamp (RFC3339)"),
			"limit":        prop("string", "Max events to return"),
			"pinned_first": prop("boolean", "Return pinned events first"),
		}, nil),
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
