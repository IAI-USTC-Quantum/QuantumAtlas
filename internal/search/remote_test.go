package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// remoteFixture is an httptest server playing the qatlas-search
// microservice contract, recording the last request it saw.
type remoteFixture struct {
	srv *httptest.Server

	lastAuth   string
	lastBody   map[string]any
	statusCode int
	response   any
}

func newRemoteFixture(t *testing.T, response any) *remoteFixture {
	t.Helper()
	f := &remoteFixture{statusCode: http.StatusOK, response: response}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastAuth = r.Header.Get("Authorization")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/search" {
			f.lastBody = map[string]any{}
			_ = json.NewDecoder(r.Body).Decode(&f.lastBody)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.statusCode)
		if f.response != nil {
			_ = json.NewEncoder(w).Encode(f.response)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *remoteFixture) provider(token string) *RemoteProvider {
	return NewRemoteProvider(f.srv.URL+"/", token, 5*time.Second)
}

var remoteOKResponse = map[string]any{
	"hits": []map[string]any{
		{
			"title":    "Quantum Teleportation",
			"authors":  []string{"Bennett"},
			"year":     1993,
			"doi":      "10.1103/PhysRevLett.70.1895",
			"arxiv_id": "",
			"source":   "openalex",
			"score":    0.91,
		},
		{
			"title":    "Teleporting an Unknown Quantum State",
			"arxiv_id": "quant-ph/9301023",
			"source":   "", // filled with "remote" by the mapping
			"score":    0.5,
		},
	},
	"conclusion": "teleportation works",
	"usage":      map[string]any{"llm_tokens": 1234},
	"errors":     map[string]any{"arxiv": "timeout"},
}

func TestRemoteProvider_SearchMapsHits(t *testing.T) {
	f := newRemoteFixture(t, remoteOKResponse)
	p := f.provider("svc-token")

	hits, err := p.Search(context.Background(), SearchEntry{Text: "teleportation", MaxResults: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("len(hits) = %d, want 2", len(hits))
	}

	h := hits[0]
	if h.DOI != "10.1103/PhysRevLett.70.1895" || h.Title != "Quantum Teleportation" ||
		h.Year != 1993 || h.Source != "openalex" || h.Score != 0.91 {
		t.Errorf("hit[0] wrong: %+v", h)
	}
	if len(h.Authors) != 1 || h.Authors[0] != "Bennett" {
		t.Errorf("hit[0].Authors = %v", h.Authors)
	}
	if hits[1].Source != "remote" {
		t.Errorf("hit[1].Source = %q, want remote (empty source filled)", hits[1].Source)
	}

	// Request contract: agent=false for plain Search, query from Text,
	// max_results passed through, bearer token on the wire.
	if f.lastBody["agent"] != false {
		t.Errorf("agent = %v, want false", f.lastBody["agent"])
	}
	if f.lastBody["query"] != "teleportation" {
		t.Errorf("query = %v", f.lastBody["query"])
	}
	if f.lastBody["max_results"] != float64(10) {
		t.Errorf("max_results = %v, want 10", f.lastBody["max_results"])
	}
	if f.lastAuth != "Bearer svc-token" {
		t.Errorf("Authorization = %q, want Bearer svc-token", f.lastAuth)
	}
}

func TestRemoteProvider_SearchFailureContract(t *testing.T) {
	// Non-200 → the provider contract: (nil, nil) + RecordFailure.
	f := newRemoteFixture(t, map[string]any{"detail": "boom"})
	f.statusCode = http.StatusInternalServerError
	p := f.provider("")

	hits, err := p.Search(context.Background(), SearchEntry{Text: "x"})
	if err != nil {
		t.Fatalf("Search returned error %v, want (nil, nil) contract", err)
	}
	if hits != nil {
		t.Errorf("hits = %v, want nil", hits)
	}
	if p.LastError() == nil {
		t.Error("LastError() = nil, want the recorded backend failure")
	}
}

func TestRemoteProvider_SearchUnreachable(t *testing.T) {
	p := NewRemoteProvider("http://127.0.0.1:1", "", 500*time.Millisecond)
	hits, err := p.Search(context.Background(), SearchEntry{Text: "x"})
	if err != nil || hits != nil {
		t.Errorf("Search = (%v, %v), want (nil, nil)", hits, err)
	}
	if p.LastError() == nil {
		t.Error("LastError() = nil, want recorded dial failure")
	}
}

func TestRemoteProvider_SearchAgentic(t *testing.T) {
	f := newRemoteFixture(t, remoteOKResponse)
	p := f.provider("tok")

	resp, err := p.SearchAgentic(context.Background(), SearchEntry{Title: "bell state"}, true, nil)
	if err != nil {
		t.Fatalf("SearchAgentic: %v", err)
	}
	if resp.Conclusion == nil || *resp.Conclusion != "teleportation works" {
		t.Errorf("Conclusion = %v", resp.Conclusion)
	}
	if resp.Usage.LLMTokens != 1234 {
		t.Errorf("Usage.LLMTokens = %d, want 1234", resp.Usage.LLMTokens)
	}
	if resp.Errors["arxiv"] != "timeout" {
		t.Errorf("Errors = %v", resp.Errors)
	}
	if len(resp.Hits) != 2 {
		t.Fatalf("len(Hits) = %d, want 2", len(resp.Hits))
	}

	// agent=true is forwarded verbatim; query falls back to Title.
	if f.lastBody["agent"] != true {
		t.Errorf("agent = %v, want true", f.lastBody["agent"])
	}
	if f.lastBody["query"] != "bell state" {
		t.Errorf("query = %v, want 'bell state' (Title fallback)", f.lastBody["query"])
	}
}

func TestRemoteProvider_SearchAgenticReturnsRealErrors(t *testing.T) {
	f := newRemoteFixture(t, map[string]any{"detail": "upstream broke"})
	f.statusCode = http.StatusBadGateway
	p := f.provider("")

	if _, err := p.SearchAgentic(context.Background(), SearchEntry{Text: "x"}, true, nil); err == nil {
		t.Fatal("SearchAgentic returned nil error on 502, want a real error (refund signal)")
	}
}

func TestRemoteProvider_Healthz(t *testing.T) {
	f := newRemoteFixture(t, map[string]any{"status": "ok"})
	p := f.provider("")
	if err := p.Healthz(context.Background()); err != nil {
		t.Fatalf("Healthz: %v", err)
	}

	f.statusCode = http.StatusServiceUnavailable
	if err := p.Healthz(context.Background()); err == nil {
		t.Error("Healthz = nil on 503, want error")
	}
}
