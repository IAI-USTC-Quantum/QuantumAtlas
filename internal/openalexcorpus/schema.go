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
	"fmt"

	"github.com/jackc/pgx/v5"
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

// baseSchemaStatements are the functions, tables, and SMALL indexes applied
// at boot by EnsureSchema. They stay cheap even against a pre-existing
// ~10^8-row corpus: CREATE TABLE IF NOT EXISTS is a no-op when the table
// already exists (it never rewrites), and the only indexes here are on the
// tiny openalex_audit table. The heavy openalex_works indexes (GIN on the
// jsonb record, the citation array, the tsvector, plus the btree hot
// columns) are SEPARATE — see corpusIndexes / EnsureIndexes — because
// building them non-concurrently on a 353 GB table takes a table-level SHARE
// lock that blocks lazy fetch-on-miss write-backs and saturates I/O (ADR
// 0013). All statements are idempotent (IF NOT EXISTS / CREATE OR REPLACE)
// so repeated runs (and both edges racing) are safe.
//
// Hot fields — including the citation out-edge array
// (openalex_referenced_work_ids, ADR 0010) — are exposed via STORED
// generated columns derived from the jsonb record; the record itself is
// never rewritten (ADR 0006: "only filtered, never modified"). A bad cast
// on a malformed record would fail the whole batch (fail-loud), which is
// acceptable: the snapshot is supposed to be byte-faithful and these
// OpenAlex fields have stable types (year/count are ints, is_retracted is
// bool).
var baseSchemaStatements = []string{
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
	// NOTE: the heavy openalex_works indexes (record GIN, citation-array
	// GIN, tsvector GIN, and the btree hot columns) are deliberately NOT
	// built here — they live in corpusIndexes and are built CONCURRENTLY by
	// EnsureIndexes (gated by QATLAS_CORPUS_ENSURE_INDEXES) so they never
	// take a boot-time SHARE lock on the 353 GB table. See ADR 0013.

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
	// BGE-M3 dense dim (EmbeddingDim). Guarded by a pgvector-exists DO
	// block so EnsureSchema is safe to run at boot on a database without
	// the `vector` extension (the table is created here once pgvector is
	// present — the operator bootstrap provisions it). The HNSW index +
	// population belong to the later subset-embedding phase.
	`DO $$
	BEGIN
		IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector') THEN
			EXECUTE 'CREATE TABLE IF NOT EXISTS work_embeddings (
				openalex_id text PRIMARY KEY REFERENCES openalex_works(openalex_id) ON DELETE CASCADE,
				embedding vector(1024) NOT NULL,
				model text NOT NULL,
				created_at timestamptz NOT NULL DEFAULT now()
			)';
		END IF;
	END $$`,
}

// indexDef pairs an index name with its CONCURRENTLY DDL so EnsureIndexes
// can detect + drop a leftover INVALID build (from an interrupted
// CONCURRENTLY run) before recreating it.
type indexDef struct {
	name string
	ddl  string
}

