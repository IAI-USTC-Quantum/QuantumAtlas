"""FastAPI sidecar — query-path service deployed on each edge.

The Internal Go server reverse-proxies ``/api/rag/*`` to this sidecar
(listening on ``127.0.0.1:8802`` by convention).  The sidecar:

1. embeds the query through the embed-worker on Ag-Workstation
2. runs hybrid (dense + sparse) Qdrant search
3. optionally reranks top-N via the embed-worker
4. returns JSON snippets the SPA can render directly

Auth is handled by the Go layer (authGuard + scopeGuard("rag","read"))
— this sidecar trusts whoever can reach 127.0.0.1:8802 (only qatlasd
on the same host) and therefore does NOT re-verify the caller.  It DOES
hold service-to-service tokens for Qdrant (read-only API key) and
embed-worker (shared bearer).
"""

from __future__ import annotations

import logging
import time
from contextlib import asynccontextmanager
from typing import Any

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field
from qdrant_client import QdrantClient
from qdrant_client.http import models as qm

from qatlas_rag.config import Settings, get_settings
from qatlas_rag.ingest.embed_client import EmbedClient
from qatlas_rag.ingest.qdrant_store import DENSE_NAME, SPARSE_NAME

logger = logging.getLogger("qatlas_rag.sidecar")


# --- API models ----------------------------------------------------------

class SearchRequest(BaseModel):
    query: str = Field(..., min_length=1, max_length=2048)
    top_k: int = Field(default=8, ge=1, le=50)
    rerank: bool = True
    rerank_pool: int = Field(default=50, ge=10, le=200)
    use_sparse: bool = True
    filters: dict[str, str] | None = None  # e.g. {"yymm": "2401"}


class SearchHit(BaseModel):
    arxiv_id: str
    canonical: str
    yymm: str
    version: int
    title: str | None = None
    authors: list[str] | None = None
    categories: list[str] | None = None
    section_path: list[str]
    chunk_index: int
    snippet: str
    score: float
    md_object_key: str
    char_start: int
    char_end: int
    image_refs: list[str] = Field(default_factory=list)


class SearchResponse(BaseModel):
    query: str
    took_s: float
    reranked: bool
    results: list[SearchHit]


class HealthResponse(BaseModel):
    status: str  # "ok" | "degraded" | "down"


# --- retriever -----------------------------------------------------------

