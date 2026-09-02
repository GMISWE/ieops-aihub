package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/domain"
)

func (s *Server) registerMemoryTools() {
	// pf_remember
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_remember",
		Description: "Store a memory in aihub. type must use full name (e.g. experience.debug). Rejects methodology.* types — write spec/plan/review/execute/retro/wrap_summary via pf_save_artifact.",
		InputSchema: rememberSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		if err := validatePfRememberArgs(args); err != nil {
			return errResult(err)
		}
		result, err := s.client.Remember(ctx, args)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_recall
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_recall",
		Description: "Recall memories from aihub with optional semantic search. type is an ARRAY of type names, e.g. [\"experience.*\",\"rule.work\"] — one filter per entry; a '|' inside an entry is NOT a separator and is rejected. An entry ending in .* is a prefix wildcard. Any entry matching no memory comes back in unmatched_types, which distinguishes a wrong type name from a project that genuinely holds no such memory. An item with content_truncated=true holds only a prefix of its content (content_full_len = full length); call pf_get_memory(memory_id) for the rest.",
		InputSchema: recallSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		if strArg(args, "project") == "" {
			return errResult(fmt.Errorf("project is required"))
		}
		result, err := s.client.Recall(ctx, buildRecallParams(args))
		if err != nil {
			return errResult(err)
		}
		// aihub#313: `fields` is deliberately NOT forwarded by buildRecallParams.
		//
		// That forwarding loop is the hop aihub#148 is about. `similarity_threshold`
		// is published in this very InputSchema; until aihub#148 it was never
		// forwarded there and never parsed by handleRecall either, while being fully
		// implemented in domain — so nothing reached it. Confirmed live on the
		// pre-fix build: passing 0.99 and passing nothing returned the same 20 items
		// in the same order, min similarity 0.154, differing only in
		// effective_strength's 10th decimal (decay between the two calls).
		//
		// `fields` cannot repeat that because it has no server hop to be dropped on.
		// The projection is a property of what the MCP process HANDS THE MODEL, and
		// this process is the last hop before the model, so the parameter is consumed
		// exactly where it is read: one hop, no wire contract, no REST parse, no
		// domain change. Adding it to the loop would be strictly worse — handleRecall
		// would ignore the query param (a third instance of aihub#148), and
		// routes_memory.go is aihub#309's declared file besides.
		//
		// This is the OPPOSITE error from aihub#287's 4_wrong_landing_point ("only
		// building the 4th hop"): there domain was complete and the MCP hop missing;
		// here the MCP hop is the only one that can implement the feature at all.
		return jsonResultCompact(slimRecallResultMode(result, strArg(args, "fields") == "brief"))
	})

	// pf_get_memory — aihub#269. pf_recall truncates content to 800 runes and
	// flags it with content_truncated/content_full_len; without a by-id read those
	// flags would tell the model its text is incomplete while giving it no way to
	// complete it. This is the tool half of that escape hatch.
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_get_memory",
		Description: "Fetch one memory by id with its FULL, untruncated content — the follow-up read for a pf_recall item whose content_truncated is true.",
		InputSchema: getMemorySchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		memID := strArg(args, "memory_id")
		if memID == "" {
			return errResult(fmt.Errorf("memory_id is required"))
		}
		result, err := s.client.GetMemory(ctx, memID)
		if err != nil {
			return errResult(err)
		}
		// compact, not indented: this payload is read by the model, and it is
		// reached precisely when the content is long (same rationale as pf_recall).
		return jsonResultCompact(result)
	})

	// pf_activate_memory
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_activate_memory",
		Description: "Activate a memory (increments activation count, updates stability)",
		InputSchema: activateMemorySchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		memID := strArg(args, "memory_id")
		if memID == "" {
			return errResult(fmt.Errorf("memory_id is required"))
		}
		result, err := s.client.ActivateMemory(ctx, memID)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_reinforce_memory
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_reinforce_memory",
		Description: "Reinforce a memory with additional context (mutating — credentials from state file)",
		InputSchema: reinforceMemorySchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		memID := strArg(args, "memory_id")
		if memID == "" {
			return errResult(fmt.Errorf("memory_id is required"))
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required for credential injection"))
		}
		sf, err := config.ResolveStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
		}
		result, err := s.client.ReinforceMemory(ctx, memID, buildReinforceMemoryBody(args, sf))
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_update_memory (aihub#201)
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_update_memory",
		Description: "Update a memory (creates a new version and advances the latest_id cursor). Credentials injected from state file.",
		InputSchema: updateMemorySchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		memID := strArg(args, "memory_id")
		if memID == "" {
			return errResult(fmt.Errorf("memory_id is required"))
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required for credential injection"))
		}
		sf, err := config.ResolveStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
		}
		result, err := s.client.UpdateMemory(ctx, memID, buildUpdateMemoryBody(args, sf))
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_redact_memory
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_redact_memory",
		Description: "Redact (soft-delete) a memory",
		InputSchema: redactMemorySchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		memID := strArg(args, "memory_id")
		if memID == "" {
			return errResult(fmt.Errorf("memory_id is required"))
		}
		result, err := s.client.RedactMemory(ctx, memID, buildRedactMemoryBody(args))
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_save_artifact
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_save_artifact",
		Description: "Save a methodology artifact (methodology.spec|plan|review|execute|retro|wrap_summary). Credentials injected from state file.",
		InputSchema: saveArtifactSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		artifactType := strArg(args, "type")
		if artifactType == "" {
			return errResult(fmt.Errorf("type is required"))
		}
		wiID := strArg(args, "work_item_id")
		if wiID == "" {
			return errResult(fmt.Errorf("work_item_id is required"))
		}
		artifactContent, err := resolveArtifactContent(args, config.WorkspaceRoot())
		if err != nil {
			return errResult(err)
		}

		sf, err := config.ResolveStateFile(wiID)
		if err != nil {
			return errResult(fmt.Errorf("read state file: %w", err))
		}

		result, err := s.client.Remember(ctx, buildSaveArtifactBody(args, sf, artifactContent))
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_adopt_artifact
	// B4: adopt/ignore/close are now pf_emit_event(type='artifact_action', payload={...})
	// These are convenience wrappers around pf_emit_event.
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_adopt_artifact",
		Description: "Mark an artifact as adopted (wrapper around pf_emit_event artifact_action)",
		InputSchema: artifactActionSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return s.emitArtifactAction(ctx, req, "adopt")
	})

	// pf_close_artifact
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_close_artifact",
		Description: "Mark an artifact as closed (wrapper around pf_emit_event artifact_action)",
		InputSchema: artifactActionSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return s.emitArtifactAction(ctx, req, "close")
	})

	// pf_ignore_artifact
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_ignore_artifact",
		Description: "Mark an artifact as ignored (wrapper around pf_emit_event artifact_action)",
		InputSchema: artifactActionSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return s.emitArtifactAction(ctx, req, "ignore")
	})

	// pf_resolve_commit
	s.mcp.AddTool(&sdkmcp.Tool{
		Name:        "pf_resolve_commit",
		Description: "Resolve a spec/plan commit annotation with an AI reply (marks status=resolved, emits memory_commit_resolved).",
		InputSchema: resolveCommitSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		memID := strArg(args, "memory_id")
		if memID == "" {
			return errResult(fmt.Errorf("memory_id is required"))
		}
		commitID := strArg(args, "commit_id")
		if commitID == "" {
			return errResult(fmt.Errorf("commit_id is required"))
		}
		if strArg(args, "reply") == "" {
			return errResult(fmt.Errorf("reply is required"))
		}
		result, err := s.client.ResolveCommit(ctx, memID, commitID, buildResolveCommitBody(args))
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})
}

