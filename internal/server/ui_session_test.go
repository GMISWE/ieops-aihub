package server

import (
	"strings"
	"testing"
	"time"
)

func TestSession_SignVerifyRoundtrip(t *testing.T) {
	sm := NewSessionManager([]byte("test-secret-must-be-long-enough-for-hmac"))
	token := sm.Sign("u_abc", "k_xyz", pfSessionTTL)
	uid, kid, err := sm.Verify(token)
	if err != nil {
		t.Fatalf("verify roundtrip: %v", err)
	}
	if uid != "u_abc" || kid != "k_xyz" {
		t.Fatalf("verify roundtrip: got (%q, %q), want (u_abc, k_xyz)", uid, kid)
	}
}

func TestSession_TamperedSigRejected(t *testing.T) {
	sm := NewSessionManager([]byte("test-secret"))
	token := sm.Sign("u_abc", "k_xyz", pfSessionTTL)
	// Flip the last byte of the signature.
	tampered := token[:len(token)-1] + flipHex(token[len(token)-1])
	if _, _, err := sm.Verify(tampered); err == nil {
		t.Fatalf("expected sig-tampered token to fail verify")
	}
}

func TestSession_TamperedPayloadRejected(t *testing.T) {
	sm := NewSessionManager([]byte("test-secret"))
	token := sm.Sign("u_abc", "k_xyz", pfSessionTTL)
	// Swap one char in the payload section (before the dot).
	dot := strings.LastIndexByte(token, '.')
	if dot <= 1 {
		t.Fatalf("unexpected token shape: %s", token)
	}
	tampered := token[:dot-1] + flipBase64URL(token[dot-1]) + token[dot:]
	if _, _, err := sm.Verify(tampered); err == nil {
		t.Fatalf("expected payload-tampered token to fail verify")
	}
}

func TestSession_ExpiredRejected(t *testing.T) {
	sm := NewSessionManager([]byte("test-secret"))
	// Sign with a TTL in the past — Sign clamps to time.Now()+ttl.
	token := sm.Sign("u_abc", "k_xyz", -1*time.Second)
	if _, _, err := sm.Verify(token); err == nil {
		t.Fatalf("expected expired token to fail verify")
	}
}

func TestSession_WrongSecretRejected(t *testing.T) {
	sm1 := NewSessionManager([]byte("secret-one"))
	sm2 := NewSessionManager([]byte("secret-two"))
	token := sm1.Sign("u_abc", "k_xyz", pfSessionTTL)
	if _, _, err := sm2.Verify(token); err == nil {
		t.Fatalf("expected token signed with sm1 to fail verify on sm2")
	}
}

func TestSession_MalformedRejected(t *testing.T) {
	sm := NewSessionManager([]byte("test-secret"))
	cases := []string{
		"",
		"nodot",
		".onlydot",
		"onlydot.",
		"bad@@@.deadbeef",
	}
	for _, c := range cases {
		if _, _, err := sm.Verify(c); err == nil {
			t.Errorf("expected malformed token %q to fail verify", c)
		}
	}
}

// flipHex returns a hex char different from the given hex char.
func flipHex(c byte) string {
	if c == '0' {
		return "1"
	}
	return "0"
}

// flipBase64URL flips a base64url char while staying in the alphabet.
func flipBase64URL(c byte) string {
	if c == 'A' {
		return "B"
	}
	return "A"
}