// corpusIndexes are the heavy openalex_works indexes, built CONCURRENTLY by
// EnsureIndexes — never on the boot-critical path in EnsureSchema. Each is
// CREATE INDEX CONCURRENTLY IF NOT EXISTS so it neither takes a blocking
// SHARE lock (lazy write-backs + the operator bootstrap keep running) nor
// rebuilds an index that is already present and valid. Building any of these
// non-concurrently on the ~10^8-row / 353 GB table is what used to thrash
// the boot loop (ADR 0013).
var corpusIndexes = []indexDef{
	// jsonb_path_ops GIN: smaller + faster than the default ops, supports
	// @> containment (the corpus query shape, e.g. record @> '{"type":...}'
	// or topics filters). Worth the space saving at ~10^8 rows.
	{"openalex_works_record_gin",
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS openalex_works_record_gin
			ON openalex_works USING gin (record jsonb_path_ops)`},
	// Default-ops GIN on the citation out-edge array: supports the `?`
	// element-exists reverse-lookup ("who cites W", ADR 0010). jsonb_path_ops
	// would NOT support `?`, so this uses the default operator class.
	{"openalex_works_referenced_gin",
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS openalex_works_referenced_gin
			ON openalex_works USING gin (openalex_referenced_work_ids)`},
	{"openalex_works_pub_year",
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS openalex_works_pub_year
			ON openalex_works (publication_year)`},
	{"openalex_works_type",
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS openalex_works_type
			ON openalex_works (work_type)`},
	{"openalex_works_language",
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS openalex_works_language
			ON openalex_works (language) WHERE language IS NOT NULL`},
	{"openalex_works_primary_topic",
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS openalex_works_primary_topic
			ON openalex_works (primary_topic_id) WHERE primary_topic_id IS NOT NULL`},
	{"openalex_works_search",
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS openalex_works_search
			ON openalex_works USING gin (search_text)`},
	{"openalex_works_cited_by",
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS openalex_works_cited_by
			ON openalex_works (cited_by_count)`},
	{"openalex_works_doi",
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS openalex_works_doi
			ON openalex_works (doi) WHERE doi IS NOT NULL`},
	// The cross-table join key to internal/papers: resolve "which hosted
	// arxiv papers does this corpus row correspond to".
	{"openalex_works_arxiv",
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS openalex_works_arxiv
			ON openalex_works (arxiv_id) WHERE arxiv_id IS NOT NULL`},
	// Incremental refresh pulls only new updated_date partitions.
	{"openalex_works_updated",
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS openalex_works_updated
			ON openalex_works (updated_date)`},
}

// EnsureSchema applies the base functions, tables, and small indexes
// (baseSchemaStatements). It is fast and safe to run at every boot even
// against a pre-existing ~10^8-row corpus: CREATE TABLE IF NOT EXISTS is a
// no-op on an existing table (never a rewrite) and the only indexes it
// creates are on the tiny audit table. The heavy openalex_works indexes are
// built separately by EnsureIndexes (concurrently, gated). Idempotent (every
// statement is IF NOT EXISTS / CREATE OR REPLACE / a guarded DO block); the
// pgvector-dependent work_embeddings table is created only when the `vector`
// extension is present, so a database without pgvector still boots. Returns
// ErrCorpusUnavailable when the backend is unreachable — schema is retried
// on the next attempt.
func (s *Store) EnsureSchema(ctx context.Context) error {
	if !s.ensure(ctx) {
		return ErrCorpusUnavailable
	}
	for _, stmt := range baseSchemaStatements {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// EnsureIndexes builds the heavy openalex_works indexes (corpusIndexes)
// CONCURRENTLY, so they never take a table-level SHARE lock that would block
// the lazy fetch-on-miss write-backs or the operator's bulk bootstrap. On a
// large pre-existing corpus each build can take a long time and heavy I/O,
// which is why it is (a) separate from the boot-critical base schema and (b)
// gated by QATLAS_CORPUS_ENSURE_INDEXES so an operator pointing an edge at a
// pre-provisioned, already-indexed corpus can skip it entirely (ADR 0013).
//
// Best-effort per index: an interrupted build leaves an INVALID index behind
// (the known CONCURRENTLY footgun) which is dropped and rebuilt on the next
// call, and one failing index does not abort the rest. The combined error
// (if any) is returned so the caller can retry the remainder. Idempotent:
// CREATE INDEX CONCURRENTLY IF NOT EXISTS skips indexes already built + valid.
//
// CONCURRENTLY cannot run inside a transaction block, so every statement uses
// the simple query protocol (pgx wraps the extended protocol in an implicit
// transaction, which PostgreSQL rejects for CONCURRENTLY).
func (s *Store) EnsureIndexes(ctx context.Context) error {
	if !s.ensure(ctx) {
		return ErrCorpusUnavailable
	}
	var errs []error
	for _, idx := range corpusIndexes {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		if err := s.ensureOneIndex(ctx, idx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", idx.name, err))
		}
	}
	return errors.Join(errs...)
}

// ensureOneIndex drops a leftover INVALID index from a previously
// interrupted CONCURRENTLY build (so IF NOT EXISTS does not skip a broken
// index forever), then builds it concurrently via the simple protocol.
func (s *Store) ensureOneIndex(ctx context.Context, idx indexDef) error {
	if err := s.dropIfInvalid(ctx, idx.name); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, idx.ddl, pgx.QueryExecModeSimpleProtocol); err != nil {
		return err
	}
	return nil
}

// dropIfInvalid drops the named index only when it exists but is marked
// invalid (indisvalid = false) — the state a CREATE INDEX CONCURRENTLY that
// was interrupted (timeout / disconnect) leaves behind. A valid index or a
// not-yet-built one is left untouched.
func (s *Store) dropIfInvalid(ctx context.Context, name string) error {
	var valid bool
	err := s.pool.QueryRow(ctx, `
		SELECT i.indisvalid
		FROM pg_class c
		JOIN pg_index i ON i.indexrelid = c.oid
		WHERE c.relname = $1`, name).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // not built yet
	}
	if err != nil {
		return err
	}
	if valid {
		return nil // already good
	}
	_, err = s.pool.Exec(ctx,
		`DROP INDEX CONCURRENTLY IF EXISTS `+pgx.Identifier{name}.Sanitize(),
		pgx.QueryExecModeSimpleProtocol)
	return err
}
