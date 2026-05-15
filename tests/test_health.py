from importlib.metadata import version as _pkg_version


def test_health_no_auth_required(client):
    resp = client.get("/health")
    assert resp.status_code == 200


def test_health_payload_shape(client):
    data = client.get("/health").json()
    assert data["status"] == "ok"
    assert data["db"] == "connected"
    assert data["model"] == "loaded"
    # Regression guard for v0.2.1: /health must report installed package
    # version, not a hardcoded string.
    assert data["version"] == _pkg_version("ieops-mem")
