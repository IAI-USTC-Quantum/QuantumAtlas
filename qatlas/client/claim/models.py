"""Data models for the Claim-drafting workflow (ADR 0005 / 0007)."""

from __future__ import annotations

from pydantic import BaseModel, Field


class PaperRef(BaseModel):
    """The paper-of-record a Claim is extracted from."""

    id: str
    doi_or_arxiv: str | None = None
    title: str | None = None
    authors: list[str] = Field(default_factory=list)


class ResolvedRef(BaseModel):
    """One entry of ``GET /api/papers/lookup``'s per-ref response (ADR 0007).

    ``ref`` is the namespaced, unversioned ``kind:id`` string. ``resolved`` is
    False for ids the corpus didn't hold (kept-unresolved refs are stored as
    their raw ``kind:id``); ``hosted`` flags refs QuantumAtlas already hosts.
    """

    ref: str
    title: str | None = None
    authors: list[str] = Field(default_factory=list)
    year: int | None = None
    hosted: bool = False
    resolved: bool = False


class ClaimDraft(BaseModel):
    """A single pre-proof natural-language statement extracted from a Paper —
    a candidate for formalization. Owns the namespace ``<paper_id>:<slug>``.
    """

    # Namespace key claim_id = <paper_id>:<slug>. This is QA's id for the
    # Claim; it is what the contrib agent writes into the trailing ``claim_id:``
    # marker of the gitea issue (the daemon dedups on that line). QA does not
    # model anything past the issue.
    claim_id: str
    paper: PaperRef
    claim_kind: str = "theorem"  # theorem | lemma | proposition | bound | ...
    # Near-verbatim natural-language statement (no transformation).
    natural_language: str
    latex: str | None = None
    why_it_matters: str | None = None
    # Source-md provenance, e.g. "L120-L135".
    source_md_lines: str | None = None
    # Namespaced, unversioned kind:id reference strings (ADR 0007).
    references: list[str] = Field(default_factory=list)
