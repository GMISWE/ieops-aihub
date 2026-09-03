package server

// The gate for aihub#277 (document→document recall via similar_to=) and
// aihub#276 (the free-text path's unfilterable false positives).
//
// ─── Why nothing here asserts a similarity NUMBER ───────────────────────────
//
// aihub#276's whole content is a measurement showing that no absolute
// similarity value means anything: over 20 queries against the production
// corpus, fourteen candidate statistics all overlapped between deliberate
// garbage and real queries, most of them inverted. A gate calibrated on an
// observed cosine would therefore be pinning a number that carries no
// information, and it would drift the moment emb_model changes — which the
// schema's emb_model column exists to allow. aihub#276 acceptance criterion 4
// forbids it outright.
//
// So every assertion below is STRUCTURAL or RELATIVE:
//
//	 A  a work item retrieves ITSELF at exactly similarity 1.0 through
//	    similar_to= — an identity (a vector cosined with itself), not a
//	    calibration, and the caller's proof that the intended source vector was
//	    the one used.
//	 B  min_similarity is REACHABLE, asserted against a floor derived from the
//	    similarities observed IN THE SAME RUN (top1+ε excludes everything,
//	    bottom-ε excludes nothing). No literal appears. This guards the defect
//	    class that made pf_recall's similarity_threshold a no-op for months:
//	    published, accepted, never forwarded.
//	 C  the caller-facing `semantic` block CHANGES between the two retrieval
//	    paths — present with mode/ranked_candidates on the vector path, ABSENT
//	    on the ILIKE text path — and ranked_candidates reports the pre-LIMIT
//	    denominator, which is what stops a full page from reading as "N
//	    matches".
//	 D  an empty page from an enforced floor is returned AS an answer, rather
//	    than being read as "nothing matched" and topped up from ILIKE with the
//	    very rows the floor excluded.
//	 E  a provider OUTAGE with a floor set is surfaced as a server error, not
//	    as a 400 telling the caller to do what they already did.
//	 F  a row carrying the same emb_model at a different vector WIDTH drops out
//	    of the candidate set instead of making pgvector raise — which on the
//	    similar_to path, having no ILIKE fallback, would be a hard 500.
//
// The vectors are written directly into emb_vector as literals (migration 0030
// declares the column as bare VECTOR, with no dimension), so the expected
// cosines are exact arithmetic rather than model output: identical vectors give
// 1, orthogonal vectors give 0. Most rows are three-dimensional; the one
// four-dimensional row exists solely for gate F.
//
// Gates A–C and F run with NO embedding provider at all (the package default is
// NoopProvider), which is itself part of the aihub#277 contract: similar_to
// reads a stored vector, so it must keep working when the provider is absent.
// Gates D and E are about the query= path, which cannot exist without a
// provider, so each installs a deterministic one for its own duration
// (planeEmbedProvider / brokenEmbedProvider) and restores NoopProvider after.
//
// DB-gated like every other handler suite here:
//
//	AIHUB_TEST_DB=postgres://postgres:test@localhost:15476/aihub_test?sslmode=disable \
//	go test ./internal/server/ -run TestSimilarTo -v -count=1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/GMISWE/ieops-aihub/internal/embedding"

	"github.com/GMISWE/ieops-aihub/internal/domain"
)

