package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GMISWE/ieops-aihub/internal/render"
)

// ─── Render Types (config-driven) ─────────────────────────────────────────────

// defaultRenderTypes is the backward-compatible default (aihub#102 / aihub#81).
// If a deploy overrides this via RENDER_MEMORY_TYPES it must include these
// types too — but the lazy-render fallback in handleArtifactHTML makes
// save-time rendering an optimisation, not the only render path.
const defaultRenderTypes = "methodology.spec,methodology.plan,methodology.review,methodology.execute,methodology.retro,methodology.wrap_summary"

// renderTypes is the set of memory types for which Markdown→HTML rendering is
// performed on save. Initialised to the default set so Remember() is safe to
// call before InitRenderTypes (e.g. in unit tests that bypass main()).
var renderTypes = parseRenderTypes(defaultRenderTypes)

// renderTypesMu guards renderTypes against concurrent read/write.
var renderTypesMu sync.RWMutex

// parseRenderTypes parses a comma-separated type list into a lookup set.
// Falls back to defaultRenderTypes when envVal is empty or whitespace-only.
func parseRenderTypes(envVal string) map[string]bool {
	if strings.TrimSpace(envVal) == "" {
		envVal = defaultRenderTypes
	}
	m := make(map[string]bool)
	for _, t := range strings.Split(envVal, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			m[t] = true
		}
	}
	return m
}

// InitRenderTypes overrides the render-type set from an env-var value at
// server startup. envVal is a comma-separated list; empty or whitespace-only
// values fall back to the default. Logs the effective set to stderr.
// Call once from cmd/aihub/main.go before serving requests.
func InitRenderTypes(envVal string) {
	m := parseRenderTypes(envVal)
	renderTypesMu.Lock()
	renderTypes = m
	renderTypesMu.Unlock()
	// Log after Unlock to avoid holding the mutex during I/O.
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(os.Stderr, "aihub: render types: %v\n", keys)
}

// IsRenderType reports whether memType is one of the configured render types —
// i.e. whether a memory of this type is an ARTIFACT in the sense the rest of the
// system uses: something whose markdown the deployment renders to HTML for a
// viewer, rather than a note/decision/observation that merely happens to have an
// html payload attached.
//
// aihub#151 needs this because rendered_html is not a usable artifact test.
// resolveRenderedHTML precedence #1 stores an explicit `html=` verbatim for ANY
// type, so "has rendered_html" was satisfiable by any writer on any memory, and
// the share endpoint used exactly that check before publishing a row to the
// unauthenticated /share/:id route.
//
// It deliberately reads the same renderTypes set rather than restating a list:
// aihub#312/#315 are what a second, drifting copy of one fact costs.
func IsRenderType(memType string) bool {
	renderTypesMu.RLock()
	defer renderTypesMu.RUnlock()
	return renderTypes[memType]
}

// RenderTypeNames returns the configured render types, sorted, for error messages.
// Callers must not mutate the result; it is a fresh slice each call.
func RenderTypeNames() []string {
	renderTypesMu.RLock()
	names := make([]string, 0, len(renderTypes))
	for t := range renderTypes {
		names = append(names, t)
	}
	renderTypesMu.RUnlock()
	sort.Strings(names)
	return names
}

