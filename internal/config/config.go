// Package config loads QuantumAtlas server configuration from a YAML
// file (default ~/.qatlas/config.yaml).
//
// Environment-variable configuration is REJECTED by design: after
// loading the file, Load scans os.Environ for any QATLAS_* variable
// (and a list of legacy names from the .env era) and fails startup
// with a message listing the offenders. The YAML file is the single
// source of truth; docker deployments bind-mount the host's config
// file into the container read-only.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds the resolved runtime configuration for the Go server.
//
// All fields are populated from the YAML config file. Empty / zero
// values mean "feature disabled" unless otherwise documented.
type Config struct {
	// HTTP bind address (host:port). Defaults to 127.0.0.1:4200.
	HTTPAddr string

	// Filesystem roots. Defaults are computed by Load when the
	// corresponding YAML keys are absent:
	//   - RawDir    -> ${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/raw
	//   - DataDir   -> ${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/data
	//   - PBDataDir -> ${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/pb_data
	// "anchor" is the directory containing the config file, used to
	// resolve relative paths.
	RawDir    string // RAW asset store (PDFs, MinerU outputs, etc.).
	DataDir   string // server-managed metadata (ingests/, MinerU lease state, etc.).
	PBDataDir string // PocketBase pb_data (SQLite + uploads); passed to --dir=.

	// ForceTCP4 pins the HTTP listener to a tcp4-only socket (WSL2 +
	// Windows netsh portproxy escape hatch). Default false.
	ForceTCP4 bool

	// SkipPBDataLock bypasses the single-process flock on pb_data.
	// Reserved for emergency recovery / diagnostics — never production.
	SkipPBDataLock bool

	// PostgreSQL catalog (server-only, non-login state).
	PostgresDSN      string
	PostgresMaxConns int

	// CorpusEnsureIndexes gates the boot-time build of the heavy
	// openalex_works indexes (GIN on the jsonb record + citation array +
	// tsvector, plus the btree hot columns). Default true. Set false when an
	// edge points at a large, pre-provisioned corpus whose indexes are built
	// out-of-band (e.g. by `qatlasd openalex bootstrap-pg`): the base schema
	// still ensures at boot, but the heavy CONCURRENTLY index builds — which
	// take heavy I/O on a 353 GB table — are skipped. See ADR 0013.
	CorpusEnsureIndexes bool

	// SearchProviders is the ordered list of search providers the
	// /api/search engine fans out to (internal/search). From the
	// search.providers YAML list; defaults to [catalog, arxiv, openalex].
	// "remote" is only constructible when search.remote below is
	// enabled with a URL — an enabled-but-unconstructible provider is
	// logged and skipped at boot.
	SearchProviders []string

	// Remote search microservice (qatlas-search) — an optional Provider
	// plugged into the /api/search fan-out and the backend for the
	// metered POST /api/search/agentic endpoint. RemoteEnabled=false
	// (default) leaves everything off, even when a URL is set.
	// RemoteToken must equal the microservice's search.service_token.
	// RemoteTimeout defaults to 60s because agentic calls may drive an
	// LLM — much longer than the per-provider fan-out budget.
	RemoteEnabled bool
	RemoteURL     string
	RemoteToken   string
	RemoteTimeout time.Duration

	// Agentic search metering. AgenticDailyLimit is the fallback daily
	// per-user limit for POST /api/search/agentic when the user has no
	// plan/quota row in Postgres (default 10000). AgenticPricePerMtok is
	// USD per 1M LLM tokens, used ONLY to display an approximate cost in
	// the admin usage view — it gates nothing.
	AgenticDailyLimit   int
	AgenticPricePerMtok float64

	// AgenticBackend selects the backend behind POST /api/search/agentic:
	// "remote" (default, the qatlas-search microservice via search.remote)
	// or "local" (the internal/agentic runner driving the local claude
	// CLI inside a per-request sandbox). The AgenticLocal* fields configure
	// the local backend: ClaudeBin (default "claude", PATH lookup), Model
	// (empty = claude's own default), SandboxDir (empty =
	// paths.data_dir/agentic), Timeout per claude call (default 5m),
	// Retention for sandbox directories before the janitor sweeps them
	// (default 24h), MaxBudgetUSD per call (0 = no --max-budget-usd flag),
	// PromptTemplate overriding the embedded prompt (empty = embedded).
	AgenticBackend           string
	AgenticLocalClaudeBin    string
	AgenticLocalModel        string
	AgenticLocalSandboxDir   string
	AgenticLocalTimeout      time.Duration
	AgenticLocalRetention    time.Duration
	AgenticLocalMaxBudgetUSD float64
	AgenticLocalPromptTpl    string

	// Public URL: server's own canonical https origin (scheme+host[+port])
	// as users see it from outside any reverse proxy. Required for
	// constructing absolute redirect URLs (OAuth callbacks, OpenAlex
	// sync links) since the proxy may rewrite Host headers.
	PublicURL string

	// Audit header injected by the upstream reverse proxy.
	UserHeader string

	// GitHub OAuth (for PocketBase auth_collection_oauth2 settings).
	GitHubClientID     string
	GitHubClientSecret string

	// GitHub login whitelist auto-promoted to admin on first OAuth login.
	AdminGitHubLogins []string

	// GitHub logins auto-promoted to superadmin (is_superadmin flag on
	// the users record) at bootstrap. Superadmins may manage other
	// users' is_admin flag via /api/admin/users in addition to
	// everything is_admin holders can do. Also seeds the flag recovery
	// path: promote-yourself here, restart, then demote via the API.
	SuperadminGitHubLogins []string

	// GitHub login allowlist gating OAuth sign-in. Only accounts whose
	// GitHub login appears here (or in AdminGitHubLogins) may obtain an
	// authenticated session. Fail-closed: when this AND AdminGitHubLogins
	// are both empty, NOBODY may sign in (see IsGitHubLoginAllowed). The
	// PocketBase superuser (email+password at /_/) is unaffected and is
	// the recovery path.
	AllowedGitHubLogins []string

	// Gitea OAuth (optional self-hosted Gitea/Forgejo instance, a second
	// provider alongside GitHub). GiteaURL is the instance origin (e.g.
	// "https://git.example.com"); the authorize/token/userinfo endpoints
	// are derived from it when the provider is synced onto the users
	// collection (the stock PocketBase gitea provider points at
	// gitea.com, which is wrong for a self-hosted instance).
	GiteaURL          string
	GiteaClientID     string
	GiteaClientSecret string

	// Gitea login lists mirroring the GitHub ones above. Deliberately
	// SEPARATE from the GitHub lists: a GitHub login and a Gitea login
	// are different identities, so an entry in auth.admin_logins must
	// not also make the same-named Gitea account an admin (and vice
	// versa). There is NO sign-in allowlist for Gitea — every account
	// on the configured instance may sign in (operator choice); these
	// lists only grant admin / superadmin.
	AdminGiteaLogins      []string
	SuperadminGiteaLogins []string

	// Object storage (RustFS / S3-compatible) for the RAW asset bucket.
	// When S3Endpoint is empty the server falls back to RawDir on the
	// local filesystem. When set, all required fields must be non-empty
	// — see ValidateForServe.
	//
	// Endpoint must include scheme (https://raw.example.tld) so the
	// minio-go client can decide TLS vs plaintext deterministically.
	//
	// S3PublicEndpoint (optional) splits the network roles:
	//   - S3Endpoint        is used for server↔RustFS traffic (mesh,
	//                       intranet, anything cheap & fast).
	//   - S3PublicEndpoint  is used ONLY when minting presigned URLs
	//                       for end users. Must front the same bucket +
	//                       credentials as S3Endpoint.
	//
	// When empty (or equal to S3Endpoint), presigned URLs reuse the
	// internal endpoint — handy for single-network dev setups.
	S3Endpoint       string
	S3PublicEndpoint string

	// Per-kind buckets (v0.7.0). S3BucketOpenAlex is reserved for the
	// OpenAlex snapshot ingest and is optional — the server runs without
	// it; only `openalex` subcommands need it.
	//
	// All three of PDF/MD/Images are required together when S3 is
	// enabled (ValidateForServe).
	S3BucketPDF      string
	S3BucketMD       string
	S3BucketImages   string
	S3BucketOpenAlex string

	S3AccessKeyID     string
	S3SecretAccessKey string

	// EdgeName labels which edge this process runs on (e.g. "edge-a",
	// "us-east", "cn-shanghai"). It is purely cosmetic: it's folded
	// into the S3 client User-Agent (qatlasd/<version>/<edge>) so the
	// RustFS audit trail can tell apart writes coming from different
	// edges at a glance. Empty → the UA is just qatlasd/<version>.
	// Never load-bearing for auth.
	EdgeName string

	// PaperAccessEnabled is the master switch for the opt-in
	// paper-access serving + server-side MinerU surface (issue #8).
	// When false (default) the /api/papers/{id}/markdown and
	// /markdown/status endpoints are NOT registered and the entire
	// MinerU* block below is ignored — even garbage values are tolerated
	// silently.
	//
	// When true the operator opts into:
	//   - serving cached markdown bytes (papers:read scope)
	//   - when MinerUAPITokens contains at least one entry, transparently
	//     triggering a MinerU conversion on cache miss
	PaperAccessEnabled bool

	// Server-side MinerU configuration. Only parsed (and only validated)
	// when PaperAccessEnabled=true. When the switch is off these
	// fields are zero values regardless of YAML content.
	//
	// MinerUAPITokens is a pool — supply multiple tokens and the
	// converter automatically fails over from one to the next when a key
	// reports daily-limit. Empty pool ⇒ cache-only mode.
	MinerUAPITokens         []string
	MinerUAPIBaseURL        string
	MinerUModelVersion      string
	MinerULanguage          string
	MinerUIsOCR             bool
	MinerUEnableFormula     bool
	MinerUEnableTable       bool
	MinerUPollInterval      time.Duration
	MinerUTimeout           time.Duration
	MinerUMaxConcurrentJobs int

	// --- RAG index push (qatlas-rag microservice) ------------------
	//
	// Semantic retrieval has moved out of qatlasd: the standalone
	// qatlas-rag microservice owns the vector index. When rag.remote
	// is enabled, qatlasd pushes an index-build task to qatlas-rag
	// (POST {url}/v1/index) whenever a paper flips to 'ready'
	// (ingest pipeline and MinerU conversion completion points).

	// RAGRemoteEnabled is the master switch for the qatlas-rag index
	// push. False (default) leaves everything off, even when a URL is
	// set.
	RAGRemoteEnabled bool

	// RAGRemoteURL — qatlas-rag base URL (e.g.
	// "http://qatlas-rag:8700"). qatlasd calls /v1/index here; the
	// /healthz probe is anonymous.
	RAGRemoteURL string

	// RAGRemoteToken — bearer the microservice requires on /v1/index.
	// Must equal the microservice's service_token.
	RAGRemoteToken string

	// RAGRemoteTimeout bounds each /v1/index call. Default 30s.
	RAGRemoteTimeout time.Duration

	// OpenAlexMailto is the contact email folded into the polite-pool
	// User-Agent for OpenAlex API calls and outbound arxiv.org PDF
	// fetches. Required when PaperAccessEnabled=true AND any
	// derivative-access endpoint that may resolve DOIs or fetch PDFs
	// from arxiv is exercised; the DOI route returns 503 when this is
	// empty so misconfigured deployments fail loudly.
	OpenAlexMailto string

	// ArxivFetchConcurrent caps the number of in-flight server-side
	// arxiv.org PDF fetches independently from MinerU job concurrency.
	// Default 2. Only consulted when PaperAccessEnabled=true.
	ArxivFetchConcurrent int

	// ArxivFetchRPS bounds the per-process rate of arxiv.org GET
	// requests (token bucket). Default 0.33 req/s with burst 2, matching
	// arxiv's published "one request every 3 seconds" guidance.
	ArxivFetchRPS float64

	// Robust downloader (internal/downloader; POST /api/downloader/*).
	// Master switch defaults to on but is subordinate to
	// PaperAccessEnabled — the downloader needs the same fetcher /
	// resolver / objstore wiring. DownloaderAgentBackend selects the
	// fallback link extractor: "" = off, "openai" (OpenAI-compatible
	// endpoint via BaseURL/APIKey/Model), "claude" (local claude CLI).
	DownloaderEnabled        bool
	DownloaderConcurrency    int
	DownloaderUnpaywallEmail string
	// DownloaderS2APIKey is an optional Semantic Scholar key — the
	// unauthenticated pool is globally shared and bursts into 429s; a
	// free key (1 rps) stabilizes the highest-recall OA resolver.
	DownloaderS2APIKey          string
	DownloaderRespectRobots     bool
	DownloaderAgentBackend      string
	DownloaderAgentBaseURL      string
	DownloaderAgentAPIKey       string
	DownloaderAgentModel        string
	DownloaderAgentMaxTokens    int
	DownloaderAgentClaudeBin    string
	DownloaderAgentClaudeModel  string
	DownloaderAgentTimeout      time.Duration
	DownloaderAgentMaxBudgetUSD float64
	// DownloaderBrowserCDPURL points the browser lane at an
	// already-running Chromium's DevTools endpoint (e.g. the
	// chromedp/headless-shell compose sidecar, ws://browser:9222).
	// Empty disables the lane; publisher logins live in that browser's
	// user-data-dir.
	DownloaderBrowserCDPURL  string
	DownloaderBrowserTimeout time.Duration

	// DownloaderProxy* wires the ladder at a standalone downloaderproxy
	// (cmd/downloaderproxy) deployed on a directly-entitled machine
	// (campus egress, e.g. an Ag-Workstation). Papers whose local
	// attempts hit entitlement walls are delegated to it.
	DownloaderProxyURL     string
	DownloaderProxyToken   string
	DownloaderProxyTimeout time.Duration

	// Plugin platform. Plugins are optional: an empty directory or no
	// manifests means the core server still starts with just papers /
	// auth enabled.
	PluginsDir              string
	PluginsEnabled          []string
	PluginsDisabled         []string
	PluginConnectSecret     string
	RPCWSBind               string
	EventRetention          time.Duration
	PluginRPCTimeout        time.Duration
	PluginReconnectInterval time.Duration
	DeadLetterDir           string

	// System PAT — operator breakglass bearer token (see
	// internal/pat/system_pat.go). Empty token = feature disabled.
	// Scopes defaults to ["*"] when the token is set but the list is
	// empty.
	SystemPATToken  string
	SystemPATScopes []string
}

