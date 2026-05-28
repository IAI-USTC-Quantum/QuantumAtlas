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
	DataDir   string // server-managed metadata (shares.json, ingests/, etc.).
	PBDataDir string // PocketBase pb_data (SQLite + uploads); passed to --dir=.

	// Neo4j (server-only).
	Neo4jURI      string
	Neo4jUser     string
	Neo4jPassword string
	Neo4jDatabase string

	// Share URL signing / public visibility.
	PublicBaseURL          string
	ShareAccessToken       string
	DefaultShareExpiresIn  int // seconds; 0 means no default expiry.

	// Audit header injected by the upstream reverse proxy.
	UserHeader string

	// MinerU PDF parser (third-party SDK; not QATLAS_*).
	MinerUAPIToken   string
	MinerUAPIBaseURL string

	// GitHub OAuth (for PocketBase auth_collection_oauth2 settings).
	GitHubClientID     string
	GitHubClientSecret string

	// GitHub login whitelist auto-promoted to admin on first OAuth login.
	AdminGitHubLogins []string

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
	// Per-edge example (production):
	//   RackNerd .env: S3Endpoint=http://10.144.18.10:9000
	//                  S3PublicEndpoint=https://raw.quantum-atlas.ai
	//   Alibaba  .env: S3Endpoint=http://10.144.18.10:9000
	//                  S3PublicEndpoint=https://47.102.36.175:9000
	//
	// When empty (or equal to S3Endpoint), presigned URLs reuse the
	// internal endpoint — handy for single-network dev setups but
	// useless for any deployment where clients can't reach the
	// internal host.
	S3Endpoint        string
	S3PublicEndpoint  string
	S3Bucket          string
	S3AccessKeyID     string
	S3SecretAccessKey string
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
		HTTPAddr:              firstEnv("QATLAS_HTTP_ADDR"),
		WikiDir:               firstEnv("QATLAS_WIKI_DIR", "WIKI_DIR"),
		RawDir:                firstEnv("QATLAS_RAW_DIR", "RAW_DIR"),
		DataDir:               firstEnv("QATLAS_DATA_DIR", "DATA_DIR"),
		PBDataDir:             firstEnv("QATLAS_PB_DATA_DIR", "PB_DATA_DIR"),
		Neo4jURI:              firstEnv("NEO4J_URI"),
		Neo4jUser:             firstEnv("NEO4J_USERNAME", "NEO4J_USER"),
		Neo4jPassword:         firstEnv("NEO4J_PASSWORD"),
		Neo4jDatabase:         firstEnv("NEO4J_DATABASE"),
		PublicBaseURL:         firstEnv("QATLAS_SERVER_URL", "PUBLIC_BASE_URL"),
		ShareAccessToken:      firstEnv("QATLAS_SHARE_ACCESS_TOKEN", "SHARE_ACCESS_TOKEN"),
		DefaultShareExpiresIn: firstEnvInt("QATLAS_DEFAULT_SHARE_EXPIRES_IN", "DEFAULT_SHARE_EXPIRES_IN"),
		UserHeader:            firstEnv("QATLAS_USER_HEADER", "USER_HEADER"),
		MinerUAPIToken:        firstEnv("MINERU_API_TOKEN"),
		MinerUAPIBaseURL:      firstEnvDefault("https://mineru.net", "MINERU_API_BASE_URL"),
		GitHubClientID:        firstEnv("GITHUB_CLIENT_ID"),
		GitHubClientSecret:    firstEnv("GITHUB_CLIENT_SECRET"),
		S3Endpoint:            firstEnv("QATLAS_S3_ENDPOINT"),
		S3PublicEndpoint:      firstEnv("QATLAS_S3_PUBLIC_ENDPOINT"),
		S3Bucket:              firstEnv("QATLAS_S3_BUCKET"),
		S3AccessKeyID:         firstEnv("QATLAS_S3_ACCESS_KEY_ID"),
		S3SecretAccessKey:     firstEnv("QATLAS_S3_SECRET_ACCESS_KEY"),
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
// cfg.RawDir; when true, all four QATLAS_S3_* fields are guaranteed
// non-empty by validateS3Config (invoked at end of Load).
func (c *Config) S3Enabled() bool {
	return c.S3Endpoint != "" && c.S3Bucket != "" && c.S3AccessKeyID != "" && c.S3SecretAccessKey != ""
}

// validateS3Config enforces the all-or-nothing rule for the QATLAS_S3_*
// quartet. We deliberately do not validate URL syntax / bucket naming
// here — those errors surface clearly at first request through minio-go,
// and adding our own validator would just be one more thing to keep in
// sync with the SDK.
func validateS3Config(cfg *Config) error {
	fields := map[string]string{
		"QATLAS_S3_ENDPOINT":          cfg.S3Endpoint,
		"QATLAS_S3_BUCKET":            cfg.S3Bucket,
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
			"set all four QATLAS_S3_* fields to enable RustFS/S3, or unset all four to use local RawDir",
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

// firstEnvInt parses the first non-empty env value as an int. Returns 0 on
// parse error (silent — same forgiving behavior as pydantic Optional[int]).
func firstEnvInt(names ...string) int {
	raw := firstEnv(names...)
	if raw == "" {
		return 0
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0
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
			return filepath.Join(".quantum-atlas-"+name) // tiny last-resort
		}
	}
	return filepath.Join(base, "quantum-atlas", name)
}
