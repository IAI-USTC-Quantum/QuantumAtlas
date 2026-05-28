package routes

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
)

// Why these tests live here (not in objstore):
//
// uploadOne encodes the *handler-side* race-safe upload policy — how
// qatlas drives the underlying store's conditional Put + Stat to decide
// what HTTP status to return. The store's conditional-Put semantics are
// tested in objstore/{local,s3}_test.go; this file tests how the
// handler reacts to each combination of (overwrite flag, store state).

// fakeStore is a minimal in-memory objstore.Store stub. It only
// implements Stat and PutWithOptions — the only methods uploadOne
// uses — plus enough bookkeeping for the tests to assert what got
// written and how the store responded.
type fakeStore struct {
	mu sync.Mutex

	// Behavior knobs (set per test).
	statFn func(ctx context.Context, key string) (objstore.ObjectInfo, bool, error)
	putFn  func(ctx context.Context, key string, opts objstore.PutOptions, body []byte) (int64, error)

	// Observations (read after the call).
	putCalls []putCall
}

type putCall struct {
	key  string
	opts objstore.PutOptions
	body []byte
}

func (f *fakeStore) Put(_ context.Context, _ string, _ io.Reader, _ int64, _ string) (int64, error) {
	panic("fakeStore.Put should not be called by uploadOne tests")
}
func (f *fakeStore) PutWithMeta(_ context.Context, _ string, _ io.Reader, _ int64, _ string, _ map[string]string) (int64, error) {
	panic("fakeStore.PutWithMeta should not be called by uploadOne tests")
}
func (f *fakeStore) PutWithOptions(ctx context.Context, key string, r io.Reader, _ int64, opts objstore.PutOptions) (int64, error) {
	body, _ := io.ReadAll(r)
	f.mu.Lock()
	f.putCalls = append(f.putCalls, putCall{key: key, opts: opts, body: body})
	f.mu.Unlock()
	if f.putFn == nil {
		return int64(len(body)), nil
	}
	return f.putFn(ctx, key, opts, body)
}
func (f *fakeStore) Get(_ context.Context, _ string) (io.ReadCloser, objstore.ObjectInfo, error) {
	panic("fakeStore.Get should not be called by uploadOne tests")
}
func (f *fakeStore) Stat(ctx context.Context, key string) (objstore.ObjectInfo, bool, error) {
	if f.statFn == nil {
		return objstore.ObjectInfo{}, false, nil
	}
	return f.statFn(ctx, key)
}
func (f *fakeStore) Delete(_ context.Context, _ string) error {
	panic("fakeStore.Delete should not be called by uploadOne tests")
}
func (f *fakeStore) ListPrefix(_ context.Context, _ string, _ int) ([]objstore.ObjectInfo, error) {
	panic("fakeStore.ListPrefix should not be called by uploadOne tests")
}
func (f *fakeStore) PresignGet(_ context.Context, _ string, _ time.Duration) (string, bool, error) {
	panic("fakeStore.PresignGet should not be called by uploadOne tests")
}

const testSha = "deadbeef00112233445566778899aabbccddeeff00112233445566778899aabb"
const otherSha = "0000beef00112233445566778899aabbccddeeff00112233445566778899aabb"

const dummyKey = "pdf/24/x.pdf"
const dummyLabel = "PDF"
const dummyContentType = "application/pdf"

var dummyBody = []byte("body-bytes")

// fakeStagedBody is a test helper that satisfies the stagedBody
// interface — we don't go through stageInMemory because the unit tests
// want to inject a specific sha256 hex string (testSha / otherSha)
// rather than the real digest of the body bytes. Lets us assert
// idempotency by sha mismatch without crafting payloads with known
// digests.
type fakeStagedBody struct {
	body []byte
	sha  string
}

func staged(body []byte, sha string) *fakeStagedBody {
	return &fakeStagedBody{body: body, sha: sha}
}

func (f *fakeStagedBody) Sha256() string { return f.sha }
func (f *fakeStagedBody) Size() int64    { return int64(len(f.body)) }
func (f *fakeStagedBody) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(f.body)), nil
}
func (f *fakeStagedBody) Close() error { return nil }

// -------- no-overwrite paths --------

