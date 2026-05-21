package auth

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHashKey_Deterministic(t *testing.T) {
	a := HashKey("secret-key")
	b := HashKey("secret-key")
	if a != b {
		t.Errorf("HashKey not deterministic: %s vs %s", a, b)
	}
}

func TestHashKey_DifferentInputs(t *testing.T) {
	if HashKey("a") == HashKey("b") {
		t.Error("different inputs produced same hash")
	}
}

func TestHashKey_HexLength(t *testing.T) {
	got := HashKey("anything")
	if len(got) != 64 {
		t.Errorf("HashKey len = %d, want 64 hex chars", len(got))
	}
}

func TestHashKey_KnownSHA256(t *testing.T) {
	// sha256("") = e3b0c4...
	if HashKey("") != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("HashKey(\"\") wrong: %s", HashKey(""))
	}
}

func TestValidateBearer_ValidKey(t *testing.T) {
	const raw = "the-secret-key"
	keys := []APIKey{
		{ID: "key_abc12345", KeyHash: HashKey(raw)},
	}
	id, ok := ValidateBearer("Bearer "+raw, keys)
	if !ok {
		t.Fatal("expected valid")
	}
	if id != "key_abc12345" {
		t.Errorf("id = %q", id)
	}
}

func TestValidateBearer_RevokedKey(t *testing.T) {
	const raw = "the-secret-key"
	revokedAt := "2025-01-01T00:00:00Z"
	keys := []APIKey{
		{ID: "key_x", KeyHash: HashKey(raw), RevokedAt: &revokedAt},
	}
	id, ok := ValidateBearer("Bearer "+raw, keys)
	if ok {
		t.Errorf("revoked key should not validate, got id=%q", id)
	}
}

func TestValidateBearer_WrongKey(t *testing.T) {
	keys := []APIKey{
		{ID: "key_x", KeyHash: HashKey("real-key")},
	}
	_, ok := ValidateBearer("Bearer wrong-key", keys)
	if ok {
		t.Error("wrong key validated")
	}
}

func TestValidateBearer_EmptyHeader(t *testing.T) {
	_, ok := ValidateBearer("", []APIKey{{ID: "x", KeyHash: HashKey("k")}})
	if ok {
		t.Error("empty header validated")
	}
}

func TestValidateBearer_NoBearerPrefix(t *testing.T) {
	_, ok := ValidateBearer("Basic abc", []APIKey{{ID: "x", KeyHash: HashKey("k")}})
	if ok {
		t.Error("non-Bearer scheme validated")
	}
}

func TestValidateBearer_EmptyKeyList(t *testing.T) {
	_, ok := ValidateBearer("Bearer anything", nil)
	if ok {
		t.Error("empty key list validated")
	}
}

func TestValidateBearer_PicksFirstUnrevokedMatch(t *testing.T) {
	// Defensive: if two keys share a hash (shouldn't happen in production)
	// and one is revoked, the unrevoked one wins.
	const raw = "shared"
	revoked := "2025-01-01T00:00:00Z"
	keys := []APIKey{
		{ID: "revoked_one", KeyHash: HashKey(raw), RevokedAt: &revoked},
		{ID: "active_one", KeyHash: HashKey(raw)},
	}
	id, ok := ValidateBearer("Bearer "+raw, keys)
	if !ok || id != "active_one" {
		t.Errorf("got id=%q ok=%v, want active_one true", id, ok)
	}
}

func TestParseAPIKeys_Roundtrip(t *testing.T) {
	src := []APIKey{
		{ID: "key_a", KeyHash: "h1", Name: "primary"},
		{ID: "key_b", KeyHash: "h2", Name: "ci"},
	}
	b, err := json.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseAPIKeys(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "key_a" || got[1].ID != "key_b" {
		t.Errorf("ParseAPIKeys = %+v", got)
	}
}

func TestParseAPIKeys_Invalid(t *testing.T) {
	_, err := ParseAPIKeys([]byte("not-json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid") && !strings.Contains(err.Error(), "character") {
		// json error messages may vary by version; existence is the contract
		t.Logf("got error: %v", err)
	}
}

func TestParseAPIKeys_EmptyArray(t *testing.T) {
	got, err := ParseAPIKeys([]byte("[]"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %d keys, want 0", len(got))
	}
}
