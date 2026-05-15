import json
import re
from datetime import datetime, timedelta, timezone
from typing import Optional

from fastapi import APIRouter, Header, HTTPException, Query
from ulid import ULID

import embedder as _embedder
from auth import check_auth
from db import get_db
from models import (
    DeprecateRequest,
    MemoryCreate,
    MemoryListResponse,
    MemoryResponse,
    MemoryUpdate,
    SearchRequest,
    SearchResponse,
    SearchResult,
    row_to_memory,
)

router = APIRouter()

# Column list excluding the 1.5KB embedding BLOB — used by all non-search reads.
_MEM_COLS = (
    "id, project, type, content, metadata, created_at, updated_at, "
    "expires_at, deprecated, deprecated_reason, superseded_by, "
    "author_key_id, showable"
)


def _iso_ms(dt: datetime) -> str:
    # Millisecond precision so same-second writes get distinct updated_at and
    # SQL lex-compare against cutoff/expires_at stays monotonic. Pre-v0.2.3
    # second-precision rows still parse via fromisoformat on read.
    return dt.strftime("%Y-%m-%dT%H:%M:%S.") + f"{dt.microsecond // 1000:03d}Z"


def _now() -> str:
    return _iso_ms(datetime.now(timezone.utc))


def _expires_at(ttl_days: Optional[int]) -> Optional[str]:
    if ttl_days is None:
        return None
    return _iso_ms(datetime.now(timezone.utc) + timedelta(days=ttl_days))


def _can_modify(row, auth: dict) -> bool:
    """admin or original author (NULL author_key_id = legacy data, any writer)."""
    if auth["role"] == "admin":
        return True
    aid = row["author_key_id"]
    return aid is None or aid == auth["key_id"]


def _can_see(row, auth: dict) -> bool:
    """admin always; otherwise showable=1 OR own memory (incl. legacy NULL)."""
    if auth["role"] == "admin":
        return True
    if bool(row["showable"]):
        return True
    aid = row["author_key_id"]
    return aid is None or aid == auth["key_id"]


@router.post("/memories", status_code=201, response_model=MemoryResponse)
async def create_memory(
    body: MemoryCreate,
    x_api_key: Optional[str] = Header(None, alias="X-API-Key"),
):
    auth = check_auth(x_api_key, body.project, "writer")
    vec = await _embedder.embed(body.content, role="passage")
    now = _now()
    mem_id = f"mem-{ULID()}"

    with get_db() as conn:
        conn.execute(
            """INSERT INTO memories
               (id, project, type, content, metadata,
                created_at, updated_at, expires_at, author_key_id, showable)
               VALUES (?,?,?,?,?,?,?,?,?,?)""",
            (
                mem_id,
                body.project,
                body.type,
                body.content,
                json.dumps(body.metadata),
                now,
                now,
                _expires_at(body.ttl_days),
                auth["key_id"],
                1 if body.showable else 0,
            ),
        )
        rowid = conn.execute(
            "SELECT rowid FROM memories WHERE id = ?", (mem_id,)
        ).fetchone()[0]
        conn.execute(
            "INSERT OR IGNORE INTO vec_memories(rowid, embedding) VALUES (?, ?)",
            (rowid, vec.tobytes()),
        )
        row = conn.execute(
            f"SELECT {_MEM_COLS} FROM memories WHERE id = ?", (mem_id,)
        ).fetchone()
    return row_to_memory(row)


