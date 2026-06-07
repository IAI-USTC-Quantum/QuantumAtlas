"""Lexical + citation ranking — the academic prior.

The complaint that motivated this package: vector similarity matches concepts
badly for literature lookup. So the composite score here is deliberately
*lexical-first*:

    score = w_lex * lexical(query, title+abstract)
          + w_cite * citation(normalized log citations)
          + w_recency * recency

with defaults that weight exact term/phrase overlap above citations above
recency. Each component is normalized to roughly [0, 1] so the weights are
interpretable.

Citation normalization is done *within the merged result set* (min-max over
log1p(citations)) rather than against some global constant, because absolute
citation counts vary by sub-field and age; ranking only needs relative order.
"""

from __future__ import annotations

import math
from collections import defaultdict

from qatlas_search.config import Settings
from qatlas_search.models import Paper, SearchQuery, _WORD_RE, normalize_title


def _tokens(text: str) -> list[str]:
    return _WORD_RE.findall(text.lower())


def lexical_score(query: SearchQuery, paper: Paper) -> float:
    """Fraction of query tokens present, with exact-title and phrase boosts.

    Returns a value in [0, ~1.5]. Title hits count double (a term in the title
    is a far stronger relevance signal than one buried in the abstract), and
    each verbatim required-phrase match adds a flat boost.
    """
    q_tokens = query.tokens()
    if not q_tokens:
        return 0.0

    title = paper.title.lower()
    abstract = (paper.abstract or "").lower()
    title_tokens = set(_tokens(title))
    abstract_tokens = set(_tokens(abstract))

    hits = 0.0
    for tok in q_tokens:
        if tok in title_tokens:
            hits += 1.0  # title match: full weight
        elif tok in abstract_tokens:
            hits += 0.5  # abstract-only match: half weight
    base = hits / len(q_tokens)

    # Verbatim phrase boosts (the reason quoted queries exist).
    phrase_boost = 0.0
    for phrase in query.required_phrases:
        p = phrase.lower().strip()
        if not p:
            continue
        if p in title:
            phrase_boost += 0.5
        elif p in abstract:
            phrase_boost += 0.25

    return base + phrase_boost


def _citation_scores(papers: list[Paper]) -> dict[int, float]:
    """Min-max normalized log1p(citations) keyed by id(paper).

    Papers whose source does not report citations (``citations is None``, e.g.
    arXiv) get a neutral 0.5 so they are neither rewarded nor punished for the
    missing signal.
    """
    logs = {id(p): math.log1p(p.citations) for p in papers if p.citations is not None}
    if not logs:
        return {id(p): 0.0 for p in papers}
    lo, hi = min(logs.values()), max(logs.values())
    span = hi - lo
    out: dict[int, float] = {}
    for p in papers:
        if p.citations is None:
            out[id(p)] = 0.5
        elif span == 0:
            # Every reported count is identical -> citations carry no
            # discriminating signal; stay neutral instead of saturating to 1.0.
            out[id(p)] = 0.5
        else:
            out[id(p)] = (logs[id(p)] - lo) / span
    return out


def _recency_scores(papers: list[Paper]) -> dict[int, float]:
    years = {id(p): p.year for p in papers if p.year}
    if not years:
        return {id(p): 0.0 for p in papers}
    lo, hi = min(years.values()), max(years.values())
    span = hi - lo
    out: dict[int, float] = {}
    for p in papers:
        if not p.year:
            out[id(p)] = 0.0
        elif span == 0:
            out[id(p)] = 1.0
        else:
            out[id(p)] = (p.year - lo) / span
    return out


def _paper_keys(p: Paper) -> list[str]:
    """All identity keys a paper carries (it may have several).

    A paper can hold *both* a DOI and an arXiv id; returning both lets two
    records that each expose only one of them still merge (very common: an
    arXiv-only preprint hit vs. an OpenAlex hit carrying the journal DOI for the
    same work). Title is only a fallback when no strong id is present.
    """
    keys: list[str] = []
    if p.doi:
        keys.append("doi:" + p.doi.lower().strip())
    if p.arxiv_id:
        keys.append("arxiv:" + p.arxiv_id.lower().strip())
    if not keys:
        keys.append("title:" + normalize_title(p.title))
    return keys


def merge(papers: list[Paper]) -> list[Paper]:
    """Collapse duplicate papers found by multiple backends.

    Records are grouped by a union-find over *any* shared strong id (DOI or
    arXiv), with normalized title as a weak fallback key. So a DOI-only record
    and an arXiv-only record for the same paper still merge as long as some
    third record (or one of them) bridges the two ids. The merged record keeps
    the richest field from any source (present abstract, max citation count,
    lowest raw_rank) and records every contributing source.
    """
    n = len(papers)
    parent = list(range(n))

    def find(x: int) -> int:
        while parent[x] != x:
            parent[x] = parent[parent[x]]
            x = parent[x]
        return x

    def union(a: int, b: int) -> None:
        ra, rb = find(a), find(b)
        if ra != rb:
            parent[max(ra, rb)] = min(ra, rb)

    key_to_idx: dict[str, int] = {}
    for i, p in enumerate(papers):
        for k in _paper_keys(p):
            if k in key_to_idx:
                union(key_to_idx[k], i)
            else:
                key_to_idx[k] = i

    groups: dict[int, list[Paper]] = defaultdict(list)
    order: list[int] = []
    for i in range(n):
        r = find(i)
        if r not in groups:
            order.append(r)
        groups[r].append(papers[i])

    merged: list[Paper] = []
    for r in order:
        members = groups[r]
        best = members[0].model_copy(deep=True)
        sources: list[str] = []
        for m in members:
            for s in m.source.split(","):
                s = s.strip()
                if s and s not in sources:
                    sources.append(s)
            if not best.abstract and m.abstract:
                best.abstract = m.abstract
            if not best.doi and m.doi:
                best.doi = m.doi
            if not best.arxiv_id and m.arxiv_id:
                best.arxiv_id = m.arxiv_id
            if not best.url and m.url:
                best.url = m.url
            if not best.venue and m.venue:
                best.venue = m.venue
            if not best.year and m.year:
                best.year = m.year
            if m.citations is not None and (best.citations is None or m.citations > best.citations):
                best.citations = m.citations
            if m.raw_rank is not None and (best.raw_rank is None or m.raw_rank < best.raw_rank):
                best.raw_rank = m.raw_rank
            if len(m.authors) > len(best.authors):
                best.authors = m.authors
        best.source = ",".join(sources)
        merged.append(best)
    return merged


def rank(query: SearchQuery, papers: list[Paper], settings: Settings) -> list[Paper]:
    """Merge duplicates, compute the composite score, return sorted desc.

    Cross-source agreement (a paper found by N>1 backends) gets a small log
    bonus — independent corroboration is itself a relevance signal.
    """
    merged = merge(papers)
    cite = _citation_scores(merged)
    rec = _recency_scores(merged)
    for p in merged:
        lex = lexical_score(query, p)
        n_sources = len([s for s in p.source.split(",") if s.strip()])
        agreement = math.log1p(max(n_sources - 1, 0)) * 0.15
        p.score = (
            settings.weight_lexical * lex
            + settings.weight_citation * cite[id(p)]
            + settings.weight_recency * rec[id(p)]
            + agreement
        )
    merged.sort(key=lambda p: p.score, reverse=True)
    return merged
