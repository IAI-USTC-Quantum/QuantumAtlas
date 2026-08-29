package rag

import (
	"context"
	"encoding/json"
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