@router.post("/memories/search", response_model=SearchResponse)
async def search_memories(
    body: SearchRequest,
    debug: bool = Query(False),
    x_api_key: Optional[str] = Header(None, alias="X-API-Key"),
):
    from search import rrf_fuse, score_max_theoretical, RRF_K_DEFAULT

    auth = check_auth(x_api_key, body.project, "reader")
    q_vec = await _embedder.embed(body.query, role="query")
    now = _now()

    # Visibility / expiry / deprecated filter shared across all channels.
    if auth["role"] == "admin":
        vis_clause = ""
        vis_params: list = []
    else:
        vis_clause = " AND (m.showable = 1 OR m.author_key_id IS NULL OR m.author_key_id = ?)"
        vis_params = [auth["key_id"]]

    common_where = (
        " WHERE m.project = ? AND m.deprecated = 0 "
        " AND (m.expires_at IS NULL OR m.expires_at > ?)"
        + vis_clause
    )
    common_params = [body.project, now] + vis_params

    # Pull at least top_k candidates per channel so a large top_k gets a
    # meaningful pool to fuse over; 50 is the floor when top_k <= 50.
    CHANNEL_TOP_N = max(50, body.top_k)

    # Sanitize query for FTS5: replace non-alphanumeric chars with spaces so
    # that e.g. "admin-visible" doesn't parse as a column NOT filter ("-visible"
    # refers to a column named "visible" which doesn't exist in memories_fts).
    _fts_safe = re.sub(r"[^A-Za-z0-9 ]", " ", body.query).strip()
    _fts_query = _fts_safe if _fts_safe else None

    with get_db() as conn:
        vec_rows = conn.execute(
            f"SELECT X.rowid FROM vec_memories X JOIN memories m ON m.rowid = X.rowid {common_where}"
            f" AND embedding MATCH ? AND k = ?"
            f" ORDER BY distance",
            common_params + [q_vec.tobytes(), CHANNEL_TOP_N],
        ).fetchall()

        if _fts_query:
            bm25_rows = conn.execute(
                f"SELECT X.rowid FROM memories_fts X JOIN memories m ON m.rowid = X.rowid {common_where}"
                f" AND memories_fts MATCH ?"
                f" ORDER BY rank LIMIT ?",
                common_params + [_fts_query, CHANNEL_TOP_N],
            ).fetchall()
        else:
            bm25_rows = []

        recency_rows = conn.execute(
            f"SELECT m.rowid FROM memories m {common_where}"
            f" ORDER BY m.created_at DESC LIMIT ?",
            common_params + [CHANNEL_TOP_N],
        ).fetchall()

    vec_ranks = [r[0] for r in vec_rows]
    bm25_ranks = [r[0] for r in bm25_rows]
    rec_ranks = [r[0] for r in recency_rows]

    # body.recency_boost is reinterpreted as recency channel weight (v0.3.0).
    weights = [1.0, 1.0, float(body.recency_boost)]
    scores = rrf_fuse([vec_ranks, bm25_ranks, rec_ranks], weights)
    max_score = score_max_theoretical(weights, RRF_K_DEFAULT)

    top_k = body.top_k
    top_ids = sorted(scores.keys(), key=lambda r: scores[r], reverse=True)[:top_k]

    if not top_ids:
        return SearchResponse(results=[], score_max_theoretical=max_score)

    placeholders = ",".join("?" * len(top_ids))
    with get_db() as conn:
        result_rows = {
            r["rowid"]: r for r in conn.execute(
                f"SELECT rowid, {_MEM_COLS} FROM memories WHERE rowid IN ({placeholders})",
                top_ids,
            ).fetchall()
        }

    def _rank_of(rowid: int, ch: list) -> Optional[int]:
        try:
            return ch.index(rowid)
        except ValueError:
            return None

    results = []
    for rowid in top_ids:
        row = result_rows.get(rowid)
        if row is None:
            continue
        item = SearchResult(memory=row_to_memory(row), score=scores[rowid])
        if debug:
            item.vector_rank = _rank_of(rowid, vec_ranks)
            item.bm25_rank = _rank_of(rowid, bm25_ranks)
            item.recency_rank = _rank_of(rowid, rec_ranks)
        results.append(item)

    return SearchResponse(results=results, score_max_theoretical=max_score)


@router.get("/memories", response_model=MemoryListResponse)
async def list_memories(
    project: str = Query(...),
    type: Optional[str] = Query(None),
    status: Optional[str] = Query(None),
    external_id: Optional[str] = Query(None),
    max_age_days: Optional[int] = Query(None, ge=0),
    include_deprecated: bool = Query(False),
    limit: int = Query(50, ge=1, le=200),
    offset: int = Query(0, ge=0),
    x_api_key: Optional[str] = Header(None, alias="X-API-Key"),
):
    auth = check_auth(x_api_key, project, "reader")
    now = _now()

    filters = ["project = ?", "(expires_at IS NULL OR expires_at > ?)"]
    params: list = [project, now]

    # Visibility filter: admin sees all; others see showable=1 OR own (incl. legacy NULL).
    if auth["role"] != "admin":
        filters.append("(showable = 1 OR author_key_id IS NULL OR author_key_id = ?)")
        params.append(auth["key_id"])

    if not include_deprecated:
        filters.append("deprecated = 0")
    if type:
        filters.append("type = ?")
        params.append(type)
    if status:
        filters.append("json_extract(metadata, '$.status') = ?")
        params.append(status)
    if external_id:
        # Needed by pf2-sync to dedup external (e.g. GitHub issue) tasks.
        filters.append("json_extract(metadata, '$.external_id') = ?")
        params.append(external_id)
    if max_age_days is not None:
        cutoff = _iso_ms(datetime.now(timezone.utc) - timedelta(days=max_age_days))
        filters.append("created_at >= ?")
        params.append(cutoff)

    where = " AND ".join(filters)
    with get_db() as conn:
        total = conn.execute(
            f"SELECT COUNT(*) FROM memories WHERE {where}", params
        ).fetchone()[0]
        rows = conn.execute(
            f"SELECT {_MEM_COLS} FROM memories WHERE {where} "
            "ORDER BY created_at DESC LIMIT ? OFFSET ?",
            params + [limit, offset],
        ).fetchall()

    return MemoryListResponse(
        memories=[row_to_memory(r) for r in rows],
        total=total,
        limit=limit,
        offset=offset,
    )


@router.get("/memories/{memory_id}", response_model=MemoryResponse)
async def get_memory(
    memory_id: str,
    x_api_key: Optional[str] = Header(None, alias="X-API-Key"),
):
    with get_db() as conn:
        row = conn.execute(
            f"SELECT {_MEM_COLS} FROM memories WHERE id = ?", (memory_id,)
        ).fetchone()
    if not row:
        raise HTTPException(
            404, detail={"code": "NOT_FOUND", "message": f"memory {memory_id} not found"}
        )
    auth = check_auth(x_api_key, row["project"], "reader")
    if not _can_see(row, auth):
        raise HTTPException(
            403,
            detail={"code": "FORBIDDEN", "message": "memory is not visible to this caller"},
        )
    return row_to_memory(row)


