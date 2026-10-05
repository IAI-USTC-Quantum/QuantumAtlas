package routes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/match"
)

func TestPaperMatchFailureDiagnostics(t *testing.T) {
	for _, tt := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"timeout", context.DeadlineExceeded, http.StatusGatewayTimeout, "match_upstream_timeout"},
		{"unknown backend failure", errors.New("private upstream data token=secret"), http.StatusBadGateway, "match_upstream_failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newMatchHarness(t, &fakeMatchBackend{err: tt.err})
			status, _, body := h.do(http.MethodPost, "/api/papers/match", `{"inputs":["quant-ph/0401083"]}`, rawHeader(h.sessionToken()))
			if status != tt.status || body["code"] != tt.code {
				t.Fatalf("got status=%d body=%v; want %d code=%s", status, body, tt.status, tt.code)
			}
			if strings.Contains(asString(body["detail"]), "secret") {
				t.Fatal("backend details leaked to caller")
			}
		})
	}
}

func TestPaperMatchUpstreamHTTPDiagnostics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"detail":"postgresql://internal:secret@database/qatlas"}`))
	}))
	defer srv.Close()
	h := newMatchHarness(t, match.NewRemoteClient(srv.URL, "", time.Second))
	status, _, body := h.do(http.MethodPost, "/api/papers/match", `{"inputs":["quant-ph/0401083"]}`, rawHeader(h.sessionToken()))
	if status != http.StatusBadGateway || body["code"] != "match_upstream_http_error" || body["upstream_status"] != float64(http.StatusServiceUnavailable) {
		t.Fatalf("got status=%d body=%v", status, body)
	}
	if strings.Contains(asString(body["detail"]), "secret") {
		t.Fatal("upstream response body leaked")
	}
}

// Drive the actual proxy through PocketBase auth and real TCP, including a
// committed connection that loses its response. Failures never become an empty
// successful match and never generate implicit upstream retries.
func TestPaperMatchRemoteFailureHTTP(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   string
		status int
		code   string
	}{
		{"invalid JSON", "html", http.StatusBadGateway, "match_upstream_invalid_response"},
		{"missing results", "missing", http.StatusBadGateway, "match_upstream_invalid_response"},
		{"response lost", "disconnect", http.StatusBadGateway, "match_upstream_transport_error"},
		{"timeout", "timeout", http.StatusGatewayTimeout, "match_upstream_timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch tc.mode {
				case "html":
					_, _ = w.Write([]byte("<html>private=secret</html>"))
				case "missing":
					_, _ = w.Write([]byte(`{"detail":"private=secret"}`))
				case "disconnect":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
				case "timeout":
					select {
					case <-r.Context().Done():
					case <-time.After(time.Second):
					}
				}
			}))
			defer srv.Close()
			timeout := time.Second
			if tc.mode == "timeout" {
				timeout = 50 * time.Millisecond
			}
			h := newMatchHarness(t, match.NewRemoteClient(srv.URL, "private-service-token", timeout))
			status, _, body := h.do(http.MethodPost, "/api/papers/match", `{"inputs":["quant-ph/0401083","2609.40263"]}`, rawHeader(h.sessionToken()))
			if status != tc.status || body["code"] != tc.code {
				t.Fatalf("HTTP %d body=%v", status, body)
			}
			if _, ok := body["results"]; ok {
				t.Fatalf("failure has results: %v", body)
			}
			detail := asString(body["detail"])
			if strings.Contains(detail, "secret") || strings.Contains(detail, srv.URL) || strings.Contains(detail, "private-service-token") {
				t.Fatalf("private upstream leaked: %q", detail)
			}
			if calls.Load() != 1 {
				t.Fatalf("upstream calls=%d; want exactly one", calls.Load())
			}
		})
	}
}