// MinerUEnabled reports whether the server should drive MinerU itself
// on cache miss. True iff the master switch is on AND at least one
// MinerU API token is configured. When false the markdown endpoints
// (when registered) serve cached bytes only and return 503 on cache
// miss.
func (c *Config) MinerUEnabled() bool {
	return c.PaperAccessEnabled && len(c.MinerUAPITokens) > 0
}

// ---------------------------------------------------------------------------
// YAML file schema
// ---------------------------------------------------------------------------

// fileConfig mirrors the on-disk YAML schema. String fields map 1:1 onto
// Config; scalar fields with non-zero defaults use pointers so Load can
// distinguish "key absent" (apply default) from "key set to zero value"
// (honour the operator's explicit choice). Durations are strings parsed
// by parseDuration (Go duration syntax plus a "Nd" days extension).
type fileConfig struct {
	HTTPAddr       string `yaml:"http_addr"`
	PublicURL      string `yaml:"public_url"`
	UserHeader     string `yaml:"user_header"`
	EdgeName       string `yaml:"edge_name"`
	ForceTCP4      bool   `yaml:"force_tcp4"`
	SkipPBDataLock bool   `yaml:"skip_pb_data_lock"`

	Paths struct {
		RawDir    string `yaml:"raw_dir"`
		DataDir   string `yaml:"data_dir"`
		PBDataDir string `yaml:"pb_data_dir"`
	} `yaml:"paths"`

	Postgres struct {
		DSN                 string `yaml:"dsn"`
		MaxConns            *int   `yaml:"max_conns"`
		CorpusEnsureIndexes *bool  `yaml:"corpus_ensure_indexes"`
	} `yaml:"postgres"`

	Search struct {
		Providers []string `yaml:"providers"`
		Remote    struct {
			Enabled bool   `yaml:"enabled"`
			URL     string `yaml:"url"`
			Token   string `yaml:"token"`
			Timeout string `yaml:"timeout"`
		} `yaml:"remote"`
		Agentic struct {
			DailyLimit   *int     `yaml:"daily_limit"`
			PricePerMtok *float64 `yaml:"price_per_mtok"`
			Backend      string   `yaml:"backend"`
			Local        struct {
				ClaudeBin      string   `yaml:"claude_bin"`
				Model          string   `yaml:"model"`
				SandboxDir     string   `yaml:"sandbox_dir"`
				Timeout        string   `yaml:"timeout"`
				Retention      string   `yaml:"retention"`
				MaxBudgetUSD   *float64 `yaml:"max_budget_usd"`
				PromptTemplate string   `yaml:"prompt_template"`
			} `yaml:"local"`
		} `yaml:"agentic"`
	} `yaml:"search"`

	Auth struct {
		GitHubClientID     string   `yaml:"github_client_id"`
		GitHubClientSecret string   `yaml:"github_client_secret"`
		AllowedLogins      []string `yaml:"allowed_logins"`
		AdminLogins        []string `yaml:"admin_logins"`
		SuperadminLogins   []string `yaml:"superadmin_logins"`

		GiteaURL              string   `yaml:"gitea_url"`
		GiteaClientID         string   `yaml:"gitea_client_id"`
		GiteaClientSecret     string   `yaml:"gitea_client_secret"`
		GiteaAdminLogins      []string `yaml:"gitea_admin_logins"`
		GiteaSuperadminLogins []string `yaml:"gitea_superadmin_logins"`
	} `yaml:"auth"`

	S3 struct {
		Endpoint        string `yaml:"endpoint"`
		PublicEndpoint  string `yaml:"public_endpoint"`
		BucketPDF       string `yaml:"bucket_pdf"`
		BucketMD        string `yaml:"bucket_md"`
		BucketImages    string `yaml:"bucket_images"`
		BucketOpenAlex  string `yaml:"bucket_openalex"`
		AccessKeyID     string `yaml:"access_key_id"`
		SecretAccessKey string `yaml:"secret_access_key"`
	} `yaml:"s3"`

	PaperAccess struct {
		Enabled              bool     `yaml:"enabled"`
		OpenAlexMailto       string   `yaml:"openalex_mailto"`
		ArxivFetchConcurrent *int     `yaml:"arxiv_fetch_concurrent"`
		ArxivFetchRPS        *float64 `yaml:"arxiv_fetch_rps"`
		MinerU               struct {
			APITokens         []string `yaml:"api_tokens"`
			APIBaseURL        string   `yaml:"api_base_url"`
			ModelVersion      string   `yaml:"model_version"`
			Language          string   `yaml:"language"`
			IsOCR             *bool    `yaml:"is_ocr"`
			EnableFormula     *bool    `yaml:"enable_formula"`
			EnableTable       *bool    `yaml:"enable_table"`
			PollInterval      string   `yaml:"poll_interval"`
			Timeout           string   `yaml:"timeout"`
			MaxConcurrentJobs *int     `yaml:"max_concurrent_jobs"`
		} `yaml:"mineru"`
	} `yaml:"paper_access"`

	Downloader struct {
		Enabled        *bool  `yaml:"enabled"`
		Concurrency    *int   `yaml:"concurrency"`
		UnpaywallEmail string `yaml:"unpaywall_email"`
		S2APIKey       string `yaml:"s2_api_key"`
		RespectRobots  *bool  `yaml:"respect_robots"`
		Agent          struct {
			Backend      string  `yaml:"backend"`
			BaseURL      string  `yaml:"base_url"`
			APIKey       string  `yaml:"api_key"`
			Model        string  `yaml:"model"`
			MaxTokens    *int    `yaml:"max_tokens"`
			ClaudeBin    string  `yaml:"claude_bin"`
			ClaudeModel  string  `yaml:"claude_model"`
			Timeout      string  `yaml:"timeout"`
			MaxBudgetUSD float64 `yaml:"max_budget_usd"`
		} `yaml:"agent"`
		Browser struct {
			CDPURL  string `yaml:"cdp_url"`
			Timeout string `yaml:"timeout"`
		} `yaml:"browser"`
		Proxy struct {
			URL     string `yaml:"url"`
			Token   string `yaml:"token"`
			Timeout string `yaml:"timeout"`
		} `yaml:"proxy"`
	} `yaml:"downloader"`

	RAG struct {
		Remote struct {
			Enabled bool   `yaml:"enabled"`
			URL     string `yaml:"url"`
			Token   string `yaml:"token"`
			Timeout string `yaml:"timeout"`
		} `yaml:"remote"`
	} `yaml:"rag"`

	Plugins struct {
		Dir               string   `yaml:"dir"`
		Enabled           []string `yaml:"enabled"`
		Disabled          []string `yaml:"disabled"`
		ConnectSecret     string   `yaml:"connect_secret"`
		RPCWSBind         string   `yaml:"rpc_ws_bind"`
		EventRetention    string   `yaml:"event_retention"`
		RPCTimeout        string   `yaml:"rpc_timeout"`
		ReconnectInterval string   `yaml:"reconnect_interval"`
		DeadLetterDir     string   `yaml:"deadletter_dir"`
	} `yaml:"plugins"`

	SystemPAT struct {
		Token  string   `yaml:"token"`
		Scopes []string `yaml:"scopes"`
	} `yaml:"system_pat"`
}

