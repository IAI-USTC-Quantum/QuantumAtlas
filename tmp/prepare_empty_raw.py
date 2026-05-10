#!/usr/bin/env python3
"""Move existing QuantumAtlas raw contents into a timestamped backup.

Default mode is dry-run. Add --execute to move entries and verify raw is empty.
"""

from __future__ import annotations

import argparse
import shutil
from datetime import datetime
from pathlib import Path
from typing import Iterable

from raw_migration_common import DEFAULT_BACKUP_ROOT, DEFAULT_RAW_ROOT


def count_descendants(path: Path) -> int:
    if not path.exists():
        return 0
    return sum(1 for _item in path.rglob("*"))


def prepare(args: argparse.Namespace) -> int:
    raw_root: Path = args.raw_root
    backup_root: Path = args.backup_root
    entries = sorted(raw_root.iterdir(), key=lambda p: p.name) if raw_root.is_dir() else []
    descendants = count_descendants(raw_root)

    print(f"raw_root={raw_root}")
    print(f"top_level_entries={len(entries)}")
    print(f"descendant_entries={descendants}")
    for entry in entries[:20]:
        print(f"  {entry.name}")
    if len(entries) > 20:
        print(f"  ... {len(entries) - 20} more top-level entries")

    if not args.execute:
        print("dry-run only; add --execute to move raw contents into a backup")
        return 0

    raw_root.mkdir(parents=True, exist_ok=True)
    timestamp = args.timestamp or datetime.now().strftime("%Y%m%d-%H%M%S")
    backup_dir = backup_root / f"raw-pre-migration-{timestamp}"
    if backup_dir.exists():
        raise SystemExit(f"backup already exists: {backup_dir}")
    backup_dir.mkdir(parents=True)

    for entry in entries:
        shutil.move(str(entry), str(backup_dir / entry.name))
        print(f"moved {entry} -> {backup_dir / entry.name}")

    remaining = sorted(raw_root.iterdir(), key=lambda p: p.name)
    if remaining:
        names = ", ".join(item.name for item in remaining[:10])
        raise SystemExit(f"raw is not empty after backup move: {names}")
    print(f"raw is empty; backup={backup_dir}")
    return 0


def parse_args(argv: Iterable[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--raw-root", type=Path, default=DEFAULT_RAW_ROOT)
    parser.add_argument("--backup-root", type=Path, default=DEFAULT_BACKUP_ROOT)
    parser.add_argument("--timestamp", help="Override backup timestamp suffix")
    parser.add_argument("--execute", action="store_true")
    return parser.parse_args(argv)


if __name__ == "__main__":
    raise SystemExit(prepare(parse_args()))
