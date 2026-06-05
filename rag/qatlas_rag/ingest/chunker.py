"""Token-aware chunker that respects inviolable spans (math / code / tables).

A Section produced by ``ingest.parser.split_sections`` is fed in; out comes
a list of Chunks ready to be embedded.  Each chunk targets ~800 tokens with
~100 overlap (under the bge-m3 tokenizer), but never slices through:
- fenced code blocks  ``` ... ```
- display math        $$ ... $$
- pipe tables         |...|
- the inline ``[FIGURE:N]`` placeholders (cosmetic, can land anywhere)

The tokenizer is lazy-loaded so this module imports cleanly without torch
installed (sidecar / ingester roles don't both need the tokenizer at
import time).
"""

from __future__ import annotations

import hashlib
import re
from dataclasses import dataclass, field
from functools import lru_cache
from typing import TYPE_CHECKING

from .parser import Section

if TYPE_CHECKING:
    from transformers import PreTrainedTokenizerBase


TARGET_TOKENS = 800
OVERLAP_TOKENS = 100


@dataclass
class Chunk:
    """One indexed chunk; payload-ready except for arxiv-level fields the
    chunker doesn't see (arxiv_id / canonical / yymm / md_object_key) —
    those are attached by the ingester wrapper."""

    section_path: list[str]
    section_level: int
    chunk_index: int                          # 0-based within document
    text: str                                 # what gets embedded
    text_hash: str                            # sha256(text).hexdigest()[:16]
    image_refs: list[str] = field(default_factory=list)
    char_start: int = 0
    char_end: int = 0


@lru_cache(maxsize=1)
def _get_tokenizer(model_name: str = "BAAI/bge-m3") -> "PreTrainedTokenizerBase":
    from transformers import AutoTokenizer

    return AutoTokenizer.from_pretrained(model_name)


# --- inviolable-span detection ---------------------------------------------
#
# Each pattern returns a non-overlapping list of (start, end) char spans
# in the input.  The chunker treats each span as one atomic block: prefer
# to cut on the boundary, never inside.

_FENCE_RE = re.compile(r"```[\s\S]*?```", re.MULTILINE)
_DISPLAY_MATH_RE = re.compile(r"\$\$[\s\S]*?\$\$")
# A table is a run of >=2 consecutive lines that start with optional
# whitespace then a '|'.  The separator line `| --- |` is conventional but
# we don't require it.
_TABLE_RE = re.compile(r"(?:^[ \t]*\|.*\n){2,}", re.MULTILINE)


def _inviolable_spans(text: str) -> list[tuple[int, int]]:
    spans: list[tuple[int, int]] = []
    for r in (_FENCE_RE, _DISPLAY_MATH_RE, _TABLE_RE):
        for m in r.finditer(text):
            spans.append((m.start(), m.end()))
    spans.sort()
    # Merge any overlap (a $$math$$ inside a code block would overlap, etc.).
    merged: list[tuple[int, int]] = []
    for s, e in spans:
        if merged and s <= merged[-1][1]:
            merged[-1] = (merged[-1][0], max(merged[-1][1], e))
        else:
            merged.append((s, e))
    return merged


# --- token <-> char offset bridge ------------------------------------------
#
# We tokenize the WHOLE section body once with offset_mapping=True; this
# gives us (start_char, end_char) per token, so we can convert any token
# range to char range cheaply and align chunk cuts to inviolable spans
# without re-tokenizing each candidate window.


def _safe_cut_index(
    token_offsets: list[tuple[int, int]],
    target_token: int,
    invs: list[tuple[int, int]],
    direction: str = "forward",
) -> int:
    """Pick a token index near ``target_token`` whose char position is
    NOT inside any inviolable span.

    direction='forward' looks for the smallest index >= target_token that
    lands outside spans (i.e. push the cut later); 'backward' looks for the
    largest index <= target_token (pull the cut earlier).  Returns a token
    index — caller is responsible for resolving to a char offset.
    """
    n = len(token_offsets)
    if target_token >= n:
        return n
    if target_token <= 0:
        return 0
    step = 1 if direction == "forward" else -1
    i = target_token
    while 0 < i < n:
        char_at = token_offsets[i][0]
        if not _in_any_span(char_at, invs):
            return i
        i += step
    return max(0, min(n, target_token))


