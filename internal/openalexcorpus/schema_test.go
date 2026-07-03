package openalexcorpus

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// joinSchema returns all schema DDL (the base statements plus the
// concurrently-built index DDL) as one string for substring assertions.
func joinSchema() string {
	parts := append([]string{}, baseSchemaStatements...)
	for _, idx := range corpusIndexes {
		parts = append(parts, idx.ddl)
	}
	return strings.Join(parts, "\n")
}

// TestSchemaCoreTable guards the openalex_works shape: jsonb record +
// the STORED generated hot columns (ADR 0006: derive, never rewrite).
func TestSchemaCoreTable(t *testing.T) {
	s := joinSchema()
	for _, must := range []string{
		"CREATE TABLE IF NOT EXISTS openalex_works",
		"record jsonb NOT NULL",
		"openalex_id text PRIMARY KEY",
		"GENERATED ALWAYS AS",
		"STORED",
		"publication_year",
		"work_type",
		"display_name",
		"language",
		"primary_topic_id",
		"search_text",
		"cited_by_count",
	} {
		if !strings.Contains(s, must) {
			t.Errorf("schema missing %q (openalex_works core / generated columns)", must)
		}
	}
}

// TestSchemaGeneratedNotRewritten ensures the hot fields are GENERATED
// from record (not plain columns the ingester would have to populate by
// rewriting the record) — the ADR 0006 "only filter, never modify" rule.
func TestSchemaGeneratedColumnsDeriveFromRecord(t *testing.T) {
	s := joinSchema()
	for _, expr := range []string{
		"record->>'publication_year'",
		"record->>'type'",
		"record->>'display_name'",
		"record->>'language'",
		"record #>> '{primary_topic,id}'",
		"record->>'cited_by_count'",
		"record->>'doi'",
	} {
		if !strings.Contains(s, expr) {
			t.Errorf("generated column should derive from %q", expr)
		}
	}
}

// TestSchemaGINIndex guards the jsonb containment index (the corpus query
// shape, record @> '{...}').
func TestSchemaGINIndex(t *testing.T) {
	s := joinSchema()
	if !strings.Contains(s, "USING gin (record jsonb_path_ops)") {
		t.Error("schema must create a GIN jsonb_path_ops index on record for @> containment queries")
	}
	if !strings.Contains(s, "openalex_works_search") || !strings.Contains(s, "USING gin (search_text)") {
		t.Error("schema must create a GIN index on search_text for local title/display_name search")
	}
}

// TestSchemaArxivJoinIndex guards the cross-table join key to paper_works.
func TestSchemaArxivJoinIndex(t *testing.T) {
	s := joinSchema()
	if !strings.Contains(s, "openalex_works_arxiv") || !strings.Contains(s, "(arxiv_id) WHERE arxiv_id IS NOT NULL") {
		t.Error("schema must index arxiv_id (partial, NOT NULL) — the join key to paper_works")
	}
}

// TestSchemaInlineCitations guards the inline citation model (ADR 0010):
// referenced_works is a STORED generated column of bare "W…" ids derived
// via the IMMUTABLE strip_openalex_prefix() function and indexed by a
// default-ops GIN for the `?` reverse-lookup — with NO separate
// work_referenced edge table.
func TestSchemaInlineCitations(t *testing.T) {
	s := joinSchema()
	for _, must := range []string{
		"CREATE OR REPLACE FUNCTION strip_openalex_prefix",
		"IMMUTABLE",
		"openalex_referenced_work_ids jsonb GENERATED ALWAYS AS (strip_openalex_prefix(record->'referenced_works')) STORED",
		"openalex_works_referenced_gin",
		"USING gin (openalex_referenced_work_ids)",
	} {
		if !strings.Contains(s, must) {
			t.Errorf("inline-citation schema missing %q", must)
		}
	}
	if strings.Contains(s, "work_referenced") {
		t.Error("schema must NOT create the work_referenced edge table (citations are inline, ADR 0010)")
	}
}

// TestSchemaSyncStateAndAudit guards the single-row refresh watermark and
// the append-only API-comparison audit table (ADR 0010).
func TestSchemaSyncStateAndAudit(t *testing.T) {
	s := joinSchema()
	for _, must := range []string{
		"CREATE TABLE IF NOT EXISTS openalex_sync_state",
		"id boolean PRIMARY KEY DEFAULT true CHECK (id)",
		"CREATE TABLE IF NOT EXISTS openalex_audit",
		"verdict text CHECK (verdict IN ('ok','field_mismatch','cited_by_regressed','stale_drift'))",
		"openalex_audit_run",
		"openalex_audit_field",
	} {
		if !strings.Contains(s, must) {
			t.Errorf("audit/sync schema missing %q", must)
		}
	}
}

