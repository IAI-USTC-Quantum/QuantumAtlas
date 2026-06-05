// Package routes — RAG reverse-proxy handler.
//
// /api/rag/* fronts a co-located sidecar process (FastAPI on a loopback
// or mesh address by convention) that handles vector search, hybrid
// retrieval (dense + sparse), reranking, and snippet assembly. The Go
// server here owns auth (session cookie or PAT carrying papers:read)
// and content-policy gating; everything else is forwarded transparently.
//
// Sidecar implementation lives in ./rag/qatlas_rag/sidecar/ — see
// ./rag/README.md and docs/deployment/rag.md for how to stand one up.
//
// Two routes are exposed when the feature is enabled:
//
//	POST /api/rag/search    — gated by authGuard + scopeGuard("papers", "read")
//	GET  /api/rag/healthz   — anonymous; lets the SPA probe whether the
//	                          toggle should appear, and lets ops curl
//	                          without minting a token. The sidecar
//	                          replies with intentionally coarse status
//	                          (`{"status":"ok"|"degraded"|"down"}`) so
//	                          leaking it anonymously does not expose
//	                          internal topology.
//
// Both routes are registered iff BOTH of the following hold:
//
//   - cfg.PaperAccessEnabled = true   (master switch for serving
//     derivative paper content; same gate as /api/papers/{id}/markdown)
//   - cfg.RAGSidecarURL != ""            (operator configured a sidecar)
//
// When either is false the routes are absent and any request returns
// 404 — indistinguishable from "no such handler". The public posture of
// quantum-atlas.ai (master switch OFF) remains "server does not
// redistribute markdown bytes", which RAG snippets would otherwise
// violate.
//
// Design note: we instantiate ONE httputil.ReverseProxy at registration
// time (per-call would re-resolve the URL on every request and
// re-construct the transport, throwing away connection pooling). The
// proxy is shared across all requests and is safe for concurrent use.
package routes

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

// RegisterRAG mounts /api/rag/* iff the feature is fully configured.
// Disabled when cfg.PaperAccessEnabled is false OR cfg.RAGSidecarURL
// is empty — server boots normally in either case.
//
// The handler strips the "/api/rag" path prefix before forwarding so
// the sidecar sees "/search" / "/healthz". This lets the sidecar live
// behind any URL the operator picks (typically 127.0.0.1:8802 on the
// same host, but a mesh-reachable address is fine too).
func RegisterRAG(se *core.ServeEvent, cfg *config.Config, enforcer *casbin.Enforcer) {
	if cfg == nil {
		return
	}
	if !cfg.PaperAccessEnabled {
		slog.Info("rag: disabled (QATLAS_PAPER_ACCESS_ENABLED is off)")
		return
	}
	if strings.TrimSpace(cfg.RAGSidecarURL) == "" {
		slog.Info("rag: disabled (QATLAS_RAG_SIDECAR_URL unset)")
		return
	}
	target, err := url.Parse(cfg.RAGSidecarURL)
	if err != nil {
		slog.Error("rag: invalid QATLAS_RAG_SIDECAR_URL", "url", cfg.RAGSidecarURL, "err", err)
		return
	}
	if target.Scheme == "" || target.Host == "" {
		slog.Error("rag: QATLAS_RAG_SIDECAR_URL missing scheme/host", "url", cfg.RAGSidecarURL)
		return
	}

	proxy := newRAGProxy(target)

	se.Router.POST("/api/rag/search", scopeGuard(enforcer, "papers", "read", func(re *core.RequestEvent) error {
		proxy.ServeHTTP(re.Response, re.Request)
		return nil
	}))

	// healthz is intentionally anonymous: lets the SPA hide the toggle
	// when RAG isn't deployed, and lets ops curl without a token.
	se.Router.GET("/api/rag/healthz", func(re *core.RequestEvent) error {
		proxy.ServeHTTP(re.Response, re.Request)
		return nil
	})

	slog.Info("rag: enabled", "sidecar", target.String())
}

// newRAGProxy builds the shared reverse proxy with our path-stripping
// director and a JSON-friendly error handler.  Factored out so tests
// can drive the proxy directly without spinning up a PocketBase
// ServeEvent.
func newRAGProxy(target *url.URL) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		// Capture the *declared* Content-Length BEFORE any further
		// mutation. PocketBase's Echo-based middleware chain may swap
		// r.Body with an upstream-buffered reader that hands out more
		// bytes than the original Content-Length advertises — classic
		// symptom from net/http:
		//
		//     "ContentLength=N with Body length 2N".
		//
		// httputil.ReverseProxy then aborts the upstream connection
		// with that error and the client sees a 502. We defensively
		// wrap with io.LimitReader so the sidecar always receives
		// exactly the bytes the client sent.
		declaredLen := r.ContentLength

		originalDirector(r)
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/rag")
		if r.URL.Path == "" {
			r.URL.Path = "/"
		}
		// Use the upstream's Host so any vhost-based router on the
		// sidecar side selects the right app. FastAPI doesn't care
		// but documenting intent for ops.
		r.Host = target.Host

		if declaredLen > 0 && r.Body != nil {
			r.Body = io.NopCloser(io.LimitReader(r.Body, declaredLen))
			r.ContentLength = declaredLen
		}
	}
	// Make sidecar outage visible as a JSON 502 rather than Go's
	// default error page; aligns with the rest of /api/* JSON contracts.
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"detail":"rag sidecar unavailable","error":"` + err.Error() + `"}`))
	}
	return proxy
}