// ---------------------------------------------------------------------------
// Path resolution + environment rejection
// ---------------------------------------------------------------------------

// DefaultPath returns the canonical config file location,
// ~/.qatlas/config.yaml. Returns "" when the user home cannot be
// determined (caller should surface that as an error).
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".qatlas", "config.yaml")
}

// legacyEnvNames are unprefixed env vars from the .env era that
// operators are likely to still have exported. They are rejected with
// the same "move to config.yaml" error as QATLAS_* vars.
var legacyEnvNames = []string{
	"GITHUB_CLIENT_ID",
	"GITHUB_CLIENT_SECRET",
	"RAW_DIR",
	"DATA_DIR",
	"PB_DATA_DIR",
	"SERVER_HOST",
	"SERVER_PORT",
	"USER_HEADER",
	"PUBLIC_BASE_URL",
}

// legacyEnvPrefixes are whole prefix families that are always rejected.
var legacyEnvPrefixes = []string{
	"MINERU_",
	"NEO4J_",
	"POSTGRES_",
}

// envAllowlistPrefixes are QATLAS_* prefixes that do NOT configure the
// server and are therefore tolerated: test-only DSN / S3 fixtures used
// by the integration test suites.
var envAllowlistPrefixes = []string{
	"QATLAS_TEST_",
	"QATLAS_S3_TEST_",
}

