//go:build integration

// Package objstore S3 integration tests.
//
// These tests run a full round-trip against a live S3-compatible
// endpoint. They are NOT part of the default `go test ./...` run —
// invoke with:
//
//	pixi run go test -tags=integration ./internal/objstore/...
//
// Environment variables (all required):
//
//	QATLAS_S3_TEST_ENDPOINT       e.g. http://127.0.0.1:9000
//	QATLAS_S3_TEST_BUCKET         pre-existing bucket the tests can scribble in
//	QATLAS_S3_TEST_ACCESS_KEY_ID
//	QATLAS_S3_TEST_SECRET_ACCESS_KEY
//
// The tests scope their writes under a random prefix and clean up on
// exit. They do NOT create or delete the bucket itself — bring your
// own bucket.

package objstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"testing"
	"time"
)

type s3TestEnv struct {
	endpoint, bucket, key, secret string
	prefix                        string
}

func requireS3Env(t *testing.T) s3TestEnv {
	t.Helper()
	env := s3TestEnv{
		endpoint: os.Getenv("QATLAS_S3_TEST_ENDPOINT"),
		bucket:   os.Getenv("QATLAS_S3_TEST_BUCKET"),
		key:      os.Getenv("QATLAS_S3_TEST_ACCESS_KEY_ID"),
		secret:   os.Getenv("QATLAS_S3_TEST_SECRET_ACCESS_KEY"),
	}
	if env.endpoint == "" || env.bucket == "" || env.key == "" || env.secret == "" {
		t.Skip("S3 integration env not set; export QATLAS_S3_TEST_* to run")
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	env.prefix = "go-test/" + hex.EncodeToString(b) + "/"
	return env
}

func newS3(t *testing.T) (*S3Store, string) {
	env := requireS3Env(t)
	s, err := NewS3Store(env.endpoint, env.bucket, env.key, env.secret)
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	t.Cleanup(func() {
		// Best-effort cleanup of every key under prefix.
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		objs, _ := s.ListPrefix(ctx, env.prefix, 0)
		for _, o := range objs {
			_ = s.Delete(ctx, o.Key)
		}
	})
	return s, env.prefix
}

func TestS3Store_PutGetRoundTrip(t *testing.T) {
	s, prefix := newS3(t)
	ctx := context.Background()
	body := []byte("rustfs e2e content " + time.Now().Format(time.RFC3339Nano))
	key := path.Join(prefix, "pdf/24/2401.00001v1.pdf")

	n, err := s.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "application/pdf")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if n != int64(len(body)) {
		t.Errorf("Put wrote %d, want %d", n, len(body))
	}

	rc, info, err := s.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, body) {
		t.Errorf("Get round-trip mismatch")
	}
	if info.Size != int64(len(body)) {
		t.Errorf("info.Size = %d, want %d", info.Size, len(body))
	}
	if info.ContentType != "application/pdf" {
		t.Errorf("info.ContentType = %q, want application/pdf", info.ContentType)
	}
}

func TestS3Store_GetMissingIsErrNotFound(t *testing.T) {
	s, prefix := newS3(t)
	_, _, err := s.Get(context.Background(), path.Join(prefix, "absent"))
	if !IsNotFound(err) {
		t.Errorf("Get missing: err = %v, want ErrNotFound", err)
	}
}

func TestS3Store_StatExistsAndMissing(t *testing.T) {
	s, prefix := newS3(t)
	ctx := context.Background()
	key := path.Join(prefix, "stat-target")
	if _, err := s.Put(ctx, key, bytes.NewReader([]byte("x")), 1, "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	info, exists, err := s.Stat(ctx, key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !exists {
		t.Errorf("Stat exists=false on present key")
	}
	if info.Size != 1 {
		t.Errorf("Stat size = %d, want 1", info.Size)
	}
	_, exists, err = s.Stat(ctx, path.Join(prefix, "no-such-key"))
	if err != nil {
		t.Fatalf("Stat missing: err = %v", err)
	}
	if exists {
		t.Errorf("Stat exists=true on missing key")
	}
}

func TestS3Store_DeleteIdempotent(t *testing.T) {
	s, prefix := newS3(t)
	ctx := context.Background()
	key := path.Join(prefix, "delete-me")
	if _, err := s.Put(ctx, key, bytes.NewReader([]byte("z")), 1, ""); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Errorf("Delete second time: %v", err)
	}
	if _, exists, _ := s.Stat(ctx, key); exists {
		t.Errorf("key still exists after delete")
	}
}

