package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
)

type storageFault struct {
	objstore.Store
	statErr, getErr              error
	lazyErr, blockStat, blockGet bool
	deadline                     time.Time
}

func (s *storageFault) Stat(ctx context.Context, key string) (objstore.ObjectInfo, bool, error) {
	s.deadline, _ = ctx.Deadline()
	if s.blockStat {
		<-ctx.Done()
		return objstore.ObjectInfo{}, false, ctx.Err()
	}
	if s.statErr != nil {
		return objstore.ObjectInfo{}, false, s.statErr
	}
	return s.Store.Stat(ctx, key)
}
func (s *storageFault) Get(ctx context.Context, key string) (io.ReadCloser, objstore.ObjectInfo, error) {
	s.deadline, _ = ctx.Deadline()
	if s.blockGet {
		<-ctx.Done()
		return nil, objstore.ObjectInfo{}, ctx.Err()
	}
	if s.getErr != nil {
		return nil, objstore.ObjectInfo{}, s.getErr
	}
	if s.lazyErr {
		return io.NopCloser(faultReader{}), objstore.ObjectInfo{Size: 9}, nil
	}
	return s.Store.Get(ctx, key)
}

type faultReader struct{}

func (faultReader) Read([]byte) (int, error) {
	return 0, errors.New("private-storage-endpoint: connection reset by peer")
}

func TestMarkdownReadHonorsEarlierDeadline(t *testing.T) {
	c, base, _ := newReadingFixture(t)
	store := &storageFault{Store: base, blockGet: true}
	conv := &readTestConverter{job: &mineru.Job{State: mineru.JobStateQueued}}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	re := newTestReqEvent(httptest.NewRequest(http.MethodGet, "/api/papers/"+readTestPaper+"/read?source_id="+readTestSource, nil).WithContext(ctx), rec)
	start := time.Now()
	if err := contentReadHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, conv, readTestPaper); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 503 || time.Since(start) > time.Second || conv.ensureCalls != 0 {
		t.Fatalf("unbounded/acquiring read %d", rec.Code)
	}
	if store.deadline.IsZero() || store.deadline.After(time.Now().Add(time.Second)) {
		t.Fatal("earlier request deadline lost")
	}
}

func TestImmutablePDFStatOutageAndRecovery(t *testing.T) {
	c, base := newAccessFixture(t)
	id := c.paper.ArxivID + "v1"
	key := paperassets.AssetKey("pdf", id)
	pdf := []byte("%PDF-1.4\nstat recovery source\n")
	if _, err := base.Put(t.Context(), key, bytes.NewReader(pdf), int64(len(pdf)), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	store := &storageFault{Store: base, statErr: errors.New("private-storage-endpoint: stat outage")}
	request := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		re := newTestReqEvent(httptest.NewRequest(http.MethodGet, "/api/papers/"+id+"/pdf", nil), rec)
		if err := contentPDFHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, nil, id); err != nil {
			t.Fatal(err)
		}
		return rec
	}
	failed := request()
	if failed.Code != 503 || len(c.sources) != 0 || len(c.imports) != 0 {
		t.Fatalf("stat outage treated as absence/minted source: %d", failed.Code)
	}
	if strings.Contains(failed.Body.String(), "private-storage-endpoint") || failed.Header().Get("Retry-After") == "" {
		t.Fatal("storage error disclosure/retry contract")
	}
	store.statErr = nil
	ready := request()
	if ready.Code != 200 || !bytes.Equal(ready.Body.Bytes(), pdf) || len(c.sources) != 1 {
		t.Fatalf("storage recovery not immediate: %d %s", ready.Code, ready.Body.String())
	}
}

func TestRecordedMarkdownStorageFailure(t *testing.T) {
	for _, path := range []string{"arxiv", "doi", "arxiv-status", "doi-status", "json"} {
		for _, fault := range []string{"get", "lazy-read"} {
			t.Run(path+"/"+fault, func(t *testing.T) {
				c, base, bundle := newDOIReadingFixture(t)
				// Both identifiers resolve this same paper, but explicit source pins avoid
				// relying on the historical origin to authorize a semantic version alias.
				conv := &readTestConverter{job: &mineru.Job{State: mineru.JobStateQueued}}
				s := &storageFault{Store: base}
				if fault == "get" {
					s.getErr = errors.New("private-storage-endpoint: read outage")
				} else {
					s.lazyErr = true
				}
				request := func() *httptest.ResponseRecorder {
					rec := httptest.NewRecorder()
					re := newTestReqEvent(httptest.NewRequest(http.MethodGet, "/api/papers/"+readTestPaper+"/read?source_id="+readTestSource, nil), rec)
					var err error
					switch path {
					case "arxiv":
						err = contentDerivativeHandler(re, &config.Config{PaperAccessEnabled: true}, s, c, conv, readTestPaper, "markdown")
					case "doi":
						err = contentDerivativeHandler(re, &config.Config{PaperAccessEnabled: true}, s, c, conv, c.doi, "markdown")
					case "arxiv-status":
						err = contentReadStatusHandler(re, &config.Config{PaperAccessEnabled: true}, s, c, conv, readTestPaper)
					case "doi-status":
						err = contentReadStatusHandler(re, &config.Config{PaperAccessEnabled: true}, s, c, conv, c.doi)
					case "json":
						err = paperBundleFileHandler(re, &config.Config{PaperAccessEnabled: true}, s, c, readTestPaper, bundle.RevisionID, bundle.MiddlePath)
					}
					if err != nil {
						t.Fatal(err)
					}
					return rec
				}
				rec := request()
				if rec.Code != 503 {
					t.Fatalf("outage %d %s", rec.Code, rec.Body.String())
				}
				var body map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body["code"] != "asset_store_unavailable" || body["retryable"] != true || rec.Header().Get("Retry-After") == "" || strings.Contains(rec.Body.String(), "private-storage-endpoint") {
					t.Fatalf("retry/privacy contract: %v", body)
				}
				if s.deadline.IsZero() || time.Until(s.deadline) > objstore.ReadTimeout || conv.ensureCalls != 0 {
					t.Fatal("outage unbounded or submitted inference")
				}
				s.getErr = nil
				s.lazyErr = false
				recovered := request()
				if recovered.Code != 200 || conv.ensureCalls != 0 {
					t.Fatalf("outage persisted after recovery: %d", recovered.Code)
				}
			})
		}
	}
}
