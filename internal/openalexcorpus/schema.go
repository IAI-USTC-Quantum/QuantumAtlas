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

// schemaStatements are the functions, tables + indexes applied at
// bootstrap. All are idempotent (IF NOT EXISTS / CREATE OR REPLACE) so
// repeated runs (and both edges racing) are safe.
//
// Hot fields — including the citation out-edge array
// (openalex_referenced_work_ids, ADR 0010) — are exposed via STORED
// generated columns derived from the jsonb record; the record itself is
// never rewritten (ADR 0006: "only filtered, never modified"). A bad cast
// on a malformed record would fail the whole batch (fail-loud), which is
// acceptable: the snapshot is supposed to be byte-faithful and these
// OpenAlex fields have stable types (year/count are ints, is_retracted is
// bool).
var schemaStatements = []string{
	// strip_openalex_prefix turns record.referenced_works (an array of
	// "https://openalex.org/W…" URLs) into an array of bare "W…" ids. A
	// generated column cannot subquery/aggregate, so the array transform is
	// packed into this IMMUTABLE function (ADR 0010).
	`CREATE OR REPLACE FUNCTION strip_openalex_prefix(urls jsonb)
		RETURNS jsonb
		LANGUAGE plpgsql
		IMMUTABLE
		AS $$
		DECLARE
			result jsonb;
		BEGIN
			IF urls IS NULL THEN
				RETURN '[]'::jsonb;
			END IF;
			SELECT coalesce(jsonb_agg(regexp_replace(elem, '^.*/', '')), '[]'::jsonb)
				INTO result
				FROM jsonb_array_elements_text(urls) AS elem;
			RETURN result;
		END;
		$$`,
	// Core corpus table: each OpenAlex work verbatim as jsonb, with STORED
	// generated hot columns + the inline citation out-edge array.
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
		openalex_referenced_work_ids jsonb GENERATED ALWAYS AS (strip_openalex_prefix(record->'referenced_works')) STORED,
		search_text tsvector GENERATED ALWAYS AS (
			to_tsvector('simple'::regconfig, coalesce(record->>'display_name', record->>'title', ''))
		) STORED
	)`,
	// jsonb_path_ops GIN: smaller + faster than the default ops, supports
	// @> containment (the corpus query shape, e.g. record @> '{"type":...}'
	// or topics filters). Worth the space saving at ~10^8 rows.
	`CREATE INDEX IF NOT EXISTS openalex_works_record_gin
		ON openalex_works USING gin (record jsonb_path_ops)`,
	// Default-ops GIN on the citation out-edge array: supports the `?`
	// element-exists reverse-lookup ("who cites W", ADR 0010). jsonb_path_ops
	// would NOT support `?`, so this uses the default operator class.
	`CREATE INDEX IF NOT EXISTS openalex_works_referenced_gin
		ON openalex_works USING gin (openalex_referenced_work_ids)`,
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

	// Single-row refresh watermark (ADR 0010): incremental refresh pulls
	// only new updated_date partitions from the snapshot, tracked here so no
	// per-row updated_date bookkeeping is needed at 10^8 rows.
	`CREATE TABLE IF NOT EXISTS openalex_sync_state (
		id boolean PRIMARY KEY DEFAULT true CHECK (id),
		last_updated_date date,
		last_synced_at timestamptz,
		snapshot_version text
	)`,

	// API-comparison audit (ADR 0010): append-only, one row per sampled
	// (work, field) including verdict='ok'. sampled_count / coverage /
	// unresolved are all derived at read time, never stored or mutated.
	`CREATE TABLE IF NOT EXISTS openalex_audit (
		id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		run_id bigint,
		openalex_id text,
		field text,
		verdict text CHECK (verdict IN ('ok','field_mismatch','cited_by_regressed','stale_drift')),
		local_value jsonb,
		remote_value jsonb,
		checked_at timestamptz NOT NULL DEFAULT now()
	)`,
	`CREATE INDEX IF NOT EXISTS openalex_audit_run
		ON openalex_audit (run_id)`,
	`CREATE INDEX IF NOT EXISTS openalex_audit_field
		ON openalex_audit (openalex_id, field)`,

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
