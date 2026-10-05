package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/testutil"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const archiveFixtureTitle = "Verified Quantum Algorithms for Distributed Archive"

// A real PDF with front matter, text objects and valid xref offsets; padding
// comments retain the ordinary archive's minimum PDF-size boundary.
func archiveFixturePDF(title string) []byte {
	stream := "BT /F1 12 Tf 40 780 Td 20 TL\n(" + title + ") Tj T*\n(Alice Example) Tj T*\n(Abstract) Tj T*\nET\n"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [4 0 R] /Count 1 >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 842] /Resources << /Font << /F1 3 0 R >> >> /Contents 5 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream),
	}
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n" + strings.Repeat("% harmless test comment\n", 1000))
	offsets := []int{0}
	for i, object := range objects {
		offsets = append(offsets, pdf.Len())
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return pdf.Bytes()
}

// This tests the REAL archive callback against all registry migrations and the
// filesystem store, complementing runner/fleet tests with synthetic callbacks.
func TestDownloadArchiveRealRegistryAndAdmission(t *testing.T) {
	if !testutil.IntegrationEnabled {
		t.Skip("requires -tags integration and a disposable PostgreSQL target")
	}
	dsn := os.Getenv("TEST_DOWNLOADFLEET_DATABASE_URL")
	if dsn == "" {
		t.Skip("disposable PostgreSQL required")
	}
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Fatal("real published archive integration requires pdftotext")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var entropy [8]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		t.Fatal(err)
	}
	schema := "archive_test_" + hex.EncodeToString(entropy[:])
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = registry.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	reg := registry.NewStore(pool)
	store, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := downloader.New(reg, store, nil, nil, downloader.Config{Journal: reg})
	defer d.Shutdown(context.Background())
	ref := registry.PaperRef{DOI: "10.1000/archive-full", Title: archiveFixtureTitle}
	paperID, _, err := reg.ResolveOrMint(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	admission, err := reg.SaveDownloadRequest(ctx, paperID, ref.DOI, "doi", ref)
	if err != nil {
		t.Fatal(err)
	}
	ctx = downloader.WithAdmissionID(ctx, admission)
	pdf := archiveFixturePDF(archiveFixtureTitle)
	digest := sha256.Sum256(pdf)
	sha := hex.EncodeToString(digest[:])
	outcome := func() *downloader.FetchOutcome {
		return &downloader.FetchOutcome{DOI: ref.DOI, Strategy: "worker:test:browser", URL: "https://publisher.example/paper.pdf", Result: &downloader.FetchResult{Body: bytes.NewReader(pdf), Size: int64(len(pdf)), Sha256: sha}}
	}
	// Older workers may send any claimed title/hash. The central archive must
	// reject wrong front matter before writing objects or registering assets.
	wrongTitle := "An Unrelated Classical Algorithm for Searching Ordered Lists"
	wrongPDF := archiveFixturePDF(wrongTitle)
	wrong := outcome()
	wrong.PublishedTitle = wrongTitle
	wrong.Result = &downloader.FetchResult{Body: bytes.NewReader(wrongPDF), Size: int64(len(wrongPDF)), Sha256: sha}
	if err = d.ArchiveRemote(ctx, ref, wrong); !errors.Is(err, downloader.ErrPDFIdentityUnproven) {
		t.Fatalf("forged worker provenance accepted: %v", err)
	}
	if _, exists, err := store.Stat(ctx, paperassets.DOIAssetKey("pdf", ref.DOI)); err != nil || exists {
		t.Fatalf("unproven archive wrote PDF: exists=%v err=%v", exists, err)
	}
	if err = d.ArchiveRemote(ctx, ref, outcome()); err != nil {
		t.Fatal(err)
	}
	// Replaying a lost post-commit response is idempotent, including create-only
	// object-store conflicts on a backend that does not support source metadata.
	if err = d.ArchiveRemote(ctx, ref, outcome()); err != nil {
		t.Fatal(err)
	}
	info, exists, err := store.Stat(ctx, paperassets.DOIAssetKey("pdf", ref.DOI))
	if err != nil || !exists || info.Size != int64(len(pdf)) {
		t.Fatalf("stored info=%+v exists=%v err=%v", info, exists, err)
	}
	detail, found, err := reg.GetWithAssets(ctx, paperID)
	if err != nil || !found || len(detail.Assets) == 0 {
		t.Fatalf("registry detail=%+v found=%v err=%v", detail, found, err)
	}
	var state string
	if err = pool.QueryRow(ctx, "SELECT state FROM downloader_requests WHERE request_id=$1", admission).Scan(&state); err != nil || state != "done" {
		t.Fatalf("admission state=%s err=%v", state, err)
	}
	newer, err := reg.SaveDownloadRequest(ctx, paperID, ref.DOI, "doi", ref)
	if err != nil || newer == admission {
		t.Fatal("explicit retry must mint a new generation")
	}
	if err = reg.FinishDownloadRequest(ctx, paperID, admission, "failed", "late old waiter"); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "SELECT state FROM downloader_requests WHERE request_id=$1", newer).Scan(&state); err != nil || state != "queued" {
		t.Fatalf("late generation overwrote new admission: %s %v", state, err)
	}
}
