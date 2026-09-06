package downloader

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// RemoteProxy is the client for a standalone downloaderproxy instance
// (cmd/downloaderproxy) deployed on a machine with direct publisher
// entitlement — e.g. an Ag-Workstation on the campus network while
// qatlasd itself sits behind a proxied egress. The ladder delegates to
// it when local strategies hit entitlement walls: submit → poll →
// one-time file token retrieval. The retrieved bytes still pass the
// shared validation pipeline before acceptance.
type RemoteProxy struct {
	BaseURL string
	Token   string
	Client  *http.Client
	// Timeout bounds submit+poll+fetch for one paper. Default 5m.
	Timeout time.Duration
	// PollInterval. Default 1.5s.
	PollInterval time.Duration
}

// Enabled reports whether the proxy is wired.
func (p *RemoteProxy) Enabled() bool {
	return p != nil && strings.TrimSpace(p.BaseURL) != ""
}

type proxyJobView struct {
	JobID     string    `json:"job_id"`
	Status    string    `json:"status"` // queued|running|done|failed|invalid
	Strategy  string    `json:"strategy"`
	URL       string    `json:"url"`
	Error     string    `json:"error"`
	Attempts  []Attempt `json:"attempts"`
	Size      int64     `json:"size"`
	Sha256    string    `json:"sha256"`
	FileToken string    `json:"file_token"`
}

// FetchPDF delegates one paper to the remote proxy and returns a
// validated result plus the winning strategy id ("remote-proxy:<s>").
func (p *RemoteProxy) FetchPDF(ctx context.Context, ref registry.PaperRef) (*FetchResult, []Attempt, string, error) {
	if !p.Enabled() {
		return nil, nil, "", ErrProxyNotConfigured
	}
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	interval := p.PollInterval
	if interval == 0 {
		interval = 1500 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	identifier := ref.DOI
	if identifier == "" {
		identifier = ref.ArxivID
	}
	// Submit.
	body, err := json.Marshal(map[string]string{"identifier": identifier})
	if err != nil {
		return nil, nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/v1/jobs", bytes.NewReader(body))
	if err != nil {
		return nil, nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, nil, "", fmt.Errorf("proxy submit: %w", err)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return nil, nil, "", fmt.Errorf("proxy submit: http %d: %.200s", resp.StatusCode, raw)
	}
	var submitted proxyJobView
	if err := json.Unmarshal(raw, &submitted); err != nil {
		return nil, nil, "", fmt.Errorf("proxy submit: decode: %w", err)
	}
	if submitted.Status == "invalid" {
		return nil, nil, "", fmt.Errorf("proxy: invalid identifier: %s", submitted.Error)
	}

	// Poll until terminal.
	for {
		select {
		case <-ctx.Done():
			return nil, submitted.Attempts, "", fmt.Errorf("proxy: %w", ctx.Err())
		case <-time.After(interval):
		}
		view, err := p.pollJob(ctx, submitted.JobID)
		if err != nil {
			return nil, nil, "", err
		}
		switch view.Status {
		case "done":
			if view.FileToken == "" {
				return nil, view.Attempts, "", fmt.Errorf("proxy: job done but file token missing/expired")
			}
			res, err := p.fetchFile(ctx, view.FileToken)
			if err != nil {
				return nil, view.Attempts, "", err
			}
			if res.Sha256 == "" {
				res.Sha256 = view.Sha256
			}
			strategy := "remote-proxy"
			if view.Strategy != "" {
				strategy = "remote-proxy:" + view.Strategy
			}
			return res, view.Attempts, strategy, nil
		case "failed", "invalid":
			return nil, view.Attempts, "", fmt.Errorf("proxy: job %s: %s", view.Status, view.Error)
		}
	}
}

func (p *RemoteProxy) pollJob(ctx context.Context, jobID string) (*proxyJobView, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL+"/v1/jobs/"+jobID, nil)
	if err != nil {
		return nil, err
	}
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("proxy poll: %w", err)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("proxy poll: http %d", resp.StatusCode)
	}
	var view proxyJobView
	if err := json.Unmarshal(raw, &view); err != nil {
		return nil, fmt.Errorf("proxy poll: decode: %w", err)
	}
	return &view, nil
}

// fetchFile retrieves the one-time-token PDF and validates it through
// the shared pipeline (magic / trailer / size bounds).
func (p *RemoteProxy) fetchFile(ctx context.Context, token string) (*FetchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL+"/v1/files/"+token, nil)
	if err != nil {
		return nil, err
	}
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("proxy file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("proxy file: http %d", resp.StatusCode)
	}
	// Bound + validate exactly like a local fetch.
	body, err := io.ReadAll(io.LimitReader(resp.Body, DefaultMaxPDFBytes+1))
	if err != nil {
		return nil, fmt.Errorf("proxy file: read: %w", err)
	}
	if int64(len(body)) > DefaultMaxPDFBytes {
		return nil, ErrTooLarge
	}
	if ClassifyBody(body) != BodyPDF {
		return nil, fmt.Errorf("proxy file: %w", ErrNotPDF)
	}
	if !bytes.Contains(body[max(0, len(body)-2048):], []byte("%%EOF")) &&
		!bytes.Contains(body, []byte("startxref")) {
		return nil, ErrTruncated
	}
	url := resp.Header.Get("X-Proxy-Source-Url")
	return &FetchResult{
		Body:   bytes.NewReader(body),
		Size:   int64(len(body)),
		URL:    url,
		Sha256: resp.Header.Get("X-Qatlas-Sha256"),
	}, nil
}
