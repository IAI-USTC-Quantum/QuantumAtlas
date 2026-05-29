"""Data contracts shared by every DOI resolver."""

from __future__ import annotations

import re
import unicodedata
from dataclasses import dataclass, field
from typing import Any, Dict, List, Literal, Optional, Protocol, runtime_checkable

DOISource = Literal[
    "arxiv",
    "crossref",
    "openalex",
    "semantic-scholar",
    "manual",
    "unresolved",
]

Confidence = Literal["high", "medium", "low"]


@dataclass(frozen=True)
class PaperContext:
    """Everything a resolver needs to know about a paper.

    Resolvers must treat this as immutable. Populate `year` when available;
    Crossref / OpenAlex hits sometimes need year disambiguation when a title
    is common.
    """

    arxiv_id: str
    title: str
    authors: List[str]
    year: Optional[int] = None


@dataclass
class DOIMatch:
    """A resolved DOI plus enough provenance to debug it later.

    `raw_record` is the upstream API row trimmed to a small dict (title /
    DOI / authors / year). Don't dump the full JSON — it's just for human
    diagnosis if a confidence call looks wrong.
    """

    doi: str
    source: str
    confidence: str
    raw_record: Dict[str, Any] = field(default_factory=dict)


@runtime_checkable
class DOIResolver(Protocol):
    """Interface for a single DOI-resolution strategy.

    Implementations should:
      * return None if they have no opinion (don't raise on "not found")
      * return a DOIMatch when they're sure enough
      * never make multi-second calls without a timeout
    """

    name: str

    def resolve(self, paper: PaperContext) -> Optional[DOIMatch]:
        ...


# --- Helpers shared across resolvers --------------------------------------

_DOI_PREFIX_RE = re.compile(r"^(?:https?://(?:dx\.)?doi\.org/|doi:)", re.IGNORECASE)


def normalize_doi(value: str) -> str:
    """Strip scheme / `doi:` prefixes and lowercase the registrant prefix.

    DOIs are case-insensitive per spec; we lowercase to make string compare
    safe. Returns the empty string for falsy / whitespace input.
    """
    if not value:
        return ""
    cleaned = _DOI_PREFIX_RE.sub("", value.strip())
    return cleaned.strip().lower()


# Title normalization for cross-checking match candidates. We strip LaTeX
# command markers, math delimiters, punctuation, and collapse whitespace.
# This is *strict* — we want "Quantum Fourier Transform" to match exactly,
# not via Levenshtein.
_LATEX_CMD_RE = re.compile(r"\\[A-Za-z]+\s*(?:\{[^}]*\})?")
_MATH_RE = re.compile(r"\$[^$]*\$")
_NONALNUM_RE = re.compile(r"[^a-z0-9]+")


def normalize_title(value: str) -> str:
    """Aggressive, deterministic title canonicalization for equality compare."""
    if not value:
        return ""
    # NFKC folds compatibility chars (e.g. ligatures) into ASCII-ish.
    s = unicodedata.normalize("NFKC", value).lower()
    s = _MATH_RE.sub(" ", s)
    s = _LATEX_CMD_RE.sub(" ", s)
    s = _NONALNUM_RE.sub(" ", s)
    return " ".join(s.split())


def author_last_names(authors: List[str]) -> List[str]:
    """Best-effort lowercase last name extraction for cross-checking.

    Handles "First Last", "Last, First", and "First Middle Last" formats.
    Diacritics are stripped via NFKD so e.g. "Schrödinger" -> "schrodinger".
    """
    out: List[str] = []
    for raw in authors:
        if not raw or not isinstance(raw, str):
            continue
        s = unicodedata.normalize("NFKD", raw)
        s = "".join(ch for ch in s if not unicodedata.combining(ch))
        s = s.strip()
        if "," in s:
            # "Last, First"
            last = s.split(",", 1)[0]
        else:
            parts = s.split()
            last = parts[-1] if parts else ""
        last = re.sub(r"[^A-Za-z\-]+", "", last).lower()
        if last:
            out.append(last)
    return out
