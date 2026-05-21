package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ─── Types ───────────────────────────────────────────────────────────────────

// Memory represents a row from the memories table.
type Memory struct {
	ID               string          `json:"id"`
	Project          string          `json:"project"`
	Type             string          `json:"type"`
	Content          string          `json:"content"`
	AuthorUserID     string          `json:"author_user_id"`
	AuthorDisplay    string          `json:"author_display"`
	WorkItemID       *string         `json:"work_item_id,omitempty"`
	Visibility       string          `json:"visibility"`
	IsImmortal       bool            `json:"is_immortal"`
	BaseStrength     float64         `json:"base_strength"`
	StabilityDays    float64         `json:"stability_days"`
	LastActivatedAt  *time.Time      `json:"last_activated_at,omitempty"`
	LastActivatedBy  *string         `json:"last_activated_by,omitempty"`
	ActivationCount  int             `json:"activation_count"`
	ExpiresAt        *time.Time      `json:"expires_at,omitempty"`
	Tags             []string        `json:"tags"`
	SourceArtifactID *string         `json:"source_artifact_id,omitempty"`
	EmbModel         *string         `json:"emb_model,omitempty"`
	EmbDims          *int            `json:"emb_dims,omitempty"`
	Status           string          `json:"status"`
	Attrs            json.RawMessage `json:"attrs,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// MemoryWithStrength extends Memory with a computed recall score.
type MemoryWithStrength struct {
	Memory
	EffectiveStrength float64  `json:"effective_strength"`
	Similarity        *float64 `json:"similarity,omitempty"` // cosine similarity from pgvector
}

// RememberRequest is the body for POST /v1/memories.
type RememberRequest struct {
	Project         string          `json:"project"`
	Type            string          `json:"type"`
	Content         string          `json:"content"`
	Visibility      string          `json:"visibility"`
	WorkItemID      *string         `json:"work_item_id,omitempty"`
	BaseStrength    *float64        `json:"base_strength,omitempty"`
	Attrs           json.RawMessage `json:"attrs,omitempty"`
	ExpiresAt       *time.Time      `json:"expires_at,omitempty"`
	DedupMode       string          `json:"dedup_mode"` // strict | suggest | off
	RelatedMemoryIDs []string       `json:"related_memory_ids,omitempty"`
	ContextSnippet  *string         `json:"context_snippet,omitempty"`
	SupersedesMemID *string         `json:"supersedes_memory_id,omitempty"`
	Tags            []string        `json:"tags,omitempty"`
	// Set by handler from Bearer token — not from JSON body.
	CallerUserID  string `json:"-"`
	CallerDisplay string `json:"-"`
}

// RecallRequest is the query for GET /v1/memories.
type RecallRequest struct {
	Project             string   `json:"project"`
	Types               []string `json:"types,omitempty"`
	Visibility          string   `json:"visibility,omitempty"`
	WorkItemID          *string  `json:"work_item_id,omitempty"`
	Query               string   `json:"query,omitempty"`
	TopK                int      `json:"top_k,omitempty"`
	SimilarityThreshold float64  `json:"similarity_threshold,omitempty"`
	MinStrength         float64  `json:"min_strength"`
	IncludeArchived     bool     `json:"include_archived,omitempty"`
	RecencyWeight       float64  `json:"recency_weight"`
	Cursor              string   `json:"cursor,omitempty"`
	CallerUserID        string   `json:"-"`
	CallerRole          string   `json:"-"`
}

// ActivateResponse is the response body for POST /v1/memories/:id/activate.
type ActivateResponse struct {
	ActivationCount   int     `json:"activation_count"`
	NewStabilityDays  float64 `json:"new_stability_days"`
	EffectiveStrength float64 `json:"effective_strength"`
}

// RecallResponse is the response body for GET /v1/memories.
type RecallResponse struct {
	Items      []MemoryWithStrength `json:"items"`
	NextCursor *string              `json:"next_cursor,omitempty"`
}

// ─── Forgetting Curve (§7.2) ──────────────────────────────────────────────────

// baseStabilityForType returns the base stability_days for a memory type.
func baseStabilityForType(memType string) float64 {
	switch {
	case strings.HasPrefix(memType, "experience."):
		return 7
	case strings.HasPrefix(memType, "fact."):
		return 180
	case strings.HasPrefix(memType, "rule."):
		return 36500
	case strings.HasPrefix(memType, "methodology."):
		return 36500
	default:
		return 7
	}
}

// isImmortalType returns true for types that should be stored with is_immortal=TRUE.
func isImmortalType(memType string) bool {
	return strings.HasPrefix(memType, "rule.")
}

// MemoryStrength calculates effective_strength (raw) per §7.2.
// Formula: base_strength × exp(-days_since / stability_days)
// days_since uses last_activated_at if set, else created_at (M8).
func MemoryStrength(baseStrength, stabilityDays float64, lastActivatedAt *time.Time, createdAt time.Time) float64 {
	if stabilityDays <= 0 {
		return 0
	}
	ref := createdAt
	if lastActivatedAt != nil {
		ref = *lastActivatedAt
	}
	daysSince := time.Since(ref).Hours() / 24
	return baseStrength * math.Exp(-daysSince/stabilityDays)
}

// computeStabilityDays returns current stability_days per activation count (§7.2).
// stability_days = base_stability × (1 + activation_count × 0.5)
func computeStabilityDays(memType string, activationCount int) float64 {
	return baseStabilityForType(memType) * (1.0 + float64(activationCount)*0.5)
}

// ─── Remember ─────────────────────────────────────────────────────────────────

// Remember creates a new memory per §7 / §4.3.
// Returns (memory, isNew, error). isNew=false if dedup hit in suggest mode.
// Strict mode returns ErrConflictSimilarMemory on high-similarity match.
func Remember(ctx context.Context, pool *pgxpool.Pool, req *RememberRequest) (*Memory, bool, error) {
	// Validate type prefix
	validPrefixes := []string{"experience.", "fact.", "rule.", "methodology."}
	typeValid := false
	for _, p := range validPrefixes {
		if strings.HasPrefix(req.Type, p) {
			typeValid = true
			break
		}
	}
	if !typeValid {
		return nil, false, NewErr(ErrInvalidMemoryType,
			fmt.Sprintf("type %q must be one of experience.*, fact.*, rule.*, methodology.*", req.Type))
	}

	if req.DedupMode == "" {
		req.DedupMode = "suggest"
	}
	if req.Visibility == "" {
		req.Visibility = "project"
	}

	// Dedup check (skip for "off" mode)
	if req.DedupMode != "off" {
		existing, err := textDedupCheck(ctx, pool, req.Project, req.Type, req.Content)
		if err != nil {
			return nil, false, err
		}
		if existing != nil {
			if req.DedupMode == "strict" {
				sim := jaccardSimilarity(req.Content, existing.Content)
				return nil, false, NewErrDetails(ErrConflictSimilarMemory,
					"similar memory already exists",
					map[string]any{"existing": map[string]any{
						"id":         existing.ID,
						"type":       existing.Type,
						"content":    existing.Content,
						"similarity": sim,
					}},
				)
			}
			// suggest mode: annotate attrs with similar_to
			attrs := make(map[string]any)
			if len(req.Attrs) > 0 {
				json.Unmarshal(req.Attrs, &attrs) //nolint:errcheck
			}
			attrs["similar_to"] = existing.ID
			req.Attrs, _ = json.Marshal(attrs)
		}
	}

	baseStrength := 3.0
	if req.BaseStrength != nil {
		baseStrength = *req.BaseStrength
	}
	immortal := isImmortalType(req.Type)
	stabilityDays := computeStabilityDays(req.Type, 0)
	if req.Tags == nil {
		req.Tags = []string{}
	}
	if len(req.Attrs) == 0 {
		req.Attrs = json.RawMessage(`{}`)
	}

	mem := &Memory{}
	err := pool.QueryRow(ctx, `
		INSERT INTO memories (
			id, project, type, content, author_user_id, author_display,
			work_item_id, visibility, is_immortal, base_strength, stability_days,
			activation_count, expires_at, tags, source_artifact_id,
			status, attrs, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11,
			0, $12, $13, $14,
			'active', $15, clock_timestamp(), clock_timestamp()
		)
		RETURNING id, project, type, content, author_user_id, author_display,
			work_item_id, visibility, is_immortal, base_strength, stability_days,
			last_activated_at, last_activated_by, activation_count, expires_at,
			tags, source_artifact_id, emb_model, emb_dims, status, attrs, created_at, updated_at`,
		NewID("mem"), req.Project, req.Type, req.Content, req.CallerUserID, req.CallerDisplay,
		req.WorkItemID, req.Visibility, immortal, baseStrength, stabilityDays,
		req.ExpiresAt, req.Tags, nil, // source_artifact_id = nil
		req.Attrs,
	).Scan(
		&mem.ID, &mem.Project, &mem.Type, &mem.Content, &mem.AuthorUserID, &mem.AuthorDisplay,
		&mem.WorkItemID, &mem.Visibility, &mem.IsImmortal, &mem.BaseStrength, &mem.StabilityDays,
		&mem.LastActivatedAt, &mem.LastActivatedBy, &mem.ActivationCount, &mem.ExpiresAt,
		&mem.Tags, &mem.SourceArtifactID, &mem.EmbModel, &mem.EmbDims, &mem.Status,
		&mem.Attrs, &mem.CreatedAt, &mem.UpdatedAt,
	)
	if err != nil {
		return nil, false, NewErr(ErrInternalError, fmt.Sprintf("failed to insert memory: %v", err))
	}

	// Emit memory_created event (non-critical, fire and forget)
	payload, _ := json.Marshal(map[string]any{
		"memory_id":  mem.ID,
		"type":       mem.Type,
		"project":    mem.Project,
		"visibility": mem.Visibility,
	})
	pool.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
		VALUES ($1, $2, $3, $4, 'memory_created', $5, $6)`,
		NewID("evt"), req.WorkItemID, req.CallerUserID, req.CallerDisplay, payload, req.Project,
	) //nolint:errcheck

	return mem, true, nil
}

