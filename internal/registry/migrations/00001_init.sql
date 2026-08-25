-- +goose Up
-- papers: one row per work, identity decoupled from any external id.
-- Surrogate paper_id ("qa_" + lowercase ULID); three UNIQUE external-id
-- columns; paper_ref generated; a default-asset pointer (added after
-- paper_assets exists, below).
CREATE TABLE papers (
    paper_id TEXT PRIMARY KEY,
    arxiv_id TEXT UNIQUE,
    doi TEXT UNIQUE,
    openalex_id TEXT UNIQUE,
    title_hash TEXT,
    title TEXT,
    authors TEXT[],
    publication_date DATE,
    abstract TEXT,
    status TEXT NOT NULL DEFAULT 'ready' CHECK (
        status IN ('pending', 'ready', 'failed') OR status LIKE 'merged_into:%'
    ),
    paper_ref TEXT GENERATED ALWAYS AS (
        CASE
            WHEN openalex_id IS NOT NULL THEN 'openalex:' || openalex_id
            WHEN arxiv_id IS NOT NULL THEN 'arxiv:' || arxiv_id
            WHEN doi IS NOT NULL THEN 'doi:' || doi
        END
    ) STORED,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT papers_at_least_one_id CHECK (
        arxiv_id IS NOT NULL OR doi IS NOT NULL OR openalex_id IS NOT NULL
    )
);

-- paper_assets: one PDF per row (a work can have arXiv v1/v2/... plus a
-- published version); holds the object-store keys, the MinerU lease, and
-- the derived asset state.
CREATE TABLE paper_assets (
    asset_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    paper_id TEXT NOT NULL REFERENCES papers(paper_id) ON DELETE CASCADE,
    source TEXT NOT NULL CHECK (source IN ('arxiv', 'published')),
    arxiv_version INT,
    pdf_path TEXT NOT NULL,
    pdf_size BIGINT,
    pdf_sha256 CHAR(64),
    mineru_md_path TEXT,
    mineru_json_path TEXT,
    image_count INT,
    lease_id TEXT,
    lease_holder TEXT,
    lease_expires_at TIMESTAMPTZ,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT paper_assets_md_json_together CHECK (
        (mineru_md_path IS NULL) = (mineru_json_path IS NULL)
    ),
    CONSTRAINT paper_assets_source_version CHECK (
        (source = 'arxiv' AND arxiv_version IS NOT NULL)
        OR (source = 'published' AND arxiv_version IS NULL)
    ),
    CONSTRAINT paper_assets_arxiv_version_unique UNIQUE (paper_id, source, arxiv_version)
);

-- One published PDF per paper (the composite UNIQUE above admits multiple
-- published rows via NULL arxiv_version).
CREATE UNIQUE INDEX paper_assets_one_published
    ON paper_assets (paper_id) WHERE source = 'published';
CREATE INDEX paper_assets_paper
    ON paper_assets (paper_id);
CREATE INDEX paper_assets_needs_mineru
    ON paper_assets (fetched_at DESC, asset_id)
    WHERE mineru_md_path IS NULL;

ALTER TABLE papers
    ADD COLUMN default_asset_id BIGINT
    REFERENCES paper_assets(asset_id) ON DELETE SET NULL;

-- Default-asset policy: published first, else the highest arXiv version.
-- Recomputed whenever a paper's assets change.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION papers_refresh_default_asset()
    RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    pid text := COALESCE(NEW.paper_id, OLD.paper_id);
BEGIN
    UPDATE papers SET default_asset_id = (
        SELECT asset_id FROM paper_assets
        WHERE paper_id = pid
        ORDER BY (source = 'published') DESC, arxiv_version DESC NULLS LAST, asset_id DESC
        LIMIT 1
    ) WHERE paper_id = pid;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER paper_assets_default_trigger
    AFTER INSERT OR UPDATE OF source, arxiv_version OR DELETE ON paper_assets
    FOR EACH ROW EXECUTE FUNCTION papers_refresh_default_asset();

-- paper_identities: the multi-identity dedup index. Every way a paper can
-- be named (DOI, bare arXiv id, versioned arXiv id, title hash) maps to
-- the owning paper; ResolveOrMint resolves and backfills through this
-- table.
CREATE TABLE paper_identities (
    identity_key TEXT PRIMARY KEY,
    paper_id TEXT NOT NULL REFERENCES papers(paper_id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('doi', 'arxiv', 'arxiv_version', 'title')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX paper_identities_paper
    ON paper_identities (paper_id);

-- +goose Down
DROP TABLE IF EXISTS paper_identities;
DROP TRIGGER IF EXISTS paper_assets_default_trigger ON paper_assets;
DROP FUNCTION IF EXISTS papers_refresh_default_asset();
ALTER TABLE papers DROP COLUMN IF EXISTS default_asset_id;
DROP TABLE IF EXISTS paper_assets;
DROP TABLE IF EXISTS papers;
