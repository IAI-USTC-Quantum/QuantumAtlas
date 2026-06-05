// CLI flag surface for `qatlasd serve`.
//
// Mirrors the pattern easytier-core uses (Rust clap, every #[arg] also
// has `env = "ET_FOO"`): each operator-tunable knob becomes both a CLI
// flag and an env var alias, and the precedence is [CLI > env > .env
// file > built-in default]. clap does this in a single derive; in Go
// we get the same shape from two pieces:
//
//   - cobra owns the CLI flag definition + parse (this file)
//   - config.Load (internal/config/config.go) owns env reading,
//     including the deprecated unprefixed aliases (WIKI_DIR etc.)
//
// applyServeFlags() runs AFTER config.Load and overrides cfg with
// the flag values cobra collected, but ONLY when the operator
// actually passed --foo on the command line (cobra's Flag.Changed
// bit). This preserves the env / .env / default precedence cobra and
// godotenv already implement between them.
//
// Known limitation — OAuth credentials are env-only:
//
//   PocketBase runs Bootstrap() BEFORE cobra parses argv, and
//   Bootstrap is where auth.Register mounts the GitHub OAuth provider
//   onto PocketBase's settings. By the time our serve RunE fires and
//   applyServeFlags() runs, the OAuth provider is already configured
//   with whatever GITHUB_CLIENT_ID / _SECRET / QATLAS_*_GITHUB_LOGINS
//   env vars said. Adding --github-client-id / --allowed-github-logins
//   flags would silently no-op, which is worse than not having them
//   at all. Operators must configure those four via env (.env or
//   `Environment=` in the systemd unit or `-e` in docker run).
//
// Why we deliberately do NOT pull in viper here:
//
//   - viper's value add is reading flag + env in one Get() call.
//     But our env layer already handles deprecated aliases via
//     firstEnv(); viper.BindEnv would lose that. The cleanest split
//     is "cobra=flags, config.Load=env+alias", with this file as the
//     thin glue.
//   - viper drags in ~1.5MB of binary size + a config-file reader
//     that overlaps with our godotenv path. The current shape is
//     small enough that the standard cobra/env-handler pattern reads
//     naturally without the extra dep.
//
// The serve command is the only one that exposes config knobs as
// flags. Operator subcommands (pat / users / storage / config) read
// the same env / .env state directly — exposing flags for everything
// would multiply the surface area without any user-visible benefit.

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"

	"github.com/spf13/cobra"
)

// serveFlagSpec describes one operator-tunable knob: the cobra flag
// name (kebab-case, no QATLAS_ prefix), the env var name (canonical,
// usually QATLAS_*; sometimes SDK-standard like NEO4J_URI), a one-line
// help string, and the Go type tag.
//
// The flag's actual default is intentionally NOT stored here — we
// register flags with empty defaults so applyServeFlags() can
// distinguish "user didn't pass --foo" (Flag.Changed=false) from
// "user passed --foo=''" (Changed=true, value=""). Real defaults are
// preserved by config.Load's existing fallback logic.
type serveFlagSpec struct {
	name    string // cobra flag name, e.g. "neo4j-uri"
	env     string // env var name, e.g. "NEO4J_URI"
	help    string // one-line description (env tag appended automatically)
	kind    string // "string" | "bool" | "stringslice"
	bindPtr any    // pointer to the destination variable in qatlasdServeFlags
}

// qatlasdServeFlags holds the raw flag values populated by cobra
// during parse. applyServeFlags() merges them into *config.Config
// after the .env load (so the [CLI > env > .env > default]
// precedence holds end-to-end).
type qatlasdServeFlags struct {
	// Server URL / identity
	publicURL  string
	userHeader string
	edgeName   string
	forceTCP4  bool

	// Filesystem
	wikiDir   string
	rawDir    string
	dataDir   string
	pbDataDir string

	// System PAT
	systemPAT       string
	systemPATScopes []string

	// Neo4j
	neo4jURI      string
	neo4jUsername string
	neo4jPassword string
	neo4jDatabase string

	// S3 / RustFS
	s3Endpoint        string
	s3PublicEndpoint  string
	s3BucketPDF       string
	s3BucketMD        string
	s3BucketImages    string
	s3BucketOpenAlex  string
	s3AccessKeyID     string
	s3SecretAccessKey string
}