// envAllowlistNames are exact QATLAS_* names that configure tooling
// AROUND qatlasd (the install script, docker-compose interpolation)
// rather than the server itself. Operators may legitimately have these
// exported; they must not trip the rejection.
var envAllowlistNames = []string{
	"QATLAS_VERSION",     // install script + compose image tag
	"QATLAS_INSTALL_DIR", // install script target dir
	"QATLAS_REPO",        // install script github owner/repo
}

// rejectEnvConfig fails when any configuration-shaped environment
// variable is set. Environment-variable configuration was removed in
// favour of the YAML file; silently ignoring stray QATLAS_* vars would
// let operators believe a setting took effect when it didn't.
func rejectEnvConfig() error {
	var offenders []string
	for _, raw := range os.Environ() {
		eq := strings.IndexByte(raw, '=')
		if eq <= 0 || raw[eq+1:] == "" {
			continue // unset or empty — harmless
		}
		name := raw[:eq]
		if isRejectedEnvName(name) {
			offenders = append(offenders, name)
		}
	}
	if len(offenders) == 0 {
		return nil
	}
	sort.Strings(offenders)
	return fmt.Errorf(
		"environment-variable configuration is no longer supported — qatlasd reads %s only.\n"+
			"Offending variables currently set: %s\n"+
			"Move these into the YAML config file (run `qatlasd config init` to create one, "+
			"see config.example.yaml for the full key reference), then unset them",
		defaultPathForMessage(), strings.Join(offenders, ", "),
	)
}

func defaultPathForMessage() string {
	if p := DefaultPath(); p != "" {
		return p
	}
	return "~/.qatlas/config.yaml"
}

