package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandPath_RelativeUsesAnchor(t *testing.T) {
	anchor := "/srv/quantum/checkout"
	got := expandPath("../QuantumAtlas-Wiki", anchor)
	want := "/srv/quantum/QuantumAtlas-Wiki"
	if got != want {
		t.Errorf("expandPath(rel, anchor) = %q, want %q", got, want)
	}
}

func TestExpandPath_RelativeFallsBackToCWD(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir tmp: %v", err)
	}
	got := expandPath("relative/sub", "")
	want := filepath.Join(tmp, "relative/sub")
	if got != want {
		t.Errorf("expandPath(rel, '') = %q, want %q", got, want)
	}
}

func TestExpandPath_AbsoluteIgnoresAnchor(t *testing.T) {
	got := expandPath("/etc/passwd", "/anywhere")
	if got != "/etc/passwd" {
		t.Errorf("expandPath(abs, anchor) = %q, want /etc/passwd", got)
	}
}

func TestExpandPath_HomeExpands(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	got := expandPath("~/foo", "/ignored-when-result-becomes-absolute")
	want := filepath.Join(home, "foo")
	if got != want {
		t.Errorf("expandPath(~/foo) = %q, want %q", got, want)
	}
}

func TestExpandPath_Empty(t *testing.T) {
	if got := expandPath("", "/anywhere"); got != "" {
		t.Errorf("expandPath('') = %q, want ''", got)
	}
}

func TestLoad_RelativeWikiDirResolvesAgainstDotenv(t *testing.T) {
	// Simulate the production layout:
	//   /home/foo/QuantumAtlas/.env       (anchor)
	//   /home/foo/QuantumAtlas-Wiki/      (target)
	tmp := t.TempDir()
	checkout := filepath.Join(tmp, "QuantumAtlas")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatalf("mkdir checkout: %v", err)
	}
	dotenvPath := filepath.Join(checkout, ".env")
	if err := os.WriteFile(dotenvPath, []byte("ignored=ignored\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Setenv("WIKI_DIR", "../QuantumAtlas-Wiki")

	cfg, err := Load(dotenvPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(tmp, "QuantumAtlas-Wiki")
	if cfg.WikiDir != want {
		t.Errorf("cfg.WikiDir = %q, want %q", cfg.WikiDir, want)
	}
}

func TestLoad_EmptyDotenvPathFallsBackToCWD(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Setenv("WIKI_DIR", "wiki-here")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(tmp, "wiki-here")
	if cfg.WikiDir != want {
		t.Errorf("cfg.WikiDir = %q, want %q", cfg.WikiDir, want)
	}
}
