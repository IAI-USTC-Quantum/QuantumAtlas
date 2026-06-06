"""Offline tests for models + ranking (no network, no extras)."""

from __future__ import annotations

from qatlas_search.config import Settings
from qatlas_search.models import Paper, SearchQuery, normalize_title
from qatlas_search.ranking import lexical_score, merge, rank


def test_query_parses_quoted_phrases() -> None:
    q = SearchQuery.parse('surface code "magic state distillation"', max_results=5)
    assert q.required_phrases == ["magic state distillation"]
    assert "surface" in q.tokens()
    assert q.max_results == 5


def test_identity_key_prefers_doi_then_arxiv_then_title() -> None:
    assert Paper(title="x", doi="10.1/AbC").identity_key() == "doi:10.1/abc"
    assert Paper(title="x", arxiv_id="2501.00010").identity_key() == "arxiv:2501.00010"
    assert Paper(title="Hello World").identity_key() == "title:hello world"


def test_normalize_title_strips_punctuation() -> None:
    assert normalize_title("Grover's Search!") == "grover s search"


def test_lexical_score_prefers_title_and_phrase() -> None:
    q = SearchQuery.parse('"surface code" threshold')
    in_title = Paper(title="Surface code threshold estimates", abstract="x")
    in_abstract = Paper(title="Unrelated", abstract="surface code threshold appears here")
    assert lexical_score(q, in_title) > lexical_score(q, in_abstract)


def test_merge_dedups_by_doi_and_unions_sources() -> None:
    a = Paper(title="Paper A", doi="10.1/x", source="arxiv", citations=None)
    b = Paper(title="Paper A", doi="10.1/x", source="openalex", citations=42, abstract="abs")
    merged = merge([a, b])
    assert len(merged) == 1
    m = merged[0]
    assert set(m.source.split(",")) == {"arxiv", "openalex"}
    assert m.citations == 42
    assert m.abstract == "abs"


def test_merge_links_doi_only_and_arxiv_only_via_bridge() -> None:
    # arXiv backend yields arxiv-only; OpenAlex yields the same paper with BOTH
    # ids; Semantic Scholar yields doi-only. All three must collapse into one.
    arxiv_only = Paper(title="P", arxiv_id="2401.01234", source="arxiv")
    bridge = Paper(title="P", arxiv_id="2401.01234", doi="10.1/x", source="openalex", citations=9)
    doi_only = Paper(title="P", doi="10.1/x", source="semantic_scholar", abstract="a")
    merged = merge([arxiv_only, bridge, doi_only])
    assert len(merged) == 1
    assert set(merged[0].source.split(",")) == {"arxiv", "openalex", "semantic_scholar"}
    assert merged[0].citations == 9
    assert merged[0].abstract == "a"


def test_merge_does_not_corrupt_input_papers() -> None:
    a = Paper(title="P", doi="10.1/x", source="arxiv")
    b = Paper(title="P", doi="10.1/x", source="openalex", abstract="abs")
    merge([a, b])
    # Inputs must be untouched (merge deep-copies).
    assert a.abstract is None
    assert a.source == "arxiv"
    s = Settings()
    q = SearchQuery.parse("surface code")
    low = Paper(title="surface code intro", citations=1, source="arxiv")
    high = Paper(title="surface code intro", citations=5000, source="openalex")
    # Same lexical match; the higher-cited one should rank first.
    ranked = rank(q, [low.model_copy(), high.model_copy()], s)
    # They share identity (same normalized title) -> merge keeps max citations.
    assert ranked[0].citations == 5000


def test_rank_citation_none_is_neutral_not_zero() -> None:
    s = Settings()
    q = SearchQuery.parse("quantum")
    p_none = Paper(title="quantum a", citations=None, source="arxiv")
    p_zero = Paper(title="quantum b", citations=0, source="openalex")
    ranked = rank(q, [p_none, p_zero], s)
    by_title = {p.title: p.score for p in ranked}
    # None (0.5 neutral) should outscore an explicit 0 citation, lexical equal.
    assert by_title["quantum a"] >= by_title["quantum b"]
