// downloaderproxy: a standalone, self-contained deployment of the
// robust downloader ladder (internal/downloader) meant to run on a
// machine with direct campus/publisher entitlement — e.g. an
// Ag-Workstation whose egress carries institutional subscriptions —
// while qatlasd itself sits behind a proxied network. qatlasd's
// downloader delegates to it via downloader.proxy_url.
//
// One container, no sidecars: the image bundles Chromium; the entry
// point starts it with a local CDP endpoint and this service drives it
// through the browser lane like any other strategy.
//
// API (Bearer token via DL_PROXY_TOKEN when set):
//
//	GET  /healthz            → {"status":"ok","browser":bool}
//	POST /v1/jobs            {"identifier": "10.1109/... | arXiv:... | url"}
//	                         → 202 {"job_id","kind"}  (async ladder run)
//	GET  /v1/jobs/{id}       → {"status":"queued|running|done|failed|invalid",
//	                            "strategy","url","error","attempts":[...],
//	                            "size","sha256","file_token","expires_at"}
//	GET  /v1/files/{token}   → the PDF bytes. Token is single-use and
//	                            expires 30 minutes after the job finished.
//
// Files live in /tmp/downloaderproxy and are deleted after first
// retrieval or expiry sweep. Nothing is persisted across restarts.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/arxiv"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
)

const (
	listenAddr     = ":8602"
	fileTTL        = 30 * time.Minute
	pollGiveupHint = "poll GET /v1/jobs/{id} until terminal"
)

type job struct {
	ID         string               `json:"job_id"`
	Identifier string               `json:"identifier"`
	Kind       string               `json:"kind"`
	Status     string               `json:"status"` // queued|running|done|failed|invalid
	Strategy   string               `json:"strategy,omitempty"`
	URL        string               `json:"url,omitempty"`
	Error      string               `json:"error,omitempty"`
	Attempts   []downloader.Attempt `json:"attempts,omitempty"`
	Size       int64                `json:"size,omitempty"`
	Sha256     string               `json:"sha256,omitempty"`
	FileToken  string               `json:"file_token,omitempty"`
	ExpiresAt  time.Time            `json:"expires_at,omitempty"`

	mu       sync.Mutex
	filePath string
}

type server struct {
	mu     sync.Mutex
	jobs   map[string]*job
	tokens map[string]string // token -> job id
	dl     *downloader.Downloader
}

func main() {
	token := strings.TrimSpace(os.Getenv("DL_PROXY_TOKEN"))
	unpaywall := strings.TrimSpace(os.Getenv("DL_UNPAYWALL_EMAIL"))
	s2key := strings.TrimSpace(os.Getenv("DL_S2_API_KEY"))
	cdp := strings.TrimSpace(os.Getenv("DL_BROWSER_CDP_URL"))
	if cdp == "" {
		cdp = "http://127.0.0.1:9222"
	}

	fetcher, err := arxiv.New(arxiv.Config{})
	if err != nil {
		log.Fatalf("arxiv fetcher: %v", err)
	}
	dl := downloader.New(nil, nil, fetcher, nil, downloader.Config{
		Concurrency:    2,
		UnpaywallEmail: unpaywall,
		S2APIKey:       s2key,
		Fetch:          downloader.FetchConfig{RequestTimeout: 60 * time.Second},
		Browser:        downloader.BrowserConfig{CDPURL: cdp, Timeout: 60 * time.Second},
	})

	s := &server{
		jobs:   map[string]*job{},
		tokens: map[string]string{},
		dl:     dl,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/v1/jobs", s.auth(s.handleSubmit))
	mux.HandleFunc("/v1/jobs/", s.auth(s.handleJob))
	mux.HandleFunc("/v1/files/", s.auth(s.handleFile))

	// Expiry sweeper.
	go func() {
		for range time.Tick(time.Minute) {
			s.sweep()
		}
	}()

	srv := &http.Server{Addr: listenAddr, Handler: mux}
	go func() {
		log.Printf("downloaderproxy listening on %s (browser lane via %s, auth=%v)",
			listenAddr, cdp, token != "")
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
}

func (s *server) auth(next http.HandlerFunc) http.HandlerFunc {
	token := strings.TrimSpace(os.Getenv("DL_PROXY_TOKEN"))
	if token == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"detail": "invalid bearer token"})
			return
		}
		next(w, r)
	}
}

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

