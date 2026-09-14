package engine

import (
	"reflect"
	"testing"
)

func TestPlanStepBracket(t *testing.T) {
	cases := []struct {
		name string
		in   BracketInput
		want []StepCall
	}{
		{
			name: "failed: exactly one call, no next call even when NextStepID is set",
			in: BracketInput{
				StepID:            "code_review",
				StepAttemptID:     "sa_1",
				Status:            "failed",
				ErrorType:         "review_fail",
				NextStepID:        "commit_and_pr", // must be ignored
				NextStepAttemptID: "sa_2",          // must be ignored
				SupportsNextStep:  true,
			},
			want: []StepCall{
				{
					Tool:          "pf_update_step",
					StepID:        "code_review",
					Status:        "failed",
					StepAttemptID: "sa_1",
					ErrorType:     "review_fail",
				},
			},
		},
		{
			name: "completed, last step (no NextStepID): exactly one call, no next_* fields",
			in: BracketInput{
				StepID:          "commit_and_pr",
				StepAttemptID:   "sa_9",
				Status:          "completed",
				ArtifactSummary: "shipped it",
			},
			want: []StepCall{
				{
					Tool:            "pf_update_step",
					StepID:          "commit_and_pr",
					Status:          "completed",
					StepAttemptID:   "sa_9",
					ArtifactSummary: "shipped it",
				},
			},
		},
		{
			name: "completed with next step, SupportsNextStep=true: one fused call",
			in: BracketInput{
				StepID:            "spec",
				StepAttemptID:     "sa_1",
				Status:            "completed",
				ArtifactSummary:   "wrote the spec",
				NextStepID:        "plan",
				NextStepAttemptID: "sa_2",
				SupportsNextStep:  true,
			},
			want: []StepCall{
				{
					Tool:              "pf_update_step",
					StepID:            "spec",
					Status:            "completed",
					StepAttemptID:     "sa_1",
					ArtifactSummary:   "wrote the spec",
					NextStep:          "plan",
					NextStepAttemptID: "sa_2",
				},
			},
		},
		{
			name: "completed with next step, SupportsNextStep=false: two degraded calls, " +
				"StepAttemptID threaded onto the second call as NextStepAttemptID",
			in: BracketInput{
				StepID:            "spec",
				StepAttemptID:     "sa_1",
				Status:            "completed",
				ArtifactSummary:   "wrote the spec",
				NextStepID:        "plan",
				NextStepAttemptID: "sa_2",
				SupportsNextStep:  false,
			},
			want: []StepCall{
				{
					Tool:            "pf_update_step",
					StepID:          "spec",
					Status:          "completed",
					StepAttemptID:   "sa_1",
					ArtifactSummary: "wrote the spec",
					// no NextStep/NextStepAttemptID: the degraded form's completing call
					// carries none of that, per lifecycle-details.md §1.
				},
				{
					Tool:          "pf_update_step",
					StepID:        "plan",
					Status:        "in_progress",
					StepAttemptID: "sa_2", // THE trap: this must be NextStepAttemptID, not "".
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanStepBracket(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("PlanStepBracket(%+v) =\n  %+v\nwant\n  %+v", tc.in, got, tc.want)
			}
		})
	}
}

// TestPlanStepBracket_DegradedSecondCallNeverEmptyAttemptID pins the single most important
// invariant of the degraded form as its own explicit assertion (not just structural equality
// above): omitting step_attempt_id on the second call leaves current_step_attempt NULL
// server-side (lifecycle-details.md §1), so a regression here would be silent in any test that
// only checks the calls' length or tool names.
func TestPlanStepBracket_DegradedSecondCallNeverEmptyAttemptID(t *testing.T) {
	in := BracketInput{
		StepID:            "spec",
		StepAttemptID:     "sa_1",
		Status:            "completed",
		NextStepID:        "plan",
		NextStepAttemptID: "sa_2",
		SupportsNextStep:  false,
	}
	calls := PlanStepBracket(in)
	if len(calls) != 2 {
		t.Fatalf("len(calls) = %d, want 2", len(calls))
	}
	second := calls[1]
	if second.StepAttemptID == "" {
		t.Fatal("second call's StepAttemptID is empty — this is the exact " +
			"lifecycle-details.md §1 trap: current_step_attempt would be left NULL server-side")
	}
	if second.StepAttemptID != in.NextStepAttemptID {
		t.Errorf("second call's StepAttemptID = %q, want NextStepAttemptID %q",
			second.StepAttemptID, in.NextStepAttemptID)
	}
}
