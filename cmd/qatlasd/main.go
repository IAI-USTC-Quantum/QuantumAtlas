// Command qatlasd is the Go + PocketBase rewrite of the QuantumAtlas
// FastAPI server. It embeds PocketBase as a Go library and exposes the same
// /api/* surface that the existing Python CLI consumes.
//
// Usage:
//
//	qatlasd serve --http=0.0.0.0:4200
//	qatlasd migrate up
//	qatlasd superuser upsert <email> <password>
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

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/arxiv"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/healthz"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/neo4j"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/papers"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/routes"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/wiki"
	qweb "github.com/IAI-USTC-Quantum/QuantumAtlas/web"

	_ "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/apidocs"

	"github.com/casbin/casbin/v2"
	"github.com/joho/godotenv"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	pbcmd "github.com/pocketbase/pocketbase/cmd"
	"github.com/pocketbase/pocketbase/core"
	"github.com/spf13/cobra"
	httpSwagger "github.com/swaggo/http-swagger/v2"
)

// installScript is the POSIX shell installer served at
// /install-qatlasd.sh. It detects OS/arch, downloads the latest
// qatlasd release artifact from GitHub over HTTPS, installs to
// ~/.local/bin, and prints next-step pointers. Kept in a separate
// file so it can be edited as a real .sh (syntax highlighting +
// shellcheck) and reviewed standalone.
//
// The script does NOT do in-band SHA256SUMS verification — see the
// script body comment near the install step for the trust-model
// rationale (HTTPS covers in-transit; SHA256SUMS / SLSA attestation
// are opt-in stronger checks documented separately).
//
//go:embed install-qatlasd.sh
var installScript string

