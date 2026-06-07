"""Offline parser tests for each backend, plus opt-in live `network` tests.

The ``_parse`` methods are pure (payload -> list[Paper]) so we test them with
canned fixtures and no network. The live calls at the bottom are marked
``network`` and excluded by CI (`-m "not network and not e2e"`).
"""

from __future__ import annotations

import pytest

from qatlas_search.backends.arxiv import ArxivBackend
from qatlas_search.backends.crossref import CrossrefBackend
from qatlas_search.backends.internal import InternalBackend
from qatlas_search.backends.openalex import OpenAlexBackend, _reconstruct_abstract
from qatlas_search.backends.semantic_scholar import SemanticScholarBackend
from qatlas_search.config import Settings
from qatlas_search.models import SearchQuery

_ARXIV_XML = """<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <id>http://arxiv.org/abs/2501.00010v2</id>
    <published>2025-01-03T00:00:00Z</published>
    <title>Surface code threshold improvements</title>
    <summary>We study the surface code.</summary>
    <author><name>Ada Lovelace</name></author>
    <author><name>Alan Turing</name></author>
  </entry>
</feed>"""


def test_arxiv_parse() -> None:
    papers = ArxivBackend()._parse(_ARXIV_XML)
    assert len(papers) == 1
    p = papers[0]
    assert p.title == "Surface code threshold improvements"
    assert p.arxiv_id == "2501.00010"
    assert p.year == 2025
    assert p.citations is None
    assert p.authors == ["Ada Lovelace", "Alan Turing"]
    assert p.source == "arxiv"


def test_arxiv_build_query_uses_quoted_phrases() -> None:
    q = SearchQuery.parse('"magic state" distillation')
    built = ArxivBackend()._build_query(q)
    assert 'all:"magic state"' in built
    assert "all:distillation" in built


def test_arxiv_search_url_encodes_unsafe_terms() -> None:
    """A token with URL-significant chars must be percent-encoded so it cannot
    terminate the search_query value early; the all:/+AND+ structure stays.
    """
    b = ArxivBackend()
    url = b._search_url(SearchQuery.parse("spin & charge"))
    assert "&" not in url.split("search_query=", 1)[1]  # no raw & in the query value
    assert "%26" in url  # the literal ampersand survived as an encoded term
    # field prefixes and the AND operator are preserved
    assert "all:" in url
    assert "+AND+" in url

    phrase_url = b._search_url(SearchQuery.parse('"magic state" distillation'))
    assert "%22" in phrase_url  # quotes encoded
    assert "+AND+" in phrase_url


def test_openalex_reconstruct_abstract() -> None:
    inverted = {"Surface": [0], "code": [1], "works": [2]}
    assert _reconstruct_abstract(inverted) == "Surface code works"
    assert _reconstruct_abstract(None) is None


def test_openalex_parse() -> None:
    data = {
        "results": [
            {
                "display_name": "A cited paper",
                "publication_year": 2020,
                "cited_by_count": 123,
                "doi": "https://doi.org/10.1/xyz",
                "relevance_score": 9.5,
                "abstract_inverted_index": {"Hello": [0], "world": [1]},
                "authorships": [{"author": {"display_name": "Grace Hopper"}}],
                "primary_location": {"source": {"display_name": "Nature"}},
                "locations": [
                    {
                        "source": {"display_name": "arXiv"},
                        "landing_page_url": "https://arxiv.org/abs/2001.00001",
                    }
                ],
                "id": "https://openalex.org/W1",
            }
        ]
    }
    p = OpenAlexBackend()._parse(data)[0]
    assert p.citations == 123
    assert p.doi == "10.1/xyz"
    assert p.arxiv_id == "2001.00001"
    assert p.abstract == "Hello world"
    assert p.venue == "Nature"
    assert p.authors == ["Grace Hopper"]


def test_semantic_scholar_parse() -> None:
    data = {
        "data": [
            {
                "title": "Quantum error correction",
                "abstract": "abc",
                "year": 2019,
                "citationCount": 500,
                "externalIds": {"DOI": "10.2/qec", "ArXiv": "1907.11111"},
                "authors": [{"name": "John Preskill"}],
                "venue": "PRX Quantum",
                "url": "https://s2.org/p1",
            }
        ]
    }
    p = SemanticScholarBackend()._parse(data)[0]
    assert p.citations == 500
    assert p.doi == "10.2/qec"
    assert p.arxiv_id == "1907.11111"
    assert p.venue == "PRX Quantum"


def test_crossref_parse_strips_abstract_tags() -> None:
    data = {
        "message": {
            "items": [
                {
                    "title": ["Cross disc paper"],
                    "issued": {"date-parts": [[2018, 5]]},
                    "is-referenced-by-count": 7,
                    "DOI": "10.3/cd",
                    "abstract": "<jats:p>Body text</jats:p>",
                    "author": [{"given": "Lise", "family": "Meitner"}],
                    "container-title": ["Physica"],
                    "URL": "https://doi.org/10.3/cd",
                }
            ]
        }
    }
    p = CrossrefBackend()._parse(data)[0]
    assert p.citations == 7
    assert p.abstract == "Body text"
    assert p.authors == ["Lise Meitner"]
    assert p.venue == "Physica"


def test_internal_cypher_is_injection_safe_and_tokenized() -> None:
    # query tokens are [a-z0-9]+ only, so the inlined Cypher can't be broken out
    q = SearchQuery.parse("surface code'; DROP")
    cypher = InternalBackend()._build_cypher(q)
    assert "DROP" not in cypher  # punctuation/keywords dropped by tokenizer
    assert "toLower(p.title) CONTAINS 'surface'" in cypher
    assert "LIMIT" not in cypher  # server appends LIMIT itself


def test_internal_unavailable_without_server_or_wikidir(monkeypatch) -> None:
    s = Settings(server_url=None, token=None, wiki_dir=None)
    b = InternalBackend()
    # No token resolvable in a bare test env -> not available.
    monkeypatch.setattr(
        "qatlas_search.backends.internal._resolve_server",
        lambda settings: (None, None),
    )
    assert b.available(s) is False


# --- opt-in live calls (excluded by CI) ------------------------------------
@pytest.mark.network
def test_arxiv_live() -> None:
    papers = ArxivBackend().search(SearchQuery.parse("surface code", max_results=3), Settings())
    assert papers and papers[0].title


@pytest.mark.network
def test_openalex_live() -> None:
    papers = OpenAlexBackend().search(
        SearchQuery.parse("quantum error correction", max_results=3), Settings()
    )
    assert papers and any(p.citations is not None for p in papers)