// textDedupCheck checks for existing active memories with Jaccard similarity > 0.75.
func textDedupCheck(ctx context.Context, pool *pgxpool.Pool, project, memType, content string) (*Memory, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, type, content
		FROM memories
		WHERE project = $1 AND type = $2 AND status = 'active'
		  AND (expires_at IS NULL OR expires_at > clock_timestamp())
		ORDER BY created_at DESC
		LIMIT 50`,
		project, memType,
	)
	if err != nil {
		return nil, NewErr(ErrInternalError, fmt.Sprintf("dedup query: %v", err))
	}
	defer rows.Close()

	type candidate struct{ ID, Type, Content string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.ID, &c.Type, &c.Content); err == nil {
			candidates = append(candidates, c)
		}
	}
	rows.Close()

	for _, c := range candidates {
		if jaccardSimilarity(content, c.Content) > 0.75 {
			return &Memory{ID: c.ID, Type: c.Type, Content: c.Content}, nil
		}
	}
	return nil, nil
}

// jaccardSimilarity computes word-level Jaccard similarity.
func jaccardSimilarity(a, b string) float64 {
	sa, sb := tokenSet(a), tokenSet(b)
	if len(sa) == 0 && len(sb) == 0 {
		return 1.0
	}
	intersection := 0
	for k := range sa {
		if sb[k] {
			intersection++
		}
	}
	union := len(sa) + len(sb) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// tokenSet returns a set of lowercase words.
func tokenSet(s string) map[string]bool {
	words := strings.Fields(strings.ToLower(s))
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// ─── Recall ───────────────────────────────────────────────────────────────────

// Recall retrieves memories per §7.5 (text/tag search path).
// pgvector cosine search is handled in RecallWithVector when embedding is available.
func Recall(ctx context.Context, pool *pgxpool.Pool, req *RecallRequest) (*RecallResponse, error) {
	if req.TopK <= 0 {
		req.TopK = 20
	}
	if req.MinStrength <= 0 {
		req.MinStrength = 0.3
	}
	if req.RecencyWeight <= 0 {
		req.RecencyWeight = 0.3
	}

	args := []any{req.Project}
	idx := 2

	statusSet := "'active'"
	if req.IncludeArchived {
		statusSet = "'active','archived'"
	}

	where := fmt.Sprintf(`
		project = $1
		AND status IN (%s)
		AND (expires_at IS NULL OR expires_at > clock_timestamp())`, statusSet)

	// Visibility: private memories only visible to author (C2 fix: 'personal' → 'private')
	if req.CallerRole != "admin" {
		where += fmt.Sprintf(` AND (visibility != 'private' OR author_user_id = $%d)`, idx)
		args = append(args, req.CallerUserID)
		idx++
	}

	// Type filter with prefix matching
	if len(req.Types) > 0 {
		typeClauses := make([]string, 0, len(req.Types))
		for _, t := range req.Types {
			if strings.HasSuffix(t, ".*") {
				prefix := strings.TrimSuffix(t, "*")
				typeClauses = append(typeClauses, fmt.Sprintf("type LIKE $%d", idx))
				args = append(args, prefix+"%")
			} else {
				typeClauses = append(typeClauses, fmt.Sprintf("type = $%d", idx))
				args = append(args, t)
			}
			idx++
		}
		where += " AND (" + strings.Join(typeClauses, " OR ") + ")"
	}

	if req.WorkItemID != nil {
		where += fmt.Sprintf(" AND work_item_id = $%d", idx)
		args = append(args, *req.WorkItemID)
		idx++
	}

	// C5 fix: cursor-based pagination using timestamp, not id.
	// ORDER BY last_activated_at DESC NULLS LAST, created_at DESC means we need
	// AND (last_activated_at < cursor_ts OR (last_activated_at IS NULL AND created_at < cursor_ts))
	// Cursor value is an RFC3339Nano timestamp string of the last item's sort key.
	if req.Cursor != "" {
		where += fmt.Sprintf(` AND (
			last_activated_at < $%d::timestamptz
			OR (last_activated_at IS NULL AND created_at < $%d::timestamptz)
		)`, idx, idx)
		args = append(args, req.Cursor)
		idx++
	}

	args = append(args, req.TopK+1)

	query := fmt.Sprintf(`
		SELECT id, project, type, content, author_user_id, author_display,
			work_item_id, visibility, is_immortal, base_strength, stability_days,
			last_activated_at, last_activated_by, activation_count, expires_at,
			tags, source_artifact_id, emb_model, emb_dims, status, attrs, created_at, updated_at
		FROM memories
		WHERE %s
		ORDER BY last_activated_at DESC NULLS LAST, created_at DESC
		LIMIT $%d`, where, idx)

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, NewErr(ErrInternalError, fmt.Sprintf("recall query: %v", err))
	}
	defer rows.Close()

	var items []MemoryWithStrength
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			continue
		}
		strength := MemoryStrength(m.BaseStrength, m.StabilityDays, m.LastActivatedAt, m.CreatedAt)
		// Apply min_strength filter (immortal memories bypass)
		if !m.IsImmortal && strength < req.MinStrength {
			continue
		}
		items = append(items, MemoryWithStrength{Memory: *m, EffectiveStrength: strength})
	}
	rows.Close()

	var nextCursor *string
	if len(items) > req.TopK {
		items = items[:req.TopK]
		last := items[len(items)-1]
		// C5 fix: cursor is the sort-key timestamp, not the id.
		// Use last_activated_at when set, else created_at (mirrors ORDER BY logic).
		var cursorVal string
		if last.LastActivatedAt != nil {
			cursorVal = last.LastActivatedAt.Format(time.RFC3339Nano)
		} else {
			cursorVal = last.CreatedAt.Format(time.RFC3339Nano)
		}
		nextCursor = &cursorVal
	}

	return &RecallResponse{Items: items, NextCursor: nextCursor}, nil
}

// scanMemory scans a full memory row from pgx.Rows.
func scanMemory(rows pgx.Rows) (*Memory, error) {
	m := &Memory{}
	err := rows.Scan(
		&m.ID, &m.Project, &m.Type, &m.Content, &m.AuthorUserID, &m.AuthorDisplay,
		&m.WorkItemID, &m.Visibility, &m.IsImmortal, &m.BaseStrength, &m.StabilityDays,
		&m.LastActivatedAt, &m.LastActivatedBy, &m.ActivationCount, &m.ExpiresAt,
		&m.Tags, &m.SourceArtifactID, &m.EmbModel, &m.EmbDims, &m.Status,
		&m.Attrs, &m.CreatedAt, &m.UpdatedAt,
	)
	return m, err
}

// ─── Activate (§7.3) ──────────────────────────────────────────────────────────

// Activate reinforces a memory: increments activation_count, recomputes stability_days,
// resets last_activated_at, and revives archived memories.
func Activate(ctx context.Context, pool *pgxpool.Pool, memID, callerUserID, callerDisplay string) (*ActivateResponse, error) {
	var memType string
	var baseStrength, stabilityDays float64
	var activationCount int
	var lastActivatedAt *time.Time
	var status string
	var createdAt time.Time

	err := pool.QueryRow(ctx, `
		SELECT type, base_strength, stability_days, activation_count,
		       last_activated_at, status, created_at
		FROM memories WHERE id = $1`, memID,
	).Scan(&memType, &baseStrength, &stabilityDays, &activationCount,
		&lastActivatedAt, &status, &createdAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, NewErr(ErrNotFound, "memory not found")
		}
		return nil, NewErr(ErrInternalError, fmt.Sprintf("failed to load memory: %v", err))
	}
	if status == "redacted" {
		return nil, NewErr(ErrForbidden, "cannot activate a redacted memory")
	}

	newCount := activationCount + 1
	newStability := computeStabilityDays(memType, newCount)

	var newLastActivatedAt time.Time
	err = pool.QueryRow(ctx, `
		UPDATE memories
		SET activation_count   = $1,
		    stability_days     = $2,
		    last_activated_at  = clock_timestamp(),
		    last_activated_by  = $3,
		    status             = 'active',
		    updated_at         = clock_timestamp()
		WHERE id = $4
		RETURNING last_activated_at`,
		newCount, newStability, callerUserID, memID,
	).Scan(&newLastActivatedAt)
	if err != nil {
		return nil, NewErr(ErrInternalError, fmt.Sprintf("failed to activate memory: %v", err))
	}

	strength := MemoryStrength(baseStrength, newStability, &newLastActivatedAt, createdAt)

	// Emit memory_activated event
	payload, _ := json.Marshal(map[string]any{
		"memory_id":          memID,
		"activation_count":   newCount,
		"new_stability_days": newStability,
	})
	pool.Exec(ctx, `
		INSERT INTO agent_events (id, actor_user_id, actor_display, event_type, payload, project)
		SELECT $1, $2, $3, 'memory_activated', $4, project
		FROM memories WHERE id = $5`,
		NewID("evt"), callerUserID, callerDisplay, payload, memID,
	) //nolint:errcheck

	return &ActivateResponse{
		ActivationCount:   newCount,
		NewStabilityDays:  newStability,
		EffectiveStrength: strength,
	}, nil
}

// ─── Redact (§4.3) ────────────────────────────────────────────────────────────

// Redact soft-deletes a memory (status='redacted', expires_at=now()).
// Only the author or an admin can redact.
func Redact(ctx context.Context, pool *pgxpool.Pool, memID, callerUserID, callerRole string) error {
	var authorID, status string
	err := pool.QueryRow(ctx, `SELECT author_user_id, status FROM memories WHERE id = $1`, memID).
		Scan(&authorID, &status)
	if err != nil {
		if err == pgx.ErrNoRows {
			return NewErr(ErrNotFound, "memory not found")
		}
		return NewErr(ErrInternalError, fmt.Sprintf("failed to load memory: %v", err))
	}
	if status == "redacted" {
		return nil // idempotent
	}
	if callerRole != "admin" && authorID != callerUserID {
		return NewErr(ErrForbidden, "only the author or an admin can redact this memory")
	}

	_, err = pool.Exec(ctx, `
		UPDATE memories
		SET status = 'redacted', is_immortal = false,
		    expires_at = clock_timestamp(), updated_at = clock_timestamp()
		WHERE id = $1`, memID)
	if err != nil {
		return NewErr(ErrInternalError, fmt.Sprintf("failed to redact memory: %v", err))
	}
	return nil
}

// ─── Events ───────────────────────────────────────────────────────────────────

// EventRow represents a row from agent_events.
type EventRow struct {
	ID           string          `json:"id"`
	WorkItemID   *string         `json:"work_item_id,omitempty"`
	WorkItemSlug *string         `json:"work_item_slug,omitempty"`
	RunAttemptID *string         `json:"run_attempt_id,omitempty"`
	ActorUserID  *string         `json:"actor_user_id,omitempty"`
	ActorDisplay *string         `json:"actor_display,omitempty"`
	EventType    string          `json:"event_type"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	Pinned       bool            `json:"pinned"`
	Project      *string         `json:"project,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
}

// ListEventsFilter is the query for GET /v1/events.
type ListEventsFilter struct {
	WorkItemID  *string
	Project     *string
	UserID      *string
	Types       []string
	Since       *string
	Limit       int
	PinnedFirst bool
	Cursor      *string
}

// ListEventsResponse is the response for GET /v1/events.
type ListEventsResponse struct {
	Events     []EventRow `json:"events"`
	NextCursor *string    `json:"next_cursor,omitempty"`
}

// ListEvents queries agent_events by work_item_id or project.
// At least one of WorkItemID or Project must be set.
func ListEvents(ctx context.Context, pool *pgxpool.Pool, f *ListEventsFilter) (*ListEventsResponse, error) {
	if f.WorkItemID == nil && f.Project == nil {
		return nil, NewErr(ErrBadRequest, "work_item_id or project is required")
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}

	args := []any{}
	idx := 1
	clauses := []string{}

	if f.WorkItemID != nil {
		clauses = append(clauses, fmt.Sprintf("e.work_item_id = $%d", idx))
		args = append(args, *f.WorkItemID)
		idx++
	} else if f.Project != nil {
		clauses = append(clauses, fmt.Sprintf("e.project = $%d", idx))
		args = append(args, *f.Project)
		idx++
	}
	if f.UserID != nil {
		clauses = append(clauses, fmt.Sprintf("e.actor_user_id = $%d", idx))
		args = append(args, *f.UserID)
		idx++
	}
	if len(f.Types) > 0 {
		ph := make([]string, len(f.Types))
		for i, t := range f.Types {
			ph[i] = fmt.Sprintf("$%d", idx)
			args = append(args, t)
			idx++
		}
		clauses = append(clauses, "e.event_type IN ("+strings.Join(ph, ",")+")")
	}
	if f.Since != nil {
		clauses = append(clauses, fmt.Sprintf("e.created_at > $%d", idx))
		args = append(args, *f.Since)
		idx++
	}
	// C5 fix: cursor is a created_at timestamp (RFC3339Nano), not an id.
	// ORDER BY e.created_at DESC means we want rows with created_at < cursor_ts.
	if f.Cursor != nil {
		clauses = append(clauses, fmt.Sprintf("e.created_at < $%d::timestamptz", idx))
		args = append(args, *f.Cursor)
		idx++
	}

	where := strings.Join(clauses, " AND ")
	if where == "" {
		where = "TRUE"
	}

	orderBy := "e.created_at DESC"
	if f.PinnedFirst {
		orderBy = "e.pinned DESC, e.created_at DESC"
	}

	args = append(args, f.Limit+1)
	query := fmt.Sprintf(`
		SELECT e.id, e.work_item_id, w.slug, e.run_attempt_id,
		       e.actor_user_id, e.actor_display, e.event_type,
		       e.payload, COALESCE(e.pinned, false), e.project, e.created_at
		FROM agent_events e
		LEFT JOIN work_items w ON w.id = e.work_item_id
		WHERE %s
		ORDER BY %s
		LIMIT $%d`, where, orderBy, idx)

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, NewErr(ErrInternalError, fmt.Sprintf("events query: %v", err))
	}
	defer rows.Close()

	var events []EventRow
	for rows.Next() {
		var ev EventRow
		if err := rows.Scan(&ev.ID, &ev.WorkItemID, &ev.WorkItemSlug, &ev.RunAttemptID,
			&ev.ActorUserID, &ev.ActorDisplay, &ev.EventType,
			&ev.Payload, &ev.Pinned, &ev.Project, &ev.CreatedAt); err != nil {
			continue
		}
		events = append(events, ev)
	}
	rows.Close()

	var nextCursor *string
	if len(events) > f.Limit {
		events = events[:f.Limit]
		// C5 fix: cursor is the created_at timestamp of the last returned event.
		cursorVal := events[len(events)-1].CreatedAt.Format(time.RFC3339Nano)
		nextCursor = &cursorVal
	}

	return &ListEventsResponse{Events: events, NextCursor: nextCursor}, nil
}

// ─── Emit Event ───────────────────────────────────────────────────────────────

// EmitEventRequest is the body for POST /v1/events.
type EmitEventRequest struct {
	WorkItemID    string          `json:"work_item_id"`
	AttemptID     string          `json:"attempt_id"`
	ClaimEpoch    int64           `json:"claim_epoch"`
	SessionSecret string          `json:"session_secret"`
	EventType     string          `json:"event_type"`
	Payload       json.RawMessage `json:"payload"`
	Pinned        bool            `json:"pinned"`
	Admin         bool            `json:"admin"`
}

// adminEventWhitelist contains event_types allowed for admin=true events.
var adminEventWhitelist = map[string]bool{
	"admin_unblock":              true,
	"admin_force_takeover":       true,
	"phase_config_updated":       true,
	"wi_needs_attention":         true,
	"wi_classification_missing":  true,
}

// adminOnlyEventTypes are event_types that ALWAYS require admin role, regardless of
// whether req.Admin is set. This prevents event-type forgery (H6 fix): a non-admin
// caller setting admin=false but using an admin event type would otherwise bypass the
// req.Admin gate.
var adminOnlyEventTypes = map[string]bool{
	"admin_redact":          true,
	"admin_unblock":         true,
	"admin_force_takeover":  true,
	"admin_gc_manual":       true,
}

// EmitEvent inserts a new event into agent_events.
func EmitEvent(ctx context.Context, pool *pgxpool.Pool, req *EmitEventRequest,
	callerUserID, callerDisplay, callerRole string) (string, error) {

	if len(req.Payload) > 65536 {
		return "", NewErr(ErrPayloadTooLarge, "event payload exceeds 64KB limit")
	}

	// H6 fix: admin-only event types require admin role regardless of req.Admin flag.
	// This blocks forgery where a non-admin omits admin=true but uses an admin event_type.
	if adminOnlyEventTypes[req.EventType] && callerRole != "admin" {
		return "", NewErr(ErrForbidden,
			fmt.Sprintf("event type %q requires admin role", req.EventType))
	}

	if req.Admin {
		if callerRole != "admin" {
			return "", NewErr(ErrForbidden, "admin=true requires admin role")
		}
		if !adminEventWhitelist[req.EventType] {
			return "", NewErr(ErrForbidden,
				fmt.Sprintf("event_type %q is not in the admin whitelist", req.EventType))
		}
	}

	// Verify attempt credential when work_item context is provided
	if req.WorkItemID != "" && req.AttemptID != "" {
		wi, aihubErr := GetWorkItem(ctx, pool, req.WorkItemID)
		if aihubErr != nil {
			return "", aihubErr
		}
		if err := verifyAttemptCredentialSimple(ctx, pool, wi, req.AttemptID, req.ClaimEpoch, req.SessionSecret); err != nil {
			return "", err
		}
	}

	if len(req.Payload) == 0 {
		req.Payload = json.RawMessage(`{}`)
	}

	var wiIDArg *string
	if req.WorkItemID != "" {
		wiIDArg = &req.WorkItemID
	}
	var attemptIDArg *string
	if req.AttemptID != "" {
		attemptIDArg = &req.AttemptID
	}

	// Derive project from the work_item if present
	var project *string
	if req.WorkItemID != "" {
		wi, err := GetWorkItem(ctx, pool, req.WorkItemID)
		if err == nil {
			project = &wi.Project
		}
	}

	evtID := NewID("evt")
	_, err := pool.Exec(ctx, `
		INSERT INTO agent_events (
			id, work_item_id, run_attempt_id, actor_user_id, actor_display,
			event_type, payload, pinned, project, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, clock_timestamp())`,
		evtID, wiIDArg, attemptIDArg, callerUserID, callerDisplay,
		req.EventType, req.Payload, req.Pinned, project,
	)
	if err != nil {
		return "", NewErr(ErrInternalError, fmt.Sprintf("failed to insert event: %v", err))
	}
	return evtID, nil
}

// verifyAttemptCredentialSimple is a lightweight credential check for event emission.
func verifyAttemptCredentialSimple(ctx context.Context, pool *pgxpool.Pool, wi *WorkItem,
	attemptID string, claimEpoch int64, sessionSecret string) *AihubError {

	if wi.CurrentAttemptID == nil || *wi.CurrentAttemptID != attemptID {
		return NewErr(ErrAttemptMismatch, "attempt_id does not match current attempt")
	}
	if wi.CurrentAttemptEpoch != claimEpoch {
		return NewErrDetails(ErrConflictEpochMismatch, "claim_epoch mismatch",
			map[string]any{"current_epoch": wi.CurrentAttemptEpoch})
	}
	secretHash := hashSecretInternal(sessionSecret)
	var storedHash string
	err := pool.QueryRow(ctx, `SELECT session_secret_hash FROM run_attempts WHERE id = $1`, attemptID).
		Scan(&storedHash)
	if err != nil || storedHash != secretHash {
		return NewErr(ErrAttemptMismatch, "invalid session_secret")
	}
	return nil
}
