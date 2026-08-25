package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// clearConfigEnv unsets every env var that the env-rejection scan
// would flag (plus the XDG/HOME vars that influence path defaults), so
// each test sees the same starting state regardless of how the
// developer's own shell is configured.
func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, raw := range os.Environ() {
		eq := strings.IndexByte(raw, '=')
		if eq <= 0 {
			continue
		}
		name := raw[:eq]
		if isRejectedEnvName(name) {
			t.Setenv(name, "")
		}
	}
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
}

// writeConfig writes content as a YAML config file inside t.TempDir()
// and returns its path.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func mustLoad(t *testing.T, path string) *Config {
	t.Helper()
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%s): %v", path, err)
	}
	return cfg
}

// ---------------------------------------------------------------------------
// expandPath (unchanged semantics from the env era)
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Missing / malformed file
// ---------------------------------------------------------------------------

func TestLoad_MissingFileHintsConfigInit(t *testing.T) {
	clearConfigEnv(t)
	missing := filepath.Join(t.TempDir(), "nope", "config.yaml")
	_, err := Load(missing)
	if err == nil {
		t.Fatal("Load on missing file succeeded; want error")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error should mention the missing path; got: %v", err)
	}
	if !strings.Contains(err.Error(), "qatlasd config init") {
		t.Errorf("error should hint at `qatlasd config init`; got: %v", err)
	}
}

func TestLoad_AllCommentsFileYieldsDefaults(t *testing.T) {
	// `qatlasd config init` writes an all-comments template; loading it
	// must behave like an empty document (defaults everywhere), not a
	// YAML EOF error.
	clearConfigEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := writeConfig(t, "# nothing but comments\n# http_addr: 127.0.0.1:4200\n")
	cfg := mustLoad(t, path)
	if cfg.HTTPAddr != "127.0.0.1:4200" {
		t.Errorf("HTTPAddr = %q, want default", cfg.HTTPAddr)
	}
}

func TestLoad_MalformedYAML(t *testing.T) {
	clearConfigEnv(t)
	path := writeConfig(t, "http_addr: [unclosed")
	if _, err := Load(path); err == nil {
		t.Fatal("Load with malformed YAML succeeded; want error")
	}
}

func TestLoad_UnknownKeyRejected(t *testing.T) {
	// Strict decode: a typo'd key must not silently fall back to the
	// default — that's how "I set s3.endpiont and nothing happened"
	// bugs are born.
	clearConfigEnv(t)
	path := writeConfig(t, "s3:\n  endpiont: https://typo.example\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load with unknown key succeeded; want strict-decode error")
	}
	if !strings.Contains(err.Error(), "endpiont") {
		t.Errorf("error should name the unknown key; got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Environment rejection
// ---------------------------------------------------------------------------

func TestLoad_RejectsEnvConfig(t *testing.T) {
	clearConfigEnv(t)
	path := writeConfig(t, "http_addr: 127.0.0.1:4200\n")
	t.Setenv("QATLAS_POSTGRES_DSN", "postgres://x/y")
	t.Setenv("MINERU_API_TOKENS", "tok-a")
	t.Setenv("GITHUB_CLIENT_ID", "ghid")
	t.Setenv("QATLAS_S3_BUCKET", "qatlas-raw") // legacy single-bucket var

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load with config env vars set succeeded; want rejection error")
	}
	msg := err.Error()
	for _, name := range []string{"GITHUB_CLIENT_ID", "MINERU_API_TOKENS", "QATLAS_POSTGRES_DSN", "QATLAS_S3_BUCKET"} {
		if !strings.Contains(msg, name) {
			t.Errorf("rejection error should list offending var %s; got: %v", name, err)
		}
	}
	if !strings.Contains(msg, "config.yaml") {
		t.Errorf("rejection error should point at the YAML config file; got: %v", err)
	}
}

func TestLoad_RejectsLegacyPrefixFamilies(t *testing.T) {
	clearConfigEnv(t)
	path := writeConfig(t, "{}\n")
	for _, name := range []string{"NEO4J_URI", "POSTGRES_PASSWORD", "MINERU_MODEL_VERSION"} {
		t.Setenv(name, "x")
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load with legacy prefix env vars succeeded; want rejection")
	}
	for _, name := range []string{"NEO4J_URI", "POSTGRES_PASSWORD", "MINERU_MODEL_VERSION"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("rejection error should list %s; got: %v", name, err)
		}
	}
}

