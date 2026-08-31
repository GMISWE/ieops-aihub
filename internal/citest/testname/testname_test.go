package testname

// Probe for aihub#303. Pure unit test, no database.
//
// The load-bearing assertion is TestSanitize_LongNamesSharingA37CharPrefix...:
// it is the one that FAILS against the implementation this package replaced
// (fold, then truncate to 37 with no disambiguator). Measured against that old
// body: all 5 pairs fail there, all 5 pass here.
//
// The other four tests pin the invariants that must survive the fix — charset,
// length bound, byte-for-byte behaviour on short names, determinism.
// ...OutputFitsProjectsNameLimit, ...OutputIsFoldedCharsetOnly and
// ...IsDeterministic pass against the old body as well, so they are guards, not
// probes. ...ShortNamesAreUnchangedFromTheOldImplementation is both: its loop
// and its short spot-checks pass against the old body, but its two
// past-the-boundary spot-checks (the '_' separator at index 30, and the length
// being exactly 37) are properties only the hashed path has, so it goes red
// there too.
//
//	go test ./internal/citest/testname/ -count=1 -v

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// legacySanitize37 is the implementation Sanitize replaced, verbatim: the same
// fold, then a bare truncation to 37 bytes. It is kept here as the reference
// side of two assertions — that short names still come back byte-for-byte
// identical, and that the collision fixture below really is a collision under
// the old rule rather than a pair that happens to differ anyway.
func legacySanitize37(name string) string {
	out := make([]byte, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, byte(r))
		case r >= 'A' && r <= 'Z':
			out = append(out, byte(r-'A'+'a'))
		default:
			out = append(out, '_')
		}
	}
	if len(out) > 37 {
		out = out[:37]
	}
	return string(out)
}

// collidingPairs are real test-function names from this repo, measured during
// aihub#303: each pair folds to the same 37-character string, so under the old
// truncation both members named the SAME project / user / schema row in the
// shared test database.
var collidingPairs = [][2]string{
	{
		"TestValidateDeclaredResources_RejectsAbsentType",
		"TestValidateDeclaredResources_RejectsLockTypeUsedAsDeclaredType",
	},
	{
		"TestBuildListWorkItemsWhere_ReadyOnlyDoesNotConsumeAPlaceholder",
		"TestBuildListWorkItemsWhere_ReadyOnlyReachesSQL",
	},
	{
		"TestArtifactHTML_UI_ExactVersionMarker_DoesNotBypassAuthorization",
		"TestArtifactHTML_UI_ExactVersionMarker_SkipsRedirect",
	},
	// Synthetic worst case: differs only in the very last byte, far past
	// any truncation point.
	{
		"TestSomeVeryLongDescriptiveTestNameThatGoesOnAndOnForever_CaseA",
		"TestSomeVeryLongDescriptiveTestNameThatGoesOnAndOnForever_CaseB",
	},
	// Subtest names: Go joins with '/', which folds to '_'. Two different
	// subtests of one long parent are the shape most likely to occur.
	{
		"TestHandleUpdateStepPersistsEverything/artifact_summary_round_trips",
		"TestHandleUpdateStepPersistsEverything/error_type_round_trips",
	},
}

// TestSanitize_LongNamesSharingA37CharPrefixDoNotCollide is the probe: it fails
// against the pre-aihub#303 truncate-to-37 implementation.
func TestSanitize_LongNamesSharingA37CharPrefixDoNotCollide(t *testing.T) {
	for _, p := range collidingPairs {
		a, b := p[0], p[1]
		require.NotEqual(t, a, b, "fixture bug: pair must be two DIFFERENT names")

		// Anchor: this pair really does collide under the old rule. If a
		// rename ever makes that false, this fails loudly instead of
		// letting the probe below pass for a trivial reason.
		require.Equal(t, legacySanitize37(a), legacySanitize37(b),
			"fixture bug: %q and %q no longer collide under the old truncate-to-37 rule, so they no longer discriminate", a, b)
		require.GreaterOrEqual(t, len(legacySanitize37(a)), 37,
			"fixture bug: shared prefix must be at least 37 folded characters")

		assert.NotEqual(t, Sanitize(a), Sanitize(b),
			"two different test names sharing a 37-character folded prefix must NOT map to the same key\n  %q -> %q\n  %q -> %q",
			a, Sanitize(a), b, Sanitize(b))
	}
}

