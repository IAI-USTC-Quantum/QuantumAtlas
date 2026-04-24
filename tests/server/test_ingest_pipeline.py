import json
from pathlib import Path

import requests
from fastapi.testclient import TestClient

from atlas.paper_assets import safe_paper_key
from atlas.server.config import ServerConfig
from atlas.server.main import create_app


def _seed_paper_assets(
    raw_dir: Path,
    arxiv_id: str = "9508027",
    *,
    markdown: bool = True,
) -> dict:
    key = safe_paper_key(arxiv_id)
    metadata = {
        "arxiv_id": arxiv_id,
        "title": "A Fast Quantum Mechanical Algorithm for Database Search",
        "authors": ["Lov K. Grover"],
        "abstract": "A test abstract.",
        "published": "1996-11-19T00:00:00Z",
        "categories": ["quant-ph"],
    }
    (raw_dir / "pdf").mkdir(parents=True, exist_ok=True)
    (raw_dir / "json").mkdir(parents=True, exist_ok=True)
    (raw_dir / "pdf" / f"{key}.pdf").write_bytes(b"%PDF-1.4\n% test pdf\n")
    (raw_dir / "json" / f"{key}.json").write_text(
        json.dumps(metadata),
        encoding="utf-8",
    )
    if markdown:
        (raw_dir / "markdown").mkdir(parents=True, exist_ok=True)
        (raw_dir / "markdown" / f"{key}.md").write_text(
            "# A Fast Quantum Mechanical Algorithm for Database Search\n\n"
            "## Abstract\n\nA test abstract.\n",
            encoding="utf-8",
        )
    return metadata


class _FakePDFResponse:
    status_code = 200
    headers = {"content-length": "13"}

    def raise_for_status(self):
        return None

    def iter_content(self, chunk_size):
        yield b"%PDF-1.4\n"
        yield b"test\n"


def _arxiv_metadata(arxiv_id: str = "9508027") -> dict:
    return {
        "arxiv_id": arxiv_id,
        "title": "A Fast Quantum Mechanical Algorithm for Database Search",
        "authors": ["Lov K. Grover"],
        "abstract": "A test abstract.",
        "published": "1996-11-19T00:00:00Z",
        "categories": ["quant-ph"],
        "pdf_url": f"https://arxiv.org/pdf/{arxiv_id}.pdf",
    }


def test_ingest_can_resume_from_existing_assets_and_create_wiki(tmp_path):
    raw_dir = tmp_path / "raw"
    _seed_paper_assets(raw_dir, markdown=True)

    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(raw_dir),
        data_dir=str(tmp_path / "data"),
    )
    app = create_app(config)

    with TestClient(app) as client:
        response = client.post(
            "/api/ingest/paper",
            json={
                "arxiv_id": "9508027",
                "fetch": False,
                "parse": True,
                "extract": False,
                "create_wiki": True,
                "sync_neo4j": False,
            },
        )
        assert response.status_code == 202

        task_response = client.get(f"/api/ingest/{response.json()['task_id']}")
        task = task_response.json()

    assert task["status"] == "succeeded"
    assert task["steps"]["fetch"]["status"] == "skipped"
    assert task["steps"]["fetch"]["result"]["reused"] is True
    assert task["steps"]["parse"]["status"] == "succeeded"
    assert task["steps"]["parse"]["message"] == "markdown ready"
    assert task["steps"]["wiki"]["status"] == "succeeded"
    assert "paper-arxiv-9508027" in task["steps"]["wiki"]["result"]["page_ids"]
    assert (tmp_path / "wiki" / "sources" / "papers" / "paper-arxiv-9508027.md").is_file()


