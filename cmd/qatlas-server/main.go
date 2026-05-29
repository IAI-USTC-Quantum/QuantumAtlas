// Command qatlas-server is the Go + PocketBase rewrite of the QuantumAtlas
// FastAPI server. It embeds PocketBase as a Go library and exposes the same
// /api/* surface that the existing Python CLI consumes.
//
// Usage:
//
//	qatlas-server serve --http=0.0.0.0:4200
//	qatlas-server migrate up
//	qatlas-server superuser upsert <email> <password>
//
// All standard PocketBase subcommands are inherited. QuantumAtlas-specific
// business routes are registered via the OnServe hook.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/healthz"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineruclaim"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperindex"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/routes"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/shares"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/wiki"
	qweb "github.com/IAI-USTC-Quantum/QuantumAtlas/web"

	"github.com/casbin/casbin/v2"
	"github.com/joho/godotenv"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

// installServerScript is the shell installer served at
// /install-server.sh. It detects OS/arch, downloads the latest
// qatlas-server release artifact from GitHub, installs to
// ~/.local/bin, and prints next-step pointers. Kept in a separate
// file so it can be edited as a real .sh (syntax highlighting +
// shellcheck) and reviewed standalone.
//
//go:embed install-server.sh
var installServerScript string

// Version is overridden at build time via:
//
//	go build -ldflags "-X main.Version=$(cat pyproject.toml ...)"
//
// Defaults to "dev" as a sentinel — if `qatlas-server --version` or
// /api/health reports "dev" in production, the binary was built without
// the release pipeline's -ldflags injection (most likely a manual
// `go build` instead of a GitHub Actions artifact). A real version
// string like "0.2.9" should be unambiguously distinguishable from the
// fallback so the failure mode is visible at a glance.
var Version = "dev"