// resolveRenderedHTML decides the value stored in memories.rendered_html on save
// (aihub#27 / aihub#104). Precedence:
//  1. explicit non-empty HTML (pf_save_artifact html=) → stored verbatim, any type;
//  2. else, if the type is in the configured renderTypes set → goldmark-render the
//     markdown content (best-effort; a render error stores NULL, never blocks insert);
//  3. else → nil (column left NULL).
func resolveRenderedHTML(explicit *string, memType, content string) *string {
	if explicit != nil && strings.TrimSpace(*explicit) != "" {
		return explicit
	}
	renderTypesMu.RLock()
	shouldRender := renderTypes[memType]
	renderTypesMu.RUnlock()
	if !shouldRender {
		return nil
	}
	h, rerr := render.Markdown(content)
	if rerr != nil {
		fmt.Fprintf(os.Stderr,
			"memory render: markdown→HTML failed for type=%s; storing without rendered_html: %v\n",
			memType, rerr)
		return nil
	}
	return &h
}

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
	RenderedHTML     *string         `json:"rendered_html,omitempty"`
	Commits          json.RawMessage `json:"commits"`
	// LatestID is the authoritative version-lineage pointer: nil while this
	// row is the current head of its supersede chain, otherwise the id of the
	// row that currently is. It is transactionally maintained by UpdateMemory
	// and propagated across the whole lineage on every supersede/redact (see
	// GetLatestByID). Do NOT confuse this with attrs["similar_to"] — that is
	// an unrelated, one-shot write-time content-similarity dedup hint set by
	// Remember (see the dedup block above); it carries no lineage meaning.
	LatestID  *string   `json:"latest_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// populated post-scan from memory_relations (aihub#74); NOT part of any SELECT/Scan — do not add to the 6 lockstep sites.
	Related   []RelatedRef `json:"related,omitempty"`
	Backlinks []RelatedRef `json:"backlinks,omitempty"`
	// opt3 P1: recall returns content truncated to a snippet; full via GET /v1/memories/:id.
	ContentTruncated bool `json:"content_truncated,omitempty"`
	ContentFullLen   int  `json:"content_full_len,omitempty"`
}

// RelatedRef is a lightweight reference to a related memory, used in
// Memory.Related (forward links) and Memory.Backlinks (incoming links).
// Summary is a short content snippet (≤120 chars) for inline display.
type RelatedRef struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Summary string `json:"summary"` // short snippet of the target memory's content
}

// MemoryWithStrength extends Memory with a computed recall score.
type MemoryWithStrength struct {
	Memory
	EffectiveStrength float64  `json:"effective_strength"`
	Similarity        *float64 `json:"similarity,omitempty"` // cosine similarity from pgvector
}

// RememberRequest is the body for POST /v1/memories.
type RememberRequest struct {
	Project          string          `json:"project"`
	Type             string          `json:"type"`
	Content          string          `json:"content"`
	Visibility       string          `json:"visibility"`
	WorkItemID       *string         `json:"work_item_id,omitempty"`
	BaseStrength     *float64        `json:"base_strength,omitempty"`
	Attrs            json.RawMessage `json:"attrs,omitempty"`
	ExpiresAt        *time.Time      `json:"expires_at,omitempty"`
	DedupMode        string          `json:"dedup_mode"` // strict | suggest | off
	RelatedMemoryIDs []string        `json:"related_memory_ids,omitempty"`
	ContextSnippet   *string         `json:"context_snippet,omitempty"`
	SupersedesMemID  *string         `json:"supersedes_memory_id,omitempty"`
	Tags             []string        `json:"tags,omitempty"`
	// G35 / design §5.2 pf_save_artifact: methodology artifacts may carry a
	// structured_payload (e.g. spec acceptance criteria). The server merges it
	// into attrs.structured_payload so the recall flow can return it later.
	StructuredPayload json.RawMessage `json:"structured_payload,omitempty"`
	// aihub#104: optional pre-rendered HTML stored verbatim in
	// memories.rendered_html. When non-empty it OVERRIDES the server-side
	// markdown auto-render (renderTypes), letting callers attach a custom
	// standalone HTML document (or fragment) for any artifact type — e.g.
	// pf_save_artifact html=. Empty/whitespace falls back to auto-render.
	RenderedHTML *string `json:"rendered_html,omitempty"`
	// aihub#210: attempt credentials for methodology.* artifact writes. Bound to
	// WorkItemID and verified in handleRemember. Previously absent, so echo's
	// c.Bind silently dropped the credentials pf_save_artifact sends, leaving
	// project-writer as the only gate on spec/plan writes. Empty for
	// non-methodology memories, which stay project-writer gated.
	AttemptID     string `json:"attempt_id,omitempty"`
	ClaimEpoch    int64  `json:"claim_epoch,omitempty"`
	SessionSecret string `json:"session_secret,omitempty"`
	// Set by handler from Bearer token — not from JSON body.
	CallerUserID  string `json:"-"`
	CallerDisplay string `json:"-"`
	// aihub#236: activation state carried forward by UpdateMemory so that
	// editing a memory does not reset its lineage's activation history (which
	// previously dropped every new version into the NULLS-LAST ranking tier).
	//
	// json:"-" is REQUIRED, not stylistic. handleRemember binds the request body
	// directly into this struct (internal/server/routes_memory.go:60) with no
	// intermediate DTO, so a JSON-named field here would let any project writer
	// POST /v1/memories with activation_count=9999 and pin their memory to the
	// top of every recall in the project. Only UpdateMemory sets these.
	LastActivatedAt *time.Time `json:"-"`
	LastActivatedBy *string    `json:"-"`
	ActivationCount int        `json:"-"`
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
	RecallAlgo          string   `json:"recall_algo,omitempty"`
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
	// Total is the count of memories matching every filter in the request
	// (project/status/visibility/type/work_item/min_strength, and, on the
	// vector path, similarity_threshold) — computed independently of
	// pagination (top_k/limit/cursor). aihub#249: without this a caller
	// cannot tell "that's everything" from "you haven't paged far enough";
	// NextCursor being nil already answers that for the text path, but Total
	// lets a caller size a UI/progress bar without walking every page.
	// Populated on both the text path (Recall) and the vector path
	// (RecallWithVector) — see countMemories.
	Total int `json:"total"`
	// UnmatchedTypes names the entries of the request's `type` filter that no
	// visible, live memory in the project matches — the difference between "this
	// project has no such memory" and "your type value is wrong", which the
	// response could not express before aihub#289 (both were an empty item list).
	// Populated by handleRecall via UnmatchedTypes(), so it covers the text and
	// vector paths alike; see memory_unmatched.go for the precise scope of the
	// claim it makes.
	//
	// `omitempty` is deliberate: absent means "nothing to report", so the healthy
	// call shapes — the overwhelming majority — pay zero tokens for it, and a
	// present field always means the caller has a problem to look at. "No type
	// filter supplied" and "every type matched" are both absent; neither is a
	// fault, so nothing needs to tell them apart. A BROKEN diagnostic is a fault
	// and is NOT folded in with them — it reports through the field below.
	UnmatchedTypes []string `json:"unmatched_types,omitempty"`
	// UnmatchedTypesError is set when the unmatched-type diagnostic could not be
	// computed (see UnmatchedTypes in memory_unmatched.go). It exists because
	// `omitempty` would otherwise make a failed check indistinguishable from a
	// clean one, which is the same silent-degradation shape this work item was
	// opened to remove — and the same omitempty trap as aihub#269's
	// content_truncated. When this is set, UnmatchedTypes is nil and says nothing
	// about the type filter either way; it is never a partial answer.
	UnmatchedTypesError string `json:"unmatched_types_error,omitempty"`
	// RequestAdjusted names the caller-supplied parameters this endpoint changed
	// on the way in — today only `top_k`, which normalizeRecallTopK caps at 200
	// and replaces with 20 when it arrives negative. Omitted when nothing was
	// adjusted, for the reason spelled out in request_adjusted.go.
	//
	// This is the field normalizeRecallTopK's comment says did not exist: it
	// argued that "a dedicated top_k_clamped field would not survive
	// slimRecallResult's opt-in whitelist, so disclosure by new field would reach
	// REST callers while silently missing the pf_recall callers who are the
	// population that has the problem". That is why aihub#314 widened the
	// whitelist FIRST and made the field generic — so this is the last time that
	// argument has to be made.
	RequestAdjusted []RequestAdjustment `json:"request_adjusted,omitempty"`
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

// memoryRefTime returns the reference timestamp used for BOTH decay and
// ranking: the most recent of last_activated_at and created_at. It is the Go
// mirror of memRefTimeSQL and the two MUST stay in agreement — recall filters
// rows in SQL and reports effective_strength from Go, so a divergence makes the
// score shown to clients disagree with the order rows come back in (aihub#236).
//
// Deliberately NOT "activation if set, else created": UpdateMemory carries a
// lineage's last_activated_at onto each new version, so a freshly created head
// can hold an old activation timestamp. Taking the later of the two keeps that
// head as fresh as it actually is.
func memoryRefTime(lastActivatedAt *time.Time, createdAt time.Time) time.Time {
	if lastActivatedAt != nil && lastActivatedAt.After(createdAt) {
		return *lastActivatedAt
	}
	return createdAt
}

// memRefTimeSQL is the SQL mirror of memoryRefTime. PostgreSQL's GREATEST
// IGNORES NULL arguments (returning NULL only when every argument is NULL) and
// memories.created_at is NOT NULL, so this expression is total: it can never
// yield NULL, and therefore can never produce a NULLS-ordering tier.
//
// Do NOT rewrite this as COALESCE(last_activated_at, created_at). COALESCE
// prefers a stale activation timestamp over a fresher created_at, which
// reintroduces aihub#236 in the min_strength filter — a freshly edited fact.*
// memory would be decayed against its old activation and filtered out.
const memRefTimeSQL = `GREATEST(last_activated_at, created_at)`

// recallCursorSep separates the two halves of a Recall cursor. Neither half can
// contain it: the left half is RFC3339Nano (digits, '-', ':', '.', 'T', and a
// 'Z'/'+'/'-' offset) and the right half is a NewID (base62 plus one '_').
const recallCursorSep = "|"

// formatRecallCursor encodes Recall's full sort position. Recall orders by
// `memRefTimeSQL DESC, id DESC` — TWO keys — so a cursor that carried only the
// timestamp could not express "the row after this one" among rows sharing a
// reference time. With a strict `<` on the timestamp alone, every row tied with
// the last row of a page was skipped on all subsequent pages (aihub#236 finding
// 7, deferred to aihub#239). Ties are rare because created_at defaults to
// clock_timestamp() at microsecond resolution, but a bulk import or a backfill
// that stamps many rows identically reaches them.
func formatRecallCursor(refTime time.Time, id string) string {
	return refTime.Format(time.RFC3339Nano) + recallCursorSep + id
}

// parseRecallCursor splits a cursor into its timestamp and id halves. Cursors
// issued before aihub#239 carry the timestamp alone; those return id == "" and
// the caller must fall back to the single-key comparison so an in-flight cursor
// keeps paginating instead of erroring or silently restarting.
func parseRecallCursor(cursor string) (ts, id string) {
	if i := strings.LastIndex(cursor, recallCursorSep); i >= 0 {
		return cursor[:i], cursor[i+len(recallCursorSep):]
	}
	return cursor, ""
}

// MemoryStrength calculates effective_strength (raw) per §7.2.
// Formula: base_strength × exp(-days_since / stability_days)
// days_since is measured from memoryRefTime (M8, revised by aihub#236).
func MemoryStrength(baseStrength, stabilityDays float64, lastActivatedAt *time.Time, createdAt time.Time) float64 {
	if stabilityDays <= 0 {
		return 0
	}
	daysSince := time.Since(memoryRefTime(lastActivatedAt, createdAt)).Hours() / 24
	return baseStrength * math.Exp(-daysSince/stabilityDays)
}

// computeStabilityDays returns current stability_days per activation count (§7.2).
// stability_days = base_stability × (1 + activation_count × 0.5)
func computeStabilityDays(memType string, activationCount int) float64 {
	return baseStabilityForType(memType) * (1.0 + float64(activationCount)*0.5)
}

// ─── Remember ─────────────────────────────────────────────────────────────────

// Querier is satisfied by both *pgxpool.Pool and pgx.Tx, letting the shared
// INSERT (and other supersede-path statements) run against either a bare
// pool connection or an in-flight transaction without duplicating the SQL.
type Querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// validateSupersedeScope enforces aihub#210: a supersede may only target a memory
// in the SAME project, and methodology.* artifacts may only supersede within the
// SAME work item — otherwise any project writer could re-home or clobber another
// wi's (or project's) spec/plan lineage. reqWI/tgtWI are canonical work_item ids
// ("" when unset). Pure so it is unit-testable without a DB.
func validateSupersedeScope(memType, reqProject, reqWI, tgtProject, tgtWI string) *AihubError {
	if tgtProject != reqProject {
		return NewErr(ErrForbidden, "supersedes_memory_id belongs to a different project")
	}
	if strings.HasPrefix(memType, "methodology.") && (tgtWI == "" || tgtWI != reqWI) {
		return NewErr(ErrForbidden, "methodology.* artifacts may only supersede a memory bound to the same work item")
	}
	return nil
}

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
	// aihub#289: reject '|' on the WRITE path too. The prefix check above accepts
	// "experience.*|rule.*" — it starts with "experience." — so a memory could be stored
	// under a type that the read path now rejects with a 400, making the row
	// permanently unreadable by type: write-only data, created by the same piped-string
	// mistake this work item is about. Guarding only the read side would have left the
	// two halves of one contract disagreeing.
	if strings.Contains(req.Type, "|") {
		return nil, false, NewErr(ErrInvalidMemoryType,
			fmt.Sprintf("type %q contains '|', which is not part of the memory type "+
				"vocabulary. A memory has exactly ONE type; '|' is not a separator here, "+
				"and a type stored with it could never be recalled by type. Pick one "+
				"concrete type (e.g. experience.pitfall).", req.Type))
	}

	if req.DedupMode == "" {
		req.DedupMode = "suggest"
	}
	if req.Visibility == "" {
		req.Visibility = "project"
	}

	// Resolve work_item_id (may be a slug like "aihub#1") to the canonical
	// work_items.id before the memories / agent_events inserts below, both of which
	// FK-reference work_items(id). Passing a raw slug violates the FK. (aihub#127)
	if req.WorkItemID != nil && *req.WorkItemID != "" {
		wi, werr := GetWorkItem(ctx, pool, *req.WorkItemID)
		if werr != nil {
			return nil, false, werr
		}
		canonical := wi.ID
		req.WorkItemID = &canonical
	}

	// C5 (aihub#210): validate supersede scope before any lineage work — the
	// target must be in the same project, and methodology.* must stay within the
	// same wi. Guards against a project-writer re-homing or clobbering another
	// wi's (or project's) spec/plan lineage.
	if req.SupersedesMemID != nil && *req.SupersedesMemID != "" {
		var tgtProject string
		var tgtWorkItemID *string
		if err := pool.QueryRow(ctx,
			`SELECT project, work_item_id FROM memories WHERE id=$1`, *req.SupersedesMemID,
		).Scan(&tgtProject, &tgtWorkItemID); err != nil {
			return nil, false, NewErr(ErrNotFound, "supersedes_memory_id not found")
		}
		reqWI := ""
		if req.WorkItemID != nil {
			reqWI = *req.WorkItemID
		}
		tgtWI := ""
		if tgtWorkItemID != nil {
			tgtWI = *tgtWorkItemID
		}
		if scopeErr := validateSupersedeScope(req.Type, req.Project, reqWI, tgtProject, tgtWI); scopeErr != nil {
			return nil, false, scopeErr
		}
	}

	// Dedup check (skip for "off" mode).
	// Design §7.7 / §11: strict mode rejects only at HIGH similarity (≥ 0.85);
	// suggest mode annotates attrs.similar_to between LOW (0.65) and HIGH.
	//
	// aihub#249: attrs.similar_to is NOT a version/lineage pointer. It is a
	// write-time content-similarity annotation (Jaccard, computed by
	// textDedupCheck below) recording that THIS new memory looked similar to
	// an existing one at the moment it was created — a one-shot dedup hint,
	// never updated again after this write, and it can point to any memory
	// with similar content regardless of type or lineage. The actual,
	// transactionally-maintained "what supersedes what" chain pointer is
	// Memory.LatestID (see its doc comment and UpdateMemory) — that field is
	// authoritative for lineage; attrs.similar_to is not and must not be
	// treated as one.
	if req.DedupMode != "off" {
		existing, err := textDedupCheck(ctx, pool, req.Project, req.Type, req.Content)
		if err != nil {
			return nil, false, err
		}
		if existing != nil {
			sim := jaccardSimilarity(req.Content, existing.Content)
			if req.DedupMode == "strict" && sim >= memoryDedupHigh {
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
			// suggest mode (or strict-below-high): annotate attrs.similar_to
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
	stabilityDays := computeStabilityDays(req.Type, req.ActivationCount)
	if req.Tags == nil {
		req.Tags = []string{}
	}
	if len(req.Attrs) == 0 {
		req.Attrs = json.RawMessage(`{}`)
	}

	// G35: merge structured_payload / context_snippet / related_memory_ids into attrs
	// so callers can retrieve them via Memory.attrs without losing data.
	if len(req.StructuredPayload) > 0 || req.ContextSnippet != nil || len(req.RelatedMemoryIDs) > 0 {
		attrsMap := map[string]any{}
		_ = json.Unmarshal(req.Attrs, &attrsMap)
		if len(req.StructuredPayload) > 0 {
			var sp any
			if jerr := json.Unmarshal(req.StructuredPayload, &sp); jerr == nil {
				attrsMap["structured_payload"] = sp
			}
		}
		if req.ContextSnippet != nil {
			attrsMap["context_snippet"] = *req.ContextSnippet
		}
		if len(req.RelatedMemoryIDs) > 0 {
			attrsMap["related_ids"] = req.RelatedMemoryIDs
		}
		merged, _ := json.Marshal(attrsMap)
		req.Attrs = merged
	}

	// aihub#27 / IEBE-1694: render markdown to HTML for configured types only.
	// Render is best-effort — a render failure must NOT block the insert (spec
	// decision 3). Other memory types leave rendered_html NULL.
	renderedHTML := resolveRenderedHTML(req.RenderedHTML, req.Type, req.Content)

	// aihub#192: compute embedding vector for embeddable types. This is a
	// network call to the embedding provider, so it MUST run before any
	// transaction below begins — never hold a DB tx open across it.
	// Best-effort: a provider error logs a warning and leaves emb_vector NULL.
	var embVecLit *string // nil → SQL NULL
	var embModel *string
	var embDims *int
	if embeddableType(req.Type) {
		if vec, embErr := embProvider.Embed(ctx, req.Content); embErr != nil {
			fmt.Fprintf(os.Stderr, "remember: embed failed for type=%s: %v\n", req.Type, embErr)
		} else if len(vec) > 0 {
			lit := vecToPGLiteral(vec)
			embVecLit = &lit
			m := embProvider.ModelID()
			embModel = &m
			d := embProvider.Dims()
			embDims = &d
		}
	}

	// aihub#201: hoisted so the new row's id can double as its own latest_id
	// (self-head trick — a freshly inserted row is always the head of its
	// lineage) and be reused below to propagate the cursor across the old
	// lineage when this insert supersedes a prior version.
	newID := NewID("mem")

	// N1 / aihub#201 / S2 / BUG1: when SupersedesMemID is set, the head
	// resolve + archive + insert + latest_id propagation must be one atomic,
	// serialized unit — otherwise concurrent supersedes can branch the
	// lineage (see the historical bug note below). Everything from here to
	// tx.Commit runs on `q` (the tx when superseding, the bare pool
	// otherwise) so the big INSERT below is never duplicated.
	//
	// Historical bug (fixed here): the OLD code ran the head-resolve/archive
	// retry loop and the latest_id propagation UPDATE as separate,
	// non-transactional statements against the pool. A concurrent loser could
	// resolve the same stale head, lose the archive race, retry, and still
	// see the old (pre-propagation) cursor because the winner hadn't
	// committed/propagated yet — exhausting all retries and falling through
	// with oldHead="", inserting a branch off a stale supersedes_id with no
	// archive and no propagation. Wrapping the whole sequence in one tx makes
	// the loser's archive UPDATE block on the winner's row lock instead of
	// racing it: by the time the loser's statement unblocks, the winner has
	// either committed (loser sees 0 rows, re-resolves the now-current head)
	// or rolled back (loser retries the same head cleanly).
	var q Querier = pool
	var tx pgx.Tx
	if req.SupersedesMemID != nil && *req.SupersedesMemID != "" {
		var txErr error
		tx, txErr = pool.Begin(ctx)
		if txErr != nil {
			return nil, false, NewErr(ErrInternalError, fmt.Sprintf("failed to begin tx: %v", txErr))
		}
		defer func() {
			if tx != nil {
				_ = tx.Rollback(ctx)
			}
		}()
		q = tx

		// Deadlock fix: row-level locks alone cannot give N-way concurrent
		// supersedes a total order. With N transactions each racing to
		// archive-then-relink one link in the SAME chain, each ends up
		// holding a lock on a different row while waiting on a different
		// transaction's row — a genuine multi-party wait-for cycle (Postgres
		// 40P01, reproduced deterministically with N=8 concurrent supersedes
		// in TestConcurrentUpdateSingleHead even after making the individual
		// SELECT/UPDATE steps blocking rather than racy). The standard fix
		// for "chain of dependent row locks with no global order" is to
		// serialize the whole resolve→archive→insert→propagate sequence
		// with a single mutex per lineage, so only one transaction is ever
		// inside the critical section for a given lineage. A Postgres
		// session-independent transaction-scoped advisory lock does exactly
		// that (auto-released on commit/rollback, no cleanup needed) — keyed
		// on the lineage's ROOT id (walked up supersedes_id to the row with
		// supersedes_id IS NULL), which is the only identifier that is
		// invariant across every step of every concurrent call, unlike
		// latest_id/head which is exactly what's racing.
		var rootID string
		if err := q.QueryRow(ctx, `
			WITH RECURSIVE up(id, supersedes_id, depth) AS (
				SELECT id, supersedes_id, 0 FROM memories WHERE id = $1
				UNION ALL
				SELECT m.id, m.supersedes_id, up.depth + 1
				FROM memories m
				JOIN up ON m.id = up.supersedes_id
				WHERE up.depth < 10000
			)
			SELECT id FROM up WHERE supersedes_id IS NULL
			ORDER BY depth DESC LIMIT 1`, *req.SupersedesMemID,
		).Scan(&rootID); err != nil {
			// aihub#334: every hop from here to tx.Commit runs inside the
			// supersede transaction, so any of them can come back as a class 40
			// rollback (40001 above READ COMMITTED, 40P01 at any level — this
			// path's own comments above record a reproduced 40P01 at N=8). That
			// is a retryable conflict, not a broken server.
			if aerr := retryConflictErr(err, "failed to resolve lineage root"); aerr != nil {
				return nil, false, aerr
			}
			return nil, false, NewErr(ErrInternalError, fmt.Sprintf("failed to resolve lineage root: %v", err))
		}
		if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, rootID); err != nil {
			if aerr := retryConflictErr(err, "failed to acquire lineage lock"); aerr != nil {
				return nil, false, aerr
			}
			return nil, false, NewErr(ErrInternalError, fmt.Sprintf("failed to acquire lineage lock: %v", err))
		}
	}

	var oldHead string
	if req.SupersedesMemID != nil && *req.SupersedesMemID != "" {
		startID := *req.SupersedesMemID
		const maxAttempts = 8
		for attempt := 0; attempt < maxAttempts; attempt++ {
			// With the per-lineage advisory lock held above, at most one
			// transaction is ever inside this loop for a given lineage, so a
			// plain (non-locking) read is sufficient and safe here.
			var head string
			if err := q.QueryRow(ctx, `
				SELECT COALESCE(latest_id, id) FROM memories WHERE id=$1`, startID,
			).Scan(&head); err != nil || head == "" {
				// aihub#334: `break` here is the same swallow shape as
				// unblockDependentWI's. A class 40 rollback would leave the
				// loop, fail the oldHead=="" check below, and be reported as
				// "failed to resolve and archive supersede head after retries"
				// — an INTERNAL_ERROR that names the wrong cause and hides a
				// SQLSTATE the caller could have acted on. Classify while the
				// *pgconn.PgError is still in hand.
				if aerr := retryConflictErr(err, "failed to read supersede head"); aerr != nil {
					return nil, false, aerr
				}
				break
			}
			tag, err := q.Exec(ctx, `
				UPDATE memories SET status='archived', updated_at=clock_timestamp()
				WHERE id=$1 AND status='active'`, head)
			if err != nil {
				// aihub#334 instance 2, measured: this is the hop that actually
				// fires. The loser blocks here on the winner's row lock and, at
				// SERIALIZABLE, is woken with 40001 rather than proceeding.
				// Note the shape — a plain DML statement, neither a
				// `SELECT ... FOR UPDATE` nor a commit.
				if aerr := retryConflictErr(err, "failed to archive head"); aerr != nil {
					return nil, false, aerr
				}
				return nil, false, NewErr(ErrInternalError, fmt.Sprintf("failed to archive head: %v", err))
			}
			if tag.RowsAffected() == 1 {
				oldHead = head
				req.SupersedesMemID = &head
				break
			}
			// Lost the race (or head was already non-active for another reason,
			// e.g. redacted): re-resolve from the same starting id and retry. In
			// a tx, this UPDATE only reaches here after any concurrent winner's
			// row lock has been released (commit or rollback), so latest_id is
			// guaranteed to reflect the winner's outcome on the next iteration.
		}
		if oldHead == "" {
			return nil, false, NewErr(ErrInternalError, "failed to resolve and archive supersede head after retries")
		}

		// aihub#239: inherit the lineage's activation history on EVERY supersede
		// path, not just UpdateMemory's. aihub#236 made UpdateMemory carry the
		// trio, but pf_save_artifact and any POST /v1/memories with
		// supersedes_memory_id reach here through Remember instead
		// (internal/mcp/tools_memory.go), so they used to mint a head with
		// activation_count=0 / last_activated_at=NULL and strand the history on
		// the row we just archived. That made #236's guarantee path-dependent.
		//
		// handleRemember zeroes the trio right after c.Bind (the #236 finding-3
		// fix, routes_memory.go), so an external caller can never supply it —
		// which is exactly why the inheritance has to happen down here in the
		// domain layer rather than being left to the caller.
		//
		// Read from oldHead, the row this call actually archived, rather than
		// from a head the caller resolved earlier: under concurrent supersedes
		// the archive loop above may have landed on a different row than the
		// caller saw. An explicitly supplied trio still wins (UpdateMemory
		// passes one), so this only fills the gap.
		if req.LastActivatedAt == nil && req.LastActivatedBy == nil && req.ActivationCount == 0 {
			var inhAt *time.Time
			var inhBy *string
			var inhCount int
			if err := q.QueryRow(ctx, `
				SELECT last_activated_at, last_activated_by, activation_count
				FROM memories WHERE id=$1`, oldHead,
			).Scan(&inhAt, &inhBy, &inhCount); err != nil {
				return nil, false, dbErrCause(err, "failed to inherit activation state from supersede head")
			}
			req.LastActivatedAt, req.LastActivatedBy, req.ActivationCount = inhAt, inhBy, inhCount
			// stabilityDays was computed above from a zero ActivationCount, so it
			// must be recomputed now that the count is inherited — otherwise the
			// new head stores a non-zero activation_count alongside a
			// stability_days derived from zero. Only bites experience.* and the
			// default bucket: fn_mem_immortal (migration 0006) overwrites
			// stability_days for rule.* / fact.* / methodology.* on INSERT.
			stabilityDays = computeStabilityDays(req.Type, req.ActivationCount)
		}
	}

	mem := &Memory{}
	err := q.QueryRow(ctx, `
		INSERT INTO memories (
			id, project, type, content, author_user_id, author_display,
			work_item_id, visibility, is_immortal, base_strength, stability_days,
			activation_count, last_activated_at, last_activated_by, expires_at, tags, source_artifact_id,
			emb_model, emb_dims, emb_vector,
			status, attrs, rendered_html, supersedes_id, latest_id, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11,
			$12, $13, $14, $15, $16, $17,
			$18, $19, $20::vector,
			'active', $21, $22, $23, $1, clock_timestamp(), clock_timestamp()
		)
		RETURNING id, project, type, content, author_user_id, author_display,
			work_item_id, visibility, is_immortal, base_strength, stability_days,
			last_activated_at, last_activated_by, activation_count, expires_at,
			tags, source_artifact_id, emb_model, emb_dims, status, attrs,
			rendered_html, commits, latest_id, created_at, updated_at`,
		newID, req.Project, req.Type, req.Content, req.CallerUserID, req.CallerDisplay,
		req.WorkItemID, req.Visibility, immortal, baseStrength, stabilityDays,
		req.ActivationCount, req.LastActivatedAt, req.LastActivatedBy, // $12, $13, $14
		req.ExpiresAt, req.Tags, nil, // $15, $16, $17 — source_artifact_id = nil
		embModel, embDims, embVecLit, // $18, $19, $20 — emb_model/dims/vector
		req.Attrs, renderedHTML, req.SupersedesMemID, // $21, $22, $23
	).Scan(
		&mem.ID, &mem.Project, &mem.Type, &mem.Content, &mem.AuthorUserID, &mem.AuthorDisplay,
		&mem.WorkItemID, &mem.Visibility, &mem.IsImmortal, &mem.BaseStrength, &mem.StabilityDays,
		&mem.LastActivatedAt, &mem.LastActivatedBy, &mem.ActivationCount, &mem.ExpiresAt,
		&mem.Tags, &mem.SourceArtifactID, &mem.EmbModel, &mem.EmbDims, &mem.Status,
		&mem.Attrs, &mem.RenderedHTML, &mem.Commits, &mem.LatestID, &mem.CreatedAt, &mem.UpdatedAt,
	)
	if err != nil {
		// aihub#334: on the supersede path this INSERT runs on the transaction
		// (`q` is the tx), so it can lose the same concurrency race.
		if aerr := retryConflictErr(err, "failed to insert memory"); aerr != nil {
			return nil, false, aerr
		}
		return nil, false, NewErr(ErrInternalError, fmt.Sprintf("failed to insert memory: %v", err))
	}

	// aihub#201: advance the old lineage's cursor to the new head. Every row
	// that used to point latest_id at oldHead (the whole prior chain) now
	// points at newID instead. Runs on the same tx as the archive+insert above
	// so the whole supersede sequence commits or rolls back together.
	//
	// BUG1 deadlock fix: this UPDATE's WHERE clause matches an unindexed
	// column (latest_id) and can therefore touch multiple rows (the whole
	// prior chain shares one cursor). Plain `UPDATE ... WHERE latest_id=$2`
	// lets Postgres acquire those row locks in scan order, which is not
	// guaranteed consistent across concurrent transactions — under N-way
	// concurrency two txs can each hold a lock the other needs, in opposite
	// order, and deadlock (40P01; reproduced with N=8 concurrent supersedes
	// in TestConcurrentUpdateSingleHead before this fix). Driving the update
	// off an explicit `SELECT ... ORDER BY id FOR UPDATE` forces every
	// transaction to acquire the same rows in the same (id-sorted) order, so
	// the wait graph can only ever be a chain, never a cycle.
	if oldHead != "" {
		if _, err := q.Exec(ctx, `
			WITH targets AS (
				SELECT id FROM memories WHERE latest_id=$2 ORDER BY id FOR UPDATE
			)
			UPDATE memories SET latest_id=$1
			WHERE id IN (SELECT id FROM targets)`, newID, oldHead); err != nil {
			// aihub#334: the ORDER BY id FOR UPDATE above is what makes 40P01
			// unlikely here, not impossible — and it does nothing at all about
			// 40001 above READ COMMITTED.
			if aerr := retryConflictErr(err, "failed to propagate latest_id"); aerr != nil {
				return nil, false, aerr
			}
			return nil, false, NewErr(ErrInternalError, fmt.Sprintf("failed to propagate latest_id: %v", err))
		}
	}

	if tx != nil {
		if err := tx.Commit(ctx); err != nil {
			// aihub#334: SSI reports most SERIALIZABLE conflicts at COMMIT
			// rather than at the statement that caused them.
			if aerr := retryConflictErr(err, "failed to commit supersede tx"); aerr != nil {
				return nil, false, aerr
			}
			return nil, false, NewErr(ErrInternalError, fmt.Sprintf("failed to commit supersede tx: %v", err))
		}
		tx = nil // the deferred Rollback becomes a no-op after a successful Commit
	}

	// aihub#74 Stream A: dual-write related links into memory_relations.
	// Keep attrs.related_ids write (above) UNCHANGED — Stream B / aihub#116 reads it;
	// do not remove until that stream switches reads to this table and drops the attrs write.
	// Skip target ids that don't exist to avoid FK violations on dangling refs,
	// and t.id <> $1 to skip self-links. The insert is non-fatal (attrs.related_ids
	// above stays the authoritative transitional copy), but a failure is logged so a
	// divergence between attrs and memory_relations is observable rather than silent.
	if len(req.RelatedMemoryIDs) > 0 {
		if _, relErr := pool.Exec(ctx, `
			INSERT INTO memory_relations (from_mem, to_mem, project)
			SELECT $1, t.id, $2 FROM memories t WHERE t.id = ANY($3) AND t.id <> $1
			ON CONFLICT DO NOTHING`,
			mem.ID, mem.Project, req.RelatedMemoryIDs,
		); relErr != nil {
			fmt.Fprintf(os.Stderr, "remember: memory_relations dual-write failed for %s: %v\n", mem.ID, relErr)
		}
	}

	// Emit memory_created event (non-critical, fire and forget)
	payload, _ := json.Marshal(map[string]any{
		"memory_id":  mem.ID,
		"type":       mem.Type,
		"project":    mem.Project,
		"visibility": mem.Visibility,
	})
	_, _ = pool.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
		VALUES ($1, $2, $3, $4, 'memory_created', $5, $6)`,
		NewID("evt"), req.WorkItemID, req.CallerUserID, req.CallerDisplay, payload, req.Project,
	)

	return mem, true, nil
}

