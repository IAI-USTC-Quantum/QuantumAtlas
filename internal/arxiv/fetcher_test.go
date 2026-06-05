package arxiv

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
)

// pdfBody is a minimal valid-magic PDF body the tests stuff into the
// httptest server. The first 5 bytes (%PDF-) are what the magic-check
// looks for; the rest is filler.
var pdfBody = []byte("%PDF-1.4\n%fakebody")

// TestFetcher_Success: happy path — single 200 response with valid PDF
// body returns Result with correct size + sha256.
func TestFetcher_Success(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := "/quant-ph/9508027v1"; r.URL.Path != want {
			t.Errorf("path: got %q, want %q", r.URL.Path, want)
		}
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "qatlasd/") {
			t.Errorf("User-Agent: got %q, want qatlasd/ prefix", ua)
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(pdfBody)
	}))
	defer srv.Close()

	f := mustFetcher(t, Config{BaseURL: srv.URL + "/", UserAgent: "qatlasd/test"})
	res, err := f.Fetch(context.Background(), paperassets.MustParse("quant-ph/9508027v1"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Size != int64(len(pdfBody)) {
		t.Errorf("Size: got %d, want %d", res.Size, len(pdfBody))
	}
	// sha256("%PDF-1.4\n%fakebody") — compute on the fly to avoid
	// embedding a constant that would drift if pdfBody changes.
	want := sha256OfBytes(pdfBody)
	if res.Sha256 != want {
		t.Errorf("Sha256: got %q, want %q", res.Sha256, want)
	}
	if res.Attempts != 1 {
		t.Errorf("Attempts: got %d, want 1", res.Attempts)
	}
}

// TestFetcher_NotFound: 404 is fatal — no retry, returns ErrNotFound.
func TestFetcher_NotFound(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	f := mustFetcher(t, Config{BaseURL: srv.URL + "/"})
	_, err := f.Fetch(context.Background(), paperassets.MustParse("2501.99999v1"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err: got %v, want ErrNotFound", err)
	}
	if hits.Load() != 1 {
		t.Errorf("hits: got %d, want 1 (404 must NOT retry)", hits.Load())
	}
}

// TestFetcher_RetryThenSucceed: server returns 503 then 200; fetcher
// retries with backoff and ultimately succeeds.
func TestFetcher_RetryThenSucceed(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n < 3 {
			w.Header().Set("Retry-After", "0") // tell us to retry immediately
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(pdfBody)
	}))
	defer srv.Close()

	f := mustFetcher(t, Config{
		BaseURL:  srv.URL + "/",
		RetryMax: 5,
	})
	res, err := f.Fetch(context.Background(), paperassets.MustParse("2501.00010v1"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("hits: got %d, want 3", got)
	}
	if res.Attempts != 3 {
		t.Errorf("Attempts: got %d, want 3", res.Attempts)
	}
}

// TestFetcher_429RetryBudgetExhausted: persistent 429 → ErrRateLimited.
func TestFetcher_429RetryBudgetExhausted(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	f := mustFetcher(t, Config{BaseURL: srv.URL + "/", RetryMax: 2})
	_, err := f.Fetch(context.Background(), paperassets.MustParse("2501.00010v1"))
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err: got %v, want ErrRateLimited", err)
	}
}

// TestFetcher_NotPDF: server returns 200 + HTML body — magic-byte check
// catches it as ErrNotPDF (fatal, no retry).
func TestFetcher_NotPDF(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>withdrawn</html>"))
	}))
	defer srv.Close()

	f := mustFetcher(t, Config{BaseURL: srv.URL + "/"})
	_, err := f.Fetch(context.Background(), paperassets.MustParse("2501.00010v1"))
	if !errors.Is(err, ErrNotPDF) {
		t.Fatalf("err: got %v, want ErrNotPDF", err)
	}
	if hits.Load() != 1 {
		t.Errorf("hits: got %d, want 1 (not-pdf must NOT retry)", hits.Load())
	}
}

// TestFetcher_TooLarge: server returns a body larger than MaxBytes —
// ErrTooLarge, no truncation pretending to be success.
func TestFetcher_TooLarge(t *testing.T) {
	t.Parallel()
	bigBody := bytes.Repeat([]byte("X"), 4096)
	bigBody[0] = '%'
	bigBody[1] = 'P'
	bigBody[2] = 'D'
	bigBody[3] = 'F'
	bigBody[4] = '-'
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(bigBody)
	}))
	defer srv.Close()

	f := mustFetcher(t, Config{BaseURL: srv.URL + "/", MaxBytes: 2048})
	_, err := f.Fetch(context.Background(), paperassets.MustParse("2501.00010v1"))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err: got %v, want ErrTooLarge", err)
	}
}

