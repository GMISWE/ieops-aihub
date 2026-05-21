"""v0.2.0: author_key_id + showable visibility rules."""
import pytest


WRITER_B = "second-writer-key-padded-1234567890"


@pytest.fixture
def writer_b_key(client, admin_key, project):
    """A second writer key in the same project — used to verify cross-author
    modification is blocked and cross-author visibility honours `showable`.
    Registration is upsert so re-running per test is cheap."""
    r = client.post(
        "/admin/access",
        json={"api_key": WRITER_B, "project": project, "role": "writer"},
        headers={"X-API-Key": admin_key},
    )
    assert r.status_code == 201, r.text
    return WRITER_B


def _post(client, key, project, **kwargs):
    body = {"project": project, "type": "note", "content": "hi", **kwargs}
    return client.post("/memories", json=body, headers={"X-API-Key": key})


# ─────── author_key_id ───────


def test_create_populates_author_key_id(client, writer_key, project):
    r = _post(client, writer_key, project)
    assert r.status_code == 201
    data = r.json()
    assert data["author_key_id"], "expected author_key_id to be populated"
    assert data["author_key_id"].startswith("01"), "ULID format"


def test_author_can_update_own(client, writer_key, project):
    mid = _post(client, writer_key, project).json()["id"]
    r = client.put(
        f"/memories/{mid}",
        json={"metadata": {"status": "done"}},
        headers={"X-API-Key": writer_key},
    )
    assert r.status_code == 200


def test_non_author_cannot_update(client, writer_key, writer_b_key, project):
    mid = _post(client, writer_key, project).json()["id"]
    r = client.put(
        f"/memories/{mid}",
        json={"metadata": {"status": "stolen"}},
        headers={"X-API-Key": writer_b_key},
    )
    assert r.status_code == 403
    assert "author" in r.json()["error"]["message"].lower()


def test_non_author_cannot_delete(client, writer_key, writer_b_key, project):
    mid = _post(client, writer_key, project).json()["id"]
    r = client.delete(f"/memories/{mid}", headers={"X-API-Key": writer_b_key})
    assert r.status_code == 403


def test_non_author_cannot_deprecate(client, writer_key, writer_b_key, project):
    mid = _post(client, writer_key, project).json()["id"]
    r = client.put(
        f"/memories/{mid}/deprecate",
        json={"reason": "not mine to deprecate"},
        headers={"X-API-Key": writer_b_key},
    )
    assert r.status_code == 403


def test_admin_can_update_any(client, writer_key, admin_key, project):
    mid = _post(client, writer_key, project).json()["id"]
    r = client.put(
        f"/memories/{mid}",
        json={"metadata": {"admin_touched": True}},
        headers={"X-API-Key": admin_key},
    )
    assert r.status_code == 200
    assert r.json()["metadata"]["admin_touched"] is True


def test_admin_can_delete_any(client, writer_key, admin_key, project):
    mid = _post(client, writer_key, project).json()["id"]
    r = client.delete(f"/memories/{mid}", headers={"X-API-Key": admin_key})
    assert r.status_code == 204


def test_legacy_null_author_modifiable_by_any_writer(
    client, writer_key, writer_b_key, project
):
    """Memories pre-dating v0.2.0 have NULL author_key_id; any writer can modify."""
    # Author this memory normally then null the author column directly to
    # simulate pre-migration data.
    mid = _post(client, writer_key, project).json()["id"]
    import db
    with db.get_db() as conn:
        conn.execute("UPDATE memories SET author_key_id = NULL WHERE id = ?", (mid,))

    r = client.put(
        f"/memories/{mid}",
        json={"metadata": {"legacy_touched": True}},
        headers={"X-API-Key": writer_b_key},
    )
    assert r.status_code == 200


# ─────── showable ───────


def test_create_defaults_showable_true(client, writer_key, project):
    data = _post(client, writer_key, project).json()
    assert data["showable"] is True


def test_create_with_showable_false(client, writer_key, project):
    data = _post(client, writer_key, project, showable=False).json()
    assert data["showable"] is False


def test_invisible_filtered_from_others_list(
    client, writer_key, writer_b_key, project
):
    _post(client, writer_key, project, showable=False, content="secret note")
    # writer_b lists same project; should NOT see writer_key's hidden memory
    r = client.get("/memories", params={"project": project}, headers={"X-API-Key": writer_b_key})
    contents = [m["content"] for m in r.json()["memories"]]
    assert "secret note" not in contents


def test_invisible_visible_to_own_author(client, writer_key, project):
    _post(client, writer_key, project, showable=False, content="my private note")
    r = client.get(
        "/memories", params={"project": project}, headers={"X-API-Key": writer_key}
    )
    contents = [m["content"] for m in r.json()["memories"]]
    assert "my private note" in contents


def test_invisible_get_by_id_others_403(
    client, writer_key, writer_b_key, project
):
    mid = _post(client, writer_key, project, showable=False).json()["id"]
    r = client.get(f"/memories/{mid}", headers={"X-API-Key": writer_b_key})
    assert r.status_code == 403


def test_invisible_get_by_id_author_200(client, writer_key, project):
    mid = _post(client, writer_key, project, showable=False).json()["id"]
    r = client.get(f"/memories/{mid}", headers={"X-API-Key": writer_key})
    assert r.status_code == 200


def test_invisible_get_by_id_admin_200(client, writer_key, admin_key, project):
    mid = _post(client, writer_key, project, showable=False).json()["id"]
    r = client.get(f"/memories/{mid}", headers={"X-API-Key": admin_key})
    assert r.status_code == 200


def test_invisible_excluded_from_others_search(
    client, writer_key, writer_b_key, project
):
    _post(
        client, writer_key, project, showable=False,
        content="zzzqqqxxx unique invisible search target",
    )
    r = client.post(
        "/memories/search",
        json={"project": project, "query": "zzzqqqxxx unique", "top_k": 50},
        headers={"X-API-Key": writer_b_key},
    )
    ids_or_contents = [res["memory"]["content"] for res in r.json()["results"]]
    assert not any("zzzqqqxxx" in c for c in ids_or_contents)


def test_invisible_included_in_own_search(client, writer_key, project):
    _post(
        client, writer_key, project, showable=False,
        content="yyywwwvvv unique invisible target",
    )
    r = client.post(
        "/memories/search",
        json={"project": project, "query": "yyywwwvvv unique", "top_k": 50},
        headers={"X-API-Key": writer_key},
    )
    contents = [res["memory"]["content"] for res in r.json()["results"]]
    assert any("yyywwwvvv" in c for c in contents)


def test_admin_search_sees_invisible(client, writer_key, admin_key, project):
    _post(
        client, writer_key, project, showable=False,
        content="ppprrrssss admin-visible secret",
    )
    r = client.post(
        "/memories/search",
        json={"project": project, "query": "ppprrrssss admin-visible", "top_k": 50},
        headers={"X-API-Key": admin_key},
    )
    contents = [res["memory"]["content"] for res in r.json()["results"]]
    assert any("ppprrrssss" in c for c in contents)


def test_put_can_flip_showable(client, writer_key, writer_b_key, project):
    # Start visible, flip to invisible
    mid = _post(client, writer_key, project, showable=True).json()["id"]
    r = client.put(
        f"/memories/{mid}",
        json={"showable": False},
        headers={"X-API-Key": writer_key},
    )
    assert r.status_code == 200
    assert r.json()["showable"] is False
    # other writer now blocked from GET
    assert client.get(f"/memories/{mid}", headers={"X-API-Key": writer_b_key}).status_code == 403
