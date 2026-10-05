package mineru

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

const testMiddle = `{"schema":"docvortex.middle","schema_version":"2.0","pdf_info":{"pages":1},"blocks":[{"page_idx":0,"index":1,"type":"text","content":"hello","bbox":[0,0,1,1]}]}`

func buildCompleteZip(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, body := range map[string]string{"markdown.md": "# Hello\n", "middle_json.json": testMiddle, "structured_content.json": "{\"schema\":\"consumption\"}", "images/a.png": "PNGDATA", "unknown/model.bin": "\x00raw\xff"} {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type fakeSourceCatalog struct {
	mu           sync.Mutex
	sources      map[string]registry.PaperSource
	imports      map[string]string
	bundles      map[string]registry.ParseBundle
	publishErr   error
	publishCount int
}

func newFakeSourceCatalog() *fakeSourceCatalog {
	return &fakeSourceCatalog{sources: map[string]registry.PaperSource{}, imports: map[string]string{}, bundles: map[string]registry.ParseBundle{}}
}
func (f *fakeSourceCatalog) GetImportedPaperSource(_ context.Context, paperID, key string) (registry.PaperSource, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.imports[paperID+":"+key]
	return f.sources[id], ok, nil
}
func (f *fakeSourceCatalog) BindPaperSourceImport(ctx context.Context, store objstore.Store, paperID, sourceID, key string) (registry.PaperSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	src := f.sources[sourceID]
	if _, err := paperbundle.New(store).ReadPDF(ctx, paperID, sourceID, src.Sha256, src.SizeBytes); err != nil {
		return src, err
	}
	f.imports[paperID+":"+key] = sourceID
	return src, nil
}
func (f *fakeSourceCatalog) ResolveOrMint(context.Context, registry.PaperRef) (string, bool, error) {
	return "paper_test", false, nil
}
func (f *fakeSourceCatalog) ListPaperSources(_ context.Context, paperID string) ([]registry.PaperSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []registry.PaperSource
	for _, s := range f.sources {
		if s.PaperID == paperID {
			out = append(out, s)
		}
	}
	return out, nil
}
func (f *fakeSourceCatalog) GetPaperSource(_ context.Context, paperID, sourceID string) (registry.PaperSource, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sources[sourceID]
	return s, ok && s.PaperID == paperID, nil
}
func (f *fakeSourceCatalog) FreezePaperSource(ctx context.Context, store objstore.Store, s registry.PaperSource) (registry.PaperSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s = f.sources[s.SourceID]
	if s.ObjstoreKey == paperbundle.PDFKey(s.PaperID, s.SourceID) {
		_, err := paperbundle.New(store).ReadPDF(ctx, s.PaperID, s.SourceID, s.Sha256, s.SizeBytes)
		return s, err
	}
	r, _, err := store.Get(ctx, s.ObjstoreKey)
	if err != nil {
		return s, err
	}
	b, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		return s, err
	}
	pdf, err := paperbundle.New(store).FreezePDF(ctx, s.PaperID, s.SourceID, b, s.Sha256)
	if err != nil {
		return s, err
	}
	s.ObjstoreKey = pdf.Key
	s.SizeBytes = pdf.SizeBytes
	f.sources[s.SourceID] = s
	return s, nil
}
func (f *fakeSourceCatalog) RegisterFrozenPaperSource(ctx context.Context, store objstore.Store, paperID, origin, key string) (registry.PaperSource, error) {
	r, _, err := store.Get(ctx, key)
	if err != nil {
		return registry.PaperSource{}, err
	}
	b, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		return registry.PaperSource{}, err
	}
	src, err := f.RegisterFrozenPDF(ctx, store, paperID, origin, b)
	if err != nil {
		return src, err
	}
	return f.BindPaperSourceImport(ctx, store, paperID, src.SourceID, key)
}
func (f *fakeSourceCatalog) RegisterFrozenPDF(ctx context.Context, store objstore.Store, paperID, origin string, b []byte) (registry.PaperSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sha := paperbundle.SHA256(b)
	for _, s := range f.sources {
		if s.PaperID == paperID && s.Sha256 == sha {
			return s, nil
		}
	}
	s := registry.PaperSource{PaperID: paperID, SourceID: registry.NewSourceID(), Origin: origin, Sha256: sha, SizeBytes: int64(len(b))}
	pdf, err := paperbundle.New(store).FreezePDF(ctx, paperID, s.SourceID, b, sha)
	if err != nil {
		return s, err
	}
	s.ObjstoreKey = pdf.Key
	f.sources[s.SourceID] = s
	return s, nil
}
func (f *fakeSourceCatalog) GetReadyParseBundle(ctx context.Context, store objstore.Store, paperID, sourceID string) (registry.ParseBundle, bool, error) {
	f.mu.Lock()
	b, ok := f.bundles[sourceID]
	f.mu.Unlock()
	if !ok {
		return b, false, nil
	}
	_, err := paperbundle.New(store).VerifyBundle(ctx, paperID, sourceID, b.RevisionID)
	if errors.Is(err, paperbundle.ErrIntegrity) || errors.Is(err, objstore.ErrNotFound) {
		return b, false, nil
	}
	return b, err == nil, err
}
func (f *fakeSourceCatalog) PublishBundle(ctx context.Context, store objstore.Store, rev registry.ParseRevision, current bool) (registry.ParseBundle, error) {
	m, err := paperbundle.New(store).VerifyBundle(ctx, rev.PaperID, rev.SourceID, rev.RevisionID)
	if err != nil {
		return registry.ParseBundle{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.publishErr != nil {
		return registry.ParseBundle{}, f.publishErr
	}
	rev.IsCurrent = current
	b := registry.ParseBundle{ParseRevision: rev, SourcePDFSHA256: m.SourcePDFSHA256, ManifestKey: paperbundle.ManifestKey(rev.PaperID, rev.SourceID, rev.RevisionID), MiddlePath: m.MiddlePath, MarkdownPath: m.MarkdownPath}
	f.bundles[rev.SourceID] = b
	f.publishCount++
	return b, nil
}

func assertCompleteStored(t *testing.T, c *Converter, store *fakeStore, alias string) *Job {
	t.Helper()
	j, ok := c.Lookup(alias)
	if !ok || j.State != JobStateDone {
		t.Fatalf("job not done: %+v", j)
	}
	m, err := paperbundle.New(store).VerifyBundle(context.Background(), j.PaperID, j.SourceID, j.RevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 5 {
		t.Fatalf("full member package missing: %+v", m)
	}
	if b, ok := store.get(paperbundle.FileKey(j.PaperID, j.SourceID, j.RevisionID, "unknown/model.bin")); !ok || !bytes.Equal(b, []byte("\x00raw\xff")) {
		t.Fatal("unknown member bytes not preserved")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for key := range store.objects {
		if len(key) >= 9 && (key[:9] == "markdown/" || key[:7] == "images/" || key[:7] == "parses/") {
			t.Fatalf("legacy output written: %s", key)
		}
	}
	return j
}

func TestSourcePDFDOIHelperWithoutTokensAndCatalogAccessor(t *testing.T) {
	store := newFakeStore()
	oaURL, hits := newFakeArxivServer(t, fakePDFBytes)
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverterWithFetcher(t, store, stub.url(), oaURL, "")
	if c.Enabled() {
		t.Fatal("empty token unexpectedly enabled inference")
	}
	if c.SourceCatalog() != c.sources || (*Converter)(nil).SourceCatalog() != nil {
		t.Fatal("catalog accessor contract")
	}
	j := c.EnsurePDFByDOI(context.Background(), testDOI, oaURL+"paper.pdf")
	if j.State != JobStateQueued {
		t.Fatalf("PDF-only fetch: %+v", j)
	}
	if !waitForJobState(c, doiJobKey(testDOI), JobStateDone, 2*time.Second) {
		t.Fatal("token-independent PDF fetch did not finish")
	}
	frozen, err := c.ResolveFrozenDOISource(context.Background(), testDOI)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.ObjstoreKey != paperbundle.PDFKey(frozen.PaperID, frozen.SourceID) || hits.load() != 1 || stub.submissions.load() != 0 {
		t.Fatal("PDF-only helper parsed or used legacy bytes")
	}
	j, _ = c.LookupDOI(testDOI)
	if j.Convert != nil || j.RevisionID != "" {
		t.Fatal("PDF-only job acquired a parse revision")
	}
}

func TestSourceFreshFetchReadySHAKeepsStatusAfterRestart(t *testing.T) {
	store := newFakeStore()
	stub := newMinerUStub(t)
	defer stub.close()
	firstProcess := makeConverter(t, store, stub.url())
	cat := firstProcess.sources.(*fakeSourceCatalog)
	src, err := cat.RegisterFrozenPDF(context.Background(), store, "paper_test", "upload", fakePDFBytes)
	if err != nil {
		t.Fatal(err)
	}
	firstProcess.EnsureSource(context.Background(), src.PaperID, src.SourceID)
	if !waitForJobState(firstProcess, sourceAlias(src.PaperID, src.SourceID), JobStateDone, 2*time.Second) {
		t.Fatal("initial parse failed")
	}
	arxivURL, hits := newFakeArxivServer(t, fakePDFBytes)
	restarted := makeConverterWithFetcher(t, store, stub.url(), arxivURL)
	restarted.sources = cat
	j := restarted.Ensure(context.Background(), "2401.12345v1")
	if j.State != JobStateQueued {
		t.Fatalf("initial fetch: %+v", j)
	}
	if !waitForJobState(restarted, "2401.12345v1", JobStateDone, 2*time.Second) {
		current, found := restarted.Lookup("2401.12345v1")
		t.Fatalf("ready cache lost async status: %+v %v", current, found)
	}
	if hits.load() != 1 || stub.submissions.load() != 1 {
		t.Fatal("already parsed frozen SHA inferred again")
	}
}

func TestSourceCanonicalAliasSurvivesLegacyDeletionAndOldOrigin(t *testing.T) {
	store := newFakeStore()
	legacyKey := "pdf/9508/9508027v2.pdf"
	store.put(legacyKey, fakePDFBytes)
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverter(t, store, stub.url())
	cat := c.sources.(*fakeSourceCatalog)
	src, err := cat.RegisterFrozenPDF(context.Background(), store, "paper_test", "upload", fakePDFBytes)
	if err != nil {
		t.Fatal(err)
	}
	first := c.Ensure(context.Background(), "quant-ph/9508027v2")
	if first.SourceID != src.SourceID {
		t.Fatal("same SHA minted a replacement source")
	}
	if !waitForJobState(c, "quant-ph/9508027v2", JobStateDone, 2*time.Second) {
		t.Fatal("initial parse failed")
	}
	if err := store.Delete(context.Background(), legacyKey); err != nil {
		t.Fatal(err)
	}
	second := c.Ensure(context.Background(), "quant-ph/9508027v2")
	if second.State != JobStateDone || second.SourceID != src.SourceID || stub.submissions.load() != 1 {
		t.Fatalf("canonical alias fell back after freeze: %+v", second)
	}
	store.put(legacyKey, fakePDFBytes)
	store.put(paperbundle.PDFKey(src.PaperID, src.SourceID), []byte("corrupt"))
	failed := c.Ensure(context.Background(), "quant-ph/9508027v2")
	if failed.State != JobStateFailed || stub.submissions.load() != 1 {
		t.Fatalf("frozen corruption used legacy bytes: %+v", failed)
	}
}

func TestSourcePDFOnlyDoesNotRequireTokenOrInfer(t *testing.T) {
	store := newFakeStore()
	cat := newFakeSourceCatalog()
	legacy := "pdf/legacy.pdf"
	store.put(legacy, fakePDFBytes)
	src := registry.PaperSource{PaperID: "paper_test", SourceID: "src_original", Origin: "upload", Sha256: paperbundle.SHA256(fakePDFBytes), SizeBytes: int64(len(fakePDFBytes)), ObjstoreKey: legacy}
	cat.sources[src.SourceID] = src
	c := NewConverter(ConverterConfig{PaperAccessEnabled: true, SourceCatalog: cat}, store, nil, nil)
	frozen, err := c.EnsureFrozenSource(context.Background(), src.PaperID, src.SourceID)
	if err != nil || frozen.SourceID != src.SourceID || frozen.ObjstoreKey != paperbundle.PDFKey(src.PaperID, src.SourceID) {
		t.Fatalf("freeze: %+v %v", frozen, err)
	}
	if c.Enabled() || c.Snapshot().Submitted != 0 || len(c.jobs) != 0 {
		t.Fatal("PDF access triggered inference")
	}
	store.put(legacy, []byte("%PDF-mutated"))
	store.put(frozen.ObjstoreKey, []byte("%PDF-corrupt"))
	if _, err = c.EnsureFrozenSource(context.Background(), src.PaperID, src.SourceID); !errors.Is(err, paperbundle.ErrIntegrity) {
		t.Fatalf("frozen corruption fell back: %v", err)
	}
}

func TestSourceConcurrentEnsureUsesOneFullParseAndRepairsNewRevision(t *testing.T) {
	store := newFakeStore()
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverter(t, store, stub.url())
	cat := c.sources.(*fakeSourceCatalog)
	src, err := cat.RegisterFrozenPDF(context.Background(), store, "paper_test", "upload", fakePDFBytes)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.EnsureSource(context.Background(), src.PaperID, src.SourceID) }()
	}
	wg.Wait()
	alias := sourceAlias(src.PaperID, src.SourceID)
	if !waitForJobState(c, alias, JobStateDone, 2*time.Second) {
		j, _ := c.Lookup(alias)
		t.Fatalf("final: %+v", j)
	}
	first := assertCompleteStored(t, c, store, alias)
	if stub.submissions.load() != 1 {
		t.Fatal("same frozen PDF submitted more than once")
	}
	if j := c.EnsureSource(context.Background(), src.PaperID, src.SourceID); j.State != JobStateDone || j.RevisionID != first.RevisionID {
		t.Fatalf("valid cache missed: %+v", j)
	}
	if err := store.Delete(context.Background(), paperbundle.FileKey(src.PaperID, src.SourceID, first.RevisionID, "unknown/model.bin")); err != nil {
		t.Fatal(err)
	}
	second := c.EnsureSource(context.Background(), src.PaperID, src.SourceID)
	if second.State != JobStateQueued || second.RevisionID == first.RevisionID {
		t.Fatalf("partial bundle reused/stale Done trusted: %+v", second)
	}
	if !waitForJobState(c, alias, JobStateDone, 2*time.Second) {
		j, _ := c.Lookup(alias)
		t.Fatalf("repair: %+v", j)
	}
	assertCompleteStored(t, c, store, alias)
	if stub.submissions.load() != 2 || cat.publishCount != 2 {
		t.Fatal("new repair revision not published")
	}
	if _, ok := store.get(paperbundle.FileKey(src.PaperID, src.SourceID, first.RevisionID, "unknown/model.bin")); ok {
		t.Fatal("old revision overwritten during repair")
	}
}

type failedMemberStore struct{ *fakeStore }

func (s *failedMemberStore) PutWithOptions(ctx context.Context, key string, r io.Reader, size int64, opts objstore.PutOptions) (int64, error) {
	if strings.HasSuffix(key, "/unknown/model.bin") {
		return 0, errors.New("simulated member persistence outage")
	}
	return s.fakeStore.PutWithOptions(ctx, key, r, size, opts)
}
func TestSourceFailedMemberNeverPublishesReady(t *testing.T) {
	store := &failedMemberStore{newFakeStore()}
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverter(t, store, stub.url())
	cat := c.sources.(*fakeSourceCatalog)
	src, err := cat.RegisterFrozenPDF(context.Background(), store, "paper_test", "upload", fakePDFBytes)
	if err != nil {
		t.Fatal(err)
	}
	j := c.EnsureSource(context.Background(), src.PaperID, src.SourceID)
	alias := sourceAlias(src.PaperID, src.SourceID)
	if !waitForJobState(c, alias, JobStateFailed, 2*time.Second) {
		t.Fatal("member failure did not fail job")
	}
	if cat.publishCount != 0 {
		t.Fatal("failed member published current revision")
	}
	if _, ok := store.get(paperbundle.ManifestKey(src.PaperID, src.SourceID, j.RevisionID)); ok {
		t.Fatal("failed member produced complete marker")
	}
}

func TestSourcePublicationFailureNeverReturnsDone(t *testing.T) {
	store := newFakeStore()
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverter(t, store, stub.url())
	cat := c.sources.(*fakeSourceCatalog)
	cat.publishErr = errors.New("simulated registry outage")
	src, err := cat.RegisterFrozenPDF(context.Background(), store, "paper_test", "upload", fakePDFBytes)
	if err != nil {
		t.Fatal(err)
	}
	j := c.EnsureSource(context.Background(), src.PaperID, src.SourceID)
	alias := sourceAlias(src.PaperID, src.SourceID)
	if !waitForJobState(c, alias, JobStateFailed, 2*time.Second) {
		t.Fatal("publication failure did not fail job")
	}
	if _, err := paperbundle.New(store).VerifyBundle(context.Background(), src.PaperID, src.SourceID, j.RevisionID); err != nil {
		t.Fatal("expected orphan complete package: ", err)
	}
	if ready, found, err := cat.GetReadyParseBundle(context.Background(), store, src.PaperID, src.SourceID); err != nil || found {
		t.Fatalf("orphan treated ready: %+v %v", ready, err)
	}
}