// seedVecWorkItem inserts a work item with an explicit emb_vector/emb_model,
// bypassing the embedding provider entirely. Returns the row's id.
//
// emb_dims is COUNTED from the literal rather than passed as a constant: with
// it hardcoded, the deliberately-mismatched row below would carry an emb_dims
// that disagreed with its own vector, and then this fixture would be asserting
// against data no writer in the codebase can produce. (Note that the WHERE
// compares vector_dims() rather than this column, so it would catch a row whose
// emb_dims lied as well — but the fixture should not be the thing that lies.)
func seedVecWorkItem(t *testing.T, pool *pgxpool.Pool, project, uid, goal string, seq int, vec, model string) string {
	t.Helper()
	id := domain.NewID("wi")
	_, err := pool.Exec(context.Background(), `
		INSERT INTO work_items (
			id, seq, project, scenario, goal, source, wi_type, priority,
			requires_human_session, milestone, labels, status,
			declared_resources, reporter_user_id, reporter_display,
			parent_work_item_id, attrs, content, emb_vector, emb_model, emb_dims
		) VALUES (
			$1, $2, $3, 'coding', $4, 'human', 'fix_bug', 'normal',
			FALSE, NULL, '{}', 'queued',
			'[]', $5, $5,
			NULL, '{}', NULL, $6::vector, $7, $8
		)`, id, seq, project, goal, uid, vec, model, strings.Count(vec, ",")+1)
	require.NoError(t, err)
	return id
}

// similarToFixture seeds four rows sharing one emb_model. The vectors are
// chosen so the ordering is decided by arithmetic and not by a model:
//
//	src  (1,0,0)                cos(src,src)  = 1
//	near (0.8,0.6,0)            cos(src,near) = 0.8
//	mid  (0.6,0.8,0)            cos(src,mid)  = 0.6
//	far  (0,1,0)                cos(src,far)  = 0   (orthogonal)
type similarToFixture struct {
	pool                    *pgxpool.Pool
	uc                      *UserContext
	project                 string
	uid                     string
	srcID                   string
	model                   string
	srcSeq                  int
	nearGoal, midGoal       string
	farGoal, unembeddedGoal string
	wrongWidthGoal          string
}

func newSimilarToFixture(t *testing.T) *similarToFixture {
	t.Helper()
	pool := setupStepTestDB(t)
	uid, project := seedStepTestUserAndProject(t, pool)
	_, err := pool.Exec(context.Background(), `DELETE FROM work_items WHERE project=$1`, project)
	require.NoError(t, err)

	const model = "simtest-model-v1"
	f := &similarToFixture{
		pool: pool, project: project, uid: uid, model: model, srcSeq: 8100,
		nearGoal: "near neighbour row", midGoal: "middle neighbour row",
		farGoal: "orthogonal row", unembeddedGoal: "row with no vector at all",
	}
	f.srcID = seedVecWorkItem(t, pool, project, uid, "the source row", 8100, "[1,0,0]", model)
	seedVecWorkItem(t, pool, project, uid, f.nearGoal, 8101, "[0.8,0.6,0]", model)
	seedVecWorkItem(t, pool, project, uid, f.midGoal, 8102, "[0.6,0.8,0]", model)
	seedVecWorkItem(t, pool, project, uid, f.farGoal, 8103, "[0,1,0]", model)
	// Deliberately left with NULL emb_* columns.
	seedQueryTestWorkItem(t, pool, project, uid, f.unembeddedGoal, 8104)
	// 🔴 Same emb_model, DIFFERENT width. This row is why the WHERE compares
	// vector_dims() and not just emb_model: EMBEDDING_MODEL and EMBEDDING_DIMS
	// are independent env vars, so a dimension change alone leaves rows tagged
	// with the same model at a different width, and pgvector raises
	// "different vector dimensions" per row when asked to cosine them.
	// Without the guard this single row turns EVERY similar_to call in this
	// project into a 500 — and unlike the query= path there is no ILIKE
	// fallback to mask it. It must drop out of the candidate set silently,
	// exactly like a row with no vector at all.
	f.wrongWidthGoal = "row tagged with the same model at a different width"
	seedVecWorkItem(t, pool, project, uid, f.wrongWidthGoal, 8105, "[1,0,0,0]", model)

	f.uc = &UserContext{
		UserID: uid, DisplayName: uid, Role: "writer",
		ProjectRoles: map[string]string{project: "viewer"},
	}
	return f
}

