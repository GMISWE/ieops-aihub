"""Event emission helper — shared by attempt / lock / work_item routes.

Wraps INSERT into agent_events with size validation per §20 M5 (64 KiB cap).
"""
from __future__ import annotations

import json
from typing import Any

import sqlalchemy as sa
from sqlalchemy.ext.asyncio import AsyncConnection
from ulid import ULID

from app.errors import AihubServerError, ErrorCode


PAYLOAD_MAX_BYTES = 65536  # §20 M5


def _evt_id() -> str:
    return "evt_" + str(ULID()).lower()


def _payload_too_large(payload: dict[str, Any]) -> bool:
    return len(json.dumps(payload).encode("utf-8")) > PAYLOAD_MAX_BYTES


async def emit_event(
    conn: AsyncConnection,
    *,
    work_item_id: str,
    event_type: str,
    payload: dict[str, Any],
    actor_user_id: str,
    api_key_id: str | None = None,
    run_attempt_id: str | None = None,
    pinned: bool = False,
) -> str:
    """Insert one agent_events row. Validates payload size; 413 on breach."""
    if _payload_too_large(payload):
        raise AihubServerError(ErrorCode.PAYLOAD_TOO_LARGE,
                               f"event payload exceeds {PAYLOAD_MAX_BYTES} bytes")
    eid = _evt_id()
    await conn.execute(sa.text("""
        INSERT INTO agent_events (id, work_item_id, run_attempt_id, actor_user_id,
                                  api_key_id, event_type, payload, pinned)
        VALUES (:id, :wid, :aid, :uid, :kid, :etype, CAST(:payload AS JSONB), :pinned)
    """), {
        "id": eid, "wid": work_item_id, "aid": run_attempt_id,
        "uid": actor_user_id, "kid": api_key_id, "etype": event_type,
        "payload": json.dumps(payload), "pinned": pinned,
    })
    return eid
