package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"sync"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/pocketbase/pocketbase/core"
)

// A locked catalog models the production paper advisory lock and immutable
// canonical alias, while real LocalStore checks actual create-only bytes.
type uploadSourceCatalog struct {
	mu      sync.Mutex
	sources map[string]registry.PaperSource
	aliases map[string]string
}

func newUploadSourceCatalog() *uploadSourceCatalog {
	return &uploadSourceCatalog{sources: map[string]registry.PaperSource{}, aliases: map[string]string{}}
}
func (c *uploadSourceCatalog) ResolveOrMint(context.Context, registry.PaperRef) (string, bool, error) {
	return "qa_upload", false, nil
}
func (c *uploadSourceCatalog) GetImportedPaperSource(_ context.Context, paper, key string) (registry.PaperSource, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, ok := c.aliases[key]
	return c.sources[id], ok, nil
}
func (c *uploadSourceCatalog) FreezePaperSource(ctx context.Context, store objstore.Store, src registry.PaperSource) (registry.PaperSource, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	src = c.sources[src.SourceID]
	_, err := paperbundle.New(store).ReadPDF(ctx, src.PaperID, src.SourceID, src.Sha256, src.SizeBytes)
	return src, err
}
func (c *uploadSourceCatalog) RegisterFrozenPDF(ctx context.Context, store objstore.Store, paper, origin string, data []byte) (registry.PaperSource, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sha := paperbundle.SHA256(data)
	for _, src := range c.sources {
		if src.PaperID == paper && src.Sha256 == sha {
			_, err := paperbundle.New(store).ReadPDF(ctx, paper, src.SourceID, sha, src.SizeBytes)
			return src, err
		}
	}
	src := registry.PaperSource{PaperID: paper, SourceID: registry.NewSourceID(), Origin: origin, Sha256: sha, SizeBytes: int64(len(data)), CreatedAt: time.Now()}
	frozen, err := paperbundle.New(store).FreezePDF(ctx, paper, src.SourceID, data, sha)
	if err != nil {
		return src, err
	}
	src.ObjstoreKey = frozen.Key
	c.sources[src.SourceID] = src
	return src, nil
}
func (c *uploadSourceCatalog) BindPaperSourceImport(ctx context.Context, store objstore.Store, paper, id, key string) (registry.PaperSource, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	src := c.sources[id]
	if prior, ok := c.aliases[key]; ok {
		old := c.sources[prior]
		if old.Sha256 != src.Sha256 {
			return src, paperbundle.ErrIntegrity
		}
		src = old
	} else {
		c.aliases[key] = id
	}
	_, err := paperbundle.New(store).ReadPDF(ctx, paper, src.SourceID, src.Sha256, src.SizeBytes)
	return src, err
}
func (c *uploadSourceCatalog) UpsertPDF(_ context.Context, ref registry.PaperRef, version int, sha string, size int64, key string) (string, int64, error) {
	return "qa_upload", 1, nil
}
func (c *uploadSourceCatalog) UpsertPDFByDOI(_ context.Context, ref registry.PaperRef, sha string, size int64, key string) (string, int64, error) {
	return "qa_upload", 1, nil
}

func TestUploadPDFHandler_ConcurrentDifferentBytes(t *testing.T) {
	testFrozenUploadConcurrency(t, false)
}
func TestUploadPDFHandler_ConcurrentIdenticalBytes(t *testing.T) {
	testFrozenUploadConcurrency(t, true)
}
func testFrozenUploadConcurrency(t *testing.T, identical bool) {
	t.Helper()
	const id = "2501.99999v1"
	const count = 8
	store, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	catalog := newUploadSourceCatalog()
	gate := make(chan struct{})
	var wg sync.WaitGroup
	codes := make([]int, count)
	bodies := make([]map[string]any, count)
	payloads := make([][]byte, count)
	for i := range count {
		suffix := i
		if identical {
			suffix = 0
		}
		payloads[i] = []byte(fmt.Sprintf("%%PDF-1.4\nworker-%d\n", suffix))
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			req := buildUploadPDFRequest(t, id, payloads[i])
			rec := httptest.NewRecorder()
			re := &core.RequestEvent{}
			re.Request = req
			re.Response = rec
			if err := uploadFrozenPDF(re, &config.Config{}, store, catalog, nil, id, false); err != nil {
				t.Error(err)
			}
			codes[i] = rec.Code
			_ = json.Unmarshal(rec.Body.Bytes(), &bodies[i])
		}(i)
	}
	close(gate)
	wg.Wait()
	successes := 0
	var winning []byte
	for i, code := range codes {
		if code == 201 || code == 200 {
			successes++
			winning = payloads[i]
		} else if identical || code != 409 {
			t.Fatalf("unexpected upload %d: %d %+v", i, code, bodies[i])
		}
	}
	if identical && successes != count || !identical && successes != 1 {
		t.Fatalf("successes=%d codes=%v", successes, codes)
	}
	alias := paperassets.AssetKey("pdf", id)
	src, found, err := catalog.GetImportedPaperSource(t.Context(), "qa_upload", alias)
	if err != nil || !found {
		t.Fatal("no frozen alias", err)
	}
	data, err := paperbundle.New(store).ReadPDF(t.Context(), src.PaperID, src.SourceID, src.Sha256, src.SizeBytes)
	if err != nil || !bytes.Equal(data, winning) {
		t.Fatal("winner frozen bytes changed", err)
	}
	objects, err := store.ListPrefix(t.Context(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		if !bytes.HasPrefix([]byte(object.Key), []byte("content/")) {
			t.Fatalf("new upload wrote legacy location: %s", object.Key)
		}
	}
}

func TestFrozenPDFUploadRequiresCatalogBeforePersisting(t *testing.T) {
	store, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	req := buildUploadPDFRequest(t, "2501.99999v1", []byte("%PDF-original"))
	rec := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rec
	if err := uploadPDFHandler(re, &config.Config{}, store, registry.NewStore(nil), "2501.99999v1"); err != nil {
		t.Fatal(err)
	}
	objects, _ := store.ListPrefix(t.Context(), "", 0)
	if rec.Code != 503 || len(objects) != 0 {
		t.Fatalf("deferred success or orphan bytes: status=%d keys=%v", rec.Code, objects)
	}
}

func TestFrozenPDFUploadOverwriteCannotReplaceBoundSource(t *testing.T) {
	store, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := newUploadSourceCatalog()
	id := "2501.99999v1"
	for i, payload := range [][]byte{[]byte("%PDF-first"), []byte("%PDF-second")} {
		req := buildUploadPDFRequest(t, id, payload)
		q := req.URL.Query()
		q.Set("overwrite", "true")
		req.URL.RawQuery = q.Encode()
		rec := httptest.NewRecorder()
		re := &core.RequestEvent{}
		re.Request = req
		re.Response = rec
		if err := uploadFrozenPDF(re, &config.Config{}, store, c, nil, id, false); err != nil {
			t.Fatal(err)
		}
		want := 201
		if i == 1 {
			want = 409
		}
		if rec.Code != want {
			t.Fatalf("overwrite=%d body=%s", rec.Code, rec.Body.String())
		}
	}
}

func buildUploadPDFRequest(t *testing.T, arxivID string, pdfBytes []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", `form-data; name="pdf"; filename="paper.pdf"`)
	hdr.Set("Content-Type", "application/pdf")
	pw, err := mw.CreatePart(hdr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pw.Write(pdfBytes); err != nil {
		t.Fatal(err)
	}
	if err = mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/papers/"+arxivID+"/upload-pdf", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}
