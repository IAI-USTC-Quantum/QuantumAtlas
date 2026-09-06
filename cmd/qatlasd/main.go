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
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/agentic"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/arxiv"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/events"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/healthz"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/hostapi"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/ingest"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalexcorpus"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"
	qplugin "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/plugin"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/rag"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/routes"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/usage"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/userkeys"
	qweb "github.com/IAI-USTC-Quantum/QuantumAtlas/web"

	_ "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/apidocs"

	"github.com/casbin/casbin/v2"
	"github.com/jackc/pgx/v5/pgxpool"
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
// @description    Go + PocketBase backend for QuantumAtlas: paper
// @description    collection + search + database. Write endpoints and
// @description    the search/paper read surface require a bearer token
// @description    (PAT or PocketBase session) plus the matching scope.
// @description    See the auth model docs for the scope vocabulary.
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
// @description                config-loaded system PAT (set
// @description                `system_pat.token` in config.yaml on the
// @description                server, send the plaintext as
// @description                `Authorization: Bearer <value>`).
// @description                Browser callers are authenticated through
// @description                pb.authStore (no copy step) — only non-browser
// @description                callers need an explicit bearer.
func main() {
	// Early --version / version short-circuit. Everything below
	// (config.Load, initPostgresPool, initRawStore, ...) runs in main()
	// body BEFORE cobra parses os.Args, so a naked `qatlasd --version`
	// would otherwise trigger file I/O (config load, PostgreSQL pool
	// init, S3 client init) before printing the version. Detect the
	// version flag at the top so the command is cheap, side-effect-free,
	// and dependency-free (no config file required — useful in
	// install-qatlasd.sh / CI smoke checks).
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
	// of fataling on a half-configured file because Load is best-effort
	// for the S3 half-set case; see internal/config/config.go::Load).
	//
	// The narrow window avoids false positives like
	// `qatlasd pat mint --name "--help"` where `--help` is a flag value
	// and would otherwise skip config loading just before cobra tries to
	// actually execute the command with zero config.
	helpMode := len(os.Args) >= 2 && (os.Args[1] == "--help" || os.Args[1] == "-h" || os.Args[1] == "help")

	// Early `config` subcommand short-circuit. `qatlasd config init`
	// must work when no config file exists yet (it creates one), and
	// `config show` is the operator's tool for diagnosing a broken
	// config — but the normal main flow below fails fast on exactly
	// those states. So intercept `config` before any config loading,
	// build a minimal cobra root, and dispatch directly.
	if firstPositionalIsConfig(os.Args[1:]) {
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

	// Load the YAML config file. The path comes from the --config flag
	// (scanned manually here because cobra hasn't parsed os.Args yet)
	// or the default ~/.qatlas/config.yaml. A missing file, malformed
	// YAML, or any configuration-shaped environment variable (QATLAS_*,
	// MINERU_*, GITHUB_CLIENT_*, ...) is a hard fatal — environment-
	// variable configuration was removed; the file is the only source
	// of truth.
	//
	// Skipped in helpMode: `qatlasd --help` / `-h` / `help` build a
	// minimal cobra tree with a zero-value cfg just to print help text,
	// so they don't need (and shouldn't be blocked by) a config file.
	var cfg *config.Config
	if helpMode {
		cfg = &config.Config{}
	} else {
		var err error
		cfg, err = config.Load(configPathFromArgs(os.Args))
		if err != nil {
			log.Fatalf("load config: %v", err)
		}
	}

	// Inject the PocketBase runtime flags (--http / --dir) into os.Args
	// BEFORE PocketBase reads them. Timing matters:
	//   - --dir is consumed by PocketBase's eagerParseFlags inside
	//     NewWithConfig (it builds BaseApp with the parsed dataDir), so
	//     it MUST be in os.Args before the constructor below runs;
	//   - --http is parsed by cobra at app.Execute() time, so being in
	//     os.Args before Execute is sufficient.
	// Both no-op when the operator already passed the flag explicitly
	// (docker's CMD does) or when cfg carries no value (helpMode).
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
	// to PocketBase's cobra root command so `qatlasd --version`
	// prints "qatlasd version 0.2.4" instead of the default
	// "qatlasd version (untracked)". Without this, the version
	// string we inject is only visible in /api/health's `data.version`
	// field — operators running `--version` on the CLI see nothing.
	app.RootCmd.Version = Version

	// The --config flag selects the YAML config file. Config loading
	// happens above (before cobra parses os.Args, via configPathFromArgs),
	// so this registration exists only to make the flag show up in
	// --help output and to keep cobra from rejecting it as unknown.
	app.RootCmd.PersistentFlags().String("config", "",
		"Path to the YAML config file (default: ~/.qatlas/config.yaml)")

	// Register the GitHub OAuth provider hook now (it fires at
	// PocketBase Bootstrap, which runs BEFORE cobra parses argv).
	// This is why OAuth credentials must come from the config file
	// loaded in main() above — by the time any subcommand flag could
	// land in cfg, the OAuth provider has already been mounted onto
	// the settings table.
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
	// `papers sync` reconciles PostgreSQL has_pdf/has_md/image_count from the
	// object-store buckets — periodic drift repair + disaster rebuild).
	// Same registration timing constraint as pat / storage above.
	papersCmd := NewPapersCommand()
	attachPBLockProbe(papersCmd, cfg)
	app.RootCmd.AddCommand(papersCmd)

	// Mount the `downloader` subcommand group (`downloader probe` — the
	// live robustness harness for the acquisition ladder).
	downloaderCmd := NewDownloaderCommand()
	attachPBLockProbe(downloaderCmd, cfg)
	app.RootCmd.AddCommand(downloaderCmd)

	// Mount the `openalex` subcommand group (bootstrap / query the
	// OpenAlex works corpus in PostgreSQL). Execution is
	// operator-driven and decoupled from server boot.
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

	// Mount our wrapped `serve` subcommand on top of PocketBase's own
	// --http / --https / --origins / --dir / --encryptionEnv flags —
	// the only runtime overrides left now that the YAML config file is
	// the single source of truth (docker's CMD uses --http / --dir).
	// The wrapper's RunE pre-step validates cfg and wires the
	// cfg-dependent backends before the original serve RunE starts the
	// HTTP listener.
	//
	// We also mount Superuser ourselves so we can skip pb.Start()
	// (which would mount its own Serve and clobber the wrapped one).
	app.RootCmd.AddCommand(pbcmd.NewSuperuserCommand(app))

	serveCmd := pbcmd.NewServeCommand(app, true)
	originalServeRunE := serveCmd.RunE
	serveCmd.RunE = func(cmd *cobra.Command, args []string) error {
		// ── STEP 1: enforce the S3 all-or-nothing invariant for the
		// serve path. config.Load only emits a slog.Warn for half-set
		// S3 so non-serve subcommands (`qatlasd --help`, `pat list`,
		// etc.) tolerate a broken config; serve cannot, because the
		// HTTP handlers would silently fall back to LocalStore.
		if err := cfg.ValidateForServe(); err != nil {
			return fmt.Errorf("config validation: %w", err)
		}

		// ── STEP 2: stamp the S3 client User-Agent. Must run before
		// any S3Store is built (initRawStore below).
		uaVersion := Version
		if cfg.EdgeName != "" {
			uaVersion = Version + "/" + cfg.EdgeName
		}
		objstore.SetClientAppInfo("qatlasd", uaVersion)

		// ── STEP 3: the PocketBase runtime flags (--http / --dir) were
		// already injected into os.Args in main() before PocketBase
		// parsed them (see injectHTTPFlag / injectPBDataDirFlag); any
		// explicit operator flags won over the cfg values there.

		// ── STEP 4: load the optional system PAT from the config
		// file's system_pat section.
		if sysPAT, err := pat.LoadSystemPAT(cfg.SystemPATToken, cfg.SystemPATScopes); err != nil {
			return fmt.Errorf("system PAT: %w", err)
		} else if sysPAT != nil {
			routes.UseSystemPAT(sysPAT)
			slog.Info("system PAT enabled",
				"length", sysPAT.Length(),
				"scopes", sysPAT.Scopes(),
			)
		} else {
			slog.Info("system PAT disabled (system_pat.token unset)")
		}

		// ── STEP 5: build the cfg-dependent backends. All closures
		// captured by the OnServe handler below need these to be
		// in scope.
		pgPool, err := initPostgresPool(cfg)
		if err != nil {
			return err
		}
		if pgPool != nil {
			app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
				pgPool.Close()
				return e.Next()
			})
		}
		registryStore := registry.NewStore(pgPool)
		// The OpenAlex corpus (ADR 0006) lives in the SAME database as the
		// paper registry, so it shares the registry pool — no separate DSN. The
		// /api/papers/lookup resolver reads it by id (ADR 0007); when pgPool is
		// nil (local dev / no DSN) the corpus reports unavailable and lookup
		// degrades gracefully (resolved=false, corpus_available=false).
		corpus := openalexcorpus.NewStore(pgPool)
		if registryStore.Configured() {
			// Schema migration runs in the background: registry.Migrate is a
			// series of goose DDL round-trips to the registry database, which can
			// exceed any startup-blocking budget and would otherwise delay
			// /api/health. We retry with a generous per-attempt timeout until the
			// schema is at the latest bundled version. One failure mode is NOT
			// retried: a database NEWER than the binary (ErrSchemaTooNew) can
			// never converge, so it is fatal.
			//
			// The OpenAlex corpus BASE schema is created here too (openalex_works
			// + sync-state + audit; the pgvector-guarded work_embeddings is a
			// no-op without the extension). This makes openalex_works exist at
			// boot so the corpus can be populated lazily (fetch-on-miss
			// write-through, ADR 0006) — the bulk `openalex bootstrap-pg` is only
			// an optional pre-warm, no longer a prerequisite.
			//
			// The HEAVY openalex_works indexes are built in a second phase
			// CONCURRENTLY (never a boot-time SHARE lock on the 353 GB table)
			// and only when postgres.corpus_ensure_indexes is true — an edge
			// pointing at a pre-indexed corpus sets it false (ADR 0013).
			go ensureCatalogSchema(pgPool, corpus, cfg.CorpusEnsureIndexes)
		} else {
			log.Printf("papers: registry disabled (postgres.dsn unset); /api/papers stats+queue report available:false")
		}

		// Wire the raw asset backend. S3Enabled is the documented split
		// point: when the s3 config section is fully set we route every PDF /
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
				slog.Warn("OpenAlex DOI resolver disabled: paper_access.openalex_mailto is unset; DOI paths will return 503")
			}
		}

		// qatlas-rag index-push client (rag.remote). nil when disabled;
		// the converter and the ingester treat a nil pusher as "no push"
		// and a push failure as best-effort (log only, never fatal).
		ragClient := buildRAGRemoteClient(cfg)
		// Go typed-nil trap: a nil *rag.RemoteClient assigned to an
		// interface yields a NON-nil interface, defeating the nil
		// checks at both call sites and turning the best-effort push
		// into a SIGSEGV. Keep the interfaces genuinely nil.
		var (
			mineruPusher mineru.IndexPusher
			ingestPusher ingest.IndexPusher
		)
		if ragClient != nil {
			mineruPusher = ragClient
			ingestPusher = ragClient
		}

		// Build the MinerU converter (always non-nil; behaves as a
		// no-op when paper_access.enabled is false). When
		// the operator opts in we emit ONE info line so deploy logs
		// make it obvious which markdown surface is live. Never logs
		// the API tokens themselves — only the COUNT.
		mineruConverter := mineru.NewConverter(
			mineru.ConverterConfig{
				PaperAccessEnabled:      cfg.PaperAccessEnabled,
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
				Fetcher:                 arxivFetcher,
				ArxivFetchConcurrent:    cfg.ArxivFetchConcurrent,
				IndexPusher:             mineruPusher,
			},
			rawStore, registryStore,
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

		// Daily 00:00 auto-conversion: each local midnight the scheduler
		// walks the registry needs-mineru queue (PDF present, markdown
		// missing) and drives Converter.Ensure per row until the queue
		// drains, the converter is disabled, or today's MinerU quota /
		// token pool is exhausted. Everything is re-evaluated fresh at
		// every tick, so a quota-exhausted day retries automatically at
		// the next midnight. Only built when paper access is on; the
		// scheduler also self-no-ops when the converter is disabled.
		var mineruScheduler *mineru.Scheduler
		if cfg.PaperAccessEnabled {
			mineruScheduler = mineru.NewScheduler(mineruConverter, registryStore, slog.Default())
			mineruScheduler.Start(context.Background())
			// Boot kick: don't make operators wait for the next midnight
			// tick after a (re)deploy — if the queue has work, start
			// today's batch immediately. Coalesced by the scheduler's
			// own singleflight; no-ops when the converter is disabled.
			go func() {
				started, reason := mineruScheduler.RunNow(context.Background())
				slog.Info("mineru scheduler boot kick", "started", started, "reason", reason)
			}()
			app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
				mineruScheduler.Stop()
				return e.Next()
			})
		}

		// Background janitor: sweep expired MinerU leases every
		// JanitorInterval. Idempotent and safe to run on both edges.
		janitorCtx, janitorCancel := context.WithCancel(context.Background())
		if registryStore.Configured() {
			go registryStore.RunJanitor(janitorCtx)
		}
		app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
			janitorCancel()
			return e.Next()
		})

		// Lazy-ingestion pipeline: every pending paper is handed to the
		// ingester, which fetches an arXiv PDF directly or resolves a
		// DOI-only hit through OpenAlex to an arXiv twin / open-access PDF.
		// Recording the asset flips the paper to 'ready', then pushes an
		// index build to qatlas-rag when configured. A nil arxiv fetcher
		// disables ingestion; Shutdown drains the queue on terminate.
		ingester := ingest.New(registryStore, arxivFetcher, rawStore,
			ingest.WithIndexPusher(ingestPusher),
			ingest.WithDOIResolver(doiResolver),
			ingest.WithPDFReadyHook(func(ctx context.Context, canonical string, isDOI bool) {
				if isDOI {
					mineruConverter.EnsureByDOI(ctx, canonical, "")
					return
				}
				mineruConverter.Ensure(ctx, canonical)
			}),
		)
		if cfg.PaperAccessEnabled && registryStore.Configured() {
			go func() {
				recovered, recoverErr := ingester.RecoverPending(context.Background())
				if recoverErr != nil {
					slog.Warn("ingest: pending recovery failed", "recovered", recovered, "error", recoverErr)
					return
				}
				slog.Info("ingest: pending recovery queued", "papers", recovered)
			}()
		}
		app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
			shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := ingester.Shutdown(shutCtx); err != nil {
				slog.Warn("ingest: shutdown drain incomplete", "error", err)
			}
			return e.Next()
		})

		// Robust downloader (POST /api/downloader/*): the multi-paradigm
		// acquisition ladder (arXiv → OA APIs → publisher patterns →
		// landing page → agent fallback) with per-attempt traces. Built
		// under the same master switch as the rest of paper access; the
		// agent fallback needs downloader.agent.* when enabled.
		var downloaderModule *downloader.Downloader
		if cfg.PaperAccessEnabled && cfg.DownloaderEnabled {
			unpaywallEmail := cfg.DownloaderUnpaywallEmail
			if unpaywallEmail == "" {
				unpaywallEmail = cfg.OpenAlexMailto
			}
			downloaderModule = downloader.New(registryStore, rawStore, arxivFetcher, doiResolver, downloader.Config{
				Concurrency:    cfg.DownloaderConcurrency,
				UnpaywallEmail: unpaywallEmail,
				S2APIKey:       cfg.DownloaderS2APIKey,
				Fetch: downloader.FetchConfig{
					RequestTimeout: 60 * time.Second,
					RespectRobots:  cfg.DownloaderRespectRobots,
				},
				Agent: downloader.AgentConfig{
					Backend:      cfg.DownloaderAgentBackend,
					BaseURL:      cfg.DownloaderAgentBaseURL,
					APIKey:       cfg.DownloaderAgentAPIKey,
					Model:        cfg.DownloaderAgentModel,
					MaxTokens:    cfg.DownloaderAgentMaxTokens,
					ClaudeBin:    cfg.DownloaderAgentClaudeBin,
					ClaudeModel:  cfg.DownloaderAgentClaudeModel,
					Timeout:      cfg.DownloaderAgentTimeout,
					MaxBudgetUSD: cfg.DownloaderAgentMaxBudgetUSD,
				},
			},
				downloader.WithIndexPusher(ingestPusher),
				downloader.WithPDFReadyHook(func(ctx context.Context, canonical string, isDOI bool) {
					if isDOI {
						mineruConverter.EnsureByDOI(ctx, canonical, "")
						return
					}
					mineruConverter.Ensure(ctx, canonical)
				}),
			)
			agentState := "off"
			if cfg.DownloaderAgentBackend != "" {
				agentState = cfg.DownloaderAgentBackend
			}
			slog.Info("downloader enabled",
				"concurrency", cfg.DownloaderConcurrency,
				"respect_robots", cfg.DownloaderRespectRobots,
				"unpaywall", unpaywallEmail != "",
				"agent", agentState,
			)
			app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
				shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := downloaderModule.Shutdown(shutCtx); err != nil {
					slog.Warn("downloader: shutdown drain incomplete", "error", err)
				}
				return e.Next()
			})
		}
		// Typed-nil trap: keep the routes-facing interface genuinely nil
		// when the module is off so its 503 branch works.
		var downloaderRoutes routes.Downloader
		if downloaderModule != nil {
			downloaderRoutes = downloaderModule
		}

		// Multi-provider search engine (POST /api/search). Providers come
		// from search.providers (default catalog,arxiv,openalex);
		// "remote" additionally requires the qatlas-search microservice
		// (search.remote). remoteProvider is held separately too:
		// POST /api/search/agentic talks to it directly (metered,
		// agent=true).
		remoteProvider := buildRemoteProvider(cfg)
		searchEngine := buildSearchEngine(cfg, pgPool, registryStore, ingester, remoteProvider)

		// Optional local agentic backend (search.agentic.backend: local) —
		// the claude-CLI runner behind POST /api/search/agentic, replacing
		// the remote microservice when configured. nil when disabled or
		// when the claude binary is unusable (endpoint then 503s unless
		// remote stays the selected backend).
		localAgentic := buildLocalAgentic(cfg, searchEngine)

		// Usage metering store for the agentic-search endpoint and the
		// /api/admin/usage|plans|quotas surface. Shares the registry
		// Postgres pool; nil-pool → the usual 503 catalog convention.
		usageStore := usage.NewStore(pgPool)

		// ── STEP 6: register the HTTP handlers + pb_data lock as
		// an OnServe hook. PocketBase fires the hook when
		// `apis.Serve` builds the router (originalServeRunE below).
		app.OnServe().BindFunc(func(se *core.ServeEvent) error {
			// Acquire the single-process lock on pb_data BEFORE any HTTP
			// listener starts. The kernel releases the flock on exit
			// (graceful, SIGTERM, kill -9, OOM), so a crashed qatlasd
			// never leaves a stale lock requiring manual cleanup.
			if cfg.SkipPBDataLock {
				slog.Warn("skip_pb_data_lock: true — pb_data multi-process safety bypassed; corruption risk",
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

			// Optional: force a tcp4-native listener via force_tcp4: true
			// (WSL2 + Windows netsh portproxy escape hatch).
			if cfg.ForceTCP4 && se.Listener == nil && se.Server != nil {
				if l, lerr := maybeIPv4Listener(se.Server.Addr); lerr == nil && l != nil {
					se.Listener = l
					log.Printf("force_tcp4: forced tcp4 listener on %s", se.Server.Addr)
				} else if lerr != nil {
					log.Printf("force_tcp4 but listener bind failed: %v (falling back to PocketBase default)", lerr)
				}
			}

			registerRoutes(se, app, cfg, rawStore, registryStore, corpus, searchEngine, remoteProvider, ragClient, localAgentic, usageStore, enforcer, mineruConverter, mineruScheduler, ingester, doiResolver, arxivFetcher, downloaderRoutes, serverStarted)

			// Docs sites (/doc public, /devdoc behind the admin ticket
			// gate): disk override under ~/.qatlas/docs first, embedded
			// bundle as the baseline — see internal/routes/docs.go. A
			// docs refresh via deploy/update-docs.sh needs NO restart.
			// Registered before the SPA catch-all so the more specific
			// patterns win and the static devdoc tree is never served
			// unauthenticated.
			distFS := qweb.MustFS()
			docsRoot := routes.DefaultDocsRoot()
			docFS, docSrc := routes.ResolveDocsFS(distFS, docsRoot, "doc")
			devdocFS, devdocSrc := routes.ResolveDocsFS(distFS, docsRoot, "devdoc")
			log.Printf("docs: serving /doc from %s, /devdoc from %s (override dir %s)",
				docSrc, devdocSrc, docsRoot)
			routes.RegisterDoc(se, docFS)
			routes.RegisterDevdoc(se, cfg, devdocFS)

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

// configPathFromArgs extracts the --config flag value from raw argv.
// Config loading happens in main() BEFORE cobra parses os.Args (the
// loaded cfg feeds auth.Register and every subcommand), so the flag
// can't go through cobra's normal binding. Both spellings are
// recognised: `--config /path` and `--config=/path`. Returns "" when
// the flag is absent (config.Load then uses ~/.qatlas/config.yaml).
func configPathFromArgs(args []string) string {
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "--config" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, "--config=") {
			return strings.TrimPrefix(a, "--config=")
		}
	}
	return ""
}

// firstPositionalIsConfig reports whether the first positional argument
// (skipping the --config flag and its value) is "config". Used for the
// early `qatlasd config` interception, which must also fire when the
// operator writes `qatlasd --config /x.yaml config show`.
func firstPositionalIsConfig(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--config" {
			i++ // skip the flag's value
			continue
		}
		if strings.HasPrefix(a, "--config=") {
			continue
		}
		return a == "config"
	}
	return false
}

