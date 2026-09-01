package mcp

import (
	"encoding/json"
	"math"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// slimRecallResult projects a pf_recall response to the fields an LLM actually
// uses, dropping bookkeeping columns and heavy blobs (commits, attrs internals)
// the model never reads. content is kept verbatim (zero info loss). opt3 Phase 1;
// see ieops-docs/polyforge-aihub-improvement/25-optimization3-phase1.md.
func slimRecallResult(result map[string]any) map[string]any {
	return slimRecallResultMode(result, false)
}

// slimRecallResultMode is slimRecallResult with the aihub#313 brief projection
// switched in. It exists as a separate entry point so every pre-existing test
// keeps calling the one-argument form and keeps guarding FULL mode: brief mode
// is additive, and the tests that lock `total`, the truncation pair and
// unmatched_types must not silently start describing the new shape.
//
// aihub#313: `content` is 60.4% of a real 20-item response (measured live
// against prod, 6,969 tok total, 348 tok/item) and `related` another 15.9%.
// Brief drops both, keeping only what the model needs to decide whether a
// memory is worth reading plus the `id` to read it with. This is a projection,
// NOT a filter: the item COUNT is unchanged, because recall breadth is the
// value of recall — narrowing top_k would trade away the thing worth keeping.
func slimRecallResultMode(result map[string]any, brief bool) map[string]any {
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
		if brief {
			out = briefRecallItem(out)
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

// briefContentMax bounds the summary a brief item carries, in runes.
//
// A cap is NOT optional here, and "first line" alone is not a projection. In the
// live 20-item sample, 9 of 20 memories contain no newline at all — for those the
// first line IS the whole (already 800-rune-truncated) body, so an uncapped
// first-line rule would have saved nothing on 45% of the corpus while reading as
// though it saved everything. Measured on that sample: uncapped 29.8% of baseline,
// cap 120 -> 28.5%, cap 80 -> 27.9%. 120 is the knee — it holds a full CJK headline
// (the corpus is markdown, so line 1 is usually a `# title`) and the return below
// 120 is ~0.6 points per 40 runes.
const briefContentMax = 120

// briefFields are the item fields brief mode keeps. Deliberately NOT the slim
// whitelist minus content: `related` (15.9% of a real response) and `tags` are
// dropped too, because a pointer the model cannot act on without a second read
// is exactly the bulk this projection exists to remove. `id` is first and is
// unconditional — see briefRecallItem.
var briefFields = []string{"id", "type", "similarity", "effective_strength", "created_at"}

// trimSubsecond drops the fractional-seconds group from an RFC3339 timestamp,
// leaving second precision and the original offset (Z or +hh:mm). A string that
// carries no fractional group, or that does not look like RFC3339 at all, is
// returned untouched — brief mode must never mangle a field it cannot parse.
func trimSubsecond(s string) string {
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		return s
	}
	end := dot + 1
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	// Only a real fractional group (at least one digit) is removable; "1.x" is not
	// a timestamp we understand, so leave it alone.
	if end == dot+1 {
		return s
	}
	return s[:dot] + s[end:]
}

// briefRecallItem projects one already-slimmed item down to a pointer: enough to
// judge whether the memory is worth reading, plus the id to read it with via
// pf_get_memory. aihub#313.
//
// The escape hatch is aihub#269's, reused rather than reinvented: an item whose
// body was cut carries content_truncated + content_full_len, both already on
// slimRecallResult's whitelist and already named in pf_recall's tool description.
// That is what keeps this a projection instead of silent loss — and it is why
// content_full_len below must stay the TRUE full length. handleRecall may have
// already truncated to 800 runes and recorded the real length; taking the length
// of the string we hold would overwrite 3,587 with 800 and tell the model its
// snippet was nearly whole. Prefer the server's value whenever it is larger.
func briefRecallItem(m map[string]any) map[string]any {
	b := make(map[string]any, len(briefFields)+3)
	for _, k := range briefFields {
		v, ok := m[k]
		if !ok {
			continue
		}
		// Noise trimming, brief mode only — full mode keeps every digit. These two
		// are not cosmetic: on the live 20-item sample they are 251 of 1,981 brief
		// tokens (12.7%), and they carry the difference between 28.4% and 24.8% of
		// the full response, i.e. between missing and meeting this wi's <=25% bar.
		//
		// Nothing is lost that a caller could act on. similarity arrives as
		// 0.2891933706162386 and effective_strength as 2.991597364792209 — the
		// consumers are a relevance ordering and a ">= 0.3" threshold, so digits past
		// the third are float64 exhaust. created_at arrives with microseconds
		// (2026-05-21T20:48:26.806541Z); recency judgements do not resolve below a
		// second. Timestamps stay RFC3339 and parseable — the DATE is NOT truncated,
		// which was the cheaper trim (another 200 tok) and was rejected because
		// time-of-day is real information.
		switch k {
		case "similarity", "effective_strength":
			if f, ok := v.(float64); ok {
				v = math.Round(f*1000) / 1000
			}
		case "created_at":
			if s, ok := v.(string); ok {
				v = trimSubsecond(s)
			}
		}
		b[k] = v
	}
	content, _ := m["content"].(string)
	full := len([]rune(content))
	// JSON numbers arrive as float64 through the REST client's map[string]any.
	if v, ok := m["content_full_len"].(float64); ok && int(v) > full {
		full = int(v)
	}
	line := content
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if r := []rune(line); len(r) > briefContentMax {
		line = string(r[:briefContentMax])
	}
	b["content"] = line
	if len([]rune(line)) < full {
		b["content_truncated"] = true
		b["content_full_len"] = full
	}
	// INVARIANT: id survives every path. Criterion 2 of aihub#313 — a brief item
	// without its id is not a smaller answer, it is an unreadable one, and it would
	// have replaced silent truncation with silent loss. Locked by
	// TestBriefRecallItem_AlwaysCarriesID.
	if _, ok := b["id"]; !ok {
		if v, ok := m["id"]; ok {
			b["id"] = v
		}
	}
	return b
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