func main() {
	// Load .env BEFORE config.Load so any vars it sets win over
	// preset systemd environment (godotenv.Load skips existing keys,
	// which is correct for the "real env beats file" precedence we want).
	//
	// Why we load it ourselves instead of relying on systemd's
	// EnvironmentFile= directive: the latter strips the file path, so
	// the server can't know where the .env lives and therefore can't
	// resolve relative paths like WIKI_DIR=../QuantumAtlas-Wiki against
	// the .env directory. Loading it ourselves lets config.Load() use
	// the .env directory as the anchor for relative paths.
	dotenvPath := loadDotEnv()
	cfg, err := config.Load(dotenvPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// Inject CLI flags from env BEFORE pocketbase.New() — that
	// constructor eagerly parses os.Args[1:] to materialise its
	// persistent flags (--dir, --dev, etc.) and snapshots the values
	// into the BaseApp. Anything we mutate after construction is
	// ignored by the app instance even though cobra.Execute() does
	// see it. Bug repro: setting QATLAS_PB_DATA_DIR and running
	// `qatlas-server superuser upsert ...` used to silently write to
	// ./build/pb_data because the injection happened post-New().
	injectHTTPFlag(cfg)
	injectPBDataDirFlag(cfg)

	app := pocketbase.NewWithConfig(pocketbase.Config{
		// Custom SQLite tuning — see sqlite_tuning.go for the rationale
		// behind each pragma. Falls back to PB's DefaultDBConnect when
		// this returns an error (so a typo here can't brick startup
		// silently; PB will log and try the default path).
		DBConnect: qatlasDBConnect,
	})

	// Surface main.Version (set via -ldflags "-X main.Version=$VERSION")
	// to PocketBase's cobra root command so `qatlas-server --version`
	// prints "qatlas-server version 0.2.4" instead of the default
	// "qatlas-server version (untracked)". Without this, the version
	// string we inject is only visible in /api/health's `data.version`
	// field — operators running `--version` on the CLI see nothing.
	app.RootCmd.Version = Version

	auth.Register(app, cfg)

	// Mount the `pat` subcommand group. This MUST come before
	// app.Start() — cobra binds commands by reference at parse time,
	// and any additions after the root walks os.Args are ignored.
	app.RootCmd.AddCommand(NewPATCommand(app))

	// Mount the `storage` subcommand group (object-store maintenance:
	// `storage prune` enumerates and deletes noncurrent S3 versions).
	// Same registration timing constraint as pat above.
	app.RootCmd.AddCommand(NewStorageCommand())

	// Mount the `service` subcommand group (install / uninstall / start /
	// stop / restart / status — wraps kardianos/service to manage the
	// systemd unit / launchd plist / Windows SCM entry). Same timing
	// constraint as pat / storage above.
	app.RootCmd.AddCommand(NewServiceCommand())

	// Mount the `bootstrap-index` subcommand (one-shot full-bucket
	// scan that rebuilds index/papers.parquet, including arxiv
	// metadata enrichment from json files). Same timing constraint
	// as pat / storage / service above. Operator-driven; see
	// bootstrap_index_cmd.go for the "stop service first" caveat.
	app.RootCmd.AddCommand(NewBootstrapIndexCommand())

	// Install our default PAT-surface rate-limit rules. Done at
	// OnBootstrap (after PocketBase has loaded settings from the DB)
	// rather than synchronously here, because Settings() is empty
	// until bootstrap fires.
	app.OnBootstrap().BindFunc(func(e *core.BootstrapEvent) error {
		if err := e.Next(); err != nil {
			return err
		}
		// Log effective SQLite pragmas + run startup WAL checkpoint.
		// Done after e.Next so PB has finished opening connections.
		logSQLitePragmas(context.Background(), app)
		changed, err := pat.EnsureDefaults(app)
		if err != nil {
			// Non-fatal: starting the server without rate limits is
			// still better than refusing to start at all. Log loudly
			// so the operator notices.
			slog.Warn("pat: failed to install default rate limits", "error", err)
			return nil
		}
		if changed {
			slog.Info("pat: installed default rate-limit rules", "rules", len(pat.DefaultRateLimitRules))
		}
		return nil
	})

	// Initialize on-disk JSON stores up-front so route handlers can
	// share single instances. Both stores no-op when their dirs already
	// exist, so this is safe to call on every boot.
	shareStore, err := initShareStore(cfg)
	if err != nil {
		log.Fatalf("init share store: %v", err)
	}
	claimStore, err := initClaimStore(cfg)
	if err != nil {
		log.Fatalf("init mineru claim store: %v", err)
	}

	// Wire the raw asset backend. S3Enabled is the documented split
	// point: when QATLAS_S3_* are all set we route every PDF /
	// markdown / JSON / image through RustFS (or any S3-compatible
	// store), otherwise we wrap cfg.RawDir with a LocalStore. The
	// config layer already validated the all-or-nothing rule, so
	// neither branch can land in a half-configured state here.
	rawStore, err := initRawStore(cfg)
	if err != nil {
		log.Fatalf("init raw object store: %v", err)
	}
	// Reconcile bucket versioning so accidental overwrites are
	// recoverable via S3 ListObjectVersions. Idempotent; non-fatal
	// when the IAM user lacks s3:Put/GetBucketVersioning (server
	// still serves, just without rollback safety) — log so ops
	// notices and grants the perms next deploy.
	if s3Store, ok := rawStore.(*objstore.S3Store); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		prior, changed, vErr := s3Store.EnsureVersioning(ctx)
		cancel()
		switch {
		case vErr != nil:
			slog.Warn("bucket versioning: reconcile failed; overwrites will not be recoverable",
				"bucket", cfg.S3Bucket, "error", vErr)
		case changed:
			log.Printf("bucket versioning: enabled (was: %q)", prior)
		default:
			log.Printf("bucket versioning: already enabled")
		}
	}

	// Build the casbin enforcer once at startup. The policy table is
	// static (defined in internal/pat/scopes.go), so a single shared
	// instance is enough — Enforce() is safe for concurrent reads.
	// Failing here is fatal: every write endpoint depends on it.
	enforcer, err := pat.NewEnforcer()
	if err != nil {
		log.Fatalf("build PAT scope enforcer: %v", err)
	}

	// Build the paperindex catalog if S3 backend is configured.
	// Non-fatal on failure: needsMineruHandler & friends will fall
	// back to the legacy store.ListPrefix path (slow but correct).
	// Rationale + design: docs/architecture.md → "论文元数据索引"
	// section. The actual parquet must be (re)built via the
	// bootstrap-index subcommand; if it doesn't exist yet, the
	// Store starts up empty and queries return zero.
	paperIndex := initPaperIndex(cfg, rawStore)

	// Build the server-side MinerU converter. Always constructed; it only
	// actually converts when MINERU_API_TOKEN is set (Converter.Enabled).
	// Powers the silent conversion behind GET /api/papers/{id}/markdown.
	mineruConverter := mineru.NewConverter(cfg, rawStore, shareStore)
	if mineruConverter.Enabled() {
		log.Printf("mineru: server-side silent conversion enabled (base=%s)", cfg.MinerUAPIBaseURL)
	} else {
		log.Printf("mineru: server-side conversion disabled (MINERU_API_TOKEN unset); markdown served from cache only")
	}

	// Build the wiki in-memory cache. Walks cfg.WikiDir once at
	// startup so the first /api/pages request hits warm data; a
	// background ticker re-walks every 60s when `git rev-parse HEAD`
	// shows a different commit (covers out-of-band `git pull` /
	// direct edits). /api/wiki/sync/pull also forces a synchronous
	// refresh so the response reflects the just-pulled commit.
	// Always non-nil — initial-load failures degrade to empty
	// responses, not crashes.
	wikiCache := wiki.NewCache(cfg.WikiDir, 60*time.Second)
	log.Printf("wiki: cache initialized (dir=%s)", cfg.WikiDir)
	app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		wikiCache.Stop()
		return e.Next()
	})

	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		// Capture process start time the first time a serve event
		// fires. /api/health uses this to report uptime_seconds.
		serverStarted := time.Now()

		// Optional: force a tcp4-native listener when the operator opts
		// in via QATLAS_FORCE_TCP4=1. Background:
		//
		// On modern Go (1.21+), net.Listen("tcp", "0.0.0.0:4200") returns
		// a dual-stack v6 socket with IPV6_V6ONLY=0 — visible in
		// /proc/<pid>/net/tcp6 but absent from /proc/<pid>/net/tcp.
		// IPv4 clients normally still reach it via IPv4-mapped IPv6.
		//
		// On regular Linux cloud VMs this is fine and serves both v4 and
		// v6 clients out of one socket. **Don't** flip this on by default
		// for community deployments — you would shut out v6-only callers.
		//
		// WSL2 + Windows netsh portproxy is the exception: the v4-only
		// portproxy rule (10.144.18.10:4200 -> 127.0.0.1:4200) injects
		// raw v4 SYNs into the WSL2 NAT layer which then cannot match a
		// pure v6 socket, even with bindv6only=0. Edge Caddy reverse-
		// proxying through the mesh sees endless 502s while curl from
		// inside WSL2 to 127.0.0.1:4200 works. The fix is to bind a
		// tcp4 socket ourselves and inject it into ServeEvent.
		//
		// Toggled on for our 1810 systemd unit; unset for everyone else.
		if forceTCP4() && se.Listener == nil && se.Server != nil {
			if l, err := maybeIPv4Listener(se.Server.Addr); err == nil && l != nil {
				se.Listener = l
				log.Printf("QATLAS_FORCE_TCP4=1: forced tcp4 listener on %s", se.Server.Addr)
			} else if err != nil {
				log.Printf("QATLAS_FORCE_TCP4=1 but listener bind failed: %v (falling back to PocketBase default)", err)
			}
		}

		registerRoutes(se, app, cfg, rawStore, shareStore, claimStore, paperIndex, mineruConverter, wikiCache, enforcer, serverStarted)

		// Serve the embedded SPA last as the catch-all. apis.Static's
		// indexFallback=true means any path that doesn't match a real
		// file falls back to /index.html — exactly the SPA-client-router
		// behavior the React app needs for /wiki, /graph, /token, etc.
		se.Router.GET("/{path...}", apis.Static(qweb.MustFS(), true))

		return se.Next()
	})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}

