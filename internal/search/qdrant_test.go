package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/qdrant/go-client/qdrant"
)

// qdrantStrValue builds a string payload value.
func qdrantStrValue(s string) *qdrant.Value {
	return &qdrant.Value{Kind: &qdrant.Value_StringValue{StringValue: s}}
}

// qdrantListValue builds a string-list payload value.
func qdrantListValue(items ...string) *qdrant.Value {
	vals := make([]*qdrant.Value, len(items))
	for i, s := range items {
		vals[i] = qdrantStrValue(s)
	}
	return &qdrant.Value{Kind: &qdrant.Value_ListValue{ListValue: &qdrant.ListValue{Values: vals}}}
}

// qdrantChunk builds a scored chunk point for one paper.
func qdrantChunk(score float32, arxivID, title, yymm string, authors ...string) *qdrant.ScoredPoint {
	payload := map[string]*qdrant.Value{
		"arxiv_id":   qdrantStrValue(arxivID),
		"title":      qdrantStrValue(title),
		"chunk_text": qdrantStrValue("some chunk of the paper"),
	}
	if yymm != "" {
		payload["yymm"] = qdrantStrValue(yymm)
	}
	if len(authors) > 0 {
		payload["authors"] = qdrantListValue(authors...)
	}
	return &qdrant.ScoredPoint{Score: score, Payload: payload}
}

func TestQdrantSearchEmptyTextNoOp(t *testing.T) {
	// No embed / qdrant clients wired: any network attempt would panic,
	// so a clean (nil, nil) proves the identity-only short-circuit.
	p := &QdrantProvider{}
	for _, e := range []SearchEntry{
		{DOI: "10.1234/foo"},
		{ArxivID: "2401.12345"},
		{DOI: "10.1234/foo", ArxivID: "2401.12345"},
		{},
	} {
		hits, err := p.Search(context.Background(), e)
		if err != nil || hits != nil {
			t.Errorf("Search(%+v) = (%v, %v), want (nil, nil)", e, hits, err)
		}
	}
}

func TestQdrantPointToHit(t *testing.T) {
	pt := qdrantChunk(0.87, "arXiv:2401.12345v2", "Quantum Error Correction", "2401", "Alice", "Bob")
	h := qdrantPointToHit(pt)
	if h.ArxivID != "2401.12345" {
		t.Errorf("ArxivID = %q, want version-stripped %q", h.ArxivID, "2401.12345")
	}
	if h.Title != "Quantum Error Correction" {
		t.Errorf("Title = %q", h.Title)
	}
	if h.Year != 2024 {
		t.Errorf("Year = %d, want 2024 (from yymm 2401)", h.Year)
	}
	if !slices.Equal(h.Authors, []string{"Alice", "Bob"}) {
		t.Errorf("Authors = %v", h.Authors)
	}
	if h.Score != float64(float32(0.87)) {
		t.Errorf("Score = %v, want 0.87", h.Score)
	}
	if h.Source != "qdrant" {
		t.Errorf("Source = %q, want qdrant", h.Source)
	}

	// DOI normalization when the payload carries one.
	pt.Payload["doi"] = qdrantStrValue("https://doi.org/10.1234/QEC.2024")
	if got := qdrantPointToHit(pt).DOI; got != "10.1234/qec.2024" {
		t.Errorf("DOI = %q, want normalized %q", got, "10.1234/qec.2024")
	}
}

func TestCollapseQdrantHits(t *testing.T) {
	points := []*qdrant.ScoredPoint{
		qdrantChunk(0.9, "2401.00001v1", "Paper A", "2401"),
		qdrantChunk(0.8, "2401.00001v2", "Paper A", "2401"), // same paper, lower score
		qdrantChunk(0.7, "2401.00002v1", "Paper B", "2401"),
		qdrantChunk(0.95, "2401.00001v3", "Paper A", "2401"), // same paper, best score
	}
	hits := collapseQdrantHits(points, 10)
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2 (chunks of one paper collapse)", len(hits))
	}
	if hits[0].ArxivID != "2401.00001" || hits[0].Score != float64(float32(0.95)) {
		t.Errorf("hit[0] = %+v, want arxiv 2401.00001 with max score 0.95", hits[0])
	}
	if hits[1].ArxivID != "2401.00002" {
		t.Errorf("hit[1] = %+v, want arxiv 2401.00002", hits[1])
	}
}

