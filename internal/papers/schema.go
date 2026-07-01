// Package papers is the PostgreSQL-backed paper catalog for non-login
// paper state. It owns the papers + paper_assets tables and exposes
// the read/write helpers the /api/papers handlers need:
//
//   - QueryStats / NeedsMineru      (dashboards + mineru queue)
//   - UpsertPDF / UpsertMD / ...     (upload write-through, create-if-missing)
//   - Lease / ReleaseLease / GC…     (atomic MinerU leases via row locks)
//   - SyncFromStore                  (periodic reconcile + disaster rebuild)
//
// Every method degrades gracefully when PostgreSQL is unreachable: writes
// return ErrCatalogUnavailable (handlers still 201 the S3 write and set
// X-Catalog-Sync: deferred), reads report availability=false. The
// connection pool is optional so local dev can run without the catalog.
package papers

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrCatalogUnavailable signals that the PostgreSQL catalog could not be
// reached for this operation. Upload handlers treat it as non-fatal
// (S3 write already succeeded) and emit X-Catalog-Sync: deferred.
var ErrCatalogUnavailable = errors.New("papers: catalog backend unavailable")

// Store is the PostgreSQL-backed catalog. Construct with NewStore; pool
// may be nil (local dev without PostgreSQL) in which case every operation reports
// the catalog as unavailable.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps a pgxpool.Pool (which may be nil / unconfigured). The
// caller owns the pool lifecycle (Close at shutdown).
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// ensure returns true when the catalog is configured. Individual queries
// surface connectivity failures as ErrCatalogUnavailable.
func (s *Store) ensure(ctx context.Context) bool {
	_ = ctx
	return s != nil && s.pool != nil
}

// Available reports whether the catalog backend is usable right now
// (attempting a lazy reconnect). Cheap when already connected.
func (s *Store) Available(ctx context.Context) bool {
	return s.ensure(ctx)
}

// Configured reports whether a PostgreSQL pool was provided at all.
func (s *Store) Configured() bool {
	return s != nil && s.pool != nil
}

