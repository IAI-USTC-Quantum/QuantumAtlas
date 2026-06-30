"""Tests for reference resolution: server lookup + public-OpenAlex fallback."""

from __future__ import annotations

from qatlas.client.claim.references import resolve_references


class FakeResponse:
    def __init__(self, status_code: int, payload=None):
        self.status_code = status_code
        self._payload = payload

    def json(self):
        return self._payload


class FakeSession:
    def __init__(self, handler):
        self._handler = handler

    def get(self, url, headers=None, params=None, verify=True, timeout=None):
        return self._handler(url, params or {})


def test_server_lookup_resolved_passthrough():
    def handler(url, params):
        assert "/api/papers/lookup" in url
        assert params["ids"] == "arxiv:1,openalex:W2"
        return FakeResponse(200, {
            "corpus_available": True,
            "results": [
                {"ref": "arxiv:1", "title": "T1", "year": 2021, "hosted": True, "resolved": True},
                {"ref": "openalex:W2", "resolved": False},
            ],
        })

    out = resolve_references("https://s", {}, True, ["arxiv:1", "openalex:W2"], session=FakeSession(handler))
    assert out[0].ref == "arxiv:1" and out[0].resolved and out[0].hosted and out[0].title == "T1"
    assert out[1].ref == "openalex:W2" and not out[1].resolved


def test_fallback_to_public_openalex_when_corpus_down():
    calls = {"openalex_hits": 0}

    def handler(url, params):
        if "/api/papers/lookup" in url:
            return FakeResponse(200, {
                "corpus_available": False,
                "results": [{"ref": "openalex:W9", "resolved": False, "hosted": False}],
            })
        if "api.openalex.org/works/W9" in url:
            calls["openalex_hits"] += 1
            return FakeResponse(200, {"title": "Public Work", "publication_year": 2019, "authorships": []})
        return FakeResponse(404, {})

    out = resolve_references("https://s", {}, True, ["openalex:W9"], session=FakeSession(handler))
    assert calls["openalex_hits"] == 1
    assert out[0].resolved and out[0].title == "Public Work" and out[0].year == 2019


def test_no_fallback_when_corpus_available():
    def handler(url, params):
        if "/api/papers/lookup" in url:
            return FakeResponse(200, {"corpus_available": True, "results": [{"ref": "openalex:W9", "resolved": False}]})
        raise AssertionError("must not hit public OpenAlex when corpus is available")

    out = resolve_references("https://s", {}, True, ["openalex:W9"], session=FakeSession(handler))
    assert not out[0].resolved


def test_empty_input():
    assert resolve_references("https://s", {}, True, [], session=FakeSession(lambda *a: None)) == []


def test_server_unreachable_degrades():
    import requests

    class Boom:
        def get(self, *a, **k):
            raise requests.RequestException("down")

    out = resolve_references("https://s", {}, True, ["arxiv:1"], session=Boom())
    assert len(out) == 1 and out[0].ref == "arxiv:1" and not out[0].resolved
