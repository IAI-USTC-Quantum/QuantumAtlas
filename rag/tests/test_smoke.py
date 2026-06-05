"""Sanity tests that don't need the optional deps to be installed."""

from __future__ import annotations

import importlib

import qatlas_rag


def test_version_exposed() -> None:
    assert qatlas_rag.__version__ == "0.1.0"


def test_cli_module_imports() -> None:
    mod = importlib.import_module("qatlas_rag.cli")
    assert callable(mod.main)


def test_config_imports_without_env() -> None:
    """Settings should construct with defaults (no env vars set)."""
    from qatlas_rag import config

    s = config.Settings()
    assert s.qdrant_collection == "qatlas_papers_v1"
    assert s.embed_model == "BAAI/bge-m3"
    assert s.s3_md_bucket == "qatlas-md"
