"""Helpers for resolving paper-related local assets."""

from __future__ import annotations

import re
from pathlib import Path
from typing import Dict, Optional

ARXIV_PREFIX_RE = re.compile(r"^arxiv:\s*", re.IGNORECASE)
ARXIV_VERSION_RE = re.compile(r"v\d+$", re.IGNORECASE)
OLD_STYLE_ID_RE = re.compile(r"^\d{7}(?:v\d+)?$", re.IGNORECASE)
SHARDED_KEY_RE = re.compile(r"^\d{4}")


def normalize_arxiv_identifier(value: str) -> str:
    """Normalize an arXiv identifier while preserving category and version."""
    return ARXIV_PREFIX_RE.sub("", value.strip())


def strip_arxiv_version(value: str) -> str:
    """Drop the trailing vN suffix when grouping different versions of the same paper."""
    return ARXIV_VERSION_RE.sub("", normalize_arxiv_identifier(value))


def safe_paper_key(arxiv_id: str) -> str:
    """Filesystem-safe key for local asset storage."""
    return normalize_arxiv_identifier(arxiv_id).replace("/", "__")


def paper_storage_key(arxiv_id: str) -> str:
    """Return the canonical raw storage key for an arXiv identifier."""
    canonical = normalize_arxiv_identifier(arxiv_id)
    if "/" in canonical:
        categoryless = canonical.split("/", 1)[1]
        if OLD_STYLE_ID_RE.match(categoryless):
            return categoryless
    return safe_paper_key(canonical)


def paper_shard(key: str) -> Optional[str]:
    """Return the raw shard prefix for a storage key when present."""
    if SHARDED_KEY_RE.match(key):
        return key[:4]
    return None


def paper_asset_path(paper_assets_root: Path, kind: str, arxiv_id: str) -> Path:
    """Return the canonical sharded asset path for new writes."""
    key = paper_storage_key(arxiv_id)
    shard = paper_shard(key)
    directory = paper_assets_root / kind / shard if shard else paper_assets_root / kind
    if kind == "pdf":
        return directory / f"{key}.pdf"
    if kind == "markdown":
        return directory / f"{key}.md"
    if kind == "json":
        return directory / f"{key}.json"
    if kind == "images":
        return directory / f"{key}.zip"
    raise ValueError(f"unknown paper asset kind: {kind}")


def wiki_source_page_id(arxiv_id: str) -> str:
    """Canonical wiki page id for a paper source page."""
    canonical = normalize_arxiv_identifier(arxiv_id)
    return f"paper-arxiv-{canonical.replace('/', '-')}"


def _candidate_stems(arxiv_id: str) -> list[str]:
    canonical = normalize_arxiv_identifier(arxiv_id)
    base = strip_arxiv_version(canonical)
    stems = [
        paper_storage_key(canonical),
        canonical,
        safe_paper_key(canonical),
        base,
        safe_paper_key(base),
    ]

    if "/" in canonical:
        categoryless = canonical.split("/", 1)[1]
        categoryless_base = strip_arxiv_version(categoryless)
        stems.extend([
            categoryless,
            safe_paper_key(categoryless),
            categoryless_base,
            safe_paper_key(categoryless_base),
        ])

    seen = set()
    out = []
    for stem in stems:
        if stem and stem not in seen:
            out.append(stem)
            seen.add(stem)
    return out


def _candidate_file_paths(directory: Path, stem: str, suffix: str) -> list[Path]:
    paths = [directory / f"{stem}.{suffix}"]
    shard = paper_shard(stem)
    if shard:
        paths.append(directory / shard / f"{stem}.{suffix}")
    return paths


def _versioned_file_matches(directory: Path, stem: str, suffix: str) -> list[Path]:
    matches = sorted(directory.glob(f"{stem}v*.{suffix}"))
    shard = paper_shard(stem)
    if shard:
        matches.extend(sorted((directory / shard).glob(f"{stem}v*.{suffix}")))
    return matches


def _resolve_existing_file(directory: Path, arxiv_id: str, suffix: str) -> Optional[Path]:
    """Resolve a stored file by trying canonical and safe-key filenames."""
    for stem in _candidate_stems(arxiv_id):
        for candidate in _candidate_file_paths(directory, stem, suffix):
            if candidate.is_file():
                return candidate

    canonical = normalize_arxiv_identifier(arxiv_id)
    base = strip_arxiv_version(canonical)
    for stem in _candidate_stems(base):
        versioned_matches = _versioned_file_matches(directory, stem, suffix)
        if len(versioned_matches) == 1:
            return versioned_matches[0]

    if "/" not in canonical and "." not in canonical:
        matches = sorted(directory.glob(f"*__{canonical}.{suffix}"))
        if len(matches) == 1:
            return matches[0]
        versioned_matches = sorted(directory.glob(f"*__{canonical}v*.{suffix}"))
        if len(versioned_matches) == 1:
            return versioned_matches[0]
    return None


def _candidate_dir_paths(directory: Path, stem: str) -> list[Path]:
    paths = [directory / stem]
    shard = paper_shard(stem)
    if shard:
        paths.append(directory / shard / stem)
    return paths


def _resolve_existing_dir(directory: Path, key: str, arxiv_id: str) -> Optional[Path]:
    """Resolve a stored directory by trying canonical and safe-key names."""
    candidates = _candidate_dir_paths(directory, key)
    for stem in _candidate_stems(arxiv_id):
        candidates.extend(_candidate_dir_paths(directory, stem))

    seen = set()
    for candidate in candidates:
        label = str(candidate)
        if label in seen:
            continue
        seen.add(label)
        if candidate.is_dir():
            return candidate
    return None


def resolve_paper_assets(paper_assets_root: Path, arxiv_id: str) -> Dict[str, Optional[Path | str]]:
    """Resolve local paper asset paths from the canonical paper asset store only."""
    canonical = normalize_arxiv_identifier(arxiv_id)

    json_path = _resolve_existing_file(paper_assets_root / "json", canonical, "json")
    markdown_path = _resolve_existing_file(paper_assets_root / "markdown", canonical, "md")
    pdf_path = _resolve_existing_file(paper_assets_root / "pdf", canonical, "pdf")
    key_source = json_path or markdown_path or pdf_path
    key = key_source.stem if key_source else paper_storage_key(canonical)
    images_dir = _resolve_existing_dir(paper_assets_root / "images", key, canonical)

    return {
        "arxiv_id": canonical,
        "key": key,
        "pdf_path": pdf_path,
        "markdown_path": markdown_path,
        "json_path": json_path,
        "images_dir": images_dir,
    }
