package domain

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
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
	// Similarity is populated only on the semantic-search path
	// (GET /v1/work_items?query=..., aihub#273): cosine similarity between the
	// query embedding and this wi's embedding. omitempty keeps every non-query
	// response byte-identical to before.
	Similarity *float64 `json:"similarity,omitempty"`
	// StepState is populated only when the caller asks for it
	// (GET /v1/work_items?include_step_state=true, aihub#280). omitempty keeps
	// every response that did not ask byte-identical to before, which is also
	// what makes "did the param take effect?" answerable by comparing the
	// response key set — the row count cannot answer it.
	//
	// Absence has THREE causes and the response cannot distinguish them:
	//  1. the caller did not ask (no include_step_state)
	//  2. the caller asked, but the wi has never been claimed, so no
	//     wi_step_state row exists
	//  3. the caller asked, the row exists, and the lookup FAILED —
	//     attachStepState is best-effort and reports only to the server's stderr
	//
	// (3) is the uncomfortable one: a transient pool exhaustion makes a claimed,
	// in-progress work item read exactly like a never-claimed one, so a consumer
	// like pf-retro would conclude "no steps ran". That is a deliberate trade —
	// failing the whole list would let include_step_state break a call that works
	// without it — but it is a real ambiguity, named here rather than left for
	// someone to discover from behaviour. Callers that must be sure should read
	// the step state through pf_get_step.
	StepState *WorkItemStepState `json:"step_state,omitempty"`
}

// WorkItemStepState mirrors the wi_step_state row for one work item, as served
// under GET /v1/work_items?include_step_state=true (aihub#280).
//
// Every field is a pointer or has a natural zero because the row's columns are
// nullable: current_step is NULL before the first start_step and again after the
// last step completes, and current_step_attempt is NULL whenever the step is
// idle. Collapsing those to "" would make "between steps" indistinguishable from
// "no such column", which is the failure class this wi exists to close.
type WorkItemStepState struct {
	WIType             *string    `json:"wi_type"`
	GraphSource        string     `json:"graph_source"`
	CurrentStep        *string    `json:"current_step"`
	CurrentStepStatus  *string    `json:"current_step_status"`
	CurrentStepAttempt *string    `json:"current_step_attempt"`
	StepStartedAt      *time.Time `json:"step_started_at"`
	Version            int64      `json:"version"`
	UpdatedAt          time.Time  `json:"updated_at"`
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
	// AttrsPatch and AttrsUnset are the aihub#288 merge path. Attrs stays a
	// whole-column REPLACE; these two are the opt-in, non-destructive
	// alternative. See buildWorkItemUpdate for the exact semantics.
	AttrsPatch       json.RawMessage `json:"attrs_patch"`
	AttrsUnset       []string        `json:"attrs_unset"`
	Goal             *string         `json:"goal"`
	GoalChangeReason *string         `json:"goal_change_reason"`
	Content          *string         `json:"content"`
}

// ReadyQueue is the six-segment LCRS response for GET /v1/work_items/ready.
type ReadyQueue struct {
	Items             []ReadyItem   `json:"items"`
	Running           []RunningItem `json:"running"`
	Stalled           []StalledItem `json:"stalled"`
	Paused            []PausedItem  `json:"paused"`
	NeedsHumanSession []ReadyItem   `json:"needs_human_session"`
	Unclassified      []ReadyItem   `json:"unclassified"`
	StaleRunning      []RunningItem `json:"stale_running,omitempty"`
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
	ID           string `json:"id"`
	Slug         string `json:"slug"`
	Goal         string `json:"goal"`
	OwnerDisplay string `json:"owner_display"`
	LastActiveAt string `json:"last_active_at"`
}

// StalledItem is a work item in the stalled segment.
type StalledItem struct {
	ID               string `json:"id"`
	Slug             string `json:"slug"`
	StallReason      string `json:"stall_reason"`
	StalledSince     string `json:"stalled_since"`
	LastActorDisplay string `json:"last_actor_display"`
}

