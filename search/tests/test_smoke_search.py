"""Smoke tests that need no optional deps and no network."""

from __future__ import annotations

import importlib


def test_version_exposed() -> None:
    import qatlas_search

    assert qatlas_search.__version__ == "0.1.0"


def test_cli_module_imports() -> None:
    mod = importlib.import_module("qatlas_search.cli")
    assert callable(mod.main)


def test_config_imports_with_defaults() -> None:
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


def test_get_settings_reads_yaml_search_section(tmp_path, monkeypatch) -> None:
    """Config aligns to the qatlas client YAML: a `search:` section is read."""
    import qatlas.paths as qpaths
    from qatlas_search import config as cfg

    yaml_file = tmp_path / "config.yaml"
    yaml_file.write_text(
        "server_url: https://example.test\n"  # top-level client key, ignored here
        "search:\n"
        "  openalex_email: you@example.com\n"
        "  max_results_per_tool: 3\n"
        "  default_tools: arxiv,openalex\n"
        "  weight_citation: 0.9\n",
        encoding="utf-8",
    )
    monkeypatch.setattr(qpaths, "user_config_yaml_path", lambda: yaml_file)

    s = cfg.get_settings()
    assert s.openalex_email == "you@example.com"
    assert s.max_results_per_tool == 3
    assert s.default_tool_list() == ["arxiv", "openalex"]
    assert s.weight_citation == 0.9
    assert s.weight_lexical == 1.0  # untouched field keeps default


def test_get_settings_missing_file_yields_defaults(tmp_path, monkeypatch) -> None:
    import qatlas.paths as qpaths
    from qatlas_search import config as cfg

    monkeypatch.setattr(qpaths, "user_config_yaml_path", lambda: tmp_path / "absent.yaml")
    s = cfg.get_settings()
    assert s.max_results_per_tool == 10
    assert s.semantic_scholar_api_key is None


def test_request_with_retry_retries_on_429_then_succeeds(monkeypatch) -> None:
    from qatlas_search.backends import base
    from qatlas_search.config import Settings

    class _Resp:
        def __init__(self, status):
            self.status_code = status
            self.headers = {}

        def raise_for_status(self):
            if self.status_code >= 400:
                raise AssertionError("should not raise on 200")

    calls = {"n": 0}

    def fake_request(method, url, **kwargs):
        calls["n"] += 1
        return _Resp(429 if calls["n"] == 1 else 200)

    monkeypatch.setattr(base.requests, "request", fake_request)
    monkeypatch.setattr(base.time, "sleep", lambda *_: None)

    resp = base.request_with_retry("GET", "http://x", settings=Settings())
    assert resp.status_code == 200
    assert calls["n"] == 2  # retried once after the 429
