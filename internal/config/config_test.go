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

// ---------------------------------------------------------------------------
// Default-application tests (storage path refactor).
//
// All four storage dirs (WikiDir / RawDir / DataDir / PBDataDir) get
// inferred defaults when both alias env names are unset. These tests
// pin down the exact defaults so future refactors can't silently move
// data into the git checkout again.
// ---------------------------------------------------------------------------

// clearStorageEnv unsets every env var that influences a storage path,
// so the test sees the same starting state regardless of how the
// developer's own shell is configured.
func clearStorageEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"QATLAS_WIKI_DIR", "WIKI_DIR",
		"QATLAS_RAW_DIR", "RAW_DIR",
		"QATLAS_DATA_DIR", "DATA_DIR",
		"QATLAS_PB_DATA_DIR", "PB_DATA_DIR",
	} {
		t.Setenv(k, "")
	}
}

func TestLoad_DefaultWikiDirIsSiblingOfAnchor(t *testing.T) {
	clearStorageEnv(t)
	tmp := t.TempDir()
	checkout := filepath.Join(tmp, "QuantumAtlas")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatalf("mkdir checkout: %v", err)
	}
	dotenvPath := filepath.Join(checkout, ".env")
	if err := os.WriteFile(dotenvPath, []byte("# empty\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	cfg, err := Load(dotenvPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(tmp, "QuantumAtlas-Wiki")
	if cfg.WikiDir != want {
		t.Errorf("cfg.WikiDir default = %q, want %q", cfg.WikiDir, want)
	}
}

func TestLoad_DefaultsResolveXDGDataHome(t *testing.T) {
	clearStorageEnv(t)
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	// HOME is irrelevant when XDG_DATA_HOME is set and absolute, but
	// pin it anyway so the test doesn't depend on the developer's $HOME.
	t.Setenv("HOME", t.TempDir())

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cases := []struct {
		name, got, want string
	}{
		{"RawDir", cfg.RawDir, filepath.Join(xdg, "quantum-atlas", "raw")},
		{"DataDir", cfg.DataDir, filepath.Join(xdg, "quantum-atlas", "data")},
		{"PBDataDir", cfg.PBDataDir, filepath.Join(xdg, "quantum-atlas", "pb_data")},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s default = %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestLoad_DefaultsFallBackToHomeWhenXDGUnset(t *testing.T) {
	clearStorageEnv(t)
	t.Setenv("XDG_DATA_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	base := filepath.Join(home, ".local", "share", "quantum-atlas")
	cases := []struct {
		name, got, want string
	}{
		{"RawDir", cfg.RawDir, filepath.Join(base, "raw")},
		{"DataDir", cfg.DataDir, filepath.Join(base, "data")},
		{"PBDataDir", cfg.PBDataDir, filepath.Join(base, "pb_data")},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s default = %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestLoad_DefaultsRejectRelativeXDGDataHome(t *testing.T) {
	// Per XDG spec, $XDG_DATA_HOME MUST be an absolute path; relative
	// values are invalid. Make sure we fall back to $HOME/.local/share
	// rather than silently leak a relative path into config.
	clearStorageEnv(t)
	t.Setenv("XDG_DATA_HOME", "not-absolute")
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(home, ".local", "share", "quantum-atlas", "raw")
	if cfg.RawDir != want {
		t.Errorf("RawDir = %q, want %q (relative XDG_DATA_HOME must be rejected)",
			cfg.RawDir, want)
	}
}

func TestLoad_ExplicitEnvOverridesDefaults(t *testing.T) {
	clearStorageEnv(t)
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("HOME", t.TempDir())

	override := t.TempDir()
	t.Setenv("QATLAS_RAW_DIR", filepath.Join(override, "raw-explicit"))
	t.Setenv("QATLAS_DATA_DIR", filepath.Join(override, "data-explicit"))
	t.Setenv("QATLAS_PB_DATA_DIR", filepath.Join(override, "pb-explicit"))
	t.Setenv("QATLAS_WIKI_DIR", filepath.Join(override, "wiki-explicit"))

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cases := []struct {
		name, got, want string
	}{
		{"WikiDir", cfg.WikiDir, filepath.Join(override, "wiki-explicit")},
		{"RawDir", cfg.RawDir, filepath.Join(override, "raw-explicit")},
		{"DataDir", cfg.DataDir, filepath.Join(override, "data-explicit")},
		{"PBDataDir", cfg.PBDataDir, filepath.Join(override, "pb-explicit")},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q (explicit env must beat default)",
				c.name, c.got, c.want)
		}
	}
}

func TestLoad_LegacyAliasesStillRecognized(t *testing.T) {
	// `WIKI_DIR` etc. (no QATLAS_ prefix) are documented .env aliases
	// from the FastAPI era. Make sure they still beat the auto-default
	// so users who haven't updated their .env keep working.
	clearStorageEnv(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	legacy := t.TempDir()
	t.Setenv("RAW_DIR", filepath.Join(legacy, "raw"))
	t.Setenv("DATA_DIR", filepath.Join(legacy, "data"))
	t.Setenv("PB_DATA_DIR", filepath.Join(legacy, "pb_data"))
	t.Setenv("WIKI_DIR", filepath.Join(legacy, "wiki"))

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cases := []struct {
		name, got, want string
	}{
		{"WikiDir", cfg.WikiDir, filepath.Join(legacy, "wiki")},
		{"RawDir", cfg.RawDir, filepath.Join(legacy, "raw")},
		{"DataDir", cfg.DataDir, filepath.Join(legacy, "data")},
		{"PBDataDir", cfg.PBDataDir, filepath.Join(legacy, "pb_data")},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s legacy alias = %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestDefaultXDGSubdir_Unit(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/srv/xdg")
	if got := defaultXDGSubdir("raw"); got != "/srv/xdg/quantum-atlas/raw" {
		t.Errorf("absolute XDG_DATA_HOME: got %q", got)
	}

	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/test")
	if got := defaultXDGSubdir("data"); got != "/home/test/.local/share/quantum-atlas/data" {
		t.Errorf("HOME fallback: got %q", got)
	}

	t.Setenv("XDG_DATA_HOME", "relative-path-rejected")
	t.Setenv("HOME", "/home/test")
	if got := defaultXDGSubdir("pb_data"); got != "/home/test/.local/share/quantum-atlas/pb_data" {
		t.Errorf("relative XDG should be rejected: got %q", got)
	}
}
