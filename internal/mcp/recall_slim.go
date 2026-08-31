package mcp

import (
	"encoding/json"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// slimRecallResult projects a pf_recall response to the fields an LLM actually
// uses, dropping bookkeeping columns and heavy blobs (commits, attrs internals)
// the model never reads. content is kept verbatim (zero info loss). opt3 Phase 1;
// see ieops-docs/polyforge-aihub-improvement/25-optimization3-phase1.md.
func slimRecallResult(result map[string]any) map[string]any {
	if result == nil {
		return result
	}
	items, ok := result["items"].([]any)
	if !ok {
		return result
	}
	// INVARIANT: this whitelist is opt-in, so a field added to the REST response
	// downstream is dropped here by default until it is listed. That has now bitten
	// twice — `total` (aihub#249) and the truncation pair below (aihub#269). When you
	// add a field to RecallResponse or domain.Memory, decide here whether the model
	// needs it; do NOT widen it wholesale, the dropped bookkeeping columns are the
	// bulk of the opt3 Phase 1 token saving (locked by
	// TestSlimRecallResult_StillDropsBookkeeping; attrs and commits are rewritten
	// rather than dropped, locked by TestSlimRecallResult_RewritesAttrsAndCommits).
	keep := map[string]bool{
		"id": true, "type": true, "content": true, "effective_strength": true,
		"similarity": true, "work_item_id": true, "tags": true, "related": true,
		"created_at": true,
		// aihub#269: content is truncated to 800 runes by handleRecall (PR #245), which
		// flags the cut with these two. Without them the model reasons on a snippet
		// believing it is the whole memory, and has no full length to tell it a
		// pf_get_memory follow-up is warranted — the escape hatch PR #245 declared,
		// which aihub#269 also gave a tool. Both are `omitempty`, so untruncated
		// items pay nothing.
		"content_truncated": true, "content_full_len": true,
	}
	slim := make([]any, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			slim = append(slim, it)
			continue
		}
		out := make(map[string]any, len(keep)+1)
		for k, v := range m {
			if keep[k] {
				out[k] = v
			}
		}
		if attrs, ok := m["attrs"].(map[string]any); ok {
			if sp, ok := attrs["structured_payload"]; ok {
				out["attrs"] = map[string]any{"structured_payload": sp}
			}
		}
		// commits: keep the human INSIGHT (comment + reply bodies, and who said it) but
		// strip bookkeeping (ids, author_user_id, timestamps, anchors, thread structure).
		// Empty commits stay omitted (zero cost). Flagged useful in report review 2026-07-29.
		if commits, ok := m["commits"].([]any); ok && len(commits) > 0 {
			notes := make([]any, 0, len(commits))
			for _, c := range commits {
				cm, ok := c.(map[string]any)
				if !ok {
					continue
				}
				note := map[string]any{}
				if b, ok := cm["body"]; ok {
					note["body"] = b
				}
				if a, ok := cm["author_display"]; ok {
					note["by"] = a
				}
				if reps, ok := cm["replies"].([]any); ok && len(reps) > 0 {
					rb := make([]any, 0, len(reps))
					for _, r := range reps {
						if rm, ok := r.(map[string]any); ok {
							if b, ok := rm["body"]; ok {
								rb = append(rb, b)
							}
						}
					}
					if len(rb) > 0 {
						note["replies"] = rb
					}
				}
				if len(note) > 0 {
					notes = append(notes, note)
				}
			}
			if len(notes) > 0 {
				out["commits"] = notes
			}
		}
		slim = append(slim, out)
	}
	res := map[string]any{"items": slim}
	if nc, ok := result["next_cursor"]; ok && nc != nil {
		res["next_cursor"] = nc
	}
	// aihub#249: total (count of memories matching the request's filters,
	// independent of pagination) must survive slimming — otherwise pf_recall
	// callers have no way to distinguish "that's everything" from "keep
	// paging", the exact gap this wi exists to close. Same conditional-copy
	// pattern as next_cursor above.
	if total, ok := result["total"]; ok && total != nil {
		res["total"] = total
	}
	// aihub#289: unmatched_types names the `type` entries that matched no row. It
	// exists solely to be READ BY THE MODEL — dropping it here would reinstate the
	// silence the field was added to end, on the one caller that matters most. The
	// server omits it when there is nothing to report, so healthy recalls pay
	// nothing. Third instance of this whitelist swallowing a new field (total,
	// aihub#249; the truncation pair, aihub#269); see the INVARIANT note above.
	if um, ok := result["unmatched_types"]; ok && um != nil {
		res["unmatched_types"] = um
	}
	// ...and its failure half. Forwarding the list but not the error would put the
	// silence straight back: the model would read "no unmatched_types" as "your type
	// filter is fine" in exactly the case where nothing was actually checked.
	if ue, ok := result["unmatched_types_error"]; ok && ue != nil {
		res["unmatched_types_error"] = ue
	}
	return res
}

// jsonResultCompact marshals v WITHOUT indentation. opt3: results the LLM
// consumes do not need pretty-printing; saves ~20-30% whitespace tokens vs the
// default MarshalIndent path.
func jsonResultCompact(v any) (*sdkmcp.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: string(b)}},
	}, nil
}
