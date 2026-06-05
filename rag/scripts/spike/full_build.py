"""Full-corpus build of qatlas_papers_v1_dryrun (Phase 6, supersedes spike).

Walks every YYMM prefix of qatlas-md, parses + chunks + embeds + upserts
into Qdrant with hybrid (dense + sparse) vectors.  Resumable via the local
SQLite manifest — a re-run only processes added or etag-changed objects.

Usage:
    # Sanity pass: 2000 random papers, fixed seed for reproducibility.
    NO_PROXY='10.144.18.10,127.0.0.1,localhost' \
        uv run --extra ingest --extra embed --extra sidecar \
        python -m scripts.spike.full_build \
            --collection qatlas_papers_v1_dryrun \
            --sample-papers 2000 --random-seed 42 \
            --log /tmp/qatlas-rag-run/build-2k.log

    # Full corpus, no sampling.
    NO_PROXY='...' uv run ... python -m scripts.spike.full_build \
            --collection qatlas_papers_v1_dryrun --log .../build-89k.log
"""

from __future__ import annotations

import argparse
import hashlib
import logging
import random
import sys
import time
from dataclasses import dataclass, asdict, field
from pathlib import Path

from qdrant_client import QdrantClient

from qatlas_rag.config import get_settings
from qatlas_rag.ingest.chunker import chunk_document
from qatlas_rag.ingest.embed_client import EmbedClient
from qatlas_rag.ingest.manifest import DocRow, Manifest
from qatlas_rag.ingest.parser import split_sections
from qatlas_rag.ingest.qdrant_store import (
    PaperMeta,
    delete_paper,
    enable_hnsw_indexing,
    ensure_collection,
    upsert_chunks,
)
from qatlas_rag.ingest.s3 import ObjectMeta, RustFsClient

log = logging.getLogger("full_build")


@dataclass
class RunStats:
    started_iso: str
    processed: int = 0
    skipped_already_indexed: int = 0
    errors: int = 0
    chunks: int = 0
    bytes_in: int = 0
    embed_s: float = 0.0
    upsert_s: float = 0.0
    parse_s: float = 0.0
    s3_get_s: float = 0.0
    failed_arxiv: list[str] = field(default_factory=list)


def setup_logging(path: str | None) -> None:
    handlers: list[logging.Handler] = [logging.StreamHandler(sys.stdout)]
    if path:
        Path(path).parent.mkdir(parents=True, exist_ok=True)
        handlers.append(logging.FileHandler(path, mode="a"))
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(message)s",
        datefmt="%H:%M:%S",
        handlers=handlers,
        force=True,
    )


def enumerate_objects(s3: RustFsClient, bucket: str) -> list[ObjectMeta]:
    """Walk all YYMM prefixes, return flat ObjectMeta list."""
    prefixes = s3.list_yymm_prefixes(bucket)
    log.info("enumerating %d YYMM prefixes ...", len(prefixes))
    t0 = time.time()
    objs: list[ObjectMeta] = []
    for i, p in enumerate(prefixes):
        before = len(objs)
        for o in s3.list_objects_in_prefix(bucket, p):
            objs.append(o)
        if (i + 1) % 30 == 0:
            log.info(
                "  prefix %3d/%d  cumulative_objects=%d  dt=%.1fs",
                i + 1, len(prefixes), len(objs), time.time() - t0,
            )
    log.info("enumerated %d objects across %d prefixes in %.1fs", len(objs), len(prefixes), time.time() - t0)
    return objs