type semanticBlock struct {
	Mode             string  `json:"mode"`
	SourceWorkItemID *string `json:"source_work_item_id"`
	EmbModel         string  `json:"emb_model"`
	RankedCandidates int     `json:"ranked_candidates"`
	MinSimilarity    float64 `json:"min_similarity"`
	SimilarityScale  string  `json:"similarity_scale"`
}

type listResp struct {
	Items    []map[string]any `json:"items"`
	Semantic *semanticBlock   `json:"semantic"`
}

func (f *similarToFixture) get(t *testing.T, rawQuery string) (*listResp, *httptest2) {
	t.Helper()
	c, rec := newListWIRequest(t, "project="+f.project+"&"+rawQuery, f.uc)
	require.NoError(t, handleListWorkItems(f.pool)(c))
	var resp listResp
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
	}
	return &resp, &httptest2{code: rec.Code, body: rec.Body.String()}
}

type httptest2 struct {
	code int
	body string
}

func simOf(t *testing.T, item map[string]any) float64 {
	t.Helper()
	v, ok := item["similarity"]
	require.True(t, ok, "item %v carries no similarity on the semantic path", item["goal"])
	f, ok := v.(float64)
	require.True(t, ok, "similarity %#v is not a number", v)
	return f
}

// ─── Gate A: a work item retrieves ITSELF, exactly, through similar_to ──────
//
// This is the assertion that the source's STORED vector — not a re-embedding
// of its text, and not some other row's vector — was used as the query vector.
// It is an identity, so it needs no calibration and survives a model change.
//
// It is also the measured heart of aihub#277. Against production, ieops#316
// searched with its own goal text verbatim ranked ITSELF 81st of 100
// (sim 0.5507); through its stored vector it ranks 1st at 1.0000. If this
// assertion regresses to "rank 1 but 0.97" then the source text is being
// re-embedded somewhere instead of the vector being read, which is exactly the
// order-of-magnitude precision loss this work item exists to remove.
func TestSimilarTo_RetrievesItselfFirstAtExactlyOne(t *testing.T) {
	f := newSimilarToFixture(t)

	for _, spelling := range []string{f.srcID, fmt.Sprintf("%s#%d", f.project, f.srcSeq)} {
		resp, res := f.get(t, "similar_to="+spelling)
		require.Equal(t, http.StatusOK, res.code, res.body)
		require.NotEmpty(t, resp.Items)

		require.Equal(t, f.srcID, resp.Items[0]["id"],
			"similar_to=%s must rank the source row FIRST; got %v", spelling, resp.Items[0]["goal"])
		require.Equal(t, 1.0, simOf(t, resp.Items[0]),
			"the source must score EXACTLY 1.0 against its own stored vector — anything less means "+
				"the text was re-embedded instead of the vector being read")

		// Ordering is decided by arithmetic: 1 > 0.8 > 0.6 > 0.
		require.Len(t, resp.Items, 4, "only the four rows sharing emb_model are candidates")
		require.Equal(t, f.nearGoal, resp.Items[1]["goal"])
		require.Equal(t, f.midGoal, resp.Items[2]["goal"])
		require.Equal(t, f.farGoal, resp.Items[3]["goal"])
		require.InDelta(t, 0.8, simOf(t, resp.Items[1]), 1e-6)
		require.InDelta(t, 0.6, simOf(t, resp.Items[2]), 1e-6)
		require.InDelta(t, 0.0, simOf(t, resp.Items[3]), 1e-6)

		// The row with NULL emb_vector is excluded rather than reported at 0 —
		// and, crucially, similar_to must NOT fall through to the ILIKE text
		// path, which would have text-searched for the literal slug string.
		for _, it := range resp.Items {
			require.NotEqual(t, f.unembeddedGoal, it["goal"])
			require.NotEqual(t, f.wrongWidthGoal, it["goal"])
		}

		require.NotNil(t, resp.Semantic, "the vector path must publish a semantic block")
		require.Equal(t, domain.SemanticModeSimilarTo, resp.Semantic.Mode)
		require.NotNil(t, resp.Semantic.SourceWorkItemID)
		require.Equal(t, f.srcID, *resp.Semantic.SourceWorkItemID,
			"a caller that passed a slug must be told which row was actually used")
		require.Equal(t, f.model, resp.Semantic.EmbModel,
			"the model must come from the SOURCE ROW, not from embProvider — that is what lets "+
				"similar_to work with no provider configured (the test default is NoopProvider)")
	}
}