def test_ingest_stages_selects_exact_steps_and_reuses_assets(tmp_path):
    raw_dir = tmp_path / "raw"
    _seed_paper_assets(raw_dir, markdown=True)

    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(raw_dir),
        data_dir=str(tmp_path / "data"),
    )
    app = create_app(config)

    with TestClient(app) as client:
        response = client.post(
            "/api/ingest/paper",
            json={
                "arxiv_id": "9508027",
                "stages": ["wiki"],
            },
        )
        assert response.status_code == 202
        task = client.get(f"/api/ingest/{response.json()['task_id']}").json()

    assert task["status"] == "succeeded"
    assert task["options"]["requested_stages"] == ["wiki"]
    assert task["steps"]["fetch"]["status"] == "skipped"
    assert task["steps"]["fetch"]["result"]["reused"] is True
    assert task["steps"]["parse"]["status"] == "skipped"
    assert task["steps"]["parse"]["result"]["reused"] is True
    assert task["steps"]["extract"]["status"] == "skipped"
    assert task["steps"]["wiki"]["status"] == "succeeded"
    assert task["steps"]["neo4j"]["status"] == "skipped"
    assert "paper-arxiv-9508027" in task["steps"]["wiki"]["result"]["page_ids"]


def test_ingest_stop_after_parse_exposes_stage_status(tmp_path):
    raw_dir = tmp_path / "raw"
    _seed_paper_assets(raw_dir, markdown=True)

    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(raw_dir),
        data_dir=str(tmp_path / "data"),
    )
    app = create_app(config)

    with TestClient(app) as client:
        stages_response = client.get("/api/ingest/stages")
        assert stages_response.status_code == 200
        assert stages_response.json()["order"] == ["fetch", "parse", "extract", "wiki", "neo4j"]

        response = client.post(
            "/api/ingest/paper",
            json={
                "arxiv_id": "9508027",
                "fetch": False,
                "stop_after": "parse",
            },
        )
        assert response.status_code == 202
        task = client.get(f"/api/ingest/{response.json()['task_id']}").json()

    assert task["status"] == "succeeded"
    assert task["options"]["stop_after"] == "parse"
    assert task["steps"]["fetch"]["status"] == "succeeded"
    assert task["steps"]["fetch"]["result"]["reused"] is True
    assert task["steps"]["parse"]["status"] == "succeeded"
    assert task["steps"]["extract"]["status"] == "skipped"
    assert task["steps"]["wiki"]["status"] == "skipped"
    assert task["steps"]["neo4j"]["status"] == "skipped"


def test_ingest_continue_runs_regular_remaining_stages_from_local_assets(tmp_path):
    raw_dir = tmp_path / "raw"
    _seed_paper_assets(raw_dir, markdown=True)

    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(raw_dir),
        data_dir=str(tmp_path / "data"),
    )
    app = create_app(config)

    with TestClient(app) as client:
        first = client.post(
            "/api/ingest/paper",
            json={
                "arxiv_id": "9508027",
                "fetch": False,
                "stop_after": "fetch",
            },
        )
        assert first.status_code == 202
        first_task_id = first.json()["task_id"]
        first_task = client.get(f"/api/ingest/{first_task_id}").json()
        assert first_task["steps"]["fetch"]["status"] == "succeeded"
        assert first_task["steps"]["parse"]["status"] == "skipped"

        response = client.post(
            f"/api/ingest/{first_task_id}/continue",
            json={
                "stages": ["parse", "wiki"],
            },
        )
        assert response.status_code == 202
        task = client.get(f"/api/ingest/{response.json()['task_id']}").json()

    assert task["status"] == "succeeded"
    assert task["options"]["source_task_id"] == first_task_id
    assert task["options"]["requested_stages"] == ["parse", "wiki"]
    assert task["steps"]["fetch"]["status"] == "skipped"
    assert task["steps"]["fetch"]["result"]["reused"] is True
    assert task["steps"]["parse"]["status"] == "succeeded"
    assert task["steps"]["extract"]["status"] == "skipped"
    assert task["steps"]["wiki"]["status"] == "succeeded"
    assert "paper-arxiv-9508027" in task["steps"]["wiki"]["result"]["page_ids"]


