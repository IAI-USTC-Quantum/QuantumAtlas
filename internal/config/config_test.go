package config

import (
	"os"
	"path/filepath"
	"strings"
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

// ---------------------------------------------------------------------------
// S3 / object storage invariant tests (Phase 3).
//
// The four QATLAS_S3_* fields must be set as a group: either all four
// non-empty (S3 backend enabled), or all four empty (local RawDir
// fallback). A partial config is a boot-time error rather than a silent
// behaviour change.
// ---------------------------------------------------------------------------

// clearS3Env unsets every env var that influences S3 wiring so the test
// sees a clean baseline regardless of the developer's shell.
func clearS3Env(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"QATLAS_S3_ENDPOINT",
		"QATLAS_S3_BUCKET",
		"QATLAS_S3_BUCKET_PDF",
		"QATLAS_S3_BUCKET_MD",
		"QATLAS_S3_BUCKET_IMAGES",
		"QATLAS_S3_BUCKET_OPENALEX_SNAPSHOT",
		"QATLAS_S3_ACCESS_KEY_ID",
		"QATLAS_S3_SECRET_ACCESS_KEY",
	} {
		t.Setenv(k, "")
	}
}

func TestLoad_S3Disabled_AllFieldsEmpty(t *testing.T) {
	clearStorageEnv(t)
	clearS3Env(t)
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.S3Enabled() {
		t.Errorf("S3Enabled() = true with all env empty; want false")
	}
}

func TestLoad_S3Enabled_AllFieldsSet(t *testing.T) {
	clearStorageEnv(t)
	clearS3Env(t)
	t.Setenv("QATLAS_S3_ENDPOINT", "https://raw.example.tld")
	t.Setenv("QATLAS_S3_BUCKET_PDF", "qatlas-pdf")
	t.Setenv("QATLAS_S3_BUCKET_MD", "qatlas-md")
	t.Setenv("QATLAS_S3_BUCKET_IMAGES", "qatlas-images")
	t.Setenv("QATLAS_S3_ACCESS_KEY_ID", "AKID")
	t.Setenv("QATLAS_S3_SECRET_ACCESS_KEY", "SK")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.S3Enabled() {
		t.Errorf("S3Enabled() = false with all env set; want true")
	}
	if cfg.S3Endpoint != "https://raw.example.tld" {
		t.Errorf("S3Endpoint = %q", cfg.S3Endpoint)
	}
	if cfg.S3BucketPDF != "qatlas-pdf" {
		t.Errorf("S3BucketPDF = %q", cfg.S3BucketPDF)
	}
	if cfg.S3BucketMD != "qatlas-md" {
		t.Errorf("S3BucketMD = %q", cfg.S3BucketMD)
	}
	if cfg.S3BucketImages != "qatlas-images" {
		t.Errorf("S3BucketImages = %q", cfg.S3BucketImages)
	}
	if cfg.S3AccessKeyID != "AKID" {
		t.Errorf("S3AccessKeyID = %q", cfg.S3AccessKeyID)
	}
	if cfg.S3SecretAccessKey != "SK" {
		t.Errorf("S3SecretAccessKey = %q", cfg.S3SecretAccessKey)
	}
}

// TestLoad_S3LegacyBucketRejected verifies the v0.6.0 single-bucket var
// is a hard boot error in v0.7.0 (a stale .env would otherwise silently
// mis-route every object into one bucket).
func TestLoad_S3LegacyBucketRejected(t *testing.T) {
	clearStorageEnv(t)
	clearS3Env(t)
	t.Setenv("QATLAS_S3_BUCKET", "qatlas-raw")
	_, err := Load("")
	if err == nil {
		t.Fatalf("Load returned nil; expected legacy QATLAS_S3_BUCKET rejection")
	}
	if !strings.Contains(err.Error(), "QATLAS_S3_BUCKET") {
		t.Errorf("error %q does not mention QATLAS_S3_BUCKET", err.Error())
	}
}

func TestLoad_S3PartialConfigRejected(t *testing.T) {
	// Each of these subtests sets a *strict subset* of the required
	// fields; Load() must refuse to boot. The check is symmetric — no
	// single field (endpoint / a bucket / a credential) alone is valid.
	cases := []struct {
		name string
		set  map[string]string
	}{
		{"only endpoint", map[string]string{"QATLAS_S3_ENDPOINT": "https://x"}},
		{"only pdf bucket", map[string]string{"QATLAS_S3_BUCKET_PDF": "b"}},
		{"only access key", map[string]string{"QATLAS_S3_ACCESS_KEY_ID": "a"}},
		{"only secret", map[string]string{"QATLAS_S3_SECRET_ACCESS_KEY": "s"}},
		{"endpoint + buckets, no creds", map[string]string{
			"QATLAS_S3_ENDPOINT":      "https://x",
			"QATLAS_S3_BUCKET_PDF":    "p",
			"QATLAS_S3_BUCKET_MD":     "m",
			"QATLAS_S3_BUCKET_IMAGES": "i",
		}},
		{"two of three buckets", map[string]string{
			"QATLAS_S3_ENDPOINT":          "https://x",
			"QATLAS_S3_BUCKET_PDF":        "p",
			"QATLAS_S3_BUCKET_MD":         "m",
			"QATLAS_S3_ACCESS_KEY_ID":     "a",
			"QATLAS_S3_SECRET_ACCESS_KEY": "s",
		}},
		{"creds, no endpoint", map[string]string{
			"QATLAS_S3_ACCESS_KEY_ID":     "a",
			"QATLAS_S3_SECRET_ACCESS_KEY": "s",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearStorageEnv(t)
			clearS3Env(t)
			for k, v := range c.set {
				t.Setenv(k, v)
			}
			_, err := Load("")
			if err == nil {
				t.Fatalf("Load returned nil error; expected partial-config failure")
			}
			// Sanity: error message lists at least one missing field name
			// so an operator can fix it from the log line.
			if !strings.Contains(err.Error(), "QATLAS_S3_") {
				t.Errorf("error %q does not mention any QATLAS_S3_* field name", err.Error())
			}
		})
	}
}
