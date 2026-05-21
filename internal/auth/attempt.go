package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// AttemptCredential is the three-tuple required for attempt-authenticated ops.
type AttemptCredential struct {
	AttemptID     string
	ClaimEpoch    int64
	SessionSecret string
}

// ParseAttemptHeader parses "Attempt <id>/<epoch>/<secret>" Authorization header.
func ParseAttemptHeader(authHeader string) (AttemptCredential, bool) {
	rest, ok := strings.CutPrefix(authHeader, "Attempt ")
	if !ok {
		return AttemptCredential{}, false
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 {
		return AttemptCredential{}, false
	}
	var epoch int64
	if _, err := fmt.Sscanf(parts[1], "%d", &epoch); err != nil {
		return AttemptCredential{}, false
	}
	return AttemptCredential{AttemptID: parts[0], ClaimEpoch: epoch, SessionSecret: parts[2]}, true
}

// HashSecret returns sha256 hex of a session secret.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
