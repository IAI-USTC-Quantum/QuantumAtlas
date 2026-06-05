"""Manifest CRUD tests (pure SQLite, no external deps)."""

from __future__ import annotations

import pytest

from qatlas_rag.ingest.manifest import DocRow, Manifest


@pytest.fixture
def manifest(tmp_path):
    m = Manifest(tmp_path / "manifest.db")
    try:
        yield m
    finally:
        m.close()


def test_upsert_and_get_roundtrip(manifest: Manifest) -> None:
    doc = DocRow(
        arxiv_id="9508027v1",
        bucket="qatlas-md",
        object_key="9508/9508027v1.md",
        etag="abc123",
        last_modified="2026-06-03T00:00:00Z",
        size_bytes=12345,
    )
    manifest.upsert(doc)
    got = manifest.get("9508027v1")
    assert got is not None
    assert got.arxiv_id == doc.arxiv_id
    assert got.etag == "abc123"
    assert got.chunk_count is None  # not yet indexed


def test_upsert_then_change_etag(manifest: Manifest) -> None:
    base = DocRow(
        arxiv_id="2401.00001v1",
        bucket="qatlas-md",
        object_key="2401/2401.00001v1.md",
        etag="v1etag",
        last_modified="2026-01-01T00:00:00Z",
        size_bytes=100,
    )
    manifest.upsert(base)
    base.etag = "v2etag"
    base.last_modified = "2026-06-01T00:00:00Z"
    manifest.upsert(base)
    got = manifest.get("2401.00001v1")
    assert got is not None and got.etag == "v2etag"


def test_stamp_indexed_preserves_other_fields(manifest: Manifest) -> None:
    doc = DocRow(
        arxiv_id="a/b",
        bucket="qatlas-md",
        object_key="0000/abv1.md",
        etag="e",
        last_modified="t",
        size_bytes=1,
    )
    manifest.upsert(doc)
    manifest.stamp_indexed("a/b", chunk_count=12, text_hash="hh")
    got = manifest.get("a/b")
    assert got is not None
    assert got.chunk_count == 12
    assert got.text_hash == "hh"
    assert got.indexed_at is not None
    assert got.last_error is None


def test_stamp_error_then_indexed_clears_error(manifest: Manifest) -> None:
    doc = DocRow(
        arxiv_id="x",
        bucket="qatlas-md",
        object_key="0000/xv1.md",
        etag="e",
        last_modified="t",
        size_bytes=1,
    )
    manifest.upsert(doc)
    manifest.stamp_error("x", "kaboom")
    got = manifest.get("x")
    assert got is not None and got.last_error == "kaboom"
    manifest.stamp_indexed("x", 3, "h")
    got = manifest.get("x")
    assert got is not None and got.last_error is None


def test_delete_and_all_ids(manifest: Manifest) -> None:
    for i in range(3):
        manifest.upsert(
            DocRow(
                arxiv_id=f"id{i}",
                bucket="qatlas-md",
                object_key=f"0000/id{i}v1.md",
                etag="e",
                last_modified="t",
                size_bytes=1,
            )
        )
    assert manifest.all_ids() == {"id0", "id1", "id2"}
    manifest.delete("id1")
    assert manifest.all_ids() == {"id0", "id2"}


def test_scan_state_kv(manifest: Manifest) -> None:
    assert manifest.get_state("foo") is None
    manifest.set_state("foo", "1")
    manifest.set_state("bar", "2")
    assert manifest.get_state("foo") == "1"
    manifest.set_state("foo", "3")
    assert manifest.get_state("foo") == "3"
