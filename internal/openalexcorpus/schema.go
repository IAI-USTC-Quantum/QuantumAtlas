// Package openalexcorpus is the PostgreSQL-backed OpenAlex works corpus
// (ADR 0006). It is a locally-held copy of OpenAlex works — each record
// stored verbatim as jsonb, "only filtered, never modified" — so that
// citation context, batch analysis, and vector joins against our own
// arxiv assets all run as plain SQL instead of hitting the rate-limited
// public API.
//
// It is deliberately SEPARATE from internal/papers:
//
//   - internal/papers owns paper_works — the ~10^5-row host-core catalog
//     of the arxiv papers QuantumAtlas hosts (asset status + MinerU lease),
//     rebuildable from the asset buckets.
//   - this package owns openalex_works (+ child tables) — the ~10^8-row
//     external reference corpus, mostly NOT papers we host.
//
// Both live in the SAME database (one pgxpool, one QATLAS_POSTGRES_DSN) so
// a deep join (corpus × asset status × vectors) stays in SQL, but they are
// distinct tables with distinct lifecycles. See ADR 0006 and
// docs/concepts/storage-architecture.md.
//
// Like internal/papers, every method degrades gracefully when PostgreSQL
// is unreachable (the pool may be nil for local dev / graph-only
// deployments): operations report ErrCorpusUnavailable rather than
// panicking. Execution of the full bootstrap is operator-driven (see the
// `qatlasd openalex bootstrap-pg` subcommand), never a side effect of a
// deploy.
package openalexcorpus

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrCorpusUnavailable signals that the PostgreSQL corpus store could not
// be reached for this operation. Callers treat it as non-fatal (the corpus
// is a derived reference store, rebuildable from the snapshot).
var ErrCorpusUnavailable = errors.New("openalexcorpus: backend unavailable")

// EmbeddingDim is the dimensionality of the domain-subset embeddings. It
// matches the RAG embed worker model (BGE-M3 dense vectors, 1024-d — see
// rag/qatlas_rag/embed/worker.go). The work_embeddings table fixes this
// dimension so an HNSW/IVFFlat index can be built in the (later, subset-
// only) embedding phase. Full-corpus embeddings are explicitly out of
// scope (ADR 0006).
const EmbeddingDim = 1024

// Store is the PostgreSQL-backed OpenAlex corpus. Construct with NewStore;
// pool may be nil (local dev without PostgreSQL) in which case every
// operation reports the corpus as unavailable.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps a pgxpool.Pool (which may be nil / unconfigured). The
// caller owns the pool lifecycle (Close at shutdown). The same pool that
// backs internal/papers can be reused — they are separate tables in one
// database.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// ensure returns true when the corpus is configured. Individual queries
// surface connectivity failures as ErrCorpusUnavailable.
func (s *Store) ensure(ctx context.Context) bool {
	_ = ctx
	return s != nil && s.pool != nil
}

// Available reports whether the corpus backend is usable right now.
func (s *Store) Available(ctx context.Context) bool {
	return s.ensure(ctx)
}

// Configured reports whether a PostgreSQL pool was provided at all.
func (s *Store) Configured() bool {
	return s != nil && s.pool != nil
}

