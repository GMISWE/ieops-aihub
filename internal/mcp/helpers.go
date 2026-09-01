package mcp

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
)

// marshalJSON marshals v to compact JSON bytes. Compact (not indented) is the
// single marshal point behind all ~51 jsonResult call sites; the indentation
// whitespace was ~15-25% of every MCP tool response for no consumer benefit —
// the client parses the JSON, it does not read it pretty-printed (aihub#212).
func marshalJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal JSON: %w", err)
	}
	return b, nil
}

// strArg extracts a string argument from MCP call arguments map.
func strArg(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// parseBoolArg decodes a boolean MCP argument, reporting whether it was present
// and whether it was readable.
//
// aihub#280: the MCP SDK's untyped AddTool — the form every aihub tool uses —
// type-checks the *schema shape* at registration and then stores the handler
// with no per-call validation, so nothing between the wire and here enforces the
// published type. A caller that sends `ready_only: "true"` against a param
// declared `boolean` reaches this function with a string.
//
// The old body returned false for anything that was not a Go bool, which made
// that request return the UNFILTERED list with no error at any hop — the exact
// symptom this wi exists to remove, on a param this wi added. It is not
// hypothetical either: csvArg exists because real callers sent `ids=[...]` and
// `status=["wrapped"]` against params declared `string`. Whatever produces those
// shapes does not coerce to the declared type, and booleans are no different.
//
// Returns:
//
//	present=false           → absent or JSON null; caller should treat as unset
//	present=true, ok=false  → sent, but not a boolean in any spelling
//	present=true, ok=true   → value is usable
func parseBoolArg(args map[string]any, key string) (value, present, ok bool) {
	v, exists := args[key]
	if !exists || v == nil {
		return false, false, true
	}
	switch typed := v.(type) {
	case bool:
		return typed, true, true
	case string:
		// Same spellings strconv.ParseBool accepts, matching parseListWIBool on
		// the server side so the two ends of the hop agree on what a bool is.
		b, err := strconv.ParseBool(strings.TrimSpace(typed))
		if err != nil {
			return false, true, false
		}
		return b, true, true
	case float64:
		switch typed {
		case 0:
			return false, true, true
		case 1:
			return true, true, true
		}
		return false, true, false
	case int:
		switch typed {
		case 0:
			return false, true, true
		case 1:
			return true, true, true
		}
		return false, true, false
	}
	return false, true, false
}

// boolArg extracts a bool argument from MCP call arguments map.
//
// Signature deliberately unchanged so the ~50 other tools that call it are not
// touched; they simply become tolerant of the string and 0/1 spellings instead
// of silently reading them as false. Callers that need to REJECT an unreadable
// value (rather than default it) use parseBoolArg directly — see
// buildListWorkItemsParams.
func boolArg(args map[string]any, key string) bool {
	v, _, _ := parseBoolArg(args, key)
	return v
}

// scalarArg renders a JSON scalar (string, number, or bool) as the string form
// the aihub HTTP API parses.
//
// aihub#280 / B6: `limit` is published as a string, but pf-retro and
// pf-crystallize both send `limit=100` as a JSON *number*. strArg returns "" for
// a non-string, so setIfNonempty dropped it and the server fell back to its
// default of 50 — the caller silently received half the data it asked for, with
// no error to notice. Numbers are rendered without a trailing ".0" because they
// arrive as float64 from encoding/json but mean integers here.
func scalarArg(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	switch typed := v.(type) {
	case string:
		return typed
	case float64:
		if typed == math.Trunc(typed) && !math.IsInf(typed, 0) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case bool:
		return strconv.FormatBool(typed)
	}
	return ""
}

// strSliceArg extracts a []string argument, skipping any entry that is not a
// string. Absent, null or non-array values yield nil, which every caller reads
// as "not specified" rather than "empty selection".
//
// Extracted from pf_commit's inline loop so pf_ship (aihub#286) shares one
// definition of how `paths` is decoded: two copies of this could drift, and the
// two tools stage files for the same worktree.
func strSliceArg(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// csvArg extracts an argument that may arrive as either a JSON string or a
// JSON array of strings, and renders it as the comma-separated form the aihub
// HTTP API parses with strings.Split.
//
// aihub#280: strArg returns "" for anything that is not a string, so a param
// declared `string` but *called* with an array was silently dropped — the whole
// argument vanished with no error at any hop. Three polyforge skills sent
// `ids=[...]` and three sent `status=["wrapped"]`; every one of those filters
// was being discarded. Accepting both shapes at the decoder is what makes the
// drop impossible rather than merely fixed at today's call sites.
//
// Absent, null, a wrong type, or an array with no usable entries all yield "",
// which setIfNonempty treats as "not specified" — never as an empty selection.
func csvArg(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok {
		return ""
	}
	switch typed := v.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if s, ok := item.(string); ok && s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ",")
	}
	return ""
}

