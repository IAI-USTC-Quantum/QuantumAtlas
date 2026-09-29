-- +goose Up
-- Block-level comments, Q1 (plan docs/plans/qatlas-block-comments.md
-- §12.3): immutable source-PDF identities and immutable parse revisions.
--
-- paper_sources: one row per distinct source PDF byte-identity a paper
-- has acquired (arXiv v2, arXiv v3, contributor upload, ...). Rows are
-- append-only: a re-fetch of the same bytes under a different origin is
-- a NEW source; the sha256 pins the bytes so comments can always cite
-- exactly what was parsed.
--
-- parse_revisions: one row per immutable parse artifact (MinerU Middle
-- JSON). is_current is the "current result" pointer (§4.2): a new parse
-- flips the pointer, never overwrites the old artifact. The partial
-- unique index guarantees at most one current revision per paper.
CREATE TABLE IF NOT EXISTS paper_sources (
    source_id TEXT PRIMARY KEY,
    paper_id TEXT NOT NULL REFERENCES papers(paper_id) ON DELETE CASCADE,
    origin TEXT NOT NULL,
    sha256 CHAR(64) NOT NULL,
    objstore_key TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS paper_sources_paper_idx ON paper_sources(paper_id);
CREATE INDEX IF NOT EXISTS paper_sources_paper_sha_idx ON paper_sources(paper_id, sha256);

CREATE TABLE IF NOT EXISTS parse_revisions (
    revision_id TEXT PRIMARY KEY,
    paper_id TEXT NOT NULL REFERENCES papers(paper_id) ON DELETE CASCADE,
    source_id TEXT NOT NULL REFERENCES paper_sources(source_id) ON DELETE CASCADE,
    schema TEXT NOT NULL,
    schema_version TEXT NOT NULL,
    artifact_sha256 CHAR(64) NOT NULL,
    objstore_key TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    is_current BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX IF NOT EXISTS parse_revisions_paper_idx ON parse_revisions(paper_id);
CREATE INDEX IF NOT EXISTS parse_revisions_source_idx ON parse_revisions(source_id);
CREATE UNIQUE INDEX IF NOT EXISTS parse_revisions_one_current
    ON parse_revisions(paper_id) WHERE is_current;

-- +goose Down
DROP TABLE IF EXISTS parse_revisions;
DROP TABLE IF EXISTS paper_sources;
