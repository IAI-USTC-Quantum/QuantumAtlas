from fastapi.testclient import TestClient

from atlas.server.config import ServerConfig
from atlas.server.main import create_app


def _client(tmp_path, **kwargs):
    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(tmp_path / "raw"),
        data_dir=str(tmp_path / "data"),
        **kwargs,
    )
    return TestClient(create_app(config))


def test_oauth_user_can_issue_cli_token(tmp_path):
    with _client(
        tmp_path,
        cli_token_secret="test-secret",
        user_header="X-Token-User-Name",
    ) as client:
        response = client.post(
            "/api/auth/cli-token",
            headers={"X-Token-User-Name": "github.com/example"},
        )

    assert response.status_code == 200
    data = response.json()
    assert data["token_type"] == "bearer"
    assert data["subject"] == "github.com/example"
    assert data["access_token"].startswith("qat1.")


def test_cli_token_authenticates_api_request(tmp_path):
    with _client(
        tmp_path,
        cli_token_secret="test-secret",
        user_header="X-Token-User-Name",
    ) as client:
        token = client.post(
            "/api/auth/cli-token",
            headers={"X-Token-User-Name": "github.com/example"},
        ).json()["access_token"]
        response = client.post(
            "/api/ingest/paper",
            headers={"Authorization": f"Bearer {token}"},
            json={"arxiv_id": "2401.00001", "fetch": False, "parse": False, "extract": False},
        )
        task = client.get(f"/api/ingest/{response.json()['task_id']}").json()

    assert response.status_code == 202
    assert task["requester"] == "github.com/example"


def test_invalid_cli_token_is_rejected(tmp_path):
    with _client(tmp_path, cli_token_secret="test-secret") as client:
        response = client.get(
            "/api/server/info",
            headers={"Authorization": "Bearer qat1.invalid.invalid"},
        )

    assert response.status_code == 401


def test_cli_token_issuing_requires_configured_secret(tmp_path):
    with _client(tmp_path, user_header="X-Token-User-Name") as client:
        response = client.post(
            "/api/auth/cli-token",
            headers={"X-Token-User-Name": "github.com/example"},
        )

    assert response.status_code == 503
