def _create(client, writer_key, project, **kwargs):
    payload = {"project": project, "type": "note", "content": "hello memory", **kwargs}
    return client.post("/memories", json=payload, headers={"X-API-Key": writer_key})


def test_create_returns_201(client, writer_key, project):
    resp = _create(client, writer_key, project)
    assert resp.status_code == 201
    data = resp.json()
    assert data["id"].startswith("mem-")
    assert data["project"] == project
    assert data["deprecated"] is False
    assert data["metadata"] == {}


def test_create_with_metadata(client, writer_key, project):
    resp = _create(client, writer_key, project, metadata={"task_ref": "t-001"})
    assert resp.status_code == 201
    assert resp.json()["metadata"]["task_ref"] == "t-001"


def test_get_by_id(client, writer_key, reader_key, project):
    mem_id = _create(client, writer_key, project).json()["id"]
    resp = client.get(f"/memories/{mem_id}", headers={"X-API-Key": reader_key})
    assert resp.status_code == 200
    assert resp.json()["id"] == mem_id


def test_get_not_found(client, reader_key):
    resp = client.get("/memories/mem-nonexistent", headers={"X-API-Key": reader_key})
    assert resp.status_code == 404
    assert resp.json()["error"]["code"] == "NOT_FOUND"


def test_list_memories(client, writer_key, reader_key, project):
    _create(client, writer_key, project)
    resp = client.get("/memories", params={"project": project}, headers={"X-API-Key": reader_key})
    assert resp.status_code == 200
    data = resp.json()
    assert "memories" in data
    assert data["total"] >= 1
    assert "limit" in data and "offset" in data


def test_list_filter_by_type(client, writer_key, reader_key, project):
    client.post(
        "/memories",
        json={"project": project, "type": "spec", "content": "a spec"},
        headers={"X-API-Key": writer_key},
    )
    resp = client.get(
        "/memories", params={"project": project, "type": "spec"}, headers={"X-API-Key": reader_key}
    )
    assert resp.status_code == 200
    for m in resp.json()["memories"]:
        assert m["type"] == "spec"


def test_list_filter_by_status(client, writer_key, reader_key, project):
    client.post(
        "/memories",
        json={"project": project, "type": "task", "content": "a task", "metadata": {"status": "open"}},
        headers={"X-API-Key": writer_key},
    )
    resp = client.get(
        "/memories", params={"project": project, "status": "open"}, headers={"X-API-Key": reader_key}
    )
    assert resp.status_code == 200
    for m in resp.json()["memories"]:
        assert m["metadata"].get("status") == "open"


def test_update_merges_metadata(client, writer_key, project):
    mem_id = _create(client, writer_key, project, metadata={"task_ref": "t-001"}).json()["id"]
    resp = client.put(
        f"/memories/{mem_id}",
        json={"metadata": {"status": "done"}},
        headers={"X-API-Key": writer_key},
    )
    assert resp.status_code == 200
    meta = resp.json()["metadata"]
    assert meta["task_ref"] == "t-001"
    assert meta["status"] == "done"


def test_update_content_re_embeds(client, writer_key, project):
    mem_id = _create(client, writer_key, project).json()["id"]
    resp = client.put(
        f"/memories/{mem_id}",
        json={"content": "updated content"},
        headers={"X-API-Key": writer_key},
    )
    assert resp.status_code == 200
    assert resp.json()["content"] == "updated content"


def test_deprecate(client, writer_key, reader_key, project):
    mem_id = _create(client, writer_key, project).json()["id"]
    resp = client.put(
        f"/memories/{mem_id}/deprecate",
        json={"reason": "superseded"},
        headers={"X-API-Key": writer_key},
    )
    assert resp.status_code == 200
    data = resp.json()
    assert data["deprecated"] is True
    assert data["deprecated_reason"] == "superseded"

    # Deprecated items excluded from default list
    list_resp = client.get(
        "/memories", params={"project": project}, headers={"X-API-Key": reader_key}
    )
    ids = [m["id"] for m in list_resp.json()["memories"]]
    assert mem_id not in ids

    # Included when requested
    list_resp2 = client.get(
        "/memories",
        params={"project": project, "include_deprecated": "true"},
        headers={"X-API-Key": reader_key},
    )
    ids2 = [m["id"] for m in list_resp2.json()["memories"]]
    assert mem_id in ids2


def test_delete(client, writer_key, reader_key, project):
    mem_id = _create(client, writer_key, project).json()["id"]
    resp = client.delete(f"/memories/{mem_id}", headers={"X-API-Key": writer_key})
    assert resp.status_code == 204
    resp2 = client.get(f"/memories/{mem_id}", headers={"X-API-Key": reader_key})
    assert resp2.status_code == 404


def test_list_filter_by_external_id(client, writer_key, reader_key, project):
    # Simulate pf2-sync ingesting an external GitHub issue
    client.post(
        "/memories",
        json={
            "project": project,
            "type": "task",
            "content": "fix the gateway 502s",
            "metadata": {
                "external_ref": "https://github.com/GMISWE/ieops-v2/issues/42",
                "external_id": "42",
                "status": "open",
            },
        },
        headers={"X-API-Key": writer_key},
    )
    # pf2-sync dedup query: is there already a task for issue #42?
    resp = client.get(
        "/memories",
        params={"project": project, "external_id": "42"},
        headers={"X-API-Key": reader_key},
    )
    assert resp.status_code == 200
    data = resp.json()
    assert data["total"] >= 1
    for m in data["memories"]:
        assert m["metadata"]["external_id"] == "42"


def test_list_external_id_no_match(client, reader_key, project):
    resp = client.get(
        "/memories",
        params={"project": project, "external_id": "nonexistent-issue-9999"},
        headers={"X-API-Key": reader_key},
    )
    assert resp.status_code == 200
    assert resp.json()["total"] == 0


def test_update_after_concurrent_delete_returns_404(client, writer_key, project):
    """Simulate: another agent deletes the memory between our auth check and UPDATE."""
    mem_id = _create(client, writer_key, project).json()["id"]
    # Simulate the racing DELETE by issuing it explicitly first
    client.delete(f"/memories/{mem_id}", headers={"X-API-Key": writer_key})
    # The PUT now operates on a vanished row — must return 404, not crash 500
    resp = client.put(
        f"/memories/{mem_id}",
        json={"content": "updated after delete"},
        headers={"X-API-Key": writer_key},
    )
    assert resp.status_code == 404


def test_deprecate_after_concurrent_delete_returns_404(client, writer_key, project):
    mem_id = _create(client, writer_key, project).json()["id"]
    client.delete(f"/memories/{mem_id}", headers={"X-API-Key": writer_key})
    resp = client.put(
        f"/memories/{mem_id}/deprecate",
        json={"reason": "stale"},
        headers={"X-API-Key": writer_key},
    )
    assert resp.status_code == 404
