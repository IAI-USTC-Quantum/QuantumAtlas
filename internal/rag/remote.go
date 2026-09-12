// Package rag is the client for the external qatlas-rag microservice.
//
// Semantic retrieval moved out of qatlasd: qatlas-rag owns the vector
// index and the embed worker. qatlasd's only responsibility is pushing
// an index-build task whenever a paper flips to 'ready' (ingest
// pipeline and MinerU conversion completion points) — the query path is
// served by qatlas-search, which fronts qatlas-rag.
//
// Wire contract (mirrors the qatlas-search style, see
// internal/search/remote.go):
//
//	POST {url}/v1/index   Authorization: Bearer {token}
//	req:  {"paper_id": "<arxiv_id or DOI>"}   — triggers the index
//	      build for that paper; idempotent
//	resp: task-status JSON (any 2xx = accepted)
//	POST {url}/v1/retrieve, /v1/evidence   Authorization: Bearer {token}
//	req/resp: qatlas-rag's own JSON schema, proxied verbatim by
//	      qatlasd's POST /api/rag/* routes (Retrieve / Evidence)
//	GET  {url}/healthz    → 200 {"status":"ok",...}
package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RemoteClient is the client for the qatlas-rag microservice. PushIndex
// drives index builds; Retrieve / Evidence relay the query surface for
// the /api/rag proxy routes; Healthz backs the plugin-status probe.
type RemoteClient struct {
	client  *http.Client
	baseURL string // trimmed of trailing slashes; /v1/index is appended
	token   string
}

// indexRequest is the /v1/index request contract.
type indexRequest struct {
	PaperID string `json:"paper_id"`
}

// NewRemoteClient builds a RemoteClient for the microservice at
// baseURL (e.g. "http://qatlas-rag:8700"). timeout bounds each call
// (config default 30s).
func NewRemoteClient(baseURL, token string, timeout time.Duration) *RemoteClient {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &RemoteClient{
		client:  &http.Client{Timeout: timeout},
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   token,
	}
}

// Healthz probes the microservice's GET /healthz; a 200 means healthy.
// Used by the plugin-status probe, never on the push path.
func (c *RemoteClient) Healthz(ctx context.Context) error {
	// Nil receiver: a disabled rag.remote yields a nil *RemoteClient
	// which may still reach here through an interface (typed-nil) —
	// return an error instead of panicking.
	if c == nil || c.baseURL == "" {
		return fmt.Errorf("rag remote: no base URL configured")
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
		return fmt.Errorf("rag remote healthz: status %d", resp.StatusCode)
	}
	return nil
}

// ErrNotConfigured is returned by Retrieve / Evidence when the client has
// no base URL (rag.remote disabled). The proxy route maps it onto 503.
var ErrNotConfigured = errors.New("rag remote: not configured")

// UpstreamError marks a non-2xx reply from the microservice. The proxy
// route forwards the upstream status and body verbatim instead of
// collapsing it into a generic 503, so rag clients see their own error
// schema.
type UpstreamError struct {
	Status int
	Body   []byte
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("rag remote: status %d: %s", e.Status, strings.TrimSpace(string(e.Body)))
}

// Retrieve proxies one POST {base}/v1/retrieve call. body is forwarded
// verbatim (the retrieve request/response schema belongs to the qatlas-rag
// repository; qatlasd only authenticates and relays) and the response
// body bytes are returned as-is.
func (c *RemoteClient) Retrieve(ctx context.Context, body []byte) ([]byte, error) {
	return c.proxyQuery(ctx, "/v1/retrieve", body)
}

// Evidence proxies one POST {base}/v1/evidence call, same passthrough
// contract as Retrieve.
func (c *RemoteClient) Evidence(ctx context.Context, body []byte) ([]byte, error) {
	return c.proxyQuery(ctx, "/v1/evidence", body)
}

// proxyQuery is the shared Retrieve/Evidence driver: POST path with the
// bearer token, stream the request bytes, return the response bytes.
// Network failures wrap the underlying error; non-2xx replies come back
// as *UpstreamError carrying the status and body.
func (c *RemoteClient) proxyQuery(ctx context.Context, path string, body []byte) ([]byte, error) {
	// Nil receiver: same typed-nil guard as Healthz — a disabled
	// rag.remote yields a nil *RemoteClient the route layer may hold.
	if c == nil || c.baseURL == "" {
		return nil, ErrNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rag remote: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("rag remote: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return raw, &UpstreamError{Status: resp.StatusCode, Body: raw}
	}
	return raw, nil
}

// PushIndex asks qatlas-rag to build the index for paperID (an arXiv
// id or a DOI). The microservice treats the call as idempotent, so
// callers may push freely on every ready transition. Any 2xx is
// accepted (the response is a task-status JSON the caller does not
// need); non-2xx, network and timeout failures are returned as errors
// so the caller can log them — push is best-effort and must never fail
// the enclosing pipeline.
func (c *RemoteClient) PushIndex(ctx context.Context, paperID string) error {
	// Nil receiver: same typed-nil guard as Healthz — the mineru and
	// ingest call sites hold the client in an interface and nil-check
	// the interface, which a typed nil defeats.
	if c == nil || c.baseURL == "" {
		return fmt.Errorf("rag remote: no base URL configured")
	}
	body, err := json.Marshal(indexRequest{PaperID: paperID})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/index", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("rag remote: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("rag remote: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("rag remote: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}
