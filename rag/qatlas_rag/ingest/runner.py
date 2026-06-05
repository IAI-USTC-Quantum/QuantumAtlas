"""Ingester orchestration: RustFS → diff → parse → embed → Qdrant.

Two modes:
- ``dry_run=True``  — list + diff only, report counts, never write.
- ``dry_run=False`` — full pipeline.

The runner is intentionally synchronous; concurrency comes from running
multiple processes against the same manifest (SQLite WAL).  Inside a
single process, calls to the embed worker are sequential by design (the
worker has its own GPU queue).
"""

from __future__ import annotations

import hashlib
import logging
import time
from dataclasses import dataclass

from qatlas_rag.config import get_settings
from qatlas_rag.ingest.chunker import Chunk, chunk_document
from qatlas_rag.ingest.embed_client import EmbedClient
from qatlas_rag.ingest.manifest import DocRow, Manifest
from qatlas_rag.ingest.parser import split_sections
from qatlas_rag.ingest.qdrant_store import PaperMeta, delete_paper, ensure_collection, upsert_chunks
from qatlas_rag.ingest.s3 import ObjectMeta, RustFsClient, policy_for_list_delta

logger = logging.getLogger("qatlas_rag.ingest.runner")


@dataclass
class DiffReport:
    added: list[ObjectMeta]
    changed: list[ObjectMeta]
    removed: list[str]                     # arxiv_id list
    list_count: int
    datausage_count: int | None
    deletes_allowed: bool


def compute_diff(
    s3_objects: list[ObjectMeta],
    manifest: Manifest,
    *,
    datausage_count: int | None,
) -> DiffReport:
    """Compare S3 listing against manifest.  Enforces the list-sanity policy."""
    delta = (
        len(s3_objects) - datausage_count
        if datausage_count is not None
        else 0
    )
    allow_add_update, allow_delete = policy_for_list_delta(delta)
    if not allow_add_update:
        raise RuntimeError(
            f"list-sanity ABORT: |list({len(s3_objects)}) - datausage({datausage_count})| = {abs(delta)} > 5"
        )

    seen_ids: dict[str, ObjectMeta] = {}
    for obj in s3_objects:
        try:
            seen_ids[obj.arxiv_id] = obj
        except ValueError:
            logger.warning("skip non-conformant key %r", obj.key)

    added: list[ObjectMeta] = []
    changed: list[ObjectMeta] = []
    for arxiv_id, obj in seen_ids.items():
        existing = manifest.get(arxiv_id)
        if existing is None:
            added.append(obj)
        elif existing.etag != obj.etag:
            changed.append(obj)

    manifest_ids = manifest.all_ids()
    removed = sorted(manifest_ids - seen_ids.keys())
    if removed and not allow_delete:
        logger.warning(
            "delta=%d (in [1,5]) — deletes are NOT allowed this run; would have removed %d", delta, len(removed)
        )
        removed = []

    return DiffReport(
        added=added,
        changed=changed,
        removed=removed,
        list_count=len(s3_objects),
        datausage_count=datausage_count,
        deletes_allowed=allow_delete,
    )


def process_paper(
    obj: ObjectMeta,
    *,
    s3: RustFsClient,
    embed: EmbedClient,
    qdrant,
    manifest: Manifest,
    collection: str,
    use_sparse: bool,
) -> tuple[int, str]:
    """Pipeline for one paper: GET → parse → chunk → embed → upsert.

    Returns (chunk_count, text_hash).  Marks manifest indexed on success
    or stamps error message on failure (caller decides what to re-raise).
    """
    raw = s3.get_object_bytes(obj.bucket, obj.key)
    text_hash = hashlib.sha256(raw).hexdigest()
    text = raw.decode("utf-8", errors="replace")

    sections = split_sections(text)
    chunks: list[Chunk] = chunk_document(sections)
    if not chunks:
        logger.warning("paper %s parsed to 0 chunks; skipping", obj.arxiv_id)
        return 0, text_hash

    dense, sparse = embed.embed([c.text for c in chunks], lane="build", return_sparse=use_sparse)
    paper = PaperMeta(
        arxiv_id=obj.arxiv_id,
        canonical=obj.canonical,
        yymm=obj.yymm,
        version=obj.version,
        md_object_key=obj.key,
    )
    # If this is an update, drop the old points first to avoid duplicates.
    delete_paper(qdrant, obj.arxiv_id, name=collection)
    upsert_chunks(
        qdrant, paper, chunks, dense, sparse if use_sparse else None, name=collection
    )

    manifest.upsert(
        DocRow(
            arxiv_id=obj.arxiv_id,
            bucket=obj.bucket,
            object_key=obj.key,
            etag=obj.etag,
            last_modified=obj.last_modified,
            size_bytes=obj.size,
            text_hash=text_hash,
            chunk_count=len(chunks),
        )
    )
    manifest.stamp_indexed(obj.arxiv_id, len(chunks), text_hash)
    return len(chunks), text_hash


def run_ingest(
    *,
    dry_run: bool,
    bucket: str | None = None,
    collection: str | None = None,
    use_sparse: bool = True,
    datausage_count: int | None = None,
    qdrant_client=None,
    rustfs_client: RustFsClient | None = None,
    embed_client: EmbedClient | None = None,
) -> DiffReport:
    """End-to-end entry called from CLI or from a higher-level driver.

    All clients are injectable so tests can pass mocks; production callers
    let the function build them from settings.
    """
    s = get_settings()
    bucket = bucket or s.s3_md_bucket
    collection = collection or s.qdrant_collection

    s3 = rustfs_client or RustFsClient(
        endpoint_url=s.s3_endpoint,
        region=s.s3_region,
        access_key=s.s3_access_key or "",
        secret_key=s.s3_secret_key or "",
    )

    logger.info("listing %s ...", bucket)
    t0 = time.time()
    objects = list(s3.list_bucket(bucket))
    logger.info("listed %d objects in %.1fs", len(objects), time.time() - t0)

    with Manifest(s.manifest_path) as manifest:
        report = compute_diff(objects, manifest, datausage_count=datausage_count)
        logger.info(
            "diff: %d added / %d changed / %d removed (deletes_allowed=%s)",
            len(report.added),
            len(report.changed),
            len(report.removed),
            report.deletes_allowed,
        )
        if dry_run:
            return report

        # --- non-dry-run path ---
        if qdrant_client is None:
            from qdrant_client import QdrantClient
            qdrant_client = QdrantClient(
                url=s.qdrant_http_url,
                api_key=s.qdrant_api_key,
                prefer_grpc=False,
            )
        ensure_collection(qdrant_client, collection)
        embed = embed_client or EmbedClient(base_url=s.embed_url, token=s.embed_token)

        for obj in report.added + report.changed:
            try:
                n, _ = process_paper(
                    obj,
                    s3=s3,
                    embed=embed,
                    qdrant=qdrant_client,
                    manifest=manifest,
                    collection=collection,
                    use_sparse=use_sparse,
                )
                logger.info("indexed %s (%d chunks)", obj.arxiv_id, n)
            except Exception as exc:
                logger.exception("failed %s: %s", obj.arxiv_id, exc)
                manifest.stamp_error(obj.arxiv_id, str(exc))

        for arxiv_id in report.removed:
            delete_paper(qdrant_client, arxiv_id, name=collection)
            manifest.delete(arxiv_id)
            logger.info("removed %s", arxiv_id)

        return report
