package mcp_test

// aihub#416 D2, the CLIENT half: after a claim's worktrees exist, the tool reads
// `git rev-parse HEAD` in each and records the result on the attempt.
//
// ─── Why this is a separate test file from the domain arm ──────────────────
//
// internal/domain/delocking_db_test.go proves the SERVER stores and returns what
// it is given. What only this side can prove is that anything is given at all:
// the pins come from real git in a real worktree, on a hop the claim makes for
// itself, and nothing in package domain can see whether that hop happens.
//
// ⚠️ THE ORDERING IS THE DESIGN, and the first assertion below is about it. The
// worktrees do not exist when the claim goes out — the tool creates them from the
// claim RESPONSE, and it must, because a claim can be refused (409) and building
// worktrees for a refused claim leaves directories behind for a session that never
// started. So the pin cannot ride on the claim body, and predicting it beforehand
// is precisely what aihub#356 did for branch names and what this work item
// deleted.
//
// MUTANT: delete the `s.client.RecordRepoPins(...)` call from the claim handler —
// the first test goes red on a missing /repo_pins request. Return a fixed string
// from claimRepoPins instead of running git — the sha assertion goes red, because
// it is compared against `git rev-parse HEAD` read out of the worktree the claim
// really made.
//
// No database: the fake aihub answers the HTTP hops.
//
// Run: go test ./internal/mcp/ -run TestClaimRecordsRepoPins -v

import (
	"net/http"
	"strings"
	"testing"
)

// claimAndCapturePins drives a real pf_claim_work_item against the fake aihub in
// a workspace where a worktree can really be created, and returns the tool
// result plus the body POSTed to /repo_pins (nil when no such call was made).
func claimAndCapturePins(t *testing.T, f *fakeAihub, wiID string) (map[string]any, map[string]any) {
	t.Helper()
	result, isErr := callTool(t, f, "pf_claim_work_item", map[string]any{
		"work_item_id":    wiID,
		"idempotency_key": "idem-pins-416",
	})
	if isErr {
		t.Fatalf("pf_claim_work_item failed: %v", result)
	}
	var pinBody map[string]any
	for _, c := range f.recorded() {
		if strings.HasSuffix(c.Path, "/repo_pins") {
			pinBody = c.Body
		}
	}
	return result, pinBody
}

// seedClaimResponse makes the fake answer a claim with the fields the handler
// needs to build a worktree: canonical id, slug (for the seq), project, goal.
func seedClaimResponse(f *fakeAihub, wiID string) {
	f.on("/v1/work_items/"+wiID+"/claim", func(map[string]any) (int, any) {
		return http.StatusOK, map[string]any{
			"attempt_id":             "ra_pins416",
			"claim_epoch":            float64(1),
			"id":                     wiID,
			"slug":                   "aihub#416",
			"project":                "aihub",
			"goal":                   "record repo pins",
			"acquired_locks":         []any{},
			"requires_human_session": false,
			"wi_type":                "feature",
		}
	})
}

