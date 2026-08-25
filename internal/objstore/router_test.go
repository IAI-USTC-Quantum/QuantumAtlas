package objstore

import (
	"bytes"
	"context"
	"sort"
	"testing"
)

func TestRouter_ListDirs(t *testing.T) {
	ctx := context.Background()
	pdfDir := t.TempDir()
	mdDir := t.TempDir()
	pdf, err := NewLocalStore(pdfDir)
	if err != nil {
		t.Fatalf("NewLocalStore pdf: %v", err)
	}
	md, err := NewLocalStore(mdDir)
	if err != nil {
		t.Fatalf("NewLocalStore md: %v", err)
	}
	r := NewRouter(map[string]Store{"pdf": pdf, "markdown": md})

	// Objects live in the per-kind backend WITHOUT the kind segment;
	// the Router strips it on the way in and re-prepends on the way out.
	for _, k := range []string{"0001/0001001v1.pdf", "0002/0002001v1.pdf", "doi/10.1234/x.pdf"} {
		if _, err := pdf.Put(ctx, k, bytes.NewReader([]byte("x")), 1, ""); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	if _, err := md.Put(ctx, "0001/0001001v1.md", bytes.NewReader([]byte("x")), 1, ""); err != nil {
		t.Fatalf("Put md: %v", err)
	}

	got, err := r.ListDirs(ctx, "pdf/")
	if err != nil {
		t.Fatalf("ListDirs: %v", err)
	}
	sort.Strings(got)
	want := []string{"pdf/0001/", "pdf/0002/", "pdf/doi/"}
	if !equalStrings(got, want) {
		t.Errorf("ListDirs pdf/: got %v, want %v", got, want)
	}

	got, err = r.ListDirs(ctx, "markdown/")
	if err != nil {
		t.Fatalf("ListDirs markdown: %v", err)
	}
	if !equalStrings(got, []string{"markdown/0001/"}) {
		t.Errorf("ListDirs markdown/: got %v, want [markdown/0001/]", got)
	}

	// Unknown kind (json is dropped) → empty listing, no error.
	got, err = r.ListDirs(ctx, "json/")
	if err != nil {
		t.Fatalf("ListDirs json: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListDirs json/: got %v, want empty", got)
	}
}

// Compile-time guard: Router implements Store.
var _ Store = (*Router)(nil)