// ─── Gate B: min_similarity is reachable, with a floor derived in-run ───────
//
// Every floor below is computed from similarities observed in this same
// response, so no literal cosine is pinned. What is asserted is that the
// parameter DOES something — the failure this guards is a published parameter
// that is accepted and silently dropped, which is indistinguishable from a
// server that has no floor at all. That is not hypothetical: aihub#276's own
// investigation found pf_recall's similarity_threshold had been exactly that
// for months (declared in the tool schema, never forwarded, byte-identical
// responses with and without it).
func TestSimilarTo_MinSimilarityIsReachableAndDefaultsOff(t *testing.T) {
	f := newSimilarToFixture(t)

	base, res := f.get(t, "similar_to="+f.srcID)
	require.Equal(t, http.StatusOK, res.code, res.body)
	require.Len(t, base.Items, 4)
	require.Zero(t, base.Semantic.MinSimilarity,
		"🔴 the DEFAULT must be 0 (no floor). aihub#276 acceptance criterion 1: no non-zero "+
			"default threshold may be introduced, because no globally valid value exists")

	top := simOf(t, base.Items[0])
	second := simOf(t, base.Items[1])
	last := simOf(t, base.Items[len(base.Items)-1])

	// A floor strictly between the 2nd and 1st similarity keeps exactly the
	// top row. Relative to this run's own values.
	mid := (top + second) / 2
	only, res := f.get(t, fmt.Sprintf("similar_to=%s&min_similarity=%v", f.srcID, mid))
	require.Equal(t, http.StatusOK, res.code, res.body)
	require.Len(t, only.Items, 1,
		"a floor between the 1st (%v) and 2nd (%v) similarity must keep exactly one row", top, second)
	require.Equal(t, f.srcID, only.Items[0]["id"])
	require.Equal(t, mid, only.Semantic.MinSimilarity, "the applied floor must be echoed back")

	// A floor between the LAST two similarities keeps everything except the
	// last row.
	//
	// 🔴 This deliberately does NOT use `last` itself as the floor. The last
	// row is the orthogonal one, so its similarity is exactly 0 — and
	// min_similarity=0 means "off", so that request would send no floor at all
	// and this arm would pass with the whole clause deleted. It has to be a
	// value strictly inside (0, 1) to assert anything.
	require.Zero(t, last, "fixture invariant: the last row is orthogonal to the source")
	third := simOf(t, base.Items[len(base.Items)-2])
	require.Greater(t, third, 0.0, "need a non-zero third similarity to build a real floor from")
	lowFloor := (third + last) / 2
	require.Greater(t, lowFloor, 0.0)
	most, res := f.get(t, fmt.Sprintf("similar_to=%s&min_similarity=%v", f.srcID, lowFloor))
	require.Equal(t, http.StatusOK, res.code, res.body)
	require.Len(t, most.Items, len(base.Items)-1,
		"a floor of %v between the last two similarities (%v, %v) must drop exactly the last row",
		lowFloor, third, last)
	require.Equal(t, len(base.Items)-1, most.Semantic.RankedCandidates)

	// A floor above the top similarity empties the page — and ranked_candidates
	// follows it down, so the caller can tell "filtered to nothing" from
	// "nothing was ever ranked".
	//
	// The source itself scores exactly 1.0, and 1.0 is the top of the
	// parameter's closed domain, so there is no legal floor above the unfiltered
	// top row — by construction, not by accident. Restricting the candidate set
	// with ids= (which also checks that a non-semantic filter composes with
	// similar_to) drops the ceiling to the near row's 0.8 and makes room for
	// one. The floor is still derived from this run's own values.
	ids := fmt.Sprintf("%s#8101,%s#8102,%s#8103", f.project, f.project, f.project)
	restricted, res := f.get(t, fmt.Sprintf("similar_to=%s&ids=%s", f.srcID, ids))
	require.Equal(t, http.StatusOK, res.code, res.body)
	require.Len(t, restricted.Items, 3, "ids= must narrow the candidate set on the similar_to path too")
	require.Equal(t, 3, restricted.Semantic.RankedCandidates)
	restrictedTop := simOf(t, restricted.Items[0])
	require.Less(t, restrictedTop, 1.0, "the source is excluded, so nothing scores 1.0 any more")

	above := (restrictedTop + 1.0) / 2
	none, res := f.get(t, fmt.Sprintf("similar_to=%s&ids=%s&min_similarity=%v", f.srcID, ids, above))
	require.Equal(t, http.StatusOK, res.code, res.body)
	require.Empty(t, none.Items,
		"a floor of %v above a top similarity of %v must empty the page", above, restrictedTop)
	require.Zero(t, none.Semantic.RankedCandidates,
		"ranked_candidates must follow the floor down, so 'filtered to nothing' is "+
			"distinguishable from 'nothing was ever ranked'")

	// The boundary itself is inclusive: >= , not >. A floor of exactly 1.0
	// keeps the source, which is the only value a caller can use to mean
	// "identical document only".
	exact, res := f.get(t, fmt.Sprintf("similar_to=%s&min_similarity=1", f.srcID))
	require.Equal(t, http.StatusOK, res.code, res.body)
	require.Len(t, exact.Items, 1)
	require.Equal(t, f.srcID, exact.Items[0]["id"])
}

