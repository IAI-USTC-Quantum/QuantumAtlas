"""Narrow unit tests for ingest API helpers and route validation.

These tests intentionally do NOT exercise the full ingest pipeline (no
fetch → parse chain) and never touch external services such as
``export.arxiv.org`` or MinerU. End-to-end pipeline coverage lives under
``tests/integration/``.

Scope:
    * Pure helper functions in ``atlas.server.routers.api`` (error
      formatting, retry classification, continue-stage logic).
    * FastAPI route behaviour that completes synchronously:
        - request validation (400)
        - read-only endpoints (200/404)
        - 202 task enqueue path with ``execute_ingest`` stubbed out so
          the background worker performs no real work.
"""

from __future__ import annotations

import requests
from fastapi.testclient import TestClient

from atlas.server.config import ServerConfig
from atlas.server.main import create_app
from atlas.server.routers import api as api_module
from atlas.server.routers.api import (
    STAGE_ORDER,
    _friendly_fetch_error,
    _friendly_parse_error,
    _is_retryable_fetch_error,
    _remaining_stage_flags,
    _stage_done_for_continue,
)
from atlas.server.tasks import IngestTask, StepStatus


# === Pure helper tests ============================================


class TestFriendlyFetchError:
    def test_404_message_is_user_friendly(self):
        exc = requests.HTTPError("404 Client Error: Not Found")
        assert _friendly_fetch_error(exc, "9508027") == "Paper not found on arXiv: 9508027"

    def test_timeout_exception_is_recognised(self):
        assert _friendly_fetch_error(
            requests.Timeout("read timed out"), "9508027"
        ) == "Failed to download PDF: connection or read timeout"

    def test_timeout_keyword_in_message_is_recognised(self):
        exc = RuntimeError("connection timeout while contacting arxiv")
        assert "timeout" in _friendly_fetch_error(exc, "9508027").lower()

    def test_unknown_error_falls_back_to_generic_message(self):
        msg = _friendly_fetch_error(RuntimeError("boom"), "9508027")
        assert msg.startswith("Failed to fetch paper:")
        assert "boom" in msg


class TestFriendlyParseError:
    def test_mineru_error_is_tagged(self):
        msg = _friendly_parse_error(RuntimeError("MinerU upstream returned 500"))
        assert msg.startswith("MinerU parsing failed:")

    def test_pymupdf_error_suggests_install(self):
        msg = _friendly_parse_error(ImportError("No module named 'pymupdf'"))
        assert msg == "PDF parser unavailable: pymupdf may not be installed"

    def test_encrypted_pdf_is_explained(self):
        msg = _friendly_parse_error(RuntimeError("file appears encrypted"))
        assert "corrupted or encrypted" in msg

    def test_public_base_url_error_is_passed_through(self):
        msg = _friendly_parse_error(ValueError("public_base_url is required for sharing"))
        assert msg == "public_base_url is required for sharing"


class TestIsRetryableFetchError:
    def test_timeout_is_retryable(self):
        assert _is_retryable_fetch_error(requests.Timeout("slow")) is True

    def test_connection_error_is_retryable(self):
        assert _is_retryable_fetch_error(requests.ConnectionError("reset")) is True

    def test_429_and_5xx_http_errors_are_retryable(self):
        for status in (408, 429, 500, 502, 503, 504):
            response = requests.Response()
            response.status_code = status
            exc = requests.HTTPError(f"{status} Server Error", response=response)
            assert _is_retryable_fetch_error(exc) is True, status

    def test_4xx_other_than_408_429_is_not_retryable(self):
        for status in (400, 401, 403, 404):
            response = requests.Response()
            response.status_code = status
            exc = requests.HTTPError(f"{status} Client Error", response=response)
            assert _is_retryable_fetch_error(exc) is False, status

    def test_unrelated_exception_is_not_retryable(self):
        assert _is_retryable_fetch_error(ValueError("nope")) is False


# === IngestTask continuation logic ================================


def _task_with_steps(**step_kwargs: StepStatus) -> IngestTask:
    """Build an in-memory IngestTask with the given step statuses."""
    steps = {stage: StepStatus() for stage in STAGE_ORDER}
    steps.update(step_kwargs)
    return IngestTask(
        task_id="t1",
        arxiv_id="9508027",
        status="queued",
        submitted_at="2026-01-01T00:00:00Z",
        steps=steps,
    )


