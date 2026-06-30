"""Render a ClaimDraft into the canonical gitea issue title + body (ADR 0005).

The shape mirrors the de-facto ``type/theorem`` issue body the upstream
``agony/qatlas-lean`` daemon consumes: ``## Claim`` (NL + LaTeX), ``## Why it
matters``, ``## Paper reference``, optional ``## References``, and a **trailing
``claim_id:`` marker** the daemon dedups on (it greps line-prefix ``claim_id:``
across all open+closed issues/PRs; the legacy ``unit_id:`` prefix is still
accepted on the daemon side for back-compat, but QA writes ``claim_id:``).

The contrib agent deliberately OMITS lean's Stage-3 "参考资料 reuse shortlist"
(reference lemmas from the Lean corpus) — it usually has no Lean checkout; the
lean daemon adds that at pickup. The contrib agent's job is scout+enricher
quality (paper-of-record, near-verbatim NL, assumptions, references).
"""

from __future__ import annotations

import re

from .models import ClaimDraft, ResolvedRef

# Gitea labels for a Claim filed via the contrib flow (the daemon's
# ``type/theorem`` + ``status/ready``). NOT ``type/audit-existing``.
LABEL_TYPE_THEOREM = "type/theorem"
LABEL_STATUS_READY = "status/ready"
CONTRIB_LABELS = [LABEL_TYPE_THEOREM, LABEL_STATUS_READY]

_SLUG_RE = re.compile(r"[^a-z0-9]+")


def slugify(text: str) -> str:
    """Lower-case, collapse non-alphanumerics to single hyphens, trim. Used to
    build the ``<slug>`` half of a ``claim_id = <paper_id>:<slug>``.
    """
    s = _SLUG_RE.sub("-", text.strip().lower()).strip("-")
    return s or "claim"


def make_claim_id(paper_id: str, slug: str) -> str:
    """Compose the ``claim_id = <paper_id>:<slug>`` namespace key. The slug is
    slugified defensively so a free-text label still yields a stable id.
    """
    return f"{paper_id}:{slugify(slug)}"


def render_title(claim: ClaimDraft) -> str:
    """``thm: <short statement>`` — the daemon's title convention. The NL
    statement is truncated to keep titles readable; identifiers stay verbatim.
    """
    nl = " ".join(claim.natural_language.split())
    short = nl if len(nl) <= 110 else nl[:107].rstrip() + "…"
    return f"thm: {short}"


def _render_paper_reference(claim: ClaimDraft) -> str:
    p = claim.paper
    authors = ", ".join(p.authors) if p.authors else "—"
    bits = [f"**{p.title or p.id}** — {authors}"]
    if p.doi_or_arxiv:
        bits.append(f"`{p.doi_or_arxiv}`")
    line = " ".join(bits)
    if claim.source_md_lines:
        line += f"\n\nsource-md: `{claim.source_md_lines}`"
    return line


def _render_one_ref(r: ResolvedRef) -> str:
    if r.resolved:
        meta = r.title or "(no title)"
        if r.year:
            meta += f" ({r.year})"
        flag = " · hosted" if r.hosted else ""
        return f"- `{r.ref}` — {meta}{flag}"
    return f"- `{r.ref}` — ⚠ unresolved"


def render_body(claim: ClaimDraft, resolved: list[ResolvedRef] | None = None) -> str:
    """Assemble the canonical issue body. The trailing ``claim_id:`` line is
    mandatory — the daemon dedups on it, and we always emit it so QA-side
    idempotency works before the daemon ever sees the issue.
    """
    parts: list[str] = []

    parts.append("## Claim\n")
    parts.append(claim.natural_language.strip())
    if claim.latex:
        parts.append(f"\n$$\n{claim.latex.strip()}\n$$")

    if claim.why_it_matters:
        parts.append("\n## Why it matters\n")
        parts.append(claim.why_it_matters.strip())

    parts.append("\n## Paper reference\n")
    parts.append(_render_paper_reference(claim))

    # References block: resolved metadata where available, else the raw kind:id
    # with an unresolved warning. Falls back to the bare ids when no resolution
    # was performed.
    if resolved:
        parts.append("\n## References\n")
        parts.append("\n".join(_render_one_ref(r) for r in resolved))
    elif claim.references:
        parts.append("\n## References\n")
        parts.append("\n".join(f"- `{ref}`" for ref in claim.references))

    body = "\n".join(parts).strip()
    # Trailing dedup marker. QA writes claim_id: (the daemon greps it; legacy
    # unit_id: is still accepted daemon-side but is a prover-internal concept).
    body += f"\n\nclaim_id: {claim.claim_id}"
    return body
