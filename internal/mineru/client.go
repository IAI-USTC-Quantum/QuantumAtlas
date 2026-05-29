// Package mineru is a server-side client + async orchestrator for MinerU's
// document-extraction API (https://mineru.net). It powers the silent
// markdown conversion behind GET /api/papers/{arxiv_id}/markdown: when a
// paper has a PDF but no cached markdown, the server submits the PDF's
// share URL to MinerU using its own MINERU_API_TOKEN, polls until the task
// finishes, then uploads the resulting full.md (and images) into the raw
// asset store so subsequent requests hit cache.
//
// This is the Go counterpart of qatlas/parser/mineru_client.py + the
// orchestration in qatlas/client/mineru.py, but driven server-side rather
// than by a contributor's local CLI. The two flows are independent and
// non-conflicting: contributors may still run `qatlas mineru` with their
// own quota; this package only kicks in when an end user requests markdown
// that doesn't exist yet.
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

// Error is returned when MinerU responds with a non-zero application code
// or an otherwise unusable payload.
type Error struct {
	Msg string
}

func (e *Error) Error() string { return "mineru: " + e.Msg }

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
	return extractResult(raw)
}

// extractResult parses a MinerU result zip: it locates the markdown
// (any entry ending in "full.md") and treats everything under an
// "images/" path component as an image keyed by the path relative to the
// markdown's directory.
func extractResult(zipBytes []byte) (Result, error) {
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
		case isImageEntry(f.Name):
			b, err := readZipEntry(f)
			if err != nil {
				return Result{}, err
			}
			res.Images[relImageName(f.Name, mdDir)] = b
		}
	}
	if res.Markdown == nil {
		return Result{}, &Error{Msg: "result zip full.md was empty / unreadable"}
	}
	return res, nil
}

// isImageEntry reports whether a zip entry path sits under an "images/"
// directory component — MinerU writes figures there and references them as
// "images/<name>" from full.md.
func isImageEntry(name string) bool {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		if p == "images" && i < len(parts)-1 {
			return true
		}
	}
	return false
}

// relImageName returns the image path relative to the markdown's directory
// so the stored key mirrors the markdown's "images/<name>" references.
func relImageName(name, mdDir string) string {
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
// {code,msg,data} envelope, returning the `data` object. A non-zero code
// or non-2xx HTTP status becomes an *Error.
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &Error{Msg: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))}
	}
	var env struct {
		Code json.Number    `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &Error{Msg: "decode envelope: " + err.Error()}
	}
	if env.Code.String() != "0" && env.Code.String() != "" {
		msg := env.Msg
		if msg == "" {
			msg = "code " + env.Code.String()
		}
		return nil, &Error{Msg: msg}
	}
	if env.Data == nil {
		return nil, &Error{Msg: "response did not include data object"}
	}
	return env.Data, nil
}
