// Admin plugin management: /api/admin/plugins.
//
// GET /api/admin/plugins mirrors /api/v1/plugins ({plugins: [...]})
// behind the adminGuard (GitHub allowlist) instead of the scope guard,
// for the admin console's plugin page.
//
// The manifest/config endpoints are a narrow proxy to the qatlas-search
// microservice's own admin surface — currently the only plugin with an
// admin page is search-remote, everything else 404s. 2xx upstream
// payloads pass through verbatim (Content-Type application/json);
// non-2xx upstreams map to 502 with the upstream detail forwarded;
// network/timeout failures map to 503.
package routes

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	qplugin "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/plugin"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"

	"github.com/pocketbase/pocketbase/core"
)

// adminPluginIDSearchRemote is the only plugin id with an admin page.
const adminPluginIDSearchRemote = "search-remote"

// registerAdminPlugins wires the adminGuard'd plugin routes. registry
// may be nil (degraded plugin loading) — the list endpoint then returns
// an empty slice. remote is the qatlas-search client; nil (remote
// search disabled) 503s the proxy endpoints while the list keeps
// working.
func registerAdminPlugins(se *core.ServeEvent, cfg *config.Config, registry *qplugin.Registry, remote *search.RemoteProvider) {
	if registry == nil {
		registry = &qplugin.Registry{}
	}
	se.Router.GET("/api/admin/plugins", adminGuard(cfg, func(re *core.RequestEvent) error {
		return re.JSON(http.StatusOK, map[string]any{
			"plugins": registry.List(),
		})
	}))
	se.Router.GET("/api/admin/plugins/{id}/manifest", adminGuard(cfg,
		adminPluginProxyHandler(remote, "/v1/admin/manifest", false)))
	se.Router.PUT("/api/admin/plugins/{id}/config", adminGuard(cfg,
		adminPluginProxyHandler(remote, "/v1/admin/config", true)))
}

// adminPluginProxyHandler forwards the request to upstreamPath on the
// remote search microservice. forwardBody=true passes the request body
// through unchanged (the config PUT contract).
func adminPluginProxyHandler(remote *search.RemoteProvider, upstreamPath string, forwardBody bool) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		if re.Request.PathValue("id") != adminPluginIDSearchRemote {
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail": "plugin has no admin page",
			})
		}
		if remote == nil {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "search remote unavailable",
			})
		}
		var body []byte
		if forwardBody {
			var err error
			body, err = io.ReadAll(io.LimitReader(re.Request.Body, 1<<20))
			if err != nil {
				return re.JSON(http.StatusBadRequest, map[string]string{
					"detail": "failed to read request body",
				})
			}
		}
		// The provider's client timeout (search.remote.timeout, 60s
		// default) bounds the upstream call.
		result, err := remote.AdminProxy(re.Request.Context(), re.Request.Method, upstreamPath, body)
		if err != nil {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "search remote unreachable: " + err.Error(),
			})
		}
		if result.Status < 200 || result.Status >= 300 {
			return re.JSON(http.StatusBadGateway, map[string]string{
				"detail": upstreamDetail(result.Body),
			})
		}
		re.Response.Header().Set("Content-Type", "application/json")
		re.Response.WriteHeader(result.Status)
		_, err = re.Response.Write(result.Body)
		return err
	}
}

// upstreamDetail extracts {"detail": "..."} from a non-2xx upstream
// body when present, so the admin UI sees the microservice's own
// explanation rather than a generic gateway error.
func upstreamDetail(body []byte) string {
	var parsed struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Detail != "" {
		return parsed.Detail
	}
	return "search remote upstream error"
}