// loadConfig resolves the config path from os.Args (--config) and loads
// the YAML config. Shared by the operator subcommands (papers / openalex
// / storage) that need a *config.Config outside the main() flow.
func loadConfig() (*config.Config, error) {
	return config.Load(configPathFromArgs(os.Args))
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
		warnIfServeRunning(cfg)
		if prev != nil {
			prev(cmd, args)
		}
	}
}

// initPostgresPool builds the catalog database pool for non-login paper
// state. A missing DSN is allowed (local dev / graph-only deployments);
// a syntactically invalid DSN is fatal because the operator explicitly
// configured the catalog. Connectivity is probed once but failures are
// not fatal: writes degrade with X-Catalog-Sync: deferred and the pool
// can recover when PostgreSQL comes back.
func initPostgresPool(cfg *config.Config) (*pgxpool.Pool, error) {
	if cfg.PostgresDSN == "" {
		return nil, nil
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.PostgresDSN)
	if err != nil {
		return nil, fmt.Errorf("postgres catalog DSN: %w", err)
	}
	if cfg.PostgresMaxConns > 0 {
		poolCfg.MaxConns = int32(cfg.PostgresMaxConns)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	if err != nil {
		return nil, fmt.Errorf("postgres catalog pool: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		slog.Warn("postgres catalog: initial connect failed; requests will retry via pool",
			"error", err)
	} else {
		log.Printf("postgres catalog: connected")
	}
	return pool, nil
}

// ensureCatalogSchema provisions the PostgreSQL schema in the background in
// two phases. Phase 1 (fast, retried) applies the paper-registry goose
// migrations (registry.Migrate) and the OpenAlex corpus base schema; the
// corpus base schema is ensured first so openalex_works exists before any
// lazy fetch-on-miss write. Phase 2 (slow, gated by ensureIndexes) builds the
// heavy openalex_works indexes CONCURRENTLY — never a boot-time SHARE lock on
// the 353 GB table (ADR 0013).
//
// A database NEWER than this binary (registry.ErrSchemaTooNew) can never
// converge by retrying, so it is a hard fatal instead of another attempt.
func ensureCatalogSchema(pool *pgxpool.Pool, corpus *openalexcorpus.Store, ensureIndexes bool) {
	const (
		attemptTimeout = 90 * time.Second
		retryDelay     = 30 * time.Second
		maxAttempts    = 10
	)
	baseOK := false
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), attemptTimeout)
		cErr := corpus.EnsureSchema(ctx)
		err := registry.Migrate(ctx, pool)
		cancel()
		if errors.Is(err, registry.ErrSchemaTooNew) {
			log.Fatalf("papers: %v", err)
		}
		if err == nil && cErr == nil {
			log.Printf("papers: registry migrated + corpus base schema ensured")
			baseOK = true
			break
		}
		slog.Warn("papers: base schema ensure attempt failed; retrying",
			"attempt", attempt, "max", maxAttempts, "registry_error", err, "corpus_error", cErr)
		time.Sleep(retryDelay)
	}
	if !baseOK {
		slog.Error("papers: base schema ensure gave up after retries (will retry next boot)")
		return
	}

	if !ensureIndexes {
		slog.Info("papers: corpus index build skipped (postgres.corpus_ensure_indexes=false; operator-provisioned)")
		return
	}
	// Phase 2: heavy openalex_works indexes, CONCURRENTLY, on a generous
	// budget (a full-corpus GIN build is hours + heavy I/O). CONCURRENTLY
	// takes only a ShareUpdateExclusive lock, so writes + the bootstrap keep
	// running; EnsureIndexes is best-effort per index and idempotent, so a
	// timeout just resumes the remainder on the next boot.
	const indexBudget = 12 * time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), indexBudget)
	defer cancel()
	if err := corpus.EnsureIndexes(ctx); err != nil {
		slog.Error("papers: corpus index build incomplete (will resume next boot)", "error", err)
		return
	}
	log.Printf("papers: corpus indexes ensured (concurrently)")
}