// Memory dedup thresholds per design §7.7 / §11:
//   - High (≥ 0.85): treat as a duplicate match (strict mode → 409, suggest mode → annotate)
//   - Low  (0.65 - 0.85): partial match, suggest mode → annotate; below low → ignore
const (
	memoryDedupHigh = 0.85
	memoryDedupLow  = 0.65
)

// textDedupCheck returns the highest-similarity active memory whose Jaccard
// similarity with `content` is ≥ memoryDedupLow (0.65). The caller decides
// whether to error (strict) or annotate (suggest) based on `dedup_mode` and the
// returned similarity score (compared against memoryDedupHigh / memoryDedupLow).
//
// Returns (nil, nil) when no candidate exceeds the low threshold.
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

	// Pick the single highest-similarity candidate above the low threshold,
	// so the caller can compare against the high threshold itself.
	var best *Memory
	bestSim := 0.0
	for i := range candidates {
		sim := jaccardSimilarity(content, candidates[i].Content)
		if sim >= memoryDedupLow && sim > bestSim {
			best = &Memory{
				ID:      candidates[i].ID,
				Type:    candidates[i].Type,
				Content: candidates[i].Content,
			}
			bestSim = sim
		}
	}
	return best, nil
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

// ─── Type Enum ────────────────────────────────────────────────────────────────