// initShareStore returns a ready-to-use ShareStore rooted at
// {DATA_DIR}/shares. cfg.DataDir always carries a value (XDG default
// applied in config.Load), so this no longer needs a "DATA_DIR unset"
// fallback like it did before storage paths got proper defaults.
func initShareStore(cfg *config.Config) (*shares.Store, error) {
	return shares.NewStore(filepath.Join(cfg.DataDir, "shares"))
}

// initClaimStore returns a ready-to-use mineru claim store rooted at
// {DATA_DIR}/mineru-claims.
func initClaimStore(cfg *config.Config) (*mineruclaim.Store, error) {
	return mineruclaim.NewStore(filepath.Join(cfg.DataDir, "mineru-claims"))
}

// initRawStore returns the objstore.Store implementation backing raw
// paper assets (PDFs, markdown, JSON, images). Selects between
// LocalStore (cfg.RawDir) and S3Store (QATLAS_S3_*) based on
// cfg.S3Enabled() — see internal/config/config.go for the
// invariant that guarantees this can't be half-configured.
//
// When cfg.S3PublicEndpoint is set and distinct from cfg.S3Endpoint we
// use NewS3StoreDual so presigned URLs handed to end-user clients
// point at the public endpoint (server↔RustFS still goes through the
// internal endpoint). Empty / equal collapses to single-endpoint mode.
func initRawStore(cfg *config.Config) (objstore.Store, error) {
	if cfg.S3Enabled() {
		if cfg.S3PublicEndpoint != "" && cfg.S3PublicEndpoint != cfg.S3Endpoint {
			log.Printf("raw store: S3 backend %s/%s (presign via %s)",
				cfg.S3Endpoint, cfg.S3Bucket, cfg.S3PublicEndpoint)
		} else {
			log.Printf("raw store: S3 backend %s/%s", cfg.S3Endpoint, cfg.S3Bucket)
		}
		return objstore.NewS3StoreDual(
			cfg.S3Endpoint, cfg.S3PublicEndpoint,
			cfg.S3Bucket, cfg.S3AccessKeyID, cfg.S3SecretAccessKey,
		)
	}
	log.Printf("raw store: local backend %s", cfg.RawDir)
	return objstore.NewLocalStore(cfg.RawDir)
}

