"""Claim drafting — turns paper markdown into ClaimDraft candidates.

The drafter combines the scout + enricher + issue-raiser roles (minus lean's
Lean-corpus reuse-shortlist, which the contrib agent omits — it usually has no
Lean checkout; the lean daemon adds it at pickup). It is **optional**: when no
LLM provider is configured the WebUI runs in **manual mode** (the human writes
the Claim), so ``qatlas contrib claim`` is fully usable without API keys.

Backend: the user's own credentials, locally (ADR 0005). We lazily use the
``anthropic`` or ``openai`` SDK (already core deps) based on which API key env
var is present.
"""

from __future__ import annotations

import json
import os
import re

from .models import ClaimDraft, PaperRef
from .issue import make_claim_id

SYSTEM_PROMPT = """You are a quantum-algorithms formalization scout + enricher.

Given a paper's markdown, extract EVERY formalization-ready claim (theorem,
lemma, proposition, complexity bound) that a Lean 4 formalizer could target.
For each, produce a faithful, NEAR-VERBATIM natural-language statement (do NOT
paraphrase away assumptions), the LaTeX if the source prints it, the stated and
implicit assumptions, a one-line "why it matters", the source markdown line
range if identifiable, and the bibliographic references it cites as namespaced
unversioned ids (arxiv:XXXX.XXXXX / openalex:WXXXXXXXXX / doi:10.x/y).

Do NOT invent results not in the paper. Do NOT add a Lean reuse shortlist — that
is added later by the prover side.

Return ONLY a JSON array; each element:
{
  "slug": "short-kebab-slug",
  "claim_kind": "theorem|lemma|proposition|bound",
  "natural_language": "...",
  "latex": "... or null",
  "why_it_matters": "...",
  "source_md_lines": "L120-L135 or null",
  "references": ["arxiv:...", "doi:..."]
}
"""

# Cap the paper text we send so a huge markdown doesn't blow the context window.
_MAX_PAPER_CHARS = 60_000


class ClaimDrafter:
    def __init__(self, paper_ref: PaperRef):
        self._paper = paper_ref
        self._backend = _detect_backend()

    @property
    def available(self) -> bool:
        """False → no LLM key configured; the WebUI runs in manual mode."""
        return self._backend is not None

    @property
    def backend_name(self) -> str:
        return self._backend or "manual"

    def draft(self, markdown: str, hint: str = "") -> list[ClaimDraft]:
        """Draft Claim candidates from the paper markdown. Raises RuntimeError
        when no backend is available (callers should check ``available`` first).
        """
        if not self._backend:
            raise RuntimeError("no LLM backend configured (manual mode)")
        text = markdown[:_MAX_PAPER_CHARS]
        user = f"Paper id: {self._paper.id}\n"
        if hint:
            user += f"Focus hint: {hint}\n"
        user += f"\nPaper markdown:\n\n{text}"
        raw = _call_llm(self._backend, SYSTEM_PROMPT, user)
        return self._parse(raw)

    def _parse(self, raw: str) -> list[ClaimDraft]:
        items = _extract_json_array(raw)
        drafts: list[ClaimDraft] = []
        for it in items:
            slug = str(it.get("slug") or it.get("natural_language", "claim"))[:60]
            drafts.append(
                ClaimDraft(
                    claim_id=make_claim_id(self._paper.id, slug),
                    paper=self._paper,
                    claim_kind=str(it.get("claim_kind") or "theorem"),
                    natural_language=str(it.get("natural_language") or "").strip(),
                    latex=(it.get("latex") or None),
                    why_it_matters=(it.get("why_it_matters") or None),
                    source_md_lines=(it.get("source_md_lines") or None),
                    references=[str(r) for r in (it.get("references") or [])],
                )
            )
        return [d for d in drafts if d.natural_language]


def _detect_backend() -> str | None:
    if os.getenv("ANTHROPIC_API_KEY"):
        return "anthropic"
    if os.getenv("OPENAI_API_KEY"):
        return "openai"
    return None


def _call_llm(backend: str, system: str, user: str) -> str:
    if backend == "anthropic":
        import anthropic

        client = anthropic.Anthropic()
        model = os.getenv("QATLAS_CLAIM_MODEL", "claude-3-5-sonnet-latest")
        msg = client.messages.create(
            model=model,
            max_tokens=4096,
            system=system,
            messages=[{"role": "user", "content": user}],
        )
        return "".join(b.text for b in msg.content if getattr(b, "type", "") == "text")
    if backend == "openai":
        from openai import OpenAI

        client = OpenAI()
        model = os.getenv("QATLAS_CLAIM_MODEL", "gpt-4o")
        resp = client.chat.completions.create(
            model=model,
            messages=[
                {"role": "system", "content": system},
                {"role": "user", "content": user},
            ],
        )
        return resp.choices[0].message.content or ""
    raise RuntimeError(f"unknown backend {backend}")


def _extract_json_array(raw: str) -> list[dict]:
    """Tolerantly pull a JSON array out of an LLM response (may be fenced)."""
    raw = raw.strip()
    fence = re.search(r"```(?:json)?\s*(.*?)```", raw, re.DOTALL)
    if fence:
        raw = fence.group(1).strip()
    start = raw.find("[")
    end = raw.rfind("]")
    if start == -1 or end == -1 or end <= start:
        return []
    try:
        data = json.loads(raw[start : end + 1])
    except json.JSONDecodeError:
        return []
    return [x for x in data if isinstance(x, dict)]
