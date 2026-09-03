package domain

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ConflictSeverity is soft_block or hard_block.
type ConflictSeverity string

const (
	SeveritySoftBlock ConflictSeverity = "soft_block"
	SeverityHardBlock ConflictSeverity = "hard_block"
	SeverityInfo      ConflictSeverity = "info"
)

// ConflictPrediction is a single prediction result.
type ConflictPrediction struct {
	Rule         int              `json:"rule"`
	Severity     ConflictSeverity `json:"severity"`
	Description  string           `json:"description"`
	ResourceType string           `json:"resource_type,omitempty"`
	ResourceKey  string           `json:"resource_key,omitempty"`
	AttemptID    string           `json:"attempt_id,omitempty"`
	ActorDisplay string           `json:"actor_display,omitempty"`
	WIID         string           `json:"work_item_id,omitempty"`
	WISlug       string           `json:"work_item_slug,omitempty"`
}

// PredictConflictsRequest is the body for POST /v1/conflicts/predict.
type PredictConflictsRequest struct {
	WorkItemID *string `json:"work_item_id"`
	// Project namespaces file_scope lock keys (aihub#222). When WorkItemID is set
	// the wi's own project takes precedence; Project is the fallback for a
	// create-preview predict issued before the wi exists. Empty yields a bare key,
	// which only matches legacy pre-migration rows.
	Project           string          `json:"project"`
	DeclaredResources json.RawMessage `json:"declared_resources"`
	DryRun            bool            `json:"dry_run"`
}

// DeclaredResourceItem is a single declared resource entry.
type DeclaredResourceItem struct {
	Type       string `json:"type"`
	URI        string `json:"uri"`
	Intent     string `json:"intent"`
	BaseBranch string `json:"base_branch,omitempty"`
	TaskBranch string `json:"task_branch,omitempty"`
}

// WillUnlockItem describes a blocked wi that will be unblocked by a successful claim.
type WillUnlockItem struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Goal string `json:"goal"`
}

// PredictConflictsResponse is returned by POST /v1/conflicts/predict.
type PredictConflictsResponse struct {
	Severity    ConflictSeverity     `json:"severity"`
	Predictions []ConflictPrediction `json:"predictions"`
	WillUnlock  []WillUnlockItem     `json:"will_unlock"`
}

