"""Tests for DOI resolvers (qatlas.parser.doi)."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any, Dict

import pytest

from qatlas.parser.doi import (
    ArxivSelfReportedResolver,
    ChainResolver,
    CrossrefResolver,
    DOIMatch,
    OpenAlexResolver,
    PaperContext,
    normalize_doi,
    normalize_title,
)


# ---------- helpers ---------------------------------------------------------


class DummyResponse:
    """Stand-in for `requests.Response` covering the surface we use."""

    def __init__(self, payload: Dict[str, Any], status_code: int = 200):
        self._payload = payload
        self.status_code = status_code

    def raise_for_status(self):
        if self.status_code >= 400:
            import requests
            raise requests.HTTPError(f"HTTP {self.status_code}")

    def json(self):
        return self._payload


def _make_session(payload: Dict[str, Any]):
    """Build a minimal session-like object whose `.get(...)` returns DummyResponse."""

    class _Sess:
        headers: Dict[str, str] = {}

        def get(self, url, params=None, timeout=None):
            self.last_call = {"url": url, "params": params, "timeout": timeout}
            return DummyResponse(payload)

    return _Sess()


# ---------- normalize_title / normalize_doi --------------------------------


class TestNormalize:
    def test_normalize_title_strips_latex_and_punctuation(self):
        # `\emph{quantum}` is fully removed (regex eats `\cmd{...}` including
        # the braced content); apostrophe in "Shor's" becomes a space-split,
        # so we end up with two tokens "shor" + "s". This is intentional —
        # we don't want false matches across "Shor" / "Shors" / "Shor's".
        assert normalize_title(r"Shor's Algorithm: \emph{quantum} factoring!") == \
            "shor s algorithm factoring"

    def test_normalize_title_handles_math(self):
        assert normalize_title(r"Computing $O(n^2)$ Eigenvalues") == \
            "computing eigenvalues"

    def test_normalize_doi_strips_doi_prefix(self):
        assert normalize_doi("https://doi.org/10.1103/PhysRevLett.103.150502") == \
            "10.1103/physrevlett.103.150502"
        assert normalize_doi("doi:10.1103/X") == "10.1103/x"

    def test_normalize_doi_empty(self):
        assert normalize_doi("") == ""
        assert normalize_doi("   ") == ""


# ---------- ArxivSelfReportedResolver --------------------------------------


class TestArxivSelfReported:
    def test_reads_doi_from_sidecar(self, tmp_path: Path):
        json_path = tmp_path / "1.json"
        json_path.write_text(json.dumps({"doi": "10.1103/PhysRevLett.103.150502"}))
        resolver = ArxivSelfReportedResolver(json_path_getter=lambda _aid: json_path)
        match = resolver.resolve(PaperContext(arxiv_id="0905.1234", title="t", authors=[]))
        assert match is not None
        assert match.doi == "10.1103/physrevlett.103.150502"
        assert match.source == "arxiv"
        assert match.confidence == "high"

    def test_missing_sidecar_returns_none(self, tmp_path: Path):
        resolver = ArxivSelfReportedResolver(json_path_getter=lambda _aid: tmp_path / "missing.json")
        assert resolver.resolve(PaperContext(arxiv_id="x", title="t", authors=[])) is None

    def test_sidecar_without_doi_returns_none(self, tmp_path: Path):
        json_path = tmp_path / "1.json"
        json_path.write_text(json.dumps({"title": "x"}))
        resolver = ArxivSelfReportedResolver(json_path_getter=lambda _aid: json_path)
        assert resolver.resolve(PaperContext(arxiv_id="x", title="t", authors=[])) is None


# ---------- CrossrefResolver ------------------------------------------------


class TestCrossref:
    def test_high_confidence_when_title_and_author_match(self):
        session = _make_session({
            "message": {
                "items": [
                    {
                        "DOI": "10.1103/PhysRevLett.103.150502",
                        "title": ["Quantum Algorithm For Linear Systems Of Equations"],
                        "author": [
                            {"family": "Harrow", "given": "A. W."},
                            {"family": "Hassidim", "given": "A."},
                        ],
                        "issued": {"date-parts": [[2009]]},
                    }
                ]
            }
        })
        resolver = CrossrefResolver(session=session)
        ctx = PaperContext(
            arxiv_id="0811.3171",
            title="Quantum algorithm for linear systems of equations",
            authors=["A. W. Harrow", "Avinatan Hassidim", "Seth Lloyd"],
            year=2009,
        )
        match = resolver.resolve(ctx)
        assert match is not None
        assert match.doi == "10.1103/physrevlett.103.150502"
        assert match.source == "crossref"
        assert match.confidence == "high"

    def test_title_mismatch_returns_none(self):
        session = _make_session({
            "message": {
                "items": [{
                    "DOI": "10.1/X",
                    "title": ["Totally Different Paper"],
                    "author": [{"family": "Harrow"}],
                }]
            }
        })
        resolver = CrossrefResolver(session=session)
        ctx = PaperContext(arxiv_id="x", title="Quantum widget paper", authors=["Harrow"])
        assert resolver.resolve(ctx) is None

    def test_title_match_but_authors_disjoint_skips_candidate(self):
        session = _make_session({
            "message": {
                "items": [{
                    "DOI": "10.1/X",
                    "title": ["Quantum Algorithm"],
                    "author": [{"family": "Smith"}],
                }]
            }
        })
        resolver = CrossrefResolver(session=session)
        ctx = PaperContext(arxiv_id="x", title="quantum algorithm", authors=["Harrow"])
        assert resolver.resolve(ctx) is None

    def test_medium_when_no_authors_either_side(self):
        session = _make_session({
            "message": {
                "items": [{
                    "DOI": "10.1/Y",
                    "title": ["Quantum Algorithm"],
                }]
            }
        })
        resolver = CrossrefResolver(session=session)
        ctx = PaperContext(arxiv_id="x", title="quantum algorithm", authors=[])
        match = resolver.resolve(ctx)
        assert match is not None
        assert match.confidence == "medium"

    def test_year_fallback_when_no_authors(self):
        session = _make_session({
            "message": {
                "items": [{
                    "DOI": "10.1/Y",
                    "title": ["Quantum Algorithm"],
                    "issued": {"date-parts": [[2017]]},
                }]
            }
        })
        resolver = CrossrefResolver(session=session)
        ctx = PaperContext(arxiv_id="x", title="quantum algorithm", authors=[], year=2017)
        match = resolver.resolve(ctx)
        assert match is not None
        assert match.confidence == "high"

    def test_request_failure_returns_none(self, monkeypatch):
        import requests

        class _Sess:
            headers: Dict[str, str] = {}

            def get(self, *a, **kw):
                raise requests.ConnectionError("down")

        resolver = CrossrefResolver(session=_Sess())
        ctx = PaperContext(arxiv_id="x", title="t", authors=[])
        assert resolver.resolve(ctx) is None

    def test_empty_title_returns_none(self):
        resolver = CrossrefResolver(session=_make_session({}))
        assert resolver.resolve(PaperContext(arxiv_id="x", title="", authors=[])) is None

    def test_mailto_passed_in_params_and_ua(self):
        session = _make_session({"message": {"items": []}})
        resolver = CrossrefResolver(session=session, mailto="hello@example.org")
        ctx = PaperContext(arxiv_id="x", title="t", authors=[])
        resolver.resolve(ctx)
        assert session.last_call["params"]["mailto"] == "hello@example.org"
        assert "mailto:hello@example.org" in session.headers.get("User-Agent", "").lower()


# ---------- OpenAlexResolver ------------------------------------------------


class TestOpenAlex:
    def test_high_confidence_with_full_doi_url(self):
        session = _make_session({
            "results": [{
                "id": "https://openalex.org/W123",
                "doi": "https://doi.org/10.1103/PhysRevLett.103.150502",
                "title": "Quantum Algorithm For Linear Systems Of Equations",
                "publication_year": 2009,
                "authorships": [
                    {"author": {"display_name": "Aram W. Harrow"}},
                    {"author": {"display_name": "Avinatan Hassidim"}},
                ],
            }]
        })
        resolver = OpenAlexResolver(session=session)
        ctx = PaperContext(
            arxiv_id="0811.3171",
            title="Quantum algorithm for linear systems of equations",
            authors=["Harrow", "Hassidim", "Lloyd"],
            year=2009,
        )
        match = resolver.resolve(ctx)
        assert match is not None
        assert match.doi == "10.1103/physrevlett.103.150502"
        assert match.source == "openalex"
        assert match.confidence == "high"

    def test_title_match_authors_disjoint_skipped(self):
        session = _make_session({
            "results": [{
                "doi": "https://doi.org/10.1/X",
                "title": "Quantum Algorithm",
                "publication_year": 2020,
                "authorships": [{"author": {"display_name": "Smith"}}],
            }]
        })
        resolver = OpenAlexResolver(session=session)
        ctx = PaperContext(arxiv_id="x", title="quantum algorithm", authors=["Harrow"])
        assert resolver.resolve(ctx) is None


# ---------- ChainResolver ---------------------------------------------------


class TestChain:
    def test_returns_first_high_confidence(self):
        class _R1:
            name = "r1"
            def resolve(self, paper):
                return DOIMatch(doi="10.1/A", source="r1", confidence="medium")

        class _R2:
            name = "r2"
            def resolve(self, paper):
                return DOIMatch(doi="10.1/B", source="r2", confidence="high")

        class _R3:
            name = "r3"
            def resolve(self, paper):
                raise AssertionError("must not be called after r2 returned high")

        chain = ChainResolver([_R1(), _R2(), _R3()])
        match = chain.resolve(PaperContext(arxiv_id="x", title="t", authors=[]))
        assert match is not None
        assert match.doi == "10.1/B"
        assert match.confidence == "high"

    def test_returns_first_medium_when_no_high(self):
        class _R1:
            name = "r1"
            def resolve(self, paper):
                return None

        class _R2:
            name = "r2"
            def resolve(self, paper):
                return DOIMatch(doi="10.1/A", source="r2", confidence="medium")

        class _R3:
            name = "r3"
            def resolve(self, paper):
                return DOIMatch(doi="10.1/B", source="r3", confidence="medium")

        chain = ChainResolver([_R1(), _R2(), _R3()])
        match = chain.resolve(PaperContext(arxiv_id="x", title="t", authors=[]))
        assert match is not None
        assert match.doi == "10.1/A"  # first medium wins

    def test_swallows_resolver_exceptions(self):
        class _Broken:
            name = "broken"
            def resolve(self, paper):
                raise RuntimeError("oops")

        class _Good:
            name = "good"
            def resolve(self, paper):
                return DOIMatch(doi="10.1/Z", source="good", confidence="high")

        chain = ChainResolver([_Broken(), _Good()])
        match = chain.resolve(PaperContext(arxiv_id="x", title="t", authors=[]))
        assert match is not None
        assert match.doi == "10.1/Z"

    def test_all_none_returns_none(self):
        class _Null:
            name = "n"
            def resolve(self, paper):
                return None

        chain = ChainResolver([_Null(), _Null()])
        assert chain.resolve(PaperContext(arxiv_id="x", title="t", authors=[])) is None

    def test_empty_chain_raises(self):
        with pytest.raises(ValueError):
            ChainResolver([])
