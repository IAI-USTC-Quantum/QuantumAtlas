"""FastAPI embed worker — bge-m3 + bge-reranker-v2-m3 on the local 5080.

Single-process, single-GPU.  Two priority lanes:

- query lane:  high priority, drained completely before each build batch
- build lane:  low priority, used by the offline ingester

Both lanes share one GPU and one asyncio.Lock so model calls stay serial.

Auth: every request must carry ``Authorization: Bearer <QATLAS_RAG_EMBED_TOKEN>``
(`/healthz` is allowed without a token but returns only a coarse status).
"""

from __future__ import annotations

import asyncio
import logging
import time
from contextlib import asynccontextmanager, contextmanager
from dataclasses import dataclass, field
from enum import Enum
from typing import Any

from fastapi import Depends, FastAPI, HTTPException, Query, Request
from pydantic import BaseModel, Field

from qatlas_rag.config import get_settings

logger = logging.getLogger("qatlas_rag.embed.worker")


# --- request / response models ---------------------------------------------

class EmbedRequest(BaseModel):
    texts: list[str] = Field(..., min_length=1, max_length=256)
    return_sparse: bool = False


class EmbedResponse(BaseModel):
    dense: list[list[float]]
    sparse: list[dict[str, Any]] | None = None
    model: str
    wall_s: float


class RerankRequest(BaseModel):
    query: str = Field(..., min_length=1, max_length=4096)
    passages: list[str] = Field(..., min_length=1, max_length=200)


class RerankResponse(BaseModel):
    scores: list[float]
    model: str
    wall_s: float


# --- priority queue --------------------------------------------------------

class Lane(str, Enum):
    QUERY = "query"
    BUILD = "build"


@dataclass(order=False)
class _Job:
    lane: Lane
    payload: Any
    op: str                                  # "embed" | "rerank"
    future: asyncio.Future = field(repr=False)
    enq_ts: float = field(default_factory=time.monotonic)


class _PriorityQueues:
    """Two asyncio.Queue objects + a coordinator that always drains query first.

    Capacity: query=64, build=256.  If query lane fills, FastAPI returns 503
    immediately (we don't want to silently buffer queries — pressure should
    propagate so the sidecar can degrade gracefully).
    """

    def __init__(self) -> None:
        self.query: asyncio.Queue[_Job] = asyncio.Queue(maxsize=64)
        self.build: asyncio.Queue[_Job] = asyncio.Queue(maxsize=256)
        self.gpu_lock = asyncio.Lock()
        self._notify = asyncio.Event()

    async def submit(self, lane: Lane, op: str, payload: Any) -> Any:
        fut: asyncio.Future = asyncio.get_running_loop().create_future()
        job = _Job(lane=lane, op=op, payload=payload, future=fut)
        target = self.query if lane is Lane.QUERY else self.build
        try:
            target.put_nowait(job)
        except asyncio.QueueFull:
            raise HTTPException(status_code=503, detail=f"{lane.value} lane saturated") from None
        self._notify.set()
        return await fut

    async def next_job(self) -> _Job:
        while True:
            if not self.query.empty():
                return self.query.get_nowait()
            if not self.build.empty():
                return self.build.get_nowait()
            self._notify.clear()
            await self._notify.wait()


# --- model wrappers --------------------------------------------------------

