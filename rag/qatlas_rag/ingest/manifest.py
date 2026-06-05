"""SQLite manifest for the ingester.

One row per document (one paper = one MD object in qatlas-md).  Tracks
identity (arxiv_id / object key), state (etag / hash / last_modified),
and processing outcome (chunk_count / indexed_at / last_error).

The actual chunks live in Qdrant; we re-derive them on demand rather
than mirroring per-chunk state here.  Deletes go through Qdrant payload
filter on `arxiv_id`, so the manifest stays small.
"""

from __future__ import annotations

import sqlite3
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Iterator


SCHEMA = """\
CREATE TABLE IF NOT EXISTS docs (
    arxiv_id      TEXT PRIMARY KEY,
    bucket        TEXT NOT NULL,
    object_key    TEXT NOT NULL,
    etag          TEXT NOT NULL,
    last_modified TEXT NOT NULL,
    size_bytes    INTEGER NOT NULL,
    text_hash     TEXT,
    chunk_count   INTEGER,
    indexed_at    TEXT,
    last_error    TEXT
);
CREATE INDEX IF NOT EXISTS idx_docs_indexed  ON docs (indexed_at);
CREATE INDEX IF NOT EXISTS idx_docs_modified ON docs (last_modified);

CREATE TABLE IF NOT EXISTS scan_state (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
"""


@dataclass
class DocRow:
    arxiv_id: str
    bucket: str
    object_key: str
    etag: str
    last_modified: str
    size_bytes: int
    text_hash: str | None = None
    chunk_count: int | None = None
    indexed_at: str | None = None
    last_error: str | None = None


class Manifest:
    """Thin wrapper around SQLite. Connection is per-instance, single-thread.

    Caller is responsible for closing via `close()` or using the manifest
    as a context manager.
    """

    def __init__(self, path: str | Path) -> None:
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self.conn = sqlite3.connect(self.path, isolation_level=None)
        self.conn.row_factory = sqlite3.Row
        self.conn.executescript(SCHEMA)

    def __enter__(self) -> "Manifest":
        return self

    def __exit__(self, *_a: object) -> None:
        self.close()

    def close(self) -> None:
        self.conn.close()

    # --- doc CRUD ---------------------------------------------------------

    def get(self, arxiv_id: str) -> DocRow | None:
        cur = self.conn.execute(
            "SELECT * FROM docs WHERE arxiv_id = ?", (arxiv_id,)
        )
        row = cur.fetchone()
        return _row_to_doc(row) if row else None

    def upsert(self, doc: DocRow) -> None:
        self.conn.execute(
            """\
INSERT INTO docs (arxiv_id, bucket, object_key, etag, last_modified,
                  size_bytes, text_hash, chunk_count, indexed_at, last_error)
VALUES (:arxiv_id, :bucket, :object_key, :etag, :last_modified,
        :size_bytes, :text_hash, :chunk_count, :indexed_at, :last_error)
ON CONFLICT(arxiv_id) DO UPDATE SET
    bucket=excluded.bucket,
    object_key=excluded.object_key,
    etag=excluded.etag,
    last_modified=excluded.last_modified,
    size_bytes=excluded.size_bytes,
    text_hash=COALESCE(excluded.text_hash, docs.text_hash),
    chunk_count=COALESCE(excluded.chunk_count, docs.chunk_count),
    indexed_at=COALESCE(excluded.indexed_at, docs.indexed_at),
    last_error=excluded.last_error
""",
            doc.__dict__,
        )

    def delete(self, arxiv_id: str) -> None:
        self.conn.execute("DELETE FROM docs WHERE arxiv_id = ?", (arxiv_id,))

    def iter_all(self) -> Iterator[DocRow]:
        for row in self.conn.execute("SELECT * FROM docs ORDER BY arxiv_id"):
            yield _row_to_doc(row)

    def all_ids(self) -> set[str]:
        return {row["arxiv_id"] for row in self.conn.execute("SELECT arxiv_id FROM docs")}

    # --- scan_state -------------------------------------------------------

    def set_state(self, key: str, value: str) -> None:
        self.conn.execute(
            "INSERT INTO scan_state (key, value) VALUES (?, ?) "
            "ON CONFLICT(key) DO UPDATE SET value=excluded.value",
            (key, value),
        )

    def get_state(self, key: str) -> str | None:
        cur = self.conn.execute("SELECT value FROM scan_state WHERE key = ?", (key,))
        row = cur.fetchone()
        return row["value"] if row else None

    def stamp_indexed(self, arxiv_id: str, chunk_count: int, text_hash: str) -> None:
        self.conn.execute(
            """\
UPDATE docs SET
    indexed_at = ?,
    chunk_count = ?,
    text_hash = ?,
    last_error = NULL
WHERE arxiv_id = ?
""",
            (_now_iso(), chunk_count, text_hash, arxiv_id),
        )

    def stamp_error(self, arxiv_id: str, msg: str) -> None:
        self.conn.execute(
            "UPDATE docs SET last_error = ? WHERE arxiv_id = ?",
            (msg, arxiv_id),
        )


def _row_to_doc(row: sqlite3.Row) -> DocRow:
    return DocRow(**{k: row[k] for k in row.keys()})


def _now_iso() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