func TestCollapseQdrantHitsCap(t *testing.T) {
	var points []*qdrant.ScoredPoint
	for _, id := range []string{"2401.00001", "2401.00002", "2401.00003"} {
		points = append(points, qdrantChunk(0.5, id, "T "+id, "2401"))
	}
	if hits := collapseQdrantHits(points, 2); len(hits) != 2 {
		t.Fatalf("got %d hits, want cap at 2", len(hits))
	}
}

func TestCollapseQdrantHitsSkipsIdentityLess(t *testing.T) {
	points := []*qdrant.ScoredPoint{
		{Score: 0.9, Payload: map[string]*qdrant.Value{"chunk_text": qdrantStrValue("orphan chunk")}},
		qdrantChunk(0.5, "2401.00001", "Paper A", "2401"),
	}
	hits := collapseQdrantHits(points, 10)
	if len(hits) != 1 || hits[0].ArxivID != "2401.00001" {
		t.Fatalf("hits = %+v, want only the identified paper", hits)
	}
}

func TestEmbedClientEmbed(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		gotAuth = r.Header.Get("authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
			"dense": [[0.1, 0.2, 0.3]],
			"sparse": [{"indices": [5, 7], "values": [0.9, 0.4]}]
		}`))
	}))
	defer srv.Close()

	c := newEmbedClient(srv.URL, "tok")
	dense, idx, val, err := c.embed(context.Background(), "hello", true)
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if gotPath != "/embed?lane=query" {
		t.Errorf("path = %q, want /embed?lane=query", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("authorization = %q", gotAuth)
	}
	if gotBody["return_sparse"] != true {
		t.Errorf("return_sparse = %v, want true", gotBody["return_sparse"])
	}
	if !slices.Equal(dense, []float32{0.1, 0.2, 0.3}) {
		t.Errorf("dense = %v", dense)
	}
	if !slices.Equal(idx, []uint32{5, 7}) || !slices.Equal(val, []float32{0.9, 0.4}) {
		t.Errorf("sparse = %v/%v", idx, val)
	}
}

func TestEmbedClientEmbedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := newEmbedClient(srv.URL, "")
	if _, _, _, err := c.embed(context.Background(), "x", true); err == nil {
		t.Fatal("embed on 500 returned nil error")
	}
}

func TestEmbedClientRerank(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"scores": [0.3, 0.9]}`))
	}))
	defer srv.Close()

	c := newEmbedClient(srv.URL, "")
	scores, err := c.rerank(context.Background(), "q", []string{"p1", "p2"})
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if !slices.Equal(scores, []float32{0.3, 0.9}) {
		t.Errorf("scores = %v", scores)
	}
	if gotBody["query"] != "q" {
		t.Errorf("query = %v", gotBody["query"])
	}
}

func TestParseQdrantURL(t *testing.T) {
	cases := []struct {
		raw    string
		host   string
		port   int
		useTLS bool
	}{
		{"qdrant.local", "qdrant.local", 6334, false},
		{"qdrant.local:6334", "qdrant.local", 6334, false},
		{"http://qdrant.local:6333", "qdrant.local", 6333, false},
		{"https://qdrant.example.com", "qdrant.example.com", 6334, true},
	}
	for _, c := range cases {
		host, port, useTLS, err := parseQdrantURL(c.raw)
		if err != nil {
			t.Errorf("parseQdrantURL(%q): %v", c.raw, err)
			continue
		}
		if host != c.host || port != c.port || useTLS != c.useTLS {
			t.Errorf("parseQdrantURL(%q) = (%q, %d, %v), want (%q, %d, %v)",
				c.raw, host, port, useTLS, c.host, c.port, c.useTLS)
		}
	}
	if _, _, _, err := parseQdrantURL("host:notaport"); err == nil {
		t.Error("parseQdrantURL with non-numeric port returned nil error")
	}
}