// MemoryTypeEnum is the curated select list for memory types (aihub#70).
// Canonical 16 + actively-used {rule.coding, rule.work, fact.note} = 19.
// Select-UX list ONLY — server validation stays lenient (4-prefix check),
// so off-list-but-valid-prefix types are still accepted.
var MemoryTypeEnum = []string{
	"experience.debug", "experience.approach", "experience.pitfall", "experience.code",
	"fact.architecture", "fact.constraint", "fact.reference", "fact.note",
	"rule.scheduling", "rule.convention", "rule.process", "rule.coding", "rule.work",
	"methodology.spec", "methodology.plan", "methodology.review",
	"methodology.execute", "methodology.retro", "methodology.wrap_summary",
}

// PfRememberTypeEnum is MemoryTypeEnum minus methodology.* — pf_remember refuses
// methodology artifacts (they must be written via pf_save_artifact: wi-bound and
// credentialed, see handleRemember's methodology gate, aihub#210). Derived from
// MemoryTypeEnum so the two lists never drift.
var PfRememberTypeEnum = func() []string {
	out := make([]string, 0, len(MemoryTypeEnum))
	for _, t := range MemoryTypeEnum {
		if !strings.HasPrefix(t, "methodology.") {
			out = append(out, t)
		}
	}
	return out
}()

// MethodologyTypeEnum is the methodology.* subset of MemoryTypeEnum — the only
// types pf_save_artifact accepts. Exposing it as the tool's `type` enum lets
// contract-lint catch server-400 calls like pf_save_artifact(type="spec") /
// type="retro" (aihub#211). Derived from MemoryTypeEnum so the two never drift;
// mirror of PfRememberTypeEnum.
var MethodologyTypeEnum = func() []string {
	out := make([]string, 0, 6)
	for _, t := range MemoryTypeEnum {
		if strings.HasPrefix(t, "methodology.") {
			out = append(out, t)
		}
	}
	return out
}()

// ─── Commit (human annotation) ────────────────────────────────────────────────

// CommitAnchorArgs carries the optional anchor fields for CommitMemory (aihub#125).
// HeadingID/HeadingText are from aihub#124; Quote/Prefix/Suffix add text-selection
// anchoring. Callers that do not anchor any field should pass the zero value.
// Validation limits: Quote ≤ 2000 chars, Prefix/Suffix ≤ 64 chars each.
type CommitAnchorArgs struct {
	HeadingID   string
	HeadingText string
	Quote       string // exact selected text (≤2000 chars)
	Prefix      string // context before the selection (≤64 chars)
	Suffix      string // context after the selection (≤64 chars)
}

// CommitMemory appends a human annotation to the dedicated `commits` JSONB column.
// It does NOT touch activation_count, base_strength, stability_days, or
// last_activated_at — those fields are managed by the forgetting-curve path only.
// updated_at is refreshed automatically by the BEFORE UPDATE trigger trg_mem_updated_at.
// Write surface: UI only (POST /ui/memories/:id/commit and POST /ui/artifacts/:id/commit).
//
// anchor carries optional section/selection anchors (aihub#124 heading fields,
// aihub#125 quote/prefix/suffix). Pass zero value when no anchor is needed.
// Anchor object is written when HeadingID != "" OR Quote != "".
func CommitMemory(ctx context.Context, pool *pgxpool.Pool, memID, body, callerUserID, callerDisplay string, anchor CommitAnchorArgs) error {
	// Validate anchor field caps (aihub#125).
	if len(anchor.Quote) > 2000 {
		return NewErr(ErrPayloadTooLarge, "anchor quote exceeds 2000 characters")
	}
	if len(anchor.Prefix) > 64 {
		return NewErr(ErrPayloadTooLarge, "anchor prefix exceeds 64 characters")
	}
	if len(anchor.Suffix) > 64 {
		return NewErr(ErrPayloadTooLarge, "anchor suffix exceeds 64 characters")
	}

	var project, status string
	err := pool.QueryRow(ctx, `SELECT project, status FROM memories WHERE id=$1`, memID).
		Scan(&project, &status)
	if err != nil {
		return pgxErr(err, "memory not found", "failed to load memory")
	}
	if status == "redacted" {
		return NewErr(ErrForbidden, "cannot commit to a redacted memory")
	}

	entry := map[string]any{
		// aihub#70 v3: every entry carries an id so it can later be edited or
		// deleted by id. Existing rows without ids are backfilled by 0022.
		"id":             NewID("cm"),
		"author_user_id": callerUserID,
		"author_display": callerDisplay,
		"body":           body,
		"created_at":     time.Now().UTC().Format(time.RFC3339),
	}
	// aihub#124/125: include anchor object only when at least one anchor field is
	// set, so entries without anchors remain backward-compatible (no extra key).
	if anchor.HeadingID != "" || anchor.Quote != "" {
		am := map[string]string{
			"heading_id":   anchor.HeadingID,
			"heading_text": anchor.HeadingText,
		}
		if anchor.Quote != "" {
			am["quote"] = anchor.Quote
		}
		if anchor.Prefix != "" {
			am["prefix"] = anchor.Prefix
		}
		if anchor.Suffix != "" {
			am["suffix"] = anchor.Suffix
		}
		entry["anchor"] = am
	}
	entryJSON, _ := json.Marshal(entry)
	// Wrap as a single-element JSON array so || can append it to the existing array.
	entryArrayJSON := "[" + string(entryJSON) + "]"

	_, execErr := pool.Exec(ctx, `
		UPDATE memories
		SET commits = commits || $2::jsonb
		WHERE id = $1`,
		memID, entryArrayJSON,
	)
	if execErr != nil {
		return NewErr(ErrInternalError, fmt.Sprintf("failed to commit memory: %v", execErr))
	}

	// Emit memory_committed event (best-effort, fire-and-forget).
	payload, _ := json.Marshal(map[string]any{
		"memory_id":      memID,
		"author_user_id": callerUserID,
	})
	_, _ = pool.Exec(ctx, `
		INSERT INTO agent_events (id, actor_user_id, actor_display, event_type, payload, project)
		VALUES ($1, $2, $3, 'memory_committed', $4, $5)`,
		NewID("evt"), callerUserID, callerDisplay, payload, project,
	) //nolint:errcheck

	return nil
}

// ─── Commit edit / delete (aihub#70 v3) ───────────────────────────────────────

// findCommitEntry locates a commit by id inside the memory's commits JSONB
// column and returns its author_user_id (for the author-or-admin check).
// Returns ErrNotFound when the memory or commit id is missing.
func findCommitEntry(ctx context.Context, pool *pgxpool.Pool, memID, commitID string) (project, status, authorUserID string, err error) {
	row := pool.QueryRow(ctx, `
		SELECT m.project, m.status,
		       (SELECT entry->>'author_user_id'
		        FROM jsonb_array_elements(m.commits) AS entry
		        WHERE entry->>'id' = $2
		        LIMIT 1)
		FROM memories m WHERE m.id = $1`, memID, commitID)
	var authorPtr *string
	if e := row.Scan(&project, &status, &authorPtr); e != nil {
		err = pgxErr(e, "memory not found", "failed to load memory")
		return
	}
	if authorPtr == nil {
		err = NewErr(ErrNotFound, "commit not found")
		return
	}
	authorUserID = *authorPtr
	return
}

// checkCommitAuthor enforces the author-or-admin permission on edit/delete.
func checkCommitAuthor(entryAuthorUserID, callerUserID, callerRole string) error {
	if callerRole == "admin" || entryAuthorUserID == callerUserID {
		return nil
	}
	return NewErr(ErrForbidden, "only the commit author or an admin may modify this commit")
}

// EditCommit replaces the body of a single commit by id, sets updated_at,
// and emits memory_commit_edited. The commit's id, author and created_at
// fields are immutable. Forgetting-curve fields are not touched.
func EditCommit(ctx context.Context, pool *pgxpool.Pool, memID, commitID, body, callerUserID, callerDisplay, callerRole string) error {
	project, status, entryAuthor, err := findCommitEntry(ctx, pool, memID, commitID)
	if err != nil {
		return err
	}
	if status == "redacted" {
		return NewErr(ErrForbidden, "cannot edit a commit on a redacted memory")
	}
	if err := checkCommitAuthor(entryAuthor, callerUserID, callerRole); err != nil {
		return err
	}

	updatedAt := time.Now().UTC().Format(time.RFC3339)
	_, execErr := pool.Exec(ctx, `
		UPDATE memories
		SET commits = (
			SELECT jsonb_agg(
				CASE
					WHEN entry->>'id' = $2 THEN
						jsonb_set(
							jsonb_set(entry, '{body}', to_jsonb($3::text), true),
							'{updated_at}', to_jsonb($4::text), true
						)
					ELSE entry
				END
			)
			FROM jsonb_array_elements(commits) AS entry
		)
		WHERE id = $1`,
		memID, commitID, body, updatedAt,
	)
	if execErr != nil {
		return NewErr(ErrInternalError, fmt.Sprintf("failed to edit commit: %v", execErr))
	}

	// best-effort audit event
	payload, _ := json.Marshal(map[string]any{
		"memory_id":     memID,
		"commit_id":     commitID,
		"actor_user_id": callerUserID,
	})
	_, _ = pool.Exec(ctx, `
		INSERT INTO agent_events (id, actor_user_id, actor_display, event_type, payload, project)
		VALUES ($1, $2, $3, 'memory_commit_edited', $4, $5)`,
		NewID("evt"), callerUserID, callerDisplay, payload, project,
	) //nolint:errcheck
	return nil
}

// DeleteCommit removes a single commit by id from the commits array and emits
// memory_commit_deleted. Hard delete; no tombstone.
func DeleteCommit(ctx context.Context, pool *pgxpool.Pool, memID, commitID, callerUserID, callerDisplay, callerRole string) error {
	project, status, entryAuthor, err := findCommitEntry(ctx, pool, memID, commitID)
	if err != nil {
		return err
	}
	if status == "redacted" {
		return NewErr(ErrForbidden, "cannot delete a commit on a redacted memory")
	}
	if err := checkCommitAuthor(entryAuthor, callerUserID, callerRole); err != nil {
		return err
	}

	_, execErr := pool.Exec(ctx, `
		UPDATE memories
		SET commits = COALESCE((
			SELECT jsonb_agg(entry)
			FROM jsonb_array_elements(commits) AS entry
			WHERE entry->>'id' != $2
		), '[]'::jsonb)
		WHERE id = $1`,
		memID, commitID,
	)
	if execErr != nil {
		return NewErr(ErrInternalError, fmt.Sprintf("failed to delete commit: %v", execErr))
	}

	payload, _ := json.Marshal(map[string]any{
		"memory_id":     memID,
		"commit_id":     commitID,
		"actor_user_id": callerUserID,
	})
	_, _ = pool.Exec(ctx, `
		INSERT INTO agent_events (id, actor_user_id, actor_display, event_type, payload, project)
		VALUES ($1, $2, $3, 'memory_commit_deleted', $4, $5)`,
		NewID("evt"), callerUserID, callerDisplay, payload, project,
	) //nolint:errcheck
	return nil
}