// Version is overridden at build time via:
//
//	go build -ldflags "-X main.Version=$(cat pyproject.toml ...)"
//
// Defaults to "dev" as a sentinel — if `qatlasd --version` or
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
// @description                "Bearer <token>" — two credential shapes
// @description                accepted: a Personal Access Token
// @description                (`Authorization: Bearer qat_...`, minted at
// @description                `/pat` after GitHub OAuth login), or the
// @description                env-loaded system PAT (set
// @description                `QATLAS_SYSTEM_PAT` on the server, send the
// @description                plaintext as `Authorization: Bearer <value>`).
// @description                Browser callers are authenticated through
// @description                pb.authStore (no copy step) — only non-browser
// @description                callers need an explicit bearer.
func main() {
	// Early --version / version short-circuit. Everything below
	// (loadDotEnv, config.Load, initNeo4jClient, initRawStore, ...)
	// runs in main() body BEFORE cobra parses os.Args, so a naked
	// `qatlasd --version` would otherwise trigger network I/O
	// (.env load, Neo4j connect attempt, S3 client init) before
	// printing the version. Detect the version flag at the top so the
	// command is cheap, side-effect-free, and dependency-free (no .env
	// required — useful in install-qatlasd.sh / CI smoke checks).
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "--version", "version":
			fmt.Printf("qatlasd version %s\n", Version)
			return
		}
	}

	// Early help short-circuit. Conservative detection: only triggers
	// on `qatlasd --help`, `qatlasd -h`, or `qatlasd help [...]` — i.e.
	// help requests aimed at the *root* command. Subcommand help like
	// `qatlasd pat --help` falls through to the normal flow (no risk
	// of fataling on broken .env because Load is now best-effort for
	// the S3 half-set case; see internal/config/config.go::Load).
	//
	// The narrow window avoids false positives like
	// `qatlasd pat mint --name "--help"` where `--help` is a flag value
	// and would otherwise skip env loading just before cobra tries to
	// actually execute the command with zero config.
	helpMode := len(os.Args) >= 2 && (os.Args[1] == "--help" || os.Args[1] == "-h" || os.Args[1] == "help")

	// Early `config` subcommand short-circuit. Same reason as the
	// --version one above, plus a critical extra: `qatlasd config
	// show` is the operator's tool for diagnosing a half-configured
	// .env, but config.Load() below fails fast on exactly that case
	// (validateS3Config rejects half-set S3 fields with a fatal exit).
	// If we let the normal main flow run first, config show could
	// never see a broken config — it would die before getting a chance
	// to display anything. So intercept `config` before any env
	// validation, build a minimal cobra root, and dispatch directly.
	if len(os.Args) >= 2 && os.Args[1] == "config" {
		root := &cobra.Command{
			Use:     "qatlasd",
			Version: Version,
		}
		root.AddCommand(NewConfigCommand())
		// Cobra's default behaviour on error is to print usage + the
		// error to stderr and return; mirror that with a non-zero exit
		// so scripts can detect "config init refused to overwrite" etc.
		if err := root.Execute(); err != nil {
			os.Exit(1)
		}
		return
	}

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
	//
	// Skipped in helpMode: `qatlasd --help` / `-h` / `help` build a
	// minimal cobra tree with a zero-value cfg just to print help text,
	// so they don't need (and shouldn't be blocked by) a working .env.
	var cfg *config.Config
	if helpMode {
		cfg = &config.Config{}
	} else {
		dotenvPath := loadDotEnv()
		var err error
		cfg, err = config.Load(dotenvPath)
		if err != nil {
			log.Fatalf("load config: %v", err)
		}
	}

	app := pocketbase.NewWithConfig(pocketbase.Config{
		// Custom SQLite tuning — see sqlite_tuning.go for the rationale
		// behind each pragma. Falls back to PB's DefaultDBConnect when
		// this returns an error (so a typo here can't brick startup
		// silently; PB will log and try the default path).
		DBConnect: qatlasDBConnect,
	})

	// Surface main.Version (set via -ldflags "-X main.Version=$VERSION")
	// to PocketBase's cobra root command so `qatlasd --version`
	// prints "qatlasd version 0.2.4" instead of the default
	// "qatlasd version (untracked)". Without this, the version
	// string we inject is only visible in /api/health's `data.version`
	// field — operators running `--version` on the CLI see nothing.
	app.RootCmd.Version = Version

	// Register the GitHub OAuth provider hook now (it fires at
	// PocketBase Bootstrap, which runs BEFORE cobra parses argv).
	// This is the timing reason GITHUB_CLIENT_* / *_GITHUB_LOGINS
	// are env-only — by the time `serve --github-client-id ...`
	// would land in cfg, the OAuth provider has already been mounted
	// onto the settings table. Documented in
	// cmd/qatlasd/serve_flags.go "Known limitation".
	auth.Register(app, cfg)

	// Mount the `pat` subcommand group. This MUST come before
	// app.Execute() — cobra binds commands by reference at parse time,
	// and any additions after the root walks os.Args are ignored.
	patCmd := NewPATCommand(app)
	attachPBLockProbe(patCmd, cfg)
	app.RootCmd.AddCommand(patCmd)

	// Mount the `storage` subcommand group (object-store maintenance:
	// `storage prune` enumerates and deletes noncurrent S3 versions).
	// Same registration timing constraint as pat above.
	//
	// Note: storage touches S3 buckets only, not pb_data SQLite, so we
	// skip the lock-probe wrapper here.
	app.RootCmd.AddCommand(NewStorageCommand())

	// Mount the `service` subcommand group (install / uninstall / start /
	// stop / restart / status — wraps kardianos/service to manage the
	// systemd unit / launchd plist / Windows SCM entry). Same timing
	// constraint as pat / storage above.
	//
	// Note: service touches systemd / launchd / Windows SCM only, not
	// pb_data SQLite — no lock-probe wrapper needed.
	app.RootCmd.AddCommand(NewServiceCommand())

	// Mount the `papers` subcommand group (catalog maintenance:
	// `papers sync` reconciles Neo4j has_pdf/has_md/image_count from the
	// object-store buckets — periodic drift repair + disaster rebuild).
	// Same registration timing constraint as pat / storage above.
	papersCmd := NewPapersCommand()
	attachPBLockProbe(papersCmd, cfg)
	app.RootCmd.AddCommand(papersCmd)

	// Mount the `openalex` subcommand group (bootstrap / sync the
	// OpenAlex works snapshot into the :PaperWork layer). Execution is
	// operator-driven and decoupled from server boot — see handoff.md.
	openalexCmd := NewOpenAlexCommand()
	attachPBLockProbe(openalexCmd, cfg)
	app.RootCmd.AddCommand(openalexCmd)

	// Mount the `users` subcommand group (`users list` enumerates the
	// PocketBase users collection — needed before `pat mint --user`
	// because each edge has an independent user store and there's
	// otherwise no non-browser way to discover which emails are
	// registered locally).
	usersCmd := NewUsersCommand(app)
	attachPBLockProbe(usersCmd, cfg)
	app.RootCmd.AddCommand(usersCmd)

	// Mount the `config` subcommand group (`config init` writes a
	// default .env template; `config path` / `config show` inspect
	// what qatlasd would load). Same cobra registration timing
	// constraint as the other subcommand mounts above.
	//
	// Note: config touches files only, not pb_data SQLite — no
	// lock-probe wrapper.
	app.RootCmd.AddCommand(NewConfigCommand())

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
		if changed, err := pat.EnsureDefaults(app); err != nil {
			// Non-fatal: starting the server without rate limits is
			// still better than refusing to start at all. Log loudly
			// so the operator notices.
			slog.Warn("pat: failed to install default rate limits", "error", err)
			return nil
		} else if changed {
			slog.Info("pat: installed default rate-limit rules", "rules", len(pat.DefaultRateLimitRules))
		}
		return nil
	})

	// Mount our wrapped `serve` subcommand: 20 qatlasd-specific flags
	// (cmd/qatlasd/serve_flags.go) on top of PocketBase's own --http /
	// --https / --origins / --dir / --encryptionEnv. The wrapper's
	// RunE pre-step applies any --foo flags the operator passed onto
	// cfg before any cfg-dependent backend init runs, so the CLI flag
	// → env → .env → default precedence actually takes effect (vs the
	// v0.17.0a0 design where most init ran in main() before the flag
	// values had been read).
	//
	// We also mount Superuser ourselves so we can skip pb.Start()
	// (which would mount its own Serve and clobber the wrapped one).
	app.RootCmd.AddCommand(pbcmd.NewSuperuserCommand(app))

	serveCmd := pbcmd.NewServeCommand(app, true)
	serveFlags := registerServeFlags(serveCmd)
	originalServeRunE := serveCmd.RunE
	serveCmd.RunE = func(cmd *cobra.Command, args []string) error {
		// ── STEP 1: merge CLI flags into cfg ───────────────────────
		applyServeFlags(cmd, serveFlags, cfg)
		// Enforce the S3 all-or-nothing invariant for the serve
		// path. config.Load only emits a slog.Warn for half-set S3
		// so non-serve subcommands (`qatlasd --help`, `pat list`,
		// etc.) tolerate a broken .env; serve cannot, because the
		// HTTP handlers would silently fall back to LocalStore.
		// This is also the first chance to catch a half-set
		// introduced by CLI flag overrides above.
		if err := validateServeCfgAfterFlags(cfg); err != nil {
			return err
		}

		// ── STEP 2: stamp the S3 client User-Agent. cfg.EdgeName
		// may have been touched by --edge-name. Must run before any
		// S3Store is built (initRawStore below).
		uaVersion := Version
		if cfg.EdgeName != "" {
			uaVersion = Version + "/" + cfg.EdgeName
		}
		objstore.SetClientAppInfo("qatlasd", uaVersion)

		// ── STEP 3: inject the PocketBase persistent flags
		// (--http / --dir) NOW, with the final cfg values. Done
		// post-flag-apply so --pb-data-dir / --http actually
		// reaches PocketBase. Order matters: PocketBase reads
		// os.Args in its eagerParseFlags routine which runs at
		// app.Execute() entry, after this RunE has fired. So
		// mutating os.Args here is the legal hook.
		injectHTTPFlag(cfg)
		injectPBDataDirFlag(cfg)

		// ── STEP 4: load the optional system PAT. --system-pat
		// mirrored its value into QATLAS_SYSTEM_PAT in
		// applyServeFlags above, so LoadSystemPAT sees the CLI
		// value here.
		if sysPAT, err := pat.LoadSystemPAT(); err != nil {
			return fmt.Errorf("system PAT: %w", err)
		} else if sysPAT != nil {
			routes.UseSystemPAT(sysPAT)
			slog.Info("system PAT enabled",
				"length", sysPAT.Length(),
				"scopes", sysPAT.Scopes(),
			)
		} else {
			slog.Info("system PAT disabled (QATLAS_SYSTEM_PAT unset)")
		}

		// ── STEP 5: build the cfg-dependent backends. All closures
		// captured by the OnServe handler below need these to be
		// in scope.
		nc := initNeo4jClient(cfg)
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
		// markdown / image through three RustFS buckets behind an
		// objstore.Router, otherwise we wrap cfg.RawDir with a single
		// LocalStore.
		rawStore, err := initRawStore(cfg)
		if err != nil {
			return fmt.Errorf("init raw object store: %w", err)
		}
		ensureBucketVersioning(rawStore)

		// Build the casbin enforcer once at startup. Failing here is
		// fatal: every write endpoint depends on it.
		enforcer, err := pat.NewEnforcer()
		if err != nil {
			return fmt.Errorf("build PAT scope enforcer: %w", err)
		}

		// Build the arxiv fetcher and OpenAlex resolver up front when
		// paper-access is on. Both are nil-safe pass-through: a nil
		// fetcher disables silent fetch (Ensure surfaces ErrFatal "no
		// PDF in store"); a nil/disabled resolver routes DOI lookups
		// to a 503. We always build the resolver value (even when
		// mailto is missing, Enabled() reports false) so the routes
		// can stay wired uniformly.
		var arxivFetcher *arxiv.Fetcher
		var doiResolver *openalex.Resolver
		if cfg.PaperAccessEnabled {
			contact := strings.TrimSpace(cfg.OpenAlexMailto)
			ua := arxiv.BuildUserAgent(Version, contact)
			fetcher, fetchErr := arxiv.New(arxiv.Config{
				UserAgent: ua,
				RPS:       int(cfg.ArxivFetchRPS), // rate.Limiter takes float internally; int rounding is fine for our scales
			})
			if fetchErr != nil {
				slog.Warn("arxiv fetcher disabled — invalid config; silent fetch will return ErrFatal on cache miss",
					"error", fetchErr,
				)
			} else {
				arxivFetcher = fetcher
			}
			doiResolver = openalex.New(openalex.Config{
				Mailto: contact,
			})
			if !doiResolver.Enabled() {
				slog.Warn("OpenAlex DOI resolver disabled: QATLAS_OPENALEX_MAILTO is unset; DOI paths will return 503")
			}
		}

		// Build the MinerU converter (always non-nil; behaves as a
		// no-op when QATLAS_PAPER_ACCESS_ENABLED is false). When
		// the operator opts in we emit ONE info line so deploy logs
		// make it obvious which markdown surface is live. Never logs
		// the API tokens themselves — only the COUNT.
		mineruConverter := mineru.NewConverter(
			mineru.ConverterConfig{
				PaperAccessEnabled:   cfg.PaperAccessEnabled,
				MinerUAPITokens:         cfg.MinerUAPITokens,
				MinerUAPIBaseURL:        cfg.MinerUAPIBaseURL,
				MinerUModelVersion:      cfg.MinerUModelVersion,
				MinerULanguage:          cfg.MinerULanguage,
				MinerUIsOCR:             cfg.MinerUIsOCR,
				MinerUEnableFormula:     cfg.MinerUEnableFormula,
				MinerUEnableTable:       cfg.MinerUEnableTable,
				MinerUPollInterval:      cfg.MinerUPollInterval,
				MinerUTimeout:           cfg.MinerUTimeout,
				MinerUMaxConcurrentJobs: cfg.MinerUMaxConcurrentJobs,
				S3PublicEndpoint:        cfg.S3PublicEndpoint,
				Fetcher:                 arxivFetcher,
				ArxivFetchConcurrent:    cfg.ArxivFetchConcurrent,
			},
			rawStore, catalog,
			slog.Default(),
		)
		if cfg.PaperAccessEnabled {
			serverSide := "disabled"
			if mineruConverter.Enabled() {
				serverSide = "enabled"
			}
			silentFetch := "disabled"
			if arxivFetcher != nil {
				silentFetch = "enabled"
			}
			doiState := "disabled"
			if doiResolver != nil && doiResolver.Enabled() {
				doiState = "enabled"
			}
			slog.Info("paper_access enabled",
				"endpoints", "[markdown,markdown_status,pdf,pdf_status]",
				"server_side_mineru", serverSide,
				"silent_fetch", silentFetch,
				"doi_resolver", doiState,
				"mineru_keys", mineruConverter.KeyRingSize(),
				"max_concurrent", mineruConverter.MaxConcurrentJobs(),
				"arxiv_fetch_concurrent", cfg.ArxivFetchConcurrent,
				"arxiv_fetch_rps", cfg.ArxivFetchRPS,
				"timeout_s", int(mineruConverter.Timeout().Seconds()),
			)
		}

		// Background janitor: sweep expired MinerU claims every
		// JanitorInterval. Idempotent and safe to run on both edges.
		janitorCtx, janitorCancel := context.WithCancel(context.Background())
		if catalog.Configured() {
			go catalog.RunJanitor(janitorCtx)
		}
		app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
			janitorCancel()
			return e.Next()
		})

		// Build the wiki in-memory cache. Walks cfg.WikiDir once at
		// startup so the first /api/pages request hits warm data.
		wikiCache := wiki.NewCache(cfg.WikiDir, 60*time.Second)
		log.Printf("wiki: cache initialized (dir=%s)", cfg.WikiDir)
		app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
			wikiCache.Stop()
			return e.Next()
		})

		// ── STEP 6: register the HTTP handlers + pb_data lock as
		// an OnServe hook. PocketBase fires the hook when
		// `apis.Serve` builds the router (originalServeRunE below).
		app.OnServe().BindFunc(func(se *core.ServeEvent) error {
			// Acquire the single-process lock on pb_data BEFORE any HTTP
			// listener starts. The kernel releases the flock on exit
			// (graceful, SIGTERM, kill -9, OOM), so a crashed qatlasd
			// never leaves a stale lock requiring manual cleanup.
			if pbDataLockSkipRequested() {
				slog.Warn("QATLAS_SKIP_PB_DATA_LOCK=1 — pb_data multi-process safety bypassed; corruption risk",
					"pb_data_dir", cfg.PBDataDir)
			} else {
				lock, lockErr := acquirePBDataLock(cfg.PBDataDir)
				if lockErr != nil {
					return fmt.Errorf("failed to acquire pb_data lock: %w", lockErr)
				}
				app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
					_ = lock.Unlock()
					return e.Next()
				})
			}

			serverStarted := time.Now()

			// Optional: force a tcp4-native listener via
			// QATLAS_FORCE_TCP4=1 / --force-tcp4 (WSL2 + Windows
			// netsh portproxy escape hatch).
			if forceTCP4() && se.Listener == nil && se.Server != nil {
				if l, lerr := maybeIPv4Listener(se.Server.Addr); lerr == nil && l != nil {
					se.Listener = l
					log.Printf("QATLAS_FORCE_TCP4=1: forced tcp4 listener on %s", se.Server.Addr)
				} else if lerr != nil {
					log.Printf("QATLAS_FORCE_TCP4=1 but listener bind failed: %v (falling back to PocketBase default)", lerr)
				}
			}

			registerRoutes(se, app, cfg, rawStore, catalog, wikiCache, enforcer, mineruConverter, doiResolver, arxivFetcher, serverStarted)

			// Serve the embedded SPA last as the catch-all. apis.Static's
			// indexFallback=true means any path that doesn't match a real
			// file falls back to /index.html — exactly the SPA-client-router
			// behavior the React app needs for /wiki, /graph, /token, etc.
			se.Router.GET("/{path...}", apis.Static(qweb.MustFS(), true))

			return se.Next()
		})

		return originalServeRunE(cmd, args)
	}
	app.RootCmd.AddCommand(serveCmd)

	if err := app.Execute(); err != nil {
		log.Fatal(err)
	}
}

