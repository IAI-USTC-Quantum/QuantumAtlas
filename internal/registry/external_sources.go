package registry

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

const (
	KindEprint    = "eprint"
	KindSourceURL = "source_url"
)

var ErrExternalSourceInvalid = errors.New("registry: invalid external source")
var eprintSourcePath = regexp.MustCompile(`^/([0-9]{4})/([1-9][0-9]*)(?:\.pdf|/pdf)?/?$`)

// NormalizeExternalSourceURL validates URL syntax, not network reachability.
// Every actual connection must additionally enforce a public, pinned DNS result.
func NormalizeExternalSourceURL(raw string) (string, error) {
	if raw == "" || len(raw) > 4096 || strings.TrimSpace(raw) != raw || strings.Contains(raw, "#") || strings.Contains(raw, "\\") {
		return "", fmt.Errorf("%w: expected an absolute HTTPS URL without fragment", ErrExternalSourceInvalid)
	}
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", fmt.Errorf("%w: URL contains whitespace/control characters", ErrExternalSourceInvalid)
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.Host == "" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return "", fmt.Errorf("%w: expected HTTPS, no userinfo or explicit port", ErrExternalSourceInvalid)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || strings.HasSuffix(u.Host, ":") || strings.ContainsAny(host, "%/") {
		return "", fmt.Errorf("%w: invalid hostname", ErrExternalSourceInvalid)
	}
	for _, r := range host {
		if r > 127 {
			return "", fmt.Errorf("%w: hostname must be ASCII (use punycode)", ErrExternalSourceInvalid)
		}
	}
	if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = strings.TrimSuffix(host, ".")
		if u.Host == "" {
			return "", fmt.Errorf("%w: invalid hostname", ErrExternalSourceInvalid)
		}
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}

// ExternalSourceIdentity binds IACR landing/PDF URLs to one work. Other URLs
// stay distinct even if their title or PDF bytes match; queries are retained.
func ExternalSourceIdentity(raw string) (normalizedURL, externalID, kind string, err error) {
	normalizedURL, err = NormalizeExternalSourceURL(raw)
	if err != nil {
		return "", "", "", err
	}
	u, _ := url.Parse(normalizedURL)
	if u.Hostname() == "eprint.iacr.org" && u.RawQuery == "" {
		if m := eprintSourcePath.FindStringSubmatch(u.Path); m != nil {
			return normalizedURL, KindEprint + ":" + m[1] + "/" + m[2], KindEprint, nil
		}
	}
	return normalizedURL, KindSourceURL + ":" + normalizedURL, KindSourceURL, nil
}

// ExternalSourcePDFURL is the direct PDF URL for a recognized ePrint work;
// generic sources must already return PDF bytes (no HTML/browser scraping).
func ExternalSourcePDFURL(raw string) (string, error) {
	normalized, identity, kind, err := ExternalSourceIdentity(raw)
	if err != nil {
		return "", err
	}
	if kind == KindEprint {
		return "https://eprint.iacr.org/" + strings.TrimPrefix(identity, "eprint:") + ".pdf", nil
	}
	return normalized, nil
}

// ExternalSourceRef carries verified PDF provenance and caller bibliographic
// metadata. RegisterExternalSource is deliberately separate from ResolveOrMint:
// title-only references on the latter remain forbidden.
type ExternalSourceRef struct {
	SourceURL string
	Title     string
	Authors   []string
	Year      int
	Source    PaperSource
}

type ExternalSourceRegistration struct {
	PaperID    string
	Created    bool
	ExternalID string
	Source     PaperSource
}

// RegisterExternalSource atomically mints/resolves an external work and appends
// its immutable byte source. A transaction-scoped identity lock serializes
// concurrent retries. No title hash/identity is inserted or consulted.
func (s *Store) RegisterExternalSource(ctx context.Context, ref ExternalSourceRef) (ExternalSourceRegistration, error) {
	var out ExternalSourceRegistration
	if !s.ensure(ctx) {
		return out, ErrCatalogUnavailable
	}
	normalized, externalID, kind, err := ExternalSourceIdentity(ref.SourceURL)
	if err != nil {
		return out, err
	}
	src := ref.Source
	decoded, hashErr := hex.DecodeString(src.Sha256)
	if hashErr != nil || len(decoded) != 32 || strings.ToLower(src.Sha256) != src.Sha256 || src.SizeBytes <= 0 ||
		src.ObjstoreKey != "pdf/external-sources/"+src.Sha256+".pdf" || strings.TrimSpace(ref.Title) == "" || ref.Year < 1 || ref.Year > 9999 {
		return out, fmt.Errorf("%w: title/year or immutable PDF metadata invalid", ErrExternalSourceInvalid)
	}
	if src.RetrievedURL == "" {
		src.RetrievedURL = normalized
	}
	if src.RetrievedURL, err = NormalizeExternalSourceURL(src.RetrievedURL); err != nil {
		return out, err
	}
	if src.RetrievedAt.IsZero() {
		src.RetrievedAt = time.Now().UTC()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, catalogUnavailable("registry: begin external source registration", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, externalID); err != nil {
		return out, catalogUnavailable("registry: lock external identity", err)
	}
	err = tx.QueryRow(ctx, `SELECT paper_id FROM paper_identities WHERE identity_key = $1`, externalID).Scan(&out.PaperID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT paper_id FROM papers WHERE external_id = $1`, externalID).Scan(&out.PaperID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		out.PaperID, out.Created = newPaperID(), true
		_, err = tx.Exec(ctx, `INSERT INTO papers (paper_id, external_id, title, authors, publication_date)
			VALUES ($1, $2, $3, $4, make_date($5, 1, 1))`, out.PaperID, externalID, strings.TrimSpace(ref.Title), ref.Authors, ref.Year)
	}
	if err != nil {
		return out, catalogUnavailable("registry: resolve external work", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO paper_identities (identity_key, paper_id, kind)
		VALUES ($1, $2, $3) ON CONFLICT (identity_key) DO NOTHING`, externalID, out.PaperID, kind); err != nil {
		return out, catalogUnavailable("registry: insert external identity", err)
	}
	src.SourceID, src.PaperID, src.Origin, src.SourceURL = NewSourceID(), out.PaperID, externalID, normalized
	_, err = tx.Exec(ctx, `INSERT INTO paper_sources
		(source_id, paper_id, origin, sha256, objstore_key, size_bytes, source_url, retrieved_url, retrieved_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (paper_id, origin, sha256) WHERE source_url IS NOT NULL DO NOTHING`,
		src.SourceID, src.PaperID, src.Origin, src.Sha256, src.ObjstoreKey, src.SizeBytes, src.SourceURL, src.RetrievedURL, src.RetrievedAt)
	if err != nil {
		return out, catalogUnavailable("registry: append external source", err)
	}
	out.Source, err = scanPaperSource(tx.QueryRow(ctx, `SELECT source_id, paper_id, origin, sha256, objstore_key, size_bytes,
		created_at, coalesce(source_url,''), coalesce(retrieved_url,''), retrieved_at
		FROM paper_sources WHERE paper_id=$1 AND origin=$2 AND sha256=$3 AND source_url IS NOT NULL`, out.PaperID, externalID, src.Sha256))
	if err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, catalogUnavailable("registry: commit external source", err)
	}
	out.ExternalID = externalID
	return out, nil
}
