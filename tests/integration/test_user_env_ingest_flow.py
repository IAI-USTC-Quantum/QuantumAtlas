"""
Live ingest smoke test against the repository .env.

This test loads the project's local ``.env`` (or GitHub-injected env in CI),
boots the server in-process, and exercises the **fetch + parse** stages
(the only stages the server runs under the ff-only architecture). It hits
``export.arxiv.org`` and optionally MinerU, so it is gated on the ``e2e``
+ ``network`` + ``slow`` markers.

Run locally:
    uv run pytest -m e2e tests/integration/test_user_env_ingest_flow.py
"""

import socket
import threading
import time

import pytest
import requests
import uvicorn

from atlas.server.config import ServerConfig, get_project_root
from atlas.server.main import create_app

pytestmark = [
    pytest.mark.e2e,
    pytest.mark.network,
    pytest.mark.slow,
    pytest.mark.legacy,
]

TEST_ARXIV_ID = "quant-ph/9508027v1"
REQUIRED_DOTENV_KEYS: list[str] = []  # fetch+parse only needs MINERU for the mineru path
CONFIG_ENV_KEYS = [
    "SERVER_HOST",
    "SERVER_PORT",
    "SERVER_DEBUG",
    "WIKI_DIR",
    "RAW_DIR",
    "DATA_DIR",
    "PUBLIC_BASE_URL",
    "SHARE_ACCESS_TOKEN",
    "DEFAULT_SHARE_EXPIRES_IN",
    "USER_HEADER",
    "MINERU_API_TOKEN",
    "MINERU_API_BASE_URL",
    "MINERU_MODEL_VERSION",
    "MINERU_LANGUAGE",
    "MINERU_IS_OCR",
    "MINERU_ENABLE_FORMULA",
    "MINERU_ENABLE_TABLE",
    "MINERU_POLL_INTERVAL",
    "MINERU_TIMEOUT",
]


def _pick_free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def _port_available(port: int) -> bool:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        try:
            sock.bind(("127.0.0.1", port))
        except OSError:
            return False
        return True


def _wait_for_health(base_url: str, timeout: float = 20.0) -> None:
    deadline = time.time() + timeout
    last_error = None
    while time.time() < deadline:
        try:
            response = requests.get(f"{base_url}/health", timeout=2)
            if response.status_code == 200:
                return
        except requests.RequestException as exc:
            last_error = exc
        time.sleep(0.2)
    raise RuntimeError(f"server did not become healthy: {last_error}")


def _poll_ingest(base_url: str, task_id: str, timeout: float = 600.0) -> dict:
    deadline = time.time() + timeout
    last_payload = None
    while time.time() < deadline:
        response = requests.get(f"{base_url}/api/ingest/{task_id}", timeout=20)
        response.raise_for_status()
        payload = response.json()
        last_payload = payload
        if payload.get("status") not in {"queued", "running", "pending"}:
            return payload
        time.sleep(2.0)
    raise TimeoutError(f"timed out waiting for ingest task {task_id}: {last_payload}")


def _load_project_dotenv(
    monkeypatch,
    *,
    required_keys: list[str] | None = None,
) -> ServerConfig:
    required_keys = REQUIRED_DOTENV_KEYS if required_keys is None else required_keys
    env_path = get_project_root() / ".env"
    if not env_path.is_file():
        pytest.skip(f"repository .env not found: {env_path}")

    dotenv_text = env_path.read_text(encoding="utf-8")
    if not any(line.strip() and not line.lstrip().startswith("#") for line in dotenv_text.splitlines()):
        pytest.skip(f"repository .env is empty: {env_path}")

    for key in CONFIG_ENV_KEYS:
        monkeypatch.delenv(key, raising=False)
    monkeypatch.delenv("QATLAS_SKIP_DOTENV", raising=False)
    monkeypatch.delenv("QUANTUMATLAS_SKIP_DOTENV", raising=False)

    config = ServerConfig.from_env()
    config_values = {
        "MINERU_API_TOKEN": config.mineru_api_token,
    }

    missing = [key for key in required_keys if not config_values.get(key)]
    if missing:
        pytest.skip(f"repository .env is missing required ingest service key(s): {missing}")

    return config


