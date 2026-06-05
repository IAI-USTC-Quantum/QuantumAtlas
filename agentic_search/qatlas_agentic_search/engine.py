"""Direct-mode engine: run selected backends concurrently, then rank.

This is the deterministic, no-LLM path (the default). Backends run in a thread
pool because each is a blocking ``requests`` call; a per-backend failure is
isolated by the backend contract (returns ``[]`` + sets ``last_error``), so a
single 429 or timeout never sinks the batch. Results are merged and scored by
``ranking.rank`` — lexical-first, citation-aware.
"""

from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass, field

from qatlas_agentic_search.backends.base import Backend
from qatlas_agentic_search.config import Settings
from qatlas_agentic_search.models import Paper, SearchQuery
from qatlas_agentic_search.ranking import rank


@dataclass
class SearchOutcome:
    papers: list[Paper]
    per_backend_counts: dict[str, int] = field(default_factory=dict)
    errors: dict[str, str] = field(default_factory=dict)


def run_direct(query: SearchQuery, backends: list[Backend], settings: Settings) -> SearchOutcome:
    raw: list[Paper] = []
    counts: dict[str, int] = {}
    errors: dict[str, str] = {}

    if backends:
        with ThreadPoolExecutor(max_workers=len(backends)) as pool:
            futures = {pool.submit(b.search, query, settings): b for b in backends}
            for fut in as_completed(futures):
                b = futures[fut]
                try:
                    found = fut.result()
                except Exception as exc:  # noqa: BLE001 - belt and suspenders
                    found = []
                    errors[b.name] = f"{type(exc).__name__}: {exc}"
                counts[b.name] = len(found)
                if b.last_error:
                    errors[b.name] = b.last_error
                raw += found

    ranked = rank(query, raw, settings)
    return SearchOutcome(papers=ranked, per_backend_counts=counts, errors=errors)
