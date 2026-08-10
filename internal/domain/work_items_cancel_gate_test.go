package domain

// Unit tests for cancelGate (aihub#242): CancelWorkItem's pure decision
// function. These run unconditionally (no AIHUB_TEST_DB needed) since
// cancelGate takes no DB dependency — this is the "actually executes"
// coverage for the fix, unlike the DB-gated test in
// work_items_cancel_dependency_test.go which skips without a live DB.
//
// The core regression this guards: before the fix, CancelWorkItem computed
// canCancel with an inline status check bound to (queued|paused) and reported
// state problems as ErrForbidden. A blocked wi's reporter got 403 even though
// the actual defect was that the wi could never leave status='blocked' via
// any path. cancelGate must (a) let a reporter cancel a blocked wi, and
// (b) never report a state rejection (running/terminal) as ErrForbidden.

import (
	"testing"
)

func TestCancelGate(t *testing.T) {
	const (
		roleAdmin  = "admin"
		roleMember = "member"

		projMaintainer = "maintainer"
		projWriter     = "writer"
	)

	type actor struct {
		name        string
		isReporter  bool
		callerRole  string
		projectRole string
	}

	actors := []actor{
		{"reporter", true, roleMember, projWriter},
		{"maintainer", false, roleMember, projMaintainer},
		{"admin", false, roleAdmin, ""},
		{"unrelated_writer", false, roleMember, projWriter},
	}

	type statusCase struct {
		status      string
		wantCodeFor map[string]ErrCode // actor name -> expected code; missing => expect nil
	}

	cases := []statusCase{
		{
			status: "queued",
			wantCodeFor: map[string]ErrCode{
				"unrelated_writer": ErrForbidden,
			},
		},
		{
			status: "paused",
			wantCodeFor: map[string]ErrCode{
				"unrelated_writer": ErrForbidden,
			},
		},
		{
			// Regression case: reporter + blocked must be allowed (nil), which
			// fails against the pre-fix logic that only allowed queued/paused.
			status: "blocked",
			wantCodeFor: map[string]ErrCode{
				"unrelated_writer": ErrForbidden,
			},
		},
		{
			status: "running",
			wantCodeFor: map[string]ErrCode{
				"reporter":         ErrConflictWIAlreadyClaimed,
				"maintainer":       ErrConflictWIAlreadyClaimed,
				"admin":            ErrConflictWIAlreadyClaimed,
				"unrelated_writer": ErrConflictWIAlreadyClaimed,
			},
		},
		{
			status: "wrapped",
			wantCodeFor: map[string]ErrCode{
				"reporter":         ErrConflictTerminalState,
				"maintainer":       ErrConflictTerminalState,
				"admin":            ErrConflictTerminalState,
				"unrelated_writer": ErrConflictTerminalState,
			},
		},
		{
			status: "failed",
			wantCodeFor: map[string]ErrCode{
				"reporter":         ErrConflictTerminalState,
				"maintainer":       ErrConflictTerminalState,
				"admin":            ErrConflictTerminalState,
				"unrelated_writer": ErrConflictTerminalState,
			},
		},
		{
			status: "cancelled",
			wantCodeFor: map[string]ErrCode{
				"reporter":         ErrConflictTerminalState,
				"maintainer":       ErrConflictTerminalState,
				"admin":            ErrConflictTerminalState,
				"unrelated_writer": ErrConflictTerminalState,
			},
		},
	}

	for _, tc := range cases {
		for _, a := range actors {
			t.Run(tc.status+"/"+a.name, func(t *testing.T) {
				got := cancelGate(tc.status, a.isReporter, a.callerRole, a.projectRole)
				wantCode, wantErr := tc.wantCodeFor[a.name]
				if !wantErr {
					if got != nil {
						t.Fatalf("cancelGate(%q, %+v) = %v; want nil (allowed)", tc.status, a, got)
					}
					return
				}
				if got == nil {
					t.Fatalf("cancelGate(%q, %+v) = nil; want error code %s", tc.status, a, wantCode)
				}
				if got.Code != wantCode {
					t.Fatalf("cancelGate(%q, %+v) code = %s; want %s (message: %s)", tc.status, a, got.Code, wantCode, got.Message)
				}
			})
		}
	}
}
