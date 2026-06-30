"""Reference resolution for the Claim flow (ADR 0007).

Primary path: ``GET /api/papers/lookup`` against the QA server's local OpenAlex
corpus. When the server reports ``corpus_available: false`` (the corpus is
unreachable), fall back to the **public OpenAlex API** for that session — exactly
the graceful degradation ADR 0006/0007 prescribes — resolving each still-
unresolved ref by id.
"""

from __future__ import annotations

import urllib.parse

import requests

from .models import ResolvedRef

_OPENALEX_API = "https://api.openalex.org/works"


def resolve_references(
    base_url: str,
    headers: dict[str, str],
    verify: bool,
    refs: list[str],
    *,
    session: requests.Session | None = None,
    mailto: str | None = None,
) -> list[ResolvedRef]:
    """Resolve namespaced ``kind:id`` refs to ``ResolvedRef``. Always returns one
    entry per input ref (order preserved); a miss is ``resolved=False`` rather
    than an error — references enrich a Claim, they are not a correctness gate.
    """
    refs = [r.strip() for r in refs if r.strip()]
    if not refs:
        return []
    sess = session or requests.Session()

    results, corpus_available = _server_lookup(sess, base_url, headers, verify, refs)

    # When the server corpus is down, resolve the still-unresolved refs against
    # the public OpenAlex API (best-effort, per-session fallback).
    if not corpus_available:
        for r in results:
            if not r.resolved:
                _public_fill(sess, r, mailto)
    return results


def _server_lookup(
    sess: requests.Session,
    base_url: str,
    headers: dict[str, str],
    verify: bool,
    refs: list[str],
) -> tuple[list[ResolvedRef], bool]:
    url = base_url.rstrip("/") + "/api/papers/lookup"
    try:
        resp = sess.get(
            url,
            headers=headers,
            params={"ids": ",".join(refs)},
            verify=verify,
            timeout=30,
        )
    except requests.RequestException:
        # Server unreachable entirely → degrade to "unresolved, corpus down".
        return [ResolvedRef(ref=r) for r in refs], False
    if resp.status_code != 200:
        return [ResolvedRef(ref=r) for r in refs], False
    payload = resp.json()
    out = [ResolvedRef(**item) for item in payload.get("results", [])]
    # Preserve input order / completeness even if the server dropped some.
    seen = {r.ref for r in out}
    out.extend(ResolvedRef(ref=r) for r in refs if r not in seen)
    return out, bool(payload.get("corpus_available", False))


def _public_fill(sess: requests.Session, ref: ResolvedRef, mailto: str | None) -> None:
    """Fill one ResolvedRef from the public OpenAlex API by id. Silent on any
    failure (the ref simply stays unresolved)."""
    kind, _, ident = ref.ref.partition(":")
    if not ident:
        return
    url = _public_url(kind, ident)
    if not url:
        return
    params = {"mailto": mailto} if mailto else {}
    try:
        resp = sess.get(url, params=params, timeout=20)
        if resp.status_code != 200:
            return
        work = resp.json()
        # A by-arxiv filter call returns a results envelope; by-id returns a work.
        if "results" in work:
            results = work.get("results") or []
            if not results:
                return
            work = results[0]
    except requests.RequestException:
        return
    ref.title = (work.get("title") or "").strip() or None
    ref.year = work.get("publication_year")
    ref.authors = _author_names(work)
    ref.resolved = bool(ref.title or ref.year)


def _public_url(kind: str, ident: str) -> str | None:
    if kind == "openalex":
        return f"{_OPENALEX_API}/{ident}"
    if kind == "doi":
        return f"{_OPENALEX_API}/doi:{urllib.parse.quote(ident, safe='')}"
    if kind == "arxiv":
        # OpenAlex has no by-arxiv primary key; use the filter endpoint.
        bare = ident.split("v")[0] if "v" in ident else ident
        return f"{_OPENALEX_API}?filter=" + urllib.parse.quote(
            f"locations.landing_page_url.search:{bare}", safe=""
        )
    return None


def _author_names(work: dict) -> list[str]:
    out = []
    for a in work.get("authorships", []) or []:
        name = (a.get("author") or {}).get("display_name")
        if name:
            out.append(name)
    return out
