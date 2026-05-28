// Package paperindex provides a fast, in-process catalog of every paper
// known to QuantumAtlas, backed by a single Parquet object in the same
// RustFS bucket that holds the PDF / markdown / JSON assets.
//
// Rationale and design rationale live in docs/architecture.md (section
// "论文元数据索引 / Lakehouse 模式"). The TL;DR:
//
//   - Object storage (RustFS) is good at "GET one blob by key" but
//     terrible at "count / filter / group across many blobs"
//     (the recursive ListObjects path on RustFS-beta times out at any
//     non-trivial bucket size, which is why the old store.ListPrefix
//     impl of needs-mineru hung for 2+ minutes).
//   - We add NO new stateful service. Instead, the source of truth
//     stays in the bucket: PDFs/MDs/JSON files. A *derived* catalog
//     (single parquet at index/papers.parquet) is maintained as
//     another object in the same bucket, with the same svcacct
//     credentials and policy. Backup/migration/DR are unchanged.
//   - Queries are answered by an in-process DuckDB instance
//     (marcboeker/go-duckdb cgo binding) holding the parquet's
//     contents in memory. No DuckDB server, no .db file on disk —
//     just an embedded library, conceptually like sqlite3 but for
//     columnar OLAP.
//
// This package implements only the read path (Phase 1 of the rollout):
// load parquet at startup, refresh periodically when the remote etag
// changes, expose a small typed query API. The write path (per-upload
// upsert + CAS flush back to S3) is Phase 2 and intentionally not yet
// implemented — see TODO_PHASE2 markers.
package paperindex

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// DefaultParquetKey is the object key inside the qatlas-raw bucket where
// the catalog parquet lives. Kept as a package-level constant so other
// callers (bootstrap subcommand, future reconciler) reference the same
// path without drift.
const DefaultParquetKey = "index/papers.parquet"

// DefaultRefreshInterval is how often the background ticker re-checks
// the remote parquet's etag and reloads if it changed. 60s is a
// compromise between "see cross-edge writes promptly" and "don't
// hammer the bucket with HEAD requests". The other edge's upserts
// are at-most ~60s stale to readers on this edge.
const DefaultRefreshInterval = 60 * time.Second

// Config bundles everything Store needs to connect DuckDB to the S3
// bucket holding the parquet. Mirrors the QATLAS_S3_* env vars; the
// caller wires it from internal/config.Config.
type Config struct {
	// S3Endpoint is the internal RustFS endpoint, e.g.
	// "http://10.144.18.10:9000". We deliberately use the INTERNAL
	// endpoint (mesh-side, not the public Caddy-fronted one) for
	// server↔storage traffic — same convention as the minio-go
	// client in internal/objstore.S3Store. The public endpoint
	// is only used when signing presigned URLs handed to end
	// users, which paperindex never does.
	S3Endpoint string

	// Bucket is the bucket name, e.g. "qatlas-raw".
	Bucket string

	// AccessKeyID and SecretAccessKey are the svcacct credentials.
	// Per scripts/rustfs_bootstrap.sh, the qatlas-raw-rw policy
	// attached to this svcacct already restricts access to this
	// single bucket — no additional ACL work needed for paperindex.
	AccessKeyID     string
	SecretAccessKey string

	// ParquetKey overrides DefaultParquetKey. Tests use this to point
	// at a per-test parquet object; production leaves it empty.
	ParquetKey string

	// RefreshInterval overrides DefaultRefreshInterval. Tests set
	// it short (e.g. 100ms) to exercise the refresh path quickly.
	RefreshInterval time.Duration

	// LocalParquetPath bypasses S3 entirely and loads the parquet
	// from a local file. Used by tests; never set in production.
	// When non-empty, S3Endpoint/Bucket/keys are ignored.
	LocalParquetPath string
}