// ─── Gate C: the semantic block changes between the two retrieval paths ─────
//
// This is aihub#276's actual defect, stated as an assertion. The bug was never
// "no threshold" — it was that an unfiltered ranking of the whole corpus is
// shaped exactly like a filtered result set, so a full page reads as "N
// matches" when a garbage query gets a full page too. The fix is a
// denominator, plus making the two paths tell themselves apart.
func TestSimilarTo_SemanticBlockDistinguishesThePaths(t *testing.T) {
	f := newSimilarToFixture(t)

	// ranked_candidates is the PRE-LIMIT count, so it exceeds len(items) as
	// soon as the page is capped. That relationship is the whole point: the
	// caller learns it received the top len(items) of ranked_candidates.
	capped, res := f.get(t, "similar_to="+f.srcID+"&limit=2")
	require.Equal(t, http.StatusOK, res.code, res.body)
	require.Len(t, capped.Items, 2)
	require.Equal(t, 4, capped.Semantic.RankedCandidates,
		"ranked_candidates must count every row the WHERE ranked, not the rows returned — "+
			"otherwise a full page still says nothing about how selective the query was")
	require.Equal(t, domain.SemanticSimilarityScaleQueryRelative, capped.Semantic.SimilarityScale)

	// And it tracks the candidate set rather than being a constant: narrowing
	// by a non-semantic filter must move it.
	narrowed, res := f.get(t, "similar_to="+f.srcID+"&limit=2&status=running")
	require.Equal(t, http.StatusOK, res.code, res.body)
	require.Zero(t, narrowed.Semantic.RankedCandidates,
		"no seeded row is running, so nothing should have been ranked")

	// 🔴 The text path must NOT publish a semantic block. Its absence is how a
	// caller tells that similarity is unavailable for this page — the same
	// absence-means-none contract `similarity` and `step_state` already use.
	// With the test default NoopProvider, query= takes the ILIKE path.
	text, res := f.get(t, "query=neighbour")
	require.Equal(t, http.StatusOK, res.code, res.body)
	require.NotEmpty(t, text.Items, "the ILIKE fallback must still find rows (aihub#270)")
	// On the RAW body, not the decoded struct: a *SemanticInfo unmarshals both
	// an absent key and an explicit null to nil, and absence is the
	// load-bearing signal this endpoint publishes (aihub#278's "an absent key
	// means null" rule). Asserting on the pointer alone cannot tell the
	// omitempty tag working from it having been dropped.
	require.NotContains(t, res.body, "\"semantic\"",
		"the ILIKE text path must not publish a semantic block AT ALL — it computed no cosine, "+
			"so a ranked_candidates or a similarity_scale there would be a fabrication")
	require.Nil(t, text.Semantic)
	for _, it := range text.Items {
		_, has := it["similarity"]
		require.False(t, has, "no item on the text path may carry a similarity")
	}

	// A cosine floor on a page the text path is about to serve is refused
	// rather than ignored. Silently dropping it is indistinguishable from a
	// server with no floor — the aihub#267/#271/#280 family's whole subject.
	_, res = f.get(t, "query=neighbour&min_similarity=0.5")
	require.Equal(t, http.StatusBadRequest, res.code,
		"min_similarity on the text path must 400, not silently no-op; got %d: %s", res.code, res.body)
	_, res = f.get(t, "min_similarity=0.5")
	require.Equal(t, http.StatusBadRequest, res.code,
		"min_similarity with no query=/similar_to= at all must 400; got %d: %s", res.code, res.body)
}