// ─── pf_recall's parameter contract, hops 1 and 2 (aihub#148) ────────────────
//
// Split out of the tool handler for the same reason buildListWorkItemsParams was
// (aihub#280): hop 2 has to be assertable on the values that actually reach the
// wire. `similarity_threshold` was published here, fully implemented in
// internal/domain/memory_vector.go, and carried by neither hop in between — and
// no test could see that, because the schema lived inside an AddTool literal and
// the forwarding lived inside a closure. Both are now named functions with a
// guard over them (recall_params_wiring_test.go).

// recallStringParams are the pf_recall arguments forwarded to GET /v1/memories
// verbatim as query strings.
//
// scalarArg, not strArg: `top_k` is published as a string, but "max results: 10"
// is most naturally written as a JSON *number*, and strArg returns "" for a
// non-string — so setIfNonempty dropped it and the server silently applied its
// own default page size of 20. The caller got a page it did not ask for with
// nothing anywhere to notice. Identical defect and identical fix to `limit` in
// buildListWorkItemsParams (aihub#280 B6).
//
// `cursor` is forwarded but deliberately not published: paging is driven by
// next_cursor from a previous response, not composed by the model.
var recallStringParams = []string{"project", "query", "visibility", "work_item_id", "top_k", "cursor"}

// recallNumberParams are the pf_recall arguments published as JSON numbers.
//
// Zero means "not specified" for all three, which is why they are not in the
// scalarArg loop above: 0 is similarity_threshold's OFF value, and min_strength
// / recency_weight both have server-side defaults that forwarding a literal 0
// would overwrite.
//
// 🔴 similarity_threshold has NO default and must keep none. Measured on
// project=ieops with limit=200: a pure-punctuation noise query scores 0.4712 at
// its WORST hit while a real Chinese query whose top hit is the correct answer
// scores 0.4798 at its BEST — 0.0086 apart, and the wrong way round for six of
// the noise query's hits. No global cutoff separates noise from signal, so the
// job here is to make the knob reachable, never to turn it on.
var recallNumberParams = []string{"similarity_threshold", "min_strength", "recency_weight"}

