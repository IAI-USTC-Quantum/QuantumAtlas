// Package config loads QuantumAtlas server configuration from environment
// variables (typically populated from a .env file by the wrapper script).
//
// The Python server used pydantic-settings with AliasChoices to accept both
// QATLAS_* names and legacy unprefixed/SERVER_* names. We preserve that
// alias behavior here so a single .env can drive both implementations
// during the transition period.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Config holds the resolved runtime configuration for the Go server.
//
// All fields are populated from environment variables. Empty / zero values
// mean "feature disabled" unless otherwise documented.
type Config struct {
	// HTTP bind address (host:port). Defaults to 127.0.0.1:4200.
	HTTPAddr string

	// Filesystem roots. Defaults are computed by Load when the
	// corresponding env vars are unset:
	//   - WikiDir   -> <anchor>/../QuantumAtlas-Wiki (sibling checkout)
	//   - RawDir    -> ${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/raw
	//   - DataDir   -> ${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/data
	//   - PBDataDir -> ${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/pb_data
	// "anchor" is the directory containing the .env loaded by the caller,
	// or the process CWD when no .env was supplied.
	WikiDir   string // local clone of the Wiki repo (markdown + frontmatter).
	RawDir    string // RAW asset store (PDFs, MinerU outputs, etc.).
	DataDir   string // server-managed metadata (ingests/, mineru-claim sidecars, etc.).
	PBDataDir string // PocketBase pb_data (SQLite + uploads); passed to --dir=.

	// Neo4j (server-only).
	Neo4jURI      string
	Neo4jUser     string
	Neo4jPassword string
	Neo4jDatabase string

	// Public base URL: server's own canonical https origin. Required
	// for OAuth callback construction and OpenAlex sync links.
	PublicBaseURL string

	// Audit header injected by the upstream reverse proxy.
	UserHeader string

	// GitHub OAuth (for PocketBase auth_collection_oauth2 settings).
	GitHubClientID     string
	GitHubClientSecret string

	// GitHub login whitelist auto-promoted to admin on first OAuth login.
	AdminGitHubLogins []string

	// GitHub login allowlist gating OAuth sign-in. Only accounts whose
	// GitHub login appears here (or in AdminGitHubLogins) may obtain an
	// authenticated session. Parsed from QATLAS_ALLOWED_GITHUB_LOGINS.
	// Fail-closed: when this AND AdminGitHubLogins are both empty, NOBODY
	// may sign in (see IsGitHubLoginAllowed). The PocketBase superuser
	// (email+password at /_/) is unaffected and is the recovery path.
	AllowedGitHubLogins []string

	// Object storage (RustFS / S3-compatible) for the RAW asset bucket.
	// When S3Endpoint is empty the server falls back to RawDir on the
	// local filesystem. When set, all four required fields must be
	// non-empty — see invariant check at the end of Load().
	//
	// Endpoint must include scheme (https://raw.example.tld) so the
	// minio-go client can decide TLS vs plaintext deterministically.
	// We don't use AWS path-style heuristics; for vendor flexibility
	// the bucket is always supplied as a separate parameter.
	//
	// S3PublicEndpoint (optional) splits the network roles:
	//   - S3Endpoint        is used for server↔RustFS traffic (mesh,
	//                       intranet, anything cheap & fast).
	//   - S3PublicEndpoint  is used ONLY when minting presigned URLs
	//                       for end users. The URL host the browser
	//                       hits = this value. Must front the same
	//                       bucket + credentials as S3Endpoint.
	//
	// Per-edge example (illustrative — concrete hosts depend on your
	// deployment topology):
	//   edge-A .env: S3Endpoint=http://<mesh-host>:9000
	//                S3PublicEndpoint=https://<edge-a-public-host>
	//   edge-B .env: S3Endpoint=http://<mesh-host>:9000
	//                S3PublicEndpoint=https://<edge-b-public-host>
	//
	// When empty (or equal to S3Endpoint), presigned URLs reuse the
	// internal endpoint — handy for single-network dev setups but
	// useless for any deployment where clients can't reach the
	// internal host.
	S3Endpoint       string
	S3PublicEndpoint string

	// Per-kind buckets (v0.7.0). The single qatlas-raw bucket was split
	// into three so each asset kind has its own lifecycle / quota /
	// access policy. S3BucketOpenAlex is reserved for the OpenAlex
	// snapshot ingest (档 B) and is optional — the server runs without
	// it; only `openalex` subcommands need it.
	//
	// All three of PDF/MD/Images are required together when S3 is
	// enabled (validateS3Config). The legacy single QATLAS_S3_BUCKET is
	// REMOVED in v0.7.0 — Load fails fast if it's still set so a stale
	// .env can't silently mis-route every object into one bucket.
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
	// Never load-bearing for auth (UA is forgeable; the load-bearing
	// forensic key is the SigV4 accessKey recorded by the audit trail
	// — T10).
	//
	// The audit *sink* itself is NOT part of this binary: a generic,
	// convention-free log shipper (Fluent Bit) deployed as a sidecar
	// next to RustFS on the NAS receives the RustFS global audit webhook
	// and writes one object per event into the qatlas-audit bucket using
	// its own dedicated svcacct. Keeping the dumb storage layer free of
	// our evolving backend conventions is the whole point — so none of
	// the sink's wiring (bucket name, sink keys, listen addr, webhook
	// token) lives in this config. See docs/deployment/rustfs.md.
	EdgeName string
}

