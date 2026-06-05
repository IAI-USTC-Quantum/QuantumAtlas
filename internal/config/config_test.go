package config

import (
	"bytes"
	"log/slog"
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
		{"RawDir", cfg.RawDir, filepath.Join(xdg, "qatlasd", "raw")},
		{"DataDir", cfg.DataDir, filepath.Join(xdg, "qatlasd", "data")},
		{"PBDataDir", cfg.PBDataDir, filepath.Join(xdg, "qatlasd", "pb_data")},
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
	base := filepath.Join(home, ".local", "share", "qatlasd")
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
	want := filepath.Join(home, ".local", "share", "qatlasd", "raw")
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
	if got := defaultXDGSubdir("raw"); got != "/srv/xdg/qatlasd/raw" {
		t.Errorf("absolute XDG_DATA_HOME: got %q", got)
	}

	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/test")
	if got := defaultXDGSubdir("data"); got != "/home/test/.local/share/qatlasd/data" {
		t.Errorf("HOME fallback: got %q", got)
	}

	t.Setenv("XDG_DATA_HOME", "relative-path-rejected")
	t.Setenv("HOME", "/home/test")
	if got := defaultXDGSubdir("pb_data"); got != "/home/test/.local/share/qatlasd/pb_data" {
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

// clearMinerUEnv unsets every env var that influences MinerU + asset
// download wiring so each test sees a deterministic baseline.
func clearMinerUEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"QATLAS_PAPER_ACCESS_ENABLED",
		"MINERU_API_TOKENS",
		"MINERU_API_BASE_URL",
		"MINERU_MODEL_VERSION",
		"MINERU_LANGUAGE",
		"MINERU_IS_OCR",
		"MINERU_ENABLE_FORMULA",
		"MINERU_ENABLE_TABLE",
		"MINERU_POLL_INTERVAL",
		"MINERU_TIMEOUT",
		"MINERU_MAX_CONCURRENT_JOBS",
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
// mis-route every object into one bucket). This check stays in Load()
// even after the partial-config check was split out into
// ValidateForServe, because the legacy var corrupts data regardless of
// which subcommand is running and must fail-fast on every invocation.
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

// TestValidateForServe_S3PartialConfigRejected verifies the half-set
// S3 invariant. This used to be enforced by Load() but was split into
// ValidateForServe so non-serve subcommands (`qatlasd --help`,
// `qatlasd pat list`, etc.) tolerate a half-configured .env without
// fataling on every invocation.
func TestValidateForServe_S3PartialConfigRejected(t *testing.T) {
	// Each of these subtests sets a *strict subset* of the required
	// fields; Load() must succeed (with a slog.Warn) but ValidateForServe
	// must refuse. The check is symmetric — no single field
	// (endpoint / a bucket / a credential) alone is valid.
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
			cfg, err := Load("")
			if err != nil {
				t.Fatalf("Load returned error %q; expected Load to be best-effort for half-set S3 (only ValidateForServe should reject)", err)
			}
			err = cfg.ValidateForServe()
			if err == nil {
				t.Fatalf("ValidateForServe returned nil; expected partial-config failure")
			}
			// Sanity: error message lists at least one missing field name
			// so an operator can fix it from the log line.
			if !strings.Contains(err.Error(), "QATLAS_S3_") {
				t.Errorf("error %q does not mention any QATLAS_S3_* field name", err.Error())
			}
		})
	}
}

// TestLoad_S3PartialEmitsWarn verifies that Load is best-effort for the
// half-set S3 case but still emits a visible slog.Warn so the operator
// sees the misconfig in every log (not just serve's fatal exit).
func TestLoad_S3PartialEmitsWarn(t *testing.T) {
	clearStorageEnv(t)
	clearS3Env(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("QATLAS_S3_ENDPOINT", "https://x")

	buf := captureSlog(t)
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "object storage config is incomplete") {
		t.Errorf("expected slog.Warn about incomplete object storage config; got %q", got)
	}
	if err := cfg.ValidateForServe(); err == nil {
		t.Errorf("ValidateForServe returned nil after half-set Load; expected error")
	}
}