def test_ingest_fetch_retries_three_times_then_stops(tmp_path, monkeypatch):
    calls = {"metadata": 0}
    monkeypatch.setattr("atlas.server.routers.api.RETRY_DELAY_SECONDS", 0)

    def timeout_metadata(self, arxiv_id):
        calls["metadata"] += 1
        raise requests.Timeout("temporary network timeout")

    monkeypatch.setattr(
        "atlas.parser.arxiv_fetcher.ArxivFetcher.fetch_metadata",
        timeout_metadata,
    )

    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(tmp_path / "raw"),
        data_dir=str(tmp_path / "data"),
    )
    app = create_app(config)

    with TestClient(app) as client:
        response = client.post(
            "/api/ingest/paper",
            json={
                "arxiv_id": "9508027",
                "stop_after": "fetch",
            },
        )
        assert response.status_code == 202
        task = client.get(f"/api/ingest/{response.json()['task_id']}").json()

    assert calls["metadata"] == 3
    assert task["status"] == "failed"
    assert task["steps"]["fetch"]["status"] == "failed"
    assert task["steps"]["fetch"]["progress"]["phase"] == "metadata"
    assert task["steps"]["fetch"]["progress"]["attempt"] == 3
    assert task["steps"]["fetch"]["progress"]["max_attempts"] == 3
    assert task["steps"]["fetch"]["progress"]["will_retry"] is False
    assert "timeout" in task["steps"]["fetch"]["error"].lower()
    assert task["steps"]["parse"]["status"] == "skipped"


def test_ingest_metadata_retry_can_recover_and_finish_fetch(tmp_path, monkeypatch):
    calls = {"metadata": 0, "download": 0}
    monkeypatch.setattr("atlas.server.routers.api.RETRY_DELAY_SECONDS", 0)

    def flaky_metadata(self, arxiv_id):
        calls["metadata"] += 1
        if calls["metadata"] < 3:
            raise requests.Timeout("temporary metadata timeout")
        return _arxiv_metadata(arxiv_id)

    def download_success(self, *args, **kwargs):
        calls["download"] += 1
        return _FakePDFResponse()

    monkeypatch.setattr(
        "atlas.parser.arxiv_fetcher.ArxivFetcher.fetch_metadata",
        flaky_metadata,
    )
    monkeypatch.setattr("requests.Session.get", download_success)

    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(tmp_path / "raw"),
        data_dir=str(tmp_path / "data"),
    )
    app = create_app(config)

    with TestClient(app) as client:
        response = client.post(
            "/api/ingest/paper",
            json={
                "arxiv_id": "9508027",
                "stop_after": "fetch",
            },
        )
        assert response.status_code == 202
        task = client.get(f"/api/ingest/{response.json()['task_id']}").json()

    assert calls == {"metadata": 3, "download": 1}
    assert task["status"] == "succeeded"
    assert task["steps"]["fetch"]["status"] == "succeeded"
    assert task["steps"]["fetch"]["result"]["reused"] is False
    assert task["steps"]["fetch"]["progress"]["phase"] == "complete"
    assert task["steps"]["fetch"]["progress"]["attempt"] == 1
    assert task["steps"]["fetch"]["progress"]["max_attempts"] == 3
    assert (tmp_path / "raw" / "pdf" / "9508027.pdf").read_bytes().startswith(b"%PDF")


