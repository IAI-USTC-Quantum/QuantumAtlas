package openalex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestFetchWorkRecord_OpenAlexID: an openalex ref hits /works/W… (no "doi:"
// prefix) and returns the raw record + parsed work.
func TestFetchWorkRecord_OpenAlexID(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubBody))
	}))
	defer srv.Close()

	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
	raw, work, err := r.FetchWorkRecord(context.Background(), "openalex", "W12345")
	if err != nil {
		t.Fatalf("FetchWorkRecord: %v", err)
	}
	if gotPath != "/works/W12345" {
		t.Errorf("request path = %q, want /works/W12345", gotPath)
	}
	if !strings.Contains(string(raw), "W12345") {
		t.Errorf("raw record missing id: %s", raw)
	}
	if shortID(work.ID) != "W12345" {
		t.Errorf("parsed work id = %q, want W12345", shortID(work.ID))
	}
}

// TestFetchWorkRecord_DOI: a doi ref hits /works/doi:<doi> (doi lower-cased
// by normalizeDOI, "doi:" kept as an unescaped path prefix).
func TestFetchWorkRecord_DOI(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubBody))
	}))
	defer srv.Close()

	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
	_, _, err := r.FetchWorkRecord(context.Background(), "doi", "10.1103/PhysRevLett.103.150502")
	if err != nil {
		t.Fatalf("FetchWorkRecord: %v", err)
	}
	if gotPath != "/works/doi:10.1103/physrevlett.103.150502" {
		t.Errorf("request path = %q, want /works/doi:10.1103/physrevlett.103.150502", gotPath)
	}
}

// TestFetchWorkRecord_NotConfigured: no mailto → fail fast, no HTTP.
func TestFetchWorkRecord_NotConfigured(t *testing.T) {
	r := New(Config{})
	if _, _, err := r.FetchWorkRecord(context.Background(), "openalex", "W1"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("err = %v, want ErrNotConfigured", err)
	}
}

// TestFetchWorkRecord_ArxivUnsupported: arxiv is not a /works/{id} key.
func TestFetchWorkRecord_ArxivUnsupported(t *testing.T) {
	r := New(Config{Mailto: "ops@example.com"})
	if _, _, err := r.FetchWorkRecord(context.Background(), "arxiv", "2401.00001"); !errors.Is(err, ErrDOINotFound) {
		t.Errorf("err = %v, want ErrDOINotFound (arxiv unsupported)", err)
	}
}

// TestFetchWorkRecord_NotFound: upstream 404 → ErrDOINotFound.
func TestFetchWorkRecord_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
	if _, _, err := r.FetchWorkRecord(context.Background(), "openalex", "W404"); !errors.Is(err, ErrDOINotFound) {
		t.Errorf("err = %v, want ErrDOINotFound", err)
	}
}

// TestFetchWorkRecord_Coalesces: N concurrent fetches for the SAME id collapse
// to a single upstream request via the shared singleflight.Group (the same
// coalescing ResolveDOI/LookupMetadata use). The handler blocks until the
// first call is in-flight and the stragglers have had time to join, so the
// coalescing window is deterministic rather than timing-lucky.
func TestFetchWorkRecord_Coalesces(t *testing.T) {
	const n = 20
	var hits int32
	var entered sync.Once
	enteredCh := make(chan struct{})
	release := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		entered.Do(func() { close(enteredCh) })
		<-release // hold the single in-flight call open while dups pile up
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubBody))
	}))
	defer srv.Close()

	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})

	var wg sync.WaitGroup
	results := make([]Work, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, w, err := r.FetchWorkRecord(context.Background(), "openalex", "W12345")
			results[i], errs[i] = w, err
		}(i)
	}

	<-enteredCh                        // the winner is in the handler
	time.Sleep(100 * time.Millisecond) // let the other 19 register as dups
	close(release)                     // now let the single call complete
	wg.Wait()

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("upstream hits = %d, want 1 (singleflight should coalesce)", got)
	}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("caller %d: unexpected err %v", i, errs[i])
			continue
		}
		if shortID(results[i].ID) != "W12345" {
			t.Errorf("caller %d: work id = %q, want W12345", i, shortID(results[i].ID))
		}
	}
}
