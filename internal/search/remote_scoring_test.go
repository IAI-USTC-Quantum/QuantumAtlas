package search

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRemoteScoringContract(t *testing.T) {
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing service auth")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/scoring/capabilities":
			if r.Method != "GET" {
				t.Error("capabilities method")
			}
			_, _ = w.Write([]byte(`{"language":"qatlas-expr-v1","generation_available":true,"features":{"year":{}}}`))
		case "/v1/scoring/generate":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			requests = append(requests, body)
			_, _ = w.Write([]byte(`{"scorer":{"language":"qatlas-expr-v1","filter":"true","score":"lexical"},"summary":"relevance","warnings":[],"scorer_hash":"abc","feature_version":"v1","usage":{"llm_tokens":24}}`))
		case "/v1/search":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			requests = append(requests, body)
			_, _ = w.Write([]byte(`{"hits":[{"title":"A","score":12.345678,"raw_rank":1,"raw_score":0.5,"score_detail":null,"score_explanation":{"nodes":[1]}},{"title":"B","score":-2}],"ranking":{"source":"scorer","score_scale":"custom"},"usage":{"llm_tokens":0},"errors":{}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	p := NewRemoteProvider(srv.URL, "secret", time.Second)
	caps, err := p.ScoringCapabilities(context.Background())
	if err != nil || caps["generation_available"] != true {
		t.Fatalf("caps %v %v", caps, err)
	}
	gen, err := p.GenerateScorer(context.Background(), ScorerGenerateRequest{Query: "quantum", Requirements: "recent"})
	if err != nil || gen.Usage.LLMTokens != 24 {
		t.Fatalf("generate %v %v", gen, err)
	}
	result, err := p.SearchRanked(context.Background(), RankedSearchRequest{Text: "quantum", Sources: []string{"arxiv"}, MaxResults: 5, Scorer: gen.Scorer, Explain: true}, map[string]string{"arxiv": "user-key"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 2 || result.Hits[0].Score != 12.345678 || result.Hits[1].Score != -2 || result.Hits[0].RawRank == nil || string(result.Hits[0].ScoreDetail) != "null" || len(result.Hits[0].ScoreExplanation) == 0 {
		t.Fatalf("metadata lost: %+v", result)
	}
	if requests[0]["query"] != "quantum" || requests[0]["requirements"] != "recent" {
		t.Fatal(requests)
	}
	req := requests[1]
	if req["ranking"] != "scorer" || req["mode"] != "fused" || req["agent"] != false || req["explain"] != true || req["query"] != "quantum" {
		t.Fatal(req)
	}
	if req["api_keys"].(map[string]any)["arxiv"] != "user-key" {
		t.Fatal("keys not forwarded")
	}
}

func TestRemoteScoringRejectsMalformedGeneratedProgram(t *testing.T) {
	for _, program := range []string{`[]`, `"lexical"`, `{}`, `null`, `{"language":"qatlas-expr-v1","filter":"true","score":12}`, `{"language":"qatlas-expr-v1","filter":null,"score":"lexical"}`} {
		t.Run(program, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"scorer":` + program + `,"scorer_hash":"abc","feature_version":"v1","usage":{"llm_tokens":31}}`))
			}))
			defer srv.Close()
			_, err := NewRemoteProvider(srv.URL, "", time.Second).GenerateScorer(context.Background(), ScorerGenerateRequest{})
			var remote *ScoringRemoteError
			if !errors.As(err, &remote) || remote.Status != 502 || remote.Usage.LLMTokens != 31 {
				t.Fatalf("invalid program not rejected or usage lost: %v", err)
			}
		})
	}
}

func TestRemoteScoringErrorsAndMixedVersions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   int
		code   string
		tokens int64
	}{
		{"compiler", 422, `{"detail":{"code":"type","message":"Expected a number","field":"score","position":{"line":1,"column":2}},"usage":{"llm_tokens":7}}`, 422, "type", 7},
		{"legacy detail", 422, `{"detail":"legacy secret","usage":{"llm_tokens":17}}`, 422, "upstream_failed", 17},
		{"malformed position", 422, `{"detail":{"code":"type","message":"Expected number","position":"secret"},"usage":{"llm_tokens":19}}`, 422, "upstream_failed", 19},
		{"generation timeout", 504, `{"detail":{"code":"generation_timeout","message":"Deadline exceeded"},"usage":{"llm_tokens":23}}`, 504, "generation_timeout", 23},
		{"busy", 503, `{"detail":{"code":"busy","message":"Busy"}}`, 503, "busy", 0},
		{"quota", 429, `{"detail":{"code":"busy","message":"Busy"}}`, 429, "busy", 0},
		{"too big", 413, `{"detail":{"code":"request_too_large","message":"Too large"}}`, 413, "request_too_large", 0},
		{"internal", 500, `{"detail":"password=secret","usage":{"llm_tokens":9}}`, 502, "upstream_failed", 9},
		{"proxy html", 502, `<html>secret upstream url</html>`, 502, "upstream_failed", 0},
		{"old service", 404, `{"detail":"Not Found"}`, 503, "unsupported_service", 0},
		{"service credential", 401, `{"detail":"secret token"}`, 502, "upstream_failed", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			p := NewRemoteProvider(srv.URL, "", time.Second)
			_, err := p.GenerateScorer(context.Background(), ScorerGenerateRequest{Query: "x", Requirements: "x"})
			var remote *ScoringRemoteError
			if !errors.As(err, &remote) || remote.Status != tc.want || remote.Detail.Code != tc.code || remote.Usage.LLMTokens != tc.tokens {
				t.Fatalf("error: %#v", err)
			}
			raw, _ := json.Marshal(remote)
			if strings.Contains(string(raw), "secret") {
				t.Fatalf("leaked upstream data: %s", raw)
			}
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"hits":[],"ranking":{"source":"default"}}`))
	}))
	defer srv.Close()
	_, err := NewRemoteProvider(srv.URL, "", time.Second).SearchRanked(context.Background(), RankedSearchRequest{}, nil)
	if err == nil {
		t.Fatal("old service ignored scorer silently")
	}
}