func isRejectedEnvName(name string) bool {
	for _, allow := range envAllowlistPrefixes {
		if strings.HasPrefix(name, allow) {
			return false
		}
	}
	for _, allow := range envAllowlistNames {
		if name == allow {
			return false
		}
	}
	if strings.HasPrefix(name, "QATLAS_") {
		return true
	}
	for _, legacy := range legacyEnvNames {
		if name == legacy {
			return true
		}
	}
	for _, prefix := range legacyEnvPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Load
// ---------------------------------------------------------------------------

// Load reads the YAML config file at path and resolves the runtime
// configuration. An empty path falls back to DefaultPath()
// (~/.qatlas/config.yaml).
//
// Failure modes (all hard errors):
//   - any QATLAS_* / legacy configuration env var is set in the process
//     environment (env-based configuration was removed; see
//     rejectEnvConfig);
//   - the file does not exist (the error hints at `qatlasd config init`);
//   - the YAML is malformed or contains unknown keys (strict decode —
//     a typo'd key must not silently fall back to the default);
//   - a duration / numeric field fails to parse, but only for sections
//     that are actually active (MinerU fields are only parsed when
//     paper_access.enabled is true, matching the historic contract).
//
// Defaults (applied when a key is absent) match the historic env-based
// defaults: XDG data dirs, 127.0.0.1:4200 bind, max_conns 10,
// corpus_ensure_indexes true, providers [catalog arxiv openalex],
// arxiv fetch 2 concurrent / 0.33 rps,
// plugin rpc bind 127.0.0.1:8799, event retention 7d, rpc timeout 30s,
// reconnect 5s.
func Load(path string) (*Config, error) {
	if err := rejectEnvConfig(); err != nil {
		return nil, err
	}

	if path == "" {
		path = DefaultPath()
		if path == "" {
			return nil, errors.New("cannot determine user home directory; pass --config /path/to/config.yaml")
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf(
				"config file not found: %s\n\nCreate one with `qatlasd config init`, "+
					"or point at an existing file with --config /path/to/config.yaml", path)
		}
		return nil, fmt.Errorf("read config file %s: %w", path, err)
	}

	var fc fileConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil {
		// io.EOF = empty document (e.g. the all-comments template that
		// `qatlasd config init` writes) — treat as an empty config,
		// every default applies.
		if !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("parse config file %s: %w", path, err)
		}
	}

	anchor := filepath.Dir(path)
	cfg, err := fc.toConfig(anchor)
	if err != nil {
		return nil, fmt.Errorf("config file %s: %w", path, err)
	}

	// Half-set S3: NOT a hard fail at Load time. Non-serve subcommands
	// (`qatlasd --help`, `qatlasd pat list`, etc.) should tolerate a
	// partially-configured file; the strict check is enforced by
	// ValidateForServe(), which `serve` calls. We still emit a WARN
	// here so the misconfig is visible in every log.
	if err := validatePartialS3Config(cfg); err != nil {
		slog.Warn(
			"object storage config is incomplete; `qatlasd serve` and S3-backed subcommands will refuse to run until this is fixed",
			"error", err,
		)
	}

	return cfg, nil
}

