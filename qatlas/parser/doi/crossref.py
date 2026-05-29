"""Crossref `query.bibliographic` resolver — strict validation, no fuzzy match.

Crossref's `query.bibliographic` accepts a title-ish string and returns up
to N hits ranked by their internal relevance score. We deliberately do
*not* trust that score: a Levenshtein-2 title match for a different paper
will happily come back as the top hit. Instead, each candidate must clear
two gates before we accept it:

  1. `normalize_title(candidate) == normalize_title(paper.title)`
     (strict equality after stripping LaTeX, punctuation, casing)
  2. At least one author last-name overlaps between paper.authors and the
     candidate's `author` field, OR (if neither side has authors) the
     candidate publication year matches `paper.year`.

A passing candidate is returned with `confidence=high` when both title and
author cross-check; `medium` if only title matched (no author data
available either side). Otherwise we return None and let the chain try the
next resolver.

Polite-pool note: Crossref strongly recommends sending `mailto=<addr>` —
either as a query param or in the User-Agent — to be routed to their
faster polite tier. Default UA includes the project name so abuse can be
traced. See https://api.crossref.org/swagger-ui/ for the API spec.
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

API_URL = "https://api.crossref.org/works"
DEFAULT_TIMEOUT = 15.0
DEFAULT_ROWS = 5


class CrossrefResolver:
    """DOI resolver hitting Crossref's REST search endpoint."""

    name = "crossref"

    def __init__(
        self,
        mailto: Optional[str] = None,
        session: Optional[requests.Session] = None,
        timeout: float = DEFAULT_TIMEOUT,
        rows: int = DEFAULT_ROWS,
        user_agent: str = "QuantumAtlas-DOIResolver/0.1 (+https://github.com/IAI-USTC-Quantum/QuantumAtlas)",
    ):
        self.mailto = mailto
        self.timeout = timeout
        self.rows = rows
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
            "query.bibliographic": title,
            "rows": self.rows,
            "select": "DOI,title,author,issued,type",
        }
        if self.mailto:
            params["mailto"] = self.mailto

        try:
            resp = self.session.get(API_URL, params=params, timeout=self.timeout)
            resp.raise_for_status()
            payload = resp.json()
        except (requests.RequestException, ValueError) as exc:
            logger.warning("crossref: request failed for %s: %s", paper.arxiv_id, exc)
            return None

        items = (payload.get("message") or {}).get("items") or []
        if not items:
            return None

        want_title = normalize_title(title)
        want_authors = set(author_last_names(paper.authors or []))

        best: Optional[DOIMatch] = None
        for item in items:
            cand_titles = item.get("title") or []
            doi = normalize_doi(str(item.get("DOI") or ""))
            if not doi or not cand_titles:
                continue
            if not any(normalize_title(t) == want_title for t in cand_titles):
                continue

            cand_authors = _crossref_author_lastnames(item.get("author") or [])
            cand_year = _crossref_year(item)

            if want_authors and cand_authors:
                if want_authors & cand_authors:
                    return DOIMatch(
                        doi=doi,
                        source="crossref",
                        confidence="high",
                        raw_record=_trim_crossref(item),
                    )
                # Title matched but authors don't — skip, this is a different work.
                continue

            if paper.year and cand_year and paper.year == cand_year:
                return DOIMatch(
                    doi=doi,
                    source="crossref",
                    confidence="high",
                    raw_record=_trim_crossref(item),
                )

            # No author data either side & no year overlap — accept once at medium.
            if best is None:
                best = DOIMatch(
                    doi=doi,
                    source="crossref",
                    confidence="medium",
                    raw_record=_trim_crossref(item),
                )
        return best


def _crossref_author_lastnames(authors: List[Dict[str, Any]]) -> set:
    names: List[str] = []
    for a in authors:
        family = a.get("family")
        if family:
            names.append(family)
            continue
        # Some legacy records only have `name`.
        name = a.get("name")
        if name:
            names.append(name)
    return set(author_last_names(names))


def _crossref_year(item: Dict[str, Any]) -> Optional[int]:
    for key in ("issued", "published-print", "published-online", "created"):
        block = item.get(key)
        if not isinstance(block, dict):
            continue
        parts = block.get("date-parts") or []
        if parts and isinstance(parts, list) and parts[0]:
            try:
                return int(parts[0][0])
            except (TypeError, ValueError):
                continue
    return None


def _trim_crossref(item: Dict[str, Any]) -> Dict[str, Any]:
    return {
        "DOI": item.get("DOI"),
        "title": item.get("title"),
        "author": [
            {"family": a.get("family"), "given": a.get("given")}
            for a in (item.get("author") or [])[:8]
        ],
        "type": item.get("type"),
        "issued": item.get("issued"),
    }
