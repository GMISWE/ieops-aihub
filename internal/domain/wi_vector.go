package domain

// pgvector cosine search over work items — the wi counterpart of
// RecallWithVector (memory_vector.go). It serves TWO retrieval shapes, and
// almost nothing that is true of one is true of the other:
//
//	query=      (aihub#273) embeds the caller's text. Needs a live provider.
//	            Any error, or an empty result the caller did not ask to be
//	            empty, makes ListWorkItems fall through to the ILIKE text path,
//	            so a wi is never structurally unreachable just because it has
//	            no vector yet (the aihub#270 lesson, applied from day one).
//	similar_to= (aihub#277) reuses another row's STORED vector. Needs NO
//	            provider, NEVER falls through to ILIKE, and rows without a
//	            vector are genuinely unreachable through it — an unusable
//	            source is an explicit error instead. See the 🔴 note below.
//
// Do not generalise a statement about one of them to the other; the split is
// the single most load-bearing fact in this file.
//
// ─── aihub#276: why there is no relevance filter here, and never will be ────
//
// The reported defect is real: this query has no distance predicate, so a
// garbage query returns a page structurally identical to a real hit. The
// obvious fix — a default similarity floor — is REFUTED BY MEASUREMENT, twice
// over, and must not be reintroduced.
//
// Measured 2026-09-03 against production (Qwen3-Embedding-0.6B @1024, the
// aihub corpus of 351 work items, FULL distributions rather than the truncated
// page the API returns), over 20 queries: 10 deliberate garbage (punctuation,
// random letters, hex, and — the hard cases — fluent off-domain English and
// Chinese sentences) and 10 real queries with known answers in the corpus.
//
// FOURTEEN candidate statistics were computed, over that same query set, and
// EVERY ONE overlaps — most of them inverted (garbage scoring HIGHER than
// real). Nine derived from the similarity values:
//
//	top1 · top2 · p50 · min · (max-min) · (top1-top2) · (top1-p50)
//	z-score of top1 over the full corpus · min-max normalised top2
//
// and five from lexical grounding, on the theory that the signal might not be
// geometric at all:
//
//	raw query-term coverage of top1 · raw coverage, best of top 10
//	count of the top 10 containing any query term
//	IDF-weighted coverage of top1 · IDF-weighted coverage, best of top 10
//
// The two cleanest inversions, both 10-for-10 rather than illustrative:
//
//   - the hex string "f3a9c1d0b7e28456aa19fd3c" scores z=3.9071, higher than
//     ALL TEN real queries (whose highest is 3.6045);
//   - the real query "pf_recall similarity_threshold is declared but never
//     forwarded" has top1=0.2880, lower than ALL TEN garbage queries (whose
//     lowest is 0.3033).
//
// The absolute scale is set by the QUERY's own length and register, not by
// match quality. On this one corpus, with full distributions: that hex string
// yields a 0.0247–0.3033 band, while aihub#276's own goal+content as the query
// yields 0.1896–1.0000. Same 351 documents, same model, same day.
//
// The reason is not a missing statistic. "Is there an answer in this corpus?"
// is a relevance judgement about the caller's intent; the server holds a bag
// of cosines. So this file does NOT compute a verdict it cannot compute.
// Instead ListWorkItems reports, per call, the facts the caller needs in order
// to judge for itself — above all RankedCandidates, which is what stops a
// full page from reading as "N matches" when it is really "the top N of M
// candidates, ranked, with no relevance test applied". See SemanticInfo.
//
// MinSimilarity exists for a caller that has calibrated a floor for its OWN
// query shape, and it DEFAULTS TO ZERO (off). Do not give it a non-zero
// default: no globally valid value exists, and the measurement above is why.
//
// ─── aihub#277: similar_to uses the STORED vector, not a re-embedding ───────
//
// A one-line text query is an order of magnitude worse than the document it
// stands for, because emb_vector embeds goal+content (up to 6000 runes).
// Measured 2026-09-03 against production: ieops#316 queried with its own goal
// text verbatim retrieves ITSELF at rank 81/100 (sim 0.5507, whole page
// compressed into 0.5390–0.6475), while the same work item's stored vector
// puts it at rank 1 (sim 1.0000) with topically precise neighbours.
//
// So the similar_to path reads the source row's emb_vector straight out of the
// table as a pgvector literal and binds it as the query vector. Three
// consequences, all deliberate:
//
//  1. It is EXACT — no re-embedding, so the source scores exactly 1.0000
//     against itself, and that self-hit is retained in the results rather than
//     filtered out.
//
//     ⚠️ But it is NOT an unconditional guarantee, so do not document it as
//     one: the source is an ordinary row subject to the whole WHERE, so
//     status=, label=, ids=, ready_only=, since=, wi_type= or priority= can
//     all exclude it (the gate exercises exactly that, restricting with ids=
//     and then asserting the top similarity is below 1). The field that IS
//     unconditional, and therefore the one a caller should verify against, is
//     SemanticInfo.SourceWorkItemID.
//  2. It costs zero embedding calls and needs NO embedding provider at all —
//     similar_to keeps working when the provider is down or NoopProvider. The
//     model is therefore taken from the SOURCE ROW's emb_model, never from
//     embProvider.
//  3. It is immune to the write-path/backfill input drift described at
//     WorkItemEmbedInput (both sides of the cosine are stored vectors).
//
// 🔴 There is deliberately NO ILIKE fallback on the similar_to path. Falling
// through would text-search for the literal string "aihub#276", which is a
// different question with a plausible-looking answer. A source that cannot be
// used is an explicit error instead (ErrNotFound / ErrPreconditionFailed).

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Values for SemanticInfo.Mode — which retrieval shape served the page.
const (
	// SemanticModeTextQuery: the query text was embedded and used as the query
	// vector (query=).
	SemanticModeTextQuery = "text_query"
	// SemanticModeSimilarTo: another work item's stored vector was used as the
	// query vector (similar_to=).
	SemanticModeSimilarTo = "similar_to"
)

