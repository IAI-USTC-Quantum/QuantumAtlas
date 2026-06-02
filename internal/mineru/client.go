// Package mineru is a Go client for MinerU's document-extraction API
// (https://mineru.net). It provides helpers used by the server to
// process MinerU result archives produced by contributors who run
// `qatlas mineru` locally with their own MINERU_API_TOKEN.
//
// The OSS server no longer drives MinerU submission itself — historical
// "silent server-side submission" was removed in v0.9.0 along with the
// byte-serving markdown endpoint. This package now exposes only the
// result-archive parsing primitives (ExtractResult, etc.) used by the
// contributor upload pipeline in internal/routes/papers.go.
package mineru

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
)

// Error definitions live in errors.go (with the Kind sentinel system that
// supports `errors.Is(err, mineru.ErrDailyLimit)` style classification).

// SubmitOptions are the per-task extraction knobs forwarded to MinerU.
// They map 1:1 onto the JSON body fields documented at
// https://mineru.net/apiManage/docs.
type SubmitOptions struct {
	ModelVersion  string
	Language      string
	EnableFormula bool
	EnableTable   bool
	IsOCR         bool
	NoCache       bool
	DataID        string
}

// TaskState is the decoded `data` object from GET .../extract/task/{id}.
type TaskState struct {
	State      string `json:"state"`
	FullZipURL string `json:"full_zip_url"`
	ErrMsg     string `json:"err_msg"`
}

// Result is the extracted content of a finished MinerU task.
type Result struct {
	Markdown []byte
	// Images maps the relative filename (as referenced from full.md, e.g.
	// "images/abc.jpg" or just "abc.jpg") to its raw bytes.
	Images map[string][]byte
}

// Client is a thin HTTP wrapper around MinerU's token-based v4 API.
//
// Safe for concurrent use: it holds no per-request mutable state and the
// embedded *http.Client is itself concurrency-safe.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

// NewClient builds a Client. baseURL defaults to https://mineru.net when
// empty. httpClient may be nil (a default with sane timeouts is used);
// tests inject an httptest-backed client here.
func NewClient(token, baseURL string, httpClient *http.Client) *Client {
	if baseURL == "" {
		baseURL = "https://mineru.net"
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 120 * time.Second}
	}
	return &Client{
		token:   token,
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    httpClient,
	}
}

// SubmitURLTask submits a URL extraction task and returns MinerU's task id.
func (c *Client) SubmitURLTask(ctx context.Context, url string, opts SubmitOptions) (string, error) {
	if opts.ModelVersion == "" {
		opts.ModelVersion = "vlm"
	}
	if opts.Language == "" {
		opts.Language = "ch"
	}
	body := map[string]any{
		"url":            url,
		"model_version":  opts.ModelVersion,
		"language":       opts.Language,
		"enable_formula": opts.EnableFormula,
		"enable_table":   opts.EnableTable,
		"is_ocr":         opts.IsOCR,
		"no_cache":       opts.NoCache,
	}
	if opts.DataID != "" {
		body["data_id"] = opts.DataID
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v4/extract/task", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Authorization", "Bearer "+c.token)

	data, err := c.doEnvelope(req)
	if err != nil {
		return "", err
	}
	taskID, _ := data["task_id"].(string)
	if taskID == "" {
		return "", &Error{Msg: "response did not include task_id"}
	}
	return taskID, nil
}

