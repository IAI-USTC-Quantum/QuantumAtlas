"""Helpers for resolving paper-related local assets."""

from __future__ import annotations

import re
from pathlib import Path
from typing import Dict, Optional

ARXIV_PREFIX_RE = re.compile(r"^arxiv:\s*", re.IGNORECASE)
ARXIV_VERSION_RE = re.compile(r"v\d+$", re.IGNORECASE)


def normalize_arxiv_identifier(value: str) -> str:
    """Normalize an arXiv identifier while preserving category and version."""
    return ARXIV_PREFIX_RE.sub("", value.strip())


def strip_arxiv_version(value: str) -> str:
    """Drop the trailing vN suffix when grouping different versions of the same paper."""
    return ARXIV_VERSION_RE.sub("", normalize_arxiv_identifier(value))


def safe_paper_key(arxiv_id: str) -> str:
    """Filesystem-safe key for local asset storage."""
    return normalize_arxiv_identifier(arxiv_id).replace("/", "__")


def wiki_source_page_id(arxiv_id: str) -> str:
    """Canonical wiki page id for a paper source page."""
    canonical = normalize_arxiv_identifier(arxiv_id)
    return f"paper-arxiv-{canonical.replace('/', '-')}"


def _candidate_stems(arxiv_id: str) -> list[str]:
    canonical = normalize_arxiv_identifier(arxiv_id)
    base = strip_arxiv_version(canonical)
    stems = [
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


def _resolve_existing_file(directory: Path, arxiv_id: str, suffix: str) -> Optional[Path]:
    """Resolve a stored file by trying canonical and safe-key filenames."""
    for stem in _candidate_stems(arxiv_id):
        candidate = directory / f"{stem}.{suffix}"
        if candidate.is_file():
            return candidate

    canonical = normalize_arxiv_identifier(arxiv_id)
    base = strip_arxiv_version(canonical)
    if canonical == base:
        versioned_matches = sorted(directory.glob(f"{safe_paper_key(base)}v*.{suffix}"))
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


def _resolve_existing_dir(directory: Path, key: str, arxiv_id: str) -> Optional[Path]:
    """Resolve a stored directory by trying canonical and safe-key names."""
    candidates = [directory / key]
    for stem in _candidate_stems(arxiv_id):
        candidates.append(directory / stem)

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
    key = json_path.stem if json_path else safe_paper_key(canonical)

    markdown_path = _resolve_existing_file(paper_assets_root / "markdown", canonical, "md")
    pdf_path = _resolve_existing_file(paper_assets_root / "pdf", canonical, "pdf")
    images_dir = _resolve_existing_dir(paper_assets_root / "images", key, canonical)

    return {
        "arxiv_id": canonical,
        "key": key,
        "pdf_path": pdf_path,
        "markdown_path": markdown_path,
        "json_path": json_path,
        "images_dir": images_dir,
    }


def share_path_for_asset(kind: str, key: str, filename: Optional[str] = None) -> str:
    """Return the share-relative path for a paper asset."""
    base = {
        "pdf": f"papers/pdf/{key}.pdf",
        "markdown": f"papers/markdown/{key}.md",
        "json": f"papers/json/{key}.json",
        "images": f"papers/images/{key}",
    }[kind]
    if filename:
        return f"{base}/{filename}"
    return base
