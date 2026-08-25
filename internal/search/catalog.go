package search

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CatalogProvider searches the PostgreSQL paper registry itself. Exact
// identity lookups (DOI / arXiv id) score 1.0; tokenized title matches
// score 0.5. A nil pool (local dev without PostgreSQL) reports
// registry.ErrCatalogUnavailable through LastError and yields no hits.
type CatalogProvider struct {
	BaseProvider
	pool *pgxpool.Pool
}

// NewCatalogProvider wraps a pgxpool.Pool (which may be nil). The caller
// owns the pool lifecycle.
func NewCatalogProvider(pool *pgxpool.Pool) *CatalogProvider {
	return &CatalogProvider{pool: pool}
}

// Name implements Provider.
func (p *CatalogProvider) Name() string { return "catalog" }

// Search implements Provider. Backend failures follow the provider
// contract: recorded via BaseProvider, reported as (nil, nil).
func (p *CatalogProvider) Search(ctx context.Context, e SearchEntry) ([]Hit, error) {
	if p.pool == nil {
		return p.RecordFailure(registry.ErrCatalogUnavailable)
	}
	if doi := registry.NormalizeDOI(e.DOI); doi != "" {
		return p.exactLookup(ctx, "doi", doi)
	}
	if arxiv := registry.NormalizeArxivID(e.ArxivID); arxiv != "" {
		return p.exactLookup(ctx, "arxiv_id", arxiv)
	}
	if tokens := queryTokens(e.Text + " " + e.Title); len(tokens) > 0 {
		return p.titleSearch(ctx, tokens, e.MaxResults)
	}
	return nil, nil
}

// exactLookup matches one paper by its UNIQUE external-id column.
func (p *CatalogProvider) exactLookup(ctx context.Context, column, value string) ([]Hit, error) {
	// column is one of two hardcoded literals from Search, never user input.
	row := p.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT coalesce(arxiv_id, ''), coalesce(doi, ''), coalesce(title, '')
		FROM papers WHERE %[1]s = $1`, column), value)
	var arxiv, doi, title string
	if err := row.Scan(&arxiv, &doi, &title); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return p.RecordFailure(fmt.Errorf("search: catalog exact lookup: %w", err))
	}
	return []Hit{{ArxivID: arxiv, DOI: doi, Title: title, Score: 1.0, Source: p.Name()}}, nil
}

// titleSearch matches papers whose title contains every query token
// (case-insensitive), newest first.
func (p *CatalogProvider) titleSearch(ctx context.Context, tokens []string, limit int) ([]Hit, error) {
	var sb strings.Builder
	sb.WriteString(`SELECT coalesce(arxiv_id, ''), coalesce(doi, ''), coalesce(title, '') FROM papers WHERE `)
	args := make([]any, 0, len(tokens)+1)
	for i, tok := range tokens {
		if i > 0 {
			sb.WriteString(" AND ")
		}
		args = append(args, "%"+tok+"%")
		fmt.Fprintf(&sb, "title ILIKE $%d", len(args))
	}
	args = append(args, limit)
	fmt.Fprintf(&sb, " ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := p.pool.Query(ctx, sb.String(), args...)
	if err != nil {
		return p.RecordFailure(fmt.Errorf("search: catalog title search: %w", err))
	}
	defer rows.Close()

	var hits []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ArxivID, &h.DOI, &h.Title); err != nil {
			return p.RecordFailure(fmt.Errorf("search: catalog scan hit: %w", err))
		}
		h.Score = 0.5
		h.Source = p.Name()
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return p.RecordFailure(fmt.Errorf("search: catalog iterate hits: %w", err))
	}
	return hits, nil
}

// queryTokens splits raw into lowercase alphanumeric tokens, mirroring
// registry's title normalization.
func queryTokens(raw string) []string {
	return strings.FieldsFunc(strings.ToLower(raw), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
