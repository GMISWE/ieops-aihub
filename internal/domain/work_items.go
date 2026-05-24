package domain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WorkItem mirrors the work_items table row.
type WorkItem struct {
	ID                   string          `json:"id"`
	Seq                  int64           `json:"seq"`
	Slug                 string          `json:"slug"`
	Project              string          `json:"project"`
	Scenario             string          `json:"scenario"`
	Goal                 string          `json:"goal"`
	Source               string          `json:"source"`
	WIType               *string         `json:"wi_type"`
	Priority             string          `json:"priority"`
	RequiresHumanSession *bool           `json:"requires_human_session"`
	Milestone            *string         `json:"milestone"`
	Labels               []string        `json:"labels"`
	Status               string          `json:"status"`
	DeclaredResources    json.RawMessage `json:"declared_resources"`
	ResourcesVersion     int             `json:"resources_version"`
	ExternalShareType    *string         `json:"external_share_type"`
	ExternalShareKey     *string         `json:"external_share_key"`
	ReporterUserID       string          `json:"reporter_user_id"`
	ReporterDisplay      string          `json:"reporter_display"`
	CurrentAttemptID     *string         `json:"current_attempt_id"`
	CurrentAttemptEpoch  int64           `json:"current_attempt_epoch"`
	ParentWorkItemID     *string         `json:"parent_work_item_id"`
	Attrs                json.RawMessage `json:"attrs"`
	Content              *string         `json:"content"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	ClosedAt             *time.Time      `json:"closed_at"`
}

// CreateWorkItemRequest is the parsed body for POST /v1/work_items.
type CreateWorkItemRequest struct {
	Project              string          `json:"project"`
	Goal                 string          `json:"goal"`
	Scenario             string          `json:"scenario"`
	Priority             string          `json:"priority"`
	WIType               *string         `json:"wi_type"`
	RequiresHumanSession *bool           `json:"requires_human_session"`
	Milestone            *string         `json:"milestone"`
	Labels               []string        `json:"labels"`
	DeclaredResources    json.RawMessage `json:"declared_resources"`
	ParentWorkItemID     *string         `json:"parent_work_item_id"`
	BlockedBy            []string        `json:"blocked_by"`
	Source               string          `json:"source"`
	Attrs                json.RawMessage `json:"attrs"`
	Content              *string         `json:"content"`
	ForceCreate          bool            `json:"force_create"`
	ForceReason          string          `json:"force_reason"`
}

// UpdateWorkItemRequest is the parsed body for PATCH /v1/work_items/:id.
type UpdateWorkItemRequest struct {
	Priority             *string         `json:"priority"`
	Milestone            *string         `json:"milestone"`
	WIType               *string         `json:"wi_type"`
	RequiresHumanSession *bool           `json:"requires_human_session"`
	ReclassifyReason     *string         `json:"reclassify_reason"`
	Labels               []string        `json:"labels"`
	DeclaredResources    json.RawMessage `json:"declared_resources"`
	ResourcesVersion     *int            `json:"resources_version"`
	Attrs                json.RawMessage `json:"attrs"`
	Goal                 *string         `json:"goal"`
	GoalChangeReason     *string         `json:"goal_change_reason"`
	Content              *string         `json:"content"`
}

// ReadyQueue is the six-segment LCRS response for GET /v1/work_items/ready.
type ReadyQueue struct {
	Items              []ReadyItem    `json:"items"`
	Running            []RunningItem  `json:"running"`
	Stalled            []StalledItem  `json:"stalled"`
	Paused             []PausedItem   `json:"paused"`
	NeedsHumanSession  []ReadyItem    `json:"needs_human_session"`
	Unclassified       []ReadyItem    `json:"unclassified"`
	StaleRunning       []RunningItem  `json:"stale_running,omitempty"`
}

// ReadyItem is a work item in the items/needs_human_session/unclassified segments.
type ReadyItem struct {
	ID          string  `json:"id"`
	Slug        string  `json:"slug"`
	WIType      *string `json:"wi_type"`
	Priority    string  `json:"priority"`
	Goal        string  `json:"goal"`
	UnblockedAt *string `json:"unblocked_at,omitempty"`
	CreatedAt   string  `json:"created_at,omitempty"`
}

// RunningItem is a work item in the running segment.
type RunningItem struct {
	ID           string  `json:"id"`
	Slug         string  `json:"slug"`
	Goal         string  `json:"goal"`
	OwnerDisplay string  `json:"owner_display"`
	LastActiveAt string  `json:"last_active_at"`
}

// StalledItem is a work item in the stalled segment.
type StalledItem struct {
	ID              string `json:"id"`
	Slug            string `json:"slug"`
	StallReason     string `json:"stall_reason"`
	StalledSince    string `json:"stalled_since"`
	LastActorDisplay string `json:"last_actor_display"`
}

// PausedItem is a work item in the paused segment.
type PausedItem struct {
	ID              string `json:"id"`
	Slug            string `json:"slug"`
	PausedSince     string `json:"paused_since"`
	LastActorDisplay string `json:"last_actor_display"`
}

// newWorkItemID generates a new wi_ prefixed ID.
func newWorkItemID() string {
	return NewID("wi")
}

// CreateWorkItem inserts a new work item atomically.
// Applies classification_rules from scenario_phase_configs, runs dedup, and
// inserts wi_dependencies for blocked_by entries.
func CreateWorkItem(ctx context.Context, pool *pgxpool.Pool, req *CreateWorkItemRequest, callerUserID, callerDisplay string) (*WorkItem, *AihubError) {
	// Validate goal
	if req.Goal == "" {
		return nil, NewErr(ErrBadRequest, "goal is required")
	}
	if utf8.RuneCountInString(req.Goal) > 500 {
		return nil, NewErr(ErrBadRequest, "goal exceeds 500 characters")
	}
	if strings.ContainsAny(req.Goal, "\n\r") {
		return nil, NewErr(ErrGoalMultiline, "goal must not contain newlines")
	}
	if req.Project == "" {
		return nil, NewErr(ErrBadRequest, "project is required")
	}

	// Defaults
	if req.Scenario == "" {
		req.Scenario = "coding"
	}
	if req.Priority == "" {
		req.Priority = "normal"
	}
	if req.Source == "" {
		req.Source = "human"
	}
	if req.Labels == nil {
		req.Labels = []string{}
	}
	if len(req.DeclaredResources) == 0 {
		req.DeclaredResources = json.RawMessage("[]")
	}
	if len(req.Attrs) == 0 {
		req.Attrs = json.RawMessage("{}")
	}

	// Reject unimplemented scenarios
	if req.Scenario != "coding" {
		return nil, NewErr(ErrNotImplemented, fmt.Sprintf("scenario %q is not yet implemented", req.Scenario))
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, NewErr(ErrInternalError, "failed to begin transaction")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Load scenario_phase_configs for classification rules
	var configRaw []byte
	err = tx.QueryRow(ctx,
		`SELECT content FROM scenario_phase_configs WHERE scenario = $1`,
		req.Scenario,
	).Scan(&configRaw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, NewErr(ErrServiceUnavailable, fmt.Sprintf("no phase config for scenario %q — server not fully initialized", req.Scenario))
		}
		return nil, NewErr(ErrInternalError, "failed to load scenario config")
	}

	// Apply classification rules
	wiType, requiresHumanSession, aihubErr := applyClassificationRules(configRaw, req)
	if aihubErr != nil {
		return nil, aihubErr
	}

	// Dedup check (skip if force_create)
	if !req.ForceCreate {
		aihubErr = checkDedup(ctx, tx, req)
		if aihubErr != nil {
			return nil, aihubErr
		}
	} else if req.ForceReason == "" || len(req.ForceReason) < 10 {
		return nil, NewErr(ErrBadRequest, "force_reason is required and must be at least 10 characters when force_create=true")
	}

	// Get next seq from projects table (UPDATE must be last write in tx to minimize row lock duration)
	// This is deferred to after the INSERT; we do it here to fail fast on FK violation.
	var seq int64
	err = tx.QueryRow(ctx,
		`UPDATE projects SET wi_seq = wi_seq + 1 WHERE name = $1 RETURNING wi_seq`,
		req.Project,
	).Scan(&seq)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return nil, NewErr(ErrProjectNotFound, fmt.Sprintf("project %q not found", req.Project))
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, NewErr(ErrProjectNotFound, fmt.Sprintf("project %q not found", req.Project))
		}
		return nil, NewErr(ErrInternalError, fmt.Sprintf("increment wi_seq: %v", err))
	}

	wiID := newWorkItemID()

	_, err = tx.Exec(ctx, `
		INSERT INTO work_items (
			id, seq, project, scenario, goal, source, wi_type, priority,
			requires_human_session, milestone, labels, status,
			declared_resources, reporter_user_id, reporter_display,
			parent_work_item_id, attrs, content
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, 'queued',
			$12, $13, $14,
			$15, $16, $17
		)`,
		wiID, seq, req.Project, req.Scenario, req.Goal, req.Source, wiType, req.Priority,
		requiresHumanSession, req.Milestone, req.Labels, req.DeclaredResources,
		callerUserID, callerDisplay, req.ParentWorkItemID, req.Attrs, req.Content,
	)
	if err != nil {
		return nil, NewErr(ErrInternalError, fmt.Sprintf("failed to insert work_item: %v", err))
	}

	// Emit work_item_filed event
	evtID := NewID("evt")
	evtPayload, _ := json.Marshal(map[string]any{
		"source":       req.Source,
		"project":      req.Project,
		"work_item_id": wiID,
		"goal":         req.Goal,
	})
	_, err = tx.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
		VALUES ($1, $2, $3, $4, 'work_item_filed', $5, $6)`,
		evtID, wiID, callerUserID, callerDisplay, evtPayload, req.Project,
	)
	if err != nil {
		return nil, NewErr(ErrInternalError, "failed to emit work_item_filed event")
	}

	// Insert blocked_by dependencies
	for _, blockingID := range req.BlockedBy {
		_, err = tx.Exec(ctx, `
			INSERT INTO wi_dependencies (blocked_wi_id, blocking_wi_id, kind, created_by)
			VALUES ($1, $2, 'blocks', $3)`,
			wiID, blockingID, callerUserID,
		)
		if err != nil {
			return nil, NewErr(ErrInternalError, fmt.Sprintf("failed to create dependency for blocking_wi %s: %v", blockingID, err))
		}
	}

	// If blocked_by is non-empty, set status to blocked
	if len(req.BlockedBy) > 0 {
		_, err = tx.Exec(ctx, `UPDATE work_items SET status='blocked' WHERE id=$1`, wiID)
		if err != nil {
			return nil, NewErr(ErrInternalError, "failed to set blocked status")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, NewErr(ErrInternalError, "failed to commit transaction")
	}

	return GetWorkItem(ctx, pool, wiID)
}