def _in_any_span(pos: int, spans: list[tuple[int, int]]) -> bool:
    for s, e in spans:
        if s <= pos < e:
            return True
        if pos < s:
            return False
    return False


def chunk_section(
    section: Section,
    *,
    target_tokens: int = TARGET_TOKENS,
    overlap_tokens: int = OVERLAP_TOKENS,
    tokenizer_model: str = "BAAI/bge-m3",
) -> list[Chunk]:
    """Cut a single Section's body into Chunks.

    No model loading happens until the first chunk_section() call (lazy
    tokenizer).  Caller passes a Section straight from ``split_sections``.
    """
    body = section.body
    if not body.strip():
        return []

    tok = _get_tokenizer(tokenizer_model)
    enc = tok(body, return_offsets_mapping=True, add_special_tokens=False)
    offsets: list[tuple[int, int]] = enc["offset_mapping"]
    n_tokens = len(offsets)

    if n_tokens == 0:
        return []
    if n_tokens <= target_tokens:
        return [_chunk_from_window(section, body, offsets, 0, n_tokens, chunk_index=0)]

    invs = _inviolable_spans(body)
    chunks: list[Chunk] = []
    cursor = 0
    chunk_index = 0
    # The effective step size is (target - overlap), but we recompute it
    # against the *actual* end_safe each iteration to absorb inviolable-span
    # jumps; so no separate `step` variable is needed.
    while cursor < n_tokens:
        end_target = min(cursor + target_tokens, n_tokens)
        end_safe = _safe_cut_index(offsets, end_target, invs, direction="forward")
        # If forward-search hit EOF (or never found a safe stop), accept what
        # we have — we'd rather make one slightly oversized chunk than slice
        # through an inviolable span.
        if end_safe <= cursor:
            end_safe = n_tokens
        chunks.append(
            _chunk_from_window(section, body, offsets, cursor, end_safe, chunk_index)
        )
        chunk_index += 1
        if end_safe >= n_tokens:
            break
        cursor = max(cursor + 1, end_safe - overlap_tokens)
    return chunks


def _chunk_from_window(
    section: Section,
    body: str,
    offsets: list[tuple[int, int]],
    tok_start: int,
    tok_end: int,
    chunk_index: int,
) -> Chunk:
    """Materialise one window into a Chunk."""
    char_start = offsets[tok_start][0]
    char_end = offsets[tok_end - 1][1] if tok_end > tok_start else char_start
    text = body[char_start:char_end].strip()
    return Chunk(
        section_path=list(section.path),
        section_level=section.level,
        chunk_index=chunk_index,
        text=text,
        text_hash=hashlib.sha256(text.encode("utf-8")).hexdigest()[:16],
        image_refs=_image_refs_in_window(section.image_refs, body, char_start, char_end),
        char_start=section.char_start + char_start,
        char_end=section.char_start + char_end,
    )


_FIGURE_RE = re.compile(r"\[FIGURE:(\d+)\]")


def _image_refs_in_window(
    section_refs: list[str], body: str, lo: int, hi: int
) -> list[str]:
    """Only attach image_refs whose placeholder actually lives in [lo, hi)."""
    indices = {int(m.group(1)) for m in _FIGURE_RE.finditer(body[lo:hi])}
    return [section_refs[i - 1] for i in sorted(indices) if 1 <= i <= len(section_refs)]


def chunk_document(sections: list[Section], **kwargs) -> list[Chunk]:
    """Chunk a full document; rewrites chunk_index to be 0-based across sections."""
    out: list[Chunk] = []
    for sec in sections:
        for ch in chunk_section(sec, **kwargs):
            ch.chunk_index = len(out)
            out.append(ch)
    return out
