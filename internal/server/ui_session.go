package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Web UI session cookie configuration.
//
// The cookie carries a signed token "<payload>.<sig>" where:
//   - payload = base64url("<user_id>|<api_key_id>|<exp_unix>")
//   - sig     = hex(HMAC-SHA256(secret, payload))
//
// We mint sessions from a valid API key (verified via the same lookup that
// BearerAuth uses) so a cookie is equivalent to a short-lived bearer key.
// Verification is constant-time; tampered, expired, or malformed tokens
// surface as errors and the middleware redirects to /ui/login.
const (
	pfSessionCookieName = "pf_session"
	pfSessionTTL        = 7 * 24 * time.Hour
)

// Errors returned by Verify so callers can distinguish failure modes for logs.
var (
	errSessionMalformed = errors.New("session: malformed token")
	errSessionBadSig    = errors.New("session: invalid signature")
	errSessionExpired   = errors.New("session: expired")
	errSessionBadPayload = errors.New("session: bad payload")
)

// SessionManager signs and verifies UI session cookies.
type SessionManager struct {
	secret []byte
}

// NewSessionManager constructs a SessionManager. The caller is responsible
// for supplying a secret of sufficient entropy (>= 32 bytes); a short secret
// works but defeats the purpose. We do not validate length here so tests can
// use small fixed values.
func NewSessionManager(secret []byte) *SessionManager {
	// Copy so the caller mutating the slice later cannot affect us.
	s := make([]byte, len(secret))
	copy(s, secret)
	return &SessionManager{secret: s}
}

// Sign produces a cookie token valid for ttl from now.
func (sm *SessionManager) Sign(userID, apiKeyID string, ttl time.Duration) string {
	exp := time.Now().Add(ttl).Unix()
	raw := userID + "|" + apiKeyID + "|" + strconv.FormatInt(exp, 10)
	payload := base64.RawURLEncoding.EncodeToString([]byte(raw))
	mac := hmac.New(sha256.New, sm.secret)
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return payload + "." + sig
}

// Verify parses and validates a token. On success returns (userID, apiKeyID).
// On any failure returns an error describing the kind; the caller must not
// disclose specifics to the client.
func (sm *SessionManager) Verify(token string) (string, string, error) {
	dot := strings.LastIndexByte(token, '.')
	if dot <= 0 || dot == len(token)-1 {
		return "", "", errSessionMalformed
	}
	payload := token[:dot]
	gotSigHex := token[dot+1:]

	// Recompute expected signature.
	mac := hmac.New(sha256.New, sm.secret)
	mac.Write([]byte(payload))
	wantSig := mac.Sum(nil)

	gotSig, err := hex.DecodeString(gotSigHex)
	if err != nil {
		return "", "", errSessionBadSig
	}
	if !hmac.Equal(gotSig, wantSig) {
		return "", "", errSessionBadSig
	}

	rawBytes, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", "", errSessionBadPayload
	}
	parts := strings.Split(string(rawBytes), "|")
	if len(parts) != 3 {
		return "", "", errSessionBadPayload
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", "", errSessionBadPayload
	}
	if time.Now().Unix() >= exp {
		return "", "", errSessionExpired
	}
	return parts[0], parts[1], nil
}
