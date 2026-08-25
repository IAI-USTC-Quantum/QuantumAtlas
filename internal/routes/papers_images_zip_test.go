package routes

// Tests for GET /api/papers/{id_or_doi}/images/zip (plan §B): the
// explicit images-bundle download for both arxiv and DOI id forms,
// plus the dispatch-layer action peel.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"

	"github.com/pocketbase/pocketbase/core"
)

// zipFakeStore answers Stat/Get/PresignGet from an in-memory map of
// key → bytes. presign controls whether PresignGet reports support.
type zipFakeStore struct {
	objects map[string][]byte
	presign bool
}

func (s *zipFakeStore) Stat(_ context.Context, key string) (objstore.ObjectInfo, bool, error) {
	if b, ok := s.objects[key]; ok {
		return objstore.ObjectInfo{Key: key, Size: int64(len(b))}, true, nil
	}
	return objstore.ObjectInfo{}, false, nil
}

func (s *zipFakeStore) Get(_ context.Context, key string) (io.ReadCloser, objstore.ObjectInfo, error) {
	if b, ok := s.objects[key]; ok {
		return io.NopCloser(strings.NewReader(string(b))), objstore.ObjectInfo{Key: key, Size: int64(len(b))}, nil
	}
	return nil, objstore.ObjectInfo{}, objstore.ErrNotFound
}

func (s *zipFakeStore) PresignGet(_ context.Context, key string, _ time.Duration) (string, bool, error) {
	if !s.presign {
		return "", false, nil
	}
	return "https://rustfs.test/presigned/" + key, true, nil
}

func (s *zipFakeStore) Put(context.Context, string, io.Reader, int64, string) (int64, error) {
	panic("Put unused")
}
func (s *zipFakeStore) PutWithMeta(context.Context, string, io.Reader, int64, string, map[string]string) (int64, error) {
	panic("PutWithMeta unused")
}
func (s *zipFakeStore) PutWithOptions(context.Context, string, io.Reader, int64, objstore.PutOptions) (int64, error) {
	panic("PutWithOptions unused")
}
func (s *zipFakeStore) Delete(context.Context, string) error { panic("Delete unused") }
func (s *zipFakeStore) ListPrefix(context.Context, string, int) ([]objstore.ObjectInfo, error) {
	panic("ListPrefix unused")
}
func (s *zipFakeStore) ListDirs(context.Context, string) ([]string, error) { panic("ListDirs unused") }

func mustImagesZipReq(t *testing.T, url string) (*core.RequestEvent, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rec
	return re, rec
}

func TestImagesZipHandler_StreamsBytes(t *testing.T) {
	canonical := "2501.00010v1"
	key := paperassets.AssetKey("images", canonical)
	store := &zipFakeStore{objects: map[string][]byte{key: []byte("PK fake zip bytes")}}

	re, rec := mustImagesZipReq(t, "/api/papers/"+canonical+"/images/zip")
	if err := imagesZipHandler(re, store, canonical); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "2501.00010v1-images.zip") {
		t.Errorf("Content-Disposition = %q, want attachment filename 2501.00010v1-images.zip", got)
	}
	if rec.Body.String() != "PK fake zip bytes" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestImagesZipHandler_NotFound(t *testing.T) {
	store := &zipFakeStore{objects: map[string][]byte{}}
	re, rec := mustImagesZipReq(t, "/api/papers/2501.00010v1/images/zip")
	if err := imagesZipHandler(re, store, "2501.00010v1"); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	detail, _ := body["detail"].(string)
	if !strings.Contains(detail, "no images available") || !strings.Contains(detail, "/markdown") {
		t.Errorf("body.detail = %q, want a 'no images available' message pointing at /markdown", detail)
	}
	if got := body["arxiv_id"]; got != "2501.00010v1" {
		t.Errorf("body.arxiv_id = %v", got)
	}
}

func TestImagesZipHandler_BadFormat400(t *testing.T) {
	canonical := "2501.00010v1"
	key := paperassets.AssetKey("images", canonical)
	store := &zipFakeStore{objects: map[string][]byte{key: []byte("PK")}}
	re, rec := mustImagesZipReq(t, "/api/papers/"+canonical+"/images/zip?format=bogus")
	if err := imagesZipHandler(re, store, canonical); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestImagesZipHandler_BadID400(t *testing.T) {
	store := &zipFakeStore{objects: map[string][]byte{}}
	re, rec := mustImagesZipReq(t, "/api/papers/2501.00010/images/zip")
	if err := imagesZipHandler(re, store, "2501.00010"); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for unversioned arxiv id", rec.Code)
	}
}

