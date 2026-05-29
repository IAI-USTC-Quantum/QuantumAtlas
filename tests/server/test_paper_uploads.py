"""Tests for /api/papers/{arxiv_id}/upload-pdf and /upload-markdown endpoints."""

from __future__ import annotations

import io
import json
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from qatlas.server.config import ServerConfig
from qatlas.server.main import create_app

PDF_HEADER = b"%PDF-1.4\n%dummy quantum atlas test pdf\n%%EOF\n"


@pytest.fixture
def client(tmp_path):
    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(tmp_path / "raw"),
        data_dir=str(tmp_path / "data"),
        user_header="X-Forwarded-User",
    )
    with TestClient(create_app(config)) as test_client:
        # Stash raw_root on the client so tests can inspect on-disk results.
        test_client._raw_root = (tmp_path / "raw").resolve()  # type: ignore[attr-defined]
        yield test_client


def _post_pdf(client, arxiv_id, pdf_bytes=PDF_HEADER, *, metadata=None, overwrite=False, user="alice@example.com"):
    files = {"pdf": ("paper.pdf", io.BytesIO(pdf_bytes), "application/pdf")}
    if metadata is not None:
        files["metadata"] = (
            "meta.json",
            io.BytesIO(json.dumps(metadata).encode("utf-8")),
            "application/json",
        )
    params = {"overwrite": "true"} if overwrite else None
    headers = {"X-Forwarded-User": user} if user else {}
    return client.post(
        f"/api/papers/{arxiv_id}/upload-pdf",
        files=files,
        params=params,
        headers=headers,
    )


def _post_markdown(client, arxiv_id, text="# Quantum Atlas test\n", *, overwrite=False, source=None, user="alice@example.com"):
    files = {"markdown": ("paper.md", io.BytesIO(text.encode("utf-8")), "text/markdown")}
    params = {}
    if overwrite:
        params["overwrite"] = "true"
    if source is not None:
        params["source"] = source
    headers = {"X-Forwarded-User": user} if user else {}
    return client.post(
        f"/api/papers/{arxiv_id}/upload-markdown",
        files=files,
        params=params or None,
        headers=headers,
    )


class TestArxivIdValidation:
    @pytest.mark.parametrize(
        "bad_id",
        [
            "quant-ph/9508027",  # missing version
            "9508027v1",        # old-style without category prefix
            "2501.00010",       # new style missing version
            "hep-th/abcd123v1",  # non-digit body
            "QUANT-PH/9508027v1",  # uppercase category
            "2501.1v1",         # too few digits after dot
            "garbage_id_v1",    # nothing arXiv-shaped
        ],
    )
    def test_rejects_malformed_ids(self, client, bad_id):
        resp = _post_pdf(client, bad_id)
        assert resp.status_code == 400, resp.text
        assert "arxiv_id" in resp.json()["detail"]

    @pytest.mark.parametrize(
        "good_id",
        [
            "quant-ph/9508027v1",
            "cond-mat/0701123v2",
            "2501.00010v1",
            "0704.0001v3",
            "2509.12345v1",
        ],
    )
    def test_accepts_canonical_ids(self, client, good_id):
        resp = _post_pdf(client, good_id)
        assert resp.status_code == 201, resp.text


class TestUploadPdf:
    def test_old_style_pdf_lands_on_sharded_path(self, client):
        resp = _post_pdf(client, "quant-ph/9508027v1")
        assert resp.status_code == 201, resp.text
        body = resp.json()
        assert body["arxiv_id"] == "quant-ph/9508027v1"
        assert body["key"] == "9508027v1"
        assert body["pdf_path"] == "pdf/9508/9508027v1.pdf"
        assert body["uploaded_by"] == "alice@example.com"
        assert (client._raw_root / "pdf" / "9508" / "9508027v1.pdf").read_bytes().startswith(b"%PDF-")

    def test_new_style_pdf_with_metadata(self, client):
        metadata = {
            "arxiv_id": "2501.00010v1",
            "title": "Demo Paper",
            "authors": ["A. N. Other"],
            "abstract": "Test abstract",
        }
        resp = _post_pdf(client, "2501.00010v1", metadata=metadata)
        assert resp.status_code == 201, resp.text
        body = resp.json()
        assert body["pdf_path"] == "pdf/2501/2501.00010v1.pdf"
        assert body["metadata_path"] == "json/2501/2501.00010v1.json"
        json_file = client._raw_root / "json" / "2501" / "2501.00010v1.json"
        assert json.loads(json_file.read_text(encoding="utf-8")) == metadata

    def test_non_pdf_payload_rejected(self, client):
        resp = _post_pdf(client, "2501.00010v1", pdf_bytes=b"not a pdf")
        assert resp.status_code == 400, resp.text
        assert "PDF" in resp.json()["detail"]
        # The bogus file should not remain on disk.
        assert not (client._raw_root / "pdf" / "2501" / "2501.00010v1.pdf").exists()

    def test_conflict_without_overwrite(self, client):
        first = _post_pdf(client, "2501.00010v1")
        assert first.status_code == 201
        second = _post_pdf(client, "2501.00010v1")
        assert second.status_code == 409
        assert "overwrite" in second.json()["detail"]

    def test_overwrite_succeeds(self, client):
        _post_pdf(client, "2501.00010v1", pdf_bytes=PDF_HEADER + b"orig")
        retry = _post_pdf(client, "2501.00010v1", pdf_bytes=PDF_HEADER + b"updated", overwrite=True)
        assert retry.status_code == 201
        on_disk = (client._raw_root / "pdf" / "2501" / "2501.00010v1.pdf").read_bytes()
        assert b"updated" in on_disk

    def test_metadata_must_be_valid_json(self, client):
        files = {
            "pdf": ("paper.pdf", io.BytesIO(PDF_HEADER), "application/pdf"),
            "metadata": ("meta.json", io.BytesIO(b"not json {"), "application/json"),
        }
        resp = client.post(
            "/api/papers/2501.00010v1/upload-pdf",
            files=files,
            headers={"X-Forwarded-User": "alice"},
        )
        assert resp.status_code == 400
        # JSON cleanup so a retry with valid metadata would not 409 on metadata.
        assert not (client._raw_root / "json" / "2501" / "2501.00010v1.json").exists()


class TestUploadMarkdown:
    def test_markdown_lands_on_sharded_path(self, client):
        resp = _post_markdown(client, "quant-ph/9508027v1", source="mineru")
        assert resp.status_code == 201, resp.text
        body = resp.json()
        assert body["markdown_path"] == "markdown/9508/9508027v1.md"
        assert body["source"] == "mineru"
        text = (client._raw_root / "markdown" / "9508" / "9508027v1.md").read_text(encoding="utf-8")
        assert text.startswith("# Quantum Atlas test")

    def test_conflict_without_overwrite(self, client):
        first = _post_markdown(client, "2501.00010v1")
        assert first.status_code == 201
        second = _post_markdown(client, "2501.00010v1")
        assert second.status_code == 409

    def test_empty_markdown_rejected(self, client):
        resp = _post_markdown(client, "2501.00010v1", text="")
        assert resp.status_code == 400

    def test_audit_user_header_recorded(self, client):
        resp = _post_markdown(client, "2501.00010v1", user="reviewer@lab")
        assert resp.status_code == 201
        assert resp.json()["uploaded_by"] == "reviewer@lab"

    def test_missing_user_header_is_allowed_and_recorded_as_none(self, client):
        resp = _post_markdown(client, "2501.00010v1", user=None)
        assert resp.status_code == 201
        assert resp.json()["uploaded_by"] is None
