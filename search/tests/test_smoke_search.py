"""Smoke tests that need no optional deps and no network."""

from __future__ import annotations

import importlib


def test_version_exposed() -> None:
    import qatlas_search

    assert qatlas_search.__version__ == "0.1.0"


def test_cli_module_imports() -> None:
    mod = importlib.import_module("qatlas_search.cli")
    assert callable(mod.main)


def test_config_imports_without_env() -> None:
    from qatlas_search import config

    s = config.Settings()
    assert s.weight_lexical == 1.0
    assert "arxiv" in s.default_tool_list()
    assert s.semantic_scholar_api_key is None


def test_registry_lists_all_backends() -> None:
    from qatlas_search.backends import all_backends, get_backend

    names = {b.name for b in all_backends()}
    assert {"arxiv", "openalex", "semantic_scholar", "crossref", "internal"} <= names
    assert get_backend("arxiv") is not None
    assert get_backend("nope") is None


def test_free_backends_available_keyless() -> None:
    from qatlas_search.backends import get_backend
    from qatlas_search.config import Settings

    s = Settings()
    # arXiv/OpenAlex/Crossref/Semantic Scholar do not hard-require a key.
    for name in ("arxiv", "openalex", "crossref", "semantic_scholar"):
        assert get_backend(name).available(s) is True


def test_package_has_no_ai_dependency() -> None:
    """The search module is pure infra — it must not import any agent SDK."""
    import sys

    importlib.import_module("qatlas_search")
    importlib.import_module("qatlas_search.engine")
    importlib.import_module("qatlas_search.cli")
    assert "pydantic_ai" not in sys.modules
    assert "claude_agent_sdk" not in sys.modules
