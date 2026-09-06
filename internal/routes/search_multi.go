package routes

// search_multi.go: the per-backend ("multi") search surface.
//
//	POST /api/search/multi   — scopeGuard(papers, read). Proxies one
//	                           mode="multi" call to the qatlas-search
//	                           microservice: one raw hit list per
//	                           requested backend, no cross-backend merge
//	                           or ranking (the fused scoring lives on
//	                           POST /api/search and /api/search/agentic).
//	                           The caller's stored third-party keys are
//	                           decrypted and forwarded as api_keys so
//	                           key-requiring backends run under the
//	                           user's own credentials. Not metered (same
//	                           as POST /api/search).
//	GET  /api/search/backends — sessionGuard. The backend catalog the
//	                           SPA renders as checkboxes: static table
//	                           merged with the microservice's live
//	                           /v1/backends (server-side availability)
//	                           and the caller's stored keys.
//	                           selectable = server_ready ||
//	                           (user_key && key_configured).

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/userkeys"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

// MultiBackend is the backend behind POST /api/search/multi and the
// catalog source of GET /api/search/backends: the remote qatlas-search
// microservice (*search.RemoteProvider). The interface keeps the
// handlers testable with a fake, mirroring AgenticBackend.
type MultiBackend interface {
	SearchMulti(ctx context.Context, query string, maxResults int, sources []string, apiKeys map[string]string) (search.RemoteMultiResponse, error)
	ListBackends(ctx context.Context) ([]search.RemoteBackendMeta, error)
}

// compile-time check: the remote microservice client is a backend.
var _ MultiBackend = (*search.RemoteProvider)(nil)

// multiSearchRequest is the POST /api/search/multi body.
type multiSearchRequest struct {
	Text       string   `json:"text"`
	MaxResults int      `json:"max_results"`
	Sources    []string `json:"sources"`
}

// multiSearchResponse forwards the microservice's multi contract plus
// the remote flag (frontends use it to pick the rendering path).
type multiSearchResponse struct {
	Results map[string][]search.RemoteHit `json:"results"`
	Usage   search.RemoteUsage            `json:"usage"`
	Errors  map[string]string             `json:"errors"`
	Remote  bool                          `json:"remote"`
}

// maxMultiSources bounds the requested backend list.
const maxMultiSources = 32

// normalizeSources trims, de-duplicates (first-seen order) and caps.
func normalizeSources(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) >= maxMultiSources {
			break
		}
	}
	return out
}

// RegisterSearchMulti mounts POST /api/search/multi. backend is nil when
// search.remote is disabled — the route stays registered but every call
// answers 503 (same convention as the agentic endpoint).
func RegisterSearchMulti(se *core.ServeEvent, keys *userkeys.Store, backend MultiBackend, enforcer *casbin.Enforcer) {
	se.Router.POST("/api/search/multi", scopeGuard(enforcer, "papers", "read", func(re *core.RequestEvent) error {
		if backend == nil {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "multi search requires the qatlas-search microservice (search.remote)",
			})
		}
		raw, err := io.ReadAll(io.LimitReader(re.Request.Body, 1<<20))
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "read body: " + err.Error()})
		}
		var req multiSearchRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid JSON: " + err.Error()})
		}
		if strings.TrimSpace(req.Text) == "" {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "text is required"})
		}
		sources := normalizeSources(req.Sources)
		if len(sources) == 0 {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "at least one source backend is required"})
		}

		// Per-user third-party keys, restricted to the requested backends.
		// System PAT callers (re.Auth == nil) search without user keys.
		var apiKeys map[string]string
		if re.Auth != nil && keys != nil && keys.Enabled() {
			userKeys, err := keys.GetKeys(re.Auth.Id)
			if err != nil {
				slog.Error("multi search: load user keys failed", "user_id", re.Auth.Id, "error", err)
				return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
			}
			for _, src := range sources {
				if k, ok := userKeys[src]; ok {
					if apiKeys == nil {
						apiKeys = map[string]string{}
					}
					apiKeys[src] = k
				}
			}
		}

		resp, err := backend.SearchMulti(re.Request.Context(), req.Text, req.MaxResults, sources, apiKeys)
		if err != nil {
			return re.JSON(http.StatusBadGateway, map[string]string{
				"detail": "multi search upstream failed: " + err.Error(),
			})
		}
		out := multiSearchResponse{
			Results: resp.Results,
			Usage:   resp.Usage,
			Errors:  resp.Errors,
			Remote:  true,
		}
		if out.Results == nil {
			out.Results = map[string][]search.RemoteHit{}
		}
		if out.Errors == nil {
			out.Errors = map[string]string{}
		}
		return re.JSON(http.StatusOK, out)
	}))
}

