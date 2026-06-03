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
	"log/slog"
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
	//   - RawDir    -> ${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/raw
	//   - DataDir   -> ${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/data
	//   - PBDataDir -> ${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/pb_data
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
//   - RawDir    -> "${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/raw"
//   - DataDir   -> "${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/data"
//   - PBDataDir -> "${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/pb_data"
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

	// Emit deprecation warnings for the legacy unprefixed env vars
	// (WIKI_DIR / RAW_DIR / SERVER_HOST / ...). Functionally they still
	// resolve via firstEnv() above so existing .env files keep working;
	// the warning gives operators one minor cycle to migrate before the
	// alias is removed in v0.17.0.
	warnDeprecatedAliases()

	// v0.17.0 renamed the XDG sub-namespace from "quantum-atlas" to
	// "qatlasd" (matches the binary name). On an upgraded host the
	// legacy directory may still hold the only copy of pb_data — and
	// because cfg.PBDataDir now defaults to the NEW path, qatlasd
	// would otherwise boot against an empty SQLite and look like a
	// brand-new install (OAuth setup reset, every PAT vanished, etc.).
	// Refuse to start in that case and tell the operator exactly how
	// to migrate; see docs/deployment/migration-storage-layout.md.
	if err := validateLegacyQuantumAtlasDir(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// validateLegacyQuantumAtlasDir fails fast when the pre-v0.17.0 XDG
// layout still exists but the resolved config points at the (empty,
// missing, or never-touched) new layout. The check is gated on three
// "this would surprise the operator" conditions, all of which must
// hold:
//
//  1. ~/.local/share/quantum-atlas/pb_data exists and looks like a
//     real PocketBase data dir (`data.db` present);
//  2. cfg.PBDataDir is the new XDG default (no explicit env / flag
//     override pointed it elsewhere);
//  3. cfg.PBDataDir does NOT contain a data.db of its own (so we
//     wouldn't be silently overwriting the legacy state, but we also
//     don't have a populated new dir the operator obviously wants).
//
// When all three fire the operator forgot to migrate. Emit the exact
// commands they need plus a way to opt out.
//
// The DataDir and RawDir cases are still warn-only — they're easy to
// rebuild from the source-of-truth wiki / RustFS, and silently
// recreating an empty `data/` or `raw/` causes no data loss. PBDataDir
// is the lone "if you lose it, your users + PATs are gone" case.
func validateLegacyQuantumAtlasDir(cfg *Config) error {
	// Documented escape hatch — operators with weird non-standard
	// setups our heuristic doesn't recognise can opt out instead of
	// being blocked.
	if v := strings.TrimSpace(strings.ToLower(os.Getenv("QATLAS_SKIP_LEGACY_DIR_CHECK"))); v == "1" || v == "true" || v == "yes" {
		return nil
	}

	base := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if base == "" || !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return nil
		}
		base = filepath.Join(home, ".local", "share")
	}
	legacyRoot := filepath.Join(base, "quantum-atlas")
	legacyPBData := filepath.Join(legacyRoot, "pb_data")
	currentPBData := filepath.Join(base, "qatlasd", "pb_data")

	// Bail early if the legacy layout was already migrated / never existed.
	if _, err := os.Stat(filepath.Join(legacyPBData, "data.db")); err != nil {
		// No populated legacy pb_data — fall through to the soft warn
		// for the other subdirectories (data/, raw/) and return.
		warnLegacyAuxiliaryDirs(legacyRoot, base)
		return nil
	}

	// If the operator explicitly pointed PBDataDir somewhere
	// non-default (env var, --pb-data-dir flag, .env file), they
	// presumably know where their data lives — don't second-guess.
	if cfg.PBDataDir != currentPBData {
		return nil
	}

	// If the new dir is already populated with its own data.db, the
	// operator must have started a fresh install after the upgrade.
	// At that point preserving the legacy data is up to them; the
	// warn is enough.
	if _, err := os.Stat(filepath.Join(currentPBData, "data.db")); err == nil {
		slog.Warn(
			"both pre-v0.17.0 and current XDG pb_data exist; using current. "+
				"If the current one is empty, stop the server and decide which to keep.",
			"legacy_pb_data", legacyPBData,
			"current_pb_data", currentPBData,
		)
		warnLegacyAuxiliaryDirs(legacyRoot, base)
		return nil
	}

	return fmt.Errorf(
		"detected pre-v0.17.0 XDG data directory at %s but the new default %s is empty. "+
			"v0.17.0 renamed the XDG sub-namespace from 'quantum-atlas' to 'qatlasd'. "+
			"To migrate (recommended):\n"+
			"    systemctl stop qatlasd  # or however you run it\n"+
			"    mv %s %s\n"+
			"    systemctl start qatlasd\n"+
			"Or keep the legacy paths by setting these env vars before starting qatlasd:\n"+
			"    QATLAS_PB_DATA_DIR=%s\n"+
			"    QATLAS_DATA_DIR=%s\n"+
			"    QATLAS_RAW_DIR=%s\n"+
			"See docs/deployment/migration-storage-layout.md for details. "+
			"If this check is wrong for your setup, set QATLAS_SKIP_LEGACY_DIR_CHECK=1 to bypass.",
		legacyPBData, currentPBData,
		legacyRoot, filepath.Join(base, "qatlasd"),
		legacyPBData,
		filepath.Join(legacyRoot, "data"),
		filepath.Join(legacyRoot, "raw"),
	)
}