func TestLoad_AllowlistTestVarsTolerated(t *testing.T) {
	// Integration-test fixtures (QATLAS_TEST_PG_DSN, QATLAS_S3_TEST_*)
	// don't configure the server and must not trip the rejection.
	clearConfigEnv(t)
	t.Setenv("QATLAS_TEST_PG_DSN", "postgres://x/y")
	t.Setenv("QATLAS_S3_TEST_ENDPOINT", "http://x")
	path := writeConfig(t, "{}\n")
	if _, err := Load(path); err != nil {
		t.Fatalf("Load with allowlisted test env vars failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Full example: every key maps onto the right Config field
// ---------------------------------------------------------------------------

func TestLoad_FullExample(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
http_addr: 0.0.0.0:4200
public_url: https://atlas.example.com
user_header: X-Token-Subject
edge_name: us-east
force_tcp4: true
skip_pb_data_lock: true
paths:
  raw_dir: /srv/raw
  data_dir: /srv/data
  pb_data_dir: /srv/pb_data
postgres:
  dsn: postgres://qatlas:secret@pg:5432/qatlas?sslmode=disable
  max_conns: 25
  corpus_ensure_indexes: false
search:
  providers: [catalog, qdrant]
auth:
  github_client_id: gh-client
  github_client_secret: gh-secret
  allowed_logins: [alice, bob]
  admin_logins: [alice]
s3:
  endpoint: http://10.0.0.1:9000
  public_endpoint: https://raw.example.com
  bucket_pdf: qatlas-pdf
  bucket_md: qatlas-md
  bucket_images: qatlas-images
  bucket_openalex: qatlas-openalex
  access_key_id: AKID
  secret_access_key: SK
paper_access:
  enabled: true
  openalex_mailto: ops@example.com
  arxiv_fetch_concurrent: 3
  arxiv_fetch_rps: 0.5
  mineru:
    api_tokens: [tok-a, tok-b]
    api_base_url: https://mineru.example
    model_version: pipeline
    language: en
    is_ocr: true
    enable_formula: false
    enable_table: false
    poll_interval: 5s
    timeout: 30m
    max_concurrent_jobs: 8
rag:
  qdrant_url: qdrant.internal:6334
  qdrant_api_key: qk
  qdrant_collection: my_collection
  embed_url: http://embed.internal:8801
  embed_token: et
plugins:
  dir: /srv/plugins
  enabled: [graph, lean]
  disabled: [rag]
  connect_secret: cs
  rpc_ws_bind: 127.0.0.1:9999
  event_retention: 2d
  rpc_timeout: 45s
  reconnect_interval: 2500ms
  deadletter_dir: /srv/dead
system_pat:
  token: breakglass-token-123456
  scopes: [papers:read, plugins:read]
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := mustLoad(t, path)

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"HTTPAddr", cfg.HTTPAddr, "0.0.0.0:4200"},
		{"PublicURL", cfg.PublicURL, "https://atlas.example.com"},
		{"UserHeader", cfg.UserHeader, "X-Token-Subject"},
		{"EdgeName", cfg.EdgeName, "us-east"},
		{"ForceTCP4", cfg.ForceTCP4, true},
		{"SkipPBDataLock", cfg.SkipPBDataLock, true},
		{"RawDir", cfg.RawDir, "/srv/raw"},
		{"DataDir", cfg.DataDir, "/srv/data"},
		{"PBDataDir", cfg.PBDataDir, "/srv/pb_data"},
		{"PostgresDSN", cfg.PostgresDSN, "postgres://qatlas:secret@pg:5432/qatlas?sslmode=disable"},
		{"PostgresMaxConns", cfg.PostgresMaxConns, 25},
		{"CorpusEnsureIndexes", cfg.CorpusEnsureIndexes, false},
		{"SearchProviders", strings.Join(cfg.SearchProviders, ","), "catalog,qdrant"},
		{"GitHubClientID", cfg.GitHubClientID, "gh-client"},
		{"GitHubClientSecret", cfg.GitHubClientSecret, "gh-secret"},
		{"AllowedGitHubLogins", strings.Join(cfg.AllowedGitHubLogins, ","), "alice,bob"},
		{"AdminGitHubLogins", strings.Join(cfg.AdminGitHubLogins, ","), "alice"},
		{"S3Endpoint", cfg.S3Endpoint, "http://10.0.0.1:9000"},
		{"S3PublicEndpoint", cfg.S3PublicEndpoint, "https://raw.example.com"},
		{"S3BucketPDF", cfg.S3BucketPDF, "qatlas-pdf"},
		{"S3BucketMD", cfg.S3BucketMD, "qatlas-md"},
		{"S3BucketImages", cfg.S3BucketImages, "qatlas-images"},
		{"S3BucketOpenAlex", cfg.S3BucketOpenAlex, "qatlas-openalex"},
		{"S3AccessKeyID", cfg.S3AccessKeyID, "AKID"},
		{"S3SecretAccessKey", cfg.S3SecretAccessKey, "SK"},
		{"PaperAccessEnabled", cfg.PaperAccessEnabled, true},
		{"OpenAlexMailto", cfg.OpenAlexMailto, "ops@example.com"},
		{"ArxivFetchConcurrent", cfg.ArxivFetchConcurrent, 3},
		{"ArxivFetchRPS", cfg.ArxivFetchRPS, 0.5},
		{"MinerUAPITokens", strings.Join(cfg.MinerUAPITokens, ","), "tok-a,tok-b"},
		{"MinerUAPIBaseURL", cfg.MinerUAPIBaseURL, "https://mineru.example"},
		{"MinerUModelVersion", cfg.MinerUModelVersion, "pipeline"},
		{"MinerULanguage", cfg.MinerULanguage, "en"},
		{"MinerUIsOCR", cfg.MinerUIsOCR, true},
		{"MinerUEnableFormula", cfg.MinerUEnableFormula, false},
		{"MinerUEnableTable", cfg.MinerUEnableTable, false},
		{"MinerUPollInterval", cfg.MinerUPollInterval, 5 * time.Second},
		{"MinerUTimeout", cfg.MinerUTimeout, 30 * time.Minute},
		{"MinerUMaxConcurrentJobs", cfg.MinerUMaxConcurrentJobs, 8},
		{"RAGQdrantURL", cfg.RAGQdrantURL, "qdrant.internal:6334"},
		{"RAGQdrantAPIKey", cfg.RAGQdrantAPIKey, "qk"},
		{"RAGQdrantCollection", cfg.RAGQdrantCollection, "my_collection"},
		{"RAGEmbedURL", cfg.RAGEmbedURL, "http://embed.internal:8801"},
		{"RAGEmbedToken", cfg.RAGEmbedToken, "et"},
		{"PluginsDir", cfg.PluginsDir, "/srv/plugins"},
		{"PluginsEnabled", strings.Join(cfg.PluginsEnabled, ","), "graph,lean"},
		{"PluginsDisabled", strings.Join(cfg.PluginsDisabled, ","), "rag"},
		{"PluginConnectSecret", cfg.PluginConnectSecret, "cs"},
		{"RPCWSBind", cfg.RPCWSBind, "127.0.0.1:9999"},
		{"EventRetention", cfg.EventRetention, 48 * time.Hour},
		{"PluginRPCTimeout", cfg.PluginRPCTimeout, 45 * time.Second},
		{"PluginReconnectInterval", cfg.PluginReconnectInterval, 2500 * time.Millisecond},
		{"DeadLetterDir", cfg.DeadLetterDir, "/srv/dead"},
		{"SystemPATToken", cfg.SystemPATToken, "breakglass-token-123456"},
		{"SystemPATScopes", strings.Join(cfg.SystemPATScopes, ","), "papers:read,plugins:read"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if !cfg.S3Enabled() {
		t.Error("S3Enabled() = false with full s3 section; want true")
	}
	if !cfg.MinerUEnabled() {
		t.Error("MinerUEnabled() = false with tokens + switch on; want true")
	}
}

// ---------------------------------------------------------------------------
// search.remote / search.agentic
// ---------------------------------------------------------------------------

func TestLoad_SearchRemoteAgentic(t *testing.T) {
	clearConfigEnv(t)
	path := writeConfig(t, `
search:
  providers: [catalog, remote]
  remote:
    enabled: true
    url: http://qatlas-search:8600
    token: svc-token-123
    timeout: 90s
  agentic:
    daily_limit: 500
    price_per_mtok: 2.5
`)
	cfg := mustLoad(t, path)

	if !cfg.RemoteEnabled {
		t.Error("RemoteEnabled = false, want true")
	}
	if cfg.RemoteURL != "http://qatlas-search:8600" {
		t.Errorf("RemoteURL = %q", cfg.RemoteURL)
	}
	if cfg.RemoteToken != "svc-token-123" {
		t.Errorf("RemoteToken = %q", cfg.RemoteToken)
	}
	if cfg.RemoteTimeout != 90*time.Second {
		t.Errorf("RemoteTimeout = %v, want 90s", cfg.RemoteTimeout)
	}
	if cfg.AgenticDailyLimit != 500 {
		t.Errorf("AgenticDailyLimit = %d, want 500", cfg.AgenticDailyLimit)
	}
	if cfg.AgenticPricePerMtok != 2.5 {
		t.Errorf("AgenticPricePerMtok = %v, want 2.5", cfg.AgenticPricePerMtok)
	}
	if got := strings.Join(cfg.SearchProviders, ","); got != "catalog,remote" {
		t.Errorf("SearchProviders = %q", got)
	}
}

func TestLoad_SearchRemoteAgenticDefaults(t *testing.T) {
	clearConfigEnv(t)
	path := writeConfig(t, "{}\n")
	cfg := mustLoad(t, path)

	if cfg.RemoteEnabled {
		t.Error("RemoteEnabled = true by default, want false")
	}
	if cfg.RemoteTimeout != 60*time.Second {
		t.Errorf("RemoteTimeout = %v, want 60s default", cfg.RemoteTimeout)
	}
	if cfg.AgenticDailyLimit != 10000 {
		t.Errorf("AgenticDailyLimit = %d, want 10000 default", cfg.AgenticDailyLimit)
	}
	if cfg.AgenticPricePerMtok != 0.0 {
		t.Errorf("AgenticPricePerMtok = %v, want 0 default", cfg.AgenticPricePerMtok)
	}
}

func TestLoad_SearchRemoteRejectsMalformedTimeout(t *testing.T) {
	clearConfigEnv(t)
	path := writeConfig(t, "search:\n  remote:\n    timeout: banana\n")
	if _, err := Load(path); err == nil {
		t.Fatal("Load succeeded with malformed search.remote.timeout, want error")
	}
}

// ---------------------------------------------------------------------------
// Defaults
// ---------------------------------------------------------------------------

func TestLoad_Defaults(t *testing.T) {
	clearConfigEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := writeConfig(t, "{}\n")
	cfg := mustLoad(t, path)

	base := filepath.Join(home, ".local", "share", "qatlasd")
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"HTTPAddr", cfg.HTTPAddr, "127.0.0.1:4200"},
		{"RawDir", cfg.RawDir, filepath.Join(base, "raw")},
		{"DataDir", cfg.DataDir, filepath.Join(base, "data")},
		{"PBDataDir", cfg.PBDataDir, filepath.Join(base, "pb_data")},
		{"PostgresMaxConns", cfg.PostgresMaxConns, 10},
		{"CorpusEnsureIndexes", cfg.CorpusEnsureIndexes, true},
		{"SearchProviders", strings.Join(cfg.SearchProviders, ","), "catalog,arxiv,openalex"},
		{"RAGQdrantCollection", cfg.RAGQdrantCollection, "qatlas_papers_v1"},
		{"ArxivFetchConcurrent", cfg.ArxivFetchConcurrent, 2},
		{"ArxivFetchRPS", cfg.ArxivFetchRPS, 0.33},
		{"PaperAccessEnabled", cfg.PaperAccessEnabled, false},
		{"RPCWSBind", cfg.RPCWSBind, "127.0.0.1:8799"},
		{"EventRetention", cfg.EventRetention, 7 * 24 * time.Hour},
		{"PluginRPCTimeout", cfg.PluginRPCTimeout, 30 * time.Second},
		{"PluginReconnectInterval", cfg.PluginReconnectInterval, 5 * time.Second},
		{"PluginsDir", cfg.PluginsDir, filepath.Join(home, ".config", "qatlasd", "plugins")},
		{"DeadLetterDir", cfg.DeadLetterDir, filepath.Join(home, ".local", "state", "qatlasd", "dead")},
		{"S3Enabled", cfg.S3Enabled(), false},
		{"MinerUEnabled", cfg.MinerUEnabled(), false},
		{"SystemPATToken", cfg.SystemPATToken, ""},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("default %s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestLoad_RelativePathsAnchorToConfigDir(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
paths:
  raw_dir: ../raw
  pb_data_dir: pb_data
plugins:
  dir: plugins
  deadletter_dir: dead
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := mustLoad(t, path)

	if want := filepath.Join(filepath.Dir(dir), "raw"); cfg.RawDir != want {
		t.Errorf("RawDir = %q, want %q (relative to config dir)", cfg.RawDir, want)
	}
	if want := filepath.Join(dir, "pb_data"); cfg.PBDataDir != want {
		t.Errorf("PBDataDir = %q, want %q", cfg.PBDataDir, want)
	}
	if want := filepath.Join(dir, "plugins"); cfg.PluginsDir != want {
		t.Errorf("PluginsDir = %q, want %q", cfg.PluginsDir, want)
	}
	if want := filepath.Join(dir, "dead"); cfg.DeadLetterDir != want {
		t.Errorf("DeadLetterDir = %q, want %q", cfg.DeadLetterDir, want)
	}
}

// ---------------------------------------------------------------------------
// S3 / object storage invariants
// ---------------------------------------------------------------------------

func TestValidateForServe_S3PartialConfigRejected(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"only endpoint", "s3:\n  endpoint: https://x\n"},
		{"only pdf bucket", "s3:\n  bucket_pdf: b\n"},
		{"only access key", "s3:\n  access_key_id: a\n"},
		{"endpoint + buckets, no creds", `s3:
  endpoint: https://x
  bucket_pdf: p
  bucket_md: m
  bucket_images: i
`},
		{"two of three buckets", `s3:
  endpoint: https://x
  bucket_pdf: p
  bucket_md: m
  access_key_id: a
  secret_access_key: s
`},
		{"creds, no endpoint", `s3:
  access_key_id: a
  secret_access_key: s
`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearConfigEnv(t)
			path := writeConfig(t, c.content)
			cfg := mustLoad(t, path) // Load is best-effort for half-set S3
			err := cfg.ValidateForServe()
			if err == nil {
				t.Fatal("ValidateForServe returned nil; expected partial-config failure")
			}
			if !strings.Contains(err.Error(), "s3.") {
				t.Errorf("error %q does not mention any s3.* key", err.Error())
			}
		})
	}
}