// Load resolves the configuration from process environment.
//
// dotenvPath, if non-empty, is the absolute path to the .env file from
// which env vars were loaded by the caller. We use its parent directory
// as the anchor for resolving relative filesystem paths (WikiDir /
// RawDir / DataDir / PBDataDir). This way a .env entry like
// `WIKI_DIR=../QuantumAtlas-Wiki` resolves consistently regardless of
// the systemd WorkingDirectory or shell CWD. If dotenvPath is empty
// (e.g. when env is provided entirely by systemd / shell), we fall back
// to the process CWD as the anchor — preserving the previous behavior.
//
// Lookup order for each logical field follows the QATLAS_* alias chain
// documented in .env.example. The first non-empty match wins.
//
// Filesystem defaults (applied when both alias names are unset):
//   - WikiDir   -> "<anchor>/../QuantumAtlas-Wiki"
//   - RawDir    -> "${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/raw"
//   - DataDir   -> "${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/data"
//   - PBDataDir -> "${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/pb_data"
//
// These defaults intentionally land *outside* the git checkout so a
// fresh `git clone` stays clean; see docs/migration-storage-layout.md
// for how to move existing in-repo data.
func Load(dotenvPath string) (*Config, error) {
	anchor := ""
	if dotenvPath != "" {
		anchor = filepath.Dir(dotenvPath)
	}

	cfg := &Config{
		HTTPAddr:                 firstEnv("QATLAS_HTTP_ADDR"),
		WikiDir:                  firstEnv("QATLAS_WIKI_DIR", "WIKI_DIR"),
		RawDir:                   firstEnv("QATLAS_RAW_DIR", "RAW_DIR"),
		DataDir:                  firstEnv("QATLAS_DATA_DIR", "DATA_DIR"),
		PBDataDir:                firstEnv("QATLAS_PB_DATA_DIR", "PB_DATA_DIR"),
		Neo4jURI:                 firstEnv("NEO4J_URI"),
		Neo4jUser:                firstEnv("NEO4J_USERNAME", "NEO4J_USER"),
		Neo4jPassword:            firstEnv("NEO4J_PASSWORD"),
		Neo4jDatabase:            firstEnv("NEO4J_DATABASE"),
		PublicBaseURL:            firstEnv("QATLAS_SERVER_URL", "PUBLIC_BASE_URL"),
		UserHeader:               firstEnv("QATLAS_USER_HEADER", "USER_HEADER"),
		GitHubClientID:           firstEnv("GITHUB_CLIENT_ID"),
		GitHubClientSecret:       firstEnv("GITHUB_CLIENT_SECRET"),
		S3Endpoint:               firstEnv("QATLAS_S3_ENDPOINT"),
		S3PublicEndpoint:         firstEnv("QATLAS_S3_PUBLIC_ENDPOINT"),
		S3BucketPDF:              firstEnv("QATLAS_S3_BUCKET_PDF"),
		S3BucketMD:               firstEnv("QATLAS_S3_BUCKET_MD"),
		S3BucketImages:           firstEnv("QATLAS_S3_BUCKET_IMAGES"),
		S3BucketOpenAlex:         firstEnv("QATLAS_S3_BUCKET_OPENALEX_SNAPSHOT"),
		S3AccessKeyID:            firstEnv("QATLAS_S3_ACCESS_KEY_ID"),
		S3SecretAccessKey:        firstEnv("QATLAS_S3_SECRET_ACCESS_KEY"),
		EdgeName:                 firstEnv("QATLAS_EDGE_NAME"),
	}

	// HTTP bind: assemble from QATLAS_SERVER_HOST + _PORT if QATLAS_HTTP_ADDR
	// is unset. This matches the FastAPI default (127.0.0.1:4200).
	if cfg.HTTPAddr == "" {
		host := firstEnvDefault("127.0.0.1", "QATLAS_SERVER_HOST", "SERVER_HOST")
		port := firstEnvDefault("4200", "QATLAS_SERVER_PORT", "SERVER_PORT")
		cfg.HTTPAddr = fmt.Sprintf("%s:%s", host, port)
	}

	// QATLAS_ADMIN_GITHUB_LOGINS is a comma-separated list.
	if raw := firstEnv("QATLAS_ADMIN_GITHUB_LOGINS"); raw != "" {
		for _, login := range strings.Split(raw, ",") {
			login = strings.TrimSpace(login)
			if login != "" {
				cfg.AdminGitHubLogins = append(cfg.AdminGitHubLogins, login)
			}
		}
	}

	// QATLAS_ALLOWED_GITHUB_LOGINS is a comma-separated GitHub login
	// allowlist gating OAuth sign-in. See Config.IsGitHubLoginAllowed.
	if raw := firstEnv("QATLAS_ALLOWED_GITHUB_LOGINS"); raw != "" {
		for _, login := range strings.Split(raw, ",") {
			login = strings.TrimSpace(login)
			if login != "" {
				cfg.AllowedGitHubLogins = append(cfg.AllowedGitHubLogins, login)
			}
		}
	}

	// Normalize filesystem paths: resolve ~ and relative-to-anchor.
	// Apply XDG / sibling-checkout defaults when an env var is unset so
	// stateful directories never accidentally land inside the git
	// checkout (no more `wiki/`, `raw/`, `data/`, `pb_data/` showing up
	// in `git status` after a clean clone).
	cfg.WikiDir = expandPath(defaultIfEmpty(cfg.WikiDir, defaultWikiDir()), anchor)
	cfg.RawDir = expandPath(defaultIfEmpty(cfg.RawDir, defaultXDGSubdir("raw")), anchor)
	cfg.DataDir = expandPath(defaultIfEmpty(cfg.DataDir, defaultXDGSubdir("data")), anchor)
	cfg.PBDataDir = expandPath(defaultIfEmpty(cfg.PBDataDir, defaultXDGSubdir("pb_data")), anchor)

	// S3 invariant: either all four S3 fields are set, or none are.
	// A half-configured client would silently fall back to the local
	// RawDir and quietly corrupt the writer/reader symmetry across
	// restarts; refuse to boot so the operator sees the misconfig
	// immediately instead of after the next upload.
	if err := validateS3Config(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// S3Enabled reports whether the object-storage backend is configured.
// When false, callers should fall back to local-filesystem I/O under
// cfg.RawDir; when true, all S3 connection fields plus the three
// per-kind buckets are guaranteed non-empty by validateS3Config.
func (c *Config) S3Enabled() bool {
	return c.S3Endpoint != "" && c.S3BucketPDF != "" && c.S3BucketMD != "" &&
		c.S3BucketImages != "" && c.S3AccessKeyID != "" && c.S3SecretAccessKey != ""
}

// validateS3Config enforces the all-or-nothing rule for the S3 connection
// quartet plus the three per-kind buckets, and fails fast when the
// removed single-bucket var QATLAS_S3_BUCKET is still set (a stale .env
// would otherwise silently mis-route every object into one bucket).
func validateS3Config(cfg *Config) error {
	// Hard fail: the v0.6.0 single-bucket var is gone. Catch it before
	// the all-or-nothing check so the operator gets a precise message.
	if v := strings.TrimSpace(os.Getenv("QATLAS_S3_BUCKET")); v != "" {
		return fmt.Errorf(
			"QATLAS_S3_BUCKET is no longer supported in v0.7.0 — the single " +
				"qatlas-raw bucket was split into per-kind buckets; set " +
				"QATLAS_S3_BUCKET_PDF / _MD / _IMAGES instead and remove QATLAS_S3_BUCKET")
	}
	fields := map[string]string{
		"QATLAS_S3_ENDPOINT":          cfg.S3Endpoint,
		"QATLAS_S3_BUCKET_PDF":        cfg.S3BucketPDF,
		"QATLAS_S3_BUCKET_MD":         cfg.S3BucketMD,
		"QATLAS_S3_BUCKET_IMAGES":     cfg.S3BucketImages,
		"QATLAS_S3_ACCESS_KEY_ID":     cfg.S3AccessKeyID,
		"QATLAS_S3_SECRET_ACCESS_KEY": cfg.S3SecretAccessKey,
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
	// Stable order for the error message so the test is deterministic
	// and the operator can grep their .env without surprises.
	sort.Strings(set)
	sort.Strings(unset)
	return fmt.Errorf(
		"object storage half-configured: %v are set but %v are missing — "+
			"set all S3 connection fields + the three QATLAS_S3_BUCKET_{PDF,MD,IMAGES} "+
			"to enable RustFS/S3, or unset all to use local RawDir",
		set, unset,
	)
}

// firstEnv returns the first non-empty environment variable from the given
// names. Returns "" if none are set.
func firstEnv(names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

// firstEnvDefault is firstEnv with a fallback default value.
func firstEnvDefault(def string, names ...string) string {
	if v := firstEnv(names...); v != "" {
		return v
	}
	return def
}

// firstEnvIntDefault parses the first non-empty env value as an int, falling
// back to def when unset or unparseable.
func firstEnvIntDefault(def int, names ...string) int {
	raw := firstEnv(names...)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

// expandPath resolves ~ and converts relative paths to absolute.
//
// If anchor is non-empty, relative paths resolve against it (typically
// the .env file's directory). Otherwise they fall back to the process
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

// defaultIfEmpty returns def when v is empty, otherwise v. Trivial
// helper but it makes the Load() default-application section read
// linearly without nesting ternaries.
func defaultIfEmpty(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// defaultWikiDir returns the conventional wiki location relative to the
// .env anchor: a sibling "../QuantumAtlas-Wiki" checkout. The returned
// path is intentionally relative so that expandPath resolves it against
// the same anchor a user-supplied `WIKI_DIR=../QuantumAtlas-Wiki` would
// use — guaranteeing the auto-default and the explicit override are
// indistinguishable.
func defaultWikiDir() string {
	return filepath.Join("..", "QuantumAtlas-Wiki")
}

// IsGitHubLoginAllowed reports whether the given GitHub login (username)
// is permitted to complete OAuth sign-in.
//
// Policy is fail-closed: a login is allowed iff it appears in either
// AllowedGitHubLogins or AdminGitHubLogins. When BOTH lists are empty the
// allowlist is unconfigured and this returns false for EVERYONE — a
// deliberate locked-by-default posture so an operator who forgets to set
// QATLAS_ALLOWED_GITHUB_LOGINS gets nobody-can-sign-in rather than the
// whole internet. The PocketBase superuser (the _superusers collection,
// email+password at /_/) is never gated by this and remains the recovery
// path to fix a misconfigured allowlist.
//
// Comparison is case-insensitive because GitHub logins are.
func (c *Config) IsGitHubLoginAllowed(login string) bool {
	login = strings.ToLower(strings.TrimSpace(login))
	if login == "" {
		return false
	}
	for _, l := range c.AllowedGitHubLogins {
		if strings.ToLower(strings.TrimSpace(l)) == login {
			return true
		}
	}
	for _, l := range c.AdminGitHubLogins {
		if strings.ToLower(strings.TrimSpace(l)) == login {
			return true
		}
	}
	return false
}

// defaultXDGSubdir returns the XDG_DATA_HOME-rooted default location
// for the named QuantumAtlas subdirectory (raw / data / pb_data).
//
// Lookup order:
//  1. $XDG_DATA_HOME, when set and absolute (per XDG spec — relative
//     values are explicitly invalid).
//  2. $HOME/.local/share, the spec's documented fallback.
//  3. ./.quantum-atlas-<name>, a last-resort relative path when even
//     $HOME is missing (e.g. minimal container). This still beats
//     emitting an absolute root like "/quantum-atlas/raw" that would
//     fail with EACCES on the first write.
//
// All returned values are absolute when paths #1 or #2 apply.
func defaultXDGSubdir(name string) string {
	base := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if base == "" || !filepath.IsAbs(base) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			base = filepath.Join(home, ".local", "share")
		} else {
			return filepath.Join(".quantum-atlas-" + name) // tiny last-resort
		}
	}
	return filepath.Join(base, "quantum-atlas", name)
}