// toConfig maps the decoded YAML onto a Config, applying defaults and
// resolving filesystem paths against anchor (the config file's
// directory).
func (fc *fileConfig) toConfig(anchor string) (*Config, error) {
	cfg := &Config{
		HTTPAddr:       fc.HTTPAddr,
		PublicURL:      fc.PublicURL,
		UserHeader:     fc.UserHeader,
		EdgeName:       fc.EdgeName,
		ForceTCP4:      fc.ForceTCP4,
		SkipPBDataLock: fc.SkipPBDataLock,

		PostgresDSN:              fc.Postgres.DSN,
		PostgresMaxConns:         intOrDefault(fc.Postgres.MaxConns, 10),
		CorpusEnsureIndexes:      boolOrDefault(fc.Postgres.CorpusEnsureIndexes, true),
		SearchProviders:          fc.Search.Providers,
		GitHubClientID:           fc.Auth.GitHubClientID,
		GitHubClientSecret:       fc.Auth.GitHubClientSecret,
		AllowedGitHubLogins:      fc.Auth.AllowedLogins,
		AdminGitHubLogins:        fc.Auth.AdminLogins,
		SuperadminGitHubLogins:   fc.Auth.SuperadminLogins,
		GiteaURL:                 strings.TrimSuffix(strings.TrimSpace(fc.Auth.GiteaURL), "/"),
		GiteaClientID:            fc.Auth.GiteaClientID,
		GiteaClientSecret:        fc.Auth.GiteaClientSecret,
		AdminGiteaLogins:         fc.Auth.GiteaAdminLogins,
		SuperadminGiteaLogins:    fc.Auth.GiteaSuperadminLogins,
		S3Endpoint:               fc.S3.Endpoint,
		S3PublicEndpoint:         fc.S3.PublicEndpoint,
		S3BucketPDF:              fc.S3.BucketPDF,
		S3BucketMD:               fc.S3.BucketMD,
		S3BucketImages:           fc.S3.BucketImages,
		S3BucketOpenAlex:         fc.S3.BucketOpenAlex,
		S3AccessKeyID:            fc.S3.AccessKeyID,
		S3SecretAccessKey:        fc.S3.SecretAccessKey,
		PaperAccessEnabled:       fc.PaperAccess.Enabled,
		OpenAlexMailto:           fc.PaperAccess.OpenAlexMailto,
		ArxivFetchConcurrent:     intOrDefault(fc.PaperAccess.ArxivFetchConcurrent, 2),
		ArxivFetchRPS:            floatOrDefault(fc.PaperAccess.ArxivFetchRPS, 0.33),
		DownloaderEnabled:        boolOrDefault(fc.Downloader.Enabled, true),
		DownloaderConcurrency:    intOrDefault(fc.Downloader.Concurrency, 2),
		DownloaderUnpaywallEmail: fc.Downloader.UnpaywallEmail,
		DownloaderS2APIKey:       fc.Downloader.S2APIKey,
		// Default off: on-demand entitled fetches, not crawling
		// (several publishers blanket-disallow "*" as an anti-AI-crawler
		// measure, which would silently break legitimate downloads).
		DownloaderRespectRobots:     boolOrDefault(fc.Downloader.RespectRobots, false),
		DownloaderAgentBackend:      strings.TrimSpace(fc.Downloader.Agent.Backend),
		DownloaderAgentBaseURL:      strings.TrimRight(strings.TrimSpace(fc.Downloader.Agent.BaseURL), "/"),
		DownloaderAgentAPIKey:       fc.Downloader.Agent.APIKey,
		DownloaderAgentModel:        strings.TrimSpace(fc.Downloader.Agent.Model),
		DownloaderAgentMaxTokens:    intOrDefault(fc.Downloader.Agent.MaxTokens, 1024),
		DownloaderAgentClaudeBin:    defaultIfEmpty(strings.TrimSpace(fc.Downloader.Agent.ClaudeBin), "claude"),
		DownloaderAgentClaudeModel:  strings.TrimSpace(fc.Downloader.Agent.ClaudeModel),
		DownloaderAgentMaxBudgetUSD: fc.Downloader.Agent.MaxBudgetUSD,
		DownloaderBrowserCDPURL:     strings.TrimSpace(fc.Downloader.Browser.CDPURL),
		DownloaderProxyURL:          strings.TrimRight(strings.TrimSpace(fc.Downloader.Proxy.URL), "/"),
		DownloaderProxyToken:        fc.Downloader.Proxy.Token,
		RAGRemoteEnabled:            fc.RAG.Remote.Enabled,
		RAGRemoteURL:                fc.RAG.Remote.URL,
		RAGRemoteToken:              fc.RAG.Remote.Token,
		PluginsEnabled:              fc.Plugins.Enabled,
		PluginsDisabled:             fc.Plugins.Disabled,
		PluginConnectSecret:         fc.Plugins.ConnectSecret,
		RPCWSBind:                   defaultIfEmpty(fc.Plugins.RPCWSBind, "127.0.0.1:8799"),
		SystemPATToken:              fc.SystemPAT.Token,
		SystemPATScopes:             fc.SystemPAT.Scopes,
	}
	if len(cfg.SearchProviders) == 0 {
		cfg.SearchProviders = []string{"catalog", "arxiv", "openalex"}
	}
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = "127.0.0.1:4200"
	}

	cfg.RemoteEnabled = fc.Search.Remote.Enabled
	cfg.RemoteURL = fc.Search.Remote.URL
	cfg.RemoteToken = fc.Search.Remote.Token
	cfg.AgenticDailyLimit = intOrDefault(fc.Search.Agentic.DailyLimit, 10000)
	cfg.AgenticPricePerMtok = floatOrDefault(fc.Search.Agentic.PricePerMtok, 0.0)

	cfg.AgenticBackend = defaultIfEmpty(strings.TrimSpace(fc.Search.Agentic.Backend), "remote")
	if cfg.AgenticBackend != "remote" && cfg.AgenticBackend != "local" {
		return nil, fmt.Errorf("search.agentic.backend must be \"remote\" or \"local\", got %q", cfg.AgenticBackend)
	}
	cfg.AgenticLocalClaudeBin = defaultIfEmpty(strings.TrimSpace(fc.Search.Agentic.Local.ClaudeBin), "claude")
	cfg.AgenticLocalModel = strings.TrimSpace(fc.Search.Agentic.Local.Model)
	cfg.AgenticLocalPromptTpl = fc.Search.Agentic.Local.PromptTemplate
	cfg.AgenticLocalMaxBudgetUSD = floatOrDefault(fc.Search.Agentic.Local.MaxBudgetUSD, 0.0)
	if cfg.AgenticLocalMaxBudgetUSD < 0 {
		return nil, fmt.Errorf("search.agentic.local.max_budget_usd must be >= 0, got %v", cfg.AgenticLocalMaxBudgetUSD)
	}

	var err error
	if cfg.RemoteTimeout, err = parseDuration(fc.Search.Remote.Timeout, 60*time.Second, "search.remote.timeout"); err != nil {
		return nil, err
	}
	if cfg.DownloaderAgentTimeout, err = parseDuration(fc.Downloader.Agent.Timeout, 120*time.Second, "downloader.agent.timeout"); err != nil {
		return nil, err
	}
	if cfg.DownloaderBrowserTimeout, err = parseDuration(fc.Downloader.Browser.Timeout, 45*time.Second, "downloader.browser.timeout"); err != nil {
		return nil, err
	}
	if cfg.DownloaderProxyTimeout, err = parseDuration(fc.Downloader.Proxy.Timeout, 5*time.Minute, "downloader.proxy.timeout"); err != nil {
		return nil, err
	}
	switch cfg.DownloaderAgentBackend {
	case "", "off", "none", "openai", "claude":
	default:
		return nil, fmt.Errorf("downloader.agent.backend must be \"\", \"openai\" or \"claude\", got %q", cfg.DownloaderAgentBackend)
	}
	if cfg.RAGRemoteTimeout, err = parseDuration(fc.RAG.Remote.Timeout, 30*time.Second, "rag.remote.timeout"); err != nil {
		return nil, err
	}
	if cfg.AgenticLocalTimeout, err = parseDuration(fc.Search.Agentic.Local.Timeout, 5*time.Minute, "search.agentic.local.timeout"); err != nil {
		return nil, err
	}
	if cfg.AgenticLocalRetention, err = parseDuration(fc.Search.Agentic.Local.Retention, 24*time.Hour, "search.agentic.local.retention"); err != nil {
		return nil, err
	}
	if cfg.EventRetention, err = parseDuration(fc.Plugins.EventRetention, 7*24*time.Hour, "plugins.event_retention"); err != nil {
		return nil, err
	}
	if cfg.PluginRPCTimeout, err = parseDuration(fc.Plugins.RPCTimeout, 30*time.Second, "plugins.rpc_timeout"); err != nil {
		return nil, err
	}
	if cfg.PluginReconnectInterval, err = parseDuration(fc.Plugins.ReconnectInterval, 5*time.Second, "plugins.reconnect_interval"); err != nil {
		return nil, err
	}

	// MinerU* fields are only populated when the master switch is on.
	// When the switch is off we intentionally skip even malformed
	// MinerU values so a stale section can't make a non-MinerU
	// deployment fail to start.
	if cfg.PaperAccessEnabled {
		if err := fc.loadMinerUConfig(cfg); err != nil {
			return nil, err
		}
	}

	// Normalize filesystem paths: resolve ~ and relative-to-anchor.
	// Apply XDG defaults when a key is absent so stateful directories
	// never accidentally land inside the git checkout.
	cfg.RawDir = expandPath(defaultIfEmpty(fc.Paths.RawDir, defaultXDGSubdir("raw")), anchor)
	cfg.DataDir = expandPath(defaultIfEmpty(fc.Paths.DataDir, defaultXDGSubdir("data")), anchor)
	cfg.PBDataDir = expandPath(defaultIfEmpty(fc.Paths.PBDataDir, defaultXDGSubdir("pb_data")), anchor)
	cfg.PluginsDir = expandPath(defaultIfEmpty(fc.Plugins.Dir, defaultXDGConfigSubdir("plugins")), anchor)
	cfg.DeadLetterDir = expandPath(defaultIfEmpty(fc.Plugins.DeadLetterDir, defaultXDGStateSubdir("dead")), anchor)

	// The local agentic backend's sandbox root defaults to a subdirectory
	// of data_dir; an explicit path resolves like every other path key.
	cfg.AgenticLocalSandboxDir = expandPath(defaultIfEmpty(fc.Search.Agentic.Local.SandboxDir,
		filepath.Join(cfg.DataDir, "agentic")), anchor)

	return cfg, nil
}

