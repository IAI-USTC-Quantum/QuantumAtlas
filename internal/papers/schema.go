// Package papers is the Neo4j-backed paper catalog that replaces the
// v0.6.0 parquet+DuckDB paperindex and the file-based mineruclaim. It
// owns the :PaperWork node layer (labels prefixed "Paper", relationships
// prefixed "PAPER_" per the cross-layer naming convention) and exposes
// the read/write helpers the /api/papers handlers need:
//
//   - QueryStats / NeedsMineru   (dashboards + mineru queue)
//   - UpsertPDF / UpsertMD / ...  (upload write-through, create-if-missing)
//   - Claim / ReleaseClaim / GC   (atomic MinerU leases via MERGE/SET)
//   - SyncFromStore               (periodic reconcile + disaster rebuild)
//
// Every method degrades gracefully when Neo4j is unreachable: writes
// return ErrCatalogUnavailable (handlers still 201 the S3 write and set
// X-Catalog-Sync: deferred), reads report availability=false. The
// connection is lazy + backoff-gated so a down Neo4j doesn't add latency
// to every request.
package papers

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/neo4j"
)

// ErrCatalogUnavailable signals that the Neo4j catalog could not be
// reached for this operation. Upload handlers treat it as non-fatal
// (S3 write already succeeded) and emit X-Catalog-Sync: deferred.
var ErrCatalogUnavailable = errors.New("papers: catalog backend unavailable")

// connectBackoff is the minimum gap between Bolt (re)connect attempts so
// a down Neo4j doesn't cost every request a full dial timeout.
const connectBackoff = 10 * time.Second

// connectTimeout caps a single reconnect attempt. Kept short so a hard-
// down Neo4j adds at most this to the first request after the backoff
// window, then fails fast for the rest of the window.
const connectTimeout = 2 * time.Second

// Store is the Neo4j-backed catalog. Construct with NewStore; nc may be
// nil (local dev without Neo4j) in which case every operation reports
// the catalog as unavailable.
type Store struct {
	nc *neo4j.Client

	mu          sync.Mutex
	lastAttempt time.Time
}

// NewStore wraps a neo4j.Client (which may be nil / unconnected). The
// caller owns the client's lifecycle (Close at shutdown).
func NewStore(nc *neo4j.Client) *Store {
	return &Store{nc: nc}
}

// ensure returns true when the catalog is connected and ready. It lazily
// (re)connects, gated by connectBackoff so a persistently-down Neo4j is
// cheap to probe.
func (s *Store) ensure(ctx context.Context) bool {
	if s.nc == nil {
		return false
	}
	if s.nc.Connected() {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nc.Connected() {
		return true
	}
	if time.Since(s.lastAttempt) < connectBackoff {
		return false
	}
	s.lastAttempt = time.Now()
	cctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := s.nc.Connect(cctx); err != nil {
		slog.Debug("papers: neo4j reconnect failed", "error", err)
		return false
	}
	slog.Info("papers: neo4j catalog connected")
	return true
}

// Available reports whether the catalog backend is usable right now
// (attempting a lazy reconnect). Cheap when already connected.
func (s *Store) Available(ctx context.Context) bool {
	return s.ensure(ctx)
}

// Configured reports whether a Neo4j client was provided at all (vs.
// local-dev no-Neo4j). Distinct from Available, which also checks live
// connectivity.
func (s *Store) Configured() bool {
	return s.nc != nil
}

// schemaStatements are the constraints + indexes applied at startup.
// All are IF NOT EXISTS so repeated boots (and both edges racing) are
// idempotent. Mirrors v0.7.0 plan §3.1.
var schemaStatements = []string{
	// PaperWork — business core keyed by arxiv_id.
	`CREATE CONSTRAINT paper_arxiv_id_unique IF NOT EXISTS
	   FOR (p:PaperWork) REQUIRE p.arxiv_id IS UNIQUE`,
	`CREATE INDEX paper_openalex_id IF NOT EXISTS
	   FOR (p:PaperWork) ON (p.openalex_id)`,
	`CREATE INDEX paper_doi IF NOT EXISTS
	   FOR (p:PaperWork) ON (p.doi)`,
	`CREATE INDEX paper_has_pdf_has_md IF NOT EXISTS
	   FOR (p:PaperWork) ON (p.has_pdf, p.has_md)`,
	`CREATE INDEX paper_claim_expires IF NOT EXISTS
	   FOR (p:PaperWork) ON (p.claim_expires_at)`,
	`CREATE INDEX paper_yymm IF NOT EXISTS
	   FOR (p:PaperWork) ON (p.yymm)`,
	// OpenAlex entities keyed by their native W/A/S/T id.
	`CREATE CONSTRAINT author_openalex_unique IF NOT EXISTS
	   FOR (a:PaperAuthor) REQUIRE a.openalex_id IS UNIQUE`,
	`CREATE INDEX author_orcid IF NOT EXISTS
	   FOR (a:PaperAuthor) ON (a.orcid)`,
	`CREATE CONSTRAINT source_openalex_unique IF NOT EXISTS
	   FOR (s:PaperSource) REQUIRE s.openalex_id IS UNIQUE`,
	`CREATE INDEX source_issn IF NOT EXISTS
	   FOR (s:PaperSource) ON (s.issn)`,
	`CREATE CONSTRAINT topic_openalex_unique IF NOT EXISTS
	   FOR (t:PaperTopic) REQUIRE t.openalex_id IS UNIQUE`,
	`CREATE CONSTRAINT institution_openalex_unique IF NOT EXISTS
	   FOR (i:PaperInstitution) REQUIRE i.openalex_id IS UNIQUE`,
	`CREATE INDEX institution_ror IF NOT EXISTS
	   FOR (i:PaperInstitution) ON (i.ror)`,
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
		if _, err := s.nc.ExecuteWrite(ctx, stmt, nil); err != nil {
			return err
		}
	}
	return nil
}
