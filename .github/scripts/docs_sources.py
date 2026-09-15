#!/usr/bin/env python3
"""Prepare/verify exact component checkouts; Sphinx does the actual aggregation."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[2]
LOCK = ROOT / "docsite/components.lock.json"
NAMES = ("qatlas-cli", "qatlas-search", "qatlas-rag")
IGNORE = {"conf.py", "check_build.py", "build.py", "build.py.lock", "requirements.in", "requirements.txt", "__pycache__", "_build"}


def load_lock(path: Path = LOCK) -> list[dict]:
    data = json.loads(path.read_text(encoding="utf-8"))
    entries = data.get("components", [])
    if data.get("schema") != 1 or [entry.get("name") for entry in entries] != list(NAMES):
        raise ValueError("Expected schema 1 and the three explicit component names")
    for entry in entries:
        if entry.get("repository") != f"IAI-USTC-Quantum/{entry['name']}":
            raise ValueError("Unexpected component repository")
        if not re.fullmatch(r"[0-9a-f]{40}", entry.get("sha", "")):
            raise ValueError("Component refs must be full immutable commit SHAs")
        if entry.get("docs_dir") != "docs":
            raise ValueError("Components must use their normal docs/ directory")
    return entries


def git(path: Path, *args: str) -> str:
    env = {**os.environ, "GIT_TERMINAL_PROMPT": "0"}
    return subprocess.check_output(["git", "-C", str(path), *args], env=env, text=True).strip()


def component_root() -> Path:
    path = Path(os.environ.get("QATLAS_DOCS_COMPONENTS", str(ROOT / "build/docs-components"))).resolve()
    if not path.is_relative_to((ROOT / "build").resolve()) or path == (ROOT / "build").resolve():
        raise ValueError("Component checkouts must live in a dedicated directory under build/")
    return path


def checkout(transport: str) -> None:
    root = component_root()
    for entry in load_lock():
        target = root / entry["name"]
        if target.is_symlink():
            raise ValueError(f"Refusing symlink checkout: {target}")
        if not target.exists():
            target.mkdir(parents=True)
            git(target, "init", "--quiet")
        elif not (target / ".git").is_dir():
            raise ValueError(f"Refusing existing non-checkout directory: {target}")
        if git(target, "status", "--porcelain"):
            raise ValueError(f"Refusing to overwrite modified checkout: {target}")
        remote = f"https://github.com/{entry['repository']}.git" if transport == "https" else f"git@github.com:{entry['repository']}.git"
        git(target, "fetch", "--depth=1", remote, entry["sha"])
        git(target, "checkout", "--quiet", "--detach", entry["sha"])
        print(f"Prepared {entry['name']} at {entry['sha']}")
    validate()


def validate() -> list[dict]:
    records = []
    root = component_root()
    for entry in load_lock():
        repo = root / entry["name"]
        if repo.is_symlink() or not (repo / ".git").is_dir():
            raise ValueError(f"Missing checkout: {repo}; run docs_sources.py checkout first")
        if git(repo, "rev-parse", "HEAD") != entry["sha"]:
            raise ValueError(f"Wrong component SHA: {entry['name']}")
        if git(repo, "status", "--porcelain", "--untracked-files=all", "--", "docs"):
            raise ValueError(f"Modified component documentation: {entry['name']}")
        for required in ("conf.py", "index.rst", "overview.md"):
            if not (repo / "docs" / required).is_file():
                raise ValueError(f"Missing {entry['name']}/docs/{required}")
        files = {}
        for item in sorted((repo / "docs").rglob("*")):
            relative = item.relative_to(repo / "docs")
            if any(part in IGNORE for part in relative.parts):
                continue
            if item.is_symlink():
                raise ValueError(f"Symlinks are not documentation inputs: {item}")
            if item.is_file():
                files[str(relative)] = hashlib.sha256(item.read_bytes()).hexdigest()
        records.append({**entry, "files": files})
    return records


def stamp(output: Path) -> None:
    # This is provenance only, not a renderer or a Sphinx extension.
    components = validate()
    manifest = {
        "schema": 1,
        "host_repository": "IAI-USTC-Quantum/QuantumAtlas",
        "host_sha": git(ROOT, "rev-parse", "HEAD"),
        "host_dirty": bool(git(ROOT, "status", "--porcelain", "--untracked-files=no", "--", "docsite", ".github/scripts", ".github/actions", ".github/workflows")),
        "source_date_epoch": int(os.environ["SOURCE_DATE_EPOCH"]),
        "components": components,
    }
    for site in ("doc", "devdoc"):
        path = output / site / "_static/docs-build.json"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    (output / "VERSION").write_text(manifest["host_sha"] + "\n", encoding="utf-8")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    fetch = sub.add_parser("checkout")
    fetch.add_argument("--transport", choices=("https", "ssh"), default="https")
    sub.add_parser("validate")
    sub.add_parser("ci-outputs")
    stamping = sub.add_parser("stamp")
    stamping.add_argument("output", type=Path)
    args = parser.parse_args()
    if args.command == "checkout":
        checkout(args.transport)
    elif args.command == "validate":
        print(json.dumps(validate(), ensure_ascii=False, indent=2))
    elif args.command == "ci-outputs":
        for entry in load_lock():
            key = entry["name"].removeprefix("qatlas-")
            print(f"{key}_sha={entry['sha']}")
    else:
        stamp(args.output)


if __name__ == "__main__":
    main()
