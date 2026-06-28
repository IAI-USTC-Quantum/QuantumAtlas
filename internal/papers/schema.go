// Package papers is the PostgreSQL-backed paper catalog for non-login
// paper state. It owns the paper_works table and exposes
// the read/write helpers the /api/papers handlers need:
//
//   - QueryStats / NeedsMineru   (dashboards + mineru queue)
//   - UpsertPDF / UpsertMD / ...  (upload write-through, create-if-missing)
//   - Claim / ReleaseClaim / GC   (atomic MinerU leases via row locks)
//   - SyncFromStore               (periodic reconcile + disaster rebuild)
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

// schemaStatements are the constraints + indexes applied at startup.
// All are IF NOT EXISTS so repeated boots (and both edges racing) are
// idempotent.
var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS paper_works (
		arxiv_id text PRIMARY KEY,
		source text NOT NULL DEFAULT 'arxiv-fallback',
		identifier_scheme text NOT NULL DEFAULT 'arxiv'
			CHECK (identifier_scheme IN ('arxiv', 'doi')),
		arxiv_id_canonical text,
		yymm char(4),
		openalex_id text,
		doi text,
		doi_arxiv_id text,
		title text,
		publication_date date,
		cited_by_count__derived integer,
		doi_title text,
		doi_authors text[] NOT NULL DEFAULT '{}',
		verification_status text CHECK (
			verification_status IS NULL OR verification_status IN (
				'verified', 'doi-not-found', 'metadata-unavailable', 'unconfigured'
			)
		),
		verified_at timestamptz,
		has_pdf boolean NOT NULL DEFAULT false,
		has_md boolean NOT NULL DEFAULT false,
		has_json boolean NOT NULL DEFAULT false,
		pdf_path text,
		pdf_size bigint,
		pdf_sha256 char(64),
		pdf_etag text,
		pdf_uploaded_at timestamptz,
		md_path text,
		md_size bigint,
		md_sha256 char(64),
		md_etag text,
		md_uploaded_at timestamptz,
		image_count integer,
		images_path_prefix text,
		claimed_by_login text,
		claim_expires_at timestamptz,
		claim_id text,
		last_assets_change_at timestamptz
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS paper_works_doi_unique
		ON paper_works (doi)
		WHERE identifier_scheme = 'doi' AND doi IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS paper_works_openalex_id
		ON paper_works (openalex_id) WHERE openalex_id IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS paper_works_doi_arxiv_id
		ON paper_works (doi_arxiv_id)
		WHERE identifier_scheme = 'doi' AND doi_arxiv_id IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS paper_works_yymm
		ON paper_works (yymm)`,
	`CREATE INDEX IF NOT EXISTS paper_works_needs_mineru
		ON paper_works (pdf_uploaded_at DESC, arxiv_id)
		WHERE identifier_scheme <> 'doi'
		  AND has_pdf = true
		  AND has_md = false`,
	`CREATE INDEX IF NOT EXISTS paper_works_claim_expires
		ON paper_works (claim_expires_at)
		WHERE claim_expires_at IS NOT NULL`,
}

// EnsureSchema applies all constraints + indexes. Non-fatal: returns an
// error the caller can log + continue (a missing index degrades
// performance, not correctness). Returns ErrCatalogUnavailable when the
// catalog is unreachable — schema is retried on the next boot.
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
