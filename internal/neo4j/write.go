package neo4j

import (
	"context"
	"errors"

	driver "github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

// Connected reports whether the underlying Bolt driver has been opened
// via Connect. Used by the papers catalog to decide between a live
// write-through and a deferred-sync degradation.
func (c *Client) Connected() bool {
	return c.driver != nil
}

// ExecuteWrite runs cypher inside a managed write transaction with the
// supplied parameters and materializes each returned record as a
// map[column]value. Retries on transient errors are handled by the
// driver's managed-transaction machinery.
func (c *Client) ExecuteWrite(ctx context.Context, cypher string, params map[string]any) ([]map[string]any, error) {
	if c.driver == nil {
		return nil, errors.New("neo4j: not connected")
	}
	session := c.session(ctx)
	defer session.Close(ctx)
	res, err := session.ExecuteWrite(ctx, func(tx driver.ManagedTransaction) (any, error) {
		return runCollect(ctx, tx, cypher, params)
	})
	if err != nil {
		return nil, err
	}
	rows, _ := res.([]map[string]any)
	return rows, nil
}

// ExecuteReadParams runs cypher inside a managed read transaction with
// parameters. Unlike ExecuteRead it never rewrites the query (no LIMIT
// injection) and accepts bind parameters, so it is safe for queries
// built from user-derived values.
func (c *Client) ExecuteReadParams(ctx context.Context, cypher string, params map[string]any) ([]map[string]any, error) {
	if c.driver == nil {
		return nil, errors.New("neo4j: not connected")
	}
	session := c.session(ctx)
	defer session.Close(ctx)
	res, err := session.ExecuteRead(ctx, func(tx driver.ManagedTransaction) (any, error) {
		return runCollect(ctx, tx, cypher, params)
	})
	if err != nil {
		return nil, err
	}
	rows, _ := res.([]map[string]any)
	return rows, nil
}

// runCollect runs a statement on a managed transaction and collects all
// records into plain maps (driver wrappers simplified via simplifyValue).
func runCollect(ctx context.Context, tx driver.ManagedTransaction, cypher string, params map[string]any) (any, error) {
	r, err := tx.Run(ctx, cypher, params)
	if err != nil {
		return nil, err
	}
	recs, err := r.Collect(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]map[string]any, 0, len(recs))
	for _, rec := range recs {
		row := map[string]any{}
		for i, k := range rec.Keys {
			row[k] = simplifyValue(rec.Values[i])
		}
		rows = append(rows, row)
	}
	return rows, nil
}