// PredictConflicts applies the 5 conflict rules and returns predictions.
// Implements §23 of the design doc.
func PredictConflicts(ctx context.Context, pool *pgxpool.Pool, req *PredictConflictsRequest, callerProjectRoles map[string]string) (*PredictConflictsResponse, *AihubError) {
	// aihub#238: validate BEFORE any database access. This is the call pf-work
	// uses as its pre-claim gate, and an unrecognized type used to fall through
	// resourceToLock into `continue`, so the response was
	// {"predictions":[],"severity":"info"} — a fake all-clear that reads exactly
	// like a genuine one. Refusing is the only safe answer for input we cannot
	// map to locks. Ordering matters: the test for this passes a nil pool, so if
	// validation ever moves below the first query the test panics instead of
	// passing.
	if aihubErr := ValidateDeclaredResources(req.DeclaredResources); aihubErr != nil {
		return nil, aihubErr
	}

	var resources []DeclaredResourceItem
	if len(req.DeclaredResources) > 0 && string(req.DeclaredResources) != "null" {
		if err := json.Unmarshal(req.DeclaredResources, &resources); err != nil {
			return nil, NewErr(ErrBadRequest, "failed to parse declared_resources")
		}
	}

	result := &PredictConflictsResponse{
		Severity:    SeverityInfo,
		Predictions: []ConflictPrediction{},
		WillUnlock:  []WillUnlockItem{},
	}

	// Resolve the project whose namespace the declared file_scope resources live
	// in. file_scope lock keys are "<project>:<path>" (aihub#222) so byte-identical
	// relative paths in different projects (e.g. a fork repo and its parent) do not
	// collide. Prefer the wi's own project (authoritative); fall back to req.Project
	// for a create-preview issued before the wi exists.
	effectiveProject := req.Project
	if req.WorkItemID != nil && *req.WorkItemID != "" {
		var p string
		if lookupErr := pool.QueryRow(ctx,
			`SELECT project FROM work_items WHERE id=$1 OR slug=$1`, *req.WorkItemID,
		).Scan(&p); lookupErr == nil && p != "" {
			effectiveProject = p
		}
	}

	// Rule 1: resource_lock conflict (hard_block)
	// Skip if dry_run=true (advisory only)
	//
	// derivedLock, not resourceToLock (aihub#342): rule 1 answers "would taking
	// this resource's lock collide", and a declaration that takes no lock cannot
	// collide with one. Before this, an intent=read path over a held lock got
	// hard_block from rule 1 and info from rule 3 — two rules of one function
	// contradicting each other on a single input, with only dry_run deciding
	// which one the caller saw.
	if !req.DryRun {
		for _, res := range resources {
			lockType, lockKey := derivedLock(res, effectiveProject)
			if lockType == "" {
				continue
			}
			var ownerAttemptID, actorDisplay, wiSlug, wiID string
			err := pool.QueryRow(ctx, `
				SELECT rl.owner_attempt_id, ra.actor_display, wi.slug, wi.id
				FROM resource_locks rl
				JOIN run_attempts ra ON ra.id = rl.owner_attempt_id
				JOIN work_items wi ON wi.id = ra.work_item_id
				WHERE rl.resource_type=$1 AND rl.resource_key=$2 AND ra.status='running'`,
				lockType, lockKey,
			).Scan(&ownerAttemptID, &actorDisplay, &wiSlug, &wiID)
			if err == nil {
				result.Predictions = append(result.Predictions, ConflictPrediction{
					Rule:         1,
					Severity:     SeverityHardBlock,
					Description:  "Resource lock is already held by another attempt",
					ResourceType: lockType,
					ResourceKey:  lockKey,
					AttemptID:    ownerAttemptID,
					ActorDisplay: actorDisplay,
					WISlug:       wiSlug,
					WIID:         wiID,
				})
				result.Severity = SeverityHardBlock
				return result, nil // hard_block: stop processing further rules
			}
		}
	}

	// Rule 2: same git_branch conflict (soft_block)
	for _, res := range resources {
		if res.Type != "repo" {
			continue
		}
		repoName := strings.TrimPrefix(res.URI, "repo:")
		rows, err := pool.Query(ctx, `
			SELECT rl.owner_attempt_id, ra.actor_display, wi.slug, wi.id
			FROM resource_locks rl
			JOIN run_attempts ra ON ra.id = rl.owner_attempt_id
			JOIN work_items wi ON wi.id = ra.work_item_id
			WHERE rl.resource_type='git_branch'
			  AND rl.resource_key LIKE $1 || '/%'
			  AND ra.status='running'`,
			repoName,
		)
		if err == nil {
			for rows.Next() {
				var ownerAttemptID, actorDisplay, wiSlug, wiID string
				if err := rows.Scan(&ownerAttemptID, &actorDisplay, &wiSlug, &wiID); err != nil {
					continue
				}
				result.Predictions = append(result.Predictions, ConflictPrediction{
					Rule:         2,
					Severity:     SeveritySoftBlock,
					Description:  "Another attempt is working on the same repo branch",
					ResourceType: "git_branch",
					AttemptID:    ownerAttemptID,
					ActorDisplay: actorDisplay,
					WISlug:       wiSlug,
					WIID:         wiID,
				})
				if result.Severity != SeverityHardBlock {
					result.Severity = SeveritySoftBlock
				}
			}
			rows.Close()
		}
	}

	// Rule 3: path glob overlap (soft_block or info based on intent)
	for _, res := range resources {
		if res.Type != "path" && res.Type != "document" && res.Type != "section" {
			continue
		}
		uri := res.URI
		lockKey := fileScopeLockKey(effectiveProject, uri)
		rows, err := pool.Query(ctx, `
			SELECT rl.resource_key, ra.actor_display, wi.slug, wi.id
			FROM resource_locks rl
			JOIN run_attempts ra ON ra.id = rl.owner_attempt_id
			JOIN work_items wi ON wi.id = ra.work_item_id
			WHERE rl.resource_type='file_scope'
			  AND ra.status='running'`,
		)
		if err == nil {
			for rows.Next() {
				var existingKey, actorDisplay, wiSlug, wiID string
				if err := rows.Scan(&existingKey, &actorDisplay, &wiSlug, &wiID); err != nil {
					continue
				}
				if globOverlap(lockKey, existingKey) {
					severity := SeveritySoftBlock
					if res.Intent == "read" {
						severity = SeverityInfo
					}
					result.Predictions = append(result.Predictions, ConflictPrediction{
						Rule:         3,
						Severity:     severity,
						Description:  "File path overlaps with another running attempt",
						ResourceType: "file_scope",
						ResourceKey:  existingKey,
						ActorDisplay: actorDisplay,
						WISlug:       wiSlug,
						WIID:         wiID,
					})
					if result.Severity != SeverityHardBlock && severity == SeveritySoftBlock {
						result.Severity = SeveritySoftBlock
					}
				}
			}
			rows.Close()
		}
	}

	// Rule 4: same repo refactor (soft_block)
	for _, res := range resources {
		if res.Type != "repo" || res.Intent != "refactor" {
			continue
		}
		repoName := strings.TrimPrefix(res.URI, "repo:")
		rows, err := pool.Query(ctx, `
			SELECT ra.actor_display, wi.slug, wi.id
			FROM work_items wi
			JOIN run_attempts ra ON ra.id = wi.current_attempt_id
			WHERE wi.status='running'
			  AND wi.declared_resources @> $1::jsonb`,
			`[{"type":"repo","uri":"repo:`+repoName+`","intent":"refactor"}]`,
		)
		if err == nil {
			for rows.Next() {
				var actorDisplay, wiSlug, wiID string
				if err := rows.Scan(&actorDisplay, &wiSlug, &wiID); err != nil {
					continue
				}
				result.Predictions = append(result.Predictions, ConflictPrediction{
					Rule:         4,
					Severity:     SeveritySoftBlock,
					Description:  "Another attempt is refactoring the same repo",
					ResourceType: "repo",
					ResourceKey:  repoName,
					ActorDisplay: actorDisplay,
					WISlug:       wiSlug,
				})
				if result.Severity != SeverityHardBlock {
					result.Severity = SeveritySoftBlock
				}
			}
			rows.Close()
		}
	}

	// Rule 5: external_ref overlap (info)
	for _, res := range resources {
		if res.Type != "external_ref" {
			continue
		}
		rows, err := pool.Query(ctx, `
			SELECT ra.actor_display, wi.slug, wi.id
			FROM work_items wi
			JOIN run_attempts ra ON ra.id = wi.current_attempt_id
			WHERE wi.status='running'
			  AND wi.declared_resources @> $1::jsonb`,
			`[{"type":"external_ref","uri":"`+res.URI+`"}]`,
		)
		if err == nil {
			for rows.Next() {
				var actorDisplay, wiSlug, wiID string
				if err := rows.Scan(&actorDisplay, &wiSlug, &wiID); err != nil {
					continue
				}
				result.Predictions = append(result.Predictions, ConflictPrediction{
					Rule:         5,
					Severity:     SeverityInfo,
					Description:  "Another attempt references the same external resource",
					ResourceType: "external_ref",
					ResourceKey:  res.URI,
					ActorDisplay: actorDisplay,
					WISlug:       wiSlug,
				})
			}
			rows.Close()
		}
	}

	// Compute will_unlock: work items that would be unblocked if this wi completes
	if req.WorkItemID != nil && *req.WorkItemID != "" {
		rows, err := pool.Query(ctx, `
			SELECT DISTINCT wi.id, wi.slug, wi.goal
			FROM wi_dependencies dep
			JOIN work_items wi ON wi.id = dep.blocked_wi_id
			WHERE dep.blocking_wi_id = $1
			  AND dep.kind = 'blocks'
			  AND wi.status = 'blocked'
			  AND NOT EXISTS (
			    SELECT 1 FROM wi_dependencies dep2
			    JOIN work_items blocker ON dep2.blocking_wi_id = blocker.id
			    WHERE dep2.blocked_wi_id = wi.id
			      AND dep2.kind = 'blocks'
			      AND dep2.blocking_wi_id != $1
			      AND blocker.status NOT IN ('wrapped','cancelled','failed')
			  )`,
			*req.WorkItemID,
		)
		if err == nil {
			for rows.Next() {
				var item WillUnlockItem
				if err := rows.Scan(&item.ID, &item.Slug, &item.Goal); err != nil {
					continue
				}
				result.WillUnlock = append(result.WillUnlock, item)
			}
			rows.Close()
		}
	}

	// H7: cross-project folding — redact actor_display/work_item_slug for projects caller can't view
	foldedPredictions := make([]ConflictPrediction, 0, len(result.Predictions))
	for _, p := range result.Predictions {
		if p.WIID != "" {
			// Look up the project of the conflicting wi
			var wiProject string
			pool.QueryRow(ctx, `SELECT project FROM work_items WHERE id=$1`, p.WIID).Scan(&wiProject) //nolint:errcheck
			if wiProject != "" {
				callerRole := callerProjectRoles[wiProject]
				if callerRole == "" && wiProject != "" {
					// No access — redact identifying info
					p.ActorDisplay = ""
					p.WIID = ""
					p.WISlug = ""
					p.AttemptID = ""
					p.Description = "[conflict in project " + wiProject + " — no visibility]"
				}
			}
		}
		foldedPredictions = append(foldedPredictions, p)
	}
	result.Predictions = foldedPredictions

	return result, nil
}

