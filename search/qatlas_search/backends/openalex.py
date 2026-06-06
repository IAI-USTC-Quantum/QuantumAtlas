"""OpenAlex backend — free, no key (contact email opts into the polite pool).

OpenAlex is the citation-aware workhorse: every work carries ``cited_by_count``,
and results can be sorted by it. Abstracts arrive as an *inverted index*
(token -> positions) which we reconstruct into plain text so the lexical ranker
can score against it.
"""

from __future__ import annotations

from qatlas_search.backends.base import COST_MEDIUM, Backend
from qatlas_search.config import Settings
from qatlas_search.models import Paper, SearchQuery

_OPENALEX_WORKS = "https://api.openalex.org/works"


def _reconstruct_abstract(inverted: dict | None) -> str | None:
    """Rebuild plain text from OpenAlex's abstract_inverted_index."""
    if not inverted:
        return None
    positions: list[tuple[int, str]] = []
    for word, idxs in inverted.items():
        for i in idxs:
            positions.append((i, word))
    if not positions:
        return None
    positions.sort()
    return " ".join(w for _, w in positions)


class OpenAlexBackend(Backend):
    name = "openalex"
    requires_key = False
    cost_tier = COST_MEDIUM

    def search(self, query: SearchQuery, settings: Settings) -> list[Paper]:
        self.last_error = None
        params = {
            "search": query.text,
            "per_page": query.max_results,
            # Relevance first; citations enter via the ranker so the lexical
            # signal isn't drowned out by a single mega-cited survey.
            "sort": "relevance_score:desc",
        }
        if settings.openalex_email:
            params["mailto"] = settings.openalex_email
        try:
            resp = self._get(settings, _OPENALEX_WORKS, params=params)
            return self._parse(resp.json())
        except Exception as exc:  # noqa: BLE001
            self.last_error = f"{type(exc).__name__}: {exc}"
            return []

    def _parse(self, data: dict) -> list[Paper]:
        out: list[Paper] = []
        for i, w in enumerate(data.get("results", [])):
            title = w.get("display_name") or w.get("title")
            if not title:
                continue
            doi = w.get("doi")
            if doi and doi.startswith("https://doi.org/"):
                doi = doi[len("https://doi.org/") :]
            arxiv_id = None
            for loc in w.get("locations") or []:
                src = loc.get("source") or {}
                if "arxiv" in (src.get("display_name") or "").lower():
                    landing = loc.get("landing_page_url") or ""
                    if "/abs/" in landing:
                        arxiv_id = landing.rsplit("/abs/", 1)[-1].split("v")[0]
            authors = [
                (a.get("author") or {}).get("display_name", "")
                for a in (w.get("authorships") or [])
            ]
            venue = ((w.get("primary_location") or {}).get("source") or {}).get("display_name")
            out.append(
                Paper(
                    title=title,
                    authors=[a for a in authors if a],
                    year=w.get("publication_year"),
                    abstract=_reconstruct_abstract(w.get("abstract_inverted_index")),
                    venue=venue,
                    citations=w.get("cited_by_count"),
                    url=w.get("id"),
                    doi=doi,
                    arxiv_id=arxiv_id,
                    source=self.name,
                    raw_rank=i + 1,
                    raw_score=w.get("relevance_score"),
                )
            )
        return out
