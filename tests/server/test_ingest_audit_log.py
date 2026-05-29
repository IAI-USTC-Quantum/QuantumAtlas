from fastapi.testclient import TestClient

from qatlas.server.config import ServerConfig
from qatlas.server.main import create_app


def test_ingest_queue_records_requester_header_without_wiki_log(tmp_path, monkeypatch):
    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(tmp_path / "raw"),
        data_dir=str(tmp_path / "data"),
        user_header="X-Token-User-Name",
    )
    monkeypatch.setattr("qatlas.server.routers.api.execute_ingest", lambda *args, **kwargs: None)

    app = create_app(config)

    with TestClient(app) as client:
        response = client.post(
            "/api/ingest/paper",
            json={"arxiv_id": "9508027", "parser": "pymupdf"},
            headers={"X-Token-User-Name": "alice"},
        )

        assert response.status_code == 202
        task_id = response.json()["task_id"]
        task = app.state.ingest_store.get(task_id)
        assert task is not None
        assert task.requester == "alice"

    assert not (tmp_path / "wiki").exists()


def test_ingest_queue_records_anonymous_without_wiki_log(tmp_path, monkeypatch):
    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(tmp_path / "raw"),
        data_dir=str(tmp_path / "data"),
    )
    monkeypatch.setattr("qatlas.server.routers.api.execute_ingest", lambda *args, **kwargs: None)

    app = create_app(config)

    with TestClient(app) as client:
        response = client.post(
            "/api/ingest/paper",
            json={"arxiv_id": "9508027", "parser": "pymupdf"},
        )

        assert response.status_code == 202
        task_id = response.json()["task_id"]
        task = app.state.ingest_store.get(task_id)
        assert task is not None
        assert task.requester is None

    assert not (tmp_path / "wiki").exists()

