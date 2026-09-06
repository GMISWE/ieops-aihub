package domain

// aihub#366 — the parts of the commit gate that need no database.
//
// The coverage predicate is the half that decides whether a changed file gets a
// lock at all, and it is decided ENTIRELY by key-form matching. Every way it can
// be wrong is a way the gate quietly stops protecting something:
//
//	too strict -> the gate re-acquires a lock the attempt already holds under an
//	              older key form, or worse, reports a conflict against itself
//	too loose  -> a file is called "already covered" on the strength of a lock
//	              that names a different repo, and is committed unprotected
//
// The second is the dangerous direction and it is not hypothetical: it is
// exactly what an unqualified probe does, which is why the endpoint refuses a
// request with no repo. The subtest that demonstrates it is below, so the
// requirement is a measured fact rather than a comment.
//
// The acquire / conflict / orphan-reclaim half needs Postgres and is not here;
// see the work item for why its CI wiring is blocked.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// coveredBy is the production coverage test, as FnReconcileCommitLocks runs it.
func coveredBy(project, repo, path string, held []string) bool {
	item := DeclaredResourceItem{Type: "path", URI: "file:" + path, Repo: repo, Intent: "write"}
	_, _, probe := derivedLockProbe(item, project)
	return anyMatches(probe, held)
}

func TestCommitGateCoverage_KeyForms(t *testing.T) {
	const project = "aihub"
	const path = "internal/domain/work_items.go"

	cases := []struct {
		name string
		repo string
		held []string
		want bool
		why  string
	}{
		{
			name: "repo-qualified key covers the same path in that repo",
			repo: "aihub",
			held: []string{project + ":aihub:" + path},
			want: true,
			why:  "the ordinary case: the lock this very gate would have taken",
		},
		{
			name: "legacy unqualified key still covers it",
			repo: "aihub",
			held: []string{project + ":" + path},
			want: true,
			why: "every lock written before aihub#261 has this shape, and a gate that " +
				"demanded the qualified form would take a SECOND row for a file the " +
				"attempt already holds — the 'same path, twice, under two names' hazard",
		},
		{
			// 🔴 The dangerous direction. A lock naming another repo protects
			// another repo's file; treating it as coverage commits this one with
			// nothing behind it.
			name: "another repo's key does NOT cover it",
			repo: "aihub",
			held: []string{project + ":some-other-repo:" + path},
			want: false,
			why:  "repo-relative paths collide across repos; that is the whole reason for the repo segment",
		},
		{
			name: "a different file in the same repo does not cover it",
			repo: "aihub",
			held: []string{project + ":aihub:internal/domain/memory.go"},
			want: false,
		},
		{
			name: "a parent directory's key does not cover it",
			repo: "aihub",
			held: []string{project + ":aihub:internal/domain"},
			want: false,
			why: "prefix overlap is lockConflictProbe.Overlaps (PredictConflicts' advisory rule 3), " +
				"not Matches; coverage must use the hard rule or a directory-shaped declaration " +
				"would silently absorb every file under it",
		},
		{
			name: "another project's key does not cover it",
			repo: "aihub",
			held: []string{"some-other-project:aihub:" + path},
			want: false,
		},
		{
			name: "holding nothing covers nothing",
			repo: "aihub",
			held: nil,
			want: false,
			why:  "the aihub#357 shape: declared_resources was an empty array, so claim took zero locks",
		},
		{
			// This subtest is the JUSTIFICATION for rejecting an empty repo, and
			// it asserts the WRONG answer on purpose: with no repo the probe
			// carries a "<project>:%:<path>" LIKE arm, so another repo's lock
			// reads as coverage. Against a competitor that arm is conservative;
			// against the attempt's own locks it inverts into a hole. The 400 in
			// FnReconcileCommitLocks is what keeps this path unreachable, and
			// TestCommitGate_RepoIsRequired below pins it.
			name: "with NO repo, another repo's key wrongly reads as coverage",
			repo: "",
			held: []string{project + ":some-other-repo:" + path},
			want: true,
			why:  "measured, not desired — it is why repo is a required field",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := coveredBy(project, tc.repo, path, tc.held)
			if got != tc.want {
				t.Fatalf("covered=%v want=%v for repo=%q held=%v.%s",
					got, tc.want, tc.repo, tc.held, prefixWhy(tc.why))
			}
		})
	}
}