func TestValidateForServe_S3FullConfigAccepted(t *testing.T) {
	clearConfigEnv(t)
	path := writeConfig(t, `s3:
  endpoint: https://x
  bucket_pdf: p
  bucket_md: m
  bucket_images: i
  access_key_id: a
  secret_access_key: s
`)
	cfg := mustLoad(t, path)
	if err := cfg.ValidateForServe(); err != nil {
		t.Fatalf("ValidateForServe on full s3 config: %v", err)
	}
	if !cfg.S3Enabled() {
		t.Error("S3Enabled() = false; want true")
	}
}

// ---------------------------------------------------------------------------
// MinerU gating (paper_access.enabled master switch)
// ---------------------------------------------------------------------------

func TestLoad_PaperAccessIgnoresMinerUWhenSwitchOff(t *testing.T) {
	// Switch OFF but MinerU keys set — must be silently ignored so a
	// stale section doesn't accidentally re-enable the surface.
	clearConfigEnv(t)
	path := writeConfig(t, `paper_access:
  enabled: false
  mineru:
    api_tokens: [stale-token]
    poll_interval: not-a-duration
`)
	cfg := mustLoad(t, path)
	if cfg.PaperAccessEnabled {
		t.Error("switch off but PaperAccessEnabled = true")
	}
	if len(cfg.MinerUAPITokens) != 0 {
		t.Errorf("MinerUAPITokens = %v; want empty when switch off", cfg.MinerUAPITokens)
	}
}