// sampleNames covers short, exactly-at-the-boundary, long, subtest, non-ASCII
// and empty inputs.
var sampleNames = []string{
	"",
	"T",
	"TestX",
	"TestShortEnough",
	"TestExactlyThirtyCharsLongHere",   // 30 folded chars
	"TestExactlyThirtyOneCharsLongHer", // > 30
	"TestValidateDeclaredResources_RejectsLockTypeUsedAsDeclaredType",
	"TestFoo/sub_test/with slashes and spaces",
	"TestUnicode/日本語のサブテスト名",
	"Test-With.Punctuation!And#Symbols$That%Are^Unsafe&In*SQL",
}

func TestSanitize_OutputFitsProjectsNameLimit(t *testing.T) {
	// projects.name is CHECK (^[a-z][a-z0-9_-]{0,39}$): 40 chars, and every
	// caller prefixes 2 ("p_", "u_", "mem_bfA_", ...).
	for _, n := range append(append([]string{}, sampleNames...), flatten(collidingPairs)...) {
		out := Sanitize(n)
		assert.LessOrEqual(t, len(out), 37, "Sanitize(%q) = %q is %d chars, over the 37-char budget", n, out, len(out))
		assert.LessOrEqual(t, len("p_"+out), 40, `"p_"+Sanitize(%q) does not fit projects.name`, n)
	}
}

func TestSanitize_OutputIsFoldedCharsetOnly(t *testing.T) {
	safe := regexp.MustCompile(`^[a-z0-9_]*$`)
	for _, n := range append(append([]string{}, sampleNames...), flatten(collidingPairs)...) {
		out := Sanitize(n)
		assert.Regexp(t, safe, out, "Sanitize(%q) = %q escapes the [a-z0-9_] charset callers splice into SQL literals", n, out)
	}
}

// TestSanitize_ShortNamesAreUnchangedFromTheOldImplementation pins the
// compatibility half: folding did not change, and names at or under 30 folded
// characters get no hash suffix, so they are byte-for-byte what the two deleted
// copies returned.
func TestSanitize_ShortNamesAreUnchangedFromTheOldImplementation(t *testing.T) {
	for _, n := range sampleNames {
		legacy := legacySanitize37(n)
		if len(legacy) > 30 {
			continue // long path: intentionally differs (hash suffix)
		}
		assert.Equal(t, legacy, Sanitize(n), "short name %q must round-trip byte-for-byte", n)
	}
	// Spot-check the exact strings so a change to the fold itself is
	// visible here and not only via the reference implementation.
	assert.Equal(t, "testfoo_bar", Sanitize("TestFoo/bar"))
	assert.Equal(t, "testshortenough", Sanitize("TestShortEnough"))
	assert.Equal(t, "", Sanitize(""))
	// 30 folded chars: the last length that passes through untouched.
	require.Len(t, "testexactlythirtycharslonghere", 30)
	assert.Equal(t, "testexactlythirtycharslonghere", Sanitize("TestExactlyThirtyCharsLongHere"))
	// 32 folded chars: first sample past the boundary, so it is hashed.
	assert.Equal(t, "testexactlythirtyonecharslongh_", Sanitize("TestExactlyThirtyOneCharsLongHer")[:31],
		"the hashed path must keep the first 30 folded chars plus '_' as its prefix")
	assert.Len(t, Sanitize("TestExactlyThirtyOneCharsLongHer"), 37)
}

func TestSanitize_IsDeterministic(t *testing.T) {
	for _, n := range append(append([]string{}, sampleNames...), flatten(collidingPairs)...) {
		first := Sanitize(n)
		for i := 0; i < 3; i++ {
			assert.Equal(t, first, Sanitize(n), "Sanitize(%q) is not deterministic", n)
		}
	}
}

func flatten(pairs [][2]string) []string {
	out := make([]string, 0, len(pairs)*2)
	for _, p := range pairs {
		out = append(out, p[0], p[1])
	}
	return out
}