func prefixWhy(why string) string {
	if why == "" {
		return ""
	}
	return " " + why
}

// TestCommitGate_RepoIsRequired pins the validation, and pins that it happens
// BEFORE any database work: the call is made with a nil pool, so a version that
// opened a transaction first would panic instead of returning the 400.
func TestCommitGate_RepoIsRequired(t *testing.T) {
	resp, aerr := FnReconcileCommitLocks(context.Background(), nil, "wi_whatever",
		&ReconcileCommitLocksRequest{
			AttemptID: "att_x", ClaimEpoch: 1, SessionSecret: "s",
			Paths: []string{"internal/domain/work_items.go"},
		})
	if aerr == nil {
		t.Fatal("an empty repo was accepted; coverage would then be computed with a probe " +
			"that matches every repo's copy of the path")
	}
	if aerr.Code != ErrBadRequest {
		t.Errorf("code = %v, want %v", aerr.Code, ErrBadRequest)
	}
	if resp != nil {
		t.Errorf("resp = %+v, want nil", resp)
	}
}

// TestCommitLockConflictErr_NamesEveryHolder covers the refusal payload.
//
// The owner's decision was "block, and report WHO holds it and WHICH files".
// A 409 that says only "locked" sends the author to pf_read_events to find out
// whose it is — which is the round-trip the decision exists to avoid — so the
// holder identity and the full blocked-path list are asserted as contract.
func TestCommitLockConflictErr_NamesEveryHolder(t *testing.T) {
	aerr := commitLockConflictErr([]commitLockConflict{
		{Path: "a.go", ResourceKey: "p:r:a.go", AttemptID: "att_1", ActorDisplay: "someone", WorkItemSlug: "aihub#1"},
		{Path: "b.go", ResourceKey: "p:r:b.go", AttemptID: "att_2", ActorDisplay: "other", WorkItemSlug: "aihub#2"},
	})

	if aerr.Code != ErrConflictLockTaken {
		t.Fatalf("code = %v, want %v — pf_commit keys its 'refused' wording on this code", aerr.Code, ErrConflictLockTaken)
	}
	for _, want := range []string{"a.go", "b.go", "someone", "aihub#1"} {
		if !strings.Contains(aerr.Message, want) {
			t.Errorf("message %q does not mention %q", aerr.Message, want)
		}
	}

	// The details ride to the caller through the client's formatDetails, so they
	// have to survive a JSON round-trip.
	raw, err := json.Marshal(aerr.Details)
	if err != nil {
		t.Fatalf("marshal details: %v", err)
	}
	var got struct {
		Conflicts []struct {
			Path         string `json:"path"`
			AttemptID    string `json:"attempt_id"`
			ActorDisplay string `json:"actor_display"`
			WorkItemSlug string `json:"work_item_slug"`
		} `json:"conflicts"`
		ConflictWith map[string]any `json:"conflict_with"`
		BlockedPaths []string       `json:"blocked_paths"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal details: %v", err)
	}

	// EVERY conflict, not just the first. Reporting one at a time would send the
	// author round the loop once per file.
	if len(got.Conflicts) != 2 {
		t.Fatalf("conflicts = %d, want 2; a refusal that names one file at a time costs "+
			"the author one re-run per blocked file", len(got.Conflicts))
	}
	if got.Conflicts[1].ActorDisplay != "other" || got.Conflicts[1].WorkItemSlug != "aihub#2" {
		t.Errorf("second conflict lost its holder: %+v", got.Conflicts[1])
	}
	// conflict_with is the shape claim and acquire_locks already publish; a
	// caller keyed on it must not start reading nothing.
	if got.ConflictWith["attempt_id"] != "att_1" {
		t.Errorf("conflict_with = %v, want the first holder in the established shape", got.ConflictWith)
	}
	if len(got.BlockedPaths) != 2 {
		t.Errorf("blocked_paths = %v, want both paths", got.BlockedPaths)
	}
}
