from typing import Optional

from fastapi import APIRouter, Header, HTTPException, Query
from ulid import ULID

from auth import check_auth, hash_key
from db import get_db
from models import AccessCreate, AccessListResponse, AccessResponse

router = APIRouter()

_VALID_ROLES = {"reader", "writer", "admin"}


@router.post("/admin/access", status_code=201, response_model=AccessResponse)
async def create_access(
    body: AccessCreate,
    x_api_key: Optional[str] = Header(None, alias="X-API-Key"),
):
    check_auth(x_api_key, "*", "admin")
    if body.role not in _VALID_ROLES:
        raise HTTPException(
            400,
            detail={"code": "INVALID_REQUEST", "message": f"role must be one of {_VALID_ROLES}"},
        )
    # spec: admin scope = role=admin AND project='*' (no project-scoped admin,
    # no global writer/reader)
    if body.project == "*" and body.role != "admin":
        raise HTTPException(
            400,
            detail={
                "code": "INVALID_REQUEST",
                "message": "project='*' is reserved for role='admin'",
            },
        )
    if body.role == "admin" and body.project != "*":
        raise HTTPException(
            400,
            detail={
                "code": "INVALID_REQUEST",
                "message": "role='admin' requires project='*' (project-scoped admin not supported)",
            },
        )

    key_hash = hash_key(body.api_key)
    key_hint = body.api_key[:8]
    new_key_id = str(ULID())

    # Atomic upsert: INSERT preserves existing key_id and key_hint on conflict
    with get_db() as conn:
        conn.execute(
            """INSERT INTO access (key_id, key_hash, key_hint, project, role)
               VALUES (?,?,?,?,?)
               ON CONFLICT(key_hash, project) DO UPDATE SET role = excluded.role""",
            (new_key_id, key_hash, key_hint, body.project, body.role),
        )
        row = conn.execute(
            "SELECT key_id, key_hint, project, role FROM access WHERE key_hash = ? AND project = ?",
            (key_hash, body.project),
        ).fetchone()

    return AccessResponse(
        key_id=row["key_id"], key_hint=row["key_hint"], project=row["project"], role=row["role"]
    )


@router.get("/admin/access", response_model=AccessListResponse)
async def list_access(
    project: str = Query(...),
    x_api_key: Optional[str] = Header(None, alias="X-API-Key"),
):
    check_auth(x_api_key, "*", "admin")
    with get_db() as conn:
        rows = conn.execute(
            "SELECT key_id, key_hint, project, role FROM access WHERE project = ?",
            (project,),
        ).fetchall()
    return AccessListResponse(
        entries=[
            AccessResponse(
                key_id=r["key_id"],
                key_hint=r["key_hint"],
                project=r["project"],
                role=r["role"],
            )
            for r in rows
        ]
    )


@router.delete("/admin/access/{key_id}/{project}", status_code=204)
async def delete_access(
    key_id: str,
    project: str,
    x_api_key: Optional[str] = Header(None, alias="X-API-Key"),
):
    check_auth(x_api_key, "*", "admin")
    with get_db() as conn:
        result = conn.execute(
            "DELETE FROM access WHERE key_id = ? AND project = ?", (key_id, project)
        )
        if result.rowcount == 0:
            raise HTTPException(
                404,
                detail={
                    "code": "NOT_FOUND",
                    "message": f"access entry {key_id}/{project} not found",
                },
            )
