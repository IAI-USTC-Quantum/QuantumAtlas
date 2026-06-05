"""HTTP client for the embed worker (Ag-Workstation 5080).

Thin wrapper over httpx so the ingester doesn't grow a httpx dependency
in every call-site.  The two methods correspond 1:1 to embed/worker.py
endpoints.
"""

from __future__ import annotations

from typing import Any

import httpx


class EmbedClient:
    def __init__(self, *, base_url: str, token: str | None = None, timeout_s: float = 60.0) -> None:
        self.base_url = base_url.rstrip("/")
        headers = {"Authorization": f"Bearer {token}"} if token else {}
        self._client = httpx.Client(base_url=self.base_url, headers=headers, timeout=timeout_s)

    def __enter__(self) -> "EmbedClient":
        return self

    def __exit__(self, *_a: object) -> None:
        self.close()

    def close(self) -> None:
        self._client.close()

    def healthz(self) -> dict[str, Any]:
        r = self._client.get("/healthz")
        r.raise_for_status()
        return r.json()

    def embed(
        self,
        texts: list[str],
        *,
        lane: str = "build",
        return_sparse: bool = False,
    ) -> tuple[list[list[float]], list[dict[str, Any]] | None]:
        r = self._client.post(
            "/embed",
            params={"lane": lane},
            json={"texts": texts, "return_sparse": return_sparse},
        )
        r.raise_for_status()
        body = r.json()
        return body["dense"], body.get("sparse")

    def rerank(self, query: str, passages: list[str], *, lane: str = "query") -> list[float]:
        r = self._client.post(
            "/rerank",
            params={"lane": lane},
            json={"query": query, "passages": passages},
        )
        r.raise_for_status()
        return r.json()["scores"]
