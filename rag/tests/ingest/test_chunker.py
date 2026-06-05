"""Tests for ingest.chunker — token-aware windowing with inviolable spans.

These tests exercise the chunker against a real BAAI/bge-m3 tokenizer
because the windowing relies on its tokenization granularity.  The
tokenizer is cached at module import via @lru_cache in chunker, so the
test cost is dominated by the first import (~5 s).
"""

from __future__ import annotations

import pytest

# Skip the whole module if transformers / FlagEmbedding aren't installed
# (e.g. when running pytest from the sidecar-only venv).
transformers = pytest.importorskip("transformers")

from qatlas_rag.ingest.chunker import (  # noqa: E402  (import after skip)
    _inviolable_spans,
    chunk_document,
    chunk_section,
)
from qatlas_rag.ingest.parser import Section, split_sections  # noqa: E402


def test_inviolable_spans_finds_each_type() -> None:
    text = """\
plain
```python
def f(): pass
```
more text
$$ E = mc^2 $$
| col1 | col2 |
|------|------|
| a    | b    |
end
"""
    spans = _inviolable_spans(text)
    # 3 spans (code fence, display math, table)
    assert len(spans) == 3
    # spans are non-overlapping and sorted
    for (s1, e1), (s2, _) in zip(spans, spans[1:]):
        assert e1 <= s2


def test_chunk_section_short_returns_single_chunk() -> None:
    sec = Section(path=["A"], level=1, body="a short paragraph " * 5, char_start=10)
    chunks = chunk_section(sec)
    assert len(chunks) == 1
    assert chunks[0].section_path == ["A"]
    assert chunks[0].chunk_index == 0
    assert chunks[0].char_start == 10  # offset preserved
    assert chunks[0].text_hash  # populated


def test_chunk_section_long_splits_with_overlap() -> None:
    # ~3000-token body; expect ~4 chunks with 100-token overlap.
    long_body = ("This sentence repeats and adds tokens. " * 400).strip()
    sec = Section(path=["Body"], level=1, body=long_body)
    chunks = chunk_section(sec, target_tokens=800, overlap_tokens=100)
    assert len(chunks) >= 3
    # Each chunk index is monotonically increasing.
    assert [c.chunk_index for c in chunks] == list(range(len(chunks)))
    # Each chunk has text.
    for c in chunks:
        assert c.text.strip()


def test_chunk_section_avoids_cutting_code_fence() -> None:
    # Build body where a code fence sits exactly where the chunker would cut.
    head = "Prose. " * 600  # ~1200 tokens — exceeds 800 target
    fence = "\n\n```python\n" + ("def foo(): pass\n" * 40) + "```\n\n"
    tail = "More prose. " * 600
    sec = Section(path=["B"], level=1, body=head + fence + tail)
    chunks = chunk_section(sec, target_tokens=800, overlap_tokens=100)
    # No chunk text starts or ends mid-fence.
    for c in chunks:
        # Either the fence is wholly inside the chunk or wholly outside.
        starts = c.text.count("```")
        assert starts % 2 == 0, (
            f"chunk {c.chunk_index} sliced through a code fence "
            f"(triple-backtick count {starts} is odd)\n{c.text[:200]}..."
        )


def test_chunk_document_global_index() -> None:
    md = """\
# A

content of A. """ + ("padding sentence. " * 400) + """

# B

content of B.
"""
    sections = split_sections(md)
    chunks = chunk_document(sections)
    assert chunks
    # chunk_index strictly increasing across the document
    assert [c.chunk_index for c in chunks] == list(range(len(chunks)))


def test_chunk_image_refs_attached_to_containing_window() -> None:
    md = """\
# Body

intro text. ![fig](images/x/y/fig1.png) more text after the figure ref.
"""
    sections = split_sections(md)
    chunks = chunk_document(sections)
    # All image_refs that appear in chunks should be the document's image.
    seen = {ref for c in chunks for ref in c.image_refs}
    assert seen == {"images/x/y/fig1.png"}
