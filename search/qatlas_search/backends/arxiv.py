"""arXiv backend — free, no API key.

Queries the arXiv Atom API. arXiv exposes **no citation count**, so ``Paper``
records from here leave ``citations=None`` (the ranker treats that as a neutral
signal rather than zero). Its strength is precise full-text term/title matching
on the canonical preprint source for quantum computing.
"""

from __future__ import annotations

import re
import xml.etree.ElementTree as ET
from urllib.parse import quote

from qatlas_search.backends.base import COST_FAST, Backend
from qatlas_search.config import Settings
from qatlas_search.models import Paper, SearchQuery

_ARXIV_API = "http://export.arxiv.org/api/query"
_ATOM = "{http://www.w3.org/2005/Atom}"
_ARXIV_ID_RE = re.compile(r"arxiv\.org/abs/([^v]+)(v\d+)?", re.IGNORECASE)


class ArxivBackend(Backend):
    name = "arxiv"
    requires_key = False
    cost_tier = COST_FAST

    def _build_query(self, query: SearchQuery) -> str:
        # Quoted phrases become exact field matches; bare tokens AND together.
        parts: list[str] = []
        for phrase in query.required_phrases:
            parts.append(f'all:"{phrase}"')
        remaining = query.text
        for phrase in query.required_phrases:
            remaining = remaining.replace(f'"{phrase}"', " ")
        for tok in remaining.split():
            tok = tok.strip()
            if tok:
                parts.append(f"all:{tok}")
        return "+AND+".join(parts) if parts else f"all:{query.text}"

    def _search_url(self, query: SearchQuery) -> str:
        """Assemble the request URL, percent-encoding the user-derived terms.

        The arXiv API wants the ``all:`` field prefixes and ``+AND+`` operators
        kept literal, but a bare token containing a URL-significant character
        (``&``, ``#``, ``?``) would otherwise terminate the ``search_query``
        value early and corrupt the request. We keep only ``:`` and ``+`` safe
        so the field/operator structure survives while every term character is
        encoded (space → %20, & → %26, " → %22).
        """
        return f"{_ARXIV_API}?search_query={quote(self._build_query(query), safe=':+')}"

    def search(self, query: SearchQuery, settings: Settings) -> list[Paper]:
        self.last_error = None
        try:
            resp = self._get(
                settings,
                self._search_url(query),
                params={
                    "start": 0,
                    "max_results": query.max_results,
                    "sortBy": "relevance",
                    "sortOrder": "descending",
                },
            )
            return self._parse(resp.text)
        except Exception as exc:  # noqa: BLE001 - resilience by contract
            self.last_error = f"{type(exc).__name__}: {exc}"
            return []

    def _parse(self, xml_text: str) -> list[Paper]:
        root = ET.fromstring(xml_text)
        out: list[Paper] = []
        for i, entry in enumerate(root.findall(f"{_ATOM}entry")):
            title_el = entry.find(f"{_ATOM}title")
            summary_el = entry.find(f"{_ATOM}id")
            abs_el = entry.find(f"{_ATOM}summary")
            pub_el = entry.find(f"{_ATOM}published")
            if title_el is None or title_el.text is None:
                continue
            arxiv_id = None
            url = None
            if summary_el is not None and summary_el.text:
                url = summary_el.text.strip()
                m = _ARXIV_ID_RE.search(url)
                if m:
                    arxiv_id = m.group(1)
            authors = [
                a.findtext(f"{_ATOM}name", default="").strip()
                for a in entry.findall(f"{_ATOM}author")
            ]
            year = None
            if pub_el is not None and pub_el.text and len(pub_el.text) >= 4:
                try:
                    year = int(pub_el.text[:4])
                except ValueError:
                    year = None
            out.append(
                Paper(
                    title=" ".join(title_el.text.split()),
                    authors=[a for a in authors if a],
                    year=year,
                    abstract=(abs_el.text or "").strip() if abs_el is not None else None,
                    citations=None,
                    url=url,
                    arxiv_id=arxiv_id,
                    source=self.name,
                    raw_rank=i + 1,
                )
            )
        return out