def test_ingest_pdf_download_retry_can_recover(tmp_path, monkeypatch):
    calls = {"metadata": 0, "download": 0}
    monkeypatch.setattr("atlas.server.routers.api.RETRY_DELAY_SECONDS", 0)

    def metadata_success(self, arxiv_id):
        calls["metadata"] += 1
        return _arxiv_metadata(arxiv_id)

    def flaky_download(self, *args, **kwargs):
        calls["download"] += 1
        if calls["download"] < 3:
            raise requests.Timeout("temporary pdf timeout")
        return _FakePDFResponse()

    monkeypatch.setattr(
        "atlas.parser.arxiv_fetcher.ArxivFetcher.fetch_metadata",
        metadata_success,
    )
    monkeypatch.setattr("requests.Session.get", flaky_download)

    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(tmp_path / "raw"),
        data_dir=str(tmp_path / "data"),
    )
    app = create_app(config)

    with TestClient(app) as client:
        response = client.post(
            "/api/ingest/paper",
            json={
                "arxiv_id": "9508027",
                "stop_after": "fetch",
            },
        )
        assert response.status_code == 202
        task = client.get(f"/api/ingest/{response.json()['task_id']}").json()

    assert calls == {"metadata": 1, "download": 3}
    assert task["status"] == "succeeded"
    assert task["steps"]["fetch"]["status"] == "succeeded"
    assert task["steps"]["fetch"]["progress"]["phase"] == "complete"
    assert task["steps"]["fetch"]["progress"]["attempt"] == 3
    assert task["steps"]["fetch"]["progress"]["max_attempts"] == 3
    assert (tmp_path / "raw" / "pdf" / "9508027.pdf").is_file()


def test_ingest_can_parse_existing_pdf_with_mineru_share_url(tmp_path, monkeypatch):
    raw_dir = tmp_path / "raw"
    _seed_paper_assets(raw_dir, markdown=False)
    captured = {}

    class FakeMinerUClient:
        def __init__(self, token, *, base_url="https://mineru.net"):
            captured["token"] = token
            captured["base_url"] = base_url

        def submit_url_task(self, **kwargs):
            captured["submit"] = kwargs
            return "mineru-task-1"

        def get_task(self, task_id):
            captured["task_id"] = task_id
            return {
                "state": "done",
                "full_zip_url": "https://cdn.example/full.zip",
                "extract_progress": {"extracted_pages": 2, "total_pages": 2},
            }

        def download_markdown_from_zip(self, full_zip_url, output_path):
            captured["full_zip_url"] = full_zip_url
            Path(output_path).write_text(
                "# MinerU Markdown\n\n## Abstract\n\nParsed by MinerU.\n",
                encoding="utf-8",
            )

    monkeypatch.setattr("atlas.server.routers.api.MinerUClient", FakeMinerUClient)

    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(raw_dir),
        data_dir=str(tmp_path / "data"),
        public_base_url="https://public.example",
        share_access_token="super-long-share-token",
        mineru_api_token="mineru-token",
        mineru_api_base_url="https://mineru.example",
        mineru_poll_interval=1,
    )
    app = create_app(config)

    with TestClient(app) as client:
        share_response = client.get(
            "/share/super-long-share-token/papers/pdf/9508027.pdf"
        )
        assert share_response.status_code == 200
        assert client.get("/share/wrong-token/papers/pdf/9508027.pdf").status_code == 404

        response = client.post(
            "/api/ingest/paper",
            json={
                "arxiv_id": "9508027",
                "fetch": False,
                "parser": "mineru",
                "extract": False,
                "sync_neo4j": False,
            },
        )
        assert response.status_code == 202
        task = client.get(f"/api/ingest/{response.json()['task_id']}").json()

    assert captured["token"] == "mineru-token"
    assert captured["base_url"] == "https://mineru.example"
    assert (
        captured["submit"]["url"]
        == "https://public.example/share/super-long-share-token/papers/pdf/9508027.pdf"
    )
    assert captured["submit"]["is_ocr"] is False
    assert captured["submit"]["model_version"] == "vlm"
    assert task["status"] == "succeeded"
    assert task["steps"]["parse"]["status"] == "succeeded"
    assert task["steps"]["parse"]["progress"]["parser"] == "mineru"
    assert task["steps"]["parse"]["progress"]["state"] == "done"
    assert task["steps"]["wiki"]["status"] == "succeeded"
    assert (
        (raw_dir / "markdown" / "9508027.md")
        .read_text(encoding="utf-8")
        .startswith("# MinerU Markdown")
    )


