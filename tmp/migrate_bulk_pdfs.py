#!/usr/bin/env python3
"""Shard Papercrawl bulk PDFs into QuantumAtlas raw/pdf/{ym}/{paper_key}.pdf.

Default mode is dry-run. Add --execute to copy files and write migration reports.
"""

from __future__ import annotations

import argparse
import shutil
from collections import Counter
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path
from typing import Iterable

from raw_migration_common import (
    DEFAULT_BULK_ROOT,
    DEFAULT_RAW_ROOT,
    apply_offset_limit,
    iter_bulk_pdfs,
    parse_cutoff,
    write_tsv,
)


def select_pdfs(args: argparse.Namespace):
    cutoff = parse_cutoff(args.before)
    max_items = args.offset + args.limit if args.limit and cutoff is None else 0
    pdfs = iter_bulk_pdfs(args.bulk_root, args.raw_root, max_items=max_items)
    if cutoff is not None:
        pdfs = [item for item in pdfs if item.mtime <= cutoff]
    pdfs.sort(key=lambda item: item.source_path.as_posix())
    return apply_offset_limit(pdfs, args.offset, args.limit)


def detect_conflicts(pdfs) -> list[tuple[str, str, str, str, str, str]]:
    conflicts: list[tuple[str, str, str, str, str, str]] = []
    seen: dict[Path, Path] = {}
    for item in pdfs:
        previous = seen.get(item.target_path)
        if previous is not None and previous != item.source_path:
            conflicts.append(
                (
                    "migrate_bulk_pdfs",
                    "pdf",
                    item.paper_key,
                    str(item.source_path),
                    str(item.target_path),
                    f"duplicate target also used by {previous}",
                )
            )
        else:
            seen[item.target_path] = item.source_path
        if item.target_path.exists():
            if item.target_path.stat().st_size == item.size:
                continue
            conflicts.append(
                (
                    "migrate_bulk_pdfs",
                    "pdf",
                    item.paper_key,
                    str(item.source_path),
                    str(item.target_path),
                    "target exists with different size",
                )
            )
    return conflicts


def write_reports(raw_root: Path, pdfs, conflicts) -> None:
    report_dir = raw_root / "migration-reports"
    counts = Counter(item.ym for item in pdfs)
    write_tsv(
        report_dir / "pdf-counts-by-ym.tsv",
        ["ym", "pdf_count"],
        [(ym, counts[ym]) for ym in sorted(counts)],
    )
    write_tsv(
        report_dir / "conflicts.tsv",
        ["script", "kind", "paper_key", "source_path", "target_path", "reason"],
        conflicts,
    )


def copy_one(item) -> bool:
    if item.target_path.exists() and item.target_path.stat().st_size == item.size:
        return False
    item.target_path.parent.mkdir(parents=True, exist_ok=True)
    tmp_path = item.target_path.with_name(f".{item.target_path.name}.tmp")
    if tmp_path.exists():
        tmp_path.unlink()
    shutil.copy2(item.source_path, tmp_path)
    tmp_path.replace(item.target_path)
    return True


def migrate(args: argparse.Namespace) -> int:
    pdfs = select_pdfs(args)
    conflicts = detect_conflicts(pdfs)
    counts = Counter(item.ym for item in pdfs)

    print(f"selected_pdfs={len(pdfs)}")
    print(f"ym_shards={len(counts)}")
    print(f"conflicts={len(conflicts)}")
    for ym, count in list(sorted(counts.items()))[:20]:
        print(f"  {ym}\t{count}")
    if len(counts) > 20:
        print(f"  ... {len(counts) - 20} more shards")
    for conflict in conflicts[:20]:
        print("CONFLICT\t" + "\t".join(conflict))
    if len(conflicts) > 20:
        print(f"... {len(conflicts) - 20} more conflicts")

    if not args.execute:
        print("dry-run only; add --execute to copy PDFs")
        return 0 if not conflicts else 2

    args.raw_root.mkdir(parents=True, exist_ok=True)
    write_reports(args.raw_root, pdfs, conflicts)
    if conflicts:
        print("stopped before copying because conflicts were found")
        return 2

    copied = 0
    skipped_existing = 0
    with ThreadPoolExecutor(max_workers=args.workers) as pool:
        futures = {pool.submit(copy_one, item): item for item in pdfs}
        for index, future in enumerate(as_completed(futures), 1):
            if future.result():
                copied += 1
            else:
                skipped_existing += 1
            if index % args.progress_every == 0:
                print(
                    f"progress processed={index}/{len(pdfs)} "
                    f"copied={copied} skipped_existing={skipped_existing}",
                    flush=True,
                )
    print(f"copied_pdfs={copied}")
    print(f"skipped_existing={skipped_existing}")
    return 0


def parse_args(argv: Iterable[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--raw-root", type=Path, default=DEFAULT_RAW_ROOT)
    parser.add_argument("--bulk-root", type=Path, default=DEFAULT_BULK_ROOT)
    parser.add_argument("--before", help="Only include PDFs whose mtime is <= this ISO time")
    parser.add_argument("--limit", type=int, default=0)
    parser.add_argument("--offset", type=int, default=0)
    parser.add_argument("--workers", type=int, default=8)
    parser.add_argument("--progress-every", type=int, default=5000)
    parser.add_argument("--execute", action="store_true")
    return parser.parse_args(argv)


if __name__ == "__main__":
    raise SystemExit(migrate(parse_args()))
