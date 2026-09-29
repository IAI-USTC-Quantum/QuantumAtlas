package registry

// blocksources.go: Q1 block-comments storage (plan §12.3, migration
// 00007) — immutable source-PDF identities (paper_sources) and immutable
// parse revisions (parse_revisions). Rows are append-only: the only
// mutable state is the is_current pointer on parse_revisions, flipped
// transactionally by InsertParseRevision so a new parse never overwrites
// the old artifact (plan §4.2: "新解析更新当前结果指针，不覆写旧产物").
//
// Reads are scoped by paper_id everywhere: a source / revision id that
// exists under another paper answers found=false, never leaks across
// papers.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	crand "crypto/rand"
	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"
)

// PaperSource projects one paper_sources row: the immutable identity of
// one source PDF byte-set (origin label + sha256 + object-store key).
type PaperSource struct {
	SourceID    string
	PaperID     string
	Origin      string // e.g. "arxiv:v2", "upload" — free-form label
	Sha256      string
	ObjstoreKey string
	SizeBytes   int64
	CreatedAt   time.Time
}

// ParseRevision projects one parse_revisions row: one immutable parse
// artifact (MinerU Middle JSON) bound to the source it was parsed from.
type ParseRevision struct {
	RevisionID     string
	PaperID        string
	SourceID       string
	Schema         string
	SchemaVersion  string
	ArtifactSha256 string
	ObjstoreKey    string
	Tier           string // parse tier (00009): 'standard' | 'lite' | ... — locator component, not identity
	CreatedAt      time.Time
	IsCurrent      bool
}

// ParseTierDefault is the tier recorded when the ingest path cannot
// observe one (00009 column default; plan §5.1 tier semantics).
const ParseTierDefault = "standard"

// NormalizeParseTier coerces a raw tier label into a storable value:
// lowercased ASCII, [a-z0-9_-]{1,32}; anything else (including empty)
// becomes ParseTierDefault so the locator never carries garbage.
func NormalizeParseTier(tier string) string {
	tier = strings.ToLower(strings.TrimSpace(tier))
	if tier == "" || len(tier) > 32 {
		return ParseTierDefault
	}
	for i := 0; i < len(tier); i++ {
		c := tier[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return ParseTierDefault
		}
	}
	return tier
}

// NewSourceID mints a source id: "src_" + lowercase ULID.
func NewSourceID() string {
	return "src_" + strings.ToLower(ulid.MustNew(ulid.Now(), crand.Reader).String())
}

// NewParseRevisionID mints a revision id: "pr_" + lowercase ULID.
func NewParseRevisionID() string {
	return "pr_" + strings.ToLower(ulid.MustNew(ulid.Now(), crand.Reader).String())
}

// ListPaperSources returns the paper's sources ordered oldest-first
// (created_at, then source_id for determinism).
func (s *Store) ListPaperSources(ctx context.Context, paperID string) ([]PaperSource, error) {
	if !s.ensure(ctx) {
		return nil, ErrCatalogUnavailable
	}
	rows, err := s.pool.Query(ctx, `
		SELECT source_id, paper_id, origin, sha256, objstore_key, size_bytes, created_at
		FROM paper_sources WHERE paper_id = $1
		ORDER BY created_at ASC, source_id ASC`, paperID)
	if err != nil {
		return nil, catalogUnavailable("registry: list paper sources "+paperID, err)
	}
	defer rows.Close()
	var out []PaperSource
	for rows.Next() {
		src, err := scanPaperSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// GetPaperSource returns one source scoped to the paper. found=false
// when the paper has no such source (including when the id exists under
// another paper).
func (s *Store) GetPaperSource(ctx context.Context, paperID, sourceID string) (PaperSource, bool, error) {
	if !s.ensure(ctx) {
		return PaperSource{}, false, ErrCatalogUnavailable
	}
	row := s.pool.QueryRow(ctx, `
		SELECT source_id, paper_id, origin, sha256, objstore_key, size_bytes, created_at
		FROM paper_sources WHERE paper_id = $1 AND source_id = $2`, paperID, sourceID)
	src, err := scanPaperSource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return PaperSource{}, false, nil
	}
	if err != nil {
		return PaperSource{}, false, catalogUnavailable("registry: get paper source "+paperID+"/"+sourceID, err)
	}
	return src, true, nil
}

// FindPaperSourceBySHA returns the paper's source row pinning sha256
// (the ingest "reuse over re-mint" lookup: a re-upload of parse output
// for bytes we already know reuses the source identity). found=false
// when this paper has no source with that sha — including when the sha
// exists under another paper (byte-identical files under different
// papers stay distinct sources; sharing would leak anchors across
// papers).
func (s *Store) FindPaperSourceBySHA(ctx context.Context, paperID, sha256 string) (PaperSource, bool, error) {
	if !s.ensure(ctx) {
		return PaperSource{}, false, ErrCatalogUnavailable
	}
	row := s.pool.QueryRow(ctx, `
		SELECT source_id, paper_id, origin, sha256, objstore_key, size_bytes, created_at
		FROM paper_sources WHERE paper_id = $1 AND sha256 = $2
		ORDER BY created_at ASC, source_id ASC LIMIT 1`, paperID, sha256)
	src, err := scanPaperSource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return PaperSource{}, false, nil
	}
	if err != nil {
		return PaperSource{}, false, catalogUnavailable("registry: find paper source by sha "+paperID, err)
	}
	return src, true, nil
}