// replyCommitSQL appends a new reply entry to a single commit's replies array
// inside the commits JSONB column. It is extracted as a constant so tests can
// assert the jsonb_set paths without a DB (same pattern as resolveCommitSQL).
//
// Parameters: $1=memID $2=commitID $3=replyJSON (a JSON object, not array)
const replyCommitSQL = `
		UPDATE memories
		SET commits = COALESCE((
			SELECT jsonb_agg(
				CASE
					WHEN entry->>'id' = $2 THEN
						jsonb_set(
							entry,
							'{replies}',
							COALESCE(entry->'replies', '[]'::jsonb) || $3::jsonb,
							true
						)
					ELSE entry
					END
			)
			FROM jsonb_array_elements(commits) AS entry
		), commits)
		WHERE id = $1`

// ReplyCommit appends a threaded reply to a single commit inside a memory's
// commits JSONB column. It emits memory_commit_replied (best-effort, same
// fire-and-forget pattern as ResolveCommit's memory_commit_resolved).
//
// Validation: body must be non-empty and ≤ 20000 chars (matching the
// memory body cap searched in domain code).
func ReplyCommit(ctx context.Context, pool *pgxpool.Pool, memID, commitID, authorUserID, authorDisplay, body string) error {
	if body == "" {
		return NewErr(ErrBadRequest, "reply body is required")
	}
	const maxBody = 20000
	if len(body) > maxBody {
		return NewErr(ErrPayloadTooLarge, fmt.Sprintf("reply body exceeds %d characters", maxBody))
	}

	project, status, _, err := findCommitEntry(ctx, pool, memID, commitID)
	if err != nil {
		return err
	}
	if status == "redacted" {
		return NewErr(ErrForbidden, "cannot reply to a commit on a redacted memory")
	}

	reply := map[string]any{
		"id":             NewID("cr"),
		"author_user_id": authorUserID,
		"author_display": authorDisplay,
		"body":           body,
		"created_at":     time.Now().UTC().Format(time.RFC3339),
	}
	replyJSON, _ := json.Marshal(reply)
	// Wrap as single-element array so || can append to the existing replies array.
	replyArrayJSON := "[" + string(replyJSON) + "]"

	_, execErr := pool.Exec(ctx, replyCommitSQL, memID, commitID, replyArrayJSON)
	if execErr != nil {
		return NewErr(ErrInternalError, fmt.Sprintf("failed to reply to commit: %v", execErr))
	}

	// Look up the memory's work_item_id for the event row.
	var wiID *string
	_ = pool.QueryRow(ctx, `SELECT work_item_id FROM memories WHERE id=$1`, memID).
		Scan(&wiID)

	// Emit memory_commit_replied (best-effort, fire-and-forget).
	payload, _ := json.Marshal(map[string]any{
		"memory_id":  memID,
		"commit_id":  commitID,
		"replied_by": authorDisplay,
	})
	_, _ = pool.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
		VALUES ($1, $2, $3, $4, 'memory_commit_replied', $5, $6)`,
		NewID("evt"), wiID, authorUserID, authorDisplay, payload, project,
	) //nolint:errcheck

	return nil
}

// ─── Recall ───────────────────────────────────────────────────────────────────

// Page-size bounds for Recall. recallTopKDefault is what a caller who names no
// page size gets; recallTopKCeiling is the largest page any caller can obtain.
//
// INVARIANT: recallTopKCeiling >= recallTopKDefault. A ceiling BELOW the default
// inverts the endpoint — asking for a bigger page then returns FEWER items than
// asking for nothing — and that inversion shipped (aihub#309): handleRecall
// carried a second cap of its own, `if req.TopK > 10 { req.TopK = 10 }`, three
// lines above the only call to Recall. Measured against production on 2026-09-01,
// same filter and total=220 throughout: top_k=30 -> 10 items, top_k unset -> 20
// items, top_k=300 -> 10 items.
//
// Deliberately unexported, and no test may derive an expectation from either: a
// fixture that reads the constant under test moves with the defect instead of
// catching it. The tests in memory_topk_test.go spell 20 and 200 out.
const (
	recallTopKDefault = 20
	recallTopKCeiling = 200
)

// normalizeRecallTopK resolves a requested page size into the one Recall uses.
//
// A non-positive request — the caller sent nothing, sent 0, or sent a negative —
// means "no page size named" and yields the default. That is the aihub#249
// contract: bad input falls back to the DEFAULT, never to a smaller page (case 5
// of TestHandleRecall_TotalAndLimitAlias pins it at the HTTP layer). Any positive
// request is honoured up to the ceiling.
//
// This is the ONLY place a recall page size is bounded, and callers must not add a
// cap of their own. A cap applied upstream of this function is invisible from
// here, so nothing can hold it to the invariant above — which is precisely how
// aihub#309 happened, and why the ceiling on the next line was unreachable, and
// therefore false, for as long as it did.
//
// Landing above the ceiling is still visible to the caller without a new response
// field: Total reports the full matching count independently of pagination on
// both the text and vector paths, so Total > len(Items) says a page was not the
// whole answer (NextCursor says it too, but only on the text path — see the note
// on paging in RecallWithVector). Total already survives the pf_recall slimming
// whitelist in internal/mcp/recall_slim.go. A dedicated `top_k_clamped` field
// would not — that whitelist is opt-in and drops anything unlisted — so
// disclosure by new field would have reached REST callers while silently missing
// the pf_recall callers who are the population that has the problem.
func normalizeRecallTopK(requested int) int {
	if requested <= 0 {
		return recallTopKDefault
	}
	if requested > recallTopKCeiling {
		return recallTopKCeiling
	}
	return requested
}

// Recall bounds the caller's page size, routes the request, and DISCLOSES the
// bound if it fired (aihub#314).
//
// 🔴 The disclosure is attached HERE, around the router, rather than at
// recallRouted's four return points — for the same reason handleRecall attaches
// unmatched_types around this call rather than inside it: four exits are four
// chances to forget one, and the one that gets forgotten is silent.
func Recall(ctx context.Context, pool *pgxpool.Pool, req *RecallRequest) (*RecallResponse, error) {
	adjusted := appendIntAdjustment(nil, "top_k", req.TopK, normalizeRecallTopK(req.TopK))
	resp, err := recallRouted(ctx, pool, req)
	if err != nil || resp == nil {
		return resp, err
	}
	resp.RequestAdjusted = adjusted
	return resp, nil
}

// recallRouted retrieves memories per §7.5. It is the router: it decides between the pgvector
// path (RecallWithVector) and the text/tag path (recallText), and — because those two
// paths own disjoint halves of the corpus — merges them when a request spans both.
func recallRouted(ctx context.Context, pool *pgxpool.Pool, req *RecallRequest) (*RecallResponse, error) {
	req.TopK = normalizeRecallTopK(req.TopK)
	if req.MinStrength <= 0 {
		req.MinStrength = 0.3
	}

	// aihub#192: route to vector path when embedding is active, a query is present,
	// and there is no work_item_id filter (wi-scoped recalls are deterministic, not
	// semantic — skip the vector path to avoid pulling unrelated memories).
	if !isNoopProvider(embProvider) && req.Query != "" && req.WorkItemID == nil {
		// aihub#270: the vector path's WHERE carries `emb_vector IS NOT NULL`, so it can
		// only ever return rows whose type is embeddable. Splitting the caller's filter
		// tells us which half of the request each path is actually able to answer.
		embTypes, nonEmbTypes := partitionTypesByEmbeddable(req.Types)

		// The complement runs only when the caller NAMED a type the vector path cannot
		// serve. An empty type filter deliberately does not qualify: a semantic recall with
		// no filter is asking "what is relevant to this query", and the text path has no
		// answer to that — it cannot score relevance, it only orders by reference time. So
		// topping such a request up would spend half its budget on whichever methodology.*
		// rows happen to be newest, regardless of the query, and spec/plan bodies are large.
		// Asking for methodology.spec by name is a different request, and gets the top-up.
		// (The common ["experience.*","rule.*"] recall names no such type either, so it also
		// pays nothing extra here.)
		needsTextComplement := len(nonEmbTypes) > 0

		// How SimilarityThreshold interacts with all of this, stated once:
		//
		//   A caller-set threshold gates the EMBEDDABLE half only.
		//
		// That half is the only one a cosine score can be computed for, so it is the only
		// one the threshold can mean anything about. Concretely: a threshold still
		// suppresses the "vector came back empty, retry over everything as text" fallback
		// (the case below) — that fallback is what the "empty is intended" rule was written
		// to prevent. It does not suppress rows of a non-embeddable type the caller named
		// explicitly, because those rows were never candidates for similarity filtering in
		// the first place. Requests with a purely embeddable filter — the only shape that
		// existed before non-embeddable types could be returned at all — behave exactly as
		// they did before aihub#270.

		// A filter naming *only* non-embeddable types (e.g.
		// ["methodology.spec","methodology.plan"]) has no vector candidates at all. Skip
		// straight to the text path rather than paying for an embed call whose result can
		// only be empty. Note
		// this reaches the text path even when a threshold is set — same rule as above:
		// nothing in this request could ever carry a similarity score.
		if len(req.Types) == 0 || len(embTypes) > 0 {
			vreq := *req
			// When req.Types is empty this stays empty, i.e. "no type filter" — correct,
			// because `emb_vector IS NOT NULL` already restricts that query to the
			// embeddable half on its own.
			vreq.Types = embTypes

			r, vecErr := RecallWithVector(ctx, pool, &vreq)
			switch {
			case vecErr != nil:
				fmt.Fprintf(os.Stderr, "recall: vector path failed, falling through to text path: %v\n", vecErr)
			case len(r.Items) == 0 && req.SimilarityThreshold <= 0:
				// No embedded candidates matched — e.g. the corpus is not yet backfilled,
				// or every stored emb_model differs from the current provider. Fall through
				// to the text/tag path so recall is never silently empty during the embedding
				// rollout window. (A caller-set SimilarityThreshold means empty is intended.)
				fmt.Fprintln(os.Stderr, "recall: vector path returned 0 items, falling through to text path")
			case needsTextComplement:
				// aihub#270: the vector half came back non-empty, which used to short-circuit
				// the whole call — and that is exactly how the non-embeddable types the caller
				// also asked for got dropped, silently, with a plausible-looking result set.
				// Top up from the text path instead.
				//
				// This branch also catches "empty vector half, but SimilarityThreshold > 0",
				// and that is deliberate: the threshold's "empty is intended" rule is about
				// suppressing the *semantic* fallback, and the non-embeddable half was never
				// subject to a similarity score in the first place (it has no vector to score).
				// So we still return its rows, and we still never run a full text query for
				// the embeddable types the threshold was meant to gate.
				return recallHybrid(ctx, pool, req, r, nonEmbTypes)
			default:
				return r, nil
			}
		}
	}

	return recallText(ctx, pool, req, false)
}

// recallHybrid completes a recall whose vector half (vec) is already in hand but whose
// type filter also names rows the vector path can never return. It runs the text path
// over exactly the non-embeddable complement and interleaves the two result lists.
//
// nonEmbTypes is the caller's filter narrowed to its non-embeddable entries, and is always
// non-empty here — Recall only reaches this path when the caller named such a type.
func recallHybrid(ctx context.Context, pool *pgxpool.Pool, req *RecallRequest, vec *RecallResponse, nonEmbTypes []string) (*RecallResponse, error) {
	creq := *req
	creq.Types = nonEmbTypes
	// A merged page has no coherent cursor position (the two halves are ordered by
	// different keys), so neither consume an incoming cursor nor emit one — same reasoning
	// the vector path already documents for its fusion ordering.
	creq.Cursor = ""

	txt, err := recallText(ctx, pool, &creq, true)
	if err != nil {
		// Degrade to vector-only rather than failing a recall that already has results.
		// This is the pre-aihub#270 behaviour, so the worst case is the old bug, not an error.
		fmt.Fprintf(os.Stderr, "recall: text complement failed, returning vector results only: %v\n", err)
		return vec, nil
	}

	return mergeRecallHalves(vec, txt, req.TopK), nil
}

// mergeRecallHalves interleaves two disjoint recall result lists round-robin and truncates
// to topK.
//
// Interleaving rather than concatenating is the point: the two halves are ranked by
// incomparable keys (the vector half by 0.7*cosine + 0.3*tanh(strength), the text half by
// reference time), so there is no honest way to sort them into one list — and any scheme
// that appends one after the other reintroduces the aihub#270 starvation as soon as the
// first half alone fills topK. Round-robin guarantees each half gets its share of the
// budget while preserving the internal order of both.
func mergeRecallHalves(vec, txt *RecallResponse, topK int) *RecallResponse {
	merged := make([]MemoryWithStrength, 0, topK)
	seen := make(map[string]bool, topK)

	// The halves are disjoint by construction (embeddable vs non-embeddable types), but
	// dedupe anyway so a future change to the partition can't produce duplicate rows.
	push := func(m MemoryWithStrength) {
		if len(merged) >= topK || seen[m.ID] {
			return
		}
		seen[m.ID] = true
		merged = append(merged, m)
	}

	for i := 0; i < len(vec.Items) || i < len(txt.Items); i++ {
		if len(merged) >= topK {
			break
		}
		if i < len(vec.Items) {
			push(vec.Items[i])
		}
		if i < len(txt.Items) {
			push(txt.Items[i])
		}
	}

	if len(merged) == 0 {
		// Match the text path, which leaves Items nil rather than empty, so the JSON is
		// `null` on both paths instead of varying with which one served the request.
		merged = nil
	}

	return &RecallResponse{
		Items: merged,
		// The halves count disjoint sets, so summing double-counts nothing. It is still
		// the size of what these two queries can see, not of the whole corpus: the vector
		// half's own filter excludes rows with a NULL or stale emb_model, so during a
		// backfill window an embeddable row that neither half matched is counted by
		// neither. That is the vector path's pre-existing blind spot, inherited here.
		Total: vec.Total + txt.Total,
		// NextCursor stays nil: see the cursor note in recallHybrid.
	}
}

// recallText is the text/tag search path (§7.5).
//
// nonEmbeddableOnly restricts the query to rows embeddableType reports false for. Recall
// sets it when this query is the complement half of a hybrid recall, so the text half
// cannot re-return rows the vector half already covers.
//
// The narrowed type filter recallHybrid passes already implies that restriction today (a
// non-embeddable filter entry cannot match an embeddable row). This flag enforces it at
// the SQL level anyway, so the disjointness that mergeRecallHalves' deduping and the
// summed Total both rely on is a property of the query rather than of a subtle argument
// about the partition — and stays true if that partition is ever reworked.
func recallText(ctx context.Context, pool *pgxpool.Pool, req *RecallRequest, nonEmbeddableOnly bool) (*RecallResponse, error) {
	// NOTE: RecencyWeight is currently a reserved-but-unused knob. The text/tag
	// recall path orders by memRefTimeSQL (see ORDER BY below) and does not blend
	// a separate recency score. The default is intentionally not set here so the
	// field stays an explicit no-op rather than a misleading "applied" value;
	// implementing recency blending is tracked separately.

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

	// Visibility: private memories only visible to author (C2 fix: 'personal' → 'private');
	// admin-tier memories only visible to users with global role='admin'.
	if req.CallerRole != "admin" {
		where += fmt.Sprintf(` AND (visibility != 'private' OR author_user_id = $%d)`, idx)
		args = append(args, req.CallerUserID)
		idx++
		where += ` AND visibility != 'admin'`
	}

	// Type filter with prefix matching. aihub#289 moved the rule into
	// typeFilterClause (memory_unmatched.go) so the unmatched-type diagnostic can
	// ask "would this entry have matched?" against the same code, not a Go
	// re-implementation of it. Rendering is byte-identical to the inline version
	// this replaces, parentheses included.
	if clause, clauseArgs, nextIdx := typeFilterClause(req.Types, idx); clause != "" {
		where += " AND " + clause
		args = append(args, clauseArgs...)
		idx = nextIdx
	}

	// aihub#270: when this query is the complement half of a hybrid recall, restrict it to
	// the rows the vector half structurally cannot return. This is what makes the two
	// halves disjoint — and it is placed here, above the count, so Total counts the same
	// set the SELECT returns.
	if nonEmbeddableOnly {
		clause, clauseArgs, nextIdx := nonEmbeddableTypeClause(idx)
		where += " AND " + clause
		args = append(args, clauseArgs...)
		idx = nextIdx
	}

	if req.WorkItemID != nil {
		where += fmt.Sprintf(" AND work_item_id = $%d", idx)
		args = append(args, *req.WorkItemID)
		idx++
	}

	// H9: min_strength filter in SQL (not Go-side post-LIMIT) using inline Ebbinghaus formula.
	// immortal memories bypass the filter.
	// Formula: base_strength * exp(-days_since / stability_days) >= min_strength
	where += fmt.Sprintf(` AND (is_immortal = true OR (stability_days > 0 AND
		base_strength * exp(
			-extract(epoch from (clock_timestamp() - `+memRefTimeSQL+`))/86400.0
			/ stability_days
		) >= $%d))`, idx)
	args = append(args, req.MinStrength)
	idx++

	// aihub#249: total is COUNT(*) over every filter above, taken BEFORE the
	// cursor predicate is appended below — it must report the size of the whole
	// matching set, not "rows remaining from this cursor onward". `where`/`args`
	// are safe to reuse here even though both are extended further down: `where`
	// is a Go string (immutable — later `+=` rebinds the variable, it doesn't
	// mutate this value) and `args` is only ever appended to, never rewritten at
	// an existing index, so this snapshot's contents can't be altered by later
	// appends. This total applies to both the plain and lexical branches below,
	// since the lexical branch's own comment notes it doesn't use the cursor.
	total, terr := countMemories(ctx, pool, where, args)
	if terr != nil {
		return nil, NewErr(ErrInternalError, fmt.Sprintf("recall count query: %v", terr))
	}

	// Cursor-based pagination. memRefTimeSQL is a single total expression (no
	// NULL branch — the previous two-branch form could not express "the next row
	// after this one" once ordering crossed the activated/never-activated
	// boundary, and silently skipped rows, aihub#236), but it is not by itself
	// unique, so the cursor must carry the `id DESC` tiebreaker from the ORDER BY
	// too. A row-value comparison is the exact inverse of that two-key ordering:
	// it admits rows strictly older by reference time, plus rows at the SAME
	// reference time with a smaller id — the ones a timestamp-only `<` dropped
	// from every page after the tie (aihub#239). `id` is already the ORDER BY
	// tiebreaker, so this needs no index or ordering change, and both halves are
	// NOT NULL (created_at is NOT NULL, so GREATEST is total) which keeps the
	// comparison from going NULL.
	if req.Cursor != "" {
		curTS, curID := parseRecallCursor(req.Cursor)
		if curID != "" {
			where += fmt.Sprintf(` AND (`+memRefTimeSQL+`, id) < ($%d::timestamptz, $%d)`, idx, idx+1)
			args = append(args, curTS, curID)
			idx += 2
		} else {
			// Pre-aihub#239 cursor: timestamp only. Keep the old single-key
			// semantics rather than inventing an id bound — `(ts, id) < (ts0, '')`
			// would drop every row at ts0, which is worse than the tie bug.
			where += fmt.Sprintf(` AND `+memRefTimeSQL+` < $%d::timestamptz`, idx)
			args = append(args, curTS)
			idx++
		}
	}

	// opt③ L1 recall precision (RecallAlgo=="lexical"): fuse lexical relevance
	// (ts_rank over content_tsv vs the query) with the strength/recency prior via
	// Reciprocal Rank Fusion, so req.Query actually drives ranking. Default
	// (""/"recency") keeps the query-blind recency order verbatim — a zero-behavior-
	// change opt-in. Lexical path skips cursor paging (fusion score is incompatible
	// with the timestamp cursor); query terms that match nothing tie rlex, so ranking
	// falls back to the strength prior and recall is preserved.
	var query string
	if req.RecallAlgo == "lexical" && req.Query != "" {
		limitIdx := idx
		args = append(args, req.TopK)
		qIdx := idx + 1
		args = append(args, req.Query)
		query = fmt.Sprintf(`
		SELECT id, project, type, content, author_user_id, author_display,
			work_item_id, visibility, is_immortal, base_strength, stability_days,
			last_activated_at, last_activated_by, activation_count, expires_at,
			tags, source_artifact_id, status, attrs, commits, latest_id, created_at, updated_at
		FROM memories
		WHERE %s
		ORDER BY ts_rank(content_tsv, replace(plainto_tsquery('english', $%d)::text, ' & ', ' | ')::tsquery) DESC,
			tanh(base_strength * exp(
				-extract(epoch from (clock_timestamp() - `+memRefTimeSQL+`))/86400.0
				/ NULLIF(stability_days, 0))) DESC
		LIMIT $%d`, where, qIdx, limitIdx)
	} else {
		args = append(args, req.TopK+1)
		query = fmt.Sprintf(`
		SELECT id, project, type, content, author_user_id, author_display,
			work_item_id, visibility, is_immortal, base_strength, stability_days,
			last_activated_at, last_activated_by, activation_count, expires_at,
			tags, source_artifact_id, status, attrs, commits, latest_id, created_at, updated_at
		FROM memories
		WHERE %s
		ORDER BY `+memRefTimeSQL+` DESC, id DESC
		LIMIT $%d`, where, idx)
	}

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, NewErr(ErrInternalError, fmt.Sprintf("recall query: %v", err))
	}
	defer rows.Close()

	var items []MemoryWithStrength
	for rows.Next() {
		m, err := scanMemoryLite(rows)
		if err != nil {
			fmt.Fprintf(os.Stderr, "recall: scanMemoryLite error (possible column drift): %v\n", err)
			continue
		}
		strength := MemoryStrength(m.BaseStrength, m.StabilityDays, m.LastActivatedAt, m.CreatedAt)
		// min_strength filter is now in SQL (H9); this is just for the EffectiveStrength field
		items = append(items, MemoryWithStrength{Memory: *m, EffectiveStrength: strength})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, NewErr(ErrInternalError, fmt.Sprintf("recall rows error: %v", err))
	}

	var nextCursor *string
	if len(items) > req.TopK {
		items = items[:req.TopK]
		last := items[len(items)-1]
		// Cursor is the last row's full sort position — reference time (computed
		// by the same rule as memRefTimeSQL) AND the id tiebreaker — so the next
		// page resumes exactly where this one ended even when several rows share
		// that reference time (aihub#239).
		cursorVal := formatRecallCursor(memoryRefTime(last.LastActivatedAt, last.CreatedAt), last.ID)
		nextCursor = &cursorVal
	}

	// aihub#74 Stream A: enrich list results with forward links (no backlinks in list to keep it lean).
	if len(items) > 0 {
		ids := make([]string, len(items))
		for i := range items {
			ids[i] = items[i].ID
		}
		forwardMap, ferr := loadForwardRelations(ctx, pool, ids, req.Project, req.CallerUserID, req.CallerRole)
		if ferr != nil {
			// Non-fatal: log and continue without enrichment.
			fmt.Fprintf(os.Stderr, "recall: loadForwardRelations error: %v\n", ferr)
		} else {
			for i := range items {
				if refs, ok := forwardMap[items[i].ID]; ok {
					items[i].Related = refs
				}
			}
		}
	}

	return &RecallResponse{Items: items, NextCursor: nextCursor, Total: total}, nil
}

// countMemories runs a COUNT(*) over the memories table with the given WHERE
// clause/args, shared by Recall (text path) and RecallWithVector (vector path)
// so the reported `total` always reflects exactly the predicate that produced
// the page of items, and the two paths cannot silently drift apart (aihub#249).
func countMemories(ctx context.Context, pool *pgxpool.Pool, where string, args []any) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM memories WHERE `+where, args...).Scan(&n)
	return n, err
}