// applyClassificationRules applies scenario classification_rules to determine wi_type and requires_human_session.
// Returns (wi_type, requires_human_session, err).
func applyClassificationRules(configRaw []byte, req *CreateWorkItemRequest) (*string, *bool, *AihubError) {
	var config struct {
		WITypes map[string]struct {
			RequiresHumanSession bool `json:"requires_human_session"`
		} `json:"wi_types"`
		ClassificationRules []struct {
			Name         string `json:"name"`
			Priority     string `json:"priority"`      // flat field, not under "match"
			WITypePrefix string `json:"wi_type_prefix"` // flat field, not under "match"
			Set struct {
				WIType               string `json:"wi_type"`
				RequiresHumanSession *bool  `json:"requires_human_session"`
			} `json:"set"`
		} `json:"classification_rules"`
	}
	if err := json.Unmarshal(configRaw, &config); err != nil {
		return nil, nil, NewErr(ErrInternalError, "failed to parse scenario config")
	}

	wiType := req.WIType
	requiresHumanSession := req.RequiresHumanSession

	// Apply classification rules (first match wins)
	for _, rule := range config.ClassificationRules {
		matchesPriority := rule.Priority == "" || rule.Priority == req.Priority
		matchesPrefix := rule.WITypePrefix == "" ||
			(req.WIType != nil && strings.HasPrefix(*req.WIType, rule.WITypePrefix)) ||
			(req.WIType == nil && strings.HasPrefix(strings.ToLower(req.Goal), strings.ToLower(rule.WITypePrefix)))
		if matchesPriority && matchesPrefix {
			if rule.Set.WIType != "" {
				t := rule.Set.WIType
				wiType = &t
			}
			if rule.Set.RequiresHumanSession != nil {
				b := *rule.Set.RequiresHumanSession
				requiresHumanSession = &b
			}
			break
		}
	}

	// C-R9-4: Validate wi_type exists in config if set
	if wiType != nil && *wiType != "" {
		if _, ok := config.WITypes[*wiType]; !ok {
			available := make([]string, 0, len(config.WITypes))
			for k := range config.WITypes {
				available = append(available, k)
			}
			return nil, nil, NewErrDetails(ErrWITypeMismatch,
				fmt.Sprintf("wi_type %q does not exist in scenario config", *wiType),
				map[string]any{"wi_type": *wiType, "available_wi_types": available},
			)
		}
		// If requires_human_session not set, derive from phase config
		if requiresHumanSession == nil {
			rhs := config.WITypes[*wiType].RequiresHumanSession
			requiresHumanSession = &rhs
		}
	}

	return wiType, requiresHumanSession, nil
}