// searchBackendEntry is one row of the /api/search/backends catalog.
type searchBackendEntry struct {
	search.BackendMeta
	ServerReady   bool `json:"server_ready"`
	KeyConfigured bool `json:"key_configured"`
	Selectable    bool `json:"selectable"`
}

// searchBackendsResponse is the GET /api/search/backends wire shape.
type searchBackendsResponse struct {
	Remote   bool                 `json:"remote"`
	KeysOn   bool                 `json:"keys_enabled"`
	Backends []searchBackendEntry `json:"backends"`
}

// backendsCatalogTimeout bounds the live /v1/backends fetch.
const backendsCatalogTimeout = 4 * time.Second

// RegisterSearchBackends mounts GET /api/search/backends (sessionGuard:
// key_configured is user-private, and the catalog only feeds the SPA).
func RegisterSearchBackends(se *core.ServeEvent, keys *userkeys.Store, backend MultiBackend) {
	se.Router.GET("/api/search/backends", sessionGuard(func(re *core.RequestEvent) error {
		out := searchBackendsResponse{Backends: []searchBackendEntry{}}

		keysOn := keys != nil && keys.Enabled()
		out.KeysOn = keysOn
		var configured map[string]bool
		if keysOn && re.Auth != nil {
			userKeys, err := keys.GetKeys(re.Auth.Id)
			if err != nil {
				slog.Error("search backends: load user keys failed", "user_id", re.Auth.Id, "error", err)
				return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
			}
			configured = make(map[string]bool, len(userKeys))
			for name := range userKeys {
				configured[name] = true
			}
		}

		if backend == nil {
			// search.remote disabled: no multi search at all — empty catalog.
			return re.JSON(http.StatusOK, out)
		}
		out.Remote = true

		ctx, cancel := context.WithTimeout(re.Request.Context(), backendsCatalogTimeout)
		defer cancel()
		live, err := backend.ListBackends(ctx)
		if err != nil {
			// Unreachable microservice: degrade to the static table with
			// server_ready=false (the search page will surface it via the
			// search-remote plugin status anyway).
			slog.Debug("search backends: live catalog unavailable", "error", err)
		}

		// Start from the static table (stable order), overlay live entries;
		// append any live-only backends at the end. When the live fetch
		// failed everything degrades to server_ready=false.
		merged := make([]searchBackendEntry, 0, len(search.BackendCatalog))
		byName := map[string]int{}
		for _, m := range search.BackendCatalog {
			merged = append(merged, searchBackendEntry{BackendMeta: m})
			byName[m.Name] = len(merged) - 1
		}
		for _, lm := range live {
			entry := searchBackendEntry{
				BackendMeta: search.BackendMeta{
					Name:        lm.Name,
					Label:       lm.Label,
					Category:    lm.Category,
					RequiresKey: lm.RequiresKey,
					UserKey:     lm.UserKey,
				},
				ServerReady: lm.Available,
			}
			if i, ok := byName[lm.Name]; ok {
				merged[i] = entry
			} else {
				byName[lm.Name] = len(merged)
				merged = append(merged, entry)
			}
		}

		for i := range merged {
			e := &merged[i]
			if e.Label == "" {
				e.Label = e.Name
			}
			e.KeyConfigured = configured[e.Name]
			e.Selectable = e.ServerReady || (e.UserKey && e.KeyConfigured)
		}
		out.Backends = merged
		return re.JSON(http.StatusOK, out)
	}))
}
