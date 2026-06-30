"""Fetch a paper's markdown via the QA suspend-and-wait endpoints (ADR 0005 §F).

``GET /api/papers/{id}/markdown`` returns ``200`` + bytes when ready, or ``202``
+ ``Operation-Location: …/markdown/status`` + ``Retry-After`` while the server
silently fetches the PDF and runs MinerU. We honour ``Retry-After`` and bound
the whole wait by a session-level wall-clock budget so a stalled conversion
surfaces as a clear error instead of hanging the agent.
"""

from __future__ import annotations

import time
import urllib.parse

import requests

DEFAULT_BUDGET_S = 600.0
DEFAULT_POLL_S = 5.0


class PaperUnavailable(Exception):
    """Markdown could not be obtained within the budget / a terminal failure."""


def fetch_markdown(
    base_url: str,
    headers: dict[str, str],
    verify: bool,
    paper_id: str,
    *,
    session: requests.Session | None = None,
    budget_s: float = DEFAULT_BUDGET_S,
    poll_s: float = DEFAULT_POLL_S,
    sleep=time.sleep,
    now=time.monotonic,
) -> str:
    """Return the paper markdown text, polling the LRO until ready or budget."""
    sess = session or requests.Session()
    md_url = base_url.rstrip("/") + f"/api/papers/{urllib.parse.quote(paper_id, safe='')}/markdown"
    deadline = now() + budget_s

    resp = sess.get(md_url, headers=headers, verify=verify, timeout=60)
    while True:
        if resp.status_code == 200:
            return resp.text
        if resp.status_code == 202:
            status_url = resp.headers.get("Operation-Location") or (md_url + "/status")
            wait = _retry_after(resp, poll_s)
            if now() + wait > deadline:
                raise PaperUnavailable(
                    f"markdown for {paper_id} not ready within {budget_s:.0f}s budget"
                )
            sleep(wait)
            _poll_status(sess, status_url, headers, verify)  # advance/surface state
            resp = sess.get(md_url, headers=headers, verify=verify, timeout=60)
            continue
        # Terminal failure (404 / 502 / 503 / 4xx).
        detail = _detail(resp)
        raise PaperUnavailable(f"markdown fetch failed: HTTP {resp.status_code} {detail}")


def _poll_status(sess, status_url: str, headers, verify) -> dict:
    if status_url.startswith("/"):
        # Operation-Location may be a path; it's relative to the same origin —
        # the caller passes an absolute md_url fallback so this is rare.
        return {}
    try:
        resp = sess.get(status_url, headers=headers, verify=verify, timeout=30)
        if resp.status_code == 200:
            return resp.json()
    except (requests.RequestException, ValueError):
        pass
    return {}


def _retry_after(resp: requests.Response, default: float) -> float:
    raw = resp.headers.get("Retry-After")
    if raw:
        try:
            return max(0.5, float(raw))
        except ValueError:
            pass
    return default


def _detail(resp: requests.Response) -> str:
    try:
        return str(resp.json().get("detail", ""))
    except ValueError:
        return resp.text[:200]
