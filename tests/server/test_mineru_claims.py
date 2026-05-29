"""Tests for the mineru claim/lease + needs-mineru endpoints."""

from __future__ import annotations

import io
import json
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from qatlas.server.config import ServerConfig
from qatlas.server.main import create_app

PDF_HEADER = b"%PDF-1.4\n%test\n%%EOF\n"


@pytest.fixture
def client(tmp_path):
    config = ServerConfig(
        wiki_dir=str(tmp_path / "wiki"),
        raw_dir=str(tmp_path / "raw"),
        data_dir=str(tmp_path / "data"),
        user_header="X-Forwarded-User",
        share_access_token="test-share-token",
        server_url="http://test.local",
    )
    with TestClient(create_app(config)) as test_client:
        test_client._raw_root = (tmp_path / "raw").resolve()  # type: ignore[attr-defined]
        test_client._data_root = (tmp_path / "data").resolve()  # type: ignore[attr-defined]
        yield test_client


def _put_pdf(client, arxiv_id, user="alice@example.com"):
    resp = client.post(
        f"/api/papers/{arxiv_id}/upload-pdf",
        files={"pdf": ("p.pdf", io.BytesIO(PDF_HEADER), "application/pdf")},
        headers={"X-Forwarded-User": user},
    )
    assert resp.status_code == 201, resp.text


def _put_markdown(client, arxiv_id, text="# md\n"):
    return client.post(
        f"/api/papers/{arxiv_id}/upload-markdown",
        files={"markdown": ("p.md", io.BytesIO(text.encode("utf-8")), "text/markdown")},
        params={"source": "mineru"},
        headers={"X-Forwarded-User": "alice"},
    )


class TestNeedsMineru:
    def test_empty_when_no_papers(self, client):
        resp = client.get("/api/papers/needs-mineru")
        assert resp.status_code == 200
        body = resp.json()
        assert body == {"papers": [], "returned": 0, "total_unclaimed": 0, "total_claimed": 0}

    def test_lists_papers_with_pdf_but_no_markdown(self, client):
        _put_pdf(client, "2501.00010v1")
        _put_pdf(client, "quant-ph/9508027v1")
        resp = client.get("/api/papers/needs-mineru")
        assert resp.status_code == 200
        body = resp.json()
        assert body["returned"] == 2
        assert body["total_unclaimed"] == 2
        assert body["total_claimed"] == 0
        keys = sorted(p["key"] for p in body["papers"])
        assert keys == ["2501.00010v1", "9508027v1"]
        assert all(not p["claimed"] for p in body["papers"])

    def test_excludes_papers_with_markdown(self, client):
        _put_pdf(client, "2501.00010v1")
        _put_pdf(client, "2501.00020v1")
        assert _put_markdown(client, "2501.00010v1").status_code == 201
        body = client.get("/api/papers/needs-mineru").json()
        assert [p["key"] for p in body["papers"]] == ["2501.00020v1"]

    def test_excludes_claimed_papers_by_default(self, client):
        _put_pdf(client, "2501.00010v1")
        _put_pdf(client, "2501.00020v1")
        claim = client.post("/api/papers/2501.00010v1/mineru-claim").json()
        assert "claim_id" in claim
        body = client.get("/api/papers/needs-mineru").json()
        assert [p["key"] for p in body["papers"]] == ["2501.00020v1"]
        assert body["total_unclaimed"] == 1
        assert body["total_claimed"] == 1

    def test_include_claimed_returns_everything(self, client):
        _put_pdf(client, "2501.00010v1")
        client.post("/api/papers/2501.00010v1/mineru-claim")
        body = client.get("/api/papers/needs-mineru?include_claimed=true").json()
        assert body["returned"] == 1
        assert body["papers"][0]["claimed"] is True
        assert body["papers"][0]["claim_expires_at"] is not None
        assert body["papers"][0]["claim_requester"] is None  # no USER_HEADER on claim call

    def test_limit_caps_output(self, client):
        for i in range(5):
            _put_pdf(client, f"2501.0001{i}v1")
        body = client.get("/api/papers/needs-mineru?limit=2").json()
        assert body["returned"] == 2
        assert body["total_unclaimed"] == 5


