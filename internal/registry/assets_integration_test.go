package registry

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
)

// fakeListStore is a minimal objstore.Store stub that only implements
// ListPrefix / ListDirs (plus panics for everything else). Used to drive
// the listing paths of SyncFromStore without a live S3 / filesystem.
// Copied from internal/papers/sync_test.go.
//
// failPrefix, when non-empty, makes ListPrefix return failErr for that
// exact prefix — used to prove a failing shard aborts the sync listing
// or triggers drill-down. failTimes < 0 fails forever; failTimes > 0
// fails that many calls and then succeeds (retry tests).
type fakeListStore struct {
	mu         sync.Mutex
	infos      []objstore.ObjectInfo
	failPrefix string
	failErr    error
	failTimes  int
}

func (f *fakeListStore) ListPrefix(_ context.Context, prefix string, _ int) ([]objstore.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPrefix != "" && prefix == f.failPrefix {
		if f.failTimes < 0 {
			return nil, f.failErr
		}
		if f.failTimes > 0 {
			f.failTimes--
			return nil, f.failErr
		}
	}
	out := make([]objstore.ObjectInfo, 0, len(f.infos))
	for _, info := range f.infos {
		if strings.HasPrefix(info.Key, prefix) {
			out = append(out, info)
		}
	}
	return out, nil
}

func (f *fakeListStore) ListDirs(_ context.Context, prefix string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, info := range f.infos {
		if !strings.HasPrefix(info.Key, prefix) {
			continue
		}
		rest := strings.TrimPrefix(info.Key, prefix)
		i := strings.IndexByte(rest, '/')
		if i < 0 {
			continue // object directly at this level, not a dir
		}
		dir := prefix + rest[:i+1]
		if !seen[dir] {
			seen[dir] = true
			out = append(out, dir)
		}
	}
	return out, nil
}

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

// migrateStore returns a migrated registry Store over the test pool.
func migrateStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	pool, ctx := testPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return NewStore(pool), ctx
}

// cleanupPapers deletes the test papers (assets + identities cascade).
func cleanupPapers(t *testing.T, s *Store, ids ...string) {
	t.Helper()
	_, _ = s.pool.Exec(context.Background(),
		`DELETE FROM papers WHERE paper_id = ANY($1)`, ids)
}

