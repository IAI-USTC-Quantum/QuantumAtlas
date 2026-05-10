#!/usr/bin/env python3
"""Rebuild QuantumAtlas raw/index.sqlite from raw/pdf, markdown, json, images.

Default mode is dry-run. Add --execute to replace index.sqlite.
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import sqlite3
import tempfile
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Iterable

from raw_migration_common import DEFAULT_RAW_ROOT, ym_from_key


@dataclass
class IndexRow:
    paper_key: str
    ym: str
    pdf_path: str | None = None
    markdown_path: str | None = None
    json_path: str | None = None
    image_dir: str | None = None
    image_count: int = 0
    metadata_missing: int = 0
    title: str | None = None
    authors: str | None = None
    arxiv_id: str | None = None
    version: str | None = None


def ensure_schema(conn: sqlite3.Connection) -> None:
    conn.execute(
        """
        create table papers (
            paper_key text primary key,
            ym text not null,
            arxiv_id text,
            version text,
            title text,
            authors text,
            pdf_path text,
            markdown_path text,
            json_path text,
            image_dir text,
            image_count integer not null,
            metadata_missing integer not null,
            indexed_at text not null
        )
        """
    )
    conn.execute(
        """
        create table assets (
            paper_key text not null,
            kind text not null,
            path text not null,
            size_bytes integer,
            mtime real,
            primary key (paper_key, kind, path)
        )
        """
    )
    conn.execute("create index idx_papers_ym on papers(ym)")
    conn.execute("create index idx_assets_kind on assets(kind)")


def rel(raw_root: Path, path: Path) -> str:
    return path.relative_to(raw_root).as_posix()


def get_row(rows: dict[str, IndexRow], paper_key: str, ym: str) -> IndexRow:
    row = rows.get(paper_key)
    if row is None:
        row = IndexRow(paper_key=paper_key, ym=ym)
        rows[paper_key] = row
    return row


def read_json(path: Path) -> dict:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except Exception:
        return {}


def collect_rows(raw_root: Path) -> tuple[list[IndexRow], list[tuple]]:
    rows: dict[str, IndexRow] = {}
    assets: list[tuple] = []

    for kind, subdir, suffix in (
        ("pdf", "pdf", ".pdf"),
        ("markdown", "markdown", ".md"),
        ("json", "json", ".json"),
    ):
        root = raw_root / subdir
        if not root.is_dir():
            continue
        for path in sorted(root.glob(f"*/*{suffix}"), key=lambda p: p.as_posix()):
            paper_key = path.stem
            ym = path.parent.name
            if ym != ym_from_key(paper_key):
                print(f"skip shard mismatch: {path}")
                continue
            row = get_row(rows, paper_key, ym)
            setattr(row, f"{kind}_path" if kind != "pdf" else "pdf_path", rel(raw_root, path))
            st = path.stat()
            assets.append((paper_key, kind, rel(raw_root, path), st.st_size, st.st_mtime))
            if kind == "json":
                payload = read_json(path)
                row.metadata_missing = int(bool(payload.get("metadata_missing")))
                row.title = payload.get("title")
                row.authors = payload.get("authors")
                row.arxiv_id = payload.get("arxiv_id") or payload.get("id")
                row.version = payload.get("version")

    images_root = raw_root / "images"
    if images_root.is_dir():
        for image_dir in sorted((p for p in images_root.glob("*/*") if p.is_dir()), key=lambda p: p.as_posix()):
            paper_key = image_dir.name
            ym = image_dir.parent.name
            if ym != ym_from_key(paper_key):
                print(f"skip image shard mismatch: {image_dir}")
                continue
            row = get_row(rows, paper_key, ym)
            row.image_dir = rel(raw_root, image_dir)
            image_files = sorted((p for p in image_dir.rglob("*") if p.is_file()), key=lambda p: p.as_posix())
            row.image_count = len(image_files)
            assets.append((paper_key, "image_dir", rel(raw_root, image_dir), 0, image_dir.stat().st_mtime))
            for image_file in image_files:
                st = image_file.stat()
                assets.append((paper_key, "image", rel(raw_root, image_file), st.st_size, st.st_mtime))

    return sorted(rows.values(), key=lambda row: row.paper_key), assets


def rebuild(args: argparse.Namespace) -> int:
    rows, assets = collect_rows(args.raw_root)
    complete_kb = sum(1 for row in rows if row.markdown_path and row.json_path and row.image_dir)
    print(f"paper_rows={len(rows)}")
    print(f"asset_rows={len(assets)}")
    print(f"complete_kb_rows={complete_kb}")
    if not args.execute:
        print("dry-run only; add --execute to replace index.sqlite")
        return 0

    args.raw_root.mkdir(parents=True, exist_ok=True)
    db_path = args.raw_root / "index.sqlite"
    raw_tmp_path = args.raw_root / "index.sqlite.tmp"
    if raw_tmp_path.exists():
        raw_tmp_path.unlink()

    fd, local_tmp_name = tempfile.mkstemp(prefix="quantumatlas-index-", suffix=".sqlite")
    os.close(fd)
    local_tmp_path = Path(local_tmp_name)
    conn = sqlite3.connect(local_tmp_path)
    try:
        ensure_schema(conn)
        indexed_at = datetime.now(timezone.utc).isoformat()
        conn.executemany(
            """
            insert into papers (
                paper_key, ym, arxiv_id, version, title, authors,
                pdf_path, markdown_path, json_path, image_dir,
                image_count, metadata_missing, indexed_at
            ) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            [
                (
                    row.paper_key,
                    row.ym,
                    row.arxiv_id,
                    row.version,
                    row.title,
                    row.authors,
                    row.pdf_path,
                    row.markdown_path,
                    row.json_path,
                    row.image_dir,
                    row.image_count,
                    row.metadata_missing,
                    indexed_at,
                )
                for row in rows
            ],
        )
        conn.executemany(
            """
            insert into assets (paper_key, kind, path, size_bytes, mtime)
            values (?, ?, ?, ?, ?)
            """,
            assets,
        )
        conn.commit()
    finally:
        conn.close()
    shutil.copy2(local_tmp_path, raw_tmp_path)
    os.replace(raw_tmp_path, db_path)
    local_tmp_path.unlink(missing_ok=True)
    print(f"rebuilt {db_path}")
    return 0


def parse_args(argv: Iterable[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--raw-root", type=Path, default=DEFAULT_RAW_ROOT)
    parser.add_argument("--execute", action="store_true")
    return parser.parse_args(argv)


if __name__ == "__main__":
    raise SystemExit(rebuild(parse_args()))