// ---------------------------------------------------------------------------
// Deprecation warnings for unprefixed legacy aliases (Phase 1).
//
// The aliases still resolve via firstEnv() so existing .env files keep
// working, but Load() now emits a slog.Warn per legacy var found. This
// gives operators one minor cycle to migrate before removal in v0.19.0.
// ---------------------------------------------------------------------------

// captureSlog redirects the default slog logger to an in-memory buffer
// for the duration of the calling test and returns the buffer. The
// previous default is restored when the test ends.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func TestLoad_DeprecatedAliasesEmitWarn(t *testing.T) {
	clearStorageEnv(t)
	clearS3Env(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	legacy := t.TempDir()
	t.Setenv("WIKI_DIR", filepath.Join(legacy, "wiki"))
	t.Setenv("SERVER_HOST", "0.0.0.0")

	buf := captureSlog(t)

	if _, err := Load(""); err != nil {
		t.Fatalf("Load: %v", err)
	}

	out := buf.String()
	for _, expected := range []string{"WIKI_DIR", "SERVER_HOST"} {
		if !strings.Contains(out, expected) {
			t.Errorf("expected slog output to mention %q for deprecated alias; got:\n%s", expected, out)
		}
	}
	if !strings.Contains(out, "without QATLAS_ prefix is deprecated") {
		t.Errorf("expected deprecation message stem in slog output; got:\n%s", out)
	}
	for _, notExpected := range []string{"RAW_DIR", "DATA_DIR", "USER_HEADER"} {
		// Match structured field (deprecated=NAME) so we don't false-positive
		// on the canonical name appearing in the message body.
		if strings.Contains(out, "deprecated="+notExpected) {
			t.Errorf("unset alias %q must not produce a deprecation warn; got:\n%s", notExpected, out)
		}
	}
}