func TestLoad_PaperAccessEnabledAppliesMinerUDefaults(t *testing.T) {
	clearConfigEnv(t)
	path := writeConfig(t, "paper_access:\n  enabled: true\n")
	cfg := mustLoad(t, path)
	if !cfg.PaperAccessEnabled {
		t.Error("PaperAccessEnabled = false; want true")
	}
	if cfg.MinerUEnabled() {
		t.Error("MinerUEnabled() = true with no tokens; want false (cache-only mode)")
	}
	if cfg.MinerUAPIBaseURL != "https://mineru.net" {
		t.Errorf("MinerUAPIBaseURL = %q; want default https://mineru.net", cfg.MinerUAPIBaseURL)
	}
	if cfg.MinerUModelVersion != "vlm" {
		t.Errorf("MinerUModelVersion = %q; want vlm", cfg.MinerUModelVersion)
	}
	if cfg.MinerULanguage != "ch" {
		t.Errorf("MinerULanguage = %q; want ch", cfg.MinerULanguage)
	}
	if !cfg.MinerUEnableFormula || !cfg.MinerUEnableTable || cfg.MinerUIsOCR {
		t.Errorf("formula/table/ocr defaults wrong: formula=%v table=%v ocr=%v",
			cfg.MinerUEnableFormula, cfg.MinerUEnableTable, cfg.MinerUIsOCR)
	}
	if cfg.MinerUPollInterval != 3*time.Second {
		t.Errorf("MinerUPollInterval = %s; want 3s", cfg.MinerUPollInterval)
	}
	if cfg.MinerUTimeout != 1800*time.Second {
		t.Errorf("MinerUTimeout = %s; want 1800s", cfg.MinerUTimeout)
	}
	if cfg.MinerUMaxConcurrentJobs != 4 {
		t.Errorf("MinerUMaxConcurrentJobs = %d; want 4", cfg.MinerUMaxConcurrentJobs)
	}
}

