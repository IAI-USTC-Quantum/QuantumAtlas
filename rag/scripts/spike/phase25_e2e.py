"""Phase 2.5 mini E2E spike — end-to-end pipeline on a small slice.

Picks N papers from the largest prefix (2103/ = 992 papers), creates a
disposable Qdrant collection, runs the full pipeline (parse → chunk →
embed → upsert), then issues a sample query and reports timings.

Usage:
    NO_PROXY='10.144.18.10,127.0.0.1,localhost' \
        uv run --extra ingest --extra embed --extra sidecar \
        python -m scripts.spike.phase25_e2e --papers 10
    ...                       --papers 500 --queries 5
"""

from __future__ import annotations

import argparse
import hashlib
import logging
import time
from dataclasses import dataclass

from qdrant_client import QdrantClient

from qatlas_rag.config import get_settings
from qatlas_rag.ingest.chunker import chunk_document
from qatlas_rag.ingest.embed_client import EmbedClient
from qatlas_rag.ingest.parser import split_sections
from qatlas_rag.ingest.qdrant_store import (
    PaperMeta,
    count_for_arxiv,
    delete_paper,
    ensure_collection,
    upsert_chunks,
)
from qatlas_rag.ingest.s3 import RustFsClient

log = logging.getLogger("spike.phase25")
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(message)s",
    datefmt="%H:%M:%S",
)


SAMPLE_QUERIES = [
    "quantum Fourier transform complexity",
    "variational quantum eigensolver convergence",
    "stabilizer code distance bound",
    "shor algorithm post-quantum cryptography",
    "amplitude amplification gate count",
]


@dataclass
class PaperStats:
    arxiv_id: str
    bytes: int
    chunks: int
    embed_s: float
    upsert_s: float


def run_spike(
    *,
    n_papers: int,
    prefix: str,
    collection: str,
    queries: int,
    use_sparse: bool,
) -> None:
    s = get_settings()
    s3 = RustFsClient(
        endpoint_url=s.s3_endpoint, region=s.s3_region,
        access_key=s.s3_access_key, secret_key=s.s3_secret_key,
    )
    qdrant = QdrantClient(url=s.qdrant_http_url, api_key=s.qdrant_api_key, prefer_grpc=False)
    embed = EmbedClient(base_url=s.embed_url, token=s.embed_token)

    # Wipe + create collection fresh so the spike is reproducible.
    try:
        qdrant.delete_collection(collection)
        log.info("dropped pre-existing collection %s", collection)
    except Exception:
        pass
    ensure_collection(qdrant, collection)
    log.info("created collection %s", collection)

    log.info("listing prefix %s ...", prefix)
    t0 = time.time()
    objects = list(s3.list_objects_in_prefix(s.s3_md_bucket, prefix))
    log.info("listed %d objects in %.1fs; will index first %d", len(objects), time.time() - t0, n_papers)
    objects = objects[:n_papers]

    stats: list[PaperStats] = []
    t_total = time.time()
    for i, obj in enumerate(objects):
        t_paper = time.time()
        raw = s3.get_object_bytes(obj.bucket, obj.key)
        sections = split_sections(raw.decode("utf-8", errors="replace"))
        chunks = chunk_document(sections)
        if not chunks:
            log.warning("[%d/%d] %s: 0 chunks — skip", i + 1, len(objects), obj.arxiv_id)
            continue

        t_embed = time.time()
        dense, sparse = embed.embed([c.text for c in chunks], lane="build", return_sparse=use_sparse)
        embed_dt = time.time() - t_embed

        paper = PaperMeta(
            arxiv_id=obj.arxiv_id,
            canonical=obj.canonical,
            yymm=obj.yymm,
            version=obj.version,
            md_object_key=obj.key,
        )
        # Always delete-by-arxiv first so re-running this script is idempotent.
        delete_paper(qdrant, obj.arxiv_id, name=collection)
        t_up = time.time()
        upsert_chunks(qdrant, paper, chunks, dense, sparse if use_sparse else None, name=collection)
        upsert_dt = time.time() - t_up

        st = PaperStats(
            arxiv_id=obj.arxiv_id,
            bytes=len(raw),
            chunks=len(chunks),
            embed_s=embed_dt,
            upsert_s=upsert_dt,
        )
        stats.append(st)
        log.info(
            "[%d/%d] %-14s %5d B %3d chunks  embed=%.2fs upsert=%.2fs  paper_total=%.2fs",
            i + 1, len(objects), obj.arxiv_id, st.bytes, st.chunks,
            st.embed_s, st.upsert_s, time.time() - t_paper,
        )

    total = time.time() - t_total
    n_chunks = sum(s.chunks for s in stats)
    sum_embed = sum(s.embed_s for s in stats)
    log.info("=" * 60)
    log.info("INDEX DONE: %d papers, %d chunks, %.1fs total", len(stats), n_chunks, total)
    log.info(
        "  per-paper p50/avg/max = %.2f / %.2f / %.2f s",
        sorted(s.embed_s + s.upsert_s for s in stats)[len(stats) // 2] if stats else 0.0,
        sum((s.embed_s + s.upsert_s) for s in stats) / max(1, len(stats)),
        max((s.embed_s + s.upsert_s) for s in stats) if stats else 0.0,
    )
    log.info("  embed throughput  = %.1f chunks/s", n_chunks / max(0.001, sum_embed))

    # --- query sanity ---
    log.info("=" * 60)
    log.info("running %d sample queries (rerank pool 50 → top 8)", queries)
    for q in SAMPLE_QUERIES[:queries]:
        t_q = time.time()
        # Embed the query (sparse + dense), then a simple dense-only Qdrant search
        # to keep the spike runner independent of the sidecar app.
        q_dense, _ = embed.embed([q], lane="query", return_sparse=False)
        from qdrant_client.http import models as qm
        res = qdrant.search(
            collection_name=collection,
            query_vector=("dense", q_dense[0]),
            limit=8,
            with_payload=True,
        )
        rerank_pairs = [(q, (p.payload or {}).get("chunk_text", "")) for p in res]
        scores = embed.rerank(q, [p[1] for p in rerank_pairs], lane="query")
        ranked = sorted(zip(res, scores), key=lambda x: x[1], reverse=True)[:5]
        dt = time.time() - t_q
        log.info("Q: %s  (%.2fs)", q, dt)
        for hit, score in ranked:
            p = hit.payload or {}
            sec = " › ".join(s for s in p.get("section_path", []) if s != "__preamble__")
            log.info("  %.2f  %s  [%s] %s", score, p.get("arxiv_id"), sec[:50], (p.get("chunk_text") or "")[:80].replace("\n", " "))

    log.info("=" * 60)
    log.info("collection summary:")
    info = qdrant.get_collection(collection)
    log.info("  points     = %s", info.points_count)
    log.info("  indexed    = %s", info.indexed_vectors_count)
    log.info("  status     = %s", info.status)


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--papers", type=int, default=10)
    p.add_argument("--prefix", default="2103/")
    p.add_argument("--collection", default="qatlas_papers_spike")
    p.add_argument("--queries", type=int, default=3)
    p.add_argument("--no-sparse", action="store_true", help="skip sparse vector path (dense-only)")
    args = p.parse_args()
    run_spike(
        n_papers=args.papers,
        prefix=args.prefix,
        collection=args.collection,
        queries=args.queries,
        use_sparse=not args.no_sparse,
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
