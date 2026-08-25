package registry

// backfill.go: metadata backfill for papers minted without metadata.
// `papers sync` rebuilds the catalog from bucket listings, which yields
// rows with an arxiv_id but NULL title/authors (the "untitled" papers).
// The `papers backfill-metadata` CLI walks them via ListUntitled (arXiv
// source) / ListUntitledDOI (OpenAlex source) and fills title / authors /
// publication_date / abstract via UpdateMetadata. Identity resolution
// (ResolveOrMint) stays the only merge path — backfill never merges, it
// only claims an external id (DOI / arXiv id) when no other row owns it.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// UntitledPaper is the minimal projection ListUntitled returns: the
// surrogate id plus the bare arXiv id to query metadata for.
type UntitledPaper struct {
	PaperID string
	ArxivID string
}

// UntitledDOIPaper is the minimal projection ListUntitledDOI returns:
// the surrogate id plus the normalized DOI to query metadata for.
type UntitledDOIPaper struct {
	PaperID string
	DOI     string
}

// Metadata is the per-paper metadata a backfill source (arXiv API,
// OpenAlex) supplies. Zero-value fields are left untouched by
// UpdateMetadata.
type Metadata struct {
	Title           string
	Authors         []string
	Year            int // publication year; feeds the title_hash identity
	PublicationDate time.Time
	Abstract        string
	DOI             string
	ArxivID         string // claimed like DOI when the paper has none yet
}

// MetadataClaims reports which guarded external-id claims UpdateMetadata
// actually performed.
type MetadataClaims struct {
	DOIClaimed   bool
	ArxivClaimed bool
}

