"""server 侧 ErrorCode + envelope。"""
from app.errors import ErrorCode, AihubServerError, envelope_response


def test_error_codes_match_client_enum_values():
    """server enum 值必须跟 client polyforge_v3.errors.ErrorCode 一字不差。"""
    server = {c.value for c in ErrorCode}
    expected = {
        "UNAUTHORIZED", "FORBIDDEN",
        "CONFLICT_EPOCH_MISMATCH", "CONFLICT_LEASE_EXPIRED",
        "CONFLICT_HARD_BLOCK", "CONFLICT_BUSY_NOT_TAKEOVER_ELIGIBLE",
        "BAD_REQUEST", "NOT_FOUND", "PAYLOAD_TOO_LARGE",
        "INTERNAL_ERROR", "SERVICE_UNAVAILABLE",
    }
    assert server == expected


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
