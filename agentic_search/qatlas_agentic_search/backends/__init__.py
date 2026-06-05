"""Backend registry + selection helpers.

The registry maps a stable tool name to a singleton ``Backend``. Selection is a
two-stage filter the CLI and the agent both reuse:

    requested (user --tools / config default)
        ∩ registered
        ∩ available(settings)   # has the key / server / wiki dir it needs

so a user can never accidentally invoke a backend that has no credentials, and
the agent is only ever handed tools the user allowed *and* that can actually run.
"""

from __future__ import annotations

from qatlas_agentic_search.backends.arxiv import ArxivBackend
from qatlas_agentic_search.backends.base import Backend
from qatlas_agentic_search.backends.crossref import CrossrefBackend
from qatlas_agentic_search.backends.internal import InternalBackend
from qatlas_agentic_search.backends.openalex import OpenAlexBackend
from qatlas_agentic_search.backends.semantic_scholar import SemanticScholarBackend
from qatlas_agentic_search.config import Settings

# Insertion order is the canonical display order.
_REGISTRY: dict[str, Backend] = {
    b.name: b
    for b in (
        ArxivBackend(),
        OpenAlexBackend(),
        SemanticScholarBackend(),
        CrossrefBackend(),
        InternalBackend(),
    )
}


def all_backends() -> list[Backend]:
    return list(_REGISTRY.values())


def get_backend(name: str) -> Backend | None:
    return _REGISTRY.get(name)


def select_backends(
    names: list[str], settings: Settings, *, only_available: bool = True
) -> list[Backend]:
    """Resolve a list of names to backends, preserving the requested order.

    Unknown names are skipped. When ``only_available`` (the default), backends
    whose ``available(settings)`` is False (missing key/server) are dropped.
    """
    out: list[Backend] = []
    for name in names:
        b = _REGISTRY.get(name)
        if b is None:
            continue
        if only_available and not b.available(settings):
            continue
        if b not in out:
            out.append(b)
    return out
