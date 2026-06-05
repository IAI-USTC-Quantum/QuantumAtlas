"""Configuration for qatlas-agentic-search.

Config split (deliberate — see README):

* **Server URL + bearer token** are *not* defined here. The internal backend
  reuses the existing ``qatlas`` client config (``~/.config/qatlas/config.yaml``
  + ``hosts.yml``), so a user who already ran ``qatlas auth login`` gets internal
  search for free. ``QATLAS_SEARCH_SERVER_URL`` / ``QATLAS_SEARCH_TOKEN`` are
  optional overrides for standalone use.
* **Everything search-specific** — third-party API keys, contact emails for the
  OpenAlex/Crossref polite pools, the LLM model for agent mode, default tool
  selection, and ranking weights — lives under the ``QATLAS_SEARCH_`` env prefix,
  mirroring how ``qatlas_rag`` uses ``QATLAS_RAG_``.
"""

from __future__ import annotations

from typing import Optional

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_prefix="QATLAS_SEARCH_",
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
    )

    # --- third-party API keys (all optional; backends self-disable without them
    #     only when a key is mandatory; arXiv/OpenAlex/Crossref need none) ---
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

    # --- agent mode (only used with --agent; needs the agentic-search extra) ---
    # Pydantic-AI model string. Defaults to a cheap OpenAI model; any
    # OpenAI-compatible endpoint works via OPENAI_BASE_URL (OpenRouter, Groq,
    # local vLLM/Ollama), which is how cost is balanced. Anthropic via
    # pydantic-ai needs anthropic>=0.61 (conflicts with this repo's pin), so
    # only the OpenAI provider is shipped today.
    llm_model: str = "openai:gpt-4o-mini"

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


def get_settings() -> Settings:
    return Settings()
