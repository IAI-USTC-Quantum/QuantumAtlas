"""Configuration for the qatlas search module.

Aligned to the qatlas **client** convention (v0.17.0+): the client is configured
by a single YAML file at ``~/.config/qatlas/config.yaml`` (run ``qatlas config
path`` to locate it), not env vars / ``.env``. qatlas-search is a client-side
tool, so it follows suit:

* **Search-specific** settings live under a dedicated ``search:`` section of that
  file (third-party API keys, polite-pool emails, default tool selection,
  ranking weights). ``qatlas-search`` owns this namespace and does not pollute
  the top-level client schema (``ServerConfig``).
* **Server URL + bearer token** for the internal backend are still resolved from
  the qatlas client auth (``qatlas auth login`` → ``hosts.yml``);
  ``search.server_url`` / ``search.token`` are optional overrides.

No env vars, no ``.env`` — matching the client. Tests construct ``Settings(...)``
directly.
"""

from __future__ import annotations

from typing import Any, Optional

from pydantic import BaseModel


class Settings(BaseModel):
    # --- third-party API keys (all optional; arXiv/OpenAlex/Crossref need none) ---
    semantic_scholar_api_key: Optional[str] = None

    # Contact emails opt into the OpenAlex / Crossref "polite pool" (faster,
    # more reliable). Strongly recommended but not required.
    openalex_email: Optional[str] = None
    crossref_email: Optional[str] = None

    # --- internal backend: optional overrides; default to the qatlas client ---
    server_url: Optional[str] = None
    token: Optional[str] = None
    # If set to an existing path, the internal backend additionally greps the
    # local wiki checkout for exact title/body matches. Off by default because
    # client users are usually not on the server host.
    wiki_dir: Optional[str] = None

    # --- tool selection / budget ---
    # Comma-separated default allow-list. Crossref is off by default (noisier,
    # broad cross-discipline) but available via --tools.
    default_tools: str = "arxiv,openalex,semantic_scholar,internal"
    max_results_per_tool: int = 10
    request_timeout: float = 20.0

    # --- ranking weights (the academic prior: lexical first, citations next) ---
    weight_lexical: float = 1.0
    weight_citation: float = 0.6
    weight_recency: float = 0.2

    def default_tool_list(self) -> list[str]:
        return [t.strip() for t in self.default_tools.split(",") if t.strip()]


_FIELDS = set(Settings.model_fields)


def _coerce(value: Any) -> Any:
    """Treat an empty YAML string as unset (uniform None)."""
    return None if value == "" else value


def get_settings() -> Settings:
    """Build :class:`Settings` from the qatlas client ``config.yaml``.

    Reads the ``search:`` section for search-specific fields. A missing file or
    section just yields built-in defaults — never raises.
    """
    data: dict[str, Any] = {}
    try:
        import yaml

        from qatlas.paths import user_config_yaml_path

        path = user_config_yaml_path()
        if path.is_file():
            raw = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
            if isinstance(raw, dict):
                section = raw.get("search") or {}
                if isinstance(section, dict):
                    for key, value in section.items():
                        if key in _FIELDS:
                            coerced = _coerce(value)
                            if coerced is not None:
                                data[key] = coerced
    except Exception:
        data = {}
    return Settings(**data)