// InsertPaperSource appends one source row. Idempotent on source_id:
// re-inserting the same id is a no-op (bytes are immutable — the
// conflict row stays authoritative). minted=false when the row already
// existed with different field values.
// TODO(Q1-ingest): surface "same sha under new origin" as a distinct
// outcome when the real ingest path lands; for now callers mint a fresh
// source_id per origin.
func (s *Store) InsertPaperSource(ctx context.Context, src PaperSource) (minted bool, err error) {
	if !s.ensure(ctx) {
		return false, ErrCatalogUnavailable
	}
	if src.SourceID == "" {
		src.SourceID = NewSourceID()
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO paper_sources (source_id, paper_id, origin, sha256, objstore_key, size_bytes)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (source_id) DO NOTHING`,
		src.SourceID, src.PaperID, src.Origin, src.Sha256, src.ObjstoreKey, src.SizeBytes)
	if err != nil {
		return false, catalogUnavailable("registry: insert paper source "+src.SourceID, err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListParseRevisions returns the paper's parse revisions ordered
// oldest-first (created_at, then revision_id).
func (s *Store) ListParseRevisions(ctx context.Context, paperID string) ([]ParseRevision, error) {
	if !s.ensure(ctx) {
		return nil, ErrCatalogUnavailable
	}
	rows, err := s.pool.Query(ctx, `
		SELECT revision_id, paper_id, source_id, schema, schema_version,
		       artifact_sha256, objstore_key, tier, created_at, is_current
		FROM parse_revisions WHERE paper_id = $1
		ORDER BY created_at ASC, revision_id ASC`, paperID)
	if err != nil {
		return nil, catalogUnavailable("registry: list parse revisions "+paperID, err)
	}
	defer rows.Close()
	var out []ParseRevision
	for rows.Next() {
		rev, err := scanParseRevision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rev)
	}
	return out, rows.Err()
}

// GetParseRevision returns one revision scoped to the paper. found=false
// when the paper has no such revision (including when the id exists
// under another paper).
func (s *Store) GetParseRevision(ctx context.Context, paperID, revisionID string) (ParseRevision, bool, error) {
	if !s.ensure(ctx) {
		return ParseRevision{}, false, ErrCatalogUnavailable
	}
	row := s.pool.QueryRow(ctx, `
		SELECT revision_id, paper_id, source_id, schema, schema_version,
		       artifact_sha256, objstore_key, tier, created_at, is_current
		FROM parse_revisions WHERE paper_id = $1 AND revision_id = $2`, paperID, revisionID)
	rev, err := scanParseRevision(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ParseRevision{}, false, nil
	}
	if err != nil {
		return ParseRevision{}, false, catalogUnavailable("registry: get parse revision "+paperID+"/"+revisionID, err)
	}
	return rev, true, nil
}

// InsertParseRevision appends one immutable parse revision. When
// setCurrent is true the whole operation runs in one transaction that
// also clears every other is_current flag for the paper, so the partial
// unique index (parse_revisions_one_current) never rejects the flip and
// the paper always keeps exactly one current revision. With
// setCurrent=false the row lands without touching the pointer.
func (s *Store) InsertParseRevision(ctx context.Context, rev ParseRevision, setCurrent bool) error {
	if !s.ensure(ctx) {
		return ErrCatalogUnavailable
	}
	if rev.RevisionID == "" {
		rev.RevisionID = NewParseRevisionID()
	}
	if setCurrent {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return catalogUnavailable("registry: begin insert parse revision", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `
			UPDATE parse_revisions SET is_current = FALSE
			WHERE paper_id = $1 AND is_current`, rev.PaperID); err != nil {
			return catalogUnavailable("registry: clear current parse revision "+rev.PaperID, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO parse_revisions
			       (revision_id, paper_id, source_id, schema, schema_version,
			        artifact_sha256, objstore_key, tier, is_current)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, TRUE)`,
			rev.RevisionID, rev.PaperID, rev.SourceID, rev.Schema, rev.SchemaVersion,
			rev.ArtifactSha256, rev.ObjstoreKey, NormalizeParseTier(rev.Tier)); err != nil {
			return catalogUnavailable("registry: insert parse revision "+rev.RevisionID, err)
		}
		return tx.Commit(ctx)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO parse_revisions
		       (revision_id, paper_id, source_id, schema, schema_version,
		        artifact_sha256, objstore_key, tier, is_current)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, FALSE)`,
		rev.RevisionID, rev.PaperID, rev.SourceID, rev.Schema, rev.SchemaVersion,
		rev.ArtifactSha256, rev.ObjstoreKey, NormalizeParseTier(rev.Tier))
	if err != nil {
		return catalogUnavailable("registry: insert parse revision "+rev.RevisionID, err)
	}
	return nil
}

// scanner is the shared row-scan surface of pgx.Row and pgx.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanPaperSource(row scanner) (PaperSource, error) {
	var src PaperSource
	if err := row.Scan(&src.SourceID, &src.PaperID, &src.Origin, &src.Sha256,
		&src.ObjstoreKey, &src.SizeBytes, &src.CreatedAt); err != nil {
		return src, fmt.Errorf("registry: scan paper source: %w", err)
	}
	return src, nil
}

func scanParseRevision(row scanner) (ParseRevision, error) {
	var rev ParseRevision
	if err := row.Scan(&rev.RevisionID, &rev.PaperID, &rev.SourceID, &rev.Schema,
		&rev.SchemaVersion, &rev.ArtifactSha256, &rev.ObjstoreKey, &rev.Tier,
		&rev.CreatedAt, &rev.IsCurrent); err != nil {
		return rev, fmt.Errorf("registry: scan parse revision: %w", err)
	}
	return rev, nil
}
