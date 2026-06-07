"""Backend abstraction: each academic source is one search *tool*.

A backend is a small object with metadata (name, whether it needs a key, a
coarse cost/latency tier) plus a single ``search(query, settings) -> list[Paper]``
method. Backends MUST be resilient: any network/parse failure returns ``[]`` (and
records the error) rather than raising, so one source 429-ing never sinks the
whole multi-tool search. This is what lets the CLI run them concurrently and
report partial results.
"""

from __future__ import annotations

import time
from abc import ABC, abstractmethod

import requests

from qatlas_search.config import Settings
from qatlas_search.models import Paper, SearchQuery

# Coarse buckets the CLI uses to honor a time/cost budget.
COST_FAST = "fast"  # single cheap HTTP call, no key
COST_MEDIUM = "medium"  # heavier API or reconstruction work
COST_SLOW = "slow"  # multiple round-trips / local scan

# Transient-failure retry policy shared by every backend's HTTP call.
_RETRY_STATUS = {429, 500, 502, 503, 504}
_MAX_ATTEMPTS = 3
_BACKOFF_BASE = 0.5  # seconds; exponential: 0.5, 1.0, ...
_RETRY_AFTER_CAP = 8  # honor 429 Retry-After but never block the batch this long


def request_with_retry(method: str, url: str, *, settings: Settings, **kwargs) -> requests.Response:
    """HTTP request with bounded retry/backoff on *transient* failures.

    Retries on connection errors, timeouts, and 429/5xx responses up to
    ``_MAX_ATTEMPTS`` (exponential backoff, honoring a numeric ``Retry-After``
    on 429, capped so a single slow source can't stall the whole search). The
    last error / ``HTTPError`` is raised when attempts are exhausted, so a
    backend's existing ``try/except`` still records it as ``last_error`` — i.e.
    retries make the path more robust without changing the failure contract.
    """
    timeout = kwargs.pop("timeout", settings.request_timeout)
    for attempt in range(1, _MAX_ATTEMPTS + 1):
        last = attempt == _MAX_ATTEMPTS
        try:
            resp = requests.request(method, url, timeout=timeout, **kwargs)
        except (requests.Timeout, requests.ConnectionError):
            if last:
                raise
            time.sleep(_BACKOFF_BASE * 2 ** (attempt - 1))
            continue
        if not last and resp.status_code in _RETRY_STATUS:
            delay = _BACKOFF_BASE * 2 ** (attempt - 1)
            if resp.status_code == 429:
                retry_after = resp.headers.get("Retry-After", "")
                if retry_after.isdigit():
                    delay = min(int(retry_after), _RETRY_AFTER_CAP)
            time.sleep(delay)
            continue
        resp.raise_for_status()
        return resp
    raise RuntimeError("request_with_retry: unreachable")  # pragma: no cover


def _user_agent(settings: Settings) -> str:
    """Polite-pool User-Agent. OpenAlex/Crossref reward a contact mailto."""
    email = settings.openalex_email or settings.crossref_email
    base = "qatlas-search/0.1"
    return f"{base} (mailto:{email})" if email else base


class Backend(ABC):
    name: str = ""
    requires_key: bool = False
    cost_tier: str = COST_FAST

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
        return request_with_retry("GET", url, settings=settings, headers=headers, **kwargs)
