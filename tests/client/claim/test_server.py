"""FastAPI wiring + idempotency-refuse tests for the Claim WebUI.

Skipped unless the optional ``contrib`` extras (fastapi + a TestClient HTTP
backend) are importable, so the default test run without the extra still passes.
"""

from __future__ import annotations

import pytest

pytest.importorskip("fastapi")
pytest.importorskip("httpx")

from fastapi.testclient import TestClient  # noqa: E402

from qatlas.client.claim.drafter import ClaimDrafter  # noqa: E402
from qatlas.client.claim.gitea import GiteaClient, GiteaConfig  # noqa: E402
from qatlas.client.claim.models import PaperRef  # noqa: E402
from qatlas.client.claim.server import SessionState, build_app  # noqa: E402
from tests.client.claim.test_gitea import FakeSession  # noqa: E402


def _state(gitea_session) -> SessionState:
    paper = PaperRef(id="2208.06941", doi_or_arxiv="2208.06941", title="A Paper")
    cfg = GiteaConfig(base_url="https://g", repo="agony/qatlas-lean", token="t")
    drafter = ClaimDrafter(paper)  # manual mode (no API key in tests)
    return SessionState(
        paper=paper,
        markdown="# A Paper\n\nbody",
        base_url="https://s",
        auth_headers={},
        verify=True,
        gitea=GiteaClient(cfg, session=gitea_session),
        drafter=drafter,
    )


def _claim_payload():
    return {
        "claim": {
            "claim_id": "2208.06941:main",
            "paper": {"id": "2208.06941", "doi_or_arxiv": "2208.06941", "title": "A Paper", "authors": []},
            "claim_kind": "theorem",
            "natural_language": "The thing holds.",
            "references": [],
        },
        "resolved": [],
    }


def test_session_endpoint():
    client = TestClient(build_app(_state(FakeSession())))
    r = client.get("/api/session")
    assert r.status_code == 200
    data = r.json()
    assert data["paper"]["id"] == "2208.06941"
    assert data["gitea_repo"] == "agony/qatlas-lean"
    assert data["agent_available"] is False  # manual mode


def test_draft_manual_mode_400():
    client = TestClient(build_app(_state(FakeSession())))
    r = client.post("/api/draft", json={"hint": ""})
    assert r.status_code == 400


def test_preview_renders_title_and_body():
    client = TestClient(build_app(_state(FakeSession())))
    r = client.post("/api/preview", json=_claim_payload())
    assert r.status_code == 200
    body = r.json()
    assert body["title"].startswith("thm: ")
    assert body["body"].strip().endswith("claim_id: 2208.06941:main")


def test_confirm_files_issue_when_no_duplicate():
    sess = FakeSession(
        issues_pages=[[]],  # no existing open issue
        labels=[{"name": "type/theorem", "id": 1}, {"name": "status/ready", "id": 2}],
        create_resp={"number": 42, "html_url": "https://g/agony/qatlas-lean/issues/42"},
    )
    client = TestClient(build_app(_state(sess)))
    r = client.post("/api/confirm", json=_claim_payload())
    assert r.status_code == 200
    assert r.json()["number"] == 42
    # The filed issue body carries the trailing claim_id marker + contrib labels.
    assert sess.posted[0]["labels"] == [1, 2]
    assert "claim_id: 2208.06941:main" in sess.posted[0]["body"]


def test_confirm_refuses_with_url_on_open_duplicate():
    sess = FakeSession(
        issues_pages=[[{"number": 9, "body": "claim_id: 2208.06941:main", "html_url": "https://g/x/issues/9"}]],
    )
    client = TestClient(build_app(_state(sess)))
    r = client.post("/api/confirm", json=_claim_payload())
    assert r.status_code == 409
    assert r.json()["existing_url"] == "https://g/x/issues/9"
    # Must NOT have created a duplicate.
    assert sess.posted == []


def test_check_endpoint():
    sess = FakeSession(issues_pages=[[{"number": 9, "body": "claim_id: 2208.06941:main", "html_url": "u9"}]])
    client = TestClient(build_app(_state(sess)))
    r = client.post("/api/check", json={"claim_id": "2208.06941:main"})
    assert r.json() == {"exists": True, "url": "u9", "number": 9}
