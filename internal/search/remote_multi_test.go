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
	lastBody   map[string]any
	respBody   string
	respStatus int
}

func newMultiFixture(t testing.TB) (*multiFixture, *httptest.Server) {
	t.Helper()
	fx := &multiFixture{respStatus: http.StatusOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fx.lastAuth = r.Header.Get("Authorization")
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

	resp, err := p.SearchMulti(context.Background(), "quantum", 5, []string{"ieee", "arxiv"}, map[string]string{"ieee": "user-key"})
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

func TestRemoteSearchMulti_OmitsEmptyApiKeys(t *testing.T) {
	fx, srv := newMultiFixture(t)
	fx.respBody = `{"results":{},"usage":{"llm_tokens":0},"errors":{}}`
	p := NewRemoteProvider(srv.URL, "", 5*time.Second)

	if _, err := p.SearchMulti(context.Background(), "q", 0, []string{"arxiv"}, nil); err != nil {
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
	if _, err := p.SearchMulti(context.Background(), "q", 5, []string{"arxiv"}, nil); err == nil {
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
