package routes

// papers_source_stream_test.go: streaming Range delivery for the Q1
// source-PDF endpoint. The hermetic tests here drive the >4 MiB branch
// (store.GetRange-backed rangedReadSeeker under http.ServeContent) with
// a LocalStore; TestIntegrationSourcePDFStreamRustFS in the integration
// build runs the same handler against a real S3 backend.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/pocketbase/pocketbase/core"
)

// bigFakePDF builds a deterministic >4 MiB payload (crosses
// streamRangeThreshold so serveSourcePDF takes the ranged path).
func bigFakePDF() []byte {
	const size = 5<<20 + 12345
	b := make([]byte, size)
	copy(b, "%PDF-1.4\n%qatlas stream test\n")
	for i := 32; i+8 <= size; i += 8 {
		b[i] = byte(i >> 13)
		b[i+1] = byte(i >> 5)
		b[i+2] = byte(i)
	}
	return b
}

// bigSourceFixture seeds a >4 MiB fake source PDF + row into the fake
// catalog and returns the source id.
func bigSourceFixture(t *testing.T, c *fakeBlockCatalog, store objstore.Store) string {
	t.Helper()
	body := bigFakePDF()
	sum := sha256.Sum256(body)
	key := "blockcomments/fixture/big-source.pdf"
	if _, err := store.Put(context.Background(), key, bytes.NewReader(body), int64(len(body)), "application/pdf"); err != nil {
		t.Fatalf("seed big pdf: %v", err)
	}
	const srcID = "src_big_stream"
	c.sources[fixturePaperID][srcID] = registry.PaperSource{
		SourceID: srcID, PaperID: fixturePaperID, Origin: "upload",
		Sha256: hex.EncodeToString(sum[:]), ObjstoreKey: key,
		SizeBytes: int64(len(body)),
	}
	return srcID
}

func callSourcePDF(t *testing.T, c blockCatalog, store objstore.Store, path, rangeHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	rec := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rec
	raw := path[len("/api/papers/"):]
	handled, err := dispatchBlockOriginalsGET(re, &config.Config{PaperAccessEnabled: true}, store, c, raw)
	if !handled {
		t.Fatalf("dispatcher did not handle %q", path)
	}
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	return rec
}

func TestSourcePDFStreamFullGetSHA(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	srcID := bigSourceFixture(t, c, store)
	body := bigFakePDF()
	want := sha256.Sum256(body)

	rec := callSourcePDF(t, c, store, "/api/papers/"+fixturePaperID+"/sources/"+srcID+"/pdf", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	got := sha256.Sum256(rec.Body.Bytes())
	if hex.EncodeToString(got[:]) != hex.EncodeToString(want[:]) {
		t.Errorf("streamed full GET sha mismatch")
	}
	if rec.Header().Get("X-QAtlas-Sha256") != hex.EncodeToString(want[:]) {
		t.Error("X-QAtlas-Sha256 header missing/wrong on streamed path")
	}
	if n := rec.Body.Len(); n != len(body) {
		t.Errorf("streamed body = %d bytes, want %d", n, len(body))
	}
}

func TestSourcePDFStreamRange206(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	srcID := bigSourceFixture(t, c, store)
	body := bigFakePDF()

	rec := callSourcePDF(t, c, store, "/api/papers/"+fixturePaperID+"/sources/"+srcID+"/pdf",
		fmt.Sprintf("bytes=%d-%d", 1<<20, 1<<20+2047))
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), body[1<<20:1<<20+2048]) {
		t.Errorf("206 body mismatch (%d bytes)", rec.Body.Len())
	}
	if cr := rec.Header().Get("Content-Range"); cr == "" {
		t.Error("missing Content-Range on 206")
	}

	// Suffix range (last 100 bytes) via ServeContent semantics.
	rec = callSourcePDF(t, c, store, "/api/papers/"+fixturePaperID+"/sources/"+srcID+"/pdf",
		fmt.Sprintf("bytes=-%d", 100))
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("suffix range status = %d, want 206", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), body[len(body)-100:]) {
		t.Errorf("suffix 206 body mismatch")
	}

	// Unsatisfiable range → 416, no body substitution.
	rec = callSourcePDF(t, c, store, "/api/papers/"+fixturePaperID+"/sources/"+srcID+"/pdf",
		fmt.Sprintf("bytes=%d-", len(body)+10))
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("unsatisfiable range status = %d, want 416", rec.Code)
	}
}

func TestSourcePDFStreamSizeDriftIs500(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	srcID := bigSourceFixture(t, c, store)
	// Corrupt the row's pinned size: Stat disagrees → honest 500,
	// never a truncated "successful" stream.
	src := c.sources[fixturePaperID][srcID]
	src.SizeBytes += 7
	c.sources[fixturePaperID][srcID] = src

	rec := callSourcePDF(t, c, store, "/api/papers/"+fixturePaperID+"/sources/"+srcID+"/pdf", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on size drift", rec.Code)
	}
}

// TestSourcePDFSmallStillWholeRead locks the threshold behaviour: a
// <=4 MiB source takes the buffered, server-side sha-verified path —
// a corrupted small object must 500 (hash mismatch), not stream.
func TestSourcePDFLargeSameSizeCorruptionRejectedBeforeRange(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	id := bigSourceFixture(t, c, store)
	src := c.sources[fixturePaperID][id]
	bad := bigFakePDF()
	bad[100] ^= 0xff
	if _, err := store.Put(context.Background(), src.ObjstoreKey, bytes.NewReader(bad), int64(len(bad)), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	re, rec := newGetReq(t, "/pdf")
	re.Request.Header.Set("Range", "bytes=0-1023")
	err := serveSourcePDF(re, store, src, "source.pdf")
	if !errors.Is(err, paperbundle.ErrIntegrity) || rec.Body.Len() != 0 {
		t.Fatalf("same-size corrupt PDF streamed: %v %d bytes", err, rec.Body.Len())
	}
}

func TestSourcePDFSmallStillWholeRead(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	// Tamper with the fixture PDF bytes behind the row's back.
	pdfKey := "blockcomments/fixture/tampered.pdf"
	tampered := []byte("%PDF-1.4 definitely not the fixture bytes")
	if _, err := store.Put(context.Background(), pdfKey, bytes.NewReader(tampered), int64(len(tampered)), "application/pdf"); err != nil {
		t.Fatalf("seed tampered: %v", err)
	}
	const srcID = "src_tampered"
	fakeSum := sha256.Sum256([]byte("not these bytes at all"))
	c.sources[fixturePaperID][srcID] = registry.PaperSource{
		SourceID: srcID, PaperID: fixturePaperID, Origin: "upload",
		Sha256: hex.EncodeToString(fakeSum[:]), ObjstoreKey: pdfKey,
		SizeBytes: int64(len(tampered)),
	}
	rec := callSourcePDF(t, c, store, "/api/papers/"+fixturePaperID+"/sources/"+srcID+"/pdf", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on small-object sha mismatch", rec.Code)
	}
}