// recallSchema is pf_recall's published InputSchema — hop 1.
func recallSchema() json.RawMessage {
	return objectSchema(map[string]any{
		"project": prop("string", "Project name"),
		"query":   prop("string", "Semantic search query"),
		// aihub#289: the shape is the whole point of this description. Three
		// SKILL.md templates taught type="a|b|c", nothing split it, and the
		// resulting empty set read as "no relevant memory". The model reads this
		// string, so this string has to state the contract.
		"type":         prop("array", "Memory types to filter — an ARRAY of names, one per entry: [\"experience.*\",\"rule.work\"]. Entries ending in .* are prefix wildcards. Do NOT pack several types into one string with '|' — that is not a separator and is rejected with a 400."),
		"visibility":   prop("string", "Filter by visibility"),
		"work_item_id": prop("string", "Filter by work item ID"),
		"top_k": prop("string", "Max results (default 20, ceiling 200). A JSON number is also "+
			"accepted, and is what most callers send."),
		"similarity_threshold": prop("number", "Minimum cosine similarity, 0-1. Applies to the "+
			"semantic (vector) half of the recall only, and is OFF by default — scores are not "+
			"comparable across queries, so there is no safe global cutoff. A threshold that "+
			"matches nothing returns an empty list rather than falling back to text search: "+
			"empty is the intended answer when you set one."),
		"min_strength":     prop("number", "Min memory strength (default 0.3)"),
		"include_archived": prop("boolean", "Include archived memories (default false)"),
		"recency_weight":   prop("number", "Recency weight (default 0.3)"),
		// aihub#313. This string is charged on EVERY request of EVERY session,
		// whether or not pf_recall is called — the standing cost that closed
		// aihub#279 as net negative — so it is priced, not written to taste.
		// Measured on the REAL tools/list payload: +59 net (this property +64, the
		// Description reword above -5), against a pf_recall tool object of 415 tok
		// and a 50-tool block of 11,634. Three wordings were measured; the one
		// below is 36 tok cheaper than a version that also enumerated the kept
		// fields (redundant — the model can see them in the response) and 22 tok
		// dearer than one that dropped the rune cap and the dropped-field list
		// (NOT redundant — a caller that needs `related` has to learn brief drops
		// it before spending a call). Do NOT re-price this with
		// `dump-mcp-schemas`: its contract JSON omits per-property descriptions
		// and reports +18, understating the real cost 3x.
		//
		// Break-even: one briefed no-top_k call saves 5,200 tok x 47.3 re-billings
		// = ~246,000 tok, paying for ~4,150 requests of this standing cost, against
		// a measured density of 16 briefed calls per 63 requests.
		//
		// propEnum, not prop: `fields` conventionally names a field LIST, so
		// fields="id,type" is a natural guess that would silently return the full
		// 6,966-token response — the exact cost this exists to remove, with no
		// signal that the request was misunderstood. The enum makes the single
		// legal value discoverable. It stays advisory (the SDK does not reject
		// other values — verified, the wiring tests still pass while sending
		// "Brief"/"BRIEF"/""), so the safe "anything but brief == full" default
		// still holds for a client that ignores the enum.
		"fields": propEnum("string", "\"brief\" replaces each item body with its first line (<=120 runes) and drops related/tags; content_truncated marks the cut, pf_get_memory(id) returns the full text.", []string{"brief"}),
	}, []string{"project"})
}

