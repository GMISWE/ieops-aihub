"""server 侧 ErrorCode + envelope。"""
from app.errors import ErrorCode, AihubServerError, envelope_response


def test_error_codes_match_client_enum_values():
    """server enum 值必须跟 client polyforge_v3.errors.ErrorCode 一字不差。

    G2-followup parity test:直接 import 两侧 enum 比较, 替代 hardcoded expected。
    """
    from polyforge_v3.aihub.errors import ErrorCode as ClientErrorCode
    server = {c.value for c in ErrorCode}
    client = {c.value for c in ClientErrorCode}
    assert server == client, f"client↔server ErrorCode drift: missing={client - server} extra={server - client}"
    # Sanity: 14 codes after G2-re r3 (+UNKNOWN_REMOTE_ERROR forward-compat)
    assert len(server) == 14, f"expected 14 ErrorCode values, got {len(server)}"


def test_unauthorized_returns_401():
    e = AihubServerError(ErrorCode.UNAUTHORIZED, "x")
    assert e.status_code == 401
    assert e.detail["code"] == "UNAUTHORIZED"


def test_epoch_mismatch_returns_409():
    e = AihubServerError(ErrorCode.CONFLICT_EPOCH_MISMATCH, "stale")
    assert e.status_code == 409


def test_envelope_response_shape():
    r = envelope_response(ErrorCode.FORBIDDEN, "no perm", details={"hint": "ask admin"})
    assert r.status_code == 403
    body = r.body.decode()
    assert "FORBIDDEN" in body
    assert "no perm" in body