// SemanticSimilarityScaleQueryRelative is the machine-readable statement that
// a `similarity` value is comparable only WITHIN one result set, and has no
// absolute meaning and no cross-query meaning (aihub#276 acceptance criterion
// 2). It is a short enum rather than a sentence because the prose belongs in
// the tool description and the API docs, which are paid for once per session
// rather than once per response.
const SemanticSimilarityScaleQueryRelative = "query_relative"

// SemanticInfo describes the semantic retrieval that produced a page, and is
// present ONLY when a page came from the vector path — its absence is how a
// caller tells that the ILIKE text fallback served the request instead.
//
// Every field is a fact about this one call. None of them is a relevance
// verdict, because no computable relevance verdict exists (see the file
// header). What the caller gets instead is the denominator.
type SemanticInfo struct {
	// Mode is SemanticModeTextQuery or SemanticModeSimilarTo.
	Mode string `json:"mode"`
	// SourceWorkItemID is the resolved id of the similar_to source, so a
	// caller that passed a slug can confirm which row was used. Absent on the
	// text_query path.
	SourceWorkItemID *string `json:"source_work_item_id,omitempty"`
	// EmbModel is the embedding model both sides of the cosine were produced
	// with. A similarity is meaningless across models, and the schema carries
	// an emb_model column precisely because the model is expected to change.
	EmbModel string `json:"emb_model"`
	// RankedCandidates is how many work items satisfied the whole WHERE and
	// were therefore ranked — the denominator of this page.
	//
	// 🔴 This is the field that fixes aihub#276's actual defect. The bug was
	// never "no threshold"; it was that an unfiltered ranking of the entire
	// corpus is shaped exactly like a filtered result set, so `items` being
	// full reads as "N matches" when a garbage query gets a full page too.
	// RankedCandidates says out loud that you received the top len(Items) of
	// RankedCandidates, and when the two are equal you were handed every
	// candidate in scope rather than a match set.
	RankedCandidates int `json:"ranked_candidates"`
	// MinSimilarity is the floor actually applied. 0 means NO floor was
	// applied and RankedCandidates is the whole embedded corpus in scope.
	MinSimilarity float64 `json:"min_similarity"`
	// SimilarityScale is always SemanticSimilarityScaleQueryRelative.
	SimilarityScale string `json:"similarity_scale"`
}

