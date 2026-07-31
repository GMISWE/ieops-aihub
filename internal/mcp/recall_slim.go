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
	keep := map[string]bool{
		"id": true, "type": true, "content": true, "effective_strength": true,
		"similarity": true, "work_item_id": true, "tags": true, "related": true,
		"created_at": true,
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
