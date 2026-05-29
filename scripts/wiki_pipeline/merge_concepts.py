# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Detect duplicate / overlapping concept entries in the QuantumAtlas wiki.

Stage 2 of the content pipeline. After many subagents each emit a concept
(see ``concept_prompt.md``), this tool scans ``concepts/`` (and optionally
``entities/``), computes pairwise similarity from title + tags + summary,
and emits candidate pairs for the merge/crosslink decision
(see ``merge_prompt.md``). It does **not** mutate any wiki file — it only
reports candidates; the actual merge/crosslink edit is done by a human/LLM
following the merge rules.

Similarity is stdlib-only (no embeddings): a weighted blend of
- token Jaccard over title,
- Jaccard over the tag set,
- difflib ratio over the summary section.

Usage:
    uv run --no-project scripts/wiki_pipeline/merge_concepts.py [WIKI_DIR] \
        [--threshold 0.45] [--include-entities] [--json]

WIKI_DIR resolution: positional arg > $QATLAS_WIKI_DIR / $WIKI_DIR >
sibling ``../QuantumAtlas-Wiki`` of this repo.
"""

from __future__ import annotations

import argparse
import difflib
import json
import os
import re
import sys
from dataclasses import dataclass, field
from itertools import combinations
from pathlib import Path

FRONTMATTER_RE = re.compile(r"^---\n(.*?)\n---\n?(.*)$", re.DOTALL)
_TOKEN_RE = re.compile(r"[a-z0-9\u4e00-\u9fff]+")


def resolve_wiki_dir(arg: str | None) -> Path:
    if arg:
        return Path(arg).expanduser().resolve()
    env = os.environ.get("QATLAS_WIKI_DIR") or os.environ.get("WIKI_DIR")
    if env:
        return Path(env).expanduser().resolve()
    return (Path(__file__).resolve().parents[2].parent / "QuantumAtlas-Wiki").resolve()


def _parse_frontmatter(text: str) -> tuple[dict[str, object], str]:
    """Minimal YAML-ish frontmatter parser (stdlib only).

    Handles ``key: value``, ``tags: [a, b]`` inline lists, and block lists
    (``key:`` followed by ``- item`` lines). Enough for wiki frontmatter.
    """
    m = FRONTMATTER_RE.match(text)
    if not m:
        return {}, text
    body = m.group(2)
    fm: dict[str, object] = {}
    lines = m.group(1).splitlines()
    i = 0
    while i < len(lines):
        line = lines[i]
        if not line.strip() or line.lstrip().startswith("#"):
            i += 1
            continue
        mm = re.match(r"^([A-Za-z0-9_]+):\s*(.*)$", line)
        if not mm:
            i += 1
            continue
        key, val = mm.group(1), mm.group(2).strip()
        if val.startswith("[") and val.endswith("]"):
            fm[key] = [x.strip().strip("'\"") for x in val[1:-1].split(",") if x.strip()]
        elif val == "":
            items: list[str] = []
            j = i + 1
            while j < len(lines) and lines[j].lstrip().startswith("- "):
                items.append(lines[j].lstrip()[2:].strip().strip("'\""))
                j += 1
            fm[key] = items if items else ""
            i = j
            continue
        else:
            fm[key] = val.strip().strip("'\"")
        i += 1
    return fm, body


def _summary(body: str) -> str:
    """Extract the first content section (## 摘要 / ## Summary / first para)."""
    sections = re.split(r"\n#{2,}\s", body)
    for sec in sections:
        text = sec.strip()
        # skip TODO banners / empty
        if not text or text.startswith(">"):
            continue
        # drop the heading word itself on the first chunk
        return " ".join(text.splitlines()[:6])[:600]
    return body.strip()[:600]


def _tokens(text: str) -> set[str]:
    return set(_TOKEN_RE.findall(text.lower()))


def _jaccard(a: set[str], b: set[str]) -> float:
    if not a and not b:
        return 0.0
    inter = len(a & b)
    union = len(a | b)
    return inter / union if union else 0.0


@dataclass
class Concept:
    id: str
    title: str
    category: str
    tags: list[str]
    summary: str
    path: Path
    title_tokens: set[str] = field(default_factory=set)
    summary_tokens: set[str] = field(default_factory=set)

    @classmethod
    def load(cls, path: Path) -> "Concept | None":
        fm, body = _parse_frontmatter(path.read_text(encoding="utf-8"))
        cid = str(fm.get("id") or path.stem)
        title = str(fm.get("title") or cid)
        category = str(fm.get("category") or "")
        tags = fm.get("tags") or []
        if not isinstance(tags, list):
            tags = [str(tags)]
        summary = _summary(body)
        c = cls(cid, title, category, [str(t) for t in tags], summary, path)
        c.title_tokens = _tokens(title)
        c.summary_tokens = _tokens(summary)
        return c


def similarity(a: Concept, b: Concept) -> float:
    title_sim = _jaccard(a.title_tokens, b.title_tokens)
    tag_sim = _jaccard(set(a.tags), set(b.tags))
    sum_sim = difflib.SequenceMatcher(None, a.summary, b.summary).ratio()
    # title carries the most signal for "same concept"; tags + summary refine.
    return 0.5 * title_sim + 0.2 * tag_sim + 0.3 * sum_sim


def collect(wiki: Path, include_entities: bool) -> list[Concept]:
    paths: list[Path] = sorted((wiki / "concepts").glob("*.md"))
    if include_entities:
        paths += sorted((wiki / "entities").rglob("*.md"))
    out: list[Concept] = []
    for p in paths:
        try:
            c = Concept.load(p)
        except Exception as exc:  # noqa: BLE001 - report and skip bad files
            print(f"warn: skip {p}: {exc}", file=sys.stderr)
            continue
        if c:
            out.append(c)
    return out


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("wiki_dir", nargs="?", default=None)
    ap.add_argument("--threshold", type=float, default=0.45)
    ap.add_argument("--include-entities", action="store_true")
    ap.add_argument("--json", action="store_true", help="emit JSON for the merge stage")
    args = ap.parse_args(argv)

    wiki = resolve_wiki_dir(args.wiki_dir)
    if not (wiki / "concepts").is_dir():
        print(f"error: {wiki}/concepts not found", file=sys.stderr)
        return 2

    concepts = collect(wiki, args.include_entities)
    pairs = []
    for a, b in combinations(concepts, 2):
        score = similarity(a, b)
        if score >= args.threshold:
            pairs.append((score, a, b))
    pairs.sort(key=lambda x: x[0], reverse=True)

    if args.json:
        payload = [
            {
                "a": a.id,
                "b": b.id,
                "similarity": round(score, 4),
                "a_title": a.title,
                "b_title": b.title,
                "a_summary": a.summary,
                "b_summary": b.summary,
                "a_category": a.category,
                "b_category": b.category,
                "a_tags": a.tags,
                "b_tags": b.tags,
                "decision": None,
            }
            for score, a, b in pairs
        ]
        print(json.dumps(payload, ensure_ascii=False, indent=2))
        return 0

    print(f"# {len(concepts)} concepts scanned in {wiki}")
    print(f"# {len(pairs)} candidate pair(s) at threshold {args.threshold}\n")
    for score, a, b in pairs:
        print(f"[{score:.3f}] {a.id}  <->  {b.id}")
        print(f"        {a.title!r}  /  {b.title!r}")
    if not pairs:
        print("(no candidates — concepts look distinct)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