// recallNumArg reads one of recallNumberParams, tolerating the JSON *string*
// spelling of a number.
//
// numArg alone would return 0 for `similarity_threshold: "0.99"`, and 0 is this
// tool's "not specified" — so a caller that quoted the value would have its
// filter silently discarded. That is defect 2 of aihub#148 (a value dropped
// because its wire shape disagrees with its declared type) pointed at the very
// parameter defect 1 is about, and nothing at any hop would have said so.
// Unparseable text still yields 0 rather than an error, matching what the whole
// forwarding block does with a value it cannot read.
func recallNumArg(args map[string]any, key string) float64 {
	if s, ok := args[key].(string); ok {
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return 0
		}
		return f
	}
	return numArg(args, key)
}

// buildRecallParams renders pf_recall's MCP arguments into the query string for
// GET /v1/memories — hop 2 of the four-hop contract.
func buildRecallParams(args map[string]any) url.Values {
	params := url.Values{}
	for _, k := range recallStringParams {
		setIfNonempty(params, k, scalarArg(args, k))
	}
	// Numbers, formatted as %g. A zero is "not specified" — see recallNumberParams.
	for _, k := range recallNumberParams {
		if v := recallNumArg(args, k); v != 0 {
			params.Set(k, fmt.Sprintf("%g", v))
		}
	}
	// type is an array — join as comma-separated
	if types, ok := args["type"]; ok {
		switch t := types.(type) {
		case []any:
			strs := make([]string, 0, len(t))
			for _, v := range t {
				if s, ok := v.(string); ok {
					strs = append(strs, s)
				}
			}
			if len(strs) > 0 {
				params.Set("type", strings.Join(strs, ","))
			}
		case string:
			params.Set("type", t)
		}
	}
	if boolArg(args, "include_archived") {
		params.Set("include_archived", "true")
	}
	// recall_algo: explicit arg wins, else env (POLYFORGE_RECALL_ALGO) lets a plugin
	// build opt into the opt③ L1 lexical-relevance recall path server-side without
	// changing the tool contract. Empty -> server default (recency).
	if algo := strArg(args, "recall_algo"); algo != "" {
		params.Set("recall_algo", algo)
	} else if algo := os.Getenv("POLYFORGE_RECALL_ALGO"); algo != "" {
		params.Set("recall_algo", algo)
	}
	return params
}

// validatePfRememberArgs enforces pf_remember's contract before the HTTP call:
// required fields present, and methodology.* rejected — those are wi-bound,
// credentialed artifacts that must be written via pf_save_artifact (aihub#210).
func validatePfRememberArgs(args map[string]any) error {
	for _, f := range []string{"project", "type", "content", "visibility"} {
		if strArg(args, f) == "" {
			return fmt.Errorf("%s is required", f)
		}
	}
	if strings.HasPrefix(strArg(args, "type"), "methodology.") {
		return fmt.Errorf("pf_remember does not accept methodology.* types; save spec/plan/review/execute/retro/wrap_summary artifacts via pf_save_artifact instead")
	}
	return nil
}