// Store is the in-process catalog. Construct one per server process at
// startup and keep it alive for the process lifetime. All methods are
// safe for concurrent use; the underlying sql.DB has its own pool.
//
// The catalog is held as an in-memory DuckDB table named "papers".
// Schema follows the parquet's natural shape — see the schema doc
// in docs/architecture.md and the bootstrap subcommand which writes
// the parquet.
type Store struct {
	db       *sql.DB
	cfg      Config
	source   string // for query strings: 's3://bucket/key' or local path
	keyForRefresh string
	bucketForRefresh string

	mu       sync.RWMutex
	loadedAt time.Time
	rowCount int

	closeCh chan struct{}
	wg      sync.WaitGroup
}

// New constructs a Store, opens an in-memory DuckDB, installs the httpfs
// extension, registers the S3 secret, and performs the initial Load.
// Returns an error if any of these setup steps fail.
//
// If the parquet object doesn't exist yet in the bucket (fresh
// deployment, no bootstrap-index run yet), Load returns a "graceful
// empty" — the Store works but reports 0 rows for everything. Callers
// (e.g. needs-mineru handler) can choose to fall back to the old
// S3-LIST impl in that case, or just serve zeros.
//
// Background refresh ticker starts automatically; stop it via Close().
func New(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.RefreshInterval == 0 {
		cfg.RefreshInterval = DefaultRefreshInterval
	}
	if cfg.ParquetKey == "" {
		cfg.ParquetKey = DefaultParquetKey
	}

	db, err := sql.Open("duckdb", "") // empty DSN = in-memory database
	if err != nil {
		return nil, fmt.Errorf("paperindex: open duckdb: %w", err)
	}
	// DuckDB serializes operations on a single connection by default
	// (no concurrent writes); cap connection pool to 1 to keep the
	// in-memory state coherent across goroutines. Reads remain fast
	// because DuckDB's own intra-query parallelism kicks in within
	// a single connection.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &Store{
		db:      db,
		cfg:     cfg,
		closeCh: make(chan struct{}),
	}

	// Decide source URI: local file (tests) vs s3:// (production).
	if cfg.LocalParquetPath != "" {
		s.source = sqlQuoteLiteral(cfg.LocalParquetPath)
	} else {
		if cfg.S3Endpoint == "" || cfg.Bucket == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
			_ = db.Close()
			return nil, errors.New("paperindex: S3 endpoint/bucket/keys required when LocalParquetPath unset")
		}
		if err := setupHTTPFS(ctx, db, cfg); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("paperindex: setup httpfs: %w", err)
		}
		s.source = sqlQuoteLiteral(fmt.Sprintf("s3://%s/%s", cfg.Bucket, cfg.ParquetKey))
		s.bucketForRefresh = cfg.Bucket
		s.keyForRefresh = cfg.ParquetKey
	}

	if err := s.Load(ctx); err != nil {
		// Soft-fail: log and continue with an empty catalog. The bucket
		// may not have a parquet yet (fresh deployment). Subsequent
		// Refresh calls will pick it up once it exists.
		slog.Warn("paperindex: initial Load failed; continuing with empty catalog",
			"source", s.source, "error", err)
		if err := s.loadEmptyTable(ctx); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("paperindex: create empty fallback table: %w", err)
		}
	}

	// Background refresh ticker. Stops cleanly on Close().
	s.wg.Add(1)
	go s.refreshLoop()

	return s, nil
}

// Close stops the background refresh and releases DB resources.
func (s *Store) Close() error {
	close(s.closeCh)
	s.wg.Wait()
	return s.db.Close()
}

// LoadedAt returns when the catalog was last (re)loaded from the source.
func (s *Store) LoadedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadedAt
}

// RowCount returns the number of papers currently in the catalog.
func (s *Store) RowCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rowCount
}

