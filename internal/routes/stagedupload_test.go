package routes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestStageInMemory_RoundTripsBytesAndComputesSha(t *testing.T) {
	body := []byte("hello world")
	want := sha256Hex(body)

	staged, vErr := stageInMemory(context.Background(), bytes.NewReader(body),
		1024, "test", func(b []byte) *uploadError { return nil })
	if vErr != nil {
		t.Fatalf("stageInMemory: %+v", vErr)
	}
	defer staged.Close()

	if staged.Sha256() != want {
		t.Errorf("Sha256 = %q, want %q", staged.Sha256(), want)
	}
	if staged.Size() != int64(len(body)) {
		t.Errorf("Size = %d, want %d", staged.Size(), len(body))
	}

	// Open + read twice — the same property uploadOne relies on for
	// the rare 412-then-retry path.
	for attempt := 0; attempt < 2; attempt++ {
		r, err := staged.Open()
		if err != nil {
			t.Fatalf("Open attempt %d: %v", attempt, err)
		}
		got, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatalf("ReadAll attempt %d: %v", attempt, err)
		}
		if !bytes.Equal(got, body) {
			t.Errorf("attempt %d: bytes mismatch", attempt)
		}
	}
}

func TestStageInMemory_RejectsOverCap(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 100)
	_, vErr := stageInMemory(context.Background(), bytes.NewReader(body),
		50, "test", func(b []byte) *uploadError { return nil })
	if vErr == nil {
		t.Fatal("expected over-cap rejection")
	}
	if vErr.Status != 413 {
		t.Errorf("Status = %d, want 413", vErr.Status)
	}
}

func TestStageInMemory_RejectsEmpty(t *testing.T) {
	_, vErr := stageInMemory(context.Background(), bytes.NewReader(nil),
		50, "test", func(b []byte) *uploadError { return nil })
	if vErr == nil {
		t.Fatal("expected empty rejection")
	}
	if vErr.Status != 400 {
		t.Errorf("Status = %d, want 400", vErr.Status)
	}
}

func TestStageInMemory_ValidatorErrorsBubble(t *testing.T) {
	_, vErr := stageInMemory(context.Background(), bytes.NewReader([]byte("xyz")),
		50, "test", func(b []byte) *uploadError {
			return &uploadError{Status: 422, Detail: "no good"}
		})
	if vErr == nil {
		t.Fatal("expected validator rejection")
	}
	if vErr.Status != 422 || vErr.Detail != "no good" {
		t.Errorf("unexpected error: %+v", vErr)
	}
}

func TestStageToTmpFile_RoundTripsBytesAndComputesSha(t *testing.T) {
	body := bytes.Repeat([]byte("PDF body bytes "), 1000) // ~15 KB
	prefix := []byte("%PDF-1.4 prefix\n")
	full := append(prefix, body...)
	want := sha256Hex(full)

	staged, vErr := stageToTmpFile(context.Background(), bytes.NewReader(full),
		1<<20, "pdf", 5, func(head []byte) *uploadError {
			if string(head) != "%PDF-" {
				return &uploadError{Status: 400, Detail: "bad head"}
			}
			return nil
		})
	if vErr != nil {
		t.Fatalf("stageToTmpFile: %+v", vErr)
	}
	defer staged.Close()

	tmp, ok := staged.(*tmpFileBody)
	if !ok {
		t.Fatalf("expected *tmpFileBody, got %T", staged)
	}
	tmpPath := tmp.f.Name()
	if _, err := os.Stat(tmpPath); err != nil {
		t.Fatalf("tmp file should exist: %v", err)
	}

	if staged.Sha256() != want {
		t.Errorf("Sha256 = %q, want %q", staged.Sha256(), want)
	}
	if staged.Size() != int64(len(full)) {
		t.Errorf("Size = %d, want %d", staged.Size(), len(full))
	}

	// Multiple Open()s each get an independent fd (so the retry path
	// can re-read after the first reader is closed).
	for attempt := 0; attempt < 3; attempt++ {
		r, err := staged.Open()
		if err != nil {
			t.Fatalf("Open attempt %d: %v", attempt, err)
		}
		got, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatalf("ReadAll attempt %d: %v", attempt, err)
		}
		if !bytes.Equal(got, full) {
			t.Errorf("attempt %d: bytes mismatch (got %d bytes, want %d)", attempt, len(got), len(full))
		}
	}

	// Close drops the tmp file.
	if err := staged.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("tmp file should be removed after Close, got %v", err)
	}
}

func TestStageToTmpFile_HeadValidationCleansUp(t *testing.T) {
	// Capture the tmp dir before staging so we can scan for leftovers.
	before := snapshotTmpDir(t)
	defer func() {
		after := snapshotTmpDir(t)
		// Diff: anything new with the qatlas-upload prefix should
		// have been cleaned up by stageToTmpFile.
		for name := range after {
			if !strings.HasPrefix(name, "qatlas-upload-") {
				continue
			}
			if _, existed := before[name]; existed {
				continue
			}
			t.Errorf("stageToTmpFile failure path leaked tmp file: %s", name)
		}
	}()

	_, vErr := stageToTmpFile(context.Background(), bytes.NewReader([]byte("garbage")),
		1024, "pdf", 5, func(head []byte) *uploadError {
			return &uploadError{Status: 400, Detail: "rejected"}
		})
	if vErr == nil {
		t.Fatal("expected head validator rejection")
	}
}

func TestStageToTmpFile_RejectsOverCap(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 200)
	_, vErr := stageToTmpFile(context.Background(), bytes.NewReader(body),
		100, "pdf", 0, func(head []byte) *uploadError { return nil })
	if vErr == nil {
		t.Fatal("expected over-cap rejection")
	}
	if vErr.Status != 413 {
		t.Errorf("Status = %d, want 413", vErr.Status)
	}
}

// Concurrent staging exercises the per-call random tmp suffix so two
// in-flight uploads can't clobber each other.
func TestStageToTmpFile_ConcurrentNonceUnique(t *testing.T) {
	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	paths := make(chan string, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			body := []byte("payload")
			s, vErr := stageToTmpFile(context.Background(), bytes.NewReader(body),
				1024, "pdf", 0, func([]byte) *uploadError { return nil })
			if vErr != nil {
				t.Errorf("stageToTmpFile: %+v", vErr)
				return
			}
			defer s.Close()
			paths <- s.(*tmpFileBody).f.Name()
		}()
	}
	wg.Wait()
	close(paths)
	seen := make(map[string]struct{})
	for p := range paths {
		if _, dup := seen[p]; dup {
			t.Errorf("tmp path %q reused across concurrent stagers", p)
		}
		seen[p] = struct{}{}
	}
	if len(seen) != n {
		t.Errorf("got %d unique paths, want %d", len(seen), n)
	}
}

func snapshotTmpDir(t *testing.T) map[string]struct{} {
	t.Helper()
	out := make(map[string]struct{})
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatalf("read tmp dir: %v", err)
	}
	for _, e := range entries {
		out[filepath.Base(e.Name())] = struct{}{}
	}
	return out
}