// emitArtifactAction is the shared implementation for adopt/close/ignore artifact wrappers.
func (s *Server) emitArtifactAction(ctx context.Context, req *sdkmcp.CallToolRequest, action string) (*sdkmcp.CallToolResult, error) {
	args, err := parseArgs(req.Params.Arguments)
	if err != nil {
		return errResult(err)
	}
	wiID := strArg(args, "work_item_id")
	if wiID == "" {
		return errResult(fmt.Errorf("work_item_id is required"))
	}
	memID := strArg(args, "memory_id")
	if memID == "" {
		return errResult(fmt.Errorf("memory_id is required"))
	}

	sf, err := config.ResolveStateFile(wiID)
	if err != nil {
		return errResult(fmt.Errorf("read state file: %w", err))
	}

	result, err := s.client.EmitEvent(ctx, buildArtifactActionBody(args, sf, action))
	if err != nil {
		return errResult(err)
	}
	return jsonResult(result)
}

// ─── The other memory tools' two hops, made assertable (aihub#325) ───────────
//
// aihub#148 split pf_recall's schema literal and its forwarding block into named
// functions so a guard could compare them (recall_params_wiring_test.go). It
// covered pf_recall alone, and the very next tool along had the same defect:
// pf_reinforce_memory declared work_item_id REQUIRED, refused the call without
// one, and then built a body that did not contain it. Every non-methodology
// reinforce answered
//
//	400  work_item_id is required when attempt_id/session_secret are provided
//
// because the MCP handler always sends attempt_id/session_secret (they come from
// the state file, unconditionally) and enforceMethodologyAttemptGate's
// non-methodology branch demands work_item_id whenever credentials are present
// (internal/server/routes_memory.go). methodology.* memories take the other
// branch, which binds to the TARGET memory's own work item and never reads the
// request's — which is why pf_save_artifact traffic was unaffected and nobody
// noticed.
//
// Both halves of every memory tool are now named functions for the same reason
// pf_recall's are: a schema inside an AddTool literal and a body inside a closure
// are unreachable from a test, so the contract between them cannot be asserted
// at all. memory_tools_wire_test.go is the guard over the whole set — it drives
// each tool through the real handler and asserts on the request bytes, so a
// builder that is correct but no longer CALLED fails it too.
//
// 🔴 These builders take the resolved *config.StateFile rather than reading it
// themselves. That is what lets the guard run with no workspace on disk, and it
// keeps the credential read in exactly one place per handler.

// rememberSchema is pf_remember's published InputSchema.
//
// pf_remember has no forwarding block to drift from: its handler passes the
// argument map to pkg/client verbatim, so every published property is on the
// wire by construction. The guard states that identity rather than assuming it.
func rememberSchema() json.RawMessage {
	return objectSchema(map[string]any{
		"project":              prop("string", "Project name"),
		"type":                 propEnum("string", "Memory type (full name e.g. experience.debug). methodology.* is not accepted here — use pf_save_artifact.", domain.PfRememberTypeEnum),
		"content":              prop("string", "Memory content"),
		"visibility":           prop("string", "private|project|team|admin"),
		"work_item_id":         prop("string", "Associated work item ID"),
		"base_strength":        prop("number", "Initial strength (0-1)"),
		"attrs":                prop("object", "Additional attributes"),
		"expires_at":           prop("string", "Expiry timestamp (RFC3339)"),
		"dedup_mode":           prop("string", "Deduplication mode"),
		"related_memory_ids":   prop("array", "Related memory IDs"),
		"context_snippet":      prop("string", "Context snippet for embedding"),
		"supersedes_memory_id": prop("string", "Memory ID this supersedes"),
	}, []string{"project", "type", "content", "visibility"})
}

// getMemorySchema is pf_get_memory's published InputSchema.
func getMemorySchema() json.RawMessage {
	return objectSchema(map[string]any{
		"memory_id": prop("string", "Memory ID (the `id` of a pf_recall item)"),
	}, []string{"memory_id"})
}

// activateMemorySchema is pf_activate_memory's published InputSchema.
func activateMemorySchema() json.RawMessage {
	return objectSchema(map[string]any{
		"memory_id": prop("string", "Memory ID"),
	}, []string{"memory_id"})
}