// initPaperIndex constructs the in-process paperindex catalog when an
// S3 backend is configured. Always returns a non-nil Store on success;
// returns nil silently when S3 isn't enabled (local-only dev setups
// don't need the catalog — needs-mineru falls back to a trivial
// LocalStore walk which is fast on a small dev RAW_DIR).
//
// Takes the already-constructed objstore.Store so paperindex shares
// the exact same bucket/credentials/dual-endpoint configuration as the
// /api/papers handlers — single source of truth for "how do we talk to
// RustFS".
//
// Failure to build / load the catalog is non-fatal: we log a warning
// and return nil so handlers fall back to the legacy slow-but-correct
// store.ListPrefix path. The most common failure mode is "parquet
// doesn't exist yet in the bucket" — Store.New handles that as a
// soft-fail and starts with an empty catalog, so this returns
// non-nil even then.
func initPaperIndex(cfg *config.Config, rawStore objstore.Store) *paperindex.Store {
	if !cfg.S3Enabled() {
		log.Printf("paperindex: skipped (S3 backend not enabled — handlers will fall back to LocalStore walks)")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := paperindex.New(ctx, paperindex.Config{
		Store: rawStore,
	})
	if err != nil {
		log.Printf("paperindex: init failed (%v); handlers will fall back to legacy LIST-based impl", err)
		return nil
	}
	log.Printf("paperindex: catalog ready (%d rows loaded from s3://%s/%s)",
		store.RowCount(), cfg.S3Bucket, paperindex.DefaultParquetKey)
	return store
}

// registerRoutes wires the QuantumAtlas /api/* surface. Most endpoints are
// implemented under internal/routes/ and pulled in by their respective
// Register* helpers as we migrate each module in subsequent phases.
func registerRoutes(se *core.ServeEvent, app core.App, cfg *config.Config, rawStore objstore.Store, shareStore *shares.Store, claimStore *mineruclaim.Store, paperIndex *paperindex.Store, mineruConverter *mineru.Converter, wikiCache *wiki.Cache, enforcer *casbin.Enforcer, started time.Time) {
	probes := healthz.Probes{
		Cfg:      cfg,
		RawStore: rawStore,
		Version:  Version,
		Started:  started,
	}

	// Override PocketBase's built-in /api/health with our dependency-
	// aware version. Implementation note:
	//
	// PocketBase registers /api/health in apis.NewRouter() (apis/base.go
	// line 50, bindHealthApi), which runs BEFORE the OnServe hook fires.
	// The router has no "remove route" API; the tools/router package
	// will panic on duplicate-route registration at BuildMux() time.
	// So we can't `se.Router.GET("/api/health", ...)` here.
	//
	// The router DOES support per-router middleware via BindFunc,
	// which runs before any matched route handler. By short-circuiting
	// the request (writing the response and returning nil without
	// calling e.Next()), we replace PocketBase's healthCheck handler
	// for this path. PocketBase's handler still exists in the route
	// tree but is never reached.
	//
	// Why we replace rather than supplement: we want exactly ONE
	// health endpoint with ONE response shape across the project, so
	// monitoring + PocketBase SDK + ops scripts can't disagree about
	// what "healthy" means. The response is a PocketBase-shape
	// superset (see healthz.PBResult): {code, message, data:{...}}
	// keeps SDK compat, with our checks/version/uptime nested in
	// `data`. PocketBase's original `data` is just `{}` (or three
	// trivial superuser-only fields), so the superset is non-lossy
	// for unauthenticated callers.
	//
	// The path comparison is a const-time string equality on every
	// request — cheap, and only the /api/health requests hit the
	// healthz.RunPB call. The rest fall through via e.Next().
	se.Router.BindFunc(func(re *core.RequestEvent) error {
		if re.Request.Method == "GET" && re.Request.URL.Path == "/api/health" {
			return re.JSON(200, healthz.RunPB(re.Request.Context(), probes))
		}
		return re.Next()
	})

	// /install-server.sh — public installer that downloads the latest
	// qatlas-server binary from GitHub releases. Serves the static
	// script verbatim. Caching: short max-age so a fresh release is
	// picked up within ~5 min of cutover but we don't hammer the
	// server for every curl|sh.
	se.Router.GET("/install-server.sh", func(re *core.RequestEvent) error {
		re.Response.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		re.Response.Header().Set("Cache-Control", "public, max-age=300")
		_, err := re.Response.Write([]byte(installServerScript))
		return err
	})

	// /api/server/info — minimal placeholder until internal/routes/info.go
	// migrates the full Python implementation in P3.
	se.Router.GET("/api/server/info", func(re *core.RequestEvent) error {
		return re.JSON(200, map[string]any{
			"mode":    "server",
			"version": Version,
			"engine":  "go+pocketbase",
		})
	})

	// (P12 removed: /api/session/token. It was a caddy-security-era stub
	// that returned an empty string. The SPA now reads pb.authStore.token
	// directly; CLI users pull their bearer from the /token page.)

	// Wiki / pages / stats / search / lint — see internal/routes/wiki.go.
	routes.RegisterWiki(se, cfg, wikiCache)

	// Graph (Neo4j) — see internal/routes/graph.go. Gated by
	// authGuard + scopeGuard("graph", "read") so it matches the rest
	// of the non-public-repo surface; sessions bypass via ScopeMaster.
	routes.RegisterGraph(se, cfg, enforcer)

	// Papers (resources, upload, mineru-claim) — see internal/routes/papers.go.
	routes.RegisterPapers(se, cfg, rawStore, shareStore, claimStore, paperIndex, mineruConverter, enforcer)

	// Shares CRUD + public /share/{token}* — see internal/routes/shares.go.
	routes.RegisterShares(se, cfg, shareStore, rawStore, enforcer)

	// Personal Access Tokens — see internal/routes/pat.go.
	// /api/pat is session-token-only (PAT auth refused by sessionGuard);
	// no enforcer needed because there's no scope-gated endpoint here.
	routes.RegisterPAT(se, app)

	// RustFS bucket-notification webhook — see internal/routes/rustfs_event.go.
	// No-op when cfg.RustFSEventToken is empty or paperIndex is nil
	// (fail-closed against unauthenticated / un-applicable events).
	routes.RegisterRustFSEvent(se, cfg, rawStore, paperIndex)
}

// injectHTTPFlag mutates os.Args to add --http=<addr> when the user invokes
// the "serve" subcommand without supplying their own --http. This lets a
// plain `qatlas-server serve` pick up QATLAS_SERVER_HOST/PORT from .env.
func injectHTTPFlag(cfg *config.Config) {
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		return
	}
	for _, a := range os.Args[2:] {
		if a == "--http" || strings.HasPrefix(a, "--http=") {
			return
		}
	}
	os.Args = append(os.Args, "--http="+cfg.HTTPAddr)
}

