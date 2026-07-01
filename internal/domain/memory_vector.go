package domain

// RecallWithVector performs pgvector cosine-similarity recall for the memory system.
// It is called by Recall when an embedding provider is available and the caller
// supplies a query string without a work_item_id filter (see Recall for routing logic).

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GMISWE/ieops-aihub/internal/embedding"
)

// RecallWithVector embeds req.Query and returns the TopK memories ordered by a
// fusion score that blends cosine similarity (0.7 weight) with Ebbinghaus
// effective_strength (0.3 weight).
//
// Only memories that have emb_model matching the current provider and a non-NULL
// emb_vector are candidates — unembedded memories fall through to the text path.
//
// aihub#201: this is a 7th full-Memory read path alongside Remember's INSERT
// RETURNING, Recall's text-path SELECT, scanMemoryLite, GetMemoryByID, and the
// two callers of those — its SELECT column list and .Scan(...) carry latest_id
// at the same relative position (between commits and created_at) as those
// sites, keeping all lockstep Scan sites in sync.
//
// ponytail: fusion weights (0.7 similarity / 0.3 strength) are tunable; adjust
// when the recall quality tradeoff between freshness and semantic match shifts.
func RecallWithVector(ctx context.Context, pool *pgxpool.Pool, req *RecallRequest) (*RecallResponse, error) {
	qvec, err := embProvider.Embed(ctx, req.Query)
	if err != nil {
		return nil, fmt.Errorf("recallWithVector: embed query: %w", err)
	}
	if len(qvec) == 0 {
		return nil, fmt.Errorf("recallWithVector: empty embedding for query")
	}

	topK := req.TopK
	if topK <= 0 {
		topK = 20
	}
	if topK > 200 {
		topK = 200
	}
	minStrength := req.MinStrength
	if minStrength <= 0 {
		minStrength = 0.3
	}

	statusSet := "'active'"
	if req.IncludeArchived {
		statusSet = "'active','archived'"
	}

	args := []any{req.Project}
	idx := 2

	where := fmt.Sprintf(`
		project = $1
		AND status IN (%s)
		AND (expires_at IS NULL OR expires_at > clock_timestamp())
		AND emb_vector IS NOT NULL
		AND emb_model = $%d`, statusSet, idx)
	args = append(args, embProvider.ModelID())
	idx++

	// Visibility scoping — mirrors Recall's predicate exactly.
	if req.CallerRole != "admin" {
		where += fmt.Sprintf(` AND (visibility != 'private' OR author_user_id = $%d)`, idx)
		args = append(args, req.CallerUserID)
		idx++
		where += ` AND visibility != 'admin'`
	}

	// Type filter with prefix matching.
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

	// H9: min_strength SQL filter (Ebbinghaus) — same expression as Recall.
	where += fmt.Sprintf(` AND (is_immortal = true OR (stability_days > 0 AND
		base_strength * exp(
			-extract(epoch from (clock_timestamp() - COALESCE(last_activated_at, created_at)))/86400.0
			/ NULLIF(stability_days, 0)
		) >= $%d))`, idx)
	args = append(args, minStrength)
	idx++

	// Query vector placeholder.
	qvecPlaceholder := fmt.Sprintf("$%d::vector", idx)
	args = append(args, vecToPGLiteral(qvec))
	idx++

	// Optional cosine-similarity floor (req.SimilarityThreshold; off when <= 0).
	if req.SimilarityThreshold > 0 {
		where += fmt.Sprintf(" AND (1 - (emb_vector <=> %s)) >= $%d", qvecPlaceholder, idx)
		args = append(args, req.SimilarityThreshold)
		idx++
	}

	// Limit placeholder ($idx — topK is appended at this 1-based position).
	limitIdx := idx
	args = append(args, topK)

	// Fusion score: 0.7*cosine_similarity + 0.3*normalized_strength.
	// cosine_similarity = 1 - (emb_vector <=> query_vector)  (pgvector cosine distance)
	// normalized_strength = tanh(effective_strength) maps (0,∞)→(0,1).
	//
	// ponytail: cursor-based pagination is skipped on the vector path because ORDER BY
	// a fusion score is incompatible with the timestamp cursor used by the text path.
	// Vector recall always returns the top TopK by score; callers that need paging
	// should reduce TopK or fall back to the text path.
	query := fmt.Sprintf(`
		SELECT
			id, project, type, content, author_user_id, author_display,
			work_item_id, visibility, is_immortal, base_strength, stability_days,
			last_activated_at, last_activated_by, activation_count, expires_at,
			tags, source_artifact_id, status, attrs, commits, latest_id, created_at, updated_at,
			1 - (emb_vector <=> %s) AS similarity,
			base_strength * exp(
				-extract(epoch from (clock_timestamp() - COALESCE(last_activated_at, created_at)))/86400.0
				/ NULLIF(stability_days, 0)
			) AS eff_strength
		FROM memories
		WHERE %s
		ORDER BY
			0.7 * (1 - (emb_vector <=> %s)) +
			0.3 * tanh(base_strength * exp(
				-extract(epoch from (clock_timestamp() - COALESCE(last_activated_at, created_at)))/86400.0
				/ NULLIF(stability_days, 0)
			)) DESC
		LIMIT $%d`, qvecPlaceholder, where, qvecPlaceholder, limitIdx)

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("recallWithVector query: %w", err)
	}
	defer rows.Close()

	var items []MemoryWithStrength
	for rows.Next() {
		m := &Memory{}
		var similarity float64
		var effStrength float64
		if scanErr := rows.Scan(
			&m.ID, &m.Project, &m.Type, &m.Content, &m.AuthorUserID, &m.AuthorDisplay,
			&m.WorkItemID, &m.Visibility, &m.IsImmortal, &m.BaseStrength, &m.StabilityDays,
			&m.LastActivatedAt, &m.LastActivatedBy, &m.ActivationCount, &m.ExpiresAt,
			&m.Tags, &m.SourceArtifactID, &m.Status,
			&m.Attrs, &m.Commits, &m.LatestID, &m.CreatedAt, &m.UpdatedAt,
			&similarity, &effStrength,
		); scanErr != nil {
			fmt.Fprintf(os.Stderr, "recallWithVector: scan error: %v\n", scanErr)
			continue
		}
		items = append(items, MemoryWithStrength{Memory: *m, EffectiveStrength: effStrength, Similarity: &similarity})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recallWithVector rows: %w", err)
	}

	// Enrich with forward relations (same as Recall text path).
	if len(items) > 0 {
		ids := make([]string, len(items))
		for i := range items {
			ids[i] = items[i].ID
		}
		forwardMap, ferr := loadForwardRelations(ctx, pool, ids, req.Project, req.CallerUserID, req.CallerRole)
		if ferr != nil {
			fmt.Fprintf(os.Stderr, "recallWithVector: loadForwardRelations error: %v\n", ferr)
		} else {
			for i := range items {
				if refs, ok := forwardMap[items[i].ID]; ok {
					items[i].Related = refs
				}
			}
		}
	}

	return &RecallResponse{Items: items}, nil
}

// isNoopProvider reports whether p is a NoopProvider (used in Recall routing).
func isNoopProvider(p embedding.Provider) bool {
	_, ok := p.(*embedding.NoopProvider)
	return ok
}
