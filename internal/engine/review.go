package engine

import "regexp"

// ReviewResult is a review step's outcome, as recorded by the
// `<!-- REVIEW_RESULT: (PASS|WARN|FAIL) -->` marker convention.
type ReviewResult string

const (
	ReviewPass ReviewResult = "PASS"
	ReviewWarn ReviewResult = "WARN"
	ReviewFail ReviewResult = "FAIL"
)

// reviewResultMarkerRe matches every `<!-- REVIEW_RESULT: (PASS|WARN|FAIL) -->` marker in a
// review step's output, in order of appearance.
var reviewResultMarkerRe = regexp.MustCompile(`<!--\s*REVIEW_RESULT:\s*(PASS|WARN|FAIL)\s*-->`)

// ParseReviewResult returns the LAST `<!-- REVIEW_RESULT: (PASS|WARN|FAIL) -->` marker present
// in output, or ReviewWarn when no marker is present at all — engine.native.md's rule verbatim
// ("no marker -> warn it is missing and return WARN, never an auto-fail"). A missing marker is
// therefore never treated as PASS (which would silently skip a review that produced no
// verdict) and never treated as FAIL (which would fail a step for a formatting omission rather
// than a genuine finding).
func ParseReviewResult(output string) ReviewResult {
	matches := reviewResultMarkerRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return ReviewWarn
	}
	last := matches[len(matches)-1]
	return ReviewResult(last[1])
}