// !overwrite + absent → uploadOne sends Put with IfNoneMatch="*" and
// gets a success back. Outcome: Written; exactly one PUT happened.
func TestUploadOne_NoOverwrite_AbsentWrites(t *testing.T) {
	store := &fakeStore{} // default Stat returns absent, default Put succeeds
	out, err := uploadOne(context.Background(), store, dummyKey, staged(dummyBody, testSha),
		dummyContentType, false, dummyLabel)
	if err != nil {
		t.Fatalf("uploadOne: %v", err)
	}
	if out.kind != outcomeWritten {
		t.Errorf("kind = %v, want outcomeWritten", out.kind)
	}
	if len(store.putCalls) != 1 {
		t.Fatalf("got %d PUT calls, want 1", len(store.putCalls))
	}
	if store.putCalls[0].opts.IfNoneMatch != "*" {
		t.Errorf("IfNoneMatch = %q, want \"*\"", store.putCalls[0].opts.IfNoneMatch)
	}
	if store.putCalls[0].opts.Metadata["sha256"] != testSha {
		t.Errorf("sha256 metadata = %q, want %q", store.putCalls[0].opts.Metadata["sha256"], testSha)
	}
}

// !overwrite + same content already there → server returns 412 →
// uploadOne Stats, sees matching sha → returns Unchanged. No retry PUT.
func TestUploadOne_NoOverwrite_SameShaIsUnchanged(t *testing.T) {
	store := &fakeStore{
		putFn: func(_ context.Context, _ string, _ objstore.PutOptions, _ []byte) (int64, error) {
			return 0, objstore.ErrPreconditionFailed
		},
		statFn: func(_ context.Context, _ string) (objstore.ObjectInfo, bool, error) {
			return objstore.ObjectInfo{Metadata: map[string]string{"sha256": testSha}}, true, nil
		},
	}
	out, err := uploadOne(context.Background(), store, dummyKey, staged(dummyBody, testSha),
		dummyContentType, false, dummyLabel)
	if err != nil {
		t.Fatalf("uploadOne: %v", err)
	}
	if out.kind != outcomeUnchanged {
		t.Errorf("kind = %v, want outcomeUnchanged", out.kind)
	}
	if out.existingSha != testSha {
		t.Errorf("existingSha = %q, want %q", out.existingSha, testSha)
	}
	if len(store.putCalls) != 1 {
		t.Errorf("expected exactly one PUT (the racy create-only attempt that 412'd); got %d", len(store.putCalls))
	}
}

// !overwrite + different content already there → 412 → Stat reveals
// mismatch → uploadOne returns Conflict carrying both hashes.
func TestUploadOne_NoOverwrite_DifferentShaConflicts(t *testing.T) {
	store := &fakeStore{
		putFn: func(_ context.Context, _ string, _ objstore.PutOptions, _ []byte) (int64, error) {
			return 0, objstore.ErrPreconditionFailed
		},
		statFn: func(_ context.Context, _ string) (objstore.ObjectInfo, bool, error) {
			return objstore.ObjectInfo{Metadata: map[string]string{"sha256": otherSha}}, true, nil
		},
	}
	out, err := uploadOne(context.Background(), store, dummyKey, staged(dummyBody, testSha),
		dummyContentType, false, dummyLabel)
	if err != nil {
		t.Fatalf("uploadOne: %v", err)
	}
	if out.kind != outcomeConflict {
		t.Errorf("kind = %v, want outcomeConflict", out.kind)
	}
	if out.existingSha != otherSha {
		t.Errorf("existingSha = %q, want %q", out.existingSha, otherSha)
	}
}

// !overwrite + legacy object (no sha256 metadata) → 412 → Stat reveals
// no metadata → Conflict with empty existingSha so the handler emits
// the "legacy upload or LocalStore backend" note.
func TestUploadOne_NoOverwrite_NoMetaConflicts(t *testing.T) {
	store := &fakeStore{
		putFn: func(_ context.Context, _ string, _ objstore.PutOptions, _ []byte) (int64, error) {
			return 0, objstore.ErrPreconditionFailed
		},
		statFn: func(_ context.Context, _ string) (objstore.ObjectInfo, bool, error) {
			return objstore.ObjectInfo{Metadata: nil}, true, nil
		},
	}
	out, err := uploadOne(context.Background(), store, dummyKey, staged(dummyBody, testSha),
		dummyContentType, false, dummyLabel)
	if err != nil {
		t.Fatalf("uploadOne: %v", err)
	}
	if out.kind != outcomeConflict {
		t.Errorf("kind = %v, want outcomeConflict", out.kind)
	}
	if out.existingSha != "" {
		t.Errorf("existingSha = %q, want empty (legacy object)", out.existingSha)
	}
}

