package match

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMatchFailureClassification(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{"upstream unavailable", 503, `{"detail":"password=do-not-return"}`},
		{"upstream unauthorized", 401, `{"detail":"service_token=do-not-return"}`},
		{"HTML response", 200, `<html>gateway error</html>`},
		{"missing results", 200, `{}`},
		{"null results", 200, `{"results":null}`},
		{"truncated JSON", 200, `{"results":[`},
		{"oversized response", 200, `{"results":[],"padding":"` + strings.Repeat("x", 1<<20) + `"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			_, err := NewRemoteClient(srv.URL, "token", time.Second).Match(context.Background(), Query{Inputs: []string{"quant-ph/0401083"}})
			if err == nil {
				t.Fatal("invalid upstream response was treated as a successful empty match")
			}
			if tt.status != 200 {
				var httpErr *HTTPError
				if !errors.As(err, &httpErr) || httpErr.Status != tt.status {
					t.Fatalf("error = %v; want typed HTTP status %d", err, tt.status)
				}
			} else if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("error = %v; want ErrInvalidResponse", err)
			}
			if strings.Contains(err.Error(), "do-not-return") || strings.Contains(err.Error(), srv.URL) {
				t.Fatal("error disclosed private response or URL")
			}
			if calls.Load() != 1 {
				t.Fatalf("calls = %d; failures must not trigger hidden retries", calls.Load())
			}
		})
	}
}

func TestMatchTimeoutRetainsClassification(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-r.Context().Done():
		case <-time.After(250 * time.Millisecond):
		}
	}))
	defer srv.Close()
	_, err := NewRemoteClient(srv.URL, "", 50*time.Millisecond).Match(context.Background(), Query{Inputs: []string{"quant-ph/0401083"}})
	var networkErr net.Error
	if err == nil || !errors.As(err, &networkErr) || !networkErr.Timeout() {
		t.Fatalf("error = %v; want timeout classification", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestMatchValidEmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()
	out, err := NewRemoteClient(srv.URL, "", time.Second).Match(context.Background(), Query{Inputs: []string{"unmatched"}})
	if err != nil || out.Results == nil || len(out.Results) != 0 {
		t.Fatalf("empty response = %+v, err = %v", out, err)
	}
}
