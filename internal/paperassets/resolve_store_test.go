package paperassets

import (
	"bytes"
	"context"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
)

// resolveStoreTester builds a temp LocalStore and seeds it with a set
// of object keys → contents pairs, then returns the store. We use
// LocalStore (not a mock) because it implements the same Store
// contract as S3Store; if the resolver passes here it'll pass against
// S3 with the same input shape.
func resolveStoreTester(t *testing.T, keys ...string) objstore.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := objstore.NewLocalStore(dir)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	for _, k := range keys {
		if _, err := s.Put(context.Background(), k, bytes.NewReader([]byte("x")), 1, ""); err != nil {
			t.Fatalf("seed Put %s: %v", k, err)
		}
	}
	return s
}

func TestResolveAssetsViaStore_NewStyleHappyPath(t *testing.T) {
	s := resolveStoreTester(t,
		"pdf/2401/2401.00001v1.pdf",
		"markdown/2401/2401.00001v1.md",
		"json/2401/2401.00001v1.json",
		"images/2401/2401.00001v1/fig1.png",
	)
	got := ResolveAssetsViaStore(context.Background(), s, "2401.00001v1")
	if got.PDFPath != "pdf/2401/2401.00001v1.pdf" {
		t.Errorf("PDFPath = %q", got.PDFPath)
	}
	if got.MarkdownPath != "markdown/2401/2401.00001v1.md" {
		t.Errorf("MarkdownPath = %q", got.MarkdownPath)
	}
	if got.JSONPath != "json/2401/2401.00001v1.json" {
		t.Errorf("JSONPath = %q", got.JSONPath)
	}
	if got.ImagesDir != "images/2401/2401.00001v1" {
		t.Errorf("ImagesDir = %q", got.ImagesDir)
	}
	if got.Key != "2401.00001v1" {
		t.Errorf("Key = %q", got.Key)
	}
}

func TestResolveAssetsViaStore_NothingPresent(t *testing.T) {
	s := resolveStoreTester(t) // empty
	got := ResolveAssetsViaStore(context.Background(), s, "2401.00001v1")
	// Missing assets → empty fields.
	if got.PDFPath != "" || got.MarkdownPath != "" || got.JSONPath != "" || got.ImagesDir != "" {
		t.Errorf("expected all-empty result, got %#v", got)
	}
	// Key still resolves from canonical id.
	if got.Key != "2401.00001v1" {
		t.Errorf("Key fallback = %q, want %q", got.Key, "2401.00001v1")
	}
}

func TestResolveAssetsViaStore_VersionlessQueryMatchesSingleVersion(t *testing.T) {
	// User asks for the versionless id "2401.00001" — algorithm should
	// pick the lone v1 file on disk via the pass-2 versionless match.
	s := resolveStoreTester(t, "pdf/2401/2401.00001v3.pdf")
	got := ResolveAssetsViaStore(context.Background(), s, "2401.00001")
	if got.PDFPath != "pdf/2401/2401.00001v3.pdf" {
		t.Errorf("PDFPath = %q, want versionless fallback to v3", got.PDFPath)
	}
}

func TestResolveAssetsViaStore_VersionlessAmbiguous_NoMatch(t *testing.T) {
	// Two versions present and the query is versionless → ambiguous,
	// pass-2 must return no result rather than guessing.
	s := resolveStoreTester(t,
		"pdf/2401/2401.00001v1.pdf",
		"pdf/2401/2401.00001v2.pdf",
	)
	got := ResolveAssetsViaStore(context.Background(), s, "2401.00001")
	if got.PDFPath != "" {
		t.Errorf("ambiguous versionless lookup returned %q; want empty", got.PDFPath)
	}
}

func TestResolveAssetsViaStore_OldStyleArxivID(t *testing.T) {
	// quant-ph/9508027v1 → StorageKey strips the category prefix →
	// "9508027v1", shard "9508".
	s := resolveStoreTester(t,
		"pdf/9508/9508027v1.pdf",
		"markdown/9508/9508027v1.md",
	)
	got := ResolveAssetsViaStore(context.Background(), s, "quant-ph/9508027v1")
	if got.PDFPath != "pdf/9508/9508027v1.pdf" {
		t.Errorf("old-style PDFPath = %q", got.PDFPath)
	}
	if got.MarkdownPath != "markdown/9508/9508027v1.md" {
		t.Errorf("old-style MarkdownPath = %q", got.MarkdownPath)
	}
}
