package mineru

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveUploadChannel is a REAL end-to-end check against mineru.net's
// upload channel (POST /api/v4/file-urls/batch → PUT bytes → poll
// /api/v4/extract-results/batch/{id}). It is skipped unless:
//
//	MINERU_LIVE_TEST=1       opt-in gate
//	MINERU_API_TOKEN=...     a real MinerU API token (never logged)
//
// Optional: MINERU_API_BASE_URL to point at a staging host.
//
// The test uploads a tiny generated one-page PDF with model_version=vlm,
// logs every observed state transition, and — on success — downloads the
// result zip and reports whether it contains markdown + images.
func TestLiveUploadChannel(t *testing.T) {
	if os.Getenv("MINERU_LIVE_TEST") != "1" {
		t.Skip("set MINERU_LIVE_TEST=1 (and MINERU_API_TOKEN) to run the live MinerU upload check")
	}
	token := os.Getenv("MINERU_API_TOKEN")
	if token == "" {
		t.Fatal("MINERU_API_TOKEN not set")
	}

	pdf := buildMinimalPDF(t)
	t.Logf("generated test PDF: %d bytes", len(pdf))

	client := NewClient(token, os.Getenv("MINERU_API_BASE_URL"), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	dataID := "qatlas-live-upload-test"
	batchID, urls, err := client.ApplyUploadURLs(ctx, []string{"live_test.pdf"}, SubmitOptions{
		ModelVersion:  "vlm",
		Language:      "en",
		EnableFormula: true,
		EnableTable:   true,
		DataID:        dataID,
	})
	if err != nil {
		t.Fatalf("ApplyUploadURLs: %v", err)
	}
	t.Logf("batch_id=%s upload_urls=%d", batchID, len(urls))

	if err := client.UploadFile(ctx, urls[0], bytes.NewReader(pdf), int64(len(pdf))); err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	t.Log("upload PUT accepted")

	var lastState string
	var zipURL string
	deadline := time.Now().Add(8 * time.Minute)
	for time.Now().Before(deadline) {
		results, err := client.GetBatch(ctx, batchID)
		if err != nil {
			t.Fatalf("GetBatch: %v", err)
		}
		if len(results) > 0 {
			st := results[0]
			for _, r := range results {
				if r.DataID == dataID {
					st = r
					break
				}
			}
			if st.State != lastState {
				t.Logf("state: %q → %q (data_id=%q)", lastState, st.State, st.DataID)
				lastState = st.State
			}
			switch st.State {
			case "done":
				zipURL = st.FullZipURL
			case "failed":
				t.Fatalf("MinerU reported failed: %s", st.ErrMsg)
			}
			if zipURL != "" {
				break
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("poll timeout: %v", ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}
	if zipURL == "" {
		t.Fatalf("never reached done within 8m (last state %q)", lastState)
	}
	t.Logf("full_zip_url present: yes (%s…)", zipURL[:min(60, len(zipURL))])

	res, err := client.FetchResult(ctx, zipURL)
	if err != nil {
		t.Fatalf("FetchResult: %v", err)
	}
	t.Logf("result zip: markdown=%d bytes, images=%d", len(res.Markdown), len(res.Images))
	if len(res.Markdown) == 0 {
		t.Fatal("result markdown is empty")
	}
	if !strings.Contains(string(res.Markdown), "QuantumAtlas") {
		t.Logf("WARN: markdown does not mention the test sentence; first 200 bytes: %q", res.Markdown[:min(200, len(res.Markdown))])
	}
}

// buildMinimalPDF returns a small but structurally valid one-page PDF
// (with a correct xref table) containing one line of text.
func buildMinimalPDF(t *testing.T) []byte {
	t.Helper()
	content := "BT /F1 24 Tf 72 700 Td (QuantumAtlas MinerU live upload test) Tj ET"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, 0, len(objects))
	for i, obj := range objects {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xrefStart := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefStart)
	return buf.Bytes()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
