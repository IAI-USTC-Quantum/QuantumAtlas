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
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/neo4j"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/papers"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/routes"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/shares"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/wiki"
	qweb "github.com/IAI-USTC-Quantum/QuantumAtlas/web"

	_ "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/apidocs"

	"github.com/casbin/casbin/v2"
	"github.com/joho/godotenv"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	httpSwagger "github.com/swaggo/http-swagger/v2"
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

// @title          QuantumAtlas API
// @version        1.0
// @description    Go + PocketBase backend for QuantumAtlas. Read endpoints
// @description    (wiki/pages/stats/search/graph metadata/health) are public
// @description    because the wiki is an open repo; write endpoints require a
// @description    bearer token (PAT or PocketBase session) plus the matching
// @description    scope. See the auth model docs for the scope vocabulary.
//
// @contact.name   QuantumAtlas
// @contact.url    https://quantum-atlas.ai
//
// @BasePath       /
//
// @securityDefinitions.apikey BearerAuth
// @in                         header
// @name                       Authorization
// @description                "Bearer <token>" — token is either a PAT
// @description                (prefix qat_, created at /pat) or a PocketBase
// @description                session token (copied from /token). Session
// @description                tokens implicitly hold every scope.
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

	// Mount the `papers` subcommand group (catalog maintenance:
	// `papers sync` reconciles Neo4j has_pdf/has_md/image_count from the
	// object-store buckets — periodic drift repair + disaster rebuild).
	// Same registration timing constraint as pat / storage above.
	app.RootCmd.AddCommand(NewPapersCommand())

	// Mount the `openalex` subcommand group (bootstrap / sync the
	// OpenAlex works snapshot into the :PaperWork layer). Execution is
	// operator-driven and decoupled from server boot — see handoff.md.
	app.RootCmd.AddCommand(NewOpenAlexCommand())

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

	// Initialize the share-token store. When Neo4j is configured the
	// records live in the catalog (:PaperShareToken nodes); otherwise
	// they fall back to {DATA_DIR}/shares JSON files. Construction is
	// no-op-safe on every boot.
	nc := initNeo4jClient(cfg)
	shareStore, err := initShareStore(cfg, nc)
	if err != nil {
		log.Fatalf("init share store: %v", err)
	}

	// Build the Neo4j-backed papers catalog (stats / needs-mineru /
	// claims / upload write-through). nc may be nil/unconnected — the
	// catalog degrades gracefully (ErrCatalogUnavailable) in that case.
	catalog := papers.NewStore(nc)
	if catalog.Configured() {
		// Schema bootstrap runs in the background: it is ~16 sequential DDL
		// round-trips to the (cross-mesh, ~700ms-latency) Neo4j, which can
		// exceed any startup-blocking budget and would otherwise delay
		// /api/health. All statements are idempotent (IF NOT EXISTS), so we
		// retry with a generous per-attempt timeout until every constraint +
		// index exists. Missing schema degrades correctness (uniqueness) +
		// performance, so we keep retrying rather than wait for the next boot.
		go ensureCatalogSchema(catalog)
	} else {
		log.Printf("papers: catalog disabled (NEO4J_URI unset); /api/papers stats+queue report available:false")
	}

	// Wire the raw asset backend. S3Enabled is the documented split
	// point: when QATLAS_S3_* are all set we route every PDF /
	// markdown / image through three RustFS buckets (qatlas-pdf /
	// qatlas-md / qatlas-images) behind an objstore.Router, otherwise
	// we wrap cfg.RawDir with a single LocalStore (keys keep their
	// <kind>/ prefix in one dir). The config layer already validated
	// the all-or-nothing rule, so neither branch can land half-
	// configured here.
	rawStore, err := initRawStore(cfg)
	if err != nil {
		log.Fatalf("init raw object store: %v", err)
	}
	// Reconcile bucket versioning on each per-kind S3 bucket so
	// accidental overwrites are recoverable via ListObjectVersions.
	// Idempotent; non-fatal when the IAM user lacks the perm.
	ensureBucketVersioning(rawStore)

	// Build the casbin enforcer once at startup. The policy table is
	// static (defined in internal/pat/scopes.go), so a single shared
	// instance is enough — Enforce() is safe for concurrent reads.
	// Failing here is fatal: every write endpoint depends on it.
	enforcer, err := pat.NewEnforcer()
	if err != nil {
		log.Fatalf("build PAT scope enforcer: %v", err)
	}

	// Background janitor: sweep expired MinerU claims + share tokens
	// every JanitorInterval. Idempotent and safe to run on both edges.
	// Stops when the app terminates.
	janitorCtx, janitorCancel := context.WithCancel(context.Background())
	if catalog.Configured() {
		go catalog.RunJanitor(janitorCtx, shareStore)
	}
	app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		janitorCancel()
		return e.Next()
	})

	// Build the server-side MinerU converter. Always constructed; it only
	// actually converts when MINERU_API_TOKEN is set (Converter.Enabled).
	// Powers the silent conversion behind GET /api/papers/{id}/markdown.
	mineruConverter := mineru.NewConverter(cfg, rawStore, shareStore)
	if mineruConverter.Enabled() {
		log.Printf("mineru: server-side silent conversion enabled (base=%s)", cfg.MinerUAPIBaseURL)
		// When a dedicated MinerU-fetch endpoint is configured (e.g. an
		// edge whose own public endpoint isn't MinerU-trusted), presign
		// the PDF download URL against it instead of the regular store.
		if cfg.S3Enabled() && cfg.MinerUFetchEndpoint != "" {
			fetchStore, err := objstore.NewS3Store(
				cfg.MinerUFetchEndpoint, cfg.S3BucketPDF,
				cfg.S3AccessKeyID, cfg.S3SecretAccessKey,
			)
			if err != nil {
				log.Fatalf("init mineru-fetch presign store: %v", err)
			}
			mineruConverter.SetPDFFetchStore(fetchStore)
			log.Printf("mineru: PDF presign via fetch endpoint %s/%s", cfg.MinerUFetchEndpoint, cfg.S3BucketPDF)
		}
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

		registerRoutes(se, app, cfg, rawStore, shareStore, catalog, mineruConverter, wikiCache, enforcer, serverStarted)

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

// initNeo4jClient builds the long-lived Neo4j client shared by the
// papers catalog and the share-token store. It attempts an initial
// Connect (best-effort, short timeout) so the first request after boot
// doesn't pay the dial latency, but a down/unconfigured Neo4j is
// non-fatal: NewClient returns ErrNotConfigured when NEO4J_URI is
// empty, in which case we return nil and every catalog op degrades to
// "unavailable". A configured-but-unreachable Neo4j returns a client
// that lazily reconnects (backoff-gated) on later requests.
// ensureCatalogSchema applies the Neo4j constraints + indexes in the
// background, retrying until success. EnsureSchema is ~16 sequential DDL
// round-trips; over a cross-mesh link (~700ms/round-trip) the full pass
// can exceed 10s, so a single short startup-blocking attempt would
// silently leave the tail of the schema (e.g. the PaperShareToken
// uniqueness constraint) uncreated. Each attempt gets a generous timeout
// and statements are idempotent, so retries converge cheaply.
func ensureCatalogSchema(catalog *papers.Store) {
	const (
		attemptTimeout = 90 * time.Second
		retryDelay     = 30 * time.Second
		maxAttempts    = 10
	)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), attemptTimeout)
		err := catalog.EnsureSchema(ctx)
		cancel()
		if err == nil {
			log.Printf("papers: catalog schema ensured")
			return
		}
		slog.Warn("papers: EnsureSchema attempt failed; retrying",
			"attempt", attempt, "max", maxAttempts, "error", err)
		time.Sleep(retryDelay)
	}
	slog.Error("papers: EnsureSchema gave up after retries (will retry next boot)")
}

