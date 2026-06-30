"""Tests for the gitea client: idempotency scan + issue creation (ADR 0005)."""

from __future__ import annotations

import json as _json

from qatlas.client.claim.gitea import GiteaClient, GiteaConfig


class FakeResponse:
    def __init__(self, status_code: int, payload=None, text: str = ""):
        self.status_code = status_code
        self._payload = payload
        self.text = text or _json.dumps(payload or {})

    def json(self):
        return self._payload


class FakeSession:
    """Routes GET/POST by URL substring; records calls for assertions."""

    def __init__(self, issues_pages=None, labels=None, create_resp=None):
        self.issues_pages = issues_pages or [[]]
        self.labels = labels if labels is not None else []
        self.create_resp = create_resp or {"number": 7, "html_url": "https://g/x/issues/7"}
        self.posted = []

    def get(self, url, headers=None, params=None, verify=True, timeout=None):
        if url.endswith("/labels"):
            return FakeResponse(200, self.labels)
        if url.endswith("/issues"):
            page = (params or {}).get("page", 1)
            items = self.issues_pages[page - 1] if page - 1 < len(self.issues_pages) else []
            return FakeResponse(200, items)
        return FakeResponse(404, {})

    def post(self, url, headers=None, json=None, verify=True, timeout=None):
        self.posted.append(json)
        return FakeResponse(201, self.create_resp)


def _client(session) -> GiteaClient:
    cfg = GiteaConfig(base_url="https://g", repo="agony/qatlas-lean", token="t", verify=True)
    return GiteaClient(cfg, session=session)


def test_find_open_issue_matches_claim_id_line_prefix():
    sess = FakeSession(
        issues_pages=[[
            {"number": 1, "body": "## Claim\nfoo\n\nclaim_id: other:slug", "html_url": "u1"},
            {"number": 2, "body": "bar\n\nclaim_id: 2208.06941:main-thm", "html_url": "u2"},
        ]]
    )
    hit = _client(sess).find_open_issue_by_claim_id("2208.06941:main-thm")
    assert hit and hit["number"] == 2


def test_find_open_issue_matches_legacy_unit_id():
    # An issue filed before the rename (trailing ``unit_id:``) must still be
    # caught by the idempotency check so we don't file a duplicate.
    sess = FakeSession(
        issues_pages=[[{"number": 3, "body": "old\n\nunit_id: 2208.06941:main-thm", "html_url": "u3"}]]
    )
    hit = _client(sess).find_open_issue_by_claim_id("2208.06941:main-thm")
    assert hit and hit["number"] == 3


def test_find_open_issue_none_when_absent():
    sess = FakeSession(issues_pages=[[{"number": 1, "body": "no marker here", "html_url": "u"}]])
    assert _client(sess).find_open_issue_by_claim_id("x:y") is None


def test_find_open_issue_substring_not_matched():
    # "claim_id: a:b-extra" must NOT match a query for "a:b".
    sess = FakeSession(issues_pages=[[{"number": 1, "body": "claim_id: a:b-extra", "html_url": "u"}]])
    assert _client(sess).find_open_issue_by_claim_id("a:b") is None


def test_create_issue_resolves_label_ids():
    sess = FakeSession(
        labels=[{"name": "type/theorem", "id": 10}, {"name": "status/ready", "id": 11}, {"name": "other", "id": 99}],
    )
    out = _client(sess).create_issue("thm: x", "body\n\nclaim_id: p:s", ["type/theorem", "status/ready"])
    assert out["number"] == 7
    assert sess.posted[0]["labels"] == [10, 11]
    assert sess.posted[0]["title"] == "thm: x"


def test_create_issue_skips_unknown_labels():
    sess = FakeSession(labels=[{"name": "type/theorem", "id": 10}])
    _client(sess).create_issue("t", "b", ["type/theorem", "status/ready"])
    # Only the known label id is sent; missing one is skipped, not fatal.
    assert sess.posted[0]["labels"] == [10]
