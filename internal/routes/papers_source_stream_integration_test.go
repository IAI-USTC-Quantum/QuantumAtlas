//go:build integration

package routes

// Real-S3 end-to-end for the streaming source-PDF endpoint: the full
// handler chain (paperSourcePDFHandler → serveSourcePDF →
// rangedReadSeeker → objstore.S3Store.GetRange → ranged GETs on the
// S3 wire) against a live S3-compatible backend.
//
// Env contract mirrors internal/objstore's S3 tests:
//
//	QATLAS_S3_TEST_ENDPOINT / QATLAS_S3_TEST_BUCKET /
//	QATLAS_S3_TEST_ACCESS_KEY_ID / QATLAS_S3_TEST_SECRET_ACCESS_KEY
//
// (During hardening this ran against RustFS — qatlas's production
// object store; see the note in internal/objstore/
// s3_range_integration_test.go for why MinIO is no longer pullable.)

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/testutil"
)

func requireRouteS3Store(t *testing.T) (objstore.Store, string) {
	t.Helper()
	if !testutil.IntegrationEnabled {
		t.Skip("requires -tags integration")
	}
	endpoint := os.Getenv("QATLAS_S3_TEST_ENDPOINT")
	bucket := os.Getenv("QATLAS_S3_TEST_BUCKET")
	key := os.Getenv("QATLAS_S3_TEST_ACCESS_KEY_ID")
	secret := os.Getenv("QATLAS_S3_TEST_SECRET_ACCESS_KEY")
	if endpoint == "" || bucket == "" || key == "" || secret == "" {
		t.Skip("S3 integration env not set; export QATLAS_S3_TEST_* to run")
	}
	s, err := objstore.NewS3Store(endpoint, bucket, key, secret)
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	var nonce [8]byte
	_, _ = rand.Read(nonce[:])
	prefix := fmt.Sprintf("go-test/routes-%s/", hex.EncodeToString(nonce[:]))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		objs, _ := s.ListPrefix(ctx, prefix, 0)
		for _, o := range objs {
			_ = s.Delete(ctx, o.Key)
		}
	})
	return s, prefix
}

// TestIntegrationSourcePDFStreamS3 drives the >4 MiB streaming path
// against the real backend: full GET must reproduce the 8 MiB sha;
// Range requests must answer 206 with byte-exact windows.
func TestIntegrationSourcePDFStreamS3(t *testing.T) {
	store, prefix := requireRouteS3Store(t)
	c, _ := newFakeBlockCatalog(t)

	body := fakeRoutePDF8MiB()
	sum := sha256.Sum256(body)
	key := prefix + "sources/8mb-fake.pdf"
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := store.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "application/pdf"); err != nil {
		t.Fatalf("put 8MiB: %v", err)
	}
	const srcID = "src_s3_stream"
	c.sources[fixturePaperID][srcID] = registry.PaperSource{
		SourceID: srcID, PaperID: fixturePaperID, Origin: "upload",
		Sha256: hex.EncodeToString(sum[:]), ObjstoreKey: key,
		SizeBytes: int64(len(body)),
	}

	// Full GET over the ranged path: sha must match the pinned bytes.
	rec := callSourcePDF(t, c, store, "/api/papers/"+fixturePaperID+"/sources/"+srcID+"/pdf", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("full GET status = %d", rec.Code)
	}
	got := sha256.Sum256(rec.Body.Bytes())
	if hex.EncodeToString(got[:]) != hex.EncodeToString(sum[:]) {
		t.Error("streamed full-GET sha mismatch against real S3 backend")
	}
	if rec.Body.Len() != len(body) {
		t.Errorf("streamed full GET = %d bytes, want %d", rec.Body.Len(), len(body))
	}

	// Range → 206 + byte-exact window.
	rec = callSourcePDF(t, c, store, "/api/papers/"+fixturePaperID+"/sources/"+srcID+"/pdf",
		fmt.Sprintf("bytes=%d-%d", 2<<20, 2<<20+4095))
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("range GET status = %d, want 206", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), body[2<<20:2<<20+4096]) {
		t.Errorf("206 body mismatch over real S3 (%d bytes)", rec.Body.Len())
	}
	_ = config.Config{}
}

// fakeRoutePDF8MiB mirrors the objstore test fixture shape.
func fakeRoutePDF8MiB() []byte {
	const size = 8 << 20
	b := make([]byte, size)
	copy(b, "%PDF-1.4\n%qatlas routes stream integration\n")
	for i := 48; i+8 <= size; i += 8 {
		b[i] = byte(i >> 9)
		b[i+1] = byte(i >> 3)
		b[i+2] = byte(i)
		b[i+3] = byte(i % 251)
	}
	return b
}