// planeEmbedProvider is a deterministic provider that embeds EVERY text to the
// same unit vector on the z axis. The fixture's rows all lie in the xy-plane,
// so every cosine it produces is exactly 0 — which makes "the vector path ran
// and returned rows" and "a floor excluded them" separable without depending on
// any model behaviour. It reports the fixture's own model id and dimension so
// the emb_model predicate MATCHES: a provider whose model differed would empty
// the page for that reason instead, and the test below would pass for the wrong
// reason.
type planeEmbedProvider struct{}

func (planeEmbedProvider) Embed(context.Context, string) ([]float32, error) {
	return []float32{0, 0, 1}, nil
}

func (p planeEmbedProvider) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{0, 0, 1}
	}
	return out, nil
}

func (planeEmbedProvider) ModelID() string            { return "simtest-model-v1" }
func (planeEmbedProvider) Dims() int                  { return 3 }
func (planeEmbedProvider) Ping(context.Context) error { return nil }

// ─── An empty vector page IS the answer when the caller set a floor ─────────
//
// The regression this pins is subtle and it was live in an earlier draft of
// this change. With a provider configured, query= takes the vector path; if the
// caller's floor legitimately excludes everything, the page is empty. The old
// code read "empty" as "nothing embedded matched" and fell through to the ILIKE
// text path, which has TWO wrong outcomes at once: ILIKE cannot apply a cosine,
// so rows BELOW the floor come back and the floor is silently discarded; and if
// the floor guard fires instead, a correctly-served request is answered with a
// 400 claiming the vector path was never used.
//
// RecallWithVector already had this exact clause on the memory side
// (`len(r.Items) == 0 && req.SimilarityThreshold <= 0` — "a caller-set
// SimilarityThreshold means empty is intended"); this asserts the work-item
// path agrees. The query text below is chosen to MATCH two rows under ILIKE, so
// a fall-through is visible as rows rather than as an empty page that would
// look identical to the correct answer.
func TestSimilarTo_FloorMakesAnEmptyVectorPageAuthoritative(t *testing.T) {
	f := newSimilarToFixture(t)
	domain.InitEmbeddingProvider(planeEmbedProvider{})
	t.Cleanup(func() { domain.InitEmbeddingProvider(&embedding.NoopProvider{}) })

	// Positive control first: with a provider, query= really does take the
	// vector path and really does return these rows, all at cosine 0.
	served, res := f.get(t, "query=neighbour")
	require.Equal(t, http.StatusOK, res.code, res.body)
	require.Len(t, served.Items, 4, "the vector path ranks all four embedded rows, not just ILIKE's two")
	require.NotNil(t, served.Semantic)
	require.Equal(t, domain.SemanticModeTextQuery, served.Semantic.Mode)
	require.Equal(t, 4, served.Semantic.RankedCandidates)
	require.InDelta(t, 0.0, simOf(t, served.Items[0]), 1e-6)

	// Now the same query with a floor nothing can clear.
	empty, res := f.get(t, "query=neighbour&min_similarity=0.5")
	require.Equal(t, http.StatusOK, res.code,
		"a floor that excludes everything is an ANSWER, not a 400: %s", res.body)
	require.Empty(t, empty.Items,
		"ILIKE must not top up a floored vector page — those rows are below the floor the "+
			"caller set, and returning them discards the parameter silently")
	require.NotNil(t, empty.Semantic,
		"the page was served by the vector path, so it must still say so — otherwise the "+
			"caller cannot tell an enforced floor from a fall-through")
	require.Equal(t, 0.5, empty.Semantic.MinSimilarity)
	require.Zero(t, empty.Semantic.RankedCandidates)
}

