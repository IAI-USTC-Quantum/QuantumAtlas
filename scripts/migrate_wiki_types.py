#!/usr/bin/env python3
# /// script
# requires-python = ">=3.10"
# dependencies = []
# ///
"""Migrate wiki page `type` frontmatter: entity/comparison -> concept.

QuantumAtlas is moving to a Wikipedia-style model where every browsable
page is a *concept* (词条) and `source` pages are citations rather than
entries. This one-shot script rewrites the `type:` field of every
`entity` and `comparison` page to `concept`, preserving `category`
(algorithm / primitive / zoo-section / ...). Comparison pages get
`category: comparison` injected when they lack one, so the SPA's
category-grouped browse keeps them together.

The edit is surgical: only the `type:` line inside the YAML frontmatter
block is touched (plus an optional `category:` insertion for
comparisons). YAML formatting, ordering and the markdown body are left
byte-for-byte intact. Idempotent — re-running is a no-op.

Usage:
    python scripts/migrate_wiki_types.py [WIKI_DIR] [--dry-run]

WIKI_DIR defaults to ../QuantumAtlas-Wiki relative to the repo root, or
$QATLAS_WIKI_DIR / $WIKI_DIR when set.
"""

from __future__ import annotations

import argparse
import os
import re
import sys
from pathlib import Path

SYSTEM_FILES = {"index.md", "log.md", "README.md"}

FRONTMATTER_RE = re.compile(r"(?s)\A(---\s*\n)(.*?\n)(---\s*\n)(.*)\Z")
TYPE_LINE_RE = re.compile(r"(?m)^(type:[ \t]*)(entity|comparison)[ \t]*$")
CATEGORY_LINE_RE = re.compile(r"(?m)^category:[ \t]*\S")


def resolve_wiki_dir(arg: str | None) -> Path:
    if arg:
        return Path(arg).expanduser().resolve()
    env = os.environ.get("QATLAS_WIKI_DIR") or os.environ.get("WIKI_DIR")
    if env:
        return Path(env).expanduser().resolve()
    repo_root = Path(__file__).resolve().parent.parent
    return (repo_root.parent / "QuantumAtlas-Wiki").resolve()


def migrate_text(text: str) -> tuple[str, bool]:
    """Return (new_text, changed). Only rewrites the frontmatter block."""
    m = FRONTMATTER_RE.match(text)
    if not m:
        return text, False
    open_fence, fm, close_fence, body = m.groups()

    type_match = TYPE_LINE_RE.search(fm)
    if not type_match:
        return text, False

    was_comparison = type_match.group(2) == "comparison"
    new_fm = TYPE_LINE_RE.sub(lambda mm: mm.group(1) + "concept", fm, count=1)

    if was_comparison and not CATEGORY_LINE_RE.search(new_fm):
        # Insert `category: comparison` right after the (now-concept)
        # type line so comparison pages cluster in the category browse.
        new_fm = re.sub(
            r"(?m)^(type:[ \t]*concept[ \t]*\n)",
            r"\1category: comparison\n",
            new_fm,
            count=1,
        )

    new_text = open_fence + new_fm + close_fence + body
    return new_text, new_text != text


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("wiki_dir", nargs="?", default=None)
    ap.add_argument("--dry-run", action="store_true", help="report without writing")
    args = ap.parse_args()

    wiki_dir = resolve_wiki_dir(args.wiki_dir)
    if not wiki_dir.is_dir():
        print(f"error: wiki dir not found: {wiki_dir}", file=sys.stderr)
        return 2

    changed = 0
    scanned = 0
    for path in sorted(wiki_dir.rglob("*.md")):
        if path.name in SYSTEM_FILES:
            continue
        scanned += 1
        original = path.read_text(encoding="utf-8")
        new_text, did = migrate_text(original)
        if not did:
            continue
        changed += 1
        rel = path.relative_to(wiki_dir)
        print(f"{'would migrate' if args.dry_run else 'migrated'}: {rel}")
        if not args.dry_run:
            path.write_text(new_text, encoding="utf-8")

    print(f"\nscanned {scanned} pages, {changed} migrated"
          f"{' (dry-run)' if args.dry_run else ''}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
