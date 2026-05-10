#!/usr/bin/env python3
"""Shard KnowledgeBase markdown, JSON metadata, and images into raw.

Default mode is dry-run. Add --execute to copy files and write reports.
"""

from __future__ import annotations

import argparse
import json
import shutil
from collections import Counter
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable

from raw_migration_common import (
    DEFAULT_BULK_ROOT,
    DEFAULT_KB_ROOT,
    DEFAULT_MANIFEST_PATH,
    DEFAULT_METADATA_PATH,
    DEFAULT_RAW_ROOT,
    apply_offset_limit,
    id_aliases,
    latest_mtime,
    load_manifest_versions,
    parse_cutoff,
    parse_id_name,
    split_versioned_key,
    version_number,
    write_tsv,
    ym_from_key,
)


@dataclass(frozen=True)
class KbPaper:
    kb_dir: Path
    markdown_path: Path
    paper_key: str
    arxiv_id: str
    version: str
    ym: str
    image_paths: tuple[Path, ...]
    latest_mtime: float
    explicit_version: bool


def choose_markdown(kb_dir: Path) -> tuple[Path | None, str | None]:
    preferred = kb_dir / f"{kb_dir.name}.md"
    if preferred.is_file():
        return preferred, None
    markdowns = sorted(kb_dir.glob("*.md"), key=lambda p: p.name)
    if len(markdowns) == 1:
        return markdowns[0], None
    if not markdowns:
        return None, "missing markdown"
    return None, "multiple markdown files"


def guessed_pdf_path(bulk_root: Path, paper_key: str) -> Path:
    return bulk_root / f"{paper_key}.pdf"


def find_pdf_by_key(
    bulk_root: Path, paper_key: str, *, deep_search: bool = False
) -> tuple[Path | None, str | None]:
    guessed = guessed_pdf_path(bulk_root, paper_key)
    if guessed.is_file():
        return guessed, None
    legacy_guess = bulk_root / "quant-ph" / "pdf" / ym_from_key(paper_key) / f"{paper_key}.pdf"
    if legacy_guess.is_file():
        return legacy_guess, None
    if not deep_search:
        return None, f"no matching bulk PDF stem for {paper_key}"

    matches = sorted(bulk_root.rglob(f"{paper_key}.pdf"), key=lambda p: p.as_posix())
    if len(matches) == 1:
        return matches[0], None
    if not matches:
        return None, f"no matching bulk PDF stem for {paper_key}"
    return None, f"multiple matching bulk PDFs for {paper_key}"


def highest_bulk_key_for_base(bulk_root: Path, storage_base: str) -> tuple[str | None, str | None]:
    candidates: list[tuple[int, str]] = []
    for path in bulk_root.glob(f"{storage_base}v*.pdf"):
        _base, version = split_versioned_key(path.stem)
        number = version_number(version)
        if number >= 0:
            candidates.append((number, path.stem))

    shard_dir = bulk_root / "quant-ph" / "pdf" / storage_base[:4]
    if shard_dir.is_dir():
        for path in shard_dir.glob(f"{storage_base}v*.pdf"):
            _base, version = split_versioned_key(path.stem)
            number = version_number(version)
            if number >= 0:
                candidates.append((number, path.stem))

    if not candidates:
        return None, "cannot resolve version"
    highest = max(number for number, _stem in candidates)
    top = sorted({stem for number, stem in candidates if number == highest})
    if len(top) != 1:
        return None, f"highest version is not unique for {storage_base}"
    return top[0], None


def resolve_paper_key(
    kb_dir: Path,
    manifest_versions: dict[str, str],
    bulk_root: Path,
    deep_search: bool,
) -> tuple[str | None, str | None, str | None, bool, str | None]:
    parts = parse_id_name(kb_dir.name)
    if parts is None:
        return None, None, None, False, "unsupported id shape"

    if parts.version:
        paper_key = f"{parts.storage_base}{parts.version}"
        explicit_version = True
    else:
        version = manifest_versions.get(parts.arxiv_id)
        if version:
            paper_key = f"{parts.storage_base}{version}"
            explicit_version = False
        else:
            paper_key, error = highest_bulk_key_for_base(bulk_root, parts.storage_base)
            if error:
                return None, None, None, False, error
            assert paper_key is not None
            explicit_version = False

    _pdf_path, error = find_pdf_by_key(bulk_root, paper_key, deep_search=deep_search)
    if error:
        return None, None, None, False, error
    _base, version = split_versioned_key(paper_key)
    if not version:
        return None, None, None, False, "resolved key has no version"

    arxiv_id = parts.arxiv_id
    if arxiv_id == parts.storage_base and len(parts.storage_base) == 7:
        arxiv_id = f"quant-ph/{parts.storage_base}"
    return paper_key, arxiv_id, version, explicit_version, None


