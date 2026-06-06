"""QuantumAtlas internal backend — the precise-match + citation tool.

Three complementary internal lookups, merged:

1. **Graph** (``POST /api/graph/query``): read-only Cypher over the ``:PaperWork``
   layer, exact token matching on the title, returning ``cited_by_count`` — the
   internal analogue of "metadata/title exact match + citation count".
2. **Wiki** (``GET /api/search``): full-text over curated concept + paper-source
   pages (good lexical recall on hand-written summaries).
3. **Local grep** (optional): if ``QATLAS_SEARCH_WIKI_DIR`` points at a wiki
   checkout, grep its Markdown for exact term matches. Off by default because
   client users are usually not on the server host.

Server URL + bearer token are resolved from the existing ``qatlas`` client
config (so ``qatlas auth login`` is enough), with ``QATLAS_SEARCH_SERVER_URL`` /
``QATLAS_SEARCH_TOKEN`` overrides. Every sub-lookup degrades gracefully: a
missing token, an unconfigured Neo4j, or a missing wiki dir just yields fewer
results, never an exception.
"""

from __future__ import annotations

import os
import re

import requests

from qatlas_search.backends.base import COST_SLOW, Backend
from qatlas_search.config import Settings
from qatlas_search.models import Paper, SearchQuery

_PAPER_ARXIV_RE = re.compile(r"paper-arxiv-(.+)$", re.IGNORECASE)
_FRONT_TITLE_RE = re.compile(r"^title:\s*(.+?)\s*$", re.MULTILINE)
_FRONT_ID_RE = re.compile(r"^id:\s*(.+?)\s*$", re.MULTILINE)


def _resolve_server(settings: Settings) -> tuple[str | None, str | None]:
    """(base_url, token) from settings overrides, else the qatlas client config."""
    base = settings.server_url
    token = settings.token
    if not base:
        try:
            from qatlas.client._common import default_base_url

            base = default_base_url()
        except Exception:
            base = None
    if not token and base:
        try:
            from qatlas.client.auth import get_stored_token

            token = get_stored_token(base) or None
        except Exception:
            token = None
    return base, token


