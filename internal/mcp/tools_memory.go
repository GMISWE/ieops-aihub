package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/config"
	"github.com/GMISWE/ieops-aihub/internal/domain"
)

func (s *Server) registerMemoryTools() {
	// pf_remember
	s.addTool(&sdkmcp.Tool{
		Name:        "pf_remember",
		Description: "Store a memory in aihub. type must use full name (e.g. experience.debug). Rejects methodology.* types; write spec/plan/review/execute/retro/wrap_summary via pf_save_artifact. A response carrying embedded_len is a truncation warning: only the first embedded_len runes of the stored content were vector-embedded (the embedding input budget), so semantic recall will never match anything past that point. Split the document or accept that the tail is text-search-only.",
		InputSchema: rememberSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		if err := validatePfRememberArgs(args); err != nil {
			return errResult(err)
		}
		// args is the caller's map projected to the published schema — addTool
		// stripped every unpublished key before this handler ran (aihub#586),
		// and the response disclosure names what it stripped. So "forwarded
		// wholesale" below means "every published property, by construction",
		// not "whatever arrived".
		result, err := s.client.Remember(ctx, args)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_recall
	s.addTool(&sdkmcp.Tool{
		Name:        "pf_recall",
		Description: "Recall memories from aihub with optional semantic search. type is an ARRAY of type names, e.g. [\"experience.*\",\"rule.work\"], one filter per entry; a '|' inside an entry is NOT a separator and is rejected. An entry ending in .* is a prefix wildcard. Any entry matching no memory comes back in unmatched_types, which distinguishes a wrong type name from a project that genuinely holds no such memory. An item with content_truncated=true holds only a prefix of its content (content_full_len = full length); call pf_get_memory(memory_id) for the rest. Separately, an item carrying embedded_len was vector-embedded from only its first embedded_len runes (the embedding input budget): semantic ranking saw that prefix alone, and content past it is findable only by text search. A query returns TWO sections (aihub#360): items[] is the semantic ranking, and `lexical` is a parallel verbatim-substring match (every whitespace token of the query, case-insensitive, must appear in a row's content; its hits carry NO similarity, as none is computed). The index is ONE unchunked vector per row, so an EXCERPT of a stored memory routinely fails to retrieve it semantically (measured 2026-09-06, aihub#367: recall@1 0/42; production-shape recall@10 33.3% vs 14.1% random); a semantic miss is NOT evidence of absence. Judge existence by the lexical section: its total is explicit, and `lexical.total: 0` is the strongest available not-in-corpus signal.",
		InputSchema: recallSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		if strArg(args, "project") == "" {
			return errResult(fmt.Errorf("project is required"))
		}
		params, err := buildRecallParams(args)
		if err != nil {
			// Rule 1 at hop 2 (aihub#432): a value this process cannot read is
			// the caller's mistake, named rather than defaulted. The refusal is
			// this hop's 400 — the request never leaves the process, so there is
			// no HTTP status to carry it.
			return errResult(err)
		}
		result, err := s.client.Recall(ctx, params)
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
		return jsonResult(slimRecallResultMode(result, strArg(args, "fields") == "brief"))
	})

	// pf_get_memory — aihub#269. pf_recall truncates content to 800 runes and
	// flags it with content_truncated/content_full_len; without a by-id read those
	// flags would tell the model its text is incomplete while giving it no way to
	// complete it. This is the tool half of that escape hatch.
	s.addTool(&sdkmcp.Tool{
		Name:        "pf_get_memory",
		Description: "Fetch one memory by id with its FULL, untruncated content: the follow-up read for a pf_recall item whose content_truncated is true. If the response carries embedded_len, the stored vector embeds only the first embedded_len runes of this content: semantic recall cannot see the rest.",
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
		// jsonResult is the server's ONE JSON result serializer — aihub#598 folded
		// the byte-identical jsonResultCompact into it.
		return jsonResult(result)
	})

	// pf_activate_memory
	s.addTool(&sdkmcp.Tool{
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
	s.addTool(&sdkmcp.Tool{
		Name:        "pf_reinforce_memory",
		Description: "Reinforce a memory with additional context (mutating; credentials from state file)",
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
			return errResult(config.StateFileMissingErr(wiID, err))
		}
		result, err := s.client.ReinforceMemory(ctx, memID, buildReinforceMemoryBody(args, sf))
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_update_memory (aihub#201)
	s.addTool(&sdkmcp.Tool{
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
			return errResult(config.StateFileMissingErr(wiID, err))
		}
		result, err := s.client.UpdateMemory(ctx, memID, buildUpdateMemoryBody(args, sf))
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_redact_memory
	s.addTool(&sdkmcp.Tool{
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
	s.addTool(&sdkmcp.Tool{
		Name:        "pf_save_artifact",
		Description: "Save a methodology artifact. type must start with methodology. (suggested: spec, plan, review, execute, retro, wrap_summary; an off-list methodology.* name is also accepted). Credentials injected from state file.",
		InputSchema: saveArtifactSchema(),
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		args, err := parseArgs(req.Params.Arguments)
		if err != nil {
			return errResult(err)
		}
		// aihub#499. The two required-field checks used to be inline here; they
		// moved into the validator with the prefix arm so that one function is
		// the whole of this tool's argument contract, the way
		// validatePfRememberArgs is for its mirror.
		if err := validatePfSaveArtifactArgs(args); err != nil {
			return errResult(err)
		}
		wiID := strArg(args, "work_item_id")
		artifactContent, err := resolveArtifactContent(args, config.WorkspaceRoot())
		if err != nil {
			return errResult(err)
		}

		sf, err := config.ResolveStateFile(wiID)
		if err != nil {
			return errResult(config.StateFileMissingErr(wiID, err))
		}

		result, err := s.client.Remember(ctx, buildSaveArtifactBody(args, sf, artifactContent))
		if err != nil {
			return errResult(err)
		}
		return jsonResult(result)
	})

	// pf_adopt_artifact / pf_close_artifact / pf_ignore_artifact were published
	// here until aihub#446 (aihub#411 T2-7). They were wrappers that emitted one
	// event, artifact_action, with payload.action in {adopt, close, ignore}, and
	// nothing in the tree read that event: no Go, template, /ui handler or plugin
	// file. The /ui annotation flow — the one reviewer-facing artifact story that
	// shares part of this vocabulary — runs on an entirely different one
	// (commit annotations, status open/resolved, POST /ui/artifacts/:id/commit/
	// :commit_id/resolve), so retiring these took nothing away from it. All three
	// measured ZERO calls in the aihub#412 21-day window against pf_save_artifact's
	// 93 — disuse rather than proof of no consumer, which is why the /ui sweep was
	// a precondition of the removal rather than a formality. It is recorded in
	// aihub#446 and in docs/design/polyforge-v1-design.md §5.2.
	//
	// Do NOT re-add them as wrappers. pf_emit_event still accepts
	// event_type="artifact_action" verbatim, so the write path is unchanged and
	// the historical events stay readable; what was removed is three schemas in
	// every session's resident tools/list prefix for an API with no observable
	// effect. If artifact state comes back, it wants a state column and a reader,
	// not an alias for an unread event.

	// pf_resolve_commit
	s.addTool(&sdkmcp.Tool{
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
//
// ⚠️ `visibility` was the third entry here, published by recallSchema as
// "Filter by visibility" and bound by handleRecall, from the day pf_recall was
// added until aihub#484 withdrew it on 2026-09-09. All three hops are present
// in `50bfc35` itself — checked, not assumed: the schema property, this
// forwarding list and handleRecall's `c.QueryParam("visibility")` are all in
// that commit's tree, so unlike recency_weight there is no later step where
// forwarding caught up with publication. It was the SECOND instance
// of aihub#469's class and was found by the gate that work item wrote, on that
// gate's first run: hops 1-3 were all intact and hop 4 was empty. None of the
// six functions in internal/domain that take a *RecallRequest ever read the
// field, so a caller sending `visibility=project` got an unfiltered page — no
// error, no warning, and a response byte-identical to the one they would have
// got without it.
//
// The visibility predicates that DO exist on both recall paths are
// authorization scoping derived from CallerRole / CallerUserID —
// `AND (visibility != 'private' OR author_user_id = $n)` and
// `AND visibility != 'admin'` — SQL literals rather than the caller's filter.
// That is why a name-only grep for `.Visibility` made the field look read, and
// it is also why withdrawing changes no result: those clauses never consulted
// this parameter and still do not.
//
// Withdrawn rather than implemented, and — unlike recency_weight — NOT because
// implementing would regress. The recall path genuinely cannot filter by
// visibility, so building it would have been a real capability; the choice was
// therefore the owner's, and on 2026-09-09 the owner ruled withdraw. The
// measurement it rests on is demand, not harm: over the transcript corpus at
// /root/.claude/projects (2,250 files, 850 raw pf_recall calls deduplicated by
// tool_use id to 835, spanning 2026-06-23 to 2026-09-09), ZERO carried a
// visibility argument. That is corroborated independently and from inside this
// repo by docs/audits/aihub-412-corpus-facts/param-types-vs-schema.md, whose
// pf_recall row for `visibility` reads "published, never observed" against a
// different corpus window. Nothing published here was ever used, so the
// withdrawal costs no caller a capability they had.
var recallStringParams = []string{"project", "query", "work_item_id", "top_k", "cursor"}

// recallNumberParams are the pf_recall arguments published as JSON numbers.
//
// Zero means "not specified" for both, which is why they are not in the
// scalarArg loop above: 0 is similarity_threshold's OFF value, and min_strength
// has a server-side default that forwarding a literal 0 would overwrite.
//
// 🔴 similarity_threshold has NO default and must keep none. Measured on
// project=ieops with limit=200: a pure-punctuation noise query scores 0.4712 at
// its WORST hit while a real Chinese query whose top hit is the correct answer
// scores 0.4798 at its BEST — 0.0086 apart, and the wrong way round for six of
// the noise query's hits. No global cutoff separates noise from signal, so the
// job here is to make the knob reachable, never to turn it on.
//
// ⚠️ `recency_weight` was the third entry here, and was published by recallSchema
// and bound by handleRecall, from the day pf_recall was added until aihub#469
// withdrew it. No read ever reached the ranking: on the tree as withdrawn, no
// function in internal/domain that takes a RecallRequest touched the field, so
// every value produced the same page as sending none, with no error and no
// warning.
//
// Stated that way rather than as "nothing ever read it", which is false and
// which this repo's own standard would catch: `Recall` did read it, as a
// self-default (`if req.RecencyWeight <= 0 { req.RecencyWeight = 0.3 }`), from
// `e3e0dfe` until `9064a35` deleted it on 2026-06-05 — a commit whose own
// message calls it "the dead recency_weight default". aihub#424 counted exactly
// that shape as a read when it audited `mode` ("one self-default and two audit
// values"), so the accurate claim is the narrower one: from the first day to the
// last, no read of this field could change a result.
//
// It is GONE rather than implemented, and the reason is not "nobody asked" —
// implementing it was measured to be a REGRESSION. docs/design/polyforge-v1-design.md
// §7.5 specified `sim*(1-w) + normalized_recency*w + normalized_strength*0.1` at
// w=0.3, which is the same shape as the fused score aihub#311 removed from the
// vector path as a defect (commit 7ad96be), with MORE non-similarity weight. The
// 0.6B embedding model packs a result set's cosines into a band ~0.04 wide, so a
// 0.3-weighted recency term spans several times the entire spread of the signal
// it is being blended into: replayed on a real live top_k=20 result set, the
// highest-cosine row fell from rank 1 to rank 10 and similarity inversions went
// from 16/190 to 98/190.
//
// And there was nothing to gain, because recency was never missing. All three
// orderings already carry it: the text default is `GREATEST(last_activated_at,
// created_at) DESC, id DESC` — recency is the only ranking signal there, `id
// DESC` being a deterministic tiebreaker rather than a second one — while the
// lexical and
// vector paths tie-break on an effective strength that contains
// `exp(-days/stability)`. The knob offered to tune a dimension that was already
// the dominant one.
//
// 🔴 Do not re-add it without a hop-4 reader to go with it:
// TestRecallEveryPublishedParamIsReadByTheRankingCode
// (recall_hop4_reader_gate_test.go) fails on a parameter this schema publishes
// that no function in internal/domain reads, UNLESS that parameter is named in
// one of the two maps at the top of that file — `recallParamsNotReadByDomain`
// (consumed in this process) or `recallParamsKnownUnreadTrackedByWi` (a known
// defect with an open work item). The first still holds `fields`; the SECOND is
// empty as of 2026-09-09, because its one entry was `visibility` and aihub#484
// withdrew that parameter, which took the entry with it.
//
// So the gate is unconditional TODAY, but it is not unconditional by
// construction, and the difference matters to anyone reading this note as a
// rule: re-adding `recency_weight` with a ratchet entry instead of a reader
// would still pass. That is why the entries carry a wi number and are checked
// for staleness in both directions.
// A third surface exists and is not an exemption list: `recallParamToField`
// remaps a published name onto a differently-named field (it carries `type` ->
// `Types`), so a wrong entry there could point an unread parameter at a field
// that IS read. It is three lines and has one entry; audit it as part of the
// gate, not as configuration.
var recallNumberParams = []string{"similarity_threshold", "min_strength"}

// recallBoolParams are the pf_recall arguments published as JSON booleans.
// Forwarded as "true" when set, omitted otherwise — the server reads an absent
// param as false, so sending "false" would be redundant and would make
// "explicitly false" and "unset" identical on the wire in any case.
//
// A slice with one entry rather than an inline conditional, and that is the
// point of aihub#464 rather than tidiness. The inline form read through boolArg,
// which discards parseBoolArg's `ok`, so `include_archived: "yess"` was answered
// false — this parameter's DEFAULT — and the caller who had asked for archived
// memories got the active-only page with nothing at any of the four hops saying
// so. That is the same Rule 1 escape aihub#432 closed for the three numbers
// above, in the same function, on the one argument it did not cover. Naming the
// set makes the rejection loop below uniform with the numeric one, and lets
// TestRecallEveryBooleanParamHasARejectionProbe cover a boolean added tomorrow
// on the day it is added.
var recallBoolParams = []string{"include_archived"}

// recallSchema is pf_recall's published InputSchema — hop 1.
func recallSchema() json.RawMessage {
	return objectSchema(map[string]any{
		"project": prop("string", "Project name"),
		"query":   prop("string", "Semantic search query"),
		// aihub#289: the shape is the whole point of this description. Three
		// SKILL.md templates taught type="a|b|c", nothing split it, and the
		// resulting empty set read as "no relevant memory". The model reads this
		// string, so this string has to state the contract.
		"type": prop("array", "Memory types to filter: an ARRAY of names, one per entry: [\"experience.*\",\"rule.work\"]. Entries ending in .* are prefix wildcards. Do NOT pack several types into one string with '|', which is not a separator and is rejected with a 400."),
		// ⚠️ No `visibility` here — withdrawn by aihub#484 on 2026-09-09; see the
		// tombstone on recallStringParams above for the measurement. It promised
		// "Filter by visibility" and no recall-path function in internal/domain
		// ever read it, and 0 of 835 deduplicated corpus calls had ever sent one.
		// aihub#590 (2026-09-10): this said "Filter by work item ID" while
		// domain.Recall has resolved id-or-slug since aihub#363 — the capability
		// existed and the published text hid it, so the slug every human and
		// skill types looked unsupported and callers burned a pf_get_work_item
		// round-trip for a canonical id nothing needs. Pinned by
		// slug_publication_test.go (TestSlugAcceptanceIsPublishedByRecall).
		"work_item_id": prop("string", "Filter by work item, as canonical id or slug; either resolves "+
			"to the same filter (aihub#363)."),
		"top_k": prop("string", "Max results (default 20, ceiling 200). A JSON number is also "+
			"accepted, and is what most callers send."),
		"similarity_threshold": prop("number", "Minimum cosine similarity, 0-1. Applies to the "+
			"semantic (vector) half of the recall only, and is OFF by default: scores are not "+
			"comparable across queries, so there is no safe global cutoff. A threshold that "+
			"matches nothing returns an empty list rather than falling back to text search: "+
			"empty is the intended answer when you set one."),
		// aihub#425. `cursor` was ALREADY forwarded by buildRecallParams and already
		// bound by handleRecall — measured on the real in-memory session against a
		// recording server, it reaches the wire today. Nothing was one line short
		// of working; the capability was reachable only by guessing a name no
		// schema mentions.
		//
		// 🔴 This SUPERSEDES a deliberate decision, so the reason is recorded here
		// rather than left implicit. recall_params_wiring_test.go's
		// recallUnpublishedForwardedParams held cursor with: "paging is driven by
		// next_cursor from a previous response, not composed by the model;
		// publishing it would invite an invented cursor." The measurement that
		// overrides it: next_cursor REACHES THE MODEL — a pf_recall result is
		// handed back with `"next_cursor":"..."` in it (verified through a real
		// session against a server returning one). So the model is not being kept
		// away from cursors; it is handed one and given no published way to spend
		// it. The invention risk the note names is real but is answered by the
		// description below, which says where the value must come from.
		//
		// The paging caveat is IN the description because leaving it out builds
		// the trap this repo keeps re-learning: recall's vector path and the
		// hybrid merge each set Cursor="" and return a nil next_cursor
		// (internal/domain/memory.go, recallHybrid), so a caller paging a
		// semantic recall gets page one forever with nothing saying why.
		//
		// recall_algo was deliberately NOT published alongside it, and aihub#632
		// then retired the parameter outright: "lexical" was its only non-default
		// value, the server branch it selected is deleted, and the aihub#360
		// lexical section serves that semantics on every recall with a query.
		// Do not re-add it here without also reversing
		// TestRecallAlgoStaysUnpublished and TestRecallAlgoIsRetired.
		"cursor": prop("string", "Opaque page token: pass a previous response's next_cursor. "+
			"TEXT-path paging only: the semantic (vector) path and the hybrid merge "+
			"return no next_cursor and ignore this."),
		// aihub#433 / aihub#411 T2-19. The default is unchanged and deliberately
		// so; what was missing is the scale. min_strength is compared against
		// base_strength after decay, and base_strength is CHECK-constrained to
		// 1-5, so 0.3 is below every legal value — it filters nothing. That reads
		// as a sensible mid-range cutoff only if you believe the range this tool
		// used to publish for base_strength, which is why the two strings are
		// fixed together and gated together.
		"min_strength":     prop("number", "Min effective strength, i.e. base_strength (1-5) after decay. Default 0.3 filters nothing"),
		"include_archived": prop("boolean", "Include archived memories (default false)"),
		// ⚠️ No `recency_weight` here — withdrawn by aihub#469, see the note on
		// recallNumberParams for the measurement. Its published description said
		// "default 0.3" while the source comment said the default was
		// deliberately not applied, so the one thing the string asserted was the
		// thing that was least true. (That comment was never on the field: it sat
		// at the head of recallText in internal/domain/memory.go, ~1,600 lines
		// away, which is part of why the contradiction went unnoticed.)

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

// buildRecallParams renders pf_recall's MCP arguments into the query string for
// GET /v1/memories — hop 2 of the four-hop contract.
//
// Returns an error when an argument ARRIVED and could not be read as the type
// this tool publishes. That is Rule 1 (internal/server/queryparam.go) applied at
// this hop, and aihub#432 is where it arrived: the numbers used to go through a
// local reader that answered 0 for unparseable text, which is this tool's "not
// specified", so `similarity_threshold: "notanumber"` produced an unfiltered
// page and no complaint at any of the four hops. parseNumArg now separates "not
// sent" from "not readable" and only the first of those may be silent.
//
// ⚠️ aihub#432 fixed the numbers and left the boolean, which is why the sentence
// above says "an argument" rather than "a numeric argument". `include_archived`
// kept reading through boolArg — the lenient wrapper that DISCARDS
// parseBoolArg's `ok` — so `include_archived: "yess"` was answered false, which
// is that parameter's default: a caller who asked for archived memories was
// handed the active-only page and told nothing. It was spotted in aihub#432's
// own self-review, filed as aihub#464 rather than fixed in passing, and the
// lesson is about scope rather than about booleans — the parameter that survived
// the audit was the one the audit's own subject ("the three numbers") had
// already excluded, in the very function being audited.
//
// The rejection now belongs to the FUNCTION, so recallBoolParams and
// recallNumberParams are both covered by completeness gates in
// recall_params_wiring_test.go.
//
// A zero still means "not specified", and that is a DIFFERENT and unfixed
// ambiguity: `similarity_threshold: 0` and no threshold at all remain the same
// request here (see TestRecallThresholdHasNoDefault, which pins the off default
// this preserves). Telling those two apart needs the presence flag threaded
// through to the query string, which changes what min_strength=0 means on the
// server — a contract change, not a bug fix.
//
// That ambiguity had a second victim worth recording. Of the 9 observed calls
// that ever carried `recency_weight`, 4 sent a literal 0 in one human debugging
// session that narrated the intent as "turn off recency weighting and retest" —
// and this `v != 0` skip meant those requests reached the server carrying no
// recency_weight at all, i.e. asking for exactly the default the caller was
// trying to suppress. Because the knob was inert the control looked like it
// worked; had it been implemented, that session's stated control would have been
// silently inverted into its opposite.
//
// Note what this does NOT explain: the skip degrades only the value 0, so it is
// not why the defect survived experiment generally. The other 5 observed calls
// carried 0.4 or 0.9, were forwarded intact, and still changed nothing — that
// half was invisible because the parameter had no reader at all, not because of
// anything this function does.
func buildRecallParams(args map[string]any) (url.Values, error) {
	params := url.Values{}
	for _, k := range recallStringParams {
		setIfNonempty(params, k, scalarArg(args, k))
	}
	// Numbers, formatted as %g. A zero is "not specified" — see recallNumberParams.
	for _, k := range recallNumberParams {
		v, present, ok := parseNumArg(args, k)
		if !ok {
			return nil, fmt.Errorf("%s must be a finite number, got %s", k, describeArg(args[k]))
		}
		if present && v != 0 {
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
	// Booleans. Same shape as the numbers above and as buildListWorkItemsParams:
	// absent is silent, unreadable is refused naming the parameter (aihub#464).
	for _, k := range recallBoolParams {
		value, present, ok := parseBoolArg(args, k)
		if !present {
			continue
		}
		if !ok {
			return nil, fmt.Errorf("%s must be a boolean (true/false, \"true\"/\"false\", or 1/0), got %s", k, describeArg(args[k]))
		}
		if value {
			params.Set(k, "true")
		}
	}
	// No recall_algo forwarding. The explicit-arg and POLYFORGE_RECALL_ALGO env
	// hops sat here until aihub#632 retired the parameter: "lexical" was its only
	// non-default value, the server branch it selected is deleted, and the
	// aihub#360 lexical section serves that semantics on every recall with a
	// query. An explicit recall_algo argument is now an unknown parameter and is
	// disclosed as such (aihub#389); TestRecallAlgoIsRetired pins that neither
	// source reaches the wire again.
	return params, nil
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

// memoryTypeParamDesc is pf_remember's `type` description, and the withdrawal of
// its enum (aihub#445, from the aihub#411 decision table §6.2 T2-6).
//
// The 13-value propEnum this replaces was ADVISORY and read as MANDATORY. What
// the server enforces is a PREFIX (internal/domain/memory.go,
// MemoryTypePrefixes) plus a '|' ban, and since aihub#445 the memories.type
// CHECK enforces that same predicate — so the accepted set is INFINITE and no
// list of concrete names can ever equal it. Publishing 13 of them under the JSON
// Schema `enum` key stated a closed contract twice over that nothing keeps:
// the server accepts experience.whatever and stores it, and this process would
// not have refused it either — polyforge registers through the untyped
// (*mcp.Server).AddTool METHOD, whose callTool path invokes the handler with no
// schema step (measured on go-sdk v1.6.0; the reading is written out in
// internal/domain/user_fields.go).
//
// That is the aihub#238 rule applied a second time — a value the server does not
// validate must not be published as a closed enum, as
// TestDeclaredResourcesProp_DescribesURIAndIntent already requires of `intent`.
// It is also the exact INVERSE of aihub#463, and both directions come from one
// principle: the published set and the enforced set are one set. There the
// vocabulary really was closed, so the repair was to make the server enforce it;
// here the leniency is the decided behaviour, so the repair is to stop
// publishing a closed set.
//
// The 13 names survive as a SUGGESTION rather than being dropped. They are the
// aihub#70 curated list, and a caller left with no vocabulary at all invents type
// names instead of reusing them — which is the failure this whole row is about,
// arriving by the other road. They were already on the wire as an enum array, so
// what is actually being bought is the framing: serialised, this property goes
// from 374 bytes to 592, +218 resident per session (measured). That is the price
// of a list that says what it is, and the alternative was 374 bytes of a list
// that said the opposite.
//
// ⚠️ One thing IS given up, and it should be given up knowingly: the `enum` key
// is what scripts/pf_contract_lint.py's ENUM_VIOLATION arm reads, so off-list
// `type` values written into plugins/ are no longer flagged statically. Measured
// at the time of the change: that arm was reporting nothing for pf_remember (0
// violations, 0 baseline entries), and it could not have been right to keep — it
// was flagging values the server accepts by decision. The check that replaces it
// is the one that can be correct: memories_type_check, which refuses only what
// Go refuses.
//
// Built from domain.MemoryTypePrefixes and domain.PfRememberTypeEnum so the
// published text cannot drift from either. tools_memory_type_vocab_test.go
// holds both halves: no `enum` key, and a description that states the enforced
// rule and every suggested value.
func memoryTypeParamDesc() string {
	globs := make([]string, 0, len(domain.MemoryTypePrefixes))
	for _, p := range domain.MemoryTypePrefixes {
		// methodology.* is legal for the COLUMN and refused by this TOOL
		// (validatePfRememberArgs), so the enforced set published here is the
		// column's minus that one prefix.
		if p == "methodology." {
			continue
		}
		globs = append(globs, p+"*")
	}
	return "Memory type, full name (e.g. experience.debug). ENFORCED: must start with " +
		strings.Join(globs, ", ") + ", and contain no '|' (a memory has exactly ONE type; " +
		"a piped one can never be recalled by type). methodology.* is refused here; use " +
		"pf_save_artifact. SUGGESTED, not a closed set: an off-list name with a legal prefix " +
		"is accepted and stored: " + strings.Join(domain.PfRememberTypeEnum, ", ") + "."
}

// rememberVisibilityParamDesc is pf_remember's published `visibility`
// description, built from the same list the write path is judged against.
//
// aihub#495. It used to be the hand-typed literal "private|project|team|admin",
// four of the column's FIVE legal values — `public` was added to
// memories_visibility_check by migration 0023 and to the Go mirror by aihub#434,
// and the published set was never widened with it. So the one tier that changes
// who can read the memory was the one tier a caller reading hop 1 could not know
// existed, and hop 1 is the only thing a tool caller ever sees.
//
// Derived rather than retyped, for the reason aihub#474 gave about the goal cap:
// a hand-typed vocabulary and the vocabulary that enforces it drift silently, and
// this one already had. domain.MemoryVisibilityList() is also what
// domain.vocabularyErr renders into the 400, so the set a caller is shown up
// front and the set the refusal names are now one value in one order.
//
// The ⚠️ is not decoration. `public` is not merely "wider than team": it is the
// tier internal/server/router.go's GET /share/:id gates on, an UNAUTHENTICATED
// route, and internal/server/routes_artifacts.go names this exact reachability
// — "`public` is settable by a project writer straight from POST /v1/memories …
// so it is not by itself a deliberate publication" — in the header of
// `hasRenderableBody`, the serve condition `handleSharedArtifact` gates through
// (attribution corrected 2026-09-10, aihub#592: this comment used to place the
// sentence on the handler itself). A
// vocabulary list that presented it as the fifth item in a ladder would publish
// the value and withhold the only thing about it a caller needs.
//
// It says "when it also has a renderable body" rather than "and nothing written
// through this tool has one", which is true of a default deployment (the render
// set is methodology.* and this tool refuses those types) but is NOT a promise
// this tool can make: renderTypes is configurable, so the conjunct is the honest
// stopping point.
//
// ⚠️ No longer scoped to this tool alone. pf_update_memory writes the same
// column through the same guard and shares this description's whole tail since
// aihub#529 (see visibilityVocabAndConsequence); pf_save_artifact still carries
// a hand-typed four-value literal and is the file scope of another work item.
// See the gate in visibility_vocab_publication_test.go for which tools are
// checked and why.
func rememberVisibilityParamDesc() string {
	return "Visibility tier. " + visibilityVocabAndConsequence() +
		" Send `project` unless you mean to publish."
}

// visibilityVocabAndConsequence is the shared tail of every `visibility`
// description the aihub#495 gate scopes: the enforced vocabulary, derived from
// domain.MemoryVisibilityList() rather than retyped — the same list
// domain.vocabularyErr renders into the 400, so the set a caller is shown and
// the set a refusal names cannot drift — plus the `public` consequence, the one
// sentence rememberVisibilityParamDesc's header explains at length: `public` is
// the tier GET /share/:id serves WITHOUT auth, so a list that omitted the
// warning would publish the affordance and withhold the reason to be careful
// with it.
//
// One function rather than a copy per tool (aihub#529): pf_remember and
// pf_update_memory write the same column through the same guard
// (domain.UpdateMemory builds a RememberRequest and calls Remember), so their
// published vocabularies diverging could only ever mislead. The gate in
// visibility_vocab_publication_test.go asserts the set and the consequence per
// tool regardless, so this sharing is a convenience, not the enforcement.
func visibilityVocabAndConsequence() string {
	return "ENFORCED: one of " +
		strings.Join(domain.MemoryVisibilityList(), "|") +
		" (memories_visibility_check, mirrored in Go; anything else is a 400 naming the field). " +
		"NOTE: `public` is the anonymous-share tier: GET /share/:id serves a public memory with NO auth " +
		"when it also has a renderable body."
}

// updateMemoryVisibilityParamDesc is pf_update_memory's published `visibility`
// description.
//
// aihub#529. It used to read "New visibility (omit to keep current)" — no
// values at all, which is worse than the four-of-five literal aihub#495 fixed
// on pf_remember: silence did not even leave a caller a stale ladder to copy
// from, so every legal value was a guess and every illegal one a 400. The
// vocabulary and its consequence are pf_remember's own tail, shared via
// visibilityVocabAndConsequence rather than restated, because both tools write
// memories.visibility through the same guard.
//
// The closing advice deliberately differs from pf_remember's "Send `project`
// unless you mean to publish": on an update the safe default is OMITTING the
// key, which keeps the lineage head's value — sending `project` here would
// itself be a change.
func updateMemoryVisibilityParamDesc() string {
	return "New visibility (omit to keep current). " + visibilityVocabAndConsequence() +
		" Omit unless you mean to change who can read the memory."
}

// rememberSchema is pf_remember's published InputSchema.
//
// pf_remember has no forwarding block to drift from: its handler passes the
// argument map to pkg/client, projected by addTool to THIS schema's own
// property set (aihub#586, wire_strip.go), so every published property is on
// the wire by construction and no unpublished name is. It used to be forwarded
// verbatim, and that was the aihub#586 vulnerability: `rendered_html` is a name
// domain.RememberRequest binds and this schema does not publish, so a caller
// who guessed it stored anonymous-shareable HTML through a tool whose contract
// never offered that. The guard states the identity rather than assuming it.
//
// aihub#433 / aihub#411 T1-3: base_strength used to be published as "(0-1)", a
// range the memories.base_strength CHECK refuses outright, so the 13 corpus
// calls that believed it all came back 500. It now publishes the enforced range
// and domain.Remember rejects a violation with a 400 naming the field.
// tools_memory_test.go asserts this string against domain.MinBaseStrength /
// domain.MaxBaseStrength so the two cannot drift apart again.
//
// aihub#459 (owner ruling 2026-09-09) adds the word "integer", and it is not a
// stylistic addition: the published TYPE is still `number` because that is what
// the JSON wire carries, so the description is the only place a caller is told
// that 2.5 — in range, and a perfectly good `number` — is a 400. The same test
// gates the word against the behaviour, so dropping it here or dropping the
// enforcement there both go red.
//
// aihub#445 / aihub#411 T2-6: `type` is NOT an enum any more. See
// memoryTypeParamDesc.
func rememberSchema() json.RawMessage {
	return objectSchema(map[string]any{
		"project":              prop("string", "Project name"),
		"type":                 prop("string", memoryTypeParamDesc()),
		"content":              prop("string", "Memory content"),
		"visibility":           prop("string", rememberVisibilityParamDesc()),
		"work_item_id":         prop("string", "Associated work item ID"),
		"base_strength":        prop("number", "Initial strength, integer 1-5 (default 3). A fractional value is refused"),
		"attrs":                prop("object", "Additional attributes."+jsonObjectPropNote),
		"expires_at":           prop("string", "Expiry timestamp (RFC3339)"),
		"dedup_mode":           prop("string", "Deduplication mode"),
		"related_memory_ids":   prop("array", "Related memory IDs"),
		"context_snippet":      prop("string", "Context snippet for embedding"),
		"supersedes_memory_id": prop("string", "Memory ID this supersedes"),
		// aihub#425. Also already on the wire — pf_remember forwards its whole
		// args map, so `tags` reached POST /v1/memories unpublished (measured).
		// Until now the only PUBLISHED way to tag a memory was to create it and
		// then call pf_update_memory, which does publish `tags`, so this closes a
		// gap that cost a second round trip rather than one that lost the data.
		// Checked, because the work item asked: no /ui writer supplies tags —
		// the only non-test writer of the column is this endpoint.
		"tags": prop("array", "Tags stored with the memory. pf_recall returns them, and fields=\"brief\" drops them."),
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
//
// aihub#475: strength_delta used to publish the bare words "Strength delta",
// which is true and useless. The stored column is SMALLINT, so the sum was
// truncated toward zero on the way in and a delta smaller than 1 in magnitude
// stored nothing at all — a caller reading the old description had no way to
// know that, and before the same work item the 200 body reported the
// untruncated arithmetic, so the call looked like it had worked. That text
// stated the granularity and pointed at the response, and deliberately stopped
// short of saying a fractional delta was rejected or rounded, because it was
// neither.
//
// aihub#459 (owner ruling 2026-09-09) settled it: refused. So the description
// no longer describes truncation, and that removal is the point — a caller who
// is told "a delta under 1 usually changes nothing" will still send 0.5 and
// then wonder; one who is told it is refused cannot. The truncation itself has
// not gone anywhere (see domain.ValidateIntegralStrength), it has just stopped
// being reachable through this parameter, and a description that keeps
// explaining an unreachable mechanism is teaching the caller the wrong model.
//
// aihub#506 (owner ruling 2026-09-09) added the saturation half, and it is a
// contract change with NO behaviour change behind it. The clamp was already
// published as "then clamped to 1-5", which is true and still let a caller read
// an overflowing sum as a refusal, because the very next sentence says a
// fractional delta IS refused. The two outcomes are not interchangeable: a
// refusal tells the caller their delta did not land, while the clamp answers 200
// having stored a value the caller did not name. So the text now says the sum
// SATURATES rather than being refused, and that an overflowing delta is applied
// only in part; the sentence pointing at the response stays, because the stored
// value is the only place the size of the loss is visible.
func reinforceMemorySchema() json.RawMessage {
	return objectSchema(map[string]any{
		"memory_id":          prop("string", "Memory ID"),
		"additional_context": prop("string", "Additional context for the memory"),
		"strength_delta":     prop("number", "Integer delta added to the memory's stored strength; the sum saturates at 1-5 rather than being refused, so a delta that overflows is applied only in part. A fractional delta is refused with a 400: strength is a whole number. The response reports the value actually stored."),
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
//
// aihub#433: base_strength is the same column pf_remember writes and is caught by
// the same guard — this tool's body reaches domain.UpdateMemory, which builds a
// RememberRequest and calls Remember. It used to publish no range at all, which
// is the quieter half of T1-3: silence about a constraint is not neutral when the
// sibling tool is publishing a wrong one.
//
// aihub#459: and for the same reason the integrality is published here too. One
// guard covers both tools, so a caller told about it by only one of them would
// meet the other's 400 with no warning — which is the T1-3 shape again, in the
// direction of silence rather than of a wrong answer.
//
// aihub#529: `visibility` is the third instance of the same shape. Same column,
// same guard, and this tool's description named no values at all; see
// updateMemoryVisibilityParamDesc.
func updateMemorySchema() json.RawMessage {
	return objectSchema(map[string]any{
		"memory_id":     prop("string", "Memory ID (any id in the lineage)"),
		"content":       prop("string", "New content (omit to keep current)"),
		"visibility":    prop("string", updateMemoryVisibilityParamDesc()),
		"tags":          prop("array", "New tags (omit to keep current)"),
		"base_strength": prop("number", "New base strength, integer 1-5 (omit to keep current). A fractional value is refused"),
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

// methodologyTypeParamDesc is pf_save_artifact's `type` description, and the
// withdrawal of its enum (aihub#499, the tail of aihub#445 / aihub#411 §6.2 T2-6).
//
// The 6-value propEnum this replaces was ADVISORY and read as MANDATORY —
// aihub#211 added it to give contract-lint something to check, and the card even
// claimed the SDK refused an out-of-vocabulary value before the handler ran.
// aihub#445 measured both halves false. This process validates nothing on that
// registration path (aihub#463, go-sdk v1.6.0: the untyped (*mcp.Server).AddTool
// invokes the handler with no schema step), and the server's gate is a PREFIX
// plus the aihub#210 credential check, so `methodology.anything` stored.
//
// WHY WITHDRAW THE NAMES AND ENFORCE THE PREFIX, rather than enforce the names.
// aihub#411 §6.2 T2-6's ruling is "keep the leniency", and the accepted set is
// open in fact and not just in principle: measured live 2026-09-09 over all ten
// projects, 1,185 methodology.* rows include 3 off these six
// (methodology.playbook, ieops wi_TYllxcv1 — operator handover docs that no
// member of the six describes). Pinning the six would have refused real
// artifacts. So this follows aihub#445's shape, for the same reason and one
// layer along: the published set and the enforced set are ONE set, and where
// leniency is the decided behaviour the repair is to stop publishing a closed
// one. The inverse case is aihub#463, where the vocabulary really was closed and
// the repair was to make the server enforce it.
//
// 🔴 The prefix half is NOT merely published, it is now checked
// (validatePfSaveArtifactArgs). Before aihub#499 nothing required it either:
// handleRemember only BRANCHES on the prefix, and domain.Remember accepts all
// four of MemoryTypePrefixes, so pf_save_artifact(type="fact.note") with a live
// claim put a non-artifact through the artifact door. The tool description had
// said "methodology.* kinds" since aihub#211 while that was untrue, so the check
// makes the older claim honest rather than adding a new promise.
//
// ⚠️ That last sentence is DERIVED, not measured end to end. Verified: hop 2
// forwards fact.note (remove the prefix arm and TestSaveArtifactTypeIsEnforced
// accepts it), handleRemember picks its non-methodology arm, Remember's loop
// admits the fact. prefix, and memories_type_check mirrors that same list. Four
// covered links, no single test over the whole path.
//
// The check has to live at THIS hop, not in domain: pf_save_artifact and
// pf_remember share POST /v1/memories, and the server cannot narrow to
// methodology.* without breaking pf_remember, which must accept the other three
// prefixes. That is why the mirror gate validatePfRememberArgs is here too — a
// tool-level narrowing is only expressible where the tool is known.
//
// Built from domain.MethodologyTypePrefix and domain.MethodologyTypeEnum so the
// published text cannot drift from either the check or the suggested list.
// tools_save_artifact_vocab_test.go holds both halves: no `enum` key, and a
// description that states the enforced rule and every suggested value.
func methodologyTypeParamDesc() string {
	return "Artifact type, full name (e.g. " + domain.MethodologyTypeEnum[0] + "). ENFORCED: must " +
		"start with " + domain.MethodologyTypePrefix + " (pf_remember takes the other prefixes), " +
		"contain no '|', and carry this work item's attempt credentials, which this tool sends " +
		"from the state file. SUGGESTED, not a closed set: an off-list name with the " +
		domain.MethodologyTypePrefix + " prefix is accepted and stored: " +
		strings.Join(domain.MethodologyTypeEnum, ", ") + ". An off-list type is stored but is " +
		"NOT pre-rendered and does NOT appear in the work item's artifact list, both of which " +
		"name the six literally (domain.defaultRenderTypes, server.fetchArtifactLinks)."
}

// offPrefixHint says what to DO about a type pf_save_artifact just refused, and
// it branches because the single most natural hint is wrong for half the cases.
//
// "store it with pf_remember instead" is right for fact.note — a legal memory
// type through the other door — and wrong for "spec", which pf_remember refuses
// too (no legal prefix at all), so a caller following that advice earns a second
// 400. The aihub#211 case is exactly the second kind: the corpus calls were bare
// "spec" / "retro", the six names with the prefix filed off, and for those the
// useful hint names the value they meant.
func offPrefixHint(artifactType string) string {
	qualified := domain.MethodologyTypePrefix + artifactType
	for _, v := range domain.MethodologyTypeEnum {
		if v == qualified {
			return "did you mean " + qualified + "? The prefix is part of the type name, not a namespace the tool adds"
		}
	}
	for _, p := range domain.MemoryTypePrefixes {
		if p != domain.MethodologyTypePrefix && strings.HasPrefix(artifactType, p) {
			return "that is a pf_remember type, not an artifact; pf_save_artifact writes " +
				"work-item-bound methodology artifacts only"
		}
	}
	return "an artifact type is a full name beginning with " + domain.MethodologyTypePrefix +
		"; for a non-artifact memory use pf_remember, whose accepted prefixes are " +
		domain.MemoryTypePrefixGloss()
}

// validatePfSaveArtifactArgs enforces pf_save_artifact's contract before the
// HTTP call: required fields present, and the type inside
// domain.MethodologyTypePrefix — the mirror of validatePfRememberArgs, which
// refuses that same prefix (aihub#210). aihub#499 added the prefix arm; see
// methodologyTypeParamDesc for why it is a prefix and not the six names, and why
// it cannot live in domain.
//
// The '|' arm duplicates no word list: domain.Remember rejects a piped type on
// the write path already (aihub#289). It is here so the refusal names the
// parameter at the hop the caller can see, rather than arriving as a server 400
// about a "memory type" from a tool whose parameter is called an artifact type.
func validatePfSaveArtifactArgs(args map[string]any) error {
	artifactType := strArg(args, "type")
	if artifactType == "" {
		return fmt.Errorf("type is required")
	}
	if strArg(args, "work_item_id") == "" {
		return fmt.Errorf("work_item_id is required")
	}
	if !strings.HasPrefix(artifactType, domain.MethodologyTypePrefix) {
		return fmt.Errorf("type %q is not a legal value; allowed: types starting with %q "+
			"(suggested: %s). %s",
			artifactType, domain.MethodologyTypePrefix,
			strings.Join(domain.MethodologyTypeEnum, ", "),
			offPrefixHint(artifactType))
	}
	if strings.Contains(artifactType, "|") {
		return fmt.Errorf("type %q contains '|', which is not part of the memory type "+
			"vocabulary. An artifact has exactly ONE type; '|' is not a separator here, and a "+
			"type stored with it could never be recalled by type. Pick one concrete type "+
			"(e.g. %s)", artifactType, domain.MethodologyTypeEnum[0])
	}
	return nil
}

// saveArtifactSchema is pf_save_artifact's published InputSchema.
//
// aihub#499: `type` is NOT an enum any more. See methodologyTypeParamDesc.
func saveArtifactSchema() json.RawMessage {
	return objectSchema(map[string]any{
		"type":                 prop("string", methodologyTypeParamDesc()),
		"work_item_id":         prop("string", "Work item ID"),
		"content":              prop("string", "Artifact content (inline). Provide content OR path, not both."),
		"path":                 prop("string", "Local filesystem path to a UTF-8 markdown file to read as the artifact content (read by the local MCP process; must resolve within the workspace, <=1 MiB). Provide content OR path, not both."),
		"structured_payload":   prop("object", "Optional structured payload."+jsonObjectPropNote),
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