// reinforceMemorySchema is pf_reinforce_memory's published InputSchema — hop 1.
func reinforceMemorySchema() json.RawMessage {
	return objectSchema(map[string]any{
		"memory_id":          prop("string", "Memory ID"),
		"additional_context": prop("string", "Additional context for the memory"),
		"strength_delta":     prop("number", "Strength delta"),
		"work_item_id":       prop("string", "Work item ID (for credential injection)"),
	}, []string{"memory_id", "additional_context", "work_item_id"})
}

// buildReinforceMemoryBody renders pf_reinforce_memory's arguments and the
// resolved state file into the body of PATCH /v1/memories/:id/reinforce — hop 2.
//
// 🔴 work_item_id is not decoration. The server VERIFIES the attempt credentials
// against it (domain.VerifyAttemptCredentialPool), and writes it into
// attrs.reinforcements[].from_wi as the provenance of the reinforcement. Sending
// the credentials without the work item they belong to is the one combination
// the gate rejects outright.
//
// memory_id is absent on purpose: it is the :id path segment. That is asserted
// on the real request URL by memory_tools_wire_test.go rather than exempted on
// trust — "it goes in the path" is a claim, and an unchecked claim is how a
// published parameter goes missing.
func buildReinforceMemoryBody(args map[string]any, sf *config.StateFile) map[string]any {
	body := map[string]any{
		"additional_context": strArg(args, "additional_context"),
		"attempt_id":         sf.AttemptID,
		"claim_epoch":        sf.ClaimEpoch,
		"session_secret":     sf.SessionSecret,
		"work_item_id":       strArg(args, "work_item_id"),
	}
	if v, ok := args["strength_delta"]; ok {
		body["strength_delta"] = v
	}
	return body
}

// updateMemorySchema is pf_update_memory's published InputSchema.
func updateMemorySchema() json.RawMessage {
	return objectSchema(map[string]any{
		"memory_id":     prop("string", "Memory ID (any id in the lineage)"),
		"content":       prop("string", "New content (omit to keep current)"),
		"visibility":    prop("string", "New visibility (omit to keep current)"),
		"tags":          prop("array", "New tags (omit to keep current)"),
		"base_strength": prop("number", "New base strength (omit to keep current)"),
		"work_item_id":  prop("string", "Work item ID (for credential injection)"),
	}, []string{"memory_id", "work_item_id"})
}

// updateMemoryPassthroughFields are the pf_update_memory arguments forwarded
// under their own names, and only when present — absent means "keep current",
// which is not the same as sending a zero value.
var updateMemoryPassthroughFields = []string{"content", "visibility", "tags", "base_strength"}

// buildUpdateMemoryBody renders pf_update_memory's arguments and the resolved
// state file into the body of PATCH /v1/memories/:id/update.
func buildUpdateMemoryBody(args map[string]any, sf *config.StateFile) map[string]any {
	body := map[string]any{
		"attempt_id":     sf.AttemptID,
		"claim_epoch":    sf.ClaimEpoch,
		"session_secret": sf.SessionSecret,
		"work_item_id":   strArg(args, "work_item_id"),
	}
	for _, k := range updateMemoryPassthroughFields {
		if v, ok := args[k]; ok {
			body[k] = v
		}
	}
	return body
}

// redactMemorySchema is pf_redact_memory's published InputSchema.
func redactMemorySchema() json.RawMessage {
	return objectSchema(map[string]any{
		"memory_id": prop("string", "Memory ID"),
		"reason":    prop("string", "Reason for redaction"),
	}, []string{"memory_id", "reason"})
}

// buildRedactMemoryBody renders pf_redact_memory's arguments into the body of
// PATCH /v1/memories/:id/redact.
func buildRedactMemoryBody(args map[string]any) map[string]any {
	return map[string]any{"reason": strArg(args, "reason")}
}

