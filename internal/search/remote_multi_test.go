package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// multiFixture is an httptest server speaking the microservice's
// /v1/search (mode=multi) and /v1/backends contracts, recording what
// the provider sent.
type multiFixture struct {
	lastAuth   string
	lastMethod string
	lastPath   string
	lastBody   map[string]any
	respBody   string
	respStatus int
}

func newMultiFixture(t testing.TB) (*multiFixture, *httptest.Server) {
	t.Helper()
	fx := &multiFixture{respStatus: http.StatusOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fx.lastAuth = r.Header.Get("Authorization")
		fx.lastMethod, fx.lastPath = r.Method, r.URL.Path
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		fx.lastBody = body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fx.respStatus)
		_, _ = w.Write([]byte(fx.respBody))
	}))
	t.Cleanup(srv.Close)
	return fx, srv
}

func TestRemoteSearchMulti_WireContract(t *testing.T) {
	fx, srv := newMultiFixture(t)
	fx.respBody = `{
		"results": {
			"ieee": [{"title":"A paper","source":"ieee","url":"https://ieeexplore.ieee.org/document/1"}],
			"arxiv": []
		},
		"conclusion": null,
		"usage": {"llm_tokens": 0},
		"errors": {"tavily": "HTTPError: 401"}
	}`
	p := NewRemoteProvider(srv.URL, "tok", 5*time.Second)

	resp, err := p.SearchMulti(context.Background(), SearchEntry{Text: "quantum", MaxResults: 5}, []string{"ieee", "arxiv"}, map[string]string{"ieee": "user-key"})
	if err != nil {
		t.Fatalf("SearchMulti: %v", err)
	}
	if fx.lastAuth != "Bearer tok" {
		t.Errorf("auth = %q", fx.lastAuth)
	}
	if fx.lastBody["mode"] != "multi" || fx.lastBody["query"] != "quantum" || fx.lastBody["max_results"] != float64(5) {
		t.Errorf("request body = %v", fx.lastBody)
	}
	if got := fx.lastBody["sources"].([]any); len(got) != 2 || got[0] != "ieee" {
		t.Errorf("sources = %v", fx.lastBody["sources"])
	}
	if got := fx.lastBody["api_keys"].(map[string]any); got["ieee"] != "user-key" {
		t.Errorf("api_keys = %v", fx.lastBody["api_keys"])
	}
	if len(resp.Results["ieee"]) != 1 || resp.Results["ieee"][0].Title != "A paper" {
		t.Errorf("results = %v", resp.Results)
	}
	if resp.Errors["tavily"] != "HTTPError: 401" {
		t.Errorf("errors = %v", resp.Errors)
	}
}