// derivedLock returns the write lock a declared resource takes, or ("", "") if
// it takes none. It is the ONE place the intent rule lives, and every path that
// turns declared_resources into resource_locks rows must go through it:
// FnClaimWorkItem, FnForceTakeover, FnAcquireLocks, and PredictConflicts rule 1.
//
// aihub#342. The rule is not new — it is the contract carried by
// declaredResourcesProp in internal/mcp ("read ... takes no write lock, and path
// overlaps report as info instead of soft_block"), advertised on
// pf_predict_conflicts, pf_update_work_item, pf_create_work_item and
// pf_batch_create_work_items — and it was already implemented, once, inside
// FnAcquireLocks. The other three derivation sites each re-implemented the
// mapping without it, so a work item whose sole declared resource was
// {"type":"path","uri":"file:.gitignore","intent":"read"} had a file_scope write
// lock taken for it at claim, then 409'd somebody else, while
// pf_predict_conflicts — the pre-claim gate — reported the same input as `info`.
// Two tools, one input, opposite answers.
//
// 🔴 The read rule is deliberately scoped to file_scope, NOT applied to every
// lock type. That is a decision, not an oversight, and it is written as a
// condition on lockType rather than on res.Type so that widening it cannot
// happen by accident:
//
//   - Both halves of the contract sentence are about paths, every recorded
//     instance is a path, and pf-plan's guidance only ever teaches intent=read
//     on a `path` entry.
//   - git_branch and deploy_env are NOT per-file exclusions that a reader can
//     harmlessly share. Dropping them for intent=read would let a second
//     attempt take a branch another attempt is on — and because both takeover
//     paths DELETE the prior attempt's locks before re-deriving, an existing
//     branch lock would be silently released on the next takeover rather than
//     merely not taken.
//   - PredictConflicts rule 2 (same-repo git_branch) has no intent check
//     either, so leaving repo alone keeps derivation and prediction in
//     agreement for repo entries. Applying the rule here and not there would
//     recreate, on `repo`, exactly the rule-1-vs-rule-3 contradiction this
//     change exists to remove. Whether intent=read should mean anything at all
//     for repo/service is genuinely undecided; deciding it needs rule 2 changed
//     in the same breath.
//
// Kept as a separate function from resourceToLock, and NOT folded into it. Note
// what that split does and does not buy: rule 3 does not call resourceToLock at
// all (it builds its comparison key straight from fileScopeLockKey), so as of
// today resourceToLock has exactly one caller and folding the check in would be
// behaviour-identical. The split is about which QUESTION each name answers —
// "what key does this map to" versus "does this take a lock" — so that the next
// caller that wants only a key does not silently inherit the lock decision, and
// so this comment has somewhere to live.
func derivedLock(res DeclaredResourceItem, project string) (lockType, lockKey string) {
	lockType, lockKey = resourceToLock(res, project)
	if lockType == "file_scope" && res.Intent == "read" {
		return "", ""
	}
	return lockType, lockKey
}

