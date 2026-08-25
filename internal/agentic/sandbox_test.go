package agentic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"
)

var sandboxNameRe = regexp.MustCompile(`^\d{8}T\d{6}-[0-9a-f]{8}$`)

func TestSandboxLifecycle(t *testing.T) {
	root := t.TempDir()
	sb, err := NewSandbox(root)
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	if !sandboxNameRe.MatchString(filepath.Base(sb.Dir)) {
		t.Errorf("sandbox dir %q does not match <ts>-<8hex>", filepath.Base(sb.Dir))
	}

	if err := sb.WriteJSON("query.json", search.SearchEntry{Text: "q", MaxResults: 5}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(sb.Dir, "query.json"))
	if err != nil {
		t.Fatalf("read query.json: %v", err)
	}
	var decoded search.SearchEntry
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Text != "q" {
		t.Errorf("query.json round-trip: %v %q", err, raw)
	}
	// Atomic write: no .part leftover.
	if matches, _ := filepath.Glob(filepath.Join(sb.Dir, "*.part")); len(matches) > 0 {
		t.Errorf("stray .part files: %v", matches)
	}

	runDir, err := sb.RunDir()
	if err != nil {
		t.Fatalf("RunDir: %v", err)
	}
	if filepath.Base(runDir) != "run" {
		t.Errorf("RunDir = %q", runDir)
	}

	if err := sb.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(sb.Dir); !os.IsNotExist(err) {
		t.Errorf("sandbox dir still present after Cleanup")
	}
}

func TestSweepOnce(t *testing.T) {
	root := t.TempDir()
	mk := func(name string, age time.Duration) string {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-age)
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	expired := mk("20200101T000000-deadbeef", 48*time.Hour)
	fresh := mk("29990101T000000-cafef00d", time.Hour)
	// A stray file at the root must never be touched.
	stray := filepath.Join(root, "stray.txt")
	if err := os.WriteFile(stray, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if n := SweepOnce(root, 24*time.Hour); n != 1 {
		t.Errorf("SweepOnce removed %d, want 1", n)
	}
	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Error("expired sandbox still present")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh sandbox removed, want kept")
	}
	if _, err := os.Stat(stray); err != nil {
		t.Error("stray file removed, want kept")
	}

	// Non-positive retention disables sweeping entirely.
	if n := SweepOnce(root, 0); n != 0 {
		t.Errorf("SweepOnce(retention=0) removed %d, want 0", n)
	}
	// Missing root is not an error.
	if n := SweepOnce(filepath.Join(root, "nope"), 24*time.Hour); n != 0 {
		t.Errorf("SweepOnce(missing root) removed %d, want 0", n)
	}
}
