package openalex

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// batchFixture is a 2-work OpenAlex list reply: one full work (inverted
// abstract index + an arXiv location + a versioned arXiv URL) and one
// minimal work (no abstract, no arXiv presence).
const batchFixture = `{
  "results": [
    {
      "id": "https://openalex.org/W201",
      "doi": "https://doi.org/10.1103/PhysRevLett.103.150502",
      "title": "Quantum algorithm for linear systems of equations",
      "publication_date": "2009-10-23",
      "abstract_inverted_index": {
        "We": [0],
        "present": [1],
        "a": [2],
        "quantum": [3],
        "algorithm": [4],
        "for": [5],
        "linear": [6],
        "systems": [7]
      },
      "authorships": [
        {"author": {"display_name": "Aram W. Harrow"}},
        {"author": {"display_name": ""}, "raw_author_name": "Avinatan Hassidim"},
        {"author": {"display_name": "Seth Lloyd"}}
      ],
      "locations": [
        {"landing_page_url": "https://journals.aps.org/prl/abstract/10.1103/PhysRevLett.103.150502"},
        {"landing_page_url": "http://arxiv.org/abs/0811.3171v2"}
      ]
    },
    {
      "id": "https://openalex.org/W202",
      "doi": "https://doi.org/10.1038/nature12345",
      "title": "A purely published result",
      "publication_date": "2013-05-01",
      "authorships": [
        {"author": {"display_name": "Jane Roe"}}
      ],
      "locations": [
        {"landing_page_url": "https://www.nature.com/articles/nature12345"}
      ]
    }
  ]
}`

func TestFetchWorksByDOIBatch(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		if ua := r.Header.Get("User-Agent"); ua != "qatlasd-backfill (mailto:ops@example.com)" {
			t.Errorf("User-Agent = %q", ua)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, batchFixture)
	}))
	defer srv.Close()

	// "10.9999/absent" is not in the fixture: unknown DOIs stay absent
	// from the map. URL-form input is normalized before the request.
	got, err := fetchWorksByDOIBatch(context.Background(), srv.Client(), srv.URL, "ops@example.com",
		[]string{"https://doi.org/10.1103/PhysRevLett.103.150502", "10.1038/nature12345", "10.9999/absent"})
	if err != nil {
		t.Fatalf("fetchWorksByDOIBatch: %v", err)
	}

	// Request shape: one OR-DOI filter, bare normalized DOIs, mailto
	// polite-pool param, per-page covering the batch.
	filter := gotQuery.Get("filter")
	if !strings.HasPrefix(filter, "doi:") ||
		!strings.Contains(filter, "10.1103/physrevlett.103.150502") ||
		!strings.Contains(filter, "|") ||
		!strings.Contains(filter, "10.1038/nature12345") {
		t.Errorf("filter = %q, want doi:<bare1>|<bare2>", filter)
	}
	if gotQuery.Get("mailto") != "ops@example.com" {
		t.Errorf("mailto param = %q", gotQuery.Get("mailto"))
	}
	if gotQuery.Get("per-page") != "3" {
		t.Errorf("per-page = %q, want batch size 3", gotQuery.Get("per-page"))
	}

	if len(got) != 2 {
		t.Fatalf("len(map) = %d, want 2 (unknown DOI must be absent)", len(got))
	}
	if _, ok := got["10.9999/absent"]; ok {
		t.Error("map contains the DOI OpenAlex did not return")
	}

	m := got["10.1103/physrevlett.103.150502"] // map key normalized
	if m.DOI != "10.1103/physrevlett.103.150502" {
		t.Errorf("DOI = %q", m.DOI)
	}
	if m.Title != "Quantum algorithm for linear systems of equations" {
		t.Errorf("title = %q", m.Title)
	}
	if len(m.Authors) != 3 || m.Authors[0] != "Aram W. Harrow" || m.Authors[1] != "Avinatan Hassidim" {
		t.Errorf("authors = %v (raw_author_name fallback broken?)", m.Authors)
	}
	if m.PublicationDate.Year() != 2009 || m.PublicationDate.Month() != 10 || m.PublicationDate.Day() != 23 {
		t.Errorf("publication date = %v", m.PublicationDate)
	}
	if want := "We present a quantum algorithm for linear systems"; m.Abstract != want {
		t.Errorf("abstract = %q, want reconstructed %q", m.Abstract, want)
	}
	if m.ArxivID != "0811.3171" {
		t.Errorf("arxiv id = %q, want version-stripped 0811.3171", m.ArxivID)
	}

	minimal := got["10.1038/nature12345"]
	if minimal.Title != "A purely published result" || len(minimal.Authors) != 1 {
		t.Errorf("minimal work = %+v", minimal)
	}
	if minimal.Abstract != "" || minimal.ArxivID != "" {
		t.Errorf("minimal work abstract/arxiv = %q/%q, want both empty", minimal.Abstract, minimal.ArxivID)
	}
}

func TestFetchWorksByDOIBatchEmpty(t *testing.T) {
	got, err := FetchWorksByDOIBatch(context.Background(), nil, "", nil)
	if err != nil || len(got) != 0 {
		t.Errorf("empty dois = %v, %v; want empty map, nil", got, err)
	}
}

func TestFetchWorksByDOIBatchHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	if _, err := fetchWorksByDOIBatch(context.Background(), srv.Client(), srv.URL, "", []string{"10.1/x"}); err == nil {
		t.Error("429 response: err = nil, want error")
	}
}

func TestReconstructAbstract(t *testing.T) {
	if got := reconstructAbstract(nil); got != "" {
		t.Errorf("nil index = %q, want empty", got)
	}
	got := reconstructAbstract(map[string][]int{"world": {1}, "hello": {0}})
	if got != "hello world" {
		t.Errorf("reconstructed = %q, want \"hello world\"", got)
	}
}