def test_ingest_mineru_can_enable_ocr_from_config(tmp_path, monkeypatch):
    raw_dir = tmp_path / "raw"
    _seed_paper_assets(raw_dir, markdown=False)
    captured = {}

    class FakeMinerUClient:
        def __init__(self, token, *, base_url="https://mineru.net"):
            pass

        def submit_url_task(self, **kwargs):
            captured["submit"] = kwargs
            return "mineru-task-ocr"

        def get_task(self, task_id):
            return {
                "state": "done",
                "full_zip_url": "https://cdn.example/full.zip",
                "extract_progress": {"extracted_pages": 1, "total_pages": 1},
            }

        def download_markdown_from_zip(self, full_zip_url, output_path):
            Path(output_path).write_text("# OCR Markdown\n", encoding="utf-8")

    monkeypatch.setattr("atlas.server.routers.api.MinerUClient", FakeMinerUClient)

    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(raw_dir),
        data_dir=str(tmp_path / "data"),
        public_base_url="https://public.example",
        mineru_api_token="mineru-token",
        mineru_is_ocr=True,
    )
    app = create_app(config)

    with TestClient(app) as client:
        response = client.post(
            "/api/ingest/paper",
            json={
                "arxiv_id": "9508027",
                "fetch": False,
                "parser": "mineru",
                "extract": False,
                "sync_neo4j": False,
            },
        )
        assert response.status_code == 202

    assert captured["submit"]["is_ocr"] is True


def test_ingest_mineru_retries_once_then_stops_on_api_failure(tmp_path, monkeypatch):
    raw_dir = tmp_path / "raw"
    _seed_paper_assets(raw_dir, markdown=False)
    captured = {"submits": 0}
    monkeypatch.setattr("atlas.server.routers.api.RETRY_DELAY_SECONDS", 0)

    class FailingMinerUClient:
        def __init__(self, token, *, base_url="https://mineru.net"):
            pass

        def submit_url_task(self, **kwargs):
            captured["submits"] += 1
            return f"mineru-failed-{captured['submits']}"

        def get_task(self, task_id):
            return {
                "state": "failed",
                "err_msg": "upstream parse failed",
                "extract_progress": {"extracted_pages": 0, "total_pages": 2},
            }

    monkeypatch.setattr("atlas.server.routers.api.MinerUClient", FailingMinerUClient)

    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(raw_dir),
        data_dir=str(tmp_path / "data"),
        public_base_url="https://public.example",
        mineru_api_token="mineru-token",
        mineru_poll_interval=1,
    )
    app = create_app(config)

    with TestClient(app) as client:
        response = client.post(
            "/api/ingest/paper",
            json={
                "arxiv_id": "9508027",
                "fetch": False,
                "parser": "mineru",
                "extract": False,
                "sync_neo4j": False,
            },
        )
        assert response.status_code == 202
        task = client.get(f"/api/ingest/{response.json()['task_id']}").json()

    assert captured["submits"] == 2
    assert task["status"] == "failed"
    assert task["steps"]["parse"]["status"] == "failed"
    assert task["steps"]["parse"]["progress"]["attempt"] == 2
    assert task["steps"]["parse"]["progress"]["max_attempts"] == 2
    assert task["steps"]["parse"]["progress"]["will_retry"] is False
    assert "upstream parse failed" in task["steps"]["parse"]["error"]
    assert task["steps"]["wiki"]["status"] == "skipped"
    assert task["steps"]["wiki"]["error"] == "skipped because an earlier ingest stage failed"


