"""
QuantumAtlas - AI 驱动的量子算法开发辅助系统

核心功能：从论文到可执行量子代码的完整转化链路
"""

from __future__ import annotations

import tomllib
from importlib.metadata import PackageNotFoundError, version
from pathlib import Path


def _version_from_pyproject() -> str:
    """Read [project].version; Commitizen bumps this field via pep621."""
    pyproject = Path(__file__).resolve().parents[1] / "pyproject.toml"
    with pyproject.open("rb") as f:
        data = tomllib.load(f)
    return str(data["project"]["version"])


def _resolve_version() -> str:
    """Prefer repo pyproject when present, else installed distribution metadata."""
    try:
        return _version_from_pyproject()
    except (FileNotFoundError, KeyError, OSError):
        try:
            return version("quantum-atlas")
        except PackageNotFoundError:
            return "0+unknown"


__version__ = _resolve_version()

__all__ = ["__version__"]
