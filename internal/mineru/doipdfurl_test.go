package mineru

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
)

func TestDOIPDFURLAsyncTokenIndependentAndDeduped(t *testing.T) {
	store := newFakeStore()
	oa, hits := newFakeArxivServer(t, fakePDFBytes)
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverterWithFetcher(t, store, stub.url(), oa, "")
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	c.cfg.DOIPDFURL = func(ctx context.Context, doi string) (string, error) {
		if doi != testDOI {
			t.Errorf("DOI not normalized: %s", doi)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("discovery job not timeout bounded")
		}
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-release:
		}
		return oa + "paper.pdf", nil
	}
	request, cancel := context.WithCancel(context.Background())
	first := c.EnsurePDFByDOI(request, strings.ToUpper(testDOI), "")
	cancel()
	if first.State != JobStateQueued {
		t.Fatalf("discovery was synchronous/unavailable: %+v", first)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("discovery not started")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j := c.EnsurePDFByDOI(context.Background(), testDOI, "")
			if j.State != JobStateQueued && j.State != JobStateRunning {
				t.Errorf("not sharing discovery: %+v", j)
			}
		}()
	}
	wg.Wait()
	close(release)
	if !waitForJobState(c, doiJobKey(testDOI), JobStateDone, 2*time.Second) {
		j, _ := c.LookupDOI(testDOI)
		t.Fatalf("discovery/freeze: %+v", j)
	}
	job, _ := c.LookupDOI(testDOI)
	if calls.Load() != 1 || hits.load() != 1 || stub.submissions.load() != 0 || job.Convert != nil || job.RevisionID != "" {
		t.Fatalf("PDF-only side effects calls=%d hits=%d job=%+v", calls.Load(), hits.load(), job)
	}
	pdf, ok := store.get(paperbundle.PDFKey(job.PaperID, job.SourceID))
	if !ok || !bytes.Equal(pdf, fakePDFBytes) {
		t.Fatal("PDF not frozen unchanged")
	}
	if _, ok := store.get(paperassets.DOIAssetKey("pdf", testDOI)); ok {
		t.Fatal("fresh discovery wrote legacy PDF")
	}
	if next := c.EnsurePDFByDOI(context.Background(), testDOI, ""); next.State != JobStateDone || calls.Load() != 1 {
		t.Fatal("frozen source rediscovered")
	}
}

func TestDOIPDFURLContentIntentParsesAfterDiscovery(t *testing.T) {
	store := newFakeStore()
	oa, hits := newFakeArxivServer(t, fakePDFBytes)
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverterWithFetcher(t, store, stub.url(), oa)
	var calls atomic.Int32
	c.cfg.DOIPDFURL = func(context.Context, string) (string, error) { calls.Add(1); return oa + "paper.pdf", nil }
	j := c.EnsureByDOI(context.Background(), testDOI, "")
	if j.State != JobStateQueued {
		t.Fatalf("initial content intent: %+v", j)
	}
	if !waitForJobState(c, doiJobKey(testDOI), JobStateDone, 2*time.Second) {
		j, _ = c.LookupDOI(testDOI)
		t.Fatalf("content intent failed: %+v", j)
	}
	assertCompleteStored(t, c, store, doiJobKey(testDOI))
	if calls.Load() != 1 || hits.load() != 1 || stub.submissions.load() != 1 {
		t.Fatal("discovery and current V1 parsing not shared")
	}
}

func TestDOIPDFURLNoOAAndArxivAreNoSourceNotSubstitution(t *testing.T) {
	for _, url := range []string{"", "https://arxiv.org/pdf/2401.12345", "https://export.arxiv.org/pdf/2401.12345", "not a URL"} {
		t.Run(url, func(t *testing.T) {
			store := newFakeStore()
			stub := newMinerUStub(t)
			defer stub.close()
			c := makeConverterWithFetcher(t, store, stub.url(), "http://unused/", "")
			c.cfg.DOIPDFURL = func(context.Context, string) (string, error) { return url, nil }
			j := c.EnsurePDFByDOI(context.Background(), testDOI, "")
			if j.State != JobStateQueued {
				t.Fatal("discovery did not use async state")
			}
			if !waitForJobState(c, doiJobKey(testDOI), JobStateFailed, time.Second) {
				t.Fatal("missing OA did not fail")
			}
			j, _ = c.LookupDOI(testDOI)
			if !errors.Is(j.Err, ErrNoDOISource) || !errors.Is(j.ErrKind, ErrFatal) || len(store.objects) != 0 || stub.submissions.load() != 0 {
				t.Fatalf("no OA falsely substituted/minted: %+v", j)
			}
		})
	}
}

func TestDOIPDFURLCallbackErrorsRemainRetryable(t *testing.T) {
	store := newFakeStore()
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverterWithFetcher(t, store, stub.url(), "http://unused/", "")
	failure := errors.New("discovery unavailable")
	c.cfg.DOIPDFURL = func(context.Context, string) (string, error) { return "", failure }
	c.EnsurePDFByDOI(context.Background(), testDOI, "")
	if !waitForJobState(c, doiJobKey(testDOI), JobStateFailed, time.Second) {
		t.Fatal("callback failure did not finish")
	}
	job, _ := c.LookupDOI(testDOI)
	if !errors.Is(job.Err, failure) || !errors.Is(job.ErrKind, ErrRetryable) || errors.Is(job.Err, ErrNoDOISource) {
		t.Fatalf("discovery outage treated as absent OA: %+v", job)
	}
}

