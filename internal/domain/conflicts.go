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
	Rule        int              `json:"rule"`
	Severity    ConflictSeverity `json:"severity"`
	Description string           `json:"description"`
	ResourceType string          `json:"resource_type,omitempty"`
	ResourceKey  string          `json:"resource_key,omitempty"`
	AttemptID   string           `json:"attempt_id,omitempty"`
	ActorDisplay string          `json:"actor_display,omitempty"`
	WIID        string           `json:"work_item_id,omitempty"`
	WISlug      string           `json:"work_item_slug,omitempty"`
}

// PredictConflictsRequest is the body for POST /v1/conflicts/predict.
type PredictConflictsRequest struct {
	WorkItemID        *string         `json:"work_item_id"`
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

	// Rule 1: resource_lock conflict (hard_block)
	// Skip if dry_run=true (advisory only)
	if !req.DryRun {
		for _, res := range resources {
			lockType, lockKey := resourceToLock(res)
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
		lockKey := fileURIToLockKey(uri)
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

// resourceToLock converts a DeclaredResourceItem to a (resource_type, resource_key) pair per §25 mapping.
func resourceToLock(res DeclaredResourceItem) (lockType, lockKey string) {
	switch res.Type {
	case "repo":
		repoName := strings.TrimPrefix(res.URI, "repo:")
		branch := res.TaskBranch
		if branch == "" {
			branch = "main"
		}
		return "git_branch", repoName + "/" + branch
	case "path", "document", "section":
		return "file_scope", fileURIToLockKey(res.URI)
	case "service":
		svc := strings.TrimPrefix(res.URI, "service:")
		return "deploy_env", svc
	case "external_ref":
		return "", "" // no lock for external_ref
	}
	return "", ""
}

// fileURIToLockKey converts a file: URI to a file_scope lock key.
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