// resourceToLock converts a DeclaredResourceItem to a (resource_type, resource_key) pair per §25 mapping.
// project namespaces file_scope keys (aihub#222); it is ignored for git_branch/deploy_env.
//
// ⚠️ This mapper answers "which lock KEY does this resource correspond to", not
// "does this resource take a lock". It deliberately ignores Intent. If you are
// about to insert a resource_locks row or check one for a conflict, call
// derivedLock instead (aihub#342).
func resourceToLock(res DeclaredResourceItem, project string) (lockType, lockKey string) {
	switch res.Type {
	case "repo":
		repoName := strings.TrimPrefix(res.URI, "repo:")
		branch := res.TaskBranch
		if branch == "" {
			branch = "main"
		}
		return "git_branch", repoName + "/" + branch
	case "path", "document", "section":
		return "file_scope", fileScopeLockKey(project, res.URI)
	case "service":
		svc := strings.TrimPrefix(res.URI, "service:")
		return "deploy_env", svc
	case "external_ref":
		return "", "" // no lock for external_ref
	}
	return "", ""
}

// fileScopeLockKey builds a file_scope lock key namespaced by the owning wi's
// project: "<project>:<path>". The bare relative path alone is unsafe as a lock
// key — a fork repo whose paths are byte-identical to its parent (a different
// project) would share keys and hard-block the parent even though they are
// physically distinct repositories (aihub#222). Namespacing by project isolates
// them, while two wi's in the SAME project touching the same file still share a
// key and still conflict. Only file_scope keys are namespaced: git_branch keys
// are already repo-qualified (repo/branch), and deploy_env keys (service) are
// intentionally global so cross-project deploys to one environment still conflict.
func fileScopeLockKey(project, uri string) string {
	return project + ":" + fileURIToLockKey(uri)
}

// fileURIToLockKey strips the "file:" scheme, yielding the bare relative path
// embedded inside a file_scope lock key. Callers namespace it via fileScopeLockKey.
func fileURIToLockKey(uri string) string {
	return strings.TrimPrefix(uri, "file:")
}

// globOverlap checks if two glob patterns (or paths) overlap.
// Simple heuristic: prefix match or exact match.
func globOverlap(a, b string) bool {
	if a == b {
		return true
	}
	if strings.HasPrefix(a, b) || strings.HasPrefix(b, a) {
		return true
	}
	// Strip ** glob suffix and check prefix
	aBase := strings.TrimSuffix(a, "/**")
	bBase := strings.TrimSuffix(b, "/**")
	return strings.HasPrefix(aBase, bBase) || strings.HasPrefix(bBase, aBase)
}
