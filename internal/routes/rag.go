package routes

// rag.go: POST /api/rag/retrieve + POST /api/rag/evidence — the qatlas-rag
// query proxy.
//
// Retrieval / evidence queries used to be a qatlasd plugin; they now live
// in the qatlas-rag microservice, which owns the vector index AND the
// request/response schemas. qatlasd only authenticates the caller (PAT or
// session via scopeGuard, papers:read — rag reads paper-derived indexes)
// and relays the bytes: the body is forwarded unmodified (64 KiB cap, the
// rag service's own documented bound) and the reply is streamed back
// as-is. This keeps the schema in exactly one repository — adding fields
// to a retrieve query never needs a qatlasd release.
//
// Degradation: rag.remote disabled → 503 "rag service not configured";
// microservice unreachable → 503 "rag unreachable: …"; a non-2xx reply is
// forwarded with its own status + body so clients see the rag error
// schema instead of a collapsed generic error.

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/rag"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

// ragProxyMaxBody bounds the proxied request body. qatlas-rag's own
// request cap is 64 KiB; anything larger is rejected here before the
// round-trip.
const ragProxyMaxBody = 64 * 1024

// RegisterRagProxy mounts the two qatlas-rag relay routes. client is nil
// when rag.remote is disabled — the routes stay registered (stable
// surface) and answer 503, same convention as the search/match proxies.
func RegisterRagProxy(se *core.ServeEvent, client *rag.RemoteClient, enforcer *casbin.Enforcer) {
	se.Router.POST("/api/rag/retrieve", ragProxyHandler(client, enforcer, (*rag.RemoteClient).Retrieve))
	se.Router.POST("/api/rag/evidence", ragProxyHandler(client, enforcer, (*rag.RemoteClient).Evidence))
}

// ragCall is the RemoteClient method the relay invokes.
type ragCall func(c *rag.RemoteClient, ctx context.Context, body []byte) ([]byte, error)

// ragProxyHandler wires one relay route: scopeGuard, bounded body read,
// forward, passthrough reply.
func ragProxyHandler(client *rag.RemoteClient, enforcer *casbin.Enforcer, call ragCall) func(re *core.RequestEvent) error {
	return scopeGuard(enforcer, "papers", "read", func(re *core.RequestEvent) error {
		body, err := io.ReadAll(io.LimitReader(re.Request.Body, ragProxyMaxBody+1))
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "read rag body: " + err.Error(),
			})
		}
		if len(body) > ragProxyMaxBody {
			return re.JSON(http.StatusRequestEntityTooLarge, map[string]string{
				"detail": "rag request body exceeds 64 KiB",
			})
		}
		out, err := call(client, re.Request.Context(), body)
		if err != nil {
			var up *rag.UpstreamError
			switch {
			case errors.Is(err, rag.ErrNotConfigured):
				return re.JSON(http.StatusServiceUnavailable, map[string]string{
					"detail": "rag service not configured",
				})
			case errors.As(err, &up):
				// Forward the microservice's own status + body.
				re.Response.Header().Set("Content-Type", "application/json")
				re.Response.WriteHeader(up.Status)
				_, _ = re.Response.Write(up.Body)
				return nil
			default:
				return re.JSON(http.StatusServiceUnavailable, map[string]string{
					"detail": "rag unreachable: " + err.Error(),
				})
			}
		}
		re.Response.Header().Set("Content-Type", "application/json")
		re.Response.WriteHeader(http.StatusOK)
		_, _ = re.Response.Write(out)
		return nil
	})
}