// scanMemoryLite scans a lightweight memory row for LLM recall (aihub#102).
// It omits rendered_html, emb_model, and emb_dims — fields the LLM never
// needs — halving the token cost for methodology.spec/plan recalls.
//
// Column order MUST match Recall's SELECT exactly (positional scan):
//
//	id, project, type, content, author_user_id, author_display,
//	work_item_id, visibility, is_immortal, base_strength, stability_days,
//	last_activated_at, last_activated_by, activation_count, expires_at,
//	tags, source_artifact_id, status, attrs, commits, latest_id, created_at, updated_at
func scanMemoryLite(rows pgx.Rows) (*Memory, error) {
	m := &Memory{}
	err := rows.Scan(
		&m.ID, &m.Project, &m.Type, &m.Content, &m.AuthorUserID, &m.AuthorDisplay,
		&m.WorkItemID, &m.Visibility, &m.IsImmortal, &m.BaseStrength, &m.StabilityDays,
		&m.LastActivatedAt, &m.LastActivatedBy, &m.ActivationCount, &m.ExpiresAt,
		&m.Tags, &m.SourceArtifactID, &m.Status,
		&m.Attrs, &m.Commits, &m.LatestID, &m.CreatedAt, &m.UpdatedAt,
	)
	return m, err
}

