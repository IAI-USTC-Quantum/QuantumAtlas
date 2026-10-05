package mineru

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
)

func TestV1RealContractUploadParseAndFullResult(t *testing.T) {
	var uploaded []byte
	var createBody, parseBody map[string]any
	zipBody := buildCompleteZip(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("API/same-origin authentication missing at %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/v1/uploads":
			_ = json.NewDecoder(r.Body).Decode(&createBody)
			_, _ = w.Write([]byte(`{"id":"up","status":"pending","upload_url":"/v1/upload-body","upload_headers":{"X-Signed":"yes"}}`))
		case "/api/v1/upload-body":
			if r.Method != http.MethodPut || r.Header.Get("X-Signed") != "yes" {
				t.Error("upload protocol")
			}
			uploaded, _ = io.ReadAll(r.Body)
		case "/api/v1/uploads/up/complete":
			_, _ = w.Write([]byte(`{"file":{"id":"pdf"}}`))
		case "/api/v1/parse/jobs":
			_ = json.NewDecoder(r.Body).Decode(&parseBody)
			_, _ = w.Write([]byte(`{"job_id":"job","status":"running"}`))
		case "/api/v1/parse/jobs/job":
			_, _ = w.Write([]byte(`{"job_id":"job","status":"completed","files":[{"status":"completed","output_files":{"zip":{"file_id":"zip"}}}]}`))
		case "/api/v1/files/zip/content":
			_, _ = w.Write(zipBody)
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient("secret", srv.URL+"/api", srv.Client())
	file, err := c.UploadV1(context.Background(), "source.pdf", fakePDFBytes, paperbundle.SHA256(fakePDFBytes))
	if err != nil {
		t.Fatal(err)
	}
	if file != "pdf" || !bytes.Equal(uploaded, fakePDFBytes) {
		t.Fatal("PDF bytes changed")
	}
	if createBody["sha256sum"] != paperbundle.SHA256(fakePDFBytes) || createBody["mime_type"] != "application/pdf" || createBody["purpose"] != "parse" {
		t.Fatalf("create payload: %v", createBody)
	}
	job, err := c.SubmitV1(context.Background(), file, "standard", false)
	if err != nil {
		t.Fatal(err)
	}
	formats := parseBody["output_formats"].([]any)
	if len(formats) != 1 || formats[0] != "zip" || parseBody["tier"] != "standard" || parseBody["ocr_mode"] != "auto" {
		t.Fatalf("parse contract: %v", parseBody)
	}
	if _, ok := parseBody["model_version"]; ok {
		t.Fatal("V4 model_version sent as V1 tier")
	}
	job, err = c.GetV1Job(context.Background(), job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	output, done, err := job.output()
	if err != nil || !done || output != "zip" {
		t.Fatalf("output: %q %v %v", output, done, err)
	}
	result, err := c.FetchV1Result(context.Background(), output)
	if err != nil || len(result.Members) != 5 || len(result.MiddleJSON) == 0 {
		t.Fatalf("complete result: %+v %v", result, err)
	}
}

func TestV1PresignedUploadDoesNotLeakToken(t *testing.T) {
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("token leaked to presigned upload origin")
		}
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	defer external.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/uploads" {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "up", "upload_url": external.URL + "/put"})
		} else {
			_, _ = w.Write([]byte(`{"file":{"id":"pdf"}}`))
		}
	}))
	defer api.Close()
	if _, err := NewClient("secret", api.URL+"/api", api.Client()).UploadV1(context.Background(), "p.pdf", fakePDFBytes, paperbundle.SHA256(fakePDFBytes)); err != nil {
		t.Fatal(err)
	}
}

func TestV1UsesConfiguredCustomAPIRoot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/parse/jobs" {
			t.Errorf("custom root rewritten: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"job_id":"job","status":"queued"}`))
	}))
	defer server.Close()
	if _, err := NewClient("secret", server.URL, server.Client()).SubmitV1(context.Background(), "pdf", "standard", false); err != nil {
		t.Fatal(err)
	}
	if got := NewClient("secret", "https://mineru.net", nil).v1Base(); got != "https://mineru.net/api" {
		t.Fatalf("legacy hosted base not normalized: %s", got)
	}
}

func TestV1RedirectDoesNotLeakToken(t *testing.T) {
	zipBody := buildCompleteZip(t)
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("API bearer leaked through cross-origin redirect")
		}
		_, _ = w.Write(zipBody)
	}))
	defer external.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, external.URL+"/output", http.StatusFound)
	}))
	defer api.Close()
	if _, err := NewClient("secret", api.URL, api.Client()).FetchV1Result(context.Background(), "result"); err != nil {
		t.Fatal(err)
	}
}

func TestV1RejectsPartialAndMissingFullOutput(t *testing.T) {
	for _, raw := range []string{`{"status":"partial"}`, `{"status":"failed","error":{"code":"invalid_request","message":"bad"}}`, `{"status":"completed","files":[{"output_files":{"middle_json":{"file_id":"not-zip"}}}]}`, `{"status":"completed","files":[{"status":"failed","output_files":{"zip":{"file_id":"bad"}}}]}`} {
		var job V1Job
		if err := json.Unmarshal([]byte(raw), &job); err != nil {
			t.Fatal(err)
		}
		if _, done, err := job.output(); err == nil || !done {
			t.Fatalf("partial result accepted: %s", raw)
		}
	}
	if _, err := NewClient("", "http://unused", nil).SubmitV1(context.Background(), "pdf", "vlm", false); !errors.Is(err, ErrFatal) {
		t.Fatal("model version accepted as tier")
	}
	if !errors.Is(classifyV1Error("daily_limit_exceeded", "", 0), ErrDailyLimit) || !errors.Is(classifyV1Error("rate_limit_exceeded", "", 429), ErrRetryable) {
		t.Fatal("V1 quotas misclassified")
	}
}
