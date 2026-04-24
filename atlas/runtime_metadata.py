"""Runtime code-version metadata for deployments and asset stores."""

from __future__ import annotations

import json
import os
import subprocess
import tempfile
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, Iterable

from atlas import __version__

MANIFEST_FILENAME = ".quantumatlas-code-version.json"


def _git_output(repo_root: Path, *args: str) -> str | None:
    try:
        result = subprocess.run(
            ["git", *args],
            cwd=str(repo_root),
            capture_output=True,
            check=False,
            text=True,
            timeout=3,
        )
    except (FileNotFoundError, subprocess.SubprocessError):
        return None
    if result.returncode != 0:
        return None
    return result.stdout.strip()


def code_git_info(repo_root: Path) -> Dict[str, Any]:
    """Return safe Git metadata for the application checkout."""
    if _git_output(repo_root, "rev-parse", "--is-inside-work-tree") != "true":
        return {"enabled": False}

    exact_tags = _git_output(repo_root, "tag", "--points-at", "HEAD")
    tags = [tag for tag in (exact_tags or "").splitlines() if tag]
    tag = f"v{__version__}"
    status = _git_output(repo_root, "status", "--porcelain")

    return {
        "enabled": True,
        "branch": _git_output(repo_root, "branch", "--show-current") or None,
        "commit": _git_output(repo_root, "rev-parse", "HEAD") or None,
        "short_commit": _git_output(repo_root, "rev-parse", "--short", "HEAD") or None,
        "dirty": None if status is None else bool(status),
        "exact_tags": tags,
        "expected_tag": tag,
        "on_expected_tag": tag in tags,
    }


def build_code_version_metadata(repo_root: Path) -> Dict[str, Any]:
    """Build the metadata persisted beside raw assets and runtime data."""
    return {
        "schema_version": 1,
        "project": "quantumatlas",
        "version": __version__,
        "tag": f"v{__version__}",
        "git": code_git_info(repo_root),
        "written_at": datetime.now(timezone.utc).isoformat(),
    }


def _atomic_write_json(path: Path, data: Dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    payload = json.dumps(data, indent=2, ensure_ascii=False) + "\n"
    fd, tmp_path_str = tempfile.mkstemp(dir=str(path.parent), suffix=".tmp")
    tmp_path = Path(tmp_path_str)
    try:
        os.write(fd, payload.encode("utf-8"))
        os.close(fd)
        os.replace(tmp_path, path)
    except Exception:
        try:
            os.close(fd)
        except OSError:
            pass
        tmp_path.unlink(missing_ok=True)
        raise


def write_code_version_manifests(
    repo_root: Path,
    roots: Iterable[Path],
) -> Dict[str, Any]:
    """Write code-version manifests into each configured persistent store."""
    metadata = build_code_version_metadata(repo_root)
    for root in roots:
        _atomic_write_json(root / MANIFEST_FILENAME, metadata)
    return metadata


def validate_release_tag(repo_root: Path) -> None:
    """Require the current checkout to be exactly on the pyproject-derived tag."""
    git = code_git_info(repo_root)
    if not git.get("enabled"):
        raise RuntimeError("release tag validation requires a Git checkout")
    tag = f"v{__version__}"
    if tag not in git.get("exact_tags", []):
        short_commit = git.get("short_commit") or "unknown"
        raise RuntimeError(
            f"QuantumAtlas version {__version__} must run from tag {tag}; "
            f"current HEAD {short_commit} is not tagged {tag}"
        )