func TestLoad_PaperAccessEnabledRejectsMalformedMinerU(t *testing.T) {
	clearConfigEnv(t)
	path := writeConfig(t, `paper_access:
  enabled: true
  mineru:
    poll_interval: not-a-duration
`)
	if _, err := Load(path); err == nil {
		t.Error("Load with malformed mineru.poll_interval succeeded; want error")
	}
}

func TestLoad_PaperAccessEnabledRejectsZeroConcurrency(t *testing.T) {
	clearConfigEnv(t)
	path := writeConfig(t, `paper_access:
  enabled: true
  mineru:
    max_concurrent_jobs: 0
`)
	if _, err := Load(path); err == nil {
		t.Error("Load with max_concurrent_jobs=0 succeeded; want error")
	}
}

// ---------------------------------------------------------------------------
// GitHub login allowlists (unchanged policy)
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

func TestIsGitHubAdmin_AdminListOnly(t *testing.T) {
	c := &Config{
		AllowedGitHubLogins: []string{"Alice"},
		AdminGitHubLogins:   []string{"carol"},
	}
	cases := map[string]bool{
		"CAROL": true, // case-insensitive
		"carol": true,
		"alice": false, // allowed sign-in but NOT admin
		"":      false,
	}
	for login, want := range cases {
		if got := c.IsGitHubAdmin(login); got != want {
			t.Errorf("IsGitHubAdmin(%q) = %v, want %v", login, got, want)
		}
	}
	// Fail-closed when the admin list is empty.
	if (&Config{}).IsGitHubAdmin("carol") {
		t.Error("empty admin allowlist must reject everyone (fail-closed)")
	}
}