// validateServeCfgAfterFlags re-runs the serve-time S3 invariants in
// case CLI flags touched part of the connection quartet after Load
// (where the half-set check is only a slog.Warn, so the operator can
// run `qatlasd --help` / `pat list` on a broken .env). Delegates to
// Config.ValidateForServe so there's one canonical implementation.
func validateServeCfgAfterFlags(cfg *config.Config) error {
	if err := cfg.ValidateForServe(); err != nil {
		return fmt.Errorf("after CLI flag overrides: %w", err)
	}
	return nil
}

// attachPBLockProbe wires an advisory pb_data lock-probe into every
// node of a cobra subcommand subtree via PersistentPreRun. Mutating
// subcommands (pat / users / papers / openalex) write directly to the
// pb_data SQLite store and would race with a concurrently-running
// serve instance; the probe emits a slog.Warn (NOT a fatal) when a
// serve appears to hold the lock, so operators at least see a hint in
// their log when they fire `qatlasd pat mint` at a live edge.
//
// PersistentPreRun (vs PreRun) propagates to descendants so this only
// has to be called once per subcommand group root; cobra runs the
// nearest-ancestor PersistentPreRun for each invocation.
//
// We chain rather than replace any pre-existing PersistentPreRun so
// that subcommand authors can still install their own without
// silently losing the probe.
func attachPBLockProbe(root *cobra.Command, cfg *config.Config) {
	prev := root.PersistentPreRun
	root.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		warnIfServeRunning(cfg.PBDataDir)
		if prev != nil {
			prev(cmd, args)
		}
	}
}