func initNeo4jClient(cfg *config.Config) *neo4j.Client {
	nc, err := neo4j.NewClient(cfg.Neo4jURI, cfg.Neo4jUser, cfg.Neo4jPassword, cfg.Neo4jDatabase)
	if err != nil {
		log.Printf("neo4j: not configured (%v); papers catalog + share tokens use file fallback", err)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if cErr := nc.Connect(ctx); cErr != nil {
		slog.Warn("neo4j: initial connect failed; will retry lazily", "uri", cfg.Neo4jURI, "error", cErr)
	} else {
		log.Printf("neo4j: connected (%s)", cfg.Neo4jURI)
	}
	return nc
}

// initShareStore returns a ready-to-use ShareStore. When a connected
// Neo4j client is available the records live in the catalog as
// :PaperShareToken nodes; otherwise they fall back to {DATA_DIR}/shares
// JSON files. The on-disk path is always constructed so a later Neo4j
// outage can still degrade to file storage.
func initShareStore(cfg *config.Config, nc *neo4j.Client) (*shares.Store, error) {
	return shares.NewNeo4jStore(filepath.Join(cfg.DataDir, "shares"), nc)
}

// initRawStore returns the objstore.Store backing raw paper assets.
// Selects between a single LocalStore (cfg.RawDir) and the v0.7.0
// three-bucket S3 split (qatlas-pdf / qatlas-md / qatlas-images behind
// an objstore.Router) based on cfg.S3Enabled().
//
// In S3 mode each kind gets its own NewS3StoreDual so presigned URLs
// point at cfg.S3PublicEndpoint while server↔RustFS traffic stays on
// the internal endpoint. The Router keys objects as "<kind>/<shard>/…"
// for the rest of the codebase and transparently strips the "<kind>/"
// prefix per bucket. The "json" kind is intentionally absent — v0.7.0
// drops paper metadata JSON — so it routes to nil (writes error, reads
// 404), which the upload handlers already treat as "no metadata".
//
// In local mode a single LocalStore keeps the "<kind>/" prefix as a
// subdirectory, preserving the dev-friendly single-RAW_DIR layout.
func initRawStore(cfg *config.Config) (objstore.Store, error) {
	if !cfg.S3Enabled() {
		log.Printf("raw store: local backend %s", cfg.RawDir)
		return objstore.NewLocalStore(cfg.RawDir)
	}
	dual := cfg.S3PublicEndpoint != "" && cfg.S3PublicEndpoint != cfg.S3Endpoint
	kinds := []struct {
		kind   string
		bucket string
	}{
		{"pdf", cfg.S3BucketPDF},
		{"markdown", cfg.S3BucketMD},
		{"images", cfg.S3BucketImages},
	}
	backends := make(map[string]objstore.Store, len(kinds))
	for _, k := range kinds {
		st, err := objstore.NewS3StoreDual(
			cfg.S3Endpoint, cfg.S3PublicEndpoint,
			k.bucket, cfg.S3AccessKeyID, cfg.S3SecretAccessKey,
		)
		if err != nil {
			return nil, fmt.Errorf("init S3 bucket %q: %w", k.bucket, err)
		}
		backends[k.kind] = st
		if dual {
			log.Printf("raw store: S3 backend %s/%s (presign via %s)", cfg.S3Endpoint, k.bucket, cfg.S3PublicEndpoint)
		} else {
			log.Printf("raw store: S3 backend %s/%s", cfg.S3Endpoint, k.bucket)
		}
	}
	return objstore.NewRouter(backends), nil
}

// ensureBucketVersioning reconciles bucket versioning on every S3
// backend behind rawStore so accidental overwrites are recoverable via
// ListObjectVersions. Handles both a bare *objstore.S3Store (local-dev
// edge case) and the production *objstore.Router (3 per-kind buckets).
// Idempotent + non-fatal: a missing s3:Put/GetBucketVersioning perm
// logs a warning and the server still serves (just without rollback
// safety).
func ensureBucketVersioning(rawStore objstore.Store) {
	var stores []*objstore.S3Store
	switch v := rawStore.(type) {
	case *objstore.S3Store:
		stores = append(stores, v)
	case *objstore.Router:
		stores = append(stores, v.S3Backends()...)
	}
	for _, s3Store := range stores {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		prior, changed, vErr := s3Store.EnsureVersioning(ctx)
		cancel()
		switch {
		case vErr != nil:
			slog.Warn("bucket versioning: reconcile failed; overwrites will not be recoverable",
				"bucket", s3Store.Bucket(), "error", vErr)
		case changed:
			log.Printf("bucket versioning: enabled (was: %q) on %s", prior, s3Store.Bucket())
		default:
			log.Printf("bucket versioning: already enabled on %s", s3Store.Bucket())
		}
	}
}

// registerRoutes wires the QuantumAtlas /api/* surface. Most endpoints are
// implemented under internal/routes/ and pulled in by their respective
// Register* helpers as we migrate each module in subsequent phases.
func registerRoutes(se *core.ServeEvent, app core.App, cfg *config.Config, rawStore objstore.Store, shareStore *shares.Store, catalog *papers.Store, mineruConverter *mineru.Converter, wikiCache *wiki.Cache, enforcer *casbin.Enforcer, started time.Time) {
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

	// /swagger/* — interactive OpenAPI (Swagger UI) for the /api surface.
	// The spec is generated from handler annotations by swaggo/swag into
	// internal/apidocs (blank-imported above to register it), and served
	// here via http-swagger. Public on purpose: it's API documentation,
	// not a write surface. doc.json is the raw OpenAPI 2.0 document.
	swaggerHandler := httpSwagger.Handler(httpSwagger.URL("/swagger/doc.json"))
	se.Router.GET("/swagger/{path...}", func(re *core.RequestEvent) error {
		swaggerHandler(re.Response, re.Request)
		return nil
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
	routes.RegisterWiki(se, cfg, wikiCache, enforcer)

	// Graph (Neo4j) — see internal/routes/graph.go. Gated by
	// authGuard + scopeGuard("graph", "read") so it matches the rest
	// of the non-public-repo surface; sessions bypass via ScopeMaster.
	routes.RegisterGraph(se, cfg, enforcer)

	// Papers (resources, upload, mineru-claim) — see internal/routes/papers.go.
	routes.RegisterPapers(se, cfg, rawStore, shareStore, catalog, mineruConverter, enforcer)

	// Shares CRUD + public /share/{token}* — see internal/routes/shares.go.
	routes.RegisterShares(se, cfg, shareStore, rawStore, enforcer)

	// Personal Access Tokens — see internal/routes/pat.go.
	// /api/pat is session-token-only (PAT auth refused by sessionGuard);
	// no enforcer needed because there's no scope-gated endpoint here.
	routes.RegisterPAT(se, app)
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
