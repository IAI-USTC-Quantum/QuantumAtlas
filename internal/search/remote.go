package search

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

// RemoteProvider is the client for the external qatlas-search
// microservice. It participates in the /api/search fan-out like any
// other Provider (agent=false queries), and additionally exposes
// SearchAgentic for the metered POST /api/search/agentic endpoint —
// that method returns the FULL wire response (conclusion, LLM token
// usage, per-backend errors) and a real error so the caller can decide
// on quota refunds.
//
// Wire contract (see the design brief):
//
//	POST {url}/v1/search   Authorization: Bearer {token}
//	req:  {"query", "max_results", "sources": [str]|null, "agent": bool}
//	resp: {"hits": [{title, authors[], year?, doi?, arxiv_id?, url?,
//	        venue?, citations?, source, score}],
//	       "conclusion": str|null, "usage": {"llm_tokens": int},
//	       "errors": {backend: msg}}
//	GET  {url}/healthz     → 200 {"status":"ok",...}
type RemoteProvider struct {
	BaseProvider
	client  *http.Client
	baseURL string // trimmed of trailing slashes; /v1/search is appended
	token   string
}

// RemoteHit is one hit in the microservice's wire shape. Fields beyond
// the common Hit shape (url, venue, citations) are kept so agentic
// responses can forward them to the caller untouched.
type RemoteHit struct {
	Title     string   `json:"title"`
	Authors   []string `json:"authors,omitempty"`
	Year      int      `json:"year,omitempty"`
	DOI       string   `json:"doi,omitempty"`
	ArxivID   string   `json:"arxiv_id,omitempty"`
	URL       string   `json:"url,omitempty"`
	Venue     string   `json:"venue,omitempty"`
	Citations int      `json:"citations,omitempty"`
	Source    string   `json:"source"`
	Score     float64  `json:"score"`
}

// RemoteUsage is the metering section of the microservice response.
type RemoteUsage struct {
	LLMTokens int64 `json:"llm_tokens"`
}

// RemoteResponse is the full /v1/search response contract.
type RemoteResponse struct {
	Hits       []RemoteHit       `json:"hits"`
	Conclusion *string           `json:"conclusion"`
	Usage      RemoteUsage       `json:"usage"`
	Errors     map[string]string `json:"errors"`
}

// remoteRequest is the /v1/search request contract.
type remoteRequest struct {
	Query      string   `json:"query"`
	MaxResults int      `json:"max_results"`
	Sources    []string `json:"sources"`
	Agent      bool     `json:"agent"`
}

// NewRemoteProvider builds a RemoteProvider for the microservice at
// baseURL (e.g. "http://qatlas-search:8600"). timeout bounds each call —
// keep it generous (config default 60s) because agentic queries may
// drive an LLM.
func NewRemoteProvider(baseURL, token string, timeout time.Duration) *RemoteProvider {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &RemoteProvider{
		client:  &http.Client{Timeout: timeout},
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   token,
	}
}

// Name implements Provider.
func (p *RemoteProvider) Name() string { return "remote" }

// Healthz probes the microservice's GET /healthz; a 200 means healthy.
// Used by the plugin-status probe, never on the request path.
func (p *RemoteProvider) Healthz(ctx context.Context) error {
	if p.baseURL == "" {
		return fmt.Errorf("remote search: no base URL configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("remote search healthz: status %d", resp.StatusCode)
	}
	return nil
}

// Search implements Provider with a plain (agent=false) query. Backend
// failures follow the provider contract: recorded via BaseProvider,
// reported as (nil, nil).
func (p *RemoteProvider) Search(ctx context.Context, e SearchEntry) ([]Hit, error) {
	resp, err := p.SearchAgentic(ctx, e, false)
	if err != nil {
		return p.RecordFailure(err)
	}
	hits := make([]Hit, 0, len(resp.Hits))
	for _, rh := range resp.Hits {
		hits = append(hits, rh.ToHit())
	}
	return hits, nil
}

// SearchAgentic runs one /v1/search call and returns the FULL contract
// response. Unlike Search it returns real errors (HTTP failure, non-200,
// malformed body) — the caller (the metered agentic endpoint) needs the
// failure signal to refund the user's quota.
func (p *RemoteProvider) SearchAgentic(ctx context.Context, entry SearchEntry, agent bool) (RemoteResponse, error) {
	if p.baseURL == "" {
		return RemoteResponse{}, fmt.Errorf("remote search: no base URL configured")
	}
	query := entry.Text
	if query == "" {
		query = entry.Title
	}
	maxResults := entry.MaxResults
	if maxResults <= 0 {
		maxResults = DefaultMaxResults
	}
	body, err := json.Marshal(remoteRequest{
		Query:      query,
		MaxResults: maxResults,
		Sources:    nil, // let the microservice pick its default backends
		Agent:      agent,
	})
	if err != nil {
		return RemoteResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/search", bytes.NewReader(body))
	if err != nil {
		return RemoteResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return RemoteResponse{}, fmt.Errorf("remote search: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return RemoteResponse{}, fmt.Errorf("remote search: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return RemoteResponse{}, fmt.Errorf("remote search: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out RemoteResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return RemoteResponse{}, fmt.Errorf("remote search: decode response: %w", err)
	}
	return out, nil
}

// AdminProxyResult is the raw upstream response of an AdminProxy call:
// status code plus body, uninterpreted, so the admin route can decide
// how to surface non-2xx answers.
type AdminProxyResult struct {
	Status int
	Body   []byte
}

// AdminProxy forwards one request to the microservice's admin surface
// ({baseURL}{path}, e.g. /v1/admin/manifest) with the service's Bearer
// token. It does NOT parse or re-shape the body: the admin routes pass
// 2xx payloads through verbatim. A returned error means the request
// never got a response (network/timeout); a non-2xx upstream is
// reported in AdminProxyResult.Status, not as an error. The client's
// configured timeout (search.remote.timeout) bounds the call.
func (p *RemoteProvider) AdminProxy(ctx context.Context, method, path string, body []byte) (AdminProxyResult, error) {
	if p.baseURL == "" {
		return AdminProxyResult{}, fmt.Errorf("remote search: no base URL configured")
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return AdminProxyResult{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return AdminProxyResult{}, fmt.Errorf("remote search: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return AdminProxyResult{}, fmt.Errorf("remote search: read body: %w", err)
	}
	return AdminProxyResult{Status: resp.StatusCode, Body: raw}, nil
}

// ToHit maps a wire hit onto the shared Hit shape; an empty source is
// filled with this provider's name so merged hits always attribute.
func (rh RemoteHit) ToHit() Hit {
	source := rh.Source
	if source == "" {
		source = "remote"
	}
	return Hit{
		ArxivID: rh.ArxivID,
		DOI:     rh.DOI,
		Title:   rh.Title,
		Authors: rh.Authors,
		Year:    rh.Year,
		Score:   rh.Score,
		Source:  source,
	}
}
