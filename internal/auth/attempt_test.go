package auth

import (
	"testing"
)

func TestHashSecret_Hex64(t *testing.T) {
	got := HashSecret("anything")
	if len(got) != 64 {
		t.Errorf("HashSecret len = %d, want 64", len(got))
	}
	for _, c := range got {
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		if !isHex {
			t.Errorf("HashSecret produced non-hex char %q", c)
		}
	}
}

func TestHashSecret_Deterministic(t *testing.T) {
	a := HashSecret("session-secret")
	b := HashSecret("session-secret")
	if a != b {
		t.Errorf("HashSecret not deterministic: %s vs %s", a, b)
	}
}

func TestHashSecret_DifferentInputs(t *testing.T) {
	if HashSecret("a") == HashSecret("b") {
		t.Error("different inputs produced same hash")
	}
}

func TestParseAttemptHeader_Valid(t *testing.T) {
	cred, ok := ParseAttemptHeader("Attempt ra_abc12345/3/deadbeef")
	if !ok {
		t.Fatal("expected valid")
	}
	if cred.AttemptID != "ra_abc12345" {
		t.Errorf("AttemptID = %q", cred.AttemptID)
	}
	if cred.ClaimEpoch != 3 {
		t.Errorf("ClaimEpoch = %d", cred.ClaimEpoch)
	}
	if cred.SessionSecret != "deadbeef" {
		t.Errorf("SessionSecret = %q", cred.SessionSecret)
	}
}

func TestParseAttemptHeader_NoPrefix(t *testing.T) {
	if _, ok := ParseAttemptHeader("Bearer xxx"); ok {
		t.Error("non-Attempt prefix should fail")
	}
	if _, ok := ParseAttemptHeader(""); ok {
		t.Error("empty header should fail")
	}
}

func TestParseAttemptHeader_MissingFields(t *testing.T) {
	cases := []string{
		"Attempt ",
		"Attempt only",
		"Attempt only/2",
		"Attempt only/two/secret", // non-numeric epoch
	}
	for _, h := range cases {
		t.Run(h, func(t *testing.T) {
			if _, ok := ParseAttemptHeader(h); ok {
				t.Errorf("header %q should not parse", h)
			}
		})
	}
}

func TestParseAttemptHeader_ZeroEpoch(t *testing.T) {
	cred, ok := ParseAttemptHeader("Attempt ra_x/0/secret")
	if !ok {
		t.Fatal("expected valid")
	}
	if cred.ClaimEpoch != 0 {
		t.Errorf("ClaimEpoch = %d, want 0", cred.ClaimEpoch)
	}
}

func TestParseAttemptHeader_SecretMayContainSlash(t *testing.T) {
	// SplitN with limit 3 means everything after the second slash is the secret.
	cred, ok := ParseAttemptHeader("Attempt ra_x/1/abc/def/ghi")
	if !ok {
		t.Fatal("expected valid")
	}
	if cred.SessionSecret != "abc/def/ghi" {
		t.Errorf("SessionSecret = %q, want abc/def/ghi", cred.SessionSecret)
	}
}

func TestParseAttemptHeader_NegativeEpoch(t *testing.T) {
	// fmt.Sscanf %d accepts negative ints; we treat it as parsed (callers can
	// later reject). This documents current behavior, not desired behavior.
	cred, ok := ParseAttemptHeader("Attempt ra_x/-1/secret")
	if !ok {
		t.Fatal("expected parse to succeed")
	}
	if cred.ClaimEpoch != -1 {
		t.Errorf("ClaimEpoch = %d, want -1", cred.ClaimEpoch)
	}
}
