package openalex

import (
	"context"
	"fmt"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/neo4j"
)

// IngestBatch is the default number of works MERGEd per transaction.
const IngestBatch = 1000

// nodeRow is the flattened, Neo4j-friendly projection of a Work that
// has an arxiv id. Only arxiv-bearing works become :PaperWork nodes;
// the OpenAlex id is retained so the second (citation) pass can match
// referenced_works against it.
type nodeRow struct {
	ArxivID    string `json:"arxiv_id"`
	OpenAlexID string `json:"openalex_id"`
	DOI        string `json:"doi"`
	Title      string `json:"title"`
	PubDate    string `json:"publication_date"`
	CitedBy    int    `json:"cited_by_count"`
	Refs       []string
}

// IngestWorks upserts a slice of works into the :PaperWork layer in
// batches. Works without an arxiv id are skipped (we only catalog the
// arxiv-reachable subset). source is set to 'openalex' on create; an
// existing 'arxiv-fallback' node is upgraded to 'openalex' so a paper
// uploaded before bootstrap gets enriched. Citation edges are written
// in a second pass (IngestCitations) once all nodes exist.
//
// Returns (nodesUpserted, error).
func IngestWorks(ctx context.Context, nc *neo4j.Client, works []Work) (int, error) {
	if nc == nil || !nc.Connected() {
		return 0, fmt.Errorf("openalex: neo4j not connected")
	}
	rows := make([]map[string]any, 0, len(works))
	for _, w := range works {
		arxiv := ExtractArxivID(w)
		if arxiv == "" {
			continue
		}
		rows = append(rows, map[string]any{
			"arxiv_id":         arxiv,
			"openalex_id":      shortID(w.ID),
			"doi":              shortDOI(w.DOI),
			"title":            w.Title,
			"publication_date": w.PublicationDate,
			"cited_by_count":   w.CitedByCount,
		})
	}
	cypher := `
		UNWIND $rows AS r
		MERGE (p:PaperWork {arxiv_id: r.arxiv_id})
		ON CREATE SET p.source = 'openalex', p.has_json = false
		SET p.openalex_id = r.openalex_id,
		    p.doi = r.doi,
		    p.title = coalesce(p.title, r.title),
		    p.publication_date = r.publication_date,
		    p.cited_by_count__derived = r.cited_by_count,
		    p.source = CASE WHEN p.source = 'arxiv-fallback' THEN 'openalex' ELSE p.source END`

	n := 0
	for i := 0; i < len(rows); i += IngestBatch {
		end := i + IngestBatch
		if end > len(rows) {
			end = len(rows)
		}
		chunk := rows[i:end]
		if _, err := nc.ExecuteWrite(ctx, cypher, map[string]any{"rows": chunk}); err != nil {
			return n, fmt.Errorf("openalex: ingest works batch: %w", err)
		}
		n += len(chunk)
	}
	return n, nil
}

// IngestCitations writes PAPER_CITES edges for the given works. Edges
// are created only between :PaperWork nodes that already exist (matched
// by openalex_id), so dangling references to non-arxiv works are
// silently dropped — the catalog is the arxiv-reachable subgraph.
func IngestCitations(ctx context.Context, nc *neo4j.Client, works []Work) (int, error) {
	if nc == nil || !nc.Connected() {
		return 0, fmt.Errorf("openalex: neo4j not connected")
	}
	rows := make([]map[string]any, 0, len(works))
	for _, w := range works {
		if len(w.ReferencedWorks) == 0 {
			continue
		}
		refs := make([]string, 0, len(w.ReferencedWorks))
		for _, r := range w.ReferencedWorks {
			refs = append(refs, shortID(r))
		}
		rows = append(rows, map[string]any{
			"src":  shortID(w.ID),
			"refs": refs,
		})
	}
	cypher := `
		UNWIND $rows AS r
		MATCH (src:PaperWork {openalex_id: r.src})
		UNWIND r.refs AS ref
		MATCH (dst:PaperWork {openalex_id: ref})
		MERGE (src)-[:PAPER_CITES]->(dst)`

	n := 0
	for i := 0; i < len(rows); i += IngestBatch {
		end := i + IngestBatch
		if end > len(rows) {
			end = len(rows)
		}
		chunk := rows[i:end]
		if _, err := nc.ExecuteWrite(ctx, cypher, map[string]any{"rows": chunk}); err != nil {
			return n, fmt.Errorf("openalex: ingest citations batch: %w", err)
		}
		n += len(chunk)
	}
	return n, nil
}