// injectPBDataDirFlag mutates os.Args to add --dir=<cfg.PBDataDir> for
// any PocketBase subcommand (serve / migrate / admin / superuser / etc.)
// that doesn't already carry an explicit --dir. Mirrors the
// injectHTTPFlag pattern.
//
// Why this matters: PocketBase's default for --dir is computed from
// the binary's own location (executable_dir + "/pb_data"). For our
// build that's "./build/pb_data", which is exactly the source tree we
// don't want SQLite state landing in. cfg.PBDataDir always carries a
// value (XDG default in config.Load), so injecting it makes
//
//	qatlas-server serve
//
// equivalent to
//
//	qatlas-server --dir=$HOME/.local/share/quantum-atlas/pb_data serve
//
// on a fresh box, while still respecting any operator-supplied --dir.
//
// Note: --dir is a **global persistent** flag on the cobra root
// command; cobra refuses to recognise persistent flags placed after a
// subcommand's positional args. So we insert it right after os.Args[0]
// (before the subcommand name), NOT append it at the end like
// injectHTTPFlag does for --http (which is a serve-only local flag).
//
// We apply this for every subcommand, not just `serve`. Migrate /
// superuser / admin commands all need to point at the same pb_data
// root or they'd silently operate on a freshly-created empty database.
func injectPBDataDirFlag(cfg *config.Config) {
	if len(os.Args) < 2 {
		return
	}
	if cfg.PBDataDir == "" {
		return
	}
	for _, a := range os.Args[1:] {
		if a == "--dir" || strings.HasPrefix(a, "--dir=") {
			return
		}
	}
	// Insert just after os.Args[0] so cobra parses --dir as a root
	// flag before it dispatches to the subcommand.
	dirArg := "--dir=" + cfg.PBDataDir
	newArgs := make([]string, 0, len(os.Args)+1)
	newArgs = append(newArgs, os.Args[0], dirArg)
	newArgs = append(newArgs, os.Args[1:]...)
	os.Args = newArgs
}