func TestLoad_DeprecatedAliasesQuietWhenAbsent(t *testing.T) {
	clearStorageEnv(t)
	clearS3Env(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	for old := range deprecatedAliases() {
		t.Setenv(old, "")
	}

	buf := captureSlog(t)

	if _, err := Load(""); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if strings.Contains(buf.String(), "deprecated") {
		t.Errorf("no aliases set, but Load emitted a deprecation warn:\n%s", buf.String())
	}
}

func TestDeprecatedAliasesMapCoversAllReaders(t *testing.T) {
	// Guard: every legacy alias we still read in Load() must appear in
	// deprecatedAliases(). If a future refactor adds a new alias to
	// firstEnv() without registering it here, operators silently lose
	// the deprecation signal. Keep this list in sync with the
	// firstEnv("...", "<alias>") calls in Load(). NEO4J_USER and
	// SERVER_DEBUG intentionally excluded (see deprecatedAliases doc).
	expected := []string{
		"WIKI_DIR", "RAW_DIR", "DATA_DIR", "PB_DATA_DIR",
		"SERVER_HOST", "SERVER_PORT",
		"USER_HEADER",
	}
	got := deprecatedAliases()
	for _, name := range expected {
		if _, ok := got[name]; !ok {
			t.Errorf("deprecatedAliases() missing entry for %q", name)
		}
	}
	if len(got) != len(expected) {
		t.Errorf("deprecatedAliases() size = %d, want %d; check whether a new alias was added without registering",
			len(got), len(expected))
	}
}

// ---------------------------------------------------------------------------
// v0.17.0 XDG rename: quantum-atlas/ → qatlasd/
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// (v0.17.0 XDG rename guard tests removed; the legacy quantum-atlas/ check
// itself is gone now that we're still on 0.x and free to make breaking
// changes without a soft-fallback. See git log for the previous
// validateLegacyQuantumAtlasDir implementation if you ever need it back.)
// ---------------------------------------------------------------------------

func TestIsGitHubLoginAllowed_FailClosedWhenEmpty(t *testing.T) {
	c := &Config{} // both lists empty
	if c.IsGitHubLoginAllowed("octocat") {
		t.Error("empty allowlist must reject everyone (fail-closed)")
	}
	if c.IsGitHubLoginAllowed("") {
		t.Error("empty login must be rejected")
	}
}

func TestIsGitHubLoginAllowed_AllowedAndAdminUnion(t *testing.T) {
	c := &Config{
		AllowedGitHubLogins: []string{"Alice", " bob "},
		AdminGitHubLogins:   []string{"carol"},
	}
	cases := map[string]bool{
		"alice":   true, // case-insensitive
		"ALICE":   true,
		"bob":     true,  // trimmed entry
		"carol":   true,  // admins implicitly allowed
		"mallory": false, // not on any list
		"":        false,
	}
	for login, want := range cases {
		if got := c.IsGitHubLoginAllowed(login); got != want {
			t.Errorf("IsGitHubLoginAllowed(%q) = %v, want %v", login, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// QATLAS_PAPER_ACCESS_ENABLED master switch + MinerU* opt-in block
// (issue #8).
// ---------------------------------------------------------------------------

func TestLoad_PaperAccessDisabledByDefault(t *testing.T) {
	clearStorageEnv(t)
	clearS3Env(t)
	clearMinerUEnv(t)
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PaperAccessEnabled {
		t.Error("PaperAccessEnabled = true; want false (default off)")
	}
	if cfg.MinerUEnabled() {
		t.Error("MinerUEnabled() = true; want false when switch off")
	}
}

func TestLoad_PaperAccessIgnoresMinerUWhenSwitchOff(t *testing.T) {
	clearStorageEnv(t)
	clearS3Env(t)
	clearMinerUEnv(t)
	// Switch OFF but MinerU envs set — must be silently ignored so a
	// stale .env doesn't accidentally re-enable the surface.
	t.Setenv("QATLAS_PAPER_ACCESS_ENABLED", "false")
	t.Setenv("MINERU_API_TOKENS", "stale-token")
	t.Setenv("MINERU_POLL_INTERVAL", "not-a-number") // would error if parsed

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v (malformed MinerU env should be ignored when switch off)", err)
	}
	if cfg.PaperAccessEnabled {
		t.Error("switch off but PaperAccessEnabled = true")
	}
	if len(cfg.MinerUAPITokens) != 0 {
		t.Errorf("MinerUAPITokens = %v; want empty when switch off", cfg.MinerUAPITokens)
	}
}

func TestLoad_PaperAccessEnabledLoadsMinerU(t *testing.T) {
	clearStorageEnv(t)
	clearS3Env(t)
	clearMinerUEnv(t)
	t.Setenv("QATLAS_PAPER_ACCESS_ENABLED", "true")
	t.Setenv("MINERU_API_TOKENS", "tok-abc")
	t.Setenv("MINERU_MAX_CONCURRENT_JOBS", "8")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.PaperAccessEnabled {
		t.Error("PaperAccessEnabled = false; want true")
	}
	if !cfg.MinerUEnabled() {
		t.Error("MinerUEnabled() = false; want true with token + switch on")
	}
	if cfg.MinerUMaxConcurrentJobs != 8 {
		t.Errorf("MinerUMaxConcurrentJobs = %d; want 8", cfg.MinerUMaxConcurrentJobs)
	}
	if cfg.MinerUAPIBaseURL != "https://mineru.net" {
		t.Errorf("MinerUAPIBaseURL = %q; want default https://mineru.net", cfg.MinerUAPIBaseURL)
	}
}

func TestLoad_PaperAccessEnabledRejectsMalformedMinerU(t *testing.T) {
	clearStorageEnv(t)
	clearS3Env(t)
	clearMinerUEnv(t)
	t.Setenv("QATLAS_PAPER_ACCESS_ENABLED", "true")
	t.Setenv("MINERU_POLL_INTERVAL", "not-a-number")

	if _, err := Load(""); err == nil {
		t.Error("Load with malformed MINERU_POLL_INTERVAL succeeded; want error")
	}
}

func TestLoad_PaperAccessEnabledNoTokenCacheOnlyMode(t *testing.T) {
	clearStorageEnv(t)
	clearS3Env(t)
	clearMinerUEnv(t)
	t.Setenv("QATLAS_PAPER_ACCESS_ENABLED", "true")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.PaperAccessEnabled {
		t.Error("PaperAccessEnabled = false; want true")
	}
	if cfg.MinerUEnabled() {
		t.Error("MinerUEnabled() = true with no token; want false (cache-only mode)")
	}
}
