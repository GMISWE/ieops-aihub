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

// TestRecallPositiveControl_Live is the reporter's acceptance gate for aihub#311
// (see the memories on wi_zAAnriiC for the full investigation): seed a batch of
// already-embedded memories, query each with a VERBATIM SUBSTRING of its own
// content, and assert the target ranks #1. Investigation showed unrelated
// queries scoring higher than a memory's own verbatim text, with similarities
// for wildly different queries clustering in a narrow, non-discriminating band
// — this test is the concrete, runnable form of that observation.
//
// This is expected and correct to be RED against a real embedding endpoint
// today. Root cause is not yet known (truncation and query-side instruction
// prefixing were both investigated and ruled out — see mem_8KuSaRGW and
// mem_hMa6uTMN on wi_zAAnriiC). Going green is the definition of "fixed" for
// that bug; do not weaken this assertion to make it pass.
//
// Same gating as TestRecallWithVector_Live — skipped unless AIHUB_TEST_DB and
// EMBEDDING_BASE_URL are set:
//
//	AIHUB_TEST_DB=postgres://postgres:test@localhost:5440/aihub_test?sslmode=disable \
//	EMBEDDING_BASE_URL=http://10.138.0.22:8090 EMBEDDING_MODEL=q \
//	go test ./internal/domain/ -run TestRecallPositiveControl_Live -v -count=1
func TestRecallPositiveControl_Live(t *testing.T) {
	dbURL := os.Getenv("AIHUB_TEST_DB")
	baseURL := os.Getenv("EMBEDDING_BASE_URL")
	if dbURL == "" || baseURL == "" {
		t.Skip("set AIHUB_TEST_DB and EMBEDDING_BASE_URL to run the live positive-control test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	mustExec(t, pool, `INSERT INTO users(id,email,display_name) VALUES('u_pc','pc@test.local','pc') ON CONFLICT (id) DO NOTHING`)
	mustExec(t, pool, `INSERT INTO projects(name,owner_user_id) VALUES('pcproj','u_pc') ON CONFLICT (name) DO NOTHING`)
	mustExec(t, pool, `DELETE FROM memories WHERE project='pcproj'`)

	model := os.Getenv("EMBEDDING_MODEL")
	if model == "" {
		model = "q"
	}
	// 4096 dims (vs. production's 1024) matches the other _Live tests in this
	// file; it is self-consistent here because write and read both go through
	// this same provider config, so it is not evidence about aihub#311.
	InitEmbeddingProvider(embedding.NewOpenAI("", model, 4096, baseURL))
	defer InitEmbeddingProvider(&embedding.NoopProvider{})

	// Five distinct-topic memories, deliberately mixing Chinese and English
	// content the way the reported corpus does — aihub#311's own repro noted
	// English queries only surfacing English memories and vice versa, so a
	// same-language-only corpus here could not have caught that failure mode.
	// Each `substring` is copied VERBATIM out of the middle of its own
	// `content`, exactly as the bug report's judgment criterion requires.
	docs := []struct {
		topic     string
		content   string
		substring string
	}{
		{
			topic:     "lock_conflict",
			content:   "CONFLICT_LOCK_TAKEN: resource git_branch:pcproj-v2/main is already locked by another attempt; retry after the holder releases, or force-takeover if the holder is abandoned.",
			substring: "resource git_branch:pcproj-v2/main is already locked by another attempt",
		},
		{
			topic:     "zsh_refspec",
			content:   "zsh 下 git push 报 refspec 不匹配,是因为默认 branch 名称与远端 HEAD 不一致,需要显式指定 refspec 或把 push.default 改成 current,否则每次 push 都要手写目标分支。",
			substring: "是因为默认 branch 名称与远端 HEAD 不一致,需要显式指定 refspec",
		},
		{
			topic:     "kind_registry",
			content:   "dev-env.sh up 一键冷启 4 个 kind 集群加 helm umbrella chart, IMAGES=local 会把镜像推到共享的 kind-registry, 这样多个集群之间不用各自重复构建同一份镜像。",
			substring: "IMAGES=local 会把镜像推到共享的 kind-registry",
		},
		{
			topic:     "oci_eloop",
			content:   "brslet OCI 解压遇到 ELOOP 是因为 /etc/alternatives 目录下存在自引用的符号链接,修法是把 SafeJoinParent 对父级路径的解析改成走字面量 base,不再跟随符号链接。",
			substring: "SafeJoinParent 对父级路径的解析改成走字面量 base",
		},
		{
			topic:     "gateway_502",
			content:   "ieops-v2 gateway 按 group 路由, 同区出现 502 的根因是 HasRegionPriority 与跨区配置耦合在一起, 心跳驱动的路由表是自动派生的, 跨区流量应当显式声明优先级而不是依赖默认值。",
			substring: "同区出现 502 的根因是 HasRegionPriority 与跨区配置耦合在一起",
		},
	}

	ids := make(map[string]string, len(docs))
	topicByID := make(map[string]string, len(docs))
	for _, d := range docs {
		if !strings.Contains(d.content, d.substring) {
			t.Fatalf("test bug: substring for %q is not actually verbatim in its own content", d.topic)
		}
		m, _, rerr := Remember(ctx, pool, &RememberRequest{
			Project:       "pcproj",
			Type:          "experience.pitfall",
			Content:       d.content,
			Visibility:    "project",
			DedupMode:     "off",
			CallerUserID:  "u_pc",
			CallerDisplay: "pc",
		})
		if rerr != nil {
			t.Fatalf("remember %s: %v", d.topic, rerr)
		}
		ids[d.topic] = m.ID
		topicByID[m.ID] = d.topic
	}

	// write path sanity check, same shape as TestRecallWithVector_Live.
	var nonNull int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE emb_vector IS NOT NULL) FROM memories WHERE project='pcproj'`,
	).Scan(&nonNull); err != nil {
		t.Fatalf("verify query: %v", err)
	}
	if nonNull != len(docs) {
		t.Fatalf("emb_vector stored for %d/%d memories", nonNull, len(docs))
	}

	// The positive control itself: for every doc, querying with a verbatim
	// substring of ITS OWN content must rank that same memory #1.
	var failures int
	for _, d := range docs {
		wantID := ids[d.topic]
		resp, rerr := Recall(ctx, pool, &RecallRequest{
			Project:      "pcproj",
			Query:        d.substring,
			TopK:         10,
			MinStrength:  0,
			CallerUserID: "u_pc",
			CallerRole:   "writer",
		})
		if rerr != nil {
			t.Errorf("topic=%s: Recall error: %v", d.topic, rerr)
			failures++
			continue
		}

		rank := -1
		for i, it := range resp.Items {
			if it.ID == wantID {
				rank = i + 1
				break
			}
		}

		if rank == 1 {
			continue // pass — no need to build the diagnostic dump below
		}
		failures++

		var b strings.Builder
		fmt.Fprintf(&b, "aihub#311 positive control FAILED for topic=%q (memory id=%s)\n", d.topic, wantID)
		fmt.Fprintf(&b, "  query (verbatim substring of the memory's own content): %q\n", d.substring)
		if rank == -1 {
			fmt.Fprintf(&b, "  target memory did not appear in the top %d results at all\n", 10)
		} else {
			fmt.Fprintf(&b, "  target memory ranked #%d, want #1\n", rank)
		}
		fmt.Fprintf(&b, "  got %d result(s):\n", len(resp.Items))
		for i, it := range resp.Items {
			sim := -1.0
			if it.Similarity != nil {
				sim = *it.Similarity
			}
			gotTopic := topicByID[it.ID]
			mark := " "
			if it.ID == wantID {
				mark = "*"
			}
			if gotTopic == "" {
				gotTopic = "(not one of this test's seed memories)"
			}
			fmt.Fprintf(&b, "  %s #%d id=%s sim=%.6f topic=%s\n", mark, i+1, it.ID, sim, gotTopic)
		}
		t.Error(b.String())
	}
	if failures > 0 {
		t.Logf("aihub#311 positive control: %d/%d topics failed to self-rank #1 — this is the RED state the bug report describes; it must go GREEN for #311 to be considered fixed", failures, len(docs))
	}
}