// ListUntitled returns up to limit untitled papers (title IS NULL or
// empty) that carry an arxiv_id, excluding merged tombstones, ordered by
// paper_id ascending. afterPaperID keyset-paginates: only rows with
// paper_id > afterPaperID are returned ("" starts from the beginning).
func (s *Store) ListUntitled(ctx context.Context, afterPaperID string, limit int) ([]UntitledPaper, error) {
	if !s.ensure(ctx) {
		return nil, ErrCatalogUnavailable
	}
	rows, err := s.pool.Query(ctx, `
		SELECT paper_id, arxiv_id FROM papers
		WHERE (title IS NULL OR title = '')
		  AND arxiv_id IS NOT NULL
		  AND status NOT LIKE 'merged_into:%'
		  AND paper_id > $1
		ORDER BY paper_id
		LIMIT $2`, afterPaperID, limit)
	if err != nil {
		return nil, catalogUnavailable("registry: list untitled papers", err)
	}
	defer rows.Close()
	out := []UntitledPaper{}
	for rows.Next() {
		var p UntitledPaper
		if err := rows.Scan(&p.PaperID, &p.ArxivID); err != nil {
			return nil, catalogUnavailable("registry: scan untitled paper row", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, catalogUnavailable("registry: iterate untitled papers", err)
	}
	return out, nil
}

// ListUntitledDOI is ListUntitled keyed on DOI instead of arXiv id:
// untitled papers carrying a doi (arxiv_id may or may not be set — many
// papers have both, and filling the title by DOI is fine either way).
// Same keyset shape: ordered by paper_id, paper_id > afterPaperID.
func (s *Store) ListUntitledDOI(ctx context.Context, afterPaperID string, limit int) ([]UntitledDOIPaper, error) {
	if !s.ensure(ctx) {
		return nil, ErrCatalogUnavailable
	}
	rows, err := s.pool.Query(ctx, `
		SELECT paper_id, doi FROM papers
		WHERE (title IS NULL OR title = '')
		  AND doi IS NOT NULL
		  AND status NOT LIKE 'merged_into:%'
		  AND paper_id > $1
		ORDER BY paper_id
		LIMIT $2`, afterPaperID, limit)
	if err != nil {
		return nil, catalogUnavailable("registry: list untitled doi papers", err)
	}
	defer rows.Close()
	out := []UntitledDOIPaper{}
	for rows.Next() {
		var p UntitledDOIPaper
		if err := rows.Scan(&p.PaperID, &p.DOI); err != nil {
			return nil, catalogUnavailable("registry: scan untitled doi paper row", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, catalogUnavailable("registry: iterate untitled doi papers", err)
	}
	return out, nil
}

// UpdateMetadata fills a paper's metadata columns from md: each field is
// written only when md carries a non-empty / non-zero value for it
// (title, authors, publication_date, abstract). A non-empty title also
// recomputes title_hash via TitleHash(title, authors, year) so the title
// identity stays consistent with the byline. updated_at is bumped on
// every call.
//
// External-id handling (md.DOI / md.ArxivID) is claim-only: when the
// value is non-empty and the paper's column is still NULL, it is set
// only if no other papers row owns it (the UNIQUE constraint plus a
// possible merged loser holding the value). On a successful claim the
// matching identity row (doi:<doi> / arxiv:<id>) is inserted (ON
// CONFLICT DO NOTHING) and the corresponding MetadataClaims flag reports
// true. An existing value is never overwritten and no merge is attempted
// here.
func (s *Store) UpdateMetadata(ctx context.Context, paperID string, md Metadata) (claims MetadataClaims, err error) {
	if !s.ensure(ctx) {
		return MetadataClaims{}, ErrCatalogUnavailable
	}

	title := strings.TrimSpace(md.Title)
	abstract := strings.TrimSpace(md.Abstract)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MetadataClaims{}, catalogUnavailable("registry: begin update-metadata tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Single UPDATE with per-field guards: NULL arguments leave the
	// column untouched, so only the fields md actually carries move.
	var pubDate *time.Time
	if !md.PublicationDate.IsZero() {
		pubDate = &md.PublicationDate
	}
	var titleHash *string
	if title != "" {
		h := TitleHash(title, md.Authors, md.Year)
		titleHash = &h
	}
	var authors []string
	if len(md.Authors) > 0 {
		authors = md.Authors
	}
	tag, err := tx.Exec(ctx, `
		UPDATE papers SET
			title            = COALESCE($2, title),
			authors          = COALESCE($3, authors),
			publication_date = COALESCE($4, publication_date),
			abstract         = COALESCE($5, abstract),
			title_hash       = COALESCE($6, title_hash),
			updated_at       = now()
		WHERE paper_id = $1`,
		paperID, nullStr(title), authors, pubDate, nullStr(abstract), titleHash)
	if err != nil {
		return MetadataClaims{}, catalogUnavailable("registry: update metadata "+paperID, err)
	}
	if tag.RowsAffected() == 0 {
		return MetadataClaims{}, nil // no such paper; nothing to claim either
	}

	if claims.DOIClaimed, err = claimExternalID(ctx, tx, paperID,
		"doi", NormalizeDOI(md.DOI), DOIKey(NormalizeDOI(md.DOI)), KindDOI); err != nil {
		return MetadataClaims{}, err
	}
	arxiv := NormalizeArxivID(md.ArxivID)
	if claims.ArxivClaimed, err = claimExternalID(ctx, tx, paperID,
		"arxiv_id", arxiv, ArxivKey(arxiv), KindArxiv); err != nil {
		return MetadataClaims{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return MetadataClaims{}, catalogUnavailable("registry: commit update-metadata "+paperID, err)
	}
	return claims, nil
}

// claimExternalID sets column (doi | arxiv_id) on the paper when value
// is non-empty, the paper's column is still NULL, and no other papers
// row owns the value. On a successful claim the identity row (key/kind)
// is inserted (ON CONFLICT DO NOTHING) and true is returned. An empty
// value is a no-op.
func claimExternalID(ctx context.Context, tx pgx.Tx, paperID, column, value, identityKey, kind string) (bool, error) {
	if value == "" {
		return false, nil
	}
	tag, err := tx.Exec(ctx, fmt.Sprintf(`
		UPDATE papers SET %[1]s = $1, updated_at = now()
		WHERE paper_id = $2 AND %[1]s IS NULL
		  AND NOT EXISTS (SELECT 1 FROM papers WHERE %[1]s = $1)`, column),
		value, paperID)
	if err != nil {
		return false, catalogUnavailable("registry: claim "+column+" on "+paperID, err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO paper_identities (identity_key, paper_id, kind)
		VALUES ($1, $2, $3)
		ON CONFLICT (identity_key) DO NOTHING`, identityKey, paperID, kind); err != nil {
		return false, catalogUnavailable("registry: insert "+kind+" identity on "+paperID, err)
	}
	return true, nil
}
