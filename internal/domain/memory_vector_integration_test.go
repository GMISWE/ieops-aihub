package domain

// Live integration test for the pgvector recall path (aihub#192).
// Skipped unless AIHUB_TEST_DB + EMBEDDING_BASE_URL are set, so it never runs
// in plain `go test ./...`. Run it against a pgvector container + a real
// OpenAI-compatible embedding endpoint:
//
//	AIHUB_TEST_DB=postgres://postgres:test@localhost:5440/aihub_test?sslmode=disable \
//	EMBEDDING_BASE_URL=http://10.138.0.22:8090 EMBEDDING_MODEL=q \
//	go test ./internal/domain/ -run TestRecallWithVector_Live -v -count=1

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/GMISWE/ieops-aihub/internal/embedding"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRecallWithVector_Live(t *testing.T) {
	dbURL := os.Getenv("AIHUB_TEST_DB")
	baseURL := os.Getenv("EMBEDDING_BASE_URL")
	if dbURL == "" || baseURL == "" {
		t.Skip("set AIHUB_TEST_DB and EMBEDDING_BASE_URL to run the live vector test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// idempotent seed (FK: memories.project -> projects, author_user_id -> users)
	mustExec(t, pool, `INSERT INTO users(id,email,display_name) VALUES('u_vt','vt@test.local','vt') ON CONFLICT (id) DO NOTHING`)
	mustExec(t, pool, `INSERT INTO projects(name,owner_user_id) VALUES('vtproj','u_vt') ON CONFLICT (name) DO NOTHING`)
	mustExec(t, pool, `DELETE FROM memories WHERE project='vtproj'`)

	model := os.Getenv("EMBEDDING_MODEL")
	if model == "" {
		model = "q"
	}
	InitEmbeddingProvider(embedding.NewOpenAI("", model, 4096, baseURL))
	defer InitEmbeddingProvider(&embedding.NoopProvider{})

	docs := []struct{ topic, content string }{
		{"vector", "pgvector 语义召回:对 query 算 embedding 做余弦 top-k,RecallWithVector 融合 strength 与 recency 排序"},
		{"gateway", "ieops-v2 gateway 按 group 路由,同区 502 源于 HasRegionPriority 与跨区配置耦合,心跳驱动自动派生路由"},
		{"devenv", "dev-env.sh up 一键冷启 4 个 kind 集群加 helm umbrella chart,IMAGES=local 推到共享 kind-registry"},
		{"oci", "brslet OCI 解压 ELOOP 在 /etc/alternatives,改 SafeJoinParent 父级解析加字面 base 修复"},
	}
	ids := map[string]string{}
	for _, d := range docs {
		m, _, rerr := Remember(ctx, pool, &RememberRequest{
			Project:       "vtproj",
			Type:          "experience.approach",
			Content:       d.content,
			Visibility:    "project",
			DedupMode:     "off",
			CallerUserID:  "u_vt",
			CallerDisplay: "vt",
		})
		if rerr != nil {
			t.Fatalf("remember %s: %v", d.topic, rerr)
		}
		ids[d.topic] = m.ID
	}

	// write path: every embeddable memory got a 4096-dim emb_vector
	var nonNull, dims int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE emb_vector IS NOT NULL), coalesce(max(emb_dims),0) FROM memories WHERE project='vtproj'`,
	).Scan(&nonNull, &dims); err != nil {
		t.Fatalf("verify query: %v", err)
	}
	if nonNull != len(docs) {
		t.Fatalf("emb_vector stored for %d/%d memories", nonNull, len(docs))
	}
	if dims != 4096 {
		t.Fatalf("emb_dims=%d, want 4096", dims)
	}
	t.Logf("write path OK: %d/%d memories have emb_vector, dims=%d", nonNull, len(docs), dims)

	// read path: a query semantically about the vector topic must rank it #1
	resp, err := RecallWithVector(ctx, pool, &RecallRequest{
		Project:      "vtproj",
		Query:        "如何实现向量余弦相似度召回与排序",
		TopK:         4,
		CallerUserID: "u_vt",
		CallerRole:   "writer",
	})
	if err != nil {
		t.Fatalf("RecallWithVector: %v", err)
	}
	if len(resp.Items) == 0 {
		t.Fatal("RecallWithVector returned no items")
	}
	for i, it := range resp.Items {
		sim := -1.0
		if it.Similarity != nil {
			sim = *it.Similarity
		}
		topic := "?"
		for tp, id := range ids {
			if id == it.ID {
				topic = tp
			}
		}
		t.Logf("  #%d sim=%.4f topic=%s", i+1, sim, topic)
	}
	top := resp.Items[0]
	if top.Similarity == nil {
		t.Error("top item Similarity is nil — cosine not populated")
	}
	if top.ID != ids["vector"] {
		t.Errorf("top result id=%s, want vector-topic %s (relevance ranking failed)", top.ID, ids["vector"])
	}
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql); err != nil {
		t.Fatalf("seed exec failed: %v\nSQL: %s", err, sql)
	}
}

// TestRecallHybridTypeUnion_Live is the end-to-end proof for aihub#270: with embedding
// active, a recall whose type union mixes embeddable and non-embeddable types must return
// BOTH halves. Before the fix the vector path answered the whole request, and because
// methodology.* rows have no emb_vector they were dropped silently — no error, no warning,
// and a `total` that still looked healthy.
//
// Same gating as TestRecallWithVector_Live — skipped unless AIHUB_TEST_DB and
// EMBEDDING_BASE_URL are set:
//
//	AIHUB_TEST_DB=postgres://postgres:test@localhost:5440/aihub_test?sslmode=disable \
//	EMBEDDING_BASE_URL=http://10.138.0.22:8090 EMBEDDING_MODEL=q \
//	go test ./internal/domain/ -run TestRecallHybridTypeUnion_Live -v -count=1
func TestRecallHybridTypeUnion_Live(t *testing.T) {
	dbURL := os.Getenv("AIHUB_TEST_DB")
	baseURL := os.Getenv("EMBEDDING_BASE_URL")
	if dbURL == "" || baseURL == "" {
		t.Skip("set AIHUB_TEST_DB and EMBEDDING_BASE_URL to run the live hybrid recall test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	mustExec(t, pool, `INSERT INTO users(id,email,display_name) VALUES('u_hy','hy@test.local','hy') ON CONFLICT (id) DO NOTHING`)
	mustExec(t, pool, `INSERT INTO projects(name,owner_user_id) VALUES('hyproj','u_hy') ON CONFLICT (name) DO NOTHING`)
	mustExec(t, pool, `DELETE FROM memories WHERE project='hyproj'`)

	model := os.Getenv("EMBEDDING_MODEL")
	if model == "" {
		model = "q"
	}
	InitEmbeddingProvider(embedding.NewOpenAI("", model, 4096, baseURL))
	defer InitEmbeddingProvider(&embedding.NoopProvider{})

	const query = "召回路径如何在向量与文本之间选择"

	// Embeddable half — enough rows to fill TopK on their own, which is precisely the
	// condition that used to starve the other half.
	for i := 0; i < 8; i++ {
		if _, _, rerr := Remember(ctx, pool, &RememberRequest{
			Project:       "hyproj",
			Type:          "experience.approach",
			Content:       fmt.Sprintf("召回路径选择与向量检索的经验记录 #%d:余弦相似度、融合排序、回落策略", i),
			Visibility:    "project",
			DedupMode:     "off",
			CallerUserID:  "u_hy",
			CallerDisplay: "hy",
		}); rerr != nil {
			t.Fatalf("remember experience #%d: %v", i, rerr)
		}
	}

	// Non-embeddable half — inserted directly, since methodology.* artifacts are written
	// through pf_save_artifact rather than pf_remember, and never carry an emb_vector.
	for i := 0; i < 4; i++ {
		mustExec(t, pool, fmt.Sprintf(
			`INSERT INTO memories(id,project,author_user_id,type,content,visibility)
			 VALUES ('mem_hy%d','hyproj','u_hy','methodology.spec','召回路径选择的 spec 文档 #%d',
			 'project')`, i, i))
	}

	var embedded int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM memories WHERE project='hyproj' AND emb_vector IS NOT NULL`,
	).Scan(&embedded); err != nil {
		t.Fatalf("verify query: %v", err)
	}
	if embedded != 8 {
		t.Fatalf("embedded rows = %d, want 8 (methodology.* must NOT be embedded)", embedded)
	}

	// The exact union polyforge 1.1.8's pf-spec Step 1 sends.
	resp, aerr := Recall(ctx, pool, &RecallRequest{
		Project:      "hyproj",
		Query:        query,
		Types:        []string{"methodology.spec", "methodology.plan", "fact.*", "rule.*", "experience.*"},
		TopK:         8,
		CallerUserID: "u_hy",
		CallerRole:   "writer",
	})
	if aerr != nil {
		t.Fatalf("Recall: %v", aerr)
	}

	byPrefix := map[string]int{}
	for _, it := range resp.Items {
		byPrefix[strings.SplitN(it.Type, ".", 2)[0]]++
	}
	t.Logf("union recall returned %d items, total=%d, distribution=%v", len(resp.Items), resp.Total, byPrefix)

	if byPrefix["methodology"] == 0 {
		t.Errorf("aihub#270 regression: 0 methodology.* items in a union recall of %d items (%v)",
			len(resp.Items), byPrefix)
	}
	if byPrefix["experience"] == 0 {
		t.Errorf("no experience.* items — the vector half was lost (%v)", byPrefix)
	}
	if len(resp.Items) > 8 {
		t.Errorf("returned %d items, exceeds TopK 8", len(resp.Items))
	}
	if want := 12; resp.Total != want {
		t.Errorf("Total = %d, want %d (8 embeddable + 4 methodology)", resp.Total, want)
	}

	// A filter naming only non-embeddable types must still work — it skips the vector path
	// entirely rather than paying for an embed call that can only come back empty.
	mResp, merr := Recall(ctx, pool, &RecallRequest{
		Project:      "hyproj",
		Query:        query,
		Types:        []string{"methodology.spec", "methodology.plan"},
		TopK:         8,
		CallerUserID: "u_hy",
		CallerRole:   "writer",
	})
	if merr != nil {
		t.Fatalf("Recall (methodology-only): %v", merr)
	}
	if len(mResp.Items) != 4 {
		t.Errorf("methodology-only recall returned %d items, want 4", len(mResp.Items))
	}

	// A purely embeddable filter must stay on the vector path — same shape as before
	// aihub#270, with cosine scores populated and no extra text query.
	eResp, eerr := Recall(ctx, pool, &RecallRequest{
		Project:      "hyproj",
		Query:        query,
		Types:        []string{"experience.*", "rule.*"},
		TopK:         8,
		CallerUserID: "u_hy",
		CallerRole:   "writer",
	})
	if eerr != nil {
		t.Fatalf("Recall (embeddable-only): %v", eerr)
	}
	if len(eResp.Items) == 0 {
		t.Fatal("embeddable-only recall returned nothing")
	}
	for _, it := range eResp.Items {
		if it.Similarity == nil {
			t.Errorf("item %s has nil Similarity — embeddable-only recall left the vector path", it.ID)
			break
		}
	}
}