func TestImagesZipHandler_FormatLink(t *testing.T) {
	canonical := "2501.00010v1"
	key := paperassets.AssetKey("images", canonical)
	store := &zipFakeStore{objects: map[string][]byte{key: []byte("PK")}, presign: true}
	re, rec := mustImagesZipReq(t, "/api/papers/"+canonical+"/images/zip?format=link")
	if err := imagesZipHandler(re, store, canonical); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := body["images_url"]; got != "https://rustfs.test/presigned/"+key {
		t.Errorf("body.images_url = %v", got)
	}
	if got := body["format"]; got != "link" {
		t.Errorf("body.format = %v, want link", got)
	}
}

func TestImagesZipHandler_FormatLinkFallsBackToBytes(t *testing.T) {
	canonical := "2501.00010v1"
	key := paperassets.AssetKey("images", canonical)
	store := &zipFakeStore{objects: map[string][]byte{key: []byte("PK")}, presign: false}
	re, rec := mustImagesZipReq(t, "/api/papers/"+canonical+"/images/zip?format=link")
	if err := imagesZipHandler(re, store, canonical); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (bytes fallback)", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip fallback", got)
	}
}

func TestImagesZipByDOIHandler_StreamsBytes(t *testing.T) {
	doi := "10.1103/physrevlett.123.070501"
	key := paperassets.DOIAssetKey("images", doi)
	store := &zipFakeStore{objects: map[string][]byte{key: []byte("PK doi zip")}}

	re, rec := mustImagesZipReq(t, "/api/papers/"+doi+"/images/zip")
	if err := imagesZipByDOIHandler(re, store, doi); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", got)
	}
	if rec.Body.String() != "PK doi zip" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestImagesZipByDOIHandler_NotFound(t *testing.T) {
	doi := "10.1103/physrevlett.123.070501"
	store := &zipFakeStore{objects: map[string][]byte{}}
	re, rec := mustImagesZipReq(t, "/api/papers/"+doi+"/images/zip")
	if err := imagesZipByDOIHandler(re, store, doi); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := body["doi"]; got != doi {
		t.Errorf("body.doi = %v", got)
	}
}

func TestImagesZipByDOIHandler_BadDOI400(t *testing.T) {
	store := &zipFakeStore{objects: map[string][]byte{}}
	re, rec := mustImagesZipReq(t, "/api/papers/not-a-doi/images/zip")
	if err := imagesZipByDOIHandler(re, store, "not-a-doi"); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid DOI", rec.Code)
	}
}

// TestPeelImagesZipAction locks in the dispatch-layer peel that turns
// splitPapersPath's ("<id>/images", "zip") parse into the single
// "images/zip" action, for both arxiv and DOI id forms.
func TestPeelImagesZipAction(t *testing.T) {
	cases := []struct {
		raw, wantID, wantAction string
	}{
		{"2501.00010v1/images/zip", "2501.00010v1", "images/zip"},
		{"quant-ph/9508027v2/images/zip", "quant-ph/9508027v2", "images/zip"},
		{"10.1103/PhysRevLett.123.070501/images/zip", "10.1103/PhysRevLett.123.070501", "images/zip"},
		{"10.1234/foo/bar/images/zip", "10.1234/foo/bar", "images/zip"}, // nested-slash DOI
		// non-images paths pass through untouched
		{"2501.00010v1/markdown", "2501.00010v1", "markdown"},
		{"10.1234/foo/pdf", "10.1234/foo", "pdf"},
		{"2501.00010v1/zip", "2501.00010v1", "zip"}, // no /images prefix — no peel
	}
	for _, c := range cases {
		id, action := splitPapersPath(c.raw)
		id, action = peelImagesZipAction(id, action)
		if id != c.wantID || action != c.wantAction {
			t.Errorf("peel(%q) = (%q,%q), want (%q,%q)", c.raw, id, action, c.wantID, c.wantAction)
		}
	}
}