@pytest.fixture
def user_env_live_server(tmp_path, monkeypatch):
    env_config = _load_project_dotenv(monkeypatch)
    port = _pick_free_port()
    base_url = f"http://127.0.0.1:{port}"
    config = env_config.model_copy(
        update={
            "host": "127.0.0.1",
            "port": port,
            "wiki_dir": str(tmp_path / "wiki"),
            "raw_dir": str(tmp_path / "raw"),
            "data_dir": str(tmp_path / "data"),
            "public_base_url": base_url,
        }
    )

    app = create_app(config)
    server = uvicorn.Server(uvicorn.Config(app, host="127.0.0.1", port=port, log_level="warning"))
    thread = threading.Thread(target=server.run, daemon=True)
    thread.start()
    _wait_for_health(base_url)
    try:
        yield base_url
    finally:
        server.should_exit = True
        thread.join(timeout=10)


@pytest.fixture
def user_env_public_base_server(tmp_path, monkeypatch):
    env_config = _load_project_dotenv(monkeypatch, required_keys=["MINERU_API_TOKEN"])
    if not env_config.get_public_base_url():
        pytest.skip("PUBLIC_BASE_URL must be set for MinerU")
    if not _port_available(env_config.port):
        pytest.skip(f"SERVER_PORT={env_config.port} is already in use")

    base_url = f"http://127.0.0.1:{env_config.port}"
    config = env_config.model_copy(
        update={
            "host": "127.0.0.1",
            "wiki_dir": str(tmp_path / "wiki"),
            "raw_dir": str(tmp_path / "raw"),
            "data_dir": str(tmp_path / "data"),
        }
    )

    app = create_app(config)
    server = uvicorn.Server(
        uvicorn.Config(app, host="127.0.0.1", port=env_config.port, log_level="warning")
    )
    thread = threading.Thread(target=server.run, daemon=True)
    thread.start()
    _wait_for_health(base_url)
    try:
        yield base_url
    finally:
        server.should_exit = True
        thread.join(timeout=10)


def test_user_dotenv_fetch_and_parse_complete(user_env_live_server):
    response = requests.post(
        f"{user_env_live_server}/api/ingest/paper",
        json={"arxiv_id": TEST_ARXIV_ID, "parser": "pymupdf"},
        timeout=30,
    )
    response.raise_for_status()

    task = _poll_ingest(user_env_live_server, response.json()["task_id"])

    assert task["status"] == "succeeded", task
    assert task["message"] == "ingest succeeded"
    assert task["steps"]["fetch"]["status"] == "succeeded"
    assert task["steps"]["parse"]["status"] == "succeeded"
    assert task["steps"]["fetch"]["progress"]["percent"] == 1.0
    assert task["steps"]["parse"]["progress"]["percent"] == 1.0
    # ff-only: server must NOT produce extract/wiki/neo4j steps
    assert "extract" not in task["steps"]
    assert "wiki" not in task["steps"]
    assert "neo4j" not in task["steps"]


def test_user_dotenv_can_resume_existing_pdf_with_mineru(user_env_public_base_server):
    fetch_response = requests.post(
        f"{user_env_public_base_server}/api/ingest/paper",
        json={
            "arxiv_id": TEST_ARXIV_ID,
            "parser": "pymupdf",
            "stop_after": "fetch",
        },
        timeout=30,
    )
    fetch_response.raise_for_status()
    fetch_task = _poll_ingest(
        user_env_public_base_server,
        fetch_response.json()["task_id"],
        timeout=300,
    )
    assert fetch_task["status"] == "succeeded", fetch_task
    assert fetch_task["steps"]["fetch"]["status"] == "succeeded"

    mineru_response = requests.post(
        f"{user_env_public_base_server}/api/ingest/paper",
        json={
            "arxiv_id": TEST_ARXIV_ID,
            "fetch": False,
            "parser": "mineru",
            "mineru_no_cache": True,
        },
        timeout=30,
    )
    mineru_response.raise_for_status()
    mineru_task = _poll_ingest(
        user_env_public_base_server,
        mineru_response.json()["task_id"],
        timeout=1200,
    )

    assert mineru_task["status"] == "succeeded", mineru_task
    assert mineru_task["steps"]["fetch"]["status"] == "skipped"
    assert mineru_task["steps"]["parse"]["status"] == "succeeded"
    assert mineru_task["steps"]["parse"]["progress"]["parser"] == "mineru"
    assert mineru_task["steps"]["parse"]["progress"]["state"] == "done"

