package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/pocketbase/pocketbase/core"
)

// callImages invokes paperImagesHandler with a synthetic request and
// returns the recorder plus the decoded body.
func callImages(t *testing.T, catalog *registry.Store, store objstore.Store, paperID string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/papers/"+paperID+"/images", nil)
	rec := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rec
	if err := paperImagesHandler(re, catalog, store, paperID); err != nil {
		t.Fatalf("paperImagesHandler: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return rec, body
}

// TestPaperImagesHandlerUnavailable verifies graceful degradation: with
// no PostgreSQL pool configured the handler returns 503, not 500.
func TestPaperImagesHandlerUnavailable(t *testing.T) {
	rec, _ := callImages(t, registry.NewStore(nil), nil, "qa_00000000000000000000000000")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// TestImageListingCandidates locks in the prefix-derivation rules for
// both production layouts (zip / per-paper dir), the legacy bare-stem
// variants, and the DOI namespace.
func TestImageListingCandidates(t *testing.T) {
	arxivPaper := &registry.Paper{ArxivID: "0906.0016"}
	oldStylePaper := &registry.Paper{ArxivID: "quant-ph/9508027"}
	doiPaper := &registry.Paper{DOI: "10.1103/physrevlett.103.150502"}

	cases := []struct {
		name  string
		paper *registry.Paper
		asset registry.Asset
		want  []string
	}{
		{
			"new-style arxiv",
			arxivPaper,
			registry.Asset{Source: "arxiv", ArxivVersion: 2},
			[]string{"images/0906/0906.0016v2.zip", "images/0906/0906.0016v2/"},
		},
		{
			"old-style arxiv (canonical + legacy bare)",
			oldStylePaper,
			registry.Asset{Source: "arxiv", ArxivVersion: 1},
			[]string{
				"images/9508/quant-ph/9508027v1.zip", "images/9508/quant-ph/9508027v1/",
				"images/9508/9508027v1.zip", "images/9508/9508027v1/",
			},
		},
		{
			"published (doi)",
			doiPaper,
			registry.Asset{Source: "published"},
			[]string{
				"images/doi/10.1103/physrevlett.103.150502.zip",
				"images/doi/10.1103/physrevlett.103.150502/",
			},
		},
		{
			"arxiv asset without arxiv id",
			&registry.Paper{},
			registry.Asset{Source: "arxiv", ArxivVersion: 1},
			nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := imageListingCandidates(c.paper, c.asset)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("candidates = %v, want %v", got, c.want)
			}
		})
	}
}

// TestListAssetImages covers the probe order (zip first, then dir), the
// empty result, and truncation at imagesListMaxFiles.
func TestListAssetImages(t *testing.T) {
	ctx := context.Background()

	seed := func(t *testing.T, keys ...string) objstore.Store {
		t.Helper()
		s, err := objstore.NewLocalStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewLocalStore: %v", err)
		}
		for _, k := range keys {
			if _, err := s.Put(ctx, k, strings.NewReader("x"), 1, ""); err != nil {
				t.Fatalf("Put %s: %v", k, err)
			}
		}
		return s
	}

	// Zip layout wins when present.
	store := seed(t,
		"images/0906/0906.0016v2.zip",
		"images/0906/0906.0016v2/img-0.png", // dir sibling must be ignored
	)
	kind, files, truncated, err := listAssetImages(ctx, store,
		[]string{"images/0906/0906.0016v2.zip", "images/0906/0906.0016v2/"})
	if err != nil {
		t.Fatalf("listAssetImages: %v", err)
	}
	if kind != "zip" || truncated || len(files) != 1 || files[0].Key != "images/0906/0906.0016v2.zip" {
		t.Errorf("zip probe = kind %q files %v truncated %v", kind, files, truncated)
	}

	// Dir layout when no zip exists.
	store = seed(t,
		"images/0906/0906.0016v2/img-1.png",
		"images/0906/0906.0016v2/img-0.png",
	)
	kind, files, truncated, err = listAssetImages(ctx, store,
		[]string{"images/0906/0906.0016v2.zip", "images/0906/0906.0016v2/"})
	if err != nil {
		t.Fatalf("listAssetImages: %v", err)
	}
	if kind != "dir" || truncated || len(files) != 2 {
		t.Fatalf("dir probe = kind %q files %v truncated %v", kind, files, truncated)
	}
	if files[0].Key != "images/0906/0906.0016v2/img-0.png" {
		t.Errorf("files not sorted by key: %v", files)
	}

	// Nothing found: canonical zip kind, empty files.
	store = seed(t)
	kind, files, truncated, err = listAssetImages(ctx, store,
		[]string{"images/0906/0906.0016v2.zip", "images/0906/0906.0016v2/"})
	if err != nil {
		t.Fatalf("listAssetImages: %v", err)
	}
	if kind != "zip" || truncated || len(files) != 0 {
		t.Errorf("empty probe = kind %q files %v truncated %v", kind, files, truncated)
	}
}
