// Command quantumatlas is the Go + PocketBase rewrite of the QuantumAtlas
// FastAPI server. It embeds PocketBase as a Go library and exposes the same
// /api/* surface that the existing Python CLI consumes.
//
// Usage:
//
//	quantumatlas serve --http=0.0.0.0:4200
//	quantumatlas migrate up
//	quantumatlas superuser upsert <email> <password>
//
// All standard PocketBase subcommands are inherited. QuantumAtlas-specific
// business routes are registered via the OnServe hook.
package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineruclaim"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/routes"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/shares"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

// Version is overridden at build time via:
//
//	go build -ldflags "-X main.Version=$(cat pyproject.toml ...)"
//
// Defaults to "0.2.2-go" so the binary always reports something useful.
var Version = "0.2.2-go"

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	app := pocketbase.New()

	// Inject --http from env if the operator didn't pass it explicitly.
	// Matches the FastAPI uvicorn --host/--port behaviour driven by .env.
	injectHTTPFlag(cfg)

	auth.Register(app, cfg)

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

	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		registerRoutes(se, app, cfg, shareStore, claimStore)

		// Serve the embedded SPA + assets last (catch-all fallback).
		// In dev pb_public/ may be empty; that's fine — the static handler
		// just 404s on misses and the API routes still work.
		se.Router.GET("/{path...}", apis.Static(os.DirFS("./pb_public"), false))

		return se.Next()
	})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}

// initShareStore returns a ready-to-use ShareStore rooted at
// {DATA_DIR}/shares (or a sensible default when DATA_DIR is unset).
func initShareStore(cfg *config.Config) (*shares.Store, error) {
	root := cfg.DataDir
	if root == "" {
		root = "./pb_data/qatlas_data"
	}
	return shares.NewStore(filepath.Join(root, "shares"))
}

// initClaimStore returns a ready-to-use mineru claim store rooted at
// {DATA_DIR}/mineru-claims (or a sensible default when DATA_DIR is unset).
func initClaimStore(cfg *config.Config) (*mineruclaim.Store, error) {
	root := cfg.DataDir
	if root == "" {
		root = "./pb_data/qatlas_data"
	}
	return mineruclaim.NewStore(filepath.Join(root, "mineru-claims"))
}

// registerRoutes wires the QuantumAtlas /api/* surface. Most endpoints are
// implemented under internal/routes/ and pulled in by their respective
// Register* helpers as we migrate each module in subsequent phases.
func registerRoutes(se *core.ServeEvent, app core.App, cfg *config.Config, shareStore *shares.Store, claimStore *mineruclaim.Store) {
	// /health — uptime probe (Python server compat). PocketBase already
	// owns /api/health, so we expose this at the root to match the old
	// FastAPI surface used by smoke tests and Caddy health probes.
	se.Router.GET("/health", func(re *core.RequestEvent) error {
		return re.JSON(200, map[string]any{
			"status":  "healthy",
			"version": Version,
		})
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

	// /api/session/token — return the empty stub for now; PocketBase's
	// built-in auth flow replaces the Caddy AUTHP_ACCESS_TOKEN cookie path.
	se.Router.GET("/api/session/token", func(re *core.RequestEvent) error {
		re.Response.Header().Set("Cache-Control", "no-store")
		return re.JSON(200, map[string]string{"token": ""})
	})

	// Wiki / pages / stats / search / lint — see internal/routes/wiki.go.
	routes.RegisterWiki(se, cfg)

	// Graph (Neo4j) — see internal/routes/graph.go.
	routes.RegisterGraph(se, cfg)

	// Papers (resources, upload, mineru-claim) — see internal/routes/papers.go.
	routes.RegisterPapers(se, cfg, shareStore, claimStore)

	// Shares CRUD + public /share/{token}* — see internal/routes/shares.go.
	routes.RegisterShares(se, cfg, shareStore)
}

// injectHTTPFlag mutates os.Args to add --http=<addr> when the user invokes
// the "serve" subcommand without supplying their own --http. This lets a
// plain `quantumatlas serve` pick up QATLAS_SERVER_HOST/PORT from .env.
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
