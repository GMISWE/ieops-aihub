package embedding

import "testing"

func TestInputMaxRunes_DefaultsWhenUnset(t *testing.T) {
	t.Setenv("EMBEDDING_INPUT_MAX_RUNES", "")
	if got := InputMaxRunes(); got != DefaultInputMaxRunes {
		t.Fatalf("unset => %d, want %d", got, DefaultInputMaxRunes)
	}
}

func TestInputMaxRunes_HonoursAValidOverride(t *testing.T) {
	t.Setenv("EMBEDDING_INPUT_MAX_RUNES", "12000")
	if got := InputMaxRunes(); got != 12000 {
		t.Fatalf("override => %d, want 12000. The whole point of aihub#361 making this a knob "+
			"is that whoever measures the provider's real context length can raise it without "+
			"a code change.", got)
	}
	// Surrounding whitespace is what a docker-compose env block produces.
	t.Setenv("EMBEDDING_INPUT_MAX_RUNES", "  8192  ")
	if got := InputMaxRunes(); got != 8192 {
		t.Fatalf("padded override => %d, want 8192", got)
	}
}

func TestInputMaxRunes_FallsBackOnGarbage(t *testing.T) {
	for _, raw := range []string{"lots", "6000runes", "6.5"} {
		t.Setenv("EMBEDDING_INPUT_MAX_RUNES", raw)
		if got := InputMaxRunes(); got != DefaultInputMaxRunes {
			t.Errorf("%q => %d, want the default %d. A typo in a quality knob must not fail "+
				"startup (same rule as EMBEDDING_TIMEOUT).", raw, got, DefaultInputMaxRunes)
		}
	}
}

// TestInputMaxRunes_RefusesToDisableTheCap is the one that matters. "0 means no
// bound" is EMBEDDING_TIMEOUT's contract, and copying it here would put the
// aihub#361 defect — an uncapped live writer, storing NULL vectors for rows the
// provider rejects on length and full-text vectors a capped backfill later
// overwrites — one environment variable away from returning, with no code
// change for anyone to review.
func TestInputMaxRunes_RefusesToDisableTheCap(t *testing.T) {
	for _, raw := range []string{"0", "-1", "-6000"} {
		t.Setenv("EMBEDDING_INPUT_MAX_RUNES", raw)
		got := InputMaxRunes()
		if got <= 0 {
			t.Errorf("%q => %d: a non-positive budget means NO TRUNCATION, which is exactly the "+
				"state aihub#361 removed. There must be no config path back to it.", raw, got)
		}
		if got != DefaultInputMaxRunes {
			t.Errorf("%q => %d, want the default %d", raw, got, DefaultInputMaxRunes)
		}
	}
}