// warnLegacyAuxiliaryDirs emits a soft warning for the non-critical
// XDG subdirectories that still live under the old layout. Data
// loss is recoverable (raw/ from RustFS, data/ from regenerated
// state), so this stays warn-only.
func warnLegacyAuxiliaryDirs(legacyRoot, base string) {
	for _, sub := range []string{"data", "raw"} {
		old := filepath.Join(legacyRoot, sub)
		if info, err := os.Stat(old); err == nil && info.IsDir() {
			slog.Warn(
				"pre-v0.17.0 XDG sub-directory still present; new default is empty. "+
					"Move it or set the matching QATLAS_*_DIR env var to silence this.",
				"legacy_path", old,
				"current_path", filepath.Join(base, "qatlasd", sub),
			)
		}
	}
}

// warnLegacyQuantumAtlasDir kept as a compatibility entrypoint for
// callers that don't have access to the resolved cfg. Wraps the
// stricter validate function and downgrades the failure to a slog.Warn
// so existing tests that only checked log output still pass.
//
// New code should call validateLegacyQuantumAtlasDir directly.
func warnLegacyQuantumAtlasDir() {
	if v := strings.TrimSpace(strings.ToLower(os.Getenv("QATLAS_SKIP_LEGACY_DIR_CHECK"))); v == "1" || v == "true" || v == "yes" {
		return
	}
	cfg := &Config{PBDataDir: defaultXDGSubdir("pb_data")}
	if err := validateLegacyQuantumAtlasDir(cfg); err != nil {
		slog.Warn(err.Error())
	}
}

// deprecatedAliases is the canonical map of legacy unprefixed env vars
// to their QATLAS_-prefixed replacements. Kept exported via a function
// for tests to assert against, not as a package-level var, so callers
// can't accidentally mutate the table.
//
// NEO4J_USER deliberately stays out: both NEO4J_USERNAME and NEO4J_USER
// are equally idiomatic across the Neo4j ecosystem (Python driver,
// Go driver, neo4j-admin all accept either), so we treat them as peers
// rather than deprecating one.
//
// SERVER_DEBUG is also absent because the codebase never read it — it
// was a phantom alias referenced only in old .env.example comments.
func deprecatedAliases() map[string]string {
	return map[string]string{
		"WIKI_DIR":        "QATLAS_WIKI_DIR",
		"RAW_DIR":         "QATLAS_RAW_DIR",
		"DATA_DIR":        "QATLAS_DATA_DIR",
		"PB_DATA_DIR":     "QATLAS_PB_DATA_DIR",
		"SERVER_HOST":     "QATLAS_SERVER_HOST",
		"SERVER_PORT":     "QATLAS_SERVER_PORT",
		"PUBLIC_BASE_URL": "QATLAS_SERVER_URL",
		"USER_HEADER":     "QATLAS_USER_HEADER",
	}
}

// warnDeprecatedAliases emits one slog.Warn per legacy unprefixed env
// var found in the process environment. Deterministic order (sorted by
// old name) so journald / log diffs are stable.
func warnDeprecatedAliases() {
	aliases := deprecatedAliases()
	oldNames := make([]string, 0, len(aliases))
	for old := range aliases {
		oldNames = append(oldNames, old)
	}
	sort.Strings(oldNames)
	for _, old := range oldNames {
		if v := strings.TrimSpace(os.Getenv(old)); v != "" {
			slog.Warn("env var without QATLAS_ prefix is deprecated, will be removed in v0.17.0",
				"deprecated", old, "use_instead", aliases[old])
		}
	}
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
// for the named qatlasd subdirectory (raw / data / pb_data).
//
// Lookup order:
//  1. $XDG_DATA_HOME, when set and absolute (per XDG spec — relative
//     values are explicitly invalid).
//  2. $HOME/.local/share, the spec's documented fallback.
//  3. ./.qatlasd-<name>, a last-resort relative path when even
//     $HOME is missing (e.g. minimal container). This still beats
//     emitting an absolute root like "/qatlasd/raw" that would
//     fail with EACCES on the first write.
//
// All returned values are absolute when paths #1 or #2 apply.
//
// **App name = "qatlasd"** (matches the binary name). Older versions
// (< v0.17.0) used "quantum-atlas" as the XDG sub-namespace. Operators
// upgrading from < v0.17.0 should rename their data directory:
//
//	mv ~/.local/share/quantum-atlas ~/.local/share/qatlasd
//
// or set the explicit env vars (QATLAS_RAW_DIR / _DATA_DIR /
// _PB_DATA_DIR) to point at the old paths. See
// docs/deployment/migration-storage-layout.md.
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