// ─── Relation helpers (aihub#74) ─────────────────────────────────────────────

// loadForwardRelations returns from_mem → []RelatedRef for a set of source memory ids.
// Each RelatedRef carries the target's id, type, and a content snippet (≤120 chars).
// Executes ONE query (no N+1). Returns an empty map for an empty ids slice.
func loadForwardRelations(ctx context.Context, pool *pgxpool.Pool, ids []string, project, callerUserID, callerRole string) (map[string][]RelatedRef, error) {
	if len(ids) == 0 {
		return map[string][]RelatedRef{}, nil
	}
	// Apply the SAME visibility/project scoping Recall enforces, so a related target
	// cannot leak a private / admin / cross-project memory's id, type, or content
	// snippet through the relation graph. Mirrors the predicate at Recall (the
	// `CallerRole != "admin"` block).
	where := "r.from_mem = ANY($1) AND m.project = $2 AND m.status != 'redacted' AND (m.expires_at IS NULL OR m.expires_at > clock_timestamp())"
	args := []any{ids, project}
	if callerRole != "admin" {
		where += " AND (m.visibility != 'private' OR m.author_user_id = $3) AND m.visibility != 'admin'"
		args = append(args, callerUserID)
	}
	rows, err := pool.Query(ctx, `
		SELECT r.from_mem, r.to_mem, m.type, left(m.content, 120)
		FROM memory_relations r
		JOIN memories m ON m.id = r.to_mem
		WHERE `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("loadForwardRelations: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]RelatedRef, len(ids))
	for rows.Next() {
		var fromMem, toMem, memType, snippet string
		if err := rows.Scan(&fromMem, &toMem, &memType, &snippet); err != nil {
			return nil, fmt.Errorf("loadForwardRelations scan: %w", err)
		}
		result[fromMem] = append(result[fromMem], RelatedRef{ID: toMem, Type: memType, Summary: snippet})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("loadForwardRelations rows: %w", err)
	}
	return result, nil
}

// GetMemoryByID loads a single active or archived memory by primary key.
// Returns ErrNotFound when the row is missing or has status='redacted'.
// Used by the artifact HTML viewer endpoint (aihub#27).
//
// Column order MUST mirror the INSERT/RETURNING in Remember and the Memory struct
// field order (positional scan — silent corruption if anything drifts).
func GetMemoryByID(ctx context.Context, pool *pgxpool.Pool, id string) (*Memory, *AihubError) {
	m := &Memory{}
	err := pool.QueryRow(ctx, `
		SELECT id, project, type, content, author_user_id, author_display,
			work_item_id, visibility, is_immortal, base_strength, stability_days,
			last_activated_at, last_activated_by, activation_count, expires_at,
			tags, source_artifact_id, emb_model, emb_dims, status, attrs,
			rendered_html, commits, latest_id, created_at, updated_at
		FROM memories
		WHERE id = $1 AND status != 'redacted'`, id,
	).Scan(
		&m.ID, &m.Project, &m.Type, &m.Content, &m.AuthorUserID, &m.AuthorDisplay,
		&m.WorkItemID, &m.Visibility, &m.IsImmortal, &m.BaseStrength, &m.StabilityDays,
		&m.LastActivatedAt, &m.LastActivatedBy, &m.ActivationCount, &m.ExpiresAt,
		&m.Tags, &m.SourceArtifactID, &m.EmbModel, &m.EmbDims, &m.Status,
		&m.Attrs, &m.RenderedHTML, &m.Commits, &m.LatestID, &m.CreatedAt, &m.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, NewErr(ErrNotFound, "memory not found")
		}
		return nil, NewErr(ErrInternalError, fmt.Sprintf("failed to load memory: %v", err))
	}

	// aihub#74 Stream A: single-memory related/backlinks enrichment is deferred to the
	// follow-up that wires it into a handler with caller-scoped visibility — GetMemoryByID
	// has no caller identity here, and populating .Related/.Backlinks without the visibility
	// predicate would leak private/admin/cross-project memories. Recall enriches forward
	// links with full scoping (see loadForwardRelations).
	return m, nil
}

// GetLatestByID resolves id to the current head of its supersede lineage via
// the latest_id cursor, then loads that head through GetMemoryByID (so it
// never duplicates GetMemoryByID's column list — avoiding a 7th lockstep
// drift site). If id does not exist at all, the initial COALESCE(latest_id,
// id) lookup itself returns pgx.ErrNoRows and this returns ErrNotFound
// directly — it never reaches GetMemoryByID. The "fall back to id itself"
// behavior (COALESCE) only applies to an EXISTING row whose latest_id is
// NULL (pre-migration data written before latest_id was backfilled/self-set).
func GetLatestByID(ctx context.Context, pool *pgxpool.Pool, id string) (*Memory, *AihubError) {
	var head string
	err := pool.QueryRow(ctx, `
		SELECT COALESCE(latest_id, id) FROM memories WHERE id = $1`, id,
	).Scan(&head)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, NewErr(ErrNotFound, "memory not found")
		}
		return nil, NewErr(ErrInternalError, fmt.Sprintf("failed to resolve latest memory: %v", err))
	}
	return GetMemoryByID(ctx, pool, head)
}

// UpdateMemoryRequest is the body for PATCH /v1/memories/:id/update. Any nil
// (or empty-slice, for Tags) field inherits the current lineage head's value.
type UpdateMemoryRequest struct {
	Content      *string
	Visibility   *string
	Tags         []string
	BaseStrength *float64
	Attrs        json.RawMessage
	// Set by handler from Bearer token — not from JSON body.
	CallerUserID  string
	CallerDisplay string
}

// UpdateMemory creates a NEW version superseding the lineage head resolved
// from id (any id in the lineage), inheriting unchanged fields from that
// head, and advances the latest_id cursor across the whole lineage (via
// Remember's supersede-propagation logic). Returns the new head.
func UpdateMemory(ctx context.Context, pool *pgxpool.Pool, id string, req *UpdateMemoryRequest) (*Memory, error) {
	head, aerr := GetLatestByID(ctx, pool, id)
	if aerr != nil {
		return nil, aerr
	}
	rr := &RememberRequest{
		Project:         head.Project,
		Type:            head.Type,
		WorkItemID:      head.WorkItemID,
		Visibility:      head.Visibility,
		Tags:            head.Tags,
		Content:         head.Content,
		DedupMode:       "off", // updating a memory is an explicit edit, not a fresh dedup-checked remember
		CallerUserID:    req.CallerUserID,
		CallerDisplay:   req.CallerDisplay,
		SupersedesMemID: &head.ID,
		// aihub#236: a new version inherits the lineage's activation history —
		// without it each edit reset the head to activation_count=0 /
		// last_activated_at=NULL, stranding the history on the archived row.
		//
		// aihub#239 moved that carry INTO Remember, which now inherits the trio
		// from the head it actually archives, so this request deliberately leaves
		// the three fields unset. Passing head's values from here would be worse,
		// not merely redundant: the `head` above comes from an unlocked
		// GetLatestByID taken before Remember opens its transaction and acquires
		// the per-lineage advisory lock, so a pf_activate_memory bump landing in
		// that window would be silently overwritten by the pre-bump values.
		// Remember reads under the lock, after the archive, and is the single
		// source of truth for the trio.
	}
	if req.Content != nil {
		rr.Content = *req.Content
	}
	if req.Visibility != nil {
		rr.Visibility = *req.Visibility
	}
	if req.Tags != nil {
		rr.Tags = req.Tags
	}
	if req.BaseStrength != nil {
		rr.BaseStrength = req.BaseStrength
	} else {
		bs := head.BaseStrength
		rr.BaseStrength = &bs
	}
	if len(req.Attrs) > 0 {
		rr.Attrs = req.Attrs
	} else {
		rr.Attrs = head.Attrs
	}
	m, _, err := Remember(ctx, pool, rr)
	return m, err
}

// PreShareVisibilityKey is the attrs key in which SetMemoryVisibility parks the
// visibility tier a memory held immediately before it was made public, so unshare
// can put it back (aihub#151). It is written and cleared by SetMemoryVisibility
// alone; nothing else should set it.
const PreShareVisibilityKey = "pre_share_visibility"

// SetMemoryVisibility updates a single memory's visibility tier. Used by the artifact
// share endpoints to toggle public/project.
//
// It also maintains attrs.pre_share_visibility, in the SAME statement as the column
// write so the two can never disagree:
//
//   - moving TO 'public' from anything else records the tier being left behind;
//   - moving to anything other than 'public' clears the key, because the memory is
//     no longer in the borrowed-visibility state the key describes.
//
// aihub#151: unshare used to hard-code 'project' as the restore target, which
// WIDENED access for any memory that was 'private' (author-only) or 'admin' before
// it was shared — a share→unshare round trip published it to the whole project.
// Restoring needs the pre-share tier to have been written down at share time, and
// the row's own attrs is the only place that survives a process restart.
//
// Recording on the way in rather than on the way out matters: at unshare time the
// column already reads 'public' and the original tier is unrecoverable.
func SetMemoryVisibility(ctx context.Context, pool *pgxpool.Pool, id, visibility string) *AihubError {
	tag, err := pool.Exec(ctx,
		`UPDATE memories
		    SET visibility = $1,
		        attrs = CASE
		                  WHEN $1 = 'public' AND visibility <> 'public'
		                    THEN jsonb_set(COALESCE(attrs, '{}'::jsonb), $3::text[], to_jsonb(visibility))
		                  WHEN $1 <> 'public'
		                    THEN COALESCE(attrs, '{}'::jsonb) - $2::text
		                  ELSE attrs
		                END,
		        updated_at = clock_timestamp()
		  WHERE id = $4`,
		visibility, PreShareVisibilityKey, []string{PreShareVisibilityKey}, id)
	if err != nil {
		return NewErr(ErrInternalError, "failed to update memory visibility")
	}
	if tag.RowsAffected() == 0 {
		return NewErr(ErrNotFound, "memory not found")
	}
	return nil
}

// ─── Activate (§7.3) ──────────────────────────────────────────────────────────

// activationTargetStatus returns the status an activated memory should take.
// Activation normally revives a memory to "active" (it was just used). The one
// exception (aihub#214): a superseded (archived) methodology.* artifact stays
// archived — activate is an unauthenticated read-side recall signal and must not
// resurrect a stale spec/plan head over its successor.
func activationTargetStatus(curStatus, memType string) string {
	if curStatus == "archived" && strings.HasPrefix(memType, "methodology.") {
		return "archived"
	}
	return "active"
}

