package mcp

import "encoding/json"

// aihub#586 — the hop-2 strip for the wholesale-forwarding memory tools.
//
// ─── The defect this closes, measured ────────────────────────────────────────
//
// aihub#389's disclosure told callers an unpublished argument "had no effect",
// and for most tools that is what the server makes true: their handlers build a
// typed body (or the server binds a struct whose fields are all published), so
// an unknown key dies at echo's c.Bind. pf_remember was the exception that made
// the sentence false AND a security hole: its handler forwards the caller's
// whole argument map to POST /v1/memories, and domain.RememberRequest binds
// MORE names than rememberSchema publishes. Measured on 2026-09-10 (aihub#574's
// wire probe, filed as aihub#586): an UNPUBLISHED `rendered_html` argument
// landed in the body verbatim, bound to RememberRequest.RenderedHTML, and
// resolveRenderedHTML's precedence #1 stored it verbatim for ANY type — so
// `visibility: public` plus a name no schema mentions was enough to put a row
// into the state GET /share/:id serves with NO auth (handleSharedArtifact gates
// on `public` and hasRenderableBody, and a stored rendered_html satisfies the
// second conjunct). The same shape tags had until aihub#425 published it; the
// owner's ruling on aihub#586 (2026-09-10) is to close the CLASS, not the one
// key: strip every unpublished key at this hop for the whole family.
//
// The full unpublished-but-bindable census for pf_remember, derived from
// domain.RememberRequest's json tags minus the published schema and pinned by
// TestRememberUnpublishedBindableKeysAreExactlyTheCensus:
//
//	attempt_id, claim_epoch, session_secret   caller-supplied attempt
//	                                          credentials on a tool that is
//	                                          documented as injecting none
//	rendered_html                             the /share exposure above
//	structured_payload                        an attrs.structured_payload write
//	                                          channel published only on
//	                                          pf_save_artifact
//
// ─── Why a whitelist at hop 2, not a fix anywhere else ───────────────────────
//
//   - NOT the server: pf_remember and pf_save_artifact share POST /v1/memories,
//     and pf_save_artifact's PUBLISHED `html` lands as `rendered_html` — the
//     server cannot refuse the field without breaking the published channel,
//     and it cannot tell the two tools apart. Same reasoning as the
//     methodology-prefix check in validatePfSaveArtifactArgs: a tool-level
//     narrowing is only expressible where the tool is known.
//   - NOT a blacklist of today's five names: the vulnerability was never
//     "rendered_html exists", it was "the bound set and the published set are
//     allowed to differ silently". A blacklist re-opens the day RememberRequest
//     grows a field. Keeping only published names closes the class.
//   - NOT `additionalProperties:false`: that is aihub#389's phase 2, gated on a
//     corpus number that does not exist yet, and it REJECTS where the ruling
//     says strip-and-report.
//
// ─── What the caller sees ────────────────────────────────────────────────────
//
// Nothing new. The stripped set IS the unknown set addTool already computes
// against the tool's own published schema, so the existing aihub#389 disclosure
// — a `request_adjusted` entry `{param: "unknown_params", requested: [names],
// applied: []}` — names every stripped key, and its `applied: []` claim ("we
// used nothing you sent under these names"), which this family used to falsify,
// is now true by construction. Published keys are untouched: values are carried
// as json.RawMessage, so what the caller sent under a published name is what
// goes on the wire, byte for byte.

// wireStrippedTools names the tools whose UNPUBLISHED arguments are stripped
// before the handler runs (aihub#586, owner ruling 2026-09-10). It is exactly
// the wholesale-forwarding memory family: the three tools that write the
// memories row whose rendered_html / credential fields bind more names than
// their schemas publish.
//
// pf_save_artifact and pf_update_memory build whitelist bodies today
// (buildSaveArtifactBody, updateMemoryPassthroughFields), so for them this is
// the boundary guarantee rather than a behaviour change — the class stays
// closed even if a builder starts forwarding wholesale tomorrow. The exact set
// is pinned by TestWireStrippedToolsAreExactlyTheMemoryWriteFamily; the
// behaviour, per tool, by wire_strip_family_test.go.
//
// 🔴 Deliberately NOT every wholesale-forwarding tool. pf_create_work_item,
// pf_create_project, pf_create_user, pf_update_user and pf_predict_conflicts
// also forward their argument maps, and for them the forwarding is harmless
// today because their bind targets publish every bound name (measured
// 2026-09-10, struct json tags against each schema's property set: all five
// come out empty) — but that is a fact about those structs, not a guarantee.
// Widening the set is cheap (one entry here) and should follow the same census
// this family got, not precede it.
var wireStrippedTools = map[string]bool{
	"pf_remember":      true,
	"pf_save_artifact": true,
	"pf_update_memory": true,
}

// stripUnpublishedArgs returns the raw arguments with every top-level key the
// published set does not contain removed. Values survive as json.RawMessage,
// so a published key's value is byte-identical on the way through; only key
// order changes (Go map marshalling sorts), which no JSON binder reads.
//
// Unparseable arguments come back unchanged, for the reason unknownParamNames
// gives: the handler is about to fail on its own terms, and a second complaint
// here would only obscure the real one. (In practice the strip is never reached
// on that path — addTool only calls this when unknownParamNames found
// something, and that returns nil for unparseable input.)
//
// An EMPTY published set strips everything. That is the correct loud failure
// for the one way it can happen — addTool could not parse the tool's own
// schema literal, a programming error it already warns about — because the
// handler then refuses on its missing required fields on the first call,
// rather than this hop quietly forwarding unvetted keys for that one tool.
func stripUnpublishedArgs(raw json.RawMessage, published map[string]struct{}) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil {
		return raw
	}
	kept := make(map[string]json.RawMessage, len(args))
	for name, value := range args {
		if _, ok := published[name]; ok {
			kept[name] = value
		}
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return raw
	}
	return b
}
