"""nDCG@5 measurement runner.

Usage:
  # Production default (recency_boost=1.0 — same as real traffic):
  python scripts/measure_search_quality.py --live URL ADMIN_KEY

  # Semantic-only baseline (isolates vector+BM25 quality from recency noise):
  python scripts/measure_search_quality.py --live URL ADMIN_KEY --recency-boost 0

Measures search quality against a deployed ieops-mem instance over the
golden fixture corpus + queries.
"""
import argparse
import json
import math
import sys
import time
import urllib.parse
import urllib.request
from pathlib import Path


FIXTURES = Path(__file__).parent.parent / "tests" / "fixtures"


def _ndcg(actual_ids, expected_id):
    try:
        idx = actual_ids.index(expected_id)
    except ValueError:
        return 0.0
    return 0.0 if idx >= 5 else 1.0 / math.log2(idx + 2)


def run_live(url, admin_key, recency_boost=None):
    corpus = json.loads((FIXTURES / "golden_corpus.json").read_text())
    queries = json.loads((FIXTURES / "golden_queries.json").read_text())

    project = f"quality-gate-{int(time.time())}"
    writer_key = "qg-w-" + "a" * 30
    reader_key = "qg-r-" + "a" * 30

    def http(method, path, body=None, key=admin_key):
        req = urllib.request.Request(
            f"{url}{path}", method=method,
            headers={"X-API-Key": key, "Content-Type": "application/json"},
            data=json.dumps(body).encode() if body else None,
        )
        with urllib.request.urlopen(req, timeout=10) as resp:
            return json.loads(resp.read().decode() or "{}")

    http("POST", "/admin/access", {"api_key": writer_key, "project": project, "role": "writer"})
    http("POST", "/admin/access", {"api_key": reader_key, "project": project, "role": "reader"})

    seeded = {}
    try:
        for doc in corpus:
            r = http("POST", "/memories",
                     {"project": project, "type": doc["type"], "content": doc["content"]},
                     key=writer_key)
            seeded[doc["id"]] = r["id"]

        ndcgs = []
        for q in queries:
            body = {"project": project, "query": q["query"], "top_k": 5}
            if recency_boost is not None:
                body["recency_boost"] = recency_boost
            res = http("POST", "/memories/search", body, key=reader_key)
            expected_actual_id = seeded[q["expected_top_id"]]
            actual = [item["memory"]["id"] for item in res["results"]]
            ndcgs.append(_ndcg(actual, expected_actual_id))
        mean = sum(ndcgs) / len(ndcgs)
        rb_label = f"recency_boost={recency_boost}" if recency_boost is not None else "recency_boost=default(1.0)"
        print(f"nDCG@5 = {mean:.4f} over {len(queries)} queries on {url} [{rb_label}]")
        return mean
    finally:
        for actual_id in seeded.values():
            try:
                http("DELETE", f"/memories/{actual_id}", key=writer_key)
            except Exception:
                pass
        entries = http("GET", f"/admin/access?project={urllib.parse.quote(project)}").get("entries", [])
        for entry in entries:
            try:
                http("DELETE", f"/admin/access/{entry['key_id']}/{project}")
            except Exception:
                pass


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--live", nargs=2, metavar=("URL", "ADMIN_KEY"))
    p.add_argument("--recency-boost", type=float, default=None, metavar="N",
                   help="Override recency_boost (default: server default 1.0). "
                        "Use 0.0 for pure semantic-quality baseline independent of insertion order.")
    args = p.parse_args()
    if args.live:
        run_live(*args.live, recency_boost=args.recency_boost)
    else:
        p.print_help()
        sys.exit(2)


if __name__ == "__main__":
    main()
