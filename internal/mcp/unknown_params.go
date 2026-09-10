package mcp

// aihub#389 phase 1 — an argument no tool publishes must not vanish behind a
// success response.
//
// ─── The defect, measured ─────────────────────────────────────────────────
//
// `objectSchema` emits `type`, `properties` and an optional `required`, and
// JSON Schema's default for a missing `additionalProperties` is *allowed*. So
// go-sdk's pre-handler validation passes anything, the handler forwards it, and
// echo's `c.Bind` ignores unknown JSON fields. Measured live on 2026-09-07
// (aihub#383): `pf_update_work_item(work_item_id="aihub#383", brief=true,
// bogus_probe_383="never-published-parameter")` returned 200 with the work item
// unchanged and no error from the harness, this process, the handler or the
// server. A misspelled parameter name is indistinguishable from a working call.
//
// That is not a hypothetical cost. Over 21 days of transcripts, 301 of 12,133
// pf_* calls (2.5%) carried at least one unpublished parameter, and
// `pf_update_step.expected_version` alone accounts for 202 of them — 18% of
// calls to the most-used write tool. Each one was a caller believing it had
// requested compare-and-set protection it never got.
//
// ─── Report, do NOT reject — and why that is not the weak option ──────────
//
// `additionalProperties:false` would be enforced by the SDK with no server
// change, and it is the right END state. Flipping it today would 400 roughly
// one `pf_update_step` call in five, i.e. break pf-execute for every agent mid
// run. So phase 1 makes every unknown parameter VISIBLE in the same response,
// which does two things a silent drop cannot: the caller can self-correct
// within the session, and the corpus re-measure that licenses the flip becomes
// possible. Phase 2 (a separate work item) sets
// `additionalProperties:false` once that number is under 0.1%.
//
// 🔴 Do NOT "simplify" this by adding `additionalProperties:false` here. It is
// not an improvement on this file, it is phase 2, and the whole point of the
// two-phase split is that the number licensing it does not exist yet.
//
// ─── Shape: an entry in the EXISTING request_adjusted list ────────────────
//
// The repo already has one generic field for "the server changed what you
// sent": `request_adjusted`, a LIST of `{param, requested, applied}` — see
// internal/domain/request_adjusted.go for why it is one field and not one per
// clamp, and why the key is absent rather than empty when nothing happened.
// This appends to that list rather than introducing a sibling key:
//
//	"request_adjusted":[{"param":"unknown_params","requested":["bogus_a"],"applied":[]}]
//
// ⚠️ APPENDS, and that matters. `request_adjusted` may ALREADY be populated by
// the server on the same response — `pf_list_work_items` with `limit=500` and a
// bogus argument is one call that produces both — so writing this as an OBJECT
// under the same key, or assigning over it, would destroy the server's own
// disclosure. That would be a new instance of the exact defect this file exists
// to fix, one layer up.
//
// `applied` is the empty list because none of the named parameters were applied
// to anything. It is not `null`: domain.RequestAdjustment's contract is that a
// present entry makes a claim, and "we used nothing you sent under these names"
// is that claim.
//
// ─── Where this runs: the registration path, once ─────────────────────────
//
// `(*Server).addTool` in server.go is the single wrapper every tool is
// registered through, so the diff is computed in one place against each tool's
// OWN published schema. It is deliberately not per-handler: 50 copies of a
// check is 50 chances for the 51st tool to be added without one. The gate
// (unknown_params_test.go) drives every registered tool through an in-memory
// MCP client and asserts the disclosure comes back, so a tool registered
// around the wrapper fails there rather than being quietly exempt.

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// unknownParamsField is the `param` value of the disclosure entry. One stable
// name so a caller (or the corpus re-measure that licenses phase 2) can select
// these entries without matching prose.
const unknownParamsField = "unknown_params"

// publishedParamNames returns the top-level property names of a tool's
// InputSchema.
//
// Takes `any` because that is the type sdkmcp.Tool.InputSchema has: this repo
// always assigns a json.RawMessage built by objectSchema, but the SDK also
// accepts a *jsonschema.Schema, and marshalling covers both without this
// function having to know which. json.Marshal of a json.RawMessage is the bytes
// themselves, so the common path costs nothing.
//
// Returns an EMPTY set when the schema has no properties block, which is the
// correct answer for `emptyObjectSchema()` — a tool that publishes no
// parameters, where every argument is unknown. Distinguishing "no properties"
// from "schema unreadable" matters, so an unreadable schema is reported by the
// error instead: silently folding a parse failure into "publishes nothing"
// would report every argument of every call to that tool as unknown, with
// nothing saying why.
func publishedParamNames(schema any) (map[string]struct{}, error) {
	if schema == nil {
		return nil, nil
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("marshal InputSchema: %w", err)
	}
	var decoded struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("decode InputSchema: %w", err)
	}
	out := make(map[string]struct{}, len(decoded.Properties))
	for name := range decoded.Properties {
		out[name] = struct{}{}
	}
	return out, nil
}

