package openalexcorpus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestIntegrationUpsert exercises the real upsert path (pgx array encoding,
// the text[]::jsonb[] cast, generated columns, NULL arxiv_id, citation
// edges, stats) against a live PostgreSQL with pgvector. It is skipped
// unless QATLAS_TEST_PG_DSN points at a disposable database, so CI and the
// default `go test` stay hermetic.
//
// It writes only rows under a unique "WITEST_<ts>" id prefix and deletes
// them at the end, so it is safe to run against the shared central corpus.
func TestIntegrationUpsert(t *testing.T) {
	dsn := os.Getenv("QATLAS_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("QATLAS_TEST_PG_DSN unset; skipping live-PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	s := NewStore(pool)

	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	prefix := fmt.Sprintf("WITEST_%d_", time.Now().UnixNano())
	id1 := prefix + "1"
	id2 := prefix + "2"
	// Clean up whatever we insert, even on failure.
	defer func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM openalex_works WHERE openalex_id LIKE $1`, prefix+"%")
	}()

	mk := func(id, body string) RawWork {
		raw := json.RawMessage(body)
		var m openalex.Work
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		m.ID = "https://openalex.org/" + id // force a deterministic, unique id
		return RawWork{Record: raw, Meta: m}
	}

	// id1: has arxiv + cites id2; id2: no arxiv, no refs.
	works := []RawWork{
		mk(id1, fmt.Sprintf(`{
			"id":"https://openalex.org/%s",
			"display_name":"Quantum Error Correction Test Work",
			"type":"article","publication_year":2003,"cited_by_count":7,
			"language":"en",
			"primary_topic":{"id":"https://openalex.org/T999","display_name":"Quantum information"},
			"doi":"https://doi.org/10.test/1","is_retracted":false,
			"locations":[{"landing_page_url":"https://arxiv.org/abs/2401.99999v1"}],
			"referenced_works":["https://openalex.org/%s"]
		}`, id1, id2)),
		mk(id2, fmt.Sprintf(`{
			"id":"https://openalex.org/%s",
			"display_name":"Classical Control Test Work",
			"type":"preprint","publication_year":2021,"cited_by_count":0,
			"language":"en",
			"is_retracted":false,"locations":[],"referenced_works":[]
		}`, id2)),
	}

	n, err := s.UpsertWorks(ctx, works, "2016-06-24")
	if err != nil {
		t.Fatalf("UpsertWorks: %v", err)
	}
	if n != 2 {
		t.Fatalf("UpsertWorks n = %d, want 2", n)
	}

	// Idempotent re-run: same counts, no duplicate-key error.
	if _, err := s.UpsertWorks(ctx, works, "2016-06-24"); err != nil {
		t.Fatalf("UpsertWorks (re-run): %v", err)
	}

	// Verify generated columns + arxiv NULLability landed correctly.
	var (
		pubYear  int
		workType string
		citedBy  int
		doi      *string
		arxiv    *string
		updated  *time.Time
	)
	err = pool.QueryRow(ctx, `
		SELECT publication_year, work_type, cited_by_count, doi, arxiv_id, updated_date
		FROM openalex_works WHERE openalex_id = $1`, id1).
		Scan(&pubYear, &workType, &citedBy, &doi, &arxiv, &updated)
	if err != nil {
		t.Fatalf("read id1: %v", err)
	}
	if pubYear != 2003 || workType != "article" || citedBy != 7 {
		t.Errorf("id1 generated cols = (%d,%q,%d), want (2003,article,7)", pubYear, workType, citedBy)
	}
	if doi == nil || *doi != "https://doi.org/10.test/1" {
		t.Errorf("id1 doi = %v, want https://doi.org/10.test/1", doi)
	}
	if arxiv == nil || *arxiv != "2401.99999" {
		t.Errorf("id1 arxiv_id = %v, want 2401.99999", arxiv)
	}
	if updated == nil || updated.Format("2006-01-02") != "2016-06-24" {
		t.Errorf("id1 updated_date = %v, want 2016-06-24", updated)
	}

	// id2 must have NULL arxiv_id (the []*string NULL path).
	var arxiv2 *string
	if err := pool.QueryRow(ctx, `SELECT arxiv_id FROM openalex_works WHERE openalex_id=$1`, id2).Scan(&arxiv2); err != nil {
		t.Fatalf("read id2: %v", err)
	}
	if arxiv2 != nil {
		t.Errorf("id2 arxiv_id = %v, want NULL", *arxiv2)
	}

	// Inline citation out-edge present, direction id1 -> id2: the generated
	// openalex_referenced_work_ids array on id1 contains id2 (bare id, URL
	// prefix stripped by strip_openalex_prefix).
	var citesID2 bool
	if err := pool.QueryRow(ctx, `SELECT openalex_referenced_work_ids ? $2 FROM openalex_works WHERE openalex_id=$1`, id1, id2).Scan(&citesID2); err != nil {
		t.Fatalf("read out-edge: %v", err)
	}
	if !citesID2 {
		t.Errorf("id1.openalex_referenced_work_ids should contain id2 (%s)", id2)
	}

	// GIN containment query works on the jsonb record.
	var articles int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM openalex_works WHERE openalex_id LIKE $1 AND record @> '{"type":"article"}'`, prefix+"%").Scan(&articles); err != nil {
		t.Fatalf("containment: %v", err)
	}
	if articles != 1 {
		t.Errorf("containment type=article count = %d, want 1", articles)
	}

	if raw, ok, err := s.GetWork(ctx, "https://openalex.org/"+id1); err != nil || !ok || !json.Valid(raw) {
		t.Fatalf("GetWork = valid:%v ok:%v err:%v", json.Valid(raw), ok, err)
	}
	if raw, ok, err := s.GetWorkByDOI(ctx, "10.TEST/1"); err != nil || !ok || !json.Valid(raw) {
		t.Fatalf("GetWorkByDOI = valid:%v ok:%v err:%v", json.Valid(raw), ok, err)
	}
	qr, err := s.QueryWorks(ctx, QueryOptions{
		Search: "quantum error",
		Filters: []Filter{
			{Key: "type", Value: "article"},
			{Key: "has_arxiv", Value: "true"},
			{Key: "cites", Value: id2},
			{Key: "primary_topic.id", Value: "https://openalex.org/T999"},
		},
		Sort:    "cited_by_count:desc",
		PerPage: 5,
		Count:   true,
	})
	if err != nil {
		t.Fatalf("QueryWorks: %v", err)
	}
	if len(qr.Results) != 1 {
		t.Fatalf("QueryWorks returned %d results, want 1", len(qr.Results))
	}
	if qr.Meta.Count == nil || *qr.Meta.Count != 1 {
		t.Fatalf("QueryWorks count = %v, want 1", qr.Meta.Count)
	}
}