func TestVerifyDOIPDFFailureCannotFreezeOrParse(t *testing.T) {
	for _, contentIntent := range []bool{false, true} {
		t.Run(map[bool]string{false: "PDF-only", true: "content-intent"}[contentIntent], func(t *testing.T) {
			store := newFakeStore()
			oa, hits := newFakeArxivServer(t, fakePDFBytes)
			stub := newMinerUStub(t)
			defer stub.close()
			c := makeConverterWithFetcher(t, store, stub.url(), oa)
			c.cfg.DOIPDFURL = func(context.Context, string) (string, error) { return oa + "paper.pdf", nil }
			failure := errors.New("publisher ownership mismatch")
			var verifies atomic.Int32
			c.cfg.VerifyDOIPDF = func(ctx context.Context, doi string, pdf []byte) error {
				verifies.Add(1)
				if doi != testDOI || !bytes.Equal(pdf, fakePDFBytes) {
					t.Error("verification did not receive exact source bytes/identity")
				}
				return failure
			}
			if contentIntent {
				c.EnsureByDOI(t.Context(), testDOI, "")
			} else {
				c.EnsurePDFByDOI(t.Context(), testDOI, "")
			}
			if !waitForJobState(c, doiJobKey(testDOI), JobStateFailed, time.Second) {
				t.Fatal("verification failure did not finish")
			}
			j, _ := c.LookupDOI(testDOI)
			cat := c.sources.(*fakeSourceCatalog)
			if !errors.Is(j.Err, ErrFatal) || !errors.Is(j.Err, failure) || !errors.Is(j.ErrKind, ErrFatal) || verifies.Load() != 1 || hits.load() != 1 || len(store.objects) != 0 || len(cat.sources) != 0 || len(cat.bundles) != 0 || stub.submissions.load() != 0 {
				t.Fatalf("rejected DOI frozen/inferred/published: %+v", j)
			}
		})
	}
}

func TestVerifiedFrozenDOIPDFNeverRevalidates(t *testing.T) {
	store := newFakeStore()
	oa, _ := newFakeArxivServer(t, fakePDFBytes)
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverterWithFetcher(t, store, stub.url(), oa, "")
	c.cfg.DOIPDFURL = func(context.Context, string) (string, error) { return oa + "paper.pdf", nil }
	var verifies atomic.Int32
	c.cfg.VerifyDOIPDF = func(context.Context, string, []byte) error { verifies.Add(1); return nil }
	c.EnsurePDFByDOI(t.Context(), testDOI, "")
	if !waitForJobState(c, doiJobKey(testDOI), JobStateDone, time.Second) {
		t.Fatal("verified fetch did not freeze")
	}
	c.cfg.DOIPDFURL = func(context.Context, string) (string, error) {
		t.Error("frozen source rediscovered")
		return "", errors.New("network unavailable")
	}
	c.cfg.VerifyDOIPDF = func(context.Context, string, []byte) error {
		t.Error("frozen source reverified over network")
		return errors.New("network unavailable")
	}
	j := c.EnsurePDFByDOI(t.Context(), testDOI, "")
	if j.State != JobStateDone || verifies.Load() != 1 {
		t.Fatal("cached source depended on fresh discovery/verification")
	}
}

func TestDOIPDFURLBypassesExistingFrozenAndUnavailableStorage(t *testing.T) {
	t.Run("frozen missing never discovery or legacy fallback", func(t *testing.T) {
		store := newFakeStore()
		stub := newMinerUStub(t)
		defer stub.close()
		c := makeConverterWithFetcher(t, store, stub.url(), "http://unused/", "")
		var calls atomic.Int32
		c.cfg.DOIPDFURL = func(context.Context, string) (string, error) {
			calls.Add(1)
			return "https://arxiv.org/pdf/2401.12345", nil
		}
		cat := c.sources.(*fakeSourceCatalog)
		src, err := cat.RegisterFrozenPDF(t.Context(), store, "paper_test", "doi:"+testDOI, fakePDFBytes)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cat.BindPaperSourceImport(t.Context(), store, src.PaperID, src.SourceID, paperassets.DOIAssetKey("pdf", testDOI)); err != nil {
			t.Fatal(err)
		}
		if err := store.Delete(t.Context(), src.ObjstoreKey); err != nil {
			t.Fatal(err)
		}
		store.put(paperassets.DOIAssetKey("pdf", testDOI), fakePDFBytes)
		j := c.EnsurePDFByDOI(t.Context(), testDOI, "")
		if j.State != JobStateFailed || calls.Load() != 0 || len(c.jobs) != 0 {
			t.Fatalf("frozen identity repaired/substituted: %+v", j)
		}
	})
	t.Run("storage outage observational", func(t *testing.T) {
		store := &failedCacheProbe{Store: newFakeStore(), err: errors.New("stat unavailable")}
		stub := newMinerUStub(t)
		defer stub.close()
		c := makeConverterWithFetcher(t, store, stub.url(), "http://unused/", "")
		var calls atomic.Int32
		c.cfg.DOIPDFURL = func(context.Context, string) (string, error) { calls.Add(1); return "", nil }
		j := c.EnsurePDFByDOI(t.Context(), testDOI, "")
		if j.State != JobStateFailed || !errors.Is(j.Err, objstore.ErrUnavailable) || calls.Load() != 0 || len(c.jobs) != 0 {
			t.Fatal("storage outage caused discovery")
		}
	})
	t.Run("invalid DOI", func(t *testing.T) {
		c := makeConverterWithFetcher(t, newFakeStore(), "http://unused", "http://unused/", "")
		var calls atomic.Int32
		c.cfg.DOIPDFURL = func(context.Context, string) (string, error) { calls.Add(1); return "", nil }
		j := c.EnsurePDFByDOI(t.Context(), "invalid", "")
		if j.State != JobStateFailed || calls.Load() != 0 {
			t.Fatal("invalid DOI discovered")
		}
	})
}
