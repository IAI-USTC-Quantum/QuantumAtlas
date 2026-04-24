from fastapi.testclient import TestClient

from atlas.server.config import ServerConfig
from atlas.server.main import create_app


def test_ingest_queue_logs_requester_header(tmp_path, monkeypatch):
    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(tmp_path / "raw"),
        data_dir=str(tmp_path / "data"),
        user_header="X-Token-User-Name",
    )
    monkeypatch.setattr("atlas.server.routers.api.execute_ingest", lambda *args, **kwargs: None)

    app = create_app(config)

    with TestClient(app) as client:
        response = client.post(
            "/api/ingest/paper",
            json={"arxiv_id": "9508027", "extract": False, "sync_neo4j": False},
            headers={"X-Token-User-Name": "alice"},
        )

        assert response.status_code == 202
        task_id = response.json()["task_id"]
        task = app.state.ingest_store.get(task_id)
        assert task is not None
        assert task.requester == "alice"

    log_text = (tmp_path / "wiki" / "log.md").read_text(encoding="utf-8")
    assert f"[INGEST] 9508027 queued by alice (task {task_id})" in log_text


def test_ingest_queue_logs_anonymous_without_requester_header(tmp_path, monkeypatch):
    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(tmp_path / "raw"),
        data_dir=str(tmp_path / "data"),
    )
    monkeypatch.setattr("atlas.server.routers.api.execute_ingest", lambda *args, **kwargs: None)

    app = create_app(config)

    with TestClient(app) as client:
        response = client.post(
            "/api/ingest/paper",
            json={"arxiv_id": "9508027", "extract": False, "sync_neo4j": False},
        )

        assert response.status_code == 202
        task_id = response.json()["task_id"]
        task = app.state.ingest_store.get(task_id)
        assert task is not None
        assert task.requester is None

    log_text = (tmp_path / "wiki" / "log.md").read_text(encoding="utf-8")
    assert f"[INGEST] 9508027 queued by anonymous (task {task_id})" in log_text
