package papers

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
)

// fakeListStore is a minimal objstore.Store stub that only implements
// ListPrefix (plus panics for everything else). Used to drive the
// listKindPaths / listImageCounts pure logic paths in SyncFromStore
// without needing a live S3 / filesystem.
type fakeListStore struct {
	mu    sync.Mutex
	infos []objstore.ObjectInfo
}

func (f *fakeListStore) ListPrefix(_ context.Context, prefix string, _ int) ([]objstore.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]objstore.ObjectInfo, 0, len(f.infos))
	for _, info := range f.infos {
		if strings.HasPrefix(info.Key, prefix) {
			out = append(out, info)
		}
	}
	return out, nil
}

// Put / PutWithMeta / PutWithOptions / Get / Stat / Delete / PresignGet:
// not exercised by sync logic; panic loudly if a future change starts
// reaching for them.
func (f *fakeListStore) Put(_ context.Context, _ string, _ io.Reader, _ int64, _ string) (int64, error) {
	panic("fakeListStore.Put should not be called")
}
func (f *fakeListStore) PutWithMeta(_ context.Context, _ string, _ io.Reader, _ int64, _ string, _ map[string]string) (int64, error) {
	panic("fakeListStore.PutWithMeta should not be called")
}
func (f *fakeListStore) PutWithOptions(_ context.Context, _ string, _ io.Reader, _ int64, _ objstore.PutOptions) (int64, error) {
	panic("fakeListStore.PutWithOptions should not be called")
}
func (f *fakeListStore) Get(_ context.Context, _ string) (io.ReadCloser, objstore.ObjectInfo, error) {
	panic("fakeListStore.Get should not be called")
}
func (f *fakeListStore) Stat(_ context.Context, _ string) (objstore.ObjectInfo, bool, error) {
	panic("fakeListStore.Stat should not be called")
}
func (f *fakeListStore) Delete(_ context.Context, _ string) error {
	panic("fakeListStore.Delete should not be called")
}
func (f *fakeListStore) PresignGet(_ context.Context, _ string, _ time.Duration) (string, bool, error) {
	panic("fakeListStore.PresignGet should not be called")
}

func TestStemFromKey(t *testing.T) {
	cases := []struct {
		name        string
		key, kind   string
		wantStem    string
		wantIsDOI   bool
		wantOK      bool
	}{
		// arXiv — stem is the bare arxiv id, no yymm prefix in the
		// returned key (the caller pairs it with the bucket path).
		{"arxiv pdf", "pdf/2401/2401.12345v1.pdf", "pdf", "2401.12345v1", false, true},
		{"arxiv md", "markdown/9508/9508027v1.md", "markdown", "9508027v1", false, true},
		// DOI — stem is "registrant/suffix" (e.g. "10.1103/foo")
		// because we need the full identifier to round-trip back to
		// DOINodeKey in the MERGE.
		{"doi pdf", "pdf/doi/10.1103/physrevlett.123.070501.pdf", "pdf", "10.1103/physrevlett.123.070501", true, true},
		{"doi md", "markdown/doi/10.1103/physrevlett.123.070501.md", "markdown", "10.1103/physrevlett.123.070501", true, true},
		// bad shapes
		{"wrong kind", "pdf/2401/2401.12345v1.pdf", "markdown", "", false, false},
		{"too few parts", "pdf/2401.12345v1.pdf", "pdf", "", false, false},
		{"doi wrong kind", "markdown/doi/10.1103/foo.pdf", "pdf", "", false, false},
		{"empty key", "", "pdf", "", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stem, isDOI, ok := stemFromKey(c.key, c.kind)
			if stem != c.wantStem || isDOI != c.wantIsDOI || ok != c.wantOK {
				t.Errorf("stemFromKey(%q,%q) = (%q,%v,%v), want (%q,%v,%v)",
					c.key, c.kind, stem, isDOI, ok, c.wantStem, c.wantIsDOI, c.wantOK)
			}
		})
	}
}

