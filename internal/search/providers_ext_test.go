package search

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// arxivFixture is a two-entry Atom feed: the first entry carries an
// arxiv:doi, the second is an old-style id without one.
const arxivFixture = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:arxiv="http://arxiv.org/schemas/atom">
  <entry>
    <id>http://arxiv.org/abs/2401.12345v2</id>
    <title>  Quantum   Error
 Correction  </title>
    <published>2024-01-23T00:00:00Z</published>
    <summary>  Error correction   abstract. </summary>
    <author><name>Alice Smith</name></author>
    <author><name>Bob Jones</name></author>
    <arxiv:doi>10.1234/QEC.2024</arxiv:doi>
  </entry>
  <entry>
    <id>http://arxiv.org/abs/quant-ph/9508027v1</id>
    <title>Teleporting an Unknown Quantum State</title>
    <published>1995-08-01T00:00:00Z</published>
    <author><name>Carol Bennett</name></author>
  </entry>
</feed>`

// arxivServer runs handler against a provider wired to a test server.
func arxivServer(t *testing.T, handler http.HandlerFunc) (*ArxivProvider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p := NewArxivProvider(srv.Client())
	p.baseURL = srv.URL
	return p, srv
}

func TestArxivSearchByID(t *testing.T) {
	var gotIDList, gotMaxResults string
	p, _ := arxivServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotIDList = r.URL.Query().Get("id_list")
		gotMaxResults = r.URL.Query().Get("max_results")
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(arxivFixture))
	})

	hits, err := p.Search(context.Background(), SearchEntry{ArxivID: "arXiv:2401.12345v2", MaxResults: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotIDList != "2401.12345" {
		t.Errorf("id_list = %q, want version-stripped %q", gotIDList, "2401.12345")
	}
	if gotMaxResults != "5" {
		t.Errorf("max_results = %q, want 5", gotMaxResults)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}
	h := hits[0]
	if h.ArxivID != "2401.12345" {
		t.Errorf("ArxivID = %q, want version-stripped 2401.12345", h.ArxivID)
	}
	if h.DOI != "10.1234/qec.2024" {
		t.Errorf("DOI = %q, want normalized 10.1234/qec.2024", h.DOI)
	}
	if h.Title != "Quantum Error Correction" {
		t.Errorf("Title = %q, want single-space normalized", h.Title)
	}
	if h.Abstract != "Error correction abstract." {
		t.Errorf("Abstract = %q", h.Abstract)
	}
	if h.Year != 2024 {
		t.Errorf("Year = %d, want 2024", h.Year)
	}
	if len(h.Authors) != 2 || h.Authors[0] != "Alice Smith" || h.Authors[1] != "Bob Jones" {
		t.Errorf("Authors = %v", h.Authors)
	}
	if h.Score != 0.9 {
		t.Errorf("Score = %v, want 0.9 for id_list exact hit", h.Score)
	}
	if h.Source != "arxiv" {
		t.Errorf("Source = %q, want arxiv", h.Source)
	}
	if hits[1].ArxivID != "quant-ph/9508027" {
		t.Errorf("hits[1].ArxivID = %q, want quant-ph/9508027", hits[1].ArxivID)
	}
	if hits[1].DOI != "" {
		t.Errorf("hits[1].DOI = %q, want empty", hits[1].DOI)
	}
}

func TestArxivSearchByDOI(t *testing.T) {
	var gotQuery string
	p, _ := arxivServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("search_query")
		_, _ = w.Write([]byte(arxivFixture))
	})

	_, err := p.Search(context.Background(), SearchEntry{DOI: "10.48550/arXiv.2401.12345", MaxResults: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	want := `doi:"10.48550/arxiv.2401.12345"`
	if gotQuery != want {
		t.Errorf("search_query = %q, want %q", gotQuery, want)
	}
}

func TestArxivSearchByTitleParsesAtom(t *testing.T) {
	var gotQuery string
	p, _ := arxivServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("search_query")
		_, _ = w.Write([]byte(arxivFixture))
	})

	hits, err := p.Search(context.Background(), SearchEntry{Title: "Quantum Error Correction", MaxResults: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if want := `ti:"Quantum Error Correction"`; gotQuery != want {
		t.Errorf("search_query = %q, want %q", gotQuery, want)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}
	if math.Abs(hits[0].Score-0.6) > 1e-9 {
		t.Errorf("hits[0].Score = %v, want 0.6", hits[0].Score)
	}
	if math.Abs(hits[1].Score-0.59) > 1e-9 {
		t.Errorf("hits[1].Score = %v, want 0.59 (rank decay)", hits[1].Score)
	}
}

func TestArxivServerError(t *testing.T) {
	p, _ := arxivServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	hits, err := p.Search(context.Background(), SearchEntry{Title: "anything", MaxResults: 5})
	if err != nil {
		t.Fatalf("Search returned error, want contract (nil, nil): %v", err)
	}
	if hits != nil {
		t.Errorf("hits = %v, want nil", hits)
	}
	if p.LastError() == nil || !strings.Contains(p.LastError().Error(), "arxiv") {
		t.Errorf("LastError = %v, want recorded arxiv failure", p.LastError())
	}
}

// openalexFixture: first result has a doi.org DOI, an arXiv location, and
// a relevance score; the second has neither DOI nor score.
const openalexFixture = `{
  "results": [
    {
      "title": "Quantum Error Correction",
      "publication_year": 2024,
      "doi": "https://doi.org/10.1234/QEC.2024",
      "relevance_score": 75.5,
      "abstract_inverted_index": {"Quantum": [0], "abstract": [1]},
      "authorships": [
        {"author": {"display_name": "Alice Smith"}},
        {"author": {}, "raw_author_name": "Bob Jones"}
      ],
      "locations": [
        {"landing_page_url": "https://arxiv.org/abs/2401.12345v2"},
        {"landing_page_url": "https://publisher.example/qec"}
      ]
    },
    {
      "title": "Another Paper",
      "publication_year": 2020,
      "doi": null,
      "authorships": [],
      "locations": []
    }
  ]
}`

func openalexServer(t *testing.T, mailto string, handler http.HandlerFunc) *OpenAlexProvider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p := NewOpenAlexProvider(srv.Client(), mailto)
	p.baseURL = srv.URL
	return p
}

func TestOpenAlexSearchByText(t *testing.T) {
	var gotSearch, gotPerPage, gotMailto string
	p := openalexServer(t, "team@example.org", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotSearch, gotPerPage, gotMailto = q.Get("search"), q.Get("per-page"), q.Get("mailto")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(openalexFixture))
	})

	hits, err := p.Search(context.Background(), SearchEntry{Text: "quantum error correction", MaxResults: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotSearch != "quantum error correction" {
		t.Errorf("search = %q", gotSearch)
	}
	if gotPerPage != "5" {
		t.Errorf("per-page = %q, want 5", gotPerPage)
	}
	if gotMailto != "team@example.org" {
		t.Errorf("mailto = %q", gotMailto)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}
	h := hits[0]
	if h.DOI != "10.1234/qec.2024" {
		t.Errorf("DOI = %q, want prefix-stripped normalized", h.DOI)
	}
	if h.ArxivID != "2401.12345" {
		t.Errorf("ArxivID = %q, want mined from locations", h.ArxivID)
	}
	if h.Abstract != "Quantum abstract" {
		t.Errorf("Abstract = %q", h.Abstract)
	}
	if h.Year != 2024 {
		t.Errorf("Year = %d, want 2024", h.Year)
	}
	if len(h.Authors) != 2 || h.Authors[0] != "Alice Smith" || h.Authors[1] != "Bob Jones" {
		t.Errorf("Authors = %v", h.Authors)
	}
	if math.Abs(h.Score-0.755) > 1e-9 {
		t.Errorf("Score = %v, want relevance 75.5/100 = 0.755", h.Score)
	}
	if h.Source != "openalex" {
		t.Errorf("Source = %q, want openalex", h.Source)
	}
	if hits[1].Score != 0.5 {
		t.Errorf("hits[1].Score = %v, want 0.5 without relevance_score", hits[1].Score)
	}
}

func TestOpenAlexSearchByDOI(t *testing.T) {
	var gotFilter, gotMailto string
	p := openalexServer(t, "team@example.org", func(w http.ResponseWriter, r *http.Request) {
		gotFilter, gotMailto = r.URL.Query().Get("filter"), r.URL.Query().Get("mailto")
		_, _ = w.Write([]byte(openalexFixture))
	})

	hits, err := p.Search(context.Background(), SearchEntry{DOI: "https://doi.org/10.1234/QEC.2024", MaxResults: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if want := "doi:10.1234/qec.2024"; gotFilter != want {
		t.Errorf("filter = %q, want %q", gotFilter, want)
	}
	if gotMailto != "team@example.org" {
		t.Errorf("mailto = %q", gotMailto)
	}
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
}

func TestOpenAlexArxivOnlyEntryYieldsNoHits(t *testing.T) {
	called := false
	p := openalexServer(t, "", func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_, _ = w.Write([]byte(openalexFixture))
	})

	hits, err := p.Search(context.Background(), SearchEntry{ArxivID: "2401.12345", MaxResults: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if hits != nil {
		t.Errorf("hits = %v, want nil (no arXiv-id lookup upstream)", hits)
	}
	if called {
		t.Error("upstream called for arXiv-only entry; want no request")
	}
	if p.LastError() != nil {
		t.Errorf("LastError = %v, want nil (unsupported is not a failure)", p.LastError())
	}
}

func TestOpenAlexServerError(t *testing.T) {
	p := openalexServer(t, "", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	hits, err := p.Search(context.Background(), SearchEntry{Text: "anything", MaxResults: 5})
	if err != nil {
		t.Fatalf("Search returned error, want contract (nil, nil): %v", err)
	}
	if hits != nil {
		t.Errorf("hits = %v, want nil", hits)
	}
	if p.LastError() == nil || !strings.Contains(p.LastError().Error(), "openalex") {
		t.Errorf("LastError = %v, want recorded openalex failure", p.LastError())
	}
}
