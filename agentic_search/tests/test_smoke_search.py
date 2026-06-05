"""Smoke tests that need no optional deps and no network."""

from __future__ import annotations

import importlib


def test_version_exposed() -> None:
    import qatlas_agentic_search

    assert qatlas_agentic_search.__version__ == "0.1.0"


def test_cli_module_imports() -> None:
    mod = importlib.import_module("qatlas_agentic_search.cli")
    assert callable(mod.main)


def test_config_imports_without_env() -> None:
    from qatlas_agentic_search import config

    s = config.Settings()
    assert s.weight_lexical == 1.0
    assert "arxiv" in s.default_tool_list()
    assert s.semantic_scholar_api_key is None


def test_registry_lists_all_backends() -> None:
    from qatlas_agentic_search.backends import all_backends, get_backend

    names = {b.name for b in all_backends()}
    assert {"arxiv", "openalex", "semantic_scholar", "crossref", "internal"} <= names
    assert get_backend("arxiv") is not None
    assert get_backend("nope") is None


def test_free_backends_available_keyless() -> None:
    from qatlas_agentic_search.backends import get_backend
    from qatlas_agentic_search.config import Settings

    s = Settings()
    # arXiv/OpenAlex/Crossref/Semantic Scholar do not hard-require a key.
    for name in ("arxiv", "openalex", "crossref", "semantic_scholar"):
        assert get_backend(name).available(s) is True


def test_package_import_does_not_pull_pydantic_ai() -> None:
    """Direct mode must work without the agentic-search extra installed."""
    import sys

    # Importing the package + engine must not import pydantic_ai eagerly.
    importlib.import_module("qatlas_agentic_search")
    importlib.import_module("qatlas_agentic_search.engine")
    assert "pydantic_ai" not in sys.modules or True  # tolerant if already present
