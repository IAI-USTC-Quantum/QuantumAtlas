-- +goose Up
-- DDL only: source PDFs migrate lazily on access; no legacy parser outputs are
-- copied or declared ready. Existing paper_sources / parse_revisions IDs remain
-- unchanged so discussion anchors continue to address their original identities.
-- A legacy PDF path is pinned on first import. Repeated/concurrent requests
-- then use its frozen source even if the old object is overwritten or removed.
CREATE UNIQUE INDEX IF NOT EXISTS paper_sources_scoped_identity_idx
    ON paper_sources(paper_id, source_id);
CREATE UNIQUE INDEX IF NOT EXISTS parse_revisions_scoped_identity_idx
    ON parse_revisions(paper_id, source_id, revision_id);

CREATE TABLE IF NOT EXISTS paper_source_imports (
    paper_id TEXT NOT NULL REFERENCES papers(paper_id) ON DELETE CASCADE,
    legacy_key TEXT NOT NULL,
    source_id TEXT NOT NULL,
    PRIMARY KEY (paper_id, legacy_key),
    FOREIGN KEY (paper_id, source_id) REFERENCES paper_sources(paper_id, source_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS parse_bundles (
    revision_id TEXT PRIMARY KEY REFERENCES parse_revisions(revision_id) ON DELETE CASCADE,
    paper_id TEXT NOT NULL REFERENCES papers(paper_id) ON DELETE CASCADE,
    source_id TEXT NOT NULL,
    source_pdf_sha256 CHAR(64) NOT NULL,
    manifest_key TEXT NOT NULL,
    manifest_sha256 CHAR(64) NOT NULL,
    middle_path TEXT NOT NULL,
    markdown_path TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (paper_id, source_id) REFERENCES paper_sources(paper_id, source_id) ON DELETE CASCADE,
    FOREIGN KEY (paper_id, source_id, revision_id)
        REFERENCES parse_revisions(paper_id, source_id, revision_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS parse_bundles_paper_source_idx
    ON parse_bundles(paper_id, source_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS parse_bundles;
DROP TABLE IF EXISTS paper_source_imports;
DROP INDEX IF EXISTS parse_revisions_scoped_identity_idx;
DROP INDEX IF EXISTS paper_sources_scoped_identity_idx;
