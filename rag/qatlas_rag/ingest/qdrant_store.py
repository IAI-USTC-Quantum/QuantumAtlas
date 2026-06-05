"""Qdrant collection schema + safe upsert / filter-delete for qatlas-rag.

Collection: ``qatlas_papers_v1``
- dense vector  : size=1024 (bge-m3), Cosine distance
- sparse vector : ``sparse`` (bge-m3 lexical, used by hybrid path)
- payload index : arxiv_id / canonical / yymm / version / categories / kind

Per rubber-duck #2 we do NOT compute deterministic chunk UUIDs; instead
each upsert uses ``uuid4()`` and updates re-key by deleting every point
whose ``payload.arxiv_id`` matches first, then upserting the new set.

Per rubber-duck #5 the full payload schema (chunk_text, title, etc.)
lives next to the vector so the sidecar can return snippets without a
second round-trip.
"""

from __future__ import annotations

import logging
import uuid
from dataclasses import dataclass
from typing import Any, Iterable

from qdrant_client import QdrantClient
from qdrant_client.http import models as qm

from qatlas_rag.ingest.chunker import Chunk

logger = logging.getLogger("qatlas_rag.ingest.qdrant_store")

DENSE_NAME = "dense"
SPARSE_NAME = "sparse"
DENSE_SIZE = 1024
DEFAULT_COLLECTION = "qatlas_papers_v1"


@dataclass(frozen=True)
class PaperMeta:
    """Document-level fields the chunker doesn't know about."""

    arxiv_id: str
    canonical: str
    yymm: str
    version: int
    md_object_key: str
    title: str | None = None
    authors: list[str] | None = None
    categories: list[str] | None = None
    abstract: str | None = None


def ensure_collection(
    client: QdrantClient,
    name: str = DEFAULT_COLLECTION,
    *,
    indexing_threshold_at_build: int = 0,
) -> None:
    """Create the collection if missing.  Disables HNSW indexing during build."""
    existing = {c.name for c in client.get_collections().collections}
    if name in existing:
        return
    client.create_collection(
        collection_name=name,
        vectors_config={
            DENSE_NAME: qm.VectorParams(size=DENSE_SIZE, distance=qm.Distance.COSINE),
        },
        sparse_vectors_config={
            SPARSE_NAME: qm.SparseVectorParams(index=qm.SparseIndexParams()),
        },
        optimizers_config=qm.OptimizersConfigDiff(indexing_threshold=indexing_threshold_at_build),
    )
    for field in ("arxiv_id", "canonical", "yymm", "version", "kind"):
        client.create_payload_index(
            collection_name=name,
            field_name=field,
            field_schema=qm.PayloadSchemaType.KEYWORD,
        )
    logger.info("created collection %s", name)


def enable_hnsw_indexing(
    client: QdrantClient,
    name: str = DEFAULT_COLLECTION,
    *,
    threshold: int = 20000,
) -> None:
    """Flip optimizer back on after the full-build is done (Phase 6 step 5)."""
    client.update_collection(
        collection_name=name,
        optimizer_config=qm.OptimizersConfigDiff(indexing_threshold=threshold),
    )


def delete_paper(client: QdrantClient, arxiv_id: str, *, name: str = DEFAULT_COLLECTION) -> int:
    """Drop every point whose payload.arxiv_id matches.  Returns operation_id (best-effort count needs follow-up scroll)."""
    resp = client.delete(
        collection_name=name,
        points_selector=qm.FilterSelector(
            filter=qm.Filter(
                must=[qm.FieldCondition(key="arxiv_id", match=qm.MatchValue(value=arxiv_id))]
            )
        ),
        wait=True,
    )
    return getattr(resp, "operation_id", 0)


def upsert_chunks(
    client: QdrantClient,
    paper: PaperMeta,
    chunks: Iterable[Chunk],
    dense_vecs: list[list[float]],
    sparse_vecs: list[dict[str, Any]] | None,
    *,
    name: str = DEFAULT_COLLECTION,
    batch_size: int = 256,
) -> int:
    """Upsert one paper's chunks.  Caller is responsible for embedding them.

    `chunks`, `dense_vecs`, and (when given) `sparse_vecs` must align by index.
    Returns the number of points written.
    """
    chunks = list(chunks)
    assert len(chunks) == len(dense_vecs), f"chunk vs dense vector count mismatch: {len(chunks)} vs {len(dense_vecs)}"
    if sparse_vecs is not None:
        assert len(chunks) == len(sparse_vecs), "chunk vs sparse vector count mismatch"

    points: list[qm.PointStruct] = []
    for i, ch in enumerate(chunks):
        vectors: dict[str, Any] = {DENSE_NAME: dense_vecs[i]}
        if sparse_vecs is not None:
            sv = sparse_vecs[i]
            vectors[SPARSE_NAME] = qm.SparseVector(
                indices=sv["indices"], values=sv["values"]
            )
        payload = {
            "arxiv_id":     paper.arxiv_id,
            "canonical":    paper.canonical,
            "yymm":         paper.yymm,
            "version":      paper.version,
            "md_object_key": paper.md_object_key,
            "kind":         "body_chunk",
            "section_path": ch.section_path,
            "section_level": ch.section_level,
            "chunk_index":  ch.chunk_index,
            "chunk_text":   ch.text,
            "text_hash":    ch.text_hash,
            "char_start":   ch.char_start,
            "char_end":     ch.char_end,
            "image_refs":   ch.image_refs,
        }
        for k in ("title", "authors", "categories", "abstract"):
            v = getattr(paper, k)
            if v is not None:
                payload[k] = v
        points.append(qm.PointStruct(id=str(uuid.uuid4()), vector=vectors, payload=payload))

    written = 0
    for i in range(0, len(points), batch_size):
        client.upsert(collection_name=name, points=points[i : i + batch_size], wait=False)
        written += len(points[i : i + batch_size])
    return written


def count_for_arxiv(client: QdrantClient, arxiv_id: str, *, name: str = DEFAULT_COLLECTION) -> int:
    """Sanity / status: how many points currently exist for this paper?"""
    res = client.count(
        collection_name=name,
        count_filter=qm.Filter(
            must=[qm.FieldCondition(key="arxiv_id", match=qm.MatchValue(value=arxiv_id))]
        ),
        exact=True,
    )
    return res.count
