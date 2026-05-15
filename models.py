import json
import sqlite3
from typing import Any, Optional
from pydantic import BaseModel, Field


class MemoryCreate(BaseModel):
    project: str
    type: str
    content: str
    metadata: dict[str, Any] = Field(default_factory=dict)
    ttl_days: Optional[int] = None
    showable: bool = True


class MemoryUpdate(BaseModel):
    content: Optional[str] = None
    metadata: Optional[dict[str, Any]] = None
    showable: Optional[bool] = None


class DeprecateRequest(BaseModel):
    reason: str
    superseded_by: Optional[str] = None


class MemoryResponse(BaseModel):
    id: str
    project: str
    type: str
    content: str
    metadata: dict[str, Any]
    created_at: str
    updated_at: str
    expires_at: Optional[str]
    deprecated: bool
    deprecated_reason: Optional[str]
    superseded_by: Optional[str]
    author_key_id: Optional[str]
    showable: bool


class SearchRequest(BaseModel):
    project: str
    query: str
    top_k: int = Field(5, ge=1, le=200)
    recency_boost: float = 0.1


class SearchResult(BaseModel):
    memory: MemoryResponse
    score: float


class SearchResponse(BaseModel):
    results: list[SearchResult]


class AccessCreate(BaseModel):
    # min_length=16 ensures key_hint (first 8 chars) cannot leak the full key.
    api_key: str = Field(..., min_length=16)
    project: str
    role: str


class AccessResponse(BaseModel):
    key_id: str
    key_hint: str
    project: str
    role: str


class AccessListResponse(BaseModel):
    entries: list[AccessResponse]


class MemoryListResponse(BaseModel):
    memories: list[MemoryResponse]
    total: int
    limit: int
    offset: int


def row_to_memory(row: sqlite3.Row) -> MemoryResponse:
    return MemoryResponse(
        id=row["id"],
        project=row["project"],
        type=row["type"],
        content=row["content"],
        metadata=json.loads(row["metadata"]) if row["metadata"] else {},
        created_at=row["created_at"],
        updated_at=row["updated_at"],
        expires_at=row["expires_at"],
        deprecated=bool(row["deprecated"]),
        deprecated_reason=row["deprecated_reason"],
        superseded_by=row["superseded_by"],
        author_key_id=row["author_key_id"],
        showable=bool(row["showable"]),
    )
