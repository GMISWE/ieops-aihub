# aihub#367 — How often does semantic recall actually return the answer?

**Read-only measurement, run 2026-09-05 against production (`10.146.0.34:8080`), model `Qwen/Qwen3-Embedding-0.6B`.**
44 queries with independently pre-determined answers. No writes, no re-embedding, no backfill,
no `aihub-embed-verify`. All calls serial.

---

## 1. The headline

Criterion is **"is the target in the top N"** — never a similarity threshold (`aihub#276`).

| query set | n | recall@1 | recall@5 | recall@10 | chance@10 |
|---|---|---|---|---|---|
| **A — production shape** (`query = wi.goal`, target = memories attached to that wi) | 18 | **0.0%** (0/18) | **11.1%** (2/18) | **33.3%** (6/18) | 14.1% |
| **B — authored queries** (6 targets × long/short/vague) | 18 | **0.0%** (0/18) | **11.1%** (2/18) | **11.1%** (2/18) | 6.7% |
| **C — work-item retrieval** (`pf_list_work_items(query=)`) | 6 | **0.0%** (0/6) | **0.0%** (0/6) | **0.0%** (0/6) | 2.7% |
| **all** | 42 | **0.0%** (0/42) | **9.5%** (4/42) | **19.0%** (8/42) | — |

Overall recall@10 95% CI (Wilson): **10.0% – 33.3%**. Family A alone: 16.3% – 56.3%.

**`recall@1 = 0/42`.** Not one query in this run put its own answer first.

`chance@10` is the expected hit rate of a *random* ranker over the same candidate set
(150 memories / 367 work items) with the same ground-truth set sizes. The system beats random
by roughly **2×**, on a corpus of 150 documents. That is the whole signal.

### Why this matters more than the raw percentage

Production Memory-First recall (injected at every pf-execute step) uses **`top_k=5`**.
The nearest measured number is **recall@5 = 9.5%** — and the 5 items it does return are displayed
to the agent as relevant. So ~90% of the time the step is handed five confidently formatted,
wrong memories.

⚠️ Not an exact reproduction of that call: production filters
`type=["experience.*","rule.*"]` whereas this run also included `fact.*`, which enlarges the
candidate set from ~120 to 150 and can only push a ground-truth item **down**. The production
figure is therefore ≥ 9.5%, not necessarily equal to it.

---

## 2. The finding that decides the question

> **For 11 of the 12 family-A queries that missed, at least one of that query's ground-truth
> memories was observed inside the top-10 of a different, unrelated query in this same run.**
> (The exception is A239, whose two ground-truth memories were never observed in any top-10 here
> — absence of observation, not evidence of absence, since only 44 pages were sampled.)

The documents are indexed and reachable. What is broken is that **rank is not a function of the
query's topic.** Fourteen such pairs are recorded in `results.json`; the sharpest:

| memory | missed by | but retrieved at rank N by |
|---|---|---|
| `mem_ebytq6ql` "aihub#361 只把「一个索引两种语义」同步地修好了" | **aihub#361's own goal** (A361) | #4 for *"markdown 里直接内联 svg 为什么会渲染失败"*, #5 for *"Can a PR description include an AI co-author line"* |
| `mem_6rxQov7o` "`pf_pr` bodies must carry no AI-attribution trailer" | all 3 of its own queries (B4) | #4 for *"d2 编译传 context.Background() 无超时"* |
| `mem_OXMaGmjL` "lock retention: every release path moves the ATTEMPT" | A355 + all 3 of B6 | #5 for *"already_held empty"* |
| `mem_59mH9zUr` "chromium.launch() 裸调必失败" | all 3 of its own queries (B2) | #2 for *"lock leak on cancel"* |
| `mem_aEqjUNQn` "AST 闸按「函数」定作用域会误报" | all 3 of its own queries (B1) | #1 for two unrelated queries, **#5 for the beef-stew control query** |
| `mem_DlG8Cv7W` "d2lib.Compile IGNORES its context (aihub#250)" | **A250, which is aihub#250's own goal** | #8 for *"already_held empty"* |

A handful of documents behave as **hubs** — `mem_aEqjUNQn`, `mem_iNC1yG01`, `mem_Z8ow5NYa`,
`mem_0aw20deK`, `mem_nzy7BHuX`, `mem_8KuSaRGW`, `mem_D2jxE0Qd` — appearing in the top-10 of
queries with no topical relation to them, including the controls. That is the classic
single-vector hubness signature.

Two further single-query illustrations:

- **A262**: query is essentially the target's own title (*"裸内联 `<svg>` 被 CommonMark 空行规则切断"*).
  The target ranks **7th**, behind *"读「已安装插件」当权威时缓存目录会被换掉"* (0.7975) and
  *"写结构化 block 必须同时传 description"* (0.7696). Its own cosine: 0.7073.
- **B5-S**: the query is the literal string `already_held empty`; the target's first line is
  ``**`pf_acquire_locks` returning `already_held: []` does NOT mean the lock is absent**``.
  **Not in the top 10.** There is no lexical component on the vector path to catch this.

---

