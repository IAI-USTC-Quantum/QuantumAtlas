package mineru

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
)

const testDOI = "10.1038/s41534-020-00001-0"

func TestEnsureByDOI_FetchThenConvert(t *testing.T) {
	store := newFakeStore()
	oaURL, hits := newFakeArxivServer(t, fakePDFBytes)
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverterWithFetcher(t, store, stub.url(), oaURL)
	j := c.EnsureByDOI(context.Background(), testDOI, oaURL+"paper.pdf")
	if j.State != JobStateQueued {
		t.Fatalf("initial job: %+v", j)
	}
	if !waitForJobState(c, doiJobKey(testDOI), JobStateDone, 5*time.Second) {
		j, _ = c.LookupDOI(testDOI)
		t.Fatalf("final: %+v", j)
	}
	final := assertCompleteStored(t, c, store, doiJobKey(testDOI))
	if hits.load() != 1 || final.Fetch == nil || final.Convert == nil {
		t.Fatalf("fetch+parse progress missing: %+v", final)
	}
	if _, ok := store.get(paperassets.DOIAssetKey("pdf", testDOI)); ok {
		t.Fatal("fresh DOI PDF wrote legacy bucket")
	}
	if pdf, ok := store.get(paperbundle.PDFKey(final.PaperID, final.SourceID)); !ok || !strings.HasPrefix(string(pdf), "%PDF-") {
		t.Fatal("frozen PDF missing")
	}
}

func TestEnsureByDOI_ConvertStoredPDFWithoutFetch(t *testing.T) {
	store := newFakeStore()
	store.put(paperassets.DOIAssetKey("pdf", testDOI), fakePDFBytes)
	oaURL, hits := newFakeArxivServer(t, fakePDFBytes)
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverterWithFetcher(t, store, stub.url(), oaURL)
	j := c.EnsureByDOI(context.Background(), testDOI, "")
	if j.State != JobStateQueued {
		t.Fatalf("initial: %+v", j)
	}
	if !waitForJobState(c, doiJobKey(testDOI), JobStateDone, 5*time.Second) {
		j, _ = c.LookupDOI(testDOI)
		t.Fatalf("final: %+v", j)
	}
	assertCompleteStored(t, c, store, doiJobKey(testDOI))
	if hits.load() != 0 {
		t.Fatal("stored PDF refetched")
	}
}

func TestEnsureByDOI_NoSourceIsFatal404Material(t *testing.T) {
	store := newFakeStore()
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverterWithFetcher(t, store, stub.url(), "http://unused/")
	j := c.EnsureByDOI(context.Background(), testDOI, "")
	if j.State != JobStateFailed || !errors.Is(j.Err, ErrNoDOISource) || !errors.Is(j.ErrKind, ErrFatal) || stub.submissions.load() != 0 {
		t.Fatalf("missing DOI source: %+v", j)
	}
}

func TestEnsureByDOI_CacheHitShortCircuits(t *testing.T) {
	store := newFakeStore()
	store.put(paperassets.DOIAssetKey("pdf", testDOI), fakePDFBytes)
	store.put(paperassets.DOIAssetKey("markdown", testDOI), []byte("# old ignored"))
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverter(t, store, stub.url())
	first := c.EnsureByDOI(context.Background(), testDOI, "")
	if first.State != JobStateQueued {
		t.Fatalf("old MD cache accepted: %+v", first)
	}
	if !waitForJobState(c, doiJobKey(testDOI), JobStateDone, 2*time.Second) {
		t.Fatal("parse not done")
	}
	second := c.EnsureByDOI(context.Background(), testDOI, "")
	if second.State != JobStateDone || second.RevisionID == "" || stub.submissions.load() != 1 {
		t.Fatalf("verified full cache: %+v", second)
	}
}

func TestEnsureByDOI_InvalidDOI(t *testing.T) {
	c := makeConverter(t, newFakeStore(), "http://unused/")
	j := c.EnsureByDOI(context.Background(), "not-a-doi", "")
	if j.State != JobStateFailed || !errors.Is(j.ErrKind, ErrFatal) {
		t.Fatalf("invalid DOI: %+v", j)
	}
}

func TestLookupDOI_NormalizesInput(t *testing.T) {
	store := newFakeStore()
	oaURL, _ := newFakeArxivServer(t, fakePDFBytes)
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverterWithFetcher(t, store, stub.url(), oaURL)
	c.EnsureByDOI(context.Background(), testDOI, oaURL+"paper.pdf")
	if _, ok := c.LookupDOI(strings.ToUpper(testDOI)); !ok {
		t.Fatal("normalized lookup missed")
	}
	if _, ok := c.LookupDOI("garbage"); ok {
		t.Fatal("invalid lookup hit")
	}
	if !waitForJobState(c, doiJobKey(testDOI), JobStateDone, 2*time.Second) {
		t.Fatal("lookup job did not finish")
	}
}