// loadMinerUConfig populates the MinerU* fields when the paper-access
// master switch is on. Strict-parse: malformed durations cause Load()
// to fail rather than silently fall back to defaults — when the switch
// is off this function is never called.
//
// Defaults match issue #8: vlm model, ch language, table+formula on,
// OCR off, 3s poll, 1800s timeout, concurrency=4. An empty api_tokens
// list is allowed — that enables "cache-only" mode where /markdown
// returns 503 on cache miss.
func (fc *fileConfig) loadMinerUConfig(cfg *Config) error {
	m := fc.PaperAccess.MinerU
	cfg.MinerUAPITokens = m.APITokens
	cfg.MinerUAPIBaseURL = defaultIfEmpty(m.APIBaseURL, "https://mineru.net")
	cfg.MinerUModelVersion = defaultIfEmpty(m.ModelVersion, "vlm")
	cfg.MinerULanguage = defaultIfEmpty(m.Language, "ch")
	cfg.MinerUIsOCR = boolOrDefault(m.IsOCR, false)
	cfg.MinerUEnableFormula = boolOrDefault(m.EnableFormula, true)
	cfg.MinerUEnableTable = boolOrDefault(m.EnableTable, true)

	var err error
	if cfg.MinerUPollInterval, err = parseDuration(m.PollInterval, 3*time.Second, "paper_access.mineru.poll_interval"); err != nil {
		return err
	}
	if cfg.MinerUTimeout, err = parseDuration(m.Timeout, 1800*time.Second, "paper_access.mineru.timeout"); err != nil {
		return err
	}

	maxConc := intOrDefault(m.MaxConcurrentJobs, 4)
	if maxConc < 1 {
		return fmt.Errorf("paper_access.mineru.max_concurrent_jobs must be ≥ 1, got %d", maxConc)
	}
	cfg.MinerUMaxConcurrentJobs = maxConc

	if _, err := url.Parse(cfg.MinerUAPIBaseURL); err != nil {
		return fmt.Errorf("paper_access.mineru.api_base_url is not a valid URL: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// S3Enabled reports whether the object-storage backend is configured.
// When false, callers should fall back to local-filesystem I/O under
// cfg.RawDir; when true, all S3 connection fields plus the three
// per-kind buckets are guaranteed non-empty by ValidateForServe.
func (c *Config) S3Enabled() bool {
	return c.S3Endpoint != "" && c.S3BucketPDF != "" && c.S3BucketMD != "" &&
		c.S3BucketImages != "" && c.S3AccessKeyID != "" && c.S3SecretAccessKey != ""
}

// ValidateForServe enforces the serve-time S3 invariant:
// validatePartialS3Config — the all-or-nothing rule for the connection
// quartet plus the three per-kind buckets.
//
// `qatlasd serve` MUST call this before wiring the object store,
// otherwise a half-configured server would silently fall back to the
// local RawDir and quietly corrupt the writer/reader symmetry across
// restarts. Non-serve subcommands (`qatlasd --help`, `qatlasd pat
// list`, etc.) deliberately skip this strict check.
func (c *Config) ValidateForServe() error {
	return validatePartialS3Config(c)
}

// validatePartialS3Config returns an error iff the S3 connection
// fields + 3 per-kind buckets are HALF-set (not none, not all). The
// check is symmetric: no single field alone is valid; mixing some-set
// some-unset is rejected. Returns nil for "all empty" (local-only mode)
// and "all set" (S3 enabled).
func validatePartialS3Config(cfg *Config) error {
	fields := map[string]string{
		"s3.endpoint":          cfg.S3Endpoint,
		"s3.bucket_pdf":        cfg.S3BucketPDF,
		"s3.bucket_md":         cfg.S3BucketMD,
		"s3.bucket_images":     cfg.S3BucketImages,
		"s3.access_key_id":     cfg.S3AccessKeyID,
		"s3.secret_access_key": cfg.S3SecretAccessKey,
	}
	var set, unset []string
	for name, v := range fields {
		if v == "" {
			unset = append(unset, name)
		} else {
			set = append(set, name)
		}
	}
	if len(set) == 0 || len(unset) == 0 {
		return nil
	}
	// Stable order for the error message so tests are deterministic
	// and the operator can grep their config.yaml without surprises.
	sort.Strings(set)
	sort.Strings(unset)
	return fmt.Errorf(
		"object storage half-configured: %v are set but %v are missing — "+
			"set all s3 connection fields + the three s3.bucket_{pdf,md,images} "+
			"keys to enable RustFS/S3, or remove them all to use local RawDir",
		set, unset,
	)
}

// ---------------------------------------------------------------------------
// Scalar helpers
// ---------------------------------------------------------------------------

func defaultIfEmpty(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func intOrDefault(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

func floatOrDefault(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}

func boolOrDefault(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// parseDuration parses a Go duration string, plus a small "Nd" days
// extension because event retention defaults are documented in days.
// Empty input falls back to def.
func parseDuration(raw string, def time.Duration, key string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, nil
	}
	if strings.HasSuffix(raw, "d") {
		daysRaw := strings.TrimSuffix(raw, "d")
		days, err := strconv.ParseFloat(daysRaw, 64)
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("%s must be a positive duration, got %q", key, raw)
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration like 168h or 7d, got %q: %w", key, raw, err)
	}
	if v <= 0 {
		return 0, fmt.Errorf("%s must be > 0, got %s", key, raw)
	}
	return v, nil
}

// ---------------------------------------------------------------------------
// Path helpers
// ---------------------------------------------------------------------------

// expandPath resolves ~ and converts relative paths to absolute.
//
// If anchor is non-empty, relative paths resolve against it (typically
// the config file's directory). Otherwise they fall back to the process
// CWD. Empty input returns empty output.
func expandPath(p, anchor string) string {
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	if !filepath.IsAbs(p) {
		if anchor != "" {
			p = filepath.Join(anchor, p)
		} else if cwd, err := os.Getwd(); err == nil {
			p = filepath.Join(cwd, p)
		}
	}
	return filepath.Clean(p)
}

func defaultXDGConfigSubdir(name string) string {
	if base, err := os.UserConfigDir(); err == nil && base != "" && filepath.IsAbs(base) {
		return filepath.Join(base, "qatlasd", name)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "qatlasd", name)
	}
	return filepath.Join(".qatlasd-config-" + name)
}

func defaultXDGStateSubdir(name string) string {
	base := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if base == "" || !filepath.IsAbs(base) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			base = filepath.Join(home, ".local", "state")
		} else {
			return filepath.Join(".qatlasd-state-" + name)
		}
	}
	return filepath.Join(base, "qatlasd", name)
}

// defaultXDGSubdir returns the XDG_DATA_HOME-rooted default location
// for the named qatlasd subdirectory (raw / data / pb_data).
//
// Lookup order:
//  1. $XDG_DATA_HOME, when set and absolute (per XDG spec — relative
//     values are explicitly invalid).
//  2. $HOME/.local/share, the spec's documented fallback.
//  3. ./.qatlasd-<name>, a last-resort relative path when even
//     $HOME is missing (e.g. minimal container).
//
// **App name = "qatlasd"** (matches the binary name).
func defaultXDGSubdir(name string) string {
	base := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if base == "" || !filepath.IsAbs(base) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			base = filepath.Join(home, ".local", "share")
		} else {
			return filepath.Join(".qatlasd-" + name) // tiny last-resort
		}
	}
	return filepath.Join(base, "qatlasd", name)
}