## 3. Negative controls — a full page is not a hit

| control | returned | similarity band |
|---|---|---|
| CTRL-1 `b74e0c19aa62f5d38e10cc47` (gibberish hex) | **10 / 10** | 0.369 – 0.396 |
| CTRL-2 `怎么用高压锅炖牛肉才软烂` (off-topic recipe) | **10 / 10** | 0.404 – 0.467 |
| B4-S — *real*, on-topic, English | 10 / 10 | 0.366 – 0.435 |
| B5-L — *real*, on-topic, English (a **hit** at rank 3) | 10 / 10 | 0.359 – 0.419 |

The off-topic cooking question scores **higher** than a real query that actually succeeded.
This independently re-confirms `aihub#276` on a fresh sample: **no similarity cutoff separates a
real query from garbage**, and `pf_list_work_items` publishes `ranked_candidates: 367` for every
input — a full page always.

---

## 4. Buckets

### 4a. Query language

| bucket | n | @1 | @5 | @10 |
|---|---|---|---|---|
| zh (A + B-zh + C-zh) | 30 | 0.0% | 10.0% | 23.3% |
| en (B-en + C-en) | 12 | 0.0% | 8.3% | 8.3% |
| **controlled subset — family B only** (same corpus, GT=1, same 3 shapes) | | | | |
| · B zh | 9 | 0.0% | 11.1% | 11.1% |
| · B en | 9 | 0.0% | 11.1% | 11.1% |

**No language effect is detectable in the controlled comparison — identical, 1/9 each.** The
apparent zh advantage in the top half is confounded: family A is 100% zh *and* has ground-truth
sets of 1–6 items (higher chance baseline) *and* uses much longer queries. Cosine *values* remain
incomparable across languages (zh 0.45–0.82 vs en 0.36–0.72 observed here) — only ranks were compared.

Language *is* the one thing the embedding reliably encodes: English queries mostly return English
pages (A202, B4-V, B5-*, B6-*), Chinese queries Chinese pages. It separates languages, not topics.

### 4b. Target document length

Family B (one target each, three shapes each):

| target length | recall@10 | targets |
|---|---|---|
| < 1 500 runes | 0/6 | 742, 1074 |
| 1 500 – 3 000 | 1/6 | 1770, 2416 |
| > 3 000 | 1/6 | 4410, 4682 |

Family A, length of the **shortest** ground-truth doc per query:

| | n | median | values |
|---|---|---|---|
| HIT queries | 6 | **1 406** runes | 848, 1074, 1178, 1634, 1676, 1878 |
| MISS queries | 12 | **2 732** runes | 742, 1443, 2545, 2560, 2601, 2681, 2782, 2783, 3293, 3446, 3941, 5233 |

Hits skew ~2× shorter than misses. Five of the six documents actually retrieved were under
1 900 runes. This is the direction chunking predicts (a longer document's single vector is a
mean over more unrelated material), but n=18 and the family-B split does not reproduce it —
**suggestive, not established.**

### 4c. Query length as a fraction of the target's full text

| ratio | recall@10 |
|---|---|
| < 2% | 1/9 (11.1%) |
| 2 – 5% | 1/13 (7.7%) |
| 5 – 15% | 4/10 (40.0%) |
| > 15% | 2/4 (50.0%) |

Monotone-ish and consistent with `aihub#311`'s single point (**60% of the source text → rank
212/359**): the more of the document you already quote, the better it works. Every query in this
run sits at **≤ 25%**, i.e. deep in the failing regime — which is exactly where real queries live.
A user who could supply 60% of the answer would not be searching for it.

### 4d. Query shape (controlled — same 6 targets)

| shape | recall@10 | median query length |
|---|---|---|
| long / specific | 1/6 | 70 chars |
| short / keyword | 1/6 | 14 chars |
| **vague ("我记得有这么回事")** | **0/6** | 85 chars |

**The vague shape — the most common real usage form — scored zero.** Note also B3: the *short*
query `内联 svg 渲染失败` hit at rank 3 while the *long* natural-language question about the same
target missed entirely. Verbosity does not help.

---

## 5. Corpus facts established read-only

`DATABASE_URL` is unset on this machine and no credentials were sought, so the `SELECT` named in
the wi was not run. A read-only substitute answers the same question:

- Vector-path `total` at `min_strength=0.001` = **161**.
- Text-path `total` with the identical filter = **161**.

The vector path's `WHERE` carries `emb_vector IS NOT NULL AND emb_model = <current>`
(`memory_vector.go:63-69`), so the two totals being equal means:

> **All 161 embeddable-type memories in project `aihub` have a vector at the current model.
> Zero un-embedded rows. Zero rows stranded on a stale `emb_model`.**

The wi's "Unverified" item is resolved: **missing embeddings are not the problem.** The
production default `min_strength=0.3` then filters 161 → **150** (11 memories, 6.8%, decayed out
of reach) — a separate, smaller issue.

---

## 6. How the query set was built (and why the answers are independent)

Frozen and **committed before the first query ran** — `ce13215`,
`.tmp-367/queryset_frozen.json` + `.tmp-367/familyA.json`.