def collect_image_paths(kb_dir: Path) -> tuple[Path, ...]:
    images_dir = kb_dir / "images"
    if not images_dir.is_dir():
        return ()
    return tuple(
        sorted(
            (path for path in images_dir.rglob("*") if path.is_file()),
            key=lambda p: p.relative_to(images_dir).as_posix(),
        )
    )


def select_kb_papers(args: argparse.Namespace):
    manifest_versions = load_manifest_versions(args.manifest)
    skipped: list[tuple[str, str, str]] = []
    papers: list[KbPaper] = []

    if not args.kb_root.is_dir():
        raise SystemExit(f"missing KnowledgeBase root: {args.kb_root}")

    cutoff = parse_cutoff(args.before)
    stop_after = args.offset + args.limit if args.limit and cutoff is None else 0
    for kb_dir in sorted((p for p in args.kb_root.iterdir() if p.is_dir()), key=lambda p: p.name):
        markdown_path, markdown_error = choose_markdown(kb_dir)
        if markdown_error:
            skipped.append((kb_dir.name, markdown_error, str(kb_dir)))
            continue

        paper_key, arxiv_id, version, explicit_version, resolve_error = resolve_paper_key(
            kb_dir, manifest_versions, args.bulk_root, args.deep_search_bulk
        )
        if resolve_error:
            skipped.append((kb_dir.name, resolve_error, str(kb_dir)))
            continue
        assert markdown_path is not None
        assert paper_key is not None
        assert arxiv_id is not None
        assert version is not None

        images = collect_image_paths(kb_dir)
        latest = latest_mtime((markdown_path, *images))
        if cutoff is not None and latest > cutoff:
            continue
        papers.append(
            KbPaper(
                kb_dir=kb_dir,
                markdown_path=markdown_path,
                paper_key=paper_key,
                arxiv_id=arxiv_id,
                version=version,
                ym=ym_from_key(paper_key),
                image_paths=images,
                latest_mtime=latest,
                explicit_version=explicit_version,
            )
        )
        if stop_after and len(papers) >= stop_after:
            break

    deduped: dict[str, KbPaper] = {}
    duplicates: dict[str, list[KbPaper]] = {}
    for paper in papers:
        if paper.paper_key in deduped:
            duplicates.setdefault(paper.paper_key, [deduped[paper.paper_key]]).append(paper)
        else:
            deduped[paper.paper_key] = paper

    if duplicates:
        for paper_key, items in duplicates.items():
            explicit = [item for item in items if item.explicit_version]
            if len(explicit) == 1:
                chosen = explicit[0]
                deduped[paper_key] = chosen
                for item in items:
                    if item != chosen:
                        skipped.append(
                            (
                                item.kb_dir.name,
                                f"duplicate KB source for {paper_key}; kept explicit version directory",
                                str(item.kb_dir),
                            )
                        )
            else:
                deduped.pop(paper_key, None)
                for item in items:
                    skipped.append(
                        (
                            item.kb_dir.name,
                            f"ambiguous duplicate KB source for {paper_key}",
                            str(item.kb_dir),
                        )
                    )

    papers = list(deduped.values())
    papers.sort(key=lambda item: item.paper_key)
    papers = apply_offset_limit(papers, args.offset, args.limit)
    return papers, skipped


def target_paths(raw_root: Path, paper: KbPaper) -> tuple[Path, Path, Path]:
    return (
        raw_root / "markdown" / paper.ym / f"{paper.paper_key}.md",
        raw_root / "json" / paper.ym / f"{paper.paper_key}.json",
        raw_root / "images" / paper.ym / paper.paper_key,
    )