func TestRemoteSearchMulti_EntryWireContract(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry SearchEntry
		max   int
	}{
		{"doi only", SearchEntry{DOI: "10.1234/qa"}, DefaultMaxResults},
		{"text and doi", SearchEntry{Text: "  quantum  ", DOI: "10.1234/qa", MaxResults: 7}, 7},
		{"legacy text", SearchEntry{Text: "  quantum  ", MaxResults: 5}, 5},
		{"doi-looking text", SearchEntry{Text: "10.1234/qa"}, DefaultMaxResults},
		{"negative max", SearchEntry{DOI: "10.1234/qa", MaxResults: -1}, DefaultMaxResults},
		{"uncapped max", SearchEntry{DOI: "10.1234/qa", MaxResults: 75}, 75},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx, srv := newMultiFixture(t)
			fx.respBody = `{"results":{},"usage":{"llm_tokens":0},"errors":{}}`
			p := NewRemoteProvider(srv.URL, "tok", 5*time.Second)
			_, err := p.SearchMulti(context.Background(), tc.entry, []string{"ieee", "arxiv"}, map[string]string{"ieee": "user-key"})
			if err != nil {
				t.Fatalf("SearchMulti: %v", err)
			}
			if fx.lastMethod != http.MethodPost || fx.lastPath != "/v1/search" || fx.lastAuth != "Bearer tok" {
				t.Fatalf("request = %s %s, auth=%q", fx.lastMethod, fx.lastPath, fx.lastAuth)
			}
			if fx.lastBody["mode"] != "multi" || fx.lastBody["agent"] != false || fx.lastBody["query"] != tc.entry.Text || fx.lastBody["max_results"] != float64(tc.max) {
				t.Errorf("request body = %v", fx.lastBody)
			}
			if got, present := fx.lastBody["doi"]; tc.entry.DOI == "" {
				if present {
					t.Errorf("text-only request must omit doi: %v", fx.lastBody)
				}
			} else if got != tc.entry.DOI {
				t.Errorf("doi = %v, want %q", got, tc.entry.DOI)
			}
			if got := fx.lastBody["sources"].([]any); len(got) != 2 || got[0] != "ieee" || got[1] != "arxiv" {
				t.Errorf("sources = %v", got)
			}
			if got := fx.lastBody["api_keys"].(map[string]any); len(got) != 1 || got["ieee"] != "user-key" {
				t.Errorf("api_keys = %v", got)
			}
		})
	}
}

func TestRemoteSearchMulti_OmitsEmptyApiKeys(t *testing.T) {
	fx, srv := newMultiFixture(t)
	fx.respBody = `{"results":{},"usage":{"llm_tokens":0},"errors":{}}`
	p := NewRemoteProvider(srv.URL, "", 5*time.Second)

	if _, err := p.SearchMulti(context.Background(), SearchEntry{Text: "q"}, []string{"arxiv"}, nil); err != nil {
		t.Fatalf("SearchMulti: %v", err)
	}
	if _, present := fx.lastBody["api_keys"]; present {
		t.Errorf("api_keys should be omitted when nil, body = %v", fx.lastBody)
	}
	if fx.lastBody["max_results"] != float64(DefaultMaxResults) {
		t.Errorf("max_results default = %v", fx.lastBody["max_results"])
	}
}

func TestRemoteSearchMulti_Non200IsError(t *testing.T) {
	fx, srv := newMultiFixture(t)
	fx.respStatus = http.StatusBadGateway
	fx.respBody = `{"detail":"boom"}`
	p := NewRemoteProvider(srv.URL, "tok", 5*time.Second)
	if _, err := p.SearchMulti(context.Background(), SearchEntry{Text: "q", MaxResults: 5}, []string{"arxiv"}, nil); err == nil {
		t.Fatal("non-200 must surface as an error")
	}
}

func TestRemoteListBackends(t *testing.T) {
	fx, srv := newMultiFixture(t)
	fx.respBody = `{"backends":[
		{"name":"arxiv","label":"arXiv","category":"academic","requires_key":false,"user_key":false,"available":true},
		{"name":"ieee","label":"IEEE Xplore","category":"academic","requires_key":true,"user_key":true,"available":false}
	]}`
	p := NewRemoteProvider(srv.URL, "tok", 5*time.Second)

	backends, err := p.ListBackends(context.Background())
	if err != nil {
		t.Fatalf("ListBackends: %v", err)
	}
	if len(backends) != 2 {
		t.Fatalf("backends = %v", backends)
	}
	if backends[1].RequiresKey != true || backends[1].Available != false || backends[1].UserKey != true {
		t.Errorf("ieee meta = %+v", backends[1])
	}
	if fx.lastAuth != "Bearer tok" {
		t.Errorf("auth = %q", fx.lastAuth)
	}
}

func TestRemoteListBackends_UnreachableIsError(t *testing.T) {
	p := NewRemoteProvider("http://127.0.0.1:0", "tok", 100*time.Millisecond)
	if _, err := p.ListBackends(context.Background()); err == nil {
		t.Fatal("unreachable service must surface as an error")
	}
}