// jaccardNGram computes a simple n-gram Jaccard similarity between two strings.
func jaccardNGram(a, b string, n int) float64 {
	setA := ngrams(a, n)
	setB := ngrams(b, n)
	if len(setA) == 0 && len(setB) == 0 {
		return 1.0
	}
	intersection := 0
	for g := range setA {
		if setB[g] {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func ngrams(s string, n int) map[string]bool {
	s = strings.ToLower(s)
	out := make(map[string]bool)
	runes := []rune(s)
	for i := 0; i+n <= len(runes); i++ {
		out[string(runes[i:i+n])] = true
	}
	return out
}

func setOverlap(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1.0
	}
	setA := make(map[string]bool, len(a))
	for _, v := range a {
		setA[v] = true
	}
	intersection := 0
	for _, v := range b {
		if setA[v] {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// checkDedup performs the F3 dedup check within a transaction.
func checkDedup(ctx context.Context, tx pgx.Tx, req *CreateWorkItemRequest) *AihubError {
	// Pass req.Labels as []string so pgx serializes it as a proper PostgreSQL text[]
	// (not JSON "[]" which cannot be cast with ::text[]).
	labels := req.Labels
	if labels == nil {
		labels = []string{}
	}

	// When labels is empty we rely only on goal similarity and resource overlap.
	// Don't use labels && $2 when $2 is empty — that would give a type-cast error.
	// When declared_resources is empty [], @> $3::jsonb is trivially true for every row,
	// so we guard with a non-empty check.
	var rows pgx.Rows
	var err error
	if len(labels) == 0 {
		// No labels: only filter by goal similarity (done in Go) + resource overlap (if any)
		rows, err = tx.Query(ctx, `
			SELECT id, slug, goal, labels, declared_resources
			FROM work_items
			WHERE project = $1
			  AND status IN ('queued','running','paused','blocked')
			LIMIT 50`,
			req.Project,
		)
	} else {
		rows, err = tx.Query(ctx, `
			SELECT id, slug, goal, labels, declared_resources
			FROM work_items
			WHERE project = $1
			  AND status IN ('queued','running','paused','blocked')
			  AND (labels && $2::text[] OR declared_resources @> $3::jsonb)
			LIMIT 50`,
			req.Project, labels, req.DeclaredResources,
		)
	}
	if err != nil {
		return nil // Dedup is best-effort; if query fails, allow creation
	}
	defer rows.Close()

	type candidate struct {
		ID         string
		Slug       string
		Goal       string
		Labels     []string
		Resources  json.RawMessage
		Similarity float64
	}

	var partials []candidate
	for rows.Next() {
		var c candidate
		var labelsRaw []string
		if scanErr := rows.Scan(&c.ID, &c.Slug, &c.Goal, &labelsRaw, &c.Resources); scanErr != nil {
			continue
		}
		c.Labels = labelsRaw

		sim := jaccardNGram(req.Goal, c.Goal, 3)
		labelSim := setOverlap(req.Labels, c.Labels)
		// C7: include resource overlap (resSim) per design §11 formula.
		// JSON errors here just degrade dedup to label+goal scoring — they
		// must not abort the create-work-item flow.
		var reqRes, cRes []string
		if req.DeclaredResources != nil {
			_ = json.Unmarshal(req.DeclaredResources, &reqRes)
		}
		if c.Resources != nil {
			_ = json.Unmarshal(c.Resources, &cRes)
		}
		resSim := setOverlap(reqRes, cRes)
		score := 0.6*sim + 0.2*labelSim + 0.2*resSim

		if score >= 0.90 {
			return NewErrDetails(ErrConflictDuplicate,
				fmt.Sprintf("work item %q is %.0f%% similar to existing %s", req.Goal, score*100, c.Slug),
				map[string]any{"existing": map[string]any{
					"id": c.ID, "slug": c.Slug, "goal": c.Goal, "status": "active",
				}},
			)
		}
		if score >= 0.65 {
			c.Similarity = score
			partials = append(partials, c)
		}
	}
	rows.Close()

	if len(partials) > 0 {
		candidates := make([]map[string]any, len(partials))
		for i, p := range partials {
			candidates[i] = map[string]any{
				"id": p.ID, "slug": p.Slug, "goal": p.Goal, "similarity": p.Similarity,
			}
		}
		return NewErrDetails(ErrConflictCandidates, "similar work items found", map[string]any{"candidates": candidates})
	}
	return nil
}

// GetWorkItem fetches a work item by ID or slug.
func GetWorkItem(ctx context.Context, pool *pgxpool.Pool, idOrSlug string) (*WorkItem, *AihubError) {
	var wi WorkItem
	var labelsRaw []string

	var q string
	var arg string
	if strings.HasPrefix(idOrSlug, "wi_") {
		q = `SELECT id, seq, slug, project, scenario, goal, source, wi_type, priority,
			       requires_human_session, milestone, labels, status,
			       declared_resources, resources_version, external_share_type, external_share_key,
			       reporter_user_id, reporter_display, current_attempt_id, current_attempt_epoch,
			       parent_work_item_id, attrs, content, created_at, updated_at, closed_at
			FROM work_items WHERE id = $1`
		arg = idOrSlug
	} else {
		q = `SELECT id, seq, slug, project, scenario, goal, source, wi_type, priority,
			       requires_human_session, milestone, labels, status,
			       declared_resources, resources_version, external_share_type, external_share_key,
			       reporter_user_id, reporter_display, current_attempt_id, current_attempt_epoch,
			       parent_work_item_id, attrs, content, created_at, updated_at, closed_at
			FROM work_items WHERE slug = $1`
		arg = idOrSlug
	}

	err := pool.QueryRow(ctx, q, arg).Scan(
		&wi.ID, &wi.Seq, &wi.Slug, &wi.Project, &wi.Scenario, &wi.Goal, &wi.Source,
		&wi.WIType, &wi.Priority, &wi.RequiresHumanSession, &wi.Milestone, &labelsRaw,
		&wi.Status, &wi.DeclaredResources, &wi.ResourcesVersion,
		&wi.ExternalShareType, &wi.ExternalShareKey,
		&wi.ReporterUserID, &wi.ReporterDisplay,
		&wi.CurrentAttemptID, &wi.CurrentAttemptEpoch,
		&wi.ParentWorkItemID, &wi.Attrs, &wi.Content, &wi.CreatedAt, &wi.UpdatedAt, &wi.ClosedAt,
	)
	if err != nil {
		return nil, pgxErr(err,
			fmt.Sprintf("work item %q not found", idOrSlug),
			"failed to get work item")
	}
	wi.Labels = labelsRaw
	if wi.Labels == nil {
		wi.Labels = []string{}
	}
	return &wi, nil
}

// ListWorkItemsFilter holds optional filters for ListWorkItems.
type ListWorkItemsFilter struct {
	Status    []string
	WIType    *string
	Priority  *string
	Milestone *string
	Label     *string
	UserID    *string
	Source    *string
	ReadyOnly bool
	IDs       []string
	Since     *time.Time
	Limit     int
	Cursor    *string
}

// ListWorkItemsResult holds paginated results.
type ListWorkItemsResult struct {
	Items      []*WorkItem `json:"items"`
	NextCursor *string     `json:"next_cursor"`
}

// ListWorkItems returns a paginated list of work items for a project.
func ListWorkItems(ctx context.Context, pool *pgxpool.Pool, project string, f ListWorkItemsFilter) (*ListWorkItemsResult, *AihubError) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}

	args := []any{project}
	conds := []string{"wi.project = $1"}
	argIdx := 2

	if len(f.Status) > 0 {
		conds = append(conds, fmt.Sprintf("wi.status = ANY($%d)", argIdx))
		args = append(args, f.Status)
		argIdx++
	}
	if f.WIType != nil {
		conds = append(conds, fmt.Sprintf("wi.wi_type = $%d", argIdx))
		args = append(args, *f.WIType)
		argIdx++
	}
	if f.Priority != nil {
		conds = append(conds, fmt.Sprintf("wi.priority = $%d", argIdx))
		args = append(args, *f.Priority)
		argIdx++
	}
	if f.Label != nil {
		conds = append(conds, fmt.Sprintf("$%d = ANY(wi.labels)", argIdx))
		args = append(args, *f.Label)
		argIdx++
	}
	if f.UserID != nil {
		conds = append(conds, fmt.Sprintf("wi.reporter_user_id = $%d", argIdx))
		args = append(args, *f.UserID)
		argIdx++
	}
	if len(f.IDs) > 0 {
		conds = append(conds, fmt.Sprintf("wi.id = ANY($%d)", argIdx))
		args = append(args, f.IDs)
		argIdx++
	}
	if f.Since != nil {
		conds = append(conds, fmt.Sprintf("wi.created_at >= $%d", argIdx))
		args = append(args, *f.Since)
		// argIdx not incremented: last optional clause, kept symmetric for future filters
	}

	where := "WHERE " + strings.Join(conds, " AND ")
	query := fmt.Sprintf(`
		SELECT wi.id, wi.seq, wi.slug, wi.project, wi.scenario, wi.goal, wi.source,
			   wi.wi_type, wi.priority, wi.requires_human_session, wi.milestone, wi.labels,
			   wi.status, wi.declared_resources, wi.resources_version,
			   wi.external_share_type, wi.external_share_key,
			   wi.reporter_user_id, wi.reporter_display,
			   wi.current_attempt_id, wi.current_attempt_epoch,
			   wi.parent_work_item_id, wi.attrs, wi.created_at, wi.updated_at, wi.closed_at
		FROM work_items wi
		%s
		ORDER BY wi.created_at DESC
		LIMIT %d`, where, f.Limit+1)

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, NewErr(ErrInternalError, fmt.Sprintf("failed to list work_items: %v", err))
	}
	defer rows.Close()

	var items []*WorkItem
	for rows.Next() {
		var wi WorkItem
		var labelsRaw []string
		if scanErr := rows.Scan(
			&wi.ID, &wi.Seq, &wi.Slug, &wi.Project, &wi.Scenario, &wi.Goal, &wi.Source,
			&wi.WIType, &wi.Priority, &wi.RequiresHumanSession, &wi.Milestone, &labelsRaw,
			&wi.Status, &wi.DeclaredResources, &wi.ResourcesVersion,
			&wi.ExternalShareType, &wi.ExternalShareKey,
			&wi.ReporterUserID, &wi.ReporterDisplay,
			&wi.CurrentAttemptID, &wi.CurrentAttemptEpoch,
			&wi.ParentWorkItemID, &wi.Attrs, &wi.CreatedAt, &wi.UpdatedAt, &wi.ClosedAt,
		); scanErr != nil {
			return nil, NewErr(ErrInternalError, fmt.Sprintf("scan error: %v", scanErr))
		}
		wi.Labels = labelsRaw
		if wi.Labels == nil {
			wi.Labels = []string{}
		}
		items = append(items, &wi)
	}

	result := &ListWorkItemsResult{}
	if len(items) > f.Limit {
		items = items[:f.Limit]
		nextCursor := items[len(items)-1].CreatedAt.Format(time.RFC3339Nano)
		result.NextCursor = &nextCursor
	}
	result.Items = items
	if result.Items == nil {
		result.Items = []*WorkItem{}
	}
	return result, nil
}

// UpdateWorkItem applies a patch to a work item.
func UpdateWorkItem(ctx context.Context, pool *pgxpool.Pool, idOrSlug string, callerUserID, callerRole string, callerProjectRoles map[string]string, req *UpdateWorkItemRequest) (*WorkItem, *AihubError) {
	wi, aihubErr := GetWorkItem(ctx, pool, idOrSlug)
	if aihubErr != nil {
		return nil, aihubErr
	}

	// Permission checks for goal change
	if req.Goal != nil {
		isReporter := wi.ReporterUserID == callerUserID
		projectRole := callerProjectRoles[wi.Project]
		canChange := isReporter || projectRole == "maintainer" || callerRole == "admin"
		if !canChange {
			return nil, NewErr(ErrGoalChangeNotAllowed, "only reporter or maintainer can update goal")
		}
		if wi.Status == "running" {
			return nil, NewErr(ErrGoalChangeNotAllowed, "cannot update goal while work item is running; pause first")
		}
		if wi.Status != "queued" && wi.Status != "paused" {
			return nil, NewErr(ErrGoalChangeNotAllowed, "goal can only be updated when status is queued or paused")
		}
		if req.GoalChangeReason == nil || len(*req.GoalChangeReason) < 10 {
			return nil, NewErr(ErrBadRequest, "goal_change_reason is required (min 10 chars) when updating goal")
		}
		if strings.ContainsAny(*req.Goal, "\n\r") {
			return nil, NewErr(ErrGoalMultiline, "goal must not contain newlines")
		}
	}

	// Permission check for wi_type reclassification
	if req.WIType != nil {
		isReporter := wi.ReporterUserID == callerUserID
		projectRole := callerProjectRoles[wi.Project]
		canReclassify := isReporter || projectRole == "maintainer" || callerRole == "admin"
		if !canReclassify {
			return nil, NewErr(ErrWIReclassifyForbidden, "only reporter, maintainer, or admin can reclassify wi_type")
		}
		if wi.Status != "queued" && wi.Status != "paused" {
			return nil, NewErr(ErrWIReclassifyForbidden, "wi_type can only be updated when status is queued or paused")
		}
		if req.ReclassifyReason == nil || len(*req.ReclassifyReason) < 10 {
			return nil, NewErr(ErrBadRequest, "reclassify_reason is required (min 10 chars) when updating wi_type")
		}

		// Fix 1: validate new wi_type exists in scenario_phase_configs.
		// Fix 2: auto-infer requires_human_session from phase config when not provided.
		var configRaw []byte
		cfgErr := pool.QueryRow(ctx,
			`SELECT content FROM scenario_phase_configs WHERE scenario = $1`, wi.Scenario,
		).Scan(&configRaw)
		if cfgErr != nil {
			if errors.Is(cfgErr, pgx.ErrNoRows) {
				return nil, NewErr(ErrServiceUnavailable, fmt.Sprintf("no phase config for scenario %q — server not fully initialized", wi.Scenario))
			}
			return nil, NewErr(ErrInternalError, "failed to load scenario config")
		}
		var phaseCfg struct {
			WITypes map[string]struct {
				RequiresHumanSession bool `json:"requires_human_session"`
			} `json:"wi_types"`
		}
		if jsonErr := json.Unmarshal(configRaw, &phaseCfg); jsonErr != nil {
			return nil, NewErr(ErrInternalError, "failed to parse scenario config")
		}
		if _, ok := phaseCfg.WITypes[*req.WIType]; !ok {
			available := make([]string, 0, len(phaseCfg.WITypes))
			for k := range phaseCfg.WITypes {
				available = append(available, k)
			}
			return nil, NewErrDetails(ErrWITypeMismatch,
				fmt.Sprintf("wi_type %q does not exist in scenario config", *req.WIType),
				map[string]any{"wi_type": *req.WIType, "available_wi_types": available},
			)
		}
		if req.RequiresHumanSession == nil {
			rhs := phaseCfg.WITypes[*req.WIType].RequiresHumanSession
			req.RequiresHumanSession = &rhs
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, NewErr(ErrInternalError, "failed to begin transaction")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	setClauses := []string{"updated_at = clock_timestamp()"}
	args := []any{}
	argIdx := 1

	if req.Priority != nil {
		setClauses = append(setClauses, fmt.Sprintf("priority = $%d", argIdx))
		args = append(args, *req.Priority)
		argIdx++
	}
	if req.Milestone != nil {
		setClauses = append(setClauses, fmt.Sprintf("milestone = $%d", argIdx))
		args = append(args, *req.Milestone)
		argIdx++
	}
	if req.WIType != nil {
		setClauses = append(setClauses, fmt.Sprintf("wi_type = $%d", argIdx))
		args = append(args, *req.WIType)
		argIdx++
	}
	if req.RequiresHumanSession != nil {
		setClauses = append(setClauses, fmt.Sprintf("requires_human_session = $%d", argIdx))
		args = append(args, *req.RequiresHumanSession)
		argIdx++
	}
	if req.Labels != nil {
		setClauses = append(setClauses, fmt.Sprintf("labels = $%d", argIdx))
		args = append(args, req.Labels)
		argIdx++
	}
	if req.DeclaredResources != nil {
		setClauses = append(setClauses, fmt.Sprintf("declared_resources = $%d", argIdx))
		args = append(args, req.DeclaredResources)
		argIdx++
		if req.ResourcesVersion != nil {
			setClauses = append(setClauses, fmt.Sprintf("resources_version = $%d", argIdx))
			args = append(args, *req.ResourcesVersion+1)
			argIdx++
		}
	}
	if req.Attrs != nil {
		setClauses = append(setClauses, fmt.Sprintf("attrs = $%d", argIdx))
		args = append(args, req.Attrs)
		argIdx++
	}
	if req.Goal != nil {
		setClauses = append(setClauses, fmt.Sprintf("goal = $%d", argIdx))
		args = append(args, *req.Goal)
		argIdx++
	}
	if req.Content != nil {
		// Content may be updated in any non-terminal status
		nonTerminal := wi.Status == "queued" || wi.Status == "paused" || wi.Status == "running" || wi.Status == "blocked"
		if !nonTerminal {
			return nil, NewErr(ErrConflictTerminalState, fmt.Sprintf("cannot update content when work item is in terminal state: %s", wi.Status))
		}
		setClauses = append(setClauses, fmt.Sprintf("content = $%d", argIdx))
		args = append(args, *req.Content)
		argIdx++
	}

	args = append(args, wi.ID)
	query := fmt.Sprintf("UPDATE work_items SET %s WHERE id = $%d",
		strings.Join(setClauses, ", "), argIdx)
	_, err = tx.Exec(ctx, query, args...)
	if err != nil {
		return nil, NewErr(ErrInternalError, fmt.Sprintf("failed to update work_item: %v", err))
	}

	// Emit goal_updated event if goal changed
	if req.Goal != nil && req.GoalChangeReason != nil {
		evtID := NewID("evt")
		payload, _ := json.Marshal(map[string]any{
			"old_goal":   wi.Goal,
			"new_goal":   *req.Goal,
			"reason":     *req.GoalChangeReason,
			"changed_by": callerUserID,
		})
		_, err = tx.Exec(ctx, `
			INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
			VALUES ($1, $2, $3, $4, 'wi_goal_updated', $5, $6)`,
			evtID, wi.ID, callerUserID, "", payload, wi.Project,
		)
		if err != nil {
			return nil, NewErr(ErrInternalError, "failed to emit wi_goal_updated event")
		}
	}

	// Fix 3: emit wi_reclassified audit event if wi_type changed
	if req.WIType != nil {
		evtID := NewID("evt")
		oldWIType := ""
		if wi.WIType != nil {
			oldWIType = *wi.WIType
		}
		var oldRHS, newRHS *bool
		oldRHS = wi.RequiresHumanSession
		newRHS = req.RequiresHumanSession
		reason := ""
		if req.ReclassifyReason != nil {
			reason = *req.ReclassifyReason
		}
		payload, _ := json.Marshal(map[string]any{
			"old_wi_type":                oldWIType,
			"new_wi_type":                *req.WIType,
			"old_requires_human_session": oldRHS,
			"new_requires_human_session": newRHS,
			"reason":                     reason,
		})
		_, err = tx.Exec(ctx, `
			INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
			VALUES ($1, $2, $3, $4, 'wi_reclassified', $5, $6)`,
			evtID, wi.ID, callerUserID, "", payload, wi.Project,
		)
		if err != nil {
			return nil, NewErr(ErrInternalError, "failed to emit wi_reclassified event")
		}
	}

	// Emit wi_content_updated event if content changed
	if req.Content != nil {
		evtID := NewID("evt")
		oldContentHash := ""
		if wi.Content != nil {
			h := sha256.Sum256([]byte(*wi.Content))
			oldContentHash = hex.EncodeToString(h[:8])
		}
		newContentLength := len(*req.Content)
		payload, _ := json.Marshal(map[string]any{
			"old_content_hash":   oldContentHash,
			"new_content_length": newContentLength,
			"changed_by":         callerUserID,
		})
		_, err = tx.Exec(ctx, `
			INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
			VALUES ($1, $2, $3, $4, 'wi_content_updated', $5, $6)`,
			evtID, wi.ID, callerUserID, "", payload, wi.Project,
		)
		if err != nil {
			return nil, NewErr(ErrInternalError, "failed to emit wi_content_updated event")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, NewErr(ErrInternalError, "failed to commit update")
	}

	return GetWorkItem(ctx, pool, wi.ID)
}

// CancelWorkItem sets a work item's status to cancelled if it's not running.
func CancelWorkItem(ctx context.Context, pool *pgxpool.Pool, idOrSlug, callerUserID, callerRole string, callerProjectRoles map[string]string) *AihubError {
	wi, aihubErr := GetWorkItem(ctx, pool, idOrSlug)
	if aihubErr != nil {
		return aihubErr
	}

	// Permission check
	isReporter := wi.ReporterUserID == callerUserID
	projectRole := callerProjectRoles[wi.Project]
	canCancel := callerRole == "admin" || projectRole == "maintainer" ||
		(isReporter && (wi.Status == "queued" || wi.Status == "paused"))
	if !canCancel {
		return NewErr(ErrForbidden, "insufficient permissions to cancel this work item")
	}

	if wi.Status == "running" {
		return NewErr(ErrConflictWIAlreadyClaimed, "work item is running; force_takeover first, then cancel")
	}
	if wi.Status == "wrapped" || wi.Status == "failed" || wi.Status == "cancelled" {
		return NewErr(ErrConflictTerminalState, fmt.Sprintf("work item is already in terminal state: %s", wi.Status))
	}

	_, err := pool.Exec(ctx, `UPDATE work_items SET status='cancelled' WHERE id=$1`, wi.ID)
	if err != nil {
		return NewErr(ErrInternalError, "failed to cancel work item")
	}
	return nil
}

// GetReadyQueue returns the six-segment LCRS view for a project.
func GetReadyQueue(ctx context.Context, pool *pgxpool.Pool, project string, max int) (*ReadyQueue, *AihubError) {
	if max <= 0 {
		max = 10
	}
	result := &ReadyQueue{
		Items:             []ReadyItem{},
		Running:           []RunningItem{},
		Stalled:           []StalledItem{},
		Paused:            []PausedItem{},
		NeedsHumanSession: []ReadyItem{},
		Unclassified:      []ReadyItem{},
	}

	// items[]: queued + no blocker + requires_human_session=false
	itemRows, err := pool.Query(ctx, `
		SELECT wi.id, wi.slug, wi.wi_type, wi.priority, wi.goal
		FROM work_items wi
		WHERE wi.project = $1
		  AND wi.status = 'queued'
		  AND wi.requires_human_session = false
		  AND NOT EXISTS (
		    SELECT 1 FROM wi_dependencies dep
		    JOIN work_items blocker ON dep.blocking_wi_id = blocker.id
		    WHERE dep.blocked_wi_id = wi.id
		      AND dep.kind = 'blocks'
		      AND blocker.status NOT IN ('wrapped','cancelled','failed')
		  )
		ORDER BY
		  CASE wi.priority WHEN 'urgent' THEN 4 WHEN 'high' THEN 3
		                   WHEN 'normal' THEN 2 WHEN 'low' THEN 1 END DESC,
		  wi.created_at ASC
		LIMIT $2`,
		project, max,
	)
	if err != nil {
		return nil, NewErr(ErrInternalError, "failed to query ready items")
	}
	defer itemRows.Close()
	for itemRows.Next() {
		var item ReadyItem
		if err := itemRows.Scan(&item.ID, &item.Slug, &item.WIType, &item.Priority, &item.Goal); err != nil {
			continue
		}
		result.Items = append(result.Items, item)
	}
	itemRows.Close()

	// running[]: status=running
	runRows, err := pool.Query(ctx, `
		SELECT wi.id, wi.slug, wi.goal, ra.actor_display, ra.last_active_at
		FROM work_items wi
		JOIN run_attempts ra ON ra.id = wi.current_attempt_id
		WHERE wi.project = $1 AND wi.status = 'running'
		ORDER BY ra.last_active_at DESC`,
		project,
	)
	if err != nil {
		return nil, NewErr(ErrInternalError, "failed to query running items")
	}
	defer runRows.Close()
	for runRows.Next() {
		var item RunningItem
		var lat time.Time
		if err := runRows.Scan(&item.ID, &item.Slug, &item.Goal, &item.OwnerDisplay, &lat); err != nil {
			continue
		}
		item.LastActiveAt = lat.Format(time.RFC3339)
		result.Running = append(result.Running, item)
	}
	runRows.Close()

	// stalled[]: status=blocked AND has wi_stalled event
	stalledRows, err := pool.Query(ctx, `
		SELECT wi.id, wi.slug,
		       ae.payload->>'stall_reason' as stall_reason,
		       ae.created_at,
		       ae.actor_display
		FROM work_items wi
		JOIN LATERAL (
		  SELECT ae2.payload, ae2.created_at, ae2.actor_display
		  FROM agent_events ae2
		  WHERE ae2.work_item_id = wi.id AND ae2.event_type = 'wi_stalled'
		  ORDER BY ae2.created_at DESC LIMIT 1
		) ae ON true
		WHERE wi.project = $1 AND wi.status = 'blocked'
		ORDER BY ae.created_at DESC`,
		project,
	)
	if err == nil {
		defer stalledRows.Close()
		for stalledRows.Next() {
			var item StalledItem
			var stalledAt time.Time
			var stall *string
			if err := stalledRows.Scan(&item.ID, &item.Slug, &stall, &stalledAt, &item.LastActorDisplay); err != nil {
				continue
			}
			if stall != nil {
				item.StallReason = *stall
			}
			item.StalledSince = stalledAt.Format(time.RFC3339)
			result.Stalled = append(result.Stalled, item)
		}
		stalledRows.Close()
	}

	// paused[]: status=paused
	pausedRows, err := pool.Query(ctx, `
		SELECT wi.id, wi.slug, ra.last_active_at, ra.actor_display
		FROM work_items wi
		LEFT JOIN run_attempts ra ON ra.id = wi.current_attempt_id
		WHERE wi.project = $1 AND wi.status = 'paused'
		ORDER BY wi.updated_at DESC`,
		project,
	)
	if err == nil {
		defer pausedRows.Close()
		for pausedRows.Next() {
			var item PausedItem
			var lat *time.Time
			var actorDisplay *string
			if err := pausedRows.Scan(&item.ID, &item.Slug, &lat, &actorDisplay); err != nil {
				continue
			}
			if lat != nil {
				item.PausedSince = lat.Format(time.RFC3339)
			}
			if actorDisplay != nil {
				item.LastActorDisplay = *actorDisplay
			}
			result.Paused = append(result.Paused, item)
		}
		pausedRows.Close()
	}

	// needs_human_session[]: queued + no blocker + requires_human_session=true
	humanRows, err := pool.Query(ctx, `
		SELECT wi.id, wi.slug, wi.wi_type, wi.priority, wi.goal, wi.created_at
		FROM work_items wi
		WHERE wi.project = $1
		  AND wi.status = 'queued'
		  AND wi.requires_human_session = true
		  AND NOT EXISTS (
		    SELECT 1 FROM wi_dependencies dep
		    JOIN work_items blocker ON dep.blocking_wi_id = blocker.id
		    WHERE dep.blocked_wi_id = wi.id
		      AND dep.kind = 'blocks'
		      AND blocker.status NOT IN ('wrapped','cancelled','failed')
		  )
		ORDER BY
		  CASE wi.priority WHEN 'urgent' THEN 4 WHEN 'high' THEN 3
		                   WHEN 'normal' THEN 2 WHEN 'low' THEN 1 END DESC,
		  wi.created_at ASC
		LIMIT $2`,
		project, max,
	)
	if err == nil {
		defer humanRows.Close()
		for humanRows.Next() {
			var item ReadyItem
			var cat time.Time
			if err := humanRows.Scan(&item.ID, &item.Slug, &item.WIType, &item.Priority, &item.Goal, &cat); err != nil {
				continue
			}
			catStr := cat.Format(time.RFC3339)
			item.CreatedAt = catStr
			result.NeedsHumanSession = append(result.NeedsHumanSession, item)
		}
		humanRows.Close()
	}

	// unclassified[]: queued + no blocker + requires_human_session IS NULL
	unclRows, err := pool.Query(ctx, `
		SELECT wi.id, wi.slug, wi.wi_type, wi.priority, wi.goal, wi.created_at
		FROM work_items wi
		WHERE wi.project = $1
		  AND wi.status = 'queued'
		  AND wi.requires_human_session IS NULL
		  AND NOT EXISTS (
		    SELECT 1 FROM wi_dependencies dep
		    JOIN work_items blocker ON dep.blocking_wi_id = blocker.id
		    WHERE dep.blocked_wi_id = wi.id
		      AND dep.kind = 'blocks'
		      AND blocker.status NOT IN ('wrapped','cancelled','failed')
		  )
		ORDER BY wi.created_at ASC
		LIMIT $2`,
		project, max,
	)
	if err == nil {
		defer unclRows.Close()
		for unclRows.Next() {
			var item ReadyItem
			var cat time.Time
			if err := unclRows.Scan(&item.ID, &item.Slug, &item.WIType, &item.Priority, &item.Goal, &cat); err != nil {
				continue
			}
			catStr := cat.Format(time.RFC3339)
			item.CreatedAt = catStr
			result.Unclassified = append(result.Unclassified, item)
		}
		unclRows.Close()
	}

	// stale_running[]: running wi with updated_at > 24h (ownership reminder, not forced)
	staleRows, staleErr := pool.Query(ctx, `
		SELECT wi.id, wi.slug, wi.goal, ra.actor_display, ra.last_active_at
		FROM work_items wi
		JOIN run_attempts ra ON ra.id = wi.current_attempt_id
		WHERE wi.project = $1
		  AND wi.status = 'running'
		  AND wi.updated_at < now() - interval '24 hours'
		ORDER BY wi.updated_at ASC`,
		project,
	)
	if staleErr == nil {
		defer staleRows.Close()
		for staleRows.Next() {
			var item RunningItem
			var lat time.Time
			if err := staleRows.Scan(&item.ID, &item.Slug, &item.Goal, &item.OwnerDisplay, &lat); err != nil {
				continue
			}
			item.LastActiveAt = lat.Format(time.RFC3339)
			result.StaleRunning = append(result.StaleRunning, item)
		}
		staleRows.Close()
	}

	return result, nil
}
