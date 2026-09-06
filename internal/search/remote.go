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
// on quota refunds. SearchMulti drives the per-backend "multi" mode and
// ListBackends fetches the backend catalog for /api/search/backends.
//
// Wire contract (see the design brief):
//
//	POST {url}/v1/search   Authorization: Bearer {token}
//	req:  {"query", "max_results", "sources": [str]|null, "agent": bool,
//	       "mode": "fused"|"multi", "api_keys": {backend: key}|null}
//	resp (fused): {"hits": [{title, authors[], year?, doi?, arxiv_id?, url?,
//	        venue?, citations?, source, score, raw_rank?, raw_score?}],
//	       "conclusion": str|null, "usage": {"llm_tokens": int},
//	       "errors": {backend: msg}}
//	resp (multi):  {"results": {backend: [hit, ...]}, "conclusion": null,
//	       "usage": {"llm_tokens": 0}, "errors": {backend: msg}}
//	GET  {url}/v1/backends Authorization: Bearer {token}
//	resp: {"backends": [{name, label, category, requires_key,
//	       user_key, available}]}
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
	Abstract  string   `json:"abstract,omitempty"`
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

// RemoteResponse is the full /v1/search response contract (fused mode).
type RemoteResponse struct {
	Hits       []RemoteHit       `json:"hits"`
	Conclusion *string           `json:"conclusion"`
	Usage      RemoteUsage       `json:"usage"`
	Errors     map[string]string `json:"errors"`
}

// RemoteMultiResponse is the mode="multi" /v1/search response: one raw
// hit list per backend in the source's own order, no cross-backend
// merge or ranking.
type RemoteMultiResponse struct {
	Results map[string][]RemoteHit `json:"results"`
	Usage   RemoteUsage            `json:"usage"`
	Errors  map[string]string      `json:"errors"`
}

// RemoteBackendMeta is one entry of the microservice's /v1/backends
// catalog. Available reflects the SERVER-side key/config only — the
// caller merges it with per-user keys to decide selectability.
type RemoteBackendMeta struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Category    string `json:"category"` // "academic" | "web"
	RequiresKey bool   `json:"requires_key"`
	UserKey     bool   `json:"user_key"`
	Available   bool   `json:"available"`
}

// remoteBackendsResponse is the /v1/backends envelope.
type remoteBackendsResponse struct {
	Backends []RemoteBackendMeta `json:"backends"`
}

// remoteRequest is the /v1/search request contract.
type remoteRequest struct {
	Query      string            `json:"query"`
	MaxResults int               `json:"max_results"`
	Sources    []string          `json:"sources"`
	Agent      bool              `json:"agent"`
	Mode       string            `json:"mode,omitempty"`
	ApiKeys    map[string]string `json:"api_keys,omitempty"`
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
	resp, err := p.SearchAgentic(ctx, e, false, nil)
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
// failure signal to refund the user's quota. sources optionally pins the
// microservice backends (nil = its default tool list).
func (p *RemoteProvider) SearchAgentic(ctx context.Context, entry SearchEntry, agent bool, sources []string) (RemoteResponse, error) {
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
	req := remoteRequest{
		Query:      query,
		MaxResults: maxResults,
		Sources:    sources,
		Agent:      agent,
	}
	var out RemoteResponse
	if err := p.postJSON(ctx, "/v1/search", req, &out); err != nil {
		return RemoteResponse{}, err
	}
	return out, nil
}

// SearchMulti runs one mode="multi" /v1/search call: one raw hit list
// per requested backend, no merge/ranking. apiKeys carries per-user
// third-party keys (backend name -> key) that the microservice applies
// over its server-level fallbacks; it may be nil. Returns real errors —
// the multi endpoint has no quota to refund but the caller still needs
// the failure signal to answer 502.
func (p *RemoteProvider) SearchMulti(ctx context.Context, query string, maxResults int, sources []string, apiKeys map[string]string) (RemoteMultiResponse, error) {
	if p.baseURL == "" {
		return RemoteMultiResponse{}, fmt.Errorf("remote search: no base URL configured")
	}
	if maxResults <= 0 {
		maxResults = DefaultMaxResults
	}
	req := remoteRequest{
		Query:      query,
		MaxResults: maxResults,
		Sources:    sources,
		Mode:       "multi",
		ApiKeys:    apiKeys,
	}
	var out RemoteMultiResponse
	if err := p.postJSON(ctx, "/v1/search", req, &out); err != nil {
		return RemoteMultiResponse{}, err
	}
	return out, nil
}

// ListBackends fetches the microservice's backend catalog
// (GET /v1/backends). The returned entries describe server-side
// availability only; merging with per-user keys is the caller's job.
func (p *RemoteProvider) ListBackends(ctx context.Context) ([]RemoteBackendMeta, error) {
	if p.baseURL == "" {
		return nil, fmt.Errorf("remote search: no base URL configured")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/backends", nil)
	if err != nil {
		return nil, err
	}
	if p.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.token)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("remote search backends: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("remote search backends: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remote search backends: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out remoteBackendsResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("remote search backends: decode response: %w", err)
	}
	return out.Backends, nil
}

// postJSON issues one authenticated JSON POST against the microservice
// and decodes the response into out. Non-200 and transport failures come
// back as errors carrying the upstream body excerpt.
func (p *RemoteProvider) postJSON(ctx context.Context, path string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("remote search: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("remote search: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("remote search: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("remote search: decode response: %w", err)
	}
	return nil
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
		ArxivID:  rh.ArxivID,
		DOI:      rh.DOI,
		Title:    rh.Title,
		Abstract: rh.Abstract,
		Authors:  rh.Authors,
		Year:     rh.Year,
		Score:    rh.Score,
		Source:   source,
	}
}