def detect_conflicts(args: argparse.Namespace, papers: list[KbPaper]):
    conflicts: list[tuple[str, str, str, str, str, str]] = []
    seen_files: dict[Path, Path] = {}
    seen_dirs: dict[Path, Path] = {}

    for paper in papers:
        md_target, json_target, image_target_dir = target_paths(args.raw_root, paper)
        for kind, source, target in (
            ("markdown", paper.markdown_path, md_target),
            ("json", args.metadata_path, json_target),
        ):
            previous = seen_files.get(target)
            if previous is not None and previous != source:
                conflicts.append(
                    (
                        "migrate_kb_assets",
                        kind,
                        paper.paper_key,
                        str(source),
                        str(target),
                        f"duplicate target also used by {previous}",
                    )
                )
            else:
                seen_files[target] = source
            if target.exists():
                conflicts.append(
                    (
                        "migrate_kb_assets",
                        kind,
                        paper.paper_key,
                        str(source),
                        str(target),
                        "target exists",
                    )
                )

        previous_dir = seen_dirs.get(image_target_dir)
        if previous_dir is not None and previous_dir != paper.kb_dir:
            conflicts.append(
                (
                    "migrate_kb_assets",
                    "images",
                    paper.paper_key,
                    str(paper.kb_dir),
                    str(image_target_dir),
                    f"duplicate image directory also used by {previous_dir}",
                )
            )
        else:
            seen_dirs[image_target_dir] = paper.kb_dir
        if image_target_dir.exists():
            conflicts.append(
                (
                    "migrate_kb_assets",
                    "images",
                    paper.paper_key,
                    str(paper.kb_dir),
                    str(image_target_dir),
                    "target exists",
                )
            )

        image_targets: dict[Path, Path] = {}
        for image_path in paper.image_paths:
            image_target = image_target_dir / image_path.name
            previous = image_targets.get(image_target)
            if previous is not None and previous != image_path:
                conflicts.append(
                    (
                        "migrate_kb_assets",
                        "image",
                        paper.paper_key,
                        str(image_path),
                        str(image_target),
                        f"duplicate image filename also used by {previous}",
                    )
                )
            else:
                image_targets[image_target] = image_path
            if image_target.exists():
                conflicts.append(
                    (
                        "migrate_kb_assets",
                        "image",
                        paper.paper_key,
                        str(image_path),
                        str(image_target),
                        "target exists",
                    )
                )
    return conflicts


def metadata_lookup_keys(paper: KbPaper) -> set[str]:
    keys = {paper.arxiv_id}
    keys.update(id_aliases(paper.arxiv_id))
    base, _version = split_versioned_key(paper.paper_key)
    keys.add(base)
    return keys


def load_metadata_records(metadata_path: Path, papers: list[KbPaper]) -> dict[str, dict]:
    needed: dict[str, str] = {}
    for paper in papers:
        for key in metadata_lookup_keys(paper):
            needed[key] = paper.paper_key
    found: dict[str, dict] = {}
    if not metadata_path.is_file() or not needed:
        return found

    remaining = set(needed)
    with metadata_path.open("r", encoding="utf-8") as f:
        for line_number, line in enumerate(f, 1):
            if not remaining:
                break
            if not line.strip():
                continue
            try:
                record = json.loads(line)
            except json.JSONDecodeError:
                continue
            arxiv_id = record.get("id")
            if not isinstance(arxiv_id, str):
                continue
            aliases = id_aliases(arxiv_id)
            aliases.add(arxiv_id)
            matches = aliases & remaining
            if not matches:
                continue
            for match in matches:
                found[needed[match]] = record
            remaining -= matches
            if line_number % 250000 == 0:
                print(f"metadata scan line={line_number} found={len(found)}/{len(papers)}")
    return found


def json_payload(paper: KbPaper, record: dict | None) -> tuple[dict, bool]:
    base = {
        "paper_key": paper.paper_key,
        "ym": paper.ym,
        "arxiv_id": paper.arxiv_id,
        "version": paper.version,
        "source_paths": {
            "kb_dir": str(paper.kb_dir),
            "markdown": str(paper.markdown_path),
            "images": [str(path) for path in paper.image_paths],
        },
    }
    if record is None:
        return {**base, "metadata_missing": True}, True
    payload = dict(record)
    payload.update(base)
    payload["metadata_missing"] = False
    return payload, False


def safe_copy(source: Path, target: Path) -> None:
    if target.exists() and target.stat().st_size == source.stat().st_size:
        return
    target.parent.mkdir(parents=True, exist_ok=True)
    tmp_path = target.with_name(f".{target.name}.tmp")
    if tmp_path.exists():
        tmp_path.unlink()
    shutil.copy2(source, tmp_path)
    tmp_path.replace(target)


def copy_one_paper(args: argparse.Namespace, paper: KbPaper, record: dict | None):
    md_target, json_target, image_target_dir = target_paths(args.raw_root, paper)
    image_target_dir.mkdir(parents=True, exist_ok=True)

    safe_copy(paper.markdown_path, md_target)
    for image_path in paper.image_paths:
        safe_copy(image_path, image_target_dir / image_path.name)

    payload, missing = json_payload(paper, record)
    json_target.parent.mkdir(parents=True, exist_ok=True)
    tmp_json = json_target.with_name(f".{json_target.name}.tmp")
    tmp_json.write_text(
        json.dumps(payload, ensure_ascii=False, sort_keys=True, indent=2) + "\n",
        encoding="utf-8",
    )
    tmp_json.replace(json_target)
    if missing:
        return (paper.paper_key, paper.arxiv_id, str(json_target), "arxiv record not found")
    return None