// semanticQuerySource resolves where the query vector comes from, returning it
// as a pgvector literal together with the emb_model that both sides of the
// cosine must share.
//
// The similar_to branch scopes its lookup to the SAME projects the results are
// scoped to, so no separate access check is needed and none can be forgotten:
// a source outside the caller's scope is reported as not found, exactly the
// asymmetry buildListWorkItemsWhere already documents for ids=. Cross-project
// neighbour search is still reachable — omit project= and the scope becomes
// every project the caller can see, source included.
func semanticQuerySource(ctx context.Context, pool *pgxpool.Pool, project string, f ListWorkItemsFilter) (
	vecLit, model, mode string, sourceID *string, aerr *AihubError) {
	// 🔴 The SAME predicate listWorkItemsPage uses to choose this branch,
	// including the TrimSpace. Two spellings of one decision drift: with
	// `f.SimilarTo != nil` here, a filter carrying SimilarTo="   " AND a Query
	// would take the query= branch upstream and the similar_to branch HERE,
	// looking up a work item whose id is "   " and answering a text query with
	// a 404. handleListWorkItems cannot construct that today, but
	// listWorkItemsFn is a package seam with other call sites in
	// ui_handlers_*.go, and this file's own header is about exactly this class.
	if f.SimilarTo == nil || strings.TrimSpace(*f.SimilarTo) == "" {
		qvec, err := embProvider.Embed(ctx, *f.Query)
		if err != nil {
			return "", "", "", nil, NewErr(ErrInternalError, fmt.Sprintf("embed query: %v", err))
		}
		if len(qvec) == 0 {
			return "", "", "", nil, NewErr(ErrInternalError, "empty embedding for query")
		}
		return vecToPGLiteral(qvec), embProvider.ModelID(), SemanticModeTextQuery, nil, nil
	}

	// emb_vector::text is already the pgvector literal format, so the stored
	// vector round-trips without ever being parsed into floats — nothing can
	// round it on the way through.
	args := []any{*f.SimilarTo}
	scope := ""
	switch {
	case project != "":
		args = append(args, project)
		scope = " AND project = $2"
	case len(f.AccessibleProjects) > 0:
		args = append(args, f.AccessibleProjects)
		scope = " AND project = ANY($2)"
	}
	var id string
	var storedVec, storedModel *string
	err := pool.QueryRow(ctx, `
		SELECT id, emb_vector::text, emb_model
		FROM work_items
		WHERE (id = $1 OR slug = $1)`+scope, args...,
	).Scan(&id, &storedVec, &storedModel)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", nil, NewErr(ErrNotFound, fmt.Sprintf(
			"similar_to: no work item %q in scope (it may exist in a project this request does not cover)", *f.SimilarTo))
	}
	if err != nil {
		return "", "", "", nil, NewErr(ErrInternalError, fmt.Sprintf("similar_to source lookup: %v", err))
	}
	// A source with no vector is a real and temporary server state (embeddings
	// disabled, or a backfill not yet run) — NOT an empty neighbour set, which
	// is what returning items:[] would have said.
	if storedVec == nil || storedModel == nil || *storedModel == "" {
		return "", "", "", nil, NewErr(ErrPreconditionFailed, fmt.Sprintf(
			"similar_to: work item %q has no embedding, so it has no neighbours to compute; "+
				"run the embedding backfill or use query= with its text instead", *f.SimilarTo))
	}
	return *storedVec, *storedModel, SemanticModeSimilarTo, &id, nil
}

