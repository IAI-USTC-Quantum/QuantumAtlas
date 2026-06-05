"""Integration test against the real Qdrant on 1810 (mesh 10.144.18.10:6333).

Skipped if QATLAS_RAG_QDRANT_URL / QATLAS_RAG_QDRANT_API_KEY aren't set
in the env (CI runners and reviewers without mesh access).

Creates an ephemeral collection ``qatlas_papers_smoke``, exercises
ensure_collection / upsert / count / delete-by-arxiv-id, then drops the
collection.  This is the only way to validate the wire protocol +
collection schema before the spike (Phase 2.5).
"""

from __future__ import annotations

import os

import pytest

qdrant_client = pytest.importorskip("qdrant_client")

if not os.getenv("QATLAS_RAG_QDRANT_API_KEY") or not os.getenv("QATLAS_RAG_QDRANT_HTTP_URL"):
    pytest.skip(
        "set QATLAS_RAG_QDRANT_HTTP_URL and QATLAS_RAG_QDRANT_API_KEY to run",
        allow_module_level=True,
    )

from qdrant_client import QdrantClient  # noqa: E402
from qdrant_client.http import models as qm  # noqa: E402

from qatlas_rag.ingest.chunker import Chunk  # noqa: E402
from qatlas_rag.ingest.qdrant_store import (  # noqa: E402
    PaperMeta,
    count_for_arxiv,
    delete_paper,
    ensure_collection,
    upsert_chunks,
)


COLLECTION = "qatlas_papers_smoke"


@pytest.fixture
def client():
    c = QdrantClient(
        url=os.environ["QATLAS_RAG_QDRANT_HTTP_URL"],
        api_key=os.environ["QATLAS_RAG_QDRANT_API_KEY"],
        prefer_grpc=False,
    )
    # Pre-clean in case of a previous failed run.
    try:
        c.delete_collection(COLLECTION)
    except Exception:
        pass
    yield c
    try:
        c.delete_collection(COLLECTION)
    except Exception:
        pass
    c.close()


def test_ensure_collection_idempotent(client) -> None:
    ensure_collection(client, COLLECTION)
    # second call must not raise nor recreate
    ensure_collection(client, COLLECTION)
    info = client.get_collection(COLLECTION)
    assert info.status in {qm.CollectionStatus.GREEN, qm.CollectionStatus.YELLOW}


def test_upsert_then_count_then_delete_by_arxiv_id(client) -> None:
    ensure_collection(client, COLLECTION)
    paper = PaperMeta(
        arxiv_id="9999.99999v1",
        canonical="9999.99999",
        yymm="9999",
        version=1,
        md_object_key="9999/9999.99999v1.md",
        title="A Smoke Test Paper",
        authors=["Smoke Tester"],
        categories=["quant-ph"],
    )
    chunks = [
        Chunk(
            section_path=["Body"],
            section_level=1,
            chunk_index=i,
            text=f"smoke test chunk #{i}",
            text_hash=f"hash{i:02d}",
        )
        for i in range(3)
    ]
    # Synthetic 1024-dim vectors (all distinct so upsert doesn't dedup).
    dense = [[float((i + j) % 7) for j in range(1024)] for i in range(3)]

    written = upsert_chunks(client, paper, chunks, dense, sparse_vecs=None, name=COLLECTION)
    assert written == 3

    # upsert_chunks uses wait=False; give the server a moment to commit.
    import time as _time
    _time.sleep(0.2)
    n = count_for_arxiv(client, "9999.99999v1", name=COLLECTION)
    assert n == 3

    delete_paper(client, "9999.99999v1", name=COLLECTION)
    n = count_for_arxiv(client, "9999.99999v1", name=COLLECTION)
    assert n == 0
