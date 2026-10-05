package mineru

// V1 production provider follows MinerU 4.0.10's api_client.py contract
// (upstream ed50cc15). V4 helpers remain for compatibility, but never feed
// the source-bound production converter.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type V1Job struct {
	JobID  string   `json:"job_id"`
	Status string   `json:"status"`
	Error  *v1Error `json:"error"`
	Files  []struct {
		Status      string   `json:"status"`
		Error       *v1Error `json:"error"`
		OutputFiles map[string]struct {
			FileID string `json:"file_id"`
		} `json:"output_files"`
	} `json:"files"`
}
type v1Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (c *Client) v1Base() string {
	base := strings.TrimRight(c.baseURL, "/")
	// Preserve custom/local api_url roots exactly as upstream does. Only
	// normalize the hosted legacy configuration (which used the site root).
	parsed, err := url.Parse(base)
	if err == nil && parsed.Path == "" && (strings.EqualFold(parsed.Hostname(), "mineru.net") || strings.EqualFold(parsed.Hostname(), "www.mineru.net")) {
		base += "/api"
	}
	return base
}

// doV1 keeps tokens on their original origin even through redirects. Go's
// default redirect policy compares hostnames but ignores ports/subdomains.
// Clone the client rather than mutating a concurrently shared token client.
func (c *Client) doV1(req *http.Request) (*http.Response, error) {
	client := *c.http
	originalCheck := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if originalCheck != nil {
			if err := originalCheck(next, via); err != nil {
				return err
			}
		} else if len(via) >= 10 {
			return fmt.Errorf("too many V1 redirects")
		}
		if len(via) > 0 && !sameOrigin(via[0].URL, next.URL) {
			next.Header.Del("Authorization")
			next.Header.Del("Proxy-Authorization")
			next.Header.Del("Cookie")
		}
		return nil
	}
	return client.Do(req)
}

func classifyV1Error(code, message string, status int) *Error {
	e := classifyAPIError(code, message, status)
	switch code {
	case "invalid_api_key", "invalid_request", "invalid_parameter", "file_too_large", "too_many_pages", "unsupported_file_type":
		e.Kind = ErrFatal
	case "daily_limit_exceeded", "daily_quota_exceeded", "quota_exceeded", "insufficient_quota":
		e.Kind = ErrDailyLimit
	case "rate_limit_exceeded", "queue_full", "service_unavailable", "timeout":
		e.Kind = ErrRetryable
	}
	return e
}

func (c *Client) v1JSON(ctx context.Context, method, endpoint string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.v1Base()+endpoint, reader)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.doV1(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return archiveError("V1 response exceeds JSON limit")
	}
	var problem struct {
		Error   *v1Error `json:"error"`
		MsgCode string   `json:"msgCode"`
		Msg     string   `json:"msg"`
	}
	_ = json.Unmarshal(raw, &problem)
	if problem.Error != nil {
		return classifyV1Error(problem.Error.Code, problem.Error.Message, resp.StatusCode)
	}
	if problem.MsgCode != "" && problem.MsgCode != "0" {
		return classifyAPIError(problem.MsgCode, problem.Msg, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return classifyV1Error("", msg, resp.StatusCode)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return archiveError("decode V1 response: " + err.Error())
		}
	}
	return nil
}