// TestIntegrationUpsertPDFMD covers the arXiv pdf→md flow: mint via
// UpsertPDF, status flips ready, the default-asset trigger points at the
// new asset, a lease survives until UpsertMD clears it, and the asset
// drops out of NeedsMineru.
func TestIntegrationUpsertPDFMD(t *testing.T) {
	s, ctx := migrateStore(t)

	const arxiv = "2401.92001"
	paperID, assetID, err := s.UpsertPDF(ctx, PaperRef{ArxivID: arxiv + "v1"}, 1,
		"deadbeef", 12345, "2401/2401.92001v1.pdf")
	if err != nil {
		t.Fatalf("UpsertPDF: %v", err)
	}
	defer cleanupPapers(t, s, paperID)
	if !strings.HasPrefix(paperID, "qa_") {
		t.Errorf("paperID %q: want qa_ prefix", paperID)
	}
	if assetID <= 0 {
		t.Errorf("assetID = %d, want > 0", assetID)
	}

	// Status ready + default asset trigger fired.
	p, found, err := s.Get(ctx, paperID)
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if p.Status != "ready" {
		t.Errorf("status = %q, want ready", p.Status)
	}
	var defaultAsset int64
	if err := s.pool.QueryRow(ctx,
		`SELECT default_asset_id FROM papers WHERE paper_id = $1`, paperID).Scan(&defaultAsset); err != nil {
		t.Fatalf("default_asset_id: %v", err)
	}
	if defaultAsset != assetID {
		t.Errorf("default_asset_id = %d, want %d", defaultAsset, assetID)
	}

	// Idempotent re-upsert resolves the same paper + asset.
	paperID2, assetID2, err := s.UpsertPDF(ctx, PaperRef{ArxivID: arxiv + "v1"}, 1,
		"deadbeef", 12345, "2401/2401.92001v1.pdf")
	if err != nil {
		t.Fatalf("re-UpsertPDF: %v", err)
	}
	if paperID2 != paperID || assetID2 != assetID {
		t.Errorf("re-UpsertPDF = (%q, %d), want (%q, %d)", paperID2, assetID2, paperID, assetID)
	}

	// Lease, then UpsertMD clears it.
	grant, err := s.Lease(ctx, paperID, "tester", 0)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if grant.TTLSeconds != DefaultTTLSeconds {
		t.Errorf("grant TTL = %d, want %d", grant.TTLSeconds, DefaultTTLSeconds)
	}
	if err := s.UpsertMD(ctx, paperID, 1, "", 0,
		"2401/2401.92001v1.md", "2401/2401.92001v1.json", 7); err != nil {
		t.Fatalf("UpsertMD: %v", err)
	}
	var (
		mdPath, jsonPath *string
		imageCount       *int
		leaseID          *string
	)
	if err := s.pool.QueryRow(ctx, `
		SELECT mineru_md_path, mineru_json_path, image_count, lease_id
		FROM paper_assets WHERE asset_id = $1`, assetID,
	).Scan(&mdPath, &jsonPath, &imageCount, &leaseID); err != nil {
		t.Fatalf("asset row: %v", err)
	}
	if mdPath == nil || *mdPath != "2401/2401.92001v1.md" {
		t.Errorf("mineru_md_path = %v", mdPath)
	}
	if jsonPath == nil || *jsonPath != "2401/2401.92001v1.json" {
		t.Errorf("mineru_json_path = %v", jsonPath)
	}
	if imageCount == nil || *imageCount != 7 {
		t.Errorf("image_count = %v, want 7", imageCount)
	}
	if leaseID != nil {
		t.Errorf("lease_id = %v, want cleared by UpsertMD", *leaseID)
	}

	// With markdown in place the asset leaves the needs-mineru queue.
	rows, err := s.NeedsMineru(ctx, 100)
	if err != nil {
		t.Fatalf("NeedsMineru: %v", err)
	}
	for _, r := range rows {
		if r.PaperID == paperID {
			t.Errorf("NeedsMineru still lists %q after UpsertMD", paperID)
		}
	}
}