class InternalBackend(Backend):
    name = "internal"
    requires_key = False  # degrades gracefully; usefulness depends on config
    cost_tier = COST_SLOW

    def available(self, settings: Settings) -> bool:
        base, token = _resolve_server(settings)
        has_server = bool(base and token)
        has_local = bool(settings.wiki_dir and os.path.isdir(settings.wiki_dir))
        return has_server or has_local

    def search(self, query: SearchQuery, settings: Settings) -> list[Paper]:
        self.last_error = None
        errors: list[str] = []
        results: list[Paper] = []

        base, token = _resolve_server(settings)
        if base and token:
            headers = {"Authorization": f"Bearer {token}"}
            try:
                results += self._graph_search(query, settings, base, headers)
            except Exception as exc:  # noqa: BLE001
                errors.append(f"graph: {type(exc).__name__}: {exc}")
            try:
                results += self._wiki_search(query, settings, base, headers)
            except Exception as exc:  # noqa: BLE001
                errors.append(f"wiki: {type(exc).__name__}: {exc}")

        if settings.wiki_dir and os.path.isdir(settings.wiki_dir):
            try:
                results += self._local_grep(query, settings)
            except Exception as exc:  # noqa: BLE001
                errors.append(f"grep: {type(exc).__name__}: {exc}")

        if errors:
            self.last_error = "; ".join(errors)
        return results

    # -- 1. graph (PaperWork: exact title tokens + citations) --------------
    def _build_cypher(self, query: SearchQuery) -> str | None:
        # query.tokens() is [a-z0-9]+ only, so inlining is injection-safe.
        tokens = query.tokens()
        if not tokens:
            return None
        conds = " AND ".join(f"toLower(p.title) CONTAINS '{t}'" for t in tokens)
        # No LIMIT here: the server appends `LIMIT <limit>` from the request
        # body when the query lacks one (see internal/neo4j ExecuteRead).
        return (
            "MATCH (p:PaperWork) "
            f"WHERE p.title IS NOT NULL AND {conds} "
            "RETURN p.title AS title, p.arxiv_id AS arxiv_id, p.doi AS doi, "
            "p.publication_date AS publication_date, "
            "coalesce(p.cited_by_count, p.cited_by_count__derived) AS citations "
            "ORDER BY citations DESC"
        )

    def _graph_search(
        self, query: SearchQuery, settings: Settings, base: str, headers: dict
    ) -> list[Paper]:
        cypher = self._build_cypher(query)
        if not cypher:
            return []
        resp = requests.post(
            f"{base.rstrip('/')}/api/graph/query",
            json={"query": cypher, "limit": query.max_results},
            headers=headers,
            timeout=settings.request_timeout,
        )
        resp.raise_for_status()
        data = resp.json()
        if data.get("error"):  # Neo4j not configured / query error: tolerate.
            return []
        out: list[Paper] = []
        for i, rec in enumerate(data.get("records", []) or []):
            title = rec.get("title")
            if not title:
                continue
            year = None
            pub = rec.get("publication_date")
            if isinstance(pub, str) and len(pub) >= 4 and pub[:4].isdigit():
                year = int(pub[:4])
            cites = rec.get("citations")
            arxiv_id = rec.get("arxiv_id")
            url = f"{base.rstrip('/')}/abs/{arxiv_id}" if arxiv_id else None
            out.append(
                Paper(
                    title=title,
                    year=year,
                    citations=int(cites) if isinstance(cites, (int, float)) else None,
                    url=url,
                    doi=rec.get("doi"),
                    arxiv_id=arxiv_id,
                    source="qatlas-graph",
                    raw_rank=i + 1,
                )
            )
        return out

    # -- 2. wiki full-text -------------------------------------------------
    def _wiki_search(
        self, query: SearchQuery, settings: Settings, base: str, headers: dict
    ) -> list[Paper]:
        resp = requests.get(
            f"{base.rstrip('/')}/api/search",
            params={
                "q": query.text,
                "limit": query.max_results,
                "include_sources": "true",
            },
            headers=headers,
            timeout=settings.request_timeout,
        )
        resp.raise_for_status()
        data = resp.json()
        out: list[Paper] = []
        for i, r in enumerate(data.get("results", []) or []):
            title = r.get("title")
            if not title:
                continue
            page_id = r.get("id") or ""
            m = _PAPER_ARXIV_RE.match(page_id)
            out.append(
                Paper(
                    title=title,
                    abstract=r.get("snippet") or None,
                    url=f"{base.rstrip('/')}/wiki/{page_id}" if page_id else None,
                    arxiv_id=m.group(1) if m else None,
                    source="qatlas-wiki",
                    raw_rank=i + 1,
                    raw_score=r.get("score"),
                )
            )
        return out

    # -- 3. optional local grep over a wiki checkout -----------------------
    def _local_grep(self, query: SearchQuery, settings: Settings) -> list[Paper]:
        tokens = query.tokens()
        if not tokens:
            return []
        root = settings.wiki_dir or ""
        out: list[Paper] = []
        for dirpath, _dirs, files in os.walk(root):
            for fn in files:
                if not fn.endswith(".md"):
                    continue
                path = os.path.join(dirpath, fn)
                try:
                    with open(path, encoding="utf-8", errors="ignore") as fh:
                        text = fh.read()
                except OSError:
                    continue
                low = text.lower()
                if not all(t in low for t in tokens):
                    continue
                tm = _FRONT_TITLE_RE.search(text)
                im = _FRONT_ID_RE.search(text)
                page_id = im.group(1).strip().strip("\"'") if im else fn[:-3]
                title = tm.group(1).strip().strip("\"'") if tm else page_id
                m = _PAPER_ARXIV_RE.match(page_id)
                out.append(
                    Paper(
                        title=title,
                        arxiv_id=m.group(1) if m else None,
                        url=f"file://{path}",
                        source="qatlas-wiki-local",
                    )
                )
                if len(out) >= query.max_results:
                    return out
        return out