func (s *server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Identifier string `json:"identifier"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "invalid JSON"})
		return
	}
	id, err := downloader.ParseIdentifier(strings.TrimSpace(body.Identifier))
	j := &job{
		ID:         "dlp_" + randHex(12),
		Identifier: body.Identifier,
	}
	if err != nil {
		j.Status = "invalid"
		j.Error = err.Error()
	} else {
		j.Kind = string(id.Kind)
		j.Status = "queued"
	}
	s.mu.Lock()
	s.jobs[j.ID] = j
	s.mu.Unlock()
	if j.Status == "invalid" {
		_ = json.NewEncoder(w).Encode(j)
		return
	}
	go s.run(j, id)
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(j)
}

func (s *server) run(j *job, id downloader.Identifier) {
	j.mu.Lock()
	j.Status = "running"
	j.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	outcome, err := s.dl.FetchPDF(ctx, id.Ref)
	j.mu.Lock()
	defer j.mu.Unlock()
	if outcome != nil {
		j.Attempts = outcome.Trace
		j.URL = outcome.URL
		j.Strategy = outcome.Strategy
	}
	if err != nil {
		j.Status = "failed"
		j.Error = err.Error()
		return
	}
	// Persist the bytes for pickup; the single-use token guards access.
	os.MkdirAll("/tmp/downloaderproxy", 0o700)
	path := filepath.Join("/tmp/downloaderproxy", j.ID+".pdf")
	if err := writeAll(path, outcome.Result.Body); err != nil {
		j.Status = "failed"
		j.Error = "store: " + err.Error()
		return
	}
	token := "dlt_" + randHex(20)
	j.Status = "done"
	j.Size = outcome.Result.Size
	j.Sha256 = outcome.Result.Sha256
	j.FileToken = token
	j.ExpiresAt = time.Now().Add(fileTTL)
	j.filePath = path
	s.mu.Lock()
	s.tokens[token] = j.ID
	s.mu.Unlock()
}

func (s *server) handleJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/jobs/")
	s.mu.Lock()
	j, ok := s.jobs[id]
	s.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "no such job"})
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.Status == "done" && (j.FileToken == "" || time.Now().After(j.ExpiresAt)) {
		// Token consumed/expired — strip it from the view.
		stripped := *j
		stripped.FileToken = ""
		_ = json.NewEncoder(w).Encode(stripped)
		return
	}
	_ = json.NewEncoder(w).Encode(j)
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/v1/files/")
	s.mu.Lock()
	id, ok := s.tokens[token]
	if ok {
		delete(s.tokens, token) // single use
	}
	s.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "unknown or already-used token"})
		return
	}
	s.mu.Lock()
	j, ok := s.jobs[id]
	s.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	j.mu.Lock()
	path, expired := j.filePath, time.Now().After(j.ExpiresAt)
	j.filePath = "" // consumed
	j.mu.Unlock()
	if expired || path == "" {
		_ = os.Remove(path)
		w.WriteHeader(http.StatusGone)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "token expired"})
		return
	}
	f, err := os.Open(path)
	if err != nil {
		w.WriteHeader(http.StatusGone)
		return
	}
	defer f.Close()
	defer os.Remove(path)
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("X-Qatlas-Sha256", j.Sha256)
	_, _ = io.Copy(w, f)
}

func (s *server) sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, j := range s.jobs {
		j.mu.Lock()
		expired := j.ExpiresAt != (time.Time{}) && time.Now().After(j.ExpiresAt.Add(fileTTL))
		if j.filePath != "" && expired {
			_ = os.Remove(j.filePath)
			j.filePath = ""
		}
		// Keep terminal jobs listable for an hour, then forget.
		if expired && time.Since(j.ExpiresAt) > time.Hour {
			delete(s.jobs, id)
		}
		j.mu.Unlock()
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func writeAll(path string, r io.Reader) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".part-*")
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
