package domain

import (
	"strings"
	"testing"
)

func TestNewID_Format(t *testing.T) {
	id := NewID("wi")
	if !strings.HasPrefix(id, "wi_") {
		t.Fatalf("id %q missing prefix wi_", id)
	}
	tail := strings.TrimPrefix(id, "wi_")
	if len(tail) != 8 {
		t.Fatalf("id %q tail length = %d, want 8", id, len(tail))
	}
	for _, c := range tail {
		if !strings.ContainsRune(base62Chars, c) {
			t.Fatalf("id %q tail contains non-base62 rune %q", id, c)
		}
	}
}

func TestNewID_DifferentPrefixes(t *testing.T) {
	prefixes := []string{"wi", "evt", "mem", "ra"}
	for _, p := range prefixes {
		id := NewID(p)
		if !strings.HasPrefix(id, p+"_") {
			t.Errorf("NewID(%q) = %q, missing prefix", p, id)
		}
	}
}

func TestNewID_Unique(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	for i := range n {
		id := NewID("wi")
		if _, ok := seen[id]; ok {
			t.Fatalf("collision after %d ids: %q already seen", i, id)
		}
		seen[id] = struct{}{}
	}
}

// TestNewID_DistributionSanity is a coarse sanity check that NewID uses a
// cryptographically uniform source (no obvious bias). We do not enforce a
// strict chi-squared bound — only that every base62 character appears at
// least once across a large sample.
func TestNewID_DistributionSanity(t *testing.T) {
	const samples = 5000
	seen := make(map[byte]int, 62)
	for range samples {
		id := NewID("x")
		tail := strings.TrimPrefix(id, "x_")
		for j := 0; j < len(tail); j++ {
			seen[tail[j]]++
		}
	}
	for i := 0; i < len(base62Chars); i++ {
		c := base62Chars[i]
		if seen[c] == 0 {
			t.Errorf("base62 char %q never produced across %d samples — suspicious distribution", c, samples)
		}
	}
}

func TestNewBase62_Length(t *testing.T) {
	for _, n := range []int{0, 1, 8, 12, 32} {
		got := NewBase62(n)
		if len(got) != n {
			t.Errorf("NewBase62(%d) length = %d, want %d", n, len(got), n)
		}
		for _, c := range got {
			if !strings.ContainsRune(base62Chars, c) {
				t.Errorf("NewBase62 produced non-base62 char %q", c)
			}
		}
	}
}

func TestNewBase62_Unique(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	for i := range n {
		s := NewBase62(12)
		if _, ok := seen[s]; ok {
			t.Fatalf("NewBase62 collision after %d samples: %q", i, s)
		}
		seen[s] = struct{}{}
	}
}

func TestFormatIDOrSlug(t *testing.T) {
	tests := []struct {
		in       string
		wantCol  string
		wantVal  string
	}{
		{"wi_abc12345", "id", "wi_abc12345"},
		{"my-slug", "slug", "my-slug"},
		{"", "slug", ""},
		// Other prefixes are not IDs by this function's contract.
		{"mem_abc12345", "slug", "mem_abc12345"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			gotCol, gotVal := FormatIDOrSlug(tt.in)
			if gotCol != tt.wantCol || gotVal != tt.wantVal {
				t.Errorf("FormatIDOrSlug(%q) = (%q,%q), want (%q,%q)",
					tt.in, gotCol, gotVal, tt.wantCol, tt.wantVal)
			}
		})
	}
}

func TestHashSecretInternal(t *testing.T) {
	got := hashSecretInternal("hello")
	if len(got) != 64 {
		t.Errorf("hashSecretInternal len = %d, want 64", len(got))
	}
	// Determinism
	if hashSecretInternal("hello") != got {
		t.Error("hashSecretInternal is not deterministic")
	}
	if hashSecretInternal("hello") == hashSecretInternal("world") {
		t.Error("hashSecretInternal returns same hash for different inputs")
	}
	// Known sha256 of "" for sanity
	emptyHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if hashSecretInternal("") != emptyHash {
		t.Errorf("hashSecretInternal(\"\") = %s, want %s", hashSecretInternal(""), emptyHash)
	}
}