func TestListImageCountsDOIDisambiguation(t *testing.T) {
	// Regression: previously DOI image counts were keyed by registrant
	// alone ("10.1103"), so two sibling DOIs under the same publisher
	// would collide into one counter. They should now be keyed by the
	// synthetic "doi:<doi>" string, one counter per DOI.
	//
	// Fixtures are built via paperassets.DOIAssetKey so the test stays
	// locked to the *real* storage layout. The PR #19 first cut built
	// directory-shaped fake keys ("images/doi/<reg>/<suffix>/fig1.png")
	// that happened to make this assertion pass while the live single-zip
	// layout ("images/doi/<reg>/<suffix>.zip") produced phantom nodes;
	// using DOIAssetKey keeps the fixtures honest going forward.
	doiA := "10.1103/physrevlett.123.070501"
	doiB := "10.1103/nature.12345"
	store := &fakeListStore{infos: []objstore.ObjectInfo{
		{Key: paperassets.DOIAssetKey("images", doiA)},
		{Key: paperassets.DOIAssetKey("images", doiB)},
		// arXiv image for comparison — legacy multi-file directory
		// layout (3+1 parts) is the only shape the existing arxiv
		// branch still recognises.
		{Key: "images/2401/2401.12345v1/fig1.png"},
	}}
	counts, total, err := listImageCounts(context.Background(), store)
	if err != nil {
		t.Fatalf("listImageCounts: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	wantA := DOINodeKey(doiA)
	wantB := DOINodeKey(doiB)
	if counts[wantA] != 1 {
		t.Errorf("counts[%q] = %d, want 1 (was colliding with DOI B before the fix)", wantA, counts[wantA])
	}
	if counts[wantB] != 1 {
		t.Errorf("counts[%q] = %d, want 1 (should be distinct from DOI A)", wantB, counts[wantB])
	}
	// arXiv images are keyed by the stem (parts[2] in the
	// "images/<yymm>/<stem>/<file>" layout). This is the legacy
	// behaviour and is unchanged by the DOI fix.
	if counts["2401.12345v1"] != 1 {
		t.Errorf("arxiv counts[2401.12345v1] = %d, want 1", counts["2401.12345v1"])
	}
	// Guard: no bare-registrant key should leak out (that was the
	// pre-fix behaviour causing sibling-DOI collisions).
	if _, ok := counts["10.1103"]; ok {
		t.Errorf("counts has bare-registrant key %q — sibling-DOI collision regression", "10.1103")
	}
	// Guard: no phantom key with the storage extension baked in
	// (that was the listImageCounts bug fixed alongside this test —
	// DOI single-zip storage put ".zip" into parts[3], which then
	// leaked into the synthetic node key).
	for k := range counts {
		if strings.HasSuffix(k, ".zip") {
			t.Errorf("counts has phantom extension-bearing key %q — listImageCounts must strip path.Ext on DOI suffix", k)
		}
	}
}

// TestListImageCountsDOIRealLayoutPhantomNodeRegression locks in the
// fix for the phantom-:PaperWork-node bug: DOI image storage is a
// SINGLE zip per DOI ("images/doi/<reg>/<suffix>.zip", emitted by
// paperassets.DOIAssetKey), but the original PR #19 listImageCounts
// passed parts[3] verbatim into the synthetic node key, producing
// "doi:<reg>/<suffix>.zip" — a string that never matches any
// :PaperWork node UpsertMDByDOI ever wrote, so mergeImageBatch would
// MERGE-create a brand-new phantom node every sync run. The synthetic
// node key MUST be DOINodeKey(<reg>/<suffix>) without the extension.
func TestListImageCountsDOIRealLayoutPhantomNodeRegression(t *testing.T) {
	doi := "10.1103/physrevlett.123.070501"
	// DOIAssetKey is the source of truth for what the upload handler
	// writes; depend on it directly so a future change to the storage
	// layout breaks this test together with the handler.
	realKey := paperassets.DOIAssetKey("images", doi)
	if realKey == "" || !strings.HasSuffix(realKey, ".zip") {
		t.Fatalf("test pre-condition: DOIAssetKey(images,%q) = %q; expected single-zip layout", doi, realKey)
	}
	store := &fakeListStore{infos: []objstore.ObjectInfo{{Key: realKey}}}
	counts, total, err := listImageCounts(context.Background(), store)
	if err != nil {
		t.Fatalf("listImageCounts: %v", err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}
	want := DOINodeKey(doi)
	if counts[want] != 1 {
		t.Errorf("counts[%q] = %d, want 1 (phantom-node bug regressed: sync would not update real DOI node)", want, counts[want])
	}
	for k := range counts {
		if k != want {
			t.Errorf("counts has unexpected key %q (only %q should appear)", k, want)
		}
	}
}

func TestListKindPathsDOIRouting(t *testing.T) {
	// Regression: listKindPaths must key DOI assets by the synthetic
	// "doi:<doi>" string so the subsequent MERGE in mergeAssetBatch
	// lands on the existing :PaperWork node from UpsertPDFByDOI /
	// UpsertMDByDOI — not on a phantom arxiv_id='<doi>' node.
	store := &fakeListStore{infos: []objstore.ObjectInfo{
		{Key: "pdf/doi/10.1103/physrevlett.123.070501.pdf"},
		{Key: "pdf/2401/2401.12345v1.pdf"},
		{Key: "markdown/doi/10.7717/peerj.4375.md"},
	}}

	pdfPaths, err := listKindPaths(context.Background(), store, "pdf")
	if err != nil {
		t.Fatalf("listKindPaths pdf: %v", err)
	}
	doiKey := DOINodeKey("10.1103/physrevlett.123.070501")
	if _, ok := pdfPaths[doiKey]; !ok {
		t.Errorf("pdfPaths missing DOI key %q; got %v", doiKey, pdfPaths)
	}
	// arxiv stem stays as the bare arxiv id (no yymm prefix in the map key)
	if _, ok := pdfPaths["2401.12345v1"]; !ok {
		t.Errorf("pdfPaths missing arxiv stem; got %v", pdfPaths)
	}
	// Guard: DOI stem must NOT appear as a bare stem in the map
	// (that would route it to the arxiv-fallback MERGE and create
	// a phantom arxiv_id='<doi>' node).
	if _, ok := pdfPaths["10.1103/physrevlett.123.070501"]; ok {
		t.Errorf("pdfPaths has bare DOI stem — would route to arxiv fallback MERGE")
	}

	mdPaths, err := listKindPaths(context.Background(), store, "markdown")
	if err != nil {
		t.Fatalf("listKindPaths markdown: %v", err)
	}
	doiMdKey := DOINodeKey("10.7717/peerj.4375")
	if _, ok := mdPaths[doiMdKey]; !ok {
		t.Errorf("mdPaths missing DOI key %q; got %v", doiMdKey, mdPaths)
	}
}

func TestDOINodeKey(t *testing.T) {
	if got := DOINodeKey("10.1103/physrevlett.123.070501"); got != "doi:10.1103/physrevlett.123.070501" {
		t.Errorf("DOINodeKey = %q, want doi:<doi>", got)
	}
}