- **Family A (18) — strongest independence.** Ground truth is the `memories.work_item_id`
  column, a DB fact, fetched on the deterministic text path. The query is the work item's
  `goal`, **written by a human at wi-creation time, before the memory existed**. I authored
  neither side. Sampling was systematic by position (every 3rd of the 60 work items that have
  attached memories, ordered by seq desc) — never by content. This is also *literally the
  production query*: `pf-execute`'s Memory-First step issues `pf_recall(query=<wi.goal>)` at
  every step, so family A measures the real query distribution, not a proxy.
- **Family B (18).** Targets picked from the corpus listing stratified by length (742 → 4 682
  runes, 3 zh + 3 en). Queries were written **from each target's first line only** — the full
  bodies were deliberately not read first, to avoid lifting verbatim phrasing.
- **Family C (6).** Targets picked by position across the seq range; questions written from
  their goals; 3 zh + 3 en.
- **Controls (2).** No correct answer exists by construction.
- Deliberately **excluded**: querying a memory with its own body text. That is a known-failing
  extreme point (`mem_…`, `aihub#360`), not a usage shape.

**Known bias, and its direction.** Families B and C were authored by someone who had just seen
the target's title, so they share vocabulary with the target more than a real forgetful user's
query would. That makes these numbers an **upper bound**. Family A is free of this bias and is
the number to trust.

### Limits

- n=42. Enough to separate "near 0%" from "near 100%"; **not** enough to resolve a 10-point
  difference between buckets. Every per-bucket cell is n=4–18 — read them as direction, not value.
- `top_k=10` was requested, so a miss means "not in top 10", not a measured deep rank. The
  rank-decoupling evidence in §2 is what establishes the targets are present and reachable.
- The memory ordering is `round(cosine, 2) DESC, eff_strength DESC` (`memory_vector.go:187`), so
  ranks inside a 0.01 cosine band are broken by recency/strength, not similarity. Observed
  repeatedly (e.g. A361 rank 5 = 0.7370 sits above rank 6 = 0.7424). Work-item ordering is pure
  cosine and scored **0/6**, so the bucketing is not what is causing the failure.

---

## 7. Verdict

**The number supports doing chunking (`aihub#364`) — but chunking alone will not be sufficient,
and it should not be the first thing shipped.**

**Supports it:**
1. `recall@1 = 0/42` and `recall@5 = 9.5%`, on a **150-document** corpus. This is not a
   degradation to be tuned; the feature does not presently work.
2. It is not a data problem. Every document is embedded (§5) and every missed target is
   retrievable by *some* query (§2). The defect is in the representation, which is exactly what
   chunking changes.
3. The two length signals (§4b hits ~2× shorter than misses; §4c recall rising monotonically
   with quoted fraction) both point the way a mean-pooled single vector over a long document
   predicts, and both agree with `aihub#311`'s independent single point.

**Argues against betting only on it:**
1. Family B's targets span 742 → 4 682 runes and recall@10 is flat at 0/6, 1/6, 1/6. If document
   length were the dominant term, the 742-rune target would be easy. It scored 0/3.
2. `mem_6rxQov7o` is **742 runes** — one short paragraph, already effectively a single chunk —
   and all three queries about it missed while an unrelated d2-timeout query retrieved it at #4.
   **Chunking cannot fix a document that is already chunk-sized.**
3. Work-item retrieval scored **0/6** with a pure-cosine ordering and no bucketing.
4. Hubness (§2) and the control results (§3) are properties of the embedding's geometry, not of
   the segmentation. Chunking multiplies the number of vectors in that same geometry.

**Recommended order, cheapest-first — each is testable against this same 44-query set:**

1. **Add a lexical channel (BM25 / Postgres FTS) and fuse it with the vector score.** B5-S is the
   proof it is missing: the exact token `already_held` fails to retrieve the one document whose
   first line contains it. This is days of work, no re-embedding, and no operational risk.
2. **Re-check the model choice.** A 0.6B embedder packs an entire result set into a ~0.04-wide
   cosine band (already noted in `memory_vector.go:158-162`), and an off-topic recipe outscores
   real queries. A stronger or instruction-tuned model is a config change plus one backfill —
   the same cost as chunking, against a cause this data implicates more directly.
3. **Then chunking**, measured on the frozen set rather than assumed.

**Do not ship any of the three without re-running this set.** It is the only baseline that
exists, and every number above is a re-runnable command, not an estimate.

⚠️ Operational note carried forward: the 2026-09-01 production incident came from a **30-row
concurrent** embed sweep. This run was 44 **serial** single-query calls; every one returned 200
with no error or timeout. (That is what was observed — server-side load was not instrumented, so
this is not a claim about production impact.) A full re-embed for chunking is 10–20× the corpus
and must not reuse the concurrent pattern.

---

*Raw per-query ranks, the 14 retrieved-by-the-wrong-query pairs, and the frozen query set are in
`.tmp-367/results.json` and `.tmp-367/queryset_frozen.json` on branch
`polyforge/aihub-367-aihub-query-n`.*