// Load (re)reads the parquet into the in-memory table, replacing any
// existing contents. Concurrent queries see either the old or new
// state atomically (DuckDB's CREATE OR REPLACE TABLE is transactional).
func (s *Store) Load(ctx context.Context) error {
	// CREATE OR REPLACE TABLE … AS SELECT * FROM parquet — DuckDB
	// reads the parquet (locally or via httpfs) and materializes it
	// into an in-memory column store. Subsequent queries hit RAM.
	stmt := fmt.Sprintf(`CREATE OR REPLACE TABLE papers AS SELECT * FROM read_parquet(%s)`, s.source)
	if _, err := s.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("load parquet from %s: %w", s.source, err)
	}

	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM papers`).Scan(&count); err != nil {
		return fmt.Errorf("count rows: %w", err)
	}

	s.mu.Lock()
	s.loadedAt = time.Now()
	s.rowCount = count
	s.mu.Unlock()

	slog.Info("paperindex: catalog loaded",
		"source", s.source, "rows", count)
	return nil
}

// loadEmptyTable creates the papers table with the expected schema but
// zero rows, used as the fallback when the parquet doesn't exist yet.
// The schema MUST match what the bootstrap subcommand writes — keep
// these in sync, ideally by adding a schema-introspection test.
func (s *Store) loadEmptyTable(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE OR REPLACE TABLE papers (
			arxiv_id          VARCHAR,
			yymm              VARCHAR,
			has_pdf           BOOLEAN,
			has_md            BOOLEAN,
			has_json          BOOLEAN,
			image_count       INTEGER,
			pdf_size_bytes    BIGINT,
			md_size_bytes     BIGINT,
			json_size_bytes   BIGINT,
			pdf_uploaded_at   TIMESTAMP,
			md_processed_at   TIMESTAMP,
			json_uploaded_at  TIMESTAMP,
			pdf_etag          VARCHAR,
			md_etag           VARCHAR,
			json_etag         VARCHAR
		)`)
	return err
}

func (s *Store) refreshLoop() {
	defer s.wg.Done()
	t := time.NewTicker(s.cfg.RefreshInterval)
	defer t.Stop()

	for {
		select {
		case <-s.closeCh:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := s.Refresh(ctx); err != nil {
				slog.Warn("paperindex: refresh failed", "error", err)
			}
			cancel()
		}
	}
}

// Refresh attempts to re-Load the parquet if the local catalog is
// older than RefreshInterval. (We do an unconditional re-Load rather
// than a proper etag-changed check because the duckdb httpfs extension
// caches range reads aggressively — etag-equality detection adds
// complexity without much benefit at 60s cadence. If parquet sizes
// grow past ~50MB this becomes worth reconsidering.)
//
// Future optimization (post-Phase 2): track the bucket's parquet etag
// via a minio-go HEAD call and skip Load when unchanged. Out of scope
// for the initial read-only ship.
func (s *Store) Refresh(ctx context.Context) error {
	return s.Load(ctx)
}

// ----- Query API -----------------------------------------------------

// Stats returns aggregate counters across the whole catalog. Used by
// the /api/papers/needs-mineru and any /api/papers/stats endpoints.
type Stats struct {
	Total         int
	HasPDF        int
	HasMD         int
	HasJSON       int
	NeedsMineru   int // has_pdf AND NOT has_md
	NeedsJSON     int // has_pdf AND NOT has_json
	TotalImages   int
	LoadedAt      time.Time
}

// QueryStats returns the aggregate counters. Sub-millisecond for the
// catalog sizes we expect (~10⁵ papers).
func (s *Store) QueryStats(ctx context.Context) (Stats, error) {
	var st Stats
	row := s.db.QueryRowContext(ctx, `
		SELECT
			count(*),
			coalesce(sum(CASE WHEN has_pdf THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN has_md THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN has_json THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN has_pdf AND NOT has_md THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN has_pdf AND NOT has_json THEN 1 ELSE 0 END), 0),
			coalesce(sum(image_count), 0)
		FROM papers`)
	if err := row.Scan(&st.Total, &st.HasPDF, &st.HasMD, &st.HasJSON,
		&st.NeedsMineru, &st.NeedsJSON, &st.TotalImages); err != nil {
		return st, fmt.Errorf("paperindex: query stats: %w", err)
	}
	st.LoadedAt = s.LoadedAt()
	return st, nil
}

