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
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineruclaim"
	// pat is imported for its init()-time AppMigrations.Register side
	// effect that adds the pat_tokens collection to the migration list.
	// The package's exported API is consumed via internal/routes/pat.go.
	_ "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/routes"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/shares"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/webui"

	"github.com/joho/godotenv"
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

		registerRoutes(se, app, cfg, shareStore, claimStore)

		// Serve the embedded SPA last as the catch-all. apis.Static's
		// indexFallback=true means any path that doesn't match a real
		// file falls back to /index.html — exactly the SPA-client-router
		// behavior the React app needs for /wiki, /graph, /token, etc.
		se.Router.GET("/{path...}", apis.Static(webui.MustFS(), true))

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

	// (P12 removed: /api/session/token. It was a caddy-security-era stub
	// that returned an empty string. The SPA now reads pb.authStore.token
	// directly; CLI users pull their bearer from the /token page.)

	// Wiki / pages / stats / search / lint — see internal/routes/wiki.go.
	routes.RegisterWiki(se, cfg)

	// Graph (Neo4j) — see internal/routes/graph.go.
	routes.RegisterGraph(se, cfg)

	// Papers (resources, upload, mineru-claim) — see internal/routes/papers.go.
	routes.RegisterPapers(se, cfg, shareStore, claimStore)

	// Shares CRUD + public /share/{token}* — see internal/routes/shares.go.
	routes.RegisterShares(se, cfg, shareStore)

	// Personal Access Tokens — see internal/routes/pat.go.
	routes.RegisterPAT(se, app)
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
//  2. ./.env — relative to CWD, for ad-hoc dev `quantumatlas serve`
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