// UploadV1 submits the exact frozen source bytes with their SHA-256. API
// credentials are sent only to same-origin upload URLs, not presigned hosts.
func (c *Client) UploadV1(ctx context.Context, filename string, pdf []byte, sha string) (string, error) {
	if len(pdf) == 0 || sha != fmt.Sprintf("%x", sha256.Sum256(pdf)) {
		return "", archiveError("V1 upload source SHA-256 does not match bytes")
	}
	var upload struct {
		ID            string            `json:"id"`
		Status        string            `json:"status"`
		UploadURL     string            `json:"upload_url"`
		UploadHeaders map[string]string `json:"upload_headers"`
		File          struct {
			ID string `json:"id"`
		} `json:"file"`
	}
	err := c.v1JSON(ctx, http.MethodPost, "/v1/uploads", map[string]any{"filename": filename, "bytes": len(pdf), "mime_type": "application/pdf", "purpose": "parse", "sha256sum": sha}, &upload)
	if err != nil {
		return "", err
	}
	if upload.Status == "completed" {
		if upload.File.ID == "" {
			return "", archiveError("V1 completed upload missing file.id")
		}
		return upload.File.ID, nil
	}
	if upload.ID == "" || upload.UploadURL == "" {
		return "", archiveError("V1 upload missing id or upload_url")
	}
	target, err := url.Parse(upload.UploadURL)
	if err != nil {
		return "", err
	}
	base, _ := url.Parse(c.v1Base())
	if !target.IsAbs() {
		if !strings.HasPrefix(upload.UploadURL, "/") || strings.HasPrefix(upload.UploadURL, "//") {
			return "", archiveError("invalid relative V1 upload URL")
		}
		target, err = url.Parse(c.v1Base() + upload.UploadURL)
		if err != nil {
			return "", err
		}
	}
	if (target.Scheme != "https" && target.Scheme != "http") || target.Host == "" || target.User != nil {
		return "", archiveError("invalid V1 upload URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target.String(), bytes.NewReader(pdf))
	if err != nil {
		return "", err
	}
	for name, value := range upload.UploadHeaders {
		// Never forward a producer-supplied bearer header cross-origin.
		if strings.EqualFold(name, "Authorization") && !sameOrigin(base, target) {
			return "", archiveError("cross-origin V1 upload authorization header")
		}
		req.Header.Set(name, value)
	}
	if sameOrigin(base, target) {
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
	}
	resp, err := c.doV1(req)
	if err != nil {
		return "", err
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
	if readErr != nil {
		return "", readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", classifyV1Error("", string(raw), resp.StatusCode)
	}
	var completed struct {
		File struct {
			ID string `json:"id"`
		} `json:"file"`
	}
	if err := c.v1JSON(ctx, http.MethodPost, "/v1/uploads/"+url.PathEscape(upload.ID)+"/complete", nil, &completed); err != nil {
		return "", err
	}
	if completed.File.ID == "" {
		return "", archiveError("V1 upload completion missing file.id")
	}
	return completed.File.ID, nil
}

func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func (c *Client) SubmitV1(ctx context.Context, fileID, tier string, ocr bool) (V1Job, error) {
	if fileID == "" {
		return V1Job{}, archiveError("empty V1 file id")
	}
	if tier == "" {
		tier = "standard"
	}
	switch tier {
	case "flash", "basic", "standard", "advanced":
	default:
		return V1Job{}, archiveError("unsupported MinerU V1 tier " + tier)
	}
	mode := "auto"
	if ocr {
		mode = "ocr"
	}
	var job V1Job
	err := c.v1JSON(ctx, http.MethodPost, "/v1/parse/jobs", map[string]any{"files": []any{map[string]any{"source": map[string]string{"type": "file_id", "file_id": fileID}}}, "output_formats": []string{"zip"}, "tier": tier, "ocr_mode": mode}, &job)
	if err != nil {
		return V1Job{}, err
	}
	if job.JobID == "" {
		return V1Job{}, archiveError("V1 parse response missing job_id")
	}
	return job, nil
}

func (c *Client) GetV1Job(ctx context.Context, id string) (V1Job, error) {
	var job V1Job
	if id == "" {
		return job, archiveError("empty V1 job id")
	}
	err := c.v1JSON(ctx, http.MethodGet, "/v1/parse/jobs/"+url.PathEscape(id), nil, &job)
	return job, err
}

func (job V1Job) output() (fileID string, done bool, err error) {
	switch job.Status {
	case "completed":
		if len(job.Files) != 1 || job.Files[0].Status != "completed" || job.Files[0].Error != nil {
			return "", true, archiveError("V1 completed job must contain one successful file")
		}
		id := job.Files[0].OutputFiles["zip"].FileID
		if id == "" {
			return "", true, archiveError("V1 job did not return full ZIP output")
		}
		return id, true, nil
	case "failed", "canceled", "partial":
		if job.Error != nil {
			return "", true, classifyV1Error(job.Error.Code, job.Error.Message, 0)
		}
		for _, f := range job.Files {
			if f.Error != nil {
				return "", true, classifyV1Error(f.Error.Code, f.Error.Message, 0)
			}
		}
		return "", true, archiveError("V1 job ended with status " + job.Status)
	case "queued", "running":
		return "", false, nil
	default:
		return "", true, archiveError("unknown V1 job status " + job.Status)
	}
}

func (c *Client) FetchV1Result(ctx context.Context, fileID string) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.v1Base()+"/v1/files/"+url.PathEscape(fileID)+"/content", nil)
	if err != nil {
		return Result{}, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.doV1(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, classifyV1Error("", fmt.Sprintf("V1 output download HTTP %d", resp.StatusCode), resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxArchiveBytes+1))
	if err != nil {
		return Result{}, err
	}
	return ExtractPackage(raw)
}