// serveFlagSpecs is the canonical flag list. Kept as a function (not a
// package-level slice) so each call gets a fresh slice with pointers
// into the supplied qatlasdServeFlags — needed because cobra binds
// flags to specific memory addresses.
//
// GitHub OAuth credentials and allowlists are deliberately absent
// from this list — see the package doc for why ("Known limitation —
// OAuth credentials are env-only").
func serveFlagSpecs(f *qatlasdServeFlags) []serveFlagSpec {
	return []serveFlagSpec{
		// Server URL / identity
		{"public-url", "QATLAS_PUBLIC_URL", "Canonical public URL of this server (used to construct absolute OAuth callbacks and share links; named to distinguish from the client-side QATLAS_SERVER_URL which means 'server I talk to')", "string", &f.publicURL},
		{"user-header", "QATLAS_USER_HEADER", "Audit user header name injected by the reverse proxy (e.g. X-Token-Subject)", "string", &f.userHeader},
		{"edge-name", "QATLAS_EDGE_NAME", "Edge identifier folded into the S3 client User-Agent for audit logs", "string", &f.edgeName},
		{"force-tcp4", "QATLAS_FORCE_TCP4", "Force a tcp4-only listener (WSL2 + Windows portproxy escape hatch)", "bool", &f.forceTCP4},

		// Filesystem
		{"wiki-dir", "QATLAS_WIKI_DIR", "Local checkout of the Wiki repo (markdown + frontmatter)", "string", &f.wikiDir},
		{"raw-dir", "QATLAS_RAW_DIR", "LocalStore fallback when S3 disabled (dev-only)", "string", &f.rawDir},
		{"data-dir", "QATLAS_DATA_DIR", "Server-managed metadata (ingests, mineru claims)", "string", &f.dataDir},
		{"pb-data-dir", "QATLAS_PB_DATA_DIR", "PocketBase data dir (SQLite + collections + uploads)", "string", &f.pbDataDir},

		// System PAT
		{"system-pat", "QATLAS_SYSTEM_PAT", "Operator breakglass bearer token (≥16 chars or boot fatal)", "string", &f.systemPAT},
		{"system-pat-scopes", "QATLAS_SYSTEM_PAT_SCOPES", "Comma-separated scope list for the system PAT (default: *)", "stringslice", &f.systemPATScopes},

		// Neo4j (third-party env names — no QATLAS_ prefix)
		{"neo4j-uri", "NEO4J_URI", "Neo4j Bolt URL (empty disables catalog features)", "string", &f.neo4jURI},
		{"neo4j-username", "NEO4J_USERNAME", "Neo4j username", "string", &f.neo4jUsername},
		{"neo4j-password", "NEO4J_PASSWORD", "Neo4j password", "string", &f.neo4jPassword},
		{"neo4j-database", "NEO4J_DATABASE", "Neo4j database name (multi-DB deployments)", "string", &f.neo4jDatabase},

		// S3 / RustFS (all-or-nothing)
		{"s3-endpoint", "QATLAS_S3_ENDPOINT", "Internal S3 endpoint URL (must include scheme)", "string", &f.s3Endpoint},
		{"s3-public-endpoint", "QATLAS_S3_PUBLIC_ENDPOINT", "Public S3 endpoint for presigned URLs (optional)", "string", &f.s3PublicEndpoint},
		{"s3-bucket-pdf", "QATLAS_S3_BUCKET_PDF", "Per-kind bucket for PDF assets", "string", &f.s3BucketPDF},
		{"s3-bucket-md", "QATLAS_S3_BUCKET_MD", "Per-kind bucket for Markdown assets", "string", &f.s3BucketMD},
		{"s3-bucket-images", "QATLAS_S3_BUCKET_IMAGES", "Per-kind bucket for image assets", "string", &f.s3BucketImages},
		{"s3-bucket-openalex", "QATLAS_S3_BUCKET_OPENALEX_SNAPSHOT", "Optional bucket for OpenAlex snapshot ingest", "string", &f.s3BucketOpenAlex},
		{"s3-access-key-id", "QATLAS_S3_ACCESS_KEY_ID", "S3 service-account access key (NEVER use root keys)", "string", &f.s3AccessKeyID},
		{"s3-secret-access-key", "QATLAS_S3_SECRET_ACCESS_KEY", "S3 service-account secret key", "string", &f.s3SecretAccessKey},
	}
}

