import pytest


def test_missing_key_returns_401(client, project):
    resp = client.get("/memories", params={"project": project})
    assert resp.status_code == 401
    assert resp.json()["error"]["code"] == "UNAUTHORIZED"


def test_invalid_key_returns_401(client, project):
    resp = client.get(
        "/memories", params={"project": project}, headers={"X-API-Key": "bad-key"}
    )
    assert resp.status_code == 401


def test_reader_cannot_write(client, project, reader_key):
    resp = client.post(
        "/memories",
        json={"project": project, "type": "note", "content": "x"},
        headers={"X-API-Key": reader_key},
    )
    assert resp.status_code == 403
    assert resp.json()["error"]["code"] == "FORBIDDEN"


def test_reader_can_read(client, project, reader_key):
    resp = client.get(
        "/memories", params={"project": project}, headers={"X-API-Key": reader_key}
    )
    assert resp.status_code == 200


def test_wrong_project_returns_403(client, writer_key):
    resp = client.get(
        "/memories",
        params={"project": "other-proj"},
        headers={"X-API-Key": writer_key},
    )
    assert resp.status_code == 403


def test_admin_can_list_access(client, project, admin_key, writer_key, reader_key):
    resp = client.get(
        "/admin/access",
        params={"project": project},
        headers={"X-API-Key": admin_key},
    )
    assert resp.status_code == 200
    keys = {e["key_hint"] for e in resp.json()["entries"]}
    assert writer_key[:8] in keys
    assert reader_key[:8] in keys


def test_non_admin_cannot_access_admin_endpoint(client, project, writer_key):
    resp = client.get(
        "/admin/access",
        params={"project": project},
        headers={"X-API-Key": writer_key},
    )
    assert resp.status_code == 403


def test_create_access_rejects_wildcard_with_non_admin_role(client, admin_key):
    resp = client.post(
        "/admin/access",
        json={"api_key": "global-writer-attempt", "project": "*", "role": "writer"},
        headers={"X-API-Key": admin_key},
    )
    assert resp.status_code == 400
    assert resp.json()["error"]["code"] == "INVALID_REQUEST"


def test_create_access_rejects_admin_with_project_scope(client, project, admin_key):
    resp = client.post(
        "/admin/access",
        json={"api_key": "scoped-admin-attempt", "project": project, "role": "admin"},
        headers={"X-API-Key": admin_key},
    )
    assert resp.status_code == 400
    assert resp.json()["error"]["code"] == "INVALID_REQUEST"


def test_create_access_upsert_preserves_key_id(client, project, admin_key):
    # First insert
    r1 = client.post(
        "/admin/access",
        json={"api_key": "upsert-test-key-padded", "project": project, "role": "reader"},
        headers={"X-API-Key": admin_key},
    )
    assert r1.status_code == 201
    key_id_1 = r1.json()["key_id"]

    # Re-upsert with new role — same key_id, new role
    r2 = client.post(
        "/admin/access",
        json={"api_key": "upsert-test-key-padded", "project": project, "role": "writer"},
        headers={"X-API-Key": admin_key},
    )
    assert r2.status_code == 201
    assert r2.json()["key_id"] == key_id_1
    assert r2.json()["role"] == "writer"


def test_hash_secret_validation_rejects_short_secret(monkeypatch):
    from auth import validate_hash_secret
    import auth as _auth

    monkeypatch.setattr(_auth, "HASH_SECRET", b"too-short")
    with pytest.raises(RuntimeError, match="HASH_SECRET"):
        validate_hash_secret()


def test_create_access_rejects_short_api_key(client, project, admin_key):
    # Short keys would leak fully via key_hint = api_key[:8]
    resp = client.post(
        "/admin/access",
        json={"api_key": "shortkey", "project": project, "role": "reader"},
        headers={"X-API-Key": admin_key},
    )
    assert resp.status_code == 422
    assert resp.json()["error"]["code"] == "VALIDATION_ERROR"
