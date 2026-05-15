import heapq
import json
import math
import os
from datetime import datetime, timedelta, timezone
from typing import Optional

import numpy as np
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

DECAY_LAMBDA = float(os.getenv("DECAY_LAMBDA", "0.005"))

# Column list excluding the 1.5KB embedding BLOB — used by all non-search reads.
_MEM_COLS = (
    "id, project, type, content, metadata, created_at, updated_at, "
    "expires_at, deprecated, deprecated_reason, superseded_by, "
    "author_key_id, showable"
)


def _now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _expires_at(ttl_days: Optional[int]) -> Optional[str]:
    if ttl_days is None:
        return None
    return (datetime.now(timezone.utc) + timedelta(days=ttl_days)).strftime(
        "%Y-%m-%dT%H:%M:%SZ"
    )


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
    vec = await _embedder.embed(body.content)
    now = _now()
    mem_id = f"mem-{ULID()}"

    with get_db() as conn:
        conn.execute(
            """INSERT INTO memories
               (id, project, type, content, metadata, embedding,
                created_at, updated_at, expires_at, author_key_id, showable)
               VALUES (?,?,?,?,?,?,?,?,?,?,?)""",
            (
                mem_id,
                body.project,
                body.type,
                body.content,
                json.dumps(body.metadata),
                vec.tobytes(),
                now,
                now,
                _expires_at(body.ttl_days),
                auth["key_id"],
                1 if body.showable else 0,
            ),
        )
        row = conn.execute(
            f"SELECT {_MEM_COLS} FROM memories WHERE id = ?", (mem_id,)
        ).fetchone()
    return row_to_memory(row)


@router.post("/memories/search", response_model=SearchResponse)
async def search_memories(
    body: SearchRequest,
    x_api_key: Optional[str] = Header(None, alias="X-API-Key"),
):
    auth = check_auth(x_api_key, body.project, "reader")
    query_vec = await _embedder.embed(body.query)
    now = _now()

    with get_db() as conn:
        # Search needs the embedding column — keep SELECT *.
        # Visibility filter pushed to SQL for efficiency at scale.
        if auth["role"] == "admin":
            visibility_clause = ""
            visibility_params: list = []
        else:
            visibility_clause = " AND (showable = 1 OR author_key_id IS NULL OR author_key_id = ?)"
            visibility_params = [auth["key_id"]]
        rows = conn.execute(
            f"""SELECT * FROM memories
               WHERE project = ? AND deprecated = 0
               AND (expires_at IS NULL OR expires_at > ?){visibility_clause}""",
            [body.project, now] + visibility_params,
        ).fetchall()

    if not rows:
        return SearchResponse(results=[])

    embeddings, valid_rows = [], []
    for row in rows:
        if row["embedding"]:
            embeddings.append(np.frombuffer(row["embedding"], dtype=np.float32))
            valid_rows.append(row)

    if not embeddings:
        return SearchResponse(results=[])

    mat = np.stack(embeddings)
    cosine = mat @ query_vec  # dot product == cosine (fastembed outputs L2-normalized)

    now_dt = datetime.now(timezone.utc)
    scores = []
    for i, row in enumerate(valid_rows):
        created = datetime.fromisoformat(row["created_at"].replace("Z", "+00:00"))
        days = max((now_dt - created).days, 0)
        recency = math.exp(-DECAY_LAMBDA * days)
        scores.append(float(cosine[i]) + body.recency_boost * recency)

    # O(N log k) partial sort; faster than full sorted() when N >> k.
    top_idx = heapq.nlargest(body.top_k, range(len(scores)), key=lambda i: scores[i])
    return SearchResponse(
        results=[
            SearchResult(memory=row_to_memory(valid_rows[i]), score=scores[i])
            for i in top_idx
        ]
    )


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
        cutoff = (datetime.now(timezone.utc) - timedelta(days=max_age_days)).strftime(
            "%Y-%m-%dT%H:%M:%SZ"
        )
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
    if body.content is not None:
        vec = await _embedder.embed(body.content)
        updates["content"] = body.content
        updates["embedding"] = vec.tobytes()
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
        conn.execute("DELETE FROM memories WHERE id = ?", (memory_id,))


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
