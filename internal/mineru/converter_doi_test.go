package mineru

// Tests for the DOI fetch+convert pipeline (converter_doi.go, plan §A):
// EnsureByDOI fetches an OA PDF URL when no DOI-keyed PDF is stored,
// converts a stored contributed PDF without fetching, fails fast with
// ErrNoDOISource when neither exists, and short-circuits on a markdown
// cache hit.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
)

const testDOI = "10.1038/s41534-020-00001-0"

func TestEnsureByDOI_FetchThenConvert(t *testing.T) {
	store := newFakeStore()
	// No PDF pre-seeded → forces the OA fetch.

	oaURL, hits := newFakeArxivServer(t, fakePDFBytes)
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverterWithFetcher(t, store, stub.url(), oaURL)

	job := c.EnsureByDOI(context.Background(), testDOI, oaURL+"paper.pdf")
	if job.State != JobStateQueued {
		t.Fatalf("initial EnsureByDOI state = %v, want queued", job.State)
	}
	if job.Canonical != testDOI {
		t.Errorf("job.Canonical = %q, want %q", job.Canonical, testDOI)
	}

	if !waitForJobState(c, doiJobKey(testDOI), JobStateDone, 5*time.Second) {
		final, _ := c.LookupDOI(testDOI)
		t.Fatalf("job did not reach Done; final = %+v", final)
	}

	// PDF must have landed under the DOI layout.
	pdfKey := paperassets.DOIAssetKey("pdf", testDOI)
	if pdf, ok := store.get(pdfKey); !ok {
		t.Errorf("PDF not written to store at %q", pdfKey)
	} else if !strings.HasPrefix(string(pdf), "%PDF-") {
		t.Errorf("stored PDF missing %%PDF- magic; got %q", string(pdf)[:8])
	}
	// Markdown + images zip under the DOI layout too.
	if _, ok := store.get(paperassets.DOIAssetKey("markdown", testDOI)); !ok {
		t.Errorf("markdown not written to store")
	}
	if _, ok := store.get(paperassets.DOIAssetKey("images", testDOI)); !ok {
		t.Errorf("images zip not written to store")
	}
	if hits.load() != 1 {
		t.Errorf("OA host hits = %d, want exactly 1", hits.load())
	}

	final, _ := c.LookupDOI(testDOI)
	if final.Phase != PhaseReady {
		t.Errorf("final Phase = %q, want %q", final.Phase, PhaseReady)
	}
	if final.Fetch == nil || final.Fetch.Sha256 == "" {
		t.Errorf("Fetch progress not populated: %+v", final.Fetch)
	}
	if final.Convert == nil {
		t.Errorf("Convert progress not populated")
	}
	// MinerU must have seen a .pdf upload name and the doi: data id.
	stub.mu.Lock()
	gotDataID := stub.lastDataID
	stub.mu.Unlock()
	if gotDataID != doiJobKey(testDOI) {
		t.Errorf("stub.lastDataID = %q, want %q", gotDataID, doiJobKey(testDOI))
	}
}

func TestEnsureByDOI_ConvertStoredPDFWithoutFetch(t *testing.T) {
	store := newFakeStore()
	store.put(paperassets.DOIAssetKey("pdf", testDOI), fakePDFBytes)

	oaURL, hits := newFakeArxivServer(t, fakePDFBytes)
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverterWithFetcher(t, store, stub.url(), oaURL)

	// Empty OA URL: convert the contributed PDF only.
	job := c.EnsureByDOI(context.Background(), testDOI, "")
	if job.State != JobStateQueued {
		t.Fatalf("initial state = %v, want queued", job.State)
	}
	if !waitForJobState(c, doiJobKey(testDOI), JobStateDone, 5*time.Second) {
		final, _ := c.LookupDOI(testDOI)
		t.Fatalf("job did not reach Done; final = %+v", final)
	}
	if hits.load() != 0 {
		t.Errorf("OA host hits = %d, want 0 (PDF was already stored)", hits.load())
	}
	if _, ok := store.get(paperassets.DOIAssetKey("markdown", testDOI)); !ok {
		t.Errorf("markdown not written to store")
	}
	final, _ := c.LookupDOI(testDOI)
	if final.Fetch != nil {
		t.Errorf("Fetch should be nil when no fetch happened, got %+v", final.Fetch)
	}
}

func TestEnsureByDOI_NoSourceIsFatal404Material(t *testing.T) {
	store := newFakeStore()
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverterWithFetcher(t, store, stub.url(), "http://unused/")

	job := c.EnsureByDOI(context.Background(), testDOI, "")
	if job.State != JobStateQueued {
		t.Fatalf("initial state = %v, want queued", job.State)
	}
	if !waitForJobState(c, doiJobKey(testDOI), JobStateFailed, 5*time.Second) {
		t.Fatal("job did not fail")
	}
	final, _ := c.LookupDOI(testDOI)
	if !errors.Is(final.Err, ErrNoDOISource) {
		t.Errorf("Err = %v, want ErrNoDOISource", final.Err)
	}
	if !errors.Is(final.ErrKind, ErrFatal) {
		t.Errorf("ErrKind = %v, want ErrFatal (404 semantics, not 502)", final.ErrKind)
	}
	if stub.submissions.load() != 0 {
		t.Errorf("mineru submissions = %d, want 0 (no PDF, no submission)", stub.submissions.load())
	}
}

func TestEnsureByDOI_CacheHitShortCircuits(t *testing.T) {
	store := newFakeStore()
	store.put(paperassets.DOIAssetKey("markdown", testDOI), []byte("# cached"))

	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverter(t, store, stub.url())

	job := c.EnsureByDOI(context.Background(), testDOI, "https://example.com/oa.pdf")
	if job.State != JobStateDone {
		t.Fatalf("State = %v, want Done (cache hit)", job.State)
	}
	if job.Phase != PhaseReady {
		t.Errorf("Phase = %q, want ready", job.Phase)
	}
	if stub.submissions.load() != 0 {
		t.Errorf("stub.submissions = %d, want 0", stub.submissions.load())
	}
}

func TestEnsureByDOI_InvalidDOI(t *testing.T) {
	store := newFakeStore()
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverter(t, store, stub.url())

	job := c.EnsureByDOI(context.Background(), "not-a-doi", "")
	if job.State != JobStateFailed {
		t.Fatalf("State = %v, want Failed", job.State)
	}
	if !errors.Is(job.ErrKind, ErrFatal) {
		t.Errorf("ErrKind = %v, want ErrFatal", job.ErrKind)
	}
}

func TestLookupDOI_NormalizesInput(t *testing.T) {
	store := newFakeStore()
	oaURL, _ := newFakeArxivServer(t, fakePDFBytes)
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverterWithFetcher(t, store, stub.url(), oaURL)

	// Queue a job, then look it up via an un-normalized (mixed-case)
	// spelling — ValidateDOI lower-cases, so both agree on the key.
	c.EnsureByDOI(context.Background(), testDOI, oaURL+"paper.pdf")
	upper := strings.ToUpper(testDOI)
	if _, ok := c.LookupDOI(upper); !ok {
		t.Errorf("LookupDOI(%q) missed the job queued under %q", upper, testDOI)
	}
	if _, ok := c.LookupDOI("garbage"); ok {
		t.Errorf("LookupDOI(garbage) should be (nil,false)")
	}
}
