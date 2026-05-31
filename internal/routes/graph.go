package routes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/neo4j"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

// RegisterGraph registers the three /api/graph/* endpoints.
//
// The Python handlers catch any exception and return a 200 with
// {"error": "..."} — almost everywhere else we'd treat a 500 as a 500,
// but the existing UI relies on this loose contract to render a friendly
// "Neo4j not configured" banner instead of a generic crash page. We
// preserve that behavior here.
//
// Auth: all three graph endpoints are GATED by authGuard +
// scopeGuard("graph", "read"). The knowledge graph is not anonymously
// readable — callers need a session token or a PAT carrying graph:read.
//
//   - GET /api/graph/stats and GET /api/graph/schema return fixed-shape
//     aggregates (node/relationship counts, the label and
//     relationship-type vocabulary) computed by server-owned Cypher.
//   - POST /api/graph/query executes caller-supplied Cypher: read-only
//     (ExecuteRead refuses writes at the driver level) but otherwise
//     unconstrained. There is no query-cost ceiling, so a pathological
//     query (e.g. an unbounded cartesian product) can pin Neo4j. We
//     deliberately do NOT add a cost limiter — once a caller is past
//     authGuard they are a trusted insider (a signed-in user or a PAT
//     holder who explicitly opted into graph:read), and the same person
//     could run the same query straight against Bolt. The accepted risk
//     and its rationale are documented in docs/concepts/auth-model.md and
//     docs/deployment/neo4j.md so operators know the only mitigation is
//     revoking the offending credential.
//
// Browser users are unaffected: session tokens carry the implicit
// ScopeMaster and short-circuit the scope check. PAT callers must mint a
// token with graph:read.
func RegisterGraph(se *core.ServeEvent, cfg *config.Config, enforcer *casbin.Enforcer) {
	se.Router.GET("/api/graph/stats", scopeGuard(enforcer, "graph", "read", func(re *core.RequestEvent) error {
		ctx, cancel := context.WithTimeout(re.Request.Context(), 10*time.Second)
		defer cancel()

		client, err := neo4j.NewClient(cfg.Neo4jURI, cfg.Neo4jUser, cfg.Neo4jPassword, cfg.Neo4jDatabase)
		if err != nil {
			return re.JSON(http.StatusOK, map[string]string{"error": err.Error()})
		}
		defer client.Close(ctx)
		if err := client.Connect(ctx); err != nil {
			return re.JSON(http.StatusOK, map[string]string{"error": err.Error()})
		}

		labelCounts, err := client.LabelCounts(ctx)
		if err != nil {
			return re.JSON(http.StatusOK, map[string]string{"error": err.Error()})
		}
		relCount, err := client.RelationshipCount(ctx)
		if err != nil {
			return re.JSON(http.StatusOK, map[string]string{"error": err.Error()})
		}

		labels := make([]string, 0, len(labelCounts))
		var total int64
		for label, count := range labelCounts {
			labels = append(labels, label)
			total += count
		}

		return re.JSON(http.StatusOK, map[string]any{
			"nodes":         total,
			"relationships": relCount,
			"labels":        labels,
			"label_counts":  labelCounts,
		})
	}))

	se.Router.POST("/api/graph/query", scopeGuard(enforcer, "graph", "read", func(re *core.RequestEvent) error {
		ctx, cancel := context.WithTimeout(re.Request.Context(), 30*time.Second)
		defer cancel()

		var body struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		raw, err := io.ReadAll(re.Request.Body)
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "read body: " + err.Error()})
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "parse body: " + err.Error()})
		}
		if body.Limit <= 0 {
			body.Limit = 50
		}
		if body.Query == "" {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "query required"})
		}

		client, err := neo4j.NewClient(cfg.Neo4jURI, cfg.Neo4jUser, cfg.Neo4jPassword, cfg.Neo4jDatabase)
		if err != nil {
			return re.JSON(http.StatusOK, map[string]any{"query": body.Query, "error": err.Error()})
		}
		defer client.Close(ctx)
		if err := client.Connect(ctx); err != nil {
			return re.JSON(http.StatusOK, map[string]any{"query": body.Query, "error": err.Error()})
		}

		records, err := client.ExecuteRead(ctx, body.Query, body.Limit)
		if err != nil {
			return re.JSON(http.StatusOK, map[string]any{"query": body.Query, "error": err.Error()})
		}
		return re.JSON(http.StatusOK, map[string]any{
			"query":   body.Query,
			"records": records,
		})
	}))

	se.Router.GET("/api/graph/schema", scopeGuard(enforcer, "graph", "read", func(re *core.RequestEvent) error {
		ctx, cancel := context.WithTimeout(re.Request.Context(), 10*time.Second)
		defer cancel()

		client, err := neo4j.NewClient(cfg.Neo4jURI, cfg.Neo4jUser, cfg.Neo4jPassword, cfg.Neo4jDatabase)
		if err != nil {
			return re.JSON(http.StatusOK, map[string]string{"error": err.Error()})
		}
		defer client.Close(ctx)
		if err := client.Connect(ctx); err != nil {
			return re.JSON(http.StatusOK, map[string]string{"error": err.Error()})
		}

		labels, relTypes, err := client.Schema(ctx)
		if err != nil {
			return re.JSON(http.StatusOK, map[string]string{"error": err.Error()})
		}
		return re.JSON(http.StatusOK, map[string]any{
			"labels":             labels,
			"relationship_types": relTypes,
		})
	}))
}