// initShareStore was removed in v0.9.0 along with the /share/*
// surface (see RegisterPapers doc comment).

// buildSearchEngine constructs the multi-provider search engine behind
// POST /api/search. The provider list comes from search.providers
// (default "catalog,arxiv,openalex"): catalog searches the PostgreSQL
// registry itself (needs the pool), arxiv / openalex hit their public
// APIs with the shared http client (+ OpenAlex polite-pool mailto), and
// "remote" joins the fan-out only when search.remote
// is enabled with a URL (remote == nil otherwise). Unknown or
// unconstructible providers are logged and skipped — one bad entry must
// not sink the whole engine. The engine resolves-or-mints every
// identity-anchored hit through registryStore and fires
// ingester.OnMint for freshly minted papers (lazy ingestion).
func buildSearchEngine(cfg *config.Config, pool *pgxpool.Pool, registryStore *registry.Store, ingester *ingest.Ingester, remote *search.RemoteProvider) *search.Engine {
	httpClient := &http.Client{Timeout: 10 * time.Second}
	mailto := strings.TrimSpace(cfg.OpenAlexMailto)
	var providers []search.Provider
	for _, name := range cfg.SearchProviders {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "":
			continue
		case "catalog":
			providers = append(providers, search.NewCatalogProvider(pool))
		case "arxiv":
			providers = append(providers, search.NewArxivProvider(httpClient))
		case "openalex":
			providers = append(providers, search.NewOpenAlexProvider(httpClient, mailto))
		case "remote":
			if remote == nil {
				slog.Warn("search: remote provider requested but search.remote is disabled or has no url; skipping")
				continue
			}
			providers = append(providers, remote)
		default:
			slog.Warn("search: unknown provider in search.providers; skipping", "provider", name)
		}
	}
	return search.NewEngine(registryStore, ingester.OnMint, providers...)
}