// brokenEmbedProvider fails every Embed call, standing in for the condition
// that actually happens in production: the embedding backend is down or slow
// enough to blow the request budget. It reports a usable ModelID/Dims so that
// the failure is unambiguously the Embed call and not a model mismatch.
type brokenEmbedProvider struct{}

func (brokenEmbedProvider) Embed(context.Context, string) ([]float32, error) {
	return nil, errors.New("embedding backend unreachable")
}

func (brokenEmbedProvider) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("embedding backend unreachable")
}

func (brokenEmbedProvider) ModelID() string            { return "simtest-model-v1" }
func (brokenEmbedProvider) Dims() int                  { return 3 }
func (brokenEmbedProvider) Ping(context.Context) error { return nil }

// ─── A provider outage must not be reported as the caller's mistake ─────────
//
// The sibling of Gate D, and the one the floor guard gets wrong if it is
// written as a bare fall-through. When the vector path ERRORS (embed timeout,
// backend down, a failed vector query) the aihub#270 behaviour is to degrade
// to ILIKE and still answer 200 — correct, and kept. But with a floor set,
// degrading is not available: ILIKE cannot apply a cosine, so falling through
// reaches the min_similarity guard and answers "pass a query= on a server with
// an embedding provider" — which is precisely what the caller did. A 400 is a
// statement about the caller's request; this is a statement about the server.
func TestSimilarTo_ProviderOutageWithAFloorIsNotBlamedOnTheCaller(t *testing.T) {
	f := newSimilarToFixture(t)
	domain.InitEmbeddingProvider(brokenEmbedProvider{})
	t.Cleanup(func() { domain.InitEmbeddingProvider(&embedding.NoopProvider{}) })

	// Without a floor: the outage degrades to ILIKE and the caller still gets
	// rows. This is the pre-existing aihub#270 contract and must not change.
	degraded, res := f.get(t, "query=neighbour")
	require.Equal(t, http.StatusOK, res.code, res.body)
	require.NotEmpty(t, degraded.Items, "an outage must still degrade to the ILIKE text path")
	require.Nil(t, degraded.Semantic, "ILIKE served it, so there is no semantic block")

	// With a floor: not a 400, and not silently-unfiltered ILIKE rows either.
	_, res = f.get(t, "query=neighbour&min_similarity=0.5")
	require.NotEqual(t, http.StatusBadRequest, res.code,
		"a provider outage is not a malformed request — a 400 here tells the caller to do "+
			"the thing they already did, and it is impossible to act on: %s", res.body)
	require.Equal(t, http.StatusInternalServerError, res.code,
		"the server-side failure must be surfaced as such: %s", res.body)
}

