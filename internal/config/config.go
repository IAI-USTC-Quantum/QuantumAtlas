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

	// Filesystem roots.
	WikiDir string // local clone of the Wiki repo (markdown + frontmatter).
	RawDir  string // RAW asset store (PDFs, MinerU outputs, etc.).
	DataDir string // server-managed metadata (shares.json, ingests/, etc.).

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

	// Release-tag enforcement (production guard).
	RequireReleaseTag bool

	// MinerU PDF parser (third-party SDK; not QATLAS_*).
	MinerUAPIToken   string
	MinerUAPIBaseURL string

	// GitHub OAuth (for PocketBase auth_collection_oauth2 settings).
	GitHubClientID     string
	GitHubClientSecret string

	// GitHub login whitelist auto-promoted to admin on first OAuth login.
	AdminGitHubLogins []string
}

// Load resolves the configuration from process environment.
//
// Lookup order for each logical field follows the QATLAS_* alias chain
// documented in .env.example. The first non-empty match wins.
func Load() (*Config, error) {
	cfg := &Config{
		HTTPAddr:              firstEnv("QATLAS_HTTP_ADDR"),
		WikiDir:               firstEnv("QATLAS_WIKI_DIR", "WIKI_DIR"),
		RawDir:                firstEnv("QATLAS_RAW_DIR", "RAW_DIR"),
		DataDir:               firstEnv("QATLAS_DATA_DIR", "DATA_DIR"),
		Neo4jURI:              firstEnv("NEO4J_URI"),
		Neo4jUser:             firstEnv("NEO4J_USERNAME", "NEO4J_USER"),
		Neo4jPassword:         firstEnv("NEO4J_PASSWORD"),
		Neo4jDatabase:         firstEnv("NEO4J_DATABASE"),
		PublicBaseURL:         firstEnv("QATLAS_SERVER_URL", "PUBLIC_BASE_URL"),
		ShareAccessToken:      firstEnv("QATLAS_SHARE_ACCESS_TOKEN", "SHARE_ACCESS_TOKEN"),
		DefaultShareExpiresIn: firstEnvInt("QATLAS_DEFAULT_SHARE_EXPIRES_IN", "DEFAULT_SHARE_EXPIRES_IN"),
		UserHeader:            firstEnv("QATLAS_USER_HEADER", "USER_HEADER"),
		RequireReleaseTag:     firstEnvBool("QATLAS_REQUIRE_RELEASE_TAG", "QUANTUMATLAS_REQUIRE_RELEASE_TAG"),
		MinerUAPIToken:        firstEnv("MINERU_API_TOKEN"),
		MinerUAPIBaseURL:      firstEnvDefault("https://mineru.net", "MINERU_API_BASE_URL"),
		GitHubClientID:        firstEnv("GITHUB_CLIENT_ID"),
		GitHubClientSecret:    firstEnv("GITHUB_CLIENT_SECRET"),
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

	// Normalize filesystem paths: resolve ~ and relative-to-CWD.
	cfg.WikiDir = expandPath(cfg.WikiDir)
	cfg.RawDir = expandPath(cfg.RawDir)
	cfg.DataDir = expandPath(cfg.DataDir)

	return cfg, nil
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

// firstEnvBool parses the first non-empty env value as a bool. Accepts the
// usual truthy strings ("1", "true", "yes", "on") case-insensitively.
func firstEnvBool(names ...string) bool {
	raw := strings.ToLower(firstEnv(names...))
	switch raw {
	case "1", "true", "yes", "on", "y", "t":
		return true
	default:
		return false
	}
}

// expandPath resolves ~ and converts relative paths to absolute (using CWD).
// Empty input returns empty output.
func expandPath(p string) string {
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	if !filepath.IsAbs(p) {
		if cwd, err := os.Getwd(); err == nil {
			p = filepath.Join(cwd, p)
		}
	}
	return filepath.Clean(p)
}
