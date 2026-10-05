package routes

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This traverses actual HTTP/PDF title verification, immutable local storage,
// PostgreSQL registration/migration, and the existing source reader. Only the
// outbound fixture transport is substituted; no production destination is used.
func TestIntegrationExternalSourceHTTPToPostgres(t *testing.T) {
	if !testutil.IntegrationEnabled {
		t.Skip("requires -tags integration and disposable PostgreSQL")
	}
	dsn := os.Getenv("QATLAS_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("QATLAS_TEST_PG_DSN unset")
	}
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := registry.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	catalog := registry.NewStore(pool)
	store, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pdf := externalFixturePDF(externalFixtureTitle, "Fixture Author", "Abstract", "Pinned original bytes")
	fetcher := externalLocalFetcher(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { _, _ = w.Write(pdf) }))
	sourceURL := "https://fixture.example.org/" + registry.NewSourceID() + ".pdf"
	rec, body := callExternalRegistration(t, catalog, store, fetcher, externalRequestJSON(sourceURL))
	if rec.Code != 200 || body["created"] != true {
		t.Fatalf("register=%d %v", rec.Code, body)
	}
	paperID := body["paper_id"].(string)
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM papers WHERE paper_id=$1`, paperID) }()
	paper, found, err := catalog.Get(ctx, paperID)
	if err != nil || !found || paper.PaperRef != "source_url:"+sourceURL || paper.DOI != "" || paper.ArxivID != "" || paper.OpenAlexID != "" {
		t.Fatalf("paper=%+v %v %v", paper, found, err)
	}
	sources, err := catalog.ListPaperSources(ctx, paperID)
	if err != nil || len(sources) != 1 || sources[0].SourceURL != sourceURL || sources[0].RetrievedAt.IsZero() {
		t.Fatalf("sources=%+v %v", sources, err)
	}
	endpoint := body["source"].(map[string]any)["pdf_endpoint"].(string)
	original, _ := callBlockOriginals(t, catalog, store, endpoint, nil)
	if original.Code != 200 || !bytes.Equal(original.Body.Bytes(), pdf) {
		t.Fatalf("original=%d %s", original.Code, original.Body.String())
	}
	pdf = externalFixturePDF(externalFixtureTitle, "Fixture Author", "Abstract", "Second immutable revision")
	rec, second := callExternalRegistration(t, catalog, store, fetcher, externalRequestJSON(sourceURL))
	if rec.Code != 200 || second["created"] != false || second["paper_id"] != paperID {
		t.Fatalf("revision=%d %v", rec.Code, second)
	}
	old, _ := callBlockOriginals(t, catalog, store, endpoint, map[string]string{"Range": "bytes=0-31"})
	if old.Code != http.StatusPartialContent || !bytes.Equal(old.Body.Bytes(), original.Body.Bytes()[:32]) {
		t.Fatal("old pinned PostgreSQL original overwritten")
	}
}