// !overwrite + "object was there at 412, then deleted before Stat" race
// → uploadOne retries the create-only Put once. If that succeeds, we
// return Written (the racy delete left the slot open).
func TestUploadOne_NoOverwrite_RetryAfterTransientRaceWrites(t *testing.T) {
	var putCount int
	store := &fakeStore{
		putFn: func(_ context.Context, _ string, _ objstore.PutOptions, _ []byte) (int64, error) {
			putCount++
			if putCount == 1 {
				return 0, objstore.ErrPreconditionFailed
			}
			return int64(len(dummyBody)), nil
		},
		statFn: func(_ context.Context, _ string) (objstore.ObjectInfo, bool, error) {
			return objstore.ObjectInfo{}, false, nil
		},
	}
	out, err := uploadOne(context.Background(), store, dummyKey, staged(dummyBody, testSha),
		dummyContentType, false, dummyLabel)
	if err != nil {
		t.Fatalf("uploadOne: %v", err)
	}
	if out.kind != outcomeWritten {
		t.Errorf("kind = %v, want outcomeWritten (retry should succeed)", out.kind)
	}
	if putCount != 2 {
		t.Errorf("PUT count = %d, want 2 (initial 412 + retry)", putCount)
	}
}

// !overwrite + retry also 412s (aggressive concurrent writer races us
// twice) → uploadOne gives up and reports Conflict rather than
// livelocking.
func TestUploadOne_NoOverwrite_RetryAlso412ReportsConflict(t *testing.T) {
	store := &fakeStore{
		putFn: func(_ context.Context, _ string, _ objstore.PutOptions, _ []byte) (int64, error) {
			return 0, objstore.ErrPreconditionFailed
		},
		statFn: func(_ context.Context, _ string) (objstore.ObjectInfo, bool, error) {
			return objstore.ObjectInfo{}, false, nil // looks absent, but the next PUT will 412
		},
	}
	out, err := uploadOne(context.Background(), store, dummyKey, staged(dummyBody, testSha),
		dummyContentType, false, dummyLabel)
	if err != nil {
		t.Fatalf("uploadOne: %v", err)
	}
	if out.kind != outcomeConflict {
		t.Errorf("kind = %v, want outcomeConflict (livelock guard)", out.kind)
	}
}

// !overwrite + backend reports PreconditionUnsupported → uploadOne
// bubbles it up as a real error rather than silently falling back to a
// racy non-conditional Put. Both supported backends (S3/RustFS,
// LocalStore) implement If-None-Match="*"; if we ever see this in
// production it means a misconfigured backend that needs operator
// attention, not silent degradation to "first-writer-wins" semantics.
func TestUploadOne_NoOverwrite_PreconditionUnsupportedBubbles(t *testing.T) {
	store := &fakeStore{
		putFn: func(_ context.Context, _ string, _ objstore.PutOptions, _ []byte) (int64, error) {
			return 0, objstore.ErrPreconditionUnsupported
		},
	}
	_, err := uploadOne(context.Background(), store, dummyKey, staged(dummyBody, testSha),
		dummyContentType, false, dummyLabel)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !objstore.IsPreconditionUnsupported(err) {
		t.Errorf("error should wrap ErrPreconditionUnsupported, got %v", err)
	}
}

// Stat errors at any point bubble up so the handler returns 500.
func TestUploadOne_NoOverwrite_StatErrorBubblesUp(t *testing.T) {
	store := &fakeStore{
		putFn: func(_ context.Context, _ string, _ objstore.PutOptions, _ []byte) (int64, error) {
			return 0, objstore.ErrPreconditionFailed
		},
		statFn: func(_ context.Context, _ string) (objstore.ObjectInfo, bool, error) {
			return objstore.ObjectInfo{}, false, errors.New("rustfs down")
		},
	}
	_, err := uploadOne(context.Background(), store, dummyKey, staged(dummyBody, testSha),
		dummyContentType, false, dummyLabel)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "rustfs down") {
		t.Errorf("error missing root cause: %q", err.Error())
	}
}

