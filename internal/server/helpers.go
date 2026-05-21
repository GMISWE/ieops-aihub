package server

import (
	"encoding/json"
)

// jsonMarshal is a thin wrapper around encoding/json.Marshal.
func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}
