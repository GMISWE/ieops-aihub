// Package auth handles API key validation and AttemptCredential verification.
package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// APIKey represents an entry in users.api_keys JSONB array.
type APIKey struct {
	ID           string  `json:"id"`
	KeyHash      string  `json:"key_hash"`
	Name         string  `json:"name"`
	ProjectScope *string `json:"project_scope,omitempty"`
	RevokedAt    *string `json:"revoked_at,omitempty"`
}

// HashKey returns the sha256 hex of a raw API key.
func HashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// ParseAPIKeys deserializes users.api_keys JSONB.
func ParseAPIKeys(raw []byte) ([]APIKey, error) {
	var keys []APIKey
	return keys, json.Unmarshal(raw, &keys)
}

// ValidateBearer validates "Bearer <key>" header value and returns the key_id.
// Returns ("", false) if invalid.
func ValidateBearer(authHeader string, apiKeys []APIKey) (keyID string, valid bool) {
	raw, ok := strings.CutPrefix(authHeader, "Bearer ")
	if !ok {
		return "", false
	}
	h := HashKey(raw)
	for _, k := range apiKeys {
		if k.KeyHash == h && k.RevokedAt == nil {
			return k.ID, true
		}
	}
	return "", false
}
