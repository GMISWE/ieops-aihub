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
	"os"
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
