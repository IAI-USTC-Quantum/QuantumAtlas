#!/usr/bin/env python3
"""Shared helpers for one-off QuantumAtlas raw migration scripts."""

from __future__ import annotations

import csv
import json
import os
import re
from collections import defaultdict
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Iterable


DEFAULT_RAW_ROOT = Path("/mnt/team/QuantumAtlas/raw")
DEFAULT_BACKUP_ROOT = Path("/mnt/team/QuantumAtlas/backups")
DEFAULT_BULK_ROOT = Path("/mnt/team/Papercrawl/bulk")
DEFAULT_KB_ROOT = Path("/mnt/team/KnowledgeBase/Obsidian/papers")
DEFAULT_MANIFEST_PATH = Path(
    "/mnt/team/Papercrawl/arxiv-metadata/quant-ph-download-manifest.json"
)
DEFAULT_METADATA_PATH = Path(
    "/mnt/team/Papercrawl/arxiv-metadata/arxiv-metadata-oai-snapshot.json"
)

VERSION_RE = re.compile(r"v(?P<num>\d+)$")
NEW_ID_RE = re.compile(r"^(?P<id>\d{4}\.\d{4,5})(?P<version>v\d+)?$")
OLD_WITH_CATEGORY_RE = re.compile(
    r"^(?P<category>[A-Za-z-]+)(?:__|_)(?P<num>\d{7})(?P<version>v\d+)?$"
)
OLD_NUMERIC_RE = re.compile(r"^(?P<num>\d{7})(?P<version>v\d+)?$")


@dataclass(frozen=True)
class IdParts:
    raw_name: str
    arxiv_id: str
    storage_base: str
    version: str | None


@dataclass(frozen=True)
class BulkPdf:
    source_path: Path
    paper_key: str
    ym: str
    target_path: Path
    mtime: float
    size: int


def parse_cutoff(value: str | None) -> float | None:
    if not value:
        return None
    raw = value.strip()
    if raw.endswith("Z"):
        raw = raw[:-1] + "+00:00"
    dt = datetime.fromisoformat(raw)
    if dt.tzinfo is None:
        dt = dt.astimezone()
    return dt.timestamp()


def id_aliases(arxiv_id: str) -> set[str]:
    aliases = {arxiv_id}
    if "/" in arxiv_id:
        category, num = arxiv_id.split("/", 1)
        aliases.add(num)
        aliases.add(f"{category}_{num}")
        aliases.add(f"{category}__{num}")
    return aliases


def parse_id_name(raw_name: str) -> IdParts | None:
    path_name = Path(raw_name).name.strip()
    if Path(path_name).suffix.lower() in {".pdf", ".md", ".json"}:
        name = Path(path_name).stem
    else:
        name = path_name
    m = NEW_ID_RE.match(name)
    if m:
        arxiv_id = m.group("id")
        return IdParts(name, arxiv_id, arxiv_id, m.group("version"))

    m = OLD_WITH_CATEGORY_RE.match(name)
    if m:
        category = m.group("category").lower()
        num = m.group("num")
        return IdParts(name, f"{category}/{num}", num, m.group("version"))

    m = OLD_NUMERIC_RE.match(name)
    if m:
        num = m.group("num")
        return IdParts(name, num, num, m.group("version"))

    return None


def split_versioned_key(paper_key: str) -> tuple[str, str | None]:
    m = VERSION_RE.search(paper_key)
    if not m:
        return paper_key, None
    return paper_key[: m.start()], paper_key[m.start() :]


def version_number(version: str | None) -> int:
    if not version:
        return -1
    m = VERSION_RE.fullmatch(version)
    if not m:
        return -1
    return int(m.group("num"))


def ym_from_key(paper_key: str) -> str:
    if len(paper_key) < 4 or not paper_key[:4].isdigit():
        raise ValueError(f"paper key does not start with YYYY/MM shard digits: {paper_key}")
    return paper_key[:4]


def apply_offset_limit(items: list, offset: int, limit: int | None) -> list:
    if offset:
        items = items[offset:]
    if limit:
        items = items[:limit]
    return items


def write_tsv(path: Path, header: list[str], rows: Iterable[Iterable[object]]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8", newline="") as f:
        writer = csv.writer(f, delimiter="\t", lineterminator="\n")
        writer.writerow(header)
        for row in rows:
            writer.writerow(["" if value is None else value for value in row])


def load_manifest_versions(path: Path) -> dict[str, str]:
    if not path.is_file():
        return {}
    raw = json.loads(path.read_text(encoding="utf-8"))
    versions: dict[str, str] = {}
    for item in raw.get("items", []):
        if not isinstance(item, dict):
            continue
        arxiv_id = item.get("arxiv_id")
        version = item.get("version")
        if not isinstance(arxiv_id, str) or not isinstance(version, str):
            continue
        for alias in id_aliases(arxiv_id):
            versions[alias] = version
    return versions


def iter_bulk_pdfs(bulk_root: Path, raw_root: Path, max_items: int = 0) -> list[BulkPdf]:
    pdfs: list[BulkPdf] = []
    if not bulk_root.is_dir():
        return pdfs
    for dirpath, dirnames, filenames in os.walk(bulk_root):
        dirnames.sort()
        for filename in sorted(filenames):
            if not filename.endswith(".pdf"):
                continue
            path = Path(dirpath) / filename
            paper_key = path.stem
            try:
                ym = ym_from_key(paper_key)
            except ValueError:
                continue
            st = path.stat()
            pdfs.append(
                BulkPdf(
                    source_path=path,
                    paper_key=paper_key,
                    ym=ym,
                    target_path=raw_root / "pdf" / ym / path.name,
                    mtime=st.st_mtime,
                    size=st.st_size,
                )
            )
            if max_items and len(pdfs) >= max_items:
                return pdfs
    return pdfs


def build_bulk_version_index(bulk_root: Path) -> tuple[set[str], dict[str, str]]:
    stems: set[str] = set()
    versions_by_base: dict[str, list[tuple[int, str]]] = defaultdict(list)
    if not bulk_root.is_dir():
        return stems, {}

    for path in sorted(bulk_root.rglob("*.pdf"), key=lambda p: p.as_posix()):
        stem = path.stem
        stems.add(stem)
        base, version = split_versioned_key(stem)
        number = version_number(version)
        if number >= 0:
            versions_by_base[base].append((number, stem))

    highest: dict[str, str] = {}
    for base, values in versions_by_base.items():
        max_number = max(number for number, _stem in values)
        top = sorted({stem for number, stem in values if number == max_number})
        if len(top) == 1:
            highest[base] = top[0]
    return stems, highest


def latest_mtime(paths: Iterable[Path]) -> float:
    values = []
    for path in paths:
        try:
            values.append(path.stat().st_mtime)
        except FileNotFoundError:
            continue
    return max(values) if values else 0.0