// buildRemoteProvider constructs the qatlas-search microservice client
// (used both as a fan-out provider and by POST /api/search/agentic).
// Returns nil unless search.remote is enabled AND carries a URL.
// multiBackendFor adapts the remote provider to the multi-search backend
// interface, mapping "not configured" (nil provider) to a nil interface
// value so the routes' `backend == nil` 503 branches work (a nil
// *search.RemoteProvider wrapped in an interface is NOT nil).
func multiBackendFor(p *search.RemoteProvider) routes.MultiBackend {
	if p == nil {
		return nil
	}
	return p
}

func buildRemoteProvider(cfg *config.Config) *search.RemoteProvider {
	if !cfg.RemoteEnabled || strings.TrimSpace(cfg.RemoteURL) == "" {
		return nil
	}
	return search.NewRemoteProvider(cfg.RemoteURL, cfg.RemoteToken, cfg.RemoteTimeout)
}

// buildRAGRemoteClient constructs the qatlas-rag microservice client
// (index-push only: qatlasd POSTs /v1/index when a paper flips to
// 'ready'). Returns nil unless rag.remote is enabled AND carries a URL.
func buildRAGRemoteClient(cfg *config.Config) *rag.RemoteClient {
	if !cfg.RAGRemoteEnabled || strings.TrimSpace(cfg.RAGRemoteURL) == "" {
		return nil
	}
	return rag.NewRemoteClient(cfg.RAGRemoteURL, cfg.RAGRemoteToken, cfg.RAGRemoteTimeout)
}

