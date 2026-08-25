package arxiv

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// metadataFeedFixture is a 3-entry Atom reply: one modern id (versioned
// in the entry URL, with a DOI), one old-style id, and one minimal entry
// with no DOI and a single author.
const metadataFeedFixture = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:arxiv="http://arxiv.org/schemas/atom">
  <entry>
    <id>http://arxiv.org/abs/2401.12345v2</id>
    <title>  Quantum   Error
	Correction  </title>
    <published>2024-01-23T00:00:00Z</published>
    <summary>  We study quantum
	error correction.  </summary>
    <arxiv:doi>10.48550/FOO.2024.001</arxiv:doi>
    <author><name>Jane Doe</name></author>
    <author><name> John Smith </name></author>
  </entry>
  <entry>
    <id>http://arxiv.org/abs/quant-ph/9508027v1</id>
    <title>Old Style Paper</title>
    <published>1995-08-27T00:00:00Z</published>
    <summary>Old summary.</summary>
    <author><name>Alice Old</name></author>
    <author><name>Bob Older</name></author>
  </entry>
  <entry>
    <id>http://arxiv.org/abs/1101.0001</id>
    <title>No DOI Here</title>
    <published>2011-01-01T00:00:00Z</published>
    <summary>No doi summary.</summary>
    <author><name>Solo Author</name></author>
  </entry>
</feed>`

func TestFetchMetadataBatch(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		if ua := r.Header.Get("User-Agent"); ua != metadataUserAgent {
			t.Errorf("User-Agent = %q, want %q", ua, metadataUserAgent)
		}
		w.Header().Set("Content-Type", "application/atom+xml")
		fmt.Fprint(w, metadataFeedFixture)
	}))
	defer srv.Close()

	// "2401.99999" is not in the fixture: missing ids stay absent from
	// the map. Version suffixes on input are stripped before the request.
	got, err := fetchMetadataBatch(context.Background(), srv.Client(), srv.URL,
		[]string{"2401.12345v2", "quant-ph/9508027", "1101.0001", "2401.99999"})
	if err != nil {
		t.Fatalf("fetchMetadataBatch: %v", err)
	}
	if !strings.Contains(gotQuery, "id_list=2401.12345") {
		t.Errorf("request query %q does not version-strip 2401.12345v2", gotQuery)
	}

	if len(got) != 3 {
		t.Fatalf("len(map) = %d, want 3 (requested-but-missing id must be absent)", len(got))
	}
	if _, ok := got["2401.99999"]; ok {
		t.Error("map contains the id arXiv did not return")
	}

	m := got["2401.12345"]
	if m.Title != "Quantum Error Correction" {
		t.Errorf("title = %q, want whitespace-collapsed", m.Title)
	}
	if len(m.Authors) != 2 || m.Authors[0] != "Jane Doe" || m.Authors[1] != "John Smith" {
		t.Errorf("authors = %v", m.Authors)
	}
	if m.Published.Year() != 2024 || m.Published.Month() != 1 || m.Published.Day() != 23 {
		t.Errorf("published = %v", m.Published)
	}
	if m.Abstract != "We study quantum error correction." {
		t.Errorf("abstract = %q", m.Abstract)
	}
	if m.DOI != "10.48550/foo.2024.001" {
		t.Errorf("doi = %q, want normalized lowercase", m.DOI)
	}

	if old := got["quant-ph/9508027"]; old.Title != "Old Style Paper" || len(old.Authors) != 2 {
		t.Errorf("old-style entry = %+v", old)
	}
	if nodoi := got["1101.0001"]; nodoi.DOI != "" {
		t.Errorf("doi-less entry DOI = %q, want empty", nodoi.DOI)
	}
}

func TestFetchMetadataBatchEmpty(t *testing.T) {
	got, err := FetchMetadataBatch(context.Background(), nil, nil)
	if err != nil || len(got) != 0 {
		t.Errorf("empty ids = %v, %v; want empty map, nil", got, err)
	}
}

func TestFetchMetadataBatchHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	if _, err := fetchMetadataBatch(context.Background(), srv.Client(), srv.URL, []string{"2401.12345"}); err == nil {
		t.Error("429 response: err = nil, want error")
	}
}