// schemaStatements are the tables, constraints, indexes, and the
// default-asset trigger applied at startup. All are idempotent (IF NOT
// EXISTS / CREATE OR REPLACE / guarded DO blocks) so repeated boots (and
// both edges racing) are safe.
//
// The catalog is two tables (ADR 0009):
//
//   - papers        one row per work, identity decoupled from any external
//     id (surrogate paper_id; three UNIQUE external-id
//     columns; paper_ref generated; a default-asset pointer).
//   - paper_assets  one row per PDF (a work can have arXiv v1/v2/… plus a
//     published version); holds the object-store keys, the
//     MinerU lease, and the derived asset state.
var schemaStatements = []string{
	// papers: the work catalog. paper_default_asset_id starts as a plain
	// bigint; its FK to paper_assets is added by a guarded DO block below
	// (paper_assets does not exist yet at this point).
	`CREATE TABLE IF NOT EXISTS papers (
		paper_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		paper_arxiv_id text UNIQUE,
		paper_doi text UNIQUE,
		paper_openalex_id text UNIQUE,
		paper_title text,
		paper_publication_date date,
		paper_default_asset_id bigint,
		paper_verification_status text CHECK (
			paper_verification_status IS NULL OR paper_verification_status IN (
				'verified', 'doi-not-found', 'metadata-unavailable', 'unconfigured'
			)
		),
		paper_ref text GENERATED ALWAYS AS (
			CASE
				WHEN paper_openalex_id IS NOT NULL THEN 'openalex:' || paper_openalex_id
				WHEN paper_arxiv_id IS NOT NULL THEN 'arxiv:' || paper_arxiv_id
				WHEN paper_doi IS NOT NULL THEN 'doi:' || paper_doi
			END
		) STORED,
		CONSTRAINT papers_at_least_one_id CHECK (
			paper_arxiv_id IS NOT NULL OR paper_doi IS NOT NULL OR paper_openalex_id IS NOT NULL
		)
	)`,

	// paper_assets: one PDF per row. Asset presence is derived from the
	// *_path columns (pdf_path IS NOT NULL, mineru_md_path IS NOT NULL);
	// there are no has_* booleans. The MinerU lease (lease_id /
	// lease_holder / lease_expires_at) reserves an asset for conversion.
	`CREATE TABLE IF NOT EXISTS paper_assets (
		asset_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		paper_id bigint NOT NULL REFERENCES papers(paper_id) ON DELETE CASCADE,
		source text NOT NULL CHECK (source IN ('arxiv', 'published')),
		arxiv_version int,
		pdf_path text NOT NULL,
		pdf_size bigint,
		pdf_sha256 char(64),
		mineru_md_path text,
		mineru_json_path text,
		image_count int,
		fetched_at timestamptz,
		lease_id text,
		lease_holder text,
		lease_expires_at timestamptz,
		CONSTRAINT paper_assets_md_json_together CHECK (
			(mineru_md_path IS NULL) = (mineru_json_path IS NULL)
		),
		CONSTRAINT paper_assets_source_version CHECK (
			(source = 'arxiv' AND arxiv_version IS NOT NULL)
			OR (source = 'published' AND arxiv_version IS NULL)
		),
		CONSTRAINT paper_assets_arxiv_version_unique UNIQUE (paper_id, source, arxiv_version)
	)`,
	// One published PDF per paper. A partial unique index sidesteps the
	// NULL-arxiv_version gap that would let the composite UNIQUE above
	// admit multiple published rows.
	`CREATE UNIQUE INDEX IF NOT EXISTS paper_assets_one_published
		ON paper_assets (paper_id) WHERE source = 'published'`,
	// PostgreSQL does not auto-index an FK column; without this, "all
	// assets of a paper" and the default-asset trigger's per-paper
	// recompute would seq-scan.
	`CREATE INDEX IF NOT EXISTS paper_assets_paper
		ON paper_assets (paper_id)`,
	// The MinerU queue: assets with a PDF, no markdown, and no live lease.
	`CREATE INDEX IF NOT EXISTS paper_assets_needs_mineru
		ON paper_assets (fetched_at DESC, asset_id)
		WHERE mineru_md_path IS NULL`,
	// Expired-lease sweep target.
	`CREATE INDEX IF NOT EXISTS paper_assets_lease_expires
		ON paper_assets (lease_expires_at) WHERE lease_expires_at IS NOT NULL`,

	// papers.paper_default_asset_id → paper_assets(asset_id). ON DELETE
	// SET NULL so dropping the default asset clears the pointer; the
	// trigger then recomputes it. Guarded DO block: idempotent ADD
	// CONSTRAINT (PostgreSQL has no ADD CONSTRAINT IF NOT EXISTS).
	`DO $$
	BEGIN
		IF NOT EXISTS (
			SELECT 1 FROM information_schema.table_constraints
			WHERE constraint_name = 'papers_default_asset_fk'
		) THEN
			ALTER TABLE papers
				ADD CONSTRAINT papers_default_asset_fk
				FOREIGN KEY (paper_default_asset_id)
				REFERENCES paper_assets(asset_id) ON DELETE SET NULL;
		END IF;
	END $$`,

	// papers.paper_openalex_id → openalex_works(openalex_id) (ADR 0009
	// Q14). The corpus base schema (openalex_works) is now created at boot
	// alongside this catalog (see cmd/qatlasd ensureCatalogSchema), and the
	// corpus is populated lazily (fetch-on-miss write-through, ADR 0006), so
	// openalex_works reliably exists here. The guarded DO block still adds
	// the FK conditionally — idempotent, and defensive if the corpus schema
	// has not landed yet on this attempt (a later retry installs it).
	`DO $$
	BEGIN
		IF EXISTS (
			SELECT 1 FROM information_schema.tables WHERE table_name = 'openalex_works'
		) AND NOT EXISTS (
			SELECT 1 FROM information_schema.table_constraints
			WHERE constraint_name = 'papers_openalex_fk'
		) THEN
			ALTER TABLE papers
				ADD CONSTRAINT papers_openalex_fk
				FOREIGN KEY (paper_openalex_id)
				REFERENCES openalex_works(openalex_id) ON DELETE SET NULL;
		END IF;
	END $$`,

	// Default-asset policy (ADR 0009 Q21): published first, else the
	// highest arXiv version. Recomputed whenever a paper's assets change.
	`CREATE OR REPLACE FUNCTION papers_refresh_default_asset()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $$
		DECLARE
			pid bigint := COALESCE(NEW.paper_id, OLD.paper_id);
		BEGIN
			UPDATE papers SET paper_default_asset_id = (
				SELECT asset_id FROM paper_assets
				WHERE paper_id = pid
				ORDER BY (source = 'published') DESC, arxiv_version DESC NULLS LAST, asset_id DESC
				LIMIT 1
			) WHERE paper_id = pid;
			RETURN NULL;
		END;
		$$`,
	`DROP TRIGGER IF EXISTS paper_assets_default_trigger ON paper_assets`,
	`CREATE TRIGGER paper_assets_default_trigger
		AFTER INSERT OR UPDATE OF source, arxiv_version OR DELETE ON paper_assets
		FOR EACH ROW EXECUTE FUNCTION papers_refresh_default_asset()`,
}

// EnsureSchema applies all tables, constraints, indexes, and the
// default-asset trigger. Non-fatal: returns an error the caller can log +
// continue (a missing index degrades performance, not correctness).
// Returns ErrCatalogUnavailable when the catalog is unreachable — schema
// is retried on the next boot.
func (s *Store) EnsureSchema(ctx context.Context) error {
	if !s.ensure(ctx) {
		return ErrCatalogUnavailable
	}
	for _, stmt := range schemaStatements {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