// compile-time check: the local runner is an agentic backend.
var _ routes.AgenticBackend = (*agentic.Runner)(nil)

// buildLocalAgentic constructs the local claude-CLI agentic backend
// (internal/agentic) when search.agentic.backend: local. Returns nil —
// with a WARN, never a fatal — when the backend is not selected or the
// claude binary cannot be resolved, leaving the endpoint to 503 unless
// the remote backend is configured.
func buildLocalAgentic(cfg *config.Config, engine *search.Engine) *agentic.Runner {
	if cfg.AgenticBackend != "local" {
		return nil
	}
	if _, err := exec.LookPath(cfg.AgenticLocalClaudeBin); err != nil {
		slog.Warn("agentic local backend: claude binary not usable; POST /api/search/agentic will 503",
			"claude_bin", cfg.AgenticLocalClaudeBin, "error", err)
		return nil
	}
	runner, err := agentic.NewRunner(agentic.Options{
		Engine:         engine,
		ClaudeBin:      cfg.AgenticLocalClaudeBin,
		Model:          cfg.AgenticLocalModel,
		SandboxRoot:    cfg.AgenticLocalSandboxDir,
		Timeout:        cfg.AgenticLocalTimeout,
		MaxBudgetUSD:   cfg.AgenticLocalMaxBudgetUSD,
		PromptTemplate: cfg.AgenticLocalPromptTpl,
	})
	if err != nil {
		slog.Warn("agentic local backend: runner construction failed; POST /api/search/agentic will 503",
			"error", err)
		return nil
	}
	slog.Info("agentic local backend enabled",
		"claude_bin", cfg.AgenticLocalClaudeBin, "sandbox_dir", cfg.AgenticLocalSandboxDir)
	return runner
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
func registerRoutes(se *core.ServeEvent, app core.App, cfg *config.Config, rawStore objstore.Store, registryStore *registry.Store, corpus *openalexcorpus.Store, searchEngine *search.Engine, remoteProvider *search.RemoteProvider, ragClient *rag.RemoteClient, localAgentic *agentic.Runner, usageStore *usage.Store, enforcer *casbin.Enforcer, mineruConverter *mineru.Converter, mineruScheduler *mineru.Scheduler, ingester *ingest.Ingester, doiResolver *openalex.Resolver, arxivFetcher *arxiv.Fetcher, downloaderRoutes routes.Downloader, started time.Time) {
	probes := healthz.Probes{
		Cfg:      cfg,
		RawStore: rawStore,
		PGPool:   registryStore.Pool(),
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
			// Inject converter counters + daily-scheduler state when
			// asset downloads are enabled. The counters stay flat under
			// `mineru` (back-compat with existing dashboards); the
			// scheduler block nests under `mineru.scheduler`.
			if cfg.PaperAccessEnabled {
				mineruHealth := struct {
					mineru.CountersSnapshot
					Scheduler *mineru.SchedulerSnapshot `json:"scheduler,omitempty"`
				}{
					CountersSnapshot: mineruConverter.Snapshot(),
				}
				if mineruScheduler != nil {
					snap := mineruScheduler.Snapshot()
					mineruHealth.Scheduler = &snap
				}
				result.Data.MinerU = mineruHealth
			}
			// Anonymous callers get a sanitised payload: just
			// status / version / uptime / per-check status. Strips
			// bucket names, mesh endpoints, schema versions, MinerU
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
	// system_pat.token from config.yaml on the server.)

	eventBus := events.NewBus()

	// The remote search microservice is reported through the plugin
	// surface as a builtin manifest (it is an in-process client): its
	// enabled state mirrors search.remote.enabled and a background
	// healthz probe (below) keeps the connected/disconnected status
	// honest.
	searchRemoteManifest := qplugin.Manifest{
		ID:         "search-remote",
		Name:       "Remote search (qatlas-search microservice)",
		Version:    Version,
		ABIVersion: qplugin.HostABIVersion,
		Kind:       qplugin.KindBuiltin,
		Contributes: qplugin.Contributes{
			Capabilities: []string{"search"},
		},
	}
	// The qatlas-rag microservice (index-push target) gets the same
	// treatment: builtin manifest whose enabled state mirrors
	// rag.remote.enabled, kept honest by the healthz probe below.
	ragRemoteManifest := qplugin.Manifest{
		ID:         "rag-remote",
		Name:       "RAG indexing (qatlas-rag microservice)",
		Version:    Version,
		ABIVersion: qplugin.HostABIVersion,
		Kind:       qplugin.KindBuiltin,
		Contributes: qplugin.Contributes{
			Capabilities: []string{"rag"},
		},
	}
	// The robust downloader is the third builtin: an internal module
	// (internal/downloader) surfaced through the plugin registry so the
	// SPA can gate the Robust Downloader page on its enabled state.
	downloaderManifest := qplugin.Manifest{
		ID:         "downloader",
		Name:       "Robust paper downloader (multi-parad + agent fallback)",
		Version:    Version,
		ABIVersion: qplugin.HostABIVersion,
		Kind:       qplugin.KindBuiltin,
		Contributes: qplugin.Contributes{
			Capabilities: []string{"download"},
		},
	}
	pluginDisabled := cfg.PluginsDisabled
	if !cfg.RemoteEnabled {
		pluginDisabled = append(append([]string(nil), pluginDisabled...), "search-remote")
	}
	if !cfg.RAGRemoteEnabled {
		pluginDisabled = append(append([]string(nil), pluginDisabled...), "rag-remote")
	}
	if downloaderRoutes == nil {
		pluginDisabled = append(append([]string(nil), pluginDisabled...), "downloader")
	}
	pluginRegistry, err := qplugin.LoadDir(cfg.PluginsDir, qplugin.Options{
		Enabled:  cfg.PluginsEnabled,
		Disabled: pluginDisabled,
		Builtins: append(qplugin.BuiltinManifests(), searchRemoteManifest, ragRemoteManifest, downloaderManifest),
	})
	if err != nil {
		slog.Warn("plugins: failed to load plugin manifests", "dir", cfg.PluginsDir, "error", err)
		pluginRegistry = qplugin.NewBuiltinRegistry(qplugin.Options{
			Enabled:  cfg.PluginsEnabled,
			Disabled: cfg.PluginsDisabled,
		})
	}
	routes.RegisterPlugins(se, pluginRegistry, enforcer)

	// Keep the search-remote plugin summary honest: probe the
	// microservice's /healthz immediately and then every 30s, folding
	// the outcome into the registry (connected/disconnected + error).
	if remoteProvider != nil {
		probeCtx, stopProbe := context.WithCancel(context.Background())
		app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
			stopProbe()
			return e.Next()
		})
		go probeRemoteSearch(probeCtx, pluginRegistry, remoteProvider)
	}

	// Same probe loop for the qatlas-rag microservice (rag-remote
	// plugin summary). Never load-bearing: a disconnected qatlas-rag
	// only means index pushes fail (logged best-effort at the call
	// sites), never that qatlasd refuses to start.
	if ragClient != nil {
		probeCtx, stopProbe := context.WithCancel(context.Background())
		app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
			stopProbe()
			return e.Next()
		})
		go probeRAGRemote(probeCtx, pluginRegistry, ragClient)
	}

	// Builtin plugins register through ONE platform hook (ADR 0003): each
	// implements routes.BuiltinPlugin; pull plugins also implement
	// routes.GitPullPlugin, so the platform mounts a uniform
	// POST /api/<id>/sync/pull + GET /api/<id>/sync/status for them. New
	// builtins land by adding to this list — no host-core surgery.
	// (The Lean-content builtin moved out of the main repo to an external
	// plugin; the list is currently empty but the hook stays.)
	builtinDeps := routes.PluginDeps{Cfg: cfg, Enforcer: enforcer, Registry: pluginRegistry}
	if err := routes.RegisterBuiltins(se, builtinDeps); err != nil {
		slog.Error("plugins: failed to register builtin plugins", "error", err)
	}

	startPluginRPCServer(cfg, rawStore, pluginRegistry, eventBus)

	// Papers (stats, needs-mineru, mineru-lease, uploads, paper detail) —
	// see internal/routes/papers.go. v0.9.0 dropped the byte-serving
	// endpoints (markdown / resources / shares); the server only
	// exposes catalog metadata + the contribution flow by default.
	// /markdown + /markdown/status come back when the operator opts
	// in via paper_access.enabled: true.
	routes.RegisterPapers(se, cfg, rawStore, registryStore, corpus, enforcer, mineruConverter, ingester, doiResolver, arxivFetcher)

	// Multi-provider paper search — POST /api/search. See
	// internal/routes/search.go.
	routes.RegisterSearch(se, searchEngine, enforcer)

	// Per-user third-party search API keys (dashboard CRUD + injection
	// into the multi/agentic proxy calls). Encrypted at rest with a key
	// derived from the system PAT token; disabled (writes 503) when the
	// server has no secret configured. See internal/userkeys.
	userKeys := userkeys.NewStore(app, cfg.SystemPATToken)

	// Per-backend ("multi") search — POST /api/search/multi plus the
	// GET /api/search/backends catalog. Both talk to the remote
	// qatlas-search microservice; remoteProvider == nil (search.remote
	// disabled) leaves multi 503 and the catalog empty+remote:false. See
	// internal/routes/search_multi.go.
	routes.RegisterSearchMulti(se, userKeys, multiBackendFor(remoteProvider), enforcer)

	// Robust downloader — POST /api/downloader/fetch + GET
	// /api/downloader/jobs. downloaderRoutes is nil when paper access /
	// the downloader switch is off: the routes stay mounted but answer
	// 503, and the builtin plugin manifest is disabled above. See
	// internal/routes/downloader.go.
	routes.RegisterDownloader(se, downloaderRoutes, registryStore, enforcer)
	routes.RegisterSearchBackends(se, userKeys, multiBackendFor(remoteProvider))

	// Metered agentic search — POST /api/search/agentic. The backend is
	// the remote qatlas-search microservice by default, or the local
	// claude-CLI runner when search.agentic.backend: local; both satisfy
	// routes.AgenticBackend. See internal/routes/search_agentic.go.
	var agenticBackend routes.AgenticBackend
	if remoteProvider != nil {
		agenticBackend = remoteProvider
	}
	if localAgentic != nil {
		agenticBackend = localAgentic

		// Reap expired per-request sandboxes (startup sweep + 1min tick),
		// same lifecycle pattern as the remote-search probe above.
		janCtx, stopJanitor := context.WithCancel(context.Background())
		app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
			stopJanitor()
			return e.Next()
		})
		go agentic.RunJanitor(janCtx, cfg.AgenticLocalSandboxDir, cfg.AgenticLocalRetention)
	}
	routes.RegisterSearchAgentic(se, cfg, agenticBackend, usageStore, searchEngine, enforcer)

	// Personal Access Tokens — see internal/routes/pat.go.
	// /api/pat is session-token-only (PAT auth refused by sessionGuard);
	// no enforcer needed because there's no scope-gated endpoint here.
	routes.RegisterPAT(se, app)

	// "Me" self-service API (user dashboard) — see internal/routes/me.go
	// and internal/routes/me_search_keys.go. Session-token-only;
	// /api/me/usage 503s when Postgres is unavailable.
	routes.RegisterMe(se, cfg, usageStore)
	routes.RegisterMeSearchKeys(se, userKeys)

	// OAuth 2.0 Device Authorization Grant (RFC 8628) — see
	// internal/routes/oauthdevice.go. Lets `qatlas auth login --device`
	// mint a PAT without a local browser (poll-based flow). /code
	// and /token are anonymous; /lookup, /approve, /deny require a
	// browser session (sessionGuard, same as /api/pat).
	routes.RegisterOAuthDevice(se, app)

	// Admin console API — see internal/routes/admin.go.
	// /api/admin/whoami is session-gated (drives the SPA's admin nav);
	// /api/admin/db/schema and the raw-row browser additionally require
	// the GitHub admin allowlist (auth.admin_logins). The mineru
	// scheduler endpoints 503 when paper access is off (sched == nil).
	// The usage / plans / quotas metering surface
	// (internal/routes/admin_usage.go) 503s when Postgres is
	// unavailable. The plugin admin surface (admin_plugins.go) lists the
	// plugin registry and proxies manifest/config to the qatlas-search
	// microservice (503 when remote search is disabled).
	routes.RegisterAdmin(se, cfg, app, registryStore.Pool(), mineruScheduler, usageStore, pluginRegistry, remoteProvider)
}

