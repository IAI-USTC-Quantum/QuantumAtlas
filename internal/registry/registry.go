package registry

import (
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

// ErrCatalogUnavailable signals that the PostgreSQL registry could not be
// reached for this operation. Callers treat it as non-fatal (the pool is
// optional so local dev can run without PostgreSQL).
var ErrCatalogUnavailable = errors.New("registry: catalog backend unavailable")

// ErrTitleOnlyRef signals that ResolveOrMint was given a reference with
// no authoritative identity (neither DOI nor arXiv id). Title-only
// references never mint a new paper — the title hash alone is too weak
// to anchor identity on.
var ErrTitleOnlyRef = errors.New("registry: cannot mint from a title-only reference")

// pgUniqueViolation is the PostgreSQL error code for unique_violation.
const pgUniqueViolation = "23505"

// Store is the PostgreSQL-backed registry. Construct with NewStore; the
// pool may be nil (local dev without PostgreSQL) in which case every
// operation reports ErrCatalogUnavailable.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps a pgxpool.Pool (which may be nil). The caller owns the
// pool lifecycle (Close at shutdown).
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Pool exposes the underlying pgx pool for read-only infrastructure
// consumers (health probes, the catalog search provider). It may be nil.
func (s *Store) Pool() *pgxpool.Pool {
	if s == nil {
		return nil
	}
	return s.pool
}

// ensure returns true when the registry is configured. Individual
// queries surface connectivity failures as ErrCatalogUnavailable.
func (s *Store) ensure(ctx context.Context) bool {
	_ = ctx
	return s != nil && s.pool != nil
}

// Available reports whether the registry backend is usable right now.
func (s *Store) Available(ctx context.Context) bool {
	return s.ensure(ctx)
}

// Configured reports whether a PostgreSQL pool was provided at all.
func (s *Store) Configured() bool {
	return s != nil && s.pool != nil
}

// PaperRef is the multi-identity input to ResolveOrMint. At least one of
// ArxivID / DOI is required — a title-only reference never mints
// (ErrTitleOnlyRef). ArxivID may carry a version suffix; OpenAlexID,
// Title, Authors, and Year are optional enrichment used for backfill and
// the title identity.
type PaperRef struct {
	ArxivID    string
	DOI        string
	OpenAlexID string
	Title      string
	Authors    []string
	Year       int
}

// Paper projects one papers row.
type Paper struct {
	PaperID    string
	ArxivID    string
	DOI        string
	OpenAlexID string
	TitleHash  string
	Title      string
	Authors    []string
	Status     string
	PaperRef   string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Stats holds aggregate registry counters by lifecycle status.
type Stats struct {
	Total   int
	Pending int
	Ready   int
	Failed  int
}

// normalizedRef is a PaperRef reduced to canonical storage forms.
type normalizedRef struct {
	doi      string
	arxiv    string // bare, version-stripped
	version  int    // 0 when the ref carried no version
	openalex string
	title    string
	authors  []string
	year     int
}

// normalizeRef reduces a PaperRef to canonical forms via the
// paperassets / identity normalization helpers.
func normalizeRef(ref PaperRef) normalizedRef {
	return normalizedRef{
		doi:      NormalizeDOI(ref.DOI),
		arxiv:    NormalizeArxivID(ref.ArxivID),
		version:  ArxivVersionOf(ref.ArxivID),
		openalex: strings.TrimSpace(ref.OpenAlexID),
		title:    strings.TrimSpace(ref.Title),
		authors:  ref.Authors,
		year:     ref.Year,
	}
}

// identityKeys builds every identity key this reference anchors, in
// priority order doi > arxiv > arxiv_version > title.
func (n normalizedRef) identityKeys() []identityKey {
	var keys []identityKey
	if n.doi != "" {
		keys = append(keys, identityKey{DOIKey(n.doi), KindDOI})
	}
	if n.arxiv != "" {
		keys = append(keys, identityKey{ArxivKey(n.arxiv), KindArxiv})
		if n.version > 0 {
			full := n.arxiv + "v" + strconv.Itoa(n.version)
			keys = append(keys, identityKey{ArxivVersionKey(full), KindArxivVersion})
		}
	}
	if n.title != "" {
		keys = append(keys, identityKey{TitleKey(TitleHash(n.title, n.authors, n.year)), KindTitle})
	}
	return keys
}

// titleHash returns the title identity hash, or "" when the ref has no
// title.
func (n normalizedRef) titleHash() string {
	if n.title == "" {
		return ""
	}
	return TitleHash(n.title, n.authors, n.year)
}

// newPaperID mints a surrogate id: "qa_" + 26-char lowercase ULID with
// crypto/rand entropy.
func newPaperID() string {
	id := ulid.MustNew(ulid.Now(), crand.Reader)
	return "qa_" + strings.ToLower(id.String())
}

// querier abstracts *pgxpool.Pool and pgx.Tx so the lookup path can run
// inside or outside a transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ResolveOrMint is the single transactional entry point of the registry.
// It resolves ref through paper_identities and the papers UNIQUE
// columns; on a hit it backfills missing identity keys and NULL columns
// (merging distinct papers that the new reference proves to be the same
// work — the older paper_id survives) and returns created=false. On a
// miss it mints a new paper + identity rows in one transaction,
// returning created=true. A concurrent mint surfaces as a unique
// violation, in which case the lookup path is re-run once and the
// existing paper_id is returned.
func (s *Store) ResolveOrMint(ctx context.Context, ref PaperRef) (paperID string, created bool, err error) {
	if !s.ensure(ctx) {
		return "", false, ErrCatalogUnavailable
	}
	n := normalizeRef(ref)
	if n.doi == "" && n.arxiv == "" {
		return "", false, ErrTitleOnlyRef
	}
	keys := n.identityKeys()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", false, catalogUnavailable("registry: begin resolve-or-mint tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id, minted, err := resolveOrMintTx(ctx, tx, n, keys)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			// A concurrent mint won the race: re-run the lookup path once
			// (outside the aborted tx) and return the existing paper.
			id, found, lerr := s.lookupPaper(ctx, keys, n)
			if lerr != nil {
				return "", false, lerr
			}
			if found {
				return id, false, nil
			}
			return "", false, fmt.Errorf("registry: mint raced but re-lookup found no paper: %w", err)
		}
		return "", false, fmt.Errorf("registry: resolve-or-mint: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, catalogUnavailable("registry: commit resolve-or-mint", err)
	}
	return id, minted, nil
}

// resolveOrMintTx is the ResolveOrMint body running inside tx.
func resolveOrMintTx(ctx context.Context, tx pgx.Tx, n normalizedRef, keys []identityKey) (string, bool, error) {
	candidates, err := findCandidates(ctx, tx, keys, n)
	if err != nil {
		return "", false, err
	}
	if len(candidates) > 0 {
		survivor, losers, err := pickSurvivor(ctx, tx, candidates)
		if err != nil {
			return "", false, err
		}
		for _, loser := range losers {
			if err := mergeInto(ctx, tx, survivor, loser); err != nil {
				return "", false, err
			}
		}
		if err := backfillIdentities(ctx, tx, survivor, keys); err != nil {
			return "", false, err
		}
		if err := backfillColumns(ctx, tx, survivor, n); err != nil {
			return "", false, err
		}
		return survivor, false, nil
	}

	// Mint.
	id := newPaperID()
	if _, err := tx.Exec(ctx, `
		INSERT INTO papers (paper_id, arxiv_id, doi, openalex_id, title_hash, title, authors)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, nullStr(n.arxiv), nullStr(n.doi), nullStr(n.openalex),
		nullStr(n.titleHash()), nullStr(n.title), n.authors); err != nil {
		return "", false, err
	}
	if err := backfillIdentities(ctx, tx, id, keys); err != nil {
		return "", false, err
	}
	return id, true, nil
}

// findCandidates returns the distinct paper_ids any of the reference's
// identity keys or UNIQUE-column values already point at.
func findCandidates(ctx context.Context, q querier, keys []identityKey, n normalizedRef) ([]string, error) {
	seen := map[string]bool{}
	var ids []string
	add := func(rows pgx.Rows, err error) error {
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		return rows.Err()
	}

	if len(keys) > 0 {
		keyStrs := make([]string, len(keys))
		for i, k := range keys {
			keyStrs[i] = k.key
		}
		if err := add(q.Query(ctx,
			`SELECT DISTINCT paper_id FROM paper_identities WHERE identity_key = ANY($1)`, keyStrs)); err != nil {
			return nil, fmt.Errorf("registry: lookup identities: %w", err)
		}
	}
	if err := add(q.Query(ctx, `
		SELECT paper_id FROM papers
		WHERE ($1::text IS NOT NULL AND arxiv_id = $1)
		   OR ($2::text IS NOT NULL AND doi = $2)
		   OR ($3::text IS NOT NULL AND openalex_id = $3)`,
		nullStr(n.arxiv), nullStr(n.doi), nullStr(n.openalex))); err != nil {
		return nil, fmt.Errorf("registry: lookup papers unique columns: %w", err)
	}
	return ids, nil
}

// lookupPaper resolves the reference to a single existing paper_id (the
// oldest when several match). Used by the concurrent-mint retry path.
func (s *Store) lookupPaper(ctx context.Context, keys []identityKey, n normalizedRef) (string, bool, error) {
	candidates, err := findCandidates(ctx, s.pool, keys, n)
	if err != nil {
		return "", false, err
	}
	if len(candidates) == 0 {
		return "", false, nil
	}
	survivor, _, err := pickSurvivor(ctx, s.pool, candidates)
	if err != nil {
		return "", false, err
	}
	return survivor, true, nil
}

// pickSurvivor orders candidates by created_at (ties broken by paper_id)
// and returns the oldest as the survivor; the rest are losers to merge.
func pickSurvivor(ctx context.Context, q querier, ids []string) (survivor string, losers []string, err error) {
	rows, err := q.Query(ctx,
		`SELECT paper_id FROM papers WHERE paper_id = ANY($1) ORDER BY created_at ASC, paper_id ASC`, ids)
	if err != nil {
		return "", nil, fmt.Errorf("registry: order candidates: %w", err)
	}
	defer rows.Close()
	var ordered []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", nil, fmt.Errorf("registry: scan candidate: %w", err)
		}
		ordered = append(ordered, id)
	}
	if err := rows.Err(); err != nil {
		return "", nil, fmt.Errorf("registry: iterate candidates: %w", err)
	}
	if len(ordered) == 0 {
		return "", nil, errors.New("registry: candidate papers vanished")
	}
	return ordered[0], ordered[1:], nil
}

// mergeInto folds loser into survivor: re-point the loser's identities
// and assets, then mark the loser merged. The loser's external-id
// columns stay in place (the papers_at_least_one_id CHECK forbids
// clearing them); the paper_identities rows now route every lookup to
// the survivor.
func mergeInto(ctx context.Context, tx pgx.Tx, survivor, loser string) error {
	if _, err := tx.Exec(ctx,
		`UPDATE paper_identities SET paper_id = $1 WHERE paper_id = $2`, survivor, loser); err != nil {
		return fmt.Errorf("registry: re-point identities %s -> %s: %w", loser, survivor, err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE paper_assets SET paper_id = $1 WHERE paper_id = $2`, survivor, loser); err != nil {
		return fmt.Errorf("registry: re-point assets %s -> %s: %w", loser, survivor, err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE papers SET status = 'merged_into:' || $1, updated_at = now() WHERE paper_id = $2`,
		survivor, loser); err != nil {
		return fmt.Errorf("registry: mark merged %s -> %s: %w", loser, survivor, err)
	}
	return nil
}

// backfillIdentities inserts any identity keys the survivor does not yet
// hold. Keys already owned by the survivor are no-ops.
func backfillIdentities(ctx context.Context, tx pgx.Tx, paperID string, keys []identityKey) error {
	for _, k := range keys {
		if _, err := tx.Exec(ctx, `
			INSERT INTO paper_identities (identity_key, paper_id, kind)
			VALUES ($1, $2, $3)
			ON CONFLICT (identity_key) DO NOTHING`, k.key, paperID, k.kind); err != nil {
			return fmt.Errorf("registry: backfill identity %s: %w", k.key, err)
		}
	}
	return nil
}

// backfillColumns fills still-NULL columns on the survivor from the
// reference. External-id columns are only claimed when no other papers
// row holds the value (a merged loser keeps its columns), preserving the
// UNIQUE constraints. Always bumps updated_at.
func backfillColumns(ctx context.Context, tx pgx.Tx, paperID string, n normalizedRef) error {
	claims := []struct {
		column string
		value  string
	}{
		{"arxiv_id", n.arxiv},
		{"doi", n.doi},
		{"openalex_id", n.openalex},
	}
	for _, c := range claims {
		if c.value == "" {
			continue
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`
			UPDATE papers SET %[1]s = $1, updated_at = now()
			WHERE paper_id = $2 AND %[1]s IS NULL
			  AND NOT EXISTS (SELECT 1 FROM papers WHERE %[1]s = $1)`, c.column),
			c.value, paperID); err != nil {
			return fmt.Errorf("registry: backfill %s on %s: %w", c.column, paperID, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE papers SET
			title = COALESCE(title, $1),
			title_hash = COALESCE(title_hash, $2),
			authors = CASE
				WHEN coalesce(cardinality(authors), 0) = 0
				 AND coalesce(cardinality($3::text[]), 0) > 0 THEN $3
				ELSE authors
			END,
			updated_at = now()
		WHERE paper_id = $4`,
		nullStr(n.title), nullStr(n.titleHash()), n.authors, paperID); err != nil {
		return fmt.Errorf("registry: backfill title on %s: %w", paperID, err)
	}
	return nil
}

// Get returns one paper by surrogate id. found=false when no such row.
func (s *Store) Get(ctx context.Context, paperID string) (p *Paper, found bool, err error) {
	if !s.ensure(ctx) {
		return nil, false, ErrCatalogUnavailable
	}
	var (
		arxiv, doi, openalex *string
		titleHash, title     *string
		paperRef             *string
		row                  Paper
	)
	err = s.pool.QueryRow(ctx, `
		SELECT paper_id, arxiv_id, doi, openalex_id, title_hash, title,
		       coalesce(authors, '{}'), status, paper_ref, created_at, updated_at
		FROM papers WHERE paper_id = $1`, paperID,
	).Scan(&row.PaperID, &arxiv, &doi, &openalex, &titleHash, &title,
		&row.Authors, &row.Status, &paperRef, &row.CreatedAt, &row.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, catalogUnavailable("registry: get "+paperID, err)
	}
	row.ArxivID = deref(arxiv)
	row.DOI = deref(doi)
	row.OpenAlexID = deref(openalex)
	row.TitleHash = deref(titleHash)
	row.Title = deref(title)
	row.PaperRef = deref(paperRef)
	return &row, true, nil
}

// LookupByIdentity resolves one identity key to its owning paper_id.
// found=false when the key is unknown.
func (s *Store) LookupByIdentity(ctx context.Context, identityKey string) (paperID string, found bool, err error) {
	if !s.ensure(ctx) {
		return "", false, ErrCatalogUnavailable
	}
	err = s.pool.QueryRow(ctx,
		`SELECT paper_id FROM paper_identities WHERE identity_key = $1`, identityKey).Scan(&paperID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, catalogUnavailable("registry: lookup identity "+identityKey, err)
	}
	return paperID, true, nil
}

// UpdateStatus sets the lifecycle status of one paper (bumping
// updated_at). found=false when no such paper.
func (s *Store) UpdateStatus(ctx context.Context, paperID, status string) (found bool, err error) {
	if !s.ensure(ctx) {
		return false, ErrCatalogUnavailable
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE papers SET status = $2, updated_at = now() WHERE paper_id = $1`, paperID, status)
	if err != nil {
		return false, catalogUnavailable("registry: update status "+paperID, err)
	}
	return tag.RowsAffected() > 0, nil
}

// QueryStats returns aggregate registry counters by lifecycle status.
// Papers merged into another (status 'merged_into:...') count toward
// Total only.
func (s *Store) QueryStats(ctx context.Context) (Stats, error) {
	var st Stats
	if !s.ensure(ctx) {
		return st, ErrCatalogUnavailable
	}
	var total, pending, ready, failed int64
	err := s.pool.QueryRow(ctx, `
		SELECT
			count(*)::bigint,
			count(*) FILTER (WHERE status = 'pending')::bigint,
			count(*) FILTER (WHERE status = 'ready')::bigint,
			count(*) FILTER (WHERE status = 'failed')::bigint
		FROM papers`).Scan(&total, &pending, &ready, &failed)
	if err != nil {
		return st, catalogUnavailable("registry: query stats", err)
	}
	st.Total = int(total)
	st.Pending = int(pending)
	st.Ready = int(ready)
	st.Failed = int(failed)
	return st, nil
}

// catalogUnavailable annotates a connectivity failure with the
// ErrCatalogUnavailable sentinel, mirroring internal/papers.
func catalogUnavailable(op string, err error) error {
	if err == nil {
		return ErrCatalogUnavailable
	}
	return fmt.Errorf("%s: %w (%v)", op, ErrCatalogUnavailable, err)
}

// nullStr maps "" to SQL NULL.
func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// deref maps SQL NULL to "".
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
