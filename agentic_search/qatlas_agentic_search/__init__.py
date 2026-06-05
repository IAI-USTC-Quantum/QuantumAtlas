"""qatlas-agentic-search: academic-style search for QuantumAtlas.

Why this exists
---------------
The Qdrant/bge-m3 RAG path (``qatlas_rag`` + ``internal/routes/rag.go``) ranks
by vector similarity. For *academic* lookup that is the wrong prior: a researcher
wants **exact term/phrase matches** and **citation count**, not fuzzy semantic
neighbours. This package adds a complementary, lexical+citation oriented search.

Layout
------
- ``qatlas_agentic_search.models``   — unified ``Paper`` result + ``SearchQuery``.
- ``qatlas_agentic_search.backends``  — one search *tool* per source (arXiv,
  OpenAlex, Semantic Scholar, Crossref, and the QuantumAtlas internal graph/wiki).
- ``qatlas_agentic_search.ranking``   — lexical (exact-term) + log-citation rank.
- ``qatlas_agentic_search.agent``     — optional Pydantic-AI agent that assembles
  the enabled backends into an agentic workflow (needs the ``agentic-search`` extra).
- ``qatlas_agentic_search.cli``       — ``qatlas-search`` entry point. Has a
  deterministic *direct* mode (no LLM key needed) and an ``--agent`` mode.

Two run modes
-------------
- **direct** (default): run the selected backends concurrently, merge + rank with
  ``ranking``. Pure ``requests``, no LLM, no extra dependency — cheapest/fastest.
- **agent** (``--agent``): an LLM orchestrates the *enabled* tools (the user's
  allow-list still bounds it). Requires the ``agentic-search`` extra.

Config split (see ``config.py``): server URL + bearer token are reused from the
existing ``qatlas`` client YAML config; only third-party API keys and ranking
knobs live under the ``QATLAS_SEARCH_`` env prefix.
"""

from __future__ import annotations

__version__ = "0.1.0"

from qatlas_agentic_search.models import Paper, SearchQuery

__all__ = ["__version__", "Paper", "SearchQuery"]
