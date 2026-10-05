-- +goose Up
-- External originals carry a real ePrint or normalized source-URL identity,
-- never a fabricated DOI/OpenAlex id or a title-only deduplication key.
ALTER TABLE papers ADD COLUMN external_id TEXT UNIQUE;
ALTER TABLE papers DROP CONSTRAINT papers_at_least_one_id;
ALTER TABLE papers ADD CONSTRAINT papers_at_least_one_id CHECK (
    arxiv_id IS NOT NULL OR doi IS NOT NULL OR openalex_id IS NOT NULL OR external_id IS NOT NULL
);
-- Drop/add works on PostgreSQL 14+; keep the existing reference priority.
ALTER TABLE papers DROP COLUMN paper_ref;
ALTER TABLE papers ADD COLUMN paper_ref TEXT GENERATED ALWAYS AS (
    CASE
        WHEN openalex_id IS NOT NULL THEN 'openalex:' || openalex_id
        WHEN arxiv_id IS NOT NULL THEN 'arxiv:' || arxiv_id
        WHEN doi IS NOT NULL THEN 'doi:' || doi
        ELSE external_id
    END
) STORED;
ALTER TABLE paper_identities DROP CONSTRAINT paper_identities_kind_check;
ALTER TABLE paper_identities ADD CONSTRAINT paper_identities_kind_check CHECK (
    kind IN ('doi', 'arxiv', 'arxiv_version', 'title', 'eprint', 'source_url')
);
ALTER TABLE paper_sources ADD COLUMN source_url TEXT;
ALTER TABLE paper_sources ADD COLUMN retrieved_url TEXT;
ALTER TABLE paper_sources ADD COLUMN retrieved_at TIMESTAMPTZ;
-- Only new external acquisitions participate; old source identity is unchanged.
CREATE UNIQUE INDEX paper_sources_external_bytes ON paper_sources(paper_id, origin, sha256)
    WHERE source_url IS NOT NULL;

-- +goose Down
-- Refuse destructive downgrade while external-only works exist. Operators must
-- export/adopt them explicitly; the old CHECK then provides the safety barrier.
ALTER TABLE papers DROP CONSTRAINT papers_at_least_one_id;
ALTER TABLE papers ADD CONSTRAINT papers_at_least_one_id CHECK (
    arxiv_id IS NOT NULL OR doi IS NOT NULL OR openalex_id IS NOT NULL
);
ALTER TABLE paper_identities DROP CONSTRAINT paper_identities_kind_check;
ALTER TABLE paper_identities ADD CONSTRAINT paper_identities_kind_check CHECK (
    kind IN ('doi', 'arxiv', 'arxiv_version', 'title')
);
DROP INDEX paper_sources_external_bytes;
ALTER TABLE paper_sources DROP COLUMN source_url;
ALTER TABLE paper_sources DROP COLUMN retrieved_url;
ALTER TABLE paper_sources DROP COLUMN retrieved_at;
ALTER TABLE papers DROP COLUMN paper_ref;
ALTER TABLE papers ADD COLUMN paper_ref TEXT GENERATED ALWAYS AS (
    CASE
        WHEN openalex_id IS NOT NULL THEN 'openalex:' || openalex_id
        WHEN arxiv_id IS NOT NULL THEN 'arxiv:' || arxiv_id
        WHEN doi IS NOT NULL THEN 'doi:' || doi
    END
) STORED;
ALTER TABLE papers DROP COLUMN external_id;