// TestIntegrationUpsertPDFFlipsReady covers the status transition: a
// 'failed' paper becomes 'ready' on a successful asset upsert.
func TestIntegrationUpsertPDFFlipsReady(t *testing.T) {
	s, ctx := migrateStore(t)

	paperID, _, err := s.ResolveOrMint(ctx, PaperRef{ArxivID: "2401.92002"})
	if err != nil {
		t.Fatalf("ResolveOrMint: %v", err)
	}
	defer cleanupPapers(t, s, paperID)
	if ok, err := s.UpdateStatus(ctx, paperID, "failed"); err != nil || !ok {
		t.Fatalf("UpdateStatus: ok=%v err=%v", ok, err)
	}
	if _, _, err := s.UpsertPDF(ctx, PaperRef{ArxivID: "2401.92002"}, 1,
		"", 100, "2401/2401.92002v1.pdf"); err != nil {
		t.Fatalf("UpsertPDF: %v", err)
	}
	p, _, err := s.Get(ctx, paperID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.Status != "ready" {
		t.Errorf("status = %q, want ready after UpsertPDF", p.Status)
	}
}

// TestIntegrationLeaseConflict covers the lease lifecycle: grant,
// conflicting grant (ErrAlreadyLeased), mismatched release
// (ErrIDMismatch), release, TTL clamping, and GC of expired leases.
func TestIntegrationLeaseConflict(t *testing.T) {
	s, ctx := migrateStore(t)

	paperID, _, err := s.UpsertPDF(ctx, PaperRef{ArxivID: "2401.92003v2"}, 2,
		"", 100, "2401/2401.92003v2.pdf")
	if err != nil {
		t.Fatalf("UpsertPDF: %v", err)
	}
	defer cleanupPapers(t, s, paperID)

	grant, err := s.Lease(ctx, paperID, "alice", 10) // clamps to MinTTLSeconds
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if grant.TTLSeconds != MinTTLSeconds {
		t.Errorf("TTL = %d, want clamped %d", grant.TTLSeconds, MinTTLSeconds)
	}
	if grant.ArxivID != "2401.92003v2" {
		t.Errorf("grant.ArxivID = %q, want 2401.92003v2", grant.ArxivID)
	}
	if grant.PDFPath != "2401/2401.92003v2.pdf" {
		t.Errorf("grant.PDFPath = %q", grant.PDFPath)
	}

	// Second lease on the same paper conflicts.
	_, err = s.Lease(ctx, paperID, "bob", 0)
	var already *ErrAlreadyLeased
	if !errors.As(err, &already) {
		t.Fatalf("second Lease err = %v, want ErrAlreadyLeased", err)
	}
	if already.Existing.Holder != "alice" {
		t.Errorf("conflict holder = %q, want alice", already.Existing.Holder)
	}

	// Leased assets stay out of the needs-mineru queue.
	rows, err := s.NeedsMineru(ctx, 100)
	if err != nil {
		t.Fatalf("NeedsMineru: %v", err)
	}
	for _, r := range rows {
		if r.PaperID == paperID {
			t.Errorf("NeedsMineru lists leased paper %q", paperID)
		}
	}

	// Mismatched release fails; matching release succeeds.
	if err := s.ReleaseLease(ctx, paperID, "not-the-lease-id"); !errors.Is(err, ErrIDMismatch) {
		t.Errorf("ReleaseLease mismatch err = %v, want ErrIDMismatch", err)
	}
	if err := s.ReleaseLease(ctx, paperID, grant.LeaseID); err != nil {
		t.Fatalf("ReleaseLease: %v", err)
	}
	// Idempotent second release.
	if err := s.ReleaseLease(ctx, paperID, grant.LeaseID); err != nil {
		t.Errorf("second ReleaseLease err = %v, want nil", err)
	}

	// Expired leases are GC'd and the asset becomes leasable again.
	if _, err := s.pool.Exec(ctx, `
		UPDATE paper_assets a SET lease_id = 'expired', lease_holder = 'ghost',
			lease_expires_at = now() - interval '1 minute'
		FROM papers p
		WHERE p.paper_id = $1 AND a.asset_id = p.default_asset_id`, paperID); err != nil {
		t.Fatalf("force-expire lease: %v", err)
	}
	n, err := s.GCExpiredLeases(ctx)
	if err != nil {
		t.Fatalf("GCExpiredLeases: %v", err)
	}
	if n != 1 {
		t.Errorf("GCExpiredLeases cleared %d, want 1", n)
	}
	if _, err := s.Lease(ctx, paperID, "carol", MaxTTLSeconds+1000); err != nil {
		t.Fatalf("re-Lease after GC: %v", err)
	}

	// Unknown paper is not leasable.
	if _, err := s.Lease(ctx, "qa_00000000000000000000000000", "dave", 0); !errors.Is(err, ErrNotLeasable) {
		t.Errorf("Lease unknown paper err = %v, want ErrNotLeasable", err)
	}
}

// TestIntegrationNeedsMineru covers the queue listing: pdf-without-md
// papers appear newest-first with the versioned arXiv id, leased and
// md-complete papers are excluded.
func TestIntegrationNeedsMineru(t *testing.T) {
	s, ctx := migrateStore(t)

	pid1, _, err := s.UpsertPDF(ctx, PaperRef{ArxivID: "2401.92004v1"}, 1,
		"", 111, "2401/2401.92004v1.pdf")
	if err != nil {
		t.Fatalf("UpsertPDF 1: %v", err)
	}
	defer cleanupPapers(t, s, pid1)
	time.Sleep(20 * time.Millisecond) // distinct fetched_at ordering
	pid2, _, err := s.UpsertPDF(ctx, PaperRef{ArxivID: "2401.92005v3"}, 3,
		"", 222, "2401/2401.92005v3.pdf")
	if err != nil {
		t.Fatalf("UpsertPDF 2: %v", err)
	}
	defer cleanupPapers(t, s, pid2)

	rows, err := s.NeedsMineru(ctx, 100)
	if err != nil {
		t.Fatalf("NeedsMineru: %v", err)
	}
	idx := map[string]int{}
	for i, r := range rows {
		idx[r.PaperID] = i
	}
	i1, ok1 := idx[pid1]
	i2, ok2 := idx[pid2]
	if !ok1 || !ok2 {
		t.Fatalf("NeedsMineru missing test papers: ok1=%v ok2=%v rows=%v", ok1, ok2, rows)
	}
	if i2 >= i1 {
		t.Errorf("newest-first violated: pid2 index %d >= pid1 index %d", i2, i1)
	}
	if rows[i2].ArxivID != "2401.92005v3" || rows[i2].Version != 3 {
		t.Errorf("row = %+v, want versioned id 2401.92005v3 / version 3", rows[i2])
	}
	if rows[i1].PDFPath != "2401/2401.92004v1.pdf" || rows[i1].PDFSizeBytes != 111 {
		t.Errorf("row = %+v", rows[i1])
	}
}

// TestIntegrationIsHosted covers the scheme dispatch and normalization.
func TestIntegrationIsHosted(t *testing.T) {
	s, ctx := migrateStore(t)

	paperID, _, err := s.ResolveOrMint(ctx, PaperRef{
		ArxivID:    "2401.92006v2",
		DOI:        "10.9999/qatlas.test.92006",
		OpenAlexID: "W92006",
	})
	if err != nil {
		t.Fatalf("ResolveOrMint: %v", err)
	}
	defer cleanupPapers(t, s, paperID)

	for _, tc := range []struct {
		scheme, id string
		want       bool
	}{
		{"arxiv", "2401.92006", true},
		{"arxiv", "arXiv:2401.92006v5", true}, // normalized to the bare id
		{"arxiv", "2401.00000", false},
		{"doi", "10.9999/qatlas.test.92006", true},
		{"doi", "https://doi.org/10.9999/QAtlas.Test.92006", true},
		{"doi", "10.9999/nope", false},
		{"openalex", "W92006", true},
		{"openalex", "W00000", false},
		{"isbn", "whatever", false}, // unknown scheme
		{"arxiv", "", false},
	} {
		got, err := s.IsHosted(ctx, tc.scheme, tc.id)
		if err != nil {
			t.Fatalf("IsHosted(%q, %q): %v", tc.scheme, tc.id, err)
		}
		if got != tc.want {
			t.Errorf("IsHosted(%q, %q) = %v, want %v", tc.scheme, tc.id, got, tc.want)
		}
	}
}

// TestIntegrationUpsertByDOITwinAttach covers the DOI twin-attach: a DOI
// contribution carrying the OpenAlex-linked arXiv id must land on the
// existing arXiv paper (ResolveOrMint merge), and the published asset
// becomes the default.
func TestIntegrationUpsertByDOITwinAttach(t *testing.T) {
	s, ctx := migrateStore(t)

	const arxiv = "2401.92007"
	const doi = "10.9999/qatlas.test.92007"
	arxivPaper, _, err := s.UpsertPDF(ctx, PaperRef{ArxivID: arxiv + "v1"}, 1,
		"", 100, "2401/2401.92007v1.pdf")
	if err != nil {
		t.Fatalf("UpsertPDF: %v", err)
	}
	defer cleanupPapers(t, s, arxivPaper)

	// DOI contribution with both identities: same paper, published asset.
	paperID, assetID, err := s.UpsertPDFByDOI(ctx, PaperRef{DOI: doi, ArxivID: arxiv}, "cafef00d", 999,
		"doi/10.9999/qatlas.test.92007.pdf")
	if err != nil {
		t.Fatalf("UpsertPDFByDOI: %v", err)
	}
	if paperID != arxivPaper {
		t.Errorf("UpsertPDFByDOI paper = %q, want twin-attach to %q", paperID, arxivPaper)
	}
	var defaultAsset int64
	if err := s.pool.QueryRow(ctx,
		`SELECT default_asset_id FROM papers WHERE paper_id = $1`, paperID).Scan(&defaultAsset); err != nil {
		t.Fatalf("default_asset_id: %v", err)
	}
	if defaultAsset != assetID {
		t.Errorf("default_asset_id = %d, want published asset %d (published-first policy)", defaultAsset, assetID)
	}

	// MD by DOI lands on the same published asset.
	if err := s.UpsertMDByDOI(ctx, PaperRef{DOI: doi}, "", 0,
		"doi/10.9999/qatlas.test.92007.md", "doi/10.9999/qatlas.test.92007.json", 3); err != nil {
		t.Fatalf("UpsertMDByDOI: %v", err)
	}
	var mdPath *string
	var imageCount *int
	if err := s.pool.QueryRow(ctx,
		`SELECT mineru_md_path, image_count FROM paper_assets WHERE asset_id = $1`, assetID,
	).Scan(&mdPath, &imageCount); err != nil {
		t.Fatalf("published asset: %v", err)
	}
	if mdPath == nil || imageCount == nil || *imageCount != 3 {
		t.Errorf("published asset md = %v image_count = %v", mdPath, imageCount)
	}

	// Invalid DOI is rejected before touching the catalog.
	if _, _, err := s.UpsertPDFByDOI(ctx, PaperRef{DOI: "not-a-doi"}, "", 0, "x.pdf"); err == nil {
		t.Error("UpsertPDFByDOI with invalid doi: want error")
	}
}

// TestIntegrationSyncFromStore reconciles a fake bucket listing into the
// registry: pdf objects mint papers through ResolveOrMint, markdown
// attaches to the synced assets, and dry-run writes nothing. The images
// bucket is no longer listed (image_count comes from upload metadata).
func TestIntegrationSyncFromStore(t *testing.T) {
	s, ctx := migrateStore(t)

	fake := &fakeListStore{infos: []objstore.ObjectInfo{
		{Key: "pdf/2401/2401.92008v1.pdf"},
		{Key: "markdown/2401/2401.92008v1.md"},
		{Key: "images/2401/2401.92008v1/fig1.png"}, // ignored: no images stage
		{Key: "images/2401/2401.92008v1/fig2.png"},
		{Key: "pdf/doi/10.9999/qatlas.test.92009.pdf"},
		{Key: "markdown/doi/10.9999/qatlas.test.92009.md"},
		{Key: "pdf/2401/2401.92010v2.pdf"}, // pdf-only paper
	}}
	syncer := NewSyncer(s)

	// Dry run: counts reported, nothing written.
	rep, err := syncer.SyncFromStore(ctx, fake, SyncOptions{DryRun: true})
	if err != nil {
		t.Fatalf("dry-run SyncFromStore: %v", err)
	}
	if rep.PDFObjects != 3 || rep.MDObjects != 2 {
		t.Errorf("dry-run report = %+v", rep)
	}
	if rep.PapersTouched != 3+2 { // 3 pdf + 2 md
		t.Errorf("dry-run PapersTouched = %d", rep.PapersTouched)
	}
	if found, _ := s.IsHosted(ctx, "arxiv", "2401.92008"); found {
		t.Fatal("dry-run wrote to the registry")
	}

	rep, err = syncer.SyncFromStore(ctx, fake, SyncOptions{})
	if err != nil {
		t.Fatalf("SyncFromStore: %v", err)
	}
	if rep.PapersTouched == 0 {
		t.Error("PapersTouched = 0 after real sync")
	}

	// arXiv paper minted via ResolveOrMint, asset carries pdf + md.
	pid, found, err := s.LookupByIdentity(ctx, ArxivKey("2401.92008"))
	if err != nil || !found {
		t.Fatalf("arxiv paper not minted: found=%v err=%v", found, err)
	}
	defer cleanupPapers(t, s, pid)
	var mdPath *string
	var imageCount *int
	if err := s.pool.QueryRow(ctx, `
		SELECT mineru_md_path, image_count FROM paper_assets
		WHERE paper_id = $1 AND source = 'arxiv' AND arxiv_version = 1`, pid,
	).Scan(&mdPath, &imageCount); err != nil {
		t.Fatalf("synced arxiv asset: %v", err)
	}
	if mdPath == nil || *mdPath != "2401/2401.92008v1.md" {
		t.Errorf("synced md path = %v", mdPath)
	}
	// image_count is NOT populated from bucket listings anymore — it
	// stays NULL until a MinerU upload reports the true count.
	if imageCount != nil {
		t.Errorf("synced image_count = %v, want NULL (no images stage)", *imageCount)
	}

	// DOI paper minted, published asset carries pdf + md.
	pidDOI, found, err := s.LookupByIdentity(ctx, DOIKey("10.9999/qatlas.test.92009"))
	if err != nil || !found {
		t.Fatalf("doi paper not minted: found=%v err=%v", found, err)
	}
	defer cleanupPapers(t, s, pidDOI)
	var doiMD *string
	if err := s.pool.QueryRow(ctx, `
		SELECT mineru_md_path FROM paper_assets
		WHERE paper_id = $1 AND source = 'published'`, pidDOI,
	).Scan(&doiMD); err != nil {
		t.Fatalf("synced doi asset: %v", err)
	}
	if doiMD == nil || *doiMD != "doi/10.9999/qatlas.test.92009.md" {
		t.Errorf("synced doi md path = %v", doiMD)
	}

	// pdf-only paper lands in the needs-mineru queue.
	pidOnly, found, err := s.LookupByIdentity(ctx, ArxivKey("2401.92010"))
	if err != nil || !found {
		t.Fatalf("pdf-only paper not minted: found=%v err=%v", found, err)
	}
	defer cleanupPapers(t, s, pidOnly)
	rows, err := s.NeedsMineru(ctx, 100)
	if err != nil {
		t.Fatalf("NeedsMineru: %v", err)
	}
	seen := false
	for _, r := range rows {
		if r.PaperID == pidOnly {
			seen = true
		}
		if r.PaperID == pid {
			t.Errorf("md-complete synced paper %q still in needs-mineru", pid)
		}
	}
	if !seen {
		t.Error("pdf-only synced paper missing from needs-mineru")
	}

	// Second run is idempotent (same papers, no duplicates).
	if _, err := syncer.SyncFromStore(ctx, fake, SyncOptions{}); err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM paper_assets WHERE paper_id = $1`, pid).Scan(&n); err != nil {
		t.Fatalf("asset count: %v", err)
	}
	if n != 1 {
		t.Errorf("asset count after re-sync = %d, want 1", n)
	}
}

// TestIntegrationSyncFromStorePartialFailure proves the staged pipeline
// isolates shard failures: shard pdf/2502/ fails to list (no child dirs,
// so drill-down can't decompose it), yet shard pdf/2501/'s paper is
// merged into the registry, the failure is recorded in the report, and
// SyncFromStore returns a non-nil error AFTER the good shards ran.
func TestIntegrationSyncFromStorePartialFailure(t *testing.T) {
	shrinkBackoff(t)
	s, ctx := migrateStore(t)

	boom := errors.New("rustfs: InternalError Io error: timeout")
	fake := &fakeListStore{
		infos: []objstore.ObjectInfo{
			{Key: "pdf/2501/2501.92010v1.pdf"},
			{Key: "pdf/2502/2502.92020v1.pdf"},
		},
		failPrefix: "pdf/2502/",
		failErr:    boom,
		failTimes:  -1,
	}
	syncer := NewSyncer(s)

	rep, err := syncer.SyncFromStore(ctx, fake, SyncOptions{})
	if err == nil {
		t.Fatal("SyncFromStore with a failed shard should return an error")
	}
	if len(rep.FailedShards) != 1 || rep.FailedShards[0].Shard != "pdf/2502/" {
		t.Errorf("FailedShards = %+v, want one entry for pdf/2502/", rep.FailedShards)
	}
	if rep.ShardsDone != 1 || rep.ShardsTotal != 2 {
		t.Errorf("ShardsDone/Total = %d/%d, want 1/2", rep.ShardsDone, rep.ShardsTotal)
	}

	// The good shard merged despite the failure.
	pid, found, err := s.LookupByIdentity(ctx, ArxivKey("2501.92010"))
	if err != nil || !found {
		t.Fatalf("good-shard paper not minted: found=%v err=%v", found, err)
	}
	defer cleanupPapers(t, s, pid)

	// The failed shard's paper was NOT minted.
	if _, found, err := s.LookupByIdentity(ctx, ArxivKey("2502.92020")); err != nil || found {
		t.Errorf("failed-shard paper should be absent: found=%v err=%v", found, err)
	}
}

// TestIntegrationSyncFromStoreShardRangeAndKinds covers SyncOptions
// filtering: --kinds=pdf skips markdown, --shard-range=2502:
// selects only the open-ended tail of numeric shards while the doi
// pseudo-shard still runs with its kind.
func TestIntegrationSyncFromStoreShardRangeAndKinds(t *testing.T) {
	s, ctx := migrateStore(t)

	fake := &fakeListStore{infos: []objstore.ObjectInfo{
		{Key: "pdf/2501/2501.92030v1.pdf"},
		{Key: "pdf/2502/2502.92040v1.pdf"},
		{Key: "pdf/doi/10.9999/qatlas.test.92050.pdf"},
		{Key: "markdown/2501/2501.92030v1.md"},
		{Key: "images/2501/2501.92030v1/fig1.png"},
	}}
	syncer := NewSyncer(s)

	rep, err := syncer.SyncFromStore(ctx, fake, SyncOptions{
		Kinds:     []string{"pdf"},
		ShardFrom: "2502",
	})
	if err != nil {
		t.Fatalf("SyncFromStore: %v", err)
	}
	// pdf shards selected: 2502 + doi (2501 excluded by range).
	if rep.ShardsTotal != 2 || rep.ShardsDone != 2 {
		t.Errorf("ShardsDone/Total = %d/%d, want 2/2", rep.ShardsDone, rep.ShardsTotal)
	}
	if rep.PDFObjects != 2 || rep.MDObjects != 0 {
		t.Errorf("report = %+v, want 2 pdf objects and no md", rep)
	}

	// In-range pdf + doi papers minted…
	pidRange, found, err := s.LookupByIdentity(ctx, ArxivKey("2502.92040"))
	if err != nil || !found {
		t.Fatalf("in-range paper not minted: found=%v err=%v", found, err)
	}
	defer cleanupPapers(t, s, pidRange)
	pidDOI, found, err := s.LookupByIdentity(ctx, DOIKey("10.9999/qatlas.test.92050"))
	if err != nil || !found {
		t.Fatalf("doi paper not minted: found=%v err=%v", found, err)
	}
	defer cleanupPapers(t, s, pidDOI)

	// …but the out-of-range shard's paper was skipped.
	if _, found, err := s.LookupByIdentity(ctx, ArxivKey("2501.92030")); err != nil || found {
		t.Errorf("out-of-range paper should be absent: found=%v err=%v", found, err)
	}
}
