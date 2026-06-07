"""qatlas-search: academic-style search for QuantumAtlas.

Why this exists
---------------
The Qdrant/bge-m3 RAG path (``qatlas_rag`` + ``internal/routes/rag.go``) ranks
by vector similarity. For *academic* lookup that is the wrong prior: a researcher
wants **exact term/phrase matches** and **citation count**, not fuzzy semantic
neighbours. This module is a sibling to ``qatlas_rag``: both provide search, but
``search`` retrieves by **strict lexical matching** (title / abstract / metadata
exact terms) plus citation weighting, with no GPU and no vector store.

This is **pure infrastructure** — a base tool, with no AI/LLM dependency. The
agentic / LLM-orchestrated layer lives in a separate repository (``agentic-
search``) that consumes this module's backends as tools.

Layout
------
- ``qatlas_search.models``   — unified ``Paper`` result + ``SearchQuery``.
- ``qatlas_search.backends``  — one search backend per source (arXiv, OpenAlex,
  Semantic Scholar, Crossref, and the QuantumAtlas internal graph/wiki).
- ``qatlas_search.ranking``   — lexical (exact-term) + log-citation rank.
- ``qatlas_search.engine``    — run the selected backends concurrently + rank.
- ``qatlas_search.cli``       — ``qatlas-search`` entry point.

Config (see ``config.py``): all settings come from the qatlas client YAML
(``~/.config/qatlas/config.yaml``). Server URL + bearer token are reused from
``qatlas auth login``; search-specific keys/knobs live under its ``search:``
section.
"""

from __future__ import annotations

__version__ = "0.1.0"

from qatlas_search.models import Paper, SearchQuery

__all__ = ["__version__", "Paper", "SearchQuery"]