// -------- overwrite paths --------

// overwrite + same content → uploadOne short-circuits via Stat, never
// sends a PUT (the zero-write idempotency path).
func TestUploadOne_Overwrite_SameShaSkipsPut(t *testing.T) {
	store := &fakeStore{
		statFn: func(_ context.Context, _ string) (objstore.ObjectInfo, bool, error) {
			return objstore.ObjectInfo{Metadata: map[string]string{"sha256": testSha}}, true, nil
		},
	}
	out, err := uploadOne(context.Background(), store, dummyKey, staged(dummyBody, testSha),
		dummyContentType, true, dummyLabel)
	if err != nil {
		t.Fatalf("uploadOne: %v", err)
	}
	if out.kind != outcomeUnchanged {
		t.Errorf("kind = %v, want outcomeUnchanged", out.kind)
	}
	if len(store.putCalls) != 0 {
		t.Errorf("expected 0 PUTs on same-sha overwrite, got %d", len(store.putCalls))
	}
}

// overwrite + different content → unconditional PUT (no IfNoneMatch,
// no IfMatch). Versioning on the backend protects the prior version.
func TestUploadOne_Overwrite_DifferentShaWritesUnconditionally(t *testing.T) {
	store := &fakeStore{
		statFn: func(_ context.Context, _ string) (objstore.ObjectInfo, bool, error) {
			return objstore.ObjectInfo{Metadata: map[string]string{"sha256": otherSha}}, true, nil
		},
	}
	out, err := uploadOne(context.Background(), store, dummyKey, staged(dummyBody, testSha),
		dummyContentType, true, dummyLabel)
	if err != nil {
		t.Fatalf("uploadOne: %v", err)
	}
	if out.kind != outcomeWritten {
		t.Errorf("kind = %v, want outcomeWritten", out.kind)
	}
	if out.existingSha != otherSha {
		t.Errorf("existingSha = %q, want %q", out.existingSha, otherSha)
	}
	if len(store.putCalls) != 1 {
		t.Fatalf("got %d PUTs, want 1", len(store.putCalls))
	}
	if store.putCalls[0].opts.IfNoneMatch != "" {
		t.Errorf("overwrite path must not send IfNoneMatch; got %q", store.putCalls[0].opts.IfNoneMatch)
	}
	if store.putCalls[0].opts.IfMatch != "" {
		t.Errorf("overwrite path must not send IfMatch; got %q", store.putCalls[0].opts.IfMatch)
	}
}

// overwrite + absent → unconditional PUT (overwrite of nothing is
// still a write).
func TestUploadOne_Overwrite_AbsentWrites(t *testing.T) {
	store := &fakeStore{} // default Stat absent, default Put succeeds
	out, err := uploadOne(context.Background(), store, dummyKey, staged(dummyBody, testSha),
		dummyContentType, true, dummyLabel)
	if err != nil {
		t.Fatalf("uploadOne: %v", err)
	}
	if out.kind != outcomeWritten {
		t.Errorf("kind = %v, want outcomeWritten", out.kind)
	}
	if len(store.putCalls) != 1 {
		t.Errorf("got %d PUTs, want 1", len(store.putCalls))
	}
}

// -------- normaliseSha256Hex (unchanged) --------

func TestNormaliseSha256Hex(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"  ", ""},
		{"deadbeef", ""}, // too short
		{strings.Repeat("a", 64), strings.Repeat("a", 64)},
		{"  " + strings.Repeat("A", 64) + "  ", strings.Repeat("a", 64)},                 // trimmed + lowercased
		{strings.Repeat("a", 63) + "Z", ""},                                              // bad char
		{strings.Repeat("a", 65), ""},                                                    // too long
		{"DEADBEEF" + strings.Repeat("0", 56), "deadbeef" + strings.Repeat("0", 56)},     // upper hex OK
		{testSha, testSha},
	}
	for _, tc := range cases {
		got := normaliseSha256Hex(tc.in)
		if got != tc.want {
			t.Errorf("normaliseSha256Hex(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