// GetTask returns the latest state for one extraction task.
func (c *Client) GetTask(ctx context.Context, taskID string) (TaskState, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/api/v4/extract/task/"+taskID, nil)
	if err != nil {
		return TaskState{}, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Authorization", "Bearer "+c.token)

	data, err := c.doEnvelope(req)
	if err != nil {
		return TaskState{}, err
	}
	// Re-marshal the data sub-object into the typed struct so we don't
	// hand-type-assert each field.
	raw, _ := json.Marshal(data)
	var st TaskState
	if err := json.Unmarshal(raw, &st); err != nil {
		return TaskState{}, &Error{Msg: "decode task data: " + err.Error()}
	}
	return st, nil
}

// MaxBatchSize is MinerU's per-batch file-count limit (POST
// /api/v4/extract/task/batch rejects bigger batches). Driver code should
// chunk longer queues at this boundary.
const MaxBatchSize = 50

// BatchFile is one entry in a SubmitURLBatch payload. Per-file knobs
// beyond URL + DataID (e.g. is_ocr, page_ranges) are not currently
// exposed; all files in one batch share the SubmitOptions passed to
// SubmitURLBatch. Add them here when there's an actual need.
type BatchFile struct {
	// URL is the publicly fetchable file location. For PDFs uploaded to
	// our RustFS, this is an S3 presigned GET URL whose lifetime must
	// outlast MinerU's processing window (we use 24h).
	URL string
	// DataID is the caller's correlation key for this file. MinerU echoes
	// it back in each per-file batch result so callers can match a
	// finished extract to the right paper without parsing file_name.
	// Optional but strongly recommended.
	DataID string
}

// BatchTaskState is one per-file entry returned by GetBatch.
//
// State is one of the MinerU lifecycle strings:
//
//	done            — full_zip_url is populated, ready to download
//	failed          — err_msg is populated; classify with classifyAPIError
//	waiting-file    — file fetch hasn't started yet
//	pending         — queued behind other tasks
//	running         — being processed
//	converting      — finalising the result zip
//
// Callers should treat anything other than done/failed as in-flight.
type BatchTaskState struct {
	FileName   string `json:"file_name"`
	DataID     string `json:"data_id"`
	State      string `json:"state"`
	FullZipURL string `json:"full_zip_url"`
	ErrMsg     string `json:"err_msg"`
	// ExtractProgress is best-effort progress when State=running. Fields
	// may be zero before MinerU has started processing.
	ExtractProgress struct {
		ExtractedPages int    `json:"extracted_pages"`
		TotalPages     int    `json:"total_pages"`
		StartTime      string `json:"start_time"`
	} `json:"extract_progress"`
}

// SubmitURLBatch submits up to MaxBatchSize URL-extraction tasks in one
// round-trip and returns MinerU's batch id. All files share the same
// SubmitOptions; per-file overrides aren't exposed (see BatchFile).
//
// Caller must chunk longer queues into multiple batches and submit each
// with its own SubmitURLBatch call.
func (c *Client) SubmitURLBatch(ctx context.Context, files []BatchFile, opts SubmitOptions) (string, error) {
	if len(files) == 0 {
		return "", &Error{Msg: "SubmitURLBatch: no files"}
	}
	if len(files) > MaxBatchSize {
		return "", &Error{Msg: fmt.Sprintf("SubmitURLBatch: %d files exceeds MinerU batch limit of %d", len(files), MaxBatchSize)}
	}
	if opts.ModelVersion == "" {
		opts.ModelVersion = "vlm"
	}
	if opts.Language == "" {
		opts.Language = "ch"
	}

	items := make([]map[string]any, 0, len(files))
	for i, f := range files {
		if f.URL == "" {
			return "", &Error{Msg: fmt.Sprintf("SubmitURLBatch: empty url at index %d", i)}
		}
		item := map[string]any{
			"url":    f.URL,
			"is_ocr": opts.IsOCR,
		}
		if f.DataID != "" {
			item["data_id"] = f.DataID
		}
		items = append(items, item)
	}

	body := map[string]any{
		"files":          items,
		"model_version":  opts.ModelVersion,
		"language":       opts.Language,
		"enable_formula": opts.EnableFormula,
		"enable_table":   opts.EnableTable,
		"no_cache":       opts.NoCache,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v4/extract/task/batch", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Authorization", "Bearer "+c.token)

	data, err := c.doEnvelope(req)
	if err != nil {
		return "", err
	}
	batchID, _ := data["batch_id"].(string)
	if batchID == "" {
		return "", &Error{Msg: "response did not include batch_id"}
	}
	return batchID, nil
}

// GetBatch returns per-file states for an in-flight batch. The slice has
// one entry per file submitted; ordering is not guaranteed to match the
// submission order, so callers should match by DataID.
//
// A nil/empty slice with no error means MinerU has accepted the batch
// but has nothing to report yet — keep polling.
func (c *Client) GetBatch(ctx context.Context, batchID string) ([]BatchTaskState, error) {
	if batchID == "" {
		return nil, &Error{Msg: "GetBatch: empty batch id"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/api/v4/extract-results/batch/"+batchID, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Authorization", "Bearer "+c.token)

	data, err := c.doEnvelope(req)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(data["extract_result"])
	if string(raw) == "null" {
		return nil, nil
	}
	var results []BatchTaskState
	if err := json.Unmarshal(raw, &results); err != nil {
		return nil, &Error{Msg: "decode batch results: " + err.Error()}
	}
	return results, nil
}

// FetchResult downloads MinerU's result zip and extracts full.md plus any
// sibling images. fullZipURL is the public URL MinerU returns on a done
// task.
func (c *Client) FetchResult(ctx context.Context, fullZipURL string) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullZipURL, nil)
	if err != nil {
		return Result{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{}, &Error{Msg: fmt.Sprintf("download result zip: HTTP %d", resp.StatusCode)}
	}
	// MinerU zips are bounded (a single paper's markdown + images); read
	// into memory so we can random-access via zip.NewReader.
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, err
	}
	return ExtractResult(raw)
}

