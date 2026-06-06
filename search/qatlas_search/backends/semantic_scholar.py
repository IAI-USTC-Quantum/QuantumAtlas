"""Semantic Scholar backend — free; optional API key raises the rate limit.

S2's relevance search is strong for term matching and reports ``citationCount``
plus cross-source ids (DOI, arXiv). Without a key the public endpoint is heavily
rate-limited (frequent HTTP 429); we surface that as ``last_error`` and return
``[]`` rather than blocking the other backends. Set ``QATLAS_SEARCH_SEMANTIC_
SCHOLAR_API_KEY`` for reliable use.
"""

from __future__ import annotations

from qatlas_search.backends.base import COST_MEDIUM, Backend
from qatlas_search.config import Settings
from qatlas_search.models import Paper, SearchQuery

_S2_SEARCH = "https://api.semanticscholar.org/graph/v1/paper/search"
_FIELDS = "title,abstract,year,citationCount,externalIds,authors,venue,url"


class SemanticScholarBackend(Backend):
    name = "semantic_scholar"
    # Usable without a key, but rate-limited; not a hard requirement.
    requires_key = False
    cost_tier = COST_MEDIUM

    def search(self, query: SearchQuery, settings: Settings) -> list[Paper]:
        self.last_error = None
        params = {
            "query": query.text,
            "limit": query.max_results,
            "fields": _FIELDS,
        }
        headers = {}
        if settings.semantic_scholar_api_key:
            headers["x-api-key"] = settings.semantic_scholar_api_key
        try:
            resp = self._get(settings, _S2_SEARCH, params=params, headers=headers)
            return self._parse(resp.json())
        except Exception as exc:  # noqa: BLE001
            self.last_error = f"{type(exc).__name__}: {exc}"
            return []

    def _parse(self, data: dict) -> list[Paper]:
        out: list[Paper] = []
        for i, p in enumerate(data.get("data", []) or []):
            title = p.get("title")
            if not title:
                continue
            ext = p.get("externalIds") or {}
            doi = ext.get("DOI")
            arxiv_id = ext.get("ArXiv")
            authors = [a.get("name", "") for a in (p.get("authors") or [])]
            out.append(
                Paper(
                    title=title,
                    authors=[a for a in authors if a],
                    year=p.get("year"),
                    abstract=p.get("abstract"),
                    venue=p.get("venue") or None,
                    citations=p.get("citationCount"),
                    url=p.get("url"),
                    doi=doi,
                    arxiv_id=arxiv_id,
                    source=self.name,
                    raw_rank=i + 1,
                )
            )
        return out