// numArg extracts a float64 argument (returns 0 if absent or wrong type).
func numArg(args map[string]any, key string) float64 {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		}
	}
	return 0
}

// normalizeIntArg rewrites args[key] in place to a Go int so it serializes into
// the request body as a JSON number.
//
// aihub#241: pf_update_work_item published `resources_version` as JSON-schema
// type "string" while domain.UpdateWorkItemRequest.ResourcesVersion is *int, so
// the value reached echo's c.Bind as `"0"` and every request carrying it failed
// with 400 BAD_REQUEST "invalid request body" — CAS was unusable on the only
// path that could have provided it. The schema now says "integer", but a
// mixed-version client (or a human calling the tool by hand) can still send the
// quoted form, so coerce here as well rather than trusting the schema alone: a
// wrong type must not turn into an opaque 400 two layers away.
//
// Returns an error naming the field when the value is present but not an
// integer. Absent keys are left untouched and report no error.
func normalizeIntArg(args map[string]any, key string) error {
	v, ok := args[key]
	if !ok || v == nil {
		return nil
	}
	switch n := v.(type) {
	case int:
		return nil
	case float64:
		// encoding/json decodes every JSON number into float64.
		if n != math.Trunc(n) {
			return fmt.Errorf("%s must be an integer, got %v", key, n)
		}
		// Out-of-range float64 -> int is implementation-defined in Go, so reject
		// rather than silently binding a garbage value into the request body.
		if n > math.MaxInt32 || n < math.MinInt32 {
			return fmt.Errorf("%s is out of range for an integer, got %v", key, n)
		}
		args[key] = int(n)
		return nil
	case json.Number:
		// Not reachable through parseArgs today (it uses a plain json.Unmarshal,
		// which yields float64), but cheap insurance if a caller ever switches to
		// a decoder with UseNumber — a silent regression to the original bug.
		i, err := n.Int64()
		if err != nil {
			return fmt.Errorf("%s must be an integer, got %q", key, n.String())
		}
		args[key] = int(i)
		return nil
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return fmt.Errorf("%s must be an integer, got %q", key, n)
		}
		args[key] = i
		return nil
	default:
		return fmt.Errorf("%s must be an integer, got %T", key, v)
	}
}

// setIfNonempty adds key=value to params if value is non-empty.
func setIfNonempty(params url.Values, key, value string) {
	if value != "" {
		params.Set(key, value)
	}
}

// prPayload builds the pr_opened event payload from GHCreatePR's result map,
// pulling "url"/"number" defensively — gh's output shape varies (a fresh PR
// has both; an "already exists" response has neither, just "existing"/
// "message") so we only include the keys that are actually present.
func prPayload(repo, title string, result map[string]any) map[string]any {
	payload := map[string]any{"repo": repo, "title": title}
	if url, ok := result["url"]; ok {
		payload["url"] = url
	}
	if number, ok := result["number"]; ok {
		payload["number"] = number
	}
	return payload
}

// addWorktrees sets result["worktrees"] from a state file's worktree map, if
// non-empty. Used by pf_wrap / pf_complete_attempt / pf_claim_work_item so
// callers get the worktree paths back without needing to have read the state
// file themselves first (aihub#207).
func addWorktrees(result map[string]any, worktrees map[string]string) {
	if len(worktrees) > 0 {
		result["worktrees"] = worktrees
	}
}

// parseArgs unmarshals the raw MCP arguments into a map.
func parseArgs(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}
	return m, nil
}

// isAihubCode checks whether the error message from the aihub client contains
// the given error code (e.g. "PROJECT_NOT_FOUND").
func isAihubCode(err error, code string) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), code)
}
