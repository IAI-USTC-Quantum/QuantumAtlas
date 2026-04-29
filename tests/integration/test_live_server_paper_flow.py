import socket
import threading
import time
from urllib.parse import urlparse

import pytest
import requests
import uvicorn

from atlas.server.config import ServerConfig
from atlas.server.main import create_app


TEST_PAPERS = [
    (
        "quant-ph/9508027v1",
        "paper-arxiv-quant-ph-9508027v1",
    ),
    (
        "2401.00001v1",
        "paper-arxiv-2401.00001v1",
    ),
]


def _pick_free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


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


def _poll_json(url: str, *, key: str = "status", timeout: float = 180.0) -> dict:
    deadline = time.time() + timeout
    last_payload = None
    while time.time() < deadline:
        response = requests.get(url, timeout=10)
        response.raise_for_status()
        payload = response.json()
        last_payload = payload
        if payload.get(key) not in {"queued", "running", "pending"}:
            return payload
        time.sleep(1.0)
    raise TimeoutError(f"timed out waiting for terminal status from {url}: {last_payload}")


def _skip_if_network_issue(task: dict) -> None:
    errors = []
    for step in task.get("steps", {}).values():
        error = step.get("error")
        if error:
            errors.append(error.lower())
    combined = " | ".join(errors)
    if any(token in combined for token in ["timeout", "connection", "ssl", "proxy", "name resolution"]):
        pytest.skip(f"live network unavailable for integration test: {combined}")


@pytest.fixture
def live_server(tmp_path):
    port = _pick_free_port()
    base_url = f"http://127.0.0.1:{port}"
    config = ServerConfig(
        host="127.0.0.1",
        port=port,
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(tmp_path / "raw"),
        data_dir=str(tmp_path / "data"),
        public_base_url=base_url,
        default_share_expires_in=3600,
    )
    app = create_app(config)
    server = uvicorn.Server(
        uvicorn.Config(
            app,
            host="127.0.0.1",
            port=port,
            log_level="warning",
        )
    )
    thread = threading.Thread(target=server.run, daemon=True)
    thread.start()
    _wait_for_health(base_url)
    try:
        yield {
            "base_url": base_url,
            "tmp_path": tmp_path,
            "config": config,
        }
    finally:
        server.should_exit = True
        thread.join(timeout=10)


@pytest.mark.integration
@pytest.mark.slow
@pytest.mark.parametrize(("arxiv_id", "expected_page_id"), TEST_PAPERS)
def test_live_server_ingest_and_paper_share_links_work(live_server, arxiv_id, expected_page_id):
    base_url = live_server["base_url"]

    ingest_response = requests.post(
        f"{base_url}/api/ingest/paper",
        json={"arxiv_id": arxiv_id, "extract": False, "sync_neo4j": False},
        timeout=20,
    )
    ingest_response.raise_for_status()
    ingest_task = _poll_json(f"{base_url}/api/ingest/{ingest_response.json()['task_id']}", timeout=240)

    _skip_if_network_issue(ingest_task)
    assert ingest_task["status"] == "succeeded"
    assert ingest_task["steps"]["fetch"]["status"] == "succeeded"
    assert ingest_task["steps"]["parse"]["status"] == "succeeded"
    assert ingest_task["steps"]["wiki"]["status"] == "skipped"
    assert ingest_task["steps"]["wiki"]["message"] == "wiki creation skipped on server"
    assert ingest_task["steps"]["neo4j"]["status"] == "skipped"

    page_response = requests.get(f"{base_url}/api/pages/{expected_page_id}", timeout=20)
    assert page_response.status_code == 404

    page_path = live_server["tmp_path"] / "wiki" / "sources" / "papers" / f"{expected_page_id}.md"
    assert not page_path.exists()

    resources_response = requests.get(f"{base_url}/api/papers/{arxiv_id}/resources", timeout=20)
    resources_response.raise_for_status()
    resources = resources_response.json()

    pdf_asset = resources["assets"]["pdf"]
    markdown_asset = resources["assets"]["markdown"]
    json_asset = resources["assets"]["json"]

    assert resources["arxiv_id"] == arxiv_id
    assert pdf_asset["exists"] is True
    assert markdown_asset["exists"] is True
    assert json_asset["exists"] is True

    pdf_response = requests.get(f"{base_url}{pdf_asset['url']}", timeout=30)
    pdf_response.raise_for_status()
    assert pdf_response.content.startswith(b"%PDF")

    markdown_response = requests.get(f"{base_url}{markdown_asset['url']}", timeout=30)
    markdown_response.raise_for_status()
    markdown_text = markdown_response.text
    assert "## Abstract" in markdown_text

    json_response = requests.get(f"{base_url}{json_asset['url']}", timeout=30)
    json_response.raise_for_status()
    metadata = json_response.json()
    assert metadata["arxiv_id"] == arxiv_id
    assert metadata["title"]
    assert metadata["title"] in markdown_text

    share_path = urlparse(pdf_asset["url"]).path.strip("/").split("/")
    share_token = share_path[1]
    share_index = requests.get(f"{base_url}/share/{share_token}/", timeout=20)
    share_index.raise_for_status()
    assert "papers/pdf/" in share_index.text
    assert "papers/markdown/" in share_index.text
    assert "papers/json/" in share_index.text