// TestFetcher_RetryAfterSeconds: backoff respects integer-seconds
// Retry-After header.
func TestFetcher_RetryAfterSeconds(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	var firstHit, secondHit time.Time
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n := hits.Add(1)
		switch n {
		case 1:
			firstHit = time.Now()
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			secondHit = time.Now()
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write(pdfBody)
		}
		mu.Unlock()
	}))
	defer srv.Close()

	f := mustFetcher(t, Config{BaseURL: srv.URL + "/", RetryMax: 1})
	_, err := f.Fetch(context.Background(), paperassets.MustParse("2501.00010v1"))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	gap := secondHit.Sub(firstHit)
	if gap < 800*time.Millisecond { // 1s wait, allow 200ms slack
		t.Errorf("retry happened too soon: gap=%v, expected ~1s", gap)
	}
}

// TestFetcher_ContextCancel: in-flight context cancellation propagates
// without further retry.
func TestFetcher_ContextCancel(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled

	f := mustFetcher(t, Config{BaseURL: srv.URL + "/", RetryMax: 5})
	_, err := f.Fetch(ctx, paperassets.MustParse("2501.00010v1"))
	if err == nil {
		t.Fatal("Fetch: nil err on cancelled ctx")
	}
}

// TestFetch_MissingVersion: bare arxiv id without version is a caller
// bug — fetcher rejects up-front (an unversioned arxiv.org URL would
// resolve to "latest" which our byte-immutability contract forbids).
func TestFetch_MissingVersion(t *testing.T) {
	t.Parallel()
	f := mustFetcher(t, Config{})
	_, err := f.Fetch(context.Background(), paperassets.MustParse("2501.00010"))
	if err == nil || !strings.Contains(err.Error(), "versioned") {
		t.Fatalf("Fetch unversioned: err=%v, want versioned-required error", err)
	}
}

// TestParseRetryAfter covers both forms (seconds + HTTP-date) plus
// the empty / garbage fallback.
func TestParseRetryAfter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{"empty", "", 0},
		{"seconds", "30", 30 * time.Second},
		{"zero seconds", "0", 0},
		{"negative seconds clamps to 0", "-5", 0},
		{"http date in future", now.Add(45 * time.Second).UTC().Format(http.TimeFormat), 45 * time.Second},
		{"http date in past clamps to 0", now.Add(-1 * time.Hour).UTC().Format(http.TimeFormat), 0},
		{"garbage", "tomorrow ish", 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseRetryAfter(tc.raw, now)
			// Allow a couple seconds of slack for date-based comparisons
			// (http.ParseTime rounds to seconds).
			diff := got - tc.want
			if diff < 0 {
				diff = -diff
			}
			if diff > 2*time.Second {
				t.Errorf("parseRetryAfter(%q): got %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestBuildUserAgent: format matches arxiv's preferred shape per the
// bulk-data etiquette doc.
func TestBuildUserAgent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		version, mailto, want string
	}{
		{"0.20.0", "ops@example.com", "qatlasd/0.20.0 (mailto:ops@example.com)"},
		{"0.20.0", "", "qatlasd/0.20.0"},
		{"", "ops@example.com", "qatlasd/dev (mailto:ops@example.com)"},
	}
	for _, tc := range cases {
		got := BuildUserAgent(tc.version, tc.mailto)
		if got != tc.want {
			t.Errorf("BuildUserAgent(%q, %q) = %q, want %q", tc.version, tc.mailto, got, tc.want)
		}
	}
}

// TestFetcher_InvalidConfig: surface the misconfiguration loudly
// instead of silently rate-limiting to nothing.
func TestFetcher_InvalidConfig(t *testing.T) {
	t.Parallel()
	cases := []Config{
		{RPS: -1},
		{Burst: -1},
		{MaxBytes: 100}, // < 1 KiB
	}
	for i, cfg := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			if _, err := New(cfg); err == nil {
				t.Fatalf("New(%+v): nil err, want config error", cfg)
			}
		})
	}
}

// --- helpers ---

func mustFetcher(t *testing.T, cfg Config) *Fetcher {
	t.Helper()
	// Tests never want production rate limiting — burst high enough
	// to never block test runs.
	if cfg.RPS == 0 {
		cfg.RPS = 1000
	}
	if cfg.Burst == 0 {
		cfg.Burst = 1000
	}
	if cfg.RetryMax == 0 {
		cfg.RetryMax = 1
	}
	if cfg.HTTPTimeout == 0 {
		cfg.HTTPTimeout = 2 * time.Second
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = 1 << 20 // 1 MiB plenty for the test payloads
	}
	f, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return f
}

func sha256OfBytes(b []byte) string {
	h := sha256.New()
	_, _ = h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}
