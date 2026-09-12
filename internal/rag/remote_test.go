package rag

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPushIndexSuccess(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody indexRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"task_id":"t-1","status":"queued"}`))
	}))
	defer srv.Close()

	c := NewRemoteClient(srv.URL, "svc-token", 5*time.Second)
	if err := c.PushIndex(context.Background(), "2401.12345"); err != nil {
		t.Fatalf("PushIndex: %v", err)
	}
	if gotPath != "/v1/index" {
		t.Errorf("path = %q, want /v1/index", gotPath)
	}
	if gotAuth != "Bearer svc-token" {
		t.Errorf("Authorization = %q, want Bearer svc-token", gotAuth)
	}
	if gotBody.PaperID != "2401.12345" {
		t.Errorf("paper_id = %q, want 2401.12345", gotBody.PaperID)
	}
}

func TestPushIndexUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"bad token"}`))
	}))
	defer srv.Close()

	c := NewRemoteClient(srv.URL, "wrong-token", 5*time.Second)
	err := c.PushIndex(context.Background(), "2401.12345")
	if err == nil {
		t.Fatal("PushIndex with 401 upstream returned nil error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error %v should mention status 401", err)
	}
}

func TestPushIndexNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`boom`))
	}))
	defer srv.Close()

	c := NewRemoteClient(srv.URL, "", 5*time.Second)
	err := c.PushIndex(context.Background(), "10.1234/qec.2024")
	if err == nil {
		t.Fatal("PushIndex with 500 upstream returned nil error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %v should mention status 500", err)
	}
}

func TestPushIndexTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewRemoteClient(srv.URL, "", 50*time.Millisecond)
	err := c.PushIndex(context.Background(), "2401.12345")
	if err == nil {
		t.Fatal("PushIndex past client timeout returned nil error")
	}
}

func TestPushIndexNoBaseURL(t *testing.T) {
	c := NewRemoteClient("", "tok", time.Second)
	if err := c.PushIndex(context.Background(), "2401.12345"); err == nil {
		t.Fatal("PushIndex with empty base URL returned nil error")
	}
}

// Regression for the v0.23.0 crash: with rag.remote disabled,
// cmd/qatlasd stored a nil *RemoteClient in the IndexPusher interface
// (typed-nil — the interface itself is non-nil), and the first
// PushIndex on the nil receiver SIGSEGV'd the process. The nil guard
// must return an error instead.
func TestNilReceiverReturnsError(t *testing.T) {
	var c *RemoteClient
	if err := c.PushIndex(context.Background(), "2401.12345"); err == nil {
		t.Fatal("PushIndex on nil receiver returned nil error")
	}
	if err := c.Healthz(context.Background()); err == nil {
		t.Fatal("Healthz on nil receiver returned nil error")
	}
}

func TestHealthz(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	c := NewRemoteClient(srv.URL, "", 5*time.Second)
	if err := c.Healthz(context.Background()); err != nil {
		t.Fatalf("Healthz: %v", err)
	}

	bad := NewRemoteClient(srv.URL+"/missing", "", 5*time.Second)
	if err := bad.Healthz(context.Background()); err == nil {
		t.Fatal("Healthz against 404 returned nil error")
	}
}

func TestRetrieveProxyRoundTrip(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hits":[{"paper_id":"2401.12345","score":0.9}]}`))
	}))
	defer srv.Close()

	c := NewRemoteClient(srv.URL, "rag-token", 5*time.Second)
	req := []byte(`{"query":"surface code thresholds","top_k":5}`)
	resp, err := c.Retrieve(context.Background(), req)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if gotPath != "/v1/retrieve" {
		t.Errorf("path = %q, want /v1/retrieve", gotPath)
	}
	if gotAuth != "Bearer rag-token" {
		t.Errorf("Authorization = %q, want Bearer rag-token", gotAuth)
	}
	if gotBody != string(req) {
		t.Errorf("body = %q, want verbatim %q", gotBody, string(req))
	}
	if string(resp) != `{"hits":[{"paper_id":"2401.12345","score":0.9}]}` {
		t.Errorf("resp = %q, want passthrough", string(resp))
	}
}

func TestEvidenceProxyRoundTrip(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"evidence":[["2401.12345","..."]]}`))
	}))
	defer srv.Close()

	c := NewRemoteClient(srv.URL, "", 5*time.Second)
	resp, err := c.Evidence(context.Background(), []byte(`{"claim_id":"x"}`))
	if err != nil {
		t.Fatalf("Evidence: %v", err)
	}
	if gotPath != "/v1/evidence" {
		t.Errorf("path = %q, want /v1/evidence", gotPath)
	}
	if !strings.Contains(string(resp), "evidence") {
		t.Errorf("resp = %q, want passthrough", string(resp))
	}
}

func TestRetrieveNotConfigured(t *testing.T) {
	c := NewRemoteClient("", "tok", time.Second)
	if _, err := c.Retrieve(context.Background(), []byte(`{}`)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Retrieve with empty base URL: err = %v, want ErrNotConfigured", err)
	}
	if _, err := c.Evidence(context.Background(), []byte(`{}`)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Evidence with empty base URL: err = %v, want ErrNotConfigured", err)
	}
	var nilClient *RemoteClient
	if _, err := nilClient.Retrieve(context.Background(), []byte(`{}`)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Retrieve on nil receiver: err = %v, want ErrNotConfigured", err)
	}
}

func TestRetrieveUpstreamErrorCarriesStatusAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"detail":"query embeds not built"}`))
	}))
	defer srv.Close()

	c := NewRemoteClient(srv.URL, "", 5*time.Second)
	_, err := c.Retrieve(context.Background(), []byte(`{}`))
	var up *UpstreamError
	if !errors.As(err, &up) {
		t.Fatalf("err = %v, want *UpstreamError", err)
	}
	if up.Status != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", up.Status)
	}
	if !strings.Contains(string(up.Body), "not built") {
		t.Errorf("body = %q, want the upstream body", string(up.Body))
	}
}

func TestRetrieveUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // dead endpoint immediately

	c := NewRemoteClient(srv.URL, "", 2*time.Second)
	if _, err := c.Retrieve(context.Background(), []byte(`{}`)); err == nil {
		t.Fatal("Retrieve against a dead endpoint returned nil error")
	} else if errors.Is(err, ErrNotConfigured) {
		t.Fatalf("dead endpoint misclassified as ErrNotConfigured: %v", err)
	}
}
