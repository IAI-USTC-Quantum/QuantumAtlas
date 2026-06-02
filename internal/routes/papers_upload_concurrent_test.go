package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/papers"
	"github.com/pocketbase/pocketbase/core"
)

// TestUploadPDFHandler_ConcurrentDifferentBytes is the cross-cutting
// race test that motivated the whole conditional-PUT refactor:
//
//   - Two clients race to upload DIFFERENT PDF bytes for the same
//     arxiv_id, neither sets overwrite=true.
//   - With the old Stat→decide→Put pipeline, both could pass the
//     "object absent" check and then both Put — last writer silently
//     wins, the loser's response says 201 but its bytes vanish.
//   - With the new uploadOne flow each goroutine sends Put with
//     If-None-Match="*"; LocalStore atomically resolves this via
//     os.Link, so exactly one create wins. The loser sees 412, Stats,
//     finds a different sha, and gets a clean 409.
//
// We assert:
//   - Exactly one of N concurrent uploads returns 201; the others all
//     return 409 (no silent overwrite, no 500s).
//   - The single object on disk has the sha256 of the winner — no
//     last-writer-wins, no corruption.
func TestUploadPDFHandler_ConcurrentDifferentBytes(t *testing.T) {
	const arxivID = "2501.99999v1"
	const concurrency = 8
	const minPDF = "%PDF-1.4\n%integration-test\n"

	tmp := t.TempDir()
	store, err := objstore.NewLocalStore(filepath.Join(tmp, "raw"))
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	cfg := &config.Config{}
	catalog := papers.NewStore(nil)

	var (
		wg          sync.WaitGroup
		mu          sync.Mutex
		statusByIdx = make([]int, concurrency)
		bodyByIdx   = make([]map[string]any, concurrency)
		bytesByIdx  = make([][]byte, concurrency)
	)

	// Use a starting-gate channel so all goroutines hit Put at roughly
	// the same wall-clock moment. Without it the test is still
	// correct, but with it we maximise the chance of a real race in
	// case the implementation regresses to a non-atomic create.
	gate := make(chan struct{})

	for i := 0; i < concurrency; i++ {
		i := i
		// Each goroutine uploads bytes that differ in the trailing
		// padding so every sha256 is distinct.
		pdfBytes := []byte(minPDF + fmt.Sprintf("worker-%d-padding\n", i))
		bytesByIdx[i] = pdfBytes

		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate

			req := buildUploadPDFRequest(t, arxivID, pdfBytes)
			rec := httptest.NewRecorder()
			re := &core.RequestEvent{}
			re.Request = req
			re.Response = rec

			if err := uploadPDFHandler(re, cfg, store, catalog, arxivID); err != nil {
				t.Errorf("worker %d: uploadPDFHandler returned err: %v", i, err)
			}

			var body map[string]any
			if rec.Body.Len() > 0 {
				_ = json.Unmarshal(rec.Body.Bytes(), &body)
			}

			mu.Lock()
			statusByIdx[i] = rec.Code
			bodyByIdx[i] = body
			mu.Unlock()
		}()
	}

	close(gate)
	wg.Wait()

	// Tally outcomes.
	var winners int32
	var conflicts int32
	var others []int
	var winnerIdx = -1
	for i, code := range statusByIdx {
		switch code {
		case http.StatusCreated:
			atomic.AddInt32(&winners, 1)
			winnerIdx = i
		case http.StatusConflict:
			atomic.AddInt32(&conflicts, 1)
		default:
			others = append(others, code)
		}
	}

	if len(others) > 0 {
		t.Fatalf("got unexpected status codes %v; full responses: %+v", others, bodyByIdx)
	}
	if winners != 1 {
		t.Fatalf("expected exactly 1 winner (201), got %d. statuses=%v bodies=%+v",
			winners, statusByIdx, bodyByIdx)
	}
	if int(conflicts) != concurrency-1 {
		t.Fatalf("expected %d conflicts (409), got %d. statuses=%v",
			concurrency-1, conflicts, statusByIdx)
	}

	// The object that actually landed in storage must match the
	// winner's bytes — last-writer-wins would manifest as some other
	// goroutine's bytes here.
	pdfKey := paperassets.AssetKey("pdf", arxivID)
	r, info, err := store.Get(context.Background(), pdfKey)
	if err != nil {
		t.Fatalf("store.Get(%s): %v", pdfKey, err)
	}
	defer r.Close()
	stored, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stored object: %v", err)
	}
	if !bytes.Equal(stored, bytesByIdx[winnerIdx]) {
		t.Fatalf("stored bytes do not match the winner's payload — "+
			"a non-winner's bytes leaked through (race regression)\nwinner=%d "+
			"stored_size=%d winner_size=%d", winnerIdx, len(stored), len(bytesByIdx[winnerIdx]))
	}
	if info.Size != int64(len(bytesByIdx[winnerIdx])) {
		t.Errorf("info.Size = %d, want %d", info.Size, len(bytesByIdx[winnerIdx]))
	}
}