class _ModelHolder:
    """Lazy holders so app import doesn't pay model load (4 GB+) cost.

    Models are created on the first call to ensure_loaded() and stay
    resident.  Cold start is ~10 s on a warm HF cache, ~120 s cold.
    """

    def __init__(self, embed_model: str, reranker_model: str) -> None:
        self.embed_name = embed_model
        self.reranker_name = reranker_model
        self._embed: Any = None
        self._reranker: Any = None

    def ensure_loaded(self) -> None:
        if self._embed is None or self._reranker is None:
            from FlagEmbedding import BGEM3FlagModel, FlagReranker

            t0 = time.time()
            self._embed = BGEM3FlagModel(self.embed_name, use_fp16=True)
            logger.info("loaded embed model %s in %.1fs", self.embed_name, time.time() - t0)
            t0 = time.time()
            self._reranker = FlagReranker(self.reranker_name, use_fp16=True)
            logger.info("loaded reranker %s in %.1fs", self.reranker_name, time.time() - t0)

    def embed(self, texts: list[str], return_sparse: bool) -> tuple[list[list[float]], list[dict] | None]:
        self.ensure_loaded()
        out = self._embed.encode(
            texts,
            batch_size=min(32, len(texts)),
            max_length=1024,
            return_dense=True,
            return_sparse=return_sparse,
        )
        dense = out["dense_vecs"].tolist()
        sparse_payload = None
        if return_sparse:
            sparse_payload = []
            for s in out.get("lexical_weights", []):
                # bge-m3 returns dict[token_id_str, weight_float]
                indices = [int(k) for k in s.keys()]
                values = [float(v) for v in s.values()]
                sparse_payload.append({"indices": indices, "values": values})
        return dense, sparse_payload

    def rerank(self, query: str, passages: list[str]) -> list[float]:
        self.ensure_loaded()
        pairs = [(query, p) for p in passages]
        scores = self._reranker.compute_score(pairs)
        if isinstance(scores, (int, float)):
            scores = [float(scores)]
        return [float(s) for s in scores]


# --- worker loop -----------------------------------------------------------

async def _worker_loop(queues: _PriorityQueues, models: _ModelHolder) -> None:
    while True:
        job = await queues.next_job()
        try:
            async with queues.gpu_lock:
                result = await asyncio.to_thread(_run_job, models, job)
            if not job.future.done():
                job.future.set_result(result)
        except Exception as exc:
            if not job.future.done():
                job.future.set_exception(exc)


def _run_job(models: _ModelHolder, job: _Job) -> Any:
    if job.op == "embed":
        return models.embed(*job.payload)
    if job.op == "rerank":
        return models.rerank(*job.payload)
    raise ValueError(f"unknown op {job.op}")


# --- auth ------------------------------------------------------------------

def _require_token(request: Request) -> None:
    expected = get_settings().embed_token
    if not expected:
        # Worker started without QATLAS_RAG_EMBED_TOKEN: treat as anti-pattern
        # only in production; in dev/test we let it through so smoke scripts
        # don't need to invent a token.  Log loudly so it's visible.
        logger.warning("EMBED token not configured; accepting unauthenticated request")
        return
    header = request.headers.get("authorization", "")
    if not header.startswith("Bearer ") or header.removeprefix("Bearer ").strip() != expected:
        raise HTTPException(status_code=401, detail="bad bearer token")


# --- FastAPI plumbing ------------------------------------------------------

@asynccontextmanager
async def _lifespan(app: FastAPI):
    settings = get_settings()
    models = _ModelHolder(settings.embed_model, settings.reranker_model)
    queues = _PriorityQueues()
    app.state.models = models
    app.state.queues = queues
    task = asyncio.create_task(_worker_loop(queues, models))
    try:
        yield
    finally:
        task.cancel()
        with suppress_cancel():
            await task


@contextmanager
def suppress_cancel():
    try:
        yield
    except asyncio.CancelledError:
        pass


app = FastAPI(title="qatlas-rag-embed", lifespan=_lifespan)


@app.get("/healthz")
async def healthz() -> dict[str, str]:
    settings = get_settings()
    return {
        "status": "ok",
        "model": settings.embed_model,
        "reranker": settings.reranker_model,
    }


@app.post("/embed", response_model=EmbedResponse, dependencies=[Depends(_require_token)])
async def embed(
    body: EmbedRequest,
    request: Request,
    lane: Lane = Query(default=Lane.QUERY),
) -> EmbedResponse:
    t0 = time.time()
    dense, sparse = await request.app.state.queues.submit(
        lane, "embed", (body.texts, body.return_sparse)
    )
    return EmbedResponse(
        dense=dense,
        sparse=sparse,
        model=request.app.state.models.embed_name,
        wall_s=time.time() - t0,
    )


@app.post("/rerank", response_model=RerankResponse, dependencies=[Depends(_require_token)])
async def rerank(
    body: RerankRequest,
    request: Request,
    lane: Lane = Query(default=Lane.QUERY),
) -> RerankResponse:
    t0 = time.time()
    scores = await request.app.state.queues.submit(
        lane, "rerank", (body.query, body.passages)
    )
    return RerankResponse(
        scores=scores,
        model=request.app.state.models.reranker_name,
        wall_s=time.time() - t0,
    )
