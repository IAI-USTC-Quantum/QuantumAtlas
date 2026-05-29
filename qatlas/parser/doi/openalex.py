"""OpenAlex resolver — strict validation, no fuzzy match.

OpenAlex's `search` parameter on `/works` is title-aware but, like
Crossref, ranks by an internal score that we don't trust on its own.
Validation mirrors `crossref.py`: exact normalized-title match plus
author-last-name overlap (or year, when authors are missing). A hit is
`confidence=high` when both gates pass; `medium` if only title matched
and we lack author cross-check.

Polite-pool note: OpenAlex asks API consumers to add `mailto=<addr>` so
their throughput is routed to a faster pool. See
https://docs.openalex.org/how-to-use-the-api/rate-limits-and-authentication
for details.
"""

from __future__ import annotations

import logging
from typing import Any, Dict, List, Optional

import requests

from .protocol import (
    DOIMatch,
    PaperContext,
    author_last_names,
    normalize_doi,
    normalize_title,
)

logger = logging.getLogger(__name__)

API_URL = "https://api.openalex.org/works"
DEFAULT_TIMEOUT = 15.0
DEFAULT_PER_PAGE = 5


class OpenAlexResolver:
    """DOI resolver hitting OpenAlex's `/works?search=...` endpoint."""

    name = "openalex"

    def __init__(
        self,
        mailto: Optional[str] = None,
        session: Optional[requests.Session] = None,
        timeout: float = DEFAULT_TIMEOUT,
        per_page: int = DEFAULT_PER_PAGE,
        user_agent: str = "QuantumAtlas-DOIResolver/0.1 (+https://github.com/IAI-USTC-Quantum/QuantumAtlas)",
    ):
        self.mailto = mailto
        self.timeout = timeout
        self.per_page = per_page
        self.session = session or requests.Session()
        ua = user_agent
        if mailto and "mailto:" not in ua.lower():
            ua = f"{ua} (mailto:{mailto})"
        self.session.headers.setdefault("User-Agent", ua)
        self.session.headers.setdefault("Accept", "application/json")

    def resolve(self, paper: PaperContext) -> Optional[DOIMatch]:
        title = (paper.title or "").strip()
        if not title:
            return None
        params: Dict[str, Any] = {
            "search": title,
            "per_page": self.per_page,
            "select": "id,doi,title,authorships,publication_year,type",
        }
        if self.mailto:
            params["mailto"] = self.mailto

        try:
            resp = self.session.get(API_URL, params=params, timeout=self.timeout)
            resp.raise_for_status()
            payload = resp.json()
        except (requests.RequestException, ValueError) as exc:
            logger.warning("openalex: request failed for %s: %s", paper.arxiv_id, exc)
            return None

        items = payload.get("results") or []
        if not items:
            return None

        want_title = normalize_title(title)
        want_authors = set(author_last_names(paper.authors or []))

        best: Optional[DOIMatch] = None
        for item in items:
            doi = normalize_doi(str(item.get("doi") or ""))
            cand_title = item.get("title") or ""
            if not doi or not cand_title:
                continue
            if normalize_title(cand_title) != want_title:
                continue

            cand_authors = _openalex_author_lastnames(item.get("authorships") or [])
            cand_year = item.get("publication_year")
            try:
                cand_year = int(cand_year) if cand_year is not None else None
            except (TypeError, ValueError):
                cand_year = None

            if want_authors and cand_authors:
                if want_authors & cand_authors:
                    return DOIMatch(
                        doi=doi,
                        source="openalex",
                        confidence="high",
                        raw_record=_trim_openalex(item),
                    )
                continue

            if paper.year and cand_year and paper.year == cand_year:
                return DOIMatch(
                    doi=doi,
                    source="openalex",
                    confidence="high",
                    raw_record=_trim_openalex(item),
                )

            if best is None:
                best = DOIMatch(
                    doi=doi,
                    source="openalex",
                    confidence="medium",
                    raw_record=_trim_openalex(item),
                )
        return best


def _openalex_author_lastnames(authorships: List[Dict[str, Any]]) -> set:
    names: List[str] = []
    for a in authorships:
        author = a.get("author") or {}
        display = author.get("display_name")
        if display:
            names.append(display)
    return set(author_last_names(names))


def _trim_openalex(item: Dict[str, Any]) -> Dict[str, Any]:
    return {
        "id": item.get("id"),
        "doi": item.get("doi"),
        "title": item.get("title"),
        "publication_year": item.get("publication_year"),
        "type": item.get("type"),
        "authorships": [
            {"display_name": (a.get("author") or {}).get("display_name")}
            for a in (item.get("authorships") or [])[:8]
        ],
    }
