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
