"""Backend abstraction: each academic source is one search *tool*.

A backend is a small object with metadata (name, whether it needs a key, a
coarse cost/latency tier) plus a single ``search(query, settings) -> list[Paper]``
method. Backends MUST be resilient: any network/parse failure returns ``[]`` (and
records the error) rather than raising, so one source 429-ing never sinks the
whole multi-tool search. This is what lets the CLI run them concurrently and
report partial results.
"""

from __future__ import annotations

from abc import ABC, abstractmethod

import requests

from qatlas_search.config import Settings
from qatlas_search.models import Paper, SearchQuery

# Coarse buckets the agent/CLI use to honor a time/cost budget.
COST_FAST = "fast"  # single cheap HTTP call, no key
COST_MEDIUM = "medium"  # heavier API or reconstruction work
COST_SLOW = "slow"  # multiple round-trips / local scan


def _user_agent(settings: Settings) -> str:
    """Polite-pool User-Agent. OpenAlex/Crossref reward a contact mailto."""
    email = settings.openalex_email or settings.crossref_email
    base = "qatlas-agentic-search/0.1"
    return f"{base} (mailto:{email})" if email else base


class Backend(ABC):
    name: str = ""
    requires_key: bool = False
    cost_tier: str = COST_FAST
    # Whether it's in the shipped default allow-list (overridable via config).
    default_enabled: bool = True

    #: populated by search() when a call fails, surfaced by the CLI in -v mode.
    last_error: str | None = None

    def available(self, settings: Settings) -> bool:
        """True when this backend can run with the current config."""
        return not self.requires_key

    @abstractmethod
    def search(self, query: SearchQuery, settings: Settings) -> list[Paper]: ...

    # -- shared HTTP helper ------------------------------------------------
    def _get(self, settings: Settings, url: str, **kwargs) -> requests.Response:
        headers = kwargs.pop("headers", {})
        headers.setdefault("User-Agent", _user_agent(settings))
        resp = requests.get(url, headers=headers, timeout=settings.request_timeout, **kwargs)
        resp.raise_for_status()
        return resp