def test_ingest_mineru_retry_can_recover(tmp_path, monkeypatch):
    raw_dir = tmp_path / "raw"
    _seed_paper_assets(raw_dir, markdown=False)
    captured = {"submits": 0}
    monkeypatch.setattr("atlas.server.routers.api.RETRY_DELAY_SECONDS", 0)

    class FlakyMinerUClient:
        def __init__(self, token, *, base_url="https://mineru.net"):
            pass

        def submit_url_task(self, **kwargs):
            captured["submits"] += 1
            return f"mineru-task-{captured['submits']}"

        def get_task(self, task_id):
            if task_id == "mineru-task-1":
                return {
                    "state": "failed",
                    "err_msg": "temporary MinerU parse failure",
                    "extract_progress": {"extracted_pages": 0, "total_pages": 2},
                }
            return {
                "state": "done",
                "full_zip_url": "https://cdn.example/full.zip",
                "extract_progress": {"extracted_pages": 2, "total_pages": 2},
            }

        def download_markdown_from_zip(self, full_zip_url, output_path):
            Path(output_path).write_text("# MinerU recovered\n", encoding="utf-8")

    monkeypatch.setattr("atlas.server.routers.api.MinerUClient", FlakyMinerUClient)

    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(raw_dir),
        data_dir=str(tmp_path / "data"),
        public_base_url="https://public.example",
        mineru_api_token="mineru-token",
        mineru_poll_interval=1,
    )
    app = create_app(config)

    with TestClient(app) as client:
        response = client.post(
            "/api/ingest/paper",
            json={
                "arxiv_id": "9508027",
                "fetch": False,
                "parser": "mineru",
                "extract": False,
                "sync_neo4j": False,
            },
        )
        assert response.status_code == 202
        task = client.get(f"/api/ingest/{response.json()['task_id']}").json()

    assert captured["submits"] == 2
    assert task["status"] == "succeeded"
    assert task["steps"]["parse"]["status"] == "succeeded"
    assert task["steps"]["parse"]["progress"]["attempt"] == 2
    assert task["steps"]["parse"]["progress"]["max_attempts"] == 2
    assert task["steps"]["parse"]["progress"]["state"] == "done"
    assert (raw_dir / "markdown" / "9508027.md").read_text(encoding="utf-8").startswith(
        "# MinerU recovered"
    )


def test_ingest_accepts_client_reviewed_extraction_and_creates_wiki(tmp_path, monkeypatch):
    raw_dir = tmp_path / "raw"
    _seed_paper_assets(raw_dir, markdown=True)

    def fail_if_server_extracts(*args, **kwargs):
        raise AssertionError("server-side LLM extraction should not run")

    monkeypatch.setattr(
        "atlas.wiki.ingester.WikiIngester._extract_algorithm",
        fail_if_server_extracts,
    )

    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(raw_dir),
        data_dir=str(tmp_path / "data"),
    )
    app = create_app(config)

    with TestClient(app) as client:
        response = client.post(
            "/api/ingest/paper/reviewed-extraction",
            json={
                "arxiv_id": "9508027",
                "reviewed_by": "alice",
                "algorithm": {
                    "id": "reviewed_search",
                    "name": "Reviewed Quantum Search",
                    "description": "Client-reviewed algorithm description.",
                    "problem_type": "unstructured_search",
                    "primitives": ["prim-qft", "primitive_amplitude_amplification"],
                    "complexity": {
                        "time": "O(sqrt(N))",
                        "space": "O(log N)",
                        "gate_count": "O(sqrt(N))",
                        "circuit_depth": "O(sqrt(N))",
                        "qubit_count": "O(log N)",
                    },
                    "pseudocode": "prepare superposition\nrepeat amplitude amplification",
                },
                "sync_neo4j": False,
            },
        )
        assert response.status_code == 202
        task = client.get(f"/api/ingest/{response.json()['task_id']}").json()

    assert task["status"] == "succeeded"
    assert task["steps"]["fetch"]["status"] == "skipped"
    assert task["steps"]["fetch"]["result"]["metadata_source"] == "local_json"
    assert task["steps"]["parse"]["status"] == "skipped"
    assert task["steps"]["extract"]["status"] == "succeeded"
    assert task["steps"]["extract"]["result"]["source"] == "client"
    assert task["steps"]["extract"]["result"]["reviewed_by"] == "alice"
    assert task["steps"]["wiki"]["status"] == "succeeded"
    assert "paper-arxiv-9508027" in task["steps"]["wiki"]["result"]["page_ids"]
    assert "algo-reviewed-search" in task["steps"]["wiki"]["result"]["page_ids"]
    assert task["steps"]["neo4j"]["status"] == "skipped"

    algo_page = tmp_path / "wiki" / "entities" / "algorithms" / "algo-reviewed-search.md"
    algo_text = algo_page.read_text(encoding="utf-8")
    assert "Client-reviewed algorithm description." in algo_text
    assert "**Problem**: unstructured_search" in algo_text
    assert "- Time: O(sqrt(N))" in algo_text
    assert "[[prim-qft]]" in algo_text
    assert "[[prim-amplitude-amplification]]" in algo_text
    assert "repeat amplitude amplification" in algo_text


