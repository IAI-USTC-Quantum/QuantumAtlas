package routes

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
)

// Why these tests live here (not in objstore):
//
// decideUpload encodes the *handler-side* policy — "what does qatlas do
// when the new sha doesn't match the existing object's metadata?" — not
// any S3 wire behavior. Keeping it co-located with uploadPDFHandler and
// uploadMarkdownHandler means a change to either keeps the test next
// door.

// fakeStore is a minimal in-memory objstore.Store stub. We don't need
// any of the I/O methods — decideUpload only calls Stat — so most
// methods panic if accidentally invoked.
type fakeStore struct {
	stat func(ctx context.Context, key string) (objstore.ObjectInfo, bool, error)
}

func (f *fakeStore) Put(_ context.Context, _ string, _ io.Reader, _ int64, _ string) (int64, error) {
	panic("fakeStore.Put should not be called by decideUpload tests")
}
func (f *fakeStore) PutWithMeta(_ context.Context, _ string, _ io.Reader, _ int64, _ string, _ map[string]string) (int64, error) {
	panic("fakeStore.PutWithMeta should not be called by decideUpload tests")
}
func (f *fakeStore) Get(_ context.Context, _ string) (io.ReadCloser, objstore.ObjectInfo, error) {
	panic("fakeStore.Get should not be called by decideUpload tests")
}
func (f *fakeStore) Stat(ctx context.Context, key string) (objstore.ObjectInfo, bool, error) {
	return f.stat(ctx, key)
}
func (f *fakeStore) Delete(_ context.Context, _ string) error {
	panic("fakeStore.Delete should not be called by decideUpload tests")
}
func (f *fakeStore) ListPrefix(_ context.Context, _ string, _ int) ([]objstore.ObjectInfo, error) {
	panic("fakeStore.ListPrefix should not be called by decideUpload tests")
}
func (f *fakeStore) PresignGet(_ context.Context, _ string, _ time.Duration) (string, bool, error) {
	panic("fakeStore.PresignGet should not be called by decideUpload tests")
}

const testSha = "deadbeef00112233445566778899aabbccddeeff00112233445566778899aabb"

func TestDecideUpload_AbsentObject(t *testing.T) {
	store := &fakeStore{
		stat: func(_ context.Context, _ string) (objstore.ObjectInfo, bool, error) {
			return objstore.ObjectInfo{}, false, nil
		},
	}
	dec, err := decideUpload(context.Background(), store, "pdf/24/x.pdf", testSha, false, "PDF")
	if err != nil {
		t.Fatalf("decideUpload: %v", err)
	}
	if dec.unchanged {
		t.Errorf("absent object should not be 'unchanged'")
	}
}

// Same content uploaded twice — handler must short-circuit, never PUT
// again. This is the core sha256 dedup property.
func TestDecideUpload_SameShaIsIdempotent(t *testing.T) {
	store := &fakeStore{
		stat: func(_ context.Context, _ string) (objstore.ObjectInfo, bool, error) {
			return objstore.ObjectInfo{
				Metadata: map[string]string{"sha256": testSha},
			}, true, nil
		},
	}
	dec, err := decideUpload(context.Background(), store, "pdf/24/x.pdf", testSha, false, "PDF")
	if err != nil {
		t.Fatalf("decideUpload: %v", err)
	}
	if !dec.unchanged {
		t.Errorf("matching sha should produce unchanged=true; got %+v", dec)
	}
	if dec.existingSha256 != testSha {
		t.Errorf("existingSha256 = %q, want %q", dec.existingSha256, testSha)
	}
}

func TestDecideUpload_DifferentShaWithoutOverwriteIsConflict(t *testing.T) {
	store := &fakeStore{
		stat: func(_ context.Context, _ string) (objstore.ObjectInfo, bool, error) {
			return objstore.ObjectInfo{
				Metadata: map[string]string{"sha256": "00000000" + testSha[8:]},
			}, true, nil
		},
	}
	_, err := decideUpload(context.Background(), store, "pdf/24/x.pdf", testSha, false, "PDF")
	if err == nil {
		t.Fatalf("expected conflict error, got nil")
	}
	var uce *uploadConflictError
	if !errors.As(err, &uce) {
		t.Fatalf("expected *uploadConflictError, got %T: %v", err, err)
	}
	if uce.Status != 409 {
		t.Errorf("Status = %d, want 409", uce.Status)
	}
	if uce.Body["new_sha256"] != testSha {
		t.Errorf("Body[new_sha256] = %v, want %q", uce.Body["new_sha256"], testSha)
	}
	if existing, ok := uce.Body["existing_sha256"].(string); !ok || existing == testSha {
		t.Errorf("Body[existing_sha256] = %v, want a different sha string", uce.Body["existing_sha256"])
	}
}

func TestDecideUpload_DifferentShaWithOverwriteAllowsWrite(t *testing.T) {
	store := &fakeStore{
		stat: func(_ context.Context, _ string) (objstore.ObjectInfo, bool, error) {
			return objstore.ObjectInfo{
				Metadata: map[string]string{"sha256": "00000000" + testSha[8:]},
			}, true, nil
		},
	}
	dec, err := decideUpload(context.Background(), store, "pdf/24/x.pdf", testSha, true /* overwrite */, "PDF")
	if err != nil {
		t.Fatalf("decideUpload: %v", err)
	}
	if dec.unchanged {
		t.Errorf("overwrite path should not short-circuit, got unchanged=true")
	}
}

// Legacy object with no sha256 metadata (pre-dedup upload or LocalStore
// backend). Without overwrite we MUST 409 — we can't safely assume the
// bytes are the same, and silently overwriting would lose the legacy
// content.
func TestDecideUpload_NoExistingMetaWithoutOverwriteIsConflict(t *testing.T) {
	store := &fakeStore{
		stat: func(_ context.Context, _ string) (objstore.ObjectInfo, bool, error) {
			return objstore.ObjectInfo{Metadata: nil}, true, nil
		},
	}
	_, err := decideUpload(context.Background(), store, "pdf/24/x.pdf", testSha, false, "PDF")
	var uce *uploadConflictError
	if !errors.As(err, &uce) {
		t.Fatalf("expected *uploadConflictError, got %T: %v", err, err)
	}
	if uce.Body["existing_sha256"] != nil {
		t.Errorf("Body[existing_sha256] should be nil when legacy object had no metadata, got %v", uce.Body["existing_sha256"])
	}
	if _, ok := uce.Body["note"].(string); !ok {
		t.Errorf("Body[note] missing — should explain why content equality couldn't be verified")
	}
}

func TestDecideUpload_StatErrorBubblesUpAs500(t *testing.T) {
	store := &fakeStore{
		stat: func(_ context.Context, _ string) (objstore.ObjectInfo, bool, error) {
			return objstore.ObjectInfo{}, false, errors.New("rustfs down")
		},
	}
	_, err := decideUpload(context.Background(), store, "pdf/24/x.pdf", testSha, false, "PDF")
	if err == nil {
		t.Fatalf("expected error")
	}
	var ue *uploadError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *uploadError, got %T: %v", err, err)
	}
	if ue.Status != 500 {
		t.Errorf("Status = %d, want 500", ue.Status)
	}
	if !strings.Contains(ue.Detail, "rustfs down") {
		t.Errorf("Detail missing root cause: %q", ue.Detail)
	}
}

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