// ExtractResult parses a MinerU result zip: it locates the markdown
// (any entry ending in "full.md") and treats everything under an
// "images/" path component as an image keyed by the path relative to the
// markdown's directory.
//
// Exported so HTTP upload handlers (POST /api/papers/{id}/upload-mineru)
// can reuse the same parsing logic the server-side silent-conversion
// converter uses, without copy/paste drift.
func ExtractResult(zipBytes []byte) (Result, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return Result{}, &Error{Msg: "open result zip: " + err.Error()}
	}

	var mdName string
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "full.md") {
			mdName = f.Name
			break
		}
	}
	if mdName == "" {
		return Result{}, &Error{Msg: "result zip did not contain full.md"}
	}
	// The directory full.md lives in is the root for relative image refs.
	mdDir := path.Dir(mdName)

	res := Result{Images: map[string][]byte{}}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		switch {
		case f.Name == mdName:
			b, err := readZipEntry(f)
			if err != nil {
				return Result{}, err
			}
			res.Markdown = b
		case IsImageEntry(f.Name):
			b, err := readZipEntry(f)
			if err != nil {
				return Result{}, err
			}
			res.Images[RelImageName(f.Name, mdDir)] = b
		}
	}
	if res.Markdown == nil {
		return Result{}, &Error{Msg: "result zip full.md was empty / unreadable"}
	}
	return res, nil
}

// IsImageEntry reports whether a zip entry path sits under an "images/"
// directory component — MinerU writes figures there and references them as
// "images/<name>" from full.md.
func IsImageEntry(name string) bool {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		if p == "images" && i < len(parts)-1 {
			return true
		}
	}
	return false
}

// RelImageName returns the image path relative to the markdown's directory
// so the stored key mirrors the markdown's "images/<name>" references.
func RelImageName(name, mdDir string) string {
	if mdDir != "." && mdDir != "" {
		if rel := strings.TrimPrefix(name, mdDir+"/"); rel != name {
			return rel
		}
	}
	// Fall back to the path starting at the "images/" component.
	idx := strings.Index(name, "images/")
	if idx >= 0 {
		return name[idx:]
	}
	return path.Base(name)
}

func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, &Error{Msg: "open zip entry " + f.Name + ": " + err.Error()}
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// doEnvelope performs the request and decodes MinerU's standard
// {code,msg,data} envelope, returning the `data` object. Both non-zero
// `code` values and non-2xx HTTP statuses become *Error with a Kind
// sentinel set via classifyAPIError so callers can do
// `errors.Is(err, mineru.ErrDailyLimit)` etc.
func (c *Client) doEnvelope(req *http.Request) (map[string]any, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	// Try to parse the body as an envelope first — even 4xx responses
	// from MinerU usually carry a code/msg we want to classify. Decode
	// failures aren't fatal here: we fall back to HTTP-status-only classification.
	var env struct {
		Code json.Number    `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	envOK := json.Unmarshal(raw, &env) == nil

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code := ""
		msg := ""
		if envOK {
			code = env.Code.String()
			msg = env.Msg
		}
		if msg == "" {
			// Surface a snippet of the body as a last resort so users see
			// *something* meaningful when MinerU returns a raw 502/504 page.
			snippet := strings.TrimSpace(string(raw))
			if len(snippet) > 200 {
				snippet = snippet[:200] + "..."
			}
			msg = snippet
		}
		return nil, classifyAPIError(code, msg, resp.StatusCode)
	}

	if !envOK {
		return nil, &Error{Msg: "decode envelope: unparseable body"}
	}
	if env.Code.String() != "0" && env.Code.String() != "" {
		return nil, classifyAPIError(env.Code.String(), env.Msg, 0)
	}
	if env.Data == nil {
		return nil, &Error{Msg: "response did not include data object"}
	}
	return env.Data, nil
}