// schemaStatements are the tables + indexes applied at bootstrap. All are
// IF NOT EXISTS so repeated runs (and both edges racing) are idempotent.
//
// Hot fields are exposed via STORED generated columns derived from the
// jsonb record — the record itself is never rewritten (ADR 0006: "only
// filtered, never modified"). A bad cast on a malformed record would fail
// the whole batch (fail-loud), which is acceptable: the snapshot is
// supposed to be byte-faithful and these OpenAlex fields have stable
// types (year/count are ints, is_retracted is bool).
var schemaStatements = []string{
	// Core corpus table: each OpenAlex work verbatim as jsonb.
	`CREATE TABLE IF NOT EXISTS openalex_works (
		openalex_id text PRIMARY KEY,
		record jsonb NOT NULL,
		arxiv_id text,
		updated_date date,
		ingested_at timestamptz NOT NULL DEFAULT now(),
		publication_year int GENERATED ALWAYS AS (NULLIF(record->>'publication_year','')::int) STORED,
		work_type text GENERATED ALWAYS AS (record->>'type') STORED,
		display_name text GENERATED ALWAYS AS (record->>'display_name') STORED,
		language text GENERATED ALWAYS AS (record->>'language') STORED,
		primary_topic_id text GENERATED ALWAYS AS (record #>> '{primary_topic,id}') STORED,
		cited_by_count int GENERATED ALWAYS AS (NULLIF(record->>'cited_by_count','')::int) STORED,
		doi text GENERATED ALWAYS AS (record->>'doi') STORED,
		is_retracted boolean GENERATED ALWAYS AS (NULLIF(record->>'is_retracted','')::boolean) STORED,
		referenced_works_count int GENERATED ALWAYS AS (NULLIF(record->>'referenced_works_count','')::int) STORED,
		search_text tsvector GENERATED ALWAYS AS (
			to_tsvector('simple'::regconfig, coalesce(record->>'display_name', record->>'title', ''))
		) STORED
	)`,
	// These ALTERs make EnsureSchema forward-compatible for a database that
	// created openalex_works before the query surface gained generated
	// columns. PostgreSQL supports IF NOT EXISTS for generated columns.
	`ALTER TABLE openalex_works
		ADD COLUMN IF NOT EXISTS display_name text GENERATED ALWAYS AS (record->>'display_name') STORED`,
	`ALTER TABLE openalex_works
		ADD COLUMN IF NOT EXISTS language text GENERATED ALWAYS AS (record->>'language') STORED`,
	`ALTER TABLE openalex_works
		ADD COLUMN IF NOT EXISTS primary_topic_id text GENERATED ALWAYS AS (record #>> '{primary_topic,id}') STORED`,
	`ALTER TABLE openalex_works
		ADD COLUMN IF NOT EXISTS search_text tsvector GENERATED ALWAYS AS (
			to_tsvector('simple'::regconfig, coalesce(record->>'display_name', record->>'title', ''))
		) STORED`,
	// jsonb_path_ops GIN: smaller + faster than the default ops, supports
	// @> containment (the corpus query shape, e.g. record @> '{"type":...}'
	// or topics filters). Worth the space saving at ~10^8 rows.
	`CREATE INDEX IF NOT EXISTS openalex_works_record_gin
		ON openalex_works USING gin (record jsonb_path_ops)`,
	`CREATE INDEX IF NOT EXISTS openalex_works_pub_year
		ON openalex_works (publication_year)`,
	`CREATE INDEX IF NOT EXISTS openalex_works_type
		ON openalex_works (work_type)`,
	`CREATE INDEX IF NOT EXISTS openalex_works_language
		ON openalex_works (language) WHERE language IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS openalex_works_primary_topic
		ON openalex_works (primary_topic_id) WHERE primary_topic_id IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS openalex_works_search
		ON openalex_works USING gin (search_text)`,
	`CREATE INDEX IF NOT EXISTS openalex_works_cited_by
		ON openalex_works (cited_by_count)`,
	`CREATE INDEX IF NOT EXISTS openalex_works_doi
		ON openalex_works (doi) WHERE doi IS NOT NULL`,
	// The cross-table join key to internal/papers.paper_works: resolve
	// "which hosted arxiv papers does this corpus row correspond to".
	`CREATE INDEX IF NOT EXISTS openalex_works_arxiv
		ON openalex_works (arxiv_id) WHERE arxiv_id IS NOT NULL`,
	// Incremental refresh pulls only new updated_date partitions.
	`CREATE INDEX IF NOT EXISTS openalex_works_updated
		ON openalex_works (updated_date)`,

	// Citation edges, extracted from record.referenced_works. PG holds the
	// source so citation resolution is ALWAYS local — never a public-API
	// fallback (ADR 0006). Edges may dangle (a referenced work outside the
	// ingested set), so no FK to openalex_works. The PK doubles as the
	// forward index (who does W cite); the extra index is the reverse
	// (who cites W).
	`CREATE TABLE IF NOT EXISTS work_referenced (
		work_id text NOT NULL,
		referenced_id text NOT NULL,
		PRIMARY KEY (work_id, referenced_id)
	)`,
	`CREATE INDEX IF NOT EXISTS work_referenced_target
		ON work_referenced (referenced_id)`,

	// Domain-subset embeddings, 1:1 with openalex_works. vector(1024) =
	// BGE-M3 dense dim (EmbeddingDim). The HNSW index + population belong
	// to the later subset-embedding phase (not built here: empty table,
	// and the index is cheaper to build after a bulk load). FK with
	// ON DELETE CASCADE because the subset is small.
	`CREATE TABLE IF NOT EXISTS work_embeddings (
		openalex_id text PRIMARY KEY REFERENCES openalex_works(openalex_id) ON DELETE CASCADE,
		embedding vector(1024) NOT NULL,
		model text NOT NULL,
		created_at timestamptz NOT NULL DEFAULT now()
	)`,
}

// EnsureSchema applies all tables + indexes. Idempotent (every statement
// is IF NOT EXISTS). Returns ErrCorpusUnavailable when the backend is
// unreachable — schema is retried on the next bootstrap. Requires the
// `vector` extension (provisioned by the pgvector image / initdb hook).
func (s *Store) EnsureSchema(ctx context.Context) error {
	if !s.ensure(ctx) {
		return ErrCorpusUnavailable
	}
	for _, stmt := range schemaStatements {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