class TestStageDoneForContinue:
    def test_succeeded_step_is_done(self):
        task = _task_with_steps(parse=StepStatus(status="succeeded"))
        assert _stage_done_for_continue(task, "parse") is True

    def test_pending_step_is_not_done(self):
        task = _task_with_steps()
        assert _stage_done_for_continue(task, "parse") is False

    def test_failed_step_is_not_done(self):
        task = _task_with_steps(parse=StepStatus(status="failed"))
        assert _stage_done_for_continue(task, "parse") is False

    def test_skipped_fetch_with_pdf_path_counts_as_done(self):
        task = _task_with_steps(
            fetch=StepStatus(status="skipped", result={"pdf_path": "/tmp/x.pdf"}),
        )
        assert _stage_done_for_continue(task, "fetch") is True

    def test_skipped_fetch_without_pdf_path_is_not_done(self):
        task = _task_with_steps(fetch=StepStatus(status="skipped", result={}))
        assert _stage_done_for_continue(task, "fetch") is False

    def test_skipped_non_fetch_stage_is_not_done(self):
        task = _task_with_steps(parse=StepStatus(status="skipped"))
        assert _stage_done_for_continue(task, "parse") is False


class TestRemainingStageFlags:
    def test_all_stages_when_nothing_finished(self):
        task = _task_with_steps()
        flags = _remaining_stage_flags(task)
        assert flags == {stage: True for stage in STAGE_ORDER}

    def test_resumes_from_first_unfinished_stage(self):
        task = _task_with_steps(
            fetch=StepStatus(status="succeeded"),
        )
        flags = _remaining_stage_flags(task)
        assert flags == {"fetch": False, "parse": True}

    def test_failure_in_parse_resumes_from_parse(self):
        task = _task_with_steps(
            fetch=StepStatus(status="succeeded"),
            parse=StepStatus(status="failed"),
        )
        flags = _remaining_stage_flags(task)
        assert flags == {"fetch": False, "parse": True}


# === FastAPI route tests ==========================================


def _make_client(tmp_path, monkeypatch, **extra_config):
    """Create a TestClient with execute_* stubs so no real work happens."""
    monkeypatch.setattr(api_module, "execute_ingest", lambda *a, **kw: None)

    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(tmp_path / "raw"),
        data_dir=str(tmp_path / "data"),
        **extra_config,
    )
    app = create_app(config)
    return TestClient(app), app


class TestIngestStagesEndpoint:
    def test_returns_canonical_stage_order(self, tmp_path, monkeypatch):
        client, _ = _make_client(tmp_path, monkeypatch)
        with client:
            response = client.get("/api/ingest/stages")
        assert response.status_code == 200
        body = response.json()
        assert body["order"] == ["fetch", "parse"]
        assert [s["name"] for s in body["stages"]] == ["fetch", "parse"]
        assert "stop_after" in body["controls"]
        assert "stages" in body["controls"]


class TestIngestPaperValidation:
    def test_invalid_arxiv_id_returns_400(self, tmp_path, monkeypatch):
        client, _ = _make_client(tmp_path, monkeypatch)
        with client:
            response = client.post(
                "/api/ingest/paper",
                json={"arxiv_id": "not-an-arxiv-id!!", "parser": "pymupdf"},
            )
        assert response.status_code == 400
        assert "invalid arxiv_id" in response.json()["detail"].lower()

    def test_all_stages_disabled_returns_400(self, tmp_path, monkeypatch):
        client, _ = _make_client(tmp_path, monkeypatch)
        with client:
            response = client.post(
                "/api/ingest/paper",
                json={
                    "arxiv_id": "9508027",
                    "parser": "pymupdf",
                    "fetch": False,
                    "parse": False,
                },
            )
        assert response.status_code == 400
        assert "no ingest stages selected" in response.json()["detail"]

    def test_extra_fields_are_rejected(self, tmp_path, monkeypatch):
        """ff-only: extract / create_wiki / sync_neo4j must NOT be accepted."""
        client, _ = _make_client(tmp_path, monkeypatch)
        with client:
            for field in ("extract", "create_wiki", "sync_neo4j", "llm_provider"):
                response = client.post(
                    "/api/ingest/paper",
                    json={
                        "arxiv_id": "9508027",
                        "parser": "pymupdf",
                        field: False if field != "llm_provider" else "openai",
                    },
                )
                assert response.status_code == 422, f"{field} should be rejected"

    def test_parser_field_is_required(self, tmp_path, monkeypatch):
        """Refuse silent pymupdf fallback: parser must be explicitly chosen."""
        client, _ = _make_client(tmp_path, monkeypatch)
        with client:
            response = client.post(
                "/api/ingest/paper",
                json={"arxiv_id": "9508027"},
            )
        assert response.status_code == 422
        detail = response.json()["detail"]
        assert any("parser" in str(err.get("loc", [])) for err in detail), detail

    def test_valid_request_returns_202_and_persists_task(self, tmp_path, monkeypatch):
        client, app = _make_client(tmp_path, monkeypatch)
        with client:
            response = client.post(
                "/api/ingest/paper",
                json={"arxiv_id": "9508027", "parser": "pymupdf"},
            )
        assert response.status_code == 202
        payload = response.json()
        assert payload["status"] == "queued"
        task = app.state.ingest_store.get(payload["task_id"])
        assert task is not None
        assert task.arxiv_id == "9508027"
        assert task.status == "queued"
        assert task.options["parser"] == "pymupdf"