// TestSchemaEmbeddingsTable guards the domain-subset vector table at the
// BGE-M3 dimension.
func TestSchemaEmbeddingsTable(t *testing.T) {
	s := joinSchema()
	if !strings.Contains(s, "CREATE TABLE IF NOT EXISTS work_embeddings") {
		t.Error("schema must create work_embeddings (domain-subset vectors)")
	}
	if !strings.Contains(s, "vector(1024)") {
		t.Errorf("work_embeddings must use vector(1024) = BGE-M3 dim (EmbeddingDim=%d)", EmbeddingDim)
	}
	if EmbeddingDim != 1024 {
		t.Errorf("EmbeddingDim = %d, want 1024 (BGE-M3 dense)", EmbeddingDim)
	}
}

// TestSchemaIdempotent guards that every DDL statement is IF NOT EXISTS so
// repeated bootstraps + both edges racing are safe.
func TestSchemaIdempotent(t *testing.T) {
	stmts := append([]string{}, baseSchemaStatements...)
	for _, idx := range corpusIndexes {
		stmts = append(stmts, idx.ddl)
	}
	for _, stmt := range stmts {
		if !strings.Contains(stmt, "IF NOT EXISTS") && !strings.Contains(stmt, "OR REPLACE") {
			t.Errorf("non-idempotent schema statement (missing IF NOT EXISTS / OR REPLACE):\n%s", stmt)
		}
	}
}

// TestSchemaHeavyIndexesAreConcurrentAndSeparate guards ADR 0013: the heavy
// openalex_works indexes are built CONCURRENTLY and live OUTSIDE the
// boot-critical base schema, so boot never takes a table-level SHARE lock on
// the 353 GB corpus. The base statements must not CREATE INDEX on
// openalex_works.
func TestSchemaHeavyIndexesAreConcurrentAndSeparate(t *testing.T) {
	for _, stmt := range baseSchemaStatements {
		if strings.Contains(stmt, "CREATE INDEX") && strings.Contains(stmt, "openalex_works ") {
			t.Errorf("base schema must not build an openalex_works index (heavy indexes belong in corpusIndexes, built CONCURRENTLY):\n%s", stmt)
		}
	}
	if len(corpusIndexes) == 0 {
		t.Fatal("corpusIndexes is empty; expected the heavy openalex_works indexes")
	}
	for _, idx := range corpusIndexes {
		if !strings.Contains(idx.ddl, "CONCURRENTLY") {
			t.Errorf("corpus index %s must be built CONCURRENTLY:\n%s", idx.name, idx.ddl)
		}
		if !strings.Contains(idx.ddl, "IF NOT EXISTS") {
			t.Errorf("corpus index %s must be IF NOT EXISTS (idempotent):\n%s", idx.name, idx.ddl)
		}
		if !strings.Contains(idx.ddl, idx.name) {
			t.Errorf("corpus index ddl must create the named index %q:\n%s", idx.name, idx.ddl)
		}
	}
}

// TestNilPoolDegradesGracefully guards the optional-pool contract: a Store
// with no pool reports unavailable rather than panicking.
func TestNilPoolDegradesGracefully(t *testing.T) {
	s := NewStore(nil)
	if s.Configured() {
		t.Error("NewStore(nil).Configured() must be false")
	}
	if s.Available(context.Background()) {
		t.Error("NewStore(nil).Available() must be false")
	}
	if err := s.EnsureSchema(context.Background()); !errors.Is(err, ErrCorpusUnavailable) {
		t.Errorf("EnsureSchema on nil pool = %v, want ErrCorpusUnavailable", err)
	}
	if err := s.EnsureIndexes(context.Background()); !errors.Is(err, ErrCorpusUnavailable) {
		t.Errorf("EnsureIndexes on nil pool = %v, want ErrCorpusUnavailable", err)
	}
	if _, err := s.CountWorks(context.Background()); !errors.Is(err, ErrCorpusUnavailable) {
		t.Errorf("CountWorks on nil pool = %v, want ErrCorpusUnavailable", err)
	}
	if _, err := s.QueryStats(context.Background()); !errors.Is(err, ErrCorpusUnavailable) {
		t.Errorf("QueryStats on nil pool = %v, want ErrCorpusUnavailable", err)
	}
	if _, err := s.UpsertWorks(context.Background(), nil, ""); !errors.Is(err, ErrCorpusUnavailable) {
		t.Errorf("UpsertWorks on nil pool = %v, want ErrCorpusUnavailable", err)
	}
}
