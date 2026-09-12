// Package match is the client for the external qatlas-match microservice.
//
// High-precision paper identity matching moved out of qatlasd: qatlas-match
// owns the strict normalization + registry lookup rules (DOI / arXiv /
// OpenAlex / paper URL / all-words title) and answers with the unified
// qa_… id. qatlasd's only responsibility is proxying authenticated user
// traffic to it — user auth (PAT + papers:read scope) happens on the
// qatlasd route, the microservice only sees network-internal callers.
//
// Wire contract (mirrors the qatlas-search / qatlas-rag style, see
// internal/search/remote.go and internal/rag/remote.go):
//
//	POST {url}/v1/match   Authorization: Bearer {token}
//	req:  {"inputs": ["…"], "doi": …, "arxiv_id": …, "openalex_id": …,
//	       "qatlas_id": …, "url": …, "title": …, "author": …, "year": …}
//	       (all fields optional; at least one input or typed field required)
//	resp: {"results": [{input, kind, normalized, matched, qatlas_id,
//	       method, ambiguous, paper, candidates, truncated, reason}]}
//	GET  {url}/healthz    → 200 {"status":"ok","db":true,"version":…}
package match

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RemoteClient is the client for the qatlas-match microservice.
type RemoteClient struct {
	client  *http.Client
	baseURL string // trimmed of trailing slashes; /v1/match is appended
	token   string
}

// matchRequest is the /v1/match request contract (mirrors the
// qatlas_match.models.MatchRequest pydantic model).
type matchRequest struct {
	Inputs     []string `json:"inputs,omitempty"`
	DOI        string   `json:"doi,omitempty"`
	ArxivID    string   `json:"arxiv_id,omitempty"`
	OpenAlexID string   `json:"openalex_id,omitempty"`
	QatlasID   string   `json:"qatlas_id,omitempty"`
	URL        string   `json:"url,omitempty"`
	Title      string   `json:"title,omitempty"`
	Author     string   `json:"author,omitempty"`
	Year       int      `json:"year,omitempty"`
}

// MatchPaper is one papers-row projection in a match result.
type MatchPaper struct {
	PaperID    string   `json:"paper_id"`
	ArxivID    string   `json:"arxiv_id,omitempty"`
	DOI        string   `json:"doi,omitempty"`
	OpenAlexID string   `json:"openalex_id,omitempty"`
	Title      string   `json:"title,omitempty"`
	Authors    []string `json:"authors,omitempty"`
	Year       int      `json:"year,omitempty"`
	Status     string   `json:"status,omitempty"`
	HasMD      bool     `json:"has_md"`
}

// MatchResult is one input's outcome.
type MatchResult struct {
	Input      string       `json:"input"`
	Kind       string       `json:"kind"`
	Normalized string       `json:"normalized,omitempty"`
	Matched    bool         `json:"matched"`
	QatlasID   string       `json:"qatlas_id,omitempty"`
	Method     string       `json:"method,omitempty"`
	Ambiguous  bool         `json:"ambiguous"`
	Paper      *MatchPaper  `json:"paper,omitempty"`
	Candidates []MatchPaper `json:"candidates,omitempty"`
	Truncated  bool         `json:"truncated"`
	Reason     string       `json:"reason,omitempty"`
}

// MatchResponse is the /v1/match response contract.
type MatchResponse struct {
	Results []MatchResult `json:"results"`
}

// Query is the typed /v1/match request the routes build; zero string
// fields are omitted from the wire body.
type Query struct {
	Inputs     []string
	DOI        string
	ArxivID    string
	OpenAlexID string
	QatlasID   string
	URL        string
	Title      string
	Author     string
	Year       int
}

// NewRemoteClient builds a RemoteClient for the microservice at baseURL
// (e.g. "http://qatlas-match:8601"). timeout bounds each call (config
// default 15s).
func NewRemoteClient(baseURL, token string, timeout time.Duration) *RemoteClient {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &RemoteClient{
		client:  &http.Client{Timeout: timeout},
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   token,
	}
}

// Healthz probes the microservice's GET /healthz; a 200 means healthy.
// Used by the plugin-status probe, never on the match path.
func (c *RemoteClient) Healthz(ctx context.Context) error {
	// Nil receiver: a disabled match.remote yields a nil *RemoteClient
	// which may still reach here through an interface (typed-nil) —
	// return an error instead of panicking.
	if c == nil || c.baseURL == "" {
		return fmt.Errorf("match remote: no base URL configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("match remote healthz: status %d", resp.StatusCode)
	}
	return nil
}

// Match proxies one match query. Non-2xx and network/timeout failures are
// returned as errors (the route maps them to 502).
func (c *RemoteClient) Match(ctx context.Context, q Query) (MatchResponse, error) {
	if c == nil || c.baseURL == "" {
		return MatchResponse{}, fmt.Errorf("match remote: no base URL configured")
	}
	body, err := json.Marshal(matchRequest{
		Inputs:     q.Inputs,
		DOI:        q.DOI,
		ArxivID:    q.ArxivID,
		OpenAlexID: q.OpenAlexID,
		QatlasID:   q.QatlasID,
		URL:        q.URL,
		Title:      q.Title,
		Author:     q.Author,
		Year:       q.Year,
	})
	if err != nil {
		return MatchResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/match", bytes.NewReader(body))
	if err != nil {
		return MatchResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return MatchResponse{}, fmt.Errorf("match remote: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return MatchResponse{}, fmt.Errorf("match remote: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return MatchResponse{}, fmt.Errorf("match remote: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out MatchResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return MatchResponse{}, fmt.Errorf("match remote: decode body: %w", err)
	}
	return out, nil
}