def write_reports(
    raw_root: Path,
    papers: list[KbPaper],
    conflicts,
    skipped,
    missing_metadata,
) -> None:
    report_dir = raw_root / "migration-reports"
    counts: dict[str, Counter] = {}
    for paper in papers:
        counter = counts.setdefault(paper.ym, Counter())
        counter["markdown"] += 1
        counter["json"] += 1
        counter["image_dirs"] += 1
        counter["image_files"] += len(paper.image_paths)

    write_tsv(
        report_dir / "kb-counts-by-ym.tsv",
        ["ym", "markdown_count", "json_count", "image_dir_count", "image_file_count"],
        [
            (
                ym,
                counts[ym]["markdown"],
                counts[ym]["json"],
                counts[ym]["image_dirs"],
                counts[ym]["image_files"],
            )
            for ym in sorted(counts)
        ],
    )
    write_tsv(
        report_dir / "conflicts.tsv",
        ["script", "kind", "paper_key", "source_path", "target_path", "reason"],
        conflicts,
    )
    write_tsv(report_dir / "skipped.tsv", ["kb_name", "reason", "detail"], skipped)
    write_tsv(
        report_dir / "missing-metadata.tsv",
        ["paper_key", "arxiv_id", "json_path", "reason"],
        missing_metadata,
    )


def migrate(args: argparse.Namespace) -> int:
    papers, skipped = select_kb_papers(args)
    conflicts = detect_conflicts(args, papers)

    print(f"selected_kb_papers={len(papers)}")
    print(f"skipped={len(skipped)}")
    print(f"conflicts={len(conflicts)}")
    for paper in papers[:20]:
        md_target, json_target, image_target_dir = target_paths(args.raw_root, paper)
        print(
            f"{paper.paper_key}: md -> {md_target.relative_to(args.raw_root)}, "
            f"json -> {json_target.relative_to(args.raw_root)}, "
            f"images={len(paper.image_paths)} -> {image_target_dir.relative_to(args.raw_root)}"
        )
    if len(papers) > 20:
        print(f"... {len(papers) - 20} more selected papers")
    for row in skipped[:20]:
        print("SKIPPED\t" + "\t".join(row))
    for conflict in conflicts[:20]:
        print("CONFLICT\t" + "\t".join(conflict))

    if not args.execute:
        print("dry-run only; add --execute to copy KB assets and write JSON")
        return 0 if not conflicts else 2

    args.raw_root.mkdir(parents=True, exist_ok=True)
    if conflicts:
        write_reports(args.raw_root, papers, conflicts, skipped, [])
        print("stopped before copying because conflicts were found")
        return 2

    metadata_records = load_metadata_records(args.metadata_path, papers)
    missing_metadata = []
    with ThreadPoolExecutor(max_workers=args.workers) as pool:
        futures = {
            pool.submit(copy_one_paper, args, paper, metadata_records.get(paper.paper_key)): paper
            for paper in papers
        }
        for index, future in enumerate(as_completed(futures), 1):
            missing = future.result()
            if missing:
                missing_metadata.append(missing)
            if index % args.progress_every == 0:
                print(f"progress copied_kb_papers={index}/{len(papers)}", flush=True)

    write_reports(args.raw_root, papers, conflicts, skipped, missing_metadata)
    print(f"copied_kb_papers={len(papers)}")
    print(f"missing_metadata={len(missing_metadata)}")
    return 0


def parse_args(argv: Iterable[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--raw-root", type=Path, default=DEFAULT_RAW_ROOT)
    parser.add_argument("--kb-root", type=Path, default=DEFAULT_KB_ROOT)
    parser.add_argument("--bulk-root", type=Path, default=DEFAULT_BULK_ROOT)
    parser.add_argument("--manifest", type=Path, default=DEFAULT_MANIFEST_PATH)
    parser.add_argument("--metadata-path", type=Path, default=DEFAULT_METADATA_PATH)
    parser.add_argument("--before", help="Only include papers whose KB asset mtime is <= this ISO time")
    parser.add_argument("--limit", type=int, default=0)
    parser.add_argument("--offset", type=int, default=0)
    parser.add_argument(
        "--deep-search-bulk",
        action="store_true",
        help="Fallback to bulk/**/*.pdf searches when the known bulk layouts miss",
    )
    parser.add_argument("--workers", type=int, default=8)
    parser.add_argument("--progress-every", type=int, default=500)
    parser.add_argument("--execute", action="store_true")
    return parser.parse_args(argv)


if __name__ == "__main__":
    raise SystemExit(migrate(parse_args()))