def process_one(
    obj: ObjectMeta,
    *, s3: RustFsClient, embed: EmbedClient, qdrant: QdrantClient,
    manifest: Manifest, collection: str, use_sparse: bool,
) -> tuple[int, dict]:
    """Return (n_chunks_written, per-stage-timings)."""
    times = {"s3_get": 0.0, "parse": 0.0, "embed": 0.0, "upsert": 0.0}

    t = time.time()
    raw = s3.get_object_bytes(obj.bucket, obj.key)
    times["s3_get"] = time.time() - t

    t = time.time()
    text_hash = hashlib.sha256(raw).hexdigest()
    sections = split_sections(raw.decode("utf-8", errors="replace"))
    chunks = chunk_document(sections)
    times["parse"] = time.time() - t

    if not chunks:
        log.warning("%s parsed to 0 chunks; skip", obj.arxiv_id)
        manifest.upsert(DocRow(
            arxiv_id=obj.arxiv_id, bucket=obj.bucket, object_key=obj.key,
            etag=obj.etag, last_modified=obj.last_modified,
            size_bytes=obj.size, text_hash=text_hash, chunk_count=0,
        ))
        manifest.stamp_indexed(obj.arxiv_id, 0, text_hash)
        return 0, times

    # Embed in batches — embed-worker EmbedRequest caps `texts` at 256.
    # Large papers (e.g. 1410.3193v1 at 270 chunks, 0812.4682v1 at 296)
    # would otherwise 422 the whole paper.
    EMBED_BATCH = 200
    t = time.time()
    dense: list = []
    sparse: list | None = [] if use_sparse else None
    texts = [c.text for c in chunks]
    for i in range(0, len(texts), EMBED_BATCH):
        d, s = embed.embed(texts[i : i + EMBED_BATCH], lane="build", return_sparse=use_sparse)
        dense.extend(d)
        if use_sparse and s is not None:
            sparse.extend(s)
    times["embed"] = time.time() - t

    # Always delete-by-arxiv first so a re-run with changed chunks doesn't
    # duplicate.  For *new* arxiv_id this is a cheap no-op (filter delete
    # on indexed payload field returns 0 deletions).
    paper = PaperMeta(
        arxiv_id=obj.arxiv_id, canonical=obj.canonical, yymm=obj.yymm,
        version=obj.version, md_object_key=obj.key,
    )
    t = time.time()
    delete_paper(qdrant, obj.arxiv_id, name=collection)
    upsert_chunks(qdrant, paper, chunks, dense, sparse if use_sparse else None, name=collection)
    times["upsert"] = time.time() - t

    manifest.upsert(DocRow(
        arxiv_id=obj.arxiv_id, bucket=obj.bucket, object_key=obj.key,
        etag=obj.etag, last_modified=obj.last_modified,
        size_bytes=obj.size, text_hash=text_hash, chunk_count=len(chunks),
    ))
    manifest.stamp_indexed(obj.arxiv_id, len(chunks), text_hash)
    return len(chunks), times


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--collection", default="qatlas_papers_v1_dryrun")
    p.add_argument("--sample-papers", type=int, default=0, help="0 = no sampling, build all")
    p.add_argument("--random-seed", type=int, default=42)
    p.add_argument("--no-sparse", action="store_true")
    p.add_argument("--log", type=str, default=None)
    p.add_argument("--enable-hnsw-after", action="store_true",
                   help="Set indexing_threshold=20000 after build completes.")
    p.add_argument("--progress-every", type=int, default=50)
    args = p.parse_args()
    setup_logging(args.log)

    s = get_settings()
    log.info("=" * 70)
    log.info("qatlas-rag full-build START  collection=%s  sample=%s  sparse=%s",
             args.collection, args.sample_papers or "ALL", not args.no_sparse)

    s3 = RustFsClient(endpoint_url=s.s3_endpoint, region=s.s3_region,
                      access_key=s.s3_access_key, secret_key=s.s3_secret_key)
    qdrant = QdrantClient(url=s.qdrant_http_url, api_key=s.qdrant_api_key, prefer_grpc=False)
    embed = EmbedClient(base_url=s.embed_url, token=s.embed_token)

    ensure_collection(qdrant, args.collection, indexing_threshold_at_build=0)
    log.info("ensured Qdrant collection %s (indexing_threshold=0 during build)", args.collection)

    objects = enumerate_objects(s3, s.s3_md_bucket)
    log.info("RustFS list complete: %d objects", len(objects))

    if args.sample_papers and args.sample_papers < len(objects):
        rng = random.Random(args.random_seed)
        objects = rng.sample(objects, args.sample_papers)
        log.info("sampled %d objects (seed=%d)", len(objects), args.random_seed)

    rs = RunStats(started_iso=time.strftime("%Y-%m-%dT%H:%M:%S"))
    t_total = time.time()

    with Manifest(s.manifest_path) as manifest:
        already = manifest.all_ids()
        for i, obj in enumerate(objects):
            if obj.arxiv_id in already:
                existing = manifest.get(obj.arxiv_id)
                if existing and existing.etag == obj.etag and existing.indexed_at:
                    rs.skipped_already_indexed += 1
                    continue
            t_p = time.time()
            try:
                n_chunks, times = process_one(
                    obj, s3=s3, embed=embed, qdrant=qdrant, manifest=manifest,
                    collection=args.collection, use_sparse=not args.no_sparse,
                )
                rs.processed += 1
                rs.chunks += n_chunks
                rs.bytes_in += obj.size
                rs.embed_s += times["embed"]
                rs.upsert_s += times["upsert"]
                rs.parse_s += times["parse"]
                rs.s3_get_s += times["s3_get"]
            except Exception as exc:
                rs.errors += 1
                rs.failed_arxiv.append(obj.arxiv_id)
                # Ensure a DocRow exists before stamping the error, otherwise
                # the UPDATE in stamp_error is a no-op and the failure
                # disappears from manifest.
                manifest.upsert(DocRow(
                    arxiv_id=obj.arxiv_id, bucket=obj.bucket, object_key=obj.key,
                    etag=obj.etag, last_modified=obj.last_modified,
                    size_bytes=obj.size,
                ))
                manifest.stamp_error(obj.arxiv_id, str(exc)[:500])
                log.exception("[%d/%d] %s FAILED: %s", i + 1, len(objects), obj.arxiv_id, exc)
                continue

            if (i + 1) % args.progress_every == 0:
                elapsed = time.time() - t_total
                rate = rs.processed / max(0.01, elapsed)
                remain = (len(objects) - (i + 1)) / max(0.01, rate)
                log.info(
                    "[%6d/%-6d] proc=%d skip=%d err=%d chunks=%d  "
                    "elapsed=%.0fs rate=%.1f papers/s  ETA=%.0fmin",
                    i + 1, len(objects), rs.processed, rs.skipped_already_indexed,
                    rs.errors, rs.chunks, elapsed, rate, remain / 60,
                )

    total = time.time() - t_total
    log.info("=" * 70)
    log.info("BUILD DONE in %.0fs (%.2fh)", total, total / 3600)
    log.info("  processed         = %d", rs.processed)
    log.info("  skipped (indexed) = %d", rs.skipped_already_indexed)
    log.info("  errors            = %d", rs.errors)
    log.info("  chunks            = %d", rs.chunks)
    log.info("  bytes_in          = %.1f MB", rs.bytes_in / 1024 / 1024)
    if rs.processed:
        log.info("  per paper: parse=%.3fs s3=%.3fs embed=%.3fs upsert=%.3fs",
                 rs.parse_s / rs.processed, rs.s3_get_s / rs.processed,
                 rs.embed_s / rs.processed, rs.upsert_s / rs.processed)
        log.info("  embed throughput  = %.1f chunks/s", rs.chunks / max(0.001, rs.embed_s))
    if rs.failed_arxiv:
        log.info("  first 10 failed   = %s", rs.failed_arxiv[:10])

    if args.enable_hnsw_after:
        log.info("enabling HNSW indexing (threshold=20000) ...")
        enable_hnsw_indexing(qdrant, args.collection, threshold=20000)
        log.info("done")

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
