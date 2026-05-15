def test_search_returns_results(client, writer_key, reader_key, project):
    for i in range(3):
        client.post(
            "/memories",
            json={"project": project, "type": "convention", "content": f"convention {i}"},
            headers={"X-API-Key": writer_key},
        )
    resp = client.post(
        "/memories/search",
        json={"project": project, "query": "convention", "top_k": 3},
        headers={"X-API-Key": reader_key},
    )
    assert resp.status_code == 200
    data = resp.json()
    assert "results" in data
    assert len(data["results"]) <= 3
    for r in data["results"]:
        assert "memory" in r
        assert "score" in r


def test_search_scores_descending(client, writer_key, reader_key, project):
    client.post(
        "/memories",
        json={"project": project, "type": "note", "content": "some note"},
        headers={"X-API-Key": writer_key},
    )
    resp = client.post(
        "/memories/search",
        json={"project": project, "query": "test query", "top_k": 10},
        headers={"X-API-Key": reader_key},
    )
    assert resp.status_code == 200
    scores = [r["score"] for r in resp.json()["results"]]
    assert scores == sorted(scores, reverse=True)


def test_search_excludes_deprecated(client, writer_key, reader_key, project):
    resp = client.post(
        "/memories",
        json={"project": project, "type": "note", "content": "to be deprecated"},
        headers={"X-API-Key": writer_key},
    )
    mem_id = resp.json()["id"]
    client.put(
        f"/memories/{mem_id}/deprecate",
        json={"reason": "old"},
        headers={"X-API-Key": writer_key},
    )

    resp2 = client.post(
        "/memories/search",
        json={"project": project, "query": "deprecated", "top_k": 50},
        headers={"X-API-Key": reader_key},
    )
    assert resp2.status_code == 200
    ids = [r["memory"]["id"] for r in resp2.json()["results"]]
    assert mem_id not in ids


def test_search_empty_project(client, reader_key):
    resp = client.post(
        "/memories/search",
        json={"project": "empty-proj-xyz", "query": "anything", "top_k": 5},
        headers={"X-API-Key": reader_key},
    )
    # reader_key is scoped to test-proj; empty-proj-xyz should 403
    assert resp.status_code == 403


def test_search_recency_boost_zero(client, writer_key, reader_key, project):
    # Just verify it returns 200 and doesn't crash
    resp = client.post(
        "/memories/search",
        json={"project": project, "query": "test", "top_k": 5, "recency_boost": 0.0},
        headers={"X-API-Key": reader_key},
    )
    assert resp.status_code == 200


def test_search_rejects_top_k_zero(client, reader_key, project):
    resp = client.post(
        "/memories/search",
        json={"project": project, "query": "x", "top_k": 0},
        headers={"X-API-Key": reader_key},
    )
    assert resp.status_code == 422


def test_search_rejects_top_k_negative(client, reader_key, project):
    resp = client.post(
        "/memories/search",
        json={"project": project, "query": "x", "top_k": -1},
        headers={"X-API-Key": reader_key},
    )
    assert resp.status_code == 422
