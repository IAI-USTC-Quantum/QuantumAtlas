package downloadworker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
)

func testClient(t *testing.T, url string) *Client {
	t.Helper()
	cfg := DefaultConfig()
	cfg.MasterURL = url
	cfg.AllowHTTP = true
	cfg.Name = "test"
	c, err := NewClient(cfg, Identity{Secret: strings.Repeat("s", 64)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}
func TestRedirectCannotLeakWorkerCredential(t *testing.T) {
	var leaked atomic.Int32
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer dest.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dest.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	c := testClient(t, origin.URL)
	_, err := c.Status(context.Background())
	if err == nil {
		t.Fatal("redirect accepted")
	}
	if leaked.Load() != 0 {
		t.Fatal("redirect reached destination")
	}
	if strings.Contains(err.Error(), c.secret) || strings.Contains(err.Error(), origin.URL) {
		t.Fatal("error leaked credentials or URL")
	}
}
func TestHTTPSRequiredAndResponseValidation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Name = "test"
	cfg.MasterURL = "http://localhost:1234"
	if _, err := NewClient(cfg, Identity{}); err == nil {
		t.Fatal("plaintext allowed without opt-in")
	}
	cfg.AllowHTTP = true
	cfg.MasterURL = "http://user:secret@localhost"
	if err := cfg.Validate(); err == nil {
		t.Fatal("URL credentials allowed")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, strings.Repeat("s", (1<<20)+1)) }))
	defer server.Close()
	c := testClient(t, server.URL)
	if _, err := c.Status(context.Background()); err == nil {
		t.Fatal("oversized response accepted")
	}
}
func TestUploadRetryAndStagedReceiptRetainsUntilArchive(t *testing.T) {
	s := testSpool(t, MaxPDFBytes, time.Hour)
	record := ready(t, s, "retry", time.Now())
	var puts atomic.Int32
	var state atomic.Value
	state.Store("running")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 64) {
			t.Error("missing independent credential")
		}
		switch {
		case strings.HasPrefix(r.URL.Path, workerprotocol.ReceiptPath):
			_ = json.NewEncoder(w).Encode(receiptFor(record, state.Load().(string)))
		case strings.HasPrefix(r.URL.Path, workerprotocol.UploadPath):
			n := puts.Add(1)
			body, _ := io.ReadAll(r.Body)
			if !bytes.Equal(body, []byte("%PDF-test body")) {
				t.Error("wrong body")
			}
			if r.Header.Get(workerprotocol.HeaderSHA256) != record.SHA256 || r.Header.Get(workerprotocol.HeaderResultMetadata) == "" {
				t.Error("missing result integrity/provenance headers")
			}
			if n == 1 {
				http.Error(w, "sensitive upstream details", http.StatusServiceUnavailable)
				return
			}
			state.Store("staged")
			_ = json.NewEncoder(w).Encode(receiptFor(record, "staged"))
		default:
			t.Error("unexpected request", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	runner := Runner{Spool: s, Client: testClient(t, server.URL)}
	if err := runner.deliver(context.Background(), record); err == nil {
		t.Fatal("failed upload accepted")
	}
	if len(s.Records()) != 1 {
		t.Fatal("failed upload lost result")
	}
	if err := runner.deliver(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if len(s.Records()) != 1 {
		t.Fatal("HTTP success/staged removed result")
	}
	if err := runner.deliver(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if puts.Load() != 2 {
		t.Fatal("staged body reuploaded instead of checking receipt")
	}
	state.Store("done")
	if err := runner.deliver(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if len(s.Records()) != 0 {
		t.Fatal("archived result retained")
	}
}
func TestReceiptRecoversLostUploadAcknowledgement(t *testing.T) {
	s := testSpool(t, MaxPDFBytes, time.Hour)
	record := ready(t, s, "lost", time.Now())
	var puts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts.Add(1)
		}
		_ = json.NewEncoder(w).Encode(receiptFor(record, "done"))
	}))
	defer server.Close()
	runner := Runner{Spool: s, Client: testClient(t, server.URL)}
	if err := runner.deliver(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if puts.Load() != 0 || len(s.Records()) != 0 {
		t.Fatal("archived receipt did not recover acknowledgement loss")
	}
}