func listWorkItemsByVector(ctx context.Context, pool *pgxpool.Pool, project string, f ListWorkItemsFilter) (*ListWorkItemsResult, *AihubError) {
	vecLit, model, mode, sourceID, aerr := semanticQuerySource(ctx, pool, project, f)
	if aerr != nil {
		return nil, aerr
	}

	// Reuse the shared WHERE builder for every non-query filter, with Query
	// cleared: the ILIKE guard belongs to the text path only — semantic search
	// must not additionally require a substring match.
	tf := f
	tf.Query = nil
	joinClause, where, args := buildListWorkItemsWhere(project, tf)

	modelIdx := len(args) + 1
	args = append(args, model)
	vecIdx := len(args) + 1
	args = append(args, vecLit)

	// The emb_model equality is what keeps a similarity meaningful: vectors
	// from two different models share a coordinate space only by accident.
	// On the similar_to path `model` comes from the SOURCE ROW, which is why
	// that path needs no embedding provider.
	//
	// 🔴 emb_model equality is NOT sufficient, and the dimension guard next to
	// it is not belt-and-braces — it is what stops this path from 500ing.
	// EMBEDDING_MODEL and EMBEDDING_DIMS are INDEPENDENT env vars
	// (embedding/factory.go) and ModelID() returns only the model name, so a
	// dimension change alone — e.g. Qwen3-Embedding truncated 1024→768, which
	// that family supports — leaves rows carrying the SAME emb_model at a
	// different width. pgvector then raises per row ("different vector
	// dimensions 1024 and 768"); cmd/aihub-embed-verify already reports that
	// exact failure for the memory side.
	//
	// The consequence is asymmetric, which is why it matters here specifically.
	// On the query= path such an error is MASKED: listWorkItemsPage's
	// `case vecErr != nil` degrades to ILIKE and the caller still gets a 200.
	// The similar_to path deliberately has no fallback, so without this guard
	// the same condition is a hard 500 for EVERY similar_to call until a full
	// backfill lands — i.e. precisely during the rollout window in which a
	// caller reaches for it.
	//
	// vector_dims() on both sides rather than the emb_dims column: it compares
	// the vectors actually being cosined, so it holds even if emb_dims is
	// wrong or NULL, and it needs no extra bound parameter. Rows at the wrong
	// width drop out of the candidate set exactly like rows with no vector.
	embCond := fmt.Sprintf(
		"wi.emb_vector IS NOT NULL AND wi.emb_model = $%d"+
			" AND vector_dims(wi.emb_vector) = vector_dims($%d::vector)", modelIdx, vecIdx)
	if where == "" {
		where = "WHERE " + embCond
	} else {
		where += " AND " + embCond
	}
	// Opt-in cosine floor; off at 0, which is the default and must stay so
	// (aihub#276 — see the file header for the measurement that forbids a
	// non-zero default). Mirrors memory_vector.go's SimilarityThreshold clause.
	//
	// ⚠️ This block MUST stay below the embCond block above, which is the thing
	// that guarantees `where != ""` (it is the one that handles the empty case
	// explicitly). Moved above it, this `+=` emits a leading " AND ..." with no
	// WHERE keyword — a syntax error reachable only on the admin/no-project/
	// no-filter path, which is the path least likely to be exercised first.
	if f.MinSimilarity > 0 {
		minIdx := len(args) + 1
		args = append(args, f.MinSimilarity)
		where += fmt.Sprintf(" AND (1 - (wi.emb_vector <=> $%d::vector)) >= $%d", vecIdx, minIdx)
	}

	// Same 26-column SELECT as buildListWorkItemsQuery (lockstep Scan sites),
	// plus the similarity expression and the pre-LIMIT candidate count.
	//
	// count(*) OVER () is evaluated before LIMIT, so it yields the number of
	// rows that satisfied the WHERE rather than the number returned — the
	// denominator SemanticInfo.RankedCandidates publishes.
	//
	// Its cost is bounded but NOT zero, and the tempting argument for zero is
	// wrong: "there is no vector index (migration 0030 says so on purpose), so
	// this query already scans the candidate set" is about the SCAN, which was
	// never the added cost. Measured on pgvector locally, the window function
	// adds a WindowAgg above the Sort, and with no PARTITION/ORDER it must
	// buffer the whole candidate set before emitting the first row. The top-N
	// heapsort does survive (same Sort Method in both arms), so the extra cost
	// is memory proportional to the candidate count, not another sort. At this
	// table's size that is immaterial; at a size where it is not, the answer is
	// a second cheap COUNT query, not dropping the denominator.
	query := fmt.Sprintf(`
		SELECT wi.id, wi.seq, wi.slug, wi.project, wi.scenario, wi.goal, wi.source,
			   wi.wi_type, wi.priority, wi.requires_human_session, wi.milestone, wi.labels,
			   wi.status, wi.declared_resources, wi.resources_version,
			   wi.external_share_type, wi.external_share_key,
			   wi.reporter_user_id, wi.reporter_display,
			   wi.current_attempt_id, wi.current_attempt_epoch,
			   wi.parent_work_item_id, wi.attrs, wi.created_at, wi.updated_at, wi.closed_at,
			   1 - (wi.emb_vector <=> $%d::vector) AS similarity,
			   count(*) OVER () AS ranked_candidates
		FROM work_items wi%s
		%s
		ORDER BY wi.emb_vector <=> $%d::vector
		LIMIT %d`, vecIdx, joinClause, where, vecIdx, f.Limit)

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, NewErr(ErrInternalError, fmt.Sprintf("vector list query: %v", err))
	}
	defer rows.Close()

	var items []*WorkItem
	rankedCandidates := 0
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
			&sim, &rankedCandidates,
		); scanErr != nil {
			return nil, NewErr(ErrInternalError, fmt.Sprintf("vector list scan: %v", scanErr))
		}
		wi.Labels = labelsRaw
		if wi.Labels == nil {
			wi.Labels = []string{}
		}
		wi.Similarity = &sim
		items = append(items, &wi)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, NewErr(ErrInternalError, fmt.Sprintf("vector list rows: %v", rowsErr))
	}
	if items == nil {
		items = []*WorkItem{}
	}
	// No cursor in semantic mode: results are similarity-ordered, which has no
	// stable pagination key. The handler rejects cursor/sort/order + query
	// combinations up front rather than silently ignoring them (aihub#267/#271
	// lesson: no silently dropped parameters).
	return &ListWorkItemsResult{
		Items: items,
		Semantic: &SemanticInfo{
			Mode:             mode,
			SourceWorkItemID: sourceID,
			EmbModel:         model,
			RankedCandidates: rankedCandidates,
			MinSimilarity:    f.MinSimilarity,
			SimilarityScale:  SemanticSimilarityScaleQueryRelative,
		},
	}, nil
}