func TestClaimRecordsRepoPins(t *testing.T) {
	const wiID = "wi_01JPINS416"

	t.Run("the claim posts the worktree head to repo_pins and echoes it", func(t *testing.T) {
		w := newClaimWorkspace(t)
		_ = w.root
		f := newFakeAihub(t)
		seedClaimResponse(f, wiID)

		result, pinBody := claimAndCapturePins(t, f, wiID)

		if pinBody == nil {
			t.Fatalf("the claim made no /repo_pins request; paths=%v.\n"+
				"Without it an attempt has no server-side record of which commit each repo started "+
				"from, which is the provenance that replaced the git_branch lock — and the absence is "+
				"silent, because the claim itself succeeds either way", f.paths())
		}

		pins, ok := pinBody["repo_pins"].(map[string]any)
		if !ok || len(pins) == 0 {
			t.Fatalf("/repo_pins carried no repo_pins object: %v", pinBody)
		}
		got, ok := pins["aihub"].(string)
		if !ok {
			t.Fatalf("no pin for the repo the workspace declares: %v", pins)
		}

		// Compared against real git in the worktree the claim really made, not
		// against a shape. A handler that invented a plausible sha would satisfy
		// a length check and be wrong about the only thing the value is for.
		wt, _ := result["worktrees"].(map[string]any)
		wtPath, _ := wt["aihub"].(string)
		if wtPath == "" {
			t.Fatalf("the claim reported no worktree for aihub, so there is nothing to have pinned: %v", result)
		}
		want := strings.TrimSpace(wsGit(t, wtPath, "rev-parse", "HEAD"))
		if got != want {
			t.Errorf("pinned %q but the worktree is on %q — the pin names a commit this attempt did not "+
				"start from, which is worse than no pin: it looks like evidence", got, want)
		}
		if len(got) != 40 {
			t.Errorf("pin %q is not a 40-char sha; a truncated one silently fails to match the commit it names", got)
		}

		// Echoed on the response, because the rule that comes with a pin — a
		// conclusion drawn in an unpinned repo must say so — needs the caller to
		// be able to see which repos got one.
		echoed, ok := result["repo_pins"].(map[string]any)
		if !ok {
			t.Fatalf("the claim response carries no repo_pins: %v", sortedAnyKeys(result))
		}
		if echoed["aihub"] != got {
			t.Errorf("the response echoed %v but %q was recorded — a caller reading the response would "+
				"cite provenance the server does not hold", echoed["aihub"], got)
		}
	})

	t.Run("the credentials travel with the pins", func(t *testing.T) {
		// The route is credential-checked server-side, so a call that forgot them
		// would 403 in production while every assertion above stayed green
		// against a fake that answers 200 to anything.
		newClaimWorkspace(t)

		f := newFakeAihub(t)
		seedClaimResponse(f, wiID)

		_, pinBody := claimAndCapturePins(t, f, wiID)
		if pinBody == nil {
			t.Fatal("no /repo_pins request")
		}
		for _, k := range []string{"attempt_id", "claim_epoch", "session_secret"} {
			if v, present := pinBody[k]; !present || v == "" {
				t.Errorf("/repo_pins body carries no %s (%v). FnRecordRepoPins verifies the attempt "+
					"credential, so without it the call is a 403 in production and this hop silently "+
					"never lands", k, sortedAnyKeys(pinBody))
			}
		}
	})

	t.Run("an older server with no repo_pins route is not reported as a problem", func(t *testing.T) {
		// 🔴 THE FORWARD-COMPATIBILITY ARM. This process can be newer than the
		// aihub it talks to, and a server predating aihub#416 answers 404 for a
		// route it does not have. Reporting that in worktree_problems would put a
		// warning on EVERY claim against such a server that the agent can do
		// nothing about — and a warning nobody can act on trains people to skip
		// warnings, which costs more than the one it delivers.
		newClaimWorkspace(t)

		f := newFakeAihub(t)
		seedClaimResponse(f, wiID)
		f.on("/v1/work_items/"+wiID+"/repo_pins", func(map[string]any) (int, any) {
			return http.StatusNotFound, map[string]any{"error": map[string]any{"message": "Not Found"}}
		})

		result, pinBody := claimAndCapturePins(t, f, wiID)
		if pinBody == nil {
			t.Fatal("the claim did not even try to record pins")
		}
		if _, present := result["repo_pins"]; present {
			t.Errorf("the response echoed repo_pins the server refused to store: %v", result["repo_pins"])
		}
		if problems, present := result["worktree_problems"]; present {
			t.Errorf("a 404 from an older server was reported as a worktree problem: %v.\n"+
				"That is 'the capability is absent', not 'the capability failed', and only the second "+
				"is the caller's problem", problems)
		}
	})

	t.Run("a real failure IS reported", func(t *testing.T) {
		// The control for the arm above, and the reason it is narrow. If every
		// error were swallowed, a credential mismatch or a 409 would leave the
		// agent believing it has provenance it does not have — which is the
		// failure the pin exists to prevent, one level up.
		newClaimWorkspace(t)

		f := newFakeAihub(t)
		seedClaimResponse(f, wiID)
		f.on("/v1/work_items/"+wiID+"/repo_pins", func(map[string]any) (int, any) {
			return http.StatusConflict, map[string]any{
				"error": map[string]any{"code": "ATTEMPT_MISMATCH", "message": "invalid session_secret"},
			}
		})

		result, _ := claimAndCapturePins(t, f, wiID)
		problems, present := result["worktree_problems"]
		if !present {
			t.Fatalf("a 409 from /repo_pins was swallowed; the response is %v.\n"+
				"The agent then publishes conclusions citing provenance the server never stored",
				sortedAnyKeys(result))
		}
		if !strings.Contains(strings.Join(anyStrings(problems), " "), "provenance") {
			t.Errorf("the reported problem does not say what was lost: %v", problems)
		}
	})
}

// anyStrings flattens a []any of strings, for asserting on a warning list.
func anyStrings(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