// registerServeFlags attaches the qatlasd-specific config knobs to the
// PocketBase serve subcommand. PocketBase ships its own --http /
// --https / --origins / --dir / --encryptionEnv; we don't touch those.
//
// Each flag's help string is automatically suffixed with `[env: FOO=]`
// so `qatlasd serve --help` documents the env-var alias inline (the
// pattern easytier uses and operators have come to expect).
//
// Returns the *qatlasdServeFlags struct cobra fills during parse; the
// caller should keep it alive and pass it to applyServeFlags() inside
// the serve command's RunE before any cfg-dependent init runs.
func registerServeFlags(serveCmd *cobra.Command) *qatlasdServeFlags {
	f := &qatlasdServeFlags{}
	flagSet := serveCmd.PersistentFlags()
	for _, spec := range serveFlagSpecs(f) {
		help := fmt.Sprintf("%s [env: %s=]", spec.help, spec.env)
		switch spec.kind {
		case "string":
			flagSet.StringVar(spec.bindPtr.(*string), spec.name, "", help)
		case "bool":
			flagSet.BoolVar(spec.bindPtr.(*bool), spec.name, false, help)
		case "stringslice":
			flagSet.StringSliceVar(spec.bindPtr.(*[]string), spec.name, nil, help)
		default:
			panic(fmt.Sprintf("registerServeFlags: unknown kind %q for flag %q", spec.kind, spec.name))
		}
	}
	return f
}

// applyServeFlags overlays the user-supplied CLI flag values onto cfg.
// Called from the serve command's RunE after godotenv has populated
// os.Environ from .env and after config.Load has filled cfg from
// those env vars — so the precedence chain ends up being [CLI flag >
// OS env > .env file > built-in default], matching easytier / clap /
// viper conventions.
//
// "Supplied" here means cobra saw an explicit --flag in argv. Default
// (empty / false / nil) values do NOT overwrite cfg — otherwise a
// flag that wasn't passed would clobber an env-supplied value, which
// is the opposite of what every operator expects.
//
// We detect "user-set" via cmd.Flag(name).Changed; that bit is set by
// cobra only when argv contains the flag, even if the value happens
// to equal the default.
func applyServeFlags(cmd *cobra.Command, f *qatlasdServeFlags, cfg *config.Config) {
	if cmd == nil || f == nil || cfg == nil {
		return
	}

	set := func(name string) bool {
		flag := cmd.Flag(name)
		return flag != nil && flag.Changed
	}

	// Server URL / identity
	if set("public-url") {
		cfg.PublicURL = f.publicURL
	}
	if set("user-header") {
		cfg.UserHeader = f.userHeader
	}
	if set("edge-name") {
		cfg.EdgeName = f.edgeName
	}
	// force-tcp4 is read directly from env (forceTCP4() in main.go);
	// if --force-tcp4 was passed, mirror it into the env var so the
	// existing reader sees it.
	if set("force-tcp4") && f.forceTCP4 {
		_ = os.Setenv("QATLAS_FORCE_TCP4", "1")
	}

	// Filesystem
	if set("wiki-dir") {
		cfg.WikiDir = f.wikiDir
	}
	if set("raw-dir") {
		cfg.RawDir = f.rawDir
	}
	if set("data-dir") {
		cfg.DataDir = f.dataDir
	}
	if set("pb-data-dir") {
		cfg.PBDataDir = f.pbDataDir
	}

	// System PAT — these don't live on cfg (read directly from env in
	// pat.LoadSystemPAT). Mirror back into os.Environ so the existing
	// reader sees the CLI-supplied value.
	if set("system-pat") {
		_ = os.Setenv("QATLAS_SYSTEM_PAT", f.systemPAT)
	}
	if set("system-pat-scopes") {
		_ = os.Setenv("QATLAS_SYSTEM_PAT_SCOPES", strings.Join(f.systemPATScopes, ","))
	}

	// Neo4j
	if set("neo4j-uri") {
		cfg.Neo4jURI = f.neo4jURI
	}
	if set("neo4j-username") {
		cfg.Neo4jUser = f.neo4jUsername
	}
	if set("neo4j-password") {
		cfg.Neo4jPassword = f.neo4jPassword
	}
	if set("neo4j-database") {
		cfg.Neo4jDatabase = f.neo4jDatabase
	}

	// S3
	if set("s3-endpoint") {
		cfg.S3Endpoint = f.s3Endpoint
	}
	if set("s3-public-endpoint") {
		cfg.S3PublicEndpoint = f.s3PublicEndpoint
	}
	if set("s3-bucket-pdf") {
		cfg.S3BucketPDF = f.s3BucketPDF
	}
	if set("s3-bucket-md") {
		cfg.S3BucketMD = f.s3BucketMD
	}
	if set("s3-bucket-images") {
		cfg.S3BucketImages = f.s3BucketImages
	}
	if set("s3-bucket-openalex") {
		cfg.S3BucketOpenAlex = f.s3BucketOpenAlex
	}
	if set("s3-access-key-id") {
		cfg.S3AccessKeyID = f.s3AccessKeyID
	}
	if set("s3-secret-access-key") {
		cfg.S3SecretAccessKey = f.s3SecretAccessKey
	}
}
