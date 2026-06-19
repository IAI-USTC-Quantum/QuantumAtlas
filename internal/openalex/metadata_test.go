package openalex

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
)

// stubBodyWithAuthors is an OpenAlex Work JSON with authors + an arxiv
// location, used to exercise LookupMetadata's full extraction.
const stubBodyWithAuthors = `{
  "id": "https://openalex.org/W12345",
  "doi": "https://doi.org/10.1103/PhysRevLett.103.150502",
  "title": "Quantum algorithm for linear systems of equations",
  "authorships": [
    {"author": {"display_name": "Aram W. Harrow"}, "raw_author_name": "A. W. Harrow"},
    {"author": {"display_name": ""}, "raw_author_name": "Avinatan Hassidim"},
    {"author": {"display_name": "Seth Lloyd"}}
  ],
  "locations": [
    {"landing_page_url": "https://arxiv.org/abs/0811.3171", "pdf_url": ""}
  ]
}`

// stubBodyPublishedOnly has authors + title but NO arxiv presence — the
// published-only case ResolveDOI rejects but LookupMetadata must accept.
const stubBodyPublishedOnly = `{
  "id": "https://openalex.org/W99999",
  "doi": "https://doi.org/10.1038/nature12345",
  "title": "A purely published result",
  "authorships": [
    {"author": {"display_name": "Jane Roe"}}
  ],
  "locations": [
    {"landing_page_url": "https://www.nature.com/articles/nature12345", "pdf_url": ""}
  ]
}`

func TestLookupMetadata_Success(t *testing.T) {
	t.Parallel()
	srv := stubOpenAlex(t, func(w http.ResponseWriter, r *http.Request) {
		assertMailtoPresent(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubBodyWithAuthors))
	})
	defer srv.Close()

	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
	meta, err := r.LookupMetadata(context.Background(), "10.1103/PhysRevLett.103.150502")
	if err != nil {
		t.Fatalf("LookupMetadata: %v", err)
	}
	if meta.Title != "Quantum algorithm for linear systems of equations" {
		t.Errorf("title: got %q", meta.Title)
	}
	want := []string{"Aram W. Harrow", "Avinatan Hassidim", "Seth Lloyd"}
	if len(meta.Authors) != len(want) {
		t.Fatalf("authors: got %v, want %v", meta.Authors, want)
	}
	for i := range want {
		if meta.Authors[i] != want[i] {
			t.Errorf("author[%d]: got %q, want %q", i, meta.Authors[i], want[i])
		}
	}
	if meta.ArxivID != "0811.3171" {
		t.Errorf("arxiv: got %q, want 0811.3171", meta.ArxivID)
	}
}

// LookupMetadata must succeed for published-only works (no arxiv), where
// ResolveDOI returns ErrDOINotFound.
func TestLookupMetadata_PublishedOnly(t *testing.T) {
	t.Parallel()
	srv := stubOpenAlex(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubBodyPublishedOnly))
	})
	defer srv.Close()

	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})

	meta, err := r.LookupMetadata(context.Background(), "10.1038/nature12345")
	if err != nil {
		t.Fatalf("LookupMetadata published-only: %v", err)
	}
	if meta.Title != "A purely published result" || len(meta.Authors) != 1 {
		t.Errorf("unexpected meta: %+v", meta)
	}
	if meta.ArxivID != "" {
		t.Errorf("arxiv: got %q, want empty", meta.ArxivID)
	}

	// Same DOI via ResolveDOI must report not-found (no arxiv presence).
	if _, err := r.ResolveDOI(context.Background(), "10.1038/nature12345"); !errors.Is(err, ErrDOINotFound) {
		t.Errorf("ResolveDOI: got %v, want ErrDOINotFound", err)
	}
}

func TestLookupMetadata_NotConfigured(t *testing.T) {
	t.Parallel()
	r := New(Config{})
	if _, err := r.LookupMetadata(context.Background(), "10.1/x"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("got %v, want ErrNotConfigured", err)
	}
}

func TestLookupMetadata_NotFound(t *testing.T) {
	t.Parallel()
	srv := stubOpenAlex(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer srv.Close()
	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
	if _, err := r.LookupMetadata(context.Background(), "10.1/missing"); !errors.Is(err, ErrDOINotFound) {
		t.Fatalf("got %v, want ErrDOINotFound", err)
	}
}

// TestLookupMetadata_PositiveCache: a second lookup for the same DOI
// within the TTL must be served from cache (one upstream hit).
func TestLookupMetadata_PositiveCache(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	srv := stubOpenAlex(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubBodyWithAuthors))
	})
	defer srv.Close()
	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
	for i := 0; i < 4; i++ {
		if _, err := r.LookupMetadata(context.Background(), "10.1103/PhysRevLett.103.150502"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("upstream hits: got %d, want 1 (cached)", got)
	}
}

// TestLookupMetadata_NegativeCache: ErrDOINotFound is cached briefly so
// repeated uploads of an unknown DOI don't hammer OpenAlex.
func TestLookupMetadata_NegativeCache(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	srv := stubOpenAlex(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	})
	defer srv.Close()
	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
	for i := 0; i < 4; i++ {
		if _, err := r.LookupMetadata(context.Background(), "10.9999/missing"); !errors.Is(err, ErrDOINotFound) {
			t.Fatalf("call %d: got %v, want ErrDOINotFound", i, err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("upstream hits: got %d, want 1 (negative cached)", got)
	}
}

// TestLookupMetadata_DoesNotShareCacheWithResolveDOI: the "meta:" key
// namespace keeps a metadata lookup and an arxiv resolution of the same
// DOI in separate slots — populating one must not satisfy the other.
func TestLookupMetadata_DoesNotShareCacheWithResolveDOI(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	srv := stubOpenAlex(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubBodyWithAuthors))
	})
	defer srv.Close()
	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
	if _, err := r.LookupMetadata(context.Background(), "10.1103/PhysRevLett.103.150502"); err != nil {
		t.Fatalf("LookupMetadata: %v", err)
	}
	if _, err := r.ResolveDOI(context.Background(), "10.1103/PhysRevLett.103.150502"); err != nil {
		t.Fatalf("ResolveDOI: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("upstream hits: got %d, want 2 (separate cache slots)", got)
	}
}
