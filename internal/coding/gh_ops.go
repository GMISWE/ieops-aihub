package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// GHCreatePR runs `gh pr create` in the worktree directory.
// Returns the PR URL and number.
func GHCreatePR(ctx context.Context, worktreePath, title, body, head, base string) (map[string]any, error) {
	args := []string{"pr", "create", "--title", title, "--body", body}
	if head != "" {
		args = append(args, "--head", head)
	}
	if base != "" {
		args = append(args, "--base", base)
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = worktreePath

	out, err := cmd.CombinedOutput()
	if err != nil {
		outStr := string(out)
		// `gh pr create` refuses when an open PR already exists for this head.
		// Resolve that PR for real instead of reporting an opaque marker with
		// no url or number: callers use this result to tell the caller of
		// pf_wrap which PR their work was delivered through, and a placeholder
		// there reads as "a PR was created" when none was (aihub#226).
		if strings.Contains(outStr, "already exists") {
			branch := head
			if branch == "" {
				branch, err = GitCurrentBranch(ctx, worktreePath)
				if err != nil {
					return nil, fmt.Errorf("gh pr create reported an existing PR, but the current branch could not be resolved to find it: %w\n%s", err, outStr)
				}
			}
			existing, lookupErr := GHGetPR(ctx, worktreePath, branch)
			if lookupErr != nil {
				return nil, fmt.Errorf("gh pr create reported an existing PR for %s, but looking it up failed: %w\n%s", branch, lookupErr, outStr)
			}
			if existing == nil {
				return nil, fmt.Errorf("gh pr create reported an existing PR for %s, but no PR could be found for that branch:\n%s", branch, outStr)
			}
			return existing, nil
		}
		return nil, fmt.Errorf("gh pr create: %w\n%s", err, outStr)
	}

	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		// gh may output just the URL
		return map[string]any{"url": strings.TrimSpace(string(out))}, nil
	}
	return result, nil
}

// ghGetPRFields are the `gh pr list --json` fields GHGetPR requests.
//
//   - url, number, state: identify the PR and its lifecycle state.
//   - baseRefName: the PR's target branch, used both to decide whether local
//     HEAD is already contained in the base and as the base for a follow-up PR.
//   - commits: the commit oids the PR actually covers. This is what lets a
//     caller tell "this PR already delivered my HEAD" apart from "a PR exists
//     for this branch". `headRefOid` would be a smaller answer to the same
//     question but is not available on older `gh` (2.4.x), whereas `commits`
//     is; see the coverage note on deliveredByPR.
const ghGetPRFields = "url,number,state,baseRefName,commits"

// GHGetPR returns existing PR info for the given head branch, or (nil, nil)
// if no PR exists for that branch. Unlike `gh pr view`, `gh pr list --head
// --state all` still finds a PR after it has been merged and its head branch
// deleted on the remote, so callers can tell "already merged" apart from
// "gh failed to run" instead of a swallowed error masking both as "no PR".
//
// When a branch has several PRs (legitimate once an earlier one is merged or
// closed), an OPEN one is returned in preference to any other, because the OPEN
// PR is the one a push would still flow into. Only if none is open does this
// fall back to the first entry (`gh` lists newest first).
func GHGetPR(ctx context.Context, worktreePath, branch string) (map[string]any, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "list", "--head", branch, "--state", "all", "--json", ghGetPRFields)
	cmd.Dir = worktreePath

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}

	var results []map[string]any
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, fmt.Errorf("parse gh pr list output: %w", err)
	}
	if len(results) == 0 {
		return nil, nil
	}
	for _, pr := range results {
		if state, _ := pr["state"].(string); strings.EqualFold(state, "OPEN") {
			return pr, nil
		}
	}
	return results[0], nil
}

// prCommitOIDs extracts the set of commit oids a PR covers from the `commits`
// field of GHGetPR's result. A PR with no parsable commits yields an empty set,
// which callers must read as "cannot prove this PR covers anything" — never as
// "covers everything".
func prCommitOIDs(pr map[string]any) map[string]bool {
	commits, _ := pr["commits"].([]any)
	oids := make(map[string]bool, len(commits))
	for _, c := range commits {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if oid, ok := m["oid"].(string); ok && oid != "" {
			oids[oid] = true
		}
	}
	return oids
}
