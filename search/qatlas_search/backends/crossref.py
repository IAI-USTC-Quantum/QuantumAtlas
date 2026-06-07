"""Crossref backend — free; contact email opts into the polite pool.

Broad cross-discipline metadata with ``is-referenced-by-count`` (citations) and
DOIs. Off by the default allow-list (noisier than the quantum-focused sources)
but available via ``--tools crossref``.
"""

from __future__ import annotations

import re

from qatlas_search.backends.base import COST_MEDIUM, Backend
from qatlas_search.config import Settings
from qatlas_search.models import Paper, SearchQuery

_CROSSREF_WORKS = "https://api.crossref.org/works"
_TAG_RE = re.compile(r"<[^>]+>")


class CrossrefBackend(Backend):
    name = "crossref"
    requires_key = False
    cost_tier = COST_MEDIUM

    def search(self, query: SearchQuery, settings: Settings) -> list[Paper]:
        self.last_error = None
        params = {"query": query.text, "rows": query.max_results}
        if settings.crossref_email:
            params["mailto"] = settings.crossref_email
        try:
            resp = self._get(settings, _CROSSREF_WORKS, params=params)
            return self._parse(resp.json())
        except Exception as exc:  # noqa: BLE001
            self.last_error = f"{type(exc).__name__}: {exc}"
            return []

    def _parse(self, data: dict) -> list[Paper]:
        out: list[Paper] = []
        items = (data.get("message") or {}).get("items", []) or []
        for i, it in enumerate(items):
            titles = it.get("title") or []
            if not titles:
                continue
            year = None
            issued = (it.get("issued") or {}).get("date-parts") or []
            if issued and issued[0]:
                year = issued[0][0]
            authors = [
                " ".join(p for p in (a.get("given"), a.get("family")) if p)
                for a in (it.get("author") or [])
            ]
            abstract = it.get("abstract")
            if abstract:
                abstract = _TAG_RE.sub("", abstract).strip()
            container = it.get("container-title") or []
            out.append(
                Paper(
                    title=" ".join(titles[0].split()),
                    authors=[a for a in authors if a],
                    year=year,
                    abstract=abstract,
                    venue=container[0] if container else None,
                    citations=it.get("is-referenced-by-count"),
                    url=it.get("URL"),
                    doi=it.get("DOI"),
                    source=self.name,
                    raw_rank=i + 1,
                )
            )
        return out
