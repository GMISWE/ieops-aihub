def test_health_no_auth_required(client):
    resp = client.get("/health")
    assert resp.status_code == 200


def test_health_payload_shape(client):
    data = client.get("/health").json()
    assert data["status"] == "ok"
    assert data["db"] == "connected"
    assert data["model"] == "loaded"
    assert data["version"] == "0.1.0"
