package arxiv

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
)

// TestResolveLatestVersion_ParsesOgURL covers the happy path: arxiv
// abs page contains `<meta property="og:url" content=".../abs/<id>vN">`
// and ResolveLatestVersion lifts the vN out into the returned
// ParsedArxivID.
func TestResolveLatestVersion_ParsesOgURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		bareID  string
		body    string
		wantVer string
		wantCan string
	}{
		{
			name:    "new-style id with v3",
			bareID:  "0811.3171",
			body:    `<meta property="og:url" content="https://arxiv.org/abs/0811.3171v3" />`,
			wantVer: "v3",
			wantCan: "0811.3171v3",
		},
		{
			name:    "old-style canonical id with v2",
			bareID:  "quant-ph/9508027",
			body:    `<meta property="og:url" content="https://arxiv.org/abs/quant-ph/9508027v2" />`,
			wantVer: "v2",
			wantCan: "quant-ph/9508027v2",
		},
		{
			name:    "v1 only",
			bareID:  "2501.00010",
			body:    `<meta property="og:url" content="https://arxiv.org/abs/2501.00010v1" />`,
			wantVer: "v1",
			wantCan: "2501.00010v1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`<html><head>` + tc.body + `</head><body/></html>`))
			}))
			defer srv.Close()

			f, err := New(Config{
				BaseURL:    "http://example.invalid/pdf/",
				AbsBaseURL: srv.URL + "/",
				UserAgent:  "qatlasd-test (mailto:test@example.com)",
				RPS:        1000,
				Burst:      1000,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			bare, perr := paperassets.Parse(tc.bareID)
			if perr != nil {
				t.Fatalf("paperassets.Parse(%q): %v", tc.bareID, perr)
			}
			got, err := f.ResolveLatestVersion(context.Background(), bare)
			if err != nil {
				t.Fatalf("ResolveLatestVersion: %v", err)
			}
			if got.Version != tc.wantVer {
				t.Errorf("Version = %q, want %q", got.Version, tc.wantVer)
			}
			if got.Canonical != tc.wantCan {
				t.Errorf("Canonical = %q, want %q", got.Canonical, tc.wantCan)
			}
		})
	}
}

// TestResolveLatestVersion_404 covers the case where arxiv reports
// the paper doesn't exist — should surface ErrNotFound, the same
// sentinel Fetch uses on PDF 404.
func TestResolveLatestVersion_404(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	f, err := New(Config{
		BaseURL:    "http://example.invalid/pdf/",
		AbsBaseURL: srv.URL + "/",
		UserAgent:  "qatlasd-test (mailto:test@example.com)",
		RPS:        1000,
		Burst:      1000,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bare, _ := paperassets.Parse("9999.99999")
	_, err = f.ResolveLatestVersion(context.Background(), bare)
	if err == nil {
		t.Fatal("expected ErrNotFound, got nil")
	}
	if err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestResolveLatestVersion_MissingOgURLIsUpstream verifies that an
// arxiv-page-shape change (e.g. they renamed the tag) surfaces as
// ErrUpstream rather than a silent v0 or panic.
func TestResolveLatestVersion_MissingOgURLIsUpstream(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html><head></head><body>no version info here</body></html>`))
	}))
	defer srv.Close()

	f, err := New(Config{
		BaseURL:    "http://example.invalid/pdf/",
		AbsBaseURL: srv.URL + "/",
		UserAgent:  "qatlasd-test (mailto:test@example.com)",
		RPS:        1000,
		Burst:      1000,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bare, _ := paperassets.Parse("0811.3171")
	_, err = f.ResolveLatestVersion(context.Background(), bare)
	if err == nil {
		t.Fatal("expected ErrUpstream, got nil")
	}
	if !errorWraps(err, ErrUpstream) {
		t.Errorf("err = %v, want wrapped ErrUpstream", err)
	}
}

// TestResolveLatestVersion_AlreadyVersionedShortCircuits documents
// that calling ResolveLatestVersion on an already-versioned id
// returns it as-is without any HTTP traffic.
func TestResolveLatestVersion_AlreadyVersionedShortCircuits(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("HTTP should not be called when version already set")
	}))
	defer srv.Close()

	f, err := New(Config{
		BaseURL:    "http://example.invalid/pdf/",
		AbsBaseURL: srv.URL + "/",
		UserAgent:  "qatlasd-test (mailto:test@example.com)",
		RPS:        1000,
		Burst:      1000,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	versioned, _ := paperassets.Parse("0811.3171v3")
	got, err := f.ResolveLatestVersion(context.Background(), versioned)
	if err != nil {
		t.Fatalf("ResolveLatestVersion: %v", err)
	}
	if got.Canonical != "0811.3171v3" {
		t.Errorf("Canonical = %q, want unchanged 0811.3171v3", got.Canonical)
	}
}

// errorWraps is a tiny helper since the production code wraps via
// fmt.Errorf("%w: ..."), and we want to verify the chain still
// matches the target sentinel.
func errorWraps(err, target error) bool {
	for cur := err; cur != nil; {
		if cur == target {
			return true
		}
		u, ok := cur.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		cur = u.Unwrap()
	}
	return false
}

// keep time imported (used by other test files in this package)
var _ = time.Now