// initNeo4jClient builds the long-lived Neo4j client shared by the
// papers catalog. It attempts an initial Connect (best-effort, short
// timeout) so the first request after boot doesn't pay the dial
// latency, but a down/unconfigured Neo4j is non-fatal: NewClient
// returns ErrNotConfigured when NEO4J_URI is empty, in which case we
// return nil and every catalog op degrades to "unavailable". A
// configured-but-unreachable Neo4j returns a client that lazily
// reconnects (backoff-gated) on later requests.
// ensureCatalogSchema applies the Neo4j constraints + indexes in the
// background, retrying until success. EnsureSchema is ~14 sequential DDL
// round-trips; over a cross-mesh link (~700ms/round-trip) the full pass
// can exceed 10s, so a single short startup-blocking attempt would
// silently leave the tail of the schema uncreated. Each attempt gets a
// generous timeout and statements are idempotent, so retries converge
// cheaply.
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
		log.Printf("neo4j: not configured (%v); papers catalog uses file fallback", err)
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

// initShareStore was removed in v0.9.0 along with the /share/*
// surface (see RegisterPapers doc comment).

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
func registerRoutes(se *core.ServeEvent, app core.App, cfg *config.Config, rawStore objstore.Store, catalog *papers.Store, wikiCache *wiki.Cache, enforcer *casbin.Enforcer, mineruConverter *mineru.Converter, doiResolver *openalex.Resolver, arxivFetcher *arxiv.Fetcher, started time.Time) {
	probes := healthz.Probes{
		Cfg:      cfg,
		RawStore: rawStore,
		Version:  Version,
		Started:  started,
	}

	// X-Attribution — declare upstream data sources on every /api/*
	// response. OpenAlex and Crossref both ship under CC0 1.0, which
	// does NOT require attribution legally, but OurResearch (OpenAlex)
	// and Crossref are non-profit infrastructure that depend on
	// visible attribution for grant funding; the broader open-data
	// community treats this as basic etiquette. arXiv is listed
	// because every paper entry in the catalog originated there.
	//
	// Registered BEFORE the /api/health override below so even the
	// short-circuited health response carries the header (the health
	// BindFunc returns without calling e.Next() — if X-Attribution
	// ran after it, the header would be missing on /api/health).
	//
	// Scope is /api/* only: SPA assets, /install-qatlasd.sh, /swagger
	// aren't API responses in the contract sense. Footer in the SPA
	// chrome covers the human-readable attribution side.
	se.Router.BindFunc(func(re *core.RequestEvent) error {
		if strings.HasPrefix(re.Request.URL.Path, "/api/") {
			re.Response.Header().Set("X-Attribution", "OpenAlex (CC0), Crossref (CC0), arXiv")
		}
		return re.Next()
	})

	// X-Qatlas-Server-Version — advertise this server's build version
	// on every /api/* response so the qatlas CLI client can do a
	// "client must be >= server" semver check (major+minor) and warn
	// on read ops / hard-fail on write ops when the local client is
	// older than the server it's talking to. Old clients that don't
	// look at this header simply ignore it (no contract change).
	// Old servers don't emit the header at all; new clients treat
	// "no header" as "unknown server version, skip negotiation"
	// — preserving forward compatibility for new clients hitting
	// pre-version-header deployments.
	se.Router.BindFunc(func(re *core.RequestEvent) error {
		if strings.HasPrefix(re.Request.URL.Path, "/api/") {
			re.Response.Header().Set("X-Qatlas-Server-Version", Version)
		}
		return re.Next()
	})

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
			result := healthz.RunPB(re.Request.Context(), probes)
			// Inject converter counters when asset downloads are enabled.
			if cfg.PaperAccessEnabled {
				result.Data.MinerU = mineruConverter.Snapshot()
			}
			// Anonymous callers get a sanitised payload: just
			// status / version / uptime / per-check status. Strips
			// bucket names, mesh endpoints, wiki commit info, MinerU
			// counters, and other deployment-topology fingerprints.
			// Authenticated callers (system PAT or session JWT) see
			// the full detail useful for dashboards. See healthz
			// package doc § "Privacy tiers" for the full rationale.
			if !routes.IsCallerAuthenticated(re) {
				result = result.Sanitise()
			}
			return re.JSON(200, result)
		}
		return re.Next()
	})

	// /install-qatlasd.sh — public installer that downloads the latest
	// qatlasd binary from GitHub releases. Serves the static script
	// verbatim. Caching: short max-age so a fresh release is picked
	// up within ~5 min of cutover but we don't hammer the server for
	// every curl|sh.
	//
	// Note: there is NO redirect from the old /install-server.sh URL
	// (it 404s through the SPA catch-all). The rename in v0.12.0 was
	// deliberate — keeping a redirect would mean operators discover
	// the new name only when they happen to bypass the redirect, and
	// the only-version-bumped-after-v0.10.0 docs already point at
	// /install-qatlasd.sh.
	se.Router.GET("/install-qatlasd.sh", func(re *core.RequestEvent) error {
		re.Response.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		re.Response.Header().Set("Cache-Control", "public, max-age=300")
		_, err := re.Response.Write([]byte(installScript))
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
	// directly; non-browser callers mint a PAT at /pat or use the
	// QATLAS_SYSTEM_PAT env on the server.)

	// Wiki / pages / stats / search / lint — see internal/routes/wiki.go.
	routes.RegisterWiki(se, cfg, wikiCache, enforcer)

	// Graph (Neo4j) — see internal/routes/graph.go. Gated by
	// authGuard + scopeGuard("graph", "read") so it matches the rest
	// of the non-public-repo surface; sessions bypass via ScopeMaster.
	routes.RegisterGraph(se, cfg, enforcer)

	// Papers (stats, needs-mineru, mineru-claim, uploads) — see
	// internal/routes/papers.go. v0.9.0 dropped the byte-serving
	// endpoints (markdown / resources / shares); the server only
	// exposes catalog metadata + the contribution flow by default.
	// /markdown + /markdown/status come back when the operator opts
	// in via QATLAS_PAPER_ACCESS_ENABLED=true.
	routes.RegisterPapers(se, cfg, rawStore, catalog, enforcer, mineruConverter, doiResolver, arxivFetcher)

	// Personal Access Tokens — see internal/routes/pat.go.
	// /api/pat is session-token-only (PAT auth refused by sessionGuard);
	// no enforcer needed because there's no scope-gated endpoint here.
	routes.RegisterPAT(se, app)

	// OAuth 2.0 Device Authorization Grant (RFC 8628) — see
	// internal/routes/oauthdevice.go. Lets `qatlas auth login --device`
	// mint a PAT without a local browser (poll-based flow). /code
	// and /token are anonymous; /lookup, /approve, /deny require a
	// browser session (sessionGuard, same as /api/pat).
	routes.RegisterOAuthDevice(se, app)

	// RAG (vector search) reverse_proxy to a sidecar — registered iff
	// QATLAS_PAPER_ACCESS_ENABLED=true AND QATLAS_RAG_SIDECAR_URL is
	// set. Same posture as /api/papers/{id}/markdown: serves derivative
	// paper content, so we gate behind the operator's opt-in.
	routes.RegisterRAG(se, cfg, enforcer)
}

// injectHTTPFlag mutates os.Args to add --http=<addr> when the user invokes
// the "serve" subcommand without supplying their own --http. This lets a
// plain `qatlasd serve` pick up QATLAS_SERVER_HOST/PORT from .env.
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
//	qatlasd serve
//
// equivalent to
//
//	qatlasd --dir=$HOME/.local/share/qatlasd/pb_data serve
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
//  2. ./.env — relative to CWD, for ad-hoc dev `qatlasd serve`
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