@router.put("/memories/{memory_id}", response_model=MemoryResponse)
async def update_memory(
    memory_id: str,
    body: MemoryUpdate,
    x_api_key: Optional[str] = Header(None, alias="X-API-Key"),
):
    with get_db() as conn:
        row = conn.execute(
            f"SELECT {_MEM_COLS} FROM memories WHERE id = ?", (memory_id,)
        ).fetchone()
    if not row:
        raise HTTPException(
            404, detail={"code": "NOT_FOUND", "message": f"memory {memory_id} not found"}
        )
    auth = check_auth(x_api_key, row["project"], "writer")
    if not _can_modify(row, auth):
        raise HTTPException(
            403,
            detail={"code": "FORBIDDEN", "message": "only the author or an admin may modify this memory"},
        )

    updates: dict = {"updated_at": _now()}
    new_vec = None
    if body.content is not None:
        new_vec = await _embedder.embed(body.content, role="passage")
        updates["content"] = body.content
    if body.metadata is not None:
        existing = json.loads(row["metadata"]) if row["metadata"] else {}
        existing.update(body.metadata)
        updates["metadata"] = json.dumps(existing)
    if body.showable is not None:
        updates["showable"] = 1 if body.showable else 0

    set_clause = ", ".join(f"{k} = ?" for k in updates)
    with get_db() as conn:
        conn.execute(
            f"UPDATE memories SET {set_clause} WHERE id = ?",
            list(updates.values()) + [memory_id],
        )
        if new_vec is not None:
            rowid = conn.execute(
                "SELECT rowid FROM memories WHERE id = ?", (memory_id,)
            ).fetchone()
            if rowid:
                conn.execute(
                    "UPDATE vec_memories SET embedding = ? WHERE rowid = ?",
                    (new_vec.tobytes(), rowid[0]),
                )
        updated = conn.execute(
            f"SELECT {_MEM_COLS} FROM memories WHERE id = ?", (memory_id,)
        ).fetchone()
    if updated is None:
        # Concurrent delete between auth check and update — surface as 404.
        raise HTTPException(
            404, detail={"code": "NOT_FOUND", "message": f"memory {memory_id} not found"}
        )
    return row_to_memory(updated)


@router.delete("/memories/{memory_id}", status_code=204)
async def delete_memory(
    memory_id: str,
    x_api_key: Optional[str] = Header(None, alias="X-API-Key"),
):
    with get_db() as conn:
        row = conn.execute(
            "SELECT project, author_key_id FROM memories WHERE id = ?", (memory_id,)
        ).fetchone()
    if not row:
        raise HTTPException(
            404, detail={"code": "NOT_FOUND", "message": f"memory {memory_id} not found"}
        )
    auth = check_auth(x_api_key, row["project"], "writer")
    if not _can_modify(row, auth):
        raise HTTPException(
            403,
            detail={"code": "FORBIDDEN", "message": "only the author or an admin may delete this memory"},
        )
    with get_db() as conn:
        rowid = conn.execute(
            "SELECT rowid FROM memories WHERE id = ?", (memory_id,)
        ).fetchone()
        conn.execute("DELETE FROM memories WHERE id = ?", (memory_id,))
        if rowid:
            conn.execute("DELETE FROM vec_memories WHERE rowid = ?", (rowid[0],))


@router.put("/memories/{memory_id}/deprecate", response_model=MemoryResponse)
async def deprecate_memory(
    memory_id: str,
    body: DeprecateRequest,
    x_api_key: Optional[str] = Header(None, alias="X-API-Key"),
):
    with get_db() as conn:
        row = conn.execute(
            "SELECT project, author_key_id FROM memories WHERE id = ?", (memory_id,)
        ).fetchone()
    if not row:
        raise HTTPException(
            404, detail={"code": "NOT_FOUND", "message": f"memory {memory_id} not found"}
        )
    auth = check_auth(x_api_key, row["project"], "writer")
    if not _can_modify(row, auth):
        raise HTTPException(
            403,
            detail={"code": "FORBIDDEN", "message": "only the author or an admin may deprecate this memory"},
        )
    now = _now()
    with get_db() as conn:
        conn.execute(
            "UPDATE memories SET deprecated=1, deprecated_reason=?, superseded_by=?, updated_at=? WHERE id=?",
            (body.reason, body.superseded_by, now, memory_id),
        )
        updated = conn.execute(
            f"SELECT {_MEM_COLS} FROM memories WHERE id = ?", (memory_id,)
        ).fetchone()
    if updated is None:
        # Concurrent delete between auth check and update — surface as 404.
        raise HTTPException(
            404, detail={"code": "NOT_FOUND", "message": f"memory {memory_id} not found"}
        )
    return row_to_memory(updated)
