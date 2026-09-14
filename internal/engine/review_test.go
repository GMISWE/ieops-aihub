package engine

import "testing"

func TestParseReviewResult(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   ReviewResult
	}{
		{
			name:   "no marker at all returns WARN, never PASS or FAIL",
			output: "Looked at the diff. Everything seems fine, no explicit marker written.",
			want:   ReviewWarn,
		},
		{
			name:   "empty output returns WARN",
			output: "",
			want:   ReviewWarn,
		},
		{
			name:   "single PASS marker",
			output: "Reviewed the change, no issues.\n<!-- REVIEW_RESULT: PASS -->\n",
			want:   ReviewPass,
		},
		{
			name:   "single WARN marker",
			output: "Minor nit, non-blocking.\n<!-- REVIEW_RESULT: WARN -->\n",
			want:   ReviewWarn,
		},
		{
			name:   "single FAIL marker",
			output: "Found a real bug.\n<!-- REVIEW_RESULT: FAIL -->\n",
			want:   ReviewFail,
		},
		{
			name: "multiple markers: LAST one wins, not first",
			output: "Initial pass: <!-- REVIEW_RESULT: FAIL -->\n" +
				"On reflection, that was a false alarm.\n" +
				"<!-- REVIEW_RESULT: PASS -->\n",
			want: ReviewPass,
		},
		{
			name:   "multiple markers, three of them, last wins",
			output: "<!-- REVIEW_RESULT: PASS -->\n<!-- REVIEW_RESULT: WARN -->\n<!-- REVIEW_RESULT: FAIL -->\n",
			want:   ReviewFail,
		},
		{
			name:   "marker with extra internal whitespace still parses",
			output: "<!--   REVIEW_RESULT:    WARN   -->",
			want:   ReviewWarn,
		},
		{
			name:   "text mentioning the words without the marker shape does not count",
			output: "The review result is PASS in spirit but I did not write the marker.",
			want:   ReviewWarn,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseReviewResult(tc.output); got != tc.want {
				t.Errorf("ParseReviewResult(%q) = %q, want %q", tc.output, got, tc.want)
			}
		})
	}
}
