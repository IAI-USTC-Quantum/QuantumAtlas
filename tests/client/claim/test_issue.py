"""Tests for the canonical gitea issue title/body rendering (ADR 0005)."""

from __future__ import annotations

from qatlas.client.claim.issue import (
    CONTRIB_LABELS,
    make_claim_id,
    render_body,
    render_title,
    slugify,
)
from qatlas.client.claim.models import ClaimDraft, PaperRef, ResolvedRef


def _claim(**over) -> ClaimDraft:
    base = dict(
        claim_id="2208.06941:main-thm",
        paper=PaperRef(id="2208.06941", doi_or_arxiv="2208.06941", title="A Paper", authors=["Alice", "Bob"]),
        claim_kind="theorem",
        natural_language="The widget converges in O(n log n) under assumption A.",
        latex="\\sum_k x_k = 1",
        why_it_matters="First near-optimal bound.",
        source_md_lines="L120-L135",
        references=["arxiv:2008.00001", "doi:10.1/x"],
    )
    base.update(over)
    return ClaimDraft(**base)


def test_slugify():
    assert slugify("Main Theorem!") == "main-theorem"
    assert slugify("  A/B  c ") == "a-b-c"
    assert slugify("") == "claim"
    assert slugify("---") == "claim"


def test_make_claim_id():
    assert make_claim_id("2208.06941", "Main Theorem") == "2208.06941:main-theorem"


def test_render_title_prefixes_thm_and_truncates():
    t = render_title(_claim())
    assert t.startswith("thm: ")
    long = _claim(natural_language="x " * 200)
    assert len(render_title(long)) <= 5 + 111  # "thm: " + truncated


def test_render_body_has_sections_and_trailing_claim_id():
    body = render_body(_claim())
    assert "## Claim" in body
    assert "## Why it matters" in body
    assert "## Paper reference" in body
    assert "$$" in body  # latex block
    assert "source-md: `L120-L135`" in body
    # Trailing dedup marker is mandatory and line-prefixed (daemon greps it).
    last = [ln for ln in body.splitlines() if ln.strip()][-1]
    assert last == "claim_id: 2208.06941:main-thm"


def test_render_body_resolved_refs():
    resolved = [
        ResolvedRef(ref="arxiv:2008.00001", title="Cited Work", year=2020, hosted=True, resolved=True),
        ResolvedRef(ref="doi:10.1/x", resolved=False),
    ]
    body = render_body(_claim(), resolved)
    assert "## References" in body
    assert "Cited Work (2020)" in body
    assert "hosted" in body
    assert "⚠ unresolved" in body


def test_render_body_bare_refs_when_unresolved_list_absent():
    body = render_body(_claim())
    assert "`arxiv:2008.00001`" in body


def test_labels():
    assert CONTRIB_LABELS == ["type/theorem", "status/ready"]
