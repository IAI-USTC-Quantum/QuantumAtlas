package objstore

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func newLocal(t *testing.T) *LocalStore {
	t.Helper()
	dir := t.TempDir()
	s, err := NewLocalStore(dir)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	return s
}

func TestLocalStore_NewRejectsRelative(t *testing.T) {
	if _, err := NewLocalStore("relative"); err == nil {
		t.Errorf("NewLocalStore(relative) should fail")
	}
	if _, err := NewLocalStore(""); err == nil {
		t.Errorf("NewLocalStore('') should fail")
	}
}

func TestLocalStore_PutGetRoundTrip(t *testing.T) {
	s := newLocal(t)
	ctx := context.Background()
	body := []byte("hello rustfs")
	n, err := s.Put(ctx, "pdf/24/2401.00001v1.pdf", bytes.NewReader(body), int64(len(body)), "application/pdf")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if n != int64(len(body)) {
		t.Errorf("Put wrote %d, want %d", n, len(body))
	}

	rc, info, err := s.Get(ctx, "pdf/24/2401.00001v1.pdf")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, body) {
		t.Errorf("Get round-trip mismatch: %q vs %q", got, body)
	}
	if info.Size != int64(len(body)) {
		t.Errorf("Get info.Size = %d, want %d", info.Size, len(body))
	}
}

func TestLocalStore_GetMissingReturnsNotFound(t *testing.T) {
	s := newLocal(t)
	_, _, err := s.Get(context.Background(), "absent/key")
	if !IsNotFound(err) {
		t.Errorf("Get missing key: err = %v, want ErrNotFound", err)
	}
}

func TestLocalStore_StatExistsAndMissing(t *testing.T) {
	s := newLocal(t)
	ctx := context.Background()
	body := []byte("x")
	if _, err := s.Put(ctx, "a/b/c.txt", bytes.NewReader(body), 1, ""); err != nil {
		t.Fatalf("Put: %v", err)
	}

	info, exists, err := s.Stat(ctx, "a/b/c.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !exists {
		t.Errorf("Stat existing key: exists=false")
	}
	if info.Size != 1 {
		t.Errorf("Stat size = %d, want 1", info.Size)
	}

	_, exists, err = s.Stat(ctx, "no/such/key")
	if err != nil {
		t.Fatalf("Stat missing: err = %v", err)
	}
	if exists {
		t.Errorf("Stat missing key: exists=true")
	}
}

func TestLocalStore_StatRejectsDirectory(t *testing.T) {
	// Stat is for objects only. A bare directory existing under the
	// resolved key path must report exists=false so callers don't
	// confuse "has children" with "object lives here".
	s := newLocal(t)
	if err := os.MkdirAll(filepath.Join(s.BaseDir, "dir-not-file"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, exists, err := s.Stat(context.Background(), "dir-not-file")
	if err != nil {
		t.Fatalf("Stat dir: err = %v", err)
	}
	if exists {
		t.Errorf("Stat on a directory returned exists=true; want false")
	}
}

func TestLocalStore_Delete(t *testing.T) {
	s := newLocal(t)
	ctx := context.Background()
	if _, err := s.Put(ctx, "del/me", bytes.NewReader([]byte("z")), 1, ""); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete(ctx, "del/me"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	if _, exists, _ := s.Stat(ctx, "del/me"); exists {
		t.Errorf("after Delete, key still exists")
	}
	// Idempotent: deleting again is fine.
	if err := s.Delete(ctx, "del/me"); err != nil {
		t.Errorf("Delete idempotent should not error, got %v", err)
	}
	// And deleting an unknown key is also fine.
	if err := s.Delete(ctx, "never-existed"); err != nil {
		t.Errorf("Delete unknown should not error, got %v", err)
	}
}

func TestLocalStore_ListPrefix(t *testing.T) {
	s := newLocal(t)
	ctx := context.Background()
	files := map[string][]byte{
		"pdf/24/2401.00001v1.pdf":  []byte("a"),
		"pdf/24/2401.00002v1.pdf":  []byte("bb"),
		"pdf/25/2501.00010v1.pdf":  []byte("ccc"),
		"markdown/24/2401.00001v1.md": []byte("dddd"),
	}
	for k, v := range files {
		if _, err := s.Put(ctx, k, bytes.NewReader(v), int64(len(v)), ""); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}

	t.Run("subdirectory prefix", func(t *testing.T) {
		got, err := s.ListPrefix(ctx, "pdf/24/", 0)
		if err != nil {
			t.Fatalf("ListPrefix: %v", err)
		}
		keys := mapKeys(got)
		sort.Strings(keys)
		want := []string{"pdf/24/2401.00001v1.pdf", "pdf/24/2401.00002v1.pdf"}
		if !equalStrings(keys, want) {
			t.Errorf("ListPrefix pdf/24/: got %v, want %v", keys, want)
		}
	})

	t.Run("partial stem prefix", func(t *testing.T) {
		// "pdf/24/2401.00001" should match exactly one file by stem.
		got, err := s.ListPrefix(ctx, "pdf/24/2401.00001", 0)
		if err != nil {
			t.Fatalf("ListPrefix: %v", err)
		}
		keys := mapKeys(got)
		want := []string{"pdf/24/2401.00001v1.pdf"}
		if !equalStrings(keys, want) {
			t.Errorf("ListPrefix partial stem: got %v, want %v", keys, want)
		}
	})

	t.Run("limit caps results", func(t *testing.T) {
		got, err := s.ListPrefix(ctx, "", 2)
		if err != nil {
			t.Fatalf("ListPrefix: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("ListPrefix limit=2 returned %d objects", len(got))
		}
	})

	t.Run("missing prefix returns empty", func(t *testing.T) {
		got, err := s.ListPrefix(ctx, "no/such/prefix", 0)
		if err != nil {
			t.Fatalf("ListPrefix: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("ListPrefix unknown: got %d, want 0", len(got))
		}
	})
}

func TestLocalStore_PresignGetUnsupported(t *testing.T) {
	s := newLocal(t)
	url, supported, err := s.PresignGet(context.Background(), "k", 0)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	if supported {
		t.Errorf("PresignGet should report supported=false")
	}
	if url != "" {
		t.Errorf("PresignGet should return empty url, got %q", url)
	}
}

func TestLocalStore_RejectsTraversalKey(t *testing.T) {
	s := newLocal(t)
	ctx := context.Background()
	bad := []string{"/abs", "../escape", "a/../b", "back\\slash"}
	for _, k := range bad {
		if _, err := s.Put(ctx, k, bytes.NewReader([]byte("x")), 1, ""); err == nil {
			t.Errorf("Put(%q) should fail", k)
		}
		if _, _, err := s.Stat(ctx, k); err == nil {
			t.Errorf("Stat(%q) should fail", k)
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func mapKeys(infos []ObjectInfo) []string {
	out := make([]string, len(infos))
	for i, o := range infos {
		out[i] = o.Key
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Compile-time guard: LocalStore implements Store.
var _ Store = (*LocalStore)(nil)

// strings used in tests but the lint may complain otherwise.
var _ = strings.Contains