def test_ingest_continue_accepts_reviewed_extraction_after_parse(tmp_path, monkeypatch):
    raw_dir = tmp_path / "raw"
    _seed_paper_assets(raw_dir, markdown=True)

    def fail_if_server_extracts(*args, **kwargs):
        raise AssertionError("server-side LLM extraction should not run")

    monkeypatch.setattr(
        "atlas.wiki.ingester.WikiIngester._extract_algorithm",
        fail_if_server_extracts,
    )

    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(raw_dir),
        data_dir=str(tmp_path / "data"),
    )
    app = create_app(config)

    with TestClient(app) as client:
        first = client.post(
            "/api/ingest/paper",
            json={
                "arxiv_id": "9508027",
                "fetch": False,
                "stop_after": "parse",
            },
        )
        assert first.status_code == 202
        first_task_id = first.json()["task_id"]
        first_task = client.get(f"/api/ingest/{first_task_id}").json()
        assert first_task["steps"]["parse"]["status"] == "succeeded"

        response = client.post(
            f"/api/ingest/{first_task_id}/continue",
            json={
                "reviewed_by": "alice",
                "algorithm": {
                    "id": "continued_search",
                    "name": "Continued Quantum Search",
                    "problem_type": "unstructured_search",
                    "primitives": [],
                    "complexity": {},
                },
                "sync_neo4j": False,
            },
        )
        assert response.status_code == 202
        task = client.get(f"/api/ingest/{response.json()['task_id']}").json()

    assert task["status"] == "succeeded"
    assert task["options"]["source_task_id"] == first_task_id
    assert task["steps"]["fetch"]["result"]["metadata_source"] == "local_json"
    assert task["steps"]["parse"]["status"] == "skipped"
    assert task["steps"]["extract"]["status"] == "succeeded"
    assert task["steps"]["extract"]["result"]["reviewed_by"] == "alice"
    assert task["steps"]["wiki"]["status"] == "succeeded"
    assert "algo-continued-search" in task["steps"]["wiki"]["result"]["page_ids"]


def test_ingest_reviewed_extraction_requires_algorithm_identity(tmp_path):
    raw_dir = tmp_path / "raw"
    _seed_paper_assets(raw_dir, markdown=True)

    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(raw_dir),
        data_dir=str(tmp_path / "data"),
    )
    app = create_app(config)

    with TestClient(app) as client:
        response = client.post(
            "/api/ingest/paper/reviewed-extraction",
            json={
                "arxiv_id": "9508027",
                "algorithm": {"name": "Missing ID"},
            },
        )

    assert response.status_code == 400
    assert "algorithm_id/id" in response.json()["detail"]
