package routes

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// newRAGProxy is library-private. These tests cover the three things
// the proxy must guarantee:
//
//   1. Path strip: "/api/rag/search" reaches upstream as "/search".
//   2. Upstream down: returns a JSON 502 (not a Go default error page).
//   3. Oversized-body defense: PocketBase's middleware chain may swap
//      r.Body with a reader that returns more bytes than the original
//      Content-Length advertises; the Director must clamp the body
//      back to the declared length so the upstream HTTP connection
//      isn't aborted with "ContentLength=N with Body length 2N".

func TestRAGProxyStripsPrefix(t *testing.T) {
	gotPath := make(chan string, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath <- r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	target, _ := url.Parse(upstream.URL)
	proxy := newRAGProxy(target)

	cases := map[string]string{
		"/api/rag/search":  "/search",
		"/api/rag/healthz": "/healthz",
		"/api/rag/foo/bar": "/foo/bar",
		"/api/rag":         "/",
	}
	for in, want := range cases {
		req := httptest.NewRequest(http.MethodGet, in, nil)
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, req)
		got := <-gotPath
		if got != want {
			t.Errorf("path strip: in=%s want=%s got=%s", in, want, got)
		}
		if rec.Code != http.StatusOK {
			t.Errorf("path %s: want 200 got %d", in, rec.Code)
		}
	}
}

func TestRAGProxyUpstreamDown(t *testing.T) {
	// Port 1 is privileged + always refused, guaranteeing dial failure.
	target, _ := url.Parse("http://127.0.0.1:1")
	proxy := newRAGProxy(target)

	req := httptest.NewRequest(http.MethodPost, "/api/rag/search", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("want application/json, got %q", ct)
	}
	if body := rec.Body.String(); body == "" {
		t.Fatal("want JSON body, got empty")
	}
}

func TestRAGProxyDefendsAgainstOversizedBody(t *testing.T) {
	gotBody := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	proxy := newRAGProxy(target)

	const original = `{"q":"hello"}` // 13 bytes
	// Simulate the bug: body returns 2× the declared ContentLength.
	doubled := original + original // 26 bytes
	req := httptest.NewRequest(http.MethodPost, "/api/rag/search",
		io.NopCloser(strings.NewReader(doubled)))
	req.ContentLength = int64(len(original))

	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)

	got := <-gotBody
	if !bytes.Equal(got, []byte(original)) {
		t.Fatalf("upstream got %d bytes %q; want %d bytes %q",
			len(got), got, len(original), original)
	}
}