// probeRemoteSearch probes the qatlas-search microservice's /healthz
// immediately and then every 30s, folding each outcome into the
// search-remote plugin summary (connected/disconnected + error). Runs
// until ctx is cancelled (server terminate).
func probeRemoteSearch(ctx context.Context, pluginRegistry *qplugin.Registry, remote *search.RemoteProvider) {
	probeHealthz(ctx, pluginRegistry, "search-remote", remote.Healthz)
}

// probeRAGRemote probes the qatlas-rag microservice's /healthz
// immediately and then every 30s, folding each outcome into the
// rag-remote plugin summary (connected/disconnected + error). Runs
// until ctx is cancelled (server terminate).
func probeRAGRemote(ctx context.Context, pluginRegistry *qplugin.Registry, ragClient *rag.RemoteClient) {
	probeHealthz(ctx, pluginRegistry, "rag-remote", ragClient.Healthz)
}

// probeHealthz is the shared 30s healthz probe loop behind
// probeRemoteSearch / probeRAGRemote: probe once immediately, then on
// every tick, folding each outcome into the named plugin summary.
func probeHealthz(ctx context.Context, pluginRegistry *qplugin.Registry, pluginID string, healthz func(context.Context) error) {
	const probeInterval = 30 * time.Second
	probe := func() {
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := healthz(pctx)
		cancel()
		pluginRegistry.SetProbeResult(pluginID, err)
	}
	probe()
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			probe()
		}
	}
}

