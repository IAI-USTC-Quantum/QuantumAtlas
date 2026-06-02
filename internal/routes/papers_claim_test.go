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

// fakeClaimStore is a minimal Store impl for claimPDFURL tests. Each
// hook is set per test; unhooked methods panic so an accidental call
// path that wasn't supposed to happen surfaces loudly.
type fakeClaimStore struct {
	statFn    func(ctx context.Context, key string) (objstore.ObjectInfo, bool, error)
	presignFn func(ctx context.Context, key string, ttl time.Duration) (string, bool, error)
}

func (f *fakeClaimStore) Put(_ context.Context, _ string, _ io.Reader, _ int64, _ string) (int64, error) {
	panic("unused")
}
func (f *fakeClaimStore) PutWithMeta(_ context.Context, _ string, _ io.Reader, _ int64, _ string, _ map[string]string) (int64, error) {
	panic("unused")
}
func (f *fakeClaimStore) PutWithOptions(_ context.Context, _ string, _ io.Reader, _ int64, _ objstore.PutOptions) (int64, error) {
	panic("unused")
}
func (f *fakeClaimStore) Get(_ context.Context, _ string) (io.ReadCloser, objstore.ObjectInfo, error) {
	panic("unused")
}
func (f *fakeClaimStore) Stat(ctx context.Context, key string) (objstore.ObjectInfo, bool, error) {
	if f.statFn == nil {
		return objstore.ObjectInfo{}, true, nil
	}
	return f.statFn(ctx, key)
}
func (f *fakeClaimStore) Delete(_ context.Context, _ string) error { panic("unused") }
func (f *fakeClaimStore) ListPrefix(_ context.Context, _ string, _ int) ([]objstore.ObjectInfo, error) {
	// ResolveAssetsViaStore calls this when probing for candidate
	// stems. Empty list = "no fallback candidates" — exactly the
	// behaviour we want in claimPDFURL tests where we don't seed any
	// alternate keys.
	return nil, nil
}
func (f *fakeClaimStore) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, bool, error) {
	if f.presignFn == nil {
		return "", false, nil
	}
	return f.presignFn(ctx, key, ttl)
}

func TestClaimPDFURL_PrefersPresignWhenSupported(t *testing.T) {
	t.Parallel()
	store := &fakeClaimStore{
		presignFn: func(_ context.Context, key string, ttl time.Duration) (string, bool, error) {
			if ttl != claimPDFTTL {
				t.Errorf("ttl = %v, want %v", ttl, claimPDFTTL)
			}
			if !strings.Contains(key, "2401.0001v1") {
				t.Errorf("unexpected key %q", key)
			}
			return "https://raw.example.com/qatlas-pdf/2401/2401.0001v1.pdf?X-Amz-Signature=ABC", true, nil
		},
	}
	got := claimPDFURL(context.Background(), store, "2401.0001v1")
	if !strings.HasPrefix(got, "https://raw.example.com/") {
		t.Fatalf("expected presigned URL, got %q", got)
	}
}

func TestClaimPDFURL_FallsBackToArxivWhenPresignNotSupported(t *testing.T) {
	t.Parallel()
	store := &fakeClaimStore{
		presignFn: func(_ context.Context, _ string, _ time.Duration) (string, bool, error) {
			// LocalStore-like backend: signals "not supported, use streaming".
			return "", false, nil
		},
	}
	got := claimPDFURL(context.Background(), store, "2401.0001v1")
	if got != "https://arxiv.org/pdf/2401.0001v1" {
		t.Fatalf("expected arxiv fallback, got %q", got)
	}
}

func TestClaimPDFURL_FallsBackOnPresignError(t *testing.T) {
	t.Parallel()
	store := &fakeClaimStore{
		presignFn: func(_ context.Context, _ string, _ time.Duration) (string, bool, error) {
			return "", true, errors.New("S3 returned 503")
		},
	}
	got := claimPDFURL(context.Background(), store, "2401.0001v1")
	if got != "https://arxiv.org/pdf/2401.0001v1" {
		t.Fatalf("expected arxiv fallback on error, got %q", got)
	}
}

func TestClaimPDFURL_OldStyleBare(t *testing.T) {
	t.Parallel()
	// Critical case: catalog stores bare "0207065v3" (subject prefix
	// dropped at ingest). The arxiv fallback URL for bare old-style is
	// broken (arxiv would 404), so this test asserts we WILL surface
	// the presigned URL when supported — that's the only way pre-2007
	// papers can be MinerU-claimed at all.
	var capturedKey string
	store := &fakeClaimStore{
		presignFn: func(_ context.Context, key string, _ time.Duration) (string, bool, error) {
			capturedKey = key
			return "https://raw.example.com/qatlas-pdf/0207/0207065v3.pdf?sig=XYZ", true, nil
		},
	}
	got := claimPDFURL(context.Background(), store, "0207065v3")
	if !strings.HasPrefix(got, "https://raw.example.com/") {
		t.Fatalf("expected presigned URL for bare old-style ID, got %q", got)
	}
	if !strings.Contains(capturedKey, "0207065v3") {
		t.Fatalf("presign called with key %q, expected to contain bare ID", capturedKey)
	}
}

func TestClaimPDFURL_FallbackResolverWhenStatMisses(t *testing.T) {
	t.Parallel()
	// If the canonical AssetKey doesn't exist (Stat returns exists=false),
	// claimPDFURL consults ResolveAssetsViaStore for a candidate stem
	// and presigns that. Hard to fully exercise without seeded objects,
	// but at minimum we verify presign still gets called and the URL is
	// returned (not the arxiv fallback) when presign succeeds.
	statCalls := 0
	store := &fakeClaimStore{
		statFn: func(_ context.Context, _ string) (objstore.ObjectInfo, bool, error) {
			statCalls++
			return objstore.ObjectInfo{}, false, nil
		},
		presignFn: func(_ context.Context, _ string, _ time.Duration) (string, bool, error) {
			return "https://raw.example.com/qatlas-pdf/2401/2401.0001v1.pdf?sig=ABC", true, nil
		},
	}
	got := claimPDFURL(context.Background(), store, "2401.0001v1")
	if got == "https://arxiv.org/pdf/2401.0001v1" {
		t.Fatalf("expected presigned URL even when initial Stat misses, got arxiv fallback")
	}
	if statCalls < 1 {
		t.Fatalf("Stat never called; claimPDFURL should sanity-check existence first")
	}
}