// forceTCP4 returns true when the operator has opted in via
// QATLAS_FORCE_TCP4. Off by default so community deployments retain
// PocketBase's dual-stack v6 socket and serve both v4 + v6 callers out
// of one bind. Set to "1" / "true" / "yes" on hosts behind a v4-only
// portproxy (notably WSL2 + Windows netsh).
func forceTCP4() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("QATLAS_FORCE_TCP4")))
	switch v {
	case "1", "true", "yes", "on", "y", "t":
		return true
	default:
		return false
	}
}

// maybeIPv4Listener returns a tcp4-bound listener when addr is a literal
// IPv4 bind expression ("0.0.0.0:NNNN" or "127.0.0.1:NNNN" etc.). For
// hostnames, empty hosts, or IPv6 literals it returns (nil, nil) so the
// caller falls back to PocketBase's default tcp/v6 dual-stack listener.
//
// The motivation is WSL2 + Windows netsh portproxy: the v4-only forward
// rule from the host-side EasyTier mesh IP can't reach a v6-only socket
// (even with bindv6only=0) because Windows' portproxy forwards into the
// WSL2 NAT layer as raw v4 SYNs that need a real v4 listener.
//
// Only invoked when forceTCP4() returns true.
func maybeIPv4Listener(addr string) (net.Listener, error) {
	if addr == "" {
		return nil, nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", addr, err)
	}
	if host == "" {
		// ":4200" / ":http" — leave to PocketBase's default behavior.
		return nil, nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// Hostname like "localhost" — let net.Listen pick a family.
		return nil, nil
	}
	if ip.To4() == nil {
		// IPv6 literal — caller wants v6 explicitly, respect it.
		return nil, nil
	}
	tcpAddr, err := net.ResolveTCPAddr("tcp4", addr)
	if err != nil {
		return nil, err
	}
	return net.ListenTCP("tcp4", tcpAddr)
}

// loadDotEnv finds and loads the .env file for the running server.
// Returns the absolute path of the file loaded, or "" if none was found
// (also OK — the operator can still set env via systemd or shell).
//
// Resolution order (first hit wins):
//  1. $QATLAS_DOTENV — explicit override; required for systemd installs
//     where CWD is not the .env-containing dir.
//  2. ./.env — relative to CWD, for ad-hoc dev `qatlas-server serve`
//     invocations from the project directory.
//
// We deliberately do NOT walk up the filesystem looking for any .env —
// that's how a stray $HOME/.env from an unrelated tool can poison the
// process environment (anchor for relative paths, WIKI_DIR, etc.). If
// the operator needs the server to find a .env outside CWD, they must
// set $QATLAS_DOTENV explicitly.
//
// Once located, godotenv.Load is used with the "don't overwrite existing
// vars" semantic so an env var already set in the process environment
// (systemd, shell export, k8s ConfigMap) always wins. The .env is only
// a fallback / convenience for dev machines.
func loadDotEnv() string {
	candidates := []string{}
	if explicit := strings.TrimSpace(os.Getenv("QATLAS_DOTENV")); explicit != "" {
		candidates = append(candidates, explicit)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, ".env"))
	}

	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			if err := godotenv.Load(path); err != nil {
				slog.Warn("found .env but could not load it", "path", path, "error", err)
				return ""
			}
			absPath, err := filepath.Abs(path)
			if err != nil {
				absPath = path
			}
			slog.Info("loaded .env", "path", absPath)
			return absPath
		}
	}

	slog.Debug("no .env located; relying on process environment alone")
	return ""
}
