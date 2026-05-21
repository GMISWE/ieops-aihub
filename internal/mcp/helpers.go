package mcp

import (
	"encoding/json"
	"fmt"
	"net/url"
)

// marshalJSON marshals v to JSON bytes.
func marshalJSON(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
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

// boolArg extracts a bool argument from MCP call arguments map.
func boolArg(args map[string]any, key string) bool {
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// setIfNonempty adds key=value to params if value is non-empty.
func setIfNonempty(params url.Values, key, value string) {
	if value != "" {
		params.Set(key, value)
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