// saveArtifactSchema is pf_save_artifact's published InputSchema.
func saveArtifactSchema() json.RawMessage {
	return objectSchema(map[string]any{
		"type":                 propEnum("string", "Artifact type (must be one of the methodology.* kinds)", domain.MethodologyTypeEnum),
		"work_item_id":         prop("string", "Work item ID"),
		"content":              prop("string", "Artifact content (inline). Provide content OR path, not both."),
		"path":                 prop("string", "Local filesystem path to a UTF-8 markdown file to read as the artifact content (read by the local MCP process; must resolve within the workspace, <=1 MiB). Provide content OR path, not both."),
		"structured_payload":   prop("object", "Optional structured payload"),
		"visibility":           prop("string", "private|project|team|admin (default: project)"),
		"supersedes_memory_id": prop("string", "Memory ID this supersedes"),
		"html":                 prop("string", "Optional pre-rendered HTML stored verbatim in rendered_html (full standalone document or body fragment). Overrides server-side markdown auto-render; use for custom-styled artifact views served by the artifact HTML viewer."),
	}, []string{"type", "work_item_id"})
}

// buildSaveArtifactBody renders pf_save_artifact's arguments, the resolved state
// file and the already-resolved content into the body of POST /v1/memories.
//
// content is a parameter rather than read from args because `path` and `content`
// are two spellings of the same field: resolveArtifactContent collapses them
// (reading the file where necessary) before this is called, which is why `path`
// has no landing of its own.
//
// `html` lands as `rendered_html` — the one renamed field in this file. A guard
// that matched names rather than values would call that a drop.
func buildSaveArtifactBody(args map[string]any, sf *config.StateFile, content string) map[string]any {
	body := map[string]any{
		"type":           strArg(args, "type"),
		"work_item_id":   strArg(args, "work_item_id"),
		"content":        content,
		"attempt_id":     sf.AttemptID,
		"claim_epoch":    sf.ClaimEpoch,
		"session_secret": sf.SessionSecret,
	}
	if v := strArg(args, "visibility"); v != "" {
		body["visibility"] = v
	}
	if v, ok := args["structured_payload"]; ok {
		body["structured_payload"] = v
	}
	if v := strArg(args, "supersedes_memory_id"); v != "" {
		body["supersedes_memory_id"] = v
	}
	if v := strArg(args, "html"); v != "" {
		body["rendered_html"] = v
	}
	return body
}

// artifactActionSchema is the InputSchema shared by pf_adopt_artifact,
// pf_close_artifact and pf_ignore_artifact.
func artifactActionSchema() json.RawMessage {
	return objectSchema(map[string]any{
		"work_item_id":  prop("string", "Work item ID"),
		"memory_id":     prop("string", "Artifact memory ID"),
		"artifact_type": prop("string", "Artifact type"),
	}, []string{"work_item_id", "memory_id"})
}

// buildArtifactActionBody renders an adopt/close/ignore call into the body of
// POST /v1/events.
//
// memory_id lands NESTED and RENAMED, as payload.artifact_key. Both are why the
// guard walks JSON paths instead of comparing top-level key sets.
func buildArtifactActionBody(args map[string]any, sf *config.StateFile, action string) map[string]any {
	return map[string]any{
		"work_item_id":   strArg(args, "work_item_id"),
		"attempt_id":     sf.AttemptID,
		"claim_epoch":    sf.ClaimEpoch,
		"session_secret": sf.SessionSecret,
		"event_type":     "artifact_action",
		"payload": map[string]any{
			"artifact_key":  strArg(args, "memory_id"),
			"artifact_type": strArg(args, "artifact_type"),
			"action":        action,
		},
	}
}

// resolveCommitSchema is pf_resolve_commit's published InputSchema.
func resolveCommitSchema() json.RawMessage {
	return objectSchema(map[string]any{
		"memory_id": prop("string", "Memory ID"),
		"commit_id": prop("string", "Commit annotation ID"),
		"reply":     prop("string", "AI reply explaining what was changed or why the annotation is resolved"),
	}, []string{"memory_id", "commit_id", "reply"})
}

// buildResolveCommitBody renders pf_resolve_commit's arguments into the body of
// POST /v1/memories/:id/commit/:commit_id/resolve. memory_id and commit_id are
// path segments, not body fields.
func buildResolveCommitBody(args map[string]any) map[string]any {
	return map[string]any{"reply": strArg(args, "reply")}
}