class TestClaimAndRelease:
    def test_claim_requires_pdf(self, client):
        resp = client.post("/api/papers/2501.00010v1/mineru-claim")
        assert resp.status_code == 404
        assert "upload-pdf" in resp.json()["detail"]

    def test_claim_refuses_if_markdown_exists(self, client):
        _put_pdf(client, "2501.00010v1")
        _put_markdown(client, "2501.00010v1")
        resp = client.post("/api/papers/2501.00010v1/mineru-claim")
        assert resp.status_code == 409
        assert "markdown already exists" in resp.json()["detail"]

    def test_claim_success_returns_pdf_share_url(self, client):
        _put_pdf(client, "quant-ph/9508027v1")
        resp = client.post(
            "/api/papers/quant-ph/9508027v1/mineru-claim",
            headers={"X-Forwarded-User": "alice"},
        )
        assert resp.status_code == 201, resp.text
        body = resp.json()
        assert body["arxiv_id"] == "quant-ph/9508027v1"
        assert body["key"] == "9508027v1"
        assert body["requester"] == "alice"
        assert body["ttl_seconds"] == 1800
        assert "9508027v1.pdf" in body["pdf_url"]
        assert body["pdf_url"].startswith("http://test.local/share/test-share-token/")
        # On disk: claim file exists
        claim_file = client._data_root / "mineru-claims" / "9508027v1.json"
        assert claim_file.is_file()

    def test_second_claim_returns_409_with_existing_metadata(self, client):
        _put_pdf(client, "2501.00010v1")
        first = client.post("/api/papers/2501.00010v1/mineru-claim").json()
        second = client.post("/api/papers/2501.00010v1/mineru-claim")
        assert second.status_code == 409
        detail = second.json()["detail"]
        assert detail["claim_id"] == first["claim_id"]
        assert detail["claim_expires_at"] == first["expires_at"]

    def test_expired_claim_is_replaceable(self, client):
        _put_pdf(client, "2501.00010v1")
        # Write a claim that already expired.
        claim_dir = client._data_root / "mineru-claims"
        claim_dir.mkdir(parents=True, exist_ok=True)
        expired = {
            "claim_id": "dead",
            "arxiv_id": "2501.00010v1",
            "expires_at": (datetime.now(timezone.utc) - timedelta(seconds=60)).isoformat(),
        }
        (claim_dir / "2501.00010v1.json").write_text(json.dumps(expired))

        fresh = client.post("/api/papers/2501.00010v1/mineru-claim")
        assert fresh.status_code == 201
        assert fresh.json()["claim_id"] != "dead"

    def test_release_with_matching_id(self, client):
        _put_pdf(client, "2501.00010v1")
        claim = client.post("/api/papers/2501.00010v1/mineru-claim").json()
        resp = client.delete(
            f"/api/papers/2501.00010v1/mineru-claim/{claim['claim_id']}"
        )
        assert resp.status_code == 204
        assert not (client._data_root / "mineru-claims" / "2501.00010v1.json").is_file()
        # Now a new claim should succeed.
        again = client.post("/api/papers/2501.00010v1/mineru-claim")
        assert again.status_code == 201

    def test_release_with_wrong_id_returns_409(self, client):
        _put_pdf(client, "2501.00010v1")
        client.post("/api/papers/2501.00010v1/mineru-claim")
        resp = client.delete("/api/papers/2501.00010v1/mineru-claim/notmyid")
        assert resp.status_code == 409

    def test_release_when_no_claim_is_204(self, client):
        # idempotent for the "client missed a previous successful release" case
        resp = client.delete("/api/papers/2501.00010v1/mineru-claim/anything")
        assert resp.status_code == 204

    def test_ttl_clamping(self, client):
        _put_pdf(client, "2501.00010v1")
        too_short = client.post("/api/papers/2501.00010v1/mineru-claim?ttl_seconds=5")
        assert too_short.status_code == 422
        client.delete("/api/papers/2501.00010v1/mineru-claim/none")  # nothing to clear
        too_long = client.post("/api/papers/2501.00020v1/mineru-claim?ttl_seconds=999999")
        # 422 (also no pdf -> 404 hits first, so let's set up properly)
        _put_pdf(client, "2501.00020v1")
        too_long = client.post("/api/papers/2501.00020v1/mineru-claim?ttl_seconds=999999")
        assert too_long.status_code == 422

    def test_custom_ttl_within_range(self, client):
        _put_pdf(client, "2501.00010v1")
        resp = client.post("/api/papers/2501.00010v1/mineru-claim?ttl_seconds=120")
        assert resp.status_code == 201
        assert resp.json()["ttl_seconds"] == 120


class TestUploadMarkdownReleasesClaim:
    def test_successful_upload_deletes_claim(self, client):
        _put_pdf(client, "2501.00010v1")
        client.post("/api/papers/2501.00010v1/mineru-claim")
        claim_file = client._data_root / "mineru-claims" / "2501.00010v1.json"
        assert claim_file.is_file()

        upload = _put_markdown(client, "2501.00010v1")
        assert upload.status_code == 201
        assert not claim_file.is_file()

    def test_failed_upload_keeps_claim(self, client):
        _put_pdf(client, "2501.00010v1")
        client.post("/api/papers/2501.00010v1/mineru-claim")
        # Empty markdown is rejected before write completes.
        resp = client.post(
            "/api/papers/2501.00010v1/upload-markdown",
            files={"markdown": ("p.md", io.BytesIO(b""), "text/markdown")},
            headers={"X-Forwarded-User": "alice"},
        )
        assert resp.status_code == 400
        # Claim must still be there so the contributor can retry.
        assert (client._data_root / "mineru-claims" / "2501.00010v1.json").is_file()
