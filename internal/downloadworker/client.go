package downloadworker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
)

// HTTPError intentionally contains no URL, response body or credentials.
type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("master returned HTTP %d", e.Status) }

type Client struct {
	base   string
	secret string
	http   *http.Client
}

func NewClient(cfg Config, identity Identity) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxConnsPerHost = cfg.Concurrency + 4
	return &Client{base: strings.TrimRight(cfg.MasterURL, "/"), secret: identity.Secret, http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }
func (c *Client) request(ctx context.Context, method, path string, body io.Reader, headers http.Header, out any, upload bool) error {
	timeout := 20 * time.Second
	if upload {
		timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return errors.New("cannot construct master request")
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	for key, values := range headers {
		req.Header[key] = values
	}
	if upload {
		if n, e := strconv.ParseInt(headers.Get(workerprotocol.HeaderSize), 10, 64); e == nil {
			req.ContentLength = n
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("master request transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{Status: resp.StatusCode}
	}
	// The response is never logged and may not grow without bound.
	b, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return errors.New("invalid master response")
	}
	if out != nil && json.Unmarshal(b, out) != nil {
		return errors.New("invalid master JSON response")
	}
	return nil
}
func (c *Client) json(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return errors.New("cannot encode master request")
		}
		body = bytes.NewReader(b)
	}
	return c.request(ctx, method, path, body, http.Header{"Content-Type": []string{"application/json"}}, out, false)
}
func (c *Client) Register(ctx context.Context, req workerprotocol.RegisterRequest) (workerprotocol.Node, error) {
	var out workerprotocol.Node
	err := c.json(ctx, http.MethodPost, workerprotocol.RegisterPath, req, &out)
	return out, err
}
func (c *Client) Status(ctx context.Context) (workerprotocol.Node, error) {
	var out workerprotocol.Node
	err := c.json(ctx, http.MethodGet, workerprotocol.StatusPath, nil, &out)
	return out, err
}
func (c *Client) Heartbeat(ctx context.Context, req workerprotocol.HeartbeatRequest) (workerprotocol.HeartbeatResponse, error) {
	var out workerprotocol.HeartbeatResponse
	err := c.json(ctx, http.MethodPost, workerprotocol.HeartbeatPath, req, &out)
	return out, err
}
func (c *Client) Claim(ctx context.Context, limit int) (workerprotocol.ClaimResponse, error) {
	var out workerprotocol.ClaimResponse
	err := c.json(ctx, http.MethodPost, workerprotocol.ClaimPath, workerprotocol.ClaimRequest{Limit: limit}, &out)
	return out, err
}
func (c *Client) Report(ctx context.Context, r Record) (workerprotocol.Receipt, error) {
	var out workerprotocol.Receipt
	err := c.json(ctx, http.MethodPost, workerprotocol.ReportPath, workerprotocol.ReportRequest{AttemptID: r.Assignment.AttemptID, Failure: r.Failure, Error: r.Error}, &out)
	return out, err
}
func (c *Client) Receipt(ctx context.Context, id string) (workerprotocol.Receipt, error) {
	var out workerprotocol.Receipt
	err := c.json(ctx, http.MethodGet, workerprotocol.ReceiptPath+url.PathEscape(id), nil, &out)
	return out, err
}
func (c *Client) Upload(ctx context.Context, r Record, body io.Reader) (workerprotocol.Receipt, error) {
	var out workerprotocol.Receipt
	headers := http.Header{"Content-Type": []string{"application/pdf"}}
	headers.Set(workerprotocol.HeaderSHA256, r.SHA256)
	headers.Set(workerprotocol.HeaderSize, strconv.FormatInt(r.Size, 10))
	headers.Set(workerprotocol.HeaderStrategy, r.Strategy)
	metadata, err := json.Marshal(r.Metadata)
	if err != nil {
		return out, errors.New("cannot encode result metadata")
	}
	encoded := base64.RawURLEncoding.EncodeToString(metadata)
	for len(encoded) > 16<<10 && len(r.Metadata.Trace) > 0 {
		r.Metadata.Trace = r.Metadata.Trace[:len(r.Metadata.Trace)-1]
		metadata, _ = json.Marshal(r.Metadata)
		encoded = base64.RawURLEncoding.EncodeToString(metadata)
	}
	if len(encoded) > 16<<10 {
		return out, errors.New("result metadata exceeds header limit")
	}
	headers.Set(workerprotocol.HeaderResultMetadata, encoded)
	err = c.request(ctx, http.MethodPut, workerprotocol.UploadPath+url.PathEscape(r.Assignment.AttemptID), body, headers, &out, true)
	return out, err
}