// Activate reinforces a memory: increments activation_count, recomputes stability_days,
// resets last_activated_at, and revives archived memories (except a superseded
// methodology.* artifact, which stays archived — see activationTargetStatus).
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
		return nil, pgxErr(err, "memory not found", "failed to load memory")
	}
	if status == "redacted" {
		return nil, NewErr(ErrForbidden, "cannot activate a redacted memory")
	}

	newCount := activationCount + 1
	newStability := computeStabilityDays(memType, newCount)

	// aihub#214: activation revives an archived memory to active — correct for
	// experience/fact/rule (used again -> live again), but WRONG for a superseded
	// (archived) methodology.* artifact: activate is an unauthenticated read-side
	// recall signal, so it must not resurrect a stale spec/plan head. Keep an
	// archived methodology.* archived; every other case still revives to active.
	newStatus := activationTargetStatus(status, memType)

	var newLastActivatedAt time.Time
	err = pool.QueryRow(ctx, `
		UPDATE memories
		SET activation_count   = $1,
		    stability_days     = $2,
		    last_activated_at  = clock_timestamp(),
		    last_activated_by  = $3,
		    status             = $4,
		    updated_at         = clock_timestamp()
		WHERE id = $5
		RETURNING last_activated_at`,
		newCount, newStability, callerUserID, newStatus, memID,
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
	_, _ = pool.Exec(ctx, `
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
		return pgxErr(err, "memory not found", "failed to load memory")
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

	// aihub#201 BUG2: if the row we just redacted was a lineage head (some row
	// — possibly itself — has latest_id = memID), that cursor now points at a
	// redacted row. GetMemoryByID filters out status='redacted', so the whole
	// lineage would 404 via GetLatestByID until repointed. Reuse migration
	// 0026's component/head-selection logic (root_walk up, down_walk down,
	// newest-non-redacted-first) scoped to just this row's component.
	if err := repointHeadIfRedacted(ctx, pool, memID); err != nil {
		return err
	}
	return nil
}

// repointHeadIfRedacted checks whether redactedID was a lineage head (i.e.
// some row's latest_id still points at it) and, if so, repoints every such
// row's latest_id at the newest non-redacted row in the connected component.
// If the whole component is now redacted, latest_id is left pointing at
// redactedID — the lineage is fully deleted, so a 404 is correct.
func repointHeadIfRedacted(ctx context.Context, pool *pgxpool.Pool, redactedID string) error {
	var wasHead bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM memories WHERE latest_id = $1)`, redactedID,
	).Scan(&wasHead); err != nil {
		return NewErr(ErrInternalError, fmt.Sprintf("failed to check lineage head: %v", err))
	}
	if !wasHead {
		return nil
	}

	var newHead string
	err := pool.QueryRow(ctx, `
		WITH RECURSIVE root_walk(cur_id) AS (
			SELECT id FROM memories WHERE id = $1
			UNION ALL
			SELECT m.supersedes_id
			FROM root_walk rw
			JOIN memories m ON m.id = rw.cur_id
			WHERE m.supersedes_id IS NOT NULL
		),
		root AS (
			SELECT rw.cur_id AS root_id
			FROM root_walk rw
			JOIN memories m ON m.id = rw.cur_id
			WHERE m.supersedes_id IS NULL
		),
		down_walk(member_id) AS (
			SELECT root_id FROM root
			UNION ALL
			SELECT m.id
			FROM down_walk dw
			JOIN memories m ON m.supersedes_id = dw.member_id
		)
		SELECT m.id
		FROM down_walk dw
		JOIN memories m ON m.id = dw.member_id
		ORDER BY (m.status = 'redacted') ASC, m.created_at DESC
		LIMIT 1`, redactedID,
	).Scan(&newHead)
	if err != nil {
		return NewErr(ErrInternalError, fmt.Sprintf("failed to resolve component head: %v", err))
	}
	if newHead == redactedID {
		// Every member is redacted (or this row has no component) — nothing to repoint.
		return nil
	}

	if _, err := pool.Exec(ctx,
		`UPDATE memories SET latest_id = $1 WHERE latest_id = $2`, newHead, redactedID,
	); err != nil {
		return NewErr(ErrInternalError, fmt.Sprintf("failed to repoint lineage head: %v", err))
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
// Per §5.2 (pf_emit_event H10): the design lists attempt_superseded,
// admin_force_takeover, admin_unblock, admin_redact as the admin-only set;
// the server also emits these via the same path.
var adminEventWhitelist = map[string]bool{
	"admin_unblock":             true,
	"admin_force_takeover":      true,
	"admin_redact":              true,
	"phase_config_updated":      true,
	"wi_needs_attention":        true,
	"wi_classification_missing": true,
	"attempt_superseded":        true,
}

// adminOnlyEventTypes are event_types that ALWAYS require admin role, regardless of
// whether req.Admin is set. This prevents event-type forgery (H6 fix): a non-admin
// caller setting admin=false but using an admin event type would otherwise bypass the
// req.Admin gate.
var adminOnlyEventTypes = map[string]bool{
	"admin_redact":         true,
	"admin_unblock":        true,
	"admin_force_takeover": true,
	"admin_gc_manual":      true,
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

	var attemptIDArg *string
	if req.AttemptID != "" {
		attemptIDArg = &req.AttemptID
	}

	// Resolve work_item_id (may be a slug like "aihub#1") to the canonical
	// work_items.id before the agent_events insert below, which FK-references
	// work_items(id). Passing a raw slug violates the FK. (aihub#127)
	var wiIDArg *string
	var project *string
	if req.WorkItemID != "" {
		wi, err := GetWorkItem(ctx, pool, req.WorkItemID)
		if err != nil {
			return "", err
		}
		canonicalID := wi.ID
		wiIDArg = &canonicalID
		project = &wi.Project
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

// ─── Resolve commit (aihub#124) ───────────────────────────────────────────────

// resolveCommitSQL is the UPDATE that atomically rewrites a single commit entry
// inside the commits JSONB array. Extracted so tests can assert the exact
// jsonb_set paths without hitting a real database.
//
// The status value "resolved" is inlined as a literal so the const itself
// encodes the invariant (tests can grep for it without a DB).
//
// Parameters: $1=memID $2=commitID $3=reply $4=resolvedAt $5=callerDisplay
const resolveCommitSQL = `
		UPDATE memories
		SET commits = (
			SELECT jsonb_agg(
				CASE
					WHEN entry->>'id' = $2 THEN
						jsonb_set(
							jsonb_set(
								jsonb_set(
									jsonb_set(entry, '{status}', '"resolved"', true),
									'{reply}', to_jsonb($3::text), true
								),
								'{resolved_at}', to_jsonb($4::text), true
							),
							'{resolved_by}', to_jsonb($5::text), true
						)
					ELSE entry
				END
			)
			FROM jsonb_array_elements(commits) AS entry
		)
		WHERE id = $1`

// ResolveCommit marks a single commit entry as resolved: sets status="resolved",
// reply, resolved_at (RFC3339 UTC), and resolved_by. It then emits
// memory_commit_resolved carrying the memory's work_item_id (which may be NULL
// when the artifact has no associated work item — the event INSERT is
// fire-and-forget and will silently fail the chk_evt_work_item_id constraint in
// that case, matching the behaviour of memory_committed / memory_commit_edited).
func ResolveCommit(ctx context.Context, pool *pgxpool.Pool, memID, commitID, reply, callerUserID, callerDisplay string) error {
	project, status, _, err := findCommitEntry(ctx, pool, memID, commitID)
	if err != nil {
		return err
	}
	if status == "redacted" {
		return NewErr(ErrForbidden, "cannot resolve a commit on a redacted memory")
	}

	resolvedAt := time.Now().UTC().Format(time.RFC3339)
	_, execErr := pool.Exec(ctx, resolveCommitSQL,
		memID, commitID, reply, resolvedAt, callerDisplay,
	)
	if execErr != nil {
		return NewErr(ErrInternalError, fmt.Sprintf("failed to resolve commit: %v", execErr))
	}

	// Look up the memory's work_item_id for the event row.
	// project is already known from findCommitEntry above.
	var wiID *string
	_ = pool.QueryRow(ctx, `SELECT work_item_id FROM memories WHERE id=$1`, memID).
		Scan(&wiID)

	// Emit memory_commit_resolved (best-effort, fire-and-forget).
	// NOTE: when work_item_id IS NULL and event_type is not in the constraint
	// whitelist, the INSERT will fail silently — this matches memory_committed /
	// memory_commit_edited behaviour for artifact memories without a wi.
	payload, _ := json.Marshal(map[string]any{
		"memory_id":   memID,
		"commit_id":   commitID,
		"resolved_by": callerDisplay,
	})
	_, _ = pool.Exec(ctx, `
		INSERT INTO agent_events (id, work_item_id, actor_user_id, actor_display, event_type, payload, project)
		VALUES ($1, $2, $3, $4, 'memory_commit_resolved', $5, $6)`,
		NewID("evt"), wiID, callerUserID, callerDisplay, payload, project,
	) //nolint:errcheck

	return nil
}

// ─── Version chain (aihub#124) ────────────────────────────────────────────────

// MemoryVersionRef is a lightweight entry in a memory's supersede lineage.
type MemoryVersionRef struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"` // RFC3339
	Status    string `json:"status"`
	IsCurrent bool   `json:"is_current"` // true for the single active (non-archived, non-superseded) head
}

// orderVersionChain takes a flat map of {id → {supersedesID, status, createdAt}} and
// the starting id, then walks the chain to produce an oldest-first ordered slice.
// This pure function is unit-tested without a DB in memory_version_test.go.
// Cycles are bounded by maxChainLen. Redacted entries (status="redacted") are excluded.
func orderVersionChain(nodes map[string]versionNode, startID string, maxChainLen int) []MemoryVersionRef {
	if len(nodes) == 0 {
		return nil
	}

	// Build a "newer" index: for each node, which node supersedes it?
	// i.e. newerOf[X] = Y means Y.supersedes_id == X  (Y is newer than X).
	newerOf := make(map[string]string, len(nodes))
	for id, n := range nodes {
		if n.SupersedesID != "" {
			newerOf[n.SupersedesID] = id
		}
	}

	// Walk backwards (via supersedes_id) to find the oldest ancestor.
	oldest := startID
	seen := map[string]bool{}
	for hop := 0; hop < maxChainLen; hop++ {
		n, ok := nodes[oldest]
		if !ok || n.SupersedesID == "" {
			break
		}
		if seen[n.SupersedesID] {
			break // cycle guard
		}
		seen[n.SupersedesID] = true
		if _, exists := nodes[n.SupersedesID]; !exists {
			break // older version was redacted/not loaded — stop
		}
		oldest = n.SupersedesID
	}

	// Walk forward from oldest via newerOf to build the chain.
	var chain []MemoryVersionRef
	cur := oldest
	seenForward := map[string]bool{}
	for hop := 0; hop < maxChainLen; hop++ {
		n, ok := nodes[cur]
		if !ok {
			break
		}
		if seenForward[cur] {
			break // cycle guard
		}
		seenForward[cur] = true
		chain = append(chain, MemoryVersionRef{
			ID:        cur,
			CreatedAt: n.CreatedAt,
			Status:    n.Status,
		})
		next, hasNewer := newerOf[cur]
		if !hasNewer {
			break
		}
		cur = next
	}

	// Mark IsCurrent: the single active head. If none is active, mark the last entry.
	activeFound := false
	for i := range chain {
		if chain[i].Status == "active" {
			chain[i].IsCurrent = true
			activeFound = true
		}
	}
	if !activeFound && len(chain) > 0 {
		chain[len(chain)-1].IsCurrent = true
	}

	return chain
}

// versionNode is the raw DB row used by MemoryVersionChain.
type versionNode struct {
	SupersedesID string // empty when this is the oldest version
	Status       string
	CreatedAt    string // RFC3339
}

// maxVersionChainLen caps the chain walk so corrupt/adversarial data cannot loop forever.
const maxVersionChainLen = 100

// MemoryVersionChain returns the full supersede lineage for the given memory id,
// ordered oldest → newest. Redacted entries are excluded. A single-version
// memory (no chain) returns a 1-element slice. An unknown id returns an empty
// slice (not an error, to keep callers simple).
//
// Implementation: two queries — one recursive CTE that walks the supersedes_id
// chain in both directions (ancestors via supersedes_id, descendants via reverse
// lookup), collecting all nodes into a flat map that orderVersionChain sorts.
func MemoryVersionChain(ctx context.Context, pool *pgxpool.Pool, memID string) ([]MemoryVersionRef, error) {
	// CTE strategy: start from memID, walk UP (older) via supersedes_id, and DOWN
	// (newer) via reverse. We use two CTEs chained together to avoid an infinite
	// recursion issue with bidirectional traversal in one CTE.
	//
	// Step 1: collect all ancestors (including self) via supersedes_id.
	// Step 2: for each ancestor, collect all descendants (including self) via reverse.
	// Combine both sets, deduplicate, exclude redacted.
	const q = `
WITH RECURSIVE
ancestors(id) AS (
    SELECT id FROM memories WHERE id = $1 AND status != 'redacted'
    UNION
    SELECT m.supersedes_id
    FROM memories m
    JOIN ancestors a ON m.id = a.id
    WHERE m.supersedes_id IS NOT NULL
      AND EXISTS (SELECT 1 FROM memories WHERE id = m.supersedes_id AND status != 'redacted')
),
descendants(id) AS (
    SELECT id FROM ancestors
    UNION
    SELECT m.id
    FROM memories m
    JOIN descendants d ON m.supersedes_id = d.id
    WHERE m.status != 'redacted'
)
SELECT m.id,
       COALESCE(m.supersedes_id, '') AS supersedes_id,
       m.status,
       m.created_at::text
FROM memories m
JOIN descendants d ON m.id = d.id
WHERE m.status != 'redacted'`

	rows, err := pool.Query(ctx, q, memID)
	if err != nil {
		return nil, fmt.Errorf("MemoryVersionChain: %w", err)
	}
	defer rows.Close()

	nodes := make(map[string]versionNode)
	for rows.Next() {
		var id, supersedesID, status, createdAt string
		if err := rows.Scan(&id, &supersedesID, &status, &createdAt); err != nil {
			return nil, fmt.Errorf("MemoryVersionChain scan: %w", err)
		}
		nodes[id] = versionNode{SupersedesID: supersedesID, Status: status, CreatedAt: createdAt}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("MemoryVersionChain rows: %w", err)
	}

	if len(nodes) == 0 {
		return nil, nil // memID not found or redacted
	}

	return orderVersionChain(nodes, memID, maxVersionChainLen), nil
}
