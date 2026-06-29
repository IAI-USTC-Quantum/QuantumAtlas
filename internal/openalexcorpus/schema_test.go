package openalexcorpus

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// joinSchema returns all schema DDL as one string for substring assertions.
func joinSchema() string { return strings.Join(schemaStatements, "\n") }

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

// TestSchemaCitationTable guards work_referenced: PK (work_id,
// referenced_id) forward index + reverse index, citation source-of-truth
// (local resolution, ADR 0006).
func TestSchemaCitationTable(t *testing.T) {
	s := joinSchema()
	if !strings.Contains(s, "CREATE TABLE IF NOT EXISTS work_referenced") {
		t.Error("schema must create work_referenced (citation edges)")
	}
	if !strings.Contains(s, "PRIMARY KEY (work_id, referenced_id)") {
		t.Error("work_referenced must PK (work_id, referenced_id) so the forward edge is unique + indexed")
	}
	if !strings.Contains(s, "work_referenced_target") {
		t.Error("work_referenced must have a reverse index on referenced_id (who cites W)")
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
	for _, stmt := range schemaStatements {
		if !strings.Contains(stmt, "IF NOT EXISTS") {
			t.Errorf("non-idempotent schema statement (missing IF NOT EXISTS):\n%s", stmt)
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