// ---------------------------------------------------------------------------
// Provider login allowlists
// ---------------------------------------------------------------------------

// loginListed reports whether the given (already lowercased + trimmed)
// login appears in the list, comparing case-insensitively. Shared by the
// GitHub and Gitea allowlist methods — both providers treat logins as
// case-insensitive identifiers.
func loginListed(list []string, login string) bool {
	for _, l := range list {
		if strings.ToLower(strings.TrimSpace(l)) == login {
			return true
		}
	}
	return false
}

// IsGitHubLoginAllowed reports whether the given GitHub login (username)
// is permitted to complete OAuth sign-in.
//
// Policy is fail-closed: a login is allowed iff it appears in either
// AllowedGitHubLogins or AdminGitHubLogins. When BOTH lists are empty the
// allowlist is unconfigured and this returns false for EVERYONE — a
// deliberate locked-by-default posture. The PocketBase superuser (the
// _superusers collection, email+password at /_/) is never gated by this
// and remains the recovery path to fix a misconfigured allowlist.
//
// Comparison is case-insensitive because GitHub logins are.
func (c *Config) IsGitHubLoginAllowed(login string) bool {
	login = strings.ToLower(strings.TrimSpace(login))
	if login == "" {
		return false
	}
	return loginListed(c.AllowedGitHubLogins, login) || loginListed(c.AdminGitHubLogins, login)
}

// IsGitHubAdmin reports whether the given GitHub login belongs to the
// admin allowlist (auth.admin_logins). Admins are a STRICT subset of
// allowed sign-ins: AllowedGitHubLogins alone does not grant admin.
// Fail-closed like IsGitHubLoginAllowed.
//
// Comparison is case-insensitive because GitHub logins are.
func (c *Config) IsGitHubAdmin(login string) bool {
	login = strings.ToLower(strings.TrimSpace(login))
	if login == "" {
		return false
	}
	return loginListed(c.AdminGitHubLogins, login)
}

// IsGitHubSuperadmin reports whether the given GitHub login belongs to
// the superadmin seed list (auth.superadmin_logins). Like IsGitHubAdmin
// it is a strict subset of allowed sign-ins and compares
// case-insensitively. Used only for the bootstrap flag promotion in
// internal/auth — runtime superadmin checks read the is_superadmin
// flag off the users record instead.
func (c *Config) IsGitHubSuperadmin(login string) bool {
	login = strings.ToLower(strings.TrimSpace(login))
	if login == "" {
		return false
	}
	return loginListed(c.SuperadminGitHubLogins, login)
}

// IsGiteaAdmin reports whether the given Gitea login belongs to the
// Gitea admin allowlist (auth.gitea_admin_logins). Fail-closed and
// case-insensitive like IsGitHubAdmin; grants on the /api/admin surface
// alongside the GitHub admin allowlist.
func (c *Config) IsGiteaAdmin(login string) bool {
	login = strings.ToLower(strings.TrimSpace(login))
	if login == "" {
		return false
	}
	return loginListed(c.AdminGiteaLogins, login)
}

// IsGiteaSuperadmin reports whether the given Gitea login belongs to the
// Gitea superadmin seed list (auth.gitea_superadmin_logins). Used only
// for the bootstrap flag promotion, like IsGitHubSuperadmin.
func (c *Config) IsGiteaSuperadmin(login string) bool {
	login = strings.ToLower(strings.TrimSpace(login))
	if login == "" {
		return false
	}
	return loginListed(c.SuperadminGiteaLogins, login)
}