func startPluginRPCServer(cfg *config.Config, rawStore objstore.Store, pluginRegistry *qplugin.Registry, eventBus *events.Bus) {
	hostAPI := hostapi.NewRegistry()
	if err := hostapi.RegisterCoreMethods(hostAPI, rawStore, eventBus); err != nil {
		slog.Error("plugin rpc: host capability registration failed", "error", err)
		return
	}
	rpcServer := (&qplugin.RPCServer{
		Registry:      pluginRegistry,
		HostAPI:       hostAPI,
		Events:        eventBus,
		ConnectSecret: cfg.PluginConnectSecret,
	}).Handler()
	srv := &http.Server{Addr: cfg.RPCWSBind, Handler: rpcServer}
	go func() {
		slog.Info("plugin rpc: listening", "addr", cfg.RPCWSBind)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("plugin rpc: server stopped", "addr", cfg.RPCWSBind, "error", err)
		}
	}()
}

// injectHTTPFlag mutates os.Args to add --http=<addr> when the user
// invokes the "serve" subcommand without supplying their own --http.
// This lets a plain `qatlasd serve` pick up the http_addr from
// config.yaml. Must run BEFORE app.Execute() (cobra parses os.Args
// there; mutating os.Args inside the serve RunE would be too late —
// the flag values are already bound by then).
func injectHTTPFlag(cfg *config.Config) {
	if cfg.HTTPAddr == "" {
		return
	}
	// Find the first positional arg (skipping --config and its value);
	// only inject when that's "serve".
	serve := false
	for i := 1; i < len(os.Args); i++ {
		a := os.Args[i]
		if a == "--config" {
			i++
			continue
		}
		if strings.HasPrefix(a, "--config=") {
			continue
		}
		serve = a == "serve"
		break
	}
	if !serve {
		return
	}
	for _, a := range os.Args[1:] {
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
// CRITICAL timing: PocketBase reads --dir in its eagerParseFlags pass
// inside NewWithConfig (the parsed value is baked into the BaseApp
// constructor). This function MUST therefore run BEFORE
// pocketbase.NewWithConfig — mutating os.Args any later (e.g. in the
// serve RunE) silently has no effect on the data directory.
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
// Only invoked when cfg.ForceTCP4 is true.
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
