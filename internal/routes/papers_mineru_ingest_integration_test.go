//go:build integration

package routes

// Real-PostgreSQL integration for the new-format MinerU ingest: the
// real registry.Store (migrations applied incl. 00009) backing the
// mineruIngestCatalog interface. Verifies source reuse, the is_current
// flip, and the tier column round-trip end to end — plus the 00009 FK
// protecting the resulting anchor.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/testutil"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pocketbase/pocketbase/core"
)

func ingestPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	if !testutil.IntegrationEnabled {
		t.Skip("requires -tags integration")
	}
	dsn := os.Getenv("QATLAS_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("QATLAS_TEST_PG_DSN unset; skipping")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60e9)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := registry.Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return pool, ctx
}

func TestIntegrationIngestNewFormatRealRegistry(t *testing.T) {
	pool, ctx := ingestPool(t)
	s := registry.NewStore(pool)
	store, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("local store: %v", err)
	}

	const arxiv = "2401.91999"
	paperID, _, err := s.ResolveOrMint(ctx, registry.PaperRef{ArxivID: arxiv + "v1"})
	if err != nil {
		t.Fatalf("ResolveOrMint: %v", err)
	}
	t.Cleanup(func() {
		// The RESTRICT-probe discussion below pins the revisions, so
		// clear it before the cascade delete (otherwise the cleanup
		// itself is RESTRICTed and the fixture leaks between runs).
		_, _ = pool.Exec(context.Background(), `DELETE FROM comment_discussions WHERE paper_id = $1`, paperID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM papers WHERE paper_id = $1`, paperID)
	})

	pdfSha := strings.Repeat("9a", 32)
	run := func(tier, text string) map[string]any {
		t.Helper()
		middle := map[string]any{
			"schema": "docvortex.middle", "schema_version": "2.0",
			"blocks": []map[string]any{{"page_idx": 0, "index": 1, "type": "text", "content": text}},
		}
		mb, _ := json.Marshal(middle)
		var buf bytes.Buffer
		zw := newZipWriter(t, &buf)
		zw.write("out/middle_json.json", mb)
		zw.write("out/markdown.md", []byte("# "+text+"\n"))
		if tier != "" {
			zw.write("out/metadata.json", []byte(`{"tier":"`+tier+`"}`))
		}
		zw.close()

		req := httptest.NewRequest(http.MethodPost, "/api/papers/"+arxiv+"v1/upload-mineru", nil)
		rec := httptest.NewRecorder()
		re := &core.RequestEvent{}
		re.Request = req
		re.Response = rec
		if err := ingestMinerUNewFormat(re, store, s, arxiv+"v1", buf.Bytes(),
			pdfSha, "", "integ", "synthetic"); err != nil {
			t.Fatalf("ingest: %v", err)
		}
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return body
	}

	body1 := run("lite", "FIRST PARSE")
	body2 := run("", "SECOND PARSE") // tier falls back to standard

	// Source reused (same PDF sha), not re-minted.
	if body1["source_id"] != body2["source_id"] {
		t.Errorf("source ids differ: %v vs %v", body1["source_id"], body2["source_id"])
	}
	if body1["source_minted"] != true || body2["source_minted"] != false {
		t.Errorf("minted flags: %v/%v", body1["source_minted"], body2["source_minted"])
	}

	// Registry rows read back through the store.
	revs, err := s.ListParseRevisions(ctx, paperID)
	if err != nil {
		t.Fatalf("ListParseRevisions: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("revisions = %d, want 2", len(revs))
	}
	if !revs[1].IsCurrent || revs[0].IsCurrent {
		t.Error("is_current did not flip to the newest revision in the real store")
	}
	if revs[0].Tier != "lite" || revs[1].Tier != "standard" {
		t.Errorf("tiers = %q/%q, want lite/standard", revs[0].Tier, revs[1].Tier)
	}
	if revs[0].SourceID != revs[1].SourceID || revs[0].SourceID == "" {
		t.Errorf("source ids on revisions: %q/%q", revs[0].SourceID, revs[1].SourceID)
	}

	// The FK from 00009 holds: a discussion pinned to revision 1 now
	// RESTRICTs its deletion (anchor integrity through the real DB).
	if _, err := pool.Exec(ctx, `
		INSERT INTO comment_discussions
		       (discussion_id, paper_id, parse_revision, page_idx, block_index, body, created_by)
		VALUES ($1, $2, $3, 0, 1, 'pinned', 'u_integ')`,
		"cd_"+strings.ToLower(strings.Repeat("5", 26)), paperID, revs[0].RevisionID); err != nil {
		t.Fatalf("insert discussion: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM parse_revisions WHERE revision_id = $1`, revs[0].RevisionID); err == nil {
		t.Error("DELETE of a discussion-pinned revision succeeded; RESTRICT broken")
	}
}