// NeedsMineruRow is one row in the "papers waiting for MinerU"
// projection. Mirrors the response shape historically returned by the
// needs-mineru endpoint so the handler can JSON-encode without
// reshuffling fields.
type NeedsMineruRow struct {
	ArxivID       string
	YYMM          string
	PDFKey        string // pdf/<yymm>/<arxiv_id>.pdf
	PDFSizeBytes  int64
	PDFUploadedAt sql.NullTime
}

// NeedsMineru returns up to `limit` papers that have a PDF but no
// markdown. Results sorted by pdf_uploaded_at DESC (newest first)
// so MinerU schedulers naturally pick up recent uploads.
//
// limit is clamped server-side to [1, 100] by the handler; this
// method assumes the caller already did that.
func (s *Store) NeedsMineru(ctx context.Context, limit int) ([]NeedsMineruRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT arxiv_id, yymm,
		       'pdf/' || yymm || '/' || arxiv_id || '.pdf' AS pdf_key,
		       pdf_size_bytes,
		       pdf_uploaded_at
		FROM papers
		WHERE has_pdf AND NOT has_md
		ORDER BY pdf_uploaded_at DESC NULLS LAST
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("paperindex: needs-mineru query: %w", err)
	}
	defer rows.Close()

	var out []NeedsMineruRow
	for rows.Next() {
		var r NeedsMineruRow
		if err := rows.Scan(&r.ArxivID, &r.YYMM, &r.PDFKey, &r.PDFSizeBytes, &r.PDFUploadedAt); err != nil {
			return nil, fmt.Errorf("paperindex: scan needs-mineru row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ----- helpers -------------------------------------------------------

// setupHTTPFS installs the httpfs extension and creates the S3 SECRET
// scoped to the configured bucket. Idempotent across reconnects —
// secrets persist per-DuckDB-instance.
//
// SCOPE 's3://<bucket>' is the defense-in-depth bit: DuckDB will only
// apply this secret to queries whose URL starts with that prefix.
// The real bucket isolation comes from the svcacct policy at the
// RustFS server side, but client-side scoping prevents accidental
// credential leakage if code ever queries an unrelated S3 URL.
func setupHTTPFS(ctx context.Context, db *sql.DB, cfg Config) error {
	if _, err := db.ExecContext(ctx, `INSTALL httpfs; LOAD httpfs;`); err != nil {
		return fmt.Errorf("install/load httpfs: %w", err)
	}

	// Endpoint must be passed WITHOUT the scheme — DuckDB s3 secret
	// expects just host:port, and the USE_SSL flag controls whether
	// it's https. Strip http:// or https:// prefix.
	host := cfg.S3Endpoint
	useSSL := "false"
	switch {
	case strings.HasPrefix(host, "https://"):
		host = strings.TrimPrefix(host, "https://")
		useSSL = "true"
	case strings.HasPrefix(host, "http://"):
		host = strings.TrimPrefix(host, "http://")
	}

	// DuckDB's CREATE SECRET takes literal SQL strings, not bound
	// parameters. We quote all user-supplied values via the
	// sqlQuoteLiteral helper to defang any embedded apostrophes
	// (unlikely in S3 keys but cheap insurance).
	stmt := fmt.Sprintf(`CREATE OR REPLACE SECRET qatlas_rustfs (
		TYPE S3,
		SCOPE %s,
		ENDPOINT %s,
		KEY_ID %s,
		SECRET %s,
		USE_SSL %s,
		URL_STYLE 'path',
		REGION 'us-east-1'
	)`,
		sqlQuoteLiteral(fmt.Sprintf("s3://%s", cfg.Bucket)),
		sqlQuoteLiteral(host),
		sqlQuoteLiteral(cfg.AccessKeyID),
		sqlQuoteLiteral(cfg.SecretAccessKey),
		useSSL,
	)
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("create s3 secret: %w", err)
	}
	return nil
}

// sqlQuoteLiteral wraps a string in SQL single quotes with embedded
// apostrophes doubled, suitable for DuckDB CREATE SECRET clauses
// where bound parameters aren't accepted.
func sqlQuoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