// unknownParamNames returns the sorted top-level argument names the published
// set does not contain.
//
// Only TOP-LEVEL names. A misspelled key inside `declared_resources[]` or
// `items[]` is a different problem with a different fix (those entry schemas
// are validated server-side; see domain.ValidateDeclaredResources), and
// pretending to cover it would be worse than not covering it — a caller reading
// "no unknown params" as "the whole payload was understood" is exactly the
// over-reading domain/request_adjusted.go warns against for its own field.
//
// Unparseable arguments yield nothing rather than an error: the SDK has already
// accepted them and the handler is about to fail on its own terms, so inventing
// a second complaint here would only obscure the real one.
func unknownParamNames(raw json.RawMessage, published map[string]struct{}) []string {
	if len(raw) == 0 {
		return nil
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil
	}
	var out []string
	for name := range args {
		if _, ok := published[name]; !ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// unknownParamsAdjustment builds the disclosure entry.
func unknownParamsAdjustment(unknown []string) domain.RequestAdjustment {
	return domain.RequestAdjustment{
		Param:     unknownParamsField,
		Requested: unknown,
		Applied:   []string{},
	}
}

// unknownParamsLogLine builds the stderr sentence, per tool family, and is its
// own function so the WORDING is probeable (aihub#570). The original line said
// the names "were forwarded to nothing and had no effect" — measured false in
// its first half: a wholesale-forwarding handler (pf_update_work_item is the
// one this file's own header measured live) puts every unknown key on the
// wire, where the server's JSON binding drops it. "Had no effect" was always
// true; the stated mechanism was not, and a caller probing what the server
// receives would catch this process asserting the opposite of what it does.
//
// Two branches because aihub#586 split the truth: the memory-write family's
// unknown set is stripped at this boundary before the request is built, so for
// those three tools nothing IS forwarded and the line may say so. Every other
// tool gets the disjunction that covers both remaining shapes — a typed-body
// handler never places the key on the wire, a wholesale forwarder sends it to
// the server, which drops it at its JSON binding — because this hop knows the
// tool name, not the handler's body-building style, and naming the wrong arm
// for a given tool would be this defect again one sentence over.
// unknown_params_wording_test.go pins both branches and refuses the old clause
// by its exact words.
func unknownParamsLogLine(tool string, unknown []string) string {
	mechanism := "they had no effect: this tool's handler either never placed them on the wire, or " +
		"sent them to the server, which dropped them at its JSON binding"
	if wireStrippedTools[tool] {
		mechanism = "they were stripped at this boundary before the request was built, so nothing " +
			"was forwarded and they had no effect (aihub#586)"
	}
	return fmt.Sprintf("polyforge: %s received %d parameter(s) it does not publish: %v — %s (aihub#389)\n",
		tool, len(unknown), unknown, mechanism)
}

// discloseUnknownParams logs the extras to stderr and attaches them to the
// result. Safe on a nil result.
func discloseUnknownParams(tool string, unknown []string, res *sdkmcp.CallToolResult) {
	if len(unknown) == 0 {
		return
	}
	// stderr as well as the response, per the decision: the response reaches the
	// model that can self-correct, and the log reaches whoever is counting these
	// to decide whether phase 2 is safe. Neither substitutes for the other — an
	// MCP server's stderr goes to a file the calling agent never reads, which is
	// why response-only was never enough and log-only would have been invisible.
	fmt.Fprint(os.Stderr, unknownParamsLogLine(tool, unknown))
	if res == nil {
		return
	}
	attachRequestAdjustment(res, unknownParamsAdjustment(unknown))
}

// attachRequestAdjustment appends entry to the result's `request_adjusted` list.
//
// Two paths, and the second is not a fallback for tidiness:
//
//  1. the result is a single TextContent holding a JSON OBJECT — the shape every
//     jsonResult produces. The entry is merged into that object's
//     `request_adjusted` list, APPENDING to whatever the server already put
//     there.
//  2. anything else — an error result (a bare string), a JSON array, several
//     content blocks. The entry goes into an ADDITIONAL text block. An error
//     result is the case that makes this worth having rather than skipping: an
//     unknown parameter is a plausible cause of the error the caller is looking
//     at, so that is the response that most needs to mention it.
//
// ⚠️ Path 1 re-marshals the object, so its keys come back in Go's map order
// (sorted) rather than in the order the handler emitted them. That is a real
// change and it is confined to responses that already carry a caller mistake;
// numbers are decoded with UseNumber so no integer is reformatted through
// float64 on the way through.
func attachRequestAdjustment(res *sdkmcp.CallToolResult, entry domain.RequestAdjustment) {
	if len(res.Content) == 1 && !res.IsError {
		if tc, ok := res.Content[0].(*sdkmcp.TextContent); ok {
			if merged, ok := mergeAdjustmentIntoObject(tc.Text, entry); ok {
				tc.Text = merged
				return
			}
		}
	}
	b, err := marshalJSON(map[string]any{
		"request_adjusted": []domain.RequestAdjustment{entry},
	})
	if err != nil {
		return
	}
	res.Content = append(res.Content, &sdkmcp.TextContent{Text: string(b)})
}

// mergeAdjustmentIntoObject appends entry to text's `request_adjusted` list,
// reporting false when text is not a JSON object.
func mergeAdjustmentIntoObject(text string, entry domain.RequestAdjustment) (string, bool) {
	dec := json.NewDecoder(strings.NewReader(text))
	// UseNumber keeps every number as its original literal. Without it a decode
	// -encode round trip runs integers through float64, which is exact only below
	// 2^53 and reformats anything larger into scientific notation — silently
	// corrupting a value on a response the caller is already looking at because
	// something was wrong.
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return "", false
	}

	existing, _ := obj["request_adjusted"].([]any)
	obj["request_adjusted"] = append(existing, map[string]any{
		"param":     entry.Param,
		"requested": entry.Requested,
		"applied":   entry.Applied,
	})

	b, err := marshalJSON(obj)
	if err != nil {
		return "", false
	}
	return string(b), true
}
