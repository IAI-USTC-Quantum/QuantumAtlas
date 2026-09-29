//go:build integration

package objstore

// Real-S3 GetRange integration test. Targets any S3-compatible backend
// via the standard QATLAS_S3_TEST_* env contract (same gating as
// s3_test.go):
//
//	QATLAS_S3_TEST_ENDPOINT       e.g. http://127.0.0.1:55400
//	QATLAS_S3_TEST_BUCKET         pre-existing bucket
//	QATLAS_S3_TEST_ACCESS_KEY_ID
//	QATLAS_S3_TEST_SECRET_ACCESS_KEY
//
// The backend used during hardening was RustFS (qatlas's production
// object store; the open-source MinIO server is archived upstream, so
// the minio container the plan sketched is no longer pullable — RustFS
// is the like-for-like substitute, wire-compatible via minio-go).
//
// Coverage: an 8 MiB deterministic fake PDF is PUT once; then
//   - GetRange windows (head / middle / tail / whole / past-EOF)
//     must return byte-identical slices (sha-compared),
//   - a presigned URL + raw HTTP Range request must answer 206 with
//     the same bytes (proves the Range header path end to end, not
//     just through minio-go's abstraction).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// fakePDF8MiB builds a deterministic 8 MiB pseudo-PDF: a %PDF-1.4 magic
// header followed by a cheap deterministic stream (position-modulo
// table). Deterministic so failures print comparable shas.
func fakePDF8MiB() []byte {
	const size = 8 << 20
	b := make([]byte, size)
	copy(b, "%PDF-1.4\n%qatlas hardening range fixture\n")
	for i := 32; i < size; i += 16 {
		b[i] = byte(i >> 8)
		b[i+1] = byte(i)
		b[i+2] = byte(i % 251)
	}
	return b
}

func TestIntegrationS3GetRange(t *testing.T) {
	store, prefix := newS3(t)
	env := s3TestEnv{prefix: prefix}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	body := fakePDF8MiB()
	sum := sha256.Sum256(body)
	key := env.prefix + "range/8mb-fake.pdf"
	if _, err := store.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "application/pdf"); err != nil {
		t.Fatalf("put 8MiB: %v", err)
	}
	t.Cleanup(func() { _ = store.Delete(context.Background(), key) })

	readRange := func(start, end int64) []byte {
		t.Helper()
		rc, err := store.GetRange(ctx, key, start, end)
		if err != nil {
			t.Fatalf("GetRange(%d,%d): %v", start, end, err)
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read GetRange(%d,%d): %v", start, end, err)
		}
		return b
	}
	window := func(start, end int64) []byte { return body[start : end+1] }

	// (a) head window.
	if got := readRange(0, 1<<20); !bytes.Equal(got, window(0, 1<<20)) {
		t.Errorf("head window mismatch")
	}
	// (b) middle window.
	if got := readRange(3<<20, 3<<20+12345); !bytes.Equal(got, window(3<<20, 3<<20+12345)) {
		t.Errorf("middle window mismatch")
	}
	// (c) tail window.
	if got := readRange(int64(len(body))-4096, int64(len(body))-1); !bytes.Equal(got, window(int64(len(body))-4096, int64(len(body))-1)) {
		t.Errorf("tail window mismatch")
	}
	// (d) window extending past EOF clamps.
	if got := readRange(int64(len(body))-100, int64(len(body))+99999); !bytes.Equal(got, body[int64(len(body))-100:]) {
		t.Errorf("clamped window mismatch")
	}
	// (e) past-EOF start yields empty, not ErrNotFound.
	if got := readRange(int64(len(body)), int64(len(body))+10); len(got) != 0 {
		t.Errorf("past-EOF start = %d bytes, want 0", len(got))
	}
	// (f) whole object via range == sha of body.
	got := readRange(0, int64(len(body))-1)
	gs := sha256.Sum256(got)
	if hex.EncodeToString(gs[:]) != hex.EncodeToString(sum[:]) {
		t.Errorf("whole-object range sha mismatch: got %s want %s", hex.EncodeToString(gs[:]), hex.EncodeToString(sum[:]))
	}

	// (g) raw HTTP Range against a presigned URL → 206 + exact bytes.
	// Range is not part of the SigV4 canonical request, so a signed
	// plain GET URL accepts an added Range header.
	url, ok, err := store.PresignGet(ctx, key, 5*time.Minute)
	if err != nil || !ok {
		t.Fatalf("presign: ok=%v err=%v", ok, err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", 2<<20, 2<<20+65535))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("presigned ranged GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("presigned ranged GET status = %d, want 206", resp.StatusCode)
	}
	partial, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(partial, window(2<<20, 2<<20+65535)) {
		t.Errorf("206 body mismatch (%d bytes)", len(partial))
	}
	if cr := resp.Header.Get("Content-Range"); cr == "" {
		t.Error("206 response missing Content-Range header")
	}

	// (h) missing key.
	if _, err := store.GetRange(ctx, env.prefix+"range/nope.pdf", 0, 9); !IsNotFound(err) {
		t.Errorf("missing key err = %v, want ErrNotFound", err)
	}
}