class Retriever:
    """Holds the two outbound clients; constructed once at lifespan startup."""

    def __init__(self, settings: Settings) -> None:
        self.settings = settings
        self.qdrant = QdrantClient(
            url=settings.qdrant_http_url,
            api_key=settings.qdrant_api_key,
            prefer_grpc=False,
        )
        self.embed = EmbedClient(base_url=settings.embed_url, token=settings.embed_token)

    def close(self) -> None:
        self.qdrant.close()
        self.embed.close()

    async def health(self) -> HealthResponse:
        # Two independent probes; degraded if either is down.  Sidecar
        # response is intentionally coarse (no internal IPs / model names)
        # so leaking /healthz to anonymous callers does not give away
        # topology.
        ok_q = False
        ok_e = False
        try:
            self.qdrant.get_collections()
            ok_q = True
        except Exception as exc:
            logger.warning("qdrant probe failed: %s", exc)
        try:
            self.embed.healthz()
            ok_e = True
        except Exception as exc:
            logger.warning("embed probe failed: %s", exc)
        if ok_q and ok_e:
            return HealthResponse(status="ok")
        if ok_q or ok_e:
            return HealthResponse(status="degraded")
        return HealthResponse(status="down")

    def _build_filter(self, filters: dict[str, str] | None) -> qm.Filter | None:
        if not filters:
            return None
        return qm.Filter(
            must=[
                qm.FieldCondition(key=k, match=qm.MatchValue(value=v))
                for k, v in filters.items()
            ]
        )

    def search(self, req: SearchRequest) -> SearchResponse:
        t0 = time.time()
        dense_list, sparse_list = self.embed.embed(
            [req.query], lane="query", return_sparse=req.use_sparse
        )
        dense_vec = dense_list[0]
        sparse_vec = sparse_list[0] if sparse_list else None

        flt = self._build_filter(req.filters)
        pool = req.rerank_pool if req.rerank else req.top_k

        # Hybrid query when sparse is available, else dense-only.  We use
        # qdrant-client's query_points (with Prefetch + RRF) when both
        # vectors exist; otherwise plain search().
        if sparse_vec and len(sparse_vec.get("indices", [])) > 0:
            try:
                response = self.qdrant.query_points(
                    collection_name=self.settings.qdrant_collection,
                    prefetch=[
                        qm.Prefetch(query=dense_vec, using=DENSE_NAME, limit=pool, filter=flt),
                        qm.Prefetch(
                            query=qm.SparseVector(
                                indices=sparse_vec["indices"], values=sparse_vec["values"]
                            ),
                            using=SPARSE_NAME,
                            limit=pool,
                            filter=flt,
                        ),
                    ],
                    query=qm.FusionQuery(fusion=qm.Fusion.RRF),
                    limit=pool,
                    with_payload=True,
                )
                points = response.points
            except Exception as exc:  # qdrant-client doesn't always expose hybrid on older servers
                logger.warning("hybrid query failed, falling back to dense-only: %s", exc)
                points = self.qdrant.search(
                    collection_name=self.settings.qdrant_collection,
                    query_vector=(DENSE_NAME, dense_vec),
                    limit=pool,
                    with_payload=True,
                    query_filter=flt,
                )
        else:
            points = self.qdrant.search(
                collection_name=self.settings.qdrant_collection,
                query_vector=(DENSE_NAME, dense_vec),
                limit=pool,
                with_payload=True,
                query_filter=flt,
            )

        # Optional rerank: send (query, chunk_text) to embed-worker.
        reranked = False
        if req.rerank and points:
            passages = [_payload_text(p) for p in points]
            scores = self.embed.rerank(req.query, passages, lane="query")
            order = sorted(range(len(points)), key=lambda i: scores[i], reverse=True)
            points = [points[i] for i in order[: req.top_k]]
            score_map = {i: scores[i] for i in order[: req.top_k]}
            # Convert order positions back to actual reranker scores.
            ranked = []
            for idx, original_pos in enumerate(order[: req.top_k]):
                hit = _to_search_hit(points[idx], score=score_map[original_pos])
                ranked.append(hit)
            return SearchResponse(
                query=req.query, took_s=time.time() - t0, reranked=True, results=ranked
            )

        # No rerank: keep first top_k.
        results = [_to_search_hit(p, score=float(getattr(p, "score", 0.0))) for p in points[: req.top_k]]
        return SearchResponse(
            query=req.query, took_s=time.time() - t0, reranked=reranked, results=results
        )


def _payload_text(point: Any) -> str:
    return point.payload.get("chunk_text", "") if point.payload else ""


def _to_search_hit(point: Any, *, score: float) -> SearchHit:
    p = point.payload or {}
    return SearchHit(
        arxiv_id=p.get("arxiv_id", ""),
        canonical=p.get("canonical", ""),
        yymm=p.get("yymm", ""),
        version=int(p.get("version", 0) or 0),
        title=p.get("title"),
        authors=p.get("authors"),
        categories=p.get("categories"),
        section_path=p.get("section_path", []),
        chunk_index=int(p.get("chunk_index", 0) or 0),
        snippet=p.get("chunk_text", ""),
        score=score,
        md_object_key=p.get("md_object_key", ""),
        char_start=int(p.get("char_start", 0) or 0),
        char_end=int(p.get("char_end", 0) or 0),
        image_refs=p.get("image_refs", []),
    )


# --- FastAPI app ---------------------------------------------------------

@asynccontextmanager
async def _lifespan(app: FastAPI):
    settings = get_settings()
    app.state.retriever = Retriever(settings)
    try:
        yield
    finally:
        app.state.retriever.close()


app = FastAPI(title="qatlas-rag-sidecar", lifespan=_lifespan)


@app.get("/healthz", response_model=HealthResponse)
async def healthz() -> HealthResponse:
    return await app.state.retriever.health()


@app.post("/search", response_model=SearchResponse)
async def search(req: SearchRequest) -> SearchResponse:
    try:
        return app.state.retriever.search(req)
    except Exception as exc:
        logger.exception("search failed: %s", exc)
        raise HTTPException(status_code=502, detail=f"upstream failure: {exc}") from exc