class TestReviewedExtractionEndpointRemoved:
    """ff-only: server must not expose the reviewed-extraction endpoint."""

    def test_endpoint_is_not_registered(self, tmp_path, monkeypatch):
        client, _ = _make_client(tmp_path, monkeypatch)
        with client:
            response = client.post(
                "/api/ingest/paper/reviewed-extraction",
                json={"arxiv_id": "9508027"},
            )
        assert response.status_code == 404


class TestIngestTaskQueries:
    def test_get_unknown_task_returns_404(self, tmp_path, monkeypatch):
        client, _ = _make_client(tmp_path, monkeypatch)
        with client:
            response = client.get("/api/ingest/does-not-exist")
        assert response.status_code == 404

    def test_get_known_task_returns_payload(self, tmp_path, monkeypatch):
        client, app = _make_client(tmp_path, monkeypatch)
        task = IngestTask(
            task_id="known01",
            arxiv_id="9508027",
            status="succeeded",
            submitted_at="2026-01-01T00:00:00Z",
        )
        with client:
            app.state.ingest_store.save(task)
            response = client.get("/api/ingest/known01")
        assert response.status_code == 200
        body = response.json()
        assert body["task_id"] == "known01"
        assert body["arxiv_id"] == "9508027"
        assert body["status"] == "succeeded"

    def test_list_ingest_tasks_is_empty_initially(self, tmp_path, monkeypatch):
        client, _ = _make_client(tmp_path, monkeypatch)
        with client:
            response = client.get("/api/ingests")
        assert response.status_code == 200
        assert response.json() == {"total": 0, "tasks": []}

    def test_list_ingest_tasks_respects_limit(self, tmp_path, monkeypatch):
        client, app = _make_client(tmp_path, monkeypatch)
        with client:
            for i in range(5):
                app.state.ingest_store.save(
                    IngestTask(
                        task_id=f"task{i:02d}",
                        arxiv_id="9508027",
                        status="queued",
                        submitted_at=f"2026-01-0{i + 1}T00:00:00Z",
                    )
                )
            response = client.get("/api/ingests?limit=3")
        assert response.status_code == 200
        body = response.json()
        assert body["total"] == 3
        assert len(body["tasks"]) == 3


class TestContinueIngestValidation:
    def test_unknown_source_task_returns_404(self, tmp_path, monkeypatch):
        client, _ = _make_client(tmp_path, monkeypatch)
        with client:
            response = client.post(
                "/api/ingest/does-not-exist/continue",
                json={"stages": ["parse"], "parser": "pymupdf"},
            )
        assert response.status_code == 404
        assert "not found" in response.json()["detail"].lower()

    def test_extra_reviewed_fields_are_rejected(self, tmp_path, monkeypatch):
        """ff-only: continue must NOT accept reviewed-extraction fields."""
        client, app = _make_client(tmp_path, monkeypatch)
        task = IngestTask(
            task_id="seed01",
            arxiv_id="9508027",
            status="succeeded",
            submitted_at="2026-01-01T00:00:00Z",
        )
        with client:
            app.state.ingest_store.save(task)
            for field in ("algorithm", "algorithm_ir", "create_wiki", "sync_neo4j", "llm_provider"):
                response = client.post(
                    f"/api/ingest/seed01/continue",
                    json={
                        "parser": "pymupdf",
                        field: {} if field in ("algorithm", "algorithm_ir") else False,
                    },
                )
                assert response.status_code == 422, f"{field} should be rejected"

    def test_parser_field_is_required(self, tmp_path, monkeypatch):
        """Refuse silent pymupdf fallback on continue too."""
        client, _ = _make_client(tmp_path, monkeypatch)
        with client:
            response = client.post(
                "/api/ingest/does-not-exist/continue",
                json={"stages": ["parse"]},
            )
        assert response.status_code == 422
        detail = response.json()["detail"]
        assert any("parser" in str(err.get("loc", [])) for err in detail), detail

