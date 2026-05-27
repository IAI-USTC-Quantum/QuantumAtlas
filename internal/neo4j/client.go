// Package neo4j wraps the official neo4j-go-driver/v6 with the small
// slice of behavior QuantumAtlas actually needs: optional connection
// (server still boots without it), label counts, schema introspection,
// and arbitrary read-only Cypher with a hard LIMIT.
//
// We deliberately do NOT keep a long-lived driver pool here — the
// Python implementation opens a fresh client per request, and matching
// that behavior keeps lifecycle reasoning identical across both servers.
// Driver pooling can be revisited once the rewrite is the only server.
package neo4j

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	driver "github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

// DefaultLabels are the four node labels the wiki ingest pipeline writes
// to Neo4j. /api/graph/stats returns a count per label using this list.
// Mirrors the hardcoded list in atlas/knowledge/neo4j_client.py:get_stats.
var DefaultLabels = []string{"Primitive", "Algorithm", "Paper", "Implementation"}

// Client is a thin connect-and-go wrapper. Construct with NewClient,
// remember to Close.
type Client struct {
	uri      string
	username string
	password string
	database string

	driver driver.DriverWithContext
}

// NewClient validates parameters but does NOT open a connection.
// Returns ErrNotConfigured if uri is empty (Neo4j is optional).
func NewClient(uri, username, password, database string) (*Client, error) {
	if uri == "" {
		return nil, ErrNotConfigured
	}
	return &Client{
		uri:      uri,
		username: username,
		password: password,
		database: database,
	}, nil
}

// ErrNotConfigured is returned by NewClient when no NEO4J_URI is set, so
// route handlers can convert it to a 200-with-{"error": ...} response
// matching the Python try/except fallback.
var ErrNotConfigured = errors.New("neo4j not configured (NEO4J_URI empty)")

