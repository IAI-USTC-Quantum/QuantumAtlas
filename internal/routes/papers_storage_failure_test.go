package routes

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

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
)

type storageFault struct {
	objstore.Store
	statErr, getErr error
	lazyErr         bool
	blockStat       bool
	deadline        time.Time
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

func TestMarkdownReadHonorsEarlierDeadline(t *testing.T) {
	store := &storageFault{Store: newDOIFlowStore(), blockStat: true}
	converter := newDOIFlowConverter(t, store, newDOIMinerUStub(t))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	re := newTestReqEvent(httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx), rec)
	start := time.Now()
	if err := markdownHandler(re, &config.Config{}, store, converter, "1010.4458v2"); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusServiceUnavailable || time.Since(start) > time.Second {
		t.Fatalf("unbounded response: %d", rec.Code)
	}
	if _, exists := converter.Lookup("1010.4458v2"); exists {
		t.Fatal("deadline failure queued conversion")
	}
}

func (s *storageFault) Get(ctx context.Context, key string) (io.ReadCloser, objstore.ObjectInfo, error) {
	s.deadline, _ = ctx.Deadline()
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

func TestRecordedMarkdownStorageFailure(t *testing.T) {
	const id = "1010.4458v2"
	const doi = "10.1103/test"
	for _, path := range []string{"arxiv", "doi", "arxiv-status", "doi-status", "json"} {
		for _, fault := range []string{"stat", "get", "lazy-read"} {
			if strings.HasSuffix(path, "status") && fault != "stat" {
				continue
			}
			if path == "doi" && fault == "stat" {
				continue
			}
			t.Run(path+"/"+fault, func(t *testing.T) {
				store := newDOIFlowStore()
				for _, key := range []string{paperassets.AssetKey("markdown", id), paperassets.AssetKey("json", id), paperassets.DOIAssetKey("markdown", doi)} {
					store.Put(context.Background(), key, strings.NewReader("recorded"), 8, "text/plain")
				}
				s := &storageFault{Store: store}
				switch fault {
				case "stat":
					s.statErr = errors.New("private-storage-endpoint: connection reset by peer")
				case "get":
					s.getErr = errors.New("private-storage-endpoint: connection reset by peer")
				case "lazy-read":
					s.lazyErr = true
				}
				converter := newDOIFlowConverter(t, s, newDOIMinerUStub(t))
				rec := httptest.NewRecorder()
				re := newTestReqEvent(httptest.NewRequest(http.MethodGet, "/api/papers/"+id+"/markdown", nil), rec)
				var err error
				switch path {
				case "arxiv":
					err = markdownHandler(re, &config.Config{}, s, converter, id)
				case "doi":
					err = getMarkdownByDOIHandler(re, &config.Config{}, s, converter, doi, "")
				case "arxiv-status":
					err = markdownStatusHandler(re, &config.Config{}, s, converter, id)
				case "doi-status":
					err = markdownStatusByDOIHandler(re, s, converter, doi)
				case "json":
					err = serveReadyAsset(re, s, "json", id, "bytes")
				}
				if err != nil {
					t.Fatal(err)
				}
				if rec.Code != http.StatusServiceUnavailable {
					t.Fatalf("status=%d body=%s; want 503", rec.Code, rec.Body.String())
				}
				var body map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body["code"] != "asset_store_unavailable" || body["retryable"] != true || rec.Header().Get("Retry-After") == "" {
					t.Fatalf("missing retry contract: %v", body)
				}
				if strings.Contains(rec.Body.String(), "private-storage-endpoint") {
					t.Fatal("leaked storage details")
				}
				if s.deadline.IsZero() || time.Until(s.deadline) > 11*time.Second {
					t.Fatal("storage read has no bounded deadline")
				}
				if _, found := converter.Lookup(id); found {
					t.Fatal("storage outage queued conversion")
				}
				if _, found := converter.LookupDOI(doi); found {
					t.Fatal("storage outage queued DOI conversion")
				}
				if _, present, _ := store.Stat(context.Background(), paperassets.AssetKey("markdown", id)); !present {
					t.Fatal("recorded assets removed")
				}
			})
		}
	}
}