// TestUploadPDFHandler_ConcurrentIdenticalBytes asserts the
// content-aware idempotency property: N goroutines uploading the SAME
// bytes for the same arxiv_id must all succeed (no 409s), with at most
// one 201 and the rest reporting unchanged. This is the "same client
// retried N times due to flaky network" scenario.
func TestUploadPDFHandler_ConcurrentIdenticalBytes(t *testing.T) {
	const arxivID = "2501.88888v2"
	const concurrency = 8
	pdfBytes := []byte("%PDF-1.4\n%shared-payload\nbody\n")

	tmp := t.TempDir()
	store, err := objstore.NewLocalStore(filepath.Join(tmp, "raw"))
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	cfg := &config.Config{}
	catalog := papers.NewStore(nil)

	var (
		wg          sync.WaitGroup
		mu          sync.Mutex
		statusByIdx = make([]int, concurrency)
		bodyByIdx   = make([]map[string]any, concurrency)
	)

	gate := make(chan struct{})

	for i := 0; i < concurrency; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate

			req := buildUploadPDFRequest(t, arxivID, pdfBytes)
			rec := httptest.NewRecorder()
			re := &core.RequestEvent{}
			re.Request = req
			re.Response = rec

			if err := uploadPDFHandler(re, cfg, store, catalog, arxivID); err != nil {
				t.Errorf("worker %d: uploadPDFHandler returned err: %v", i, err)
			}

			var body map[string]any
			if rec.Body.Len() > 0 {
				_ = json.Unmarshal(rec.Body.Bytes(), &body)
			}

			mu.Lock()
			statusByIdx[i] = rec.Code
			bodyByIdx[i] = body
			mu.Unlock()
		}()
	}

	close(gate)
	wg.Wait()

	var created, unchanged, conflicts int
	for i, code := range statusByIdx {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusOK:
			unchanged++
			if got, _ := bodyByIdx[i]["unchanged"].(bool); !got {
				t.Errorf("worker %d returned 200 but body unchanged=%v (want true)", i, bodyByIdx[i]["unchanged"])
			}
		case http.StatusConflict:
			conflicts++
		default:
			t.Errorf("worker %d unexpected status %d body=%+v", i, code, bodyByIdx[i])
		}
	}

	if conflicts != 0 {
		t.Fatalf("identical-content concurrent upload must never 409, got %d conflicts. statuses=%v",
			conflicts, statusByIdx)
	}
	if created < 1 {
		t.Errorf("expected at least 1 winner (201), got %d", created)
	}
	if created+unchanged != concurrency {
		t.Errorf("201+200 must cover all workers, got 201=%d 200=%d total=%d",
			created, unchanged, concurrency)
	}
}

// buildUploadPDFRequest constructs a multipart/form-data request body
// targeting /api/papers/{arxiv_id}/upload-pdf. The arxiv_id is only
// embedded in the URL for parity with production; the handler takes it
// as a separate parameter in the test call.
func buildUploadPDFRequest(t *testing.T, arxivID string, pdfBytes []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", `form-data; name="pdf"; filename="paper.pdf"`)
	hdr.Set("Content-Type", "application/pdf")
	pw, err := mw.CreatePart(hdr)
	if err != nil {
		t.Fatalf("CreatePart pdf: %v", err)
	}
	if _, err := pw.Write(pdfBytes); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	if err := mw.Close(); err != nil {
		t.Fatalf("multipart close: %v", err)
	}

	url := "/api/papers/" + arxivID + "/upload-pdf"
	req := httptest.NewRequest(http.MethodPost, url, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}
