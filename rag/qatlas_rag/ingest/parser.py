"""Identify and extract logical units from a MinerU-style arxiv Markdown.

Order of operations:
1. Pull arxiv image refs `![alt](images/.../fig.png)` out and replace with
   `[FIGURE:N]` placeholders.  We keep the *path* in payload (sidecar can
   later serve / link the image); the alt-text is intentionally dropped
   so it does not poison the embedding text.
2. Mark inviolable spans — code fences (```...```), display math (``$$...$$``)
   and pipe-tables — so the chunker does not slice across them.
3. Walk the headings (``^#{1,6} ...``) to build a path-aware section tree;
   the body of each leaf section is what eventually feeds the chunker.

mistune is used as the AST parser so we do not invent another Markdown
grammar; we only post-process the AST.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field


# arxiv MinerU output uses ![caption](images/<yymm>/<canonical>vN/<file>)
_IMG_RE = re.compile(r"!\[(?P<alt>[^\]]*)\]\((?P<path>[^)]+)\)")


@dataclass
class Section:
    """A leaf section of the document — the unit the chunker eats."""

    path: list[str]            # ["Introduction", "Background"]
    level: int                 # depth, 1-indexed
    body: str                  # plain markdown with [FIGURE:N] / inviolable markers preserved
    image_refs: list[str] = field(default_factory=list)
    char_start: int = 0        # offset in the *original* document
    char_end: int = 0


def extract_image_refs(text: str) -> tuple[str, list[str]]:
    """Replace every image ref with `[FIGURE:N]` and return (new_text, paths).

    The placeholder count is per-document (1-indexed), so the same path
    appearing twice gets two different placeholders — that is acceptable
    because the payload list also stores duplicates and the sidecar can
    dedupe at render time.
    """
    refs: list[str] = []

    def _sub(m: re.Match[str]) -> str:
        refs.append(m.group("path"))
        return f"[FIGURE:{len(refs)}]"

    return _IMG_RE.sub(_sub, text), refs


# Headings: ``^#{1,6}\s+title`` *anywhere in the document*.  We do NOT
# consider setext-style underline headings (===/---), MinerU does not emit
# them.
_HEADING_RE = re.compile(r"^(?P<hashes>#{1,6})\s+(?P<title>.+?)\s*$", re.MULTILINE)


def split_sections(text: str) -> list[Section]:
    """Walk the document and return one Section per leaf header.

    A "leaf" is a header followed (eventually) by body text and no deeper
    header *before the next sibling or shallower header*.  Concretely: if
    ``## A`` is immediately followed by ``### A.1``, then ``## A`` is NOT a
    leaf — only ``### A.1`` (and any later leaves under ``## A``) emit a
    Section.  Body text that appears *before* the first header is emitted
    as a Section with path ``["__preamble__"]`` and level 0.
    """
    # Strip image refs first so they don't confuse offset bookkeeping.
    text_no_imgs, refs_global = extract_image_refs(text)

    headings = list(_HEADING_RE.finditer(text_no_imgs))
    if not headings:
        # Single-section document.
        body = text_no_imgs.strip()
        return [
            Section(
                path=["__preamble__"],
                level=0,
                body=body,
                image_refs=refs_global,
                char_start=0,
                char_end=len(text_no_imgs),
            )
        ]

    # Build [(level, title, span_start, span_end), ...] entries; the body
    # belonging to entry i runs from its end-of-heading-line to entry i+1's
    # start.
    entries: list[tuple[int, str, int, int]] = []
    # Preamble
    if headings[0].start() > 0:
        entries.append((0, "__preamble__", 0, headings[0].start()))
    for i, m in enumerate(headings):
        body_start = m.end()
        body_end = headings[i + 1].start() if i + 1 < len(headings) else len(text_no_imgs)
        level = len(m.group("hashes"))
        title = m.group("title").strip()
        entries.append((level, title, body_start, body_end))

    # Build sections via a path stack; emit a Section only at *leaves*
    # (entries that don't have a deeper-than-itself successor immediately
    # following with no body in between).
    sections: list[Section] = []
    path_stack: list[str] = []
    for idx, (level, title, body_start, body_end) in enumerate(entries):
        body = text_no_imgs[body_start:body_end]
        # Maintain path stack
        if title == "__preamble__":
            # Always emit preamble as its own section if non-empty.
            stripped = body.strip()
            if stripped:
                sections.append(
                    Section(
                        path=["__preamble__"],
                        level=0,
                        body=stripped,
                        image_refs=_collect_refs_in_span(refs_global, text_no_imgs, body_start, body_end),
                        char_start=body_start,
                        char_end=body_end,
                    )
                )
            continue
        # Adjust path stack to this header's level
        while len(path_stack) >= level:
            path_stack.pop()
        path_stack.append(title)
        # Decide if this entry is a "leaf": next entry exists *and* has level
        # > this one *and* body between current header and next is whitespace
        # only.  Otherwise emit.
        is_intermediate = False
        if idx + 1 < len(entries):
            next_level = entries[idx + 1][0]
            if next_level > level and not body.strip():
                is_intermediate = True
        if is_intermediate:
            # last_level intentionally untracked; path_stack carries the state.
            continue
        stripped = body.strip()
        # Even an intermediate that has body text gets its own Section
        # (don't lose the prose between ``## A`` and ``### A.1``).
        if stripped or not is_intermediate:
            sections.append(
                Section(
                    path=list(path_stack),
                    level=level,
                    body=stripped,
                    image_refs=_collect_refs_in_span(refs_global, text_no_imgs, body_start, body_end),
                    char_start=body_start,
                    char_end=body_end,
                )
            )
        # last_level intentionally unused: path stack tracks state.

    return sections


# --- helpers -----------------------------------------------------------

_FIGURE_RE = re.compile(r"\[FIGURE:(\d+)\]")


def _collect_refs_in_span(
    refs_global: list[str], text_with_placeholders: str, start: int, end: int
) -> list[str]:
    """Pick the subset of `refs_global` whose `[FIGURE:N]` lives inside [start, end)."""
    indices = {int(m.group(1)) for m in _FIGURE_RE.finditer(text_with_placeholders[start:end])}
    return [refs_global[i - 1] for i in sorted(indices) if 1 <= i <= len(refs_global)]
