"""Unified result + query models shared by every search backend.

A ``Paper`` is the lowest common denominator across arXiv / OpenAlex /
Semantic Scholar / Crossref / the QuantumAtlas internal graph. Backends fill
what they can; missing fields stay ``None`` rather than being faked, so the
ranker can reason about *absence* of a citation count (e.g. arXiv) instead of
treating it as zero.
"""

from __future__ import annotations

import re
from typing import Optional

from pydantic import BaseModel, Field

# Tokens shorter than this are dropped from the dedup key / lexical match so
# stop-word-ish fragments ("a", "of") don't dominate.
_MIN_TOKEN = 2

_WORD_RE = re.compile(r"[a-z0-9]+")


def normalize_title(title: str) -> str:
    """Lowercase, collapse to alphanumeric tokens — a weak dedup signal only.

    Used **only** as a fallback merge key when no DOI / arXiv / source id is
    available. Distinct papers can share a title, so the ranker treats a
    title-only match as a soft merge, never an authoritative identity.
    """
    return " ".join(_WORD_RE.findall(title.lower()))


class Paper(BaseModel):
    """One search hit, normalized across sources."""

    title: str
    authors: list[str] = Field(default_factory=list)
    year: Optional[int] = None
    abstract: Optional[str] = None
    venue: Optional[str] = None
    # None means "this source does not report citations" (e.g. arXiv); 0 means
    # "reported as uncited". The ranker distinguishes the two.
    citations: Optional[int] = None
    url: Optional[str] = None
    doi: Optional[str] = None
    arxiv_id: Optional[str] = None
    # Which backend produced this record (e.g. "openalex"). After merge() a
    # record may carry several, comma-joined, when the same paper was found by
    # multiple sources.
    source: str = ""
    # 1-based position in the source's own result list, if known.
    raw_rank: Optional[int] = None
    # The source's own relevance score, if it exposes one (OpenAlex/S2 do).
    raw_score: Optional[float] = None
    # Composite score filled in by ranking.rank(); higher is better.
    score: float = 0.0

    def identity_key(self) -> str:
        """Best available stable identity for dedup.

        Priority: DOI > arXiv id > normalized title. The first two are
        authoritative; the title fallback is intentionally weak.
        """
        if self.doi:
            return "doi:" + self.doi.lower().strip()
        if self.arxiv_id:
            return "arxiv:" + self.arxiv_id.lower().strip()
        return "title:" + normalize_title(self.title)

    def match_text(self) -> str:
        """Concatenated text the lexical ranker scores against."""
        return " ".join(p for p in (self.title, self.abstract) if p)


class SearchQuery(BaseModel):
    """A single search request.

    ``required_phrases`` are quoted spans pulled out of the raw query (e.g.
    ``"surface code"``); the ranker boosts records that contain them verbatim,
    which is the whole point of *academic* (lexical) search over vector search.
    """

    text: str
    max_results: int = 10
    required_phrases: list[str] = Field(default_factory=list)

    @classmethod
    def parse(cls, raw: str, max_results: int = 10) -> "SearchQuery":
        phrases = re.findall(r'"([^"]+)"', raw)
        return cls(text=raw.strip(), max_results=max_results, required_phrases=phrases)

    def tokens(self) -> list[str]:
        """Lowercased query tokens (quotes stripped) of useful length."""
        return [t for t in _WORD_RE.findall(self.text.lower()) if len(t) >= _MIN_TOKEN]
