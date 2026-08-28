package domain

// aihub#273: pgvector cosine search over work items — the wi counterpart of
// RecallWithVector (memory_vector.go). Called by ListWorkItems when the caller
// supplies query= and an embedding provider is active; any error or an empty
// result makes the caller fall through to the ILIKE text path, so a wi is
// never structurally unreachable just because it has no vector yet (the
// aihub#270 lesson, applied here from day one).

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

func listWorkItemsByVector(ctx context.Context, pool *pgxpool.Pool, project string, f ListWorkItemsFilter) (*ListWorkItemsResult, error) {
	qvec, err := embProvider.Embed(ctx, *f.Query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(qvec) == 0 {
		return nil, fmt.Errorf("empty embedding for query")
	}

	// Reuse the shared WHERE builder for every non-query filter, with Query
	// cleared: the ILIKE guard belongs to the text path only — semantic search
	// must not additionally require a substring match.
	tf := f
	tf.Query = nil
	joinClause, where, args := buildListWorkItemsWhere(project, tf)

	modelIdx := len(args) + 1
	args = append(args, embProvider.ModelID())
	vecIdx := len(args) + 1
	args = append(args, vecToPGLiteral(qvec))

	embCond := fmt.Sprintf("wi.emb_vector IS NOT NULL AND wi.emb_model = $%d", modelIdx)
	if where == "" {
		where = "WHERE " + embCond
	} else {
		where += " AND " + embCond
	}

	// Same 26-column SELECT as buildListWorkItemsQuery (lockstep Scan sites),
	// plus the similarity expression.
	query := fmt.Sprintf(`
		SELECT wi.id, wi.seq, wi.slug, wi.project, wi.scenario, wi.goal, wi.source,
			   wi.wi_type, wi.priority, wi.requires_human_session, wi.milestone, wi.labels,
			   wi.status, wi.declared_resources, wi.resources_version,
			   wi.external_share_type, wi.external_share_key,
			   wi.reporter_user_id, wi.reporter_display,
			   wi.current_attempt_id, wi.current_attempt_epoch,
			   wi.parent_work_item_id, wi.attrs, wi.created_at, wi.updated_at, wi.closed_at,
			   1 - (wi.emb_vector <=> $%d::vector) AS similarity
		FROM work_items wi%s
		%s
		ORDER BY wi.emb_vector <=> $%d::vector
		LIMIT %d`, vecIdx, joinClause, where, vecIdx, f.Limit)

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("vector list query: %w", err)
	}
	defer rows.Close()

	var items []*WorkItem
	for rows.Next() {
		var wi WorkItem
		var labelsRaw []string
		var sim float64
		if scanErr := rows.Scan(
			&wi.ID, &wi.Seq, &wi.Slug, &wi.Project, &wi.Scenario, &wi.Goal, &wi.Source,
			&wi.WIType, &wi.Priority, &wi.RequiresHumanSession, &wi.Milestone, &labelsRaw,
			&wi.Status, &wi.DeclaredResources, &wi.ResourcesVersion,
			&wi.ExternalShareType, &wi.ExternalShareKey,
			&wi.ReporterUserID, &wi.ReporterDisplay,
			&wi.CurrentAttemptID, &wi.CurrentAttemptEpoch,
			&wi.ParentWorkItemID, &wi.Attrs, &wi.CreatedAt, &wi.UpdatedAt, &wi.ClosedAt,
			&sim,
		); scanErr != nil {
			return nil, fmt.Errorf("vector list scan: %w", scanErr)
		}
		wi.Labels = labelsRaw
		if wi.Labels == nil {
			wi.Labels = []string{}
		}
		wi.Similarity = &sim
		items = append(items, &wi)
	}
	if items == nil {
		items = []*WorkItem{}
	}
	// No cursor in semantic mode: results are similarity-ordered, which has no
	// stable pagination key. The handler rejects cursor/sort/order + query
	// combinations up front rather than silently ignoring them (aihub#267/#271
	// lesson: no silently dropped parameters).
	return &ListWorkItemsResult{Items: items}, nil
}