// ─── The parameter's edges: every rejection is explicit ────────────────────
func TestSimilarTo_RejectionsAreExplicitNeverSilent(t *testing.T) {
	f := newSimilarToFixture(t)

	// Mutually exclusive with query=: two different query vectors were asked
	// for, and picking one silently is the defect class, not the fix.
	_, res := f.get(t, "similar_to="+f.srcID+"&query=neighbour")
	require.Equal(t, http.StatusBadRequest, res.code, res.body)
	require.Contains(t, res.body, "mutually exclusive")

	// Similarity ordering has no stable pagination key, exactly as for query=.
	for _, combo := range []string{"sort=created_at", "order=asc", "cursor=2026-01-01T00:00:00Z"} {
		_, res := f.get(t, "similar_to="+f.srcID+"&"+combo)
		require.Equal(t, http.StatusBadRequest, res.code, "similar_to+%s must 400: %s", combo, res.body)
	}

	// A source that does not exist (or is out of scope) is a 404 — NOT an empty
	// page, which would read as "this work item has no neighbours".
	_, res = f.get(t, "similar_to=wi_definitelyNotReal")
	require.Equal(t, http.StatusNotFound, res.code, res.body)

	// A source that exists but carries no vector is a distinct, non-404 answer:
	// it is a server state (embeddings off, or a backfill not yet run), and it
	// must not be reported as "no neighbours" either.
	var unembeddedID string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT id FROM work_items WHERE project=$1 AND goal=$2`, f.project, f.unembeddedGoal,
	).Scan(&unembeddedID))
	_, res = f.get(t, "similar_to="+unembeddedID)
	require.Equal(t, http.StatusPreconditionFailed, res.code, res.body)
	require.Contains(t, res.body, "no embedding")

	// Out-of-range and malformed floors are refused, not clamped: 1.5 would
	// otherwise return an empty page indistinguishable from "no neighbours",
	// and NaN compares false against every bound.
	for _, bad := range []string{"1.5", "-0.1", "notanumber", "NaN", "Inf"} {
		_, res := f.get(t, "similar_to="+f.srcID+"&min_similarity="+bad)
		require.Equal(t, http.StatusBadRequest, res.code,
			"min_similarity=%s must 400, got %d: %s", bad, res.code, res.body)
	}

	// A source in a project this request does not cover is reported as not
	// found rather than used — the scope of the source is the scope of the
	// results, so no separate access check exists to be forgotten.
	otherProject := f.project + "x"
	_, err := f.pool.Exec(context.Background(),
		`INSERT INTO projects(name,owner_user_id) VALUES($1,$2) ON CONFLICT (name) DO NOTHING`,
		otherProject, f.uid)
	require.NoError(t, err)
	_, err = f.pool.Exec(context.Background(), `DELETE FROM work_items WHERE project=$1`, otherProject)
	require.NoError(t, err)
	outsideID := seedVecWorkItem(t, f.pool, otherProject, f.uid, "a row in another project", 8200, "[1,0,0]", f.model)
	_, res = f.get(t, "similar_to="+outsideID)
	require.Equal(t, http.StatusNotFound, res.code,
		"a source outside the result scope must be 404, not silently used: %s", res.body)

	// The same rule on the OTHER access branch. With no project= the scope
	// becomes every project the caller can see (AccessibleProjects) rather than
	// one named project, and that is a separate SQL predicate — so it is a
	// separate way for the source lookup to escape its scope. The user here
	// holds a role on f.project only.
	c, rec := newListWIRequest(t, "similar_to="+outsideID, f.uc)
	require.NoError(t, handleListWorkItems(f.pool)(c))
	require.Equal(t, http.StatusNotFound, rec.Code,
		"unscoped (no project=) similar_to must still refuse a source in a project the caller "+
			"has no role on: %s", rec.Body.String())

	// ...and the positive control for that same branch, so the 404 above is
	// known to come from the scope predicate rather than from the unscoped path
	// being broken outright.
	c, rec = newListWIRequest(t, "similar_to="+f.srcID, f.uc)
	require.NoError(t, handleListWorkItems(f.pool)(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var unscoped listResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &unscoped))
	require.NotEmpty(t, unscoped.Items)
	require.Equal(t, f.srcID, unscoped.Items[0]["id"])
}