// PausedItem is a work item in the paused segment.
type PausedItem struct {
	ID               string  `json:"id"`
	Slug             string  `json:"slug"`
	PausedSince      string  `json:"paused_since"`
	LastActorDisplay string  `json:"last_actor_display"`
	PauseReason      *string `json:"pause_reason,omitempty"`
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
	// aihub#238: reject resource types the lock mapper cannot understand at the
	// point of entry. Before this, a mistyped entry was stored happily, showed up
	// in the UI as "resources declared", and acquired no lock — the wi looked
	// guarded and was not.
	if aihubErr := ValidateDeclaredResources(req.DeclaredResources); aihubErr != nil {
		return nil, aihubErr
	}
	if len(req.Attrs) == 0 {
		req.Attrs = json.RawMessage("{}")
	}

	// Reject unimplemented scenarios
	if req.Scenario != "coding" {
		return nil, NewErr(ErrNotImplemented, fmt.Sprintf("scenario %q is not yet implemented", req.Scenario))
	}

	// aihub#273: compute the embedding before the transaction below begins —
	// it is a network call and must never run inside an open tx (same rule as
	// Remember, aihub#192). Best-effort: failure leaves the emb_* columns NULL
	// and the wi is still findable via the ILIKE text fallback.
	wiContent := ""
	if req.Content != nil {
		wiContent = *req.Content
	}
	embVecLit, embModel, embDims := embedWorkItemBestEffort(ctx, req.Goal, wiContent)

	// aihub#316: sampled BEFORE pool.Begin, and that ordering is the whole
	// point. Reading ctx.Err() AFTER Begin returns cannot tell "the context was
	// already dead when we called Begin" (embedding ate the budget — the
	// aihub#316 shape) from "the context died WHILE Begin was blocked waiting
	// for a free connection" (pool exhaustion — a database-side problem).
	// Blaming the embedding provider for the second case would be the same
	// mis-attribution this whole branch exists to remove, just pointing the
	// other way: measured with embedding switched off entirely and MaxConns=1
	// held busy, the after-the-fact read produced "an upstream dependency (most
	// likely the embedding provider) consumed the request budget" for a request
	// that had no upstream dependency at all.
	ctxDeadBeforeDB := ctx.Err()

	tx, err := pool.Begin(ctx)
	if err != nil {
		// The three cases get three different sentences, because each sends the
		// reader somewhere different. The original single "failed to begin
		// transaction" is how the 2026-09-01 investigation spent two passes on a
		// database that was answering in 9ms.
		switch {
		case errors.Is(ctxDeadBeforeDB, context.DeadlineExceeded):
			return nil, NewErr(ErrInternalError,
				"request deadline exhausted before reaching the database — an upstream dependency (most likely the embedding provider) consumed the request budget")
		case ctxDeadBeforeDB != nil:
			// Canceled: the caller hung up. Saying "deadline exhausted" here
			// would send the next reader hunting a slow dependency that was
			// never slow.
			return nil, NewErr(ErrInternalError,
				"request was cancelled before reaching the database — the caller disconnected upstream of any database work")
		case ctx.Err() != nil:
			// Alive on entry, dead now: the time went INSIDE Begin, i.e.
			// waiting for a pool connection. Name the pool, not the upstream.
			return nil, NewErr(ErrInternalError,
				"request deadline expired while waiting for a database connection — the connection pool is saturated, not an upstream dependency")
		default:
			return nil, NewErr(ErrInternalError, "failed to begin transaction")
		}
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// wi_type and requires_human_session are provided by the caller directly.
	// scenario_phase_configs has been removed; classification is handled client-side
	// using the local scenario clone (scenario_ref).
	wiType := req.WIType
	requiresHumanSession := req.RequiresHumanSession

	// Dedup check (skip if force_create)
	if !req.ForceCreate {
		if aihubErr := checkDedup(ctx, tx, req); aihubErr != nil {
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
		return nil, dbErrCause(err, "increment wi_seq")
	}

	wiID := newWorkItemID()

	_, err = tx.Exec(ctx, `
		INSERT INTO work_items (
			id, seq, project, scenario, goal, source, wi_type, priority,
			requires_human_session, milestone, labels, status,
			declared_resources, reporter_user_id, reporter_display,
			parent_work_item_id, attrs, content,
			emb_model, emb_dims, emb_vector
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, 'queued',
			$12, $13, $14,
			$15, $16, $17,
			$18, $19, $20::vector
		)`,
		wiID, seq, req.Project, req.Scenario, req.Goal, req.Source, wiType, req.Priority,
		requiresHumanSession, req.Milestone, req.Labels, req.DeclaredResources,
		callerUserID, callerDisplay, req.ParentWorkItemID, req.Attrs, req.Content,
		embModel, embDims, embVecLit,
	)
	if err != nil {
		return nil, dbErrCause(err, "failed to insert work_item")
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
		return nil, dbErr(err, "failed to emit work_item_filed event")
	}

	// Insert blocked_by dependencies
	for _, blockingID := range req.BlockedBy {
		_, err = tx.Exec(ctx, `
			INSERT INTO wi_dependencies (blocked_wi_id, blocking_wi_id, kind, created_by)
			VALUES ($1, $2, 'blocks', $3)`,
			wiID, blockingID, callerUserID,
		)
		if err != nil {
			return nil, dbErrCause(err, fmt.Sprintf("failed to create dependency for blocking_wi %s", blockingID))
		}
	}

	// If blocked_by is non-empty, set status to blocked
	if len(req.BlockedBy) > 0 {
		_, err = tx.Exec(ctx, `UPDATE work_items SET status='blocked' WHERE id=$1`, wiID)
		if err != nil {
			return nil, dbErr(err, "failed to set blocked status")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		if aerr := retryConflictErr(err, "failed to commit transaction"); aerr != nil { // aihub#334
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "failed to commit transaction")
	}

	return GetWorkItem(ctx, pool, wiID)
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

// setOverlap computes the Jaccard index |A∩B|/|A∪B| over the sets obtained by
// DE-DUPLICATING both a and b. Both sides must be deduplicated before
// intersection and union are computed, so duplicate entries in either input
// can never push the ratio above 1.0.
//
// aihub#251 defect 1: the previous version only deduplicated side a into a
// set, then counted the intersection by iterating the RAW (non-deduplicated)
// slice b and sized the union from raw slice lengths (len(a)+len(b)). A
// duplicate-laden b (e.g. an existing candidate's stored labels containing
// the same label 5+ times) could then push intersection above len(a) and the
// ratio arbitrarily far past 1.0 -- the reported ">100% similar" scores.
//
// Empty-case semantics: both-empty returns 0, not 1. "Neither side declared
// anything" is an ABSENCE of evidence, not evidence of similarity -- treating
// it as a perfect match manufactured a constant score bonus for every
// candidate that also happened to have no labels/resources, which was a
// second major driver of the false-positive collisions (aihub#251).
func setOverlap(a, b []string) float64 {
	setA := make(map[string]bool, len(a))
	for _, v := range a {
		setA[v] = true
	}
	setB := make(map[string]bool, len(b))
	for _, v := range b {
		setB[v] = true
	}
	if len(setA) == 0 && len(setB) == 0 {
		return 0
	}
	intersection := 0
	for v := range setA {
		if setB[v] {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// weightedComponent decides whether one non-goal dedup dimension (labels or
// resources) is APPLICABLE and, if so, what it scores.
//
// aihub#251 review follow-up (mem_veTEPhFm, WARN): a dimension is
// inapplicable ONLY when BOTH sides are empty -- neither side offers any
// evidence at all, so it should carry no weight in the composite score
// rather than being scored a hard 0 (which structurally capped the score at
// 0.6 whenever neither side declared labels/resources, even for a
// byte-identical goal). When exactly ONE side is empty and the other is not,
// that IS genuine evidence of difference -- the dimension is applicable and
// legitimately contributes a real 0, not a dropped weight.
//
// setOverlap alone cannot distinguish these two cases: it returns 0 for both
// the both-empty and the one-empty shape. That distinction has to be made
// here, from the raw (pre-overlap) inputs, before calling setOverlap.
func weightedComponent(a, b []string, weight float64) (score float64, appliedWeight float64) {
	if len(a) == 0 && len(b) == 0 {
		return 0, 0
	}
	return setOverlap(a, b), weight
}

// declaredResourceKeys parses a declared_resources JSON payload into a slice
// of canonical per-entry keys suitable for setOverlap. It handles both the
// current entry shape ({"type":"path","uri":"file:...","intent":"write"}) and
// the legacy pre-aihub#238 shape that may still be stored on old rows
// ({"type":"file_scope","value":"..."} -- see
// TestCreateWorkItem_RejectsUnknownTypeBeforeTouchingDB in
// declared_resources_wiring_test.go, which confirms new requests can no
// longer create this shape but says nothing about what is already stored).
// Entries matching neither shape are skipped rather than causing a crash or
// being silently treated as a match.
//
// ok is false ONLY when raw is non-empty and is not parseable as a JSON array
// of objects -- a genuine parse failure. It is true (with a nil/empty key
// slice) for absent, empty-array, or null input, since declaring no resources
// at all is not a parse error.
//
// aihub#251 defect 2: the previous code unmarshalled this same object-array
// JSON directly into []string, which always fails for object entries; the
// error was discarded (`_ = json.Unmarshal(...)`), leaving both sides nil.
// setOverlap(nil, nil) then hit its both-empty branch (formerly 1.0),
// silently adding a constant +0.2 to the composite score for every candidate
// regardless of whether resources actually matched. Callers here MUST treat
// ok=false as "not comparable" (contribute no similarity), never as a match.
func declaredResourceKeys(raw json.RawMessage) (keys []string, ok bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == "[]" {
		return nil, true
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false
	}
	keys = make([]string, 0, len(items))
	for _, item := range items {
		typ, _ := item["type"].(string)
		if typ == "" {
			continue
		}
		if uri, ok := item["uri"].(string); ok && uri != "" {
			keys = append(keys, typ+":"+uri)
			continue
		}
		if val, ok := item["value"].(string); ok && val != "" {
			keys = append(keys, typ+":"+val)
			continue
		}
		// Matches neither the current (`uri`) nor legacy (`value`) shape --
		// skip it rather than crash or let it silently vanish into a
		// both-empty comparison.
	}
	return keys, true
}

// candidateScore computes the F3 composite dedup similarity score for one
// existing candidate work item against an incoming create request. It has no
// DB access, so it is directly unit-testable without a live work_items table
// (checkDedup itself needs AIHUB_TEST_DB; the scoring math does not).
//
// The nominal weights are goal 0.6 / labels 0.2 / resources 0.2. Goal
// similarity is always applicable (Goal is a required non-empty field --
// CreateWorkItem rejects an empty goal before dedup ever runs). Labels and
// resources are each applicable UNLESS both sides are empty for that
// dimension (weightedComponent decides this from the raw inputs -- see its
// doc comment). An inapplicable dimension drops its weight entirely rather
// than contributing a hard 0, and the remaining applicable weights are
// renormalized to sum to 1.0, so an absent dimension neither manufactures a
// spurious match (the pre-aihub#251 bug) nor dilutes the score toward zero
// for the common case of a minimal work item with no labels/resources at all
// (aihub#251 review follow-up, mem_veTEPhFm WARN finding).
//
// An unparseable declared_resources payload on either side (reqOK or cOK
// false) is treated as APPLICABLE-but-zero, not inapplicable: malformed JSON
// in a stored column is abnormal/corrupt data, not mere absence of
// information, so it must never be dropped in a way that could inflate the
// score -- it stays a real, weighted 0 (matches the existing "unparseable
// never counts as a match" rule from defect 2).
//
// valid is false if the computed score falls outside [0,1] -- structurally
// this should still be impossible, since sim/labelScore/resScore are each
// bounded to [0,1] and the composite is a weighted average over weights that
// are re-scaled to sum to totalWeight (never zero: goalWeight alone is
// 0.6), but this is a defensive backstop (aihub#251 defect 3): an
// out-of-range score is a programming bug in one of the sub-scores, not
// something that should ever be compared to the 0.90/0.65 thresholds or
// formatted into a user-facing ">100% similar" string. Callers must fail
// OPEN on valid=false (skip this candidate), consistent with the existing
// "dedup is best-effort" philosophy applied when the candidate query itself
// fails.
func candidateScore(req *CreateWorkItemRequest, goal string, labels []string, resources json.RawMessage) (score float64, valid bool) {
	sim := jaccardNGram(req.Goal, goal, 3)

	labelScore, labelWeight := weightedComponent(req.Labels, labels, 0.2)

	reqRes, reqOK := declaredResourceKeys(req.DeclaredResources)
	cRes, cOK := declaredResourceKeys(resources)
	var resScore, resWeight float64
	if !reqOK || !cOK {
		resScore, resWeight = 0, 0.2
	} else {
		resScore, resWeight = weightedComponent(reqRes, cRes, 0.2)
	}

	const goalWeight = 0.6
	totalWeight := goalWeight + labelWeight + resWeight
	if totalWeight <= 0 {
		// Unreachable today: goalWeight alone keeps totalWeight >= 0.6,
		// since Goal is required non-empty. Guarded anyway rather than ever
		// dividing by zero if that invariant is ever relaxed.
		return 0, false
	}

	score = (goalWeight*sim + labelWeight*labelScore + resWeight*resScore) / totalWeight
	if score < 0 || score > 1 {
		return score, false
	}
	return score, true
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

		// C7: include resource overlap (resSim) per design §11 formula.
		// candidateScore is a pure helper (aihub#251) so the scoring math is
		// unit-testable without a live DB; it also carries the setOverlap and
		// declared_resources fixes for defects 1 and 2 (see their doc
		// comments above).
		score, valid := candidateScore(req, c.Goal, c.Labels, c.Resources)
		if !valid {
			// aihub#251 defect 3: an out-of-range score is a programming bug
			// in a sub-score, not a real signal. Fail OPEN — skip scoring
			// this one candidate — consistent with the existing "dedup is
			// best-effort" philosophy applied above when the query itself
			// fails, and never let a >100% (or negative) score reach a
			// threshold comparison or a user-facing message.
			fmt.Fprintf(os.Stderr,
				"aihub: checkDedup: candidate %s produced out-of-range score %.4f, skipping (aihub#251)\n",
				c.Slug, score)
			continue
		}

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
	// aihub#334: pgx's Query is lazy, so a server-side failure while streaming
	// this result set is reachable only here. Dedup is best-effort and stays
	// best-effort for ordinary failures — but a class 40 rollback has already
	// killed the transaction CreateWorkItem is holding, so "allow creation" is
	// not a fallback, it is the caller being told 500 two statements later with
	// the SQLSTATE gone.
	if err := rows.Err(); err != nil {
		if aerr := retryConflictErr(err, "failed to scan dedup candidates"); aerr != nil {
			return aerr
		}
		return nil // Dedup is best-effort, as above.
	}

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
	Status             []string
	WIType             *string
	Priority           *string
	Milestone          *string
	Label              *string
	UserID             *string  // reporter user_id exact match (legacy)
	ReporterDisplay    *string  // case-insensitive contains on wi.reporter_display
	OwnerDisplay       *string  // case-insensitive contains on run_attempts.actor_display (current attempt)
	AccessibleProjects []string // project allow-list for "view all" when project arg is ""
	Source             *string
	Scenario           *string
	// ReadyOnly narrows the set to the ready-queue's items[] segment — see
	// readyOnlyPredicate for the exact definition and why it is that one.
	ReadyOnly bool
	IDs       []string
	Since     *time.Time
	// IncludeStepState attaches each item's wi_step_state row as
	// WorkItem.StepState. Not a filter: it widens each row rather than
	// narrowing the set, so its guard asserts the response key set, never the
	// row count (aihub#280).
	IncludeStepState bool
	// Query is a semantic search over goal+content (aihub#273): pgvector
	// cosine when an embedding provider is active, ILIKE fallback otherwise.
	// Not combinable with Sort/Order/Cursor — the handler rejects those.
	Query  *string
	Limit  int
	Cursor *string
	// Sort selects the ordering column, one of ListWorkItemsSortValues; ""
	// means created_at. Order is "desc" (default) or "asc". Both also key the
	// cursor predicate and the emitted next_cursor — see buildListWorkItemsWhere
	// and listWorkItemsNextCursor. Prefer NormalizeListWorkItemsSort to fill
	// these from caller input; unrecognised values fall back to the defaults
	// rather than reaching the query.
	Sort  string
	Order string
}

// Legal `sort` and `order` values for ListWorkItems (aihub#224).
const (
	ListWorkItemsSortCreatedAt = "created_at"
	ListWorkItemsSortClosedAt  = "closed_at"

	ListWorkItemsOrderDesc = "desc"
	ListWorkItemsOrderAsc  = "asc"
)

// listWorkItemsSortColumns maps each legal `sort` value to its qualified column.
// This map is the *enforced* set: nothing outside it can reach ORDER BY, and the
// published enum below is derived from it so contract and enforcement cannot
// drift (cf. mem_X8JDSC96).
var listWorkItemsSortColumns = map[string]string{
	ListWorkItemsSortCreatedAt: "wi.created_at",
	ListWorkItemsSortClosedAt:  "wi.closed_at",
}

// ListWorkItemsSortValues returns the legal `sort` values in a stable order, for
// callers that publish the enum (the HTTP 400 message, the MCP tool schema).
func ListWorkItemsSortValues() []string {
	return []string{ListWorkItemsSortCreatedAt, ListWorkItemsSortClosedAt}
}

// ListWorkItemsOrderValues returns the legal `order` values in a stable order.
func ListWorkItemsOrderValues() []string {
	return []string{ListWorkItemsOrderDesc, ListWorkItemsOrderAsc}
}

// WorkItemStatusValues returns the legal `status` values, in the order the schema
// declares them.
//
// 🔴 This is a SECOND COPY of a vocabulary whose authority is the database, not
// this file: work_items.status carries
//
//	CHECK (status IN ('queued','running','paused','blocked','wrapped','failed','cancelled'))
//
// from internal/db/migrations/0002_work_items.sql. Copies drift, so this one is
// not trusted — TestWorkItemStatusValuesMatchTheSchemaCheck reads the constraint
// back out of a live database with the migrations applied and fails on any
// divergence in either direction. Add a status in a migration without adding it
// here and the gate is red; the list can never silently go stale.
//
// A Go copy exists at all because the alternative is worse: rejecting an unknown
// `?status=` by asking Postgres would mean the rejection depends on a query, and
// the whole point (aihub#255) is to bound the request BEFORE it reaches the
// database.
func WorkItemStatusValues() []string {
	return []string{"queued", "running", "paused", "blocked", "wrapped", "failed", "cancelled"}
}

// NormalizeListWorkItemsSort validates caller-supplied sort/order and fills in
// the defaults (created_at / desc) for empty input, returning the lowercased
// values to store on the filter.
//
// Empty means "the caller did not ask" and must default, since every pre-aihub#224
// caller sends neither param. A *non-empty* unrecognised value is caller-supplied
// input and gets a hard reject naming the offending value and enumerating the
// legal ones, so the mistake is fixed at its source.
func NormalizeListWorkItemsSort(sort, order string) (string, string, *AihubError) {
	sort = strings.ToLower(strings.TrimSpace(sort))
	order = strings.ToLower(strings.TrimSpace(order))
	if sort == "" {
		sort = ListWorkItemsSortCreatedAt
	}
	if order == "" {
		order = ListWorkItemsOrderDesc
	}
	if _, ok := listWorkItemsSortColumns[sort]; !ok {
		return "", "", NewErr(ErrBadRequest, fmt.Sprintf(
			"invalid sort %q: must be one of %s",
			sort, strings.Join(ListWorkItemsSortValues(), ", ")))
	}
	if order != ListWorkItemsOrderDesc && order != ListWorkItemsOrderAsc {
		return "", "", NewErr(ErrBadRequest, fmt.Sprintf(
			"invalid order %q: must be one of %s",
			order, strings.Join(ListWorkItemsOrderValues(), ", ")))
	}
	return sort, order, nil
}

// listWorkItemsSort resolves a filter's Sort/Order to the qualified ORDER BY
// column, the SQL direction keyword, and the strict cursor comparison operator
// that matches that direction (DESC → `<`, ASC → `>`).
//
// Empty or unrecognised values fall back to the defaults, so the domain layer is
// safe even for a caller that skipped NormalizeListWorkItemsSort — the column
// always comes from listWorkItemsSortColumns and caller text is never
// interpolated into the query.
func listWorkItemsSort(f ListWorkItemsFilter) (col, dir, cursorOp string) {
	col, ok := listWorkItemsSortColumns[strings.ToLower(strings.TrimSpace(f.Sort))]
	if !ok {
		col = listWorkItemsSortColumns[ListWorkItemsSortCreatedAt]
	}
	if strings.EqualFold(strings.TrimSpace(f.Order), ListWorkItemsOrderAsc) {
		return col, "ASC", ">"
	}
	return col, "DESC", "<"
}

// listWorkItemsNextCursor returns the pagination cursor for the last row of a
// page: the value of the column the page was ordered by. Emitting created_at for
// a page ordered by closed_at would make page 2 an arbitrary slice of the table.
//
// A NULL sort value cannot be encoded as a cursor. buildListWorkItemsWhere
// excludes those rows so this is unreachable, but ending pagination is the
// correct degradation — a cursor read off a different column is not.
func listWorkItemsNextCursor(last *WorkItem, sortCol string) *string {
	t := last.CreatedAt
	if sortCol == listWorkItemsSortColumns[ListWorkItemsSortClosedAt] {
		if last.ClosedAt == nil {
			return nil
		}
		t = *last.ClosedAt
	}
	s := t.Format(time.RFC3339Nano)
	return &s
}

// ListWorkItemsResult holds paginated results.
type ListWorkItemsResult struct {
	Items      []*WorkItem `json:"items"`
	NextCursor *string     `json:"next_cursor"`
	// RequestAdjusted names the caller-supplied parameters this endpoint changed
	// on the way in — today only `limit`, which NormalizeListWorkItemsLimit clamps
	// to 200 when it arrives above the ceiling and replaces with 50 when it arrives
	// non-positive (aihub#267). Omitted when nothing was adjusted; see
	// request_adjusted.go for why absence rather than an empty list, and for the
	// one case this cannot report.
	RequestAdjusted []RequestAdjustment `json:"request_adjusted,omitempty"`
}

// ListWorkItems returns a paginated list of work items.
//
// Project scoping:
//   - project != ""              → single project (WHERE wi.project = $project)
//   - project == "" + AccessibleProjects set → scope to those projects
//     (WHERE wi.project = ANY(...)), used by the "view all" UI option for a
//     non-admin caller
//   - project == "" + AccessibleProjects empty → no project clause at all
//     (admin "view all" across every project)
//
// readyOnlyPredicate is *the* SQL definition of "ready", shared by the ready
// queue's items[] segment (GetReadyQueue) and by ListWorkItemsFilter.ReadyOnly.
//
// aihub#280: `ready_only` sat in the published MCP schema for a long time with
// nothing on the server consuming it, which means the decision this constant
// records had never actually been made. It is not a free choice, though — the
// six-segment LCRS view already defines "ready" as the items[] segment: takeable
// right now, by an agent, with nobody having to unblock anything first. That is
// exactly three conditions:
//
//   - status = 'queued'              — not already running/paused/terminal
//   - requires_human_session = false — an agent may take it unattended
//   - no live 'blocks' dependency    — nothing has to land before it
//
// It is one constant rather than two copies specifically because a second copy
// is how `ready_only` would drift into meaning something other than the queue
// it is named after. Combining ready_only with an explicit status filter yields
// the intersection, so `status=running&ready_only=true` correctly returns none.
const readyOnlyPredicate = `(wi.status = 'queued'
		  AND wi.requires_human_session = false
		  AND ` + noLiveBlockerPredicate + `)`

// noLiveBlockerPredicate is the SQL for "nothing has to land before this wi":
// no 'blocks' dependency whose blocker is still open. It assumes the outer query
// aliases work_items as `wi`.
//
// One definition rather than four. Before aihub#280 this subquery was copied
// verbatim into three of GetReadyQueue's segments (items[],
// needs_human_session[], unclassified[]), and adding ready_only to the list
// endpoint would have made a fourth. Those segments differ only in their
// requires_human_session test, so a change to what "blocked" means had to be
// applied identically in every copy or the segments would start disagreeing
// about the same work item — silently, since each query is individually valid.
const noLiveBlockerPredicate = `NOT EXISTS (
		    SELECT 1 FROM wi_dependencies dep
		    JOIN work_items blocker ON dep.blocking_wi_id = blocker.id
		    WHERE dep.blocked_wi_id = wi.id
		      AND dep.kind = 'blocks'
		      AND blocker.status NOT IN ('wrapped','cancelled','failed')
		  )`

// buildListWorkItemsWhere builds the WHERE clause, JOIN clause, and ordered
// bound args for ListWorkItems from the given project scope and filter. It is
// split out from ListWorkItems so the query construction (notably placeholder
// numbering and the cursor predicate) is unit-testable without a live DB.
func buildListWorkItemsWhere(project string, f ListWorkItemsFilter) (joinClause, where string, args []any) {
	args = []any{}
	conds := []string{}
	argIdx := 1
	if project != "" {
		conds = append(conds, fmt.Sprintf("wi.project = $%d", argIdx))
		args = append(args, project)
		argIdx++
	} else if len(f.AccessibleProjects) > 0 {
		conds = append(conds, fmt.Sprintf("wi.project = ANY($%d)", argIdx))
		args = append(args, f.AccessibleProjects)
		argIdx++
	}

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
	if f.Milestone != nil {
		conds = append(conds, fmt.Sprintf("wi.milestone = $%d", argIdx))
		args = append(args, *f.Milestone)
		argIdx++
	}
	if f.Source != nil {
		conds = append(conds, fmt.Sprintf("wi.source = $%d", argIdx))
		args = append(args, *f.Source)
		argIdx++
	}
	if f.Scenario != nil {
		conds = append(conds, fmt.Sprintf("wi.scenario = $%d", argIdx))
		args = append(args, *f.Scenario)
		argIdx++
	}
	if f.Label != nil {
		conds = append(conds, fmt.Sprintf("$%d = ANY(wi.labels)", argIdx))
		args = append(args, *f.Label)
		argIdx++
	}
	// ReadyOnly binds no args, so it deliberately does not bump argIdx.
	if f.ReadyOnly {
		conds = append(conds, readyOnlyPredicate)
	}
	if f.UserID != nil {
		conds = append(conds, fmt.Sprintf("wi.reporter_user_id = $%d", argIdx))
		args = append(args, *f.UserID)
		argIdx++
	}
	if f.ReporterDisplay != nil && *f.ReporterDisplay != "" {
		conds = append(conds, fmt.Sprintf("wi.reporter_display ILIKE '%%' || $%d || '%%'", argIdx))
		args = append(args, *f.ReporterDisplay)
		argIdx++
	}
	// OwnerDisplay needs a JOIN to run_attempts. Only inject the join when
	// the filter is requested so the no-filter path stays at zero extra cost.
	if f.OwnerDisplay != nil && *f.OwnerDisplay != "" {
		joinClause = " LEFT JOIN run_attempts ra ON ra.id = wi.current_attempt_id"
		conds = append(conds, fmt.Sprintf("ra.actor_display ILIKE '%%' || $%d || '%%'", argIdx))
		args = append(args, *f.OwnerDisplay)
		argIdx++
	}
	// Slugs as well as ids, because the MCP schema publishes "IDs or slugs" and a
	// published capability the SQL does not implement is exactly this wi's defect
	// class: `ids=["aihub#280"]` returned {"items":[]} with HTTP 200 and no error
	// — indistinguishable from "no such work item". GetWorkItem has always
	// accepted either spelling; the list path had not. One bound arg referenced
	// twice, the same shape as the Query predicate below (aihub#280).
	if len(f.IDs) > 0 {
		conds = append(conds, fmt.Sprintf("(wi.id = ANY($%d) OR wi.slug = ANY($%d))", argIdx, argIdx))
		args = append(args, f.IDs)
		argIdx++
	}
	if f.Since != nil {
		conds = append(conds, fmt.Sprintf("wi.created_at >= $%d", argIdx))
		args = append(args, *f.Since)
		argIdx++
	}
	// aihub#273 text path: substring match over goal+content. The vector path
	// (listWorkItemsByVector) clears Query before calling this builder, so the
	// guard only ever applies to the fallback.
	if f.Query != nil && *f.Query != "" {
		conds = append(conds, fmt.Sprintf(
			"(wi.goal ILIKE '%%' || $%d || '%%' OR wi.content ILIKE '%%' || $%d || '%%')", argIdx, argIdx))
		args = append(args, *f.Query)
		argIdx++
	}
	sortCol, _, cursorOp := listWorkItemsSort(f)
	// sort=closed_at restricts the set to rows that HAVE a close time. Two
	// reasons, both load-bearing:
	//   1. A NULL sort key is unreachable by any cursor — `closed_at < $n` is
	//      never true for NULL — so open wis would silently vanish after page 1.
	//      Excluding them up front keeps the ordering total and the cursor exact.
	//   2. It loses nothing for the terminal statuses this sort is for: the
	//      trg_wi_closed_at trigger (migration 0002) stamps closed_at on every
	//      transition into wrapped/failed/cancelled, and INSERT always starts at
	//      'queued', so status-terminal implies closed_at IS NOT NULL. Verified
	//      against live data at aihub#224: 0 of 200 terminal aihub wis had a NULL.
	// It also makes the query eligible for the partial index idx_wi_closed.
	// Callers that want open items must sort by created_at (the default).
	if sortCol == listWorkItemsSortColumns[ListWorkItemsSortClosedAt] {
		conds = append(conds, "wi.closed_at IS NOT NULL")
	}
	// Cursor pagination: NextCursor is the last returned item's *sort column*
	// value (RFC3339Nano), so the predicate must be on that same column, with a
	// strict comparison following the sort direction (DESC → `<`, ASC → `>`).
	// Mirrors ListEvents in memory.go (strict comparison, ::timestamptz cast, no
	// secondary tie-breaker).
	if f.Cursor != nil && *f.Cursor != "" {
		conds = append(conds, fmt.Sprintf("%s %s $%d::timestamptz", sortCol, cursorOp, argIdx))
		args = append(args, *f.Cursor)
		argIdx++
	}
	// Every clause above that BINDS AN ARG bumps argIdx uniformly, so clauses can
	// be reordered or extended without re-introducing a placeholder-numbering bug
	// (cf. aihub#147). The arg-free clauses — the ReadyOnly predicate and the
	// sort=closed_at NOT NULL guard — deliberately do not bump it, which is what
	// keeps argIdx == len(args)+1. listWorkItemsByVector relies on exactly that
	// invariant to place its own placeholders after these (wi_vector.go).
	// The final bump is otherwise unread; sink it so ineffassign stays happy.
	_ = argIdx

	where = ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	return joinClause, where, args
}

// buildListWorkItemsQuery assembles the full SELECT for ListWorkItems, returning
// it alongside the bound args and the sort column the page is ordered by (which
// listWorkItemsNextCursor needs to emit the right cursor).
//
// Split out from ListWorkItems for the same reason buildListWorkItemsWhere was:
// the domain suite is pure-unit, so the assembled statement — clause order, the
// ORDER BY built from two interpolated fragments, the LIMIT — has no coverage
// unless it can be inspected without a live pool (cf. gc_test pinning the sweep
// SQL). Assumes f.Limit is already clamped.
func buildListWorkItemsQuery(project string, f ListWorkItemsFilter) (query string, args []any, sortCol string) {
	// The column and direction come from listWorkItemsSortColumns, never from
	// caller text, so this stays injection-safe despite the Sprintf.
	sortCol, sortDir, _ := listWorkItemsSort(f)

	joinClause, where, args := buildListWorkItemsWhere(project, f)
	query = fmt.Sprintf(`
		SELECT wi.id, wi.seq, wi.slug, wi.project, wi.scenario, wi.goal, wi.source,
			   wi.wi_type, wi.priority, wi.requires_human_session, wi.milestone, wi.labels,
			   wi.status, wi.declared_resources, wi.resources_version,
			   wi.external_share_type, wi.external_share_key,
			   wi.reporter_user_id, wi.reporter_display,
			   wi.current_attempt_id, wi.current_attempt_epoch,
			   wi.parent_work_item_id, wi.attrs, wi.created_at, wi.updated_at, wi.closed_at
		FROM work_items wi%s
		%s
		ORDER BY %s %s
		LIMIT %d`, joinClause, where, sortCol, sortDir, f.Limit+1)

	return query, args, sortCol
}

// Page-size bounds for ListWorkItems.
//
// INVARIANT: ListWorkItemsLimitCeiling >= ListWorkItemsLimitDefault, for the
// reason spelled out on recallTopKCeiling — a ceiling below the default inverts
// the endpoint, so asking for a bigger page returns fewer items.
//
// Exported only so the HTTP handler can seed filter.Limit with the same default
// the domain would apply; no test may derive an expectation from either (the
// tests spell 50 and 200 out), because a fixture read off the constant under
// test moves with the defect instead of catching it.
const (
	ListWorkItemsLimitDefault = 50
	ListWorkItemsLimitCeiling = 200
)

// NormalizeListWorkItemsLimit resolves a requested page size into the one
// ListWorkItems uses. It is the recall side's normalizeRecallTopK, deliberately
// down to its shape (aihub#267).
//
// 🔴 What changed here: a request ABOVE the ceiling now yields the CEILING, where
// it used to yield the default of 50. The two halves of one API had opposite
// answers to the same question — `top_k=300` on GET /v1/memories returned 200
// items while `limit=300` on GET /v1/work_items returned 50 — and 50 is the worse
// of the two in the direction that costs something: the caller asked for more
// than the endpoint will give and got less than it would have given.
//
// That is not hypothetical. An audit of two aihub instances on 2026-08-27
// enumerated work items with limit=5000, received a silently truncated 308, and
// concluded 11 work items existed on one instance only. Correct pagination
// showed the true figure was zero. The retraction is in aihub#267; the number was
// wrong in the direction that would have driven the opposite operational call.
//
// A NON-POSITIVE request is neither malformed nor out of range: it means the
// caller named no page size and yields the default. That is normalizeRecallTopK's
// rule, and the aihub#249 contract behind it — bad input falls back to the
// DEFAULT, never to a smaller page.
//
// This is the ONLY place a work-item page size may be bounded. A cap applied
// upstream is invisible from here, so nothing can hold it to the invariant above;
// that is exactly how aihub#309 made the recall ceiling unreachable, and why
// handleListWorkItems now forwards whatever it parsed, negatives included.
func NormalizeListWorkItemsLimit(requested int) int {
	if requested <= 0 {
		return ListWorkItemsLimitDefault
	}
	if requested > ListWorkItemsLimitCeiling {
		return ListWorkItemsLimitCeiling
	}
	return requested
}

// ListWorkItems bounds the caller's page size, runs the query, and DISCLOSES the
// bound if it fired.
//
// 🔴 The disclosure is attached HERE, wrapped around the routing, and not at the
// two `return` statements inside listWorkItemsPage. Those are the vector path and
// the text path, and annotating both is two chances to forget one — which is
// precisely how aihub#280's include_step_state worked on every query except a
// semantically-matched one. The same reasoning put handleRecall's unmatched_types
// outside domain.Recall rather than at its four exits.
func ListWorkItems(ctx context.Context, pool *pgxpool.Pool, project string, f ListWorkItemsFilter) (*ListWorkItemsResult, *AihubError) {
	requestedLimit := f.Limit
	f.Limit = NormalizeListWorkItemsLimit(f.Limit)
	res, err := listWorkItemsPage(ctx, pool, project, f)
	if err != nil || res == nil {
		return res, err
	}
	res.RequestAdjusted = appendIntAdjustment(res.RequestAdjusted, "limit", requestedLimit, f.Limit)
	return res, nil
}

// listWorkItemsPage is ListWorkItems with the page size already bounded. Split
// out so the disclosure above covers every way this can return.
func listWorkItemsPage(ctx context.Context, pool *pgxpool.Pool, project string, f ListWorkItemsFilter) (*ListWorkItemsResult, *AihubError) {
	// aihub#273: semantic path first when the caller sent query= and an
	// embedding provider is active. Any error or an empty result falls through
	// to the ILIKE text path below (via buildListWorkItemsWhere's Query guard)
	// — the aihub#270 lesson: a non-empty vector shortcut must never make
	// unembedded rows structurally unreachable, and pre-backfill "0 embedded"
	// must never read as "no matches".
	if f.Query != nil && strings.TrimSpace(*f.Query) != "" && !isNoopProvider(embProvider) {
		res, vecErr := listWorkItemsByVector(ctx, pool, project, f)
		switch {
		case vecErr != nil:
			fmt.Fprintf(os.Stderr, "list work_items: vector path failed, falling through to text path: %v\n", vecErr)
		case len(res.Items) == 0:
			// nothing embedded matched — fall through to ILIKE
		default:
			// The vector path returns before the text path's enrichment, so it
			// needs its own call — otherwise include_step_state would work for
			// every query except a semantically-matched one (aihub#280).
			if f.IncludeStepState {
				attachStepState(ctx, pool, res.Items)
			}
			return res, nil
		}
	}

	query, args, sortCol := buildListWorkItemsQuery(project, f)

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
		result.NextCursor = listWorkItemsNextCursor(items[len(items)-1], sortCol)
	}
	result.Items = items
	if result.Items == nil {
		result.Items = []*WorkItem{}
	}
	// After truncation to f.Limit, so the extra look-ahead row never costs a
	// step-state lookup.
	if f.IncludeStepState {
		attachStepState(ctx, pool, result.Items)
	}
	return result, nil
}

// attachStepState fills in WorkItem.StepState for every item that has a
// wi_step_state row, in one round trip for the whole page (aihub#280).
//
// Best-effort by design: a failure here leaves StepState nil, which is the same
// shape as "this wi was never claimed". The alternative — failing the whole list
// — would make `include_step_state=true` able to break a call that works without
// it, and the caller (pf-status, pf-retro) needs the wi fields far more than the
// step fields.
func attachStepState(ctx context.Context, pool *pgxpool.Pool, items []*WorkItem) {
	if len(items) == 0 {
		return
	}
	byID := make(map[string]*WorkItem, len(items))
	ids := make([]string, 0, len(items))
	for _, wi := range items {
		byID[wi.ID] = wi
		ids = append(ids, wi.ID)
	}
	rows, err := pool.Query(ctx, `
		SELECT work_item_id, wi_type, graph_source, current_step, current_step_status,
		       current_step_attempt, step_started_at, version, updated_at
		FROM wi_step_state
		WHERE work_item_id = ANY($1)`, ids)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list work_items: include_step_state lookup failed: %v\n", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var wiID string
		var st WorkItemStepState
		if scanErr := rows.Scan(&wiID, &st.WIType, &st.GraphSource, &st.CurrentStep,
			&st.CurrentStepStatus, &st.CurrentStepAttempt, &st.StepStartedAt,
			&st.Version, &st.UpdatedAt); scanErr != nil {
			fmt.Fprintf(os.Stderr, "list work_items: include_step_state scan failed: %v\n", scanErr)
			continue
		}
		if wi, ok := byID[wiID]; ok {
			state := st
			wi.StepState = &state
		}
	}
	// A mid-stream read failure would otherwise truncate the enrichment with no
	// signal at all — the same silence the two paths above deliberately log.
	if err := rows.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "list work_items: include_step_state rows failed: %v\n", err)
	}
}

// workItemUpdate is the compiled UPDATE statement for UpdateWorkItem.
//
// It is produced by buildWorkItemUpdate, a pure function, so the two aihub#241
// invariants below have coverage that actually executes. UpdateWorkItem itself
// reads the database before it reaches this logic, so a behavioural test of it
// would have to be DB-gated — and a DB-gated test in this repo runs nowhere
// (AIHUB_TEST_DB is unset locally and on CI's "Unit tests" step), so it would
// SKIP while reading as coverage (mem_I98xpPgY). Same technique as cancelGate
// in aihub#242.
type workItemUpdate struct {
	Query string
	Args  []any
	// CAS is true when the WHERE clause carries a resources_version predicate,
	// i.e. the caller asked for compare-and-set. RowsAffected()==0 is then a
	// version conflict rather than a missing row.
	CAS bool
}

// buildWorkItemUpdate compiles the SET/WHERE clauses for UpdateWorkItem.
//
// aihub#241 fixes two defects that lived here:
//
//   - The counter never advanced. resources_version was written only when the
//     caller supplied one, as `= <caller value> + 1`; the ordinary path (no
//     version passed) left it at its old value forever. Every caller therefore
//     read 0, so even a working CAS could never detect a conflict. It is now
//     incremented in the database — `resources_version = resources_version + 1`
//     — on every write of declared_resources, independent of what the caller
//     sent, so concurrent writers cannot both compute the same next value.
//
//   - There was no compare-and-set at all. Passing resources_version changed
//     what got stored but added no predicate, so a stale writer silently
//     overwrote a fresher one. The version is now a WHERE precondition.
//
// The two are deliberately orthogonal: the increment is keyed on
// declared_resources being written, the precondition on the caller supplying a
// version. Passing resources_version alone is a plain guard ("only apply this
// patch if nobody has touched declared_resources since I read it") and is
// never wrong; omitting it keeps the historical unconditional behaviour, which
// callers depend on today.
//
// # aihub#288: the attrs merge path
//
// `attrs` was — and still is — a whole-column REPLACE: `attrs = $n` drops every
// key the caller did not resend. That is not a hypothetical. On 2026-08-30 an
// update carrying 2 keys destroyed 3 unrelated keys on aihub#284, with no
// error and no conflict, because two agents were annotating the same work item
// and the second one had only ever seen its own two keys.
//
// The fix is additive, not a change of meaning:
//
//   - `attrs` keeps REPLACE, byte for byte. Changing its default to merge would
//     silently rewrite the behaviour of every caller that exists today (the Web
//     UI, pkg/client, the MCP tool, anything scripted against the REST API) and
//     would remove the only way to delete a key. A silent semantic change is a
//     worse defect than the one being fixed.
//   - `attrs_patch` is the opt-in merge: `attrs = attrs || $n::jsonb`. Shallow,
//     i.e. a top-level key present in the patch replaces the stored value for
//     that key outright and is NOT merged into it recursively. `null` in the
//     patch therefore STORES a JSON null; it does not delete.
//   - `attrs_unset` deletes top-level keys: `attrs = attrs - $n::text[]`.
//     Deletion has to be spelled out precisely because the merge is shallow —
//     without it the merge path would be a capability regression against
//     "resend the whole object".
//
// Shallow, not RFC 7396 deep merge: the only way to spell RFC 7396 in plain SQL
// is `jsonb_strip_nulls(attrs || $n)`, which strips nulls recursively out of the
// WHOLE merged document — so an unrelated patch would silently delete a
// pre-existing key whose value happened to be null. Explicit `attrs_unset` costs
// one parameter and has no such blast radius.
//
// Both may be sent together; the merge is applied first and the unset second
// (composed inside one SET expression), so a key named in both ends up deleted.
// `attrs` together with `attrs_patch`/`attrs_unset` is rejected as a 400 by
// UpdateWorkItem — the two are contradictory instructions for one column, and
// silently picking a winner is how this class of bug starts.
func buildWorkItemUpdate(req *UpdateWorkItemRequest, wiID string) workItemUpdate {
	setClauses := []string{"updated_at = clock_timestamp()"}
	args := []any{}
	argIdx := 1

	add := func(clause string, val any) {
		setClauses = append(setClauses, fmt.Sprintf(clause, argIdx))
		args = append(args, val)
		argIdx++
	}

	if req.Priority != nil {
		add("priority = $%d", *req.Priority)
	}
	if req.Milestone != nil {
		add("milestone = $%d", *req.Milestone)
	}
	if req.WIType != nil {
		add("wi_type = $%d", *req.WIType)
	}
	if req.RequiresHumanSession != nil {
		add("requires_human_session = $%d", *req.RequiresHumanSession)
	}
	if req.Labels != nil {
		add("labels = $%d", req.Labels)
	}
	if req.DeclaredResources != nil {
		add("declared_resources = $%d", req.DeclaredResources)
		// Computed by Postgres from the stored value, not from anything the
		// caller sent — that is what makes it a usable CAS counter.
		setClauses = append(setClauses, "resources_version = resources_version + 1")
	}
	if req.Attrs != nil {
		add("attrs = $%d", req.Attrs)
	}
	// aihub#288: the non-destructive path, composed into ONE SET expression so
	// merge-then-delete happens in a single statement (two SET clauses for the
	// same column is a syntax error in Postgres, and two statements would
	// reintroduce a window where a concurrent reader sees the half-applied
	// state).
	//
	// The parentheses are load-bearing. Postgres assigns precedence by operator
	// SPELLING, not by operand type: binary `-` sits at the addition/subtraction
	// level, which binds TIGHTER than `||` ("any other operator"). Unparenthesised,
	// `attrs || $1::jsonb - $2::text[]` parses as `attrs || ($1::jsonb - $2::text[])`
	// — the keys would be stripped out of the incoming patch and nothing would be
	// removed from the stored value, with no error at all. Verified on PG 18.4:
	// `'{"a":1,"gone":9}'::jsonb || '{"b":2}'::jsonb - ARRAY['gone']::text[]` keeps
	// "gone". TestBuildWorkItemUpdate_MergeThenUnsetIsParenthesised locks this.
	if req.AttrsPatch != nil || req.AttrsUnset != nil {
		expr := attrsAsObject
		if req.AttrsPatch != nil {
			expr = fmt.Sprintf("(%s || $%d::jsonb)", expr, argIdx)
			args = append(args, req.AttrsPatch)
			argIdx++
		}
		if req.AttrsUnset != nil {
			expr = fmt.Sprintf("(%s - $%d::text[])", expr, argIdx)
			args = append(args, req.AttrsUnset)
			argIdx++
		}
		setClauses = append(setClauses, "attrs = "+expr)
	}
	if req.Goal != nil {
		add("goal = $%d", *req.Goal)
	}
	if req.Content != nil {
		add("content = $%d", *req.Content)
	}

	whereClauses := []string{fmt.Sprintf("id = $%d", argIdx)}
	args = append(args, wiID)
	argIdx++

	cas := req.ResourcesVersion != nil
	if cas {
		whereClauses = append(whereClauses, fmt.Sprintf("resources_version = $%d", argIdx))
		args = append(args, *req.ResourcesVersion)
	}

	return workItemUpdate{
		Query: fmt.Sprintf("UPDATE work_items SET %s WHERE %s",
			strings.Join(setClauses, ", "), strings.Join(whereClauses, " AND ")),
		Args: args,
		CAS:  cas,
	}
}

// attrsAsObject is the left operand of the aihub#288 merge: the stored attrs,
// coerced to an object if it somehow is not one.
//
// The column is `JSONB NOT NULL DEFAULT '{}'` and every reader unmarshals it
// into a map, but nothing enforces that it holds an OBJECT — `attrs` REPLACE
// takes any JSON, and JSON null is not SQL NULL, so `{"attrs":null}` stores a
// jsonb null past the NOT NULL constraint. Merging onto such a value is not a
// no-op, it is corruption, and both halves misbehave differently (verified on
// PG 18.4):
//
//	'null'::jsonb  || '{"a":1}'::jsonb        -> [null, {"a": 1}]      -- silent
//	'[1,2]'::jsonb || '{"a":1}'::jsonb        -> [1, 2, {"a": 1}]      -- silent
//	'null'::jsonb  -  ARRAY['a']::text[]      -> ERROR: cannot delete from scalar
//
// So a row that is already outside the column's intended type would either turn
// into an array nobody can read or 500 the request. Coercing to '{}' keeps the
// type invariant the rest of the codebase assumes; a caller who wants the old
// value back can still overwrite it wholesale with `attrs`.
const attrsAsObject = `CASE WHEN jsonb_typeof(attrs) = 'object' THEN attrs ELSE '{}'::jsonb END`

// normalizeAttrsPatch treats a literal JSON `null`, and an empty attrs_unset, as
// "field not supplied" (aihub#288).
//
// json.RawMessage keeps the four bytes `null` rather than staying nil, so
// `{"attrs":{…},"attrs_patch":null}` — a request that carries no patch at all —
// would otherwise trip the "cannot be combined" check and come back as a 400
// describing something the caller did not do. Clients that null-fill optional
// parameters are common enough that this must not be an error, and attrs_patch
// is a brand-new field, so nothing can be relying on it rejecting null: being
// permissive here cannot regress a caller, being strict can.
//
// An empty attrs_unset is normalised for a smaller reason — `attrs - '{}'::text[]`
// is a self-assignment, and a caller who asked for nothing should not write the
// column at all.
func normalizeAttrsPatch(req *UpdateWorkItemRequest) {
	if req.AttrsPatch != nil && bytes.Equal(bytes.TrimSpace(req.AttrsPatch), []byte("null")) {
		req.AttrsPatch = nil
	}
	if req.AttrsUnset != nil && len(req.AttrsUnset) == 0 {
		req.AttrsUnset = nil
	}
}

// normalizeDeclaredResources treats an explicit JSON `null` for
// declared_resources as "not specified" (aihub#264).
//
// A separate one-line function rather than a branch inside UpdateWorkItem for
// the reason cancelGate and isCASConflict are separate: the only behavioural
// test of a check living inline in UpdateWorkItem would be DB-gated, and a
// DB-gated test here runs only in its own scoped CI step, so deleting the check
// would leave `go test ./...` entirely green.
//
// json.RawMessage preserves `"declared_resources": null` as the four literal
// bytes, so `!= nil` is true and the field counted as part of the patch.
// ValidateDeclaredResources returns early for "null" without complaint, so the
// column was overwritten with a jsonb null: every declaration silently
// destroyed, resources_version bumped, HTTP 200.
//
// That was already wrong before this work item, and the lock release makes it
// materially worse — a null payload derives an EMPTY key set, so it would also
// drop every file_scope lock the work item holds. A caller sending null meaning
// "leave this alone" would lose its declarations AND its write protection in one
// call with nothing to notice, which is the exact silent-loss-of-protection
// shape aihub#264 is about. Measured before the fold was added: the update
// returned 200, stored `null`, and left the attempt holding no locks.
//
// Folded to "not specified", matching normalizeAttrsPatch. Clearing declarations
// still has a spelling, and it is the one the schema documents: an empty array.
func normalizeDeclaredResources(req *UpdateWorkItemRequest) {
	if req.DeclaredResources != nil && bytes.Equal(bytes.TrimSpace(req.DeclaredResources), []byte("null")) {
		req.DeclaredResources = nil
	}
}

// validateAttrsPatch rejects the two attrs payloads that must never reach
// Postgres (aihub#288). Run normalizeAttrsPatch first.
//
// Extracted as a pure function for the same reason isCASConflict was: the only
// behavioural test of a check living inline in UpdateWorkItem would be DB-gated,
// and a DB-gated test in this repo runs only in its own scoped CI step — deleting
// the check would leave `go test ./...` entirely green (mem_I98xpPgY).
//
//  1. An `attrs_patch` that is not a JSON object. `jsonb || jsonb` accepts
//     arrays and scalars but does something quite different with them —
//     `'{"a":1}'::jsonb || '[1]'::jsonb` yields an ARRAY, not an object — and
//     malformed JSON would surface as a 500 from the driver. A caller error
//     must be a 400. Checked BEFORE the combination rule below so a bad patch is
//     reported as a bad patch rather than as a conflict.
//
//  2. `attrs` together with `attrs_patch`/`attrs_unset`. They are contradictory
//     instructions for one column — REPLACE everything vs. keep everything and
//     amend it. Applying both would make the result depend on clause order,
//     which is exactly the kind of silent, order-dependent outcome this work
//     item exists to remove. Fail loudly instead.
func validateAttrsPatch(req *UpdateWorkItemRequest) *AihubError {
	if req.AttrsPatch != nil {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(req.AttrsPatch, &probe); err != nil || probe == nil {
			return NewErr(ErrBadRequest, "attrs_patch must be a JSON object")
		}
	}
	if req.Attrs != nil && (req.AttrsPatch != nil || req.AttrsUnset != nil) {
		return NewErr(ErrBadRequest,
			"attrs cannot be combined with attrs_patch/attrs_unset: attrs REPLACES the whole object, attrs_patch/attrs_unset amend it — send one or the other")
	}
	return nil
}

// casVersionUnknown is the placeholder reported when the current
// resources_version could not be re-read after a failed compare-and-set.
const casVersionUnknown = -1

// isCASConflict reports whether a completed UPDATE must be rejected as a
// compare-and-set conflict (aihub#241).
//
// Split out so the decision has coverage that actually executes: review of this
// change found that deleting the branch in UpdateWorkItem left `go test ./...`
// entirely green, because the only behavioural check of it lives in the
// DB-gated suite that SKIPs everywhere except its own scoped CI step
// (mem_I98xpPgY). The branch is a one-liner; the failure it would let through
// is not — a failed CAS silently returning 200 is exactly the silent
// unprotected overwrite this work item exists to remove.
//
// Zero rows is only meaningful when the caller asked for CAS. Without a
// version the WHERE clause is `id = $n` alone, and a work item whose row
// vanished between GetWorkItem and here is a 404, not a conflict — the caller
// of this helper distinguishes those two.
func isCASConflict(cas bool, rowsAffected int64) bool {
	return cas && rowsAffected == 0
}

// casConflictErr builds the 409 for a failed compare-and-set. Never a 400: the
// caller's payload was well-formed, someone else simply wrote
// declared_resources first. `current` may be casVersionUnknown when the re-read
// failed, which must not stop the conflict from being reported.
func casConflictErr(expected, current int) *AihubError {
	currentText := strconv.Itoa(current)
	if current == casVersionUnknown {
		currentText = "unknown"
	}
	return NewErrDetails(ErrConflictCASFailed,
		fmt.Sprintf("declared_resources CAS failed: resources_version is %s, not the expected %d — reread the work item and retry with its current resources_version", currentText, expected),
		map[string]any{
			"expected_resources_version": expected,
			"current_resources_version":  current,
		})
}

// releaseUndeclaredLocksSQL drops the file_scope locks a narrowing orphaned.
//
// Scoped by work_item_id through run_attempts rather than by a single attempt id
// on purpose: the reported instance (ieops#798) was on claim epoch 7, and each
// re-claim mints a new attempt row, so residue from an earlier epoch is owned by
// an attempt that is no longer current. Joining on work_item_id reaches that
// residue while still making it impossible to touch a lock belonging to any
// OTHER work item, whatever its key looks like.
const releaseUndeclaredLocksSQL = `
	DELETE FROM resource_locks rl
	USING run_attempts ra
	WHERE rl.owner_attempt_id = ra.id
	  AND ra.work_item_id = $1
	  AND rl.resource_type = 'file_scope'
	  AND rl.resource_key = ANY($2::text[])`

// releaseUndeclaredFileScopeLocks releases the file_scope locks that `prior`
// justified and `next` no longer does (aihub#264).
//
// 🔴 PREVENTION, NOT CLEANUP — and the reported lock is not in scope.
//
// The candidate set is `prior − next`, so only a key present in the declaration
// this update REPLACES can be released. ieops#798's leaked lock came from a
// declaration dropped several resources_versions before the one it now stores,
// so it is in neither side and no future update of that work item will release
// it. This change stops new residue accruing; it does not sweep residue that
// already exists. The recoveries for an already-leaked lock are to re-declare
// the path and remove it again (one narrowing, now effective), to end the
// attempt, or to let the orphan sweep in gc.go take it once the owning attempt
// is no longer live.
//
// Widening the candidate set to "every file_scope lock this work item's attempts
// hold, minus next" WOULD clear that residue, and was deliberately not taken:
// it would also release locks from a client-supplied requested_locks that never
// had a declaration behind them (run_attempts.go:325-332 — the raw-API path the
// plugin never uses, but which is trusted verbatim when present). That is a
// bigger behaviour change than this item asked for, and it belongs with a
// decision about whether an undeclared lock should be able to exist at all.
//
// # Why this hangs off the update path
//
// Acquisition lives on the claim path and mutation lives here, and nothing
// connected them — which is the whole defect. It is fixed at the moment the
// declaration changes rather than when a blocked caller asks, deliberately: a
// holder can be between two declaration updates, so deciding "not in the current
// declarations, therefore free" at QUERY time would hand away a lock the holder
// is about to re-declare. Releasing at write time has no such window, and it
// keeps resources_version and the lock set moving together — this runs inside the
// same transaction as the UPDATE and after the CAS check, so a rejected update
// releases nothing and there is never a committed state where the version did not
// advance but the locks changed.
//
// # Why file_scope only
//
// Not an oversight, and not simply "that is what was reported" — it is a
// reversibility argument. FnAcquireLocks re-acquires file_scope and nothing else,
// so every lock this function can release has a documented way to be taken back
// within the same attempt, and a narrowing made by mistake costs one
// pf_acquire_locks call. git_branch and deploy_env have no such path: they are
// taken at claim and re-derived only on the next claim/resume, so releasing one
// here would leave the work item unprotected until then, and would let a second
// attempt take a branch this one still has checked out. That is the same hazard
// derivedLock's comment already refuses to open for intent=read, and it is not
// worth opening for a narrowing either. A work item that really means to give up
// a branch can pause, which already releases file_scope and re-derives the rest.
func releaseUndeclaredFileScopeLocks(ctx context.Context, tx pgx.Tx, wiID, project string, prior, next json.RawMessage) *AihubError {
	priorKeys, ok := derivedFileScopeLockKeys(prior, project)
	if !ok {
		// Unparseable stored declarations: which locks they produced is unknown,
		// so releasing any of them would be a guess. Leaving them is the
		// pre-aihub#264 behaviour and is the safe direction.
		return nil
	}
	nextKeys, ok := derivedFileScopeLockKeys(next, project)
	if !ok {
		// Unreachable from the REST/MCP surface: UpdateWorkItem runs
		// ValidateDeclaredResources on caller input before opening the
		// transaction. Guarded rather than asserted, because "release everything
		// the old payload had" is the wrong answer to a payload we cannot read.
		return nil
	}

	removed := make([]string, 0, len(priorKeys))
	for key := range priorKeys {
		if !nextKeys[key] {
			removed = append(removed, key)
		}
	}
	if len(removed) == 0 {
		return nil
	}
	// Sorted so the statement's parameters are deterministic and a failure is
	// reproducible. That is the whole claim: `resource_key = ANY($2)` does not
	// scan or row-lock in array order, so this does NOT influence lock ordering,
	// and it does not need to — two different work items can never target
	// overlapping rows here, so these deletes cannot deadlock against each other.
	sort.Strings(removed)

	// dbErrCause already maps a class 40 rollback to a retryable 409 before
	// falling back to ErrInternalError (aihub#334), and ErrInternalError is
	// exactly the non-conflict outcome wanted here, so it is the whole mapping.
	if _, err := tx.Exec(ctx, releaseUndeclaredLocksSQL, wiID, removed); err != nil {
		return dbErrCause(err, "failed to release locks for removed declared_resources")
	}
	return nil
}

// UpdateWorkItem applies a patch to a work item.
func UpdateWorkItem(ctx context.Context, pool *pgxpool.Pool, idOrSlug string, callerUserID, callerRole string, callerProjectRoles map[string]string, req *UpdateWorkItemRequest) (*WorkItem, *AihubError) {
	wi, aihubErr := GetWorkItem(ctx, pool, idOrSlug)
	if aihubErr != nil {
		return nil, aihubErr
	}

	// aihub#264: fold an explicit null away BEFORE anything reads the field, so
	// validation, the UPDATE and the lock release all agree it was not supplied.
	normalizeDeclaredResources(req)

	// aihub#238: same entry-point validation as CreateWorkItem — an update must
	// not be able to replace good declared_resources with silently lockless ones.
	if req.DeclaredResources != nil {
		if vErr := ValidateDeclaredResources(req.DeclaredResources); vErr != nil {
			return nil, vErr
		}
	}

	// aihub#288: fold null-filled optionals away, then reject contradictory or
	// malformed attrs instructions — both before any write happens.
	normalizeAttrsPatch(req)
	if vErr := validateAttrsPatch(req); vErr != nil {
		return nil, vErr
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

		// scenario_phase_configs has been removed; wi_type is accepted as-is from the caller.
		// requires_human_session must be provided by the caller when reclassifying.
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, NewErr(ErrInternalError, "failed to begin transaction")
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if req.Content != nil {
		// Content may be updated in any non-terminal status
		nonTerminal := wi.Status == "queued" || wi.Status == "paused" || wi.Status == "running" || wi.Status == "blocked"
		if !nonTerminal {
			return nil, NewErr(ErrConflictTerminalState, fmt.Sprintf("cannot update content when work item is in terminal state: %s", wi.Status))
		}
	}

	// aihub#264: read the declaration this update is about to replace, inside the
	// transaction and FOR UPDATE, so the diff below is computed against the value
	// the UPDATE actually overwrites.
	//
	// The GetWorkItem at the top of this function ran on the pool before the
	// transaction opened, so wi.DeclaredResources is a pre-transaction read and
	// using it would leave a window in which a concurrent narrowing's locks are
	// resurrected or a concurrent widening's are dropped. FOR UPDATE also matches
	// FnAcquireLocks, which takes the same row lock before touching
	// resource_locks — same order (work_items then resource_locks) in both, so the
	// two cannot deadlock against each other. Taken only when declared_resources
	// is part of the patch, so no other update path changes its locking.
	var priorDeclared json.RawMessage
	if req.DeclaredResources != nil {
		if scanErr := tx.QueryRow(ctx,
			`SELECT declared_resources FROM work_items WHERE id = $1 FOR UPDATE`, wi.ID,
		).Scan(&priorDeclared); scanErr != nil {
			if errors.Is(scanErr, pgx.ErrNoRows) {
				return nil, NewErr(ErrNotFound, fmt.Sprintf("work item %q not found", wi.ID))
			}
			if aerr := retryConflictErr(scanErr, "failed to read declared_resources for update"); aerr != nil {
				return nil, aerr
			}
			return nil, NewErr(ErrInternalError, "failed to read declared_resources for update")
		}
	}

	upd := buildWorkItemUpdate(req, wi.ID)
	tag, err := tx.Exec(ctx, upd.Query, upd.Args...)
	if err != nil {
		return nil, dbErrCause(err, "failed to update work_item")
	}
	if isCASConflict(upd.CAS, tag.RowsAffected()) {
		// Re-read inside the same transaction to find out what the row actually
		// holds, so the caller is told what to retry with.
		//
		// Isolation matters here and is worth stating: this transaction runs at
		// pool.Begin's default READ COMMITTED, so each statement takes a fresh
		// snapshot and this SELECT sees the value the winning writer committed.
		// Under Serializable it would instead see the snapshot from before the
		// conflict and report "is 0, not the expected 0" — nonsense. If this
		// function is ever moved onto pgx.Serializable (as run_attempts.go uses),
		// this read has to move outside the transaction.
		current := casVersionUnknown
		scanErr := tx.QueryRow(ctx, `SELECT resources_version FROM work_items WHERE id = $1`, wi.ID).Scan(&current)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			// Not a conflict: the row is gone. GetWorkItem above ran on the pool
			// BEFORE this transaction opened, so there is a window — narrow, and
			// nothing outside tests deletes work items today, but reporting a
			// vanished row as a version conflict would send the caller into a
			// retry loop that can never succeed.
			return nil, NewErr(ErrNotFound, fmt.Sprintf("work item %q not found", wi.ID))
		}
		return nil, casConflictErr(*req.ResourcesVersion, current)
	}

	// aihub#264: the UPDATE has been applied and the CAS (if any) has passed, so
	// the narrowing is real — release the file_scope locks it orphaned. Placed
	// after the CAS check on purpose: a rejected update returns above and rolls
	// back, so it can never leave locks released against a version that did not
	// move.
	if req.DeclaredResources != nil {
		if aerr := releaseUndeclaredFileScopeLocks(ctx, tx, wi.ID, wi.Project, priorDeclared, req.DeclaredResources); aerr != nil {
			return nil, aerr
		}
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
			return nil, dbErr(err, "failed to emit wi_goal_updated event")
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
			return nil, dbErr(err, "failed to emit wi_reclassified event")
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
			return nil, dbErr(err, "failed to emit wi_content_updated event")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		if aerr := retryConflictErr(err, "failed to commit update"); aerr != nil { // aihub#334
			return nil, aerr
		}
		return nil, NewErr(ErrInternalError, "failed to commit update")
	}

	// aihub#273: goal/content changed — refresh the wi embedding outside the
	// committed tx (network call), best-effort like the create path.
	if req.Goal != nil || req.Content != nil {
		refreshWorkItemEmbeddingBestEffort(ctx, pool, wi.ID)
	}

	return GetWorkItem(ctx, pool, wi.ID)
}

// cancelGate is CancelWorkItem's pure decision function (aihub#242). State is
// checked BEFORE permission so a state rejection is never reported as a
// permission failure: 409 means "wrong state", 403 means "wrong caller", and
// the two are no longer conflated.
//
// Before this fix, CancelWorkItem computed canCancel with a status check
// hard-wired to (queued|paused) and only checked wi.Status afterward. A
// blocked wi therefore always fell into the "insufficient permissions" 403
// branch — even for its own reporter — even though the real problem was
// state, not permission. Because a blocked wi (once its last dependency is
// removed) has no other exit — pf_claim_work_item also rejects status=blocked
// — that made such wis permanently stuck. Reporters may now cancel a blocked
// wi: that is the missing exit.
func cancelGate(status string, isReporter bool, callerRole, projectRole string) *AihubError {
	switch status {
	case "running":
		return NewErr(ErrConflictWIAlreadyClaimed, "work item is running; force_takeover first, then cancel")
	case "wrapped", "failed", "cancelled":
		return NewErr(ErrConflictTerminalState, fmt.Sprintf("work item is already in terminal state: %s", status))
	}

	// Remaining statuses (queued, paused, blocked): permission-only gate.
	canCancel := callerRole == "admin" || projectRole == "maintainer" || isReporter
	if !canCancel {
		return NewErr(ErrForbidden, "insufficient permissions: only the reporter, a project maintainer, or an admin may cancel this work item")
	}
	return nil
}

// CancelWorkItem sets a work item's status to cancelled if it's not running.
func CancelWorkItem(ctx context.Context, pool *pgxpool.Pool, idOrSlug, callerUserID, callerRole string, callerProjectRoles map[string]string) *AihubError {
	wi, aihubErr := GetWorkItem(ctx, pool, idOrSlug)
	if aihubErr != nil {
		return aihubErr
	}

	isReporter := wi.ReporterUserID == callerUserID
	projectRole := callerProjectRoles[wi.Project]
	if gateErr := cancelGate(wi.Status, isReporter, callerRole, projectRole); gateErr != nil {
		return gateErr
	}

	_, err := pool.Exec(ctx, `UPDATE work_items SET status='cancelled' WHERE id=$1`, wi.ID)
	if err != nil {
		return NewErr(ErrInternalError, "failed to cancel work item")
	}
	return nil
}

// buildReadyQueueItemsQuery assembles the SQL for the ready queue's items[]
// segment: $1 = project, $2 = max.
//
// A function rather than an inline literal so the sharing is at least visible in
// one place. Note precisely what that does and does not buy, because an earlier
// version of this comment overclaimed and was wrong:
//
// Inspecting this function's return value proves nothing about GetReadyQueue.
// Nothing forces GetReadyQueue to call it — an unused function is legal Go — so
// a divergent query inlined at the call site leaves a helper-inspecting test
// green. That was verified, not assumed: replacing the call site with an inline
// query that dropped both requires_human_session and the blocker NOT EXISTS left
// every aihub#280 test passing.
//
// The real guard is therefore behavioural and lives in
// TestGetReadyQueue_ItemsExcludesHumanSessionAndBlocked, which calls
// GetReadyQueue against a live DB and asserts what it actually returns. This
// helper's own test only pins the SQL's shape.
func buildReadyQueueItemsQuery() string {
	return `
		SELECT wi.id, wi.slug, wi.wi_type, wi.priority, wi.goal
		FROM work_items wi
		WHERE wi.project = $1
		  AND ` + readyOnlyPredicate + `
		ORDER BY
		  CASE wi.priority WHEN 'urgent' THEN 4 WHEN 'high' THEN 3
		                   WHEN 'normal' THEN 2 WHEN 'low' THEN 1 END DESC,
		  wi.created_at ASC
		LIMIT $2`
}

// GetReadyQueue returns the six-segment LCRS view for a project.
func GetReadyQueue(ctx context.Context, pool *pgxpool.Pool, project string, max int) (*ReadyQueue, *AihubError) {
	// ⚠️ This clamp obeys half of queryparam.go's Rule 2 and not the other half:
	// it clamps to the CEILING rather than back to the default (which is right,
	// and is what aihub#267 made ListWorkItems do too), but it does NOT disclose.
	// `max=5000` and `max=200` return byte-identical responses.
	//
	// Left open deliberately rather than overlooked. Disclosing means a
	// `request_adjusted` field on ReadyQueue, which has none, and the six-segment
	// response is consumed by pf_get_ready_queue and /ui. That is a wider change
	// than aihub#255/#267/#340 asked for, and a half-done version — clamping
	// louder without a field to say so — would be no better than this. Stated
	// here so the next reader finds a known gap rather than an inconsistency
	// they have to re-derive.
	if max <= 0 {
		max = 10
	}
	if max > 200 {
		max = 200
	}
	result := &ReadyQueue{
		Items:             []ReadyItem{},
		Running:           []RunningItem{},
		Stalled:           []StalledItem{},
		Paused:            []PausedItem{},
		NeedsHumanSession: []ReadyItem{},
		Unclassified:      []ReadyItem{},
	}

	// items[]: queued + no blocker + requires_human_session=false.
	itemRows, err := pool.Query(ctx, buildReadyQueueItemsQuery(), project, max)
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
			var stall, actorDisplay *string
			// aihub#206: actor_display on the wi_stalled event can be NULL
			// (e.g. escalated-stall events emitted without a display name
			// set), which can't scan into item.LastActorDisplay's plain
			// string directly — scan through a nullable local instead.
			if err := stalledRows.Scan(&item.ID, &item.Slug, &stall, &stalledAt, &actorDisplay); err != nil {
				continue
			}
			if stall != nil {
				item.StallReason = *stall
			}
			if actorDisplay != nil {
				item.LastActorDisplay = *actorDisplay
			}
			item.StalledSince = stalledAt.Format(time.RFC3339)
			result.Stalled = append(result.Stalled, item)
		}
		stalledRows.Close()
	}

	// paused[]: status=paused
	pausedRows, err := pool.Query(ctx, `
		SELECT wi.id, wi.slug, ra.last_active_at, ra.actor_display, ra.pause_reason
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
			if err := pausedRows.Scan(&item.ID, &item.Slug, &lat, &actorDisplay, &item.PauseReason); err != nil {
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
		  AND `+noLiveBlockerPredicate+`
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
		  AND `+noLiveBlockerPredicate+`
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