// Connect opens the actual Bolt connection. Safe to call concurrently;
// subsequent calls are no-ops once the driver is initialized.
func (c *Client) Connect(ctx context.Context) error {
	if c.driver != nil {
		return nil
	}
	d, err := driver.NewDriverWithContext(c.uri, driver.BasicAuth(c.username, c.password, ""))
	if err != nil {
		return fmt.Errorf("neo4j: open driver: %w", err)
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := d.VerifyConnectivity(verifyCtx); err != nil {
		_ = d.Close(ctx)
		return fmt.Errorf("neo4j: verify connectivity: %w", err)
	}
	c.driver = d
	return nil
}

// Close releases the underlying driver. Safe to call multiple times.
func (c *Client) Close(ctx context.Context) {
	if c.driver != nil {
		_ = c.driver.Close(ctx)
		c.driver = nil
	}
}

// session opens a SessionWithContext targeting the configured database
// (or default db when none specified).
func (c *Client) session(ctx context.Context) driver.SessionWithContext {
	cfg := driver.SessionConfig{}
	if c.database != "" {
		cfg.DatabaseName = c.database
	}
	return c.driver.NewSession(ctx, cfg)
}

// LabelCounts returns the node count for each label in DefaultLabels.
// Missing labels resolve to zero, not an error — matches the Python
// behavior where get_stats happily returns 0 for absent labels.
func (c *Client) LabelCounts(ctx context.Context) (map[string]int64, error) {
	if c.driver == nil {
		return nil, errors.New("neo4j: not connected")
	}
	out := map[string]int64{}
	session := c.session(ctx)
	defer session.Close(ctx)

	for _, label := range DefaultLabels {
		// Label names are from our static whitelist — safe to interpolate.
		// User-supplied input never reaches this query.
		query := fmt.Sprintf("MATCH (n:%s) RETURN count(n) AS count", label)
		count, err := singleInt64(ctx, session, query, "count")
		if err != nil {
			return nil, fmt.Errorf("neo4j: count %s: %w", label, err)
		}
		out[label] = count
	}
	return out, nil
}

// RelationshipCount returns the total number of relationships in the graph.
func (c *Client) RelationshipCount(ctx context.Context) (int64, error) {
	if c.driver == nil {
		return 0, errors.New("neo4j: not connected")
	}
	session := c.session(ctx)
	defer session.Close(ctx)
	return singleInt64(ctx, session, "MATCH ()-[r]->() RETURN count(r) AS count", "count")
}

// Schema returns the labels and relationship types defined in the
// database, sourced from db.labels() and db.relationshipTypes().
func (c *Client) Schema(ctx context.Context) (labels []string, relTypes []string, err error) {
	if c.driver == nil {
		return nil, nil, errors.New("neo4j: not connected")
	}
	session := c.session(ctx)
	defer session.Close(ctx)

	labels, err = stringList(ctx, session, "CALL db.labels()", "label")
	if err != nil {
		return nil, nil, fmt.Errorf("neo4j: labels: %w", err)
	}
	relTypes, err = stringList(ctx, session, "CALL db.relationshipTypes()", "relationshipType")
	if err != nil {
		return nil, nil, fmt.Errorf("neo4j: rel types: %w", err)
	}
	return labels, relTypes, nil
}

// ExecuteRead runs an arbitrary Cypher query and materializes each
// record as a map[column]value. Use limit to clamp output. Limit > 0
// is appended to the query as "LIMIT n" only when the query doesn't
// already include LIMIT itself (case-insensitive substring check).
//
// This mirrors the Python /api/graph/query handler's loose "f"{q} LIMIT
// {limit}"" composition.
func (c *Client) ExecuteRead(ctx context.Context, query string, limit int) ([]map[string]any, error) {
	if c.driver == nil {
		return nil, errors.New("neo4j: not connected")
	}
	if limit > 0 && !strings.Contains(strings.ToUpper(query), "LIMIT") {
		query = strings.TrimRight(query, " ;\n\t") + fmt.Sprintf(" LIMIT %d", limit)
	}

	session := c.session(ctx)
	defer session.Close(ctx)

	result, err := session.Run(ctx, query, nil)
	if err != nil {
		return nil, err
	}
	records, err := result.Collect(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		row := map[string]any{}
		for i, key := range rec.Keys {
			row[key] = simplifyValue(rec.Values[i])
		}
		out = append(out, row)
	}
	return out, nil
}

// singleInt64 runs query and returns rec[key] as int64 from the first
// record. Errors if no rows or wrong type.
func singleInt64(ctx context.Context, session driver.SessionWithContext, query, key string) (int64, error) {
	result, err := session.Run(ctx, query, nil)
	if err != nil {
		return 0, err
	}
	rec, err := result.Single(ctx)
	if err != nil {
		return 0, err
	}
	v, ok := rec.Get(key)
	if !ok {
		return 0, fmt.Errorf("missing field %q in result", key)
	}
	switch n := v.(type) {
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	default:
		return 0, fmt.Errorf("unexpected type %T for %q", v, key)
	}
}

// stringList runs query and returns the string value of rec[key] for
// each record. Used for db.labels() / db.relationshipTypes().
func stringList(ctx context.Context, session driver.SessionWithContext, query, key string) ([]string, error) {
	result, err := session.Run(ctx, query, nil)
	if err != nil {
		return nil, err
	}
	records, err := result.Collect(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(records))
	for _, rec := range records {
		if v, ok := rec.Get(key); ok {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out, nil
}

// simplifyValue strips driver-specific wrappers (Node, Relationship,
// Path) into plain map/slice shapes so the JSON marshaller emits
// frontend-friendly data instead of dumping internal IDs.
func simplifyValue(v any) any {
	switch n := v.(type) {
	case driver.Node:
		return map[string]any{
			"id":         n.GetElementId(),
			"labels":     n.Labels,
			"properties": n.Props,
		}
	case driver.Relationship:
		return map[string]any{
			"id":         n.GetElementId(),
			"type":       n.Type,
			"start_node": n.StartElementId,
			"end_node":   n.EndElementId,
			"properties": n.Props,
		}
	case driver.Path:
		nodes := make([]any, 0, len(n.Nodes))
		for _, node := range n.Nodes {
			nodes = append(nodes, simplifyValue(node))
		}
		rels := make([]any, 0, len(n.Relationships))
		for _, rel := range n.Relationships {
			rels = append(rels, simplifyValue(rel))
		}
		return map[string]any{
			"nodes":         nodes,
			"relationships": rels,
		}
	default:
		return v
	}
}
