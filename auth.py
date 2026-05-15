import hashlib
import hmac
import os
from fastapi import HTTPException
from db import get_db

HASH_SECRET = os.getenv("HASH_SECRET", "").encode()
MIN_HASH_SECRET_BYTES = 32

ROLE_RANK = {"reader": 0, "writer": 1, "admin": 2}


def validate_hash_secret() -> None:
    if len(HASH_SECRET) < MIN_HASH_SECRET_BYTES:
        raise RuntimeError(
            f"HASH_SECRET must be at least {MIN_HASH_SECRET_BYTES} bytes "
            f"(got {len(HASH_SECRET)}). Set the HASH_SECRET env var to a "
            "cryptographically random value (e.g. `openssl rand -hex 32`)."
        )


def hash_key(api_key: str) -> str:
    return hmac.new(HASH_SECRET, api_key.encode(), hashlib.sha256).hexdigest()


def check_auth(api_key: str | None, project: str, min_role: str = "reader") -> dict:
    """Authenticate + authorize. Returns {"key_id": str, "role": str} so callers
    can record authorship on writes and gate own-vs-other modifications."""
    if not api_key:
        raise HTTPException(
            status_code=401,
            detail={"code": "UNAUTHORIZED", "message": "X-API-Key header is required"},
        )
    key_hash = hash_key(api_key)
    with get_db() as conn:
        exists = conn.execute(
            "SELECT COUNT(*) FROM access WHERE key_hash = ?", (key_hash,)
        ).fetchone()[0]
        if not exists:
            raise HTTPException(
                status_code=401,
                detail={"code": "UNAUTHORIZED", "message": "invalid API key"},
            )
        row = conn.execute(
            "SELECT key_id, role FROM access WHERE key_hash = ? AND (project = ? OR project = '*')",
            (key_hash, project),
        ).fetchone()

    if row is None:
        raise HTTPException(
            status_code=403,
            detail={"code": "FORBIDDEN", "message": "key not authorized for this project"},
        )
    if ROLE_RANK.get(row["role"], -1) < ROLE_RANK.get(min_role, 999):
        raise HTTPException(
            status_code=403,
            detail={"code": "FORBIDDEN", "message": f"requires {min_role} role"},
        )
    return {"key_id": row["key_id"], "role": row["role"]}


def bootstrap(admin_api_key: str | None) -> None:
    if not admin_api_key:
        return
    with get_db() as conn:
        count = conn.execute("SELECT COUNT(*) FROM access").fetchone()[0]
        if count == 0:
            from ulid import ULID
            key_id = str(ULID())
            key_hash = hash_key(admin_api_key)
            key_hint = admin_api_key[:8]
            conn.execute(
                "INSERT INTO access (key_id, key_hash, key_hint, project, role) VALUES (?,?,?,?,?)",
                (key_id, key_hash, key_hint, "*", "admin"),
            )