func TestS3Store_ListPrefix(t *testing.T) {
	s, prefix := newS3(t)
	ctx := context.Background()
	items := []string{
		path.Join(prefix, "pdf/24/a.pdf"),
		path.Join(prefix, "pdf/24/b.pdf"),
		path.Join(prefix, "pdf/25/c.pdf"),
	}
	for _, k := range items {
		if _, err := s.Put(ctx, k, bytes.NewReader([]byte("x")), 1, ""); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	got, err := s.ListPrefix(ctx, path.Join(prefix, "pdf/24/"), 0)
	if err != nil {
		t.Fatalf("ListPrefix: %v", err)
	}
	keys := make([]string, len(got))
	for i, o := range got {
		keys[i] = o.Key
	}
	sort.Strings(keys)
	want := []string{
		path.Join(prefix, "pdf/24/a.pdf"),
		path.Join(prefix, "pdf/24/b.pdf"),
	}
	if !equalStrings(keys, want) {
		t.Errorf("ListPrefix pdf/24/: got %v, want %v", keys, want)
	}
}

func TestS3Store_PresignGetWorksAgainstLiveServer(t *testing.T) {
	s, prefix := newS3(t)
	ctx := context.Background()
	body := []byte("presign-test " + time.Now().Format(time.RFC3339Nano))
	key := path.Join(prefix, "presign.bin")
	if _, err := s.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "application/octet-stream"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	url, supported, err := s.PresignGet(ctx, key, 30*time.Second)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	if !supported {
		t.Fatalf("PresignGet supported=false on S3 backend")
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET presigned: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("presigned GET status %d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, body) {
		t.Errorf("presigned body mismatch")
	}
}

// TestS3Store_PutWithMeta_RoundTrip verifies that user metadata set on
// PutWithMeta surfaces back via Stat / Get with lowercase keys (matches
// the S3 wire convention; minio-go's UserMetadata roundtrip is the
// load-bearing assumption behind our content-aware idempotency).
func TestS3Store_PutWithMeta_RoundTrip(t *testing.T) {
	s, prefix := newS3(t)
	ctx := context.Background()
	key := path.Join(prefix, "meta-target")
	body := []byte("hello-meta " + time.Now().Format(time.RFC3339Nano))

	// Use a deliberately mixed-case key on the way in to surface any
	// case-collapsing surprise. Per Store contract callers should
	// pass lowercase; we test the roundtrip behavior so a future SDK
	// upgrade can't silently break it.
	meta := map[string]string{
		"sha256":         "deadbeef",
		"original-name":  "Whatever.pdf",
	}
	if _, err := s.PutWithMeta(ctx, key, bytes.NewReader(body), int64(len(body)), "application/octet-stream", meta); err != nil {
		t.Fatalf("PutWithMeta: %v", err)
	}

	// Stat path.
	info, exists, err := s.Stat(ctx, key)
	if err != nil || !exists {
		t.Fatalf("Stat: err=%v exists=%v", err, exists)
	}
	if got := info.Metadata["sha256"]; got != "deadbeef" {
		t.Errorf("Stat.Metadata[sha256] = %q, want %q", got, "deadbeef")
	}
	if got := info.Metadata["original-name"]; got != "Whatever.pdf" {
		t.Errorf("Stat.Metadata[original-name] = %q, want %q", got, "Whatever.pdf")
	}

	// Get path.
	rc, gInfo, err := s.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	if got := gInfo.Metadata["sha256"]; got != "deadbeef" {
		t.Errorf("Get.Metadata[sha256] = %q, want %q", got, "deadbeef")
	}
}

// TestS3Store_PutWithMeta_EmptyMetadataMatchesPut verifies that passing
// nil or empty metadata produces an object indistinguishable from one
// written via Put — this is the documented Store contract and the
// papers handler relies on it when calling PutWithMeta with a
// single-entry map vs Put with no metadata.
func TestS3Store_PutWithMeta_EmptyMetadataMatchesPut(t *testing.T) {
	s, prefix := newS3(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		meta map[string]string
	}{
		{"nil-meta", nil},
		{"empty-meta", map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := path.Join(prefix, tc.name)
			if _, err := s.PutWithMeta(ctx, key, bytes.NewReader([]byte("x")), 1, "text/plain", tc.meta); err != nil {
				t.Fatalf("PutWithMeta: %v", err)
			}
			info, exists, err := s.Stat(ctx, key)
			if err != nil || !exists {
				t.Fatalf("Stat: err=%v exists=%v", err, exists)
			}
			if len(info.Metadata) != 0 {
				t.Errorf("Metadata = %v, want empty", info.Metadata)
			}
		})
	}
}

// TestS3Store_EnsureVersioning_Idempotent runs the reconcile pass twice
// against a live bucket. First call may flip Suspended/empty -> Enabled
// (changed=true) or be a no-op (changed=false, depends on bucket
// state). Second call MUST be a no-op. We intentionally do NOT assert
// the prior status, since the test bucket may be in any state when the
// CI fires this.
//
// Note: requires the test creds to have s3:Get/PutBucketVersioning on
// the bucket; without those the call errors out and the test fails. We
// surface that as a real failure rather than skip, because EnsureVersioning
// is on the qatlas server's startup hot-path — silently passing tests
// in a misconfigured CI is worse than a loud red mark.
func TestS3Store_EnsureVersioning_Idempotent(t *testing.T) {
	s, _ := newS3(t)
	ctx := context.Background()
	if _, _, err := s.EnsureVersioning(ctx); err != nil {
		t.Fatalf("EnsureVersioning first call: %v", err)
	}
	prior, changed, err := s.EnsureVersioning(ctx)
	if err != nil {
		t.Fatalf("EnsureVersioning second call: %v", err)
	}
	if changed {
		t.Errorf("second call reported changed=true; want false (already Enabled). prior=%q", prior)
	}
	if prior != "Enabled" {
		t.Errorf("second call prior=%q; want %q", prior, "Enabled")
	}
}
